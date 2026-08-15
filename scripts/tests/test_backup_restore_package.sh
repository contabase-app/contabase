#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BACKUP_SCRIPT="$REPO_DIR/scripts/ops/backup.sh"
RESTORE_SCRIPT="$REPO_DIR/scripts/ops/restore.sh"
REAL_SQLITE="$(command -v sqlite3)"
REAL_CP="$(command -v cp)"
REAL_MV="$(command -v mv)"
REAL_CHOWN="$(command -v chown)"
REAL_CHMOD="$(command -v chmod)"
REAL_STAT="$(command -v stat)"
REAL_LN="$(command -v ln)"

[ "$(id -u)" -ne 0 ] || {
  echo "erro: a matriz canonica deve executar como usuario nao-root" >&2
  exit 1
}

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/contabase-br-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

SCENARIOS=0
ASSERTIONS=0
FAILED_ASSERTIONS=0
EXPECTED_SCENARIOS=41
EXPECTED_ASSERTIONS=149

scenario() {
  SCENARIOS=$((SCENARIOS + 1))
  printf '\nSCENARIO %02d: %s\n' "$SCENARIOS" "$1"
}

assert_true() {
  local label="$1"
  shift
  ASSERTIONS=$((ASSERTIONS + 1))
  if "$@"; then
    printf '  PASS: %s\n' "$label"
  else
    printf '  FAIL: %s\n' "$label" >&2
    FAILED_ASSERTIONS=$((FAILED_ASSERTIONS + 1))
  fi
}

assert_eq() {
  local label="$1" expected="$2" actual="$3"
  ASSERTIONS=$((ASSERTIONS + 1))
  if [ "$expected" = "$actual" ]; then
    printf '  PASS: %s\n' "$label"
  else
    printf '  FAIL: %s (esperado=%q obtido=%q)\n' "$label" "$expected" "$actual" >&2
    FAILED_ASSERTIONS=$((FAILED_ASSERTIONS + 1))
  fi
}

assert_contains() {
  local label="$1" needle="$2" file="$3"
  ASSERTIONS=$((ASSERTIONS + 1))
  if grep -Fq "$needle" "$file"; then
    printf '  PASS: %s\n' "$label"
  else
    printf '  FAIL: %s (diagnostico ausente: %s)\n' "$label" "$needle" >&2
    FAILED_ASSERTIONS=$((FAILED_ASSERTIONS + 1))
  fi
}

assert_not_contains() {
  local label="$1" needle="$2" file="$3"
  ASSERTIONS=$((ASSERTIONS + 1))
  if grep -Fq "$needle" "$file"; then
    printf '  FAIL: %s (conteudo inesperado: %s)\n' "$label" "$needle" >&2
    FAILED_ASSERTIONS=$((FAILED_ASSERTIONS + 1))
  else
    printf '  PASS: %s\n' "$label"
  fi
}

new_dir() {
  local dir="$TEST_ROOT/$1"
  mkdir -p "$dir"
  printf '%s\n' "$dir"
}

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

test_stat_value() {
  local kind="$1" bsd_format="$2" gnu_format="$3" path="$4" value
  if value="$(stat -f "$bsd_format" "$path" 2>/dev/null)"; then
    case "$kind:$value" in decimal:|decimal:*[!0-9]*|octal:|octal:*[!0-7]*) return 1 ;; esac
    printf '%s\n' "$value"
    return 0
  fi
  if value="$(stat -c "$gnu_format" "$path" 2>/dev/null)"; then
    case "$kind:$value" in decimal:|decimal:*[!0-9]*|octal:|octal:*[!0-7]*) return 1 ;; esac
    printf '%s\n' "$value"
    return 0
  fi
  return 1
}

file_size() { test_stat_value decimal '%z' '%s' "$1"; }
file_mode() { test_stat_value octal '%Lp' '%a' "$1"; }
file_uid() { test_stat_value decimal '%u' '%u' "$1"; }
file_gid() { test_stat_value decimal '%g' '%g' "$1"; }
file_nlink() { test_stat_value decimal '%l' '%h' "$1"; }

is_decimal() { case "$1" in ''|*[!0-9]*) return 1 ;; *) return 0 ;; esac; }
is_octal() { case "$1" in ''|*[!0-7]*) return 1 ;; *) return 0 ;; esac; }

init_test_db() {
  local db="$1" marker="${2:-source}"
  sqlite3 "$db" "
    CREATE TABLE schema_migrations(version INTEGER NOT NULL);
    INSERT INTO schema_migrations VALUES(54);
    CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT NOT NULL);
    INSERT INTO t VALUES(1,'$marker'),(2,'second');
  "
}

init_crash_wal_db() {
  local db="$1" marker="$2" fifo pid
  fifo="$TEST_ROOT/wal-$RANDOM.fifo"
  mkfifo "$fifo"
  sqlite3 "$db" < "$fifo" >/dev/null 2>&1 &
  pid=$!
  exec 7> "$fifo"
  printf '%s\n' "PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE schema_migrations(version INTEGER NOT NULL); INSERT INTO schema_migrations VALUES(54); CREATE TABLE t(id INTEGER PRIMARY KEY,v TEXT NOT NULL); BEGIN; INSERT INTO t VALUES(1,'$marker'),(2,'second'); COMMIT;" >&7
  for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$db-wal" ] && break; sleep 0.1; done
  [ -s "$db-wal" ] && [ -s "$db-shm" ]
  kill -9 "$pid"
  exec 7>&-
  wait "$pid" 2>/dev/null || :
  rm "$fifo"
}

make_package() {
  local name="$1" root data dest pkg
  root="$(new_dir "$name")"
  data="$root/data"
  dest="$root/dest"
  mkdir -p "$data/uploads" "$dest"
  init_test_db "$data/contabase.db" "$name"
  printf 'upload-%s' "$name" > "$data/uploads/file.txt"
  APP_VERSION=v-test DATA_DIR="$data" DB_FILE="$data/contabase.db" UPLOADS_DIR="$data/uploads" \
    "$BACKUP_SCRIPT" "$dest" > "$root/backup.out"
  pkg="$(sed -n '1p' "$root/backup.out")"
  [ -d "$pkg" ]
  printf '%s\n' "$pkg"
}

clone_package() {
  local source="$1" name="$2" target
  target="$(new_dir "$name")/package"
  cp -pR "$source" "$target"
  printf '%s\n' "$target"
}

run_restore() {
  local package="$1" target="$2" log="$3"
  mkdir -p "$target"
  CONFIRM_APP_STOPPED=yes DATA_DIR="$target" DB_FILE="$target/contabase.db" UPLOADS_DIR="$target/uploads" \
    "$RESTORE_SCRIPT" "$package" > "$log.out" 2> "$log.err"
}

replace_manifest_line() {
  local manifest="$1" key="$2" replacement="$3" tmp
  tmp="${manifest}.tmp"
  awk -v key="$key" -v replacement="$replacement" 'index($0,key "=")==1 {$0=replacement} {print}' "$manifest" > "$tmp"
  mv "$tmp" "$manifest"
}

remove_manifest_key() {
  local manifest="$1" key="$2" tmp
  tmp="${manifest}.tmp"
  awk -v key="$key" 'index($0,key "=")!=1 {print}' "$manifest" > "$tmp"
  mv "$tmp" "$manifest"
}

refresh_db_metadata() {
  local pkg="$1" db_sha db_size tmp
  db_sha="$(sha_file "$pkg/database.sqlite")"
  db_size="$(file_size "$pkg/database.sqlite")"
  replace_manifest_line "$pkg/manifest.txt" database_sha256 "database_sha256=$db_sha"
  replace_manifest_line "$pkg/manifest.txt" database_size "database_size=$db_size"
  tmp="$pkg/checksums.sha256.tmp"
  awk -v sha="$db_sha" '$2=="database.sqlite" {$1=sha} {print $1 "  " $2}' "$pkg/checksums.sha256" > "$tmp"
  mv "$tmp" "$pkg/checksums.sha256"
}

fingerprint_package() {
  local pkg="$1" output="$2" path rel type size digest mode uid gid list_nul list
  list_nul="${output}.list.nul"
  list="${output}.list"
  : > "$output"
  find "$pkg" -mindepth 1 -print0 > "$list_nul"
  : > "$list"
  while IFS= read -r -d '' path; do
    case "$path" in *$'\n'*|*$'\r'*|*$'\t'*) return 1 ;; esac
    printf '%s\n' "$path" >> "$list"
  done < "$list_nul"
  LC_ALL=C sort -o "$list" "$list"
  while IFS= read -r path; do
    rel="${path#"$pkg/"}"
    if [ -L "$path" ]; then type=symlink
    elif [ -f "$path" ]; then type=file
    elif [ -d "$path" ]; then type=directory
    else type=other
    fi
    mode="$(file_mode "$path")"; uid="$(file_uid "$path")"; gid="$(file_gid "$path")"
    if [ "$type" = file ]; then size="$(file_size "$path")"; digest="$(sha_file "$path")"; else size=-; digest=-; fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$rel" "$type" "$size" "$digest" "$mode" "$uid" "$gid" >> "$output"
  done < "$list"
  rm "$list_nul" "$list"
}

make_sqlite_shim() {
  local dir="$1"
  cat > "$dir/sqlite3" <<'SHIM'
#!/usr/bin/env bash
if [ "${CB_SQLITE_FAIL_INTEGRITY:-0}" = 1 ] && [[ "$*" == *"PRAGMA integrity_check"* ]]; then
  echo corrupt
  exit 0
fi
if [ "${CB_SQLITE_FAIL_FK:-0}" = 1 ] && [[ "$*" == *"PRAGMA foreign_key_check"* ]]; then
  echo 'child|1|parent|0'
  exit 0
fi
exec "$CB_REAL_SQLITE" "$@"
SHIM
  chmod +x "$dir/sqlite3"
}

make_cp_shim() {
  local dir="$1"
  cat > "$dir/cp" <<'SHIM'
#!/usr/bin/env bash
if [ "${CB_CP_FAIL_UPLOADS:-0}" = 1 ] && [[ "$*" == *"uploads/."* ]] && [[ "$*" == *".backup-partial."* ]]; then
  echo 'SIMULATED UPLOAD COPY FAILURE' >&2
  exit 91
fi
exec "$CB_REAL_CP" "$@"
SHIM
  chmod +x "$dir/cp"
}

make_staging_shims() {
  local dir="$1"
  cat > "$dir/cp" <<'SHIM'
#!/usr/bin/env bash
file_sha() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}
previous=""
current=""
for argument in "$@"; do
  previous="$current"
  current="$argument"
done

if [ "${CB_CP_MODE:-}" = database ] && [ "$previous" = "$CB_PACKAGE_DB" ] && [[ "$current" == */.restore-stage.*/database.sqlite ]]; then
  "$CB_REAL_CP" "$@" || exit $?
  printf 'tampered-database-in-staging' >> "$current"
  printf 'database source_sha=%s staged_sha=%s source=%s staging=%s\n' \
    "$(file_sha "$previous")" "$(file_sha "$current")" "$previous" "$current" > "$CB_CP_PROOF"
  exit 0
fi

if { [ "${CB_CP_MODE:-}" = upload ] || [ "${CB_CP_MODE:-}" = symlink ]; } && \
   [ "$previous" = "$CB_PACKAGE_UPLOADS/." ] && [[ "$current" == */.restore-stage.*/uploads/ ]]; then
  "$CB_REAL_CP" "$@" || exit $?
  if [ "$CB_CP_MODE" = upload ]; then
    source_file="$CB_PACKAGE_UPLOADS/file.txt"
    staged_file="${current%/}/file.txt"
    printf 'tampered-upload-in-staging' > "$staged_file"
    printf 'upload source_sha=%s staged_sha=%s source=%s staging=%s\n' \
      "$(file_sha "$source_file")" "$(file_sha "$staged_file")" "$source_file" "$staged_file" > "$CB_CP_PROOF"
  else
    staged_link="${current%/}/injected-link"
    "$CB_REAL_LN" -s file.txt "$staged_link" || exit $?
    [ -L "$staged_link" ] || exit 98
    printf 'symlink path=%s\n' "$staged_link" > "$CB_CP_PROOF"
  fi
  exit 0
fi

exec "$CB_REAL_CP" "$@"
SHIM
  cat > "$dir/mv" <<'SHIM'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$CB_MV_AUDIT_LOG"
exec "$CB_REAL_MV" "$@"
SHIM
  chmod +x "$dir/cp" "$dir/mv"
}

make_mv_shim() {
  local dir="$1"
  cat > "$dir/mv" <<'SHIM'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$CB_MV_LOG"
source_path="${1:-}"
target_path="${2:-}"
kind="${CB_MV_FAIL_KIND:-none}"
if { [ "$kind" = install_db ] || [ "$kind" = install_db_rollback_db ]; } && \
   [[ "$source_path" == *"/.restore-stage."*"/database.sqlite" ]] && [ "$target_path" = "$CB_TARGET_DB" ]; then
  exit 92
fi
if [ "$kind" = install_uploads ] && [[ "$source_path" == *"/.restore-stage."*"/uploads" ]] && [ "$target_path" = "$CB_TARGET_UPLOADS" ]; then
  exit 93
fi
if { [ "$kind" = rollback_db ] || [ "$kind" = install_db_rollback_db ]; } && \
   [[ "$source_path" == *"/.restore-rollback."*"/old-database.sqlite" ]] && [ "$target_path" = "$CB_TARGET_DB" ]; then
  exit 94
fi
exec "$CB_REAL_MV" "$@"
SHIM
  chmod +x "$dir/mv"
}

make_metadata_shims() {
  local dir="$1"
  cat > "$dir/chmod" <<'SHIM'
#!/usr/bin/env bash
printf 'chmod %s\n' "$*" >> "$CB_METADATA_LOG"
if [ -n "${CB_CHMOD_FAIL_PATH:-}" ] && [ "${*: -1}" = "$CB_CHMOD_FAIL_PATH" ]; then exit 96; fi
exec "$CB_REAL_CHMOD" "$@"
SHIM
  cat > "$dir/chown" <<'SHIM'
#!/usr/bin/env bash
printf 'chown %s\n' "$*" >> "$CB_METADATA_LOG"
if [ -n "${CB_CHOWN_FAIL_PATH:-}" ] && [ "${*: -1}" = "$CB_CHOWN_FAIL_PATH" ]; then exit 95; fi
exec "$CB_REAL_CHOWN" "$@"
SHIM
  chmod +x "$dir/chmod" "$dir/chown"
}

make_stat_shim() {
  local dir="$1"
  cat > "$dir/stat" <<'SHIM'
#!/usr/bin/env bash
set -u

if [ -n "${CB_STAT_AUDIT_LOG:-}" ]; then
  printf 'call:' >> "$CB_STAT_AUDIT_LOG"
  printf ' %s' "$@" >> "$CB_STAT_AUDIT_LOG"
  printf '\n' >> "$CB_STAT_AUDIT_LOG"
fi

native_stat() {
  local format="${2:-}" path="${3:-}" translated
  if "$CB_REAL_STAT" -c '%s' "$path" >/dev/null 2>&1; then
    exec "$CB_REAL_STAT" "$@"
  fi
  case "$format" in
    %s) translated=%z ;;
    %a) translated=%Lp ;;
    %u) translated=%u ;;
    %g) translated=%g ;;
    %h) translated=%l ;;
    *) exit 96 ;;
  esac
  exec "$CB_REAL_STAT" -f "$translated" "$path"
}

case "$CB_STAT_SHIM_MODE" in
  contaminate-first)
    if [ "${1:-}" = -f ]; then
      printf '%s\n' 'PARTIAL-STAT-STDOUT'
      printf '%s\n' 'PARTIAL-STAT-STDERR' >&2
      exit 97
    fi
    [ "${1:-}" = -c ] || exit 96
    native_stat "$@"
    ;;
  fail-all)
    printf '%s\n' 'PARTIAL-STAT-STDOUT'
    printf '%s\n' 'PARTIAL-STAT-STDERR' >&2
    exit 98
    ;;
  invalid-success)
    if [ "${1:-}" = -f ]; then
      printf '%s\n' '12invalid'
      exit 0
    fi
    [ "${1:-}" = -c ] || exit 96
    native_stat "$@"
    ;;
  *) exit 96 ;;
esac
SHIM
  chmod +x "$dir/stat"
}

# Portabilidade de stat e falhas de leitura devem ser exercitadas antes da
# matriz historica de manifest, staging e rollback.
scenario "banco ausente bloqueia backup sem pacote parcial"
missing_db_root="$(new_dir 'missing database')"; mkdir -p "$missing_db_root/data/uploads" "$missing_db_root/dest"
missing_db_rc=0
APP_VERSION=v-test DATA_DIR="$missing_db_root/data" DB_FILE="$missing_db_root/data/missing database.sqlite" UPLOADS_DIR="$missing_db_root/data/uploads" "$BACKUP_SCRIPT" "$missing_db_root/dest" > "$missing_db_root/out" 2> "$missing_db_root/err" || missing_db_rc=$?
assert_true "banco ausente retornou erro" test "$missing_db_rc" -ne 0
assert_contains "diagnostico identifica banco ausente" "banco nao encontrado" "$missing_db_root/err"
assert_eq "banco ausente nao finalizou pacote" 0 "$(find "$missing_db_root/dest" -maxdepth 1 -type d -name 'backup-*' | wc -l | tr -d ' ')"

scenario "fallback GNU isolado descarta stdout parcial da tentativa BSD"
portable_root="$(new_dir 'portable stat with spaces')"; mkdir -p "$portable_root/data/uploads" "$portable_root/dest" "$portable_root/shim" "$portable_root/target/uploads"
init_test_db "$portable_root/data/contabase.db" portable-source
printf portable-upload > "$portable_root/data/uploads/file with space.txt"
init_test_db "$portable_root/target/contabase.db" portable-old
printf old-upload > "$portable_root/target/uploads/old.txt"
make_stat_shim "$portable_root/shim"
: > "$portable_root/stat.audit"
portable_backup_rc=0
PATH="$portable_root/shim:$PATH" CB_REAL_STAT="$REAL_STAT" CB_STAT_SHIM_MODE=contaminate-first CB_STAT_AUDIT_LOG="$portable_root/stat.audit" APP_VERSION=v-test DATA_DIR="$portable_root/data" DB_FILE="$portable_root/data/contabase.db" UPLOADS_DIR="$portable_root/data/uploads" "$BACKUP_SCRIPT" "$portable_root/dest" > "$portable_root/backup.out" 2> "$portable_root/backup.err" || portable_backup_rc=$?
portable_pkg="$(sed -n '1p' "$portable_root/backup.out")"
portable_manifest_size="$(sed -n 's/^database_size=//p' "$portable_pkg/manifest.txt")"
assert_eq "backup com primeira tentativa contaminada passou" 0 "$portable_backup_rc"
assert_not_contains "stdout parcial nao vazou para output do backup" PARTIAL-STAT "$portable_root/backup.out"
assert_true "pacote portavel foi finalizado" test -d "$portable_pkg"
assert_true "database_size do manifest e decimal estrito" is_decimal "$portable_manifest_size"
assert_eq "database_size corresponde ao arquivo logico" "$(file_size "$portable_pkg/database.sqlite")" "$portable_manifest_size"
portable_restore_rc=0
PATH="$portable_root/shim:$PATH" CB_REAL_STAT="$REAL_STAT" CB_STAT_SHIM_MODE=contaminate-first CB_STAT_AUDIT_LOG="$portable_root/stat.audit" run_restore "$portable_pkg" "$portable_root/target" "$portable_root/restore" || portable_restore_rc=$?
assert_eq "restore com primeira tentativa contaminada passou" 0 "$portable_restore_rc"
assert_eq "DB restaurado preserva payload" portable-source "$(sqlite3 "$portable_root/target/contabase.db" 'SELECT v FROM t WHERE id=1;')"
assert_true "path de upload com espaco foi restaurado" test -f "$portable_root/target/uploads/file with space.txt"
assert_not_contains "stdout parcial nao contaminou snapshot do restore" PARTIAL-STAT "$portable_root/restore.err"
assert_true "size restaurado e decimal" is_decimal "$(file_size "$portable_root/target/contabase.db")"
assert_true "mode restaurado e octal" is_octal "$(file_mode "$portable_root/target/contabase.db")"
assert_true "uid restaurado e decimal" is_decimal "$(file_uid "$portable_root/target/contabase.db")"
assert_true "gid restaurado e decimal" is_decimal "$(file_gid "$portable_root/target/contabase.db")"
assert_eq "nlink restaurado e unitario" 1 "$(file_nlink "$portable_root/target/contabase.db")"
assert_contains "fallback de nlink GNU foi exercitado" "call: -c %h" "$portable_root/stat.audit"

scenario "saida bem-sucedida nao numerica falha sem fallback permissivo"
invalid_root="$(new_dir invalid-stat)"; mkdir -p "$invalid_root/data/uploads" "$invalid_root/dest" "$invalid_root/shim"; init_test_db "$invalid_root/data/contabase.db" invalid
make_stat_shim "$invalid_root/shim"; : > "$invalid_root/stat.audit"; invalid_rc=0
PATH="$invalid_root/shim:$PATH" CB_REAL_STAT="$REAL_STAT" CB_STAT_SHIM_MODE=invalid-success CB_STAT_AUDIT_LOG="$invalid_root/stat.audit" APP_VERSION=v-test DATA_DIR="$invalid_root/data" DB_FILE="$invalid_root/data/contabase.db" UPLOADS_DIR="$invalid_root/data/uploads" "$BACKUP_SCRIPT" "$invalid_root/dest" > "$invalid_root/out" 2> "$invalid_root/err" || invalid_rc=$?
assert_true "saida nao numerica retornou erro" test "$invalid_rc" -ne 0
assert_contains "saida nao numerica tem diagnostico deterministico" "nao foi possivel obter tamanho do banco" "$invalid_root/err"
assert_eq "saida nao numerica nao finalizou pacote" 0 "$(find "$invalid_root/dest" -maxdepth 1 -type d -name 'backup-*' | wc -l | tr -d ' ')"
assert_not_contains "sucesso invalido nao foi mascarado por fallback" "call: -c" "$invalid_root/stat.audit"

scenario "ambas variantes de stat falham antes de qualquer mutacao"
fail_stat_root="$(new_dir fail-stat)"; mkdir -p "$fail_stat_root/data/uploads" "$fail_stat_root/dest" "$fail_stat_root/shim" "$fail_stat_root/target/uploads"; init_test_db "$fail_stat_root/data/contabase.db" fail-stat
make_stat_shim "$fail_stat_root/shim"; fail_stat_backup_rc=0
PATH="$fail_stat_root/shim:$PATH" CB_REAL_STAT="$REAL_STAT" CB_STAT_SHIM_MODE=fail-all APP_VERSION=v-test DATA_DIR="$fail_stat_root/data" DB_FILE="$fail_stat_root/data/contabase.db" UPLOADS_DIR="$fail_stat_root/data/uploads" "$BACKUP_SCRIPT" "$fail_stat_root/dest" > "$fail_stat_root/backup.out" 2> "$fail_stat_root/backup.err" || fail_stat_backup_rc=$?
assert_true "falha dupla de stat bloqueou backup" test "$fail_stat_backup_rc" -ne 0
assert_contains "backup reportou falha de medicao" "nao foi possivel obter tamanho do banco" "$fail_stat_root/backup.err"
assert_eq "falha dupla nao finalizou backup" 0 "$(find "$fail_stat_root/dest" -maxdepth 1 -type d -name 'backup-*' | wc -l | tr -d ' ')"
assert_true "backup falho nao imprimiu caminho final" test ! -s "$fail_stat_root/backup.out"
init_test_db "$fail_stat_root/target/contabase.db" fail-stat-old; printf old-upload > "$fail_stat_root/target/uploads/old.txt"; fail_stat_restore_rc=0
PATH="$fail_stat_root/shim:$PATH" CB_REAL_STAT="$REAL_STAT" CB_STAT_SHIM_MODE=fail-all run_restore "$portable_pkg" "$fail_stat_root/target" "$fail_stat_root/restore" || fail_stat_restore_rc=$?
assert_true "falha dupla de stat bloqueou restore" test "$fail_stat_restore_rc" -ne 0
assert_contains "restore reportou metadado indisponivel" "nao foi possivel obter metadado mode" "$fail_stat_root/restore.err"
assert_eq "restore falho preservou DB alvo" fail-stat-old "$(sqlite3 "$fail_stat_root/target/contabase.db" 'SELECT v FROM t WHERE id=1;')"
assert_eq "restore falho preservou upload alvo" old-upload "$(cat "$fail_stat_root/target/uploads/old.txt")"
assert_true "restore falho nao imprimiu sucesso" test ! -s "$fail_stat_root/restore.out"

scenario "permission denied real como usuario nao-root permanece fail-closed"
permission_root="$(new_dir permission-denied)"; mkdir -p "$permission_root/data/uploads" "$permission_root/dest"; init_test_db "$permission_root/data/contabase.db" denied; chmod 000 "$permission_root/data/contabase.db"; permission_rc=0
APP_VERSION=v-test DATA_DIR="$permission_root/data" DB_FILE="$permission_root/data/contabase.db" UPLOADS_DIR="$permission_root/data/uploads" "$BACKUP_SCRIPT" "$permission_root/dest" > "$permission_root/out" 2> "$permission_root/err" || permission_rc=$?
chmod 600 "$permission_root/data/contabase.db"
assert_true "permission denied retornou erro" test "$permission_rc" -ne 0
assert_contains "permission denied bloqueou sqlite backup" "sqlite3 .backup falhou" "$permission_root/err"
assert_eq "permission denied nao finalizou pacote" 0 "$(find "$permission_root/dest" -maxdepth 1 -type d -name 'backup-*' | wc -l | tr -d ' ')"
assert_true "permission denied nao imprimiu caminho final" test ! -s "$permission_root/out"

# A. WAL realmente ativo durante o backup.
scenario "backup valido com WAL realmente ativo"
wal_root="$(new_dir wal-real)"; mkdir -p "$wal_root/data/uploads" "$wal_root/dest"
printf upload-wal > "$wal_root/data/uploads/file.txt"
wal_db="$wal_root/data/contabase.db"; wal_fifo="$wal_root/sql.fifo"; mkfifo "$wal_fifo"
sqlite3 "$wal_db" < "$wal_fifo" > "$wal_root/sql.out" 2> "$wal_root/sql.err" & wal_pid=$!
exec 9> "$wal_fifo"
printf '%s\n' "PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE schema_migrations(version INTEGER NOT NULL); INSERT INTO schema_migrations VALUES(54); CREATE TABLE t(id INTEGER PRIMARY KEY,v TEXT); BEGIN; INSERT INTO t VALUES(1,'wal-committed'); COMMIT;" >&9
for _ in 1 2 3 4 5 6 7 8 9 10; do [ -s "$wal_db-wal" ] && break; sleep 0.1; done
assert_true "WAL sidecar existia e tinha conteudo" test -s "$wal_db-wal"
APP_VERSION=v-test DATA_DIR="$wal_root/data" DB_FILE="$wal_db" UPLOADS_DIR="$wal_root/data/uploads" "$BACKUP_SCRIPT" "$wal_root/dest" > "$wal_root/backup.out"
wal_pkg="$(sed -n '1p' "$wal_root/backup.out")"
assert_eq "registro confirmado entrou no database.sqlite" wal-committed "$(sqlite3 "$wal_pkg/database.sqlite" 'SELECT v FROM t WHERE id=1;')"
assert_true "WAL nao vazou para o pacote" test ! -e "$wal_pkg/database.sqlite-wal"
assert_true "SHM nao vazou para o pacote" test ! -e "$wal_pkg/database.sqlite-shm"
exec 9>&-; wait "$wal_pid"
base_pkg="$wal_pkg"

# B. Backups no mesmo segundo nao colidem.
scenario "dois backups no mesmo segundo sem colisao"
same_root="$(new_dir same-second)"; mkdir -p "$same_root/data/uploads" "$same_root/dest"; init_test_db "$same_root/data/contabase.db" same
APP_VERSION=v-test DATA_DIR="$same_root/data" DB_FILE="$same_root/data/contabase.db" UPLOADS_DIR="$same_root/data/uploads" "$BACKUP_SCRIPT" "$same_root/dest" > "$same_root/one"
APP_VERSION=v-test DATA_DIR="$same_root/data" DB_FILE="$same_root/data/contabase.db" UPLOADS_DIR="$same_root/data/uploads" "$BACKUP_SCRIPT" "$same_root/dest" > "$same_root/two"
assert_true "nomes finais sao distintos" test "$(cat "$same_root/one")" != "$(cat "$same_root/two")"
assert_eq "dois pacotes finais existem" 2 "$(find "$same_root/dest" -maxdepth 1 -type d -name 'backup-*' | wc -l | tr -d ' ')"

# C. Falha real de copia de upload nao finaliza pacote.
scenario "falha de copia de upload nao produz pacote final"
copy_root="$(new_dir copy-fail)"; mkdir -p "$copy_root/data/uploads" "$copy_root/dest" "$copy_root/shim"; init_test_db "$copy_root/data/contabase.db" copy; printf x > "$copy_root/data/uploads/x"
make_cp_shim "$copy_root/shim"; copy_rc=0
PATH="$copy_root/shim:$PATH" CB_REAL_CP="$REAL_CP" CB_CP_FAIL_UPLOADS=1 APP_VERSION=v-test DATA_DIR="$copy_root/data" DB_FILE="$copy_root/data/contabase.db" UPLOADS_DIR="$copy_root/data/uploads" "$BACKUP_SCRIPT" "$copy_root/dest" > "$copy_root/out" 2> "$copy_root/err" || copy_rc=$?
assert_true "backup falhou na copia" test "$copy_rc" -ne 0
assert_contains "diagnostico identifica copia" "falha ao copiar uploads" "$copy_root/err"
assert_eq "nenhum pacote final foi criado" 0 "$(find "$copy_root/dest" -maxdepth 1 -type d -name 'backup-*' | wc -l | tr -d ' ')"

# D-E. Checks SQLite do backup exercitados por shim seletivo.
scenario "integrity_check invalido bloqueia backup"
int_root="$(new_dir integrity-fail)"; mkdir -p "$int_root/data/uploads" "$int_root/dest" "$int_root/shim"; init_test_db "$int_root/data/contabase.db" int; make_sqlite_shim "$int_root/shim"; int_rc=0
PATH="$int_root/shim:$PATH" CB_REAL_SQLITE="$REAL_SQLITE" CB_SQLITE_FAIL_INTEGRITY=1 APP_VERSION=v-test DATA_DIR="$int_root/data" DB_FILE="$int_root/data/contabase.db" UPLOADS_DIR="$int_root/data/uploads" "$BACKUP_SCRIPT" "$int_root/dest" > "$int_root/out" 2> "$int_root/err" || int_rc=$?
assert_true "integrity_check bloqueou" test "$int_rc" -ne 0
assert_contains "diagnostico de integridade" integrity_check "$int_root/err"

scenario "foreign_key_check invalido bloqueia backup"
fk_b_root="$(new_dir fk-backup-fail)"; mkdir -p "$fk_b_root/data/uploads" "$fk_b_root/dest" "$fk_b_root/shim"; init_test_db "$fk_b_root/data/contabase.db" fk; make_sqlite_shim "$fk_b_root/shim"; fk_b_rc=0
PATH="$fk_b_root/shim:$PATH" CB_REAL_SQLITE="$REAL_SQLITE" CB_SQLITE_FAIL_FK=1 APP_VERSION=v-test DATA_DIR="$fk_b_root/data" DB_FILE="$fk_b_root/data/contabase.db" UPLOADS_DIR="$fk_b_root/data/uploads" "$BACKUP_SCRIPT" "$fk_b_root/dest" > "$fk_b_root/out" 2> "$fk_b_root/err" || fk_b_rc=$?
assert_true "foreign_key_check bloqueou" test "$fk_b_rc" -ne 0
assert_contains "diagnostico de FK" foreign_key_check "$fk_b_root/err"

# F. Contrato exato do manifest.
scenario "manifest com exatamente 15 chaves validas"
assert_eq "manifest possui 15 linhas" 15 "$(wc -l < "$base_pkg/manifest.txt" | tr -d ' ')"
assert_eq "chaves exatas e unicas" "application_version backup_id created_at_utc database_file database_sha256 database_size foreign_key_check format_version integrity_check method schema_version source_db_path uploads_directory uploads_file_count wal_consistency_method" "$(cut -d= -f1 "$base_pkg/manifest.txt" | LC_ALL=C sort | tr '\n' ' ' | sed 's/ $//')"

# G-I. Ausente, desconhecida e duplicada com diagnosticos independentes.
scenario "chave obrigatoria ausente"
pkg_missing="$(clone_package "$base_pkg" manifest-missing)"; remove_manifest_key "$pkg_missing/manifest.txt" database_sha256; missing_target="$(new_dir manifest-missing-target)"; missing_rc=0
run_restore "$pkg_missing" "$missing_target" "$missing_target/log" || missing_rc=$?
assert_true "manifest sem chave bloqueou" test "$missing_rc" -ne 0
assert_contains "diagnostico nao depende de arquivo lateral" "chave obrigatoria ausente no manifest: database_sha256" "$missing_target/log.err"
assert_true "nenhum .bak foi criado" test ! -e "$pkg_missing/manifest.txt.bak"

scenario "chave desconhecida"
pkg_unknown="$(clone_package "$base_pkg" manifest-unknown)"; printf 'surpresa=1\n' >> "$pkg_unknown/manifest.txt"; unknown_target="$(new_dir manifest-unknown-target)"; unknown_rc=0
run_restore "$pkg_unknown" "$unknown_target" "$unknown_target/log" || unknown_rc=$?
assert_true "chave desconhecida bloqueou" test "$unknown_rc" -ne 0
assert_contains "diagnostico de chave desconhecida" "chave desconhecida no manifest: surpresa" "$unknown_target/log.err"

scenario "chave duplicada"
pkg_dupkey="$(clone_package "$base_pkg" manifest-duplicate)"; printf 'database_sha256=%s\n' "$(sha_file "$pkg_dupkey/database.sqlite")" >> "$pkg_dupkey/manifest.txt"; dupkey_target="$(new_dir manifest-duplicate-target)"; dupkey_rc=0
run_restore "$pkg_dupkey" "$dupkey_target" "$dupkey_target/log" || dupkey_rc=$?
assert_true "chave duplicada bloqueou" test "$dupkey_rc" -ne 0
assert_contains "diagnostico de duplicata" "chave duplicada no manifest: database_sha256" "$dupkey_target/log.err"

# J-K. Versao e schema fail-closed no backup.
scenario "application_version desconhecida bloqueia backup"
ver_root="$(new_dir version-unknown)"; mkdir -p "$ver_root/data/uploads" "$ver_root/dest"; init_test_db "$ver_root/data/contabase.db" ver; ver_rc=0
APP_VERSION=unknown DATA_DIR="$ver_root/data" DB_FILE="$ver_root/data/contabase.db" UPLOADS_DIR="$ver_root/data/uploads" "$BACKUP_SCRIPT" "$ver_root/dest" > "$ver_root/out" 2> "$ver_root/err" || ver_rc=$?
assert_true "versao unknown bloqueou" test "$ver_rc" -ne 0
assert_contains "diagnostico da versao" "versao da aplicacao desconhecida" "$ver_root/err"

scenario "schema nao comprovado bloqueia backup"
schema_root="$(new_dir schema-missing)"; mkdir -p "$schema_root/data/uploads" "$schema_root/dest"; sqlite3 "$schema_root/data/contabase.db" 'CREATE TABLE t(id INTEGER);'; schema_rc=0
APP_VERSION=v-test DATA_DIR="$schema_root/data" DB_FILE="$schema_root/data/contabase.db" UPLOADS_DIR="$schema_root/data/uploads" "$BACKUP_SCRIPT" "$schema_root/dest" > "$schema_root/out" 2> "$schema_root/err" || schema_rc=$?
assert_true "schema ausente bloqueou" test "$schema_rc" -ne 0
assert_contains "diagnostico do schema" "nao foi possivel consultar schema_version" "$schema_root/err"

# L-S. Parser de checksum e igualdade exata do inventario.
scenario "checksum do DB divergente"
pkg_dbsha="$(clone_package "$base_pkg" checksum-db)"; printf x >> "$pkg_dbsha/database.sqlite"; dbsha_target="$(new_dir checksum-db-target)"; dbsha_rc=0
run_restore "$pkg_dbsha" "$dbsha_target" "$dbsha_target/log" || dbsha_rc=$?
assert_true "DB divergente bloqueou" test "$dbsha_rc" -ne 0
assert_contains "diagnostico aponta checksum" "checksum divergente: database.sqlite" "$dbsha_target/log.err"

scenario "checksum de upload divergente"
pkg_upsha="$(clone_package "$base_pkg" checksum-upload)"; printf x >> "$pkg_upsha/uploads/file.txt"; upsha_target="$(new_dir checksum-upload-target)"; upsha_rc=0
run_restore "$pkg_upsha" "$upsha_target" "$upsha_target/log" || upsha_rc=$?
assert_true "upload divergente bloqueou" test "$upsha_rc" -ne 0
assert_contains "diagnostico aponta upload" "checksum divergente: uploads/file.txt" "$upsha_target/log.err"

scenario "entrada duplicada no checksums"
pkg_dupsum="$(clone_package "$base_pkg" checksum-duplicate)"; duplicate_checksum_line="$(grep 'database.sqlite$' "$pkg_dupsum/checksums.sha256")"; printf '%s\n' "$duplicate_checksum_line" >> "$pkg_dupsum/checksums.sha256"; dupsum_target="$(new_dir checksum-duplicate-target)"; dupsum_rc=0
run_restore "$pkg_dupsum" "$dupsum_target" "$dupsum_target/log" || dupsum_rc=$?
assert_true "duplicata bloqueou" test "$dupsum_rc" -ne 0
assert_contains "diagnostico da entrada duplicada" "entrada duplicada no checksums: database.sqlite" "$dupsum_target/log.err"

scenario "arquivo real nao listado"
pkg_unlisted="$(clone_package "$base_pkg" real-unlisted)"; printf extra > "$pkg_unlisted/uploads/extra.txt"; unlisted_target="$(new_dir real-unlisted-target)"; unlisted_rc=0
run_restore "$pkg_unlisted" "$unlisted_target" "$unlisted_target/log" || unlisted_rc=$?
assert_true "arquivo extra bloqueou" test "$unlisted_rc" -ne 0
assert_contains "diagnostico de igualdade do inventario" "inventario do checksum difere do conteudo real" "$unlisted_target/log.err"

scenario "arquivo listado ausente"
pkg_absent="$(clone_package "$base_pkg" listed-absent)"; rm "$pkg_absent/uploads/file.txt"; absent_target="$(new_dir listed-absent-target)"; absent_rc=0
run_restore "$pkg_absent" "$absent_target" "$absent_target/log" || absent_rc=$?
assert_true "arquivo ausente bloqueou" test "$absent_rc" -ne 0
assert_contains "diagnostico de igualdade exata" "inventario do checksum difere do conteudo real" "$absent_target/log.err"

scenario "caminho absoluto no checksums"
pkg_absolute="$(clone_package "$base_pkg" path-absolute)"; printf '%064d  /etc/passwd\n' 0 >> "$pkg_absolute/checksums.sha256"; absolute_target="$(new_dir path-absolute-target)"; absolute_rc=0
run_restore "$pkg_absolute" "$absolute_target" "$absolute_target/log" || absolute_rc=$?
assert_true "caminho absoluto bloqueou" test "$absolute_rc" -ne 0
assert_contains "diagnostico de caminho absoluto" "caminho absoluto ou vazio no checksum" "$absolute_target/log.err"

scenario "componente .. no checksums"
pkg_traversal="$(clone_package "$base_pkg" path-traversal)"; printf '%064d  uploads/../database.sqlite\n' 0 >> "$pkg_traversal/checksums.sha256"; traversal_target="$(new_dir path-traversal-target)"; traversal_rc=0
run_restore "$pkg_traversal" "$traversal_target" "$traversal_target/log" || traversal_rc=$?
assert_true "path traversal bloqueou" test "$traversal_rc" -ne 0
assert_contains "diagnostico de componente ambiguo" "componente ambiguo no checksum" "$traversal_target/log.err"

scenario "symlink ou tipo nao regular no pacote"
pkg_symlink="$(clone_package "$base_pkg" payload-symlink)"; ln -s file.txt "$pkg_symlink/uploads/link.txt"; symlink_target="$(new_dir payload-symlink-target)"; symlink_rc=0
run_restore "$pkg_symlink" "$symlink_target" "$symlink_target/log" || symlink_rc=$?
assert_true "symlink bloqueou" test "$symlink_rc" -ne 0
assert_contains "diagnostico de symlink" "symlink nao permitido no pacote" "$symlink_target/log.err"

# T-U. Corrupcao e FK chegam aos validadores do DB, nao ao checksum.
scenario "DB corrompido bloqueia restore"
pkg_corrupt="$(clone_package "$base_pkg" db-corrupt)"; dd if=/dev/zero of="$pkg_corrupt/database.sqlite" bs=512 count=1 conv=notrunc 2>/dev/null; refresh_db_metadata "$pkg_corrupt"; corrupt_target="$(new_dir db-corrupt-target)"; corrupt_rc=0
run_restore "$pkg_corrupt" "$corrupt_target" "$corrupt_target/log" || corrupt_rc=$?
assert_true "DB corrompido bloqueou" test "$corrupt_rc" -ne 0
assert_contains "diagnostico do DB" "DB do pacote falhou" "$corrupt_target/log.err"

scenario "foreign key invalida bloqueia restore"
pkg_restore_fk="$(clone_package "$base_pkg" restore-fk)"
sqlite3 "$pkg_restore_fk/database.sqlite" 'CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER PRIMARY KEY,pid INTEGER REFERENCES parent(id)); PRAGMA foreign_keys=OFF; INSERT INTO child VALUES(1,99);'
refresh_db_metadata "$pkg_restore_fk"; restore_fk_target="$(new_dir restore-fk-target)"; restore_fk_rc=0
run_restore "$pkg_restore_fk" "$restore_fk_target" "$restore_fk_target/log" || restore_fk_rc=$?
assert_true "FK invalida bloqueou" test "$restore_fk_rc" -ne 0
assert_contains "diagnostico do DB com FK" "DB do pacote falhou" "$restore_fk_target/log.err"

# V-X. Sucesso, sidecars e fonte imutavel.
scenario "restore valido de DB e uploads"
valid_target="$(new_dir valid-restore-target)"; init_test_db "$valid_target/contabase.db" old; mkdir "$valid_target/uploads"; printf old > "$valid_target/uploads/old.txt"
run_restore "$base_pkg" "$valid_target" "$valid_target/log"
assert_eq "DB restaurado contem registro do pacote" wal-committed "$(sqlite3 "$valid_target/contabase.db" 'SELECT v FROM t WHERE id=1;')"
assert_true "upload do pacote restaurado" test -f "$valid_target/uploads/file.txt"
assert_contains "sucesso so foi impresso no caminho feliz" "restore concluido com seguranca" "$valid_target/log.out"

scenario "sidecars antigos nao permanecem apos sucesso"
side_target="$(new_dir sidecar-target)"; init_test_db "$side_target/contabase.db" side; mkdir "$side_target/uploads"; : > "$side_target/contabase.db-wal"; : > "$side_target/contabase.db-shm"
run_restore "$base_pkg" "$side_target" "$side_target/log"
assert_true "WAL antigo foi retirado" test ! -e "$side_target/contabase.db-wal"
assert_true "SHM antigo foi retirado" test ! -e "$side_target/contabase.db-shm"

scenario "fonte imutavel no sucesso"
fp_before="$TEST_ROOT/fingerprint.before"; fp_after_success="$TEST_ROOT/fingerprint.success"; fp_after_failure="$TEST_ROOT/fingerprint.failure"; fingerprint_package "$base_pkg" "$fp_before"
immutable_success_target="$(new_dir immutable-success-target)"; init_test_db "$immutable_success_target/contabase.db" immutable-old; mkdir "$immutable_success_target/uploads"; printf old > "$immutable_success_target/uploads/old.txt"
run_restore "$base_pkg" "$immutable_success_target" "$immutable_success_target/log"
assert_contains "restore real executado entre fingerprints" "restore concluido com seguranca" "$immutable_success_target/log.out"
fingerprint_package "$base_pkg" "$fp_after_success"
assert_true "fonte ficou identica apos sucesso" cmp -s "$fp_before" "$fp_after_success"

scenario "fonte imutavel em falha"
fingerprint_package "$pkg_absolute" "$fp_after_failure"
failure_fp_before="$TEST_ROOT/failure-fingerprint.before"; cp "$fp_after_failure" "$failure_fp_before"
failure_again_target="$(new_dir immutable-failure-target)"; run_restore "$pkg_absolute" "$failure_again_target" "$failure_again_target/log" || :
fingerprint_package "$pkg_absolute" "$fp_after_failure"
assert_true "fonte ficou identica apos falha" cmp -s "$failure_fp_before" "$fp_after_failure"

# Z-AA. Falhas na instalacao devem recuperar DB/uploads anteriores.
scenario "falha ao instalar DB executa rollback completo"
rb_db_target="$(new_dir rollback-db-target)"; init_crash_wal_db "$rb_db_target/contabase.db" old-db; mkdir "$rb_db_target/uploads"; printf old-upload > "$rb_db_target/uploads/old.txt"; mkdir "$rb_db_target/shim"; make_mv_shim "$rb_db_target/shim"; rb_db_rc=0
rb_db_hash_before="$(sha_file "$rb_db_target/contabase.db")"; rb_wal_hash_before="$(sha_file "$rb_db_target/contabase.db-wal")"; rb_shm_hash_before="$(sha_file "$rb_db_target/contabase.db-shm")"
PATH="$rb_db_target/shim:$PATH" CB_REAL_MV="$REAL_MV" CB_MV_LOG="$rb_db_target/mv.log" CB_MV_FAIL_KIND=install_db CB_TARGET_DB="$rb_db_target/contabase.db" CB_TARGET_UPLOADS="$rb_db_target/uploads" CONFIRM_APP_STOPPED=yes DATA_DIR="$rb_db_target" DB_FILE="$rb_db_target/contabase.db" UPLOADS_DIR="$rb_db_target/uploads" "$RESTORE_SCRIPT" "$base_pkg" > "$rb_db_target/out" 2> "$rb_db_target/err" || rb_db_rc=$?
assert_true "falha injetada retornou erro" test "$rb_db_rc" -ne 0
assert_eq "DB anterior voltou byte a byte" "$rb_db_hash_before" "$(sha_file "$rb_db_target/contabase.db")"
assert_eq "WAL anterior voltou byte a byte" "$rb_wal_hash_before" "$(sha_file "$rb_db_target/contabase.db-wal")"
assert_eq "SHM anterior voltou byte a byte" "$rb_shm_hash_before" "$(sha_file "$rb_db_target/contabase.db-shm")"
assert_eq "DB anterior voltou" old-db "$(sqlite3 "$rb_db_target/contabase.db" 'SELECT v FROM t WHERE id=1;')"
assert_eq "uploads anteriores voltaram" old-upload "$(cat "$rb_db_target/uploads/old.txt")"
assert_contains "rollback foi comprovado" "rollback concluido e estado anterior verificado" "$rb_db_target/err"
assert_true "nenhum sucesso foi impresso" test ! -s "$rb_db_target/out"

scenario "falha ao instalar uploads executa rollback completo"
rb_up_target="$(new_dir rollback-uploads-target)"; init_test_db "$rb_up_target/contabase.db" old-up; mkdir "$rb_up_target/uploads"; printf old-upload > "$rb_up_target/uploads/old.txt"; mkdir "$rb_up_target/shim"; make_mv_shim "$rb_up_target/shim"; rb_up_rc=0
PATH="$rb_up_target/shim:$PATH" CB_REAL_MV="$REAL_MV" CB_MV_LOG="$rb_up_target/mv.log" CB_MV_FAIL_KIND=install_uploads CB_TARGET_DB="$rb_up_target/contabase.db" CB_TARGET_UPLOADS="$rb_up_target/uploads" CONFIRM_APP_STOPPED=yes DATA_DIR="$rb_up_target" DB_FILE="$rb_up_target/contabase.db" UPLOADS_DIR="$rb_up_target/uploads" "$RESTORE_SCRIPT" "$base_pkg" > "$rb_up_target/out" 2> "$rb_up_target/err" || rb_up_rc=$?
assert_true "falha de uploads retornou erro" test "$rb_up_rc" -ne 0
assert_eq "DB anterior foi recuperado" old-up "$(sqlite3 "$rb_up_target/contabase.db" 'SELECT v FROM t WHERE id=1;')"
assert_eq "uploads anteriores foram recuperados" old-upload "$(cat "$rb_up_target/uploads/old.txt")"
assert_contains "sequencia registrou preservacao do DB antigo" "old-database.sqlite" "$rb_up_target/mv.log"
assert_contains "sequencia registrou preservacao de uploads antigos" "old-uploads" "$rb_up_target/mv.log"
assert_contains "sequencia registrou instalacao da staging" ".restore-stage." "$rb_up_target/mv.log"
assert_contains "sequencia registrou retirada do DB parcial" "partial-new-database.sqlite" "$rb_up_target/mv.log"
assert_contains "sequencia registrou devolucao do rollback" ".restore-rollback." "$rb_up_target/mv.log"

# AB. Falha do rollback e evidencias preservadas.
scenario "falha durante o proprio rollback e fatal"
rb_fail_target="$(new_dir rollback-fatal-target)"; init_test_db "$rb_fail_target/contabase.db" fatal-old; mkdir "$rb_fail_target/uploads"; printf old > "$rb_fail_target/uploads/old.txt"; mkdir "$rb_fail_target/shim"; make_mv_shim "$rb_fail_target/shim"; rb_fail_rc=0
PATH="$rb_fail_target/shim:$PATH" CB_REAL_MV="$REAL_MV" CB_MV_LOG="$rb_fail_target/mv.log" CB_MV_FAIL_KIND=install_db_rollback_db CB_TARGET_DB="$rb_fail_target/contabase.db" CB_TARGET_UPLOADS="$rb_fail_target/uploads" CONFIRM_APP_STOPPED=yes DATA_DIR="$rb_fail_target" DB_FILE="$rb_fail_target/contabase.db" UPLOADS_DIR="$rb_fail_target/uploads" "$RESTORE_SCRIPT" "$base_pkg" > "$rb_fail_target/out" 2> "$rb_fail_target/err" || rb_fail_rc=$?
assert_eq "status fatal dedicado" 70 "$rb_fail_rc"
assert_contains "diagnostico exige recuperacao manual" "ROLLBACK INCOMPLETO; recuperacao manual necessaria" "$rb_fail_target/err"
assert_true "diretorio de rollback foi preservado" test -n "$(find "$rb_fail_target" -maxdepth 1 -type d -name '.restore-rollback.*' -print -quit)"
assert_true "staging foi preservada" test -n "$(find "$rb_fail_target" -maxdepth 1 -type d -name '.restore-stage.*' -print -quit)"
assert_true "sucesso nao foi impresso" test ! -s "$rb_fail_target/out"

# AC. Tentativas independentes.
scenario "duas tentativas nao reutilizam staging ou rollback"
attempt_target="$(new_dir two-attempts-target)"; init_test_db "$attempt_target/contabase.db" attempt-old; mkdir "$attempt_target/uploads"; printf old > "$attempt_target/uploads/old.txt"; mkdir "$attempt_target/shim"; make_mv_shim "$attempt_target/shim"
for attempt in 1 2; do
  rc=0
  PATH="$attempt_target/shim:$PATH" CB_REAL_MV="$REAL_MV" CB_MV_LOG="$attempt_target/mv-$attempt.log" CB_MV_FAIL_KIND=install_db CB_TARGET_DB="$attempt_target/contabase.db" CB_TARGET_UPLOADS="$attempt_target/uploads" CONFIRM_APP_STOPPED=yes DATA_DIR="$attempt_target" DB_FILE="$attempt_target/contabase.db" UPLOADS_DIR="$attempt_target/uploads" "$RESTORE_SCRIPT" "$base_pkg" > "$attempt_target/out-$attempt" 2> "$attempt_target/err-$attempt" || rc=$?
  [ "$rc" -ne 0 ]
done
stage_one="$(grep -m 1 -o "$attempt_target/.restore-stage\.[^/]\+" "$attempt_target/mv-1.log")"
stage_two="$(grep -m 1 -o "$attempt_target/.restore-stage\.[^/]\+" "$attempt_target/mv-2.log")"
rollback_one="$(grep -m 1 -o "$attempt_target/.restore-rollback\.[^/]\+" "$attempt_target/mv-1.log")"
rollback_two="$(grep -m 1 -o "$attempt_target/.restore-rollback\.[^/]\+" "$attempt_target/mv-2.log")"
assert_true "stagings foram distintas" test "$stage_one" != "$stage_two"
assert_true "areas de rollback foram distintas" test "$rollback_one" != "$rollback_two"

# AD-AF. Mode e ownership preservados.
scenario "mode do DB preservado"
mode_target="$(new_dir mode-target)"; init_test_db "$mode_target/contabase.db" mode-old; mkdir "$mode_target/uploads" "$mode_target/shim"; chmod 640 "$mode_target/contabase.db"; chmod 750 "$mode_target/uploads"; make_metadata_shims "$mode_target/shim"
PATH="$mode_target/shim:$PATH" CB_METADATA_LOG="$mode_target/metadata.log" CB_REAL_CHMOD="$REAL_CHMOD" CB_REAL_CHOWN="$REAL_CHOWN" run_restore "$base_pkg" "$mode_target" "$mode_target/log"
assert_eq "mode do DB preservado" 640 "$(file_mode "$mode_target/contabase.db")"
assert_contains "shim registrou chmod seletivo do DB" "$mode_target/contabase.db" "$mode_target/metadata.log"
assert_contains "shim registrou chown seletivo do DB" "chown" "$mode_target/metadata.log"

scenario "mode do diretorio de uploads preservado"
assert_eq "mode de uploads preservado" 750 "$(file_mode "$mode_target/uploads")"

scenario "UID e GID preservados quando suportado"
owner_target="$(new_dir owner-target)"; init_test_db "$owner_target/contabase.db" owner-old; mkdir "$owner_target/uploads"
old_db_uid="$(file_uid "$owner_target/contabase.db")"; old_db_gid="$(file_gid "$owner_target/contabase.db")"; old_up_uid="$(file_uid "$owner_target/uploads")"; old_up_gid="$(file_gid "$owner_target/uploads")"
run_restore "$base_pkg" "$owner_target" "$owner_target/log"
assert_eq "UID do DB preservado" "$old_db_uid" "$(file_uid "$owner_target/contabase.db")"
assert_eq "GID do DB preservado" "$old_db_gid" "$(file_gid "$owner_target/contabase.db")"
assert_eq "UID de uploads preservado" "$old_up_uid" "$(file_uid "$owner_target/uploads")"
assert_eq "GID de uploads preservado" "$old_up_gid" "$(file_gid "$owner_target/uploads")"

# AG. chown negado so e aceito se stat confirmar; aqui stat diverge e bloqueia.
scenario "ownership fail-closed quando chown nao e permitido"
owner_fail_target="$(new_dir owner-fail-target)"; init_test_db "$owner_fail_target/contabase.db" owner-fail-old; mkdir "$owner_fail_target/uploads" "$owner_fail_target/shim"
cat > "$owner_fail_target/shim/chown" <<'SHIM'
#!/usr/bin/env bash
if [ "${*: -1}" = "$CB_CHOWN_FAIL_PATH" ]; then
  : > "$CB_CHOWN_FAILED_MARKER"
  exit 95
fi
exec "$CB_REAL_CHOWN" "$@"
SHIM
cat > "$owner_fail_target/shim/stat" <<'SHIM'
#!/usr/bin/env bash
if [ -f "$CB_CHOWN_FAILED_MARKER" ] && { [ "${1:-}" = -f ] || [ "${1:-}" = -c ]; } && { [ "${2:-}" = %u ] || [ "${2:-}" = %g ]; }; then
  echo 99999
  exit 0
fi
exec "$CB_REAL_STAT" "$@"
SHIM
chmod +x "$owner_fail_target/shim/chown" "$owner_fail_target/shim/stat"
owner_fail_rc=0
PATH="$owner_fail_target/shim:$PATH" CB_CHOWN_FAILED_MARKER="$owner_fail_target/chown.failed" CB_CHOWN_FAIL_PATH="$owner_fail_target/contabase.db" CB_REAL_CHOWN="$REAL_CHOWN" CB_REAL_STAT="$REAL_STAT" CONFIRM_APP_STOPPED=yes DATA_DIR="$owner_fail_target" DB_FILE="$owner_fail_target/contabase.db" UPLOADS_DIR="$owner_fail_target/uploads" "$RESTORE_SCRIPT" "$base_pkg" > "$owner_fail_target/out" 2> "$owner_fail_target/err" || owner_fail_rc=$?
assert_true "ownership nao comprovado bloqueou" test "$owner_fail_rc" -ne 0
assert_contains "diagnostico de metadata" "falha ao aplicar metadados do DB" "$owner_fail_target/err"
assert_true "nenhum sucesso no branch fail-closed" test ! -s "$owner_fail_target/out"

# AH. Item nao regular inserido somente na staging durante a copia.
scenario "symlink injetado no staging bloqueia antes da mutacao"
stg_link_root="$(new_dir staging-symlink)"; stg_link_target="$stg_link_root/target"; stg_link_shim="$stg_link_root/shim"; mkdir "$stg_link_target" "$stg_link_target/uploads" "$stg_link_shim"
init_test_db "$stg_link_target/contabase.db" link-old; printf old-link > "$stg_link_target/uploads/old.txt"; make_staging_shims "$stg_link_shim"; : > "$stg_link_root/mv.log"
link_expected_sha="$(awk '$2=="uploads/file.txt" {print $1}' "$base_pkg/checksums.sha256")"
assert_eq "payload listado continuava valido antes da copia" "$link_expected_sha" "$(sha_file "$base_pkg/uploads/file.txt")"
fingerprint_package "$base_pkg" "$stg_link_root/source.before"; fingerprint_package "$stg_link_target" "$stg_link_root/target.before"
set +e
PATH="$stg_link_shim:$PATH" CB_CP_MODE=symlink CB_PACKAGE_DB="$base_pkg/database.sqlite" CB_PACKAGE_UPLOADS="$base_pkg/uploads" CB_CP_PROOF="$stg_link_root/cp.proof" CB_MV_AUDIT_LOG="$stg_link_root/mv.log" CB_REAL_CP="$REAL_CP" CB_REAL_MV="$REAL_MV" CB_REAL_LN="$REAL_LN" run_restore "$base_pkg" "$stg_link_target" "$stg_link_root/restore"
stg_link_rc=$?
set -e
fingerprint_package "$base_pkg" "$stg_link_root/source.after"; fingerprint_package "$stg_link_target" "$stg_link_root/target.after"
assert_contains "shim comprovou symlink existente na staging" "symlink path=" "$stg_link_root/cp.proof"
assert_true "symlink no staging retornou erro real" test "$stg_link_rc" -ne 0
assert_contains "tipo proibido foi o bloqueador" "symlink nao permitido no staging" "$stg_link_root/restore.err"
assert_true "fonte permaneceu identica no bloqueio de symlink" cmp -s "$stg_link_root/source.before" "$stg_link_root/source.after"
assert_true "destino permaneceu identico no bloqueio de symlink" cmp -s "$stg_link_root/target.before" "$stg_link_root/target.after"
assert_true "mutation_started nao foi alcancado no symlink" test ! -s "$stg_link_root/mv.log"
assert_true "symlink bloqueado nao imprimiu sucesso" test ! -s "$stg_link_root/restore.out"

# AI. Upload adulterado somente no destino da copia para staging.
scenario "TOCTOU real de upload bloqueia antes da mutacao"
stg_upl_root="$(new_dir staging-upload-toctou)"; stg_upl_target="$stg_upl_root/target"; stg_upl_shim="$stg_upl_root/shim"; mkdir "$stg_upl_target" "$stg_upl_target/uploads" "$stg_upl_shim"
init_test_db "$stg_upl_target/contabase.db" upload-old; printf old-upload > "$stg_upl_target/uploads/old.txt"; make_staging_shims "$stg_upl_shim"; : > "$stg_upl_root/mv.log"
upl_expected_sha="$(awk '$2=="uploads/file.txt" {print $1}' "$base_pkg/checksums.sha256")"
assert_eq "upload fonte estava validado antes do TOCTOU" "$upl_expected_sha" "$(sha_file "$base_pkg/uploads/file.txt")"
fingerprint_package "$base_pkg" "$stg_upl_root/source.before"; fingerprint_package "$stg_upl_target" "$stg_upl_root/target.before"
set +e
PATH="$stg_upl_shim:$PATH" CB_CP_MODE=upload CB_PACKAGE_DB="$base_pkg/database.sqlite" CB_PACKAGE_UPLOADS="$base_pkg/uploads" CB_CP_PROOF="$stg_upl_root/cp.proof" CB_MV_AUDIT_LOG="$stg_upl_root/mv.log" CB_REAL_CP="$REAL_CP" CB_REAL_MV="$REAL_MV" CB_REAL_LN="$REAL_LN" run_restore "$base_pkg" "$stg_upl_target" "$stg_upl_root/restore"
stg_upl_rc=$?
set -e
fingerprint_package "$base_pkg" "$stg_upl_root/source.after"; fingerprint_package "$stg_upl_target" "$stg_upl_root/target.after"
upl_source_sha="$(awk '{sub(/^source_sha=/,"",$2); print $2}' "$stg_upl_root/cp.proof")"; upl_staged_sha="$(awk '{sub(/^staged_sha=/,"",$3); print $3}' "$stg_upl_root/cp.proof")"
assert_contains "shim interceptou a copia real do upload" "upload source_sha=" "$stg_upl_root/cp.proof"
assert_true "bytes do upload na staging divergiram" test "$upl_source_sha" != "$upl_staged_sha"
assert_true "TOCTOU de upload retornou erro real" test "$stg_upl_rc" -ne 0
assert_contains "checksum divergente do staging foi detectado" "checksum do staging diverge: uploads/file.txt" "$stg_upl_root/restore.err"
assert_true "fonte permaneceu identica no TOCTOU de upload" cmp -s "$stg_upl_root/source.before" "$stg_upl_root/source.after"
assert_true "destino permaneceu identico no TOCTOU de upload" cmp -s "$stg_upl_root/target.before" "$stg_upl_root/target.after"
assert_true "mutation_started nao foi alcancado no TOCTOU de upload" test ! -s "$stg_upl_root/mv.log"
assert_true "TOCTOU de upload nao imprimiu sucesso" test ! -s "$stg_upl_root/restore.out"

# AJ. Database adulterado somente no destino da copia para staging.
scenario "TOCTOU real do DB bloqueia antes da mutacao"
stg_db_root="$(new_dir staging-db-toctou)"; stg_db_target="$stg_db_root/target"; stg_db_shim="$stg_db_root/shim"; mkdir "$stg_db_target" "$stg_db_target/uploads" "$stg_db_shim"
init_test_db "$stg_db_target/contabase.db" db-old; printf old-db-upload > "$stg_db_target/uploads/old.txt"; make_staging_shims "$stg_db_shim"; : > "$stg_db_root/mv.log"
db_expected_sha="$(awk '$2=="database.sqlite" {print $1}' "$base_pkg/checksums.sha256")"
assert_eq "DB fonte estava validado antes do TOCTOU" "$db_expected_sha" "$(sha_file "$base_pkg/database.sqlite")"
fingerprint_package "$base_pkg" "$stg_db_root/source.before"; fingerprint_package "$stg_db_target" "$stg_db_root/target.before"
set +e
PATH="$stg_db_shim:$PATH" CB_CP_MODE=database CB_PACKAGE_DB="$base_pkg/database.sqlite" CB_PACKAGE_UPLOADS="$base_pkg/uploads" CB_CP_PROOF="$stg_db_root/cp.proof" CB_MV_AUDIT_LOG="$stg_db_root/mv.log" CB_REAL_CP="$REAL_CP" CB_REAL_MV="$REAL_MV" CB_REAL_LN="$REAL_LN" run_restore "$base_pkg" "$stg_db_target" "$stg_db_root/restore"
stg_db_rc=$?
set -e
fingerprint_package "$base_pkg" "$stg_db_root/source.after"; fingerprint_package "$stg_db_target" "$stg_db_root/target.after"
db_source_sha="$(awk '{sub(/^source_sha=/,"",$2); print $2}' "$stg_db_root/cp.proof")"; db_staged_sha="$(awk '{sub(/^staged_sha=/,"",$3); print $3}' "$stg_db_root/cp.proof")"
assert_contains "shim interceptou a copia real do DB" "database source_sha=" "$stg_db_root/cp.proof"
assert_true "bytes do DB na staging divergiram" test "$db_source_sha" != "$db_staged_sha"
assert_true "TOCTOU do DB retornou erro real" test "$stg_db_rc" -ne 0
assert_contains "DB divergente do staging foi detectado" "database_size do staging diverge do manifest" "$stg_db_root/restore.err"
assert_true "fonte permaneceu identica no TOCTOU do DB" cmp -s "$stg_db_root/source.before" "$stg_db_root/source.after"
assert_true "destino permaneceu identico no TOCTOU do DB" cmp -s "$stg_db_root/target.before" "$stg_db_root/target.after"
assert_true "mutation_started nao foi alcancado no TOCTOU do DB" test ! -s "$stg_db_root/mv.log"
assert_true "TOCTOU do DB nao imprimiu sucesso" test ! -s "$stg_db_root/restore.out"

printf '\n=== MATRIZ REAL: %d cenarios, %d assertions, %d falhas ===\n' "$SCENARIOS" "$ASSERTIONS" "$FAILED_ASSERTIONS"
[ "$SCENARIOS" -eq "$EXPECTED_SCENARIOS" ] || { echo "erro interno: esperado $EXPECTED_SCENARIOS cenarios" >&2; exit 1; }
[ "$ASSERTIONS" -eq "$EXPECTED_ASSERTIONS" ] || { echo "erro interno: esperado $EXPECTED_ASSERTIONS assertions" >&2; exit 1; }
[ "$FAILED_ASSERTIONS" -eq 0 ] || exit 1

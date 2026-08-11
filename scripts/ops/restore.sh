#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "uso: $0 <backup-pacote-dir>" >&2
  exit 1
fi

PACKAGE_DIR="${1%/}"
DATA_DIR="${DATA_DIR:-data}"
DB_FILE="${DB_FILE:-$DATA_DIR/contabase.db}"
UPLOADS_DIR="${UPLOADS_DIR:-$DATA_DIR/uploads}"
CONFIRM_APP_STOPPED="${CONFIRM_APP_STOPPED:-}"

mutation_started=0
rollback_attempted=0
rollback_succeeded=0
restore_succeeded=0
old_db_saved=0
old_wal_saved=0
old_shm_saved=0
old_uploads_saved=0
new_db_installed=0
new_uploads_installed=0
old_db_existed=0
old_wal_existed=0
old_shm_existed=0
old_uploads_existed=0
old_db_contract_ok=0
STAGE_DIR=""
ROLLBACK_DIR=""
SOURCE_BEFORE=""
SOURCE_AFTER=""
SOURCE_LIST=""
EXPECTED_LIST=""
REAL_LIST=""
PRECHECK_LIST=""
VALIDATED_CHECKSUMS=""
STAGE_REAL_LIST=""
STAGE_ENTRIES_NUL=""
source_validated=0

say_error() { echo "erro: $*" >&2; }
fail() { say_error "$*"; exit 1; }

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

stat_mode() { stat -f '%Lp' "$1" 2>/dev/null || stat -c '%a' "$1"; }
stat_uid() { stat -f '%u' "$1" 2>/dev/null || stat -c '%u' "$1"; }
stat_gid() { stat -f '%g' "$1" 2>/dev/null || stat -c '%g' "$1"; }
stat_size() { stat -f '%z' "$1" 2>/dev/null || stat -c '%s' "$1"; }
stat_nlink() { stat -f '%l' "$1" 2>/dev/null || stat -c '%h' "$1"; }

is_sqlite_file() {
  local file="$1" header_hex
  header_hex="$(dd if="$file" bs=16 count=1 2>/dev/null | od -An -t x1 | tr -d ' \n')"
  [ "$header_hex" = "53514c69746520666f726d6174203300" ]
}

db_contract_ok() {
  local file="$1" integrity fk
  is_sqlite_file "$file" || return 1
  integrity="$(sqlite3 -batch -noheader "$file" 'PRAGMA integrity_check;' 2>/dev/null | tr -d '\r')" || return 1
  [ "$integrity" = "ok" ] || return 1
  fk="$(sqlite3 -batch -noheader "$file" 'PRAGMA foreign_key_check;' 2>/dev/null | tr -d '\r')" || return 1
  [ -z "$fk" ]
}

validate_db_state_copy() {
  local source_db="$1" validation_dir="$2"
  mkdir "$validation_dir" || return 1
  cp -p "$source_db" "$validation_dir/contabase.db" || return 1
  if [ -f "$source_db-wal" ]; then
    cp -p "$source_db-wal" "$validation_dir/contabase.db-wal" || return 1
  fi
  db_contract_ok "$validation_dir/contabase.db"
}

apply_owner() {
  local path="$1" uid="$2" gid="$3"
  if chown "$uid:$gid" "$path" 2>/dev/null; then
    :
  elif [ "$(stat_uid "$path")" = "$uid" ] && [ "$(stat_gid "$path")" = "$gid" ]; then
    :
  else
    return 1
  fi
  [ "$(stat_uid "$path")" = "$uid" ] && [ "$(stat_gid "$path")" = "$gid" ]
}

apply_metadata() {
  local path="$1" mode="$2" uid="$3" gid="$4"
  chmod "$mode" "$path" || return 1
  apply_owner "$path" "$uid" "$gid" || return 1
  [ "$(stat_mode "$path")" = "$mode" ] || return 1
  [ "$(stat_uid "$path")" = "$uid" ] || return 1
  [ "$(stat_gid "$path")" = "$gid" ]
}

build_source_snapshot() {
  local output="$1" entry rel type size digest mode uid gid
  : > "$output"
  while IFS= read -r rel; do
    [ -n "$rel" ] || continue
    entry="$PACKAGE_DIR/$rel"
    if [ -L "$entry" ]; then type=symlink
    elif [ -f "$entry" ]; then type=file
    elif [ -d "$entry" ]; then type=directory
    else type=other
    fi
    mode="$(stat_mode "$entry")" || return 1
    uid="$(stat_uid "$entry")" || return 1
    gid="$(stat_gid "$entry")" || return 1
    if [ "$type" = file ]; then
      size="$(stat_size "$entry")" || return 1
      digest="$(sha_file "$entry")" || return 1
    else
      size=-
      digest=-
    fi
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$rel" "$type" "$size" "$digest" "$mode" "$uid" "$gid" >> "$output"
  done < "$SOURCE_LIST"
}

validate_source_immutable() {
  [ -n "$SOURCE_BEFORE" ] && [ -f "$SOURCE_BEFORE" ] || return 0
  SOURCE_AFTER="${SOURCE_BEFORE}.after"
  if ! build_source_snapshot "$SOURCE_AFTER"; then
    say_error "nao foi possivel imprimir a fonte depois do restore"
    return 1
  fi
  echo "fonte do pacote depois (path tipo size sha256 mode uid gid):" >&2
  cat "$SOURCE_AFTER" >&2
  if ! cmp -s "$SOURCE_BEFORE" "$SOURCE_AFTER"; then
    say_error "pacote fonte foi modificado durante o restore"
    return 1
  fi
}

rollback_fatal() {
  say_error "$1"
  say_error "ROLLBACK INCOMPLETO; recuperacao manual necessaria"
  say_error "staging preservado em: $STAGE_DIR"
  say_error "rollback preservado em: $ROLLBACK_DIR"
  say_error "estado parcial preservado nos caminhos de destino"
  return 1
}

rollback_restore() {
  rollback_attempted=1
  say_error "falha apos inicio da mutacao; iniciando rollback explicito"

  if [ -e "$DB_FILE" ] || [ -L "$DB_FILE" ]; then
    if [ "$old_db_existed" -eq 1 ] && [ "$old_db_saved" -eq 0 ] && [ "$new_db_installed" -eq 0 ]; then
      : # A falha ao preservar o DB deixou o estado anterior no lugar.
    elif ! mv "$DB_FILE" "$ROLLBACK_DIR/partial-new-database.sqlite"; then
      rollback_fatal "falha ao retirar DB parcialmente instalado"
      return 1
    fi
  fi
  new_db_installed=0

  if [ -e "$UPLOADS_DIR" ] || [ -L "$UPLOADS_DIR" ]; then
    if [ "$old_uploads_existed" -eq 1 ] && [ "$old_uploads_saved" -eq 0 ] && [ "$new_uploads_installed" -eq 0 ]; then
      : # A falha ao preservar uploads deixou o estado anterior no lugar.
    elif ! mv "$UPLOADS_DIR" "$ROLLBACK_DIR/partial-new-uploads"; then
      rollback_fatal "falha ao retirar uploads parcialmente instalados"
      return 1
    fi
  fi
  new_uploads_installed=0

  if [ "$old_db_existed" -eq 1 ]; then
    if [ "$old_db_saved" -eq 1 ]; then
      [ -f "$ROLLBACK_DIR/old-database.sqlite" ] || {
        rollback_fatal "DB anterior nao esta disponivel no rollback"
        return 1
      }
      if ! mv "$ROLLBACK_DIR/old-database.sqlite" "$DB_FILE"; then
        rollback_fatal "falha ao devolver DB anterior"
        return 1
      fi
      old_db_saved=0
    elif [ ! -f "$DB_FILE" ]; then
      rollback_fatal "DB anterior nao esta ativo nem salvo no rollback"
      return 1
    fi
    if ! apply_metadata "$DB_FILE" "$old_db_mode" "$old_db_uid" "$old_db_gid"; then
      rollback_fatal "falha ao restaurar metadados do DB anterior"
      return 1
    fi
  elif [ -e "$DB_FILE" ] || [ -L "$DB_FILE" ]; then
    rollback_fatal "DB novo permaneceu ativo sem DB anterior"
    return 1
  fi

  if [ "$old_wal_existed" -eq 1 ]; then
    if [ "$old_wal_saved" -eq 1 ]; then
      [ -f "$ROLLBACK_DIR/old-database.sqlite-wal" ] || {
        rollback_fatal "WAL anterior nao esta disponivel no rollback"
        return 1
      }
      if ! mv "$ROLLBACK_DIR/old-database.sqlite-wal" "$DB_FILE-wal"; then
        rollback_fatal "falha ao devolver WAL anterior"
        return 1
      fi
      old_wal_saved=0
    elif [ ! -f "$DB_FILE-wal" ]; then
      rollback_fatal "WAL anterior nao esta ativo nem salvo no rollback"
      return 1
    fi
    [ "$(sha_file "$DB_FILE-wal")" = "$old_wal_sha" ] || { rollback_fatal "conteudo do WAL divergiu apos rollback"; return 1; }
    [ "$(stat_mode "$DB_FILE-wal")" = "$old_wal_mode" ] || { rollback_fatal "mode do WAL divergiu apos rollback"; return 1; }
    [ "$(stat_uid "$DB_FILE-wal")" = "$old_wal_uid" ] || { rollback_fatal "UID do WAL divergiu apos rollback"; return 1; }
    [ "$(stat_gid "$DB_FILE-wal")" = "$old_wal_gid" ] || { rollback_fatal "GID do WAL divergiu apos rollback"; return 1; }
  elif [ -e "$DB_FILE-wal" ]; then
    rollback_fatal "WAL inesperado permaneceu apos rollback"
    return 1
  fi

  if [ "$old_shm_existed" -eq 1 ]; then
    if [ "$old_shm_saved" -eq 1 ]; then
      [ -f "$ROLLBACK_DIR/old-database.sqlite-shm" ] || {
        rollback_fatal "SHM anterior nao esta disponivel no rollback"
        return 1
      }
      if ! mv "$ROLLBACK_DIR/old-database.sqlite-shm" "$DB_FILE-shm"; then
        rollback_fatal "falha ao devolver SHM anterior"
        return 1
      fi
      old_shm_saved=0
    elif [ ! -f "$DB_FILE-shm" ]; then
      rollback_fatal "SHM anterior nao esta ativo nem salvo no rollback"
      return 1
    fi
    [ "$(sha_file "$DB_FILE-shm")" = "$old_shm_sha" ] || { rollback_fatal "conteudo do SHM divergiu apos rollback"; return 1; }
    [ "$(stat_mode "$DB_FILE-shm")" = "$old_shm_mode" ] || { rollback_fatal "mode do SHM divergiu apos rollback"; return 1; }
    [ "$(stat_uid "$DB_FILE-shm")" = "$old_shm_uid" ] || { rollback_fatal "UID do SHM divergiu apos rollback"; return 1; }
    [ "$(stat_gid "$DB_FILE-shm")" = "$old_shm_gid" ] || { rollback_fatal "GID do SHM divergiu apos rollback"; return 1; }
  elif [ -e "$DB_FILE-shm" ]; then
    rollback_fatal "SHM inesperado permaneceu apos rollback"
    return 1
  fi

  if [ "$old_uploads_existed" -eq 1 ]; then
    if [ "$old_uploads_saved" -eq 1 ]; then
      [ -d "$ROLLBACK_DIR/old-uploads" ] || {
        rollback_fatal "uploads anteriores nao estao disponiveis no rollback"
        return 1
      }
      if ! mv "$ROLLBACK_DIR/old-uploads" "$UPLOADS_DIR"; then
        rollback_fatal "falha ao devolver uploads anteriores"
        return 1
      fi
      old_uploads_saved=0
    elif [ ! -d "$UPLOADS_DIR" ]; then
      rollback_fatal "uploads anteriores nao estao ativos nem salvos no rollback"
      return 1
    fi
    if ! apply_metadata "$UPLOADS_DIR" "$old_uploads_mode" "$old_uploads_uid" "$old_uploads_gid"; then
      rollback_fatal "falha ao restaurar metadados dos uploads anteriores"
      return 1
    fi
  elif [ -e "$UPLOADS_DIR" ] || [ -L "$UPLOADS_DIR" ]; then
    rollback_fatal "uploads novos permaneceram ativos sem estado anterior"
    return 1
  fi

  if [ "$old_db_existed" -eq 1 ]; then
    [ -f "$DB_FILE" ] || { rollback_fatal "DB anterior ausente apos rollback"; return 1; }
    [ "$(stat_mode "$DB_FILE")" = "$old_db_mode" ] || { rollback_fatal "mode do DB divergiu apos rollback"; return 1; }
    [ "$(stat_uid "$DB_FILE")" = "$old_db_uid" ] || { rollback_fatal "UID do DB divergiu apos rollback"; return 1; }
    [ "$(stat_gid "$DB_FILE")" = "$old_db_gid" ] || { rollback_fatal "GID do DB divergiu apos rollback"; return 1; }
    [ "$(sha_file "$DB_FILE")" = "$old_db_sha" ] || { rollback_fatal "conteudo do DB divergiu apos rollback"; return 1; }
    if [ "$old_db_contract_ok" -eq 1 ] && ! validate_db_state_copy "$DB_FILE" "$STAGE_DIR/rollback-validation"; then
      rollback_fatal "DB recuperado falhou integrity_check ou foreign_key_check"
      return 1
    fi
  fi
  if [ "$old_uploads_existed" -eq 1 ]; then
    [ -d "$UPLOADS_DIR" ] || { rollback_fatal "uploads anteriores ausentes apos rollback"; return 1; }
    [ "$(stat_mode "$UPLOADS_DIR")" = "$old_uploads_mode" ] || { rollback_fatal "mode dos uploads divergiu apos rollback"; return 1; }
    [ "$(stat_uid "$UPLOADS_DIR")" = "$old_uploads_uid" ] || { rollback_fatal "UID dos uploads divergiu apos rollback"; return 1; }
    [ "$(stat_gid "$UPLOADS_DIR")" = "$old_uploads_gid" ] || { rollback_fatal "GID dos uploads divergiu apos rollback"; return 1; }
  fi

  rollback_succeeded=1
  say_error "rollback concluido e estado anterior verificado"
  return 0
}

on_exit() {
  local status=$?
  trap - EXIT
  set +e
  if [ "$status" -ne 0 ] && [ "$mutation_started" -eq 1 ] && [ "$rollback_attempted" -eq 0 ]; then
    if ! rollback_restore; then status=70; fi
  fi
  if [ "$source_validated" -eq 0 ] && ! validate_source_immutable; then status=71; fi
  if [ -n "$STAGE_DIR" ] && [ -n "$ROLLBACK_DIR" ]; then
    if [ "$restore_succeeded" -eq 1 ] || [ "$rollback_succeeded" -eq 1 ]; then
      rm -rf "$STAGE_DIR" "$ROLLBACK_DIR"
    elif [ "$mutation_started" -eq 0 ]; then
      rm -rf "$STAGE_DIR" "$ROLLBACK_DIR"
    fi
  fi
  if [ -n "$SOURCE_BEFORE" ]; then
    rm -f "$SOURCE_BEFORE" "$SOURCE_AFTER" "$SOURCE_LIST" "$EXPECTED_LIST" "$REAL_LIST" "$PRECHECK_LIST" "$VALIDATED_CHECKSUMS" "$STAGE_REAL_LIST" "$STAGE_ENTRIES_NUL"
  fi
  exit "$status"
}
trap on_exit EXIT

[ -d "$PACKAGE_DIR" ] || fail "pacote inexistente: $PACKAGE_DIR"
[ "$CONFIRM_APP_STOPPED" = yes ] || fail "restore bloqueado; pare a aplicacao e use CONFIRM_APP_STOPPED=yes"
command -v sqlite3 >/dev/null 2>&1 || fail "sqlite3 nao encontrado"
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  fail "sha256sum ou shasum nao encontrado"
fi
[ -d "$DATA_DIR" ] || fail "DATA_DIR deve existir antes do restore: $DATA_DIR"
[ -d "$(dirname "$DB_FILE")" ] || fail "diretorio pai do DB deve existir"
[ -d "$(dirname "$UPLOADS_DIR")" ] || fail "diretorio pai de uploads deve existir"

SOURCE_LIST="$(mktemp "${TMPDIR:-/tmp}/contabase-source-list.XXXXXX")"
SOURCE_BEFORE="$(mktemp "${TMPDIR:-/tmp}/contabase-source-before.XXXXXX")"

if ! find "$PACKAGE_DIR" -mindepth 1 -print0 > "${SOURCE_LIST}.nul"; then
  fail "falha ao enumerar pacote"
fi
: > "$SOURCE_LIST"
while IFS= read -r -d '' entry; do
  rel="${entry#"$PACKAGE_DIR/"}"
  case "$rel" in ''|*$'\n'*|*$'\r'*|*$'\t'*) fail "caminho inseguro no pacote" ;; esac
  if [ -L "$entry" ]; then fail "symlink nao permitido no pacote: $rel"; fi
  if [ ! -f "$entry" ] && [ ! -d "$entry" ]; then fail "tipo nao regular no pacote: $rel"; fi
  printf '%s\n' "$rel" >> "$SOURCE_LIST"
done < "${SOURCE_LIST}.nul"
rm "${SOURCE_LIST}.nul"
LC_ALL=C sort -o "$SOURCE_LIST" "$SOURCE_LIST"
MANIFEST="$PACKAGE_DIR/manifest.txt"
CHECKSUMS="$PACKAGE_DIR/checksums.sha256"
PACKAGE_DB="$PACKAGE_DIR/database.sqlite"
PACKAGE_UPLOADS="$PACKAGE_DIR/uploads"
[ -f "$MANIFEST" ] && [ ! -L "$MANIFEST" ] || fail "manifest.txt ausente ou invalido"
[ -f "$CHECKSUMS" ] && [ ! -L "$CHECKSUMS" ] || fail "checksums.sha256 ausente ou invalido"
[ -f "$PACKAGE_DB" ] && [ ! -L "$PACKAGE_DB" ] || fail "database.sqlite ausente ou invalido"
[ -d "$PACKAGE_UPLOADS" ] && [ ! -L "$PACKAGE_UPLOADS" ] || fail "diretorio uploads ausente ou invalido"

# Checksums sao analisados como dados antes de abrir qualquer payload listado.
PRECHECK_LIST="$(mktemp "${TMPDIR:-/tmp}/contabase-precheck.XXXXXX")"
: > "$PRECHECK_LIST"
precheck_count=0
while IFS= read -r line || [ -n "$line" ]; do
  precheck_count=$((precheck_count + 1))
  case "$line" in *$'\r'*|*$'\n'*|*$'\t'*) fail "linha de checksum contem caractere de controle" ;; esac
  if [[ ! "$line" =~ ^([0-9A-Fa-f]{64})\ \ (.+)$ ]]; then
    fail "formato invalido em checksums.sha256"
  fi
  payload_path="${BASH_REMATCH[2]}"
  case "$payload_path" in
    /*|'') fail "caminho absoluto ou vazio no checksum: $payload_path" ;;
    database.sqlite|uploads/*) ;;
    *) fail "entrada nao permitida no checksum: $payload_path" ;;
  esac
  case "/$payload_path/" in *'/../'*|*'/./'*|*'//'*) fail "componente ambiguo no checksum: $payload_path" ;; esac
  if grep -Fqx -- "$payload_path" "$PRECHECK_LIST"; then
    fail "entrada duplicada no checksums: $payload_path"
  fi
  printf '%s\n' "$payload_path" >> "$PRECHECK_LIST"
done < "$CHECKSUMS"
[ "$precheck_count" -gt 0 ] || fail "checksums.sha256 vazio"

if ! build_source_snapshot "$SOURCE_BEFORE"; then fail "falha ao imprimir fonte do pacote"; fi
echo "fonte do pacote antes (path tipo size sha256 mode uid gid):" >&2
cat "$SOURCE_BEFORE" >&2

manifest_seen='|'
manifest_count=0
while IFS= read -r line || [ -n "$line" ]; do
  manifest_count=$((manifest_count + 1))
  case "$line" in *$'\r'*|*$'\t'*) fail "valor do manifest contem caractere de controle" ;; esac
  case "$line" in *=*) ;; *) fail "linha do manifest sem =" ;; esac
  key="${line%%=*}"
  value="${line#*=}"
  [ -n "$key" ] || fail "chave vazia no manifest"
  case "$manifest_seen" in *"|$key|"*) fail "chave duplicada no manifest: $key" ;; esac
  manifest_seen="${manifest_seen}${key}|"
  case "$key" in
    format_version) m_format="$value" ;;
    backup_id) m_backup_id="$value" ;;
    created_at_utc) m_created="$value" ;;
    application_version) m_app="$value" ;;
    schema_version) m_schema="$value" ;;
    source_db_path) m_source="$value" ;;
    method) m_method="$value" ;;
    wal_consistency_method) m_wal_method="$value" ;;
    database_file) m_db_file="$value" ;;
    uploads_directory) m_uploads_dir="$value" ;;
    database_size) m_db_size="$value" ;;
    database_sha256) m_db_sha="$value" ;;
    uploads_file_count) m_upload_count="$value" ;;
    integrity_check) m_integrity="$value" ;;
    foreign_key_check) m_fk="$value" ;;
    *) fail "chave desconhecida no manifest: $key" ;;
  esac
done < "$MANIFEST"
for required in format_version backup_id created_at_utc application_version schema_version source_db_path method wal_consistency_method database_file uploads_directory database_size database_sha256 uploads_file_count integrity_check foreign_key_check; do
  case "$manifest_seen" in *"|$required|"*) ;; *) fail "chave obrigatoria ausente no manifest: $required" ;; esac
done
[ "$manifest_count" -eq 15 ] || fail "manifest deve conter exatamente 15 chaves"
[ "$m_format" = 1 ] || fail "format_version invalido"
[ "$m_method" = sqlite-backup ] || fail "method invalido"
[ "$m_wal_method" = sqlite-online-backup ] || fail "wal_consistency_method invalido"
[ "$m_db_file" = database.sqlite ] || fail "database_file invalido"
[ "$m_uploads_dir" = uploads ] || fail "uploads_directory invalido"
[ "$m_integrity" = ok ] || fail "integrity_check invalido no manifest"
[ "$m_fk" = ok ] || fail "foreign_key_check invalido no manifest"
[ -n "$m_backup_id" ] || fail "backup_id vazio"
case "$m_created" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z) ;; *) fail "created_at_utc invalido" ;; esac
[ -n "$m_app" ] || fail "application_version desconhecida"
m_app_lower="$(printf '%s' "$m_app" | tr 'A-Z' 'a-z')"
[ "$m_app_lower" != unknown ] || fail "application_version desconhecida"
case "$m_schema" in ''|*[!0-9]*) fail "schema_version invalido" ;; esac
[ "$m_schema" -gt 0 ] || fail "schema_version nao comprovado"
case "$m_db_size" in ''|*[!0-9]*) fail "database_size invalido" ;; esac
case "$m_upload_count" in ''|*[!0-9]*) fail "uploads_file_count invalido" ;; esac
case "$m_db_sha" in ''|*[!0-9a-fA-F]*) fail "database_sha256 invalido" ;; esac
[ "${#m_db_sha}" -eq 64 ] || fail "database_sha256 invalido"

EXPECTED_LIST="$(mktemp "${TMPDIR:-/tmp}/contabase-expected.XXXXXX")"
REAL_LIST="$(mktemp "${TMPDIR:-/tmp}/contabase-real.XXXXXX")"
VALIDATED_CHECKSUMS="$(mktemp "${TMPDIR:-/tmp}/contabase-validated-checksums.XXXXXX")"
: > "$EXPECTED_LIST"
: > "$VALIDATED_CHECKSUMS"
checksum_count=0
while IFS= read -r line || [ -n "$line" ]; do
  checksum_count=$((checksum_count + 1))
  case "$line" in *$'\r'*|*$'\n'*|*$'\t'*) fail "linha de checksum contem caractere de controle" ;; esac
  if [[ ! "$line" =~ ^([0-9A-Fa-f]{64})\ \ (.+)$ ]]; then
    fail "formato invalido em checksums.sha256"
  fi
  digest="${BASH_REMATCH[1]}"
  payload_path="${BASH_REMATCH[2]}"
  expected_lower="$(printf '%s' "$digest" | tr 'A-F' 'a-f')"
  case "$payload_path" in
    /*|'') fail "caminho absoluto ou vazio no checksum: $payload_path" ;;
    database.sqlite) ;;
    uploads/*) ;;
    *) fail "entrada nao permitida no checksum: $payload_path" ;;
  esac
  case "/$payload_path/" in *'/../'*|*'/./'*|*'//'*) fail "componente ambiguo no checksum: $payload_path" ;; esac
  if grep -Fqx -- "$payload_path" "$EXPECTED_LIST"; then fail "entrada duplicada no checksums: $payload_path"; fi
  printf '%s\n' "$payload_path" >> "$EXPECTED_LIST"
  printf '%s  %s\n' "$expected_lower" "$payload_path" >> "$VALIDATED_CHECKSUMS"
done < "$CHECKSUMS"
[ "$checksum_count" -gt 0 ] || fail "checksums.sha256 vazio"
LC_ALL=C sort -o "$EXPECTED_LIST" "$EXPECTED_LIST"

: > "$REAL_LIST"
printf 'database.sqlite\n' >> "$REAL_LIST"
while IFS= read -r rel; do
  case "$rel" in
    manifest.txt|checksums.sha256|database.sqlite|uploads) ;;
    uploads/*)
      entry="$PACKAGE_DIR/$rel"
      if [ -d "$entry" ]; then :
      elif [ -f "$entry" ] && [ ! -L "$entry" ]; then printf '%s\n' "$rel" >> "$REAL_LIST"
      else fail "tipo nao regular no payload: $rel"
      fi
      ;;
    *) fail "arquivo ou diretorio inesperado no pacote: $rel" ;;
  esac
done < "$SOURCE_LIST"
LC_ALL=C sort -o "$REAL_LIST" "$REAL_LIST"
if ! cmp -s "$EXPECTED_LIST" "$REAL_LIST"; then
  say_error "inventario do checksum difere do conteudo real"
  diff -u "$EXPECTED_LIST" "$REAL_LIST" >&2 || :
  exit 1
fi

while IFS= read -r line || [ -n "$line" ]; do
  digest="${line:0:64}"
  payload_path="${line:66}"
  actual_digest="$(sha_file "$PACKAGE_DIR/$payload_path")" || fail "falha ao calcular checksum: $payload_path"
  [ "$actual_digest" = "$digest" ] || fail "checksum divergente: $payload_path"
done < "$VALIDATED_CHECKSUMS"

actual_db_size="$(stat_size "$PACKAGE_DB")" || fail "falha ao medir database.sqlite"
[ "$actual_db_size" = "$m_db_size" ] || fail "database_size diverge do pacote"
actual_db_sha="$(sha_file "$PACKAGE_DB")" || fail "falha ao calcular SHA-256 do DB"
manifest_db_sha="$(printf '%s' "$m_db_sha" | tr 'A-F' 'a-f')"
[ "$actual_db_sha" = "$manifest_db_sha" ] || fail "database_sha256 diverge do pacote"
actual_upload_count=$(( $(wc -l < "$REAL_LIST") - 1 ))
[ "$actual_upload_count" = "$m_upload_count" ] || fail "uploads_file_count diverge do pacote"

db_contract_ok "$PACKAGE_DB" || fail "DB do pacote falhou integrity_check ou foreign_key_check"
if ! actual_schema="$(sqlite3 -batch -noheader "$PACKAGE_DB" 'SELECT MAX(version) FROM schema_migrations;' 2>/dev/null)"; then
  fail "schema_version nao pode ser comprovado pelo DB"
fi
[ "$actual_schema" = "$m_schema" ] || fail "schema_version diverge do DB"

STAGE_DIR="$(mktemp -d "$DATA_DIR/.restore-stage.XXXXXX")"
ROLLBACK_DIR="$(mktemp -d "$DATA_DIR/.restore-rollback.XXXXXX")"
if ! cp -p "$PACKAGE_DB" "$STAGE_DIR/database.sqlite"; then fail "falha ao copiar DB para staging"; fi
if ! mkdir "$STAGE_DIR/uploads"; then fail "falha ao criar uploads na staging"; fi
if ! cp -pR "$PACKAGE_UPLOADS/." "$STAGE_DIR/uploads/"; then fail "falha ao copiar uploads para staging"; fi

# Reconstroi toda a staging antes de abrir qualquer payload copiado.
STAGE_REAL_LIST="$(mktemp "${TMPDIR:-/tmp}/contabase-stage-real.XXXXXX")"
STAGE_ENTRIES_NUL="$(mktemp "${TMPDIR:-/tmp}/contabase-stage-entries.XXXXXX")"
: > "$STAGE_REAL_LIST"
if ! find "$STAGE_DIR" -mindepth 1 -print0 > "$STAGE_ENTRIES_NUL"; then
  fail "falha ao enumerar staging"
fi
while IFS= read -r -d '' entry; do
  rel="${entry#"$STAGE_DIR/"}"
  case "$rel" in
    ''|/*|*$'\n'*|*$'\r'*|*$'\t'*) fail "caminho inseguro no staging" ;;
  esac
  case "/$rel/" in *'/../'*|*'/./'*|*'//'*) fail "componente ambiguo no staging: $rel" ;; esac
  [ ! -L "$entry" ] || fail "symlink nao permitido no staging: $rel"
  case "$rel" in
    database.sqlite)
      [ -f "$entry" ] || fail "database.sqlite nao e arquivo regular no staging"
      [ "$(stat_nlink "$entry")" = 1 ] || fail "hardlink nao permitido no staging: $rel"
      printf '%s\n' "$rel" >> "$STAGE_REAL_LIST"
      ;;
    uploads)
      [ -d "$entry" ] || fail "uploads nao e diretorio real no staging"
      ;;
    uploads/*)
      if [ -d "$entry" ]; then
        :
      elif [ -f "$entry" ]; then
        [ "$(stat_nlink "$entry")" = 1 ] || fail "hardlink nao permitido no staging: $rel"
        printf '%s\n' "$rel" >> "$STAGE_REAL_LIST"
      else
        fail "tipo nao regular no staging: $rel"
      fi
      ;;
    *) fail "item inesperado no staging: $rel" ;;
  esac
done < "$STAGE_ENTRIES_NUL"
LC_ALL=C sort -o "$STAGE_REAL_LIST" "$STAGE_REAL_LIST"
if ! cmp -s "$EXPECTED_LIST" "$STAGE_REAL_LIST"; then
  say_error "inventario do staging difere dos checksums validados"
  diff -u "$EXPECTED_LIST" "$STAGE_REAL_LIST" >&2 || :
  exit 1
fi

staged_upload_count=$(( $(wc -l < "$STAGE_REAL_LIST") - 1 ))
[ "$staged_upload_count" = "$m_upload_count" ] || fail "uploads_file_count do staging diverge do manifest"

# Revalida cada byte da staging sem voltar a ler payloads do pacote fonte.
staged_db_size="$(stat_size "$STAGE_DIR/database.sqlite")" || fail "falha ao medir DB do staging"
[ "$staged_db_size" = "$m_db_size" ] || fail "database_size do staging diverge do manifest"
staged_db_sha="$(sha_file "$STAGE_DIR/database.sqlite")" || fail "falha ao calcular SHA-256 do DB do staging"
[ "$staged_db_sha" = "$manifest_db_sha" ] || fail "database_sha256 do staging diverge do manifest"

while IFS= read -r line || [ -n "$line" ]; do
  digest="${line:0:64}"
  payload_path="${line:66}"
  staged_digest="$(sha_file "$STAGE_DIR/$payload_path")" || fail "falha ao calcular checksum do staging: $payload_path"
  [ "$staged_digest" = "$digest" ] || fail "checksum do staging diverge: $payload_path"
done < "$VALIDATED_CHECKSUMS"

db_contract_ok "$STAGE_DIR/database.sqlite" || fail "DB da staging falhou integrity_check ou foreign_key_check"
staged_schema="$(sqlite3 -batch -noheader "$STAGE_DIR/database.sqlite" 'SELECT MAX(version) FROM schema_migrations;' 2>/dev/null)" || fail "schema da staging nao comprovado"
[ "$staged_schema" = "$m_schema" ] || fail "schema da staging diverge do manifest"

if [ -e "$DB_FILE" ] || [ -L "$DB_FILE" ]; then
  [ -f "$DB_FILE" ] && [ ! -L "$DB_FILE" ] || fail "DB atual nao e arquivo regular"
  old_db_existed=1
  old_db_mode="$(stat_mode "$DB_FILE")"; old_db_uid="$(stat_uid "$DB_FILE")"; old_db_gid="$(stat_gid "$DB_FILE")"
  old_db_sha="$(sha_file "$DB_FILE")"
fi
if [ -e "$DB_FILE-wal" ]; then
  [ -f "$DB_FILE-wal" ] && [ ! -L "$DB_FILE-wal" ] || fail "WAL atual invalido"
  old_wal_existed=1; old_wal_sha="$(sha_file "$DB_FILE-wal")"; old_wal_mode="$(stat_mode "$DB_FILE-wal")"; old_wal_uid="$(stat_uid "$DB_FILE-wal")"; old_wal_gid="$(stat_gid "$DB_FILE-wal")"
fi
if [ -e "$DB_FILE-shm" ]; then
  [ -f "$DB_FILE-shm" ] && [ ! -L "$DB_FILE-shm" ] || fail "SHM atual invalido"
  old_shm_existed=1; old_shm_sha="$(sha_file "$DB_FILE-shm")"; old_shm_mode="$(stat_mode "$DB_FILE-shm")"; old_shm_uid="$(stat_uid "$DB_FILE-shm")"; old_shm_gid="$(stat_gid "$DB_FILE-shm")"
fi
if [ -e "$UPLOADS_DIR" ] || [ -L "$UPLOADS_DIR" ]; then
  [ -d "$UPLOADS_DIR" ] && [ ! -L "$UPLOADS_DIR" ] || fail "uploads atuais nao sao diretorio regular"
  old_uploads_existed=1
  old_uploads_mode="$(stat_mode "$UPLOADS_DIR")"; old_uploads_uid="$(stat_uid "$UPLOADS_DIR")"; old_uploads_gid="$(stat_gid "$UPLOADS_DIR")"
fi
if [ "$old_db_existed" -eq 1 ] && validate_db_state_copy "$DB_FILE" "$STAGE_DIR/old-validation"; then
  old_db_contract_ok=1
fi

mutation_started=1
if [ "$old_db_existed" -eq 1 ]; then
  mv "$DB_FILE" "$ROLLBACK_DIR/old-database.sqlite" || fail "falha ao preservar DB anterior"
  old_db_saved=1
fi
if [ "$old_wal_existed" -eq 1 ]; then
  mv "$DB_FILE-wal" "$ROLLBACK_DIR/old-database.sqlite-wal" || fail "falha ao preservar WAL anterior"
  old_wal_saved=1
fi
if [ "$old_shm_existed" -eq 1 ]; then
  mv "$DB_FILE-shm" "$ROLLBACK_DIR/old-database.sqlite-shm" || fail "falha ao preservar SHM anterior"
  old_shm_saved=1
fi
if [ "$old_uploads_existed" -eq 1 ]; then
  mv "$UPLOADS_DIR" "$ROLLBACK_DIR/old-uploads" || fail "falha ao preservar uploads anteriores"
  old_uploads_saved=1
fi

mv "$STAGE_DIR/database.sqlite" "$DB_FILE" || fail "falha ao instalar novo DB"
new_db_installed=1
if [ "$old_db_existed" -eq 1 ]; then
  apply_metadata "$DB_FILE" "$old_db_mode" "$old_db_uid" "$old_db_gid" || fail "falha ao aplicar metadados do DB"
fi
mv "$STAGE_DIR/uploads" "$UPLOADS_DIR" || fail "falha ao instalar novos uploads"
new_uploads_installed=1
if [ "$old_uploads_existed" -eq 1 ]; then
  apply_metadata "$UPLOADS_DIR" "$old_uploads_mode" "$old_uploads_uid" "$old_uploads_gid" || fail "falha ao aplicar metadados dos uploads"
fi

db_contract_ok "$DB_FILE" || fail "DB instalado falhou validacao final"
[ ! -e "$DB_FILE-wal" ] && [ ! -e "$DB_FILE-shm" ] || fail "sidecars antigos permaneceram apos restore"
if [ "$old_db_existed" -eq 1 ]; then
  [ "$(stat_mode "$DB_FILE")" = "$old_db_mode" ] || fail "mode do DB nao foi preservado"
  [ "$(stat_uid "$DB_FILE")" = "$old_db_uid" ] || fail "UID do DB nao foi preservado"
  [ "$(stat_gid "$DB_FILE")" = "$old_db_gid" ] || fail "GID do DB nao foi preservado"
fi
if [ "$old_uploads_existed" -eq 1 ]; then
  [ "$(stat_mode "$UPLOADS_DIR")" = "$old_uploads_mode" ] || fail "mode dos uploads nao foi preservado"
  [ "$(stat_uid "$UPLOADS_DIR")" = "$old_uploads_uid" ] || fail "UID dos uploads nao foi preservado"
  [ "$(stat_gid "$UPLOADS_DIR")" = "$old_uploads_gid" ] || fail "GID dos uploads nao foi preservado"
fi
if ! validate_source_immutable; then fail "pacote fonte foi modificado"; fi
source_validated=1

restore_succeeded=1
echo "restore concluido com seguranca para $DB_FILE"
exit 0

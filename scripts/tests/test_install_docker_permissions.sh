#!/usr/bin/env bash
# Testes sinteticos das permissoes do instalador Docker legado.
# Nao usam Docker real, dados reais, systemd ou rede.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
INSTALLER="${ROOT_DIR}/scripts/install-contabase-docker.sh"
TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-docker-perm-test.XXXXXX")" || exit 1
TMP_BASE="$(cd "$TMP_BASE" && pwd -P)"
PASS=0
FAIL=0

cleanup() {
  if [ "${KEEP_TMP:-0}" = "1" ]; then
    printf 'TMP_BASE preservado: %s\n' "$TMP_BASE" >&2
    return
  fi
  rm -rf "$TMP_BASE"
}
trap cleanup EXIT

ok() {
  PASS=$((PASS + 1))
  printf 'ok - %s\n' "$1"
}

not_ok() {
  FAIL=$((FAIL + 1))
  printf 'not ok - %s\n' "$1"
}

file_mode() {
  if stat -c %a "$1" >/dev/null 2>&1; then
    stat -c %a "$1"
  else
    stat -f %Lp "$1"
  fi
}

file_owner() {
  if stat -c '%u:%g' "$1" >/dev/null 2>&1; then
    stat -c '%u:%g' "$1"
  else
    stat -f '%u:%g' "$1"
  fi
}

assert_mode() {
  local expected="$1" path="$2" label="$3" observed
  observed="$(file_mode "$path" 2>/dev/null || printf 'missing')"
  if [ "$observed" = "$expected" ]; then
    ok "$label"
  else
    not_ok "$label (esperado: $expected; observado: $observed)"
  fi
}

assert_equal() {
  if [ "$1" = "$2" ]; then
    ok "$3"
  else
    not_ok "$3 (esperado: $1; observado: $2)"
  fi
}

assert_fails() {
  local label="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    not_ok "$label"
  else
    ok "$label"
  fi
}

# Extrai somente as definicoes de funcoes; o fluxo principal do instalador
# comeca em `for arg in "$@"; do` e nunca e executado.
main_line="$(grep -n '^for arg in "\$@"; do$' "$INSTALLER" | head -n1 | cut -d: -f1)"
[ -n "$main_line" ] || { printf 'FAIL: fluxo principal nao localizado\n' >&2; exit 1; }
sed -n "1,$((main_line - 1))p" "$INSTALLER" > "$TMP_BASE/installer-functions.sh"
mkdir -p "$TMP_BASE/lib"
cp "$ROOT_DIR/scripts/lib/contabase-install-state.sh" "$TMP_BASE/lib/"

# Stub de chown para testar modos sem root. Registra o UID/GID solicitado.
mkdir -p "$TMP_BASE/bin"
cat > "$TMP_BASE/bin/chown" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "${CHOWN_LOG:?}"
exit 0
STUB
chmod 0755 "$TMP_BASE/bin/chown"
export CHOWN_LOG="$TMP_BASE/chown.log"
: > "$CHOWN_LOG"
export PATH="$TMP_BASE/bin:$PATH"

# shellcheck disable=SC1091
source "$TMP_BASE/installer-functions.sh"
set +e
as_root() { "$@"; }

WORK="$TMP_BASE/work"
mkdir -p "$WORK"
cd "$WORK" || exit 1

# 1. Umask do instalador restringe arquivos novos.
umask_observed="$(umask)"
assert_equal "0077" "$umask_observed" "instalador define umask 0077"
printf 'novo\n' > novo-arquivo.txt
assert_mode "600" "novo-arquivo.txt" "arquivo novo criado com 0600"

# 2. Env existente 0644 e backup viram 0600 sem mudar ownership.
printf 'CONTABASE_SETUP_TOKEN=synthetic\nAUTH_ENCRYPTION_KEY=synthetic\nSECURITY_MASTER_KEY=synthetic\n' > .env.docker
chmod 0644 .env.docker
env_owner_before="$(file_owner .env.docker)"
env_backup ".env.docker" >/dev/null
backup_path="$(find . -maxdepth 1 -name '.env.docker.*.bak' -print -quit)"
[ -n "$backup_path" ] && ok "backup do env criado" || not_ok "backup do env criado"
assert_mode "600" "$backup_path" "backup do env criado com 0600"
secure_env_file ".env.docker"
assert_mode "600" ".env.docker" "env existente restringido para 0600"
assert_equal "$env_owner_before" "$(file_owner .env.docker)" "ownership do env preservado"
set_env_var "CONTABASE_TIMEZONE" "America/Sao_Paulo" ".env.docker"
assert_mode "600" ".env.docker" "env permanece 0600 apos alteracao"
grep -q '^CONTABASE_TIMEZONE=America/Sao_Paulo$' .env.docker \
  && ok "env preserva valor gravado" || not_ok "env preserva valor gravado"

# 3. Data existente insegura vira 0700/0600, incluindo DB/WAL/SHM/backups/uploads.
mkdir -p data/backups data/uploads/profile
printf 'db\n' > data/contabase.db
printf 'wal\n' > data/contabase.db-wal
printf 'shm\n' > data/contabase.db-shm
printf 'backup\n' > data/backups/antigo.sqlite
printf 'upload\n' > data/uploads/profile/foto.png
chmod 0755 data data/backups data/uploads data/uploads/profile
chmod 0644 data/contabase.db data/contabase.db-wal data/contabase.db-shm \
  data/backups/antigo.sqlite data/uploads/profile/foto.png
data_owner_before="$(file_owner data)"
printf 'fora\n' > "$TMP_BASE/outside.txt"
chmod 0644 "$TMP_BASE/outside.txt"
ln -s "$TMP_BASE/outside.txt" data/outside-link

secure_data_permissions
assert_equal "$data_owner_before" "$(file_owner data)" "ownership de data/ preservado quando ja correto"
assert_mode "700" data "data/ restrito a 0700"
assert_mode "700" data/backups "data/backups restrito a 0700"
assert_mode "700" data/uploads "data/uploads restrito a 0700"
assert_mode "700" data/uploads/profile "subdiretorio de uploads restrito a 0700"
assert_mode "600" data/contabase.db "SQLite restrito a 0600"
assert_mode "600" data/contabase.db-wal "WAL restrito a 0600"
assert_mode "600" data/contabase.db-shm "SHM restrito a 0600"
assert_mode "600" data/backups/antigo.sqlite "backup existente restrito a 0600"
assert_mode "600" data/uploads/profile/foto.png "upload existente restrito a 0600"
assert_mode "644" "$TMP_BASE/outside.txt" "symlink nao altera alvo externo"
[ -L data/outside-link ] && ok "symlink preservado" || not_ok "symlink preservado"
grep -q '1000:1000' "$CHOWN_LOG" && ok "ownership solicitado como 1000:1000" \
  || {
    if [ ! -s "$CHOWN_LOG" ]; then
      ok "ownership ja pertencente ao UID 1000 preservado"
    else
      not_ok "ownership solicitado como 1000:1000"
    fi
  }

# 4. Idempotencia: segunda execucao nao piora nem falha.
secure_data_permissions
assert_mode "700" data "segunda execucao mantem data/ 0700"
assert_mode "600" data/contabase.db "segunda execucao mantem SQLite 0600"

# 5. Arquivos criados depois do start sao restringidos.
chmod 0644 data/contabase.db data/contabase.db-wal data/contabase.db-shm
secure_runtime_data_files
assert_mode "600" data/contabase.db "pos-start restringe SQLite"
assert_mode "600" data/contabase.db-wal "pos-start restringe WAL"
assert_mode "600" data/contabase.db-shm "pos-start restringe SHM"

# 6. data/ como symlink falha fechado e nao segue o alvo.
mkdir -p "$TMP_BASE/symlink-case"
printf 'alvo\n' > "$TMP_BASE/symlink-target"
chmod 0644 "$TMP_BASE/symlink-target"
ln -s "$TMP_BASE/symlink-target" "$TMP_BASE/symlink-case/data"
(
  cd "$TMP_BASE/symlink-case" || exit 1
  secure_data_permissions
) >/dev/null 2>&1
rc=$?
assert_equal "1" "$rc" "data/ symlink e rejeitado"
assert_mode "644" "$TMP_BASE/symlink-target" "alvo do symlink data/ nao e alterado"

# 7. Env inexistente nao falha.
rm -f .env.docker
secure_env_file ".env.docker"
assert_equal "0" "$?" "env ausente e aceito sem erro"

printf '\nRESULT docker-permissions: %d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

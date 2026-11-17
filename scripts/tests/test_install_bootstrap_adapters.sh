#!/usr/bin/env bash
# Testes sinteticos dos adaptadores Binario/Source do Bootstrap Beta 8.
# Nao executam instalacao, systemd, Docker, banco ou rede real.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LIB="${ROOT_DIR}/scripts/lib/contabase-bootstrap-adapter.sh"
RELEASE="${ROOT_DIR}/scripts/install-contabase-release.sh"
SOURCE="${ROOT_DIR}/scripts/install-contabase-source.sh"
TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-bootstrap-adapter-test.XXXXXX")" || exit 1
TMP_BASE="$(cd "$TMP_BASE" && pwd -P)"
TEST_VERSION="v1.0.0-beta.8"
TEST_COMMIT="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
TEST_SHA="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
TEST_SHA2="cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
SENTINEL="SYNTHETIC-SECRET-MUST-NOT-LEAK"
PASS=0
FAIL=0
PASS_FILE="${TMP_BASE}/.pass-count"
FAIL_FILE="${TMP_BASE}/.fail-count"
printf '0\n' > "$PASS_FILE"
printf '0\n' > "$FAIL_FILE"

cleanup() {
  if [ "${KEEP_TMP:-0}" = "1" ]; then
    printf 'TMP_BASE preservado: %s\n' "$TMP_BASE" >&2
    return
  fi
  rm -rf "$TMP_BASE"
}
trap cleanup EXIT

ok() {
  local count
  count="$(cat "$PASS_FILE")"
  printf '%s\n' $((count + 1)) > "$PASS_FILE"
  printf 'ok - %s\n' "$1"
}

not_ok() {
  local count
  count="$(cat "$FAIL_FILE")"
  printf '%s\n' $((count + 1)) > "$FAIL_FILE"
  printf 'not ok - %s\n' "$1"
}

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

run_pass() {
  local label="$1"
  shift
  if ( "$@" ) >/dev/null 2>&1; then ok "$label"; else not_ok "$label"; fi
}

run_fail() {
  local label="$1"
  shift
  if ( "$@" ) >/dev/null 2>&1; then not_ok "$label (aceito indevidamente)"; else ok "$label"; fi
}

t_release_contract() {
  . "$LIB"
  bootstrap_validate_release_contract
}

t_release_cli_rejected() {
  local output status
  output="$(bash "$RELEASE" --validate-only 2>&1)"
  status=$?
  [ "$status" -ne 0 ] || return 1
  case "$output" in
    *"adaptador Bootstrap nao aceita argumentos avulsos"*) return 0 ;;
    *) return 1 ;;
  esac
}

t_source_contract() {
  . "$LIB"
  bootstrap_validate_source_contract
}

t_network() {
  . "$LIB"
  bootstrap_validate_network "$1"
}

t_paths() {
  . "$LIB"
  bootstrap_validate_paths release
}

t_existing() {
  . "$LIB"
  bootstrap_validate_no_existing_state
}

t_unit_lines() {
  . "$LIB"
  bootstrap_unit_network_lines
}

t_digest() {
  . "$LIB"
  bootstrap_verify_file_digest "$1" "$2" "teste"
}

t_checkout() {
  . "$LIB"
  bootstrap_verify_checkout_identity "$1" "$2" "$3"
}

export CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1
export CONTABASE_INSTALL_MODE=initial
export CONTABASE_CONFIRM_INSTALL=INSTALAR
export CONTABASE_BOOTSTRAP_ADAPTER=release
export CONTABASE_VERSION="$TEST_VERSION"
export CONTABASE_CHANNEL=beta
export CONTABASE_COMMIT="$TEST_COMMIT"
export CONTABASE_FEATURE_INVESTMENTS=false
export CONTABASE_INSTALL_DEPS=0
export CONTABASE_HELPER_SHA256="$TEST_SHA"
export CONTABASE_ADAPTER_LIB_SHA256="$TEST_SHA2"
export CONTABASE_ARTIFACT_SHA256="$TEST_SHA"
export CONTABASE_CHECKSUMS_SHA256="$TEST_SHA2"
export CONTABASE_UPDATE_WRAPPER_SHA256="$TEST_SHA2"
export CONTABASE_ARTIFACT_NAME=contabase-linux-arm64.tar.gz
export CONTABASE_ARTIFACT_PLATFORM=linux/arm64
export CONTABASE_ARTIFACT_PATH="${TMP_BASE}/contabase-linux-arm64.tar.gz"
export CONTABASE_CHECKSUMS_PATH="${TMP_BASE}/checksums.txt"
export CONTABASE_SOURCE_ARCHIVE="${TMP_BASE}/source.tar.gz"
export CONTABASE_SOURCE_ARCHIVE_SHA256="$TEST_SHA"
export CONTABASE_SOURCE_ARCHIVE_KIND=github-tarball
export CONTABASE_INSTALL_DIR="${TMP_BASE}/install"
export CONTABASE_DATA_DIR="${TMP_BASE}/data"
export CONTABASE_CONFIG_DIR="${TMP_BASE}/config"
export CONTABASE_PORT=18080
export CONTABASE_INTERNAL_PORT=18080
export CONTABASE_HOST_BIND=127.0.0.1
export CONTABASE_NETWORK_PROFILE=local
export CONTABASE_ACCESS_MODE=local
export APP_BASE_URL=http://localhost:18080
export ALLOWED_HOSTS=localhost,127.0.0.1,::1
export TRUSTED_PROXIES=
export CONTABASE_SERVICE_FILE="${TMP_BASE}/contabase.service"
export CONTABASE_STATE_FILE="${TMP_BASE}/install-state.env"
export CONTABASE_MODE_FILE="${TMP_BASE}/install-mode"
export CONTABASE_ENV_FILE="${TMP_BASE}/contabase.env"
export CONTABASE_UPDATE_BIN="${TMP_BASE}/contabase-update"
export CONTABASE_CB_UPDATE_BIN="${TMP_BASE}/cb-update"
export CONTABASE_TEST_SKIP_SYSTEMD=1
export CONTABASE_TEST_SKIP_DEFAULT_PATHS=1

mkdir -p "$TMP_BASE/systemd" "$TMP_BASE/state" "$TMP_BASE/bin"
printf 'artifact\n' > "$CONTABASE_ARTIFACT_PATH"
printf '%s  contabase-linux-arm64.tar.gz\n' "$(sha_file "$CONTABASE_ARTIFACT_PATH")" > "$CONTABASE_CHECKSUMS_PATH"
export CONTABASE_ARTIFACT_SHA256="$(sha_file "$CONTABASE_ARTIFACT_PATH")"
export CONTABASE_CHECKSUMS_SHA256="$(sha_file "$CONTABASE_CHECKSUMS_PATH")"
printf 'source archive\n' > "$CONTABASE_SOURCE_ARCHIVE"
export CONTABASE_SOURCE_ARCHIVE_SHA256="$(sha_file "$CONTABASE_SOURCE_ARCHIVE")"

# 1-3. Contrato Release.
run_pass "contrato Release sintetico valido" t_release_contract
run_pass "Release Bootstrap rejeita --validate-only antes do parser legado" t_release_cli_rejected
CONTABASE_VERSION=main run_fail "branch como versao bloqueada" t_release_contract
CONTABASE_CHANNEL=stable run_fail "beta em canal stable bloqueada" t_release_contract
CONTABASE_VERSION=v1.0.0 run_fail "stable sem proveniencia no adaptador beta" t_release_contract

# 4-10. Identidade do artifact.
CONTABASE_COMMIT=abc run_fail "commit curto bloqueado" t_release_contract
CONTABASE_BOOTSTRAP_ADAPTER=source run_fail "adaptador divergente bloqueado" t_release_contract
CONTABASE_FEATURE_INVESTMENTS=true run_fail "investimentos true bloqueado" t_release_contract
CONTABASE_INSTALL_DEPS=1 run_fail "instalacao automatica de dependencias bloqueada" t_release_contract
CONTABASE_ARTIFACT_NAME=contabase-linux-amd64.tar.gz run_fail "artifact de outra arquitetura bloqueado" t_release_contract
CONTABASE_ARTIFACT_PLATFORM=linux/riscv64 run_fail "plataforma desconhecida bloqueada" t_release_contract
CONTABASE_ARTIFACT_SHA256=short run_fail "digest curto do artifact bloqueado" t_release_contract
CONTABASE_CHECKSUMS_SHA256=short run_fail "digest curto de checksums bloqueado" t_release_contract

# 11-13. Contrato Source.
CONTABASE_BOOTSTRAP_ADAPTER=source run_pass "contrato Source sintetico valido" t_source_contract
CONTABASE_SOURCE_ARCHIVE_KIND=branch run_fail "origem Source por branch bloqueada" t_source_contract
CONTABASE_SOURCE_ARCHIVE_SHA256=short run_fail "digest curto do archive Source bloqueado" t_source_contract

# 14-24. Rede.
CONTABASE_BOOTSTRAP_ADAPTER=release run_pass "perfil local valido" t_network release
CONTABASE_NETWORK_PROFILE=lan CONTABASE_ACCESS_MODE=lan CONTABASE_HOST_BIND=192.168.1.20 \
  APP_BASE_URL=http://192.168.1.20:18080 ALLOWED_HOSTS=192.168.1.20 TRUSTED_PROXIES= \
  run_pass "perfil LAN privado valido" t_network release
CONTABASE_NETWORK_PROFILE=proxy CONTABASE_ACCESS_MODE=proxy CONTABASE_HOST_BIND=127.0.0.1 \
  APP_BASE_URL=https://fin.example.test ALLOWED_HOSTS=fin.example.test TRUSTED_PROXIES=127.0.0.1/32 \
  run_pass "perfil proxy HTTPS valido" t_network release
CONTABASE_NETWORK_PROFILE=tunnel CONTABASE_ACCESS_MODE=proxy CONTABASE_HOST_BIND=127.0.0.1 \
  APP_BASE_URL=https://fin.example.test ALLOWED_HOSTS=fin.example.test TRUSTED_PROXIES=127.0.0.1/32 \
  run_pass "perfil tunnel usa contrato proxy" t_network release
CONTABASE_HOST_BIND=0.0.0.0 run_fail "bind 0.0.0.0 bloqueado" t_network release
CONTABASE_NETWORK_PROFILE=lan CONTABASE_ACCESS_MODE=lan CONTABASE_HOST_BIND=8.8.8.8 \
  APP_BASE_URL=http://8.8.8.8:18080 ALLOWED_HOSTS=8.8.8.8 run_fail "LAN com IP publico bloqueada" t_network release
CONTABASE_NETWORK_PROFILE=proxy CONTABASE_ACCESS_MODE=proxy CONTABASE_HOST_BIND=127.0.0.1 \
  APP_BASE_URL=https://fin.example.test ALLOWED_HOSTS=fin.example.test TRUSTED_PROXIES= \
  run_fail "proxy sem TRUSTED_PROXIES bloqueado" t_network release
CONTABASE_NETWORK_PROFILE=proxy CONTABASE_ACCESS_MODE=proxy CONTABASE_HOST_BIND=127.0.0.1 \
  APP_BASE_URL=https://fin.example.test ALLOWED_HOSTS=fin.example.test TRUSTED_PROXIES=0.0.0.0/0 \
  run_fail "proxy universal bloqueado" t_network release
CONTABASE_NETWORK_PROFILE=proxy CONTABASE_ACCESS_MODE=proxy CONTABASE_HOST_BIND=127.0.0.1 \
  APP_BASE_URL=http://fin.example.test ALLOWED_HOSTS=fin.example.test TRUSTED_PROXIES=127.0.0.1/32 \
  run_fail "proxy HTTP publico bloqueado" t_network release
CONTABASE_PORT=18081 run_fail "porta da URL divergente bloqueada" t_network release
ALLOWED_HOSTS='*' run_fail "wildcard de hosts bloqueado" t_network release
CONTABASE_NETWORK_PROFILE=proxy CONTABASE_ACCESS_MODE=proxy CONTABASE_HOST_BIND=127.0.0.1 \
  APP_BASE_URL=https://fin.example.test ALLOWED_HOSTS=fin.example.test TRUSTED_PROXIES=10.0.0.0/8 \
  run_fail "proxy /8 amplo bloqueado" t_network release
CONTABASE_INTERNAL_PORT=8080 run_fail "porta interna divergente bloqueada" t_network release

# 25-31. Paths.
run_pass "paths novos e separados aceitos" t_paths
CONTABASE_DATA_DIR="$CONTABASE_INSTALL_DIR" run_fail "instalacao igual a dados bloqueada" t_paths
CONTABASE_DATA_DIR="${CONTABASE_INSTALL_DIR}/data" run_fail "dados dentro da instalacao bloqueados" t_paths
CONTABASE_CONFIG_DIR=/etc run_fail "diretorio generico bloqueado" t_paths
mkdir -p "${TMP_BASE}/existing"
CONTABASE_INSTALL_DIR="${TMP_BASE}/existing" run_fail "diretorio existente bloqueado" t_existing
ln -s "${TMP_BASE}/existing" "${TMP_BASE}/link"
CONTABASE_INSTALL_DIR="${TMP_BASE}/link/new" run_fail "symlink no path bloqueado" t_paths
CONTABASE_INSTALL_DIR=relative run_fail "path relativo bloqueado" t_paths

# 32-37. Estado existente.
run_pass "sem estado previo aceito" t_existing
printf 'x\n' > "$CONTABASE_STATE_FILE"
run_fail "install-state existente bloqueia" t_existing
rm -f "$CONTABASE_STATE_FILE"
printf 'x\n' > "$CONTABASE_ENV_FILE"
run_fail "contabase.env existente bloqueia" t_existing
rm -f "$CONTABASE_ENV_FILE"
printf 'x\n' > "$CONTABASE_SERVICE_FILE"
run_fail "unit systemd existente bloqueia" t_existing
rm -f "$CONTABASE_SERVICE_FILE"
printf 'x\n' > "$CONTABASE_UPDATE_BIN"
run_fail "contabase-update existente bloqueia" t_existing
rm -f "$CONTABASE_UPDATE_BIN"
mkdir -p "$CONTABASE_DATA_DIR"
run_fail "diretorio de dados existente bloqueia" t_existing
rmdir "$CONTABASE_DATA_DIR"

# 38-40. Criacao exclusiva.
t_create() {
  . "$LIB"
  bootstrap_create_dir_exclusive "$1" "$(id -un)" "$(id -gn)" 0755
}
run_pass "criacao exclusiva de diretorio novo" t_create "${TMP_BASE}/created"
run_fail "segunda criacao nao sobrescreve" t_create "${TMP_BASE}/created"
run_fail "criacao sem diretorio pai falha" t_create "${TMP_BASE}/missing/child"

# 41-43. Unit systemd.
lines="$(CONTABASE_ACCESS_MODE=local t_unit_lines)"
case "$lines" in *"IPAddressDeny=any"*"IPAddressAllow=localhost"*) ok "unit local restringe loopback" ;; *) not_ok "unit local sem restricao" ;; esac
lines="$(CONTABASE_ACCESS_MODE=lan t_unit_lines)"
case "$lines" in *"192.168.0.0/16"*) ok "unit LAN limita redes privadas" ;; *) not_ok "unit LAN sem redes privadas" ;; esac
lines="$(CONTABASE_ACCESS_MODE=proxy t_unit_lines)"
case "$lines" in *"IPAddressAllow=localhost"*) ok "unit proxy restringe loopback" ;; *) not_ok "unit proxy sem loopback" ;; esac

# 44-46. Digests.
run_pass "digest de arquivo valido" t_digest "$CONTABASE_ARTIFACT_PATH" "$(sha_file "$CONTABASE_ARTIFACT_PATH")"
run_fail "digest adulterado bloqueado" t_digest "$CONTABASE_ARTIFACT_PATH" "$TEST_SHA"
ln -s "$CONTABASE_ARTIFACT_PATH" "${TMP_BASE}/artifact-link"
run_fail "digest de symlink bloqueado" t_digest "${TMP_BASE}/artifact-link" "$(sha_file "$CONTABASE_ARTIFACT_PATH")"

# 47-50. Checkout Source.
checkout="${TMP_BASE}/checkout"
mkdir -p "$checkout"
(
  cd "$checkout" || exit 1
  git init -q
  git config user.email synthetic@example.test
  git config user.name Synthetic
  printf '%s\n' "$TEST_VERSION" > VERSION
  git add VERSION
  git commit -qm synthetic
  git checkout -q --detach
) || exit 1
commit="$(git -C "$checkout" rev-parse HEAD)"
run_pass "checkout destacado na tag exata aceito" t_checkout "$checkout" "$TEST_VERSION" "$commit"
git -C "$checkout" checkout -q -b local-branch
run_fail "checkout em branch movel bloqueado" t_checkout "$checkout" "$TEST_VERSION" "$commit"
git -C "$checkout" checkout -q --detach "$commit"
printf 'dirty\n' > "$checkout/untracked"
run_fail "checkout sujo bloqueado" t_checkout "$checkout" "$TEST_VERSION" "$commit"
rm -f "$checkout/untracked"
run_fail "commit divergente bloqueado" t_checkout "$checkout" "$TEST_VERSION" "$TEST_COMMIT"
run_fail "VERSION divergente bloqueada" t_checkout "$checkout" "v1.0.0-beta.9" "$commit"

# 51-54. Entrada dos instaladores: falha antes de escrita e sem vazar sentinelas.
empty_home="${TMP_BASE}/empty-home"
mkdir -p "$empty_home"
release_sha="$(sha_file "$RELEASE")"
lib_sha="$(sha_file "$LIB")"
source_sha="$(sha_file "$SOURCE")"
wrapper_sha="$(sha_file "${ROOT_DIR}/scripts/lib/contabase-update-wrapper.sh")"
release_output="$(
  env -i PATH="$PATH" HOME="$empty_home" TMPDIR="$empty_home" \
    CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1 CONTABASE_INSTALL_MODE=initial \
    CONTABASE_CONFIRM_INSTALL=INSTALAR CONTABASE_BOOTSTRAP_ADAPTER=release \
    CONTABASE_VERSION=main CONTABASE_CHANNEL=beta CONTABASE_COMMIT="$TEST_COMMIT" \
    CONTABASE_FEATURE_INVESTMENTS=false CONTABASE_INSTALL_DEPS=0 \
    CONTABASE_HELPER_SHA256="$release_sha" CONTABASE_ADAPTER_LIB_SHA256="$lib_sha" \
    CONTABASE_UPDATE_WRAPPER_SHA256="$wrapper_sha" \
    CONTABASE_BOOTSTRAP_CHECKOUT="${ROOT_DIR}/scripts" \
    CONTABASE_SETUP_TOKEN="$SENTINEL" CONTABASE_TEST_SKIP_SYSTEMD=1 \
    CONTABASE_INSTALL_DIR="${TMP_BASE}/never-install" CONTABASE_DATA_DIR="${TMP_BASE}/never-data" \
    CONTABASE_CONFIG_DIR="${TMP_BASE}/never-config" \
    bash "$RELEASE" 2>&1
)"
status=$?
[ "$status" -ne 0 ] && ok "Release rejeita contrato invalido" || not_ok "Release aceitou contrato invalido"
case "$release_output" in *"versao publica invalida"*) ok "Release chega ao contrato antes de escrever" ;; *) not_ok "Release nao chegou ao contrato (saida inesperada)" ;; esac
case "$release_output" in *"$SENTINEL"*) not_ok "Release vazou segredo sintetico" ;; *) ok "Release nao vaza segredo sintetico" ;; esac
[ ! -e "${TMP_BASE}/never-install" ] && [ ! -e "${TMP_BASE}/never-data" ] && [ ! -e "${TMP_BASE}/never-config" ] \
  && ok "Release falhou sem criar diretorios" || not_ok "Release criou diretorios antes de validar"

source_output="$(
  env -i PATH="$PATH" HOME="$empty_home" TMPDIR="$empty_home" \
    CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1 CONTABASE_INSTALL_MODE=initial \
    CONTABASE_CONFIRM_INSTALL=INSTALAR CONTABASE_BOOTSTRAP_ADAPTER=source \
    CONTABASE_VERSION=main CONTABASE_CHANNEL=beta CONTABASE_COMMIT="$TEST_COMMIT" \
    CONTABASE_FEATURE_INVESTMENTS=false CONTABASE_INSTALL_DEPS=0 \
    CONTABASE_HELPER_SHA256="$source_sha" CONTABASE_ADAPTER_LIB_SHA256="$lib_sha" \
    CONTABASE_BOOTSTRAP_CHECKOUT="$ROOT_DIR" \
    CONTABASE_SETUP_TOKEN="$SENTINEL" CONTABASE_TEST_SKIP_SYSTEMD=1 \
    CONTABASE_INSTALL_DIR="${TMP_BASE}/never-source" CONTABASE_DATA_DIR="${TMP_BASE}/never-source-data" \
    CONTABASE_CONFIG_DIR="${TMP_BASE}/never-source-config" \
    bash "$SOURCE" 2>&1
)"
status=$?
[ "$status" -ne 0 ] && ok "Source Bootstrap indisponivel" || not_ok "Source Bootstrap iniciou"
case "$source_output" in *"Source Bootstrap indisponivel"*) ok "Source recusa Bootstrap antes de escrever" ;; *) not_ok "Source sem diagnostico de adiamento" ;; esac
case "$source_output" in *"$SENTINEL"*) not_ok "Source vazou segredo sintetico" ;; *) ok "Source nao vaza segredo sintetico" ;; esac

# 55. Updater reconhece release e falha fechado para source-bootstrap.
printf 'CONTABASE_STATE_VERSION=1\nCONTABASE_INSTALL_METHOD=release\nCONTABASE_CHANNEL=pinned\nCONTABASE_INSTALLED_VERSION=v1.0.0-beta.7\nCONTABASE_REPO_PATH=\n' > "${TMP_BASE}/release-state.env"
output="$(env -i PATH="$PATH" CONTABASE_STATE_FILE="${TMP_BASE}/release-state.env" \
  CONTABASE_MODE_FILE="${TMP_BASE}/no-mode" CONTABASE_ENV_FILE="${TMP_BASE}/no-env" \
  CONTABASE_UPDATE_RESOLVE_ONLY=1 bash "${ROOT_DIR}/scripts/lib/contabase-update-wrapper.sh" "$TEST_VERSION" 2>&1)"
case "$output" in *"metodo=release"*) ok "updater reconhece estado release" ;; *) not_ok "updater nao reconhece release" ;; esac
printf 'CONTABASE_STATE_VERSION=1\nCONTABASE_INSTALL_METHOD=source-bootstrap\nCONTABASE_CHANNEL=beta\nCONTABASE_INSTALLED_VERSION=%s\n' "$TEST_VERSION" > "${TMP_BASE}/source-state.env"
output="$(env -i PATH="$PATH" CONTABASE_STATE_FILE="${TMP_BASE}/source-state.env" \
  CONTABASE_MODE_FILE="${TMP_BASE}/no-mode" CONTABASE_ENV_FILE="${TMP_BASE}/no-env" \
  CONTABASE_UPDATE_RESOLVE_ONLY=1 bash "${ROOT_DIR}/scripts/lib/contabase-update-wrapper.sh" "$TEST_VERSION" 2>&1)"
status=$?
[ "$status" -ne 0 ] && ok "updater falha fechado para source-bootstrap" || not_ok "updater aceita source-bootstrap"
case "$output" in *"desconhecido"*) ok "updater informa modo desconhecido" ;; *) not_ok "updater sem mensagem de modo desconhecido" ;; esac

# 56-61. Toolchain, dependencias e healthcheck (funcoes extraidas; sem systemd real).
extract_function() {
  awk -v name="$1" '$0 ~ "^" name "\\(\\) \\{" { f=1 } f { print } f && /^}$/ { exit }' "$2"
}

toolchain="${TMP_BASE}/toolchain"
mkdir -p "$toolchain/bin" "$toolchain/work"
printf 'module synthetic\n\ngo 1.26.10\n' > "$toolchain/work/go.mod"
cat > "$toolchain/bin/node" <<'NODE'
#!/usr/bin/env bash
printf '%s\n' "${TEST_NODE_VERSION:-v24.0.0}"
NODE
cat > "$toolchain/bin/go" <<'GO'
#!/usr/bin/env bash
printf '%s\n' "${TEST_GO_VERSION:-go version go1.26.10 linux/amd64}"
GO
chmod +x "$toolchain/bin/node" "$toolchain/bin/go"

run_node_check() {
  (
    PATH="${toolchain}/bin:${PATH}"
    cd "$toolchain/work" || exit 1
    log_error() { :; }
    log_ok() { :; }
    log_warn() { :; }
    eval "$(extract_function require_node_version "$SOURCE")"
    require_node_version
  ) >/dev/null 2>&1
}
run_go_check() {
  (
    PATH="${toolchain}/bin:${PATH}"
    cd "$toolchain/work" || exit 1
    log_error() { :; }
    log_ok() { :; }
    log_warn() { :; }
    eval "$(extract_function require_go_version "$SOURCE")"
    require_go_version
  ) >/dev/null 2>&1
}
run_pass "Node 24 aceito pelo contrato Source" run_node_check
TEST_NODE_VERSION=v22.0.0 run_pass "Node 22 aceito pelo contrato Source" run_node_check
TEST_NODE_VERSION=v18.0.0 run_fail "Node 18 rejeitado pelo contrato Source" run_node_check
run_pass "Go compativel aceito pelo contrato Source" run_go_check
TEST_GO_VERSION='go version go1.19.0 linux/amd64' run_fail "Go incompativel rejeitado pelo contrato Source" run_go_check

run_missing_commands() {
  (
    PATH="/nonexistent"
    log_error() { :; }
    eval "$(extract_function require_flock "$SOURCE")"
    eval "$(extract_function require_commands "$SOURCE")"
    require_commands
  ) >/dev/null 2>&1
}
run_fail "dependencias ausentes bloqueiam sem instalacao automatica" run_missing_commands

health_stub="${TMP_BASE}/health-stub"
mkdir -p "$health_stub"
cat > "$health_stub/curl" <<'CURL'
#!/usr/bin/env bash
printf '%s' '{"status":"unhealthy"}'
CURL
chmod +x "$health_stub/curl"
run_unhealthy_health() {
  (
    PATH="${health_stub}:${PATH}"
    HEALTH_URL="http://127.0.0.1:18080/health"
    log_warn() { :; }
    sleep() { :; }
    eval "$(extract_function wait_for_healthcheck "$SOURCE")"
    wait_for_healthcheck
  ) >/dev/null 2>&1
}
run_fail "healthcheck unhealthy impede sucesso" run_unhealthy_health

# 62-64. Ordem de estado e preservacao de SQLite/WAL sinteticos.
if [ "$(grep -n 'start_and_check_service' "$SOURCE" | tail -n1 | cut -d: -f1)" -lt "$(grep -n 'install_update_command' "$SOURCE" | tail -n1 | cut -d: -f1)" ]; then
  ok "Source registra estado depois de health/runtime"
else
  not_ok "Source registra estado antes de health/runtime"
fi
if [ "$(grep -n 'verify_running_release' "$RELEASE" | tail -n1 | cut -d: -f1)" -lt "$(grep -n 'install_update_command' "$RELEASE" | tail -n1 | cut -d: -f1)" ]; then
  ok "Release registra estado depois de runtime"
else
  not_ok "Release registra estado antes de runtime"
fi
preserve="${TMP_BASE}/preserve-data"
mkdir -p "$preserve"
printf 'sqlite-db\n' > "$preserve/contabase.db"
printf 'sqlite-wal\n' > "$preserve/contabase.db-wal"
printf 'sqlite-shm\n' > "$preserve/contabase.db-shm"
before="$(sha_file "$preserve/contabase.db")$(sha_file "$preserve/contabase.db-wal")$(sha_file "$preserve/contabase.db-shm")"
CONTABASE_DATA_DIR="$preserve" run_fail "dados SQLite/WAL existentes bloqueiam reinstalacao" t_existing
after="$(sha_file "$preserve/contabase.db")$(sha_file "$preserve/contabase.db-wal")$(sha_file "$preserve/contabase.db-shm")"
[ "$before" = "$after" ] && ok "SQLite/WAL/SHM sinteticos preservados" || not_ok "SQLite/WAL/SHM alterados"

PASS="$(cat "$PASS_FILE")"
FAIL="$(cat "$FAIL_FILE")"
printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

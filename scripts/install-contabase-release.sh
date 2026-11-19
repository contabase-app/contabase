#!/usr/bin/env bash
# O instalador le secrets existentes; xtrace pode expor seus valores.
set +x
set -euo pipefail

REPO="${CONTABASE_REPO:-https://github.com/contabase-app/contabase}"
TAG="${CONTABASE_VERSION:-}"
SCRIPT_SOURCE="${BASH_SOURCE[0]:-$0}"
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_SOURCE")" 2>/dev/null && pwd)"
PUBLIC_RAW_BASE="${CONTABASE_RAW_BASE:-https://raw.githubusercontent.com/contabase-app/contabase}"
PORT_WAS_SET=false
if [ -n "${CONTABASE_PORT+x}" ]; then
  PORT_WAS_SET=true
  PORT="${CONTABASE_PORT}"
elif [ -n "${PORT+x}" ]; then
  PORT_WAS_SET=true
  PORT="${PORT}"
else
  PORT="8080"
fi
APP_BASE_URL_VALUE="${APP_BASE_URL:-}"
ALLOWED_HOSTS_VALUE="${ALLOWED_HOSTS:-}"
TRUSTED_PROXIES_VALUE="${TRUSTED_PROXIES:-}"
CONTABASE_ACCESS_MODE_VALUE="${CONTABASE_ACCESS_MODE:-}"
CONTABASE_HOST_BIND_VALUE="${CONTABASE_HOST_BIND:-}"
CONTABASE_NETWORK_PROFILE_VALUE="${CONTABASE_NETWORK_PROFILE:-}"
CONTABASE_TIMEZONE_VALUE="${CONTABASE_TIMEZONE:-America/Sao_Paulo}"
BOOTSTRAP_MODE=false
if [ "${CONTABASE_BOOTSTRAP_CONTRACT_VERSION:-}" = "1" ]; then
  BOOTSTRAP_MODE=true
fi
# Prova de sanitizacao vive em argv (marcador do reexec), nunca no ambiente;
# a inicializacao incondicional descarta valor herdado do chamador.
BOOTSTRAP_SANITIZED_ENTRY=false
SETUP_TOKEN_GENERATED_NOW=false
SETUP_TOKEN_SUPPLIED_BY_ENV=false
APP_USER="${CONTABASE_USER:-contabase}"
INSTALL_DIR="${CONTABASE_INSTALL_DIR:-/opt/contabase}"
DATA_DIR="${CONTABASE_DATA_DIR:-/var/lib/contabase}"
CONFIG_DIR="${CONTABASE_CONFIG_DIR:-/etc/contabase}"
STATE_FILE="${CONFIG_DIR}/install-state.env"
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-pinned}"
ASSUME_YES="${CONTABASE_ASSUME_YES:-0}"
HEALTHCHECK_ATTEMPTS="${CONTABASE_HEALTHCHECK_ATTEMPTS:-30}"
VALIDATE_ONLY=false
ARCH=""
ARTIFACT_NAME=""
ARTIFACT_URL=""
CHECKSUMS_URL=""
TMP_DIR=""
EXTRACT_DIR=""
STAGING_DIR=""
PREVIOUS_DIR=""
ENV_FILE="${CONFIG_DIR}/contabase.env"
SERVICE_FILE="${CONTABASE_SERVICE_FILE:-/etc/systemd/system/contabase.service}"
SERVICE_BACKUP=""
NEW_BUNDLE_ACTIVE=false
NEW_RUNTIME_STARTED=false
RELEASE_BUNDLE_VERIFIED=false
BUNDLE_WRAPPER_PATH=""
BUNDLE_WRAPPER_SHA256=""
RELEASE_WRAPPER_SOURCE=""
RELEASE_WRAPPER_SHA256=""
# Protocolo de publicacao atomica do dispatcher e do estado administrativo.
DISPATCHER_TMP=""
DISPATCHER_RENAMED=false
DISPATCHER_PUBLISHED=false
DISPATCHER_PREV_KIND="other"
DISPATCHER_PREV_BACKUP=""
DISPATCHER_PREV_SHA=""
MODE_RENAMED=false
MODE_PUBLISHED=false
MODE_PREV_KIND="other"
MODE_PREV_BACKUP=""
MODE_PREV_SHA=""
STATE_RENAMED=false
STATE_PUBLISHED=false
STATE_PREV_KIND="other"
STATE_PREV_BACKUP=""
STATE_PREV_SHA=""

# Recovery evidence is administrative state, never imported from a release bundle.
# === SAFE_RECOVERY_BLOCK_BEGIN ===
recovery_version_known() {
  [ "$1" = "v1.0.0-beta.6" ]
}

recovery_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}

recovery_admin_path() {
  local path="$1" owner mode
  while :; do
    [ -e "$path" ] && [ ! -L "$path" ] || return 1
    owner="$(stat -c %u "$path" 2>/dev/null)" || owner="$(stat -f %u "$path")" || return 1
    mode="$(stat -c %a "$path" 2>/dev/null)" || mode="$(stat -f %Lp "$path")" || return 1
    [ "$owner" = 0 ] && [[ "$mode" =~ ^[0-7]{3,4}$ ]] || return 1
    (( (8#$mode & 0022) == 0 )) || return 1
    [ "$path" != / ] || break
    path="$(dirname "$path")"
  done
}

recovery_source_known() {
  local root="$1" expected path actual
  [ "$(cat "$root/VERSION" 2>/dev/null)" = v1.0.0-beta.6 ] || return 1
  # Exact reviewed security sources; a version label cannot attest a local build.
  while read -r expected path; do
    [ -f "$root/$path" ] && [ ! -L "$root/$path" ] || return 1
    actual="$(recovery_sha256 < "$root/$path")" || return 1
    [ "$actual" = "$expected" ] || return 1
  done <<'RECOVERY_SOURCES'
e00dcd07672e3f89242ffecf489926621020e18e943d3becd5ad8766140db674 cmd/server/main.go
28dac28177886cef83978757d943e013924af575ae4ac58052de0a668e29487c cmd/server/server_runtime.go
0fc224bc1fa1b27cc339aaaede07f28cdc204ae5a0cc17c8bd3c8fe8589340b5 cmd/server/legacy_seed_credentials.go
e6d72d81fd2d8e4089d718e1027faf214bd5791dff7f14c0eb9d821a6637ab8a internal/security/crypto.go
RECOVERY_SOURCES
}

recovery_bundle_identity() (
  set -euo pipefail
  local root="$1" suffix="$2" unit="$3" version path file files bad digest
  [ -d "$root" ] && [ ! -L "$root" ] && [ -f "$unit" ] && [ ! -L "$unit" ] || exit 1
  cd "$root"
  version="$(cat "VERSION$suffix")"
  recovery_version_known "$version" || exit 1
  for path in contabase VERSION templates assets; do
    [ -e "$path$suffix" ] && [ ! -L "$path$suffix" ] || exit 1
  done
  [ -f "contabase$suffix" ] && [ -f "VERSION$suffix" ] \
    && [ -d "templates$suffix" ] && [ -d "assets$suffix" ] || exit 1
  set -- "contabase$suffix" "VERSION$suffix" "templates$suffix" "assets$suffix"
  if [ -e "admin$suffix" ] || [ -L "admin$suffix" ]; then
    [ -f "admin$suffix" ] && [ ! -L "admin$suffix" ] || exit 1
    set -- "$@" "admin$suffix"
  fi
  bad="$(find "$@" ! -type f ! -type d -print)" || exit 1
  [ -z "$bad" ] || exit 1
  files="$(find "$@" -type f -print | LC_ALL=C sort)" || exit 1
  # Hash a framed manifest, including logical paths, without executing old code.
  {
    printf 'bundle-v1\000%s\000' "$version"
    recovery_sha256 < "$unit"
    while IFS= read -r file; do
      [ -f "$file" ] && [ ! -L "$file" ] || exit 1
      if [ -n "$suffix" ]; then
        path="${file%%"$suffix"*}${file#*"$suffix"}"
      else
        path="$file"
      fi
      digest="$(recovery_sha256 < "$file")" || exit 1
      printf '%s\000%s\000' "$path" "$digest"
    done <<< "$files"
  } | recovery_sha256
)

recovery_proof_text() {
  printf 'contabase-recovery-v1\nkind=%s\nversion=%s\nidentity=%s\n' "$1" "$2" "$3"
}

recovery_record() {
  local directory="$1" kind="$2" version="$3" identity="$4" tmp
  case "$kind" in bundle|docker) ;; *) return 1 ;; esac
  recovery_version_known "$version" || return 1
  [[ "$identity" =~ ^[0-9a-f]{64}$ ]] || return 1
  recovery_admin_path "$(dirname "$directory")" || return 1
  [ ! -L "$directory" ] || return 1
  install -d -o root -g root -m 0700 "$directory" || return 1
  recovery_admin_path "$directory" || return 1
  tmp="$(mktemp "$directory/.proof.XXXXXX")" || return 1
  if ! recovery_proof_text "$kind" "$version" "$identity" > "$tmp" \
    || ! chmod 0600 "$tmp" || ! mv -f "$tmp" "$directory/$kind-$identity.proof"; then
    rm -f "$tmp"
    return 1
  fi
}

recovery_verify() {
  local directory="$1" kind="$2" version="$3" identity="$4" proof
  case "$kind" in bundle|docker) ;; *) return 1 ;; esac
  recovery_version_known "$version" || return 1
  [[ "$identity" =~ ^[0-9a-f]{64}$ ]] || return 1
  proof="$directory/$kind-$identity.proof"
  [ -f "$proof" ] && recovery_admin_path "$proof" || return 1
  recovery_proof_text "$kind" "$version" "$identity" | cmp -s - "$proof"
}

recovery_blocked() {
  printf '%s\n' \
    'RECOVERY_BLOCKED: nenhum codigo anterior sera ativado automaticamente.' \
    'Recuperacao exige prova de codigo corrigido e nenhuma tentativa de startup da candidata.' \
    'Mantenha o servico parado. Preserve banco, WAL, uploads, configuracao, chaves e backups.' \
    'Repare a falha e use um artefato corrigido, verificado e compativel com o schema atual.' \
    'Nao use downgrade como resgate. Restore de banco exige plano e decisao explicita.' >&2
}
# === SAFE_RECOVERY_BLOCK_END ===

say() {
  printf '%s\n' "$*"
}

fail() {
  say "Erro: $*" >&2
  exit 1
}

if [ "${CONTABASE_BOOTSTRAP_CONTRACT_VERSION+x}" = x ]; then
  [ "${CONTABASE_BOOTSTRAP_CONTRACT_VERSION}" = 1 ] \
    || fail "versao do contrato Bootstrap invalida."
  if [ "$#" -gt 0 ] && [ "$1" = "--contabase-sanitized-reexec" ]; then
    # Marcador transmitido somente pelo reexec saneado do adaptador; o ambiente
    # do chamador nao pode injeta-lo em argv. Nenhuma variavel de ambiente e
    # aceita como prova de sanitizacao.
    BOOTSTRAP_SANITIZED_ENTRY=true
    shift
  fi
  [ "$#" -eq 0 ] || fail "adaptador Bootstrap nao aceita argumentos avulsos."
fi

usage() {
  cat <<'EOF'
Uso:
  sudo env CONTABASE_VERSION=vX.Y.Z ./scripts/install-contabase-release.sh
  ./scripts/install-contabase-release.sh --validate-only

Instala um artifact oficial do GitHub Release após validar SHA-256.
O modo --validate-only baixa, valida e extrai o bundle sem instalar.
EOF
}

cleanup() {
  # Remove apenas temporarios desta execucao; nunca apaga arquivos preexistentes.
  if [ -n "${STAGING_DIR:-}" ] && [ -d "${STAGING_DIR:-}" ]; then
    rm -rf "$STAGING_DIR"
  fi
  if [ -n "$DISPATCHER_TMP" ]; then
    rm -f "$DISPATCHER_TMP" 2>/dev/null || true
    DISPATCHER_TMP=""
  fi
  if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT
# Sinais tambem precisam acionar o EXIT trap para remover temporarios em voo.
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

while [ "$#" -gt 0 ]; do
  case "$1" in
    --validate-only)
      VALIDATE_ONLY=true
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "argumento desconhecido: $1"
      ;;
  esac
done

validate_test_mode() {
  if [ -n "${CONTABASE_TEST_FIXTURE_DIR:-}" ] || [ -n "${CONTABASE_TEST_UNAME_M:-}" ]; then
    [ "${CONTABASE_TEST_MODE:-0}" = "1" ] || fail "variaveis CONTABASE_TEST_* exigem CONTABASE_TEST_MODE=1."
    [ "$VALIDATE_ONLY" = true ] || fail "CONTABASE_TEST_MODE so pode ser usado com --validate-only."
  fi
}

prompt_version() {
  if [ -n "$TAG" ]; then
    return
  fi

  [ -t 0 ] || fail "CONTABASE_VERSION e obrigatorio em modo nao interativo."
  read -r -p "Tag publica para instalar (ex.: vMAJOR.MINOR.PATCH[-beta.N]): " TAG
}

validate_version() {
  [ -n "$TAG" ] || fail "tag vazia."
  case "$TAG" in
    *-internal*) fail "versoes com -internal sao privadas e nao podem ser instaladas por release." ;;
  esac

  if [[ ! "$TAG" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$ ]]; then
    fail "tag publica invalida: $TAG"
  fi
}

validate_install_channel() {
  case "$INSTALL_CHANNEL" in
    stable|beta|pinned) ;;
    *) fail "CONTABASE_CHANNEL invalido: ${INSTALL_CHANNEL}. Use stable, beta ou pinned." ;;
  esac
  case "$INSTALL_CHANNEL:$TAG" in
    beta:v*-beta.*) ;;
    beta:*) fail "CONTABASE_CHANNEL=beta exige tag beta. Tag recebida: $TAG" ;;
    stable:*-*) fail "CONTABASE_CHANNEL=stable exige tag stable. Tag recebida: $TAG" ;;
  esac
}

validate_repo() {
  REPO="${REPO%.git}"
  REPO="${REPO%/}"
  if [[ ! "$REPO" =~ ^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    fail "CONTABASE_REPO deve ser uma URL HTTPS de repositorio GitHub, sem paths extras."
  fi
}

detect_arch() {
  local machine
  machine="${CONTABASE_TEST_UNAME_M:-$(uname -m)}"
  case "$machine" in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) fail "arquitetura nao suportada: $machine. Suportadas: x86_64/amd64 e aarch64/arm64." ;;
  esac
}

validate_numeric_settings() {
  [[ "$PORT" =~ ^[0-9]+$ ]] && [ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] \
    || fail "CONTABASE_PORT invalida: $PORT"
  [[ "$HEALTHCHECK_ATTEMPTS" =~ ^[0-9]+$ ]] && [ "$HEALTHCHECK_ATTEMPTS" -ge 1 ] \
    || fail "CONTABASE_HEALTHCHECK_ATTEMPTS deve ser inteiro positivo."
  [[ "$APP_USER" =~ ^[a-z_][a-z0-9_-]*\$?$ ]] || fail "CONTABASE_USER invalido: $APP_USER"
  [[ "$INSTALL_DIR" = /* ]] || fail "CONTABASE_INSTALL_DIR deve ser absoluto."
  [[ "$DATA_DIR" = /* ]] || fail "CONTABASE_DATA_DIR deve ser absoluto."
  [[ "$CONFIG_DIR" = /* ]] || fail "CONTABASE_CONFIG_DIR deve ser absoluto."
  case "/${INSTALL_DIR#/}/" in *"/../"*|*"/./"*) fail "CONTABASE_INSTALL_DIR contem componente inseguro." ;; esac
  case "/${DATA_DIR#/}/" in *"/../"*|*"/./"*) fail "CONTABASE_DATA_DIR contem componente inseguro." ;; esac
  case "/${CONFIG_DIR#/}/" in *"/../"*|*"/./"*) fail "CONTABASE_CONFIG_DIR contem componente inseguro." ;; esac
  case "${INSTALL_DIR}:${DATA_DIR}:${CONFIG_DIR}" in
    *[[:space:]]*) fail "diretorios configurados nao podem conter espacos." ;;
  esac
  INSTALL_DIR="${INSTALL_DIR%/}"
  DATA_DIR="${DATA_DIR%/}"
  CONFIG_DIR="${CONFIG_DIR%/}"
  ENV_FILE="${CONFIG_DIR}/contabase.env"
  [ -n "$INSTALL_DIR" ] && [ -n "$DATA_DIR" ] && [ -n "$CONFIG_DIR" ] \
    && [ "$INSTALL_DIR" != "/" ] && [ "$DATA_DIR" != "/" ] && [ "$CONFIG_DIR" != "/" ] \
    || fail "diretorios configurados nao podem ser a raiz /."
  [ "$INSTALL_DIR" != "$DATA_DIR" ] && [ "$INSTALL_DIR" != "$CONFIG_DIR" ] && [ "$DATA_DIR" != "$CONFIG_DIR" ] \
    || fail "diretorios de instalacao, dados e configuracao devem ser distintos."
  case "${INSTALL_DIR}/" in "${DATA_DIR}/"*|"${CONFIG_DIR}/"*) fail "CONTABASE_INSTALL_DIR nao pode ficar dentro dos diretorios de dados/configuracao." ;; esac
  case "${DATA_DIR}/" in "${INSTALL_DIR}/"*|"${CONFIG_DIR}/"*) fail "CONTABASE_DATA_DIR nao pode ficar dentro dos diretorios de instalacao/configuracao." ;; esac
  case "${CONFIG_DIR}/" in "${INSTALL_DIR}/"*|"${DATA_DIR}/"*) fail "CONTABASE_CONFIG_DIR nao pode ficar dentro dos diretorios de instalacao/dados." ;; esac
}

sync_port_from_existing_env() {
  local configured_port
  [ "$PORT_WAS_SET" = false ] || return 0
  [ -f "$ENV_FILE" ] || return 0

  configured_port="$(awk -F= '$1 == "PORT" { value = substr($0, index($0, "=") + 1) } END { print value }' "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$configured_port" ] || return 0
  [[ "$configured_port" =~ ^[0-9]+$ ]] && [ "$configured_port" -ge 1 ] && [ "$configured_port" -le 65535 ] \
    || fail "PORT existente em ${ENV_FILE} e invalida: ${configured_port}"
  PORT="$configured_port"
  say "Porta preservada da configuracao existente: $PORT"
}

detect_default_host() {
  local detected=""
  if command -v hostname >/dev/null 2>&1; then
    detected="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
    if [ -z "$detected" ]; then
      detected="$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
    fi
  fi
  detected="${detected:-localhost}"
  printf '%s' "$detected"
}

host_from_base_url() {
  local value="$1"
  value="${value#http://}"
  value="${value#https://}"
  value="${value%%/*}"
  case "$value" in
    \[*\]*)
      value="${value#\[}"
      value="${value%%\]*}"
      ;;
    *)
      value="${value%%:*}"
      ;;
  esac
  printf '%s' "$value"
}

scheme_from_base_url() {
  local value="$1"
  case "$value" in
    http://*) printf '%s' "http" ;;
    https://*) printf '%s' "https" ;;
    *) printf '' ;;
  esac
}

host_is_localhost() {
  case "$1" in
    localhost|127.*|::1) return 0 ;;
    *) return 1 ;;
  esac
}

host_is_private_ipv4() {
  local host="$1"
  local a b c d
  IFS=. read -r a b c d <<EOF
$host
EOF
  for part in "$a" "$b" "$c" "$d"; do
    [[ "$part" =~ ^[0-9]+$ ]] || return 1
    [ "$part" -ge 0 ] && [ "$part" -le 255 ] || return 1
  done
  [ -n "$d" ] || return 1
  [ "$a" -eq 10 ] && return 0
  [ "$a" -eq 192 ] && [ "$b" -eq 168 ] && return 0
  [ "$a" -eq 172 ] && [ "$b" -ge 16 ] && [ "$b" -le 31 ] && return 0
  return 1
}

infer_access_mode() {
  local base_url="$1"
  local proxies="$2"
  local scheme host

  if [ -n "$proxies" ]; then
    printf '%s' "proxy"
    return 0
  fi

  scheme="$(scheme_from_base_url "$base_url")"
  host="$(host_from_base_url "$base_url")"
  case "$scheme" in
    https) printf '%s' "proxy"; return 0 ;;
    http) ;;
    *) printf '%s' "local"; return 0 ;;
  esac

  if host_is_localhost "$host"; then
    printf '%s' "local"
    return 0
  fi
  if host_is_private_ipv4 "$host"; then
    printf '%s' "lan"
    return 0
  fi

  printf '%s' "blocked"
}

validate_access_contract() {
  local inferred
  inferred="$(infer_access_mode "$APP_BASE_URL_VALUE" "$TRUSTED_PROXIES_VALUE")"

  if [ -z "$CONTABASE_ACCESS_MODE_VALUE" ]; then
    CONTABASE_ACCESS_MODE_VALUE="$inferred"
  fi

  case "$CONTABASE_ACCESS_MODE_VALUE" in
    local|lan|proxy) ;;
    blocked)
      fail "APP_BASE_URL em HTTP publico sem reverse proxy nao e permitido. Use HTTPS com proxy/tunnel ou um IP privado RFC1918 em modo LAN."
      ;;
    *) fail "CONTABASE_ACCESS_MODE invalido: ${CONTABASE_ACCESS_MODE_VALUE}. Use local, lan ou proxy." ;;
  esac

  if [ "$inferred" = "lan" ] && [ "$CONTABASE_ACCESS_MODE_VALUE" != "lan" ]; then
    fail "APP_BASE_URL usa IP privado por HTTP sem proxy; configure CONTABASE_ACCESS_MODE=lan."
  fi
  if [ "$inferred" = "proxy" ] && [ "$CONTABASE_ACCESS_MODE_VALUE" != "proxy" ]; then
    fail "APP_BASE_URL/proxy indicam modo proxy; configure CONTABASE_ACCESS_MODE=proxy."
  fi

  if [ "$CONTABASE_ACCESS_MODE_VALUE" = "lan" ]; then
    local host
    host="$(host_from_base_url "$APP_BASE_URL_VALUE")"
    host_is_private_ipv4 "$host" || fail "CONTABASE_ACCESS_MODE=lan exige APP_BASE_URL com IP privado RFC1918."
    [ "$(scheme_from_base_url "$APP_BASE_URL_VALUE")" = "http" ] || fail "CONTABASE_ACCESS_MODE=lan exige APP_BASE_URL http://IP_PRIVADO:PORTA."
  fi

  if [ "$inferred" = "blocked" ]; then
    fail "APP_BASE_URL em HTTP publico sem reverse proxy nao e permitido. Use HTTPS com proxy/tunnel ou um IP privado RFC1918 em modo LAN."
  fi
}

validate_runtime_config_values() {
  local key value
  for key in APP_BASE_URL ALLOWED_HOSTS TRUSTED_PROXIES CONTABASE_ACCESS_MODE CONTABASE_TIMEZONE; do
    case "$key" in
      APP_BASE_URL) value="$APP_BASE_URL_VALUE" ;;
      ALLOWED_HOSTS) value="$ALLOWED_HOSTS_VALUE" ;;
      TRUSTED_PROXIES) value="$TRUSTED_PROXIES_VALUE" ;;
      CONTABASE_ACCESS_MODE) value="$CONTABASE_ACCESS_MODE_VALUE" ;;
      CONTABASE_TIMEZONE) value="$CONTABASE_TIMEZONE_VALUE" ;;
    esac
    if printf '%s' "$value" | grep -q '[[:cntrl:]]'; then
      fail "${key} contem caractere de controle invalido."
    fi
    if [[ "$value" =~ [[:space:]] ]]; then
      fail "${key} nao pode conter espacos."
    fi
  done
  [ -n "$APP_BASE_URL_VALUE" ] || fail "APP_BASE_URL nao pode ficar vazio."
  [ -n "$ALLOWED_HOSTS_VALUE" ] || fail "ALLOWED_HOSTS nao pode ficar vazio."
  validate_access_contract
}

configure_new_installation_runtime() {
  local default_host default_base_url default_allowed_hosts base_host answer access_choice proxy_value

  [ -f "$ENV_FILE" ] && return 0

  default_host="$(detect_default_host)"

  if [ "$ASSUME_YES" != "1" ] && [ -t 0 ]; then
    say ""
    say "-- Configuracao publica da instancia --"

    read -r -p "Porta local do ContaBase [${PORT}]: " answer
    PORT="${answer:-$PORT}"
    validate_numeric_settings

    say "Como voce vai acessar o ContaBase?"
    say "1) Local somente nesta maquina"
    say "2) Rede local/LAN por IP privado"
    say "3) Dominio HTTPS/proxy reverso"
    say "4) Avancado/manual"
    read -r -p "Opcao (1/2/3/4) [3]: " access_choice

    case "${access_choice:-3}" in
      1)
        APP_BASE_URL_VALUE="http://localhost:${PORT}"
        ALLOWED_HOSTS_VALUE="localhost,127.0.0.1,::1"
        TRUSTED_PROXIES_VALUE=""
        CONTABASE_ACCESS_MODE_VALUE="local"
        ;;
      2)
        read -r -p "IP privado LAN do servidor (ex: 192.168.1.50): " answer
        base_host="$(host_from_base_url "$answer")"
        base_host="${base_host:-$answer}"
        host_is_private_ipv4 "$base_host" || fail "Modo LAN exige IP privado RFC1918. Nao use localhost, dominio ou IP publico."
        APP_BASE_URL_VALUE="http://${base_host}:${PORT}"
        ALLOWED_HOSTS_VALUE="$base_host"
        TRUSTED_PROXIES_VALUE=""
        CONTABASE_ACCESS_MODE_VALUE="lan"
        ;;
      3)
        read -r -p "Dominio HTTPS (ex: financeiro.exemplo.com): " answer
        base_host="$(host_from_base_url "$answer")"
        base_host="${base_host:-$answer}"
        [ -n "$base_host" ] || fail "Dominio obrigatorio para modo proxy."
        read -r -p "IP(s) do proxy confiavel, separados por virgula [127.0.0.1,::1]: " proxy_value
        APP_BASE_URL_VALUE="https://${base_host}"
        ALLOWED_HOSTS_VALUE="$base_host"
        TRUSTED_PROXIES_VALUE="${proxy_value:-127.0.0.1,::1}"
        CONTABASE_ACCESS_MODE_VALUE="proxy"
        ;;
      4)
        default_base_url="${APP_BASE_URL_VALUE:-http://${default_host}:${PORT}}"
        read -r -p "URL publica do ContaBase [${default_base_url}]: " answer
        APP_BASE_URL_VALUE="${answer:-$default_base_url}"
        base_host="$(host_from_base_url "$APP_BASE_URL_VALUE")"
        base_host="${base_host:-$default_host}"
        default_allowed_hosts="${ALLOWED_HOSTS_VALUE:-$base_host}"
        read -r -p "Dominios/IPs permitidos, separados por virgula [${default_allowed_hosts}]: " answer
        ALLOWED_HOSTS_VALUE="${answer:-$default_allowed_hosts}"
        read -r -p "TRUSTED_PROXIES (vazio se nao houver proxy): " answer
        TRUSTED_PROXIES_VALUE="${answer:-$TRUSTED_PROXIES_VALUE}"
        ;;
      *)
        fail "Opcao invalida."
        ;;
    esac
  else
    APP_BASE_URL_VALUE="${APP_BASE_URL_VALUE:-http://${default_host}:${PORT}}"
    base_host="$(host_from_base_url "$APP_BASE_URL_VALUE")"
    base_host="${base_host:-$default_host}"
    if host_is_localhost "$base_host"; then
      ALLOWED_HOSTS_VALUE="${ALLOWED_HOSTS_VALUE:-localhost,127.0.0.1,::1}"
    else
      ALLOWED_HOSTS_VALUE="${ALLOWED_HOSTS_VALUE:-$base_host}"
    fi
    TRUSTED_PROXIES_VALUE="${TRUSTED_PROXIES_VALUE:-}"
  fi

  validate_runtime_config_values
}

require_flock() {
  if ! command -v flock >/dev/null 2>&1; then
    fail "flock obrigatorio para atualizacao segura. Instale util-linux antes de continuar."
  fi
}

require_commands() {
  local cmd
  local missing=()
  [ "${VALIDATE_ONLY:-false}" = true ] || require_flock
  for cmd in curl tar install openssl awk grep find mktemp cp wc tr; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  if [ "${VALIDATE_ONLY:-false}" != true ]; then
    command -v runuser >/dev/null 2>&1 || missing+=("runuser")
  fi
  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    missing+=("sha256sum ou shasum")
  fi
  [ "${#missing[@]}" -eq 0 ] || fail "comandos obrigatorios ausentes: ${missing[*]}"
}

check_install_platform() {
  [ "$(id -u)" -eq 0 ] || fail "execute como root (ex.: sudo)."
  [ "$(uname -s)" = "Linux" ] || fail "instalacao suporta apenas Linux; use --validate-only para validar o artifact."
  command -v systemctl >/dev/null 2>&1 || fail "systemctl nao encontrado."
  [ -r /proc/1/comm ] || fail "nao foi possivel confirmar o PID 1."
  [ "$(tr -d '[:space:]' < /proc/1/comm)" = "systemd" ] || fail "PID 1 nao e systemd."

  if [ -f /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    case "${ID:-}" in
      debian|ubuntu) ;;
      *) say "Aviso: distribuicao fora de Debian/Ubuntu: ${PRETTY_NAME:-desconhecida}." ;;
    esac
  fi

  for cmd in systemctl journalctl getent groupadd useradd chown chmod mv cp seq id; do
    command -v "$cmd" >/dev/null 2>&1 || fail "comando obrigatorio ausente: $cmd"
  done
}

build_urls() {
  ARTIFACT_NAME="contabase-linux-${ARCH}.tar.gz"
  ARTIFACT_URL="${REPO}/releases/download/${TAG}/${ARTIFACT_NAME}"
  CHECKSUMS_URL="${REPO}/releases/download/${TAG}/checksums.txt"
}

download_file() {
  local url="$1"
  local destination="$2"
  local fixture_name

  if [ "${CONTABASE_TEST_MODE:-0}" = "1" ] && [ -n "${CONTABASE_TEST_FIXTURE_DIR:-}" ]; then
    fixture_name="${url##*/}"
    [ -f "${CONTABASE_TEST_FIXTURE_DIR}/${fixture_name}" ] \
      || fail "fixture ausente: ${fixture_name}"
    cp "${CONTABASE_TEST_FIXTURE_DIR}/${fixture_name}" "$destination"
    return
  fi

  curl --fail --location --silent --show-error \
    --proto '=https' --tlsv1.2 \
    "$url" -o "$destination"
}

compute_sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  else
    shasum -a 256 "$file" | awk '{print $1}'
  fi
}

validate_checksum() {
  local checksums_file="$1"
  local artifact_file="$2"
  local matches expected actual expected_normalized actual_normalized

  matches="$(awk -v name="$ARTIFACT_NAME" '
    NF >= 2 {
      file = $2
      sub(/^\*/, "", file)
      if (file == name) {
        print $1
      }
    }
  ' "$checksums_file")"

  [ -n "$matches" ] || fail "${ARTIFACT_NAME} nao aparece em checksums.txt."
  [ "$(printf '%s\n' "$matches" | wc -l | tr -d ' ')" = "1" ] \
    || fail "checksums.txt contem entradas duplicadas para ${ARTIFACT_NAME}."

  expected="$matches"
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || fail "SHA-256 invalido em checksums.txt."
  actual="$(compute_sha256 "$artifact_file")"
  expected_normalized="$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')"
  actual_normalized="$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')"
  [ "$actual_normalized" = "$expected_normalized" ] \
    || fail "checksum SHA-256 divergente para ${ARTIFACT_NAME}."
  say "Checksum SHA-256 validado: $actual"
}

validate_tar_entries() {
  local artifact_file="$1"
  local entry component type_listing
  local entries=()

  while IFS= read -r entry; do
    [ -n "$entry" ] || continue
    entries+=("$entry")
    case "$entry" in
      /*|*\\*) fail "path inseguro no tarball: $entry" ;;
      contabase|contabase/*) ;;
      *) fail "path fora do diretorio contabase/: $entry" ;;
    esac

    IFS='/' read -r -a components <<< "$entry"
    for component in "${components[@]}"; do
      case "$component" in
        ".."|".") fail "path traversal detectado no tarball: $entry" ;;
      esac
    done
  done < <(tar -tzf "$artifact_file")

  [ "${#entries[@]}" -gt 0 ] || fail "tarball vazio."

  type_listing="$(tar -tvzf "$artifact_file")"
  if printf '%s\n' "$type_listing" | awk 'substr($1,1,1) == "l" || substr($1,1,1) == "h" { found=1 } END { exit found ? 0 : 1 }'; then
    fail "tarball contem symlink ou hardlink; artifact rejeitado."
  fi
  if printf '%s\n' "$type_listing" | awk 'substr($1,1,1) != "-" && substr($1,1,1) != "d" { found=1 } END { exit found ? 0 : 1 }'; then
    fail "tarball contem tipo especial; somente arquivos e diretorios sao aceitos."
  fi
}

validate_extracted_bundle() {
  local root="$1"
  local required wrapper
  for required in \
    contabase \
    admin \
    VERSION \
    LICENSE \
    contabase.env.example \
    contabase.service.example
  do
    [ -f "$root/$required" ] || fail "arquivo obrigatorio ausente no bundle: $required"
  done
  [ -d "$root/templates" ] || fail "diretorio templates/ ausente no bundle."
  [ -d "$root/assets" ] || fail "diretorio assets/ ausente no bundle."
  [ -z "$(find "$root" -type l -print -quit)" ] || fail "bundle extraido contem symlink."
  [ "$(tr -d '[:space:]' < "$root/VERSION")" = "$TAG" ] \
    || fail "VERSION do artifact nao coincide com a tag $TAG."

  wrapper="$root/lib/contabase-update-wrapper.sh"
  [ -f "$wrapper" ] && [ ! -L "$wrapper" ] && [ -s "$wrapper" ] \
    || fail "wrapper de update ausente, vazio ou inseguro no bundle: lib/contabase-update-wrapper.sh"
  bash -n "$wrapper" 2>/dev/null \
    || fail "wrapper de update invalido no bundle: lib/contabase-update-wrapper.sh"
  BUNDLE_WRAPPER_PATH="$wrapper"
  BUNDLE_WRAPPER_SHA256="$(compute_sha256 "$wrapper")"
  [ -n "$BUNDLE_WRAPPER_SHA256" ] \
    || fail "nao foi possivel calcular SHA-256 do wrapper de update."
}

download_and_validate_bundle() {
  local artifact_file checksums_file
  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-release-install.XXXXXX")"
  EXTRACT_DIR="${TMP_DIR}/extract"
  artifact_file="${TMP_DIR}/${ARTIFACT_NAME}"
  checksums_file="${TMP_DIR}/checksums.txt"
  mkdir -p "$EXTRACT_DIR"

  if [ "$BOOTSTRAP_MODE" = true ]; then
    local prepared_root
    prepared_root="$(cd "${CONTABASE_BOOTSTRAP_CHECKOUT:-}" 2>/dev/null && pwd -P)" \
      || fail "checkout preparado ausente."
    [ "$(cd "$(dirname "${CONTABASE_CHECKSUMS_PATH}")" 2>/dev/null && pwd -P)" = "$prepared_root" ] \
      || fail "checksums fora do checkout preparado."
    [ "$(cd "$(dirname "${CONTABASE_ARTIFACT_PATH}")" 2>/dev/null && pwd -P)" = "$prepared_root" ] \
      || fail "artifact fora do checkout preparado."
    [ "$(basename "$CONTABASE_CHECKSUMS_PATH")" = "checksums.txt" ] \
      || fail "nome de checksums divergente."
    [ "$(basename "$CONTABASE_ARTIFACT_PATH")" = "$ARTIFACT_NAME" ] \
      || fail "nome do artifact diverge da arquitetura."
    bootstrap_verify_file_digest "$CONTABASE_CHECKSUMS_PATH" "$CONTABASE_CHECKSUMS_SHA256" "checksums" \
      || fail "checksums nao corresponde ao snapshot homologado."
    bootstrap_verify_file_digest "$CONTABASE_ARTIFACT_PATH" "$CONTABASE_ARTIFACT_SHA256" "artifact" \
      || fail "artifact nao corresponde ao snapshot homologado."
    cp "$CONTABASE_CHECKSUMS_PATH" "$checksums_file" \
      || fail "nao foi possivel preparar checksums."
    cp "$CONTABASE_ARTIFACT_PATH" "$artifact_file" \
      || fail "nao foi possivel preparar o artifact."
    say "Payload do Bootstrap verificado por SHA-256."
  else
    say "Baixando ${ARTIFACT_URL}"
    download_file "$ARTIFACT_URL" "$artifact_file"
    say "Baixando ${CHECKSUMS_URL}"
    download_file "$CHECKSUMS_URL" "$checksums_file"
  fi
  validate_checksum "$checksums_file" "$artifact_file"
  validate_tar_entries "$artifact_file"
  tar --no-same-owner --no-same-permissions -xzf "$artifact_file" -C "$EXTRACT_DIR"
  validate_extracted_bundle "${EXTRACT_DIR}/contabase"
  RELEASE_BUNDLE_VERIFIED=true
  say "Artifact validado e extraido com seguranca."
}

confirm_install() {
  say ""
  say "Resumo:"
  say "  Repositorio:  $REPO"
  say "  Versao:       $TAG"
  say "  Arquitetura:  $ARCH"
  say "  Instalacao:   $INSTALL_DIR"
  say "  Dados:        $DATA_DIR"
  say "  Configuracao: $CONFIG_DIR"
  say "  Porta:        $PORT"
  if [ -f "$ENV_FILE" ]; then
    say "  .env:         existente (valores preservados)"
  else
    say "  .env:         novo (criado com secrets fortes)"
    say "  URL publica:  $APP_BASE_URL_VALUE"
    say "  Hosts:        $ALLOWED_HOSTS_VALUE"
    say "  Modo acesso:  $CONTABASE_ACCESS_MODE_VALUE"
    if [ -n "$TRUSTED_PROXIES_VALUE" ]; then
      say "  Proxies:      $TRUSTED_PROXIES_VALUE"
    else
      say "  Proxies:      vazio (sem reverse proxy confiavel)"
    fi
  fi
  if [ -e "$INSTALL_DIR" ]; then
    say "  Bundle:       existente (backup sera criado)"
  fi
  say ""

  [ "$ASSUME_YES" = "1" ] && return
  [ -t 0 ] || fail "confirmacao interativa indisponivel; use CONTABASE_ASSUME_YES=1."
  read -r -p "Continuar com a instalacao? (s/N) " answer
  case "$answer" in
    s|S|sim|SIM|Sim) ;;
    *) fail "instalacao cancelada." ;;
  esac
}

ensure_user_and_directories() {
  [ ! -L "$INSTALL_DIR" ] || fail "CONTABASE_INSTALL_DIR nao pode ser symlink."
  [ ! -L "$DATA_DIR" ] || fail "CONTABASE_DATA_DIR nao pode ser symlink."
  [ ! -L "$CONFIG_DIR" ] || fail "CONTABASE_CONFIG_DIR nao pode ser symlink."
  [ ! -L "$ENV_FILE" ] || fail "contabase.env nao pode ser symlink."
  [ ! -e "$ENV_FILE" ] || [ -f "$ENV_FILE" ] || fail "contabase.env existente deve ser arquivo regular."
  [ ! -L "$SERVICE_FILE" ] || fail "a unit systemd nao pode ser symlink."

  if ! getent group "$APP_USER" >/dev/null 2>&1; then
    groupadd --system "$APP_USER"
  fi
  if ! id -u "$APP_USER" >/dev/null 2>&1; then
    useradd --system --gid "$APP_USER" --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$APP_USER"
  fi

  if [ "$BOOTSTRAP_MODE" = true ]; then
    bootstrap_create_dir_exclusive "$INSTALL_DIR" root root 0755 \
      || fail "nao foi possivel criar o diretorio de instalacao sem sobrescrever."
    bootstrap_create_dir_exclusive "$CONFIG_DIR" root root 0755 \
      || fail "nao foi possivel criar o diretorio de configuracao sem sobrescrever."
    bootstrap_create_dir_exclusive "$DATA_DIR" "$APP_USER" "$APP_USER" 0750 \
      || fail "nao foi possivel criar o diretorio de dados sem sobrescrever."
  else
    install -d -o root -g root -m 0755 "$(dirname "$INSTALL_DIR")"
    install -d -o root -g root -m 0755 "$CONFIG_DIR"
    install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR"
  fi
  install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR/uploads"
  install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR/uploads/profile"
  install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR/uploads/workspaces"
  install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR/backups"
}

# ==============================================================================
# Secure environment file helpers
# ==============================================================================

# Load a single key's value from an env file. Silent; value via stdout.
# Returns empty string if key not found. Never prints to stderr/stdout except the value.
env_load_value() {
  local key="$1"
  local file="${2:-$ENV_FILE}"
  [ -f "$file" ] || { printf ''; return 0; }
  awk -F= -v k="$key" '$1 == k { val = substr($0, index($0, "=") + 1); print val; exit }' "$file"
}

# Check if a key exists in env file and has a non-empty, non-placeholder value.
# Returns 0 (true) if key is properly set, 1 (false) otherwise.
env_key_is_set() {
  local key="$1"
  local file="${2:-$ENV_FILE}"
  local val
  [ -f "$file" ] || return 1
  val="$(env_load_value "$key" "$file")"
  [ -n "$val" ] || return 1
  env_is_placeholder "$val" && return 1
  return 0
}

# Detect known placeholder patterns that indicate a value was not configured.
env_is_placeholder() {
  local val="$1"
  [ -z "$val" ] && return 0
  case "$val" in
    __PREENCHA_*|__GERAR_COM_*|CHANGE_ME*|change_me*|REPLACE_ME*|replace_me*|YOUR_*|your_*|PLACEHOLDER*|placeholder*) return 0 ;;
    *) return 1 ;;
  esac
}

# Create a timestamped backup of the env file with secure permissions.
# Fails the script if backup cannot be created and env already exists.
env_backup() {
  local backup_path
  [ -f "$ENV_FILE" ] || return 0
  backup_path="${ENV_FILE}.$(date +%Y%m%d-%H%M%S).bak"
  if cp -a "$ENV_FILE" "$backup_path"; then
    chmod 0600 "$backup_path" 2>/dev/null || true
    say "Backup do .env criado em: ${backup_path}"
    return 0
  fi
  fail "nao foi possivel fazer backup de ${ENV_FILE}."
}

# Ensure env file has secure permissions (0600, root:root).
env_ensure_permissions() {
  [ -f "$ENV_FILE" ] || return 0
  chown root:root "$ENV_FILE" 2>/dev/null || true
  chmod 0600 "$ENV_FILE" || fail "nao foi possivel proteger ${ENV_FILE}."
}

# Generate a cryptographically strong base64 secret.
# Args: number of random bytes (default 32).
env_generate_secret() {
  local bytes="${1:-32}"
  openssl rand -base64 "$bytes" 2>/dev/null | tr -d '\n' || fail "falha ao gerar secret com openssl (verifique se openssl esta instalado)."
}

# Generate a cryptographically strong hex secret.
env_generate_hex_secret() {
  local bytes="${1:-16}"
  openssl rand -hex "$bytes" 2>/dev/null | tr -d '\n' || fail "falha ao gerar secret hex com openssl (verifique se openssl esta instalado)."
}

# Safely set a key to a value, replacing existing line if present (no duplicates).
# NEVER appends duplicate; replaces or appends exactly once.
env_set_key() {
  local key="$1"
  local value="$2"
  local file="${3:-$ENV_FILE}"
  local tmpfile

  tmpfile="$(mktemp)"
  if grep -q "^${key}=" "$file" 2>/dev/null; then
    # Replace existing line in-place
    awk -F= -v k="$key" -v v="$value" '
      $1 == k { print k "=" v; next }
      { print }
    ' "$file" > "$tmpfile"
  else
    cp "$file" "$tmpfile"
    printf '%s=%s\n' "$key" "$value" >> "$tmpfile"
  fi
  mv "$tmpfile" "$file"
}

# Add a non-sensitive env variable only if it does not already exist with a real value.
# If key exists with empty/placeholder value, replace it after confirmation (interactive)
# or fail (headless for sensitive keys). Preserves real user values.
env_set_if_missing() {
  local key="$1"
  local value="$2"
  local file="${3:-$ENV_FILE}"

  # Key already has a real (non-placeholder) value — preserve it
  if env_key_is_set "$key" "$file"; then
    return 0
  fi

  # Key does not exist at all — append
  if ! grep -q "^${key}=" "$file" 2>/dev/null; then
    printf '%s=%s\n' "$key" "$value" >> "$file"
    say "  [ADD] ${key}=<definido>"
    return 0
  fi

  # Key exists but is empty or placeholder — replace (non-sensitive keys only)
  # Sensitive keys are handled separately by env_manage_existing / env_require_secrets
  env_set_key "$key" "$value" "$file"
  say "  [FIX] ${key} substituido (estava vazio ou com placeholder)."
}

# ==============================================================================
# Environment file creation and management
# ==============================================================================

# Create a fresh env file with all required variables and strong secrets.
# Only called for new installations (env file does not exist).
env_create_initial() {
  local xtrace_was_enabled=false

  case "$-" in
    *x*) xtrace_was_enabled=true; set +x ;;
  esac

  if ! (
    umask 077
    set -o noclobber
    {
      printf 'APP_ENV=production\n'
      printf 'APP_DEBUG=false\n'
      printf 'PORT=%s\n' "$PORT"
      printf 'DATABASE_URL=file:%s/contabase.db\n' "$DATA_DIR"
      printf 'DATA_DIR=%s\n' "$DATA_DIR"
      printf 'DB_FILE=%s/contabase.db\n' "$DATA_DIR"
      printf 'UPLOADS_DIR=%s/uploads\n' "$DATA_DIR"
      printf 'CONTABASE_TIMEZONE=%s\n' "$CONTABASE_TIMEZONE_VALUE"
      printf 'APP_BASE_URL=%s\n' "$APP_BASE_URL_VALUE"
      printf 'ALLOWED_HOSTS=%s\n' "$ALLOWED_HOSTS_VALUE"
      printf 'TRUSTED_PROXIES=%s\n' "$TRUSTED_PROXIES_VALUE"
      printf 'CONTABASE_ACCESS_MODE=%s\n' "$CONTABASE_ACCESS_MODE_VALUE"
      printf 'CONTABASE_FEATURE_INVESTMENTS=false\n'
      printf 'VERSION=%s\n' "$TAG"
      printf 'CONTABASE_CHANNEL=%s\n' "$INSTALL_CHANNEL"
      printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$TAG"
    } > "$ENV_FILE"
  ); then
    [ "$xtrace_was_enabled" = false ] || set -x
    fail "nao foi possivel criar ${ENV_FILE} sem sobrescrever arquivo existente."
  fi
  [ "$xtrace_was_enabled" = false ] || set -x

  # Generate and append secrets (suppress xtrace for this section)
  local setup_token auth_key master_key
  case "$-" in
    *x*) xtrace_was_enabled=true; set +x ;;
  esac
  if [ -n "${CONTABASE_SETUP_TOKEN:-}" ] && ! env_is_placeholder "$CONTABASE_SETUP_TOKEN"; then
    setup_token="$CONTABASE_SETUP_TOKEN"
    SETUP_TOKEN_SUPPLIED_BY_ENV=true
  else
    setup_token="$(env_generate_secret 48)"
    SETUP_TOKEN_GENERATED_NOW=true
  fi
  auth_key="$(env_generate_secret 32)"
  master_key="$(env_generate_hex_secret 16)"

  {
    printf 'CONTABASE_SETUP_TOKEN=%s\n' "$setup_token"
    printf 'AUTH_ENCRYPTION_KEY=%s\n' "$auth_key"
    printf 'SECURITY_MASTER_KEY=%s\n' "$master_key"
  } >> "$ENV_FILE"
  [ "$xtrace_was_enabled" = false ] || set -x

  env_ensure_permissions
  say "Arquivo de configuracao criado em: ${ENV_FILE}"
}

# Manage an existing env file: backup, validate secrets, merge missing vars.
# Fails if mandatory secrets are missing and cannot be resolved.
env_manage_existing() {
  say "Configuracao existente detectada em ${ENV_FILE}."
  say "Preservando valores existentes..."

  env_backup
  env_ensure_permissions

  # Check mandatory sensitive keys
  local missing_secrets=()
  env_key_is_set "AUTH_ENCRYPTION_KEY" "$ENV_FILE" || missing_secrets+=("AUTH_ENCRYPTION_KEY")
  env_key_is_set "SECURITY_MASTER_KEY" "$ENV_FILE" || missing_secrets+=("SECURITY_MASTER_KEY")
  env_key_is_set "CONTABASE_SETUP_TOKEN" "$ENV_FILE" || missing_secrets+=("CONTABASE_SETUP_TOKEN")

  if [ "${#missing_secrets[@]}" -gt 0 ]; then
    say ""
    say "====================================================================="
    say "ALERTA: ${#missing_secrets[@]} secret(s) obrigatorio(s) ausente(s) ou nao preenchido(s):"
    for s in "${missing_secrets[@]}"; do
      say "  - ${s}"
    done
    say "Arquivo: ${ENV_FILE}"
    say "====================================================================="

    if [ "$ASSUME_YES" = "1" ]; then
      fail "Modo headless (CONTABASE_ASSUME_YES=1): corrija os secrets em ${ENV_FILE} e execute novamente."
    fi

    if [ ! -t 0 ]; then
      fail "Sem TTY interativo e sem CONTABASE_ASSUME_YES=1: corrija os secrets em ${ENV_FILE} e execute novamente."
    fi

    say ""
    read -r -p "Deseja gerar os secrets ausentes automaticamente? (s/N) " answer
    case "$answer" in
      s|S|sim|SIM) ;;
      *) fail "Instalacao cancelada. Defina os secrets manualmente em ${ENV_FILE} e execute novamente." ;;
    esac

    # Generate missing secrets (replace placeholder lines, never append duplicate)
    local xtrace_was_enabled=false
    case "$-" in
      *x*) xtrace_was_enabled=true; set +x ;;
    esac
    for s in "${missing_secrets[@]}"; do
      case "$s" in
        AUTH_ENCRYPTION_KEY)
          env_set_key "AUTH_ENCRYPTION_KEY" "$(env_generate_secret 32)" "$ENV_FILE"
          say "  [NEW] AUTH_ENCRYPTION_KEY gerado."
          ;;
        SECURITY_MASTER_KEY)
          env_set_key "SECURITY_MASTER_KEY" "$(env_generate_hex_secret 16)" "$ENV_FILE"
          say "  [NEW] SECURITY_MASTER_KEY gerado."
          ;;
        CONTABASE_SETUP_TOKEN)
          env_set_key "CONTABASE_SETUP_TOKEN" "$(env_generate_secret 48)" "$ENV_FILE"
          SETUP_TOKEN_GENERATED_NOW=true
          say "  [NEW] CONTABASE_SETUP_TOKEN gerado."
          ;;
      esac
    done
    [ "$xtrace_was_enabled" = false ] || set -x
  fi

  # Merge missing non-sensitive config vars (preserve existing user values)
  env_set_if_missing "APP_ENV" "production" "$ENV_FILE"
  env_set_if_missing "APP_DEBUG" "false" "$ENV_FILE"
  env_set_if_missing "CONTABASE_TIMEZONE" "$CONTABASE_TIMEZONE_VALUE" "$ENV_FILE"
  env_set_if_missing "DATA_DIR" "$DATA_DIR" "$ENV_FILE"
  env_set_if_missing "UPLOADS_DIR" "${DATA_DIR}/uploads" "$ENV_FILE"
  if ! env_key_is_set "CONTABASE_ACCESS_MODE" "$ENV_FILE"; then
    local existing_base_url existing_trusted_proxies
    existing_base_url="$(env_load_value "APP_BASE_URL" "$ENV_FILE" | tr -d "[:space:]'\"")"
    existing_trusted_proxies="$(env_load_value "TRUSTED_PROXIES" "$ENV_FILE" | tr -d "[:space:]'\"")"
    APP_BASE_URL_VALUE="$existing_base_url"
    TRUSTED_PROXIES_VALUE="$existing_trusted_proxies"
    CONTABASE_ACCESS_MODE_VALUE="$(infer_access_mode "$existing_base_url" "$existing_trusted_proxies")"
    validate_access_contract
    env_set_if_missing "CONTABASE_ACCESS_MODE" "$CONTABASE_ACCESS_MODE_VALUE" "$ENV_FILE"
  fi

  # PORT, DATABASE_URL, DB_FILE, APP_BASE_URL, ALLOWED_HOSTS, TRUSTED_PROXIES
  # are intentionally NOT merged — they must be preserved from user configuration.

  # VERSION tracks the installed release tag; always update to current
  env_set_key "VERSION" "$TAG" "$ENV_FILE"
  env_set_key "CONTABASE_CHANNEL" "$INSTALL_CHANNEL" "$ENV_FILE"
  env_set_key "CONTABASE_INSTALLED_VERSION" "$TAG" "$ENV_FILE"

  env_ensure_permissions
  say "Configuracao preservada. Secrets validados."
  if env_key_is_set "CONTABASE_SETUP_TOKEN" "$ENV_FILE" && [ "$SETUP_TOKEN_GENERATED_NOW" != true ]; then
    say "CONTABASE_SETUP_TOKEN existente preservado; valor nao exibido."
  fi
}

show_setup_token_if_needed() {
  [ "$VALIDATE_ONLY" != true ] || return 0

  if [ "$SETUP_TOKEN_SUPPLIED_BY_ENV" = true ]; then
    say "CONTABASE_SETUP_TOKEN fornecido por ambiente e gravado no arquivo de configuracao."
  fi

  say ""
  say "O token de setup permanece no arquivo protegido: ${ENV_FILE} (root:root, 0600)."
  say "Para recupera-lo, abra esse arquivo como root em um terminal local autorizado."
  say "Use o valor de CONTABASE_SETUP_TOKEN em /setup para criar o primeiro workspace e usuario admin."
  say "Nao copie o segredo para logs ou mensagens de suporte."
}

# Assert that persistent mandatory secrets exist in an existing env file (for update/headless).
# Exits with error if any secret is missing or is a placeholder.
env_require_secrets() {
  local file="${1:-$ENV_FILE}"
  local missing=()
  local key

  for key in AUTH_ENCRYPTION_KEY SECURITY_MASTER_KEY; do
    env_key_is_set "$key" "$file" || missing+=("$key")
  done

  [ "${#missing[@]}" -eq 0 ] && return 0

  say ""
  say "====================================================================="
  say "ERRO: Secret(s) persistente(s) obrigatorio(s) ausente(s) ou nao preenchido(s) em ${file}:"
  for key in "${missing[@]}"; do
    say "  - ${key}"
  done
  say ""
  say "Para resolver:"
  say "  - Edite ${file} manualmente."
  say "  - Gere valores fortes para AUTH_ENCRYPTION_KEY e SECURITY_MASTER_KEY."
  say "  - CONTABASE_SETUP_TOKEN e gerado apenas no setup inicial e nao e obrigatorio para update."
  say "  - Execute novamente."
  say "====================================================================="
  return 1
}

# Main env file management: routes to create or manage based on existence.
manage_env_file() {
  mkdir -p "$CONFIG_DIR"

  if [ ! -f "$ENV_FILE" ]; then
    say ""
    say "-- Nova instalacao: criando arquivo de configuracao --"
    env_create_initial
    show_setup_token_if_needed

    return 0
  fi

  # Env file exists — manage carefully
  env_manage_existing
  show_setup_token_if_needed
}

render_service_file() {
  local service_staging="${TMP_DIR}/contabase.service.new"
  local network_lines=""
  if [ "$BOOTSTRAP_MODE" = true ]; then
    [ ! -e "$SERVICE_FILE" ] && [ ! -L "$SERVICE_FILE" ] \
      || { fail "unit systemd apareceu durante a instalacao; operacao bloqueada."; return 1; }
    network_lines="$(bootstrap_unit_network_lines)" \
      || { fail "perfil de rede sem restricao systemd suportada."; return 1; }
  fi
  if [ -f "$SERVICE_FILE" ]; then
    SERVICE_BACKUP="${TMP_DIR}/contabase.service.previous"
    cp "$SERVICE_FILE" "$SERVICE_BACKUP" || return 1
  fi

  {
    echo "[Unit]"
    echo "Description=ContaBase - Base Financeira Privada"
    echo "After=network.target"
    echo ""
    echo "[Service]"
    echo "Type=simple"
    echo "User=${APP_USER}"
    echo "Group=${APP_USER}"
    echo "WorkingDirectory=${INSTALL_DIR}"
    echo "EnvironmentFile=${ENV_FILE}"
    echo "ExecStart=${INSTALL_DIR}/contabase"
    echo "Restart=on-failure"
    echo "RestartSec=5"
    echo "NoNewPrivileges=true"
    echo "PrivateTmp=true"
    echo "ProtectHome=true"
    echo "ProtectSystem=strict"
    echo "ReadWritePaths=${DATA_DIR}"
    if [ -n "$network_lines" ]; then
      printf '%s\n' "$network_lines"
    fi
    echo ""
    echo "[Install]"
    echo "WantedBy=multi-user.target"
  } > "$service_staging" || return 1
  install -o root -g root -m 0644 "$service_staging" "$SERVICE_FILE" || return 1
}

# A extracao usa --no-same-permissions. Quando o instalador roda com umask
# restritivo, diretorios chegam como 0700 e arquivos como 0600. Normalize
# somente a arvore do bundle e comprove o acesso do usuario do servico antes
# de parar ou substituir qualquer runtime.
normalize_bundle_tree() {
  local root="$1"
  [ -d "$root" ] && [ ! -L "$root" ] || return 1
  [ -z "$(find "$root" -xdev -type l -print -quit)" ] || return 1
  find "$root" -xdev -type d -exec chmod 0755 {} + || return 1
  find "$root" -xdev -type f -exec chmod 0644 {} + || return 1
  chmod 0755 "$root" "$root/contabase" "$root/admin" \
    "$root/lib/contabase-update-wrapper.sh" || return 1
  return 0
}

service_user_can_access_bundle() {
  local root="$1" user="$2" current sample
  [ -d "$root" ] && [ ! -L "$root" ] || return 1
  current="$root"
  while [ "$current" != "/" ]; do
    runuser -u "$user" -- test -x "$current" || return 1
    current="$(dirname "$current")"
  done
  runuser -u "$user" -- test -x "$root" || return 1
  runuser -u "$user" -- test -x "$root/assets" || return 1
  sample="$(find "$root/assets" -xdev -type f -print -quit)"
  [ -n "$sample" ] || return 1
  runuser -u "$user" -- test -r "$sample" || return 1
  runuser -u "$user" -- test -x "$root/templates" || return 1
  sample="$(find "$root/templates" -xdev -type f -print -quit)"
  [ -n "$sample" ] || return 1
  runuser -u "$user" -- test -r "$sample" || return 1
  runuser -u "$user" -- test -r "$root/VERSION" || return 1
  runuser -u "$user" -- test -x "$root/contabase" || return 1
  runuser -u "$user" -- sh -c 'cd "$1"' sh "$root" || return 1
  return 0
}

stage_bundle() {
  local bundle_root timestamp
  bundle_root="${EXTRACT_DIR}/contabase"
  timestamp="$(date +%Y%m%d-%H%M%S)"
  STAGING_DIR="${INSTALL_DIR}.staging.${timestamp}.$$"

  install -d -o root -g root -m 0755 "$STAGING_DIR" \
    || fail "nao foi possivel criar o diretorio de staging."
  cp -a "${bundle_root}/." "$STAGING_DIR/" \
    || fail "nao foi possivel copiar o bundle para staging."
  chown -R root:root "$STAGING_DIR" \
    || fail "nao foi possivel ajustar ownership do staging."
  normalize_bundle_tree "$STAGING_DIR" \
    || fail "nao foi possivel normalizar permissoes do bundle em staging."
  service_user_can_access_bundle "$STAGING_DIR" "$APP_USER" \
    || fail "bundle em staging nao e acessivel pelo usuario ${APP_USER}; instalacao abortada antes de parar ou substituir o runtime."
  say "Bundle em staging validado para o usuario ${APP_USER}."
}

activate_staged_bundle() {
  local timestamp
  timestamp="$(date +%Y%m%d-%H%M%S)"
  PREVIOUS_DIR="${INSTALL_DIR}.previous.${timestamp}.$$"
  [ -n "$STAGING_DIR" ] && [ -d "$STAGING_DIR" ] && [ ! -L "$STAGING_DIR" ] \
    || fail "staging do bundle ausente ou inseguro."

  if [ -e "$INSTALL_DIR" ]; then
    if [ "$BOOTSTRAP_MODE" = true ]; then
      # O adaptador criou o diretorio vazio de forma exclusiva; nao ha bundle anterior.
      rmdir "$INSTALL_DIR" || fail "diretorio de instalacao inicial nao esta vazio."
      PREVIOUS_DIR=""
    else
      mv "$INSTALL_DIR" "$PREVIOUS_DIR" || fail "nao foi possivel preservar o bundle anterior."
    fi
  else
    PREVIOUS_DIR=""
  fi
  if ! mv "$STAGING_DIR" "$INSTALL_DIR"; then
    rollback_bundle || true
    fail "nao foi possivel ativar o bundle em staging."
  fi
  STAGING_DIR=""
  NEW_BUNDLE_ACTIVE=true
}

wait_for_healthcheck() {
  local attempt health
  for attempt in $(seq 1 "$HEALTHCHECK_ATTEMPTS"); do
    health="$(curl -fsS --max-time 3 "http://127.0.0.1:${PORT}/health" 2>/dev/null || true)"
    [ "$health" = '{"status":"healthy"}' ] && return 0
    say "Aguardando healthcheck... tentativa ${attempt}/${HEALTHCHECK_ATTEMPTS}"
    sleep 2
  done
  return 1
}

rollback_bundle() {
  [ "${RECOVERY_COMPLETED:-false}" != true ] || return 0
  local identity unit="${SERVICE_BACKUP:-$SERVICE_FILE}" previous_version
  say "Tentando rollback do bundle anterior; dados e configuracao nao serao restaurados."
  systemctl stop contabase || { recovery_blocked; return 1; }
  if [ "$NEW_RUNTIME_STARTED" = true ] || [ -z "$PREVIOUS_DIR" ] || [ ! -d "$PREVIOUS_DIR" ]; then
    recovery_blocked
    return 1
  fi
  identity="$(recovery_bundle_identity "$PREVIOUS_DIR" "" "$unit")" || { recovery_blocked; return 1; }
  previous_version="$(cat "$PREVIOUS_DIR/VERSION")" || return 1
  recovery_verify "$CONFIG_DIR/recovery" bundle "$previous_version" "$identity" || { recovery_blocked; return 1; }

  if [ "$NEW_BUNDLE_ACTIVE" = true ] && [ -e "$INSTALL_DIR" ]; then
    mv "$INSTALL_DIR" "${INSTALL_DIR}.failed.$(date +%Y%m%d-%H%M%S).$$" || return 1
  fi
  [ ! -e "$INSTALL_DIR" ] || return 1
  mv "$PREVIOUS_DIR" "$INSTALL_DIR" || return 1
  NEW_BUNDLE_ACTIVE=false
  PREVIOUS_DIR=""
  if [ -n "$SERVICE_BACKUP" ] && [ -f "$SERVICE_BACKUP" ]; then
    cp "$SERVICE_BACKUP" "$SERVICE_FILE" || return 1
  fi
  systemctl daemon-reload || return 1
  NEW_RUNTIME_STARTED=true
  if systemctl restart contabase && wait_for_healthcheck; then RECOVERY_COMPLETED=true; return 0; fi
  systemctl stop contabase || true
  recovery_blocked
  return 1
}

record_release_recovery() {
  local identity expected
  if [ "$RELEASE_BUNDLE_VERIFIED" = true ] \
    && [ "$REPO" = https://github.com/contabase-app/contabase ] \
    && recovery_version_known "$TAG" \
    && expected="$(recovery_bundle_identity "$EXTRACT_DIR/contabase" "" "$SERVICE_FILE")" \
    && identity="$(recovery_bundle_identity "$INSTALL_DIR" "" "$SERVICE_FILE")" \
    && [ "$identity" = "$expected" ] \
    && recovery_record "$CONFIG_DIR/recovery" bundle "$TAG" "$identity"; then
    say "Prova administrativa de recuperacao vinculada ao bundle corrigido."
  else
    say "Aviso: prova de recuperacao nao emitida; retorno automatico a este bundle ficara bloqueado."
  fi
}

path_uid() {
  stat -c %u "$1" 2>/dev/null || stat -f %u "$1"
}

path_mode() {
  stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"
}

# Diretorio de destino seguro para publicacao administrativa: real, e quando a
# execucao e root, de propriedade root e sem escrita por grupo/outros.
admin_dir_is_secure() {
  local dir="$1" owner mode
  [ -d "$dir" ] && [ ! -L "$dir" ] || return 1
  if [ "$(id -u)" -eq 0 ]; then
    owner="$(path_uid "$dir")" || return 1
    [ "$owner" = "0" ] || return 1
  fi
  mode="$(path_mode "$dir")" || return 1
  case "$mode" in
    ""|*[!0-7]*) return 1 ;;
  esac
  if (( (8#$mode & 0022) == 0 )); then
    return 0
  fi
  return 1
}

# Protege o temporario (modo/ownership) e publica por rename atomico no mesmo
# diretorio. Nunca escreve in-place no destino; falha anterior ao rename deixa
# o destino preexistente intocado.
publish_atomic_tmp() {
  local tmp="$1"
  local dest="$2"
  local mode="$3"

  [ -f "$tmp" ] && [ ! -L "$tmp" ] \
    || { say "Erro: temporario ausente ou inseguro para ${dest}." >&2; rm -f "$tmp" 2>/dev/null || true; return 1; }
  # rename sobre diretorio nao substitui o destino: o arquivo seria movido para
  # dentro dele e a publicacao retornaria sucesso sem persistir o caminho.
  [ ! -d "$dest" ] \
    || { say "Erro: destino de publicacao e diretorio: ${dest}." >&2; rm -f "$tmp" 2>/dev/null || true; return 1; }
  chmod "$mode" "$tmp" \
    || { say "Erro: falha ao definir modo ${mode} do temporario de ${dest}." >&2; rm -f "$tmp"; return 1; }
  if [ "$(id -u)" -eq 0 ]; then
    chown root:root "$tmp" 2>/dev/null \
      || { say "Erro: falha ao definir ownership root do temporario de ${dest}." >&2; rm -f "$tmp"; return 1; }
  fi
  mv -f "$tmp" "$dest" \
    || { say "Erro: falha no rename atomico de ${dest}." >&2; rm -f "$tmp"; return 1; }
  return 0
}

# Copia fonte autenticada para temporario exclusivo no diretorio de destino e
# publica por rename atomico. SHA-256 conferido antes da copia, no temporario e
# apos o rename; o destino nunca recebe escrita parcial. DISPATCHER_RENAMED
# indica se o rename ocorreu (compensacao decide o que desfazer).
atomic_publish_authenticated_file() {
  local source="$1"
  local dest="$2"
  local expected_sha="$3"
  local mode="$4"
  local dest_dir tmp

  DISPATCHER_RENAMED=false
  dest_dir="$(dirname "$dest")"
  admin_dir_is_secure "$dest_dir" \
    || { say "Erro: diretorio inseguro para publicacao atomica: ${dest_dir}" >&2; return 1; }
  [ ! -d "$dest" ] \
    || { say "Erro: destino de publicacao e diretorio: ${dest}" >&2; return 1; }
  [ -f "$source" ] && [ ! -L "$source" ] \
    || { say "Erro: fonte ausente ou insegura para publicacao: ${source}" >&2; return 1; }
  [ "$(compute_sha256 "$source")" = "$expected_sha" ] \
    || { say "Erro: conteudo divergente do autenticado antes da publicacao atomica." >&2; return 1; }
  tmp="$(mktemp "${dest_dir%/}/.contabase-publish.tmp.XXXXXX")" \
    || { say "Erro: falha ao criar temporario de publicacao em ${dest_dir}." >&2; return 1; }
  DISPATCHER_TMP="$tmp"
  if ! cp -- "$source" "$tmp" || [ "$(compute_sha256 "$tmp")" != "$expected_sha" ]; then
    rm -f "$tmp" 2>/dev/null || true
    DISPATCHER_TMP=""
    say "Erro: copia parcial ou SHA-256 divergente no temporario de ${dest}." >&2
    return 1
  fi
  if ! publish_atomic_tmp "$tmp" "$dest" "$mode"; then
    rm -f "$tmp" 2>/dev/null || true
    DISPATCHER_TMP=""
    say "Erro: falha ao preparar a publicacao atomica de ${dest}." >&2
    return 1
  fi
  DISPATCHER_TMP=""
  if [ ! -f "$dest" ] || [ -L "$dest" ]; then
    DISPATCHER_RENAMED=true
    say "Erro: publicacao atomica resultou em arquivo inseguro: ${dest}" >&2
    return 1
  fi
  if [ "$(compute_sha256 "$dest")" != "$expected_sha" ]; then
    DISPATCHER_RENAMED=true
    say "Erro: SHA-256 divergente apos a publicacao atomica de ${dest}." >&2
    return 1
  fi
  DISPATCHER_RENAMED=true
  return 0
}

write_install_state() {
  local method="$1"
  local channel="$2"
  local version="$3"
  local repo_path="${4:-}"
  local tmpfile

  STATE_RENAMED=false
  mkdir -p "$CONFIG_DIR" \
    || { say "Erro: nao foi possivel preparar ${CONFIG_DIR}." >&2; return 1; }
  admin_dir_is_secure "$CONFIG_DIR" \
    || { say "Erro: diretorio do estado inseguro: ${CONFIG_DIR}" >&2; return 1; }
  # Temporario no mesmo diretorio do destino garante publicacao por rename atomico.
  tmpfile="$(mktemp "${CONFIG_DIR%/}/.install-state.env.tmp.XXXXXX")" \
    || { say "Erro: falha ao criar temporario do estado administrativo." >&2; return 1; }
  if ! {
    printf 'CONTABASE_STATE_VERSION=1\n'
    printf 'CONTABASE_INSTALL_METHOD=%s\n' "$method"
    printf 'CONTABASE_CHANNEL=%s\n' "$channel"
    printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$version"
    printf 'CONTABASE_REPO_PATH=%s\n' "$repo_path"
    if [ "$BOOTSTRAP_MODE" = true ]; then
      printf 'CONTABASE_COMMIT=%s\n' "$CONTABASE_COMMIT"
      printf 'CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1\n'
      printf 'CONTABASE_BOOTSTRAP_ADAPTER=release\n'
      printf 'CONTABASE_ARTIFACT_NAME=%s\n' "$ARTIFACT_NAME"
      printf 'CONTABASE_ARTIFACT_PLATFORM=%s\n' "$CONTABASE_ARTIFACT_PLATFORM"
      printf 'CONTABASE_ARTIFACT_SHA256=%s\n' "$CONTABASE_ARTIFACT_SHA256"
      printf 'CONTABASE_CHECKSUMS_SHA256=%s\n' "$CONTABASE_CHECKSUMS_SHA256"
      printf 'CONTABASE_HELPER_SHA256=%s\n' "$CONTABASE_HELPER_SHA256"
      printf 'CONTABASE_NETWORK_PROFILE=%s\n' "$CONTABASE_NETWORK_PROFILE_VALUE"
      printf 'CONTABASE_ACCESS_MODE=%s\n' "$CONTABASE_ACCESS_MODE_VALUE"
      printf 'CONTABASE_HOST_BIND=%s\n' "$CONTABASE_HOST_BIND_VALUE"
      printf 'CONTABASE_PORT=%s\n' "$PORT"
      printf 'CONTABASE_INSTALL_DIR=%s\n' "$INSTALL_DIR"
      printf 'CONTABASE_DATA_DIR=%s\n' "$DATA_DIR"
      printf 'CONTABASE_CONFIG_DIR=%s\n' "$CONFIG_DIR"
      printf 'CONTABASE_UPDATE_SUPPORT=legacy-release\n'
    fi
  } > "$tmpfile"; then
    rm -f "$tmpfile" 2>/dev/null || true
    say "Erro: falha ao gravar o temporario do estado administrativo." >&2
    return 1
  fi
  publish_atomic_tmp "$tmpfile" "$STATE_FILE" 0644 || return 1
  STATE_RENAMED=true
  # Commit verificado: o estado so passa a valer se o conteudo publicado conferir.
  if ! grep -qxF "CONTABASE_INSTALL_METHOD=${method}" "${STATE_FILE:-}" \
    || ! grep -qxF "CONTABASE_INSTALLED_VERSION=${version}" "${STATE_FILE:-}"; then
    say "Erro: estado administrativo publicado diverge do esperado." >&2
    return 1
  fi
  return 0
}

require_release_update_wrapper() {
  local wrapper_source wrapper_sha

  if [ "$BOOTSTRAP_MODE" = true ]; then
    wrapper_source="${SCRIPT_DIR}/lib/contabase-update-wrapper.sh"
    [ -f "$wrapper_source" ] && [ ! -L "$wrapper_source" ] && [ -s "$wrapper_source" ] \
      || fail "bundle Release sem wrapper autenticado: ${wrapper_source}; nenhuma alteracao foi executada."
    bootstrap_verify_file_digest "$wrapper_source" "$CONTABASE_UPDATE_WRAPPER_SHA256" "wrapper de update" \
      || fail "wrapper de update divergente do snapshot homologado."
    wrapper_sha="$(compute_sha256 "$wrapper_source")"
    if [ -n "$BUNDLE_WRAPPER_PATH" ]; then
      [ -f "$BUNDLE_WRAPPER_PATH" ] && [ ! -L "$BUNDLE_WRAPPER_PATH" ] \
        || fail "wrapper de update do artifact ausente ou inseguro."
      [ "$wrapper_sha" = "$BUNDLE_WRAPPER_SHA256" ] \
        || fail "wrapper de update do Bootstrap diverge do artifact Release."
    fi
  else
    wrapper_source="$BUNDLE_WRAPPER_PATH"
    [ -n "$wrapper_source" ] \
      || fail "bundle Release sem wrapper autenticado; nenhuma alteracao foi executada."
    [ -f "$wrapper_source" ] && [ ! -L "$wrapper_source" ] && [ -s "$wrapper_source" ] \
      || fail "wrapper de update ausente, vazio ou inseguro no bundle: ${wrapper_source}"
    wrapper_sha="$(compute_sha256 "$wrapper_source")"
    [ -n "$BUNDLE_WRAPPER_SHA256" ] && [ "$wrapper_sha" = "$BUNDLE_WRAPPER_SHA256" ] \
      || fail "wrapper de update foi alterado apos a validacao do artifact."
  fi

  RELEASE_WRAPPER_SOURCE="$wrapper_source"
  RELEASE_WRAPPER_SHA256="$wrapper_sha"
  say "Wrapper de update validado: ${RELEASE_WRAPPER_SOURCE} (${RELEASE_WRAPPER_SHA256})"
}

# Registra o estado anterior de um arquivo administrativo para compensacao.
# Imprime "<kind> <sha256>" onde kind e: absent (nao existia), regular (existia
# como arquivo; backup em $2 quando o diretorio de trabalho permite) ou other
# (existia, mas nao e arquivo regular; nao restauravel comprovadamente).
record_admin_previous() {
  local file="$1"
  local backup="$2"
  local sha

  if [ ! -e "$file" ] && [ ! -L "$file" ]; then
    printf 'absent \n'
    return 0
  fi
  if [ ! -f "$file" ] || [ -L "$file" ]; then
    printf 'other \n'
    return 0
  fi
  if [ -z "${TMP_DIR:-}" ] || [ ! -d "$TMP_DIR" ] || ! cp -- "$file" "$backup" 2>/dev/null; then
    printf 'regular \n'
    return 0
  fi
  sha="$(compute_sha256 "$file")"
  if [ -n "$sha" ] && [ "$(compute_sha256 "$backup")" = "$sha" ]; then
    printf 'regular %s\n' "$sha"
    return 0
  fi
  printf 'regular \n'
  return 0
}

# Verifica o install-mode publicado: regular, SHA igual ao conteudo gravado e
# 0644 (em execucao root, ownership root). Rename atomico sozinho nao prova que
# o caminho final recebeu o arquivo.
verify_published_install_mode() {
  local dest="$1"
  local expected_sha="$2"

  if [ ! -f "$dest" ] || [ -L "$dest" ]; then
    say "Erro: install-mode publicado ausente ou inseguro: ${dest}" >&2
    return 1
  fi
  if [ -z "$expected_sha" ] || [ "$(compute_sha256 "$dest")" != "$expected_sha" ]; then
    say "Erro: install-mode publicado diverge do conteudo gravado." >&2
    return 1
  fi
  case "$(path_mode "$dest")" in
    644|0644) ;;
    *)
      say "Erro: install-mode publicado sem modo 0644: ${dest}" >&2
      return 1
      ;;
  esac
  if [ "$(id -u)" -eq 0 ] && [ "$(path_uid "$dest")" != "0" ]; then
    say "Erro: install-mode publicado sem ownership root: ${dest}" >&2
    return 1
  fi
  return 0
}

# Publica o conteudo de install-mode (stdin) por rename atomico.
write_install_mode_atomic() {
  local mode_file="$1"
  local mode_dir tmp expected_sha

  MODE_RENAMED=false
  mode_dir="$(dirname "$mode_file")"
  admin_dir_is_secure "$mode_dir" \
    || { say "Erro: diretorio inseguro para ${mode_file}: ${mode_dir}" >&2; return 1; }
  tmp="$(mktemp "${mode_dir%/}/.install-mode.tmp.XXXXXX")" \
    || { say "Erro: falha ao criar temporario de ${mode_file}." >&2; return 1; }
  if ! cat > "$tmp"; then
    rm -f "$tmp" 2>/dev/null || true
    say "Erro: falha ao gravar temporario de ${mode_file}." >&2
    return 1
  fi
  expected_sha="$(compute_sha256 "$tmp")" \
    || { rm -f "$tmp" 2>/dev/null || true; say "Erro: falha ao medir temporario de ${mode_file}." >&2; return 1; }
  publish_atomic_tmp "$tmp" "$mode_file" 0644 || return 1
  MODE_RENAMED=true
  verify_published_install_mode "$mode_file" "$expected_sha" || return 1
  return 0
}

# Verifica o dispatcher publicado: regular, SHA igual ao autenticado, 0755 e,
# em execucao root, propriedade root.
verify_published_dispatcher() {
  local dest="$1"
  local expected_sha="$2"

  if [ ! -f "$dest" ] || [ -L "$dest" ]; then
    say "Erro: dispatcher publicado ausente ou inseguro: ${dest}" >&2
    return 1
  fi
  if [ "$(compute_sha256 "$dest")" != "$expected_sha" ]; then
    say "Erro: dispatcher publicado diverge do bundle autenticado." >&2
    return 1
  fi
  case "$(path_mode "$dest")" in
    755|0755) ;;
    *)
      say "Erro: dispatcher publicado sem modo 0755: ${dest}" >&2
      return 1
      ;;
  esac
  if [ "$(id -u)" -eq 0 ] && [ "$(path_uid "$dest")" != "0" ]; then
    say "Erro: dispatcher publicado sem ownership root: ${dest}" >&2
    return 1
  fi
  return 0
}

# Restaura arquivo administrativo a partir do backup verificado; quando nao
# havia arquivo antes, remove apenas a publicacao desta propria execucao.
# Retorna 1 quando a compensacao nao pode ser comprovada.
restore_admin_file() {
  local kind="$1"
  local backup="$2"
  local expected_sha="$3"
  local dest="$4"
  local mode="$5"

  case "$kind" in
    absent)
      if rm -f -- "$dest"; then
        return 0
      fi
      say "Erro: falha ao remover a publicacao incompleta: ${dest}" >&2
      return 1
      ;;
    regular)
      if [ -n "$backup" ] && [ -f "$backup" ] && [ ! -L "$backup" ] \
        && [ -n "$expected_sha" ] \
        && [ "$(compute_sha256 "$backup")" = "$expected_sha" ] \
        && atomic_publish_authenticated_file "$backup" "$dest" "$expected_sha" "$mode"; then
        return 0
      fi
      ;;
  esac
  say "Erro: compensacao nao comprovavel para ${dest}; estado anterior indisponivel ou divergente." >&2
  return 1
}

# Compensa a publicacao parcial desta execucao na ordem inversa de gravacao
# (estado -> modo -> dispatcher). Retorna 0 somente quando o estado anterior
# foi integralmente restaurado.
compensate_admin_state_failure() {
  local dispatcher="$1"
  local mode_file="$2"
  local compensated=true

  if [ "$STATE_PUBLISHED" = true ]; then
    restore_admin_file "$STATE_PREV_KIND" "$STATE_PREV_BACKUP" "$STATE_PREV_SHA" "${STATE_FILE:-}" 0644 \
      || compensated=false
  fi
  if [ "$MODE_PUBLISHED" = true ]; then
    restore_admin_file "$MODE_PREV_KIND" "$MODE_PREV_BACKUP" "$MODE_PREV_SHA" "$mode_file" 0644 \
      || compensated=false
  fi
  if [ "$DISPATCHER_PUBLISHED" = true ]; then
    restore_admin_file "$DISPATCHER_PREV_KIND" "$DISPATCHER_PREV_BACKUP" "$DISPATCHER_PREV_SHA" "$dispatcher" 0755 \
      || compensated=false
  fi
  [ "$compensated" = true ]
}

admin_state_recovery_banner() {
  say "" >&2
  say "=====================================================================" >&2
  say "INCONSISTENCIA ADMINISTRATIVA NAO COMPENSADA." >&2
  say "A instalacao do comando de atualizacao falhou e o estado anterior nao" >&2
  say "pode ser restaurado com seguranca. Nao confie em install-state.env nem" >&2
  say "em install-mode ate verificar manualmente:" >&2
  say "  - /usr/local/bin/contabase-update (sha256sum contra o bundle validado)" >&2
  say "  - ${STATE_FILE:-<estado administrativo>}" >&2
  say "Nenhuma atualizacao deve ser executada sem verificacao administrativa." >&2
  say "=====================================================================" >&2
}

# Compensa o que foi publicado nesta execucao e encerra com a causa informada.
abort_admin_state_publication() {
  local dispatcher="$1"
  local mode_file="$2"
  local context="$3"

  if ! compensate_admin_state_failure "$dispatcher" "$mode_file"; then
    admin_state_recovery_banner
  fi
  fail "${context}"
}

install_update_command() {
  local update_wrapper="/usr/local/bin/contabase-update"
  local mode_file="/etc/contabase/install-mode"
  local mode_dir
  local wrapper_source

  require_release_update_wrapper
  wrapper_source="$RELEASE_WRAPPER_SOURCE"
  [ "$(compute_sha256 "$wrapper_source")" = "$RELEASE_WRAPPER_SHA256" ] \
    || fail "wrapper de update foi alterado antes da instalacao."

  mode_dir="$(dirname "$mode_file")"
  mkdir -p "$mode_dir" || fail "nao foi possivel preparar ${mode_dir}."

  # Protocolo de publicacao/compensacao: registra o estado anterior, publica
  # dispatcher -> install-mode -> install-state.env por rename atomico e
  # compensa na ordem inversa quando qualquer etapa falhar. O estado
  # administrativo so e gravado depois do dispatcher comprovado; rename atomico
  # individual nao torna os tres arquivos uma unica transacao, por isso toda
  # falha aciona compensacao verificada ou banner de recuperacao manual.
  DISPATCHER_PREV_BACKUP="${TMP_DIR:-}/dispatcher.previous"
  read -r DISPATCHER_PREV_KIND DISPATCHER_PREV_SHA \
    < <(record_admin_previous "$update_wrapper" "$DISPATCHER_PREV_BACKUP") || true
  MODE_PREV_BACKUP="${TMP_DIR:-}/install-mode.previous"
  read -r MODE_PREV_KIND MODE_PREV_SHA \
    < <(record_admin_previous "$mode_file" "$MODE_PREV_BACKUP") || true
  STATE_PREV_BACKUP="${TMP_DIR:-}/install-state.previous"
  read -r STATE_PREV_KIND STATE_PREV_SHA \
    < <(record_admin_previous "${STATE_FILE:-}" "$STATE_PREV_BACKUP") || true
  DISPATCHER_PUBLISHED=false
  MODE_PUBLISHED=false
  STATE_PUBLISHED=false

  if ! atomic_publish_authenticated_file "$wrapper_source" "$update_wrapper" "$RELEASE_WRAPPER_SHA256" 0755; then
    [ "$DISPATCHER_RENAMED" != true ] || DISPATCHER_PUBLISHED=true
    abort_admin_state_publication "$update_wrapper" "$mode_file" \
      "falha ao publicar o dispatcher autenticado de forma atomica."
  fi
  if ! verify_published_dispatcher "$update_wrapper" "$RELEASE_WRAPPER_SHA256"; then
    DISPATCHER_PUBLISHED=true
    abort_admin_state_publication "$update_wrapper" "$mode_file" \
      "dispatcher publicado falhou na verificacao pos-publicacao."
  fi
  DISPATCHER_PUBLISHED=true

  if ! write_install_mode_atomic "$mode_file" <<< "release"; then
    [ "$MODE_RENAMED" != true ] || MODE_PUBLISHED=true
    abort_admin_state_publication "$update_wrapper" "$mode_file" \
      "falha ao publicar ${mode_file} de forma atomica."
  fi
  MODE_PUBLISHED=true

  if ! write_install_state "release" "$INSTALL_CHANNEL" "$TAG" ""; then
    [ "$STATE_RENAMED" != true ] || STATE_PUBLISHED=true
    abort_admin_state_publication "$update_wrapper" "$mode_file" \
      "falha ao publicar ${STATE_FILE} de forma atomica."
  fi
  STATE_PUBLISHED=true

  if [ ! -e /usr/local/bin/cb-update ]; then
    ln -s contabase-update /usr/local/bin/cb-update 2>/dev/null || true
  fi

  say ""
  say "Comando de atualizacao instalado:"
  say "  sudo contabase-update"
  say "  sudo cb-update"
}

install_bundle() {
  ensure_user_and_directories
  # Prepare and validate the new bundle while any current runtime is still
  # running. A permission failure aborts before the service is stopped or
  # before the existing environment is modified.
  stage_bundle
  manage_env_file
  if [ "$BOOTSTRAP_MODE" = true ]; then
    # Contrato Bootstrap inicial: nao ha servico nem runtime anterior validado.
    # O diretorio vazio criado pelo adaptador nao justifica systemctl stop.
    :
  elif [ -f "$SERVICE_FILE" ] || [ -f "$INSTALL_DIR/contabase" ]; then
    systemctl stop contabase || fail "nao foi possivel parar o servico antes da troca do bundle."
  fi
  activate_staged_bundle
  if ! render_service_file; then
    rollback_bundle || true
    fail "falha ao instalar a unit systemd; rollback do bundle solicitado."
  fi
  if ! systemctl daemon-reload; then
    rollback_bundle || true
    fail "systemd daemon-reload falhou; rollback do bundle solicitado."
  fi
  if ! systemctl enable contabase; then
    rollback_bundle || true
    fail "nao foi possivel habilitar o servico; rollback do bundle solicitado."
  fi
  NEW_RUNTIME_STARTED=true
  if ! systemctl restart contabase; then
    rollback_bundle || true
    fail "servico nao iniciou; retorno automatico bloqueado apos tentativa de startup. Preserve dados e backup; pode ser necessaria intervencao administrativa especializada com artefato compativel com o schema atual."
  fi
  if ! wait_for_healthcheck; then
    systemctl status contabase --no-pager || true
    journalctl -u contabase -n 100 --no-pager || true
    if rollback_bundle; then
      fail "novo bundle falhou; bundle anterior recuperado com healthcheck saudavel."
    fi
    fail "novo bundle falhou no healthcheck; retorno automatico bloqueado apos tentativa de startup. Preserve dados e backup; pode ser necessaria intervencao administrativa especializada com artefato compativel com o schema atual."
  fi

  NEW_BUNDLE_ACTIVE=false
  record_release_recovery
  if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
    say "Bundle anterior preservado em: $PREVIOUS_DIR"
  fi

  if [ "$BOOTSTRAP_MODE" = true ]; then
    say "Bundle ativo; estado sera registrado somente apos a verificacao do runtime."
  else
    install_update_command
  fi
}

verify_running_release() {
  [ "$BOOTSTRAP_MODE" = true ] || return 0
  bootstrap_verify_running_binary "$INSTALL_DIR" "$TAG" \
    || fail "runtime nao corresponde ao binario e a versao instalados."
  say "Runtime confirmado: processo principal executa ${INSTALL_DIR}/contabase (${TAG})."
}

bootstrap_release_main() {
  local prepared_root lib_path
  [ "$#" -eq 0 ] || fail "adaptador Bootstrap nao aceita argumentos avulsos."
  prepared_root="$(cd "${CONTABASE_BOOTSTRAP_CHECKOUT:-}" 2>/dev/null && pwd -P)" \
    || fail "checkout preparado ausente."
  [ "$prepared_root" = "$(cd "$SCRIPT_DIR" && pwd -P)" ] \
    || fail "diretorio do helper diverge do checkout preparado."
  lib_path="${SCRIPT_DIR}/lib/contabase-bootstrap-adapter.sh"
  bootstrap_verify_file_digest "$SCRIPT_SOURCE" "$CONTABASE_HELPER_SHA256" "helper Release" \
    || fail "helper Release divergente do snapshot homologado."
  bootstrap_verify_file_digest "$lib_path" "$CONTABASE_ADAPTER_LIB_SHA256" "biblioteca do adaptador" \
    || fail "biblioteca do adaptador divergente do snapshot homologado."
  bootstrap_verify_file_digest "${SCRIPT_DIR}/lib/contabase-update-wrapper.sh" "$CONTABASE_UPDATE_WRAPPER_SHA256" "wrapper de update" \
    || fail "wrapper de update divergente do snapshot homologado."
  bootstrap_validate_release_contract || exit 1
  bootstrap_validate_network release || exit 1
  bootstrap_validate_paths release || exit 1
  bootstrap_validate_no_existing_state || exit 1

  validate_version
  validate_install_channel
  validate_repo
  detect_arch
  validate_numeric_settings
  require_commands
  build_urls
  [ "$CONTABASE_ARTIFACT_PLATFORM" = "linux/${ARCH}" ] \
    || fail "plataforma do artifact diverge da arquitetura do host."
  [ "$CONTABASE_ARTIFACT_NAME" = "$ARTIFACT_NAME" ] \
    || fail "nome do artifact diverge da arquitetura do host."
  check_install_platform
  validate_runtime_config_values
  ASSUME_YES=1
  confirm_install
  download_and_validate_bundle
  require_release_update_wrapper
  bootstrap_validate_no_existing_state || exit 1
  install_bundle
  verify_running_release
  install_update_command
  say ""
  say "ContaBase ${TAG} instalado por artifact Release e saudavel."
  say "Servico: systemctl status contabase"
  say "Healthcheck: http://127.0.0.1:${PORT}/health"
  say "Configuracao preservada em: ${ENV_FILE}"
  say "Dados preservados em: ${DATA_DIR}"
}

main() {
  validate_test_mode
  prompt_version
  validate_version
  validate_install_channel
  validate_repo
  detect_arch
  validate_numeric_settings
  sync_port_from_existing_env
  require_commands
  build_urls

  if [ "$VALIDATE_ONLY" = false ]; then
    check_install_platform
    configure_new_installation_runtime
    confirm_install
  fi

  download_and_validate_bundle
  require_release_update_wrapper

  if [ "$VALIDATE_ONLY" = true ]; then
    say "Validacao concluida; nenhuma instalacao foi alterada."
    return
  fi

  install_bundle
  say ""
  say "ContaBase ${TAG} instalado e saudavel."
  say "Servico: systemctl status contabase"
  say "Healthcheck: http://127.0.0.1:${PORT}/health"
  say "Configuracao preservada em: ${ENV_FILE}"
  say "Dados preservados em: ${DATA_DIR}"
}

if [ "$BOOTSTRAP_MODE" = true ]; then
  BOOTSTRAP_LIB="${SCRIPT_DIR}/lib/contabase-bootstrap-adapter.sh"
  [ -f "$BOOTSTRAP_LIB" ] || fail "biblioteca do adaptador Bootstrap ausente."
  # shellcheck source=lib/contabase-bootstrap-adapter.sh
  . "$BOOTSTRAP_LIB" || fail "falha ao carregar o contrato do adaptador Bootstrap."
  # Exatamente uma instalacao: o processo nao saneado apenas reexecuta; somente
  # o filho saneado entra no adaptador. A origem saneada e comprovada pelo
  # marcador em argv mais o atestado integral do ambiente; o valor herdado de
  # CONTABASE_BOOTSTRAP_SANITIZED nao e prova de nada. Sem o exit, o pai
  # repetiria install.
  if [ "$BOOTSTRAP_SANITIZED_ENTRY" != true ]; then
    bootstrap_sanitized_reexec "$SCRIPT_SOURCE" "$@"
    exit $?
  fi
  bootstrap_attest_sanitized_environment \
    || fail "ambiente nao comprovadamente saneado; instalacao bloqueada."
  bootstrap_release_main "$@"
  exit $?
fi

main

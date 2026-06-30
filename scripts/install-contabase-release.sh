#!/usr/bin/env bash
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
SERVICE_FILE="/etc/systemd/system/contabase.service"
SERVICE_BACKUP=""
NEW_BUNDLE_ACTIVE=false

say() {
  printf '%s\n' "$*"
}

fail() {
  say "Erro: $*" >&2
  exit 1
}

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
  if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT

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
  for key in APP_BASE_URL ALLOWED_HOSTS TRUSTED_PROXIES CONTABASE_ACCESS_MODE; do
    case "$key" in
      APP_BASE_URL) value="$APP_BASE_URL_VALUE" ;;
      ALLOWED_HOSTS) value="$ALLOWED_HOSTS_VALUE" ;;
      TRUSTED_PROXIES) value="$TRUSTED_PROXIES_VALUE" ;;
      CONTABASE_ACCESS_MODE) value="$CONTABASE_ACCESS_MODE_VALUE" ;;
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

require_commands() {
  local cmd
  local missing=()
  for cmd in curl tar install openssl awk grep find mktemp cp wc tr; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
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
  local required
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
}

download_and_validate_bundle() {
  local artifact_file checksums_file
  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-release-install.XXXXXX")"
  EXTRACT_DIR="${TMP_DIR}/extract"
  artifact_file="${TMP_DIR}/${ARTIFACT_NAME}"
  checksums_file="${TMP_DIR}/checksums.txt"
  mkdir -p "$EXTRACT_DIR"

  say "Baixando ${ARTIFACT_URL}"
  download_file "$ARTIFACT_URL" "$artifact_file"
  say "Baixando ${CHECKSUMS_URL}"
  download_file "$CHECKSUMS_URL" "$checksums_file"
  validate_checksum "$checksums_file" "$artifact_file"
  validate_tar_entries "$artifact_file"
  tar --no-same-owner --no-same-permissions -xzf "$artifact_file" -C "$EXTRACT_DIR"
  validate_extracted_bundle "${EXTRACT_DIR}/contabase"
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

  install -d -o root -g root -m 0755 "$(dirname "$INSTALL_DIR")"
  install -d -o root -g root -m 0755 "$CONFIG_DIR"
  install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR"
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
      printf 'APP_BASE_URL=%s\n' "$APP_BASE_URL_VALUE"
      printf 'ALLOWED_HOSTS=%s\n' "$ALLOWED_HOSTS_VALUE"
      printf 'TRUSTED_PROXIES=%s\n' "$TRUSTED_PROXIES_VALUE"
      printf 'CONTABASE_ACCESS_MODE=%s\n' "$CONTABASE_ACCESS_MODE_VALUE"
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
  local xtrace_was_enabled=false
  local setup_token

  [ "$VALIDATE_ONLY" != true ] || return 0

  if [ "$SETUP_TOKEN_SUPPLIED_BY_ENV" = true ]; then
    say "CONTABASE_SETUP_TOKEN fornecido por ambiente e gravado no arquivo de configuracao."
    return 0
  fi

  [ "$SETUP_TOKEN_GENERATED_NOW" = true ] || return 0

  case "$-" in
    *x*) xtrace_was_enabled=true; set +x ;;
  esac
  setup_token="$(env_load_value "CONTABASE_SETUP_TOKEN" "$ENV_FILE")"
  say ""
  say "====================================================================="
  say "TOKEN DE SETUP INICIAL"
  say "====================================================================="
  say "Use este token em /setup para criar o primeiro workspace e usuario admin."
  say ""
  say "CONTABASE_SETUP_TOKEN=${setup_token}"
  say ""
  say "Guarde este valor. Ele nao sera exibido novamente."
  say "====================================================================="
  [ "$xtrace_was_enabled" = false ] || set -x
}

# Assert that mandatory secrets exist in an existing env file (for update/headless).
# Exits with error if any secret is missing or is a placeholder.
env_require_secrets() {
  local file="${1:-$ENV_FILE}"
  local missing=()
  local key

  for key in AUTH_ENCRYPTION_KEY SECURITY_MASTER_KEY CONTABASE_SETUP_TOKEN; do
    env_key_is_set "$key" "$file" || missing+=("$key")
  done

  [ "${#missing[@]}" -eq 0 ] && return 0

  say ""
  say "====================================================================="
  say "ERRO: Secret(s) obrigatorio(s) ausente(s) ou nao preenchido(s) em ${file}:"
  for key in "${missing[@]}"; do
    say "  - ${key}"
  done
  say ""
  say "Para resolver:"
  say "  - Edite ${file} manualmente."
  say "  - Gere valores fortes para cada secret (ex.: openssl rand -base64 32)."
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
    echo ""
    echo "[Install]"
    echo "WantedBy=multi-user.target"
  } > "$service_staging" || return 1
  install -o root -g root -m 0644 "$service_staging" "$SERVICE_FILE" || return 1
}

stage_and_swap_bundle() {
  local bundle_root timestamp
  bundle_root="${EXTRACT_DIR}/contabase"
  timestamp="$(date +%Y%m%d-%H%M%S)"
  STAGING_DIR="${INSTALL_DIR}.staging.${timestamp}.$$"
  PREVIOUS_DIR="${INSTALL_DIR}.previous.${timestamp}.$$"

  install -d -o root -g root -m 0755 "$STAGING_DIR"
  cp -a "${bundle_root}/." "$STAGING_DIR/"
  chown -R root:root "$STAGING_DIR"
  chmod 0755 "$STAGING_DIR/contabase" "$STAGING_DIR/admin"

  if [ -e "$INSTALL_DIR" ]; then
    mv "$INSTALL_DIR" "$PREVIOUS_DIR" || fail "nao foi possivel preservar o bundle anterior."
  else
    PREVIOUS_DIR=""
  fi
  if ! mv "$STAGING_DIR" "$INSTALL_DIR"; then
    if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
      mv "$PREVIOUS_DIR" "$INSTALL_DIR" || true
    fi
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
  local previous_restored=false
  say "Tentando rollback do bundle anterior; dados e configuracao nao serao restaurados."
  systemctl stop contabase >/dev/null 2>&1 || true

  if [ "$NEW_BUNDLE_ACTIVE" = true ] && [ -e "$INSTALL_DIR" ]; then
    mv "$INSTALL_DIR" "${INSTALL_DIR}.failed.$(date +%Y%m%d-%H%M%S).$$"
  fi
  if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
    mv "$PREVIOUS_DIR" "$INSTALL_DIR"
    previous_restored=true
  else
    say "Nenhum bundle anterior disponivel para rollback."
  fi
  if [ -n "$SERVICE_BACKUP" ] && [ -f "$SERVICE_BACKUP" ]; then
    cp "$SERVICE_BACKUP" "$SERVICE_FILE"
  else
    systemctl disable contabase >/dev/null 2>&1 || true
    rm -f "$SERVICE_FILE"
  fi
  systemctl daemon-reload || return 1
  [ "$previous_restored" = true ] || return 1
  systemctl restart contabase || return 1
  wait_for_healthcheck
}

write_install_state() {
  local method="$1"
  local channel="$2"
  local version="$3"
  local repo_path="${4:-}"
  local tmpfile

  mkdir -p "$CONFIG_DIR"
  tmpfile="$(mktemp "${TMPDIR:-/tmp}/contabase-install-state.XXXXXX")"
  {
    printf 'CONTABASE_STATE_VERSION=1\n'
    printf 'CONTABASE_INSTALL_METHOD=%s\n' "$method"
    printf 'CONTABASE_CHANNEL=%s\n' "$channel"
    printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$version"
    printf 'CONTABASE_REPO_PATH=%s\n' "$repo_path"
  } > "$tmpfile"
  install -o root -g root -m 0644 "$tmpfile" "$STATE_FILE"
  rm -f "$tmpfile"
}

install_update_command() {
  local update_wrapper="/usr/local/bin/contabase-update"
  local mode_file="/etc/contabase/install-mode"
  local wrapper_source="${SCRIPT_DIR}/lib/contabase-update-wrapper.sh"
  local wrapper_url="${PUBLIC_RAW_BASE}/${TAG}/scripts/lib/contabase-update-wrapper.sh"
  local wrapper_tmp=""

  write_install_state "release" "$INSTALL_CHANNEL" "$TAG" ""

  mkdir -p "$(dirname "$mode_file")"
  printf '%s\n' "release" > "$mode_file"
  chown root:root "$mode_file" 2>/dev/null || true
  chmod 0644 "$mode_file"

  if [ -f "$wrapper_source" ]; then
    install -o root -g root -m 0755 "$wrapper_source" "$update_wrapper"
  else
    command -v curl >/dev/null 2>&1 || fail "curl e obrigatorio para instalar o wrapper de update."
    wrapper_tmp="$(mktemp "${TMPDIR:-/tmp}/contabase-update-wrapper.XXXXXX")"
    if ! curl --fail --location --silent --show-error \
      --proto '=https' --tlsv1.2 \
      "$wrapper_url" -o "$wrapper_tmp"; then
      rm -f "$wrapper_tmp"
      fail "nao foi possivel baixar o wrapper de update da tag ${TAG}."
    fi
    install -o root -g root -m 0755 "$wrapper_tmp" "$update_wrapper"
    rm -f "$wrapper_tmp"
  fi

  chown root:root "$update_wrapper" 2>/dev/null || true
  chmod 0755 "$update_wrapper"

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
  manage_env_file
  stage_and_swap_bundle
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
  if ! systemctl restart contabase; then
    rollback_bundle || true
    fail "servico nao iniciou com o novo bundle."
  fi
  if ! wait_for_healthcheck; then
    systemctl status contabase --no-pager || true
    journalctl -u contabase -n 100 --no-pager || true
    if rollback_bundle; then
      fail "novo bundle falhou; bundle anterior recuperado com healthcheck saudavel."
    fi
    fail "novo bundle falhou e o rollback nao recuperou o healthcheck."
  fi

  NEW_BUNDLE_ACTIVE=false
  if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
    say "Bundle anterior preservado em: $PREVIOUS_DIR"
  fi

  install_update_command
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

main

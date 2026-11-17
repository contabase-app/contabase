#!/usr/bin/env bash
# O fluxo le secrets; mantenha xtrace desativado ate o fim do processo.
set +x
set -Eeuo pipefail

if [ "${CONTABASE_BOOTSTRAP_CONTRACT_VERSION+x}" = x ] \
  || [ "${CONTABASE_BOOTSTRAP_ADAPTER+x}" = x ] \
  || [ "${CONTABASE_INSTALL_MODE:-}" = source-bootstrap ]; then
  printf '%s\n' 'Erro: Source Bootstrap indisponivel nesta fase; instalacao adiada e sem atualizacao suportada.' >&2
  printf '%s\n' 'Nenhuma alteracao foi executada. Para VPS/LXC, use o binario da Release; Source legado e um metodo separado.' >&2
  exit 1
fi

if command -v tput >/dev/null 2>&1 && [ -t 1 ]; then
  GREEN="$(tput setaf 2)"
  RED="$(tput setaf 1)"
  YELLOW="$(tput setaf 3)"
  BLUE="$(tput setaf 4)"
  NC="$(tput sgr0)"
else
  GREEN=""
  RED=""
  YELLOW=""
  BLUE=""
  NC=""
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_LIB="${REPO_ROOT}/scripts/lib/contabase-install-state.sh"
[ -f "$STATE_LIB" ] || { echo "Erro: biblioteca de estado ausente: $STATE_LIB" >&2; exit 1; }
# shellcheck source=lib/contabase-install-state.sh
. "$STATE_LIB" || { echo "Erro: falha ao carregar o contrato de estado." >&2; exit 1; }
ENV_TEMPLATE="${REPO_ROOT}/docs/binario/contabase.env.example"
CONFIG_DIR="${CONTABASE_CONFIG_DIR:-/etc/contabase}"
ENV_FILE="${CONFIG_DIR}/contabase.env"
STATE_FILE="${CONFIG_DIR}/install-state.env"
SERVICE_FILE="${CONTABASE_SERVICE_FILE:-/etc/systemd/system/contabase.service}"
APP_DIR="${CONTABASE_INSTALL_DIR:-/opt/contabase}"
if [ -n "${CONTABASE_DATA_DIR:-}" ]; then
  DATA_DIR="$CONTABASE_DATA_DIR"
else
  DATA_DIR="${DATA_DIR:-/var/lib/contabase}"
fi
UPLOADS_DIR="${UPLOADS_DIR:-${DATA_DIR}/uploads}"
PROFILE_UPLOADS_DIR="${UPLOADS_DIR}/profile"
WORKSPACE_UPLOADS_DIR="${UPLOADS_DIR}/workspaces"
BACKUPS_DIR="${DATA_DIR}/backups"
PORT="${CONTABASE_PORT:-8080}"
TMP_BINARY="/tmp/contabase"
HEALTH_URL="http://127.0.0.1:${PORT}/health"
APP_VERSION=""
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-}"
CONTABASE_HOST_BIND_VALUE="${CONTABASE_HOST_BIND:-}"
CONTABASE_NETWORK_PROFILE_VALUE="${CONTABASE_NETWORK_PROFILE:-}"
CONTABASE_ACCESS_MODE_VALUE="${CONTABASE_ACCESS_MODE:-}"
BOOTSTRAP_MODE=false
if [ "${CONTABASE_BOOTSTRAP_CONTRACT_VERSION:-}" = "1" ]; then
  BOOTSTRAP_MODE=true
fi

error_handler() {
  echo ""
  echo -e "${RED}ERRO: instalacao binaria publica falhou.${NC}"
  echo -e "${YELLOW}Revise a saida acima antes de tentar novamente.${NC}"
}
trap 'error_handler' ERR

usage() {
  cat <<'EOF'
Uso:
  ./scripts/install-contabase-source.sh

Descricao:
  Instala o ContaBase em Linux/systemd sem Docker, usando o clone local
  do repositório público e gravando o bundle em /opt/contabase.
EOF
}

log_step() {
  echo ""
  echo -e "${BLUE}$1${NC}"
}

log_ok() {
  echo -e "${GREEN}$1${NC}"
}

log_warn() {
  echo -e "${YELLOW}$1${NC}"
}

log_error() {
  echo -e "${RED}$1${NC}"
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    log_error "Erro: este script deve ser executado como root."
    exit 1
  fi
}

require_repo_root() {
  cd "$REPO_ROOT"
  if [ ! -d .git ] || [ ! -f go.mod ] || [ ! -f scripts/build-css.sh ]; then
    log_error "Erro: execute este script na raiz do repositório ContaBase."
    exit 1
  fi
}

detect_app_version() {
  APP_VERSION="${CONTABASE_VERSION:-}"
  if [ -z "$APP_VERSION" ] && [ -f VERSION ]; then
    APP_VERSION="$(tr -d '[:space:]' < VERSION)"
  fi
  APP_VERSION="${APP_VERSION:-dev}"
}

infer_install_channel() {
  if [ -n "$INSTALL_CHANNEL" ]; then
    printf '%s' "$INSTALL_CHANNEL"
    return 0
  fi
  if [ -n "${CONTABASE_VERSION:-}" ]; then
    printf '%s' "pinned"
    return 0
  fi
  case "$APP_VERSION" in
    v*-beta.*) printf '%s' "beta" ;;
    v*.*.*)
      case "$APP_VERSION" in
        *-*) printf '%s' "pinned" ;;
        *) printf '%s' "stable" ;;
      esac
      ;;
    *) printf '%s' "pinned" ;;
  esac
}

write_install_state() {
  local method="$1"
  local channel="$2"
  local version="$3"
  local repo_path="$4"
  local kind="${5:-}"
  local ref="${6:-}"
  local commit="${7:-}"
  local origin="${8:-}"
  local tmpfile

  if [ "$method" = "source" ]; then
    state_write_atomic "$STATE_FILE" "source" "$channel" "$version" "$repo_path" \
      "$kind" "$ref" "$commit" "$origin" || return 1
    return 0
  fi

  install -d -o root -g root -m 0755 /etc/contabase
  tmpfile="$(mktemp "${TMPDIR:-/tmp}/contabase-install-state.XXXXXX")"
  {
    printf 'CONTABASE_STATE_VERSION=1\n'
    printf 'CONTABASE_INSTALL_METHOD=%s\n' "$method"
    printf 'CONTABASE_CHANNEL=%s\n' "$channel"
    printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$version"
    printf 'CONTABASE_REPO_PATH=%s\n' "$repo_path"
    if [ "$BOOTSTRAP_MODE" = true ] && [ "$method" = "source-bootstrap" ]; then
      printf 'CONTABASE_COMMIT=%s\n' "$CONTABASE_COMMIT"
      printf 'CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1\n'
      printf 'CONTABASE_BOOTSTRAP_ADAPTER=source\n'
      printf 'CONTABASE_SOURCE_ARCHIVE_SHA256=%s\n' "$CONTABASE_SOURCE_ARCHIVE_SHA256"
      printf 'CONTABASE_SOURCE_ARCHIVE_KIND=%s\n' "$CONTABASE_SOURCE_ARCHIVE_KIND"
      printf 'CONTABASE_HELPER_SHA256=%s\n' "$CONTABASE_HELPER_SHA256"
      printf 'CONTABASE_NETWORK_PROFILE=%s\n' "$CONTABASE_NETWORK_PROFILE_VALUE"
      printf 'CONTABASE_ACCESS_MODE=%s\n' "$CONTABASE_ACCESS_MODE_VALUE"
      printf 'CONTABASE_HOST_BIND=%s\n' "$CONTABASE_HOST_BIND_VALUE"
      printf 'CONTABASE_PORT=%s\n' "$PORT"
      printf 'CONTABASE_INSTALL_DIR=%s\n' "$APP_DIR"
      printf 'CONTABASE_DATA_DIR=%s\n' "$DATA_DIR"
      printf 'CONTABASE_CONFIG_DIR=%s\n' "$CONFIG_DIR"
      printf 'CONTABASE_UPDATE_SUPPORT=none\n'
    fi
  } > "$tmpfile"
  install -o root -g root -m 0644 "$tmpfile" "$STATE_FILE"
  rm -f "$tmpfile"
}

check_linux_systemd() {
  if [ "$(uname -s)" != "Linux" ]; then
    log_error "Erro: este script suporta apenas Linux."
    exit 1
  fi

  if ! command -v systemctl >/dev/null 2>&1; then
    log_error "Erro: systemctl nao encontrado."
    exit 1
  fi

  local pid1_comm=""
  if [ -r /proc/1/comm ]; then
    pid1_comm="$(tr -d '[:space:]' < /proc/1/comm)"
  fi
  if [ "$pid1_comm" != "systemd" ]; then
    log_error "Erro: PID 1 nao e systemd."
    exit 1
  fi

  if [ -f /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    OS_ID="${ID:-}"
    OS_PRETTY_NAME="${PRETTY_NAME:-}"
    unset ID PRETTY_NAME VERSION 2>/dev/null || true
    case "${OS_ID:-}" in
      debian|ubuntu)
        log_ok "Distribuicao detectada: ${OS_PRETTY_NAME:-desconhecida}."
        ;;
      *)
        log_warn "Aviso: distribuicao fora de Debian/Ubuntu detectada (${OS_PRETTY_NAME:-desconhecida}). Prosseguindo porque systemd esta funcional."
        ;;
    esac
  fi
}

require_flock() {
  if ! command -v flock >/dev/null 2>&1; then
    log_error "Erro: flock obrigatorio para atualizacao segura. Instale util-linux antes de continuar."
    exit 1
  fi
}

require_commands() {
  local cmd
  local missing=()
  require_flock
  for cmd in git go node npm curl systemctl sqlite3 openssl tar install; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      missing+=("$cmd")
    fi
  done

  if [ "${#missing[@]}" -gt 0 ]; then
    log_error "Erro: comandos obrigatorios ausentes: ${missing[*]}"
    exit 1
  fi
}

require_node_version() {
  local node_full major
  node_full="$(node --version 2>/dev/null)"
  if [ -z "$node_full" ]; then
    log_error "Erro: node nao encontrado. O toolchain CSS do ContaBase exige Node.js 22 ou 24 LTS."
    exit 1
  fi

  major="$(echo "$node_full" | sed -E 's/^v([0-9]+).*/\1/')"
  case "$major" in
  22|24)
    ;;
  *)
    log_error "Erro: Node.js ${node_full} nao e suportado pelo toolchain CSS do ContaBase."
    log_error "O toolchain CSS do ContaBase foi validado com Node.js 22 e 24 LTS."
    log_error ""
    log_error "Versoes suportadas:"
    log_error "  Node.js 24 LTS — recomendada"
    log_error "  Node.js 22 LTS — compativel"
    log_error ""
    if [ "$major" -lt 22 ] 2>/dev/null; then
      log_error "O apt do Debian 12 instala Node 18, que faz o npm pular o binario nativo @tailwindcss/oxide e quebra o build."
    fi
    log_error "Instale Node.js 24 LTS antes de continuar. Veja docs/instalacao-lxc-vps.md."
    exit 1
    ;;
  esac
}

require_go_version() {
  local required have smallest
  required="$(awk '$1 == "go" { print $2; exit }' go.mod 2>/dev/null)"
  if [ -z "$required" ]; then
    log_error "Erro: nao foi possivel ler a versao minima do Go em go.mod."
    exit 1
  fi

  have="$(go version 2>/dev/null | sed -nE 's/.*go([0-9]+\.[0-9]+(\.[0-9]+)?).*/\1/p')"
  if [ -z "$have" ]; then
    log_error "Erro: nao foi possivel determinar a versao do Go instalada (go version)."
    exit 1
  fi

  smallest="$(printf '%s\n%s\n' "$required" "$have" | sort -V | head -n1)"
  if [ "$smallest" != "$required" ]; then
    log_error "Erro: Go ${have} detectado, mas o go.mod exige Go ${required} ou superior."
    log_error "O apt do Debian 12 instala Go 1.19 (golang-go), incompativel com este projeto."
    log_error "Instale Go ${required}+ via tarball oficial (https://go.dev/dl/) ou gerenciador externo confiavel antes de continuar. Veja docs/instalacao-lxc-vps.md."
    exit 1
  fi
}

warn_if_dev_version() {
  if [ "$APP_VERSION" = "dev" ]; then
    log_warn "Aviso: VERSION efetiva = dev. Se quiser um rodape de release, ajuste o arquivo VERSION ou exporte VERSION antes da instalacao."
  fi
}

ensure_group_user() {
  if ! getent group contabase >/dev/null 2>&1; then
    groupadd --system contabase
  fi

  if ! id -u contabase >/dev/null 2>&1; then
    useradd --system --gid contabase --home-dir "$DATA_DIR" --shell /usr/sbin/nologin contabase
  fi
}

ensure_directories() {
  if [ "$BOOTSTRAP_MODE" = true ]; then
    bootstrap_create_dir_exclusive "$APP_DIR" root root 0755 \
      || { log_error "Erro: nao foi possivel criar ${APP_DIR} sem sobrescrever."; exit 1; }
    bootstrap_create_dir_exclusive "$CONFIG_DIR" root root 0755 \
      || { log_error "Erro: nao foi possivel criar ${CONFIG_DIR} sem sobrescrever."; exit 1; }
    bootstrap_create_dir_exclusive "$DATA_DIR" contabase contabase 0750 \
      || { log_error "Erro: nao foi possivel criar ${DATA_DIR} sem sobrescrever."; exit 1; }
  else
    install -d -o root -g root -m 0755 "$APP_DIR"
    install -d -o root -g root -m 0755 "$CONFIG_DIR"
    install -d -o contabase -g contabase -m 0750 "$DATA_DIR"
  fi
  install -d -o contabase -g contabase -m 0750 "$UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$PROFILE_UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$WORKSPACE_UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$BACKUPS_DIR"
}

render_default_env() {
  cat > "$ENV_FILE" <<EOF
# ContaBase binary/systemd defaults
APP_ENV=production
APP_DEBUG=false
PORT=${PORT}
DATABASE_URL=file:${DATA_DIR}/contabase.db
DATA_DIR=${DATA_DIR}
DB_FILE=${DATA_DIR}/contabase.db
UPLOADS_DIR=${DATA_DIR}/uploads
APP_BASE_URL=${APP_BASE_URL:-https://financeiro.seu-dominio.com}
ALLOWED_HOSTS=${ALLOWED_HOSTS:-financeiro.seu-dominio.com}
TRUSTED_PROXIES=${TRUSTED_PROXIES:-}
CONTABASE_ACCESS_MODE=${CONTABASE_ACCESS_MODE:-proxy}
CONTABASE_FEATURE_INVESTMENTS=false
CONTABASE_SETUP_TOKEN=__PREENCHA_APENAS_NO_SETUP_INICIAL__
AUTH_ENCRYPTION_KEY=
SECURITY_MASTER_KEY=
EOF
}

ensure_env_file() {
  if [ ! -f "$ENV_FILE" ]; then
    if [ "$BOOTSTRAP_MODE" = true ]; then
      if [ -f "$ENV_TEMPLATE" ]; then
        (set -o noclobber; cat "$ENV_TEMPLATE" > "$ENV_FILE") \
          || { log_error "Erro: nao foi possivel criar ${ENV_FILE} sem sobrescrever."; exit 1; }
      else
        (set -o noclobber; render_default_env) \
          || { log_error "Erro: nao foi possivel criar ${ENV_FILE} sem sobrescrever."; exit 1; }
      fi
    elif [ -f "$ENV_TEMPLATE" ]; then
      cp "$ENV_TEMPLATE" "$ENV_FILE"
    else
      render_default_env
    fi
  fi

  chown root:root "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
}

generate_setup_token() {
  openssl rand -base64 48 | tr -d '\n'
}

generate_auth_key() {
  openssl rand -base64 32 | tr -d '\n'
}

generate_master_key() {
  openssl rand -hex 16 | tr -d '\n'
}

get_env_value() {
  local key="$1"
  awk -F= -v key="$key" '$1 == key { value = substr($0, index($0, "=") + 1) } END { print value }' "$ENV_FILE"
}

normalize_env_value() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr -d "[:space:]'\""
}

is_invalid_secret_value() {
  local normalized
  normalized="$(normalize_env_value "$1")"

  case "$normalized" in
    ""|"change_me"|"change-me"|"changeme"|"placeholder"|"todo"|"example"|"examples"|"exemplo"|"exemplos"|"documentation"|"documentacao"|"example/documentation"|"exemplo/documentacao"|"__preencha_apenas_no_setup_inicial__")
      return 0
      ;;
  esac

  return 1
}

set_env_value_if_missing() {
  local key="$1"
  local value="$2"

  if grep -Eq "^${key}=" "$ENV_FILE"; then
    return 0
  fi

  printf '%s=%s\n' "$key" "$value" >> "$ENV_FILE"
}

set_env_value() {
  local key="$1"
  local value="$2"

  if grep -Eq "^${key}=" "$ENV_FILE"; then
    sed -i.bak "s|^${key}=.*|${key}=${value}|" "$ENV_FILE"
    rm -f "${ENV_FILE}.bak"
    return 0
  fi

  printf '%s=%s\n' "$key" "$value" >> "$ENV_FILE"
}

normalize_host_input() {
  local host="$1"
  host="${host#http://}"
  host="${host#https://}"
  host="${host%%/*}"
  host="${host%%:*}"
  printf '%s' "$host"
}

is_private_ipv4() {
  local ip="$1"
  local a b c d part

  IFS=. read -r a b c d <<EOF
$ip
EOF

  for part in "$a" "$b" "$c" "$d"; do
    case "$part" in
      ""|*[!0-9]*) return 1 ;;
    esac
    [ "$part" -ge 0 ] 2>/dev/null && [ "$part" -le 255 ] 2>/dev/null || return 1
  done

  [ "$a" -eq 10 ] && return 0
  [ "$a" -eq 172 ] && [ "$b" -ge 16 ] && [ "$b" -le 31 ] && return 0
  [ "$a" -eq 192 ] && [ "$b" -eq 168 ] && return 0
  return 1
}

configure_access_mode_interactive() {
  local choice host_raw host proxy_raw proxy

  [ "${CONTABASE_ASSUME_YES:-0}" = "1" ] && return 0
  [ -t 0 ] || return 0

  echo ""
  echo "Como voce vai acessar o ContaBase instalado por source/systemd?"
  echo "1) Local somente nesta maquina"
  echo "2) Rede local/LAN por IP privado"
  echo "3) Dominio HTTPS/proxy reverso"
  echo "4) Avancado/manual (preservar valores atuais)"
  read -r -p "Opcao (1/2/3/4) [3]: " choice

  case "${choice:-3}" in
    1)
      set_env_value "APP_BASE_URL" "http://localhost:8080"
      set_env_value "ALLOWED_HOSTS" "localhost,127.0.0.1,::1"
      set_env_value "TRUSTED_PROXIES" ""
      set_env_value "CONTABASE_ACCESS_MODE" "local"
      ;;
    2)
      read -r -p "IP privado LAN do servidor (ex: 192.168.1.50): " host_raw
      host="$(normalize_host_input "$host_raw")"
      if ! is_private_ipv4 "$host"; then
        echo "Erro: modo LAN exige IP privado RFC1918. Nao use localhost, dominio ou IP publico."
        exit 1
      fi
      set_env_value "APP_BASE_URL" "http://${host}:8080"
      set_env_value "ALLOWED_HOSTS" "$host"
      set_env_value "TRUSTED_PROXIES" ""
      set_env_value "CONTABASE_ACCESS_MODE" "lan"
      ;;
    3)
      read -r -p "Dominio HTTPS (ex: financeiro.exemplo.com): " host_raw
      host="$(normalize_host_input "$host_raw")"
      [ -n "$host" ] || { echo "Erro: dominio obrigatorio para modo proxy."; exit 1; }
      read -r -p "TRUSTED_PROXIES (IP/CIDR do proxy que conecta no app): " proxy_raw
      proxy="$(normalize_host_input "$proxy_raw")"
      set_env_value "APP_BASE_URL" "https://${host}"
      set_env_value "ALLOWED_HOSTS" "$host"
      set_env_value "TRUSTED_PROXIES" "$proxy"
      set_env_value "CONTABASE_ACCESS_MODE" "proxy"
      ;;
    4)
      ;;
    *)
      echo "Opcao invalida."
      exit 1
      ;;
  esac
}

set_secret_value_if_missing_or_invalid() {
  local key="$1"
  local value="$2"
  local current_value

  if grep -Eq "^${key}=" "$ENV_FILE"; then
    current_value="$(get_env_value "$key")"
    if is_invalid_secret_value "$current_value"; then
      sed -i.bak "s|^${key}=.*|${key}=${value}|" "$ENV_FILE"
      rm -f "${ENV_FILE}.bak"
    fi
    return 0
  fi

  printf '%s=%s\n' "$key" "$value" >> "$ENV_FILE"
}

ensure_env_values() {
  set_env_value_if_missing "APP_ENV" "production"
  set_env_value_if_missing "APP_DEBUG" "false"
  set_env_value_if_missing "PORT" "$PORT"
  set_env_value_if_missing "DATABASE_URL" "file:${DATA_DIR}/contabase.db"
  set_env_value_if_missing "DATA_DIR" "$DATA_DIR"
  set_env_value_if_missing "DB_FILE" "${DATA_DIR}/contabase.db"
  set_env_value_if_missing "UPLOADS_DIR" "${DATA_DIR}/uploads"
  set_env_value_if_missing "APP_BASE_URL" "${APP_BASE_URL:-https://financeiro.seu-dominio.com}"
  set_env_value_if_missing "ALLOWED_HOSTS" "${ALLOWED_HOSTS:-financeiro.seu-dominio.com}"
  set_env_value_if_missing "TRUSTED_PROXIES" "${TRUSTED_PROXIES:-}"
  set_env_value_if_missing "CONTABASE_ACCESS_MODE" "${CONTABASE_ACCESS_MODE:-proxy}"
  set_env_value_if_missing "CONTABASE_FEATURE_INVESTMENTS" "false"
  set_secret_value_if_missing_or_invalid "CONTABASE_SETUP_TOKEN" "$(generate_setup_token)"
  set_secret_value_if_missing_or_invalid "AUTH_ENCRYPTION_KEY" "$(generate_auth_key)"
  set_secret_value_if_missing_or_invalid "SECURITY_MASTER_KEY" "$(generate_master_key)"
  chown root:root "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
}

run_local_validations() {
  npm ci --include=optional
  ./scripts/build-css.sh
  go test ./...
  go build ./...
}

build_release_binary() {
  rm -f "$TMP_BINARY"
  go build \
    -ldflags "-X github.com/contabase-app/contabase/internal/version.Version=${APP_VERSION}" \
    -o "$TMP_BINARY" \
    ./cmd/server
}

install_bundle() {
  if [ "$BOOTSTRAP_MODE" = true ]; then
    [ ! -e "${APP_DIR}/templates" ] && [ ! -L "${APP_DIR}/templates" ] \
      || { log_error "Erro: ${APP_DIR}/templates ja existe; instalacao inicial bloqueada."; exit 1; }
    [ ! -e "${APP_DIR}/assets" ] && [ ! -L "${APP_DIR}/assets" ] \
      || { log_error "Erro: ${APP_DIR}/assets ja existe; instalacao inicial bloqueada."; exit 1; }
    [ ! -e "${APP_DIR}/contabase" ] && [ ! -L "${APP_DIR}/contabase" ] \
      || { log_error "Erro: ${APP_DIR}/contabase ja existe; instalacao inicial bloqueada."; exit 1; }
  fi
  install -o root -g root -m 0755 "$TMP_BINARY" "${APP_DIR}/contabase"
  rm -rf "${APP_DIR}/templates" "${APP_DIR}/assets"
  cp -R templates "${APP_DIR}/templates"
  cp -R assets "${APP_DIR}/assets"
  chown -R root:root "$APP_DIR"
  find "${APP_DIR}/templates" -type d -exec chmod 0755 {} +
  find "${APP_DIR}/templates" -type f -exec chmod 0644 {} +
  find "${APP_DIR}/assets" -type d -exec chmod 0755 {} +
  find "${APP_DIR}/assets" -type f -exec chmod 0644 {} +
  chmod 0755 "${APP_DIR}/contabase"
}

render_service_file() {
  local network_lines=""
  if [ "$BOOTSTRAP_MODE" = true ]; then
    [ ! -e "$SERVICE_FILE" ] && [ ! -L "$SERVICE_FILE" ] \
      || { log_error "Erro: unit systemd apareceu durante a instalacao; operacao bloqueada."; exit 1; }
    network_lines="$(bootstrap_unit_network_lines)" \
      || { log_error "Erro: perfil de rede sem restricao systemd suportada."; exit 1; }
  fi
  cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=ContaBase - Base Financeira Privada
After=network.target

[Service]
Type=simple
User=contabase
Group=contabase
WorkingDirectory=${APP_DIR}
EnvironmentFile=${ENV_FILE}
ExecStart=${APP_DIR}/contabase
Restart=on-failure
RestartSec=5
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true
ReadWritePaths=${DATA_DIR}
EOF
  if [ -n "$network_lines" ]; then
    printf '%s\n' "$network_lines" >> "$SERVICE_FILE"
  fi
  cat >> "$SERVICE_FILE" <<'EOF'

[Install]
WantedBy=multi-user.target
EOF

  chown root:root "$SERVICE_FILE"
  chmod 0644 "$SERVICE_FILE"
}

wait_for_healthcheck() {
  local max_attempts=30
  local attempt=1
  local health=""

  while [ "$attempt" -le "$max_attempts" ]; do
    health="$(curl -fsS --max-time 2 "$HEALTH_URL" 2>/dev/null || true)"
    if [ "$health" = '{"status":"healthy"}' ]; then
      return 0
    fi
    log_warn "Aguardando healthcheck... tentativa ${attempt}/${max_attempts}"
    sleep 1
    attempt=$((attempt + 1))
  done

  return 1
}

start_and_check_service() {
  systemctl daemon-reload
  systemctl enable contabase
  systemctl start contabase

  if ! wait_for_healthcheck; then
    log_warn "Healthcheck nao retornou healthy em ${HEALTH_URL}."
    systemctl status contabase --no-pager || true
    journalctl -u contabase -n 100 --no-pager || true
    exit 1
  fi
}

print_summary() {
  echo ""
  echo -e "${BLUE}Resumo final:${NC}"
  echo "Commit fonte:      $(git log --oneline -1)"
  echo "Versao:            ${APP_VERSION}"
  if [ "$BOOTSTRAP_MODE" = true ]; then
    echo "Metodo:            Source Bootstrap (experimental)"
    echo "Atualizacao:       nao suportada"
  else
    echo "Metodo:            Source legado (checkout persistente)"
    echo "Atualizacao:       contabase-update / update-contabase-source.sh"
  fi
  echo "Binario:           ${APP_DIR}/contabase"
  echo "Env:               ${ENV_FILE}"
  echo "Servico:           ${SERVICE_FILE}"
  echo "Healthcheck:       ${HEALTH_URL}"
  echo "Status do servico: $(systemctl is-active contabase 2>/dev/null || echo desconhecido)"
  echo ""
  if [ "$BOOTSTRAP_MODE" = true ]; then
    log_warn "Source Bootstrap e experimental e NAO possui atualizacao automatica suportada."
    log_warn "Nao confundir com Source legado; esta instalacao nao recebe contabase-update."
  else
    log_warn "Source Bootstrap e um metodo separado, experimental, sem atualizacao automatica suportada."
    log_warn "Nao confundir este Source legado com Source Bootstrap."
  fi
  log_warn "Para novas instalacoes VPS/LXC, prefira o binario da Release."
  log_warn "Edite ${ENV_FILE} para ajustar APP_BASE_URL, ALLOWED_HOSTS, TRUSTED_PROXIES e CONTABASE_ACCESS_MODE."
  log_warn "Apos concluir o setup inicial, remova ou comente CONTABASE_SETUP_TOKEN em ${ENV_FILE} e reinicie o servico."
  log_warn "Configure backup regular antes de operar com dados reais."
}

install_update_command() {
  local update_wrapper="/usr/local/bin/contabase-update"
  local mode_file="${CONTABASE_MODE_FILE:-${CONFIG_DIR}/install-mode}"
  local repo_path installed_channel wrapper_source
  repo_path="$REPO_ROOT"
  installed_channel="$(infer_install_channel)"
  wrapper_source="${repo_path}/scripts/lib/contabase-update-wrapper.sh"

  if [ "$BOOTSTRAP_MODE" != true ] \
    && { [ ! -f "$wrapper_source" ] || [ -L "$wrapper_source" ]; }; then
    log_error "Erro: dispatcher autenticado ausente ou inseguro: $wrapper_source"
    log_error "Nenhum contabase-update foi instalado ou substituido."
    return 1
  fi

  if [ "$BOOTSTRAP_MODE" = true ]; then
    write_install_state "source-bootstrap" "$installed_channel" "$APP_VERSION" ""
    printf '%s\n' "source-bootstrap" > "$mode_file"
    chown root:root "$mode_file" 2>/dev/null || true
    chmod 0644 "$mode_file"
    echo ""
    log_warn "Atualizacao automatica NAO instalada para este metodo."
    log_warn "O updater Source legado exige branch/upstream e rejeita HEAD destacado."
    log_warn "Esta instalacao permanece BLOQUEADA para producao ate homologacao do update."
    return 0
  fi

  write_install_state "source" "$installed_channel" "$APP_VERSION" "$repo_path" \
    "$PROVENANCE_KIND" "$PROVENANCE_REF" "$PROVENANCE_COMMIT" "$PROVENANCE_ORIGIN"
  printf '%s\n' "source" > "$mode_file"
  printf '%s\n' "$repo_path" >> "$mode_file"
  chown root:root "$mode_file" 2>/dev/null || true
  chmod 0644 "$mode_file"

  install -o root -g root -m 0755 "$wrapper_source" "$update_wrapper" || {
    log_error "Erro: nao foi possivel instalar o dispatcher autenticado."
    return 1
  }
  if [ ! -e /usr/local/bin/cb-update ]; then
    ln -s contabase-update /usr/local/bin/cb-update 2>/dev/null || true
  fi
  echo ""
  echo -e "${GREEN}Comando de atualizacao instalado:${NC}"
  echo "  sudo contabase-update"
  echo "  sudo cb-update"
  return 0
}

verify_running_source() {
  [ "$BOOTSTRAP_MODE" = true ] || return 0
  bootstrap_verify_running_binary "$APP_DIR" "$APP_VERSION" \
    || { log_error "Erro: runtime nao corresponde ao binario e a versao instalados."; exit 1; }
  if command -v go >/dev/null 2>&1 && go version -m "$APP_DIR/contabase" >/dev/null 2>&1; then
    if ! go version -m "$APP_DIR/contabase" | grep -F -- "Version=${APP_VERSION}" >/dev/null 2>&1; then
      log_error "Erro: binario em execucao nao registra a versao ${APP_VERSION}."
      exit 1
    fi
  fi
  log_ok "Runtime confirmado: processo principal executa ${APP_DIR}/contabase (${APP_VERSION})."
}

bootstrap_source_main() {
  local lib_path archive
  [ "$#" -eq 0 ] || { log_error "Erro: adaptador Bootstrap nao aceita argumentos avulsos."; exit 1; }
  lib_path="${REPO_ROOT}/scripts/lib/contabase-bootstrap-adapter.sh"
  bootstrap_verify_file_digest "${BASH_SOURCE[0]}" "$CONTABASE_HELPER_SHA256" "helper Source" \
    || { log_error "Erro: helper Source divergente do snapshot homologado."; exit 1; }
  bootstrap_verify_file_digest "$lib_path" "$CONTABASE_ADAPTER_LIB_SHA256" "biblioteca do adaptador" \
    || { log_error "Erro: biblioteca do adaptador divergente do snapshot homologado."; exit 1; }
  bootstrap_validate_source_contract || exit 1
  bootstrap_validate_network source || exit 1
  bootstrap_validate_paths source || exit 1
  bootstrap_validate_no_existing_state || exit 1

  [ "$(cd "${CONTABASE_BOOTSTRAP_CHECKOUT:-}" 2>/dev/null && pwd -P)" = "$REPO_ROOT" ] \
    || { log_error "Erro: diretorio do helper diverge do checkout preparado."; exit 1; }
  archive="${CONTABASE_SOURCE_ARCHIVE:-}"
  bootstrap_verify_file_digest "$archive" "$CONTABASE_SOURCE_ARCHIVE_SHA256" "archive Source" \
    || { log_error "Erro: archive Source divergente do snapshot homologado."; exit 1; }
  bootstrap_verify_checkout_identity "$REPO_ROOT" "${CONTABASE_VERSION}" "${CONTABASE_COMMIT}" \
    || exit 1

  require_root
  require_repo_root
  detect_app_version
  [ "$APP_VERSION" = "$CONTABASE_VERSION" ] \
    || { log_error "Erro: VERSION do checkout diverge da versao fixada."; exit 1; }

  log_step "[1/8] Preflight de sistema e comandos"
  check_linux_systemd
  require_commands
  require_node_version
  require_go_version
  log_ok "Preflight basico concluido."

  log_step "[2/8] Preparando usuario e diretorios"
  ensure_group_user
  ensure_directories
  log_ok "Usuario e diretorios preparados."

  log_step "[3/8] Preparando ${ENV_FILE}"
  ensure_env_file
  ensure_env_values
  set_env_value "PORT" "$PORT"
  set_env_value "APP_BASE_URL" "$APP_BASE_URL"
  set_env_value "ALLOWED_HOSTS" "$ALLOWED_HOSTS"
  set_env_value "TRUSTED_PROXIES" "$TRUSTED_PROXIES"
  set_env_value "CONTABASE_ACCESS_MODE" "$CONTABASE_ACCESS_MODE"
  set_env_value "CONTABASE_FEATURE_INVESTMENTS" "false"
  chown root:root "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
  log_ok "Arquivo de ambiente preparado sem segredos em stdout."

  TMP_BINARY="$(mktemp "${TMPDIR:-/tmp}/contabase-build.XXXXXX")" \
    || { log_error "Erro: nao foi possivel criar temporario de build."; exit 1; }

  log_step "[4/8] Validacoes locais de build"
  run_local_validations
  log_ok "npm ci, build CSS, go test e go build concluidos."

  log_step "[5/8] Compilando binario final"
  build_release_binary
  log_ok "Binario final compilado em ${TMP_BINARY}."

  log_step "[6/8] Instalando bundle da aplicacao"
  install_bundle
  rm -f "$TMP_BINARY"
  log_ok "Binario, templates e assets instalados em ${APP_DIR}."

  log_step "[7/8] Renderizando unit systemd"
  render_service_file
  log_ok "Unit escrita em ${SERVICE_FILE}."

  log_step "[8/8] Ativando servico e validando healthcheck"
  start_and_check_service
  verify_running_source
  log_ok "ContaBase instalado e saudavel."

  install_update_command

  print_summary
}

main() {
  if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
    usage
    exit 0
  fi

  require_root
  require_repo_root
  detect_app_version
  if ! provenance_detect "$REPO_ROOT" "$APP_VERSION"; then
    log_error "Nao foi possivel provar a Tag/branch, o commit ou a origem oficial do checkout."
    log_error "Use um checkout de tag publica ou uma branch com upstream da origem oficial."
    exit 1
  fi

  log_step "[1/8] Preflight de sistema e comandos"
  check_linux_systemd
  require_commands
  require_node_version
  require_go_version
  warn_if_dev_version
  log_ok "Preflight basico concluido."

  log_step "[2/8] Preparando usuario e diretorios"
  ensure_group_user
  ensure_directories
  log_ok "Usuario e diretorios preparados."

  log_step "[3/8] Preparando ${ENV_FILE}"
  ensure_env_file
  ensure_env_values
  configure_access_mode_interactive
  log_ok "Arquivo de ambiente preparado sem sobrescrever valores validos."

  log_step "[4/8] Validacoes locais de build"
  run_local_validations
  log_ok "npm ci, build CSS, go test e go build concluidos."

  log_step "[5/8] Compilando binario final"
  build_release_binary
  log_ok "Binario final compilado em ${TMP_BINARY}."

  log_step "[6/8] Instalando bundle da aplicacao"
  install_bundle
  log_ok "Binario, templates e assets instalados em ${APP_DIR}."

  log_step "[7/8] Renderizando unit systemd"
  render_service_file
  log_ok "Unit escrita em ${SERVICE_FILE}."

  log_step "[8/8] Ativando servico e validando healthcheck"
  start_and_check_service
  log_ok "ContaBase instalado e saudavel."

  install_update_command

  print_summary
}

if [ "$BOOTSTRAP_MODE" = true ]; then
  BOOTSTRAP_LIB="${REPO_ROOT}/scripts/lib/contabase-bootstrap-adapter.sh"
  [ -f "$BOOTSTRAP_LIB" ] || { log_error "Erro: biblioteca do adaptador Bootstrap ausente."; exit 1; }
  # shellcheck source=lib/contabase-bootstrap-adapter.sh
  . "$BOOTSTRAP_LIB" || { log_error "Erro: falha ao carregar o contrato do adaptador Bootstrap."; exit 1; }
  bootstrap_sanitized_reexec "${BASH_SOURCE[0]}" "$@"
  bootstrap_source_main "$@"
  exit $?
fi

main "$@"

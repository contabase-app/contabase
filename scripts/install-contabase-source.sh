#!/usr/bin/env bash
set -Eeuo pipefail

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
ENV_TEMPLATE="${REPO_ROOT}/docs/binario/contabase.env.example"
ENV_FILE="/etc/contabase/contabase.env"
STATE_FILE="/etc/contabase/install-state.env"
SERVICE_FILE="/etc/systemd/system/contabase.service"
APP_DIR="/opt/contabase"
DATA_DIR="${DATA_DIR:-/var/lib/contabase}"
UPLOADS_DIR="${UPLOADS_DIR:-${DATA_DIR}/uploads}"
PROFILE_UPLOADS_DIR="${UPLOADS_DIR}/profile"
WORKSPACE_UPLOADS_DIR="${UPLOADS_DIR}/workspaces"
BACKUPS_DIR="${DATA_DIR}/backups"
TMP_BINARY="/tmp/contabase"
HEALTH_URL="http://127.0.0.1:8080/health"
APP_VERSION=""
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-}"

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
  local tmpfile

  install -d -o root -g root -m 0755 /etc/contabase
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

require_commands() {
  local cmd
  local missing=()
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
  local node_version major
  node_version="$(node --version 2>/dev/null | sed 's/^v//')"
  if [ -z "$node_version" ]; then
    log_error "Erro: node nao encontrado. Instale Node.js 20 ou superior."
    exit 1
  fi

  major="${node_version%%.*}"
  if ! [ "$major" -ge 20 ] 2>/dev/null; then
    log_error "Erro: Node.js ${node_version} detectado, mas o build do CSS (Tailwind 4) exige Node.js 20 ou superior."
    log_error "O apt do Debian 12 instala Node 18, que faz o npm pular o binario nativo @tailwindcss/oxide e quebra o build."
    log_error "Instale Node.js 20+ (ex.: via NodeSource) antes de continuar. Veja docs/instalacao-lxc-vps.md."
    exit 1
  fi
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
  install -d -o root -g root -m 0755 "$APP_DIR"
  install -d -o root -g root -m 0755 /etc/contabase
  install -d -o contabase -g contabase -m 0750 "$DATA_DIR"
  install -d -o contabase -g contabase -m 0750 "$UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$PROFILE_UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$WORKSPACE_UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$BACKUPS_DIR"
}

render_default_env() {
  cat > "$ENV_FILE" <<'EOF'
# ContaBase binary/systemd defaults
APP_ENV=production
APP_DEBUG=false
PORT=8080
DATABASE_URL=file:/var/lib/contabase/contabase.db
DATA_DIR=/var/lib/contabase
DB_FILE=/var/lib/contabase/contabase.db
UPLOADS_DIR=/var/lib/contabase/uploads
APP_BASE_URL=https://financeiro.seu-dominio.com
ALLOWED_HOSTS=financeiro.seu-dominio.com
TRUSTED_PROXIES=
CONTABASE_ACCESS_MODE=proxy
CONTABASE_SETUP_TOKEN=__PREENCHA_APENAS_NO_SETUP_INICIAL__
AUTH_ENCRYPTION_KEY=
SECURITY_MASTER_KEY=
EOF
}

ensure_env_file() {
  if [ ! -f "$ENV_FILE" ]; then
    if [ -f "$ENV_TEMPLATE" ]; then
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
  set_env_value_if_missing "PORT" "8080"
  set_env_value_if_missing "DATABASE_URL" "file:/var/lib/contabase/contabase.db"
  set_env_value_if_missing "DATA_DIR" "/var/lib/contabase"
  set_env_value_if_missing "DB_FILE" "/var/lib/contabase/contabase.db"
  set_env_value_if_missing "UPLOADS_DIR" "/var/lib/contabase/uploads"
  set_env_value_if_missing "APP_BASE_URL" "https://financeiro.seu-dominio.com"
  set_env_value_if_missing "ALLOWED_HOSTS" "financeiro.seu-dominio.com"
  set_env_value_if_missing "TRUSTED_PROXIES" ""
  set_env_value_if_missing "CONTABASE_ACCESS_MODE" "proxy"
  set_secret_value_if_missing_or_invalid "CONTABASE_SETUP_TOKEN" "$(generate_setup_token)"
  set_secret_value_if_missing_or_invalid "AUTH_ENCRYPTION_KEY" "$(generate_auth_key)"
  set_secret_value_if_missing_or_invalid "SECURITY_MASTER_KEY" "$(generate_master_key)"
  chown root:root "$ENV_FILE"
  chmod 0600 "$ENV_FILE"
}

run_local_validations() {
  npm ci
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
  cat > "$SERVICE_FILE" <<'EOF'
[Unit]
Description=ContaBase - Base Financeira Privada
After=network.target

[Service]
Type=simple
User=contabase
Group=contabase
WorkingDirectory=/opt/contabase
EnvironmentFile=/etc/contabase/contabase.env
ExecStart=/opt/contabase/contabase
Restart=on-failure
RestartSec=5
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true
ReadWritePaths=/var/lib/contabase

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
  echo "Binario:           ${APP_DIR}/contabase"
  echo "Env:               ${ENV_FILE}"
  echo "Servico:           ${SERVICE_FILE}"
  echo "Healthcheck:       ${HEALTH_URL}"
  echo "Status do servico: $(systemctl is-active contabase 2>/dev/null || echo desconhecido)"
  echo ""
  log_warn "Edite ${ENV_FILE} para ajustar APP_BASE_URL, ALLOWED_HOSTS, TRUSTED_PROXIES e CONTABASE_ACCESS_MODE."
  log_warn "Apos concluir o setup inicial, remova ou comente CONTABASE_SETUP_TOKEN em ${ENV_FILE} e reinicie o servico."
  log_warn "Configure backup regular antes de operar com dados reais."
}

install_update_command() {
  local update_wrapper="/usr/local/bin/contabase-update"
  local mode_file="/etc/contabase/install-mode"
  local repo_path installed_channel wrapper_source
  repo_path="$REPO_ROOT"
  installed_channel="$(infer_install_channel)"
  wrapper_source="${repo_path}/scripts/lib/contabase-update-wrapper.sh"

  write_install_state "source" "$installed_channel" "$APP_VERSION" "$repo_path"
  printf '%s\n' "source" > "$mode_file"
  printf '%s\n' "$repo_path" >> "$mode_file"
  chown root:root "$mode_file" 2>/dev/null || true
  chmod 0644 "$mode_file"

  if [ -f "$wrapper_source" ]; then
    install -o root -g root -m 0755 "$wrapper_source" "$update_wrapper"
    if [ ! -e /usr/local/bin/cb-update ]; then
      ln -s contabase-update /usr/local/bin/cb-update 2>/dev/null || true
    fi
    echo ""
    echo -e "${GREEN}Comando de atualizacao instalado:${NC}"
    echo "  sudo contabase-update"
    echo "  sudo cb-update"
    return 0
  fi

  cat > "$update_wrapper" <<'WRAPPER_EOF'
#!/usr/bin/env bash
set -euo pipefail

MODE_FILE="/etc/contabase/install-mode"
PUBLIC_RAW_BASE="${CONTABASE_RAW_BASE:-https://raw.githubusercontent.com/contabase-app/contabase}"

say() { printf '%s\n' "$*"; }

usage() {
  cat <<EOF
Uso:
  sudo contabase-update [VERSAO]
  sudo contabase-update vMAJOR.MINOR.PATCH[-beta.N]

Atualiza o ContaBase detectando automaticamente o modo de instalacao
(binary, docker ou source) e chamando o script de update correto.

Opcional: sudo cb-update (atalho curto).

Variaveis de ambiente:
  CONTABASE_ASSUME_YES=1   Modo nao interativo (quando suportado).
EOF
}

if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
  usage
  exit 0
fi

if [ ! -f "$MODE_FILE" ]; then
  say "Nao foi possivel detectar o modo de instalacao do ContaBase."
  say "Arquivo ausente: $MODE_FILE"
  say ""
  say "Se voce instalou manualmente, execute o script de update correspondente:"
  say "  Release/LXC:  sudo env CONTABASE_VERSION=vX.Y.Z bash scripts/update-contabase-release.sh"
  say "  Docker:       ./scripts/update-contabase-docker.sh"
  say "  Source:       sudo ./scripts/update-contabase-source.sh"
  exit 1
fi

MODE="$(head -n1 "$MODE_FILE" | awk '{print $1}' | tr -d '[:space:]')"

case "$MODE" in
  binary)
    if [ "$(id -u)" -ne 0 ]; then
      say "Este modo exige root. Execute com sudo:"
      say "  sudo contabase-update [VERSAO]"
      exit 1
    fi

    VERSION="${1:-}"
    if [ -z "$VERSION" ]; then
      if [ -t 0 ]; then
        read -r -p "Versao para atualizar (ex.: vMAJOR.MINOR.PATCH[-beta.N]): " VERSION
      else
        say "Erro: informe a versao. Exemplo: sudo contabase-update vMAJOR.MINOR.PATCH[-beta.N]"
        exit 1
      fi
    fi

    case "$VERSION" in
      *-internal*) say "Erro: versoes com -internal sao privadas."; exit 1 ;;
    esac
    if [[ ! "$VERSION" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$ ]]; then
      say "Erro: versao invalida: $VERSION"
      exit 1
    fi

    TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-update.XXXXXX")"
    # shellcheck disable=SC2064
    trap 'rm -rf "$TMP_DIR"' EXIT

    INSTALL_SCRIPT="${TMP_DIR}/install.sh"
    INSTALL_URL="${PUBLIC_RAW_BASE}/${VERSION}/scripts/install.sh"

    say "Baixando instalador da versao ${VERSION}..."
    if ! curl --fail --location --silent --show-error \
      --proto '=https' --tlsv1.2 \
      "$INSTALL_URL" -o "$INSTALL_SCRIPT"; then
      say "Erro: nao foi possivel baixar o instalador da versao ${VERSION}."
      exit 1
    fi

    say "Executando atualizacao para ${VERSION}..."
    exec env \
      CONTABASE_INSTALL_METHOD=update-release \
      CONTABASE_VERSION="$VERSION" \
      CONTABASE_ASSUME_YES="${CONTABASE_ASSUME_YES:-0}" \
      bash "$INSTALL_SCRIPT"
    ;;

  docker)
    REPO_PATH="$(sed -n '2p' "$MODE_FILE" 2>/dev/null | tr -d '[:space:]')"
    if [ -z "$REPO_PATH" ] || [ ! -d "$REPO_PATH" ]; then
      say "Erro: repositorio Docker nao encontrado."
      say "Va ate o diretorio do ContaBase e execute:"
      say "  ./scripts/update-contabase-docker.sh"
      exit 1
    fi
    cd "$REPO_PATH"
    exec ./scripts/update-contabase-docker.sh "$@"
    ;;

  source)
    if [ "$(id -u)" -ne 0 ]; then
      say "Este modo exige root. Execute com sudo:"
      say "  sudo contabase-update"
      exit 1
    fi

    REPO_PATH="$(sed -n '2p' "$MODE_FILE" 2>/dev/null | tr -d '[:space:]')"
    if [ -z "$REPO_PATH" ] || [ ! -d "$REPO_PATH" ]; then
      say "Erro: repositorio source nao encontrado."
      say "Va ate o diretorio do ContaBase e execute:"
      say "  sudo ./scripts/update-contabase-source.sh"
      exit 1
    fi
    cd "$REPO_PATH"
    exec ./scripts/update-contabase-source.sh "$@"
    ;;

  *)
    say "Modo de instalacao desconhecido: $MODE"
    say "Valores aceitos: binary, docker, source"
    exit 1
    ;;
esac
WRAPPER_EOF

  chown root:root "$update_wrapper" 2>/dev/null || true
  chmod 0755 "$update_wrapper"

  if [ ! -e /usr/local/bin/cb-update ]; then
    ln -s contabase-update /usr/local/bin/cb-update 2>/dev/null || true
  fi

  echo ""
  echo -e "${GREEN}Comando de atualizacao instalado:${NC}"
  echo "  sudo contabase-update"
  echo "  sudo cb-update"
}

main() {
  if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
    usage
    exit 0
  fi

  require_root
  require_repo_root
  detect_app_version

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

main "$@"

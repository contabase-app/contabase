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
APP_DIR="/opt/contabase"
DATA_DIR="${DATA_DIR:-/var/lib/contabase}"
STATE_FILE="/etc/contabase/install-state.env"
UPLOADS_DIR="${UPLOADS_DIR:-${DATA_DIR}/uploads}"
PROFILE_UPLOADS_DIR="${UPLOADS_DIR}/profile"
WORKSPACE_UPLOADS_DIR="${UPLOADS_DIR}/workspaces"
BACKUPS_DIR="${DATA_DIR}/backups"
DB_FILE="${DATA_DIR}/contabase.db"
TMP_BINARY="/tmp/contabase"
HEALTH_URL="http://127.0.0.1:8080/health"
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-}"

BACKUP_DIR=""
APP_VERSION=""
CURRENT_BRANCH=""
CURRENT_COMMIT=""
UPSTREAM_REF=""
ROLLED_BACK=false
SERVICE_STOPPED=false
BUNDLE_PRESERVED=false
BUNDLE_SWAP_STARTED=false
UPDATE_SUCCEEDED=false

error_handler() {
  local exit_code=$?
  trap - ERR

  echo ""
  echo -e "${RED}ERRO: atualizacao binaria publica falhou.${NC}"
  echo -e "${YELLOW}Revise a saida acima antes de tentar novamente.${NC}"

  if [ "$UPDATE_SUCCEEDED" = true ]; then
    exit 0
  fi

  if [ "$SERVICE_STOPPED" = false ]; then
    log_warn "Falha antes de parar o servico. Nenhuma recuperacao operacional foi necessaria."
  elif [ "$BUNDLE_SWAP_STARTED" = false ]; then
    log_warn "Falha apos parar o servico, mas antes de trocar o bundle. Tentando religar a instancia anterior."
    start_service || true
  elif [ "$BUNDLE_PRESERVED" = true ]; then
    log_warn "Falha apos iniciar a troca do bundle. Tentando rollback do bundle anterior."
    rollback_bundle || true
  else
    log_error "Falha apos parar o servico sem bundle preservado para rollback automatico."
  fi

  print_failure_diagnostics
  print_summary
  exit "$exit_code"
}
trap 'error_handler' ERR

usage() {
  cat <<'EOF'
Uso:
  ./scripts/update-contabase-source.sh

Descricao:
  Atualiza o ContaBase em Linux/systemd sem Docker, criando backup preventivo
  e fazendo rollback apenas do bundle da aplicacao se o healthcheck falhar.
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

confirm_or_abort() {
  local prompt="$1"
  read -r -p "${prompt} (s/N) " answer
  case "$answer" in
    [sS]|[sS][iI][mM]) ;;
    *)
      echo "Cancelado."
      exit 1
      ;;
  esac
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

detect_branch_and_commit() {
  CURRENT_BRANCH="$(git branch --show-current)"
  CURRENT_COMMIT="$(git log --oneline -1)"
  if [ -z "$CURRENT_BRANCH" ]; then
    log_error "Erro: HEAD destacado nao e suportado neste fluxo. Use uma branch local do repositório público."
    exit 1
  fi
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

check_dirty_repo() {
  if [ -n "$(git status --short)" ]; then
    log_warn "Repositorio com alteracoes locais:"
    git status --short
    confirm_or_abort "Continuar mesmo assim? O git pull --ff-only pode falhar se suas alteracoes locais conflitarem com o update"
  fi
}

fetch_remote_state() {
  git fetch --tags origin
}

resolve_upstream() {
  if git rev-parse --abbrev-ref --symbolic-full-name "@{upstream}" >/dev/null 2>&1; then
    UPSTREAM_REF="$(git rev-parse --abbrev-ref --symbolic-full-name "@{upstream}")"
    return 0
  fi

  if git show-ref --verify --quiet "refs/remotes/origin/${CURRENT_BRANCH}"; then
    UPSTREAM_REF="origin/${CURRENT_BRANCH}"
    return 0
  fi

  log_error "Erro: nao foi possivel localizar uma referencia remota para a branch ${CURRENT_BRANCH}."
  exit 1
}

ensure_fast_forward_possible() {
  if git merge-base --is-ancestor HEAD "$UPSTREAM_REF"; then
    return 0
  fi

  if git merge-base --is-ancestor "$UPSTREAM_REF" HEAD; then
    return 0
  fi

  log_error "Erro: a branch local divergiu de ${UPSTREAM_REF}. Resolva a divergencia manualmente antes de usar este script."
  exit 1
}

record_current_state() {
  echo "Branch atual: $CURRENT_BRANCH"
  echo "Commit atual: $CURRENT_COMMIT"
  echo "Versao atual: $APP_VERSION"
  echo "Referencia remota: $UPSTREAM_REF"
}

create_backup_dir() {
  BACKUP_DIR="${BACKUPS_DIR}/pre-update-$(date +%Y%m%d-%H%M%S)"
  install -d -o contabase -g contabase -m 0750 "$BACKUP_DIR"
}

ensure_storage_dirs() {
  install -d -o contabase -g contabase -m 0750 "$DATA_DIR"
  install -d -o contabase -g contabase -m 0750 "$UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$PROFILE_UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$WORKSPACE_UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$BACKUPS_DIR"
}

backup_database() {
  if [ -f "$DB_FILE" ]; then
    sqlite3 "$DB_FILE" ".backup '${BACKUP_DIR}/contabase.db'"
  else
    log_warn "Aviso: banco nao encontrado em ${DB_FILE}. Backup do banco foi pulado."
  fi

  if [ -f "${DB_FILE}-wal" ]; then
    cp "${DB_FILE}-wal" "${BACKUP_DIR}/contabase.db-wal"
  fi

  if [ -f "${DB_FILE}-shm" ]; then
    cp "${DB_FILE}-shm" "${BACKUP_DIR}/contabase.db-shm"
  fi
}

backup_uploads() {
  if [ -d "$UPLOADS_DIR" ]; then
    tar -czf "${BACKUP_DIR}/uploads.tar.gz" -C "$DATA_DIR" uploads
  else
    log_warn "Aviso: uploads/ nao encontrado; backup de uploads foi pulado."
  fi

  chown -R contabase:contabase "$BACKUP_DIR"
}

stop_service() {
  systemctl stop contabase
  SERVICE_STOPPED=true
}

pull_fast_forward() {
  git pull --ff-only origin "$CURRENT_BRANCH"
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

preserve_previous_bundle() {
  rm -rf "${APP_DIR}/contabase.previous" "${APP_DIR}/templates.previous" "${APP_DIR}/assets.previous"

  if [ -f "${APP_DIR}/contabase" ]; then
    cp "${APP_DIR}/contabase" "${APP_DIR}/contabase.previous"
  fi

  if [ -d "${APP_DIR}/templates" ]; then
    cp -R "${APP_DIR}/templates" "${APP_DIR}/templates.previous"
  fi

  if [ -d "${APP_DIR}/assets" ]; then
    cp -R "${APP_DIR}/assets" "${APP_DIR}/assets.previous"
  fi

  BUNDLE_PRESERVED=true
}

install_new_bundle() {
  BUNDLE_SWAP_STARTED=true
  install -o root -g root -m 0755 "$TMP_BINARY" "${APP_DIR}/contabase"
  rm -rf "${APP_DIR}/templates" "${APP_DIR}/assets"
  cp -R templates "${APP_DIR}/templates"
  cp -R assets "${APP_DIR}/assets"
  chown -R root:root "${APP_DIR}"
  find "${APP_DIR}/templates" -type d -exec chmod 0755 {} +
  find "${APP_DIR}/templates" -type f -exec chmod 0644 {} +
  find "${APP_DIR}/assets" -type d -exec chmod 0755 {} +
  find "${APP_DIR}/assets" -type f -exec chmod 0644 {} +
  chmod 0755 "${APP_DIR}/contabase"
}

start_service() {
  systemctl start contabase
}

run_healthcheck() {
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

rollback_bundle() {
  ROLLED_BACK=true

  systemctl stop contabase || true

  if [ -f "${APP_DIR}/contabase.previous" ]; then
    cp "${APP_DIR}/contabase.previous" "${APP_DIR}/contabase"
    chmod 0755 "${APP_DIR}/contabase"
  fi

  if [ -d "${APP_DIR}/templates.previous" ]; then
    rm -rf "${APP_DIR}/templates"
    cp -R "${APP_DIR}/templates.previous" "${APP_DIR}/templates"
  fi

  if [ -d "${APP_DIR}/assets.previous" ]; then
    rm -rf "${APP_DIR}/assets"
    cp -R "${APP_DIR}/assets.previous" "${APP_DIR}/assets"
  fi

  chown -R root:root "${APP_DIR}"
  start_service || true
}

print_failure_diagnostics() {
  systemctl status contabase --no-pager || true
  journalctl -u contabase -n 100 --no-pager || true
}

print_summary() {
  echo ""
  echo -e "${BLUE}Resumo final:${NC}"
  echo "Branch:              $(git branch --show-current)"
  echo "Commit:              $(git log --oneline -1)"
  echo "Versao:              ${APP_VERSION}"
  echo "Backup criado:       ${BACKUP_DIR:-nao-criado}"
  echo "Status do servico:   $(systemctl is-active contabase 2>/dev/null || echo desconhecido)"
  if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then
    echo "Healthcheck:         healthy"
  else
    echo "Healthcheck:         falhou"
  fi
  if [ "$ROLLED_BACK" = true ]; then
    log_warn "Rollback do bundle foi executado. O banco NAO foi restaurado automaticamente."
  fi
}

backfill_update_command() {
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
    echo -e "${GREEN}Comando global de atualizacao instalado:${NC}"
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
  echo -e "${GREEN}Comando global de atualizacao instalado:${NC}"
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
  detect_branch_and_commit
  detect_app_version

  log_step "[1/8] Preflight de branch, sistema e comandos"
  check_linux_systemd
  require_commands
  require_node_version
  require_go_version
  check_dirty_repo
  fetch_remote_state
  resolve_upstream
  ensure_fast_forward_possible
  record_current_state
  log_ok "Preflight concluido antes de parar o servico."

  log_step "[2/8] Parando servico"
  stop_service
  log_ok "Servico contabase parado."

  log_step "[3/8] Criando backup preventivo"
  ensure_storage_dirs
  create_backup_dir
  backup_database
  backup_uploads
  log_ok "Backup criado em ${BACKUP_DIR}."

  log_step "[4/8] Atualizando repositorio com fast-forward"
  pull_fast_forward
  detect_app_version
  log_ok "Repositorio atualizado via git pull --ff-only."

  log_step "[5/8] Validacoes locais de build"
  run_local_validations
  log_ok "npm ci, build CSS, go test e go build concluidos."

  log_step "[6/8] Compilando e preservando bundle anterior"
  build_release_binary
  preserve_previous_bundle
  log_ok "Bundle anterior preservado e binario novo compilado."

  log_step "[7/8] Instalando novo bundle"
  install_new_bundle
  log_ok "Novo bundle instalado em ${APP_DIR}."

  log_step "[8/8] Iniciando servico e validando healthcheck"
  start_service
  if run_healthcheck; then
    UPDATE_SUCCEEDED=true
    log_ok "Healthcheck retornou healthy."
    backfill_update_command
    print_summary
    exit 0
  fi

  log_warn "Healthcheck falhou. Iniciando rollback do bundle anterior."
  rollback_bundle

  if run_healthcheck; then
    log_warn "Rollback do bundle anterior concluiu com healthcheck healthy."
  else
    log_error "Rollback do bundle nao recuperou healthcheck healthy."
  fi

  print_failure_diagnostics
  print_summary
  exit 1
}

main "$@"

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
BACKUP_PATH=""
BACKUP_ID=""
CANONICAL_BACKUP_HELPER=""
CANONICAL_BACKUP_HELPER_TMP=""
APP_VERSION=""
PREVIOUS_VERSION=""
PREVIOUS_VERSION_RESOLVED=false
TARGET_VERSION=""
CURRENT_BRANCH=""
CURRENT_COMMIT=""
UPSTREAM_REF=""
ROLLED_BACK=false
SERVICE_STOPPED=false
BUNDLE_PRESERVED=false
BUNDLE_SWAP_STARTED=false
UPDATE_SUCCEEDED=false
BACKUP_SUCCEEDED=false
STATE_PRESENT=false
NEW_RUNTIME_STARTED=false

error_handler() {
  local exit_code=$?
  trap - ERR

  echo ""
  echo -e "${RED}ERRO: atualizacao binaria publica falhou.${NC}"
  echo -e "${YELLOW}Revise a saida acima antes de tentar novamente.${NC}"

  if [ "$UPDATE_SUCCEEDED" = true ]; then
    exit 0
  fi

  if [ "$NEW_RUNTIME_STARTED" = true ]; then
    log_error "Falha depois do inicio do novo runtime; a instancia antiga nao sera reativada automaticamente."
    systemctl stop contabase >/dev/null 2>&1 || true
    print_post_start_failure
  elif [ "$SERVICE_STOPPED" = false ]; then
    log_warn "Falha antes de parar o servico. Nenhuma recuperacao operacional foi necessaria."
  elif [ "$BACKUP_SUCCEEDED" = false ]; then
    log_warn "Backup canonico falhou; o servico permanecera parado e nenhuma mutacao sera iniciada."
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

cleanup_update_tmp() {
  if [ -n "$CANONICAL_BACKUP_HELPER_TMP" ] && [ -f "$CANONICAL_BACKUP_HELPER_TMP" ]; then
    rm -f "$CANONICAL_BACKUP_HELPER_TMP"
  fi
}
trap cleanup_update_tmp EXIT

usage() {
  cat <<'EOF'
Uso:
  ./scripts/update-contabase-source.sh

Descricao:
  Atualiza o ContaBase em Linux/systemd sem Docker, criando backup preventivo
  Se o healthcheck do novo runtime falhar, o servico permanece parado e a
  versao anterior nao e reativada automaticamente. Para retornar a versao
  anterior, restaure explicitamente o pacote pre-update.
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

resolve_previous_version() {
  local installed_version=""
  PREVIOUS_VERSION=""
  PREVIOUS_VERSION_RESOLVED=false

  if [ -f VERSION ] && [ ! -L VERSION ] && [ -r VERSION ]; then
    installed_version="$(tr -d '[:space:]' < VERSION)"
  fi

  case "$installed_version" in
    dev)
      ;;
    v*)
      [[ "$installed_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$ ]] || return 0
      ;;
    *)
      return 0
      ;;
  esac

  PREVIOUS_VERSION="$installed_version"
  PREVIOUS_VERSION_RESOLVED=true
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

ensure_storage_dirs() {
  install -d -o contabase -g contabase -m 0750 "$DATA_DIR"
  install -d -o contabase -g contabase -m 0750 "$UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$PROFILE_UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$WORKSPACE_UPLOADS_DIR"
  install -d -o contabase -g contabase -m 0750 "$BACKUPS_DIR"
}

validate_state_paths() {
  case "$DATA_DIR" in /*) ;; *) log_error "DATA_DIR deve ser absoluto: $DATA_DIR"; return 1 ;; esac
  case "$DB_FILE" in /*) ;; *) log_error "DB_FILE deve ser absoluto: $DB_FILE"; return 1 ;; esac
  case "$UPLOADS_DIR" in /*) ;; *) log_error "UPLOADS_DIR deve ser absoluto: $UPLOADS_DIR"; return 1 ;; esac

  STATE_PRESENT=false
  if [ -e "$DB_FILE" ] || [ -L "$DB_FILE" ]; then
    [ -f "$DB_FILE" ] && [ ! -L "$DB_FILE" ] && [ -r "$DB_FILE" ] || {
      log_error "Banco existente e invalido, ambiguo ou ilegivel: $DB_FILE"
      return 1
    }
    STATE_PRESENT=true
  elif [ -e "$UPLOADS_DIR" ] || [ -L "$UPLOADS_DIR" ]; then
    log_error "Uploads existem sem o banco esperado; estado persistente inconsistente: $UPLOADS_DIR"
    return 1
  else
    log_ok "Nenhum estado persistente encontrado; update pode continuar sem pacote de backup."
    return 0
  fi

  if [ -e "$UPLOADS_DIR" ] || [ -L "$UPLOADS_DIR" ]; then
    [ -d "$UPLOADS_DIR" ] && [ ! -L "$UPLOADS_DIR" ] && [ -r "$UPLOADS_DIR" ] || {
      log_error "Uploads invalidos, ambiguos ou ilegíveis: $UPLOADS_DIR"
      return 1
    }
  fi
  [ "$PREVIOUS_VERSION_RESOLVED" = true ] || {
    log_error "Nao foi possivel identificar a versao instalada antes do backup."
    return 1
  }
}

prepare_canonical_backup_helper() {
  local target_blob actual_blob ref_version
  target_blob="$(git rev-parse "${UPSTREAM_REF}:scripts/ops/backup.sh" 2>/dev/null)" || {
    log_error "O alvo ${UPSTREAM_REF} nao contem scripts/ops/backup.sh."
    return 1
  }
  CANONICAL_BACKUP_HELPER_TMP="$(mktemp "${TMPDIR:-/tmp}/contabase-backup-helper.XXXXXX")"
  git show "${UPSTREAM_REF}:scripts/ops/backup.sh" > "$CANONICAL_BACKUP_HELPER_TMP" || return 1
  actual_blob="$(git hash-object "$CANONICAL_BACKUP_HELPER_TMP")"
  [ "$actual_blob" = "$target_blob" ] || {
    log_error "Identidade do helper materializado diverge do objeto Git alvo."
    return 1
  }
  chmod 0700 "$CANONICAL_BACKUP_HELPER_TMP"
  CANONICAL_BACKUP_HELPER="$CANONICAL_BACKUP_HELPER_TMP"
  ref_version="$(git show "${UPSTREAM_REF}:VERSION" | tr -d '[:space:]')"
  [ -n "$ref_version" ] || { log_error "VERSION ausente no alvo ${UPSTREAM_REF}."; return 1; }
  TARGET_VERSION="${CONTABASE_VERSION:-$ref_version}"
  [ "$TARGET_VERSION" = "$ref_version" ] || {
    log_error "Versao alvo solicitada (${TARGET_VERSION}) diverge de ${UPSTREAM_REF} (${ref_version})."
    return 1
  }
  log_ok "Helper canonico vinculado a ${UPSTREAM_REF} (blob ${target_blob})."
}

validate_backup_package() {
  [ -n "$BACKUP_PATH" ] && [ -d "$BACKUP_PATH" ] || return 1
  [ -f "$BACKUP_PATH/manifest.txt" ] || return 1
  [ -f "$BACKUP_PATH/checksums.sha256" ] || return 1
  [ -f "$BACKUP_PATH/database.sqlite" ] || return 1
  [ -d "$BACKUP_PATH/uploads" ] || return 1
  BACKUP_ID="$(awk -F= '$1 == "backup_id" { print $2; exit }' "$BACKUP_PATH/manifest.txt")"
  [ -n "$BACKUP_ID" ] || return 1
}

create_canonical_backup() {
  local helper_output
  [ "$STATE_PRESENT" = true ] || return 0
  install -d -o contabase -g contabase -m 0750 "$BACKUPS_DIR"
  if ! helper_output="$(
    APP_VERSION="$PREVIOUS_VERSION" \
      DATA_DIR="$DATA_DIR" DB_FILE="$DB_FILE" UPLOADS_DIR="$UPLOADS_DIR" \
      "$CANONICAL_BACKUP_HELPER" "$BACKUPS_DIR"
  )"; then
    log_error "Helper canonico de backup falhou."
    return 1
  fi
  BACKUP_PATH="$(printf '%s\n' "$helper_output" | tail -n1)"
  validate_backup_package || {
    log_error "Pacote canonico final ausente ou incompleto."
    return 1
  }
  BACKUP_DIR="$BACKUP_PATH"
  log_ok "Backup pre-update criado: $BACKUP_PATH"
  log_ok "Backup ID: $BACKUP_ID; versao anterior: $PREVIOUS_VERSION"
  log_ok "Pacote confirmado: database.sqlite + uploads/. Preserve-o ate concluir o update."
}

stop_service() {
  systemctl stop contabase
  SERVICE_STOPPED=true
}

pull_fast_forward() {
  git pull --ff-only origin "$CURRENT_BRANCH"
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

start_new_runtime() {
  NEW_RUNTIME_STARTED=true
  systemctl start contabase
}

print_post_start_failure() {
  log_error "A atualizacao NAO foi concluida."
  log_error "O runtime antigo NAO foi reativado automaticamente."
  log_error "O novo runtime foi parado; o banco pode ter sido migrado."
  log_warn "Bundle/checkout que falhou foi preservado para diagnostico."
  log_warn "Pacote pre-update: ${BACKUP_PATH:-nao aplicavel (instalacao sem estado)}"
  if [ -n "$BACKUP_PATH" ]; then
    log_warn "Restore oficial, somente apos decisao explicita do operador:"
    log_warn "  CONFIRM_APP_STOPPED=yes DATA_DIR=$DATA_DIR DB_FILE=$DB_FILE UPLOADS_DIR=$UPLOADS_DIR $REPO_ROOT/scripts/ops/restore.sh $BACKUP_PATH"
    log_warn "Aviso: o restore descarta alteracoes feitas depois desse backup."
  fi
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
  echo "Backup criado:       ${BACKUP_PATH:-nao-aplicavel}"
  echo "Backup ID:           ${BACKUP_ID:-nao-aplicavel}"
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
  if [ "$#" -gt 0 ]; then
    log_error "Erro: argumento nao suportado: $1. Backup canonico nao possui bypass."
    exit 1
  fi

  require_root
  require_repo_root
  detect_branch_and_commit
  resolve_previous_version
  APP_VERSION="${PREVIOUS_VERSION:-desconhecida}"

  log_step "[1/8] Preflight de branch, sistema e comandos"
  check_linux_systemd
  require_commands
  require_node_version
  require_go_version
  check_dirty_repo
  fetch_remote_state
  resolve_upstream
  ensure_fast_forward_possible
  prepare_canonical_backup_helper
  validate_state_paths
  record_current_state
  echo "Versao alvo: $TARGET_VERSION"
  log_ok "Preflight concluido antes de parar o servico."

  log_step "[2/8] Parando servico"
  stop_service
  log_ok "Servico contabase parado."

  log_step "[3/8] Criando pacote canonico pre-update"
  create_canonical_backup
  BACKUP_SUCCEEDED=true
  ensure_storage_dirs
  if [ "$STATE_PRESENT" = true ]; then
    log_ok "Pacote canonico criado em ${BACKUP_PATH}."
  else
    log_ok "Backup nao aplicavel: banco e uploads ausentes."
  fi

  log_step "[4/8] Atualizando repositorio com fast-forward"
  pull_fast_forward
  detect_app_version
  [ "$APP_VERSION" = "$TARGET_VERSION" ] || {
    log_error "VERSION apos pull (${APP_VERSION}) diverge do alvo validado (${TARGET_VERSION})."
    return 1
  }
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
  start_new_runtime
  if run_healthcheck; then
    UPDATE_SUCCEEDED=true
    log_ok "Healthcheck retornou healthy."
    backfill_update_command
    print_summary
    exit 0
  fi

  log_error "Healthcheck falhou depois do inicio do novo runtime."
  return 1
}

main "$@"

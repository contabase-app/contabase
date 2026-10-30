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
. "$REPO_ROOT/scripts/lib/contabase-recovery.sh"
APP_DIR="${CONTABASE_INSTALL_DIR:-/opt/contabase}"
DATA_DIR="${DATA_DIR:-/var/lib/contabase}"
STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
SERVICE_FILE=/etc/systemd/system/contabase.service
UPLOADS_DIR="${UPLOADS_DIR:-${DATA_DIR}/uploads}"
PROFILE_UPLOADS_DIR="${UPLOADS_DIR}/profile"
WORKSPACE_UPLOADS_DIR="${UPLOADS_DIR}/workspaces"
BACKUPS_DIR="${DATA_DIR}/backups"
DB_FILE="${DATA_DIR}/contabase.db"
TMP_BINARY="${CONTABASE_TMP_BINARY:-/tmp/contabase}"
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
CURRENT_VERSION=""
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
UPSTREAM_REMOTE=""
UPSTREAM_MERGE_REF=""
TARGET_OID=""
CURRENT_HEAD_OID=""
ACTUAL_HEAD_OID=""
CHECKOUT_VERSION=""
CANONICAL_BUILD_DIR=""
CHECKOUT_VERSION_BEFORE=""
PERSISTED_VERSION=""
CONFIRMED_RUNTIME_VERSION=""
RECOVERY_FINALIZE_ONLY=false
RUNTIME_CONFIRMED=false

error_handler() {
  local exit_code="${1:-$?}"
  trap - ERR INT TERM

  echo ""
  echo -e "${RED}ERRO: atualizacao binaria publica falhou.${NC}"
  echo -e "${YELLOW}Revise a saida acima antes de tentar novamente.${NC}"

  if [ "$UPDATE_SUCCEEDED" = true ]; then
    exit 0
  fi

  if [ "$RUNTIME_CONFIRMED" = true ]; then
    log_warn "Runtime alvo confirmado; mantendo a instancia saudavel para finalizar checkout/estado no retry."
  elif [ "$NEW_RUNTIME_STARTED" = true ]; then
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
trap 'error_handler $?' ERR

handle_signal() {
  error_handler 130
}
trap handle_signal INT TERM

cleanup_update_tmp() {
  if [ -n "$CANONICAL_BACKUP_HELPER_TMP" ] && [ -f "$CANONICAL_BACKUP_HELPER_TMP" ]; then
    rm -f "$CANONICAL_BACKUP_HELPER_TMP"
  fi
  if [ -n "$CANONICAL_BUILD_DIR" ] && [ -d "$CANONICAL_BUILD_DIR" ]; then
    rm -rf "$CANONICAL_BUILD_DIR"
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

resolve_checkout_version() {
  local checkout_version oid_version
  if [ ! -f VERSION ]; then
    log_error "VERSION ausente no checkout apos a transicao."
    return 1
  fi
  checkout_version="$(tr -d '[:space:]' < VERSION)"
  CHECKOUT_VERSION="$checkout_version"
  oid_version="$(git show "${TARGET_OID}:VERSION" 2>/dev/null | tr -d '[:space:]')"

  if [ "$CHECKOUT_VERSION" != "$TARGET_VERSION" ]; then
    log_error "VERSION do checkout (${CHECKOUT_VERSION}) diverge do alvo (${TARGET_VERSION})."
    return 1
  fi
  if [ "$oid_version" != "$TARGET_VERSION" ]; then
    log_error "VERSION do OID congelado (${oid_version}) diverge do alvo (${TARGET_VERSION})."
    return 1
  fi
  if [ -n "${CONTABASE_VERSION:-}" ] && [ "${CONTABASE_VERSION}" != "$TARGET_VERSION" ]; then
    log_error "CONTABASE_VERSION (${CONTABASE_VERSION}) diverge do alvo (${TARGET_VERSION})."
    return 1
  fi
  APP_VERSION="$CHECKOUT_VERSION"
  return 0
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
  CHECKOUT_VERSION_BEFORE="$installed_version"
  PREVIOUS_VERSION_RESOLVED=true
}

# === VERSION_POLICY_BLOCK_BEGIN ===
# canonicalize_version <version>
#   Parses vMAJOR.MINOR.PATCH or vMAJOR.MINOR.PATCH-beta.N into a canonical
#   5-tuple: major minor patch release_rank beta_number
#   release_rank: 0 = beta, 1 = stable; beta_number: 0 for stable.
#   Returns 0 on success, 1 on invalid/ambiguous format.
canonicalize_version() {
  local version="$1"
  local major minor patch base prerelease beta_num

  [ -n "$version" ] || return 1
  [[ "$version" =~ ^v[0-9] ]] || return 1
  case "$version" in
    *$'\n'*|*$'\r'*) return 1 ;;
  esac

  base="${version#v}"

  if [[ "$base" == *-* ]]; then
    prerelease="${base#*-}"
    base="${base%%-*}"
    [ -n "$prerelease" ] || return 1
  else
    prerelease=""
  fi

  IFS='.' read -r major minor patch <<< "$base"
  [[ "$major" =~ ^(0|[1-9][0-9]*)$ ]] || return 1
  [[ "$minor" =~ ^(0|[1-9][0-9]*)$ ]] || return 1
  [[ "$patch" =~ ^(0|[1-9][0-9]*)$ ]] || return 1

  if [ -z "$prerelease" ]; then
    printf '%s %s %s 1 0' "$major" "$minor" "$patch"
    return 0
  fi

  if [[ "$prerelease" =~ ^beta\.(0|[1-9][0-9]*)$ ]]; then
    beta_num="${BASH_REMATCH[1]}"
    printf '%s %s %s 0 %s' "$major" "$minor" "$patch" "$beta_num"
    return 0
  fi

  return 1
}

# compare_decimal <left> <right>
#   Compares two canonical non-negative decimal strings of arbitrary length
#   without arithmetic on the values. Prints -1/0/1. Returns 2 on non-canonical.
compare_decimal() {
  local left="$1" right="$2"
  local left_len right_len

  [[ "$left" =~ ^(0|[1-9][0-9]*)$ ]] || return 2
  [[ "$right" =~ ^(0|[1-9][0-9]*)$ ]] || return 2

  left_len="${#left}"
  right_len="${#right}"

  if [ "$left_len" -lt "$right_len" ]; then printf '%s' "-1"; return 0; fi
  if [ "$left_len" -gt "$right_len" ]; then printf '%s' "1"; return 0; fi
  if [ "$left" = "$right" ]; then printf '%s' "0"; return 0; fi

  if LC_ALL=C [ "$left" \< "$right" ]; then
    printf '%s' "-1"
    return 0
  fi
  printf '%s' "1"
  return 0
}

# compare_canonical <major1> <minor1> <patch1> <rank1> <beta1> \
#                  <major2> <minor2> <patch2> <rank2> <beta2>
#   Prints -1 if v1 < v2, 0 if equal, 1 if v1 > v2. Returns 2 on error.
compare_canonical() {
  local major1="$1" minor1="$2" patch1="$3" rank1="$4" beta1="$5"
  local major2="$6" minor2="$7" patch2="$8" rank2="$9" beta2="${10}"
  local cmp

  cmp="$(compare_decimal "$major1" "$major2")" || return 2
  [ "$cmp" = "0" ] || { printf '%s' "$cmp"; return 0; }

  cmp="$(compare_decimal "$minor1" "$minor2")" || return 2
  [ "$cmp" = "0" ] || { printf '%s' "$cmp"; return 0; }

  cmp="$(compare_decimal "$patch1" "$patch2")" || return 2
  [ "$cmp" = "0" ] || { printf '%s' "$cmp"; return 0; }

  cmp="$(compare_decimal "$rank1" "$rank2")" || return 2
  [ "$cmp" = "0" ] || { printf '%s' "$cmp"; return 0; }

  cmp="$(compare_decimal "$beta1" "$beta2")" || return 2
  [ "$cmp" = "0" ] || { printf '%s' "$cmp"; return 0; }

  printf '%s' "0"
  return 0
}
# === VERSION_POLICY_BLOCK_END ===

version_decision() {
  local current="$1" target="$2"
  local cc tc cmp

  if [ -z "$current" ]; then
    log_error "Versao instalada nao encontrada ou vazia."
    return 3
  fi
  if [ -z "$target" ]; then
    log_error "Versao alvo nao encontrada ou vazia."
    return 3
  fi

  cc="$(canonicalize_version "$current")" || {
    log_error "Versao instalada invalida ou ambigua: ${current}"
    return 3
  }
  tc="$(canonicalize_version "$target")" || {
    log_error "Versao alvo invalida ou ambigua: ${target}"
    return 3
  }

  cmp="$(compare_canonical $cc $tc)" || {
    log_error "Erro interno ao comparar versoes: ${current} vs ${target}"
    return 3
  }

  case "$cmp" in
    "-1") return 0 ;;
    "0")  return 2 ;;
    "1")  return 1 ;;
    *)    return 3 ;;
  esac
}

resolve_current_version() {
  local persisted=""

  if [ -f "$STATE_FILE" ]; then
    persisted="$(awk -F= '$1 == "CONTABASE_INSTALLED_VERSION" { v = substr($0, index($0, "=") + 1); gsub(/[[:space:]]/, "", v); print v; exit }' "$STATE_FILE")"
  fi

  PERSISTED_VERSION="$persisted"
  CURRENT_VERSION=""

  if [ -n "$CONFIRMED_RUNTIME_VERSION" ]; then
    CURRENT_VERSION="$CONFIRMED_RUNTIME_VERSION"
    PREVIOUS_VERSION="$CONFIRMED_RUNTIME_VERSION"
    PREVIOUS_VERSION_RESOLVED=true
    if [ "$CURRENT_VERSION" = "$TARGET_VERSION" ] \
      && { [ "$persisted" != "$CURRENT_VERSION" ] || [ "$CHECKOUT_VERSION_BEFORE" != "$CURRENT_VERSION" ]; }; then
      RECOVERY_FINALIZE_ONLY=true
    fi
    return 0
  fi

  if [ -n "$persisted" ] && [ "$PREVIOUS_VERSION_RESOLVED" = true ] && [ "$persisted" != "$PREVIOUS_VERSION" ]; then
    log_error "Contradicao de versao: estado persistido=${persisted} checkout=${PREVIOUS_VERSION}."
    return 1
  fi

  if [ -n "$persisted" ]; then
    CURRENT_VERSION="$persisted"
  elif [ "$PREVIOUS_VERSION_RESOLVED" = true ]; then
    CURRENT_VERSION="$PREVIOUS_VERSION"
  fi
  return 0
}

detect_confirmed_runtime_version() {
  local runtime_version health
  CONFIRMED_RUNTIME_VERSION=""

  [ -f "${APP_DIR}/VERSION" ] && [ ! -L "${APP_DIR}/VERSION" ] && [ -r "${APP_DIR}/VERSION" ] || return 0
  runtime_version="$(tr -d '[:space:]' < "${APP_DIR}/VERSION")"
  canonicalize_version "$runtime_version" >/dev/null 2>&1 || return 0
  health="$(curl -fsS --max-time 2 "$HEALTH_URL" 2>/dev/null || true)"
  [ "$health" = '{"status":"healthy"}' ] || return 0

  CONFIRMED_RUNTIME_VERSION="$runtime_version"
  log_ok "Runtime saudavel comprova versao instalada ${CONFIRMED_RUNTIME_VERSION}."
}

enforce_version_policy() {
  local decision_rc=0
  version_decision "$CURRENT_VERSION" "$TARGET_VERSION" || decision_rc=$?
  case "$decision_rc" in
    0) return 0 ;;
    2)
      if [ "$RECOVERY_FINALIZE_ONLY" = true ]; then
        return 0
      fi
      log_ok "ContaBase ja esta em ${CURRENT_VERSION}; nenhuma atualizacao necessaria."
      exit 0
      ;;
    1)
      log_error "Downgrade recusado: instalado=${CURRENT_VERSION} oferecido=${TARGET_VERSION}."
      log_error "O updater normal nao realiza downgrade."
      exit 1
      ;;
    3)
      exit 1
      ;;
  esac
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
  local tmpfile state_dir

  state_dir="$(dirname "$STATE_FILE")"
  install -d -o root -g root -m 0755 "$state_dir"
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

  local pid1_comm="${CONTABASE_PID1_COMM:-}"
  if [ -z "$pid1_comm" ] && [ -r /proc/1/comm ]; then
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
  required="$(awk '$1 == "go" { print $2; exit }' "$CANONICAL_BUILD_DIR/go.mod" 2>/dev/null)"
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

enforce_clean_worktree() {
  local staged tracked untracked
  staged="$(git diff --cached --name-only 2>/dev/null || true)"
  tracked="$(git diff --name-only 2>/dev/null || true)"
  untracked="$(git ls-files --others --exclude-standard 2>/dev/null || true)"

  if [ -n "$staged" ] || [ -n "$tracked" ] || [ -n "$untracked" ]; then
    log_error "Working tree nao esta limpo. Resolva manualmente o estado local antes do update."
    if [ -n "$staged" ]; then
      log_error "Mudancas staged:"
      printf '%s\n' "$staged" >&2
    fi
    if [ -n "$tracked" ]; then
      log_error "Mudancas tracked:"
      printf '%s\n' "$tracked" >&2
    fi
    if [ -n "$untracked" ]; then
      log_error "Arquivos untracked nao ignorados:"
      printf '%s\n' "$untracked" >&2
    fi
    return 1
  fi
  return 0
}

fetch_remote_state() {
  git fetch --no-tags "$UPSTREAM_REMOTE" "$UPSTREAM_MERGE_REF"
}

freeze_target_oid() {
  TARGET_OID="$(git rev-parse --verify 'FETCH_HEAD^{commit}')" || {
    log_error "Nao foi possivel congelar o alvo a partir do FETCH_HEAD."
    return 1
  }
  [ -n "$TARGET_OID" ] || {
    log_error "TARGET_OID vazio apos o fetch."
    return 1
  }
  CURRENT_HEAD_OID="$(git rev-parse --verify 'HEAD^{commit}')" || {
    log_error "Nao foi possivel resolver o HEAD atual."
    return 1
  }
  log_ok "Alvo congelado: ${TARGET_OID} (HEAD atual: ${CURRENT_HEAD_OID})."
}

resolve_upstream() {
  local remote merge_ref remote_branch

  remote="$(git config --get "branch.${CURRENT_BRANCH}.remote" 2>/dev/null || true)"
  merge_ref="$(git config --get "branch.${CURRENT_BRANCH}.merge" 2>/dev/null || true)"

  if [ -n "$remote" ] && [ -n "$merge_ref" ]; then
    UPSTREAM_REMOTE="$remote"
    UPSTREAM_MERGE_REF="$merge_ref"
  else
    UPSTREAM_REMOTE="origin"
    UPSTREAM_MERGE_REF="refs/heads/${CURRENT_BRANCH}"
  fi

  remote_branch="${UPSTREAM_MERGE_REF#refs/heads/}"
  UPSTREAM_REF="${UPSTREAM_REMOTE}/${remote_branch}"
  log_ok "Upstream resolvido: remote=${UPSTREAM_REMOTE} ref=${UPSTREAM_MERGE_REF}."
}

ensure_fast_forward_possible() {
  if git merge-base --is-ancestor "$CURRENT_HEAD_OID" "$TARGET_OID"; then
    return 0
  fi

  log_error "Erro: HEAD atual (${CURRENT_HEAD_OID}) nao e ancestral do alvo congelado (${TARGET_OID})."
  log_error "Resolva a divergencia manualmente antes de usar este script."
  return 1
}

record_current_state() {
  echo "Branch atual: $CURRENT_BRANCH"
  echo "Commit atual: $CURRENT_COMMIT"
  echo "Versao atual: $APP_VERSION"
  echo "Referencia remota: $UPSTREAM_REF"
  echo "Target OID: $TARGET_OID"
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
  local target_blob actual_blob
  target_blob="$(git rev-parse "${TARGET_OID}:scripts/ops/backup.sh" 2>/dev/null)" || {
    log_error "O alvo ${TARGET_OID} nao contem scripts/ops/backup.sh."
    return 1
  }
  CANONICAL_BACKUP_HELPER_TMP="$(mktemp "${TMPDIR:-/tmp}/contabase-backup-helper.XXXXXX")"
  git show "${TARGET_OID}:scripts/ops/backup.sh" > "$CANONICAL_BACKUP_HELPER_TMP" || return 1
  actual_blob="$(git hash-object "$CANONICAL_BACKUP_HELPER_TMP")"
  [ "$actual_blob" = "$target_blob" ] || {
    log_error "Identidade do helper materializado diverge do objeto Git alvo."
    return 1
  }
  chmod 0700 "$CANONICAL_BACKUP_HELPER_TMP"
  CANONICAL_BACKUP_HELPER="$CANONICAL_BACKUP_HELPER_TMP"
  log_ok "Helper canonico vinculado a ${TARGET_OID} (blob ${target_blob})."
}

resolve_target_version() {
  local ref_version
  ref_version="$(git show "${TARGET_OID}:VERSION" 2>/dev/null | tr -d '[:space:]')"
  [ -n "$ref_version" ] || {
    log_error "VERSION ausente no alvo congelado ${TARGET_OID}."
    return 1
  }
  if [ -n "${CONTABASE_VERSION:-}" ] && [ "${CONTABASE_VERSION}" != "$ref_version" ]; then
    log_error "Versao alvo solicitada (${CONTABASE_VERSION}) diverge do OID congelado ${TARGET_OID} (${ref_version})."
    return 1
  fi
  TARGET_VERSION="$ref_version"
  canonicalize_version "$TARGET_VERSION" >/dev/null 2>&1 || {
    log_error "Versao alvo invalida ou ambigua: ${TARGET_VERSION}"
    return 1
  }
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

merge_target_oid() {
  git merge --ff-only "$TARGET_OID"
}

create_canonical_build_snapshot() {
  local snapshot_version
  CANONICAL_BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-build-snapshot.XXXXXX")" || return 1
  if ! git archive --format=tar "$TARGET_OID" | tar -xf - -C "$CANONICAL_BUILD_DIR"; then
    log_error "Falha ao materializar o snapshot canonico do alvo ${TARGET_OID}."
    return 1
  fi
  if [ ! -f "$CANONICAL_BUILD_DIR/VERSION" ]; then
    log_error "Snapshot canonico sem VERSION."
    return 1
  fi
  snapshot_version="$(tr -d '[:space:]' < "$CANONICAL_BUILD_DIR/VERSION")"
  if [ "$snapshot_version" != "$TARGET_VERSION" ]; then
    log_error "VERSION do snapshot (${snapshot_version}) diverge do alvo (${TARGET_VERSION})."
    return 1
  fi
  if [ ! -f "$CANONICAL_BUILD_DIR/go.mod" ] || [ ! -f "$CANONICAL_BUILD_DIR/scripts/build-css.sh" ]; then
    log_error "Snapshot canonico incompleto para build (go.mod ou build-css.sh ausente)."
    return 1
  fi
  return 0
}

run_local_validations() {
  (
    trap - ERR
    cd "$CANONICAL_BUILD_DIR" || exit 1
    npm ci --include=optional \
      && ./scripts/build-css.sh \
      && go test ./... \
      && go build ./...
  )
}

build_release_binary() {
  rm -f "$TMP_BINARY"
  (
    trap - ERR
    set -e
    cd "$CANONICAL_BUILD_DIR" || exit 1
    go build \
      -ldflags "-X github.com/contabase-app/contabase/internal/version.Version=${APP_VERSION}" \
      -o "$TMP_BINARY" \
      ./cmd/server
  )
}

preserve_previous_bundle() {
  rm -rf "${APP_DIR}/contabase.previous" "${APP_DIR}/templates.previous" "${APP_DIR}/assets.previous" "${APP_DIR}/VERSION.previous" "${APP_DIR}/admin.previous"
  if [ -f "$APP_DIR/admin" ]; then cp "$APP_DIR/admin" "$APP_DIR/admin.previous"; fi

  if [ -f "${APP_DIR}/contabase" ]; then
    cp "${APP_DIR}/contabase" "${APP_DIR}/contabase.previous"
  fi

  if [ -d "${APP_DIR}/templates" ]; then
    cp -R "${APP_DIR}/templates" "${APP_DIR}/templates.previous"
  fi

  if [ -d "${APP_DIR}/assets" ]; then
    cp -R "${APP_DIR}/assets" "${APP_DIR}/assets.previous"
  fi

  if [ -f "${APP_DIR}/VERSION" ]; then
    cp "${APP_DIR}/VERSION" "${APP_DIR}/VERSION.previous"
  fi

  BUNDLE_PRESERVED=true
}

install_new_bundle() {
  BUNDLE_SWAP_STARTED=true
  install -o root -g root -m 0755 "$TMP_BINARY" "${APP_DIR}/contabase"
  rm -rf "${APP_DIR}/templates" "${APP_DIR}/assets"
  cp -R "$CANONICAL_BUILD_DIR/templates" "${APP_DIR}/templates"
  cp -R "$CANONICAL_BUILD_DIR/assets" "${APP_DIR}/assets"
  install -o root -g root -m 0644 "$CANONICAL_BUILD_DIR/VERSION" "${APP_DIR}/VERSION"
  chown -R root:root "${APP_DIR}"
  find "${APP_DIR}/templates" -type d -exec chmod 0755 {} +
  find "${APP_DIR}/templates" -type f -exec chmod 0644 {} +
  find "${APP_DIR}/assets" -type d -exec chmod 0755 {} +
  find "${APP_DIR}/assets" -type f -exec chmod 0644 {} +
  chmod 0755 "${APP_DIR}/contabase"
}

start_service() {
  [ "${RECOVERY_COMPLETED:-false}" != true ] || return 0
  local identity version
  if [ "$NEW_RUNTIME_STARTED" = true ]; then recovery_blocked; return 1; fi
  identity="$(recovery_bundle_identity "$APP_DIR" "" "$SERVICE_FILE")" || { recovery_blocked; return 1; }
  version="$(cat "$APP_DIR/VERSION")" || return 1
  recovery_verify "$(dirname "$STATE_FILE")/recovery" bundle "$version" "$identity" || { recovery_blocked; return 1; }
  NEW_RUNTIME_STARTED=true
  if systemctl start contabase && run_healthcheck; then RECOVERY_COMPLETED=true; return 0; fi
  systemctl stop contabase || true
  return 1
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
  [ "${RECOVERY_COMPLETED:-false}" != true ] || return 0
  local identity version
  systemctl stop contabase || { recovery_blocked; return 1; }
  if [ "$NEW_RUNTIME_STARTED" = true ]; then recovery_blocked; return 1; fi
  identity="$(recovery_bundle_identity "$APP_DIR" .previous "$SERVICE_FILE")" || { recovery_blocked; return 1; }
  version="$(cat "$APP_DIR/VERSION.previous")" || return 1
  recovery_verify "$(dirname "$STATE_FILE")/recovery" bundle "$version" "$identity" || { recovery_blocked; return 1; }

  # Every copy must succeed before any start; callers may invoke this in an if.
  if [ -f "$APP_DIR/admin.previous" ]; then cp "$APP_DIR/admin.previous" "$APP_DIR/admin" || return 1; fi

  if [ -f "${APP_DIR}/contabase.previous" ]; then
    cp "${APP_DIR}/contabase.previous" "${APP_DIR}/contabase" || return 1
    chmod 0755 "${APP_DIR}/contabase" || return 1
  fi

  if [ -d "${APP_DIR}/templates.previous" ]; then
    rm -rf "${APP_DIR}/templates" || return 1
    cp -R "${APP_DIR}/templates.previous" "${APP_DIR}/templates" || return 1
  fi

  if [ -d "${APP_DIR}/assets.previous" ]; then
    rm -rf "${APP_DIR}/assets" || return 1
    cp -R "${APP_DIR}/assets.previous" "${APP_DIR}/assets" || return 1
  fi

  if [ -f "${APP_DIR}/VERSION.previous" ]; then
    cp "${APP_DIR}/VERSION.previous" "${APP_DIR}/VERSION" || return 1
    chmod 0644 "${APP_DIR}/VERSION" || return 1
  else
    rm -f "${APP_DIR}/VERSION"
  fi

  chown -R root:root "${APP_DIR}" || return 1
  start_service || return 1
  ROLLED_BACK=true
}

record_source_recovery() {
  local identity
  recovery_source_known "$CANONICAL_BUILD_DIR" \
    && cmp -s "$TMP_BINARY" "$APP_DIR/contabase" \
    && identity="$(recovery_bundle_identity "$APP_DIR" "" "$SERVICE_FILE")" \
    && recovery_record "$(dirname "$STATE_FILE")/recovery" bundle "$TARGET_VERSION" "$identity" \
    || log_warn "Prova de recuperacao nao emitida; retorno automatico a este bundle ficara bloqueado."
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
  local update_wrapper="${CONTABASE_UPDATE_WRAPPER:-/usr/local/bin/contabase-update}"
  local update_alias="${CONTABASE_UPDATE_ALIAS:-/usr/local/bin/cb-update}"
  local mode_file="${CONTABASE_MODE_FILE:-/etc/contabase/install-mode}"
  local repo_path installed_channel wrapper_source
  repo_path="$REPO_ROOT"
  installed_channel="$(infer_install_channel)"
  wrapper_source="${repo_path}/scripts/lib/contabase-update-wrapper.sh"

  printf '%s\n' "source" > "$mode_file"
  printf '%s\n' "$repo_path" >> "$mode_file"
  chown root:root "$mode_file" 2>/dev/null || true
  chmod 0644 "$mode_file"

  if [ -f "$wrapper_source" ]; then
    install -o root -g root -m 0755 "$wrapper_source" "$update_wrapper"
    if [ ! -e "$update_alias" ]; then
      ln -s "$(basename "$update_wrapper")" "$update_alias" 2>/dev/null || true
    fi
    write_install_state "source" "$installed_channel" "$APP_VERSION" "$repo_path"
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

  if [ ! -e "$update_alias" ]; then
    ln -s "$(basename "$update_wrapper")" "$update_alias" 2>/dev/null || true
  fi

  write_install_state "source" "$installed_channel" "$APP_VERSION" "$repo_path"

  echo ""
  echo -e "${GREEN}Comando global de atualizacao instalado:${NC}"
  echo "  sudo contabase-update"
  echo "  sudo cb-update"
}

finalize_confirmed_update() {
  local head_oid

  enforce_clean_worktree || return 1
  head_oid="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  if [ "$head_oid" != "$TARGET_OID" ]; then
    git merge-base --is-ancestor "$head_oid" "$TARGET_OID" || {
      log_error "Checkout atual nao pode avancar em fast-forward para o runtime confirmado ${TARGET_OID}."
      return 1
    }
    merge_target_oid || return 1
  fi

  ACTUAL_HEAD_OID="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  [ "$ACTUAL_HEAD_OID" = "$TARGET_OID" ] || {
    log_error "HEAD apos finalizacao (${ACTUAL_HEAD_OID}) diverge do runtime confirmado (${TARGET_OID})."
    return 1
  }
  resolve_checkout_version || return 1
  APP_VERSION="$TARGET_VERSION"
  backfill_update_command || return 1
  log_ok "Commit logico concluido: runtime, checkout e estado em ${TARGET_VERSION}."
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
  detect_confirmed_runtime_version
  APP_VERSION="${PREVIOUS_VERSION:-desconhecida}"

  log_step "[1/9] Resolvendo alvo e congelando OID"
  enforce_clean_worktree || exit 1
  resolve_upstream
  fetch_remote_state
  freeze_target_oid || exit 1
  resolve_target_version || exit 1
  resolve_current_version || exit 1
  enforce_version_policy
  ensure_fast_forward_possible || exit 1
  APP_VERSION="$TARGET_VERSION"
  echo "Versao alvo: $TARGET_VERSION"
  echo "Target OID: $TARGET_OID"
  log_ok "Alvo congelado e versao instalada resolvidos antes de qualquer mutacao."

  if [ "$RECOVERY_FINALIZE_ONLY" = true ]; then
    log_step "Finalizando transacao confirmada de execucao anterior"
    finalize_confirmed_update || exit 1
    UPDATE_SUCCEEDED=true
    print_summary
    exit 0
  fi

  log_step "[2/9] Materializando helper canonico vinculado ao OID"
  prepare_canonical_backup_helper || exit 1
  log_ok "Helper canonico materializado do OID ${TARGET_OID}."

  log_step "[3/9] Materializando snapshot canonico sem avancar o checkout"
  create_canonical_build_snapshot || exit 1
  log_ok "Snapshot canonico em ${CANONICAL_BUILD_DIR}."

  log_step "[4/9] Preflight operacional"
  check_linux_systemd
  require_commands
  require_node_version
  require_go_version
  validate_state_paths
  record_current_state
  log_ok "Preflight concluido antes de parar o servico."

  log_step "[5/9] Parando servico"
  stop_service
  log_ok "Servico contabase parado."

  log_step "[6/9] Criando pacote canonico pre-update"
  create_canonical_backup
  BACKUP_SUCCEEDED=true
  ensure_storage_dirs
  if [ "$STATE_PRESENT" = true ]; then
    log_ok "Pacote canonico criado em ${BACKUP_PATH}."
  else
    log_ok "Backup nao aplicavel: banco e uploads ausentes."
  fi

  log_step "[7/9] Validacoes e build no snapshot"
  if ! run_local_validations; then
    log_error "Validacoes locais falharam no snapshot canonico."
    return 1
  fi
  if ! build_release_binary; then
    log_error "Build do binario falhou no snapshot canonico."
    return 1
  fi
  preserve_previous_bundle
  log_ok "Build concluido no snapshot; bundle anterior preservado."

  log_step "[8/9] Instalando novo bundle e iniciando servico"
  install_new_bundle
  start_new_runtime
  if run_healthcheck; then
    RUNTIME_CONFIRMED=true
    record_source_recovery
    log_ok "Healthcheck retornou healthy."
    log_step "[9/9] Finalizando checkout e estado instalado"
    finalize_confirmed_update
    UPDATE_SUCCEEDED=true
    print_summary
    exit 0
  fi

  log_error "Healthcheck falhou depois do inicio do novo runtime."
  return 1
}

main "$@"

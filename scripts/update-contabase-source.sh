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

SCRIPT_SOURCE_RAW="${BASH_SOURCE[0]:-$0}"

bundle_fail() {
  printf 'Erro: bundle externo do updater recusado: %s\n' "$*" >&2
  exit 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum < "$1" | awk '{print $1}'
  else
    shasum -a 256 < "$1" | awk '{print $1}'
  fi
}

# Retorna 0 quando o caminho absoluto contem symlink ou componente ambiguo.
bundle_path_unsafe() {
  local remaining="$1" output="${2:-}" current="" part
  [ -n "$remaining" ] || return 1
  case "$remaining" in /*) ;; *) return 0 ;; esac
  remaining="${remaining#/}"
  while [ -n "$remaining" ]; do
    part="${remaining%%/*}"
    case "$remaining" in
      */*) remaining="${remaining#*/}" ;;
      *) remaining="" ;;
    esac
    case "$part" in
      ""|.) continue ;;
      ..) return 0 ;;
    esac
    current="$current/$part"
    [ -L "$current" ] && return 0
  done
  [ -z "$output" ] || printf -v "$output" '%s' "${current:-/}"
  return 1
}

# B1: a intencao de bundle autenticado nao depende de SCRIPT_ROOT == REPO_ROOT.
# Qualquer variavel definida (inclusive vazia) ou manifesto presente (inclusive
# symlink pendente) exige o contrato completo. Sem sinal, o modo legado segue.
bundle_contract_requested=false
[ -n "${CONTABASE_UPDATER_BUNDLE_COMMIT+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATE_WRAPPER_PATH+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATE_WRAPPER_SHA256+x}" ] && bundle_contract_requested=true
case "$SCRIPT_SOURCE_RAW" in
  /*) ;;
  *) SCRIPT_SOURCE_RAW="$PWD/$SCRIPT_SOURCE_RAW" ;;
esac
script_dir_raw="${SCRIPT_SOURCE_RAW%/*}"
script_dir_raw="${script_dir_raw:-/}"
manifest_raw="$script_dir_raw/../updater.sha256"
if [ -e "$manifest_raw" ] || [ -L "$manifest_raw" ]; then
  bundle_contract_requested=true
fi
script_path_unsafe=false
script_path_literal=""
bundle_path_unsafe "$SCRIPT_SOURCE_RAW" script_path_literal && script_path_unsafe=true
if [ "$bundle_contract_requested" = true ]; then
  [ "$script_path_unsafe" = false ] \
    || bundle_fail "caminho do updater com symlink ou componente inseguro"
  script_dir_literal="${script_path_literal%/*}"
  SCRIPT_ROOT="${script_dir_literal%/*}"
  SCRIPT_ROOT="${SCRIPT_ROOT:-/}"
else
  SCRIPT_ROOT="$(cd "$script_dir_raw/.." && pwd -P)"
  if [ -e "$SCRIPT_ROOT/updater.sha256" ] || [ -L "$SCRIPT_ROOT/updater.sha256" ]; then
    [ "$script_path_unsafe" = false ] \
      || bundle_fail "caminho do updater com symlink ou componente inseguro"
    bundle_contract_requested=true
  fi
fi
if [ -n "${CONTABASE_REPO_ROOT:-}" ]; then
  case "$CONTABASE_REPO_ROOT" in
    /*) ;;
    *) echo "Erro: CONTABASE_REPO_ROOT deve ser absoluto: $CONTABASE_REPO_ROOT" >&2; exit 1 ;;
  esac
  REPO_ROOT="$(cd "$CONTABASE_REPO_ROOT" 2>/dev/null && pwd -P)" || {
    echo "Erro: CONTABASE_REPO_ROOT inacessivel: $CONTABASE_REPO_ROOT" >&2
    exit 1
  }
else
  REPO_ROOT="$SCRIPT_ROOT"
fi

UPDATER_BUNDLE_EXTERNAL=false
LIB_ROOT="$REPO_ROOT"
VERIFIED_WRAPPER_PATH=""
VERIFIED_WRAPPER_SHA256=""
if [ "$bundle_contract_requested" = true ]; then
  UPDATER_BUNDLE_EXTERNAL=true
  LIB_ROOT="$SCRIPT_ROOT"
  [ -n "${CONTABASE_VERSION:-}" ] \
    || bundle_fail "CONTABASE_VERSION obrigatoria no modo autenticado"
fi

# Contrato de recebimento do bundle externo. Esta validacao roda ANTES de
# carregar qualquer biblioteca do bundle e antes de --help/source/exec.
# A autenticidade da propria referencia (manifesto/commit) e responsabilidade
# do dispatcher F3-C.
validate_external_bundle() {
  [ "$UPDATER_BUNDLE_EXTERNAL" = true ] || return 0

  local version="${CONTABASE_VERSION:-}"
  local commit="${CONTABASE_UPDATER_BUNDLE_COMMIT:-}"
  local expected="${CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256:-}"
  local manifest="$SCRIPT_ROOT/updater.sha256"
  local allowed="VERSION scripts/update-contabase-source.sh scripts/update-contabase-docker.sh scripts/update-contabase-release.sh scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh scripts/lib/contabase-update-wrapper.sh scripts/lib/contabase-docker-image-update.py"
  local required_paths=()
  local header actual entries hash path entry_path file actual_hash verified_file found
  local verified_files=()

  required_paths=(VERSION scripts/update-contabase-source.sh \
    scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
    scripts/lib/contabase-update-wrapper.sh)

  [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.(0|[1-9][0-9]*))?$ ]] \
    || bundle_fail "versao ausente ou invalida"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] \
    || bundle_fail "commit autorizado ausente ou invalido"
  [[ "$expected" =~ ^[0-9a-f]{64}$ ]] \
    || bundle_fail "digest autorizado do manifesto ausente ou invalido"
  [ -n "${CONTABASE_REPO_ROOT:-}" ] \
    || bundle_fail "CONTABASE_REPO_ROOT obrigatorio no modo autenticado"
  for cmd in awk sed grep find tr; do
    command -v "$cmd" >/dev/null 2>&1 \
      || bundle_fail "comando obrigatorio ausente: $cmd"
  done

  [ -d "$SCRIPT_ROOT/scripts" ] && [ ! -L "$SCRIPT_ROOT/scripts" ] \
    || bundle_fail "diretorio scripts ausente ou inseguro"
  [ -f "$manifest" ] && [ ! -L "$manifest" ] \
    || bundle_fail "updater.sha256 ausente ou inseguro"
  LC_ALL=C grep -q $'\r' "$manifest" 2>/dev/null \
    && bundle_fail "CRLF nao permitido no updater.sha256"
  actual="$(sha256_file "$manifest")" \
    || bundle_fail "falha ao calcular o SHA-256 do manifesto"
  [ "$actual" = "$expected" ] \
    || bundle_fail "manifesto diverge da identidade autorizada"
  header="$(sed -n '1p' "$manifest")" \
    || bundle_fail "falha ao ler o cabecalho do manifesto"
  [ "$header" = "commit=$commit" ] \
    || bundle_fail "commit do manifesto diverge do autorizado"

  entries="$(LC_ALL=C awk -v allowed="$allowed" '
    BEGIN { n = split(allowed, a, " "); for (i = 1; i <= n; i++) ok[a[i]] = 1 }
    NR == 1 { next }
    {
      if (length($0) < 67) exit 1
      hash = substr($0, 1, 64)
      sep = substr($0, 65, 2)
      path = substr($0, 67)
      if (length(hash) != 64 || hash ~ /[^0-9a-f]/ || sep != "  " || path == "") exit 1
      if (path ~ /(^\/|(^|\/)\.\.?(\/|$)|\\|  )/) exit 1
      if (!(path in ok) || (path in seen)) exit 1
      if (count && path < prev) exit 1
      seen[path] = 1
      prev = path
      count++
      print hash "\t" path
    }
    END { if (count == 0) exit 1 }
  ' "$manifest")" || bundle_fail "formato, ordem, duplicata ou caminho invalido no manifesto"
  [ -n "$entries" ] || bundle_fail "manifesto vazio"

  # Comparacoes literais em Bash: nenhum nome de arquivo passa por awk -v,
  # glob ou interpretacao de escape.
  for path in "${required_paths[@]}"; do
    found=false
    while IFS=$'\t' read -r hash entry_path; do
      [ -n "$hash" ] || continue
      if [ "$entry_path" = "$path" ]; then
        found=true
        break
      fi
    done <<< "$entries"
    [ "$found" = true ] || bundle_fail "manifesto incompleto: $path"
  done

  VERIFIED_WRAPPER_PATH="$SCRIPT_ROOT/scripts/lib/contabase-update-wrapper.sh"
  VERIFIED_WRAPPER_SHA256=""
  while IFS=$'\t' read -r hash entry_path; do
    [ -n "$hash" ] || continue
    if [ "$entry_path" = "scripts/lib/contabase-update-wrapper.sh" ]; then
      VERIFIED_WRAPPER_SHA256="$hash"
      break
    fi
  done <<< "$entries"
  [ -n "$VERIFIED_WRAPPER_SHA256" ] \
    || bundle_fail "wrapper verificado ausente do manifesto"

  while IFS=$'\t' read -r hash path; do
    [ -n "$hash" ] || continue
    file="$SCRIPT_ROOT/$path"
    bundle_path_unsafe "$file" \
      && bundle_fail "caminho com symlink ou componente inseguro: $path"
    [ -f "$file" ] && [ ! -L "$file" ] \
      || bundle_fail "arquivo ausente ou inseguro: $path"
    actual_hash="$(sha256_file "$file")" \
      || bundle_fail "falha ao calcular o SHA-256: $path"
    [ "$actual_hash" = "$hash" ] \
      || bundle_fail "conteudo divergente no bundle: $path"
    verified_files+=("$file")
  done <<< "$entries"

  [ "$(tr -d '[:space:]' < "$SCRIPT_ROOT/VERSION")" = "$version" ] \
    || bundle_fail "VERSION do bundle diverge da versao solicitada"

  (
    inventory="$(mktemp "${TMPDIR:-/tmp}/contabase-updater-inventory.XXXXXX")" \
      || bundle_fail "falha ao criar inventario do bundle"
    trap 'rm -f -- "$inventory"' EXIT
    find "$SCRIPT_ROOT" \
      \( -type l -o \( -type f \( -perm -0100 -o -perm -0010 -o -perm -0001 \) \) \) \
      -print0 > "$inventory" 2>/dev/null \
      || bundle_fail "falha ao enumerar executaveis do bundle"
    while IFS= read -r -d '' file; do
      [ ! -L "$file" ] || bundle_fail "symlink no bundle"
      for verified_file in "${verified_files[@]}"; do
        [ "$file" != "$verified_file" ] || continue 2
      done
      bundle_fail "arquivo executavel nao declarado: ${file#"$SCRIPT_ROOT"/}"
    done < "$inventory"
  ) || exit 1
}

validate_external_bundle

. "$LIB_ROOT/scripts/lib/contabase-recovery.sh"
STATE_LIB="${LIB_ROOT}/scripts/lib/contabase-install-state.sh"
[ -f "$STATE_LIB" ] || { echo "Erro: biblioteca de estado ausente: $STATE_LIB" >&2; exit 1; }
# shellcheck source=lib/contabase-install-state.sh
. "$STATE_LIB" || { echo "Erro: falha ao carregar o contrato de estado." >&2; exit 1; }
APP_DIR="${CONTABASE_INSTALL_DIR:-/opt/contabase}"
DATA_DIR="${DATA_DIR:-/var/lib/contabase}"
STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
FINALIZE_MARKER="$(dirname "$STATE_FILE")/finalize.pending"
TRANSITION_JOURNAL="$(dirname "$STATE_FILE")/transition.pending"
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
CHECKOUT_KIND=""
INSTALL_REF=""
INSTALL_COMMIT=""
INSTALL_ORIGIN=""
STATE_PROVENANCE_COMPLETE=false
TARGET_REF=""
TARGET_TAG=""
TARGET_OBJECT_TYPE=""
TEMP_TARGET_REF="refs/contabase-update/target"
UNSHALLOW_REF_PREFIX="refs/contabase-update/unshallow"
HEAD_BEFORE_FETCH=""
STATUS_BEFORE_FETCH=""
EFFECTIVE_ORIGIN=""
VALIDATED_ORIGIN=""
TAGS_BEFORE_FILE=""
TRANSITION_AUTHORIZED=false

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
  if [ -n "${REPO_ROOT:-}" ] && [ -d "$REPO_ROOT/.git" ]; then
    git -C "$REPO_ROOT" update-ref -d "$TEMP_TARGET_REF" 2>/dev/null || true
    git -C "$REPO_ROOT" for-each-ref --format='delete %(refname)' "$UNSHALLOW_REF_PREFIX" \
      | git -C "$REPO_ROOT" update-ref --stdin 2>/dev/null || true
  fi
  if [ -n "$TAGS_BEFORE_FILE" ] && [ -f "$TAGS_BEFORE_FILE" ]; then
    rm -f "$TAGS_BEFORE_FILE"
  fi
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

ensure_local_tag() {
  local local_oid
  [ "$CHECKOUT_KIND" = "tag" ] || return 0
  [ -n "$TARGET_TAG" ] && [ -n "$TARGET_OID" ] || return 1
  local_oid="$(git rev-parse --verify "refs/tags/${TARGET_TAG}^{commit}" 2>/dev/null || true)"
  if [ -n "$local_oid" ]; then
    [ "$local_oid" = "$TARGET_OID" ] || {
      log_error "Erro: tag local ${TARGET_TAG} diverge do alvo confirmado."
      return 1
    }
    return 0
  fi
  git tag "$TARGET_TAG" "$TARGET_OID" || {
    log_error "Erro: nao foi possivel registrar a tag local ${TARGET_TAG}."
    return 1
  }
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
      && { [ "$persisted" != "$CURRENT_VERSION" ] || [ "$CHECKOUT_VERSION_BEFORE" != "$CURRENT_VERSION" ] || [ -f "$FINALIZE_MARKER" ]; }; then
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
  local kind="${5:-}"
  local ref="${6:-}"
  local commit="${7:-}"
  local origin="${8:-}"

  if [ -z "$kind" ]; then
    kind="$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF_KIND)"
    ref="$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF)"
    origin="$(state_value "$STATE_FILE" CONTABASE_INSTALL_ORIGIN)"
    if [ -n "$kind" ]; then
      commit="$(git -C "$REPO_ROOT" rev-parse --verify 'HEAD^{commit}' 2>/dev/null || true)"
    else
      commit=""
    fi
  elif [ -z "$commit" ]; then
    commit="$(git -C "$REPO_ROOT" rev-parse --verify 'HEAD^{commit}' 2>/dev/null || true)"
  fi

  state_write_atomic "$STATE_FILE" "$method" "$channel" "$version" "$repo_path" \
    "$kind" "$ref" "$commit" "$origin"
}

reject_unsafe_git_environment() {
  local name
  while IFS= read -r name; do
    case "$name" in
      GIT_CONFIG_KEY_*|GIT_CONFIG_VALUE_*|\
      GIT_DIR|GIT_WORK_TREE|GIT_INDEX_FILE|GIT_OBJECT_DIRECTORY|\
      GIT_ALTERNATE_OBJECT_DIRECTORIES|GIT_COMMON_DIR|GIT_NAMESPACE|\
      GIT_CEILING_DIRECTORIES|GIT_DISCOVERY_ACROSS_FILESYSTEM|\
      GIT_CONFIG|GIT_CONFIG_GLOBAL|GIT_CONFIG_SYSTEM|GIT_CONFIG_COUNT|\
      GIT_CONFIG_PARAMETERS|GIT_PROXY_COMMAND|GIT_SSH|GIT_SSH_COMMAND|\
      GIT_SSH_VARIANT|GIT_ASKPASS|SSH_ASKPASS|GIT_ALLOW_PROTOCOL|\
      GIT_PROTOCOL_FROM_USER|GIT_SSL_NO_VERIFY|GIT_SSL_CAINFO|\
      GIT_SSL_CAPATH|GIT_SSL_CERT|GIT_SSL_KEY|GIT_SHALLOW_FILE|\
      GIT_REPLACE_REF_BASE|GIT_NO_REPLACE_OBJECTS|GIT_QUARANTINE_PATH|\
      GIT_INTERNAL_SUPER_PREFIX|GIT_PREFIX|GIT_EXEC_PATH)
        log_error "Erro: variavel de ambiente Git nao permitida: ${name}"
        return 1
        ;;
    esac
  done < <(env | sed -n 's/^\([A-Za-z_][A-Za-z0-9_]*\)=.*/\1/p')
  return 0
}

validate_remote_origin() {
  local repo="$1" remote="$2" expected="${3:-$CONTABASE_OFFICIAL_ORIGIN}"
  local raw_values raw_count raw normalized effective_values effective_count effective rewrite value

  case "$remote" in
    ""|*[!A-Za-z0-9._-]*)
      log_error "Erro: remote Git invalido: ${remote:-<vazio>}"
      return 1
      ;;
  esac

  raw_values="$(git -C "$repo" config --get-all "remote.${remote}.url" 2>/dev/null || true)"
  raw_count="$(state_count_lines "$raw_values")"
  [ "$raw_count" = "1" ] || {
    log_error "Erro: remote ${remote} ausente ou com URL ambigua."
    return 1
  }
  raw="$(printf '%s\n' "$raw_values" | sed -n '1p')"
  normalized="$(state_normalize_origin "$raw")" || {
    log_error "Erro: URL configurada nao pertence a origem oficial."
    return 1
  }
  [ "$normalized" = "$expected" ] || {
    log_error "Erro: URL configurada diverge da origem oficial."
    return 1
  }

  effective_values="$(git -C "$repo" remote get-url --all "$remote" 2>/dev/null || true)"
  effective_count="$(state_count_lines "$effective_values")"
  [ "$effective_count" = "1" ] || {
    log_error "Erro: URL efetiva do remote ${remote} ausente ou ambigua."
    return 1
  }
  effective="$(printf '%s\n' "$effective_values" | sed -n '1p')"
  normalized_effective="$(state_normalize_origin "$effective")" || {
    log_error "Erro: URL efetiva nao pertence a origem oficial."
    return 1
  }
  [ "$normalized_effective" = "$expected" ] || {
    log_error "Erro: URL efetiva diverge da origem oficial."
    return 1
  }

  while IFS= read -r rewrite; do
    [ -n "$rewrite" ] || continue
    value="${rewrite#* }"
    [ -n "$value" ] || continue
    case "$expected" in
      "$value"*)
        log_error "Erro: configuracao Git de reescrita de URL afeta a origem oficial."
        return 1
        ;;
    esac
  done <<< "$(git -C "$repo" config --get-regexp '^url\..*\.(insteadOf|pushInsteadOf)$' 2>/dev/null || true)"

  VALIDATED_ORIGIN="$normalized_effective"
  return 0
}

record_local_tags() {
  TAGS_BEFORE_FILE="$(mktemp "${TMPDIR:-/tmp}/contabase-tags-before.XXXXXX")" || return 1
  if ! git -C "$REPO_ROOT" for-each-ref --format='%(refname) %(objectname)' refs/tags \
    | LC_ALL=C sort > "$TAGS_BEFORE_FILE"; then
    log_error "Erro: nao foi possivel registrar as tags locais antes do fetch."
    return 1
  fi
}

assert_local_tags_unchanged() {
  local current
  [ -n "$TAGS_BEFORE_FILE" ] && [ -f "$TAGS_BEFORE_FILE" ] || {
    log_error "Erro: registro previo das tags locais ausente."
    return 1
  }
  current="$(mktemp "${TMPDIR:-/tmp}/contabase-tags-after.XXXXXX")" || return 1
  if ! git -C "$REPO_ROOT" for-each-ref --format='%(refname) %(objectname)' refs/tags \
    | LC_ALL=C sort > "$current"; then
    rm -f "$current"
    log_error "Erro: nao foi possivel reler as tags locais."
    return 1
  fi
  if ! cmp -s "$TAGS_BEFORE_FILE" "$current"; then
    rm -f "$current"
    log_error "Erro: refs/tags locais foram alteradas durante a obtencao de objetos Git."
    return 1
  fi
  rm -f "$current"
  return 0
}

finalize_marker_value() {
  local file="$1" key="$2"
  [ -f "$file" ] && [ ! -L "$file" ] || return 1
  awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$file"
}

write_finalize_marker() {
  local dir tmp
  [ -n "$TARGET_VERSION" ] && [ -n "$TARGET_OID" ] && [ -n "$CHECKOUT_KIND" ] && [ -n "$TARGET_REF" ] || {
    log_error "Erro: dados insuficientes para o marcador de finalizacao."
    return 1
  }
  [[ "$TARGET_OID" =~ ^[0-9a-f]{40}$ ]] || return 1
  dir="$(dirname "$FINALIZE_MARKER")"
  mkdir -p "$dir" || return 1
  tmp="$(mktemp "${dir}/.finalize.pending.XXXXXX")" || return 1
  (
    umask 077
    {
      printf 'schema=1\n'
      printf 'method=source\n'
      printf 'target_version=%s\n' "$TARGET_VERSION"
      printf 'target_kind=%s\n' "$CHECKOUT_KIND"
      printf 'target_ref=%s\n' "$TARGET_REF"
      printf 'target_commit=%s\n' "$TARGET_OID"
      printf 'previous_version=%s\n' "${PREVIOUS_VERSION:-desconhecida}"
      printf 'previous_commit=%s\n' "${CURRENT_HEAD_OID:-$TARGET_OID}"
      printf 'runtime_version=%s\n' "$TARGET_VERSION"
    } > "$tmp"
  ) || { rm -f "$tmp"; return 1; }
  chmod 0600 "$tmp" || { rm -f "$tmp"; return 1; }
  mv "$tmp" "$FINALIZE_MARKER" || { rm -f "$tmp"; return 1; }
}

validate_finalize_marker_structure() {
  local file="$FINALIZE_MARKER" size key value
  local keys="schema method target_version target_kind target_ref target_commit previous_version previous_commit runtime_version"

  [ -f "$file" ] && [ ! -L "$file" ] || {
    log_error "Erro: marcador de finalizacao ausente ou inseguro."
    return 1
  }
  size="$(wc -c < "$file" | tr -d '[:space:]')" || return 1
  [ "$size" -le 4096 ] || {
    log_error "Erro: marcador de finalizacao excessivamente grande."
    return 1
  }
  awk -F= -v allowed="$keys" '
    BEGIN { split(allowed, a, " "); for (i in a) allowed_keys[a[i]] = 1 }
    /^[[:space:]]*$/ { next }
    {
      if (index($0, "=") == 0 || !($1 in allowed_keys) || ($1 in seen)) exit 1
      seen[$1] = 1
      value = substr($0, index($0, "=") + 1)
      if (value ~ /[\r\n\t]/ || value == "") exit 1
    }
    END {
      for (i in allowed_keys) if (!(i in seen)) exit 1
    }
  ' "$file" || {
    log_error "Erro: marcador de finalizacao invalido ou ambiguo."
    return 1
  }

  [ "$(finalize_marker_value "$file" schema)" = "1" ] || return 1
  [ "$(finalize_marker_value "$file" method)" = "source" ] || return 1
  [[ "$(finalize_marker_value "$file" target_commit)" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "$(finalize_marker_value "$file" previous_commit)" =~ ^[0-9a-f]{40}$ ]] || return 1
  case "$(finalize_marker_value "$file" target_kind)" in
    tag|branch) ;;
    *) return 1 ;;
  esac

  for key in target_version target_ref previous_version; do
    value="$(finalize_marker_value "$file" "$key")"
    case "$value" in
      ""|*[!A-Za-z0-9._/-]*) log_error "Erro: campo inseguro no marcador: $key"; return 1 ;;
    esac
  done
  return 0
}

finalize_marker_matches_target() {
  local file="$FINALIZE_MARKER"
  [ -f "$file" ] && [ ! -L "$file" ] || return 1
  [ "$(finalize_marker_value "$file" target_version)" = "$TARGET_VERSION" ] || return 1
  [ "$(finalize_marker_value "$file" target_kind)" = "$CHECKOUT_KIND" ] || return 1
  [ "$(finalize_marker_value "$file" target_ref)" = "$TARGET_REF" ] || return 1
  [ "$(finalize_marker_value "$file" target_commit)" = "$TARGET_OID" ] || return 1
  [ "$(finalize_marker_value "$file" runtime_version)" = "${CONFIRMED_RUNTIME_VERSION:-}" ] || return 1
  return 0
}

validate_finalize_marker_for_target() {
  validate_finalize_marker_structure || return 1
  finalize_marker_matches_target || {
    log_error "Erro: marcador de finalizacao diverge do alvo confirmado."
    return 1
  }
  return 0
}

validate_pending_marker_for_target() {
  local marker_version marker_kind decision_rc=0 runtime="${CONFIRMED_RUNTIME_VERSION:-}"
  local journal_present=false marker_present=false marker_matches=false
  local journal_previous_checkout="" journal_previous_marker=""

  if [ -e "$TRANSITION_JOURNAL" ] || [ -L "$TRANSITION_JOURNAL" ]; then
    [ -f "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || {
      log_error "Erro: registro de transicao inseguro."
      return 1
    }
    journal_present=true
    validate_transition_journal_structure || return 1
    if ! transition_journal_matches_target; then
      validate_transition_journal_supersession || return 1
      log_warn "Intencao anterior sera substituida somente apos o healthcheck do novo alvo."
      return 0
    fi
  fi

  if [ -e "$FINALIZE_MARKER" ] || [ -L "$FINALIZE_MARKER" ]; then
    [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ] || {
      log_error "Erro: marcador de finalizacao inseguro."
      return 1
    }
    marker_present=true
    validate_finalize_marker_structure || return 1
    if finalize_marker_matches_target; then
      marker_matches=true
    fi
  fi

  if [ "$marker_matches" = true ]; then
    return 0
  fi

  if [ "$runtime" = "$TARGET_VERSION" ] \
    && [ "$RECOVERY_FINALIZE_ONLY" != true ] \
    && [ "${PERSISTED_VERSION:-}" = "$TARGET_VERSION" ] \
    && [ "${CHECKOUT_VERSION_BEFORE:-}" = "$TARGET_VERSION" ] \
    && [ "$journal_present" = false ]; then
    return 0
  fi

  if [ "$runtime" = "$TARGET_VERSION" ]; then
    if [ "$journal_present" = true ]; then
      validate_transition_journal_previous || return 1
      TRANSITION_AUTHORIZED=true
      log_warn "Runtime ${TARGET_VERSION} confirmado com intencao de transicao valida."
      return 0
    fi
    log_error "Erro: runtime ${TARGET_VERSION} saudavel sem intencao de transicao persistida."
    log_error "Nao promova o checkout apenas pela versao do runtime."
    log_error "Preserve marcador, registro, estado e dados; use recuperacao administrativa."
    return 1
  fi

  if [ "$journal_present" = true ]; then
    validate_transition_journal_previous || return 1
    journal_previous_checkout="$(transition_journal_value "$TRANSITION_JOURNAL" previous_checkout_version)"
    journal_previous_marker="$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_version)"
    case "$runtime" in
      "")
        return 0
        ;;
      "$journal_previous_checkout")
        return 0
        ;;
    esac
    if [ "$journal_previous_marker" != "none" ] && [ "$runtime" = "$journal_previous_marker" ]; then
      return 0
    fi
    log_error "Erro: runtime ${runtime:-<ausente>} nao corresponde a transicao registrada."
    return 1
  fi

  if [ "$marker_present" = true ]; then
    marker_version="$(finalize_marker_value "$FINALIZE_MARKER" target_version)"
    marker_kind="$(finalize_marker_value "$FINALIZE_MARKER" target_kind)"
    [ "$marker_kind" = "$CHECKOUT_KIND" ] || {
      log_error "Erro: marcador pendente pertence a outro tipo de checkout."
      return 1
    }
    if [ "$marker_version" = "$TARGET_VERSION" ]; then
      log_error "Erro: runtime nao confirma o alvo pendente ${marker_version}."
      return 1
    fi
    version_decision "$marker_version" "$TARGET_VERSION" || decision_rc=$?
    [ "$decision_rc" = "0" ] || {
      log_error "Erro: alvo ${TARGET_VERSION} nao supera a finalizacao pendente ${marker_version}."
      return 1
    }
    [ "$runtime" = "$marker_version" ] || {
      log_error "Erro: runtime nao confirma a versao pendente ${marker_version}; estado ambiguo."
      return 1
    }
    log_warn "Finalizacao pendente de ${marker_version} preservada; alvo ${TARGET_VERSION} a supera."
    return 0
  fi

  return 0
}

remove_finalize_marker() {
  [ -e "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ] || return 0
  rm -f "$FINALIZE_MARKER"
}

transition_journal_value() {
  local file="$1" key="$2"
  [ -f "$file" ] && [ ! -L "$file" ] || return 1
  awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$file"
}

current_install_channel() {
  local persisted
  persisted="$(state_value "$STATE_FILE" CONTABASE_CHANNEL)"
  if [ -n "$persisted" ]; then
    printf '%s' "$persisted"
    return 0
  fi
  infer_install_channel
}

write_transition_journal() {
  local dir tmp channel marker_hash marker_version marker_commit state_version state_commit

  [ -n "$TARGET_VERSION" ] && [ -n "$TARGET_OID" ] && [ -n "$CHECKOUT_KIND" ] && [ -n "$TARGET_REF" ] \
    && [ -n "$INSTALL_ORIGIN" ] && [ -n "$CURRENT_HEAD_OID" ] || {
    log_error "Erro: dados insuficientes para registrar a intencao da transicao."
    return 1
  }
  [[ "$TARGET_OID" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "$CURRENT_HEAD_OID" =~ ^[0-9a-f]{40}$ ]] || return 1

  channel="$(current_install_channel)" || return 1
  case "$channel" in
    beta|stable|pinned) ;;
    *) log_error "Erro: canal invalido para a intencao da transicao: ${channel:-<vazio>}"; return 1 ;;
  esac

  if [ -f "$FINALIZE_MARKER" ]; then
    validate_finalize_marker_structure || return 1
    marker_hash="$(git -C "$REPO_ROOT" hash-object --no-filters "$FINALIZE_MARKER")" || {
      log_error "Erro: nao foi possivel identificar o marcador pendente."
      return 1
    }
    [[ "$marker_hash" =~ ^[0-9a-f]{40}$ ]] || {
      log_error "Erro: identidade do marcador pendente invalida."
      return 1
    }
    marker_version="$(finalize_marker_value "$FINALIZE_MARKER" target_version)"
    marker_commit="$(finalize_marker_value "$FINALIZE_MARKER" target_commit)"
  else
    marker_hash="none"
    marker_version="none"
    marker_commit="none"
  fi

  if [ -f "$STATE_FILE" ]; then
    state_version="$(state_value "$STATE_FILE" CONTABASE_INSTALLED_VERSION)"
    state_commit="$(state_value "$STATE_FILE" CONTABASE_INSTALL_COMMIT)"
  else
    state_version=""
    state_commit=""
  fi
  [ -n "$state_version" ] || state_version="none"
  [ -n "$state_commit" ] || state_commit="none"

  dir="$(dirname "$TRANSITION_JOURNAL")"
  mkdir -p "$dir" || return 1
  tmp="$(mktemp "${dir}/.transition.pending.XXXXXX")" || return 1
  (
    umask 077
    {
      printf 'schema=1\n'
      printf 'method=source\n'
      printf 'target_version=%s\n' "$TARGET_VERSION"
      printf 'target_kind=%s\n' "$CHECKOUT_KIND"
      printf 'target_ref=%s\n' "$TARGET_REF"
      printf 'target_commit=%s\n' "$TARGET_OID"
      printf 'target_origin=%s\n' "$INSTALL_ORIGIN"
      printf 'channel=%s\n' "$channel"
      printf 'previous_checkout_version=%s\n' "${CHECKOUT_VERSION_BEFORE:-none}"
      printf 'previous_commit=%s\n' "$CURRENT_HEAD_OID"
      printf 'previous_state_version=%s\n' "$state_version"
      printf 'previous_state_commit=%s\n' "$state_commit"
      printf 'previous_marker_hash=%s\n' "$marker_hash"
      printf 'previous_marker_version=%s\n' "$marker_version"
      printf 'previous_marker_commit=%s\n' "$marker_commit"
    } > "$tmp"
  ) || { rm -f "$tmp"; return 1; }
  chmod 0600 "$tmp" || { rm -f "$tmp"; return 1; }
  mv "$tmp" "$TRANSITION_JOURNAL" || { rm -f "$tmp"; return 1; }
}

validate_transition_journal_structure() {
  local file="$TRANSITION_JOURNAL" size key value
  local keys="schema method target_version target_kind target_ref target_commit target_origin channel previous_checkout_version previous_commit previous_state_version previous_state_commit previous_marker_hash previous_marker_version previous_marker_commit"

  [ -f "$file" ] && [ ! -L "$file" ] || {
    log_error "Erro: registro de transicao ausente ou inseguro."
    return 1
  }
  size="$(wc -c < "$file" | tr -d '[:space:]')" || return 1
  [ "$size" -le 4096 ] || {
    log_error "Erro: registro de transicao excessivamente grande."
    return 1
  }
  awk -F= -v allowed="$keys" '
    BEGIN { split(allowed, a, " "); for (i in a) allowed_keys[a[i]] = 1 }
    /^[[:space:]]*$/ { next }
    {
      if (index($0, "=") == 0 || !($1 in allowed_keys) || ($1 in seen)) exit 1
      seen[$1] = 1
      value = substr($0, index($0, "=") + 1)
      if (value ~ /[\r\n\t]/ || value == "") exit 1
    }
    END {
      for (i in allowed_keys) if (!(i in seen)) exit 1
    }
  ' "$file" || {
    log_error "Erro: registro de transicao invalido ou ambiguo."
    return 1
  }

  [ "$(transition_journal_value "$file" schema)" = "1" ] || return 1
  [ "$(transition_journal_value "$file" method)" = "source" ] || return 1
  canonicalize_version "$(transition_journal_value "$file" target_version)" >/dev/null 2>&1 || return 1
  case "$(transition_journal_value "$file" target_kind)" in
    tag|branch) ;;
    *) return 1 ;;
  esac
  [[ "$(transition_journal_value "$file" target_commit)" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "$(transition_journal_value "$file" previous_commit)" =~ ^[0-9a-f]{40}$ ]] || return 1
  local normalized_origin
  normalized_origin="$(state_normalize_origin "$(transition_journal_value "$file" target_origin)")" || return 1
  [ "$normalized_origin" = "$CONTABASE_OFFICIAL_ORIGIN" ] || return 1
  case "$(transition_journal_value "$file" channel)" in
    beta|stable|pinned) ;;
    *) return 1 ;;
  esac

  for key in target_version target_ref previous_checkout_version previous_state_version previous_marker_version; do
    value="$(transition_journal_value "$file" "$key")"
    case "$value" in
      ""|*[!A-Za-z0-9._/-]*) log_error "Erro: campo inseguro no registro de transicao: $key"; return 1 ;;
    esac
  done
  if [ "$(transition_journal_value "$file" target_kind)" = "tag" ]; then
    [ "$(transition_journal_value "$file" target_ref)" = "$(transition_journal_value "$file" target_version)" ] || return 1
  fi

  for key in previous_state_commit previous_marker_hash previous_marker_commit; do
    value="$(transition_journal_value "$file" "$key")"
    if [ "$value" != "none" ] && [[ ! "$value" =~ ^[0-9a-f]{40}$ ]]; then
      log_error "Erro: identidade invalida no registro de transicao: $key"
      return 1
    fi
  done
  if [ "$(transition_journal_value "$file" previous_marker_hash)" = "none" ]; then
    [ "$(transition_journal_value "$file" previous_marker_version)" = "none" ] \
      && [ "$(transition_journal_value "$file" previous_marker_commit)" = "none" ] || return 1
  else
    [ "$(transition_journal_value "$file" previous_marker_version)" != "none" ] \
      && [ "$(transition_journal_value "$file" previous_marker_commit)" != "none" ] || return 1
  fi
  if [ "$(transition_journal_value "$file" previous_state_version)" = "none" ]; then
    [ "$(transition_journal_value "$file" previous_state_commit)" = "none" ] || return 1
  fi
  return 0
}

transition_journal_matches_target() {
  [ -f "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || return 1
  [ "$(transition_journal_value "$TRANSITION_JOURNAL" target_version)" = "$TARGET_VERSION" ] || return 1
  [ "$(transition_journal_value "$TRANSITION_JOURNAL" target_kind)" = "$CHECKOUT_KIND" ] || return 1
  [ "$(transition_journal_value "$TRANSITION_JOURNAL" target_ref)" = "$TARGET_REF" ] || return 1
  [ "$(transition_journal_value "$TRANSITION_JOURNAL" target_commit)" = "$TARGET_OID" ] || return 1
  [ "$(transition_journal_value "$TRANSITION_JOURNAL" target_origin)" = "$EFFECTIVE_ORIGIN" ] || return 1
  [ "$(transition_journal_value "$TRANSITION_JOURNAL" channel)" = "$(current_install_channel)" ] || return 1
  return 0
}

validate_transition_journal_supersession() {
  local journal_version journal_kind journal_ref journal_commit decision_rc=0
  local marker_version marker_commit marker_hash previous_hash

  [ -f "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || return 1
  [ "$(transition_journal_value "$TRANSITION_JOURNAL" target_origin)" = "$EFFECTIVE_ORIGIN" ] || {
    log_error "Erro: intencao anterior diverge da origem oficial."
    return 1
  }
  [ "$(transition_journal_value "$TRANSITION_JOURNAL" channel)" = "$(current_install_channel)" ] || {
    log_error "Erro: intencao anterior diverge do canal persistido."
    return 1
  }
  journal_kind="$(transition_journal_value "$TRANSITION_JOURNAL" target_kind)"
  [ "$journal_kind" = "$CHECKOUT_KIND" ] || {
    log_error "Erro: intencao anterior pertence a outro tipo de checkout."
    return 1
  }
  journal_version="$(transition_journal_value "$TRANSITION_JOURNAL" target_version)"
  journal_ref="$(transition_journal_value "$TRANSITION_JOURNAL" target_ref)"
  journal_commit="$(transition_journal_value "$TRANSITION_JOURNAL" target_commit)"
  version_decision "$journal_version" "$TARGET_VERSION" || decision_rc=$?
  [ "$decision_rc" = "0" ] || {
    log_error "Erro: alvo ${TARGET_VERSION} nao supera a intencao anterior ${journal_version}."
    return 1
  }
  [ "${CONFIRMED_RUNTIME_VERSION:-}" = "$journal_version" ] || {
    log_error "Erro: runtime nao confirma a intencao anterior ${journal_version}; supersessao recusada."
    return 1
  }

  if [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ]; then
    validate_finalize_marker_structure || return 1
    marker_version="$(finalize_marker_value "$FINALIZE_MARKER" target_version)"
    marker_commit="$(finalize_marker_value "$FINALIZE_MARKER" target_commit)"
    if [ "$marker_version" = "$journal_version" ] \
      && [ "$marker_commit" = "$journal_commit" ] \
      && [ "$(finalize_marker_value "$FINALIZE_MARKER" target_kind)" = "$journal_kind" ] \
      && [ "$(finalize_marker_value "$FINALIZE_MARKER" target_ref)" = "$journal_ref" ]; then
      return 0
    fi
    previous_hash="$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_hash)"
    if [ "$previous_hash" != "none" ]; then
      marker_hash="$(git -C "$REPO_ROOT" hash-object --no-filters "$FINALIZE_MARKER")" || return 1
      [ "$marker_hash" = "$previous_hash" ] \
        && [ "$marker_version" = "$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_version)" ] \
        && [ "$marker_commit" = "$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_commit)" ] \
        && return 0
    fi
    log_error "Erro: marcador pendente nao corresponde a intencao anterior."
    return 1
  elif [ -e "$FINALIZE_MARKER" ] || [ -L "$FINALIZE_MARKER" ]; then
    log_error "Erro: marcador de finalizacao inseguro."
    return 1
  fi

  [ "$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_hash)" = "none" ] || {
    log_error "Erro: marcador esperado pela intencao anterior esta ausente."
    return 1
  }
  return 0
}

validate_transition_journal_previous() {
  local previous_checkout previous_commit previous_state_version previous_state_commit
  local previous_marker_hash previous_marker_version previous_marker_commit
  local current_state_version current_state_commit current_marker_hash

  [ -f "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || return 1
  previous_checkout="$(transition_journal_value "$TRANSITION_JOURNAL" previous_checkout_version)"
  previous_commit="$(transition_journal_value "$TRANSITION_JOURNAL" previous_commit)"
  previous_state_version="$(transition_journal_value "$TRANSITION_JOURNAL" previous_state_version)"
  previous_state_commit="$(transition_journal_value "$TRANSITION_JOURNAL" previous_state_commit)"
  previous_marker_hash="$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_hash)"
  previous_marker_version="$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_version)"
  previous_marker_commit="$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_commit)"

  [ "${CHECKOUT_VERSION_BEFORE:-none}" = "$previous_checkout" ] || {
    log_error "Erro: versao anterior do checkout diverge da intencao registrada."
    return 1
  }
  [ "${CURRENT_HEAD_OID:-}" = "$previous_commit" ] || {
    log_error "Erro: commit anterior do checkout diverge da intencao registrada."
    return 1
  }

  if [ "$previous_state_version" = "none" ]; then
    [ ! -e "$STATE_FILE" ] || {
      log_error "Erro: estado de instalacao surgiu apos a intencao registrada."
      return 1
    }
  else
    [ -f "$STATE_FILE" ] || {
      log_error "Erro: estado de instalacao ausente para a intencao registrada."
      return 1
    }
    current_state_version="$(state_value "$STATE_FILE" CONTABASE_INSTALLED_VERSION)"
    current_state_commit="$(state_value "$STATE_FILE" CONTABASE_INSTALL_COMMIT)"
    [ -n "$current_state_commit" ] || current_state_commit="none"
    [ "$current_state_version" = "$previous_state_version" ] \
      && [ "$current_state_commit" = "$previous_state_commit" ] || {
      log_error "Erro: estado de instalacao diverge da intencao registrada."
      return 1
    }
  fi

  if [ "$previous_marker_hash" = "none" ]; then
    [ ! -e "$FINALIZE_MARKER" ] || {
      log_error "Erro: marcador pendente surgiu apos a intencao registrada."
      return 1
    }
  else
    [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ] || {
      log_error "Erro: marcador pendente ausente para a intencao registrada."
      return 1
    }
    validate_finalize_marker_structure || return 1
    current_marker_hash="$(git -C "$REPO_ROOT" hash-object --no-filters "$FINALIZE_MARKER")" || return 1
    [ "$current_marker_hash" = "$previous_marker_hash" ] || {
      log_error "Erro: marcador pendente foi alterado apos a intencao registrada."
      return 1
    }
    [ "$(finalize_marker_value "$FINALIZE_MARKER" target_version)" = "$previous_marker_version" ] \
      && [ "$(finalize_marker_value "$FINALIZE_MARKER" target_commit)" = "$previous_marker_commit" ] || {
      log_error "Erro: identidade do marcador pendente diverge da intencao registrada."
      return 1
    }
  fi
  return 0
}

remove_transition_journal() {
  [ -e "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || return 0
  rm -f "$TRANSITION_JOURNAL"
}

validate_target_channel() {
  local persisted_channel effective_channel
  persisted_channel="$(state_value "$STATE_FILE" CONTABASE_CHANNEL)"
  effective_channel="${persisted_channel:-$INSTALL_CHANNEL}"
  case "$effective_channel" in
    "")
      return 0
      ;;
    pinned)
      return 0
      ;;
    beta)
      case "$TARGET_VERSION" in
        *-beta.*) return 0 ;;
        *) log_error "Erro: alvo ${TARGET_VERSION} nao pertence ao canal beta persistido."; return 1 ;;
      esac
      ;;
    stable)
      case "$TARGET_VERSION" in
        *-*) log_error "Erro: alvo ${TARGET_VERSION} nao pertence ao canal stable persistido."; return 1 ;;
        *) return 0 ;;
      esac
      ;;
    *)
      log_error "Erro: canal persistido invalido: $effective_channel"
      return 1
      ;;
  esac
}

is_shallow_repo() {
  local shallow
  if shallow="$(git rev-parse --is-shallow-repository 2>/dev/null)"; then
    [ "$shallow" = "true" ]
    return
  fi
  shallow="$(git rev-parse --git-path shallow 2>/dev/null || true)"
  [ -n "$shallow" ] && [ -s "$shallow" ]
}

assert_checkout_unchanged() {
  local head_after="${1:-}"
  [ -z "$head_after" ] || [ "$head_after" = "$HEAD_BEFORE_FETCH" ] || {
    log_error "Erro: HEAD mudou durante a obtencao de objetos Git."
    return 1
  }
  [ "$(git rev-parse --verify 'HEAD^{commit}' 2>/dev/null)" = "$HEAD_BEFORE_FETCH" ] || {
    log_error "Erro: HEAD diverge do valor observado antes do preflight Git."
    return 1
  }
  [ -z "$(git status --porcelain 2>/dev/null)" ] || {
    log_error "Erro: worktree mudou durante a obtencao de objetos Git."
    return 1
  }
  [ -z "$STATUS_BEFORE_FETCH" ] || {
    log_error "Erro: worktree nao estava limpo antes da obtencao de objetos Git."
    return 1
  }
}

# validate_complete_state_provenance_local <state_file> <repo_root>
#   Valida proveniencia persistida completa de forma autoritativa para o
#   checkout instalado. Retorna 0 quando REF_KIND/REF/COMMIT/origem, HEAD,
#   VERSION e branch/tag persistida sao coerentes; 2 quando nao ha proveniencia;
#   1 quando ha proveniencia parcial ou divergente.
#   Nao relaxa HEAD, commit, origem, versao ou ref. As unicas diferencas em
#   relacao ao detector legado sao: branch sem upstream (fallback determinístico
#   para a branch de mesmo nome na origem oficial) e tag com aliases no HEAD.
validate_complete_state_provenance_local() {
  local file="$1" repo="$2"
  local kind ref commit origin version repo_state repo_canonical
  local head branch checkout_version normalized_origin tag_oid

  state_validate_file "$file" || return 1
  kind="$(state_value "$file" CONTABASE_INSTALL_REF_KIND)"
  ref="$(state_value "$file" CONTABASE_INSTALL_REF)"
  commit="$(state_value "$file" CONTABASE_INSTALL_COMMIT)"
  origin="$(state_value "$file" CONTABASE_INSTALL_ORIGIN)"

  if [ -z "$kind" ] && [ -z "$ref" ] && [ -z "$commit" ] && [ -z "$origin" ]; then
    return 2
  fi
  [ -n "$kind" ] && [ -n "$ref" ] && [ -n "$commit" ] && [ -n "$origin" ] || return 1
  case "$kind" in
    tag|branch) ;;
    *) return 1 ;;
  esac
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || return 1
  normalized_origin="$(state_normalize_origin "$origin")" || return 1
  [ "$normalized_origin" = "$EFFECTIVE_ORIGIN" ] || return 1

  repo_state="$(state_value "$file" CONTABASE_REPO_PATH)"
  [ -n "$repo_state" ] && [ -d "$repo_state" ] || return 1
  repo_canonical="$(cd "$repo_state" 2>/dev/null && pwd -P)" || return 1
  [ "$repo_canonical" = "$repo" ] || return 1

  version="$(state_value "$file" CONTABASE_INSTALLED_VERSION)"
  [ -n "$version" ] || return 1
  checkout_version="$(tr -d '[:space:]' < "$repo/VERSION" 2>/dev/null || true)"
  [ -n "$checkout_version" ] && [ "$checkout_version" = "$version" ] || return 1

  head="$(git -C "$repo" rev-parse --verify 'HEAD^{commit}' 2>/dev/null)" || return 1
  [ "$head" = "$commit" ] || return 1
  branch="$(git -C "$repo" branch --show-current 2>/dev/null || true)"

  case "$kind" in
    tag)
      [ -z "$branch" ] || return 1
      [ "$ref" = "$version" ] || return 1
      tag_oid="$(git -C "$repo" rev-parse --verify "refs/tags/${ref}^{commit}" 2>/dev/null || true)"
      [ "$tag_oid" = "$commit" ] || return 1
      ;;
    branch)
      [ "$branch" = "$ref" ] || return 1
      ;;
  esac
  return 0
}

detect_checkout_provenance() {
  local checkout_version state_rc=2 state_kind="" state_ref="" state_commit="" state_origin="" state_repo=""
  local state_canonical marker_pending=false marker_kind="" marker_ref="" marker_commit="" marker_previous_commit=""
  local local_rc=0 state_err=""

  CURRENT_BRANCH="$(git branch --show-current 2>/dev/null || true)"
  CURRENT_COMMIT="$(git log --oneline -1 2>/dev/null || true)"
  CURRENT_HEAD_OID="$(git rev-parse --verify 'HEAD^{commit}' 2>/dev/null)" || {
    log_error "Erro: nao foi possivel resolver HEAD."
    return 1
  }
  [ -f VERSION ] && [ ! -L VERSION ] && [ -r VERSION ] || {
    log_error "Erro: VERSION ausente ou inseguro no checkout."
    return 1
  }
  checkout_version="$(tr -d '[:space:]' < VERSION)"
  [ -n "$checkout_version" ] || {
    log_error "Erro: VERSION vazio no checkout."
    return 1
  }

  reject_unsafe_git_environment || return 1
  validate_remote_origin "$REPO_ROOT" origin || return 1
  EFFECTIVE_ORIGIN="$VALIDATED_ORIGIN"

  if [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ]; then
    marker_pending=true
    validate_finalize_marker_structure || return 1
    marker_kind="$(finalize_marker_value "$FINALIZE_MARKER" target_kind)"
    marker_ref="$(finalize_marker_value "$FINALIZE_MARKER" target_ref)"
    marker_commit="$(finalize_marker_value "$FINALIZE_MARKER" target_commit)"
    marker_previous_commit="$(finalize_marker_value "$FINALIZE_MARKER" previous_commit)"
    log_warn "Finalizacao pendente detectada; validando estado/checkout de forma recuperavel."
  elif [ -e "$FINALIZE_MARKER" ] || [ -L "$FINALIZE_MARKER" ]; then
    log_error "Erro: marcador de finalizacao inseguro."
    return 1
  fi

  if [ -f "$STATE_FILE" ]; then
    state_validate_file "$STATE_FILE" || return 1
    state_repo="$(state_value "$STATE_FILE" CONTABASE_REPO_PATH)"
    [ -n "$state_repo" ] && [ -d "$state_repo" ] || {
      log_error "Erro: CONTABASE_REPO_PATH ausente ou inacessivel."
      return 1
    }
    state_canonical="$(cd "$state_repo" 2>/dev/null && pwd -P)" || {
      log_error "Erro: checkout do estado inacessivel: $state_repo"
      return 1
    }
    [ "$state_canonical" = "$REPO_ROOT" ] || {
      log_error "Erro: CONTABASE_REPO_PATH diverge do checkout em uso."
      return 1
    }
    state_kind="$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF_KIND)"
    state_ref="$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF)"
    state_commit="$(state_value "$STATE_FILE" CONTABASE_INSTALL_COMMIT)"
    state_origin="$(state_value "$STATE_FILE" CONTABASE_INSTALL_ORIGIN)"
    if [ "$marker_pending" = true ]; then
      if [ -n "$state_kind" ] || [ -n "$state_ref" ] || [ -n "$state_commit" ] || [ -n "$state_origin" ]; then
        [ -n "$state_kind" ] && [ -n "$state_ref" ] && [ -n "$state_commit" ] && [ -n "$state_origin" ] \
          && { [ "$state_kind" = "tag" ] || [ "$state_kind" = "branch" ]; } \
          && [[ "$state_commit" =~ ^[0-9a-f]{40}$ ]] || {
          log_error "Erro: proveniencia persistida parcial durante finalizacao pendente."
          return 1
        }
        state_origin="$(state_normalize_origin "$state_origin")" || {
          log_error "Erro: origem persistida invalida durante finalizacao pendente."
          return 1
        }
        [ "$state_origin" = "$EFFECTIVE_ORIGIN" ] || {
          log_error "Erro: origem persistida diverge da origem efetiva durante finalizacao pendente."
          return 1
        }
        state_rc=0
      else
        state_rc=2
      fi
    else
      state_rc=0
      state_err="$(trap - ERR; set +e; provenance_validate_state "$STATE_FILE" "$REPO_ROOT" 2>&1)" || state_rc=$?
      if [ "$state_rc" -ne 0 ]; then
        # A proveniencia completa e autoritativa. O detector legado exige
        # upstream em branches e tag unica no HEAD; essas duas heuristicas nao
        # provam divergencia de identidade e podem ser relaxadas localmente,
        # sem aceitar HEAD, commit, origem, versao ou ref diferentes.
        local_rc=0
        validate_complete_state_provenance_local "$STATE_FILE" "$REPO_ROOT" || local_rc=$?
        if [ "$local_rc" = "0" ]; then
          state_rc=0
          log_warn "Proveniencia persistida completa validada localmente; heuristicas legadas de upstream/tag alias relaxadas."
        elif [ -n "$state_err" ]; then
          printf '%s\n' "$state_err" >&2
        fi
        if [ "$local_rc" != "0" ] && [ "$state_kind" = "tag" ] && [ -n "$CURRENT_BRANCH" ]; then
          log_error "Erro: proveniencia persistida diverge do checkout atual."
        fi
      fi
      case "$state_rc" in
        0|2) ;;
        *) log_error "Erro: estado de instalacao invalido ou divergente."; return 1 ;;
      esac
    fi
  fi

  if [ "$marker_pending" = true ]; then
    if [ "$marker_kind" = "tag" ] && [ ! -f "$STATE_FILE" ]; then
      log_error "Erro: finalizacao pendente de tag sem estado de instalacao."
      return 1
    fi
    CHECKOUT_KIND="$marker_kind"
    INSTALL_REF="$marker_ref"
    INSTALL_COMMIT="$marker_commit"
    if [ "$state_rc" = "0" ]; then
      STATE_PROVENANCE_COMPLETE=true
      [ "$state_kind" = "$marker_kind" ] || {
        log_error "Erro: tipo persistido diverge do marcador de finalizacao."
        return 1
      }
      [ "$state_origin" = "$EFFECTIVE_ORIGIN" ] || {
        log_error "Erro: origem persistida diverge da origem efetiva."
        return 1
      }
      INSTALL_ORIGIN="$EFFECTIVE_ORIGIN"
      if [ "$state_kind" = "branch" ]; then
        [ "$state_ref" = "$marker_ref" ] && [ "$CURRENT_BRANCH" = "$marker_ref" ] || {
          log_error "Erro: branch persistida diverge do marcador de finalizacao."
          return 1
        }
      fi
    else
      INSTALL_ORIGIN="$EFFECTIVE_ORIGIN"
    fi
    if [ "$CHECKOUT_KIND" = "tag" ]; then
      [ -z "$CURRENT_BRANCH" ] || {
        log_error "Erro: marcador de tag em checkout de branch."
        return 1
      }
      [ "$CURRENT_HEAD_OID" = "$INSTALL_COMMIT" ] || [ "$CURRENT_HEAD_OID" = "$state_commit" ] \
        || [ "$CURRENT_HEAD_OID" = "$marker_previous_commit" ] || {
        log_error "Erro: HEAD nao corresponde ao estado nem ao alvo do marcador."
        return 1
      }
    else
      [ "$CURRENT_BRANCH" = "$INSTALL_REF" ] || {
        log_error "Erro: branch atual diverge do marcador de finalizacao."
        return 1
      }
      [ "$CURRENT_HEAD_OID" = "$INSTALL_COMMIT" ] || [ "$CURRENT_HEAD_OID" = "$state_commit" ] \
        || [ "$CURRENT_HEAD_OID" = "$marker_previous_commit" ] || {
        log_error "Erro: HEAD nao corresponde ao estado nem ao alvo do marcador."
        return 1
      }
    fi
    TARGET_REF="$INSTALL_REF"
    log_warn "Finalizacao pendente aceita para kind=${CHECKOUT_KIND} ref=${INSTALL_REF}."
    return 0
  fi

  if [ "$state_rc" = "0" ]; then
    STATE_PROVENANCE_COMPLETE=true
    # Proveniencia persistida completa: REF_KIND/REF/COMMIT/origem/HEAD/
    # VERSION ja foram validados. Nao repete as heuristicas legadas de
    # upstream unico ou tag unica no HEAD.
    CHECKOUT_KIND="$state_kind"
    INSTALL_REF="$state_ref"
    INSTALL_COMMIT="$state_commit"
    INSTALL_ORIGIN="$state_origin"
    [ "$CURRENT_HEAD_OID" = "$INSTALL_COMMIT" ] || {
      log_error "Erro: HEAD diverge do commit persistido."
      return 1
    }
    [ "$INSTALL_ORIGIN" = "$EFFECTIVE_ORIGIN" ] || {
      log_error "Erro: origem persistida diverge da origem efetiva."
      return 1
    }
    if [ "$CHECKOUT_KIND" = "tag" ]; then
      [ -z "$CURRENT_BRANCH" ] || {
        log_error "Erro: proveniencia persistida diverge do checkout atual (estado de tag em branch)."
        return 1
      }
    else
      [ "$CURRENT_BRANCH" = "$INSTALL_REF" ] || {
        log_error "Erro: proveniencia persistida diverge do checkout atual (branch divergente)."
        return 1
      }
    fi
    TARGET_REF="$INSTALL_REF"
    log_ok "Proveniencia persistida completa aceita: kind=${CHECKOUT_KIND} ref=${INSTALL_REF} commit=${INSTALL_COMMIT} origin=${INSTALL_ORIGIN}."
    return 0
  fi

  provenance_detect "$REPO_ROOT" "$checkout_version" || return 1
  [ "$PROVENANCE_ORIGIN" = "$EFFECTIVE_ORIGIN" ] || {
    log_error "Erro: origem detectada diverge da origem efetiva do checkout."
    return 1
  }
  CHECKOUT_KIND="$PROVENANCE_KIND"
  INSTALL_REF="$PROVENANCE_REF"
  INSTALL_COMMIT="$PROVENANCE_COMMIT"
  INSTALL_ORIGIN="$EFFECTIVE_ORIGIN"
  log_warn "Estado legado sem proveniencia; usando deteccao local nao ambigua."

  if [ "$CHECKOUT_KIND" = "tag" ] && [ -n "$CURRENT_BRANCH" ]; then
    log_error "Erro: tag exige checkout destacado."
    return 1
  fi
  if [ "$CHECKOUT_KIND" = "tag" ] && [ ! -f "$STATE_FILE" ]; then
    log_error "Erro: update por tag exige install-state.env legivel."
    return 1
  fi
  TARGET_REF="$INSTALL_REF"
  log_ok "Proveniencia detectada: kind=${CHECKOUT_KIND} ref=${INSTALL_REF} commit=${INSTALL_COMMIT} origin=${INSTALL_ORIGIN}."
}

resolve_target_tag() {
  TARGET_TAG="${CONTABASE_VERSION:-}"
  [ -n "$TARGET_TAG" ] || {
    log_error "Erro: tag mode exige CONTABASE_VERSION explicito."
    return 1
  }
  canonicalize_version "$TARGET_TAG" >/dev/null 2>&1 || {
    log_error "Erro: versao alvo invalida ou ambigua: ${TARGET_TAG}"
    return 1
  }
  TARGET_REF="$TARGET_TAG"
}

fetch_target_tag() {
  local local_tag_oid
  HEAD_BEFORE_FETCH="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  STATUS_BEFORE_FETCH="$(git status --porcelain 2>/dev/null || true)"
  [ -z "$STATUS_BEFORE_FETCH" ] || {
    log_error "Erro: worktree nao esta limpo antes do fetch da tag."
    return 1
  }

  if is_shallow_repo; then
    log_warn "Shallow clone detectado; expandindo historia antes da validacao de ancestralidade."
    if ! git fetch --unshallow --no-tags "$CONTABASE_OFFICIAL_ORIGIN" \
      "+refs/heads/*:${UNSHALLOW_REF_PREFIX}/heads/*" \
      "+refs/tags/*:${UNSHALLOW_REF_PREFIX}/tags/*"; then
      log_error "Erro: falha ao expandir clone shallow."
      return 1
    fi
    if is_shallow_repo; then
      log_error "Erro: clone permaneceu shallow apos a expansao."
      return 1
    fi
    assert_local_tags_unchanged || return 1
    assert_checkout_unchanged "$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  fi

  if ! git fetch --no-tags "$CONTABASE_OFFICIAL_ORIGIN" "+refs/tags/${TARGET_TAG}:${TEMP_TARGET_REF}"; then
    log_error "Erro: tag alvo ausente ou inacessivel na origem oficial: ${TARGET_TAG}"
    return 1
  fi
  assert_local_tags_unchanged || return 1
  assert_checkout_unchanged "$(git rev-parse --verify 'HEAD^{commit}')" || return 1

  [ "$(git cat-file -t "$TEMP_TARGET_REF" 2>/dev/null)" = "tag" ] \
    || [ "$(git cat-file -t "$TEMP_TARGET_REF" 2>/dev/null)" = "commit" ] || {
    log_error "Erro: ref alvo nao e tag nem commit."
    return 1
  }
  TARGET_OBJECT_TYPE="$(git cat-file -t "$TEMP_TARGET_REF")"
  TARGET_OID="$(git rev-parse --verify "${TEMP_TARGET_REF}^{commit}")" || {
    log_error "Erro: nao foi possivel resolver a tag alvo para commit."
    return 1
  }
  [[ "$TARGET_OID" =~ ^[0-9a-f]{40}$ ]] || {
    log_error "Erro: OID alvo invalido."
    return 1
  }

  local_tag_oid="$(git rev-parse --verify "refs/tags/${TARGET_TAG}^{commit}" 2>/dev/null || true)"
  if [ -n "$local_tag_oid" ] && [ "$local_tag_oid" != "$TARGET_OID" ]; then
    log_error "Erro: tag local ${TARGET_TAG} diverge da tag remota; possivel tag movida."
    return 1
  fi

  TARGET_VERSION="$(git show "${TARGET_OID}:VERSION" 2>/dev/null | tr -d '[:space:]')"
  [ -n "$TARGET_VERSION" ] || {
    log_error "Erro: VERSION ausente no commit alvo."
    return 1
  }
  canonicalize_version "$TARGET_VERSION" >/dev/null 2>&1 || {
    log_error "Erro: VERSION alvo invalida: ${TARGET_VERSION}"
    return 1
  }
  [ "$TARGET_VERSION" = "$TARGET_TAG" ] || {
    log_error "Erro: VERSION do commit alvo (${TARGET_VERSION}) diverge da tag (${TARGET_TAG})."
    return 1
  }
  log_ok "Tag alvo resolvida: ${TARGET_TAG} (${TARGET_OBJECT_TYPE}) -> ${TARGET_OID}."
}

validate_external_bundle_target() {
  [ "$UPDATER_BUNDLE_EXTERNAL" = true ] || return 0
  [ "${CONTABASE_UPDATER_BUNDLE_COMMIT:-}" = "$TARGET_OID" ] || {
    log_error "Erro: identidade do bundle externo diverge do alvo congelado."
    return 1
  }
  log_ok "Bundle externo do updater validado: versao ${TARGET_VERSION}, commit ${TARGET_OID}."
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
  for cmd in git go node npm curl systemctl sqlite3 openssl tar install awk sed tr wc mktemp find; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      missing+=("$cmd")
    fi
  done

  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    missing+=("sha256sum ou shasum")
  fi

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
  git fetch --no-tags "$CONTABASE_OFFICIAL_ORIGIN" "$UPSTREAM_MERGE_REF" || return 1
  assert_local_tags_unchanged || return 1
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
  local remote="" merge_ref="" remote_branch upstream_origin remote_set=false merge_set=false

  if remote="$(git config --get-all "branch.${CURRENT_BRANCH}.remote" && printf '.')"; then
    remote_set=true
    remote="${remote%$'\n.'}"
  elif [ "$?" -ne 1 ]; then
    log_error "Erro: falha ao ler o remote da branch ${CURRENT_BRANCH}."
    return 1
  fi
  if merge_ref="$(git config --get-all "branch.${CURRENT_BRANCH}.merge" && printf '.')"; then
    merge_set=true
    merge_ref="${merge_ref%$'\n.'}"
  elif [ "$?" -ne 1 ]; then
    log_error "Erro: falha ao ler o merge da branch ${CURRENT_BRANCH}."
    return 1
  fi

  if [ "$remote_set" = false ] && [ "$merge_set" = false ] && [ "$STATE_PROVENANCE_COMPLETE" = true ]; then
    remote="origin"
    merge_ref="refs/heads/${CURRENT_BRANCH}"
  elif [ "$remote_set" != true ] || [ "$merge_set" != true ]; then
    log_error "Erro: upstream incompleto ou ausente sem proveniencia persistida completa."
    return 1
  fi

  case "$merge_ref" in
    refs/heads/*) git check-ref-format "$merge_ref" || return 1 ;;
    *)
      log_error "Erro: upstream da branch ${CURRENT_BRANCH} nao aponta para refs/heads: ${merge_ref}."
      return 1
      ;;
  esac

  UPSTREAM_REMOTE="$remote"
  UPSTREAM_MERGE_REF="$merge_ref"

  validate_remote_origin "$REPO_ROOT" "$UPSTREAM_REMOTE" || return 1
  upstream_origin="$VALIDATED_ORIGIN"
  [ "$upstream_origin" = "$EFFECTIVE_ORIGIN" ] || {
    log_error "Erro: remote de upstream diverge da origem efetiva validada."
    return 1
  }

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
  local repo_path installed_channel persisted_channel wrapper_source
  local provenance_kind provenance_ref provenance_commit provenance_origin
  repo_path="$REPO_ROOT"
  persisted_channel="$(state_value "$STATE_FILE" CONTABASE_CHANNEL)"
  installed_channel="${persisted_channel:-$(infer_install_channel)}"
  provenance_kind="$CHECKOUT_KIND"
  provenance_ref="$TARGET_REF"
  provenance_commit="$TARGET_OID"
  provenance_origin="$INSTALL_ORIGIN"
  wrapper_source="${repo_path}/scripts/lib/contabase-update-wrapper.sh"

  if [ "$UPDATER_BUNDLE_EXTERNAL" != true ] \
    && { [ ! -f "$wrapper_source" ] || [ -L "$wrapper_source" ]; }; then
    log_error "Erro: dispatcher autenticado ausente ou inseguro: $wrapper_source"
    log_error "Nenhum contabase-update foi instalado ou substituido."
    return 1
  fi

  if [ "$UPDATER_BUNDLE_EXTERNAL" = true ]; then
    [ -n "$VERIFIED_WRAPPER_PATH" ] && [ -n "$VERIFIED_WRAPPER_SHA256" ] || {
      log_error "Erro: wrapper verificado ausente no bundle autenticado."
      return 1
    }
    bundle_path_unsafe "$VERIFIED_WRAPPER_PATH" && {
      log_error "Erro: caminho do wrapper verificado contem symlink ou componente inseguro."
      return 1
    }
    [ -f "$VERIFIED_WRAPPER_PATH" ] && [ ! -L "$VERIFIED_WRAPPER_PATH" ] || {
      log_error "Erro: wrapper verificado ausente ou inseguro."
      return 1
    }
    [ "$(sha256_file "$VERIFIED_WRAPPER_PATH")" = "$VERIFIED_WRAPPER_SHA256" ] || {
      log_error "Erro: wrapper verificado diverge do hash autorizado."
      return 1
    }
  fi

  printf '%s\n' "source" > "$mode_file"
  printf '%s\n' "$repo_path" >> "$mode_file"
  chown root:root "$mode_file" 2>/dev/null || true
  chmod 0644 "$mode_file"

  if [ "$UPDATER_BUNDLE_EXTERNAL" = true ]; then
    [ "$(sha256_file "$VERIFIED_WRAPPER_PATH")" = "$VERIFIED_WRAPPER_SHA256" ] || {
      log_error "Erro: wrapper verificado foi alterado antes da instalacao."
      return 1
    }
    install -o root -g root -m 0755 "$VERIFIED_WRAPPER_PATH" "$update_wrapper" || {
      log_error "Erro: nao foi possivel instalar o wrapper verificado."
      return 1
    }
    if [ ! -e "$update_alias" ]; then
      ln -s "$(basename "$update_wrapper")" "$update_alias" 2>/dev/null || true
    fi
    write_install_state "source" "$installed_channel" "$APP_VERSION" "$repo_path" \
      "$provenance_kind" "$provenance_ref" "$provenance_commit" "$provenance_origin" || return 1
    echo ""
    echo -e "${GREEN}Comando global de atualizacao instalado:${NC}"
    echo "  sudo contabase-update"
    echo "  sudo cb-update"
    return 0
  fi

  install -o root -g root -m 0755 "$wrapper_source" "$update_wrapper" || {
    log_error "Erro: nao foi possivel instalar o wrapper local."
    return 1
  }
  if [ ! -e "$update_alias" ]; then
    ln -s "$(basename "$update_wrapper")" "$update_alias" 2>/dev/null || true
  fi
  write_install_state "source" "$installed_channel" "$APP_VERSION" "$repo_path" \
    "$provenance_kind" "$provenance_ref" "$provenance_commit" "$provenance_origin" || return 1
  echo ""
  echo -e "${GREEN}Comando global de atualizacao instalado:${NC}"
  echo "  sudo contabase-update"
  echo "  sudo cb-update"
  return 0
}

finalize_confirmed_update() {
  local head_oid journal_present=false

  enforce_clean_worktree || return 1
  if [ -e "$TRANSITION_JOURNAL" ] || [ -L "$TRANSITION_JOURNAL" ]; then
    [ -f "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || {
      log_error "Erro: registro de transicao inseguro."
      return 1
    }
    journal_present=true
    validate_transition_journal_structure || return 1
    transition_journal_matches_target || {
      log_error "Erro: registro de transicao diverge do alvo confirmado."
      return 1
    }
  fi

  if [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ]; then
    if finalize_marker_matches_target; then
      validate_finalize_marker_for_target || return 1
    else
      validate_finalize_marker_structure || return 1
      [ "$journal_present" = true ] || {
        log_error "Erro: marcador anterior sem intencao de transicao persistida."
        log_error "Preserve as evidencias e use recuperacao administrativa."
        return 1
      }
      validate_transition_journal_previous || return 1
      [ "${CONFIRMED_RUNTIME_VERSION:-}" = "$TARGET_VERSION" ] || {
        log_error "Erro: runtime nao confirma o alvo ${TARGET_VERSION} para substituir o marcador."
        return 1
      }
      log_warn "Substituindo marcador de finalizacao anterior pelo alvo confirmado ${TARGET_VERSION}."
      write_finalize_marker || {
        log_error "Erro: nao foi possivel substituir o marcador de finalizacao."
        return 1
      }
    fi
  elif [ -e "$FINALIZE_MARKER" ] || [ -L "$FINALIZE_MARKER" ]; then
    log_error "Erro: marcador de finalizacao inseguro."
    return 1
  else
    [ "$journal_present" = true ] || {
      log_error "Erro: finalizacao sem marcador ou intencao de transicao persistida."
      log_error "Preserve as evidencias e use recuperacao administrativa."
      return 1
    }
    validate_transition_journal_previous || return 1
    [ "${CONFIRMED_RUNTIME_VERSION:-}" = "$TARGET_VERSION" ] || {
      log_error "Erro: runtime nao confirma o alvo ${TARGET_VERSION} para finalizar."
      return 1
    }
    write_finalize_marker || {
      log_error "Erro: nao foi possivel persistir o marcador de finalizacao."
      return 1
    }
  fi

  head_oid="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  if [ "$head_oid" != "$TARGET_OID" ]; then
    if [ "$CHECKOUT_KIND" = "tag" ]; then
      git checkout --detach "$TARGET_OID" || {
        log_error "Checkout destacado do alvo falhou; nenhum reset/checkout -f sera executado."
        return 1
      }
    else
      git merge-base --is-ancestor "$head_oid" "$TARGET_OID" || {
        log_error "Checkout atual nao pode avancar em fast-forward para o runtime confirmado ${TARGET_OID}."
        return 1
      }
      merge_target_oid || return 1
    fi
  fi

  ACTUAL_HEAD_OID="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  [ "$ACTUAL_HEAD_OID" = "$TARGET_OID" ] || {
    log_error "HEAD apos finalizacao (${ACTUAL_HEAD_OID}) diverge do runtime confirmado (${TARGET_OID})."
    return 1
  }
  ensure_local_tag || return 1
  resolve_checkout_version || return 1
  APP_VERSION="$TARGET_VERSION"
  backfill_update_command || return 1
  remove_transition_journal || {
    log_error "Erro: nao foi possivel remover o registro de transicao."
    return 1
  }
  remove_finalize_marker || {
    log_error "Erro: nao foi possivel remover o marcador de finalizacao."
    return 1
  }
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
  reject_unsafe_git_environment || exit 1
  detect_checkout_provenance || exit 1
  resolve_previous_version
  detect_confirmed_runtime_version
  APP_VERSION="${PREVIOUS_VERSION:-desconhecida}"

  log_step "[1/9] Resolvendo alvo e congelando OID"
  enforce_clean_worktree || exit 1
  record_local_tags || exit 1
  if [ "$CHECKOUT_KIND" = "tag" ]; then
    resolve_target_tag || exit 1
    fetch_target_tag || exit 1
  else
    resolve_upstream || exit 1
    TARGET_REF="$CURRENT_BRANCH"
    fetch_remote_state || exit 1
    freeze_target_oid || exit 1
    resolve_target_version || exit 1
  fi
  validate_external_bundle_target || exit 1
  resolve_current_version || exit 1
  if [ "$CHECKOUT_KIND" = "tag" ] && [ "$TARGET_VERSION" = "$CURRENT_VERSION" ] \
    && [ "$RECOVERY_FINALIZE_ONLY" != true ] && [ "$TARGET_OID" != "$CURRENT_HEAD_OID" ]; then
    log_error "Erro: mesma versao com OID divergente; tag possivelmente movida."
    exit 1
  fi
  APP_VERSION="$TARGET_VERSION"
  validate_target_channel || exit 1
  ensure_fast_forward_possible || exit 1
  validate_pending_marker_for_target || exit 1
  enforce_version_policy
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
  write_transition_journal || {
    log_error "Erro: nao foi possivel registrar a intencao da transicao."
    return 1
  }
  log_ok "Intencao de transicao persistida para ${TARGET_VERSION}."
  preserve_previous_bundle
  log_ok "Build concluido no snapshot; bundle anterior preservado."

  log_step "[8/9] Instalando novo bundle e iniciando servico"
  install_new_bundle
  start_new_runtime
  if run_healthcheck; then
    RUNTIME_CONFIRMED=true
    CONFIRMED_RUNTIME_VERSION="$TARGET_VERSION"
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

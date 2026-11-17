#!/usr/bin/env bash
# O fluxo le secrets; mantenha xtrace desativado ate o fim do processo.
set +x
set -euo pipefail

# Dependencias externas para execucao pelo dispatcher oficial (F3):
#   scripts/lib/contabase-install-state.sh     (obrigatoria, carregada abaixo)
#   scripts/lib/contabase-recovery.sh          (obrigatoria, carregada abaixo)
#   scripts/lib/contabase-update-wrapper.sh    (backfill do contabase-update)
#   scripts/ops/backup.sh no OID alvo          (materializado via git show)
#   Dockerfile e docker-compose.yml no OID alvo
#   docker-compose.override.yml e .env.docker no REPO_ROOT
#   VERSION e updater.sha256 no bundle externo autenticado pela F3
#   git, docker compose, curl, awk, sed, tar, mktemp, python3
# O dispatch Docker por imagem continua isolado em contabase-docker-image-update.py.
SCRIPT_ENTRY="${BASH_SOURCE[0]}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
SCRIPT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd -P)"
if [ -n "${CONTABASE_REPO_ROOT:-}" ]; then
  case "$CONTABASE_REPO_ROOT" in
    /*) ;;
    *) printf 'Erro: CONTABASE_REPO_ROOT deve ser absoluto: %s\n' "$CONTABASE_REPO_ROOT" >&2; exit 1 ;;
  esac
  REPO_ROOT="$(cd "$CONTABASE_REPO_ROOT" 2>/dev/null && pwd -P)" || {
    printf 'Erro: CONTABASE_REPO_ROOT inacessivel: %s\n' "$CONTABASE_REPO_ROOT" >&2
    exit 1
  }
else
  REPO_ROOT="$(pwd -P)"
fi
# Componentes sao lidos por expansao literal, sem glob, regex ou escape.
# O mesmo caminho inspecionado e o caminho usado pelo chamador.
bundle_path_unsafe() {
  local remaining="$1" current="" part
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
  return 1
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# B1: contrato autenticado independe de SCRIPT_DIR == REPO_ROOT/scripts.
# Qualquer variavel definida (inclusive vazia) ou manifesto presente
# (inclusive symlink pendente) exige o contrato completo.
bundle_contract_requested=false
[ -n "${CONTABASE_UPDATER_BUNDLE_COMMIT+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATE_WRAPPER_PATH+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATE_WRAPPER_SHA256+x}" ] && bundle_contract_requested=true
if [ -e "$SCRIPT_ROOT/updater.sha256" ] || [ -L "$SCRIPT_ROOT/updater.sha256" ]; then
  bundle_contract_requested=true
fi

UPDATER_BUNDLE_EXTERNAL=false
if [ "$bundle_contract_requested" = true ]; then
  UPDATER_BUNDLE_EXTERNAL=true
  LIB_DIR="$SCRIPT_ROOT/scripts/lib"
  [ -n "${CONTABASE_VERSION:-}" ] || {
    printf 'Erro: CONTABASE_VERSION obrigatoria no modo autenticado.\n' >&2
    exit 1
  }
  [ -n "${CONTABASE_REPO_ROOT:-}" ] || {
    printf 'Erro: CONTABASE_REPO_ROOT obrigatorio no modo autenticado.\n' >&2
    exit 1
  }
  raw_entry="$SCRIPT_ENTRY"
  case "$raw_entry" in /*) ;; *) raw_entry="$(pwd)/$raw_entry" ;; esac
  bundle_path_unsafe "$raw_entry" && {
    printf 'Erro: caminho do updater com symlink ou componente inseguro.\n' >&2
    exit 1
  }
elif [ "$SCRIPT_DIR" = "$REPO_ROOT/scripts" ]; then
  LIB_DIR="$REPO_ROOT/scripts/lib"
elif [ -f "$REPO_ROOT/scripts/lib/contabase-install-state.sh" ]; then
  LIB_DIR="$REPO_ROOT/scripts/lib"
else
  printf 'Erro: contrato de bundle ausente e checkout sem bibliotecas do updater.\n' >&2
  exit 1
fi
# F3 must verify the updater itself before launch and keep the bundle private
# and immutable. The manifest digest and commit must come from that caller.
validate_external_bundle() {
  [ "$UPDATER_BUNDLE_EXTERNAL" = true ] || return 0
  python3 -I -B - "$SCRIPT_ENTRY" "$SCRIPT_ROOT" "${CONTABASE_VERSION:-}" \
    "${CONTABASE_UPDATER_BUNDLE_COMMIT:-}" "${CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256:-}" <<'PYTHON'
import hashlib
import os
from pathlib import Path
import re
import stat
import sys

entry, root, version, commit, manifest_digest = sys.argv[1:]
required = {
    "VERSION", "scripts/update-contabase-docker.sh",
    "scripts/lib/contabase-install-state.sh", "scripts/lib/contabase-recovery.sh",
    "scripts/lib/contabase-update-wrapper.sh",
}
image_helper = "scripts/lib/contabase-docker-image-update.py"
allowed = required | {
    "scripts/update-contabase-source.sh",
    "scripts/update-contabase-release.sh",
    "scripts/lib/contabase-update-wrapper.sh",
    "scripts/lib/contabase-docker-image-update.py",
}


def regular_file(path):
    path = Path(path).absolute()
    if ".." in path.parts or any(parent.is_symlink() for parent in path.parents):
        raise ValueError(f"caminho com symlink ou componente inseguro: {path}")
    if not stat.S_ISREG(path.lstat().st_mode):
        raise ValueError(f"arquivo ausente ou inseguro: {path}")
    return path


try:
    if (not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+(?:-beta\.[0-9]+)?", version)
            or not re.fullmatch(r"[0-9a-f]{40}", commit)
            or not re.fullmatch(r"[0-9a-f]{64}", manifest_digest)):
        raise ValueError("versao, commit e digest autenticado do manifesto sao obrigatorios")
    if regular_file(entry) != Path(root) / "scripts/update-contabase-docker.sh":
        raise ValueError("caminho do updater diverge do bundle")
    manifest = regular_file(Path(root) / "updater.sha256").read_bytes()
    if hashlib.sha256(manifest).hexdigest() != manifest_digest:
        raise ValueError("manifesto diverge da identidade autorizada")
    if b"\r" in manifest:
        raise ValueError("CRLF nao permitido no updater.sha256")
    header, *lines = manifest.decode("ascii").splitlines()
    if header != f"commit={commit}":
        raise ValueError("commit do manifesto diverge do autorizado")
    seen = set()
    previous = ""
    for line in lines:
        digest, path = line.split("  ", 1)
        if path not in allowed or path in seen:
            raise ValueError(f"entrada invalida no manifesto: {path}")
        if previous and path < previous:
            raise ValueError("manifesto fora da ordem canonica")
        if hashlib.sha256(regular_file(Path(root) / path).read_bytes()).hexdigest() != digest:
            raise ValueError(f"conteudo divergente no bundle: {path}")
        seen.add(path)
        previous = path
    if not required <= seen or (os.path.lexists(Path(root) / image_helper) and image_helper not in seen):
        raise ValueError("manifesto incompleto")
    root_mode = os.lstat(root).st_mode
    if not stat.S_ISDIR(root_mode):
        raise ValueError("bundle externo ausente ou inseguro")
    # os.scandir propaga EACCES/EIO; Path.rglob ignora subarvores
    # inacessiveis e aceitaria inventario parcial. Nenhum erro e suprimido.
    pending_dirs = [root]
    while pending_dirs:
        with os.scandir(pending_dirs.pop()) as items:
            for item in items:
                mode = item.stat(follow_symlinks=False).st_mode
                if stat.S_ISLNK(mode):
                    raise ValueError(f"symlink no bundle: {item.path}")
                if stat.S_ISDIR(mode):
                    pending_dirs.append(item.path)
                if stat.S_ISREG(mode) and (mode & 0o111):
                    relative = Path(item.path).relative_to(root).as_posix()
                    if relative not in seen:
                        raise ValueError(f"arquivo executavel nao declarado: {relative}")
    if (Path(root) / "VERSION").read_text(encoding="ascii").strip() != version:
        raise ValueError("VERSION do bundle diverge da versao solicitada")
except (OSError, ValueError) as exc:
    sys.exit(f"Erro: bundle externo do updater recusado: {exc}")
PYTHON
}

dispatch_docker_image_update() {
  local state="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}" method=""
  if [ -f "$state" ]; then
    method="$(awk -F= '$1 == "CONTABASE_INSTALL_METHOD" { print substr($0,index($0,"=")+1); exit }' "$state")"
    if [ "$method" = docker-image ]; then
      [ -f "$LIB_DIR/contabase-docker-image-update.py" ] && [ ! -L "$LIB_DIR/contabase-docker-image-update.py" ] || {
        echo "Erro: modulo docker-image ausente ou inseguro." >&2
        exit 1
      }
      # Preserve the headless environment contract without injecting duplicate flags.
      exec python3 -I -B "$LIB_DIR/contabase-docker-image-update.py" \
        --state-file "$state" "$@"
    fi
    if grep -Eq '^CONTABASE_(IMAGE_|COMPOSE_IMAGE_FILE=)' "$state" || [ -e docker-compose.image.yml ]; then
      printf '%s\n' 'Erro: estado de imagem contradiz o metodo salvo.' >&2
      exit 1
    fi
  fi
  if [ "${CONTABASE_INSTALL_METHOD:-}" = docker-image ]; then
    printf '%s\n' 'Erro: estado docker-image ausente ou divergente.' >&2
    exit 1
  fi
}
validate_external_bundle || exit 1
[ -d "$LIB_DIR" ] || {
  printf 'Erro: diretorio de bibliotecas do updater ausente: %s\n' "$LIB_DIR" >&2
  exit 1
}
VERIFIED_WRAPPER_PATH=""
VERIFIED_WRAPPER_SHA256=""
if [ "$UPDATER_BUNDLE_EXTERNAL" = true ]; then
  VERIFIED_WRAPPER_PATH="$SCRIPT_ROOT/scripts/lib/contabase-update-wrapper.sh"
  VERIFIED_WRAPPER_SHA256="$(awk -F '  ' '$2 == "scripts/lib/contabase-update-wrapper.sh" { print $1 }' "$SCRIPT_ROOT/updater.sha256")"
  [ -n "$VERIFIED_WRAPPER_SHA256" ] || {
    printf 'Erro: wrapper verificado ausente do manifesto.\n' >&2
    exit 1
  }
fi
dispatch_docker_image_update "$@"

STATE_LIB="$LIB_DIR/contabase-install-state.sh"
RECOVERY_LIB="$LIB_DIR/contabase-recovery.sh"
for required_lib in "$STATE_LIB" "$RECOVERY_LIB"; do
  [ -f "$required_lib" ] && [ ! -L "$required_lib" ] || {
    printf 'Erro: biblioteca obrigatoria ausente ou insegura: %s\n' "$required_lib" >&2
    exit 1
  }
done
# shellcheck source=lib/contabase-install-state.sh
. "$STATE_LIB" || { printf '%s\n' 'Erro: falha ao carregar o contrato de estado.' >&2; exit 1; }

# ==============================================================================
# update-contabase-docker.sh — Atualização segura do ContaBase via Docker Compose
#
# Por padrão, este script é seguro:
#   - NÃO apaga seu banco de dados.
#   - NÃO apaga seus arquivos .env.docker.
#   - NÃO apaga seu compose.local.yaml.
#   - NÃO executa git reset --hard nem git clean.
#   - Faz backup do banco ANTES de atualizar.
#   - Pede confirmação antes de prosseguir.
#
# Flags:
#   --yes              Não perguntar confirmação.
#   --force-reset      Modo agressivo: git reset --hard + git clean (preserva .env, banco e local).
#                      Recusado em instalacoes por tag.
#   --compose ARQUIVO  Usar compose específico em vez da busca automática.
#   --skip-pull        Recompilar sem atualizar Git (útil para testes locais).
#                      Recusado em instalacoes por tag, salvo no-op comprovado.
#   --help             Mostrar esta ajuda.
#
# Modo tag:
#   Instalacoes com checkout detached em tag oficial exigem CONTABASE_VERSION
#   explicito. Nenhuma busca automatica de "ultima tag" e feita.
#   Ex.: CONTABASE_VERSION=v1.0.0-beta.8 ./scripts/update-contabase-docker.sh --yes
#
# Uso:
#   ./scripts/update-contabase-docker.sh                    # Atualização segura interativa
#   ./scripts/update-contabase-docker.sh --yes              # Atualização sem confirmação
#   ./scripts/update-contabase-docker.sh --compose docker-compose.yml --yes
#   ./scripts/update-contabase-docker.sh --force-reset      # Reset completo (use com cuidado)
# ==============================================================================

YIELD=false
FORCE_RESET=false
DRY_RUN=false
COMPOSE_FILE=""
SKIP_PULL=false
STACK_STOPPED=false
PREVIOUS_IMAGE_ID=""
PREVIOUS_IMAGE_NAME=""
PREVIOUS_ROLLBACK_TAG=""
PREVIOUS_VERSION=""
APP_VERSION=""
BACKUP_PATH=""
BACKUP_ID=""
CANONICAL_BACKUP_HELPER=""
CANONICAL_BACKUP_HELPER_TMP=""
TARGET_REF=""
TARGET_TAG=""
TARGET_OBJECT_TYPE=""
TARGET_VERSION=""
TARGET_OID=""
CURRENT_VERSION=""
CURRENT_BRANCH=""
CURRENT_COMMIT=""
CURRENT_HEAD_OID=""
STATE_PRESENT=false
NEW_RUNTIME_STARTED=false
CANDIDATE_BUILD_STARTED=false
CANDIDATE_BUILD_TAG=""
CANONICAL_BUILD_DIR=""
CANONICAL_COMPOSE_OVERRIDE=""
ACTIVATION_COMPOSE_OVERRIDE=""
HEALTH_URL=""
ENV_BACKUP_PATH=""
CHECKOUT_VERSION_BEFORE=""
CHECKOUT_VERSION=""
PERSISTED_VERSION=""
CONFIRMED_RUNTIME_VERSION=""
CONFIRMED_RUNTIME_IMAGE_ID=""
OBSERVED_RUNTIME_IMAGE_ID=""
EXPECTED_IMAGE_ID=""
RECOVERY_FINALIZE_ONLY=false
RUNTIME_CONFIRMED=false
COMPOSE_SERVICE="contabase"
RESOLVED_CONTAINER_ID=""
RESOLVED_COMPOSE_PROJECT=""
STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
FINALIZE_MARKER="$(dirname "$STATE_FILE")/finalize.pending"
TRANSITION_JOURNAL="$(dirname "$STATE_FILE")/transition.pending"
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-}"
. "$RECOVERY_LIB"
PREVIOUS_COMPOSE_ID=""
RECOVERY_COMPOSE_FILE=""
CHECKOUT_KIND=""
INSTALL_REF=""
INSTALL_COMMIT=""
INSTALL_ORIGIN=""
TEMP_TARGET_REF="refs/contabase-update/target"
UNSHALLOW_REF_PREFIX="refs/contabase-update/unshallow"
HEAD_BEFORE_FETCH=""
STATUS_BEFORE_FETCH=""
EFFECTIVE_ORIGIN=""
VALIDATED_ORIGIN=""
TAGS_BEFORE_FILE=""
UPSTREAM_REMOTE=""
UPSTREAM_MERGE_REF=""
DRY_GIT_DIR=""
TARGET_REFS_TOUCHED=false
STATE_PROVENANCE_COMPLETE=false

# Accept CONTABASE_ASSUME_YES=1 as headless mode (same as --yes)
[ "${CONTABASE_ASSUME_YES:-0}" = "1" ] && YIELD=true

show_help() {
  {
    printf '%s\n' '# =============================================================================='
    sed -n '/^# update-contabase-docker\.sh — /,/^# ==============================================================================$/p' "$0"
  } | grep '^#' | sed 's/^# //;s/^#$//'
  exit 0
}

while [ $# -gt 0 ]; do
  case "$1" in
    --yes|-y) YIELD=true; shift ;;
    --force-reset) FORCE_RESET=true; shift ;;
    --no-backup)
      echo "Erro: --no-backup foi removido; todo update com estado persistente exige backup canonico."
      exit 1
      ;;
    --skip-pull) SKIP_PULL=true; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --help|-h) show_help ;;
    --compose)
      if [ -z "${2:-}" ]; then
        echo "Erro: --compose requer um arquivo."
        exit 1
      fi
      COMPOSE_FILE="$2"
      shift 2
      ;;
    *) shift ;;
  esac
done

if [ "$DRY_RUN" = true ]; then export GIT_OPTIONAL_LOCKS=0; fi

if [ ! -d "$REPO_ROOT/.git" ]; then
  echo "Erro: execute este script dentro do repositório ContaBase."
  exit 1
fi
cd "$REPO_ROOT" || {
  echo "Erro: nao foi possivel entrar em $REPO_ROOT."
  exit 1
}

read_version_file() {
  if [ ! -f VERSION ]; then
    return 1
  fi

  tr -d '[:space:]' < VERSION
}

validate_external_bundle_target() {
  [ "$UPDATER_BUNDLE_EXTERNAL" = true ] || return 0
  if [ "$CONTABASE_VERSION" != "$TARGET_VERSION" ] || [ "$CONTABASE_UPDATER_BUNDLE_COMMIT" != "$TARGET_OID" ]; then
    echo "Erro: identidade do bundle externo diverge do alvo congelado." >&2
    return 1
  fi
  echo "Bundle externo do updater validado: versao $TARGET_VERSION, commit $TARGET_OID."
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
    echo "Versao instalada nao encontrada ou vazia." >&2
    return 3
  fi
  if [ -z "$target" ]; then
    echo "Versao alvo nao encontrada ou vazia." >&2
    return 3
  fi

  cc="$(canonicalize_version "$current")" || {
    echo "Versao instalada invalida ou ambigua: ${current}" >&2
    return 3
  }
  tc="$(canonicalize_version "$target")" || {
    echo "Versao alvo invalida ou ambigua: ${target}" >&2
    return 3
  }

  cmp="$(compare_canonical $cc $tc)" || {
    echo "Erro interno ao comparar versoes: ${current} vs ${target}" >&2
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
    if [ "$CURRENT_VERSION" = "$TARGET_VERSION" ] \
      && { [ "$persisted" != "$CURRENT_VERSION" ] \
        || [ "$CHECKOUT_VERSION_BEFORE" != "$CURRENT_VERSION" ] \
        || [ -f "$FINALIZE_MARKER" ] \
        || [ -f "$TRANSITION_JOURNAL" ]; }; then
      RECOVERY_FINALIZE_ONLY=true
    fi
    return 0
  fi

  if [ -n "$persisted" ] && [ -n "$PREVIOUS_VERSION" ] \
    && [ "$PREVIOUS_VERSION" != "desconhecida" ] && [ "$PREVIOUS_VERSION" != "unknown" ] \
    && [ "$persisted" != "$PREVIOUS_VERSION" ]; then
    echo "Erro: contradicao de versao: estado persistido=${persisted} checkout=${PREVIOUS_VERSION}."
    return 1
  fi

  if [ -n "$persisted" ]; then
    CURRENT_VERSION="$persisted"
  elif [ -n "$PREVIOUS_VERSION" ] && [ "$PREVIOUS_VERSION" != "desconhecida" ] && [ "$PREVIOUS_VERSION" != "unknown" ]; then
    CURRENT_VERSION="$PREVIOUS_VERSION"
  fi
  return 0
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
      echo "ContaBase ja esta em ${CURRENT_VERSION}; nenhuma atualizacao necessaria."
      exit 0
      ;;
    1)
      echo "Downgrade recusado: instalado=${CURRENT_VERSION} oferecido=${TARGET_VERSION}."
      echo "O updater normal nao realiza downgrade."
      exit 1
      ;;
    3)
      exit 1
      ;;
  esac
}

filter_control_pending_paths() {
  local list="$1" dir="$2" line rel
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    rel="$line"
    if [ -n "$dir" ]; then
      case "$line" in
        "$dir"/*) rel="${line#"$dir"/}" ;;
        *) printf '%s\n' "$line"; continue ;;
      esac
    fi
    case "$rel" in
      finalize.pending|transition.pending) continue ;;
    esac
    printf '%s\n' "$line"
  done <<EOF
$list
EOF
}

control_state_dir_rel() {
  case "$STATE_FILE" in
    "$REPO_ROOT"/*)
      local rel
      rel="$(dirname "${STATE_FILE#"$REPO_ROOT"/}")"
      [ "$rel" = "." ] && rel=""
      printf '%s' "$rel"
      ;;
  esac
}

filter_control_status_paths() {
  local list="$1" dir="$2" line path rel
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    case "$line" in
      "?? "*) path="${line#\?\? }" ;;
      *) printf '%s\n' "$line"; continue ;;
    esac
    rel="$path"
    if [ -n "$dir" ]; then
      case "$path" in
        "$dir"/*) rel="${path#"$dir"/}" ;;
        *) printf '%s\n' "$line"; continue ;;
      esac
    fi
    case "$rel" in
      finalize.pending|transition.pending) continue ;;
    esac
    printf '%s\n' "$line"
  done <<EOF
$list
EOF
}

enforce_clean_worktree() {
  local staged tracked untracked state_dir_rel=""
  staged="$(git diff --cached --name-only 2>/dev/null || true)"
  tracked="$(git diff --name-only 2>/dev/null || true)"
  untracked="$(git ls-files --others --exclude-standard 2>/dev/null || true)"
  state_dir_rel="$(control_state_dir_rel)"
  [ -z "$untracked" ] || untracked="$(filter_control_pending_paths "$untracked" "$state_dir_rel")"

  if [ -n "$staged" ] || [ -n "$tracked" ] || [ -n "$untracked" ]; then
    echo "Erro: working tree nao esta limpo. Resolva manualmente o estado local antes do update."
    if [ -n "$staged" ]; then
      echo "Mudancas staged:"
      printf '%s\n' "$staged"
    fi
    if [ -n "$tracked" ]; then
      echo "Mudancas tracked:"
      printf '%s\n' "$tracked"
    fi
    if [ -n "$untracked" ]; then
      echo "Arquivos untracked nao ignorados:"
      printf '%s\n' "$untracked"
    fi
    return 1
  fi
  return 0
}

# ==============================================================================
# Proveniencia Git, tag oficial e finalizacao durave
# ==============================================================================

# The directory and each rename must be flushed separately. Python is used
# for the same filesystem operations on Linux and macOS.
durable_state_file() {
  local operation="$1" destination="$2" dir
  local runner=("$STATE_PYTHON")
  dir="$(dirname "$destination")"
  if [ "$operation" != check ] && [ "$(id -u)" -ne 0 ] && [ ! -w "$dir" ]; then
    runner=(sudo "$STATE_PYTHON")
  fi
  "${runner[@]}" -I -B - "$operation" "$destination" "${3:-}" <<'PYTHON'
import os
import shutil
import stat
import sys
import tempfile

operation, path, source = sys.argv[1:]
directory, name = os.path.split(os.path.abspath(path))
try:
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        if operation in {"replace", "probe"}:
            tmp_fd, tmp_path = tempfile.mkstemp(prefix=".contabase-state-", dir=directory)
            try:
                with os.fdopen(tmp_fd, "wb") as temp:
                    if operation == "replace":
                        with open(source, "rb") as data:
                            shutil.copyfileobj(data, temp)
                        os.fchmod(temp.fileno(), 0o644)
                    else:
                        temp.write(b"fsync probe\n")
                    temp.flush()
                    os.fsync(temp.fileno())
                if operation == "replace":
                    os.replace(os.path.basename(tmp_path), name, src_dir_fd=fd, dst_dir_fd=fd)
                else:
                    os.unlink(os.path.basename(tmp_path), dir_fd=fd)
                tmp_path = None
            finally:
                if tmp_path is not None:
                    os.unlink(os.path.basename(tmp_path), dir_fd=fd)
        elif operation == "remove":
            try:
                if not stat.S_ISREG(os.stat(name, dir_fd=fd, follow_symlinks=False).st_mode):
                    raise OSError("evidencia nao e arquivo regular")
                os.unlink(name, dir_fd=fd)
            except FileNotFoundError:
                pass
        elif operation == "confirm":
            existing = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=fd)
            try:
                if not stat.S_ISREG(os.fstat(existing).st_mode):
                    raise OSError("evidencia nao e arquivo regular")
                os.fsync(existing)
            finally:
                os.close(existing)
        elif operation != "check":
            raise ValueError(f"operacao desconhecida: {operation}")
        os.fsync(fd)
    finally:
        os.close(fd)
except (OSError, ValueError) as exc:
    sys.exit(f"Erro: persistencia {operation} em {path}: {exc}")
PYTHON
}

install_pending_file() {
  durable_state_file replace "$2" "$1"
}

remove_pending_file() {
  durable_state_file remove "$1"
}

lock_state_directory() {
  local dir="$1"
  exec 9<"$dir" || return 1
  # flock remains held by descriptor 9 in the updater after Python exits.
  "$STATE_PYTHON" -I -B -c 'import fcntl; fcntl.flock(9, fcntl.LOCK_EX | fcntl.LOCK_NB)' || {
    echo "Erro: nao foi possivel reservar o diretorio de estado para o update." >&2
    return 1
  }
}

ensure_state_write_access() {
  local dir
  dir="$(dirname "$STATE_FILE")"
  if [ "$(id -u)" -eq 0 ] || [ -w "$dir" ] || { [ ! -e "$dir" ] && [ -w "$(dirname "$dir")" ]; }; then
    mkdir -p "$dir" || return 1
    return 0
  fi
  command -v sudo >/dev/null 2>&1 || {
    echo "Erro: root ou sudo e obrigatorio para persistir marcador/estado do update." >&2
    return 1
  }
  sudo -v || {
    echo "Erro: nao foi possivel confirmar sudo antes de mutar a instalacao." >&2
    return 1
  }
  sudo mkdir -p "$dir" || return 1
}

validate_install_method() {
  local method=""
  if [ -n "${CONTABASE_INSTALL_METHOD:-}" ] && [ "${CONTABASE_INSTALL_METHOD}" != "docker" ]; then
    echo "Erro: metodo de instalacao em ambiente diverge do updater Docker: ${CONTABASE_INSTALL_METHOD}" >&2
    return 1
  fi
  [ -f "$STATE_FILE" ] || return 0
  state_validate_file "$STATE_FILE" || return 1
  method="$(state_value "$STATE_FILE" CONTABASE_INSTALL_METHOD)"
  case "$method" in
    docker) return 0 ;;
    docker-image)
      echo "Erro: metodo docker-image deve ser atualizado pelo updater de imagem/digest." >&2
      return 1
      ;;
    release|binary|source|source-bootstrap)
      echo "Erro: estado pertence ao metodo ${method}; este updater gerencia apenas Docker Compose (docker)." >&2
      echo "Nao troque silenciosamente o metodo de instalacao." >&2
      return 1
      ;;
    *)
      echo "Erro: metodo de instalacao desconhecido no estado: ${method:-<vazio>}." >&2
      return 1
      ;;
  esac
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
        echo "Erro: variavel de ambiente Git nao permitida: ${name}" >&2
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
      echo "Erro: remote Git invalido: ${remote:-<vazio>}" >&2
      return 1
      ;;
  esac

  raw_values="$(git -C "$repo" config --get-all "remote.${remote}.url" 2>/dev/null || true)"
  raw_count="$(state_count_lines "$raw_values")"
  [ "$raw_count" = "1" ] || {
    echo "Erro: remote ${remote} ausente ou com URL ambigua." >&2
    return 1
  }
  raw="$(printf '%s\n' "$raw_values" | sed -n '1p')"
  normalized="$(state_normalize_origin "$raw")" || {
    echo "Erro: URL configurada nao pertence a origem oficial." >&2
    return 1
  }
  [ "$normalized" = "$expected" ] || {
    echo "Erro: URL configurada diverge da origem oficial." >&2
    return 1
  }

  effective_values="$(git -C "$repo" remote get-url --all "$remote" 2>/dev/null || true)"
  effective_count="$(state_count_lines "$effective_values")"
  [ "$effective_count" = "1" ] || {
    echo "Erro: URL efetiva do remote ${remote} ausente ou ambigua." >&2
    return 1
  }
  effective="$(printf '%s\n' "$effective_values" | sed -n '1p')"
  normalized="$(state_normalize_origin "$effective")" || {
    echo "Erro: URL efetiva nao pertence a origem oficial." >&2
    return 1
  }
  [ "$normalized" = "$expected" ] || {
    echo "Erro: URL efetiva diverge da origem oficial." >&2
    return 1
  }

  while IFS= read -r rewrite; do
    [ -n "$rewrite" ] || continue
    value="${rewrite#* }"
    [ -n "$value" ] || continue
    case "$expected" in
      "$value"*)
        echo "Erro: configuracao Git de reescrita de URL afeta a origem oficial." >&2
        return 1
        ;;
    esac
  done <<< "$(git -C "$repo" config --get-regexp '^url\..*\.(insteadOf|pushInsteadOf)$' 2>/dev/null || true)"

  VALIDATED_ORIGIN="$normalized"
  return 0
}

record_local_tags() {
  TAGS_BEFORE_FILE="$(mktemp "${TMPDIR:-/tmp}/contabase-tags-before.XXXXXX")" || return 1
  if ! git -C "$REPO_ROOT" for-each-ref --format='%(refname) %(objectname)' refs/tags \
    | LC_ALL=C sort > "$TAGS_BEFORE_FILE"; then
    echo "Erro: nao foi possivel registrar as tags locais antes do fetch." >&2
    return 1
  fi
}

assert_local_tags_unchanged() {
  local current
  [ -n "$TAGS_BEFORE_FILE" ] && [ -f "$TAGS_BEFORE_FILE" ] || {
    echo "Erro: registro previo das tags locais ausente." >&2
    return 1
  }
  current="$(mktemp "${TMPDIR:-/tmp}/contabase-tags-after.XXXXXX")" || return 1
  if ! git -C "$REPO_ROOT" for-each-ref --format='%(refname) %(objectname)' refs/tags \
    | LC_ALL=C sort > "$current"; then
    rm -f "$current"
    echo "Erro: nao foi possivel reler as tags locais." >&2
    return 1
  fi
  if ! cmp -s "$TAGS_BEFORE_FILE" "$current"; then
    rm -f "$current"
    echo "Erro: refs/tags locais foram alteradas durante a obtencao de objetos Git." >&2
    return 1
  fi
  rm -f "$current"
  return 0
}

is_shallow_repo() {
  local shallow
  if shallow="$(target_git rev-parse --is-shallow-repository 2>/dev/null)"; then
    [ "$shallow" = "true" ]
    return
  fi
  shallow="$(target_git rev-parse --git-path shallow 2>/dev/null || true)"
  [ -n "$shallow" ] && [ -s "$shallow" ]
}

assert_checkout_unchanged() {
  local head_after="${1:-}" status_now
  [ -z "$head_after" ] || [ "$head_after" = "$HEAD_BEFORE_FETCH" ] || {
    echo "Erro: HEAD mudou durante a obtencao de objetos Git." >&2
    return 1
  }
  [ "$(git rev-parse --verify 'HEAD^{commit}' 2>/dev/null)" = "$HEAD_BEFORE_FETCH" ] || {
    echo "Erro: HEAD diverge do valor observado antes do preflight Git." >&2
    return 1
  }
  status_now="$(git status --porcelain 2>/dev/null || true)"
  status_now="$(filter_control_status_paths "$status_now" "$(control_state_dir_rel)")"
  if [ "$FORCE_RESET" = false ]; then
    [ -z "$status_now" ] || {
      echo "Erro: worktree mudou durante a obtencao de objetos Git." >&2
      return 1
    }
    [ -z "$STATUS_BEFORE_FETCH" ] || {
      echo "Erro: worktree nao estava limpo antes da obtencao de objetos Git." >&2
      return 1
    }
  fi
}

resolve_upstream() {
  local remote="" merge_ref="" remote_set=false merge_set=false

  if remote="$(git config --get-all "branch.$CURRENT_BRANCH.remote" && printf '.')"; then
    remote_set=true
    remote="${remote%$'\n.'}"
  elif [ "$?" -ne 1 ]; then
    return 1
  fi
  if merge_ref="$(git config --get-all "branch.$CURRENT_BRANCH.merge" && printf '.')"; then
    merge_set=true
    merge_ref="${merge_ref%$'\n.'}"
  elif [ "$?" -ne 1 ]; then
    return 1
  fi

  if [ "$remote_set" = false ] && [ "$merge_set" = false ] && [ "$STATE_PROVENANCE_COMPLETE" = true ]; then
    remote=origin
    merge_ref="refs/heads/$CURRENT_BRANCH"
  elif [ "$remote_set" != true ] || [ "$merge_set" != true ]; then
    echo "Erro: upstream incompleto ou ausente sem proveniencia persistida completa." >&2
    return 1
  fi
  case "$merge_ref" in
    refs/heads/*) git check-ref-format "$merge_ref" || return 1 ;;
    *) echo "Erro: upstream da branch $CURRENT_BRANCH nao aponta para refs/heads: $merge_ref" >&2; return 1 ;;
  esac
  validate_remote_origin "$REPO_ROOT" "$remote" || return 1
  [ "$VALIDATED_ORIGIN" = "$EFFECTIVE_ORIGIN" ] || return 1

  UPSTREAM_REMOTE="$remote"
  UPSTREAM_MERGE_REF="$merge_ref"
  echo "Upstream resolvido: remote=$UPSTREAM_REMOTE ref=$UPSTREAM_MERGE_REF."
}

target_git() {
  if [ -n "$DRY_GIT_DIR" ]; then
    GIT_DIR="$DRY_GIT_DIR/.git" git "$@"
  else
    git "$@"
  fi
}

fetch_remote_state() {
  HEAD_BEFORE_FETCH="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  STATUS_BEFORE_FETCH="$(git status --porcelain 2>/dev/null || true)"
  STATUS_BEFORE_FETCH="$(filter_control_status_paths "$STATUS_BEFORE_FETCH" "$(control_state_dir_rel)")"
  if [ "$FORCE_RESET" = false ]; then
    [ -z "$STATUS_BEFORE_FETCH" ] || {
      echo "Erro: worktree nao esta limpo antes do fetch da branch." >&2
      return 1
    }
  fi
  TARGET_REFS_TOUCHED=true
  target_git fetch --no-tags "$CONTABASE_OFFICIAL_ORIGIN" "$UPSTREAM_MERGE_REF" || return 1
  assert_local_tags_unchanged || return 1
  assert_checkout_unchanged "$(git rev-parse --verify 'HEAD^{commit}')" || return 1
}

freeze_target_oid() {
  TARGET_OID="$(target_git rev-parse --verify 'FETCH_HEAD^{commit}')" || {
    echo "Erro: nao foi possivel congelar o alvo a partir do FETCH_HEAD." >&2
    return 1
  }
  [ -n "$TARGET_OID" ] || {
    echo "Erro: TARGET_OID vazio apos o fetch." >&2
    return 1
  }
  echo "Alvo congelado: ${TARGET_OID} (HEAD atual: ${CURRENT_HEAD_OID})."
}

resolve_target_version() {
  local ref_version
  ref_version="$(target_git show "${TARGET_OID}:VERSION" 2>/dev/null | tr -d '[:space:]')"
  [ -n "$ref_version" ] || {
    echo "Erro: VERSION ausente no alvo congelado ${TARGET_OID}." >&2
    return 1
  }
  if [ -n "${CONTABASE_VERSION:-}" ] && [ "${CONTABASE_VERSION}" != "$ref_version" ]; then
    echo "Erro: TARGET_PROVENANCE_MISMATCH: versao solicitada (${CONTABASE_VERSION}) diverge do VERSION do alvo ${TARGET_OID} (${ref_version})." >&2
    return 1
  fi
  TARGET_VERSION="$ref_version"
  canonicalize_version "$TARGET_VERSION" >/dev/null 2>&1 || {
    echo "Erro: versao alvo invalida ou ambigua: ${TARGET_VERSION}" >&2
    return 1
  }
}

resolve_target_tag() {
  TARGET_TAG="${CONTABASE_VERSION:-}"
  [ -n "$TARGET_TAG" ] || {
    echo "Erro: modo tag exige CONTABASE_VERSION explicito." >&2
    return 1
  }
  canonicalize_version "$TARGET_TAG" >/dev/null 2>&1 || {
    echo "Erro: versao alvo invalida ou ambigua: ${TARGET_TAG}" >&2
    return 1
  }
  TARGET_REF="$TARGET_TAG"
}

fetch_target_tag() {
  local local_tag_oid
  HEAD_BEFORE_FETCH="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  STATUS_BEFORE_FETCH="$(git status --porcelain 2>/dev/null || true)"
  STATUS_BEFORE_FETCH="$(filter_control_status_paths "$STATUS_BEFORE_FETCH" "$(control_state_dir_rel)")"
  [ -z "$STATUS_BEFORE_FETCH" ] || {
    echo "Erro: worktree nao esta limpo antes do fetch da tag." >&2
    return 1
  }

  TARGET_REFS_TOUCHED=true
  if is_shallow_repo; then
    echo "Shallow clone detectado; expandindo historia antes da validacao de ancestralidade."
    if ! target_git fetch --unshallow --no-tags "$CONTABASE_OFFICIAL_ORIGIN" \
      "+refs/heads/*:${UNSHALLOW_REF_PREFIX}/heads/*" \
      "+refs/tags/*:${UNSHALLOW_REF_PREFIX}/tags/*"; then
      echo "Erro: falha ao expandir clone shallow." >&2
      return 1
    fi
    if is_shallow_repo; then
      echo "Erro: clone permaneceu shallow apos a expansao." >&2
      return 1
    fi
    assert_local_tags_unchanged || return 1
    assert_checkout_unchanged "$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  fi

  if ! target_git fetch --no-tags "$CONTABASE_OFFICIAL_ORIGIN" "+refs/tags/${TARGET_TAG}:${TEMP_TARGET_REF}"; then
    echo "Erro: tag alvo ausente ou inacessivel na origem oficial: ${TARGET_TAG}" >&2
    return 1
  fi
  assert_local_tags_unchanged || return 1
  assert_checkout_unchanged "$(git rev-parse --verify 'HEAD^{commit}')" || return 1

  [ "$(target_git cat-file -t "$TEMP_TARGET_REF" 2>/dev/null)" = "tag" ] \
    || [ "$(target_git cat-file -t "$TEMP_TARGET_REF" 2>/dev/null)" = "commit" ] || {
    echo "Erro: ref alvo nao e tag nem commit." >&2
    return 1
  }
  TARGET_OBJECT_TYPE="$(target_git cat-file -t "$TEMP_TARGET_REF")"
  TARGET_OID="$(target_git rev-parse --verify "${TEMP_TARGET_REF}^{commit}")" || {
    echo "Erro: nao foi possivel resolver a tag alvo para commit." >&2
    return 1
  }
  [[ "$TARGET_OID" =~ ^[0-9a-f]{40}$ ]] || {
    echo "Erro: OID alvo invalido." >&2
    return 1
  }

  local_tag_oid="$(git rev-parse --verify "refs/tags/${TARGET_TAG}^{commit}" 2>/dev/null || true)"
  if [ -n "$local_tag_oid" ] && [ "$local_tag_oid" != "$TARGET_OID" ]; then
    echo "Erro: tag local ${TARGET_TAG} diverge da tag remota; possivel tag movida." >&2
    return 1
  fi

  TARGET_VERSION="$(target_git show "${TARGET_OID}:VERSION" 2>/dev/null | tr -d '[:space:]')"
  [ -n "$TARGET_VERSION" ] || {
    echo "Erro: VERSION ausente no commit alvo." >&2
    return 1
  }
  canonicalize_version "$TARGET_VERSION" >/dev/null 2>&1 || {
    echo "Erro: VERSION alvo invalida: ${TARGET_VERSION}" >&2
    return 1
  }
  [ "$TARGET_VERSION" = "$TARGET_TAG" ] || {
    echo "Erro: VERSION do commit alvo (${TARGET_VERSION}) diverge da tag (${TARGET_TAG})." >&2
    return 1
  }
  echo "Tag alvo resolvida: ${TARGET_TAG} (${TARGET_OBJECT_TYPE}) -> ${TARGET_OID}."
}

ensure_fast_forward_possible() {
  if target_git merge-base --is-ancestor "$CURRENT_HEAD_OID" "$TARGET_OID"; then
    return 0
  fi
  echo "Erro: HEAD atual (${CURRENT_HEAD_OID}) nao e ancestral do alvo congelado (${TARGET_OID})." >&2
  echo "Resolva a divergencia manualmente antes de usar este script." >&2
  return 1
}

current_install_channel() {
  local persisted
  persisted="$(state_value "$STATE_FILE" CONTABASE_CHANNEL)"
  if [ -n "$persisted" ]; then
    printf '%s' "$persisted"
    return 0
  fi
  infer_install_channel "$TARGET_VERSION"
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
        *) echo "Erro: alvo ${TARGET_VERSION} nao pertence ao canal beta persistido." >&2; return 1 ;;
      esac
      ;;
    stable)
      case "$TARGET_VERSION" in
        *-*) echo "Erro: alvo ${TARGET_VERSION} nao pertence ao canal stable persistido." >&2; return 1 ;;
        *) return 0 ;;
      esac
      ;;
    *)
      echo "Erro: canal persistido invalido: $effective_channel" >&2
      return 1
      ;;
  esac
}

tag_mode_noop_proven() {
  local version="${CONTABASE_VERSION:-}" local_tag_oid
  canonicalize_version "$version" >/dev/null 2>&1 || return 1
  [ "$CHECKOUT_KIND" = "tag" ] || return 1
  [ "$version" = "${CONFIRMED_RUNTIME_VERSION:-}" ] || return 1
  [ -n "${CONFIRMED_RUNTIME_IMAGE_ID:-}" ] || return 1
  [ "$version" = "$(read_version_file 2>/dev/null || true)" ] || return 1
  [ "$CURRENT_HEAD_OID" = "${INSTALL_COMMIT:-}" ] || return 1
  [ -z "${CURRENT_BRANCH:-}" ] || return 1
  [ "$(state_value "$STATE_FILE" CONTABASE_INSTALLED_VERSION)" = "$version" ] || return 1
  local_tag_oid="$(git rev-parse --verify "refs/tags/${version}^{commit}" 2>/dev/null || true)"
  [ "$local_tag_oid" = "$CURRENT_HEAD_OID" ] || return 1
  [ ! -e "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ] || return 1
  [ ! -e "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || return 1
  return 0
}

finalize_marker_value() {
  local file="$1" key="$2"
  [ -f "$file" ] && [ ! -L "$file" ] || return 1
  awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$file"
}

write_finalize_marker() {
  local content
  [ -n "$TARGET_VERSION" ] && [ -n "$TARGET_OID" ] && [ -n "$CHECKOUT_KIND" ] && [ -n "$TARGET_REF" ] || {
    echo "Erro: dados insuficientes para o marcador de finalizacao." >&2
    return 1
  }
  [[ "$TARGET_OID" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "${CONFIRMED_RUNTIME_IMAGE_ID:-}" =~ ^[0-9a-f]{64}$ ]] || {
    echo "Erro: identidade da imagem do runtime nao comprovada para o marcador." >&2
    return 1
  }
  content="$(mktemp "${TMPDIR:-/tmp}/contabase-finalize-marker.XXXXXX")" || return 1
  {
    printf 'schema=1\n'
    printf 'method=docker\n'
    printf 'target_version=%s\n' "$TARGET_VERSION"
    printf 'target_kind=%s\n' "$CHECKOUT_KIND"
    printf 'target_ref=%s\n' "$TARGET_REF"
    printf 'target_commit=%s\n' "$TARGET_OID"
    printf 'previous_version=%s\n' "${PREVIOUS_VERSION:-desconhecida}"
    printf 'previous_commit=%s\n' "${CURRENT_HEAD_OID:-$TARGET_OID}"
    printf 'runtime_version=%s\n' "$TARGET_VERSION"
    printf 'runtime_image=%s\n' "$CONFIRMED_RUNTIME_IMAGE_ID"
  } > "$content" || { rm -f "$content"; return 1; }
  if ! install_pending_file "$content" "$FINALIZE_MARKER"; then
    rm -f "$content"
    return 1
  fi
  rm -f "$content"
}

validate_finalize_marker_structure() {
  local file="$FINALIZE_MARKER" size key value
  local keys="schema method target_version target_kind target_ref target_commit previous_version previous_commit runtime_version runtime_image"

  [ -f "$file" ] && [ ! -L "$file" ] || {
    echo "Erro: marcador de finalizacao ausente ou inseguro." >&2
    return 1
  }
  size="$(wc -c < "$file" | tr -d '[:space:]')" || return 1
  [ "$size" -le 4096 ] || {
    echo "Erro: marcador de finalizacao excessivamente grande." >&2
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
    echo "Erro: marcador de finalizacao invalido ou ambiguo." >&2
    return 1
  }

  [ "$(finalize_marker_value "$file" schema)" = "1" ] || return 1
  [ "$(finalize_marker_value "$file" method)" = "docker" ] || return 1
  [[ "$(finalize_marker_value "$file" target_commit)" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "$(finalize_marker_value "$file" previous_commit)" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "$(finalize_marker_value "$file" runtime_image)" =~ ^[0-9a-f]{64}$ ]] || return 1
  case "$(finalize_marker_value "$file" target_kind)" in
    tag|branch) ;;
    *) return 1 ;;
  esac

  for key in target_version target_ref previous_version runtime_version; do
    value="$(finalize_marker_value "$file" "$key")"
    case "$value" in
      ""|*[!A-Za-z0-9._/-]*) echo "Erro: campo inseguro no marcador: $key" >&2; return 1 ;;
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
  [ "$(finalize_marker_value "$file" runtime_image)" = "${CONFIRMED_RUNTIME_IMAGE_ID:-}" ] || return 1
  return 0
}

validate_finalize_marker_for_target() {
  validate_finalize_marker_structure || return 1
  finalize_marker_matches_target || {
    echo "Erro: marcador de finalizacao diverge do alvo confirmado." >&2
    return 1
  }
  return 0
}

transition_journal_value() {
  local file="$1" key="$2"
  [ -f "$file" ] && [ ! -L "$file" ] || return 1
  awk -F= -v key="$key" '$1 == key { print substr($0, index($0, "=") + 1); exit }' "$file"
}

# O journal e gravado antes do build e por isso nao registra runtime_image.
# A imagem candidata so pode ser vinculada depois do health, no
# finalize.pending. Sem marcador, o retry confirma apenas a imagem atual do
# runtime; nao existe comparacao persistida com o candidato anterior.
write_transition_journal() {
  local content marker_hash marker_version marker_commit state_version state_commit force_reset_value

  [ -n "$TARGET_VERSION" ] && [ -n "$TARGET_OID" ] && [ -n "$CHECKOUT_KIND" ] && [ -n "$TARGET_REF" ] \
    && [ -n "$INSTALL_ORIGIN" ] && [ -n "$CURRENT_HEAD_OID" ] || {
    echo "Erro: dados insuficientes para registrar a intencao da transicao." >&2
    return 1
  }
  [[ "$TARGET_OID" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "$CURRENT_HEAD_OID" =~ ^[0-9a-f]{40}$ ]] || return 1
  force_reset_value=0
  [ "$FORCE_RESET" = true ] && force_reset_value=1

  if [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ]; then
    validate_finalize_marker_structure || return 1
    marker_hash="$(git -C "$REPO_ROOT" hash-object --no-filters "$FINALIZE_MARKER")" || {
      echo "Erro: nao foi possivel identificar o marcador pendente." >&2
      return 1
    }
    [[ "$marker_hash" =~ ^[0-9a-f]{40}$ ]] || {
      echo "Erro: identidade do marcador pendente invalida." >&2
      return 1
    }
    marker_version="$(finalize_marker_value "$FINALIZE_MARKER" target_version)"
    marker_commit="$(finalize_marker_value "$FINALIZE_MARKER" target_commit)"
  elif [ -e "$FINALIZE_MARKER" ] || [ -L "$FINALIZE_MARKER" ]; then
    echo "Erro: marcador de finalizacao inseguro." >&2
    return 1
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

  content="$(mktemp "${TMPDIR:-/tmp}/contabase-transition-journal.XXXXXX")" || return 1
  {
    printf 'schema=2\n'
    printf 'expected_image=none\n'
    printf 'method=docker\n'
    printf 'target_version=%s\n' "$TARGET_VERSION"
    printf 'target_kind=%s\n' "$CHECKOUT_KIND"
    printf 'target_ref=%s\n' "$TARGET_REF"
    printf 'target_commit=%s\n' "$TARGET_OID"
    printf 'target_origin=%s\n' "$INSTALL_ORIGIN"
    printf 'channel=%s\n' "$(current_install_channel)"
    printf 'previous_checkout_version=%s\n' "${CHECKOUT_VERSION_BEFORE:-none}"
    printf 'previous_commit=%s\n' "$CURRENT_HEAD_OID"
    printf 'previous_state_version=%s\n' "$state_version"
    printf 'previous_state_commit=%s\n' "$state_commit"
    printf 'previous_marker_hash=%s\n' "$marker_hash"
    printf 'previous_marker_version=%s\n' "$marker_version"
    printf 'previous_marker_commit=%s\n' "$marker_commit"
    printf 'force_reset=%s\n' "$force_reset_value"
  } > "$content" || { rm -f "$content"; return 1; }
  if ! install_pending_file "$content" "$TRANSITION_JOURNAL"; then
    rm -f "$content"
    return 1
  fi
  rm -f "$content"
}

validate_transition_journal_structure() {
  local file="$TRANSITION_JOURNAL" size key value normalized_origin
  local keys="schema method target_version target_kind target_ref target_commit target_origin channel previous_checkout_version previous_commit previous_state_version previous_state_commit previous_marker_hash previous_marker_version previous_marker_commit force_reset"

  [ -f "$file" ] && [ ! -L "$file" ] || {
    echo "Erro: registro de transicao ausente ou inseguro." >&2
    return 1
  }
  size="$(wc -c < "$file" | tr -d '[:space:]')" || return 1
  [ "$size" -le 4096 ] || {
    echo "Erro: registro de transicao excessivamente grande." >&2
    return 1
  }
  case "$(transition_journal_value "$file" schema)" in
    1) ;;
    2) keys="$keys expected_image" ;;
    *) return 1 ;;
  esac
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
    echo "Erro: registro de transicao invalido ou ambiguo." >&2
    return 1
  }

  if [ "$(transition_journal_value "$file" schema)" = "2" ]; then
    value="$(transition_journal_value "$file" expected_image)"
    [ "$value" = "none" ] || [[ "$value" =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
  fi
  [ "$(transition_journal_value "$file" method)" = "docker" ] || return 1
  canonicalize_version "$(transition_journal_value "$file" target_version)" >/dev/null 2>&1 || return 1
  case "$(transition_journal_value "$file" target_kind)" in
    tag|branch) ;;
    *) return 1 ;;
  esac
  [[ "$(transition_journal_value "$file" target_commit)" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "$(transition_journal_value "$file" previous_commit)" =~ ^[0-9a-f]{40}$ ]] || return 1
  normalized_origin="$(state_normalize_origin "$(transition_journal_value "$file" target_origin)")" || return 1
  [ "$normalized_origin" = "$CONTABASE_OFFICIAL_ORIGIN" ] || return 1
  case "$(transition_journal_value "$file" channel)" in
    beta|stable|pinned) ;;
    *) return 1 ;;
  esac
  case "$(transition_journal_value "$file" force_reset)" in
    0|1) ;;
    *) return 1 ;;
  esac

  for key in target_version target_ref previous_checkout_version previous_state_version previous_marker_version; do
    value="$(transition_journal_value "$file" "$key")"
    case "$value" in
      ""|*[!A-Za-z0-9._/-]*) echo "Erro: campo inseguro no registro de transicao: $key" >&2; return 1 ;;
    esac
  done
  if [ "$(transition_journal_value "$file" target_kind)" = "tag" ]; then
    [ "$(transition_journal_value "$file" target_ref)" = "$(transition_journal_value "$file" target_version)" ] || return 1
  fi

  for key in previous_state_commit previous_marker_hash previous_marker_commit; do
    value="$(transition_journal_value "$file" "$key")"
    if [ "$value" != "none" ] && [[ ! "$value" =~ ^[0-9a-f]{40}$ ]]; then
      echo "Erro: identidade invalida no registro de transicao: $key" >&2
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
  local journal_version journal_image decision_rc=0

  [ "$(transition_journal_value "$TRANSITION_JOURNAL" target_origin)" = "$EFFECTIVE_ORIGIN" ] \
    && [ "$(transition_journal_value "$TRANSITION_JOURNAL" channel)" = "$(current_install_channel)" ] \
    && [ "$(transition_journal_value "$TRANSITION_JOURNAL" target_kind)" = "$CHECKOUT_KIND" ] || {
    echo "Erro: intencao anterior diverge da origem, canal ou tipo de checkout." >&2
    return 1
  }
  journal_version="$(transition_journal_value "$TRANSITION_JOURNAL" target_version)"
  journal_image="$(transition_journal_value "$TRANSITION_JOURNAL" expected_image)"
  version_decision "$journal_version" "$TARGET_VERSION" || decision_rc=$?
  [ "$decision_rc" = "0" ] || {
    echo "Erro: alvo $TARGET_VERSION nao supera a intencao anterior $journal_version." >&2
    return 1
  }

  if ! validate_finalize_marker_structure \
    || [ "$journal_version" != "$CONFIRMED_RUNTIME_VERSION" ] \
    || [ "$journal_image" != "sha256:$CONFIRMED_RUNTIME_IMAGE_ID" ] \
    || [ "$(finalize_marker_value "$FINALIZE_MARKER" runtime_image)" != "$CONFIRMED_RUNTIME_IMAGE_ID" ] \
    || [ "$(finalize_marker_value "$FINALIZE_MARKER" runtime_version)" != "$journal_version" ]; then
    echo "Erro: runtime e marcador nao confirmam a imagem da intencao anterior; supersessao recusada." >&2
    return 1
  fi
  local key
  for key in target_version target_kind target_ref target_commit; do
    [ "$(finalize_marker_value "$FINALIZE_MARKER" "$key")" = "$(transition_journal_value "$TRANSITION_JOURNAL" "$key")" ] || {
      echo "Erro: marcador pendente nao corresponde a intencao anterior." >&2
      return 1
    }
  done
}

record_expected_image() {
  local image="$1" existing content
  validate_transition_journal_structure && transition_journal_matches_target || return 1
  existing="$(transition_journal_value "$TRANSITION_JOURNAL" expected_image)"
  if [ "$existing" = "$image" ]; then
    durable_state_file confirm "$TRANSITION_JOURNAL"
    return
  fi
  [ "$existing" = "none" ] || return 1
  content="$(mktemp "${TMPDIR:-/tmp}/contabase-expected-image.XXXXXX")" || return 1
  if ! sed "s/^expected_image=none$/expected_image=$image/" "$TRANSITION_JOURNAL" > "$content" \
    || ! install_pending_file "$content" "$TRANSITION_JOURNAL"; then
    rm -f "$content"
    return 1
  fi
  rm -f "$content"
}

completed_install_matches_target() {
  [ "$CURRENT_HEAD_OID" = "$TARGET_OID" ] \
    && [ "$CHECKOUT_VERSION_BEFORE" = "$TARGET_VERSION" ] \
    && [ "$PERSISTED_VERSION" = "$TARGET_VERSION" ] \
    && [ "$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF_KIND)" = "$CHECKOUT_KIND" ] \
    && [ "$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF)" = "$TARGET_REF" ] \
    && [ "$(state_value "$STATE_FILE" CONTABASE_INSTALL_COMMIT)" = "$TARGET_OID" ] \
    && [ "$(state_value "$STATE_FILE" CONTABASE_INSTALL_ORIGIN)" = "$EFFECTIVE_ORIGIN" ]
}

validate_transition_journal_previous() {
  local previous_checkout previous_commit previous_state_version previous_state_commit
  local previous_marker_hash previous_marker_version previous_marker_commit
  local current_state_version current_state_commit current_marker_hash force_reset_journal head_now version_now

  [ -f "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || return 1
  previous_checkout="$(transition_journal_value "$TRANSITION_JOURNAL" previous_checkout_version)"
  previous_commit="$(transition_journal_value "$TRANSITION_JOURNAL" previous_commit)"
  previous_state_version="$(transition_journal_value "$TRANSITION_JOURNAL" previous_state_version)"
  previous_state_commit="$(transition_journal_value "$TRANSITION_JOURNAL" previous_state_commit)"
  previous_marker_hash="$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_hash)"
  previous_marker_version="$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_version)"
  previous_marker_commit="$(transition_journal_value "$TRANSITION_JOURNAL" previous_marker_commit)"
  force_reset_journal="$(transition_journal_value "$TRANSITION_JOURNAL" force_reset)"

  [ "${CHECKOUT_VERSION_BEFORE:-none}" = "$previous_checkout" ] || {
    echo "Erro: versao anterior do checkout diverge da intencao registrada." >&2
    return 1
  }
  if [ "${CURRENT_HEAD_OID:-}" != "$previous_commit" ]; then
    head_now="$(git rev-parse --verify 'HEAD^{commit}' 2>/dev/null || true)"
    version_now="$(read_version_file 2>/dev/null || true)"
    if ! { [ "$force_reset_journal" = "1" ] && [ "$CHECKOUT_KIND" = "branch" ] \
      && [ "$head_now" = "$TARGET_OID" ] && [ "$version_now" = "$TARGET_VERSION" ]; }; then
      echo "Erro: commit anterior do checkout diverge da intencao registrada." >&2
      return 1
    fi
  fi

  if [ "$previous_state_version" = "none" ]; then
    [ ! -e "$STATE_FILE" ] || {
      echo "Erro: estado de instalacao surgiu apos a intencao registrada." >&2
      return 1
    }
  else
    [ -f "$STATE_FILE" ] || {
      echo "Erro: estado de instalacao ausente para a intencao registrada." >&2
      return 1
    }
    current_state_version="$(state_value "$STATE_FILE" CONTABASE_INSTALLED_VERSION)"
    current_state_commit="$(state_value "$STATE_FILE" CONTABASE_INSTALL_COMMIT)"
    [ -n "$current_state_commit" ] || current_state_commit="none"
    [ "$current_state_version" = "$previous_state_version" ] \
      && [ "$current_state_commit" = "$previous_state_commit" ] || {
      echo "Erro: estado de instalacao diverge da intencao registrada." >&2
      return 1
    }
  fi

  if [ "$previous_marker_hash" = "none" ]; then
    [ ! -e "$FINALIZE_MARKER" ] || {
      echo "Erro: marcador pendente surgiu apos a intencao registrada." >&2
      return 1
    }
  else
    [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ] || {
      echo "Erro: marcador pendente ausente para a intencao registrada." >&2
      return 1
    }
    validate_finalize_marker_structure || return 1
    current_marker_hash="$(git -C "$REPO_ROOT" hash-object --no-filters "$FINALIZE_MARKER")" || return 1
    [ "$current_marker_hash" = "$previous_marker_hash" ] || {
      echo "Erro: marcador pendente foi alterado apos a intencao registrada." >&2
      return 1
    }
    [ "$(finalize_marker_value "$FINALIZE_MARKER" target_version)" = "$previous_marker_version" ] \
      && [ "$(finalize_marker_value "$FINALIZE_MARKER" target_commit)" = "$previous_marker_commit" ] || {
      echo "Erro: identidade do marcador pendente diverge da intencao registrada." >&2
      return 1
    }
  fi
  return 0
}

remove_transition_journal() {
  remove_pending_file "$TRANSITION_JOURNAL"
}

remove_finalize_marker() {
  remove_pending_file "$FINALIZE_MARKER"
}

validate_pending_marker_for_target() {
  local marker_version marker_kind decision_rc=0 runtime="${CONFIRMED_RUNTIME_VERSION:-}"
  local journal_present=false marker_present=false marker_matches=false
  local journal_previous_checkout="" journal_previous_marker="" journal_image=""

  if [ -e "$TRANSITION_JOURNAL" ] || [ -L "$TRANSITION_JOURNAL" ]; then
    [ -f "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || {
      echo "Erro: registro de transicao inseguro." >&2
      return 1
    }
    journal_present=true
    validate_transition_journal_structure || return 1
    if ! transition_journal_matches_target; then
      validate_transition_journal_supersession || return 1
      echo "Intencao anterior validada para supersessao."
      return 0
    fi
    journal_image="$(transition_journal_value "$TRANSITION_JOURNAL" expected_image)"
    if [ -z "$journal_image" ]; then
      echo "Erro: journal legado sem imagem esperada; preserve as evidencias e use recuperacao administrativa." >&2
      return 1
    fi
    if [ "$journal_image" != "none" ]; then
      EXPECTED_IMAGE_ID="$journal_image"
      if [ -n "$OBSERVED_RUNTIME_IMAGE_ID" ] && [ "$OBSERVED_RUNTIME_IMAGE_ID" != "$EXPECTED_IMAGE_ID" ]; then
        echo "Erro: imagem do runtime diverge da imagem esperada no journal." >&2
        return 1
      fi
    fi
  fi

  if [ -e "$FINALIZE_MARKER" ] || [ -L "$FINALIZE_MARKER" ]; then
    [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ] || {
      echo "Erro: marcador de finalizacao inseguro." >&2
      return 1
    }
    marker_present=true
    validate_finalize_marker_structure || return 1
    if finalize_marker_matches_target; then
      marker_matches=true
    fi
    if [ "$marker_matches" != true ]; then
      marker_version="$(finalize_marker_value "$FINALIZE_MARKER" target_version)"
      decision_rc=0
      version_decision "$marker_version" "$TARGET_VERSION" || decision_rc=$?
      case "$decision_rc" in
        0) ;;
        2)
          echo "Erro: marcador do mesmo alvo ${marker_version} diverge da identidade confirmada (imagem/ref/commit/runtime)." >&2
          echo "Nao trate divergencia de imagem do mesmo alvo como supersessao." >&2
          return 1
          ;;
        1)
          echo "Erro: alvo ${TARGET_VERSION} nao supera o marcador pendente ${marker_version}." >&2
          return 1
          ;;
        *)
          echo "Erro: versao do marcador pendente invalida: ${marker_version:-<vazia>}." >&2
          return 1
          ;;
      esac
    fi
  fi

  if [ "$marker_matches" = true ]; then
    if [ "$journal_present" = false ] && completed_install_matches_target; then
      EXPECTED_IMAGE_ID="sha256:$CONFIRMED_RUNTIME_IMAGE_ID"
    fi
    if [ "$EXPECTED_IMAGE_ID" != "sha256:$CONFIRMED_RUNTIME_IMAGE_ID" ]; then
      echo "Erro: marcador sem prova suficiente da imagem esperada; use recuperacao administrativa." >&2
      return 1
    fi
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
      if [ "$EXPECTED_IMAGE_ID" != "sha256:$CONFIRMED_RUNTIME_IMAGE_ID" ]; then
        echo "Erro: runtime alvo sem imagem esperada verificavel no journal; use recuperacao administrativa." >&2
        return 1
      fi
      if [ "$marker_present" = true ] || ! completed_install_matches_target; then
        validate_transition_journal_previous || return 1
      fi
      echo "Runtime $TARGET_VERSION confirmado com a imagem esperada no journal."
      return 0
    fi
    echo "Erro: runtime ${TARGET_VERSION} saudavel sem intencao de transicao persistida." >&2
    echo "Nao promova o checkout apenas pela versao do runtime." >&2
    echo "Preserve marcador, registro, estado e dados; use recuperacao administrativa." >&2
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
    echo "Erro: runtime ${runtime:-<ausente>} nao corresponde a transicao registrada." >&2
    return 1
  fi

  if [ "$marker_present" = true ]; then
    marker_version="$(finalize_marker_value "$FINALIZE_MARKER" target_version)"
    marker_kind="$(finalize_marker_value "$FINALIZE_MARKER" target_kind)"
    [ "$marker_kind" = "$CHECKOUT_KIND" ] || {
      echo "Erro: marcador pendente pertence a outro tipo de checkout." >&2
      return 1
    }
    if [ "$marker_version" = "$TARGET_VERSION" ]; then
      echo "Erro: runtime nao confirma o alvo pendente ${marker_version}." >&2
      return 1
    fi
    version_decision "$marker_version" "$TARGET_VERSION" || decision_rc=$?
    [ "$decision_rc" = "0" ] || {
      echo "Erro: alvo ${TARGET_VERSION} nao supera a finalizacao pendente ${marker_version}." >&2
      return 1
    }
    [ "$runtime" = "$marker_version" ] \
      && [ "$(finalize_marker_value "$FINALIZE_MARKER" runtime_version)" = "$runtime" ] \
      && [ "$(finalize_marker_value "$FINALIZE_MARKER" runtime_image)" = "$CONFIRMED_RUNTIME_IMAGE_ID" ] || {
      echo "Erro: runtime nao confirma a versao pendente ${marker_version}; estado ambiguo." >&2
      return 1
    }
    echo "Finalizacao pendente de ${marker_version} preservada; alvo ${TARGET_VERSION} a supera."
    return 0
  fi

  return 0
}

# validate_complete_state_provenance_local <state_file> <repo_root>
#   Valida proveniencia persistida completa de forma autoritativa para o
#   checkout instalado. Retorna 0 quando REF_KIND/REF/COMMIT/origem, HEAD,
#   VERSION e branch/tag persistida sao coerentes; 2 quando nao ha proveniencia;
#   1 quando ha proveniencia parcial ou divergente.
#   Nao relaxa HEAD, commit, origem, versao ou ref. As unicas diferencas em
#   relacao ao detector legado sao: branch sem upstream (fallback deterministico
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
    echo "Erro: nao foi possivel resolver HEAD." >&2
    return 1
  }
  [ -f VERSION ] && [ ! -L VERSION ] && [ -r VERSION ] || {
    echo "Erro: VERSION ausente ou inseguro no checkout." >&2
    return 1
  }
  checkout_version="$(tr -d '[:space:]' < VERSION)"
  [ -n "$checkout_version" ] || {
    echo "Erro: VERSION vazio no checkout." >&2
    return 1
  }
  CHECKOUT_VERSION_BEFORE="$checkout_version"

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
    echo "Finalizacao pendente detectada; validando estado/checkout de forma recuperavel."
  elif [ -e "$FINALIZE_MARKER" ] || [ -L "$FINALIZE_MARKER" ]; then
    echo "Erro: marcador de finalizacao inseguro." >&2
    return 1
  fi

  if [ -f "$STATE_FILE" ]; then
    state_validate_file "$STATE_FILE" || return 1
    state_repo="$(state_value "$STATE_FILE" CONTABASE_REPO_PATH)"
    [ -n "$state_repo" ] && [ -d "$state_repo" ] || {
      echo "Erro: CONTABASE_REPO_PATH ausente ou inacessivel." >&2
      return 1
    }
    state_canonical="$(cd "$state_repo" 2>/dev/null && pwd -P)" || {
      echo "Erro: checkout do estado inacessivel: $state_repo" >&2
      return 1
    }
    [ "$state_canonical" = "$REPO_ROOT" ] || {
      echo "Erro: CONTABASE_REPO_PATH diverge do checkout em uso." >&2
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
          echo "Erro: proveniencia persistida parcial durante finalizacao pendente." >&2
          return 1
        }
        state_origin="$(state_normalize_origin "$state_origin")" || {
          echo "Erro: origem persistida invalida durante finalizacao pendente." >&2
          return 1
        }
        [ "$state_origin" = "$EFFECTIVE_ORIGIN" ] || {
          echo "Erro: origem persistida diverge da origem efetiva durante finalizacao pendente." >&2
          return 1
        }
        state_rc=0
      else
        state_rc=2
      fi
    else
      state_rc=0
      state_err="$(provenance_validate_state "$STATE_FILE" "$REPO_ROOT" 2>&1)" || state_rc=$?
      if [ "$state_rc" -ne 0 ]; then
        # A proveniencia completa e autoritativa. O detector legado exige
        # upstream em branches e tag unica no HEAD; essas duas heuristicas nao
        # provam divergencia de identidade e podem ser relaxadas localmente,
        # sem aceitar HEAD, commit, origem, versao ou ref diferentes.
        local_rc=0
        validate_complete_state_provenance_local "$STATE_FILE" "$REPO_ROOT" || local_rc=$?
        if [ "$local_rc" = "0" ]; then
          state_rc=0
          echo "Proveniencia persistida completa validada localmente; heuristicas legadas de upstream/tag alias relaxadas."
        elif [ -n "$state_err" ]; then
          printf '%s\n' "$state_err" >&2
        fi
      fi
      case "$state_rc" in
        0|2) ;;
        *) echo "Erro: estado de instalacao invalido ou divergente." >&2; return 1 ;;
      esac
    fi
  fi

  if [ "$marker_pending" = true ]; then
    if [ "$marker_kind" = "tag" ] && [ ! -f "$STATE_FILE" ]; then
      echo "Erro: finalizacao pendente de tag sem estado de instalacao." >&2
      return 1
    fi
    CHECKOUT_KIND="$marker_kind"
    INSTALL_REF="$marker_ref"
    INSTALL_COMMIT="$marker_commit"
    if [ "$state_rc" = "0" ]; then
      STATE_PROVENANCE_COMPLETE=true
      [ "$state_kind" = "$marker_kind" ] || {
        echo "Erro: tipo persistido diverge do marcador de finalizacao." >&2
        return 1
      }
      [ "$state_origin" = "$EFFECTIVE_ORIGIN" ] || {
        echo "Erro: origem persistida diverge da origem efetiva." >&2
        return 1
      }
      INSTALL_ORIGIN="$EFFECTIVE_ORIGIN"
      if [ "$state_kind" = "branch" ]; then
        [ "$state_ref" = "$marker_ref" ] && [ "$CURRENT_BRANCH" = "$marker_ref" ] || {
          echo "Erro: branch persistida diverge do marcador de finalizacao." >&2
          return 1
        }
      fi
    else
      INSTALL_ORIGIN="$EFFECTIVE_ORIGIN"
    fi
    if [ "$CHECKOUT_KIND" = "tag" ]; then
      [ -z "$CURRENT_BRANCH" ] || {
        echo "Erro: marcador de tag em checkout de branch." >&2
        return 1
      }
      [ "$CURRENT_HEAD_OID" = "$INSTALL_COMMIT" ] || [ "$CURRENT_HEAD_OID" = "$state_commit" ] \
        || [ "$CURRENT_HEAD_OID" = "$marker_previous_commit" ] || {
        echo "Erro: HEAD nao corresponde ao estado nem ao alvo do marcador." >&2
        return 1
      }
    else
      [ "$CURRENT_BRANCH" = "$INSTALL_REF" ] || {
        echo "Erro: branch atual diverge do marcador de finalizacao." >&2
        return 1
      }
      [ "$CURRENT_HEAD_OID" = "$INSTALL_COMMIT" ] || [ "$CURRENT_HEAD_OID" = "$state_commit" ] \
        || [ "$CURRENT_HEAD_OID" = "$marker_previous_commit" ] || {
        echo "Erro: HEAD nao corresponde ao estado nem ao alvo do marcador." >&2
        return 1
      }
    fi
    TARGET_REF="$INSTALL_REF"
    echo "Finalizacao pendente aceita para kind=${CHECKOUT_KIND} ref=${INSTALL_REF}."
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
      echo "Erro: HEAD diverge do commit persistido." >&2
      return 1
    }
    [ "$INSTALL_ORIGIN" = "$EFFECTIVE_ORIGIN" ] || {
      echo "Erro: origem persistida diverge da origem efetiva." >&2
      return 1
    }
    if [ "$CHECKOUT_KIND" = "tag" ]; then
      [ -z "$CURRENT_BRANCH" ] || {
        echo "Erro: estado de tag em checkout de branch." >&2
        return 1
      }
    else
      [ "$CURRENT_BRANCH" = "$INSTALL_REF" ] || {
        echo "Erro: branch atual diverge da branch persistida." >&2
        return 1
      }
    fi
    TARGET_REF="$INSTALL_REF"
    echo "Proveniencia persistida completa aceita: kind=${CHECKOUT_KIND} ref=${INSTALL_REF} commit=${INSTALL_COMMIT} origin=${INSTALL_ORIGIN}."
    return 0
  fi

  provenance_detect "$REPO_ROOT" "$checkout_version" || return 1
  [ "$PROVENANCE_ORIGIN" = "$EFFECTIVE_ORIGIN" ] || {
    echo "Erro: origem detectada diverge da origem efetiva do checkout." >&2
    return 1
  }
  CHECKOUT_KIND="$PROVENANCE_KIND"
  INSTALL_REF="$PROVENANCE_REF"
  INSTALL_COMMIT="$PROVENANCE_COMMIT"
  INSTALL_ORIGIN="$EFFECTIVE_ORIGIN"
  echo "Estado legado sem proveniencia; usando deteccao local nao ambigua."

  if [ "$CHECKOUT_KIND" = "tag" ] && [ -n "$CURRENT_BRANCH" ]; then
    echo "Erro: tag exige checkout destacado." >&2
    return 1
  fi
  if [ "$CHECKOUT_KIND" = "tag" ] && [ ! -f "$STATE_FILE" ]; then
    echo "Erro: update por tag exige install-state.env legivel." >&2
    return 1
  fi
  TARGET_REF="$INSTALL_REF"
  echo "Proveniencia detectada: kind=${CHECKOUT_KIND} ref=${INSTALL_REF} commit=${INSTALL_COMMIT} origin=${INSTALL_ORIGIN}."
}

ensure_local_tag() {
  local local_oid
  [ "$CHECKOUT_KIND" = "tag" ] || return 0
  [ -n "$TARGET_TAG" ] && [ -n "$TARGET_OID" ] || return 1
  local_oid="$(git rev-parse --verify "refs/tags/${TARGET_TAG}^{commit}" 2>/dev/null || true)"
  if [ -n "$local_oid" ]; then
    [ "$local_oid" = "$TARGET_OID" ] || {
      echo "Erro: tag local ${TARGET_TAG} diverge do alvo confirmado." >&2
      return 1
    }
    return 0
  fi
  git tag "$TARGET_TAG" "$TARGET_OID" || {
    echo "Erro: nao foi possivel registrar a tag local ${TARGET_TAG}." >&2
    return 1
  }
}

resolve_checkout_version() {
  local checkout_version oid_version
  if [ ! -f VERSION ]; then
    echo "Erro: VERSION ausente no checkout apos a transicao." >&2
    return 1
  fi
  checkout_version="$(tr -d '[:space:]' < VERSION)"
  CHECKOUT_VERSION="$checkout_version"
  oid_version="$(git show "${TARGET_OID}:VERSION" 2>/dev/null | tr -d '[:space:]')"
  if [ "$CHECKOUT_VERSION" != "$TARGET_VERSION" ]; then
    echo "Erro: VERSION do checkout (${CHECKOUT_VERSION}) diverge do alvo (${TARGET_VERSION})." >&2
    return 1
  fi
  if [ "$oid_version" != "$TARGET_VERSION" ]; then
    echo "Erro: VERSION do OID congelado (${oid_version}) diverge do alvo (${TARGET_VERSION})." >&2
    return 1
  fi
  if [ -n "${CONTABASE_VERSION:-}" ] && [ "${CONTABASE_VERSION}" != "$TARGET_VERSION" ]; then
    echo "Erro: CONTABASE_VERSION (${CONTABASE_VERSION}) diverge do alvo (${TARGET_VERSION})." >&2
    return 1
  fi
  APP_VERSION="$CHECKOUT_VERSION"
  return 0
}

finalize_confirmed_update() {
  local head_oid journal_present=false marker_version="" decision_rc=0 context=operational
  [ "$NEW_RUNTIME_STARTED" = false ] || context=build

  enforce_clean_worktree || return 1
  [[ "$EXPECTED_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] \
    && [ "$EXPECTED_IMAGE_ID" = "sha256:$CONFIRMED_RUNTIME_IMAGE_ID" ] \
    && assert_effective_container_unchanged "$context" "$RESOLVED_CONTAINER_ID" \
    && [ "$(docker inspect "$RESOLVED_CONTAINER_ID" --format '{{.Image}}')" = "$EXPECTED_IMAGE_ID" ] || {
    echo "Erro: imagem esperada nao confirmada antes da finalizacao." >&2
    return 1
  }
  if [ -e "$TRANSITION_JOURNAL" ] || [ -L "$TRANSITION_JOURNAL" ]; then
    [ -f "$TRANSITION_JOURNAL" ] && [ ! -L "$TRANSITION_JOURNAL" ] || {
      echo "Erro: registro de transicao inseguro." >&2
      return 1
    }
    journal_present=true
    validate_transition_journal_structure || return 1
    transition_journal_matches_target || {
      echo "Erro: registro de transicao diverge do alvo confirmado." >&2
      return 1
    }
    [ "$(transition_journal_value "$TRANSITION_JOURNAL" expected_image)" = "$EXPECTED_IMAGE_ID" ] \
      && durable_state_file confirm "$TRANSITION_JOURNAL" || return 1
  fi

  if [ -f "$FINALIZE_MARKER" ] && [ ! -L "$FINALIZE_MARKER" ]; then
    if finalize_marker_matches_target; then
      validate_finalize_marker_for_target && durable_state_file confirm "$FINALIZE_MARKER" || return 1
    else
      validate_finalize_marker_structure || return 1
      marker_version="$(finalize_marker_value "$FINALIZE_MARKER" target_version)"
      decision_rc=0
      version_decision "$marker_version" "$TARGET_VERSION" || decision_rc=$?
      case "$decision_rc" in
        0) ;;
        2)
          echo "Erro: marcador do mesmo alvo ${marker_version} diverge da identidade confirmada; substituicao recusada." >&2
          return 1
          ;;
        1)
          echo "Erro: alvo ${TARGET_VERSION} nao supera o marcador pendente ${marker_version}; substituicao recusada." >&2
          return 1
          ;;
        *)
          echo "Erro: versao do marcador pendente invalida: ${marker_version:-<vazia>}." >&2
          return 1
          ;;
      esac
      [ "$journal_present" = true ] || {
        echo "Erro: marcador anterior sem intencao de transicao persistida." >&2
        echo "Preserve as evidencias e use recuperacao administrativa." >&2
        return 1
      }
      validate_transition_journal_previous || return 1
      [ "${CONFIRMED_RUNTIME_VERSION:-}" = "$TARGET_VERSION" ] || {
        echo "Erro: runtime nao confirma o alvo ${TARGET_VERSION} para substituir o marcador." >&2
        return 1
      }
      echo "Substituindo marcador de finalizacao anterior pelo alvo confirmado ${TARGET_VERSION}."
      write_finalize_marker || {
        echo "Erro: nao foi possivel substituir o marcador de finalizacao." >&2
        return 1
      }
    fi
  elif [ -e "$FINALIZE_MARKER" ] || [ -L "$FINALIZE_MARKER" ]; then
    echo "Erro: marcador de finalizacao inseguro." >&2
    return 1
  else
    [ "$journal_present" = true ] || {
      echo "Erro: finalizacao sem marcador ou intencao de transicao persistida." >&2
      echo "Preserve as evidencias e use recuperacao administrativa." >&2
      return 1
    }
    if ! completed_install_matches_target; then
      validate_transition_journal_previous || return 1
      echo "Marcador ausente; a imagem do runtime coincide com a imagem esperada no journal."
      write_finalize_marker || {
        echo "Erro: nao foi possivel persistir o marcador de finalizacao." >&2
        return 1
      }
    fi
  fi

  head_oid="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  if [ "$head_oid" != "$TARGET_OID" ]; then
    if [ "$CHECKOUT_KIND" = "tag" ]; then
      git checkout --detach "$TARGET_OID" || {
        echo "Erro: checkout destacado do alvo falhou; nenhum reset/checkout -f sera executado." >&2
        return 1
      }
    else
      git merge-base --is-ancestor "$head_oid" "$TARGET_OID" || {
        echo "Erro: checkout atual nao pode avancar em fast-forward para o runtime confirmado ${TARGET_OID}." >&2
        return 1
      }
      git merge --ff-only "$TARGET_OID" || return 1
    fi
  fi

  [ "$(git rev-parse --verify 'HEAD^{commit}')" = "$TARGET_OID" ] || {
    echo "Erro: HEAD apos finalizacao diverge do runtime confirmado ${TARGET_OID}." >&2
    return 1
  }
  ensure_local_tag || return 1
  resolve_checkout_version || return 1
  APP_VERSION="$TARGET_VERSION"
  backfill_update_command || return 1
  remove_transition_journal || {
    echo "Erro: nao foi possivel remover o registro de transicao." >&2
    return 1
  }
  remove_finalize_marker || {
    echo "Erro: nao foi possivel remover o marcador de finalizacao." >&2
    return 1
  }
  echo "Commit logico concluido: runtime, checkout e estado em ${TARGET_VERSION}."
}

create_canonical_build_snapshot() {
  local snapshot_version
  CANONICAL_BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-build-snapshot.XXXXXX")" || return 1
  if ! target_git archive --format=tar "$TARGET_OID" | tar -xf - -C "$CANONICAL_BUILD_DIR"; then
    echo "Erro: falha ao materializar o snapshot canonico do alvo ${TARGET_OID}."
    return 1
  fi
  if [ ! -f "$CANONICAL_BUILD_DIR/VERSION" ]; then
    echo "Erro: snapshot canonico sem VERSION."
    return 1
  fi
  snapshot_version="$(tr -d '[:space:]' < "$CANONICAL_BUILD_DIR/VERSION")"
  if [ "$snapshot_version" != "$TARGET_VERSION" ]; then
    echo "Erro: VERSION do snapshot (${snapshot_version}) diverge do alvo (${TARGET_VERSION})."
    return 1
  fi
  if [ ! -f "$CANONICAL_BUILD_DIR/Dockerfile" ]; then
    echo "Erro: snapshot canonico sem Dockerfile."
    return 1
  fi
  return 0
}

create_canonical_compose_override() {
  CANONICAL_COMPOSE_OVERRIDE="$(mktemp "${TMPDIR:-/tmp}/contabase-compose-override.XXXXXX")" || return 1
  CANDIDATE_BUILD_TAG="contabase-f2-local:$TARGET_OID-${CANONICAL_COMPOSE_OVERRIDE##*.}"
  cat > "$CANONICAL_COMPOSE_OVERRIDE" <<EOF
services:
  contabase:
    build:
      context: ${CANONICAL_BUILD_DIR}
      dockerfile: ${CANONICAL_BUILD_DIR}/Dockerfile
      args:
        VERSION: ${TARGET_VERSION}
      tags:
        - ${CANDIDATE_BUILD_TAG}
EOF
}

cleanup_update_tmp() {
  if [ -n "$DRY_GIT_DIR" ]; then
    rm -rf "$DRY_GIT_DIR"
  elif [ "$TARGET_REFS_TOUCHED" = true ]; then
    git -C "$REPO_ROOT" update-ref -d "$TEMP_TARGET_REF" 2>/dev/null || true
    git -C "$REPO_ROOT" for-each-ref --format='delete %(refname)' "$UNSHALLOW_REF_PREFIX" \
      | git -C "$REPO_ROOT" update-ref --stdin 2>/dev/null || true
  fi
  if [ -n "$TAGS_BEFORE_FILE" ] && [ -f "$TAGS_BEFORE_FILE" ]; then rm -f "$TAGS_BEFORE_FILE"; fi
  if [ -n "$RECOVERY_COMPOSE_FILE" ]; then rm -f "$RECOVERY_COMPOSE_FILE"; fi
  if [ -n "$CANONICAL_BACKUP_HELPER_TMP" ] && [ -f "$CANONICAL_BACKUP_HELPER_TMP" ]; then
    rm -f "$CANONICAL_BACKUP_HELPER_TMP"
  fi
  if [ -n "$ACTIVATION_COMPOSE_OVERRIDE" ]; then rm -f "$ACTIVATION_COMPOSE_OVERRIDE"; fi
  if [ -n "$CANONICAL_COMPOSE_OVERRIDE" ] && [ -f "$CANONICAL_COMPOSE_OVERRIDE" ]; then
    rm -f "$CANONICAL_COMPOSE_OVERRIDE"
  fi
  if [ -n "$CANONICAL_BUILD_DIR" ] && [ -d "$CANONICAL_BUILD_DIR" ]; then
    rm -rf "$CANONICAL_BUILD_DIR"
  fi
}
trap cleanup_update_tmp EXIT

infer_install_channel() {
  local version="$1"
  if [ -n "$INSTALL_CHANNEL" ]; then
    printf '%s' "$INSTALL_CHANNEL"
    return 0
  fi
  if [ -n "${CONTABASE_VERSION:-}" ]; then
    printf '%s' "pinned"
    return 0
  fi
  case "$version" in
    v*-beta.*) printf '%s' "beta" ;;
    v*.*.*)
      case "$version" in
        *-*) printf '%s' "pinned" ;;
        *) printf '%s' "stable" ;;
      esac
      ;;
    *) printf '%s' "pinned" ;;
  esac
}

# ==============================================================================
# Secure env.docker helpers (read-only for update — never writes to .env)
# ==============================================================================

env_load_value() {
  local key="$1"
  local file="${2:-.env.docker}"
  [ -f "$file" ] || { printf ''; return 0; }
  awk -v k="$key" 'index($0, k"=") == 1 { v = substr($0, length(k) + 2); gsub(/[[:space:]]/, "", v); print v; exit }' "$file"
}

env_is_placeholder() {
  local val="$1"
  [ -z "$val" ] && return 0
  case "$val" in
    __PREENCHA_*|__GERAR_COM_*|CHANGE_ME*|change_me*|REPLACE_ME*|replace_me*|YOUR_*|your_*|PLACEHOLDER*|placeholder*) return 0 ;;
    *) return 1 ;;
  esac
}

env_key_is_set() {
  local key="$1"
  local file="${2:-.env.docker}"
  local val
  [ -f "$file" ] || return 1
  val="$(env_load_value "$key" "$file")"
  [ -n "$val" ] || return 1
  env_is_placeholder "$val" && return 1
  return 0
}

env_set_if_missing() {
  local key="$1"
  local value="$2"
  local file="${3:-.env.docker}"

  if env_key_is_set "$key" "$file"; then
    return 0
  fi

  local tmp
  tmp="$(mktemp "${TMPDIR:-/tmp}/contabase-env.XXXXXX")"
  if grep -q "^${key}=" "$file" 2>/dev/null; then
    awk -F= -v k="$key" -v v="$value" '
      $1 == k { print k "=" v; next }
      { print }
    ' "$file" > "$tmp"
  else
    cp "$file" "$tmp"
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
  fi
  mv "$tmp" "$file"
}

env_backup() {
  local file="${1:-.env.docker}"
  [ -f "$file" ] || return 0
  ENV_BACKUP_PATH="${file}.pre-update-$(date +%Y%m%d-%H%M%S).bak"
  if cp "$file" "$ENV_BACKUP_PATH"; then
    if ! chmod 0600 "$ENV_BACKUP_PATH" 2>/dev/null; then
      echo "Erro: nao foi possivel restringir o backup ${ENV_BACKUP_PATH} para 0600." >&2
      return 1
    fi
    echo "Backup de ${file} criado em: ${ENV_BACKUP_PATH}"
    return 0
  fi
  echo "Aviso: nao foi possivel criar backup de ${file}."
  return 1
}

validate_env_secrets() {
  local file="${1:-.env.docker}"
  local missing=()
  local key

  # Update requires only persistent runtime secrets. CONTABASE_SETUP_TOKEN is a
  # one-time bootstrap token and may be removed/commented after initial setup.
  for key in AUTH_ENCRYPTION_KEY SECURITY_MASTER_KEY; do
    env_key_is_set "$key" "$file" || missing+=("$key")
  done

  [ "${#missing[@]}" -eq 0 ] && return 0

  echo ""
  echo "====================================================================="
  echo "ERRO: Secret(s) persistente(s) obrigatorio(s) ausente(s) ou com placeholder em ${file}:"
  for key in "${missing[@]}"; do
    echo "  - ${key}"
  done
  echo ""
  echo "Para resolver:"
  echo "  - Edite ${file} manualmente."
  echo "  - Gere valores fortes para AUTH_ENCRYPTION_KEY e SECURITY_MASTER_KEY."
  echo "  - Nao gere CONTABASE_SETUP_TOKEN para update normal; ele e usado apenas no setup inicial."
  echo "  - Execute o update novamente."
  echo "====================================================================="
  return 1
}

resolve_health_url() {
  local container_port health_port
  container_port="8080"
  if [ -f ".env.docker" ]; then
    container_port="$(awk -F= '$1 == "PORT" && $0 !~ /^#/ { print $2 }' .env.docker | tr -d '[:space:]' | head -n1)"
  fi
  container_port="${container_port:-8080}"

  health_port="$(docker compose $COMPOSE_ARGS port contabase "$container_port" 2>/dev/null | awk -F: '{print $NF}')"
  health_port="${health_port:-8080}"
  HEALTH_URL="http://127.0.0.1:${health_port}/health"
}

compose_container_ids() {
  local compose_context="$1"

  case "$compose_context" in
    operational)
      docker compose $COMPOSE_ARGS ps -q "$COMPOSE_SERVICE"
      ;;
    build)
      VERSION="$TARGET_VERSION" docker compose "${COMPOSE_BUILD_ARGS[@]}" ps -q "$COMPOSE_SERVICE"
      ;;
    *)
      echo "Erro: contexto Compose desconhecido ao resolver container: $compose_context."
      return 1
      ;;
  esac
}

resolve_effective_container() {
  local compose_context="$1"
  local allow_absent="${2:-false}"
  local ids line container_id="" count=0 actual_id service_label project_label working_dir_label resolved_working_dir

  RESOLVED_CONTAINER_ID=""
  RESOLVED_COMPOSE_PROJECT=""
  if ! ids="$(compose_container_ids "$compose_context" 2>/dev/null)"; then
    echo "Erro: nao foi possivel resolver o container do servico ${COMPOSE_SERVICE}."
    return 1
  fi

  while IFS= read -r line; do
    [ -n "$line" ] || continue
    count=$((count + 1))
    container_id="$line"
  done <<EOF
$ids
EOF

  if [ "$count" -eq 0 ]; then
    if [ "$allow_absent" = true ]; then
      return 0
    fi
    echo "Erro: o Compose nao retornou container para o servico ${COMPOSE_SERVICE}."
    return 1
  fi
  if [ "$count" -ne 1 ]; then
    echo "Erro: o Compose retornou ${count} containers para ${COMPOSE_SERVICE}; identidade ambigua."
    return 1
  fi

  actual_id="$(docker inspect "$container_id" --format '{{.Id}}' 2>/dev/null)" || {
    echo "Erro: nao foi possivel inspecionar o container ${container_id}."
    return 1
  }
  [ -n "$actual_id" ] && [ "$actual_id" = "$container_id" ] || {
    echo "Erro: identidade inspecionada do container diverge do Compose."
    return 1
  }

  service_label="$(docker inspect "$container_id" --format '{{ index .Config.Labels "com.docker.compose.service" }}' 2>/dev/null)" || return 1
  project_label="$(docker inspect "$container_id" --format '{{ index .Config.Labels "com.docker.compose.project" }}' 2>/dev/null)" || return 1
  [ "$service_label" = "<no value>" ] && service_label=""
  [ "$project_label" = "<no value>" ] && project_label=""
  [ "$service_label" = "$COMPOSE_SERVICE" ] || {
    echo "Erro: container ${container_id} declara servico Compose '${service_label:-ausente}', esperado '${COMPOSE_SERVICE}'."
    return 1
  }
  [ -n "$project_label" ] || {
    echo "Erro: container ${container_id} nao possui label de projeto Compose."
    return 1
  }

  working_dir_label="$(docker inspect "$container_id" --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' 2>/dev/null)" || return 1
  [ "$working_dir_label" = "<no value>" ] && working_dir_label=""
  if [ -n "$working_dir_label" ]; then
    [ -d "$working_dir_label" ] || {
      echo "Erro: working_dir Compose do container nao existe: $working_dir_label"
      return 1
    }
    resolved_working_dir="$(cd "$working_dir_label" && pwd -P)"
    [ "$resolved_working_dir" = "$REPO_ROOT" ] || {
      echo "Erro: container pertence a outro working_dir Compose: $resolved_working_dir"
      return 1
    }
  fi

  RESOLVED_CONTAINER_ID="$container_id"
  RESOLVED_COMPOSE_PROJECT="$project_label"
}

assert_effective_container_unchanged() {
  local compose_context="$1"
  local expected_id="$2"

  resolve_effective_container "$compose_context" false || return 1
  [ "$RESOLVED_CONTAINER_ID" = "$expected_id" ] || {
    echo "Erro: container efetivo mudou durante a verificacao (${expected_id} -> ${RESOLVED_CONTAINER_ID})."
    return 1
  }
}

validate_runtime_mounts() {
  local container_id="$1"
  local destinations destination

  destinations="$(docker inspect "$container_id" --format '{{range .Mounts}}{{println .Destination}}{{end}}' 2>/dev/null)" || {
    echo "Erro: nao foi possivel auditar mounts do container ${container_id}."
    return 1
  }

  while IFS= read -r destination; do
    [ -n "$destination" ] || continue
    case "$destination" in
      /app/data|/app/data/*)
        ;;
      /|/app|/app/VERSION|/app/server|/app/server/*|/app/admin|/app/admin/*|/app/templates|/app/templates/*|/app/assets|/app/assets/*)
        echo "Erro: mount operacional proibido shadowa runtime canonico em ${destination}."
        return 1
        ;;
    esac
  done <<EOF
$destinations
EOF
}

read_container_runtime_version() {
  local container_id="$1"
  local runtime_with_sentinel runtime_version sentinel=$'\034'

  if ! runtime_with_sentinel="$(docker exec "$container_id" cat /app/VERSION 2>/dev/null && printf '%s' "$sentinel")"; then
    echo "Erro: nao foi possivel ler /app/VERSION do container ${container_id}." >&2
    return 1
  fi
  case "$runtime_with_sentinel" in
    *"$sentinel") runtime_version="${runtime_with_sentinel%"$sentinel"}" ;;
    *) return 1 ;;
  esac
  case "$runtime_version" in
    *$'\n') runtime_version="${runtime_version%$'\n'}" ;;
  esac
  canonicalize_version "$runtime_version" >/dev/null 2>&1 || {
    echo "Erro: /app/VERSION ausente, vazia ou invalida no container ${container_id}." >&2
    return 1
  }
  printf '%s' "$runtime_version"
}

detect_confirmed_runtime_version() {
  local container_id runtime_version health docker_health restart_before restart_after image_before image_after
  CONFIRMED_RUNTIME_VERSION=""
  CONFIRMED_RUNTIME_IMAGE_ID=""
  OBSERVED_RUNTIME_IMAGE_ID=""

  resolve_effective_container operational true || return 1
  container_id="$RESOLVED_CONTAINER_ID"
  [ -n "$container_id" ] || return 0
  OBSERVED_RUNTIME_IMAGE_ID="$(docker inspect "$container_id" --format '{{.Image}}' 2>/dev/null)" || return 1
  validate_runtime_mounts "$container_id" || return 1
  docker_health="$(docker inspect "$container_id" --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" || return 1
  [ "$docker_health" = "healthy" ] || return 0
  resolve_health_url
  health="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null || true)"
  [ "$health" = '{"status":"healthy"}' ] || return 0
  restart_before="$(docker inspect "$container_id" --format '{{.RestartCount}}' 2>/dev/null)" || return 1
  image_before="$(docker inspect "$container_id" --format '{{.Image}}' 2>/dev/null)" || return 1
  runtime_version="$(read_container_runtime_version "$container_id")" || return 1
  assert_effective_container_unchanged operational "$container_id" || return 1
  docker_health="$(docker inspect "$container_id" --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" || return 1
  restart_after="$(docker inspect "$container_id" --format '{{.RestartCount}}' 2>/dev/null)" || return 1
  image_after="$(docker inspect "$container_id" --format '{{.Image}}' 2>/dev/null)" || return 1
  [ "$docker_health" = "healthy" ] && [ "$restart_after" = "$restart_before" ] || return 0
  [ "$OBSERVED_RUNTIME_IMAGE_ID" = "$image_before" ] && [ "$image_before" = "$image_after" ] || return 0
  [[ "$image_after" =~ ^sha256:[0-9a-f]{64}$ ]] || return 1

  CONFIRMED_RUNTIME_VERSION="$runtime_version"
  CONFIRMED_RUNTIME_IMAGE_ID="${image_after#sha256:}"
  echo "Runtime Docker saudavel comprova versao instalada ${CONFIRMED_RUNTIME_VERSION} (imagem ${CONFIRMED_RUNTIME_IMAGE_ID})."
}

confirm_target_runtime_identity() {
  local attempt max_attempts health container_id docker_health runtime_version restart_before restart_after image_before image_after
  max_attempts="${HEALTHCHECK_ATTEMPTS:-30}"
  resolve_effective_container build false || return 1
  container_id="$RESOLVED_CONTAINER_ID"
  validate_runtime_mounts "$container_id" || return 1
  resolve_health_url

  for attempt in $(seq 1 "$max_attempts"); do
    assert_effective_container_unchanged build "$container_id" || return 1
    docker_health="$(docker inspect "$container_id" --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" || {
      echo "Erro: falha ao consultar health do container ${container_id}."
      return 1
    }
    [ -n "$docker_health" ] || {
      echo "Erro: container ${container_id} nao possui Docker HEALTHCHECK observavel."
      return 1
    }

    if [ "$docker_health" = "healthy" ]; then
      health="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null || true)"
      if [ "$health" = '{"status":"healthy"}' ]; then
        restart_before="$(docker inspect "$container_id" --format '{{.RestartCount}}' 2>/dev/null)" || return 1
        image_before="$(docker inspect "$container_id" --format '{{.Image}}' 2>/dev/null)" || return 1
        runtime_version="$(read_container_runtime_version "$container_id")" || return 1
        [ "$runtime_version" = "$TARGET_VERSION" ] || {
          echo "Erro: runtime do container ${container_id} esta em ${runtime_version}, mas o alvo congelado e ${TARGET_VERSION}."
          return 1
        }
        assert_effective_container_unchanged build "$container_id" || return 1
        docker_health="$(docker inspect "$container_id" --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" || return 1
        restart_after="$(docker inspect "$container_id" --format '{{.RestartCount}}' 2>/dev/null)" || return 1
        image_after="$(docker inspect "$container_id" --format '{{.Image}}' 2>/dev/null)" || return 1
        [ "$docker_health" = "healthy" ] || {
          echo "Erro: container perdeu health durante a verificacao de identidade."
          return 1
        }
        [ "$restart_after" = "$restart_before" ] || {
          echo "Erro: container reiniciou durante a verificacao de identidade."
          return 1
        }
        [ "$image_before" = "$EXPECTED_IMAGE_ID" ] && [ "$image_after" = "$EXPECTED_IMAGE_ID" ] || {
          echo "Erro: imagem do container diverge da imagem candidata registrada."
          return 1
        }
        [[ "$image_after" =~ ^sha256:[0-9a-f]{64}$ ]] || {
          echo "Erro: identidade da imagem do container ${container_id} e invalida."
          return 1
        }
        health="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null || true)"
        [ "$health" = '{"status":"healthy"}' ] || {
          echo "Erro: health HTTP deixou de responder durante a verificacao de identidade."
          return 1
        }
        CONFIRMED_RUNTIME_VERSION="$runtime_version"
        CONFIRMED_RUNTIME_IMAGE_ID="${image_after#sha256:}"
        echo "Runtime Docker confirmado: projeto=${RESOLVED_COMPOSE_PROJECT} servico=${COMPOSE_SERVICE} container=${container_id} versao=${runtime_version} imagem=${CONFIRMED_RUNTIME_IMAGE_ID}."
        return 0
      fi
    fi

    echo "Aguardando identidade/health do runtime: tentativa ${attempt}/${max_attempts}..."
    sleep 2
  done

  return 1
}

capture_previous_runtime() {
  local container_id rollback_timestamp
  container_id="$(docker compose $COMPOSE_ARGS ps -q contabase 2>/dev/null || true)"
  if [ -z "$container_id" ]; then
    echo "Aviso: nenhum container anterior encontrado para rollback de imagem."
    return 0
  fi

  PREVIOUS_IMAGE_ID="$(docker inspect "$container_id" --format '{{.Image}}' 2>/dev/null || true)"
  PREVIOUS_IMAGE_NAME="$(docker inspect "$container_id" --format '{{.Config.Image}}' 2>/dev/null || true)"
  PREVIOUS_COMPOSE_ID="$(docker compose $COMPOSE_ARGS config | recovery_sha256)" || return 1

  if [ -n "$PREVIOUS_IMAGE_ID" ] && [ -n "$PREVIOUS_IMAGE_NAME" ]; then
    rollback_timestamp="$(date +%Y%m%d-%H%M%S)"
    PREVIOUS_ROLLBACK_TAG="contabase-rollback:pre-update-${rollback_timestamp}-$$"
    if ! docker image tag "$PREVIOUS_IMAGE_ID" "$PREVIOUS_ROLLBACK_TAG"; then
      PREVIOUS_ROLLBACK_TAG=""
      echo "Aviso: nao foi possivel criar a tag dedicada da imagem anterior."
      return 0
    fi
    echo "Imagem anterior preservada para rollback: $PREVIOUS_ROLLBACK_TAG ($PREVIOUS_IMAGE_ID)"
  else
    echo "Aviso: nao foi possivel identificar a imagem anterior para rollback."
  fi
}

validate_persistent_state() {
  local data_dir="$REPO_ROOT/data"
  local db="$REPO_ROOT/data/contabase.db"
  local uploads="$REPO_ROOT/data/uploads"

  STATE_PRESENT=false
  if [ -e "$data_dir" ] || [ -L "$data_dir" ]; then
    [ -d "$data_dir" ] && [ ! -L "$data_dir" ] && [ -r "$data_dir" ] && [ -x "$data_dir" ] || {
      echo "Erro: diretorio de dados invalido, ambiguo ou inacessivel: $data_dir"
      return 1
    }
    if ! find "$data_dir" -mindepth 1 -maxdepth 1 -print -quit >/dev/null; then
      echo "Erro: nao foi possivel enumerar com seguranca o diretorio de dados: $data_dir"
      return 1
    fi
  fi

  if [ -e "$db" ] || [ -L "$db" ]; then
    [ -f "$db" ] && [ ! -L "$db" ] && [ -r "$db" ] || {
      echo "Erro: banco existente e invalido, ambiguo ou ilegivel: $db"
      return 1
    }
    STATE_PRESENT=true
  elif [ -e "$uploads" ] || [ -L "$uploads" ]; then
    echo "Erro: uploads existem sem o banco esperado; estado persistente inconsistente: $uploads"
    return 1
  else
    echo "Nenhum estado persistente encontrado; update pode continuar sem pacote de backup."
    return 0
  fi

  if [ -e "$uploads" ] || [ -L "$uploads" ]; then
    [ -d "$uploads" ] && [ ! -L "$uploads" ] && [ -r "$uploads" ] && [ -x "$uploads" ] || {
      echo "Erro: diretorio de uploads invalido, ambiguo ou ilegivel: $uploads"
      return 1
    }
    if ! find "$uploads" -mindepth 1 -maxdepth 1 -print -quit >/dev/null; then
      echo "Erro: nao foi possivel enumerar com seguranca o diretorio de uploads: $uploads"
      return 1
    fi
  fi
  if [ "$PREVIOUS_VERSION" = "desconhecida" ] || [ "$PREVIOUS_VERSION" = "unknown" ]; then
    echo "Erro: nao foi possivel identificar a versao instalada antes do backup."
    return 1
  fi
}

prepare_canonical_backup_helper() {
  local target_blob actual_blob
  target_blob="$(target_git rev-parse "${TARGET_OID}:scripts/ops/backup.sh" 2>/dev/null)" || {
    echo "Erro: o alvo ${TARGET_OID} nao contem scripts/ops/backup.sh."
    return 1
  }
  CANONICAL_BACKUP_HELPER_TMP="$(mktemp "${TMPDIR:-/tmp}/contabase-backup-helper.XXXXXX")"
  if ! target_git show "${TARGET_OID}:scripts/ops/backup.sh" > "$CANONICAL_BACKUP_HELPER_TMP"; then
    echo "Erro: nao foi possivel materializar o helper canonico do alvo ${TARGET_OID}."
    return 1
  fi
  actual_blob="$(git hash-object "$CANONICAL_BACKUP_HELPER_TMP")"
  [ "$actual_blob" = "$target_blob" ] || {
    echo "Erro: identidade do helper materializado diverge do objeto Git alvo."
    return 1
  }
  chmod 0700 "$CANONICAL_BACKUP_HELPER_TMP"
  CANONICAL_BACKUP_HELPER="$CANONICAL_BACKUP_HELPER_TMP"
  echo "Helper canonico vinculado ao alvo ${TARGET_OID} (blob ${target_blob})."
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

create_stopped_backup() {
  local backup_root helper_output
  [ "$STATE_PRESENT" = true ] || return 0
  backup_root="$REPO_ROOT/data/backups"
  install -d -m 0700 "$backup_root"

  echo "Criando pacote canonico com a stack parada..."
  if ! helper_output="$(
    APP_VERSION="$PREVIOUS_VERSION" \
      DATA_DIR="$REPO_ROOT/data" \
      DB_FILE="$REPO_ROOT/data/contabase.db" \
      UPLOADS_DIR="$REPO_ROOT/data/uploads" \
      "$CANONICAL_BACKUP_HELPER" "$backup_root"
  )"; then
    echo "Erro: helper canonico de backup falhou."
    return 1
  fi
  BACKUP_PATH="$(printf '%s\n' "$helper_output" | tail -n1)"
  validate_backup_package || {
    echo "Erro: pacote canonico final ausente ou incompleto."
    return 1
  }
  echo "Backup pre-update criado: $BACKUP_PATH"
  echo "Backup ID: $BACKUP_ID"
  echo "Versao anterior registrada: $PREVIOUS_VERSION"
  echo "Pacote confirmado: database.sqlite + uploads/. Preserve-o ate concluir o update."
}

print_post_start_failure() {
  echo "A atualizacao NAO foi concluida."
  echo "O runtime antigo NAO foi reativado automaticamente."
  echo "O novo runtime foi parado; o banco pode ter sido migrado."
  echo "Estado/bundle que falhou foi preservado para diagnostico."
  echo "Pacote pre-update: ${BACKUP_PATH:-nao aplicavel (instalacao sem estado)}"
  if [ -n "$BACKUP_PATH" ]; then
    echo "Restore oficial, somente apos decisao explicita do operador:"
    echo "  CONFIRM_APP_STOPPED=yes DATA_DIR=$REPO_ROOT/data DB_FILE=$REPO_ROOT/data/contabase.db UPLOADS_DIR=$REPO_ROOT/data/uploads $REPO_ROOT/scripts/ops/restore.sh $BACKUP_PATH"
    echo "Aviso: o restore descarta alteracoes feitas depois desse backup."
  fi
}

rollback_runtime() {
  [ "${RECOVERY_COMPLETED:-false}" != true ] || return 0
  local override container_id image_id compose_id
  echo ""
  echo "=== Rollback do runtime Docker ==="
  echo "O banco e os uploads NAO serao restaurados automaticamente."
  docker compose $COMPOSE_ARGS down || { recovery_blocked; return 1; }
  STACK_STOPPED=true
  if [ "$NEW_RUNTIME_STARTED" = true ] \
    || [[ ! "$PREVIOUS_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] \
    || ! recovery_verify "$(dirname "$STATE_FILE")/recovery" docker "$PREVIOUS_VERSION" "${PREVIOUS_IMAGE_ID#sha256:}"; then
    recovery_blocked
    return 1
  fi
  RECOVERY_COMPOSE_FILE="$(mktemp "${TMPDIR:-/tmp}/contabase-recovery-config.XXXXXX")" || return 1
  if ! docker compose $COMPOSE_ARGS config > "$RECOVERY_COMPOSE_FILE"; then
    rm -f "$RECOVERY_COMPOSE_FILE"
    return 1
  fi
  compose_id="$(recovery_sha256 < "$RECOVERY_COMPOSE_FILE")" || return 1
  if [ -z "$PREVIOUS_COMPOSE_ID" ] || [ "$compose_id" != "$PREVIOUS_COMPOSE_ID" ]; then
    rm -f "$RECOVERY_COMPOSE_FILE"
    recovery_blocked
    return 1
  fi

  # Pin the content ID at the final activation point; mutable names are not proof.
  override="$(mktemp "${TMPDIR:-/tmp}/contabase-recovery-compose.XXXXXX")" || return 1
  printf 'services:\n  contabase:\n    image: %s\n    pull_policy: never\n' "$PREVIOUS_IMAGE_ID" > "$override" || { rm -f "$override"; return 1; }
  NEW_RUNTIME_STARTED=true
  if ! docker compose --project-directory "$REPO_ROOT" -f "$RECOVERY_COMPOSE_FILE" -f "$override" up -d --no-build --pull never --remove-orphans; then
    rm -f "$override"
    rm -f "$RECOVERY_COMPOSE_FILE"
    docker compose $COMPOSE_ARGS down || true
    return 1
  fi
  rm -f "$override"
  rm -f "$RECOVERY_COMPOSE_FILE"

  if detect_confirmed_runtime_version \
    && [ -n "$CONFIRMED_RUNTIME_VERSION" ] \
    && [ "$CONFIRMED_RUNTIME_VERSION" = "$PREVIOUS_VERSION" ] \
    && container_id="$RESOLVED_CONTAINER_ID" \
    && image_id="$(docker inspect "$container_id" --format '{{.Image}}')" \
    && [ "$image_id" = "$PREVIOUS_IMAGE_ID" ]; then
    STACK_STOPPED=false
    RECOVERY_COMPLETED=true
    echo "Rollback do runtime concluido: imagem anterior saudavel em $HEALTH_URL."
    echo "Codigo local pode permanecer atualizado; dados nao foram restaurados."
    return 0
  fi

  echo "Erro: rollback subiu a imagem anterior, mas o healthcheck continuou falhando."
  docker compose $COMPOSE_ARGS logs --tail 100 contabase || true
  docker compose $COMPOSE_ARGS down || true
  return 1
}

fail_with_rollback() {
  local message="$1"
  echo ""
  echo "ERRO: $message"

  if [ "$RUNTIME_CONFIRMED" = true ]; then
    echo "Runtime alvo confirmado e saudavel; mantendo-o ativo para finalizar checkout/estado no retry."
  elif [ "$NEW_RUNTIME_STARTED" = true ]; then
    docker compose $COMPOSE_ARGS down >/dev/null 2>&1 || true
    STACK_STOPPED=true
    print_post_start_failure
  elif [ "$CANDIDATE_BUILD_STARTED" = true ]; then
    echo "A stack permanece parada. A imagem anterior NAO foi reativada."
  elif [ "$STACK_STOPPED" = true ]; then
    if rollback_runtime; then
      echo "A instancia anterior foi recuperada, mas a atualizacao NAO foi concluida."
    else
      echo "O rollback automatico nao recuperou uma instancia saudavel."
    fi
  fi

  exit 1
}

handle_signal() {
  trap - INT TERM
  echo ""
  echo "Atualizacao interrompida por sinal."
  if [ "$RUNTIME_CONFIRMED" = true ]; then
    echo "Runtime alvo confirmado; checkout/estado serao finalizados na proxima execucao."
  elif [ "$NEW_RUNTIME_STARTED" = true ]; then
    docker compose $COMPOSE_ARGS down >/dev/null 2>&1 || true
    print_post_start_failure
  elif [ "$STACK_STOPPED" = true ]; then
    rollback_runtime || true
  fi
  exit 130
}

trap handle_signal INT TERM

PREVIOUS_VERSION="$(read_version_file 2>/dev/null || true)"
PREVIOUS_VERSION="${PREVIOUS_VERSION:-desconhecida}"

reject_unsafe_git_environment || exit 1
STATE_PYTHON="$(command -v python3)" || { echo "Erro: python3 obrigatorio para fsync." >&2; exit 1; }
STATE_DIR="$(dirname "$STATE_FILE")"
if [ "$DRY_RUN" = false ]; then
  ensure_state_write_access || exit 1
fi
if [ -d "$STATE_DIR" ]; then
  lock_state_directory "$STATE_DIR" || exit 1
fi
validate_install_method || exit 1
detect_checkout_provenance || exit 1
if [ "$CHECKOUT_KIND" = "branch" ]; then
  resolve_upstream || exit 1
fi
PREVIOUS_VERSION="$(read_version_file 2>/dev/null || true)"
PREVIOUS_VERSION="${PREVIOUS_VERSION:-desconhecida}"

echo "=== ContaBase — Atualização Docker ==="
echo ""
if [ "$CHECKOUT_KIND" = "tag" ]; then
  echo "Checkout: detached HEAD em tag oficial"
else
  echo "Branch atual: ${CURRENT_BRANCH:-<detached>}"
fi
echo "Commit atual: $(git log --oneline -1)"
echo "Referencia instalada: ${CHECKOUT_KIND}:${INSTALL_REF} (${INSTALL_COMMIT})"
echo "Versão anterior: $PREVIOUS_VERSION"
echo ""

# --- Escolher arquivo compose ---
COMPOSE_ARGS=""
if [ -n "$COMPOSE_FILE" ]; then
  COMPOSE_ARGS="-f $COMPOSE_FILE"
  echo "Compose explícito (modo avançado): $COMPOSE_FILE"
else
  echo "Usando arquivos de Compose padrão (docker-compose.yml e docker-compose.override.yml)"

  if [ -f "compose.local.yaml" ] || [ -f "compose.local.yml" ]; then
    echo "======================================================================"
    echo " AVISO: compose.local.yaml é um padrão legado."
    echo " Migre suas configurações para docker-compose.override.yml."
    echo " Este script agora usa o padrão nativo do Docker Compose."
    echo "======================================================================"
    echo ""
  fi
fi

# --- Validar compose ---
if ! docker compose $COMPOSE_ARGS config >/dev/null 2>&1; then
  echo "Erro: configurações do compose inválidas ou Docker indisponível."
  echo "Rode 'docker compose $COMPOSE_ARGS config' para ver detalhes."
  exit 1
fi

# --- Verificar .env.docker ---
ENV_FILE_DOCKER=""
if [ -f ".env.docker" ]; then
  ENV_FILE_DOCKER=".env.docker"
else
  echo "Erro: .env.docker nao encontrado."
  echo "Instalação Docker exige .env.docker."
  echo "Para instalação release/LXC use update-contabase-release.sh."
  echo "Crie com: cp .env.docker.example .env.docker"
  echo "Depois edite com suas configuracoes, incluindo CONTABASE_TIMEZONE se precisar mudar o fuso financeiro."
  exit 1
fi

# Validate secrets in existing env
echo "--- Validando secrets em ${ENV_FILE_DOCKER} ---"
if ! validate_env_secrets "$ENV_FILE_DOCKER"; then
  exit 1
fi
echo "Secrets validados: todos presentes e preenchidos."

detect_confirmed_runtime_version

if [ "$CHECKOUT_KIND" = "tag" ] && { [ "$FORCE_RESET" = true ] || [ "$SKIP_PULL" = true ]; }; then
  if tag_mode_noop_proven; then
    echo "No-op comprovado no modo tag: versao ${CONTABASE_VERSION}, OID ${CURRENT_HEAD_OID} e runtime saudavel ja confirmados."
    echo "--force-reset/--skip-pull nao executam fetch nem mutacao neste estado."
    exit 0
  fi
  if [ "$FORCE_RESET" = true ]; then
    echo "Erro: --force-reset e recusado em instalacoes por tag." >&2
  fi
  if [ "$SKIP_PULL" = true ]; then
    echo "Erro: --skip-pull e recusado em instalacoes por tag, salvo no-op explicitamente comprovado." >&2
  fi
  echo "Nenhum reset, rebuild ou startup foi executado." >&2
  exit 1
fi

show_dry_run() {
  echo "DRY RUN: preflight validado para $TARGET_VERSION ($TARGET_OID)."
  echo "NENHUMA ALTERACAO FOI FEITA."
  exit 0
}

# --- Resolucao do alvo e preflight Git ---
if [ "$SKIP_PULL" = true ] && [ "$FORCE_RESET" = true ]; then
  echo "Erro: --force-reset --skip-pull e uma combinacao contraditoria."
  exit 1
fi

if [ "$FORCE_RESET" = false ]; then
  enforce_clean_worktree || exit 1
fi
record_local_tags || exit 1
if [ "$DRY_RUN" = true ] && [ "$SKIP_PULL" = false ]; then
  DRY_GIT_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-dry-run.XXXXXX")" || exit 1
  git clone --quiet --shared --no-checkout -- "$REPO_ROOT" "$DRY_GIT_DIR" || exit 1
fi

COMMITS_AHEAD=0
if [ "$CHECKOUT_KIND" = "tag" ]; then
  resolve_target_tag || exit 1
  fetch_target_tag || exit 1
else
  [ -n "$CURRENT_BRANCH" ] || { echo "Erro: HEAD destacado nao e suportado para update de branch."; exit 1; }
  TARGET_REF="$CURRENT_BRANCH"
  if [ "$SKIP_PULL" = true ]; then
    TARGET_OID="$(git rev-parse --verify 'HEAD^{commit}')" || {
      echo "Erro: nao foi possivel resolver o HEAD atual."
      exit 1
    }
    echo "Alvo congelado sem fetch: $TARGET_OID"
  else
    echo "Buscando atualizações..."
    fetch_remote_state || exit 1
    freeze_target_oid || exit 1
  fi
  resolve_target_version || exit 1
  if [ "$SKIP_PULL" = false ]; then
    COMMITS_AHEAD=$(target_git rev-list HEAD.."$TARGET_OID" --count 2>/dev/null || echo "0")
    if [ "$COMMITS_AHEAD" -gt 0 ]; then
      echo ""
      echo "Commits pendentes: $COMMITS_AHEAD"
      target_git log --oneline HEAD.."$TARGET_OID"
    else
      echo "Nenhum commit novo. Prosseguindo apenas com rebuild."
    fi
  fi
fi
[ -n "$TARGET_OID" ] || { echo "Erro: TARGET_OID vazio."; exit 1; }
echo "Alvo congelado: $TARGET_OID"

validate_external_bundle_target || exit 1

resolve_current_version || exit 1
if [ "$CHECKOUT_KIND" = "tag" ] && [ "$TARGET_VERSION" = "$CURRENT_VERSION" ] \
  && [ "$RECOVERY_FINALIZE_ONLY" != true ] && [ "$TARGET_OID" != "$CURRENT_HEAD_OID" ]; then
  echo "Erro: mesma versao com OID divergente; tag possivelmente movida." >&2
  exit 1
fi
APP_VERSION="$TARGET_VERSION"
validate_target_channel || exit 1
if [ "$FORCE_RESET" = false ]; then
  ensure_fast_forward_possible || exit 1
fi
validate_pending_marker_for_target || exit 1
if [ -n "$EXPECTED_IMAGE_ID" ] && [ "$RECOVERY_FINALIZE_ONLY" = false ] \
  && [ "$(docker image inspect --format '{{.Id}}' -- "$EXPECTED_IMAGE_ID")" != "$EXPECTED_IMAGE_ID" ]; then
  echo "Erro: imagem esperada no journal indisponivel; use recuperacao administrativa." >&2
  exit 1
fi
if [ "$DRY_RUN" = true ]; then
  state_probe_dir="$STATE_DIR"
  while [ ! -d "$state_probe_dir" ]; do state_probe_dir="$(dirname "$state_probe_dir")"; done
  durable_state_file check "$state_probe_dir/.contabase-preflight" || exit 1
fi
enforce_version_policy

echo "Versao alvo: $TARGET_VERSION"
echo "Target OID: $TARGET_OID"
echo "Tipo de checkout: ${CHECKOUT_KIND}:${TARGET_REF}"

if [ "$RECOVERY_FINALIZE_ONLY" = true ]; then
  [ "$CONFIRMED_RUNTIME_VERSION" = "$TARGET_VERSION" ] || {
    echo "Erro: finalizacao de recovery sem runtime comprovado na versao alvo."
    exit 1
  }
  [ -n "$CONFIRMED_RUNTIME_IMAGE_ID" ] || {
    echo "Erro: finalizacao de recovery sem identidade de imagem comprovada."
    exit 1
  }
  if [ "$DRY_RUN" = true ]; then show_dry_run; fi
  RUNTIME_CONFIRMED=true
  echo "Finalizando transacao confirmada de execucao anterior."
  echo "Nenhum backup, rebuild, stop ou startup sera executado nesta finalizacao."
fi

if [ "$RECOVERY_FINALIZE_ONLY" = false ]; then
prepare_canonical_backup_helper || exit 1
validate_persistent_state || exit 1

# --- Snapshot canonico e override compose, sem avancar o checkout normal ---
if ! create_canonical_build_snapshot; then
  echo "Erro: falha ao materializar o snapshot canonico."
  exit 1
fi
if ! create_canonical_compose_override; then
  echo "Erro: falha ao criar o override compose canonico."
  exit 1
fi

COMPOSE_BUILD_FILES=()
if [ -n "$COMPOSE_FILE" ]; then
  COMPOSE_BUILD_FILES+=("$COMPOSE_FILE")
else
  [ -f "$CANONICAL_BUILD_DIR/docker-compose.yml" ] || {
    echo "Erro: snapshot canonico sem docker-compose.yml."
    exit 1
  }
  COMPOSE_BUILD_FILES+=("$CANONICAL_BUILD_DIR/docker-compose.yml")
  if [ -f "docker-compose.override.yml" ]; then
    COMPOSE_BUILD_FILES+=("docker-compose.override.yml")
  fi
fi
COMPOSE_BUILD_FILES+=("$CANONICAL_COMPOSE_OVERRIDE")
COMPOSE_BUILD_ARGS=("--project-directory" "$REPO_ROOT")
for f in "${COMPOSE_BUILD_FILES[@]}"; do
  COMPOSE_BUILD_ARGS+=("-f" "$f")
done

if ! CANDIDATE_IMAGE_REF="$(
  VERSION="$TARGET_VERSION" docker compose "${COMPOSE_BUILD_ARGS[@]}" config --format json |
    "$STATE_PYTHON" -I -B -c '
import json, sys
config = json.load(sys.stdin)
service = config["services"]["contabase"]
if not service.get("build"):
    sys.exit("Servico contabase sem build no Compose canonico.")
print(service.get("image") or config["name"] + "-contabase")
'
)" || [ -z "$CANDIDATE_IMAGE_REF" ]; then
  echo "Erro: Compose canonico do TARGET_OID ou referencia de imagem invalida."
  exit 1
fi

if [ "$DRY_RUN" = true ]; then show_dry_run; fi

# --- Confirmação ---
if [ "$FORCE_RESET" = true ]; then
  echo "ATENÇÃO: modo --force-reset ativado."
  echo "Será executado: git reset --hard $TARGET_OID + git clean -fd"
  echo "Seus arquivos LOCAIS (.env.docker, compose.local.yaml, data/) serão PRESERVADOS."
  echo "Demais arquivos modificados localmente podem ser PERDIDOS."
  echo ""
fi

RESUMO="Resumo:
  Checkout:     ${CHECKOUT_KIND}:${TARGET_REF}
  Branch:       ${CURRENT_BRANCH:-<detached>}
  Commits novos: $COMMITS_AHEAD
  Compose:      ${COMPOSE_FILE:-"docker-compose.yml + overrides"}
  Reset forçado: $FORCE_RESET
  Backup seguro: $( [ "$STATE_PRESENT" = true ] && echo "obrigatorio (pacote canonico, stack parada)" || echo "nao aplicavel (sem estado)" )
  Helper alvo:   $TARGET_OID
  Versao alvo:   $TARGET_VERSION
"

echo "$RESUMO"

if [ "$YIELD" = false ]; then
  read -rp "Prosseguir com a atualização? (s/N) " yn
  case "$yn" in
    [sS]*) ;;
    *) echo "Cancelado."; exit 0 ;;
  esac
fi
echo ""

durable_state_file probe "$TRANSITION_JOURNAL" || exit 1

# --- Preservar runtime, parar stack e criar backup consistente ---
echo "--- Preparando backup seguro ---"
capture_previous_runtime

STACK_STOPPED=true
if ! docker compose $COMPOSE_ARGS down; then
  fail_with_rollback "nao foi possivel parar a stack antes do backup."
fi

if ! create_stopped_backup; then
  echo "ERRO: backup canonico falhou; codigo nao sera atualizado e a stack permanecera parada."
  exit 1
fi

# A intencao duravel e gravada depois do backup e antes de qualquer mutacao de
# configuracao, bundle ou runtime.
echo "--- Registrando intencao de transicao ---"
if ! transition_journal_matches_target && ! write_transition_journal; then
  fail_with_rollback "nao foi possivel persistir a intencao de transicao."
fi
echo "Intencao de transicao persistida para ${TARGET_VERSION} (${CHECKOUT_KIND}:${TARGET_REF})."

# O arquivo de ambiente so pode ser alterado depois do pacote pre-update.
echo "--- Backup de ${ENV_FILE_DOCKER} ---"
if ! chmod 0600 "$ENV_FILE_DOCKER" 2>/dev/null; then
  if ! sudo chmod 0600 "$ENV_FILE_DOCKER" 2>/dev/null; then
    fail_with_rollback "nao foi possivel restringir ${ENV_FILE_DOCKER} para 0600."
  fi
fi
if ! env_backup "$ENV_FILE_DOCKER"; then
  fail_with_rollback "backup de ${ENV_FILE_DOCKER} falhou."
fi
if ! env_set_if_missing "CONTABASE_TIMEZONE" "America/Sao_Paulo" "$ENV_FILE_DOCKER"; then
  fail_with_rollback "ajuste de CONTABASE_TIMEZONE falhou."
fi

# --- Atualizar código ---
if [ "$FORCE_RESET" = true ]; then
  echo "--- Reset forçado para o OID congelado ---"

  if ! git reset --hard "$TARGET_OID"; then
    fail_with_rollback "reset do codigo falhou."
  fi

  # VACINA ANTI-AUTODELETE:
  # Impede o Git de apagar arquivos de ambiente, configurações locais, banco de dados, uploads e scripts de update do usuário.
  if ! git clean -fd \
    -e .env \
    -e .env.docker \
    -e compose.local.yaml \
    -e compose.local.yml \
    -e docker-compose.override.yml \
    -e docker-compose.override.yaml \
    -e data/ \
    -e backups/ \
    -e uploads/ \
    -e update.sh \
    -e update-contabase-docker.sh \
    -e finalize.pending \
    -e transition.pending \
    -e scripts/update-contabase-docker.sh; then
    fail_with_rollback "limpeza do codigo falhou."
  fi

  echo ""
  echo "Código atualizado para: $(git log --oneline -1)"
fi

if { [ "$FORCE_RESET" = true ] || [ "$SKIP_PULL" = true ]; } \
  && [ "$(git rev-parse --verify 'HEAD^{commit}')" != "$TARGET_OID" ]; then
  fail_with_rollback "HEAD diverge do OID congelado apos a transicao."
fi

APP_VERSION="$TARGET_VERSION"

echo "Versão anterior: $PREVIOUS_VERSION"
echo "Versão alvo do snapshot: $APP_VERSION"

# --- Rebuild ---
echo ""
echo "--- Preparando imagem candidata ---"
CANDIDATE_BUILD_STARTED=true
if [ -z "$EXPECTED_IMAGE_ID" ]; then
  if ! VERSION="$APP_VERSION" docker compose "${COMPOSE_BUILD_ARGS[@]}" build "$COMPOSE_SERVICE"; then
    fail_with_rollback "build da imagem candidata falhou."
  fi
  # The private build tag prevents a concurrent update of the regular Compose
  # tag from becoming the expected image recorded for this build.
  EXPECTED_IMAGE_ID="$(docker image inspect --format '{{.Id}}' -- "$CANDIDATE_BUILD_TAG")" || EXPECTED_IMAGE_ID=""
  if [ "$(docker image inspect --format '{{.Id}}' -- "$CANDIDATE_IMAGE_REF")" != "$EXPECTED_IMAGE_ID" ]; then
    fail_with_rollback "referencia Compose diverge da imagem produzida pelo build."
  fi
else
  CANDIDATE_BUILD_TAG=""
fi
if ! [[ "$EXPECTED_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] \
  || [ "$(docker image inspect --format '{{.Id}}' -- "$EXPECTED_IMAGE_ID")" != "$EXPECTED_IMAGE_ID" ] \
  || ! record_expected_image "$EXPECTED_IMAGE_ID"; then
  fail_with_rollback "nao foi possivel confirmar e persistir a imagem candidata antes do startup."
fi

ACTIVATION_COMPOSE_OVERRIDE="$(mktemp "${TMPDIR:-/tmp}/contabase-activate.XXXXXX")" || exit 1
printf 'services:\n  contabase:\n    image: %s\n    pull_policy: never\n' "$EXPECTED_IMAGE_ID" \
  > "$ACTIVATION_COMPOSE_OVERRIDE" || exit 1
COMPOSE_BUILD_ARGS+=("-f" "$ACTIVATION_COMPOSE_OVERRIDE")
NEW_RUNTIME_STARTED=true
if ! VERSION="$APP_VERSION" docker compose "${COMPOSE_BUILD_ARGS[@]}" up -d --no-build --pull never --remove-orphans; then
  fail_with_rollback "subida da imagem candidata falhou."
fi

if ! confirm_target_runtime_identity; then
  docker compose $COMPOSE_ARGS logs --tail 100 contabase || true
  fail_with_rollback "identidade ou health da nova imagem nao foi comprovado."
fi

[ "$CONFIRMED_RUNTIME_VERSION" = "$TARGET_VERSION" ] || {
  fail_with_rollback "runtime confirmado diverge da versao alvo antes da promocao."
}
RUNTIME_CONFIRMED=true
if ! write_finalize_marker; then
  fail_with_rollback "runtime alvo confirmado, mas marcador de finalizacao nao foi persistido; retry permanece seguro."
fi
if [ -n "$CANDIDATE_BUILD_TAG" ] \
  && [ "$(docker image inspect --format '{{.Id}}' -- "$CANDIDATE_IMAGE_REF")" = "$EXPECTED_IMAGE_ID" ]; then
  docker image rm "$CANDIDATE_BUILD_TAG" >/dev/null 2>&1 || true
fi
if recovery_source_known "$CANONICAL_BUILD_DIR"; then
  recovery_record "$(dirname "$STATE_FILE")/recovery" docker "$TARGET_VERSION" "${EXPECTED_IMAGE_ID#sha256:}" \
    || echo "Aviso: prova de recuperacao nao gravada; retorno automatico a esta imagem ficara bloqueado."
fi
STACK_STOPPED=false
if [ -n "$PREVIOUS_ROLLBACK_TAG" ]; then
  docker image rm "$PREVIOUS_ROLLBACK_TAG" >/dev/null 2>&1 || true
fi
fi

# ==============================================================================
# Global update command (backfill for existing installs)
# ==============================================================================

backfill_update_command() {
  local update_wrapper="${CONTABASE_UPDATE_WRAPPER:-/usr/local/bin/contabase-update}"
  local update_alias="${CONTABASE_UPDATE_ALIAS:-/usr/local/bin/cb-update}"
  local mode_file="${CONTABASE_MODE_FILE:-/etc/contabase/install-mode}"
  local repo_path installed_channel persisted_channel wrapper_source state_dir
  local provenance_kind provenance_ref provenance_commit provenance_origin
  local existing_kind existing_ref existing_origin current_commit
  repo_path="$REPO_ROOT"
  persisted_channel="$(state_value "$STATE_FILE" CONTABASE_CHANNEL)"
  installed_channel="${persisted_channel:-$(infer_install_channel "$APP_VERSION")}"
  existing_kind="$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF_KIND)"
  existing_ref="$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF)"
  existing_origin="$(state_value "$STATE_FILE" CONTABASE_INSTALL_ORIGIN)"
  current_commit=""
  if [ -n "$existing_kind" ]; then
    current_commit="$(git rev-parse --verify 'HEAD^{commit}' 2>/dev/null || true)"
  fi
  provenance_kind="$CHECKOUT_KIND"
  provenance_ref="$TARGET_REF"
  provenance_commit="$TARGET_OID"
  provenance_origin="$INSTALL_ORIGIN"
  if [ -z "$provenance_kind" ]; then
    provenance_kind="$existing_kind"
    provenance_ref="$existing_ref"
    provenance_commit="$current_commit"
    provenance_origin="$existing_origin"
  fi
  wrapper_source="${repo_path}/scripts/lib/contabase-update-wrapper.sh"
  state_dir="$(dirname "$STATE_FILE")"
  local has_priv=false

  if [ "$UPDATER_BUNDLE_EXTERNAL" != true ] \
    && { [ ! -f "$wrapper_source" ] || [ -L "$wrapper_source" ]; }; then
    echo "Erro: dispatcher autenticado ausente ou inseguro: $wrapper_source" >&2
    echo "Nenhum contabase-update foi instalado ou substituido." >&2
    return 1
  fi

  if [ "$(id -u)" -eq 0 ]; then
    has_priv=true
  elif command -v sudo >/dev/null 2>&1; then
    has_priv=true
  fi

  if [ "$has_priv" = false ]; then
    echo ""
    echo "Erro: permissao root/sudo e obrigatoria para confirmar o estado instalado."
    return 1
  fi

  run_priv() {
    if [ "$(id -u)" -eq 0 ]; then
      "$@"
    else
      sudo "$@"
    fi
  }

  commit_install_state() {
    local state_tmp
    state_tmp="$(mktemp "${TMPDIR:-/tmp}/contabase-install-state.XXXXXX")" || return 1
    if ! state_render "$STATE_FILE" "docker" "$installed_channel" "$APP_VERSION" "$repo_path" \
      "$provenance_kind" "$provenance_ref" "$provenance_commit" "$provenance_origin" > "$state_tmp"; then
      rm -f "$state_tmp"
      echo "Erro: estado de instalacao invalido para $STATE_FILE."
      return 1
    fi
    if ! install_pending_file "$state_tmp" "$STATE_FILE"; then
      rm -f "$state_tmp"
      echo "Erro: nao foi possivel confirmar $STATE_FILE."
      return 1
    fi
    rm -f "$state_tmp"
  }

  if [ "$UPDATER_BUNDLE_EXTERNAL" = true ]; then
    [ -n "$VERIFIED_WRAPPER_PATH" ] && [ -n "$VERIFIED_WRAPPER_SHA256" ] || {
      echo "Erro: wrapper verificado ausente no bundle autenticado." >&2
      return 1
    }
    bundle_path_unsafe "$VERIFIED_WRAPPER_PATH" && {
      echo "Erro: caminho do wrapper verificado contem symlink ou componente inseguro." >&2
      return 1
    }
    [ -f "$VERIFIED_WRAPPER_PATH" ] && [ ! -L "$VERIFIED_WRAPPER_PATH" ] || {
      echo "Erro: wrapper verificado ausente ou inseguro." >&2
      return 1
    }
    [ "$(sha256_file "$VERIFIED_WRAPPER_PATH")" = "$VERIFIED_WRAPPER_SHA256" ] || {
      echo "Erro: wrapper verificado diverge do hash autorizado." >&2
      return 1
    }
  fi

  if ! run_priv mkdir -p "$state_dir" 2>/dev/null; then
    echo "Erro: nao foi possivel criar ${state_dir} (sudo necessario)."
    return 1
  fi

  if ! printf 'docker\n%s\n' "$repo_path" | run_priv tee "$mode_file" >/dev/null 2>/dev/null; then
    echo "Erro: nao foi possivel escrever $mode_file."
    return 1
  fi
  run_priv chmod 0644 "$mode_file" 2>/dev/null || true

  if [ "$UPDATER_BUNDLE_EXTERNAL" = true ]; then
    [ "$(sha256_file "$VERIFIED_WRAPPER_PATH")" = "$VERIFIED_WRAPPER_SHA256" ] || {
      echo "Erro: wrapper verificado foi alterado antes da instalacao." >&2
      return 1
    }
    run_priv install -m 0755 "$VERIFIED_WRAPPER_PATH" "$update_wrapper" 2>/dev/null || {
      echo "Erro: nao foi possivel instalar o wrapper verificado." >&2
      return 1
    }
    if [ ! -e "$update_alias" ]; then
      run_priv ln -s "$(basename "$update_wrapper")" "$update_alias" 2>/dev/null || true
    fi
    commit_install_state || return 1
    echo "OK: contabase-update atualizado."
    return 0
  fi

  run_priv install -m 0755 "$wrapper_source" "$update_wrapper" 2>/dev/null || {
    echo "Erro: nao foi possivel instalar $update_wrapper."
    return 1
  }
  if [ ! -e "$update_alias" ]; then
    run_priv ln -s "$(basename "$update_wrapper")" "$update_alias" 2>/dev/null || true
  fi
  commit_install_state || return 1
  echo "OK: contabase-update atualizado."
  return 0
}

if ! finalize_confirmed_update; then
  fail_with_rollback "runtime alvo esta saudavel, mas checkout/estado nao foram finalizados; retry permanece seguro."
fi

echo ""
echo "Healthcheck saudavel: $HEALTH_URL"
if [ "$RECOVERY_FINALIZE_ONLY" = true ]; then
  echo "Backup preventivo: ${BACKUP_PATH:-nao recriado nesta finalizacao}"
  echo "Backup ID:         ${BACKUP_ID:-nao aplicavel}"
  echo "Backup .env:       ${ENV_BACKUP_PATH:-nao recriado nesta finalizacao}"
else
  echo "Backup preventivo: ${BACKUP_PATH:-nao criado}"
  echo "Backup ID:         ${BACKUP_ID:-nao aplicavel}"
  echo "Backup .env:       ${ENV_BACKUP_PATH:-nao criado}"
fi
echo "Commit logico concluido: runtime, checkout e estado em ${APP_VERSION}."
echo "=== Atualizacao concluida ==="

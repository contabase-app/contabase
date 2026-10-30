#!/usr/bin/env bash
set -euo pipefail

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
#   --compose ARQUIVO  Usar compose específico em vez da busca automática.
#   --skip-pull        Recompilar sem atualizar Git (útil para testes locais).
#   --help             Mostrar esta ajuda.
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
TARGET_VERSION=""
TARGET_OID=""
CURRENT_VERSION=""
STATE_PRESENT=false
NEW_RUNTIME_STARTED=false
CANONICAL_BUILD_DIR=""
CANONICAL_COMPOSE_OVERRIDE=""
HEALTH_URL=""
ENV_BACKUP_PATH=""
CHECKOUT_VERSION_BEFORE=""
PERSISTED_VERSION=""
CONFIRMED_RUNTIME_VERSION=""
RECOVERY_FINALIZE_ONLY=false
RUNTIME_CONFIRMED=false
COMPOSE_SERVICE="contabase"
RESOLVED_CONTAINER_ID=""
RESOLVED_COMPOSE_PROJECT=""
STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-}"
REPO_ROOT="$(pwd -P)"
. "$(dirname "${BASH_SOURCE[0]}")/lib/contabase-recovery.sh"
PREVIOUS_COMPOSE_ID=""
RECOVERY_COMPOSE_FILE=""

# Accept CONTABASE_ASSUME_YES=1 as headless mode (same as --yes)
[ "${CONTABASE_ASSUME_YES:-0}" = "1" ] && YIELD=true

show_help() {
  sed -n '1,/^$/p' "$0" | grep '^#' | sed 's/^# //;s/^#$//'
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

if [ ! -d .git ]; then
  echo "Erro: execute este script dentro do repositório ContaBase."
  exit 1
fi

read_version_file() {
  if [ ! -f VERSION ]; then
    return 1
  fi

  tr -d '[:space:]' < VERSION
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
      && { [ "$persisted" != "$CURRENT_VERSION" ] || [ "$CHECKOUT_VERSION_BEFORE" != "$CURRENT_VERSION" ]; }; then
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

enforce_clean_worktree() {
  local staged tracked untracked
  staged="$(git diff --cached --name-only 2>/dev/null || true)"
  tracked="$(git diff --name-only 2>/dev/null || true)"
  untracked="$(git ls-files --others --exclude-standard 2>/dev/null || true)"

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

create_canonical_build_snapshot() {
  local snapshot_version
  CANONICAL_BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-build-snapshot.XXXXXX")" || return 1
  if ! git archive --format=tar "$TARGET_OID" | tar -xf - -C "$CANONICAL_BUILD_DIR"; then
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
  cat > "$CANONICAL_COMPOSE_OVERRIDE" <<EOF
services:
  contabase:
    build:
      context: ${CANONICAL_BUILD_DIR}
      dockerfile: ${CANONICAL_BUILD_DIR}/Dockerfile
      args:
        VERSION: ${TARGET_VERSION}
EOF
}

cleanup_update_tmp() {
  if [ -n "$RECOVERY_COMPOSE_FILE" ]; then rm -f "$RECOVERY_COMPOSE_FILE"; fi
  if [ -n "$CANONICAL_BACKUP_HELPER_TMP" ] && [ -f "$CANONICAL_BACKUP_HELPER_TMP" ]; then
    rm -f "$CANONICAL_BACKUP_HELPER_TMP"
  fi
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
  tmp="$(mktemp)"
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
  local container_id runtime_version health docker_health restart_before restart_after
  CONFIRMED_RUNTIME_VERSION=""

  resolve_effective_container operational true || return 1
  container_id="$RESOLVED_CONTAINER_ID"
  [ -n "$container_id" ] || return 0
  validate_runtime_mounts "$container_id" || return 1
  docker_health="$(docker inspect "$container_id" --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" || return 1
  [ "$docker_health" = "healthy" ] || return 0
  resolve_health_url
  health="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null || true)"
  [ "$health" = '{"status":"healthy"}' ] || return 0
  restart_before="$(docker inspect "$container_id" --format '{{.RestartCount}}' 2>/dev/null)" || return 1
  runtime_version="$(read_container_runtime_version "$container_id")" || return 1
  assert_effective_container_unchanged operational "$container_id" || return 1
  docker_health="$(docker inspect "$container_id" --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" || return 1
  restart_after="$(docker inspect "$container_id" --format '{{.RestartCount}}' 2>/dev/null)" || return 1
  [ "$docker_health" = "healthy" ] && [ "$restart_after" = "$restart_before" ] || return 0

  CONFIRMED_RUNTIME_VERSION="$runtime_version"
  echo "Runtime Docker saudavel comprova versao instalada ${CONFIRMED_RUNTIME_VERSION}."
}

confirm_target_runtime_identity() {
  local attempt max_attempts health container_id docker_health runtime_version restart_before restart_after
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
        runtime_version="$(read_container_runtime_version "$container_id")" || return 1
        [ "$runtime_version" = "$TARGET_VERSION" ] || {
          echo "Erro: runtime do container ${container_id} esta em ${runtime_version}, mas o alvo congelado e ${TARGET_VERSION}."
          return 1
        }
        assert_effective_container_unchanged build "$container_id" || return 1
        docker_health="$(docker inspect "$container_id" --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null)" || return 1
        restart_after="$(docker inspect "$container_id" --format '{{.RestartCount}}' 2>/dev/null)" || return 1
        [ "$docker_health" = "healthy" ] || {
          echo "Erro: container perdeu health durante a verificacao de identidade."
          return 1
        }
        [ "$restart_after" = "$restart_before" ] || {
          echo "Erro: container reiniciou durante a verificacao de identidade."
          return 1
        }
        health="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null || true)"
        [ "$health" = '{"status":"healthy"}' ] || {
          echo "Erro: health HTTP deixou de responder durante a verificacao de identidade."
          return 1
        }
        CONFIRMED_RUNTIME_VERSION="$runtime_version"
        echo "Runtime Docker confirmado: projeto=${RESOLVED_COMPOSE_PROJECT} servico=${COMPOSE_SERVICE} container=${container_id} versao=${runtime_version}."
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
  target_blob="$(git rev-parse "${TARGET_OID}:scripts/ops/backup.sh" 2>/dev/null)" || {
    echo "Erro: o alvo ${TARGET_OID} nao contem scripts/ops/backup.sh."
    return 1
  }
  CANONICAL_BACKUP_HELPER_TMP="$(mktemp "${TMPDIR:-/tmp}/contabase-backup-helper.XXXXXX")"
  if ! git show "${TARGET_OID}:scripts/ops/backup.sh" > "$CANONICAL_BACKUP_HELPER_TMP"; then
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
  install -d -m 0750 "$backup_root"

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
CHECKOUT_VERSION_BEFORE="$PREVIOUS_VERSION"

echo "=== ContaBase — Atualização Docker ==="
echo ""
echo "Branch atual: $(git branch --show-current)"
echo "Commit atual: $(git log --oneline -1)"
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

# Dry-run: validate everything, simulate, exit without changes
# Must run BEFORE env_backup and git fetch to guarantee zero mutations.
if [ "$DRY_RUN" = true ]; then
  echo ""
  echo "====================================================================="
  echo "DRY RUN — nenhuma alteracao sera feita."
  echo "====================================================================="
  echo ""
  echo "Validacoes concluidas:"
  echo "  - Docker e Docker Compose disponiveis"
  echo "  - ${ENV_FILE_DOCKER} presente e secrets validados"
  echo "  - docker compose config valido"
  echo ""
  echo "O que seria feito:"
  echo "  1. Materializar o helper canonico vinculado ao alvo Git"
  echo "  2. Parar a stack e criar o pacote canonico pre-update"
  echo "  3. Backup e ajuste compativel do .env.docker"
  echo "  4. Materializar snapshot canonico e override compose sem avancar o checkout normal"
  echo "  5. Reconstruir e subir containers a partir do snapshot"
  echo "  6. Validar healthcheck"
  echo "  7. Finalizar checkout e estado instalado"
  echo "  8. Antes do health: rollback de runtime; depois: preservar runtime saudavel para retry"
  echo ""
  echo "NENHUMA ALTERACAO FOI FEITA."
  exit 0
fi

if [ "$(id -u)" -ne 0 ] && ! command -v sudo >/dev/null 2>&1; then
  echo "Erro: root ou sudo e obrigatorio para confirmar estado e comando global do update."
  exit 1
fi

# --- Fetch único e congelamento do OID ---
if [ "$SKIP_PULL" = true ] && [ "$FORCE_RESET" = true ]; then
  echo "Erro: --force-reset --skip-pull e uma combinacao contraditoria."
  exit 1
fi

CURRENT_BRANCH="$(git branch --show-current)"
[ -n "$CURRENT_BRANCH" ] || { echo "Erro: HEAD destacado nao e suportado para update."; exit 1; }

if [ "$SKIP_PULL" = false ]; then
  if [ "$FORCE_RESET" = false ]; then
    enforce_clean_worktree || exit 1
  fi
  echo "Buscando atualizações..."
  git fetch --no-tags origin "refs/heads/${CURRENT_BRANCH}"
  TARGET_OID="$(git rev-parse --verify 'FETCH_HEAD^{commit}')" || {
    echo "Erro: nao foi possivel congelar o alvo a partir do FETCH_HEAD."
    exit 1
  }
  COMMITS_AHEAD=$(git rev-list HEAD.."$TARGET_OID" --count 2>/dev/null || echo "0")
  if [ "$COMMITS_AHEAD" -gt 0 ]; then
    echo ""
    echo "Commits pendentes: $COMMITS_AHEAD"
    git log --oneline HEAD.."$TARGET_OID"
  else
    echo "Nenhum commit novo. Prosseguindo apenas com rebuild."
  fi
else
  enforce_clean_worktree || exit 1
  TARGET_OID="$(git rev-parse --verify 'HEAD^{commit}')" || {
    echo "Erro: nao foi possivel resolver o HEAD atual."
    exit 1
  }
  COMMITS_AHEAD=0
fi
[ -n "$TARGET_OID" ] || { echo "Erro: TARGET_OID vazio."; exit 1; }
echo "Alvo congelado: $TARGET_OID"

TARGET_REF="$TARGET_OID"
TARGET_VERSION="$(git show "${TARGET_OID}:VERSION" 2>/dev/null | tr -d '[:space:]')"
[ -n "$TARGET_VERSION" ] || { echo "Erro: VERSION ausente ou vazia no alvo ${TARGET_OID}."; exit 1; }
canonicalize_version "$TARGET_VERSION" >/dev/null 2>&1 || {
  echo "Erro: VERSION invalida ou ambigua no alvo ${TARGET_OID}: ${TARGET_VERSION}."
  exit 1
}
if [ -n "${CONTABASE_VERSION:-}" ] && [ "${CONTABASE_VERSION}" != "$TARGET_VERSION" ]; then
  echo "Erro: TARGET_PROVENANCE_MISMATCH: versao solicitada (${CONTABASE_VERSION}) diverge do VERSION do alvo ${TARGET_OID} (${TARGET_VERSION})."
  exit 1
fi
resolve_current_version || exit 1
enforce_version_policy

if [ "$RECOVERY_FINALIZE_ONLY" = true ]; then
  [ "$CONFIRMED_RUNTIME_VERSION" = "$TARGET_VERSION" ] || {
    echo "Erro: finalizacao de recovery sem runtime comprovado na versao alvo."
    exit 1
  }
  RUNTIME_CONFIRMED=true
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

if ! VERSION="$TARGET_VERSION" docker compose "${COMPOSE_BUILD_ARGS[@]}" config >/dev/null; then
  echo "Erro: Compose canonico do TARGET_OID e invalido."
  exit 1
fi

# --- Confirmação ---
if [ "$FORCE_RESET" = true ]; then
  echo "ATENÇÃO: modo --force-reset ativado."
  echo "Será executado: git reset --hard $TARGET_OID + git clean -fd"
  echo "Seus arquivos LOCAIS (.env.docker, compose.local.yaml, data/) serão PRESERVADOS."
  echo "Demais arquivos modificados localmente podem ser PERDIDOS."
  echo ""
fi

RESUMO="Resumo:
  Branch:       $(git branch --show-current)
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

# O arquivo de ambiente so pode ser alterado depois do pacote pre-update.
echo "--- Backup de ${ENV_FILE_DOCKER} ---"
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
    -e data/ \
    -e backups/ \
    -e uploads/ \
    -e update.sh \
    -e update-contabase-docker.sh \
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
echo "--- Reconstruindo contêineres ---"
NEW_RUNTIME_STARTED=true
if ! VERSION="$APP_VERSION" docker compose "${COMPOSE_BUILD_ARGS[@]}" up -d --build --remove-orphans; then
  fail_with_rollback "build ou subida da nova imagem falhou."
fi

if ! confirm_target_runtime_identity; then
  docker compose $COMPOSE_ARGS logs --tail 100 contabase || true
  fail_with_rollback "identidade ou health da nova imagem nao foi comprovado."
fi

[ "$CONFIRMED_RUNTIME_VERSION" = "$TARGET_VERSION" ] || {
  fail_with_rollback "runtime confirmado diverge da versao alvo antes da promocao."
}
RUNTIME_CONFIRMED=true
if recovery_source_known "$CANONICAL_BUILD_DIR"; then
  recovery_image="$(docker inspect "$RESOLVED_CONTAINER_ID" --format '{{.Image}}')" || recovery_image=""
  if [[ "$recovery_image" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    recovery_record "$(dirname "$STATE_FILE")/recovery" docker "$TARGET_VERSION" "${recovery_image#sha256:}" \
      || echo "Aviso: prova de recuperacao nao gravada; retorno automatico a esta imagem ficara bloqueado."
  fi
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
  local repo_path installed_channel wrapper_source state_dir
  repo_path="$(pwd)"
  installed_channel="$(infer_install_channel "$APP_VERSION")"
  wrapper_source="${repo_path}/scripts/lib/contabase-update-wrapper.sh"
  state_dir="$(dirname "$STATE_FILE")"
  local has_priv=false

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
    {
      printf 'CONTABASE_STATE_VERSION=1\n'
      printf 'CONTABASE_INSTALL_METHOD=docker\n'
      printf 'CONTABASE_CHANNEL=%s\n' "$installed_channel"
      printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$APP_VERSION"
      printf 'CONTABASE_REPO_PATH=%s\n' "$repo_path"
    } > "$state_tmp"
    if ! run_priv install -m 0644 "$state_tmp" "$STATE_FILE"; then
      rm -f "$state_tmp"
      echo "Erro: nao foi possivel confirmar $STATE_FILE."
      return 1
    fi
    rm -f "$state_tmp"
  }

  if ! run_priv mkdir -p "$state_dir" 2>/dev/null; then
    echo "Erro: nao foi possivel criar ${state_dir} (sudo necessario)."
    return 1
  fi

  if ! printf 'docker\n%s\n' "$repo_path" | run_priv tee "$mode_file" >/dev/null 2>/dev/null; then
    echo "Erro: nao foi possivel escrever $mode_file."
    return 1
  fi
  run_priv chmod 0644 "$mode_file" 2>/dev/null || true

  if [ -f "$wrapper_source" ]; then
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
  fi

  if ! run_priv tee "$update_wrapper" >/dev/null 2>/dev/null; then
    echo "Erro: nao foi possivel instalar $update_wrapper."
    return 1
  else
    cat <<'WRAPPER_EOF' | run_priv tee "$update_wrapper" >/dev/null 2>/dev/null
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
  fi

  run_priv chmod 0755 "$update_wrapper" 2>/dev/null || return 1

  if [ ! -e "$update_alias" ]; then
    run_priv ln -s "$(basename "$update_wrapper")" "$update_alias" 2>/dev/null || true
  fi

  commit_install_state || return 1

  echo ""
  echo "Comando global de atualizacao instalado:"
  echo "  contabase-update [--yes] [--dry-run]"
  echo "  cb-update [--yes] [--dry-run]"
  echo "  sudo contabase-update --help"
}

finalize_confirmed_checkout() {
  local head_oid version_value

  enforce_clean_worktree || return 1
  head_oid="$(git rev-parse --verify 'HEAD^{commit}')" || return 1
  if [ "$head_oid" != "$TARGET_OID" ]; then
    git merge-base --is-ancestor "$head_oid" "$TARGET_OID" || {
      echo "Erro: checkout atual nao pode avancar em fast-forward para o runtime confirmado ${TARGET_OID}."
      return 1
    }
    git merge --ff-only "$TARGET_OID" || return 1
  fi

  [ "$(git rev-parse --verify 'HEAD^{commit}')" = "$TARGET_OID" ] || return 1
  version_value="$(read_version_file 2>/dev/null || true)"
  [ "$version_value" = "$TARGET_VERSION" ] || {
    echo "Erro: VERSION do checkout (${version_value:-ausente}) diverge do runtime confirmado (${TARGET_VERSION})."
    return 1
  }
  APP_VERSION="$TARGET_VERSION"
}

if ! finalize_confirmed_checkout; then
  fail_with_rollback "runtime alvo esta saudavel, mas checkout nao foi finalizado; retry permanece seguro."
fi
if ! backfill_update_command; then
  fail_with_rollback "runtime alvo esta saudavel, mas estado/update command nao foram confirmados; retry permanece seguro."
fi

echo ""
echo "Healthcheck saudavel: $HEALTH_URL"
echo "Backup preventivo: ${BACKUP_PATH:-nao criado}"
echo "Backup ID:         ${BACKUP_ID:-nao aplicavel}"
echo "Backup .env:       ${ENV_BACKUP_PATH:-nao criado}"
echo "Commit logico concluido: runtime, checkout e estado em ${APP_VERSION}."
echo "=== Atualizacao concluida ==="

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
#   --no-backup        Pular o backup do banco.
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
NO_BACKUP=false
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
HEALTH_URL=""
ENV_BACKUP_PATH=""
STATE_FILE="/etc/contabase/install-state.env"
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-}"

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
    --no-backup) NO_BACKUP=true; shift ;;
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

  for key in AUTH_ENCRYPTION_KEY SECURITY_MASTER_KEY CONTABASE_SETUP_TOKEN; do
    env_key_is_set "$key" "$file" || missing+=("$key")
  done

  [ "${#missing[@]}" -eq 0 ] && return 0

  echo ""
  echo "====================================================================="
  echo "ERRO: Secret(s) obrigatorio(s) ausente(s) ou com placeholder em ${file}:"
  for key in "${missing[@]}"; do
    echo "  - ${key}"
  done
  echo ""
  echo "Para resolver:"
  echo "  - Edite ${file} manualmente."
  echo "  - Gere valores fortes para cada secret (ex.: openssl rand -base64 32)."
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

wait_for_healthcheck() {
  local attempt max_attempts health container_id docker_health
  max_attempts="${HEALTHCHECK_ATTEMPTS:-30}"
  resolve_health_url

  for attempt in $(seq 1 "$max_attempts"); do
    if command -v curl >/dev/null 2>&1; then
      health="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null || true)"
      if [ "$health" = '{"status":"healthy"}' ]; then
        return 0
      fi
    else
      container_id="$(docker compose $COMPOSE_ARGS ps -q contabase 2>/dev/null || true)"
      if [ -n "$container_id" ]; then
        docker_health="$(docker inspect "$container_id" --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null || true)"
        if [ "$docker_health" = "healthy" ]; then
          return 0
        fi
      fi
    fi

    echo "Aguardando healthcheck: tentativa ${attempt}/${max_attempts}..."
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

create_stopped_backup() {
  local timestamp backup_root fallback_dir
  timestamp="$(date +%Y%m%d-%H%M%S)"
  backup_root="data/backups/pre-update-${timestamp}"

  if [ ! -f "data/contabase.db" ]; then
    echo "Aviso: banco data/contabase.db nao encontrado; backup de dados foi pulado."
    return 0
  fi

  mkdir -p "$backup_root"

  if command -v sqlite3 >/dev/null 2>&1; then
    echo "Criando backup WAL-safe com sqlite3 .backup e stack parada..."
    DATA_DIR=data \
      DB_FILE=data/contabase.db \
      UPLOADS_DIR=data/uploads \
      ./scripts/ops/backup.sh "$backup_root"
    BACKUP_PATH="$(find "$backup_root" -mindepth 1 -maxdepth 1 -type d -name 'contabase-backup-*' | sort | tail -n1)"
  else
    echo "sqlite3 nao encontrado no host; usando copia consistente com a stack parada."
    fallback_dir="$backup_root/contabase-backup-stopped-${timestamp}"
    mkdir -p "$fallback_dir"
    cp data/contabase.db "$fallback_dir/contabase.db"
    [ ! -f data/contabase.db-wal ] || cp data/contabase.db-wal "$fallback_dir/contabase.db-wal"
    [ ! -f data/contabase.db-shm ] || cp data/contabase.db-shm "$fallback_dir/contabase.db-shm"
    if [ -d data/uploads ]; then
      cp -R data/uploads "$fallback_dir/uploads"
    else
      mkdir -p "$fallback_dir/uploads"
    fi
    {
      echo "created_at=$timestamp"
      echo "method=stopped_stack_copy"
      echo "wal_consistency=stack_stopped"
    } > "$fallback_dir/manifest.txt"
    BACKUP_PATH="$fallback_dir"
  fi

  if [ -z "$BACKUP_PATH" ] || [ ! -f "$BACKUP_PATH/contabase.db" ]; then
    echo "Erro: backup preventivo nao foi criado corretamente."
    return 1
  fi

  echo "Backup preventivo criado: $BACKUP_PATH"
}

rollback_runtime() {
  echo ""
  echo "=== Rollback do runtime Docker ==="
  echo "O banco e os uploads NAO serao restaurados automaticamente."
  docker compose $COMPOSE_ARGS down >/dev/null 2>&1 || true

  if [ -z "$PREVIOUS_ROLLBACK_TAG" ] || [ -z "$PREVIOUS_IMAGE_NAME" ]; then
    echo "Erro: imagem anterior indisponivel; rollback automatico do runtime nao e possivel."
    return 1
  fi

  if ! docker image tag "$PREVIOUS_ROLLBACK_TAG" "$PREVIOUS_IMAGE_NAME"; then
    echo "Erro: nao foi possivel restaurar a tag da imagem anterior."
    return 1
  fi

  if ! docker compose $COMPOSE_ARGS up -d --no-build --remove-orphans; then
    echo "Erro: a imagem anterior nao voltou a subir."
    return 1
  fi

  STACK_STOPPED=false
  if wait_for_healthcheck; then
    echo "Rollback do runtime concluido: imagem anterior saudavel em $HEALTH_URL."
    echo "Codigo local pode permanecer atualizado; dados nao foram restaurados."
    docker image rm "$PREVIOUS_ROLLBACK_TAG" >/dev/null 2>&1 || true
    return 0
  fi

  echo "Erro: rollback subiu a imagem anterior, mas o healthcheck continuou falhando."
  docker compose $COMPOSE_ARGS logs --tail 100 contabase || true
  return 1
}

fail_with_rollback() {
  local message="$1"
  echo ""
  echo "ERRO: $message"

  if [ "$STACK_STOPPED" = true ]; then
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
  if [ "$STACK_STOPPED" = true ]; then
    rollback_runtime || true
  fi
  exit 130
}

trap handle_signal INT TERM

PREVIOUS_VERSION="$(read_version_file 2>/dev/null || true)"
PREVIOUS_VERSION="${PREVIOUS_VERSION:-desconhecida}"

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
  echo "Depois edite com suas configuracoes antes de prosseguir."
  exit 1
fi

# Validate secrets in existing env
echo "--- Validando secrets em ${ENV_FILE_DOCKER} ---"
if ! validate_env_secrets "$ENV_FILE_DOCKER"; then
  exit 1
fi
echo "Secrets validados: todos presentes e preenchidos."

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
  echo "  1. Backup do .env.docker"
  echo "  2. Backup do banco SQLite (stack parada)"
  echo "  3. Atualizar codigo (git pull --ff-only)"
  echo "  4. Reconstruir e subir containers"
  echo "  5. Validar healthcheck"
  echo "  6. Em caso de falha: rollback para imagem anterior"
  echo ""
  echo "NENHUMA ALTERACAO FOI FEITA."
  exit 0
fi

# Backup .env.docker before any changes
echo "--- Backup de ${ENV_FILE_DOCKER} ---"
env_backup "$ENV_FILE_DOCKER"

# --- Fetch ---
if [ "$SKIP_PULL" = false ]; then
  echo "Buscando atualizações..."
  git fetch origin
  COMMITS_AHEAD=$(git rev-list HEAD..origin/"$(git branch --show-current)" --count 2>/dev/null || echo "0")
  if [ "$COMMITS_AHEAD" -gt 0 ]; then
    echo ""
    echo "Commits pendentes: $COMMITS_AHEAD"
    git log --oneline HEAD..origin/"$(git branch --show-current)"
  else
    echo "Nenhum commit novo. Prosseguindo apenas com rebuild."
  fi
else
  echo "Pulando atualização Git (--skip-pull)."
  COMMITS_AHEAD=0
fi

# --- Confirmação ---
if [ "$FORCE_RESET" = true ]; then
  echo "ATENÇÃO: modo --force-reset ativado."
  echo "Será executado: git reset --hard origin/$(git branch --show-current) + git clean -fd"
  echo "Seus arquivos LOCAIS (.env.docker, compose.local.yaml, data/) serão PRESERVADOS."
  echo "Demais arquivos modificados localmente podem ser PERDIDOS."
  echo ""
fi

RESUMO="Resumo:
  Branch:       $(git branch --show-current)
  Commits novos: $COMMITS_AHEAD
  Compose:      ${COMPOSE_FILE:-"docker-compose.yml + overrides"}
  Reset forçado: $FORCE_RESET
  Backup seguro: $( [ "$NO_BACKUP" = false ] && echo "sim (stack parada)" || echo "não (--no-backup)" )
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

if [ "$NO_BACKUP" = false ]; then
  if ! create_stopped_backup; then
    fail_with_rollback "backup WAL-safe falhou; codigo nao sera atualizado."
  fi
else
  echo "Aviso: backup preventivo pulado por --no-backup."
fi

# --- Atualizar código ---
if [ "$FORCE_RESET" = true ]; then
  echo "--- Reset forçado ---"

  if ! git reset --hard origin/"$(git branch --show-current)"; then
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
elif [ "$SKIP_PULL" = false ]; then
  echo "--- Atualizando código (git pull --ff-only) ---"
  if ! git pull --ff-only origin "$(git branch --show-current)"; then
    fail_with_rollback "git pull --ff-only falhou."
  fi
  echo "Código atualizado para: $(git log --oneline -1)"
fi

# --- Versao efetiva apos atualizar o codigo ---
VERSION_FILE_VALUE="$(read_version_file 2>/dev/null || true)"
if [ -z "$VERSION_FILE_VALUE" ]; then
  fail_with_rollback "VERSION ausente ou vazia apos atualizar o codigo."
fi

APP_VERSION="${CONTABASE_VERSION:-$VERSION_FILE_VALUE}"
if [ -z "$APP_VERSION" ]; then
  fail_with_rollback "versao efetiva vazia apos atualizar o codigo."
fi

echo "Versão anterior: $PREVIOUS_VERSION"
echo "Versão nova após atualização do código: $APP_VERSION"

# --- Rebuild ---
echo ""
echo "--- Reconstruindo contêineres ---"
if ! VERSION="$APP_VERSION" docker compose $COMPOSE_ARGS up -d --build --remove-orphans; then
  fail_with_rollback "build ou subida da nova imagem falhou."
fi

if ! wait_for_healthcheck; then
  docker compose $COMPOSE_ARGS logs --tail 100 contabase || true
  fail_with_rollback "healthcheck da nova imagem falhou."
fi

STACK_STOPPED=false
if [ -n "$PREVIOUS_ROLLBACK_TAG" ]; then
  docker image rm "$PREVIOUS_ROLLBACK_TAG" >/dev/null 2>&1 || true
fi

echo ""
echo "Healthcheck saudavel: $HEALTH_URL"
echo "Backup preventivo: ${BACKUP_PATH:-nao criado}"
echo "Backup .env:       ${ENV_BACKUP_PATH:-nao criado}"
echo "=== Atualizacao concluida ==="

# ==============================================================================
# Global update command (backfill for existing installs)
# ==============================================================================

backfill_update_command() {
  local update_wrapper="/usr/local/bin/contabase-update"
  local mode_file="/etc/contabase/install-mode"
  local repo_path installed_channel wrapper_source
  repo_path="$(pwd)"
  installed_channel="$(infer_install_channel "$APP_VERSION")"
  wrapper_source="${repo_path}/scripts/lib/contabase-update-wrapper.sh"
  local has_priv=false

  if [ "$(id -u)" -eq 0 ]; then
    has_priv=true
  elif command -v sudo >/dev/null 2>&1; then
    has_priv=true
  fi

  if [ "$has_priv" = false ]; then
    echo ""
    echo "Aviso: permissao root/sudo necessaria para instalar o comando global contabase-update."
    echo "Execute manualmente como root:"
    echo "  sudo ./scripts/update-contabase-docker.sh"
    echo "Ou continue usando o script manual: ./scripts/update-contabase-docker.sh"
    return 0
  fi

  run_priv() {
    if [ "$(id -u)" -eq 0 ]; then
      "$@"
    else
      sudo "$@"
    fi
  }

  if ! run_priv mkdir -p /etc/contabase 2>/dev/null; then
    echo "Aviso: nao foi possivel criar /etc/contabase/ (sudo necessario)."
    return 0
  fi

  {
    printf 'CONTABASE_STATE_VERSION=1\n'
    printf 'CONTABASE_INSTALL_METHOD=docker\n'
    printf 'CONTABASE_CHANNEL=%s\n' "$installed_channel"
    printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$APP_VERSION"
    printf 'CONTABASE_REPO_PATH=%s\n' "$repo_path"
  } | run_priv tee "$STATE_FILE" >/dev/null 2>/dev/null || {
    echo "Aviso: nao foi possivel escrever $STATE_FILE."
    return 0
  }
  run_priv chmod 0644 "$STATE_FILE" 2>/dev/null || true

  if ! printf 'docker\n%s\n' "$repo_path" | run_priv tee "$mode_file" >/dev/null 2>/dev/null; then
    echo "Aviso: nao foi possivel escrever $mode_file."
    return 0
  fi
  run_priv chmod 0644 "$mode_file" 2>/dev/null || true

  if [ -f "$wrapper_source" ]; then
    run_priv install -m 0755 "$wrapper_source" "$update_wrapper" 2>/dev/null || {
      echo "Aviso: nao foi possivel instalar $update_wrapper."
      return 0
    }
    if [ ! -e /usr/local/bin/cb-update ]; then
      run_priv ln -s contabase-update /usr/local/bin/cb-update 2>/dev/null || true
    fi
    echo "OK: contabase-update atualizado."
    return 0
  fi

  if ! run_priv tee "$update_wrapper" >/dev/null 2>/dev/null; then
    echo "Aviso: nao foi possivel instalar $update_wrapper."
    return 0
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

  run_priv chmod 0755 "$update_wrapper" 2>/dev/null || true

  if [ ! -e /usr/local/bin/cb-update ]; then
    run_priv ln -s contabase-update /usr/local/bin/cb-update 2>/dev/null || true
  fi

  echo ""
  echo "Comando global de atualizacao instalado:"
  echo "  contabase-update [--yes] [--dry-run]"
  echo "  cb-update [--yes] [--dry-run]"
  echo "  sudo contabase-update --help"
}

backfill_update_command || true

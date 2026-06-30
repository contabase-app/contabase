#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# update-contabase-release.sh — Atualizacao segura via GitHub Release
#
# Baixa um artifact de release publica, valida checksum e VERSION,
# preserva configuracao (/etc/contabase/contabase.env) e dados
# (/var/lib/contabase), troca o bundle e reinicia o servico.
#
# Uso:
#   sudo env CONTABASE_VERSION=vX.Y.Z ./scripts/update-contabase-release.sh
#   sudo env CONTABASE_VERSION=vX.Y.Z CONTABASE_ASSUME_YES=1 ./scripts/update-contabase-release.sh
#   ./scripts/update-contabase-release.sh --validate-only
#   ./scripts/update-contabase-release.sh --dry-run
#
# CONTRATO DE PRESERVACAO:
#   - Nunca sobrescreve secrets/tokens em /etc/contabase/contabase.env
#   - Atualiza apenas VERSION= no .env para rastrear a tag instalada
#   - Nunca altera secrets/tokens preenchidos
#   - Nunca regenera AUTH_ENCRYPTION_KEY, SECURITY_MASTER_KEY, CONTABASE_SETUP_TOKEN
#   - Preserva APP_BASE_URL, ALLOWED_HOSTS, TRUSTED_PROXIES, CONTABASE_ACCESS_MODE, PORT
#   - Preserva DATABASE_URL, DATA_DIR, DB_FILE, UPLOADS_DIR
#   - Preserva /var/lib/contabase (dados, uploads, backups, banco)
#   - Nunca restaura banco SQLite automaticamente
#   - Faz backup do .env e do bundle antes de qualquer alteracao
#   - Rollback automatico apenas de binario/assets/templates/unit
# ==============================================================================

REPO="${CONTABASE_REPO:-https://github.com/contabase-app/contabase}"
TAG="${CONTABASE_VERSION:-}"
SCRIPT_SOURCE="${BASH_SOURCE[0]:-$0}"
SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_SOURCE")" 2>/dev/null && pwd)"
PUBLIC_RAW_BASE="${CONTABASE_RAW_BASE:-https://raw.githubusercontent.com/contabase-app/contabase}"
PORT="${CONTABASE_PORT:-8080}"
APP_USER="${CONTABASE_USER:-contabase}"
INSTALL_DIR="${CONTABASE_INSTALL_DIR:-/opt/contabase}"
DATA_DIR="${CONTABASE_DATA_DIR:-/var/lib/contabase}"
CONFIG_DIR="${CONTABASE_CONFIG_DIR:-/etc/contabase}"
STATE_FILE="${CONFIG_DIR}/install-state.env"
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-pinned}"
ASSUME_YES="${CONTABASE_ASSUME_YES:-0}"
HEALTHCHECK_ATTEMPTS="${CONTABASE_HEALTHCHECK_ATTEMPTS:-30}"
VALIDATE_ONLY=false
DRY_RUN=false
ARCH=""
ARTIFACT_NAME=""
ARTIFACT_URL=""
CHECKSUMS_URL=""
TMP_DIR=""
EXTRACT_DIR=""
STAGING_DIR=""
PREVIOUS_DIR=""
ENV_FILE="${CONFIG_DIR}/contabase.env"
ENV_BACKUP=""
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
  sudo env CONTABASE_VERSION=vX.Y.Z ./scripts/update-contabase-release.sh
  ./scripts/update-contabase-release.sh --validate-only
  ./scripts/update-contabase-release.sh --dry-run

Atualiza uma instalacao release/LXC existente a partir de um artifact
oficial do GitHub Release, validando SHA-256 e VERSION.

--validate-only  Baixa e valida o artifact; nao altera instalacao.
--dry-run        Valida o ambiente e exibe o que seria feito; nao altera nada.

Variaveis de ambiente uteis:
  CONTABASE_ASSUME_YES=1            Modo nao interativo.
  CONTABASE_HEALTHCHECK_ATTEMPTS=N  Tentativas do healthcheck.
  CONTABASE_PORT=N                  Porta (padrao: 8080).
  CONTABASE_INSTALL_DIR=/opt/...    Diretorio do bundle.
  CONTABASE_DATA_DIR=/var/lib/...   Diretorio de dados.
  CONTABASE_CONFIG_DIR=/etc/...     Diretorio de configuracao.
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
    --dry-run)
      DRY_RUN=true
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

# ==============================================================================
# Validation & environment detection
# ==============================================================================

prompt_version() {
  if [ -n "$TAG" ]; then
    return
  fi

  [ -t 0 ] || fail "CONTABASE_VERSION e obrigatorio em modo nao interativo."
  read -r -p "Tag publica para atualizar (ex.: vMAJOR.MINOR.PATCH[-beta.N]): " TAG
}

validate_version() {
  [ -n "$TAG" ] || fail "tag vazia."
  case "$TAG" in
    latest|main|master|dev|develop|stable|*-internal*)
      fail "versao invalida para update: ${TAG}. Use uma tag SemVer publica (ex.: vMAJOR.MINOR.PATCH[-beta.N])."
      ;;
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

check_platform() {
  [ "$(id -u)" -eq 0 ] || fail "execute como root (ex.: sudo)."
  [ "$(uname -s)" = "Linux" ] || fail "update suporta apenas Linux."
  command -v systemctl >/dev/null 2>&1 || fail "systemctl nao encontrado."
}

# ==============================================================================
# Env file validation (read-only — never write to .env during update)
# ==============================================================================

env_load_value() {
  local key="$1"
  local file="${2:-$ENV_FILE}"
  [ -f "$file" ] || { printf ''; return 0; }
  awk -F= -v k="$key" '$1 == k { val = substr($0, index($0, "=") + 1); print val; exit }' "$file"
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
  local file="${2:-$ENV_FILE}"
  local val
  [ -f "$file" ] || return 1
  val="$(env_load_value "$key" "$file")"
  [ -n "$val" ] || return 1
  env_is_placeholder "$val" && return 1
  return 0
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

validate_access_contract_values() {
  local base_url="$1"
  local proxies="$2"
  local mode="$3"
  local inferred host

  inferred="$(infer_access_mode "$base_url" "$proxies")"
  mode="${mode:-$inferred}"

  case "$mode" in
    local|lan|proxy) ;;
    blocked)
      fail "APP_BASE_URL em HTTP publico sem reverse proxy nao e permitido. Use HTTPS com proxy/tunnel ou um IP privado RFC1918 em modo LAN."
      ;;
    *) fail "CONTABASE_ACCESS_MODE invalido em ${ENV_FILE}: ${mode}. Use local, lan ou proxy." ;;
  esac

  if [ "$inferred" = "lan" ] && [ "$mode" != "lan" ]; then
    fail "APP_BASE_URL usa IP privado por HTTP sem proxy; configure CONTABASE_ACCESS_MODE=lan em ${ENV_FILE}."
  fi
  if [ "$inferred" = "proxy" ] && [ "$mode" != "proxy" ]; then
    fail "APP_BASE_URL/proxy indicam modo proxy; configure CONTABASE_ACCESS_MODE=proxy em ${ENV_FILE}."
  fi
  if [ "$mode" = "lan" ]; then
    host="$(host_from_base_url "$base_url")"
    host_is_private_ipv4 "$host" || fail "CONTABASE_ACCESS_MODE=lan exige APP_BASE_URL com IP privado RFC1918."
    [ "$(scheme_from_base_url "$base_url")" = "http" ] || fail "CONTABASE_ACCESS_MODE=lan exige APP_BASE_URL http://IP_PRIVADO:PORTA."
  fi
  if [ "$inferred" = "blocked" ]; then
    fail "APP_BASE_URL em HTTP publico sem reverse proxy nao e permitido. Use HTTPS com proxy/tunnel ou um IP privado RFC1918 em modo LAN."
  fi
}

backfill_access_mode_in_env() {
  local base_url proxies mode inferred
  base_url="$(env_load_value "APP_BASE_URL" "$ENV_FILE" | tr -d "[:space:]'\"")"
  proxies="$(env_load_value "TRUSTED_PROXIES" "$ENV_FILE" | tr -d "[:space:]'\"")"
  mode="$(env_load_value "CONTABASE_ACCESS_MODE" "$ENV_FILE" | tr -d "[:space:]'\"")"
  validate_access_contract_values "$base_url" "$proxies" "$mode"

  if env_key_is_set "CONTABASE_ACCESS_MODE" "$ENV_FILE"; then
    say "CONTABASE_ACCESS_MODE detectado: ${mode}"
    return 0
  fi

  inferred="$(infer_access_mode "$base_url" "$proxies")"
  env_set_key "CONTABASE_ACCESS_MODE" "$inferred" "$ENV_FILE"
  say "CONTABASE_ACCESS_MODE adicionado ao .env: ${inferred}"
}

validate_existing_env() {
  say "Verificando configuracao existente..."

  if [ ! -f "$ENV_FILE" ]; then
    fail "Arquivo de configuracao nao encontrado: ${ENV_FILE}. Execute a instalacao primeiro."
  fi

  if [ ! -r "$ENV_FILE" ]; then
    fail "Arquivo de configuracao sem permissao de leitura: ${ENV_FILE}"
  fi

  # Validate mandatory secrets
  local missing=()
  for key in AUTH_ENCRYPTION_KEY SECURITY_MASTER_KEY CONTABASE_SETUP_TOKEN; do
    env_key_is_set "$key" "$ENV_FILE" || missing+=("$key")
  done

  if [ "${#missing[@]}" -gt 0 ]; then
    say ""
    say "====================================================================="
    say "ERRO: Secret(s) obrigatorio(s) ausente(s) ou com placeholder em ${ENV_FILE}:"
    for key in "${missing[@]}"; do
      say "  - ${key}"
    done
    say ""
    say "Para resolver:"
    say "  - Edite ${ENV_FILE}"
    say "  - Gere valores fortes para cada secret (ex.: openssl rand -base64 32)."
    say "  - Execute o update novamente."
    say "====================================================================="
    return 1
  fi

  # Load custom paths from existing env (preserve user configuration)
  local custom_data_dir custom_db_file custom_uploads_dir
  custom_data_dir="$(env_load_value "DATA_DIR" "$ENV_FILE" | tr -d "[:space:]'\"")"
  if [ -n "$custom_data_dir" ] && [ "$custom_data_dir" != "/" ] && [[ "$custom_data_dir" = /* ]]; then
    DATA_DIR="$custom_data_dir"
    say "DATA_DIR detectado da configuracao existente: ${DATA_DIR}"
  fi
  custom_db_file="$(env_load_value "DB_FILE" "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$custom_db_file" ] && say "DB_FILE detectado: ${custom_db_file}"
  custom_uploads_dir="$(env_load_value "UPLOADS_DIR" "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$custom_uploads_dir" ] && say "UPLOADS_DIR detectado: ${custom_uploads_dir}"

  local custom_base_url custom_allowed_hosts custom_proxies
  custom_base_url="$(env_load_value "APP_BASE_URL" "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$custom_base_url" ] && say "APP_BASE_URL detectado: ${custom_base_url}"
  custom_allowed_hosts="$(env_load_value "ALLOWED_HOSTS" "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$custom_allowed_hosts" ] && say "ALLOWED_HOSTS detectado"
  custom_proxies="$(env_load_value "TRUSTED_PROXIES" "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$custom_proxies" ] && say "TRUSTED_PROXIES detectado"
  local custom_access_mode
  custom_access_mode="$(env_load_value "CONTABASE_ACCESS_MODE" "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$custom_access_mode" ] && say "CONTABASE_ACCESS_MODE detectado: ${custom_access_mode}"
  validate_access_contract_values "$custom_base_url" "$custom_proxies" "$custom_access_mode"

  # Detect PORT from existing env for healthcheck
  local configured_port
  configured_port="$(env_load_value "PORT" "$ENV_FILE" | tr -d "[:space:]'\"")"
  if [ -n "$configured_port" ] && [[ "$configured_port" =~ ^[0-9]+$ ]] \
    && [ "$configured_port" -ge 1 ] && [ "$configured_port" -le 65535 ]; then
    PORT="$configured_port"
    say "Porta detectada da configuracao existente: ${PORT}"
  fi

  # Load DATABASE_URL for validation (not used directly by unit)
  local custom_db_url
  custom_db_url="$(env_load_value "DATABASE_URL" "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$custom_db_url" ] && say "DATABASE_URL detectado"

  say "Configuracao validada: secrets presentes, paths customizados carregados."
}

# ==============================================================================
# Installation detection
# ==============================================================================

validate_existing_installation() {
  say "Verificando instalacao existente..."

  if [ ! -d "$INSTALL_DIR" ]; then
    fail "Diretorio de instalacao nao encontrado: ${INSTALL_DIR}."
  fi

  if [ ! -f "${INSTALL_DIR}/contabase" ]; then
    fail "Binario contabase nao encontrado em ${INSTALL_DIR}/contabase."
  fi

  if [ ! -d "$DATA_DIR" ]; then
    fail "Diretorio de dados nao encontrado: ${DATA_DIR}."
  fi

  if [ -f "$SERVICE_FILE" ]; then
    say "Unit systemd detectada: ${SERVICE_FILE}"
  else
    say "Aviso: unit systemd nao encontrada em ${SERVICE_FILE}. Sera criada se ausente."
  fi

  # Detect current installed version
  if [ -f "${INSTALL_DIR}/VERSION" ]; then
    local current_version
    current_version="$(tr -d '[:space:]' < "${INSTALL_DIR}/VERSION")"
    say "Versao instalada atualmente: ${current_version}"
    if [ "$current_version" = "$TAG" ]; then
      if [ "$ASSUME_YES" = "1" ]; then
        say "Mesma versao ja instalada. Continuando (reinstall seguro)."
      else
        say "Aviso: A versao ${TAG} ja esta instalada."
      fi
    fi
  fi

  say "Instalacao existente validada."
}

# ==============================================================================
# Backup functions
# ==============================================================================

backup_env_file() {
  if [ ! -f "$ENV_FILE" ]; then
    return 0
  fi

  ENV_BACKUP="${ENV_FILE}.pre-update-$(date +%Y%m%d-%H%M%S).bak"
  if cp -a "$ENV_FILE" "$ENV_BACKUP"; then
    chmod 0600 "$ENV_BACKUP" 2>/dev/null || true
    say "Backup do .env criado em: ${ENV_BACKUP}"
  else
    fail "nao foi possivel fazer backup de ${ENV_FILE}."
  fi
}

backup_bundle() {
  if [ ! -e "$INSTALL_DIR" ]; then
    return 0
  fi

  local timestamp
  timestamp="$(date +%Y%m%d-%H%M%S)"
  PREVIOUS_DIR="${INSTALL_DIR}.previous.${timestamp}.$$"

  if mv "$INSTALL_DIR" "$PREVIOUS_DIR"; then
    say "Bundle anterior preservado em: ${PREVIOUS_DIR}"
    return 0
  fi
  fail "nao foi possivel preservar o bundle anterior em ${PREVIOUS_DIR}."
}

# ==============================================================================
# Download & validation (reuses patterns from install script)
# ==============================================================================

build_urls() {
  ARTIFACT_NAME="contabase-linux-${ARCH}.tar.gz"
  ARTIFACT_URL="${REPO}/releases/download/${TAG}/${ARTIFACT_NAME}"
  CHECKSUMS_URL="${REPO}/releases/download/${TAG}/checksums.txt"
}

download_file() {
  local url="$1"
  local destination="$2"

  say "Baixando ${url}"
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
  actual_normalized="$(printf '%s' "$actual" | tr '[:upper:]' '[:lower:]')"
  expected_normalized="$(printf '%s' "$expected" | tr '[:upper:]' '[:lower:]')"
  [ "$actual_normalized" = "$expected_normalized" ] \
    || fail "checksum SHA-256 divergente para ${ARTIFACT_NAME}."
  say "Checksum SHA-256 validado: ${actual}"
}

validate_tar_entries() {
  local artifact_file="$1"
  local entry components component
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

  local type_listing
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
  say "Bundle extraido validado: versao ${TAG} confirmada."
}

download_and_validate_bundle() {
  local artifact_file checksums_file
  TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-release-update.XXXXXX")"
  EXTRACT_DIR="${TMP_DIR}/extract"
  artifact_file="${TMP_DIR}/${ARTIFACT_NAME}"
  checksums_file="${TMP_DIR}/checksums.txt"
  mkdir -p "$EXTRACT_DIR"

  download_file "$ARTIFACT_URL" "$artifact_file"
  download_file "$CHECKSUMS_URL" "$checksums_file"
  validate_checksum "$checksums_file" "$artifact_file"
  validate_tar_entries "$artifact_file"
  tar --no-same-owner --no-same-permissions -xzf "$artifact_file" -C "$EXTRACT_DIR"
  validate_extracted_bundle "${EXTRACT_DIR}/contabase"
  say "Artifact validado e extraido com seguranca."
}

# ==============================================================================
# Confirm update
# ==============================================================================

confirm_update() {
  say ""
  say "Resumo da atualizacao:"
  say "  Repositorio:  $REPO"
  say "  Versao:       $TAG"
  say "  Arquitetura:  $ARCH"
  say "  Instalacao:   $INSTALL_DIR"
  say "  Configuracao: ${ENV_FILE} (preservada)"
  say "  Dados:        ${DATA_DIR} (preservados)"
  say "  Porta:        $PORT"
  say ""
  say "A configuracao em ${ENV_FILE} NAO sera alterada."
  say "Os dados em ${DATA_DIR} (banco, uploads, backups) NAO serao alterados."
  say ""

  [ "$ASSUME_YES" = "1" ] && return
  [ -t 0 ] || fail "confirmacao interativa indisponivel; use CONTABASE_ASSUME_YES=1."
  read -r -p "Atualizar para ${TAG}? (s/N) " answer
  case "$answer" in
    s|S|sim|SIM|Sim) ;;
    *) fail "atualizacao cancelada." ;;
  esac
}

# ==============================================================================
# Service management
# ==============================================================================

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

  if ! install -o root -g root -m 0644 "$service_staging" "$SERVICE_FILE"; then
    return 1
  fi
}

stop_service() {
  if systemctl is-active --quiet contabase 2>/dev/null; then
    say "Parando servico contabase..."
    systemctl stop contabase || fail "nao foi possivel parar o servico contabase."
  else
    say "Servico contabase ja esta parado."
  fi
}

# ==============================================================================
# Bundle swap, healthcheck, and rollback
# ==============================================================================

stage_and_swap_bundle() {
  local bundle_root timestamp
  bundle_root="${EXTRACT_DIR}/contabase"
  timestamp="$(date +%Y%m%d-%H%M%S)"
  STAGING_DIR="${INSTALL_DIR}.staging.${timestamp}.$$"

  install -d -o root -g root -m 0755 "$STAGING_DIR"
  cp -a "${bundle_root}/." "$STAGING_DIR/"
  chown -R root:root "$STAGING_DIR"
  chmod 0755 "$STAGING_DIR/contabase" "$STAGING_DIR/admin"

  if ! mv "$STAGING_DIR" "$INSTALL_DIR"; then
    if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
      mv "$PREVIOUS_DIR" "$INSTALL_DIR" || true
    fi
    fail "nao foi possivel ativar o bundle em staging."
  fi
  STAGING_DIR=""
  NEW_BUNDLE_ACTIVE=true
  say "Novo bundle ativado em ${INSTALL_DIR}."
}

wait_for_healthcheck() {
  local attempt health
  for attempt in $(seq 1 "$HEALTHCHECK_ATTEMPTS"); do
    health="$(curl -fsS --max-time 3 "http://127.0.0.1:${PORT}/health" 2>/dev/null || true)"
    [ "$health" = '{"status":"healthy"}' ] && {
      say "Healthcheck OK na tentativa ${attempt}."
      return 0
    }
    say "Aguardando healthcheck... tentativa ${attempt}/${HEALTHCHECK_ATTEMPTS}"
    sleep 2
  done
  return 1
}

rollback_bundle() {
  say ""
  say "====================================================================="
  say "ROLLBACK: restaurando bundle anterior."
  say "Dados e configuracao NAO serao restaurados."
  say "====================================================================="

  systemctl stop contabase >/dev/null 2>&1 || true

  if [ "$NEW_BUNDLE_ACTIVE" = true ] && [ -e "$INSTALL_DIR" ]; then
    mv "$INSTALL_DIR" "${INSTALL_DIR}.failed.$(date +%Y%m%d-%H%M%S).$$" || true
  fi

  if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
    mv "$PREVIOUS_DIR" "$INSTALL_DIR" || {
      say "Erro: nao foi possivel restaurar o bundle anterior."
      return 1
    }
    say "Bundle anterior restaurado em ${INSTALL_DIR}."
  else
    say "Nenhum bundle anterior disponivel para rollback."
    return 1
  fi

  if [ -n "$SERVICE_BACKUP" ] && [ -f "$SERVICE_BACKUP" ]; then
    cp "$SERVICE_BACKUP" "$SERVICE_FILE" || true
  fi

  systemctl daemon-reload || return 1
  systemctl restart contabase || return 1

  if wait_for_healthcheck; then
    say "Rollback concluido. Servico saudavel com o bundle anterior."
    return 0
  fi

  say "ALERTA: Rollback do bundle concluido, mas healthcheck ainda falha."
  systemctl status contabase --no-pager || true
  journalctl -u contabase -n 100 --no-pager || true
  return 1
}

rollback_env() {
  if [ -n "$ENV_BACKUP" ] && [ -f "$ENV_BACKUP" ]; then
    say "Backup do .env disponivel em: ${ENV_BACKUP}"
    say "O .env atual em ${ENV_FILE} nao foi alterado pelo update."
    say "Se necessario, restaure manualmente: cp ${ENV_BACKUP} ${ENV_FILE}"
  fi
}

# ==============================================================================
# Global update command (backfill for existing installs)
# ==============================================================================

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

backfill_update_command() {
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
  say "Comando global de atualizacao instalado:"
  say "  sudo contabase-update"
  say "  sudo cb-update"
}

# ==============================================================================
# Main update flow
# ==============================================================================

perform_update() {
  # Dry-run mode: validate everything but make no changes
  if [ "$DRY_RUN" = true ]; then
    say ""
    say "====================================================================="
    say "DRY RUN — nenhuma alteracao sera feita."
    say "====================================================================="
    say "Simulacao concluida com sucesso."
    say ""
    say "O que seria feito:"
    say "  1. Parar servico contabase"
    say "  2. Backup do .env em ${ENV_FILE}.pre-update-<timestamp>.bak"
    say "  3. Backup do bundle anterior em ${INSTALL_DIR}.previous.<timestamp>.$$"
    say "  4. Extrair novo bundle em ${INSTALL_DIR}"
    say "  5. Atualizar unit systemd em ${SERVICE_FILE}"
    say "  6. Iniciar servico e validar healthcheck em http://127.0.0.1:${PORT}/health"
    say "  7. Em caso de falha: rollback automatico do bundle"
    say ""
    return 0
  fi

  # Real update
  say ""
  say "====================================================================="
  say "Iniciando atualizacao para ${TAG}..."
  say "====================================================================="

  # Backup env file (always, before any mutation)
  backup_env_file
  backfill_access_mode_in_env

  # Stop service
  stop_service

  # Backup existing bundle
  backup_bundle

  # Swap bundle
  if ! stage_and_swap_bundle; then
    rollback_env
    fail "falha ao ativar o novo bundle."
  fi

  # Update systemd unit
  if ! render_service_file; then
    say "Falha ao atualizar unit systemd; tentando rollback."
    rollback_bundle || true
    rollback_env
    fail "falha ao instalar a unit systemd; rollback do bundle solicitado."
  fi

  if ! systemctl daemon-reload; then
    say "systemd daemon-reload falhou; tentando rollback."
    rollback_bundle || true
    rollback_env
    fail "systemd daemon-reload falhou; rollback do bundle solicitado."
  fi

  # Start service
  say "Iniciando servico com o novo bundle..."
  if ! systemctl restart contabase; then
    say "Servico nao iniciou com o novo bundle; tentando rollback."
    systemctl status contabase --no-pager || true
    journalctl -u contabase -n 100 --no-pager || true
    if rollback_bundle; then
      fail "novo bundle falhou; bundle anterior recuperado com sucesso."
    fi
    rollback_env
    fail "novo bundle falhou e o rollback nao recuperou o servico."
  fi

  # Healthcheck
  if ! wait_for_healthcheck; then
    say "Healthcheck falhou apos o novo bundle; tentando rollback."
    systemctl status contabase --no-pager || true
    journalctl -u contabase -n 100 --no-pager || true
    if rollback_bundle; then
      fail "novo bundle falhou no healthcheck; bundle anterior recuperado com healthcheck saudavel."
    fi
    rollback_env
    fail "novo bundle falhou no healthcheck e o rollback nao recuperou o servico."
  fi

  NEW_BUNDLE_ACTIVE=false

  # Update VERSION in env file to track installed release tag
  env_set_key "VERSION" "$TAG" "$ENV_FILE"
  env_set_key "CONTABASE_CHANNEL" "$INSTALL_CHANNEL" "$ENV_FILE"
  env_set_key "CONTABASE_INSTALLED_VERSION" "$TAG" "$ENV_FILE"

  # Install/refresh global update command (backfill for existing installs)
  backfill_update_command

  # Report
  say ""
  say "====================================================================="
  say "Atualizacao concluida: ContaBase ${TAG}"
  say "====================================================================="
  say ""
  say "Servico:    systemctl status contabase"
  say "Health:     http://127.0.0.1:${PORT}/health"
  say "Config:     ${ENV_FILE} (VERSION atualizado; CONTABASE_ACCESS_MODE preenchido se ausente; demais valores preservados)"
  say "Dados:      ${DATA_DIR} (preservados)"
  if [ -n "$ENV_BACKUP" ] && [ -f "$ENV_BACKUP" ]; then
    say "Env backup: ${ENV_BACKUP}"
  fi
  if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
    say "Bundle ant: ${PREVIOUS_DIR}"
  fi
  say ""
}

# ==============================================================================
# main
# ==============================================================================

main() {
  prompt_version
  validate_version
  validate_install_channel
  validate_repo
  detect_arch
  validate_numeric_settings
  require_commands

  # Platform/env/install validation (skip only for validate-only mode)
  if [ "$VALIDATE_ONLY" = false ]; then
    check_platform
    validate_existing_env
    validate_existing_installation
  fi

  build_urls
  download_and_validate_bundle

  if [ "$VALIDATE_ONLY" = true ]; then
    say ""
    say "Validacao concluida; nenhuma instalacao foi alterada."
    return 0
  fi

  # Confirm for real update only (dry-run skips interactive prompt)
  if [ "$DRY_RUN" = false ]; then
    confirm_update
  fi

  perform_update
}

main "$@"

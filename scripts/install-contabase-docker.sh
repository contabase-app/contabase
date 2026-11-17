#!/bin/bash
# O fluxo le secrets; mantenha xtrace desativado ate o fim do processo.
set +x
set -euo pipefail

# Arquivos criados pelo instalador nao devem ser legiveis por grupo/outros.
umask 077

# ==============================================================================
# ContaBase - Instalador Guiado Docker
# ==============================================================================

INTERACTIVE=true
CHECK_ONLY=false
ACCESS_URL="http://localhost:8080"
STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
MODE_FILE="${CONTABASE_MODE_FILE:-/etc/contabase/install-mode}"
HISTORY_FILE="${CONTABASE_INSTALL_HISTORY_FILE:-/etc/contabase/install-history.log}"
INSTALL_CHANNEL="${CONTABASE_CHANNEL:-}"
CONTABASE_TIMEZONE_VALUE="${CONTABASE_TIMEZONE:-America/Sao_Paulo}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE_LIB="${SCRIPT_DIR}/lib/contabase-docker-image.sh"
STATE_LIB="${SCRIPT_DIR}/lib/contabase-install-state.sh"
[ -f "$STATE_LIB" ] || { printf 'Erro: biblioteca de estado ausente: %s\n' "$STATE_LIB" >&2; exit 1; }
# shellcheck source=lib/contabase-install-state.sh
. "$STATE_LIB" || { printf '%s\n' 'Erro: falha ao carregar o contrato de estado.' >&2; exit 1; }
IMAGE_MODE=false
IMAGE_SUCCESS=false
INSTALL_DIR_CREATED=false
STACK_STARTED=false
DOCKER_HOME="/nonexistent"
IMAGE_COMPOSE_ARGS=()
CONTABASE_TMP_DIR=""

if [ "${CONTABASE_INSTALL_MODE:-}" = "image" ] || [ -n "${CONTABASE_IMAGE_INDEX_DIGEST:-}" ] \
  || [ -n "${CONTABASE_IMAGE_REFERENCE:-}" ]; then
  IMAGE_MODE=true
fi

say() {
  printf '%s\n' "$*"
}

blank() {
  printf '\n'
}

section() {
  blank
  say "$1"
}

random_base64() {
  bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$bytes" | tr -d '\n'
  else
    head -c "$bytes" /dev/urandom | base64 | tr -d '\n'
  fi
}

random_hex() {
  bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes" | tr -d '\n'
  else
    od -An -N"$bytes" -tx1 /dev/urandom | tr -d ' \n'
  fi
}

set_env_var() {
  key="$1"
  value="$2"
  file="${3:-.env.docker}"
  tmp="$(mktemp "${TMPDIR:-/tmp}/contabase-env.XXXXXX")"

  if grep -q "^${key}=" "$file"; then
    awk -v key="$key" -v value="$value" '
      BEGIN { prefix = key "=" }
      index($0, prefix) == 1 { $0 = prefix value }
      { print }
    ' "$file" > "$tmp"
    cat "$tmp" > "$file"
  else
    cat "$file" > "$tmp"
    printf '%s=%s\n' "$key" "$value" >> "$tmp"
    cat "$tmp" > "$file"
  fi

  rm -f "$tmp"
}

env_var_has_value() {
  key="$1"
  file="${2:-.env.docker}"
  awk -v key="$key" '
    BEGIN { found = 0 }
    index($0, key "=") == 1 {
      value = substr($0, length(key) + 2)
      gsub(/[[:space:]]/, "", value)
      if (value != "") {
        found = 1
      }
    }
    END { exit found ? 0 : 1 }
  ' "$file"
}

# Read raw value of a key from env file. Silent; value via stdout.
env_load_value() {
  key="$1"
  file="${2:-.env.docker}"
  [ -f "$file" ] || { printf ''; return 0; }
  awk -v k="$key" 'index($0, k"=") == 1 { v = substr($0, length(k) + 2); gsub(/[[:space:]]/, "", v); print v; exit }' "$file"
}

# Detect known placeholder patterns.
env_is_placeholder() {
  local val="$1"
  [ -z "$val" ] && return 0
  case "$val" in
    __PREENCHA_*|__GERAR_COM_*|CHANGE_ME*|change_me*|REPLACE_ME*|replace_me*|YOUR_*|your_*|PLACEHOLDER*|placeholder*) return 0 ;;
    *) return 1 ;;
  esac
}

# Check if key has a real (non-empty, non-placeholder) value.
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

# Set a key only if it does not have a real value. Replaces placeholder/empty lines.
set_env_var_safe() {
  local key="$1"
  local value="$2"
  local file="${3:-.env.docker}"

  if env_key_is_set "$key" "$file"; then
    return 0
  fi
  set_env_var "$key" "$value" "$file"
}

set_env_var_for_profile() {
  local key="$1"
  local value="$2"
  local file="${3:-.env.docker}"

  set_env_var "$key" "$value" "$file"
}

# Restrict an existing env file without changing its ownership.
secure_env_file() {
  local file="${1:-.env.docker}"

  [ -f "$file" ] || return 0
  if chmod 0600 "$file" 2>/dev/null; then
    return 0
  fi
  if as_root chmod 0600 "$file"; then
    return 0
  fi
  say "Erro: nao foi possivel restringir ${file} para 0600." >&2
  return 1
}

# Create timestamped backup of the env file.
env_backup() {
  local file="${1:-.env.docker}"
  [ -f "$file" ] || return 0
  local backup="${file}.$(date +%Y%m%d-%H%M%S).bak"
  if cp "$file" "$backup"; then
    if ! chmod 0600 "$backup" 2>/dev/null && ! as_root chmod 0600 "$backup"; then
      say "Erro: nao foi possivel restringir o backup ${backup} para 0600." >&2
      return 1
    fi
    say "Backup de ${file} criado em: ${backup}"
  fi
}

# Validate mandatory secrets exist. Returns 0 if all ok, 1 if any missing.
env_validate_secrets() {
  local file="${1:-.env.docker}"
  local missing=()
  local key

  for key in AUTH_ENCRYPTION_KEY SECURITY_MASTER_KEY CONTABASE_SETUP_TOKEN; do
    env_key_is_set "$key" "$file" || missing+=("$key")
  done

  [ "${#missing[@]}" -eq 0 ] && return 0

  say ""
  say "====================================================================="
  say "ERRO: Secret(s) obrigatorio(s) ausente(s) ou com placeholder em ${file}:"
  for key in "${missing[@]}"; do
    say "  - ${key}"
  done
  say "====================================================================="
  return 1
}

ensure_secret() {
  local key="$1"
  local generator="$2"
  local file="${3:-.env.docker}"
  local is_new="${4:-false}"
  local value

  # Key already has a real value — preserve
  if env_key_is_set "$key" "$file"; then
    say "  [OK] ${key} ja configurado (valor preservado)."
    return 0
  fi

  # Key is missing, empty, or placeholder
  say "  [ALERTA] ${key} ausente, vazio ou com placeholder."

  # New installation — auto-generate always (headless or interactive)
  if [ "$is_new" = "true" ]; then
    : # fall through to generation below
  # Existing installation, headless — fail
  elif [ "$INTERACTIVE" = false ]; then
    say ""
    say "====================================================================="
    say "ERRO (modo headless): ${key} ausente em instalacao existente."
    say "Nao e possivel gerar automaticamente."
    say "Edite ${file} manualmente e execute novamente:"
    say "  - ${key}=<valor seguro>"
    say "====================================================================="
    return 1
  else
    # Interactive mode — ask before generating
    read -r -p "Gerar novo valor para ${key}? (s/N) " answer
    case "$answer" in
      s|S|sim|SIM) ;;
      *) say "  [SKIP] ${key} nao foi gerado. Defina manualmente em ${file}."; return 1 ;;
    esac
  fi

  # Generate secret
  case "$generator" in
    setup-token) value="$(random_base64 48)" ;;
    auth-key) value="$(random_base64 32)" ;;
    master-key) value="$(random_hex 16)" ;;
    *)
      say "Erro: gerador desconhecido para $key."
      exit 1
      ;;
  esac
  set_env_var "$key" "$value" "$file"
  say "  [NEW] ${key} gerado (valor nao exibido)."
}

normalize_domain() {
  domain="$1"
  domain="${domain#http://}"
  domain="${domain#https://}"
  domain="${domain%%/*}"
  domain="${domain%%:*}"
  printf '%s' "$domain"
}

is_private_ipv4() {
  local ip="$1"
  local a b c d

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

read_public_version_file() {
  [ -f VERSION ] || { printf '%s' "dev"; return 0; }
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

write_install_state() {
  local method="$1"
  local channel="$2"
  local version="$3"
  local repo_path="$4"
  local kind="${5:-}"
  local ref="${6:-}"
  local commit="${7:-}"
  local origin="${8:-}"
  local state_dir state_tmp

  if [ -z "$kind" ]; then
    kind="$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF_KIND)"
    ref="$(state_value "$STATE_FILE" CONTABASE_INSTALL_REF)"
    origin="$(state_value "$STATE_FILE" CONTABASE_INSTALL_ORIGIN)"
    if [ -n "$kind" ]; then
      commit="$(git rev-parse --verify 'HEAD^{commit}' 2>/dev/null || true)"
    else
      commit=""
    fi
  elif [ -z "$commit" ]; then
    commit="$(git rev-parse --verify 'HEAD^{commit}' 2>/dev/null || true)"
  fi

  state_dir="$(dirname "$STATE_FILE")"
  as_root mkdir -p "$state_dir" || return 1
  state_tmp="$(mktemp "${TMPDIR:-/tmp}/contabase-install-state.XXXXXX")" || return 1
  if ! state_render "$STATE_FILE" "$method" "$channel" "$version" "$repo_path" \
    "$kind" "$ref" "$commit" "$origin" > "$state_tmp"; then
    rm -f "$state_tmp"
    return 1
  fi

  if ! as_root sh -c 'umask 077; tmp="$1.tmp.$$"; trap "rm -f \"$tmp\"" EXIT; cat > "$tmp" && chmod 0644 "$tmp" && mv "$tmp" "$1"' _ "$STATE_FILE" < "$state_tmp"; then
    rm -f "$state_tmp"
    return 1
  fi
  rm -f "$state_tmp"
}

show_override_warning() {
  say "Revise docker-compose.override.yml antes de expor publicamente."
  say "- Use-o para portas, redes Docker, labels de proxy e IP fixo."
  say "- Nao edite docker-compose.yml."
  say "- Se usar proxy/tunnel no mesmo host, prefira 127.0.0.1:8080:8080 ou rede Docker compartilhada."
  say "- Exposicao publica direta sem HTTPS/proxy/tunnel nao e recomendada e pode ser bloqueada."
}

create_override_if_requested() {
  if [ -f "docker-compose.override.yml" ]; then
    say "OK: docker-compose.override.yml ja existe e foi preservado."
    return
  fi

  if [ ! -f "docker-compose.override.example.yml" ]; then
    return
  fi

  if [ "$INTERACTIVE" = true ]; then
    read -r -p "Deseja criar docker-compose.override.yml para customizacoes? (s/N) " reply
    if printf '%s' "$reply" | grep -qE '^[Ss]$'; then
      cp docker-compose.override.example.yml docker-compose.override.yml
      say "OK: docker-compose.override.yml criado a partir do exemplo."
      show_override_warning
    fi
  else
    say "Pulando criacao do override (modo automatico)."
  fi
}

ensure_override_for_profile() {
  if [ ! -f "docker-compose.override.yml" ] && [ -f "docker-compose.override.example.yml" ]; then
    cp docker-compose.override.example.yml docker-compose.override.yml
    say "OK: docker-compose.override.yml criado para customizacoes locais."
    show_override_warning
  fi
}

as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  elif command -v sudo >/dev/null 2>&1; then
    sudo "$@"
  else
    return 1
  fi
}

# Restrict the container data volume without following symlinks.
# Ownership remains scoped to data/ and is set to the container UID/GID.
secure_data_permissions() {
  local data_dir="data"

  if [ -L "$data_dir" ] || { [ -e "$data_dir" ] && [ ! -d "$data_dir" ]; }; then
    say "Erro: ${data_dir}/ deve ser um diretorio real, sem symlink." >&2
    return 1
  fi
  [ -d "$data_dir" ] || {
    say "Erro: ${data_dir}/ ausente." >&2
    return 1
  }

  # Pre-create the known container subdirectories so the application does not
  # create them with a permissive umask on first start.
  for subdir in backups uploads uploads/profile uploads/workspaces; do
    if ! mkdir -p "$data_dir/$subdir" 2>/dev/null; then
      if ! as_root mkdir -p "$data_dir/$subdir"; then
        say "Erro: nao foi possivel criar ${data_dir}/${subdir}." >&2
        return 1
      fi
    fi
  done

  if ! find "$data_dir" ! -type l ! -uid 1000 -exec chown 1000:1000 {} + 2>/dev/null; then
    if ! as_root find "$data_dir" ! -type l ! -uid 1000 -exec chown 1000:1000 {} +; then
      say "Erro: nao foi possivel ajustar ownership de ${data_dir}/ para 1000:1000." >&2
      return 1
    fi
  fi

  if ! find "$data_dir" -type d -exec chmod 0700 {} + 2>/dev/null; then
    if ! as_root find "$data_dir" -type d -exec chmod 0700 {} +; then
      say "Erro: nao foi possivel restringir diretorios em ${data_dir}/." >&2
      return 1
    fi
  fi
  if ! find "$data_dir" -type f -exec chmod 0600 {} + 2>/dev/null; then
    if ! as_root find "$data_dir" -type f -exec chmod 0600 {} +; then
      say "Erro: nao foi possivel restringir arquivos em ${data_dir}/." >&2
      return 1
    fi
  fi
  return 0
}

# Reapply file modes after the container starts and creates SQLite/WAL/SHM.
secure_runtime_data_files() {
  local data_dir="data" attempt path

  for attempt in $(seq 1 15); do
    [ -f "$data_dir/contabase.db" ] && break
    sleep 1
  done

  for path in "$data_dir/contabase.db" "$data_dir/contabase.db-wal" "$data_dir/contabase.db-shm"; do
    [ -e "$path" ] && [ ! -L "$path" ] || continue
    if ! chmod 0600 "$path" 2>/dev/null && ! as_root chmod 0600 "$path"; then
      say "Erro: nao foi possivel restringir ${path} para 0600." >&2
      return 1
    fi
  done

  for path in "$data_dir/backups" "$data_dir/uploads"; do
    [ -d "$path" ] && [ ! -L "$path" ] || continue
    if ! chmod 0700 "$path" 2>/dev/null && ! as_root chmod 0700 "$path"; then
      say "Erro: nao foi possivel restringir ${path} para 0700." >&2
      return 1
    fi
  done
  return 0
}

is_debian_like() {
  [ -f /etc/os-release ] || return 1
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID:-} ${ID_LIKE:-}" in
    *debian*|*ubuntu*) return 0 ;;
    *) return 1 ;;
  esac
}

ca_certificates_installed() {
  if command -v dpkg-query >/dev/null 2>&1; then
    dpkg-query -W -f='${Status}' ca-certificates 2>/dev/null | grep -q "install ok installed"
    return
  fi
  [ -d /etc/ssl/certs ]
}

collect_missing_dependencies() {
  local missing=()

  command -v curl >/dev/null 2>&1 || missing+=("curl")
  command -v openssl >/dev/null 2>&1 || missing+=("openssl")
  command -v python3 >/dev/null 2>&1 || missing+=("python3")
  ca_certificates_installed || missing+=("ca-certificates")
  command -v docker >/dev/null 2>&1 || missing+=("docker")
  if command -v docker >/dev/null 2>&1 && ! docker compose version >/dev/null 2>&1; then
    missing+=("docker compose")
  fi

  printf '%s\n' "${missing[@]}"
}

install_debian_dependencies() {
  say "Instalando dependencias via apt..."
  as_root apt-get update
  if ! as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y \
    ca-certificates curl openssl python3 docker.io docker-compose-plugin; then
    say "Pacote docker-compose-plugin indisponivel; tentando fallback docker-compose."
    as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y \
      ca-certificates curl openssl python3 docker.io docker-compose
  fi

  if command -v systemctl >/dev/null 2>&1; then
    as_root systemctl enable --now docker >/dev/null 2>&1 || true
  fi
  as_root service docker start >/dev/null 2>&1 || true
}

print_dependency_help() {
  local missing_text="$1"
  say "Dependencias ausentes:"
  printf '%s\n' "$missing_text" | sed '/^$/d;s/^/  - /'
  say ""
  say "Instale Docker Engine, Docker Compose v2, curl, openssl, ca-certificates e python3."
  say "Em Debian/Ubuntu, execute novamente em modo interativo para autorizar a instalacao,"
  say "ou use CONTABASE_INSTALL_DEPS=1 em modo headless se quiser instalar via apt."
}

ensure_dependencies() {
  local missing_text answer

  missing_text="$(collect_missing_dependencies | sed '/^$/d')"
  [ -n "$missing_text" ] || return 0

  if [ "$CHECK_ONLY" = true ]; then
    print_dependency_help "$missing_text"
    exit 1
  fi

  if ! is_debian_like; then
    print_dependency_help "$missing_text"
    say "Sistema nao suportado para instalacao automatica de dependencias."
    exit 1
  fi

  if [ "$INTERACTIVE" = true ] && [ -t 0 ]; then
    print_dependency_help "$missing_text"
    read -r -p "Docker/Compose nao estao instalados. Deseja instalar as dependencias agora? [s/N]: " answer
    case "$answer" in
      s|S|sim|SIM|Sim) ;;
      *) say "Instalacao cancelada. Instale as dependencias e rode novamente."; exit 1 ;;
    esac
  elif [ "${CONTABASE_INSTALL_DEPS:-0}" != "1" ]; then
    print_dependency_help "$missing_text"
    say "Modo headless: defina CONTABASE_INSTALL_DEPS=1 para autorizar instalacao via apt."
    exit 1
  else
    say "CONTABASE_INSTALL_DEPS=1 definido; instalando dependencias ausentes via apt."
  fi

  install_debian_dependencies

  missing_text="$(collect_missing_dependencies | sed '/^$/d')"
  if [ -n "$missing_text" ]; then
    print_dependency_help "$missing_text"
    say "Dependencias ainda ausentes apos tentativa de instalacao."
    exit 1
  fi
}

image_fail() {
  say "Erro: $*" >&2
}

docker_run() {
  local -a environment=(
    "PATH=${PATH}"
    "HOME=${DOCKER_HOME}"
    "DOCKER_CONFIG=${DOCKER_HOME}/.docker"
    "LANG=C"
    "COMPOSE_PROJECT_NAME=contabase"
    "VERSION=${CONTABASE_VERSION:-}"
  )
  if [ -n "${DOCKER_HOST:-}" ]; then
    environment+=("DOCKER_HOST=${DOCKER_HOST}")
  fi
  if [ -n "${CONTABASE_TMP_DIR:-}" ]; then
    environment+=("TMPDIR=${CONTABASE_TMP_DIR}")
  fi
  env -i "${environment[@]}" docker "$@"
}

ensure_image_dependencies() {
  local missing=() command_name security

  for command_name in bash tar openssl docker python3 sqlite3 git awk stat sort wc tr date \
                      mktemp mkdir cp find rm mv cat chmod chown; do
    command -v "$command_name" >/dev/null 2>&1 || missing+=("$command_name")
  done
  if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
    missing+=("sha256sum ou shasum")
  fi
  if [ "${#missing[@]}" -gt 0 ]; then
    image_fail "dependencias ausentes para imagem GHCR: ${missing[*]}"
    return 1
  fi
  python3 -I -B -c 'import sys; sys.exit(0 if sys.version_info >= (3, 9) else 1)' \
    || { image_fail "Python 3.9+ obrigatorio para instalar e atualizar docker-image"; return 1; }
  if ! docker_run compose version >/dev/null 2>&1; then
    image_fail "Docker Compose v2 de sistema indisponivel; plugins de usuario nao sao usados nesta fase"
    return 1
  fi
  security="$(docker_run info --format '{{json .SecurityOptions}}' 2>/dev/null)" \
    || { image_fail "Docker indisponivel; nenhuma escrita foi executada"; return 1; }
  assert_not_rootless "$security" || return 1
}

check_conflicting_docker_resources() {
  local install_dir="$1" ids id name labels mounts source count volume projects
  local -a items

  ids="$(docker_run ps -aq)" || { image_fail "nao foi possivel listar containers"; return 1; }
  count="$(printf '%s\n' "$ids" | sed '/^$/d' | wc -l | tr -d ' ')"
  [ "$count" -le 1000 ] || { image_fail "inventario Docker grande demais; operacao bloqueada"; return 1; }
  for id in $ids; do
    case "$id" in
      *[!0-9a-f]*|"") image_fail "inventario Docker ambiguo"; return 1 ;;
    esac
    [ "${#id}" -ge 12 ] || { image_fail "id de container invalido"; return 1; }
    name="$(docker_run inspect --format '{{.Name}}' "$id" 2>/dev/null)" \
      || { image_fail "nao foi possivel inspecionar container existente"; return 1; }
    case "$(printf '%s' "$name" | tr '[:upper:]' '[:lower:]')" in
      *contabase*) image_fail "container ContaBase existente; use atualizacao explicita"; return 1 ;;
    esac
    labels="$(docker_run inspect --format '{{json .Config.Labels}}' "$id" 2>/dev/null)" \
      || { image_fail "nao foi possivel inspecionar labels existentes"; return 1; }
    case "$(printf '%s' "$labels" | tr '[:upper:]' '[:lower:]')" in
      *contabase*) image_fail "container/Compose ContaBase existente; use atualizacao explicita"; return 1 ;;
    esac
    mounts="$(docker_run inspect --format '{{range .Mounts}}{{.Source}}|{{end}}' "$id" 2>/dev/null)" \
      || { image_fail "nao foi possivel inspecionar mounts existentes"; return 1; }
    IFS='|' read -ra items <<< "$mounts"
    for source in "${items[@]}"; do
      [ -n "$source" ] || continue
      if docker_image_path_overlaps "$source" "$install_dir" \
        || docker_image_path_overlaps "$source" "$install_dir/data"; then
        image_fail "container existente usa o diretorio selecionado: $source"
        return 1
      fi
    done
  done

  volume=""
  while IFS= read -r volume; do
    [ -n "$volume" ] || continue
    case "$volume" in
      *contabase*) image_fail "volume Docker conflitante: $volume"; return 1 ;;
    esac
  done < <(docker_run volume ls -q 2>/dev/null || true)

  projects="$(docker_run compose ls -q 2>/dev/null || true)"
  if printf '%s\n' "$projects" | grep -qx 'contabase'; then
    image_fail "projeto Compose contabase existente; use atualizacao explicita"
    return 1
  fi
}

copy_prepared_checkout() {
  local source="$1" destination="$2"

  if find "$source" -type l -print -quit | grep -q .; then
    image_fail "checkout preparado contem symlink"
    return 1
  fi
  (cd "$source" && tar -cf - .) | (cd "$destination" && tar -xf -) \
    || { image_fail "falha ao copiar checkout preparado"; return 1; }
}

write_image_env() {
  local directory="$1"
  local env_file="$directory/.env.docker"

  [ -f "$directory/.env.docker.example" ] \
    || { image_fail ".env.docker.example ausente no checkout"; return 1; }
  [ ! -e "$env_file" ] && [ ! -L "$env_file" ] \
    || { image_fail ".env.docker existente; operacao bloqueada"; return 1; }
  cp "$directory/.env.docker.example" "$env_file" \
    || { image_fail "falha ao criar .env.docker"; return 1; }
  chmod 0600 "$env_file" || { image_fail "falha ao proteger .env.docker"; return 1; }

  set_env_var "PORT" "8080" "$env_file"
  set_env_var "APP_BASE_URL" "$APP_BASE_URL" "$env_file"
  set_env_var "ALLOWED_HOSTS" "$ALLOWED_HOSTS" "$env_file"
  set_env_var "TRUSTED_PROXIES" "$TRUSTED_PROXIES" "$env_file"
  set_env_var "CONTABASE_ACCESS_MODE" "$CONTABASE_ACCESS_MODE" "$env_file"
  set_env_var "CONTABASE_FEATURE_INVESTMENTS" "false" "$env_file"
  set_env_var "DATA_DIR" "/app/data" "$env_file"
  set_env_var "DB_FILE" "/app/data/contabase.db" "$env_file"
  set_env_var "UPLOADS_DIR" "/app/data/uploads" "$env_file"
  set_env_var "CONTABASE_TIMEZONE" "$CONTABASE_TIMEZONE_VALUE" "$env_file"

  ensure_secret "CONTABASE_SETUP_TOKEN" "setup-token" "$env_file" "true" \
    || { image_fail "falha ao gerar CONTABASE_SETUP_TOKEN"; return 1; }
  ensure_secret "AUTH_ENCRYPTION_KEY" "auth-key" "$env_file" "true" \
    || { image_fail "falha ao gerar AUTH_ENCRYPTION_KEY"; return 1; }
  ensure_secret "SECURITY_MASTER_KEY" "master-key" "$env_file" "true" \
    || { image_fail "falha ao gerar SECURITY_MASTER_KEY"; return 1; }
  env_validate_secrets "$env_file" \
    || { image_fail "secrets obrigatorios ausentes em .env.docker"; return 1; }
  chmod 0600 "$env_file" || { image_fail "falha ao proteger .env.docker"; return 1; }
}

write_image_state() {
  local install_dir="$1" temporary state_dir

  state_dir="$(dirname "$STATE_FILE")"
  [ ! -L "$state_dir" ] && [ ! -L "$STATE_FILE" ] && [ ! -L "$HISTORY_FILE" ] \
    || { image_fail "caminho de estado contem symlink"; return 1; }

  temporary="$(mktemp "${TMPDIR:-/tmp}/contabase-state.XXXXXX")" \
    || { image_fail "falha ao preparar estado da instalacao"; return 1; }
  {
    printf 'CONTABASE_STATE_VERSION=1\n'
    printf 'CONTABASE_INSTALL_METHOD=%s\n' "$CONTABASE_IMAGE_METHOD"
    printf 'CONTABASE_CHANNEL=%s\n' "$CONTABASE_CHANNEL"
    printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$CONTABASE_VERSION"
    printf 'CONTABASE_COMMIT=%s\n' "$CONTABASE_COMMIT"
    printf 'CONTABASE_REPO_PATH=%s\n' "$install_dir"
    printf 'CONTABASE_IMAGE_REFERENCE=%s\n' "$CONTABASE_IMAGE_REFERENCE"
    printf 'CONTABASE_IMAGE_INDEX_DIGEST=%s\n' "$CONTABASE_IMAGE_INDEX_DIGEST"
    printf 'CONTABASE_IMAGE_PLATFORM=%s\n' "$CONTABASE_IMAGE_PLATFORM"
    printf 'CONTABASE_IMAGE_PLATFORM_DIGEST=%s\n' "$CONTABASE_IMAGE_PLATFORM_DIGEST"
    printf 'CONTABASE_IMAGE_PROOF_SOURCE=bootstrap-catalog\n'
    printf 'CONTABASE_COMPOSE_IMAGE_FILE=%s\n' "$CONTABASE_IMAGE_COMPOSE_FILE"
  } > "$temporary" || { rm -f "$temporary"; image_fail "falha ao preparar estado"; return 1; }

  install_image_update_command "$install_dir" "$temporary" \
    || { rm -f "$temporary"; image_fail "falha na publicacao exclusiva de comando e estado"; return 1; }
  rm -f "$temporary"
}

wait_image_health() {
  local deadline=$((SECONDS + 180)) ids status

  while [ "$SECONDS" -lt "$deadline" ]; do
    ids="$(docker_run "${IMAGE_COMPOSE_ARGS[@]}" ps -q contabase 2>/dev/null || true)"
    if [ -n "$ids" ]; then
      status="$(docker_run inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$ids" 2>/dev/null || true)"
      case "$status" in
        healthy) return 0 ;;
        unhealthy|exited|dead) return 1 ;;
      esac
    fi
    sleep 3
  done
  return 1
}

image_exit_handler() {
  local status=$?

  if [ -n "${CONTABASE_TMP_DIR:-}" ] && [ -d "$CONTABASE_TMP_DIR" ]; then
    rm -rf "$CONTABASE_TMP_DIR" 2>/dev/null || true
  fi
  if [ "$IMAGE_SUCCESS" != "true" ]; then
    if [ "$STACK_STARTED" = "true" ] && [ "${#IMAGE_COMPOSE_ARGS[@]}" -gt 0 ]; then
      docker_run "${IMAGE_COMPOSE_ARGS[@]}" down --remove-orphans >/dev/null 2>&1 || true
    fi
    say "Instalacao interrompida. Nenhum dado preexistente foi removido." >&2
    if [ "$INSTALL_DIR_CREATED" = "true" ]; then
      say "Diretorio preservado para diagnostico: ${CONTABASE_INSTALL_DIR:-}" >&2
      say "Remova manualmente apenas depois de conferir que nao ha dados a preservar." >&2
    fi
  fi
  exit "$status"
}

install_image_update_command() {
  local install_dir="$1" state_payload="$2"
  python3 -I -B - "$install_dir/scripts/lib/contabase-docker-image-update.py" \
    "$install_dir" "$STATE_FILE" "$HISTORY_FILE" "$state_payload" <<'PY'
import importlib.util
from pathlib import Path
import sys
try:
    spec = importlib.util.spec_from_file_location('image_update', sys.argv[1])
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.install_image_update_command(*sys.argv[2:5], Path(sys.argv[5]).read_bytes())
except Exception:
    sys.exit('Erro: comando/estado nao instalados; destinos preexistentes preservados.')
PY
}

print_image_summary() {
  local install_dir="$1"

  blank
  say "======================================================================"
  say "CONCLUIDO: ContaBase instalado por imagem GHCR fixada por digest."
  say "======================================================================"
  say "Versao: ${CONTABASE_VERSION}"
  say "Canal: ${CONTABASE_CHANNEL}"
  say "Imagem: ${CONTABASE_IMAGE_REFERENCE}"
  say "Plataforma: ${CONTABASE_IMAGE_PLATFORM}"
  say "Acesso: ${APP_BASE_URL}"
  say "Diretorio: ${install_dir}"
  say "Token de setup: leia CONTABASE_SETUP_TOKEN em ${install_dir}/.env.docker (modo 0600)."
  say "Nao imprima esse valor em logs ou canais compartilhados."
  blank
  say "Atualizacao por digest: sudo contabase-update (sem recuperacao automatica)."
  say "O caminho docker-image ainda depende de revisao independente e homologacao B8-6."
  say "A instalacao serve para homologacao; nao esta declarada pronta para producao."
}

run_image_installation() {
  trap image_exit_handler EXIT
  INTERACTIVE=false

  [ "$(id -u)" -eq 0 ] \
    || { image_fail "instalacao docker-image exige root desde o inicio; execute o Bootstrap como root"; return 1; }

  [ -f "$IMAGE_LIB" ] || { image_fail "biblioteca do adaptador Docker ausente"; return 1; }
  # shellcheck source=lib/contabase-docker-image.sh
  . "$IMAGE_LIB" || { image_fail "falha ao carregar contrato do adaptador Docker"; return 1; }

  validate_image_contract || return 1
  validate_network_config || return 1
  validate_install_paths || return 1
  validate_no_existing_state || return 1
  validate_docker_endpoint || return 1
  ensure_image_dependencies || return 1

  local checkout_source install_dir data_dir parent host_arch
  checkout_source="${CONTABASE_BOOTSTRAP_CHECKOUT:-$(pwd -P)}"
  checkout_source="$(cd "$checkout_source" 2>/dev/null && pwd -P)" \
    || { image_fail "checkout preparado ausente"; return 1; }
  [ "$checkout_source" = "$(pwd -P)" ] \
    || { image_fail "checkout diverge do diretorio de execucao"; return 1; }
  [ -f "$checkout_source/VERSION" ] \
    && [ "$(tr -d '[:space:]' < "$checkout_source/VERSION")" = "$CONTABASE_VERSION" ] \
    || { image_fail "VERSION do checkout diverge da versao fixada"; return 1; }
  local required
  for required in docker-compose.yml Dockerfile .env.docker.example \
                  scripts/install-contabase-docker.sh scripts/lib/contabase-update-wrapper.sh \
                  scripts/lib/contabase-docker-image-update.py scripts/ops/backup.sh; do
    [ -f "$checkout_source/$required" ] \
      || { image_fail "checkout incompleto: $required"; return 1; }
  done
  python3 -I -B - "$checkout_source/scripts/ops/backup.sh" <<'PY'
import hashlib
from pathlib import Path
import sys
helper = Path(sys.argv[1])
sys.exit(0 if helper.is_file() and not helper.is_symlink() and
         hashlib.sha256(helper.read_bytes()).hexdigest() ==
         '6b9382927526cdd77b0d46fec7c6ff410bf003b69420a34aa080970d0956bc72' else 1)
PY
  [ "$?" -eq 0 ] || { image_fail "helper de backup ausente ou divergente"; return 1; }
  [ -d "$checkout_source/scripts/lib" ] \
    || { image_fail "checkout incompleto: scripts/lib"; return 1; }
  [ "$(uname -s)" = "Linux" ] || { image_fail "instalacao Docker exige Linux"; return 1; }
  case "$(uname -m)" in
    x86_64|amd64) host_arch="amd64" ;;
    aarch64|arm64) host_arch="arm64" ;;
    *) image_fail "arquitetura nao suportada; use amd64 ou arm64"; return 1 ;;
  esac
  [ "$CONTABASE_IMAGE_PLATFORM" = "linux/$host_arch" ] \
    || { image_fail "plataforma homologada diverge do host"; return 1; }

  install_dir="${CONTABASE_INSTALL_DIR%/}"
  data_dir="${CONTABASE_DATA_DIR%/}"
  python3 -I -B - "$checkout_source/scripts/lib/contabase-docker-image-update.py" \
    "$install_dir" "$STATE_FILE" "$HISTORY_FILE" <<'PY'
import importlib.util
import sys
try:
    spec = importlib.util.spec_from_file_location('image_update', sys.argv[1])
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.installation_preflight(*sys.argv[2:5])
except Exception:
    sys.exit('Erro: caminhos administrativos exigem ownership root e permissoes seguras.')
PY
  check_conflicting_docker_resources "$install_dir" || return 1

  blank
  say "======================================================================"
  say "ContaBase - instalacao Docker por imagem GHCR homologada"
  say "======================================================================"
  say "Versao: ${CONTABASE_VERSION}"
  say "Imagem: ${CONTABASE_IMAGE_REFERENCE}"
  say "Plataforma: ${CONTABASE_IMAGE_PLATFORM}"
  say "Acesso: ${APP_BASE_URL}"
  say "Porta do host: ${CONTABASE_PORT} (interna fixa: 8080)"
  say "Bind: ${CONTABASE_HOST_BIND}"
  say "Diretorio: ${install_dir}"
  say "Dados: ${data_dir}"

  if [ "$CHECK_ONLY" = "true" ]; then
    blank
    say "Modo --check: contrato de imagem valido; nenhuma escrita foi executada."
    IMAGE_SUCCESS=true
    return 0
  fi

  # Revalidacao imediatamente antes da criacao exclusiva.
  validate_install_paths || return 1
  validate_no_existing_state || return 1
  check_conflicting_docker_resources "$install_dir" || return 1

  parent="$(dirname "$install_dir")"
  [ -d "$parent" ] && [ ! -L "$parent" ] \
    || { image_fail "diretorio pai ausente ou symlink"; return 1; }
  mkdir "$install_dir" \
    || { image_fail "nao foi possivel criar ${install_dir}; instalacao existente ou sem permissao"; return 1; }
  [ -d "$install_dir" ] && [ ! -L "$install_dir" ] \
    || { image_fail "diretorio criado nao e um diretorio real"; return 1; }
  INSTALL_DIR_CREATED=true

  local nonce
  nonce="$(random_hex 16)"
  printf '%s\n' "$nonce" > "$install_dir/.contabase-bootstrap-install" \
    || { image_fail "falha ao registrar marcador de instalacao"; return 1; }
  chmod 0600 "$install_dir/.contabase-bootstrap-install" || true

  copy_prepared_checkout "$checkout_source" "$install_dir" || return 1
  chmod 0755 "$install_dir"
  [ "$(tr -d '[:space:]' < "$install_dir/VERSION")" = "$CONTABASE_VERSION" ] \
    || { image_fail "VERSION copiada diverge da versao fixada"; return 1; }
  [ -d "$install_dir/.git" ] \
    || { image_fail "checkout copiado sem metadados Git"; return 1; }
  local observed_commit
  observed_commit="$(git -C "$install_dir" rev-parse HEAD 2>/dev/null)" \
    || { image_fail "nao foi possivel confirmar o commit do checkout"; return 1; }
  [ "$observed_commit" = "$CONTABASE_COMMIT" ] \
    || { image_fail "commit do checkout diverge do commit homologado"; return 1; }

  CONTABASE_TMP_DIR="$install_dir/.contabase-tmp"
  mkdir "$CONTABASE_TMP_DIR" \
    || { image_fail "falha ao criar diretorio temporario privado"; return 1; }
  chmod 0700 "$CONTABASE_TMP_DIR" || { image_fail "falha ao proteger temporarios"; return 1; }
  export TMPDIR="$CONTABASE_TMP_DIR"

  mkdir "$install_dir/data" \
    || { image_fail "falha ao criar diretorio de dados"; return 1; }
  chmod 0750 "$install_dir/data" || { image_fail "falha ao ajustar permissao de dados"; return 1; }
  if ! chown -R 1000:1000 "$install_dir/data" 2>/dev/null; then
    if command -v sudo >/dev/null 2>&1; then
      sudo chown -R 1000:1000 "$install_dir/data" \
        || { image_fail "falha ao ajustar UID/GID 1000 em data/"; return 1; }
    else
      image_fail "nao foi possivel ajustar UID/GID 1000 em data/"
      return 1
    fi
  fi
  assert_local_storage "$install_dir/data" || return 1

  write_image_env "$install_dir" || return 1

  DOCKER_HOME="$install_dir/.docker-home"
  mkdir -p "$DOCKER_HOME/.docker" \
    || { image_fail "falha ao criar HOME isolado do Docker"; return 1; }
  chmod 0700 "$DOCKER_HOME" "$DOCKER_HOME/.docker" || true

  write_image_compose "$install_dir/$CONTABASE_IMAGE_COMPOSE_FILE" \
    "$CONTABASE_IMAGE_REFERENCE" "$CONTABASE_HOST_BIND" "$CONTABASE_PORT" \
    "$APP_BASE_URL" "$ALLOWED_HOSTS" "$TRUSTED_PROXIES" \
    "$CONTABASE_ACCESS_MODE" "$CONTABASE_TIMEZONE_VALUE" \
    || { image_fail "falha ao gerar Compose da imagem"; return 1; }

  IMAGE_COMPOSE_ARGS=(
    compose
    --project-directory "$install_dir"
    -f "$install_dir/$CONTABASE_IMAGE_COMPOSE_FILE"
  )

  local rendered
  rendered="$(docker_run "${IMAGE_COMPOSE_ARGS[@]}" config 2>/dev/null)" \
    || { image_fail "Compose da imagem invalido"; return 1; }
  assert_rendered_compose "$rendered" "$CONTABASE_IMAGE_REFERENCE" \
    "$CONTABASE_HOST_BIND" "$CONTABASE_PORT" "$APP_BASE_URL" "$ALLOWED_HOSTS" \
    "$TRUSTED_PROXIES" "$CONTABASE_ACCESS_MODE" || return 1
  unset rendered

  say "Baixando imagem fixada por digest..."
  docker_run "${IMAGE_COMPOSE_ARGS[@]}" pull \
    || { image_fail "falha ao baixar a imagem homologada"; return 1; }

  local observed
  observed="$(docker_run image inspect --format '{{json .RepoDigests}}' "$CONTABASE_IMAGE_REFERENCE" 2>/dev/null)" \
    || { image_fail "imagem puxada nao encontrada pelo digest"; return 1; }
  assert_repo_digest "$observed" "$CONTABASE_IMAGE_REFERENCE" || return 1

  STACK_STARTED=true
  docker_run "${IMAGE_COMPOSE_ARGS[@]}" up -d --remove-orphans \
    || { image_fail "falha ao iniciar a stack"; return 1; }
  wait_image_health \
    || { image_fail "healthcheck nao ficou saudavel; stack sera parada e dados preservados"; return 1; }

  local runtime_version
  runtime_version="$(docker_run "${IMAGE_COMPOSE_ARGS[@]}" exec -T contabase cat /app/VERSION 2>/dev/null | tr -d '[:space:]')" \
    || { image_fail "nao foi possivel confirmar a versao no container"; return 1; }
  [ "$runtime_version" = "$CONTABASE_VERSION" ] \
    || { image_fail "versao em execucao diverge da versao fixada"; return 1; }

  write_image_state "$install_dir" \
    || { image_fail "instalacao ativa, mas estado nao registrado"; return 1; }

  rm -rf "$CONTABASE_TMP_DIR" 2>/dev/null || true
  CONTABASE_TMP_DIR=""
  rm -f "$install_dir/.contabase-bootstrap-install" || true
  IMAGE_SUCCESS=true
  print_image_summary "$install_dir"
}

for arg in "$@"; do
  case "$arg" in
    --help)
      say "ContaBase Instalador Guiado"
      blank
      say "Uso: ./scripts/install-contabase-docker.sh [opcoes]"
      blank
      say "Opcoes:"
      say "  --help       Exibe esta mensagem"
      say "  --yes        Aceita todos os padroes automaticamente (instalacao local)"
      say "  --check      Apenas verifica os pre-requisitos sem rodar o container"
      say "  --dry-run    Alias para --check"
      exit 0
      ;;
    --yes)
      INTERACTIVE=false
      ;;
    --check|--dry-run)
      CHECK_ONLY=true
      ;;
  esac
done

[ "${CONTABASE_ASSUME_YES:-0}" = "1" ] && INTERACTIVE=false

require_flock() {
  if ! command -v flock >/dev/null 2>&1; then
    say "Erro: flock obrigatorio para atualizacao segura. Instale util-linux antes de continuar."
    exit 1
  fi
}

require_flock

if [ "$IMAGE_MODE" = true ]; then
  run_image_installation
  exit $?
fi

say "======================================================================"
say "Iniciando Instalador do ContaBase"
say "======================================================================"

section "[1/5] Verificando pre-requisitos..."

if [ ! -f "docker-compose.yml" ]; then
  say "Erro: arquivo docker-compose.yml nao encontrado."
  say "Certifique-se de estar na raiz do repositorio ContaBase."
  exit 1
fi

if [ "$IMAGE_MODE" = false ]; then
  repo_root="$(pwd -P)"
  installed_version="${CONTABASE_VERSION:-$(read_public_version_file)}"
  if ! provenance_detect "$repo_root" "$installed_version"; then
    say "Erro: nao foi possivel provar a tag/branch, o commit ou a origem oficial do checkout."
    say "Use um checkout de tag publica ou uma branch com upstream da origem oficial."
    exit 1
  fi
fi

ensure_dependencies

say "OK: pre-requisitos atendidos."

if [ "$CHECK_ONLY" = true ]; then
  blank
  say "Modo --check ativado. Instalacao simulada com sucesso."
  say "Para instalar de verdade, rode o script sem a flag --check."
  exit 0
fi

section "[2/5] Configurando orquestracao Docker..."
say "Aviso: docker-compose.yml e oficial e nao deve ser editado manualmente."
say "Customizacoes locais devem ficar em docker-compose.override.yml."
create_override_if_requested

section "[3/5] Configurando ambiente local..."

env_is_new=false
if [ ! -f ".env.docker" ]; then
  if [ -f ".env.docker.example" ]; then
    cp .env.docker.example .env.docker
    secure_env_file ".env.docker" || exit 1
    say "OK: .env.docker criado a partir do exemplo (modo 0600)."
    env_is_new=true
  else
    say "Erro: .env.docker.example nao encontrado."
    exit 1
  fi
else
  say "OK: .env.docker ja existe."
  secure_env_file ".env.docker" || exit 1
  say "Fazendo backup antes de qualquer alteracao..."
  env_backup ".env.docker" || exit 1

  # Validate secrets in existing env (headless: fail; interactive: offer to fix)
  if [ "$INTERACTIVE" = false ]; then
    if ! env_validate_secrets ".env.docker"; then
      say ""
      say "Corrija os secrets em .env.docker e execute novamente."
      exit 1
    fi
    say "Secrets validados com sucesso (modo headless)."
  fi
fi

say ""
say "Verificando secrets obrigatorios..."
if ! ensure_secret "CONTABASE_SETUP_TOKEN" "setup-token" ".env.docker" "$env_is_new"; then
  [ "$INTERACTIVE" = false ] && exit 1
fi
if ! ensure_secret "AUTH_ENCRYPTION_KEY" "auth-key" ".env.docker" "$env_is_new"; then
  [ "$INTERACTIVE" = false ] && exit 1
fi
if ! ensure_secret "SECURITY_MASTER_KEY" "master-key" ".env.docker" "$env_is_new"; then
  [ "$INTERACTIVE" = false ] && exit 1
fi

set_env_var_safe "CONTABASE_TIMEZONE" "$CONTABASE_TIMEZONE_VALUE" ".env.docker"
secure_env_file ".env.docker" || exit 1

section "[4/5] Configurando volume de persistencia local..."

if [ -L "data" ]; then
  say "Erro: data/ nao pode ser symlink."
  exit 1
fi

if [ ! -d "data" ]; then
  mkdir -p data
  say "OK: pasta data/ criada."
fi

say "Ajustando permissoes para o usuario non-root do container (UID 1000)..."

if ! secure_data_permissions; then
  say "Erro: nao foi possivel restringir data/ para 0700/0600."
  say "A aplicacao nao conseguira operar com seguranca."
  say "Corrija o ownership de data/ para 1000:1000 e execute novamente."
  exit 1
fi
say "OK: data/ restrito a 0700, arquivos 0600 e ownership 1000:1000."

if [ "$INTERACTIVE" = true ]; then
  section "Selecione o perfil de instalacao:"
  say "1) Local Docker nesta maquina (http://localhost)"
  say "2) Rede local/LAN por IP privado"
  say "3) Domínio com HTTPS via proxy/tunnel/CDN"
  say "4) Avançado / manual"
  read -r -p "Opcao (1/2/3/4): " INSTALL_TYPE
  blank

  case "$INSTALL_TYPE" in
    1)
      say "Modo Local Docker selecionado."
      say "Este perfil e apenas para acesso do host ao container por localhost."
      set_env_var_for_profile "APP_BASE_URL" "http://localhost:8080"
      set_env_var_for_profile "ALLOWED_HOSTS" "localhost,127.0.0.1,::1"
      set_env_var_for_profile "TRUSTED_PROXIES" ""
      set_env_var_for_profile "CONTABASE_ACCESS_MODE" "local-docker"
      ACCESS_URL="http://localhost:8080"
      say "OK: modo local-docker configurado para Docker local somente nesta maquina."
      ;;
    2)
      say "Modo LAN privada selecionado."
      say "Informe o IP privado RFC1918 do servidor, por exemplo 192.168.1.50."
      read -r -p "IP privado LAN: " LAN_HOST_RAW
      LAN_HOST="$(normalize_domain "$LAN_HOST_RAW")"
      if ! is_private_ipv4 "$LAN_HOST"; then
        say "Erro: modo LAN exige IP privado RFC1918. Nao use localhost, dominio ou IP publico."
        exit 1
      fi
      set_env_var_for_profile "APP_BASE_URL" "http://$LAN_HOST:8080"
      set_env_var_for_profile "ALLOWED_HOSTS" "$LAN_HOST"
      set_env_var_for_profile "TRUSTED_PROXIES" ""
      set_env_var_for_profile "CONTABASE_ACCESS_MODE" "lan"
      ACCESS_URL="http://$LAN_HOST:8080"
      say "OK: modo LAN configurado. IP publico HTTP e dominio HTTP continuam bloqueados pelo app."
      ;;
    3)
      say "Modo Domínio com HTTPS via proxy/tunnel/CDN selecionado."
      say "Este perfil cobre Nginx Proxy Manager, Caddy, Traefik, Nginx,"
      say "Cloudflare Tunnel, Cloudflare Proxy/CDN, BunnyCDN/outro CDN,"
      say "e proxies em servidor local ou VPS."
      say "O instalador nao configura provedores externos automaticamente."
      read -r -p "Dominio HTTPS (ex: app.seudominio.com): " PUBLIC_DOMAIN_RAW
      PUBLIC_DOMAIN="$(normalize_domain "$PUBLIC_DOMAIN_RAW")"

      if [ -n "$PUBLIC_DOMAIN" ]; then
        set_env_var_for_profile "APP_BASE_URL" "https://$PUBLIC_DOMAIN"
        set_env_var_for_profile "ALLOWED_HOSTS" "$PUBLIC_DOMAIN"
        set_env_var_for_profile "CONTABASE_ACCESS_MODE" "proxy"
        ACCESS_URL="https://$PUBLIC_DOMAIN"
        say "OK: APP_BASE_URL, ALLOWED_HOSTS e CONTABASE_ACCESS_MODE definidos em .env.docker (valores existentes preservados)."
      else
        say "Aviso: dominio vazio. Configure APP_BASE_URL, ALLOWED_HOSTS e CONTABASE_ACCESS_MODE manualmente antes de expor."
      fi

      ensure_override_for_profile
      blank
      say "Configure o proxy/tunnel/origin local para enviar estes headers ao ContaBase:"
      say "- Host"
      say "- X-Forwarded-Host"
      say "- X-Forwarded-Proto"
      say "- X-Real-IP"
      say "- X-Forwarded-For"
      blank
      say "TRUSTED_PROXIES deve ser o IP/CIDR que conecta diretamente no ContaBase."
      say "Geralmente e a rede/IP do proxy local, tunnel ou container que encaminha ao app."
      say "Nao e necessariamente o IP publico do usuario."
      say "Nao e necessariamente todos os IPs da Cloudflare/Bunny se houver proxy local entre eles e o ContaBase."
      say "Sugestoes comuns, confirme de acordo com a sua topologia real:"
      say "- 172.16.0.0/12 para redes Docker privadas comuns"
      say "- 172.17.0.0/16 para a bridge Docker padrao"
      say "- IP/CIDR especifico do proxy/tunnel/container, se voce souber"
      read -r -p "TRUSTED_PROXIES (pode deixar vazio para configurar manualmente): " TRUSTED_PROXIES_VALUE
      if [ -n "$TRUSTED_PROXIES_VALUE" ]; then
        set_env_var_for_profile "TRUSTED_PROXIES" "$TRUSTED_PROXIES_VALUE"
        say "OK: TRUSTED_PROXIES definido em .env.docker (valor existente preservado)."
      else
        say "Aviso forte: TRUSTED_PROXIES vazio/incorreto pode fazer o app bloquear acesso remoto por seguranca."
      fi
      ;;
    4)
      say "Modo Avancado / manual selecionado."
      ensure_override_for_profile
      say "Revise .env.docker e docker-compose.override.yml antes de expor a aplicacao."
      ;;
    *)
      say "Opcao invalida. Prosseguindo com defaults locais."
      set_env_var_for_profile "APP_BASE_URL" "http://localhost:8080"
      set_env_var_for_profile "ALLOWED_HOSTS" "localhost,127.0.0.1,::1"
      set_env_var_for_profile "TRUSTED_PROXIES" ""
      set_env_var_for_profile "CONTABASE_ACCESS_MODE" "local-docker"
      ACCESS_URL="http://localhost:8080"
      ;;
  esac
else
  set_env_var_for_profile "APP_BASE_URL" "http://localhost:8080"
  set_env_var_for_profile "ALLOWED_HOSTS" "localhost,127.0.0.1,::1"
  set_env_var_for_profile "TRUSTED_PROXIES" ""
  set_env_var_for_profile "CONTABASE_ACCESS_MODE" "local-docker"
fi

section "[5/5] Subindo aplicacao via Docker..."

if [ -f ".env.docker" ]; then
  say "Executando docker compose config para validar arquivos..."
  docker compose config >/dev/null
  say "OK: configuracoes do Compose validadas."
else
  say "Aviso: docker compose config pulado porque .env.docker nao existe."
fi

install_update_command() {
  local update_wrapper="/usr/local/bin/contabase-update"
  local mode_file="/etc/contabase/install-mode"
  local repo_path installed_version installed_channel wrapper_source
  repo_path="$(pwd)"
  installed_version="${CONTABASE_VERSION:-$(read_public_version_file)}"
  installed_channel="$(infer_install_channel "$installed_version")"
  wrapper_source="${repo_path}/scripts/lib/contabase-update-wrapper.sh"

  if [ ! -f "$wrapper_source" ] || [ -L "$wrapper_source" ]; then
    say "Erro: dispatcher autenticado ausente ou inseguro: $wrapper_source"
    say "Nenhum contabase-update foi instalado ou substituido."
    return 1
  fi

  as_root mkdir -p "$(dirname "$STATE_FILE")"
  write_install_state "docker" "$installed_channel" "$installed_version" "$repo_path" \
    "$PROVENANCE_KIND" "$PROVENANCE_REF" "$PROVENANCE_COMMIT" "$PROVENANCE_ORIGIN"
  as_root tee "$mode_file" >/dev/null <<MODE_EOF
docker
${repo_path}
MODE_EOF
  as_root chmod 0644 "$mode_file" 2>/dev/null || true

  as_root install -m 0755 "$wrapper_source" "$update_wrapper" || {
    say "Erro: nao foi possivel instalar o dispatcher autenticado."
    return 1
  }
  if [ ! -e /usr/local/bin/cb-update ]; then
    as_root ln -s contabase-update /usr/local/bin/cb-update 2>/dev/null || true
  fi
  say ""
  say "Comando de atualizacao instalado:"
  say "  contabase-update"
  say "  cb-update"
  say "  sudo contabase-update --help"
  return 0
}

report_docker_startup() {
  local container_id runtime_state

  say "Instalacao Docker executada."
  if ! container_id="$(docker compose ps --all -q contabase)" || [ -z "$container_id" ] \
    || ! runtime_state="$(docker inspect --format '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id")"; then
    say "AVISO: nao foi possivel confirmar o estado da aplicacao."
    say "Confira: docker compose ps --all e docker compose logs --tail=80 contabase"
    return 1
  fi

  case "$runtime_state" in
    'running healthy')
      say "APLICACAO SAUDAVEL: healthcheck Docker healthy."
      ;;
    'running starting')
      say "APLICACAO INICIALIZANDO: healthcheck Docker starting. Aguarde antes de acessar."
      say "Acompanhe: docker compose ps --all"
      ;;
    'running none')
      say "AVISO: container em execucao sem healthcheck; saude da aplicacao nao confirmada."
      say "Verifique o endpoint /health antes de acessar."
      ;;
    *)
      say "FALHA DE INICIALIZACAO: a aplicacao nao esta saudavel (estado Docker: ${runtime_state})."
      say "Confira: docker compose ps --all e docker compose logs --tail=80 contabase"
      return 1
      ;;
  esac
}

if ! docker compose up -d --build; then
  say "FALHA DE INICIALIZACAO: docker compose up nao foi concluido."
  exit 1
fi
secure_runtime_data_files || {
  say "Erro: nao foi possivel restringir os arquivos de dados criados pelo container."
  exit 1
}
blank
say "Containers ativos:"
docker compose ps

blank
say "Ultimos logs:"
docker compose logs --tail=80 contabase || true

blank
say "======================================================================"
docker_startup_status=0
report_docker_startup || docker_startup_status=$?
say "======================================================================"
blank
if [ "$docker_startup_status" -eq 0 ]; then
  say "Quando a aplicacao estiver saudavel, acesse e continue:"
  say "1. Acesse: $ACCESS_URL"
  say "2. O sistema pedira o TOKEN LOCAL DE SETUP."
  say "3. Copie o valor de CONTABASE_SETUP_TOKEN diretamente do arquivo .env.docker local."
  blank
  say "IMPORTANTE POS-SETUP:"
  say "Apos criar sua primeira conta de administrador, remova ou comente CONTABASE_SETUP_TOKEN no .env.docker."
  say "Depois reinicie sem o token: docker compose up -d --build"
  say "O alerta no painel administrativo deve sumir apos o container reiniciar sem esse token."
  blank
fi
say "Atualizacoes futuras:"
say "  contabase-update"
say "  ./scripts/update-contabase-docker.sh"
blank

install_update_command || say "Aviso: comando contabase-update nao foi instalado; a instalacao principal continua."
exit "$docker_startup_status"

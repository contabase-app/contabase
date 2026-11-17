#!/usr/bin/env bash
# Contrato do adaptador Docker por digest. Funcoes puras, sem executar Docker.

CONTABASE_IMAGE_REPOSITORY="ghcr.io/contabase-app/contabase"
CONTABASE_IMAGE_COMPOSE_FILE="docker-compose.image.yml"
CONTABASE_IMAGE_METHOD="docker-image"

docker_image_fail() {
  printf 'Erro: %s\n' "$*" >&2
  return 1
}

docker_image_digest_valid() {
  case "${1:-}" in
    sha256:*) [[ "${1#sha256:}" =~ ^[0-9a-f]{64}$ ]] ;;
    *) return 1 ;;
  esac
}

docker_image_private_ipv4() {
  local ip="${1:-}" a b c d part
  IFS=. read -r a b c d <<EOF
$ip
EOF
  for part in "$a" "$b" "$c" "$d"; do
    case "$part" in ""|*[!0-9]*) return 1 ;; esac
    [ "$part" -ge 0 ] 2>/dev/null && [ "$part" -le 255 ] 2>/dev/null || return 1
  done
  [ "$a" -eq 10 ] && return 0
  [ "$a" -eq 172 ] && [ "$b" -ge 16 ] && [ "$b" -le 31 ] && return 0
  [ "$a" -eq 192 ] && [ "$b" -eq 168 ] && return 0
  return 1
}

docker_image_host() {
  local value="${1:-}"
  value="${value#*://}"
  value="${value%%/*}"
  value="${value%%:*}"
  printf '%s' "$value"
}

docker_image_path_has_symlink() {
  local path="${1:-}" current="" part
  [ -n "$path" ] && [ "${path#/}" != "$path" ] || return 0
  local -a parts
  IFS=/ read -ra parts <<< "${path#/}"
  for part in "${parts[@]}"; do
    [ -n "$part" ] || continue
    current="${current}/${part}"
    [ -L "$current" ] && return 0
  done
  return 1
}

docker_image_path_overlaps() {
  local first="${1%/}" second="${2%/}"
  [ -n "$first" ] && [ -n "$second" ] || return 1
  [ "$first" = "$second" ] && return 0
  case "${first}/" in "${second}/"*) return 0 ;; esac
  case "${second}/" in "${first}/"*) return 0 ;; esac
  return 1
}

validate_image_contract() {
  local version="${CONTABASE_VERSION:-}"
  local channel="${CONTABASE_CHANNEL:-}"
  local commit="${CONTABASE_COMMIT:-}"
  local index="${CONTABASE_IMAGE_INDEX_DIGEST:-}"
  local platform="${CONTABASE_IMAGE_PLATFORM:-}"
  local platform_digest="${CONTABASE_IMAGE_PLATFORM_DIGEST:-}"
  local reference="${CONTABASE_IMAGE_REFERENCE:-}"
  local timezone="${CONTABASE_TIMEZONE:-America/Sao_Paulo}"

  [ "${CONTABASE_INSTALL_METHOD:-}" = "docker" ] || docker_image_fail "modo imagem exige CONTABASE_INSTALL_METHOD=docker" || return 1
  [ "${CONTABASE_INSTALL_MODE:-}" = "image" ] || docker_image_fail "modo imagem nao declarado" || return 1
  [ "${CONTABASE_BOOTSTRAP_CONTRACT_VERSION:-}" = "1" ] || docker_image_fail "origem Bootstrap ausente" || return 1
  [ "${CONTABASE_IMAGE_PROOF_SOURCE:-}" = "bootstrap-catalog" ] || docker_image_fail "digest avulso nao e prova de homologacao" || return 1
  [ "${CONTABASE_CONFIRM_INSTALL:-}" = "INSTALAR" ] || docker_image_fail "confirmacao explicita ausente" || return 1
  [ "${CONTABASE_FEATURE_INVESTMENTS:-}" = "false" ] || docker_image_fail "nova instalacao exige CONTABASE_FEATURE_INVESTMENTS=false" || return 1
  [ "${CONTABASE_INTERNAL_PORT:-}" = "8080" ] || docker_image_fail "porta interna deve ser fixa em 8080" || return 1

  [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.(0|[1-9][0-9]*))?$ ]] \
    || docker_image_fail "versao publica invalida" || return 1
  case "$channel" in beta|stable) ;; *) docker_image_fail "canal invalido" || return 1 ;; esac
  case "$version" in
    *-beta.*) [ "$channel" = "beta" ] || docker_image_fail "versao beta em canal divergente" || return 1 ;;
    *) [ "$channel" = "stable" ] || docker_image_fail "versao stable em canal divergente" || return 1 ;;
  esac
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || docker_image_fail "commit publico invalido" || return 1

  docker_image_digest_valid "$index" || docker_image_fail "digest do indice OCI invalido" || return 1
  docker_image_digest_valid "$platform_digest" || docker_image_fail "digest da plataforma OCI invalido" || return 1
  case "$platform" in linux/amd64|linux/arm64) ;; *) docker_image_fail "plataforma OCI invalida" || return 1 ;; esac
  [ "$reference" = "${CONTABASE_IMAGE_REPOSITORY}@${index}" ] \
    || docker_image_fail "referencia imutavel divergente do digest validado" || return 1
  [[ "$timezone" =~ ^(UTC|[A-Za-z_]+/[A-Za-z0-9_+\-]+)$ ]] \
    || docker_image_fail "CONTABASE_TIMEZONE invalido" || return 1
}

validate_network_config() {
  local base="${APP_BASE_URL:-}"
  local allowed="${ALLOWED_HOSTS:-}"
  local trusted="${TRUSTED_PROXIES:-}"
  local mode="${CONTABASE_ACCESS_MODE:-}"
  local port="${CONTABASE_PORT:-}"
  local bind="${CONTABASE_HOST_BIND:-}"
  local host item

  [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1 ] && [ "$port" -le 65535 ] \
    || docker_image_fail "porta do host invalida" || return 1
  [[ "$base" =~ ^https?://[^[:space:]]+$ ]] \
    || docker_image_fail "APP_BASE_URL invalida" || return 1
  case "$base" in *@*|*\?*|*#*) docker_image_fail "APP_BASE_URL com credenciais/query/fragmento" || return 1 ;; esac
  case "$allowed" in ""|*\**|*$'\n'*) docker_image_fail "ALLOWED_HOSTS invalido" || return 1 ;; esac
  [ "${#allowed}" -le 1024 ] && [ "${#trusted}" -le 1024 ] \
    || docker_image_fail "ALLOWED_HOSTS/TRUSTED_PROXIES longo demais" || return 1
  case "$trusted" in *0.0.0.0/0*|*::/0*) docker_image_fail "TRUSTED_PROXIES universal nao permitido" || return 1 ;; esac

  host="$(docker_image_host "$base")"
  [ -n "$host" ] || docker_image_fail "APP_BASE_URL sem host" || return 1
  case ",$allowed," in *",$host,"*) ;; *) docker_image_fail "ALLOWED_HOSTS nao contem o host da URL" || return 1 ;; esac

  case "$mode" in
    local-docker)
      case "$base" in http://localhost:*|http://127.0.0.1:*) ;; *) docker_image_fail "local-docker exige URL loopback HTTP" || return 1 ;; esac
      [ "$bind" = "127.0.0.1" ] || docker_image_fail "local-docker exige bind loopback" || return 1
      [ -z "$trusted" ] || docker_image_fail "local-docker nao aceita TRUSTED_PROXIES" || return 1
      local -a items
      IFS=, read -ra items <<< "$allowed"
      for item in "${items[@]}"; do
        case "$item" in localhost|127.0.0.1|::1) ;; *) docker_image_fail "local-docker aceita apenas hosts loopback" || return 1 ;; esac
      done
      ;;
    lan)
      case "$base" in http://*) ;; *) docker_image_fail "LAN exige HTTP privado" || return 1 ;; esac
      docker_image_private_ipv4 "$host" || docker_image_fail "LAN exige IPv4 privado RFC1918" || return 1
      [ "$bind" = "$host" ] || docker_image_fail "LAN exige bind no IP privado da URL" || return 1
      [ -z "$trusted" ] || docker_image_fail "LAN nao aceita TRUSTED_PROXIES" || return 1
      ;;
    proxy)
      case "$base" in https://*) ;; *) docker_image_fail "proxy exige HTTPS" || return 1 ;; esac
      [ "$bind" = "127.0.0.1" ] || docker_image_fail "proxy exige bind loopback para o proxy" || return 1
      [ -n "$trusted" ] || docker_image_fail "proxy exige TRUSTED_PROXIES explicito" || return 1
      case "$host" in localhost|127.0.0.1|::1) docker_image_fail "proxy exige dominio publico"; return 1 ;; esac
      if docker_image_private_ipv4 "$host"; then
        docker_image_fail "proxy exige dominio publico"
        return 1
      fi
      ;;
    *) docker_image_fail "CONTABASE_ACCESS_MODE invalido" || return 1 ;;
  esac
}

validate_install_paths() {
  local install="${CONTABASE_INSTALL_DIR:-}"
  local data="${CONTABASE_DATA_DIR:-}"
  local config="${CONTABASE_CONFIG_DIR:-}"
  local parent part

  install="${install%/}"
  data="${data%/}"
  config="${config%/}"
  [ -n "$install" ] && [ "${install#/}" != "$install" ] \
    || docker_image_fail "CONTABASE_INSTALL_DIR invalido" || return 1
  [ -n "$data" ] && [ "${data#/}" != "$data" ] \
    || docker_image_fail "CONTABASE_DATA_DIR invalido" || return 1
  [ -n "$config" ] && [ "${config#/}" != "$config" ] \
    || docker_image_fail "CONTABASE_CONFIG_DIR invalido" || return 1
  case "$install$data$config" in *$'\n'*|*$'\r'*) docker_image_fail "diretorio com caractere de controle" || return 1 ;; esac
  for part in "${install#/}" "${data#/}" "${config#/}"; do
    case "/$part/" in
      *"/../"*|*"/./"*) docker_image_fail "diretorio com componente . ou .." || return 1 ;;
    esac
  done
  [ "$data" = "$install/data" ] || docker_image_fail "Docker desta fase exige DATA_DIR=INSTALL_DIR/data" || return 1
  [ "$config" = "$install" ] || docker_image_fail "Docker desta fase exige CONFIG_DIR=INSTALL_DIR" || return 1
  case "$install" in
    /|/usr|/usr/bin|/usr/lib|/usr/local|/etc|/var|/var/lib|/opt|/home|/root|/run)
      docker_image_fail "diretorio generico do sistema nao permitido" || return 1 ;;
  esac
  [ "${#install}" -le 200 ] || docker_image_fail "caminho de instalacao longo demais" || return 1
  if docker_image_path_has_symlink "$install"; then
    docker_image_fail "caminho de instalacao contem symlink"
    return 1
  fi
  parent="$(dirname "$install")"
  [ -d "$parent" ] && [ ! -L "$parent" ] \
    || docker_image_fail "diretorio pai ausente ou symlink" || return 1
  [ -w "$parent" ] || docker_image_fail "sem permissao no diretorio pai" || return 1
  [ ! -e "$install" ] && [ ! -L "$install" ] \
    || docker_image_fail "instalacao/diretorio existente: use atualizacao explicita" || return 1
  [ ! -e "$data" ] && [ ! -L "$data" ] \
    || docker_image_fail "diretorio de dados existente" || return 1
}

validate_no_existing_state() {
  local state_file="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
  local mode_file="${CONTABASE_MODE_FILE:-/etc/contabase/install-mode}"
  local path state

  for path in "$state_file" "$mode_file" /etc/contabase/contabase.env \
              /usr/local/bin/contabase-update /usr/local/bin/cb-update; do
    [ ! -e "$path" ] && [ ! -L "$path" ] \
      || docker_image_fail "instalacao anterior registrada em $path" || return 1
  done
  for path in /etc/systemd/system/contabase.service /etc/systemd/system/contabase.service.d \
              /lib/systemd/system/contabase.service /usr/lib/systemd/system/contabase.service; do
    [ ! -e "$path" ] && [ ! -L "$path" ] \
      || docker_image_fail "servico ContaBase existente: use atualizacao explicita" || return 1
  done
  if command -v systemctl >/dev/null 2>&1; then
    state="$(systemctl show contabase.service --property=LoadState --value 2>/dev/null)" \
      || docker_image_fail "estado systemd ambiguo; operacao bloqueada" || return 1
    [ "$state" = "not-found" ] \
      || docker_image_fail "servico systemd existente ou ambiguo" || return 1
  fi
}

validate_docker_endpoint() {
  local host="${DOCKER_HOST:-}"
  local context="${DOCKER_CONTEXT:-}"
  local socket dir resolved

  [ -z "$context" ] \
    || docker_image_fail "DOCKER_CONTEXT nao suportado nesta fase; use o contexto padrao do sistema" || return 1
  [ -n "$host" ] || return 0
  case "$host" in
    unix://*) ;;
    *) docker_image_fail "DOCKER_HOST remoto/TCP/SSH nao permitido" || return 1 ;;
  esac
  socket="${host#unix://}"
  case "$socket" in
    /run/user/*/docker.sock) docker_image_fail "Docker rootless ainda nao homologado" || return 1 ;;
  esac
  [ -n "$socket" ] && [ "${socket#/}" != "$socket" ] \
    || docker_image_fail "socket Docker invalido" || return 1
  dir="$(cd "$(dirname "$socket")" 2>/dev/null && pwd -P)" \
    || docker_image_fail "diretorio do socket Docker inacessivel" || return 1
  resolved="${dir}/$(basename "$socket")"
  case "$resolved" in
    /run/docker.sock|/var/run/docker.sock) ;;
    *) docker_image_fail "socket Docker fora da allowlist" || return 1 ;;
  esac
  [ -S "$resolved" ] || docker_image_fail "socket Docker ausente" || return 1
}

assert_not_rootless() {
  case "${1:-}" in
    *rootless*) docker_image_fail "Docker rootless ainda nao homologado nesta fase" || return 1 ;;
  esac
}

yaml_quote() {
  local value="${1:-}"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '"%s"' "$value"
}

write_image_compose() {
  local output="$1" image="$2" bind="$3" port="$4" base_url="$5"
  local allowed="$6" trusted="$7" access="$8" timezone="$9" tmp
  tmp="$(mktemp "${output}.XXXXXX")" || return 1
  {
    printf 'services:\n'
    printf '  contabase:\n'
    printf '    image: %s\n' "$(yaml_quote "$image")"
    printf '    container_name: contabase_app\n'
    printf '    user: "1000:1000"\n'
    printf '    ports:\n'
    printf '      - %s\n' "$(yaml_quote "$bind:$port:8080")"
    printf '    env_file:\n'
    printf '      - .env.docker\n'
    printf '    environment:\n'
    printf '      PORT: "8080"\n'
    printf '      APP_BASE_URL: %s\n' "$(yaml_quote "$base_url")"
    printf '      ALLOWED_HOSTS: %s\n' "$(yaml_quote "$allowed")"
    printf '      TRUSTED_PROXIES: %s\n' "$(yaml_quote "$trusted")"
    printf '      CONTABASE_ACCESS_MODE: %s\n' "$(yaml_quote "$access")"
    printf '      CONTABASE_FEATURE_INVESTMENTS: "false"\n'
    printf '      DATA_DIR: "/app/data"\n'
    printf '      DB_FILE: "/app/data/contabase.db"\n'
    printf '      UPLOADS_DIR: "/app/data/uploads"\n'
    printf '      CONTABASE_TIMEZONE: %s\n' "$(yaml_quote "$timezone")"
    printf '    volumes:\n'
    printf '      - ./data:/app/data\n'
    printf '    restart: unless-stopped\n'
    printf '    healthcheck:\n'
    printf '      test: ["CMD", "wget", "--no-verbose", "--tries=1", "--spider", "http://localhost:8080/health"]\n'
    printf '      interval: 30s\n'
    printf '      timeout: 5s\n'
    printf '      retries: 3\n'
    printf '      start_period: 10s\n'
  } > "$tmp" || { rm -f "$tmp"; return 1; }
  chmod 0644 "$tmp" || { rm -f "$tmp"; return 1; }
  mv "$tmp" "$output" || { rm -f "$tmp"; return 1; }
}

assert_rendered_compose() {
  local rendered="$1" image="$2" bind="$3" port="$4" base_url="$5"
  local allowed="$6" trusted="$7" access="$8"

  printf '%s\n' "$rendered" | grep -Fq "image: $image" \
    || docker_image_fail "Compose renderizado sem a imagem fixada" || return 1
  if printf '%s\n' "$rendered" | grep -Fq "build:"; then
    docker_image_fail "Compose renderizado contem build local" || return 1
  fi
  printf '%s\n' "$rendered" | grep -Fq "host_ip: $bind" \
    || docker_image_fail "Compose renderizado com bind divergente" || return 1
  printf '%s\n' "$rendered" | grep -Fq "target: 8080" \
    || docker_image_fail "Compose renderizado sem porta interna 8080" || return 1
  printf '%s\n' "$rendered" | grep -Fq "published: \"$port\"" \
    || docker_image_fail "Compose renderizado com porta externa divergente" || return 1
  printf '%s\n' "$rendered" | grep -Fq "target: /app/data" \
    || docker_image_fail "Compose renderizado sem volume /app/data" || return 1
  if printf '%s\n' "$rendered" | grep -Fq "target: /app/uploads"; then
    docker_image_fail "Compose renderizado com mount redundante de uploads" || return 1
  fi
  if printf '%s\n' "$rendered" | grep -Fq "target: /app/backups"; then
    docker_image_fail "Compose renderizado com mount redundante de backups" || return 1
  fi
  printf '%s\n' "$rendered" | grep -Fq 'CONTABASE_FEATURE_INVESTMENTS: "false"' \
    || docker_image_fail "Compose renderizado sem investimentos desativados" || return 1
  printf '%s\n' "$rendered" | grep -Fq "user: 1000:1000" \
    || docker_image_fail "Compose renderizado sem UID/GID 1000" || return 1
  printf '%s\n' "$rendered" | grep -Fq "restart: unless-stopped" \
    || docker_image_fail "Compose renderizado sem politica de restart" || return 1
  printf '%s\n' "$rendered" | grep -Fq "APP_BASE_URL: $base_url" \
    || docker_image_fail "Compose renderizado sem APP_BASE_URL" || return 1
  printf '%s\n' "$rendered" | grep -Fq "ALLOWED_HOSTS: $allowed" \
    || docker_image_fail "Compose renderizado sem ALLOWED_HOSTS" || return 1
  printf '%s\n' "$rendered" | grep -Fq "CONTABASE_ACCESS_MODE: $access" \
    || docker_image_fail "Compose renderizado sem CONTABASE_ACCESS_MODE" || return 1
  if [ -n "$trusted" ]; then
    printf '%s\n' "$rendered" | grep -Fq "TRUSTED_PROXIES: $trusted" \
      || docker_image_fail "Compose renderizado sem TRUSTED_PROXIES" || return 1
  else
    printf '%s\n' "$rendered" | grep -Fq 'TRUSTED_PROXIES: ""' \
      || docker_image_fail "Compose renderizado com TRUSTED_PROXIES inesperado" || return 1
  fi
  printf '%s\n' "$rendered" | grep -Fq "http://localhost:8080/health" \
    || docker_image_fail "Compose renderizado sem healthcheck" || return 1
}

assert_repo_digest() {
  local observed="${1:-}" reference="${2:-}"
  case "$observed" in
    *"\"$reference\""*) return 0 ;;
  esac
  docker_image_fail "imagem executavel nao corresponde ao digest homologado"
}

assert_local_storage() {
  local path="${1:-}" fstype=""
  if command -v findmnt >/dev/null 2>&1; then
    fstype="$(findmnt -no FSTYPE -T "$path" 2>/dev/null || true)"
  elif command -v stat >/dev/null 2>&1; then
    fstype="$(stat -f -c %T "$path" 2>/dev/null || true)"
  fi
  if [ -n "$fstype" ]; then
    case "$fstype" in
      nfs|nfs4|cifs|smb3|9p|tmpfs|ramfs|vfat|exfat|fuse.*)
        docker_image_fail "filesystem de dados nao suportado para SQLite/WAL: $fstype" || return 1 ;;
      ext2|ext3|ext4|xfs|btrfs|zfs|f2fs|overlay|apfs|hfs|hfsplus) ;;
      *) printf 'Aviso: filesystem de dados nao reconhecido (%s); confirme armazenamento local.\n' "$fstype" >&2 ;;
    esac
  else
    printf 'Aviso: nao foi possivel confirmar o filesystem de dados; confirme armazenamento local.\n' >&2
  fi
}

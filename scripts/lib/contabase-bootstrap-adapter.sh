#!/usr/bin/env bash
# Contrato comum dos adaptadores Binario e Source do Bootstrap Beta 8.
# Funcoes de validacao fail-closed. Nao executam instalacao, rede ou systemd.

CONTABASE_BOOTSTRAP_CONTRACT=1
CONTABASE_BOOTSTRAP_SAFE_PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
CONTABASE_BOOTSTRAP_SERVICE_FILE="${CONTABASE_SERVICE_FILE:-/etc/systemd/system/contabase.service}"
CONTABASE_BOOTSTRAP_STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
CONTABASE_BOOTSTRAP_MODE_FILE="${CONTABASE_MODE_FILE:-/etc/contabase/install-mode}"
CONTABASE_BOOTSTRAP_ENV_FILE="${CONTABASE_ENV_FILE:-/etc/contabase/contabase.env}"
CONTABASE_BOOTSTRAP_UPDATE_BIN="${CONTABASE_UPDATE_BIN:-/usr/local/bin/contabase-update}"
CONTABASE_BOOTSTRAP_CB_UPDATE_BIN="${CONTABASE_CB_UPDATE_BIN:-/usr/local/bin/cb-update}"

bootstrap_fail() {
  printf 'Erro: %s\n' "$*" >&2
  return 1
}

bootstrap_digest_valid() {
  case "${1:-}" in
    *[!0-9a-f]*|"") return 1 ;;
  esac
  [ "${#1}" -eq 64 ]
}

bootstrap_private_ipv4() {
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

bootstrap_loopback_host() {
  case "${1:-}" in
    localhost|127.0.0.1|::1) return 0 ;;
  esac
  return 1
}

bootstrap_host_from_url() {
  local value="${1:-}"
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

bootstrap_url_scheme() {
  case "${1:-}" in
    http://*) printf 'http' ;;
    https://*) printf 'https' ;;
    *) printf '' ;;
  esac
}

bootstrap_url_port() {
  local value="${1:-}" authority port
  authority="${value#*://}"
  authority="${authority%%/*}"
  case "$authority" in
    \[*\]:*) port="${authority##*:}" ;;
    \[*\]) port="" ;;
    *:*) port="${authority##*:}" ;;
    *) port="" ;;
  esac
  case "$port" in
    ""|*[!0-9]*) printf '' ;;
    *) printf '%s' "$port" ;;
  esac
}

bootstrap_path_has_symlink() {
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

bootstrap_path_overlaps() {
  local first="${1%/}" second="${2%/}"
  [ -n "$first" ] && [ -n "$second" ] || return 1
  [ "$first" = "$second" ] && return 0
  case "${first}/" in "${second}/"*) return 0 ;; esac
  case "${second}/" in "${first}/"*) return 0 ;; esac
  return 1
}

bootstrap_validate_common_contract() {
  local adapter="${1:-}" version channel commit helper lib
  version="${CONTABASE_VERSION:-}"
  channel="${CONTABASE_CHANNEL:-}"
  commit="${CONTABASE_COMMIT:-}"
  helper="${CONTABASE_HELPER_SHA256:-}"
  lib="${CONTABASE_ADAPTER_LIB_SHA256:-}"

  [ "${CONTABASE_BOOTSTRAP_CONTRACT_VERSION:-}" = "1" ] \
    || { bootstrap_fail "contrato Bootstrap ausente"; return 1; }
  [ "${CONTABASE_INSTALL_MODE:-}" = "initial" ] \
    || { bootstrap_fail "modo de instalacao inicial nao declarado"; return 1; }
  [ "${CONTABASE_CONFIRM_INSTALL:-}" = "INSTALAR" ] \
    || { bootstrap_fail "confirmacao explicita ausente"; return 1; }
  [ "${CONTABASE_BOOTSTRAP_ADAPTER:-}" = "$adapter" ] \
    || { bootstrap_fail "adaptador divergente"; return 1; }
  [ "${CONTABASE_FEATURE_INVESTMENTS:-}" = "false" ] \
    || { bootstrap_fail "nova instalacao exige CONTABASE_FEATURE_INVESTMENTS=false"; return 1; }
  [ "${CONTABASE_INSTALL_DEPS:-0}" = "0" ] \
    || { bootstrap_fail "instalacao automatica de dependencias nao autorizada"; return 1; }

  [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.(0|[1-9][0-9]*))?$ ]] \
    || { bootstrap_fail "versao publica invalida"; return 1; }
  case "$channel" in
    beta) case "$version" in *-beta.*) ;; *) bootstrap_fail "versao fora do canal beta"; return 1 ;; esac ;;
    stable) case "$version" in *-beta.*) bootstrap_fail "versao beta em canal stable"; return 1 ;; esac ;;
    *) bootstrap_fail "canal invalido"; return 1 ;;
  esac
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] \
    || { bootstrap_fail "commit publico invalido"; return 1; }
  bootstrap_digest_valid "$helper" \
    || { bootstrap_fail "hash do helper ausente/invalido"; return 1; }
  bootstrap_digest_valid "$lib" \
    || { bootstrap_fail "hash da biblioteca do adaptador ausente/invalido"; return 1; }
}

bootstrap_validate_release_contract() {
  bootstrap_validate_common_contract release || return 1
  bootstrap_digest_valid "${CONTABASE_ARTIFACT_SHA256:-}" \
    || { bootstrap_fail "SHA-256 do artifact ausente/invalido"; return 1; }
  bootstrap_digest_valid "${CONTABASE_CHECKSUMS_SHA256:-}" \
    || { bootstrap_fail "SHA-256 de checksums ausente/invalido"; return 1; }
  bootstrap_digest_valid "${CONTABASE_UPDATE_WRAPPER_SHA256:-}" \
    || { bootstrap_fail "SHA-256 do wrapper de update ausente/invalido"; return 1; }
  [ "${CONTABASE_ARTIFACT_NAME:-}" = "contabase-linux-${CONTABASE_ARTIFACT_PLATFORM#linux/}.tar.gz" ] \
    || { bootstrap_fail "artifact diverge da arquitetura"; return 1; }
  case "${CONTABASE_ARTIFACT_PLATFORM:-}" in
    linux/amd64|linux/arm64) ;;
    *) bootstrap_fail "plataforma do artifact invalida"; return 1 ;;
  esac
  [ -n "${CONTABASE_ARTIFACT_PATH:-}" ] && [ -n "${CONTABASE_CHECKSUMS_PATH:-}" ] \
    || { bootstrap_fail "payload preparado ausente"; return 1; }
}

bootstrap_validate_source_contract() {
  bootstrap_validate_common_contract source || return 1
  bootstrap_digest_valid "${CONTABASE_SOURCE_ARCHIVE_SHA256:-}" \
    || { bootstrap_fail "SHA-256 do archive source ausente/invalido"; return 1; }
  [ "${CONTABASE_SOURCE_ARCHIVE_KIND:-}" = "github-tarball" ] \
    || { bootstrap_fail "origem do archive source invalida"; return 1; }
  [ -n "${CONTABASE_SOURCE_ARCHIVE:-}" ] \
    || { bootstrap_fail "archive source preparado ausente"; return 1; }
}

bootstrap_validate_network() {
  local method="${1:-}" profile base allowed trusted mode port internal bind host item
  local -a items
  profile="${CONTABASE_NETWORK_PROFILE:-}"
  base="${APP_BASE_URL:-}"
  allowed="${ALLOWED_HOSTS:-}"
  trusted="${TRUSTED_PROXIES:-}"
  mode="${CONTABASE_ACCESS_MODE:-}"
  port="${CONTABASE_PORT:-}"
  internal="${CONTABASE_INTERNAL_PORT:-}"
  bind="${CONTABASE_HOST_BIND:-}"

  [[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1 ] && [ "$port" -le 65535 ] \
    || { bootstrap_fail "porta do host invalida"; return 1; }
  [ "$internal" = "$port" ] \
    || { bootstrap_fail "porta interna diverge da porta do servico systemd"; return 1; }
  case "$profile" in local|lan|proxy|tunnel|advanced) ;; *) bootstrap_fail "perfil de rede invalido"; return 1 ;; esac
  case "$base" in *[[:space:]]*|*@*|*\?*|*#*) bootstrap_fail "APP_BASE_URL invalida"; return 1 ;; esac
  case "$base" in *[!A-Za-z0-9:/._~\[\]-]*) bootstrap_fail "APP_BASE_URL com caractere invalido"; return 1 ;; esac
  [[ "$base" =~ ^https?://[^[:space:]]+$ ]] \
    || { bootstrap_fail "APP_BASE_URL invalida"; return 1; }
  case "$allowed" in ""|*\**|*$'\n'*) bootstrap_fail "ALLOWED_HOSTS invalido"; return 1 ;; esac
  [ "${#allowed}" -le 1024 ] && [ "${#trusted}" -le 1024 ] \
    || { bootstrap_fail "ALLOWED_HOSTS/TRUSTED_PROXIES longo demais"; return 1; }
  case "$trusted" in *0.0.0.0/0*|*::/0*) bootstrap_fail "TRUSTED_PROXIES universal nao permitido"; return 1 ;; esac
  host="$(bootstrap_host_from_url "$base")"
  [ -n "$host" ] || { bootstrap_fail "APP_BASE_URL sem host"; return 1; }
  case ",$allowed," in
    *",$host,"*) ;;
    *) bootstrap_fail "ALLOWED_HOSTS nao contem o host da URL"; return 1 ;;
  esac
  case "$allowed" in *",,"*|*", "*|*" ,"*) bootstrap_fail "ALLOWED_HOSTS com item vazio"; return 1 ;; esac
  IFS=, read -ra items <<< "$allowed"
  for item in "${items[@]}"; do
    [ -n "$item" ] || { bootstrap_fail "ALLOWED_HOSTS com item vazio"; return 1; }
    case "$item" in *[!A-Za-z0-9.:_-]*) bootstrap_fail "ALLOWED_HOSTS com host invalido"; return 1 ;; esac
  done
  [ "${#items[@]}" -le 16 ] || { bootstrap_fail "ALLOWED_HOSTS excessivo"; return 1; }

  case "$mode" in
    local)
      case "$base" in http://localhost:*|http://127.0.0.1:*|http://\[::1\]:*) ;; *) bootstrap_fail "local exige URL loopback HTTP"; return 1 ;; esac
      [ "$bind" = "127.0.0.1" ] || { bootstrap_fail "local exige bind loopback"; return 1; }
      [ -z "$trusted" ] || { bootstrap_fail "local nao aceita TRUSTED_PROXIES"; return 1; }
      [ "$(bootstrap_url_port "$base")" = "$port" ] || { bootstrap_fail "porta da URL local diverge da porta do host"; return 1; }
      for item in "${items[@]}"; do
        bootstrap_loopback_host "$item" || { bootstrap_fail "local aceita apenas hosts loopback"; return 1; }
      done
      ;;
    lan)
      case "$base" in http://*) ;; *) bootstrap_fail "LAN exige HTTP privado"; return 1 ;; esac
      bootstrap_private_ipv4 "$host" || { bootstrap_fail "LAN exige IPv4 privado RFC1918"; return 1; }
      [ "$bind" = "$host" ] || { bootstrap_fail "LAN exige bind no IP privado da URL"; return 1; }
      [ -z "$trusted" ] || { bootstrap_fail "LAN nao aceita TRUSTED_PROXIES"; return 1; }
      [ "$(bootstrap_url_port "$base")" = "$port" ] || { bootstrap_fail "porta da URL LAN diverge da porta do host"; return 1; }
      ;;
    proxy)
      case "$base" in https://*) ;; *) bootstrap_fail "proxy exige HTTPS"; return 1 ;; esac
      [ "$bind" = "127.0.0.1" ] || { bootstrap_fail "proxy exige bind loopback"; return 1; }
      [ -n "$trusted" ] || { bootstrap_fail "proxy exige TRUSTED_PROXIES explicito"; return 1; }
      bootstrap_loopback_host "$host" && { bootstrap_fail "proxy exige dominio publico"; return 1; }
      bootstrap_private_ipv4 "$host" && { bootstrap_fail "proxy exige dominio publico"; return 1; }
      case "$host" in *.*) ;; *) bootstrap_fail "dominio do proxy invalido"; return 1 ;; esac
      case "$host" in *[!A-Za-z0-9.-]*|.*|*.) bootstrap_fail "dominio do proxy invalido"; return 1 ;; esac
      ;;
    *) bootstrap_fail "CONTABASE_ACCESS_MODE invalido"; return 1 ;;
  esac

  if [ -n "$trusted" ]; then
    IFS=, read -ra items <<< "$trusted"
    for item in "${items[@]}"; do
      case "$item" in
        */*)
          local network="${item%/*}" prefix="${item#*/}"
          [[ "$prefix" =~ ^[0-9]+$ ]] || { bootstrap_fail "TRUSTED_PROXIES invalido"; return 1; }
          if bootstrap_private_ipv4 "$network" || [ "$network" = "127.0.0.1" ]; then
            [ "$prefix" -ge 24 ] || { bootstrap_fail "TRUSTED_PROXIES deve ser /24 ou mais especifico"; return 1; }
          elif [ "$item" = "::1/128" ]; then
            :
          else
            bootstrap_fail "TRUSTED_PROXIES deve ser loopback ou rede privada restrita"
            return 1
          fi
          ;;
        ::1) ;;
        *) bootstrap_fail "TRUSTED_PROXIES invalido"; return 1 ;;
      esac
    done
    [ "${#items[@]}" -le 16 ] || { bootstrap_fail "TRUSTED_PROXIES excessivo"; return 1; }
  fi

  [ -n "$method" ] || { bootstrap_fail "metodo do adaptador ausente"; return 1; }
  case "$method" in release|source) ;; *) bootstrap_fail "metodo do adaptador invalido"; return 1 ;; esac
}

bootstrap_validate_paths() {
  local method="${1:-}" install data config part path
  install="${CONTABASE_INSTALL_DIR:-}"
  data="${CONTABASE_DATA_DIR:-}"
  config="${CONTABASE_CONFIG_DIR:-}"

  for path in "$install" "$data" "$config"; do
    [ -n "$path" ] && [ "${path#/}" != "$path" ] \
      || { bootstrap_fail "diretorio deve ser absoluto"; return 1; }
    case "$path" in *$'\n'*|*$'\r'*|*' '*) bootstrap_fail "diretorio com caractere invalido"; return 1 ;; esac
    case "/${path#/}/" in *"/../"*|*"/./"*) bootstrap_fail "diretorio com componente . ou .."; return 1 ;; esac
    [ "${#path}" -le 200 ] || { bootstrap_fail "diretorio longo demais"; return 1; }
    case "${path%/}" in
      /|/usr|/usr/bin|/usr/lib|/usr/local|/etc|/var|/var/lib|/opt|/home|/root|/run|/tmp)
        bootstrap_fail "diretorio generico do sistema nao permitido"; return 1 ;;
    esac
    if bootstrap_path_has_symlink "$path"; then
      bootstrap_fail "diretorio contem symlink"; return 1
    fi
  done
  [ "$install" != "$data" ] && [ "$install" != "$config" ] && [ "$data" != "$config" ] \
    || { bootstrap_fail "diretorios de instalacao, dados e configuracao devem ser distintos"; return 1; }
  if bootstrap_path_overlaps "$install" "$data"; then
    bootstrap_fail "instalacao e dados nao podem se sobrepor"
    return 1
  fi
  if bootstrap_path_overlaps "$install" "$config"; then
    bootstrap_fail "instalacao e configuracao nao podem se sobrepor"
    return 1
  fi
  if bootstrap_path_overlaps "$data" "$config"; then
    bootstrap_fail "dados e configuracao nao podem se sobrepor"
    return 1
  fi
  case "$method" in release|source) ;; *) bootstrap_fail "metodo do adaptador invalido"; return 1 ;; esac
}

bootstrap_validate_no_existing_state() {
  local install="${CONTABASE_INSTALL_DIR:-}" data="${CONTABASE_DATA_DIR:-}" config="${CONTABASE_CONFIG_DIR:-}"
  local path default
  for path in "$install" "$data" "$config"; do
    [ -n "$path" ] || continue
    [ ! -e "$path" ] && [ ! -L "$path" ] \
      || { bootstrap_fail "instalacao/diretorio existente: use atualizacao explicita"; return 1; }
  done
  if [ "${CONTABASE_TEST_SKIP_DEFAULT_PATHS:-0}" != "1" ]; then
    for path in /opt/contabase /var/lib/contabase /etc/contabase; do
      [ ! -e "$path" ] && [ ! -L "$path" ] \
        || { bootstrap_fail "instalacao/diretorio existente: use atualizacao explicita"; return 1; }
    done
  fi
  for path in "$CONTABASE_BOOTSTRAP_STATE_FILE" "$CONTABASE_BOOTSTRAP_MODE_FILE" \
              "$CONTABASE_BOOTSTRAP_ENV_FILE" "$CONTABASE_BOOTSTRAP_UPDATE_BIN" \
              "$CONTABASE_BOOTSTRAP_CB_UPDATE_BIN"; do
    [ ! -e "$path" ] && [ ! -L "$path" ] \
      || { bootstrap_fail "instalacao anterior registrada em $path"; return 1; }
  done
  for path in "$CONTABASE_BOOTSTRAP_SERVICE_FILE" \
              /etc/systemd/system/contabase.service.d \
              /lib/systemd/system/contabase.service \
              /usr/lib/systemd/system/contabase.service; do
    [ ! -e "$path" ] && [ ! -L "$path" ] \
      || { bootstrap_fail "servico ContaBase existente: use atualizacao explicita"; return 1; }
  done
  if [ "${CONTABASE_TEST_SKIP_SYSTEMD:-0}" != "1" ] && command -v systemctl >/dev/null 2>&1; then
    default="$(systemctl show contabase.service --property=LoadState --value 2>/dev/null)" \
      || { bootstrap_fail "estado systemd ambiguo; operacao bloqueada"; return 1; }
    [ "$default" = "not-found" ] \
      || { bootstrap_fail "servico systemd existente ou ambiguo"; return 1; }
  fi
}

bootstrap_create_dir_exclusive() {
  local path="${1:-}" owner="${2:-root}" group="${3:-root}" mode="${4:-0755}" parent
  [ -n "$path" ] && [ "${path#/}" != "$path" ] || { bootstrap_fail "diretorio invalido"; return 1; }
  parent="$(dirname "$path")"
  [ -d "$parent" ] && [ ! -L "$parent" ] \
    || { bootstrap_fail "diretorio pai ausente ou symlink: $parent"; return 1; }
  [ ! -e "$path" ] && [ ! -L "$path" ] \
    || { bootstrap_fail "diretorio existente: $path"; return 1; }
  mkdir "$path" || { bootstrap_fail "nao foi possivel criar $path"; return 1; }
  chown "$owner:$group" "$path" 2>/dev/null || { bootstrap_fail "nao foi possivel ajustar dono de $path"; return 1; }
  chmod "$mode" "$path" || { bootstrap_fail "nao foi possivel ajustar permissao de $path"; return 1; }
}

bootstrap_unit_network_lines() {
  local mode="${CONTABASE_ACCESS_MODE:-}"
  case "$mode" in
    local) printf 'IPAddressDeny=any\nIPAddressAllow=localhost\n' ;;
    lan) printf 'IPAddressDeny=any\nIPAddressAllow=localhost 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16\n' ;;
    proxy) printf 'IPAddressDeny=any\nIPAddressAllow=localhost\n' ;;
    *) return 1 ;;
  esac
}

bootstrap_verify_file_digest() {
  local path="${1:-}" expected="${2:-}" label="${3:-arquivo}" observed
  [ -f "$path" ] && [ ! -L "$path" ] \
    || { bootstrap_fail "$label ausente ou symlink"; return 1; }
  bootstrap_digest_valid "$expected" \
    || { bootstrap_fail "$label sem digest valido"; return 1; }
  if command -v sha256sum >/dev/null 2>&1; then
    observed="$(sha256sum "$path" | awk '{print $1}')"
  else
    observed="$(shasum -a 256 "$path" | awk '{print $1}')"
  fi
  [ "$observed" = "$expected" ] \
    || { bootstrap_fail "$label divergente do snapshot homologado"; return 1; }
}

bootstrap_verify_checkout_identity() {
  local checkout="${1:-}" version="${2:-}" commit="${3:-}" observed
  [ -d "$checkout" ] && [ ! -L "$checkout" ] \
    || { bootstrap_fail "checkout ausente ou symlink"; return 1; }
  [ -f "$checkout/VERSION" ] && [ ! -L "$checkout/VERSION" ] \
    || { bootstrap_fail "VERSION do checkout ausente"; return 1; }
  [ "$(tr -d '[:space:]' < "$checkout/VERSION")" = "$version" ] \
    || { bootstrap_fail "VERSION do checkout diverge da versao fixada"; return 1; }
  if git -C "$checkout" symbolic-ref -q HEAD >/dev/null 2>&1; then
    bootstrap_fail "checkout deve estar com HEAD destacado, nunca em branch movel"
    return 1
  fi
  observed="$(git -C "$checkout" rev-parse HEAD 2>/dev/null)" \
    || { bootstrap_fail "nao foi possivel ler HEAD do checkout"; return 1; }
  [ "$observed" = "$commit" ] \
    || { bootstrap_fail "HEAD do checkout diverge do commit homologado"; return 1; }
  [ -z "$(git -C "$checkout" status --porcelain 2>/dev/null)" ] \
    || { bootstrap_fail "checkout possui alteracoes locais"; return 1; }
}

bootstrap_verify_running_binary() {
  local install_dir="${1:-}" expected_version="${2:-}" service="${3:-contabase.service}"
  local main_pid exe installed exe_digest installed_digest
  [ "${CONTABASE_TEST_SKIP_SYSTEMD:-0}" != "1" ] || return 0
  command -v systemctl >/dev/null 2>&1 \
    || { bootstrap_fail "systemctl ausente para confirmar runtime"; return 1; }
  main_pid="$(systemctl show "$service" --property=MainPID --value 2>/dev/null)" \
    || { bootstrap_fail "nao foi possivel confirmar o PID do servico"; return 1; }
  [[ "$main_pid" =~ ^[0-9]+$ ]] && [ "$main_pid" -gt 0 ] \
    || { bootstrap_fail "servico sem processo principal"; return 1; }
  [ -r "/proc/$main_pid/exe" ] \
    || { bootstrap_fail "nao foi possivel ler o executavel em execucao"; return 1; }
  exe="$(readlink -f "/proc/$main_pid/exe" 2>/dev/null)" \
    || { bootstrap_fail "nao foi possivel resolver o executavel em execucao"; return 1; }
  installed="$(readlink -f "$install_dir/contabase" 2>/dev/null)" \
    || { bootstrap_fail "binario instalado ausente"; return 1; }
  [ "$exe" = "$installed" ] \
    || { bootstrap_fail "processo em execucao nao e o binario instalado"; return 1; }
  exe_digest="$(bootstrap_sha256 "/proc/$main_pid/exe")" \
    || { bootstrap_fail "nao foi possivel medir o executavel em execucao"; return 1; }
  installed_digest="$(bootstrap_sha256 "$installed")" \
    || { bootstrap_fail "nao foi possivel medir o binario instalado"; return 1; }
  [ "$exe_digest" = "$installed_digest" ] \
    || { bootstrap_fail "executavel em execucao diverge do binario instalado"; return 1; }
  [ "$(tr -d '[:space:]' < "$install_dir/VERSION")" = "$expected_version" ] \
    || { bootstrap_fail "versao do bundle instalado diverge"; return 1; }
}

bootstrap_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

bootstrap_sanitized_reexec() {
  local script="${1:-}"
  shift || true
  [ "${CONTABASE_BOOTSTRAP_SANITIZED:-0}" = "1" ] && return 0
  [ -n "$script" ] && [ -f "$script" ] || { bootstrap_fail "helper ausente para saneamento"; return 1; }

  local home status
  home="$(mktemp -d "${TMPDIR:-/tmp}/contabase-bootstrap-home.XXXXXX")" \
    || { bootstrap_fail "nao foi possivel criar HOME controlado"; return 1; }
  chmod 0700 "$home" || { rm -rf "$home"; bootstrap_fail "nao foi possivel proteger HOME controlado"; return 1; }

  (
    trap 'rm -rf "$home"' EXIT
    trap 'exit 2' HUP INT TERM
    env -i \
      PATH="$CONTABASE_BOOTSTRAP_SAFE_PATH" \
      HOME="$home" \
      TMPDIR="$home" \
      CURL_HOME="$home" \
      GIT_CONFIG_NOSYSTEM=1 \
      GIT_CONFIG_GLOBAL=/dev/null \
      GIT_TERMINAL_PROMPT=0 \
      LANG=C \
      LC_ALL=C \
      CONTABASE_BOOTSTRAP_SANITIZED=1 \
      CONTABASE_BOOTSTRAP_CONTRACT_VERSION="${CONTABASE_BOOTSTRAP_CONTRACT_VERSION:-}" \
      CONTABASE_INSTALL_MODE="${CONTABASE_INSTALL_MODE:-}" \
      CONTABASE_CONFIRM_INSTALL="${CONTABASE_CONFIRM_INSTALL:-}" \
      CONTABASE_BOOTSTRAP_ADAPTER="${CONTABASE_BOOTSTRAP_ADAPTER:-}" \
      CONTABASE_VERSION="${CONTABASE_VERSION:-}" \
      CONTABASE_CHANNEL="${CONTABASE_CHANNEL:-}" \
      CONTABASE_COMMIT="${CONTABASE_COMMIT:-}" \
      CONTABASE_INSTALL_METHOD="${CONTABASE_INSTALL_METHOD:-}" \
      CONTABASE_FEATURE_INVESTMENTS="${CONTABASE_FEATURE_INVESTMENTS:-}" \
      CONTABASE_INSTALL_DEPS="${CONTABASE_INSTALL_DEPS:-}" \
      CONTABASE_HELPER_SHA256="${CONTABASE_HELPER_SHA256:-}" \
      CONTABASE_ADAPTER_LIB_SHA256="${CONTABASE_ADAPTER_LIB_SHA256:-}" \
      CONTABASE_UPDATE_WRAPPER_SHA256="${CONTABASE_UPDATE_WRAPPER_SHA256:-}" \
      CONTABASE_ARTIFACT_SHA256="${CONTABASE_ARTIFACT_SHA256:-}" \
      CONTABASE_CHECKSUMS_SHA256="${CONTABASE_CHECKSUMS_SHA256:-}" \
      CONTABASE_ARTIFACT_NAME="${CONTABASE_ARTIFACT_NAME:-}" \
      CONTABASE_ARTIFACT_PLATFORM="${CONTABASE_ARTIFACT_PLATFORM:-}" \
      CONTABASE_ARTIFACT_PATH="${CONTABASE_ARTIFACT_PATH:-}" \
      CONTABASE_CHECKSUMS_PATH="${CONTABASE_CHECKSUMS_PATH:-}" \
      CONTABASE_SOURCE_ARCHIVE="${CONTABASE_SOURCE_ARCHIVE:-}" \
      CONTABASE_SOURCE_ARCHIVE_SHA256="${CONTABASE_SOURCE_ARCHIVE_SHA256:-}" \
      CONTABASE_SOURCE_ARCHIVE_KIND="${CONTABASE_SOURCE_ARCHIVE_KIND:-}" \
      CONTABASE_BOOTSTRAP_CHECKOUT="${CONTABASE_BOOTSTRAP_CHECKOUT:-}" \
      CONTABASE_INSTALL_DIR="${CONTABASE_INSTALL_DIR:-}" \
      CONTABASE_DATA_DIR="${CONTABASE_DATA_DIR:-}" \
      CONTABASE_CONFIG_DIR="${CONTABASE_CONFIG_DIR:-}" \
      CONTABASE_PORT="${CONTABASE_PORT:-}" \
      CONTABASE_HOST_BIND="${CONTABASE_HOST_BIND:-}" \
      CONTABASE_INTERNAL_PORT="${CONTABASE_INTERNAL_PORT:-}" \
      CONTABASE_NETWORK_PROFILE="${CONTABASE_NETWORK_PROFILE:-}" \
      APP_BASE_URL="${APP_BASE_URL:-}" \
      ALLOWED_HOSTS="${ALLOWED_HOSTS:-}" \
      TRUSTED_PROXIES="${TRUSTED_PROXIES:-}" \
      CONTABASE_ACCESS_MODE="${CONTABASE_ACCESS_MODE:-}" \
      CONTABASE_TIMEZONE="${CONTABASE_TIMEZONE:-}" \
      CONTABASE_TEST_SKIP_SYSTEMD="${CONTABASE_TEST_SKIP_SYSTEMD:-}" \
      CONTABASE_TEST_SKIP_DEFAULT_PATHS="${CONTABASE_TEST_SKIP_DEFAULT_PATHS:-}" \
      CONTABASE_TEST_MODE="${CONTABASE_TEST_MODE:-}" \
      CONTABASE_TEST_FIXTURE_DIR="${CONTABASE_TEST_FIXTURE_DIR:-}" \
      CONTABASE_TEST_UNAME_M="${CONTABASE_TEST_UNAME_M:-}" \
      CONTABASE_SERVICE_FILE="${CONTABASE_SERVICE_FILE:-}" \
      CONTABASE_STATE_FILE="${CONTABASE_STATE_FILE:-}" \
      CONTABASE_MODE_FILE="${CONTABASE_MODE_FILE:-}" \
      CONTABASE_ENV_FILE="${CONTABASE_ENV_FILE:-}" \
      CONTABASE_UPDATE_BIN="${CONTABASE_UPDATE_BIN:-}" \
      CONTABASE_CB_UPDATE_BIN="${CONTABASE_CB_UPDATE_BIN:-}" \
      bash "$script" "$@"
  )
  status=$?
  rm -rf "$home"
  return "$status"
}

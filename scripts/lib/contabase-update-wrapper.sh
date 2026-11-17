#!/usr/bin/env bash
# ContaBase official update dispatcher (F3-C).
#
# The dispatcher resolves the persisted installation channel, authenticates the
# authorized release identity, downloads the updater bundle into a private
# directory, verifies every byte before execution and only then delegates to
# the official updater for the installation method.
#
# This file is installed as /usr/local/bin/contabase-update by the updaters
# themselves, always from authenticated bundle bytes.
set -euo pipefail
umask 077
LC_ALL=C
export LC_ALL

# Root must never resolve commands from a user-controlled PATH.
if [ "${EUID:-$(id -u)}" -eq 0 ]; then
  PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
  export PATH
  LC_ALL=C
  export LC_ALL
fi

STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
LEGACY_MODE_FILE="${CONTABASE_MODE_FILE:-/etc/contabase/install-mode}"
ENV_FILE="${CONTABASE_ENV_FILE:-/etc/contabase/contabase.env}"
WRAPPER_PATH="${CONTABASE_UPDATE_WRAPPER:-/usr/local/bin/contabase-update}"
ALIAS_PATH="${CONTABASE_UPDATE_ALIAS:-/usr/local/bin/cb-update}"

CHANNEL_BASE="https://get-contabase.pages.dev"
RAW_BASE="https://raw.githubusercontent.com/contabase-app/contabase"
API_BASE="https://api.github.com/repos/contabase-app/contabase"
SAFE_PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
JSON_SEP=$'\034'

SAVED_METHOD=""
SAVED_CHANNEL=""
SAVED_VERSION=""
SAVED_REPO_PATH=""
UPDATER_CHANNEL=""
TARGET_VERSION=""
TARGET_CHANNEL=""
TARGET_COMMIT=""
TARGET_UPDATER_SHA=""
MANIFEST_VERSION=""
MANIFEST_COMMIT=""
MANIFEST_UPDATER_SHA=""
CATALOG_COMMIT=""
CATALOG_UPDATER_SHA=""
UPDATE_DRY_RUN=false
RESOLVE_ONLY="${CONTABASE_UPDATE_RESOLVE_ONLY:-0}"
ASSUME_YES="${CONTABASE_ASSUME_YES:-0}"
PASSTHROUGH_ARGS=()
STATE_DIR=""
WORK_DIR=""
META_DIR=""
BUNDLE_DIR=""
WRAPPER_SHA=""
LOCK_HELD=false
CHILD_PID=""
WRAPPER_MARKER=""
PENDING_UPDATE=false
INTERRUPTED_UPDATE=false
LOCK_FILE=""
LOCKED_STATE_DIR=""
CHILD_START=""

say() { printf '%s\n' "$*"; }

warn() { printf 'Aviso: %s\n' "$*" >&2; }

fail() {
  printf 'Erro: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Uso:
  sudo contabase-update
  sudo contabase-update vX.Y.Z[-beta.N]
  sudo contabase-update --channel beta
  sudo contabase-update --channel stable

Atalho: sudo cb-update

Sem argumentos, atualiza dentro do canal salvo:
  stable  -> ultima stable publicada
  beta    -> ultima beta publicada
  pinned  -> exige versao ou --channel explicito

Opcoes:
  --channel beta|stable  Seleciona o canal para instalacao pinned; troca de
                         canal persistido exige fluxo proprio.
  --dry-run              Encaminha dry-run quando suportado pelo metodo.
  --yes, -y              Modo nao interativo quando suportado.
  --help, -h             Mostra esta ajuda.

O dispatcher autentica canal, release, tag, commit, manifesto e bundle antes
de executar qualquer updater. Nao existe fallback para download sem hash.
EOF
}

is_public_semver() {
  local value="$1"
  [[ "$value" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$ ]]
}

validate_public_version() {
  local version="$1"
  [ -n "$version" ] || fail "versao vazia."
  case "$version" in
    latest|main|master|dev|develop|stable|beta|*-internal*)
      fail "versao invalida para update: ${version}. Use uma tag SemVer publica."
      ;;
  esac
  is_public_semver "$version" || fail "tag publica invalida: $version"
}

infer_channel_from_version() {
  local version="$1"
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
    say "Versao instalada nao encontrada ou vazia." >&2
    return 3
  fi
  if [ -z "$target" ]; then
    say "Versao alvo nao encontrada ou vazia." >&2
    return 3
  fi

  cc="$(canonicalize_version "$current")" || {
    say "Versao instalada invalida ou ambigua: ${current}" >&2
    return 3
  }
  tc="$(canonicalize_version "$target")" || {
    say "Versao alvo invalida ou ambigua: ${target}" >&2
    return 3
  }

  cmp="$(compare_canonical $cc $tc)" || {
    say "Erro interno ao comparar versoes: ${current} vs ${target}" >&2
    return 3
  }

  case "$cmp" in
    "-1") return 0 ;; # UPDATE
    "0")  return 2 ;; # ALREADY_CURRENT
    "1")  return 1 ;; # DOWNGRADE_REJECTED
    *)    return 3 ;;
  esac
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum < "$1" | awk '{print $1}'
  else
    shasum -a 256 < "$1" | awk '{print $1}'
  fi
}

file_mode() {
  stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"
}

file_uid() {
  stat -c %u "$1" 2>/dev/null || stat -f %u "$1"
}

file_nlink() {
  stat -c %h "$1" 2>/dev/null || stat -f %l "$1"
}

# Retorna 0 quando o caminho absoluto contem symlink ou componente ambiguo.
path_unsafe() {
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

process_start() {
  LC_ALL=C ps -p "$1" -o lstart= 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

process_state() {
  LC_ALL=C ps -p "$1" -o stat= 2>/dev/null | sed 's/^[[:space:]]*//;s/[[:space:]]*$//'
}

# PID + start time is stable across exec and distinguishes PID reuse.
process_matches() {
  local pid="$1" start="$2" observed
  [ -n "$pid" ] && [ "$pid" != "none" ] || return 1
  [ -n "$start" ] && [ "$start" != "none" ] || return 1
  observed="$(process_start "$pid")"
  [ -n "$observed" ] && [ "$observed" = "$start" ]
}

reject_dangerous_environment() {
  local name
  for name in \
    CONTABASE_CHANNEL_BASE CONTABASE_RAW_BASE CONTABASE_ALLOW_INSECURE_CHANNEL \
    CONTABASE_REPO CONTABASE_API_BASE CONTABASE_SITE CONTABASE_REGISTRY \
    GIT_DIR GIT_WORK_TREE GIT_CONFIG GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_NOSYSTEM \
    GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_SSH GIT_SSH_COMMAND \
    GIT_EXEC_PATH GIT_INDEX_FILE GIT_CEILING_DIRECTORIES GIT_DISCOVERY_ACROSS_FILESYSTEM \
    GIT_NAMESPACE GIT_COMMON_DIR GIT_QUARANTINE_PATH \
    LD_PRELOAD LD_LIBRARY_PATH LD_AUDIT DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH \
    BASH_ENV ENV CDPATH GLOBIGNORE \
    DOCKER_HOST DOCKER_CONTEXT COMPOSE_FILE COMPOSE_PROJECT_NAME \
    PYTHONPATH PYTHONHOME PYTHONSTARTUP PYTHONINSPECT
  do
    if [ -n "${!name+x}" ]; then
      fail "variavel de ambiente nao autorizada: ${name}"
    fi
  done
}

state_value() {
  local key="$1"
  local file="$2"
  [ -f "$file" ] || { printf ''; return 0; }
  awk -F= -v k="$key" '$1 == k { print substr($0, index($0, "=") + 1); exit }' "$file" | tr -d '\r' | sed -e 's/^"\(.*\)"$/\1/' -e "s/^'\(.*\)'$/\1/"
}

validate_state_file() {
  local file="$1" size state_dir mode owner current_uid
  [ -n "$file" ] && [ -f "$file" ] && [ ! -L "$file" ] \
    || fail "arquivo de estado ausente, irregular ou symlink: ${file:-<vazio>}"
  state_dir="$(dirname "$file")"
  [ -d "$state_dir" ] && [ ! -L "$state_dir" ] \
    || fail "diretorio do estado ausente ou symlink: $state_dir"
  current_uid="$(id -u)"
  owner="$(file_uid "$file")"
  if [ "$current_uid" -eq 0 ]; then
    [ "$owner" = "0" ] || fail "estado sem ownership root: $file"
  else
    [ "$owner" = "$current_uid" ] || fail "estado com ownership divergente: $file"
  fi
  mode="$(file_mode "$file")"
  case "$mode" in
    ""|*[!0-7]*) fail "modo invalido do estado: $file" ;;
  esac
  if (( (8#$mode & 0022) != 0 )); then
    fail "estado gravavel por grupo/outros: $file"
  fi
  owner="$(file_uid "$state_dir")"
  if [ "$current_uid" -eq 0 ]; then
    [ "$owner" = "0" ] || fail "diretorio de estado sem ownership root: $state_dir"
  else
    [ "$owner" = "$current_uid" ] || fail "diretorio de estado com ownership divergente: $state_dir"
  fi
  mode="$(file_mode "$state_dir")"
  case "$mode" in
    ""|*[!0-7]*) fail "modo invalido do diretorio de estado: $state_dir" ;;
  esac
  if (( (8#$mode & 0022) != 0 )); then
    fail "diretorio de estado gravavel por grupo/outros: $state_dir"
  fi
  size="$(wc -c < "$file" | tr -d '[:space:]')"
  case "$size" in
    ""|*[!0-9]*) fail "tamanho invalido do estado: $file" ;;
  esac
  [ "$size" -le 32768 ] || fail "arquivo de estado excede 32768 bytes: $file"
  LC_ALL=C awk -F= '
    /^[[:space:]]*$/ || /^[[:space:]]*#/ { next }
    {
      if (index($0, "=") == 0 || $1 !~ /^CONTABASE_[A-Z0-9_]+$/) exit 1
      if ($1 in seen) exit 1
      seen[$1] = 1
      if ($1 ~ /(SECRET|TOKEN|PASSWORD|KEY|CREDENTIAL)/) exit 1
      value = substr($0, index($0, "=") + 1)
      if (value ~ /[\r\n]/) exit 1
    }
  ' "$file" || fail "sintaxe, duplicata ou campo proibido no estado: $file"
  if LC_ALL=C grep -q 'CONTABASE_STATE_VERSION=' "$file"; then
    [ "$(state_value CONTABASE_STATE_VERSION "$file")" = "1" ] \
      || fail "CONTABASE_STATE_VERSION invalido em $file"
  fi
  [ -n "$(state_value CONTABASE_INSTALL_METHOD "$file")" ] \
    || fail "campo obrigatorio ausente em $file: CONTABASE_INSTALL_METHOD"
  [ -n "$(state_value CONTABASE_INSTALLED_VERSION "$file")" ] \
    || fail "campo obrigatorio ausente em $file: CONTABASE_INSTALLED_VERSION"
}

load_state() {
  local fragment config_env
  case "$STATE_FILE" in
    /*) ;;
    *) fail "CONTABASE_STATE_FILE deve ser um caminho absoluto: ${STATE_FILE:-<vazio>}" ;;
  esac
  case "$LEGACY_MODE_FILE" in
    /*) ;;
    *) fail "CONTABASE_MODE_FILE deve ser um caminho absoluto: ${LEGACY_MODE_FILE:-<vazio>}" ;;
  esac
  case "$ENV_FILE" in
    /*) ;;
    *) fail "CONTABASE_ENV_FILE deve ser um caminho absoluto: ${ENV_FILE:-<vazio>}" ;;
  esac
  SAVED_METHOD=""
  SAVED_CHANNEL=""
  SAVED_VERSION=""
  SAVED_REPO_PATH=""

  if [ ! -e "$STATE_FILE" ] && [ -z "${CONTABASE_STATE_FILE+x}" ] \
    && [ -z "${CONTABASE_MODE_FILE+x}" ] && command -v systemctl >/dev/null 2>&1; then
    fragment="$(systemctl show contabase.service --property=FragmentPath --value 2>/dev/null)" || fragment=""
    if [ -n "$fragment" ]; then
      [[ "$fragment" =~ ^/[A-Za-z0-9_./-]+$ ]] && [ -f "$fragment" ] && [ ! -L "$fragment" ] \
        || fail "unit insegura ao localizar o estado da instalacao."
      config_env="$(awk -F= '
        /^\[/ { section=$0 }
        section=="[Service]" && $1=="EnvironmentFile" { value=$2; count++ }
        END { if(count!=1) exit 1; print value }' "$fragment")" \
        || fail "EnvironmentFile ambiguo ao localizar estado."
      [[ "$config_env" =~ ^/[A-Za-z0-9_./-]+/contabase\.env$ ]] \
        || fail "EnvironmentFile fora do contrato Release."
      STATE_FILE="${config_env%/contabase.env}/install-state.env"
      LEGACY_MODE_FILE="${config_env%/contabase.env}/install-mode"
      ENV_FILE="$config_env"
    fi
  fi

  if [ -f "$STATE_FILE" ]; then
    validate_state_file "$STATE_FILE"
    SAVED_METHOD="$(state_value CONTABASE_INSTALL_METHOD "$STATE_FILE")"
    SAVED_CHANNEL="$(state_value CONTABASE_CHANNEL "$STATE_FILE")"
    SAVED_VERSION="$(state_value CONTABASE_INSTALLED_VERSION "$STATE_FILE")"
    SAVED_REPO_PATH="$(state_value CONTABASE_REPO_PATH "$STATE_FILE")"
  elif [ -f "$LEGACY_MODE_FILE" ]; then
    SAVED_METHOD="$(sed -n '1p' "$LEGACY_MODE_FILE" | awk '{print $1}' | tr -d '[:space:]')"
    SAVED_REPO_PATH="$(sed -n '2p' "$LEGACY_MODE_FILE" | tr -d '[:space:]')"
    SAVED_VERSION="$(state_value VERSION "$ENV_FILE")"
    SAVED_VERSION="${SAVED_VERSION:-$(state_value CONTABASE_INSTALLED_VERSION "$ENV_FILE")}"
    SAVED_CHANNEL="$(infer_channel_from_version "$SAVED_VERSION")"
  else
    fail "nao foi possivel detectar o modo de instalacao. Arquivo ausente: $STATE_FILE"
  fi

  [ -n "$SAVED_METHOD" ] || fail "nao foi possivel detectar o modo de instalacao."
  case "$SAVED_METHOD" in
    release|binary|docker|docker-image|source) ;;
    *) fail "modo de instalacao desconhecido: $SAVED_METHOD" ;;
  esac
  [ -n "$SAVED_VERSION" ] || fail "versao instalada ausente no estado."
  validate_public_version "$SAVED_VERSION"
  canonicalize_version "$SAVED_VERSION" >/dev/null 2>&1 \
    || fail "versao instalada nao suportada: $SAVED_VERSION"

  [ -n "$SAVED_CHANNEL" ] || SAVED_CHANNEL="$(infer_channel_from_version "$SAVED_VERSION")"
  case "$SAVED_CHANNEL" in
    stable|beta|pinned) ;;
    *) fail "canal salvo invalido: $SAVED_CHANNEL" ;;
  esac
  UPDATER_CHANNEL="$SAVED_CHANNEL"

  case "$SAVED_METHOD" in
    source|docker)
      [ -n "$SAVED_REPO_PATH" ] && [ -d "$SAVED_REPO_PATH" ] \
        || fail "repositorio da instalacao nao encontrado: ${SAVED_REPO_PATH:-<vazio>}"
      ;;
  esac
  STATE_DIR="$(dirname "$STATE_FILE")"
  WRAPPER_MARKER="$STATE_DIR/wrapper.pending"
}

reject_method_override() {
  [ -n "${CONTABASE_INSTALL_METHOD:-}" ] || return 0
  case "$SAVED_METHOD:$CONTABASE_INSTALL_METHOD" in
    release:release|release:binary|binary:release|binary:binary|\
    docker:docker|source:source|docker-image:docker-image)
      return 0
      ;;
  esac
  fail "metodo de ambiente diverge do estado: ${CONTABASE_INSTALL_METHOD} != ${SAVED_METHOD}"
}

JSON_AWK='
function jfail(msg) { printf "JSON invalido: %s\n", msg > "/dev/stderr"; exit 1 }
function jws(  c) {
  while (i <= n) { c = substr(s, i, 1); if (c == " " || c == "\t" || c == "\n" || c == "\r") { i++; continue } break }
}
function jexpect(t) { if (substr(s, i, length(t)) != t) jfail("token esperado: " t); i += length(t) }
function jchild(path, key) { return path == "" ? key : path SUBSEP key }
function jstring(  c, out, esc, hex) {
  if (substr(s, i, 1) != "\"") jfail("string esperada")
  i++; out = ""
  while (i <= n) {
    c = substr(s, i, 1)
    if (c == "\"") { i++; return out }
    if (c == "\\") {
      i++; esc = substr(s, i, 1)
      if (esc == "\"" || esc == "\\" || esc == "/") out = out esc
      else if (esc == "b" || esc == "f" || esc == "n" || esc == "r" || esc == "t") out = out "\\" esc
      else if (esc == "u") {
        hex = substr(s, i+1, 4)
        if (hex !~ /^[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]$/) jfail("escape unicode invalido")
        out = out "\\u" hex; i += 4
      } else jfail("escape invalido")
      i++; continue
    }
    if (c ~ /[[:cntrl:]]/) jfail("caractere de controle na string")
    out = out c; i++
  }
  jfail("string sem fechamento")
}
function jnumber(  start, c) {
  start = i; c = substr(s, i, 1)
  if (c == "-") { i++; c = substr(s, i, 1) }
  if (c == "0") { i++ }
  else if (c ~ /[1-9]/) { while (substr(s, i, 1) ~ /[0-9]/) i++ }
  else jfail("numero invalido")
  if (substr(s, i, 1) == ".") { i++; if (substr(s, i, 1) !~ /[0-9]/) jfail("fracao invalida"); while (substr(s, i, 1) ~ /[0-9]/) i++ }
  c = substr(s, i, 1)
  if (c == "e" || c == "E") { i++; c = substr(s, i, 1); if (c == "+" || c == "-") i++; if (substr(s, i, 1) !~ /[0-9]/) jfail("expoente invalido"); while (substr(s, i, 1) ~ /[0-9]/) i++ }
  return substr(s, start, i - start)
}
function jemit(path, type, value) { printf "%s\034%s\034%s\n", path, type, value }
function jvalue(path, depth,  c, v) {
  if (depth > 64) jfail("profundidade excessiva")
  jws(); c = substr(s, i, 1)
  if (c == "{") { jemit(path, "object", ""); jparse_object(path, depth + 1); return }
  if (c == "[") { jemit(path, "array", ""); jparse_array(path, depth + 1); return }
  if (c == "\"") { v = jstring(); jemit(path, "string", v); return }
  if (c == "t") { jexpect("true"); jemit(path, "bool", "true"); return }
  if (c == "f") { jexpect("false"); jemit(path, "bool", "false"); return }
  if (c == "n") { jexpect("null"); jemit(path, "null", "null"); return }
  if (c == "-" || c ~ /[0-9]/) { v = jnumber(); jemit(path, "number", v); return }
  jfail("valor invalido")
}
function jparse_object(path, depth,  key, child, c) {
  i++; jws(); if (substr(s, i, 1) == "}") { i++; return }
  while (1) {
    jws(); key = jstring()
    if (index(key, "\\")) jfail("escape em chave nao permitido")
    child = jchild(path, key)
    if (child in seen) jfail("chave duplicada: " key)
    seen[child] = 1
    jws(); jexpect(":")
    jvalue(child, depth + 1)
    jws(); c = substr(s, i, 1)
    if (c == ",") { i++; continue }
    if (c == "}") { i++; return }
    jfail("objeto malformado")
  }
}
function jparse_array(path, depth,  idx, child, c) {
  i++; jws(); if (substr(s, i, 1) == "]") { i++; return }
  idx = 0
  while (1) {
    child = jchild(path, idx)
    jvalue(child, depth + 1); idx++
    jws(); c = substr(s, i, 1)
    if (c == ",") { i++; continue }
    if (c == "]") { i++; return }
    jfail("array malformado")
  }
}
BEGIN {
  s = ""
  while ((getline line < file) > 0) s = s line "\n"
  close(file)
  n = length(s); i = 1
  jvalue("", 0); jws()
  if (i <= n) jfail("conteudo extra apos o documento")
  exit 0
}
'

json_parse() {
  local file="$1" out="$2" limit="$3" size stripped
  [ -n "$file" ] && [ -f "$file" ] && [ ! -L "$file" ] \
    || fail "documento JSON ausente ou inseguro: ${file:-<vazio>}"
  size="$(wc -c < "$file" | tr -d '[:space:]')"
  case "$size" in
    ""|*[!0-9]*) fail "tamanho invalido do documento JSON" ;;
  esac
  [ "$size" -le "$limit" ] || fail "documento JSON excede ${limit} bytes"
  stripped="$(LC_ALL=C tr -d '\000' < "$file" | wc -c | tr -d '[:space:]')"
  [ "$stripped" = "$size" ] || fail "documento JSON contem byte NUL"
  if ! LC_ALL=C awk -v file="$file" "$JSON_AWK" > "$out"; then
    fail "documento JSON invalido: $file"
  fi
  [ -s "$out" ] || fail "documento JSON vazio: $file"
}

json_kind() {
  local file="$1" key="$2"
  LC_ALL=C awk -F "$JSON_SEP" -v k="$key" '
    {
      path = $1
      for (i = 2; i <= NF - 2; i++) path = path FS $i
      if (path == k) { print $(NF - 1); exit }
    }' "$file"
}

json_value() {
  local file="$1" key="$2"
  LC_ALL=C awk -F "$JSON_SEP" -v k="$key" '
    {
      path = $1
      for (i = 2; i <= NF - 2; i++) path = path FS $i
      if (path == k) { print $NF; exit }
    }' "$file"
}

json_root_kind() {
  json_kind "$1" ""
}

download_url() {
  local url="$1" out="$2" limit="$3" want_json="${4:-0}" accept="${5:-}" result status content_type effective
  local curl_args=(--fail --silent --show-error --proto '=https' --tlsv1.2 \
    --max-redirs 0 --max-time 120 --max-filesize "$limit" \
    -A 'ContaBase-Update/1' -o "$out" -w '%{http_code}\n%{content_type}\n%{url_effective}')
  if [ -n "$accept" ]; then
    curl_args+=(-H "Accept: ${accept}")
  fi
  rm -f "$out"
  if ! result="$(curl "${curl_args[@]}" "$url")"; then
    rm -f "$out"
    fail "falha de rede ao baixar ${url}"
  fi
  status=""
  content_type=""
  effective=""
  {
    IFS= read -r status || true
    IFS= read -r content_type || true
    IFS= read -r effective || true
  } <<< "$result"
  [ "$status" = "200" ] || { rm -f "$out"; fail "resposta HTTP ${status:-desconhecida} em ${url}"; }
  [ "$effective" = "$url" ] || { rm -f "$out"; fail "redirecionamento nao autorizado em ${url}"; }
  [ -f "$out" ] && [ ! -L "$out" ] || { rm -f "$out"; fail "download ausente ou inseguro: ${url}"; }
  local size
  size="$(wc -c < "$out" | tr -d '[:space:]')"
  [ "$size" -le "$limit" ] || { rm -f "$out"; fail "download excede ${limit} bytes: ${url}"; }
  if [ "$want_json" = "1" ]; then
    case "$content_type" in
      application/json*) ;;
      *) rm -f "$out"; fail "content-type nao JSON em ${url}: ${content_type:-ausente}" ;;
    esac
  fi
}

validate_channel_manifest() {
  local json="$1" expected_channel="$2" vkind version ckind commit ukind updater_sha pkind published nested
  [ "$(json_root_kind "$json")" = "object" ] || fail "manifest do canal ${expected_channel} nao e objeto JSON"
  if LC_ALL=C awk -F "$JSON_SEP" 'NF == 3 && $1 != "" && ($2 == "object" || $2 == "array") { found = 1 } END { exit found ? 0 : 1 }' "$json"; then
    fail "manifest do canal ${expected_channel} contem campo estruturado nao suportado"
  fi
  [ "$(json_kind "$json" schema_version)" = "number" ] \
    && [ "$(json_value "$json" schema_version)" = "1" ] \
    || fail "manifest do canal ${expected_channel} tem schema_version invalido."
  [ "$(json_kind "$json" channel)" = "string" ] \
    && [ "$(json_value "$json" channel)" = "$expected_channel" ] \
    || fail "manifest do canal ${expected_channel} declara canal divergente."
  [ "$(json_kind "$json" release_type)" = "string" ] \
    && [ "$(json_value "$json" release_type)" = "$expected_channel" ] \
    || fail "manifest do canal ${expected_channel} tem release_type divergente."

  vkind="$(json_kind "$json" version)"
  case "$vkind" in
    null)
      fail "canal ${expected_channel} ainda nao possui versao publicada."
      ;;
    string)
      version="$(json_value "$json" version)"
      ;;
    *)
      fail "campo version ausente ou com tipo invalido no canal ${expected_channel}."
      ;;
  esac
  validate_public_version "$version"
  case "$expected_channel:$version" in
    beta:v*-beta.*) ;;
    stable:*-*) fail "manifest stable apontou para pre-release: $version" ;;
    stable:*) ;;
    *) fail "manifest beta apontou para versao nao beta: $version" ;;
  esac

  pkind="$(json_kind "$json" published_at)"
  [ "$pkind" = "string" ] \
    || fail "campo published_at ausente ou invalido no canal ${expected_channel}."
  published="$(json_value "$json" published_at)"
  [[ "$published" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] \
    || fail "published_at fora do formato RFC3339 UTC no canal ${expected_channel}."

  ckind="$(json_kind "$json" commit)"
  [ "$ckind" = "string" ] \
    || fail "campo commit ausente no manifesto autenticado do canal ${expected_channel}."
  commit="$(json_value "$json" commit)"
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] \
    || fail "commit invalido no manifesto autenticado do canal ${expected_channel}."

  ukind="$(json_kind "$json" updater_manifest_sha256)"
  [ "$ukind" = "string" ] \
    || fail "campo updater_manifest_sha256 ausente no manifesto autenticado do canal ${expected_channel}."
  updater_sha="$(json_value "$json" updater_manifest_sha256)"
  [[ "$updater_sha" =~ ^[0-9a-f]{64}$ ]] \
    || fail "updater_manifest_sha256 invalido no canal ${expected_channel}."

  MANIFEST_VERSION="$version"
  MANIFEST_COMMIT="$commit"
  MANIFEST_UPDATER_SHA="$updater_sha"
}

resolve_channel_manifest() {
  local channel="$1" file tsv
  case "$channel" in
    beta|stable) ;;
    *) fail "canal invalido: $channel" ;;
  esac
  [ -n "$META_DIR" ] || fail "diretorio de metadados ausente."
  file="$META_DIR/channel-${channel}.json"
  tsv="$META_DIR/channel-${channel}.tsv"
  download_url "${CHANNEL_BASE%/}/channels/${channel}.json" "$file" 65536 1
  json_parse "$file" "$tsv" 65536
  validate_channel_manifest "$tsv" "$channel"
}

host_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf '%s' "amd64" ;;
    aarch64|arm64) printf '%s' "arm64" ;;
    *) return 1 ;;
  esac
}

catalog_method_for_install() {
  case "$SAVED_METHOD" in
    source) printf '%s' "source" ;;
    docker) printf '%s' "docker" ;;
    release|binary) printf '%s' "release" ;;
    docker-image) printf '%s' "docker" ;;
    *) return 1 ;;
  esac
}

resolve_catalog_entry() {
  local version="$1" channel="$2" file tsv idx entry versions method arch
  file="$META_DIR/catalog.json"
  tsv="$META_DIR/catalog.tsv"
  download_url "${CHANNEL_BASE%/}/channels/releases-v1.json" "$file" 1048576 1
  json_parse "$file" "$tsv" 1048576
  [ "$(json_root_kind "$tsv")" = "object" ] || fail "catalogo de releases nao e objeto JSON"
  [ "$(json_kind "$tsv" schema_version)" = "number" ] \
    && [ "$(json_value "$tsv" schema_version)" = "1" ] \
    || fail "catalogo de releases com schema_version invalido."
  [ "$(json_kind "$tsv" releases)" = "array" ] || fail "catalogo de releases sem lista releases."

  versions="$META_DIR/catalog-versions.txt"
  LC_ALL=C awk -F "$JSON_SEP" 'NF == 5 && $1 == "releases" && $3 == "version" { print $5 }' "$tsv" > "$versions"
  [ -s "$versions" ] || fail "catalogo de releases vazio."
  if [ -n "$(LC_ALL=C sort "$versions" | LC_ALL=C uniq -d)" ]; then
    fail "catalogo de releases contem versao duplicada."
  fi

  idx="$(LC_ALL=C awk -F "$JSON_SEP" -v v="$version" '
    NF == 5 && $1 == "releases" && $3 == "version" && $5 == v { print $2; exit }
  ' "$tsv")"
  [[ "$idx" =~ ^[0-9]+$ ]] || fail "versao ${version} nao autorizada pelo catalogo do canal ${channel}."
  entry="releases${JSON_SEP}${idx}"

  [ "$(json_value "$tsv" "${entry}${JSON_SEP}version")" = "$version" ] \
    || fail "entrada do catalogo diverge da versao ${version}."
  [ "$(json_kind "$tsv" "${entry}${JSON_SEP}channel")" = "string" ] \
    && [ "$(json_value "$tsv" "${entry}${JSON_SEP}channel")" = "$channel" ] \
    || fail "entrada do catalogo diverge do canal ${channel}."
  case "$(json_value "$tsv" "${entry}${JSON_SEP}state")" in
    published|homologated) ;;
    *) fail "versao ${version} nao esta publicada/homologada no catalogo." ;;
  esac
  [ "$(json_kind "$tsv" "${entry}${JSON_SEP}commit")" = "string" ] \
    || fail "entrada do catalogo sem commit para ${version}."
  CATALOG_COMMIT="$(json_value "$tsv" "${entry}${JSON_SEP}commit")"
  [[ "$CATALOG_COMMIT" =~ ^[0-9a-f]{40}$ ]] \
    || fail "commit invalido no catalogo para ${version}."
  [ "$(json_kind "$tsv" "${entry}${JSON_SEP}updater_manifest_sha256")" = "string" ] \
    || fail "entrada do catalogo sem updater_manifest_sha256 para ${version}."
  CATALOG_UPDATER_SHA="$(json_value "$tsv" "${entry}${JSON_SEP}updater_manifest_sha256")"
  [[ "$CATALOG_UPDATER_SHA" =~ ^[0-9a-f]{64}$ ]] \
    || fail "updater_manifest_sha256 invalido no catalogo para ${version}."

  method="$(catalog_method_for_install)" \
    || fail "metodo de instalacao sem contrato de catalogo: ${SAVED_METHOD}."
  arch="$(host_arch)" \
    || fail "arquitetura do host nao homologada para o catalogo."
  [ "$(json_kind "$tsv" "${entry}${JSON_SEP}methods")" = "object" ] \
    || fail "catalogo sem methods para ${version}."
  [ "$(json_kind "$tsv" "${entry}${JSON_SEP}methods${JSON_SEP}${method}")" = "object" ] \
    || fail "metodo ${method} nao homologado no catalogo para ${version}."
  [ "$(json_kind "$tsv" "${entry}${JSON_SEP}methods${JSON_SEP}${method}${JSON_SEP}${arch}")" = "object" ] \
    || fail "arquitetura ${arch} nao homologada no catalogo para ${version}."
}

verify_github_identity() {
  local version="$1" channel="$2" expected_prerelease="$3" expected_commit="$4" release ref commit_sha
  local ref_type ref_sha current_type current_sha depth tag_file tag_tsv tag_seen
  release="$META_DIR/github-release.json"
  ref="$META_DIR/github-ref.json"
  download_url "${API_BASE}/releases/tags/${version}" "$release" 524288 1 "application/vnd.github+json"
  json_parse "$release" "$META_DIR/github-release.tsv" 524288
  [ "$(json_kind "$META_DIR/github-release.tsv" tag_name)" = "string" ] \
    && [ "$(json_value "$META_DIR/github-release.tsv" tag_name)" = "$version" ] \
    || fail "release oficial nao corresponde a tag ${version}."
  [ "$(json_kind "$META_DIR/github-release.tsv" draft)" = "bool" ] \
    && [ "$(json_value "$META_DIR/github-release.tsv" draft)" = "false" ] \
    || fail "release ${version} ainda esta em draft."
  [ "$(json_kind "$META_DIR/github-release.tsv" prerelease)" = "bool" ] \
    && [ "$(json_value "$META_DIR/github-release.tsv" prerelease)" = "$expected_prerelease" ] \
    || fail "release ${version} tem marcador prerelease divergente do canal ${channel}."
  [ "$(json_kind "$META_DIR/github-release.tsv" published_at)" = "string" ] \
    || fail "release ${version} nao esta publicada."

  download_url "${API_BASE}/git/ref/tags/${version}" "$ref" 524288 1 "application/vnd.github+json"
  json_parse "$ref" "$META_DIR/github-ref.tsv" 524288
  [ "$(json_value "$META_DIR/github-ref.tsv" ref)" = "refs/tags/${version}" ] \
    || fail "referencia oficial nao e a tag ${version}."
  ref_type="$(json_value "$META_DIR/github-ref.tsv" "object${JSON_SEP}type")"
  ref_sha="$(json_value "$META_DIR/github-ref.tsv" "object${JSON_SEP}sha")"
  [[ "$ref_sha" =~ ^[0-9a-f]{40}$ ]] \
    || fail "tag ${version} com object.sha ausente ou invalido."
  case "$ref_type" in
    commit)
      current_type="commit"
      current_sha="$ref_sha"
      ;;
    tag)
      current_type="tag"
      current_sha="$ref_sha"
      ;;
    *)
      fail "tag ${version} nao resolve para commit/tag oficial."
      ;;
  esac

  depth=0
  tag_seen=" "
  while [ "$current_type" = "tag" ]; do
    depth=$((depth + 1))
    [ "$depth" -le 8 ] || fail "cadeia de tags excessiva para ${version}."
    case "$tag_seen" in
      *" ${current_sha} "*) fail "ciclo de tags detectado para ${version}." ;;
    esac
    tag_seen="${tag_seen}${current_sha} "
    tag_file="$META_DIR/github-tag-${depth}.json"
    tag_tsv="$META_DIR/github-tag-${depth}.tsv"
    download_url "${API_BASE}/git/tags/${current_sha}" "$tag_file" 524288 1 "application/vnd.github+json"
    json_parse "$tag_file" "$tag_tsv" 524288
    if [ "$depth" -eq 1 ]; then
      [ "$(json_kind "$tag_tsv" tag)" = "string" ] \
        && [ "$(json_value "$tag_tsv" tag)" = "$version" ] \
        || fail "objeto tag nao corresponde a ${version}."
    fi
    current_type="$(json_value "$tag_tsv" "object${JSON_SEP}type")"
    current_sha="$(json_value "$tag_tsv" "object${JSON_SEP}sha")"
    [[ "$current_sha" =~ ^[0-9a-f]{40}$ ]] \
      || fail "objeto tag de ${version} com sha invalido."
    case "$current_type" in
      commit|tag) ;;
      *) fail "objeto tag de ${version} nao aponta para commit/tag." ;;
    esac
  done
  [ "$current_type" = "commit" ] && [ "$current_sha" = "$expected_commit" ] \
    || fail "tag ${version} nao resolve para o commit autorizado."

  download_url "${API_BASE}/commits/${version}" "$META_DIR/github-commit.txt" 256 0 "application/vnd.github.sha"
  commit_sha="$(tr -d '[:space:]' < "$META_DIR/github-commit.txt")"
  [[ "$commit_sha" =~ ^[0-9a-f]{40}$ ]] || fail "commit da tag ${version} invalido."
  [ "$commit_sha" = "$expected_commit" ] \
    || fail "commit da tag ${version} diverge do manifesto autorizado."
}

verify_manifest_entries() {
  local version="$1" commit="$2" expected_manifest="$3" method="$4"
  local manifest="$BUNDLE_DIR/updater.sha256"
  local allowed="VERSION scripts/update-contabase-source.sh scripts/update-contabase-docker.sh scripts/update-contabase-release.sh scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh scripts/lib/contabase-update-wrapper.sh scripts/lib/contabase-docker-image-update.py"
  local required_paths=() entries header actual path found hash entry_path
  local entries_file="$META_DIR/entries.tsv"
  local paths_file="$META_DIR/paths.txt"

  [ -f "$manifest" ] && [ ! -L "$manifest" ] || fail "updater.sha256 ausente ou inseguro."
  LC_ALL=C grep -q $'\r' "$manifest" 2>/dev/null && fail "CRLF nao permitido no updater.sha256."
  actual="$(sha256_file "$manifest")" || fail "falha ao calcular o SHA-256 do manifesto."
  [ "$actual" = "$expected_manifest" ] || fail "manifesto diverge da identidade autorizada."
  header="$(sed -n '1p' "$manifest")" || fail "falha ao ler o cabecalho do manifesto."
  [ "$header" = "commit=$commit" ] || fail "commit do manifesto diverge do autorizado."

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
  ' "$manifest")" || fail "formato, ordem, duplicata ou caminho invalido no manifesto."
  [ -n "$entries" ] || fail "manifesto vazio."
  printf '%s\n' "$entries" > "$entries_file"
  LC_ALL=C awk -F '\t' '{ print $2 }' "$entries_file" > "$paths_file"

  case "$method" in
    source)
      required_paths=(VERSION scripts/update-contabase-source.sh \
        scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
        scripts/lib/contabase-update-wrapper.sh)
      ;;
    docker)
      required_paths=(VERSION scripts/update-contabase-docker.sh \
        scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
        scripts/lib/contabase-update-wrapper.sh)
      ;;
    release)
      required_paths=(VERSION scripts/update-contabase-release.sh \
        scripts/lib/contabase-update-wrapper.sh)
      ;;
    docker-image)
      required_paths=(VERSION scripts/lib/contabase-docker-image-update.py \
        scripts/lib/contabase-update-wrapper.sh)
      ;;
    *)
      fail "metodo sem contrato de bundle: $method"
      ;;
  esac

  for path in "${required_paths[@]}"; do
    found=false
    while IFS=$'\t' read -r hash entry_path; do
      [ -n "$hash" ] || continue
      if [ "$entry_path" = "$path" ]; then
        found=true
        break
      fi
    done <<< "$entries"
    [ "$found" = true ] || fail "manifesto incompleto: $path"
  done

  WRAPPER_SHA=""
  while IFS=$'\t' read -r hash entry_path; do
    [ -n "$hash" ] || continue
    if [ "$entry_path" = "scripts/lib/contabase-update-wrapper.sh" ]; then
      WRAPPER_SHA="$hash"
      break
    fi
  done <<< "$entries"
  [ -n "$WRAPPER_SHA" ] || fail "wrapper verificado ausente do manifesto."
}

verify_bundle_files() {
  local version="$1" method="$2" file rel hash path actual mode owner nlink inventory
  local manifest="$BUNDLE_DIR/updater.sha256"
  local entries_file="$META_DIR/entries.tsv"
  local paths_file="$META_DIR/paths.txt"

  [ -d "$BUNDLE_DIR" ] && [ ! -L "$BUNDLE_DIR" ] || fail "bundle ausente ou inseguro."
  [ "$(file_mode "$BUNDLE_DIR")" = "700" ] || fail "bundle sem permissao privada 0700."
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    [ "$(file_uid "$BUNDLE_DIR")" = "0" ] || fail "bundle sem ownership root."
  fi
  [ -f "$manifest" ] && [ ! -L "$manifest" ] || fail "updater.sha256 ausente ou inseguro."
  [ "$(sha256_file "$manifest")" = "$TARGET_UPDATER_SHA" ] || fail "manifesto mudou antes do uso."

  [ "$(tr -d '[:space:]' < "$BUNDLE_DIR/VERSION" 2>/dev/null || true)" = "$version" ] \
    || fail "VERSION do bundle diverge da versao solicitada."

  inventory="$META_DIR/bundle-files.bin"
  find "$BUNDLE_DIR" -type l -print0 > "$META_DIR/bundle-links.bin" 2>/dev/null \
    || fail "falha ao enumerar symlinks do bundle."
  [ -s "$META_DIR/bundle-links.bin" ] && fail "symlink no bundle."
  find "$BUNDLE_DIR" ! -type f ! -type d -print0 > "$META_DIR/bundle-special.bin" 2>/dev/null \
    || fail "falha ao enumerar arquivos especiais do bundle."
  [ -s "$META_DIR/bundle-special.bin" ] && fail "arquivo especial no bundle."
  find "$BUNDLE_DIR" -type f -print0 > "$inventory" 2>/dev/null \
    || fail "falha ao enumerar arquivos do bundle."
  while IFS= read -r -d '' file; do
    rel="${file#"$BUNDLE_DIR"/}"
    if [ "$rel" = "updater.sha256" ]; then
      continue
    fi
    grep -Fqx -- "$rel" "$paths_file" || fail "arquivo extra no bundle: $rel"
    [ -f "$file" ] && [ ! -L "$file" ] || fail "arquivo irregular no bundle: $rel"
    nlink="$(file_nlink "$file")"
    [ "$nlink" = "1" ] || fail "hardlink nao permitido no bundle: $rel"
    mode="$(file_mode "$file")"
    case "$mode" in
      ""|*[!0-7]*) fail "modo invalido no bundle: $rel" ;;
    esac
    if (( (8#$mode & 0022) != 0 )); then
      fail "arquivo gravavel por grupo/outros no bundle: $rel"
    fi
    owner="$(file_uid "$file")"
    if [ "${EUID:-$(id -u)}" -eq 0 ]; then
      [ "$owner" = "0" ] || fail "ownership inseguro no bundle: $rel"
    fi
  done < "$inventory"

  while IFS=$'\t' read -r hash path; do
    [ -n "$hash" ] || continue
    file="$BUNDLE_DIR/$path"
    path_unsafe "$file" && fail "caminho com symlink ou componente inseguro: $path"
    [ -f "$file" ] && [ ! -L "$file" ] || fail "arquivo ausente ou inseguro no bundle: $path"
    actual="$(sha256_file "$file")" || fail "falha ao calcular o SHA-256: $path"
    [ "$actual" = "$hash" ] || fail "conteudo divergente no bundle: $path"
  done < "$entries_file"

  [ -f "$BUNDLE_DIR/scripts/lib/contabase-update-wrapper.sh" ] \
    && [ ! -L "$BUNDLE_DIR/scripts/lib/contabase-update-wrapper.sh" ] \
    || fail "wrapper verificado ausente ou inseguro."
  [ "$(sha256_file "$BUNDLE_DIR/scripts/lib/contabase-update-wrapper.sh")" = "$WRAPPER_SHA" ] \
    || fail "wrapper verificado diverge do manifesto."
}

make_work_dir() {
  local base temp_dir owner
  [ -z "$WORK_DIR" ] || return 0
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    base="/var/tmp"
    [ -d "$base" ] || base="/tmp"
  else
    base="${TMPDIR:-/var/tmp}"
    case "$base" in
      /*) ;;
      *) base="/var/tmp" ;;
    esac
    [ -d "$base" ] || base="/tmp"
  fi
  temp_dir="$(mktemp -d "${base%/}/contabase-update.XXXXXX")" \
    || fail "nao foi possivel criar diretorio privado de execucao."
  WORK_DIR="$(cd "$temp_dir" && pwd -P)" || fail "diretorio privado inacessivel."
  chmod 0700 "$WORK_DIR" || fail "nao foi possivel ajustar o diretorio privado."
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    chown root:root "$WORK_DIR" || fail "nao foi possivel ajustar ownership do diretorio privado."
  fi
  owner="$(file_uid "$WORK_DIR")"
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    [ "$owner" = "0" ] || fail "diretorio privado sem ownership root."
  fi
  META_DIR="$WORK_DIR/meta"
  mkdir -p "$META_DIR" "$WORK_DIR/tmp" || fail "nao foi possivel preparar o diretorio privado."
  chmod 0700 "$META_DIR" "$WORK_DIR/tmp" || fail "nao foi possivel proteger o diretorio privado."
}

acquire_lock() {
  [ -d "$STATE_DIR" ] || fail "diretorio de estado ausente: $STATE_DIR"
  [ -w "$STATE_DIR" ] || fail "sem permissao de escrita no diretorio de estado: $STATE_DIR"
  if [ -e "$STATE_DIR/contabase-update.lock.d" ] || [ -L "$STATE_DIR/contabase-update.lock.d" ]; then
    fail "lock legado contabase-update.lock.d presente; intervencao necessaria."
  fi
  command -v flock >/dev/null 2>&1 \
    || fail "flock obrigatório para atualização segura. Instale util-linux."
  LOCK_FILE="$STATE_DIR/contabase-update.lock"
  exec 200>"$LOCK_FILE" || fail "nao foi possivel abrir o lock global."
  chmod 0600 "$LOCK_FILE" 2>/dev/null || fail "nao foi possivel proteger o lock global."
  if ! flock -n 200 2>/dev/null; then
    exec 200>&-
    fail "outra atualizacao esta em curso."
  fi
  LOCK_HELD=true
  LOCKED_STATE_DIR="$STATE_DIR"
}

release_lock() {
  [ "$LOCK_HELD" = true ] || return 0
  LOCK_HELD=false
  exec 200>&- 2>/dev/null || true
  return 0
}

cleanup() {
  local rc=0 child_gone=true i
  if [ -n "$CHILD_PID" ]; then
    if [ -z "$CHILD_START" ]; then
      if kill -0 "$CHILD_PID" 2>/dev/null; then
        warn "identidade do updater desconhecida; lock e workdir preservados."
        child_gone=false
        rc=1
      else
        wait "$CHILD_PID" 2>/dev/null || true
      fi
    elif process_matches "$CHILD_PID" "$CHILD_START"; then
      kill -TERM "$CHILD_PID" 2>/dev/null || true
      for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
        process_matches "$CHILD_PID" "$CHILD_START" || break
        sleep 0.1
      done
      if process_matches "$CHILD_PID" "$CHILD_START"; then
        kill -KILL "$CHILD_PID" 2>/dev/null || true
        for i in 1 2 3 4 5 6 7 8 9 10; do
          process_matches "$CHILD_PID" "$CHILD_START" || break
          sleep 0.1
        done
      fi
      wait "$CHILD_PID" 2>/dev/null || true
      if process_matches "$CHILD_PID" "$CHILD_START"; then
        warn "updater ainda ativo; lock e workdir preservados."
        child_gone=false
        rc=1
      fi
    else
      wait "$CHILD_PID" 2>/dev/null || true
    fi
    if [ "$child_gone" = true ]; then
      CHILD_PID=""
    fi
  fi

  if [ "$child_gone" = true ] && [ -n "$WORK_DIR" ]; then
    case "$WORK_DIR" in
      */contabase-update.*) rm -rf "$WORK_DIR" ;;
    esac
  fi
  if [ "$child_gone" = true ]; then
    release_lock || rc=1
  fi
  return "$rc"
}

check_write_access() {
  local mode
  [ -d "$STATE_DIR" ] && [ -w "$STATE_DIR" ] \
    || fail "sem permissao de escrita no diretorio de estado: $STATE_DIR"
  local wrapper_dir
  wrapper_dir="$(dirname "$WRAPPER_PATH")"
  if [ "$SAVED_METHOD" != "docker-image" ]; then
    [ -d "$wrapper_dir" ] && [ -w "$wrapper_dir" ] \
      || fail "sem permissao de escrita no diretorio do wrapper: $wrapper_dir"
  fi
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    [ "$(file_uid "$STATE_DIR")" = "0" ] || fail "diretorio de estado sem ownership root."
    mode="$(file_mode "$STATE_DIR")"
    if (( (8#$mode & 0022) != 0 )); then
      fail "diretorio de estado gravavel por grupo/outros."
    fi
    if [ "$SAVED_METHOD" != "docker-image" ]; then
      [ "$(file_uid "$wrapper_dir")" = "0" ] || fail "diretorio do wrapper sem ownership root."
      mode="$(file_mode "$wrapper_dir")"
      if (( (8#$mode & 0022) != 0 )); then
        fail "diretorio do wrapper gravavel por grupo/outros."
      fi
    fi
  fi
  case "$SAVED_METHOD" in
    source|docker)
      [ -w "$SAVED_REPO_PATH" ] || fail "checkout da instalacao sem permissao de escrita: $SAVED_REPO_PATH"
      ;;
  esac
}

authenticate_bundle() {
  local version="$1" commit="$2" manifest_sha="$3" method="$4" hash path dest
  TARGET_UPDATER_SHA="$manifest_sha"
  make_work_dir
  BUNDLE_DIR="$WORK_DIR/bundle"
  mkdir -p "$BUNDLE_DIR/scripts/lib" || fail "nao foi possivel preparar o bundle privado."
  chmod 0700 "$BUNDLE_DIR" "$BUNDLE_DIR/scripts" "$BUNDLE_DIR/scripts/lib" \
    || fail "nao foi possivel proteger o bundle privado."

  download_url "${CHANNEL_BASE%/}/updaters/${version}/updater.sha256" \
    "$BUNDLE_DIR/updater.sha256" 65536
  chmod 0600 "$BUNDLE_DIR/updater.sha256" || fail "nao foi possivel proteger updater.sha256."

  verify_manifest_entries "$version" "$commit" "$manifest_sha" "$method"

  while IFS=$'\t' read -r hash path; do
    [ -n "$hash" ] || continue
    dest="$BUNDLE_DIR/$path"
    path_unsafe "$dest" && fail "caminho com symlink ou componente inseguro: $path"
    mkdir -p "$(dirname "$dest")" || fail "nao foi possivel criar diretorio do bundle: $path"
    chmod 0700 "$(dirname "$dest")" || fail "nao foi possivel proteger diretorio do bundle: $path"
    download_url "${RAW_BASE%/}/${commit}/${path}" "$dest" 4194304
    chmod 0600 "$dest" || fail "nao foi possivel proteger arquivo do bundle: $path"
  done < "$META_DIR/entries.tsv"

  verify_bundle_files "$version" "$method"
}

verify_github_identity_for_target() {
  local version="$1" channel="$2" expected_commit="$3" prerelease="false"
  if [ "$channel" = "beta" ]; then prerelease="true"; fi
  verify_github_identity "$version" "$channel" "$prerelease" "$expected_commit"
}

install_wrapper_from_bundle() {
  local src="$BUNDLE_DIR/scripts/lib/contabase-update-wrapper.sh"
  local dest="$WRAPPER_PATH" dir tmp
  [ -n "$BUNDLE_DIR" ] && [ -n "$WRAPPER_SHA" ] || { warn "bundle autenticado ausente para instalar o wrapper."; return 1; }
  [ -f "$src" ] && [ ! -L "$src" ] || { warn "wrapper verificado ausente ou inseguro."; return 1; }
  [ "$(sha256_file "$src")" = "$WRAPPER_SHA" ] || { warn "wrapper verificado mudou antes da instalacao."; return 1; }
  case "$dest" in
    /*) ;;
    *) warn "destino do wrapper deve ser absoluto: $dest"; return 1 ;;
  esac
  if path_unsafe "$dest"; then
    warn "destino do wrapper contem symlink ou componente inseguro."
    return 1
  fi
  if [ -e "$dest" ] && [ -L "$dest" ]; then
    warn "destino do wrapper e symlink."
    return 1
  fi
  if [ -d "$dest" ]; then
    warn "destino do wrapper e diretorio."
    return 1
  fi
  dir="$(dirname "$dest")"
  [ -d "$dir" ] || { warn "diretorio do wrapper ausente: $dir"; return 1; }
  if [ "${EUID:-$(id -u)}" -ne 0 ]; then
    [ -w "$dir" ] || { warn "diretorio do wrapper sem escrita: $dir"; return 1; }
  else
    [ "$(file_uid "$dir")" = "0" ] || { warn "diretorio do wrapper sem ownership root: $dir"; return 1; }
    local dir_mode
    dir_mode="$(file_mode "$dir")"
    case "$dir_mode" in
      ""|*[!0-7]*) warn "modo invalido do diretorio do wrapper: $dir"; return 1 ;;
    esac
    if (( (8#$dir_mode & 0022) != 0 )); then
      warn "diretorio do wrapper gravavel por grupo/outros: $dir"
      return 1
    fi
  fi
  tmp="$(mktemp "${dir%/}/.contabase-update.XXXXXX")" || { warn "nao foi possivel criar wrapper temporario."; return 1; }
  if ! install -m 0755 "$src" "$tmp"; then
    rm -f "$tmp"
    warn "nao foi possivel preparar o wrapper autenticado."
    return 1
  fi
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    if ! chown root:root "$tmp" 2>/dev/null; then
      rm -f "$tmp"
      warn "nao foi possivel ajustar ownership do wrapper."
      return 1
    fi
  fi
  if ! chmod 0755 "$tmp"; then
    rm -f "$tmp"
    warn "nao foi possivel ajustar modo do wrapper."
    return 1
  fi
  if ! mv -f "$tmp" "$dest"; then
    rm -f "$tmp"
    warn "nao foi possivel instalar o wrapper autenticado."
    return 1
  fi
  if [ "$(sha256_file "$dest")" != "$WRAPPER_SHA" ]; then
    warn "wrapper instalado diverge do bundle autenticado."
    return 1
  fi
  if [ ! -e "$ALIAS_PATH" ]; then
    ln -s "$(basename "$dest")" "$ALIAS_PATH" 2>/dev/null || true
  fi
  return 0
}

write_wrapper_pending() {
  local version="$1" sha="${2:-unknown}" tmp
  [ -d "$STATE_DIR" ] || return 1
  tmp="$(mktemp "${STATE_DIR%/}/.wrapper.pending.XXXXXX")" || return 1
  if ! {
    printf 'schema=1\n'
    printf 'version=%s\n' "$version"
    printf 'sha256=%s\n' "$sha"
  } > "$tmp"; then
    rm -f "$tmp"
    return 1
  fi
  chmod 0600 "$tmp" || { rm -f "$tmp"; return 1; }
  mv -f "$tmp" "$WRAPPER_MARKER" || { rm -f "$tmp"; return 1; }
  sync 2>/dev/null || true
}

read_wrapper_pending() {
  local size
  [ -f "$WRAPPER_MARKER" ] && [ ! -L "$WRAPPER_MARKER" ] \
    || fail "marcador de backfill do wrapper ausente ou inseguro."
  size="$(wc -c < "$WRAPPER_MARKER" | tr -d '[:space:]')"
  [ "$size" -le 4096 ] || fail "marcador de backfill do wrapper excede limite."
  LC_ALL=C awk -F= '
    /^[[:space:]]*$/ { next }
    {
      if ($1 !~ /^(schema|version|sha256)$/ || ($1 in seen)) exit 1
      seen[$1] = 1
    }
    END { if (seen["schema"] != 1 || !("version" in seen) || !("sha256" in seen)) exit 1 }
  ' "$WRAPPER_MARKER" || fail "marcador de backfill do wrapper invalido."
  [ "$(state_value schema "$WRAPPER_MARKER")" = "1" ] \
    || fail "marcador de backfill do wrapper com schema invalido."
  PENDING_WRAPPER_VERSION="$(state_value version "$WRAPPER_MARKER")"
  PENDING_WRAPPER_SHA="$(state_value sha256 "$WRAPPER_MARKER")"
  validate_public_version "$PENDING_WRAPPER_VERSION"
  case "$PENDING_WRAPPER_SHA" in
    unknown) ;;
    *) [[ "$PENDING_WRAPPER_SHA" =~ ^[0-9a-f]{64}$ ]] || fail "hash do marcador de backfill invalido." ;;
  esac
}

remove_wrapper_pending() {
  [ -e "$WRAPPER_MARKER" ] || return 0
  [ -f "$WRAPPER_MARKER" ] && [ ! -L "$WRAPPER_MARKER" ] \
    || fail "marcador de backfill do wrapper inseguro."
  rm -f "$WRAPPER_MARKER" || fail "nao foi possivel remover marcador de backfill."
}

run_child() {
  local rc=0 state=""
  "$@" &
  CHILD_PID=$!
  CHILD_START="$(process_start "$CHILD_PID")"
  if [ -z "$CHILD_START" ]; then
    # The child may have exited before ps observed it. Reap it and propagate
    # its real status. A zombie is also already finished; only a live process
    # without identity keeps the fail-closed path.
    if ! kill -0 "$CHILD_PID" 2>/dev/null; then
      if wait "$CHILD_PID"; then rc=0; else rc=$?; fi
      CHILD_PID=""
      return "$rc"
    fi
    state="$(process_state "$CHILD_PID")"
    case "$state" in
      Z*)
        if wait "$CHILD_PID"; then rc=0; else rc=$?; fi
        CHILD_PID=""
        return "$rc"
        ;;
    esac
    kill -TERM "$CHILD_PID" 2>/dev/null || true
    kill -KILL "$CHILD_PID" 2>/dev/null || true
    wait "$CHILD_PID" 2>/dev/null || true
    CHILD_PID=""
    return 1
  fi
  if wait "$CHILD_PID"; then
    rc=0
  else
    rc=$?
  fi
  CHILD_PID=""
  return "$rc"
}

build_updater_env() {
  local method="$1" channel="$2" version="$3" commit="$4" manifest_sha="$5"
  local home_dir
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    home_dir="/root"
  else
    home_dir="${HOME:-/root}"
  fi
  ENV_ARGS=(
    "PATH=$SAFE_PATH"
    "HOME=$home_dir"
    "LANG=C"
    "LC_ALL=C"
    "TMPDIR=$WORK_DIR/tmp"
    "CONTABASE_VERSION=$version"
    "CONTABASE_UPDATER_BUNDLE_COMMIT=$commit"
    "CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256=$manifest_sha"
    "CONTABASE_STATE_FILE=$STATE_FILE"
    "CONTABASE_INSTALL_METHOD=$SAVED_METHOD"
    "CONTABASE_CHANNEL=$channel"
    "CONTABASE_ASSUME_YES=$ASSUME_YES"
  )
  case "$method" in
    source|docker)
      ENV_ARGS+=("CONTABASE_REPO_ROOT=$SAVED_REPO_PATH")
      ;;
    release)
      ENV_ARGS+=("CONTABASE_UPDATE_WRAPPER_PATH=$BUNDLE_DIR/scripts/lib/contabase-update-wrapper.sh")
      ENV_ARGS+=("CONTABASE_UPDATE_WRAPPER_SHA256=$WRAPPER_SHA")
      ;;
  esac
  local name
  for name in CONTABASE_INSTALL_DIR CONTABASE_DATA_DIR CONTABASE_CONFIG_DIR \
    CONTABASE_ENV_FILE CONTABASE_MODE_FILE CONTABASE_TMP_BINARY CONTABASE_UPDATE_WRAPPER \
    CONTABASE_UPDATE_ALIAS CONTABASE_SERVICE_FILE CONTABASE_HEALTHCHECK_ATTEMPTS \
    CONTABASE_TIMEZONE CONTABASE_USER CONTABASE_PORT; do
    if [ -n "${!name+x}" ]; then
      ENV_ARGS+=("${name}=${!name}")
    fi
  done
}

run_authenticated_update() {
  local method="$1" script rc=0
  script="$BUNDLE_DIR/scripts/update-contabase-${method}.sh"
  [ -f "$script" ] && [ ! -L "$script" ] || fail "updater autenticado ausente: $script"
  build_updater_env "$method" "$UPDATER_CHANNEL" "$TARGET_VERSION" "$TARGET_COMMIT" "$TARGET_UPDATER_SHA"
  local cmd=()
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    cmd=(env -i "${ENV_ARGS[@]}" bash "$script")
  else
    cmd=(env "${ENV_ARGS[@]}" bash "$script")
  fi
  if [ "$UPDATE_DRY_RUN" = true ]; then cmd+=("--dry-run"); fi
  if run_child "${cmd[@]}"; then
    :
  else
    rc=$?
    return "$rc"
  fi
  if [ "$UPDATE_DRY_RUN" = true ]; then return 0; fi
  if ! install_wrapper_from_bundle; then
    write_wrapper_pending "$TARGET_VERSION" "${WRAPPER_SHA:-unknown}" || true
    fail "update concluido, mas o backfill do wrapper falhou; estado preservado."
  fi
  remove_wrapper_pending
  return 0
}

locate_docker_image_helper() {
  local wrapper_dir wrapper_parent candidate
  wrapper_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd -P)" \
    || return 1
  wrapper_parent="$(cd "$wrapper_dir/.." 2>/dev/null && pwd -P)" \
    || return 1
  for candidate in \
    "$wrapper_parent/lib/contabase/contabase-docker-image-update.py" \
    "$wrapper_dir/contabase-docker-image-update.py"
  do
    if [ -e "$candidate" ] || [ -L "$candidate" ]; then
      [ -f "$candidate" ] && [ ! -L "$candidate" ] \
        || fail "consumidor docker-image ausente ou symlink: $candidate"
      path_unsafe "$candidate" && fail "caminho do consumidor docker-image inseguro: $candidate"
      printf '%s' "$candidate"
      return 0
    fi
  done
  return 1
}

run_docker_image_backfill() {
  local new_version new_channel new_commit new_repo wrapper_dir
  wrapper_dir="$(dirname "$WRAPPER_PATH")"
  if [ ! -d "$wrapper_dir" ] || [ ! -w "$wrapper_dir" ]; then
    warn "diretorio do wrapper sem escrita; backfill do wrapper adiado."
    return 0
  fi
  new_version="$(state_value CONTABASE_INSTALLED_VERSION "$STATE_FILE")"
  new_channel="$(state_value CONTABASE_CHANNEL "$STATE_FILE")"
  new_commit="$(state_value CONTABASE_COMMIT "$STATE_FILE")"
  new_repo="$(state_value CONTABASE_REPO_PATH "$STATE_FILE")"
  if [ -z "$new_commit" ] || [ -z "$new_repo" ] \
    || ! grep -q '^CONTABASE_IMAGE_INDEX_DIGEST=' "$STATE_FILE" 2>/dev/null; then
    warn "estado docker-image incompleto; backfill do wrapper adiado."
    return 0
  fi
  validate_public_version "$new_version"
  canonicalize_version "$new_version" >/dev/null 2>&1 \
    || fail "versao docker-image instalada nao suportada: $new_version"
  case "$new_channel" in
    beta|stable) ;;
    *) fail "canal docker-image invalido no estado final: $new_channel" ;;
  esac
  [[ "$new_commit" =~ ^[0-9a-f]{40}$ ]] || fail "commit docker-image invalido no estado final."

  make_work_dir
  if ! ( resolve_channel_manifest "$new_channel"; \
    printf '%s\n%s\n%s\n' "$MANIFEST_VERSION" "$MANIFEST_COMMIT" "$MANIFEST_UPDATER_SHA" > "$META_DIR/channel-resolved" ); then
    warn "manifesto do canal indisponivel; backfill do wrapper adiado."
    return 0
  fi
  {
    IFS= read -r MANIFEST_VERSION || true
    IFS= read -r MANIFEST_COMMIT || true
    IFS= read -r MANIFEST_UPDATER_SHA || true
  } < "$META_DIR/channel-resolved"

  if [ "$MANIFEST_VERSION" = "$new_version" ]; then
    TARGET_COMMIT="$MANIFEST_COMMIT"
    TARGET_UPDATER_SHA="$MANIFEST_UPDATER_SHA"
  else
    if ! ( resolve_catalog_entry "$new_version" "$new_channel"; \
      printf '%s\n%s\n' "$CATALOG_COMMIT" "$CATALOG_UPDATER_SHA" > "$META_DIR/catalog-resolved" ); then
      warn "versao docker-image nao autorizada no catalogo; backfill do wrapper adiado."
      return 0
    fi
    {
      IFS= read -r CATALOG_COMMIT || true
      IFS= read -r CATALOG_UPDATER_SHA || true
    } < "$META_DIR/catalog-resolved"
    TARGET_COMMIT="$CATALOG_COMMIT"
    TARGET_UPDATER_SHA="$CATALOG_UPDATER_SHA"
  fi
  [ "$TARGET_COMMIT" = "$new_commit" ] \
    || fail "commit docker-image instalado diverge do manifesto autorizado."
  write_wrapper_pending "$new_version" "unknown" || true
  verify_github_identity_for_target "$new_version" "$new_channel" "$TARGET_COMMIT"
  authenticate_bundle "$new_version" "$TARGET_COMMIT" "$TARGET_UPDATER_SHA" docker-image
  if ! install_wrapper_from_bundle; then
    fail "update docker-image concluido, mas o backfill do wrapper falhou."
  fi
  remove_wrapper_pending
  return 0
}

run_docker_image_dispatch() {
  local helper rc=0 args=() cmd=()
  helper="$(locate_docker_image_helper)" || fail "consumidor docker-image ausente ou inseguro."
  [ -f "$helper" ] && [ ! -L "$helper" ] || fail "consumidor docker-image inseguro."
  case "$(file_mode "$helper")" in
    ""|*[!0-7]*) fail "modo invalido do consumidor docker-image." ;;
  esac
  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    [ "$(file_uid "$helper")" = "0" ] || fail "ownership inseguro do consumidor docker-image."
    if (( (8#$(file_mode "$helper") & 0022) != 0 )); then
      fail "consumidor docker-image gravavel por grupo/outros."
    fi
  fi

  args=(--state-file "$STATE_FILE")
  if [ -n "$TARGET_VERSION" ]; then args+=("$TARGET_VERSION"); fi
  if [ -z "$TARGET_CHANNEL" ]; then
    case "$SAVED_CHANNEL" in
      beta|stable) TARGET_CHANNEL="$SAVED_CHANNEL" ;;
    esac
  fi
  case "$TARGET_CHANNEL" in
    beta|stable) args+=(--channel "$TARGET_CHANNEL") ;;
  esac
  if [ "$ASSUME_YES" = "1" ]; then
    args+=(--yes)
  fi
  if [ "$UPDATE_DRY_RUN" = true ] || [ "$RESOLVE_ONLY" = "1" ]; then
    args+=(--dry-run)
  fi

  if [ "${EUID:-$(id -u)}" -eq 0 ]; then
    cmd=(env -i "PATH=$SAFE_PATH" "HOME=/root" "LANG=C" "LC_ALL=C" \
      "TMPDIR=/var/tmp" "CONTABASE_INSTALL_METHOD=docker-image" \
      "CONTABASE_ASSUME_YES=$ASSUME_YES" \
      python3 -I -B "$helper" "${args[@]}")
  else
    cmd=(env "CONTABASE_INSTALL_METHOD=docker-image" "CONTABASE_ASSUME_YES=$ASSUME_YES" \
      python3 -I -B "$helper" "${args[@]}")
  fi
  if run_child "${cmd[@]}"; then
    :
  else
    rc=$?
    return "$rc"
  fi
  if [ "$RESOLVE_ONLY" = "1" ]; then return 0; fi
  if [ "$UPDATE_DRY_RUN" = true ]; then return 0; fi
  run_docker_image_backfill
  return $?
}

check_pending_markers() {
  local marker kind method target size
  for marker in "$STATE_DIR/finalize.pending" "$STATE_DIR/transition.pending"; do
    [ -e "$marker" ] || [ -L "$marker" ] || continue
    [ -f "$marker" ] && [ ! -L "$marker" ] || fail "marcador de atualizacao inseguro: $marker"
    size="$(wc -c < "$marker" | tr -d '[:space:]')"
    [ "$size" -le 16384 ] || fail "marcador de atualizacao excede limite: $marker"
    kind="$(state_value target_kind "$marker")"
    method="$(state_value method "$marker")"
    target="$(state_value target_version "$marker")"
    case "$method" in
      source|docker) ;;
      "") fail "marcador de atualizacao sem metodo: $marker" ;;
      *) fail "marcador de atualizacao com metodo invalido: $method" ;;
    esac
    [ "$method" = "$SAVED_METHOD" ] \
      || fail "marcador de atualizacao diverge do metodo salvo: $method != $SAVED_METHOD"
    [ -n "$target" ] || fail "marcador de atualizacao sem versao alvo: $marker"
    validate_public_version "$target"
    case "$kind" in
      tag|branch) ;;
      *) fail "marcador de atualizacao com target_kind invalido: $kind" ;;
    esac
    PENDING_UPDATE=true
  done
}

installed_checkout_version() {
  local version_file=""
  case "$SAVED_METHOD" in
    source|docker)
      version_file="$SAVED_REPO_PATH/VERSION"
      ;;
    release|binary)
      version_file="${CONTABASE_INSTALL_DIR:-/opt/contabase}/VERSION"
      ;;
    docker-image)
      return 0
      ;;
  esac
  [ -n "$version_file" ] || return 0
  [ -f "$version_file" ] && [ ! -L "$version_file" ] || return 0
  tr -d '[:space:]' < "$version_file"
}

installed_commit() {
  case "$SAVED_METHOD" in
    source|docker)
      state_value CONTABASE_INSTALL_COMMIT "$STATE_FILE"
      ;;
    release|binary|docker-image)
      state_value CONTABASE_COMMIT "$STATE_FILE"
      ;;
  esac
}

check_interrupted_update() {
  local checkout
  [ "$SAVED_METHOD" = "docker-image" ] && return 0
  checkout="$(installed_checkout_version)"
  [ -n "$checkout" ] || return 0
  validate_public_version "$checkout"
  if [ "$SAVED_VERSION" = "$TARGET_VERSION" ] && [ "$checkout" != "$TARGET_VERSION" ]; then
    INTERRUPTED_UPDATE=true
    return 0
  fi
  if [ "$checkout" = "$TARGET_VERSION" ] && [ "$SAVED_VERSION" != "$TARGET_VERSION" ]; then
    INTERRUPTED_UPDATE=true
    return 0
  fi
  if [ "$checkout" != "$SAVED_VERSION" ]; then
    INTERRUPTED_UPDATE=true
  fi
}

resolve_explicit_target_identity() {
  local version_channel
  version_channel="$TARGET_CHANNEL"
  resolve_channel_manifest "$version_channel"
  if [ "$MANIFEST_VERSION" = "$TARGET_VERSION" ]; then
    TARGET_COMMIT="$MANIFEST_COMMIT"
    TARGET_UPDATER_SHA="$MANIFEST_UPDATER_SHA"
    return 0
  fi
  resolve_catalog_entry "$TARGET_VERSION" "$version_channel"
  TARGET_COMMIT="$CATALOG_COMMIT"
  TARGET_UPDATER_SHA="$CATALOG_UPDATER_SHA"
}

resolve_target() {
  local version_channel
  if [ -n "$TARGET_VERSION" ]; then
    validate_public_version "$TARGET_VERSION"
    canonicalize_version "$TARGET_VERSION" >/dev/null 2>&1 \
      || fail "versao alvo nao suportada: $TARGET_VERSION"
    version_channel="$(infer_channel_from_version "$TARGET_VERSION")"
    case "$version_channel" in
      beta|stable) ;;
      *) fail "versao publica deve ser beta ou stable: $TARGET_VERSION" ;;
    esac
    if [ -n "$TARGET_CHANNEL" ]; then
      [ "$TARGET_CHANNEL" = "$version_channel" ] \
        || fail "versao ${TARGET_VERSION} nao pertence ao canal ${TARGET_CHANNEL}."
      if [ "$SAVED_CHANNEL" != "pinned" ] && [ "$TARGET_CHANNEL" != "$SAVED_CHANNEL" ]; then
        fail "troca de canal exige fluxo proprio: ${SAVED_CHANNEL} -> ${TARGET_CHANNEL}."
      fi
    elif [ "$SAVED_CHANNEL" != "pinned" ]; then
      if [ "$RESOLVE_ONLY" = "1" ]; then
        TARGET_CHANNEL="$version_channel"
      else
        [ "$SAVED_CHANNEL" = "$version_channel" ] \
          || fail "troca de canal exige fluxo proprio: ${SAVED_CHANNEL} -> ${version_channel}."
        TARGET_CHANNEL="$SAVED_CHANNEL"
      fi
    else
      TARGET_CHANNEL="$version_channel"
    fi
    return 0
  fi

  if [ -n "$TARGET_CHANNEL" ]; then
    if [ "$SAVED_CHANNEL" != "pinned" ] && [ "$TARGET_CHANNEL" != "$SAVED_CHANNEL" ]; then
      fail "troca de canal exige fluxo proprio: ${SAVED_CHANNEL} -> ${TARGET_CHANNEL}."
    fi
    resolve_channel_manifest "$TARGET_CHANNEL"
    TARGET_VERSION="$MANIFEST_VERSION"
    TARGET_COMMIT="$MANIFEST_COMMIT"
    TARGET_UPDATER_SHA="$MANIFEST_UPDATER_SHA"
    return 0
  fi

  case "$SAVED_CHANNEL" in
    beta|stable)
      TARGET_CHANNEL="$SAVED_CHANNEL"
      resolve_channel_manifest "$TARGET_CHANNEL"
      TARGET_VERSION="$MANIFEST_VERSION"
      TARGET_COMMIT="$MANIFEST_COMMIT"
      TARGET_UPDATER_SHA="$MANIFEST_UPDATER_SHA"
      ;;
    pinned)
      fail "instalacao pinned/manual. Informe uma versao: cb-update vX.Y.Z, ou troque explicitamente de canal: cb-update --channel beta|stable."
      ;;
  esac
}

handle_wrapper_pending() {
  local bundle_method="$SAVED_METHOD"
  read_wrapper_pending
  [ "$PENDING_WRAPPER_VERSION" = "$TARGET_VERSION" ] \
    || fail "marcador de backfill do wrapper diverge do alvo ${TARGET_VERSION}."
  if [ "$bundle_method" = "binary" ]; then bundle_method="release"; fi
  verify_github_identity_for_target "$TARGET_VERSION" "$TARGET_CHANNEL" "$TARGET_COMMIT"
  authenticate_bundle "$TARGET_VERSION" "$TARGET_COMMIT" "$TARGET_UPDATER_SHA" "$bundle_method"
  if [ "$PENDING_WRAPPER_SHA" != "unknown" ] && [ "$PENDING_WRAPPER_SHA" != "$WRAPPER_SHA" ]; then
    fail "hash do marcador de backfill diverge do bundle autenticado."
  fi
  if ! install_wrapper_from_bundle; then
    fail "nao foi possivel restaurar o wrapper autenticado."
  fi
  remove_wrapper_pending
  say "Wrapper de atualizacao restaurado a partir do bundle autenticado."
}

parse_args() {
  TARGET_VERSION=""
  TARGET_CHANNEL=""
  UPDATE_DRY_RUN=false
  PASSTHROUGH_ARGS=()

  while [ "$#" -gt 0 ]; do
    case "$1" in
      --help|-h)
        usage
        exit 0
        ;;
      --channel)
        [ -n "${2:-}" ] || fail "--channel exige stable ou beta."
        TARGET_CHANNEL="$2"
        shift 2
        ;;
      --channel=*)
        TARGET_CHANNEL="${1#--channel=}"
        shift
        ;;
      --yes|-y)
        export CONTABASE_ASSUME_YES=1
        ASSUME_YES=1
        shift
        ;;
      --dry-run)
        UPDATE_DRY_RUN=true
        PASSTHROUGH_ARGS+=("--dry-run")
        shift
        ;;
      v*)
        [ -z "$TARGET_VERSION" ] || fail "informe apenas uma versao alvo."
        TARGET_VERSION="$1"
        shift
        ;;
      *)
        fail "argumento nao suportado: $1"
        ;;
    esac
  done

  if [ -n "$TARGET_CHANNEL" ]; then
    case "$TARGET_CHANNEL" in
      stable|beta) ;;
      *) fail "canal invalido: $TARGET_CHANNEL" ;;
    esac
  fi
}

main() {
  local installed_commit_value="" decision_rc=0 bundle_method=""
  parse_args "$@"
  reject_dangerous_environment
  load_state
  reject_method_override

  if [ "$RESOLVE_ONLY" = "1" ]; then
    if [ "$SAVED_METHOD" = "docker-image" ]; then
      run_docker_image_dispatch
      exit $?
    fi
    if [ -n "$TARGET_VERSION" ]; then
      resolve_target
      version_decision "$SAVED_VERSION" "$TARGET_VERSION" || decision_rc=$?
      case "$decision_rc" in
        1)
          say "Downgrade recusado: instalado=${SAVED_VERSION} oferecido=${TARGET_VERSION}."
          say "O updater normal nao realiza downgrade."
          exit 1
          ;;
        3) exit 1 ;;
      esac
      say "Update resolvido: metodo=${SAVED_METHOD} canal=${TARGET_CHANNEL} versao=${TARGET_VERSION}"
      exit 0
    fi
    make_work_dir
    resolve_target
    version_decision "$SAVED_VERSION" "$TARGET_VERSION" || decision_rc=$?
    case "$decision_rc" in
      1)
        say "Downgrade recusado: instalado=${SAVED_VERSION} oferecido=${TARGET_VERSION}."
        say "O updater normal nao realiza downgrade."
        exit 1
        ;;
      3) exit 1 ;;
    esac
    say "Update resolvido: metodo=${SAVED_METHOD} canal=${TARGET_CHANNEL} versao=${TARGET_VERSION}"
    exit 0
  fi

  acquire_lock
  load_state
  reject_method_override
  if [ "$STATE_DIR" != "$LOCKED_STATE_DIR" ]; then
    warn "diretorio de estado mudou durante a aquisicao do lock."
    release_lock || true
    fail "estado da instalacao mudou; nenhuma mutacao executada."
  fi

  if [ "$SAVED_METHOD" = "docker-image" ]; then
    check_write_access
    run_docker_image_dispatch
    exit $?
  fi

  if [ -n "$TARGET_VERSION" ]; then
    resolve_target
    version_decision "$SAVED_VERSION" "$TARGET_VERSION" || decision_rc=$?
    case "$decision_rc" in
      1)
        say "Downgrade recusado: instalado=${SAVED_VERSION} oferecido=${TARGET_VERSION}."
        say "O updater normal nao realiza downgrade."
        exit 1
        ;;
      3) exit 1 ;;
    esac
    check_pending_markers
    check_interrupted_update
    if [ "$decision_rc" = "2" ] && [ "$PENDING_UPDATE" = false ] && [ "$INTERRUPTED_UPDATE" = false ] \
      && [ ! -e "$WRAPPER_MARKER" ] && [ ! -L "$WRAPPER_MARKER" ]; then
      installed_commit_value="$(installed_commit)"
      if [ -z "$installed_commit_value" ]; then
        say "ContaBase ja esta em ${SAVED_VERSION}; nenhuma atualizacao necessaria."
        exit 0
      fi
      [[ "$installed_commit_value" =~ ^[0-9a-f]{40}$ ]] \
        || fail "commit instalado invalido no estado."
      make_work_dir
      resolve_explicit_target_identity
      [ "$installed_commit_value" = "$TARGET_COMMIT" ] \
        || fail "commit instalado (${installed_commit_value}) diverge do autorizado (${TARGET_COMMIT}) para ${TARGET_VERSION}."
      say "ContaBase ja esta em ${SAVED_VERSION}; commit confirmado."
      exit 0
    fi

    check_write_access
    make_work_dir
    resolve_explicit_target_identity
    if [ "$decision_rc" = "2" ] && [ "$PENDING_UPDATE" = false ] && [ "$INTERRUPTED_UPDATE" = false ] \
      && { [ -e "$WRAPPER_MARKER" ] || [ -L "$WRAPPER_MARKER" ]; }; then
      handle_wrapper_pending
      exit 0
    fi
  else
    make_work_dir
    resolve_target
    version_decision "$SAVED_VERSION" "$TARGET_VERSION" || decision_rc=$?
    case "$decision_rc" in
      1)
        say "Downgrade recusado: instalado=${SAVED_VERSION} oferecido=${TARGET_VERSION}."
        say "O updater normal nao realiza downgrade."
        exit 1
        ;;
      3) exit 1 ;;
      2)
        check_pending_markers
        check_interrupted_update
        if [ "$PENDING_UPDATE" = false ] && [ "$INTERRUPTED_UPDATE" = false ] \
          && [ ! -e "$WRAPPER_MARKER" ] && [ ! -L "$WRAPPER_MARKER" ]; then
          installed_commit_value="$(installed_commit)"
          if [ -n "$installed_commit_value" ]; then
            [[ "$installed_commit_value" =~ ^[0-9a-f]{40}$ ]] \
              || fail "commit instalado invalido no estado."
            [ "$installed_commit_value" = "$TARGET_COMMIT" ] \
              || fail "commit instalado (${installed_commit_value}) diverge do autorizado (${TARGET_COMMIT}) para ${TARGET_VERSION}."
          fi
          say "ContaBase ja esta em ${SAVED_VERSION}; nenhuma atualizacao necessaria."
          exit 0
        fi
        if [ "$PENDING_UPDATE" = false ] && [ "$INTERRUPTED_UPDATE" = false ] \
          && { [ -e "$WRAPPER_MARKER" ] || [ -L "$WRAPPER_MARKER" ]; }; then
          check_write_access
          handle_wrapper_pending
          exit 0
        fi
        check_write_access
        ;;
      0)
        check_write_access
        ;;
    esac
  fi

  if [ -z "$TARGET_COMMIT" ] || [ -z "$TARGET_UPDATER_SHA" ]; then
    fail "identidade autenticada do alvo ausente."
  fi

  bundle_method="$SAVED_METHOD"
  if [ "$bundle_method" = "binary" ]; then bundle_method="release"; fi
  make_work_dir
  verify_github_identity_for_target "$TARGET_VERSION" "$TARGET_CHANNEL" "$TARGET_COMMIT"
  authenticate_bundle "$TARGET_VERSION" "$TARGET_COMMIT" "$TARGET_UPDATER_SHA" "$bundle_method"

  case "$SAVED_METHOD" in
    source|docker|release|binary)
      run_authenticated_update "$bundle_method"
      exit $?
      ;;
    *)
      fail "metodo de instalacao desconhecido: $SAVED_METHOD"
      ;;
  esac
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  trap cleanup EXIT
  trap 'cleanup; exit 130' INT
  trap 'cleanup; exit 143' TERM
  main "$@"
fi

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
#   - Nunca regenera AUTH_ENCRYPTION_KEY, SECURITY_MASTER_KEY ou CONTABASE_SETUP_TOKEN
#   - CONTABASE_SETUP_TOKEN e token one-time de bootstrap; update nao exige nem recria
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
CONTABASE_TIMEZONE_VALUE="${CONTABASE_TIMEZONE:-America/Sao_Paulo}"
APP_USER="${CONTABASE_USER:-contabase}"
INSTALL_DIR="${CONTABASE_INSTALL_DIR:-/opt/contabase}"
DATA_DIR="${CONTABASE_DATA_DIR:-/var/lib/contabase}"
DB_FILE="${CONTABASE_DB_FILE:-}"
UPLOADS_DIR="${CONTABASE_UPLOADS_DIR:-}"
BACKUPS_DIR=""
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
NEW_RUNTIME_STARTED=false
RELEASE_BUNDLE_VERIFIED=false
SERVICE_WAS_ACTIVE=false
STATE_PRESENT=false
PREVIOUS_VERSION="desconhecida"
PREVIOUS_VERSION_RESOLVED=false
BACKUP_PATH=""
BACKUP_ID=""
CANONICAL_BACKUP_HELPER=""
CANONICAL_RESTORE_HELPER=""
PERSISTED_RESTORE_HELPER=""
CANONICAL_BACKUP_SHA256="6b9382927526cdd77b0d46fec7c6ff410bf003b69420a34aa080970d0956bc72"
CANONICAL_RESTORE_SHA256="dd8685882a5508fe41705280936a9b301d60248411f972b3fa5e8d288e953b5b"

# Recovery evidence is administrative state, never imported from a release bundle.
# === SAFE_RECOVERY_BLOCK_BEGIN ===
recovery_version_known() {
  [ "$1" = "v1.0.0-beta.6" ]
}

recovery_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | awk '{print $1}'
  else
    shasum -a 256 | awk '{print $1}'
  fi
}

recovery_admin_path() {
  local path="$1" owner mode
  while :; do
    [ -e "$path" ] && [ ! -L "$path" ] || return 1
    owner="$(stat -c %u "$path" 2>/dev/null)" || owner="$(stat -f %u "$path")" || return 1
    mode="$(stat -c %a "$path" 2>/dev/null)" || mode="$(stat -f %Lp "$path")" || return 1
    [ "$owner" = 0 ] && [[ "$mode" =~ ^[0-7]{3,4}$ ]] || return 1
    (( (8#$mode & 0022) == 0 )) || return 1
    [ "$path" != / ] || break
    path="$(dirname "$path")"
  done
}

recovery_source_known() {
  local root="$1" expected path actual
  [ "$(cat "$root/VERSION" 2>/dev/null)" = v1.0.0-beta.6 ] || return 1
  # Exact reviewed security sources; a version label cannot attest a local build.
  while read -r expected path; do
    [ -f "$root/$path" ] && [ ! -L "$root/$path" ] || return 1
    actual="$(recovery_sha256 < "$root/$path")" || return 1
    [ "$actual" = "$expected" ] || return 1
  done <<'RECOVERY_SOURCES'
e00dcd07672e3f89242ffecf489926621020e18e943d3becd5ad8766140db674 cmd/server/main.go
28dac28177886cef83978757d943e013924af575ae4ac58052de0a668e29487c cmd/server/server_runtime.go
0fc224bc1fa1b27cc339aaaede07f28cdc204ae5a0cc17c8bd3c8fe8589340b5 cmd/server/legacy_seed_credentials.go
e6d72d81fd2d8e4089d718e1027faf214bd5791dff7f14c0eb9d821a6637ab8a internal/security/crypto.go
RECOVERY_SOURCES
}

recovery_bundle_identity() (
  set -euo pipefail
  local root="$1" suffix="$2" unit="$3" version path file files bad digest
  [ -d "$root" ] && [ ! -L "$root" ] && [ -f "$unit" ] && [ ! -L "$unit" ] || exit 1
  cd "$root"
  version="$(cat "VERSION$suffix")"
  recovery_version_known "$version" || exit 1
  for path in contabase VERSION templates assets; do
    [ -e "$path$suffix" ] && [ ! -L "$path$suffix" ] || exit 1
  done
  [ -f "contabase$suffix" ] && [ -f "VERSION$suffix" ] \
    && [ -d "templates$suffix" ] && [ -d "assets$suffix" ] || exit 1
  set -- "contabase$suffix" "VERSION$suffix" "templates$suffix" "assets$suffix"
  if [ -e "admin$suffix" ] || [ -L "admin$suffix" ]; then
    [ -f "admin$suffix" ] && [ ! -L "admin$suffix" ] || exit 1
    set -- "$@" "admin$suffix"
  fi
  bad="$(find "$@" ! -type f ! -type d -print)" || exit 1
  [ -z "$bad" ] || exit 1
  files="$(find "$@" -type f -print | LC_ALL=C sort)" || exit 1
  # Hash a framed manifest, including logical paths, without executing old code.
  {
    printf 'bundle-v1\000%s\000' "$version"
    recovery_sha256 < "$unit"
    while IFS= read -r file; do
      [ -f "$file" ] && [ ! -L "$file" ] || exit 1
      if [ -n "$suffix" ]; then
        path="${file%%"$suffix"*}${file#*"$suffix"}"
      else
        path="$file"
      fi
      digest="$(recovery_sha256 < "$file")" || exit 1
      printf '%s\000%s\000' "$path" "$digest"
    done <<< "$files"
  } | recovery_sha256
)

recovery_proof_text() {
  printf 'contabase-recovery-v1\nkind=%s\nversion=%s\nidentity=%s\n' "$1" "$2" "$3"
}

recovery_record() {
  local directory="$1" kind="$2" version="$3" identity="$4" tmp
  case "$kind" in bundle|docker) ;; *) return 1 ;; esac
  recovery_version_known "$version" || return 1
  [[ "$identity" =~ ^[0-9a-f]{64}$ ]] || return 1
  recovery_admin_path "$(dirname "$directory")" || return 1
  [ ! -L "$directory" ] || return 1
  install -d -o root -g root -m 0700 "$directory" || return 1
  recovery_admin_path "$directory" || return 1
  tmp="$(mktemp "$directory/.proof.XXXXXX")" || return 1
  if ! recovery_proof_text "$kind" "$version" "$identity" > "$tmp" \
    || ! chmod 0600 "$tmp" || ! mv -f "$tmp" "$directory/$kind-$identity.proof"; then
    rm -f "$tmp"
    return 1
  fi
}

recovery_verify() {
  local directory="$1" kind="$2" version="$3" identity="$4" proof
  case "$kind" in bundle|docker) ;; *) return 1 ;; esac
  recovery_version_known "$version" || return 1
  [[ "$identity" =~ ^[0-9a-f]{64}$ ]] || return 1
  proof="$directory/$kind-$identity.proof"
  [ -f "$proof" ] && recovery_admin_path "$proof" || return 1
  recovery_proof_text "$kind" "$version" "$identity" | cmp -s - "$proof"
}

recovery_blocked() {
  printf '%s\n' \
    'RECOVERY_BLOCKED: nenhum codigo anterior sera ativado automaticamente.' \
    'Recuperacao exige prova de codigo corrigido e nenhuma tentativa de startup da candidata.' \
    'Mantenha o servico parado. Preserve banco, WAL, uploads, configuracao, chaves e backups.' \
    'Repare a falha e use um artefato corrigido, verificado e compativel com o schema atual.' \
    'Nao use downgrade como resgate. Restore de banco exige plano e decisao explicita.' >&2
}
# === SAFE_RECOVERY_BLOCK_END ===

say() {
  printf '%s\n' "$*"
}

fail() {
  say "Erro: $*" >&2
  exit 1
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
    "-1") return 0 ;;
    "0")  return 2 ;;
    "1")  return 1 ;;
    *)    return 3 ;;
  esac
}

resolve_current_version() {
  local persisted=""

  if [ -f "$STATE_FILE" ]; then
    persisted="$(awk -F= '$1 == "CONTABASE_INSTALLED_VERSION" { v = substr($0, index($0, "=") + 1); gsub(/[[:space:]'"'"']/, "", v); print v; exit }' "$STATE_FILE")"
  fi

  CURRENT_VERSION=""

  if [ -n "$persisted" ] && [ "$PREVIOUS_VERSION_RESOLVED" = true ] && [ "$persisted" != "$PREVIOUS_VERSION" ]; then
    fail "contradicao de versao: estado persistido=${persisted} runtime=${PREVIOUS_VERSION}."
  fi

  if [ -n "$persisted" ]; then
    CURRENT_VERSION="$persisted"
  elif [ "$PREVIOUS_VERSION_RESOLVED" = true ]; then
    CURRENT_VERSION="$PREVIOUS_VERSION"
  fi
}

enforce_version_policy() {
  local decision_rc=0
  version_decision "$CURRENT_VERSION" "$TAG" || decision_rc=$?
  case "$decision_rc" in
    0) return 0 ;;
    2)
      say "ContaBase ja esta em ${CURRENT_VERSION}; nenhuma atualizacao necessaria."
      exit 0
      ;;
    1)
      say "Downgrade recusado: instalado=${CURRENT_VERSION} oferecido=${TAG}."
      say "O updater normal nao realiza downgrade."
      exit 1
      ;;
    3)
      exit 1
      ;;
  esac
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
  for cmd in curl tar install openssl awk grep find mktemp cp wc tr sqlite3; do
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

env_set_if_missing() {
  local key="$1"
  local value="$2"
  local file="${3:-$ENV_FILE}"

  if env_key_is_set "$key" "$file"; then
    return 0
  fi
  env_set_key "$key" "$value" "$file"
  say "${key} adicionado ao .env."
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

  # Validate persistent mandatory secrets. CONTABASE_SETUP_TOKEN is a one-time
  # bootstrap token and is intentionally optional for existing installations.
  local missing=()
  for key in AUTH_ENCRYPTION_KEY SECURITY_MASTER_KEY; do
    env_key_is_set "$key" "$ENV_FILE" || missing+=("$key")
  done

  if [ "${#missing[@]}" -gt 0 ]; then
    say ""
    say "====================================================================="
    say "ERRO: Secret(s) persistente(s) obrigatorio(s) ausente(s) ou com placeholder em ${ENV_FILE}:"
    for key in "${missing[@]}"; do
      say "  - ${key}"
    done
    say ""
    say "Para resolver:"
    say "  - Edite ${ENV_FILE}"
    say "  - Gere valores fortes para AUTH_ENCRYPTION_KEY e SECURITY_MASTER_KEY."
    say "  - Nao gere CONTABASE_SETUP_TOKEN para update normal; ele e usado apenas no setup inicial."
    say "  - Execute o update novamente."
    say "====================================================================="
    return 1
  fi

  local setup_token_value
  setup_token_value="$(env_load_value "CONTABASE_SETUP_TOKEN" "$ENV_FILE" | tr -d "[:space:]'\"")"
  if [ -z "$setup_token_value" ]; then
    say "CONTABASE_SETUP_TOKEN ausente/comentado; OK para update de instalacao existente."
  elif env_is_placeholder "$setup_token_value"; then
    say "Aviso: CONTABASE_SETUP_TOKEN esta vazio ou com placeholder; ignorado no update normal."
  else
    say "CONTABASE_SETUP_TOKEN existente preservado; valor nao exibido."
  fi

  # Load custom paths from existing env (preserve user configuration)
  local custom_data_dir custom_db_file custom_uploads_dir
  custom_data_dir="$(env_load_value "DATA_DIR" "$ENV_FILE" | tr -d "[:space:]'\"")"
  if [ -n "$custom_data_dir" ] && [ "$custom_data_dir" != "/" ] && [[ "$custom_data_dir" = /* ]]; then
    DATA_DIR="$custom_data_dir"
    say "DATA_DIR detectado da configuracao existente: ${DATA_DIR}"
  fi
  custom_db_file="$(env_load_value "DB_FILE" "$ENV_FILE" | tr -d "[:space:]'\"")"
  if [ -n "$custom_db_file" ]; then
    [[ "$custom_db_file" = /* ]] || fail "DB_FILE deve ser absoluto: ${custom_db_file}"
    DB_FILE="$custom_db_file"
    say "DB_FILE detectado: ${DB_FILE}"
  elif [ -z "$DB_FILE" ]; then
    DB_FILE="${DATA_DIR}/contabase.db"
  fi
  custom_uploads_dir="$(env_load_value "UPLOADS_DIR" "$ENV_FILE" | tr -d "[:space:]'\"")"
  if [ -n "$custom_uploads_dir" ]; then
    [[ "$custom_uploads_dir" = /* ]] || fail "UPLOADS_DIR deve ser absoluto: ${custom_uploads_dir}"
    UPLOADS_DIR="$custom_uploads_dir"
    say "UPLOADS_DIR detectado: ${UPLOADS_DIR}"
  elif [ -z "$UPLOADS_DIR" ]; then
    UPLOADS_DIR="${DATA_DIR}/uploads"
  fi
  BACKUPS_DIR="${DATA_DIR}/backups"

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
  local custom_timezone
  custom_timezone="$(env_load_value "CONTABASE_TIMEZONE" "$ENV_FILE" | tr -d "[:space:]'\"")"
  [ -n "$custom_timezone" ] && say "CONTABASE_TIMEZONE detectado: ${custom_timezone}"
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
    say "Diretorio de dados ainda ausente; a classificacao de estado decidira se o update pode continuar."
  fi

  if [ -f "$SERVICE_FILE" ]; then
    say "Unit systemd detectada: ${SERVICE_FILE}"
  else
    say "Aviso: unit systemd nao encontrada em ${SERVICE_FILE}. Sera criada se ausente."
  fi

  # Detect current installed version
  if [ -f "${INSTALL_DIR}/VERSION" ]; then
    PREVIOUS_VERSION="$(tr -d '[:space:]' < "${INSTALL_DIR}/VERSION")"
    PREVIOUS_VERSION="${PREVIOUS_VERSION:-desconhecida}"
    [ "$PREVIOUS_VERSION" = "desconhecida" ] || PREVIOUS_VERSION_RESOLVED=true
    say "Versao instalada atualmente: ${PREVIOUS_VERSION}"
    if [ "$PREVIOUS_VERSION" = "$TAG" ]; then
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

validate_persistent_state() {
  STATE_PRESENT=false
  if [ -e "$DB_FILE" ] || [ -L "$DB_FILE" ]; then
    [ -f "$DB_FILE" ] && [ ! -L "$DB_FILE" ] && [ -r "$DB_FILE" ] \
      || fail "banco existente e invalido, ambiguo ou ilegivel: $DB_FILE"
    STATE_PRESENT=true
  elif [ -e "$UPLOADS_DIR" ] || [ -L "$UPLOADS_DIR" ]; then
    fail "uploads existem sem o banco esperado; estado persistente inconsistente: $UPLOADS_DIR"
  else
    say "Nenhum estado persistente encontrado; update pode continuar sem pacote de backup."
    return 0
  fi

  if [ -e "$UPLOADS_DIR" ] || [ -L "$UPLOADS_DIR" ]; then
    [ -d "$UPLOADS_DIR" ] && [ ! -L "$UPLOADS_DIR" ] && [ -r "$UPLOADS_DIR" ] \
      || fail "uploads invalidos, ambiguos ou ilegíveis: $UPLOADS_DIR"
  fi
  [ "$PREVIOUS_VERSION_RESOLVED" = true ] \
    || fail "nao foi possivel identificar a versao instalada antes do backup."
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
  install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$BACKUPS_DIR"
  if ! helper_output="$(
    APP_VERSION="$PREVIOUS_VERSION" \
      DATA_DIR="$DATA_DIR" DB_FILE="$DB_FILE" UPLOADS_DIR="$UPLOADS_DIR" \
      "$CANONICAL_BACKUP_HELPER" "$BACKUPS_DIR"
  )"; then
    say "Erro: helper canonico de backup falhou; bundle ativo nao sera alterado." >&2
    return 1
  fi
  BACKUP_PATH="$(printf '%s\n' "$helper_output" | tail -n1)"
  if ! validate_backup_package; then
    say "Erro: pacote canonico final ausente ou incompleto." >&2
    return 1
  fi
  say "Backup pre-update criado: $BACKUP_PATH"
  say "Backup ID: $BACKUP_ID"
  say "Versao anterior registrada: $PREVIOUS_VERSION"
  say "Pacote confirmado: database.sqlite + uploads/. Preserve-o ate concluir o update."
}

print_post_start_failure() {
  say ""
  say "====================================================================="
  say "A atualizacao NAO foi concluida."
  say "O runtime antigo NAO foi reativado automaticamente."
  say "O novo runtime foi parado; o banco pode ter sido migrado."
  say "O bundle que falhou e o bundle anterior foram preservados para diagnostico."
  say "Pacote pre-update: ${BACKUP_PATH:-nao aplicavel (instalacao sem estado)}"
  if [ -n "$BACKUP_PATH" ]; then
    say "Restore oficial, somente apos decisao explicita do operador:"
    say "  CONFIRM_APP_STOPPED=yes DATA_DIR=$DATA_DIR DB_FILE=$DB_FILE UPLOADS_DIR=$UPLOADS_DIR $PERSISTED_RESTORE_HELPER $BACKUP_PATH"
    say "Aviso: o restore descarta alteracoes feitas depois desse backup."
  fi
  say "====================================================================="
}

fail_after_new_runtime() {
  local message="$1"
  systemctl stop contabase >/dev/null 2>&1 || true
  systemctl status contabase --no-pager || true
  journalctl -u contabase -n 100 --no-pager || true
  print_post_start_failure
  fail "$message"
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

download_and_validate_ops_helpers() {
  local backup_url restore_url backup_actual restore_actual
  backup_url="${PUBLIC_RAW_BASE}/${TAG}/scripts/ops/backup.sh"
  restore_url="${PUBLIC_RAW_BASE}/${TAG}/scripts/ops/restore.sh"
  CANONICAL_BACKUP_HELPER="${TMP_DIR}/backup.sh"
  CANONICAL_RESTORE_HELPER="${TMP_DIR}/restore.sh"

  download_file "$backup_url" "$CANONICAL_BACKUP_HELPER"
  download_file "$restore_url" "$CANONICAL_RESTORE_HELPER"
  backup_actual="$(compute_sha256 "$CANONICAL_BACKUP_HELPER" | tr '[:upper:]' '[:lower:]')"
  restore_actual="$(compute_sha256 "$CANONICAL_RESTORE_HELPER" | tr '[:upper:]' '[:lower:]')"
  [ "$backup_actual" = "$CANONICAL_BACKUP_SHA256" ] \
    || fail "helper de backup da tag ${TAG} diverge do digest canonico vinculado ao updater."
  [ "$restore_actual" = "$CANONICAL_RESTORE_SHA256" ] \
    || fail "helper de restore da tag ${TAG} diverge do digest canonico vinculado ao updater."
  chmod 0700 "$CANONICAL_BACKUP_HELPER" "$CANONICAL_RESTORE_HELPER"
  say "Helpers operacionais da tag ${TAG} validados pelos digests vinculados ao updater."
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
  RELEASE_BUNDLE_VERIFIED=true
  download_and_validate_ops_helpers
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
    SERVICE_WAS_ACTIVE=true
    say "Parando servico contabase..."
    systemctl stop contabase || fail "nao foi possivel parar o servico contabase."
  else
    SERVICE_WAS_ACTIVE=false
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
    rollback_bundle || true
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
  [ "${RECOVERY_COMPLETED:-false}" != true ] || return 0
  local identity unit="${SERVICE_BACKUP:-$SERVICE_FILE}" previous_version
  say ""
  say "====================================================================="
  say "ROLLBACK: restaurando bundle anterior."
  say "Dados e configuracao NAO serao restaurados."
  say "====================================================================="

  systemctl stop contabase || { recovery_blocked; return 1; }
  if [ "$NEW_RUNTIME_STARTED" = true ] || [ -z "$PREVIOUS_DIR" ] || [ ! -d "$PREVIOUS_DIR" ]; then
    recovery_blocked
    return 1
  fi
  identity="$(recovery_bundle_identity "$PREVIOUS_DIR" "" "$unit")" || { recovery_blocked; return 1; }
  previous_version="$(cat "$PREVIOUS_DIR/VERSION")" || return 1
  recovery_verify "$CONFIG_DIR/recovery" bundle "$previous_version" "$identity" || { recovery_blocked; return 1; }

  if [ "$NEW_BUNDLE_ACTIVE" = true ] && [ -e "$INSTALL_DIR" ]; then
    mv "$INSTALL_DIR" "${INSTALL_DIR}.failed.$(date +%Y%m%d-%H%M%S).$$" || return 1
  fi

  [ ! -e "$INSTALL_DIR" ] || return 1
  if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
    mv "$PREVIOUS_DIR" "$INSTALL_DIR" || {
      say "Erro: nao foi possivel restaurar o bundle anterior."
      return 1
    }
    say "Bundle anterior restaurado em ${INSTALL_DIR}."
    PREVIOUS_DIR=""
    NEW_BUNDLE_ACTIVE=false
  else
    say "Nenhum bundle anterior disponivel para rollback."
    return 1
  fi

  if [ -n "$SERVICE_BACKUP" ] && [ -f "$SERVICE_BACKUP" ]; then
    cp "$SERVICE_BACKUP" "$SERVICE_FILE" || return 1
  fi

  systemctl daemon-reload || return 1
  NEW_RUNTIME_STARTED=true
  if systemctl restart contabase && wait_for_healthcheck; then
    RECOVERY_COMPLETED=true
    say "Rollback concluido. Servico saudavel com o bundle anterior."
    return 0
  fi

  say "ALERTA: Rollback do bundle concluido, mas healthcheck ainda falha."
  systemctl stop contabase || true
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

record_release_recovery() {
  local identity expected
  if [ "$RELEASE_BUNDLE_VERIFIED" = true ] \
    && [ "$REPO" = https://github.com/contabase-app/contabase ] \
    && recovery_version_known "$TAG" \
    && expected="$(recovery_bundle_identity "$EXTRACT_DIR/contabase" "" "$SERVICE_FILE")" \
    && identity="$(recovery_bundle_identity "$INSTALL_DIR" "" "$SERVICE_FILE")" \
    && [ "$identity" = "$expected" ] \
    && recovery_record "$CONFIG_DIR/recovery" bundle "$TAG" "$identity"; then
    say "Prova administrativa de recuperacao vinculada ao bundle corrigido."
  else
    say "Aviso: prova de recuperacao nao emitida; retorno automatico a este bundle ficara bloqueado."
  fi
}

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
    say "  2. Criar pacote canonico pre-update, quando houver estado"
    say "  3. Backup do .env em ${ENV_FILE}.pre-update-<timestamp>.bak"
    say "  4. Backup do bundle anterior em ${INSTALL_DIR}.previous.<timestamp>.$$"
    say "  5. Extrair novo bundle em ${INSTALL_DIR}"
    say "  6. Atualizar unit systemd em ${SERVICE_FILE}"
    say "  7. Iniciar servico e validar healthcheck em http://127.0.0.1:${PORT}/health"
    say "  8. Apos iniciar o novo runtime, falhas param a instancia e exigem decisao de restore"
    say ""
    return 0
  fi

  # Real update
  say ""
  say "====================================================================="
  say "Iniciando atualizacao para ${TAG}..."
  say "====================================================================="

  # Stop service
  stop_service

  # O pacote canonico precede qualquer alteracao do ambiente ou do bundle.
  if ! create_canonical_backup; then
    fail "backup pre-update falhou; update abortado. O servico permanece parado por seguranca; resolva a causa antes de tentar novamente."
  fi

  PERSISTED_RESTORE_HELPER="${CONFIG_DIR}/ops/restore-${TAG}.sh"
  install -d -o root -g root -m 0755 "${CONFIG_DIR}/ops"
  install -o root -g root -m 0700 "$CANONICAL_RESTORE_HELPER" "$PERSISTED_RESTORE_HELPER"

  # Somente depois do pacote pre-update a configuracao pode ser ajustada.
  backup_env_file
  backfill_access_mode_in_env
  env_set_if_missing "CONTABASE_TIMEZONE" "$CONTABASE_TIMEZONE_VALUE" "$ENV_FILE"

  install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR" "$UPLOADS_DIR" "$BACKUPS_DIR"

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
  NEW_RUNTIME_STARTED=true
  if ! systemctl restart contabase; then
    fail_after_new_runtime "novo bundle falhou ao iniciar; restore de dados exige decisao explicita."
  fi

  # Healthcheck
  if ! wait_for_healthcheck; then
    fail_after_new_runtime "novo bundle falhou no healthcheck; restore de dados exige decisao explicita."
  fi

  NEW_BUNDLE_ACTIVE=false
  record_release_recovery

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
  say "Config:     ${ENV_FILE} (VERSION atualizado; CONTABASE_ACCESS_MODE/CONTABASE_TIMEZONE preenchidos se ausentes; demais valores preservados)"
  say "Dados:      ${DATA_DIR} (preservados)"
  if [ -n "$ENV_BACKUP" ] && [ -f "$ENV_BACKUP" ]; then
    say "Env backup: ${ENV_BACKUP}"
  fi
  if [ -n "$PREVIOUS_DIR" ] && [ -e "$PREVIOUS_DIR" ]; then
    say "Bundle ant: ${PREVIOUS_DIR}"
  fi
  say "Backup:      ${BACKUP_PATH:-nao aplicavel}"
  say "Backup ID:   ${BACKUP_ID:-nao aplicavel}"
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

  # Platform/install validation is skipped in validate-only mode, but when a
  # config file is explicitly present we still validate its update contract.
  if [ "$VALIDATE_ONLY" = true ]; then
    if [ -f "$ENV_FILE" ]; then
      validate_existing_env
    fi
  else
    check_platform
    validate_existing_env
    validate_existing_installation
    validate_persistent_state
    resolve_current_version
    enforce_version_policy
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

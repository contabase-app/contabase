#!/usr/bin/env bash
# O fluxo le secrets; mantenha xtrace desativado ate o fim do processo.
set +x
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
PUBLIC_RAW_BASE="${CONTABASE_RAW_BASE:-https://raw.githubusercontent.com/contabase-app/contabase}"
BUNDLE_COMMIT="${CONTABASE_UPDATER_BUNDLE_COMMIT:-}"
BUNDLE_MANIFEST_SHA256="${CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256:-}"
UPDATE_WRAPPER_PATH="${CONTABASE_UPDATE_WRAPPER_PATH:-}"
UPDATE_WRAPPER_SHA256="${CONTABASE_UPDATE_WRAPPER_SHA256:-}"
PORT="${CONTABASE_PORT:-8080}"
CONTABASE_TIMEZONE_VALUE="${CONTABASE_TIMEZONE:-America/Sao_Paulo}"
APP_USER="${CONTABASE_USER:-contabase}"
INSTALL_DIR="${CONTABASE_INSTALL_DIR:-/opt/contabase}"
DATA_DIR="${CONTABASE_DATA_DIR:-/var/lib/contabase}"
DB_FILE="${CONTABASE_DB_FILE:-}"
UPLOADS_DIR="${CONTABASE_UPLOADS_DIR:-}"
BACKUPS_DIR=""
CONFIG_DIR="${CONTABASE_CONFIG_DIR:-/etc/contabase}"
STATE_FILE="${CONTABASE_STATE_FILE:-${CONFIG_DIR}/install-state.env}"
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
BOOTSTRAP_TMP_DIR=""
EXTRACT_DIR=""
STAGING_DIR=""
PREVIOUS_DIR=""
ENV_FILE="${CONFIG_DIR}/contabase.env"
ENV_BACKUP=""
SERVICE_FILE="${CONTABASE_SERVICE_FILE:-/etc/systemd/system/contabase.service}"
SERVICE_BACKUP=""
NEW_BUNDLE_ACTIVE=false
NEW_RUNTIME_STARTED=false
RELEASE_BUNDLE_VERIFIED=false
SERVICE_WAS_ACTIVE=false
STATE_PRESENT=false
PREVIOUS_VERSION="desconhecida"
PREVIOUS_VERSION_RESOLVED=false
BOOTSTRAP_STATE=false
INSTALL_STATE_SHA256=""
INSTALLED_UNIT_SHA256=""
CANDIDATE_COMMIT=""
CANDIDATE_ARTIFACT_SHA256=""
CANDIDATE_CHECKSUMS_SHA256=""
CANDIDATE_BINARY_SHA256=""
BACKUP_PATH=""
BACKUP_ID=""
CANONICAL_BACKUP_HELPER=""
CANONICAL_RESTORE_HELPER=""
PERSISTED_RESTORE_HELPER=""
CANONICAL_BACKUP_SHA256="6b9382927526cdd77b0d46fec7c6ff410bf003b69420a34aa080970d0956bc72"
CANONICAL_RESTORE_SHA256="dd8685882a5508fe41705280936a9b301d60248411f972b3fa5e8d288e953b5b"
BOOTSTRAP_CHANNEL_BASE="https://get-contabase.pages.dev"
BOOTSTRAP_RAW_BASE="https://raw.githubusercontent.com/contabase-app/contabase"

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


bundle_sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum < "$1" | awk '{print $1}'
  else
    shasum -a 256 < "$1" | awk '{print $1}'
  fi
}

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

# B1: a intencao de bundle autenticado e detectada antes de --help,
# --validate-only, --dry-run ou qualquer download/mutacao. Qualquer variavel
# definida (inclusive vazia) ou manifesto presente (inclusive symlink pendente)
# exige o contrato completo; nao existe fallback para o modo legado.
bundle_contract_requested=false
[ -n "${CONTABASE_UPDATER_BUNDLE_COMMIT+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATE_WRAPPER_PATH+x}" ] && bundle_contract_requested=true
[ -n "${CONTABASE_UPDATE_WRAPPER_SHA256+x}" ] && bundle_contract_requested=true
case "$SCRIPT_SOURCE" in
  /*) ;;
  *) SCRIPT_SOURCE="$PWD/$SCRIPT_SOURCE" ;;
esac
script_dir_raw="${SCRIPT_SOURCE%/*}"
script_dir_raw="${script_dir_raw:-/}"
manifest_raw="$script_dir_raw/../updater.sha256"
if [ -e "$manifest_raw" ] || [ -L "$manifest_raw" ]; then
  bundle_contract_requested=true
fi
script_path_unsafe=false
script_path_literal=""
bundle_path_unsafe "$SCRIPT_SOURCE" script_path_literal && script_path_unsafe=true
if [ "$bundle_contract_requested" = true ]; then
  [ "$script_path_unsafe" = false ] \
    || fail "caminho do updater com symlink ou componente inseguro."
  script_dir_literal="${script_path_literal%/*}"
  SCRIPT_ROOT="${script_dir_literal%/*}"
  SCRIPT_ROOT="${SCRIPT_ROOT:-/}"
  SCRIPT_DIR="$script_dir_literal"
else
  SCRIPT_DIR="$(cd "$script_dir_raw" 2>/dev/null && pwd)"
  SCRIPT_ROOT="$(cd "$SCRIPT_DIR/.." 2>/dev/null && pwd -P)"
  if [ -e "$SCRIPT_ROOT/updater.sha256" ] || [ -L "$SCRIPT_ROOT/updater.sha256" ]; then
    [ "$script_path_unsafe" = false ] \
      || fail "caminho do updater com symlink ou componente inseguro."
    bundle_contract_requested=true
  fi
fi

validate_external_bundle() {
  [ "$bundle_contract_requested" = true ] || return 0

  local version="$TAG"
  local commit="$BUNDLE_COMMIT"
  local expected="$BUNDLE_MANIFEST_SHA256"
  local wrapper_path="$UPDATE_WRAPPER_PATH"
  local wrapper_sha="$UPDATE_WRAPPER_SHA256"
  local manifest="$SCRIPT_ROOT/updater.sha256"
  local allowed="VERSION scripts/update-contabase-source.sh scripts/update-contabase-docker.sh scripts/update-contabase-release.sh scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh scripts/lib/contabase-update-wrapper.sh scripts/lib/contabase-docker-image-update.py"
  local required_paths=()
  local header actual entries hash path entry_path file actual_hash verified_file found
  local verified_files=()
  local wrapper_real wrapper_manifest_sha running_updater_sha

  required_paths=(VERSION scripts/update-contabase-release.sh \
    scripts/lib/contabase-update-wrapper.sh)

  [ -n "$version" ] || fail "CONTABASE_VERSION obrigatoria no modo autenticado."
  [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?(\+[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$ ]] \
    || fail "versao publica invalida no modo autenticado."
  for cmd in awk sed grep find tr; do
    command -v "$cmd" >/dev/null 2>&1 \
      || fail "comando obrigatorio ausente: $cmd"
  done
  [ -n "$commit" ] && [ -n "$expected" ] && [ -n "$wrapper_path" ] && [ -n "$wrapper_sha" ] \
    || fail "identidade do bundle externo incompleta (commit/manifesto/wrapper)."
  [[ "$commit" =~ ^[0-9a-f]{40}$ ]] \
    || fail "commit autorizado do bundle invalido."
  [[ "$expected" =~ ^[0-9a-f]{64}$ ]] \
    || fail "digest autorizado do manifesto invalido."
  [[ "$wrapper_sha" =~ ^[0-9a-f]{64}$ ]] \
    || fail "hash do wrapper verificado invalido."

  [ -d "$SCRIPT_ROOT/scripts" ] && [ ! -L "$SCRIPT_ROOT/scripts" ] \
    || fail "diretorio de scripts do bundle ausente ou inseguro."
  [ -f "$manifest" ] && [ ! -L "$manifest" ] \
    || fail "updater.sha256 ausente ou inseguro."
  LC_ALL=C grep -q $'\r' "$manifest" 2>/dev/null \
    && fail "CRLF nao permitido no updater.sha256."
  actual="$(bundle_sha256 "$manifest")" \
    || fail "falha ao calcular o SHA-256 do manifesto."
  [ "$actual" = "$expected" ] \
    || fail "manifesto diverge da identidade autorizada."
  header="$(sed -n '1p' "$manifest")" \
    || fail "falha ao ler o cabecalho do manifesto."
  [ "$header" = "commit=$commit" ] \
    || fail "commit do manifesto diverge do autorizado."

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
    [ "$found" = true ] || fail "manifesto incompleto: $path"
  done

  bundle_path_unsafe "$wrapper_path" wrapper_real \
    && fail "caminho do wrapper com symlink ou componente inseguro."
  [ "$wrapper_real" = "$SCRIPT_ROOT/scripts/lib/contabase-update-wrapper.sh" ] \
    || fail "wrapper verificado fora do caminho canonico."
  [ -f "$wrapper_real" ] && [ ! -L "$wrapper_real" ] \
    || fail "wrapper verificado ausente ou inseguro."
  wrapper_manifest_sha=""
  while IFS=$'\t' read -r hash entry_path; do
    [ -n "$hash" ] || continue
    if [ "$entry_path" = "scripts/lib/contabase-update-wrapper.sh" ]; then
      wrapper_manifest_sha="$hash"
      break
    fi
  done <<< "$entries"
  [ -n "$wrapper_manifest_sha" ] \
    || fail "wrapper verificado ausente do manifesto."
  [ "$(bundle_sha256 "$wrapper_real")" = "$wrapper_sha" ] \
    || fail "wrapper verificado diverge do hash autorizado."
  [ "$wrapper_manifest_sha" = "$wrapper_sha" ] \
    || fail "wrapper verificado diverge do manifesto."

  while IFS=$'\t' read -r hash path; do
    [ -n "$hash" ] || continue
    file="$SCRIPT_ROOT/$path"
    bundle_path_unsafe "$file" \
      && fail "caminho com symlink ou componente inseguro: $path"
    [ -f "$file" ] && [ ! -L "$file" ] \
      || fail "arquivo ausente ou inseguro no bundle: $path"
    actual_hash="$(bundle_sha256 "$file")" \
      || fail "falha ao calcular o SHA-256: $path"
    [ "$actual_hash" = "$hash" ] \
      || fail "conteudo divergente no bundle: $path"
    verified_files+=("$file")
  done <<< "$entries"

  [ "$(tr -d '[:space:]' < "$SCRIPT_ROOT/VERSION")" = "$version" ] \
    || fail "VERSION do bundle diverge da versao solicitada."
  running_updater_sha=""
  while IFS=$'\t' read -r hash entry_path; do
    [ -n "$hash" ] || continue
    if [ "$entry_path" = "scripts/update-contabase-release.sh" ]; then
      running_updater_sha="$hash"
      break
    fi
  done <<< "$entries"
  [ -n "$running_updater_sha" ] \
    || fail "updater Release ausente do manifesto."
  [ "$(bundle_sha256 "$SCRIPT_SOURCE")" = "$running_updater_sha" ] \
    || fail "updater Release em execucao diverge do manifesto."

  (
    inventory="$(mktemp "${TMPDIR:-/tmp}/contabase-updater-inventory.XXXXXX")" \
      || fail "falha ao criar inventario do bundle."
    trap 'rm -f -- "$inventory"' EXIT
    find "$SCRIPT_ROOT" \
      \( -type l -o \( -type f \( -perm -0100 -o -perm -0010 -o -perm -0001 \) \) \) \
      -print0 > "$inventory" 2>/dev/null \
      || fail "falha ao enumerar executaveis do bundle."
    while IFS= read -r -d '' file; do
      [ ! -L "$file" ] || fail "symlink no bundle externo."
      for verified_file in "${verified_files[@]}"; do
        [ "$file" != "$verified_file" ] || continue 2
      done
      fail "arquivo executavel nao declarado no bundle: ${file#"$SCRIPT_ROOT"/}"
    done < "$inventory"
  ) || exit 1

  UPDATE_WRAPPER_PATH="$wrapper_real"
}

validate_external_bundle

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
  if [ -n "$BOOTSTRAP_TMP_DIR" ] && [ -d "$BOOTSTRAP_TMP_DIR" ]; then
    rm -rf "$BOOTSTRAP_TMP_DIR"
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
  for cmd in curl tar install openssl awk sed grep find mktemp cp wc tr sqlite3; do
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

release_safe_path() {
  local path="$1" current="" part
  local -a parts
  [[ "$path" =~ ^/[A-Za-z0-9_./-]+$ ]] && [ "${#path}" -le 240 ] || return 1
  case "$path" in /|*/|*//*|*/./*|*/../*|*/.|*/..) return 1 ;; esac
  IFS=/ read -ra parts <<< "${path#/}"
  for part in "${parts[@]}"; do
    current="$current/$part"
    [ ! -L "$current" ] || return 1
  done
}

release_state_value() {
  awk -F= -v key="$1" '
    $1 == key {
      v=substr($0,index($0,"=")+1)
      if (v ~ /^".*"$/ || v ~ /^\047.*\047$/) v=substr(v,2,length(v)-2)
      print v; exit
    }' "$STATE_FILE"
}

release_validate_state() {
  # State is data: reject duplicate keys, shell syntax and secret-bearing fields.
  [ "$(wc -c < "$STATE_FILE")" -le 32768 ] || return 1
  [ "$(LC_ALL=C tr -d '\012\040-\176' < "$STATE_FILE" | wc -c)" -eq 0 ] || return 1
  awk -F= '
    /^[[:space:]]*$/ || /^#/ { next }
    {
      k=$1; v=substr($0,index($0,"=")+1)
      if (!index($0,"=") || length($0)>2048 || k !~ /^CONTABASE_[A-Z0-9_]+$/ || seen[k]++) exit 1
      if (k ~ /(SECRET|TOKEN|PASSWORD|KEY|CREDENTIAL)/) exit 1
      if (v ~ /^".*"$/ || v ~ /^\047.*\047$/) v=substr(v,2,length(v)-2)
      if (v ~ /[^A-Za-z0-9_.\/:@,+= -]/) exit 1
    }' "$STATE_FILE" || return 1
  [ "$(release_state_value CONTABASE_STATE_VERSION)" = 1 ] || return 1
  case "$(release_state_value CONTABASE_INSTALL_METHOD)" in release|binary) ;; *) return 1 ;; esac
  case "$(release_state_value CONTABASE_CHANNEL)" in beta|stable|pinned) ;; *) return 1 ;; esac
  canonicalize_version "$(release_state_value CONTABASE_INSTALLED_VERSION)" >/dev/null || return 1
  [ -z "$(release_state_value CONTABASE_REPO_PATH)" ] || return 1
}

release_adopt_path() {
  local key="$1" explicit="$2" current="$3" saved
  saved="$(release_state_value "$key")"
  release_safe_path "$saved" || fail "caminho persistido invalido: $key."
  [ -z "$explicit" ] || [ "$current" = "$saved" ] \
    || fail "override diverge da instalacao identificada: $key."
  printf '%s' "$saved"
}

resolve_release_installation() {
  local fragment unit_env saved key value
  # The loaded service identifies the configuration; never search other installs.
  fragment="$(systemctl show contabase.service --property=FragmentPath --value)" \
    || fail "nao foi possivel identificar a unit carregada."
  [ -f "$fragment" ] && release_safe_path "$fragment" && recovery_admin_path "$fragment" \
    || fail "unit ausente, symlink ou sem protecao administrativa."
  if [ -n "${CONTABASE_SERVICE_FILE+x}" ]; then
    [ "$SERVICE_FILE" = "$fragment" ] || fail "unit solicitada diverge da unit carregada."
  fi
  SERVICE_FILE="$fragment"
  unit_env="$(release_unit_value EnvironmentFile)" || fail "EnvironmentFile ambiguo na unit."
  release_safe_path "$unit_env" || fail "EnvironmentFile inseguro na unit."
  case "$unit_env" in */contabase.env) ;; *) fail "EnvironmentFile fora do contrato Release." ;; esac
  saved="${unit_env%/contabase.env}"
  [ -z "${CONTABASE_CONFIG_DIR+x}" ] || [ "$CONFIG_DIR" = "$saved" ] \
    || fail "configuracao solicitada diverge da unit."
  CONFIG_DIR="$saved"
  ENV_FILE="$unit_env"
  [ -z "${CONTABASE_STATE_FILE+x}" ] || [ "$STATE_FILE" = "$CONFIG_DIR/install-state.env" ] \
    || fail "estado solicitado diverge da configuracao da unit."
  STATE_FILE="$CONFIG_DIR/install-state.env"
  [ -f "$ENV_FILE" ] && recovery_admin_path "$CONFIG_DIR" && recovery_admin_path "$ENV_FILE" \
    || fail "configuracao sem protecao administrativa."
  awk -F= '
    /^[[:space:]]*$/ || /^[[:space:]]*#/ { next }
    $1 !~ /^[A-Z][A-Z0-9_]*$/ || seen[$1]++ { exit 1 }
  ' "$ENV_FILE" || fail "env com chaves duplicadas ou sintaxe ambigua."
  if [ -e "$STATE_FILE" ] || [ -L "$STATE_FILE" ]; then
    [ -f "$STATE_FILE" ] && release_safe_path "$STATE_FILE" && recovery_admin_path "$STATE_FILE" \
      && release_validate_state || fail "install-state.env invalido ou inseguro."
    INSTALL_STATE_SHA256="$(compute_sha256 "$STATE_FILE")"
    if [ -n "$(release_state_value CONTABASE_BOOTSTRAP_CONTRACT_VERSION)" ]; then
      BOOTSTRAP_STATE=true
      [ "$(release_state_value CONTABASE_BOOTSTRAP_CONTRACT_VERSION)" = 1 ] \
        && [ "$(release_state_value CONTABASE_BOOTSTRAP_ADAPTER)" = release ] \
        || fail "contrato Bootstrap Release inconsistente."
      INSTALL_DIR="$(release_adopt_path CONTABASE_INSTALL_DIR "${CONTABASE_INSTALL_DIR+x}" "$INSTALL_DIR")"
      DATA_DIR="$(release_adopt_path CONTABASE_DATA_DIR "${CONTABASE_DATA_DIR+x}" "$DATA_DIR")"
      [ "$(release_state_value CONTABASE_CONFIG_DIR)" = "$CONFIG_DIR" ] \
        || fail "diretorio de configuracao diverge do estado Bootstrap."
      for key in CONTABASE_COMMIT CONTABASE_ARTIFACT_SHA256 CONTABASE_CHECKSUMS_SHA256 CONTABASE_HELPER_SHA256; do
        value="$(release_state_value "$key")"
        case "$key" in
          CONTABASE_COMMIT) [[ "$value" =~ ^[0-9a-f]{40}$ ]] ;;
          *) [[ "$value" =~ ^[0-9a-f]{64}$ ]] ;;
        esac || fail "identidade Bootstrap invalida: $key."
      done
      [ "$(release_state_value CONTABASE_ARTIFACT_PLATFORM)" = "linux/$ARCH" ] \
        && [ "$(release_state_value CONTABASE_ARTIFACT_NAME)" = "contabase-linux-$ARCH.tar.gz" ] \
        || fail "arquitetura persistida diverge do host."
      [ -n "$(release_state_value CONTABASE_UPDATE_SUPPORT)" ] || fail "suporte de update ausente do estado Bootstrap."
    elif grep -Eq '^CONTABASE_(INSTALL_DIR|DATA_DIR|CONFIG_DIR|NETWORK_PROFILE|BOOTSTRAP_ADAPTER)=' "$STATE_FILE"; then
      fail "estado Bootstrap incompleto; nenhum fallback legado permitido."
    fi
    # Channel changes are not part of this updater correction.
    saved="$(release_state_value CONTABASE_CHANNEL)"
    [ -z "${CONTABASE_CHANNEL+x}" ] || [ "$INSTALL_CHANNEL" = "$saved" ] \
      || fail "canal solicitado diverge do estado instalado."
    INSTALL_CHANNEL="$saved"
  else
    [ -f "$CONFIG_DIR/install-mode" ] && recovery_admin_path "$CONFIG_DIR/install-mode" \
      && [ "$(cat "$CONFIG_DIR/install-mode")" = release ] \
      || fail "estado ausente; instalacao legada nao comprovada."
    if grep -Eq '^[[:space:]]*IPAddress(Deny|Allow)[[:space:]]*=' "$SERVICE_FILE"; then
      fail "estado ausente em instalacao com filtro de rede; revisao administrativa necessaria."
    fi
  fi
  if [ "$BOOTSTRAP_STATE" = false ]; then
    # Legacy paths must still be attested by the service and existing env.
    INSTALL_DIR="$(release_unit_value WorkingDirectory)" || fail "WorkingDirectory ambiguo."
    [ -z "${CONTABASE_INSTALL_DIR+x}" ] || [ "$INSTALL_DIR" = "$CONTABASE_INSTALL_DIR" ] \
      || fail "diretorio solicitado diverge da unit legada."
  fi
  INSTALLED_UNIT_SHA256="$(compute_sha256 "$SERVICE_FILE")"
}

release_unit_value() {
  awk -F= -v key="$1" '
    { line=$0; sub(/^[ \t]+/,"",line); sub(/[ \t]+$/,"",line) }
    line ~ /^\[/ { section=line }
    section=="[Service]" {
      k=line; sub(/=.*/,"",k); sub(/[ \t]+$/,"",k)
      if (k==key) {
        value=substr(line,index(line,"=")+1); sub(/^[ \t]+/,"",value); count++
      }
    }
    END { if(count!=1 || value=="") exit 1; print value }' "$SERVICE_FILE"
}

release_check_unit_value() {
  [ "$(release_unit_value "$1")" = "$2" ] || fail "unit/configuracao divergente: $1."
}

validate_release_installation_contract() {
  local path first second mode base host bind allowed item ip prefix a b c d part fragment drops reload trusted
  for path in "$INSTALL_DIR" "$CONFIG_DIR" "$DATA_DIR" "$ENV_FILE" "$STATE_FILE" \
              "$SERVICE_FILE" "$INSTALL_DIR/contabase" "$INSTALL_DIR/VERSION" "$DB_FILE" "$UPLOADS_DIR" \
              "$DB_FILE-wal" "$DB_FILE-shm" "$BACKUPS_DIR" "$CONFIG_DIR/ops" "$CONFIG_DIR/install-mode"; do
    release_safe_path "$path" || fail "path inseguro ou symlink na instalacao."
  done
  for first in "$INSTALL_DIR" "$DATA_DIR" "$CONFIG_DIR"; do
    case "$first" in /etc|/var|/var/lib|/opt|/usr|/usr/local|/home|/root|/run|/tmp) fail "diretorio generico nao permitido." ;; esac
    for second in "$INSTALL_DIR" "$DATA_DIR" "$CONFIG_DIR"; do
      [ "$first" = "$second" ] && continue
      case "$first/" in "$second/"*) fail "diretorios conflitantes na instalacao." ;; esac
    done
  done
  recovery_admin_path "$INSTALL_DIR" && recovery_admin_path "$INSTALL_DIR/contabase" \
    && recovery_admin_path "$INSTALL_DIR/VERSION" \
    && recovery_admin_path "$(dirname "$DATA_DIR")" || fail "paths sem protecao administrativa."
  [ "$(compute_sha256 "$SERVICE_FILE")" = "$INSTALLED_UNIT_SHA256" ] \
    || fail "unit alterada durante a atualizacao."
  fragment="$(systemctl show contabase.service --property=FragmentPath --value)" || fail "nao foi possivel ler FragmentPath."
  drops="$(systemctl show contabase.service --property=DropInPaths --value)" || fail "nao foi possivel ler DropInPaths."
  reload="$(systemctl show contabase.service --property=NeedDaemonReload --value)" || fail "nao foi possivel ler NeedDaemonReload."
  [ "$fragment" = "$SERVICE_FILE" ] && [ -z "$drops" ] && [ "$reload" = no ] \
    || fail "unit carregada diverge do disco ou possui drop-ins; revisar configuracao explicitamente."
  release_check_unit_value WorkingDirectory "$INSTALL_DIR"
  release_check_unit_value EnvironmentFile "$ENV_FILE"
  release_check_unit_value ExecStart "$INSTALL_DIR/contabase"
  release_check_unit_value User "$APP_USER"
  release_check_unit_value Group "$APP_USER"
  # Preserve every directive, but do not allow an override of the validated env.
  if grep -Eq '^[[:space:]]*((Environment|PassEnvironment|UnsetEnvironment|ExecStartPre|ExecStartPost|RootDirectory|RootImage|Include)[[:space:]]*=|\.include([[:space:]]|$))' "$SERVICE_FILE"; then
    fail "unit possui overrides fora do contrato; revisao administrativa necessaria."
  fi
  if [ "$BOOTSTRAP_STATE" = true ]; then
    [ "$(release_state_value CONTABASE_DATA_DIR)" = "$DATA_DIR" ] \
      && [ "$(release_state_value CONTABASE_PORT)" = "$PORT" ] || fail "env diverge dos paths/porta Bootstrap."
    [ "$(env_load_value DATA_DIR)" = "$DATA_DIR" ] \
      && [ "$(env_load_value DB_FILE)" = "$DATA_DIR/contabase.db" ] \
      && [ "$(env_load_value UPLOADS_DIR)" = "$DATA_DIR/uploads" ] \
      || fail "paths do env divergem do estado Bootstrap."
    mode="$(release_state_value CONTABASE_ACCESS_MODE)"
    [ "$mode" = "$(env_load_value CONTABASE_ACCESS_MODE)" ] || fail "perfil de acesso diverge do env."
    case "$(release_state_value CONTABASE_NETWORK_PROFILE):$mode" in
      local:local|lan:lan|proxy:proxy|tunnel:proxy|advanced:local|advanced:lan|advanced:proxy) ;;
      *) fail "perfil de rede inconsistente." ;;
    esac
    base="$(env_load_value APP_BASE_URL)"; host="$(host_from_base_url "$base")"
    case "$base" in *[!A-Za-z0-9:/._~\[\]-]*|*'@'*|*'?'*|*'#'*) fail "URL Bootstrap invalida." ;; esac
    bind="$(release_state_value CONTABASE_HOST_BIND)"
    case "$mode" in
      local) host_is_localhost "$host" && [ "$bind" = 127.0.0.1 ] && [ "$(scheme_from_base_url "$base")" = http ] || fail "rede localhost inconsistente." ;;
      lan) [ "$bind" = "$host" ] && host_is_private_ipv4 "$host" || fail "rede LAN inconsistente." ;;
      proxy) [ "$bind" = 127.0.0.1 ] && [ "$(scheme_from_base_url "$base")" = https ] \
        && [ -n "$(env_load_value TRUSTED_PROXIES)" ] || fail "rede proxy inconsistente." ;;
      *) fail "modo de acesso invalido." ;;
    esac
    allowed="$(env_load_value ALLOWED_HOSTS)"
    case ",$allowed," in *",$host,"*) ;; *) fail "ALLOWED_HOSTS diverge da URL." ;; esac
    case "$allowed" in ""|*[!A-Za-z0-9.,:_-]*|,*|*,|*,,*) fail "ALLOWED_HOSTS invalido." ;; esac
    if [ "$mode" != proxy ]; then
      [ -z "$(env_load_value TRUSTED_PROXIES)" ] || fail "proxy inesperado no perfil local/LAN."
      [ "${base##*:}" = "$PORT" ] || fail "porta da URL diverge do estado."
    fi
    if [ "$mode" = local ]; then
      local -a hosts
      IFS=, read -ra hosts <<< "$allowed"
      for item in "${hosts[@]}"; do
        case "$item" in localhost|127.0.0.1|::1) ;; *) fail "host amplia acesso alem de localhost." ;; esac
      done
    fi
    trusted="$(env_load_value TRUSTED_PROXIES)"
    if [ "$mode" = proxy ]; then
      case "$host" in *.*) ;; *) fail "dominio proxy invalido." ;; esac
      case "$host" in *[!A-Za-z0-9.-]*|.*|*.) fail "dominio proxy invalido." ;; esac
      host_is_private_ipv4 "$host" && fail "proxy exige dominio publico."
      local -a proxies
      IFS=, read -ra proxies <<< "$trusted"
      [ "${#proxies[@]}" -le 16 ] || fail "proxies excessivos."
      for item in "${proxies[@]}"; do
        case "$item" in ::1|::1/128) continue ;; esac
        ip="${item%/*}"; prefix="${item#*/}"
        [[ "$prefix" =~ ^(2[4-9]|3[0-2])$ ]] || fail "prefixo proxy invalido."
        [ "$ip" = 127.0.0.1 ] || host_is_private_ipv4 "$ip" || fail "proxy fora de rede restrita."
      done
    fi
    release_check_unit_value NoNewPrivileges true
    release_check_unit_value PrivateTmp true
    release_check_unit_value ProtectHome true
    release_check_unit_value ProtectSystem strict
    release_check_unit_value ReadWritePaths "$DATA_DIR"
    release_check_unit_value IPAddressDeny any
    allowed="$(release_unit_value IPAddressAllow)" || fail "filtro IP ausente ou ambiguo."
    local -a filters
    read -ra filters <<< "$allowed"
    for item in "${filters[@]}"; do
      case "$item" in localhost|127.0.0.1|127.0.0.1/32|::1|::1/128) continue ;; esac
      [ "$mode" = lan ] || fail "filtro IP amplia acesso alem de localhost."
      ip="${item%/*}"; prefix="${item#*/}"
      [[ "$prefix" =~ ^([0-9]|[12][0-9]|3[0-2])$ ]] || fail "prefixo IP invalido."
      IFS=. read -r a b c d <<< "$ip"
      for part in "$a" "$b" "$c" "$d"; do
        [[ "$part" =~ ^(0|[1-9][0-9]{0,2})$ ]] && [ "$part" -le 255 ] || fail "IPv4 invalido no filtro."
      done
      if [ "$a" = 10 ] && [ "$prefix" -ge 8 ]; then :
      elif [ "$a" = 172 ] && [ "$b" -ge 16 ] && [ "$b" -le 31 ] && [ "$prefix" -ge 12 ]; then :
      elif [ "$a" = 192 ] && [ "$b" = 168 ] && [ "$prefix" -ge 16 ]; then :
      else fail "filtro IP amplia acesso alem da LAN privada."; fi
    done
  fi
}

verify_release_runtime() {
  local pid exe before after
  (validate_release_installation_contract) || return 1
  systemctl is-active --quiet contabase.service || return 1
  pid="$(systemctl show contabase.service --property=MainPID --value)" || return 1
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] || return 1
  exe="$(readlink "/proc/$pid/exe")" || return 1
  [ "$exe" = "$INSTALL_DIR/contabase" ] || return 1
  before="$(compute_sha256 "/proc/$pid/exe")" || return 1
  after="$(compute_sha256 "$INSTALL_DIR/contabase")" || return 1
  [ "$before" = "$CANDIDATE_BINARY_SHA256" ] && [ "$after" = "$before" ] \
    && [ "$(cat "$INSTALL_DIR/VERSION")" = "$TAG" ] || return 1
  [ "$(systemctl show contabase.service --property=MainPID --value)" = "$pid" ] || return 1
}

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

# Contrato de recebimento do bundle externo para o updater Release.
# Roda antes de qualquer mutacao. A autenticidade da referencia
# (manifesto/commit) e responsabilidade do launcher F3-C; aqui apenas
# provamos coerencia byte a byte contra a identidade recebida.

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

resolve_candidate_identity() {
  # GitHub returns the peeled commit as plain SHA for this media type.
  CANDIDATE_COMMIT="$(curl -q --fail --silent --show-error --proto '=https' --tlsv1.2 \
    --max-time 30 --max-filesize 128 -H 'Accept: application/vnd.github.sha' \
    "https://api.github.com/repos/${REPO#https://github.com/}/commits/$TAG")" \
    || fail "nao foi possivel vincular a tag ao commit publico."
  [[ "$CANDIDATE_COMMIT" =~ ^[0-9a-f]{40}$ ]] || fail "identidade do commit da tag invalida."
  CANDIDATE_ARTIFACT_SHA256="$(compute_sha256 "$TMP_DIR/$ARTIFACT_NAME")"
  CANDIDATE_CHECKSUMS_SHA256="$(compute_sha256 "$TMP_DIR/checksums.txt")"
  CANDIDATE_BINARY_SHA256="$(compute_sha256 "$EXTRACT_DIR/contabase/contabase")"
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
  # Existing restrictions belong to this installation, not to the new bundle.
  [ -f "$SERVICE_FILE" ] && [ ! -L "$SERVICE_FILE" ] || return 1
  [ "$(compute_sha256 "$SERVICE_FILE")" = "$INSTALLED_UNIT_SHA256" ] || return 1
  SERVICE_BACKUP="${TMP_DIR}/contabase.service.previous"
  cp "$SERVICE_FILE" "$SERVICE_BACKUP" || return 1
  cmp -s "$SERVICE_FILE" "$SERVICE_BACKUP"
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

write_install_state() (
  umask 077
  local method="$1"
  local channel="$2"
  local version="$3"
  local repo_path="${4:-}"
  local tmpfile

  [ "$version" = "$TAG" ] && [ "$channel" = "$INSTALL_CHANNEL" ] && [ -z "$repo_path" ] || return 1
  canonicalize_version "$version" >/dev/null || return 1
  [[ "$CANDIDATE_COMMIT" =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ "$CANDIDATE_ARTIFACT_SHA256" =~ ^[0-9a-f]{64}$ ]] \
    && [[ "$CANDIDATE_CHECKSUMS_SHA256" =~ ^[0-9a-f]{64}$ ]] || return 1
  release_safe_path "$STATE_FILE" && recovery_admin_path "$CONFIG_DIR" || return 1
  if [ -n "$INSTALL_STATE_SHA256" ]; then
    recovery_admin_path "$STATE_FILE" && release_validate_state \
      && [ "$(compute_sha256 "$STATE_FILE")" = "$INSTALL_STATE_SHA256" ] || return 1
    method="$(release_state_value CONTABASE_INSTALL_METHOD)"
  else
    [ ! -e "$STATE_FILE" ] && [ ! -L "$STATE_FILE" ] || return 1
  fi
  tmpfile="$(mktemp "$CONFIG_DIR/.install-state.XXXXXX")" || return 1
  trap 'rm -f "$tmpfile"' EXIT
  if [ -n "$INSTALL_STATE_SHA256" ]; then cp "$STATE_FILE" "$tmpfile" || return 1
  else
    printf 'CONTABASE_STATE_VERSION=1\nCONTABASE_INSTALL_METHOD=%s\nCONTABASE_CHANNEL=%s\nCONTABASE_INSTALLED_VERSION=%s\nCONTABASE_REPO_PATH=\n' \
      "$method" "$channel" "$PREVIOUS_VERSION" > "$tmpfile" || return 1
  fi
  # Keep the original identity once, and track the newly installed payload.
  local key original value output="$tmpfile.next"
  trap 'rm -f "$tmpfile" "$output"' EXIT
  for key in CONTABASE_INSTALLED_VERSION CONTABASE_COMMIT CONTABASE_ARTIFACT_NAME \
             CONTABASE_ARTIFACT_PLATFORM CONTABASE_ARTIFACT_SHA256 CONTABASE_CHECKSUMS_SHA256; do
    case "$key" in
      CONTABASE_INSTALLED_VERSION) value="$version" ;;
      CONTABASE_COMMIT) value="$CANDIDATE_COMMIT" ;;
      CONTABASE_ARTIFACT_NAME) value="$ARTIFACT_NAME" ;;
      CONTABASE_ARTIFACT_PLATFORM) value="linux/$ARCH" ;;
      CONTABASE_ARTIFACT_SHA256) value="$CANDIDATE_ARTIFACT_SHA256" ;;
      CONTABASE_CHECKSUMS_SHA256) value="$CANDIDATE_CHECKSUMS_SHA256" ;;
    esac
    [ -n "$value" ] && [[ "$value" =~ ^[A-Za-z0-9_./-]+$ ]] || return 1
    original="CONTABASE_ORIGIN_${key#CONTABASE_}"
    awk -F= -v k="$key" -v v="$value" -v origin="$original" '
      $1==origin { found_origin=1 }
      $1==k { old=substr($0,index($0,"=")+1); found=1; print k "=" v; next }
      { print }
      END { if(!found) print k "=" v; if(found && !found_origin) print origin "=" old }
    ' "$tmpfile" > "$output" || return 1
    mv -f "$output" "$tmpfile" || return 1
  done
  chmod 0600 "$tmpfile" && chown root:root "$tmpfile" || return 1
  STATE_FILE="$tmpfile" release_validate_state || return 1
  mv -f "$tmpfile" "$STATE_FILE" || return 1
  [ "$(release_state_value CONTABASE_INSTALLED_VERSION)" = "$TAG" ] \
    && [ "$(release_state_value CONTABASE_COMMIT)" = "$CANDIDATE_COMMIT" ]
)

# === RELEASE_ISOLATED_BOOTSTRAP_BEGIN ===
# A beta.7 baixa apenas este script, sem scripts/lib. Nesse modo isolado,
# autentica o dispatcher da versao alvo pelo canal e entrega o controle a ele.
# O dispatcher baixa o bundle completo do commit autorizado e revalida tudo.
bootstrap_channel_for_tag() {
  case "$1" in
    v*-beta.*) printf '%s' beta ;;
    v[0-9]*.[0-9]*.[0-9]*) printf '%s' stable ;;
    *) return 1 ;;
  esac
}

bootstrap_json_string_field() {
  local key="$1" file="$2"
  tr '\n' ' ' < "$file" | sed -nE 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p'
}

bootstrap_json_number_field() {
  local key="$1" file="$2"
  tr '\n' ' ' < "$file" | sed -nE 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p'
}

bootstrap_download() {
  local url="$1" out="$2" limit="$3" want_json="${4:-0}"
  local result status content_type effective size
  result="$(curl --fail --silent --show-error --proto '=https' --tlsv1.2 \
    --max-redirs 0 --max-time 120 --max-filesize "$limit" \
    -A 'ContaBase-Update/1' -o "$out" \
    -w '%{http_code}\n%{content_type}\n%{url_effective}' "$url")" \
    || { rm -f "$out"; fail "bootstrap: falha de rede ao baixar ${url}"; }
  status=""
  content_type=""
  effective=""
  {
    IFS= read -r status || true
    IFS= read -r content_type || true
    IFS= read -r effective || true
  } <<< "$result"
  [ "$status" = "200" ] || { rm -f "$out"; fail "bootstrap: HTTP ${status:-desconhecida} em ${url}"; }
  [ "$effective" = "$url" ] || { rm -f "$out"; fail "bootstrap: redirecionamento nao autorizado em ${url}"; }
  [ -f "$out" ] && [ ! -L "$out" ] || { rm -f "$out"; fail "bootstrap: download ausente ou inseguro: ${url}"; }
  if [ "$want_json" = "1" ]; then
    case "$content_type" in
      application/json*) ;;
      *) rm -f "$out"; fail "bootstrap: content-type nao JSON em ${url}: ${content_type:-ausente}";;
    esac
  fi
  size="$(wc -c < "$out" | tr -d '[:space:]')"
  [ -n "$size" ] && [ "$size" -gt 0 ] && [ "$size" -le "$limit" ] \
    || { rm -f "$out"; fail "bootstrap: tamanho invalido em ${url}"; }
}

bootstrap_standalone_release_wrapper() {
  # Bundle autenticado ou checkout completo seguem pelo caminho ja existente.
  [ -z "${BUNDLE_COMMIT}" ] || return 0
  [ -z "${UPDATE_WRAPPER_PATH}" ] || return 0
  [ -z "${UPDATE_WRAPPER_SHA256}" ] || return 0
  local local_wrapper="${SCRIPT_DIR}/lib/contabase-update-wrapper.sh"
  if [ -e "$local_wrapper" ] || [ -L "$local_wrapper" ]; then
    return 0
  fi

  local channel manifest_url manifest_file manifest_sha manifest_commit
  local updater_url updater_file wrapper_hash wrapper_url wrapper_file actual
  channel="$(bootstrap_channel_for_tag "$TAG")" \
    || fail "bootstrap: nao foi possivel inferir o canal de ${TAG}."
  BOOTSTRAP_TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/contabase-release-bootstrap.XXXXXX")" \
    || fail "bootstrap: nao foi possivel criar diretorio temporario."
  chmod 0700 "$BOOTSTRAP_TMP_DIR"

  say "Bootstrap isolado detectado; autenticando o updater ${TAG} pelo canal ${channel}..."
  manifest_file="${BOOTSTRAP_TMP_DIR}/channel.json"
  manifest_url="${BOOTSTRAP_CHANNEL_BASE%/}/channels/${channel}.json"
  bootstrap_download "$manifest_url" "$manifest_file" 65536 1
  [ "$(bootstrap_json_number_field schema_version "$manifest_file")" = "1" ] \
    || fail "bootstrap: manifest do canal ${channel} tem schema_version invalido."
  [ "$(bootstrap_json_string_field channel "$manifest_file")" = "$channel" ] \
    || fail "bootstrap: manifest do canal ${channel} declara canal divergente."
  [ "$(bootstrap_json_string_field version "$manifest_file")" = "$TAG" ] \
    || fail "bootstrap: canal ${channel} nao aponta para ${TAG}."
  manifest_commit="$(bootstrap_json_string_field commit "$manifest_file")"
  manifest_sha="$(bootstrap_json_string_field updater_manifest_sha256 "$manifest_file")"
  [[ "$manifest_commit" =~ ^[0-9a-f]{40}$ ]] \
    || fail "bootstrap: commit do canal ${channel} invalido."
  [[ "$manifest_sha" =~ ^[0-9a-f]{64}$ ]] \
    || fail "bootstrap: updater_manifest_sha256 invalido no canal ${channel}."

  updater_file="${BOOTSTRAP_TMP_DIR}/updater.sha256"
  updater_url="${BOOTSTRAP_CHANNEL_BASE%/}/updaters/${TAG}/updater.sha256"
  bootstrap_download "$updater_url" "$updater_file" 65536
  actual="$(compute_sha256 "$updater_file" | tr '[:upper:]' '[:lower:]')"
  [ "$actual" = "$manifest_sha" ] \
    || fail "bootstrap: updater.sha256 diverge do canal ${channel}."
  [ "$(sed -n '1p' "$updater_file")" = "commit=${manifest_commit}" ] \
    || fail "bootstrap: commit do updater.sha256 diverge do canal ${channel}."

  wrapper_hash="$(awk -v p="scripts/lib/contabase-update-wrapper.sh" '
    NF >= 2 { f = $2; sub(/^\*/, "", f); if (f == p) print $1 }
  ' "$updater_file")"
  [ "$(printf '%s\n' "$wrapper_hash" | wc -l | tr -d '[:space:]')" = "1" ] \
    || fail "bootstrap: wrapper ausente ou duplicado no updater.sha256."
  [[ "$wrapper_hash" =~ ^[0-9a-f]{64}$ ]] \
    || fail "bootstrap: hash do wrapper invalido no updater.sha256."

  wrapper_file="${BOOTSTRAP_TMP_DIR}/contabase-update-wrapper.sh"
  wrapper_url="${BOOTSTRAP_RAW_BASE%/}/${manifest_commit}/scripts/lib/contabase-update-wrapper.sh"
  bootstrap_download "$wrapper_url" "$wrapper_file" 1048576
  actual="$(compute_sha256 "$wrapper_file" | tr '[:upper:]' '[:lower:]')"
  [ "$actual" = "$wrapper_hash" ] \
    || fail "bootstrap: wrapper diverge do updater.sha256."
  chmod 0700 "$wrapper_file"

  say "Updater autenticado pelo canal ${channel}; executando o dispatcher ${TAG}..."
  local rc=0
  "$wrapper_file" "$TAG" "$@" || rc=$?
  rm -rf "$BOOTSTRAP_TMP_DIR"
  BOOTSTRAP_TMP_DIR=""
  exit "$rc"
}
# === RELEASE_ISOLATED_BOOTSTRAP_END ===

RELEASE_WRAPPER_SOURCE=""

validate_release_update_wrapper() {
  local wrapper_source="${SCRIPT_DIR}/lib/contabase-update-wrapper.sh"
  if [ -n "$UPDATE_WRAPPER_PATH" ] || [ -n "$UPDATE_WRAPPER_SHA256" ]; then
    [ -n "$UPDATE_WRAPPER_PATH" ] && [ -n "$UPDATE_WRAPPER_SHA256" ] \
      || fail "wrapper verificado incompleto (caminho/hash)."
    bundle_path_unsafe "$UPDATE_WRAPPER_PATH" \
      && fail "caminho do wrapper verificado com symlink ou componente inseguro."
    [ -f "$UPDATE_WRAPPER_PATH" ] && [ ! -L "$UPDATE_WRAPPER_PATH" ] \
      || fail "wrapper verificado ausente ou inseguro."
    [ "$(bundle_sha256 "$UPDATE_WRAPPER_PATH")" = "$UPDATE_WRAPPER_SHA256" ] \
      || fail "wrapper verificado foi alterado antes da instalacao."
    RELEASE_WRAPPER_SOURCE="$UPDATE_WRAPPER_PATH"
    return 0
  fi
  [ -n "$BUNDLE_COMMIT" ] \
    && fail "wrapper verificado ausente no caminho autenticado."
  [ -f "$wrapper_source" ] && [ ! -L "$wrapper_source" ] \
    || fail "bundle Release sem wrapper autenticado; nenhuma alteracao foi executada."
  RELEASE_WRAPPER_SOURCE="$wrapper_source"
}

backfill_update_command() {
  local update_wrapper="/usr/local/bin/contabase-update"
  local mode_file="${CONFIG_DIR}/install-mode"

  validate_release_update_wrapper
  mkdir -p "$(dirname "$mode_file")"
  printf '%s\n' "release" > "$mode_file"
  chown root:root "$mode_file" 2>/dev/null || true
  chmod 0644 "$mode_file"

  if [ -n "$UPDATE_WRAPPER_PATH" ]; then
    [ "$(bundle_sha256 "$UPDATE_WRAPPER_PATH")" = "$UPDATE_WRAPPER_SHA256" ] \
      || fail "wrapper verificado foi alterado antes da instalacao."
  fi
  install -o root -g root -m 0755 "$RELEASE_WRAPPER_SOURCE" "$update_wrapper"

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
  validate_release_installation_contract
  say ""
  say "====================================================================="
  say "Iniciando atualizacao para ${TAG}..."
  say "====================================================================="

  # Stop service
  stop_service
  validate_release_installation_contract

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

  if ! verify_release_runtime; then
    fail_after_new_runtime "processo ativo, unit ou diretorios divergem do candidato."
  fi

  NEW_BUNDLE_ACTIVE=false
  record_release_recovery

  # Update VERSION in env file to track installed release tag
  env_set_key "VERSION" "$TAG" "$ENV_FILE"
  env_set_key "CONTABASE_CHANNEL" "$INSTALL_CHANNEL" "$ENV_FILE"
  env_set_key "CONTABASE_INSTALLED_VERSION" "$TAG" "$ENV_FILE"

  # Install/refresh global update command (backfill for existing installs)
  backfill_update_command

  if ! write_install_state "release" "$INSTALL_CHANNEL" "$TAG" ""; then
    fail_after_new_runtime "nao foi possivel persistir o estado da atualizacao."
  fi

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
    resolve_release_installation
    validate_existing_env
    validate_numeric_settings
    validate_install_channel
    validate_existing_installation
    validate_release_installation_contract
    validate_persistent_state
    resolve_current_version
    enforce_version_policy
  fi

  if [ "$VALIDATE_ONLY" != true ]; then
    bootstrap_standalone_release_wrapper "$@"
  fi

  build_urls
  download_and_validate_bundle

  # B6: no modo autenticado o commit da tag e validado tambem em
  # --validate-only, antes de qualquer retorno de sucesso.
  if [ -n "$BUNDLE_COMMIT" ]; then
    resolve_candidate_identity
    [ "$CANDIDATE_COMMIT" = "$BUNDLE_COMMIT" ] \
      || fail "commit da tag diverge do bundle autorizado."
  fi

  if [ "$VALIDATE_ONLY" = true ]; then
    say ""
    say "Validacao concluida; nenhuma instalacao foi alterada."
    return 0
  fi

  validate_release_update_wrapper

  if [ -z "$BUNDLE_COMMIT" ]; then
    resolve_candidate_identity
  fi

  # Confirm for real update only (dry-run skips interactive prompt)
  if [ "$DRY_RUN" = false ]; then
    confirm_update
  fi

  perform_update
}

main "$@"

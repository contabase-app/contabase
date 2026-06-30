#!/usr/bin/env bash
set -euo pipefail

STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"
LEGACY_MODE_FILE="${CONTABASE_MODE_FILE:-/etc/contabase/install-mode}"
ENV_FILE="${CONTABASE_ENV_FILE:-/etc/contabase/contabase.env}"
PUBLIC_RAW_BASE="${CONTABASE_RAW_BASE:-https://raw.githubusercontent.com/contabase-app/contabase}"
CHANNEL_BASE="${CONTABASE_CHANNEL_BASE:-https://get-contabase.pages.dev}"

say() { printf '%s\n' "$*"; }

fail() {
  say "Erro: $*" >&2
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
  --channel beta|stable  Troca explicitamente o canal de update.
  --dry-run             Encaminha dry-run quando suportado pelo metodo.
  --yes, -y             Modo nao interativo quando suportado.
  --help, -h            Mostra esta ajuda.
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

state_value() {
  local key="$1"
  local file="$2"
  [ -f "$file" ] || { printf ''; return 0; }
  awk -F= -v k="$key" '$1 == k { print substr($0, index($0, "=") + 1); exit }' "$file" | tr -d "[:space:]'\""
}

load_state() {
  SAVED_METHOD=""
  SAVED_CHANNEL=""
  SAVED_VERSION=""
  SAVED_REPO_PATH=""

  if [ -f "$STATE_FILE" ]; then
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
  fi

  [ -n "$SAVED_METHOD" ] || fail "nao foi possivel detectar o modo de instalacao. Arquivo ausente: $STATE_FILE"
  case "$SAVED_METHOD" in
    release|binary|docker|source) ;;
    *) fail "modo de instalacao desconhecido: $SAVED_METHOD" ;;
  esac

  [ -n "$SAVED_CHANNEL" ] || SAVED_CHANNEL="pinned"
  case "$SAVED_CHANNEL" in
    stable|beta|pinned) ;;
    *) fail "canal salvo invalido: $SAVED_CHANNEL" ;;
  esac
}

json_string_value() {
  local key="$1"
  local file="$2"
  tr '\n' ' ' < "$file" | sed -nE 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p'
}

json_null_value() {
  local key="$1"
  local file="$2"
  tr '\n' ' ' < "$file" | grep -Eq '"'$key'"[[:space:]]*:[[:space:]]*null'
}

resolve_channel_version() {
  local channel="$1"
  local manifest_url manifest_file schema manifest_channel version

  case "$channel" in
    stable|beta) ;;
    *) fail "canal invalido: $channel" ;;
  esac

  command -v curl >/dev/null 2>&1 || fail "curl e obrigatorio para resolver o canal $channel."
  command -v mktemp >/dev/null 2>&1 || fail "mktemp e obrigatorio para resolver o canal $channel."

  manifest_url="${CHANNEL_BASE%/}/channels/${channel}.json"
  manifest_file="$(mktemp "${TMPDIR:-/tmp}/contabase-channel.XXXXXX")"
  local curl_args=(--fail --location --silent --show-error)
  if [ "${CONTABASE_ALLOW_INSECURE_CHANNEL:-0}" != "1" ]; then
    curl_args+=(--proto '=https' --tlsv1.2)
  fi
  if ! curl "${curl_args[@]}" "$manifest_url" -o "$manifest_file"; then
    rm -f "$manifest_file"
    fail "nao foi possivel baixar o manifest do canal ${channel}: ${manifest_url}"
  fi

  schema="$(json_string_value schema_version "$manifest_file")"
  [ -n "$schema" ] || schema="$(tr '\n' ' ' < "$manifest_file" | sed -nE 's/.*"schema_version"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p')"
  manifest_channel="$(json_string_value channel "$manifest_file")"
  version="$(json_string_value version "$manifest_file")"

  [ "$schema" = "1" ] || { rm -f "$manifest_file"; fail "manifest do canal ${channel} tem schema_version invalido."; }
  [ "$manifest_channel" = "$channel" ] || { rm -f "$manifest_file"; fail "manifest do canal ${channel} declara canal '${manifest_channel}'."; }

  if [ -z "$version" ] && json_null_value version "$manifest_file"; then
    rm -f "$manifest_file"
    fail "canal ${channel} ainda nao possui versao publicada."
  fi
  rm -f "$manifest_file"

  validate_public_version "$version"
  case "$channel" in
    beta)
      case "$version" in
        v*-beta.*) ;;
        *) fail "manifest beta apontou para versao nao beta: $version" ;;
      esac
      ;;
    stable)
      case "$version" in
        *-*) fail "manifest stable apontou para pre-release: $version" ;;
      esac
      ;;
  esac
  printf '%s' "$version"
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
        PASSTHROUGH_ARGS+=("$1")
        shift
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

resolve_target() {
  if [ -n "$TARGET_VERSION" ]; then
    validate_public_version "$TARGET_VERSION"
    TARGET_CHANNEL="pinned"
    return
  fi

  if [ -n "$TARGET_CHANNEL" ]; then
    TARGET_VERSION="$(resolve_channel_version "$TARGET_CHANNEL")"
    return
  fi

  case "$SAVED_CHANNEL" in
    stable|beta)
      TARGET_CHANNEL="$SAVED_CHANNEL"
      TARGET_VERSION="$(resolve_channel_version "$TARGET_CHANNEL")"
      ;;
    pinned)
      fail "instalacao pinned/manual. Informe uma versao: cb-update vX.Y.Z, ou troque explicitamente de canal: cb-update --channel beta|stable."
      ;;
  esac
}

run_release_update() {
  local tmp_dir update_script update_url
  [ "$(id -u)" -eq 0 ] || fail "este modo exige root. Execute com sudo."

  tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/contabase-update.XXXXXX")"
  trap 'rm -rf "$tmp_dir"' EXIT
  update_script="${tmp_dir}/update-contabase-release.sh"
  update_url="${PUBLIC_RAW_BASE}/${TARGET_VERSION}/scripts/update-contabase-release.sh"

  say "Baixando updater da versao ${TARGET_VERSION}..."
  if ! curl --fail --location --silent --show-error \
    --proto '=https' --tlsv1.2 \
    "$update_url" -o "$update_script"; then
    fail "nao foi possivel baixar o updater da versao ${TARGET_VERSION}."
  fi

  say "Executando update release para ${TARGET_VERSION} (canal ${TARGET_CHANNEL})..."
  exec env \
    CONTABASE_INSTALL_METHOD=release \
    CONTABASE_CHANNEL="$TARGET_CHANNEL" \
    CONTABASE_VERSION="$TARGET_VERSION" \
    CONTABASE_ASSUME_YES="${CONTABASE_ASSUME_YES:-0}" \
    bash "$update_script" "${PASSTHROUGH_ARGS[@]}"
}

run_repo_update() {
  local script="$1"
  local needs_root="${2:-0}"

  [ -n "$SAVED_REPO_PATH" ] && [ -d "$SAVED_REPO_PATH" ] || fail "repositorio da instalacao nao encontrado: $SAVED_REPO_PATH"
  if [ "$needs_root" = "1" ] && [ "$(id -u)" -ne 0 ]; then
    fail "este modo exige root. Execute com sudo."
  fi

  cd "$SAVED_REPO_PATH"
  say "Executando update ${SAVED_METHOD} para ${TARGET_VERSION} (canal ${TARGET_CHANNEL})..."
  exec env \
    CONTABASE_INSTALL_METHOD="$SAVED_METHOD" \
    CONTABASE_CHANNEL="$TARGET_CHANNEL" \
    CONTABASE_VERSION="$TARGET_VERSION" \
    CONTABASE_ASSUME_YES="${CONTABASE_ASSUME_YES:-0}" \
    "$script" "${PASSTHROUGH_ARGS[@]}"
}

main() {
  parse_args "$@"
  load_state
  resolve_target

  if [ "${CONTABASE_UPDATE_RESOLVE_ONLY:-0}" = "1" ]; then
    say "Update resolvido: metodo=${SAVED_METHOD} canal=${TARGET_CHANNEL} versao=${TARGET_VERSION}"
    exit 0
  fi

  case "$SAVED_METHOD" in
    release|binary)
      run_release_update
      ;;
    docker)
      run_repo_update ./scripts/update-contabase-docker.sh 0
      ;;
    source)
      run_repo_update ./scripts/update-contabase-source.sh 1
      ;;
  esac
}

main "$@"

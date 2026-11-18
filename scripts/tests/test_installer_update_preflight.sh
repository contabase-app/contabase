#!/usr/bin/env bash
# Focused checks: installer flock preflight, authenticated updater fallback
# removal and atomic dispatcher/state publication with compensation.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
SOURCE="${ROOT_DIR}/scripts/install-contabase-source.sh"
DOCKER="${ROOT_DIR}/scripts/install-contabase-docker.sh"
RELEASE="${ROOT_DIR}/scripts/install-contabase-release.sh"
UPDATE_SOURCE="${ROOT_DIR}/scripts/update-contabase-source.sh"
UPDATE_DOCKER="${ROOT_DIR}/scripts/update-contabase-docker.sh"
UPDATE_RELEASE="${ROOT_DIR}/scripts/update-contabase-release.sh"
TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-installer-preflight.XXXXXX")" || exit 1
TMP_BASE="$(cd "$TMP_BASE" && pwd -P)"
PASS=0
FAIL=0

cleanup() { rm -rf "$TMP_BASE"; }
trap cleanup EXIT

ok() { PASS=$((PASS + 1)); printf 'ok - %s\n' "$1"; }
not_ok() { FAIL=$((FAIL + 1)); printf 'not ok - %s\n' "$1"; }

extract_function() {
  awk -v name="$1" '$0 ~ "^" name "\\(\\) \\{" { f=1 } f { print } f && /^}$/ { exit }' "$2"
}

make_tool_path() {
  local directory="$1" with_flock="$2"
  shift 2
  mkdir -p "$directory"
  local command
  for command in "$@"; do
    printf '#!/bin/sh\nexit 0\n' > "$directory/$command"
    chmod +x "$directory/$command"
  done
  if [ "$with_flock" = true ]; then
    printf '#!/bin/sh\nexit 0\n' > "$directory/flock"
    chmod +x "$directory/flock"
  fi
}

run_require_flock() {
  local script="$1" path="$2" function_text
  function_text="$(extract_function require_flock "$script")"
  (
    PATH="$path"
    log_error() { :; }
    fail() { exit 1; }
    say() { :; }
    eval "$function_text"
    require_flock
  ) >/dev/null 2>&1
}

run_source_commands() {
  local path="$1" validate="$2" flock_text commands_text
  flock_text="$(extract_function require_flock "$SOURCE")"
  commands_text="$(extract_function require_commands "$SOURCE")"
  (
    PATH="$path"
    VALIDATE_ONLY="$validate"
    log_error() { :; }
    eval "$flock_text"
    eval "$commands_text"
    require_commands
  ) >/dev/null 2>&1
}

run_release_commands() {
  local path="$1" validate="$2" flock_text commands_text
  flock_text="$(extract_function require_flock "$RELEASE")"
  commands_text="$(extract_function require_commands "$RELEASE")"
  (
    PATH="$path"
    VALIDATE_ONLY="$validate"
    fail() { exit 1; }
    eval "$flock_text"
    eval "$commands_text"
    require_commands
  ) >/dev/null 2>&1
}

install_stub() {
  local source="" destination=""
  while [ "$#" -gt 0 ]; do
    case "$1" in
      -o|-g|-m) shift 2 ;;
      *) source="$1"; destination="$2"; break ;;
    esac
  done
  [ -n "$source" ] && [ -n "$destination" ] || return 1
  cp "$source" "$destination"
  chmod 0755 "$destination"
}

# 1-8. flock preflight: missing rejects, present accepts.
NO_FLOCK="${TMP_BASE}/no-flock"
WITH_FLOCK="${TMP_BASE}/with-flock"
SOURCE_COMMANDS="git go node npm curl systemctl sqlite3 openssl tar install"
RELEASE_COMMANDS="curl tar install openssl awk grep find mktemp cp wc tr sha256sum"
make_tool_path "$NO_FLOCK" false $SOURCE_COMMANDS $RELEASE_COMMANDS
make_tool_path "$WITH_FLOCK" true $SOURCE_COMMANDS $RELEASE_COMMANDS

run_require_flock "$SOURCE" "$NO_FLOCK" && not_ok "Source aceitou ausencia de flock" || ok "Source recusa ausencia de flock"
run_require_flock "$SOURCE" "$WITH_FLOCK" && ok "Source aceita flock presente" || not_ok "Source recusou flock presente"
run_require_flock "$DOCKER" "$NO_FLOCK" && not_ok "Docker aceitou ausencia de flock" || ok "Docker recusa ausencia de flock"
run_require_flock "$DOCKER" "$WITH_FLOCK" && ok "Docker aceita flock presente" || not_ok "Docker recusou flock presente"
run_require_flock "$RELEASE" "$NO_FLOCK" && not_ok "Release aceitou ausencia de flock" || ok "Release recusa ausencia de flock"
run_require_flock "$RELEASE" "$WITH_FLOCK" && ok "Release aceita flock presente" || not_ok "Release recusou flock presente"
run_source_commands "$NO_FLOCK" false && not_ok "Source require_commands aceitou ausencia de flock" || ok "Source require_commands bloqueia sem flock"
run_source_commands "$WITH_FLOCK" false && ok "Source require_commands aceita flock" || not_ok "Source require_commands recusou flock"
run_release_commands "$NO_FLOCK" false && not_ok "Release require_commands aceitou ausencia de flock" || ok "Release require_commands bloqueia sem flock"
run_release_commands "$WITH_FLOCK" false && ok "Release require_commands aceita flock" || not_ok "Release require_commands recusou flock"
run_release_commands "$NO_FLOCK" true && ok "Release validate-only nao exige flock" || not_ok "Release validate-only passou a exigir flock"

# 9. Checkout incompleto do Source.
INCOMPLETE="${TMP_BASE}/incomplete"
mkdir -p "$INCOMPLETE"
run_source_repo() {
  (
    REPO_ROOT="$INCOMPLETE"
    log_error() { :; }
    eval "$(extract_function require_repo_root "$SOURCE")"
    require_repo_root
  ) >/dev/null 2>&1
}
run_source_repo && not_ok "checkout Source incompleto aceito" || ok "checkout Source incompleto bloqueado"

# 10. Checkout Docker incompleto: falha antes de alterar o diretorio.
DOCKER_EMPTY="${TMP_BASE}/docker-empty"
mkdir -p "$DOCKER_EMPTY"
run_docker_incomplete() {
  ( cd "$DOCKER_EMPTY" && PATH="${WITH_FLOCK}:${PATH}" bash "$DOCKER" --check ) >/dev/null 2>&1
}
run_docker_incomplete && not_ok "checkout Docker incompleto aceito" || ok "checkout Docker incompleto bloqueado"
[ -z "$(find "$DOCKER_EMPTY" -mindepth 1 -print -quit)" ] && ok "checkout Docker incompleto nao foi alterado" || not_ok "checkout Docker incompleto sofreu alteracao"

# 11-18. Wrapper autenticado ausente e caminho legitimo.
WORK="${TMP_BASE}/work"
mkdir -p "$WORK/repo-missing" "$WORK/repo-ok/scripts/lib" "$WORK/config"
printf '#!/bin/sh\necho authenticated-dispatcher\n' > "$WORK/repo-ok/scripts/lib/contabase-update-wrapper.sh"
chmod 0755 "$WORK/repo-ok/scripts/lib/contabase-update-wrapper.sh"

source_install_function() {
  extract_function install_update_command "$SOURCE" \
    | sed "s#/usr/local/bin/contabase-update#$WORK/contabase-update#g; s#/usr/local/bin/cb-update#$WORK/cb-update#g"
}

run_source_install_missing() {
  (
    cd "$WORK"
    REPO_ROOT="$WORK/repo-missing"
    CONFIG_DIR="$WORK/config"
    CONTABASE_MODE_FILE="$WORK/install-mode"
    APP_VERSION=v1.0.0-beta.8
    BOOTSTRAP_MODE=false
    PROVENANCE_KIND=tag
    PROVENANCE_REF=v1.0.0-beta.8
    PROVENANCE_COMMIT="$(printf 'a%.0s' {1..40})"
    PROVENANCE_ORIGIN=https://example.test/repo
    log_error() { printf 'guard\n' >> "$WORK/guard.log"; }
    infer_install_channel() { printf beta; }
    write_install_state() { printf 'state\n' >> "$WORK/state.log"; }
    install() { install_stub "$@"; }
    chown() { :; }
    eval "$(source_install_function)"
    install_update_command
  ) >/dev/null 2>&1
}

run_source_install_ok() {
  (
    cd "$WORK"
    REPO_ROOT="$WORK/repo-ok"
    CONFIG_DIR="$WORK/config"
    CONTABASE_MODE_FILE="$WORK/install-mode"
    APP_VERSION=v1.0.0-beta.8
    BOOTSTRAP_MODE=false
    PROVENANCE_KIND=tag
    PROVENANCE_REF=v1.0.0-beta.8
    PROVENANCE_COMMIT="$(printf 'a%.0s' {1..40})"
    PROVENANCE_ORIGIN=https://example.test/repo
    GREEN="" RED="" YELLOW="" BLUE="" NC=""
    log_error() { :; }
    infer_install_channel() { printf beta; }
    write_install_state() { :; }
    install() { install_stub "$@"; }
    chown() { :; }
    eval "$(source_install_function)"
    install_update_command
  ) >/dev/null 2>&1
}

printf 'sentinel\n' > "$WORK/contabase-update"
run_source_install_missing && not_ok "Source instalou update sem wrapper" || ok "Source falha fechado sem wrapper"
[ "$(cat "$WORK/contabase-update")" = "sentinel" ] && ok "Source preservou comando existente" || not_ok "Source substituiu comando existente"
[ -f "$WORK/guard.log" ] && ok "Source registrou erro de wrapper ausente" || not_ok "Source sem erro de wrapper ausente"
[ ! -e "$WORK/install-mode" ] && ok "Source nao alterou modo sem wrapper" || not_ok "Source alterou modo sem wrapper"
rm -f "$WORK/contabase-update" "$WORK/cb-update" "$WORK/install-mode" "$WORK/guard.log"
run_source_install_ok && ok "Source instala wrapper autenticado" || not_ok "Source nao instalou wrapper autenticado"
cmp -s "$WORK/repo-ok/scripts/lib/contabase-update-wrapper.sh" "$WORK/contabase-update" && ok "Source copiou dispatcher autenticado" || not_ok "Source copiou conteudo divergente"
[ -L "$WORK/cb-update" ] && ok "Source criou alias do dispatcher" || not_ok "Source nao criou alias"

docker_install_function() {
  extract_function install_update_command "$DOCKER" \
    | sed "s#/usr/local/bin/contabase-update#$WORK/docker-update#g; s#/usr/local/bin/cb-update#$WORK/docker-cb-update#g; s#/etc/contabase/install-mode#$WORK/docker-install-mode#g"
}

run_docker_install_missing() {
  (
    cd "$WORK/repo-missing"
    STATE_FILE="$WORK/docker-state.env"
    CONTABASE_VERSION=v1.0.0-beta.8
    PROVENANCE_KIND=tag
    PROVENANCE_REF=v1.0.0-beta.8
    PROVENANCE_COMMIT="$(printf 'a%.0s' {1..40})"
    PROVENANCE_ORIGIN=https://example.test/repo
    as_root() { "$@"; }
    say() { :; }
    infer_install_channel() { printf beta; }
    write_install_state() { printf 'state\n' >> "$WORK/docker-state.log"; }
    install() { install_stub "$@"; }
    chown() { :; }
    eval "$(docker_install_function)"
    install_update_command
  ) >/dev/null 2>&1
}

run_docker_install_ok() {
  (
    cd "$WORK/repo-ok"
    STATE_FILE="$WORK/docker-state.env"
    CONTABASE_VERSION=v1.0.0-beta.8
    PROVENANCE_KIND=tag
    PROVENANCE_REF=v1.0.0-beta.8
    PROVENANCE_COMMIT="$(printf 'a%.0s' {1..40})"
    PROVENANCE_ORIGIN=https://example.test/repo
    as_root() { "$@"; }
    say() { :; }
    infer_install_channel() { printf beta; }
    write_install_state() { :; }
    install() { install_stub "$@"; }
    chown() { :; }
    eval "$(docker_install_function)"
    install_update_command
  ) >/dev/null 2>&1
}

printf 'docker-sentinel\n' > "$WORK/docker-update"
run_docker_install_missing && not_ok "Docker instalou update sem wrapper" || ok "Docker falha fechado sem wrapper"
[ "$(cat "$WORK/docker-update")" = "docker-sentinel" ] && ok "Docker preservou comando existente" || not_ok "Docker substituiu comando existente"
[ ! -e "$WORK/docker-install-mode" ] && ok "Docker nao alterou modo sem wrapper" || not_ok "Docker alterou modo sem wrapper"
rm -f "$WORK/docker-update" "$WORK/docker-cb-update" "$WORK/docker-install-mode"
run_docker_install_ok && ok "Docker instala wrapper autenticado" || not_ok "Docker nao instalou wrapper autenticado"
cmp -s "$WORK/repo-ok/scripts/lib/contabase-update-wrapper.sh" "$WORK/docker-update" && ok "Docker copiou dispatcher autenticado" || not_ok "Docker copiou conteudo divergente"

run_source_backfill() {
  local repo="$1"
  (
    CONTABASE_UPDATE_WRAPPER="$WORK/source-update"
    CONTABASE_UPDATE_ALIAS="$WORK/source-cb-update"
    CONTABASE_MODE_FILE="$WORK/source-install-mode"
    STATE_FILE="$WORK/source-state.env"
    REPO_ROOT="$repo"
    APP_VERSION=v1.0.0-beta.8
    CHECKOUT_KIND=tag
    TARGET_REF=v1.0.0-beta.8
    TARGET_OID="$(printf 'a%.0s' {1..40})"
    INSTALL_ORIGIN=https://example.test/repo
    UPDATER_BUNDLE_EXTERNAL=false
    GREEN="" RED="" YELLOW="" BLUE="" NC=""
    state_value() { printf ''; }
    infer_install_channel() { printf beta; }
    log_error() { printf 'guard\n' >> "$WORK/source-guard.log"; }
    write_install_state() { :; }
    install() { install_stub "$@"; }
    chown() { :; }
    eval "$(extract_function backfill_update_command "$UPDATE_SOURCE")"
    backfill_update_command
  ) >/dev/null 2>&1
}

printf 'source-update-sentinel\n' > "$WORK/source-update"
run_source_backfill "$WORK/repo-missing" && not_ok "Source updater instalou sem wrapper" || ok "Source updater falha fechado sem wrapper"
[ "$(cat "$WORK/source-update")" = "source-update-sentinel" ] && ok "Source updater preservou comando existente" || not_ok "Source updater substituiu comando existente"
[ ! -e "$WORK/source-install-mode" ] && ok "Source updater nao alterou modo sem wrapper" || not_ok "Source updater alterou modo sem wrapper"
rm -f "$WORK/source-update" "$WORK/source-cb-update" "$WORK/source-install-mode"
run_source_backfill "$WORK/repo-ok" && ok "Source updater instala wrapper autenticado" || not_ok "Source updater nao instalou wrapper"
cmp -s "$WORK/repo-ok/scripts/lib/contabase-update-wrapper.sh" "$WORK/source-update" && ok "Source updater copiou dispatcher" || not_ok "Source updater copiou conteudo divergente"

run_docker_backfill() {
  local repo="$1"
  (
    CONTABASE_UPDATE_WRAPPER="$WORK/docker-backfill-update"
    CONTABASE_UPDATE_ALIAS="$WORK/docker-backfill-cb-update"
    CONTABASE_MODE_FILE="$WORK/docker-backfill-install-mode"
    STATE_FILE="$WORK/docker-backfill-state.env"
    REPO_ROOT="$repo"
    APP_VERSION=v1.0.0-beta.8
    CHECKOUT_KIND=tag
    TARGET_REF=v1.0.0-beta.8
    TARGET_OID="$(printf 'a%.0s' {1..40})"
    INSTALL_ORIGIN=https://example.test/repo
    UPDATER_BUNDLE_EXTERNAL=false
    id() { printf '0\n'; }
    state_value() { printf ''; }
    infer_install_channel() { printf beta; }
    state_render() { printf 'state\n'; }
    install_pending_file() { cp "$1" "$2"; }
    install() { install_stub "$@"; }
    chown() { :; }
    eval "$(extract_function backfill_update_command "$UPDATE_DOCKER")"
    backfill_update_command
  ) >/dev/null 2>&1
}

printf 'docker-update-sentinel\n' > "$WORK/docker-backfill-update"
run_docker_backfill "$WORK/repo-missing" && not_ok "Docker updater instalou sem wrapper" || ok "Docker updater falha fechado sem wrapper"
[ "$(cat "$WORK/docker-backfill-update")" = "docker-update-sentinel" ] && ok "Docker updater preservou comando existente" || not_ok "Docker updater substituiu comando existente"
[ ! -e "$WORK/docker-backfill-install-mode" ] && ok "Docker updater nao alterou modo sem wrapper" || not_ok "Docker updater alterou modo sem wrapper"
rm -f "$WORK/docker-backfill-update" "$WORK/docker-backfill-cb-update" "$WORK/docker-backfill-install-mode"
run_docker_backfill "$WORK/repo-ok" && ok "Docker updater instala wrapper autenticado" || not_ok "Docker updater nao instalou wrapper"
cmp -s "$WORK/repo-ok/scripts/lib/contabase-update-wrapper.sh" "$WORK/docker-backfill-update" && ok "Docker updater copiou dispatcher" || not_ok "Docker updater copiou conteudo divergente"

# 19-27. Release: wrapper autenticado do artifact, sem fallback para SCRIPT_DIR.
sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

mkdir -p \
  "$WORK/release-missing/extract/contabase/lib" \
  "$WORK/release-ok/helper" \
  "$WORK/release-ok/lib" \
  "$WORK/release-ok/extract/contabase/lib" \
  "$WORK/release-checkout-only/helper/lib" \
  "$WORK/release-checkout-only/extract/contabase/lib" \
  "$WORK/release-bootstrap/lib" \
  "$WORK/release-config"

printf '#!/bin/sh\necho release-authenticated-dispatcher\n' > "$WORK/release-ok/extract/contabase/lib/contabase-update-wrapper.sh"
cp "$WORK/release-ok/extract/contabase/lib/contabase-update-wrapper.sh" "$WORK/release-ok/lib/contabase-update-wrapper.sh"
chmod 0755 "$WORK/release-ok/extract/contabase/lib/contabase-update-wrapper.sh"
printf '#!/bin/sh\necho checkout-only-dispatcher\n' > "$WORK/release-checkout-only/helper/lib/contabase-update-wrapper.sh"
chmod 0755 "$WORK/release-checkout-only/helper/lib/contabase-update-wrapper.sh"
printf '#!/bin/sh\necho bootstrap-dispatcher\n' > "$WORK/release-bootstrap/lib/contabase-update-wrapper.sh"
chmod 0755 "$WORK/release-bootstrap/lib/contabase-update-wrapper.sh"

release_install_function() {
  local name text=""
  for name in path_uid path_mode admin_dir_is_secure publish_atomic_tmp \
              atomic_publish_authenticated_file record_admin_previous \
              write_install_mode_atomic verify_published_install_mode \
              verify_published_dispatcher \
              restore_admin_file compensate_admin_state_failure \
              admin_state_recovery_banner abort_admin_state_publication \
              require_release_update_wrapper install_update_command; do
    text="$text"$'\n'"$(extract_function "$name" "$RELEASE")"
  done
  printf '%s\n' "$text" \
    | sed "s#/usr/local/bin/contabase-update#$WORK/release-update#g; s#/usr/local/bin/cb-update#$WORK/release-cb-update#g; s#/etc/contabase/install-mode#$WORK/release-install-mode#g"
}

release_wrapper_helpers() {
  compute_sha256() { sha_file "$1"; }
  bootstrap_verify_file_digest() { [ "$(sha_file "$1")" = "$2" ]; }
}

release_wrapper_path() {
  printf '%s' "$1/extract/contabase/lib/contabase-update-wrapper.sh"
}

run_release_install() {
  local root="$1"
  (
    release_wrapper_helpers
    SCRIPT_DIR="$root/helper"
    TAG=v1.0.0-beta.8
    INSTALL_CHANNEL=beta
    BOOTSTRAP_MODE=false
    BUNDLE_WRAPPER_PATH="$(release_wrapper_path "$root")"
    if [ -f "$BUNDLE_WRAPPER_PATH" ]; then
      BUNDLE_WRAPPER_SHA256="$(sha_file "$BUNDLE_WRAPPER_PATH")"
    else
      BUNDLE_WRAPPER_SHA256=""
    fi
    RELEASE_WRAPPER_SOURCE=""
    RELEASE_WRAPPER_SHA256=""
    write_install_state() { printf 'state\n' >> "$WORK/release-state.log"; }
    install() { install_stub "$@"; }
    chown() { :; }
    say() { :; }
    fail() { exit 1; }
    eval "$(release_install_function)"
    install_update_command
  ) >/dev/null 2>&1
}

run_release_tampered() {
  local root="$1" wrapper
  wrapper="$(release_wrapper_path "$root")"
  (
    release_wrapper_helpers
    SCRIPT_DIR="$root/helper"
    TAG=v1.0.0-beta.8
    INSTALL_CHANNEL=beta
    BOOTSTRAP_MODE=false
    BUNDLE_WRAPPER_PATH="$wrapper"
    BUNDLE_WRAPPER_SHA256="$(sha_file "$wrapper")"
    RELEASE_WRAPPER_SOURCE=""
    RELEASE_WRAPPER_SHA256=""
    write_install_state() { printf 'state\n' >> "$WORK/release-state.log"; }
    install() { install_stub "$@"; }
    chown() { :; }
    say() { :; }
    fail() { exit 1; }
    eval "$(release_install_function)"
    printf '\n# adulterado depois da validacao\n' >> "$wrapper"
    install_update_command
  ) >/dev/null 2>&1
}

run_release_bootstrap_pin() {
  local expected="$1"
  (
    release_wrapper_helpers
    SCRIPT_DIR="$WORK/release-bootstrap"
    TAG=v1.0.0-beta.8
    INSTALL_CHANNEL=beta
    BOOTSTRAP_MODE=true
    CONTABASE_UPDATE_WRAPPER_SHA256="$expected"
    BUNDLE_WRAPPER_PATH=""
    BUNDLE_WRAPPER_SHA256=""
    RELEASE_WRAPPER_SOURCE=""
    RELEASE_WRAPPER_SHA256=""
    say() { :; }
    fail() { exit 1; }
    eval "$(extract_function require_release_update_wrapper "$RELEASE")"
    require_release_update_wrapper
  ) >/dev/null 2>&1
}

run_release_bootstrap_artifact_mismatch() {
  local pinned_wrapper="$WORK/release-bootstrap/lib/contabase-update-wrapper.sh"
  local artifact_wrapper="$WORK/release-ok/extract/contabase/lib/contabase-update-wrapper.sh"
  (
    release_wrapper_helpers
    SCRIPT_DIR="$WORK/release-bootstrap"
    TAG=v1.0.0-beta.8
    INSTALL_CHANNEL=beta
    BOOTSTRAP_MODE=true
    CONTABASE_UPDATE_WRAPPER_SHA256="$(sha_file "$pinned_wrapper")"
    BUNDLE_WRAPPER_PATH="$artifact_wrapper"
    BUNDLE_WRAPPER_SHA256="$(sha_file "$artifact_wrapper")"
    RELEASE_WRAPPER_SOURCE=""
    RELEASE_WRAPPER_SHA256=""
    say() { :; }
    fail() { exit 1; }
    eval "$(extract_function require_release_update_wrapper "$RELEASE")"
    require_release_update_wrapper
  ) >/dev/null 2>&1
}

printf 'release-sentinel\n' > "$WORK/release-update"
run_release_install "$WORK/release-missing" && not_ok "Release instalou update sem wrapper no artifact" || ok "Release falha fechado sem wrapper no artifact"
[ "$(cat "$WORK/release-update")" = "release-sentinel" ] && ok "Release preservou comando existente" || not_ok "Release substituiu comando existente"
[ ! -e "$WORK/release-install-mode" ] && ok "Release nao alterou modo sem wrapper" || not_ok "Release alterou modo sem wrapper"
rm -f "$WORK/release-update" "$WORK/release-cb-update" "$WORK/release-install-mode"
run_release_install "$WORK/release-ok" && ok "Release instala wrapper autenticado do artifact" || not_ok "Release nao instalou wrapper do artifact"
cmp -s "$WORK/release-ok/extract/contabase/lib/contabase-update-wrapper.sh" "$WORK/release-update" && ok "Release copiou dispatcher do artifact" || not_ok "Release copiou conteudo divergente"
rm -f "$WORK/release-update" "$WORK/release-cb-update" "$WORK/release-install-mode"

printf 'release-sentinel\n' > "$WORK/release-update"
run_release_install "$WORK/release-checkout-only" && not_ok "Release aceitou wrapper apenas do checkout" || ok "Release recusa wrapper apenas do checkout"
[ "$(cat "$WORK/release-update" 2>/dev/null || true)" = "release-sentinel" ] && ok "Release checkout-only preservou comando existente" || not_ok "Release checkout-only substituiu comando existente"
rm -f "$WORK/release-update" "$WORK/release-cb-update" "$WORK/release-install-mode"

cp "$WORK/release-ok/extract/contabase/lib/contabase-update-wrapper.sh" "$WORK/release-tampered-wrapper.sh"
printf 'release-sentinel\n' > "$WORK/release-update"
run_release_tampered "$WORK/release-ok" && not_ok "Release aceitou wrapper adulterado apos validacao" || ok "Release recusa wrapper adulterado apos validacao"
[ "$(cat "$WORK/release-update" 2>/dev/null || true)" = "release-sentinel" ] && ok "Release adulterado preservou comando existente" || not_ok "Release adulterado substituiu comando existente"
cp "$WORK/release-tampered-wrapper.sh" "$WORK/release-ok/extract/contabase/lib/contabase-update-wrapper.sh"
rm -f "$WORK/release-tampered-wrapper.sh" "$WORK/release-update" "$WORK/release-cb-update" "$WORK/release-install-mode"

run_release_bootstrap_pin "$(printf 'a%.0s' {1..64})" && not_ok "Bootstrap aceitou pin invalido do wrapper" || ok "Bootstrap recusa pin invalido do wrapper"
run_release_bootstrap_pin "$(sha_file "$WORK/release-bootstrap/lib/contabase-update-wrapper.sh")" && ok "Bootstrap aceita pin valido do wrapper" || not_ok "Bootstrap recusou pin valido do wrapper"
run_release_bootstrap_artifact_mismatch && not_ok "Bootstrap aceitou artifact divergente do pin" || ok "Bootstrap recusa artifact divergente do pin"

release_backfill_function() {
  local text
  text="$(extract_function validate_release_update_wrapper "$UPDATE_RELEASE")"
  text="$text"$'\n'"$(extract_function backfill_update_command "$UPDATE_RELEASE")"
  printf '%s\n' "$text" \
    | sed "s#/usr/local/bin/contabase-update#$WORK/release-backfill-update#g; s#/usr/local/bin/cb-update#$WORK/release-backfill-cb-update#g"
}

run_release_backfill() {
  local script_dir="$1"
  (
    SCRIPT_DIR="$script_dir"
    CONFIG_DIR="$WORK/release-config"
    TAG=v1.0.0-beta.8
    UPDATE_WRAPPER_PATH=""
    UPDATE_WRAPPER_SHA256=""
    BUNDLE_COMMIT=""
    install() { install_stub "$@"; }
    chown() { :; }
    say() { :; }
    fail() { exit 1; }
    eval "$(release_backfill_function)"
    backfill_update_command
  ) >/dev/null 2>&1
}

printf 'release-backfill-sentinel\n' > "$WORK/release-backfill-update"
run_release_backfill "$WORK/release-missing" && not_ok "Release updater instalou sem wrapper" || ok "Release updater falha fechado sem wrapper"
[ "$(cat "$WORK/release-backfill-update")" = "release-backfill-sentinel" ] && ok "Release updater preservou comando existente" || not_ok "Release updater substituiu comando existente"
[ ! -e "$WORK/release-config/install-mode" ] && ok "Release updater nao alterou modo sem wrapper" || not_ok "Release updater alterou modo sem wrapper"
rm -f "$WORK/release-backfill-update" "$WORK/release-backfill-cb-update" "$WORK/release-config/install-mode"
run_release_backfill "$WORK/release-ok" && ok "Release updater instala wrapper autenticado" || not_ok "Release updater nao instalou wrapper"
cmp -s "$WORK/release-ok/lib/contabase-update-wrapper.sh" "$WORK/release-backfill-update" && ok "Release updater copiou dispatcher" || not_ok "Release updater copiou conteudo divergente"
release_wrapper_urls="$(grep -c 'wrapper_url' "$RELEASE" 2>/dev/null || true)"
[ "$release_wrapper_urls" = "0" ] && ok "Release installer sem download de wrapper por URL" || not_ok "Release installer manteve download de wrapper por URL"
bootstrap_block="$(extract_function bootstrap_standalone_release_wrapper "$UPDATE_RELEASE")"
total_wrapper_urls="$(grep -c 'wrapper_url' "$UPDATE_RELEASE" 2>/dev/null || true)"
bootstrap_wrapper_urls="$(printf '%s\n' "$bootstrap_block" | grep -c 'wrapper_url' 2>/dev/null || true)"
if [ -n "$bootstrap_block" ] && [ "$total_wrapper_urls" = "$bootstrap_wrapper_urls" ] && [ "$bootstrap_wrapper_urls" -gt 0 ]; then
  ok "Release wrapper_url restrito ao bootstrap autenticado"
else
  not_ok "Release wrapper_url fora do bootstrap autenticado"
fi
if printf '%s\n' "$bootstrap_block" | grep -q 'updater.sha256' \
  && printf '%s\n' "$bootstrap_block" | grep -q 'wrapper_hash='; then
  ok "Release bootstrap valida manifesto e hash do wrapper"
else
  not_ok "Release bootstrap nao valida manifesto/hash do wrapper"
fi
bootstrap_hash_line="$(printf '%s\n' "$bootstrap_block" | grep -n 'wrapper_hash=' | head -n1 | cut -d: -f1)"
bootstrap_download_line="$(printf '%s\n' "$bootstrap_block" | grep -nF 'bootstrap_download "$wrapper_url"' | head -n1 | cut -d: -f1)"
if [ -n "$bootstrap_hash_line" ] && [ -n "$bootstrap_download_line" ] && [ "$bootstrap_hash_line" -lt "$bootstrap_download_line" ]; then
  ok "Release bootstrap autentica hash antes do download do wrapper"
else
  not_ok "Release bootstrap baixa wrapper antes de autenticar hash"
fi
release_require_lines="$(grep -n '^  require_release_update_wrapper$' "$RELEASE" | cut -d: -f1)"
release_install_lines="$(grep -n '^  install_bundle$' "$RELEASE" | cut -d: -f1)"
release_require_first="$(printf '%s\n' "$release_require_lines" | sed -n '1p')"
release_require_second="$(printf '%s\n' "$release_require_lines" | sed -n '2p')"
release_install_first="$(printf '%s\n' "$release_install_lines" | sed -n '1p')"
release_install_second="$(printf '%s\n' "$release_install_lines" | sed -n '2p')"
if [ -n "$release_require_first" ] && [ -n "$release_require_second" ] \
  && [ -n "$release_install_first" ] && [ -n "$release_install_second" ] \
  && [ "$release_require_first" -lt "$release_install_first" ] \
  && [ "$release_require_second" -lt "$release_install_second" ]; then
  ok "Release valida wrapper antes de install_bundle"
else
  not_ok "Release valida wrapper depois de install_bundle"
fi

# 23b. Bootstrap autenticado executa a instalacao exatamente uma vez.
# O helper e copiado para fixture e apenas bootstrap_release_main e substituido:
# o reexec saneado, o marcador de argv e o atestado de ambiente continuam reais
# e cada entrada no adaptador e contada com dump do ambiente visto pelo main.
BOOTSTRAP_SINGLE="${WORK}/bootstrap-single"
BOOTSTRAP_SAFE_PATH_EXPECTED="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
mkdir -p "$BOOTSTRAP_SINGLE/lib"
awk -v marker="$BOOTSTRAP_SINGLE/executions.log" -v envdump="$BOOTSTRAP_SINGLE/main.env" '
  /^if \[ "\$BOOTSTRAP_MODE" = true \]; then$/ && !done {
    print "bootstrap_release_main() { echo main >> \"" marker "\"; env > \"" envdump "\"; }"
    done=1
  }
  { print }
' "$RELEASE" > "$BOOTSTRAP_SINGLE/install-contabase-release.sh"
cp "$ROOT_DIR/scripts/lib/contabase-bootstrap-adapter.sh" "$BOOTSTRAP_SINGLE/lib/"
bootstrap_single_reset() {
  rm -f "$BOOTSTRAP_SINGLE/executions.log" "$BOOTSTRAP_SINGLE/main.env"
}
bootstrap_single_executions() {
  wc -l < "$BOOTSTRAP_SINGLE/executions.log" 2>/dev/null || printf '0'
}
bootstrap_env_is_sanitized() {
  grep -qxF "PATH=$BOOTSTRAP_SAFE_PATH_EXPECTED" "$BOOTSTRAP_SINGLE/main.env" \
    && grep -Eq '^TMPDIR=.*/contabase-bootstrap-home\.[A-Za-z0-9]{6}$' "$BOOTSTRAP_SINGLE/main.env" \
    && [ "$(sed -n 's/^TMPDIR=//p' "$BOOTSTRAP_SINGLE/main.env")" = "$(sed -n 's/^HOME=//p' "$BOOTSTRAP_SINGLE/main.env")" ] \
    && grep -qxF "CONTABASE_BOOTSTRAP_SANITIZED=1" "$BOOTSTRAP_SINGLE/main.env" \
    && ! grep -q '^ATTACKER_VAR=' "$BOOTSTRAP_SINGLE/main.env"
}

# Fluxo canonico: entrada sem marcador; a origem saneada vem do reexec real.
bootstrap_single_reset
rc=0
(
  TMPDIR="$BOOTSTRAP_SINGLE"
  CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1
  export TMPDIR CONTABASE_BOOTSTRAP_CONTRACT_VERSION
  bash "$BOOTSTRAP_SINGLE/install-contabase-release.sh"
) > "$BOOTSTRAP_SINGLE/run.log" 2>&1 || rc=$?
[ "$rc" -eq 0 ] && ok "Bootstrap nao saneado reexecuta com sucesso" || not_ok "Bootstrap nao saneado falhou: $rc"
[ "$(bootstrap_single_executions)" -eq 1 ] \
  && ok "Bootstrap executa a instalacao exatamente uma vez" \
  || not_ok "Bootstrap executou a instalacao duplicada ou nao executou"
bootstrap_env_is_sanitized \
  && ok "Bootstrap legitimo roda o main em ambiente saneado" \
  || not_ok "Bootstrap legitimo rodou o main fora do ambiente saneado"

# Ataque por variavel externa: CONTABASE_BOOTSTRAP_SANITIZED=1 herdado com
# PATH hostil e variavel estranha NAO desvia a sanitizacao; o reexec continua
# obrigatorio e o main so roda no filho saneado.
bootstrap_single_reset
rc=0
(
  TMPDIR="$BOOTSTRAP_SINGLE"
  CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1
  CONTABASE_BOOTSTRAP_SANITIZED=1
  ATTACKER_VAR=1
  PATH="/tmp/contabase-hijack-inexistente:$PATH"
  export TMPDIR CONTABASE_BOOTSTRAP_CONTRACT_VERSION CONTABASE_BOOTSTRAP_SANITIZED ATTACKER_VAR PATH
  bash "$BOOTSTRAP_SINGLE/install-contabase-release.sh"
) > "$BOOTSTRAP_SINGLE/attack-env.log" 2>&1 || rc=$?
[ "$rc" -eq 0 ] \
  && ok "Variavel externa SANITIZED nao quebra o fluxo do Bootstrap" \
  || not_ok "Variavel externa SANITIZED derrubou o Bootstrap: $rc"
[ "$(bootstrap_single_executions)" -eq 1 ] \
  && ok "Ataque por variavel externa: instalacao ainda exatamente uma vez" \
  || not_ok "Ataque por variavel externa: execucao duplicada ou ausente"
bootstrap_env_is_sanitized \
  && ok "Ataque por variavel externa: main roda somente em ambiente saneado" \
  || not_ok "Ataque por variavel externa contornou a sanitizacao"

# Ataque por marcador de argv forjado com ambiente hostil: falha fechada antes
# do main, sem execucao e sem estado tocado.
bootstrap_single_reset
rc=0
(
  TMPDIR="$BOOTSTRAP_SINGLE"
  CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1
  PATH="/tmp/contabase-hijack-inexistente:$PATH"
  export TMPDIR CONTABASE_BOOTSTRAP_CONTRACT_VERSION PATH
  bash "$BOOTSTRAP_SINGLE/install-contabase-release.sh" --contabase-sanitized-reexec
) > "$BOOTSTRAP_SINGLE/attack-argv.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] \
  && ok "Marcador forjado com PATH hostil: falha fechada" \
  || not_ok "Marcador forjado com PATH hostil foi aceito"
[ "$(bootstrap_single_executions)" -eq 0 ] \
  && ok "Marcador forjado com PATH hostil: main nunca executa" \
  || not_ok "Marcador forjado com PATH hostil executou o main"
grep -q "PATH divergente do perfil saneado" "$BOOTSTRAP_SINGLE/attack-argv.log" \
  && ok "Marcador forjado com PATH hostil: causa reportada" \
  || not_ok "Marcador forjado com PATH hostil: causa nao reportada"

# Ataque com perfil replicado: marcador forjado, PATH seguro e HOME falso com
# padrao/0700/dono corretos, mas variavel exportada fora do perfil saneado.
# PATH==SAFE_PATH sozinho nao e prova suficiente de origem.
BOOTSTRAP_ATTACK_HOME="${WORK}/bootstrap-attack-home/contabase-bootstrap-home.abc123"
mkdir -p "$(dirname "$BOOTSTRAP_ATTACK_HOME")"
mkdir -m 0700 "$BOOTSTRAP_ATTACK_HOME"
bootstrap_single_reset
rc=0
(
  CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1
  ATTACKER_VAR=1
  HOME="$BOOTSTRAP_ATTACK_HOME"
  TMPDIR="$BOOTSTRAP_ATTACK_HOME"
  CURL_HOME="$BOOTSTRAP_ATTACK_HOME"
  PATH="$BOOTSTRAP_SAFE_PATH_EXPECTED"
  export CONTABASE_BOOTSTRAP_CONTRACT_VERSION ATTACKER_VAR HOME TMPDIR CURL_HOME PATH
  bash "$BOOTSTRAP_SINGLE/install-contabase-release.sh" --contabase-sanitized-reexec
) > "$BOOTSTRAP_SINGLE/attack-profile.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] \
  && ok "Marcador com perfil replicado e variavel estranha: falha fechada" \
  || not_ok "Marcador com perfil replicado foi aceito"
[ "$(bootstrap_single_executions)" -eq 0 ] \
  && ok "Marcador com perfil replicado: main nunca executa" \
  || not_ok "Marcador com perfil replicado executou o main"
grep -q "variavel exportada fora do perfil saneado" "$BOOTSTRAP_SINGLE/attack-profile.log" \
  && ok "Marcador com perfil replicado: causa reportada" \
  || not_ok "Marcador com perfil replicado: causa nao reportada"

# Sincronizacao do literal do marcador entre helper e adaptador.
grep -Fq -- '--contabase-sanitized-reexec' "$RELEASE" \
  && ok "Helper reconhece o marcador de argv do reexec" \
  || not_ok "Helper nao reconhece o marcador de argv do reexec"
grep -Fq 'CONTABASE_BOOTSTRAP_REEXEC_ARG="--contabase-sanitized-reexec"' "$ROOT_DIR/scripts/lib/contabase-bootstrap-adapter.sh" \
  && ok "Adaptador transmite o mesmo marcador de argv" \
  || not_ok "Adaptador diverge do marcador de argv do helper"

# 23c. Instalacao nova nao para servico inexistente; reinstalacao valida mantem stop.
INSTALL_BUNDLE_FRESH="${WORK}/install-bundle-fresh"
INSTALL_BUNDLE_REINSTALL="${WORK}/install-bundle-reinstall"
INSTALL_BUNDLE_PARTIAL="${WORK}/install-bundle-partial"
mkdir -p "$INSTALL_BUNDLE_FRESH" "$INSTALL_BUNDLE_REINSTALL" "$INSTALL_BUNDLE_PARTIAL"
printf 'old-bundle\n' > "$INSTALL_BUNDLE_REINSTALL/contabase"
printf 'old-unit\n' > "$WORK/contabase.service.reinstall"
printf 'old-unit-partial\n' > "$WORK/contabase.service.partial"

run_install_bundle_stop() {
  local bootstrap="$1" install_dir="$2" service_file="$3"
  (
    BOOTSTRAP_MODE="$bootstrap"
    INSTALL_DIR="$install_dir"
    SERVICE_FILE="$service_file"
    PREVIOUS_DIR=""
    SERVICE_BACKUP=""
    NEW_BUNDLE_ACTIVE=false
    ensure_user_and_directories() { :; }
    manage_env_file() { :; }
    stage_and_swap_bundle() { :; }
    render_service_file() { :; }
    wait_for_healthcheck() { :; }
    record_release_recovery() { :; }
    install_update_command() { :; }
    systemctl() { printf '%s\n' "$*" >> "$WORK/systemctl.log"; return 0; }
    say() { :; }
    fail() { printf 'Erro: %s\n' "$*" >&2; exit 1; }
    eval "$(extract_function install_bundle "$RELEASE")"
    install_bundle
  ) >/dev/null 2>&1
}

: > "$WORK/systemctl.log"
run_install_bundle_stop true "$INSTALL_BUNDLE_FRESH" "$WORK/service.absent" \
  && ok "Bootstrap inicial instala sem parar servico" || not_ok "Bootstrap inicial falhou no install_bundle"
if ! grep -qF 'stop contabase' "$WORK/systemctl.log"; then
  ok "Instalacao nova nao executa systemctl stop"
else
  not_ok "Instalacao nova executou systemctl stop indevido"
fi
grep -qF 'restart contabase' "$WORK/systemctl.log" && ok "Bootstrap inicial inicia o servico" || not_ok "Bootstrap inicial nao iniciou o servico"

: > "$WORK/systemctl.log"
run_install_bundle_stop false "$INSTALL_BUNDLE_REINSTALL" "$WORK/contabase.service.reinstall" \
  && ok "Reinstalacao valida conclui install_bundle" || not_ok "Reinstalacao valida falhou"
[ "$(sed -n '1p' "$WORK/systemctl.log")" = "stop contabase" ] \
  && ok "Reinstalacao valida para o servico antes da troca" \
  || not_ok "Reinstalacao valida nao parou o servico"

: > "$WORK/systemctl.log"
run_install_bundle_stop false "$INSTALL_BUNDLE_PARTIAL" "$WORK/service.absent" \
  && ok "Instalacao nova sem unit conclui install_bundle" || not_ok "Instalacao nova sem unit falhou"
if ! grep -qF 'stop contabase' "$WORK/systemctl.log"; then
  ok "Diretorio vazio sem unit nao tenta parar servico inexistente"
else
  not_ok "Diretorio vazio sem unit executou systemctl stop"
fi

# 24. Release: publicacao atomica do dispatcher e consistencia administrativa.
# Extrai o protocolo completo (helpers + install_update_command + write_install_state
# real) com paths reescritos para diretorios distintos por superficie (bin/etc/config).
ATOMIC="${WORK}/atomic"
ATOMIC_BIN="${ATOMIC}/bin"
ATOMIC_ETC="${ATOMIC}/etc"
ATOMIC_CONFIG="${ATOMIC}/config"
ATOMIC_TMP="${ATOMIC}/tmp"
ATOMIC_DISPATCHER="${ATOMIC_BIN}/contabase-update"
ATOMIC_MODE="${ATOMIC_ETC}/install-mode"
ATOMIC_STATE="${ATOMIC_CONFIG}/install-state.env"
mkdir -p "$ATOMIC_BIN" "$ATOMIC_ETC" "$ATOMIC_CONFIG" "$ATOMIC_TMP"
printf '#!/bin/sh\necho atomic-dispatcher-a\n' > "$WORK/atomic-wrapper-a.sh"
printf '#!/bin/sh\necho atomic-dispatcher-b\n' > "$WORK/atomic-wrapper-b.sh"
chmod 0755 "$WORK/atomic-wrapper-a.sh" "$WORK/atomic-wrapper-b.sh"
ATOMIC_SHA_A="$(sha_file "$WORK/atomic-wrapper-a.sh")"
ATOMIC_SHA_B="$(sha_file "$WORK/atomic-wrapper-b.sh")"
ATOMIC_COMMIT="$(printf 'a%.0s' {1..40})"

release_atomic_function() {
  local name text=""
  for name in path_uid path_mode admin_dir_is_secure publish_atomic_tmp \
              atomic_publish_authenticated_file write_install_state \
              record_admin_previous write_install_mode_atomic \
              verify_published_install_mode verify_published_dispatcher \
              restore_admin_file \
              compensate_admin_state_failure admin_state_recovery_banner \
              abort_admin_state_publication require_release_update_wrapper \
              install_update_command; do
    text="$text"$'\n'"$(extract_function "$name" "$RELEASE")"
  done
  printf '%s\n' "$text" \
    | sed "s#/usr/local/bin/contabase-update#${ATOMIC_DISPATCHER}#g; s#/usr/local/bin/cb-update#${ATOMIC_BIN}/cb-update#g; s#/etc/contabase/install-mode#${ATOMIC_MODE}#g"
}

atomic_reset() {
  rm -rf "$ATOMIC_BIN" "$ATOMIC_ETC" "$ATOMIC_CONFIG" "$ATOMIC_TMP"
  mkdir -p "$ATOMIC_BIN" "$ATOMIC_ETC" "$ATOMIC_CONFIG" "$ATOMIC_TMP"
}

atomic_env() {
  release_wrapper_helpers
  TAG="v1.0.0-beta.8"
  INSTALL_CHANNEL="beta"
  BOOTSTRAP_MODE="false"
  SCRIPT_DIR="${WORK}/release-ok/helper"
  BUNDLE_WRAPPER_PATH="${WORK}/atomic-wrapper-a.sh"
  BUNDLE_WRAPPER_SHA256="$ATOMIC_SHA_A"
  RELEASE_WRAPPER_SOURCE=""
  RELEASE_WRAPPER_SHA256=""
  CONFIG_DIR="$ATOMIC_CONFIG"
  STATE_FILE="$ATOMIC_STATE"
  TMP_DIR="$ATOMIC_TMP"
  ARTIFACT_NAME="contabase-linux-arm64.tar.gz"
  DISPATCHER_TMP=""
  DISPATCHER_RENAMED=false
  DISPATCHER_PUBLISHED=false
  DISPATCHER_PREV_KIND="other"
  DISPATCHER_PREV_BACKUP=""
  DISPATCHER_PREV_SHA=""
  MODE_RENAMED=false
  MODE_PUBLISHED=false
  MODE_PREV_KIND="other"
  MODE_PREV_BACKUP=""
  MODE_PREV_SHA=""
  STATE_RENAMED=false
  STATE_PUBLISHED=false
  STATE_PREV_KIND="other"
  STATE_PREV_BACKUP=""
  STATE_PREV_SHA=""
  say() { printf '%s\n' "$*" >&2; }
  fail() { printf 'Erro: %s\n' "$*" >&2; exit 1; }
  eval "$(release_atomic_function)"
}

atomic_no_temp_files() {
  local leftovers
  leftovers="$(find "$ATOMIC" \( -name '.contabase-publish.tmp.*' -o -name '.install-mode.tmp.*' -o -name '.install-state.env.tmp.*' \) -print 2>/dev/null)"
  [ -z "$leftovers" ]
}

atomic_file_sha() {
  if [ -f "$1" ] && [ ! -L "$1" ]; then sha_file "$1"; else printf 'MISSING'; fi
}

# Stub de cp parcial: corrompe apenas a copia para o temporario do dispatcher.
atomic_partial_cp() {
  cp() {
    [ "${1:-}" = "--" ] && shift
    case "$2" in
      "${ATOMIC_BIN}/.contabase-publish.tmp."*) head -c 2 "$1" > "$2" ;;
      *) command cp -- "$1" "$2" ;;
    esac
  }
}

# Stub de compute_sha256: diverge somente quando o dispatcher publicado ja
# contem o wrapper em instalacao (cenario de corrompimento pos-rename). O teste
# de conteudo e stateless porque cada $(...) roda em subshell propria.
atomic_corrupt_dest_sha() {
  compute_sha256() {
    if [ "$1" = "$ATOMIC_DISPATCHER" ] && [ -f "$1" ] \
      && cmp -s "$1" "$BUNDLE_WRAPPER_PATH"; then
      printf '%064d\n' 0
      return 0
    fi
    sha_file "$1"
  }
}

# --- Cenario A: instalacao nova, sem dispatcher anterior. ---

# A1. Copia parcial corrompe o temporario: falha antes do rename, destino ausente.
atomic_reset
rc=0
(
  atomic_env
  atomic_partial_cp
  install_update_command
) > "$ATOMIC/a1.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "A1 copia parcial: falha fechada" || not_ok "A1 copia parcial aceita"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "A1 dispatcher ausente" || not_ok "A1 dispatcher publicado parcial"
[ ! -e "$ATOMIC_MODE" ] && ok "A1 modo nao gravado" || not_ok "A1 modo gravado"
[ ! -e "$ATOMIC_STATE" ] && ok "A1 estado nao gravado" || not_ok "A1 estado gravado"
atomic_no_temp_files && ok "A1 sem temporarios remanescentes" || not_ok "A1 deixou temporario"
grep -q "SHA-256 divergente no temporario" "$ATOMIC/a1.log" && ok "A1 causa reportada" || not_ok "A1 causa nao reportada"

# A2. SHA da fonte diverge do autenticado apos a validacao: nada e publicado.
atomic_reset
rc=0
(
  atomic_env
  require_release_update_wrapper() {
    RELEASE_WRAPPER_SOURCE="$BUNDLE_WRAPPER_PATH"
    RELEASE_WRAPPER_SHA256="$ATOMIC_SHA_B"
  }
  install_update_command
) > "$ATOMIC/a2.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "A2 SHA invalido da fonte: falha fechada" || not_ok "A2 SHA invalido aceito"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "A2 dispatcher ausente" || not_ok "A2 dispatcher publicado"
[ ! -e "$ATOMIC_MODE" ] && ok "A2 modo nao gravado" || not_ok "A2 modo gravado"
[ ! -e "$ATOMIC_STATE" ] && ok "A2 estado nao gravado" || not_ok "A2 estado gravado"
atomic_no_temp_files && ok "A2 sem temporarios" || not_ok "A2 deixou temporario"

# A3a. Destino e diretorio: recusa antes de qualquer copia.
atomic_reset
mkdir "$ATOMIC_DISPATCHER"
rc=0
( atomic_env; install_update_command ) > "$ATOMIC/a3a.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "A3a destino-diretorio: falha fechada" || not_ok "A3a destino-diretorio aceito"
[ -d "$ATOMIC_DISPATCHER" ] && ok "A3a diretorio preexistente preservado" || not_ok "A3a diretorio preexistente alterado"
[ ! -e "$ATOMIC_MODE" ] && [ ! -e "$ATOMIC_STATE" ] && ok "A3a estado administrativo intocado" || not_ok "A3a estado alterado"
atomic_no_temp_files && ok "A3a sem temporarios" || not_ok "A3a deixou temporario"

# A3c. install-mode e diretorio: publish_atomic_tmp recusa antes do rename e o
# dispatcher publicado nesta execucao e compensado.
atomic_reset
mkdir "$ATOMIC_MODE"
rc=0
( atomic_env; install_update_command ) > "$ATOMIC/a3c.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "A3c install-mode diretorio: falha fechada" || not_ok "A3c install-mode diretorio aceito"
[ -d "$ATOMIC_MODE" ] && ok "A3c install-mode diretorio preservado" || not_ok "A3c install-mode diretorio alterado"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "A3c dispatcher compensado" || not_ok "A3c dispatcher permaneceu"
[ ! -e "$ATOMIC_STATE" ] && ok "A3c estado nao gravado" || not_ok "A3c estado gravado"
grep -q "destino de publicacao e diretorio" "$ATOMIC/a3c.log" && ok "A3c causa reportada" || not_ok "A3c causa nao reportada"
atomic_no_temp_files && ok "A3c sem temporarios" || not_ok "A3c deixou temporario"

# A3b. Rename falha: temporario removido, destino permanece ausente.
atomic_reset
rc=0
(
  atomic_env
  mv() { return 1; }
  install_update_command
) > "$ATOMIC/a3b.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "A3b rename falho: falha fechada" || not_ok "A3b rename falho aceito"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "A3b dispatcher ausente" || not_ok "A3b dispatcher publicado"
[ ! -e "$ATOMIC_MODE" ] && [ ! -e "$ATOMIC_STATE" ] && ok "A3b estado administrativo ausente" || not_ok "A3b estado gravado"
grep -q "falha no rename atomico" "$ATOMIC/a3b.log" && ok "A3b causa reportada" || not_ok "A3b causa nao reportada"
atomic_no_temp_files && ok "A3b sem temporarios" || not_ok "A3b deixou temporario"

# A4. Falha de persistencia do estado apos publicar dispatcher e modo:
# compensacao remove as publicacoes proprias; nada declara sucesso.
atomic_reset
chmod 0555 "$ATOMIC_CONFIG"
rc=0
( atomic_env; install_update_command ) > "$ATOMIC/a4.log" 2>&1 || rc=$?
chmod 0755 "$ATOMIC_CONFIG"
[ "$rc" -ne 0 ] && ok "A4 falha de estado: falha fechada" || not_ok "A4 falha de estado aceita"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "A4 dispatcher compensado (ausente)" || not_ok "A4 dispatcher permaneceu"
[ ! -e "$ATOMIC_MODE" ] && ok "A4 modo compensado (ausente)" || not_ok "A4 modo permaneceu"
[ ! -e "$ATOMIC_STATE" ] && ok "A4 estado ausente (nunca publicado)" || not_ok "A4 estado publicado"
grep -q "falha ao criar temporario do estado administrativo" "$ATOMIC/a4.log" && ok "A4 causa reportada" || not_ok "A4 causa nao reportada"
grep -q "INCONSISTENCIA ADMINISTRATIVA" "$ATOMIC/a4.log" && not_ok "A4 banner desnecessario" || ok "A4 sem banner: compensacao completa"
atomic_no_temp_files && ok "A4 sem temporarios" || not_ok "A4 deixou temporario"

# A5. Interrupcao (SIGTERM) durante a publicacao: EXIT trap remove o temporario.
atomic_reset
rc=0
(
  atomic_env
  eval "$(extract_function cleanup "$RELEASE")"
  trap cleanup EXIT
  trap 'exit 143' TERM
  mv() { kill -TERM "$(sh -c 'printf "%s" "$PPID"')"; }
  install_update_command
) > "$ATOMIC/a5.log" 2>&1 || rc=$?
[ "$rc" -eq 143 ] && ok "A5 interrupcao: encerrada pelo sinal" || not_ok "A5 rc inesperado: $rc"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "A5 dispatcher ausente" || not_ok "A5 dispatcher publicado"
[ ! -e "$ATOMIC_MODE" ] && [ ! -e "$ATOMIC_STATE" ] && ok "A5 estado administrativo ausente" || not_ok "A5 estado gravado"
atomic_no_temp_files && ok "A5 temporario removido pelo trap" || not_ok "A5 deixou temporario"

# A6. SHA divergente apos o rename (corrupcao pos-publicacao): compensacao
# remove a publicacao desta execucao quando nao havia dispatcher anterior.
atomic_reset
rc=0
(
  atomic_env
  atomic_corrupt_dest_sha
  install_update_command
) > "$ATOMIC/a6.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "A6 SHA pos-rename: falha fechada" || not_ok "A6 SHA pos-rename aceito"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "A6 publicacao corrompida removida" || not_ok "A6 dispatcher corrompido permaneceu"
[ ! -e "$ATOMIC_MODE" ] && [ ! -e "$ATOMIC_STATE" ] && ok "A6 estado administrativo ausente" || not_ok "A6 estado gravado"
grep -q "SHA-256 divergente apos a publicacao atomica" "$ATOMIC/a6.log" && ok "A6 causa reportada" || not_ok "A6 causa nao reportada"

# A7. install-mode publicado diverge do conteudo gravado (corrupcao
# pos-publicacao): a verificacao falha e a publicacao desta execucao e compensada.
atomic_reset
rc=0
(
  atomic_env
  compute_sha256() {
    if [ "$1" = "$ATOMIC_MODE" ] && [ -f "$1" ]; then
      printf '%064d\n' 0
      return 0
    fi
    sha_file "$1"
  }
  install_update_command
) > "$ATOMIC/a7.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "A7 install-mode divergente: falha fechada" || not_ok "A7 install-mode divergente aceito"
[ ! -e "$ATOMIC_MODE" ] && ok "A7 install-mode corrompido removido" || not_ok "A7 install-mode corrompido permaneceu"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "A7 dispatcher compensado" || not_ok "A7 dispatcher permaneceu"
[ ! -e "$ATOMIC_STATE" ] && ok "A7 estado nao gravado" || not_ok "A7 estado gravado"
grep -q "install-mode publicado diverge do conteudo gravado" "$ATOMIC/a7.log" && ok "A7 causa reportada" || not_ok "A7 causa nao reportada"

# --- Cenario C: caminho feliz e idempotencia (base para o cenario B). ---
atomic_reset
rc=0
( atomic_env; install_update_command ) > "$ATOMIC/c1.log" 2>&1 || rc=$?
[ "$rc" -eq 0 ] && ok "C1 caminho feliz conclui" || not_ok "C1 caminho feliz falhou"
[ "$(atomic_file_sha "$ATOMIC_DISPATCHER")" = "$ATOMIC_SHA_A" ] && ok "C1 dispatcher igual ao wrapper autenticado" || not_ok "C1 dispatcher divergente"
[ "$(cat "$ATOMIC_MODE")" = "release" ] && ok "C1 modo release" || not_ok "C1 modo divergente"
grep -qxF "CONTABASE_INSTALL_METHOD=release" "$ATOMIC_STATE" && ok "C1 estado method=release" || not_ok "C1 estado sem method"
grep -qxF "CONTABASE_INSTALLED_VERSION=v1.0.0-beta.8" "$ATOMIC_STATE" && ok "C1 estado version" || not_ok "C1 estado sem version"
grep -qxF "CONTABASE_CHANNEL=beta" "$ATOMIC_STATE" && ok "C1 estado channel" || not_ok "C1 estado sem channel"
[ "$(stat -c %a "$ATOMIC_DISPATCHER" 2>/dev/null || stat -f %Lp "$ATOMIC_DISPATCHER")" = "755" ] && ok "C1 dispatcher 0755" || not_ok "C1 modo do dispatcher divergente"
[ -L "$ATOMIC_BIN/cb-update" ] && ok "C1 alias cb-update criado" || not_ok "C1 alias ausente"
atomic_no_temp_files && ok "C1 sem temporarios" || not_ok "C1 deixou temporario"

# C2. Idempotencia: reexecucao mantem bytes e permissoes.
C1_DISP_SHA="$(atomic_file_sha "$ATOMIC_DISPATCHER")"
C1_MODE_SHA="$(atomic_file_sha "$ATOMIC_MODE")"
C1_STATE_SHA="$(atomic_file_sha "$ATOMIC_STATE")"
rc=0
( atomic_env; install_update_command ) > "$ATOMIC/c2.log" 2>&1 || rc=$?
[ "$rc" -eq 0 ] && ok "C2 reexecucao conclui" || not_ok "C2 reexecucao falhou"
[ "$(atomic_file_sha "$ATOMIC_DISPATCHER")" = "$C1_DISP_SHA" ] && ok "C2 dispatcher estavel" || not_ok "C2 dispatcher mudou"
[ "$(atomic_file_sha "$ATOMIC_MODE")" = "$C1_MODE_SHA" ] && ok "C2 modo estavel" || not_ok "C2 modo mudou"
[ "$(atomic_file_sha "$ATOMIC_STATE")" = "$C1_STATE_SHA" ] && ok "C2 estado estavel" || not_ok "C2 estado mudou"

# --- Cenario B: instalacao existente; falhas preservam dispatcher e estado. ---
B_SNAP_DISP="$C1_DISP_SHA"
B_SNAP_MODE="$C1_MODE_SHA"
B_SNAP_STATE="$C1_STATE_SHA"

# B1. Copia parcial do novo wrapper: dispatcher anterior intocado.
rc=0
(
  atomic_env
  BUNDLE_WRAPPER_PATH="$WORK/atomic-wrapper-b.sh"
  BUNDLE_WRAPPER_SHA256="$ATOMIC_SHA_B"
  atomic_partial_cp
  install_update_command
) > "$ATOMIC/b1.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "B1 copia parcial: falha fechada" || not_ok "B1 copia parcial aceita"
[ "$(atomic_file_sha "$ATOMIC_DISPATCHER")" = "$B_SNAP_DISP" ] && ok "B1 dispatcher anterior preservado" || not_ok "B1 dispatcher anterior perdido"
[ "$(atomic_file_sha "$ATOMIC_MODE")" = "$B_SNAP_MODE" ] && ok "B1 modo anterior preservado" || not_ok "B1 modo alterado"
[ "$(atomic_file_sha "$ATOMIC_STATE")" = "$B_SNAP_STATE" ] && ok "B1 estado anterior preservado" || not_ok "B1 estado alterado"
atomic_no_temp_files && ok "B1 sem temporarios" || not_ok "B1 deixou temporario"

# B2. SHA divergente apos o rename: compensacao restaura o dispatcher anterior.
rc=0
(
  atomic_env
  BUNDLE_WRAPPER_PATH="$WORK/atomic-wrapper-b.sh"
  BUNDLE_WRAPPER_SHA256="$ATOMIC_SHA_B"
  atomic_corrupt_dest_sha
  install_update_command
) > "$ATOMIC/b2.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "B2 SHA pos-rename: falha fechada" || not_ok "B2 SHA pos-rename aceito"
[ "$(atomic_file_sha "$ATOMIC_DISPATCHER")" = "$B_SNAP_DISP" ] && ok "B2 dispatcher anterior restaurado" || not_ok "B2 restauracao falhou"
[ "$(atomic_file_sha "$ATOMIC_MODE")" = "$B_SNAP_MODE" ] && ok "B2 modo anterior preservado" || not_ok "B2 modo alterado"
[ "$(atomic_file_sha "$ATOMIC_STATE")" = "$B_SNAP_STATE" ] && ok "B2 estado anterior preservado" || not_ok "B2 estado alterado"
grep -q "SHA-256 divergente apos a publicacao atomica" "$ATOMIC/b2.log" && ok "B2 causa reportada" || not_ok "B2 causa nao reportada"
grep -q "INCONSISTENCIA ADMINISTRATIVA" "$ATOMIC/b2.log" && not_ok "B2 banner desnecessario" || ok "B2 compensacao completa (sem banner)"
atomic_no_temp_files && ok "B2 sem temporarios" || not_ok "B2 deixou temporario"

# B3. Rename falho: dispatcher anterior nunca e substituido.
rc=0
(
  atomic_env
  BUNDLE_WRAPPER_PATH="$WORK/atomic-wrapper-b.sh"
  BUNDLE_WRAPPER_SHA256="$ATOMIC_SHA_B"
  mv() { return 1; }
  install_update_command
) > "$ATOMIC/b3.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "B3 rename falho: falha fechada" || not_ok "B3 rename falho aceito"
[ "$(atomic_file_sha "$ATOMIC_DISPATCHER")" = "$B_SNAP_DISP" ] && ok "B3 dispatcher anterior preservado" || not_ok "B3 dispatcher anterior perdido"
[ "$(atomic_file_sha "$ATOMIC_MODE")" = "$B_SNAP_MODE" ] && ok "B3 modo anterior preservado" || not_ok "B3 modo alterado"
[ "$(atomic_file_sha "$ATOMIC_STATE")" = "$B_SNAP_STATE" ] && ok "B3 estado anterior preservado" || not_ok "B3 estado alterado"

# B4. Falha ao gravar install-state.env: dispatcher e modo novos sao compensados.
chmod 0555 "$ATOMIC_CONFIG"
rc=0
(
  atomic_env
  BUNDLE_WRAPPER_PATH="$WORK/atomic-wrapper-b.sh"
  BUNDLE_WRAPPER_SHA256="$ATOMIC_SHA_B"
  install_update_command
) > "$ATOMIC/b4.log" 2>&1 || rc=$?
chmod 0755 "$ATOMIC_CONFIG"
[ "$rc" -ne 0 ] && ok "B4 falha de estado: falha fechada" || not_ok "B4 falha de estado aceita"
[ "$(atomic_file_sha "$ATOMIC_DISPATCHER")" = "$B_SNAP_DISP" ] && ok "B4 dispatcher anterior restaurado" || not_ok "B4 ficou com dispatcher novo sem estado"
[ "$(atomic_file_sha "$ATOMIC_MODE")" = "$B_SNAP_MODE" ] && ok "B4 modo anterior restaurado" || not_ok "B4 modo novo permaneceu"
[ "$(atomic_file_sha "$ATOMIC_STATE")" = "$B_SNAP_STATE" ] && ok "B4 estado anterior intacto" || not_ok "B4 estado anterior corrompido"
grep -q "INCONSISTENCIA ADMINISTRATIVA" "$ATOMIC/b4.log" && not_ok "B4 banner desnecessario" || ok "B4 compensacao completa (sem banner)"
atomic_no_temp_files && ok "B4 sem temporarios" || not_ok "B4 deixou temporario"

# B5. Falha ao gravar install-mode: dispatcher novo compensado, estado intocado.
chmod 0555 "$ATOMIC_ETC"
rc=0
(
  atomic_env
  BUNDLE_WRAPPER_PATH="$WORK/atomic-wrapper-b.sh"
  BUNDLE_WRAPPER_SHA256="$ATOMIC_SHA_B"
  install_update_command
) > "$ATOMIC/b5.log" 2>&1 || rc=$?
chmod 0755 "$ATOMIC_ETC"
[ "$rc" -ne 0 ] && ok "B5 falha de modo: falha fechada" || not_ok "B5 falha de modo aceita"
[ "$(atomic_file_sha "$ATOMIC_DISPATCHER")" = "$B_SNAP_DISP" ] && ok "B5 dispatcher anterior restaurado" || not_ok "B5 ficou com dispatcher novo"
[ "$(atomic_file_sha "$ATOMIC_MODE")" = "$B_SNAP_MODE" ] && ok "B5 modo anterior intacto" || not_ok "B5 modo alterado"
[ "$(atomic_file_sha "$ATOMIC_STATE")" = "$B_SNAP_STATE" ] && ok "B5 estado anterior intacto" || not_ok "B5 estado alterado"

# --- Cenario D: Bootstrap autenticado. ---
mkdir -p "$WORK/atomic-bootstrap/lib"
cp "$WORK/atomic-wrapper-a.sh" "$WORK/atomic-bootstrap/lib/contabase-update-wrapper.sh"

# D1. Pin do Bootstrap diverge do artifact: nada e publicado.
atomic_reset
rc=0
(
  atomic_env
  BOOTSTRAP_MODE="true"
  SCRIPT_DIR="$WORK/atomic-bootstrap"
  CONTABASE_UPDATE_WRAPPER_SHA256="$ATOMIC_SHA_A"
  BUNDLE_WRAPPER_PATH="$WORK/atomic-wrapper-b.sh"
  BUNDLE_WRAPPER_SHA256="$ATOMIC_SHA_B"
  install_update_command
) > "$ATOMIC/d1.log" 2>&1 || rc=$?
[ "$rc" -ne 0 ] && ok "D1 pin divergente do artifact: falha fechada" || not_ok "D1 pin divergente aceito"
[ ! -e "$ATOMIC_DISPATCHER" ] && ok "D1 dispatcher ausente" || not_ok "D1 dispatcher publicado"
[ ! -e "$ATOMIC_MODE" ] && [ ! -e "$ATOMIC_STATE" ] && ok "D1 estado administrativo ausente" || not_ok "D1 estado gravado"
grep -q "diverge do artifact Release" "$ATOMIC/d1.log" && ok "D1 causa reportada" || not_ok "D1 causa nao reportada"

# D2. Bootstrap valido: publica exatamente o wrapper pinado e o estado extendido.
atomic_reset
rc=0
(
  atomic_env
  BOOTSTRAP_MODE="true"
  SCRIPT_DIR="$WORK/atomic-bootstrap"
  CONTABASE_UPDATE_WRAPPER_SHA256="$ATOMIC_SHA_A"
  CONTABASE_ARTIFACT_NAME="contabase-linux-arm64.tar.gz"
  CONTABASE_ARTIFACT_PLATFORM="linux/arm64"
  CONTABASE_ARTIFACT_SHA256="$(printf 'b%.0s' {1..64})"
  CONTABASE_CHECKSUMS_SHA256="$(printf 'c%.0s' {1..64})"
  CONTABASE_HELPER_SHA256="$(printf 'd%.0s' {1..64})"
  CONTABASE_COMMIT="$ATOMIC_COMMIT"
  CONTABASE_NETWORK_PROFILE_VALUE="local"
  CONTABASE_ACCESS_MODE_VALUE="local"
  CONTABASE_HOST_BIND_VALUE="127.0.0.1"
  PORT="18080"
  INSTALL_DIR="/opt/contabase"
  DATA_DIR="/var/lib/contabase"
  install_update_command
) > "$ATOMIC/d2.log" 2>&1 || rc=$?
[ "$rc" -eq 0 ] && ok "D2 bootstrap conclui" || not_ok "D2 bootstrap falhou"
[ "$(atomic_file_sha "$ATOMIC_DISPATCHER")" = "$ATOMIC_SHA_A" ] && ok "D2 publica exatamente o wrapper pinado" || not_ok "D2 dispatcher diverge do pin"
grep -qxF "CONTABASE_BOOTSTRAP_ADAPTER=release" "$ATOMIC_STATE" && ok "D2 estado bootstrap adapter" || not_ok "D2 estado sem adapter"
grep -qxF "CONTABASE_COMMIT=$ATOMIC_COMMIT" "$ATOMIC_STATE" && ok "D2 estado commit homologado" || not_ok "D2 estado sem commit"
grep -qxF "CONTABASE_HELPER_SHA256=$(printf 'd%.0s' {1..64})" "$ATOMIC_STATE" && ok "D2 estado pins preservados" || not_ok "D2 estado sem pins"

# D3. O protocolo novo nao introduz download ou fallback de rede.
atomic_network_refs="$(extract_function install_update_command "$RELEASE" | grep -Ec 'curl|wget|https?://' || true)"
[ "$atomic_network_refs" = "0" ] && ok "D3 publicacao sem download/fallback de rede" || not_ok "D3 protocolo referencia rede"

# 25. Nenhum fallback legado remanescente.
for script in "$SOURCE" "$DOCKER" "$UPDATE_SOURCE" "$UPDATE_DOCKER"; do
  if grep -q "WRAPPER_EOF" "$script"; then
    not_ok "fallback legado remanescente: $(basename "$script")"
  else
    ok "sem heredoc legado: $(basename "$script")"
  fi
done

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

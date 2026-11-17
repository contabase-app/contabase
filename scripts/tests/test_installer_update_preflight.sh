#!/usr/bin/env bash
# Focused checks: installer flock preflight and authenticated updater fallback removal.
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

# 19-24. Release: wrapper autenticado presente/ausente.
mkdir -p "$WORK/release-missing" "$WORK/release-ok/lib" "$WORK/release-config"
printf '#!/bin/sh\necho release-authenticated-dispatcher\n' > "$WORK/release-ok/lib/contabase-update-wrapper.sh"
chmod 0755 "$WORK/release-ok/lib/contabase-update-wrapper.sh"

release_install_function() {
  local text
  text="$(extract_function require_release_update_wrapper "$RELEASE")"
  text="$text"$'\n'"$(extract_function install_update_command "$RELEASE")"
  printf '%s\n' "$text" \
    | sed "s#/usr/local/bin/contabase-update#$WORK/release-update#g; s#/usr/local/bin/cb-update#$WORK/release-cb-update#g; s#/etc/contabase/install-mode#$WORK/release-install-mode#g"
}

run_release_install() {
  local script_dir="$1"
  (
    SCRIPT_DIR="$script_dir"
    TAG=v1.0.0-beta.8
    INSTALL_CHANNEL=beta
    write_install_state() { printf 'state\n' >> "$WORK/release-state.log"; }
    install() { install_stub "$@"; }
    chown() { :; }
    say() { :; }
    fail() { exit 1; }
    eval "$(release_install_function)"
    install_update_command
  ) >/dev/null 2>&1
}

printf 'release-sentinel\n' > "$WORK/release-update"
run_release_install "$WORK/release-missing" && not_ok "Release instalou update sem wrapper" || ok "Release falha fechado sem wrapper"
[ "$(cat "$WORK/release-update")" = "release-sentinel" ] && ok "Release preservou comando existente" || not_ok "Release substituiu comando existente"
[ ! -e "$WORK/release-install-mode" ] && ok "Release nao alterou modo sem wrapper" || not_ok "Release alterou modo sem wrapper"
rm -f "$WORK/release-update" "$WORK/release-cb-update" "$WORK/release-install-mode"
run_release_install "$WORK/release-ok" && ok "Release instala wrapper autenticado" || not_ok "Release nao instalou wrapper autenticado"
cmp -s "$WORK/release-ok/lib/contabase-update-wrapper.sh" "$WORK/release-update" && ok "Release copiou dispatcher autenticado" || not_ok "Release copiou conteudo divergente"

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

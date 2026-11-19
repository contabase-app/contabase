#!/usr/bin/env bash
# Regression: a restricted umask during extraction must never leave the new
# bundle inaccessible to the non-root service user. This test exercises the
# normalization and the service-user preflight used by both the installer and
# the updater. The full systemd Beta 7 -> Beta 9 flow is covered by
# test_release_update_permissions_systemd.sh.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
INSTALLER="$REPO_DIR/scripts/install-contabase-release.sh"
UPDATER="$REPO_DIR/scripts/update-contabase-release.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/contabase-bundle-permissions.XXXXXX")"
chmod 0755 "$TMP_ROOT"

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }
not_ok() { FAIL=$((FAIL + 1)); printf 'not ok - %s\n' "$1" >&2; }

cleanup() { rm -rf "$TMP_ROOT"; }
trap cleanup EXIT

extract_function() {
  awk -v name="$1" '$0 ~ "^" name "\\(\\) \\{" { f=1 } f { print } f && /^}$/ { exit }' "$2"
}

stat_mode() {
  stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"
}

make_private_bundle() {
  local root="$1"
  mkdir -p "$root/assets/css" "$root/templates/pages" "$root/lib"
  printf '#!/bin/sh\nexit 0\n' > "$root/contabase"
  printf '#!/bin/sh\nexit 0\n' > "$root/admin"
  printf '#!/bin/sh\nexit 0\n' > "$root/lib/contabase-update-wrapper.sh"
  printf 'v1.0.0-beta.9\n' > "$root/VERSION"
  printf '/* fixture */\n' > "$root/assets/css/style.css"
  printf '<html>fixture</html>\n' > "$root/templates/pages/login.html"
  find "$root" -type d -exec chmod 0700 {} +
  find "$root" -type f -exec chmod 0600 {} +
}

check_normalized() {
  local root="$1" label="$2" file
  for file in "$root" "$root/assets" "$root/assets/css" "$root/templates" "$root/lib"; do
    [ "$(stat_mode "$file")" = "755" ] \
      && ok "$label: diretorio 0755 ${file#$root/}" \
      || not_ok "$label: diretorio com modo $(stat_mode "$file") em ${file#$root/}"
  done
  for file in "$root/VERSION" "$root/assets/css/style.css" "$root/templates/pages/login.html"; do
    [ "$(stat_mode "$file")" = "644" ] \
      && ok "$label: arquivo 0644 ${file#$root/}" \
      || not_ok "$label: arquivo com modo $(stat_mode "$file") em ${file#$root/}"
  done
  for file in "$root/contabase" "$root/admin" "$root/lib/contabase-update-wrapper.sh"; do
    [ "$(stat_mode "$file")" = "755" ] \
      && ok "$label: executavel 0755 ${file#$root/}" \
      || not_ok "$label: executavel com modo $(stat_mode "$file") em ${file#$root/}"
  done
  [ ! -L "$root" ] && ok "$label: raiz nao e symlink" || not_ok "$label: raiz virou symlink"
}

run_normalization_case() {
  local script="$1" label="$2" root="$TMP_ROOT/$label"
  make_private_bundle "$root"
  eval "$(extract_function normalize_bundle_tree "$script")"
  if normalize_bundle_tree "$root"; then
    ok "$label: normalize_bundle_tree aceitou arvore privada"
  else
    not_ok "$label: normalize_bundle_tree rejeitou arvore privada"
    return
  fi
  check_normalized "$root" "$label"
  [ -r "$root/VERSION" ] && ok "$label: VERSION legivel pelo dono" || not_ok "$label: VERSION ilegivel"
  [ -r "$root/assets/css/style.css" ] && ok "$label: asset legivel pelo dono" || not_ok "$label: asset ilegivel"
}

run_symlink_case() {
  local script="$1" label="$2" root="$TMP_ROOT/$label-symlink"
  make_private_bundle "$root"
  ln -s /etc/passwd "$root/assets/escape"
  eval "$(extract_function normalize_bundle_tree "$script")"
  if normalize_bundle_tree "$root"; then
    not_ok "$label: normalize_bundle_tree aceitou symlink"
  else
    ok "$label: normalize_bundle_tree rejeitou symlink"
  fi
}

run_access_case() {
  local script="$1" label="$2" root="$TMP_ROOT/$label-access"
  make_private_bundle "$root"
  eval "$(extract_function normalize_bundle_tree "$script")"
  eval "$(extract_function service_user_can_access_bundle "$script")"
  normalize_bundle_tree "$root" || { not_ok "$label: falha ao normalizar fixture"; return; }
  if [ "$(id -u)" -ne 0 ] || ! command -v runuser >/dev/null 2>&1; then
    printf 'skip - %s: preflight real do usuario do servico exige root e runuser\n' "$label"
    return
  fi
  if service_user_can_access_bundle "$root" nobody; then
    ok "$label: usuario nao-root acessa bundle normalizado"
  else
    not_ok "$label: usuario nao-root nao acessa bundle normalizado"
  fi
  chmod 0700 "$root/assets"
  if service_user_can_access_bundle "$root" nobody; then
    not_ok "$label: preflight aceitou assets 0700"
  else
    ok "$label: preflight bloqueia assets 0700"
  fi
  chmod 0755 "$root/assets"
  chmod 0600 "$root/VERSION"
  if service_user_can_access_bundle "$root" nobody; then
    not_ok "$label: preflight aceitou VERSION 0600"
  else
    ok "$label: preflight bloqueia VERSION 0600"
  fi
}

run_cleanup_case() {
  local script="$1" label="$2" staging="$TMP_ROOT/$label-staging"
  mkdir -p "$staging"
  eval "$(extract_function cleanup "$script")"
  STAGING_DIR="$staging"
  TMP_DIR=""
  BOOTSTRAP_TMP_DIR=""
  DISPATCHER_TMP=""
  cleanup
  [ ! -e "$staging" ] && ok "$label: cleanup remove staging" || not_ok "$label: cleanup deixou staging"
}

run_order_case() {
  local script="$1" label="$2" stage_line stop_line
  stage_line="$(grep -n '^  stage_bundle$' "$script" | head -1 | cut -d: -f1)"
  if [ "$label" = "updater" ]; then
    stop_line="$(grep -n '^  stop_service$' "$script" | head -1 | cut -d: -f1)"
  else
    stop_line="$(grep -n 'systemctl stop contabase' "$script" | tail -1 | cut -d: -f1)"
  fi
  if [ -n "$stage_line" ] && [ -n "$stop_line" ] && [ "$stage_line" -lt "$stop_line" ]; then
    ok "$label: stage_bundle ocorre antes da parada/substituicao"
  else
    not_ok "$label: stage_bundle nao ocorre antes da parada/substituicao"
  fi
}

for pair in "$INSTALLER:installer" "$UPDATER:updater"; do
  script="${pair%%:*}"
  label="${pair##*:}"
  run_normalization_case "$script" "$label"
  run_symlink_case "$script" "$label"
  run_access_case "$script" "$label"
  run_cleanup_case "$script" "$label"
  run_order_case "$script" "$label"
done

printf '\n%d ok, %d falhas\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

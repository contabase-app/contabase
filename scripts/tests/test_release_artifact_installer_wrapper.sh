#!/usr/bin/env bash
# Regression: the Release artifact must carry the update wrapper and the
# standalone helper must consume it from the extracted bundle.
# Uses the tarballs produced by .github/scripts/release-validation.sh.
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIST_DIR="${1:-${RELEASE_DIST_DIR:-$REPO_DIR/release-dist}}"
VERSION="$(tr -d '[:space:]' < "$REPO_DIR/VERSION")"
HELPER_SOURCE="$REPO_DIR/scripts/install-contabase-release.sh"
WRAPPER_SOURCE="$REPO_DIR/scripts/lib/contabase-update-wrapper.sh"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/contabase-release-wrapper-test.XXXXXX")"

cleanup() {
  if [ "${KEEP_TMP:-0}" = 1 ]; then
    printf '# fixtures preservadas em %s\n' "$TMP_ROOT" >&2
  else
    rm -rf "$TMP_ROOT"
  fi
}
trap cleanup EXIT

PASS=0
FAIL=0
OUT=""
ok() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }
not_ok() { FAIL=$((FAIL + 1)); printf 'not ok - %s\n' "$1" >&2; }

assert_eq() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then ok "$name"; else not_ok "$name (esperado=$expected obtido=$actual)"; fi
}
assert_contains() {
  local name="$1" text="$2"
  if grep -Fq -- "$text" "$OUT"; then ok "$name"; else not_ok "$name"; fi
}
assert_file() {
  local name="$1" path="$2"
  if [ -f "$path" ] && [ ! -L "$path" ]; then ok "$name"; else not_ok "$name"; fi
}

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

channel_for_version() {
  case "$1" in
    *-beta.*) printf '%s' beta ;;
    *) printf '%s' stable ;;
  esac
}

prepare_helper() {
  local directory="$1" with_lib="$2"
  mkdir -p "$directory"
  cp "$HELPER_SOURCE" "$directory/install-contabase-release.sh"
  if [ "$with_lib" = 1 ]; then
    mkdir -p "$directory/lib"
    cp "$WRAPPER_SOURCE" "$directory/lib/contabase-update-wrapper.sh"
  fi
}

run_validate() {
  local label="$1" arch="$2" uname_m="$3" fixture="$4" helper_dir="$5"
  local rc=0
  OUT="$TMP_ROOT/$label.out"
  (
    export CONTABASE_TEST_MODE=1
    export CONTABASE_TEST_FIXTURE_DIR="$fixture"
    export CONTABASE_TEST_UNAME_M="$uname_m"
    export CONTABASE_VERSION="$VERSION"
    CONTABASE_CHANNEL="$(channel_for_version "$VERSION")"
    export CONTABASE_CHANNEL
    export CONTABASE_INSTALL_DIR="$TMP_ROOT/$label/install"
    export CONTABASE_DATA_DIR="$TMP_ROOT/$label/data"
    export CONTABASE_CONFIG_DIR="$TMP_ROOT/$label/config"
    export CONTABASE_ACCESS_MODE=local
    export APP_BASE_URL=http://localhost:18080
    export ALLOWED_HOSTS=localhost,127.0.0.1,::1
    export CONTABASE_PORT=18080
    export HOME="$TMP_ROOT/home"
    export TMPDIR="$TMP_ROOT/tmp"
    mkdir -p "$HOME" "$TMPDIR"
    bash "$helper_dir/install-contabase-release.sh" --validate-only
  ) > "$OUT" 2>&1 || rc=$?
  return "$rc"
}

repack_archive() {
  local root="$1" destination="$2"
  (
    cd "$root"
    COPYFILE_DISABLE=1 tar -czf "$destination" contabase
  )
}

write_checksums() {
  local directory="$1" arch="$2"
  printf '%s  %s\n' \
    "$(sha_file "$directory/contabase-linux-$arch.tar.gz")" \
    "contabase-linux-$arch.tar.gz" > "$directory/checksums.txt"
}

make_mutated_fixture() {
  local arch="$1" mode="$2" destination="$3"
  local root="$TMP_ROOT/mut-$mode-$arch"
  rm -rf "$root"
  mkdir -p "$root" "$destination"
  tar -xzf "$DIST_DIR/contabase-linux-$arch.tar.gz" -C "$root"
  case "$mode" in
    absent) rm -f "$root/contabase/lib/contabase-update-wrapper.sh" ;;
    empty) : > "$root/contabase/lib/contabase-update-wrapper.sh" ;;
    invalid) printf 'if true; then\n' > "$root/contabase/lib/contabase-update-wrapper.sh" ;;
    symlink)
      rm -f "$root/contabase/lib/contabase-update-wrapper.sh"
      ln -s ../../contabase "$root/contabase/lib/contabase-update-wrapper.sh"
      ;;
    *) return 1 ;;
  esac
  repack_archive "$root" "$destination/contabase-linux-$arch.tar.gz"
  write_checksums "$destination" "$arch"
}

if [ ! -f "$WRAPPER_SOURCE" ] || [ -L "$WRAPPER_SOURCE" ]; then
  not_ok "wrapper fonte ausente ou inseguro"
  exit 1
fi
if [ ! -f "$HELPER_SOURCE" ] || [ -L "$HELPER_SOURCE" ]; then
  not_ok "helper fonte ausente ou inseguro"
  exit 1
fi

for arch in amd64 arm64; do
  archive="$DIST_DIR/contabase-linux-$arch.tar.gz"
  checksums="$DIST_DIR/checksums.txt"
  if [ ! -f "$archive" ] || [ ! -f "$checksums" ]; then
    not_ok "artifacts ausentes para $arch em $DIST_DIR"
    exit 1
  fi

  listing="$TMP_ROOT/contents-$arch.txt"
  tar -tzf "$archive" > "$listing"
  wrapper_count="$(grep -Fxc 'contabase/lib/contabase-update-wrapper.sh' "$listing" || true)"
  assert_eq "artifact $arch: wrapper unico no caminho exato" "1" "$wrapper_count"

  extracted="$TMP_ROOT/extract-$arch"
  mkdir -p "$extracted"
  tar -xzf "$archive" -C "$extracted"
  extracted_wrapper="$extracted/contabase/lib/contabase-update-wrapper.sh"
  assert_file "artifact $arch: wrapper regular" "$extracted_wrapper"
  extracted_version="$(tr -d '[:space:]' < "$extracted/contabase/VERSION")"
  assert_eq "artifact $arch: VERSION" "$VERSION" "$extracted_version"
  assert_eq "artifact $arch: wrapper igual a fonte" "$(sha_file "$WRAPPER_SOURCE")" "$(sha_file "$extracted_wrapper")"

  helper_dir="$TMP_ROOT/helper-$arch"
  prepare_helper "$helper_dir" 0
  if run_validate "positive-$arch" "$arch" "$([ "$arch" = amd64 ] && printf x86_64 || printf aarch64)" "$DIST_DIR" "$helper_dir"; then
    ok "standalone $arch: validate-only aceita artifact real"
  else
    not_ok "standalone $arch: validate-only rejeitou artifact real"
  fi
  assert_contains "standalone $arch: wrapper validado" "Wrapper de update validado:"
  assert_contains "standalone $arch: caminho do artifact" "lib/contabase-update-wrapper.sh"
done

# Negatives use the real amd64 artifact as the base.
NEG_ARCH=amd64
NEG_UNAME=x86_64
for mode in absent empty invalid symlink; do
  fixture="$TMP_ROOT/fixture-$mode"
  make_mutated_fixture "$NEG_ARCH" "$mode" "$fixture"
  helper_dir="$TMP_ROOT/helper-$mode"
  prepare_helper "$helper_dir" 0
  if run_validate "negative-$mode" "$NEG_ARCH" "$NEG_UNAME" "$fixture" "$helper_dir"; then
    not_ok "negativo $mode: validate-only aceitou wrapper invalido"
  else
    ok "negativo $mode: validate-only rejeitou wrapper invalido"
  fi
  case "$mode" in
    absent) assert_contains "negativo $mode: mensagem" "wrapper de update ausente" ;;
    empty) assert_contains "negativo $mode: mensagem" "wrapper de update ausente, vazio ou inseguro" ;;
    invalid) assert_contains "negativo $mode: mensagem" "wrapper de update invalido" ;;
    symlink) assert_contains "negativo $mode: mensagem" "symlink" ;;
  esac
done

# Checksum divergence: the archive changes after checksums.txt was generated.
fixture="$TMP_ROOT/fixture-checksum"
mkdir -p "$fixture"
cp "$DIST_DIR/contabase-linux-$NEG_ARCH.tar.gz" "$fixture/"
printf '%064d  contabase-linux-%s.tar.gz\n' 0 "$NEG_ARCH" > "$fixture/checksums.txt"
helper_dir="$TMP_ROOT/helper-checksum"
prepare_helper "$helper_dir" 0
if run_validate "negative-checksum" "$NEG_ARCH" "$NEG_UNAME" "$fixture" "$helper_dir"; then
  not_ok "negativo checksum: validate-only aceitou checksum divergente"
else
  ok "negativo checksum: validate-only rejeitou checksum divergente"
fi
assert_contains "negativo checksum: mensagem" "checksum SHA-256 divergente"

# A wrapper beside the helper (checkout) must not satisfy a legacy artifact
# that lacks the wrapper. There is no fallback to SCRIPT_DIR.
fixture="$TMP_ROOT/fixture-checkout-only"
make_mutated_fixture "$NEG_ARCH" absent "$fixture"
helper_dir="$TMP_ROOT/helper-checkout-only"
prepare_helper "$helper_dir" 1
if run_validate "negative-checkout-only" "$NEG_ARCH" "$NEG_UNAME" "$fixture" "$helper_dir"; then
  not_ok "negativo checkout-only: aceitou wrapper fora do artifact"
else
  ok "negativo checkout-only: rejeitou wrapper fora do artifact"
fi
assert_contains "negativo checkout-only: mensagem" "wrapper de update ausente"

printf '\n%d ok, %d falhas\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

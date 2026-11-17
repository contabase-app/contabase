#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/contabase-release-bootstrap-test.XXXXXX")"
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
ok() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }
not_ok() { FAIL=$((FAIL + 1)); printf 'not ok - %s\n' "$1" >&2; }

assert_eq() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then ok "$name"; else not_ok "$name (esperado=$expected obtido=$actual)"; fi
}
assert_contains() {
  local name="$1" file="$2" text="$3"
  if grep -Fq -- "$text" "$file"; then ok "$name"; else not_ok "$name"; fi
}
assert_not_contains() {
  local name="$1" file="$2" text="$3"
  if grep -Fq -- "$text" "$file"; then not_ok "$name"; else ok "$name"; fi
}

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

RELEASE="$REPO_DIR/scripts/update-contabase-release.sh"
BLOCK="$TMP_ROOT/bootstrap-block.sh"
awk '
  /^# === RELEASE_ISOLATED_BOOTSTRAP_BEGIN ===$/ { inside = 1 }
  inside { print }
  /^# === RELEASE_ISOLATED_BOOTSTRAP_END ===$/ { inside = 0 }
' "$RELEASE" > "$BLOCK"
if [ ! -s "$BLOCK" ]; then
  not_ok "bloco de bootstrap isolado ausente"
  exit 1
fi
ok "bloco de bootstrap isolado extraido"

BIN="$TMP_ROOT/bin"
FIX="$TMP_ROOT/fixtures"
mkdir -p "$BIN" "$FIX"
cat > "$BIN/curl" <<'FAKE_CURL'
#!/usr/bin/env bash
set -euo pipefail
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w|-A|--proto|--max-redirs|--max-time|--max-filesize) shift 2 ;;
    --tlsv1.2|--fail|--silent|--show-error) shift ;;
    https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
[ -n "$out" ] && [ -n "$url" ] || exit 2
case "$url" in
  */channels/beta.json|*/channels/stable.json) src="$FAKE_FIXTURES/channel.json" ;;
  */updaters/*/updater.sha256) src="$FAKE_FIXTURES/updater.sha256" ;;
  */scripts/lib/contabase-update-wrapper.sh) src="$FAKE_FIXTURES/wrapper.sh" ;;
  *) printf 'URL inesperada: %s\n' "$url" >&2; exit 7 ;;
esac
cp "$src" "$out"
printf '%s\n%s\n%s\n' "${FAKE_HTTP_STATUS:-200}" "${FAKE_CONTENT_TYPE:-application/json}" "${FAKE_EFFECTIVE_URL:-$url}"
FAKE_CURL
chmod +x "$BIN/curl"

COMMIT="$(printf 'a%.0s' $(seq 1 40))"
cat > "$FIX/wrapper.sh" <<'FAKE_WRAPPER'
#!/usr/bin/env bash
printf 'FAKE_WRAPPER_EXECUTED version=%s args=%s\n' "${1:-}" "$*"
exit 0
FAKE_WRAPPER
chmod +x "$FIX/wrapper.sh"
printf 'v1.0.0-beta.8\n' > "$FIX/VERSION"

write_updater_manifest() {
  local wrapper_hash version_hash
  wrapper_hash="$(sha_file "$FIX/wrapper.sh")"
  version_hash="$(sha_file "$FIX/VERSION")"
  {
    printf 'commit=%s\n' "$COMMIT"
    printf '%s  scripts/lib/contabase-update-wrapper.sh\n' "$wrapper_hash"
    printf '%s  VERSION\n' "$version_hash"
  } > "$FIX/updater.sha256"
}

write_channel_manifest() {
  local version="$1" updater_sha
  updater_sha="$(sha_file "$FIX/updater.sha256")"
  printf '{"schema_version":1,"channel":"beta","release_type":"beta","version":"%s","commit":"%s","updater_manifest_sha256":"%s"}\n' \
    "$version" "$COMMIT" "$updater_sha" > "$FIX/channel.json"
}

write_updater_manifest
write_channel_manifest "v1.0.0-beta.8"

run_bootstrap() {
  (
    set -euo pipefail
    export PATH="$BIN:$PATH"
    export FAKE_FIXTURES="$FIX"
    fail() { printf 'Erro: %s\n' "$*" >&2; exit 1; }
    say() { printf '%s\n' "$*"; }
    compute_sha256() { sha_file "$1"; }
    . "$BLOCK"
    BOOTSTRAP_CHANNEL_BASE="https://channel.invalid"
    BOOTSTRAP_RAW_BASE="https://raw.invalid"
    SCRIPT_DIR="${TEST_SCRIPT_DIR:-$TMP_ROOT/script-dir}"
    BUNDLE_COMMIT="${TEST_BUNDLE_COMMIT:-}"
    UPDATE_WRAPPER_PATH="${TEST_UPDATE_WRAPPER_PATH:-}"
    UPDATE_WRAPPER_SHA256="${TEST_UPDATE_WRAPPER_SHA256:-}"
    TAG="v1.0.0-beta.8"
    BOOTSTRAP_TMP_DIR=""
    bootstrap_standalone_release_wrapper "$@"
  )
}

mkdir -p "$TMP_ROOT/script-dir"
OUT="$TMP_ROOT/out"
RC=0
run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "isolado: rc" "0" "$RC"
assert_contains "isolado: mensagem de bootstrap" "$OUT" "Bootstrap isolado detectado"
assert_contains "isolado: dispatcher executado" "$OUT" "FAKE_WRAPPER_EXECUTED version=v1.0.0-beta.8"

# Reproduz o env exato do wrapper beta.7 e o argumento opcional de dry-run.
export CONTABASE_INSTALL_METHOD=release
export CONTABASE_CHANNEL=beta
export CONTABASE_VERSION=v1.0.0-beta.8
export CONTABASE_ASSUME_YES=1
RC=0
run_bootstrap --dry-run > "$OUT" 2>&1 || RC=$?
assert_eq "beta7 equivalente: rc" "0" "$RC"
assert_contains "beta7 equivalente: dispatcher recebeu dry-run" "$OUT" "v1.0.0-beta.8 --dry-run"

# Wrapper adulterado: falha antes de executar.
cp "$FIX/wrapper.sh" "$FIX/wrapper.sh.good"
printf '\n# adulterado\n' >> "$FIX/wrapper.sh"
RC=0
run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "wrapper adulterado: rc" "1" "$RC"
assert_contains "wrapper adulterado: erro" "$OUT" "wrapper diverge do updater.sha256"
assert_not_contains "wrapper adulterado: nao executa" "$OUT" "FAKE_WRAPPER_EXECUTED"
mv "$FIX/wrapper.sh.good" "$FIX/wrapper.sh"

# updater.sha256 adulterado.
cp "$FIX/updater.sha256" "$FIX/updater.sha256.good"
printf '\n' >> "$FIX/updater.sha256"
RC=0
run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "manifesto adulterado: rc" "1" "$RC"
assert_contains "manifesto adulterado: erro" "$OUT" "updater.sha256 diverge do canal"
assert_not_contains "manifesto adulterado: nao executa" "$OUT" "FAKE_WRAPPER_EXECUTED"
mv "$FIX/updater.sha256.good" "$FIX/updater.sha256"

# Canal sem identidade autenticada.
cp "$FIX/channel.json" "$FIX/channel.json.good"
printf '{"schema_version":1,"channel":"beta","release_type":"beta","version":"v1.0.0-beta.8","commit":"%s"}\n' "$COMMIT" > "$FIX/channel.json"
RC=0
run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "canal sem updater hash: rc" "1" "$RC"
assert_contains "canal sem updater hash: erro" "$OUT" "updater_manifest_sha256 invalido"
assert_not_contains "canal sem updater hash: nao executa" "$OUT" "FAKE_WRAPPER_EXECUTED"
mv "$FIX/channel.json.good" "$FIX/channel.json"

# Canal apontando para outra versao.
cp "$FIX/channel.json" "$FIX/channel.json.good"
write_channel_manifest "v1.0.0-beta.7"
RC=0
run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "canal divergente: rc" "1" "$RC"
assert_contains "canal divergente: erro" "$OUT" "nao aponta para v1.0.0-beta.8"
assert_not_contains "canal divergente: nao executa" "$OUT" "FAKE_WRAPPER_EXECUTED"
mv "$FIX/channel.json.good" "$FIX/channel.json"

# HTTP nao-200 falha fechado.
RC=0
FAKE_HTTP_STATUS=302 run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "http 302: rc" "1" "$RC"
assert_contains "http 302: erro" "$OUT" "HTTP 302"
assert_not_contains "http 302: nao executa" "$OUT" "FAKE_WRAPPER_EXECUTED"

# Content-type nao JSON no canal falha fechado.
RC=0
FAKE_CONTENT_TYPE=text/html run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "content-type html: rc" "1" "$RC"
assert_contains "content-type html: erro" "$OUT" "content-type nao JSON"
assert_not_contains "content-type html: nao executa" "$OUT" "FAKE_WRAPPER_EXECUTED"

# Caminho autenticado e checkout completo nao passam pelo bootstrap.
RC=0
TEST_BUNDLE_COMMIT="$COMMIT" run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "bundle autenticado: rc" "0" "$RC"
assert_not_contains "bundle autenticado: sem bootstrap" "$OUT" "Bootstrap isolado detectado"

mkdir -p "$TMP_ROOT/script-dir-lib/lib"
printf '#!/usr/bin/env bash\n' > "$TMP_ROOT/script-dir-lib/lib/contabase-update-wrapper.sh"
RC=0
TEST_SCRIPT_DIR="$TMP_ROOT/script-dir-lib" run_bootstrap > "$OUT" 2>&1 || RC=$?
assert_eq "checkout completo: rc" "0" "$RC"
assert_not_contains "checkout completo: sem bootstrap" "$OUT" "Bootstrap isolado detectado"

printf '\n%d ok, %d falhas\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# F3-C: authenticated update dispatcher.
# Synthetic fixtures and a local HTTP server only. No real installation,
# database, secret or external network is touched.
set -u

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
WRAPPER="$ROOT_DIR/scripts/lib/contabase-update-wrapper.sh"
TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-dispatcher.XXXXXX")"
TMP_BASE="$(cd "$TMP_BASE" && pwd -P)"
trap 'if [ -n "${SERVER_PID:-}" ]; then kill "$SERVER_PID" 2>/dev/null || true; fi; if [ "${KEEP_TMP:-0}" = "1" ]; then printf "TMP_BASE preservado: %s\n" "$TMP_BASE" >&2; else rm -rf "$TMP_BASE"; fi' EXIT

if ! command -v flock >/dev/null 2>&1; then
  printf 'FATAL: flock real obrigatorio para os testes de lock. Execute em Linux com util-linux.\n' >&2
  exit 2
fi

PASS=0
FAIL=0
SERVER_PID=""
REAL_CURL="$(command -v curl)"

assert_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL: %s (esperado=%s obtido=%s)\n' "$label" "$expected" "$actual" >&2
  fi
}

assert_contains() {
  local label="$1" needle="$2" file="$3"
  if grep -Fq -- "$needle" "$file" 2>/dev/null; then
    PASS=$((PASS + 1))
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL: %s (nao encontrado: %s em %s)\n' "$label" "$needle" "$file" >&2
  fi
}

assert_not_contains() {
  local label="$1" needle="$2" file="$3"
  if grep -Fq -- "$needle" "$file" 2>/dev/null; then
    FAIL=$((FAIL + 1))
    printf 'FAIL: %s (encontrado indevidamente: %s)\n' "$label" "$needle" >&2
  else
    PASS=$((PASS + 1))
  fi
}

assert_file_exists() {
  local label="$1" file="$2"
  if [ -f "$file" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); printf 'FAIL: %s (ausente: %s)\n' "$label" "$file" >&2; fi
}

assert_file_absent() {
  local label="$1" file="$2"
  if [ ! -e "$file" ]; then PASS=$((PASS + 1)); else FAIL=$((FAIL + 1)); printf 'FAIL: %s (presente: %s)\n' "$label" "$file" >&2; fi
}

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

write_json() {
  local file="$1"; shift
  mkdir -p "$(dirname "$file")"
  printf '%s\n' "$*" > "$file"
}

# ---------------------------------------------------------------------------
# Local HTTP fixture server.
# ---------------------------------------------------------------------------
start_server() {
  mkdir -p "$TMP_BASE/www" "$TMP_BASE/control" "$TMP_BASE/bin"
  cat > "$TMP_BASE/server.py" <<'PY'
import http.server
import os
import sys

root, control, port_file = sys.argv[1:4]

class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        path = self.path.split('?', 1)[0]
        if os.path.exists(os.path.join(control, 'redirect')):
            self.send_response(302)
            self.send_header('Location', 'https://evil.example/redirect')
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        if os.path.exists(os.path.join(control, 'html200')):
            body = b'<!DOCTYPE html><html><body>not json</body></html>\n'
            self.send_response(200)
            self.send_header('Content-Type', 'text/html; charset=utf-8')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if os.path.exists(os.path.join(control, 'status500')):
            self.send_response(500)
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        fs_path = os.path.join(root, path.lstrip('/'))
        if not os.path.isfile(fs_path):
            self.send_response(404)
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        with open(fs_path, 'rb') as source:
            body = source.read()
        if os.path.exists(os.path.join(control, 'truncate')) and '/raw/' in path:
            body = body[:max(1, len(body) // 2)]
        if path.startswith('/pages/channels/') or path.startswith('/api/'):
            ctype = 'application/json'
        else:
            ctype = 'application/octet-stream'
        self.send_response(200)
        self.send_header('Content-Type', ctype)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
with open(port_file, 'w', encoding='ascii') as handle:
    handle.write(str(server.server_address[1]))
server.serve_forever()
PY
  rm -f "$TMP_BASE/port"
  python3 "$TMP_BASE/server.py" "$TMP_BASE/www" "$TMP_BASE/control" "$TMP_BASE/port" &
  SERVER_PID=$!
  local n=0
  while [ ! -s "$TMP_BASE/port" ] && [ "$n" -lt 100 ]; do
    n=$((n + 1))
    sleep 0.05
  done
  [ -s "$TMP_BASE/port" ] || { printf 'FATAL: servidor local nao iniciou\n' >&2; exit 1; }
  local port
  port="$(cat "$TMP_BASE/port")"
  cat > "$TMP_BASE/bin/curl" <<EOF
#!/usr/bin/env bash
real_curl="$REAL_CURL"
port="$port"
out=""
url=""
orig=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    --proto|--tlsv1.2) shift; [ "\${1:-}" = "=https" ] && shift; continue ;;
    --max-redirs|--max-time|--max-filesize|-A|-H) shift; shift; continue ;;
    -o) out="\$2"; shift 2; continue ;;
    -w) shift 2; continue ;;
    --fail|--silent|--show-error) shift; continue ;;
    http*) orig="\$1"; url="\$1"; shift; continue ;;
    *) shift; continue ;;
  esac
done
printf '%s\n' "\$orig" >> "$TMP_BASE/curl.log"
case "\$url" in
  https://get-contabase.pages.dev/*) url="http://127.0.0.1:\$port/pages/\${url#https://get-contabase.pages.dev/}" ;;
  https://raw.githubusercontent.com/contabase-app/contabase/*) url="http://127.0.0.1:\$port/raw/\${url#https://raw.githubusercontent.com/contabase-app/contabase/}" ;;
  https://api.github.com/*) url="http://127.0.0.1:\$port/api/\${url#https://api.github.com/}" ;;
esac
resp="\$("\$real_curl" -sS -o "\$out" -w '%{http_code}\n%{content_type}' "\$url")" || exit \$?
printf '%s\n%s\n' "\$resp" "\$orig"
EOF
  chmod +x "$TMP_BASE/bin/curl"
  mkdir -p "$TMP_BASE/bin-noflock"
  for cmd in bash sh awk sed grep find stat mktemp install mv chmod chown rm mkdir rmdir \
             kill sleep wc sort uniq tr cat dirname basename sync id uname sha256sum shasum; do
    src="$(command -v "$cmd" 2>/dev/null)" && ln -sf "$src" "$TMP_BASE/bin-noflock/$cmd"
  done
  cp "$TMP_BASE/bin/curl" "$TMP_BASE/bin-noflock/curl"
  chmod +x "$TMP_BASE/bin-noflock/curl"
}

stop_server() {
  [ -n "$SERVER_PID" ] || return 0
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
}

# ---------------------------------------------------------------------------
# Synthetic bundle and publication fixtures.
# ---------------------------------------------------------------------------
BUNDLE_PATHS="VERSION scripts/update-contabase-source.sh scripts/update-contabase-docker.sh scripts/update-contabase-release.sh scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh scripts/lib/contabase-update-wrapper.sh scripts/lib/contabase-docker-image-update.py"

seal_bundle() {
  local dest="$1" commit="$2"
  (
    cd "$dest"
    printf 'commit=%s\n' "$commit"
    for file in $BUNDLE_PATHS; do
      printf '%s  %s\n' "$(sha_file "$file")" "$file"
    done | LC_ALL=C sort -k2,2
  ) > "$dest/updater.sha256"
}

make_fake_updater() {
  local file="$1" method="$2"
  cat > "$file" <<EOF
#!/bin/sh
{
  echo "method=$method"
  echo "version=\${CONTABASE_VERSION:-}"
  echo "commit=\${CONTABASE_UPDATER_BUNDLE_COMMIT:-}"
  echo "manifest=\${CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256:-}"
  echo "repo=\${CONTABASE_REPO_ROOT:-}"
  echo "wrapper_path=\${CONTABASE_UPDATE_WRAPPER_PATH:-}"
  echo "wrapper_sha=\${CONTABASE_UPDATE_WRAPPER_SHA256:-}"
  echo "channel=\${CONTABASE_CHANNEL:-}"
  echo "assume=\${CONTABASE_ASSUME_YES:-}"
  echo "state=\${CONTABASE_STATE_FILE:-}"
  echo "install_method=\${CONTABASE_INSTALL_METHOD:-}"
} > "\${TEST_EXEC_LOG:?}"
if [ -n "\${TEST_STARTED:-}" ]; then : > "\${TEST_STARTED}"; fi
if [ -n "\${TEST_CHILD_PID:-}" ]; then printf '%s\\n' "\$\$" > "\${TEST_CHILD_PID}"; fi
if [ "\${TEST_IGNORE_TERM:-0}" = "1" ]; then
  trap '' TERM INT
  while :; do :; done
fi
if [ -n "\${TEST_SLEEP:-}" ] && [ "\${TEST_SLEEP}" != "0" ]; then
  sleep "\${TEST_SLEEP}" 200>&- &
  SLEEP_PID=\$!
  trap 'kill "\$SLEEP_PID" 2>/dev/null || true; exit 143' TERM INT
  wait "\$SLEEP_PID"
fi
exit "\${TEST_EXEC_RC:-0}"
EOF
  chmod +x "$file"
}

setup_case() {
  local name="$1" method="$2" installed="$3" channel="$4" version="$5" commit="$6" installed_commit="${7-0000000000000000000000000000000000000000}"
  CASE="$TMP_BASE/case-$name"
  rm -rf "$CASE"
  mkdir -p "$CASE/bundle/scripts/lib" "$CASE/repo/scripts/lib" "$CASE/state" "$CASE/install"
  CASE_WRAPPER="$CASE/contabase-update"
  cp "$WRAPPER" "$CASE_WRAPPER"
  chmod 0700 "$CASE_WRAPPER"
  VERSION="$version"
  COMMIT="$commit"
  METHOD="$method"
  INSTALLED="$installed"
  CHANNEL="$channel"

  printf '%s\n' "$version" > "$CASE/bundle/VERSION"
  make_fake_updater "$CASE/bundle/scripts/update-contabase-source.sh" source
  make_fake_updater "$CASE/bundle/scripts/update-contabase-docker.sh" docker
  make_fake_updater "$CASE/bundle/scripts/update-contabase-release.sh" release
  printf '#!/bin/sh\n' > "$CASE/bundle/scripts/lib/contabase-install-state.sh"
  printf '#!/bin/sh\n' > "$CASE/bundle/scripts/lib/contabase-recovery.sh"
  printf '#!/bin/sh\necho verified-wrapper\n' > "$CASE/bundle/scripts/lib/contabase-update-wrapper.sh"
  printf '#!/usr/bin/env python3\n' > "$CASE/bundle/scripts/lib/contabase-docker-image-update.py"
  chmod +x "$CASE/bundle/scripts/"*.sh
  seal_bundle "$CASE/bundle" "$commit"
  BUNDLE_SHA="$(sha_file "$CASE/bundle/updater.sha256")"

  mkdir -p "$TMP_BASE/www/pages/updaters/$version" "$TMP_BASE/www/raw/$commit"
  cp "$CASE/bundle/updater.sha256" "$TMP_BASE/www/pages/updaters/$version/updater.sha256"
  (
    cd "$CASE/bundle"
    for file in $BUNDLE_PATHS; do
      mkdir -p "$TMP_BASE/www/raw/$commit/$(dirname "$file")"
      cp "$file" "$TMP_BASE/www/raw/$commit/$file"
    done
  )

  write_json "$TMP_BASE/www/pages/channels/beta.json" \
    "{\"schema_version\":1,\"channel\":\"beta\",\"version\":\"$version\",\"release_type\":\"beta\",\"published_at\":\"2026-11-08T00:00:00Z\",\"commit\":\"$commit\",\"updater_manifest_sha256\":\"$BUNDLE_SHA\"}"
  write_json "$TMP_BASE/www/pages/channels/stable.json" \
    '{"schema_version":1,"channel":"stable","version":null,"release_type":"stable","published_at":null,"message":"Nenhuma release stable publicada ainda."}'
  write_json "$TMP_BASE/www/api/repos/contabase-app/contabase/releases/tags/$version" \
    "{\"tag_name\":\"$version\",\"draft\":false,\"prerelease\":true,\"published_at\":\"2026-11-08T00:00:00Z\"}"
  write_json "$TMP_BASE/www/api/repos/contabase-app/contabase/git/ref/tags/$version" \
    "{\"ref\":\"refs/tags/$version\",\"object\":{\"type\":\"commit\",\"sha\":\"$commit\"}}"
  mkdir -p "$TMP_BASE/www/api/repos/contabase-app/contabase/commits"
  printf '%s\n' "$commit" > "$TMP_BASE/www/api/repos/contabase-app/contabase/commits/$version"

  printf '%s\n' "$installed" > "$CASE/repo/VERSION"
  printf '%s\n' "$installed" > "$CASE/install/VERSION"
  case "$method" in
    source|docker)
      cat > "$CASE/state/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=$method
CONTABASE_CHANNEL=$channel
CONTABASE_INSTALLED_VERSION=$installed
CONTABASE_REPO_PATH=$CASE/repo
CONTABASE_INSTALL_REF_KIND=tag
CONTABASE_INSTALL_REF=$installed
CONTABASE_INSTALL_COMMIT=$installed_commit
CONTABASE_INSTALL_ORIGIN=https://github.com/contabase-app/contabase.git
EOF
      ;;
    release)
      cat > "$CASE/state/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=release
CONTABASE_CHANNEL=$channel
CONTABASE_INSTALLED_VERSION=$installed
CONTABASE_REPO_PATH=
CONTABASE_COMMIT=$installed_commit
EOF
      ;;
    docker-image)
      cat > "$CASE/state/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=docker-image
CONTABASE_CHANNEL=$channel
CONTABASE_INSTALLED_VERSION=$installed
CONTABASE_REPO_PATH=$CASE/repo
CONTABASE_COMMIT=$installed_commit
CONTABASE_IMAGE_INDEX_DIGEST=sha256:0000000000000000000000000000000000000000000000000000000000000000
CONTABASE_IMAGE_PLATFORM_DIGEST=sha256:0000000000000000000000000000000000000000000000000000000000000000
CONTABASE_IMAGE_PLATFORM=linux/amd64
CONTABASE_IMAGE_REFERENCE=ghcr.io/contabase-app/contabase@sha256:0000000000000000000000000000000000000000000000000000000000000000
CONTABASE_IMAGE_PROOF_SOURCE=bootstrap-catalog
CONTABASE_COMPOSE_IMAGE_FILE=docker-compose.image.yml
EOF
      cat > "$CASE/contabase-docker-image-update.py" <<'PY'
#!/usr/bin/env python3
import os
import sys
log = os.environ.get('TEST_EXEC_LOG')
args = sys.argv[1:]
if log:
    with open(log, 'w', encoding='utf-8') as handle:
        handle.write('method=docker-image\n')
        handle.write('args=' + ' '.join(args) + '\n')
        handle.write('assume=' + os.environ.get('CONTABASE_ASSUME_YES', '') + '\n')
version = next((item for item in args if item.startswith('v')), None)
state = os.environ.get('CONTABASE_STATE_FILE')
if state and version:
    with open(state, encoding='utf-8') as source:
        lines = source.read().splitlines()
    updated = []
    for line in lines:
        if line.startswith('CONTABASE_INSTALLED_VERSION='):
            updated.append('CONTABASE_INSTALLED_VERSION=' + version)
        elif line.startswith('CONTABASE_COMMIT='):
            updated.append('CONTABASE_COMMIT=' + os.environ.get('TEST_DOCKER_IMAGE_COMMIT', line.split('=', 1)[1]))
        else:
            updated.append(line)
    if not any(line.startswith('CONTABASE_COMMIT=') for line in updated):
        updated.append('CONTABASE_COMMIT=' + os.environ.get('TEST_DOCKER_IMAGE_COMMIT', ''))
    if not any(line.startswith('CONTABASE_IMAGE_INDEX_DIGEST=') for line in updated):
        updated.append('CONTABASE_IMAGE_INDEX_DIGEST=sha256:' + ('1' * 64))
    with open(state + '.tmp', 'w', encoding='utf-8') as handle:
        handle.write('\n'.join(updated) + '\n')
    os.replace(state + '.tmp', state)
sys.exit(0)
PY
      chmod +x "$CASE/contabase-docker-image-update.py"
      ;;
  esac
  rm -f "$CASE/exec.log" "$CASE/out.log" "$CASE/started" "$CASE/installed-update"
}

publish_extra_version() {
  local version="$1" commit="$2" extra sha file
  extra="$TMP_BASE/extra-$version"
  rm -rf "$extra"
  cp -R "$CASE/bundle" "$extra"
  printf '%s\n' "$version" > "$extra/VERSION"
  seal_bundle "$extra" "$commit"
  sha="$(sha_file "$extra/updater.sha256")"
  mkdir -p "$TMP_BASE/www/pages/updaters/$version" "$TMP_BASE/www/raw/$commit"
  cp "$extra/updater.sha256" "$TMP_BASE/www/pages/updaters/$version/updater.sha256"
  for file in $BUNDLE_PATHS; do
    mkdir -p "$TMP_BASE/www/raw/$commit/$(dirname "$file")"
    cp "$extra/$file" "$TMP_BASE/www/raw/$commit/$file"
  done
  write_json "$TMP_BASE/www/api/repos/contabase-app/contabase/releases/tags/$version" \
    "{\"tag_name\":\"$version\",\"draft\":false,\"prerelease\":true,\"published_at\":\"2026-11-08T00:00:00Z\"}"
  write_json "$TMP_BASE/www/api/repos/contabase-app/contabase/git/ref/tags/$version" \
    "{\"ref\":\"refs/tags/$version\",\"object\":{\"type\":\"commit\",\"sha\":\"$commit\"}}"
  mkdir -p "$TMP_BASE/www/api/repos/contabase-app/contabase/commits"
  printf '%s\n' "$commit" > "$TMP_BASE/www/api/repos/contabase-app/contabase/commits/$version"
  EXTRA_BUNDLE_SHA="$sha"
  EXTRA_BUNDLE_VERSION="$version"
  EXTRA_BUNDLE_COMMIT="$commit"
}

run_case() {
  local case_dir="$1"; shift
  local rc=0
  (
    cd "$case_dir/repo" 2>/dev/null || cd "$case_dir"
    if [ "${TEST_PATH_ONLY:-0}" = "1" ]; then
      export PATH="${TEST_PATH_PREFIX:?}"
    else
      export PATH="${TEST_PATH_PREFIX:-$TMP_BASE/bin}:$PATH"
    fi
    export CONTABASE_STATE_FILE="$case_dir/state/install-state.env"
    export CONTABASE_UPDATE_WRAPPER="$case_dir/installed-update"
    export CONTABASE_UPDATE_ALIAS="$case_dir/cb-update"
    export CONTABASE_INSTALL_DIR="$case_dir/install"
    export CONTABASE_MODE_FILE="$case_dir/state/install-mode"
    export CONTABASE_ENV_FILE="$case_dir/state/contabase.env"
    export TEST_EXEC_LOG="$case_dir/exec.log"
    export TEST_STARTED="$case_dir/started"
    export TEST_CHILD_PID="${TEST_CHILD_PID:-}"
    export TEST_SLEEP="${TEST_SLEEP:-0}"
    export TEST_IGNORE_TERM="${TEST_IGNORE_TERM:-0}"
    bash "${CASE_WRAPPER:-$WRAPPER}" "$@"
  ) > "$case_dir/out.log" 2>&1 || rc=$?
  printf '%s' "$rc"
}

run_case_env() {
  local case_dir="$1"; shift
  local env_args=()
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
    env_args+=("$1")
    shift
  done
  [ "${1:-}" = "--" ] && shift
  local rc=0
  (
    cd "$case_dir/repo" 2>/dev/null || cd "$case_dir"
    if [ "${TEST_PATH_ONLY:-0}" = "1" ]; then
      export PATH="${TEST_PATH_PREFIX:?}"
    else
      export PATH="${TEST_PATH_PREFIX:-$TMP_BASE/bin}:$PATH"
    fi
    export CONTABASE_STATE_FILE="$case_dir/state/install-state.env"
    export CONTABASE_UPDATE_WRAPPER="$case_dir/installed-update"
    export CONTABASE_UPDATE_ALIAS="$case_dir/cb-update"
    export CONTABASE_INSTALL_DIR="$case_dir/install"
    export CONTABASE_MODE_FILE="$case_dir/state/install-mode"
    export CONTABASE_ENV_FILE="$case_dir/state/contabase.env"
    export TEST_EXEC_LOG="$case_dir/exec.log"
    export TEST_STARTED="$case_dir/started"
    export TEST_CHILD_PID="${TEST_CHILD_PID:-}"
    export TEST_SLEEP="${TEST_SLEEP:-0}"
    export TEST_IGNORE_TERM="${TEST_IGNORE_TERM:-0}"
    env "${env_args[@]}" bash "${CASE_WRAPPER:-$WRAPPER}" "$@"
  ) > "$case_dir/out.log" 2>&1 || rc=$?
  printf '%s' "$rc"
}

# ---------------------------------------------------------------------------
# Fixture lifecycle.
# ---------------------------------------------------------------------------
start_server

B8=8888888888888888888888888888888888888888
case "$(uname -m)" in
  x86_64|amd64) HOST_ARCH=amd64; OTHER_ARCH=arm64 ;;
  aarch64|arm64) HOST_ARCH=arm64; OTHER_ARCH=amd64 ;;
  *) HOST_ARCH=amd64; OTHER_ARCH=arm64 ;;
esac

# 1. Source beta: explicit version, authenticated bundle, dispatch and wrapper.
setup_case source-beta source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mkdir -p "$CASE/repo/scripts/lib"
printf '#!/bin/sh\necho tampered-checkout-wrapper\n' > "$CASE/repo/scripts/lib/contabase-update-wrapper.sh"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "source beta rc" "0" "$rc"
assert_contains "source beta method" "method=source" "$CASE/exec.log"
assert_contains "source beta version" "version=v1.0.0-beta.8" "$CASE/exec.log"
assert_contains "source beta commit" "commit=$B8" "$CASE/exec.log"
assert_contains "source beta manifest" "manifest=$BUNDLE_SHA" "$CASE/exec.log"
assert_contains "source beta repo" "repo=$CASE/repo" "$CASE/exec.log"
assert_contains "source beta channel" "channel=beta" "$CASE/exec.log"
assert_contains "source beta install method" "install_method=source" "$CASE/exec.log"
assert_eq "source beta wrapper" "$(sha_file "$CASE/bundle/scripts/lib/contabase-update-wrapper.sh")" "$(sha_file "$CASE/installed-update")"
assert_not_contains "source beta ignores checkout wrapper" "tampered-checkout-wrapper" "$CASE/installed-update"
assert_not_contains "source beta official channel only" "evil.example" "$TMP_BASE/curl.log"
assert_contains "source beta pages origin" "https://get-contabase.pages.dev/channels/beta.json" "$TMP_BASE/curl.log"
assert_contains "source beta raw origin" "https://raw.githubusercontent.com/contabase-app/contabase/$B8/scripts/update-contabase-source.sh" "$TMP_BASE/curl.log"
assert_contains "source beta api origin" "https://api.github.com/repos/contabase-app/contabase/releases/tags/v1.0.0-beta.8" "$TMP_BASE/curl.log"
assert_file_absent "source beta no legacy lock" "$CASE/state/contabase-update.lock.d"
assert_file_exists "source beta flock file" "$CASE/state/contabase-update.lock"
assert_file_absent "source beta no wrapper pending" "$CASE/state/wrapper.pending"

# 1.1 Immediate success: child exits before process_start can observe it.
setup_case immediate-success source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case_env "$CASE" TEST_SLEEP=0 TEST_EXEC_RC=0 -- v1.0.0-beta.8)"
assert_eq "immediate success rc" "0" "$rc"
assert_contains "immediate success method" "method=source" "$CASE/exec.log"
assert_eq "immediate success wrapper" "$(sha_file "$CASE/bundle/scripts/lib/contabase-update-wrapper.sh")" "$(sha_file "$CASE/installed-update")"
assert_file_absent "immediate success no wrapper pending" "$CASE/state/wrapper.pending"

# 1.2 Immediate failure: the real child status must propagate.
setup_case immediate-failure source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case_env "$CASE" TEST_SLEEP=0 TEST_EXEC_RC=7 -- v1.0.0-beta.8)"
assert_eq "immediate failure rc" "7" "$rc"
assert_contains "immediate failure method" "method=source" "$CASE/exec.log"
assert_file_absent "immediate failure no wrapper" "$CASE/installed-update"
assert_file_absent "immediate failure no wrapper pending" "$CASE/state/wrapper.pending"

# 2. Channel resolution without explicit version.
setup_case source-channel source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case "$CASE")"
assert_eq "source channel rc" "0" "$rc"
assert_contains "source channel method" "method=source" "$CASE/exec.log"
assert_contains "source channel version" "version=v1.0.0-beta.8" "$CASE/exec.log"

# 3. Stable without release: no fallback to beta.
setup_case stable-null release v1.0.0-beta.7 stable v1.0.0 "$B8"
rc="$(run_case "$CASE" --channel stable)"
assert_eq "stable null rc" "1" "$rc"
assert_contains "stable null diagnostic" "canal stable ainda nao possui versao publicada" "$CASE/out.log"
assert_file_absent "stable null no exec" "$CASE/exec.log"

# 4. Pinned explicit version equal to the authorized manifest.
setup_case pinned release v1.0.0-beta.7 pinned v1.0.0-beta.8 "$B8"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "pinned rc" "0" "$rc"
assert_contains "pinned method" "method=release" "$CASE/exec.log"
assert_contains "pinned wrapper path" "/scripts/lib/contabase-update-wrapper.sh" "$CASE/exec.log"
assert_contains "pinned wrapper sha" "wrapper_sha=$(sha_file "$CASE/bundle/scripts/lib/contabase-update-wrapper.sh")" "$CASE/exec.log"
assert_contains "pinned preserves channel" "channel=pinned" "$CASE/exec.log"

# 5. Equal version: no network, no execution.
setup_case equal source v1.0.0-beta.8 beta v1.0.0-beta.8 "$B8" ""
: > "$TMP_BASE/curl.log"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "equal rc" "0" "$rc"
assert_contains "equal diagnostic" "nenhuma atualizacao necessaria" "$CASE/out.log"
assert_file_absent "equal no exec" "$CASE/exec.log"
assert_eq "equal no network" "" "$(cat "$TMP_BASE/curl.log")"

# 5.1 Equal version with an installed commit: revalidate the authorized commit.
setup_case equal-commit source v1.0.0-beta.8 beta v1.0.0-beta.8 "$B8" "$B8"
: > "$TMP_BASE/curl.log"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "equal commit rc" "0" "$rc"
assert_contains "equal commit diagnostic" "commit confirmado" "$CASE/out.log"
assert_file_absent "equal commit no exec" "$CASE/exec.log"
assert_contains "equal commit manifest consulted" "https://get-contabase.pages.dev/channels/beta.json" "$TMP_BASE/curl.log"

# 5.2 Equal version with a divergent installed commit: fail closed.
setup_case equal-commit-divergent source v1.0.0-beta.8 beta v1.0.0-beta.8 "$B8" "1234567890123456789012345678901234567890"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "equal commit divergent rc" "1" "$rc"
assert_contains "equal commit divergent diagnostic" "commit instalado" "$CASE/out.log"
assert_file_absent "equal commit divergent no exec" "$CASE/exec.log"

# 6. Equal version with finalize.pending must retry, not short-circuit.
setup_case equal-finalize source v1.0.0-beta.8 beta v1.0.0-beta.8 "$B8"
cat > "$CASE/state/finalize.pending" <<EOF
schema=1
method=source
target_version=v1.0.0-beta.8
target_kind=tag
target_ref=v1.0.0-beta.8
target_commit=$B8
EOF
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "equal finalize rc" "0" "$rc"
assert_contains "equal finalize dispatched" "method=source" "$CASE/exec.log"

# 7. Equal version with transition.pending must retry.
setup_case equal-journal source v1.0.0-beta.8 beta v1.0.0-beta.8 "$B8"
cat > "$CASE/state/transition.pending" <<EOF
schema=1
method=source
target_version=v1.0.0-beta.8
target_kind=tag
target_ref=v1.0.0-beta.8
target_commit=$B8
EOF
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "equal journal rc" "0" "$rc"
assert_contains "equal journal dispatched" "method=source" "$CASE/exec.log"

# 8. State/checkout divergence must retry.
setup_case equal-interrupted source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf 'v1.0.0-beta.8\n' > "$CASE/repo/VERSION"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "interrupted rc" "0" "$rc"
assert_contains "interrupted dispatched" "method=source" "$CASE/exec.log"

# 9. Downgrade and invalid versions.
setup_case downgrade source v1.0.0-beta.8 beta v1.0.0-beta.9 "$B8"
rc="$(run_case "$CASE" v1.0.0-beta.7)"
assert_eq "downgrade rc" "1" "$rc"
assert_contains "downgrade diagnostic" "Downgrade recusado" "$CASE/out.log"
assert_file_absent "downgrade no exec" "$CASE/exec.log"
setup_case invalid source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case "$CASE" latest)"
assert_eq "invalid rc" "1" "$rc"
assert_file_absent "invalid no exec" "$CASE/exec.log"

# 10. Manifest absent, HTML 200, malformed and duplicated.
setup_case missing-manifest source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rm -f "$TMP_BASE/www/pages/channels/beta.json"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "missing manifest rc" "1" "$rc"
assert_contains "missing manifest diagnostic" "resposta HTTP 404" "$CASE/out.log"
assert_file_absent "missing manifest no exec" "$CASE/exec.log"

setup_case html-manifest source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
: > "$TMP_BASE/control/html200"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
rm -f "$TMP_BASE/control/html200"
assert_eq "html manifest rc" "1" "$rc"
assert_file_absent "html manifest no exec" "$CASE/exec.log"

setup_case malformed-manifest source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf '{not-json\n' > "$TMP_BASE/www/pages/channels/beta.json"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "malformed manifest rc" "1" "$rc"
assert_file_absent "malformed manifest no exec" "$CASE/exec.log"

setup_case duplicate-manifest source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf '{"schema_version":1,"schema_version":1,"channel":"beta","version":"v1.0.0-beta.8","release_type":"beta","published_at":"2026-11-08T00:00:00Z","commit":"%s","updater_manifest_sha256":"%s"}\n' "$B8" "$BUNDLE_SHA" > "$TMP_BASE/www/pages/channels/beta.json"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "duplicate manifest rc" "1" "$rc"
assert_file_absent "duplicate manifest no exec" "$CASE/exec.log"

# 11. Channel/version/schema divergences.
setup_case wrong-channel source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf '{"schema_version":1,"channel":"stable","version":"v1.0.0-beta.8","release_type":"beta","published_at":"2026-11-08T00:00:00Z","commit":"%s","updater_manifest_sha256":"%s"}\n' "$B8" "$BUNDLE_SHA" > "$TMP_BASE/www/pages/channels/beta.json"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "wrong channel rc" "1" "$rc"
assert_file_absent "wrong channel no exec" "$CASE/exec.log"

setup_case wrong-version-type source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf '{"schema_version":1,"channel":"beta","version":"v1.0.0","release_type":"beta","published_at":"2026-11-08T00:00:00Z","commit":"%s","updater_manifest_sha256":"%s"}\n' "$B8" "$BUNDLE_SHA" > "$TMP_BASE/www/pages/channels/beta.json"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "wrong version type rc" "1" "$rc"
assert_file_absent "wrong version type no exec" "$CASE/exec.log"

setup_case beta8-not-published source v1.0.0-beta.7 beta v1.0.0-beta.7 "$B8"
printf '{"schema_version":1,"channel":"beta","version":"v1.0.0-beta.7","release_type":"beta","published_at":"2026-11-01T00:00:00Z"}\n' > "$TMP_BASE/www/pages/channels/beta.json"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "beta8 not published rc" "1" "$rc"
assert_contains "beta8 not published diagnostic" "commit" "$CASE/out.log"
assert_file_absent "beta8 not published no exec" "$CASE/exec.log"

# 12. GitHub identity divergences.
setup_case draft-release source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf '{"tag_name":"v1.0.0-beta.8","draft":true,"prerelease":true,"published_at":"2026-11-08T00:00:00Z"}\n' > "$TMP_BASE/www/api/repos/contabase-app/contabase/releases/tags/v1.0.0-beta.8"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "draft release rc" "1" "$rc"
assert_contains "draft release diagnostic" "draft" "$CASE/out.log"
assert_file_absent "draft release no exec" "$CASE/exec.log"

setup_case prerelease-mismatch source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf '{"tag_name":"v1.0.0-beta.8","draft":false,"prerelease":false,"published_at":"2026-11-08T00:00:00Z"}\n' > "$TMP_BASE/www/api/repos/contabase-app/contabase/releases/tags/v1.0.0-beta.8"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "prerelease mismatch rc" "1" "$rc"
assert_contains "prerelease mismatch diagnostic" "prerelease" "$CASE/out.log"
assert_file_absent "prerelease mismatch no exec" "$CASE/exec.log"

setup_case commit-mismatch source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf '9999999999999999999999999999999999999999\n' > "$TMP_BASE/www/api/repos/contabase-app/contabase/commits/v1.0.0-beta.8"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "commit mismatch rc" "1" "$rc"
assert_contains "commit mismatch diagnostic" "commit da tag" "$CASE/out.log"
assert_file_absent "commit mismatch no exec" "$CASE/exec.log"

# 13. Updater manifest digest and bundle bytes.
setup_case manifest-digest source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf 'commit=%s\n' "$B8" > "$TMP_BASE/www/pages/updaters/v1.0.0-beta.8/updater.sha256"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "manifest digest rc" "1" "$rc"
assert_contains "manifest digest diagnostic" "manifesto diverge" "$CASE/out.log"
assert_file_absent "manifest digest no exec" "$CASE/exec.log"

setup_case tampered-bundle source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
printf '\n# tampered\n' >> "$TMP_BASE/www/raw/$B8/scripts/update-contabase-source.sh"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tampered bundle rc" "1" "$rc"
assert_contains "tampered bundle diagnostic" "conteudo divergente no bundle" "$CASE/out.log"
assert_file_absent "tampered bundle no exec" "$CASE/exec.log"

# 14. Redirect, 500 and partial download.
setup_case redirect source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
: > "$TMP_BASE/control/redirect"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
rm -f "$TMP_BASE/control/redirect"
assert_eq "redirect rc" "1" "$rc"
assert_contains "redirect diagnostic" "resposta HTTP 302" "$CASE/out.log"
assert_file_absent "redirect no exec" "$CASE/exec.log"

setup_case network500 source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
: > "$TMP_BASE/control/status500"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
rm -f "$TMP_BASE/control/status500"
assert_eq "network 500 rc" "1" "$rc"
assert_contains "network 500 diagnostic" "resposta HTTP 500" "$CASE/out.log"
assert_file_absent "network 500 no exec" "$CASE/exec.log"

setup_case partial source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
: > "$TMP_BASE/control/truncate"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
rm -f "$TMP_BASE/control/truncate"
assert_eq "partial download rc" "1" "$rc"
assert_contains "partial download diagnostic" "bundle" "$CASE/out.log"
assert_file_absent "partial download no exec" "$CASE/exec.log"

# 15. Malicious environment and alternative origins.
setup_case malicious-env source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
: > "$TMP_BASE/curl.log"
rc="$(run_case_env "$CASE" CONTABASE_CHANNEL_BASE=https://evil.example -- v1.0.0-beta.8)"
assert_eq "malicious channel base rc" "1" "$rc"
assert_contains "malicious channel base diagnostic" "CONTABASE_CHANNEL_BASE" "$CASE/out.log"
assert_eq "malicious channel base no network" "" "$(cat "$TMP_BASE/curl.log")"
assert_file_absent "malicious channel base no exec" "$CASE/exec.log"

setup_case malicious-raw source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
: > "$TMP_BASE/curl.log"
rc="$(run_case_env "$CASE" CONTABASE_RAW_BASE=https://evil.example -- v1.0.0-beta.8)"
assert_eq "malicious raw base rc" "1" "$rc"
assert_contains "malicious raw base diagnostic" "CONTABASE_RAW_BASE" "$CASE/out.log"
assert_eq "malicious raw base no network" "" "$(cat "$TMP_BASE/curl.log")"
assert_file_absent "malicious raw base no exec" "$CASE/exec.log"

setup_case malicious-insecure source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case_env "$CASE" CONTABASE_ALLOW_INSECURE_CHANNEL=1 -- v1.0.0-beta.8)"
assert_eq "malicious insecure rc" "1" "$rc"
assert_contains "malicious insecure diagnostic" "CONTABASE_ALLOW_INSECURE_CHANNEL" "$CASE/out.log"
assert_file_absent "malicious insecure no exec" "$CASE/exec.log"

# 15.1 Pinned version authorized by the catalog (newer than the manifest).
setup_case pinned-catalog source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
B9=9999999999999999999999999999999999999999
publish_extra_version v1.0.0-beta.9 "$B9"
write_json "$TMP_BASE/www/pages/channels/releases-v1.json" \
  "{\"schema_version\":1,\"releases\":[{\"version\":\"v1.0.0-beta.9\",\"channel\":\"beta\",\"commit\":\"$B9\",\"published_at\":\"2026-11-08T00:00:00Z\",\"state\":\"homologated\",\"methods\":{\"source\":{\"$HOST_ARCH\":{\"payload_sha256\":\"$(printf 'a%.0s' {1..64})\",\"evidence_id\":\"fixture\"}}},\"updater_manifest_sha256\":\"$EXTRA_BUNDLE_SHA\"}]}"
rc="$(run_case "$CASE" v1.0.0-beta.9)"
assert_eq "pinned catalog rc" "0" "$rc"
assert_contains "pinned catalog version" "version=v1.0.0-beta.9" "$CASE/exec.log"
assert_contains "pinned catalog commit" "commit=$B9" "$CASE/exec.log"

# 15.1b Pinned catalog with a version homologated only for another architecture.
setup_case pinned-catalog-wrong-arch source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
B9=9999999999999999999999999999999999999999
publish_extra_version v1.0.0-beta.9 "$B9"
write_json "$TMP_BASE/www/pages/channels/releases-v1.json" \
  "{\"schema_version\":1,\"releases\":[{\"version\":\"v1.0.0-beta.9\",\"channel\":\"beta\",\"commit\":\"$B9\",\"published_at\":\"2026-11-08T00:00:00Z\",\"state\":\"homologated\",\"methods\":{\"source\":{\"$OTHER_ARCH\":{\"payload_sha256\":\"$(printf 'a%.0s' {1..64})\",\"evidence_id\":\"fixture\"}}},\"updater_manifest_sha256\":\"$EXTRA_BUNDLE_SHA\"}]}"
rc="$(run_case "$CASE" v1.0.0-beta.9)"
assert_eq "pinned catalog wrong arch rc" "1" "$rc"
assert_contains "pinned catalog wrong arch diagnostic" "arquitetura" "$CASE/out.log"
assert_file_absent "pinned catalog wrong arch no exec" "$CASE/exec.log"

# 15.1c Pinned catalog with a version homologated only for another method.
setup_case pinned-catalog-wrong-method source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
B9=9999999999999999999999999999999999999999
publish_extra_version v1.0.0-beta.9 "$B9"
write_json "$TMP_BASE/www/pages/channels/releases-v1.json" \
  "{\"schema_version\":1,\"releases\":[{\"version\":\"v1.0.0-beta.9\",\"channel\":\"beta\",\"commit\":\"$B9\",\"published_at\":\"2026-11-08T00:00:00Z\",\"state\":\"homologated\",\"methods\":{\"docker\":{\"$HOST_ARCH\":{\"payload_sha256\":\"$(printf 'a%.0s' {1..64})\",\"evidence_id\":\"fixture\"}}},\"updater_manifest_sha256\":\"$EXTRA_BUNDLE_SHA\"}]}"
rc="$(run_case "$CASE" v1.0.0-beta.9)"
assert_eq "pinned catalog wrong method rc" "1" "$rc"
assert_contains "pinned catalog wrong method diagnostic" "metodo" "$CASE/out.log"
assert_file_absent "pinned catalog wrong method no exec" "$CASE/exec.log"

# 15.2 Pinned version absent from a non-empty catalog.
setup_case pinned-not-catalog source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
B9=9999999999999999999999999999999999999999
publish_extra_version v1.0.0-beta.9 "$B9"
write_json "$TMP_BASE/www/pages/channels/releases-v1.json" \
  "{\"schema_version\":1,\"releases\":[{\"version\":\"v1.0.0-beta.9\",\"channel\":\"beta\",\"commit\":\"$B9\",\"published_at\":\"2026-11-08T00:00:00Z\",\"state\":\"homologated\",\"methods\":{\"source\":{\"$HOST_ARCH\":{\"payload_sha256\":\"$(printf 'a%.0s' {1..64})\",\"evidence_id\":\"fixture\"}}},\"updater_manifest_sha256\":\"$EXTRA_BUNDLE_SHA\"}]}"
rc="$(run_case "$CASE" v1.0.0-beta.10)"
assert_eq "pinned not catalog rc" "1" "$rc"
assert_contains "pinned not catalog diagnostic" "nao autorizada pelo catalogo" "$CASE/out.log"
assert_file_absent "pinned not catalog no exec" "$CASE/exec.log"

# 15.3 Pinned stable with stable manifest null.
setup_case pinned-stable source v1.0.0-beta.7 pinned v1.0.0-beta.8 "$B8"
rc="$(run_case "$CASE" v1.0.0)"
assert_eq "pinned stable null rc" "1" "$rc"
assert_contains "pinned stable null diagnostic" "canal stable ainda nao possui versao publicada" "$CASE/out.log"
assert_file_absent "pinned stable null no exec" "$CASE/exec.log"

# 15.4 Tag reference divergence.
setup_case tag-ref source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
write_json "$TMP_BASE/www/api/repos/contabase-app/contabase/git/ref/tags/v1.0.0-beta.8" \
  '{"ref":"refs/tags/v1.0.0-beta.7","object":{"type":"commit","sha":"'"$B8"'"}}'
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag ref rc" "1" "$rc"
assert_contains "tag ref diagnostic" "referencia oficial nao e a tag" "$CASE/out.log"
assert_file_absent "tag ref no exec" "$CASE/exec.log"

# 16. Legacy Beta 7 state without CONTABASE_STATE_VERSION.
setup_case legacy source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
cat > "$CASE/state/install-state.env" <<EOF
CONTABASE_INSTALL_METHOD=source
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
CONTABASE_REPO_PATH=$CASE/repo
EOF
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "legacy state rc" "0" "$rc"
assert_contains "legacy state dispatched" "method=source" "$CASE/exec.log"

# 17. Docker method.
setup_case docker docker v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "docker rc" "0" "$rc"
assert_contains "docker method" "method=docker" "$CASE/exec.log"
assert_contains "docker repo" "repo=$CASE/repo" "$CASE/exec.log"

# 18. Docker-image early dispatch and wrapper backfill.
setup_case docker-image docker-image v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case_env "$CASE" TEST_DOCKER_IMAGE_COMMIT="$B8" -- v1.0.0-beta.8)"
assert_eq "docker-image rc" "0" "$rc"
assert_contains "docker-image method" "method=docker-image" "$CASE/exec.log"
assert_contains "docker-image args version" "v1.0.0-beta.8" "$CASE/exec.log"
assert_contains "docker-image args channel" "--channel beta" "$CASE/exec.log"
assert_contains "docker-image state arg" "--state-file $CASE/state/install-state.env" "$CASE/exec.log"

setup_case docker-image-backfill docker-image v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case_env "$CASE" TEST_DOCKER_IMAGE_WRITE_STATE=1 TEST_DOCKER_IMAGE_COMMIT="$B8" -- v1.0.0-beta.8)"
assert_eq "docker-image backfill rc" "0" "$rc"
assert_eq "docker-image backfill wrapper" "$(sha_file "$CASE/bundle/scripts/lib/contabase-update-wrapper.sh")" "$(sha_file "$CASE/installed-update")"
assert_file_absent "docker-image backfill no marker" "$CASE/state/wrapper.pending"

# 19. Insufficient permissions fail before network/exec.
setup_case no-write source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
chmod 0500 "$CASE/state"
: > "$TMP_BASE/curl.log"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
chmod 0700 "$CASE/state"
assert_eq "no-write rc" "1" "$rc"
assert_contains "no-write diagnostic" "sem permissao de escrita" "$CASE/out.log"
assert_eq "no-write no network" "" "$(cat "$TMP_BASE/curl.log")"
assert_file_absent "no-write no exec" "$CASE/exec.log"

# 20. Concurrent dispatchers: second must fail on the global lock.
setup_case lock source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
(
  cd "$CASE/repo"
  export PATH="$TMP_BASE/bin:$PATH"
  export CONTABASE_STATE_FILE="$CASE/state/install-state.env"
  export CONTABASE_UPDATE_WRAPPER="$CASE/installed-update"
  export CONTABASE_UPDATE_ALIAS="$CASE/cb-update"
  export CONTABASE_INSTALL_DIR="$CASE/install"
  export TEST_EXEC_LOG="$CASE/exec.log"
  export TEST_STARTED="$CASE/started"
  export TEST_SLEEP=2
  bash "$WRAPPER" v1.0.0-beta.8
) > "$CASE/first.log" 2>&1 &
FIRST_PID=$!
n=0
while [ ! -e "$CASE/started" ] && [ "$n" -lt 100 ]; do n=$((n + 1)); sleep 0.05; done
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "lock second rc" "1" "$rc"
assert_contains "lock second diagnostic" "outra atualizacao esta em curso" "$CASE/out.log"
first_rc=0
wait "$FIRST_PID" 2>/dev/null || first_rc=$?
assert_eq "lock first rc" "0" "$first_rc"
assert_file_absent "lock released" "$CASE/state/contabase-update.lock.d"

# 21. Wrapper backfill failure writes a marker and retry succeeds.
setup_case wrapper-retry source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mkdir -p "$CASE/installed-update"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "wrapper retry first rc" "1" "$rc"
assert_contains "wrapper retry first diagnostic" "backfill do wrapper falhou" "$CASE/out.log"
assert_file_exists "wrapper retry marker" "$CASE/state/wrapper.pending"
assert_contains "wrapper retry first exec" "method=source" "$CASE/exec.log"
rmdir "$CASE/installed-update"
printf 'v1.0.0-beta.8\n' > "$CASE/repo/VERSION"
sed -i.bak 's/^CONTABASE_INSTALLED_VERSION=.*/CONTABASE_INSTALLED_VERSION=v1.0.0-beta.8/' "$CASE/state/install-state.env"
rm -f "$CASE/state/install-state.env.bak" "$CASE/exec.log"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "wrapper retry second rc" "0" "$rc"
assert_file_absent "wrapper retry second no marker" "$CASE/state/wrapper.pending"
assert_eq "wrapper retry second wrapper" "$(sha_file "$CASE/bundle/scripts/lib/contabase-update-wrapper.sh")" "$(sha_file "$CASE/installed-update")"
assert_file_absent "wrapper retry second no exec" "$CASE/exec.log"

# 22. Direct bundle re-verification: symlink, extra file, tamper and substitution.
setup_case direct-bundle source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
chmod 0700 "$CASE/bundle"
direct_check() {
  local mode="$1" rc=0
  (
    set +u
    # shellcheck disable=SC1090
    . "$WRAPPER"
    BUNDLE_DIR="$CASE/bundle"
    META_DIR="$CASE/direct-meta"
    mkdir -p "$META_DIR"
    TARGET_UPDATER_SHA="$(sha_file "$BUNDLE_DIR/updater.sha256")"
    verify_manifest_entries v1.0.0-beta.8 "$B8" "$TARGET_UPDATER_SHA" source
    case "$mode" in
      symlink) ln -s /nonexistent "$BUNDLE_DIR/scripts/lib/evil" ;;
      extra) printf 'extra\n' > "$BUNDLE_DIR/scripts/evil.sh" ;;
      fifo) mkfifo "$BUNDLE_DIR/scripts/lib/evil-fifo" ;;
      tamper) printf '\n# tampered\n' >> "$BUNDLE_DIR/scripts/update-contabase-source.sh" ;;
      substitution)
        verify_bundle_files v1.0.0-beta.8 source
        printf '\n# substituted\n' >> "$BUNDLE_DIR/scripts/update-contabase-source.sh"
        ;;
    esac
    verify_bundle_files v1.0.0-beta.8 source
  ) > "$CASE/direct.log" 2>&1 || rc=$?
  printf '%s' "$rc"
}
assert_eq "direct bundle baseline" "0" "$(direct_check baseline)"
assert_eq "direct bundle symlink rc" "1" "$(direct_check symlink)"
assert_contains "direct bundle symlink diagnostic" "symlink no bundle" "$CASE/direct.log"
rm -f "$CASE/bundle/scripts/lib/evil"
assert_eq "direct bundle extra rc" "1" "$(direct_check extra)"
assert_contains "direct bundle extra diagnostic" "arquivo extra no bundle" "$CASE/direct.log"
rm -f "$CASE/bundle/scripts/evil.sh"
assert_eq "direct bundle fifo rc" "1" "$(direct_check fifo)"
assert_contains "direct bundle fifo diagnostic" "arquivo especial no bundle" "$CASE/direct.log"
rm -f "$CASE/bundle/scripts/lib/evil-fifo"
assert_eq "direct bundle tamper rc" "1" "$(direct_check tamper)"
assert_contains "direct bundle tamper diagnostic" "conteudo divergente no bundle" "$CASE/direct.log"
# Rebuild for the substitution case.
cp "$TMP_BASE/www/raw/$B8/scripts/update-contabase-source.sh" "$CASE/bundle/scripts/update-contabase-source.sh"
assert_eq "direct bundle substitution rc" "1" "$(direct_check substitution)"
assert_contains "direct bundle substitution diagnostic" "conteudo divergente no bundle" "$CASE/direct.log"

# 23. Release method receives the verified wrapper path and hash.
setup_case release release v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "release rc" "0" "$rc"
assert_contains "release method" "method=release" "$CASE/exec.log"
assert_contains "release wrapper path" "/scripts/lib/contabase-update-wrapper.sh" "$CASE/exec.log"
assert_contains "release wrapper sha" "wrapper_sha=$(sha_file "$CASE/bundle/scripts/lib/contabase-update-wrapper.sh")" "$CASE/exec.log"

# ---------------------------------------------------------------------------
# F3-C blockers: tag identity, lock/child lifecycle, catalog method/arch,
# Docker Image helper provenance, state path/owner/mode and no-op locking.
# ---------------------------------------------------------------------------
set_ref() {
  local version="$1" type="$2" sha="$3"
  write_json "$TMP_BASE/www/api/repos/contabase-app/contabase/git/ref/tags/$version" \
    "{\"ref\":\"refs/tags/$version\",\"object\":{\"type\":\"$type\",\"sha\":\"$sha\"}}"
}

set_tag_object() {
  local sha="$1" type="$2" target="$3" tag="$4"
  write_json "$TMP_BASE/www/api/repos/contabase-app/contabase/git/tags/$sha" \
    "{\"tag\":\"$tag\",\"object\":{\"type\":\"$type\",\"sha\":\"$target\"}}"
}

wait_for_file() {
  local file="$1" n=0
  while [ ! -s "$file" ] && [ "$n" -lt 200 ]; do
    n=$((n + 1))
    sleep 0.05
  done
  [ -s "$file" ]
}

start_case_background() {
  local case_dir="$1"; shift
  (
    cd "$case_dir/repo" 2>/dev/null || cd "$case_dir"
    if [ "${TEST_PATH_ONLY:-0}" = "1" ]; then
      export PATH="${TEST_PATH_PREFIX:?}"
    else
      export PATH="${TEST_PATH_PREFIX:-$TMP_BASE/bin}:$PATH"
    fi
    export CONTABASE_STATE_FILE="$case_dir/state/install-state.env"
    export CONTABASE_UPDATE_WRAPPER="$case_dir/installed-update"
    export CONTABASE_UPDATE_ALIAS="$case_dir/cb-update"
    export CONTABASE_INSTALL_DIR="$case_dir/install"
    export CONTABASE_MODE_FILE="$case_dir/state/install-mode"
    export CONTABASE_ENV_FILE="$case_dir/state/contabase.env"
    export TEST_EXEC_LOG="$case_dir/exec.log"
    export TEST_STARTED="$case_dir/started"
    export TEST_CHILD_PID="$case_dir/child.pid"
    export TEST_SLEEP="${TEST_SLEEP:-0}"
    export TEST_IGNORE_TERM="${TEST_IGNORE_TERM:-0}"
    export TMPDIR="$case_dir/tmp"
    exec bash "${CASE_WRAPPER:-$WRAPPER}" "$@"
  ) > "$case_dir/out.log" 2>&1 &
  BG_PID=$!
}

direct_cleanup_probe() {
  local rc=0
  (
    set +u
    # shellcheck disable=SC1090
    . "$WRAPPER"
    STATE_DIR="$CASE/state"
    LOCK_HELD=false
    WORK_DIR="$CASE/contabase-update.probe"
    mkdir -p "$WORK_DIR"
    sleep 30 & SPID=$!
    CHILD_PID=$SPID
    CHILD_START=""
    cleanup
    rc=$?
    [ "$rc" -eq 1 ] || exit 20
    kill -0 "$SPID" 2>/dev/null || exit 21
    [ -d "$WORK_DIR" ] || exit 22
    kill "$SPID" 2>/dev/null || true
    exit 0
  ) > "$CASE/direct-cleanup.log" 2>&1 || rc=$?
  printf '%s' "$rc"
}

# P1-2: annotated tag, nested tag, tag object SHA and cycle handling.
setup_case tag-annotated source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
T1=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
T2=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
BAD=cccccccccccccccccccccccccccccccccccccccc
T3=dddddddddddddddddddddddddddddddddddddddd
set_ref v1.0.0-beta.8 tag "$T1"
set_tag_object "$T1" commit "$B8" v1.0.0-beta.8
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag annotated valid rc" "0" "$rc"
assert_contains "tag annotated valid exec" "method=source" "$CASE/exec.log"

setup_case tag-nested source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
set_ref v1.0.0-beta.8 tag "$T1"
set_tag_object "$T1" tag "$T2" v1.0.0-beta.8
set_tag_object "$T2" commit "$B8" v1.0.0-beta.8
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag nested valid rc" "0" "$rc"
assert_contains "tag nested valid exec" "method=source" "$CASE/exec.log"

setup_case tag-sha-divergent source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
set_ref v1.0.0-beta.8 commit "$BAD"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag sha divergent rc" "1" "$rc"
assert_contains "tag sha divergent diagnostic" "nao resolve para o commit autorizado" "$CASE/out.log"
assert_file_absent "tag sha divergent no exec" "$CASE/exec.log"

setup_case tag-other-commit source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
set_ref v1.0.0-beta.8 tag "$T1"
set_tag_object "$T1" commit "$BAD" v1.0.0-beta.8
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag other commit rc" "1" "$rc"
assert_file_absent "tag other commit no exec" "$CASE/exec.log"

setup_case tag-branch-same-name source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
set_ref v1.0.0-beta.8 commit "$BAD"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag branch same name rc" "1" "$rc"
assert_file_absent "tag branch same name no exec" "$CASE/exec.log"

setup_case tag-missing-object source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
set_ref v1.0.0-beta.8 tag "$T3"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag missing object rc" "1" "$rc"
assert_contains "tag missing object diagnostic" "resposta HTTP 404" "$CASE/out.log"
assert_file_absent "tag missing object no exec" "$CASE/exec.log"

setup_case tag-invalid-object source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
set_ref v1.0.0-beta.8 tag "$T1"
set_tag_object "$T1" commit nothex v1.0.0-beta.8
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag invalid object rc" "1" "$rc"
assert_file_absent "tag invalid object no exec" "$CASE/exec.log"

setup_case tag-cycle source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
set_ref v1.0.0-beta.8 tag "$T1"
set_tag_object "$T1" tag "$T1" v1.0.0-beta.8
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "tag cycle rc" "1" "$rc"
assert_contains "tag cycle diagnostic" "ciclo" "$CASE/out.log"
assert_file_absent "tag cycle no exec" "$CASE/exec.log"

# P2-4: no-op must acquire the lock before reporting already-current.
setup_case noop-lock source v1.0.0-beta.8 beta v1.0.0-beta.8 "$B8" "$B8"
mkdir -p "$CASE/state"
(
  exec 200>"$CASE/state/contabase-update.lock"
  flock -n 200
  sleep 5 200>&-
) &
HOLDER=$!
sleep 0.2
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "noop lock blocked rc" "1" "$rc"
assert_contains "noop lock blocked diagnostic" "outra atualizacao esta em curso" "$CASE/out.log"
assert_file_absent "noop lock blocked no exec" "$CASE/exec.log"
kill "$HOLDER" 2>/dev/null || true
wait "$HOLDER" 2>/dev/null || true

# P2-5: legacy directory lock must fail closed instead of being removed.
setup_case legacy-lock source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mkdir -p "$CASE/state/contabase-update.lock.d"
printf '999999\n' > "$CASE/state/contabase-update.lock.d/pid"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "legacy lock rc" "1" "$rc"
assert_contains "legacy lock diagnostic" "lock legado" "$CASE/out.log"
assert_file_absent "legacy lock no exec" "$CASE/exec.log"

# flock unavailable: fail closed before network, execution or mutation.
setup_case no-flock source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
: > "$TMP_BASE/curl.log"
TEST_PATH_ONLY=1
TEST_PATH_PREFIX="$TMP_BASE/bin-noflock"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
TEST_PATH_ONLY=0
unset TEST_PATH_PREFIX
assert_eq "no flock rc" "1" "$rc"
assert_contains "no flock diagnostic" "flock obrigatório" "$CASE/out.log"
assert_eq "no flock no network" "" "$(cat "$TMP_BASE/curl.log")"
assert_file_absent "no flock no exec" "$CASE/exec.log"

# P1-1: SIGTERM with an active child must terminate the child and release only
# after it is gone.
setup_case signal-term source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mkdir -p "$CASE/tmp"
TEST_SLEEP=30
TEST_IGNORE_TERM=0
start_case_background "$CASE" v1.0.0-beta.8
WPID="$BG_PID"
wait_for_file "$CASE/started"
wait_for_file "$CASE/child.pid"
CHILD="$(cat "$CASE/child.pid")"
kill -TERM "$WPID" 2>/dev/null || true
wait "$WPID" 2>/dev/null; wrc=$?
assert_eq "signal term dispatcher rc" "143" "$wrc"
sleep 0.5
if [ -n "$CHILD" ] && kill -0 "$CHILD" 2>/dev/null; then echo "signal term child alive" >&2; FAIL=$((FAIL + 1)); else PASS=$((PASS + 1)); fi
assert_file_absent "signal term workdir removed" "$(find "$CASE/tmp" -maxdepth 1 -name 'contabase-update.*' -print -quit)"
TEST_SLEEP=0
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "signal term retry rc" "0" "$rc"

# P1-1: SIGKILL leaves the child holding the global lock; the next dispatcher
# must not start until that child is gone. The workdir stays available.
setup_case signal-kill source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mkdir -p "$CASE/tmp"
TEST_SLEEP=30
TEST_IGNORE_TERM=0
start_case_background "$CASE" v1.0.0-beta.8
WPID="$BG_PID"
wait_for_file "$CASE/started"
wait_for_file "$CASE/child.pid"
CHILD="$(cat "$CASE/child.pid")"
kill -KILL "$WPID" 2>/dev/null || true
wait "$WPID" 2>/dev/null; wrc=$?
assert_eq "signal kill dispatcher rc" "137" "$wrc"
sleep 0.3
if [ -n "$CHILD" ] && kill -0 "$CHILD" 2>/dev/null; then PASS=$((PASS + 1)); else echo "signal kill child dead too early" >&2; FAIL=$((FAIL + 1)); fi
if find "$CASE/tmp" -maxdepth 1 -name 'contabase-update.*' -print -quit | grep -q .; then PASS=$((PASS + 1)); else echo "signal kill workdir missing" >&2; FAIL=$((FAIL + 1)); fi
TEST_SLEEP=0
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "signal kill next dispatcher blocked" "1" "$rc"
assert_contains "signal kill next dispatcher diagnostic" "outra atualizacao esta em curso" "$CASE/out.log"
kill -KILL "$CHILD" 2>/dev/null || true
sleep 0.3
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "signal kill after child rc" "0" "$rc"

# P1-1: a child that ignores SIGTERM is escalated to SIGKILL and only then
# releases the lock.
setup_case signal-ignore source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mkdir -p "$CASE/tmp"
TEST_SLEEP=30
TEST_IGNORE_TERM=1
start_case_background "$CASE" v1.0.0-beta.8
WPID="$BG_PID"
wait_for_file "$CASE/started"
wait_for_file "$CASE/child.pid"
CHILD="$(cat "$CASE/child.pid")"
kill -TERM "$WPID" 2>/dev/null || true
wait "$WPID" 2>/dev/null; wrc=$?
assert_eq "signal ignore dispatcher rc" "143" "$wrc"
sleep 0.3
if [ -n "$CHILD" ] && kill -0 "$CHILD" 2>/dev/null; then echo "signal ignore child alive" >&2; FAIL=$((FAIL + 1)); else PASS=$((PASS + 1)); fi
TEST_SLEEP=0
TEST_IGNORE_TERM=0
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "signal ignore retry rc" "0" "$rc"

# P2-5 / P1-1: PID + start time distinguishes reuse; an unknown child
# identity preserves the workdir and does not kill an unrelated process.
setup_case direct-lock source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
assert_eq "direct cleanup unknown child" "0" "$(direct_cleanup_probe)"

# P2-6/P2-10: state path, symlink and permissions.
setup_case state-relative source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rc="$(run_case_env "$CASE" CONTABASE_STATE_FILE=relative -- v1.0.0-beta.8)"
assert_eq "state relative rc" "1" "$rc"
assert_contains "state relative diagnostic" "absoluto" "$CASE/out.log"
assert_file_absent "state relative no exec" "$CASE/exec.log"

setup_case state-symlink source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mv "$CASE/state/install-state.env" "$CASE/state/real.env"
ln -s real.env "$CASE/state/install-state.env"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "state symlink rc" "1" "$rc"
assert_contains "state symlink diagnostic" "symlink" "$CASE/out.log"
assert_file_absent "state symlink no exec" "$CASE/exec.log"

setup_case state-parent-symlink source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mv "$CASE/state" "$CASE/state-real"
ln -s state-real "$CASE/state"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "state parent symlink rc" "1" "$rc"
assert_contains "state parent symlink diagnostic" "diretorio do estado" "$CASE/out.log"
assert_file_absent "state parent symlink no exec" "$CASE/exec.log"

setup_case state-group-write source v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
chmod 0666 "$CASE/state/install-state.env"
rc="$(run_case "$CASE" v1.0.0-beta.8)"
assert_eq "state group write rc" "1" "$rc"
assert_contains "state group write diagnostic" "gravavel por grupo" "$CASE/out.log"
assert_file_absent "state group write no exec" "$CASE/exec.log"

# P2-2/P2-3: Docker Image helper provenance. The installed copy is preferred;
# an unsafe primary fails closed and never falls back to a second helper.
setup_case helper-precedence docker-image v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mkdir -p "$CASE/bin" "$CASE/lib/contabase"
cp "$WRAPPER" "$CASE/bin/contabase-update"
chmod 0700 "$CASE/bin/contabase-update"
cat > "$CASE/lib/contabase/contabase-docker-image-update.py" <<'PY'
import os
with open(os.environ['TEST_EXEC_LOG'], 'w', encoding='utf-8') as handle:
    handle.write('helper=installed\n')
PY
chmod 0700 "$CASE/lib/contabase/contabase-docker-image-update.py"
cat > "$CASE/bin/contabase-docker-image-update.py" <<'PY'
import os
with open(os.environ['TEST_EXEC_LOG'], 'w', encoding='utf-8') as handle:
    handle.write('helper=wrapper-dir\n')
PY
chmod 0700 "$CASE/bin/contabase-docker-image-update.py"
CASE_WRAPPER="$CASE/bin/contabase-update"
rc="$(run_case_env "$CASE" CONTABASE_UPDATE_RESOLVE_ONLY=1 -- v1.0.0-beta.8)"
assert_eq "helper precedence rc" "0" "$rc"
assert_contains "helper precedence installed" "helper=installed" "$CASE/exec.log"
assert_not_contains "helper precedence no fallback" "helper=wrapper-dir" "$CASE/exec.log"

setup_case helper-unsafe docker-image v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
rm -f "$CASE/contabase-docker-image-update.py"
ln -s /nonexistent "$CASE/contabase-docker-image-update.py"
rc="$(run_case_env "$CASE" CONTABASE_UPDATE_RESOLVE_ONLY=1 -- v1.0.0-beta.8)"
assert_eq "helper unsafe rc" "1" "$rc"
assert_contains "helper unsafe diagnostic" "symlink" "$CASE/out.log"
assert_file_absent "helper unsafe no exec" "$CASE/exec.log"

setup_case helper-primary-unsafe docker-image v1.0.0-beta.7 beta v1.0.0-beta.8 "$B8"
mkdir -p "$CASE/bin" "$CASE/lib/contabase"
cp "$WRAPPER" "$CASE/bin/contabase-update"
chmod 0700 "$CASE/bin/contabase-update"
ln -s /nonexistent "$CASE/lib/contabase/contabase-docker-image-update.py"
cat > "$CASE/bin/contabase-docker-image-update.py" <<'PY'
import os
with open(os.environ['TEST_EXEC_LOG'], 'w', encoding='utf-8') as handle:
    handle.write('helper=wrapper-dir\n')
PY
chmod 0700 "$CASE/bin/contabase-docker-image-update.py"
CASE_WRAPPER="$CASE/bin/contabase-update"
rc="$(run_case_env "$CASE" CONTABASE_UPDATE_RESOLVE_ONLY=1 -- v1.0.0-beta.8)"
assert_eq "helper primary unsafe rc" "1" "$rc"
assert_contains "helper primary unsafe diagnostic" "symlink" "$CASE/out.log"
assert_file_absent "helper primary unsafe no fallback exec" "$CASE/exec.log"


stop_server

printf '\n=== F3-C DISPATCHER: %s passed, %s failed ===\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

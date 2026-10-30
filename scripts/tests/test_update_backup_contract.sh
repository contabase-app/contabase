#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/contabase-update-entrypoints.XXXXXX")"
cleanup() {
  find "$TMP_ROOT" -type d -name data -exec chmod 700 {} + 2>/dev/null || true
  if [ "${KEEP_TMP:-0}" = 1 ]; then printf '# fixtures preservadas em %s\n' "$TMP_ROOT" >&2; else rm -rf "$TMP_ROOT"; fi
}
trap cleanup EXIT

REAL_CP="$(command -v cp)"
REAL_MV="$(command -v mv)"
REAL_RM="$(command -v rm)"
REAL_FIND="$(command -v find)"
REAL_GIT="$(command -v git)"
REAL_INSTALL="$(command -v install)"
REAL_TAR="$(command -v tar)"
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

assert_file_content() {
  local name="$1" file="$2" expected="$3" actual
  if [ ! -f "$file" ]; then not_ok "$name"; return; fi
  actual="$(cat "$file")"
  if [ "$actual" = "$expected" ]; then ok "$name"; else not_ok "$name"; fi
}

assert_backup_preserved() {
  local name="$1" log="$2" package
  package="$(sed -n 's/^event:backup_success package=//p' "$log" | tail -n1)"
  if [ -n "$package" ] && [ -f "$package/database.sqlite" ] && [ -d "$package/uploads" ]; then
    ok "$name"
  else
    not_ok "$name"
  fi
}

assert_event() { assert_contains "$1" "$2" "event:$3"; }
assert_no_event() { assert_not_contains "$1" "$2" "event:$3"; }

assert_event_order() {
  local name="$1" log="$2"
  shift 2
  local previous=0 event line
  for event in "$@"; do
    line="$(awk -v event="event:${event}" 'index($0,event) { print NR; exit }' "$log")"
    if [ -z "$line" ] || [ "$line" -le "$previous" ]; then not_ok "$name"; return; fi
    previous="$line"
  done
  ok "$name"
}

write_fake_backup_helper() {
  local destination="$1"
  cat > "$destination" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'event:canonical_backup cmd=%s app_version=%s data=%s db=%s uploads=%s\n' \
  "$0" "$APP_VERSION" "$DATA_DIR" "$DB_FILE" "$UPLOADS_DIR" >> "$EVENT_LOG"
printf 'backup_previous=%s\ntarget_version=%s\n' "$APP_VERSION" "${CONTABASE_VERSION:-}" >> "$ARGS_LOG"
if [[ "${TEST_SCENARIO:-}" = backup_fail* ]]; then
  printf 'event:backup_failure status=42\n' >> "$EVENT_LOG"
  exit 42
fi
counter=0
[ ! -f "$COUNTER_FILE" ] || counter="$(cat "$COUNTER_FILE")"
counter=$((counter + 1))
printf '%s\n' "$counter" > "$COUNTER_FILE"
package="$1/backup-fixture-$counter"
mkdir -p "$package/uploads"
: > "$package/database.sqlite"
: > "$package/checksums.sha256"
printf 'backup_id=backup-fixture-%s\n' "$counter" > "$package/manifest.txt"
printf 'event:backup_success package=%s\n' "$package" >> "$EVENT_LOG"
printf '%s\n' "$package"
SHIM
  chmod +x "$destination"
}

write_shims() {
  local bin="$TMP_ROOT/bin"
  mkdir -p "$bin"

  cat > "$bin/git" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'cmd:git %s\n' "$*" >> "$EVENT_LOG"
exec "$REAL_GIT" "$@"
SHIM

  cat > "$bin/docker" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'cmd:docker %s\n' "$*" >> "$EVENT_LOG"
if [ "${1:-}" = exec ]; then
  cat "$RUNTIME_VERSION_FILE"
  exit 0
fi
case "$*" in
  'compose '*config*) exit 0 ;;
  'compose '*"ps -q contabase"*) printf 'fixture-container\n'; exit 0 ;;
  inspect*'{{.Id}}'*) printf 'fixture-container\n'; exit 0 ;;
  inspect*'State.Health'*) printf 'healthy\n'; exit 0 ;;
  inspect*'RestartCount'*) printf '0\n'; exit 0 ;;
  inspect*'com.docker.compose.service'*) printf 'contabase\n'; exit 0 ;;
  inspect*'com.docker.compose.project.working_dir'*) printf '<no value>\n'; exit 0 ;;
  inspect*'com.docker.compose.project'*) printf 'fixture-project\n'; exit 0 ;;
  inspect*'Mounts'*) printf '/app/data\n'; exit 0 ;;
  inspect*'Config.Image'*) printf 'contabase:fixture-old\n'; exit 0 ;;
  inspect*'.Image'*) printf 'sha256:fixture-old\n'; exit 0 ;;
  'image tag '*|'image rm '*) exit 0 ;;
  'compose '*port*) printf '127.0.0.1:8080\n'; exit 0 ;;
  'compose '*logs*) exit 0 ;;
esac
if [ "${1:-}" = compose ]; then
  case " $* " in
    *' down '*)
      if grep -q 'event:start_new_runtime' "$EVENT_LOG"; then
        printf 'event:stop_or_safe_state cmd=docker_compose_down\n' >> "$EVENT_LOG"
      elif ! grep -q 'event:stop_or_quiesce' "$EVENT_LOG"; then
        printf 'event:stop_or_quiesce cmd=docker_compose_down\n' >> "$EVENT_LOG"
      fi
      exit 0
      ;;
    *' up '*' --build '*)
      printf '%s\n' "${VERSION:-}" > "$RUNTIME_VERSION_FILE"
      printf 'event:start_new_runtime cmd=docker_compose_up_build\n' >> "$EVENT_LOG"
      if [ "${TEST_SCENARIO:-}" = post_start ]; then
        printf 'event:failure point=new_runtime_start\n' >> "$EVENT_LOG"
        exit 45
      fi
      exit 0
      ;;
    *' up '*' --no-build '*)
      printf '%s\n' "${PREVIOUS_RUNTIME_VERSION:-v0.1.0-beta.7}" > "$RUNTIME_VERSION_FILE"
      printf 'event:start_old_runtime cmd=docker_compose_up_no_build\n' >> "$EVENT_LOG"
      exit 0
      ;;
  esac
fi
exit 0
SHIM

  cat > "$bin/systemctl" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'cmd:systemctl %s\n' "$*" >> "$EVENT_LOG"
case "${1:-}" in
  is-active)
    if [ "${TEST_MODE:-}" = release ] && [ "${TEST_SCENARIO:-}" = backup_fail_inactive ]; then
      printf 'event:service_initially_inactive cmd=systemctl_is_active\n' >> "$EVENT_LOG"
      exit 3
    fi
    exit 0
    ;;
  stop)
    if grep -q 'event:start_new_runtime' "$EVENT_LOG"; then
      printf 'event:stop_or_safe_state cmd=systemctl_stop\n' >> "$EVENT_LOG"
    elif ! grep -q 'event:stop_or_quiesce' "$EVENT_LOG"; then
      printf 'event:stop_or_quiesce cmd=systemctl_stop\n' >> "$EVENT_LOG"
    fi
    exit 0
    ;;
  start)
    if grep -q 'event:backup_failure' "$EVENT_LOG"; then
      printf 'event:start_old_runtime cmd=systemctl_start after=backup_failure\n' >> "$EVENT_LOG"
    elif [ "${TEST_SCENARIO:-}" = pre_start ]; then
      printf 'event:start_old_runtime cmd=systemctl_start\n' >> "$EVENT_LOG"
    else
      printf 'event:start_new_runtime cmd=systemctl_start\n' >> "$EVENT_LOG"
      if [ "${TEST_SCENARIO:-}" = post_start ]; then
        printf 'event:failure point=new_runtime_start\n' >> "$EVENT_LOG"
        exit 46
      fi
    fi
    exit 0
    ;;
  restart)
    if grep -q 'event:backup_failure' "$EVENT_LOG"; then
      printf 'event:start_old_runtime cmd=systemctl_restart after=backup_failure\n' >> "$EVENT_LOG"
    elif [ "${TEST_SCENARIO:-}" = pre_start ]; then
      printf 'event:start_old_runtime cmd=systemctl_restart\n' >> "$EVENT_LOG"
    else
      printf 'event:start_new_runtime cmd=systemctl_restart\n' >> "$EVENT_LOG"
      if [ "${TEST_SCENARIO:-}" = post_start ]; then
        printf 'event:failure point=new_runtime_start\n' >> "$EVENT_LOG"
        exit 47
      fi
    fi
    exit 0
    ;;
  daemon-reload|status|enable) exit 0 ;;
esac
exit 0
SHIM

  cat > "$bin/curl" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'cmd:curl %s\n' "$*" >> "$EVENT_LOG"
destination=""; url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) destination="$2"; shift 2 ;;
    http://*|https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
if [ -n "$destination" ]; then
  case "$url" in
    */checksums.txt) "$REAL_CP" "$TEST_CHECKSUMS" "$destination" ;;
    */contabase-linux-*.tar.gz) "$REAL_CP" "$TEST_ARTIFACT" "$destination" ;;
    */scripts/ops/backup.sh) "$REAL_CP" "$REAL_BACKUP_HELPER" "$destination" ;;
    */scripts/ops/restore.sh) "$REAL_CP" "$REAL_RESTORE_HELPER" "$destination" ;;
    *) exit 48 ;;
  esac
  exit 0
fi
if [ "${TEST_SCENARIO:-}" = health_fail ]; then exit 1; fi
printf '{"status":"healthy"}'
SHIM

  cat > "$bin/cp" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
map_path() {
  case "$1" in
    /opt/contabase*) printf '%s%s' "$TEST_APP_DIR" "${1#/opt/contabase}" ;;
    /tmp/contabase) printf '%s' "$TEST_TMP_BINARY" ;;
    *) printf '%s' "$1" ;;
  esac
}
args=(); for arg in "$@"; do args+=("$(map_path "$arg")"); done
printf 'cmd:cp %s\n' "${args[*]}" >> "$EVENT_LOG"
destination="${args[${#args[@]}-1]:-}"
if [ "${TEST_MODE:-}" = docker ] && [ "${TEST_SCENARIO:-}" = pre_start ]; then
  case "$destination" in
    *.pre-update-*.bak)
      printf 'event:first_mutation cmd=cp\n' >> "$EVENT_LOG"
      printf 'event:failure point=env_backup\n' >> "$EVENT_LOG"
      exit 53
      ;;
  esac
fi
if grep -q 'event:backup_success' "$EVENT_LOG" && ! grep -q 'event:first_mutation' "$EVENT_LOG"; then
  printf 'event:first_mutation cmd=cp\n' >> "$EVENT_LOG"
fi
exec "$REAL_CP" "${args[@]}"
SHIM

  cat > "$bin/mv" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'cmd:mv %s\n' "$*" >> "$EVENT_LOG"
"$REAL_MV" "$@"
case "${1:-}" in
  *'.backup-partial.'*) printf 'event:backup_success package=%s\n' "${@: -1}" >> "$EVENT_LOG" ;;
  *)
    if grep -q 'event:backup_success' "$EVENT_LOG" && ! grep -q 'event:first_mutation' "$EVENT_LOG"; then
      printf 'event:first_mutation cmd=mv\n' >> "$EVENT_LOG"
    fi
    ;;
esac
SHIM

  cat > "$bin/install" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
map_path() {
  case "$1" in
    /opt/contabase*) printf '%s%s' "$TEST_APP_DIR" "${1#/opt/contabase}" ;;
    /tmp/contabase) printf '%s' "$TEST_TMP_BINARY" ;;
    /etc/systemd/system/contabase.service) printf '%s' "$TEST_SERVICE_FILE" ;;
    /usr/local/bin/*) printf '%s/%s' "$TEST_LOCAL_BIN" "${1##*/}" ;;
    *) printf '%s' "$1" ;;
  esac
}
args=()
while [ "$#" -gt 0 ]; do
  case "$1" in -o|-g) shift 2 ;; *) args+=("$(map_path "$1")"); shift ;; esac
done
destination="${args[${#args[@]}-1]}"
printf 'cmd:install %s\n' "${args[*]}" >> "$EVENT_LOG"
if [ "${TEST_MODE:-}" = release ] && [ "${TEST_SCENARIO:-}" = pre_start ] && [ "$destination" = "$TEST_SERVICE_FILE" ]; then
  printf 'event:failure point=service_file_install\n' >> "$EVENT_LOG"
  exit 49
fi
if grep -q 'event:backup_success' "$EVENT_LOG" && ! grep -q 'event:first_mutation' "$EVENT_LOG"; then
  printf 'event:first_mutation cmd=install destination=%s\n' "$destination" >> "$EVENT_LOG"
fi
exec "$REAL_INSTALL" "${args[@]}"
SHIM

  cat > "$bin/rm" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
args=()
for arg in "$@"; do
  case "$arg" in
    /opt/contabase*) args+=("$TEST_APP_DIR${arg#/opt/contabase}") ;;
    /tmp/contabase) args+=("$TEST_TMP_BINARY") ;;
    *) args+=("$arg") ;;
  esac
done
exec "$REAL_RM" "${args[@]}"
SHIM

  cat > "$bin/find" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" = /opt/contabase* ]]; then
  first="$TEST_APP_DIR${1#/opt/contabase}"; shift
  exec "$REAL_FIND" "$first" "$@"
fi
exec "$REAL_FIND" "$@"
SHIM

  cat > "$bin/sqlite3" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'cmd:sqlite3 %s\n' "$*" >> "$EVENT_LOG"
case "$*" in
  *'.backup '*)
    printf 'event:canonical_backup cmd=sqlite3_backup\n' >> "$EVENT_LOG"
    if [[ "${TEST_SCENARIO:-}" = backup_fail* ]]; then
      printf 'event:backup_failure status=51\n' >> "$EVENT_LOG"
      exit 51
    fi
    destination="$(printf '%s' "$*" | sed -n "s/.*\.backup '\([^']*\)'.*/\1/p")"
    "$REAL_CP" "$1" "$destination"
    ;;
  *'PRAGMA journal_mode=DELETE;'*) printf 'delete\n' ;;
  *'PRAGMA integrity_check;'*) printf 'ok\n' ;;
  *'PRAGMA foreign_key_check;'*) : ;;
  *'SELECT MAX(version) FROM schema_migrations;'*) printf '54\n' ;;
esac
SHIM

  cat > "$bin/id" <<'SHIM'
#!/usr/bin/env bash
case "${1:-}" in -u) echo 0 ;; -un) echo "${TEST_USER:-fixture}" ;; *) echo "${TEST_USER:-fixture}" ;; esac
SHIM
  cat > "$bin/uname" <<'SHIM'
#!/usr/bin/env bash
case "${1:-}" in -m) echo x86_64 ;; *) echo Linux ;; esac
SHIM
  cat > "$bin/node" <<'SHIM'
#!/usr/bin/env bash
echo v24.0.0
SHIM
  cat > "$bin/npm" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
printf 'cmd:npm %s\n' "$*" >> "$EVENT_LOG"
if [ "${TEST_MODE:-}" = source ] && [ "${TEST_SCENARIO:-}" = pre_start ]; then
  printf 'event:failure point=npm_ci\n' >> "$EVENT_LOG"
  exit 52
fi
exit 0
SHIM
  cat > "$bin/go" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = version ]; then echo 'go version go1.24.0 linux/amd64'; exit 0; fi
printf 'cmd:go %s\n' "$*" >> "$EVENT_LOG"
while [ "$#" -gt 0 ]; do
  if [ "$1" = -o ]; then
    destination="$2"; [ "$destination" != /tmp/contabase ] || destination="$TEST_TMP_BINARY"
    mkdir -p "$(dirname "$destination")"; : > "$destination"; chmod +x "$destination"; break
  fi
  shift
done
exit 0
SHIM
  cat > "$bin/chown" <<'SHIM'
#!/usr/bin/env bash
exit 0
SHIM
  cat > "$bin/chmod" <<'SHIM'
#!/usr/bin/env bash
set -euo pipefail
args=()
for arg in "$@"; do
  case "$arg" in
    /opt/contabase*) args+=("$TEST_APP_DIR${arg#/opt/contabase}") ;;
    /tmp/contabase) args+=("$TEST_TMP_BINARY") ;;
    *) args+=("$arg") ;;
  esac
done
exec /bin/chmod "${args[@]}"
SHIM
  cat > "$bin/journalctl" <<'SHIM'
#!/usr/bin/env bash
exit 0
SHIM
  cat > "$bin/sudo" <<'SHIM'
#!/usr/bin/env bash
exit 1
SHIM
  cat > "$bin/sleep" <<'SHIM'
#!/usr/bin/env bash
exit 0
SHIM
  chmod +x "$bin"/*
}

make_git_fixture() {
  local root="$1" updater="$2"
  local seed="$root/seed" origin="$root/origin.git" install_repo="$root/repo"
  mkdir -p "$seed/scripts/ops" "$seed/scripts/lib" "$seed/templates" "$seed/assets"
  "$REAL_CP" "$REPO_DIR/scripts/lib/contabase-recovery.sh" "$seed/scripts/lib/contabase-recovery.sh"
  "$REAL_GIT" -C "$seed" init -q -b main
  "$REAL_GIT" -C "$seed" config user.name Fixture
  "$REAL_GIT" -C "$seed" config user.email fixture@example.invalid
  "$REAL_CP" "$updater" "$seed/scripts/$(basename "$updater")"
  write_fake_backup_helper "$seed/scripts/ops/backup.sh"
  printf 'v0.1.0-beta.7\n' > "$seed/VERSION"
  printf 'module github.com/contabase-app/contabase\n\ngo 1.24\n' > "$seed/go.mod"
  cat > "$seed/.gitignore" <<'IGN'
data/
*.db
*.db-journal
*.db-wal
*.db-shm
.env
.env.*
!*.example
compose.local.yaml
compose.local.yml
docker-compose.override.yml
*.bak
IGN
  cat > "$seed/Dockerfile" <<'DOCKER'
FROM scratch
DOCKER
  cat > "$seed/docker-compose.yml" <<'COMPOSE'
services:
  contabase:
    image: contabase:fixture
COMPOSE
  cat > "$seed/scripts/build-css.sh" <<'SHIM'
#!/usr/bin/env bash
exit 0
SHIM
  chmod +x "$seed/scripts/build-css.sh" "$seed/scripts/$(basename "$updater")"
  : > "$seed/templates/template.html"; : > "$seed/assets/asset.css"
  "$REAL_GIT" -C "$seed" add .; "$REAL_GIT" -C "$seed" commit -qm beta7
  "$REAL_GIT" init -q --bare "$origin"
  "$REAL_GIT" -C "$seed" remote add origin "$origin"; "$REAL_GIT" -C "$seed" push -q -u origin main
  "$REAL_GIT" --git-dir="$origin" symbolic-ref HEAD refs/heads/main
  "$REAL_GIT" clone -q "$origin" "$install_repo"
  printf 'v0.1.0-beta.8\n' > "$seed/VERSION"
  "$REAL_GIT" -C "$seed" add VERSION; "$REAL_GIT" -C "$seed" commit -qm beta8; "$REAL_GIT" -C "$seed" push -q origin main
  printf '%s' "$install_repo"
}

prepare_runtime_env() {
  local root="$1" mode="$2" scenario="$3"
  export TEST_MODE="$mode" TEST_SCENARIO="$scenario"
  export EVENT_LOG="$root/events.log" ARGS_LOG="$root/args.log" COUNTER_FILE="$root/counter"
  export TEST_APP_DIR="$root/app" TEST_TMP_BINARY="$root/tmp/contabase"
  export TEST_SERVICE_FILE="$root/systemd/contabase.service" TEST_LOCAL_BIN="$root/local-bin"
  export TEST_DATA_DIR="$root/data" TEST_USER="$(id -un)"
  export REAL_CP REAL_MV REAL_RM REAL_FIND REAL_GIT REAL_INSTALL REAL_TAR
  export REAL_BACKUP_HELPER="$REPO_DIR/scripts/ops/backup.sh" REAL_RESTORE_HELPER="$REPO_DIR/scripts/ops/restore.sh"
  export PATH="$TMP_ROOT/bin:/usr/bin:/bin:/usr/sbin:/sbin"
  mkdir -p "$TEST_APP_DIR" "$root/tmp" "$(dirname "$TEST_SERVICE_FILE")" "$TEST_LOCAL_BIN" "$TEST_DATA_DIR/uploads"
  : > "$EVENT_LOG"; : > "$ARGS_LOG"
  printf 'synthetic-db\n' > "$TEST_DATA_DIR/contabase.db"
  printf 'synthetic-upload\n' > "$TEST_DATA_DIR/uploads/file.txt"
  export RUNTIME_VERSION_FILE="$root/runtime-version" PREVIOUS_RUNTIME_VERSION="v0.1.0-beta.7"
  printf 'v0.1.0-beta.7\n' > "$RUNTIME_VERSION_FILE"
}

run_docker() {
  local scenario="$1" root repo output rc
  root="$TMP_ROOT/docker-$scenario"
  mkdir -p "$root"; repo="$(make_git_fixture "$root" "$REPO_DIR/scripts/update-contabase-docker.sh")"
  prepare_runtime_env "$root" docker "$scenario"
  cat > "$repo/.env.docker" <<'ENV'
AUTH_ENCRYPTION_KEY=fixture-auth
SECURITY_MASTER_KEY=fixture-security
CONTABASE_TIMEZONE=America/Sao_Paulo
PORT=8080
ENV
  mkdir -p "$repo/data/uploads"; printf 'synthetic-db\n' > "$repo/data/contabase.db"; printf 'upload\n' > "$repo/data/uploads/file.txt"
  export TEST_DATA_DIR="$repo/data"; output="$root/output.log"
  if [ "$scenario" = inaccessible ]; then chmod 000 "$repo/data"; fi
  set +e
  (cd "$repo" && CONTABASE_ASSUME_YES=1 HEALTHCHECK_ATTEMPTS=1 \
    CONTABASE_STATE_FILE="$root/etc/contabase/install-state.env" \
    CONTABASE_MODE_FILE="$root/etc/contabase/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$root/local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$root/local-bin/cb-update" \
    ./scripts/update-contabase-docker.sh --yes) >"$output" 2>&1
  rc=$?; set -e; chmod 700 "$repo/data" 2>/dev/null || true; printf '%s' "$rc"
}

run_source() {
  local scenario="$1" root repo output rc bash_env
  root="$TMP_ROOT/source-$scenario"
  mkdir -p "$root"; repo="$(make_git_fixture "$root" "$REPO_DIR/scripts/update-contabase-source.sh")"
  prepare_runtime_env "$root" source "$scenario"; output="$root/output.log"
  bash_env="$root/bash-env"
  cat > "$bash_env" <<'ENTRY'
__entrypoint_test_debug() {
  case "$BASH_COMMAND" in
    check_linux_systemd) check_linux_systemd() { :; } ;;
    backfill_update_command) backfill_update_command() { :; }; trap - DEBUG ;;
  esac
}
set -T
trap '__entrypoint_test_debug' DEBUG
ENTRY
  set +e
  (cd "$repo" && DATA_DIR="$TEST_DATA_DIR" CONTABASE_VERSION=v0.1.0-beta.8 \
    BASH_ENV="$bash_env" bash "$repo/scripts/update-contabase-source.sh") >"$output" 2>&1
  rc=$?; set -e; printf '%s' "$rc"
}

make_release_fixture() {
  local root="$1" bundle="$root/bundle/contabase"
  mkdir -p "$bundle/templates" "$bundle/assets" "$root/install" "$root/config" "$root/data/uploads"
  : > "$bundle/contabase"; : > "$bundle/admin"; chmod +x "$bundle/contabase" "$bundle/admin"
  : > "$bundle/templates/template.html"; : > "$bundle/assets/asset.css"; : > "$bundle/LICENSE"
  : > "$bundle/contabase.env.example"; : > "$bundle/contabase.service.example"; printf 'v0.1.0-beta.8\n' > "$bundle/VERSION"
  TEST_ARTIFACT="$root/contabase-linux-amd64.tar.gz"; "$REAL_TAR" -czf "$TEST_ARTIFACT" -C "$root/bundle" contabase
  TEST_CHECKSUMS="$root/checksums.txt"; shasum -a 256 "$TEST_ARTIFACT" | awk '{print $1 "  contabase-linux-amd64.tar.gz"}' > "$TEST_CHECKSUMS"
  export TEST_ARTIFACT TEST_CHECKSUMS
  : > "$root/install/contabase"; chmod +x "$root/install/contabase"; printf 'v0.1.0-beta.7\n' > "$root/install/VERSION"
  cat > "$root/config/contabase.env" <<ENV
AUTH_ENCRYPTION_KEY=fixture-auth
SECURITY_MASTER_KEY=fixture-security
APP_BASE_URL=http://127.0.0.1:8080
PORT=8080
DATA_DIR=$root/data
DB_FILE=$root/data/contabase.db
UPLOADS_DIR=$root/data/uploads
CONTABASE_ACCESS_MODE=local
CONTABASE_TIMEZONE=America/Sao_Paulo
ENV
}

run_release() {
  local scenario="$1" root output rc bash_env
  root="$TMP_ROOT/release-$scenario"
  mkdir -p "$root"; prepare_runtime_env "$root" release "$scenario"; make_release_fixture "$root"
  export TEST_APP_DIR="$root/install" TEST_DATA_DIR="$root/data"; output="$root/output.log"
  bash_env="$root/bash-env"
  cat > "$bash_env" <<'ENTRY'
__entrypoint_test_debug() {
  if [ "$BASH_COMMAND" = backfill_update_command ]; then
    backfill_update_command() { :; }
    trap - DEBUG
  fi
}
set -T
trap '__entrypoint_test_debug' DEBUG
ENTRY
  set +e
  CONTABASE_VERSION=v0.1.0-beta.8 CONTABASE_ASSUME_YES=1 CONTABASE_HEALTHCHECK_ATTEMPTS=1 \
    CONTABASE_INSTALL_DIR="$root/install" CONTABASE_DATA_DIR="$root/data" CONTABASE_CONFIG_DIR="$root/config" \
    CONTABASE_USER="$TEST_USER" BASH_ENV="$bash_env" bash "$REPO_DIR/scripts/update-contabase-release.sh" >"$output" 2>&1
  rc=$?; set -e; printf '%s' "$rc"
}

write_shims

docker_success_rc="$(run_docker success)"
assert_eq "Docker sucesso retorna zero" 0 "$docker_success_rc"
assert_event_order "Docker ordem real de sucesso" "$TMP_ROOT/docker-success/events.log" stop_or_quiesce canonical_backup backup_success first_mutation start_new_runtime

docker_backup_rc="$(run_docker backup_fail)"
[ "$docker_backup_rc" -ne 0 ] && ok "Docker backup falho retorna erro" || not_ok "Docker backup falho retorna erro"
assert_event_order "Docker backup falho para apos helper" "$TMP_ROOT/docker-backup_fail/events.log" stop_or_quiesce canonical_backup backup_failure
assert_no_event "Docker backup falho sem mutacao" "$TMP_ROOT/docker-backup_fail/events.log" first_mutation
assert_no_event "Docker backup falho sem runtime novo" "$TMP_ROOT/docker-backup_fail/events.log" start_new_runtime
assert_no_event "Docker backup falho sem runtime antigo" "$TMP_ROOT/docker-backup_fail/events.log" start_old_runtime

docker_pre_rc="$(run_docker pre_start)"
[ "$docker_pre_rc" -ne 0 ] && ok "Docker falha pre-start retorna erro" || not_ok "Docker falha pre-start retorna erro"
assert_event_order "Docker falha pre-start real" "$TMP_ROOT/docker-pre_start/events.log" backup_success first_mutation failure
assert_no_event "Docker anterior desconhecido nao reinicia" "$TMP_ROOT/docker-pre_start/events.log" start_old_runtime
assert_no_event "Docker pre-start nao inicia runtime novo" "$TMP_ROOT/docker-pre_start/events.log" start_new_runtime
assert_backup_preserved "Docker pre-start preserva pacote" "$TMP_ROOT/docker-pre_start/events.log"
assert_contains "Docker pre-start bloqueia sem prova" "$TMP_ROOT/docker-pre_start/output.log" 'RECOVERY_BLOCKED'

docker_post_rc="$(run_docker post_start)"
[ "$docker_post_rc" -ne 0 ] && ok "Docker falha pos-start retorna erro" || not_ok "Docker falha pos-start retorna erro"
assert_event_order "Docker falha pos-start entra em estado seguro" "$TMP_ROOT/docker-post_start/events.log" start_new_runtime failure stop_or_safe_state
assert_no_event "Docker pos-start nao reativa runtime antigo" "$TMP_ROOT/docker-post_start/events.log" start_old_runtime
assert_contains "Docker pos-start informa pacote" "$TMP_ROOT/docker-post_start/output.log" 'Pacote pre-update:'
assert_contains "Docker pos-start exige restore" "$TMP_ROOT/docker-post_start/output.log" 'Restore oficial, somente apos decisao explicita'
assert_contains "Docker pos-start alerta banco migrado" "$TMP_ROOT/docker-post_start/output.log" 'banco pode ter sido migrado'

docker_inaccessible_rc="$(run_docker inaccessible)"
[ "$docker_inaccessible_rc" -ne 0 ] && ok "Docker DATA_DIR inacessivel retorna erro" || not_ok "Docker DATA_DIR inacessivel retorna erro"
assert_contains "Docker diagnostica DATA_DIR inacessivel" "$TMP_ROOT/docker-inaccessible/output.log" 'diretorio de dados invalido, ambiguo ou inacessivel'
assert_not_contains "Docker inacessivel nao declara estado ausente" "$TMP_ROOT/docker-inaccessible/output.log" 'Nenhum estado persistente encontrado'
assert_no_event "Docker inacessivel nao chama backup" "$TMP_ROOT/docker-inaccessible/events.log" canonical_backup
assert_no_event "Docker inacessivel nao muta" "$TMP_ROOT/docker-inaccessible/events.log" first_mutation
assert_no_event "Docker inacessivel nao inicia runtime" "$TMP_ROOT/docker-inaccessible/events.log" start_new_runtime
assert_file_content "Docker inacessivel preserva DB da fixture" "$TMP_ROOT/docker-inaccessible/repo/data/contabase.db" synthetic-db
assert_file_content "Docker inacessivel preserva upload da fixture" "$TMP_ROOT/docker-inaccessible/repo/data/uploads/file.txt" upload

source_order_rc="$(run_source health_fail)"
[ "$source_order_rc" -ne 0 ] && ok "Source ordem termina no healthcheck injetado" || not_ok "Source ordem termina no healthcheck injetado"
assert_event_order "Source ordem real ate runtime novo" "$TMP_ROOT/source-health_fail/events.log" stop_or_quiesce canonical_backup backup_success first_mutation start_new_runtime stop_or_safe_state
assert_contains "Source registra Beta 7 como origem" "$TMP_ROOT/source-health_fail/args.log" 'backup_previous=v0.1.0-beta.7'
assert_contains "Source mantem Beta 8 como alvo" "$TMP_ROOT/source-health_fail/args.log" 'target_version=v0.1.0-beta.8'

source_success_rc="$(run_source success)"
assert_eq "Source sucesso retorna zero" 0 "$source_success_rc"
assert_event_order "Source ordem real de sucesso" "$TMP_ROOT/source-success/events.log" stop_or_quiesce canonical_backup backup_success first_mutation start_new_runtime

source_backup_rc="$(run_source backup_fail)"
[ "$source_backup_rc" -ne 0 ] && ok "Source backup falho retorna erro" || not_ok "Source backup falho retorna erro"
assert_event_order "Source backup falho para apos helper" "$TMP_ROOT/source-backup_fail/events.log" stop_or_quiesce canonical_backup backup_failure
assert_no_event "Source backup falho sem mutacao" "$TMP_ROOT/source-backup_fail/events.log" first_mutation
assert_no_event "Source backup falho sem runtime novo" "$TMP_ROOT/source-backup_fail/events.log" start_new_runtime
assert_no_event "Source backup falho sem runtime antigo" "$TMP_ROOT/source-backup_fail/events.log" start_old_runtime

source_pre_rc="$(run_source pre_start)"
[ "$source_pre_rc" -ne 0 ] && ok "Source falha pre-start retorna erro" || not_ok "Source falha pre-start retorna erro"
assert_event_order "Source falha pre-start real" "$TMP_ROOT/source-pre_start/events.log" backup_success first_mutation failure
assert_no_event "Source anterior desconhecido nao reinicia" "$TMP_ROOT/source-pre_start/events.log" start_old_runtime
assert_contains "Source pre-start bloqueia sem prova" "$TMP_ROOT/source-pre_start/output.log" 'RECOVERY_BLOCKED'
assert_no_event "Source pre-start nao inicia runtime novo" "$TMP_ROOT/source-pre_start/events.log" start_new_runtime
assert_backup_preserved "Source pre-start preserva pacote" "$TMP_ROOT/source-pre_start/events.log"
assert_not_contains "Source pre-start nao afirma restore" "$TMP_ROOT/source-pre_start/output.log" 'dados foram restaurados'

source_post_rc="$(run_source post_start)"
[ "$source_post_rc" -ne 0 ] && ok "Source falha pos-start retorna erro" || not_ok "Source falha pos-start retorna erro"
assert_event_order "Source falha pos-start entra em estado seguro" "$TMP_ROOT/source-post_start/events.log" start_new_runtime failure stop_or_safe_state
assert_no_event "Source pos-start nao reativa runtime antigo" "$TMP_ROOT/source-post_start/events.log" start_old_runtime
assert_contains "Source pos-start informa pacote" "$TMP_ROOT/source-post_start/output.log" 'Pacote pre-update:'
assert_contains "Source pos-start exige restore" "$TMP_ROOT/source-post_start/output.log" 'Restore oficial, somente apos decisao explicita'
assert_contains "Source pos-start alerta banco migrado" "$TMP_ROOT/source-post_start/output.log" 'banco pode ter sido migrado'

release_order_rc="$(run_release post_start)"
[ "$release_order_rc" -ne 0 ] && ok "Release cenario pos-start retorna erro" || not_ok "Release cenario pos-start retorna erro"
assert_event_order "Release ordem real ate estado seguro" "$TMP_ROOT/release-post_start/events.log" stop_or_quiesce canonical_backup backup_success first_mutation start_new_runtime failure stop_or_safe_state
assert_no_event "Release pos-start nao reativa runtime antigo" "$TMP_ROOT/release-post_start/events.log" start_old_runtime
assert_contains "Release pos-start informa pacote" "$TMP_ROOT/release-post_start/output.log" 'Pacote pre-update:'
assert_contains "Release pos-start exige restore" "$TMP_ROOT/release-post_start/output.log" 'Restore oficial, somente apos decisao explicita'
assert_contains "Release pos-start alerta banco migrado" "$TMP_ROOT/release-post_start/output.log" 'banco pode ter sido migrado'

release_success_rc="$(run_release success)"
assert_eq "Release sucesso retorna zero" 0 "$release_success_rc"
assert_event_order "Release ordem real de sucesso" "$TMP_ROOT/release-success/events.log" stop_or_quiesce canonical_backup backup_success first_mutation start_new_runtime

release_backup_rc="$(run_release backup_fail)"
[ "$release_backup_rc" -ne 0 ] && ok "Release backup falho com servico ativo retorna erro" || not_ok "Release backup falho com servico ativo retorna erro"
assert_event_order "Release backup falho com servico ativo para apos helper" "$TMP_ROOT/release-backup_fail/events.log" stop_or_quiesce canonical_backup backup_failure
assert_no_event "Release backup falho com servico ativo sem mutacao" "$TMP_ROOT/release-backup_fail/events.log" first_mutation
assert_no_event "Release backup falho com servico ativo sem runtime novo" "$TMP_ROOT/release-backup_fail/events.log" start_new_runtime
assert_no_event "Release backup falho com servico ativo sem runtime antigo" "$TMP_ROOT/release-backup_fail/events.log" start_old_runtime

release_backup_inactive_rc="$(run_release backup_fail_inactive)"
[ "$release_backup_inactive_rc" -ne 0 ] && ok "Release backup falho com servico inativo retorna erro" || not_ok "Release backup falho com servico inativo retorna erro"
assert_event_order "Release backup falho preserva servico inativo" "$TMP_ROOT/release-backup_fail_inactive/events.log" service_initially_inactive canonical_backup backup_failure
assert_no_event "Release backup falho com servico inativo sem mutacao" "$TMP_ROOT/release-backup_fail_inactive/events.log" first_mutation
assert_no_event "Release backup falho com servico inativo sem runtime novo" "$TMP_ROOT/release-backup_fail_inactive/events.log" start_new_runtime
assert_no_event "Release backup falho com servico inativo sem runtime antigo" "$TMP_ROOT/release-backup_fail_inactive/events.log" start_old_runtime

release_pre_rc="$(run_release pre_start)"
[ "$release_pre_rc" -ne 0 ] && ok "Release falha pre-start retorna erro" || not_ok "Release falha pre-start retorna erro"
assert_event_order "Release falha pre-start real" "$TMP_ROOT/release-pre_start/events.log" backup_success first_mutation failure
assert_no_event "Release anterior desconhecido nao reinicia" "$TMP_ROOT/release-pre_start/events.log" start_old_runtime
assert_contains "Release pre-start bloqueia sem prova" "$TMP_ROOT/release-pre_start/output.log" 'RECOVERY_BLOCKED'
assert_no_event "Release pre-start nao inicia runtime novo" "$TMP_ROOT/release-pre_start/events.log" start_new_runtime
assert_backup_preserved "Release pre-start preserva pacote" "$TMP_ROOT/release-pre_start/events.log"
assert_contains "Release pre-start nao afirma restore" "$TMP_ROOT/release-pre_start/output.log" 'Dados e configuracao NAO serao restaurados.'

for script in "$REPO_DIR/scripts/update-contabase-source.sh" "$REPO_DIR/scripts/update-contabase-release.sh"; do
  if bash "$script" --no-backup >/dev/null 2>&1; then not_ok "$(basename "$script") rejeita --no-backup"; else ok "$(basename "$script") rejeita --no-backup"; fi
done
if bash "$REPO_DIR/scripts/update-contabase-docker.sh" --no-backup >/dev/null 2>&1; then not_ok "Docker rejeita --no-backup"; else ok "Docker rejeita --no-backup"; fi
assert_not_contains "Docker continua sem fallback cru" "$REPO_DIR/scripts/update-contabase-docker.sh" 'stopped_stack_copy'
assert_not_contains "Docker continua sem copia WAL" "$REPO_DIR/scripts/update-contabase-docker.sh" 'contabase.db-wal'
assert_not_contains "Source continua sem backup paralelo" "$REPO_DIR/scripts/update-contabase-source.sh" 'backup_database()'
assert_contains "Release continua com backup de dados" "$REPO_DIR/scripts/update-contabase-release.sh" 'create_canonical_backup'

printf '=== MATRIZ ENTRYPOINTS: 15 cenarios, %d assertions, %d falhas ===\n' "$((PASS + FAIL))" "$FAIL"
[ "$FAIL" -eq 0 ]

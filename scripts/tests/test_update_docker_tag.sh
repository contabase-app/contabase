#!/usr/bin/env bash
# F2: atualizacao Docker Compose por tag oficial, com Git real e Docker simulado.
# Nao executa Docker real, banco real, backup real ou instalacao real.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UPDATER="$ROOT_DIR/scripts/update-contabase-docker.sh"
REAL_GIT="$(command -v git)"
REAL_PYTHON="$(command -v python3)"
TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-docker-tag.XXXXXX")" || exit 1
TMP_BASE="$(cd "$TMP_BASE" && pwd -P)"
cleanup() {
  if [ "${KEEP_TMP:-0}" = "1" ]; then
    printf 'TMP_BASE preservado: %s\n' "$TMP_BASE" >&2
    return
  fi
  rm -rf "$TMP_BASE"
}
trap cleanup EXIT

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }
assert_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then ok "$label"; else bad "$label (expected='$expected' actual='$actual')"; fi
}
assert_contains() {
  local label="$1" needle="$2" file="$3"
  if grep -Fq -- "$needle" "$file" 2>/dev/null; then ok "$label"; else bad "$label (missing '$needle')"; fi
}
assert_not_contains() {
  local label="$1" needle="$2" file="$3"
  if grep -Fq -- "$needle" "$file" 2>/dev/null; then bad "$label (found '$needle')"; else ok "$label"; fi
}
sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}
mode_of() {
  local value
  if value="$(stat -c '%a' "$1" 2>/dev/null)" && [[ "$value" =~ ^[0-7]{3,4}$ ]]; then
    printf '%s' "$value"
    return 0
  fi
  if value="$(stat -f '%Lp' "$1" 2>/dev/null)" && [[ "$value" =~ ^[0-7]{3,4}$ ]]; then
    printf '%s' "$value"
    return 0
  fi
  printf 'mode_of: could not determine octal mode for %s\n' "$1" >&2
  return 1
}
owner_of() {
  local value
  if value="$(stat -c '%u:%g' "$1" 2>/dev/null)" && [[ "$value" =~ ^[0-9]+:[0-9]+$ ]]; then
    printf '%s' "$value"
    return 0
  fi
  if value="$(stat -f '%u:%g' "$1" 2>/dev/null)" && [[ "$value" =~ ^[0-9]+:[0-9]+$ ]]; then
    printf '%s' "$value"
    return 0
  fi
  printf 'owner_of: could not determine numeric UID:GID for %s\n' "$1" >&2
  return 1
}
state_value() {
  awk -F= -v k="$1" '$1 == k { print substr($0, index($0, "=") + 1); exit }' "$CONF/install-state.env"
}
tag_oid() {
  git -C "$LOCAL" rev-parse --verify "refs/tags/$1^{commit}" 2>/dev/null || true
}
tag_snapshot() {
  git -C "$LOCAL" for-each-ref --format='%(refname) %(objectname)' refs/tags | LC_ALL=C sort
}
count_lines() {
  [ -f "$1" ] || { printf '0'; return; }
  grep -c -- "$2" "$1" 2>/dev/null || true
}
event_index() {
  [ -f "$EVENT_LOG" ] || { printf '0'; return; }
  awk -v want="$1" '$1 == want { print NR; exit }' "$EVENT_LOG"
}
assert_event_order() {
  local label="$1" first="$2" second="$3" a b
  a="$(event_index "$first")"; b="$(event_index "$second")"
  if [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]; then ok "$label"; else bad "$label ($first=$a $second=$b)"; fi
}
assert_file_unchanged() {
  local label="$1" path="$2" expected="$3"
  assert_eq "$label" "$expected" "$(sha_file "$path")"
}

write_marker() {
  local version="$1" kind="$2" ref="$3" commit="$4" previous_version="$5" previous_commit="$6"
  local runtime_version="$7" runtime_image="$8"
  cat > "$CONF/finalize.pending" <<EOF
schema=1
method=docker
target_version=$version
target_kind=$kind
target_ref=$ref
target_commit=$commit
previous_version=$previous_version
previous_commit=$previous_commit
runtime_version=$runtime_version
runtime_image=$runtime_image
EOF
}

write_journal() {
  local target_version="$1" target_commit="$2" channel="${3:-beta}"
  local previous_checkout previous_commit previous_state_version previous_state_commit
  local marker_hash marker_version marker_commit force_reset="${4:-0}"
  local expected_image="${5:-sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"

  previous_checkout="$(tr -d '[:space:]' < "$LOCAL/VERSION")"
  previous_commit="$(git -C "$LOCAL" rev-parse HEAD)"
  previous_state_version="$(state_value CONTABASE_INSTALLED_VERSION)"
  previous_state_commit="$(state_value CONTABASE_INSTALL_COMMIT)"
  [ -n "$previous_state_version" ] || previous_state_version="none"
  [ -n "$previous_state_commit" ] || previous_state_commit="none"
  if [ -f "$CONF/finalize.pending" ]; then
    marker_hash="$(git -C "$LOCAL" hash-object --no-filters "$CONF/finalize.pending")"
    marker_version="$(awk -F= '$1=="target_version"{print $2}' "$CONF/finalize.pending")"
    marker_commit="$(awk -F= '$1=="target_commit"{print $2}' "$CONF/finalize.pending")"
  else
    marker_hash="none"
    marker_version="none"
    marker_commit="none"
  fi
  cat > "$CONF/transition.pending" <<EOF
schema=2
expected_image=$expected_image
method=docker
target_version=$target_version
target_kind=tag
target_ref=$target_version
target_commit=$target_commit
target_origin=https://github.com/contabase-app/contabase.git
channel=$channel
previous_checkout_version=$previous_checkout
previous_commit=$previous_commit
previous_state_version=$previous_state_version
previous_state_commit=$previous_state_commit
previous_marker_hash=$marker_hash
previous_marker_version=$marker_version
previous_marker_commit=$marker_commit
force_reset=$force_reset
EOF
}

make_malicious_remote() {
  local dir="$1" marker="$2"
  mkdir -p "$dir/scripts/ops" "$dir/scripts/lib" "$dir/templates" "$dir/assets"
  git -C "$dir" init -q -b main
  git -C "$dir" config user.email synthetic@example.invalid
  git -C "$dir" config user.name Synthetic
  printf 'v1.0.0-beta.8\n' > "$dir/VERSION"
  cat > "$dir/scripts/ops/backup.sh" <<EOF
#!/bin/sh
printf 'executed\n' > "$marker"
exit 0
EOF
  chmod +x "$dir/scripts/ops/backup.sh"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "malicious beta8"
  git -C "$dir" tag v1.0.0-beta.8
}

make_remote() {
  local base="$1"
  REMOTE="$base/remote"
  mkdir -p "$REMOTE"
  git -C "$REMOTE" init -q -b main
  git -C "$REMOTE" config user.email synthetic@example.invalid
  git -C "$REMOTE" config user.name Synthetic
}

commit_version() {
  local version="$1" tag="$2" tag_kind="$3" msg="$4" include_state_lib="${5:-1}"
  mkdir -p "$REMOTE/scripts/ops" "$REMOTE/scripts/lib" "$REMOTE/templates" "$REMOTE/assets"
  cp "$ROOT_DIR/scripts/lib/contabase-update-wrapper.sh" "$REMOTE/scripts/lib/"
  if [ "$include_state_lib" = "1" ]; then
    cp "$ROOT_DIR/scripts/lib/contabase-install-state.sh" "$REMOTE/scripts/lib/"
  else
    rm -f "$REMOTE/scripts/lib/contabase-install-state.sh"
  fi
  cp "$ROOT_DIR/scripts/lib/contabase-recovery.sh" "$REMOTE/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-docker-image-update.py" "$REMOTE/scripts/lib/"
  printf '%s\n' "$version" > "$REMOTE/VERSION"
  printf 'module github.com/contabase-app/contabase\n\ngo 1.23\n' > "$REMOTE/go.mod"
  printf 'FROM alpine:3.21\n' > "$REMOTE/Dockerfile"
  cat > "$REMOTE/docker-compose.yml" <<'COMPOSE'
services:
  contabase:
    image: contabase:fixture
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
COMPOSE
  cat > "$REMOTE/scripts/ops/backup.sh" <<'BK'
#!/bin/sh
set -eu
if [ -n "${BACKUP_FAIL_FILE:-}" ]; then
  : > "$BACKUP_FAIL_FILE"
  exit 1
fi
dest="${1%/}/backup-${APP_VERSION:-unknown}"
mkdir -p "$dest/uploads"
cp "$DB_FILE" "$dest/database.sqlite"
printf 'backup_id=fixture-backup\n' > "$dest/manifest.txt"
: > "$dest/checksums.sha256"
printf 'event:backup\n' >> "${EVENT_LOG:-/dev/null}"
printf '%s\n' "$dest"
BK
  chmod +x "$REMOTE/scripts/ops/backup.sh"
  git -C "$REMOTE" add -A
  git -C "$REMOTE" commit -q -m "$msg"
  case "$tag_kind" in
    annotated) git -C "$REMOTE" tag -a "$tag" -m "$tag" ;;
    lightweight|"") git -C "$REMOTE" tag "$tag" ;;
    none) ;;
  esac
  git -C "$REMOTE" rev-parse HEAD
}

make_stubs() {
  local base="$1" bin="$base/bin"
  mkdir -p "$bin" "$base/tmp"
  cat > "$bin/id" <<'EOF'
#!/bin/sh
echo 0
EOF
  cat > "$bin/docker" <<'EOF'
#!/bin/sh
DOCKER_LOG="${DOCKER_LOG:-/dev/null}"
EVENT_LOG="${EVENT_LOG:-/dev/null}"
printf '%s\n' "$*" >> "$DOCKER_LOG"
container_id="${DOCKER_CONTAINER_ID:-contabase-test-container}"
emit_event() { printf 'event:%s\n' "$1" >> "$EVENT_LOG"; }
if [ "$1" = "compose" ]; then
  shift
  sub=""
  for a in "$@"; do
    case "$a" in config|build|ps|exec|down|up|port|logs) sub="$a"; break ;; esac
  done
  case "$sub" in
    config)
      case " $* " in
        *' --format json '*) printf '{"name":"fixture-project","services":{"contabase":{"build":{"context":"."},"image":"contabase:fixture"}}}\n' ;;
        *) printf 'fixture-compose-config\n' ;;
      esac
      exit 0
      ;;
    build)
      emit_event build
      if [ -n "${DOCKER_BUILD_FAIL_ONCE_FILE:-}" ] && [ ! -e "$DOCKER_BUILD_FAIL_ONCE_FILE" ]; then
        : > "$DOCKER_BUILD_FAIL_ONCE_FILE"
        exit 1
      fi
      exit 0
      ;;
    ps)
      if [ -f "${DOCKER_RUNNING_FILE:-/nonexistent}" ]; then
        if [ "${DOCKER_HIDE_CONTAINER_AFTER_UP:-0}" = "1" ] \
          && [ -f "${DOCKER_NEW_RUNTIME_FILE:-/nonexistent}" ]; then
          exit 0
        fi
        printf '%s\n' "$container_id"
        if [ -n "${DOCKER_EXTRA_CONTAINER_ID_AFTER_UP:-}" ] \
          && [ -f "${DOCKER_NEW_RUNTIME_FILE:-/nonexistent}" ]; then
          printf '%s\n' "$DOCKER_EXTRA_CONTAINER_ID_AFTER_UP"
        fi
      fi
      exit 0
      ;;
    down)
      emit_event down
      rm -f "${DOCKER_RUNNING_FILE:-/nonexistent}" "${DOCKER_NEW_RUNTIME_FILE:-/nonexistent}"
      exit "${DOCKER_DOWN_RC:-0}"
      ;;
    up)
      emit_event up
      if [ -n "${DOCKER_COMPOSE_CAPTURE:-}" ]; then
        : > "$DOCKER_COMPOSE_CAPTURE"
        printf '%s\n' "$*" >> "$DOCKER_COMPOSE_CAPTURE"
        expect_file=0
        for a in "$@"; do
          if [ "$expect_file" = "1" ]; then
            if [ -f "$a" ]; then printf 'content=%s\n' "$(tr '\n' '|' < "$a")" >> "$DOCKER_COMPOSE_CAPTURE"; fi
            expect_file=0
            continue
          fi
          case "$a" in -f) expect_file=1 ;; esac
        done
      fi
      if [ -n "${DOCKER_UP_FAIL_ONCE_FILE:-}" ] && [ ! -f "$DOCKER_UP_FAIL_ONCE_FILE" ]; then
        : > "$DOCKER_UP_FAIL_ONCE_FILE"
        exit 1
      fi
      if [ -n "${DOCKER_START_FAIL_ONCE_FILE:-}" ] && [ ! -f "$DOCKER_START_FAIL_ONCE_FILE" ]; then
        : > "$DOCKER_START_FAIL_ONCE_FILE"
        exit 0
      fi
      [ -z "${DOCKER_RUNNING_FILE:-}" ] || : > "$DOCKER_RUNNING_FILE"
      [ -z "${DOCKER_NEW_RUNTIME_FILE:-}" ] || : > "$DOCKER_NEW_RUNTIME_FILE"
      if [ "${DOCKER_KEEP_RUNTIME_ON_UP:-0}" != "1" ] && [ -n "${DOCKER_RUNTIME_VERSION_FILE:-}" ]; then
        printf '%s\n' "${DOCKER_TARGET_VERSION:-}" > "$DOCKER_RUNTIME_VERSION_FILE"
      fi
      exit 0
      ;;
    port) printf '0.0.0.0:8080\n' ;;
    logs) exit 0 ;;
    *) exit 0 ;;
  esac
fi
case "$1" in
  inspect)
    case "$*" in
      *'{{.Id}}'*) printf '%s\n' "$container_id" ;;
      *'com.docker.compose.service'*) printf 'contabase\n' ;;
      *'com.docker.compose.project.working_dir'*) printf '%s\n' "${DOCKER_PROJECT_WORKDIR:?}" ;;
      *'com.docker.compose.project'*) printf 'fixture-project\n' ;;
      *'range .Mounts'*)
        if [ -n "${DOCKER_MOUNTS_FILE_AFTER_UP:-}" ] \
          && [ -f "${DOCKER_NEW_RUNTIME_FILE:-/nonexistent}" ]; then
          cat "$DOCKER_MOUNTS_FILE_AFTER_UP"
        elif [ -n "${DOCKER_MOUNTS_FILE:-}" ] && [ -f "$DOCKER_MOUNTS_FILE" ]; then
          cat "$DOCKER_MOUNTS_FILE"
        else
          printf '/app/data\n'
        fi
        ;;
      *'State.Health'*) printf '%s\n' "${DOCKER_HEALTH_STATUS:-healthy}" ;;
      *'RestartCount'*)
        if [ -n "${DOCKER_RESTART_COUNT_FILE:-}" ] && [ -f "$DOCKER_RESTART_COUNT_FILE" ]; then
          cat "$DOCKER_RESTART_COUNT_FILE"
        else
          printf '0\n'
        fi
        ;;
      *'Config.Image'*) printf 'contabase:fixture-old\n' ;;
      *'{{.Image}}'*)
        if [ -f "${DOCKER_NEW_RUNTIME_FILE:-/nonexistent}" ]; then
          printf 'sha256:%s\n' "${DOCKER_ACTIVATED_ID:-${DOCKER_CANDIDATE_ID:-$DOCKER_IMAGE_ID}}"
        else
          printf 'sha256:%s\n' "${DOCKER_IMAGE_ID:?}"
        fi
        ;;
      *) exit 1 ;;
    esac
    ;;
  exec)
    [ -f "${DOCKER_RUNTIME_VERSION_FILE:-/nonexistent}" ] || exit 65
    cat "$DOCKER_RUNTIME_VERSION_FILE"
    ;;
  image)
    if [ "$2" = inspect ]; then printf 'sha256:%s\n' "${DOCKER_CANDIDATE_ID:-$DOCKER_IMAGE_ID}"; fi
    exit 0
    ;;
  *) exit 0 ;;
esac
exit 0
EOF
  cat > "$bin/curl" <<'EOF'
#!/bin/sh
if [ -n "${HEALTH_BAD_FILE:-}" ] && [ -f "$HEALTH_BAD_FILE" ]; then
  printf '{"status":"unhealthy"}\n'
  exit 0
fi
printf '{"status":"healthy"}\n'
EOF
  cat > "$bin/sleep" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat > "$bin/python3" <<EOF
#!/bin/sh
case "\$3" in
  */contabase-docker-image-update.py)
    printf '%s\n' "\$@" >> "\${CONTABASE_TEST_DISPATCH_LOG:?}"
    exit 0
    ;;
esac
if [ "\$3" = "-" ]; then
  case "\$4" in
    probe|replace|remove|check|confirm)
      exec "$REAL_PYTHON" -I -B "$bin/fsync-probe.py" "\$4" "\$5" "\$6"
      ;;
  esac
fi
exec "$REAL_PYTHON" "\$@"
EOF
  cat > "$bin/fsync-probe.py" <<'PYTHON'
import os
from pathlib import Path
import stat
import sys

operation, path, source = sys.argv[1:]
destination = Path(path).name
fsync = os.fsync


def observed_fsync(fd):
    kind = "dir" if stat.S_ISDIR(os.fstat(fd).st_mode) else "file"
    event = f"{operation}:{destination}:{kind}"
    with open(os.environ.get("FSYNC_LOG", os.devnull), "a") as log:
        print(event, file=log)
    failure = os.environ.get("FAIL_FSYNC")
    if failure == event or (failure == "expected_image_" + kind and operation == "replace"
                            and destination == "transition.pending"
                            and "expected_image=sha256:" in Path(source).read_text()):
        raise OSError("fsync adversarial fixture")
    for var, expected in (("MARKER_FAIL_ONCE_FILE", "replace:finalize.pending:file"),
                          ("PERSIST_FAIL_ONCE_FILE", "replace:install-state.env:file")):
        marker = os.environ.get(var)
        if marker and event == expected and not Path(marker).exists():
            Path(marker).touch()
            raise OSError("fsync fixture")
    return fsync(fd)


unlink = os.unlink


def observed_unlink(path, *args, **kwargs):
    marker = os.environ.get("RM_FAIL_FILE")
    if marker and path == "finalize.pending" and not Path(marker).exists():
        Path(marker).touch()
        raise OSError("unlink fixture")
    return unlink(path, *args, **kwargs)


os.fsync = observed_fsync
os.unlink = observed_unlink
exec(compile(sys.stdin.read(), "<updater fsync>", "exec"))
PYTHON
  cat > "$bin/git" <<EOF
#!/usr/bin/env bash
REAL_GIT="$REAL_GIT"
if [ -n "\${GIT_LOG:-}" ]; then printf '%s\n' "\$*" >> "\$GIT_LOG"; fi
args=("\$@")
if [ "\${args[0]:-}" = "fetch" ] && [ "\${TEST_GIT_TRANSLATE:-1}" = "1" ] && [ -n "\${TEST_REMOTE_DIR:-}" ]; then
  for ((i=0; i<\${#args[@]}; i++)); do
    case "\${args[\$i]}" in
      https://github.com/contabase-app/contabase.git|https://github.com/contabase-app/contabase)
        args[\$i]="\$TEST_REMOTE_DIR"
        ;;
    esac
  done
fi
exec "\$REAL_GIT" "\${args[@]}"
EOF
  chmod +x "$bin"/*
}

setup_case() {
  local name="$1" channel="${2:-beta}" series="${3:-beta}" legacy_old="${4:-0}"
  local base="$TMP_BASE/$name"
  local baseline_tag old_tag target_tag later_tag
  case "$series" in
    stable)
      baseline_tag=v0.9.0; old_tag=v1.0.0; target_tag=v1.0.1; later_tag=v1.0.2
      ;;
    *)
      baseline_tag=v1.0.0-beta.6; old_tag=v1.0.0-beta.7; target_tag=v1.0.0-beta.8; later_tag=v1.0.0-beta.9
      ;;
  esac
  rm -rf "$base"
  mkdir -p "$base"
  make_remote "$base"
  commit_version "$baseline_tag" "$baseline_tag" lightweight "baseline" >/dev/null
  local old_oid target_oid later_oid
  if [ "$legacy_old" = "1" ]; then
    old_oid="$(commit_version "$old_tag" "$old_tag" annotated "old" 0)"
  else
    old_oid="$(commit_version "$old_tag" "$old_tag" annotated "old")"
  fi
  target_oid="$(commit_version "$target_tag" "$target_tag" lightweight "target")"
  later_oid="$(commit_version "$later_tag" "$later_tag" annotated "later")"

  LOCAL="$base/local"
  git clone -q --branch "$old_tag" --depth 1 "file://$REMOTE" "$LOCAL"
  git -C "$LOCAL" config user.email synthetic@example.invalid
  git -C "$LOCAL" config user.name Synthetic
  git -C "$LOCAL" remote set-url origin https://github.com/contabase-app/contabase.git
  printf '%s\n' 'scripts/update-contabase-docker.sh' '.env.docker' '.env.docker.pre-update-*' 'data/' 'docker-compose.override.yml' > "$LOCAL/.git/info/exclude"

  CONF="$base/config"; BIN="$base/bin"
  mkdir -p "$CONF" "$base/tmp"
  make_stubs "$base"
  cp "$UPDATER" "$LOCAL/scripts/update-contabase-docker.sh"
  chmod +x "$LOCAL/scripts/update-contabase-docker.sh"

  cat > "$LOCAL/.env.docker" <<'ENV'
AUTH_ENCRYPTION_KEY=fixture-auth
SECURITY_MASTER_KEY=fixture-security
CONTABASE_TIMEZONE=America/Sao_Paulo
PORT=8080
ENV
  chmod 0600 "$LOCAL/.env.docker"
  cat > "$LOCAL/docker-compose.override.yml" <<'OVR'
services:
  contabase:
    restart: unless-stopped
OVR
  chmod 0640 "$LOCAL/docker-compose.override.yml"
  mkdir -p "$LOCAL/data/uploads"
  printf 'synthetic-db\n' > "$LOCAL/data/contabase.db"
  printf 'synthetic-wal\n' > "$LOCAL/data/contabase.db-wal"
  printf 'synthetic-shm\n' > "$LOCAL/data/contabase.db-shm"
  printf 'synthetic-upload\n' > "$LOCAL/data/uploads/file.txt"
  chmod 0750 "$LOCAL/data" "$LOCAL/data/uploads"
  chmod 0640 "$LOCAL/data/contabase.db" "$LOCAL/data/contabase.db-wal" "$LOCAL/data/contabase.db-shm" "$LOCAL/data/uploads/file.txt"

  cat > "$CONF/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=docker
CONTABASE_CHANNEL=$channel
CONTABASE_INSTALLED_VERSION=$old_tag
CONTABASE_REPO_PATH=$LOCAL
CONTABASE_INSTALL_REF_KIND=tag
CONTABASE_INSTALL_REF=$old_tag
CONTABASE_INSTALL_COMMIT=$old_oid
CONTABASE_INSTALL_ORIGIN=https://github.com/contabase-app/contabase.git
CONTABASE_LOCAL_NOTE=preservar
EOF
  printf 'docker\n%s\n' "$LOCAL" > "$CONF/install-mode"

  RUNTIME_RUNNING="$base/runtime-running"
  RUNTIME_VERSION="$base/runtime-version"
  NEW_RUNTIME_FILE="$base/new-runtime"
  GIT_LOG="$base/git.log"
  DOCKER_LOG="$base/docker.log"
  EVENT_LOG="$base/events.log"
  FSYNC_LOG="$base/fsync.log"
  DOCKER_COMPOSE_CAPTURE="$base/compose-capture"
  : > "$RUNTIME_RUNNING"
  printf '%s\n' "$old_tag" > "$RUNTIME_VERSION"

  C="$old_oid"; A="$target_oid"; L="$later_oid"
  TARGET_TAG="$target_tag"; OLD_TAG="$old_tag"; LATER_TAG="$later_tag"
}

setup_branch_case() {
  local name="$1" channel="${2:-beta}"
  local base="$TMP_BASE/$name"
  rm -rf "$base"
  mkdir -p "$base"
  make_remote "$base"
  commit_version v1.0.0-beta.6 v1.0.0-beta.6 lightweight "beta6" >/dev/null
  local old_oid
  old_oid="$(commit_version v1.0.0-beta.7 v1.0.0-beta.7 annotated "beta7")"

  LOCAL="$base/local"
  git clone -q "file://$REMOTE" "$LOCAL"
  git -C "$LOCAL" config user.email synthetic@example.invalid
  git -C "$LOCAL" config user.name Synthetic
  git -C "$LOCAL" remote set-url origin https://github.com/contabase-app/contabase.git
  printf '%s\n' 'scripts/update-contabase-docker.sh' '.env.docker' '.env.docker.pre-update-*' 'data/' 'docker-compose.override.yml' > "$LOCAL/.git/info/exclude"

  CONF="$base/config"; BIN="$base/bin"
  mkdir -p "$CONF" "$base/tmp"
  make_stubs "$base"
  cp "$UPDATER" "$LOCAL/scripts/update-contabase-docker.sh"
  chmod +x "$LOCAL/scripts/update-contabase-docker.sh"
  cat > "$LOCAL/.env.docker" <<'ENV'
AUTH_ENCRYPTION_KEY=fixture-auth
SECURITY_MASTER_KEY=fixture-security
CONTABASE_TIMEZONE=America/Sao_Paulo
PORT=8080
ENV
  chmod 0600 "$LOCAL/.env.docker"
  cat > "$LOCAL/docker-compose.override.yml" <<'OVR'
services:
  contabase:
    restart: unless-stopped
OVR
  mkdir -p "$LOCAL/data/uploads"
  printf 'synthetic-db\n' > "$LOCAL/data/contabase.db"
  printf 'synthetic-upload\n' > "$LOCAL/data/uploads/file.txt"
  cat > "$CONF/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=docker
CONTABASE_CHANNEL=$channel
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
CONTABASE_REPO_PATH=$LOCAL
CONTABASE_INSTALL_REF_KIND=branch
CONTABASE_INSTALL_REF=main
CONTABASE_INSTALL_COMMIT=$old_oid
CONTABASE_INSTALL_ORIGIN=https://github.com/contabase-app/contabase.git
EOF
  printf 'docker\n%s\n' "$LOCAL" > "$CONF/install-mode"

  RUNTIME_RUNNING="$base/runtime-running"
  RUNTIME_VERSION="$base/runtime-version"
  NEW_RUNTIME_FILE="$base/new-runtime"
  GIT_LOG="$base/git.log"
  DOCKER_LOG="$base/docker.log"
  EVENT_LOG="$base/events.log"
  FSYNC_LOG="$base/fsync.log"
  DOCKER_COMPOSE_CAPTURE="$base/compose-capture"
  : > "$RUNTIME_RUNNING"
  printf 'v1.0.0-beta.7\n' > "$RUNTIME_VERSION"

  C="$old_oid"; TARGET_TAG=""; OLD_TAG=v1.0.0-beta.7; LATER_TAG=v1.0.0-beta.9
}

make_external_bundle() {
  local base="$1" version="$2" dest="$1/external"
  rm -rf "$dest"
  mkdir -p "$dest/scripts/lib"
  cp "$UPDATER" "$dest/scripts/update-contabase-docker.sh"
  cp "$ROOT_DIR/scripts/update-contabase-source.sh" "$dest/scripts/update-contabase-source.sh"
  cp "$ROOT_DIR/scripts/update-contabase-release.sh" "$dest/scripts/update-contabase-release.sh"
  cp "$ROOT_DIR/scripts/lib/contabase-install-state.sh" "$dest/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-recovery.sh" "$dest/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-update-wrapper.sh" "$dest/scripts/lib/"
  chmod +x "$dest/scripts/update-contabase-docker.sh" \
    "$dest/scripts/update-contabase-source.sh" "$dest/scripts/update-contabase-release.sh"
  printf '%s\n' "$version" > "$dest/VERSION"
  cp "$ROOT_DIR/scripts/lib/contabase-docker-image-update.py" "$dest/scripts/lib/"
  EXTERNAL="$dest"
  BUNDLE_COMMIT="$A"
  (
    cd "$dest"
    printf 'commit=%s\n' "$BUNDLE_COMMIT"
    for file in VERSION scripts/update-contabase-docker.sh scripts/update-contabase-source.sh \
      scripts/update-contabase-release.sh scripts/lib/contabase-install-state.sh \
      scripts/lib/contabase-recovery.sh scripts/lib/contabase-update-wrapper.sh \
      scripts/lib/contabase-docker-image-update.py; do
      printf '%s  %s\n' "$(sha_file "$file")" "$file"
    done | LC_ALL=C sort -k2,2
  ) > "$dest/updater.sha256"
  BUNDLE_DIGEST="$(sha_file "$dest/updater.sha256")"
}

run_update() {
  local base="$1" target="$2"; shift 2
  local rc=0 workdir="${RUN_WORKDIR:-$LOCAL}"
  local updater="${RUN_UPDATER:-$LOCAL/scripts/update-contabase-docker.sh}"
  (
    cd "$workdir"
    export PATH="$BIN:$PATH"
    export TMPDIR="$base/tmp"
    export CONTABASE_REPO_ROOT="$LOCAL"
    if [ -n "${RUN_UPDATER:-}" ]; then
      export CONTABASE_UPDATER_BUNDLE_COMMIT="${BUNDLE_COMMIT:-}"
      export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="${BUNDLE_DIGEST:-}"
    fi
    export FSYNC_LOG="$base/fsync.log"
    export CONTABASE_STATE_FILE="$CONF/install-state.env"
    export CONTABASE_FINALIZE_MARKER="$CONF/finalize.pending"
    export CONTABASE_TRANSITION_JOURNAL="$CONF/transition.pending"
    export CONTABASE_MODE_FILE="$CONF/install-mode"
    export CONTABASE_UPDATE_WRAPPER="${RUN_UPDATE_WRAPPER:-$base/contabase-update}"
    export CONTABASE_UPDATE_ALIAS="$base/cb-update"
    export CONTABASE_ASSUME_YES=1
    export CONTABASE_TEST_DISPATCH_LOG="$base/dispatch.log"
    export GIT_LOG="$base/git.log"
    export DOCKER_LOG="$base/docker.log"
    export EVENT_LOG="$base/events.log"
    export HEALTH_BAD_FILE="$base/health-bad"
    export TEST_REMOTE_DIR="$REMOTE"
    export TEST_GIT_TRANSLATE="${TEST_GIT_TRANSLATE:-1}"
    export DOCKER_RUNNING_FILE="$RUNTIME_RUNNING"
    export DOCKER_RUNTIME_VERSION_FILE="$RUNTIME_VERSION"
    export DOCKER_NEW_RUNTIME_FILE="$NEW_RUNTIME_FILE"
    export DOCKER_TARGET_VERSION="$target"
    export DOCKER_IMAGE_ID="${RUNTIME_IMAGE_FIXTURE:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"
    export DOCKER_PROJECT_WORKDIR="$LOCAL"
    export DOCKER_COMPOSE_CAPTURE="$base/compose-capture"
    export HEALTHCHECK_ATTEMPTS=1
    if [ -n "$target" ]; then export CONTABASE_VERSION="$target"; else unset CONTABASE_VERSION; fi
    if [ -n "${RUN_HOME:-}" ]; then export HOME="$RUN_HOME"; fi
    if [ -n "${DOCKER_UP_FAIL_ONCE_FILE:-}" ]; then export DOCKER_UP_FAIL_ONCE_FILE; fi
    if [ -n "${DOCKER_START_FAIL_ONCE_FILE:-}" ]; then export DOCKER_START_FAIL_ONCE_FILE; fi
    if [ -n "${MARKER_FAIL_ONCE_FILE:-}" ]; then export MARKER_FAIL_ONCE_FILE; fi
    if [ -n "${PERSIST_FAIL_ONCE_FILE:-}" ]; then export PERSIST_FAIL_ONCE_FILE; fi
    if [ -n "${RM_FAIL_FILE:-}" ]; then export RM_FAIL_FILE; fi
    if [ -n "${DOCKER_HEALTH_STATUS:-}" ]; then export DOCKER_HEALTH_STATUS; fi
    if [ -n "${DOCKER_HIDE_CONTAINER_AFTER_UP:-}" ]; then export DOCKER_HIDE_CONTAINER_AFTER_UP; fi
    if [ -n "${DOCKER_EXTRA_CONTAINER_ID_AFTER_UP:-}" ]; then export DOCKER_EXTRA_CONTAINER_ID_AFTER_UP; fi
    if [ -n "${DOCKER_KEEP_RUNTIME_ON_UP:-}" ]; then export DOCKER_KEEP_RUNTIME_ON_UP; fi
    bash "$updater" "$@" >"$base/out.log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}

reset_logs() {
  : > "$GIT_LOG"; : > "$DOCKER_LOG"; : > "$EVENT_LOG"
  [ -z "${FSYNC_LOG:-}" ] || : > "$FSYNC_LOG"
  rm -f "$DOCKER_COMPOSE_CAPTURE"
}

# ---------------------------------------------------------------------------
# Estrutura minima e contrato de dependencias externas (F3).
# ---------------------------------------------------------------------------
assert_contains "estrutura: repo root explicito" "CONTABASE_REPO_ROOT" "$UPDATER"
assert_contains "estrutura: biblioteca de estado" "contabase-install-state.sh" "$UPDATER"
assert_contains "estrutura: biblioteca de recovery" "contabase-recovery.sh" "$UPDATER"
assert_contains "estrutura: wrapper de update" "contabase-update-wrapper.sh" "$UPDATER"
assert_contains "estrutura: recusa force-reset em tag" "--force-reset e recusado em instalacoes por tag" "$UPDATER"
assert_contains "estrutura: recusa skip-pull em tag" "--skip-pull e recusado em instalacoes por tag" "$UPDATER"

# ---------------------------------------------------------------------------
# 1. Tag anotada -> lightweight, shallow, detached, backup e dados.
# ---------------------------------------------------------------------------
setup_case success
before_db="$(sha_file "$LOCAL/data/contabase.db")"
before_wal="$(sha_file "$LOCAL/data/contabase.db-wal")"
before_shm="$(sha_file "$LOCAL/data/contabase.db-shm")"
before_upload="$(sha_file "$LOCAL/data/uploads/file.txt")"
before_env="$(sha_file "$LOCAL/.env.docker")"
before_override="$(sha_file "$LOCAL/docker-compose.override.yml")"
before_db_mode="$(mode_of "$LOCAL/data/contabase.db")"
before_env_mode="$(mode_of "$LOCAL/.env.docker")"
before_data_mode="$(mode_of "$LOCAL/data")"
before_upload_mode="$(mode_of "$LOCAL/data/uploads/file.txt")"
before_db_owner="$(owner_of "$LOCAL/data/contabase.db")"
before_env_owner="$(owner_of "$LOCAL/.env.docker")"
before_data_owner="$(owner_of "$LOCAL/data")"
before_upload_owner="$(owner_of "$LOCAL/data/uploads/file.txt")"
before_tag="$(tag_oid "$OLD_TAG")"
assert_eq "fixture tag e shallow" "true" "$(git -C "$LOCAL" rev-parse --is-shallow-repository)"
rc="$(run_update "$TMP_BASE/success" "$TARGET_TAG" --yes)"
assert_eq "tag sucesso rc" "0" "$rc"
if [ "$rc" = "0" ]; then
  assert_eq "tag checkout detached" "" "$(git -C "$LOCAL" branch --show-current)"
  assert_eq "tag HEAD no alvo" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
  assert_eq "tag shallow expandido" "false" "$(git -C "$LOCAL" rev-parse --is-shallow-repository)"
  assert_eq "tag estado versao" "$TARGET_TAG" "$(state_value CONTABASE_INSTALLED_VERSION)"
  assert_eq "tag estado kind" "tag" "$(state_value CONTABASE_INSTALL_REF_KIND)"
  assert_eq "tag estado ref" "$TARGET_TAG" "$(state_value CONTABASE_INSTALL_REF)"
  assert_eq "tag estado commit" "$A" "$(state_value CONTABASE_INSTALL_COMMIT)"
  assert_eq "tag estado origem" "https://github.com/contabase-app/contabase.git" "$(state_value CONTABASE_INSTALL_ORIGIN)"
  assert_eq "tag canal beta" "beta" "$(state_value CONTABASE_CHANNEL)"
  assert_eq "tag campo desconhecido preservado" "preservar" "$(state_value CONTABASE_LOCAL_NOTE)"
  assert_eq "tag local alvo criada" "$A" "$(tag_oid "$TARGET_TAG")"
  assert_eq "tag instalada anterior preservada" "$before_tag" "$(tag_oid "$OLD_TAG")"
  assert_eq "tag worktree limpo" "" "$(git -C "$LOCAL" status --porcelain)"
  assert_eq "marcador removido" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
  assert_eq "journal removido" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
  assert_eq "backup canonico criado" "yes" "$([ -n "$(find "$LOCAL/data/backups" -maxdepth 1 -type d -name 'backup-*' -print -quit 2>/dev/null)" ] && echo yes || echo no)"
  assert_file_unchanged "DB preservado" "$LOCAL/data/contabase.db" "$before_db"
  assert_file_unchanged "WAL preservado" "$LOCAL/data/contabase.db-wal" "$before_wal"
  assert_file_unchanged "SHM preservado" "$LOCAL/data/contabase.db-shm" "$before_shm"
  assert_file_unchanged "upload preservado" "$LOCAL/data/uploads/file.txt" "$before_upload"
  assert_file_unchanged ".env.docker preservado" "$LOCAL/.env.docker" "$before_env"
  assert_file_unchanged "override preservado" "$LOCAL/docker-compose.override.yml" "$before_override"
  assert_eq "modo DB preservado" "$before_db_mode" "$(mode_of "$LOCAL/data/contabase.db")"
  assert_eq "modo env preservado" "$before_env_mode" "$(mode_of "$LOCAL/.env.docker")"
  assert_eq "modo data preservado" "$before_data_mode" "$(mode_of "$LOCAL/data")"
  assert_eq "modo upload preservado" "$before_upload_mode" "$(mode_of "$LOCAL/data/uploads/file.txt")"
  assert_eq "UID/GID DB preservado" "$before_db_owner" "$(owner_of "$LOCAL/data/contabase.db")"
  assert_eq "UID/GID env preservado" "$before_env_owner" "$(owner_of "$LOCAL/.env.docker")"
  assert_eq "UID/GID data preservado" "$before_data_owner" "$(owner_of "$LOCAL/data")"
  assert_eq "UID/GID upload preservado" "$before_upload_owner" "$(owner_of "$LOCAL/data/uploads/file.txt")"
  assert_contains "snapshot usa build context canonico" "contabase-build-snapshot" "$TMP_BASE/success/compose-capture"
  assert_contains "snapshot injeta VERSION alvo" "VERSION: $TARGET_TAG" "$TMP_BASE/success/compose-capture"
  assert_contains "snapshot archive usa OID alvo" "archive --format=tar $A" "$TMP_BASE/success/git.log"
  assert_contains "fetch exato da tag em ref privada" "+refs/tags/${TARGET_TAG}:refs/contabase-update/target" "$TMP_BASE/success/git.log"
  assert_contains "fetch usa origem oficial explicita" "fetch --no-tags https://github.com/contabase-app/contabase.git +refs/tags/${TARGET_TAG}" "$TMP_BASE/success/git.log"
  assert_not_contains "down nao remove volumes" " -v" "$TMP_BASE/success/docker.log"
  assert_not_contains "up nao remove volumes" "--volumes" "$TMP_BASE/success/docker.log"
  assert_event_order "backup antes do rebuild" event:backup event:build
  assert_event_order "build separado antes do startup" event:build event:up
  assert_event_order "parada antes do backup" event:down event:backup
  assert_contains "tag lightweight reconhecida" "Tag alvo resolvida: ${TARGET_TAG} (commit)" "$TMP_BASE/success/out.log"
else
  bad "tag sucesso falhou"; sed -n '1,160p' "$TMP_BASE/success/out.log" >&2
fi

assert_contains "fsync do temporario do journal" "replace:transition.pending:file" "$FSYNC_LOG"
assert_contains "fsync do diretorio apos rename de estado" "replace:install-state.env:dir" "$FSYNC_LOG"
assert_contains "fsync do diretorio apos remover journal" "remove:transition.pending:dir" "$FSYNC_LOG"
assert_contains "fsync do diretorio apos remover marcador" "remove:finalize.pending:dir" "$FSYNC_LOG"

# ---------------------------------------------------------------------------
# 2. Segundo update consecutivo por tag anotada.
# ---------------------------------------------------------------------------
if [ "$rc" = "0" ]; then
  reset_logs
  rc="$(run_update "$TMP_BASE/success" "$LATER_TAG" --yes)"
  assert_eq "segunda tag rc" "0" "$rc"
  if [ "$rc" = "0" ]; then
    assert_eq "segunda tag estado" "$LATER_TAG" "$(state_value CONTABASE_INSTALLED_VERSION)"
    assert_eq "segunda tag ref" "$LATER_TAG" "$(state_value CONTABASE_INSTALL_REF)"
    assert_eq "segunda tag HEAD" "$L" "$(git -C "$LOCAL" rev-parse HEAD)"
    assert_eq "segunda tag commit estado" "$L" "$(state_value CONTABASE_INSTALL_COMMIT)"
    assert_eq "segunda tag preserva beta7" "$C" "$(tag_oid "$OLD_TAG")"
    assert_eq "segunda tag preserva beta8" "$A" "$(tag_oid "$TARGET_TAG")"
    assert_contains "tag anotada reconhecida" "Tag alvo resolvida: ${LATER_TAG} (tag)" "$TMP_BASE/success/out.log"
  else
    bad "segunda tag falhou"; sed -n '1,160p' "$TMP_BASE/success/out.log" >&2
  fi
fi

# ---------------------------------------------------------------------------
# 2b. Estado completo com tag alias no mesmo commit continua autoritativo.
# ---------------------------------------------------------------------------
if [ "$rc" = "0" ]; then
  git -C "$LOCAL" tag alias-extra HEAD
  reset_logs
  rc_alias="$(run_update "$TMP_BASE/success" "$LATER_TAG" --yes)"
  assert_eq "tag alias rc" "0" "$rc_alias"
  assert_eq "tag alias preserva versao" "$LATER_TAG" "$(state_value CONTABASE_INSTALLED_VERSION)"
  assert_eq "tag alias preserva HEAD" "$L" "$(git -C "$LOCAL" rev-parse HEAD)"
  assert_eq "tag alias nao para stack" "0" "$(count_lines "$TMP_BASE/success/events.log" '^event:down$')"
  assert_eq "tag alias nao cria backup" "0" "$(count_lines "$TMP_BASE/success/events.log" '^event:backup$')"
  assert_contains "tag alias proveniencia aceita" "Proveniencia persistida completa aceita" "$TMP_BASE/success/out.log"
fi

# ---------------------------------------------------------------------------
# 3. Branch mode: duas atualizacoes preservam upstream e checkout.
# ---------------------------------------------------------------------------
setup_branch_case branch_seq
A="$(commit_version v1.0.0-beta.8 v1.0.0-beta.8 lightweight "beta8")"
rc="$(run_update "$TMP_BASE/branch_seq" v1.0.0-beta.8 --yes)"
assert_eq "branch primeira rc" "0" "$rc"
if [ "$rc" = "0" ]; then
  assert_eq "branch permanece main" "main" "$(git -C "$LOCAL" branch --show-current)"
  assert_eq "branch upstream preservado" "origin/main" "$(git -C "$LOCAL" rev-parse --abbrev-ref 'main@{upstream}')"
  assert_eq "branch primeira versao" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"
  assert_eq "branch primeira kind" "branch" "$(state_value CONTABASE_INSTALL_REF_KIND)"
  assert_eq "branch primeira ref" "main" "$(state_value CONTABASE_INSTALL_REF)"
  assert_eq "branch primeiro commit" "$A" "$(state_value CONTABASE_INSTALL_COMMIT)"
  assert_eq "branch primeiro HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
  reset_logs
  B="$(commit_version v1.0.0-beta.9 v1.0.0-beta.9 annotated "beta9")"
  rc="$(run_update "$TMP_BASE/branch_seq" v1.0.0-beta.9 --yes)"
  assert_eq "branch segunda rc" "0" "$rc"
  assert_eq "branch segunda versao" "v1.0.0-beta.9" "$(state_value CONTABASE_INSTALLED_VERSION)"
  assert_eq "branch segundo commit" "$B" "$(state_value CONTABASE_INSTALL_COMMIT)"
  assert_eq "branch segundo HEAD" "$B" "$(git -C "$LOCAL" rev-parse HEAD)"
  assert_eq "branch segue main" "main" "$(git -C "$LOCAL" branch --show-current)"
  assert_eq "branch worktree limpo" "" "$(git -C "$LOCAL" status --porcelain)"
  assert_eq "branch sem journal residual" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
else
  bad "branch primeira falhou"; sed -n '1,160p' "$TMP_BASE/branch_seq/out.log" >&2
fi

# ---------------------------------------------------------------------------
# 3b. Branch com proveniencia completa e sem upstream usa fallback oficial.
# ---------------------------------------------------------------------------
setup_branch_case branch_no_upstream
A="$(commit_version v1.0.0-beta.8 v1.0.0-beta.8 lightweight "beta8")"
git -C "$LOCAL" config --unset branch.main.remote
git -C "$LOCAL" config --unset branch.main.merge
rc="$(run_update "$TMP_BASE/branch_no_upstream" v1.0.0-beta.8 --yes)"
assert_eq "branch sem upstream rc" "0" "$rc"
assert_eq "branch sem upstream permanece main" "main" "$(git -C "$LOCAL" branch --show-current)"
assert_eq "branch sem upstream versao" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "branch sem upstream HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "branch sem upstream nao aceita remote arbitrario" "" "$(git -C "$LOCAL" config --get branch.main.remote 2>/dev/null || true)"
assert_contains "branch sem upstream proveniencia aceita" "Proveniencia persistida completa validada localmente" "$TMP_BASE/branch_no_upstream/out.log"

setup_branch_case branch_invalid_upstream
A="$(commit_version v1.0.0-beta.8 v1.0.0-beta.8 lightweight "beta8")"
git -C "$LOCAL" config branch.main.merge refs/tags/v1.0.0-beta.7
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/branch_invalid_upstream" v1.0.0-beta.8 --yes)"
assert_eq "branch upstream invalido rc" "1" "$rc"
assert_contains "branch upstream invalido diagnostico" "nao aponta para refs/heads" "$TMP_BASE/branch_invalid_upstream/out.log"
assert_eq "branch upstream invalido preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "branch upstream invalido nao para stack" "0" "$(count_lines "$TMP_BASE/branch_invalid_upstream/events.log" '^event:down$')"
rc="$(run_update "$TMP_BASE/branch_invalid_upstream" v1.0.0-beta.8 --dry-run)"
assert_eq "branch upstream invalido dry-run rc" "1" "$rc"
assert_contains "branch upstream invalido dry-run diagnostico" "nao aponta para refs/heads" "$TMP_BASE/branch_invalid_upstream/out.log"

setup_branch_case partial_upstream
git -C "$LOCAL" config remote.rogue.url https://example.invalid/rogue.git
git -C "$LOCAL" config branch.main.remote rogue
git -C "$LOCAL" config --unset-all branch.main.merge
before_state="$(sha_file "$CONF/install-state.env")"
before_data="$(sha_file "$LOCAL/data/contabase.db")"
for flag in --yes --dry-run; do
  rc="$(run_update "$TMP_BASE/partial_upstream" v1.0.0-beta.8 "$flag")"
  assert_eq "upstream parcial $flag rc" "1" "$rc"
  assert_contains "upstream parcial $flag diagnostico" "upstream incompleto" "$TMP_BASE/partial_upstream/out.log"
done
assert_eq "upstream parcial preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_file_unchanged "upstream parcial preserva estado" "$CONF/install-state.env" "$before_state"
assert_file_unchanged "upstream parcial preserva banco" "$LOCAL/data/contabase.db" "$before_data"
assert_eq "upstream parcial sem fetch" "0" "$(count_lines "$GIT_LOG" '^fetch ')"
assert_eq "upstream parcial sem mutacao Docker" "0" "$(count_lines "$EVENT_LOG" '^event:')"

# ---------------------------------------------------------------------------
# 4. Shallow/refspec malicioso nao move tags instaladas.
# ---------------------------------------------------------------------------
setup_case refspec_tags
git -C "$LOCAL" config --unset-all remote.origin.fetch
git -C "$LOCAL" config --add remote.origin.fetch '+refs/heads/main:refs/tags/v1.0.0-beta.7'
git -C "$LOCAL" config --add remote.origin.fetch '+refs/heads/*:refs/heads/*'
before_refspec_tag="$(tag_oid v1.0.0-beta.7)"
rc="$(run_update "$TMP_BASE/refspec_tags" v1.0.0-beta.8 --yes)"
assert_eq "refspec tag rc" "0" "$rc"
assert_eq "refspec nao move tag instalada" "$before_refspec_tag" "$(tag_oid v1.0.0-beta.7)"
assert_eq "refspec nao cria branch local" "" "$(git -C "$LOCAL" show-ref --verify refs/heads/main 2>/dev/null || true)"
assert_eq "refspec HEAD final" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "refspec permanece detached" "" "$(git -C "$LOCAL" branch --show-current)"

# ---------------------------------------------------------------------------
# 5. Origem adulterada, insteadOf e ambiente Git.
# ---------------------------------------------------------------------------
setup_case bad_origin
git -C "$LOCAL" remote set-url origin https://github.com/example/contabase.git
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/bad_origin" v1.0.0-beta.8 --yes)"
assert_eq "origem adulterada rc" "1" "$rc"
assert_eq "origem adulterada preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "origem adulterada preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_not_contains "origem adulterada nao faz fetch" "fetch " "$TMP_BASE/bad_origin/git.log"
assert_eq "origem adulterada nao para stack" "0" "$(count_lines "$TMP_BASE/bad_origin/events.log" '^event:down$')"
assert_eq "origem adulterada nao cria backup" "no" "$(test -d "$LOCAL/data/backups" && echo yes || echo no)"

setup_case origin_redirect
MAL="$TMP_BASE/origin_redirect/malicious"
MAL_HELPER="$TMP_BASE/origin_redirect/malicious-helper-executed"
make_malicious_remote "$MAL" "$MAL_HELPER"
git -C "$LOCAL" config "url.file://${MAL}.insteadOf" https://github.com/contabase-app/contabase.git
export TEST_GIT_TRANSLATE=0
rc="$(run_update "$TMP_BASE/origin_redirect" v1.0.0-beta.8 --yes)"
unset TEST_GIT_TRANSLATE
assert_eq "insteadOf rc" "1" "$rc"
assert_contains "insteadOf recusa URL efetiva" "URL efetiva" "$TMP_BASE/origin_redirect/out.log"
assert_eq "insteadOf nao executa helper" "no" "$(test -e "$MAL_HELPER" && echo yes || echo no)"
assert_not_contains "insteadOf nao faz fetch" "fetch " "$TMP_BASE/origin_redirect/git.log"
assert_eq "insteadOf nao para stack" "0" "$(count_lines "$TMP_BASE/origin_redirect/events.log" '^event:down$')"

setup_case global_redirect
MAL="$TMP_BASE/global_redirect/malicious"
MAL_HELPER="$TMP_BASE/global_redirect/malicious-helper-executed"
make_malicious_remote "$MAL" "$MAL_HELPER"
RUN_HOME="$TMP_BASE/global_redirect/home"
mkdir -p "$RUN_HOME"
cat > "$RUN_HOME/.gitconfig" <<EOF
[url "file://${MAL}"]
    insteadOf = https://github.com/contabase-app/contabase.git
EOF
export RUN_HOME="$TMP_BASE/global_redirect/home"
export TEST_GIT_TRANSLATE=0
rc="$(run_update "$TMP_BASE/global_redirect" v1.0.0-beta.8 --yes)"
unset RUN_HOME TEST_GIT_TRANSLATE
assert_eq "origem global rc" "1" "$rc"
assert_eq "origem global nao executa helper" "no" "$(test -e "$MAL_HELPER" && echo yes || echo no)"
assert_not_contains "origem global nao faz fetch" "fetch " "$TMP_BASE/global_redirect/git.log"

setup_case git_env_poison
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0="url.file://${TMP_BASE}/git_env_poison/poison.insteadOf"
  export GIT_CONFIG_VALUE_0="https://github.com/contabase-app/contabase.git"
  run_update "$TMP_BASE/git_env_poison" v1.0.0-beta.8 --yes
)"
assert_eq "ambiente Git adulterado rc" "1" "$rc"
assert_contains "ambiente Git adulterado recusado" "variavel de ambiente Git nao permitida" "$TMP_BASE/git_env_poison/out.log"
assert_eq "ambiente Git adulterado preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_not_contains "ambiente Git adulterado nao faz fetch" "fetch " "$TMP_BASE/git_env_poison/git.log"

# ---------------------------------------------------------------------------
# 6. Estado legado sem proveniencia.
# ---------------------------------------------------------------------------
setup_case legacy
sed '/^CONTABASE_INSTALL_REF/d;/^CONTABASE_INSTALL_COMMIT/d;/^CONTABASE_INSTALL_ORIGIN/d' \
  "$CONF/install-state.env" > "$CONF/state.tmp" && mv "$CONF/state.tmp" "$CONF/install-state.env"
rc="$(run_update "$TMP_BASE/legacy" v1.0.0-beta.8 --yes)"
assert_eq "legado tag rc" "0" "$rc"
assert_eq "legado tag grava kind" "tag" "$(state_value CONTABASE_INSTALL_REF_KIND)"
assert_eq "legado tag grava ref" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALL_REF)"
assert_eq "legado tag grava commit" "$A" "$(state_value CONTABASE_INSTALL_COMMIT)"
assert_contains "legado tag informa deteccao" "Estado legado sem proveniencia" "$TMP_BASE/legacy/out.log"

setup_case legacy_ambiguous
sed '/^CONTABASE_INSTALL_REF/d;/^CONTABASE_INSTALL_COMMIT/d;/^CONTABASE_INSTALL_ORIGIN/d' \
  "$CONF/install-state.env" > "$CONF/state.tmp" && mv "$CONF/state.tmp" "$CONF/install-state.env"
git -C "$LOCAL" tag alias-legacy HEAD
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/legacy_ambiguous" v1.0.0-beta.8 --yes)"
assert_eq "legado ambiguo rc" "1" "$rc"
assert_contains "legado ambiguo recusa tag dupla" "HEAD destacado sem tag exata unica" "$TMP_BASE/legacy_ambiguous/out.log"
assert_eq "legado ambiguo preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "legado ambiguo nao para stack" "0" "$(count_lines "$TMP_BASE/legacy_ambiguous/events.log" '^event:down$')"

# ---------------------------------------------------------------------------
# 7. Versao invalida, igual, downgrade e tag movida.
# ---------------------------------------------------------------------------
setup_case invalid_version
rc="$(run_update "$TMP_BASE/invalid_version" v1.0 --yes)"
assert_eq "versao invalida rc" "1" "$rc"
assert_contains "versao invalida informa motivo" "versao alvo invalida" "$TMP_BASE/invalid_version/out.log"
assert_eq "versao invalida nao para stack" "0" "$(count_lines "$TMP_BASE/invalid_version/events.log" '^event:down$')"

setup_case missing_tag
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/missing_tag" v1.0.0-beta.99 --yes)"
assert_eq "tag ausente rc" "1" "$rc"
assert_contains "tag ausente diagnostico" "tag alvo ausente ou inacessivel" "$TMP_BASE/missing_tag/out.log"
assert_eq "tag ausente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "tag ausente preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "tag ausente nao para stack" "0" "$(count_lines "$TMP_BASE/missing_tag/events.log" '^event:down$')"

setup_case unshallow_fail
rm -rf "$REMOTE"
rc="$(run_update "$TMP_BASE/unshallow_fail" v1.0.0-beta.8 --yes)"
assert_eq "unshallow falho rc" "1" "$rc"
assert_contains "unshallow falho diagnostico" "falha ao expandir clone shallow" "$TMP_BASE/unshallow_fail/out.log"
assert_eq "unshallow falho preserva tag instalada" "$C" "$(tag_oid v1.0.0-beta.7)"
assert_eq "unshallow falho preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "unshallow falho nao para stack" "0" "$(count_lines "$TMP_BASE/unshallow_fail/events.log" '^event:down$')"

setup_case non_ancestor
git -C "$REMOTE" checkout -q --orphan unrelated-tag
git -C "$REMOTE" rm -rf -q . >/dev/null 2>&1 || true
mkdir -p "$REMOTE/scripts/ops"
printf 'v1.0.0-beta.8\n' > "$REMOTE/VERSION"
printf 'FROM alpine:3.21\n' > "$REMOTE/Dockerfile"
printf 'services:\n  contabase:\n    image: fixture\n' > "$REMOTE/docker-compose.yml"
printf '#!/bin/sh\nexit 1\n' > "$REMOTE/scripts/ops/backup.sh"
chmod +x "$REMOTE/scripts/ops/backup.sh"
git -C "$REMOTE" add -A
git -C "$REMOTE" commit -q -m "orphan beta8"
git -C "$REMOTE" tag -f v1.0.0-beta.8 >/dev/null 2>&1
rc="$(run_update "$TMP_BASE/non_ancestor" v1.0.0-beta.8 --yes)"
assert_eq "ancestralidade invalida rc" "1" "$rc"
assert_contains "ancestralidade invalida diagnostico" "nao e ancestral do alvo" "$TMP_BASE/non_ancestor/out.log"
assert_eq "ancestralidade invalida preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "ancestralidade invalida nao para stack" "0" "$(count_lines "$TMP_BASE/non_ancestor/events.log" '^event:down$')"

setup_case same
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/same" v1.0.0-beta.7 --yes)"
assert_eq "mesma versao rc" "0" "$rc"
assert_eq "mesma versao preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "mesma versao nao para stack" "0" "$(count_lines "$TMP_BASE/same/events.log" '^event:down$')"
assert_eq "mesma versao nao cria backup" "no" "$(test -d "$LOCAL/data/backups" && echo yes || echo no)"

setup_case downgrade
git -C "$REMOTE" tag -d v1.0.0-beta.6 >/dev/null 2>&1 || true
printf 'v1.0.0-beta.6\n' > "$REMOTE/VERSION"
git -C "$REMOTE" add VERSION
git -C "$REMOTE" commit -q -m "downgrade descendant"
git -C "$REMOTE" tag v1.0.0-beta.6
git -C "$LOCAL" tag -d v1.0.0-beta.6 >/dev/null 2>&1 || true
rc="$(run_update "$TMP_BASE/downgrade" v1.0.0-beta.6 --yes)"
assert_eq "downgrade rc" "1" "$rc"
assert_contains "downgrade recusado" "Downgrade recusado" "$TMP_BASE/downgrade/out.log"
assert_eq "downgrade preserva estado" "v1.0.0-beta.7" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "downgrade nao para stack" "0" "$(count_lines "$TMP_BASE/downgrade/events.log" '^event:down$')"

setup_case moved
decoy_commit="$(git -C "$LOCAL" commit-tree "$(git -C "$LOCAL" rev-parse 'HEAD^{tree}')" -p "$C" -m "decoy local")"
git -C "$LOCAL" tag v1.0.0-beta.8 "$decoy_commit"
rc="$(run_update "$TMP_BASE/moved" v1.0.0-beta.8 --yes)"
assert_eq "tag movida rc" "1" "$rc"
assert_contains "tag movida diagnostico" "possivel tag movida" "$TMP_BASE/moved/out.log"
assert_eq "tag movida preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "tag movida preserva beta8 local" "$decoy_commit" "$(tag_oid v1.0.0-beta.8)"

# ---------------------------------------------------------------------------
# 8. Canais beta, stable e pinned.
# ---------------------------------------------------------------------------
setup_case pinned pinned beta
rc="$(run_update "$TMP_BASE/pinned" v1.0.0-beta.8 --yes)"
assert_eq "pinned rc" "0" "$rc"
assert_eq "pinned canal preservado" "pinned" "$(state_value CONTABASE_CHANNEL)"

setup_case stable stable stable
rc="$(run_update "$TMP_BASE/stable" v1.0.1 --yes)"
assert_eq "stable rc" "0" "$rc"
assert_eq "stable canal preservado" "stable" "$(state_value CONTABASE_CHANNEL)"
assert_eq "stable versao final" "v1.0.1" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "stable checkout detached" "" "$(git -C "$LOCAL" branch --show-current)"

setup_case channel_mismatch stable beta
rc="$(run_update "$TMP_BASE/channel_mismatch" v1.0.0-beta.8 --yes)"
assert_eq "canal stable x alvo beta rc" "1" "$rc"
assert_contains "canal stable x alvo beta diagnostico" "canal stable" "$TMP_BASE/channel_mismatch/out.log"
assert_eq "canal incompativel nao para stack" "0" "$(count_lines "$TMP_BASE/channel_mismatch/events.log" '^event:down$')"

setup_case channel_mismatch_stable beta stable
rc="$(run_update "$TMP_BASE/channel_mismatch_stable" v1.0.1 --yes)"
assert_eq "canal beta x alvo stable rc" "1" "$rc"
assert_contains "canal beta x alvo stable diagnostico" "canal beta" "$TMP_BASE/channel_mismatch_stable/out.log"

# ---------------------------------------------------------------------------
# 9. Worktree sujo e flags proibidas no modo tag.
# ---------------------------------------------------------------------------
setup_case dirty
printf 'dirty\n' > "$LOCAL/untracked.txt"
rc="$(run_update "$TMP_BASE/dirty" v1.0.0-beta.8 --yes)"
assert_eq "worktree sujo rc" "1" "$rc"
assert_eq "worktree sujo preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_not_contains "worktree sujo nao faz fetch" "fetch " "$TMP_BASE/dirty/git.log"
assert_eq "worktree sujo nao para stack" "0" "$(count_lines "$TMP_BASE/dirty/events.log" '^event:down$')"

setup_case force_reset
rc="$(run_update "$TMP_BASE/force_reset" v1.0.0-beta.8 --yes --force-reset)"
assert_eq "force-reset tag rc" "1" "$rc"
assert_contains "force-reset tag diagnostico" "recusado em instalacoes por tag" "$TMP_BASE/force_reset/out.log"
assert_eq "force-reset tag nao faz fetch" "0" "$(count_lines "$TMP_BASE/force_reset/git.log" 'fetch ')"
assert_eq "force-reset tag nao para stack" "0" "$(count_lines "$TMP_BASE/force_reset/events.log" '^event:down$')"

setup_case skip_pull
rc="$(run_update "$TMP_BASE/skip_pull" v1.0.0-beta.8 --yes --skip-pull)"
assert_eq "skip-pull tag rc" "1" "$rc"
assert_contains "skip-pull tag diagnostico" "recusado em instalacoes por tag" "$TMP_BASE/skip_pull/out.log"
assert_eq "skip-pull tag nao faz fetch" "0" "$(count_lines "$TMP_BASE/skip_pull/git.log" 'fetch ')"
assert_eq "skip-pull tag nao para stack" "0" "$(count_lines "$TMP_BASE/skip_pull/events.log" '^event:down$')"

setup_case noop_flag
rc="$(run_update "$TMP_BASE/noop_flag" v1.0.0-beta.7 --yes --skip-pull)"
assert_eq "no-op skip-pull rc" "0" "$rc"
assert_contains "no-op skip-pull comprovado" "No-op comprovado no modo tag" "$TMP_BASE/noop_flag/out.log"
assert_eq "no-op skip-pull nao faz fetch" "0" "$(count_lines "$TMP_BASE/noop_flag/git.log" 'fetch ')"
assert_eq "no-op skip-pull nao para stack" "0" "$(count_lines "$TMP_BASE/noop_flag/events.log" '^event:down$')"
rc="$(run_update "$TMP_BASE/noop_flag" v1.0.0-beta.7 --yes --force-reset)"
assert_eq "no-op force-reset rc" "0" "$rc"
assert_eq "no-op force-reset nao para stack" "0" "$(count_lines "$TMP_BASE/noop_flag/events.log" '^event:down$')"

# ---------------------------------------------------------------------------
# 10. Backup antes da mutacao e falha de backup.
# ---------------------------------------------------------------------------
setup_case backup_fail
BACKUP_FAIL_FILE="$TMP_BASE/backup_fail/backup-fail"
export BACKUP_FAIL_FILE
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/backup_fail" v1.0.0-beta.8 --yes)"
unset BACKUP_FAIL_FILE
assert_eq "backup falho rc" "1" "$rc"
assert_eq "backup falho para stack" "1" "$(count_lines "$TMP_BASE/backup_fail/events.log" '^event:down$')"
assert_eq "backup falho nao inicia runtime novo" "0" "$(count_lines "$TMP_BASE/backup_fail/events.log" '^event:up$')"
assert_eq "backup falho preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "backup falho preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "backup falho nao cria journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"

# ---------------------------------------------------------------------------
# 11. Falha de build, startup e health: sem rollback automatico.
# ---------------------------------------------------------------------------
setup_case build_fail
DOCKER_BUILD_FAIL_ONCE_FILE="$TMP_BASE/build_fail/build-fail"
export DOCKER_BUILD_FAIL_ONCE_FILE
rc="$(run_update "$TMP_BASE/build_fail" v1.0.0-beta.8 --yes)"
unset DOCKER_BUILD_FAIL_ONCE_FILE
assert_eq "build falho rc" "1" "$rc"
assert_eq "build falho nao inicia runtime" "0" "$(count_lines "$TMP_BASE/build_fail/events.log" '^event:up$')"
assert_not_contains "build falho sem rollback de imagem" "--no-build" "$TMP_BASE/build_fail/docker.log"
assert_eq "build falho preserva estado" "v1.0.0-beta.7" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "build falho preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
[ -f "$CONF/transition.pending" ] && ok "build falho preserva journal" || bad "build falho sem journal"
assert_eq "build falho sem marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_contains "build falho nao reativa antigo" "imagem anterior NAO foi reativada" "$TMP_BASE/build_fail/out.log"
reset_logs
rc="$(run_update "$TMP_BASE/build_fail" v1.0.0-beta.8 --yes)"
assert_eq "retry build falho rc" "0" "$rc"
assert_eq "retry build falho estado" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "retry build falho HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "retry build falho remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"

setup_case start_fail
DOCKER_START_FAIL_ONCE_FILE="$TMP_BASE/start_fail/start-fail"
export DOCKER_START_FAIL_ONCE_FILE
rc="$(run_update "$TMP_BASE/start_fail" v1.0.0-beta.8 --yes)"
unset DOCKER_START_FAIL_ONCE_FILE
assert_eq "startup falho rc" "1" "$rc"
assert_eq "startup falho inicia apenas uma vez" "1" "$(count_lines "$TMP_BASE/start_fail/events.log" '^event:up$')"
assert_eq "startup falho preserva estado" "v1.0.0-beta.7" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "startup falho preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
[ -f "$CONF/transition.pending" ] && ok "startup falho preserva journal" || bad "startup falho sem journal"

setup_case health_fail
DOCKER_HEALTH_STATUS=unhealthy
export DOCKER_HEALTH_STATUS
rc="$(run_update "$TMP_BASE/health_fail" v1.0.0-beta.8 --yes)"
unset DOCKER_HEALTH_STATUS
assert_eq "health falho rc" "1" "$rc"
assert_eq "health falho inicia apenas uma vez" "1" "$(count_lines "$TMP_BASE/health_fail/events.log" '^event:up$')"
assert_eq "health falho preserva estado" "v1.0.0-beta.7" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "health falho preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_contains "health falho sem reativacao" "runtime antigo NAO foi reativado" "$TMP_BASE/health_fail/out.log"
assert_eq "health falho nao cria marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"

# ---------------------------------------------------------------------------
# 12. Interrupcao entre healthcheck e finalizacao (marcador falha).
# ---------------------------------------------------------------------------
setup_case marker_fail
MARKER_FAIL_ONCE_FILE="$TMP_BASE/marker_fail/marker-fail"
export MARKER_FAIL_ONCE_FILE
rc="$(run_update "$TMP_BASE/marker_fail" v1.0.0-beta.8 --yes)"
unset MARKER_FAIL_ONCE_FILE
assert_eq "health->finalizacao rc" "1" "$rc"
assert_eq "health->finalizacao runtime alvo" "v1.0.0-beta.8" "$(tr -d '[:space:]' < "$RUNTIME_VERSION")"
assert_eq "health->finalizacao estado antigo" "v1.0.0-beta.7" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "health->finalizacao HEAD antigo" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
[ -f "$CONF/transition.pending" ] && ok "health->finalizacao preserva journal" || bad "health->finalizacao sem journal"
assert_eq "health->finalizacao sem marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
cp "$CONF/transition.pending" "$TMP_BASE/marker_fail/saved-journal"
reset_logs
rc="$(run_update "$TMP_BASE/marker_fail" v1.0.0-beta.8 --yes)"
assert_eq "retry health->finalizacao rc" "0" "$rc"
assert_eq "retry health->finalizacao estado" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "retry health->finalizacao HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "retry health->finalizacao sem up" "0" "$(count_lines "$TMP_BASE/marker_fail/events.log" '^event:up$')"
assert_eq "retry health->finalizacao sem down" "0" "$(count_lines "$TMP_BASE/marker_fail/events.log" '^event:down$')"
assert_eq "retry health->finalizacao sem novo backup" "0" "$(count_lines "$TMP_BASE/marker_fail/events.log" '^event:backup$')"
assert_eq "retry health->finalizacao remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "retry health->finalizacao remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_contains "retry sem marcador declara limite de identidade" "coincide com a imagem esperada no journal" "$TMP_BASE/marker_fail/out.log"
cp "$TMP_BASE/marker_fail/saved-journal" "$CONF/transition.pending"
reset_logs
rc="$(run_update "$TMP_BASE/marker_fail" "$TARGET_TAG" --yes)"
assert_eq "journal reaparecido conclui retry" "0" "$rc"
assert_eq "journal reaparecido removido" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "journal reaparecido sem mutacao Docker" "0" "$(count_lines "$EVENT_LOG" '^event:')"

# ---------------------------------------------------------------------------
# 13. Interrupcao entre checkout e gravacao do estado (retry idempotente).
# ---------------------------------------------------------------------------
setup_case state_fail
PERSIST_FAIL_ONCE_FILE="$TMP_BASE/state_fail/persist-fail"
export PERSIST_FAIL_ONCE_FILE
rc="$(run_update "$TMP_BASE/state_fail" v1.0.0-beta.8 --yes)"
unset PERSIST_FAIL_ONCE_FILE
assert_eq "estado falho rc" "1" "$rc"
assert_eq "estado falho checkout alvo" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "estado falho estado antigo" "v1.0.0-beta.7" "$(state_value CONTABASE_INSTALLED_VERSION)"
[ -f "$CONF/finalize.pending" ] && ok "estado falho preserva marcador" || bad "estado falho sem marcador"
[ -f "$CONF/transition.pending" ] && ok "estado falho preserva journal" || bad "estado falho sem journal"
reset_logs
rc="$(run_update "$TMP_BASE/state_fail" v1.0.0-beta.8 --yes)"
assert_eq "retry estado rc" "0" "$rc"
assert_eq "retry estado versao" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "retry estado commit" "$A" "$(state_value CONTABASE_INSTALL_COMMIT)"
assert_eq "retry estado sem up" "0" "$(count_lines "$TMP_BASE/state_fail/events.log" '^event:up$')"
assert_eq "retry estado sem down" "0" "$(count_lines "$TMP_BASE/state_fail/events.log" '^event:down$')"
assert_eq "retry estado sem backup" "0" "$(count_lines "$TMP_BASE/state_fail/events.log" '^event:backup$')"
assert_eq "retry estado remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "retry estado remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
reset_logs
rc="$(run_update "$TMP_BASE/state_fail" v1.0.0-beta.8 --yes)"
assert_eq "segundo retry idempotente rc" "0" "$rc"
assert_eq "segundo retry idempotente sem up" "0" "$(count_lines "$TMP_BASE/state_fail/events.log" '^event:up$')"
assert_eq "segundo retry idempotente sem backup" "0" "$(count_lines "$TMP_BASE/state_fail/events.log" '^event:backup$')"

# ---------------------------------------------------------------------------
# 14. Interrupcao entre remocoes de journal e marcador.
# ---------------------------------------------------------------------------
setup_case cleanup_order
rc="$(run_update "$TMP_BASE/cleanup_order" v1.0.0-beta.8 --yes)"
assert_eq "cleanup base rc" "0" "$rc"
write_marker "v1.0.0-beta.8" tag "v1.0.0-beta.8" "$A" "v1.0.0-beta.7" "$C" "v1.0.0-beta.8" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
write_journal "v1.0.0-beta.8" "$A" beta 0
RM_FAIL_FILE="$TMP_BASE/cleanup_order/rm-fail"
export RM_FAIL_FILE
reset_logs
rc="$(run_update "$TMP_BASE/cleanup_order" v1.0.0-beta.8 --yes)"
unset RM_FAIL_FILE
assert_eq "cleanup interrompido rc" "1" "$rc"
assert_eq "cleanup removeu journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
[ -f "$CONF/finalize.pending" ] && ok "cleanup preserva marcador" || bad "cleanup perdeu marcador"
assert_eq "cleanup preserva estado" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "cleanup preserva HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
reset_logs
rc="$(run_update "$TMP_BASE/cleanup_order" v1.0.0-beta.8 --yes)"
assert_eq "cleanup retry rc" "0" "$rc"
assert_eq "cleanup retry remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "cleanup retry sem up" "0" "$(count_lines "$TMP_BASE/cleanup_order/events.log" '^event:up$')"
assert_eq "cleanup retry sem backup" "0" "$(count_lines "$TMP_BASE/cleanup_order/events.log" '^event:backup$')"

# ---------------------------------------------------------------------------
# 15. Marcador antigo com journal valido e supersessao por alvo novo.
# ---------------------------------------------------------------------------
setup_case supersession
rc="$(run_update "$TMP_BASE/supersession" v1.0.0-beta.8 --yes)"
assert_eq "supersessao base rc" "0" "$rc"
write_marker "v1.0.0-beta.8" tag "v1.0.0-beta.8" "$A" "v1.0.0-beta.8" "$A" "v1.0.0-beta.8" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
write_journal "v1.0.0-beta.8" "$A" beta 0
rc="$(run_update "$TMP_BASE/supersession" v1.0.0-beta.9 --yes)"
assert_eq "supersessao rc" "0" "$rc"
assert_eq "supersessao estado" "v1.0.0-beta.9" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "supersessao HEAD" "$L" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "supersessao remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "supersessao remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"

# ---------------------------------------------------------------------------
# 15b. Mesmo alvo com imagem divergente falha fechado; imagem identica e
#      retry idempotente; marcador de alvo posterior nao e supersedido.
# ---------------------------------------------------------------------------
setup_case image_divergence
rc="$(run_update "$TMP_BASE/image_divergence" v1.0.0-beta.8 --yes)"
assert_eq "imagem divergente base rc" "0" "$rc"
write_marker "v1.0.0-beta.8" tag "v1.0.0-beta.8" "$A" "v1.0.0-beta.7" "$C" "v1.0.0-beta.8" "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
write_journal "v1.0.0-beta.8" "$A" beta 0
before_marker="$(sha_file "$CONF/finalize.pending")"
before_journal="$(sha_file "$CONF/transition.pending")"
before_state="$(sha_file "$CONF/install-state.env")"
before_head="$(git -C "$LOCAL" rev-parse HEAD)"
reset_logs
rc="$(run_update "$TMP_BASE/image_divergence" v1.0.0-beta.8 --yes)"
assert_eq "imagem divergente rc" "1" "$rc"
assert_contains "imagem divergente diagnostico" "mesmo alvo" "$TMP_BASE/image_divergence/out.log"
assert_eq "imagem divergente preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "imagem divergente preserva journal" "$before_journal" "$(sha_file "$CONF/transition.pending")"
assert_eq "imagem divergente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "imagem divergente preserva HEAD" "$before_head" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "imagem divergente sem down" "0" "$(count_lines "$TMP_BASE/image_divergence/events.log" '^event:down$')"
assert_eq "imagem divergente sem backup" "0" "$(count_lines "$TMP_BASE/image_divergence/events.log" '^event:backup$')"
assert_eq "imagem divergente sem up" "0" "$(count_lines "$TMP_BASE/image_divergence/events.log" '^event:up$')"

setup_case image_same
rc="$(run_update "$TMP_BASE/image_same" v1.0.0-beta.8 --yes)"
assert_eq "imagem identica base rc" "0" "$rc"
write_marker "v1.0.0-beta.8" tag "v1.0.0-beta.8" "$A" "v1.0.0-beta.7" "$C" "v1.0.0-beta.8" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
write_journal "v1.0.0-beta.8" "$A" beta 0
reset_logs
rc="$(run_update "$TMP_BASE/image_same" v1.0.0-beta.8 --yes)"
assert_eq "imagem identica retry rc" "0" "$rc"
assert_eq "imagem identica remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "imagem identica remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "imagem identica sem down" "0" "$(count_lines "$TMP_BASE/image_same/events.log" '^event:down$')"
assert_eq "imagem identica sem backup" "0" "$(count_lines "$TMP_BASE/image_same/events.log" '^event:backup$')"
assert_eq "imagem identica sem up" "0" "$(count_lines "$TMP_BASE/image_same/events.log" '^event:up$')"
assert_eq "imagem identica estado alvo" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "imagem identica HEAD alvo" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case supersession_newer
rc="$(run_update "$TMP_BASE/supersession_newer" v1.0.0-beta.8 --yes)"
assert_eq "supersessao newer base rc" "0" "$rc"
write_marker "v1.0.0-beta.9" tag "v1.0.0-beta.9" "$L" "v1.0.0-beta.8" "$A" "v1.0.0-beta.9" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
before_marker="$(sha_file "$CONF/finalize.pending")"
reset_logs
rc="$(run_update "$TMP_BASE/supersession_newer" v1.0.0-beta.8 --yes)"
assert_eq "supersessao newer rc" "1" "$rc"
assert_contains "supersessao newer diagnostico" "nao supera o marcador pendente" "$TMP_BASE/supersession_newer/out.log"
assert_eq "supersessao newer preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "supersessao newer sem down" "0" "$(count_lines "$TMP_BASE/supersession_newer/events.log" '^event:down$')"
assert_eq "supersessao newer sem backup" "0" "$(count_lines "$TMP_BASE/supersession_newer/events.log" '^event:backup$')"

# ---------------------------------------------------------------------------
# 16. Journal ausente, alterado ou conflitante.
# ---------------------------------------------------------------------------
setup_case no_transition
run_update "$TMP_BASE/no_transition" v1.0.0-beta.8 --yes >/dev/null
git -C "$LOCAL" checkout -q --detach "$C"
cat > "$CONF/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=docker
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
CONTABASE_REPO_PATH=$LOCAL
CONTABASE_INSTALL_REF_KIND=tag
CONTABASE_INSTALL_REF=v1.0.0-beta.7
CONTABASE_INSTALL_COMMIT=$C
CONTABASE_INSTALL_ORIGIN=https://github.com/contabase-app/contabase.git
EOF
printf 'v1.0.0-beta.8\n' > "$RUNTIME_VERSION"
rm -f "$CONF/finalize.pending" "$CONF/transition.pending"
rc="$(run_update "$TMP_BASE/no_transition" v1.0.0-beta.8 --yes)"
assert_eq "runtime novo sem intencao rc" "1" "$rc"
assert_contains "runtime novo sem intencao diagnostico" "sem intencao de transicao persistida" "$TMP_BASE/no_transition/out.log"
assert_eq "runtime novo sem intencao sem marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "runtime novo sem intencao preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case marker_tamper
run_update "$TMP_BASE/marker_tamper" v1.0.0-beta.8 --yes >/dev/null
write_marker "v1.0.0-beta.8" tag "v1.0.0-beta.8" "$A" "v1.0.0-beta.7" "$C" "v1.0.0-beta.8" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
write_journal "v1.0.0-beta.8" "$A" beta 0
cp "$CONF/finalize.pending" "$CONF/finalize.valid"
sed 's/^target_commit=.*/target_commit=0000000000000000000000000000000000000000/' "$CONF/finalize.valid" > "$CONF/finalize.pending"
before_marker="$(sha_file "$CONF/finalize.pending")"
reset_logs
rc="$(run_update "$TMP_BASE/marker_tamper" v1.0.0-beta.8 --yes)"
assert_eq "marcador adulterado rc" "1" "$rc"
assert_eq "marcador adulterado preservado" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "marcador adulterado nao para stack" "0" "$(count_lines "$TMP_BASE/marker_tamper/events.log" '^event:down$')"

setup_case journal_tamper
run_update "$TMP_BASE/journal_tamper" v1.0.0-beta.8 --yes >/dev/null
write_marker "v1.0.0-beta.8" tag "v1.0.0-beta.8" "$A" "v1.0.0-beta.7" "$C" "v1.0.0-beta.8" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
write_journal "v1.0.0-beta.8" "$A" beta 0
cp "$CONF/transition.pending" "$CONF/transition.valid"
sed 's/^target_commit=.*/target_commit=0000000000000000000000000000000000000000/' "$CONF/transition.valid" > "$CONF/transition.pending"
before_journal="$(sha_file "$CONF/transition.pending")"
reset_logs
rc="$(run_update "$TMP_BASE/journal_tamper" v1.0.0-beta.8 --yes)"
assert_eq "journal adulterado rc" "1" "$rc"
assert_eq "journal adulterado preservado" "$before_journal" "$(sha_file "$CONF/transition.pending")"
assert_eq "journal adulterado preserva marcador" "yes" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "journal adulterado nao para stack" "0" "$(count_lines "$TMP_BASE/journal_tamper/events.log" '^event:down$')"
cp "$CONF/transition.valid" "$CONF/transition.pending"
sed 's/^target_origin=.*/target_origin=file:\/\/\/tmp\/evil/' "$CONF/transition.valid" > "$CONF/transition.pending"
rc="$(run_update "$TMP_BASE/journal_tamper" v1.0.0-beta.8 --yes)"
assert_eq "journal origem conflitante rc" "1" "$rc"
cp "$CONF/transition.valid" "$CONF/transition.pending"
sed 's/^channel=.*/channel=stable/' "$CONF/transition.valid" > "$CONF/transition.pending"
rc="$(run_update "$TMP_BASE/journal_tamper" v1.0.0-beta.8 --yes)"
assert_eq "journal canal conflitante rc" "1" "$rc"

# ---------------------------------------------------------------------------
# 17. Metodo Docker imagem/digest e metodo Release preservados.
# ---------------------------------------------------------------------------
setup_case dispatch_image
cat > "$CONF/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=docker-image
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
CONTABASE_REPO_PATH=$LOCAL
CONTABASE_IMAGE_REFERENCE=ghcr.io/contabase-app/contabase:v1.0.0-beta.7
EOF
rm -f "$TMP_BASE/dispatch_image/dispatch.log"
rc="$(run_update "$TMP_BASE/dispatch_image" v1.0.0-beta.8 --dry-run)"
assert_eq "docker-image dispatch rc" "0" "$rc"
assert_contains "docker-image dispatch chama modulo" "contabase-docker-image-update.py" "$TMP_BASE/dispatch_image/dispatch.log"
assert_contains "docker-image dispatch preserva state-file" "$CONF/install-state.env" "$TMP_BASE/dispatch_image/dispatch.log"
assert_contains "docker-image dispatch preserva flags" "--dry-run" "$TMP_BASE/dispatch_image/dispatch.log"
assert_eq "docker-image dispatch nao para stack" "0" "$(count_lines "$TMP_BASE/dispatch_image/events.log" '^event:down$')"

setup_case image_conflict
printf 'CONTABASE_IMAGE_INDEX_DIGEST=sha256:invalid\n' >> "$CONF/install-state.env"
rc="$(run_update "$TMP_BASE/image_conflict" v1.0.0-beta.8 --dry-run)"
assert_eq "metodo docker x chave de imagem rc" "1" "$rc"
assert_contains "metodo docker x chave de imagem diagnostico" "contradiz o metodo salvo" "$TMP_BASE/image_conflict/out.log"

setup_case release_method
cat > "$CONF/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=release
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
CONTABASE_REPO_PATH=$LOCAL
EOF
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/release_method" v1.0.0-beta.8 --yes)"
assert_eq "metodo release recusado rc" "1" "$rc"
assert_contains "metodo release recusado diagnostico" "gerencia apenas Docker Compose" "$TMP_BASE/release_method/out.log"
assert_eq "metodo release preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "metodo release nao para stack" "0" "$(count_lines "$TMP_BASE/release_method/events.log" '^event:down$')"

# ---------------------------------------------------------------------------
# 18. CONTABASE_REPO_ROOT explicito fora do diretorio corrente.
# ---------------------------------------------------------------------------
setup_case repo_root_dry
rc=0
(
  cd "$TMP_BASE/repo_root_dry"
  PATH="$BIN:$PATH" \
  TMPDIR="$TMP_BASE/repo_root_dry/tmp" \
  CONTABASE_REPO_ROOT="$LOCAL" \
  CONTABASE_STATE_FILE="$CONF/install-state.env" \
  TEST_REMOTE_DIR="$REMOTE" \
  CONTABASE_VERSION="v1.0.0-beta.8" \
  CONTABASE_ASSUME_YES=1 \
  DOCKER_LOG="$TMP_BASE/repo_root_dry/docker.log" \
  EVENT_LOG="$TMP_BASE/repo_root_dry/events.log" \
  DOCKER_RUNNING_FILE="$RUNTIME_RUNNING" \
  DOCKER_RUNTIME_VERSION_FILE="$RUNTIME_VERSION" \
  DOCKER_NEW_RUNTIME_FILE="$NEW_RUNTIME_FILE" \
  DOCKER_IMAGE_ID="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  DOCKER_PROJECT_WORKDIR="$LOCAL" \
    bash "$LOCAL/scripts/update-contabase-docker.sh" --dry-run > "$TMP_BASE/repo_root_dry/out.log" 2>&1
) || rc=$?
assert_eq "repo root externo rc" "0" "${rc:-0}"
assert_contains "repo root externo dry-run" "NENHUMA ALTERACAO FOI FEITA" "$TMP_BASE/repo_root_dry/out.log"
assert_eq "repo root externo nao para stack" "0" "$(count_lines "$TMP_BASE/repo_root_dry/events.log" '^event:down$')"

setup_case outside_root
mkdir -p "$TMP_BASE/outside_root/workdir"
RUN_WORKDIR="$TMP_BASE/outside_root/workdir"
rc="$(run_update "$TMP_BASE/outside_root" v1.0.0-beta.8 --yes)"
unset RUN_WORKDIR
assert_eq "update completo fora do repo rc" "0" "$rc"
assert_eq "update completo fora do repo HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "update completo fora do repo estado" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"

# ---------------------------------------------------------------------------
# 19. F3: updater/bibliotecas externos sobre checkout Beta 7 sem F0.
# ---------------------------------------------------------------------------
setup_case external_beta7 beta beta 1
[ ! -e "$LOCAL/scripts/lib/contabase-install-state.sh" ] \
  && ok "beta7 externo sem biblioteca F0 no checkout" \
  || bad "fixture beta7 externa ainda possui biblioteca F0"
make_external_bundle "$TMP_BASE/external_beta7" "$TARGET_TAG"
mkdir -p "$TMP_BASE/external_beta7/workdir"
RUN_UPDATER="$EXTERNAL/scripts/update-contabase-docker.sh"
RUN_WORKDIR="$TMP_BASE/external_beta7/workdir"
rc="$(run_update "$TMP_BASE/external_beta7" "$TARGET_TAG" --yes)"
unset RUN_UPDATER RUN_WORKDIR
assert_eq "beta7 externo rc" "0" "$rc"
assert_eq "beta7 externo HEAD alvo" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "beta7 externo estado alvo" "v1.0.0-beta.8" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_contains "beta7 externo bundle validado" "Bundle externo do updater validado" "$TMP_BASE/external_beta7/out.log"
assert_contains "beta7 externo proveniencia completa" "Proveniencia persistida completa aceita" "$TMP_BASE/external_beta7/out.log"
assert_eq "beta7 externo instala wrapper do bundle" \
  "$(sha_file "$EXTERNAL/scripts/lib/contabase-update-wrapper.sh")" \
  "$(sha_file "$TMP_BASE/external_beta7/contabase-update")"
assert_eq "beta7 externo checkout limpo" "" "$(git -C "$LOCAL" status --porcelain)"
[ -f "$LOCAL/scripts/lib/contabase-install-state.sh" ] \
  && ok "beta7 externo checkout alvo contem biblioteca do alvo" \
  || bad "beta7 externo checkout alvo sem biblioteca do alvo"

# ---------------------------------------------------------------------------
# 19b. B3: wrapper do checkout adulterado com assume-unchanged.
# ---------------------------------------------------------------------------
setup_case external_wrapper_tamper beta beta 1
git -C "$LOCAL" update-index --assume-unchanged scripts/lib/contabase-update-wrapper.sh
printf '\n# wrapper adulterado\n' >> "$LOCAL/scripts/lib/contabase-update-wrapper.sh"
assert_eq "docker fixture wrapper adulterado e invisivel ao git" "" "$(git -C "$LOCAL" status --porcelain)"
make_external_bundle "$TMP_BASE/external_wrapper_tamper" "$TARGET_TAG"
mkdir -p "$TMP_BASE/external_wrapper_tamper/workdir"
RUN_UPDATER="$EXTERNAL/scripts/update-contabase-docker.sh"
RUN_WORKDIR="$TMP_BASE/external_wrapper_tamper/workdir"
rc="$(run_update "$TMP_BASE/external_wrapper_tamper" "$TARGET_TAG" --yes)"
unset RUN_UPDATER RUN_WORKDIR
assert_eq "docker wrapper adulterado no checkout rc" "0" "$rc"
assert_eq "docker wrapper adulterado instala bundle" \
  "$(sha_file "$EXTERNAL/scripts/lib/contabase-update-wrapper.sh")" \
  "$(sha_file "$TMP_BASE/external_wrapper_tamper/contabase-update")"

# ---------------------------------------------------------------------------
# 19c. B3: falha no backfill preserva estado, marcador e journal.
# ---------------------------------------------------------------------------
setup_case external_backfill_fail beta beta 1
make_external_bundle "$TMP_BASE/external_backfill_fail" "$TARGET_TAG"
mkdir -p "$TMP_BASE/external_backfill_fail/workdir"
RUN_UPDATER="$EXTERNAL/scripts/update-contabase-docker.sh"
RUN_WORKDIR="$TMP_BASE/external_backfill_fail/workdir"
RUN_UPDATE_WRAPPER=/dev/full
rc="$(run_update "$TMP_BASE/external_backfill_fail" "$TARGET_TAG" --yes)"
unset RUN_UPDATER RUN_WORKDIR RUN_UPDATE_WRAPPER
assert_eq "docker backfill falho rc" "1" "$rc"
assert_eq "docker backfill falho preserva estado anterior" "v1.0.0-beta.7" \
  "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "docker backfill falho preserva marcador" "yes" \
  "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "docker backfill falho preserva journal" "yes" \
  "$(test -e "$CONF/transition.pending" && echo yes || echo no)"

setup_case external_missing beta beta 1
make_external_bundle "$TMP_BASE/external_missing" "$TARGET_TAG"
rm -f "$EXTERNAL/scripts/lib/contabase-recovery.sh"
mkdir -p "$TMP_BASE/external_missing/workdir"
before_state="$(sha_file "$CONF/install-state.env")"
before_head="$(git -C "$LOCAL" rev-parse HEAD)"
RUN_UPDATER="$EXTERNAL/scripts/update-contabase-docker.sh"
RUN_WORKDIR="$TMP_BASE/external_missing/workdir"
rc="$(run_update "$TMP_BASE/external_missing" "$TARGET_TAG" --yes)"
unset RUN_UPDATER RUN_WORKDIR
assert_eq "bundle sem biblioteca rc" "1" "$rc"
assert_contains "bundle sem biblioteca diagnostico" "bundle externo do updater recusado" "$TMP_BASE/external_missing/out.log"
assert_eq "bundle sem biblioteca preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "bundle sem biblioteca preserva HEAD" "$before_head" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "bundle sem biblioteca nao para stack" "0" "$(count_lines "$TMP_BASE/external_missing/events.log" '^event:down$')"

setup_case external_wrong_version beta beta 1
make_external_bundle "$TMP_BASE/external_wrong_version" "v1.0.0-beta.9"
mkdir -p "$TMP_BASE/external_wrong_version/workdir"
before_state="$(sha_file "$CONF/install-state.env")"
RUN_UPDATER="$EXTERNAL/scripts/update-contabase-docker.sh"
RUN_WORKDIR="$TMP_BASE/external_wrong_version/workdir"
rc="$(run_update "$TMP_BASE/external_wrong_version" "$TARGET_TAG" --yes)"
unset RUN_UPDATER RUN_WORKDIR
assert_eq "bundle versao divergente rc" "1" "$rc"
assert_contains "bundle versao divergente diagnostico" "bundle externo" "$TMP_BASE/external_wrong_version/out.log"
assert_eq "bundle versao divergente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "bundle versao divergente nao para stack" "0" "$(count_lines "$TMP_BASE/external_wrong_version/events.log" '^event:down$')"

setup_case external_bundle_dirty beta beta 1
make_external_bundle "$TMP_BASE/external_bundle_dirty" "$TARGET_TAG"
git -C "$EXTERNAL" init -q
git -C "$EXTERNAL" config user.email synthetic@example.invalid
git -C "$EXTERNAL" config user.name Synthetic
git -C "$EXTERNAL" add -A
git -C "$EXTERNAL" commit -q -m "fixture external bundle"
git -C "$EXTERNAL" update-index --assume-unchanged scripts/lib/contabase-install-state.sh
printf '\nprintf sourced > "%s"\n' "$TMP_BASE/external_bundle_dirty/sourced" >> "$EXTERNAL/scripts/lib/contabase-install-state.sh"
assert_eq "assume-unchanged mascara alteracao no indice" "" "$(git -C "$EXTERNAL" status --porcelain)"
mkdir -p "$TMP_BASE/external_bundle_dirty/workdir"
RUN_UPDATER="$EXTERNAL/scripts/update-contabase-docker.sh"
RUN_WORKDIR="$TMP_BASE/external_bundle_dirty/workdir"
rc="$(run_update "$TMP_BASE/external_bundle_dirty" "$TARGET_TAG" --yes)"
unset RUN_UPDATER RUN_WORKDIR
assert_eq "bundle git sujo rc" "1" "$rc"
assert_contains "bundle git sujo diagnostico" "conteudo divergente no bundle" "$TMP_BASE/external_bundle_dirty/out.log"
assert_eq "bundle git sujo nao para stack" "0" "$(count_lines "$TMP_BASE/external_bundle_dirty/events.log" '^event:down$')"
assert_eq "assume-unchanged nao executa sentinela" "no" "$(test -e "$TMP_BASE/external_bundle_dirty/sourced" && echo yes || echo no)"

# Review probes exercise the failing boundary before any transition work.
for condition in symlink_dir archive_modified missing_pin image_without_version frozen_oid; do
  setup_case "bundle_$condition" beta beta 1
  base="$TMP_BASE/bundle_$condition"
  make_external_bundle "$base" "$TARGET_TAG"
  before_state="$(sha_file "$CONF/install-state.env")"
  before_data="$(sha_file "$LOCAL/data/contabase.db")"
  target="$TARGET_TAG"; flag=--help
  case "$condition" in
    symlink_dir)
      mv "$EXTERNAL/scripts/lib" "$base/lib-outside"
      ln -s "$base/lib-outside" "$EXTERNAL/scripts/lib"
      printf '\nprintf sourced > "%s"\n' "$base/sourced" >> "$base/lib-outside/contabase-install-state.sh"
      ;;
    archive_modified)
      printf '\nprintf sourced > "%s"\n' "$base/sourced" >> "$EXTERNAL/scripts/lib/contabase-install-state.sh"
      ;;
    missing_pin) target="" ;;
    image_without_version)
      rm "$EXTERNAL/VERSION"
      sed 's/INSTALL_METHOD=docker$/INSTALL_METHOD=docker-image/' "$CONF/install-state.env" > "$base/new-state"
      mv "$base/new-state" "$CONF/install-state.env"
      before_state="$(sha_file "$CONF/install-state.env")"
      ;;
    frozen_oid)
      BUNDLE_COMMIT="$L"
      sed "1s/.*/commit=$L/" "$EXTERNAL/updater.sha256" > "$base/new-manifest"
      mv "$base/new-manifest" "$EXTERNAL/updater.sha256"
      BUNDLE_DIGEST="$(sha_file "$EXTERNAL/updater.sha256")"
      flag=--yes
      ;;
  esac
  RUN_UPDATER="$EXTERNAL/scripts/update-contabase-docker.sh"
  rc="$(run_update "$base" "$target" "$flag")"
  unset RUN_UPDATER
  assert_eq "bundle $condition recusado" "1" "$rc"
  assert_eq "bundle $condition sem source/dispatch" "no" "$(if [ -e "$base/sourced" ] || [ -e "$base/dispatch.log" ]; then echo yes; else echo no; fi)"
  assert_eq "bundle $condition preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
  assert_file_unchanged "bundle $condition preserva estado" "$CONF/install-state.env" "$before_state"
  assert_file_unchanged "bundle $condition preserva banco" "$LOCAL/data/contabase.db" "$before_data"
  assert_eq "bundle $condition sem mutacao Docker" "0" "$(count_lines "$EVENT_LOG" '^event:')"
done

for condition in supersession_wrong_image journal_wrong_image journal_legacy; do
  setup_case "$condition"
  base="$TMP_BASE/$condition"
  printf '%s\n' "$TARGET_TAG" > "$RUNTIME_VERSION"
  target="$TARGET_TAG"
  if [ "$condition" = supersession_wrong_image ]; then
    write_marker "$TARGET_TAG" tag "$TARGET_TAG" "$A" "$OLD_TAG" "$C" "$TARGET_TAG" \
      aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    target="$LATER_TAG"
  fi
  write_journal "$TARGET_TAG" "$A" beta 0
  if [ "$condition" = journal_legacy ]; then
    sed 's/^schema=2$/schema=1/; /^expected_image=/d' "$CONF/transition.pending" > "$base/legacy"
    mv "$base/legacy" "$CONF/transition.pending"
  fi
  before_state="$(sha_file "$CONF/install-state.env")"
  before_data="$(sha_file "$LOCAL/data/contabase.db")"
  before_journal="$(sha_file "$CONF/transition.pending")"
  before_marker="$(test ! -f "$CONF/finalize.pending" || sha_file "$CONF/finalize.pending")"
  RUNTIME_IMAGE_FIXTURE=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
  rc="$(run_update "$base" "$target" --yes)"
  unset RUNTIME_IMAGE_FIXTURE
  assert_eq "$condition recusado" "1" "$rc"
  assert_eq "$condition preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
  assert_file_unchanged "$condition preserva estado" "$CONF/install-state.env" "$before_state"
  assert_file_unchanged "$condition preserva banco" "$LOCAL/data/contabase.db" "$before_data"
  assert_file_unchanged "$condition preserva journal" "$CONF/transition.pending" "$before_journal"
  assert_eq "$condition preserva marcador" "$before_marker" "$(test ! -f "$CONF/finalize.pending" || sha_file "$CONF/finalize.pending")"
  assert_eq "$condition sem mutacao Docker" "0" "$(count_lines "$EVENT_LOG" '^event:')"
done

setup_case fsync_probe
export FAIL_FSYNC=probe:transition.pending:dir
rc="$(run_update "$TMP_BASE/fsync_probe" "$TARGET_TAG" --yes)"
unset FAIL_FSYNC
assert_eq "fsync preflight falha fechado" "1" "$rc"
assert_eq "fsync preflight sem mutacao Docker" "0" "$(count_lines "$EVENT_LOG" '^event:')"

for failure in expected_image_file expected_image_dir; do
  setup_case "$failure"
  base="$TMP_BASE/$failure"
  export FAIL_FSYNC="$failure"
  rc="$(run_update "$base" "$TARGET_TAG" --yes)"
  unset FAIL_FSYNC
  assert_eq "$failure propaga erro" "1" "$rc"
  assert_eq "$failure executou build" "1" "$(count_lines "$EVENT_LOG" '^event:build$')"
  assert_eq "$failure sem startup" "0" "$(count_lines "$EVENT_LOG" '^event:up$')"
  assert_eq "$failure preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
  assert_eq "$failure preserva estado" "$OLD_TAG" "$(state_value CONTABASE_INSTALLED_VERSION)"
  reset_logs
  if [ "$failure" = expected_image_dir ]; then
    before_journal="$(sha_file "$CONF/transition.pending")"
    export DOCKER_CANDIDATE_ID=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    rc="$(run_update "$base" "$TARGET_TAG" --yes)"
    unset DOCKER_CANDIDATE_ID
    assert_eq "retry recusa substituir imagem persistida" "1" "$rc"
    assert_file_unchanged "retry preserva ID no journal" "$CONF/transition.pending" "$before_journal"
    assert_eq "retry divergente sem mutacao Docker" "0" "$(count_lines "$EVENT_LOG" '^event:')"
  fi
  rc="$(run_update "$base" "$TARGET_TAG" --yes)"
  assert_eq "$failure retry converge" "0" "$rc"
  if [ "$failure" = expected_image_dir ]; then
    assert_eq "retry reutiliza imagem persistida sem build" "0" "$(count_lines "$EVENT_LOG" '^event:build$')"
  fi
done

setup_case container_wrong_image
export DOCKER_ACTIVATED_ID=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
rc="$(run_update "$TMP_BASE/container_wrong_image" "$TARGET_TAG" --yes)"
unset DOCKER_ACTIVATED_ID
assert_eq "health recusa imagem diferente da construida" "1" "$rc"
assert_eq "health preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "health preserva estado" "$OLD_TAG" "$(state_value CONTABASE_INSTALLED_VERSION)"
assert_eq "health preserva ID no journal" "1" "$(count_lines "$CONF/transition.pending" '^expected_image=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa$')"

setup_case cleanup_fsync
export FAIL_FSYNC=remove:transition.pending:dir
rc="$(run_update "$TMP_BASE/cleanup_fsync" "$TARGET_TAG" --yes)"
unset FAIL_FSYNC
assert_eq "fsync apos unlink propaga erro" "1" "$rc"
assert_eq "fsync apos unlink conserva marcador" "yes" "$(test -f "$CONF/finalize.pending" && echo yes || echo no)"
assert_not_contains "fsync apos unlink sem falso sucesso" "Commit logico concluido" "$TMP_BASE/cleanup_fsync/out.log"
reset_logs
rc="$(run_update "$TMP_BASE/cleanup_fsync" "$TARGET_TAG" --yes)"
assert_eq "retry com marcador isolado converge" "0" "$rc"
assert_eq "retry cleanup sem mutacao Docker" "0" "$(count_lines "$EVENT_LOG" '^event:')"

# ---------------------------------------------------------------------------
# 25. P2/B4/B5: inventario literal, EACCES fail-closed e nomes com metacaractere.
# ---------------------------------------------------------------------------
setup_case external_bundle_literal
make_external_bundle "$TMP_BASE/external_bundle_literal" "$TARGET_TAG"
P2_DOCKER_BASE="$TMP_BASE/external_bundle_literal"
P2_DOCKER_BUNDLE="$EXTERNAL"
P2_DOCKER_STATE="$(sha_file "$CONF/install-state.env")"
P2_DOCKER_HEAD="$(git -C "$LOCAL" rev-parse HEAD)"
RUN_UPDATER="$P2_DOCKER_BUNDLE/scripts/update-contabase-docker.sh"

for ambiguous in $'VERSION\n' 'VERSI\117N'; do
  printf '#!/bin/sh\nexit 0\n' > "$P2_DOCKER_BUNDLE/$ambiguous"
  chmod 0755 "$P2_DOCKER_BUNDLE/$ambiguous"
  rc="$(run_update "$P2_DOCKER_BASE" "$TARGET_TAG" --help)"
  assert_eq "P2 docker nome ambiguo rc" "1" "$rc"
  assert_contains "P2 docker nome ambiguo recusado" "arquivo executavel nao declarado" "$P2_DOCKER_BASE/out.log"
  assert_eq "P2 docker nome ambiguo preserva estado" "$P2_DOCKER_STATE" "$(sha_file "$CONF/install-state.env")"
  assert_eq "P2 docker nome ambiguo preserva HEAD" "$P2_DOCKER_HEAD" "$(git -C "$LOCAL" rev-parse HEAD)"
  assert_eq "P2 docker nome ambiguo sem mutacao" "0" "$(count_lines "$EVENT_LOG" '^event:')"
  rm -f "$P2_DOCKER_BUNDLE/$ambiguous"
done

printf '#!/bin/sh\nexit 0\n' > "$P2_DOCKER_BUNDLE/scripts/lib/extra-0650.sh"
chmod 0650 "$P2_DOCKER_BUNDLE/scripts/lib/extra-0650.sh"
rc="$(run_update "$P2_DOCKER_BASE" "$TARGET_TAG" --help)"
assert_eq "P2 docker executavel 0650 rc" "1" "$rc"
assert_contains "P2 docker executavel 0650 recusado" "arquivo executavel nao declarado: scripts/lib/extra-0650.sh" "$P2_DOCKER_BASE/out.log"
assert_eq "P2 docker 0650 sem mutacao" "0" "$(count_lines "$EVENT_LOG" '^event:')"
rm -f "$P2_DOCKER_BUNDLE/scripts/lib/extra-0650.sh"

cp "$P2_DOCKER_BUNDLE/updater.sha256" "$P2_DOCKER_BASE/manifest.orig"
awk 'substr($0, 67) != "scripts/update-contabase-source.sh"' \
  "$P2_DOCKER_BUNDLE/updater.sha256" > "$P2_DOCKER_BASE/manifest.new"
mv "$P2_DOCKER_BASE/manifest.new" "$P2_DOCKER_BUNDLE/updater.sha256"
BUNDLE_DIGEST="$(sha_file "$P2_DOCKER_BUNDLE/updater.sha256")"
rc="$(run_update "$P2_DOCKER_BASE" "$TARGET_TAG" --help)"
assert_eq "P2 docker executavel permitido omitido rc" "1" "$rc"
assert_contains "P2 docker executavel permitido omitido" "arquivo executavel nao declarado: scripts/update-contabase-source.sh" "$P2_DOCKER_BASE/out.log"
assert_eq "P2 docker omitido sem mutacao" "0" "$(count_lines "$EVENT_LOG" '^event:')"
mv "$P2_DOCKER_BASE/manifest.orig" "$P2_DOCKER_BUNDLE/updater.sha256"
BUNDLE_DIGEST="$(sha_file "$P2_DOCKER_BUNDLE/updater.sha256")"

mkdir "$P2_DOCKER_BUNDLE/blocked"
printf '#!/bin/sh\nexit 0\n' > "$P2_DOCKER_BUNDLE/blocked/extra"
chmod 0755 "$P2_DOCKER_BUNDLE/blocked/extra"
chmod 000 "$P2_DOCKER_BUNDLE/blocked"
cp "$BIN/python3" "$BIN/python3.orig"
cat > "$BIN/python3" <<'EOF_PYTHON'
#!/bin/sh
exec "$P2_REAL_PYTHON" -I -B -c '
import errno
import os
import sys

scandir = os.scandir
def failing_scandir(path):
    if os.fspath(path) == os.environ.get("P2_BLOCKED_DIR"):
        code = getattr(errno, os.environ["P2_SCAN_ERROR"])
        raise OSError(code, os.strerror(code), path)
    return scandir(path)

os.scandir = failing_scandir
sys.argv = sys.argv[3:]
exec(compile(sys.stdin.read(), "<updater>", "exec"))
' "$@"
EOF_PYTHON
chmod +x "$BIN/python3"
P2_REAL_PYTHON="$REAL_PYTHON"
rc="$(P2_REAL_PYTHON="$P2_REAL_PYTHON" P2_BLOCKED_DIR="$P2_DOCKER_BUNDLE/blocked" P2_SCAN_ERROR=EACCES \
  run_update "$P2_DOCKER_BASE" "$TARGET_TAG" --help)"
assert_eq "P2 docker EACCES rc" "1" "$rc"
assert_contains "P2 docker EACCES propagado" "[Errno 13]" "$P2_DOCKER_BASE/out.log"
assert_eq "P2 docker EACCES sem mutacao" "0" "$(count_lines "$EVENT_LOG" '^event:')"
rc="$(P2_REAL_PYTHON="$P2_REAL_PYTHON" P2_BLOCKED_DIR="$P2_DOCKER_BUNDLE/blocked" P2_SCAN_ERROR=EIO \
  run_update "$P2_DOCKER_BASE" "$TARGET_TAG" --help)"
assert_eq "P2 docker EIO rc" "1" "$rc"
assert_contains "P2 docker EIO propagado" "[Errno 5]" "$P2_DOCKER_BASE/out.log"
mv "$BIN/python3.orig" "$BIN/python3"
chmod 0700 "$P2_DOCKER_BUNDLE/blocked"
rm -rf "$P2_DOCKER_BUNDLE/blocked"
rc="$(run_update "$P2_DOCKER_BASE" "$TARGET_TAG" --help)"
assert_eq "P2 docker inventario completo" "0" "$rc"
assert_eq "P2 docker inventario completo preserva estado" "$P2_DOCKER_STATE" "$(sha_file "$CONF/install-state.env")"
assert_eq "P2 docker inventario completo preserva HEAD" "$P2_DOCKER_HEAD" "$(git -C "$LOCAL" rev-parse HEAD)"

# O componente alias[1] e literal mesmo se alias1 existir no CWD.
P2_DOCKER_PARENT="$P2_DOCKER_BASE/p2-parent"
mkdir -p "$P2_DOCKER_PARENT" "$P2_DOCKER_BASE/p2-cwd/alias1"
cp -R "$P2_DOCKER_BUNDLE" "$P2_DOCKER_PARENT/bundle"
ln -s "$P2_DOCKER_PARENT" "$P2_DOCKER_BASE/p2-cwd/alias[1]"
P2_DOCKER_ALIASED="$P2_DOCKER_BASE/p2-cwd/alias[1]/bundle"
RUN_WORKDIR="$P2_DOCKER_BASE/p2-cwd"
RUN_UPDATER="$P2_DOCKER_ALIASED/scripts/update-contabase-docker.sh"
rc="$(run_update "$P2_DOCKER_BASE" "$TARGET_TAG" --help)"
assert_eq "P2 docker pai alias[1] rc" "1" "$rc"
assert_contains "P2 docker pai alias[1] literal" "caminho do updater com symlink" "$P2_DOCKER_BASE/out.log"
assert_eq "P2 docker alias sem mutacao" "0" "$(count_lines "$EVENT_LOG" '^event:')"

rm -f "$P2_DOCKER_BASE/p2-cwd/alias[1]"
mv "$P2_DOCKER_PARENT" "$P2_DOCKER_BASE/p2-cwd/alias[1]"
rc="$(run_update "$P2_DOCKER_BASE" "$TARGET_TAG" --help)"
assert_eq "P2 docker diretorio literal alias[1] valido" "0" "$rc"
assert_eq "P2 docker alias valido preserva estado" "$P2_DOCKER_STATE" "$(sha_file "$CONF/install-state.env")"
assert_eq "P2 docker alias valido preserva HEAD" "$P2_DOCKER_HEAD" "$(git -C "$LOCAL" rev-parse HEAD)"
unset RUN_UPDATER RUN_WORKDIR

printf '\n=== F2 DOCKER TAG: %d passed, %d failed ===\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# F1: atualizacao Source por tag oficial, com Git real e servicos simulados.
# Nao executa systemd, Docker, banco real, backup real ou instalacao real.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
UPDATER="$ROOT_DIR/scripts/update-contabase-source.sh"
REAL_GIT="$(command -v git)"
TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-source-tag.XXXXXX")" || exit 1
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
  if grep -Fq -- "$needle" "$file"; then ok "$label"; else bad "$label (missing '$needle')"; fi
}
assert_not_contains() {
  local label="$1" needle="$2" file="$3"
  if grep -Fq -- "$needle" "$file"; then bad "$label (found '$needle')"; else ok "$label"; fi
}
sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi
}
systemctl_calls() {
  [ -f "$1" ] || { printf '0'; return; }
  awk '$1 == "start" || $1 == "stop" { n++ } END { print n+0 }' "$1"
}

tag_oid() {
  git -C "$LOCAL" rev-parse --verify "refs/tags/$1^{commit}" 2>/dev/null || true
}

tag_snapshot() {
  git -C "$LOCAL" for-each-ref --format='%(refname) %(objectname)' refs/tags | LC_ALL=C sort
}

write_pending_marker() {
  local version="$1" kind="$2" ref="$3" commit="$4" previous_version="$5" previous_commit="$6" runtime_version="$7"
  cat > "$CONF/finalize.pending" <<EOF
schema=1
method=source
target_version=$version
target_kind=$kind
target_ref=$ref
target_commit=$commit
previous_version=$previous_version
previous_commit=$previous_commit
runtime_version=$runtime_version
EOF
}

write_transition_journal_fixture() {
  local target_version="$1" target_commit="$2" channel="${3:-}"
  local marker_hash marker_version marker_commit state_version state_commit
  local previous_checkout previous_commit

  previous_checkout="$(tr -d '[:space:]' < "$LOCAL/VERSION")"
  previous_commit="$(git -C "$LOCAL" rev-parse HEAD)"
  if [ -f "$CONF/finalize.pending" ]; then
    marker_hash="$(git -C "$LOCAL" hash-object --no-filters "$CONF/finalize.pending")"
    marker_version="$(awk -F= '$1=="target_version"{print $2}' "$CONF/finalize.pending")"
    marker_commit="$(awk -F= '$1=="target_commit"{print $2}' "$CONF/finalize.pending")"
  else
    marker_hash="none"
    marker_version="none"
    marker_commit="none"
  fi
  if [ -f "$CONF/install-state.env" ]; then
    state_version="$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
    state_commit="$(awk -F= '$1=="CONTABASE_INSTALL_COMMIT"{print $2}' "$CONF/install-state.env")"
  else
    state_version=""
    state_commit=""
  fi
  [ -n "$state_version" ] || state_version="none"
  [ -n "$state_commit" ] || state_commit="none"
  if [ -z "$channel" ]; then
    channel="$(awk -F= '$1=="CONTABASE_CHANNEL"{print $2}' "$CONF/install-state.env" 2>/dev/null || true)"
  fi
  [ -n "$channel" ] || channel="beta"
  cat > "$CONF/transition.pending" <<EOF
schema=1
method=source
target_version=$target_version
target_kind=tag
target_ref=$target_version
target_commit=$target_commit
target_origin=https://github.com/contabase-app/contabase.git
channel=$channel
previous_checkout_version=$previous_checkout
previous_commit=$previous_commit
previous_state_version=$state_version
previous_state_commit=$state_commit
previous_marker_hash=$marker_hash
previous_marker_version=$marker_version
previous_marker_commit=$marker_commit
EOF
}

make_malicious_remote() {
  local dir="$1" marker="$2"
  mkdir -p "$dir/scripts/ops" "$dir/scripts/lib" "$dir/templates" "$dir/assets"
  git -C "$dir" init -q
  git -C "$dir" symbolic-ref HEAD refs/heads/main
  git -C "$dir" config user.email synthetic@example.invalid
  git -C "$dir" config user.name Synthetic
  printf 'v1.0.0-beta.8\n' > "$dir/VERSION"
  printf 'module github.com/contabase-app/contabase\n\ngo 1.23\n' > "$dir/go.mod"
  printf '#!/bin/sh\nexit 0\n' > "$dir/scripts/build-css.sh"
  cat > "$dir/scripts/ops/backup.sh" <<EOF
#!/bin/sh
printf 'executed\n' > "$marker"
exit 0
EOF
  chmod +x "$dir/scripts/build-css.sh" "$dir/scripts/ops/backup.sh"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "malicious beta8"
  git -C "$dir" tag v1.0.0-beta.8
}

make_remote() {
  local base="$1" backup_fail="${2:-0}"
  local remote="$base/remote"
  mkdir -p "$remote"
  git -C "$remote" init -q
  git -C "$remote" symbolic-ref HEAD refs/heads/main
  git -C "$remote" config user.email synthetic@example.invalid
  git -C "$remote" config user.name Synthetic
  REMOTE="$remote"
  BACKUP_FAIL="$backup_fail"
}

commit_version() {
  local version="$1" tag="$2" tag_kind="$3" msg="$4" backup_fail="${5:-0}" include_state_lib="${6:-1}"
  mkdir -p "$REMOTE/scripts/ops" "$REMOTE/scripts/lib" "$REMOTE/templates" "$REMOTE/assets" "$REMOTE/cmd/server"
  cp "$ROOT_DIR/scripts/update-contabase-source.sh" "$REMOTE/scripts/"
  cp "$ROOT_DIR/scripts/lib/contabase-update-wrapper.sh" "$REMOTE/scripts/lib/"
  if [ "$include_state_lib" = "1" ]; then
    cp "$ROOT_DIR/scripts/lib/contabase-install-state.sh" "$REMOTE/scripts/lib/"
  else
    rm -f "$REMOTE/scripts/lib/contabase-install-state.sh"
  fi
  cp "$ROOT_DIR/scripts/lib/contabase-recovery.sh" "$REMOTE/scripts/lib/"
  printf '%s\n' "$version" > "$REMOTE/VERSION"
  printf 'module github.com/contabase-app/contabase\n\ngo 1.23\n' > "$REMOTE/go.mod"
  printf '#!/bin/sh\nexit 0\n' > "$REMOTE/scripts/build-css.sh"
  chmod +x "$REMOTE/scripts/build-css.sh" "$REMOTE/scripts/update-contabase-source.sh"
  printf 'template\n' > "$REMOTE/templates/sentinel.txt"
  printf 'asset\n' > "$REMOTE/assets/sentinel.txt"
  if [ "$backup_fail" = "1" ]; then
    cat > "$REMOTE/scripts/ops/backup.sh" <<'BK'
#!/bin/sh
exit 1
BK
  else
    cat > "$REMOTE/scripts/ops/backup.sh" <<'BK'
#!/bin/sh
set -eu
dest="${1%/}/backup-${APP_VERSION:-unknown}"
mkdir -p "$dest/uploads"
cp "$DB_FILE" "$dest/database.sqlite"
printf 'backup_id=fixture-backup\n' > "$dest/manifest.txt"
: > "$dest/checksums.sha256"
printf '%s\n' "$dest"
BK
  fi
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
  cat > "$bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac
EOF
  cat > "$bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG:?}"
case "${1:-}" in
  is-active) echo active ;;
esac
exit 0
EOF
  cat > "$bin/go" <<'EOF'
#!/bin/sh
case "$1" in
  version) echo "go version go1.23.0 linux/amd64" ;;
  build)
    prev=""
    for arg in "$@"; do
      if [ "$prev" = "-o" ]; then mkdir -p "$(dirname "$arg")"; : > "$arg"; fi
      prev="$arg"
    done
    ;;
  test) exit 0 ;;
esac
exit 0
EOF
  cat > "$bin/node" <<'EOF'
#!/bin/sh
echo v24.0.0
EOF
  cat > "$bin/npm" <<'EOF'
#!/bin/sh
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
  cat > "$bin/sqlite3" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat > "$bin/openssl" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat > "$bin/chown" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat > "$bin/journalctl" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat > "$bin/sleep" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat > "$bin/install" <<'EOF'
#!/bin/sh
if [ "$1" = "-d" ]; then
  shift
  while [ "$#" -gt 0 ]; do
    case "$1" in -o|-g|-m) shift 2 ;; *) mkdir -p "$1"; shift ;; esac
  done
  exit 0
fi
src=""; dst=""
while [ "$#" -gt 0 ]; do
  case "$1" in -o|-g|-m) shift 2 ;; *) if [ -z "$src" ]; then src="$1"; else dst="$1"; fi; shift ;; esac
done
[ -n "$src" ] && [ -n "$dst" ] || exit 1
if [ -n "${INSTALL_FAIL_ONCE_FILE:-}" ] && [ "$dst" = "${CONTABASE_INSTALL_DIR}/contabase" ] \
  && [ ! -f "$INSTALL_FAIL_ONCE_FILE" ]; then
  : > "$INSTALL_FAIL_ONCE_FILE"
  exit 1
fi
mkdir -p "$(dirname "$dst")"
cp "$src" "$dst"
EOF
  local real_mv
  real_mv="$(command -v mv)"
  cat > "$bin/mv" <<EOF
#!/bin/sh
dst=""
for arg in "\$@"; do dst="\$arg"; done
if [ -n "\${PERSIST_FAIL_ONCE_FILE:-}" ] && [ "\$dst" = "\${CONTABASE_STATE_FILE:-}" ] \
  && [ ! -f "\$PERSIST_FAIL_ONCE_FILE" ]; then
  : > "\$PERSIST_FAIL_ONCE_FILE"
  exit 1
fi
if [ -n "\${MARKER_FAIL_ONCE_FILE:-}" ]; then
  case "\$dst" in
    */finalize.pending)
      if [ ! -f "\$MARKER_FAIL_ONCE_FILE" ]; then
        : > "\$MARKER_FAIL_ONCE_FILE"
        exit 1
      fi
      ;;
  esac
fi
exec "$real_mv" "\$@"
EOF
  cat > "$bin/git" <<EOF
#!/usr/bin/env bash
REAL_GIT="$REAL_GIT"
if [ -n "\${GIT_LOG:-}" ]; then
  printf '%s\n' "\$*" >> "\$GIT_LOG"
fi
args=("\$@")
idx=0
if [ "\${args[0]:-}" = "-C" ]; then
  idx=2
fi
cmd="\${args[\$idx]:-}"
if [ "\$cmd" = "fetch" ] && [ "\${TEST_GIT_TRANSLATE:-0}" = "1" ] && [ -n "\${TEST_REMOTE_DIR:-}" ]; then
  for ((i=0; i<\${#args[@]}; i++)); do
    case "\${args[\$i]}" in
      https://github.com/contabase-app/contabase.git|https://github.com/contabase-app/contabase|origin)
        args[\$i]="\$TEST_REMOTE_DIR"
        ;;
    esac
  done
fi
exec "\$REAL_GIT" "\${args[@]}"
EOF
  chmod +x "$bin/id" "$bin/uname" "$bin/systemctl" "$bin/go" "$bin/node" "$bin/npm" \
    "$bin/curl" "$bin/sqlite3" "$bin/openssl" "$bin/chown" "$bin/journalctl" \
    "$bin/sleep" "$bin/install" "$bin/mv" "$bin/git"
  chmod +x "$bin"/*
}

setup_case() {
  local name="$1" backup_fail="${2:-0}" channel="${3:-beta}" series="${4:-beta}" old_state_lib="${5:-1}"
  local base="$TMP_BASE/$name"
  local baseline_tag old_tag target_tag later_tag
  case "$series" in
    stable)
      baseline_tag=v0.9.0
      old_tag=v1.0.0
      target_tag=v1.0.1
      later_tag=v1.0.2
      ;;
    *)
      baseline_tag=v1.0.0-beta.6
      old_tag=v1.0.0-beta.7
      target_tag=v1.0.0-beta.8
      later_tag=v1.0.0-beta.9
      ;;
  esac
  rm -rf "$base"
  mkdir -p "$base"
  make_remote "$base" "$backup_fail"
  local old_oid target_oid later_oid
  commit_version "$baseline_tag" "$baseline_tag" lightweight "baseline" "$backup_fail" >/dev/null
  old_oid="$(commit_version "$old_tag" "$old_tag" annotated "old" "$backup_fail" "$old_state_lib")"
  target_oid="$(commit_version "$target_tag" "$target_tag" lightweight "target" "$backup_fail")"
  later_oid="$(commit_version "$later_tag" "$later_tag" lightweight "later" "$backup_fail")"
  LOCAL="$base/local"
  git clone -q --branch "$old_tag" --depth 1 "file://$REMOTE" "$LOCAL"
  git -C "$LOCAL" config user.email synthetic@example.invalid
  git -C "$LOCAL" config user.name Synthetic
  git -C "$LOCAL" remote set-url origin https://github.com/contabase-app/contabase.git
  CONF="$base/config"; APP="$base/app"; DATA="$base/data"; BIN="$base/bin"
  mkdir -p "$CONF" "$APP/templates" "$APP/assets" "$DATA/uploads" "$base/tmp"
  printf '%s\n' "$old_tag" > "$APP/VERSION"
  printf 'old-binary\n' > "$APP/contabase"
  printf 'old-admin\n' > "$APP/admin"
  printf 'old-template\n' > "$APP/templates/t.txt"
  printf 'old-asset\n' > "$APP/assets/a.txt"
  printf 'synthetic-db\n' > "$DATA/contabase.db"
  printf 'synthetic-wal\n' > "$DATA/contabase.db-wal"
  printf 'synthetic-shm\n' > "$DATA/contabase.db-shm"
  printf 'synthetic-upload\n' > "$DATA/uploads/file.txt"
  printf 'AUTH_ENCRYPTION_KEY=fixture\nSECURITY_MASTER_KEY=fixture\n' > "$CONF/contabase.env"
  cat > "$CONF/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=source
CONTABASE_CHANNEL=$channel
CONTABASE_INSTALLED_VERSION=$old_tag
CONTABASE_REPO_PATH=$LOCAL
CONTABASE_INSTALL_REF_KIND=tag
CONTABASE_INSTALL_REF=$old_tag
CONTABASE_INSTALL_COMMIT=$old_oid
CONTABASE_INSTALL_ORIGIN=https://github.com/contabase-app/contabase.git
CONTABASE_LOCAL_NOTE=preservar
EOF
  make_stubs "$base"
  C="$old_oid"
  A="$target_oid"
  L="$later_oid"
}

setup_branch_case() {
  local name="$1" channel="${2:-beta}"
  local base="$TMP_BASE/$name"
  rm -rf "$base"
  mkdir -p "$base"
  make_remote "$base" 0
  commit_version v1.0.0-beta.6 v1.0.0-beta.6 lightweight "beta6" >/dev/null
  local old_oid
  old_oid="$(commit_version v1.0.0-beta.7 v1.0.0-beta.7 annotated "beta7")"
  LOCAL="$base/local"
  git clone -q "file://$REMOTE" "$LOCAL"
  git -C "$LOCAL" config user.email synthetic@example.invalid
  git -C "$LOCAL" config user.name Synthetic
  git -C "$LOCAL" remote set-url origin https://github.com/contabase-app/contabase.git
  CONF="$base/config"; APP="$base/app"; DATA="$base/data"; BIN="$base/bin"
  mkdir -p "$CONF" "$APP/templates" "$APP/assets" "$DATA/uploads" "$base/tmp"
  printf 'v1.0.0-beta.7\n' > "$APP/VERSION"
  printf 'old-binary\n' > "$APP/contabase"
  printf 'synthetic-db\n' > "$DATA/contabase.db"
  printf 'synthetic-upload\n' > "$DATA/uploads/file.txt"
  cat > "$CONF/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=source
CONTABASE_CHANNEL=$channel
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
CONTABASE_REPO_PATH=$LOCAL
CONTABASE_INSTALL_REF_KIND=branch
CONTABASE_INSTALL_REF=main
CONTABASE_INSTALL_COMMIT=$old_oid
CONTABASE_INSTALL_ORIGIN=https://github.com/contabase-app/contabase.git
EOF
  make_stubs "$base"
  C="$old_oid"
}

run_update() {
  local base="$1" target="$2" persist_file="${3:-}" marker_fail_file="${4:-}" translate="${5:-1}" install_fail_file="${6:-}"
  local rc=0
  (
    cd "$LOCAL"
    export PATH="$BIN:$PATH"
    export TMPDIR="$base/tmp"
    export CONTABASE_PID1_COMM=systemd
    export CONTABASE_REPO_ROOT="$LOCAL"
    export CONTABASE_STATE_FILE="$CONF/install-state.env"
    export CONTABASE_INSTALL_DIR="$APP"
    export DATA_DIR="$DATA"
    export CONTABASE_TMP_BINARY="$base/tmp/contabase"
    export CONTABASE_MODE_FILE="$CONF/install-mode"
    export CONTABASE_UPDATE_WRAPPER="$base/contabase-update"
    export CONTABASE_UPDATE_ALIAS="$base/cb-update"
    export CONTABASE_VERSION="$target"
    export SYSTEMCTL_LOG="$base/systemctl.log"
    export HEALTH_BAD_FILE="$base/health-bad"
    export TEST_REMOTE_DIR="$REMOTE"
    export TEST_GIT_TRANSLATE="$translate"
    export GIT_LOG="$base/git.log"
    if [ -n "$persist_file" ]; then
      export PERSIST_FAIL_ONCE_FILE="$persist_file"
    else
      unset PERSIST_FAIL_ONCE_FILE
    fi
    if [ -n "$marker_fail_file" ]; then
      export MARKER_FAIL_ONCE_FILE="$marker_fail_file"
    else
      unset MARKER_FAIL_ONCE_FILE
    fi
    if [ -n "$install_fail_file" ]; then
      export INSTALL_FAIL_ONCE_FILE="$install_fail_file"
    else
      unset INSTALL_FAIL_ONCE_FILE
    fi
    bash "$base/update.sh" >"$base/out.log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}

prepare_updater_copy() {
  local base="$1"
  cp "$UPDATER" "$base/update.sh"
  chmod +x "$base/update.sh"
}

run_external_source_update() {
  local base="$1" bundle="$2" target="$3" commit="$4" digest="$5" update_wrapper="${6:-$1/contabase-update}"
  local rc=0
  if [ "$#" -ge 6 ]; then
    shift 6
  else
    shift "$#"
  fi
  (
    cd "$LOCAL"
    export PATH="$BIN:$PATH"
    export TMPDIR="$base/tmp"
    export CONTABASE_PID1_COMM=systemd
    export CONTABASE_REPO_ROOT="$LOCAL"
    export CONTABASE_STATE_FILE="$CONF/install-state.env"
    export CONTABASE_INSTALL_DIR="$APP"
    export DATA_DIR="$DATA"
    export CONTABASE_TMP_BINARY="$base/tmp/contabase"
    export CONTABASE_MODE_FILE="$CONF/install-mode"
    export CONTABASE_UPDATE_WRAPPER="$update_wrapper"
    export CONTABASE_UPDATE_ALIAS="$base/cb-update"
    export CONTABASE_VERSION="$target"
    export CONTABASE_UPDATER_BUNDLE_COMMIT="$commit"
    export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="$digest"
    export SYSTEMCTL_LOG="$base/systemctl.log"
    export HEALTH_BAD_FILE="$base/health-bad"
    export TEST_REMOTE_DIR="$REMOTE"
    export TEST_GIT_TRANSLATE=1
    export GIT_LOG="$base/git.log"
    bash "$bundle/scripts/update-contabase-source.sh" "$@" >"$base/out.log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}

make_external_source_bundle() {
  local dest="$1/external"
  rm -rf "$dest"
  mkdir -p "$dest/scripts/lib"
  for file in VERSION scripts/update-contabase-source.sh \
    scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
    scripts/lib/contabase-update-wrapper.sh; do
    git -C "$REMOTE" show "$A:$file" > "$dest/$file"
  done
  chmod +x "$dest/scripts/update-contabase-source.sh"
  (
    cd "$dest"
    printf 'commit=%s\n' "$A"
    for file in VERSION scripts/update-contabase-source.sh \
      scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
      scripts/lib/contabase-update-wrapper.sh; do
      printf '%s  %s\n' "$(sha_file "$file")" "$file"
    done | LC_ALL=C sort -k2,2
  ) > "$dest/updater.sha256"
}

# ---------------------------------------------------------------------------
# 1. Tag anotada -> tag lightweight, shallow clone, repo root explicito,
#    backup, dados, canal beta e finalizacao detached.
# ---------------------------------------------------------------------------
setup_case success
prepare_updater_copy "$TMP_BASE/success"
before_db="$(sha_file "$DATA/contabase.db")"
before_wal="$(sha_file "$DATA/contabase.db-wal")"
before_shm="$(sha_file "$DATA/contabase.db-shm")"
before_upload="$(sha_file "$DATA/uploads/file.txt")"
before_env="$(sha_file "$CONF/contabase.env")"
before_tag_beta7="$(tag_oid v1.0.0-beta.7)"
assert_eq "fixture inicial e shallow" "true" "$(git -C "$LOCAL" rev-parse --is-shallow-repository)"
rc="$(run_update "$TMP_BASE/success" v1.0.0-beta.8)"
assert_eq "sucesso tag rc" "0" "$rc"
if [ "$rc" = "0" ]; then
  assert_not_contains "estado valido nao e tratado como legado" "Estado legado sem proveniencia" "$TMP_BASE/success/out.log"
  assert_eq "tag instalada beta7 nao foi movida" "$before_tag_beta7" "$(tag_oid v1.0.0-beta.7)"
  assert_eq "HEAD final no OID alvo" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
  assert_eq "checkout permanece detached" "" "$(git -C "$LOCAL" branch --show-current)"
  assert_eq "shallow expandido" "false" "$(git -C "$LOCAL" rev-parse --is-shallow-repository)"
  assert_eq "estado final tag" "v1.0.0-beta.8" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
  assert_eq "estado final ref kind" "tag" "$(awk -F= '$1=="CONTABASE_INSTALL_REF_KIND"{print $2}' "$CONF/install-state.env")"
  assert_eq "estado final ref" "v1.0.0-beta.8" "$(awk -F= '$1=="CONTABASE_INSTALL_REF"{print $2}' "$CONF/install-state.env")"
  assert_eq "estado final commit" "$A" "$(awk -F= '$1=="CONTABASE_INSTALL_COMMIT"{print $2}' "$CONF/install-state.env")"
  assert_eq "canal beta preservado" "beta" "$(awk -F= '$1=="CONTABASE_CHANNEL"{print $2}' "$CONF/install-state.env")"
  assert_eq "tag local alvo criada" "$A" "$(git -C "$LOCAL" rev-parse 'refs/tags/v1.0.0-beta.8^{commit}')"
  assert_eq "worktree limpo" "" "$(git -C "$LOCAL" status --porcelain)"
  assert_eq "APP VERSION alvo" "v1.0.0-beta.8" "$(tr -d '[:space:]' < "$APP/VERSION")"
  assert_eq "DB preservado" "$before_db" "$(sha_file "$DATA/contabase.db")"
  assert_eq "upload preservado" "$before_upload" "$(sha_file "$DATA/uploads/file.txt")"
  assert_eq "WAL preservado" "$before_wal" "$(sha_file "$DATA/contabase.db-wal")"
  assert_eq "SHM preservado" "$before_shm" "$(sha_file "$DATA/contabase.db-shm")"
  assert_eq "env preservado" "$before_env" "$(sha_file "$CONF/contabase.env")"
  assert_eq "campo desconhecido preservado" "preservar" "$(awk -F= '$1=="CONTABASE_LOCAL_NOTE"{print $2}' "$CONF/install-state.env")"
  assert_eq "marcador removido" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
  assert_eq "intencao de transicao removida" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
  [ -n "$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' -print -quit 2>/dev/null)" ] \
    && ok "backup canonico criado" || bad "backup canonico ausente"
  [ -f "$TMP_BASE/success/contabase-update" ] && ok "wrapper atualizado" || bad "wrapper atualizado ausente"
else
  bad "sucesso tag falhou"; sed -n '1,160p' "$TMP_BASE/success/out.log" >&2
fi

# ---------------------------------------------------------------------------
# 2. Segundo update consecutivo ainda em tag.
# ---------------------------------------------------------------------------
if [ -f "$TMP_BASE/success/update.sh" ]; then
  rc="$(run_update "$TMP_BASE/success" v1.0.0-beta.9)"
  assert_eq "segunda atualizacao tag rc" "0" "$rc"
  if [ "$rc" = "0" ]; then
    assert_eq "segunda atualizacao estado" "v1.0.0-beta.9" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
    assert_eq "segunda atualizacao ref" "v1.0.0-beta.9" "$(awk -F= '$1=="CONTABASE_INSTALL_REF"{print $2}' "$CONF/install-state.env")"
    assert_eq "segunda atualizacao HEAD" "$L" "$(git -C "$LOCAL" rev-parse HEAD)"
    assert_eq "segunda atualizacao commit estado" "$L" "$(awk -F= '$1=="CONTABASE_INSTALL_COMMIT"{print $2}' "$CONF/install-state.env")"
    assert_eq "segunda atualizacao preserva tag beta7" "$C" "$(tag_oid v1.0.0-beta.7)"
    assert_eq "segunda atualizacao preserva tag beta8" "$A" "$(tag_oid v1.0.0-beta.8)"
  else
    bad "segunda atualizacao tag falhou"; sed -n '1,160p' "$TMP_BASE/success/out.log" >&2
  fi
fi

# ---------------------------------------------------------------------------
# 3. Tag inexistente: nao para servico, nao cria backup.
# ---------------------------------------------------------------------------
setup_case missing
prepare_updater_copy "$TMP_BASE/missing"
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/missing" v1.0.0-beta.99)"
assert_eq "tag ausente rc" "1" "$rc"
assert_eq "tag ausente nao para servico" "0" "$(systemctl_calls "$TMP_BASE/missing/systemctl.log")"
assert_eq "tag ausente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "tag ausente preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

# Versao alvo invalida e rejeitada antes de fetch/servico.
setup_case invalid_version
prepare_updater_copy "$TMP_BASE/invalid_version"
rc="$(run_update "$TMP_BASE/invalid_version" v1.0)"
assert_eq "versao invalida rc" "1" "$rc"
assert_eq "versao invalida nao para servico" "0" "$(systemctl_calls "$TMP_BASE/invalid_version/systemctl.log")"
assert_contains "versao invalida informa motivo" "versao alvo invalida" "$TMP_BASE/invalid_version/out.log"

# Falha no unshallow: clone shallow sem origem acessivel.
setup_case unshallow_fail
prepare_updater_copy "$TMP_BASE/unshallow_fail"
rm -rf "$REMOTE"
rc="$(run_update "$TMP_BASE/unshallow_fail" v1.0.0-beta.8)"
assert_eq "unshallow falho rc" "1" "$rc"
assert_contains "unshallow falho informa motivo" "falha ao expandir clone shallow" "$TMP_BASE/unshallow_fail/out.log"
assert_eq "unshallow falho nao para servico" "0" "$(systemctl_calls "$TMP_BASE/unshallow_fail/systemctl.log")"
assert_eq "unshallow falho preserva tag instalada" "$C" "$(tag_oid v1.0.0-beta.7)"
assert_eq "unshallow falho preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

# ---------------------------------------------------------------------------
# 4. Tag divergente de VERSION.
# ---------------------------------------------------------------------------
setup_case divergent
prepare_updater_copy "$TMP_BASE/divergent"
git -C "$REMOTE" tag -d v1.0.0-beta.8 >/dev/null 2>&1 || true
git -C "$REMOTE" tag v1.0.0-beta.8 "$(git -C "$REMOTE" rev-parse HEAD)"
# O commit atual do remote e beta.9; a tag beta.8 aponta para VERSION beta.9.
rc="$(run_update "$TMP_BASE/divergent" v1.0.0-beta.8)"
assert_eq "tag divergente rc" "1" "$rc"
assert_eq "tag divergente nao para servico" "0" "$(systemctl_calls "$TMP_BASE/divergent/systemctl.log")"
assert_eq "tag divergente preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "tag divergente preserva tag instalada" "$C" "$(tag_oid v1.0.0-beta.7)"

# ---------------------------------------------------------------------------
# 5. Tag movida: tag local aponta para OID diferente do remoto.
# ---------------------------------------------------------------------------
setup_case moved
prepare_updater_copy "$TMP_BASE/moved"
git -C "$LOCAL" tag v1.0.0-beta.8 "$C"
rc="$(run_update "$TMP_BASE/moved" v1.0.0-beta.8)"
assert_eq "tag movida rc" "1" "$rc"
assert_eq "tag movida nao para servico" "0" "$(systemctl_calls "$TMP_BASE/moved/systemctl.log")"
assert_eq "tag movida preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "tag movida preserva beta7" "$C" "$(tag_oid v1.0.0-beta.7)"
assert_eq "tag movida preserva beta8 local" "$C" "$(tag_oid v1.0.0-beta.8)"

# ---------------------------------------------------------------------------
# 6. Origem adulterada e worktree sujo.
# ---------------------------------------------------------------------------
setup_case bad_origin
prepare_updater_copy "$TMP_BASE/bad_origin"
git -C "$LOCAL" remote set-url origin https://github.com/example/contabase.git
rc="$(run_update "$TMP_BASE/bad_origin" v1.0.0-beta.8)"
assert_eq "origem adulterada rc" "1" "$rc"
assert_eq "origem adulterada nao para servico" "0" "$(systemctl_calls "$TMP_BASE/bad_origin/systemctl.log")"
assert_eq "origem adulterada preserva estado" "v1.0.0-beta.7" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "origem adulterada preserva tag instalada" "$C" "$(tag_oid v1.0.0-beta.7)"
assert_eq "origem adulterada preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_not_contains "origem adulterada nao faz fetch" "fetch " "$TMP_BASE/bad_origin/git.log"
assert_eq "origem adulterada nao cria backup" "no" "$(test -d "$DATA/backups" && echo yes || echo no)"

setup_case dirty
prepare_updater_copy "$TMP_BASE/dirty"
printf 'dirty\n' > "$LOCAL/untracked.txt"
rc="$(run_update "$TMP_BASE/dirty" v1.0.0-beta.8)"
assert_eq "worktree sujo rc" "1" "$rc"
assert_eq "worktree sujo nao para servico" "0" "$(systemctl_calls "$TMP_BASE/dirty/systemctl.log")"
assert_eq "worktree sujo preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

# ---------------------------------------------------------------------------
# 7. Versao igual e downgrade nao mutam.
# ---------------------------------------------------------------------------
setup_case same
prepare_updater_copy "$TMP_BASE/same"
rc="$(run_update "$TMP_BASE/same" v1.0.0-beta.7)"
assert_eq "mesma versao rc" "0" "$rc"
assert_eq "mesma versao nao para servico" "0" "$(systemctl_calls "$TMP_BASE/same/systemctl.log")"
assert_eq "mesma versao preserva estado" "v1.0.0-beta.7" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"

setup_case downgrade
prepare_updater_copy "$TMP_BASE/downgrade"
git -C "$REMOTE" tag v1.0.0-beta.6 "$C"
rc="$(run_update "$TMP_BASE/downgrade" v1.0.0-beta.6)"
assert_eq "downgrade rc" "1" "$rc"
assert_eq "downgrade nao para servico" "0" "$(systemctl_calls "$TMP_BASE/downgrade/systemctl.log")"
assert_eq "downgrade preserva estado" "v1.0.0-beta.7" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"


# ---------------------------------------------------------------------------
# 8. Ancestralidade invalida: tag alvo fora da ascendencia.
# ---------------------------------------------------------------------------
setup_case non_ancestor
prepare_updater_copy "$TMP_BASE/non_ancestor"
git -C "$REMOTE" checkout -q --orphan unrelated-branch
git -C "$REMOTE" rm -rf -q . >/dev/null 2>&1 || true
mkdir -p "$REMOTE/scripts/ops" "$REMOTE/scripts/lib" "$REMOTE/templates" "$REMOTE/assets"
printf 'v1.0.0-beta.8\n' > "$REMOTE/VERSION"
printf 'module github.com/contabase-app/contabase\n\ngo 1.23\n' > "$REMOTE/go.mod"
printf '#!/bin/sh\nexit 0\n' > "$REMOTE/scripts/build-css.sh"
printf '#!/bin/sh\nexit 1\n' > "$REMOTE/scripts/ops/backup.sh"
chmod +x "$REMOTE/scripts/build-css.sh" "$REMOTE/scripts/ops/backup.sh"
git -C "$REMOTE" add -A
git -C "$REMOTE" commit -q -m "orphan beta8"
git -C "$REMOTE" tag -d v1.0.0-beta.8 >/dev/null 2>&1 || true
git -C "$REMOTE" tag v1.0.0-beta.8
rc="$(run_update "$TMP_BASE/non_ancestor" v1.0.0-beta.8)"
assert_eq "ancestralidade invalida rc" "1" "$rc"
assert_eq "ancestralidade invalida nao para servico" "0" "$(systemctl_calls "$TMP_BASE/non_ancestor/systemctl.log")"
assert_eq "ancestralidade invalida preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "ancestralidade invalida preserva tag instalada" "$C" "$(tag_oid v1.0.0-beta.7)"

# ---------------------------------------------------------------------------
# 9. Falha no backup: para o servico, nao altera checkout/estado.
# ---------------------------------------------------------------------------
setup_case backup_fail 1
prepare_updater_copy "$TMP_BASE/backup_fail"
rc="$(run_update "$TMP_BASE/backup_fail" v1.0.0-beta.8)"
assert_eq "backup falho rc" "1" "$rc"
assert_eq "backup falho para servico" "1" "$(systemctl_calls "$TMP_BASE/backup_fail/systemctl.log")"
assert_eq "backup falho preserva estado" "v1.0.0-beta.7" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "backup falho preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

# ---------------------------------------------------------------------------
# 9. Falha de health depois do startup: fail-closed, sem runtime antigo.
# ---------------------------------------------------------------------------
setup_case health_fail
prepare_updater_copy "$TMP_BASE/health_fail"
: > "$TMP_BASE/health_fail/health-bad"
rc="$(run_update "$TMP_BASE/health_fail" v1.0.0-beta.8)"
assert_eq "health falho rc" "1" "$rc"
assert_eq "health falho para servico" "3" "$(systemctl_calls "$TMP_BASE/health_fail/systemctl.log")"
assert_eq "health falho preserva estado" "v1.0.0-beta.7" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "health falho preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
grep -q 'nao sera reativada automaticamente' "$TMP_BASE/health_fail/out.log" \
  && ok "health falho informa ausencia de reativacao" || bad "health falho nao informa fail-closed"


# ---------------------------------------------------------------------------
# 9b. Estado legado sem provedencia e tag detached.
# ---------------------------------------------------------------------------
setup_case legacy
prepare_updater_copy "$TMP_BASE/legacy"
sed '/^CONTABASE_INSTALL_REF/d;/^CONTABASE_INSTALL_COMMIT/d;/^CONTABASE_INSTALL_ORIGIN/d' "$CONF/install-state.env" > "$CONF/install-state.env.tmp" && mv "$CONF/install-state.env.tmp" "$CONF/install-state.env"
rc="$(run_update "$TMP_BASE/legacy" v1.0.0-beta.8)"
assert_eq "legado tag rc" "0" "$rc"
assert_eq "legado tag grava REF_KIND" "tag" "$(awk -F= '$1=="CONTABASE_INSTALL_REF_KIND"{print $2}' "$CONF/install-state.env")"
assert_eq "legado tag grava REF" "v1.0.0-beta.8" "$(awk -F= '$1=="CONTABASE_INSTALL_REF"{print $2}' "$CONF/install-state.env")"
assert_eq "legado tag grava COMMIT" "$A" "$(awk -F= '$1=="CONTABASE_INSTALL_COMMIT"{print $2}' "$CONF/install-state.env")"

# ---------------------------------------------------------------------------
# 10. Falha na gravacao do estado depois do checkout: marcador + retry.
# ---------------------------------------------------------------------------
setup_case finalize_retry
prepare_updater_copy "$TMP_BASE/finalize_retry"
rc="$(run_update "$TMP_BASE/finalize_retry" v1.0.0-beta.8 "$TMP_BASE/finalize_retry/persist-fail")"
assert_eq "finalizacao interrompida rc" "1" "$rc"
assert_eq "finalizacao interrompida HEAD alvo" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "finalizacao interrompida estado antigo" "v1.0.0-beta.7" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
[ -f "$CONF/finalize.pending" ] && ok "marcador durable persistido" || bad "marcador durable ausente"
[ -f "$CONF/transition.pending" ] && ok "intencao durable persistida" || bad "intencao durable ausente"
retry_calls_before="$(systemctl_calls "$TMP_BASE/finalize_retry/systemctl.log")"
retry_backups_before="$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"
rc="$(run_update "$TMP_BASE/finalize_retry" v1.0.0-beta.8 "$TMP_BASE/finalize_retry/persist-fail")"
assert_eq "retry finalizacao rc" "0" "$rc"
assert_eq "retry finalizacao estado alvo" "v1.0.0-beta.8" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "retry finalizacao commit alvo" "$A" "$(awk -F= '$1=="CONTABASE_INSTALL_COMMIT"{print $2}' "$CONF/install-state.env")"
assert_eq "retry remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "retry remove intencao" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "retry finalize-only sem novo systemctl" "$retry_calls_before" "$(systemctl_calls "$TMP_BASE/finalize_retry/systemctl.log")"
assert_eq "retry finalize-only sem novo backup" "$retry_backups_before" "$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"

# ---------------------------------------------------------------------------
# 11. Canal pinned preservado.
# ---------------------------------------------------------------------------
setup_case pinned 0 pinned
prepare_updater_copy "$TMP_BASE/pinned"
rc="$(run_update "$TMP_BASE/pinned" v1.0.0-beta.8)"
assert_eq "pinned update rc" "0" "$rc"
assert_eq "canal pinned preservado" "pinned" "$(awk -F= '$1=="CONTABASE_CHANNEL"{print $2}' "$CONF/install-state.env")"



# ---------------------------------------------------------------------------
# 11b. Canal stable preservado em atualizacao stable -> stable.
# ---------------------------------------------------------------------------
setup_case stable 0 stable stable
prepare_updater_copy "$TMP_BASE/stable"
rc="$(run_update "$TMP_BASE/stable" v1.0.1)"
assert_eq "stable update rc" "0" "$rc"
assert_eq "stable canal preservado" "stable" "$(awk -F= '$1=="CONTABASE_CHANNEL"{print $2}' "$CONF/install-state.env")"
assert_eq "stable versao final" "v1.0.1" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "stable checkout detached" "" "$(git -C "$LOCAL" branch --show-current)"

# ---------------------------------------------------------------------------
# 12. Branch mode: duas atualizacoes consecutivas preservam branch/upstream.
# ---------------------------------------------------------------------------
setup_branch_case branch_seq
prepare_updater_copy "$TMP_BASE/branch_seq"
A="$(commit_version v1.0.0-beta.8 v1.0.0-beta.8 lightweight "beta8")"
rc="$(run_update "$TMP_BASE/branch_seq" v1.0.0-beta.8)"
assert_eq "branch primeira atualizacao rc" "0" "$rc"
assert_eq "branch permanece main" "main" "$(git -C "$LOCAL" branch --show-current)"
assert_eq "branch primeira versao" "v1.0.0-beta.8" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "branch primeira ref kind" "branch" "$(awk -F= '$1=="CONTABASE_INSTALL_REF_KIND"{print $2}' "$CONF/install-state.env")"
assert_eq "branch primeira ref" "main" "$(awk -F= '$1=="CONTABASE_INSTALL_REF"{print $2}' "$CONF/install-state.env")"
assert_eq "branch primeiro commit" "$A" "$(awk -F= '$1=="CONTABASE_INSTALL_COMMIT"{print $2}' "$CONF/install-state.env")"
assert_eq "branch primeiro HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "branch upstream preservado" "origin/main" "$(git -C "$LOCAL" rev-parse --abbrev-ref 'main@{upstream}')"
B="$(commit_version v1.0.0-beta.9 v1.0.0-beta.9 lightweight "beta9")"
rc="$(run_update "$TMP_BASE/branch_seq" v1.0.0-beta.9)"
assert_eq "branch segunda atualizacao rc" "0" "$rc"
assert_eq "branch segunda versao" "v1.0.0-beta.9" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "branch segundo commit" "$B" "$(awk -F= '$1=="CONTABASE_INSTALL_COMMIT"{print $2}' "$CONF/install-state.env")"
assert_eq "branch segundo HEAD" "$B" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "branch segue em main" "main" "$(git -C "$LOCAL" branch --show-current)"
assert_eq "branch worktree limpo" "" "$(git -C "$LOCAL" status --porcelain)"
assert_eq "branch sem intencao residual" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"


# ---------------------------------------------------------------------------
# 13. Proveniencia persistida: estado valido deve ser comparado, nao tratado
#     como legado; divergencias falham antes de qualquer mutacao.
# ---------------------------------------------------------------------------
setup_case state_tag_branch
prepare_updater_copy "$TMP_BASE/state_tag_branch"
git -C "$LOCAL" checkout -q -B main "$C"
git -C "$LOCAL" config branch.main.remote origin
git -C "$LOCAL" config branch.main.merge refs/heads/main
before_state="$(sha_file "$CONF/install-state.env")"
before_tags="$(tag_snapshot)"
rc="$(run_update "$TMP_BASE/state_tag_branch" v1.0.0-beta.8)"
assert_eq "estado tag em checkout branch rc" "1" "$rc"
assert_contains "estado valido nao e legado" "proveniencia persistida diverge" "$TMP_BASE/state_tag_branch/out.log"
assert_not_contains "estado tag/branch nao alega legado" "Estado legado sem proveniencia" "$TMP_BASE/state_tag_branch/out.log"
assert_eq "estado tag/branch nao para servico" "0" "$(systemctl_calls "$TMP_BASE/state_tag_branch/systemctl.log")"
assert_eq "estado tag/branch preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "estado tag/branch preserva tags" "$before_tags" "$(tag_snapshot)"
assert_eq "estado tag/branch preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case state_bad_ref
prepare_updater_copy "$TMP_BASE/state_bad_ref"
sed 's/^CONTABASE_INSTALL_REF=.*/CONTABASE_INSTALL_REF=v1.0.0-beta.6/' \
  "$CONF/install-state.env" > "$CONF/install-state.env.tmp"
mv "$CONF/install-state.env.tmp" "$CONF/install-state.env"
before_state="$(sha_file "$CONF/install-state.env")"
before_tags="$(tag_snapshot)"
rc="$(run_update "$TMP_BASE/state_bad_ref" v1.0.0-beta.8)"
assert_eq "estado REF divergente rc" "1" "$rc"
assert_eq "estado REF divergente nao para servico" "0" "$(systemctl_calls "$TMP_BASE/state_bad_ref/systemctl.log")"
assert_eq "estado REF divergente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "estado REF divergente preserva tags" "$before_tags" "$(tag_snapshot)"
assert_eq "estado REF divergente preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case state_bad_commit
prepare_updater_copy "$TMP_BASE/state_bad_commit"
sed 's/^CONTABASE_INSTALL_COMMIT=.*/CONTABASE_INSTALL_COMMIT=0000000000000000000000000000000000000000/' \
  "$CONF/install-state.env" > "$CONF/install-state.env.tmp"
mv "$CONF/install-state.env.tmp" "$CONF/install-state.env"
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/state_bad_commit" v1.0.0-beta.8)"
assert_eq "estado COMMIT divergente rc" "1" "$rc"
assert_eq "estado COMMIT divergente nao para servico" "0" "$(systemctl_calls "$TMP_BASE/state_bad_commit/systemctl.log")"
assert_eq "estado COMMIT divergente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "estado COMMIT divergente preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case state_partial
prepare_updater_copy "$TMP_BASE/state_partial"
sed '/^CONTABASE_INSTALL_ORIGIN=/d' "$CONF/install-state.env" > "$CONF/install-state.env.tmp"
mv "$CONF/install-state.env.tmp" "$CONF/install-state.env"
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/state_partial" v1.0.0-beta.8)"
assert_eq "estado parcial rc" "1" "$rc"
assert_eq "estado parcial nao para servico" "0" "$(systemctl_calls "$TMP_BASE/state_partial/systemctl.log")"
assert_eq "estado parcial preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "estado parcial preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

# ---------------------------------------------------------------------------
# 14. Marcador pendente: nenhuma solicitacao invalida o remove.
# ---------------------------------------------------------------------------
setup_case marker_channel
prepare_updater_copy "$TMP_BASE/marker_channel"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.8\n' > "$APP/VERSION"
commit_version v1.1.0 v1.1.0 lightweight "stable" >/dev/null
before_marker="$(sha_file "$CONF/finalize.pending")"
before_state="$(sha_file "$CONF/install-state.env")"
before_tags="$(tag_snapshot)"
rc="$(run_update "$TMP_BASE/marker_channel" v1.1.0)"
assert_eq "marcador + canal incompativel rc" "1" "$rc"
assert_contains "marcador + canal incompativel informa canal" "canal beta" "$TMP_BASE/marker_channel/out.log"
assert_eq "marcador + canal incompativel nao para servico" "0" "$(systemctl_calls "$TMP_BASE/marker_channel/systemctl.log")"
assert_eq "marcador + canal incompativel preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "marcador + canal incompativel nao cria intencao" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "marcador + canal incompativel preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "marcador + canal incompativel preserva tags" "$before_tags" "$(tag_snapshot)"

setup_case marker_downgrade
prepare_updater_copy "$TMP_BASE/marker_downgrade"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.8\n' > "$APP/VERSION"
before_marker="$(sha_file "$CONF/finalize.pending")"
rc="$(run_update "$TMP_BASE/marker_downgrade" v1.0.0-beta.7)"
assert_eq "marcador + downgrade rc" "1" "$rc"
assert_eq "marcador + downgrade nao para servico" "0" "$(systemctl_calls "$TMP_BASE/marker_downgrade/systemctl.log")"
assert_eq "marcador + downgrade preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "marcador + downgrade nao cria intencao" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "marcador + downgrade preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case marker_invalid
prepare_updater_copy "$TMP_BASE/marker_invalid"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.8\n' > "$APP/VERSION"
before_marker="$(sha_file "$CONF/finalize.pending")"
rc="$(run_update "$TMP_BASE/marker_invalid" v1.0)"
assert_eq "marcador + versao invalida rc" "1" "$rc"
assert_eq "marcador + versao invalida nao para servico" "0" "$(systemctl_calls "$TMP_BASE/marker_invalid/systemctl.log")"
assert_eq "marcador + versao invalida preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "marcador + versao invalida nao cria intencao" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "marcador + versao invalida preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case marker_non_ancestor
prepare_updater_copy "$TMP_BASE/marker_non_ancestor"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.8\n' > "$APP/VERSION"
git -C "$REMOTE" checkout -q --orphan unrelated-branch
git -C "$REMOTE" rm -rf -q . >/dev/null 2>&1 || true
git -C "$REMOTE" tag -d v1.0.0-beta.9 >/dev/null 2>&1 || true
commit_version v1.0.0-beta.9 v1.0.0-beta.9 lightweight "orphan" >/dev/null
before_marker="$(sha_file "$CONF/finalize.pending")"
before_tags="$(tag_snapshot)"
rc="$(run_update "$TMP_BASE/marker_non_ancestor" v1.0.0-beta.9)"
assert_eq "marcador + alvo nao descendente rc" "1" "$rc"
assert_eq "marcador + alvo nao descendente nao para servico" "0" "$(systemctl_calls "$TMP_BASE/marker_non_ancestor/systemctl.log")"
assert_eq "marcador + alvo nao descendente preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "marcador + alvo nao descendente nao cria intencao" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "marcador + alvo nao descendente preserva tags" "$before_tags" "$(tag_snapshot)"
assert_eq "marcador + alvo nao descendente preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case marker_unhealthy
prepare_updater_copy "$TMP_BASE/marker_unhealthy"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.8\n' > "$APP/VERSION"
: > "$TMP_BASE/marker_unhealthy/health-bad"
before_marker="$(sha_file "$CONF/finalize.pending")"
rc="$(run_update "$TMP_BASE/marker_unhealthy" v1.0.0-beta.9)"
assert_eq "marcador + runtime nao saudavel rc" "1" "$rc"
assert_eq "marcador + runtime nao saudavel nao inicia servico" "0" "$(systemctl_calls "$TMP_BASE/marker_unhealthy/systemctl.log")"
assert_eq "marcador + runtime nao saudavel preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "marcador + runtime nao saudavel preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case marker_transition
prepare_updater_copy "$TMP_BASE/marker_transition"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.8\n' > "$APP/VERSION"
before_marker="$(sha_file "$CONF/finalize.pending")"
before_state="$(sha_file "$CONF/install-state.env")"
before_tags="$(tag_snapshot)"
rc="$(run_update "$TMP_BASE/marker_transition" v1.0.0-beta.9 "" "$TMP_BASE/marker_transition/marker-fail")"
assert_eq "transicao de marcador interrompida rc" "1" "$rc"
assert_eq "transicao de marcador preserva marcador anterior" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "transicao de marcador preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "transicao de marcador preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "transicao de marcador nao cria tag beta9" "" "$(tag_oid v1.0.0-beta.9)"
assert_eq "transicao de marcador preserva tags instaladas" "$before_tags" "$(tag_snapshot)"
[ -f "$CONF/transition.pending" ] && ok "transicao de marcador preserva intencao" || bad "transicao de marcador sem intencao"
retry_calls_before="$(systemctl_calls "$TMP_BASE/marker_transition/systemctl.log")"
retry_backups_before="$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"
rc="$(run_update "$TMP_BASE/marker_transition" v1.0.0-beta.9)"
assert_eq "retry com intencao beta9 rc" "0" "$rc"
assert_eq "retry com intencao beta9 estado" "v1.0.0-beta.9" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "retry com intencao beta9 HEAD" "$L" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "retry com intencao beta9 remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "retry com intencao beta9 remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "retry com intencao beta9 sem novo systemctl" "$retry_calls_before" "$(systemctl_calls "$TMP_BASE/marker_transition/systemctl.log")"
assert_eq "retry com intencao beta9 sem novo backup" "$retry_backups_before" "$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"

# ---------------------------------------------------------------------------
# 15. Origem efetiva: insteadOf nao pode executar helper de origem adulterada.
# ---------------------------------------------------------------------------
setup_case origin_redirect
prepare_updater_copy "$TMP_BASE/origin_redirect"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.8\n' > "$APP/VERSION"
MAL="$TMP_BASE/origin_redirect/malicious"
MAL_HELPER="$TMP_BASE/origin_redirect/malicious-helper-executed"
make_malicious_remote "$MAL" "$MAL_HELPER"
git -C "$LOCAL" config "url.file://${MAL}.insteadOf" https://github.com/contabase-app/contabase.git
before_marker="$(sha_file "$CONF/finalize.pending")"
before_state="$(sha_file "$CONF/install-state.env")"
before_tags="$(tag_snapshot)"
rc="$(run_update "$TMP_BASE/origin_redirect" v1.0.0-beta.9 "" "" 0)"
assert_eq "origem redirecionada rc" "1" "$rc"
assert_contains "origem redirecionada recusa URL efetiva" "URL efetiva" "$TMP_BASE/origin_redirect/out.log"
assert_eq "origem redirecionada nao executa helper" "no" "$(test -e "$MAL_HELPER" && echo yes || echo no)"
assert_eq "origem redirecionada nao para servico" "0" "$(systemctl_calls "$TMP_BASE/origin_redirect/systemctl.log")"
assert_eq "origem redirecionada nao cria backup" "no" "$(test -d "$DATA/backups" && echo yes || echo no)"
assert_not_contains "origem redirecionada nao faz fetch" "fetch " "$TMP_BASE/origin_redirect/git.log"
assert_eq "origem redirecionada preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "origem redirecionada preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "origem redirecionada sem intencao residual" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "origem redirecionada preserva tags" "$before_tags" "$(tag_snapshot)"
assert_eq "origem redirecionada preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"



setup_case origin_redirect_normal
prepare_updater_copy "$TMP_BASE/origin_redirect_normal"
MAL="$TMP_BASE/origin_redirect_normal/malicious"
MAL_HELPER="$TMP_BASE/origin_redirect_normal/malicious-helper-executed"
make_malicious_remote "$MAL" "$MAL_HELPER"
git -C "$LOCAL" config "url.file://${MAL}.insteadOf" https://github.com/contabase-app/contabase.git
before_state="$(sha_file "$CONF/install-state.env")"
before_tags="$(tag_snapshot)"
rc="$(run_update "$TMP_BASE/origin_redirect_normal" v1.0.0-beta.8 "" "" 0)"
assert_eq "origem redirecionada normal rc" "1" "$rc"
assert_eq "origem redirecionada normal nao executa helper" "no" "$(test -e "$MAL_HELPER" && echo yes || echo no)"
assert_eq "origem redirecionada normal nao para servico" "0" "$(systemctl_calls "$TMP_BASE/origin_redirect_normal/systemctl.log")"
assert_not_contains "origem redirecionada normal nao faz fetch" "fetch " "$TMP_BASE/origin_redirect_normal/git.log"
assert_eq "origem redirecionada normal preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "origem redirecionada normal sem intencao residual" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "origem redirecionada normal preserva tags" "$before_tags" "$(tag_snapshot)"
assert_eq "origem redirecionada normal preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"


setup_case global_redirect
prepare_updater_copy "$TMP_BASE/global_redirect"
MAL="$TMP_BASE/global_redirect/malicious"
MAL_HELPER="$TMP_BASE/global_redirect/malicious-helper-executed"
make_malicious_remote "$MAL" "$MAL_HELPER"
GHOME="$TMP_BASE/global_redirect/home"
mkdir -p "$GHOME"
cat > "$GHOME/.gitconfig" <<EOF
[url "file://${MAL}"]
    insteadOf = https://github.com/contabase-app/contabase.git
EOF
before_state="$(sha_file "$CONF/install-state.env")"
before_tags="$(tag_snapshot)"
rc="$(
  HOME="$GHOME" run_update "$TMP_BASE/global_redirect" v1.0.0-beta.8 "" "" 0
)"
assert_eq "origem global redirecionada rc" "1" "$rc"
assert_eq "origem global redirecionada nao executa helper" "no" "$(test -e "$MAL_HELPER" && echo yes || echo no)"
assert_eq "origem global redirecionada nao para servico" "0" "$(systemctl_calls "$TMP_BASE/global_redirect/systemctl.log")"
assert_not_contains "origem global redirecionada nao faz fetch" "fetch " "$TMP_BASE/global_redirect/git.log"
assert_eq "origem global redirecionada preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "origem global redirecionada sem intencao residual" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "origem global redirecionada preserva tags" "$before_tags" "$(tag_snapshot)"
assert_eq "origem global redirecionada preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

setup_case git_env_poison
prepare_updater_copy "$TMP_BASE/git_env_poison"
before_state="$(sha_file "$CONF/install-state.env")"
before_tags="$(tag_snapshot)"
rc="$(
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0="url.file://${TMP_BASE}/git_env_poison/poison.insteadOf"
  export GIT_CONFIG_VALUE_0="https://github.com/contabase-app/contabase.git"
  run_update "$TMP_BASE/git_env_poison" v1.0.0-beta.8
)"
assert_eq "ambiente Git adulterado rc" "1" "$rc"
assert_contains "ambiente Git adulterado recusado" "variavel de ambiente Git nao permitida" "$TMP_BASE/git_env_poison/out.log"
assert_eq "ambiente Git adulterado nao para servico" "0" "$(systemctl_calls "$TMP_BASE/git_env_poison/systemctl.log")"
assert_eq "ambiente Git adulterado preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "ambiente Git adulterado preserva tags" "$before_tags" "$(tag_snapshot)"
assert_eq "ambiente Git adulterado preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_not_contains "ambiente Git adulterado nao faz fetch" "fetch " "$TMP_BASE/git_env_poison/git.log"

# ---------------------------------------------------------------------------
# 16. Refspecs configurados nao podem mover tags durante o unshallow.
# ---------------------------------------------------------------------------
setup_case refspec_tags
prepare_updater_copy "$TMP_BASE/refspec_tags"
git -C "$LOCAL" config --unset-all remote.origin.fetch
git -C "$LOCAL" config --add remote.origin.fetch '+refs/heads/main:refs/tags/v1.0.0-beta.7'
git -C "$LOCAL" config --add remote.origin.fetch '+refs/heads/*:refs/heads/*'
before_tag_beta7="$(tag_oid v1.0.0-beta.7)"
rc="$(run_update "$TMP_BASE/refspec_tags" v1.0.0-beta.8)"
assert_eq "refspec de tag rc" "0" "$rc"
assert_eq "refspec de tag nao move beta7" "$C" "$(tag_oid v1.0.0-beta.7)"
assert_eq "refspec de tag preserva OID previo" "$before_tag_beta7" "$(tag_oid v1.0.0-beta.7)"
assert_eq "refspec de tag nao cria branch local" "" "$(git -C "$LOCAL" show-ref --verify refs/heads/main 2>/dev/null || true)"
assert_eq "refspec de tag finaliza HEAD alvo" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "refspec de tag permanece detached" "" "$(git -C "$LOCAL" branch --show-current)"


# ---------------------------------------------------------------------------
# 17. Intencao de transicao: retry, falhas antes/depois do startup e
#     recuperacao idempotente do caso marcador antigo + runtime novo.
# ---------------------------------------------------------------------------
setup_case journal_health_retry
prepare_updater_copy "$TMP_BASE/journal_health_retry"
rc="$(run_update "$TMP_BASE/journal_health_retry" v1.0.0-beta.8 "" "$TMP_BASE/journal_health_retry/marker-fail")"
assert_eq "health antes do marcador rc" "1" "$rc"
assert_eq "health antes do marcador preserva estado" "v1.0.0-beta.7" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "health antes do marcador preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "health antes do marcador sem marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
[ -f "$CONF/transition.pending" ] && ok "health antes do marcador preserva intencao" || bad "health antes do marcador sem intencao"
retry_calls_before="$(systemctl_calls "$TMP_BASE/journal_health_retry/systemctl.log")"
retry_backups_before="$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"
rc="$(run_update "$TMP_BASE/journal_health_retry" v1.0.0-beta.8)"
assert_eq "retry health antes do marcador rc" "0" "$rc"
assert_eq "retry health antes do marcador estado" "v1.0.0-beta.8" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "retry health antes do marcador HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "retry health antes do marcador remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "retry health antes do marcador remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "retry health antes do marcador sem systemctl" "$retry_calls_before" "$(systemctl_calls "$TMP_BASE/journal_health_retry/systemctl.log")"
assert_eq "retry health antes do marcador sem backup" "$retry_backups_before" "$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"

setup_case journal_before_start
prepare_updater_copy "$TMP_BASE/journal_before_start"
rc="$(run_update "$TMP_BASE/journal_before_start" v1.0.0-beta.8 "" "" 1 "$TMP_BASE/journal_before_start/install-fail")"
assert_eq "intencao antes do startup rc" "1" "$rc"
assert_eq "intencao antes do startup nao promove estado" "v1.0.0-beta.7" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "intencao antes do startup nao promove HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "intencao antes do startup preserva runtime" "v1.0.0-beta.7" "$(tr -d '[:space:]' < "$APP/VERSION")"
[ -f "$CONF/transition.pending" ] && ok "intencao antes do startup preserva journal" || bad "intencao antes do startup sem journal"
assert_eq "intencao antes do startup sem marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_not_contains "intencao antes do startup nao finaliza" "Finalizando transacao confirmada" "$TMP_BASE/journal_before_start/out.log"
rc="$(run_update "$TMP_BASE/journal_before_start" v1.0.0-beta.8)"
assert_eq "retry apos intencao antes do startup rc" "0" "$rc"
assert_eq "retry apos intencao antes do startup estado" "v1.0.0-beta.8" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "retry apos intencao antes do startup HEAD" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_contains "retry apos intencao antes do startup refaz update" "Parando servico" "$TMP_BASE/journal_before_start/out.log"
assert_eq "retry apos intencao antes do startup remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"

setup_case journal_after_marker
prepare_updater_copy "$TMP_BASE/journal_after_marker"
write_pending_marker v1.0.0-beta.9 tag v1.0.0-beta.9 "$L" v1.0.0-beta.7 "$C" v1.0.0-beta.9
write_transition_journal_fixture v1.0.0-beta.9 "$L"
printf 'v1.0.0-beta.9\n' > "$APP/VERSION"
rc="$(run_update "$TMP_BASE/journal_after_marker" v1.0.0-beta.9)"
assert_eq "retry apos substituir marcador rc" "0" "$rc"
assert_eq "retry apos substituir marcador estado" "v1.0.0-beta.9" "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "retry apos substituir marcador HEAD" "$L" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "retry apos substituir marcador remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "retry apos substituir marcador remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "retry apos substituir marcador sem systemctl" "0" "$(systemctl_calls "$TMP_BASE/journal_after_marker/systemctl.log")"

setup_case journal_after_state
prepare_updater_copy "$TMP_BASE/journal_after_state"
rc="$(run_update "$TMP_BASE/journal_after_state" v1.0.0-beta.8)"
assert_eq "base para estado final rc" "0" "$rc"
before_state="$(sha_file "$CONF/install-state.env")"
before_head="$(git -C "$LOCAL" rev-parse HEAD)"
before_calls="$(systemctl_calls "$TMP_BASE/journal_after_state/systemctl.log")"
before_backups="$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
write_transition_journal_fixture v1.0.0-beta.8 "$A"
rc="$(run_update "$TMP_BASE/journal_after_state" v1.0.0-beta.8)"
assert_eq "retry apos persistir estado rc" "0" "$rc"
assert_eq "retry apos persistir estado preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "retry apos persistir estado preserva HEAD" "$before_head" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "retry apos persistir estado remove marcador" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "retry apos persistir estado remove journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "retry apos persistir estado sem systemctl" "$before_calls" "$(systemctl_calls "$TMP_BASE/journal_after_state/systemctl.log")"
assert_eq "retry apos persistir estado sem backup" "$before_backups" "$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"
rc="$(run_update "$TMP_BASE/journal_after_state" v1.0.0-beta.8)"
assert_eq "segundo retry idempotente rc" "0" "$rc"
assert_eq "segundo retry idempotente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "segundo retry idempotente preserva HEAD" "$before_head" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "segundo retry idempotente sem systemctl" "$before_calls" "$(systemctl_calls "$TMP_BASE/journal_after_state/systemctl.log")"
assert_eq "segundo retry idempotente sem backup" "$before_backups" "$(find "$DATA/backups" -maxdepth 1 -type d -name 'backup-*' 2>/dev/null | wc -l | tr -d ' ')"

setup_case no_transition_intent
prepare_updater_copy "$TMP_BASE/no_transition_intent"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.9\n' > "$APP/VERSION"
before_marker="$(sha_file "$CONF/finalize.pending")"
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/no_transition_intent" v1.0.0-beta.9)"
assert_eq "runtime novo sem intencao rc" "1" "$rc"
assert_contains "runtime novo sem intencao diagnostico" "sem intencao de transicao persistida" "$TMP_BASE/no_transition_intent/out.log"
assert_eq "runtime novo sem intencao preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "runtime novo sem intencao preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "runtime novo sem intencao preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "runtime novo sem intencao sem journal" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
assert_eq "runtime novo sem intencao nao para servico" "0" "$(systemctl_calls "$TMP_BASE/no_transition_intent/systemctl.log")"

setup_case transition_tamper
prepare_updater_copy "$TMP_BASE/transition_tamper"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.9\n' > "$APP/VERSION"
write_transition_journal_fixture v1.0.0-beta.9 "$L"
cp "$CONF/transition.pending" "$CONF/transition.valid"
before_marker="$(sha_file "$CONF/finalize.pending")"
before_state="$(sha_file "$CONF/install-state.env")"
sed 's/^target_commit=.*/target_commit=0000000000000000000000000000000000000000/' \
  "$CONF/transition.valid" > "$CONF/transition.pending"
rc="$(run_update "$TMP_BASE/transition_tamper" v1.0.0-beta.9)"
assert_eq "intencao com OID divergente rc" "1" "$rc"
assert_eq "intencao com OID divergente preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "intencao com OID divergente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "intencao com OID divergente nao para servico" "0" "$(systemctl_calls "$TMP_BASE/transition_tamper/systemctl.log")"
cp "$CONF/transition.valid" "$CONF/transition.pending"
sed 's#^target_origin=.*#target_origin=file:///tmp/evil#' "$CONF/transition.valid" > "$CONF/transition.pending"
rc="$(run_update "$TMP_BASE/transition_tamper" v1.0.0-beta.9)"
assert_eq "intencao com origem adulterada rc" "1" "$rc"
assert_eq "intencao com origem adulterada preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
cp "$CONF/transition.valid" "$CONF/transition.pending"
sed 's/^channel=.*/channel=stable/' "$CONF/transition.valid" > "$CONF/transition.pending"
rc="$(run_update "$TMP_BASE/transition_tamper" v1.0.0-beta.9)"
assert_eq "intencao com canal incompativel rc" "1" "$rc"
assert_eq "intencao com canal incompativel preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
cp "$CONF/transition.valid" "$CONF/transition.pending"
sed 's/^target_version=.*/target_version=v1.0.0-beta.8/; s/^target_ref=.*/target_ref=v1.0.0-beta.8/' \
  "$CONF/transition.valid" > "$CONF/transition.pending"
rc="$(run_update "$TMP_BASE/transition_tamper" v1.0.0-beta.9)"
assert_eq "intencao com alvo divergente rc" "1" "$rc"
assert_eq "intencao com alvo divergente preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "intencao com alvo divergente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"

setup_case transition_unhealthy
prepare_updater_copy "$TMP_BASE/transition_unhealthy"
write_pending_marker v1.0.0-beta.8 tag v1.0.0-beta.8 "$A" v1.0.0-beta.7 "$C" v1.0.0-beta.8
printf 'v1.0.0-beta.9\n' > "$APP/VERSION"
write_transition_journal_fixture v1.0.0-beta.9 "$L"
: > "$TMP_BASE/transition_unhealthy/health-bad"
before_marker="$(sha_file "$CONF/finalize.pending")"
before_journal="$(sha_file "$CONF/transition.pending")"
before_state="$(sha_file "$CONF/install-state.env")"
rc="$(run_update "$TMP_BASE/transition_unhealthy" v1.0.0-beta.9)"
assert_eq "intencao com runtime doente rc" "1" "$rc"
assert_not_contains "intencao com runtime doente nao finaliza" "Finalizando transacao confirmada" "$TMP_BASE/transition_unhealthy/out.log"
assert_eq "intencao com runtime doente preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
assert_eq "intencao com runtime doente preserva journal" "$before_journal" "$(sha_file "$CONF/transition.pending")"
assert_eq "intencao com runtime doente preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
assert_eq "intencao com runtime doente preserva HEAD" "$C" "$(git -C "$LOCAL" rev-parse HEAD)"

# ---------------------------------------------------------------------------
# 20. Beta 7 real sem F0: updater e bibliotecas vem do bundle externo.
# ---------------------------------------------------------------------------
setup_case external_source 0 beta beta 0
assert_eq "beta7 externo sem biblioteca F0 no checkout" "no" \
  "$(test -e "$LOCAL/scripts/lib/contabase-install-state.sh" && echo yes || echo no)"
external_bundle="$TMP_BASE/external_source/external"
mkdir -p "$external_bundle/scripts/lib"
for file in VERSION scripts/update-contabase-source.sh \
  scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
  scripts/lib/contabase-update-wrapper.sh; do
  git -C "$REMOTE" show "$A:$file" > "$external_bundle/$file"
done
chmod +x "$external_bundle/scripts/update-contabase-source.sh"
(
  cd "$external_bundle"
  printf 'commit=%s\n' "$A"
  for file in VERSION scripts/update-contabase-source.sh \
    scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
    scripts/lib/contabase-update-wrapper.sh; do
    printf '%s  %s\n' "$(sha_file "$file")" "$file"
  done | LC_ALL=C sort -k2,2
) > "$external_bundle/updater.sha256"
external_digest="$(sha_file "$external_bundle/updater.sha256")"
rc="$(run_external_source_update "$TMP_BASE/external_source" "$external_bundle" v1.0.0-beta.8 "$A" "$external_digest")"
assert_eq "beta7 externo source rc" "0" "$rc"
assert_contains "beta7 externo bundle validado" "Bundle externo do updater validado" "$TMP_BASE/external_source/out.log"
assert_contains "beta7 externo proveniencia completa" "Proveniencia persistida completa aceita" "$TMP_BASE/external_source/out.log"
assert_eq "beta7 externo versao final" "v1.0.0-beta.8" \
  "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "beta7 externo HEAD final" "$A" "$(git -C "$LOCAL" rev-parse HEAD)"
assert_eq "beta7 externo dados preservados" "synthetic-upload" "$(cat "$DATA/uploads/file.txt")"
assert_eq "beta7 externo instala wrapper do bundle" \
  "$(sha_file "$external_bundle/scripts/lib/contabase-update-wrapper.sh")" \
  "$(sha_file "$TMP_BASE/external_source/contabase-update")"

# ---------------------------------------------------------------------------
# 21. B3: wrapper do checkout adulterado com assume-unchanged nao pode ser
#     instalado no caminho autenticado.
# ---------------------------------------------------------------------------
setup_case external_source_wrapper_tamper 0 beta beta 0
external_bundle_t="$TMP_BASE/external_source_wrapper_tamper/external"
mkdir -p "$external_bundle_t/scripts/lib"
for file in VERSION scripts/update-contabase-source.sh \
  scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
  scripts/lib/contabase-update-wrapper.sh; do
  git -C "$REMOTE" show "$A:$file" > "$external_bundle_t/$file"
done
chmod +x "$external_bundle_t/scripts/update-contabase-source.sh"
(
  cd "$external_bundle_t"
  printf 'commit=%s\n' "$A"
  for file in VERSION scripts/update-contabase-source.sh \
    scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
    scripts/lib/contabase-update-wrapper.sh; do
    printf '%s  %s\n' "$(sha_file "$file")" "$file"
  done | LC_ALL=C sort -k2,2
) > "$external_bundle_t/updater.sha256"
external_digest_t="$(sha_file "$external_bundle_t/updater.sha256")"
git -C "$LOCAL" update-index --assume-unchanged scripts/lib/contabase-update-wrapper.sh
printf '\n# wrapper adulterado\n' >> "$LOCAL/scripts/lib/contabase-update-wrapper.sh"
assert_eq "fixture wrapper local adulterado e invisivel ao git" "" "$(git -C "$LOCAL" status --porcelain)"
rc="$(run_external_source_update "$TMP_BASE/external_source_wrapper_tamper" "$external_bundle_t" v1.0.0-beta.8 "$A" "$external_digest_t")"
assert_eq "wrapper adulterado no checkout rc" "0" "$rc"
assert_eq "wrapper adulterado no checkout instala bundle" \
  "$(sha_file "$external_bundle_t/scripts/lib/contabase-update-wrapper.sh")" \
  "$(sha_file "$TMP_BASE/external_source_wrapper_tamper/contabase-update")"

# ---------------------------------------------------------------------------
# 22. B3: falha no backfill preserva estado, marcador e journal.
# ---------------------------------------------------------------------------
setup_case external_source_backfill_fail 0 beta beta 0
external_bundle_f="$TMP_BASE/external_source_backfill_fail/external"
mkdir -p "$external_bundle_f/scripts/lib"
for file in VERSION scripts/update-contabase-source.sh \
  scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
  scripts/lib/contabase-update-wrapper.sh; do
  git -C "$REMOTE" show "$A:$file" > "$external_bundle_f/$file"
done
chmod +x "$external_bundle_f/scripts/update-contabase-source.sh"
(
  cd "$external_bundle_f"
  printf 'commit=%s\n' "$A"
  for file in VERSION scripts/update-contabase-source.sh \
    scripts/lib/contabase-install-state.sh scripts/lib/contabase-recovery.sh \
    scripts/lib/contabase-update-wrapper.sh; do
    printf '%s  %s\n' "$(sha_file "$file")" "$file"
  done | LC_ALL=C sort -k2,2
) > "$external_bundle_f/updater.sha256"
external_digest_f="$(sha_file "$external_bundle_f/updater.sha256")"
rc="$(run_external_source_update "$TMP_BASE/external_source_backfill_fail" "$external_bundle_f" v1.0.0-beta.8 "$A" "$external_digest_f" /dev/full)"
assert_eq "backfill falho rc" "1" "$rc"
assert_eq "backfill falho preserva estado anterior" "v1.0.0-beta.7" \
  "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$CONF/install-state.env")"
assert_eq "backfill falho preserva marcador" "yes" \
  "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
assert_eq "backfill falho preserva journal" "yes" \
  "$(test -e "$CONF/transition.pending" && echo yes || echo no)"

# ---------------------------------------------------------------------------
# 23. P2/B4: inventario por bytes literais e enumeracao fail-closed.
# ---------------------------------------------------------------------------
setup_case external_source_literal 0 beta beta 0
P2_SOURCE_BASE="$TMP_BASE/external_source_literal"
make_external_source_bundle "$P2_SOURCE_BASE"
P2_SOURCE_BUNDLE="$P2_SOURCE_BASE/external"
P2_SOURCE_DIGEST="$(sha_file "$P2_SOURCE_BUNDLE/updater.sha256")"
P2_SOURCE_STATE="$(sha_file "$CONF/install-state.env")"
P2_SOURCE_HEAD="$(git -C "$LOCAL" rev-parse HEAD)"

for ambiguous in $'VERSION\n' 'VERSI\117N'; do
  printf '#!/bin/sh\nexit 0\n' > "$P2_SOURCE_BUNDLE/$ambiguous"
  chmod 0755 "$P2_SOURCE_BUNDLE/$ambiguous"
  rc="$(run_external_source_update "$P2_SOURCE_BASE" "$P2_SOURCE_BUNDLE" v1.0.0-beta.8 "$A" "$P2_SOURCE_DIGEST")"
  assert_eq "P2 source nome ambiguo rc" "1" "$rc"
  assert_contains "P2 source nome ambiguo recusado" "arquivo executavel nao declarado" "$P2_SOURCE_BASE/out.log"
  assert_eq "P2 source nome ambiguo preserva estado" "$P2_SOURCE_STATE" "$(sha_file "$CONF/install-state.env")"
  assert_eq "P2 source nome ambiguo preserva HEAD" "$P2_SOURCE_HEAD" "$(git -C "$LOCAL" rev-parse HEAD)"
  rm -f "$P2_SOURCE_BUNDLE/$ambiguous"
done

printf '#!/bin/sh\nexit 0\n' > "$P2_SOURCE_BUNDLE/scripts/lib/extra-0650.sh"
chmod 0650 "$P2_SOURCE_BUNDLE/scripts/lib/extra-0650.sh"
rc="$(run_external_source_update "$P2_SOURCE_BASE" "$P2_SOURCE_BUNDLE" v1.0.0-beta.8 "$A" "$P2_SOURCE_DIGEST")"
assert_eq "P2 source executavel 0650 rc" "1" "$rc"
assert_contains "P2 source executavel 0650 recusado" "arquivo executavel nao declarado: scripts/lib/extra-0650.sh" "$P2_SOURCE_BASE/out.log"
assert_eq "P2 source 0650 preserva estado" "$P2_SOURCE_STATE" "$(sha_file "$CONF/install-state.env")"
rm -f "$P2_SOURCE_BUNDLE/scripts/lib/extra-0650.sh"

cp "$P2_SOURCE_BUNDLE/updater.sha256" "$P2_SOURCE_BASE/manifest.orig"
awk 'substr($0, 67) != "scripts/update-contabase-source.sh"' \
  "$P2_SOURCE_BUNDLE/updater.sha256" > "$P2_SOURCE_BASE/manifest.new"
mv "$P2_SOURCE_BASE/manifest.new" "$P2_SOURCE_BUNDLE/updater.sha256"
P2_SOURCE_OMITTED_DIGEST="$(sha_file "$P2_SOURCE_BUNDLE/updater.sha256")"
rc="$(run_external_source_update "$P2_SOURCE_BASE" "$P2_SOURCE_BUNDLE" v1.0.0-beta.8 "$A" "$P2_SOURCE_OMITTED_DIGEST")"
assert_eq "P2 source executavel permitido omitido rc" "1" "$rc"
assert_contains "P2 source executavel permitido omitido" "manifesto incompleto: scripts/update-contabase-source.sh" "$P2_SOURCE_BASE/out.log"
assert_eq "P2 source omitido preserva estado" "$P2_SOURCE_STATE" "$(sha_file "$CONF/install-state.env")"
mv "$P2_SOURCE_BASE/manifest.orig" "$P2_SOURCE_BUNDLE/updater.sha256"

P2_REAL_FIND="$(command -v find)"
cat > "$BIN/find" <<'EOF_FIND'
#!/bin/sh
"$P2_REAL_FIND" "$@"
exit 74
EOF_FIND
chmod +x "$BIN/find"
rc="$(P2_REAL_FIND="$P2_REAL_FIND" run_external_source_update "$P2_SOURCE_BASE" "$P2_SOURCE_BUNDLE" v1.0.0-beta.8 "$A" "$P2_SOURCE_DIGEST")"
rm -f "$BIN/find"
assert_eq "P2 source find incompleto rc" "1" "$rc"
assert_contains "P2 source find incompleto" "falha ao enumerar executaveis do bundle" "$P2_SOURCE_BASE/out.log"
assert_eq "P2 source find incompleto preserva estado" "$P2_SOURCE_STATE" "$(sha_file "$CONF/install-state.env")"
assert_eq "P2 source find incompleto preserva HEAD" "$P2_SOURCE_HEAD" "$(git -C "$LOCAL" rev-parse HEAD)"

# 24. P2/B5: componente com metacaractere nao sofre expansao de glob.
setup_case external_source_alias 0 beta beta 0
P2_ALIAS_BASE="$TMP_BASE/external_source_alias"
make_external_source_bundle "$P2_ALIAS_BASE"
P2_ALIAS_BUNDLE="$P2_ALIAS_BASE/external"
P2_ALIAS_DIGEST="$(sha_file "$P2_ALIAS_BUNDLE/updater.sha256")"
P2_ALIAS_STATE="$(sha_file "$CONF/install-state.env")"
P2_ALIAS_HEAD="$(git -C "$LOCAL" rev-parse HEAD)"
mkdir -p "$P2_ALIAS_BASE/parent" "$LOCAL/alias1"
cp -R "$P2_ALIAS_BUNDLE" "$P2_ALIAS_BASE/parent/bundle"
ln -s "$P2_ALIAS_BASE/parent" "$P2_ALIAS_BASE/alias[1]"
P2_ALIASED="$P2_ALIAS_BASE/alias[1]/bundle"
rc="$(run_external_source_update "$P2_ALIAS_BASE" "$P2_ALIASED" v1.0.0-beta.8 "$A" "$P2_ALIAS_DIGEST")"
assert_eq "P2 source pai alias[1] rc" "1" "$rc"
assert_contains "P2 source pai alias[1] literal" "caminho do updater com symlink" "$P2_ALIAS_BASE/out.log"
assert_eq "P2 source alias preserva estado" "$P2_ALIAS_STATE" "$(sha_file "$CONF/install-state.env")"
assert_eq "P2 source alias preserva HEAD" "$P2_ALIAS_HEAD" "$(git -C "$LOCAL" rev-parse HEAD)"

rm -f "$P2_ALIAS_BASE/alias[1]"
mv "$P2_ALIAS_BASE/parent" "$P2_ALIAS_BASE/alias[1]"
rc="$(run_external_source_update "$P2_ALIAS_BASE" "$P2_ALIASED" v1.0.0-beta.8 "$A" "$P2_ALIAS_DIGEST" "" --help)"
assert_eq "P2 source diretorio literal alias[1] valido" "0" "$rc"
assert_contains "P2 source diretorio literal alias[1] ajuda" "Uso:" "$P2_ALIAS_BASE/out.log"
assert_eq "P2 source alias valido preserva estado" "$P2_ALIAS_STATE" "$(sha_file "$CONF/install-state.env")"
assert_eq "P2 source alias valido preserva HEAD" "$P2_ALIAS_HEAD" "$(git -C "$LOCAL" rev-parse HEAD)"
rmdir "$LOCAL/alias1"

printf '\n=== F1 SOURCE TAG: %d passed, %d failed ===\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

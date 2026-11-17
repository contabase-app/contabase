#!/usr/bin/env bash
# F3-B: contrato de recebimento do bundle externo autenticado.
# Testes sinteticos; nenhum servico, banco ou instalacao real e alterado.
set -u

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
export ROOT_DIR
UPDATER_SOURCE="$ROOT_DIR/scripts/update-contabase-source.sh"
UPDATER_DOCKER="$ROOT_DIR/scripts/update-contabase-docker.sh"
UPDATER_RELEASE="$ROOT_DIR/scripts/update-contabase-release.sh"
TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-bundle-auth.XXXXXX")"
TMP_BASE="$(cd "$TMP_BASE" && pwd -P)"
trap 'if [ "${KEEP_TMP:-0}" = "1" ]; then printf "TMP_BASE preservado: %s\n" "$TMP_BASE" >&2; else rm -rf "$TMP_BASE"; fi' EXIT

PASS=0
FAIL=0
BUNDLE_TARGET_VERSION="v1.0.0-beta.8"

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

sha_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

tag_oid() {
  git -C "$REMOTE" rev-parse --verify "refs/tags/$1^{commit}" 2>/dev/null
}

commit_version() {
  local version="$1" tag="$2" include_state_lib="$3" msg="$4"
  mkdir -p "$REMOTE/scripts/ops" "$REMOTE/scripts/lib" "$REMOTE/templates" "$REMOTE/assets" "$REMOTE/cmd/server"
  cp "$ROOT_DIR/scripts/update-contabase-source.sh" "$REMOTE/scripts/"
  cp "$ROOT_DIR/scripts/update-contabase-docker.sh" "$REMOTE/scripts/"
  cp "$ROOT_DIR/scripts/update-contabase-release.sh" "$REMOTE/scripts/"
  cp "$ROOT_DIR/scripts/lib/contabase-recovery.sh" "$REMOTE/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-update-wrapper.sh" "$REMOTE/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-docker-image-update.py" "$REMOTE/scripts/lib/"
  if [ "$include_state_lib" = "1" ]; then
    cp "$ROOT_DIR/scripts/lib/contabase-install-state.sh" "$REMOTE/scripts/lib/"
  else
    rm -f "$REMOTE/scripts/lib/contabase-install-state.sh"
  fi
  printf '%s\n' "$version" > "$REMOTE/VERSION"
  printf 'module github.com/contabase-app/contabase\n\ngo 1.23\n' > "$REMOTE/go.mod"
  printf '#!/bin/sh\nexit 0\n' > "$REMOTE/scripts/build-css.sh"
  chmod +x "$REMOTE/scripts/"*.sh "$REMOTE/scripts/build-css.sh"
  printf 'template\n' > "$REMOTE/templates/sentinel.txt"
  printf 'asset\n' > "$REMOTE/assets/sentinel.txt"
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
  chmod +x "$REMOTE/scripts/ops/backup.sh"
  git -C "$REMOTE" add -A
  git -C "$REMOTE" commit -q -m "$msg"
  git -C "$REMOTE" tag "$tag"
  git -C "$REMOTE" rev-parse HEAD
}

setup_fixture() {
  local name="$1"
  BASE="$TMP_BASE/$name"
  REMOTE="$BASE/remote"
  rm -rf "$BASE"
  mkdir -p "$REMOTE"
  git -C "$REMOTE" init -q -b main
  git -C "$REMOTE" config user.email synthetic@example.invalid
  git -C "$REMOTE" config user.name Synthetic
  B7="$(commit_version v1.0.0-beta.7 v1.0.0-beta.7 0 "beta7")"
  B8="$(commit_version v1.0.0-beta.8 v1.0.0-beta.8 1 "beta8")"

  LOCAL="$BASE/local"
  git clone -q --branch v1.0.0-beta.7 --depth 1 "file://$REMOTE" "$LOCAL"
  git -C "$LOCAL" config user.email synthetic@example.invalid
  git -C "$LOCAL" config user.name Synthetic
  git -C "$LOCAL" remote set-url origin https://github.com/contabase-app/contabase.git

  CONF="$BASE/config"; APP="$BASE/app"; DATA="$BASE/data"; BIN="$BASE/bin"
  mkdir -p "$CONF" "$APP/templates" "$APP/assets" "$DATA/uploads" "$BIN" "$BASE/tmp"
  printf 'v1.0.0-beta.7\n' > "$APP/VERSION"
  printf 'old-binary\n' > "$APP/contabase"
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
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
CONTABASE_REPO_PATH=$LOCAL
CONTABASE_INSTALL_REF_KIND=tag
CONTABASE_INSTALL_REF=v1.0.0-beta.7
CONTABASE_INSTALL_COMMIT=$B7
CONTABASE_INSTALL_ORIGIN=https://github.com/contabase-app/contabase.git
CONTABASE_LOCAL_NOTE=preservar
EOF

  cat > "$BIN/id" <<'EOF'
#!/bin/sh
echo 0
EOF
  cat > "$BIN/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac
EOF
  cat > "$BIN/curl" <<'EOF'
#!/bin/sh
printf '{"status":"healthy"}\n'
EOF
  cat > "$BIN/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG:?}"
case "${1:-}" in is-active) echo active ;; esac
exit 0
EOF
  REAL_GIT="$(command -v git)"
  cat > "$BIN/git" <<EOF
#!/usr/bin/env bash
REAL_GIT="$REAL_GIT"
args=("\$@")
idx=0
[ "\${args[0]:-}" = "-C" ] && idx=2
cmd="\${args[\$idx]:-}"
if [ "\$cmd" = "fetch" ] && [ -n "\${TEST_REMOTE_DIR:-}" ]; then
  for ((i=0; i<\${#args[@]}; i++)); do
    case "\${args[i]}" in
      https://github.com/contabase-app/contabase.git|https://github.com/contabase-app/contabase|origin)
        args[i]="\$TEST_REMOTE_DIR"
        ;;
    esac
  done
fi
exec "$REAL_GIT" "\${args[@]}"
EOF
  chmod +x "$BIN/id" "$BIN/uname" "$BIN/curl" "$BIN/systemctl" "$BIN/git"
}

make_bundle() {
  local dest="$1" version="$2" commit="$3"
  rm -rf "$dest"
  mkdir -p "$dest/scripts/lib"
  cp "$UPDATER_SOURCE" "$dest/scripts/update-contabase-source.sh"
  cp "$UPDATER_DOCKER" "$dest/scripts/update-contabase-docker.sh"
  cp "$UPDATER_RELEASE" "$dest/scripts/update-contabase-release.sh"
  cp "$ROOT_DIR/scripts/lib/contabase-install-state.sh" "$dest/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-recovery.sh" "$dest/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-update-wrapper.sh" "$dest/scripts/lib/"
  cp "$ROOT_DIR/scripts/lib/contabase-docker-image-update.py" "$dest/scripts/lib/"
  chmod +x "$dest/scripts/update-contabase-source.sh" \
    "$dest/scripts/update-contabase-docker.sh" "$dest/scripts/update-contabase-release.sh"
  printf '%s\n' "$version" > "$dest/VERSION"
  seal_bundle "$dest" "$commit"
}

seal_bundle() {
  local dest="$1" commit="$2"
  (
    cd "$dest"
    printf 'commit=%s\n' "$commit"
    for file in VERSION scripts/update-contabase-source.sh scripts/update-contabase-docker.sh \
      scripts/update-contabase-release.sh scripts/lib/contabase-install-state.sh \
      scripts/lib/contabase-recovery.sh scripts/lib/contabase-update-wrapper.sh \
      scripts/lib/contabase-docker-image-update.py; do
      printf '%s  %s\n' "$(sha_file "$file")" "$file"
    done | LC_ALL=C sort -k2,2
  ) > "$dest/updater.sha256"
  BUNDLE_DIGEST="$(sha_file "$dest/updater.sha256")"
}

bundle_digest() {
  sha_file "$1/updater.sha256"
}

run_source() {
  local bundle="$1"; shift
  local rc=0
  (
    cd "$LOCAL"
    export PATH="$BIN:$PATH"
    export TMPDIR="$BASE/tmp"
    export CONTABASE_PID1_COMM=systemd
    export CONTABASE_REPO_ROOT="$LOCAL"
    export CONTABASE_STATE_FILE="$CONF/install-state.env"
    export CONTABASE_INSTALL_DIR="$APP"
    export DATA_DIR="$DATA"
    export CONTABASE_TMP_BINARY="$BASE/tmp/contabase"
    export CONTABASE_MODE_FILE="$CONF/install-mode"
    export CONTABASE_UPDATE_WRAPPER="$BASE/contabase-update"
    export CONTABASE_UPDATE_ALIAS="$BASE/cb-update"
    export CONTABASE_VERSION="${TARGET_VERSION-$BUNDLE_TARGET_VERSION}"
    export CONTABASE_UPDATER_BUNDLE_COMMIT="${TARGET_COMMIT-$B8}"
    export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="${TARGET_DIGEST-$(bundle_digest "$bundle")}"
    export SYSTEMCTL_LOG="$BASE/systemctl.log"
    export TEST_REMOTE_DIR="$REMOTE"
    bash "$bundle/scripts/update-contabase-source.sh" "$@" >"$BASE/out.log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}

bundle_rejected() {
  local label="$1" bundle="$2"; shift 2
  local before_state before_head before_marker before_journal rc
  before_state="$(sha_file "$CONF/install-state.env")"
  before_head="$(git -C "$LOCAL" rev-parse HEAD)"
  before_marker="no"
  [ -e "$CONF/finalize.pending" ] && before_marker="$(sha_file "$CONF/finalize.pending")"
  before_journal="no"
  [ -e "$CONF/transition.pending" ] && before_journal="$(sha_file "$CONF/transition.pending")"
  : > "$BASE/systemctl.log"
  rc="$(run_source "$bundle" "$@")"
  assert_eq "$label rc" "1" "$rc"
  assert_contains "$label diagnostico" "bundle externo do updater recusado" "$BASE/out.log"
  assert_eq "$label sem systemctl" "" "$(cat "$BASE/systemctl.log")"
  assert_eq "$label preserva estado" "$before_state" "$(sha_file "$CONF/install-state.env")"
  assert_eq "$label preserva HEAD" "$before_head" "$(git -C "$LOCAL" rev-parse HEAD)"
  if [ "$before_marker" = "no" ]; then
    assert_eq "$label sem marcador criado" "no" "$(test -e "$CONF/finalize.pending" && echo yes || echo no)"
  else
    assert_eq "$label preserva marcador" "$before_marker" "$(sha_file "$CONF/finalize.pending")"
  fi
  if [ "$before_journal" = "no" ]; then
    assert_eq "$label sem journal criado" "no" "$(test -e "$CONF/transition.pending" && echo yes || echo no)"
  else
    assert_eq "$label preserva journal" "$before_journal" "$(sha_file "$CONF/transition.pending")"
  fi
}

# ---------------------------------------------------------------------------
# Fixture base e bundle valido.
# ---------------------------------------------------------------------------
setup_fixture base
BUNDLE="$BASE/bundle"
make_bundle "$BUNDLE" v1.0.0-beta.8 "$B8"
assert_eq "bundle base digest" "$(bundle_digest "$BUNDLE")" "$(sha_file "$BUNDLE/updater.sha256")"

# 1. Beta 7 sem biblioteca F0: controle negativo do checkout local.
assert_eq "beta7 local sem F0" "no" \
  "$(test -e "$LOCAL/scripts/lib/contabase-install-state.sh" && echo yes || echo no)"
# 2. Docker reconhece o manifesto compartilhado (Source/Release/wrapper).
(
  export PATH="$BIN:$PATH"
  export CONTABASE_REPO_ROOT="$LOCAL"
  export CONTABASE_VERSION=v1.0.0-beta.8
  export CONTABASE_UPDATER_BUNDLE_COMMIT="$B8"
  export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="$(bundle_digest "$BUNDLE")"
  bash "$BUNDLE/scripts/update-contabase-docker.sh" --help >"$BASE/docker-help.log" 2>&1
) || true
assert_not_contains "docker aceita manifesto compartilhado" "bundle externo do updater recusado" "$BASE/docker-help.log"

# 3. Versao divergente no VERSION do bundle.
cp -R "$BUNDLE" "$BASE/bad_version"
printf 'v1.0.0-beta.9\n' > "$BASE/bad_version/VERSION"
seal_bundle "$BASE/bad_version" "$B8"
bundle_rejected "versao divergente" "$BASE/bad_version"

# 4. Commit divergente no cabecalho.
cp -R "$BUNDLE" "$BASE/bad_commit"
sed '1s/.*/commit=0000000000000000000000000000000000000000/' \
  "$BASE/bad_commit/updater.sha256" > "$BASE/bad_commit/new"
mv "$BASE/bad_commit/new" "$BASE/bad_commit/updater.sha256"
bundle_rejected "commit divergente" "$BASE/bad_commit"

# 5. Manifesto alterado (linha extra).
cp -R "$BUNDLE" "$BASE/bad_manifest"
printf 'comentario\n' >> "$BASE/bad_manifest/updater.sha256"
bundle_rejected "manifesto alterado" "$BASE/bad_manifest"

# 6. SHA-256 divergente de arquivo declarado.
cp -R "$BUNDLE" "$BASE/bad_hash"
printf '\nprintf hacked\n' >> "$BASE/bad_hash/scripts/lib/contabase-install-state.sh"
bundle_rejected "hash divergente" "$BASE/bad_hash"

# 7. Entrada duplicada / nao autorizada.
cp -R "$BUNDLE" "$BASE/duplicate"
awk 'NR==2 { print; print } { print }' "$BASE/duplicate/updater.sha256" > "$BASE/duplicate/new"
mv "$BASE/duplicate/new" "$BASE/duplicate/updater.sha256"
bundle_rejected "entrada duplicada" "$BASE/duplicate"
cp -R "$BUNDLE" "$BASE/unauthorized"
printf '%s  %s\n' "$(printf 'a%.0s' {1..64})" "scripts/evil.sh" >> "$BASE/unauthorized/updater.sha256"
bundle_rejected "entrada nao autorizada" "$BASE/unauthorized"

# 8. Arquivo ausente.
cp -R "$BUNDLE" "$BASE/missing"
rm -f "$BASE/missing/scripts/lib/contabase-recovery.sh"
bundle_rejected "arquivo ausente" "$BASE/missing"

# 9. Symlink de arquivo e de diretorio.
cp -R "$BUNDLE" "$BASE/symlink_file"
mv "$BASE/symlink_file/scripts/lib/contabase-recovery.sh" "$BASE/symlink_file/real-recovery.sh"
ln -s ../real-recovery.sh "$BASE/symlink_file/scripts/lib/contabase-recovery.sh"
bundle_rejected "symlink de arquivo" "$BASE/symlink_file"
cp -R "$BUNDLE" "$BASE/symlink_dir"
mv "$BASE/symlink_dir/scripts/lib" "$BASE/symlink_dir/lib-real"
ln -s ../lib-real "$BASE/symlink_dir/scripts/lib"
bundle_rejected "symlink de diretorio" "$BASE/symlink_dir"

# 10. Arquivo modificado com assume-unchanged.
cp -R "$BUNDLE" "$BASE/assume_unchanged"
git -C "$BASE/assume_unchanged" init -q
git -C "$BASE/assume_unchanged" config user.email synthetic@example.invalid
git -C "$BASE/assume_unchanged" config user.name Synthetic
git -C "$BASE/assume_unchanged" add -A
git -C "$BASE/assume_unchanged" commit -q -m fixture
git -C "$BASE/assume_unchanged" update-index --assume-unchanged scripts/lib/contabase-install-state.sh
printf '\nprintf sourced > "%s"\n' "$BASE/sentinel-assume" >> "$BASE/assume_unchanged/scripts/lib/contabase-install-state.sh"
bundle_rejected "assume-unchanged" "$BASE/assume_unchanged"
assert_eq "assume-unchanged nao executa sentinela" "no" \
  "$(test -e "$BASE/sentinel-assume" && echo yes || echo no)"

# 11. Caminho inseguro / traversal.
cp -R "$BUNDLE" "$BASE/traversal"
printf '%s  %s\n' "$(printf 'b%.0s' {1..64})" "../evil.sh" >> "$BASE/traversal/updater.sha256"
bundle_rejected "traversal" "$BASE/traversal"

# 12. Biblioteca adulterada: sentinela nao pode executar.
cp -R "$BUNDLE" "$BASE/library_tamper"
printf '\nprintf sourced > "%s"\n' "$BASE/sentinel-library" >> "$BASE/library_tamper/scripts/lib/contabase-install-state.sh"
bundle_rejected "biblioteca adulterada" "$BASE/library_tamper"
assert_eq "biblioteca adulterada nao executa sentinela" "no" \
  "$(test -e "$BASE/sentinel-library" && echo yes || echo no)"

# 13. Contrato de launcher ausente.
cp -R "$BUNDLE" "$BASE/no_contract"
TARGET_VERSION="v1.0.0-beta.8" TARGET_COMMIT="" TARGET_DIGEST="" \
  bundle_rejected "contrato ausente" "$BASE/no_contract"

# 14. Modo in-repo preservado (sem variaveis de bundle).
(
  cd "$LOCAL"
  export PATH="$BIN:$PATH"
  export CONTABASE_REPO_ROOT="$LOCAL"
  export CONTABASE_STATE_FILE="$CONF/install-state.env"
  export CONTABASE_INSTALL_DIR="$APP"
  export DATA_DIR="$DATA"
  export CONTABASE_PID1_COMM=systemd
  export SYSTEMCTL_LOG="$BASE/systemctl-inrepo.log"
  export TEST_REMOTE_DIR="$REMOTE"
  bash "$LOCAL/scripts/update-contabase-source.sh" >"$BASE/inrepo.log" 2>&1
) || true
assert_not_contains "modo in-repo nao exige bundle" "bundle externo do updater recusado" "$BASE/inrepo.log"

# 15. Branch mode com proveniencia completa e sem upstream.
cp -R "$BASE" "$BASE-branch"
BASE="$BASE-branch"
REMOTE="$BASE/remote"
LOCAL="$BASE/local"
CONF="$BASE/config"; APP="$BASE/app"; DATA="$BASE/data"; BIN="$BASE/bin"
rm -rf "$LOCAL"
git clone -q "file://$REMOTE" "$LOCAL"
git -C "$LOCAL" checkout -q -B main "$B8"
git -C "$LOCAL" config user.email synthetic@example.invalid
git -C "$LOCAL" config user.name Synthetic
git -C "$LOCAL" remote set-url origin https://github.com/contabase-app/contabase.git
git -C "$LOCAL" config --unset branch.main.remote 2>/dev/null || true
git -C "$LOCAL" config --unset branch.main.merge 2>/dev/null || true
printf 'v1.0.0-beta.8\n' > "$APP/VERSION"
cat > "$CONF/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=source
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.8
CONTABASE_REPO_PATH=$LOCAL
CONTABASE_INSTALL_REF_KIND=branch
CONTABASE_INSTALL_REF=main
CONTABASE_INSTALL_COMMIT=$B8
CONTABASE_INSTALL_ORIGIN=https://github.com/contabase-app/contabase.git
EOF
TARGET_VERSION="v1.0.0-beta.8" TARGET_COMMIT="$B8" TARGET_DIGEST="$(bundle_digest "$BUNDLE")" \
  run_source "$BUNDLE" >/dev/null 2>&1 || true
assert_contains "branch completa sem upstream" "Upstream resolvido: remote=origin ref=refs/heads/main" "$BASE/out.log"
assert_contains "branch completa sem upstream proveniencia" "Proveniencia persistida completa aceita" "$BASE/out.log"

# 16. Tag detached legitima: bundle aceito antes da falha controlada de comandos.
BASE="$TMP_BASE/base"
REMOTE="$BASE/remote"; LOCAL="$BASE/local"; CONF="$BASE/config"; APP="$BASE/app"; DATA="$BASE/data"; BIN="$BASE/bin"
TARGET_VERSION="v1.0.0-beta.8" TARGET_COMMIT="$B8" TARGET_DIGEST="$(bundle_digest "$BUNDLE")" \
  run_source "$BUNDLE" >/dev/null 2>&1 || true
assert_contains "tag detached bundle validado" "Bundle externo do updater validado" "$BASE/out.log"
assert_not_contains "tag detached sem mutacao de servico" "systemctl stop" "$BASE/out.log"

# 17. Proveniencia legada: tag sem REF_KIND/REF/COMMIT/ORIGIN.
cp -R "$BASE" "$BASE-legacy"
BASE="$BASE-legacy"
REMOTE="$BASE/remote"; LOCAL="$BASE/local"; CONF="$BASE/config"; APP="$BASE/app"; DATA="$BASE/data"; BIN="$BASE/bin"
sed '/^CONTABASE_INSTALL_REF/d;/^CONTABASE_INSTALL_COMMIT/d;/^CONTABASE_INSTALL_ORIGIN/d' \
  "$CONF/install-state.env" > "$CONF/state.tmp"
sed "s#^CONTABASE_REPO_PATH=.*#CONTABASE_REPO_PATH=$LOCAL#" \
  "$CONF/state.tmp" > "$CONF/install-state.env"
rm -f "$CONF/state.tmp"
TARGET_VERSION="v1.0.0-beta.8" TARGET_COMMIT="$B8" TARGET_DIGEST="$(bundle_digest "$BUNDLE")" \
  run_source "$BUNDLE" >/dev/null 2>&1 || true
assert_contains "proveniencia legada detectada" "Estado legado sem proveniencia" "$BASE/out.log"
assert_contains "proveniencia legada bundle validado" "Bundle externo do updater validado" "$BASE/out.log"

# 18. Retry com marcador: bundle invalido nao pode mutar marcador/estado.
cp -R "$BASE" "$BASE-retry"
BASE="$BASE-retry"
REMOTE="$BASE/remote"; LOCAL="$BASE/local"; CONF="$BASE/config"; APP="$BASE/app"; DATA="$BASE/data"; BIN="$BASE/bin"
printf 'schema=1\nmethod=source\ntarget_version=v1.0.0-beta.8\ntarget_kind=tag\ntarget_ref=v1.0.0-beta.8\ntarget_commit=%s\ntarget_origin=https://github.com/contabase-app/contabase.git\nchannel=beta\nprevious_checkout_version=v1.0.0-beta.7\nprevious_commit=%s\nprevious_state_version=v1.0.0-beta.7\nprevious_state_commit=%s\nprevious_marker_hash=none\nprevious_marker_version=none\nprevious_marker_commit=none\n' \
  "$B8" "$B7" "$B7" > "$CONF/transition.pending"
bundle_rejected "retry com bundle invalido" "$BASE/bad_hash"

# 19/20. Release: wrapper previamente verificado e wrapper divergente.
run_release() {
  local bundle="$1" wrapper_sha="$2"
  local rc=0
  (
    export PATH="$BIN:$PATH"
    export CONTABASE_VERSION=v1.0.0-beta.8
    export CONTABASE_CHANNEL=beta
    export CONTABASE_UPDATER_BUNDLE_COMMIT="$B8"
    export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="$(bundle_digest "$bundle")"
    export CONTABASE_UPDATE_WRAPPER_PATH="$bundle/scripts/lib/contabase-update-wrapper.sh"
    export CONTABASE_UPDATE_WRAPPER_SHA256="$wrapper_sha"
    bash "$bundle/scripts/update-contabase-release.sh" --validate-only >"$BASE/release.log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}
BASE="$TMP_BASE/base"
BIN="$BASE/bin"
run_release "$BUNDLE" "$(sha_file "$BUNDLE/scripts/lib/contabase-update-wrapper.sh")" >/dev/null 2>&1 || true
assert_not_contains "release wrapper verificado aceito" "bundle externo do updater recusado" "$BASE/release.log"
run_release "$BUNDLE" "0000000000000000000000000000000000000000000000000000000000000000" >/dev/null 2>&1 || true
assert_contains "release wrapper divergente recusado" "wrapper verificado diverge do hash autorizado" "$BASE/release.log"

# ---------------------------------------------------------------------------
# B1: intencao autenticada antes de --help e sem fallback para legado.
# ---------------------------------------------------------------------------
b1="$TMP_BASE/b1"
mkdir -p "$b1/source/scripts" "$b1/docker/scripts" "$b1/release/scripts" \
  "$b1/legacy/scripts/lib" "$b1/legacy/repo/scripts/lib"
cp "$UPDATER_SOURCE" "$b1/source/scripts/update-contabase-source.sh"
cp "$UPDATER_DOCKER" "$b1/docker/scripts/update-contabase-docker.sh"
cp "$UPDATER_RELEASE" "$b1/release/scripts/update-contabase-release.sh"
cp "$UPDATER_SOURCE" "$b1/legacy/scripts/update-contabase-source.sh"
cp "$UPDATER_DOCKER" "$b1/legacy/scripts/update-contabase-docker.sh"
cp "$UPDATER_RELEASE" "$b1/legacy/scripts/update-contabase-release.sh"
cp "$ROOT_DIR/scripts/lib/contabase-install-state.sh" "$b1/legacy/scripts/lib/"
cp "$ROOT_DIR/scripts/lib/contabase-recovery.sh" "$b1/legacy/scripts/lib/"

run_help() {
  local log="$1"; shift
  local rc=0
  "$@" --help >"$log" 2>&1 || rc=$?
  printf '%s' "$rc"
}

# Source parcial, raiz do script == checkout.
rc="$(CONTABASE_VERSION=v1.0.0-beta.8 CONTABASE_UPDATER_BUNDLE_COMMIT="" \
  run_help "$b1/source.log" bash "$b1/source/scripts/update-contabase-source.sh")"
assert_eq "B1 source parcial rc" "1" "$rc"
assert_contains "B1 source parcial diagnostico" "bundle externo do updater recusado" "$b1/source.log"
assert_not_contains "B1 source parcial nao imprime ajuda" "Uso:" "$b1/source.log"
assert_not_contains "B1 source parcial sem download" "Baixando" "$b1/source.log"

# Source com manifesto symlink pendente.
ln -s /nonexistent "$b1/source/updater.sha256"
rc="$(run_help "$b1/source_dangling.log" bash "$b1/source/scripts/update-contabase-source.sh")"
assert_eq "B1 source manifesto dangling rc" "1" "$rc"
assert_contains "B1 source manifesto dangling diagnostico" "bundle externo do updater recusado" "$b1/source_dangling.log"
assert_not_contains "B1 source manifesto dangling sem download" "Baixando" "$b1/source_dangling.log"
rm -f "$b1/source/updater.sha256"

# Docker parcial, raiz do script == checkout.
rc="$(CONTABASE_VERSION=v1.0.0-beta.8 CONTABASE_REPO_ROOT="$b1/docker" \
  CONTABASE_UPDATER_BUNDLE_COMMIT="" \
  run_help "$b1/docker.log" bash "$b1/docker/scripts/update-contabase-docker.sh")"
assert_eq "B1 docker parcial rc" "1" "$rc"
assert_contains "B1 docker parcial diagnostico" "digest autenticado" "$b1/docker.log"
assert_not_contains "B1 docker parcial nao imprime ajuda" "Uso:" "$b1/docker.log"
assert_not_contains "B1 docker parcial sem download" "Baixando" "$b1/docker.log"

# Release com manifesto presente e contrato ausente.
printf 'commit=%s\n' "$B8" > "$b1/release/updater.sha256"
rc="$(run_help "$b1/release_manifest.log" bash "$b1/release/scripts/update-contabase-release.sh")"
assert_eq "B1 release manifesto sem vars rc" "1" "$rc"
assert_contains "B1 release manifesto sem vars diagnostico" "CONTABASE_VERSION obrigatoria" "$b1/release_manifest.log"
assert_not_contains "B1 release manifesto sem vars nao imprime ajuda" "Uso:" "$b1/release_manifest.log"
assert_not_contains "B1 release manifesto sem vars sem download" "Baixando" "$b1/release_manifest.log"

# Release parcial com versao e commit, sem manifesto/wrapper.
rc="$(CONTABASE_VERSION=v1.0.0-beta.8 CONTABASE_UPDATER_BUNDLE_COMMIT="$B8" \
  run_help "$b1/release_partial.log" bash "$b1/release/scripts/update-contabase-release.sh")"
assert_eq "B1 release parcial rc" "1" "$rc"
assert_contains "B1 release parcial diagnostico" "identidade do bundle externo incompleta" "$b1/release_partial.log"
assert_not_contains "B1 release parcial nao imprime ajuda" "Uso:" "$b1/release_partial.log"
assert_not_contains "B1 release parcial sem download" "Baixando" "$b1/release_partial.log"

# Legado legitimo: sem manifesto e sem variaveis, --help continua funcionando.
rc="$(run_help "$b1/legacy_source.log" bash "$b1/legacy/scripts/update-contabase-source.sh")"
assert_eq "B1 legado source help rc" "0" "$rc"
assert_contains "B1 legado source help" "Uso:" "$b1/legacy_source.log"
rc="$(run_help "$b1/legacy_docker.log" bash "$b1/legacy/scripts/update-contabase-docker.sh")"
assert_eq "B1 legado docker help rc" "0" "$rc"
assert_contains "B1 legado docker help" "Uso:" "$b1/legacy_docker.log"
rc="$(run_help "$b1/legacy_release.log" bash "$b1/legacy/scripts/update-contabase-release.sh")"
assert_eq "B1 legado release help rc" "0" "$rc"
assert_contains "B1 legado release help" "Uso:" "$b1/legacy_release.log"

# ---------------------------------------------------------------------------
# B2: wrapper verificado e obrigatorio antes de download/stop/backup/startup.
# ---------------------------------------------------------------------------
run_release_contract() {
  local bundle="$1" wrapper_path="$2" wrapper_sha="$3" log="$4"
  local rc=0
  (
    export PATH="$BIN:$PATH"
    export CONTABASE_VERSION=v1.0.0-beta.8
    export CONTABASE_CHANNEL=beta
    export CONTABASE_UPDATER_BUNDLE_COMMIT="$B8"
    export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="$(bundle_digest "$bundle")"
    export CONTABASE_UPDATE_WRAPPER_PATH="$wrapper_path"
    export CONTABASE_UPDATE_WRAPPER_SHA256="$wrapper_sha"
    bash "$bundle/scripts/update-contabase-release.sh" --validate-only >"$log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}

b2="$TMP_BASE/b2"
wrapper="$BUNDLE/scripts/lib/contabase-update-wrapper.sh"
wrapper_sha="$(sha_file "$wrapper")"
version_sha="$(sha_file "$BUNDLE/VERSION")"

run_release_contract "$BUNDLE" "" "$wrapper_sha" "$b2-missing-path.log" >/dev/null 2>&1 || true
assert_contains "B2 wrapper path ausente" "identidade do bundle externo incompleta" "$b2-missing-path.log"
run_release_contract "$BUNDLE" "$wrapper" "" "$b2-missing-hash.log" >/dev/null 2>&1 || true
assert_contains "B2 wrapper hash ausente" "identidade do bundle externo incompleta" "$b2-missing-hash.log"
run_release_contract "$BUNDLE" "$BUNDLE/VERSION" "$version_sha" "$b2-version-as-wrapper.log" >/dev/null 2>&1 || true
assert_contains "B2 wrapper apontando para VERSION" "wrapper verificado fora do caminho canonico" "$b2-version-as-wrapper.log"
run_release_contract "$BUNDLE" "$wrapper" "0000000000000000000000000000000000000000000000000000000000000000" "$b2-tampered-hash.log" >/dev/null 2>&1 || true
assert_contains "B2 wrapper hash divergente" "wrapper verificado diverge do hash autorizado" "$b2-tampered-hash.log"
ln -sf "$wrapper" "$BUNDLE/scripts/lib/wrapper-link"
run_release_contract "$BUNDLE" "$BUNDLE/scripts/lib/wrapper-link" "$wrapper_sha" "$b2-symlink.log" >/dev/null 2>&1 || true
assert_contains "B2 wrapper symlink" "symlink ou componente inseguro" "$b2-symlink.log"
ln -sfn "$BUNDLE/scripts/lib" "$BUNDLE/scripts/lib-link"
run_release_contract "$BUNDLE" "$BUNDLE/scripts/lib-link/contabase-update-wrapper.sh" "$wrapper_sha" "$b2-parent-symlink.log" >/dev/null 2>&1 || true
assert_contains "B2 diretorio pai do wrapper symlink" "symlink ou componente inseguro" "$b2-parent-symlink.log"
rm -f "$BUNDLE/scripts/lib/wrapper-link" "$BUNDLE/scripts/lib-link"

for log in "$b2-missing-path.log" "$b2-missing-hash.log" "$b2-version-as-wrapper.log" \
  "$b2-tampered-hash.log" "$b2-symlink.log" "$b2-parent-symlink.log"; do
  assert_not_contains "B2 sem download antes da recusa ($(basename "$log"))" "Baixando" "$log"
  assert_not_contains "B2 sem systemctl antes da recusa ($(basename "$log"))" "systemctl" "$log"
done
rc="$(run_release_contract "$BUNDLE" "$wrapper" "$wrapper_sha" "$b2-valid.log")"
assert_not_contains "B2 wrapper correto aceito" "bundle externo do updater recusado" "$b2-valid.log"

# ---------------------------------------------------------------------------
# B4: qualquer executavel nao declarado, em qualquer profundidade, e recusado.
# ---------------------------------------------------------------------------
b4="$TMP_BASE/b4"
run_docker_help_contract() {
  local bundle="$1" log="$2"
  local rc=0
  (
    export PATH="$BIN:$PATH"
    export CONTABASE_REPO_ROOT="$bundle"
    export CONTABASE_VERSION=v1.0.0-beta.8
    export CONTABASE_UPDATER_BUNDLE_COMMIT="$B8"
    export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="$(bundle_digest "$bundle")"
    bash "$bundle/scripts/update-contabase-docker.sh" --help >"$log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}
run_release_help_contract() {
  local bundle="$1" log="$2"
  local rc=0
  (
    export PATH="$BIN:$PATH"
    export CONTABASE_VERSION=v1.0.0-beta.8
    export CONTABASE_CHANNEL=beta
    export CONTABASE_UPDATER_BUNDLE_COMMIT="$B8"
    export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="$(bundle_digest "$bundle")"
    export CONTABASE_UPDATE_WRAPPER_PATH="$bundle/scripts/lib/contabase-update-wrapper.sh"
    export CONTABASE_UPDATE_WRAPPER_SHA256="$(sha_file "$bundle/scripts/lib/contabase-update-wrapper.sh")"
    bash "$bundle/scripts/update-contabase-release.sh" --help >"$log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}

# ---------------------------------------------------------------------------
# B3: wrapper adulterado/ausente no bundle e recusado antes de source/exec.
# ---------------------------------------------------------------------------
b3="$TMP_BASE/b3"
cp -R "$BUNDLE" "$b3-source-wrapper"
printf '\n# wrapper adulterado no bundle\n' >> "$b3-source-wrapper/scripts/lib/contabase-update-wrapper.sh"
rc="$(run_source "$b3-source-wrapper")"
assert_eq "B3 source bundle wrapper adulterado rc" "1" "$rc"
assert_contains "B3 source bundle wrapper adulterado" "conteudo divergente no bundle: scripts/lib/contabase-update-wrapper.sh" "$BASE/out.log"

cp -R "$BUNDLE" "$b3-docker-wrapper"
printf '\n# wrapper adulterado no bundle\n' >> "$b3-docker-wrapper/scripts/lib/contabase-update-wrapper.sh"
rc="$(run_docker_help_contract "$b3-docker-wrapper" "$b3-docker-wrapper.log")"
assert_eq "B3 docker bundle wrapper adulterado rc" "1" "$rc"
assert_contains "B3 docker bundle wrapper adulterado" "conteudo divergente no bundle: scripts/lib/contabase-update-wrapper.sh" "$b3-docker-wrapper.log"

cp -R "$BUNDLE" "$b3-source-missing"
awk 'substr($0, 67) != "scripts/lib/contabase-update-wrapper.sh"' \
  "$b3-source-missing/updater.sha256" > "$b3-source-missing/new"
mv "$b3-source-missing/new" "$b3-source-missing/updater.sha256"
rc="$(run_source "$b3-source-missing")"
assert_eq "B3 source wrapper ausente do manifesto rc" "1" "$rc"
assert_contains "B3 source wrapper ausente do manifesto" "manifesto incompleto: scripts/lib/contabase-update-wrapper.sh" "$BASE/out.log"

cp -R "$BUNDLE" "$b3-docker-missing"
awk 'substr($0, 67) != "scripts/lib/contabase-update-wrapper.sh"' \
  "$b3-docker-missing/updater.sha256" > "$b3-docker-missing/new"
mv "$b3-docker-missing/new" "$b3-docker-missing/updater.sha256"
rc="$(run_docker_help_contract "$b3-docker-missing" "$b3-docker-missing.log")"
assert_eq "B3 docker wrapper ausente do manifesto rc" "1" "$rc"
assert_contains "B3 docker wrapper ausente do manifesto" "manifesto incompleto" "$b3-docker-missing.log"

cp -R "$BUNDLE" "$b4-source-root"
printf '#!/bin/sh\ntouch "%s"\n' "$b4-sentinel-root" > "$b4-source-root/extra-tool"
chmod 0755 "$b4-source-root/extra-tool"
rc="$(run_source "$b4-source-root")"
assert_eq "B4 source executavel extra na raiz rc" "1" "$rc"
assert_contains "B4 source executavel extra na raiz" "arquivo executavel nao declarado: extra-tool" "$BASE/out.log"
assert_eq "B4 source extra nao executa sentinela" "no" "$(test -e "$b4-sentinel-root" && echo yes || echo no)"

cp -R "$BUNDLE" "$b4-source-0650"
printf '#!/bin/sh\ntouch "%s"\n' "$b4-sentinel-0650" > "$b4-source-0650/scripts/lib/extra-0650.sh"
chmod 0650 "$b4-source-0650/scripts/lib/extra-0650.sh"
rc="$(run_source "$b4-source-0650")"
assert_eq "B4 source executavel 0650 rc" "1" "$rc"
assert_contains "B4 source executavel 0650" "arquivo executavel nao declarado: scripts/lib/extra-0650.sh" "$BASE/out.log"
assert_eq "B4 source 0650 nao executa sentinela" "no" "$(test -e "$b4-sentinel-0650" && echo yes || echo no)"

cp -R "$BUNDLE" "$b4-docker-root"
printf '#!/bin/sh\ntouch "%s"\n' "$b4-sentinel-docker" > "$b4-docker-root/extra-tool"
chmod 0755 "$b4-docker-root/extra-tool"
rc="$(run_docker_help_contract "$b4-docker-root" "$b4-docker-root.log")"
assert_eq "B4 docker executavel extra na raiz rc" "1" "$rc"
assert_contains "B4 docker executavel extra na raiz" "arquivo executavel nao declarado: extra-tool" "$b4-docker-root.log"
assert_eq "B4 docker extra nao executa sentinela" "no" "$(test -e "$b4-sentinel-docker" && echo yes || echo no)"

cp -R "$BUNDLE" "$b4-docker-omitted"
awk 'substr($0, 67) != "scripts/update-contabase-source.sh"' \
  "$b4-docker-omitted/updater.sha256" > "$b4-docker-omitted/new"
mv "$b4-docker-omitted/new" "$b4-docker-omitted/updater.sha256"
rc="$(run_docker_help_contract "$b4-docker-omitted" "$b4-docker-omitted.log")"
assert_eq "B4 docker permitido omitido rc" "1" "$rc"
assert_contains "B4 docker permitido omitido" "arquivo executavel nao declarado: scripts/update-contabase-source.sh" "$b4-docker-omitted.log"

cp -R "$BUNDLE" "$b4-release-root"
printf '#!/bin/sh\ntouch "%s"\n' "$b4-sentinel-release" > "$b4-release-root/extra-tool"
chmod 0755 "$b4-release-root/extra-tool"
rc="$(run_release_help_contract "$b4-release-root" "$b4-release-root.log")"
assert_eq "B4 release executavel extra na raiz rc" "1" "$rc"
assert_contains "B4 release executavel extra na raiz" "arquivo executavel nao declarado no bundle: extra-tool" "$b4-release-root.log"
assert_eq "B4 release extra nao executa sentinela" "no" "$(test -e "$b4-sentinel-release" && echo yes || echo no)"

cp -R "$BUNDLE" "$b4-release-0650"
printf '#!/bin/sh\ntouch "%s"\n' "$b4-sentinel-release-0650" > "$b4-release-0650/scripts/lib/extra-0650.sh"
chmod 0650 "$b4-release-0650/scripts/lib/extra-0650.sh"
rc="$(run_release_help_contract "$b4-release-0650" "$b4-release-0650.log")"
assert_eq "B4 release executavel 0650 rc" "1" "$rc"
assert_contains "B4 release executavel 0650" "arquivo executavel nao declarado no bundle: scripts/lib/extra-0650.sh" "$b4-release-0650.log"
assert_eq "B4 release 0650 nao executa sentinela" "no" "$(test -e "$b4-sentinel-release-0650" && echo yes || echo no)"

# ---------------------------------------------------------------------------
# B5: symlink no diretorio pai do bundle nao pode ser escondido pela
#     canonicalizacao.
# ---------------------------------------------------------------------------
b5="$TMP_BASE/b5"
ln -s "$BUNDLE" "$b5-bundle-link"
rc="$(run_source "$b5-bundle-link")"
assert_eq "B5 source pai do bundle symlink rc" "1" "$rc"
assert_contains "B5 source pai do bundle symlink" "symlink ou componente inseguro" "$BASE/out.log"

cp -R "$BUNDLE" "$b5-docker"
rm -rf "$b5-docker/scripts/lib"
ln -s "$BUNDLE/scripts/lib" "$b5-docker/scripts/lib"
rc="$(run_docker_help_contract "$b5-docker" "$b5-docker.log")"
assert_eq "B5 docker scripts/lib symlink rc" "1" "$rc"
assert_contains "B5 docker scripts/lib symlink" "symlink" "$b5-docker.log"

ln -s "$BUNDLE" "$b5-release-link"
rc="$(run_release_help_contract "$b5-release-link" "$b5-release-link.log")"
assert_eq "B5 release pai do bundle symlink rc" "1" "$rc"
assert_contains "B5 release pai do bundle symlink" "symlink ou componente inseguro" "$b5-release-link.log"
ln -s "$BUNDLE" "$b5-docker-link"
rc="$(run_docker_help_contract "$b5-docker-link" "$b5-docker-link.log")"
assert_eq "B5 docker pai do bundle symlink rc" "1" "$rc"
assert_contains "B5 docker pai do bundle symlink" "symlink ou componente inseguro" "$b5-docker-link.log"

# P2: nomes literais e inventario incompleto.
run_shell_bundle_help() {
  local method="$1" bundle="$2" log="$3"
  if [ "$method" = source ]; then
    run_source "$bundle" --help
    cp "$BASE/out.log" "$log"
  else
    run_release_help_contract "$bundle" "$log"
  fi
}

for method in source release; do
  copy="$b4-literal-$method"
  cp -R "$BUNDLE" "$copy"
  for name in $'VERSION\n' 'VERSI\117N'; do
    printf '#!/bin/sh\nexit 0\n' > "$copy/$name"
    chmod 0755 "$copy/$name"
    rc="$(run_shell_bundle_help "$method" "$copy" "$copy.log")"
    assert_eq "P2 $method executavel com nome literal rc" "1" "$rc"
    assert_contains "P2 $method executavel com nome literal" "arquivo executavel nao declarado" "$copy.log"
    rm -f "$copy/$name"
  done
  rc="$(run_shell_bundle_help "$method" "$copy" "$copy.log")"
  assert_eq "P2 $method controle LF sem extras" "0" "$rc"

  awk 'substr($0, 67) != "scripts/update-contabase-docker.sh"' \
    "$copy/updater.sha256" > "$copy/manifest"
  mv "$copy/manifest" "$copy/updater.sha256"
  rc="$(run_shell_bundle_help "$method" "$copy" "$copy.log")"
  assert_eq "P2 $method executavel permitido omitido rc" "1" "$rc"
  assert_contains "P2 $method executavel permitido omitido" "scripts/update-contabase-docker.sh" "$copy.log"
done

# O produtor pode emitir resultados validos antes de falhar.
real_find="$(command -v find)"
cat > "$BIN/find" <<'EOF_FIND'
#!/bin/sh
"$P2_REAL_FIND" "$@"
exit 74
EOF_FIND
chmod +x "$BIN/find"
for method in source release; do
  rc="$(P2_REAL_FIND="$real_find" run_shell_bundle_help "$method" "$BUNDLE" "$b4-find-$method.log")"
  assert_eq "P2 $method find incompleto rc" "1" "$rc"
  assert_contains "P2 $method find incompleto" "falha ao enumerar executaveis" "$b4-find-$method.log"
done
rm -f "$BIN/find"

copy="$b4-docker-unreadable"
cp -R "$BUNDLE" "$copy"
mkdir "$copy/blocked"
printf '#!/bin/sh\nexit 0\n' > "$copy/blocked/extra"
chmod 0755 "$copy/blocked/extra"
chmod 000 "$copy/blocked"

# Root pode ler um diretorio 000. Nesse caso, injete EACCES na chamada real
# de enumeracao. Injete EIO separadamente para cobrir outros erros de I/O.
real_python="$(command -v python3)"
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
if [ -r "$copy/blocked" ] && [ -x "$copy/blocked" ]; then
  rc="$(P2_REAL_PYTHON="$real_python" P2_BLOCKED_DIR="$copy/blocked" P2_SCAN_ERROR=EACCES \
    run_docker_help_contract "$copy" "$copy.log")"
else
  rc="$(P2_REAL_PYTHON="$real_python" run_docker_help_contract "$copy" "$copy.log")"
fi
assert_eq "P2 Docker EACCES rc" "1" "$rc"
assert_contains "P2 Docker EACCES propagado" "[Errno 13]" "$copy.log"
chmod 0700 "$copy/blocked"
rc="$(P2_REAL_PYTHON="$real_python" P2_BLOCKED_DIR="$copy/blocked" P2_SCAN_ERROR=EIO \
  run_docker_help_contract "$copy" "$copy.log")"
assert_eq "P2 Docker EIO injetado rc" "1" "$rc"
assert_contains "P2 Docker EIO propagado" "[Errno 5]" "$copy.log"
rm -f "$BIN/python3"
rm -rf "$copy/blocked"
rc="$(run_docker_help_contract "$copy" "$copy.log")"
assert_eq "P2 Docker inventario completo" "0" "$rc"

# O componente alias[1] e literal mesmo se alias1 existir no CWD.
p2_parent="$TMP_BASE/p2-parent"
mkdir "$p2_parent" "$LOCAL/alias1"
cp -R "$BUNDLE" "$p2_parent/bundle"
ln -s "$p2_parent" "$TMP_BASE/alias[1]"
aliased="$TMP_BASE/alias[1]/bundle"
rc="$(run_source "$aliased" --help)"
assert_eq "P2 Source pai alias[1] rc" "1" "$rc"
assert_contains "P2 Source pai alias[1] literal" "caminho do updater com symlink" "$BASE/out.log"
rc="$(cd "$LOCAL" && run_release_help_contract "$aliased" "$b5-release-glob.log")"
assert_eq "P2 Release pai alias[1] rc" "1" "$rc"
assert_contains "P2 Release pai alias[1] literal" "caminho do updater com symlink" "$b5-release-glob.log"
rc="$(cd "$LOCAL" && run_release_contract "$p2_parent/bundle" \
  "$aliased/scripts/lib/contabase-update-wrapper.sh" "$wrapper_sha" "$b5-wrapper-glob.log")"
assert_eq "P2 Release wrapper via alias[1] rc" "1" "$rc"
assert_contains "P2 Release wrapper via alias[1] literal" "caminho do wrapper com symlink" "$b5-wrapper-glob.log"
assert_not_contains "P2 Release wrapper recusado antes do download" "Baixando" "$b5-wrapper-glob.log"

rm -f "$TMP_BASE/alias[1]"
mv "$p2_parent" "$TMP_BASE/alias[1]"
rc="$(run_source "$aliased" --help)"
assert_eq "P2 Source diretorio literal alias[1] valido" "0" "$rc"
rc="$(cd "$LOCAL" && run_release_help_contract "$aliased" "$b5-release-glob.log")"
assert_eq "P2 Release diretorio literal alias[1] valido" "0" "$rc"
rmdir "$LOCAL/alias1"

# Nome ambiguo que, sem registros NUL, perderia a identidade e coincidiria
# com um caminho verificado do manifesto.
for method in source release; do
  copy="$b4-literal-updater-$method"
  cp -R "$BUNDLE" "$copy"
  case "$method" in
    source) ambiguous=$'scripts/update-contabase-source.sh\n' ;;
    release) ambiguous=$'scripts/update-contabase-release.sh\n' ;;
  esac
  printf '#!/bin/sh\nexit 0\n' > "$copy/$ambiguous"
  chmod 0755 "$copy/$ambiguous"
  rc="$(run_shell_bundle_help "$method" "$copy" "$copy.log")"
  assert_eq "P2 $method updater com quebra de linha rc" "1" "$rc"
  assert_contains "P2 $method updater com quebra de linha recusado" "arquivo executavel nao declarado" "$copy.log"
  rm -f "$copy/$ambiguous"
  rc="$(run_shell_bundle_help "$method" "$copy" "$copy.log")"
  assert_eq "P2 $method controle sem nome ambiguo" "0" "$rc"
done

# Docker: o mesmo componente literal, com alias1 no CWD, nao pode ser
# expandido para um caminho diferente do executado.
p2_docker_parent="$TMP_BASE/p2-docker-parent"
mkdir "$p2_docker_parent" "$LOCAL/alias1"
cp -R "$BUNDLE" "$p2_docker_parent/bundle"
ln -s "$p2_docker_parent" "$TMP_BASE/docker-alias[1]"
docker_aliased="$TMP_BASE/docker-alias[1]/bundle"
rc="$(cd "$LOCAL" && run_docker_help_contract "$docker_aliased" "$b5-docker-glob.log")"
assert_eq "P2 Docker pai alias[1] rc" "1" "$rc"
assert_contains "P2 Docker pai alias[1] literal" "caminho do updater com symlink" "$b5-docker-glob.log"
rm -f "$TMP_BASE/docker-alias[1]"
mv "$p2_docker_parent" "$TMP_BASE/docker-alias[1]"
rc="$(cd "$LOCAL" && run_docker_help_contract "$docker_aliased" "$b5-docker-glob-ok.log")"
assert_eq "P2 Docker diretorio literal alias[1] valido" "0" "$rc"
rmdir "$LOCAL/alias1"

# ---------------------------------------------------------------------------
# CRLF: os tres parsers rejeitam os mesmos bytes.
# ---------------------------------------------------------------------------
for method in source docker release; do
  copy="$b4-crlf-$method"
  cp -R "$BUNDLE" "$copy"
  sed $'s/$/\r/' "$copy/updater.sha256" > "$copy/updater-crlf"
  mv "$copy/updater-crlf" "$copy/updater.sha256"
  if [ "$method" = "source" ]; then
    rc="$(run_source "$copy")"
    log="$BASE/out.log"
  elif [ "$method" = "docker" ]; then
    rc="$(run_docker_help_contract "$copy" "$copy/log")"
    log="$copy/log"
  else
    rc="$(run_release_help_contract "$copy" "$copy/log")"
    log="$copy/log"
  fi
  assert_eq "CRLF $method rc" "1" "$rc"
  assert_contains "CRLF $method diagnostico" "CRLF" "$log"
done

# ---------------------------------------------------------------------------
# B6: Release --validate-only valida o commit autorizado.
# ---------------------------------------------------------------------------
b6="$TMP_BASE/b6"
mkdir -p "$b6/root/contabase/templates" "$b6/root/contabase/assets" "$b6/bin"
printf '%s\n' "v1.0.0-beta.8" > "$b6/root/contabase/VERSION"
for file in contabase admin; do
  printf 'fixture-%s\n' "$file" > "$b6/root/contabase/$file"
done
cp "$ROOT_DIR/LICENSE" "$b6/root/contabase/LICENSE"
cp "$ROOT_DIR/docs/binario/contabase.env.example" "$b6/root/contabase/contabase.env.example"
cp "$ROOT_DIR/docs/binario/contabase.service.example" "$b6/root/contabase/contabase.service.example"
tar -czf "$b6/artifact.tar.gz" -C "$b6/root" contabase
artifact_sha="$(sha_file "$b6/artifact.tar.gz")"
printf '%s  %s\n' "$artifact_sha" "contabase-linux-amd64.tar.gz" > "$b6/checksums.txt"

cat > "$b6/bin/curl" <<'CURL'
#!/bin/sh
out=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  *"/releases/download/"*"/contabase-linux-amd64.tar.gz") cp "$RELEASE_FIXTURE/artifact.tar.gz" "$out" ;;
  *"/releases/download/"*"/checksums.txt") cp "$RELEASE_FIXTURE/checksums.txt" "$out" ;;
  *"/scripts/ops/backup.sh") cp "$ROOT_DIR/scripts/ops/backup.sh" "$out" ;;
  *"/scripts/ops/restore.sh") cp "$ROOT_DIR/scripts/ops/restore.sh" "$out" ;;
  *"api.github.com/repos/contabase-app/contabase/commits/"*) printf '%s\n' "${CONTABASE_TEST_CANDIDATE_COMMIT:?}" ;;
  *) exit 1 ;;
esac
CURL
chmod +x "$b6/bin/curl"
RELEASE_FIXTURE="$b6"

run_release_validate() {
  local commit="$1" log="$2"
  local rc=0
  (
    export PATH="$b6/bin:$BIN:$PATH"
    export CONTABASE_VERSION=v1.0.0-beta.8
    export CONTABASE_CHANNEL=beta
    export CONTABASE_UPDATER_BUNDLE_COMMIT="$B8"
    export CONTABASE_UPDATER_BUNDLE_MANIFEST_SHA256="$(bundle_digest "$BUNDLE")"
    export CONTABASE_UPDATE_WRAPPER_PATH="$BUNDLE/scripts/lib/contabase-update-wrapper.sh"
    export CONTABASE_UPDATE_WRAPPER_SHA256="$(sha_file "$BUNDLE/scripts/lib/contabase-update-wrapper.sh")"
    export CONTABASE_TEST_UNAME_M=x86_64
    export CONTABASE_INSTALL_DIR="$b6/install"
    export CONTABASE_DATA_DIR="$b6/data"
    export CONTABASE_CONFIG_DIR="$b6/config"
    export CONTABASE_TEST_CANDIDATE_COMMIT="$commit"
    export RELEASE_FIXTURE="$b6"
    bash "$BUNDLE/scripts/update-contabase-release.sh" --validate-only >"$log" 2>&1
  ) || rc=$?
  printf '%s' "$rc"
}

rc="$(run_release_validate "$B8" "$b6-ok.log")"
assert_eq "B6 validate-only commit correto rc" "0" "$rc"
assert_contains "B6 validate-only commit correto conclui" "Validacao concluida" "$b6-ok.log"
assert_not_contains "B6 validate-only sem stop" "systemctl stop" "$b6-ok.log"
assert_not_contains "B6 validate-only sem backup" "Backup pre-update" "$b6-ok.log"
assert_not_contains "B6 validate-only sem startup" "systemctl restart" "$b6-ok.log"
assert_eq "B6 validate-only nao escreve estado" "no" "$(test -e "$b6/config/install-state.env" && echo yes || echo no)"

rc="$(run_release_validate "0000000000000000000000000000000000000000" "$b6-bad-commit.log")"
assert_eq "B6 validate-only commit divergente rc" "1" "$rc"
assert_contains "B6 validate-only commit divergente" "commit da tag diverge do bundle autorizado" "$b6-bad-commit.log"

cp -R "$BUNDLE" "$b6-bad-manifest"
printf 'comentario\n' >> "$b6-bad-manifest/updater.sha256"
run_release_contract "$b6-bad-manifest" "$b6-bad-manifest/scripts/lib/contabase-update-wrapper.sh" \
  "$(sha_file "$b6-bad-manifest/scripts/lib/contabase-update-wrapper.sh")" "$b6-bad-manifest.log" >/dev/null 2>&1 || true
assert_contains "B6 manifesto invalido falha antes do download" "formato, ordem, duplicata ou caminho invalido" "$b6-bad-manifest.log"
assert_not_contains "B6 manifesto invalido sem download" "Baixando" "$b6-bad-manifest.log"

# 21/22. Nenhuma execucao nao verificada e nenhuma mutacao antes das validacoes.
assert_eq "estado base preservado" "v1.0.0-beta.7" \
  "$(awk -F= '$1=="CONTABASE_INSTALLED_VERSION"{print $2}' "$TMP_BASE/base/config/install-state.env")"
assert_eq "HEAD base preservado" "$B7" "$(git -C "$TMP_BASE/base/local" rev-parse HEAD)"
assert_eq "nenhum sentinela executado" "" "$(find "$TMP_BASE" -name 'sentinel-*' -type f -print 2>/dev/null)"

printf '\n=== F3-B BUNDLE AUTH: %d passed, %d failed ===\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HELPER="$ROOT/.github/scripts/release-state.sh"
WORKFLOW="$ROOT/.github/workflows/release-artifacts.yml"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/release-state-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

PASS=0
FAIL=0
pass() { printf 'PASS: %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf 'FAIL: %s\n' "$1" >&2; FAIL=$((FAIL + 1)); }

TAG='v0.1.0-beta.8'
REPO="$TEST_ROOT/repo"
ASSETS_DIR="$REPO/release-dist"
SHIM_DIR="$TEST_ROOT/shim"
GH_LOG="$TEST_ROOT/gh.log"
mkdir -p "$REPO" "$ASSETS_DIR" "$SHIM_DIR"

git -C "$REPO" init -q
git -C "$REPO" config user.email 'release-test@example.invalid'
git -C "$REPO" config user.name 'release state test'
printf '%s\n' "$TAG" > "$REPO/VERSION"
git -C "$REPO" add VERSION
git -C "$REPO" commit -qm 'release fixture'
git -C "$REPO" tag -a "$TAG" -m 'release fixture'
TAG_COMMIT="$(git -C "$REPO" rev-parse "$TAG^{commit}")"

EXPECTED_ASSETS=(
  contabase-linux-amd64.tar.gz
  contabase-linux-arm64.tar.gz
  checksums.txt
)
for asset in "${EXPECTED_ASSETS[@]}"; do
  printf 'fixture for %s\n' "$asset" > "$ASSETS_DIR/$asset"
done

cat > "$SHIM_DIR/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${GH_LOG:?}"
case "${GH_MODE:-fixture}" in
  fixture) sed -n '1,$p' "${GH_FIXTURE:?}" ;;
  http403|http500|timeout) exit 1 ;;
  empty) exit 0 ;;
  invalid) printf '%s\n' 'not-json' ;;
  *) exit 2 ;;
esac
EOF
chmod +x "$SHIM_DIR/gh"

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

digest_for() {
  printf 'sha256:%s' "$(hash_file "$ASSETS_DIR/$1")"
}

asset_json() {
  local name="$1"
  local state="${2:-uploaded}"
  local digest="${3-$(digest_for "$name")}"
  printf '{"name":"%s","state":"%s","digest":"%s"}' "$name" "$state" "$digest"
}

all_assets_json() {
  local result=''
  local asset
  for asset in "${EXPECTED_ASSETS[@]}"; do
    [[ -n "$result" ]] && result+=','
    result+="$(asset_json "$asset")"
  done
  printf '[%s]' "$result"
}

write_fixture() {
  local name="$1"
  local payload="$2"
  GH_FIXTURE="$TEST_ROOT/$name.json"
  printf '%s\n' "$payload" > "$GH_FIXTURE"
}

write_release_fixture() {
  local name="$1"
  local draft="$2"
  local prerelease="$3"
  local assets_json="$4"
  local extra="${5:-}"
  write_fixture "$name" "[{\"id\":1,\"tag_name\":\"$TAG\",\"draft\":$draft,\"prerelease\":$prerelease,\"assets\":$assets_json$extra}]"
}

reset_context() {
  printf '%s\n' "$TAG" > "$REPO/VERSION"
  TEST_EVENT='push'
  TEST_REF_TYPE='tag'
  TEST_REF="refs/tags/$TAG"
  TEST_GH_BIN="$SHIM_DIR/gh"
  GH_MODE='fixture'
  : > "$GH_LOG"
}

run_state() {
  (
    cd "$REPO"
    RELEASE_EVENT_NAME="$TEST_EVENT" \
    RELEASE_REF_TYPE="$TEST_REF_TYPE" \
    RELEASE_REF="$TEST_REF" \
    GITHUB_REPOSITORY='contabase-app/contabase' \
    GH_BIN="$TEST_GH_BIN" \
    GH_MODE="$GH_MODE" \
    GH_FIXTURE="$GH_FIXTURE" \
    GH_LOG="$GH_LOG" \
    GITHUB_TOKEN='token-must-not-leak' \
      "$HELPER" "$1" "$2" "$ASSETS_DIR"
  )
}

capture_state() {
  local tag="$1"
  local commit="$2"
  CAPTURE_RESULT=0
  CAPTURE_OUTPUT="$(run_state "$tag" "$commit" 2>"$TEST_ROOT/capture.stderr")" || CAPTURE_RESULT=$?
  CAPTURE_STATE="$(printf '%s\n' "$CAPTURE_OUTPUT" | jq -r '.state // empty' 2>/dev/null || true)"
}

assert_result() {
  local label="$1"
  local expected_state="$2"
  local expected_exit="$3"
  if [[ "$CAPTURE_STATE" == "$expected_state" && "$CAPTURE_RESULT" == "$expected_exit" ]]; then
    pass "$label"
  else
    fail "$label (state=$CAPTURE_STATE exit=$CAPTURE_RESULT)"
  fi
}

assert_upload_count() {
  local label="$1"
  local expected="$2"
  local actual
  actual="$(printf '%s\n' "$CAPTURE_OUTPUT" | jq -r '.upload | length' 2>/dev/null || printf '%s' invalid)"
  if [[ "$actual" == "$expected" ]]; then
    pass "$label"
  else
    fail "$label (upload count=$actual)"
  fi
}

# A: valid tag, VERSION and peeled annotated-tag SHA.
reset_context
write_fixture A '[]'
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'A: valid tag, VERSION and SHA' missing 0
assert_upload_count 'A: missing release schedules all assets' 3

# B: non-tag ref.
reset_context
write_fixture B '[]'
TEST_REF_TYPE='branch'
TEST_REF='refs/heads/beta'
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'B: non-tag ref rejected' incompatible 1

# C: empty tag.
reset_context
write_fixture C '[]'
capture_state '' "$TAG_COMMIT"
assert_result 'C: empty tag rejected' incompatible 1

# D: invalid SemVer.
reset_context
write_fixture D '[]'
TEST_REF='refs/tags/not-a-version'
capture_state 'not-a-version' "$TAG_COMMIT"
assert_result 'D: invalid SemVer rejected' incompatible 1

# E: VERSION mismatch.
reset_context
write_fixture E '[]'
printf '%s\n' 'v0.1.0-beta.7' > "$REPO/VERSION"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'E: VERSION mismatch rejected' incompatible 1

# F: tag points to a different SHA.
reset_context
write_fixture F '[]'
printf '%s\n' 'different commit' > "$REPO/marker"
git -C "$REPO" add marker
git -C "$REPO" commit -qm 'different commit'
OTHER_COMMIT="$(git -C "$REPO" rev-parse HEAD)"
capture_state "$TAG" "$OTHER_COMMIT"
assert_result 'F: tag SHA mismatch rejected' incompatible 1

# G-J: gh absence and API command failures are never missing.
reset_context
write_fixture G '[]'
TEST_GH_BIN="$TEST_ROOT/no-gh"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'G: missing gh rejected' api-error 1

for case_name in H I J; do
  reset_context
  write_fixture "$case_name" '[]'
  case "$case_name" in
    H) GH_MODE='http403'; label='H: API 403 rejected' ;;
    I) GH_MODE='http500'; label='I: API 500 rejected' ;;
    J) GH_MODE='timeout'; label='J: API timeout rejected' ;;
  esac
  capture_state "$TAG" "$TAG_COMMIT"
  assert_result "$label" api-error 1
done

# K-L: empty and invalid JSON are fail-closed.
reset_context
write_fixture K '[]'
GH_MODE='empty'
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'K: empty API output rejected' api-error 1

reset_context
write_fixture L '[]'
GH_MODE='invalid'
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'L: invalid API JSON rejected' api-error 1

# M: a valid empty release list is the only missing state.
reset_context
write_fixture M '[]'
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'M: valid empty release list is missing' missing 0

# N: compatible draft without assets uploads all expected files.
reset_context
write_release_fixture N true true '[]'
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'N: draft without assets is compatible' draft-compatible 0
assert_upload_count 'N: draft without assets uploads three' 3

# O: compatible draft with all identical assets uploads nothing.
reset_context
write_release_fixture O true true "$(all_assets_json)"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'O: complete identical draft is compatible' draft-compatible 0
assert_upload_count 'O: complete identical draft uploads zero' 0

# P: compatible partial draft uploads only missing assets.
reset_context
write_release_fixture P true true "[$(asset_json contabase-linux-amd64.tar.gz)]"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'P: partial identical draft is compatible' draft-compatible 0
assert_upload_count 'P: partial draft uploads only missing assets' 2

# Q-U: every incompatible asset state blocks before upload.
reset_context
write_release_fixture Q true true "[$(asset_json contabase-linux-amd64.tar.gz uploaded sha256:0000000000000000000000000000000000000000000000000000000000000000),$(asset_json contabase-linux-arm64.tar.gz),$(asset_json checksums.txt)]"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'Q: divergent asset rejected' incompatible 1

reset_context
write_release_fixture R true true "[$(asset_json contabase-linux-amd64.tar.gz uploaded ''),$(asset_json contabase-linux-arm64.tar.gz),$(asset_json checksums.txt)]"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'R: asset without digest rejected' incompatible 1

reset_context
write_release_fixture S true true "[$(asset_json contabase-linux-amd64.tar.gz),$(asset_json contabase-linux-amd64.tar.gz),$(asset_json contabase-linux-arm64.tar.gz),$(asset_json checksums.txt)]"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'S: duplicate asset rejected' incompatible 1

reset_context
write_release_fixture T true true "[$(all_assets_json),{\"name\":\"unexpected.txt\",\"state\":\"uploaded\",\"digest\":\"sha256:0000000000000000000000000000000000000000000000000000000000000000\"}]"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'T: unexpected asset rejected' incompatible 1

reset_context
write_release_fixture U true true "[$(asset_json contabase-linux-amd64.tar.gz starter),$(asset_json contabase-linux-arm64.tar.gz),$(asset_json checksums.txt)]"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'U: asset with invalid state rejected' incompatible 1

# V-W: every non-draft or non-prerelease release blocks.
reset_context
write_release_fixture V false true "$(all_assets_json)"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'V: published release rejected' published 1

reset_context
write_release_fixture W true false "$(all_assets_json)"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'W: incompatible release rejected' incompatible 1

# X: rerunning the same compatible fixture converges.
reset_context
write_release_fixture X true true "$(all_assets_json)"
capture_state "$TAG" "$TAG_COMMIT"
first_output="$CAPTURE_OUTPUT"
capture_state "$TAG" "$TAG_COMMIT"
if [[ "$CAPTURE_RESULT" == '0' && "$CAPTURE_OUTPUT" == "$first_output" ]]; then
  pass 'X: rerun is convergent'
else
  fail 'X: rerun is not convergent'
fi

# Y: the classifier performs a paginated read only.
reset_context
write_fixture Y '[]'
capture_state "$TAG" "$TAG_COMMIT"
if [[ "$CAPTURE_RESULT" == '0' ]] && grep -Fqx "api --paginate repos/contabase-app/contabase/releases?per_page=100" "$GH_LOG" && ! grep -Eq 'POST|PUT|PATCH|DELETE|upload' "$GH_LOG"; then
  pass 'Y: classification performs no remote write'
else
  fail 'Y: classification invoked an unexpected gh command'
fi

# Z: no API body or token leaks through classifier output.
reset_context
write_release_fixture Z true true "$(all_assets_json)" ',"body":"secret-body-token-must-not-leak"'
capture_state "$TAG" "$TAG_COMMIT"
if [[ "$CAPTURE_RESULT" == '0' ]] && ! grep -Fq 'secret-body-token-must-not-leak' <<<"$CAPTURE_OUTPUT" && ! grep -Fq 'token-must-not-leak' <<<"$CAPTURE_OUTPUT"; then
  pass 'Z: output does not leak release body or token'
else
  fail 'Z: sensitive output leaked'
fi

# AA: duplicate releases for the same tag are an unknown, blocking state.
reset_context
write_fixture AA "[{\"id\":1,\"tag_name\":\"$TAG\"},{\"id\":2,\"tag_name\":\"$TAG\"}]"
capture_state "$TAG" "$TAG_COMMIT"
assert_result 'AA: duplicate releases for one tag rejected' unknown 1

# Structural checks are exact, contextual checks of the relevant workflow declarations.
if ! grep -Fqx '  workflow_dispatch:' "$WORKFLOW" &&
   grep -Fqx '      - "v*-beta.*"' "$WORKFLOW" &&
   grep -Fqx '          if [[ "$EVENT_NAME" != "push" || "$REF_TYPE" != "tag" ]]; then' "$WORKFLOW" &&
   grep -Fqx '          if [[ "$GIT_REF" != "refs/tags/$version" ]]; then' "$WORKFLOW" &&
   grep -Fqx '          if ! tag_commit="$(git rev-parse --verify -q "refs/tags/$version^{commit}")"; then' "$WORKFLOW" &&
   grep -Fqx '          if [[ "$tag_commit" != "$workflow_commit" ]]; then' "$WORKFLOW" &&
   grep -Fqx '          RELEASE_EVENT_NAME: ${{ github.event_name }}' "$WORKFLOW" &&
   grep -Fqx '          RELEASE_REF_TYPE: ${{ github.ref_type }}' "$WORKFLOW" &&
   grep -Fqx '          RELEASE_REF: ${{ github.ref }}' "$WORKFLOW"; then
  pass 'S1: tag-only trigger and tag/commit guard'
else
  fail 'S1: tag-only trigger or tag/commit guard'
fi

release_line="$(grep -nF '      - name: Preflight release state' "$WORKFLOW" | cut -d: -f1)"
action_line="$(grep -nF '      - name: Attach assets to GitHub Release' "$WORKFLOW" | cut -d: -f1)"
if grep -Fqx '          draft: true' "$WORKFLOW" &&
   grep -Fqx '          prerelease: true' "$WORKFLOW" &&
   grep -Fqx '          fail_on_unmatched_files: true' "$WORKFLOW" &&
   grep -Fqx "        if: (steps.release-state.outputs.state == 'missing' || steps.release-state.outputs.state == 'draft-compatible') && steps.release-state.outputs.upload_count != '0'" "$WORKFLOW" &&
   [[ -n "$release_line" && -n "$action_line" && "$release_line" -lt "$action_line" ]]; then
  pass 'S2: preflight precedes dynamic draft-only action'
else
  fail 'S2: release action contract'
fi

if grep -Fqx '  group: release-artifacts-${{ github.repository }}-${{ github.ref }}' "$WORKFLOW" &&
   grep -Fqx '  cancel-in-progress: false' "$WORKFLOW" &&
   grep -Fqx '      - artifacts' "$WORKFLOW" &&
   ! grep -Fq 'if: always()' "$WORKFLOW" &&
   ! grep -Fq 'draft: false' "$WORKFLOW" &&
   ! grep -Eq 'git (push|tag)|--force|force push|contabase-canal' "$WORKFLOW"; then
  pass 'S3: concurrency, GHCR dependency and no automatic publication'
else
  fail 'S3: workflow safety boundaries'
fi

if grep -Fqx '  contents: read' "$WORKFLOW" &&
   grep -Fqx '      contents: write' "$WORKFLOW" &&
   grep -Fqx '      packages: write' "$WORKFLOW" &&
   ! grep -Fq 'id-token: write' "$WORKFLOW" &&
   ! grep -Fq 'pull-requests: write' "$WORKFLOW"; then
  pass 'S4: minimum permissions'
else
  fail 'S4: workflow permissions'
fi

printf '\n=== Results: %d passed, %d failed ===\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]

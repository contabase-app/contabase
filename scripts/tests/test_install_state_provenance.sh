#!/usr/bin/env bash
# F0: contrato sintetico de estado/proveniencia Source/Docker.
# Nao executa instaladores, updaters, backup, restore, systemd, Docker ou rede.
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
LIB="${ROOT_DIR}/scripts/lib/contabase-install-state.sh"
[ -f "$LIB" ] || { printf 'missing library: %s\n' "$LIB" >&2; exit 1; }
. "$LIB"

TMP_BASE="$(mktemp -d "${TMPDIR:-/tmp}/contabase-state-provenance.XXXXXX")" || exit 1
TMP_BASE="$(cd "$TMP_BASE" && pwd -P)"
cleanup() { rm -rf "$TMP_BASE"; }
trap cleanup EXIT

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }
bad() { FAIL=$((FAIL + 1)); printf 'FAIL: %s\n' "$*" >&2; }
assert_eq() {
  local label="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then ok "$label"; else bad "$label (expected='$expected' actual='$actual')"; fi
}
assert_fail() {
  local label="$1"; shift
  if "$@" >/dev/null 2>&1; then bad "$label (unexpected success)"; else ok "$label"; fi
}
assert_rc() {
  local label="$1" expected="$2"; shift 2
  "$@" >/dev/null 2>&1
  local rc=$?
  if [ "$rc" = "$expected" ]; then ok "$label"; else bad "$label (expected rc=$expected got rc=$rc)"; fi
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
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

make_repo() {
  local repo="$1" version="${2:-v1.0.0-beta.7}"
  mkdir -p "$repo"
  git -C "$repo" init -q
  git -C "$repo" symbolic-ref HEAD refs/heads/main
  git -C "$repo" config user.email synthetic@example.invalid
  git -C "$repo" config user.name Synthetic
  printf '%s\n' "$version" > "$repo/VERSION"
  printf 'fixture\n' > "$repo/file.txt"
  git -C "$repo" add VERSION file.txt
  git -C "$repo" commit -q -m "fixture ${version}"
  git -C "$repo" remote add origin https://github.com/contabase-app/contabase.git
}

set_upstream() {
  local repo="$1" branch="${2:-main}"
  git -C "$repo" config "branch.${branch}.remote" origin
  git -C "$repo" config "branch.${branch}.merge" "refs/heads/${branch}"
}

make_state() {
  local file="$1" repo="$2" method="${3:-source}" channel="${4:-beta}" \
    version="${5:-v1.0.0-beta.7}" kind="${6:-}" ref="${7:-}" commit="${8:-}" \
    origin="${9:-https://github.com/contabase-app/contabase.git}"
  {
    printf 'CONTABASE_STATE_VERSION=1\n'
    printf 'CONTABASE_INSTALL_METHOD=%s\n' "$method"
    printf 'CONTABASE_CHANNEL=%s\n' "$channel"
    printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$version"
    printf 'CONTABASE_REPO_PATH=%s\n' "$repo"
    if [ -n "$kind" ]; then
      printf 'CONTABASE_INSTALL_REF_KIND=%s\n' "$kind"
      printf 'CONTABASE_INSTALL_REF=%s\n' "$ref"
      printf 'CONTABASE_INSTALL_COMMIT=%s\n' "$commit"
      printf 'CONTABASE_INSTALL_ORIGIN=%s\n' "$origin"
    fi
  } > "$file"
}

# ---------------------------------------------------------------------------
# 1. Deteccao: branch com upstream, tag anotada e tag lightweight.
# ---------------------------------------------------------------------------
repo_branch="$TMP_BASE/repo-branch"
make_repo "$repo_branch"
set_upstream "$repo_branch"
if provenance_detect "$repo_branch" "v1.0.0-beta.7"; then
  assert_eq "branch: kind" "branch" "$PROVENANCE_KIND"
  assert_eq "branch: ref" "main" "$PROVENANCE_REF"
  assert_eq "branch: origin" "$CONTABASE_OFFICIAL_ORIGIN" "$PROVENANCE_ORIGIN"
  assert_eq "branch: commit" "$(git -C "$repo_branch" rev-parse HEAD)" "$PROVENANCE_COMMIT"
else
  bad "branch com upstream deveria ser detectada"
fi

repo_tag_annotated="$TMP_BASE/repo-tag-annotated"
make_repo "$repo_tag_annotated"
git -C "$repo_tag_annotated" tag -a v1.0.0-beta.7 -m release
git -C "$repo_tag_annotated" checkout -q --detach HEAD
if provenance_detect "$repo_tag_annotated" "v1.0.0-beta.7"; then
  assert_eq "tag anotada: kind" "tag" "$PROVENANCE_KIND"
  assert_eq "tag anotada: ref" "v1.0.0-beta.7" "$PROVENANCE_REF"
else
  bad "tag anotada deveria ser detectada"
fi

repo_tag_light="$TMP_BASE/repo-tag-light"
make_repo "$repo_tag_light"
git -C "$repo_tag_light" tag v1.0.0-beta.7
git -C "$repo_tag_light" checkout -q --detach HEAD
if provenance_detect "$repo_tag_light" "v1.0.0-beta.7"; then
  assert_eq "tag lightweight: kind" "tag" "$PROVENANCE_KIND"
  assert_eq "tag lightweight: ref" "v1.0.0-beta.7" "$PROVENANCE_REF"
else
  bad "tag lightweight deveria ser detectada"
fi

repo_no_upstream="$TMP_BASE/repo-no-upstream"
make_repo "$repo_no_upstream"
assert_fail "branch sem upstream recusada" provenance_detect "$repo_no_upstream" "v1.0.0-beta.7"

repo_detached_no_tag="$TMP_BASE/repo-detached-no-tag"
make_repo "$repo_detached_no_tag"
git -C "$repo_detached_no_tag" checkout -q --detach HEAD
assert_fail "detached sem tag exata recusado" provenance_detect "$repo_detached_no_tag" "v1.0.0-beta.7"

repo_multi_tag="$TMP_BASE/repo-multi-tag"
make_repo "$repo_multi_tag"
git -C "$repo_multi_tag" tag v1.0.0-beta.7
git -C "$repo_multi_tag" tag v1.0.0-beta.7-extra
git -C "$repo_multi_tag" checkout -q --detach HEAD
assert_fail "detached com multiplas tags recusado" provenance_detect "$repo_multi_tag" "v1.0.0-beta.7"

repo_tag_mismatch="$TMP_BASE/repo-tag-mismatch"
make_repo "$repo_tag_mismatch"
git -C "$repo_tag_mismatch" tag v1.0.0-beta.8
git -C "$repo_tag_mismatch" checkout -q --detach HEAD
assert_fail "tag divergente de VERSION recusada" provenance_detect "$repo_tag_mismatch" "v1.0.0-beta.7"

repo_wrong_origin="$TMP_BASE/repo-wrong-origin"
make_repo "$repo_wrong_origin"
git -C "$repo_wrong_origin" remote set-url origin https://github.com/example/contabase.git
assert_fail "origem nao oficial recusada" provenance_detect "$repo_wrong_origin" "v1.0.0-beta.7"

repo_amb_origin="$TMP_BASE/repo-amb-origin"
make_repo "$repo_amb_origin"
git -C "$repo_amb_origin" remote set-url --add origin https://github.com/example/contabase.git
assert_fail "origem ambigua recusada" provenance_detect "$repo_amb_origin" "v1.0.0-beta.7"

# ---------------------------------------------------------------------------
# 2. Estado legado e renderizacao preservando campos.
# ---------------------------------------------------------------------------
state_legacy="$TMP_BASE/legacy.env"
make_state "$state_legacy" "$repo_branch"
printf 'CONTABASE_CUSTOM_FIELD=preservar\n' >> "$state_legacy"
assert_rc "estado legado e reconhecido como legado" 2 provenance_validate_state "$state_legacy" "$repo_branch"

state_out="$TMP_BASE/rendered.env"
state_render "$state_legacy" "source" "beta" "v1.0.0-beta.8" "$repo_branch" \
  "branch" "main" "$(git -C "$repo_branch" rev-parse HEAD)" \
  "https://github.com/contabase-app/contabase.git" > "$state_out" || true
assert_contains "render preserva campo desconhecido" "CONTABASE_CUSTOM_FIELD=preservar" "$state_out"
assert_contains "render preserva canal beta" "CONTABASE_CHANNEL=beta" "$state_out"
assert_contains "render grava REF_KIND" "CONTABASE_INSTALL_REF_KIND=branch" "$state_out"
assert_contains "render grava REF" "CONTABASE_INSTALL_REF=main" "$state_out"
assert_contains "render grava ORIGIN oficial" "CONTABASE_INSTALL_ORIGIN=$CONTABASE_OFFICIAL_ORIGIN" "$state_out"
assert_not_contains "render nao persiste token" "CONTABASE_SETUP_TOKEN" "$state_out"

for channel in beta stable pinned; do
  state_render "$state_legacy" "source" "$channel" "v1.0.0-beta.7" "$repo_branch" > "$state_out" || true
  assert_contains "canal ${channel} preservado" "CONTABASE_CHANNEL=${channel}" "$state_out"
done

state_prov="$TMP_BASE/provenance.env"
make_state "$state_prov" "$repo_branch" source beta v1.0.0-beta.7 branch main \
  "$(git -C "$repo_branch" rev-parse HEAD)" "https://github.com/contabase-app/contabase.git"
assert_rc "proveniencia branch valida" 0 provenance_validate_state "$state_prov" "$repo_branch"
state_prov_out="$TMP_BASE/provenance-rendered.env"
state_render "$state_prov" "source" "beta" "v1.0.0-beta.7" "$repo_branch" > "$state_prov_out" || true
assert_contains "render preserva REF_KIND existente" "CONTABASE_INSTALL_REF_KIND=branch" "$state_prov_out"
assert_contains "render preserva REF existente" "CONTABASE_INSTALL_REF=main" "$state_prov_out"
assert_contains "render preserva COMMIT existente" "CONTABASE_INSTALL_COMMIT=$(git -C "$repo_branch" rev-parse HEAD)" "$state_prov_out"
assert_contains "render preserva ORIGIN existente" "CONTABASE_INSTALL_ORIGIN=$CONTABASE_OFFICIAL_ORIGIN" "$state_prov_out"

state_tag="$TMP_BASE/tag-state.env"
make_state "$state_tag" "$repo_tag_annotated" source beta v1.0.0-beta.7 tag v1.0.0-beta.7 \
  "$(git -C "$repo_tag_annotated" rev-parse HEAD)" "https://github.com/contabase-app/contabase.git"
assert_rc "proveniencia tag anotada valida" 0 provenance_validate_state "$state_tag" "$repo_tag_annotated"

repo_other="$TMP_BASE/repo-other"
make_repo "$repo_other"
state_repo_mismatch="$TMP_BASE/repo-mismatch.env"
sed "s#^CONTABASE_REPO_PATH=.*#CONTABASE_REPO_PATH=$repo_other#" "$state_prov" > "$state_repo_mismatch"
assert_fail "caminho do checkout divergente recusado" provenance_validate_state "$state_repo_mismatch" "$repo_branch"

state_head="$TMP_BASE/head-divergent.env"
make_state "$state_head" "$repo_branch" source beta v1.0.0-beta.7 branch main \
  "0000000000000000000000000000000000000000" "https://github.com/contabase-app/contabase.git"
assert_fail "HEAD divergente do estado recusado" provenance_validate_state "$state_head" "$repo_branch"

state_short="$TMP_BASE/short-commit.env"
make_state "$state_short" "$repo_branch" source beta v1.0.0-beta.7 branch main \
  "abc" "https://github.com/contabase-app/contabase.git"
assert_fail "commit invalido recusado" provenance_validate_state "$state_short" "$repo_branch"

state_wrong_origin="$TMP_BASE/wrong-origin.env"
make_state "$state_wrong_origin" "$repo_branch" source beta v1.0.0-beta.7 branch main \
  "$(git -C "$repo_branch" rev-parse HEAD)" "https://github.com/example/contabase.git"
assert_fail "origem incorreta no estado recusada" provenance_validate_state "$state_wrong_origin" "$repo_branch"

state_partial="$TMP_BASE/partial.env"
make_state "$state_partial" "$repo_branch"
printf 'CONTABASE_INSTALL_REF_KIND=branch\n' >> "$state_partial"
assert_fail "proveniencia parcial recusada" provenance_validate_state "$state_partial" "$repo_branch"
assert_fail "render de proveniencia parcial recusado" state_render "$state_partial" \
  "source" "beta" "v1.0.0-beta.7" "$repo_branch" "branch" "" "" ""

state_dup="$TMP_BASE/duplicate.env"
make_state "$state_dup" "$repo_branch"
printf 'CONTABASE_CHANNEL=beta\n' >> "$state_dup"
assert_fail "campo duplicado recusado" state_validate_file "$state_dup"

state_unexpected="$TMP_BASE/unexpected.env"
make_state "$state_unexpected" "$repo_branch"
printf 'FOO=bar\n' >> "$state_unexpected"
assert_fail "campo inesperado recusado" state_validate_file "$state_unexpected"

state_secret="$TMP_BASE/secret.env"
make_state "$state_secret" "$repo_branch"
printf 'CONTABASE_SETUP_TOKEN=synthetic-token\n' >> "$state_secret"
assert_fail "campo semelhante a segredo recusado" state_validate_file "$state_secret"

state_partial_file="$TMP_BASE/partial-file.env"
printf 'CONTABASE_STATE_VERSION=1\nCONTABASE_INSTALL_METHOD=source\n' > "$state_partial_file"
assert_fail "arquivo parcialmente escrito recusado" state_validate_file "$state_partial_file"

# ---------------------------------------------------------------------------
# 3. Escrita atomica e preservacao.
# ---------------------------------------------------------------------------
state_atomic="$TMP_BASE/atomic.env"
make_state "$state_atomic" "$repo_branch"
printf 'CONTABASE_CUSTOM_FIELD=atomic\n' >> "$state_atomic"
if state_write_atomic "$state_atomic" "source" "beta" "v1.0.0-beta.8" "$repo_branch" \
  "branch" "main" "$(git -C "$repo_branch" rev-parse HEAD)" \
  "https://github.com/contabase-app/contabase.git"; then
  ok "escrita atomica valida"
else
  bad "escrita atomica valida falhou"
fi
assert_contains "escrita preserva campo desconhecido" "CONTABASE_CUSTOM_FIELD=atomic" "$state_atomic"
assert_contains "escrita atualiza versao" "CONTABASE_INSTALLED_VERSION=v1.0.0-beta.8" "$state_atomic"

state_atomic_before="$(sha_file "$state_atomic")"
if state_write_atomic "$state_atomic" "source" "beta" "v1.0.0-beta.7" "$repo_branch" \
  "tag" "v1.0.0-beta.8" "$(git -C "$repo_branch" rev-parse HEAD)" \
  "https://github.com/contabase-app/contabase.git"; then
  bad "escrita invalida deveria falhar"
else
  ok "escrita invalida recusada"
fi
assert_eq "escrita invalida preserva arquivo anterior" "$state_atomic_before" "$(sha_file "$state_atomic")"
mode="$(stat -c '%a' "$state_atomic" 2>/dev/null || stat -f '%Lp' "$state_atomic" 2>/dev/null || true)"
assert_eq "estado escrito com modo 0644" "644" "$mode"

# ---------------------------------------------------------------------------
# 4. Prova estatica: instaladores nao contem logica de update/backup.
# ---------------------------------------------------------------------------
for script in scripts/install-contabase-source.sh scripts/install-contabase-docker.sh; do
  path="$ROOT_DIR/$script"
  if grep -Eq 'git[[:space:]]+(fetch|checkout|merge|reset)|create_canonical_backup' "$path"; then
    bad "$script contem logica proibida de update/backup"
  else
    ok "$script sem logica de update/backup"
  fi
  if grep -Fq 'provenance_detect' "$path"; then ok "$script registra proveniencia"; else bad "$script nao registra proveniencia"; fi
done

printf '\n=== F0 STATE PROVENANCE: %d passed, %d failed ===\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

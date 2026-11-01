#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

failures=0
assert_contains() {
  local file="$1"
  local text="$2"
  if ! grep -Fq -- "$text" "$file"; then
    echo "FAIL: $file does not contain: $text" >&2
    failures=$((failures + 1))
  fi
}

assert_absent() {
  local file="$1"
  local text="$2"
  if grep -Fq -- "$text" "$file"; then
    echo "FAIL: $file must not contain: $text" >&2
    failures=$((failures + 1))
  fi
}

assert_count() {
  local file="$1"
  local text="$2"
  local expected="$3"
  local actual
  actual="$(grep -Fc -- "$text" "$file" || true)"
  if [[ "$actual" != "$expected" ]]; then
    echo "FAIL: $file expected $expected occurrence(s), got $actual: $text" >&2
    failures=$((failures + 1))
  fi
}

preflight=".github/workflows/release-preflight.yml"
release=".github/workflows/release-artifacts.yml"
validation=".github/scripts/release-validation.sh"
busybox=".github/scripts/run-backup-restore-busybox.sh"
publisher="${CONTABASE_PUBLISHER_PATH:-}"

for script in "$validation" "$busybox"; do
  bash -n "$script"
done

assert_contains "$preflight" "Release Preflight"
assert_contains "$preflight" "- beta"
assert_contains "$preflight" "- main"
assert_contains "$preflight" "contents: read"
assert_contains "$preflight" "./.github/scripts/release-validation.sh"
assert_contains "$preflight" "./.github/scripts/run-backup-restore-busybox.sh"
assert_contains "$preflight" "stat_family: GNU"
assert_contains "$preflight" "stat_family: BSD"
assert_contains "$release" "./.github/scripts/release-validation.sh"
assert_contains "$release" "./.github/scripts/run-backup-restore-busybox.sh"
assert_contains "$release" "actions/runs?branch=beta"
assert_contains "$release" '.name == "Release Preflight"'
assert_contains "$release" "origin/beta no longer points to the tagged commit"
assert_contains "$release" "No successful Release Preflight exists"
assert_absent "$release" "ghcr.io/contabase-app/contabase:beta"
assert_contains "$validation" "TZ=UTC go test -timeout=20m ./... -count=1"
assert_absent "$validation" "-short"
assert_contains "$validation" "summarize-go-test-durations.sh"
assert_contains "$validation" "go vet ./..."
assert_contains "$validation" "go build ./..."
assert_contains "$validation" "GOARCH=\"\$arch\""
assert_contains "$validation" "checksums.txt"
assert_contains "$validation" '(^|/)\.[^/]+(/|$)'

if [[ -n "$publisher" ]]; then
  if [[ ! -f "$publisher" ]]; then
    echo "FAIL: private publisher not found: $publisher" >&2
    failures=$((failures + 1))
  else
    bash -n "$publisher"
    assert_contains "$publisher" "validate_export_private_references"
    assert_contains "$publisher" "prepare_readonly_candidate"
    assert_contains "$publisher" 'EXPORT_TREEISH="$(git write-tree)"'
    assert_contains "$publisher" 'git archive --worktree-attributes --format=tar "${EXPORT_TREEISH:-HEAD}"'
    assert_count "$publisher" "  prepare_readonly_candidate" "3"
    assert_contains "$publisher" 'ls-remote --heads origin "refs/heads/$PUBLIC_BRANCH"'
    assert_contains "$publisher" '--commit "$public_sha"'
    assert_contains "$publisher" '--status success'
    assert_contains "$publisher" 'require_release_preflight_for_sha "$public_sha"'
    assert_contains "$publisher" 'require_release_preflight_for_sha "$tag_sha"'
    assert_count "$publisher" '  require_release_preflight_for_sha "$' "2"
    assert_contains "$publisher" 'origin/$PUBLIC_BRANCH=$remote_sha diverge do SHA candidato=$public_sha'
  fi
fi

for forbidden in \
  "softprops/action-gh-release" \
  "docker/build-push-action" \
  "packages: write" \
  "gh release" \
  "git tag" \
  "git push" \
  "ghcr.io/"; do
  assert_absent "$preflight" "$forbidden"
done

if (( failures > 0 )); then
  echo "release preflight policy: $failures failure(s)" >&2
  exit 1
fi

echo "release preflight policy: PASS"

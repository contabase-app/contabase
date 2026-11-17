#!/usr/bin/env bash
set -euo pipefail

target_root="${1:-.}"

if [[ ! -d "$target_root" ]]; then
  echo "ERROR: public candidate directory not found: $target_root" >&2
  exit 2
fi

cd "$target_root"

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

inside_worktree="$(git rev-parse --is-inside-work-tree 2>/dev/null)" || {
  fail "cannot verify public candidate: git worktree is unavailable or invalid"
}
if [[ "$inside_worktree" != "true" ]]; then
  fail "cannot verify public candidate: git rev-parse --is-inside-work-tree returned '$inside_worktree'"
fi
if ! git rev-parse --verify -q HEAD >/dev/null 2>&1; then
  fail "cannot verify public candidate: HEAD is unavailable"
fi

forbidden_paths=(
  .docs
  AGENTS.md
  AGENTS.private.md
)

found=()

# Untracked or checked-out paths, including symlinks.
for path in "${forbidden_paths[@]}"; do
  if [[ -e "$path" || -L "$path" ]]; then
    found+=("$path")
  fi
done

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/contabase-public-paths.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT

index_paths="$tmp_dir/index-paths"
head_paths="$tmp_dir/head-paths"

# Capture NUL-delimited output so filenames with newlines remain intact and
# every Git read has an explicit exit status.
if ! git ls-files -z -- "${forbidden_paths[@]}" > "$index_paths"; then
  fail "cannot verify public candidate: git ls-files failed"
fi
if ! git ls-tree -r -z --name-only HEAD -- "${forbidden_paths[@]}" > "$head_paths"; then
  fail "cannot verify public candidate: git ls-tree HEAD failed"
fi

while IFS= read -r -d '' path; do
  [[ -n "$path" ]] && found+=("$path")
done < "$index_paths"

while IFS= read -r -d '' path; do
  [[ -n "$path" ]] && found+=("$path")
done < "$head_paths"

if [[ "${#found[@]}" -gt 0 ]]; then
  echo "ERROR: private documentation path found in public candidate:" >&2
  printf '  %s\n' "${found[@]}" | sort -u >&2
  exit 1
fi

echo "OK: no private documentation paths in public candidate."

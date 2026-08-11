#!/usr/bin/env bash
set -euo pipefail

# Classifies a tag-bound GitHub Release without mutating remote state.
# Usage: release-state.sh <tag> <workflow_commit> <assets_dir>

EXPECTED_ASSETS=(
  contabase-linux-amd64.tar.gz
  contabase-linux-arm64.tar.gz
  checksums.txt
)

tag="${1:-}"
workflow_commit_ref="${2:-}"
assets_dir="${3:-}"

emit_state() {
  local state="$1"
  local reason="$2"
  local uploads="${3:-}"
  local upload_json='[]'

  if [[ -n "$uploads" ]]; then
    upload_json="$(printf '%s\n' "$uploads" | jq -Rsc 'split("\n") | map(select(length > 0))')"
  fi

  jq -cn \
    --arg state "$state" \
    --arg reason "$reason" \
    --arg tag "$tag" \
    --arg commit "$workflow_commit_ref" \
    --argjson upload "$upload_json" \
    '{state: $state, reason: $reason, tag: $tag, commit_sha: $commit, upload: $upload}'
}

block() {
  emit_state "$1" "$2"
  exit 1
}

hash_file() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  else
    shasum -a 256 "$file" | awk '{print $1}'
  fi
}

is_expected_asset() {
  local candidate="$1"
  local expected
  for expected in "${EXPECTED_ASSETS[@]}"; do
    [[ "$candidate" == "$expected" ]] && return 0
  done
  return 1
}

if [[ -z "$tag" || -z "$workflow_commit_ref" || -z "$assets_dir" ]]; then
  block incompatible 'tag, workflow commit and assets directory are required'
fi

if [[ "${RELEASE_EVENT_NAME:-}" != 'push' || "${RELEASE_REF_TYPE:-}" != 'tag' ]]; then
  block incompatible 'release workflow must run from a tag push'
fi

if [[ "${RELEASE_REF:-}" != "refs/tags/$tag" ]]; then
  block incompatible 'workflow ref does not match requested tag'
fi

if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-beta\.(0|[1-9][0-9]*)$ ]]; then
  block incompatible 'invalid beta SemVer tag'
fi

version_file="${RELEASE_VERSION_FILE:-VERSION}"
if [[ ! -f "$version_file" ]]; then
  block incompatible 'VERSION file is missing'
fi
snapshot_version="$(tr -d '[:space:]' < "$version_file")"
if [[ "$snapshot_version" != "$tag" ]]; then
  block incompatible 'VERSION does not match tag'
fi

if ! tag_commit="$(git rev-parse --verify -q "refs/tags/$tag^{commit}")"; then
  block incompatible 'tag is not available in the checkout'
fi
if ! workflow_commit="$(git rev-parse --verify -q "$workflow_commit_ref^{commit}")"; then
  block incompatible 'workflow commit is not available in the checkout'
fi
if [[ "$tag_commit" != "$workflow_commit" ]]; then
  block incompatible 'tag commit does not match workflow commit'
fi

local_hashes=()
for asset_name in "${EXPECTED_ASSETS[@]}"; do
  asset_path="$assets_dir/$asset_name"
  if [[ ! -f "$asset_path" ]]; then
    block incompatible "expected local asset is missing: $asset_name"
  fi
  local_hashes+=("$(hash_file "$asset_path")")
done

gh_bin="${GH_BIN:-gh}"
if ! command -v "$gh_bin" >/dev/null 2>&1; then
  block api-error 'GitHub CLI is unavailable'
fi
if [[ -z "${GITHUB_REPOSITORY:-}" ]]; then
  block api-error 'GITHUB_REPOSITORY is unavailable'
fi

if ! api_output="$("$gh_bin" api --paginate "repos/$GITHUB_REPOSITORY/releases?per_page=100" 2>/dev/null)"; then
  block api-error 'GitHub release query failed'
fi
if [[ -z "$api_output" ]]; then
  block api-error 'GitHub release query returned empty output'
fi
if ! releases="$(printf '%s\n' "$api_output" | jq -e -c -s '
  if length == 0 then error("empty response")
  elif all(.[]; type == "array") then add
  else error("unexpected paginated response")
  end
')"; then
  block api-error 'GitHub release query returned invalid JSON'
fi
if ! printf '%s\n' "$releases" | jq -e '
  all(.[]; (type == "object") and (.tag_name | type == "string"))
' >/dev/null; then
  block unknown 'GitHub release query returned an unexpected release object'
fi

matches="$(printf '%s\n' "$releases" | jq -c --arg tag "$tag" '[.[] | select(.tag_name == $tag)]')"
match_count="$(printf '%s\n' "$matches" | jq -er 'length')"
if [[ "$match_count" == '0' ]]; then
  emit_state missing 'no release exists for the validated tag' "$(printf '%s\n' "${EXPECTED_ASSETS[@]}")"
  exit 0
fi
if [[ "$match_count" != '1' ]]; then
  block unknown 'multiple releases exist for the same tag'
fi

release="$(printf '%s\n' "$matches" | jq -c '.[0]')"
if ! printf '%s\n' "$release" | jq -e '
  (.draft | type == "boolean") and
  (.prerelease | type == "boolean") and
  (.assets | type == "array")
' >/dev/null; then
  block unknown 'release response is incomplete'
fi

if [[ "$(printf '%s\n' "$release" | jq -r '.draft')" != 'true' ]]; then
  block published 'release is no longer a draft'
fi
if [[ "$(printf '%s\n' "$release" | jq -r '.prerelease')" != 'true' ]]; then
  block incompatible 'draft is not a prerelease'
fi
if ! printf '%s\n' "$release" | jq -e '
  all(.assets[];
    (type == "object") and
    (.name | type == "string") and
    (.state | type == "string") and
    (.digest | type == "string")
  )
' >/dev/null; then
  block incompatible 'release asset metadata is incomplete'
fi

while IFS= read -r remote_name; do
  if ! is_expected_asset "$remote_name"; then
    block incompatible "unexpected remote asset: $remote_name"
  fi
done < <(printf '%s\n' "$release" | jq -r '.assets[].name')

uploads=''
for index in "${!EXPECTED_ASSETS[@]}"; do
  asset_name="${EXPECTED_ASSETS[$index]}"
  remote_count="$(printf '%s\n' "$release" | jq -er --arg name "$asset_name" '[.assets[] | select(.name == $name)] | length')"
  if [[ "$remote_count" == '0' ]]; then
    uploads+="$asset_name"$'\n'
    continue
  fi
  if [[ "$remote_count" != '1' ]]; then
    block incompatible "duplicate remote asset: $asset_name"
  fi

  remote_asset="$(printf '%s\n' "$release" | jq -c --arg name "$asset_name" '[.assets[] | select(.name == $name)][0]')"
  remote_state="$(printf '%s\n' "$remote_asset" | jq -r '.state')"
  remote_digest="$(printf '%s\n' "$remote_asset" | jq -r '.digest')"
  if [[ "$remote_state" != 'uploaded' ]]; then
    block incompatible "remote asset is not uploaded: $asset_name"
  fi
  if [[ ! "$remote_digest" =~ ^sha256:[[:xdigit:]]{64}$ ]]; then
    block incompatible "remote asset digest is invalid: $asset_name"
  fi
  local_digest="$(printf '%s' "${local_hashes[$index]}" | tr '[:upper:]' '[:lower:]')"
  remote_digest_value="$(printf '%s' "${remote_digest#sha256:}" | tr '[:upper:]' '[:lower:]')"
  if [[ "$remote_digest_value" != "$local_digest" ]]; then
    block incompatible "remote asset digest differs: $asset_name"
  fi
done

emit_state draft-compatible 'draft prerelease assets are compatible' "$uploads"

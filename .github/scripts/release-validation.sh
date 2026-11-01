#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

release_version="${1:-}"
if [[ -z "$release_version" ]]; then
  echo "usage: $0 <vX.Y.Z[-beta.N]>" >&2
  exit 2
fi
if [[ ! "$release_version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.(0|[1-9][0-9]*))?$ ]]; then
  echo "invalid public SemVer: $release_version" >&2
  exit 1
fi

snapshot_version="$(tr -d '[:space:]' < VERSION)"
if [[ "$snapshot_version" != "$release_version" ]]; then
  echo "VERSION mismatch: snapshot=$snapshot_version requested=$release_version" >&2
  exit 1
fi

expected_go_version="go$(awk '$1 == "go" { print $2; exit }' go.mod)"
if [[ ! "$expected_go_version" =~ ^go[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "invalid Go version in go.mod: $expected_go_version" >&2
  exit 1
fi

verify_artifact_toolchain() {
  local artifact_name="$1"
  local artifact_path="$2"
  local artifact_go_version
  artifact_go_version="$(go version "$artifact_path" | awk '{ print $NF }')"
  if [[ "$artifact_go_version" != "$expected_go_version" ]]; then
    echo "Go toolchain mismatch for $artifact_name: got=$artifact_go_version want=$expected_go_version" >&2
    exit 1
  fi
  printf '%s toolchain: %s\n' "$artifact_name" "$artifact_go_version"
}

dist_dir="${RELEASE_DIST_DIR:-$repo_root/release-dist}"
work_dir="${RELEASE_WORK_DIR:-$repo_root/release-work}"
case "$dist_dir" in
  "$repo_root/release-dist"|/tmp/*) ;;
  *) echo "unsafe RELEASE_DIST_DIR: $dist_dir" >&2; exit 1 ;;
esac
case "$work_dir" in
  "$repo_root/release-work"|/tmp/*) ;;
  *) echo "unsafe RELEASE_WORK_DIR: $work_dir" >&2; exit 1 ;;
esac

echo "== Install frontend dependencies =="
npm ci --include=optional

echo "== Build CSS =="
./scripts/build-css.sh

echo "== Go tests (UTC, uncached) =="
go_test_start="$SECONDS"
go_test_log="$(mktemp "${TMPDIR:-/tmp}/contabase-go-tests.XXXXXX")"
go_test_status=0
TZ=UTC go test -timeout=20m ./... -count=1 | tee "$go_test_log" || go_test_status=$?
./.github/scripts/summarize-go-test-durations.sh "$go_test_log"
echo "== Duracao total do passo de testes: $((SECONDS - go_test_start))s =="
rm -f "$go_test_log"
if [[ "$go_test_status" -ne 0 ]]; then
  exit "$go_test_status"
fi

echo "== Go vet =="
go vet ./...

echo "== Go vulnerability scan =="
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...

echo "== Native build =="
go build ./...

echo "== Build release packages =="
rm -rf "$dist_dir" "$work_dir"
mkdir -p "$dist_dir" "$work_dir"

for arch in amd64 arm64; do
  bundle_root="$work_dir/${arch}/contabase"
  mkdir -p "$bundle_root"

  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
    -trimpath \
    -ldflags="-s -w -X github.com/contabase-app/contabase/internal/version.Version=${release_version}" \
    -o "$bundle_root/contabase" \
    ./cmd/server

  verify_artifact_toolchain "contabase-linux-$arch" "$bundle_root/contabase"

  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
    -trimpath \
    -ldflags="-s -w" \
    -o "$bundle_root/admin" \
    ./cmd/admin

  verify_artifact_toolchain "admin-linux-$arch" "$bundle_root/admin"

  cp -R templates assets "$bundle_root/"
  cp LICENSE "$bundle_root/LICENSE"
  cp docs/binario/contabase.env.example "$bundle_root/contabase.env.example"
  cp docs/binario/contabase.service.example "$bundle_root/contabase.service.example"
  printf '%s\n' "$release_version" > "$bundle_root/VERSION"

  tar -czf "$dist_dir/contabase-linux-${arch}.tar.gz" \
    -C "$work_dir/${arch}" \
    contabase
done

echo "== Generate checksums =="
(
  cd "$dist_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum contabase-linux-amd64.tar.gz contabase-linux-arm64.tar.gz > checksums.txt
  else
    shasum -a 256 contabase-linux-amd64.tar.gz contabase-linux-arm64.tar.gz > checksums.txt
  fi
)

echo "== Validate archives and checksums =="
required_paths=(
  contabase
  contabase/contabase
  contabase/admin
  contabase/templates
  contabase/assets
  contabase/VERSION
  contabase/LICENSE
  contabase/contabase.env.example
  contabase/contabase.service.example
)

for arch in amd64 arm64; do
  archive="$dist_dir/contabase-linux-${arch}.tar.gz"
  listing="$dist_dir/contents-${arch}.txt"
  verbose_listing="$dist_dir/contents-${arch}-verbose.txt"

  tar -tzf "$archive" | sed 's#/$##' > "$listing"
  tar -tvzf "$archive" > "$verbose_listing"

  if awk 'substr($0,1,1) == "/" { bad=1 } END { exit bad ? 0 : 1 }' "$listing"; then
    echo "absolute path found in $archive" >&2
    exit 1
  fi
  if awk -F/ '{ for (i=1; i<=NF; i++) if ($i == ".." || $i == ".") bad=1 } END { exit bad ? 0 : 1 }' "$listing"; then
    echo "path traversal found in $archive" >&2
    exit 1
  fi
  if awk '$0 != "contabase" && index($0, "contabase/") != 1 { bad=1 } END { exit bad ? 0 : 1 }' "$listing"; then
    echo "path outside contabase/ found in $archive" >&2
    exit 1
  fi
  if awk 'substr($1,1,1) != "-" && substr($1,1,1) != "d" { bad=1 } END { exit bad ? 0 : 1 }' "$verbose_listing"; then
    echo "link or special file found in $archive" >&2
    exit 1
  fi

  for required in "${required_paths[@]}"; do
    grep -Fxq "$required" "$listing" || {
      echo "required path missing from $archive: $required" >&2
      exit 1
    }
  done

  if grep -E '(^|/)\.[^/]+(/|$)|(^|/)(data|uploads|backups|logs)(/|$)|(^|/)(PRODUCT|DESIGN)\.md$|\.db(-wal|-shm)?$' "$listing"; then
    echo "forbidden path found in $archive" >&2
    exit 1
  fi

  extracted_version="$(tar -xOzf "$archive" contabase/VERSION | tr -d '[:space:]')"
  if [[ "$extracted_version" != "$release_version" ]]; then
    echo "artifact VERSION mismatch in $archive" >&2
    exit 1
  fi
done

expected_names=$'contabase-linux-amd64.tar.gz\ncontabase-linux-arm64.tar.gz'
actual_names="$(awk '{print $2}' "$dist_dir/checksums.txt")"
if [[ "$actual_names" != "$expected_names" ]]; then
  echo "checksums.txt contains unexpected paths or ordering" >&2
  exit 1
fi

(
  cd "$dist_dir"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c checksums.txt
  else
    shasum -a 256 -c checksums.txt
  fi
)

rm -f "$dist_dir"/contents-*.txt
echo "release validation passed for $release_version"

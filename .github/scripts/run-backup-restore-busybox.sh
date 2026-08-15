#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="contabase-backup-restore-alpine:3.21"

docker build --tag "$image" - <<'DOCKERFILE'
FROM alpine:3.21
RUN apk add --no-cache bash sqlite
USER 65534:65534
WORKDIR /workspace
DOCKERFILE

docker run --rm \
  --volume "$repo_root:/workspace:ro" \
  "$image" \
  bash ./scripts/tests/test_backup_restore_package.sh

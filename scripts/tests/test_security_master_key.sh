#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/contabase-master-key.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

# Load only the functions under test; installer/update entrypoints never run.
load_functions() {
  local script="$1" name block
  shift
  for name in "$@"; do
    block="$(awk -v signature="${name}() {" '
      $0 == signature { active = 1 }
      active { print }
      active && $0 == "}" { exit }
    ' "$REPO_DIR/$script")"
    [ -n "$block" ] || { printf 'missing function: %s\n' "$name" >&2; exit 1; }
    eval "$block"
  done
}

assert_hex_key() {
  [[ "$1" =~ ^[0-9a-f]{32}$ ]] || { echo 'invalid generated master key' >&2; exit 1; }
}

(
  load_functions scripts/install-contabase-docker.sh say random_hex set_env_var env_load_value env_is_placeholder env_key_is_set ensure_secret
  fixture="$TEST_ROOT/docker.env"
  : > "$fixture"
  INTERACTIVE=false
  ensure_secret SECURITY_MASTER_KEY master-key "$fixture" true > "$TEST_ROOT/docker.log"
  first="$(env_load_value SECURITY_MASTER_KEY "$fixture")"
  assert_hex_key "$first"
  ensure_secret SECURITY_MASTER_KEY master-key "$fixture" false >> "$TEST_ROOT/docker.log"
  [ "$(env_load_value SECURITY_MASTER_KEY "$fixture")" = "$first" ]
  assert_hex_key "$(random_hex 16)"
  [ "$(random_hex 16)" != "$first" ]
  ! grep -Fq "$first" "$TEST_ROOT/docker.log"
  : > "$fixture"
  if ensure_secret SECURITY_MASTER_KEY master-key "$fixture" false >> "$TEST_ROOT/docker.log"; then
    echo 'existing installation must not silently generate a missing key' >&2
    exit 1
  fi
  [ ! -s "$fixture" ]
)
echo 'PASS: Docker installer generates a strong key and preserves existing values'

for script in scripts/install-contabase-source.sh; do
  (
    load_functions "$script" generate_master_key get_env_value normalize_env_value is_invalid_secret_value set_secret_value_if_missing_or_invalid
    ENV_FILE="$TEST_ROOT/source.env"
    first="$(generate_master_key)"
    assert_hex_key "$first"
    printf 'SECURITY_MASTER_KEY=%s\n' "$first" > "$ENV_FILE"
    set_secret_value_if_missing_or_invalid SECURITY_MASTER_KEY "$(generate_master_key)"
    [ "$(get_env_value SECURITY_MASTER_KEY)" = "$first" ]
    : > "$ENV_FILE"
    set_secret_value_if_missing_or_invalid SECURITY_MASTER_KEY "$(generate_master_key)"
    assert_hex_key "$(get_env_value SECURITY_MASTER_KEY)"
  )
done
echo 'PASS: source installer generates and preserves master keys'

(
  load_functions scripts/install-contabase-release.sh env_generate_hex_secret
  fail() { echo 'secret generation failed' >&2; exit 1; }
  first="$(env_generate_hex_secret 16)"
  assert_hex_key "$first"
  [ "$(env_generate_hex_secret 16)" != "$first" ]
)
echo 'PASS: release installer generates independent CSPRNG master keys'

for script in scripts/update-contabase-docker.sh scripts/update-contabase-release.sh; do
  (
    load_functions "$script" env_load_value env_is_placeholder env_key_is_set
    if [ "$script" = scripts/update-contabase-docker.sh ]; then
      load_functions "$script" validate_env_secrets
      validate() { validate_env_secrets "$ENV_FILE"; }
    else
      load_functions "$script" say host_from_base_url scheme_from_base_url host_is_localhost host_is_private_ipv4 infer_access_mode validate_access_contract_values validate_existing_env
      fail() { echo 'configuration validation failed' >&2; exit 1; }
      validate() { validate_existing_env; }
    fi
    ENV_FILE="$TEST_ROOT/update.env"
    DATA_DIR="$TEST_ROOT/data"
    DB_FILE=""
    UPLOADS_DIR=""
    printf 'AUTH_ENCRYPTION_KEY=synthetic-auth-key\nSECURITY_MASTER_KEY=0123456789abcdef0123456789abcdef\n' > "$ENV_FILE"
    cp "$ENV_FILE" "$TEST_ROOT/expected.env"
    validate > "$TEST_ROOT/update.log"
    cmp "$ENV_FILE" "$TEST_ROOT/expected.env"
    printf 'AUTH_ENCRYPTION_KEY=synthetic-auth-key\nSECURITY_MASTER_KEY=\n' > "$ENV_FILE"
    cp "$ENV_FILE" "$TEST_ROOT/expected.env"
    if validate >> "$TEST_ROOT/update.log"; then
      echo 'update must reject a missing persistent key' >&2
      exit 1
    fi
    cmp "$ENV_FILE" "$TEST_ROOT/expected.env"
  )
done
echo 'PASS: update validators preserve configured keys and refuse missing keys without generating replacements'

#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WRAPPER="$REPO_DIR/scripts/lib/contabase-update-wrapper.sh"
LEAF_RELEASE="$REPO_DIR/scripts/update-contabase-release.sh"
LEAF_SOURCE="$REPO_DIR/scripts/update-contabase-source.sh"
LEAF_DOCKER="$REPO_DIR/scripts/update-contabase-docker.sh"

PASS_COUNT=0
FAIL_COUNT=0

fail_test() { printf 'FAIL: %s\n' "$1" >&2; FAIL_COUNT=$((FAIL_COUNT + 1)); }
pass_test() { printf 'PASS: %s\n' "$1"; PASS_COUNT=$((PASS_COUNT + 1)); }

TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/contabase-version-policy-test.XXXXXX")"
trap 'rm -rf "$TEST_ROOT"' EXIT

# ------------------------------------------------------------------------------
# 1. Extract the productive VERSION_POLICY block from each of the 4 scripts
#    and confirm they are BYTE-IDENTICAL (no fifth oracle).
# ------------------------------------------------------------------------------
extract_block() {
  sed -n '/^# === VERSION_POLICY_BLOCK_BEGIN ===$/,/^# === VERSION_POLICY_BLOCK_END ===$/p' "$1"
}

echo "=== BLOCK: extract + equality ==="
BLOCK_WRAPPER="$(extract_block "$WRAPPER")"
BLOCK_RELEASE="$(extract_block "$LEAF_RELEASE")"
BLOCK_SOURCE="$(extract_block "$LEAF_SOURCE")"
BLOCK_DOCKER="$(extract_block "$LEAF_DOCKER")"

[ -n "$BLOCK_WRAPPER" ] && pass_test "Block: wrapper block extraido" || fail_test "Block: wrapper block vazio"

check_block_equal() {
  local name="$1" a="$2" b="$3"
  if [ "$a" = "$b" ]; then
    pass_test "Block: wrapper == $name (byte-identical)"
  else
    fail_test "Block: wrapper != $name (DRIFT detectado)"
  fi
}
check_block_equal "release" "$BLOCK_WRAPPER" "$BLOCK_RELEASE"
check_block_equal "source" "$BLOCK_WRAPPER" "$BLOCK_SOURCE"
check_block_equal "docker" "$BLOCK_WRAPPER" "$BLOCK_DOCKER"

# Load the productive code (from the wrapper block).
eval "$BLOCK_WRAPPER"

# version_decision lives OUTSIDE the block (per-file message helper). Load the
# wrapper's real one, providing the `say` helper it depends on.
say() { printf '%s\n' "$*"; }

extract_version_decision() {
  awk '/^version_decision\(\) \{/ { f=1 } f { print } f && /^\}$/ { exit }' "$1"
}

eval "$(extract_version_decision "$WRAPPER")"

# ------------------------------------------------------------------------------
# 2. PARSER matrix (canonicalize_version)
# ------------------------------------------------------------------------------
echo ""
echo "=== PARSER: canonicalize_version ==="

test_canon() {
  local input="$1" expected="$2" label="$3"
  local result rc=0
  result="$(canonicalize_version "$input" 2>/dev/null)" || rc=$?
  if [ "$rc" -ne 0 ] && [ "$expected" = "INVALID" ]; then
    pass_test "Parser: $label -> rejeitado"
  elif [ "$rc" -eq 0 ] && [ "$result" = "$expected" ]; then
    pass_test "Parser: $label -> $result"
  else
    fail_test "Parser: $label -> got rc=$rc result='$result' expected='$expected'"
  fi
}

test_canon "v0.1.0-beta.7"   "0 1 0 0 7"   "v0.1.0-beta.7"
test_canon "v0.1.0-beta.8"   "0 1 0 0 8"   "v0.1.0-beta.8"
test_canon "v0.1.0-beta.9"   "0 1 0 0 9"   "v0.1.0-beta.9"
test_canon "v0.1.0-beta.10"  "0 1 0 0 10"  "v0.1.0-beta.10"
test_canon "v0.1.0-beta.99"  "0 1 0 0 99"  "v0.1.0-beta.99"
test_canon "v0.1.0"          "0 1 0 1 0"   "v0.1.0 (stable)"
test_canon "v1.0.0"          "1 0 0 1 0"   "v1.0.0 (stable)"
test_canon "v0.0.1"          "0 0 1 1 0"   "v0.0.1"
test_canon "v10.20.30"       "10 20 30 1 0" "v10.20.30"
test_canon "v0.1.0-beta.0"   "0 1 0 0 0"   "v0.1.0-beta.0 (zero beta)"

test_canon ""              "INVALID" "vazio"
test_canon "0.1.0"         "INVALID" "sem v"
test_canon "v0.1"          "INVALID" "incompleta"
test_canon "v0.1.0.0"      "INVALID" "4 componentes"
test_canon "v0.1.0-beta.x" "INVALID" "beta nao numerico"
test_canon "v0.1.0-beta.01" "INVALID" "beta com leading zero"
test_canon "v0.1.0-alpha.1" "INVALID" "prerelease desconhecida"
test_canon "v0.1.0-rc.1"   "INVALID" "rc"
test_canon "v0.01.0"       "INVALID" "minor leading zero"
test_canon "latest"        "INVALID" "latest"
test_canon "beta"          "INVALID" "beta sozinho"
test_canon "v0.1.0-internal.1" "INVALID" "internal"
test_canon "v0.1.0-beta"   "INVALID" "beta sem numero"
test_canon "v0.1.0-"       "INVALID" "trailing dash"
test_canon $'v0.1.0\nbeta' "INVALID" "newline embutido"
test_canon $'v0.1.0\rbeta' "INVALID" "carriage return embutido"
test_canon "v0.1.0 "       "INVALID" "espaco trailing"

# ------------------------------------------------------------------------------
# 3. compare_decimal matrix
# ------------------------------------------------------------------------------
echo ""
echo "=== COMPARE_DECIMAL ==="

test_dec() {
  local left="$1" right="$2" expected="$3" label="$4"
  local result rc=0
  result="$(compare_decimal "$left" "$right" 2>/dev/null)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    fail_test "Decimal: $label -> erro rc=$rc"
  elif [ "$result" = "$expected" ]; then
    pass_test "Decimal: $label -> $result"
  else
    fail_test "Decimal: $label -> got '$result' expected '$expected'"
  fi
}

test_dec "0" "0" "0" "0 == 0"
test_dec "0" "1" "-1" "0 < 1"
test_dec "1" "0" "1" "1 > 0"
test_dec "9" "10" "-1" "9 < 10 (len differ)"
test_dec "10" "9" "1" "10 > 9 (len differ)"
test_dec "123" "123" "0" "123 == 123"
test_dec "123" "124" "-1" "123 < 124 (same len)"
test_dec "124" "123" "1" "124 > 123 (same len)"
test_dec "999" "1000" "-1" "999 < 1000"
test_dec "1000" "999" "1" "1000 > 999"
test_dec "9223372036854775807" "9223372036854775808" "-1" "max<int64> < max<int64>+1"
test_dec "9223372036854775808" "9223372036854775807" "1" "max<int64>+1 > max<int64>"
test_dec "99999999999999999999" "100000000000000000000" "-1" "20 digits < 21 digits"
test_dec "12345678901234567890123456789012345678901234567890123" "12345678901234567890123456789012345678901234567890124" "-1" "53 digits < 53 digits"
test_dec "12345678901234567890123456789012345678901234567890124" "12345678901234567890123456789012345678901234567890123" "1" "53 digits > 53 digits"

test_dec_invalid() {
  local left="$1" right="$2" label="$3"
  local rc=0
  compare_decimal "$left" "$right" >/dev/null 2>&1 || rc=$?
  if [ "$rc" -ne 0 ]; then
    pass_test "Decimal: $label -> rejeitado (rc=$rc)"
  else
    fail_test "Decimal: $label -> aceitou entrada invalida"
  fi
}

test_dec_invalid "" "1" "vazio"
test_dec_invalid "01" "1" "leading zero"
test_dec_invalid "1.5" "1" "decimal point"
test_dec_invalid "-1" "1" "negativo"
test_dec_invalid "abc" "1" "nao numerico"
test_dec_invalid "1a" "1" "sufixo alfa"

# ------------------------------------------------------------------------------
# 4. compare_canonical matrix
# ------------------------------------------------------------------------------
echo ""
echo "=== COMPARE_CANONICAL ==="

test_cmp() {
  local a="$1" b="$2" expected="$3" label="$4"
  local result rc=0
  result="$(compare_canonical $a $b 2>/dev/null)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    fail_test "Compare: $label -> erro rc=$rc"
  elif [ "$result" = "$expected" ]; then
    pass_test "Compare: $label -> $result"
  else
    fail_test "Compare: $label -> got '$result' expected '$expected'"
  fi
}

test_cmp "0 1 0 0 7" "0 1 0 0 8" "-1" "beta.7 < beta.8"
test_cmp "0 1 0 0 8" "0 1 0 0 7" "1"  "beta.8 > beta.7"
test_cmp "0 1 0 0 9" "0 1 0 0 10" "-1" "beta.9 < beta.10"
test_cmp "0 1 0 0 10" "0 1 0 0 9" "1"  "beta.10 > beta.9"
test_cmp "0 1 0 0 7" "0 1 0 1 0" "-1" "beta.7 < v0.1.0"
test_cmp "0 1 0 1 0" "0 1 0 0 99" "1"  "stable > beta.99"
test_cmp "0 1 0 0 7" "0 1 0 0 7" "0"  "beta.7 == beta.7"
test_cmp "0 1 0 1 0" "0 1 0 1 0" "0"  "stable == stable"
test_cmp "0 1 0 0 7" "1 0 0 0 1" "-1" "v0.1.0-beta.7 < v1.0.0-beta.1"
test_cmp "0 1 0 0 7" "0 2 0 0 7" "-1" "minor bump"
test_cmp "0 1 0 0 7" "0 1 1 0 7" "-1" "patch bump"
test_cmp "0 1 0 0 0" "0 1 0 0 1" "-1" "beta.0 < beta.1"

# ------------------------------------------------------------------------------
# 5. DECISION matrix (small numbers)
# ------------------------------------------------------------------------------
echo ""
echo "=== DECISION: version_decision ==="

test_decision() {
  local current="$1" target="$2" expected_rc="$3" label="$4"
  local rc=0
  version_decision "$current" "$target" >/dev/null 2>&1 || rc=$?
  if [ "$rc" = "$expected_rc" ]; then
    pass_test "Decision: $label -> rc=$rc"
  else
    fail_test "Decision: $label -> got rc=$rc expected=$expected_rc"
  fi
}

test_decision "v0.1.0-beta.7" "v0.1.0-beta.9" 0 "beta.7 -> beta.9 (UPDATE)"
test_decision "v0.1.0-beta.8" "v0.1.0-beta.9" 0 "beta.8 -> beta.9 (UPDATE)"
test_decision "v0.1.0-beta.7" "v0.1.0-beta.8" 0 "beta.7 -> beta.8 (UPDATE)"
test_decision "v0.1.0-beta.9" "v0.1.0-beta.10" 0 "beta.9 -> beta.10 (UPDATE)"
test_decision "v0.1.0-beta.7" "v0.1.0" 0 "beta.7 -> stable (UPDATE)"
test_decision "v0.1.0-beta.9" "v0.1.0" 0 "beta.9 -> stable (UPDATE)"
test_decision "v0.1.0" "v1.0.0" 0 "stable -> major bump (UPDATE)"

test_decision "v0.1.0-beta.7" "v0.1.0-beta.7" 2 "beta.7 -> beta.7 (ALREADY_CURRENT)"
test_decision "v0.1.0-beta.9" "v0.1.0-beta.9" 2 "beta.9 -> beta.9 (ALREADY_CURRENT)"
test_decision "v0.1.0" "v0.1.0" 2 "stable -> stable (ALREADY_CURRENT)"

test_decision "v0.1.0-beta.8" "v0.1.0-beta.7" 1 "beta.8 -> beta.7 (DOWNGRADE)"
test_decision "v0.1.0-beta.9" "v0.1.0-beta.8" 1 "beta.9 -> beta.8 (DOWNGRADE)"
test_decision "v0.1.0-beta.9" "v0.1.0-beta.7" 1 "beta.9 -> beta.7 (DOWNGRADE)"
test_decision "v0.1.0-beta.10" "v0.1.0-beta.9" 1 "beta.10 -> beta.9 (DOWNGRADE)"
test_decision "v0.1.0" "v0.1.0-beta.9" 1 "stable -> beta.9 (DOWNGRADE)"
test_decision "v0.1.0" "v0.1.0-beta.7" 1 "stable -> beta.7 (DOWNGRADE)"
test_decision "v0.1.0-beta.7" "v0.0.9-beta.1" 1 "major downgrade (DOWNGRADE)"

test_decision "" "v0.1.0-beta.9" 3 "current vazio"
test_decision "v0.1.0-beta.7" "" 3 "target vazio"
test_decision "invalid" "v0.1.0-beta.7" 3 "current invalido"
test_decision "v0.1.0-beta.7" "invalid" 3 "target invalido"
test_decision "0.1.0" "v0.1.0-beta.7" 3 "current sem v"
test_decision "v0.1.0-beta.7" "v0.1.0-beta.01" 3 "target beta.01"
test_decision "v0.1.0-beta.7" "v0.1.0-alpha.1" 3 "target alpha"
test_decision "v0.1.0-beta.x" "v0.1.0-beta.7" 3 "current beta.x"

# ------------------------------------------------------------------------------
# 6. BIG NUMBERS matrix (arbitrarily large decimals, no arithmetic overflow)
# ------------------------------------------------------------------------------
echo ""
echo "=== BIG NUMBERS: version_decision ==="

H1="9223372036854775807"   # 2^63 - 1
H2="9223372036854775808"   # 2^63
H3="99999999999999999999"  # 20 digits
H4="12345678901234567890123456789012345678901234567890123"  # 53 digits

test_decision_big() {
  local cur="$1" tgt="$2" expected_rc="$3" label="$4"
  local rc=0 err=""
  version_decision "$cur" "$tgt" >/dev/null 2>"$TEST_ROOT/big.err" || rc=$?
  err="$(cat "$TEST_ROOT/big.err" 2>/dev/null || true)"
  if [ "$rc" != "$expected_rc" ]; then
    fail_test "BigDecision: $label -> rc=$rc expected=$expected_rc msg='$err'"
    return
  fi
  if printf '%s' "$err" | grep -Eq 'integer expression expected|arithmetic syntax error|syntax error in expression'; then
    fail_test "BigDecision: $label -> erro aritmetico no stderr: $err"
    return
  fi
  pass_test "BigDecision: $label -> rc=$rc"
}

# A. enorme > pequeno
test_decision_big "v0.1.0" "v${H2}.0.0" 0 "A: pequeno -> 2^63 (UPDATE)"
# B. pequeno < enorme (reverse)
test_decision_big "v${H2}.0.0" "v0.1.0" 1 "B: 2^63 -> pequeno (DOWNGRADE)"
# C. enorme == mesmo enorme
test_decision_big "v${H2}.0.0" "v${H2}.0.0" 2 "C: 2^63 == 2^63 (ALREADY_CURRENT)"
# D. dois enormes de mesmo comprimento, valores diferentes
test_decision_big "v${H2}.0.0" "v9223372036854775809.0.0" 0 "D1: 2^63 -> 2^63+1 (UPDATE)"
test_decision_big "v9223372036854775809.0.0" "v${H2}.0.0" 1 "D2: 2^63+1 -> 2^63 (DOWNGRADE)"
# E. enormes de comprimentos diferentes
test_decision_big "v${H3}.0.0" "v${H2}.0.0" 1 "E1: 20-digit -> 19-digit (DOWNGRADE)"
test_decision_big "v${H2}.0.0" "v${H3}.0.0" 0 "E2: 19-digit -> 20-digit (UPDATE)"
# F. major enorme
test_decision_big "v${H2}.1.1" "v0.99999999999999999999.99999999999999999999" 1 "F: major enorme > 0 (DOWNGRADE)"
# G. minor enorme
test_decision_big "v0.1.0" "v0.${H2}.0" 0 "G: minor 1 -> 2^63 (UPDATE)"
test_decision_big "v0.${H2}.0" "v0.1.0" 1 "G2: minor 2^63 -> 1 (DOWNGRADE)"
# H. patch enorme
test_decision_big "v0.1.0" "v0.1.${H2}" 0 "H: patch 0 -> 2^63 (UPDATE)"
test_decision_big "v0.1.${H2}" "v0.1.0" 1 "H2: patch 2^63 -> 0 (DOWNGRADE)"
# I. beta_number enorme
test_decision_big "v0.1.0-beta.${H2}" "v0.1.0-beta.${H1}" 1 "I: beta 2^63 -> 2^63-1 (DOWNGRADE)"
test_decision_big "v0.1.0-beta.${H1}" "v0.1.0-beta.${H2}" 0 "I2: beta 2^63-1 -> 2^63 (UPDATE)"
# J. stable vs beta com beta_number enorme
test_decision_big "v0.1.0" "v0.1.0-beta.${H3}" 1 "J: stable -> beta enorme (DOWNGRADE)"
test_decision_big "v0.1.0-beta.${H3}" "v0.1.0" 0 "J2: beta enorme -> stable (UPDATE)"
# 50+ digitos
test_decision_big "v${H4}.0.0" "v0.1.0" 1 "53-digit major -> small (DOWNGRADE)"
test_decision_big "v0.1.0" "v${H4}.0.0" 0 "small -> 53-digit major (UPDATE)"
test_decision_big "v${H4}.0.0" "v${H4}.0.0" 2 "53-digit == 53-digit (ALREADY_CURRENT)"

# ------------------------------------------------------------------------------
# 7. INTEGRATION: wrapper main() with mocked state file
# ------------------------------------------------------------------------------
echo ""
echo "=== INTEGRATION: wrapper main() ==="

run_wrapper_for() {
  local saved_version="$1" target_version="$2" label="$3" expected_rc="$4"
  local state_file="$TEST_ROOT/state-${5}.env"
  printf 'CONTABASE_INSTALL_METHOD=release\n' > "$state_file"
  printf 'CONTABASE_CHANNEL=beta\n' >> "$state_file"
  printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$saved_version" >> "$state_file"
  printf 'CONTABASE_REPO_PATH=/opt/stacks/contabase\n' >> "$state_file"

  local rc=0
  CONTABASE_STATE_FILE="$state_file" \
  CONTABASE_UPDATE_RESOLVE_ONLY=1 \
    "$WRAPPER" "$target_version" >/dev/null 2>&1 || rc=$?

  if [ "$rc" = "$expected_rc" ]; then
    pass_test "Wrapper: $label -> rc=$rc"
  else
    fail_test "Wrapper: $label -> got rc=$rc expected=$expected_rc"
  fi
}

run_wrapper_for "v0.1.0-beta.7" "v0.1.0-beta.9"  "beta.7 -> beta.9 (UPDATE)" 0 w1
run_wrapper_for "v0.1.0-beta.7" "v0.1.0-beta.7"  "beta.7 -> beta.7 (ALREADY_CURRENT)" 0 w2
run_wrapper_for "v0.1.0-beta.8" "v0.1.0-beta.7"  "beta.8 -> beta.7 (DOWNGRADE)" 1 w3
run_wrapper_for "v0.1.0-beta.9" "v0.1.0-beta.9"  "beta.9 -> beta.9 (ALREADY_CURRENT)" 0 w4
run_wrapper_for "v0.1.0-beta.9" "v0.1.0-beta.8"  "beta.9 -> beta.8 (DOWNGRADE)" 1 w5
run_wrapper_for "v0.1.0-beta.9" "v0.1.0-beta.7"  "beta.9 -> beta.7 (DOWNGRADE)" 1 w6
run_wrapper_for "v0.1.0-beta.9" "v0.1.0-beta.10" "beta.9 -> beta.10 (UPDATE)" 0 w7
run_wrapper_for "v0.1.0-beta.9" "v${H3}.0.0" "beta.9 -> huge major (UPDATE)" 0 w8
run_wrapper_for "v${H3}.0.0" "v0.1.0-beta.9" "huge major -> beta.9 (DOWNGRADE)" 1 w9

echo ""
echo "=== NEGATIVE PROOF: ALREADY_CURRENT / DOWNGRADE no mutam filesystem ==="

test_no_fs_mutation() {
  local saved_version="$1" target_version="$2" label="$3" expected_rc="$4" n="$5"
  local state_file="$TEST_ROOT/fs-state-${n}.env"
  printf 'CONTABASE_INSTALL_METHOD=release\n' > "$state_file"
  printf 'CONTABASE_CHANNEL=beta\n' >> "$state_file"
  printf 'CONTABASE_INSTALLED_VERSION=%s\n' "$saved_version" >> "$state_file"
  printf 'CONTABASE_REPO_PATH=/opt/stacks/contabase\n' >> "$state_file"

  local state_before
  state_before="$(sha256sum "$state_file" | cut -d' ' -f1)"

  local rc=0
  CONTABASE_STATE_FILE="$state_file" \
    "$WRAPPER" "$target_version" >/dev/null 2>&1 || rc=$?

  local state_after
  state_after="$(sha256sum "$state_file" | cut -d' ' -f1)"

  if [ "$rc" = "$expected_rc" ]; then
    if [ "$state_before" = "$state_after" ]; then
      pass_test "Negative: $label -> rc=$rc state unchanged"
    else
      fail_test "Negative: $label -> state file foi modificado"
    fi
  else
    fail_test "Negative: $label -> got rc=$rc expected=$expected_rc"
  fi
}

test_no_fs_mutation "v0.1.0-beta.7" "v0.1.0-beta.7"  "ALREADY_CURRENT no muta estado" 0 n1
test_no_fs_mutation "v0.1.0-beta.8" "v0.1.0-beta.7"  "DOWNGRADE no muta estado" 1 n2
test_no_fs_mutation "v0.1.0-beta.9" "v0.1.0-beta.9"  "ALREADY_CURRENT beta.9 no muta estado" 0 n3
test_no_fs_mutation "invalid-version" "v0.1.0-beta.7" "AMBIGUOUS no muta estado" 1 n4

# ------------------------------------------------------------------------------
# 8. STRUCTURAL: leaves embed the policy functions + markers + call gate
# ------------------------------------------------------------------------------
echo ""
echo "=== STRUCTURAL ==="

check_embedded() {
  local leaf="$1" func="$2"
  if grep -q "^${func}()" "$leaf"; then
    pass_test "Structural: $(basename "$leaf") embeds ${func}()"
  else
    fail_test "Structural: $(basename "$leaf") MISSING ${func}()"
  fi
}

for leaf in "$LEAF_RELEASE" "$LEAF_SOURCE" "$LEAF_DOCKER"; do
  check_embedded "$leaf" "canonicalize_version"
  check_embedded "$leaf" "compare_decimal"
  check_embedded "$leaf" "compare_canonical"
  check_embedded "$leaf" "version_decision"
  check_embedded "$leaf" "resolve_current_version"
  check_embedded "$leaf" "enforce_version_policy"
  if grep -q "enforce_version_policy" "$leaf"; then
    pass_test "Structural: $(basename "$leaf") calls enforce_version_policy"
  else
    fail_test "Structural: $(basename "$leaf") does NOT call enforce_version_policy"
  fi
  if grep -q "VERSION_POLICY_BLOCK_BEGIN" "$leaf" && grep -q "VERSION_POLICY_BLOCK_END" "$leaf"; then
    pass_test "Structural: $(basename "$leaf") tem marcadores de bloco"
  else
    fail_test "Structural: $(basename "$leaf") sem marcadores de bloco"
  fi
done

# ------------------------------------------------------------------------------
# 9. RELEASE LEAF DIRECT (sandbox com stubs)
# ------------------------------------------------------------------------------
echo ""
echo "=== RELEASE LEAF DIRECT ==="

run_release_leaf() {
  local current="$1" target="$2" expected_rc="$3" label="$4"
  local safe_label
  safe_label="$(printf '%s' "$label" | tr -c 'A-Za-z0-9' '_')"
  local sandbox="$TEST_ROOT/release-$safe_label"
  mkdir -p "$sandbox/bin" "$sandbox/install" "$sandbox/config" "$sandbox/data"

  cat > "$sandbox/bin/id" <<'EOF'
#!/bin/sh
echo 0
EOF
  cat > "$sandbox/bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
  -s) echo Linux ;;
  -m) echo x86_64 ;;
  *) echo Linux ;;
esac
EOF
  cat > "$sandbox/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG:-/dev/null}"
exit 0
EOF
  chmod +x "$sandbox/bin/id" "$sandbox/bin/uname" "$sandbox/bin/systemctl"

  touch "$sandbox/install/contabase"
  chmod 0755 "$sandbox/install/contabase"
  printf '%s\n' "$current" > "$sandbox/install/VERSION"

  cat > "$sandbox/config/contabase.env" <<'EOF'
AUTH_ENCRYPTION_KEY=test-auth-key-not-real
SECURITY_MASTER_KEY=test-security-key-not-real
EOF

  local rc=0
  PATH="$sandbox/bin:$PATH" \
  SYSTEMCTL_LOG="$sandbox/systemctl.log" \
  CONTABASE_VERSION="$target" \
  CONTABASE_ASSUME_YES=1 \
  CONTABASE_INSTALL_DIR="$sandbox/install" \
  CONTABASE_CONFIG_DIR="$sandbox/config" \
  CONTABASE_DATA_DIR="$sandbox/data" \
    "$LEAF_RELEASE" >/dev/null 2>&1 || rc=$?

  if [ "$rc" != "$expected_rc" ]; then
    fail_test "Release leaf: $label -> got rc=$rc expected=$expected_rc"
    return
  fi

  local systemctl_calls=0
  [ -f "$sandbox/systemctl.log" ] && systemctl_calls="$(wc -l < "$sandbox/systemctl.log" | tr -d ' ')"
  local backup_files
  backup_files="$(find "$sandbox" -name '*.pre-update*' -o -name '*.staging.*' -o -name '*.previous.*' 2>/dev/null | wc -l | tr -d ' ')"

  if [ "$systemctl_calls" -ne 0 ]; then
    fail_test "Release leaf: $label -> systemctl chamado ($systemctl_calls vezes); esperado 0"
  elif [ "$backup_files" -ne 0 ]; then
    fail_test "Release leaf: $label -> arquivos de backup/staging/swap presentes; esperado 0"
  else
    pass_test "Release leaf: $label -> rc=$rc, sem stop/backup/bundle swap"
  fi
}

run_release_leaf "v0.1.0-beta.9" "v0.1.0-beta.9" 0 "beta.9 -> beta.9 (NO-OP)"
run_release_leaf "v0.1.0-beta.9" "v0.1.0-beta.8" 1 "beta.9 -> beta.8 (DOWNGRADE)"

run_release_leaf_divergent() {
  local sandbox="$TEST_ROOT/release-divergent"
  mkdir -p "$sandbox/bin" "$sandbox/install" "$sandbox/config" "$sandbox/data"
  cat > "$sandbox/bin/id" <<'EOF'
#!/bin/sh
echo 0
EOF
  cat > "$sandbox/bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac
EOF
  cat > "$sandbox/bin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "${SYSTEMCTL_LOG:-/dev/null}"
exit 0
EOF
  chmod +x "$sandbox/bin/id" "$sandbox/bin/uname" "$sandbox/bin/systemctl"
  touch "$sandbox/install/contabase"
  chmod 0755 "$sandbox/install/contabase"
  printf 'v0.1.0-beta.8\n' > "$sandbox/install/VERSION"
  cat > "$sandbox/config/contabase.env" <<'EOF'
AUTH_ENCRYPTION_KEY=test-auth-key-not-real
SECURITY_MASTER_KEY=test-security-key-not-real
EOF
  cat > "$sandbox/config/install-state.env" <<'EOF'
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=release
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v0.1.0-beta.9
CONTABASE_REPO_PATH=
EOF

  local rc=0
  PATH="$sandbox/bin:$PATH" \
  SYSTEMCTL_LOG="$sandbox/systemctl.log" \
  CONTABASE_VERSION="v0.1.0-beta.9" \
  CONTABASE_ASSUME_YES=1 \
  CONTABASE_INSTALL_DIR="$sandbox/install" \
  CONTABASE_CONFIG_DIR="$sandbox/config" \
  CONTABASE_DATA_DIR="$sandbox/data" \
    "$LEAF_RELEASE" >/dev/null 2>&1 || rc=$?

  if [ "$rc" -ne 0 ]; then
    pass_test "Release leaf: state divergente -> rc=$rc (FAIL-CLOSED)"
  else
    fail_test "Release leaf: state divergente -> rc=0, deveria falhar fechado"
  fi
}
run_release_leaf_divergent

# ------------------------------------------------------------------------------
# 10. SOURCE LEAF DIRECT — negative + positive proofs
# ------------------------------------------------------------------------------
echo ""
echo "=== SOURCE LEAF DIRECT ==="

setup_source_sandbox() {
  local dir="$1"
  mkdir -p "$dir/bin" "$dir/scripts/ops" "$dir/.git" "$dir/tmp"
  cat > "$dir/go.mod" <<'EOF'
module github.com/contabase-app/contabase
go 1.23
EOF
  cat > "$dir/scripts/build-css.sh" <<'EOF'
#!/bin/sh
exit 0
EOF
  chmod +x "$dir/scripts/build-css.sh"
  cp "$LEAF_SOURCE" "$dir/scripts/update-contabase-source.sh"
  mkdir -p "$dir/scripts/lib"
  cp "$REPO_DIR/scripts/lib/contabase-recovery.sh" "$dir/scripts/lib/contabase-recovery.sh"

  cat > "$dir/bin/id" <<'EOF'
#!/bin/sh
echo 0
EOF
  cat > "$dir/bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac
EOF
  cat > "$dir/bin/systemctl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$dir/systemctl.log"
exit 0
EOF
  chmod +x "$dir/bin/id" "$dir/bin/uname" "$dir/bin/systemctl"

  cat > "$dir/bin/git" <<'STUB'
#!/bin/sh
GIT_LOG="${GIT_LOG:-/dev/null}"
log() { printf '%s\n' "$*" >> "$GIT_LOG"; }
cmd="$1"
shift
case "$cmd" in
  branch) [ "${1:-}" = "--show-current" ] && echo "${GIT_BRANCH:-main}" ;;
  log) echo "abc123 test commit" ;;
  fetch) log "fetch $*"; exit 0 ;;
  rev-parse)
    case "$*" in
      *"@{upstream}"*) exit 1 ;;
      *":scripts/ops/backup.sh") log "rev-parse backup.sh"; echo "${BLOB_ID:-deadbeef}" ;;
      *) echo "HEAD" ;;
    esac ;;
  show-ref) exit 0 ;;
  merge-base) exit 0 ;;
  show)
    case "$*" in
      *":scripts/ops/backup.sh") log "show backup.sh"; printf '#!/bin/sh\necho backup-helper\n' ;;
      *":VERSION") echo "${GIT_TARGET_VER}" ;;
      *) echo "" ;;
    esac ;;
  hash-object) echo "${BLOB_ID:-deadbeef}" ;;
  status) echo "" ;;
  pull) log "pull $*"; echo "pulled" ;;
  *) exit 0 ;;
esac
exit 0
STUB
  chmod +x "$dir/bin/git"
}

run_source_leaf() {
  local dir="$1" current="$2" target="$3" state_ver="$4" out="$5"
  printf '%s\n' "$current" > "$dir/VERSION"
  if [ -n "$state_ver" ]; then
    cat > "$dir/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=source
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=$state_ver
CONTABASE_REPO_PATH=$dir
EOF
  fi

  local rc=0
  PATH="$dir/bin:$PATH" \
  TMPDIR="$dir/tmp" \
  GIT_LOG="$dir/git.log" \
  GIT_TARGET_VER="$target" \
  GIT_BRANCH="main" \
  BLOB_ID="deadbeef" \
  CONTABASE_STATE_FILE="$dir/install-state.env" \
  DATA_DIR="$dir/data" \
    bash "$dir/scripts/update-contabase-source.sh" >"$out" 2>&1 || rc=$?
  echo "$rc"
}

source_no_mutation() {
  local current="$1" target="$2" state_ver="$3" expected_rc="$4" label="$5"
  local dir="$TEST_ROOT/src-$(echo "$label" | tr -c 'A-Za-z0-9' '_')"
  setup_source_sandbox "$dir"

  local rc
  rc="$(run_source_leaf "$dir" "$current" "$target" "$state_ver" "$dir/out.log")"

  local backup_show=0 helper_files=0 stop_calls=0 pull_calls=0
  [ -f "$dir/git.log" ] && backup_show="$(grep -c 'backup.sh' "$dir/git.log" || true)"
  helper_files="$(find "$dir/tmp" -name 'contabase-backup-helper.*' 2>/dev/null | wc -l | tr -d ' ')"
  [ -f "$dir/systemctl.log" ] && stop_calls="$(wc -l < "$dir/systemctl.log" | tr -d ' ')"
  [ -f "$dir/git.log" ] && pull_calls="$(grep -c '^pull' "$dir/git.log" || true)"

  local ok=1
  [ "$rc" = "$expected_rc" ] || { fail_test "Source: $label -> rc=$rc expected=$expected_rc"; ok=0; }
  [ "$backup_show" = "0" ] || { fail_test "Source: $label -> git show backup.sh chamado ($backup_show)"; ok=0; }
  [ "$helper_files" = "0" ] || { fail_test "Source: $label -> helper materializado ($helper_files)"; ok=0; }
  [ "$stop_calls" = "0" ] || { fail_test "Source: $label -> systemctl stop chamado ($stop_calls)"; ok=0; }
  [ "$pull_calls" = "0" ] || { fail_test "Source: $label -> git pull chamado ($pull_calls)"; ok=0; }
  [ "$ok" = "1" ] && pass_test "Source: $label -> rc=$rc sem mutacao (helper/stop/pull)"
}

# negative proofs
source_no_mutation "v0.1.0-beta.9" "v0.1.0-beta.9" "v0.1.0-beta.9" 0 "ALREADY_CURRENT"
source_no_mutation "v0.1.0-beta.9" "v0.1.0-beta.8" "v0.1.0-beta.9" 1 "DOWNGRADE_REJECTED"
source_no_mutation "not-a-version" "v0.1.0-beta.9" "" 1 "VERSION_AMBIGUOUS"

source_positive_update() {
  local dir="$TEST_ROOT/src-positive"
  setup_source_sandbox "$dir"

  # UPDATE: current beta.8 -> target beta.9
  local rc
  rc="$(run_source_leaf "$dir" "v0.1.0-beta.8" "v0.1.0-beta.9" "v0.1.0-beta.8" "$dir/out.log")"

  # The helper materialization must have happened (after the gate).
  local backup_show=0
  [ -f "$dir/git.log" ] && backup_show="$(grep -c 'backup.sh' "$dir/git.log" || true)"
  if [ "$backup_show" -ge 1 ]; then
    pass_test "Source: UPDATE beta.8 -> beta.9 -> helper materializado apos o gate (rc=$rc)"
  else
    fail_test "Source: UPDATE -> helper NAO materializado (backup_show=$backup_show, rc=$rc)"
  fi
}
source_positive_update

# ------------------------------------------------------------------------------
# 11. DOCKER LEAF DIRECT — negative + positive proofs
# ------------------------------------------------------------------------------
echo ""
echo "=== DOCKER LEAF DIRECT ==="

setup_docker_sandbox() {
  local dir="$1"
  mkdir -p "$dir/bin" "$dir/.git" "$dir/tmp"
  cat > "$dir/.env.docker" <<'EOF'
AUTH_ENCRYPTION_KEY=test-auth-key-not-real
SECURITY_MASTER_KEY=test-security-key-not-real
EOF

  cat > "$dir/bin/docker" <<'STUB'
#!/bin/sh
DOCKER_LOG="${DOCKER_LOG:-/dev/null}"
log() { printf '%s\n' "$*" >> "$DOCKER_LOG"; }
if [ "$1" = "compose" ]; then
  shift
  case "$1" in
    config) exit 0 ;;
    ps) exit 0 ;;
    down) log "compose down"; exit "${DOCKER_DOWN_RC:-0}" ;;
    up) log "compose up $*"; exit 0 ;;
    port) echo "8080" ;;
    logs) exit 0 ;;
    *) exit 0 ;;
  esac
fi
case "$1" in
  inspect) echo "" ;;
  image) exit 0 ;;
  *) exit 0 ;;
esac
exit 0
STUB
  chmod +x "$dir/bin/docker"

  cat > "$dir/bin/git" <<'STUB'
#!/bin/sh
GIT_LOG="${GIT_LOG:-/dev/null}"
log() { printf '%s\n' "$*" >> "$GIT_LOG"; }
cmd="$1"
shift
case "$cmd" in
  branch) [ "${1:-}" = "--show-current" ] && echo "${GIT_BRANCH:-main}" ;;
  log) echo "abc123 test commit" ;;
  fetch) log "fetch $*"; exit 0 ;;
  rev-list) echo "0" ;;
  rev-parse)
    case "$*" in
      *":scripts/ops/backup.sh") log "rev-parse backup.sh"; echo "${BLOB_ID:-deadbeef}" ;;
      *) echo "HEAD" ;;
    esac ;;
  show)
    case "$*" in
      *":scripts/ops/backup.sh") log "show backup.sh"; printf '#!/bin/sh\necho backup-helper\n' ;;
      *":VERSION") echo "${GIT_TARGET_VER}" ;;
      *) echo "" ;;
    esac ;;
  hash-object) echo "${BLOB_ID:-deadbeef}" ;;
  pull) log "pull $*"; echo "pulled" ;;
  reset) exit 0 ;;
  clean) exit 0 ;;
  *) exit 0 ;;
esac
exit 0
STUB
  chmod +x "$dir/bin/git"

  cat > "$dir/bin/curl" <<'EOF'
#!/bin/sh
echo '{"status":"healthy"}'
exit 0
EOF
  chmod +x "$dir/bin/curl"
}

run_docker_leaf() {
  local dir="$1" current="$2" target="$3" state_ver="$4" explicit_ver="$5" out="$6"
  printf '%s\n' "$current" > "$dir/VERSION"
  if [ -n "$state_ver" ]; then
    cat > "$dir/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=docker
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=$state_ver
CONTABASE_REPO_PATH=$dir
EOF
  fi

  local rc=0
  (
    cd "$dir"
    PATH="$dir/bin:$PATH" \
    TMPDIR="$dir/tmp" \
    DOCKER_LOG="$dir/docker.log" \
    GIT_LOG="$dir/git.log" \
    GIT_TARGET_VER="$target" \
    GIT_BRANCH="main" \
    BLOB_ID="deadbeef" \
    CONTABASE_STATE_FILE="$dir/install-state.env" \
    CONTABASE_ASSUME_YES=1 \
    CONTABASE_VERSION="$explicit_ver" \
      bash "$LEAF_DOCKER" >"$out" 2>&1
  ) || rc=$?
  echo "$rc"
}

docker_no_mutation() {
  local current="$1" target="$2" state_ver="$3" explicit_ver="$4" expected_rc="$5" label="$6"
  local dir="$TEST_ROOT/dkr-$(echo "$label" | tr -c 'A-Za-z0-9' '_')"
  setup_docker_sandbox "$dir"

  local rc
  rc="$(run_docker_leaf "$dir" "$current" "$target" "$state_ver" "$explicit_ver" "$dir/out.log")"

  local backup_show=0 helper_files=0 down_calls=0 up_calls=0
  [ -f "$dir/git.log" ] && backup_show="$(grep -c 'backup.sh' "$dir/git.log" || true)"
  helper_files="$(find "$dir/tmp" -name 'contabase-backup-helper.*' 2>/dev/null | wc -l | tr -d ' ')"
  [ -f "$dir/docker.log" ] && down_calls="$(grep -c '^compose down' "$dir/docker.log" || true)"
  [ -f "$dir/docker.log" ] && up_calls="$(grep -c '^compose up' "$dir/docker.log" || true)"

  local ok=1
  [ "$rc" = "$expected_rc" ] || { fail_test "Docker: $label -> rc=$rc expected=$expected_rc"; ok=0; }
  [ "$backup_show" = "0" ] || { fail_test "Docker: $label -> git show backup.sh chamado ($backup_show)"; ok=0; }
  [ "$helper_files" = "0" ] || { fail_test "Docker: $label -> helper materializado ($helper_files)"; ok=0; }
  [ "$down_calls" = "0" ] || { fail_test "Docker: $label -> compose down chamado ($down_calls)"; ok=0; }
  [ "$up_calls" = "0" ] || { fail_test "Docker: $label -> compose up chamado ($up_calls)"; ok=0; }
  [ "$ok" = "1" ] && pass_test "Docker: $label -> rc=$rc sem mutacao (helper/down/up)"
}

# negative proofs
docker_no_mutation "v0.1.0-beta.9" "v0.1.0-beta.9" "v0.1.0-beta.9" "" 0 "ALREADY_CURRENT"
docker_no_mutation "v0.1.0-beta.9" "v0.1.0-beta.8" "v0.1.0-beta.9" "" 1 "DOWNGRADE_REJECTED"
docker_no_mutation "dev" "v0.1.0-beta.9" "" "" 1 "VERSION_AMBIGUOUS"

# provenance mismatch: explicit CONTABASE_VERSION != target VERSION
docker_provenance_mismatch() {
  local dir="$TEST_ROOT/dkr-provenance"
  setup_docker_sandbox "$dir"
  local rc
  rc="$(run_docker_leaf "$dir" "v0.1.0-beta.9" "v0.1.0-beta.9" "v0.1.0-beta.9" "v0.1.0-beta.8" "$dir/out.log")"

  local backup_show=0
  [ -f "$dir/git.log" ] && backup_show="$(grep -c 'backup.sh' "$dir/git.log" || true)"
  if [ "$rc" -ne 0 ] && [ "$backup_show" = "0" ]; then
    pass_test "Docker: TARGET_PROVENANCE_MISMATCH -> rc=$rc sem helper"
  else
    fail_test "Docker: provenance mismatch -> rc=$rc backup_show=$backup_show (esperado fail + sem helper)"
  fi
}
docker_provenance_mismatch

docker_positive_update() {
  local dir="$TEST_ROOT/dkr-positive"
  setup_docker_sandbox "$dir"
  # interrupt cleanly after helper materialization by making compose down fail
  export DOCKER_DOWN_RC=1
  local rc
  rc="$(run_docker_leaf "$dir" "v0.1.0-beta.8" "v0.1.0-beta.9" "v0.1.0-beta.8" "" "$dir/out.log")"
  unset DOCKER_DOWN_RC

  local backup_show=0
  [ -f "$dir/git.log" ] && backup_show="$(grep -c 'backup.sh' "$dir/git.log" || true)"
  if [ "$backup_show" -ge 1 ]; then
    pass_test "Docker: UPDATE beta.8 -> beta.9 -> helper materializado apos o gate (rc=$rc)"
  else
    fail_test "Docker: UPDATE -> helper NAO materializado (backup_show=$backup_show, rc=$rc)"
  fi
}
docker_positive_update

# ------------------------------------------------------------------------------
# 12. U3G — SOURCE A->B provenance (git real)
# ------------------------------------------------------------------------------
echo ""
echo "=== U3G SOURCE A->B (git real) ==="

U3G_GIT_REAL="$(command -v git)"

u3g_init_repo() {
  local dir="$1"
  mkdir -p "$dir"
  "$U3G_GIT_REAL" -C "$dir" init -q
  "$U3G_GIT_REAL" -C "$dir" config user.email "t@e"
  "$U3G_GIT_REAL" -C "$dir" config user.name "t"
}

u3g_source_commit() {
  local repo="$1" version="$2" sentinel="$3" msg="$4"
  mkdir -p "$repo/scripts/lib"
  cp "$REPO_DIR/scripts/lib/contabase-recovery.sh" "$repo/scripts/lib/contabase-recovery.sh"
  printf '%s\n' "$version" > "$repo/VERSION"
  printf 'module github.com/contabase-app/contabase\n\ngo 1.23\n' > "$repo/go.mod"
  mkdir -p "$repo/scripts/ops"
  printf '#!/bin/sh\nexit 0\n' > "$repo/scripts/build-css.sh"
  cat > "$repo/scripts/ops/backup.sh" <<EOF
#!/bin/sh
set -eu
dest="\${1%/}/backup-$sentinel"
mkdir -p "\$dest/uploads"
cp "\$DB_FILE" "\$dest/database.sqlite"
printf 'backup_id=test-$sentinel\n' > "\$dest/manifest.txt"
: > "\$dest/checksums.sha256"
printf '%s\n' "\$dest"
EOF
  chmod +x "$repo/scripts/build-css.sh" "$repo/scripts/ops/backup.sh"
  printf '%s\n' "$sentinel" > "$repo/scripts/ops/sentinel.txt"
  mkdir -p "$repo/templates" "$repo/assets"
  printf '%s\n' "$sentinel" > "$repo/templates/sentinel.txt"
  printf '%s\n' "$sentinel" > "$repo/assets/sentinel.txt"
  cat > "$repo/.gitignore" <<'GITIGNORE'
scripts/update-contabase-source.sh
install-state.env
tmp/
data/
install/
config/
usr-local-bin/
systemctl.log
*.log
GITIGNORE
  "$U3G_GIT_REAL" -C "$repo" add -A
  "$U3G_GIT_REAL" -C "$repo" commit -q -m "$msg"
  "$U3G_GIT_REAL" -C "$repo" rev-parse HEAD
}

u3g_git_wrapper() {
  local bin="$1" remote="$2" oid_b="$3"
  cat > "$bin/git" <<GITWRAP
#!/bin/sh
GIT_REAL="\${GIT_REAL:-$U3G_GIT_REAL}"
GIT_LOG="\${GIT_LOG:-/dev/null}"
FETCH_COUNT_FILE="\${FETCH_COUNT_FILE:-/tmp/u3g-fetch-count}"
MOVE_FLAG="\${MOVE_FLAG:-/tmp/u3g-move-flag}"
MERGE_NOOP="\${MERGE_NOOP:-0}"
ARCHIVE_FAIL_ONCE_FILE="\${ARCHIVE_FAIL_ONCE_FILE:-}"
REMOTE_DIR="\${REMOTE_DIR:-$remote}"
OID_B="\${OID_B:-$oid_b}"
printf '%s\n' "\$*" >> "\$GIT_LOG"
case "\$1" in
  fetch)
    n=\$(cat "\$FETCH_COUNT_FILE" 2>/dev/null || echo 0)
    n=\$((n+1))
    echo "\$n" > "\$FETCH_COUNT_FILE"
    ;;
  merge-base)
    if [ ! -f "\$MOVE_FLAG" ]; then
      "\$GIT_REAL" -C "\$REMOTE_DIR" update-ref refs/heads/main "\$OID_B" 2>/dev/null || true
      touch "\$MOVE_FLAG"
    fi
    ;;
  merge)
    if [ "\$MERGE_NOOP" = "1" ]; then
      exit 0
    fi
    ;;
  archive)
    if [ -n "\$ARCHIVE_FAIL_ONCE_FILE" ] && [ ! -f "\$ARCHIVE_FAIL_ONCE_FILE" ]; then
      touch "\$ARCHIVE_FAIL_ONCE_FILE"
      exit 1
    fi
    ;;
esac
exec "\$GIT_REAL" "\$@"
GITWRAP
  chmod +x "$bin/git"
}

u3g_source_stubs() {
  local bin="$1" dir="$2"
  mkdir -p "$bin"
  cat > "$bin/id" <<'EOF'
#!/bin/sh
echo 0
EOF
  cat > "$bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in -s) echo Linux ;; -m) echo x86_64 ;; *) echo Linux ;; esac
EOF
  cat > "$bin/systemctl" <<EOF
#!/bin/sh
printf '%s\n' "\$*" >> "$dir/systemctl.log"
[ "\${1:-}" != "start" ] || { mkdir -p "$dir/data"; : > "$dir/data/contabase.db"; }
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
esac
exit 0
EOF
  cat > "$bin/node" <<'EOF'
#!/bin/sh
case "${1:-}" in --version) echo "v24.0.0" ;; *) exit 0 ;; esac
EOF
  cat > "$bin/npm" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat > "$bin/curl" <<'EOF'
#!/bin/sh
echo '{"status":"healthy"}'
exit 0
EOF
  cat > "$bin/install" <<'EOF'
#!/bin/sh
if [ "$1" = "-d" ]; then
  shift
  paths=""
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
mkdir -p "$(dirname "$dst")"
cp "$src" "$dst"
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
  chmod +x "$bin/id" "$bin/uname" "$bin/systemctl" "$bin/go" "$bin/node" "$bin/npm" "$bin/curl" "$bin/install" "$bin/sqlite3" "$bin/openssl" "$bin/chown"
}

u3g_make_source_ab() {
  local base="$1"
  local remote="$base/remote" local="$base/local" bin="$base/bin"
  rm -rf "$remote" "$local" "$bin"
  u3g_init_repo "$remote"
  "$U3G_GIT_REAL" -C "$remote" checkout -q -b main
  u3g_source_commit "$remote" "v0.1.0-beta.8" "C" "C beta.8" >/dev/null
  OID_C="$( "$U3G_GIT_REAL" -C "$remote" rev-parse HEAD )"
  u3g_source_commit "$remote" "v0.1.0-beta.9" "A" "A beta.9" >/dev/null
  OID_A="$( "$U3G_GIT_REAL" -C "$remote" rev-parse HEAD )"
  u3g_source_commit "$remote" "v0.1.0-beta.10" "B" "B beta.10" >/dev/null
  OID_B="$( "$U3G_GIT_REAL" -C "$remote" rev-parse HEAD )"
  "$U3G_GIT_REAL" -C "$remote" update-ref refs/heads/main "$OID_A"
  "$U3G_GIT_REAL" clone -q "$remote" "$local"
  "$U3G_GIT_REAL" -C "$local" config user.email "t@e"
  "$U3G_GIT_REAL" -C "$local" config user.name "t"
  "$U3G_GIT_REAL" -C "$local" reset --hard -q "$OID_C"
  cp "$LEAF_SOURCE" "$local/scripts/update-contabase-source.sh"
  u3g_source_stubs "$bin" "$local"
  u3g_git_wrapper "$bin" "$remote" "$OID_B"
  rm -rf "$local/tmp"; mkdir -p "$local/tmp"
  mkdir -p "$local/install/templates" "$local/install/assets" "$local/config" "$local/usr-local-bin"
  printf 'v0.1.0-beta.8\n' > "$local/install/VERSION"
  printf 'CONTABASE_STATE_VERSION=1\nCONTABASE_INSTALL_METHOD=source\nCONTABASE_CHANNEL=beta\nCONTABASE_INSTALLED_VERSION=v0.1.0-beta.8\nCONTABASE_REPO_PATH=%s\n' "$local" > "$local/install-state.env"
}

u3g_source_ab_test() {
  local base="$TEST_ROOT/u3g-src-ab"
  u3g_make_source_ab "$base"
  local local="$base/local" bin="$base/bin"
  local fetch_file="$base/fetch-count" move_flag="$base/move-flag"
  rm -f "$fetch_file" "$move_flag"

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$move_flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_INSTALL_DIR="$local/install" \
    CONTABASE_TMP_BINARY="$local/tmp/contabase" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_PID1_COMM=systemd \
    DATA_DIR="$local/data" \
      bash "$local/scripts/update-contabase-source.sh" >"$base/out.log" 2>&1
  ) || rc=$?

  local fetch_count=0
  [ -f "$fetch_file" ] && fetch_count="$(cat "$fetch_file" | tr -d '[:space:]')"
  local head_oid
  head_oid="$( "$U3G_GIT_REAL" -C "$local" rev-parse HEAD 2>/dev/null )"

  local ok=1
  [ "$rc" = "0" ] || { fail_test "U3G Source A->B: rc=$rc esperado sucesso"; sed -n '1,220p' "$base/out.log" >&2; ok=0; }
  [ "$fetch_count" = "1" ] || { fail_test "U3G Source A->B: fetch count=$fetch_count esperado 1"; ok=0; }
  [ "$head_oid" = "$OID_A" ] || { fail_test "U3G Source A->B: HEAD=$head_oid esperado A"; ok=0; }
  grep -q "show $OID_A:scripts/ops/backup.sh" "$base/git.log" || { fail_test "U3G Source A->B: helper nao veio de A"; ok=0; }
  grep -q "merge --ff-only $OID_A" "$base/git.log" || { fail_test "U3G Source A->B: merge nao usou OID A"; ok=0; }
  grep -q "archive --format=tar $OID_A" "$base/git.log" || { fail_test "U3G Source A->B: archive nao usou OID A"; ok=0; }
  grep -q "$OID_B" "$base/git.log" && { fail_test "U3G Source A->B: OID B vazou em comando de conteudo"; ok=0; }
  grep -q '^pull' "$base/git.log" && { fail_test "U3G Source A->B: git pull foi usado"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.9' "$local/install-state.env" || { fail_test "U3G Source A->B: estado final nao confirmou beta.9"; ok=0; }
  [ "$(tr -d '[:space:]' < "$local/install/VERSION")" = "v0.1.0-beta.9" ] || { fail_test "U3G Source A->B: bundle final nao confirmou beta.9"; ok=0; }
  [ "$ok" = "1" ] && pass_test "U3G Source A->B: OID congelado A, 1 fetch, sem pull, HEAD/snapshot/helper de A"
}

u3g_source_ab_test

# ------------------------------------------------------------------------------
# 13. U3G — SOURCE dirty worktree (FAIL-CLOSED)
# ------------------------------------------------------------------------------
echo ""
echo "=== U3G SOURCE dirty ==="

u3g_source_dirty_test() {
  local label="$1" dirty_kind="$2"
  local base="$TEST_ROOT/u3g-src-dirty-$(echo "$label" | tr -c 'A-Za-z0-9' '_')"
  u3g_make_source_ab "$base"
  local local="$base/local" bin="$base/bin"
  local fetch_file="$base/fetch-count"
  rm -f "$fetch_file"

  case "$dirty_kind" in
    tracked) printf 'dirty\n' >> "$local/go.mod" ;;
    staged) printf 'dirty\n' >> "$local/go.mod"; "$U3G_GIT_REAL" -C "$local" add go.mod ;;
    untracked) printf 'dirty\n' > "$local/untracked-file.txt" ;;
  esac

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$base/move-flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    DATA_DIR="$local/data" \
      bash "$local/scripts/update-contabase-source.sh" >"$base/out.log" 2>&1
  ) || rc=$?

  local fetch_count=0
  [ -f "$fetch_file" ] && fetch_count="$(cat "$fetch_file" | tr -d '[:space:]')"
  local helper_show=0
  [ -f "$base/git.log" ] && helper_show="$(grep -c 'scripts/ops/backup.sh' "$base/git.log" || true)"

  local ok=1
  [ "$rc" -ne 0 ] || { fail_test "U3G Source dirty($dirty_kind): rc=0, esperado FAIL-CLOSED"; ok=0; }
  [ "$fetch_count" = "0" ] || { fail_test "U3G Source dirty($dirty_kind): fetch executado ($fetch_count)"; ok=0; }
  [ "$helper_show" = "0" ] || { fail_test "U3G Source dirty($dirty_kind): helper materializado"; ok=0; }
  [ "$ok" = "1" ] && pass_test "U3G Source dirty($dirty_kind): FAIL-CLOSED antes de fetch/helper"
}

u3g_source_dirty_test "tracked" "tracked"
u3g_source_dirty_test "staged" "staged"
u3g_source_dirty_test "untracked" "untracked"

# Falha antes do runtime novo: a segunda execucao precisa realizar o UPDATE inteiro.
u3h_source_snapshot_retry_test() {
  local base="$TEST_ROOT/u3h-src-snapshot-retry"
  u3g_make_source_ab "$base"
  local local="$base/local" bin="$base/bin" fetch_file="$base/fetch-count"
  touch "$base/move-flag"
  rm -f "$fetch_file" "$base/archive-fail"

  local rc_first=0
  (
    cd "$local"
    PATH="$bin:$PATH" TMPDIR="$local/tmp" GIT_LOG="$base/git.log" \
    FETCH_COUNT_FILE="$fetch_file" MOVE_FLAG="$base/move-flag" \
    ARCHIVE_FAIL_ONCE_FILE="$base/archive-fail" REMOTE_DIR="$base/remote" OID_B="$OID_B" GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" CONTABASE_INSTALL_DIR="$local/install" \
    CONTABASE_TMP_BINARY="$local/tmp/contabase" CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_PID1_COMM=systemd DATA_DIR="$local/data" \
      bash "$local/scripts/update-contabase-source.sh" >"$base/first.log" 2>&1
  ) || rc_first=$?

  local ok=1
  [ "$rc_first" -ne 0 ] || { fail_test "U3H Source snapshot retry: primeira execucao deveria falhar"; ok=0; }
  [ "$( "$U3G_GIT_REAL" -C "$local" rev-parse HEAD )" = "$OID_C" ] || { fail_test "U3H Source snapshot retry: checkout avancou antes do runtime"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.8' "$local/install-state.env" || { fail_test "U3H Source snapshot retry: estado antigo nao foi preservado"; ok=0; }
  [ "$(tr -d '[:space:]' < "$local/install/VERSION")" = "v0.1.0-beta.8" ] || { fail_test "U3H Source snapshot retry: runtime antigo nao foi preservado"; ok=0; }

  local rc_retry=0
  (
    cd "$local"
    PATH="$bin:$PATH" TMPDIR="$local/tmp" GIT_LOG="$base/git.log" \
    FETCH_COUNT_FILE="$fetch_file" MOVE_FLAG="$base/move-flag" \
    ARCHIVE_FAIL_ONCE_FILE="$base/archive-fail" REMOTE_DIR="$base/remote" OID_B="$OID_B" GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" CONTABASE_INSTALL_DIR="$local/install" \
    CONTABASE_TMP_BINARY="$local/tmp/contabase" CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_PID1_COMM=systemd DATA_DIR="$local/data" \
      bash "$local/scripts/update-contabase-source.sh" >"$base/retry.log" 2>&1
  ) || rc_retry=$?

  [ "$rc_retry" = "0" ] || { fail_test "U3H Source snapshot retry: segunda execucao rc=$rc_retry"; ok=0; }
  [ "$( "$U3G_GIT_REAL" -C "$local" rev-parse HEAD )" = "$OID_A" ] || { fail_test "U3H Source snapshot retry: HEAD final nao e B"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.9' "$local/install-state.env" || { fail_test "U3H Source snapshot retry: estado final nao e beta.9"; ok=0; }
  [ "$(tr -d '[:space:]' < "$local/install/VERSION")" = "v0.1.0-beta.9" ] || { fail_test "U3H Source snapshot retry: runtime final nao e beta.9"; ok=0; }
  grep -q '\[7/9\] Validacoes e build no snapshot' "$base/retry.log" || { fail_test "U3H Source snapshot retry: retry nao realizou UPDATE completo"; ok=0; }
  ! grep -q 'contradicao de versao\|nenhuma atualizacao necessaria' "$base/retry.log" || { fail_test "U3H Source snapshot retry: contradicao/falso no-op"; ok=0; }
  [ "$(cat "$fetch_file" | tr -d '[:space:]')" = "2" ] || { fail_test "U3H Source snapshot retry: esperado um fetch por execucao"; ok=0; }
  [ "$ok" = "1" ] && pass_test "U3H Source retry(snapshot): segunda execucao realiza UPDATE completo e converge"
}
u3h_source_snapshot_retry_test

# Falha na finalizacao do checkout, seguida de retry real sem intervencao.
u3g_source_merge_fail_test() {
  local state_mode="${1:-with-state}"
  local remote_mode="${2:-same-target}"
  local base="$TEST_ROOT/u3g-src-mergefail-$state_mode-$remote_mode"
  u3g_make_source_ab "$base"
  local local="$base/local" bin="$base/bin"
  local fetch_file="$base/fetch-count"
  rm -f "$fetch_file"
  if [ "$remote_mode" = "same-target" ]; then
    touch "$base/move-flag"
  fi
  if [ "$state_mode" = "without-state" ]; then
    rm -f "$local/install-state.env"
  fi

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$base/move-flag" \
    MERGE_NOOP=1 \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_INSTALL_DIR="$local/install" \
    CONTABASE_TMP_BINARY="$local/tmp/contabase" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_PID1_COMM=systemd \
    DATA_DIR="$local/data" \
      bash "$local/scripts/update-contabase-source.sh" >"$base/out.log" 2>&1
  ) || rc=$?

  local head_oid
  head_oid="$( "$U3G_GIT_REAL" -C "$local" rev-parse HEAD 2>/dev/null )"
  local archive_count=0
  [ -f "$base/git.log" ] && archive_count="$(grep -c 'archive --format=tar' "$base/git.log" || true)"

  local ok=1
  [ "$rc" -ne 0 ] || { fail_test "U3G Source merge-fail: rc=0, esperado FAIL-CLOSED"; ok=0; }
  [ "$head_oid" != "$OID_A" ] || { fail_test "U3G Source merge-fail: HEAD inesperadamente em A"; ok=0; }
  [ "$archive_count" = "1" ] || { fail_test "U3G Source merge-fail: snapshot count=$archive_count esperado 1"; ok=0; }
  if [ "$state_mode" = "with-state" ]; then
    grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.8' "$local/install-state.env" || { fail_test "U3G Source merge-fail: estado antigo nao foi preservado"; ok=0; }
  else
    [ ! -f "$local/install-state.env" ] || { fail_test "U3G Source merge-fail: estado foi antecipado no modo sem estado"; ok=0; }
  fi
  [ "$(tr -d '[:space:]' < "$local/install/VERSION")" = "v0.1.0-beta.9" ] || { fail_test "U3G Source merge-fail: runtime beta.9 nao ficou comprovado"; ok=0; }

  local rc_retry=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$base/move-flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_INSTALL_DIR="$local/install" \
    CONTABASE_TMP_BINARY="$local/tmp/contabase" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_PID1_COMM=systemd \
    DATA_DIR="$local/data" \
      bash "$local/scripts/update-contabase-source.sh" >"$base/retry.log" 2>&1
  ) || rc_retry=$?

  local expected_oid="$OID_A" expected_version="v0.1.0-beta.9"
  if [ "$remote_mode" = "advance-remote" ]; then
    expected_oid="$OID_B"
    expected_version="v0.1.0-beta.10"
  fi
  head_oid="$( "$U3G_GIT_REAL" -C "$local" rev-parse HEAD 2>/dev/null )"
  [ "$rc_retry" = "0" ] || { fail_test "U3H Source retry: rc=$rc_retry esperado 0"; sed -n '1,180p' "$base/retry.log" >&2; ok=0; }
  [ "$head_oid" = "$expected_oid" ] || { fail_test "U3H Source retry: HEAD nao finalizou alvo da segunda execucao"; ok=0; }
  grep -q "CONTABASE_INSTALLED_VERSION=$expected_version" "$local/install-state.env" || { fail_test "U3H Source retry: estado nao finalizou $expected_version"; ok=0; }
  [ "$(tr -d '[:space:]' < "$local/install/VERSION")" = "$expected_version" ] || { fail_test "U3H Source retry: runtime nao finalizou $expected_version"; ok=0; }
  if [ "$remote_mode" = "same-target" ]; then
    grep -q 'Finalizando transacao confirmada' "$base/retry.log" || { fail_test "U3H Source retry: nao entrou na finalizacao confirmada"; ok=0; }
  else
    grep -q "Target OID: $OID_B" "$base/retry.log" || { fail_test "U3H Source retry: segunda execucao nao congelou C"; ok=0; }
  fi
  ! grep -q 'nenhuma atualizacao necessaria' "$base/retry.log" || { fail_test "U3H Source retry: falso ALREADY_CURRENT"; ok=0; }
  [ "$ok" = "1" ] && pass_test "U3H Source retry($state_mode,$remote_mode): duas execucoes convergem sem misturar OIDs"
}

u3g_source_merge_fail_test with-state
u3g_source_merge_fail_test without-state
u3g_source_merge_fail_test with-state advance-remote

# ------------------------------------------------------------------------------
# 14. U3G — DOCKER A->B provenance (git real)
# ------------------------------------------------------------------------------
echo ""
echo "=== U3G DOCKER A->B (git real) ==="

u3g_docker_commit() {
  local repo="$1" version="$2" sentinel="$3" msg="$4"
  mkdir -p "$repo/scripts/lib"
  cp "$REPO_DIR/scripts/lib/contabase-recovery.sh" "$repo/scripts/lib/contabase-recovery.sh"
  printf '%s\n' "$version" > "$repo/VERSION"
  mkdir -p "$repo/scripts/ops"
  printf '#!/bin/sh\necho backup-helper-%s\n' "$sentinel" > "$repo/scripts/ops/backup.sh"
  printf '%s\n' "$sentinel" > "$repo/scripts/ops/sentinel.txt"
  printf 'FROM alpine:3.21\n' > "$repo/Dockerfile"
  printf '#!/bin/sh\nexit 0\n' > "$repo/docker-compose.yml"
  cat > "$repo/.gitignore" <<'GITIGNORE'
scripts/update-contabase-docker.sh
install-state.env
tmp/
data/
config/
usr-local-bin/
.env.docker
.env.docker.pre-update-*
docker-compose.override.yml
*.log
GITIGNORE
  "$U3G_GIT_REAL" -C "$repo" add -A
  "$U3G_GIT_REAL" -C "$repo" commit -q -m "$msg"
  "$U3G_GIT_REAL" -C "$repo" rev-parse HEAD
}

u3g_docker_stubs() {
  local bin="$1" dir="$2"
  mkdir -p "$bin"
  cat > "$bin/id" <<'EOF'
#!/bin/sh
echo 0
EOF
  cat > "$bin/curl" <<'EOF'
#!/bin/sh
if [ -n "${NEW_RUNTIME_MARKER:-}" ] && [ -f "$NEW_RUNTIME_MARKER" ] \
  && [ -n "${HEALTH_FAIL_ONCE_FILE:-}" ] && [ ! -f "$HEALTH_FAIL_ONCE_FILE" ]; then
  touch "$HEALTH_FAIL_ONCE_FILE"
  exit 1
fi
echo '{"status":"healthy"}'
exit 0
EOF
  cat > "$bin/docker" <<EOF
#!/bin/sh
DOCKER_LOG="\${DOCKER_LOG:-/dev/null}"
printf '%s\n' "\$*" >> "\$DOCKER_LOG"
container_id="\${DOCKER_EFFECTIVE_CONTAINER_ID:-contabase-test-container}"
if [ "\$1" = "compose" ]; then
  shift
  sub=""
  for a in "\$@"; do
    case "\$a" in config|ps|exec|down|up|port|logs) sub="\$a"; break ;; esac
  done
  case "\$sub" in
    config) exit 0 ;;
    ps)
      if [ -f "\${DOCKER_RUNNING_FILE:-/nonexistent}" ]; then
        if [ "\${DOCKER_HIDE_CONTAINER_AFTER_UP:-0}" = "1" ] \
          && [ -f "\${NEW_RUNTIME_MARKER:-/nonexistent}" ]; then
          exit 0
        fi
        echo "\$container_id"
        if [ -n "\${DOCKER_EXTRA_CONTAINER_ID_AFTER_UP:-}" ] \
          && [ -f "\${NEW_RUNTIME_MARKER:-/nonexistent}" ]; then
          echo "\$DOCKER_EXTRA_CONTAINER_ID_AFTER_UP"
        fi
      fi
      exit 0
      ;;
    exec) [ -f "\${DOCKER_RUNTIME_VERSION_FILE:-/nonexistent}" ] && cat "\$DOCKER_RUNTIME_VERSION_FILE"; exit 0 ;;
    down)
      rm -f "\${DOCKER_RUNNING_FILE:-/nonexistent}" "\${NEW_RUNTIME_MARKER:-/nonexistent}"
      exit "\${DOCKER_DOWN_RC:-0}"
      ;;
    up)
      if [ -n "\${DOCKER_COMPOSE_CAPTURE:-}" ]; then
        : > "\$DOCKER_COMPOSE_CAPTURE"
        expect=""; last_file=""
        for arg in "\$@"; do
          if [ "\$expect" = "project" ]; then
            printf 'project=%s\n' "\$arg" >> "\$DOCKER_COMPOSE_CAPTURE"
            expect=""; continue
          fi
          if [ "\$expect" = "file" ]; then
            printf 'file=%s\n' "\$arg" >> "\$DOCKER_COMPOSE_CAPTURE"
            last_file="\$arg"; expect=""; continue
          fi
          case "\$arg" in
            --project-directory) expect="project" ;;
            -f) expect="file" ;;
          esac
        done
        printf 'last_file=%s\n' "\$last_file" >> "\$DOCKER_COMPOSE_CAPTURE"
        [ -z "\$last_file" ] || sed 's/^/last_content=/' "\$last_file" >> "\$DOCKER_COMPOSE_CAPTURE"
      fi
      if [ -n "\${DOCKER_UP_FAIL_ONCE_FILE:-}" ] && [ ! -f "\$DOCKER_UP_FAIL_ONCE_FILE" ]; then
        touch "\$DOCKER_UP_FAIL_ONCE_FILE"
        exit 1
      fi
      [ -n "\${DOCKER_RUNNING_FILE:-}" ] && touch "\$DOCKER_RUNNING_FILE"
      [ -n "\${NEW_RUNTIME_MARKER:-}" ] && touch "\$NEW_RUNTIME_MARKER"
      if [ "\${DOCKER_KEEP_RUNTIME_ON_UP:-0}" != "1" ] \
        && [ -n "\${DOCKER_RUNTIME_VERSION_FILE:-}" ]; then
        printf '%s\n' "\${DOCKER_TARGET_VERSION:-}" > "\$DOCKER_RUNTIME_VERSION_FILE"
      fi
      exit 0
      ;;
    port) echo "8080" ;;
    logs) exit 0 ;;
    *) exit 0 ;;
  esac
fi
case "\$1" in
  inspect)
    case "\$*" in
      *'{{.Id}}'*) echo "\$container_id" ;;
      *'com.docker.compose.service'*) echo "\${DOCKER_SERVICE_LABEL:-contabase}" ;;
      *'com.docker.compose.project.working_dir'*) printf '%s\n' "\${DOCKER_PROJECT_WORKDIR:-\$(pwd -P)}" ;;
      *'com.docker.compose.project'*) echo "\${DOCKER_PROJECT_LABEL:-contabase-test}" ;;
      *'range .Mounts'*)
        if [ -n "\${DOCKER_MOUNTS_FILE_AFTER_UP:-}" ] \
          && [ -f "\${NEW_RUNTIME_MARKER:-/nonexistent}" ]; then
          cat "\$DOCKER_MOUNTS_FILE_AFTER_UP"
        elif [ -n "\${DOCKER_MOUNTS_FILE:-}" ] && [ -f "\$DOCKER_MOUNTS_FILE" ]; then
          cat "\$DOCKER_MOUNTS_FILE"
        else
          echo /app/data
        fi
        ;;
      *'.State.Health'*) echo "\${DOCKER_HEALTH_STATUS:-healthy}" ;;
      *'.RestartCount'*)
        if [ -n "\${DOCKER_RESTART_COUNT_FILE:-}" ] && [ -f "\$DOCKER_RESTART_COUNT_FILE" ]; then
          cat "\$DOCKER_RESTART_COUNT_FILE"
        else
          echo 0
        fi
        ;;
      *'{{.Config.Image}}'*) echo contabase:fixture-old ;;
      *'{{.Image}}'*) echo sha256:fixture-old ;;
      *) exit 1 ;;
    esac
    ;;
  exec)
    [ "\${2:-}" = "\$container_id" ] || exit 65
    [ "\${DOCKER_EXEC_FAIL:-0}" != "1" ] || exit 66
    [ -f "\${DOCKER_RUNTIME_VERSION_FILE:-/nonexistent}" ] || exit 67
    cat "\$DOCKER_RUNTIME_VERSION_FILE"
    ;;
  image) exit 0 ;;
  *) exit 0 ;;
esac
exit 0
EOF
  cat > "$bin/install" <<'EOF'
#!/bin/sh
dst=""
for arg in "$@"; do dst="$arg"; done
if [ -n "${PERSIST_FAIL_ONCE_FILE:-}" ] && [ "$dst" = "${CONTABASE_STATE_FILE:-}" ] \
  && [ ! -f "$PERSIST_FAIL_ONCE_FILE" ]; then
  touch "$PERSIST_FAIL_ONCE_FILE"
  exit 1
fi
exec /usr/bin/install "$@"
EOF
  cat > "$bin/sudo" <<'EOF'
#!/bin/sh
exec "$@"
EOF
  chmod +x "$bin/id" "$bin/curl" "$bin/docker" "$bin/sudo" "$bin/install"
}

u3g_make_docker_ab() {
  local base="$1"
  local remote="$base/remote" local="$base/local" bin="$base/bin"
  rm -rf "$remote" "$local" "$bin"
  u3g_init_repo "$remote"
  "$U3G_GIT_REAL" -C "$remote" checkout -q -b main
  u3g_docker_commit "$remote" "v0.1.0-beta.8" "C" "C beta.8" >/dev/null
  OID_C="$( "$U3G_GIT_REAL" -C "$remote" rev-parse HEAD )"
  u3g_docker_commit "$remote" "v0.1.0-beta.9" "A" "A beta.9" >/dev/null
  OID_A="$( "$U3G_GIT_REAL" -C "$remote" rev-parse HEAD )"
  u3g_docker_commit "$remote" "v0.1.0-beta.10" "B" "B beta.10" >/dev/null
  OID_B="$( "$U3G_GIT_REAL" -C "$remote" rev-parse HEAD )"
  "$U3G_GIT_REAL" -C "$remote" update-ref refs/heads/main "$OID_A"
  "$U3G_GIT_REAL" clone -q "$remote" "$local"
  "$U3G_GIT_REAL" -C "$local" config user.email "t@e"
  "$U3G_GIT_REAL" -C "$local" config user.name "t"
  "$U3G_GIT_REAL" -C "$local" reset --hard -q "$OID_C"
  cp "$LEAF_DOCKER" "$local/scripts/update-contabase-docker.sh"
  u3g_docker_stubs "$bin" "$local"
  u3g_git_wrapper "$bin" "$remote" "$OID_B"
  rm -rf "$local/tmp"; mkdir -p "$local/tmp"
  mkdir -p "$local/config" "$local/usr-local-bin"
  printf 'v0.1.0-beta.8\n' > "$base/runtime-version"
  touch "$base/runtime-running"
  printf 'AUTH_ENCRYPTION_KEY=test-auth-key-not-real\nSECURITY_MASTER_KEY=test-security-key-not-real\n' > "$local/.env.docker"
  printf 'CONTABASE_STATE_VERSION=1\nCONTABASE_INSTALL_METHOD=docker\nCONTABASE_CHANNEL=beta\nCONTABASE_INSTALLED_VERSION=v0.1.0-beta.8\nCONTABASE_REPO_PATH=%s\n' "$local" > "$local/install-state.env"
}

u3g_docker_ab_test() {
  local base="$TEST_ROOT/u3g-dkr-ab"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"
  local fetch_file="$base/fetch-count" move_flag="$base/move-flag"
  rm -f "$fetch_file" "$move_flag"

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    DOCKER_LOG="$base/docker.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$move_flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 \
    DOCKER_RUNNING_FILE="$base/runtime-running" \
    DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version" \
    DOCKER_TARGET_VERSION="v0.1.0-beta.9" \
    NEW_RUNTIME_MARKER="$base/new-runtime" \
    HEALTHCHECK_ATTEMPTS=1 \
      bash "$LEAF_DOCKER" >"$base/out.log" 2>&1
  ) || rc=$?

  local fetch_count=0
  [ -f "$fetch_file" ] && fetch_count="$(cat "$fetch_file" | tr -d '[:space:]')"
  local head_oid
  head_oid="$( "$U3G_GIT_REAL" -C "$local" rev-parse HEAD 2>/dev/null )"

  local ok=1
  [ "$rc" = "0" ] || { fail_test "U3G Docker A->B: rc=$rc esperado sucesso"; ok=0; }
  [ "$fetch_count" = "1" ] || { fail_test "U3G Docker A->B: fetch count=$fetch_count esperado 1"; ok=0; }
  [ "$head_oid" = "$OID_A" ] || { fail_test "U3G Docker A->B: HEAD=$head_oid esperado A"; ok=0; }
  grep -q "show $OID_A:scripts/ops/backup.sh" "$base/git.log" || { fail_test "U3G Docker A->B: helper nao veio de A"; ok=0; }
  grep -q "merge --ff-only $OID_A" "$base/git.log" || { fail_test "U3G Docker A->B: merge nao usou OID A"; ok=0; }
  grep -q "archive --format=tar $OID_A" "$base/git.log" || { fail_test "U3G Docker A->B: archive nao usou OID A"; ok=0; }
  grep -q "$OID_B" "$base/git.log" && { fail_test "U3G Docker A->B: OID B vazou em comando de conteudo"; ok=0; }
  grep -q '^pull' "$base/git.log" && { fail_test "U3G Docker A->B: git pull foi usado"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.9' "$local/install-state.env" || { fail_test "U3G Docker A->B: estado final nao confirmou beta.9"; ok=0; }
  [ "$(tr -d '[:space:]' < "$base/runtime-version")" = "v0.1.0-beta.9" ] || { fail_test "U3I Docker A->B: runtime final nao e beta.9"; ok=0; }
  grep -q 'exec contabase-test-container cat /app/VERSION' "$base/docker.log" || { fail_test "U3I Docker A->B: VERSION nao foi lida do container efetivo"; ok=0; }
  grep -q 'State.Health' "$base/docker.log" || { fail_test "U3I Docker A->B: health Docker do container nao foi consultado"; ok=0; }
  grep -q 'range .Mounts' "$base/docker.log" || { fail_test "U3I Docker A->B: mounts efetivos nao foram auditados"; ok=0; }
  [ "$ok" = "1" ] && pass_test "U3I Docker A->B: OID congelado e runtime beta.9 comprovado no container efetivo"
}

u3g_docker_ab_test

# ------------------------------------------------------------------------------
# 14b. U3I — DOCKER runtime identity binding
# ------------------------------------------------------------------------------
echo ""
echo "=== U3I DOCKER runtime identity binding ==="

u3i_docker_runtime_mismatch_retry_test() {
  local base="$TEST_ROOT/u3i-dkr-runtime-mismatch"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin" fetch_file="$base/fetch-count"
  touch "$base/move-flag"
  rm -f "$fetch_file" "$base/new-runtime"

  local first_rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    DOCKER_LOG="$base/docker.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$base/move-flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 \
    DOCKER_RUNNING_FILE="$base/runtime-running" \
    DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version" \
    DOCKER_TARGET_VERSION="v0.1.0-beta.9" \
    DOCKER_KEEP_RUNTIME_ON_UP=1 \
    NEW_RUNTIME_MARKER="$base/new-runtime" \
    HEALTHCHECK_ATTEMPTS=1 \
      bash "$LEAF_DOCKER" >"$base/first.log" 2>&1
  ) || first_rc=$?

  local ok=1 head_after_first
  head_after_first="$("$U3G_GIT_REAL" -C "$local" rev-parse HEAD)"
  [ "$first_rc" -ne 0 ] || { fail_test "U3I mismatch: primeira execucao deveria falhar"; ok=0; }
  [ "$head_after_first" = "$OID_C" ] || { fail_test "U3I mismatch: checkout avancou apesar de runtime A"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.8' "$local/install-state.env" || { fail_test "U3I mismatch: estado A nao foi preservado"; ok=0; }
  [ "$(tr -d '[:space:]' < "$base/runtime-version")" = "v0.1.0-beta.8" ] || { fail_test "U3I mismatch: fixture nao preservou runtime A"; ok=0; }
  grep -q 'runtime do container .* esta em v0.1.0-beta.8, mas o alvo congelado e v0.1.0-beta.9' "$base/first.log" || { fail_test "U3I mismatch: diagnostico de identidade ausente"; ok=0; }
  ! grep -q 'Commit logico concluido' "$base/first.log" || { fail_test "U3I mismatch: sucesso foi declarado indevidamente"; ok=0; }

  local retry_rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    DOCKER_LOG="$base/docker.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$base/move-flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 \
    DOCKER_RUNNING_FILE="$base/runtime-running" \
    DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version" \
    DOCKER_TARGET_VERSION="v0.1.0-beta.9" \
    NEW_RUNTIME_MARKER="$base/new-runtime" \
    HEALTHCHECK_ATTEMPTS=1 \
      bash "$LEAF_DOCKER" >"$base/retry.log" 2>&1
  ) || retry_rc=$?

  [ "$retry_rc" = 0 ] || { fail_test "U3I mismatch retry: segunda execucao rc=$retry_rc"; ok=0; }
  [ "$("$U3G_GIT_REAL" -C "$local" rev-parse HEAD)" = "$OID_A" ] || { fail_test "U3I mismatch retry: checkout final nao e B"; ok=0; }
  [ "$(tr -d '[:space:]' < "$base/runtime-version")" = "v0.1.0-beta.9" ] || { fail_test "U3I mismatch retry: runtime final nao e B"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.9' "$local/install-state.env" || { fail_test "U3I mismatch retry: estado final nao e B"; ok=0; }
  [ "$ok" = 1 ] && pass_test "U3I Docker runtime A/target B: falha closed e segunda execucao converge em B"
  return 0
}

u3i_docker_mount_policy_test() {
  local base="$TEST_ROOT/u3i-dkr-mount-policy"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"
  touch "$base/move-flag"
  printf '/app/data\n/app\n' > "$base/mounts-after-up"

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" TMPDIR="$local/tmp" GIT_LOG="$base/git.log" DOCKER_LOG="$base/docker.log" \
    FETCH_COUNT_FILE="$base/fetch-count" MOVE_FLAG="$base/move-flag" REMOTE_DIR="$base/remote" OID_B="$OID_B" GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 DOCKER_RUNNING_FILE="$base/runtime-running" DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version" \
    DOCKER_TARGET_VERSION="v0.1.0-beta.9" DOCKER_MOUNTS_FILE_AFTER_UP="$base/mounts-after-up" \
    NEW_RUNTIME_MARKER="$base/new-runtime" HEALTHCHECK_ATTEMPTS=1 \
      bash "$LEAF_DOCKER" >"$base/out.log" 2>&1
  ) || rc=$?

  local ok=1
  [ "$rc" -ne 0 ] || { fail_test "U3I mount: /app deveria falhar"; ok=0; }
  grep -q 'mount operacional proibido.* /app' "$base/out.log" || { fail_test "U3I mount: diagnostico ausente"; ok=0; }
  [ "$("$U3G_GIT_REAL" -C "$local" rev-parse HEAD)" = "$OID_C" ] || { fail_test "U3I mount: checkout avancou"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.8' "$local/install-state.env" || { fail_test "U3I mount: estado avancou"; ok=0; }
  [ "$ok" = 1 ] && pass_test "U3I Docker mounts: /app rejeitado e /app/data aceito na deteccao inicial"
  return 0
}

u3i_docker_container_selection_test() {
  local base="$TEST_ROOT/u3i-dkr-container-selection"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"
  touch "$base/move-flag"

  local rc=0 foreign_health
  foreign_health="$(DOCKER_FOREIGN_CONTAINER_ID="foreign-healthy-container" \
    "$bin/docker" inspect foreign-healthy-container --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}')"
  (
    cd "$local"
    PATH="$bin:$PATH" TMPDIR="$local/tmp" GIT_LOG="$base/git.log" DOCKER_LOG="$base/docker.log" \
    FETCH_COUNT_FILE="$base/fetch-count" MOVE_FLAG="$base/move-flag" REMOTE_DIR="$base/remote" OID_B="$OID_B" GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 DOCKER_RUNNING_FILE="$base/runtime-running" DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version" \
    DOCKER_TARGET_VERSION="v0.1.0-beta.9" DOCKER_EFFECTIVE_CONTAINER_ID="compose-service-container" \
    DOCKER_FOREIGN_CONTAINER_ID="foreign-healthy-container" NEW_RUNTIME_MARKER="$base/new-runtime" HEALTHCHECK_ATTEMPTS=1 \
      bash "$LEAF_DOCKER" >"$base/out.log" 2>&1
  ) || rc=$?

  local ok=1
  [ "$foreign_health" = "healthy" ] || { fail_test "U3I container: fixture estranha nao estava healthy"; ok=0; }
  [ "$rc" = 0 ] || { fail_test "U3I container: happy path rc=$rc"; ok=0; }
  grep -q 'exec compose-service-container cat /app/VERSION' "$base/docker.log" || { fail_test "U3I container: VERSION nao veio do servico Compose"; ok=0; }
  ! grep -q 'foreign-healthy-container' "$base/docker.log" || { fail_test "U3I container: container estranho foi consultado"; ok=0; }
  [ "$ok" = 1 ] && pass_test "U3I Docker container: usa ID retornado pelo servico Compose e ignora estranho"
  return 0
}

u3i_docker_ambiguous_container_test() {
  local base="$TEST_ROOT/u3i-dkr-container-ambiguous"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"
  touch "$base/move-flag"

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" TMPDIR="$local/tmp" GIT_LOG="$base/git.log" DOCKER_LOG="$base/docker.log" \
    FETCH_COUNT_FILE="$base/fetch-count" MOVE_FLAG="$base/move-flag" REMOTE_DIR="$base/remote" OID_B="$OID_B" GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 DOCKER_RUNNING_FILE="$base/runtime-running" DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version" \
    DOCKER_TARGET_VERSION="v0.1.0-beta.9" DOCKER_EXTRA_CONTAINER_ID_AFTER_UP="second-container" \
    NEW_RUNTIME_MARKER="$base/new-runtime" HEALTHCHECK_ATTEMPTS=1 \
      bash "$LEAF_DOCKER" >"$base/out.log" 2>&1
  ) || rc=$?

  local ok=1
  [ "$rc" -ne 0 ] || { fail_test "U3I containers ambiguos: deveria falhar"; ok=0; }
  grep -q '2 containers.*identidade ambigua' "$base/out.log" || { fail_test "U3I containers ambiguos: diagnostico ausente"; ok=0; }
  [ "$("$U3G_GIT_REAL" -C "$local" rev-parse HEAD)" = "$OID_C" ] || { fail_test "U3I containers ambiguos: checkout avancou"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.8' "$local/install-state.env" || { fail_test "U3I containers ambiguos: estado avancou"; ok=0; }
  [ "$ok" = 1 ] && pass_test "U3I Docker containers ambiguos: fail-closed antes da promocao"
  return 0
}

u3i_docker_runtime_mismatch_retry_test
u3i_docker_mount_policy_test
u3i_docker_container_selection_test
u3i_docker_ambiguous_container_test

# ------------------------------------------------------------------------------
# 14c. U3H — DOCKER retry real em falhas intermediarias
# ------------------------------------------------------------------------------
echo ""
echo "=== U3H DOCKER retry (duas execucoes) ==="

u3h_docker_retry_test() {
  local kind="$1"
  local remote_mode="${2:-same-target}"
  local base="$TEST_ROOT/u3h-dkr-retry-$kind-$remote_mode"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"
  local fetch_file="$base/fetch-count"
  if [ "$remote_mode" = "same-target" ]; then
    touch "$base/move-flag"
  fi
  rm -f "$fetch_file" "$base/archive-fail" "$base/up-fail" "$base/health-fail" "$base/persist-fail" "$base/new-runtime"

  local rc_first=0
  (
    cd "$local"
    export PATH="$bin:$PATH"
    export TMPDIR="$local/tmp"
    export GIT_LOG="$base/git.log"
    export DOCKER_LOG="$base/docker.log"
    export FETCH_COUNT_FILE="$fetch_file"
    export MOVE_FLAG="$base/move-flag"
    export REMOTE_DIR="$base/remote"
    export OID_B="$OID_B"
    export GIT_REAL="$U3G_GIT_REAL"
    export CONTABASE_STATE_FILE="$local/install-state.env"
    export CONTABASE_MODE_FILE="$local/config/install-mode"
    export CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update"
    export CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update"
    export CONTABASE_ASSUME_YES=1
    export DOCKER_RUNNING_FILE="$base/runtime-running"
    export DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version"
    export DOCKER_TARGET_VERSION="v0.1.0-beta.9"
    export NEW_RUNTIME_MARKER="$base/new-runtime"
    export HEALTHCHECK_ATTEMPTS=1
    case "$kind" in
      snapshot) export ARCHIVE_FAIL_ONCE_FILE="$base/archive-fail" ;;
      up) export DOCKER_UP_FAIL_ONCE_FILE="$base/up-fail" ;;
      health) export HEALTH_FAIL_ONCE_FILE="$base/health-fail" ;;
      persist) export PERSIST_FAIL_ONCE_FILE="$base/persist-fail" ;;
    esac
    bash "$LEAF_DOCKER" >"$base/first.log" 2>&1
  ) || rc_first=$?

  local ok=1 head_after_first
  head_after_first="$( "$U3G_GIT_REAL" -C "$local" rev-parse HEAD )"
  [ "$rc_first" -ne 0 ] || { fail_test "U3H Docker retry($kind): primeira execucao deveria falhar"; ok=0; }
  grep -q 'CONTABASE_INSTALLED_VERSION=v0.1.0-beta.8' "$local/install-state.env" || { fail_test "U3H Docker retry($kind): estado antigo nao foi preservado"; ok=0; }
  if [ "$kind" = "persist" ]; then
    [ "$head_after_first" = "$OID_A" ] || { fail_test "U3H Docker retry(persist): checkout deveria estar em B confirmado"; ok=0; }
  else
    [ "$head_after_first" = "$OID_C" ] || { fail_test "U3H Docker retry($kind): checkout avancou antes do health"; ok=0; }
  fi

  local expected_oid="$OID_A" expected_version="v0.1.0-beta.9"
  if [ "$remote_mode" = "advance-remote" ]; then
    expected_oid="$OID_B"
    expected_version="v0.1.0-beta.10"
  fi

  local rc_retry=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    DOCKER_LOG="$base/docker.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$base/move-flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 \
    DOCKER_RUNNING_FILE="$base/runtime-running" \
    DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version" \
    DOCKER_TARGET_VERSION="$expected_version" \
    NEW_RUNTIME_MARKER="$base/new-runtime" \
    HEALTHCHECK_ATTEMPTS=1 \
      bash "$LEAF_DOCKER" >"$base/retry.log" 2>&1
  ) || rc_retry=$?

  local head_final runtime_final fetch_count
  head_final="$( "$U3G_GIT_REAL" -C "$local" rev-parse HEAD )"
  runtime_final="$(tr -d '[:space:]' < "$base/runtime-version")"
  fetch_count="$(cat "$fetch_file" | tr -d '[:space:]')"
  [ "$rc_retry" = "0" ] || { fail_test "U3H Docker retry($kind): segunda execucao rc=$rc_retry"; sed -n '1,180p' "$base/retry.log" >&2; ok=0; }
  [ "$head_final" = "$expected_oid" ] || { fail_test "U3H Docker retry($kind,$remote_mode): HEAD final nao e o alvo esperado"; ok=0; }
  [ "$runtime_final" = "$expected_version" ] || { fail_test "U3H Docker retry($kind,$remote_mode): runtime final nao e $expected_version"; ok=0; }
  grep -q "CONTABASE_INSTALLED_VERSION=$expected_version" "$local/install-state.env" || { fail_test "U3H Docker retry($kind,$remote_mode): estado final nao e $expected_version"; ok=0; }
  [ "$fetch_count" = "2" ] || { fail_test "U3H Docker retry($kind): fetch total=$fetch_count esperado 2"; ok=0; }
  ! grep -q 'contradicao de versao\|nenhuma atualizacao necessaria' "$base/retry.log" || { fail_test "U3H Docker retry($kind): retry caiu em contradicao/falso no-op"; ok=0; }
  if [ "$remote_mode" = "advance-remote" ]; then
    grep -q "Alvo congelado: $OID_B" "$base/retry.log" || { fail_test "U3H Docker retry($kind,$remote_mode): segunda execucao nao congelou C"; ok=0; }
  fi
  [ "$ok" = "1" ] && pass_test "U3H Docker retry($kind,$remote_mode): segunda execucao converge runtime/checkout/estado"
}

u3h_docker_retry_test snapshot
u3h_docker_retry_test up
u3h_docker_retry_test health
u3h_docker_retry_test persist
u3h_docker_retry_test persist advance-remote

# ------------------------------------------------------------------------------
# 15. U3G — DOCKER dirty worktree + flag combos
# ------------------------------------------------------------------------------
echo ""
echo "=== U3G DOCKER dirty ==="

u3g_docker_dirty_test() {
  local label="$1" dirty_kind="$2"
  local base="$TEST_ROOT/u3g-dkr-dirty-$(echo "$label" | tr -c 'A-Za-z0-9' '_')"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"
  local fetch_file="$base/fetch-count"
  rm -f "$fetch_file"

  case "$dirty_kind" in
    tracked) printf 'dirty\n' >> "$local/VERSION" ;;
    staged) printf 'dirty\n' >> "$local/VERSION"; "$U3G_GIT_REAL" -C "$local" add VERSION ;;
    untracked) printf 'dirty\n' > "$local/untracked-file.txt" ;;
  esac

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    DOCKER_LOG="$base/docker.log" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$base/move-flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 \
      bash "$LEAF_DOCKER" >"$base/out.log" 2>&1
  ) || rc=$?

  local fetch_count=0
  [ -f "$fetch_file" ] && fetch_count="$(cat "$fetch_file" | tr -d '[:space:]')"
  local ok=1
  [ "$rc" -ne 0 ] || { fail_test "U3G Docker dirty($dirty_kind): rc=0, esperado FAIL-CLOSED"; ok=0; }
  [ "$fetch_count" = "0" ] || { fail_test "U3G Docker dirty($dirty_kind): fetch executado"; ok=0; }
  [ "$ok" = "1" ] && pass_test "U3G Docker dirty($dirty_kind): FAIL-CLOSED antes de fetch"
}

u3g_docker_dirty_test "tracked" "tracked"
u3g_docker_dirty_test "staged" "staged"
u3g_docker_dirty_test "untracked" "untracked"

u3g_docker_skip_pull_dirty_test() {
  local base="$TEST_ROOT/u3g-dkr-skippull-dirty"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"
  printf 'dirty\n' >> "$local/VERSION"

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    DOCKER_LOG="$base/docker.log" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 \
      bash "$LEAF_DOCKER" --skip-pull >"$base/out.log" 2>&1
  ) || rc=$?

  [ "$rc" -ne 0 ] && pass_test "U3G Docker --skip-pull dirty: FAIL-CLOSED" \
    || fail_test "U3G Docker --skip-pull dirty: rc=0, esperado FAIL-CLOSED"
}
u3g_docker_skip_pull_dirty_test

u3g_docker_force_reset_skip_pull_test() {
  local base="$TEST_ROOT/u3g-dkr-force-skip"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    DOCKER_LOG="$base/docker.log" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 \
      bash "$LEAF_DOCKER" --force-reset --skip-pull >"$base/out.log" 2>&1
  ) || rc=$?

  [ "$rc" -ne 0 ] && pass_test "U3G Docker --force-reset --skip-pull: REJECT" \
    || fail_test "U3G Docker --force-reset --skip-pull: rc=0, esperado rejeicao"
}
u3g_docker_force_reset_skip_pull_test

# ------------------------------------------------------------------------------
# 16. U3G — DOCKER compose canonical override
# ------------------------------------------------------------------------------
echo ""
echo "=== U3G DOCKER compose override ==="

u3g_docker_compose_override_test() {
  local base="$TEST_ROOT/u3g-dkr-override"
  u3g_make_docker_ab "$base"
  local local="$base/local" bin="$base/bin"
  local fetch_file="$base/fetch-count" move_flag="$base/move-flag"
  rm -f "$fetch_file" "$move_flag"
  printf 'services:\n  contabase:\n    build:\n      context: /host-override-must-lose\n' > "$local/docker-compose.override.yml"

  local rc=0
  (
    cd "$local"
    PATH="$bin:$PATH" \
    TMPDIR="$local/tmp" \
    GIT_LOG="$base/git.log" \
    DOCKER_LOG="$base/docker.log" \
    DOCKER_COMPOSE_CAPTURE="$base/compose.capture" \
    FETCH_COUNT_FILE="$fetch_file" \
    MOVE_FLAG="$move_flag" \
    REMOTE_DIR="$base/remote" \
    OID_B="$OID_B" \
    GIT_REAL="$U3G_GIT_REAL" \
    CONTABASE_STATE_FILE="$local/install-state.env" \
    CONTABASE_MODE_FILE="$local/config/install-mode" \
    CONTABASE_UPDATE_WRAPPER="$local/usr-local-bin/contabase-update" \
    CONTABASE_UPDATE_ALIAS="$local/usr-local-bin/cb-update" \
    CONTABASE_ASSUME_YES=1 \
    DOCKER_RUNNING_FILE="$base/runtime-running" \
    DOCKER_RUNTIME_VERSION_FILE="$base/runtime-version" \
    DOCKER_TARGET_VERSION="v0.1.0-beta.9" \
    NEW_RUNTIME_MARKER="$base/new-runtime" \
    HEALTHCHECK_ATTEMPTS=1 \
      bash "$LEAF_DOCKER" >"$base/out.log" 2>&1
  ) || rc=$?

  local up_line capture="$base/compose.capture" local_real
  local_real="$(cd "$local" && pwd -P)"
  up_line="$(grep -m1 ' up ' "$base/docker.log" 2>/dev/null || true)"

  local ok=1
  if [ -n "$up_line" ]; then
    printf '%s' "$up_line" | grep -q -- "--build" || { fail_test "U3G Docker override: up sem --build"; ok=0; }
    printf '%s' "$up_line" | grep -q -- "contabase-compose-override" || { fail_test "U3G Docker override: override canonico ausente nos args"; ok=0; }
  else
    fail_test "U3G Docker override: compose up --build nao chamado"
    ok=0
  fi
  [ -f "$capture" ] || { fail_test "U3G Docker override: captura comportamental ausente"; ok=0; }
  if [ -f "$capture" ]; then
    grep -Fxq "project=$local_real" "$capture" || { fail_test "U3G Docker override: project-directory nao preservou o repo"; ok=0; }
    [ "$(grep -c '^file=' "$capture")" = "3" ] || { fail_test "U3G Docker override: cadeia -f nao tem base + host + canonico"; ok=0; }
    sed -n '1p' "$capture" | grep -q '^project=' || { fail_test "U3G Docker override: project-directory fora da precedencia esperada"; ok=0; }
    sed -n '2p' "$capture" | grep -q 'contabase-build-snapshot.*/docker-compose.yml$' || { fail_test "U3G Docker override: base nao veio do snapshot"; ok=0; }
    sed -n '3p' "$capture" | grep -q 'file=docker-compose.override.yml$' || { fail_test "U3G Docker override: override local nao ficou entre base e canonico"; ok=0; }
    grep -q '^last_file=.*contabase-compose-override' "$capture" || { fail_test "U3G Docker override: override canonico nao tem maior precedencia"; ok=0; }
    grep -q "last_content=      context: $local/tmp/contabase-build-snapshot" "$capture" || { fail_test "U3G Docker override: contexto efetivo nao e snapshot canonico"; ok=0; }
    grep -q "last_content=      dockerfile: $local/tmp/contabase-build-snapshot.*/Dockerfile" "$capture" || { fail_test "U3G Docker override: Dockerfile efetivo nao e canonico"; ok=0; }
    grep -q 'last_content=        VERSION: v0.1.0-beta.9' "$capture" || { fail_test "U3G Docker override: VERSION efetiva nao e beta.9"; ok=0; }
  fi
  [ "$ok" = "1" ] && pass_test "U3G Docker override: precedencia efetiva preserva repo e força snapshot/version canonicos"
}
u3g_docker_compose_override_test

# ------------------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------------------
echo ""
echo "============================================"
echo "  RESULTS: $PASS_COUNT passed, $FAIL_COUNT failed"
echo "============================================"

if [ "$FAIL_COUNT" -gt 0 ]; then
  exit 1
fi
exit 0

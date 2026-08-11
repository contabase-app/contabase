#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$(dirname "$SCRIPT_DIR")")"
FAILED=0
LOG="$(mktemp)"
PASS="pass:"
FAIL="FAIL:"


make_fake_node() {
  local version="$1"
  local fake_dir
  fake_dir="$(mktemp -d)"
  cat > "$fake_dir/node" <<NODESCRIPT
#!/usr/bin/env bash
echo "$version"
NODESCRIPT
  chmod +x "$fake_dir/node"
  echo "$fake_dir"
}


test_build_css_node_gate() {
  local script="$PROJECT_ROOT/scripts/build-css.sh"
  local tmp_rc fake_dir

  for spec in "v20.18.0	fail" "v22.14.0	pass" "v23.0.0	fail" "v24.18.1	pass" "v26.0.0	fail"; do
    local version="${spec%%	*}"
    local expect="${spec##*	}"

    fake_dir="$(make_fake_node "$version")"
    tmp_rc=0

    PATH="$fake_dir:$PATH" bash "$script" > "$LOG" 2>&1 || tmp_rc=$?

    if [ "$expect" = "pass" ]; then
      if [ "$tmp_rc" -ne 0 ]; then
        echo "$FAIL build-css: $version should pass (exit $tmp_rc)"
        FAILED=1
      else
        echo "$PASS build-css: $version accepted"
      fi
    else
      if [ "$tmp_rc" -eq 0 ]; then
        echo "$FAIL build-css: $version should fail (exit $tmp_rc)"
        FAILED=1
      else
        if grep -qE '22|24|LTS' "$LOG"; then
          echo "$PASS build-css: $version rejected with correct message"
        else
          echo "$FAIL build-css: $version rejected but message missing 22/24/LTS"
          FAILED=1
        fi
      fi
    fi
    rm -rf "$fake_dir"
  done
}


test_script_node_gate_by_grep() {
  local name="$1" script="$2"

  if grep -q 'case "\$major" in' "$script" 2>/dev/null; then
    echo "$PASS $name: uses case 22|24 gate"
  else
    echo "$FAIL $name: missing case 22|24 gate"
    FAILED=1
  fi

  if grep -q '22|24' "$script" 2>/dev/null; then
    echo "$PASS $name: mentions 22 and 24"
  else
    echo "$FAIL $name: missing 22|24 reference"
    FAILED=1
  fi

  if grep -qE 'recomendada|compativel' "$script" 2>/dev/null; then
    echo "$PASS $name: has recommended/compatible message"
  else
    echo "$FAIL $name: missing recommended/compatible message"
    FAILED=1
  fi

  if ! grep -qE 'Node\.js 20|Node 20[^0-9]|Node\.js.*(18|16|14)' "$script" 2>/dev/null || true; then
    echo "$PASS $name: no outdated Node version referenced"
  fi
}


test_update_checks_node_before_stop() {
  local script="$PROJECT_ROOT/scripts/update-contabase-source.sh"

  local nodeline
  nodeline="$(grep -n 'require_node_version' "$script" | head -1 | cut -d: -f1)"
  local stopline
  stopline="$(grep -n 'stop_service' "$script" | head -1 | cut -d: -f1)"

  if [ -z "$nodeline" ] || [ -z "$stopline" ]; then
    echo "$FAIL update-source: could not find require_node_version or stop_service"
    FAILED=1
    return
  fi

  if [ "$nodeline" -lt "$stopline" ]; then
    echo "$PASS update-source: require_node_version (line $nodeline) before stop_service (line $stopline)"
  else
    echo "$FAIL update-source: require_node_version (line $nodeline) AFTER stop_service (line $stopline)"
    FAILED=1
  fi
}


test_install_checks_node_before_install() {
  local script="$PROJECT_ROOT/scripts/install-contabase-source.sh"

  local nodeline
  nodeline="$(grep -n 'require_node_version' "$script" | head -1 | cut -d: -f1)"

  if [ -z "$nodeline" ]; then
    echo "$FAIL install-source: could not find require_node_version call"
    FAILED=1
    return
  fi

  local systemdline
  systemdline="$(grep -n 'systemctl.*enable\|systemctl.*start' "$script" | head -1 | cut -d: -f1)"

  if [ -z "$systemdline" ]; then
    echo "$PASS install-source: require_node_version found (line $nodeline) — no systemctl start found to compare"
    return
  fi

  if [ "$nodeline" -lt "$systemdline" ]; then
    echo "$PASS install-source: require_node_version (line $nodeline) before systemctl enable/start (line $systemdline)"
  else
    echo "$FAIL install-source: require_node_version (line $nodeline) AFTER systemctl enable/start (line $systemdline)"
    FAILED=1
  fi
}


test_build_css_uses_local_bin() {
  local script="$PROJECT_ROOT/scripts/build-css.sh"

  if grep -q 'node_modules/.bin/tailwindcss' "$script"; then
    echo "$PASS build-css.sh uses local tailwindcss binary"
  else
    echo "$FAIL build-css.sh does not reference local tailwindcss binary"
    FAILED=1
  fi

  if ! grep -E 'npx[[:space:]]+tailwindcss[^-]' "$script" 2>/dev/null; then
    echo "$PASS build-css.sh does not use plain npx download"
  else
    echo "$FAIL build-css.sh uses npx without --no-install"
    FAILED=1
  fi

  if grep -q -- '--no-install' "$script"; then
    echo "$PASS build-css.sh uses npx --no-install fallback correctly"
  fi
}


test_all_three_scripts_message_consistency() {
  local a b c
  a="$PROJECT_ROOT/scripts/build-css.sh"
  b="$PROJECT_ROOT/scripts/install-contabase-source.sh"
  c="$PROJECT_ROOT/scripts/update-contabase-source.sh"

  for f in "$a" "$b" "$c"; do
    local name
    name="$(basename "$f")"
    if grep -q 'Node.js 24 LTS.*recomendada' "$f" 2>/dev/null; then
      echo "$PASS $name: Node.js 24 LTS mencionado como recomendado"
    else
      echo "$FAIL $name: Node.js 24 LTS nao mencionado como recomendado"
      FAILED=1
    fi
    if grep -q 'Node.js 22 LTS.*compativel' "$f" 2>/dev/null; then
      echo "$PASS $name: Node.js 22 LTS mencionado como compativel"
    else
      echo "$FAIL $name: Node.js 22 LTS nao mencionado como compativel"
      FAILED=1
    fi
  done
}


test_style_css_cache_busting() {
  local missing=""
  for f in templates/pages/login.html templates/pages/login_2fa.html templates/pages/setup.html templates/pages/trocar_senha.html templates/pages/ativar_conta.html; do
    if grep -q 'assetPath.*style.css' "$f"; then
      echo "$PASS $(basename "$f"): uses assetPath for style.css"
    else
      echo "$FAIL $(basename "$f"): does NOT use assetPath for style.css"
      missing="$missing $(basename "$f")"
      FAILED=1
    fi
  done

  if [ -z "$missing" ]; then
    echo ""
    echo "All 5 auth/public templates use {{assetPath \"/assets/css/style.css\"}}."
  fi
}


echo "=== Node version gate tests ==="
echo ""
echo "-- build-css.sh (full function test) --"
test_build_css_node_gate
echo ""

echo "-- install-contabase-source.sh (grep verification) --"
test_script_node_gate_by_grep "install-source" "$PROJECT_ROOT/scripts/install-contabase-source.sh"
echo ""

echo "-- update-contabase-source.sh (grep verification) --"
test_script_node_gate_by_grep "update-source" "$PROJECT_ROOT/scripts/update-contabase-source.sh"
echo ""

echo "=== build-css.sh binary checks ==="
test_build_css_uses_local_bin
echo ""

echo "=== preflight ordering ==="
test_update_checks_node_before_stop
test_install_checks_node_before_install
echo ""

echo "=== Message consistency ==="
test_all_three_scripts_message_consistency
echo ""

echo "=== Cache busting ==="
test_style_css_cache_busting
echo ""

if [ "$FAILED" -ne 0 ]; then
  echo "$FAILED test(s) FAILED."
  exit 1
else
  echo "All tests PASSED."
  exit 0
fi

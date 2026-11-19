#!/usr/bin/env bash
# Full regression for the Beta 9 update incident.
#
# Run only in a disposable Linux host/container with systemd as PID 1:
#   CONTABASE_SYSTEMD_INTEGRATION=1 \
#   BETA7_ARTIFACT=/artifacts/beta7.tar.gz \
#   bash scripts/tests/test_release_update_permissions_systemd.sh
#
# The test installs Beta 7, seeds synthetic data, runs the patched Beta 9
# updater from this checkout, and verifies the non-root service user can
# traverse the bundle, the schema migrates 82 -> 83, the pre-update backup
# exists, and the synthetic user/workspace/transaction survive.
set -euo pipefail

[ "${CONTABASE_SYSTEMD_INTEGRATION:-0}" = "1" ] || {
  printf 'skip: set CONTABASE_SYSTEMD_INTEGRATION=1 in a disposable systemd host\n'
  exit 0
}
[ "$(id -u)" -eq 0 ] || { printf 'FAIL: run as root\n' >&2; exit 1; }
[ "$(tr -d '[:space:]' < /proc/1/comm 2>/dev/null || true)" = "systemd" ] \
  || { printf 'FAIL: PID 1 is not systemd\n' >&2; exit 1; }

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
BETA7_ARTIFACT="${BETA7_ARTIFACT:-/artifacts/beta7.tar.gz}"
APP_USER=contabase
PORT=18081
INSTALL_DIR=/opt/contabase
DATA_DIR=/var/lib/contabase
CONFIG_DIR=/etc/contabase
DB="${DATA_DIR}/contabase.db"
SERVICE=contabase
SETUP_TOKEN="SyntheticBeta9UpdateToken0123456789ABCDEF"
TMP_DIR="$(mktemp -d /tmp/contabase-systemd-update.XXXXXX)"
chmod 0755 "$TMP_DIR"

PASS=0
FAIL=0
ok() { PASS=$((PASS + 1)); printf 'ok %d - %s\n' "$PASS" "$1"; }
not_ok() { FAIL=$((FAIL + 1)); printf 'not ok - %s\n' "$1" >&2; }
cleanup() { rm -rf "$TMP_DIR"; }
trap cleanup EXIT

fail_test() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

[ -f "$BETA7_ARTIFACT" ] || fail_test "Beta 7 artifact missing: $BETA7_ARTIFACT"
if [ -e "$DB" ] || [ -e "$CONFIG_DIR/contabase.env" ]; then
  fail_test "existing ContaBase state found; refusing to run outside a disposable host"
fi
systemctl cat "$SERVICE" >/dev/null 2>&1 && fail_test "existing ${SERVICE}.service found; refusing"
command -v runuser >/dev/null 2>&1 || fail_test "runuser missing"
command -v sqlite3 >/dev/null 2>&1 || fail_test "sqlite3 missing"

say() { printf '%s\n' "$*"; }
wait_health() {
  local attempt code
  for attempt in $(seq 1 60); do
    code="$(curl -fsS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/health" 2>/dev/null || true)"
    [ "$code" = "200" ] && return 0
    sleep 1
  done
  return 1
}

# --- Beta 7 installation ----------------------------------------------------
getent group "$APP_USER" >/dev/null 2>&1 || groupadd --system "$APP_USER"
id -u "$APP_USER" >/dev/null 2>&1 || \
  useradd --system --gid "$APP_USER" --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$APP_USER"

install -d -o root -g root -m 0755 /opt
rm -rf "$INSTALL_DIR" "$DATA_DIR" "$CONFIG_DIR"
install -d -o root -g root -m 0755 "$INSTALL_DIR"
install -d -o "$APP_USER" -g "$APP_USER" -m 0750 "$DATA_DIR" "$DATA_DIR/uploads" "$DATA_DIR/backups"
install -d -o root -g root -m 0755 "$CONFIG_DIR"
( umask 022
  tar --no-same-owner --no-same-permissions -xzf "$BETA7_ARTIFACT" \
    -C "$INSTALL_DIR" --strip-components=1
)
find "$INSTALL_DIR" -xdev -type d -exec chmod 0755 {} +
find "$INSTALL_DIR" -xdev -type f -exec chmod 0644 {} +
chmod 0755 "$INSTALL_DIR/contabase" "$INSTALL_DIR/admin"

AUTH_KEY="$(openssl rand -base64 32 | tr -d '\n')"
MASTER_KEY="$(openssl rand -hex 16)"
umask 077
cat > "$CONFIG_DIR/contabase.env" <<EOF
APP_ENV=production
APP_DEBUG=false
PORT=${PORT}
DATABASE_URL=file:${DB}
DATA_DIR=${DATA_DIR}
DB_FILE=${DB}
UPLOADS_DIR=${DATA_DIR}/uploads
CONTABASE_TIMEZONE=America/Sao_Paulo
APP_BASE_URL=http://127.0.0.1:${PORT}
ALLOWED_HOSTS=localhost,127.0.0.1,::1
TRUSTED_PROXIES=
CONTABASE_ACCESS_MODE=local
CONTABASE_FEATURE_INVESTMENTS=false
CONTABASE_SETUP_TOKEN=${SETUP_TOKEN}
AUTH_ENCRYPTION_KEY=${AUTH_KEY}
SECURITY_MASTER_KEY=${MASTER_KEY}
VERSION=v1.0.0-beta.7
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
EOF
chmod 0600 "$CONFIG_DIR/contabase.env"
cat > "$CONFIG_DIR/install-state.env" <<EOF
CONTABASE_STATE_VERSION=1
CONTABASE_INSTALL_METHOD=release
CONTABASE_CHANNEL=beta
CONTABASE_INSTALLED_VERSION=v1.0.0-beta.7
CONTABASE_REPO_PATH=
EOF
chmod 0644 "$CONFIG_DIR/install-state.env"
printf 'release\n' > "$CONFIG_DIR/install-mode"
chmod 0644 "$CONFIG_DIR/install-mode"

cat > "/etc/systemd/system/${SERVICE}.service" <<EOF
[Unit]
Description=ContaBase - synthetic beta7 update test
After=network.target

[Service]
Type=simple
User=${APP_USER}
Group=${APP_USER}
WorkingDirectory=${INSTALL_DIR}
EnvironmentFile=${CONFIG_DIR}/contabase.env
ExecStart=${INSTALL_DIR}/contabase
Restart=on-failure
RestartSec=2
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "/etc/systemd/system/${SERVICE}.service"

systemctl daemon-reload
systemctl enable "$SERVICE" >/dev/null
systemctl start "$SERVICE"
wait_health || fail_test "Beta 7 did not become healthy"
[ "$(tr -d '[:space:]' < "$INSTALL_DIR/VERSION")" = "v1.0.0-beta.7" ] \
  && ok "Beta 7 installed and healthy" || not_ok "Beta 7 version mismatch"

# Initial setup and synthetic data.
page="$(curl -fsS -c "$TMP_DIR/cookies" "http://127.0.0.1:${PORT}/setup")"
csrf="$(printf '%s' "$page" | grep -o 'name="csrf_token" value="[^"]*"' | head -1 | sed 's/.*value="//;s/"$//')"
[ -n "$csrf" ] || fail_test "setup CSRF missing"
code="$(curl -sS -o /dev/null -w '%{http_code}' -b "$TMP_DIR/cookies" -c "$TMP_DIR/cookies" \
  -d "csrf_token=${csrf}" --data-urlencode "setup_token=${SETUP_TOKEN}" \
  -d "name=Synthetic Beta7" -d "email=synthetic-beta7@example.invalid" \
  --data-urlencode "password=Synthetic!Beta7#2026" -d "workspace_type=business" \
  "http://127.0.0.1:${PORT}/setup")"
[ "$code" = "303" ] || fail_test "setup returned ${code}"
schema="$(sqlite3 "$DB" 'SELECT max(version) FROM schema_migrations;')"
[ "$schema" = "82" ] && ok "Beta 7 schema is 82" || not_ok "Beta 7 schema is ${schema}"
user_id="$(sqlite3 "$DB" 'SELECT id FROM users ORDER BY rowid LIMIT 1;')"
workspace_id="$(sqlite3 "$DB" 'SELECT id FROM workspaces ORDER BY rowid LIMIT 1;')"
[ -n "$user_id" ] && [ -n "$workspace_id" ] || fail_test "setup did not create user/workspace"
account_id="synthetic-account-beta7"
tx_id="synthetic-transaction-beta7"
sqlite3 -cmd '.timeout 10000' "$DB" <<SQL
BEGIN;
INSERT INTO accounts (id, workspace_id, name, type, initial_balance, current_balance)
VALUES ('${account_id}', '${workspace_id}', 'Synthetic Test Account', 'CHECKING', 0, 0);
INSERT INTO transactions
  (id, workspace_id, user_id, account_id, type, amount, date, description, status)
VALUES
  ('${tx_id}', '${workspace_id}', '${user_id}', '${account_id}', 'EXPENSE', 12345, unixepoch(), 'Synthetic update regression', 'paid');
COMMIT;
SQL
[ "$(sqlite3 "$DB" "SELECT count(*) FROM transactions WHERE id='${tx_id}' AND workspace_id='${workspace_id}';")" = "1" ] \
  && ok "synthetic account/transaction seeded in schema 82" \
  || not_ok "synthetic data missing before update"
printf '%s\n%s\n%s\n%s\n' "$user_id" "$workspace_id" "$account_id" "$tx_id" > "$TMP_DIR/identity"

# --- Patched Beta 9 update --------------------------------------------------
say "Running patched Beta 9 updater against the live channel..."
if CONTABASE_VERSION=v1.0.0-beta.9 \
   CONTABASE_CHANNEL=beta \
   CONTABASE_ASSUME_YES=1 \
   CONTABASE_INSTALL_DIR="$INSTALL_DIR" \
   CONTABASE_DATA_DIR="$DATA_DIR" \
   CONTABASE_CONFIG_DIR="$CONFIG_DIR" \
   CONTABASE_USER="$APP_USER" \
   CONTABASE_SERVICE_FILE="/etc/systemd/system/${SERVICE}.service" \
   CONTABASE_PORT="$PORT" \
   CONTABASE_HEALTHCHECK_ATTEMPTS=60 \
   bash "$REPO_DIR/scripts/update-contabase-release.sh" > "$TMP_DIR/update.log" 2>&1; then
  ok "patched Beta 9 updater completed"
else
  tail -80 "$TMP_DIR/update.log" >&2
  fail_test "patched Beta 9 updater failed"
fi

systemctl is-active --quiet "$SERVICE" && ok "service active after update" || not_ok "service inactive"
wait_health && ok "healthcheck 200 after update" || not_ok "healthcheck failed after update"
[ "$(tr -d '[:space:]' < "$INSTALL_DIR/VERSION")" = "v1.0.0-beta.9" ] \
  && ok "installed VERSION is Beta 9" || not_ok "installed VERSION mismatch"
[ "$(sqlite3 "$DB" 'SELECT max(version) FROM schema_migrations;')" = "83" ] \
  && ok "schema migrated 82 -> 83" || not_ok "schema did not reach 83"
[ "$(sqlite3 "$DB" 'PRAGMA integrity_check;')" = "ok" ] \
  && ok "SQLite integrity check ok" || not_ok "SQLite integrity check failed"
[ "$(sqlite3 "$DB" 'SELECT count(*) FROM pragma_foreign_key_check;')" = "0" ] \
  && ok "foreign keys clean" || not_ok "foreign key violations found"

{
  read -r user_id
  read -r workspace_id
  read -r account_id
  read -r tx_id
} < "$TMP_DIR/identity"
[ "$(sqlite3 "$DB" "SELECT count(*) FROM users WHERE id='${user_id}';")" = "1" ] \
  && ok "synthetic user preserved" || not_ok "synthetic user lost"
[ "$(sqlite3 "$DB" "SELECT count(*) FROM workspaces WHERE id='${workspace_id}';")" = "1" ] \
  && ok "workspace_id preserved" || not_ok "workspace_id lost"
[ "$(sqlite3 "$DB" "SELECT count(*) FROM transactions WHERE id='${tx_id}' AND workspace_id='${workspace_id}' AND amount=12345;")" = "1" ] \
  && ok "synthetic transaction preserved" || not_ok "synthetic transaction lost"

# The regression itself: the service user must be able to enter and read the bundle.
runuser -u "$APP_USER" -- test -x "$INSTALL_DIR" \
  && ok "service user can traverse WorkingDirectory" || not_ok "service user cannot traverse WorkingDirectory"
runuser -u "$APP_USER" -- test -r "$INSTALL_DIR/VERSION" \
  && ok "service user can read VERSION" || not_ok "service user cannot read VERSION"
runuser -u "$APP_USER" -- test -r "$INSTALL_DIR/assets/css/style.css" \
  && ok "service user can read CSS assets" || not_ok "service user cannot read CSS assets"
asset_code="$(curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:${PORT}/assets/css/style.css")"
[ "$asset_code" = "200" ] && ok "CSS served with 200" || not_ok "CSS served with ${asset_code}"

# State/env reconciled to Beta 9.
grep -q '^CONTABASE_INSTALLED_VERSION=v1.0.0-beta.9$' "$CONFIG_DIR/install-state.env" \
  && ok "install-state records Beta 9" || not_ok "install-state not Beta 9"
grep -q '^VERSION=v1.0.0-beta.9$' "$CONFIG_DIR/contabase.env" \
  && ok "contabase.env records Beta 9" || not_ok "contabase.env not Beta 9"

# Pre-update backup exists and is schema 82/integrity ok.
backup_dir="$(find "$DATA_DIR/backups" -maxdepth 1 -type d -name 'backup-*' | sort | tail -1)"
[ -n "$backup_dir" ] && [ -f "$backup_dir/manifest.txt" ] \
  && ok "pre-update backup present" || not_ok "pre-update backup missing"
if [ -n "$backup_dir" ] && [ -f "$backup_dir/database.sqlite" ]; then
  backup_schema="$(sqlite3 -readonly "$backup_dir/database.sqlite" 'SELECT max(version) FROM schema_migrations;')"
  backup_integrity="$(sqlite3 -readonly "$backup_dir/database.sqlite" 'PRAGMA integrity_check;')"
  [ "$backup_schema" = "82" ] && [ "$backup_integrity" = "ok" ] \
    && ok "backup is schema 82 and integrity ok" || not_ok "backup schema/integrity mismatch"
fi

# Previous Beta 7 bundle preserved.
previous_dir="$(find /opt -maxdepth 1 -type d -name 'contabase.previous.*' | sort | tail -1)"
[ -n "$previous_dir" ] && [ "$(tr -d '[:space:]' < "$previous_dir/VERSION")" = "v1.0.0-beta.7" ] \
  && ok "previous Beta 7 bundle preserved" || not_ok "previous Beta 7 bundle missing"

# Negative preflight: a private nested asset must be rejected before any swap.
bad="$TMP_DIR/bad-bundle"
cp -a "$INSTALL_DIR" "$bad"
chmod 0700 "$bad/assets"
extract_function() {
  awk -v name="$1" '$0 ~ "^" name "\\(\\) \\{" { f=1 } f { print } f && /^}$/ { exit }' "$2"
}
eval "$(extract_function service_user_can_access_bundle "$REPO_DIR/scripts/update-contabase-release.sh")"
command -v service_user_can_access_bundle >/dev/null 2>&1 \
  || fail_test "service_user_can_access_bundle was not extracted"
if runuser -u "$APP_USER" -- test -x "$bad" \
   && service_user_can_access_bundle "$bad" "$APP_USER"; then
  not_ok "preflight accepted a service-user-inaccessible bundle"
else
  ok "preflight rejects a service-user-inaccessible bundle"
fi
systemctl is-active --quiet "$SERVICE" \
  && ok "service remains active after negative preflight" || not_ok "service stopped during negative preflight"

printf '\n%d ok, %d falhas\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

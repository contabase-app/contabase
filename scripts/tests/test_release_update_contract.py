#!/usr/bin/env python3
"""B8-4C: updater real com fronteiras Linux/ownership/rede simuladas."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
UPDATER = ROOT / 'scripts/update-contabase-release.sh'
WRAPPER = ROOT / 'scripts/lib/contabase-update-wrapper.sh'
OLD = 'v1.0.0-beta.7'
NEW = 'v1.0.0-beta.8'
SHA = 'a' * 40
SECRET = 'SYNTHETIC-PRIVATE-ENV-SENTINEL'


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


class ReleaseUpdateContract(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='contabase-b84c-test-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.bin = self.root / 'bin'
        self.config = self.root / 'custom-config'
        self.install = self.root / 'custom-install'
        self.data = self.root / 'custom-data'
        self.unit = self.root / 'contabase.service'
        self.state = self.config / 'install-state.env'
        self.env_file = self.config / 'contabase.env'
        self.log = self.root / 'events.log'
        for path in (self.bin, self.config, self.install, self.data, self.root / 'home', self.root / 'work'):
            path.mkdir()
        (self.root / 'lib').mkdir()
        (self.root / 'lib/contabase-update-wrapper.sh').write_text(
            '#!/bin/sh\n# synthetic authenticated wrapper\n')
        (self.data / 'uploads').mkdir()
        for name in ('contabase.db', 'contabase.db-wal', 'contabase.db-shm'):
            (self.data / name).write_bytes(('synthetic-persistent-' + name).encode())
        (self.install / 'contabase').write_bytes(b'old-binary')
        (self.install / 'VERSION').write_text(OLD + '\n')
        self.bundle = self.root / 'extract/contabase'
        self.bundle.mkdir(parents=True)
        (self.bundle / 'contabase').write_bytes(b'candidate-binary')
        (self.bundle / 'admin').write_bytes(b'candidate-admin')
        (self.bundle / 'VERSION').write_text(NEW + '\n')
        (self.bundle / 'templates').mkdir()
        (self.bundle / 'assets').mkdir()
        self.process_binary = self.root / 'process-binary'
        self.env = {'PATH': str(self.bin) + ':/opt/homebrew/bin:/usr/local/bin:' + os.defpath,
                    'HOME': str(self.root / 'home'), 'TMPDIR': str(self.root / 'work'),
                    'LC_ALL': 'C', 'FIXTURE': str(self.root),
                    'CONTABASE_VERSION': NEW, 'CONTABASE_CHANNEL': 'beta'}
        self.stub('chown', 'printf "ownership %s\\n" "$*" >> "$FIXTURE/events.log"\n')
        self.stub('mktemp', '[ "$#" -ne 0 ] || set -- "$TMPDIR/env-key.XXXXXX"\nexec /usr/bin/mktemp "$@"\n')
        self.stub('systemctl', '''
case "$*" in
  *--property=FragmentPath*) printf '%s/contabase.service\\n' "$FIXTURE" ;;
  *--property=DropInPaths*) printf '%s' "${FIXTURE_DROPINS:-}" ;;
  *--property=NeedDaemonReload*) printf '%s' "${FIXTURE_RELOAD:-no}" ;;
  *--property=MainPID*) printf '%s' "${FIXTURE_PID:-4242}" ;;
  'is-active '*) [ "${FIXTURE_INACTIVE:-0}" != 1 ] ;;
  'restart contabase')
    printf 'restart\\n' >> "$FIXTURE/events.log"
    [ "${FIXTURE_START_FAIL:-0}" != 1 ] || exit 1
    cp "$FIXTURE/custom-install/contabase" "$FIXTURE/process-binary"
    if [ "${FIXTURE_BINARY_MISMATCH:-0}" = 1 ]; then printf old > "$FIXTURE/process-binary"; fi ;;
  'stop contabase') printf 'stop\\n' >> "$FIXTURE/events.log" ;;
  daemon-reload) : ;;
  *) exit 1 ;;
esac
''')
        self.stub('readlink', '''
if [ "$1" = /proc/4242/exe ]; then
  printf '%s\\n' "${FIXTURE_EXE:-$FIXTURE/custom-install/contabase}"
else exec /usr/bin/readlink "$@"; fi
''')
        self.stub('curl', '''
case "$*" in
  *api.github.com*) printf '%s' "${FIXTURE_COMMIT:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" ;;
  *127.0.0.1*)
    if [ -n "${FIXTURE_HEALTH+x}" ]; then printf '%s' "$FIXTURE_HEALTH"
    else printf '{"status":"healthy"}'; fi ;;
  *) exit 1 ;;
esac
''')
        self.stub('sleep', ':\n')
        # install(1) privilege changes are simulated; copying/chmod remain real.
        self.stub('install', '''
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o|-g) shift 2 ;;
    -m) mode="$2"; shift 2 ;;
    -d) directory=1; shift ;;
    *) break ;;
  esac
done
if [ "${directory:-0}" = 1 ]; then mkdir -p "$@"; chmod "${mode:-755}" "$@"
else cp "$1" "$2"; chmod "${mode:-644}" "$2"; fi
''')
        self.seed()
        self.sentinels = {p: digest(p) for p in self.data.iterdir() if p.is_file()}
        self.unit_before = self.unit.read_bytes()
        self.state_before = self.state.read_bytes()

    def stub(self, name, body):
        path = self.bin / name
        path.write_text('#!/bin/bash\nset -eu\n' + body)
        path.chmod(0o755)

    def seed(self, mode='local', bootstrap=True):
        (self.install / 'contabase').write_bytes(b'old-binary')
        (self.install / 'VERSION').write_text(OLD + '\n')
        base = {'local': 'http://localhost:18080', 'lan': 'http://192.168.1.2:18080',
                'proxy': 'https://finance.example.invalid'}[mode]
        host = {'local': 'localhost,127.0.0.1,::1', 'lan': '192.168.1.2',
                'proxy': 'finance.example.invalid'}[mode]
        self.env_file.write_text(
            f'AUTH_ENCRYPTION_KEY={SECRET}\nSECURITY_MASTER_KEY={SECRET}\n'
            f'APP_BASE_URL={base}\nALLOWED_HOSTS={host}\n'
            f'TRUSTED_PROXIES={"127.0.0.1/32" if mode == "proxy" else ""}\n'
            f'CONTABASE_ACCESS_MODE={mode}\nPORT=18080\nDATA_DIR={self.data}\n'
            f'DB_FILE={self.data}/contabase.db\nUPLOADS_DIR={self.data}/uploads\n'
            f'VERSION={OLD}\nCONTABASE_TIMEZONE=America/Sao_Paulo\n')
        self.env_file.chmod(0o600)
        fields = {'CONTABASE_STATE_VERSION': '1', 'CONTABASE_INSTALL_METHOD': 'release',
                  'CONTABASE_CHANNEL': 'beta', 'CONTABASE_INSTALLED_VERSION': OLD,
                  'CONTABASE_REPO_PATH': ''}
        if bootstrap:
            fields.update({'CONTABASE_BOOTSTRAP_CONTRACT_VERSION': '1',
                           'CONTABASE_BOOTSTRAP_ADAPTER': 'release', 'CONTABASE_COMMIT': 'b' * 40,
                           'CONTABASE_ARTIFACT_NAME': 'contabase-linux-arm64.tar.gz',
                           'CONTABASE_ARTIFACT_PLATFORM': 'linux/arm64',
                           'CONTABASE_ARTIFACT_SHA256': 'b' * 64,
                           'CONTABASE_CHECKSUMS_SHA256': 'c' * 64,
                           'CONTABASE_HELPER_SHA256': 'd' * 64,
                           'CONTABASE_NETWORK_PROFILE': mode, 'CONTABASE_ACCESS_MODE': mode,
                           'CONTABASE_HOST_BIND': '192.168.1.2' if mode == 'lan' else '127.0.0.1',
                           'CONTABASE_PORT': '18080', 'CONTABASE_INSTALL_DIR': str(self.install),
                           'CONTABASE_DATA_DIR': str(self.data), 'CONTABASE_CONFIG_DIR': str(self.config),
                           'CONTABASE_UPDATE_SUPPORT': 'legacy-release',
                           'CONTABASE_CUSTOM_PROVENANCE': 'fixture-admission-1'})
        self.state.write_text(''.join(f'{key}={value}\n' for key, value in fields.items()))
        allow = 'localhost'
        if mode == 'lan':
            allow += ' 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16'
        filters = f'IPAddressDeny=any\nIPAddressAllow={allow}\n' if bootstrap else ''
        self.unit.write_text(
            '[Unit]\nDescription=Synthetic ContaBase\n[Service]\nType=simple\n'
            f'User=contabase\nGroup=contabase\nWorkingDirectory={self.install}\n'
            f'EnvironmentFile={self.env_file}\nExecStart={self.install}/contabase\n'
            f'NoNewPrivileges=true\nPrivateTmp=true\nProtectHome=true\nProtectSystem=strict\n'
            f'ReadWritePaths={self.data}\n{filters}RestrictSUIDSGID=true\n'
            'CapabilityBoundingSet=\n[Install]\nWantedBy=multi-user.target\n')

    def replace_state(self, key, value):
        lines = self.state.read_text().splitlines()
        self.state.write_text('\n'.join(f'{key}={value}' if line.startswith(key + '=') else line
                                        for line in lines) + '\n')

    def shell(self, body, setup='', updater=True):
        source = (UPDATER if updater else WRAPPER).read_text().rsplit('\nmain', 1)[0]
        # Only the Linux/root trust boundary is simulated, never parser/path logic.
        boundary = '''
recovery_admin_path() {
  local path="$1" mode
  while [[ "$path" == "$FIXTURE"* ]]; do
    [ -e "$path" ] && [ ! -L "$path" ] || return 1
    mode="$(stat -c %a "$path" 2>/dev/null)" || mode="$(stat -f %Lp "$path" 2>/dev/null)" || return 1
    (( (8#$mode & 0022) == 0 )) || return 1
    [ "$path" != "$FIXTURE" ] || break
    path="$(dirname "$path")"
  done
}
compute_sha256() {
  local file="$1"
  case "$file" in /proc/*/exe) file="$FIXTURE/process-binary" ;; esac
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$file" | awk '{print $1}'
  else shasum -a 256 "$file" | awk '{print $1}'; fi
}
check_platform() { :; }
require_commands() { :; }
stop_service() { systemctl stop contabase; }
create_canonical_backup() { printf 'backup-boundary\\n' >> "$FIXTURE/events.log"; }
record_release_recovery() { :; }
backfill_update_command() { printf 'wrapper-boundary\\n' >> "$FIXTURE/events.log"; }
fail_after_new_runtime() { systemctl stop contabase; fail "$1"; }
download_and_validate_bundle() {
  TMP_DIR="$FIXTURE/work"
  mkdir -p "$TMP_DIR"
  EXTRACT_DIR="$FIXTURE/extract"
  printf 'artifact-fixture' > "$TMP_DIR/$ARTIFACT_NAME"
  printf 'checksum-fixture' > "$TMP_DIR/checksums.txt"
  CANONICAL_RESTORE_HELPER="$FIXTURE/restore-fixture"
  printf 'synthetic-restore' > "$CANONICAL_RESTORE_HELPER"
}
ARCH=arm64
detect_arch() { ARCH=arm64; }
ASSUME_YES=1
HEALTHCHECK_ATTEMPTS=1
'''
        script = self.root / 'harness.sh'
        script.write_text(source + '\n' + (boundary if updater else '') + '\n' + setup + '\n' + body)
        result = subprocess.run(['/bin/bash', str(script)], env=self.env, cwd=self.root,
                                capture_output=True, timeout=20)
        self.assertNotIn(SECRET.encode(), result.stdout + result.stderr, 'secret in output')
        for path, expected in self.sentinels.items():
            self.assertEqual(digest(path), expected, 'persistent sentinel changed')
        return result

    def preflight(self, setup=''):
        return self.shell('resolve_release_installation\nvalidate_existing_env\n'
                          'validate_numeric_settings\nvalidate_release_installation_contract\n', setup)

    def update(self, setup=''):
        # Main/perform_update and the real swap/env/state functions are exercised.
        return self.shell('main\n', setup)

    def assert_ok(self, result):
        self.assertEqual(result.returncode, 0, result.stderr.decode()[:500])

    def assert_blocked(self, result):
        self.assertNotEqual(result.returncode, 0, 'unsafe update accepted')
        self.assertNotIn(b'Atualizacao concluida:', result.stdout)

    def test_legacy_without_local_wrapper_fails_before_stop_or_backup(self):
        (self.root / 'lib/contabase-update-wrapper.sh').unlink()
        result = self.update()
        self.assert_blocked(result)
        events = self.log.read_text() if self.log.exists() else ''
        self.assertNotIn('stop', events)
        self.assertNotIn('backup-boundary', events)
        self.assertNotIn('restart', events)
        self.assertIn(b'bootstrap', result.stderr.lower())
        self.assertIn(b'channels/beta.json', result.stderr.lower())

    def test_custom_bootstrap_complete_update(self):
        self.assert_ok(self.update())
        self.assertEqual(self.unit.read_bytes(), self.unit_before)
        fields = dict(line.split('=', 1) for line in self.state.read_text().splitlines())
        old_fields = dict(line.split('=', 1) for line in self.state_before.decode().splitlines())
        for key in ('CONTABASE_CUSTOM_PROVENANCE', 'CONTABASE_HELPER_SHA256',
                    'CONTABASE_INSTALL_DIR', 'CONTABASE_CONFIG_DIR', 'CONTABASE_DATA_DIR',
                    'CONTABASE_NETWORK_PROFILE', 'CONTABASE_CHANNEL', 'CONTABASE_UPDATE_SUPPORT'):
            self.assertEqual(fields[key], old_fields[key])
        self.assertEqual(fields['CONTABASE_INSTALLED_VERSION'], NEW)
        self.assertEqual(fields['CONTABASE_COMMIT'], SHA)
        self.assertEqual(fields['CONTABASE_ORIGIN_COMMIT'], 'b' * 40)
        self.assertEqual(fields['CONTABASE_ORIGIN_INSTALLED_VERSION'], OLD)
        self.assertNotIn(SECRET, self.state.read_text())
        self.assertEqual(self.state.stat().st_mode & 0o777, 0o600)
        self.assertEqual(list(self.config.glob('.install-state.*')), [])
        self.assertIn('ownership root:root', self.log.read_text())
        self.assertEqual((self.install / 'contabase').read_bytes(), b'candidate-binary')

    def test_profiles_preserve_unit_and_network_config(self):
        for mode in ('local', 'lan', 'proxy'):
            with self.subTest(mode=mode):
                self.seed(mode)
                unit = self.unit.read_bytes()
                self.assert_ok(self.update())
                self.assertEqual(self.unit.read_bytes(), unit)
                self.assertIn(f'CONTABASE_ACCESS_MODE={mode}', self.env_file.read_text())

    def test_legacy_state_and_custom_unit_preserved(self):
        self.seed(bootstrap=False)
        self.assert_ok(self.update())
        self.assertIn('RestrictSUIDSGID=true', self.unit.read_text())
        self.assertNotIn('CONTABASE_BOOTSTRAP', self.state.read_text())

    def test_legacy_default_paths_contract(self):
        # Root filesystem paths are compared without creating /opt, /etc or /var.
        self.assert_ok(self.shell('''
INSTALL_DIR=/opt/contabase; CONFIG_DIR=/etc/contabase; DATA_DIR=/var/lib/contabase
validate_numeric_settings
[ "$INSTALL_DIR" = /opt/contabase ] && [ "$ENV_FILE" = /etc/contabase/contabase.env ]
'''))

    def test_legacy_quoted_state(self):
        self.seed(bootstrap=False)
        self.replace_state('CONTABASE_INSTALLED_VERSION', "'" + OLD + "'")
        self.assert_ok(self.update())

    def test_binary_method_preserved(self):
        self.seed(bootstrap=False)
        self.replace_state('CONTABASE_INSTALL_METHOD', 'binary')
        self.assert_ok(self.update())
        self.assertIn('CONTABASE_INSTALL_METHOD=binary', self.state.read_text())

    def test_missing_bootstrap_state_blocked(self):
        self.state.unlink()
        self.assert_blocked(self.preflight())

    def test_missing_legacy_state_requires_mode_proof(self):
        self.seed(bootstrap=False)
        self.state.unlink()
        (self.config / 'install-mode').write_text('release\n')
        self.assert_ok(self.update())

    def test_conflicting_paths(self):
        self.replace_state('CONTABASE_INSTALL_DIR', str(self.data))
        self.assert_blocked(self.preflight())

    def test_overlapping_paths(self):
        self.replace_state('CONTABASE_INSTALL_DIR', str(self.data / 'nested'))
        self.assert_blocked(self.preflight())

    def test_path_override_conflict(self):
        self.env['CONTABASE_INSTALL_DIR'] = str(self.root / 'parallel')
        self.assert_blocked(self.preflight())

    def test_state_path_override_conflict(self):
        self.env['CONTABASE_STATE_FILE'] = str(self.root / 'parallel-state')
        self.assert_blocked(self.preflight())

    def test_symlink_install_and_ancestor(self):
        shutil.move(str(self.install), str(self.root / 'other'))
        self.install.symlink_to(self.root / 'other', target_is_directory=True)
        self.assert_blocked(self.preflight())

    def test_symlink_state(self):
        self.state.rename(self.config / 'other-state')
        self.state.symlink_to(self.config / 'other-state')
        self.assert_blocked(self.preflight())

    def test_tampered_state(self):
        with self.state.open('a') as stream:
            stream.write('CONTABASE_CHANNEL=stable\n')
        self.assert_blocked(self.preflight())

    def test_binary_state_data_rejected(self):
        self.state.write_bytes(self.state.read_bytes() + b'CONTABASE_CUSTOM=bad\0data\n')
        self.assert_blocked(self.preflight())

    def test_state_line_without_assignment_rejected(self):
        with self.state.open('a') as stream:
            stream.write('CONTABASE_MALFORMED\n')
        self.assert_blocked(self.preflight())

    def test_secret_field_rejected(self):
        with self.state.open('a') as stream:
            stream.write('CONTABASE_SETUP_TOKEN=' + SECRET + '\n')
        self.assert_blocked(self.preflight())

    def test_state_shell_payload_rejected_without_execution(self):
        self.replace_state('CONTABASE_CUSTOM_PROVENANCE', '$(touch ' + str(self.root / 'executed') + ')')
        self.assert_blocked(self.preflight())
        self.assertFalse((self.root / 'executed').exists())

    def test_bootstrap_incomplete_rejects_legacy_fallback(self):
        self.state.write_text(self.state.read_text().replace('CONTABASE_BOOTSTRAP_CONTRACT_VERSION=1\n', ''))
        self.assert_blocked(self.preflight())

    def test_state_and_env_data_diverge(self):
        self.env_file.write_text(self.env_file.read_text().replace('DATA_DIR=' + str(self.data), 'DATA_DIR=' + str(self.config)))
        self.assert_blocked(self.preflight())

    def test_unit_missing_filter(self):
        self.unit.write_text(self.unit.read_text().replace('IPAddressDeny=any\n', ''))
        self.assert_blocked(self.preflight())

    def test_unit_network_broadened(self):
        self.unit.write_text(self.unit.read_text().replace('IPAddressAllow=localhost', 'IPAddressAllow=any'))
        self.assert_blocked(self.preflight())

    def test_lan_public_subnet_rejected(self):
        self.seed('lan')
        self.unit.write_text(self.unit.read_text().replace('10.0.0.0/8', '10.0.0.0/0'))
        self.assert_blocked(self.preflight())

    def test_lan_narrower_filter_preserved(self):
        self.seed('lan')
        self.unit.write_text(self.unit.read_text().replace('localhost 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16', 'localhost 192.168.1.0/24'))
        before = self.unit.read_bytes()
        self.assert_ok(self.update())
        self.assertEqual(before, self.unit.read_bytes())

    def test_duplicate_unit_directive_rejected(self):
        self.unit.write_text(self.unit.read_text().replace('IPAddressAllow=localhost', 'IPAddressAllow=localhost\n IPAddressAllow = any'))
        self.assert_blocked(self.preflight())

    def test_legacy_unit_include_rejected(self):
        self.unit.write_text(self.unit.read_text() + '.include /synthetic/override.conf\n')
        self.assert_blocked(self.preflight())

    def test_dropins_blocked_without_removal(self):
        self.env['FIXTURE_DROPINS'] = '/synthetic/hardening.conf'
        self.assert_blocked(self.preflight())

    def test_daemon_reload_required_blocks(self):
        self.env['FIXTURE_RELOAD'] = 'yes'
        self.assert_blocked(self.preflight())

    def test_healthcheck_failure_preserves_state(self):
        self.env['FIXTURE_HEALTH'] = 'unhealthy'
        self.assert_blocked(self.update())
        self.assertEqual(self.state.read_bytes(), self.state_before)
        self.assertIn('stop', self.log.read_text())

    def test_start_failure_preserves_state(self):
        self.env['FIXTURE_START_FAIL'] = '1'
        self.assert_blocked(self.update())
        self.assertEqual(self.state.read_bytes(), self.state_before)

    def test_old_process_path_rejected(self):
        self.env['FIXTURE_EXE'] = str(self.root / 'old-install/contabase')
        self.assert_blocked(self.update())
        self.assertEqual(self.state.read_bytes(), self.state_before)

    def test_active_binary_digest_mismatch_rejected(self):
        self.env['FIXTURE_BINARY_MISMATCH'] = '1'
        self.assert_blocked(self.update())
        self.assertEqual(self.state.read_bytes(), self.state_before)

    def test_state_rename_failure_is_atomic_and_unsuccessful(self):
        self.stub('mv', '''
for arg in "$@"; do
  case "$arg" in "$FIXTURE/custom-config/install-state.env") exit 73 ;; esac
done
exec /bin/mv "$@"
''')
        result = self.update()
        self.assert_blocked(result)
        self.assertIn(b'nao foi possivel persistir o estado', result.stderr)
        self.assertEqual(self.state.read_bytes(), self.state_before)
        self.assertEqual(list(self.config.glob('.install-state.*')), [])

    def test_state_chmod_failure_does_not_replace_state(self):
        self.stub('chmod', '''
case "$*" in *'.install-state.'*) exit 73 ;; esac
exec /bin/chmod "$@"
''')
        result = self.update()
        self.assert_blocked(result)
        self.assertIn(b'nao foi possivel persistir o estado', result.stderr)
        self.assertEqual(self.state.read_bytes(), self.state_before)

    def test_downgrade_no_mutation(self):
        self.env['CONTABASE_VERSION'] = 'v1.0.0-beta.6'
        self.assert_blocked(self.update())
        self.assertFalse(self.log.exists())
        self.assertEqual(self.state.read_bytes(), self.state_before)

    def test_same_version_no_mutation(self):
        self.env['CONTABASE_VERSION'] = OLD
        self.assert_ok(self.update())
        self.assertFalse(self.log.exists())
        self.assertEqual(self.state.read_bytes(), self.state_before)

    def test_commit_resolution_rejects_invalid_response(self):
        self.env['FIXTURE_COMMIT'] = 'not-a-commit'
        self.assert_blocked(self.update())
        self.assertFalse(self.log.exists())

    def test_no_active_systemd_pid_rejected(self):
        self.env['FIXTURE_PID'] = '0'
        self.assert_blocked(self.update())
        self.assertEqual(self.state.read_bytes(), self.state_before)

    def test_duplicate_env_key_rejected(self):
        with self.env_file.open('a') as stream:
            stream.write('PORT=8080\n')
        self.assert_blocked(self.preflight())

    def test_url_port_conflict_rejected(self):
        self.env_file.write_text(self.env_file.read_text().replace('localhost:18080', 'localhost:8080'))
        self.assert_blocked(self.preflight())

    def test_proxy_universal_network_rejected(self):
        self.seed('proxy')
        self.env_file.write_text(self.env_file.read_text().replace('127.0.0.1/32', '0.0.0.0/0'))
        self.assert_blocked(self.preflight())

    def test_state_changed_during_update_rejected(self):
        result = self.update('backfill_update_command() { printf "CONTABASE_CONCURRENT_CHANGE=yes\\n" >> "$STATE_FILE"; }')
        self.assert_blocked(result)
        self.assertIn(b'nao foi possivel persistir o estado', result.stderr)

    def test_original_provenance_is_not_overwritten(self):
        with self.state.open('a') as stream:
            stream.write('CONTABASE_ORIGIN_COMMIT=' + 'e' * 40 + '\n')
        self.assert_ok(self.update())
        self.assertIn('CONTABASE_ORIGIN_COMMIT=' + 'e' * 40, self.state.read_text())

    def test_unsafe_ownership_mode_rejected(self):
        self.config.chmod(0o777)
        self.assert_blocked(self.preflight())

    def test_symlink_in_ancestor_rejected(self):
        link = self.root / 'linked-parent'
        link.symlink_to(self.root, target_is_directory=True)
        self.replace_state('CONTABASE_INSTALL_DIR', str(link / self.install.name))
        self.assert_blocked(self.preflight())

    def test_wrapper_reads_preserved_state(self):
        self.assert_ok(self.update())
        self.env['CONTABASE_STATE_FILE'] = str(self.state)
        self.assert_ok(self.shell('load_state\n[ "$SAVED_METHOD:$SAVED_CHANNEL:$SAVED_VERSION" = "release:beta:' + NEW + '" ]\n', updater=False))

    def test_wrapper_discovers_custom_configuration(self):
        self.assert_ok(self.shell('load_state\n[ "$STATE_FILE" = "$FIXTURE/custom-config/install-state.env" ]\n',
                                  'STATE_FILE="$FIXTURE/no-default-state"\n', updater=False))

    def test_backup_restore_and_recovery_implementations_untouched(self):
        baseline = subprocess.check_output(['git', 'show', 'HEAD:scripts/update-contabase-release.sh'], cwd=ROOT).decode()
        current = UPDATER.read_text()
        for name in ('create_canonical_backup', 'validate_backup_package', 'rollback_bundle',
                     'recovery_bundle_identity', 'download_and_validate_ops_helpers',
                     'validate_checksum', 'validate_tar_entries', 'validate_extracted_bundle'):
            start = name + ('() (' if name == 'recovery_bundle_identity' else '() {')
            self.assertEqual(baseline.split(start, 1)[1].split('\n}', 1)[0],
                             current.split(start, 1)[1].split('\n}', 1)[0], name)


if __name__ == '__main__':
    unittest.main()

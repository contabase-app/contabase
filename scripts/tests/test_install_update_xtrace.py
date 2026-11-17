#!/usr/bin/env python3
"""P2-N1: synthetic secret paths only; no installer/update entrypoint runs."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
KEYS = ('AUTH_ENCRYPTION_KEY', 'SECURITY_MASTER_KEY', 'CONTABASE_SETUP_TOKEN')
EXISTING = dict(zip(KEYS, ('P2N1-Existing-Auth', 'a1' * 16, 'P2N1-Existing-Setup')))
GENERATED = dict(zip(KEYS, ('P2N1-Generated-Auth', 'b2' * 16, 'P2N1-Generated-Setup')))
PATHS = {
    'install-docker': 'scripts/install-contabase-docker.sh',
    'install-source': 'scripts/install-contabase-source.sh',
    'update-docker': 'scripts/update-contabase-docker.sh',
    'update-release': 'scripts/update-contabase-release.sh',
    'install-release': 'scripts/install-contabase-release.sh',
}


def prologue(text):
    lines = []
    for line in text.splitlines(keepends=True):
        if line.strip() and not line.startswith(('#', 'set ')):
            break
        lines.append(line)
    return ''.join(lines)


def function(text, name):
    start = text.index('\n' + name + '() {') + 1
    end = text.index('\n}', start) + 2
    return text[start:end] + '\n'


class InstallUpdateXtrace(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='contabase-p2n1-test-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        (self.root / 'home').mkdir()
        self.env = {
            'PATH': str(self.bin) + ':/usr/bin:/bin:/usr/sbin:/sbin',
            'HOME': str(self.root / 'home'), 'TMPDIR': str(self.root), 'LC_ALL': 'C',
            'ENV_FILE': str(self.root / 'synthetic.env'),
            'TRACE_LOG': str(self.root / 'trace.log'),
            'FIXTURE_SCRIPT': str(self.root / 'fixture.sh'),
        }
        openssl = self.bin / 'openssl'
        openssl.write_text('''#!/bin/sh
case "$*" in
  'rand -base64 48') printf '%s' 'P2N1-Generated-Setup' ;;
  'rand -base64 32') printf '%s' 'P2N1-Generated-Auth' ;;
  'rand -hex 16') printf '%s' '__MASTER__' ;;
  *) exit 82 ;;
esac
'''.replace('__MASTER__', GENERATED['SECURITY_MASTER_KEY']))
        openssl.chmod(0o755)

    def run_path(self, kind, mode, state='existing'):
        text = (ROOT / PATHS[kind]).read_text()
        Path(self.env['TRACE_LOG']).unlink(missing_ok=True)
        env_file = Path(self.env['ENV_FILE'])
        values = dict(EXISTING)
        if state == 'missing':
            values.pop('SECURITY_MASTER_KEY')
        if state == 'new':
            values = {}
        contents = ''.join(f'{key}={value}\n' for key, value in values.items())
        contents += 'APP_BASE_URL=http://localhost:18080\nALLOWED_HOSTS=localhost,127.0.0.1,::1\nCONTABASE_ACCESS_MODE=local\nPORT=18080\n'
        env_file.write_text(contents)
        env_file.chmod(0o600)
        names = []
        setup = 'INTERACTIVE=false\n'
        body = ''
        if kind == 'install-source':
            names = ['generate_setup_token', 'generate_auth_key', 'generate_master_key',
                     'get_env_value', 'normalize_env_value', 'is_invalid_secret_value',
                     'set_secret_value_if_missing_or_invalid']
            for key, generator in zip(KEYS, ('generate_auth_key', 'generate_master_key', 'generate_setup_token')):
                body += f'set_secret_value_if_missing_or_invalid {key} "$({generator})"\n'
        else:
            names = ['env_load_value', 'env_is_placeholder', 'env_key_is_set']
            if kind == 'install-docker':
                names += ['say', 'random_base64', 'random_hex', 'set_env_var', 'ensure_secret', 'env_validate_secrets']
                for key, generator in zip(KEYS, ('auth-key', 'master-key', 'setup-token')):
                    body += f'ensure_secret {key} {generator} "$ENV_FILE" {"true" if state == "new" else "false"}\n'
                body += 'env_validate_secrets "$ENV_FILE"\n'
            elif kind == 'update-docker':
                names += ['validate_env_secrets']
                body = 'validate_env_secrets "$ENV_FILE"\n'
            elif kind == 'update-release':
                names += ['say', 'host_from_base_url', 'scheme_from_base_url', 'host_is_localhost',
                          'host_is_private_ipv4', 'infer_access_mode', 'validate_access_contract_values',
                          'validate_existing_env']
                setup += 'DATA_DIR="$TMPDIR/data"\nDB_FILE=\nUPLOADS_DIR=\nPORT=18080\n'
                body = 'validate_existing_env\n'
            else:
                body = ''.join(f'env_key_is_set {key} "$ENV_FILE"\n' for key in KEYS)
        # Read actual entry guards and actual functions; skip operational top-level code.
        script = prologue(text) + ''.join(function(text, name) for name in names)
        script += '\n' + setup + body
        script += 'case $- in *x*) exit 83 ;; esac\nprintf "synthetic secret path OK\\n"\n'
        Path(self.env['FIXTURE_SCRIPT']).write_text(script)
        env = dict(self.env)
        if mode == 'inherited':
            env['SHELLOPTS'] = 'braceexpand:hashall:interactive-comments:xtrace'
        argv = ['/bin/bash'] + (['-x'] if mode == 'stderr' else []) + [env['FIXTURE_SCRIPT']]
        if mode == 'fd':
            argv = ['/bin/bash', '-c', 'exec 9>"$TRACE_LOG"; export BASH_XTRACEFD=9; exec bash -x "$FIXTURE_SCRIPT"']
        elif mode == 'sourced':
            argv = ['/bin/bash', '-c', 'set -x; . "$FIXTURE_SCRIPT"']
        result = subprocess.run(argv, env=env, cwd=self.root, stdin=subprocess.DEVNULL,
                                capture_output=True, timeout=15)
        trace = Path(env['TRACE_LOG'])
        streams = (result.stdout, result.stderr, trace.read_bytes() if trace.exists() else b'')
        return result, streams, contents, env_file.read_text()

    def assert_private(self, streams):
        for stream in streams:
            for value in (*EXISTING.values(), *GENERATED.values()):
                self.assertNotIn(value.encode(), stream, 'synthetic secret exposed')

    def test_existing_values_preserved_without_trace_leaks(self):
        for kind in PATHS:
            for mode in ('normal', 'stderr', 'inherited', 'fd', 'sourced'):
                with self.subTest(kind=kind, mode=mode):
                    result, streams, before, after = self.run_path(kind, mode)
                    self.assertEqual(result.returncode, 0, 'secret path failed')
                    self.assert_private(streams)
                    self.assertEqual(after, before)
                    self.assertIn(b'synthetic secret path OK', result.stdout)

    def test_generation_and_persistence_without_trace_leaks(self):
        for kind in ('install-docker', 'install-source'):
            for mode in ('normal', 'stderr', 'inherited', 'fd', 'sourced'):
                with self.subTest(kind=kind, mode=mode):
                    result, streams, _, after = self.run_path(kind, mode, 'new')
                    self.assertEqual(result.returncode, 0, 'generation failed')
                    self.assert_private(streams)
                    for key, value in GENERATED.items():
                        self.assertEqual(after.count(f'{key}={value}\n'), 1)

    def test_missing_secret_keeps_actionable_diagnostic(self):
        for kind in ('install-docker', 'update-docker', 'update-release'):
            for mode in ('normal', 'stderr', 'inherited', 'fd', 'sourced'):
                with self.subTest(kind=kind, mode=mode):
                    result, streams, before, after = self.run_path(kind, mode, 'missing')
                    self.assertNotEqual(result.returncode, 0, 'missing key accepted')
                    self.assert_private(streams)
                    self.assertIn(b'SECURITY_MASTER_KEY', result.stdout + result.stderr)
                    self.assertEqual(before, after)


if __name__ == '__main__':
    unittest.main()

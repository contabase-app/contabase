#!/usr/bin/env python3
"""B8-4B: fixtures de secrets e CLI; nunca instala ou executa systemd/rede."""
import hashlib
import importlib.util
import io
import os
from pathlib import Path
import pty
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
RELEASE = ROOT / 'scripts/install-contabase-release.sh'
LIB = ROOT / 'scripts/lib/contabase-bootstrap-adapter.sh'
CHANNEL = ROOT.parent / 'contabase-canal/public/bootstrap'
VERSION = 'v1.0.0-beta.8'
GENERATED = 'SYNTHETIC-GENERATED-SETUP-SENTINEL'
EXISTING = 'SYNTHETIC-EXISTING-SETUP-SENTINEL'
SUPPLIED = 'SYNTHETIC-SUPPLIED-SETUP-SENTINEL'
AUTH = 'synthetic-auth-preserved'
MASTER = '0123456789abcdef0123456789abcdef'
TOKENS = (GENERATED, EXISTING, SUPPLIED)


def digest(data):
    return hashlib.sha256(data).hexdigest()


class ReleaseSetupSecurity(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='contabase-b84b-security-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.home = self.root / 'home'
        self.home.mkdir()
        self.config = self.root / 'config'
        self.env_file = self.config / 'contabase.env'
        self.calls = self.root / 'calls.log'
        self.env = {
            'PATH': str(self.bin) + ':/opt/homebrew/bin:/usr/local/bin:' + os.defpath,
            'HOME': str(self.home), 'TMPDIR': str(self.home), 'LC_ALL': 'C',
            'CONTABASE_VERSION': VERSION, 'CONTABASE_CHANNEL': 'beta',
            'CONTABASE_INSTALL_DIR': str(self.root / 'install'),
            'CONTABASE_DATA_DIR': str(self.root / 'data'),
            'CONTABASE_CONFIG_DIR': str(self.config),
            'APP_BASE_URL': 'http://localhost:18080',
            'ALLOWED_HOSTS': 'localhost,127.0.0.1,::1',
            'CONTABASE_ACCESS_MODE': 'local', 'CONTABASE_PORT': '18080',
            'TEST_CALLS': str(self.calls),
        }
        self.stub('chown', 'printf "chown %s\\n" "$*" >> "$TEST_CALLS"\n')
        # mktemp sem template usa o temporario do usuario no macOS; a fixture
        # reproduz TMPDIR do Linux sem permitir escrita fora de seu sandbox.
        self.stub('mktemp', '[ "$#" -ne 0 ] || set -- "$TMPDIR/env-key.XXXXXX"\nexec /usr/bin/mktemp "$@"\n')
        self.stub('openssl', '''printf 'openssl %s\n' "$*" >> "$TEST_CALLS"
case "$*" in
  'rand -base64 48') printf '%s\n' 'SYNTHETIC-GENERATED-SETUP-SENTINEL' ;;
  'rand -base64 32') printf '%s\n' 'synthetic-new-auth' ;;
  'rand -hex 16') printf '%s\n' 'fedcba9876543210fedcba9876543210' ;;
  *) exit 82 ;;
esac
''')
        self.text = RELEASE.read_text()
        marker = '\nif [ "$BOOTSTRAP_MODE" = true ]; then\n'
        self.assertTrue(marker in self.text, 'entrypoint Bootstrap nao localizado')
        self.functions = self.text.rsplit(marker, 1)[0]

    def stub(self, name, body):
        script = self.bin / name
        script.write_text('#!/bin/sh\n' + body)
        script.chmod(0o755)

    def run_shell(self, script, args=(), trace=False, interactive=False):
        argv = ['bash'] + (['-x'] if trace else []) + [str(script)] + list(args)
        if interactive:
            master, slave = pty.openpty()
            try:
                proc = subprocess.Popen(argv, stdin=slave, stdout=subprocess.PIPE,
                                        stderr=subprocess.PIPE, env=self.env, cwd=self.root)
                os.close(slave)
                slave = None
                os.write(master, b's\n')
                stdout, stderr = proc.communicate(timeout=20)
                result = subprocess.CompletedProcess(argv, proc.returncode, stdout, stderr)
            finally:
                os.close(master)
                if slave is not None:
                    os.close(slave)
        else:
            result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, env=self.env, cwd=self.root, timeout=20)
        capture = len(list(self.root.glob('capture-*.log')))
        (self.root / ('capture-' + str(capture) + '.log')).write_bytes(result.stdout + result.stderr)
        # Falhas nunca incluem conteudo capturado ou valores dos sentinelas.
        for stream in (result.stdout, result.stderr):
            self.assertTrue(all(token.encode() not in stream for token in TOKENS),
                            'token detectado em stdout/stderr/trace')
        for log in self.root.rglob('*.log'):
            self.assertTrue(all(token.encode() not in log.read_bytes() for token in TOKENS),
                            'token detectado em log sintetico')
        return result

    def run_functions(self, body='manage_env_file\n', **options):
        harness = self.root / 'functions.sh'
        harness.write_text(self.functions + '\n' + body)
        return self.run_shell(harness, **options)

    def seed_existing(self, token=EXISTING):
        self.config.mkdir(exist_ok=True)
        contents = ('AUTH_ENCRYPTION_KEY=' + AUTH + '\nSECURITY_MASTER_KEY=' + MASTER
                    + '\nCONTABASE_ACCESS_MODE=local\n')
        if token is not None:
            contents += 'CONTABASE_SETUP_TOKEN=' + token + '\n'
        self.env_file.write_text(contents)
        self.env_file.chmod(0o600)

    def value(self, key):
        lines = self.env_file.read_text().splitlines()
        values = [line.partition('=')[2] for line in lines if line.startswith(key + '=')]
        self.assertTrue(len(values) == 1, 'chave ausente ou duplicada')
        return values[0]

    def assert_protected(self):
        self.assertEqual(self.env_file.stat().st_mode & 0o777, 0o600)
        self.assertTrue(('chown root:root ' + str(self.env_file)) in self.calls.read_text(),
                        'root:root nao solicitado na fixture de ownership')

    def assert_recovery(self, result):
        self.assertEqual(result.returncode, 0, 'gestao de env falhou')
        self.assertTrue(str(self.env_file).encode() in result.stdout)
        self.assertTrue(b'como root em um terminal local autorizado' in result.stdout)
        self.assertTrue(b'/setup' in result.stdout)
        self.assert_protected()

    def test_new_generated_token_and_administrative_recovery(self):
        result = self.run_functions()
        self.assert_recovery(result)
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == GENERATED)
        self.assertEqual(list(self.config.iterdir()), [self.env_file], 'arquivo de segredo extra')
        self.assertEqual(self.calls.read_text().count('openssl rand -base64 48'), 1)

    def test_new_token_with_xtrace_has_no_output_or_log_leak(self):
        self.assert_recovery(self.run_functions(trace=True))
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == GENERATED)

    def test_bootstrap_env_generation_uses_protected_file_and_recovery(self):
        self.env['CONTABASE_BOOTSTRAP_CONTRACT_VERSION'] = '1'
        self.assert_recovery(self.run_functions(trace=True))
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == GENERATED)

    def test_legacy_supplied_token_is_preserved_and_not_reprinted(self):
        self.env['CONTABASE_SETUP_TOKEN'] = SUPPLIED
        self.assert_recovery(self.run_functions(trace=True))
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == SUPPLIED)
        self.assertTrue('openssl rand -base64 48' not in self.calls.read_text())

    def test_placeholder_environment_token_is_generated(self):
        self.env['CONTABASE_SETUP_TOKEN'] = 'CHANGE_ME_SETUP'
        self.assert_recovery(self.run_functions(trace=True))
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == GENERATED)

    def test_existing_token_and_persistent_keys_are_preserved(self):
        self.seed_existing()
        self.env['CONTABASE_SETUP_TOKEN'] = SUPPLIED
        self.assert_recovery(self.run_functions(trace=True))
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == EXISTING)
        self.assertTrue(self.value('AUTH_ENCRYPTION_KEY') == AUTH)
        self.assertTrue(self.value('SECURITY_MASTER_KEY') == MASTER)
        self.assertTrue('openssl' not in self.calls.read_text())

    def test_missing_token_is_generated_only_after_legacy_confirmation(self):
        self.seed_existing(None)
        self.assert_recovery(self.run_functions(trace=True, interactive=True))
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == GENERATED)
        self.assertTrue(self.value('AUTH_ENCRYPTION_KEY') == AUTH)
        self.assertTrue(self.value('SECURITY_MASTER_KEY') == MASTER)
        self.assertEqual(self.calls.read_text().count('openssl rand -base64 48'), 1)
        self.assert_recovery(self.run_functions(trace=True))
        self.assertEqual(self.calls.read_text().count('openssl rand -base64 48'), 1)

    def test_empty_existing_token_is_generated_after_confirmation(self):
        self.seed_existing('')
        self.assert_recovery(self.run_functions(trace=True, interactive=True))
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == GENERATED)
        self.assertTrue(self.value('AUTH_ENCRYPTION_KEY') == AUTH)
        self.assertTrue(self.value('SECURITY_MASTER_KEY') == MASTER)

    def test_placeholder_existing_token_is_generated_after_confirmation(self):
        self.seed_existing('__PREENCHA_TOKEN__')
        self.assert_recovery(self.run_functions(trace=True, interactive=True))
        self.assertTrue(self.value('CONTABASE_SETUP_TOKEN') == GENERATED)
        self.assertTrue(self.value('AUTH_ENCRYPTION_KEY') == AUTH)
        self.assertTrue(self.value('SECURITY_MASTER_KEY') == MASTER)

    def test_headless_missing_token_fails_without_regenerating(self):
        self.seed_existing(None)
        self.env['CONTABASE_ASSUME_YES'] = '1'
        before = self.env_file.read_bytes()
        result = self.run_functions(trace=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(self.env_file.read_bytes() == before)
        self.assertTrue('openssl' not in self.calls.read_text())

    def test_generation_error_has_no_token_output(self):
        self.stub('openssl', 'exit 1\n')
        result = self.run_functions(trace=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(b'falha ao gerar secret com openssl' in result.stderr)

    def helper_with_tripwire(self):
        helper = self.root / 'helper/install-contabase-release.sh'
        helper.parent.mkdir()
        shutil.copyfile(RELEASE, helper)
        library = helper.parent / 'lib/contabase-bootstrap-adapter.sh'
        library.parent.mkdir()
        library.write_text('printf "library-loaded\\n" >> "$TEST_CALLS"\nexit 88\n')
        for cmd in ('mktemp', 'install', 'systemctl', 'curl', 'mkdir', 'journalctl'):
            self.stub(cmd, 'printf "unexpected-command\\n" >> "$TEST_CALLS"\nexit 89\n')
        return helper

    def test_bootstrap_rejects_all_cli_before_library_helper_or_writes(self):
        helper = self.helper_with_tripwire()
        self.env.update(CONTABASE_BOOTSTRAP_CONTRACT_VERSION='1', CONTABASE_SETUP_TOKEN=SUPPLIED)
        data = self.root / 'data'
        data.mkdir()
        for suffix in ('', '-wal', '-shm'):
            (data / ('contabase.db' + suffix)).write_bytes(b'synthetic SQLite sentinel')
        before = {p.name: digest(p.read_bytes()) for p in data.iterdir()}
        for index, args in enumerate((('--validate-only',), ('--help',), ('-h',), ('--unexpected',),
                                      ('--',), ('',), ('--validate-only', '--help'),
                                      ('--unexpected=' + SUPPLIED,))):
            # O indice identifica o caso sem incluir seu argumento nos logs de falha.
            with self.subTest(case=index):
                result = self.run_shell(helper, args, trace=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, b'')
                self.assertTrue(b'adaptador Bootstrap nao aceita argumentos avulsos' in result.stderr)
                self.assertFalse(self.calls.exists(), 'helper/reexec/comando executado antes da rejeicao')
                self.assertFalse(self.config.exists())
                self.assertFalse((self.root / 'install').exists())
                self.assertEqual(list(self.home.iterdir()), [], 'reexec criou temporario')
        self.assertEqual({p.name: digest(p.read_bytes()) for p in data.iterdir()}, before)

    def test_invalid_or_empty_bootstrap_contract_does_not_fall_back_to_legacy(self):
        helper = self.helper_with_tripwire()
        for version in ('2', ''):
            self.env['CONTABASE_BOOTSTRAP_CONTRACT_VERSION'] = version
            result = self.run_shell(helper, ('--validate-only',), trace=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue(b'versao do contrato Bootstrap invalida' in result.stderr)
            self.assertFalse(self.calls.exists())

    def test_bootstrap_without_cli_reaches_preinstall_boundary_after_token_fix(self):
        self.env.update(CONTABASE_BOOTSTRAP_CONTRACT_VERSION='1',
                        CONTABASE_BOOTSTRAP_CHECKOUT=str(self.root),
                        CONTABASE_HELPER_SHA256='a' * 64, CONTABASE_ADAPTER_LIB_SHA256='b' * 64,
                        CONTABASE_UPDATE_WRAPPER_SHA256='c' * 64,
                        CONTABASE_ARTIFACT_PLATFORM='linux/arm64',
                        CONTABASE_ARTIFACT_NAME='contabase-linux-arm64.tar.gz')
        # Preflight simulado; interrompe antes do primeiro download/escrita de instalacao.
        body = '. "' + str(LIB) + '"\n'
        for name in ('bootstrap_verify_file_digest', 'bootstrap_validate_release_contract',
                     'bootstrap_validate_network', 'bootstrap_validate_paths',
                     'bootstrap_validate_no_existing_state', 'validate_version',
                     'validate_install_channel', 'validate_repo', 'validate_numeric_settings',
                     'require_commands', 'check_install_platform', 'validate_runtime_config_values'):
            body += name + '() { :; }\n'
        body += '''detect_arch() { ARCH=arm64; }
build_urls() { ARTIFACT_NAME=contabase-linux-arm64.tar.gz; }
confirm_install() { printf 'preinstall-boundary\n' >> "$TEST_CALLS"; exit 73; }
bootstrap_release_main
'''
        result = self.run_functions(body, trace=True)
        self.assertEqual(result.returncode, 73, 'gate antigo ainda bloqueia a fixture')
        self.assertEqual(self.calls.read_text(), 'preinstall-boundary\n')
        self.assertFalse(self.config.exists())
        self.assertFalse((self.root / 'install').exists())
        self.assertFalse((self.root / 'data').exists())

    def test_legacy_validate_only_checks_synthetic_bundle_without_installing(self):
        fixtures = self.root / 'fixtures'
        fixtures.mkdir()
        artifact = fixtures / 'contabase-linux-arm64.tar.gz'
        with tarfile.open(artifact, 'w:gz') as archive:
            for name in ('templates', 'assets'):
                member = tarfile.TarInfo('contabase/' + name)
                member.type = tarfile.DIRTYPE
                archive.addfile(member)
            for name in ('contabase', 'admin', 'VERSION', 'LICENSE',
                         'contabase.env.example', 'contabase.service.example'):
                payload = (VERSION if name == 'VERSION' else 'synthetic; never executed').encode()
                member = tarfile.TarInfo('contabase/' + name)
                member.size = len(payload)
                archive.addfile(member, io.BytesIO(payload))
        (fixtures / 'checksums.txt').write_text(digest(artifact.read_bytes()) + '  ' + artifact.name + '\n')
        for cmd in ('systemctl', 'install', 'curl'):
            self.stub(cmd, 'printf "unexpected-command\\n" >> "$TEST_CALLS"\nexit 89\n')
        self.env.update(CONTABASE_TEST_MODE='1', CONTABASE_TEST_FIXTURE_DIR=str(fixtures),
                        CONTABASE_TEST_UNAME_M='aarch64', CONTABASE_SETUP_TOKEN=SUPPLIED)
        result = self.run_shell(RELEASE, ('--validate-only',), trace=True)
        self.assertEqual(result.returncode, 0, 'validate-only legado falhou')
        self.assertTrue(b'Validacao concluida; nenhuma instalacao foi alterada.' in result.stdout)
        self.assertFalse(self.calls.exists())
        self.assertFalse(self.config.exists())
        self.assertFalse((self.root / 'install').exists())
        self.assertFalse((self.root / 'data').exists())
        self.assertEqual(list(self.home.iterdir()), [])

    def test_validate_only_does_not_offer_a_setup_token(self):
        result = self.run_functions('VALIDATE_ONLY=true\nshow_setup_token_if_needed\n', trace=True)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b'')
        self.assertFalse(self.config.exists())

    def test_bootstrap_install_state_contains_no_setup_secret(self):
        self.env.update(CONTABASE_BOOTSTRAP_CONTRACT_VERSION='1', CONTABASE_SETUP_TOKEN=SUPPLIED,
                        CONTABASE_COMMIT='a' * 40, CONTABASE_ARTIFACT_PLATFORM='linux/arm64',
                        CONTABASE_ARTIFACT_SHA256='b' * 64, CONTABASE_CHECKSUMS_SHA256='c' * 64,
                        CONTABASE_HELPER_SHA256='d' * 64)
        result = self.run_functions('''manage_env_file
install() { cp "$7" "$8"; chmod 0644 "$8"; }
write_install_state release beta "$TAG" ""
''', trace=True)
        self.assert_recovery(result)
        state = (self.config / 'install-state.env').read_bytes()
        self.assertTrue(all(token.encode() not in state for token in TOKENS),
                        'token detectado no estado sintetico')
        self.assertTrue(b'CONTABASE_SETUP_TOKEN=' not in state)


class BootstrapBoundary(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # Le o motor do canal sem altera-lo ou escrever bytecode.
        for name in ('contract', 'transport', 'engine'):
            path = CHANNEL / (name + '.py')
            if not path.is_file():
                raise RuntimeError('checkout local do Bootstrap necessario para esta regressao')
            spec = importlib.util.spec_from_file_location(name, path)
            module = importlib.util.module_from_spec(spec)
            sys.modules[name] = module
            spec.loader.exec_module(module)
            setattr(cls, name, module)

    def test_dispatch_rejects_unverified_invocation_for_every_method(self):
        for method in ('release', 'docker', 'source'):
            with self.subTest(method=method), self.assertRaises(self.contract.Rejected):
                self.engine.dispatch(None, method)

    def test_release_invocation_has_no_cli_and_no_inherited_setup_token(self):
        with tempfile.TemporaryDirectory(prefix='contabase-b84b-invocation-') as tmp:
            root = Path(tmp)
            (root / 'lib').mkdir()
            for name in ('install-contabase-release.sh', 'lib/contabase-bootstrap-adapter.sh',
                         'lib/contabase-update-wrapper.sh'):
                (root / name).write_text('# synthetic; never executed\n')
            artifact = b'synthetic artifact'
            checksums = (digest(artifact) + '  contabase-linux-arm64.tar.gz\n').encode()
            (root / 'contabase-linux-arm64.tar.gz').write_bytes(artifact)
            (root / 'checksums.txt').write_bytes(checksums)
            identity = {'version': VERSION, 'channel': 'beta', 'asset': 'contabase-linux-arm64.tar.gz',
                        'platform': 'linux/arm64', 'payload_sha256': digest(artifact),
                        'checksums_sha256': digest(checksums)}
            chosen = {'version': VERSION, 'channel': 'beta', 'commit': 'a' * 40,
                      'state': 'homologated', 'methods': {'release': {'arm64': {
                          'payload_sha256': digest(artifact), 'checksums_sha256': digest(checksums),
                          'evidence_id': 'synthetic-test-only'}}}}
            network = self.engine.NetworkConfig('local', 18080, 'http://localhost:18080',
                                               'localhost,127.0.0.1,::1', '', 'local', '127.0.0.1', 18080)
            directories = self.engine.Paths(root / 'install', root / 'data', root / 'config')
            command = self.engine.invocation(chosen, 'release', self.transport.Prepared(root, identity),
                                             network, directories, 'arm64')
            self.assertEqual(command.argv, ('bash', str(root / 'install-contabase-release.sh')))
            self.assertNotIn('CONTABASE_SETUP_TOKEN', command.environment)
            self.assertEqual(command.environment['CONTABASE_BOOTSTRAP_CONTRACT_VERSION'], '1')


if __name__ == '__main__':
    unittest.main(verbosity=2)

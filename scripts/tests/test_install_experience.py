#!/usr/bin/env python3
"""Menu real em PTY e limites de startup com comandos sinteticos, sem rede/servicos."""
import errno
import os
from pathlib import Path
import re
import select
import shutil
import subprocess
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parents[2]


def function(text, name):
    start = text.index('\n' + name + '() {') + 1
    return text[start:text.index('\n}', start) + 2] + '\n'


def tty_run(argv, env, cwd, answers=b''):
    master, slave = os.openpty()
    proc = subprocess.Popen(argv, env=env, cwd=cwd, stdin=slave, stdout=slave, stderr=slave)
    os.close(slave)
    output = bytearray()
    try:
        if answers:
            os.write(master, answers)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if select.select([master], [], [], 0.1)[0]:
                try:
                    chunk = os.read(master, 65536)
                except OSError as exc:
                    if exc.errno == errno.EIO:
                        break
                    raise
                if not chunk:
                    break
                output.extend(chunk)
        else:
            raise TimeoutError('terminal sintetico excedeu 15 segundos')
        return proc.wait(timeout=2), output.decode().replace('\r\n', '\n')
    finally:
        if proc.poll() is None:
            proc.kill()
            proc.wait()
        os.close(master)


class InstallExperience(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='contabase-experience-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.scripts = self.root / 'scripts'
        self.scripts.mkdir()
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.env = {'PATH': str(self.bin) + ':/usr/bin:/bin', 'HOME': str(self.root),
                    'TMPDIR': str(self.root), 'LC_ALL': 'C', 'CALLS': str(self.root / 'calls')}
        shutil.copyfile(ROOT / 'scripts/install.sh', self.scripts / 'install.sh')
        self.methods = {
            'docker': 'install-contabase-docker.sh', 'release': 'install-contabase-release.sh',
            'source': 'install-contabase-source.sh', 'update-docker': 'update-contabase-docker.sh',
            'update-release': 'update-contabase-release.sh', 'update-source': 'update-contabase-source.sh',
        }
        for method, script in self.methods.items():
            (self.scripts / script).write_text(
                f'printf "%s\\n" {method} >> "$CALLS"\nexit "${{LEAF_STATUS:-0}}"\n')

    def calls(self):
        path = Path(self.env['CALLS'])
        return path.read_text().splitlines() if path.exists() else []

    def menu(self, answers):
        return tty_run(['/bin/bash', str(self.scripts / 'install.sh')], self.env, self.root, answers)

    def test_menu_has_five_options_and_matches_guide(self):
        status, output = self.menu(b'5\n')
        self.assertEqual(status, 0)
        options = re.findall(r'^[1-5]\) .+', output, re.M)
        self.assertEqual(len(options), 5)
        guide = (ROOT / 'docs/instalacao.md').read_text()
        for option in options:
            self.assertIn(option, guide)
        self.assertIn('Opcao (1-5):', output)
        self.assertIn('use contabase-update.', output)
        self.assertNotIn(') Atualizar', output)
        self.assertEqual(self.calls(), [])

    def test_interactive_install_dispatch_and_manual(self):
        for answer, method in ((b'1\n', 'docker'), (b'2\n', 'release'), (b'3\n', 'source')):
            with self.subTest(method=method):
                status, _ = self.menu(answer)
                self.assertEqual(status, 0)
                self.assertEqual(self.calls()[-1], method)
        before = self.calls()
        status, output = self.menu(b'4\n')
        self.assertEqual(status, 0)
        self.assertIn('Scripts manuais disponíveis:', output)
        for script in list(self.methods.values())[:3]:
            self.assertIn('./scripts/' + script, output)
        self.assertEqual(self.calls(), before)

    def test_old_update_numbers_are_invalid_and_retry_without_dispatch(self):
        status, output = self.menu(b'6\n7\n8\n5\n')
        self.assertEqual(status, 0)
        self.assertEqual(output.count('Opcao invalida. Escolha um numero de 1 a 5.'), 3)
        self.assertEqual(self.calls(), [])

    def test_all_noninteractive_methods_and_leaf_exit_status(self):
        for method in self.methods:
            with self.subTest(method=method):
                env = dict(self.env, CONTABASE_INSTALL_METHOD=method, CONTABASE_VERSION='v1.0.0-beta.8')
                result = subprocess.run(['/bin/bash', str(self.scripts / 'install.sh')], env=env,
                                        cwd=self.root, capture_output=True, timeout=10)
                self.assertEqual(result.returncode, 0)
                self.assertEqual(self.calls()[-1], method)
                env['LEAF_STATUS'] = '23'
                result = subprocess.run(['/bin/bash', str(self.scripts / 'install.sh')], env=env,
                                        cwd=self.root, capture_output=True, timeout=10)
                self.assertEqual(result.returncode, 23)

    def test_source_bootstrap_refused_before_commands_files_or_tty_setup(self):
        source = ROOT / 'scripts/install-contabase-source.sh'
        for command in ('tput', 'id', 'mkdir', 'chmod', 'chown', 'systemctl', 'npm', 'go', 'curl'):
            stub = self.bin / command
            stub.write_text('#!/bin/bash\nprintf "unexpected command\\n" >> "$CALLS"\nexit 92\n')
            stub.chmod(0o755)
        sentinel = self.root / 'persistent-synthetic'
        sentinel.write_text('preserve me\n')
        markers = ({'CONTABASE_BOOTSTRAP_CONTRACT_VERSION': '1'},
                   {'CONTABASE_BOOTSTRAP_CONTRACT_VERSION': ''},
                   {'CONTABASE_BOOTSTRAP_CONTRACT_VERSION': 'invalid'},
                   {'CONTABASE_BOOTSTRAP_ADAPTER': 'source'},
                   {'CONTABASE_INSTALL_MODE': 'source-bootstrap'})
        before = {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        for marker in markers:
            for tty in (False, True):
                with self.subTest(marker=marker, tty=tty):
                    env = dict(self.env, **marker, CONTABASE_SETUP_TOKEN='synthetic-secret-not-for-output',
                               CONTABASE_INSTALL_DIR=str(self.root / 'never-install'),
                               CONTABASE_DATA_DIR=str(self.root / 'never-data'),
                               CONTABASE_CONFIG_DIR=str(self.root / 'never-config'))
                    if tty:
                        status, output = tty_run(['/bin/bash', '-x', str(source)], env, self.root)
                    else:
                        result = subprocess.run(['/bin/bash', '-x', str(source)], env=env, cwd=self.root,
                                                capture_output=True, text=True, timeout=10)
                        status, output = result.returncode, result.stdout + result.stderr
                    self.assertEqual(status, 1)
                    self.assertIn('Source Bootstrap indisponivel', output)
                    self.assertIn('Nenhuma alteracao foi executada.', output)
                    self.assertNotIn(env['CONTABASE_SETUP_TOKEN'], output)
        self.assertEqual(self.calls(), [])
        after = {str(p): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        self.assertEqual(before, after)
        for name in ('never-install', 'never-data', 'never-config'):
            self.assertFalse((self.root / name).exists())

    def test_source_legacy_still_reaches_help_without_bootstrap(self):
        result = subprocess.run(['/bin/bash', str(ROOT / 'scripts/install-contabase-source.sh'), '--help'],
                                env=self.env, cwd=self.root, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0)
        self.assertIn('Uso:', result.stdout)
        self.assertNotIn('indisponivel', result.stdout + result.stderr)

    def test_docker_startup_messages_and_exit_status(self):
        text = (ROOT / 'scripts/install-contabase-docker.sh').read_text()
        tail = text[text.index('\nreport_docker_startup() {'):]
        fixture = self.root / 'docker-startup.sh'
        fixture.write_text('set +x\nset -euo pipefail\n' + function(text, 'say') + function(text, 'blank') + '''
ACCESS_URL=http://localhost:8080
secure_runtime_data_files() { printf 'permissions\\n' >> "$CALLS"; }
install_update_command() { printf 'wrapper\\n' >> "$CALLS"; }
''' + tail)
        docker = self.bin / 'docker'
        docker.write_text('''#!/bin/bash
printf '%s\\n' "$*" >> "$CALLS"
case "$*" in
  'compose up -d --build') exit "${UP_STATUS:-0}" ;;
  'compose ps --all -q contabase') printf '%s' "${CONTAINER_ID-test-container}" ;;
  inspect*) [ "${INSPECT_STATUS:-0}" = 0 ] || exit 1; printf '%s' "$RUNTIME_STATE" ;;
esac
''')
        docker.chmod(0o755)
        scenarios = (
            ('running healthy', {}, 0, 'APLICACAO SAUDAVEL:'),
            ('running starting', {}, 0, 'APLICACAO INICIALIZANDO:'),
            ('running unhealthy', {}, 1, 'FALHA DE INICIALIZACAO:'),
            ('exited unhealthy', {}, 1, 'FALHA DE INICIALIZACAO:'),
            ('restarting starting', {}, 1, 'FALHA DE INICIALIZACAO:'),
            ('running none', {}, 0, 'saude da aplicacao nao confirmada.'),
            ('running healthy', {'INSPECT_STATUS': '1'}, 1, 'nao foi possivel confirmar'),
            ('running healthy', {'CONTAINER_ID': ''}, 1, 'nao foi possivel confirmar'),
            ('running healthy', {'UP_STATUS': '1'}, 1, 'docker compose up nao foi concluido.'),
        )
        for state, overrides, expected, message in scenarios:
            with self.subTest(state=state, overrides=overrides):
                Path(self.env['CALLS']).unlink(missing_ok=True)
                result = subprocess.run(['/bin/bash', str(fixture)], cwd=self.root,
                                        env=dict(self.env, RUNTIME_STATE=state, **overrides),
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, expected)
                self.assertIn(message, result.stdout)
                self.assertNotIn('CONCLUIDO:', result.stdout)
                if state != 'running healthy' or overrides:
                    self.assertNotIn('APLICACAO SAUDAVEL:', result.stdout)
                if expected:
                    self.assertNotIn('1. Acesse:', result.stdout)
                if not overrides.get('UP_STATUS'):
                    self.assertIn('permissions', self.calls())
                    self.assertEqual(self.calls()[-1], 'wrapper')
                for call in self.calls():
                    self.assertNotRegex(call, r'\b(down|restart|restore)\b')

    def test_documented_prompts_match_scripts(self):
        guides = '\n'.join((ROOT / 'docs' / name).read_text() for name in (
            'instalacao.md', 'instalacao-docker.md', 'instalacao-lxc-vps.md'))
        for script in ('install-contabase-release.sh', 'install-contabase-source.sh', 'install-contabase-docker.sh'):
            text = (ROOT / 'scripts' / script).read_text()
            for prompt in re.findall(r'read -r -p "([^"]+)"', text):
                if prompt.startswith('Versao para atualizar'):
                    continue  # wrapper legado embutido, nao e pergunta de instalacao
                prompt = prompt.strip().replace('${PORT}', '8080')
                with self.subTest(script=script, prompt=prompt):
                    self.assertIn(prompt, guides)
        self.assertNotIn('o instalador mostra um token', guides)
        self.assertNotIn('escolher a opção de atualização no menu', guides)
        self.assertNotIn('8) Sair', (ROOT / 'docs/instalacao.md').read_text())
        self.assertNotRegex(guides, r'digite `[5-8]` para atualizar')


if __name__ == '__main__':
    unittest.main(verbosity=2)

#!/usr/bin/env python3
"""Synthetic contract tests. Docker and distribution are simulated, never homologated."""

import contextlib
import copy
import fcntl
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import shlex
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace


ROOT = Path(__file__).resolve().parents[2]
MODULE = ROOT / 'scripts/lib/contabase-docker-image-update.py'
spec = importlib.util.spec_from_file_location('image_update', MODULE)
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
CID = 'a' * 64
REAL_ASSERT_NO_WRITERS = m.assert_no_writers
REAL_RUNTIME = m.Runtime


def encoded(value):
    return json.dumps(value, sort_keys=True).encode()


class SyntheticRuntime(m.Runtime):
    """A daemon substitute with observable activation and persistent state."""
    fixture = None

    def __init__(self, state, state_file):
        super().__init__(state, state_file)
        self.f = self.fixture
        self.f.runtime = self

    def model(self, file=None):
        source = (file or self.compose).read_text()
        reference = re.search(r'image: "([^"]+)"', source).group(1)
        result = copy.deepcopy(self.f.model)
        result['services']['contabase']['image'] = reference
        if self.f.failure == 'compose-change' and file:
            result['services']['contabase']['ports'][0]['published'] = '9999'
        return result

    def inspect(self, identifier, image=False, platform=None):
        identity = self.f.by_reference.get(identifier) if image else self.f.active
        manifest = {'digest': identity['platform_digest'], 'mediaType': m.MANIFEST_TYPES[0],
                    'platform': {'os': 'linux', 'architecture': 'arm64'}}
        if image:
            result = {'Id': identity['config_digest'], 'RepoDigests': [identity['reference']],
                      'Os': 'linux', 'Architecture': 'arm64',
                      'Config': {'Entrypoint': ['./server'], 'Cmd': None}}
            if self.f.image_store == 'containerd':
                if platform is None:
                    result.update(Id=identity['index_digest'], Descriptor={
                        'digest': identity['index_digest'], 'mediaType': m.INDEX_TYPES[0]})
                else:
                    if platform != 'linux/arm64':
                        raise AssertionError('Unexpected platform: ' + platform)
                    result.update(Id=identity['platform_digest'], Descriptor=manifest)
            if self.f.failure == 'pull-identity' and identifier == self.f.target['reference']:
                result['Id'] = 'sha256:' + 'f' * 64
            return result
        result = {'Id': CID, 'Image': identity['config_digest'], 'RestartCount': 0,
                  'Config': {'Image': identity['reference'], 'User': '1000:1000',
                             'Labels': {'com.docker.compose.project': 'contabase',
                                        'com.docker.compose.service': 'contabase',
                                        'com.docker.compose.project.working_dir': str(self.root),
                                        'com.docker.compose.project.config_files': str(self.compose)},
                             'Env': [k + '=' + str(v) for k, v in self.f.model['services']['contabase']['environment'].items()],
                             'Entrypoint': ['./server'], 'Cmd': None,
                             'Healthcheck': {'Test': self.f.model['services']['contabase']['healthcheck']['test']}},
                  'Mounts': [{'Type': 'bind', 'RW': True, 'Source': str(self.root / 'data'), 'Destination': '/app/data'}],
                  'HostConfig': {'PortBindings': {'8080/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '18080'}]},
                                 'RestartPolicy': {'Name': 'unless-stopped'}},
                  'NetworkSettings': {'Networks': {'contabase_default': {}}},
                  'State': {'Running': self.f.running, 'Health': {'Status': 'healthy'}}}
        if self.f.image_store == 'containerd':
            result.update(Image=identity['index_digest'], ImageManifestDescriptor=manifest)
        if identity == self.f.target:
            if self.f.failure == 'health':
                result['State']['Health']['Status'] = 'unhealthy'
            if self.f.failure == 'runtime-image':
                result['Image'] = 'sha256:' + 'f' * 64
            if self.f.failure == 'runtime-mount':
                result['Mounts'][0]['Source'] = '/wrong/data'
        return result

    def docker(self, *args):
        self.f.events.append(args)
        if args[0] == 'info':
            return b'[]'
        if args[0] == 'pull':
            if self.f.failure == 'pull':
                raise m.Blocked('pull falhou')
            return b''
        if args[0] == 'exec':
            if 'cat' in args:
                value = ('v9.0.0' if self.f.failure == 'runtime-version' and self.f.active == self.f.target
                         else self.f.active['version'])
                return (value + '\n').encode()
            return b'{"status":"healthy"}'
        if args[0] == 'ps':
            return (CID if self.f.running else '').encode()
        raise AssertionError('Unexpected Docker call: ' + repr(args))

    def compose_command(self, *args, file=None):
        self.f.events.append(('compose', *args))
        if args[0] == 'ps':
            return CID.encode()
        if args[0] == 'stop':
            if self.f.failure == 'stop':
                raise m.Blocked('parada falhou')
            self.f.running = False
            return b''
        if args[0] == 'up':
            transaction = m.document(self.pending.read_bytes())
            if transaction['startup_attempted'] is not True:
                raise AssertionError('Startup without durable marker')
            if m.load_image_state(self.state_file) != transaction['old_state']:
                raise AssertionError('State promoted before startup')
            self.f.starts += 1
            self.f.active = self.f.target
            self.f.running = True
            if self.f.failure == 'activation':
                raise m.Blocked('startup parcial')
            if self.f.failure == 'signal':
                raise InterruptedError('sinal sintetico')
            return b''
        raise AssertionError('Unexpected Compose call: ' + repr(args))

    def save(self, transaction):
        if transaction['startup_attempted'] and self.f.failure == 'marker':
            raise OSError('synthetic fsync failure')
        super().save(transaction)

    def run(self, args, timeout=180, environment=None):
        if args[0] == 'stat':
            return b'ext2/ext3\n'
        self.f.events.append(('backup',))
        if self.f.failure == 'backup':
            raise m.Blocked('backup falhou')
        return super().run(args, timeout, environment)


class UpdateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='image-update-test-')
        self.base = Path(self.temp.name).resolve()
        self.root = self.base / 'custom-install'
        self.root.mkdir(mode=0o700)
        self.config = self.base / 'config'
        self.config.mkdir(mode=0o700)
        self.state_file = self.config / 'install-state.env'
        (self.root / 'data/uploads').mkdir(parents=True)
        (self.root / 'data/uploads/sentinel').write_bytes(b'synthetic-upload')
        # Keep the connection open so committed rows remain in the WAL before backup.
        self.db = sqlite3.connect(str(self.root / 'data/contabase.db'))
        self.db.execute('pragma journal_mode=wal')
        self.db.execute('create table schema_migrations(version integer)')
        self.db.execute('insert into schema_migrations values(77)')
        self.db.execute('create table sentinel(value text)')
        self.db.execute("insert into sentinel values('committed-in-wal')")
        self.db.commit()
        self.assertGreater((self.root / 'data/contabase.db-wal').stat().st_size, 0)
        helper = self.root / 'scripts/ops/backup.sh'
        helper.parent.mkdir(parents=True)
        helper.write_bytes((ROOT / 'scripts/ops/backup.sh').read_bytes())
        self.env_bytes = b'AUTH_ENCRYPTION_KEY=synthetic-auth\nSECURITY_MASTER_KEY=synthetic-master\nCONTABASE_SETUP_TOKEN=synthetic-setup\n'
        env_file = self.root / '.env.docker'
        env_file.write_bytes(self.env_bytes)
        env_file.chmod(0o600)
        self.documents = {}
        self.current_entry, self.current = self.release('v1.0.0-beta.7', '1')
        self.target_entry, self.target = self.release('v1.0.0-beta.8', '2')
        self.by_reference = {v['reference']: v for v in (self.current, self.target)}
        self.state = {'CONTABASE_STATE_VERSION': '1', 'CONTABASE_INSTALL_METHOD': 'docker-image',
                      'CONTABASE_CHANNEL': 'beta', 'CONTABASE_INSTALLED_VERSION': self.current['version'],
                      'CONTABASE_COMMIT': self.current['commit'], 'CONTABASE_REPO_PATH': str(self.root),
                      'CONTABASE_IMAGE_REFERENCE': self.current['reference'],
                      'CONTABASE_IMAGE_INDEX_DIGEST': self.current['index_digest'],
                      'CONTABASE_IMAGE_PLATFORM': 'linux/arm64',
                      'CONTABASE_IMAGE_PLATFORM_DIGEST': self.current['platform_digest'],
                      'CONTABASE_IMAGE_PROOF_SOURCE': 'bootstrap-catalog',
                      'CONTABASE_COMPOSE_IMAGE_FILE': 'docker-compose.image.yml'}
        self.write_state(self.state)
        self.compose = self.root / 'docker-compose.image.yml'
        self.compose.write_text('services:\n  contabase:\n    image: "' + self.current['reference'] + '"\n')
        self.original_compose = self.compose.read_bytes()
        self.model = {'name': 'contabase', 'services': {'contabase': {
            'image': self.current['reference'], 'container_name': 'contabase_app', 'user': '1000:1000',
            'ports': [{'host_ip': '127.0.0.1', 'target': 8080, 'published': '18080', 'protocol': 'tcp'}],
            'environment': {'PORT': '8080', 'DATA_DIR': '/app/data', 'DB_FILE': '/app/data/contabase.db',
                            'UPLOADS_DIR': '/app/data/uploads', 'APP_BASE_URL': 'http://localhost:18080',
                            'CONTABASE_TIMEZONE': 'America/Sao_Paulo', 'AUTH_ENCRYPTION_KEY': 'synthetic-auth',
                            'SECURITY_MASTER_KEY': 'synthetic-master', 'CONTABASE_SETUP_TOKEN': 'synthetic-setup'},
            'volumes': [{'type': 'bind', 'source': str(self.root / 'data'), 'target': '/app/data'}],
            'restart': 'unless-stopped', 'networks': {'default': None},
            'healthcheck': {'test': ['CMD', 'wget', '--no-verbose', '--tries=1', '--spider', 'http://localhost:8080/health']}}},
            'networks': {'default': {'name': 'contabase_default'}}}
        self.documents[m.SITE + '/channels/beta.json'] = encoded({
            'schema_version': 1, 'channel': 'beta', 'release_type': 'beta',
            'version': self.target['version'], 'published_at': self.target_entry['published_at'],
            'commit': self.target['commit'],
            'updater_manifest_sha256': self.target_entry['updater_manifest_sha256']})
        self.catalog = {'schema_version': 1, 'releases': [self.current_entry, self.target_entry]}
        self.documents[m.SITE + '/channels/releases-v1.json'] = encoded(self.catalog)
        self.documents['https://ghcr.io/token?service=ghcr.io&scope=repository:contabase-app/contabase:pull'] = b'{"token":"synthetic-public-token"}'
        self.active = self.current
        self.running = True
        self.starts = 0
        self.events = []
        self.failure = ''
        self.image_store = 'classic'
        self.runtime = None
        SyntheticRuntime.fixture = self
        self.patches = [patch.object(m, 'ADMIN_UID', os.getuid()),
                        patch.object(m, 'fetch_official', self.fetch),
                        patch.object(m, 'Runtime', SyntheticRuntime),
                        patch.object(m.sys, 'platform', 'linux'),
                        patch.object(m.os, 'geteuid', return_value=0),
                        patch.object(m.os, 'uname', return_value=SimpleNamespace(machine='arm64')),
                        patch.object(m, 'assert_no_writers', self.no_writers),
                        patch.object(m.time, 'sleep'), patch.dict(os.environ, {}, clear=True)]
        for item in self.patches:
            item.start()
        self.output = io.StringIO()
        self.errors = io.StringIO()

    def tearDown(self):
        self.db.close()
        for item in reversed(self.patches):
            item.stop()
        self.temp.cleanup()

    def release(self, tag, digit):
        config = 'sha256:' + digit * 64
        payload = encoded({'schemaVersion': 2, 'mediaType': m.MANIFEST_TYPES[0], 'config': {'digest': config}})
        platform_digest = 'sha256:' + m.sha(payload)
        index = encoded({'schemaVersion': 2, 'mediaType': m.INDEX_TYPES[0], 'manifests': [
            {'platform': {'os': 'linux', 'architecture': 'arm64'}, 'digest': platform_digest}]})
        index_digest = 'sha256:' + m.sha(index)
        entry = {'version': tag, 'channel': 'beta', 'commit': digit * 40, 'published_at': '2026-11-01T12:00:00Z',
                 'state': 'homologated', 'methods': {'docker': {'arm64': {
                     'payload_sha256': platform_digest[7:], 'index_sha256': index_digest[7:], 'evidence_id': 'synthetic'}}},
                 'updater_manifest_sha256': digit * 64}
        identity = {'version': tag, 'channel': 'beta', 'commit': digit * 40,
                    'reference': m.REPOSITORY + '@' + index_digest, 'index_digest': index_digest,
                    'platform': 'linux/arm64', 'platform_digest': platform_digest, 'config_digest': config}
        self.documents[m.REGISTRY + index_digest] = index
        self.documents[m.REGISTRY + tag] = index
        self.documents[m.REGISTRY + platform_digest] = payload
        self.documents[m.API + '/releases/tags/' + tag] = encoded({
            'tag_name': tag, 'draft': False, 'prerelease': True, 'published_at': entry['published_at']})
        self.documents[m.API + '/commits/' + tag] = encoded({'sha': entry['commit']})
        return entry, identity

    def fetch(self, url, headers=None):
        m.allowed_url(url)
        try:
            return self.documents[url]
        except KeyError:
            raise m.Blocked('rede indisponivel sintetica') from None

    def write_state(self, state):
        self.state_file.write_text(''.join(k + '=' + v + '\n' for k, v in state.items()))

    def no_writers(self, runtime):
        if self.running or self.failure == 'writer':
            raise m.Blocked('escritor sintetico ainda ativo')

    def run_update(self, *args):
        with contextlib.redirect_stdout(self.output), contextlib.redirect_stderr(self.errors):
            return m.main(['--state-file', str(self.state_file), '--yes', *args])

    def assert_preserved(self):
        self.assertEqual(self.env_bytes, (self.root / '.env.docker').read_bytes())
        self.assertEqual(0o600, (self.root / '.env.docker').stat().st_mode & 0o777)
        self.assertEqual(b'synthetic-upload', (self.root / 'data/uploads/sentinel').read_bytes())
        self.assertEqual([('committed-in-wal',)], self.db.execute('select value from sentinel').fetchall())
        self.assertFalse((self.config / 'recovery').exists())
        for secret in ('synthetic-auth', 'synthetic-master', 'synthetic-setup'):
            self.assertNotIn(secret, self.output.getvalue() + self.errors.getvalue())

    def test_success_custom_directory_without_git_preserves_committed_wal(self):
        self.assertFalse((self.root / '.git').exists())
        self.assertEqual(0, self.run_update())
        updated = m.load_image_state(self.state_file)
        self.assertEqual('docker-image', updated['CONTABASE_INSTALL_METHOD'])
        self.assertEqual(self.target['version'], updated['CONTABASE_INSTALLED_VERSION'])
        self.assertEqual(1, self.starts)
        self.assertTrue(self.running)
        activation = next(e for e in self.events if e[:2] == ('compose', 'up'))
        self.assertIn('--no-build', activation)
        self.assertIn('never', activation)
        self.assertNotIn('--build', activation)
        self.assertFalse(self.runtime.pending.exists())
        backup = next((self.root / 'data/backups').iterdir())
        with sqlite3.connect(str(backup / 'database.sqlite')) as connection:
            self.assertEqual([('committed-in-wal',)], connection.execute('select value from sentinel').fetchall())
            self.assertEqual(('ok',), connection.execute('pragma integrity_check').fetchone())
        self.assert_preserved()

    def test_noop_requires_identity_and_does_not_start_or_backup(self):
        self.assertEqual(0, self.run_update(self.current['version']))
        self.assertEqual(0, self.starts)
        self.assertFalse(any(e[0] == 'backup' for e in self.events))
        self.assertEqual(self.original_compose, self.compose.read_bytes())
        self.assert_preserved()

    def test_containerd_update_binds_index_platform_and_running_container(self):
        self.image_store = 'containerd'
        self.assertEqual(0, self.run_update())
        self.assertEqual(1, self.starts)
        self.assertTrue(self.running)
        self.assertEqual(self.target['version'], m.load_image_state(self.state_file)['CONTABASE_INSTALLED_VERSION'])
        self.assertFalse(self.runtime.pending.exists())
        self.assert_preserved()

    def test_containerd_noop_validates_platform_without_start_or_backup(self):
        self.image_store = 'containerd'
        self.assertEqual(0, self.run_update(self.current['version']))
        self.assertEqual(0, self.starts)
        self.assertFalse(any(e[0] == 'backup' for e in self.events))
        self.assert_preserved()

    def test_containerd_local_platform_proof_refusals_precede_stop(self):
        self.image_store = 'containerd'
        original = SyntheticRuntime.inspect
        cases = [
            ('root', ('Id',), self.current['platform_digest']),
            ('root', ('RepoDigests',), []),
            ('root', ('Architecture',), 'amd64'),
            ('root', ('Descriptor',), None),
            ('root', ('Descriptor',), []),
            ('root', ('Descriptor', 'digest'), self.current['config_digest']),
            ('root', ('Descriptor', 'mediaType'), m.MANIFEST_TYPES[0]),
            ('selected', ('Id',), self.current['config_digest']),
            ('selected', ('RepoDigests',), []),
            ('selected', ('Os',), 'windows'),
            ('selected', ('Architecture',), 'amd64'),
            ('selected', ('Config', 'Entrypoint'), ['./unverified']),
            ('selected', ('Descriptor',), None),
            ('selected', ('Descriptor', 'digest'), self.current['index_digest']),
            ('selected', ('Descriptor', 'mediaType'), m.INDEX_TYPES[0]),
            ('selected', ('Descriptor', 'platform'), None),
            ('selected', ('Descriptor', 'platform'), {}),
            ('selected', ('Descriptor', 'platform', 'architecture'), 'amd64'),
        ]
        for stage, path, value in cases:
            def inspect(runtime, identifier, image=False, platform=None):
                result = original(runtime, identifier, image, platform)
                if image and (('root' if platform is None else 'selected') == stage):
                    item = result
                    for key in path[:-1]:
                        item = item[key]
                    item[path[-1]] = value
                return result
            with self.subTest(stage=stage, path=path, value=value), patch.object(SyntheticRuntime, 'inspect', inspect):
                self.assertEqual(1, self.run_update())
                self.assertTrue(self.running)
                self.assertEqual(0, self.starts)
                self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))
                self.assertFalse(self.runtime.pending.exists())
                self.assert_preserved()

    def test_containerd_target_pull_refusals_precede_activation(self):
        self.image_store = 'containerd'
        original = SyntheticRuntime.inspect
        for stage, path, value in (
                ('root', ('Id',), self.current['index_digest']),
                ('root', ('Descriptor', 'digest'), self.current['index_digest']),
                ('root', ('RepoDigests',), []),
                ('selected', ('Id',), self.current['platform_digest']),
                ('selected', ('Descriptor', 'digest'), self.current['platform_digest']),
                ('selected', ('Architecture',), 'amd64'),
                ('selected', ('Config', 'Entrypoint'), ['./unverified'])):
            def inspect(runtime, identifier, image=False, platform=None):
                result = original(runtime, identifier, image, platform)
                if image and identifier == self.target['reference'] and (('root' if platform is None else 'selected') == stage):
                    item = result
                    for key in path[:-1]:
                        item = item[key]
                    item[path[-1]] = value
                return result
            with self.subTest(stage=stage, path=path), patch.object(SyntheticRuntime, 'inspect', inspect):
                self.assertEqual(1, self.run_update())
                self.assertTrue(self.running)
                self.assertEqual(0, self.starts)
                self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))
                self.assertFalse(self.runtime.pending.exists())
                self.assert_preserved()

    def test_containerd_container_manifest_is_mandatory_and_exact(self):
        self.image_store = 'containerd'
        original = SyntheticRuntime.inspect
        valid = {'digest': self.current['platform_digest'], 'mediaType': m.MANIFEST_TYPES[0],
                 'platform': {'os': 'linux', 'architecture': 'arm64'}}
        descriptors = [None, {}, [], dict(valid, digest=self.current['index_digest']),
                       dict(valid, digest=self.current['config_digest']),
                       dict(valid, mediaType=m.INDEX_TYPES[0]), dict(valid, platform={}),
                       dict(valid, platform={'os': 'linux', 'architecture': 'amd64'})]
        for descriptor in descriptors:
            def inspect(runtime, identifier, image=False, platform=None):
                result = original(runtime, identifier, image, platform)
                if not image:
                    result['ImageManifestDescriptor'] = descriptor
                return result
            with self.subTest(descriptor=descriptor), patch.object(SyntheticRuntime, 'inspect', inspect):
                self.assertEqual(1, self.run_update())
                self.assertEqual(0, self.starts)
                self.assertTrue(self.running)
                self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))
                self.assert_preserved()

    def test_containerd_container_id_and_reference_cannot_be_mixed(self):
        self.image_store = 'containerd'
        original = SyntheticRuntime.inspect
        for path, value in ((('Image',), self.current['config_digest']),
                            (('Image',), self.target['index_digest']),
                            (('Config', 'Image'), self.target['reference']),
                            (('Config', 'Image'), m.REPOSITORY + ':' + self.current['version'])):
            def inspect(runtime, identifier, image=False, platform=None):
                result = original(runtime, identifier, image, platform)
                if not image:
                    item = result
                    for key in path[:-1]:
                        item = item[key]
                    item[path[-1]] = value
                return result
            with self.subTest(path=path, value=value), patch.object(SyntheticRuntime, 'inspect', inspect):
                self.assertEqual(1, self.run_update())
                self.assertEqual(0, self.starts)
                self.assertTrue(self.running)
                self.assert_preserved()

    def test_containerd_wrong_manifest_after_start_stops_candidate(self):
        self.image_store = 'containerd'
        original = SyntheticRuntime.inspect
        def inspect(runtime, identifier, image=False, platform=None):
            result = original(runtime, identifier, image, platform)
            if not image and self.active == self.target:
                result['ImageManifestDescriptor']['digest'] = self.current['platform_digest']
            return result
        with patch.object(SyntheticRuntime, 'inspect', inspect):
            self.assertEqual(1, self.run_update())
        self.assertEqual(1, self.starts)
        self.assertFalse(self.running)
        self.assertTrue(self.runtime.pending.exists())
        self.assertEqual(self.current['version'], m.load_image_state(self.state_file)['CONTABASE_INSTALLED_VERSION'])
        self.assert_preserved()

    def test_classic_config_id_does_not_override_conflicting_manifest(self):
        original = SyntheticRuntime.inspect
        def inspect(runtime, identifier, image=False, platform=None):
            result = original(runtime, identifier, image, platform)
            if not image:
                result['ImageManifestDescriptor'] = {'digest': self.target['platform_digest'],
                    'mediaType': m.MANIFEST_TYPES[0], 'platform': {'os': 'linux', 'architecture': 'arm64'}}
            return result
        with patch.object(SyntheticRuntime, 'inspect', inspect):
            self.assertEqual(1, self.run_update())
        self.assertTrue(self.running)
        self.assertEqual(0, self.starts)
        self.assert_preserved()

    def test_container_manifest_change_during_health_validation_is_refused(self):
        self.image_store = 'containerd'
        original = SyntheticRuntime.inspect
        reads = 0
        def inspect(runtime, identifier, image=False, platform=None):
            nonlocal reads
            result = original(runtime, identifier, image, platform)
            if not image:
                reads += 1
                if reads == 2:
                    result['ImageManifestDescriptor']['digest'] = self.target['platform_digest']
            return result
        runtime = SyntheticRuntime(self.state, self.state_file)
        with patch.object(SyntheticRuntime, 'inspect', inspect), self.assertRaises(m.Blocked):
            m.validate_current_runtime(runtime, self.current)
        self.assertEqual(2, reads)

    def test_dry_run_has_no_writes_or_pull(self):
        before = {str(p): p.read_bytes() for p in self.base.rglob('*') if p.is_file()}
        self.assertEqual(0, self.run_update('--dry-run'))
        self.assertEqual(before, {str(p): p.read_bytes() for p in self.base.rglob('*') if p.is_file()})
        self.assertFalse(any(e[0] in ('pull', 'backup') for e in self.events))

    def test_history_failure_after_state_commit_is_idempotent(self):
        original = m.atomic_write
        def fail_history(path, raw, mode=0o600):
            if Path(path).name == 'install-history.log':
                raise OSError('synthetic history failure')
            return original(path, raw, mode)
        with patch.object(m, 'atomic_write', fail_history):
            self.assertEqual(1, self.run_update())
        self.assertTrue(self.running)
        self.assertEqual(self.target['version'], m.load_image_state(self.state_file)['CONTABASE_INSTALLED_VERSION'])
        self.assertEqual(0, self.run_update())
        self.assertEqual(1, self.starts)
        self.assertEqual(1, (self.config / 'install-history.log').read_text().count('transaction='))

    def test_interruption_before_stop_preserves_current_and_closes_pending(self):
        original = SyntheticRuntime.save
        def interrupt(runtime, transaction):
            original(runtime, transaction)
            if transaction['phase'] == 'prepared':
                raise InterruptedError('synthetic pre-stop interruption')
        with patch.object(SyntheticRuntime, 'save', interrupt):
            self.assertEqual(1, self.run_update())
        self.assertTrue(self.running)
        self.assertEqual(0, self.starts)
        self.assertEqual(0, self.run_update())
        self.assertTrue(self.running)
        self.assertEqual(0, self.starts)
        self.assertFalse(self.runtime.pending.exists())

    def test_real_compose_json_and_candidate_for_all_access_profiles(self):
        docker = shutil.which('docker', path=m.SAFE_PATH)
        if not docker or subprocess.run(
                [docker, 'compose', 'version'], stdout=subprocess.PIPE, stderr=subprocess.PIPE).returncode:
            self.skipTest('Docker Compose real indisponivel; homologacao continua pendente')
        def real_model(runtime, file=None):
            result = subprocess.run([docker, 'compose', '--project-directory', str(self.root),
                                     '--project-name', 'contabase', '-f', str(file or self.compose),
                                     'config', '--format', 'json'], check=True, stdout=subprocess.PIPE,
                                    stderr=subprocess.PIPE, env={**os.environ, 'PATH': os.defpath + ':/usr/local/bin',
                                    'DOCKER_HOST': 'unix:///nonexistent'})
            return m.document(result.stdout)
        for bind, port, url, allowed, trusted, mode in (
                ('127.0.0.1', '18080', 'http://localhost:18080', 'localhost,127.0.0.1,::1', '', 'local-docker'),
                ('192.168.1.20', '18081', 'http://192.168.1.20:18081', '192.168.1.20', '', 'lan'),
                ('127.0.0.1', '18082', 'https://money.example.test', 'money.example.test', '127.0.0.1/32', 'proxy')):
            with self.subTest(mode=mode):
                (self.root / '.env.docker').write_bytes(self.env_bytes)
                subprocess.run(['bash', '-c', '. "$1"; shift; write_image_compose "$@"', '_',
                                str(ROOT / 'scripts/lib/contabase-docker-image.sh'), str(self.compose),
                                self.current['reference'], bind, port, url, allowed, trusted, mode,
                                'America/Sao_Paulo'], check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                runtime = SyntheticRuntime(self.state, self.state_file)
                with patch.object(SyntheticRuntime, 'model', real_model):
                    model = runtime.model()
                    m.validate_model(runtime, model, self.current['reference'])
                    for token_line in (b'', b'# CONTABASE_SETUP_TOKEN=retired\n', b'CONTABASE_SETUP_TOKEN=\n'):
                        (self.root / '.env.docker').write_bytes(self.env_bytes.replace(
                            b'CONTABASE_SETUP_TOKEN=synthetic-setup\n', token_line))
                        model = runtime.model()
                        m.validate_model(runtime, model, self.current['reference'])
                    candidate, changed = m.prepare_image_compose(runtime, self.current, self.target, self.compose.read_bytes())
                    try:
                        expected = copy.deepcopy(model)
                        expected['services']['contabase']['image'] = self.target['reference']
                        self.assertEqual(expected, changed)
                    finally:
                        candidate.unlink()

    def test_effective_compose_commands_are_refused(self):
        runtime = SyntheticRuntime(self.state, self.state_file)
        for key in ('command', 'entrypoint'):
            model = copy.deepcopy(self.model)
            model['services']['contabase'][key] = None
            m.validate_model(runtime, model, self.current['reference'])
            for value in ('./other', [], '', ['./server']):
                model['services']['contabase'][key] = value
                with self.subTest(key=key, value=value), self.assertRaises(m.Blocked):
                    m.validate_model(runtime, model, self.current['reference'])

    def test_real_wrapper_and_leaf_headless_confirmation(self):
        shim_dir = self.base / 'entrypoint-bin'
        shim_dir.mkdir()
        # The shim injects the existing synthetic daemon/distribution, then runs main.
        bootstrap = '''import importlib.util, os, sys
assume = os.environ.get('CONTABASE_ASSUME_YES', '')
expected_state = os.environ.get('EXPECTED_IMAGE_STATE')
forwarded = sys.argv[2:]
assert forwarded[:2] == ['-I', '-B']
args = forwarded[3:]
assert args.count('--yes') <= 1
if expected_state:
    assert args[args.index('--state-file') + 1] == expected_state
spec = importlib.util.spec_from_file_location('tests', sys.argv[1])
spec.loader.exec_module(tests := importlib.util.module_from_spec(spec))
fixture = tests.UpdateTests()
fixture.setUp()
try:
    args[args.index('--state-file') + 1] = str(fixture.state_file)
    if assume:
        os.environ['CONTABASE_ASSUME_YES'] = assume
    rc = tests.m.main(args)
    if rc == 0:
        assert fixture.starts == 1
        fixture.assert_preserved()
    else:
        assert fixture.starts == 0 and fixture.running
    sys.exit(rc)
finally:
    fixture.tearDown()
'''
        shim = shim_dir / 'python3'
        shim.write_text('#!/bin/sh\nexec ' + shlex.quote(sys.executable) + ' -I -B -c ' +
                        shlex.quote(bootstrap) + ' ' + shlex.quote(str(Path(__file__).resolve())) + ' "$@"\n')
        shim.chmod(0o755)
        for entry in ('scripts/lib/contabase-update-wrapper.sh', 'scripts/update-contabase-docker.sh'):
            for assume, flags, expected in (('1', [], 0), ('1', ['--yes'], 0), ('', ['--yes'], 0), ('0', [], 1)):
                with self.subTest(entry=entry, assume=assume, flags=flags):
                    result = subprocess.run(['bash', str(ROOT / entry), *flags], stdin=subprocess.DEVNULL,
                                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, env={
                                                'PATH': str(shim_dir) + ':' + os.defpath,
                                                'CONTABASE_STATE_FILE': str(self.state_file),
                                                'CONTABASE_ASSUME_YES': assume})
                    self.assertEqual(expected, result.returncode, result.stderr.decode())
                    self.assertNotIn(b'Atualizar para', result.stdout + result.stderr)
                    if expected == 0:
                        self.assertIn(b'Atualizacao docker-image concluida', result.stdout)

        for layout, relative in (('default-layout', 'etc/contabase/install-state.env'),
                                 ('custom-layout', "custom-config/selected 'state$(literal).env")):
            with self.subTest(installed_layout=layout):
                lib = self.root / 'scripts/lib'
                lib.mkdir(exist_ok=True)
                for name in ('contabase-update-wrapper.sh', 'contabase-docker-image-update.py'):
                    (lib / name).write_bytes((ROOT / 'scripts/lib' / name).read_bytes())
                prefix = self.base / layout / 'usr/local'
                state = self.base / layout / relative
                with patch.object(m, 'COMMAND_PREFIX', prefix):
                    m.install_image_update_command(self.root, state, state.with_name('install-history.log'),
                                                   self.state_file.read_bytes())
                result = subprocess.run(['bash', str(prefix / 'bin/contabase-update')],
                                        stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                        env={'PATH': str(shim_dir) + ':' + os.defpath,
                                             'CONTABASE_ASSUME_YES': '1', 'EXPECTED_IMAGE_STATE': str(state)})
                self.assertEqual(0, result.returncode, result.stderr.decode())
                self.assertIn(b'Atualizacao docker-image concluida', result.stdout)

    def test_placeholder_secrets_are_refused_without_stop(self):
        for key in ('AUTH_ENCRYPTION_KEY', 'SECURITY_MASTER_KEY', 'CONTABASE_SETUP_TOKEN'):
            for value in ('CHANGE_ME', 'placeholder-token', '__PREENCHA_TOKEN__', '__GERAR_COM_OPENSSL__', '   '):
                with self.subTest(key=key, value=value):
                    original = self.model['services']['contabase']['environment'][key]
                    self.model['services']['contabase']['environment'][key] = value
                    self.assertEqual(1, self.run_update())
                    self.assertTrue(self.running)
                    self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))
                    self.model['services']['contabase']['environment'][key] = original

    def test_pending_before_start_closes_only_with_unchanged_running_old_identity(self):
        original = SyntheticRuntime.save
        for phase in ('prepared', 'stopping'):
            def interrupt(runtime, transaction):
                original(runtime, transaction)
                if transaction['phase'] == phase:
                    raise InterruptedError('synthetic persistence boundary')
            with self.subTest(phase=phase), patch.object(SyntheticRuntime, 'save', interrupt):
                self.assertEqual(1, self.run_update())
            self.assertEqual(0, self.starts)
            self.running = False
            self.assertEqual(1, self.run_update())
            self.assertTrue(self.runtime.pending.exists())
            self.running = True
            self.model['services']['contabase']['environment']['DATA_DIR'] = '/wrong'
            self.assertEqual(1, self.run_update())
            self.assertTrue(self.runtime.pending.exists())
            self.model['services']['contabase']['environment']['DATA_DIR'] = '/app/data'
            self.assertEqual(0, self.run_update())
            self.assertFalse(self.runtime.pending.exists())
            self.assertEqual(0, self.starts)

    def test_backup_uses_no_artificial_health_timeout(self):
        original = SyntheticRuntime.run
        observed = []
        def check(runtime, args, timeout=180, environment=None):
            if args[0] == '/bin/bash':
                observed.append(timeout)
            return original(runtime, args, timeout, environment)
        with patch.object(SyntheticRuntime, 'run', check):
            self.assertEqual(0, self.run_update())
        self.assertEqual([None], observed)

    def test_old_python_blocks_before_runtime_and_pending(self):
        with patch.object(m.sys, 'version_info', (3, 8, 20)):
            self.assertEqual(1, self.run_update())
        self.assertIsNone(self.runtime)
        self.assertEqual([], self.events)

    def test_preflight_missing_tools_blocks_without_pending_or_stop(self):
        which = shutil.which
        for missing in ({'sqlite3'}, {'sha256sum', 'shasum'}, {'bash'}, {'find'}):
            with self.subTest(missing=missing), patch.object(m.shutil, 'which',
                    side_effect=lambda name, path=None: None if name in missing else which(name, path=path)):
                self.assertEqual(1, self.run_update())
                self.assertFalse(self.runtime.pending.exists())
                self.assertTrue(self.running)
                self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))

    def test_preflight_database_and_backup_destination(self):
        database = self.root / 'data/contabase.db'
        saved = self.root / 'data/saved.db'
        database.rename(saved)
        for kind in ('missing', 'symlink', 'directory'):
            with self.subTest(kind=kind):
                if kind == 'symlink':
                    database.symlink_to(saved)
                elif kind == 'directory':
                    database.mkdir()
                self.assertEqual(1, self.run_update())
                self.assertTrue(self.running)
                self.assertFalse(self.runtime.pending.exists())
                if kind == 'directory':
                    database.rmdir()
                elif kind == 'symlink':
                    database.unlink()
        saved.rename(database)
        destination = self.root / 'data/backups'
        destination.symlink_to(self.base, target_is_directory=True)
        self.assertEqual(1, self.run_update())
        destination.unlink()
        destination.mkdir(mode=0o500)
        self.assertEqual(1, self.run_update())
        self.assertTrue(self.running)
        self.assertFalse(self.runtime.pending.exists())
        self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))

    def test_writer_after_backup_blocks_before_compose_exchange(self):
        original = m.create_image_backup
        def concurrent_writer(runtime, transaction, helper):
            original(runtime, transaction, helper)
            self.failure = 'writer'
        with patch.object(m, 'create_image_backup', concurrent_writer):
            self.assertEqual(1, self.run_update())
        self.assertEqual(0, self.starts)
        self.assertEqual(self.original_compose, self.compose.read_bytes())
        self.assertFalse(self.running)
        self.assert_preserved()

    def test_real_synthetic_writer_started_after_backup_is_detected(self):
        original = m.create_image_backup
        original_check = m.assert_no_writers
        writer = None
        proc = self.base / 'synthetic-proc'
        proc.mkdir()
        def concurrent_writer(runtime, transaction, helper):
            nonlocal writer
            original(runtime, transaction, helper)
            writer = subprocess.Popen([sys.executable, '-c', '''import os, sqlite3, sys
fd = os.open(sys.argv[1], os.O_RDWR)
connection = sqlite3.connect(sys.argv[1])
connection.execute('begin immediate')
connection.execute('update sentinel set value=value')
print(fd, flush=True)
sys.stdin.read()
connection.rollback()
connection.close()
os.close(fd)
''', str(self.root / 'data/contabase.db')], stdin=subprocess.PIPE,
                                      stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            fd = writer.stdout.readline().decode().strip()
            self.assertTrue(fd.isdigit())
            fds = proc / str(writer.pid) / 'fd'
            fds.mkdir(parents=True)
            (fds / fd).symlink_to(self.root / 'data/contabase.db')
        def check(runtime):
            if writer is None:
                return original_check(runtime)
            # Only the proc inventory is synthetic (macOS has no /proc).
            return REAL_ASSERT_NO_WRITERS(runtime)
        listing = Path.iterdir
        def processes(path):
            return listing(proc) if str(path) == '/proc' else listing(path)
        try:
            with patch.object(m, 'create_image_backup', concurrent_writer), \
                    patch.object(m, 'assert_no_writers', check), patch.object(Path, 'iterdir', processes):
                self.assertEqual(1, self.run_update())
        finally:
            if writer is not None:
                writer.stdin.close()
                writer.wait(timeout=10)
                writer.stdout.close()
                writer.stderr.close()
        self.assertEqual(0, self.starts)
        self.assertEqual(self.original_compose, self.compose.read_bytes())
        self.assertTrue(self.runtime.pending.exists())
        self.assert_preserved()

    def test_interruption_after_backup_preserves_pending_without_restart(self):
        original = m.create_image_backup
        def interrupt(runtime, transaction, helper):
            original(runtime, transaction, helper)
            raise InterruptedError('synthetic interruption after backup persistence')
        with patch.object(m, 'create_image_backup', interrupt):
            self.assertEqual(1, self.run_update())
        self.assertFalse(self.running)
        self.assertEqual(1, self.run_update())
        self.assertTrue(self.runtime.pending.exists())
        self.assertEqual(0, self.starts)
        self.assert_preserved()

    def test_pending_confirmed_revalidates_distribution(self):
        original = m.atomic_write
        def fail_state(path, raw, mode=0o600):
            if Path(path) == self.state_file:
                raise OSError('synthetic state failure')
            return original(path, raw, mode)
        with patch.object(m, 'atomic_write', fail_state):
            self.assertEqual(1, self.run_update())
        self.catalog['releases'][1]['state'] = 'withdrawn'
        self.documents[m.SITE + '/channels/releases-v1.json'] = encoded(self.catalog)
        self.assertEqual(1, self.run_update())
        self.assertEqual(self.current['version'], m.load_image_state(self.state_file)['CONTABASE_INSTALLED_VERSION'])
        self.assertTrue(self.runtime.pending.exists())
        self.assertEqual(1, self.starts)
        self.assertTrue(self.running)

    def test_marker_failure_leaves_durable_pre_start_state(self):
        self.failure = 'marker'
        self.assertEqual(1, self.run_update())
        self.assertFalse(m.document(self.runtime.pending.read_bytes())['startup_attempted'])
        self.assertEqual(0, self.starts)
        self.assertFalse(self.running)
        self.failure = ''
        self.assertEqual(1, self.run_update())
        self.assertTrue(self.runtime.pending.exists())
        self.assertEqual(0, self.starts)

    def test_real_writer_detector_with_synthetic_proc_and_shared_mount(self):
        runtime = SyntheticRuntime(self.state, self.state_file)
        with self.assertRaises(m.Blocked):
            REAL_ASSERT_NO_WRITERS(runtime)
        self.running = False
        proc = self.base / 'synthetic-proc/123/fd'
        proc.mkdir(parents=True)
        (proc / '9').symlink_to(self.root / 'data/contabase.db')
        original = Path.iterdir
        def listing(path):
            return iter([proc.parent]) if str(path) == '/proc' else original(path)
        with patch.object(Path, 'iterdir', listing), self.assertRaises(m.Blocked):
            REAL_ASSERT_NO_WRITERS(runtime)

    def test_initial_runtime_mount_and_env_contradictions_fail_closed(self):
        for section, key, value in (('environment', 'AUTH_ENCRYPTION_KEY', ''),
                                    ('environment', 'DATA_DIR', '/wrong'),
                                    ('ports', 'published', '19090')):
            with self.subTest(key=key):
                original = copy.deepcopy(self.model)
                if section == 'ports':
                    self.model['services']['contabase'][section][0][key] = value
                else:
                    self.model['services']['contabase'][section][key] = value
                self.assertEqual(1, self.run_update())
                self.assertEqual(0, self.starts)
                self.assertTrue(self.running)
                self.model = original

    def test_input_change_during_pull_blocks_before_stop(self):
        original = SyntheticRuntime.docker
        def change_input(runtime, *args):
            result = original(runtime, *args)
            if args[0] == 'pull':
                (self.root / '.env.docker').write_bytes(self.env_bytes + b'TIMEZONE=UTC\n')
            return result
        with patch.object(SyntheticRuntime, 'docker', change_input):
            self.assertEqual(1, self.run_update())
        self.assertTrue(self.running)
        self.assertEqual(0, self.starts)

    def test_downgrade_rejected(self):
        self.write_state(m.next_state(self.state, self.target))
        self.active = self.target
        self.compose.write_text(self.original_compose.decode().replace(self.current['reference'], self.target['reference']))
        self.assertEqual(1, self.run_update(self.current['version']))
        self.assertEqual(0, self.starts)
        self.assertTrue(self.running)

    def test_distribution_refusals_do_not_stop_current(self):
        cases = ('missing-catalog', 'invalid-catalog', 'draft', 'commit', 'index', 'platform', 'tag-image', 'withdrawn', 'same-version-digest')
        original = copy.deepcopy(self.documents)
        for case in cases:
            with self.subTest(case=case):
                self.documents = copy.deepcopy(original)
                if case == 'missing-catalog':
                    del self.documents[m.SITE + '/channels/releases-v1.json']
                elif case == 'invalid-catalog':
                    self.documents[m.SITE + '/channels/releases-v1.json'] = b'{"schema_version":1,"schema_version":1}'
                elif case == 'draft':
                    release = m.document(self.documents[m.API + '/releases/tags/' + self.target['version']])
                    release['draft'] = True
                    self.documents[m.API + '/releases/tags/' + self.target['version']] = encoded(release)
                elif case == 'commit':
                    self.documents[m.API + '/commits/' + self.target['version']] = encoded({'sha': 'f' * 40})
                elif case == 'index':
                    self.documents[m.REGISTRY + self.target['index_digest']] = b'{}'
                elif case == 'platform':
                    self.documents[m.REGISTRY + self.target['platform_digest']] = b'{}'
                elif case == 'tag-image':
                    self.documents[m.REGISTRY + self.target['version']] = b'{}'
                elif case == 'withdrawn':
                    catalog = copy.deepcopy(self.catalog)
                    catalog['releases'][1]['state'] = 'withdrawn'
                    self.documents[m.SITE + '/channels/releases-v1.json'] = encoded(catalog)
                else:
                    catalog = copy.deepcopy(self.catalog)
                    catalog['releases'][0]['methods'] = copy.deepcopy(self.target_entry['methods'])
                    self.documents[m.SITE + '/channels/releases-v1.json'] = encoded(catalog)
                args = (self.current['version'],) if case == 'same-version-digest' else ()
                self.assertEqual(1, self.run_update(*args))
                self.assertTrue(self.running)
                self.assertEqual(0, self.starts)
                self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))
                self.assert_preserved()

    def test_catalog_accepts_legacy_entry_without_updater_identity(self):
        self.catalog['releases'][0].pop('updater_manifest_sha256')
        self.documents[m.SITE + '/channels/releases-v1.json'] = encoded(self.catalog)
        m.validate_catalog(self.catalog)
        self.assertEqual(0, self.run_update())
        self.assertEqual(self.target['version'], m.load_image_state(self.state_file)['CONTABASE_INSTALLED_VERSION'])
        self.assertEqual(1, self.starts)
        self.assert_preserved()

    def test_catalog_rejects_missing_updater_identity_for_beta8_before_stop(self):
        self.catalog['releases'][1].pop('updater_manifest_sha256')
        self.documents[m.SITE + '/channels/releases-v1.json'] = encoded(self.catalog)
        self.assertEqual(1, self.run_update())
        self.assertTrue(self.running)
        self.assertEqual(0, self.starts)
        self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))
        self.assert_preserved()

    def test_manifest_identity_is_required_for_beta8(self):
        beta7 = {'version': 'v1.0.0-beta.7'}
        beta8 = {'version': 'v1.0.0-beta.8'}
        m.validate_manifest_identity(beta7)
        for data in (beta8, dict(beta8, commit='a' * 40),
                     dict(beta8, updater_manifest_sha256='b' * 64),
                     dict(beta8, commit='a' * 40, updater_manifest_sha256='B' * 64)):
            with self.subTest(data=data), self.assertRaises(m.Blocked):
                m.validate_manifest_identity(data)
        m.validate_manifest_identity(dict(beta8, commit='a' * 40,
                                          updater_manifest_sha256='b' * 64))

    def test_manifest_without_beta8_identity_blocks_before_target(self):
        document = m.document(self.documents[m.SITE + '/channels/beta.json'])
        for key in ('commit', 'updater_manifest_sha256'):
            with self.subTest(key=key):
                changed = copy.deepcopy(document)
                changed.pop(key)
                self.documents[m.SITE + '/channels/beta.json'] = encoded(changed)
                with self.assertRaises(m.Blocked):
                    m.resolve_image_target(self.state)
        self.documents[m.SITE + '/channels/beta.json'] = encoded(document)

    def test_catalog_rejects_invalid_updater_identity_before_stop(self):
        original = copy.deepcopy(self.catalog)
        for value in ('a' * 63, 'A' * 64, 1, None):
            with self.subTest(value=value):
                self.catalog = copy.deepcopy(original)
                self.catalog['releases'][1]['updater_manifest_sha256'] = value
                self.documents[m.SITE + '/channels/releases-v1.json'] = encoded(self.catalog)
                self.assertEqual(1, self.run_update())
                self.assertTrue(self.running)
                self.assertEqual(0, self.starts)
                self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))
                self.assert_preserved()

    def test_pre_activation_failures_preserve_old_runtime(self):
        for case in ('pull', 'pull-identity', 'compose-change'):
            with self.subTest(case=case):
                self.failure = case
                self.assertEqual(1, self.run_update())
                self.assertTrue(self.running)
                self.assertEqual(0, self.starts)
                self.assertEqual(self.original_compose, self.compose.read_bytes())
                self.assert_preserved()

    def test_ambiguous_state_symlink_permissions_and_duplicates(self):
        original = self.state_file.read_bytes()
        for mutation in (original + b'CONTABASE_COMMIT=' + b'1' * 40 + b'\n',
                         original.replace(b'docker-image', b'docker'),
                         original.replace(self.current['index_digest'].encode(), b'sha256:invalid'),
                         original.replace(m.REPOSITORY.encode(), b'ghcr.io/other/project'),
                         original.replace(b'CONTABASE_COMMIT=', b'UNKNOWN=')):
            self.state_file.write_bytes(mutation)
            self.assertEqual(1, self.run_update())
            self.assertEqual(0, self.starts)
        self.state_file.write_bytes(original)
        self.state_file.chmod(0o666)
        self.assertEqual(1, self.run_update())
        self.state_file.chmod(0o644)
        saved = self.config / 'saved.env'
        self.state_file.rename(saved)
        self.state_file.symlink_to(saved)
        self.assertEqual(1, self.run_update())
        self.state_file.unlink()
        saved.rename(self.state_file)

    def test_unknown_and_incompatible_arguments(self):
        for args in (['--build'], ['--force-reset'], ['--skip-pull'], ['--compose', 'x'], ['--unknown'],
                     ['--dry'], ['--channel', 'beta', '--channel', 'stable'], ['--state-file', 'x', '--state-file', 'y']):
            with contextlib.redirect_stderr(self.errors), self.assertRaises(SystemExit):
                m.arguments(args)
        self.assertEqual(1, self.run_update('--channel', 'stable'))
        with patch.dict(os.environ, {'CONTABASE_IMAGE_REFERENCE': self.target['reference']}):
            self.assertEqual(1, self.run_update())

    def test_helper_mismatch_blocks_before_stop(self):
        (self.root / 'scripts/ops/backup.sh').write_text('exit 0\n')
        self.assertEqual(1, self.run_update())
        self.assertTrue(self.running)
        self.assertFalse(any(e[:2] == ('compose', 'stop') for e in self.events))

    def test_backup_symlink_and_checksum_tampering_are_rejected(self):
        self.assertEqual(0, self.run_update())
        backup = next((self.root / 'data/backups').iterdir())
        uploads = backup / 'uploads'
        saved = self.base / 'saved-uploads'
        uploads.rename(saved)
        uploads.symlink_to(saved, target_is_directory=True)
        with self.assertRaises(m.Blocked):
            m.validate_backup(backup)
        uploads.unlink()
        saved.rename(uploads)
        (backup / 'uploads/sentinel').write_bytes(b'changed')
        with self.assertRaises(m.Blocked):
            m.validate_backup(backup)

    def test_concurrency(self):
        fd = os.open(str(self.config / 'docker-image-update.lock'), os.O_CREAT | os.O_RDWR, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.assertEqual(1, self.run_update())
            self.assertEqual([], self.events)
        finally:
            os.close(fd)

    def test_pending_unconfirmed_refuses_retry_and_never_restarts_old(self):
        self.failure = 'activation'
        self.assertEqual(1, self.run_update())
        self.assertEqual(1, self.starts)
        self.assertFalse(self.running)
        self.assertTrue(m.document(self.runtime.pending.read_bytes())['startup_attempted'])
        self.failure = ''
        self.assertEqual(1, self.run_update())
        self.assertEqual(1, self.starts)
        self.assertEqual(self.current['version'], m.load_image_state(self.state_file)['CONTABASE_INSTALLED_VERSION'])
        self.assert_preserved()

    def test_state_write_failure_revalidates_and_finalizes_without_start(self):
        original = m.atomic_write
        def fail_state(path, raw, mode=0o600):
            if Path(path) == self.state_file:
                raise OSError('synthetic state failure')
            return original(path, raw, mode)
        with patch.object(m, 'atomic_write', fail_state):
            self.assertEqual(1, self.run_update())
        self.assertTrue(self.running)
        self.assertEqual(1, self.starts)
        self.assertTrue(self.runtime.pending.exists())
        self.assertEqual(0, self.run_update())
        self.assertEqual(1, self.starts)
        self.assertEqual(self.target['version'], m.load_image_state(self.state_file)['CONTABASE_INSTALLED_VERSION'])
        self.assertEqual(1, (self.config / 'install-history.log').read_text().count('transaction='))
        self.assert_preserved()

    def test_existing_proof_is_preserved_and_never_used(self):
        directory = self.config / 'recovery'
        directory.mkdir()
        proof = directory / ('docker-' + '1' * 64 + '.proof')
        raw = b'contabase-recovery-v1\nkind=docker\nversion=v1.0.0-beta.6\nidentity=' + b'1' * 64 + b'\n'
        proof.write_bytes(raw)
        self.failure = 'activation'
        self.assertEqual(1, self.run_update())
        self.assertEqual(raw, proof.read_bytes())
        self.assertEqual(1, len(list(directory.iterdir())))
        self.assertEqual(1, self.starts)
        self.assertFalse(self.running)

    def test_backup_helper_anchor_and_no_recovery_calls(self):
        self.assertEqual(m.BACKUP_SHA256, m.sha((ROOT / 'scripts/ops/backup.sh').read_bytes()))
        source = MODULE.read_text()
        for name in ('recovery_record', 'recovery_verify', 'recovery_source_known', 'rollback_runtime'):
            self.assertNotIn(name, source)
        for url in ('http://ghcr.io/token', 'https://evil.test/x', m.REGISTRY + 'latest',
                    'https://ghcr.io/token?service=evil'):
            with self.assertRaises(m.Blocked):
                m.allowed_url(url)


def failure_test(name):
    def test(self):
        self.failure = name
        self.assertEqual(1, self.run_update())
        self.assertEqual(self.current['version'], m.load_image_state(self.state_file)['CONTABASE_INSTALLED_VERSION'])
        self.assertEqual(0 if name in ('stop', 'backup', 'writer', 'marker') else 1, self.starts)
        if name != 'stop':
            self.assertFalse(self.running)
        self.assertTrue(self.runtime.pending.exists())
        self.assert_preserved()
    return test


for failure in ('stop', 'backup', 'writer', 'marker', 'activation', 'signal', 'health',
                'runtime-image', 'runtime-mount', 'runtime-version'):
    setattr(UpdateTests, 'test_failure_' + failure.replace('-', '_'), failure_test(failure))


def optional_setup_token_test(mode):
    def test(self):
        line = b'CONTABASE_SETUP_TOKEN=synthetic-setup\n'
        replacement = {'removed': b'', 'commented': b'# CONTABASE_SETUP_TOKEN=retired\n',
                       'empty': b'CONTABASE_SETUP_TOKEN=\n'}[mode]
        self.env_bytes = self.env_bytes.replace(line, replacement)
        (self.root / '.env.docker').write_bytes(self.env_bytes)
        environment = self.model['services']['contabase']['environment']
        if mode == 'empty':
            environment['CONTABASE_SETUP_TOKEN'] = ''
        else:
            environment.pop('CONTABASE_SETUP_TOKEN')
        self.assertEqual(0, self.run_update('--dry-run'))
        self.assertEqual(0, self.run_update(self.current['version']))
        self.assertEqual(0, self.run_update())
        self.assert_preserved()
    return test


for mode in ('removed', 'commented', 'empty'):
    setattr(UpdateTests, 'test_optional_setup_token_' + mode, optional_setup_token_test(mode))


if __name__ == '__main__':
    unittest.main(verbosity=2)

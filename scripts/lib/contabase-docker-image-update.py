#!/usr/bin/env python3
"""Explicit update of Bootstrap docker-image installations. No recovery policy."""

import argparse
import copy
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import signal
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


REPOSITORY = 'ghcr.io/contabase-app/contabase'
SITE = 'https://get-contabase.pages.dev'
API = 'https://api.github.com/repos/contabase-app/contabase'
REGISTRY = 'https://ghcr.io/v2/contabase-app/contabase/manifests/'
SAFE_PATH = '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
# Reviewed canonical helper; never derive the expected value from an installation.
BACKUP_SHA256 = '6b9382927526cdd77b0d46fec7c6ff410bf003b69420a34aa080970d0956bc72'
ADMIN_UID = 0
COMMAND_PREFIX = Path('/usr/local')
STATE_KEYS = {
    'CONTABASE_STATE_VERSION', 'CONTABASE_INSTALL_METHOD', 'CONTABASE_CHANNEL',
    'CONTABASE_INSTALLED_VERSION', 'CONTABASE_COMMIT', 'CONTABASE_REPO_PATH',
    'CONTABASE_IMAGE_REFERENCE', 'CONTABASE_IMAGE_INDEX_DIGEST',
    'CONTABASE_IMAGE_PLATFORM', 'CONTABASE_IMAGE_PLATFORM_DIGEST',
    'CONTABASE_IMAGE_PROOF_SOURCE', 'CONTABASE_COMPOSE_IMAGE_FILE',
}
INDEX_TYPES = ('application/vnd.oci.image.index.v1+json',
               'application/vnd.docker.distribution.manifest.list.v2+json')
MANIFEST_TYPES = ('application/vnd.oci.image.manifest.v1+json',
                  'application/vnd.docker.distribution.manifest.v2+json')


class Blocked(Exception):
    pass


def require(condition, message):
    if not condition:
        raise Blocked(message)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def document(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, 'chave JSON duplicada')
            result[key] = value
        return result
    require(len(raw) <= 1048576, 'documento excede limite')
    try:
        return json.loads(raw, object_pairs_hook=unique,
                          parse_constant=lambda _: require(False, 'numero JSON invalido'))
    except (ValueError, TypeError, UnicodeError):
        raise Blocked('documento JSON invalido') from None


def version(value):
    require(isinstance(value, str) and len(value) <= 128, 'versao invalida')
    match = re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-beta\.(0|[1-9][0-9]*))?', value)
    require(match is not None, 'versao publica invalida')
    major, minor, patch, beta = match.groups()
    return int(major), int(minor), int(patch), beta is None, int(beta or 0)


AUTHENTICATED_FROM = version('v1.0.0-beta.8')


def requires_identity(value):
    return version(value) >= AUTHENTICATED_FROM


def channel(value):
    version(value)
    return 'beta' if '-beta.' in value else 'stable'


def digest(value):
    require(isinstance(value, str) and re.fullmatch(r'sha256:[0-9a-f]{64}', value),
            'digest invalido')
    return value


def timestamp(value):
    require(isinstance(value, str) and re.fullmatch(r'\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ', value), 'data invalida')
    try:
        datetime.datetime.strptime(value, '%Y-%m-%dT%H:%M:%SZ')
    except ValueError:
        raise Blocked('data invalida') from None


def secure_path(path, directory=False, data=False, hardlinks=False):
    path = Path(path)
    require(path.is_absolute() and str(path) == os.path.normpath(str(path)), 'caminho invalido')
    for item in (path, *path.parents):
        info = item.lstat()
        require(not stat.S_ISLNK(info.st_mode), 'symlink nao permitido')
        require(info.st_uid in ((0, ADMIN_UID, 1000) if data else (0, ADMIN_UID)), 'ownership inseguro')
        require(not info.st_mode & 0o022 or
                (stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and info.st_mode & stat.S_ISVTX),
                'permissoes inseguras')
    info = path.stat()
    require(stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode), 'tipo de arquivo invalido')
    if not directory and not hardlinks:
        require(info.st_nlink == 1, 'hardlink nao permitido')
    return path


def read_private(path, limit=1048576, data=False):
    secure_path(path, data=data)
    fd = os.open(str(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as source:
        before = os.fstat(source.fileno())
        require(stat.S_ISREG(before.st_mode) and before.st_nlink == 1, 'arquivo mudou de tipo')
        raw = source.read(limit + 1)
        after = os.fstat(source.fileno())
        current = Path(path).lstat()
        require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) ==
                (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns) and
                (after.st_dev, after.st_ino) == (current.st_dev, current.st_ino), 'arquivo mudou durante leitura')
    require(len(raw) <= limit, 'arquivo excede limite')
    return raw


def load_image_state(path):
    raw = read_private(path)
    result = {}
    for line in raw.decode('utf-8').splitlines():
        key, sep, value = line.partition('=')
        require(sep and key in STATE_KEYS and key not in result and value and
                not any(ord(c) < 32 for c in value), 'estado incompleto ou ambiguo')
        result[key] = value
    require(set(result) == STATE_KEYS, 'estado docker-image incompleto')
    require(result['CONTABASE_STATE_VERSION'] == '1' and
            result['CONTABASE_INSTALL_METHOD'] == 'docker-image', 'metodo/estado divergente')
    require(result['CONTABASE_CHANNEL'] == channel(result['CONTABASE_INSTALLED_VERSION']), 'canal divergente')
    require(re.fullmatch('[0-9a-f]{40}', result['CONTABASE_COMMIT']), 'commit invalido')
    index = digest(result['CONTABASE_IMAGE_INDEX_DIGEST'])
    digest(result['CONTABASE_IMAGE_PLATFORM_DIGEST'])
    require(result['CONTABASE_IMAGE_REFERENCE'] == REPOSITORY + '@' + index, 'origem/digest divergente')
    require(result['CONTABASE_IMAGE_PLATFORM'] in ('linux/amd64', 'linux/arm64'), 'plataforma invalida')
    require(result['CONTABASE_IMAGE_PROOF_SOURCE'] == 'bootstrap-catalog' and
            result['CONTABASE_COMPOSE_IMAGE_FILE'] == 'docker-compose.image.yml', 'contrato Bootstrap divergente')
    root = secure_path(result['CONTABASE_REPO_PATH'], directory=True)
    secure_path(root / result['CONTABASE_COMPOSE_IMAGE_FILE'])
    env = secure_path(root / '.env.docker')
    require(env.stat().st_mode & 0o077 == 0, 'ambiente deve ter modo 0600')
    secure_path(root / 'data', directory=True, data=True)
    return result


def allowed_url(url):
    p = urllib.parse.urlsplit(url)
    require(p.scheme == 'https' and not p.username and not p.password and
            p.port in (None, 443) and not p.fragment, 'origem HTTPS invalida')
    routes = {
        'get-contabase.pages.dev': r'/channels/(?:beta|stable|releases-v1)\.json',
        'api.github.com': r'/repos/contabase-app/contabase/(?:releases/tags/v[0-9A-Za-z.\-]+|commits/v[0-9A-Za-z.\-]+)',
        'ghcr.io': r'/(?:token|v2/contabase-app/contabase/manifests/(?:v[0-9A-Za-z.\-]+|sha256:[0-9a-f]{64}))',
    }
    require(p.hostname in routes and re.fullmatch(routes[p.hostname], p.path), 'origem nao autorizada')
    require(not p.query or url == 'https://ghcr.io/token?service=ghcr.io&scope=repository:contabase-app/contabase:pull',
            'query nao autorizada')


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise Blocked('redirecionamento nao autorizado')


def fetch_official(url, headers=None):
    allowed_url(url)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    request = urllib.request.Request(url, headers={'User-Agent': 'ContaBase-Docker-Update/1', **(headers or {})})
    try:
        with opener.open(request, timeout=15) as response:
            require(response.status == 200 and response.url == url, 'resposta de origem divergente')
            raw = response.read(1048577)
            require(len(raw) <= 1048576, 'download excede limite')
            return raw
    except (OSError, ValueError, urllib.error.URLError):
        raise Blocked('distribuicao indisponivel; sem fallback') from None


def validate_manifest_identity(manifest):
    require(isinstance(manifest, dict) and isinstance(manifest.get('version'), str), 'manifesto invalido')
    required = requires_identity(manifest['version'])
    has_commit = 'commit' in manifest
    has_updater = 'updater_manifest_sha256' in manifest
    if required:
        require(has_commit and has_updater, 'identidade autenticada obrigatoria no manifesto')
    else:
        require(has_commit == has_updater, 'identidade autenticada parcial no manifesto')
    if has_commit:
        require(isinstance(manifest['commit'], str) and re.fullmatch('[0-9a-f]{40}', manifest['commit']),
                'commit invalido no manifesto')
    if has_updater:
        require(isinstance(manifest['updater_manifest_sha256'], str) and
                re.fullmatch('[0-9a-f]{64}', manifest['updater_manifest_sha256']),
                'updater manifest invalido no manifesto')


def validate_catalog(data):
    require(isinstance(data, dict) and set(data) == {'schema_version', 'releases'} and
            type(data['schema_version']) is int and data['schema_version'] == 1 and
            isinstance(data['releases'], list) and len(data['releases']) <= 1000, 'catalogo invalido')
    seen = set()
    fields = {'version', 'channel', 'commit', 'published_at', 'state', 'methods'}
    for entry in data['releases']:
        require(isinstance(entry, dict) and set(entry) in (
            fields, fields | {'updater_manifest_sha256'}), 'entrada de catalogo invalida')
        require(entry['channel'] == channel(entry['version']) and entry['version'] not in seen, 'versao/canal duplicado')
        seen.add(entry['version'])
        require(isinstance(entry['commit'], str) and re.fullmatch('[0-9a-f]{40}', entry['commit']), 'commit invalido')
        timestamp(entry['published_at'])
        if requires_identity(entry['version']):
            require('updater_manifest_sha256' in entry, 'identidade autenticada obrigatoria no catalogo')
        if 'updater_manifest_sha256' in entry:
            require(isinstance(entry['updater_manifest_sha256'], str) and
                    re.fullmatch('[0-9a-f]{64}', entry['updater_manifest_sha256']), 'updater manifest invalido')
        require(entry['state'] in ('published', 'homologated', 'withdrawn'), 'admissao invalida')
        methods = entry['methods']
        require(isinstance(methods, dict) and methods and set(methods) <= {'docker', 'release', 'source'}, 'metodos invalidos')
        for method, architectures in methods.items():
            require(isinstance(architectures, dict) and architectures and set(architectures) <= {'amd64', 'arm64'}, 'arquiteturas invalidas')
            for proof in architectures.values():
                extra = {'docker': {'index_sha256'}, 'release': {'checksums_sha256'}, 'source': set()}[method]
                require(isinstance(proof, dict) and set(proof) == {'payload_sha256', 'evidence_id'} | extra, 'identidade incompleta')
                require(isinstance(proof['evidence_id'], str) and
                        re.fullmatch('[A-Za-z0-9][A-Za-z0-9._-]{0,127}', proof['evidence_id']), 'evidencia invalida')
                for key in set(proof) - {'evidence_id'}:
                    digest('sha256:' + proof[key])


def resolve_image_target(state, requested=None):
    saved_channel = state['CONTABASE_CHANNEL']
    manifest = document(fetch_official(SITE + '/channels/' + saved_channel + '.json'))
    catalog = document(fetch_official(SITE + '/channels/releases-v1.json'))
    require(isinstance(manifest, dict) and type(manifest.get('schema_version')) is int and
            manifest['schema_version'] == 1 and manifest.get('channel') == saved_channel and
            manifest.get('release_type') == saved_channel and manifest.get('version') is not None,
            'manifest/canal indisponivel')
    require(channel(manifest['version']) == saved_channel, 'manifest fora do canal')
    timestamp(manifest.get('published_at'))
    validate_manifest_identity(manifest)
    validate_catalog(catalog)
    eligible = sorted((e for e in catalog['releases'] if e['channel'] == saved_channel and
                       e['state'] == 'homologated'), key=lambda e: version(e['version']), reverse=True)
    require(eligible and eligible[0]['version'] == manifest['version'] and
            eligible[0]['published_at'] == manifest['published_at'], 'recomendacao/catalogo divergentes')
    target = requested or manifest['version']
    require(channel(target) == saved_channel, 'troca de canal recusada')
    chosen = next((e for e in eligible if e['version'] == target), None)
    arch = state['CONTABASE_IMAGE_PLATFORM'].split('/')[1]
    require(chosen is not None and arch in chosen['methods'].get('docker', {}), 'alvo nao homologado para Docker/plataforma')
    require(version(target) >= version(state['CONTABASE_INSTALLED_VERSION']), 'downgrade recusado')
    verify_release_identity(chosen)
    identity = verify_oci_identity(chosen, arch)
    if target == state['CONTABASE_INSTALLED_VERSION']:
        require(identity == state_identity(state, identity['config_digest']), 'mesma versao com identidade divergente')
    return identity


def verify_release_identity(chosen):
    release = document(fetch_official(API + '/releases/tags/' + chosen['version']))
    commit = document(fetch_official(API + '/commits/' + chosen['version']))
    require(isinstance(release, dict) and release.get('tag_name') == chosen['version'] and
            release.get('draft') is False and release.get('prerelease') is (chosen['channel'] == 'beta') and
            release.get('published_at') == chosen['published_at'] and isinstance(commit, dict) and
            commit.get('sha') == chosen['commit'], 'release/tag/commit divergente ou nao publicado')


def verify_oci_identity(chosen, arch):
    token = document(fetch_official('https://ghcr.io/token?service=ghcr.io&scope=repository:contabase-app/contabase:pull'))
    credential = token.get('token') if isinstance(token, dict) else None
    require(isinstance(credential, str) and 0 < len(credential) < 16384 and
            all(32 <= ord(c) < 127 for c in credential), 'token publico GHCR invalido')
    headers = {'Authorization': 'Bearer ' + credential, 'Accept': ', '.join(INDEX_TYPES + MANIFEST_TYPES)}
    proof = chosen['methods']['docker'][arch]
    index_digest = digest('sha256:' + proof['index_sha256'])
    platform_digest = digest('sha256:' + proof['payload_sha256'])
    raw = fetch_official(REGISTRY + index_digest, headers)
    require(sha(raw) == index_digest[7:], 'indice OCI divergente')
    index = document(raw)
    require(isinstance(index, dict) and index.get('schemaVersion') == 2 and
            index.get('mediaType') in INDEX_TYPES and isinstance(index.get('manifests'), list) and
            0 < len(index['manifests']) <= 64, 'indice OCI invalido')
    require(all(isinstance(r, dict) and isinstance(r.get('platform'), dict) for r in index['manifests']), 'plataformas OCI invalidas')
    matches = [r for r in index['manifests'] if r['platform'].get('os') == 'linux' and
               r['platform'].get('architecture') == arch]
    require(len(matches) == 1 and matches[0].get('digest') == platform_digest, 'plataforma OCI divergente')
    payload = fetch_official(REGISTRY + platform_digest, headers)
    require(sha(payload) == platform_digest[7:], 'manifest da plataforma divergente')
    manifest = document(payload)
    require(isinstance(manifest, dict) and manifest.get('schemaVersion') == 2 and
            manifest.get('mediaType') in MANIFEST_TYPES and isinstance(manifest.get('config'), dict), 'manifest OCI invalido')
    config_digest = digest(manifest['config'].get('digest'))
    require(sha(fetch_official(REGISTRY + chosen['version'], headers)) == index_digest[7:], 'tag da imagem divergente')
    return {'version': chosen['version'], 'channel': chosen['channel'], 'commit': chosen['commit'],
            'reference': REPOSITORY + '@' + index_digest, 'index_digest': index_digest,
            'platform': 'linux/' + arch, 'platform_digest': platform_digest, 'config_digest': config_digest}


def state_identity(state, config_digest):
    return {'version': state['CONTABASE_INSTALLED_VERSION'], 'channel': state['CONTABASE_CHANNEL'],
            'commit': state['CONTABASE_COMMIT'], 'reference': state['CONTABASE_IMAGE_REFERENCE'],
            'index_digest': state['CONTABASE_IMAGE_INDEX_DIGEST'], 'platform': state['CONTABASE_IMAGE_PLATFORM'],
            'platform_digest': state['CONTABASE_IMAGE_PLATFORM_DIGEST'], 'config_digest': config_digest}


def atomic_write(path, raw, mode=0o600):
    path = Path(path)
    secure_path(path.parent, directory=True)
    if path.exists() or path.is_symlink():
        secure_path(path)
    fd, name = tempfile.mkstemp(prefix='.' + path.name + '.', dir=str(path.parent))
    try:
        with os.fdopen(fd, 'wb') as output:
            os.fchmod(output.fileno(), mode)
            output.write(raw)
            output.flush()
            os.fsync(output.fileno())
        os.replace(name, path)
        sync_directory(path.parent)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def sync_directory(path):
    fd = os.open(str(path), os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def installation_preflight(root, state_file, history_file):
    require(os.geteuid() == 0 and sys.version_info >= (3, 9), 'root e Python 3.9+ obrigatorios')
    secure_path(Path(root).parent, directory=True)
    paths = [Path(state_file), Path(history_file), COMMAND_PREFIX / 'bin/contabase-update',
             COMMAND_PREFIX / 'bin/cb-update', COMMAND_PREFIX / 'lib/contabase/contabase-docker-image-update.py']
    require(len(set(paths)) == len(paths), 'destinos administrativos devem ser distintos')
    for path in paths:
        require(path.is_absolute() and str(path) == os.path.normpath(str(path)) and
                not any(ord(c) < 32 for c in str(path)), 'caminho administrativo invalido')
        require(not path.exists() and not path.is_symlink(), 'destino administrativo preexistente')
        parent = path.parent
        while not parent.exists() and not parent.is_symlink():
            parent = parent.parent
        secure_path(parent, directory=True)


def install_image_update_command(root, state_file, history_file, state_raw):
    """Publish only new administrative files; remove our own files on failure."""
    require(os.geteuid() == 0, 'instalacao docker-image exige root')
    root = secure_path(Path(root), directory=True)
    installation_preflight(root, state_file, history_file)
    state_file, history_file = Path(state_file), Path(history_file)
    require(state_file != history_file, 'estado e historico devem ser distintos')
    wrapper = read_private(root / 'scripts/lib/contabase-update-wrapper.sh').decode()
    anchor = 'STATE_FILE="${CONTABASE_STATE_FILE:-/etc/contabase/install-state.env}"'
    require(wrapper.count(anchor) == 1, 'wrapper fora do contrato de estado')
    # The binding is part of the root-owned command, never a sourced env file.
    wrapper = wrapper.replace(anchor, 'STATE_FILE=' + shlex.quote(str(state_file)) +
                              '\nSTATE_FILE="${CONTABASE_STATE_FILE:-$STATE_FILE}"')
    helper = read_private(root / 'scripts/lib/contabase-docker-image-update.py')
    state = dict(line.split('=', 1) for line in state_raw.decode().splitlines())
    require(state.get('CONTABASE_REPO_PATH') == str(root) and
            state.get('CONTABASE_INSTALL_METHOD') == 'docker-image', 'estado inicial divergente')
    history = (datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ') +
               ' method=docker-image version=' + state['CONTABASE_INSTALLED_VERSION'] +
               ' commit=' + state['CONTABASE_COMMIT'] + ' digest=' + state['CONTABASE_IMAGE_INDEX_DIGEST'] + '\n').encode()
    lib = COMMAND_PREFIX / 'lib/contabase'
    command = COMMAND_PREFIX / 'bin/contabase-update'
    alias = command.with_name('cb-update')
    files = [(lib / 'contabase-docker-image-update.py', helper, 0o644),
             (command, wrapper.encode(), 0o755), (alias, None, None),
             (history_file, history, 0o644), (state_file, state_raw, 0o644)]
    created_files, created_dirs, directories = [], [], {}
    def directory(path):
        if path in directories:
            return directories[path]
        if not path.exists():
            parent_fd = directory(path.parent)
            os.mkdir(path.name, 0o755, dir_fd=parent_fd)
            created_dirs.append((parent_fd, path.name, path.lstat()))
            os.fsync(parent_fd)
        secure_path(path, directory=True)
        fd = os.open(str(path), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        require(os.fstat(fd).st_ino == path.stat().st_ino, 'diretorio mudou')
        directories[path] = fd
        return fd
    try:
        # Validate all destinations before creating any command or state.
        for path, _, _ in files:
            directory(path.parent)
            require(not path.exists() and not path.is_symlink(), 'destino administrativo preexistente')
        for path, raw, mode in files:
            parent_fd = directories[path.parent]
            if raw is None:
                os.symlink('contabase-update', path.name, dir_fd=parent_fd)
                created_files.append((parent_fd, path.name, os.stat(path.name, dir_fd=parent_fd, follow_symlinks=False)))
            else:
                fd = os.open(path.name, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW,
                             mode, dir_fd=parent_fd)
                created_files.append((parent_fd, path.name, os.fstat(fd)))
                with os.fdopen(fd, 'wb') as output:
                    os.fchmod(output.fileno(), mode)
                    output.write(raw)
                    output.flush()
                    os.fsync(output.fileno())
            os.fsync(parent_fd)
        load_image_state(state_file)
    except BaseException:
        for parent_fd, name, created in reversed(created_files):
            try:
                current = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
                if (created.st_dev, created.st_ino) == (current.st_dev, current.st_ino):
                    os.unlink(name, dir_fd=parent_fd)
                    os.fsync(parent_fd)
            except FileNotFoundError:
                pass
        for parent_fd, name, created in reversed(created_dirs):
            try:
                current = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
                if (created.st_dev, created.st_ino) == (current.st_dev, current.st_ino):
                    os.rmdir(name, dir_fd=parent_fd)
                    os.fsync(parent_fd)
            except OSError:
                pass
        raise
    finally:
        for fd in directories.values():
            os.close(fd)


def required_tool(name):
    executable = shutil.which(name, path=SAFE_PATH)
    require(executable is not None, 'ferramenta obrigatoria ausente: ' + name)
    path = secure_path(Path(executable).resolve(strict=True), hardlinks=True)
    require(os.access(path, os.X_OK), 'ferramenta nao executavel: ' + name)
    return path


def backup_preflight(runtime):
    require(sys.version_info >= (3, 9), 'Python 3.9+ obrigatorio')
    for name in ('bash', 'sqlite3', 'awk', 'stat', 'sort', 'wc', 'tr', 'date',
                 'mktemp', 'mkdir', 'cp', 'find', 'rm', 'mv', 'cat'):
        required_tool(name)
    checksum = next((name for name in ('sha256sum', 'shasum') if shutil.which(name, path=SAFE_PATH)), None)
    require(checksum is not None, 'sha256sum ou shasum obrigatorio')
    required_tool(checksum)
    data = secure_path(runtime.root / 'data', directory=True, data=True)
    secure_path(data / 'contabase.db', data=True)
    destination = data / 'backups'
    if destination.exists() or destination.is_symlink():
        destination = secure_path(destination, directory=True, data=True)
    else:
        destination = data
    require(destination.stat().st_mode & 0o222 and os.access(destination, os.W_OK | os.X_OK),
            'destino de backup nao gravavel')
    backup_helper(runtime)


class Runtime:
    def __init__(self, state, state_file):
        self.state = state
        self.state_file = Path(state_file)
        self.root = Path(state['CONTABASE_REPO_PATH'])
        self.compose = self.root / state['CONTABASE_COMPOSE_IMAGE_FILE']
        self.pending = self.state_file.parent / 'docker-image-update.pending.json'
        self.deadline = None
        self.environment = {'PATH': SAFE_PATH, 'LANG': 'C', 'HOME': '/nonexistent',
                            'DOCKER_CONFIG': '/nonexistent', 'DOCKER_HOST': 'unix:///var/run/docker.sock'}

    def run(self, args, timeout=180, environment=None):
        if self.deadline is not None:
            remaining = self.deadline - time.monotonic()
            timeout = remaining if timeout is None else min(timeout, remaining)
            require(timeout > 0, 'prazo de verificacao excedido')
        executable = required_tool(args[0])
        try:
            result = subprocess.run([str(executable), *args[1:]], cwd=str(self.root), env=environment or self.environment,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout, check=True)
            require(len(result.stdout) <= 16777216, 'saida de ferramenta excede limite')
            return result.stdout
        except (OSError, subprocess.SubprocessError):
            raise Blocked('ferramenta falhou; saida sensivel suprimida') from None

    def docker(self, *args):
        return self.run(['docker', *args])

    def compose_command(self, *args, file=None):
        return self.docker('compose', '--project-directory', str(self.root), '--project-name', 'contabase',
                           '-f', str(file or self.compose), *args)

    def model(self, file=None):
        return document(self.compose_command('config', '--format', 'json', file=file))

    def inspect(self, identifier, image=False, platform=None):
        args = ['image', 'inspect'] if image else ['inspect']
        if platform is not None:
            args += ['--platform', platform]
        values = document(self.docker(*args, identifier))
        require(isinstance(values, list) and len(values) == 1 and isinstance(values[0], dict), 'inspect ambiguo')
        return values[0]

    def save(self, transaction):
        atomic_write(self.pending, json.dumps(transaction, sort_keys=True).encode())


def model_hash(model):
    return sha(json.dumps(model, sort_keys=True, separators=(',', ':')).encode())


def validate_model(runtime, model, reference):
    require(isinstance(model, dict) and set(model.get('services', {})) == {'contabase'}, 'servicos Compose ambiguos')
    service = model['services']['contabase']
    require(set(service) <= {'image', 'container_name', 'user', 'ports', 'env_file', 'environment',
                            'volumes', 'restart', 'healthcheck', 'networks', 'command', 'entrypoint'} and
            service.get('command') is None and service.get('entrypoint') is None and service.get('image') == reference and
            service.get('container_name') == 'contabase_app' and service.get('user') == '1000:1000' and
            service.get('restart') == 'unless-stopped', 'Compose fora do contrato de imagem')
    env = service.get('environment', {})
    require(isinstance(env, dict) and env.get('DATA_DIR') == '/app/data' and
            env.get('DB_FILE') == '/app/data/contabase.db' and env.get('UPLOADS_DIR') == '/app/data/uploads' and
            str(env.get('PORT')) == '8080', 'persistencia/porta Compose divergente')
    for key in ('AUTH_ENCRYPTION_KEY', 'SECURITY_MASTER_KEY', 'CONTABASE_SETUP_TOKEN'):
        value = env.get(key)
        if key == 'CONTABASE_SETUP_TOKEN' and (value is None or value == ''):
            continue
        require(isinstance(value, str) and value.strip() and not value.strip().lower().startswith(
                    ('change', 'replace', 'your_', 'placeholder', 'example', '__preencha_', '__gerar_com_')),
                'chave/configuracao obrigatoria ausente')
    mounts = service.get('volumes', [])
    require(len(mounts) == 1 and mounts[0].get('type') == 'bind' and
            mounts[0].get('source') == str(runtime.root / 'data') and mounts[0].get('target') == '/app/data' and
            mounts[0].get('read_only', False) is False, 'mount Compose divergente')
    require(service.get('healthcheck', {}).get('test') ==
            ['CMD', 'wget', '--no-verbose', '--tries=1', '--spider', 'http://localhost:8080/health'], 'healthcheck divergente')
    require(service.get('ports') and all(p.get('target') == 8080 and p.get('protocol', 'tcp') == 'tcp'
                                       for p in service['ports']), 'portas divergentes')
    return service


def validate_platform_descriptor(descriptor, identity):
    os_name, architecture = identity['platform'].split('/')
    require(isinstance(descriptor, dict) and descriptor.get('digest') == identity['platform_digest'] and
            descriptor.get('mediaType') in MANIFEST_TYPES and
            isinstance(descriptor.get('platform'), dict) and
            descriptor['platform'].get('os') == os_name and descriptor['platform'].get('architecture') == architecture,
            'descritor do manifesto de plataforma divergente')


def validate_local_image(runtime, identity):
    os_name, architecture = identity['platform'].split('/')
    image = runtime.inspect(identity['reference'], image=True)
    require(identity['reference'] in image.get('RepoDigests', []) and
            image.get('Os') == os_name and image.get('Architecture') == architecture, 'identidade da imagem divergente')
    if image.get('Id') == identity['config_digest']:
        return image
    # containerd exposes the index as Id; bind its selected manifest separately.
    descriptor = image.get('Descriptor')
    require(image.get('Id') == identity['index_digest'] and isinstance(descriptor, dict) and
            descriptor.get('digest') == identity['index_digest'] and descriptor.get('mediaType') in INDEX_TYPES,
            'identidade do indice local divergente')
    selected = runtime.inspect(identity['reference'], image=True, platform=identity['platform'])
    require(selected.get('Id') == identity['platform_digest'] and
            identity['reference'] in selected.get('RepoDigests', []) and
            selected.get('Os') == os_name and selected.get('Architecture') == architecture and
            selected.get('Config') == image.get('Config'), 'imagem local da plataforma divergente')
    validate_platform_descriptor(selected.get('Descriptor'), identity)
    return image


def validate_current_runtime(runtime, identity, model=None, healthy=True):
    model = runtime.model() if model is None else model
    service = validate_model(runtime, model, identity['reference'])
    ids = runtime.compose_command('ps', '--all', '-q', 'contabase').decode().split()
    require(len(ids) == 1 and re.fullmatch('[0-9a-f]{64}', ids[0]), 'container ausente ou ambiguo')
    item = runtime.inspect(ids[0])
    labels = item.get('Config', {}).get('Labels', {})
    require(item.get('Id') == ids[0] and labels.get('com.docker.compose.project') == 'contabase' and
            labels.get('com.docker.compose.service') == 'contabase' and
            labels.get('com.docker.compose.project.working_dir') == str(runtime.root) and
            labels.get('com.docker.compose.project.config_files') == str(runtime.compose), 'identidade Compose divergente')
    image = validate_local_image(runtime, identity)
    require(image.get('Id') == item.get('Image') and
            item['Config'].get('Image') == identity['reference'] and item['Config'].get('User') == '1000:1000',
            'identidade da imagem/container divergente')
    if image['Id'] != identity['config_digest'] or item.get('ImageManifestDescriptor') is not None:
        validate_platform_descriptor(item.get('ImageManifestDescriptor'), identity)
    mounts = item.get('Mounts', [])
    require(len(mounts) == 1 and mounts[0].get('Type') == 'bind' and mounts[0].get('RW') is True and
            mounts[0].get('Source') == str(runtime.root / 'data') and mounts[0].get('Destination') == '/app/data', 'mount do runtime divergente')
    env_pairs = [v.split('=', 1) for v in item['Config'].get('Env', []) if '=' in v]
    actual_env = dict(env_pairs)
    require(len(actual_env) == len(env_pairs), 'ambiente do container ambiguo')
    require(all(actual_env.get(k) == str(v) for k, v in service['environment'].items()), 'ambiente efetivo divergente')
    require(item['Config'].get('Entrypoint') == image.get('Config', {}).get('Entrypoint') and
            item['Config'].get('Cmd') == image.get('Config', {}).get('Cmd') and
            item['Config'].get('Healthcheck', {}).get('Test') == service['healthcheck']['test'], 'comando/healthcheck efetivo divergente')
    expected_ports = {}
    for port in service['ports']:
        expected_ports.setdefault('8080/tcp', []).append({'HostIp': port.get('host_ip', ''), 'HostPort': str(port['published'])})
    require(item.get('HostConfig', {}).get('PortBindings') == expected_ports and
            item['HostConfig'].get('RestartPolicy', {}).get('Name') == 'unless-stopped', 'portas/restart efetivos divergentes')
    expected_networks = {model.get('networks', {}).get(n, {}).get('name', 'contabase_' + n)
                         for n in service.get('networks', {})}
    require(set(item.get('NetworkSettings', {}).get('Networks', {})) == expected_networks, 'redes efetivas divergentes')
    if healthy:
        require(item.get('State', {}).get('Running') is True and
                item['State'].get('Health', {}).get('Status') == 'healthy', 'runtime nao saudavel')
        raw = runtime.docker('exec', ids[0], 'cat', '/app/VERSION')
        require(raw in (identity['version'].encode(), (identity['version'] + '\n').encode()), 'VERSION interna divergente')
        require(document(runtime.docker('exec', ids[0], 'wget', '-q', '-O', '-', 'http://localhost:8080/health')) ==
                {'status': 'healthy'}, 'health HTTP divergente')
        again = runtime.inspect(ids[0])
        require(again.get('Id') == item['Id'] and again.get('Image') == item['Image'] and
                again.get('ImageManifestDescriptor') == item.get('ImageManifestDescriptor') and
                again.get('RestartCount') == item.get('RestartCount') and
                again.get('State', {}).get('Running') is True and
                again['State'].get('Health', {}).get('Status') == 'healthy', 'runtime mudou durante verificacao')
        require(runtime.compose_command('ps', '--all', '-q', 'contabase').decode().split() == ids, 'container mudou')
    return item


def prepare_image_compose(runtime, current, target, original):
    model = runtime.model()
    validate_model(runtime, model, current['reference'])
    pattern = r'(?m)^    image: (?:"' + re.escape(current['reference']) + r'"|\x27' + re.escape(current['reference']) + r'\x27|' + re.escape(current['reference']) + r')[ \t]*$'
    replacement, count = re.subn(pattern, '    image: "' + target['reference'] + '"', original.decode())
    require(count == 1, 'declaracao de imagem Compose ambigua')
    fd, name = tempfile.mkstemp(prefix='.contabase-image-candidate-', suffix='.yml', dir=str(runtime.root))
    os.close(fd)
    candidate = Path(name)
    try:
        atomic_write(candidate, replacement.encode(), runtime.compose.stat().st_mode & 0o777)
        candidate_model = runtime.model(candidate)
        expected = copy.deepcopy(model)
        expected['services']['contabase']['image'] = target['reference']
        require(candidate_model == expected, 'Compose candidato altera parametros alem da imagem')
        return candidate, candidate_model
    except BaseException:
        candidate.unlink()
        raise


def assert_no_writers(runtime):
    data = runtime.root / 'data'
    for identifier in runtime.docker('ps', '-q').decode().split():
        item = runtime.inspect(identifier)
        for mount in item.get('Mounts', []):
            source = Path(mount.get('Source', '/'))
            require(not (source == data or source in data.parents or data in source.parents), 'outro container compartilha os dados')
    for process in Path('/proc').iterdir():
        if not process.name.isdigit():
            continue
        try:
            for fd in (process / 'fd').iterdir():
                try:
                    target = Path(os.readlink(fd))
                    require(target != data and data not in target.parents, 'processo mantem dados abertos')
                except FileNotFoundError:
                    pass
        except FileNotFoundError:
            pass


def validate_backup(path):
    path = secure_path(path, directory=True, data=True)
    require({p.name for p in path.iterdir()} == {'database.sqlite', 'uploads', 'checksums.sha256', 'manifest.txt'}, 'pacote de backup incompleto')
    secure_path(path / 'uploads', directory=True, data=True)
    manifest_lines = read_private(path / 'manifest.txt', data=True).decode().splitlines()
    manifest = dict(line.split('=', 1) for line in manifest_lines)
    require(len(manifest) == len(manifest_lines), 'manifest de backup ambiguo')
    require(manifest.get('format_version') == '1' and manifest.get('integrity_check') == 'ok' and
            manifest.get('foreign_key_check') == 'ok' and manifest.get('wal_consistency_method') == 'sqlite-online-backup', 'manifest de backup invalido')
    expected = {}
    for line in read_private(path / 'checksums.sha256', data=True).decode().splitlines():
        match = re.fullmatch(r'([0-9a-f]{64})  (database\.sqlite|uploads/.+)', line)
        require(match is not None, 'checksum de backup invalido')
        checksum, name = match.groups()
        require(name not in expected and '..' not in Path(name).parts, 'inventario de backup ambiguo')
        expected[name] = checksum
    actual = {}
    for item in (path / 'uploads').rglob('*'):
        require(not item.is_symlink(), 'symlink no backup')
        if item.is_file():
            actual[str(item.relative_to(path))] = file_sha(secure_path(item, data=True))
        else:
            require(item.is_dir(), 'tipo especial no backup')
    actual['database.sqlite'] = file_sha(secure_path(path / 'database.sqlite', data=True))
    require(actual == expected and actual['database.sqlite'] == manifest.get('database_sha256') and
            manifest.get('backup_id') == path.name, 'integridade de backup divergente')
    return manifest


def file_sha(path):
    result = hashlib.sha256()
    with open(path, 'rb') as source:
        for chunk in iter(lambda: source.read(65536), b''):
            result.update(chunk)
    return result.hexdigest()


def backup_helper(runtime):
    helper = secure_path(runtime.root / 'scripts/ops/backup.sh')
    raw = read_private(helper)
    require(sha(raw) == BACKUP_SHA256, 'helper de backup diverge da referencia revisada')
    return raw


def create_image_backup(runtime, transaction, helper):
    assert_no_writers(runtime)
    data = runtime.root / 'data'
    database = data / 'contabase.db'
    require(database.is_file() and not database.is_symlink(), 'banco esperado ausente')
    destination = data / 'backups'
    if not destination.exists():
        destination.mkdir(mode=0o750)
    secure_path(destination, directory=True, data=True)
    fd, name = tempfile.mkstemp(prefix='.contabase-backup-helper-', dir=str(runtime.state_file.parent))
    try:
        with os.fdopen(fd, 'wb') as output:
            output.write(helper)
        require(file_sha(name) == BACKUP_SHA256, 'helper materializado divergente')
        environment = {**runtime.environment, 'APP_VERSION': runtime.state['CONTABASE_INSTALLED_VERSION'],
                       'DATA_DIR': str(data), 'DB_FILE': str(database), 'UPLOADS_DIR': str(data / 'uploads')}
        # Canonical backup duration depends on DB/uploads size, not the health deadline.
        output = runtime.run(['/bin/bash', name, str(destination)], timeout=None, environment=environment).decode().strip()
        path = Path(output)
        require(path.parent == destination and re.fullmatch(r'backup-[A-Za-z0-9-]+', path.name), 'destino do backup divergente')
        manifest = validate_backup(path)
        require(manifest.get('application_version') == runtime.state['CONTABASE_INSTALLED_VERSION'] and
                manifest.get('source_db_path') == str(database), 'origem do backup divergente')
        transaction['backup'] = str(path)
        runtime.save(transaction)
    finally:
        os.unlink(name)


def assert_inputs(runtime, transaction, expected_compose):
    require(sha(read_private(runtime.root / '.env.docker')) == transaction['env_hash'] and
            sha(read_private(runtime.compose)) == expected_compose, 'configuracao mudou durante transacao')
    require(load_image_state(runtime.state_file) == transaction['old_state'], 'estado mudou durante transacao')


def activate_image_target(runtime, transaction, candidate):
    assert_inputs(runtime, transaction, transaction['old_compose_hash'])
    transaction['phase'] = 'stopping'
    runtime.save(transaction)
    runtime.compose_command('stop', '-t', '30', 'contabase')
    assert_no_writers(runtime)
    create_image_backup(runtime, transaction, backup_helper(runtime))
    assert_inputs(runtime, transaction, transaction['old_compose_hash'])
    require(sha(read_private(candidate)) == transaction['new_compose_hash'], 'candidata Compose mudou')
    # These scans reject observed writers; privileged external processes still require isolation.
    assert_no_writers(runtime)
    atomic_write(runtime.compose, read_private(candidate), runtime.compose.stat().st_mode & 0o777)
    transaction['phase'] = 'starting'
    transaction['startup_attempted'] = True
    runtime.save(transaction)
    assert_no_writers(runtime)
    runtime.compose_command('up', '-d', '--no-build', '--pull', 'never', '--no-deps', 'contabase')


def confirm_image_runtime(runtime, identity):
    runtime.deadline = time.monotonic() + 180
    try:
        for _ in range(60):
            try:
                return validate_current_runtime(runtime, identity)
            except Blocked:
                if time.monotonic() >= runtime.deadline:
                    break
                time.sleep(3)
    finally:
        runtime.deadline = None
    raise Blocked('identidade/health da candidata nao confirmados')


def next_state(old, target):
    result = dict(old)
    for key, field in {'CONTABASE_INSTALLED_VERSION': 'version', 'CONTABASE_COMMIT': 'commit',
                       'CONTABASE_IMAGE_REFERENCE': 'reference', 'CONTABASE_IMAGE_INDEX_DIGEST': 'index_digest',
                       'CONTABASE_IMAGE_PLATFORM': 'platform', 'CONTABASE_IMAGE_PLATFORM_DIGEST': 'platform_digest'}.items():
        result[key] = target[field]
    return result


def commit_image_state(runtime, transaction):
    require(transaction['runtime_confirmed'] is True and transaction['startup_attempted'] is True,
            'promocao sem confirmacao')
    require(sha(read_private(runtime.compose)) == transaction['new_compose_hash'] and
            sha(read_private(runtime.root / '.env.docker')) == transaction['env_hash'] and
            model_hash(runtime.model()) == transaction['model_hash'], 'inputs mudaram antes da promocao')
    expected = next_state(transaction['old_state'], transaction['target'])
    require(load_image_state(runtime.state_file) in (transaction['old_state'], expected), 'estado divergiu antes da promocao')
    validate_current_runtime(runtime, transaction['target'])
    atomic_write(runtime.state_file, ''.join(k + '=' + expected[k] + '\n' for k in sorted(expected)).encode(), 0o644)
    history = runtime.state_file.parent / 'install-history.log'
    previous = read_private(history) if history.exists() else b''
    marker = (' transaction=' + transaction['id'] + '\n').encode()
    if marker not in previous:
        line = (datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ') +
                ' method=docker-image version=' + transaction['target']['version'] +
                ' commit=' + transaction['target']['commit'] + ' digest=' + transaction['target']['index_digest'] +
                marker.decode()).encode()
        atomic_write(history, previous + line, 0o644)
    runtime.pending.unlink()
    sync_directory(runtime.pending.parent)


def handle_pending_update(runtime):
    if not runtime.pending.exists() and not runtime.pending.is_symlink():
        return False
    transaction = document(read_private(runtime.pending))
    require(isinstance(transaction, dict) and set(transaction) == {
        'schema', 'id', 'phase', 'old_state', 'target', 'old_compose_hash', 'new_compose_hash',
        'env_hash', 'model_hash', 'startup_attempted', 'runtime_confirmed', 'backup'}, 'registro pendente ambiguo')
    require(type(transaction['schema']) is int and transaction['schema'] == 1 and
            isinstance(transaction['id'], str) and re.fullmatch('[0-9a-f]{32}', transaction['id']) and
            type(transaction['startup_attempted']) is bool and type(transaction['runtime_confirmed']) is bool,
            'registro pendente invalido')
    if (transaction['phase'] in ('prepared', 'stopping') and not transaction['startup_attempted']
            and not transaction['runtime_confirmed']):
        assert_inputs(runtime, transaction, transaction['old_compose_hash'])
        old = transaction['old_state']
        arch = old['CONTABASE_IMAGE_PLATFORM'].split('/')[1]
        chosen = {'version': old['CONTABASE_INSTALLED_VERSION'], 'channel': old['CONTABASE_CHANNEL'],
                  'commit': old['CONTABASE_COMMIT'], 'methods': {'docker': {arch: {
                      'index_sha256': old['CONTABASE_IMAGE_INDEX_DIGEST'][7:],
                      'payload_sha256': old['CONTABASE_IMAGE_PLATFORM_DIGEST'][7:]}}}}
        validate_current_runtime(runtime, verify_oci_identity(chosen, arch))
        runtime.pending.unlink()
        sync_directory(runtime.pending.parent)
        print('Transacao anterior encerrada; runtime anterior integro e ativo; nenhum startup adicional.')
        return True
    if transaction['phase'] != 'confirmed' or not transaction['runtime_confirmed']:
        if transaction['startup_attempted']:
            require(sha(read_private(runtime.compose)) == transaction['new_compose_hash'], 'Compose pendente divergente')
            stop_failed_candidate(runtime)
        raise Blocked('transacao interrompida; nenhuma reativacao automatica')
    require(transaction['startup_attempted'], 'registro de confirmacao contraditorio')
    require(load_image_state(runtime.state_file) in (transaction['old_state'], next_state(transaction['old_state'], transaction['target'])),
            'estado pendente contraditorio')
    target = transaction['target']
    require(target['reference'] == REPOSITORY + '@' + digest(target['index_digest']) and
            target['channel'] == runtime.state['CONTABASE_CHANNEL'] and
            version(target['version']) >= version(transaction['old_state']['CONTABASE_INSTALLED_VERSION']), 'alvo pendente invalido')
    require(resolve_image_target(transaction['old_state'], target['version']) == target,
            'distribuicao/identidade pendente divergente')
    require(Path(transaction['backup']).parent == runtime.root / 'data/backups', 'backup pendente fora do diretorio')
    validate_backup(Path(transaction['backup']))
    try:
        validate_current_runtime(runtime, target)
        commit_image_state(runtime, transaction)
    except Exception:
        try:
            validate_current_runtime(runtime, target)
        except Exception:
            stop_failed_candidate(runtime)
        raise
    print('Estado docker-image finalizado; nenhum startup adicional.')
    return True


def arguments(argv):
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            self.exit(2, 'Erro: argumentos invalidos para docker-image.\n')

    supplied = list(sys.argv[1:] if argv is None else argv)
    parser = Parser(description=__doc__, allow_abbrev=False)
    parser.add_argument('version', nargs='?')
    parser.add_argument('--channel', choices=('beta', 'stable'))
    parser.add_argument('--state-file', default='/etc/contabase/install-state.env')
    parser.add_argument('--yes', '-y', action='store_true', default=os.environ.get('CONTABASE_ASSUME_YES') == '1')
    parser.add_argument('--dry-run', action='store_true')
    for option in ('--state-file', '--channel'):
        if sum(a == option or a.startswith(option + '=') for a in supplied) > 1:
            parser.error('opcao duplicada: ' + option)
    return parser.parse_args(supplied)


def stop_failed_candidate(runtime):
    try:
        runtime.compose_command('stop', '-t', '30', 'contabase')
        assert_no_writers(runtime)
    except Exception:
        print('PARADA NAO CONFIRMADA: mantenha a instancia isolada; intervencao necessaria.', file=sys.stderr)


def main(argv=None):
    args = arguments(argv)
    runtime = None
    transaction = None
    candidate = None
    confirmed = False
    lock_fd = None
    try:
        require(sys.version_info >= (3, 9), 'Python 3.9+ obrigatorio')
        require(sys.platform == 'linux' and os.geteuid() == 0, 'docker-image exige Linux e root')
        state = load_image_state(Path(args.state_file))
        require(args.channel in (None, state['CONTABASE_CHANNEL']), 'troca de canal recusada')
        require(os.environ.get('CONTABASE_INSTALL_METHOD', 'docker-image') == 'docker-image', 'metodo de ambiente divergente')
        require(not any(k.startswith('CONTABASE_IMAGE_') for k in os.environ) and
                not any(k in os.environ for k in ('CONTABASE_CHANNEL_BASE', 'CONTABASE_RAW_BASE', 'DOCKER_CONTEXT', 'DOCKER_HOST', 'COMPOSE_FILE')),
                'override de identidade/origem nao permitido')
        runtime = Runtime(state, args.state_file)
        require(not (runtime.root / '.env').exists() and not (runtime.root / '.env').is_symlink(),
                '.env implicito nao suportado no contrato de imagem')
        if not args.dry_run:
            lock = runtime.state_file.parent / 'docker-image-update.lock'
            lock_fd = os.open(str(lock), os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
            secure_path(lock)
            require(os.fstat(lock_fd).st_ino == lock.stat().st_ino, 'lock mudou')
            try:
                fcntl.flock(lock_fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                raise Blocked('outra atualizacao esta em curso') from None
            require(load_image_state(runtime.state_file) == state, 'estado mudou antes do lock')
            if handle_pending_update(runtime):
                return 0
        else:
            require(not runtime.pending.exists() and not runtime.pending.is_symlink(), 'transacao pendente bloqueia dry-run')
        info = document(runtime.docker('info', '--format', '{{json .SecurityOptions}}'))
        require(isinstance(info, list) and not any('rootless' in v for v in info), 'Docker rootless nao homologado')
        arch = {'x86_64': 'amd64', 'aarch64': 'arm64', 'arm64': 'arm64'}.get(os.uname().machine)
        require(state['CONTABASE_IMAGE_PLATFORM'] == 'linux/' + str(arch), 'host/plataforma divergente')
        current_choice = {'version': state['CONTABASE_INSTALLED_VERSION'], 'channel': state['CONTABASE_CHANNEL'],
                          'commit': state['CONTABASE_COMMIT'], 'methods': {'docker': {arch: {
                              'index_sha256': state['CONTABASE_IMAGE_INDEX_DIGEST'][7:],
                              'payload_sha256': state['CONTABASE_IMAGE_PLATFORM_DIGEST'][7:]}}}}
        current = verify_oci_identity(current_choice, arch)
        original = read_private(runtime.compose)
        original_env_hash = sha(read_private(runtime.root / '.env.docker'))
        model = runtime.model()
        item = validate_current_runtime(runtime, current, model)
        target = resolve_image_target(state, args.version)
        backup_preflight(runtime)
        filesystem = runtime.run(['stat', '-f', '-c', '%T', str(runtime.root / 'data')]).decode().strip()
        require(filesystem in ('ext2/ext3', 'xfs', 'btrfs', 'zfs', 'f2fs', 'overlayfs'),
                'filesystem local persistente nao comprovado')
        if target == current:
            print('ContaBase ja esta na identidade solicitada; nenhuma alteracao.')
            return 0
        if args.dry_run:
            print('Alvo Docker validado: ' + target['version'] + '; nenhuma alteracao.')
            return 0
        candidate, candidate_model = prepare_image_compose(runtime, current, target, original)
        runtime.docker('pull', '--platform', target['platform'], target['reference'])
        validate_local_image(runtime, target)
        if not args.yes:
            require(sys.stdin.isatty() and input('Atualizar para ' + target['version'] + '? [s/N] ').lower() == 's', 'atualizacao cancelada')
        require(validate_current_runtime(runtime, current, model)['Id'] == item['Id'], 'runtime mudou antes da parada')
        require(read_private(runtime.compose) == original and runtime.model() == model and
                sha(read_private(runtime.root / '.env.docker')) == original_env_hash, 'inputs mudaram antes da parada')
        backup_preflight(runtime)
        transaction = {'schema': 1, 'id': uuid.uuid4().hex, 'phase': 'prepared', 'old_state': state, 'target': target,
                       'old_compose_hash': sha(original), 'new_compose_hash': sha(read_private(candidate)),
                       'env_hash': original_env_hash, 'model_hash': model_hash(candidate_model),
                       'startup_attempted': False, 'runtime_confirmed': False, 'backup': ''}
        runtime.save(transaction)
        activate_image_target(runtime, transaction, candidate)
        confirm_image_runtime(runtime, target)
        transaction['runtime_confirmed'] = True
        transaction['phase'] = 'confirmed'
        runtime.save(transaction)
        confirmed = True
        commit_image_state(runtime, transaction)
        print('Atualizacao docker-image concluida: ' + target['version'])
        return 0
    except (Exception, KeyboardInterrupt) as error:
        if runtime and transaction and transaction['startup_attempted']:
            if confirmed:
                try:
                    validate_current_runtime(runtime, transaction['target'])
                except Exception:
                    stop_failed_candidate(runtime)
            else:
                stop_failed_candidate(runtime)
        reason = str(error) if isinstance(error, Blocked) else 'etapa ou integridade nao confirmada'
        print('UPDATE_BLOCKED: ' + reason + '. Nenhum retorno automatico. '
              'Preserve dados, configuracao, chaves, backup e registro pendente.', file=sys.stderr)
        return 1
    finally:
        if candidate is not None:
            candidate.unlink(missing_ok=True)
        if lock_fd is not None:
            os.close(lock_fd)


if __name__ == '__main__':
    def interrupted(signum, frame):
        raise InterruptedError('interrompido')
    signal.signal(signal.SIGTERM, interrupted)
    sys.exit(main())

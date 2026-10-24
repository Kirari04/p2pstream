#!/usr/bin/env python3
"""Release-distributed host controller. No third-party Python modules or Git checkout.

Only fixed Docker/Compose operations are executed. Deployment hooks and build
steps are rejected by the independently shipped Go layout validator.
"""
import argparse
import contextlib
import datetime
import fcntl
import grp
import hashlib
import http.client
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import socket
import signal
import stat
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request
import uuid

STATE = Path('/etc/p2pstream-server-updater')
BUNDLE_FILES = {'server-updater-host.py', 'install-server-updater.sh', 'server-updater-compose.sh', 'compose.yaml', '.env.example'}
TERMINAL = {'succeeded', 'rolled_back', 'failed'}
HEX = re.compile(r'^[0-9a-f]{64}$')
VERSION = re.compile(r'^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-staging\.[1-9][0-9]*)?$')
REPOSITORY = re.compile(r'^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$')
CANCELLATION = 'cancelled-enrollment.json'
BLOCKED_INITIAL_OWNER = 'blocked-initial-owner.json'
OWNER_ABSENT = 'test -d /data && test ! -e /data/.p2pstream-runtime-owner.json && test ! -L /data/.p2pstream-runtime-owner.json'
# Published servers need no new recovery CLI or interpreter. These fixed native
# commands run only inside the independently proved, immutable current server.
# stat does not follow links; dd refuses links and cannot block on a FIFO. Read
# bounds and before/after metadata checks also apply to the read-only probe.
OWNER_PROBE = r'''set -eu
export LC_ALL=C PATH=/usr/bin:/bin
cd /data
test ! -L /data
owner=.p2pstream-runtime-owner.json
test ! -L "$owner" && test -f "$owner"
metadata=$(stat -c '%d:%i:%f:%h:%u:%g:%s:%y:%z' -- "$owner")
printf '%s\n' "$metadata"
dd if="$owner" iflag=nonblock,nofollow bs=4097 count=1 status=none
test "$metadata" = "$(stat -c '%d:%i:%f:%h:%u:%g:%s:%y:%z' -- "$owner")"
test ! -L "$owner"
'''
LEASE_PROBE = r'''set -eu
export LC_ALL=C PATH=/usr/bin:/bin
cd /data
test ! -L /data
stat -c '%d:%i' -- /data
test ! -L .p2pstream-runtime.lock && test -f .p2pstream-runtime.lock
stat -c '%d:%i:%f:%h:%u:%g:%s:%y:%z' -- .p2pstream-runtime.lock
head -c 65537 /proc/locks
'''

class Failure(RuntimeError):
    pass

class UnsettledHelper(Failure):
    pass

class MissingRollbackServer(Failure):
    pass

def require(condition, message):
    if not condition:
        raise Failure(message)

def unique_pairs(pairs):
    out = {}
    for key, value in pairs:
        require(key not in out, 'Duplicate metadata key: ' + key)
        out[key] = value
    return out

def decode(data):
    return json.loads(data, object_pairs_hook=unique_pairs)

def digest(data):
    return hashlib.sha256(data).hexdigest()

def stable_version(value):
    require(isinstance(value, str) and VERSION.fullmatch(value) and '-' not in value, 'Invalid retained minimum safe version')
    return tuple(int(part) for part in value[1:].split('.'))

def validate_floor(value):
    require(isinstance(value, dict) and set(value) == {'sequence', 'security_epoch', 'minimum_safe_version', 'manifest_sha256'}, 'Invalid retained enrollment floor')
    require(all(type(value[key]) is int and 0 < value[key] < 2 ** 64 for key in ('sequence', 'security_epoch')) and isinstance(value['manifest_sha256'], str) and HEX.fullmatch(value['manifest_sha256']), 'Invalid retained enrollment floor')
    stable_version(value['minimum_safe_version'])
    return value

def require_floor(candidate, previous):
    validate_floor(candidate)
    validate_floor(previous)
    require(candidate['sequence'] >= previous['sequence'] and candidate['security_epoch'] >= previous['security_epoch'] and stable_version(candidate['minimum_safe_version']) >= stable_version(previous['minimum_safe_version']), 'Release is below the retained enrollment floor')
    require(candidate['sequence'] != previous['sequence'] or candidate['manifest_sha256'] == previous['manifest_sha256'], 'Release identity differs from the retained enrollment floor')

def cancellation_receipt(directory, installation, repository, channel):
    path = directory / CANCELLATION
    if not os.path.lexists(path):
        return None
    value = read_json(path)
    require(set(value) == {'api', 'installation', 'repository', 'channel', 'floor', 'completed'} and value['api'] == 1 and type(value['completed']) is bool, 'Invalid cancellation receipt')
    require((value['installation'], value['repository'], value['channel']) == (installation, repository, channel), 'Cancellation receipt belongs to another installation, publisher or channel')
    validate_floor(value['floor'])
    return value

def check_cancellation_floor(directory, args, manifest, manifest_hash):
    receipt = cancellation_receipt(directory, args.expect_installation, args.repository, args.expect_channel)
    if receipt:
        require_floor({key: manifest[key] for key in ('sequence', 'security_epoch', 'minimum_safe_version')} | {'manifest_sha256': manifest_hash}, receipt['floor'])
        version = args.expect_version.partition('-')[0]
        require(stable_version(version) > stable_version(receipt['floor']['minimum_safe_version']) or version == receipt['floor']['minimum_safe_version'] and '-' not in args.expect_version, 'Selected server is below the retained minimum safe version')

def read_json(path):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_size <= 2 * 1024 * 1024, 'Unsafe or oversized state file: ' + str(path))
    return decode(path.read_bytes())

def atomic(path, data, mode=0o600, uid=None, gid=None):
    if not isinstance(data, bytes):
        data = json.dumps(data, separators=(',', ':'), sort_keys=True).encode()
    fd, name = tempfile.mkstemp(prefix='.host-', dir=path.parent)
    try:
        if uid is not None or gid is not None:
            os.fchown(fd, -1 if uid is None else uid, -1 if gid is None else gid)
        os.fchmod(fd, mode)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
        fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    finally:
        if os.path.exists(name):
            os.unlink(name)

def run(args, *, cwd=None, check=True, timeout=180):
    # Never honor ambient Docker/Compose overrides inside trusted host commands.
    env = {'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
           'HOME': str(Path.home())}
    try:
        result = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise Failure('Command unavailable or timed out: ' + args[0]) from error
    require(len(result.stdout) <= 2 * 1024 * 1024, 'Command response exceeds limit')
    if check and result.returncode:
        # Compose diagnostics can include resolved secrets. Keep failures terse;
        # operator logs are available from the private host command.
        raise Failure('Host command failed: ' + ' '.join(args[:4]) + ' (exit ' + str(result.returncode) + ')')
    return result

def docker(*args, **kwargs):
    return run(['docker', '--host', 'unix:///var/run/docker.sock', *args], **kwargs)

def local_engine(updater=True):
    require(os.environ.get('DOCKER_HOST', 'unix:///var/run/docker.sock') == 'unix:///var/run/docker.sock', 'Run on the local rootful Docker host; DOCKER_HOST selects another daemon')
    require(not os.environ.get('DOCKER_CONTEXT'), 'Unset DOCKER_CONTEXT; only the local rootful Docker socket is supported')
    endpoint = decode(run(['docker', 'context', 'inspect']).stdout)[0]['Endpoints']['docker']['Host']
    require(endpoint == 'unix:///var/run/docker.sock', 'The selected Docker context is not the local /var/run/docker.sock daemon')
    info = decode(docker('info', '--format', '{{json .}}').stdout)
    require(info['OSType'] == 'linux', 'Only Linux Docker hosts are supported')
    if updater:
        require(not any('rootless' in item for item in info.get('SecurityOptions', [])), 'Rootless Docker updater enrollment is unsupported')
    arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(info['Architecture'], info['Architecture'])
    require(arch in {'amd64', 'arm64'}, 'Only Linux amd64 and arm64 are supported')
    return arch

class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        parsed = urllib.parse.urlparse(newurl)
        require(parsed.scheme == 'https' and parsed.hostname in {'github.com', 'api.github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com'} and parsed.port is None and parsed.username is None, 'Untrusted release redirect')
        return super().redirect_request(req, fp, code, msg, headers, newurl)

def fetch(url, limit):
    require(url.startswith('https://'), 'Release download requires HTTPS')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), SafeRedirect())
    req = urllib.request.Request(url, headers={'User-Agent': 'p2pstream-host-install/1', 'Accept': 'application/vnd.github+json' if url.startswith('https://api.github.com/') else 'application/octet-stream'})
    try:
        with opener.open(req, timeout=30) as response:
            data = response.read(limit + 1)
    except Exception as error:
        raise Failure('Release download unavailable; no deployment change was activated') from error
    require(len(data) <= limit, 'Release download exceeds size limit')
    return data

def release_metadata(repository, version, commit=None, channel=None, expected_manifest=None):
    require(REPOSITORY.fullmatch(repository) and VERSION.fullmatch(version), 'Invalid repository or exact release version')
    channel = channel or ('staging' if '-staging.' in version else 'stable')
    require(channel == ('staging' if '-staging.' in version else 'stable'), 'Release channel mismatch')
    release = decode(fetch('https://api.github.com/repos/' + repository + '/releases/tags/' + version, 262144))
    require(release['tag_name'] == version and not release['draft'] and release['prerelease'] == (channel == 'staging'), 'Release is unpublished or in another channel')
    base = 'https://github.com/' + repository + '/releases/download/' + version + '/'
    raw = fetch(base + 'p2pstream_agent_update_manifest.json', 65536)
    if expected_manifest:
        require(digest(raw) == expected_manifest, 'Release changed since this command was copied; refresh the selected server')
    manifest = decode(raw)
    require(manifest['schema_version'] == 1 and manifest['version'] == version and manifest['channel'] == channel and re.fullmatch('[0-9a-f]{40}', manifest['commit']), 'Manifest identity mismatch')
    if commit:
        require(manifest['commit'] == commit, 'Release commit differs from running server')
    expires = time.strptime(manifest['expires_at'], '%Y-%m-%dT%H:%M:%SZ')
    import calendar
    require(calendar.timegm(expires) > time.time(), 'Release metadata expired; manually upgrade to a current published release before enrollment')
    assets = {a['name']: a for a in manifest['release_assets']}
    require(len(assets) == len(manifest['release_assets']), 'Duplicate release assets')
    require('p2pstream_install.json' in assets, 'This published release has no Docker installation assets; manually deploy a newer release containing them. Existing releases are never rewritten.')
    descriptor_raw = fetch(base + 'p2pstream_install.json', 16384)
    descriptor_asset = assets['p2pstream_install.json']
    require(descriptor_asset['size'] == len(descriptor_raw) and descriptor_asset['sha256'] == digest(descriptor_raw), 'Installation descriptor verification failed')
    desc = decode(descriptor_raw)
    require(set(desc) == {'api', 'version', 'commit', 'channel', 'bundle', 'updater_images'} and desc['api'] == 1 and (desc['version'], desc['commit'], desc['channel']) == (version, manifest['commit'], channel), 'Installation descriptor identity mismatch')
    require(desc['bundle'] == 'p2pstream_' + version + '_docker.tar.gz' and desc['bundle'] in assets, 'Installation bundle is not bound to this release')
    require(set(desc['updater_images']) == {'linux/amd64', 'linux/arm64'}, 'Unsupported updater platforms')
    for ref in desc['updater_images'].values():
        require(re.fullmatch(re.escape('ghcr.io/' + repository.lower() + '-updater@sha256:') + '[0-9a-f]{64}', ref), 'Invalid updater image repository/digest')
    require('p2pstream_server_update.json' in assets, 'Release lacks server enrollment metadata')
    metadata = fetch(base + 'p2pstream_server_update.json', 16384)
    asset = assets['p2pstream_server_update.json']
    require(asset['size'] == len(metadata) and asset['sha256'] == digest(metadata), 'Server metadata verification failed')
    manifest['_files'] = {'p2pstream_agent_update_manifest.json': raw, 'p2pstream_install.json': descriptor_raw, 'p2pstream_server_update.json': metadata}
    images = manifest['oci_images']
    require(len(images) == 1 and images[0]['repository'] == 'ghcr.io/' + repository.lower() and re.fullmatch('sha256:[0-9a-f]{64}', images[0]['digest']), 'Server image binding invalid')
    return manifest, desc, images[0]['repository'] + '@' + images[0]['digest'], digest(raw)

def compose_options(options):
    # Preserve ordered inputs; allow only explicit global context flags. No
    # profiles, remote includes, positional commands, or lifecycle hooks.
    accepted = {'-p', '--project-name', '-f', '--file', '--project-directory', '--env-file'}
    require(len(options) % 2 == 0, 'Compose options must be pairs: -p NAME, -f FILE, --env-file FILE, --project-directory DIR')
    out = []
    for key, value in zip(options[::2], options[1::2]):
        require(key in accepted and value and not value.startswith('-') and '\n' not in value, 'Unsupported Compose option: ' + key)
        if key not in {'-p', '--project-name'}:
            path = Path(value).resolve()
            require(path.exists(), 'Compose input is missing: ' + str(path))
            value = str(path)
        out += [key, value]
    return out

def operator_gid():
    uid = os.environ.get('SUDO_UID')
    if uid is None:
        return 0
    gid = os.environ.get('SUDO_GID', '')
    require(uid.isdecimal() and gid.isdecimal(), 'Invalid sudo operator identity')
    account = pwd.getpwuid(int(uid))
    require(account.pw_gid == int(gid), 'Sudo operator group differs from the account primary group')
    try:
        engine = Path('/var/run/docker.sock').lstat()
        members = grp.getgrgid(engine.st_gid).gr_mem
        if stat.S_ISSOCK(engine.st_mode) and engine.st_uid == 0 and engine.st_mode & 0o060 == 0o060 and (account.pw_gid == engine.st_gid or account.pw_name in members):
            return engine.st_gid  # This group already has rootful Docker authority.
    except (FileNotFoundError, KeyError):
        pass
    try:
        members = set(grp.getgrgid(account.pw_gid).gr_mem)
        members.update(entry.pw_name for entry in pwd.getpwall() if entry.pw_gid == account.pw_gid)
    except KeyError:
        return 0
    return account.pw_gid if members <= {account.pw_name, 'root'} else 0

def compose_sources(options, container):
    paths = [value for key, value in zip(options[::2], options[1::2]) if key in {'-f', '--file'}]
    if not paths:
        paths = container['Config']['Labels'].get('com.docker.compose.project.config_files', '').split(',')
    require(paths and all(paths) and len(paths) == len(set(paths)), 'Cannot identify unique local Compose files; use explicit ordered -f options')
    result = []
    for name in paths:
        path = Path(name)
        require(path.is_absolute(), 'Compose source paths must resolve to absolute files')
        info = path.lstat()
        require(stat.S_ISREG(info.st_mode), 'Compose source bridges require regular files without symlinks')
        result.append(str(path))
    return result

def source_layout(source_dir, files, project, owned_aliases=None):
    directory = Path(source_dir)
    defaults = [directory / name for name in ('compose.yaml', 'compose.yml', 'docker-compose.yaml', 'docker-compose.yml') if os.path.lexists(directory / name)]
    require(len(defaults) <= 1, 'Multiple default Compose files exist; resolve the default command context before enrollment')
    owned_aliases = owned_aliases or {}
    for path in defaults:
        require(stat.S_ISREG(path.lstat().st_mode), 'Default Compose files must be regular files without symlinks')
        require(str(path) in files or owned_aliases.get(str(path)) == digest(path.read_bytes()), 'An unrelated default Compose file exists; resolve the default command context before enrollment')
    for name in ('compose.override.yaml', 'compose.override.yml', 'docker-compose.override.yaml', 'docker-compose.override.yml'):
        path = directory / name
        require(not os.path.lexists(path) or str(path) in files, 'An unrelated automatic Compose override exists; include or resolve it before enrollment')
    # Compose exposes its effective project name here even when it was only
    # derived from the directory. A final name-only override anchors that default
    # to the selected project without using -p, which would hide an explicit
    # conflicting COMPOSE_PROJECT_NAME from the source .env.
    with tempfile.TemporaryDirectory(prefix='p2pstream-compose-project-') as tmp:
        selected = Path(tmp) / 'selected-project.json'
        atomic(selected, {'name': project})
        environment = docker('compose', *[part for path in files for part in ('-f', path)], '-f', str(selected), 'config', '--environment', cwd=source_dir)
    values = {}
    if environment.returncode == 0:
        for line in environment.stdout.decode().splitlines():
            key, separator, value = line.partition('=')
            if separator and key in {'COMPOSE_FILE', 'COMPOSE_PATH_SEPARATOR', 'COMPOSE_PROJECT_NAME'}:
                values[key] = value
    require(not values.get('COMPOSE_PROJECT_NAME') or values['COMPOSE_PROJECT_NAME'] == project, 'Default COMPOSE_PROJECT_NAME selects another project; resolve it before enrollment')
    if values.get('COMPOSE_FILE'):
        paths = [str((directory / part).resolve()) for part in values['COMPOSE_FILE'].split(values.get('COMPOSE_PATH_SEPARATOR') or ':')]
        require(set(paths) == set(files), 'Default COMPOSE_FILE selects different inputs; resolve it before enrollment')
        primary = paths[0]
    else:
        primary = str(defaults[0]) if defaults and str(defaults[0]) in files else files[0]
    alias = str(defaults[0]) if defaults and str(defaults[0]) not in files else str(directory / 'compose.yaml') if not defaults and not values.get('COMPOSE_FILE') else None
    return {'primary': primary, 'default_alias': alias, 'existing_alias_sha256': owned_aliases.get(alias) if alias and os.path.lexists(alias) else None}

class Host:
    def __init__(self, directory=STATE):
        self.directory = directory
        self.journal = {}
        self.config = None
        self.updater = None

    def current_model_path(self):
        return Path(self.config.get('deployment_file', self.directory / 'compose.json')) if self.config else self.directory / 'compose.json'

    def model_path(self, model='compose.json'):
        return self.current_model_path() if model == 'compose.json' else self.directory / model

    def write_model(self, data):
        path = self.current_model_path()
        atomic(path, data, mode=0o640 if self.config and self.config.get('deployment_protocol') == 2 else 0o600)

    def input_options(self):
        return self.journal.get('editable_compose_options', self.journal['compose_options'])

    def prepare_deployment_directory(self):
        path = self.directory.with_name('p2pstream-server-deployment')
        path.mkdir(mode=0o750, exist_ok=True)
        info = path.lstat()
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and not info.st_mode & 0o022, 'Unsafe canonical deployment directory')
        os.chown(path, 0, operator_gid())
        path.chmod(0o2750)
        if path.stat().st_gid == 0:
            print('Canonical deployment is root-readable; use sudo docker compose for the original and default Compose commands.')
        self.phase(self.journal['phase'], deployment_directory=str(path))
        return path

    def capability(self, image, container=None):
        if container:
            raw = docker('exec', container, '/app/p2pstream', 'server-deployment-capabilities').stdout
        else:
            immutable = decode(docker('image', 'inspect', image).stdout)[0]['Id']
            raw = docker('run', '--rm', '--cap-drop', 'ALL', '--cap-add', 'NET_BIND_SERVICE', '--security-opt', 'no-new-privileges', '--entrypoint', '/app/p2pstream', immutable, 'server-deployment-capabilities').stdout
        value = decode(raw)
        require(value.get('runtime_guard') == 2, 'This server/executor lacks managed Compose startup protection; manually deploy a compatible published server before enabling or migrating deployment ownership')
        labels = decode(docker('image', 'inspect', image).stdout)[0]['Config']['Labels']
        require((value.get('version'), value.get('commit')) == (labels.get('org.opencontainers.image.version'), labels.get('org.opencontainers.image.revision')), 'Deployment capabilities differ from immutable image identity')

    def runtime(self, action):
        if self.config.get('deployment_protocol') == 2:
            return self.command('runtime-' + action, '--config', str(self.directory / 'config.json'))

    def record_runtime(self):
        if self.config.get('deployment_protocol') == 2:
            self.command('record-runtime', '--config', str(self.directory / 'config.json'))

    def handoff_recovery_controller(self, bundle):
        # The outer release bootstrap verifies the complete bundle hash. Before
        # replacing recovery code, also bind this copy to the verified module
        # currently executing and reject special files or partial bundles.
        bundle = Path(bundle)
        for name in BUNDLE_FILES:
            info = (bundle / name).lstat()
            require(stat.S_ISREG(info.st_mode) and info.st_size <= 262144, 'Unsafe or incomplete verified recovery bundle')
        controller = (bundle / 'server-updater-host.py').read_bytes()
        require(controller == Path(__file__).read_bytes(), 'Recovery controller differs from the executing verified installation helper')
        target = self.directory / 'server-updater-host.py'
        require(not os.path.lexists(target) or stat.S_ISREG(target.lstat().st_mode), 'Installed recovery controller must be a regular file')
        # Existing manage launchers invoke this stable path. Publish and fsync
        # compatible recovery code before writing any journal phase it owns.
        atomic(target, controller)

    def source_bridges(self):
        plan = self.journal.get('source_bridge')
        if not plan:
            files = []
            for index, name in enumerate(self.journal['source_files']):
                path = Path(name)
                info = path.lstat()
                require(stat.S_ISREG(info.st_mode), 'Compose source changed type before migration')
                editable = path.with_name(path.name + '.p2pstream-input-' + uuid.uuid4().hex[:8])
                require(not os.path.lexists(editable), 'Editable Compose input already exists')
                data = {'name': self.config['project'], 'include': [{'path': str(self.current_model_path()), 'project_directory': str(self.current_model_path().parent)}]} if name == self.journal['source_layout']['primary'] else {}
                files.append({'path': name, 'editable': str(editable), 'original_sha256': digest(path.read_bytes()), 'bridge': data,
                              'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid})
            mapping = {item['path']: item['editable'] for item in files}
            options = self.journal['compose_options'][:]
            explicit = False
            for index in range(0, len(options), 2):
                if options[index] in {'-f', '--file'}:
                    options[index + 1] = mapping[options[index + 1]]
                    explicit = True
            if not explicit:
                options += [part for item in files for part in ('-f', item['editable'])]
            alias = self.journal['source_layout'].get('default_alias')
            if alias:
                previous = self.journal['source_layout'].get('existing_alias_sha256')
                require(not os.path.lexists(alias) or previous and digest(Path(alias).read_bytes()) == previous, 'A default Compose file appeared before migration; inspect it before repair')
                backup = self.directory / 'default-compose-before.json'
                if previous:
                    atomic(backup, Path(alias).read_bytes())
                files.append({'path': alias, 'editable': None, 'original_sha256': previous, 'original_backup': str(backup) if previous else None, 'bridge': {'name': self.config['project'], 'include': [{'path': str(self.current_model_path()), 'project_directory': str(self.current_model_path().parent)}]}, 'mode': 0o644, 'uid': 0, 'gid': operator_gid()})
            plan = {'files': files}
            self.phase(self.journal['phase'], source_bridge=plan, editable_compose_options=options)
        # Validate the complete transaction before replacing any entry point.
        # A later unknown file must not cause a partially migrated source set.
        for item in plan['files']:
            source = Path(item['path'])
            bridge = json.dumps(item['bridge'], separators=(',', ':'), sort_keys=True).encode()
            accepted = {item['original_sha256'], digest(bridge)}
            if (item['path'] == self.journal['source_layout']['primary'] or item['editable'] is None) and plan.get('export_sha256'):
                accepted.add(plan['export_sha256'])
            require(not os.path.lexists(source) and item['original_sha256'] is None or stat.S_ISREG(source.lstat().st_mode) and digest(source.read_bytes()) in accepted, 'Compose source changed during bridge migration; inspect saved inputs before repair')
        for item in plan['files']:
            source = Path(item['path'])
            editable = Path(item['editable']) if item['editable'] else None
            bridge = json.dumps(item['bridge'], separators=(',', ':'), sort_keys=True).encode()
            accepted = {item['original_sha256'], digest(bridge)}
            if (item['path'] == self.journal['source_layout']['primary'] or item['editable'] is None) and plan.get('export_sha256'):
                accepted.add(plan['export_sha256'])
            require(not source.exists() and item['original_sha256'] is None or source.exists() and digest(source.read_bytes()) in accepted, 'Compose source changed during bridge migration; inspect saved inputs before repair')
            if editable and not editable.exists():
                require(digest(source.read_bytes()) == item['original_sha256'], 'Missing editable source copy during interrupted migration')
                atomic(editable, source.read_bytes(), mode=item['mode'], uid=item['uid'], gid=item['gid'])
        for item in plan['files']:
            source = Path(item['path'])
            bridge = json.dumps(item['bridge'], separators=(',', ':'), sort_keys=True).encode()
            atomic(source, bridge, mode=item['mode'], uid=item['uid'], gid=item['gid'])
        # Validate the actual original entry points before allowing startup.
        raw = docker('compose', *self.journal['compose_options'], 'config', '--format', 'json', cwd=self.journal['source_directory']).stdout
        model = decode(raw)
        require(model.get('name') == self.config['project'] and 'p2pstream-server-updater' in model.get('services', {}), 'Original Compose entry points did not resolve the managed deployment')
        default = decode(docker('compose', 'config', '--format', 'json', cwd=self.journal['source_directory']).stdout)
        canonical = decode(docker('compose', '--project-name', self.config['project'], '--project-directory', str(self.current_model_path().parent), '-f', str(self.current_model_path()), 'config', '--format', 'json').stdout)
        require(default == model == canonical, 'Default or original Compose command differs from the canonical managed deployment; restore the recorded entry points before repair')

    def export_sources(self, model):
        plan = self.journal.get('source_bridge')
        if not plan:
            return
        plan['export_sha256'] = digest(model)
        self.phase(self.journal['phase'], source_bridge=plan)
        primary = self.journal['source_layout']['primary']
        for index, item in enumerate(plan['files']):
            path = Path(item['path'])
            bridge = json.dumps(item['bridge'], separators=(',', ':'), sort_keys=True).encode()
            content = model if item['path'] == primary or item['editable'] is None else b'{}'
            require(path.read_bytes() in {bridge, content}, 'Compose bridge changed; inspect it before completing removal')
            owner = next(value for value in plan['files'] if value['path'] == primary) if item['editable'] is None else item
            atomic(path, content, mode=0o600 if content == model else item['mode'], uid=owner['uid'], gid=owner['gid'])

    def restore_sources(self):
        for item in self.journal.get('source_bridge', {}).get('files', []):
            path = Path(item['path'])
            bridge = json.dumps(item['bridge'], separators=(',', ':'), sort_keys=True).encode()
            if item['editable'] is None:
                require(not path.exists() or path.read_bytes() == bridge or digest(path.read_bytes()) == item['original_sha256'], 'Default Compose bridge changed; inspect before rollback')
                if item.get('original_backup'):
                    original = Path(item['original_backup']).read_bytes()
                    require(digest(original) == item['original_sha256'], 'Default Compose recovery snapshot changed')
                    atomic(path, original, mode=0o600, uid=item['uid'], gid=item['gid'])
                else:
                    path.unlink(missing_ok=True)
                continue
            editable = Path(item['editable'])
            require(path.read_bytes() == bridge or digest(path.read_bytes()) == item['original_sha256'], 'Compose bridge changed; inspect before restoring original inputs')
            require(digest(editable.read_bytes()) == item['original_sha256'], 'Editable inputs changed during setup; preserve them and inspect before rollback')
            atomic(path, editable.read_bytes(), mode=item['mode'], uid=item['uid'], gid=item['gid'])

    def deploy_server(self, force=False, detached=False):
        args = ('--force-recreate',) if force else ()
        if self.config.get('deployment_protocol') == 2:
            self.compose('stop', 'p2pstream')
            if self.journal.get('replacement_intent'):
                intent = self.verify_replacement_intent()
                require(self.cancellation_model_hash(self.current_model_path().read_bytes()) == intent['target_sha256'], 'Replacement target changed before creation')
                observed = self.server_container(allow_missing=True)
                require((self.cancellation_stable_server(observed) if observed else None) == getattr(self, '_replacement_server', None), 'Deployment changed before replacement creation')
                require(self.replacement_data()['owner']['allowed'] is False, 'Replacement admission changed before creation')
                self._replacement_stopped_target = {'server': self.cancellation_stable_server(observed), 'started_at': observed['State'].get('StartedAt'), 'restart_count': observed.get('RestartCount')} if observed else None
            # Compose config-change recreation can remove the old container
            # before replacement creation fails, even without --force-recreate.
            self.compose('up', '--no-start', '--no-deps', '--no-build', '--pull', 'never', *args, 'p2pstream')
            if detached:
                self.begin_detached_continuation()
                self.continue_detached_start()
            else:
                self.runtime('allow')
                self.compose('start', 'p2pstream')
        else:
            self.compose('up', '-d', '--no-deps', '--no-build', '--pull', 'never', *args, 'p2pstream')

    def migrate_deployment(self):
        migration = self.journal.get('deployment_migration')
        require(migration and self.journal['phase'] == 'migrating_deployment', 'No approved deployment migration exists')
        self.idle()
        old_config = read_json(self.directory / 'migration-config.json')
        old_model = (self.directory / 'migration-compose.json').read_bytes()
        require(digest(old_model) == migration['model_sha256'] and digest((self.directory / 'migration-config.json').read_bytes()) == migration['config_sha256'], 'Migration recovery snapshots changed')
        executor_lock = self.stop_executor()
        try:
            if self.config.get('deployment_protocol') != 2:
                require(self.config == old_config and self.current_model_path().read_bytes() == old_model, 'Legacy deployment changed after migration was approved')
                model = decode(old_model)
                cfg = dict(old_config, deployment_protocol=2, deployment_file=str(Path(self.journal['deployment_directory']) / 'compose.json'))
                server = model['services']['p2pstream']
                env = server['environment']
                require(env.get('SERVER_UPDATE_TOKEN') == cfg['token'], 'Legacy deployment credential differs from saved configuration')
                del env['SERVER_UPDATE_TOKEN']
                env.update(SERVER_UPDATE_TOKEN_FILE='/run/p2pstream-server-update/token', SERVER_UPDATE_RUNTIME_PROTOCOL='2', SERVER_UPDATE_GENERATION=migration['generation'])
                updater = model['services']['p2pstream-server-updater']
                updater['image'] = migration['updater_image']
                updater['volumes'].append({'type': 'bind', 'source': self.journal['deployment_directory'], 'target': self.journal['deployment_directory']})
                model_raw = json.dumps(model, separators=(',', ':'), sort_keys=True).encode()
                config_raw = json.dumps(cfg, separators=(',', ':'), sort_keys=True).encode()
                migration = dict(migration, converted_model_sha256=digest(model_raw), converted_config_sha256=digest(config_raw))
                self.phase('migrating_deployment', deployment_migration=migration)
                atomic(Path(cfg['control_dir']) / 'token', cfg['token'].encode(), mode=0o444)
                atomic(Path(cfg['deployment_file']), model_raw, mode=0o640)
                atomic(self.directory / 'config.json', config_raw)
                self.config = cfg
            require(digest(self.current_model_path().read_bytes()) == migration['converted_model_sha256'] and digest((self.directory / 'config.json').read_bytes()) == migration['converted_config_sha256'], 'Converted deployment changed during migration')
            if self.journal.get('source_bridge'):
                self.runtime('block')
                self.source_bridges()
            raw = docker('compose', *self.input_options(), 'config', '--format', 'json', cwd=self.journal['source_directory']).stdout
            atomic(self.directory / 'migration-input.json', raw)
            self.command('configure', '--compose', str(self.directory / 'migration-input.json'), '--output', str(self.directory / 'migration-validated.json'))
            require(decode(self.compose('config', '--format', 'json', model='migration-validated.json').stdout) == decode(self.compose('config', '--format', 'json').stdout), 'Editable settings differ from the current managed deployment; restore the last applied inputs before migration')
            self.runtime('block')
            self.source_bridges()
            self.deploy_server()
        finally:
            os.close(executor_lock)
        self.start_executor()
        status = self.wait_healthy()
        self.record_runtime()
        atomic(self.directory / 'enrolled-compose.json', self.current_model_path().read_bytes())
        self.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
        print('Compose deployment migrated; original Compose commands now use the current managed image and settings.')

    @contextlib.contextmanager
    def lock(self, allow_archive=False):
        require(os.geteuid() == 0, 'Run this host operation with sudo')
        parent = self.directory.parent.lstat()
        require(stat.S_ISDIR(parent.st_mode) and parent.st_uid == 0 and not parent.st_mode & 0o022, 'Unsafe updater directory parent')
        self.directory.mkdir(mode=0o700, exist_ok=True)
        info = self.directory.lstat()
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == 0 and not info.st_mode & 0o077, 'Updater state must be a root-owned private directory without symlinks')
        fd = os.open(self.directory / 'host.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise Failure('Another installation, host operation, or update acceptance owns this deployment; retry later') from error
            if (self.directory / 'archive.json').exists():
                if not (self.directory / 'host.json').exists():
                    self.finish_archive()
                else:
                    if not allow_archive and os.path.lexists(self.directory / CANCELLATION) and not read_json(self.directory / CANCELLATION).get('completed'):
                        raise Failure('Enrollment cancellation archival is incomplete; retry the verified cancellation command')
                    require(allow_archive, 'Interrupted archival must finish before host operations. For unactivated preparation run sudo ' + str(self.directory / 'manage') + ' discard-preparation; for completed removal retry the fresh setup block.')
            if (self.directory / 'host.json').exists():
                self.journal = read_json(self.directory / 'host.json')
            if (self.directory / 'config.json').exists():
                self.config = read_json(self.directory / 'config.json')
                model = read_json(self.current_model_path())
                self.updater = self.journal.get('updater_image') or model.get('services', {}).get('p2pstream-server-updater', {}).get('image', '')
                require(re.fullmatch(re.escape('ghcr.io/' + self.config['repository'].lower() + '-updater@sha256:') + '[0-9a-f]{64}', self.updater), 'Host operations require the enrolled published updater image')
            yield
        finally:
            os.close(fd)

    def phase(self, phase, **values):
        self.journal.update(values)
        self.journal['phase'] = phase
        if phase in {'healthy', 'rolled_back', 'removed'}:
            self.journal.pop('replacement_intent', None)
            self.journal.pop('rollback_target', None)
            self.journal.pop('rollback_transition', None)
            self.journal.pop('detached_continuation', None)
        if phase == 'healthy':
            self.journal.pop('recovery_phase', None)
            self.journal.pop('recovery_operation', None)
            self.journal.pop('recreation', None)
        self.journal['updated_at'] = int(time.time())
        atomic(self.directory / 'host.json', self.journal)

    def helper_name(self):
        return 'p2pstream-server-update-host-' + self.journal['installation']

    def settle_helper(self):
        path = self.directory / 'helper.json'
        if not path.exists():
            return
        pending = read_json(path)
        name = self.helper_name()
        require(pending['name'] == name, 'Host helper record belongs to another installation')
        try:
            # A successful daemon inventory, not a failed inspect, proves absence.
            names = docker('ps', '--all', '--filter', 'label=p2pstream.server-update.host-helper=' + self.journal['installation'], '--format', '{{.Names}}').stdout.decode().split()
            require(all(n == name for n in names), 'Unexpected duplicate host helper; inspect Docker on the host')
            if name in names:
                result = docker('kill', name, check=False, timeout=30)
                # Already-exited helpers also appear in the inventory. Confirm
                # the canonical state before removing; never race a live writer.
                info = docker('inspect', name, check=False, timeout=30)
                if info.returncode == 0:
                    require(not decode(info.stdout)[0]['State']['Running'], 'Host helper is still running')
                    docker('rm', '--force', name, timeout=30)
                else:
                    names = docker('ps', '--all', '--filter', 'label=p2pstream.server-update.host-helper=' + self.journal['installation'], '--format', '{{.Names}}', timeout=30).stdout.decode().split()
                    require(name not in names, 'Cannot confirm host helper termination')
            path.unlink()
        except Exception as error:
            previous = self.journal.get('recovery_phase') or pending.get('phase', self.journal['phase'])
            self.phase('recovery_required', recovery_phase=previous, error='Cannot confirm host helper termination: ' + str(error))
            raise UnsettledHelper('Host helper may still be running. Do not start competing recovery. Restore Docker access, then sudo ' + str(self.directory / 'manage') + ' repair') from error

    def cancellation_helper_args(self):
        return [tuple(('compose', '--project-name', self.config['project'], '--project-directory', str(path.parent), '-f', str(path), *operation))
                for path, operation in ((self.current_model_path(), ('ps', '--all', '--quiet', 'p2pstream')),
                                        (self.directory / 'before-compose.json', ('config', '--hash', 'p2pstream')))]

    def run_helper(self, entrypoint, args, *, mounts=(), readonly=False, image=None, timeout=180, cancellation_validation=False):
        if readonly:
            require(not (self.directory / 'helper.json').exists(), 'An unfinished host helper exists; use explicit repair or recover-update before inspection')
        self.settle_helper()
        name = self.helper_name()
        pending = {'name': name, 'entrypoint': entrypoint, 'phase': self.journal['phase'], 'readonly': readonly}
        if cancellation_validation:
            require(readonly and entrypoint == '/usr/local/bin/docker' and tuple(args) in self.cancellation_helper_args() and not mounts and image is None, 'Unexpected cancellation validation helper')
            pending.update(cancellation_validation=True, cancellation_args=list(args))
        atomic(self.directory / 'helper.json', pending)
        mount = 'type=bind,src=' + str(self.directory) + ',dst=' + str(self.directory) + (',readonly' if readonly else '')
        deployment = self.config.get('deployment_file') if self.config else None
        deployment_directory = str(Path(deployment).parent) if deployment else self.journal.get('deployment_directory')
        if deployment_directory and Path(deployment_directory) != self.directory:
            mounts = (*mounts, '--mount', 'type=bind,src=' + deployment_directory + ',dst=' + deployment_directory + (',readonly' if readonly else ''))
        try:
            result = docker('run', '--rm', '--name', name, '--label', 'p2pstream.server-update.host-helper=' + self.journal['installation'],
                            '--user', '0:0', '--env', 'COMPOSE_DISABLE_ENV_FILE=1', '--entrypoint', entrypoint,
                            '--mount', 'type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock',
                            '--mount', mount, *mounts, image or self.updater, *args, timeout=timeout, check=False)
            if result.returncode:
                diagnostic = self.directory / 'helper-failure.log'
                action = 'host helper'
                if entrypoint == '/app/p2pstream' and len(args) >= 2 and args[0] == 'server-updater' and args[1] in {'check', 'enroll', 'configure', 'recover', 'runtime-block', 'runtime-allow', 'runtime-detach', 'record-runtime'}:
                    action = 'server-updater ' + args[1]
                elif entrypoint == '/usr/local/bin/docker' and args and args[0] == 'compose':
                    action = 'Docker Compose'
                header = {'time': time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()), 'check': action, 'exit': result.returncode}
                atomic(diagnostic, json.dumps(header).encode() + b'\n' + result.stdout + b'\n' + result.stderr)
                self._helper_failure_diagnostic = diagnostic
                raise Failure('Host helper failed (exit ' + str(result.returncode) + '); private diagnostic: ' + str(diagnostic))
        except BaseException:
            # Killing the Docker client does not kill its container. Confirm the
            # helper is stopped before rollback or releasing host ownership.
            self.settle_helper()
            raise
        self.settle_helper()
        return result

    def tools(self, *args, image=None, readonly=False, cancellation_validation=False):
        # Read Compose inputs on the HOST, never inside this container. Only
        # normalized snapshots underneath the private state directory are used.
        return self.run_helper('/usr/local/bin/docker', args, image=image, readonly=readonly, cancellation_validation=cancellation_validation)

    def compose(self, *args, model='compose.json'):
        path = self.model_path(model)
        return self.tools('compose', '--project-name', self.config['project'], '--project-directory', str(path.parent), '-f', str(path), *args, readonly=bool(args and args[0] in {'ps', 'logs', 'config'}), cancellation_validation=bool(getattr(self, '_cancellation_validation', False)))

    def command(self, *args):
        mounts = []
        if args and args[0] in {'recover', 'runtime-block', 'runtime-allow', 'runtime-detach'}:
            mounts = ['--label', 'p2pstream.server-update.executor=' + self.config['instance_id'],
                      '--mount', 'type=volume,src=' + self.config['data_volume'] + ',dst=/server-data']
        return self.run_helper('/app/p2pstream', ('server-updater', *args), mounts=mounts, readonly=bool(args and args[0] == 'check'), timeout=900 if args and args[0] == 'recover' else 180)

    def idle(self):
        if (self.directory / 'state.json').exists():
            operation = read_json(self.directory / 'state.json').get('operation')
            require(not operation or operation['phase'] in TERMINAL, 'An update or paused data recovery is active. Run the documented recover-update command before host configuration/removal.')
        require(not (self.directory / 'control' / 'maintenance').exists(), 'Server maintenance is active; use the documented update recovery command')

    def status_socket(self):
        cfg = self.config
        class UnixConnection(http.client.HTTPConnection):
            def connect(connection):
                connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                connection.sock.settimeout(5)
                connection.sock.connect(str(Path(cfg['control_dir']) / 'control.sock'))
        conn = UnixConnection('updater', timeout=5)
        try:
            conn.request('POST', '/status', '{}', {'Authorization': 'Bearer ' + cfg['token'], 'Content-Type': 'application/json'})
            response = conn.getresponse()
            require(response.status == 200, 'Updater is not reachable/authenticated')
            result = decode(response.read(65537))
            require(result['overview']['instance_id'] == cfg['instance_id'], 'Private updater belongs to another installation')
        finally:
            conn.close()

    def wait_socket(self):
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            try:
                self.status_socket()
                return
            except (OSError, Failure, ValueError):
                time.sleep(1)
        raise Failure('Private updater did not become reachable')

    def healthy(self):
        self._helper_failure_diagnostic = None
        self._health_check_stage = 'private updater authentication'
        self.status_socket()
        self._health_check_stage = 'server readiness and deployment verification'
        result = self.command('check')
        self._health_check_stage = 'verification response decoding'
        return decode(result.stdout)

    def wait_healthy(self):
        deadline = time.monotonic() + 120
        consecutive = 0
        last_failure = None
        while time.monotonic() < deadline:
            try:
                self._health_check_stage = 'server verification'
                self._helper_failure_diagnostic = None
                status = self.healthy()
                consecutive += 1
                if consecutive >= 3:
                    return status
            except UnsettledHelper:
                raise  # Preserve the explicit helper-settlement recovery boundary.
            except (Failure, OSError, ValueError) as error:
                consecutive = 0
                # Raw exception text can contain private response or filesystem
                # values. Report the fixed stage and point to attributable local
                # helper output when this attempt actually produced it.
                detail = 'verification refused'
                if isinstance(error, OSError):
                    detail = 'local operating-system error'
                    if type(error.errno) is int:
                        detail += ' (errno ' + str(error.errno) + ')'
                elif isinstance(error, ValueError):
                    detail = 'invalid verification response'
                last_failure = self._health_check_stage + ': ' + detail
                if self._helper_failure_diagnostic:
                    last_failure += '; private diagnostic: ' + str(self._helper_failure_diagnostic)
            time.sleep(2)
        message = 'Server verification did not reach three consecutive successful checks'
        if last_failure:
            message += '. Last failure during ' + last_failure
        raise Failure(message)

    def archive_removed(self):
        require(self.journal.get('phase') == 'removed', 'Only a completed removal can be reenrolled')
        self.settle_helper()
        self.wait_original()
        self.save_reenrollment_context()
        self.archive_state()
        self.journal = {}
        self.config = None
        self.updater = None

    def save_reenrollment_context(self):
        require(self.journal.get('phase') == 'removed', 'Reenrollment ownership requires a completed removal')
        plan = self.journal.get('source_bridge')
        if not plan:
            return
        exported = plan.get('export_sha256', '')
        require(HEX.fullmatch(exported), 'Completed removal lacks an exported deployment digest')
        primary = self.journal['source_layout']['primary']
        aliases = {item['path']: exported for item in plan['files'] if item['editable'] is None}
        context = {'installation': self.journal['installation'], 'source_directory': self.journal['source_directory'],
                   'compose_options': self.journal['compose_options'], 'source_files': self.journal['source_files'],
                   'primary': primary, 'export_sha256': exported, 'aliases': aliases}
        self.validate_reenrollment_context(context)
        atomic(self.directory / 'reenrollment.json', context)

    def validate_reenrollment_context(self, context):
        require(context['primary'] in context['source_files'] and HEX.fullmatch(context['export_sha256']), 'Unsafe reenrollment ownership receipt')
        for name in context['source_files']:
            path = Path(name)
            require(path.is_absolute() and stat.S_ISREG(path.lstat().st_mode), 'Recorded Compose sources must remain regular files without symlinks')
        # Selected source files remain operator-editable after removal. Their
        # current resolved settings must pass normal running-model preflight;
        # historical content ownership is needed only for the extra alias.
        existing_aliases = {}
        for name, expected in context['aliases'].items():
            path = Path(name)
            require(path.is_absolute() and expected == context['export_sha256'], 'Exported default Compose alias changed; inspect it before reenrollment')
            if not os.path.lexists(path):
                continue  # No existing file needs historical ownership proof.
            require(stat.S_ISREG(path.lstat().st_mode) and digest(path.read_bytes()) == expected, 'Exported default Compose alias changed; inspect it before reenrollment')
            existing_aliases[name] = expected
        return existing_aliases

    def discard_preparation(self):
        phase = self.journal.get('recovery_phase') or self.journal['phase']
        require(phase == 'preparing' and not os.path.lexists(self.directory / 'config.json'), 'Preparation can only be discarded before enrollment configuration exists; use repair or rollback')
        self.settle_helper()
        phase = self.journal.get('recovery_phase') or self.journal['phase']
        require(phase == 'preparing' and not os.path.lexists(self.directory / 'config.json'), 'Preparation can only be discarded before enrollment configuration exists; use repair or rollback')
        require(not os.path.lexists(self.directory / 'state.json') and not os.path.lexists(self.directory / 'control' / 'maintenance'), 'Update or maintenance state exists; preparation cannot be discarded')
        executors = docker('ps', '--all', '--quiet', '--filter', 'label=p2pstream.server-update.executor=' + self.journal['installation']).stdout.decode().split()
        require(not executors, 'An enrolled executor exists; preparation cannot be discarded')
        self.archive_state()
        print('Unactivated preparation archived privately. Deployment unchanged. Correct/apply your original Compose inputs, then copy a fresh setup command from the selected server.')

    def validate_cancelled_original(self):
        require(self.journal.get('phase') == 'rolled_back' and self.config, 'Only an already rolled-back initial enrollment can be cancelled; use repair or rollback first')
        require(not self.journal.get('verified_version') and not self.journal.get('verified_commit') and not self.journal.get('deployment_migration'), 'A previously enabled enrollment cannot be cancelled')
        require(self.config.get('deployment_protocol') == 2, 'Cancellation requires managed deployment ownership')
        require(not os.path.lexists(self.directory / 'state.json') and not os.path.lexists(self.directory / 'control' / 'maintenance'), 'Update history or maintenance exists; enrollment cancellation is refused')
        require(not os.path.lexists(self.directory / 'helper.json'), 'An unfinished helper exists; use repair or rollback before cancellation')
        helpers = docker('ps', '--all', '--quiet', '--filter', 'label=p2pstream.server-update.host-helper=' + self.journal['installation']).stdout.decode().split()
        require(not helpers, 'A host helper still exists; cancellation is refused')
        raw = (self.directory / 'before-compose.json').read_bytes()
        require(digest(raw) == self.journal.get('before_sha256'), 'Original deployment snapshot changed; cancellation is refused')
        original = decode(raw)
        require('p2pstream-server-updater' not in original['services'], 'Original deployment was already enrolled; cancellation is refused')
        server = original['services']['p2pstream']
        require(not any(key.startswith('SERVER_UPDATE_') for key in server.get('environment', {})) and not any(key.startswith('p2pstream.server-update.') for key in server.get('labels', {})), 'Original deployment retains an updater binding')
        require(not any(v.get('target') == '/run/p2pstream-server-update' or v.get('target', '').startswith('/run/p2pstream-server-update/') for v in server.get('volumes', [])), 'Original deployment retains an updater control mount')
        require(read_json(self.current_model_path()) == original, 'Current model differs from the restored original deployment')
        for item in self.journal.get('source_bridge', {}).get('files', []):
            path = Path(item['path'])
            if item['editable'] is None and not item.get('original_backup'):
                require(not os.path.lexists(path), 'A generated Compose bridge remains after rollback')
            else:
                require(stat.S_ISREG(path.lstat().st_mode) and digest(path.read_bytes()) == item['original_sha256'], 'Original Compose source changed after rollback')
        source = decode(docker('compose', *self.journal['compose_options'], 'config', '--format', 'json', cwd=self.journal['source_directory']).stdout)
        # Enrollment replaces only the input image reference with its publisher-
        # verified digest; source files may retain their equivalent release tag.
        require(source['services']['p2pstream'].get('image') == read_json(self.directory / 'input.json')['services']['p2pstream'].get('image'), 'Original Compose image input changed')
        source['services']['p2pstream']['image'] = server['image']
        require(source == original, 'Resolved original Compose inputs changed after rollback')
        info = self.server_container()
        require(info['State']['Running'] and info['Image'] == self.journal['before_image_id'], 'Original running image was not restored; cancellation is refused')
        labels = info['Config'].get('Labels', {})
        require(labels.get('com.docker.compose.project') == self.config['project'] and labels.get('com.docker.compose.service') == 'p2pstream', 'Original server belongs to another Compose deployment')
        fields = self.compose('config', '--hash', 'p2pstream', model='before-compose.json').stdout.decode().split()
        require(len(fields) == 2 and fields[0] == 'p2pstream' and HEX.fullmatch(fields[1]) and labels.get('com.docker.compose.config-hash') == fields[1], 'Running original settings differ from the rollback snapshot')
        require(not any(key.startswith('p2pstream.server-update.') for key in labels) and not any(value.partition('=')[0].startswith('SERVER_UPDATE_') for value in info['Config'].get('Env', [])), 'Running original server retains an updater binding')
        require(not any(m['Destination'] == '/run/p2pstream-server-update' or m['Destination'].startswith('/run/p2pstream-server-update/') for m in info['Mounts']), 'Running original server retains an updater control mount')
        mounts = [m for m in info['Mounts'] if m['Destination'] == '/data']
        require(len(mounts) == 1 and mounts[0].get('Name') == self.config['data_volume'] and mounts[0].get('RW') and not any(m['Destination'].startswith('/data/') for m in info['Mounts']), 'Original data volume was not restored')
        require(docker('exec', info['Id'], '/app/p2pstream', 'server-installation-identity').stdout.decode().strip() == self.config['instance_id'] == self.journal['installation'], 'Original installation identity changed')
        require(docker('exec', info['Id'], '/app/p2pstream', 'server-health', check=False).returncode == 0, 'Original server is unhealthy; cancellation is refused')
        # Inspect through the exact restored image, without mounting a second
        # writer or altering the stable lock inode. Broken owner links also fail.
        owner = docker('exec', info['Id'], '/bin/sh', '-c', 'test -d /data && test ! -e /data/.p2pstream-runtime-owner.json && test ! -L /data/.p2pstream-runtime-owner.json', check=False)
        require(owner.returncode == 0, 'Runtime ownership remains or cannot be inspected; cancellation is refused')
        require_data_writers(self.config['data_volume'], info['Id'])

    def validate_cancelled_upgrade(self, args):
        # Old controllers can leave bare recovery_required after refusing an
        # external image change. It is not evidence of a completed rollback:
        # prove the entire current deployment independently, without a helper,
        # phase rewrite, source restoration, or execution of the saved model.
        require(self.config and self.config.get('deployment_protocol') == 2, 'Upgraded cancellation requires managed deployment ownership')
        require(self.journal.get('phase') in {'rolled_back', 'recovery_required'} and not self.journal.get('recovery_phase') and not self.journal.get('recovery_operation'), 'Upgraded cancellation belongs to another host operation')
        require(not self.journal.get('verified_version') and not self.journal.get('verified_commit') and not self.journal.get('deployment_migration'), 'A previously enabled enrollment cannot be cancelled')
        require(not os.path.lexists(self.directory / 'state.json') and not os.path.lexists(self.directory / 'control' / 'maintenance'), 'Update history or maintenance exists; enrollment cancellation is refused')
        require(not os.path.lexists(self.directory / 'helper.json'), 'An unfinished helper exists; upgraded cancellation is refused')
        helpers = docker('ps', '--all', '--quiet', '--filter', 'label=p2pstream.server-update.host-helper=' + self.journal['installation']).stdout.decode().split()
        require(not helpers, 'A host helper still exists; cancellation is refused')
        history = self.directory / 'history'
        if os.path.lexists(history):
            require(stat.S_ISDIR(history.lstat().st_mode), 'Unsafe enrollment history')
            transaction = read_json(self.directory / 'archive.json') if os.path.lexists(self.directory / 'archive.json') else None
            partial = self.archive_directory(transaction) if transaction else None
            for archive in history.iterdir():
                require(stat.S_ISDIR(archive.lstat().st_mode) and not os.path.lexists(archive / 'state.json') and not os.path.lexists(archive / 'control' / 'maintenance'), 'Prior update history exists; upgraded cancellation is refused')
                if archive == partial:
                    receipt = cancellation_receipt(self.directory, args.expect_installation, args.repository, args.expect_channel)
                    require(transaction['stage'] in {'copying', 'copied'} and receipt and not receipt['completed'], 'Partial archive is not an active enrollment cancellation')
                    # copy2 may leave a present but truncated destination. This
                    # exact transaction's intact active journal is authoritative
                    # until the archive commit removes it; all other history is
                    # still parsed strictly below.
                    continue
                previous = read_json(archive / 'host.json')
                require(previous.get('installation') == self.journal['installation'] and previous.get('phase') in {'preparing', 'rolled_back', 'recovery_required'} and not any(previous.get(key) for key in ('verified_version', 'verified_commit', 'deployment_migration', 'recovery_phase', 'recovery_operation')), 'Prior verified enrollment history exists; upgraded cancellation is refused')
        raw = (self.directory / 'before-compose.json').read_bytes()
        require(digest(raw) == self.journal.get('before_sha256'), 'Original deployment snapshot changed; cancellation is refused')
        original = decode(raw)
        require('p2pstream-server-updater' not in original['services'], 'Original deployment was already enrolled; cancellation is refused')
        server = original['services']['p2pstream']
        require(not any(key.startswith('SERVER_UPDATE_') for key in server.get('environment', {})) and not any(key.startswith('p2pstream.server-update.') for key in server.get('labels', {})), 'Original deployment retains an updater binding')
        require(not any(v.get('target') == '/run/p2pstream-server-update' or v.get('target', '').startswith('/run/p2pstream-server-update/') for v in server.get('volumes', [])), 'Original deployment retains an updater control mount')
        saved = read_json(self.current_model_path())
        require(saved == original or saved == read_json(self.directory / 'enrolled-compose.json'), 'Saved model differs from the initial enrollment snapshots')
        options, snapshots, replacements = self.upgraded_cancellation_sources()
        arch = local_engine()
        require(arch == args.expect_arch == self.journal['architecture'], 'Docker daemon architecture differs from selected server')
        recorded = self.blocked_owner_archive()
        retry = recorded and recorded['api'] == 2
        if retry:
            # This resumes only an already admitted archival transaction. No
            # cached manifest can admit a new cancellation or a different image.
            receipt = cancellation_receipt(self.directory, args.expect_installation, args.repository, args.expect_channel)
            release = recorded['release']
            identity = self.cancellation_release_identity(args, arch)
            require(set(release) == set(identity) | {'image'} and all(release[key] == value for key, value in identity.items()), 'Cancellation retry release identity differs')
            require(receipt and not receipt['completed'] and receipt['floor'] == recorded['provenance']['floor'], 'Blocked initial owner archive or retained floor differs')
            floor = validate_floor(receipt['floor'])
            require(floor['manifest_sha256'] == args.manifest_sha256, 'Cancellation retry manifest differs from retained floor')
            server_image = release['image']
            require(isinstance(server_image, str) and re.fullmatch(re.escape('ghcr.io/' + args.repository.lower() + '@sha256:') + '[0-9a-f]{64}', server_image), 'Invalid admitted cancellation image')
            manifest, manifest_hash = floor, floor['manifest_sha256']
        else:
            manifest, _, server_image, manifest_hash = release_metadata(args.repository, args.expect_version, args.expect_commit, args.expect_channel, args.manifest_sha256)
            floor = {key: manifest[key] for key in ('sequence', 'security_epoch', 'minimum_safe_version')} | {'manifest_sha256': manifest_hash}
            platform = [p for p in manifest['oci_images'][0]['platforms'] if p['os'] == 'linux' and p['arch'] == arch]
            require(len(platform) == 1, 'Release lacks the selected server platform')
            # Fresh admission always verifies the current, unexpired release.
            docker('pull', '--quiet', '--platform', 'linux/' + arch, server_image)
        self._cancellation_release = self.cancellation_release_identity(args, arch) | {'image': server_image}
        require_floor(floor, self.config['bootstrap_floor'])
        check_cancellation_floor(self.directory, args, manifest, manifest_hash)
        version = args.expect_version.partition('-')[0]
        require(stable_version(version) > stable_version(floor['minimum_safe_version']) or version == floor['minimum_safe_version'] and '-' not in args.expect_version, 'Selected server is below the current minimum safe version')
        require(args.expect_version != self.journal['version'] and floor['sequence'] > self.config['bootstrap_floor']['sequence'], 'Upgraded cancellation requires a newer published release')
        published = decode(docker('image', 'inspect', server_image).stdout)[0]
        source_dir = self.journal['source_directory']
        source_raw = docker('compose', *options, 'config', '--format', 'json', cwd=source_dir).stdout
        source = decode(source_raw)
        source_image = source['services']['p2pstream']['image']
        source['services']['p2pstream']['image'] = server['image']
        require(source == original, 'Only the server image may change after failed enrollment; resolved Compose settings differ')
        source['services']['p2pstream']['image'] = source_image
        ids = docker('compose', *options, 'ps', '--all', '--quiet', 'p2pstream', cwd=source_dir).stdout.decode().split()
        require(len(ids) == 1, 'Expected exactly one existing upgraded server')
        inspection = decode(docker('inspect', ids[0]).stdout)
        require(len(inspection) == 1 and inspection[0]['Id'] == ids[0], 'Server inspection differs from Compose inventory')
        info = inspection[0]
        require(not info['State'].get('Paused') and not info['State'].get('Restarting'), 'Upgraded server is not stably running')
        require(info['Image'] == published['Id'] and info['Image'] != self.journal['before_image_id'], 'Running image is not the upgraded canonical release image')
        require(decode(docker('image', 'inspect', source_image).stdout)[0]['Id'] == info['Image'], 'Compose image input differs from the running published image')
        require(validate_running(info, source, published, arch, args) == self.config['data_volume'], 'Original data volume was not preserved')
        require(source['name'] == self.config['project'], 'Upgraded server belongs to another Compose deployment')
        with tempfile.TemporaryDirectory(prefix='p2pstream-cancellation-') as tmp:
            path = Path(tmp) / 'compose.json'
            atomic(path, source_raw)
            fields = docker('compose', '--project-directory', source_dir, '-f', str(path), 'config', '--hash', 'p2pstream').stdout.decode().split()
        labels = info['Config'].get('Labels', {})
        require(len(fields) == 2 and fields[0] == 'p2pstream' and HEX.fullmatch(fields[1]) and labels.get('com.docker.compose.config-hash') == fields[1], 'Running upgraded settings differ from current Compose inputs')
        require(not any(key.startswith('p2pstream.server-update.') for key in labels) and not any(value.partition('=')[0].startswith('SERVER_UPDATE_') for value in info['Config'].get('Env', [])), 'Running upgraded server retains an updater binding')
        require(not any(m['Destination'] == '/run/p2pstream-server-update' or m['Destination'].startswith('/run/p2pstream-server-update/') for m in info['Mounts']), 'Running upgraded server retains an updater control mount')
        require(docker('exec', info['Id'], '/app/p2pstream', 'server-installation-identity').stdout.decode().strip() == self.config['instance_id'] == self.journal['installation'], 'Original installation identity changed')
        require(docker('exec', info['Id'], '/app/p2pstream', 'server-health', check=False).returncode == 0, 'Upgraded server is unhealthy; cancellation is refused')
        require_data_writers(self.config['data_volume'], info['Id'])
        owner = self.prove_blocked_initial_owner(info, args, floor, source_raw)
        self._cancelled_upgrade = {'options': options, 'model': source_raw, 'server': self.cancellation_server_identity(info), 'snapshots': snapshots, 'replacements': replacements, 'owner': owner}
        self.check_cancelled_upgrade(options)
        for name, snapshot in snapshots.items():
            require(self.cancellation_source_snapshot(Path(name)) == snapshot, 'Compose source changed during cancellation validation')
        return floor

    @staticmethod
    def cancellation_inode(metadata, limit):
        fields = metadata.split(':', 7)
        require(len(fields) == 8 and all(re.fullmatch(r'[0-9]+', fields[i]) for i in (0, 1, 3, 4, 5, 6)) and re.fullmatch(r'[0-9a-f]+', fields[2]), 'Unsafe runtime ownership metadata')
        device, inode, mode, links, uid, gid, size = [int(value, 16 if i == 2 else 10) for i, value in enumerate(fields[:7])]
        require(stat.S_ISREG(mode) and not mode & 0o022 and links == 1 and size <= limit, 'Unsafe runtime ownership metadata')
        return device, inode

    def cancellation_owner_snapshot(self, container):
        absent = docker('exec', container, '/bin/sh', '-c', OWNER_ABSENT, check=False, timeout=15)
        if absent.returncode == 0:
            return None
        result = docker('exec', container, '/bin/sh', '-c', OWNER_PROBE, check=False, timeout=15)
        require(result.returncode == 0, 'Runtime ownership remains or cannot be inspected; cancellation is refused')
        metadata, separator, raw = result.stdout.partition(b'\n')
        require(separator and 0 < len(raw) <= 4096, 'Unsafe runtime owner marker')
        metadata = metadata.decode('ascii')
        self.cancellation_inode(metadata, 4096)
        return {'metadata': metadata, 'data_hex': raw.hex(), 'sha256': digest(raw)}

    def cancellation_live_lease(self, info):
        # The published standalone binary must be PID 1 in its own namespace.
        # Thus a kernel FLOCK entry for PID 1 proves this exact server holds the
        # stable writer lease, without attempting to acquire or replace it.
        require(info['Config'].get('Cmd') == ['/app/p2pstream', 'server'] and not info['Config'].get('Entrypoint') and not info.get('HostConfig', {}).get('Init') and not info.get('HostConfig', {}).get('PidMode'), 'Cannot prove the standalone server lifetime lease')
        result = docker('exec', info['Id'], '/bin/sh', '-c', LEASE_PROBE, check=False, timeout=15)
        require(result.returncode == 0, 'Cannot inspect the standalone server lifetime lease')
        directory, separator, remainder = result.stdout.partition(b'\n')
        require(separator and re.fullmatch(rb'[0-9]+:[0-9]+', directory), 'Cannot inspect the runtime data directory')
        metadata, separator, locks = remainder.partition(b'\n')
        require(separator and len(locks) <= 65536, 'Cannot inspect the standalone server lifetime lease')
        metadata = metadata.decode('ascii')
        device, inode = self.cancellation_inode(metadata, 4096)
        held = []
        for line in locks.decode('ascii').splitlines():
            match = re.fullmatch(r'[0-9]+: +FLOCK +ADVISORY +WRITE +1 +([0-9a-f]+):([0-9a-f]+):([0-9]+) +0 +EOF', line)
            if match and (int(match[1], 16), int(match[2], 16), int(match[3])) == (os.major(device), os.minor(device), inode):
                held.append(':'.join(match.groups()))
        require(len(held) == 1, 'Standalone server does not hold the stable lifetime lease')
        return {'metadata': metadata, 'key': held[0], 'directory': directory.decode('ascii')}

    @staticmethod
    def cancellation_stat_matches(info, metadata):
        fields = metadata.split(':', 7)
        identity = [int(value, 16 if i == 2 else 10) for i, value in enumerate(fields[:7])]
        dates = re.fullmatch(r'(.*)\.([0-9]{9}) ([+-][0-9]{4}):(.*)\.([0-9]{9}) ([+-][0-9]{4})', fields[7])
        require(dates, 'Invalid runtime ownership timestamps')
        times = [int(datetime.datetime.strptime(dates[i] + dates[i + 2], '%Y-%m-%d %H:%M:%S%z').timestamp()) * 10 ** 9 + int(dates[i + 1]) for i in (1, 4)]
        return identity + times == [info.st_dev, info.st_ino, info.st_mode, info.st_nlink, info.st_uid, info.st_gid, info.st_size, info.st_mtime_ns, info.st_ctime_ns]

    @contextlib.contextmanager
    def cancellation_data_directory(self, info, lease):
        volume = decode(docker('volume', 'inspect', self.config['data_volume']).stdout)
        require(len(volume) == 1 and volume[0].get('Name') == self.config['data_volume'] and volume[0].get('Driver') == 'local' and volume[0].get('Scope') == 'local', 'Cannot prove the local runtime data volume')
        mountpoint = volume[0].get('Mountpoint')
        mounts = [m for m in info['Mounts'] if m['Destination'] == '/data']
        require(isinstance(mountpoint, str) and Path(mountpoint).is_absolute() and len(mounts) == 1 and mounts[0].get('Source') == mountpoint and mounts[0].get('Type') == 'volume', 'Runtime data mountpoint differs from the local volume')
        try:
            fd = os.open(mountpoint, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        except OSError as error:
            raise Failure('Runtime data volume is not locally accessible; cancellation is refused') from error
        try:
            current = os.fstat(fd)
            require(f'{current.st_dev}:{current.st_ino}' == lease['directory'], 'Local runtime data directory differs from the running container')
            yield fd
        finally:
            os.close(fd)

    def check_local_cancellation_owner(self, directory, snapshot, lease):
        current_lock = os.stat('.p2pstream-runtime.lock', dir_fd=directory, follow_symlinks=False)
        require(self.cancellation_stat_matches(current_lock, lease['metadata']), 'Local stable lifetime lock differs from the running container')
        name = '.p2pstream-runtime-owner.json'
        if snapshot is None:
            try:
                os.stat(name, dir_fd=directory, follow_symlinks=False)
            except FileNotFoundError:
                return
            raise Failure('Runtime ownership changed during cancellation validation')
        fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        try:
            require(self.cancellation_stat_matches(os.fstat(fd), snapshot['metadata']), 'Local runtime owner differs from the running container')
            raw = os.read(fd, 4097)
            require(raw.hex() == snapshot['data_hex'] and digest(raw) == snapshot['sha256'], 'Local runtime owner bytes differ from the archived marker')
            require(self.cancellation_stat_matches(os.fstat(fd), snapshot['metadata']) and self.cancellation_stat_matches(os.stat(name, dir_fd=directory, follow_symlinks=False), snapshot['metadata']), 'Runtime owner changed during cancellation validation')
        finally:
            os.close(fd)

    @staticmethod
    def cancellation_release_identity(args, arch):
        return {'repository': args.repository, 'version': args.expect_version, 'commit': args.expect_commit,
                'channel': args.expect_channel, 'architecture': arch, 'manifest_sha256': args.manifest_sha256}

    def blocked_owner_archive(self):
        path = self.directory / BLOCKED_INITIAL_OWNER
        if not os.path.lexists(path):
            return None
        metadata = path.lstat()
        require(stat.S_ISREG(metadata.st_mode) and metadata.st_nlink == 1 and not metadata.st_mode & 0o022 and metadata.st_uid == 0, 'Unsafe blocked initial owner archive')
        recorded = read_json(path)
        require(type(recorded.get('api')) is int and recorded['api'] in {1, 2} and set(recorded) == ({'api', 'owner', 'lease', 'provenance'} | ({'release'} if recorded['api'] == 2 else set())), 'Invalid blocked initial owner archive')
        return recorded

    @staticmethod
    def cancellation_stable_lease(lease):
        # Timestamp changes are not ownership changes between attempts. Every
        # per-attempt comparison still includes the complete fresh stat/lease.
        return {'directory': lease['directory'], 'metadata': ':'.join(lease['metadata'].split(':', 7)[:7])}

    @staticmethod
    def cancellation_stable_server(info):
        value = {key: info.get(key) for key in ('Id', 'Image', 'Config', 'HostConfig', 'Mounts')}
        value['Mounts'] = sorted(info['Mounts'], key=lambda mount: json.dumps(mount, sort_keys=True, separators=(',', ':')))
        return value

    @staticmethod
    def cancellation_model_hash(raw):
        return digest(json.dumps(decode(raw), sort_keys=True, separators=(',', ':')).encode())

    def prove_blocked_initial_owner(self, info, args, floor, source_raw):
        snapshot = self.cancellation_owner_snapshot(info['Id'])
        path = self.directory / BLOCKED_INITIAL_OWNER
        if snapshot is None and not os.path.lexists(path):
            return None  # Preserve the original owner-absent cancellation path.
        enrolled_raw = (self.directory / 'enrolled-compose.json').read_bytes()
        enrolled = decode(enrolled_raw)
        server = enrolled['services']['p2pstream']
        env = server.get('environment', {})
        generation = env.get('SERVER_UPDATE_GENERATION')
        require(isinstance(generation, str) and re.fullmatch(r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}', generation), 'Unknown initial enrollment generation')
        require(isinstance(self.journal.get('version'), str) and VERSION.fullmatch(self.journal['version']) and isinstance(self.journal.get('commit'), str) and re.fullmatch(r'[0-9a-f]{40}', self.journal['commit']), 'Invalid initial enrolled release identity')
        require(isinstance(server.get('image'), str) and re.fullmatch(re.escape('ghcr.io/' + self.config['repository'].lower() + '@sha256:') + r'[0-9a-f]{64}', server['image']), 'Initial enrolled image is not pinned')
        require(enrolled.get('name') == self.config['project'] and server.get('image') == self.journal['server_image'] and env.get('SERVER_UPDATE_INSTANCE_ID') == self.journal['installation'] and env.get('SERVER_UPDATE_RUNTIME_PROTOCOL') == '2' and not env.get('SERVER_UPDATE_TOKEN') and env.get('SERVER_UPDATE_TOKEN_FILE') == '/run/p2pstream-server-update/token' and env.get('SERVER_UPDATE_SOCKET') == '/run/p2pstream-server-update/control.sock' and env.get('SERVER_UPDATE_GATE_FILE') == '/run/p2pstream-server-update/maintenance', 'Initial enrolled runtime binding differs')
        old = decode(docker('image', 'inspect', server['image']).stdout)
        require(len(old) == 1 and old[0]['Id'] == self.journal['before_image_id'], 'Initial enrolled image differs')
        labels = old[0]['Config'].get('Labels', {})
        require((labels.get('org.opencontainers.image.version'), labels.get('org.opencontainers.image.revision'), labels.get('org.opencontainers.image.source', '').lower()) == (self.journal['version'], self.journal['commit'], 'https://github.com/' + self.config['repository'].lower()), 'Initial enrolled image release identity differs')
        expected = {'api': 2, 'allowed': False, 'instance_id': self.journal['installation'], 'generation': generation, 'version': self.journal['version'], 'commit': self.journal['commit']}
        recorded = self.blocked_owner_archive()
        legacy = recorded and recorded['api'] == 1
        provenance = {'installation': self.journal['installation'], 'data_volume': self.config['data_volume'], 'enrolled_sha256': digest(enrolled_raw), 'journal_sha256': digest((self.directory / 'host.json').read_bytes()), 'config_sha256': digest((self.directory / 'config.json').read_bytes()), 'source_model_sha256': digest(source_raw) if legacy else self.cancellation_model_hash(source_raw), 'server_sha256': digest(json.dumps(self.cancellation_server_identity(info, legacy=True) if legacy else self.cancellation_stable_server(info), sort_keys=True).encode()), 'current_version': args.expect_version, 'current_commit': args.expect_commit, 'floor': floor}
        lease = self.cancellation_live_lease(info)
        with self.cancellation_data_directory(info, lease) as directory:
            self.check_local_cancellation_owner(directory, snapshot, lease)
        if recorded:
            # Legacy api1 hashes include volatile runtime state. They cannot be
            # inverted into a stable binding after a restart; retain strict
            # compatibility only while the original proof still matches.
            receipt = cancellation_receipt(self.directory, args.expect_installation, args.repository, args.expect_channel)
            require(recorded['provenance'] == provenance and recorded['lease'] == (lease if legacy else self.cancellation_stable_lease(lease)) and receipt and not receipt['completed'] and receipt['floor'] == floor, 'Blocked initial owner archive or retained floor differs')
            require(snapshot is None or snapshot == recorded['owner'], 'Runtime owner changed after cancellation was recorded')
            archived = recorded['owner']
        else:
            require(not os.path.lexists(self.directory / 'archive.json'), 'Runtime owner appeared during cancellation archival')
            archived = snapshot
        require(isinstance(archived, dict) and set(archived) == {'metadata', 'data_hex', 'sha256'}, 'Invalid blocked initial owner archive')
        raw = bytes.fromhex(archived['data_hex'])
        self.cancellation_inode(archived['metadata'], 4096)
        require(0 < len(raw) <= 4096 and digest(raw) == archived['sha256'], 'Blocked initial owner archive hash differs')
        owner = decode(raw)
        require(isinstance(owner, dict) and owner == expected and type(owner.get('api')) is int and owner.get('allowed') is False, 'Runtime ownership is not the exact blocked initial enrollment')
        record = recorded or {'api': 2, 'owner': archived, 'lease': self.cancellation_stable_lease(lease), 'provenance': provenance, 'release': self._cancellation_release}
        return {'snapshot': snapshot, 'lease': lease, 'record': record}

    def retire_blocked_initial_owner(self):
        proof = self._cancelled_upgrade
        owner = proof['owner']
        if owner is None:
            return
        # Host and executor locks, current release floor and every deployment
        # gate precede this one narrowly scoped admission-metadata operation.
        # The sole published standalone writer never changes its owner marker;
        # trusted host writers are serialized by the two controller locks. No
        # generic detach/offline data operation is being authorized here.
        self.check_cancelled_upgrade(proof['options'])
        for name, snapshot in proof['snapshots'].items():
            require(self.cancellation_source_snapshot(Path(name)) == snapshot, 'Compose source changed during cancellation validation')
        path = self.directory / BLOCKED_INITIAL_OWNER
        if not os.path.lexists(path):
            atomic(path, owner['record'])  # Exact bytes/hash/provenance durable before unlink.
        else:
            require(read_json(path) == owner['record'], 'Blocked initial owner archive changed')
        self.check_cancelled_upgrade(proof['options'])
        snapshot, lease = owner['snapshot'], owner['lease']
        with self.cancellation_data_directory(proof['server'], lease) as directory:
            self.check_cancelled_upgrade(proof['options'])
            # Mutation stays in the Python process holding both controller
            # locks: an orphaned Docker exec could resume after a retry and
            # unlink a newly enrolled owner. Read-only exec probes are harmless
            # if orphaned. A directory FD binds every local lookup and unlink to
            # the exact volume inode independently observed in the container.
            self.clear_blocked_initial_owner(directory, snapshot, lease)
        owner['snapshot'] = None
        self.check_cancelled_upgrade(proof['options'])

    def clear_blocked_initial_owner(self, directory, snapshot, lease):
        self.check_local_cancellation_owner(directory, snapshot, lease)
        if snapshot is not None:
            os.unlink('.p2pstream-runtime-owner.json', dir_fd=directory)
        os.fsync(directory)  # Also make an interrupted unlink durable on retry.

    @staticmethod
    def cancellation_source_snapshot(path):
        if not os.path.lexists(path):
            return None
        info = path.lstat()
        require(stat.S_ISREG(info.st_mode), 'Cancellation source must be a regular file without symlinks')
        return {'data': path.read_bytes(), 'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid}

    def upgraded_cancellation_sources(self):
        snapshots, replacements = {}, {}
        files = self.journal.get('source_bridge', {}).get('files', [])
        def snapshot(name):
            if name not in snapshots:
                snapshots[name] = self.cancellation_source_snapshot(Path(name))
            return snapshots[name]
        for name in self.journal['source_files']:
            require(snapshot(name), 'Original Compose source is missing')
        bridges = {item['path']: json.dumps(item['bridge'], separators=(',', ':'), sort_keys=True).encode() for item in files}
        restoring = any(snapshot(item['path']) and snapshot(item['path'])['data'] == bridges[item['path']] for item in files)
        for item in files:
            name = item['path']
            current = snapshot(name)
            if item['editable'] is not None:
                if restoring:
                    editable = snapshot(item['editable'])
                    require(editable and current and current['data'] in (bridges[name], editable['data']), 'Compose source is not an owned bridge or its exact editable input')
                    if current['data'] == bridges[name]:
                        replacements[name] = dict(current, data=editable['data'])
            else:
                backup = snapshot(item['original_backup']) if item.get('original_backup') else None
                require(not item.get('original_backup') or backup and digest(backup['data']) == item['original_sha256'], 'Default Compose recovery snapshot changed')
                require(current is None and backup is None or current and current['data'] in ([bridges[name]] + ([backup['data']] if backup else [])), 'Default Compose alias is not an owned bridge or its original backup')
                if current and current['data'] == bridges[name]:
                    replacements[name] = dict(current, data=backup['data'], mode=0o600, uid=item['uid'], gid=item['gid']) if backup else None
        return self.input_options() if restoring else self.journal['compose_options'], snapshots, replacements

    @staticmethod
    def cancellation_server_identity(info, legacy=False):
        # Health logs and ExecIDs change during probes; runtime/settings changes
        # must not. Keep the projection independent of probe timestamps.
        value = {key: info.get(key) for key in ('Id', 'Image', 'Config', 'HostConfig', 'Mounts', 'NetworkSettings', 'RestartCount')} | {
            'State': {key: info['State'].get(key) for key in ('Running', 'Paused', 'Restarting', 'StartedAt')}}
        if not legacy:
            # Docker does not promise top-level Mounts ordering. Preserve every
            # field, duplicate and nested array; api1's recorded hash stays raw.
            value['Mounts'] = sorted(info['Mounts'], key=lambda mount: json.dumps(mount, sort_keys=True, separators=(',', ':')))
        return value

    def check_cancelled_upgrade(self, options):
        proof = self._cancelled_upgrade
        source_dir = self.journal['source_directory']
        info = decode(docker('inspect', proof['server']['Id']).stdout)
        require(len(info) == 1 and self.cancellation_server_identity(info[0]) == proof['server'], 'Upgraded deployment changed during cancellation validation')
        require(decode(docker('compose', *options, 'config', '--format', 'json', cwd=source_dir).stdout) == decode(proof['model']) and docker('compose', *options, 'ps', '--all', '--quiet', 'p2pstream', cwd=source_dir).stdout.decode().split() == [proof['server']['Id']], 'Upgraded deployment changed during cancellation validation')
        require(docker('exec', proof['server']['Id'], '/app/p2pstream', 'server-health', check=False).returncode == 0, 'Upgraded server is unhealthy; cancellation is refused')
        require(docker('exec', proof['server']['Id'], '/app/p2pstream', 'server-installation-identity').stdout.decode().strip() == self.config['instance_id'] == self.journal['installation'], 'Original installation identity changed')
        expected = proof['owner']
        require(self.cancellation_owner_snapshot(proof['server']['Id']) == (expected['snapshot'] if expected else None), 'Runtime ownership changed during cancellation validation')
        if expected:
            require(self.cancellation_live_lease(info[0]) == expected['lease'], 'Standalone lifetime lease changed during cancellation validation')
        require_data_writers(self.config['data_volume'], proof['server']['Id'])

    def restore_cancelled_upgrade(self):
        proof = self._cancelled_upgrade
        self.check_cancelled_upgrade(proof['options'])
        # Validate the entire captured set before the first source write. The
        # receipt is already durable; retries recognize exact mixed restored and
        # bridge bytes, and independently prove the same current deployment.
        for name, snapshot in proof['snapshots'].items():
            require(self.cancellation_source_snapshot(Path(name)) == snapshot, 'Compose source changed during cancellation validation')
        for name, replacement in proof['replacements'].items():
            path = Path(name)
            require(self.cancellation_source_snapshot(path) == proof['snapshots'][name], 'Compose source changed during cancellation restoration')
            if replacement is None:
                path.unlink()
                fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
                try:
                    os.fsync(fd)
                finally:
                    os.close(fd)
            else:
                atomic(path, replacement['data'], mode=replacement['mode'], uid=replacement['uid'], gid=replacement['gid'])
        self.check_cancelled_upgrade(self.journal['compose_options'])

    def cancel_enrollment(self, args):
        receipt = cancellation_receipt(self.directory, args.expect_installation, args.repository, args.expect_channel)
        if not self.journal:
            require(receipt, 'No cancelled enrollment exists on this host')
            # lock() finishes a committed archive before reaching this branch.
            receipt['completed'] = True
            atomic(self.directory / CANCELLATION, receipt)
            return
        require((self.journal.get('installation'), self.config and self.config.get('repository'), self.config and self.config.get('channel')) == (args.expect_installation, args.repository, args.expect_channel), 'Saved enrollment differs from the selected installation, publisher or channel')
        if getattr(args, 'upgraded_deployment', False):
            floor = self.validate_cancelled_upgrade(args)
        else:
            self.validate_original_cancellation()
            floor = validate_floor(self.config['bootstrap_floor'])
        if receipt:
            require_floor(floor, receipt['floor'])
        self.archive_cancelled_enrollment(args, floor)

    def validate_original_cancellation(self):
        # Validation can be interrupted before any cancellation receipt exists,
        # or while a completed receipt from an earlier enrollment is retained.
        # Mark its two exact read-only helpers from the first attempt, and settle
        # only those helpers before re-proving restoration on every retry.
        require(self.journal['phase'] == 'rolled_back' or (self.journal['phase'] == 'recovery_required' and self.journal.get('recovery_phase') == 'rolled_back'), 'Cancellation validation recovery belongs to another host operation')
        require(not os.path.lexists(self.directory / 'state.json') and not os.path.lexists(self.directory / 'control' / 'maintenance'), 'Update history or maintenance exists; enrollment cancellation is refused')
        if os.path.lexists(self.directory / 'helper.json'):
            pending = read_json(self.directory / 'helper.json')
            require(pending.get('cancellation_validation') is True and pending.get('readonly') is True and pending.get('name') == self.helper_name() and pending.get('entrypoint') == '/usr/local/bin/docker' and pending.get('phase') == 'rolled_back' and isinstance(pending.get('cancellation_args'), list) and tuple(pending['cancellation_args']) in self.cancellation_helper_args(), 'Pending helper is not a recorded read-only cancellation validation')
            try:
                self.settle_helper()
            except UnsettledHelper as error:
                raise UnsettledHelper('Cannot settle cancellation validation; restore Docker access and retry the verified cancellation command') from error
        if self.journal['phase'] == 'recovery_required':
            self.journal.pop('recovery_phase', None)
            self.phase('rolled_back', error='Original deployment restored; cancellation validation resumed')
        self._cancellation_validation = True
        try:
            self.validate_cancelled_original()
        except UnsettledHelper as error:
            raise UnsettledHelper('Cannot settle cancellation validation; restore Docker access and retry the verified cancellation command') from error

    def archive_cancelled_enrollment(self, args, floor):
        executors = docker('ps', '--all', '--quiet', '--no-trunc', '--filter', 'label=p2pstream.server-update.executor=' + self.journal['installation']).stdout.decode().split()
        require(len(executors) <= 1, 'Multiple saved executors exist; cancellation is refused')
        if executors:
            executor = decode(docker('inspect', executors[0]).stdout)[0]
            labels = executor['Config'].get('Labels', {})
            expected_image = decode(docker('image', 'inspect', self.updater).stdout)[0]['Id']
            require(executor['Id'] == executors[0] and executor['Image'] == expected_image and labels.get('com.docker.compose.project') == self.config['project'] and labels.get('com.docker.compose.service') == 'p2pstream-server-updater' and labels.get('p2pstream.server-update.executor') == self.journal['installation'], 'Saved executor identity differs; cancellation is refused')
            require(not executor['State']['Running'] and not executor['State'].get('Restarting') and not executor['State'].get('Paused'), 'The saved executor is still active; cancellation is refused')
        fd = os.open(self.directory / 'executor.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            try:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise Failure('An executor still owns this deployment; cancellation is refused') from error
            self.handoff_recovery_controller(args.bundle)
            receipt = {'api': 1, 'installation': args.expect_installation, 'repository': args.repository, 'channel': args.expect_channel, 'floor': floor, 'completed': False}
            atomic(self.directory / CANCELLATION, receipt)
            if executors:
                docker('rm', executors[0])  # No force: a concurrently started executor fails closed.
            remaining = docker('ps', '--all', '--quiet', '--no-trunc', '--filter', 'label=p2pstream.server-update.executor=' + self.journal['installation']).stdout.decode().split()
            require(not remaining, 'An executor remains; cancellation archival is refused')
            if getattr(args, 'upgraded_deployment', False):
                self.retire_blocked_initial_owner()
                self.restore_cancelled_upgrade()
            self.archive_state()
            receipt['completed'] = True
            atomic(self.directory / CANCELLATION, receipt)
        finally:
            os.close(fd)

    def archive_state(self):
        marker = self.directory / 'archive.json'
        if marker.exists():
            transaction = read_json(marker)
        else:
            names = [p.name for p in self.directory.iterdir() if p.name not in {'host.lock', 'history', 'detached-compose.json', 'archive.json', 'reenrollment.json', CANCELLATION}]
            transaction = {'id': str(uuid.uuid4()), 'stage': 'copying', 'names': names}
            atomic(marker, transaction)
        archive = self.archive_directory(transaction)
        archive.mkdir(mode=0o700, parents=True, exist_ok=True)
        # Keep every recovery input/tool in place until the journal commit. Large
        # completed data snapshots can move without duplicating their disk usage.
        for name in transaction['names']:
            source, target = self.directory / name, archive / name
            if not os.path.lexists(source):
                require(target.exists(), 'Archive input disappeared; inspect the private history')
                continue
            if name.startswith('backup-') and source.is_file():
                source.rename(target)
            elif source.is_dir() and not source.is_symlink():
                shutil.copytree(source, target, dirs_exist_ok=True, symlinks=True, ignore=lambda directory, names: [n for n in names if stat.S_ISSOCK((Path(directory) / n).lstat().st_mode)])
            else:
                shutil.copy2(source, target, follow_symlinks=False)
        for path in [*archive.rglob('*'), archive, archive.parent]:
            if not path.is_symlink() and (path.is_file() or path.is_dir()):
                fd = os.open(path, os.O_RDONLY)
                try:
                    os.fsync(fd)
                finally:
                    os.close(fd)
        transaction['stage'] = 'copied'
        atomic(marker, transaction)
        (self.directory / 'host.json').unlink()
        self.sync_directory()
        self.finish_archive()

    def archive_directory(self, transaction):
        require(str(uuid.UUID(transaction['id'])) == transaction['id'], 'Invalid archive identity')
        require(all(isinstance(n, str) and Path(n).name == n and n not in {'', '.', '..', 'host.lock', 'history', 'detached-compose.json', 'archive.json', 'reenrollment.json', CANCELLATION} for n in transaction['names']), 'Unsafe archive inventory')
        return self.directory / 'history' / transaction['id']

    def sync_directory(self):
        fd = os.open(self.directory, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)

    def finish_archive(self):
        marker = self.directory / 'archive.json'
        transaction = read_json(marker)
        archive = self.archive_directory(transaction)
        require(transaction['stage'] == 'copied' and archive.is_dir(), 'Incomplete archive; inspect the private history before proceeding')
        for name in transaction['names']:
            require((archive / name).exists() or (archive / name).is_symlink(), 'Missing archived input; refusing cleanup')
            source = self.directory / name
            if source.is_dir() and not source.is_symlink():
                shutil.rmtree(source)
            elif os.path.lexists(source):
                source.unlink()
        self.sync_directory()
        marker.unlink()
        self.sync_directory()

    def stop_executor(self):
        self.idle()  # Lock excludes new update acceptance. Never interrupt recovery.
        self.compose('stop', 'p2pstream-server-updater')
        self.idle()
        fd = os.open(self.directory / 'executor.lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            os.close(fd)
            raise Failure('A second executor is still running; no configuration was changed') from error
        return fd

    def start_executor(self):
        self.compose('up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'p2pstream-server-updater')
        self.wait_socket()

    def server_container(self, allow_missing=False):
        ids = self.compose('ps', '--all', '--quiet', 'p2pstream').stdout.decode().split()
        if not ids and allow_missing:
            return None  # A successful daemon inventory, never a failed inspect.
        require(len(ids) == 1, 'Expected exactly one existing server; inspect the deployment before recreation')
        info = decode(docker('inspect', ids[0]).stdout)
        require(len(info) == 1 and info[0]['Id'] == ids[0], 'Server inspection differs from Compose inventory')
        return info[0]

    def canonical_hash(self):
        fields = self.compose('config', '--hash', 'p2pstream').stdout.decode().split()
        require(len(fields) == 2 and fields[0] == 'p2pstream' and HEX.fullmatch(fields[1]), 'Cannot identify the saved server configuration')
        return fields[1]

    def data_identity(self):
        return self.run_helper('/app/p2pstream', ('server-installation-identity',), readonly=True, image=read_json(self.current_model_path())['services']['p2pstream']['image'], mounts=(
            '--env', 'CONFIG_DIR=/server-data', '--mount', 'type=volume,src=' + self.config['data_volume'] + ',dst=/server-data,readonly')).stdout.decode().strip()

    def validate_recreation_server(self, info, image_id, hashes, *, stopped=False):
        labels = info['Config']['Labels']
        require(labels.get('com.docker.compose.project') == self.config['project'] and labels.get('com.docker.compose.service') == 'p2pstream', 'Server belongs to another Compose deployment')
        require(info['Image'] == image_id, 'Running image changed; configuration-only recreation cannot change or downgrade software')
        require(labels.get('com.docker.compose.config-hash') in hashes, 'Running settings differ from the accepted deployment; restore the recorded Compose inputs before repair')
        mounts = [v for v in info['Mounts'] if v['Destination'] == '/data']
        require(len(mounts) == 1 and mounts[0].get('Name') == self.config['data_volume'] and mounts[0]['RW'], 'Server data volume changed; refusing recreation')
        require(not any(v['Destination'].startswith('/data/') for v in info['Mounts']), 'Nested server data mounts cannot be recreated safely')
        require_data_writers(self.config['data_volume'], info['Id'], self.config['instance_id'])
        if info['State']['Running']:
            identity = docker('exec', info['Id'], '/app/p2pstream', 'server-installation-identity').stdout.decode().strip()
            require(docker('exec', info['Id'], '/app/p2pstream', 'server-health', check=False).returncode == 0, 'Server health failed; diagnose the server before configuration-only recreation')
        else:
            require(stopped, 'Server is not running; no recreation identity has been recorded')
            # A stopped replacement cannot answer exec. Read only the identity
            # from the exact volume proven before this transaction; do not open
            # the database or infer identity from a Docker label.
            identity = self.data_identity()
        require(identity == self.journal['installation'] == self.config['instance_id'], 'Installation identity changed; refusing recreation')

    def prepare_recreation(self, detached=False, before_persist=None):
        current = self.current_model_path().read_bytes()
        model = decode(current)
        image_id = decode(docker('image', 'inspect', model['services']['p2pstream']['image']).stdout)[0]['Id']
        canonical = self.canonical_hash()
        accepted = canonical
        info = self.server_container()
        if not detached:
            # The driver may have authenticated a different native Compose
            # version through its trusted runtime receipt. Bind that verified
            # live hash to the same immutable container before persisting it.
            self.healthy()
            verified = self.server_container()
            require(verified['Id'] == info['Id'] and verified['Image'] == info['Image'] and verified['Config']['Labels'].get('com.docker.compose.config-hash') == info['Config']['Labels'].get('com.docker.compose.config-hash'), 'Deployment changed during recreation verification; retry after it settles')
            accepted = info['Config']['Labels'].get('com.docker.compose.config-hash')
            require(isinstance(accepted, str) and HEX.fullmatch(accepted), 'Verified native Compose configuration hash is invalid')
        if detached:
            raw = docker('compose', *self.input_options(), 'config', '--format', 'json', cwd=self.journal['source_directory']).stdout
            atomic(self.directory / 'repair-input.json', raw)
            self.command('configure', '--compose', str(self.directory / 'repair-input.json'), '--output', str(self.directory / 'repair-compose.json'))
            candidate = decode(self.compose('config', '--format', 'json', model='repair-compose.json').stdout)
            saved = decode(self.compose('config', '--format', 'json').stdout)
            require(candidate == saved, 'Edited Compose settings differ from the last managed configuration. Restore the last applied inputs, run repair, then edit and run manage apply.')
            # Match the native Compose version which performed the ordinary
            # recreation. Its hash need not equal the executor's pinned version.
            fields = docker('compose', '--project-directory', self.journal['source_directory'], '-f', str(self.directory / 'repair-input.json'), 'config', '--hash', 'p2pstream').stdout.decode().split()
            require(len(fields) == 2 and fields[0] == 'p2pstream' and HEX.fullmatch(fields[1]), 'Cannot identify the original Compose deployment')
            accepted = fields[1]
        self.validate_recreation_server(info, image_id, {accepted})
        receipt = {'model_sha256': digest(current), 'config_sha256': digest((self.directory / 'config.json').read_bytes()),
                   'image_id': image_id, 'container_id': info['Id'], 'accepted_hash': accepted, 'canonical_hash': canonical,
                   'installation': self.config['instance_id'], 'data_volume': self.config['data_volume'], 'detached': detached}
        if before_persist:
            before_persist()
        self.phase('recreating', recreation=receipt, error='')

    def recreate_server(self):
        receipt = self.journal.get('recreation')
        require(receipt and receipt['model_sha256'] == digest(self.current_model_path().read_bytes()) and receipt['config_sha256'] == digest((self.directory / 'config.json').read_bytes()), 'Saved recreation model or credentials changed; inspect the private recovery journal')
        require(receipt['installation'] == self.config['instance_id'] == self.journal['installation'] and receipt['data_volume'] == self.config['data_volume'], 'Saved recreation identity changed')
        model = read_json(self.current_model_path())
        require(decode(docker('image', 'inspect', model['services']['p2pstream']['image']).stdout)[0]['Id'] == receipt['image_id'], 'Saved recreation image changed')
        require(self.canonical_hash() == receipt['canonical_hash'], 'Saved recreation configuration changed')
        def inspected():
            info = self.server_container(allow_missing=True)
            if info is None:
                # Compose may have removed the old container before the helper
                # was interrupted. Only this durable transaction may recover
                # absence, after independently proving the original data ID.
                require_data_writers(self.config['data_volume'], '', self.config['instance_id'])
                require(self.data_identity() == receipt['installation'], 'Installation identity changed; refusing recreation')
                return None
            original = info['Id'] == receipt['container_id']
            accepted = {receipt['accepted_hash'], receipt['canonical_hash']} if original else {receipt['canonical_hash']}
            if not original or info['Config']['Labels'].get('com.docker.compose.config-hash') == receipt['canonical_hash']:
                require(info['Config']['Labels'].get('p2pstream.server-update.instance') == self.config['instance_id'], 'Replacement is outside the saved enrolled deployment')
            self.validate_recreation_server(info, receipt['image_id'], accepted, stopped=True)
            return info
        inspected()  # Refuse drift before stopping even the idle executor.
        executor_lock = self.stop_executor()
        try:
            info = inspected()
            # An interrupted up can have completed replacement. The exact
            # canonical replacement is resumed without a second recreation.
            self.runtime('block')
            self.deploy_server(force=info is not None and info['Id'] == receipt['container_id'])
        finally:
            os.close(executor_lock)
        self.start_executor()
        status = self.wait_healthy()
        self.record_runtime()
        self.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
        print('Last managed server configuration restored; current image, data and credentials preserved.')

    def replacement_data(self):
        # This helper only reads the named volume. Its journal is settled before
        # any recovery decision; command failure is never interpreted as absence.
        probe = "stat -c '%d:%i' -- /server-data\nif " + OWNER_ABSENT.replace('/data', '/server-data') + "; then printf 'absent\\n'; else\n" + OWNER_PROBE.replace('/data', '/server-data') + "\nfi"
        result = self.run_helper('/bin/sh', ('-c', probe), readonly=True,
                                 mounts=('--mount', 'type=volume,src=' + self.config['data_volume'] + ',dst=/server-data,readonly'))
        directory, separator, body = result.stdout.partition(b'\n')
        require(separator and re.fullmatch(rb'[0-9]+:[0-9]+', directory), 'Cannot prove replacement data directory')
        if body == b'absent\n':
            owner = None
        else:
            metadata, separator, raw = body.partition(b'\n')
            require(separator and 0 < len(raw) <= 4096, 'Cannot prove replacement runtime owner')
            self.cancellation_inode(metadata.decode('ascii'), 4096)
            owner = decode(raw)
            require(isinstance(owner, dict) and set(owner) == {'api', 'allowed', 'instance_id', 'generation', 'version', 'commit'} and type(owner['api']) is int and owner['api'] == 2 and type(owner['allowed']) is bool, 'Invalid replacement runtime owner')
            require(isinstance(owner['generation'], str) and re.fullmatch(r'[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}', owner['generation']), 'Invalid replacement runtime generation')
        return {'directory': directory.decode('ascii'), 'owner': owner}

    def replacement_binding(self):
        raw = (self.directory / 'before-compose.json').read_bytes()
        require(digest(raw) == self.journal.get('before_sha256'), 'Recovery snapshot differs from the recorded transaction')
        model = decode(raw)
        image = model['services']['p2pstream']['image']
        require(read_json(self.current_model_path())['services']['p2pstream']['image'] == image, 'Replacement active image differs from the recorded transaction')
        require(re.fullmatch(re.escape('ghcr.io/' + self.config['repository'].lower() + '@sha256:') + '[0-9a-f]{64}', image), 'Recovery image is not pinned')
        published = decode(docker('image', 'inspect', image).stdout)
        require(len(published) == 1 and published[0]['Id'] == self.journal['before_image_id'], 'Recovery image differs from the recorded transaction')
        labels = published[0]['Config'].get('Labels', {})
        version, commit = labels.get('org.opencontainers.image.version'), labels.get('org.opencontainers.image.revision')
        require(isinstance(version, str) and VERSION.fullmatch(version) and isinstance(commit, str) and re.fullmatch('[0-9a-f]{40}', commit) and labels.get('org.opencontainers.image.source', '').lower() == 'https://github.com/' + self.config['repository'].lower(), 'Recovery image release identity differs')
        require(self.journal['installation'] == self.config['instance_id'] and model['name'] == self.config['project'], 'Recovery installation or project differs')
        return {'before_sha256': digest(raw), 'image': image, 'image_id': published[0]['Id'], 'version': version, 'commit': commit,
                'config_sha256': digest((self.directory / 'config.json').read_bytes()), 'installation': self.journal['installation'],
                'project': self.config['project'], 'data_volume': self.config['data_volume'],
                'source_sha256': self.cancellation_model_hash(docker('compose', *self.input_options(), 'config', '--format', 'json', cwd=self.journal['source_directory']).stdout)}

    def recorded_rollback_transition(self, intent, binding):
        transition = self.journal.get('rollback_transition')
        if not transition:
            return None
        require(set(transition) == {'api', 'intent_sha256', 'binding', 'previous_model_sha256', 'target_sha256', 'server'} and transition['api'] == 1 and transition['intent_sha256'] == digest(json.dumps(intent, sort_keys=True).encode()) and transition['binding'] == binding == intent['binding'], 'Rollback publication transition binding changed')
        require(transition['previous_model_sha256'] in {intent['previous_model_sha256'], intent['target_sha256']}, 'Rollback publication prior model differs')
        model = read_json(self.directory / 'before-compose.json')
        if 'p2pstream-server-updater' in model['services']:
            target = self.journal.get('rollback_target')
            require(target and target['binding'] == binding and isinstance(target['generation'], str) and str(uuid.UUID(target['generation'])) == target['generation'], 'Rollback publication generation differs')
            model['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION'] = target['generation']
        require(self.cancellation_model_hash(json.dumps(model).encode()) == transition['target_sha256'], 'Rollback publication target differs from the recorded snapshot')
        return transition

    def replacement_model_hashes(self, intent, binding):
        hashes = {intent['target_sha256'], intent['previous_model_sha256']}
        transition = self.recorded_rollback_transition(intent, binding)
        if transition:
            hashes.add(transition['target_sha256'])
        return hashes

    def replacement_server(self, info, binding):
        labels = info['Config'].get('Labels', {})
        require(info['Image'] == binding['image_id'], 'Running image changed outside this host transaction; refusing configuration-only rollback')
        require(labels.get('com.docker.compose.project') == binding['project'] and labels.get('com.docker.compose.service') == 'p2pstream', 'Replacement server belongs to another deployment')
        hashes = set()
        for name in ('before-compose.json', 'compose.json'):
            fields = self.compose('config', '--hash', 'p2pstream', model=name).stdout.decode().split()
            require(len(fields) == 2 and fields[0] == 'p2pstream' and HEX.fullmatch(fields[1]), 'Cannot verify replacement settings')
            hashes.add(fields[1])
        if not self.journal.get('verified_version') and not self.journal.get('verified_commit'):
            fields = docker('compose', '--project-directory', self.journal['source_directory'], '-f', str(self.directory / 'input.json'), 'config', '--hash', 'p2pstream').stdout.decode().split()
            require(len(fields) == 2 and fields[0] == 'p2pstream' and HEX.fullmatch(fields[1]), 'Cannot verify original replacement settings')
            hashes.add(fields[1])
        intent = self.journal.get('replacement_intent')
        recorded_server = False
        if intent and intent['binding'] == binding:
            transition = self.recorded_rollback_transition(intent, binding)
            projection = self.cancellation_stable_server(info)
            if transition:
                require(projection == transition['server'], 'Server differs from the declared rollback publication transition')
            recorded_server = projection == intent.get('previous_server') or transition is not None and projection == transition['server']
        require(recorded_server or labels.get('com.docker.compose.config-hash') in hashes, 'Running settings differ from the recorded replacement transaction')
        mounts = [m for m in info['Mounts'] if m['Destination'] == '/data']
        require(len(mounts) == 1 and mounts[0].get('Name') == binding['data_volume'] and mounts[0].get('RW') and not any(m['Destination'].startswith('/data/') for m in info['Mounts']), 'Replacement data volume differs')
        identity = docker('exec', info['Id'], '/app/p2pstream', 'server-installation-identity').stdout.decode().strip() if info['State']['Running'] else self.data_identity()
        require(identity == binding['installation'], 'Installation identity changed during replacement')
        require_data_writers(binding['data_volume'], info['Id'], binding['installation'])

    @staticmethod
    def replacement_owned(owner, intent):
        permitted = intent.get('permitted_owner')
        return owner is not None and (owner == intent['owner'] or permitted is not None and owner in (permitted, dict(permitted, allowed=False)))

    def verify_replacement_intent(self, binding=None, previous_model=None):
        intent = self.journal.get('replacement_intent')
        require(self.config.get('deployment_protocol') == 2 and intent and intent.get('api') == 1, 'Missing deployment has no durable blocked replacement proof; refusing to recreate a missing deployment')
        binding = binding or self.replacement_binding()
        require(intent['binding'] == binding, 'Replacement snapshot, configuration or image binding changed')
        require(self.cancellation_model_hash(previous_model if previous_model is not None else self.current_model_path().read_bytes()) in self.replacement_model_hashes(intent, binding), 'Replacement active model changed')
        data = self.replacement_data()
        require(data['directory'] == intent['data_directory'] and self.replacement_owned(data['owner'], intent), 'Replacement runtime owner or data directory changed')
        require(self.data_identity() == binding['installation'], 'Installation identity changed during replacement')
        require_data_writers(binding['data_volume'], '', binding['installation'])
        return intent

    def prepare_replacement(self, info, operation, target, *, stopped_from=None, previous_model=None):
        if self.config.get('deployment_protocol') != 2:
            return
        self.idle()
        binding = self.replacement_binding()
        existing = self.journal.get('replacement_intent')
        if existing:
            require(existing.get('api') == 1 and existing['binding'] == binding, 'Replacement snapshot, configuration or image binding changed')
            require(self.cancellation_model_hash(previous_model if previous_model is not None else self.current_model_path().read_bytes()) in self.replacement_model_hashes(existing, binding), 'Replacement active model changed')
        if info is None:
            self.verify_replacement_intent(binding, previous_model)
        elif stopped_from is not None:
            require(self.cancellation_stable_server(info) == self.cancellation_stable_server(stopped_from) and not info['State']['Running'] and info['State'].get('StartedAt') == stopped_from['State'].get('StartedAt') and info.get('RestartCount') == stopped_from.get('RestartCount'), 'Deployment changed while stopping for rollback')
            require(self.data_identity() == binding['installation'], 'Installation identity changed during replacement')
            require_data_writers(binding['data_volume'], info['Id'], binding['installation'])
        else:
            self.replacement_server(info, binding)
        data = self.replacement_data()
        if existing:
            require(data['directory'] == existing['data_directory'] and self.replacement_owned(data['owner'], existing), 'Replacement runtime owner or data directory changed')
        elif data['owner'] is not None:
            generations = set()
            for path in (self.current_model_path(), self.directory / 'before-compose.json', self.directory / 'enrolled-compose.json'):
                if path.exists():
                    generations.add(read_json(path)['services']['p2pstream'].get('environment', {}).get('SERVER_UPDATE_GENERATION'))
            owner = data['owner']
            require(owner['instance_id'] == binding['installation'] and owner['version'] == binding['version'] and owner['commit'] == binding['commit'] and owner['generation'] in generations, 'Runtime owner does not belong to this replacement transaction')
        # Block first, then recheck the same live deployment before recording
        # authority. A prepared phase or installation UUID alone proves nothing.
        require(data['owner'] is not None or read_json(self.current_model_path())['services']['p2pstream'].get('environment', {}).get('SERVER_UPDATE_RUNTIME_PROTOCOL') == '2', 'Detached rollback has no blocked runtime authority; restore the verified managed deployment before retrying')
        self.runtime('block')
        blocked = self.replacement_data()
        require(blocked['directory'] == data['directory'] and blocked['owner'] is not None and blocked['owner']['allowed'] is False, 'Replacement runtime admission was not blocked')
        owner = blocked['owner']
        require(owner['instance_id'] == binding['installation'] and (owner['version'], owner['commit']) == (binding['version'], binding['commit']), 'Blocked replacement owner release differs')
        if data['owner'] is not None:
            require(owner == dict(data['owner'], allowed=False), 'Runtime owner changed while blocking replacement')
        else:
            require(owner['generation'] == read_json(self.current_model_path())['services']['p2pstream']['environment'].get('SERVER_UPDATE_GENERATION'), 'Blocked replacement generation differs from the verified model')
        if info is not None:
            current = self.server_container()
            require(self.cancellation_server_identity(current) == self.cancellation_server_identity(info), 'Deployment changed while blocking replacement')
            if stopped_from is None:
                self.replacement_server(current, binding)
            else:
                require_data_writers(binding['data_volume'], current['Id'], binding['installation'])
        else:
            require(self.server_container(allow_missing=True) is None, 'A server appeared during replacement recovery')
            require_data_writers(binding['data_volume'], '', binding['installation'])
        require(self.replacement_binding() == binding, 'Replacement snapshots changed while blocking')
        target_raw = json.dumps(target, separators=(',', ':'), sort_keys=True).encode()
        require(target['services']['p2pstream']['image'] == binding['image'], 'Replacement target image differs')
        env = target['services']['p2pstream'].get('environment', {})
        permitted = None
        if 'p2pstream-server-updater' in target['services']:
            generation = env.get('SERVER_UPDATE_GENERATION')
            require(isinstance(generation, str) and str(uuid.UUID(generation)) == generation, 'Invalid replacement target generation')
            permitted = dict(owner, generation=generation, allowed=True)
        self._replacement_server = self.cancellation_stable_server(info) if info else None
        if existing and existing['operation'] == operation and existing['target_sha256'] == digest(target_raw):
            return  # Retain exact original authority across retries.
        intent = {'api': 1, 'operation': operation, 'binding': binding, 'container_id': info['Id'] if info else existing['container_id'],
                  'data_directory': blocked['directory'], 'owner': owner, 'permitted_owner': permitted, 'previous_server': self.cancellation_stable_server(info) if info else None, 'target_sha256': digest(target_raw), 'previous_model_sha256': self.cancellation_model_hash(previous_model if previous_model is not None else self.current_model_path().read_bytes())}
        # Commit new authority and consume its publication bridge in one write.
        self.journal.pop('rollback_transition', None)
        self.phase(self.journal['phase'], replacement_intent=intent)

    def begin_detached_continuation(self):
        intent = self.verify_replacement_intent()
        require(intent['operation'] in {'rollback', 'remove'} and intent['permitted_owner'] is None, 'No detached replacement transaction exists')
        info = self.server_container()
        self.replacement_server(info, intent['binding'])
        require(not info['State']['Running'] and not info['State'].get('Paused') and not info['State'].get('Restarting'), 'Detached target is not stopped')
        stopped = getattr(self, '_replacement_stopped_target', None)
        reused = stopped is not None and self.cancellation_stable_server(info) == stopped['server'] and info['State'].get('StartedAt') == stopped['started_at'] and info.get('RestartCount') == stopped['restart_count']
        require(reused or (stopped is None or info['Id'] != stopped['server']['Id']) and info['State'].get('StartedAt') in (None, '', '0001-01-01T00:00:00Z') and info.get('RestartCount') in (None, 0), 'Detached target has already been started outside its continuation')
        data = self.replacement_data()
        require(data['owner'] is not None and data['owner']['allowed'] is False and self.replacement_owned(data['owner'], intent), 'Detached target lacks its blocked owner')
        continuation = {'api': 1, 'stage': 'detaching', 'operation': intent['operation'], 'reused_stopped_target': reused,
                        'intent_sha256': digest(json.dumps(intent, sort_keys=True).encode()),
                        'server': self.cancellation_stable_server(info), 'started_at': info['State'].get('StartedAt'),
                        'restart_count': info.get('RestartCount'), 'target_sha256': intent['target_sha256'],
                        'data_directory': data['directory'], 'owner': data['owner']}
        self.phase(self.journal['phase'], detached_continuation=continuation)

    def detached_server_matches(self, info, continuation):
        observed = self.cancellation_stable_server(info)
        recorded = continuation['server']
        actual, saved = observed.get('HostConfig'), recorded.get('HostConfig')
        key = 'OomKillDisable'
        if isinstance(actual, dict) and isinstance(saved, dict) and (key in actual or key in saved):
            if key not in actual or key not in saved:
                return False
            if any(value is not None and type(value) is not bool for value in (actual[key], saved[key])):
                return False
            # Docker start revalidates resources and can clear default false to
            # null when OOM-disable is unsupported. Only the declared start may
            # do this; keep the saved projection and cancellation hashes exact.
            if continuation['stage'] == 'starting' and saved[key] is False and actual[key] is None:
                observed = dict(observed, HostConfig=dict(actual, OomKillDisable=False))
        return observed == recorded

    def detached_target(self):
        continuation = self.journal.get('detached_continuation')
        intent = self.journal.get('replacement_intent')
        require(self.config.get('deployment_protocol') == 2 and continuation and continuation.get('api') == 1 and continuation['stage'] in {'detaching', 'starting'} and intent and continuation['operation'] == intent['operation'] in {'rollback', 'remove'}, 'Invalid detached continuation')
        require(continuation['intent_sha256'] == digest(json.dumps(intent, sort_keys=True).encode()) and intent['binding'] == self.replacement_binding(), 'Detached continuation bindings changed')
        require(self.cancellation_model_hash(self.current_model_path().read_bytes()) == continuation['target_sha256'] == intent['target_sha256'], 'Detached target model changed')
        require('p2pstream-server-updater' not in read_json(self.current_model_path())['services'], 'Detached target still has an updater')
        # This is completion of one already-created target, never authority to
        # recreate an absent target or return to a historical image.
        info = self.server_container(allow_missing=True)
        require(info is not None and self.detached_server_matches(info, continuation), 'Declared detached target is missing or changed; no recreation is authorized')
        require(not info['State'].get('Paused') and not info['State'].get('Restarting') and info.get('RestartCount') == continuation['restart_count'], 'Detached target runtime changed')
        if info['State']['Running']:
            require(continuation['stage'] == 'starting', 'Detached target started before its declared start transition')
        else:
            require(info['State'].get('StartedAt') == continuation['started_at'], 'Stopped detached target was started outside this continuation')
        data = self.replacement_data()
        require(data['directory'] == continuation['data_directory'] == intent['data_directory'], 'Detached data directory changed')
        require(data['owner'] is None or continuation['stage'] == 'detaching' and data['owner'] == continuation['owner'], 'Detached runtime owner changed')
        require(not info['State']['Running'] or data['owner'] is None, 'Running detached target retains runtime ownership')
        require(self.data_identity() == intent['binding']['installation'], 'Detached installation identity changed')
        require_data_writers(intent['binding']['data_volume'], info['Id'], intent['binding']['installation'])
        return info, data

    def continue_detached_start(self):
        self.settle_helper()
        self.idle()
        info, data = self.detached_target()
        continuation = self.journal['detached_continuation']
        if data['owner'] is not None:
            self.runtime('detach')  # Existing runtime command takes the stable lease.
            info, data = self.detached_target()
            require(data['owner'] is None, 'Runtime detach did not remove its declared owner')
        if continuation['stage'] != 'starting':
            # Durable intent precedes start, including a lost helper response.
            continuation = dict(continuation, stage='starting')
            self.phase(self.journal['phase'], detached_continuation=continuation)
        info, _ = self.detached_target()
        if not info['State']['Running']:
            self.compose('start', 'p2pstream')
        self.detached_target()
        self.wait_original()
        self.detached_target()

    def finish_removal(self):
        self.wait_original()
        candidate = self.current_model_path().read_bytes()
        self.compose('rm', '--force', 'p2pstream-server-updater', model='before-compose.json')
        atomic(self.directory / 'detached-compose.json', candidate)
        if self.config.get('deployment_protocol') == 2:
            self.export_sources(candidate)
            self.phase('removed', error='', editable_compose_options=self.journal['compose_options'])
        else:
            self.phase('removed', error='', source_directory=str(self.directory), compose_options=['-p', self.config['project'], '-f', str(self.directory / 'detached-compose.json')])
        print('Updater removed. Current pinned image, settings, ports and data retained.')
        if self.config.get('deployment_protocol') == 2:
            print('Editable current deployment: ' + self.journal['source_layout']['primary'] + '; the original and default Compose commands retain the current image and settings.')
        else:
            print('Editable deployment: ' + str(self.directory / 'detached-compose.json') + '; apply with sudo docker compose -p ' + self.config['project'] + ' -f ' + str(self.directory / 'detached-compose.json') + ' up -d --no-deps --no-build p2pstream')

    def resume_detached_continuation(self):
        self.continue_detached_start()
        if self.journal['detached_continuation']['operation'] == 'remove':
            self.finish_removal()
        else:
            self.phase('rolled_back', error='Original deployment restored; enrollment is not enabled')
            print(self.journal['error'])

    def interrupted_initial_activation(self):
        return (self.journal['phase'] == 'activating' and self.config.get('deployment_protocol') == 2
                and not any(self.journal.get(key) for key in ('verified_version', 'verified_commit', 'deployment_migration', 'recovery_phase', 'recovery_operation'))
                and not os.path.lexists(self.directory / 'state.json'))

    def activate(self):
        self.idle()
        raw = docker('compose', *self.input_options(), 'config', '--format', 'json', cwd=self.journal['source_directory']).stdout
        require(raw == (self.directory / 'input.json').read_bytes(), 'Compose inputs changed after setup was staged; restore the recorded inputs before repair')
        expected = decode(docker('image', 'inspect', self.journal['server_image']).stdout)[0]['Id']
        ids = self.compose('ps', '--all', '--quiet', 'p2pstream').stdout.decode().split()
        require(len(ids) == 1 or not ids and self.interrupted_initial_activation() and self.journal.get('replacement_intent'), 'Server disappeared after setup was staged; inspect the host before repair')
        if ids:
            actual = decode(docker('inspect', ids[0]).stdout)[0]
            require(actual['Image'] == expected, 'Running image changed after setup was staged; refusing a configuration-only replacement')
        else:
            require(self.data_identity() == self.journal['installation'], 'Installation identity changed during activation')
        require_data_writers(self.config['data_volume'], ids[0] if ids else '', self.config['instance_id'])
        self.phase('activating')
        self.prepare_replacement(actual if ids else None, 'activation', read_json(self.current_model_path()))
        if self.config.get('deployment_protocol') != 2:
            self.runtime('block')
        if self.config.get('deployment_protocol') == 2:
            self.source_bridges()
            self.deploy_server()
            self.start_executor()
        else:
            self.start_executor()
            self.deploy_server()
        status = self.wait_healthy()
        self.record_runtime()
        self.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
        print('Server updates enabled: server health and private updater verified. Later updates require an operator action.')

    def rollback(self):
        require(self.journal['phase'] in {'preparing', 'prepared', 'activating', 'applying', 'removing', 'rolling_back', 'recovery_required', 'rolled_back'}, 'No unfinished host transaction exists; rollback cannot downgrade a healthy updated deployment')
        self.idle()
        # Stop before changing models; a successful same-image configuration
        # rollback does not claim to restore application data.
        previous = self.directory / 'before-compose.json'
        require(previous.exists(), 'No saved deployment available for configuration rollback')
        require(digest(previous.read_bytes()) == self.journal.get('before_sha256'), 'Recovery snapshot differs from the recorded transaction')
        before = read_json(previous)
        ids = self.compose('ps', '--all', '--quiet', 'p2pstream', model='before-compose.json').stdout.decode().split()
        if not ids:
            if not self.journal.get('replacement_intent'):
                raise MissingRollbackServer('Expected exactly one existing server for configuration rollback; no durable blocked replacement proof, refusing to recreate a missing deployment')
            self.verify_replacement_intent()
            info = None
        else:
            require(len(ids) == 1, 'Expected exactly one existing server for configuration rollback')
            inspection = decode(docker('inspect', ids[0]).stdout)
            require(len(inspection) == 1 and inspection[0]['Id'] == ids[0], 'Server inspection differs from Compose inventory')
            info = inspection[0]
            require(info['Image'] == self.journal['before_image_id'], 'Running image changed outside this host transaction; refusing configuration-only rollback')
        model = decode(json.dumps(before))
        enrolled = 'p2pstream-server-updater' in model['services']
        intent = self.journal.get('replacement_intent')
        rollback_target = None
        if enrolled and self.config.get('deployment_protocol') == 2:
            binding = self.replacement_binding()
            rollback_target = self.journal.get('rollback_target')
            if rollback_target:
                require(rollback_target['binding'] == binding, 'Rollback target binding changed')
            else:
                rollback_target = {'binding': binding, 'generation': str(uuid.uuid4())}
            model['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION'] = rollback_target['generation']
        previous_model = self.current_model_path().read_bytes()
        if self.config.get('deployment_protocol') == 2 and info is not None:
            binding = self.replacement_binding()
            self.replacement_server(info, binding)
            data = self.replacement_data()
            if intent:
                require(self.cancellation_model_hash(previous_model) in self.replacement_model_hashes(intent, binding), 'Replacement active model changed')
                require(intent['binding'] == binding and data['directory'] == intent['data_directory'] and self.replacement_owned(data['owner'], intent), 'Replacement runtime owner or data directory changed')
            require(data['owner'] is not None or enrolled or read_json(self.current_model_path())['services']['p2pstream'].get('environment', {}).get('SERVER_UPDATE_RUNTIME_PROTOCOL') == '2', 'Detached rollback has no blocked runtime authority; restore the verified managed deployment before retrying')
        declaration = {'rollback_target': rollback_target} if rollback_target else {}
        if intent and intent['operation'] != 'rollback' and not self.journal.get('rollback_transition'):
            # Preserve the validated old authority while publishing the exact
            # next model. Neither phase nor a model hash alone grants ownership.
            declaration['rollback_transition'] = {'api': 1, 'intent_sha256': digest(json.dumps(intent, sort_keys=True).encode()),
                'binding': intent['binding'], 'previous_model_sha256': self.cancellation_model_hash(previous_model),
                'target_sha256': self.cancellation_model_hash(json.dumps(model).encode()),
                'server': self.cancellation_stable_server(info) if info else None}
        # No Compose create/up may run while rollback_transition is durable.
        # prepare_replacement below must atomically advance replacement_intent
        # and consume the transition before deploy_server may create a target.
        self.phase('rolling_back', **declaration)
        helper_model = 'before-compose.json' if 'p2pstream-server-updater' in before['services'] else 'enrolled-compose.json'
        if (self.directory / helper_model).exists():
            self.compose('stop', 'p2pstream-server-updater', model=helper_model)
        current_env = read_json(self.current_model_path())['services']['p2pstream'].get('environment', {})
        if self.config.get('deployment_protocol') != 2 or current_env.get('SERVER_UPDATE_RUNTIME_PROTOCOL') == '2':
            self.runtime('block')
        self.compose('stop', 'p2pstream', model='before-compose.json')
        self.write_model(json.dumps(model, separators=(',', ':')).encode())
        if self.config.get('deployment_protocol') == 2:
            if enrolled:
                self.runtime('block')
                self.source_bridges()
            else:
                self.restore_sources()
        if self.config.get('deployment_protocol') == 2:
            stopped = self.server_container(allow_missing=True)
            require((stopped is None) == (info is None), 'Server inventory changed before rollback replacement')
            self.prepare_replacement(stopped, 'rollback', model, stopped_from=info, previous_model=previous_model)
        self.deploy_server(detached=not enrolled)
        if enrolled:
            self.start_executor()
            self.wait_healthy()
            self.record_runtime()
            self.phase('healthy', error='Previous deployment configuration restored; data was not rolled back')
        else:
            self.wait_original()
            # Keep staged controls/models and credentials for a repair; do not
            # generate a fresh token or delete diagnostic/recovery information.
            self.phase('rolled_back', error='Original deployment restored; enrollment is not enabled')
        print(self.journal['error'])

    def wait_original(self):
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            ids = self.compose('ps', '--quiet', 'p2pstream').stdout.decode().split()
            if len(ids) == 1:
                inspection = decode(docker('inspect', ids[0]).stdout)[0]
                identity = docker('exec', ids[0], '/app/p2pstream', 'server-installation-identity', check=False)
                if inspection['State']['Running'] and identity.stdout.decode().strip() == self.journal['installation']:
                    # Original versions have no private runtime socket. Probe the
                    # original management listener with its own CA from /data.
                    health = docker('exec', ids[0], '/app/p2pstream', 'server-health', check=False)
                    if health.returncode == 0:
                        return
            time.sleep(2)
        raise Failure('Restored original server health could not be confirmed')

    def recover_failure(self, error):
        if isinstance(error, UnsettledHelper):
            raise error
        if self.journal.get('detached_continuation'):
            self.phase('recovery_required', error=str(error))
            raise Failure(str(error) + '; retry the declared detached target with sudo ' + str(self.directory / 'manage') + ' repair or rollback') from error
        activating = self.interrupted_initial_activation()
        self.phase('activating' if activating else 'recovery_required', error=str(error))
        try:
            self.rollback()
        except Exception as rollback_error:
            # Preserve only an actual initial activation whose rollback proved
            # absence before starting. Once rollback enters rolling_back, even
            # a later missing container must stay on the rollback recovery path.
            resume = activating and isinstance(rollback_error, MissingRollbackServer) and self.journal['phase'] == 'activating'
            self.phase('activating' if resume else 'recovery_required', error=str(error) + '; configuration rollback failed: ' + str(rollback_error))
        raise Failure(str(error) + '\nDurable recovery state: ' + str(self.directory / 'host.json') + '\nRecovery: sudo ' + str(self.directory / 'manage') + ' rollback\nResume: sudo ' + str(self.directory / 'manage') + ' repair') from error

def validate_running(container, model, image, arch, expected):
    require(container['State']['Running'], 'Selected server is not running')
    labels = container['Config']['Labels']
    require(labels.get('com.docker.compose.project') == model['name'] and labels.get('com.docker.compose.service') == 'p2pstream', 'Resolved Compose project does not match running server')
    require(image['Os'] == 'linux' and image['Architecture'] == arch, 'Running image platform differs from selected environment')
    immutable = image['Config']['Labels']
    require((immutable.get('org.opencontainers.image.version'), immutable.get('org.opencontainers.image.revision')) == (expected.expect_version, expected.expect_commit), 'Stale command: installed release changed; refresh the selected server')
    require(immutable.get('org.opencontainers.image.source', '').lower() == 'https://github.com/' + expected.repository.lower(), 'Running image belongs to another publisher')
    volumes = model.get('volumes', {})
    data = [v for v in model['services']['p2pstream'].get('volumes', []) if v['target'] == '/data']
    require(len(data) == 1 and data[0]['type'] == 'volume' and not data[0].get('read_only') and not data[0].get('volume', {}).get('subpath'), 'A writable, whole named /data volume is required')
    name = volumes.get(data[0]['source'], {}).get('name')
    mounts = [v for v in container['Mounts'] if v['Destination'] == '/data']
    require(len(mounts) == 1 and mounts[0].get('Name') == name and mounts[0]['RW'], 'Running data mount differs from resolved Compose input')
    for mount in container['Mounts']:
        require(mount['Destination'] == '/data' or not mount['Destination'].startswith('/data/'), 'Running nested data mounts cannot be restored safely')
    return name

def require_data_writers(volume, allowed, executor=None):
    ids = docker('ps', '--no-trunc', '--quiet', '--filter', 'volume=' + volume).stdout.decode().split()
    for container in ids:
        if container == allowed:
            continue
        info = decode(docker('inspect', container).stdout)[0]
        if executor and info['Config']['Labels'].get('p2pstream.server-update.executor') == executor:
            continue
        require(not any(m.get('Name') == volume and m['RW'] for m in info['Mounts']), 'Another running container writes the data volume; stop it before enrollment')

def install(args, options):
    arch = local_engine()
    require(arch == args.expect_arch, 'Docker daemon architecture differs from selected server')
    require(str(uuid.UUID(args.expect_installation)) == args.expect_installation, 'Invalid installation identity')
    require(HEX.fullmatch(args.manifest_sha256), 'A verified manifest pin is required')
    options = compose_options(options)
    source_dir = str(Path.cwd().resolve())
    # Verify wrong-host/stale commands BEFORE writing any state or downloading
    # tools. Discovery is read-only on the selected host deployment.
    ids = docker('compose', *options, 'ps', '--quiet', 'p2pstream', cwd=source_dir).stdout.decode().split()
    require(len(ids) == 1, 'Expected exactly one running p2pstream service in this Compose context')
    container = decode(docker('inspect', ids[0]).stdout)[0]
    identity = docker('exec', ids[0], '/app/p2pstream', 'server-installation-identity').stdout.decode().strip()
    require(identity == args.expect_installation, 'Wrong Docker host or Compose deployment: installation identity differs from selected environment')
    image = decode(docker('image', 'inspect', container['Image']).stdout)[0]
    model_raw = docker('compose', *options, 'config', '--format', 'json', cwd=source_dir).stdout
    model = decode(model_raw)
    data_volume = validate_running(container, model, image, arch, args)
    migration_requested = False
    owned_aliases = {}
    removed_source_files = None
    if any(os.path.lexists(STATE / name) for name in ('host.json', 'archive.json', 'reenrollment.json', CANCELLATION)):
        host = Host()
        with host.lock(allow_archive=True):
            cancellation = cancellation_receipt(STATE, identity, args.repository, args.expect_channel)
            if cancellation and not cancellation['completed']:
                require(not host.journal, 'Enrollment cancellation is incomplete; retry the verified cancellation command before setup')
                cancellation['completed'] = True
                atomic(STATE / CANCELLATION, cancellation)
            if host.journal:
                require(host.journal.get('installation') == identity, 'State directory belongs to another installation')
            elif (STATE / 'reenrollment.json').exists():
                context = read_json(STATE / 'reenrollment.json')
                require(context['installation'] == identity, 'Reenrollment ownership belongs to another installation')
                if context['source_directory'] == source_dir and context['compose_options'] == options:
                    owned_aliases = host.validate_reenrollment_context(context)
                    removed_source_files = context['source_files']
            if host.journal.get('phase') == 'removed':
                plan = host.journal.get('source_bridge', {})
                owned_aliases = {item['path']: plan.get('export_sha256') for item in plan.get('files', []) if item['editable'] is None and os.path.lexists(item['path'])}
                if host.journal.get('source_directory') == source_dir and host.journal.get('compose_options') == options:
                    removed_source_files = host.journal.get('source_files')
            if host.journal and host.journal['phase'] != 'removed':
                require(host.journal.get('source_directory') == source_dir and host.journal.get('compose_options') == options, 'Use the recorded Compose context for this enrollment')
                if host.journal['phase'] != 'healthy' and not host.journal.get('verified_version') and not host.journal.get('verified_commit'):
                    # Check before saved repair can write a model, start helpers,
                    # or convert a failed preflight into a rollback transaction.
                    require((host.journal.get('version'), host.journal.get('commit')) == (args.expect_version, args.expect_commit), 'Running release changed after setup was staged; use verified upgraded-deployment cancellation before fresh enrollment')
                    staged_image = host.journal.get('before_image_id') or decode(docker('image', 'inspect', host.journal['server_image']).stdout)[0]['Id']
                    require(container['Image'] == staged_image, 'Running image changed after setup was staged; use verified upgraded-deployment cancellation before fresh enrollment')
            if host.journal.get('phase') == 'healthy':
                try:
                    host.healthy()
                except (Failure, OSError, ValueError) as error:
                    # The verified installation bundle may be newer than the
                    # installed host controller. Recover only an exact legacy
                    # source recreation using its saved executor and credentials
                    # before attempting protocol migration. Hand off compatible
                    # recovery code before its first durable phase; retain the
                    # saved executor, credentials and update floors.
                    if isinstance(error, UnsettledHelper) or host.config.get('deployment_protocol', 0) != 0 or host.journal['phase'] != 'healthy':
                        raise
                    host.idle()
                    existing = host.server_container()
                    if existing['Config']['Labels'].get('com.docker.compose.config-hash') == host.canonical_hash():
                        raise  # A socket/server failure is not a detached binding.
                    host.prepare_recreation(detached=True, before_persist=lambda: host.handoff_recovery_controller(args.bundle))
                    host.recreate_server()
                    # Repair deliberately replaced the selected container. Bind
                    # subsequent installation/migration checks to its identity,
                    # rather than treating the expected replacement as drift.
                    ids = docker('compose', *options, 'ps', '--quiet', 'p2pstream', cwd=source_dir).stdout.decode().split()
                    require(len(ids) == 1, 'Expected one repaired server before deployment migration')
                    container = decode(docker('inspect', ids[0]).stdout)[0]
                    identity = docker('exec', ids[0], '/app/p2pstream', 'server-installation-identity').stdout.decode().strip()
                    require(identity == args.expect_installation, 'Installation changed after legacy binding repair')
                    image = decode(docker('image', 'inspect', container['Image']).stdout)[0]
                    model_raw = docker('compose', *options, 'config', '--format', 'json', cwd=source_dir).stdout
                    model = decode(model_raw)
                    data_volume = validate_running(container, model, image, arch, args)
                migration_requested = bool(getattr(args, 'migrate_deployment', False) and host.config.get('deployment_protocol') != 2)
                if not migration_requested:
                    print('Enrollment healthy; current image and credentials preserved.')
                    return
        # Reuse the saved tools, pins and credentials, never the downloaded
        # helper, to resume an interrupted enrollment.
        if host.journal and host.journal['phase'] != 'removed' and not migration_requested:
            manage(argparse.Namespace(action='repair'))
            return
    # Roundtrip the escaped snapshot with precisely the host Compose version.
    with tempfile.TemporaryDirectory(prefix='p2pstream-preflight-') as tmp:
        path = Path(tmp) / 'compose.json'
        path.write_bytes(model_raw)
        path.chmod(0o600)
        fields = docker('compose', '--project-directory', source_dir, '-f', str(path), 'config', '--hash', 'p2pstream').stdout.decode().split()
        require(migration_requested or len(fields) == 2 and fields[1] == container['Config']['Labels'].get('com.docker.compose.config-hash'), 'Compose inputs differ from the running server; apply or revert pending changes before enrollment')
    manifest, desc, server_image, manifest_hash = release_metadata(args.repository, args.expect_version, args.expect_commit, args.expect_channel, args.manifest_sha256)
    check_cancellation_floor(STATE, args, manifest, manifest_hash)
    platform = [p for p in manifest['oci_images'][0]['platforms'] if p['os'] == 'linux' and p['arch'] == arch]
    require(len(platform) == 1, 'Release lacks the selected server platform')
    # Resolve exact index and compare its selected local immutable image config
    # ID. RepoDigests ordering is never trusted.
    docker('pull', '--quiet', '--platform', 'linux/' + arch, server_image)
    require(decode(docker('image', 'inspect', server_image).stdout)[0]['Id'] == container['Image'], 'Running image is not the canonical release image')
    updater_image = desc['updater_images']['linux/' + arch]
    docker('pull', '--quiet', '--platform', 'linux/' + arch, updater_image)
    helper = decode(docker('image', 'inspect', updater_image).stdout)[0]
    helper_labels = helper['Config'].get('Labels', {})
    require(helper['Os'] == 'linux' and helper['Architecture'] == arch and helper_labels.get('p2pstream.image.role') == 'server-updater' and helper_labels.get('org.opencontainers.image.version') == args.expect_version and helper_labels.get('org.opencontainers.image.revision') == args.expect_commit, 'Published updater identity/platform verification failed')
    bundle = Path(args.bundle).resolve()
    require(all((bundle / name).is_file() and not (bundle / name).is_symlink() for name in BUNDLE_FILES), 'Incomplete verified installation bundle')
    host = Host()
    host.capability(container['Image'], ids[0])
    host.capability(updater_image)
    source_files = removed_source_files or compose_sources(options, container)
    layout = source_layout(source_dir, source_files, model['name'], owned_aliases)
    with host.lock(allow_archive=True):
        check_cancellation_floor(STATE, args, manifest, manifest_hash)
        if host.journal.get('phase') == 'removed':
            host.archive_removed()
        if host.journal:
            require(host.journal.get('installation') == identity, 'State directory belongs to another installation')
            if host.journal.get('phase') == 'healthy':
                host.healthy()
                if migration_requested:
                    host.idle()
                    actual = host.server_container()
                    require(actual['Id'] == ids[0] and actual['Image'] == container['Image'] and actual['Config']['Labels'].get('com.docker.compose.config-hash') == container['Config']['Labels'].get('com.docker.compose.config-hash'), 'Deployment changed while migration tools were downloaded; refresh the selected server setup block')
                    current_inputs = docker('compose', *options, 'config', '--format', 'json', cwd=source_dir).stdout
                    require(current_inputs == model_raw, 'Compose inputs changed while migration tools were downloaded; restore or refresh before migration')
                    original = host.current_model_path().read_bytes()
                    atomic(STATE / 'migration-compose.json', original)
                    atomic(STATE / 'migration-config.json', (STATE / 'config.json').read_bytes())
                    host.prepare_deployment_directory()
                    for name in ('server-updater-host.py', 'server-updater-compose.sh'):
                        atomic(STATE / name, (bundle / name).read_bytes())
                    host.phase('migrating_deployment', source_files=source_files, source_layout=layout, updater_image=updater_image,
                               deployment_migration={'model_sha256': digest(original), 'config_sha256': digest((STATE / 'config.json').read_bytes()), 'updater_image': updater_image, 'generation': str(uuid.uuid4())})
                    host.updater = updater_image
                    host.migrate_deployment()
                    return
                print('Enrollment already healthy; no restart and credentials unchanged.')
                return
            raise Failure('Existing setup requires the saved controller, preserving its credentials: sudo ' + str(STATE / 'manage') + ' repair (or rollback).')
        current_ids = docker('compose', *options, 'ps', '--quiet', 'p2pstream', cwd=source_dir).stdout.decode().split()
        current_raw = docker('compose', *options, 'config', '--format', 'json', cwd=source_dir).stdout
        require(current_ids == ids and current_raw == model_raw, 'Deployment changed during dependency downloads; refresh and rerun setup')
        current_info = decode(docker('inspect', ids[0]).stdout)[0]
        require(current_info['State']['Running'] and current_info['Image'] == container['Image'] and current_info['Config']['Labels'].get('com.docker.compose.config-hash') == container['Config']['Labels'].get('com.docker.compose.config-hash'), 'Running deployment changed before enrollment')
        require(docker('exec', ids[0], '/app/p2pstream', 'server-installation-identity').stdout.decode().strip() == identity, 'Installation identity changed before enrollment')
        require(docker('exec', ids[0], '/app/p2pstream', 'server-health', check=False).returncode == 0, 'Running management listener is unhealthy; repair it before enrollment')
        require_data_writers(data_volume, ids[0])
        # Detect another executor/control mount via Go validator before activation.
        (STATE / 'release').mkdir(mode=0o700, exist_ok=True)
        for name, data in manifest['_files'].items():
            atomic(STATE / 'release' / name, data)
        atomic(STATE / 'input.json', model_raw)
        for name in ('server-updater-host.py', 'server-updater-compose.sh'):
            atomic(STATE / name, (bundle / name).read_bytes())
        atomic(STATE / 'manage', b'#!/bin/sh\nexec python3 -I /etc/p2pstream-server-updater/server-updater-host.py "$@"\n')
        (STATE / 'manage').chmod(0o700)
        atomic(STATE / 'compose', (bundle / 'server-updater-compose.sh').read_bytes())
        (STATE / 'compose').chmod(0o700)
        host.updater = updater_image
        host.phase('preparing', installation=identity, source_directory=source_dir, compose_options=options,
                   version=args.expect_version, commit=args.expect_commit, channel=args.expect_channel,
                   architecture=arch, repository=args.repository, manifest_sha256=manifest_hash,
                   updater_image=updater_image, server_image=server_image, source_files=source_files, source_layout=layout)
        (STATE / 'reenrollment.json').unlink(missing_ok=True)
        host.sync_directory()
        deployment_directory = host.prepare_deployment_directory()
        host.command('enroll', '--directory', str(STATE), '--compose', str(STATE / 'input.json'), '--image', server_image,
                     '--updater-image', updater_image, '--installation', identity, '--release-directory', str(STATE / 'release'), '--deployment-directory', str(deployment_directory))
        host.config = read_json(STATE / 'config.json')
        atomic(STATE / 'enrolled-compose.json', host.current_model_path().read_bytes())
        before = decode(model_raw)
        before['services']['p2pstream']['image'] = server_image
        before_raw = json.dumps(before, separators=(',', ':')).encode()
        atomic(STATE / 'before-compose.json', before_raw)
        host.phase('prepared', before_sha256=digest(before_raw), before_image_id=container['Image'])
        try:
            host.activate()
        except Exception as error:
            host.recover_failure(error)
        print('Host settings: edit ' + ' '.join(item['editable'] for item in host.journal.get('source_bridge', {}).get('files', []) if item['editable']) + ', then sudo ' + str(STATE / 'manage') + ' apply')

def manage(args):
    local_engine()
    host = Host()
    with host.lock(allow_archive=args.action == 'discard-preparation'):
        require(host.journal, 'No saved enrollment; use the selected server setup command')
        receipt_path = STATE / CANCELLATION
        if os.path.lexists(receipt_path) and not read_json(receipt_path).get('completed'):
            # Even read-only Compose inspection starts a journaled helper. An
            # interrupted helper must not strand a transaction whose only safe
            # next action is finishing cancellation. Status exits before helpers.
            if args.action == 'status':
                print('Host setup: ' + host.journal['phase'])
            raise Failure('Enrollment cancellation is incomplete; retry the verified cancellation command')
        if args.action == 'discard-preparation':
            host.discard_preparation()
            return
        if args.action == 'inspect':
            require(args.compose_args and args.compose_args[0] in {'ps', 'logs', 'config'}, 'Only read-only Compose inspection is supported')
            sys.stdout.buffer.write(host.compose(*args.compose_args).stdout)
            return
        if args.action == 'status':
            print('Host setup: ' + host.journal['phase'])
            print('Compose inputs: ' + host.journal['source_directory'] + ' ' + ' '.join(host.journal['compose_options']))
            if host.journal.get('source_bridge') and host.journal['phase'] != 'removed':
                print('Editable settings: ' + ' '.join(item['editable'] for item in host.journal['source_bridge']['files'] if item['editable']))
            if host.journal['phase'] == 'removed':
                host.wait_original()
                print('Updater removed. Editable current deployment: ' + (host.journal['source_layout']['primary'] if host.config.get('deployment_protocol') == 2 else str(STATE / 'detached-compose.json')))
                return
            require(host.journal['phase'] == 'healthy', 'Setup is not enabled. Recovery: sudo ' + str(STATE / 'manage') + ' repair or rollback')
            host.healthy()
            print('Server health and authenticated updater verified.')
            return
        if args.action == 'logs':
            require(host.config, 'Updater is not yet staged')
            sys.stdout.buffer.write(host.compose('logs', '--tail', '100', 'p2pstream', 'p2pstream-server-updater').stdout)
            return
        if args.action == 'recover-update':
            recovering = host.journal['phase'] == 'recovering_update' or host.journal.get('recovery_phase') == 'recovering_update'
            diagnostics_failure = host.journal['phase'] == 'recovery_required' and host.journal.get('recovery_phase') == 'healthy'
            require(host.config and (host.journal['phase'] == 'healthy' or recovering or diagnostics_failure), 'Resolve host setup before data recovery')
            host.settle_helper()
            if diagnostics_failure:
                host.phase('healthy')
            operation = read_json(STATE / 'state.json').get('operation')
            require(operation and (operation['id'] == host.journal.get('recovery_operation') if recovering else operation['phase'] == 'recovery_required'), 'Explicit data recovery requires the recorded paused update; active updates must finish first')
            host.phase('recovering_update', recovery_operation=operation['id'])
            host.compose('stop', 'p2pstream-server-updater')
            host.command('recover')
            host.idle()
            host.start_executor()
            status = host.wait_healthy()
            host.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
            print('Paused update recovery completed; server and updater verified.')
            return
        if args.action in {'repair', 'rollback'} and host.journal.get('detached_continuation'):
            host.resume_detached_continuation()
            return
        if args.action == 'rollback':
            require(host.journal['phase'] in {'preparing', 'prepared', 'activating', 'applying', 'removing', 'rolling_back', 'recovery_required', 'rolled_back'}, 'No unfinished host transaction exists; rollback cannot downgrade a healthy updated deployment')
            require(host.journal.get('recovery_phase') not in {'healthy', 'repairing', 'recreating', 'migrating_deployment', 'recovering_update'}, 'Use repair or recover-update for the recorded helper; no unfinished configuration transaction exists')
            host.settle_helper()
            activating = host.interrupted_initial_activation()
            try:
                host.rollback()
            except Exception as error:
                # Refusing an absent server before rollback starts must retain
                # the same initial-activation repair path as automatic recovery.
                resume = activating and isinstance(error, MissingRollbackServer) and host.journal['phase'] == 'activating'
                host.phase('activating' if resume else 'recovery_required', error=str(error))
                raise
            return
        if args.action == 'recreate':
            require(host.config and host.journal['phase'] == 'healthy', 'Resolve interrupted host work with manage repair before recreation')
            host.idle()
            host.healthy()
            host.prepare_recreation()
            host.recreate_server()
            return
        if args.action == 'repair':
            host.settle_helper()
            require(host.journal['phase'] != 'removed', 'Enrollment was removed; use a fresh setup command')
            require(host.journal['phase'] != 'recovering_update' and host.journal.get('recovery_phase') != 'recovering_update', 'Data recovery was interrupted; run sudo ' + str(STATE / 'manage') + ' recover-update to resume it')
            if host.journal['phase'] == 'recovery_required' and host.journal.get('recovery_phase') in {'healthy', 'repairing'}:
                host.phase(host.journal['recovery_phase'])
            host.idle()
            if host.journal['phase'] == 'migrating_deployment' or host.journal.get('recovery_phase') == 'migrating_deployment':
                host.phase('migrating_deployment')
                host.migrate_deployment()
                return
            if host.journal['phase'] == 'recreating' or host.journal.get('recovery_phase') == 'recreating':
                host.phase('recreating')
                host.recreate_server()
                return
            if host.journal['phase'] in {'healthy', 'repairing'}:
                if host.journal['phase'] == 'healthy':
                    try:
                        host.healthy()
                        print('Enrollment healthy; credentials preserved.')
                        return
                    except (Failure, OSError, ValueError):
                        pass
                host.phase('repairing')
                executor_lock = host.stop_executor()
                os.close(executor_lock)
                host.start_executor()
                try:
                    host.healthy()
                except (Failure, OSError, ValueError):
                    # An ordinary source Compose recreation can strip only the
                    # private binding. Prove an exact same-image/settings match
                    # before replacing the server; never adopt edited settings.
                    host.prepare_recreation(detached=True)
                    host.recreate_server()
                    return
                status = host.wait_healthy()
                host.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
                print('Updater repaired; credentials preserved.')
                return
            if not host.config:
                # Resume preparation after interrupted download/write/enroll.
                host.updater = host.journal['updater_image']
                deployment = host.journal.get('deployment_directory')
                extra = ('--deployment-directory', deployment) if deployment else ()
                host.command('enroll', '--directory', str(STATE), '--compose', str(STATE / 'input.json'), '--image', host.journal['server_image'], '--updater-image', host.updater, '--installation', host.journal['installation'], '--release-directory', str(STATE / 'release'), *extra)
                host.config = read_json(STATE / 'config.json')
            if not (STATE / 'enrolled-compose.json').exists():
                atomic(STATE / 'enrolled-compose.json', host.current_model_path().read_bytes())
            if not (STATE / 'before-compose.json').exists():
                before = read_json(STATE / 'input.json')
                before['services']['p2pstream']['image'] = host.journal['server_image']
                before_raw = json.dumps(before, separators=(',', ':')).encode()
                atomic(STATE / 'before-compose.json', before_raw)
                image_id = decode(docker('image', 'inspect', host.journal['server_image']).stdout)[0]['Id']
                host.phase('prepared', before_sha256=digest(before_raw), before_image_id=image_id)
            if not host.journal.get('before_sha256'):
                before_raw = (STATE / 'before-compose.json').read_bytes()
                image_id = decode(docker('image', 'inspect', host.journal['server_image']).stdout)[0]['Id']
                host.phase('prepared', before_sha256=digest(before_raw), before_image_id=image_id)
            # Interrupted apply/remove rolls back first. Operators can edit and
            # explicitly apply again after a confirmed restoration.
            if host.journal['phase'] in {'applying', 'removing', 'rolling_back', 'recovery_required'}:
                host.rollback()
                return
            host.write_model((STATE / 'enrolled-compose.json').read_bytes())
            try:
                host.activate()
            except Exception as error:
                host.recover_failure(error)
            return
        require(host.config and host.journal['phase'] == 'healthy', 'Resolve the interrupted setup before apply/remove')
        host.idle()
        host.healthy()  # Configuration hash and immutable runtime identity.
        current = host.current_model_path().read_bytes()
        model = decode(current)
        if args.action == 'apply':
            raw = docker('compose', *host.input_options(), 'config', '--format', 'json', cwd=host.journal['source_directory']).stdout
            atomic(STATE / 'candidate-input.json', raw)
            host.command('configure', '--compose', str(STATE / 'candidate-input.json'), '--output', str(STATE / 'candidate-compose.json'))
            # Validate with pinned Compose before stopping either service.
            host.compose('config', '--format', 'json', model='candidate-compose.json')
            candidate = (STATE / 'candidate-compose.json').read_bytes()
            if host.config.get('deployment_protocol') == 2:
                candidate_model = decode(candidate)
                candidate_model['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION'] = str(uuid.uuid4())
                candidate = json.dumps(candidate_model, separators=(',', ':')).encode()
                atomic(STATE / 'candidate-compose.json', candidate)
                host.compose('config', '--format', 'json', model='candidate-compose.json')
        elif args.action == 'remove':
            candidate = remove_model(model)
            atomic(STATE / 'candidate-compose.json', candidate)
            host.compose('config', '--format', 'json', model='candidate-compose.json')
        else:
            raise Failure('Unsupported host operation')
        atomic(STATE / 'before-compose.json', current)
        image_id = decode(docker('image', 'inspect', model['services']['p2pstream']['image']).stdout)[0]['Id']
        host.phase('applying' if args.action == 'apply' else 'removing', before_sha256=digest(current), before_image_id=image_id)
        executor_lock = None
        try:
            executor_lock = host.stop_executor()
            host.prepare_replacement(host.server_container(), args.action, decode(candidate))
            if host.config.get('deployment_protocol') != 2:
                host.runtime('block')
            host.write_model(candidate)
            host.deploy_server(detached=args.action == 'remove')
            os.close(executor_lock)
            executor_lock = None
            if args.action == 'apply':
                host.start_executor()
                status = host.wait_healthy()
                host.record_runtime()
                atomic(STATE / 'enrolled-compose.json', candidate)
                host.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
                print('Configuration applied; current pinned image and private updater connection preserved.')
            else:
                host.finish_removal()
        except Exception as error:
            if executor_lock is not None:
                os.close(executor_lock)
            host.recover_failure(error)

def remove_model(model):
    model = decode(json.dumps(model))
    service = model['services']['p2pstream']
    env = service['environment']
    for key in list(env):
        if key.startswith('SERVER_UPDATE_'):
            del env[key]
    service.get('labels', {}).pop('p2pstream.server-update.instance', None)
    service['volumes'] = [v for v in service['volumes'] if v['target'] != '/run/p2pstream-server-update']
    del model['services']['p2pstream-server-updater']
    model['volumes'].pop('server-updater-data', None)
    return json.dumps(model, separators=(',', ':')).encode()

def deploy(args):
    arch = local_engine(updater=False)
    manifest, desc, image, manifest_hash = release_metadata(args.repository, args.release)
    require(any(p['os'] == 'linux' and p['arch'] == arch for p in manifest['oci_images'][0]['platforms']), 'Release lacks this platform')
    directory = Path(args.directory).resolve()
    directory.mkdir(mode=0o750, parents=True, exist_ok=True)
    require(not (directory / 'compose.yaml').exists() and not (directory / '.env').exists(), 'Deployment inputs already exist; use them instead of overwriting them')
    bundle = Path(__file__).resolve().parent
    shutil.copyfile(bundle / 'compose.yaml', directory / 'compose.yaml')
    env = (bundle / '.env.example').read_bytes() + b'\nP2PSTREAM_IMAGE=' + image.encode() + b'\n'
    atomic(directory / '.env', env)
    atomic(directory / 'release.json', {'repository': args.repository, 'version': args.release, 'commit': manifest['commit'], 'channel': manifest['channel'], 'manifest_sha256': manifest_hash, 'image': image})
    print('Prepared ' + str(directory) + ' with release ' + args.release + ' pinned by digest.')
    print('Edit .env with your management hostname and published ports, then run docker compose up -d from that directory.')

def main():
    def interrupted(signum, frame):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGHUP, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='action', required=True)
    setup = commands.add_parser('install', help='Enroll the exact running installation selected in management')
    setup.add_argument('--bundle', required=True)
    for key in ['installation', 'version', 'commit', 'channel', 'arch']:
        setup.add_argument('--expect-' + key, required=True)
    setup.add_argument('--manifest-sha256', required=True)
    setup.add_argument('--repository', required=True)
    setup.add_argument('--migrate-deployment', action='store_true', help='Explicitly migrate compatible legacy enrollment to managed Compose ownership')
    cancel = commands.add_parser('cancel-enrollment', help='Archive a proven restored, never-completed enrollment using a verified release bundle')
    for key in ('installation', 'channel'):
        cancel.add_argument('--expect-' + key, required=True)
    cancel.add_argument('--repository', required=True)
    cancel.add_argument('--bundle', required=True)
    cancel.add_argument('--upgraded-deployment', action='store_true', help='Prove a manually upgraded published server before retiring a never-completed enrollment')
    for key in ('version', 'commit', 'arch'):
        cancel.add_argument('--expect-' + key)
    cancel.add_argument('--manifest-sha256')
    for action in ['status', 'logs', 'apply', 'recreate', 'repair', 'rollback', 'remove', 'recover-update', 'discard-preparation']:
        commands.add_parser(action)
    inspect = commands.add_parser('inspect')
    inspect.add_argument('compose_args', nargs=argparse.REMAINDER)
    deployment = commands.add_parser('deploy', help='Prepare editable pinned Docker deployment inputs')
    deployment.add_argument('--release', required=True)
    deployment.add_argument('--repository', default='Kirari04/p2pstream')
    deployment.add_argument('--directory', default='p2pstream')
    parts = sys.argv[1:]
    separator = parts.index('--') if '--' in parts else len(parts)
    args = parser.parse_args(parts[:separator])
    options = parts[separator + 1:]
    try:
        if args.action == 'install':
            install(args, options)
        elif args.action == 'cancel-enrollment':
            require(not options, 'Cancellation uses the saved Compose context; additional options are refused')
            require(str(uuid.UUID(args.expect_installation)) == args.expect_installation and REPOSITORY.fullmatch(args.repository) and args.expect_channel in {'stable', 'staging'}, 'Invalid cancellation identity, publisher or channel')
            current_identity = (args.expect_version, args.expect_commit, args.expect_arch, args.manifest_sha256)
            if args.upgraded_deployment:
                require(all(current_identity), 'Upgraded cancellation requires current version, commit, architecture and manifest SHA-256')
                require(VERSION.fullmatch(args.expect_version) and re.fullmatch('[0-9a-f]{40}', args.expect_commit) and args.expect_arch in {'amd64', 'arm64'} and HEX.fullmatch(args.manifest_sha256), 'Invalid current release identity')
            else:
                require(not any(value is not None for value in current_identity), 'Current release identity requires --upgraded-deployment')
            local_engine()
            controller = Host()
            with controller.lock(allow_archive=True):
                controller.cancel_enrollment(args)
            if args.upgraded_deployment:
                print('Failed enrollment cancelled and archived privately; verified current server remains running. Release floors retained. Copy its fresh setup command to enroll.')
            else:
                print('Failed enrollment cancelled and archived privately; original server remains running. Release floors retained. Manually deploy a current fixed published server using the same Compose inputs and data volume, then copy its fresh setup command.')
        elif args.action == 'deploy':
            require(not options, 'Unexpected Compose options')
            deploy(args)
        else:
            require(not options, 'Saved host operations use their recorded Compose context')
            manage(args)
    except (Failure, OSError, ValueError, KeyError) as error:
        print('p2pstream: ' + str(error), file=sys.stderr)
        sys.exit(1)

if __name__ == '__main__':
    main()

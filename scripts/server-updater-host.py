#!/usr/bin/env python3
"""Release-distributed host controller. No third-party Python modules or Git checkout.

Only fixed Docker/Compose operations are executed. Deployment hooks and build
steps are rejected by the independently shipped Go layout validator.
"""
import argparse
import contextlib
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
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

class Failure(RuntimeError):
    pass

class UnsettledHelper(Failure):
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

def read_json(path):
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and info.st_size <= 2 * 1024 * 1024, 'Unsafe or oversized state file: ' + str(path))
    return decode(path.read_bytes())

def atomic(path, data):
    if not isinstance(data, bytes):
        data = json.dumps(data, separators=(',', ':'), sort_keys=True).encode()
    fd, name = tempfile.mkstemp(prefix='.host-', dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
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

class Host:
    def __init__(self, directory=STATE):
        self.directory = directory
        self.journal = {}
        self.config = None
        self.updater = None

    @contextlib.contextmanager
    def lock(self):
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
            if (self.directory / 'host.json').exists():
                self.journal = read_json(self.directory / 'host.json')
            if (self.directory / 'config.json').exists():
                self.config = read_json(self.directory / 'config.json')
                model = read_json(self.directory / 'compose.json')
                self.updater = self.journal.get('updater_image') or model.get('services', {}).get('p2pstream-server-updater', {}).get('image', '')
                require(re.fullmatch(re.escape('ghcr.io/' + self.config['repository'].lower() + '-updater@sha256:') + '[0-9a-f]{64}', self.updater), 'Host operations require the enrolled published updater image')
            yield
        finally:
            os.close(fd)

    def phase(self, phase, **values):
        self.journal.update(values)
        self.journal['phase'] = phase
        if phase == 'healthy':
            self.journal.pop('recovery_phase', None)
            self.journal.pop('recovery_operation', None)
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

    def run_helper(self, entrypoint, args, *, mounts=(), readonly=False, image=None, timeout=180):
        if readonly:
            require(not (self.directory / 'helper.json').exists(), 'An unfinished host helper exists; use explicit repair or recover-update before inspection')
        self.settle_helper()
        name = self.helper_name()
        atomic(self.directory / 'helper.json', {'name': name, 'entrypoint': entrypoint, 'phase': self.journal['phase'], 'readonly': readonly})
        mount = 'type=bind,src=' + str(self.directory) + ',dst=' + str(self.directory) + (',readonly' if readonly else '')
        try:
            result = docker('run', '--rm', '--name', name, '--label', 'p2pstream.server-update.host-helper=' + self.journal['installation'],
                            '--user', '0:0', '--env', 'COMPOSE_DISABLE_ENV_FILE=1', '--entrypoint', entrypoint,
                            '--mount', 'type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock',
                            '--mount', mount, *mounts, image or self.updater, *args, timeout=timeout)
        except BaseException:
            # Killing the Docker client does not kill its container. Confirm the
            # helper is stopped before rollback or releasing host ownership.
            self.settle_helper()
            raise
        self.settle_helper()
        return result

    def tools(self, *args, image=None, readonly=False):
        # Read Compose inputs on the HOST, never inside this container. Only
        # normalized snapshots underneath the private state directory are used.
        return self.run_helper('/usr/local/bin/docker', args, image=image, readonly=readonly)

    def compose(self, *args, model='compose.json'):
        return self.tools('compose', '--project-name', self.config['project'], '--project-directory', str(self.directory), '-f', str(self.directory / model), *args, readonly=bool(args and args[0] in {'ps', 'logs', 'config'}))

    def command(self, *args):
        mounts = []
        if args and args[0] == 'recover':
            mounts = ['--label', 'p2pstream.server-update.executor=' + self.config['instance_id'],
                      '--mount', 'type=volume,src=' + self.config['data_volume'] + ',dst=/server-data']
        return self.run_helper('/app/p2pstream', ('server-updater', *args), mounts=mounts, readonly=bool(args and args[0] == 'check'), timeout=900 if mounts else 180)

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
        self.status_socket()
        return decode(self.command('check').stdout)

    def wait_healthy(self):
        deadline = time.monotonic() + 120
        consecutive = 0
        while time.monotonic() < deadline:
            try:
                status = self.healthy()
                consecutive += 1
                if consecutive >= 3:
                    return status
            except (Failure, OSError, ValueError):
                consecutive = 0
            time.sleep(2)
        raise Failure('Server health, deployment hash, or authenticated updater verification failed')

    def archive_removed(self):
        require(self.journal.get('phase') == 'removed', 'Only a completed removal can be reenrolled')
        self.wait_original()
        directory = self.directory / 'history' / str(uuid.uuid4())
        directory.mkdir(mode=0o700, parents=True)
        for child in self.directory.iterdir():
            if child.name not in {'host.lock', 'history', 'detached-compose.json'}:
                child.rename(directory / child.name)
        self.journal = {}
        self.config = None
        self.updater = None

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

    def activate(self):
        self.idle()
        raw = docker('compose', *self.journal['compose_options'], 'config', '--format', 'json', cwd=self.journal['source_directory']).stdout
        require(raw == (self.directory / 'input.json').read_bytes(), 'Compose inputs changed after setup was staged; restore the recorded inputs before repair')
        expected = decode(docker('image', 'inspect', self.journal['server_image']).stdout)[0]['Id']
        ids = self.compose('ps', '--all', '--quiet', 'p2pstream').stdout.decode().split()
        require(len(ids) == 1, 'Server disappeared after setup was staged; inspect the host before repair')
        actual = decode(docker('inspect', ids[0]).stdout)[0]
        require(actual['Image'] == expected, 'Running image changed after setup was staged; refusing a configuration-only replacement')
        require_data_writers(self.config['data_volume'], ids[0], self.config['instance_id'])
        self.phase('activating')
        self.start_executor()
        self.compose('up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'p2pstream')
        status = self.wait_healthy()
        self.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
        print('Server updates enabled: server health and private updater verified. Later updates require an operator action.')

    def rollback(self):
        require(self.journal['phase'] in {'preparing', 'prepared', 'activating', 'applying', 'removing', 'rolling_back', 'recovery_required', 'rolled_back'}, 'No unfinished host transaction exists; rollback cannot downgrade a healthy updated deployment')
        self.idle()
        self.phase('rolling_back')
        # Stop before changing models; a successful same-image configuration
        # rollback does not claim to restore application data.
        previous = self.directory / 'before-compose.json'
        require(previous.exists(), 'No saved deployment available for configuration rollback')
        require(digest(previous.read_bytes()) == self.journal.get('before_sha256'), 'Recovery snapshot differs from the recorded transaction')
        before = read_json(previous)
        ids = self.compose('ps', '--all', '--quiet', 'p2pstream', model='before-compose.json').stdout.decode().split()
        if ids:
            require(len(ids) == 1, 'Ambiguous recovery deployment')
            info = decode(docker('inspect', ids[0]).stdout)[0]
            require(info['Image'] == self.journal['before_image_id'], 'Running image changed outside this host transaction; refusing configuration-only rollback')
        helper_model = 'before-compose.json' if 'p2pstream-server-updater' in before['services'] else 'enrolled-compose.json'
        if (self.directory / helper_model).exists():
            self.compose('stop', 'p2pstream-server-updater', model=helper_model)
        self.compose('stop', 'p2pstream', model='before-compose.json')
        atomic(self.directory / 'compose.json', previous.read_bytes())
        model = read_json(previous)
        enrolled = 'p2pstream-server-updater' in model['services']
        self.compose('up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'p2pstream')
        if enrolled:
            self.start_executor()
            self.wait_healthy()
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
        self.phase('recovery_required', error=str(error))
        try:
            self.rollback()
        except Exception as rollback_error:
            self.phase('recovery_required', error=str(error) + '; configuration rollback failed: ' + str(rollback_error))
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
    if (STATE / 'host.json').exists():
        host = Host()
        with host.lock():
            require(host.journal.get('installation') == identity, 'State directory belongs to another installation')
            if host.journal['phase'] != 'removed':
                require(host.journal.get('source_directory') == source_dir and host.journal.get('compose_options') == options, 'Use the recorded Compose context for this enrollment')
            if host.journal['phase'] == 'healthy':
                host.healthy()
                print('Enrollment already healthy; no restart and credentials unchanged.')
                return
        # Reuse the saved tools, pins and credentials, never the downloaded
        # helper, to resume an interrupted enrollment.
        if host.journal['phase'] != 'removed':
            manage(argparse.Namespace(action='repair'))
            return
    # Roundtrip the escaped snapshot with precisely the host Compose version.
    with tempfile.TemporaryDirectory(prefix='p2pstream-preflight-') as tmp:
        path = Path(tmp) / 'compose.json'
        path.write_bytes(model_raw)
        path.chmod(0o600)
        fields = docker('compose', '--project-directory', source_dir, '-f', str(path), 'config', '--hash', 'p2pstream').stdout.decode().split()
        require(len(fields) == 2 and fields[1] == container['Config']['Labels'].get('com.docker.compose.config-hash'), 'Compose inputs differ from the running server; apply or revert pending changes before enrollment')
    manifest, desc, server_image, manifest_hash = release_metadata(args.repository, args.expect_version, args.expect_commit, args.expect_channel, args.manifest_sha256)
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
    with host.lock():
        if host.journal.get('phase') == 'removed':
            host.archive_removed()
        if host.journal:
            require(host.journal.get('installation') == identity, 'State directory belongs to another installation')
            if host.journal.get('phase') == 'healthy':
                host.healthy()
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
                   updater_image=updater_image, server_image=server_image)
        host.command('enroll', '--directory', str(STATE), '--compose', str(STATE / 'input.json'), '--image', server_image,
                     '--updater-image', updater_image, '--installation', identity, '--release-directory', str(STATE / 'release'))
        host.config = read_json(STATE / 'config.json')
        atomic(STATE / 'enrolled-compose.json', (STATE / 'compose.json').read_bytes())
        before = decode(model_raw)
        before['services']['p2pstream']['image'] = server_image
        before_raw = json.dumps(before, separators=(',', ':')).encode()
        atomic(STATE / 'before-compose.json', before_raw)
        host.phase('prepared', before_sha256=digest(before_raw), before_image_id=container['Image'])
        try:
            host.activate()
        except Exception as error:
            host.recover_failure(error)
        print('Host settings: edit the original Compose inputs, then sudo ' + str(STATE / 'manage') + ' apply')

def manage(args):
    local_engine()
    host = Host()
    with host.lock():
        require(host.journal, 'No saved enrollment; use the selected server setup command')
        if args.action == 'inspect':
            require(args.compose_args and args.compose_args[0] in {'ps', 'logs', 'config'}, 'Only read-only Compose inspection is supported')
            sys.stdout.buffer.write(host.compose(*args.compose_args).stdout)
            return
        if args.action == 'status':
            print('Host setup: ' + host.journal['phase'])
            print('Compose inputs: ' + host.journal['source_directory'] + ' ' + ' '.join(host.journal['compose_options']))
            if host.journal['phase'] == 'removed':
                host.wait_original()
                print('Updater removed. Editable current deployment: ' + str(STATE / 'detached-compose.json'))
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
        if args.action == 'rollback':
            require(host.journal['phase'] in {'preparing', 'prepared', 'activating', 'applying', 'removing', 'rolling_back', 'recovery_required', 'rolled_back'}, 'No unfinished host transaction exists; rollback cannot downgrade a healthy updated deployment')
            require(host.journal.get('recovery_phase') not in {'healthy', 'repairing', 'recovering_update'}, 'Use repair or recover-update for the recorded helper; no unfinished configuration transaction exists')
            host.settle_helper()
            try:
                host.rollback()
            except Exception as error:
                host.phase('recovery_required', error=str(error))
                raise
            return
        if args.action == 'repair':
            host.settle_helper()
            require(host.journal['phase'] != 'removed', 'Enrollment was removed; use a fresh setup command')
            require(host.journal['phase'] != 'recovering_update' and host.journal.get('recovery_phase') != 'recovering_update', 'Data recovery was interrupted; run sudo ' + str(STATE / 'manage') + ' recover-update to resume it')
            if host.journal['phase'] == 'recovery_required' and host.journal.get('recovery_phase') in {'healthy', 'repairing'}:
                host.phase(host.journal['recovery_phase'])
            host.idle()
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
                status = host.wait_healthy()
                host.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
                print('Updater repaired; credentials preserved.')
                return
            if not host.config:
                # Resume preparation after interrupted download/write/enroll.
                host.updater = host.journal['updater_image']
                host.command('enroll', '--directory', str(STATE), '--compose', str(STATE / 'input.json'), '--image', host.journal['server_image'], '--updater-image', host.updater, '--installation', host.journal['installation'], '--release-directory', str(STATE / 'release'))
                host.config = read_json(STATE / 'config.json')
            if not (STATE / 'enrolled-compose.json').exists():
                atomic(STATE / 'enrolled-compose.json', (STATE / 'compose.json').read_bytes())
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
            atomic(STATE / 'compose.json', (STATE / 'enrolled-compose.json').read_bytes())
            try:
                host.activate()
            except Exception as error:
                host.recover_failure(error)
            return
        require(host.config and host.journal['phase'] == 'healthy', 'Resolve the interrupted setup before apply/remove')
        host.idle()
        host.healthy()  # Configuration hash and immutable runtime identity.
        current = (STATE / 'compose.json').read_bytes()
        model = decode(current)
        if args.action == 'apply':
            raw = docker('compose', *host.journal['compose_options'], 'config', '--format', 'json', cwd=host.journal['source_directory']).stdout
            atomic(STATE / 'candidate-input.json', raw)
            host.command('configure', '--compose', str(STATE / 'candidate-input.json'), '--output', str(STATE / 'candidate-compose.json'))
            # Validate with pinned Compose before stopping either service.
            host.compose('config', '--format', 'json', model='candidate-compose.json')
            candidate = (STATE / 'candidate-compose.json').read_bytes()
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
            atomic(STATE / 'compose.json', candidate)
            host.compose('up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'p2pstream')
            os.close(executor_lock)
            executor_lock = None
            if args.action == 'apply':
                host.start_executor()
                status = host.wait_healthy()
                atomic(STATE / 'enrolled-compose.json', candidate)
                host.phase('healthy', verified_version=status['version'], verified_commit=status['commit'], error='')
                print('Configuration applied; current pinned image and private updater connection preserved.')
            else:
                host.wait_original()
                # Remove the stopped executor using the saved pre-removal model.
                host.compose('rm', '--force', 'p2pstream-server-updater', model='before-compose.json')
                atomic(STATE / 'detached-compose.json', candidate)
                host.phase('removed', error='', source_directory=str(STATE), compose_options=['-p', host.config['project'], '-f', str(STATE / 'detached-compose.json')])
                print('Updater removed. Current pinned image, settings, ports and data retained.')
                print('Editable deployment: ' + str(STATE / 'detached-compose.json') + '; apply with sudo docker compose -p ' + host.config['project'] + ' -f ' + str(STATE / 'detached-compose.json') + ' up -d --no-deps --no-build p2pstream')
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
    for action in ['status', 'logs', 'apply', 'repair', 'rollback', 'remove', 'recover-update']:
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

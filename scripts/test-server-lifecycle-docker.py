#!/usr/bin/env python3
"""Source-built single-file host qualification inside test-server-updater.sh.

Only catalog metadata and explicit one-shot interruptions are fixtures. Every
successful enrollment, block, stop, publication, update, apply and removal uses
the shipped controller/CLI and real Docker. This is NOT published-pair release
qualification; that gate needs two feature-bearing published releases and their
verified bundles. Never run this script on a deployment host.

Workers are fresh processes. Instrumentation forwards real operations unchanged,
observes boundaries, and can interrupt immediately before/after a real operation.
Summary/log output contains check names and hashes only, never resolved models,
environment, credentials, owner contents, or database contents.
"""
import argparse
import base64
import copy
from datetime import datetime, timedelta, timezone
import gzip
import hashlib
import http.cookiejar
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import ssl
import stat
import subprocess
import sys
import tarfile
import time


ROOT = Path('/review/lifecycle')
STATE = Path('/etc/p2pstream-server-updater')
DEPLOYMENT = Path('/opt/p2pstream/deployment')
CANONICAL = DEPLOYMENT / 'compose.yaml'
INPUTS = Path('/opt/p2pstream/compose.inputs.yaml')
BUNDLE = Path('/review/install-bundle')
LAUNCHER = Path('/usr/local/sbin/p2pstream-server-update')
PROJECT = 'review-single-file-lifecycle'
VOLUME = PROJECT + '-data'
OWNER = '.p2pstream-runtime-owner.json'
SETUP_TOKEN = 'review-lifecycle-setup-0123456789abcdef'
PASSWORD = 'Review-lifecycle-only-password-123!'
INTERRUPTED = 75
READINESS_DEADLINE = None
STAGE = 'setup'
CHECKS = []
CONTAINER_FIELDS = ('Id', 'Image', 'Running', 'StartedAt', 'RestartCount', 'Labels', 'Mounts')


class CheckFailure(RuntimeError):
    """Fixed, safe diagnostics; never attach captured command output."""

    def __init__(self, check, *, changed_fields=(), worker_stage=None, worker_checks=()):
        super().__init__(check)
        self.changed_fields = list(changed_fields)
        self.worker_stage = worker_stage
        self.worker_checks = list(worker_checks)


class DeliberateInterruption(BaseException):
    pass


def require(value, check):
    if not value:
        raise CheckFailure(check)


def stage(name):
    # Callers supply only fixed harness stage names, never inspected values.
    global STAGE
    STAGE = name


def failure_report(error):
    code = str(error) if isinstance(error, CheckFailure) else type(error).__name__
    report = {'result': 'failed', 'check': code, 'stage': STAGE, 'checks': list(CHECKS),
              'qualification': 'source-built-real-docker', 'published_pair_gate': 'pending'}
    if isinstance(error, CheckFailure):
        report['changed_fields'] = error.changed_fields
        if error.worker_stage is not None:
            report.update(worker_stage=error.worker_stage, worker_checks=error.worker_checks)
    return report


def isolated():
    require(os.geteuid() == 0 and Path('/review/ISOLATED_TEST_VM').is_file()
            and socket.gethostbyname('ghcr.io') == '127.0.0.1', 'disposable-daemon-required')


def run(arguments, *, timeout=180, cwd=None, expected=0, check='command'):
    # Capture both streams: Docker/Compose and host diagnostics can contain
    # resolved settings. A command failure is identified by its bounded check.
    if READINESS_DEADLINE is not None:
        timeout = min(timeout, max(0.001, READINESS_DEADLINE - time.monotonic()))
    try:
        result = subprocess.run([str(arg) for arg in arguments], cwd=cwd,
                                capture_output=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        raise CheckFailure(check + '-unavailable-or-timed-out') from None
    require(result.returncode == expected, check + '-exit-' + str(result.returncode))
    require(len(result.stdout) <= 2 * 1024 * 1024 and len(result.stderr) <= 2 * 1024 * 1024,
            'command-output-limit')
    return result


def docker(*arguments, **kwargs):
    return run(['docker', *arguments], **kwargs).stdout.decode().strip()


def compose(*arguments, native=False):
    prefix = ['--config', '/review/native-docker'] if native else []
    return docker(*prefix, 'compose', *arguments, cwd=DEPLOYMENT)


def read(path):
    return json.loads(Path(path).read_bytes())


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def write(path, value, mode=0o600):
    path = Path(path)
    path.write_text(json.dumps(value, sort_keys=True, indent=2) + '\n')
    path.chmod(mode)


def wait(fn):
    global READINESS_DEADLINE
    previous = READINESS_DEADLINE
    end = min(time.monotonic() + 90, previous) if previous else time.monotonic() + 90
    READINESS_DEADLINE = end
    try:
        while time.monotonic() < end:
            try:
                if fn():
                    return
            except (OSError, ValueError, CheckFailure):
                pass
            time.sleep(min(1, max(0, end - time.monotonic())))
        raise CheckFailure('readiness-deadline-90s')
    finally:
        READINESS_DEADLINE = previous


def server_info(all_containers=False):
    args = ['ps', '--quiet', *(['--all'] if all_containers else []),
            '--filter', 'label=com.docker.compose.project=' + PROJECT,
            '--filter', 'label=com.docker.compose.service=p2pstream']
    ids = docker(*args, timeout=10).split()
    require(len(ids) == 1, 'exactly-one-server')
    return json.loads(docker('inspect', ids[0], timeout=10))[0]


def owner_bytes(images):
    # An independent, read-only named-volume mount works even when the server is
    # stopped. /data is application-owned mode 0700: UID 0 alone cannot traverse
    # it after dropping capabilities. Grant only DAC_READ_SEARCH, keep the mount
    # read-only, and prove actual directory access before reporting absence.
    # Do not save or print contents. No data writer is introduced.
    result = run(['docker', 'run', '--rm', '--read-only', '--user', '0:0', '--cap-drop', 'ALL',
                '--cap-add', 'DAC_READ_SEARCH', '--security-opt', 'no-new-privileges',
                '--mount', 'type=volume,src=' + VOLUME + ',dst=/data,readonly',
                '--entrypoint', '/bin/sh', images['baseline'], '-c',
                'set -eu\n'
                'test -d /data && cd /data && ls -A -- . >/dev/null || exit 41\n'
                'present=false\n'
                'for entry in .[!.]*; do\n'
                '  if test "$entry" = ' + OWNER + '; then present=true; fi\n'
                'done\n'
                'if test "$present" = true; then\n'
                '  test ! -L ' + OWNER + ' || exit 42\n'
                '  test -f ' + OWNER + ' || exit 43\n'
                '  printf PRESENT:\n'
                '  cat -- ' + OWNER + '\n'
                'else\n'
                '  printf ABSENT\n'
                'fi'], check='owner-observation-data-or-marker-unreadable').stdout
    require(result == b'ABSENT' or result.startswith(b'PRESENT:'), 'invalid-owner-observation')
    return result


def file_snapshot(path):
    if not os.path.lexists(path):
        return None
    item = Path(path)
    metadata = item.lstat()
    require(stat.S_ISREG(metadata.st_mode), 'snapshot-requires-ordinary-file')
    return (item.read_bytes(), metadata.st_mode, metadata.st_uid, metadata.st_gid)


def container_snapshot(info):
    # Docker 28.3.3 GetMountPoints builds the inspect array from a map; array
    # order is not mount configuration. Sort complete canonical objects, keeping
    # every field and duplicate. Never key/deduplicate by destination or name.
    # https://github.com/moby/moby/blob/v28.3.3/container/container_unix.go#L396-L412
    mounts = sorted(json.dumps(mount, sort_keys=True, separators=(',', ':'), allow_nan=False)
                    for mount in info['Mounts'])
    return dict(zip(CONTAINER_FIELDS, (info['Id'], info['Image'], info['State']['Running'],
                                      info['State']['StartedAt'], info['RestartCount'],
                                      copy.deepcopy(info['Config']['Labels']), mounts)))


def snapshot_boundary(images, *, private=False):
    info = server_info(all_containers=True)
    files = [CANONICAL, INPUTS, STATE / 'installation-receipt.json']
    if private:
        files += [STATE / 'config.json', STATE / 'staged-compose.yaml', STATE / 'host.json',
                  STATE / 'runtime-receipt.json', STATE / 'control/token']
    # Keep full bytes in memory for exact equality. Publish only booleans/hashes.
    return {'container': container_snapshot(info),
            'model': compose('config', '--format', 'json'),
            'owner': owner_bytes(images),
            'files': {str(path): file_snapshot(path) for path in files}}


def assert_unchanged(before, images, *, private=False):
    after = snapshot_boundary(images, private=private)
    changed = []
    if after['container'] != before['container']:
        changed += ['container.' + field for field in CONTAINER_FIELDS
                    if after['container'][field] != before['container'][field]]
    # Do not expose file paths, arbitrary label/mount keys, or any values.
    changed += [field for field in ('model', 'owner', 'files') if after[field] != before[field]]
    if changed:
        raise CheckFailure('refusal-changed-' + changed[0].split('.')[0], changed_fields=changed)
    CHECKS.append(STAGE + '-boundary-unchanged')


class Management:
    def __init__(self):
        import urllib.request
        self.opener = urllib.request.build_opener(
            urllib.request.HTTPSHandler(context=ssl._create_unverified_context()),
            urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def rpc(self, name, body=None):
        import urllib.request
        endpoint = 'https://127.0.0.1:18101'
        req = urllib.request.Request(endpoint + '/p2pstream.v1.AgentManagementService/' + name,
                                     data=json.dumps(body or {}).encode(),
                                     headers={'Content-Type': 'application/json', 'Origin': endpoint})
        try:
            with self.opener.open(req, timeout=10) as response:
                return json.load(response)
        except Exception:
            raise CheckFailure('management-rpc-' + name) from None

    def login(self):
        self.rpc('Login', {'username': 'reviewadmin', 'password': PASSWORD})

    def stage(self):
        self.rpc('GenerateManagementTlsRotation')

    def clear(self):
        self.rpc('CancelManagementTlsRotation')
        # Generating a new CA can require a cleanup phase after cancellation.
        rotation = self.rpc('GetManagementTlsRotation')['rotation']
        if rotation.get('phase') in {'MANAGEMENT_TLS_ROTATION_PHASE_CLEANING_UP', 5}:
            self.rpc('FinalizeManagementTlsTrustCleanup', {'force': False})


def preflight():
    return json.loads(docker('exec', server_info()['Id'], '/app/p2pstream', 'server-enrollment-preflight', timeout=15))


def health():
    docker('exec', server_info()['Id'], '/app/p2pstream', 'server-health', timeout=15)
    return True


def fixture_catalog(host, images):
    # Store once so every fresh worker and the Go engine see identical bytes,
    # including publication times and the manifest hash for each sequence.
    now = datetime.now(timezone.utc).replace(microsecond=0)
    stamp = lambda value: value.strftime('%Y-%m-%dT%H:%M:%SZ')
    tar_data = io.BytesIO()
    with tarfile.open(fileobj=tar_data, mode='w') as archive:
        for name in sorted(host.BUNDLE_FILES):
            raw = (BUNDLE / name).read_bytes()
            item = tarfile.TarInfo(name)
            item.size, item.mode, item.mtime = len(raw), 0o644, 0
            archive.addfile(item, io.BytesIO(raw))
    bundle = gzip.compress(tar_data.getvalue(), mtime=0)
    catalog = {}
    for key, number, letter, epoch, minimum in [('baseline', 9000, 'a', 1, 'v0.1.51'),
                                                ('candidate', 9001, 'b', 2, 'v0.1.52')]:
        version, commit = 'v0.1.53-staging.' + str(number), letter * 40
        updater = images['updater' if key == 'baseline' else 'candidate_updater']
        metadata = {'api': 1, 'version': version, 'commit': commit, 'source_schema_min': 17,
                    'source_schema_max': 20, 'target_schema': 20, 'runtime_api_min': 1,
                    'agent_protocol_min': 1, 'agent_protocol_max': 1, 'rollback': 'snapshot'}
        descriptor = {'api': 1, 'version': version, 'commit': commit, 'channel': 'staging',
                      'bundle': 'p2pstream_' + version + '_docker.tar.gz',
                      'updater_images': {'linux/amd64': updater, 'linux/arm64': updater}}
        encode = lambda value: json.dumps(value, separators=(',', ':')).encode()
        files = {'p2pstream_server_update.json': encode(metadata),
                 'p2pstream_install.json': encode(descriptor)}
        assets = dict(files, **{descriptor['bundle']: bundle})
        digest = images[key].split('@')[1]
        # ParseManifest byte-compares against the Go Manifest struct's JSON
        # encoding. Preserve its field order, including the release floor.
        manifest = {'schema_version': 1, 'channel': 'staging', 'version': version, 'commit': commit,
                    'sequence': number,
                    'published_at': stamp(now - timedelta(hours=1)), 'expires_at': stamp(now + timedelta(days=1)),
                    'minimum_safe_version': minimum, 'security_epoch': epoch,
                    'compatibility': {'server': {'min': 'v0.1.51', 'max': 'v0.1.54'},
                                      'protocol': {'min': 1, 'max': 1},
                                      'updater': {'min': 'v0.1.53-staging.9000', 'max': 'v0.1.53-staging.9001'}},
                    'artifacts': [{'os': 'linux', 'arch': 'amd64', 'name': 'fixture-binary',
                                   'size': 1, 'sha256': 'a' * 64}],
                    'oci_images': [{'repository': 'ghcr.io/review/p2pstream', 'digest': digest,
                                    'media_type': 'application/vnd.oci.image.index.v1+json', 'size': 123,
                                    'platforms': [{'os': 'linux', 'arch': 'amd64', 'digest': digest,
                                                   'media_type': 'application/vnd.oci.image.manifest.v1+json', 'size': 123},
                                                  {'os': 'linux', 'arch': 'arm64', 'digest': 'sha256:' + 'c' * 64,
                                                   'media_type': 'application/vnd.oci.image.manifest.v1+json', 'size': 123}]}],
                    'release_assets': [{'name': name, 'size': len(raw), 'sha256': sha(raw)}
                                       for name, raw in sorted(assets.items())]}
        raw = encode(manifest)
        files['p2pstream_agent_update_manifest.json'] = raw
        release = {'version': version, 'commit': commit, 'channel': 'staging', 'image': images[key],
                   'sequence': number, 'security_epoch': epoch, 'minimum_safe_version': minimum,
                   'manifest_sha256': sha(raw), 'expires_at': manifest['expires_at'], 'metadata': metadata}
        catalog[key] = {'manifest': manifest, 'descriptor': descriptor, 'release': release,
                        'files': {name: base64.b64encode(value).decode() for name, value in files.items()}}
    write(ROOT / 'catalog.json', catalog)
    write(ROOT / 'candidate-release.json', catalog['candidate']['release'])
    return catalog


def load_host():
    spec = importlib.util.spec_from_file_location('host', BUNDLE / 'server-updater-host.py')
    host = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(host)
    return host


def install_arguments(catalog, release, installation, variant):
    value = copy.deepcopy(catalog[release])
    if variant:
        manifest = value['manifest']
        if variant == 'sequence':
            manifest['sequence'] -= 1
        elif variant == 'epoch':
            manifest['security_epoch'] -= 1
        elif variant == 'minimum':
            manifest['minimum_safe_version'] = 'v0.1.51'
        elif variant == 'identity':
            manifest['published_at'] = '2026-01-01T00:00:00Z'
        raw = json.dumps(manifest, separators=(',', ':')).encode()
        value['files']['p2pstream_agent_update_manifest.json'] = base64.b64encode(raw).decode()
        value['release']['manifest_sha256'] = sha(raw)
    value['manifest']['_files'] = {name: base64.b64decode(raw) for name, raw in value['files'].items()}
    r = value['release']
    args = argparse.Namespace(bundle=str(BUNDLE), expect_installation=installation,
                              expect_version=r['version'], expect_commit=r['commit'],
                              expect_channel='staging', expect_arch='amd64', repository='review/p2pstream',
                              manifest_sha256=r['manifest_sha256'], deployment_layout='single-file-v1',
                              migrate_deployment=False)
    return args, value


def host_refusal_check(host, error):
    # The host's error body and helper log can contain private command/model
    # data. Preserve only source locations in the exact loaded controller so a
    # CI refusal can be traced without publishing either of those bodies. Keep
    # wrapping/recovery causes, too: their outer site can hide the first failure.
    sites, seen = [], set()
    for _ in range(4):
        if error is None or id(error) in seen:
            break
        seen.add(id(error))
        lines = []
        trace = error.__traceback__
        while trace is not None:
            if trace.tb_frame.f_globals is vars(host):
                lines.append(str(trace.tb_lineno))
            trace = trace.tb_next
        site = 'host-lines-' + '-'.join(lines[-8:]) if lines else 'host-site-unavailable'
        if site not in sites:
            sites.append(site)
        error = error.__cause__ or error.__context__
    return 'unexpected-host-refusal-' + '-cause-'.join(sites)


def worker(args):
    stage('worker-setup')
    host, images = load_host(), read('/review/images.json')
    catalog, installation = read(ROOT / 'catalog.json'), read(ROOT / 'identity.json')['installation']
    setup, release = install_arguments(catalog, args.release, installation, args.variant)
    # The sole successful-operation substitution: source-built catalog metadata.
    host.release_metadata = lambda *unused: (release['manifest'], release['descriptor'],
                                             release['release']['image'], setup.manifest_sha256)
    original_docker, original_command = host.docker, host.Host.command
    original_prepare, original_write = host.Host.prepare_single, host.Host.write_model
    original_canonical, original_owner = file_snapshot(CANONICAL), owner_bytes(images)
    events = []
    fired = False
    refusal_snapshot = None

    def record(event):
        events.append(event)
        CHECKS.append(event)
        write(ROOT / ('trace-' + args.label + '.json'), events)

    def interrupt(point):
        nonlocal fired
        if args.interrupt == point and not fired:
            fired = True
            record('interrupt-' + point)
            raise DeliberateInterruption()

    def observed_docker(*arguments, **kwargs):
        # Preserve production timeouts, with a bounded upper limit per call.
        kwargs['timeout'] = min(kwargs.get('timeout', 180), 180)
        return original_docker(*arguments, **kwargs)

    def observed_command(controller, *arguments):
        if arguments and arguments[0] == 'runtime-block-staged':
            require((STATE / 'host.json').is_file(), 'journal-must-precede-block')
            require('--installation' in arguments and installation in arguments
                    and '--image' in arguments and release['release']['image'] in arguments,
                    'staged-block-must-bind-installation-and-image')
            record('journal-before-block')
        result = original_command(controller, *arguments)
        if arguments and arguments[0] == 'runtime-block-staged':
            owner = owner_bytes(images)
            require(owner.startswith(b'PRESENT:') and json.loads(owner[8:])['allowed'] is False,
                    'staged-block-did-not-close-admission')
            record('runtime-blocked')
        return result

    def observed_write(controller, data):
        transaction = controller.journal.get('canonical_transaction', {})
        publishing = (controller.current_model_path() == CANONICAL and args.action in {'install', 'repair'}
                      and transaction.get('kind') == 'initial' and controller.journal['phase'] == 'activating')
        if publishing:
            raw = data if isinstance(data, bytes) else json.dumps(data).encode()
            model = json.loads(raw)
            publishing = 'p2pstream-server-updater' in model.get('services', {})
        if publishing:
            require((STATE / 'host.json').is_file(), 'publication-without-private-journal')
            require('runtime-blocked' in events, 'publication-before-staged-block')
            require(not server_info(all_containers=True)['State']['Running'], 'publication-before-stop')
            require(sha(raw) == sha((STATE / 'staged-compose.yaml').read_bytes()),
                    'publication-differs-from-private-candidate')
            record('server-stopped-before-publish')
            interrupt('before-publish')
        result = original_write(controller, data)
        if publishing:
            require(stat.S_IMODE(CANONICAL.stat().st_mode) == 0o640, 'canonical-mode-0640')
            require(CANONICAL.read_bytes() == raw, 'canonical-publication-bytes-changed')
            require(raw.endswith(b'\n') and b'\n  "' in raw, 'canonical-model-not-readable-json')
            record('published')
            interrupt('after-publish')
        return result

    # Block only after all private preparation has completed. Its runtime
    # readiness response still comes from the real server observation socket.
    def observed_prepare(controller):
        nonlocal refusal_snapshot
        stage('private-preparation')
        result = original_prepare(controller)
        require(file_snapshot(CANONICAL) == original_canonical,
                'go-enrollment-changed-standalone-canonical')
        staged = STATE / 'staged-compose.yaml'
        require(staged.is_file() and stat.S_IMODE(staged.stat().st_mode) == 0o600,
                'private-candidate-mode-0600')
        config = read(STATE / 'config.json')
        require(config['deployment_layout'] == 'single-file-v1'
                and config['deployment_file'] == str(CANONICAL), 'explicit-canonical-layout')
        require(owner_bytes(images) == original_owner, 'go-enrollment-changed-owner')
        record('private-prepared')
        if args.block_after_prepare:
            stage('after-private-preparation-blocker')
            manager = Management()
            manager.login()
            manager.stage()
            require(any(item['code'] == 'certificate_rotation_active' for item in preflight()['blockers']),
                    'real-after-prepare-blocker-not-observed')
            refusal_snapshot = snapshot_boundary(images, private=True)
            record('real-blocker-after-private-prepare')
        return result

    host.docker, host.Host.command = observed_docker, observed_command
    host.Host.prepare_single, host.Host.write_model = observed_prepare, observed_write
    original_wait = host.Host.wait_original
    def observed_wait(controller):
        result = original_wait(controller)
        if args.action == 'remove':
            interrupt('after-remove-deploy')
        return result
    host.Host.wait_original = observed_wait
    os.chdir(DEPLOYMENT)
    stage('host-' + args.action)
    try:
        if args.action == 'install':
            host.install(setup, [])
        else:
            host.manage(argparse.Namespace(action=args.action))
    except host.Failure as error:
        expected = {'blocker': 'Enrollment blocked:', 'history': 'Missing retained configuration',
                    'floor': 'retained enrollment floor'}.get(args.refusal)
        if not expected or expected not in str(error):
            raise CheckFailure(host_refusal_check(host, error)) from None
        if args.block_after_prepare:
            stage('after-private-preparation-boundary')
            require(refusal_snapshot is not None, 'after-prepare-checkpoint-not-reached')
            assert_unchanged(refusal_snapshot, images, private=True)
            require('published' not in events and 'runtime-blocked' not in events,
                    'blocker-crossed-mutation-boundary')
        record('expected-' + args.refusal + '-refusal')
        return 0
    require(not args.refusal, 'expected-refusal-did-not-happen')
    require(not args.interrupt, 'interruption-checkpoint-not-reached')
    record('completed')
    return 0


def fresh(action, *, release='baseline', label=None, refusal='', variant='', interrupt='', block=False):
    label = label or action
    arguments = [sys.executable, '-I', __file__, '--worker', action, '--release', release,
                 '--label', label, '--refusal', refusal, '--variant', variant, '--interrupt', interrupt]
    if block:
        arguments.append('--block-after-prepare')
    try:
        run(arguments, timeout=600, expected=INTERRUPTED if interrupt else 0, check='worker-' + label)
    except CheckFailure:
        report = ROOT / ('failure-' + label + '.json')
        if report.is_file():
            failure = read(report)
            raise CheckFailure('worker-' + label + '-' + failure['check'],
                               changed_fields=failure.get('changed_fields', ()),
                               worker_stage=failure.get('stage'),
                               worker_checks=failure.get('checks', ())) from None
        raise
    CHECKS.append('worker-' + label + '-expected-exit')


def helper(images, test, *, update=False):
    config = read(STATE / 'config.json')
    args = ['run', '--rm', '--read-only', '--tmpfs', '/tmp', '--user', '0:0',
            '--label', 'p2pstream.server-update.executor=' + config['instance_id'],
            '--mount', 'type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock',
            '--mount', 'type=bind,src=' + str(STATE) + ',dst=' + str(STATE),
            '--mount', 'type=bind,src=' + str(DEPLOYMENT) + ',dst=' + str(DEPLOYMENT),
            '--mount', 'type=bind,src=/review,dst=/review',
            '--mount', 'type=volume,src=' + VOLUME + ',dst=/server-data',
            '--env', 'REVIEW_CONFIG=' + str(STATE / 'config.json')]
    if update:
        args += ['--env', 'REVIEW_HOST_RELEASE=' + str(ROOT / 'candidate-release.json')]
    args += [images['helper'], '/app/server-update-test', '-test.timeout=13m', '-test.run=^' + test + '$']
    docker(*args, timeout=800 if update else 90)


def assert_binding(images, expected):
    wait(health)
    config, info = read(STATE / 'config.json'), server_info()
    require(info['Config']['Image'] == images[expected], 'runtime-image-changed')
    require(info['Config']['Labels']['p2pstream.server-update.instance'] == config['instance_id'],
            'runtime-private-instance-missing')
    require(any(mount['Destination'] == '/run/p2pstream-server-update' and not mount['RW']
                for mount in info['Mounts']), 'runtime-private-binding-missing')
    require(not any(mount['Destination'] == '/var/run/docker.sock' for mount in info['Mounts']),
            'server-has-docker-socket')
    require(info['HostConfig']['ReadonlyRootfs'], 'server-root-no-longer-read-only')
    require(docker('exec', info['Id'], '/app/p2pstream', 'server-installation-identity')
            == read(ROOT / 'identity.json')['installation'], 'installation-identity-changed')
    require(sha(docker('exec', info['Id'], 'cat', '/data/certs/management/ca.crt.pem').encode())
            == read(ROOT / 'identity.json')['ca_sha256'], 'management-ca-changed')
    def authenticated_status():
        run([LAUNCHER, 'status'], timeout=30, cwd=DEPLOYMENT, check='stable-launcher-status')
        return True
    wait(authenticated_status)


def assert_native_recreate(images, expected):
    before = server_info()['Id']
    config = (STATE / 'config.json').read_bytes()
    result = run(['docker', '--config', '/review/native-docker', 'compose', 'up', '-d', '--force-recreate'],
                 cwd=DEPLOYMENT)
    require(b'orphan' not in (result.stdout + result.stderr).lower(), 'native-compose-orphan-warning')
    require(server_info()['Id'] != before, 'native-compose-did-not-recreate')
    assert_binding(images, expected)
    require((STATE / 'config.json').read_bytes() == config, 'native-compose-changed-private-config')
    helper(images, 'TestDockerReviewRuntimeReceipt')
    require(read(STATE / 'native-runtime-receipt.json')['runtime_receipt_verified_directly'],
            'native-runtime-receipt-not-verified')


def main():
    CHECKS.clear()
    stage('fixture-setup')
    isolated()
    ROOT.mkdir(mode=0o700)
    images, host = read('/review/images.json'), load_host()
    require(docker('info', '--format', '{{.Architecture}}') in {'x86_64', 'amd64'},
            'catalog-fixture-requires-amd64')
    # The preceding legacy rehearsal finishes removed. Keep its private state
    # intact while giving this independent installation the production defaults.
    if STATE.exists():
        require(read(STATE / 'host.json')['phase'] == 'removed', 'legacy-fixture-must-finish-removed')
        STATE.rename(ROOT / 'completed-legacy-state')
    require(not CANONICAL.exists() and not INPUTS.exists(), 'canonical-fixture-requires-clean-paths')
    DEPLOYMENT.mkdir(parents=True, mode=0o2750)
    DEPLOYMENT.chmod(0o2750)
    model = {'name': PROJECT, 'services': {'p2pstream': {
        'image': images['baseline'], 'restart': 'unless-stopped', 'read_only': True, 'tmpfs': ['/tmp'],
        'environment': {'CONFIG_DIR': '/data', 'MANAGEMENT_PORT': '8081', 'MANAGEMENT_SETUP_TOKEN': SETUP_TOKEN},
        'ports': [{'target': 8081, 'published': '18101', 'host_ip': '127.0.0.1', 'protocol': 'tcp'}],
        'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}]}},
        'volumes': {'data': {'name': VOLUME}}}
    write(CANONICAL, model, 0o640)
    compose('up', '-d')
    wait(health)
    info = server_info()
    installation = docker('exec', info['Id'], '/app/p2pstream', 'server-installation-identity')
    ca = docker('exec', info['Id'], 'cat', '/data/certs/management/ca.crt.pem')
    write(ROOT / 'identity.json', {'installation': installation, 'ca_sha256': sha(ca.encode())})
    original = file_snapshot(CANONICAL)
    catalog = fixture_catalog(host, images)
    checks = CHECKS
    manager = Management()
    manager.rpc('SetupAdmin', {'username': 'reviewadmin', 'password': PASSWORD, 'setupToken': SETUP_TOKEN})
    manager.login()
    for key in ('baseline', 'candidate'):
        stage('capabilities-' + key)
        old = json.loads(docker('run', '--rm', '--entrypoint', '/app/p2pstream', images[key],
                                'server-deployment-capabilities'))
        new = json.loads(docker('run', '--rm', '--entrypoint', '/app/p2pstream', images[key],
                                'server-enrollment-capabilities'))
        require(set(old) == {'runtime_guard', 'version', 'commit'}, 'legacy-capability-shape-changed')
        require(new == dict(old, enrollment_preflight=1, canonical_compose=1)
                and old['version'] == catalog[key]['release']['version']
                and old['commit'] == catalog[key]['release']['commit'], 'new-capability-mismatch')
    checks.append('old-and-new-capability-contracts')
    stage('initial-blocker')
    manager.stage()
    require(not preflight()['ready'], 'initial-real-blocker-not-observed')
    before = snapshot_boundary(images)
    fresh('install', label='initial-blocker', refusal='blocker')
    assert_unchanged(before, images)
    require(not STATE.exists(), 'initial-blocker-created-private-state')
    manager.clear()
    wait(lambda: preflight()['ready'])
    checks.append('real-blocker-before-staging-exact-no-mutation')
    stage('prepared-blocker')
    fresh('install', label='prepared-blocker', refusal='blocker', block=True)
    manager.clear()
    wait(lambda: preflight()['ready'])
    checks.append('real-blocker-after-private-preparation-exact-no-mutation')
    stage('before-publication')
    fresh('repair', label='before-publication', interrupt='before-publish')
    require(file_snapshot(CANONICAL) == original, 'interrupted-before-publication-changed-canonical')
    stage('after-publication')
    fresh('repair', label='after-publication', interrupt='after-publish')
    stage('resume-publication')
    fresh('repair', label='resume-publication')
    assert_binding(images, 'baseline')
    trace = read(ROOT / 'trace-after-publication.json')
    require(trace.index('journal-before-block') < trace.index('runtime-blocked')
            < trace.index('server-stopped-before-publish') < trace.index('published'),
            'journal-block-stop-publish-order')
    checks.append('fresh-process-publication-interruptions-and-order')
    stage('healthy-setup-snapshot')
    before = snapshot_boundary(images, private=True)
    executor = compose('ps', '-q', 'p2pstream-server-updater')
    executor_before = json.loads(docker('inspect', executor))[0]
    for number in range(2):
        stage('idempotent-' + str(number))
        fresh('install', label='idempotent-' + str(number))
        assert_unchanged(before, images, private=True)
        current = json.loads(docker('inspect', executor))[0]
        require(current['State']['StartedAt'] == executor_before['State']['StartedAt']
                and current['RestartCount'] == executor_before['RestartCount'], 'healthy-setup-restarted-executor')
    checks.append('repeated-healthy-setup-no-restart')
    stage('healthy-stopped-executor')
    # A healthy host journal with a deliberately stopped executor is an
    # executor-only repair, not an unfinished publication or server rollback.
    before = snapshot_boundary(images, private=True)
    compose('stop', 'p2pstream-server-updater')
    require(not json.loads(docker('inspect', executor))[0]['State']['Running'],
            'executor-stop-checkpoint-not-reached')
    fresh('repair', label='healthy-stopped-executor')
    assert_unchanged(before, images, private=True)
    require(json.loads(docker('inspect', compose('ps', '-q', 'p2pstream-server-updater')))[0]['State']['Running'],
            'healthy-repair-did-not-restart-executor')
    checks.append('healthy-stopped-executor-repair-preserves-server-model-owner')
    stage('native-recreate-baseline')
    assert_native_recreate(images, 'baseline')
    checks.append('native-default-compose-baseline-binding-no-orphan-and-direct-receipt')
    stage('engine-update')
    compose('stop', 'p2pstream-server-updater')
    helper(images, 'TestDockerReviewHostUpdate', update=True)
    result = read(STATE / 'host-update-result.json')
    expected_floor = {key: catalog['candidate']['release'][key]
                      for key in ('sequence', 'security_epoch', 'minimum_safe_version', 'manifest_sha256')}
    require(result['phase'] == 'succeeded' and result['snapshot_verified']
            and result['floor'] == expected_floor, 'actual-engine-update-floor-mismatch')
    compose('up', '-d', 'p2pstream-server-updater')
    wait(lambda: (STATE / 'control/control.sock').exists())
    stage('native-recreate-candidate')
    assert_native_recreate(images, 'candidate')
    checks.append('actual-engine-update-and-native-recreation-retain-candidate')
    stage('apply-editable-inputs')
    config = (STATE / 'config.json').read_bytes()
    inputs = read(INPUTS)
    require(not inputs.get('include') and set(inputs['services']) == {'p2pstream'},
            'editable-inputs-not-fully-resolved-standalone-model')
    inputs['services']['p2pstream']['environment']['QUALIFICATION_SETTING'] = 'applied-after-update'
    write(INPUTS, inputs, 0o640)
    fresh('apply', release='candidate')
    assert_binding(images, 'candidate')
    require((STATE / 'config.json').read_bytes() == config, 'apply-changed-private-config')
    require('QUALIFICATION_SETTING=applied-after-update' in server_info()['Config']['Env'],
            'apply-did-not-use-editable-inputs')
    checks.append('apply-editable-inputs-retains-updated-image')
    stage('interrupt-remove')
    generation_before = next(value.split('=', 1)[1] for value in server_info()['Config']['Env']
                             if value.startswith('SERVER_UPDATE_GENERATION='))
    inputs_before_remove = INPUTS.read_bytes()
    fresh('remove', release='candidate', label='interrupt-remove', interrupt='after-remove-deploy')
    require(read(STATE / 'host.json')['phase'] == 'removing', 'interrupted-remove-lost-phase')
    stage('rollback-interrupted-remove')
    fresh('repair', release='candidate', label='rollback-interrupted-remove')
    assert_binding(images, 'candidate')
    generation_after = next(value.split('=', 1)[1] for value in server_info()['Config']['Env']
                            if value.startswith('SERVER_UPDATE_GENERATION='))
    owner = owner_bytes(images)
    require(generation_after != generation_before and owner.startswith(b'PRESENT:')
            and json.loads(owner[8:])['generation'] == generation_after
            and json.loads(owner[8:])['allowed'] is True, 'rollback-did-not-authorize-fresh-generation')
    require(read(STATE / 'host.json')['phase'] == 'healthy' and INPUTS.read_bytes() == inputs_before_remove,
            'rollback-phase-or-editable-inputs-changed')
    require(CANONICAL.read_bytes().endswith(b'\n') and b'\n  "' in CANONICAL.read_bytes(),
            'rollback-model-not-readable-json')
    checks.append('interrupted-removal-rollback-fresh-generation-and-readable-model')
    stage('complete-remove')
    fresh('remove', release='candidate', label='complete-remove')
    require(read(STATE / 'host.json')['phase'] == 'removed', 'remove-not-committed')
    require(server_info()['Config']['Image'] == images['candidate'], 'remove-downgraded-image')
    require('QUALIFICATION_SETTING=applied-after-update' in server_info()['Config']['Env'],
            'remove-dropped-applied-settings')
    require(not any(value.startswith('SERVER_UPDATE_') for value in server_info()['Config']['Env']),
            'remove-retained-private-binding')
    receipt = read(STATE / 'installation-receipt.json')
    require(receipt['floor'] == expected_floor, 'remove-dropped-release-floor')
    # The protected model owns software updates independently of editable
    # settings. Explicitly reconcile that one stale input before fresh setup;
    # never ask enrollment to silently adopt a different resolved source model.
    inputs = read(INPUTS)
    inputs['services']['p2pstream']['image'] = images['candidate']
    write(INPUTS, inputs, 0o640)
    require(json.loads(docker('compose', '-f', str(INPUTS), 'config', '--format', 'json', cwd=DEPLOYMENT))
            == json.loads(compose('config', '--format', 'json')), 'reenrollment-input-reconciliation-mismatch')
    for variant in ('sequence', 'epoch', 'minimum', 'identity'):
        stage('floor-' + variant)
        before = snapshot_boundary(images, private=True)
        fresh('install', release='candidate', label='floor-' + variant, refusal='floor', variant=variant)
        assert_unchanged(before, images, private=True)
        checks.append('floor-' + variant + '-exact-no-mutation')
    checks.append('remove-retains-floor-and-rejects-four-floor-regressions')
    stage('reenroll')
    fresh('install', release='candidate', label='reenroll')
    assert_binding(images, 'candidate')
    require(read(STATE / 'config.json')['bootstrap_floor'] == expected_floor,
            'reenrollment-reset-bootstrap-floor')
    require(read(STATE / 'installation-receipt.json')['floor'] == expected_floor,
            'reenrollment-reset-retained-floor')
    checks.append('reenrollment-preserves-installation-and-candidate-floor')
    stage('remove-before-lost-history')
    fresh('remove', release='candidate', label='remove-before-lost-history')
    history = next((STATE / 'history').iterdir())
    missing = history / 'config.json'
    saved, saved_mode = missing.read_bytes(), stat.S_IMODE(missing.stat().st_mode)
    missing.unlink()  # Deliberate loss of this run's disposable private history.
    try:
        stage('lost-history')
        before = snapshot_boundary(images, private=True)
        fresh('install', release='candidate', label='lost-history', refusal='history')
        assert_unchanged(before, images, private=True)
        require(not missing.exists(), 'lost-history-was-silently-reconstructed')
    finally:
        missing.write_bytes(saved)
        missing.chmod(saved_mode)
    checks.append('lost-private-history-diagnosed-without-current-image-adoption')
    # Cleanup is explicit and precedes the sanitized success summary.
    stage('cleanup')
    compose('stop', 'p2pstream')
    write(ROOT / 'summary.json', {'result': 'passed', 'qualification': 'source-built-real-docker',
                                 'published_pair_gate': 'pending-future-feature-bearing-releases',
                                 'checks': checks, 'candidate_image': images['candidate'],
                                 'candidate_manifest_sha256': expected_floor['manifest_sha256'],
                                 'limitations': ['synthetic-catalog', 'same-source-two-release-identities',
                                                 'one-shot-test-interruptions', 'no-host-reboot']})
    print('PASS source-built canonical lifecycle: ' + ', '.join(checks), flush=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--worker', dest='action', choices=['install', 'repair', 'apply', 'remove'])
    parser.add_argument('--release', choices=['baseline', 'candidate'], default='baseline')
    parser.add_argument('--label', default='worker')
    parser.add_argument('--refusal', choices=['', 'blocker', 'floor', 'history'], default='')
    parser.add_argument('--variant', choices=['', 'sequence', 'epoch', 'minimum', 'identity'], default='')
    parser.add_argument('--interrupt', choices=['', 'before-publish', 'after-publish', 'after-remove-deploy'], default='')
    parser.add_argument('--block-after-prepare', action='store_true')
    options = parser.parse_args()
    try:
        isolated()
        if options.action:
            sys.exit(worker(options))
        main()
    except DeliberateInterruption:
        sys.exit(INTERRUPTED)
    except Exception as error:
        # No traceback or exception body from Docker, the controller or RPCs can
        # leak credentials/model/database data into the wrapper's public log.
        report = failure_report(error)
        code = report['check']
        if ROOT.exists():
            if options.action:
                write(ROOT / ('failure-' + options.label + '.json'), report)
            else:
                write(ROOT / 'summary.json', report)
        print('FAIL source-built canonical lifecycle: ' + code + ' stage=' + report['stage'], file=sys.stderr)
        sys.exit(1)

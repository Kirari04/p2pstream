#!/usr/bin/env python3
"""Production host lifecycle against the disposable daemon. Catalog metadata,
one-shot failures, and an explicitly labeled old-repair sequence are fixtures;
Docker, private auth, runtime ownership, IDs and recovery are real. Never run
this directly on a deployment host.
"""
import argparse
import base64
import copy
import errno
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tarfile
import time
import urllib.request
import uuid

assert os.geteuid() == 0 and socket.gethostbyname('ghcr.io') == '127.0.0.1' and Path('/review/ISOLATED_TEST_VM').exists(), 'Disposable review daemon required'
spec = importlib.util.spec_from_file_location('host', '/review/install-bundle/server-updater-host.py')
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)
images = json.loads(Path('/review/images.json').read_text())

WORKER_CONTEXT = Path('/review/installation-worker-context.json')
WORKER_RESULT = Path('/review/installation-worker-result.json')
INTERRUPTED = 75
REPLACEMENT_FAULT = 'fixture owned replacement creation failed'
RECORD_FAULT = 'fixture first record-runtime failure'


class DeliberateInterruption(BaseException):
    pass


def semantic_compose_result(result):
    """Change serialization only, after a successful actual Compose command."""
    assert result.returncode == 0
    model = json.loads(result.stdout)
    raw = (json.dumps(model, sort_keys=True, indent=3) + '\n').encode()
    assert json.loads(raw) == model
    return subprocess.CompletedProcess(result.args, result.returncode, raw, result.stderr)


def legacy_worker(arguments):
    """Fresh public API calls; only catalog bytes and one-shot faults vary."""
    parser = argparse.ArgumentParser()
    parser.add_argument('action', choices=['install', 'apply', 'repair', 'rollback', 'cancel-enrollment'])
    parser.add_argument('--fault', choices=['', 'replacement-create', 'record-runtime'], default='')
    parser.add_argument('--interrupt', choices=['', 'after-owner-unlink', 'after-runtime-detach'], default='')
    parser.add_argument('--semantic-compose', action='store_true')
    options = parser.parse_args(arguments)
    context = json.loads(WORKER_CONTEXT.read_bytes())
    setup = argparse.Namespace(**context['args'])
    manifest = context['manifest']
    manifest['_files'] = {name: base64.b64decode(raw) for name, raw in context['files'].items()}
    host.release_metadata = lambda *unused: (manifest, context['descriptor'], context['image'],
                                             setup.manifest_sha256)
    os.chdir(context['source'])
    events, fired, interrupted = [], False, False
    checkpoint = None
    original_docker, original_compose = host.docker, host.Host.compose
    original_record = host.Host.record_runtime
    original_runtime = host.Host.runtime
    original_clear = host.Host.clear_blocked_initial_owner

    def docker_observed(*args, **kwargs):
        kwargs['timeout'] = min(kwargs.get('timeout', 180), 180)
        result = original_docker(*args, **kwargs)
        if options.semantic_compose and args[:1] == ('compose',) and 'config' in args and '--format' in args and 'json' in args:
            # Re-encode only a SUCCESSFUL real Compose response. Preserve the
            # complete JSON object; this must never manufacture runtime proof.
            rendered = semantic_compose_result(result)
            if rendered.stdout != result.stdout:
                events.append('actual-compose-output-semantic-rerender')
            result = rendered
        return result

    def compose_observed(controller, *args, **kwargs):
        nonlocal fired
        if options.fault == 'replacement-create' and not fired and args[:1] == ('up',) and '--no-start' in args and args[-1] == 'p2pstream':
            ids = original_compose(controller, 'ps', '--all', '--quiet', 'p2pstream').stdout.decode().split()
            assert len(ids) == 1, 'replacement-fault-existing-owned-server'
            info = json.loads(original_docker('inspect', ids[0], timeout=15).stdout)[0]
            assert not info['State']['Running'] and info['Image'] == controller.journal['before_image_id'], 'replacement-fault-stopped-current-image'
            labels = info['Config']['Labels']
            assert labels['com.docker.compose.project'] == controller.config['project'] and labels['com.docker.compose.service'] == 'p2pstream', 'replacement-fault-owned-project-service'
            before = (host.STATE / 'before-compose.json').read_bytes()
            assert hashlib.sha256(before).hexdigest() == controller.journal['before_sha256'], 'replacement-fault-durable-snapshot'
            events.append('source-before-failure-' + hashlib.sha256(before).hexdigest())
            original_docker('rm', ids[0], timeout=30)
            assert not original_compose(controller, 'ps', '--all', '--quiet', 'p2pstream').stdout.strip(), 'replacement-fault-real-removal'
            events.append('actual-owned-server-removed')
            fired = True
            raise host.Failure(REPLACEMENT_FAULT)
        return original_compose(controller, *args, **kwargs)

    def record_observed(controller):
        nonlocal fired
        if options.fault == 'record-runtime' and not fired:
            fired = True
            events.append('first-record-runtime-fault')
            raise host.Failure(RECORD_FAULT)
        return original_record(controller)

    def clear_observed(controller, directory, snapshot, lease):
        nonlocal interrupted
        result = original_clear(controller, directory, snapshot, lease)
        if options.interrupt == 'after-owner-unlink' and not interrupted:
            assert snapshot is not None, 'owner-unlink-fixture-already-absent'
            try:
                os.stat('.p2pstream-runtime-owner.json', dir_fd=directory, follow_symlinks=False)
            except FileNotFoundError:
                pass
            else:
                raise AssertionError('actual-owner-unlink-did-not-happen')
            events.append('actual-owner-unlinked-and-synced')
            interrupted = True
            raise DeliberateInterruption()
        return result

    def runtime_observed(controller, action):
        nonlocal interrupted, checkpoint
        result = original_runtime(controller, action)
        if options.interrupt == 'after-runtime-detach' and action == 'detach' and not interrupted:
            assert controller.journal['phase'] == 'rolling_back', 'detach-interruption-not-rollback'
            ids = original_compose(controller, 'ps', '--all', '--quiet', 'p2pstream').stdout.decode().split()
            assert len(ids) == 1, 'detach-target-not-created'
            target = json.loads(original_docker('inspect', ids[0], timeout=15).stdout)[0]
            assert not target['State']['Running'] and target['Image'] == controller.journal['before_image_id'], 'detach-target-not-stopped-current-image'
            assert target['State']['Status'] == 'created', 'detach-target-was-not-newly-created'
            assert not any(value.startswith('SERVER_UPDATE_') for value in target['Config']['Env']), 'detach-target-retains-private-environment'
            assert not any(m['Destination'] == '/run/p2pstream-server-update' for m in target['Mounts']), 'detach-target-retains-control-mount'
            volume = json.loads(original_docker('volume', 'inspect', controller.config['data_volume'], timeout=15).stdout)
            assert len(volume) == 1 and volume[0]['Driver'] == 'local', 'detach-volume-not-local'
            assert not os.path.lexists(Path(volume[0]['Mountpoint']) / '.p2pstream-runtime-owner.json'), 'runtime-detach-did-not-remove-owner'
            checkpoint = {'id': target['Id'], 'image': target['Image'], 'mounts': target['Mounts']}
            events.append('actual-created-standalone-target-detached-before-start')
            interrupted = True
            raise DeliberateInterruption()
        return result

    host.docker, host.Host.compose = docker_observed, compose_observed
    host.Host.record_runtime, host.Host.clear_blocked_initial_owner = record_observed, clear_observed
    host.Host.runtime = runtime_observed
    result, code = 'completed', 0
    try:
        if options.action == 'install':
            host.install(setup, context['options'])
        elif options.action == 'cancel-enrollment':
            # Exercise the production CLI parser and its existing cancellation
            # entry point, with a frozen, verified synthetic release catalog.
            sys.argv = ['server-updater-host.py', 'cancel-enrollment', '--upgraded-deployment']
            for key in ('expect_installation', 'expect_version', 'expect_commit', 'expect_channel',
                        'expect_arch', 'manifest_sha256', 'repository', 'bundle'):
                sys.argv += ['--' + key.replace('_', '-'), str(getattr(setup, key))]
            host.main()
        else:
            host.manage(argparse.Namespace(action=options.action))
        assert not options.fault and not options.interrupt, 'one-shot-boundary-not-reached'
        if options.semantic_compose:
            assert 'actual-compose-output-semantic-rerender' in events, 'semantic-compose-not-observed'
    except DeliberateInterruption:
        assert interrupted, 'interruption-boundary-not-reached'
        result, code = 'interrupted', INTERRUPTED
    except host.Failure as error:
        expected = REPLACEMENT_FAULT if options.fault == 'replacement-create' else RECORD_FAULT
        if not options.fault or not fired or expected not in str(error):
            raise
        result = 'expected-' + options.fault + '-failure'
    finally:
        host.docker, host.Host.compose = original_docker, original_compose
        host.Host.record_runtime, host.Host.clear_blocked_initial_owner = original_record, original_clear
        host.Host.runtime = original_runtime
        WORKER_RESULT.write_text(json.dumps({'result': result, 'events': events, 'checkpoint': checkpoint}, indent=2))
        WORKER_RESULT.chmod(0o600)
    return code


def fresh_legacy(action, *, fault='', interrupt='', semantic=False):
    WORKER_RESULT.unlink(missing_ok=True)
    arguments = [sys.executable, '-I', __file__, '--legacy-worker', action,
                 '--fault', fault, '--interrupt', interrupt]
    if semantic:
        arguments.append('--semantic-compose')
    result = subprocess.run(arguments, capture_output=True, timeout=600)
    # Keep full failure diagnostics only in the guest's private packet. The
    # public failure below contains fixed stage/status, never controller text.
    log = WORKER_RESULT.with_name('installation-worker-' + action + '.log')
    log.write_bytes(result.stdout + result.stderr)
    log.chmod(0o600)
    assert result.returncode == (INTERRUPTED if interrupt else 0), 'fresh-legacy-' + action + '-exit-' + str(result.returncode)
    report = json.loads(WORKER_RESULT.read_bytes())
    assert report['result'] == ('interrupted' if interrupt else 'expected-' + fault + '-failure' if fault else 'completed'), 'fresh-legacy-unexpected-result'
    return report


if sys.argv[1:2] == ['--legacy-worker']:
    try:
        sys.exit(legacy_worker(sys.argv[2:]))
    except Exception as error:
        # Never print Docker/controller output or its potentially private body.
        trace, lines = error.__traceback__, []
        while trace is not None:
            if trace.tb_frame.f_globals is vars(host):
                lines.append(str(trace.tb_lineno))
            trace = trace.tb_next
        print('FAIL legacy worker: ' + type(error).__name__ + '-host-lines-' + '-'.join(lines[-8:]), file=sys.stderr)
        sys.exit(1)

source = Path('/review/installation-source')
source.mkdir(mode=0o700)
project = 'review-release-install'
base = source / 'compose.json'
override = source / 'production.json'
env = source / '.env'
env.write_text('PUBLIC_URL=https://localhost:18091\n')
env.chmod(0o600)
model = {'services': {'p2pstream': {'image': images['baseline'], 'container_name': 'p2pstream', 'restart': 'unless-stopped', 'read_only': True, 'tmpfs': ['/tmp:rw,noexec,nosuid,size=16m'], 'environment': {'CONFIG_DIR': '/data', 'MANAGEMENT_PORT': '8081'}, 'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}]}, 'observer': {'image': images['baseline'], 'command': ['/bin/sh', '-c', 'sleep infinity'], 'volumes': [{'type': 'volume', 'source': 'data', 'target': '/read-only-data', 'read_only': True}]}}, 'volumes': {'data': {'name': project + '-data'}}}
base.write_text(json.dumps(model))
extra = {'services': {'p2pstream': {'environment': {'MANAGEMENT_PUBLIC_URL': '${PUBLIC_URL}', 'SECRET': 'literal-$$HOME-$${SECRET}-$$$$'}, 'ports': [{'target': 8081, 'published': '18091', 'host_ip': '127.0.0.1'}]}}}
override.write_text(json.dumps(extra))
options = ['-p', project, '-f', str(base), '-f', str(override), '--env-file', str(env)]

def docker(*args):
    return subprocess.check_output(['docker', *args], text=True, timeout=180).strip()

def compose(*args):
    return docker('compose', *options, *args)

def info():
    return json.loads(docker('inspect', compose('ps', '-q', 'p2pstream')))[0]

alias_evidence = []
direct_receipt_failures = []

def assert_runtime_aliases(stage, *, receipt=False):
    actual = info()
    networks = actual['NetworkSettings']['Networks']
    assert set(networks) == {project + '_default'}, networks
    aliases = {name: network['Aliases'] for name, network in networks.items()}
    # Stock Compose duplicates this name because container_name equals service.
    # Assert the real daemon behavior so the regression cannot become vacuous.
    assert all(values.count('p2pstream') == 2 for values in aliases.values()), aliases
    assert all(set(values) == {'p2pstream'} for values in aliases.values()), aliases
    evidence = {'stage': stage, 'docker_aliases': aliases}
    if receipt:
        saved = host.read_json(host.STATE / 'runtime-receipt.json')
        recorded = {name: network['Aliases'] for name, network in saved['fingerprint']['Networks'].items()}
        assert recorded == {name: ['p2pstream'] for name in networks}, recorded
        assert saved['deployment_sha256'] == hashlib.sha256(Path(host.read_json(host.STATE / 'config.json')['deployment_file']).read_bytes()).hexdigest()
        evidence['receipt_aliases'] = recorded
    alias_evidence.append(evidence)
    Path('/review/installation-alias-evidence.json').write_text(json.dumps(alias_evidence, indent=2))

def wait(fn, seconds=90):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        try:
            return fn()
        except (OSError, host.Failure, subprocess.CalledProcessError):
            time.sleep(1)
    raise AssertionError('Real Docker fixture did not become ready')

compose('up', '-d')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
installation = docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity')
# Verify the public CLI against the actual production server's Linux PID-1 peer.
# No injected verifier or forged responder is involved in this Docker check.
preflight_before = info()
preflight = json.loads(docker('exec', preflight_before['Id'], '/app/p2pstream', 'server-enrollment-preflight'))
assert preflight['api'] == 1 and preflight['installation_id'] == installation
assert preflight['ready'] is True and preflight['blockers'] == []
preflight_after = info()
assert (preflight_after['Id'], preflight_after['Image'], preflight_after['State']['StartedAt'], preflight_after['RestartCount']) == (preflight_before['Id'], preflight_before['Image'], preflight_before['State']['StartedAt'], preflight_before['RestartCount'])
docker('exec', preflight_before['Id'], '/bin/sh', '-c', 'test ! -e /data/.p2pstream-runtime-owner.json')
observer = compose('ps', '-q', 'observer')
initial_container = info()['Id']
assert_runtime_aliases('original-compose')
initial_secret = next(v for v in info()['Config']['Env'] if v.startswith('SECRET='))

# The fixture binds the exact real registry image/index. The only nonpublished
# artifacts are the catalog and its small installation descriptor.
def fixture(version, commit, server_ref, updater_ref):
    metadata = {'api': 1, 'version': version, 'commit': commit, 'source_schema_min': 17, 'source_schema_max': 20, 'target_schema': 20, 'runtime_api_min': 1, 'agent_protocol_min': 1, 'agent_protocol_max': 1, 'rollback': 'snapshot'}
    desc = {'api': 1, 'version': version, 'commit': commit, 'channel': 'staging', 'bundle': 'p2pstream_' + version + '_docker.tar.gz', 'updater_images': {'linux/amd64': updater_ref, 'linux/arm64': updater_ref}}
    metadata_raw = json.dumps(metadata, separators=(',', ':')).encode()
    desc_raw = json.dumps(desc, separators=(',', ':')).encode()
    bundle_buffer = io.BytesIO()
    with tarfile.open(fileobj=bundle_buffer, mode='w:gz') as archive:
        for name in sorted(host.BUNDLE_FILES):
            archive.add('/review/install-bundle/' + name, arcname=name)
    assets = [{'name': name, 'size': len(raw), 'sha256': hashlib.sha256(raw).hexdigest()} for name, raw in sorted({'p2pstream_server_update.json': metadata_raw, 'p2pstream_install.json': desc_raw, desc['bundle']: bundle_buffer.getvalue()}.items())]
    # Production index is verified by the surrounding release pipeline. This
    # isolated catalog binds the real amd64 platform manifest used by the daemon;
    # its arm64 descriptor is a metadata stub, never executed in this rehearsal.
    manifest = {'schema_version': 1, 'channel': 'staging', 'version': version, 'commit': commit, 'sequence': 9000 if commit.startswith('a') else 9001, 'published_at': (datetime.now(timezone.utc) - timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M:%SZ'), 'expires_at': (datetime.now(timezone.utc) + timedelta(days=1)).strftime('%Y-%m-%dT%H:%M:%SZ'), 'minimum_safe_version': 'v0.1.52', 'security_epoch': 1, 'compatibility': {'server': {'min': 'v0.1.52', 'max': 'v0.1.54'}, 'protocol': {'min': 1, 'max': 1}, 'updater': {'min': 'v0.1.53-staging.9000', 'max': 'v0.1.53-staging.9001'}}, 'artifacts': [{'os': 'linux', 'arch': 'amd64', 'name': 'fixture-binary', 'size': 1, 'sha256': 'a' * 64}], 'oci_images': [{'repository': 'ghcr.io/review/p2pstream', 'digest': server_ref.split('@')[1], 'media_type': 'application/vnd.oci.image.index.v1+json', 'size': 123, 'platforms': [{'os': 'linux', 'arch': 'amd64', 'digest': server_ref.split('@')[1], 'media_type': 'application/vnd.oci.image.manifest.v1+json', 'size': 123}, {'os': 'linux', 'arch': 'arm64', 'digest': 'sha256:' + 'c' * 64, 'media_type': 'application/vnd.oci.image.manifest.v1+json', 'size': 123}]}], 'release_assets': assets}
    manifest_raw = json.dumps(manifest, separators=(',', ':')).encode()
    manifest['_files'] = {'p2pstream_agent_update_manifest.json': manifest_raw, 'p2pstream_install.json': desc_raw, 'p2pstream_server_update.json': metadata_raw}
    manifest_hash = hashlib.sha256(manifest_raw).hexdigest()
    host.release_metadata = lambda *args: (manifest, desc, server_ref, manifest_hash)
    arguments = argparse.Namespace(bundle='/review/install-bundle', expect_installation=installation, expect_version=version, expect_commit=commit, expect_channel='staging', expect_arch='amd64', manifest_sha256=manifest_hash, repository='review/p2pstream')
    WORKER_CONTEXT.write_text(json.dumps({'args': vars(arguments), 'source': str(source),
                                         'options': options, 'image': server_ref, 'descriptor': desc,
                                         'manifest': {k: v for k, v in manifest.items() if k != '_files'},
                                         'files': {k: base64.b64encode(v).decode() for k, v in manifest['_files'].items()}}))
    WORKER_CONTEXT.chmod(0o600)
    return arguments

args = fixture('v0.1.53-staging.9000', 'a' * 40, images['baseline'], images['updater'])
os.chdir(source)
checks = ['public-preflight-authenticates-real-pid1-without-lifecycle-or-owner-mutation', 'container-name-matches-service-produces-duplicate-docker-aliases']

def rejected(fn, text):
    try:
        fn()
    except (host.Failure, OSError) as error:
        assert text in str(error), str(error)
        return
    raise AssertionError('Expected rejection: ' + text)

def managed(action):
    output = []
    original_compose = host.Host.compose
    def observed(controller, *args, **kwargs):
        result = original_compose(controller, *args, **kwargs)
        output.append(result.stdout + result.stderr)
        return result
    host.Host.compose = observed
    try:
        host.manage(argparse.Namespace(action=action))
    finally:
        host.Host.compose = original_compose
    return b'\n'.join(output)

wrong = copy.copy(args)
wrong.expect_installation = str(uuid.uuid4())
rejected(lambda: host.install(wrong, options), 'Wrong Docker host')
stale = copy.copy(args)
stale.expect_version = 'v0.1.53-staging.9001'
rejected(lambda: host.install(stale, options), 'Stale command')
original = override.read_bytes()
extra['services']['p2pstream']['environment']['SECRET'] = 'unapplied'
override.write_text(json.dumps(extra))
rejected(lambda: host.install(args, options), 'differ from the running server')
override.write_bytes(original)
assert info()['Id'] == initial_container and not (host.STATE / 'config.json').exists()
checks += ['wrong-host-same-release', 'stale-command', 'unapplied-inputs-before-mutation']

# A custom entrypoint can run the same binary yet is outside the supported
# enrollment contract. Rejection must leave the original container/data intact,
# and discarded preparation must permit fresh setup after source correction.
model['services']['p2pstream']['entrypoint'] = ['/app/p2pstream', 'server']
base.write_text(json.dumps(model))
compose('up', '-d', '--no-deps', 'p2pstream')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
rejected_container = info()['Id']
rejected(lambda: host.install(args, options), 'private diagnostic')
assert not (host.STATE / 'config.json').exists() and info()['Id'] == rejected_container
host.manage(argparse.Namespace(action='discard-preparation'))
assert not (host.STATE / 'host.json').exists() and info()['Id'] == rejected_container
assert docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity') == installation
del model['services']['p2pstream']['entrypoint']
base.write_text(json.dumps(model))
compose('up', '-d', '--no-deps', 'p2pstream')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
initial_container = info()['Id']
checks += ['unsupported-layout-no-activation', 'discard-unactivated-preparation-preserves-container-data', 'corrected-sources-allow-fresh-setup']

# Exercise compatible legacy enrollment before the explicit protocol migration.
# This is the legacy enrollment API, not a published release replacement.
original_command = host.Host.command
def legacy_enrollment(controller, *arguments):
    if arguments and arguments[0] == 'enroll' and '--deployment-directory' in arguments:
        values = list(arguments)
        index = values.index('--deployment-directory')
        del values[index:index + 2]
        arguments = tuple(values)
    return original_command(controller, *arguments)
host.Host.command = legacy_enrollment
try:
    host.install(args, options)
finally:
    host.Host.command = original_command
current_container = info()['Id']
assert current_container != initial_container
config = (host.STATE / 'config.json').read_bytes()
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
assert next(v for v in info()['Config']['Env'] if v.startswith('SECRET=')) == initial_secret
assert compose('ps', '-q', 'observer') == observer
host.install(args, options)
assert info()['Id'] == current_container and (host.STATE / 'config.json').read_bytes() == config
checks += ['fresh-setup', 'custom-project-two-files-env', 'dollar-secrets', 'one-recreation', 'idempotent-no-restart', 'other-service-preserved']

# Managed recreation carries the complete enrolled model and holds the host
# locks. Ordinary source Compose still models a legacy bypass: it drops the
# private connection and warns about the same-project orphaned executor.
managed_output = managed('recreate')
assert b'orphan' not in managed_output.lower()
assert info()['Id'] != current_container and (host.STATE / 'config.json').read_bytes() == config
current_container = info()['Id']
assert compose('ps', '-q', 'observer') == observer
controller = host.Host()
with controller.lock():
    updater = controller.compose('ps', '-q', 'p2pstream-server-updater').stdout.decode().strip()
native = subprocess.run(['docker', 'compose', *options, 'up', '-d', '--force-recreate', 'p2pstream'], check=True, capture_output=True, text=True, timeout=180)
assert 'orphan' in (native.stdout + native.stderr).lower()
detached_container = info()['Id']
assert detached_container != current_container
assert not any(value.startswith('SERVER_UPDATE_') for value in info()['Config']['Env'])
assert not any(mount['Destination'] == '/run/p2pstream-server-update' for mount in info()['Mounts'])
assert json.loads(docker('inspect', updater))[0]['State']['Running']
wait(lambda: docker('exec', detached_container, '/app/p2pstream', 'server-health'))
rejected(lambda: host.manage(argparse.Namespace(action='status')), 'private diagnostic')
saved_launcher = (host.STATE / 'manage').read_bytes()
# Invoke the freshly verified installation helper, not the installed controller
# whose older repair implementation may lack detached-binding recovery.
host.install(args, options)
current_container = info()['Id']
assert current_container != detached_container and info()['Config']['Image'] == images['baseline']
assert (host.STATE / 'config.json').read_bytes() == config
assert (host.STATE / 'server-updater-host.py').read_bytes() == Path('/review/install-bundle/server-updater-host.py').read_bytes()
assert (host.STATE / 'manage').read_bytes() == saved_launcher
assert docker('exec', current_container, '/app/p2pstream', 'server-installation-identity') == installation
assert next(value for value in info()['Config']['Env'] if value.startswith('SECRET=')) == initial_secret
assert any(mount['Destination'] == '/run/p2pstream-server-update' for mount in info()['Mounts'])
assert compose('ps', '-q', 'observer') == observer
host.manage(argparse.Namespace(action='status'))
checks += ['managed-recreate-no-orphan', 'native-recreate-reproduces-orphan-and-stripped-binding', 'verified-install-hands-off-controller-and-repairs-legacy-binding', 'repair-preserves-executor-data-identity-token-settings']

# Refuse to silently adopt source settings which differ from the last managed
# deployment, even when the immutable image and installation still match.
source_before = override.read_bytes()
extra['services']['p2pstream']['environment']['SECRET'] = 'unmanaged-change'
override.write_text(json.dumps(extra))
compose('up', '-d', '--force-recreate', 'p2pstream')
unmanaged_container = info()['Id']
wait(lambda: docker('exec', unmanaged_container, '/app/p2pstream', 'server-health'))
rejected(lambda: host.manage(argparse.Namespace(action='repair')), 'Restore the last applied inputs')
assert info()['Id'] == unmanaged_container and (host.STATE / 'config.json').read_bytes() == config
override.write_bytes(source_before)
extra['services']['p2pstream']['environment']['SECRET'] = 'literal-$$HOME-$${SECRET}-$$$$'
# Restore the exact source/runtime match before allowing legacy binding repair.
compose('up', '-d', '--force-recreate', 'p2pstream')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
host.manage(argparse.Namespace(action='repair'))
current_container = info()['Id']
assert next(value for value in info()['Config']['Env'] if value.startswith('SECRET=')) == initial_secret
checks += ['detached-repair-rejects-changed-settings-without-replacement', 'restored-sources-allow-binding-repair']

# Explicit migration preserves legacy credentials/floors while publishing one
# canonical model and original/default entry points. Exercise a genuinely
# different native Compose plugin retained by the surrounding harness.
old_config = host.read_json(host.STATE / 'config.json')
compose('up', '-d', '--force-recreate', 'p2pstream')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
assert not any(value.startswith('SERVER_UPDATE_') for value in info()['Config']['Env'])
args.migrate_deployment = True
host.install(args, options)
args.migrate_deployment = False
new_config = host.read_json(host.STATE / 'config.json')
assert new_config['deployment_protocol'] == 2 and new_config['token'] == old_config['token']
assert_runtime_aliases('protocol-2-migration', receipt=True)
assert new_config['instance_id'] == old_config['instance_id'] and new_config['bootstrap_floor'] == old_config['bootstrap_floor']
config = (host.STATE / 'config.json').read_bytes()
assert new_config['token'].encode() not in Path(new_config['deployment_file']).read_bytes()
assert Path(new_config['deployment_file']).stat().st_mode & 0o777 == 0o640
compose_versions = {'managed': docker('compose', 'version', '--short'), 'native': docker('--config', '/review/native-docker', 'compose', 'version', '--short')}
assert compose_versions['managed'].lstrip('v') != compose_versions['native'].lstrip('v'), compose_versions
native_checks = []
receipt_before_native = (host.STATE / 'runtime-receipt.json').read_bytes()
# Exact inputs stay in the disposable guest's private diagnostics. They contain
# fixture secrets and must be excluded from published evidence selections.
receipt_inputs = Path('/review/private-runtime-receipt-inputs')
receipt_inputs.mkdir(mode=0o700)
(receipt_inputs / 'receipt.json').write_bytes(receipt_before_native)
(receipt_inputs / 'deployment.json').write_bytes(Path(new_config['deployment_file']).read_bytes())
for native_options in [[], options]:
    native = subprocess.run(['docker', '--config', '/review/native-docker', 'compose', *native_options, 'up', '-d', '--force-recreate'], cwd=source, check=True, capture_output=True, text=True, timeout=180)
    assert 'orphan' not in (native.stdout + native.stderr).lower()
    wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
    host.manage(argparse.Namespace(action='status'))
    assert info()['Config']['Image'] == images['baseline']
    assert docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity') == installation
    assert (host.STATE / 'config.json').read_bytes() == config
    managed_hash = docker('compose', '-p', project, '-f', new_config['deployment_file'], 'config', '--hash', 'p2pstream').split()[1]
    native_hash = info()['Config']['Labels']['com.docker.compose.config-hash']
    context = 'original-files' if native_options else 'default'
    assert_runtime_aliases('native-compose-' + context, receipt=True)
    # Equal service hashes can skip compatibility dispatch. Exercise the receipt
    # verifier directly against the real native-created container as well.
    (receipt_inputs / ('inspection-' + context + '.json')).write_text(json.dumps(info()))
    controller = host.Host()
    verified = False
    try:
        with controller.lock():
            result = controller.run_helper('/app/server-update-test', ['-test.v', '-test.run=^TestDockerReviewRuntimeReceipt$'],
                                           image=images['helper'], mounts=('--env', 'REVIEW_CONFIG=' + str(host.STATE / 'config.json'),
                                                                          '--mount', 'type=bind,src=/review,dst=/review'))
        Path('/review/installation-direct-receipt-' + context + '.log').write_bytes(result.stdout + result.stderr)
        direct = host.read_json(host.STATE / 'native-runtime-receipt.json')
        assert direct['runtime_receipt_verified_directly'] and direct['native_hash'] == native_hash
        verified = True
    except host.Failure:
        Path('/review/installation-direct-receipt-' + context + '.log').write_bytes((host.STATE / 'helper-failure.log').read_bytes())
        direct_receipt_failures.append(context)
    assert (host.STATE / 'runtime-receipt.json').read_bytes() == receipt_before_native
    native_checks.append({'context': context, 'runtime_receipt_verified_directly': verified, 'managed_hash': managed_hash, 'native_hash': native_hash, 'receipt_fallback_required': native_hash != managed_hash})
Path('/review/installation-compose-compatibility.json').write_text(json.dumps({'versions': compose_versions, 'recreations': native_checks}))
observer = compose('ps', '-q', 'observer')
current_container = info()['Id']
managed_output = managed('recreate')
assert b'orphan' not in managed_output.lower()
assert info()['Id'] != current_container
assert info()['Config']['Image'] == images['baseline']
assert (host.STATE / 'config.json').read_bytes() == config
assert compose('ps', '-q', 'observer') == observer
current_container = info()['Id']
plan = host.read_json(host.STATE / 'host.json')['source_bridge']['files']
override = Path(next(item['editable'] for item in plan if item['path'] == str(override)))
checks += ['explicit-migration-repairs-legacy-binding-before-transition', 'explicit-legacy-migration-preserves-credentials-floors', 'default-and-original-multifile-native-compose-no-orphan', 'different-compose-version-compatible', 'managed-recreate-after-verified-native-hash', 'canonical-model-private-without-token', 'duplicate-docker-aliases-normalized-in-managed-runtime-receipt']
if not direct_receipt_failures:
    checks.append('native-recreation-duplicate-aliases-direct-receipt-verification')
if any(check['receipt_fallback_required'] for check in native_checks):
    checks.append('native-compose-hash-mismatch-runtime-receipt')

# A missing socket must never be considered enabled merely because config exists.
controller = host.Host()
with controller.lock():
    updater = controller.compose('ps', '-q', 'p2pstream-server-updater').stdout.decode().strip()
    docker('stop', updater)
rejected(lambda: host.manage(argparse.Namespace(action='status')), 'No such file')
host.manage(argparse.Namespace(action='repair'))
assert info()['Id'] == current_container
checks += ['socket-unavailable-no-success', 'helper-repair-no-server-restart']

# Docker client termination must also stop its container before ownership is
# released. A read-only command must not kill a helper left by an interrupted
# host controller; explicit repair settles it without recreating the server.
controller = host.Host()
unchanged = info()['Id']
with controller.lock():
    rejected(lambda: controller.run_helper('/bin/sh', ['-c', 'sleep 120'], timeout=1), 'timed out')
    assert not (host.STATE / 'helper.json').exists()
    name = controller.helper_name()
    assert not docker('ps', '-aq', '--filter', 'name=^/' + name + '$')
    docker('run', '-d', '--rm', '--name', name, '--label', 'p2pstream.server-update.host-helper=' + installation, '--entrypoint', '/bin/sh', images['updater'], '-c', 'sleep 120')
    host.atomic(host.STATE / 'helper.json', {'name': name, 'phase': 'healthy', 'readonly': True})
rejected(lambda: host.manage(argparse.Namespace(action='logs')), 'explicit repair or recover-update')
assert json.loads(docker('inspect', name))[0]['State']['Running']
host.manage(argparse.Namespace(action='repair'))
assert info()['Id'] == unchanged and not docker('ps', '-aq', '--filter', 'name=^/' + name + '$')
checks += ['timed-out-helper-confirmed-stopped', 'inspection-preserves-abandoned-helper', 'explicit-repair-settles-without-server-restart']

# Model an executor-pinned software change independently of editable sources.
controller = host.Host()
with controller.lock():
    current = host.read_json(controller.current_model_path())
    controller.runtime('block')
    current['services']['p2pstream']['image'] = images['candidate']
    current['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION'] = str(uuid.uuid4())
    controller.write_model(current)
    controller.deploy_server()
    controller.wait_healthy()
    controller.record_runtime()
native = subprocess.run(['docker', '--config', '/review/native-docker', 'compose', 'up', '-d', '--force-recreate', 'p2pstream'], cwd=source, check=True, capture_output=True, text=True, timeout=180)
assert 'orphan' not in (native.stdout + native.stderr).lower()
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
host.manage(argparse.Namespace(action='status'))
assert info()['Config']['Image'] == images['candidate']
checks += ['ordinary-compose-after-software-change-retains-current-image']
extra['services']['p2pstream']['environment']['SECRET'] = 'changed-$$HOME-$${SECRET}-$$$$'
extra['services']['p2pstream']['ports'][0]['published'] = '18092'
override.write_text(json.dumps(extra))
env.write_text('PUBLIC_URL=https://localhost:18092\n')
host.manage(argparse.Namespace(action='apply'))
assert info()['Config']['Image'] == images['candidate']
assert (host.STATE / 'config.json').read_bytes() == config
assert info()['HostConfig']['PortBindings']['8081/tcp'][0]['HostPort'] == '18092'
assert compose('ps', '-q', 'observer') == observer
checks += ['apply-after-software-change-retains-current-image', 'ports-env-applied', 'private-token-preserved']

def removal_recovery_boundaries():
    """Exercise rollback before declaration and exact-target completion afterward."""
    before = info()
    controller = host.Host()
    with controller.lock():
        before_model = host.read_json(controller.current_model_path())
    def preserved(*, removed=False):
        current = info()
        controller = host.Host()
        with controller.lock():
            actual_model = host.read_json(controller.current_model_path())
        expected_model = host.decode(host.remove_model(before_model)) if removed else copy.deepcopy(before_model)
        if not removed:
            for model in (actual_model, expected_model):
                model['services']['p2pstream']['environment'].pop('SERVER_UPDATE_GENERATION', None)
        assert actual_model == expected_model
        assert current['Config']['Image'] == before['Config']['Image'] == images['candidate']
        assert current['Image'] == before['Image']
        settings = lambda value: dict(item.split('=', 1) for item in value['Config']['Env'] if not item.startswith('SERVER_UPDATE_'))
        assert settings(current) == settings(before)
        assert current['HostConfig']['PortBindings'] == before['HostConfig']['PortBindings']
        assert [mount for mount in current['Mounts'] if mount['Destination'] == '/data'] == [mount for mount in before['Mounts'] if mount['Destination'] == '/data']
        assert docker('exec', current['Id'], '/app/p2pstream', 'server-installation-identity') == installation
        assert (host.STATE / 'config.json').read_bytes() == config
        assert compose('ps', '-q', 'observer') == observer

    # This seam precedes the declaration, not merely runtime detach: a persisted
    # detaching-stage continuation already requires forward-only completion.
    real_begin, real_stop = host.Host.begin_detached_continuation, host.Host.stop_executor
    pre_declaration, executor_fds = [], []
    interrupted = False
    def observed_stop(controller):
        fd = real_stop(controller)
        identity = os.fstat(fd)
        executor_fds.append((fd, (identity.st_dev, identity.st_ino, identity.st_mode)))
        return fd
    def release_interrupted_executor():
        # Model process-exit release after an expected in-process interruption.
        # Never unlock/change the file or close an unknown/reused descriptor.
        assert len(executor_fds) == 1
        fd, identity = executor_fds[0]
        try:
            current_fd = os.fstat(fd)
        except OSError as error:
            raise AssertionError('Interrupted executor fd was unexpectedly closed') from error
        assert (current_fd.st_dev, current_fd.st_ino, current_fd.st_mode) == identity, 'Interrupted executor fd was reused'
        os.close(fd)
        # Check this process only. listdir's own transient fd can already be
        # closed by the time fstat observes it; matching live fds must fail.
        for name in os.listdir('/proc/self/fd'):
            try:
                other = os.fstat(int(name))
            except OSError as error:
                if error.errno == errno.EBADF:
                    continue
                raise
            assert (other.st_dev, other.st_ino) != identity[:2], 'Another descriptor still references interrupted executor lock'
    def interrupt_before_declaration(controller):
        journal = host.read_json(host.STATE / 'host.json')
        assert journal['phase'] == 'removing' and 'detached_continuation' not in journal
        target = controller.server_container()
        assert not target['State']['Running'] and not target['State'].get('Paused') and not target['State'].get('Restarting')
        intent = controller.verify_replacement_intent()
        controller.replacement_server(target, intent['binding'])
        data = controller.replacement_data()
        assert data['owner'] is not None and data['owner']['allowed'] is False
        assert controller.replacement_owned(data['owner'], intent)
        pre_declaration.append(data['directory'])
        raise KeyboardInterrupt()
    host.Host.begin_detached_continuation, host.Host.stop_executor = interrupt_before_declaration, observed_stop
    try:
        try:
            host.manage(argparse.Namespace(action='remove'))
        except KeyboardInterrupt:
            interrupted = True
        else:
            raise AssertionError('Pre-declaration removal interruption did not occur')
    finally:
        host.Host.begin_detached_continuation, host.Host.stop_executor = real_begin, real_stop
        if interrupted:
            assert len(pre_declaration) == 1
            release_interrupted_executor()
    assert len(pre_declaration) == 1
    journal = host.read_json(host.STATE / 'host.json')
    assert journal['phase'] == 'removing' and 'detached_continuation' not in journal
    host.manage(argparse.Namespace(action='repair'))
    assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
    preserved()
    checks.extend(['pre-declaration-interrupted-removal-rolls-back', 'removal-rollback-restores-current-image'])

    # At wait_original the declared target has been detached and started. Repair
    # must finish that same target, rather than restore the managed deployment.
    real_wait = host.Host.wait_original
    detached, executor_fds = [], []
    interrupted = False
    def interrupt_after_start(controller):
        journal = host.read_json(host.STATE / 'host.json')
        continuation = journal['detached_continuation']
        assert journal['phase'] == 'removing'
        assert continuation['operation'] == 'remove' and continuation['stage'] == 'starting'
        target, data = controller.detached_target()  # Keep every production identity/runtime gate.
        assert target['State']['Running'] and data['owner'] is None
        assert target['Id'] == continuation['server']['Id']
        assert target.get('RestartCount') == continuation['restart_count']
        assert data['directory'] == pre_declaration[0]
        detached.append(copy.deepcopy(target))
        raise KeyboardInterrupt()
    host.Host.wait_original, host.Host.stop_executor = interrupt_after_start, observed_stop
    try:
        try:
            host.manage(argparse.Namespace(action='remove'))
        except KeyboardInterrupt:
            interrupted = True
        else:
            raise AssertionError('Post-declaration removal interruption did not occur')
    finally:
        host.Host.wait_original, host.Host.stop_executor = real_wait, real_stop
        if interrupted:
            assert len(detached) == 1
            release_interrupted_executor()
    assert len(detached) == 1

    real_compose = host.Host.compose
    events = []
    def verified_health(controller):
        result = real_wait(controller)
        events.append('healthy')
        return result
    def observed_compose(controller, *arguments, **kwargs):
        if arguments[0] in {'start', 'stop', 'up'}:
            events.append(arguments[0])
        if arguments[0] == 'rm' and 'p2pstream-server-updater' in arguments:
            assert events and events[-1] == 'healthy', 'Executor removal preceded confirmed server health'
            target, data = controller.detached_target()
            assert target['Id'] == detached[0]['Id'] and target['State']['Running'] and data['owner'] is None
        result = real_compose(controller, *arguments, **kwargs)
        if arguments[0] == 'rm' and 'p2pstream-server-updater' in arguments:
            events.append('executor-removed')
        return result
    host.Host.wait_original, host.Host.compose = verified_health, observed_compose
    try:
        host.manage(argparse.Namespace(action='repair'))
    finally:
        host.Host.wait_original, host.Host.compose = real_wait, real_compose
    assert events and events[-1] == 'executor-removed'
    assert not any(event in {'start', 'stop', 'up'} for event in events)
    journal = host.read_json(host.STATE / 'host.json')
    assert journal['phase'] == 'removed'
    assert 'detached_continuation' not in journal and 'replacement_intent' not in journal
    current = info()
    assert current['Id'] == detached[0]['Id'] and current['State']['Running']
    assert current['State'].get('StartedAt') == detached[0]['State'].get('StartedAt')
    assert current.get('RestartCount') == detached[0].get('RestartCount')
    assert not any(value.startswith('SERVER_UPDATE_') for value in current['Config']['Env'])
    assert not any(mount['Destination'] == '/run/p2pstream-server-update' for mount in current['Mounts'])
    assert 'p2pstream.server-update.instance' not in current['Config'].get('Labels', {})
    controller = host.Host()
    with controller.lock():
        assert 'p2pstream-server-updater' not in host.read_json(controller.current_model_path())['services']
        assert not controller.compose('ps', '--all', '--quiet', 'p2pstream-server-updater', model='before-compose.json').stdout.strip()
        assert controller.replacement_data() == {'directory': pre_declaration[0], 'owner': None}
    assert not docker('ps', '-aq', '--filter', 'label=p2pstream.server-update.executor=' + installation)
    preserved(removed=True)
    checks.extend(['post-declaration-interrupted-removal-completes-recorded-target',
                   'removal-completion-confirms-health-before-executor-removal',
                   'completed-removal-keeps-latest-image-settings-data'])


removal_recovery_boundaries()

# Re-enroll from the supported exported current model using a newly verified
# recipe, archiving removal state without moving away the editable input.
args = fixture('v0.1.53-staging.9001', 'b' * 40, images['candidate'], images['candidate_updater'])
os.chdir(source)
real_finish_archive = host.Host.finish_archive
def interrupt_archive(self):
    raise KeyboardInterrupt()
host.Host.finish_archive = interrupt_archive
try:
    host.install(args, options)
except KeyboardInterrupt:
    pass
else:
    raise AssertionError('Archive interruption did not occur')
host.Host.finish_archive = real_finish_archive
assert not (host.STATE / 'host.json').exists() and (host.STATE / 'archive.json').exists()
host.install(args, options)
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
assert info()['Config']['Image'] == images['candidate']
assert (host.STATE / 'history').exists()
assert host.read_json(host.STATE / 'config.json')['deployment_protocol'] == 2
assert_runtime_aliases('fresh-protocol-2-reenrollment', receipt=True)
checks += ['archive-interruption-after-journal-commit-resumes', 'reenroll-after-removal-current-recipe', 'fresh-protocol-2-enrollment-with-duplicate-docker-aliases']

# Stop the first fixture before beginning an independent first-activation case.
# These are only this script's containers/directories inside the nested daemon.
host.manage(argparse.Namespace(action='remove'))
controller = host.Host()
completed_server = info()['Id']
with controller.lock():
    controller.compose('stop', 'p2pstream', 'observer')
docker('rm', completed_server)
completed = Path('/review/completed-main')
completed.mkdir(mode=0o700)
host.STATE.rename(completed / 'updater')
Path('/etc/p2pstream-server-deployment').rename(completed / 'deployment')

source = Path('/review/recovery-source')
source.mkdir(mode=0o700)
project = 'review-failed-install-recovery'
base = source / 'compose.json'
options = ['-p', project, '-f', str(base)]
model = {'services': {'p2pstream': {'image': images['baseline'], 'container_name': 'p2pstream', 'restart': 'unless-stopped', 'read_only': True, 'tmpfs': ['/tmp:rw,noexec,nosuid,size=16m'],
                                  'environment': {'CONFIG_DIR': '/data', 'MANAGEMENT_PORT': '8081'},
                                  'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}]}},
         'volumes': {'data': {'name': project + '-data'}}}
model['services']['p2pstream']['tmpfs'] = ['/tmp']
base.write_text(json.dumps(model))
os.chdir(source)
compose('up', '-d')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
installation = docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity')
assert_runtime_aliases('recovery-original-compose')
args = fixture('v0.1.53-staging.9000', 'a' * 40, images['baseline'], images['updater'])
original_record_runtime = host.Host.record_runtime
def fail_first_record_runtime(controller):
    raise host.Failure('fixture first record-runtime failure')
host.Host.record_runtime = fail_first_record_runtime
try:
    rejected(lambda: host.install(args, options), 'fixture first record-runtime failure')
finally:
    host.Host.record_runtime = original_record_runtime
journal = host.read_json(host.STATE / 'host.json')
assert journal['phase'] == 'rolled_back', journal['phase']
assert not (host.STATE / 'state.json').exists(), 'Executor created update history before first activation completed'
assert not (host.STATE / 'history').exists()
recovery_evidence = {'initial_failed_phase': journal['phase'], 'update_history_created': False, 'existing_host_history': False}
Path('/review/installation-recovery-evidence.json').write_text(json.dumps(recovery_evidence, indent=2))
rolled_back_container = info()['Id']
rolled_back_sources = base.read_bytes()
old_config = host.read_json(host.STATE / 'config.json')
old_floor = old_config['bootstrap_floor']
assert info()['Config']['Image'] == images['baseline']
assert not any(v.startswith('SERVER_UPDATE_') for v in info()['Config']['Env'])
assert not any(m['Destination'] == '/run/p2pstream-server-update' for m in info()['Mounts'])
executors = docker('ps', '-aq', '--filter', 'label=p2pstream.server-update.executor=' + installation).split()
assert len(executors) == 1 and not json.loads(docker('inspect', executors[0]))[0]['State']['Running']

cancel_args = argparse.Namespace(expect_installation=installation, repository='review/p2pstream', expect_channel='staging', bundle='/review/install-bundle')
def cancel():
    controller = host.Host()
    with controller.lock(allow_archive=True):
        controller.cancel_enrollment(cancel_args)

# Changed sources and an independently replaced image must be refused without
# archiving credentials, removing the saved executor, or replacing the server.
model['services']['p2pstream']['environment']['EXTRA_SETTING'] = 'unapplied'
base.write_text(json.dumps(model))
rejected(cancel, 'Original Compose source changed')
assert info()['Id'] == rolled_back_container and (host.STATE / 'host.json').exists()
base.write_bytes(rolled_back_sources)
del model['services']['p2pstream']['environment']['EXTRA_SETTING']
drift = host.read_json(host.STATE / 'before-compose.json')
drift['services']['p2pstream']['image'] = images['candidate']
drift_file = source / 'drift.json'
drift_file.write_text(json.dumps(drift))
docker('compose', '-p', project, '-f', str(drift_file), 'up', '-d', '--no-deps', '--force-recreate', 'p2pstream')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
drifted_container = info()['Id']
rejected(cancel, 'Original running image was not restored')
assert info()['Id'] == drifted_container and (host.STATE / 'host.json').exists()
assert not json.loads(docker('inspect', executors[0]))[0]['State']['Running']
docker('compose', '-p', project, '-f', str(host.STATE / 'before-compose.json'), 'up', '-d', '--no-deps', '--force-recreate', 'p2pstream')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
restored_container = info()['Id']
cancel()
assert info()['Id'] == restored_container and base.read_bytes() == rolled_back_sources
assert not (host.STATE / 'host.json').exists() and not (host.STATE / 'config.json').exists()
assert not docker('ps', '-aq', '--filter', 'label=p2pstream.server-update.executor=' + installation)
floor_receipt = host.read_json(host.STATE / 'cancelled-enrollment.json')
assert floor_receipt['completed'] and floor_receipt['installation'] == installation and floor_receipt['floor'] == old_floor
assert (host.STATE / 'history').is_dir()
recovery_evidence.update({'source_drift_refused': True, 'running_image_drift_refused': True, 'stopped_executor_removed': True, 'release_floor_retained': True, 'cancelled_original_container_preserved': True})
Path('/review/installation-recovery-evidence.json').write_text(json.dumps(recovery_evidence, indent=2))
assert docker('exec', restored_container, '/app/p2pstream', 'server-installation-identity') == installation
assert_runtime_aliases('cancelled-original-compose')

# Manual deployment of the source-built newer identity retains the same volume.
model['services']['p2pstream']['image'] = images['candidate']
base.write_text(json.dumps(model))
compose('up', '-d', '--no-deps', 'p2pstream')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
assert docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity') == installation
args = fixture('v0.1.53-staging.9001', 'b' * 40, images['candidate'], images['candidate_updater'])
host.install(args, options)
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
new_config = host.read_json(host.STATE / 'config.json')
assert new_config['deployment_protocol'] == 2 and new_config['bootstrap_floor']['sequence'] >= old_floor['sequence']
assert host.read_json(host.STATE / 'cancelled-enrollment.json') == floor_receipt
assert info()['Config']['Image'] == images['candidate']
assert docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity') == installation
assert_runtime_aliases('cancel-upgrade-fresh-enrollment', receipt=True)
recovery_evidence.update({'manual_candidate_upgrade_preserves_data_identity': True, 'fresh_protocol_2_enrollment': 'healthy', 'release_floor_receipt_survives_reenrollment': True})
Path('/review/installation-recovery-evidence.json').write_text(json.dumps(recovery_evidence, indent=2))
checks += ['first-protocol-2-activation-failure-rolls-back-without-update-history', 'cancel-refuses-source-drift', 'cancel-refuses-running-image-drift',
           'cancel-removes-only-stopped-original-executor', 'cancel-retains-release-floor-receipt', 'manual-fixed-image-same-data-fresh-enrollment']


def finish_fixture(name):
    """Retire only this disposable fixture, retaining all its private inputs."""
    host.manage(argparse.Namespace(action='remove'))
    server = info()['Id']
    controller = host.Host()
    with controller.lock():
        controller.compose('stop', 'p2pstream')
    docker('rm', server)
    completed = Path('/review') / name
    completed.mkdir(mode=0o700)
    host.STATE.rename(completed / 'updater')
    deployment = Path('/etc/p2pstream-server-deployment')
    if deployment.exists():
        deployment.rename(completed / 'deployment')


def start_standalone(name):
    global source, project, base, options, model, installation
    source = Path('/review') / name
    source.mkdir(mode=0o700)
    project = 'review-' + name
    base = source / 'compose.json'
    options = ['-p', project, '-f', str(base)]
    model = {'services': {'p2pstream': {
        'image': images['baseline'], 'container_name': 'p2pstream', 'restart': 'unless-stopped',
        'read_only': True, 'tmpfs': ['/tmp'],
        'environment': {'CONFIG_DIR': '/data', 'MANAGEMENT_PORT': '8081'},
        'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}]}},
        'volumes': {'data': {'name': project + '-data'}}}
    base.write_text(json.dumps(model))
    base.chmod(0o600)
    os.chdir(source)
    compose('up', '-d')
    wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
    installation = docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity')


def current_identity():
    actual = info()
    # Preserve every mount field; Docker may reorder the mount array.
    return {'Id': actual['Id'], 'Image': actual['Image'],
            'StartedAt': actual['State']['StartedAt'], 'Running': actual['State']['Running'],
            'RestartCount': actual['RestartCount'],
            'Mounts': sorted(json.dumps(m, sort_keys=True) for m in actual['Mounts']),
            'installation': docker('exec', actual['Id'], '/app/p2pstream', 'server-installation-identity'),
            'ca_sha256': hashlib.sha256(docker('exec', actual['Id'], 'cat',
                                              '/data/certs/management/ca.crt.pem').encode()).hexdigest()}


def cancellation_retry_inputs():
    result = {}
    for name in ('host.json', 'config.json', host.CANCELLATION, host.BLOCKED_INITIAL_OWNER,
                 'archive.json', 'host.lock', 'executor.lock'):
        path = host.STATE / name
        if not path.exists():
            result[name] = None
        else:
            metadata = path.stat()
            result[name] = (path.read_bytes(), metadata.st_dev, metadata.st_ino,
                            metadata.st_mode, metadata.st_uid, metadata.st_gid)
    return result


# Actual initial owned replacement removal/create failure. The sole failure
# substitution removes the stopped owned server at --no-start, then raises; all
# successful rollback/repair deployment and runtime checks remain production.
finish_fixture('completed-original-recovery')
start_standalone('initial-replacement-gap')
original_info = info()
original_resolved = json.loads(compose('config', '--format', 'json'))
original_volume = json.loads(docker('volume', 'inspect', project + '-data'))
fixture('v0.1.53-staging.9000', 'a' * 40, images['baseline'], images['updater'])
gap_result = fresh_legacy('install', fault='replacement-create')
before_gap = (host.STATE / 'before-compose.json').read_bytes()
assert 'source-before-failure-' + hashlib.sha256(before_gap).hexdigest() in gap_result['events']
assert 'actual-owned-server-removed' in gap_result['events']
assert json.loads(before_gap) == original_resolved, 'initial-gap-snapshot-not-current-source'
assert not docker('ps', '--all', '--quiet', '--filter', 'id=' + original_info['Id']), 'initial-gap-old-server-not-removed'
gap_config = (host.STATE / 'config.json').read_bytes()
fresh_legacy('repair')
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
assert info()['Id'] != original_info['Id'] and info()['Image'] == original_info['Image']
assert (host.STATE / 'config.json').read_bytes() == gap_config
assert docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity') == installation
assert json.loads(docker('volume', 'inspect', project + '-data')) == original_volume
assert [m for m in info()['Mounts'] if m['Destination'] == '/data'] == [m for m in original_info['Mounts'] if m['Destination'] == '/data']
assert (host.STATE / 'before-compose.json').read_bytes() == before_gap
checks += ['initial-owned-no-start-removal-failure-has-current-source-snapshot',
           'fresh-process-initial-replacement-repair-preserves-software-data-credentials']

# Repeat the actual remove/create failure on an owned configuration apply.
# Automatic rollback must use its recorded current model, and a new Python
# process independently confirms repair rather than reusing cached authority.
before_apply = info()
before_apply_model = Path(host.read_json(host.STATE / 'config.json')['deployment_file']).read_bytes()
before_apply_config = (host.STATE / 'config.json').read_bytes()
input_file = Path(next(item['editable'] for item in host.read_json(host.STATE / 'host.json')['source_bridge']['files']
                       if item['editable']))
pending = json.loads(input_file.read_bytes())
pending['services']['p2pstream']['environment']['UNCOMMITTED_SETTING'] = 'legacy-apply'
input_file.write_text(json.dumps(pending))
pending_bytes = input_file.read_bytes()
apply_gap = fresh_legacy('apply', fault='replacement-create')
assert 'source-before-failure-' + hashlib.sha256(before_apply_model).hexdigest() in apply_gap['events']
assert 'actual-owned-server-removed' in apply_gap['events']
fresh_legacy('repair')
after_apply = info()
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
assert after_apply['Id'] != before_apply['Id'] and after_apply['Image'] == before_apply['Image']
assert (host.STATE / 'before-compose.json').read_bytes() == before_apply_model
assert (host.STATE / 'config.json').read_bytes() == before_apply_config and input_file.read_bytes() == pending_bytes
unchanged_env = lambda value: sorted(item for item in value['Config']['Env']
                                     if not item.startswith('SERVER_UPDATE_GENERATION='))
assert unchanged_env(after_apply) == unchanged_env(before_apply)
assert sorted(json.dumps(m, sort_keys=True) for m in after_apply['Mounts']) == sorted(json.dumps(m, sort_keys=True) for m in before_apply['Mounts'])
assert after_apply['HostConfig']['PortBindings'] == before_apply['HostConfig']['PortBindings']
assert docker('exec', after_apply['Id'], '/app/p2pstream', 'server-installation-identity') == installation
assert json.loads(docker('volume', 'inspect', project + '-data')) == original_volume
checks += ['owned-legacy-apply-removal-failure-rolls-back-current-model-with-fresh-repair']

# Rollback has created the standalone target and really detached ownership, but
# has not started it. The absent owner authorizes completion only for this exact
# existing target; a retry must not remove/recreate it or replace application data.
finish_fixture('completed-initial-replacement-gap')
start_standalone('detached-existing-target')
fixture('v0.1.53-staging.9000', 'a' * 40, images['baseline'], images['updater'])
detach_original = current_identity()
detach_volume = json.loads(docker('volume', 'inspect', project + '-data'))
detach_result = fresh_legacy('install', fault='record-runtime', interrupt='after-runtime-detach')
assert 'actual-created-standalone-target-detached-before-start' in detach_result['events']
detached_target = detach_result['checkpoint']
assert detached_target and detached_target['id'] != detach_original['Id']
assert detached_target['image'] == detach_original['Image']
target_before_start = json.loads(docker('inspect', detached_target['id']))[0]
assert target_before_start['State']['Status'] == 'created' and not target_before_start['State']['Running']
assert host.read_json(host.STATE / 'host.json')['phase'] == 'rolling_back'
detach_config = (host.STATE / 'config.json').read_bytes()
fresh_legacy('rollback')
target_after_start = info()
assert target_after_start['Id'] == detached_target['id'], 'detached-target-retry-recreated-container'
assert target_after_start['Image'] == detached_target['image'] and target_after_start['State']['Running']
assert sorted(json.dumps(m, sort_keys=True) for m in target_after_start['Mounts']) == sorted(json.dumps(m, sort_keys=True) for m in detached_target['mounts'])
assert host.read_json(host.STATE / 'host.json')['phase'] == 'rolled_back'
assert (host.STATE / 'config.json').read_bytes() == detach_config
assert docker('exec', target_after_start['Id'], '/app/p2pstream', 'server-installation-identity') == installation
assert current_identity()['ca_sha256'] == detach_original['ca_sha256']
assert json.loads(docker('volume', 'inspect', project + '-data')) == detach_volume
checks += ['fresh-rollback-after-real-detach-starts-exact-existing-standalone-target-without-recreation']
# Return to healthy enrollment only after the exact-target assertions, then use
# the normal removal/fixture cleanup before the independent cancellation case.
fresh_legacy('repair')

# Independent historical-residue fixture, explicitly replaying only the old
# repair publication/block sequence AFTER the newer standalone writer started.
# Current setup guards do not naturally create this state. Owner bytes below
# come exclusively from the real production runtime('block') operation.
finish_fixture('completed-detached-existing-target')
start_standalone('upgraded-cancellation-retry')
fixture('v0.1.53-staging.9000', 'a' * 40, images['baseline'], images['updater'])
fresh_legacy('install', fault='record-runtime')
assert host.read_json(host.STATE / 'host.json')['phase'] == 'rolled_back'
assert not (host.STATE / 'state.json').exists() and not (host.STATE / 'history').exists()
docker('exec', info()['Id'], '/bin/sh', '-c',
       'test ! -e /data/.p2pstream-runtime-owner.json && test ! -L /data/.p2pstream-runtime-owner.json')
model['services']['p2pstream']['image'] = images['candidate']
base.write_text(json.dumps(model))
compose('up', '-d', '--no-deps', '--force-recreate', 'p2pstream')
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
assert info()['Config']['Image'] == images['candidate']
docker('exec', info()['Id'], '/bin/sh', '-c',
       'test ! -e /data/.p2pstream-runtime-owner.json && test ! -L /data/.p2pstream-runtime-owner.json')
candidate_before_replay = current_identity()
volume_before_replay = json.loads(docker('volume', 'inspect', project + '-data'))
source_before_replay = base.read_bytes()
controller = host.Host()
with controller.lock():
    controller.write_model((host.STATE / 'enrolled-compose.json').read_bytes())
    controller.runtime('block')  # TEST REPLAY: old repair's publication/block.
assert current_identity() == candidate_before_replay, 'historical-replay-changed-live-candidate'
blocked_owner = subprocess.check_output(['docker', 'exec', info()['Id'], 'cat',
                                         '/data/.p2pstream-runtime-owner.json'], timeout=15)
owner = json.loads(blocked_owner)
assert owner['api'] == 2 and owner['allowed'] is False and owner['instance_id'] == installation
assert owner['version'] == 'v0.1.53-staging.9000' and owner['commit'] == 'a' * 40
assert owner['generation'] == host.read_json(host.STATE / 'enrolled-compose.json')['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION']
rejected(lambda: host.manage(argparse.Namespace(action='rollback')), 'Running image changed outside this host transaction')
assert host.read_json(host.STATE / 'host.json')['phase'] == 'recovery_required'
assert not host.read_json(host.STATE / 'host.json').get('recovery_phase')
assert current_identity() == candidate_before_replay, 'rollback-refusal-changed-live-candidate'
assert base.read_bytes() == source_before_replay
assert json.loads(docker('volume', 'inspect', project + '-data')) == volume_before_replay
fixture('v0.1.53-staging.9001', 'b' * 40, images['candidate'], images['candidate_updater'])
cancel_result = fresh_legacy('cancel-enrollment', interrupt='after-owner-unlink')
assert 'actual-owner-unlinked-and-synced' in cancel_result['events']
receipt_before_retry = (host.STATE / 'cancelled-enrollment.json').read_bytes()
retry_inputs_before_restart = cancellation_retry_inputs()
assert not json.loads(receipt_before_retry)['completed']
assert (host.STATE / 'host.json').exists() and (host.STATE / 'config.json').exists()
assert current_identity() == candidate_before_replay, 'interrupted-cancellation-changed-live-candidate'
docker('exec', info()['Id'], '/bin/sh', '-c',
       'test ! -e /data/.p2pstream-runtime-owner.json && test ! -L /data/.p2pstream-runtime-owner.json')
# Keep all receipt/archive/lock inputs. Restart the SAME actual container so its
# process lease and StartedAt change independently of the saved cancellation.
docker('restart', candidate_before_replay['Id'])
wait(lambda: docker('exec', info()['Id'], '/app/p2pstream', 'server-health'))
restarted_candidate = current_identity()
assert restarted_candidate['Id'] == candidate_before_replay['Id']
assert restarted_candidate['Image'] == candidate_before_replay['Image']
assert restarted_candidate['StartedAt'] != candidate_before_replay['StartedAt']
assert restarted_candidate['installation'] == installation
assert (host.STATE / 'cancelled-enrollment.json').read_bytes() == receipt_before_retry
assert cancellation_retry_inputs() == retry_inputs_before_restart, 'restart-altered-cancellation-receipt-archive-or-lock-inputs'
retry_result = fresh_legacy('cancel-enrollment', semantic=True)
assert 'actual-compose-output-semantic-rerender' in retry_result['events']
assert current_identity() == restarted_candidate, 'cancellation-retry-recreated-or-restarted-candidate'
assert base.read_bytes() == source_before_replay
assert json.loads(docker('volume', 'inspect', project + '-data')) == volume_before_replay
assert not (host.STATE / 'host.json').exists() and not (host.STATE / 'config.json').exists()
cancel_receipt = host.read_json(host.STATE / 'cancelled-enrollment.json')
assert cancel_receipt['completed'] and cancel_receipt['floor']['sequence'] == 9001
archives = list((host.STATE / 'history').iterdir())
assert len(archives) == 1
archived_owner = host.read_json(archives[0] / host.BLOCKED_INITIAL_OWNER)['owner']
assert bytes.fromhex(archived_owner['data_hex']) == blocked_owner
assert archived_owner['sha256'] == hashlib.sha256(blocked_owner).hexdigest()
# A successful fresh enrollment and removal confirms the retained receipt and
# data remain usable; it also leaves the legacy driver in its existing final
# removed phase for the canonical lifecycle driver.
fresh_legacy('install')
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
assert info()['Config']['Image'] == images['candidate']
assert docker('exec', info()['Id'], '/app/p2pstream', 'server-installation-identity') == installation
checks += ['historical-old-repair-runtime-block-replay-preserves-live-newer-candidate',
           'upgraded-cancel-real-unlink-interruption-keeps-durable-receipt',
           'fresh-cancel-retry-after-same-container-restart-and-semantic-compose-rerender',
           'cancel-retry-preserves-current-image-data-floor-and-fresh-enrollment']

# Cleanup precedes the final deferred assertion so every outcome is reviewable.
host.manage(argparse.Namespace(action='remove'))
controller = host.Host()
with controller.lock():
    controller.compose('stop', 'p2pstream')
result = 'failed' if direct_receipt_failures else 'passed'
Path('/review/installation-evidence.json').write_text(json.dumps({'checks': checks, 'project': project, 'runtime_aliases': alias_evidence,
                                                               'direct_runtime_receipt_failures': direct_receipt_failures, 'result': result}, indent=2))
assert not direct_receipt_failures, 'Direct native runtime receipt verification failed: ' + ', '.join(direct_receipt_failures)
print('PASS release host installation: ' + ', '.join(checks), flush=True)

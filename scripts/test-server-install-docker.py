#!/usr/bin/env python3
"""Production host lifecycle against the disposable daemon. Only release catalog
metadata is a fixture; Docker, Compose, private auth, runtime, IDs and recovery
are real. Never run this directly on a deployment host.
"""
import argparse
import copy
from datetime import datetime, timedelta, timezone
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import socket
import subprocess
import tarfile
import time
import urllib.request
import uuid

assert os.geteuid() == 0 and socket.gethostbyname('ghcr.io') == '127.0.0.1' and Path('/review/ISOLATED_TEST_VM').exists(), 'Disposable review daemon required'
spec = importlib.util.spec_from_file_location('host', '/review/install-bundle/server-updater-host.py')
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)
images = json.loads(Path('/review/images.json').read_text())
source = Path('/review/installation-source')
source.mkdir(mode=0o700)
project = 'review-release-install'
base = source / 'compose.json'
override = source / 'production.json'
env = source / '.env'
env.write_text('PUBLIC_URL=https://localhost:18091\n')
env.chmod(0o600)
model = {'services': {'p2pstream': {'image': images['baseline'], 'container_name': 'p2pstream', 'restart': 'unless-stopped', 'read_only': True, 'environment': {'CONFIG_DIR': '/data', 'MANAGEMENT_PORT': '8081'}, 'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}]}, 'observer': {'image': images['baseline'], 'command': ['/bin/sh', '-c', 'sleep infinity'], 'volumes': [{'type': 'volume', 'source': 'data', 'target': '/read-only-data', 'read_only': True}]}}, 'volumes': {'data': {'name': project + '-data'}}}
base.write_text(json.dumps(model))
extra = {'services': {'p2pstream': {'environment': {'MANAGEMENT_PUBLIC_URL': '${PUBLIC_URL}', 'SECRET': 'literal-$$HOME-$${SECRET}-$$$$'}, 'ports': [{'target': 8081, 'published': '18091', 'host_ip': '127.0.0.1'}]}}}
override.write_text(json.dumps(extra))
options = ['-p', project, '-f', str(base), '-f', str(override), '--env-file', str(env)]

def docker(*args):
    return subprocess.check_output(['docker', *args], text=True).strip()

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
    return argparse.Namespace(bundle='/review/install-bundle', expect_installation=installation, expect_version=version, expect_commit=commit, expect_channel='staging', expect_arch='amd64', manifest_sha256=manifest_hash, repository='review/p2pstream')

args = fixture('v0.1.53-staging.9000', 'a' * 40, images['baseline'], images['updater'])
os.chdir(source)
checks = ['container-name-matches-service-produces-duplicate-docker-aliases']

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
native = subprocess.run(['docker', 'compose', *options, 'up', '-d', '--force-recreate', 'p2pstream'], check=True, capture_output=True, text=True)
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
    native = subprocess.run(['docker', '--config', '/review/native-docker', 'compose', *native_options, 'up', '-d', '--force-recreate'], cwd=source, check=True, capture_output=True, text=True)
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
native = subprocess.run(['docker', '--config', '/review/native-docker', 'compose', 'up', '-d', '--force-recreate', 'p2pstream'], cwd=source, check=True, capture_output=True, text=True)
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

# Crash after saving the removal model, then rollback via pre-transition services.
real_wait_original = host.Host.wait_original
def interrupt_original(self):
    raise KeyboardInterrupt()
host.Host.wait_original = interrupt_original
try:
    host.manage(argparse.Namespace(action='remove'))
except KeyboardInterrupt:
    pass
else:
    raise AssertionError('Removal interruption did not occur')
host.Host.wait_original = real_wait_original
assert host.read_json(host.STATE / 'host.json')['phase'] == 'removing'
host.manage(argparse.Namespace(action='repair'))
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
assert info()['Config']['Image'] == images['candidate']
assert (host.STATE / 'config.json').read_bytes() == config
checks += ['interrupted-removal-durable-phase', 'removal-rollback-restores-current-image']

host.manage(argparse.Namespace(action='remove'))
assert host.read_json(host.STATE / 'host.json')['phase'] == 'removed'
assert info()['Config']['Image'] == images['candidate']
assert not any(v.startswith('SERVER_UPDATE_') for v in info()['Config']['Env'])
assert compose('ps', '-q', 'observer') == observer
checks += ['successful-removal-keeps-latest-image-settings-data']

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
model = {'services': {'p2pstream': {'image': images['baseline'], 'container_name': 'p2pstream', 'restart': 'unless-stopped', 'read_only': True,
                                  'environment': {'CONFIG_DIR': '/data', 'MANAGEMENT_PORT': '8081'},
                                  'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}]}},
         'volumes': {'data': {'name': project + '-data'}}}
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

# Manual deployment of the fixed published server retains the same data volume.
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

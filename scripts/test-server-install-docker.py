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
model = {'services': {'p2pstream': {'image': images['baseline'], 'restart': 'unless-stopped', 'read_only': True, 'environment': {'CONFIG_DIR': '/data', 'MANAGEMENT_PORT': '8081'}, 'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}]}, 'observer': {'image': images['baseline'], 'command': ['/bin/sh', '-c', 'sleep infinity'], 'volumes': [{'type': 'volume', 'source': 'data', 'target': '/read-only-data', 'read_only': True}]}}, 'volumes': {'data': {'name': project + '-data'}}}
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
    # isolated catalog binds the real platform manifest used by the nested daemon.
    manifest = {'schema_version': 1, 'channel': 'staging', 'version': version, 'commit': commit, 'sequence': 9000 if commit.startswith('a') else 9001, 'published_at': (datetime.now(timezone.utc) - timedelta(hours=1)).strftime('%Y-%m-%dT%H:%M:%SZ'), 'expires_at': (datetime.now(timezone.utc) + timedelta(days=1)).strftime('%Y-%m-%dT%H:%M:%SZ'), 'minimum_safe_version': version, 'security_epoch': 1, 'compatibility': {'server': {'min': 'v0.1.53-staging.9000', 'max': 'v0.1.53-staging.9001'}, 'protocol': {'min': 1, 'max': 1}, 'updater': {'min': 'v0.1.53-staging.9000', 'max': 'v0.1.53-staging.9001'}}, 'artifacts': [{'os': 'linux', 'arch': 'amd64', 'name': 'fixture-binary', 'size': 1, 'sha256': 'a' * 64}], 'oci_images': [{'repository': 'ghcr.io/review/p2pstream', 'digest': server_ref.split('@')[1], 'media_type': 'application/vnd.oci.image.index.v1+json', 'size': 123, 'platforms': [{'os': 'linux', 'arch': 'amd64', 'digest': server_ref.split('@')[1], 'media_type': 'application/vnd.oci.image.manifest.v1+json', 'size': 123}]}], 'release_assets': assets}
    manifest_raw = json.dumps(manifest, separators=(',', ':')).encode()
    manifest['_files'] = {'p2pstream_agent_update_manifest.json': manifest_raw, 'p2pstream_install.json': desc_raw, 'p2pstream_server_update.json': metadata_raw}
    manifest_hash = hashlib.sha256(manifest_raw).hexdigest()
    host.release_metadata = lambda *args: (manifest, desc, server_ref, manifest_hash)
    return argparse.Namespace(bundle='/review/install-bundle', expect_installation=installation, expect_version=version, expect_commit=commit, expect_channel='staging', expect_arch='amd64', manifest_sha256=manifest_hash, repository='review/p2pstream')

args = fixture('v0.1.53-staging.9000', 'a' * 40, images['baseline'], images['updater'])
os.chdir(source)
checks = []

def rejected(fn, text):
    try:
        fn()
    except (host.Failure, OSError) as error:
        assert text in str(error), str(error)
        return
    raise AssertionError('Expected rejection: ' + text)

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

host.install(args, options)
current_container = info()['Id']
assert current_container != initial_container
config = (host.STATE / 'config.json').read_bytes()
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
assert next(v for v in info()['Config']['Env'] if v.startswith('SECRET=')) == initial_secret
assert compose('ps', '-q', 'observer') == observer
host.install(args, options)
assert info()['Id'] == current_container and (host.STATE / 'config.json').read_bytes() == config
checks += ['fresh-setup', 'custom-project-two-files-env', 'dollar-secrets', 'one-recreation', 'idempotent-no-restart', 'other-service-preserved']

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
    current = host.read_json(host.STATE / 'compose.json')
    current['services']['p2pstream']['image'] = images['candidate']
    host.atomic(host.STATE / 'compose.json', current)
    controller.compose('up', '-d', '--no-deps', '--no-build', '--pull', 'never', 'p2pstream')
    controller.wait_healthy()
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
options = ['-p', project, '-f', str(host.STATE / 'detached-compose.json')]
os.chdir(host.STATE)
host.install(args, options)
assert host.read_json(host.STATE / 'host.json')['phase'] == 'healthy'
assert info()['Config']['Image'] == images['candidate']
assert (host.STATE / 'history').exists()
checks += ['reenroll-after-removal-current-recipe']

Path('/review/installation-evidence.json').write_text(json.dumps({'checks': checks, 'project': project, 'result': 'passed'}, indent=2))
print('PASS release host installation: ' + ', '.join(checks), flush=True)
# Keep all diagnostics, but stop this project's services so subsequent scenarios
# remain isolated and do not introduce writers on another scenario's data.
host.manage(argparse.Namespace(action='remove'))
controller = host.Host()
with controller.lock():
    controller.compose('stop', 'p2pstream', 'observer')

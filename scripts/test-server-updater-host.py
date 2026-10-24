#!/usr/bin/env python3
"""Deterministic host lifecycle boundaries; real Docker rehearsal complements these."""
import argparse
import contextlib
import copy
import fcntl
import importlib.util
import io
import json
import os
from pathlib import Path
import select
import signal
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location('host', ROOT / 'server-updater-host.py')
host = importlib.util.module_from_spec(spec)
spec.loader.exec_module(host)
ID = '11111111-1111-4111-8111-111111111111'
REF = 'ghcr.io/test/repo@sha256:' + 'a' * 64
HELPER = 'ghcr.io/test/repo-updater@sha256:' + 'd' * 64
EXECUTOR_ID = 'e' * 64

class Lifecycle(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = Path(self.temp.name)
        self.state = self.path / 'state'
        self.source = self.path / 'deployment'
        self.source.mkdir()
        for name in ('compose.yaml', 'prod.yaml', '.env'):
            (self.source / name).write_text('fixture input')
        self.model = {'name': 'custom-project', 'services': {'p2pstream': {'image': 'ghcr.io/test/repo:v1.0.1', 'restart': 'unless-stopped', 'environment': {'CONFIG_DIR': '/data', 'SECRET': '$$literal'}, 'volumes': [{'type': 'volume', 'source': 'data', 'target': '/data'}], 'ports': [{'target': 8081, 'published': '9443'}]}}, 'volumes': {'data': {'name': 'custom-data'}}}
        self.raw = json.dumps(self.model).encode()
        self.info = {'Id': 'container', 'Image': 'sha256:config', 'State': {'Running': True}, 'Config': {'Labels': {'com.docker.compose.project': 'custom-project', 'com.docker.compose.service': 'p2pstream', 'com.docker.compose.config-hash': 'a' * 64}}, 'Mounts': [{'Destination': '/data', 'Name': 'custom-data', 'RW': True}]}
        self.image = {'Id': 'sha256:config', 'Os': 'linux', 'Architecture': 'amd64', 'Config': {'Labels': {'org.opencontainers.image.version': 'v1.0.1', 'org.opencontainers.image.revision': 'b' * 40, 'org.opencontainers.image.source': 'https://github.com/test/repo'}}}
        self.helper = copy.deepcopy(self.image)
        self.helper['Config']['Labels']['p2pstream.image.role'] = 'server-updater'
        self.identity = ID
        self.restart_count = 0
        self.commands = []
        self.models_started = []
        self.failure = ''
        self.drift = False
        self.config_reads = 0
        self.detached = False
        self.foreign_writer = False
        self.server_inventory = None
        self.managed = False
        self.accepted_native_hash = None
        self.bundle = self.path / 'bundle'
        self.bundle.mkdir()
        for name in host.BUNDLE_FILES:
            source = ROOT / name
            if not source.exists():
                source = ROOT.parent / name
            (self.bundle / name).write_bytes(source.read_bytes())
        self.args = argparse.Namespace(bundle=str(self.bundle), expect_installation=ID, expect_version='v1.0.1', expect_commit='b' * 40, expect_channel='stable', expect_arch='amd64', manifest_sha256='f' * 64, repository='test/repo')
        self.options = ['-p', 'custom-project', '-f', str(self.source / 'compose.yaml'), '-f', str(self.source / 'prod.yaml'), '--env-file', str(self.source / '.env')]
        self.manifest = {'sequence': 12, 'security_epoch': 2, 'minimum_safe_version': 'v1.0.0', 'oci_images': [{'platforms': [{'os': 'linux', 'arch': 'amd64'}]}], '_files': {'p2pstream_agent_update_manifest.json': b'fixture', 'p2pstream_install.json': b'fixture', 'p2pstream_server_update.json': b'fixture'}}
        self.descriptor = {'updater_images': {'linux/amd64': HELPER}}
        outer = self
        class FixtureHost(host.Host):
            def __init__(self, directory=None):
                super().__init__(directory or outer.state)
            def capability(self, image, container=None):
                pass  # Legacy lifecycle fixtures retain their original protocol.
            def command(self, *args):
                outer.commands.append(('binary', args))
                if args[0] == 'enroll':
                    if outer.failure == 'layout':
                        raise host.Failure('unsupported layout')
                    cfg = {'instance_id': ID, 'repository': 'test/repo', 'project': 'custom-project', 'token': 'private-token' * 4, 'control_dir': str(outer.state / 'control'), 'data_volume': 'custom-data', 'channel': outer.args.expect_channel, 'bootstrap_floor': {key: outer.manifest[key] for key in ('sequence', 'security_epoch', 'minimum_safe_version')} | {'manifest_sha256': outer.args.manifest_sha256}}
                    model = host.read_json(Path(args[args.index('--compose') + 1]))
                    model['services']['p2pstream']['image'] = args[args.index('--image') + 1]
                    model['services']['p2pstream']['environment']['SERVER_UPDATE_TOKEN'] = cfg['token']
                    model['services']['p2pstream']['labels'] = {'p2pstream.server-update.instance': ID}
                    model['services']['p2pstream-server-updater'] = {'image': HELPER, 'volumes': []}
                    (outer.state / 'control').mkdir(exist_ok=True)
                    if outer.managed:
                        cfg.update(deployment_protocol=2, deployment_file=str(Path(args[args.index('--deployment-directory') + 1]) / 'compose.json'))
                        env = model['services']['p2pstream']['environment']
                        del env['SERVER_UPDATE_TOKEN']
                        env.update(SERVER_UPDATE_TOKEN_FILE='/run/p2pstream-server-update/token', SERVER_UPDATE_RUNTIME_PROTOCOL='2', SERVER_UPDATE_GENERATION=ID)
                        host.atomic(outer.state / 'control' / 'token', cfg['token'].encode(), mode=0o444)
                    host.atomic(outer.state / 'config.json', cfg)
                    host.atomic(Path(cfg.get('deployment_file', outer.state / 'compose.json')), model, mode=0o640 if outer.managed else 0o600)
                elif args[0] == 'configure':
                    model = host.read_json(Path(args[args.index('--compose') + 1]))
                    current = host.read_json(self.current_model_path())
                    server = model['services']['p2pstream']
                    previous = current['services']['p2pstream']
                    server['image'] = previous['image']
                    server['environment'].update({key: value for key, value in previous['environment'].items() if key.startswith('SERVER_UPDATE_')})
                    server.setdefault('labels', {})['p2pstream.server-update.instance'] = ID
                    model['services']['p2pstream-server-updater'] = current['services']['p2pstream-server-updater']
                    host.atomic(Path(args[args.index('--output') + 1]), model)
                return subprocess.CompletedProcess(args, 0, b'{}', b'')
            def compose(self, *args, model='compose.json'):
                outer.commands.append(('compose', args, model))
                if args[0] == 'ps':
                    return subprocess.CompletedProcess(args, 0, outer.server_inventory if outer.server_inventory is not None else (outer.info['Id'] + '\n').encode(), b'')
                if args[0] == 'config':
                    output = ('p2pstream ' + 'c' * 64 + '\n').encode() if '--hash' in args else self.model_path(model).read_bytes()
                    return subprocess.CompletedProcess(args, 0, output, b'')
                if args[0] == 'up' and args[-1] == 'p2pstream':
                    raw = self.model_path(model).read_bytes()
                    if '--force-recreate' in args or outer.server_inventory == b'' or outer.detached or not outer.models_started or outer.models_started[-1] != raw:
                        outer.restart_count += 1
                        outer.info['Id'] = 'container-' + str(outer.restart_count)
                    outer.server_inventory = None
                    outer.models_started.append(raw)
                    enrolled = 'p2pstream-server-updater' in host.decode(raw)['services']
                    outer.info['Config']['Labels']['com.docker.compose.config-hash'] = ('c' if enrolled else 'a') * 64
                    if enrolled:
                        outer.info['Config']['Labels']['p2pstream.server-update.instance'] = ID
                    else:
                        outer.info['Config']['Labels'].pop('p2pstream.server-update.instance', None)
                    outer.info['State']['Running'] = '--no-start' not in args
                    outer.detached = not enrolled
                if args[0] in {'stop', 'start'} and args[-1] == 'p2pstream':
                    outer.info['State']['Running'] = args[0] == 'start'
                return subprocess.CompletedProcess(args, 0, b'{}', b'')
            def status_socket(self):
                if outer.failure == 'socket':
                    raise host.Failure('socket unavailable')
            def wait_socket(self):
                self.status_socket()
            def healthy(self):
                self.status_socket()
                if outer.detached:
                    raise host.Failure('container is outside the enrolled deployment')
                if outer.info['Config']['Labels']['com.docker.compose.config-hash'] not in {'c' * 64, outer.accepted_native_hash}:
                    raise host.Failure('unverified configuration hash')
                return {'version': 'v1.0.1', 'commit': 'b' * 40}
            def wait_healthy(self):
                if outer.failure == 'crash':
                    outer.failure = ''
                    raise KeyboardInterrupt()
                if outer.failure == 'health':
                    outer.failure = ''
                    raise host.Failure('health failure')
                return self.healthy()
            def wait_original(self):
                pass
        self.patches = [patch.object(host, 'STATE', self.state), patch.object(host, 'Host', FixtureHost), patch.object(host, 'docker', self.docker), patch.object(host, 'local_engine', return_value='amd64'), patch.object(host, 'release_metadata', return_value=(self.manifest, self.descriptor, REF, 'f' * 64)), patch.object(os, 'geteuid', return_value=0), patch.object(os, 'chown'), patch.object(os, 'fchown')]
        # Run real flock, fsync, journal/recovery and root safety checks without
        # requiring privileged CI. Simulate ownership only; modes/type checks stay.
        original_lstat = Path.lstat
        def owned(path, *args, **kwargs):
            info = original_lstat(path, *args, **kwargs)
            values = list(info)
            values[4] = 0
            return os.stat_result(values)
        self.patches.append(patch.object(Path, 'lstat', owned))
        self.stack = contextlib.ExitStack()
        for item in self.patches:
            self.stack.enter_context(item)
        self.stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
        self.cwd = Path.cwd()
        os.chdir(self.source)
    def tearDown(self):
        os.chdir(self.cwd)
        self.stack.close()
        self.temp.cleanup()
    def docker(self, *args, **kwargs):
        self.commands.append(('docker', args))
        out = b''
        if args[0] == 'compose':
            if 'ps' in args:
                out = (self.info['Id'] + '\n').encode()
            elif '--hash' in args:
                out = ('p2pstream ' + 'a' * 64 + '\n').encode()
            elif 'config' in args:
                self.config_reads += 1
                out = self.raw + (b' ' if self.drift and self.config_reads > 1 else b'')
                if self.managed and (self.state / 'config.json').exists() and '--environment' not in args:
                    paths = [value for index, value in enumerate(args) if index and args[index - 1] in {'-f', '--file'}]
                    editable = any('.p2pstream-input-' in value for value in paths)
                    bridge = any(Path(value).exists() and b'"include"' in Path(value).read_bytes() for value in paths) if paths else (self.source / 'compose.yaml').exists() and b'"include"' in (self.source / 'compose.yaml').read_bytes()
                    cfg = host.read_json(self.state / 'config.json')
                    if not editable and (bridge or cfg.get('deployment_file') in paths):
                        out = Path(cfg['deployment_file']).read_bytes()
        elif args[0] == 'inspect':
            out = json.dumps([self.info]).encode()
        elif args[0:2] == ('image', 'inspect'):
            out = json.dumps([self.helper if args[-1] == HELPER else self.image]).encode()
        elif args[0] == 'exec' and args[-1] in {'server-deployment-capabilities', 'server-enrollment-capabilities'}:
            out = json.dumps({'runtime_guard': 2, 'enrollment_preflight': 1, 'version': self.args.expect_version, 'commit': self.args.expect_commit}).encode()
        elif args[0] == 'exec' and args[-1] == 'server-enrollment-preflight':
            out = json.dumps({'api': 1, 'installation_id': self.identity, 'version': self.args.expect_version, 'commit': self.args.expect_commit, 'ready': True, 'blockers': []}).encode()
        elif args[0] == 'exec':
            out = (self.identity + '\n').encode() if args[-1] == 'server-installation-identity' else b'{}'
        elif args[0] == 'ps':
            out = b'' if self.server_inventory == b'' or any('host-helper=' in str(value) or 'server-update.executor=' in str(value) for value in args) else (self.info['Id'] + '\n').encode()
            if self.foreign_writer and any('volume=' in str(value) for value in args):
                out += b'foreign-writer\n'
        if args[0] == 'inspect' and args[-1] == 'foreign-writer':
            out = json.dumps([{'Config': {'Labels': {}}, 'Mounts': [{'Name': 'custom-data', 'RW': True}]}]).encode()
        return subprocess.CompletedProcess(args, 0, out, b'')
    def install(self):
        host.install(self.args, self.options)
    def detach(self):
        self.detached = True
        self.info['Id'] = 'native-container'
        self.info['Config']['Labels']['com.docker.compose.config-hash'] = 'a' * 64
        self.info['Config']['Labels'].pop('p2pstream.server-update.instance', None)
    def installed_repair(self):
        spec = importlib.util.spec_from_file_location('installed_recovery', self.state / 'server-updater-host.py')
        installed = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(installed)
        outer = self
        fixture = host.Host
        class InstalledHost(installed.Host):
            def __init__(self):
                super().__init__(outer.state)
            command = fixture.command
            compose = fixture.compose
            status_socket = fixture.status_socket
            wait_socket = fixture.wait_socket
            healthy = fixture.healthy
            wait_healthy = fixture.wait_healthy
            wait_original = fixture.wait_original
            def data_identity(self):
                return outer.identity
        with patch.object(installed, 'STATE', self.state), patch.object(installed, 'Host', InstalledHost), patch.object(installed, 'docker', self.docker), patch.object(installed, 'local_engine', return_value='amd64'), patch.object(installed, 'Failure', host.Failure), patch.object(installed, 'UnsettledHelper', host.UnsettledHelper):
            installed.manage(argparse.Namespace(action='repair'))
    def custom_removed_deployment(self):
        (self.source / 'compose.yaml').unlink()
        custom = self.source / 'custom.yaml'
        custom.write_text('fixture input')
        self.options[3] = str(custom)
        self.managed = True
        self.install()
        current_image = 'ghcr.io/test/repo@sha256:' + 'e' * 64
        controller = host.Host()
        with controller.lock():
            model = host.read_json(controller.current_model_path())
            model['services']['p2pstream']['image'] = current_image
            model['services']['p2pstream']['environment']['SECRET'] = 'exported-current-settings'
            controller.write_model(model)
            controller.runtime('block')
            controller.deploy_server()
        host.manage(argparse.Namespace(action='remove'))
        self.raw = custom.read_bytes()
        return current_image
    def test_fresh_custom_context_and_idempotent_rerun(self):
        self.install()
        cfg = (self.state / 'config.json').read_bytes()
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['compose_options'], self.options)
        self.assertEqual(journal['phase'], 'healthy')
        self.assertEqual(self.restart_count, 1)
        self.install()
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
        self.assertEqual(self.restart_count, 1)
        self.assertFalse(any(item[0] == 'docker' and item[1][0] == 'build' for item in self.commands))

    def test_managed_install_publishes_private_model_and_preserves_editable_sources(self):
        self.managed = True
        originals = {path: path.read_bytes() for path in self.source.iterdir()}
        self.install()
        cfg = host.read_json(self.state / 'config.json')
        published = Path(cfg['deployment_file'])
        self.assertEqual(cfg['deployment_protocol'], 2)
        self.assertEqual(published.stat().st_mode & 0o777, 0o640)
        self.assertEqual(published.parent.stat().st_mode & 0o7777, 0o2750)
        self.assertNotIn(cfg['token'].encode(), published.read_bytes())
        journal = host.read_json(self.state / 'host.json')
        self.assertIn('include', host.read_json(self.source / 'compose.yaml'))
        self.assertEqual(host.read_json(self.source / 'prod.yaml'), {})
        for item in journal['source_bridge']['files']:
            self.assertEqual(Path(item['editable']).read_bytes(), originals[Path(item['path'])])
        runtime = [item[1][0] for item in self.commands if item[0] == 'binary' and item[1][0].startswith('runtime-')]
        self.assertEqual(runtime, ['runtime-block', 'runtime-allow'])
        self.assertEqual(self.restart_count, 1)
        self.install()
        self.assertEqual(self.restart_count, 1)

    def test_managed_apply_rotates_generation_and_remove_exports_current_private_settings(self):
        self.managed = True
        self.install()
        cfg = host.read_json(self.state / 'config.json')
        before = host.read_json(Path(cfg['deployment_file']))
        candidate = copy.deepcopy(self.model)
        candidate['services']['p2pstream']['environment']['SECRET'] = 'new-settings'
        self.raw = json.dumps(candidate).encode()
        host.manage(argparse.Namespace(action='apply'))
        after = host.read_json(Path(cfg['deployment_file']))
        self.assertNotEqual(after['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION'], before['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION'])
        self.assertEqual(after['services']['p2pstream']['environment']['SECRET'], 'new-settings')
        host.manage(argparse.Namespace(action='remove'))
        exported = host.read_json(self.source / 'compose.yaml')
        self.assertEqual(exported['services']['p2pstream']['environment']['SECRET'], 'new-settings')
        self.assertEqual(exported['services']['p2pstream']['image'], REF)
        self.assertEqual((self.source / 'compose.yaml').stat().st_mode & 0o777, 0o600)
        self.assertNotIn('p2pstream-server-updater', exported['services'])
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'removed')

    def test_managed_source_migration_resumes_after_partial_bridge_write(self):
        self.managed = True
        original_atomic = host.atomic
        def interrupted(path, data, *args, **kwargs):
            if path == self.source / 'prod.yaml' and data == b'{}':
                raise KeyboardInterrupt()
            return original_atomic(path, data, *args, **kwargs)
        with patch.object(host, 'atomic', interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.install()
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['phase'], 'activating')
        self.assertTrue(all(Path(item['editable']).exists() for item in journal['source_bridge']['files']))
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual(self.restart_count, 1)

    def test_managed_source_migration_resumes_after_plan_before_first_copy(self):
        self.managed = True
        original_phase = host.Host.phase
        def interrupted(controller, phase, **values):
            original_phase(controller, phase, **values)
            if 'source_bridge' in values:
                raise KeyboardInterrupt()
        with patch.object(host.Host, 'phase', interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.install()
        journal = host.read_json(self.state / 'host.json')
        self.assertFalse(any(Path(item['editable']).exists() for item in journal['source_bridge']['files']))
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual(self.restart_count, 1)

    def test_managed_bridge_unknown_source_refuses_before_any_other_source_write(self):
        self.managed = True
        self.install()
        (self.source / 'prod.yaml').write_text('operator change')
        original = (self.source / 'compose.yaml').read_bytes()
        controller = host.Host()
        with controller.lock():
            with self.assertRaisesRegex(host.Failure, 'source changed during bridge migration'):
                controller.source_bridges()
        self.assertEqual((self.source / 'compose.yaml').read_bytes(), original)
        self.assertEqual((self.source / 'prod.yaml').read_text(), 'operator change')

    def test_custom_sources_get_default_facade_without_overwriting_unrelated_default(self):
        (self.source / 'compose.yaml').unlink()
        custom = self.source / 'custom.yaml'
        custom.write_text('fixture input')
        self.options[3] = str(custom)
        self.managed = True
        self.install()
        self.assertIn('include', host.read_json(self.source / 'compose.yaml'))
        self.assertIn('include', host.read_json(custom))
        self.assertEqual(host.read_json(self.source / 'prod.yaml'), {})

    def test_custom_remove_reenroll_preserves_alias_ownership_across_archive_commit(self):
        current_image = self.custom_removed_deployment()
        metadata = (self.manifest, self.descriptor, current_image, 'f' * 64)
        with patch.object(host, 'release_metadata', return_value=metadata):
            with patch.object(host.Host, 'finish_archive', side_effect=KeyboardInterrupt()):
                with self.assertRaises(KeyboardInterrupt):
                    self.install()
            self.assertFalse((self.state / 'host.json').exists())
            self.assertTrue((self.state / 'archive.json').exists())
            receipt = host.read_json(self.state / 'reenrollment.json')
            self.assertEqual(receipt['aliases'][str(self.source / 'compose.yaml')], host.digest((self.source / 'compose.yaml').read_bytes()))
            self.assertEqual(receipt['compose_options'], self.options)
            self.install()
        cfg = host.read_json(self.state / 'config.json')
        current = host.read_json(Path(cfg['deployment_file']))
        self.assertEqual(current['services']['p2pstream']['image'], current_image)
        self.assertEqual(current['services']['p2pstream']['environment']['SECRET'], 'exported-current-settings')
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertFalse((self.state / 'reenrollment.json').exists())
        self.assertIn('include', host.read_json(self.source / 'compose.yaml'))

    def test_custom_remove_reenroll_preserves_receipt_after_archive_cleanup_before_new_journal(self):
        current_image = self.custom_removed_deployment()
        original = host.Host.phase
        def interrupted(controller, phase, **values):
            if phase == 'preparing':
                raise KeyboardInterrupt()
            return original(controller, phase, **values)
        metadata = (self.manifest, self.descriptor, current_image, 'f' * 64)
        with patch.object(host, 'release_metadata', return_value=metadata):
            with patch.object(host.Host, 'phase', interrupted):
                with self.assertRaises(KeyboardInterrupt):
                    self.install()
            self.assertFalse((self.state / 'host.json').exists())
            self.assertFalse((self.state / 'archive.json').exists())
            self.assertTrue((self.state / 'reenrollment.json').exists())
            self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertFalse((self.state / 'reenrollment.json').exists())

    def test_reenrollment_receipt_refuses_modified_or_symlink_default_alias(self):
        current_image = self.custom_removed_deployment()
        controller = host.Host()
        with controller.lock():
            controller.archive_removed()
        alias = self.source / 'compose.yaml'
        exported = alias.read_bytes()
        alias.write_bytes(exported + b' ')
        metadata = (self.manifest, self.descriptor, current_image, 'f' * 64)
        with patch.object(host, 'release_metadata', return_value=metadata):
            with self.assertRaisesRegex(host.Failure, 'Exported default Compose alias changed'):
                self.install()
            alias.unlink()
            alias.symlink_to(self.source / 'missing-target.yaml')
            with self.assertRaisesRegex(host.Failure, 'Exported default Compose alias changed'):
                self.install()
            self.assertFalse((self.state / 'host.json').exists())
            alias.unlink()
            alias.symlink_to(self.source / 'custom.yaml')
            with self.assertRaisesRegex(host.Failure, 'Exported default Compose alias changed'):
                self.install()
        self.assertTrue(alias.is_symlink())
        self.assertTrue((self.state / 'reenrollment.json').exists())

    def missing_alias_reenrollment(self, archived):
        current_image = self.custom_removed_deployment()
        if archived:
            controller = host.Host()
            with controller.lock():
                controller.archive_removed()
        alias = self.source / 'compose.yaml'
        alias.unlink()
        with patch.object(host, 'release_metadata', return_value=(self.manifest, self.descriptor, current_image, 'f' * 64)):
            self.install()
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['phase'], 'healthy')
        self.assertIsNone(journal['source_layout']['existing_alias_sha256'])
        created_alias = next(item for item in journal['source_bridge']['files'] if item['path'] == str(alias))
        self.assertIsNone(created_alias['original_sha256'])
        self.assertIsNone(created_alias['original_backup'])
        self.assertIn('include', host.read_json(alias))
        cfg = host.read_json(self.state / 'config.json')
        self.assertEqual(host.read_json(Path(cfg['deployment_file']))['services']['p2pstream']['image'], current_image)

    def test_reenrollment_recreates_deleted_alias_before_archive(self):
        self.missing_alias_reenrollment(archived=False)

    def test_reenrollment_recreates_deleted_alias_from_durable_receipt(self):
        self.missing_alias_reenrollment(archived=True)

    def test_missing_alias_does_not_relax_receipt_path_or_export_hash_binding(self):
        self.custom_removed_deployment()
        controller = host.Host()
        with controller.lock():
            controller.archive_removed()
        context = host.read_json(self.state / 'reenrollment.json')
        alias = self.source / 'compose.yaml'
        alias.unlink()
        self.assertEqual(controller.validate_reenrollment_context(context), {})
        for aliases in [{'relative-compose.yaml': context['export_sha256']}, {str(alias): '0' * 64}]:
            with self.subTest(aliases=aliases):
                invalid = dict(context, aliases=aliases)
                with self.assertRaisesRegex(host.Failure, 'Exported default Compose alias changed'):
                    controller.validate_reenrollment_context(invalid)

    def test_reenrollment_accepts_applied_primary_edits_but_refuses_unapplied_input_drift(self):
        current_image = self.custom_removed_deployment()
        controller = host.Host()
        with controller.lock():
            controller.archive_removed()
        primary = self.source / 'custom.yaml'
        alias = self.source / 'compose.yaml'
        alias_before = alias.read_bytes()
        model = host.read_json(primary)
        model['services']['p2pstream']['environment']['SECRET'] = 'operator-applied-current-settings'
        host.atomic(primary, model)
        self.raw = primary.read_bytes()
        original_docker = self.docker
        def updated_hash(*args, **kwargs):
            if args[0] == 'compose' and '--hash' in args:
                return subprocess.CompletedProcess(args, 0, ('p2pstream ' + 'b' * 64 + '\n').encode(), b'')
            return original_docker(*args, **kwargs)
        metadata = (self.manifest, self.descriptor, current_image, 'f' * 64)
        with patch.object(host, 'release_metadata', return_value=metadata), patch.object(host, 'docker', updated_hash):
            with self.assertRaisesRegex(host.Failure, 'Compose inputs differ from the running server'):
                self.install()
            self.assertEqual(alias.read_bytes(), alias_before)
            self.assertFalse((self.state / 'host.json').exists())
            # Model the operator's ordinary Compose application: now the live
            # container carries the new selected source hash, with the known
            # extra default alias still exactly the previous exported bytes.
            self.info['Config']['Labels']['com.docker.compose.config-hash'] = 'b' * 64
            self.install()
        cfg = host.read_json(self.state / 'config.json')
        self.assertEqual(host.read_json(Path(cfg['deployment_file']))['services']['p2pstream']['environment']['SECRET'], 'operator-applied-current-settings')
        self.assertFalse((self.state / 'reenrollment.json').exists())
        self.assertIn('include', host.read_json(alias))

    def test_active_host_journal_takes_precedence_over_stale_reenrollment_receipt(self):
        self.install()
        host.atomic(self.state / 'reenrollment.json', {'installation': 'other'})
        self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_unrelated_default_or_automatic_override_rejected_before_state_changes(self):
        self.managed = True
        automatic = self.source / 'compose.override.yaml'
        automatic.write_text('operator settings')
        with self.assertRaisesRegex(host.Failure, 'unrelated automatic Compose override'):
            self.install()
        self.assertFalse((self.state / 'config.json').exists())
        self.assertEqual(automatic.read_text(), 'operator settings')

    def test_unrelated_default_file_is_never_adopted_for_custom_input_context(self):
        custom = self.source / 'custom.yaml'
        custom.write_text('fixture input')
        self.options[3] = str(custom)
        original_default = (self.source / 'compose.yaml').read_bytes()
        with self.assertRaisesRegex(host.Failure, 'unrelated default Compose file'):
            self.install()
        self.assertEqual((self.source / 'compose.yaml').read_bytes(), original_default)
        self.assertFalse((self.state / 'config.json').exists())

    def test_default_compose_environment_must_select_recorded_inputs_and_project(self):
        original_docker = self.docker
        for output in [b'COMPOSE_FILE=unrelated.yaml\n', b'COMPOSE_PROJECT_NAME=other\n']:
            def environment(*args, **kwargs):
                if '--environment' in args:
                    return subprocess.CompletedProcess(args, 0, output, b'')
                return original_docker(*args, **kwargs)
            with self.subTest(output=output), patch.object(host, 'docker', environment):
                with self.assertRaisesRegex(host.Failure, 'Default COMPOSE_'):
                    self.install()
                self.assertFalse((self.state / 'config.json').exists())

    def test_custom_project_probe_anchors_derived_name_without_using_project_flag(self):
        original_docker = self.docker
        observed = []
        def environment(*args, **kwargs):
            if '--environment' in args:
                self.assertNotIn('-p', args)
                self.assertNotIn('--project-name', args)
                inputs = [value for index, value in enumerate(args) if index and args[index - 1] == '-f']
                self.assertEqual(len(inputs), 3)
                self.assertEqual(host.read_json(Path(inputs[-1])), {'name': 'custom-project'})
                observed.append(inputs[-1])
                return subprocess.CompletedProcess(args, 0, b'COMPOSE_PROJECT_NAME=custom-project\n', b'')
            return original_docker(*args, **kwargs)
        with patch.object(host, 'docker', environment):
            self.install()
        self.assertEqual(len(observed), 1)
        self.assertFalse(Path(observed[0]).exists())

    def test_incompatible_migration_capabilities_preserve_legacy_state_and_sources(self):
        self.install()
        cfg = (self.state / 'config.json').read_bytes()
        sources = {path: path.read_bytes() for path in self.source.iterdir()}
        self.args.migrate_deployment = True
        with patch.object(host.Host, 'capability', side_effect=host.Failure('manually deploy a compatible published server')):
            with self.assertRaisesRegex(host.Failure, 'manually deploy'):
                self.install()
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
        self.assertEqual({path: path.read_bytes() for path in self.source.iterdir()}, sources)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_legacy_deployment_migrates_only_explicitly_preserving_current_identity(self):
        self.install()
        old_cfg = host.read_json(self.state / 'config.json')
        self.assertNotIn('deployment_protocol', old_cfg)
        self.install()
        self.assertEqual(self.restart_count, 1)
        self.managed = True
        self.args.migrate_deployment = True
        self.install()
        cfg = host.read_json(self.state / 'config.json')
        self.assertEqual(cfg['token'], old_cfg['token'])
        self.assertEqual(cfg['instance_id'], old_cfg['instance_id'])
        self.assertEqual(cfg['data_volume'], old_cfg['data_volume'])
        self.assertEqual(cfg['deployment_protocol'], 2)
        self.assertEqual(self.restart_count, 2)
        self.assertNotIn(cfg['token'].encode(), Path(cfg['deployment_file']).read_bytes())

    def test_verified_migration_repairs_broken_legacy_binding_before_transition(self):
        self.install()
        old_cfg = host.read_json(self.state / 'config.json')
        self.detach()
        self.managed = True
        self.args.migrate_deployment = True
        self.install()
        cfg = host.read_json(self.state / 'config.json')
        self.assertEqual(cfg['deployment_protocol'], 2)
        self.assertEqual(cfg['token'], old_cfg['token'])
        self.assertEqual(cfg['instance_id'], old_cfg['instance_id'])
        self.assertEqual(cfg['data_volume'], old_cfg['data_volume'])
        self.assertEqual(self.restart_count, 3)  # Exact repair, then explicit migration.
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_verified_ordinary_install_hands_off_controller_without_upgrading_executor(self):
        self.install()
        cfg = (self.state / 'config.json').read_bytes()
        old_controller = b'# old controller does not know the recreating journal phase\n'
        host.atomic(self.state / 'server-updater-host.py', old_controller)
        launcher = (self.state / 'manage').read_bytes()
        model = (self.state / 'compose.json').read_bytes()
        self.detach()
        before = len(self.commands)
        self.install()
        self.assertEqual(self.restart_count, 2)
        self.assertFalse(self.detached)
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
        self.assertEqual((self.state / 'compose.json').read_bytes(), model)
        self.assertEqual((self.state / 'server-updater-host.py').read_bytes(), (self.bundle / 'server-updater-host.py').read_bytes())
        self.assertEqual((self.state / 'manage').read_bytes(), launcher)
        self.assertFalse(any(item[0] == 'docker' and item[1][0] == 'pull' for item in self.commands[before:]))

    def test_delivery_repair_interruption_before_handoff_preserves_old_phase_and_server(self):
        self.install()
        old = b'# installed legacy controller\n'
        host.atomic(self.state / 'server-updater-host.py', old)
        self.detach()
        original = host.atomic
        def interrupted(path, *args, **kwargs):
            if path == self.state / 'server-updater-host.py':
                raise KeyboardInterrupt()
            return original(path, *args, **kwargs)
        with patch.object(host, 'atomic', interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.install()
        self.assertEqual((self.state / 'server-updater-host.py').read_bytes(), old)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual(self.restart_count, 1)
        self.assertTrue(self.info['State']['Running'])

    def test_delivery_repair_handoff_precedes_new_phase_and_installed_controller_can_resume(self):
        self.install()
        host.atomic(self.state / 'server-updater-host.py', b'# legacy controller\n')
        self.detach()
        original = host.Host.phase
        def interrupted(controller, phase, **values):
            if phase == 'recreating':
                self.assertEqual((self.state / 'server-updater-host.py').read_bytes(), (self.bundle / 'server-updater-host.py').read_bytes())
                raise KeyboardInterrupt()
            return original(controller, phase, **values)
        with patch.object(host.Host, 'phase', interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.installed_repair()
        self.assertEqual(self.restart_count, 2)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_delivery_repair_installed_controller_recovers_stopped_or_missing_current_model(self):
        self.install()
        current = host.read_json(self.state / 'compose.json')
        current_image = 'ghcr.io/test/repo@sha256:' + 'e' * 64
        current['services']['p2pstream']['image'] = current_image
        host.atomic(self.state / 'compose.json', current)
        cfg = (self.state / 'config.json').read_bytes()
        original = host.Host.compose
        for missing in [False, True]:
            with self.subTest(missing=missing):
                host.atomic(self.state / 'server-updater-host.py', b'# obsolete legacy recovery\n')
                self.detach()
                def interrupted(controller, *args, **kwargs):
                    if args[0] == 'up' and args[-1] == 'p2pstream':
                        self.info['State']['Running'] = False
                        self.server_inventory = b'' if missing else None
                        raise KeyboardInterrupt()
                    return original(controller, *args, **kwargs)
                with patch.object(host.Host, 'compose', interrupted):
                    with self.assertRaises(KeyboardInterrupt):
                        self.install()
                self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'recreating')
                self.assertEqual((self.state / 'server-updater-host.py').read_bytes(), (self.bundle / 'server-updater-host.py').read_bytes())
                self.installed_repair()
                self.assertEqual(host.decode(self.models_started[-1])['services']['p2pstream']['image'], current_image)
                self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
                self.assertEqual(host.read_json(self.state / 'enrolled-compose.json')['services']['p2pstream']['image'], REF)
                self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_delivery_repair_rejects_mismatched_bundle_before_handoff_or_phase(self):
        self.install()
        old = b'# legacy recovery controller\n'
        host.atomic(self.state / 'server-updater-host.py', old)
        self.detach()
        controller = self.bundle / 'server-updater-host.py'
        controller.write_bytes(controller.read_bytes() + b'\n')
        with self.assertRaisesRegex(host.Failure, 'differs from the executing verified'):
            self.install()
        self.assertEqual((self.state / 'server-updater-host.py').read_bytes(), old)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual(self.restart_count, 1)

    def test_delivery_repair_rejects_special_bundle_member_before_controller_handoff(self):
        self.install()
        old = b'# installed legacy controller\n'
        host.atomic(self.state / 'server-updater-host.py', old)
        self.detach()
        member = self.bundle / '.env.example'
        outside = self.path / 'outside-env-example'
        outside.write_bytes(member.read_bytes())
        member.unlink()
        member.symlink_to(outside)
        with self.assertRaisesRegex(host.Failure, 'Unsafe or incomplete verified recovery bundle'):
            self.install()
        self.assertEqual((self.state / 'server-updater-host.py').read_bytes(), old)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual(self.restart_count, 1)

    def test_verified_install_does_not_apply_legacy_recovery_to_protocol_two(self):
        self.managed = True
        self.install()
        self.detach()
        with self.assertRaisesRegex(host.Failure, 'outside the enrolled deployment'):
            self.install()
        self.assertEqual(self.restart_count, 1)

    def test_verified_migration_refuses_legacy_drift_before_repair_or_tool_replacement(self):
        self.install()
        controller = (self.state / 'server-updater-host.py').read_bytes()
        config = (self.state / 'config.json').read_bytes()
        self.detach()
        self.args.migrate_deployment = True
        self.info['Image'] = 'sha256:other'
        with self.assertRaisesRegex(host.Failure, 'Running image changed'):
            self.install()
        self.assertEqual(self.restart_count, 1)
        self.assertEqual((self.state / 'server-updater-host.py').read_bytes(), controller)
        self.assertEqual((self.state / 'config.json').read_bytes(), config)

    def test_verified_migration_never_repairs_during_active_update(self):
        self.install()
        self.detach()
        self.args.migrate_deployment = True
        host.atomic(self.state / 'state.json', {'operation': {'id': 'active', 'phase': 'validating'}})
        with self.assertRaisesRegex(host.Failure, 'paused data recovery is active'):
            self.install()
        self.assertEqual(self.restart_count, 1)

    def test_legacy_migration_resumes_same_conversion_after_source_interruption(self):
        self.install()
        old_token = host.read_json(self.state / 'config.json')['token']
        self.managed = True
        self.args.migrate_deployment = True
        original_atomic = host.atomic
        def interrupted(path, data, *args, **kwargs):
            if path == self.source / 'prod.yaml' and data == b'{}':
                raise KeyboardInterrupt()
            return original_atomic(path, data, *args, **kwargs)
        with patch.object(host, 'atomic', interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'migrating_deployment')
        generation = host.read_json(self.state / 'host.json')['deployment_migration']['generation']
        host.manage(argparse.Namespace(action='repair'))
        cfg = host.read_json(self.state / 'config.json')
        self.assertEqual(cfg['token'], old_token)
        self.assertEqual(host.read_json(Path(cfg['deployment_file']))['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION'], generation)
        self.assertEqual(self.restart_count, 2)
    def test_download_unavailable_precedes_state_writes(self):
        with patch.object(host, 'release_metadata', side_effect=host.Failure('Release download unavailable')):
            with self.assertRaisesRegex(host.Failure, 'download unavailable'):
                self.install()
        self.assertFalse(self.state.exists())
        self.assertEqual(self.restart_count, 0)
    def test_wrong_host_and_stale_command_precede_state_writes(self):
        self.identity = '22222222-2222-4222-8222-222222222222'
        with self.assertRaisesRegex(host.Failure, 'Wrong Docker host'):
            self.install()
        self.assertFalse(self.state.exists())
        self.identity = ID
        self.image['Config']['Labels']['org.opencontainers.image.version'] = 'v1.0.2'
        with self.assertRaisesRegex(host.Failure, 'Stale command'):
            self.install()
        self.assertFalse(self.state.exists())
    def test_deployment_changed_during_downloads_is_rejected(self):
        self.drift = True
        with self.assertRaisesRegex(host.Failure, 'changed during dependency'):
            self.install()
        self.assertFalse((self.state / 'config.json').exists())
        self.assertEqual(self.restart_count, 0)
    def test_failed_activation_restores_pinned_original_then_repair_preserves_token(self):
        self.failure = 'health'
        with self.assertRaisesRegex(host.Failure, 'Recovery: sudo'):
            self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'rolled_back')
        cfg = (self.state / 'config.json').read_bytes()
        previous = host.read_json(self.state / 'compose.json')
        self.assertEqual(previous['services']['p2pstream']['image'], REF)
        self.assertNotIn('p2pstream-server-updater', previous['services'])
        self.assertIn(('compose', ('stop', 'p2pstream-server-updater'), 'enrolled-compose.json'), self.commands)
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
    def test_interrupted_after_recreation_resumes_without_duplicate_restart(self):
        self.failure = 'crash'
        with self.assertRaises(KeyboardInterrupt):
            self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'activating')
        cfg = (self.state / 'config.json').read_bytes()
        self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
        self.assertEqual(self.restart_count, 1)

    def interrupt_activation_replacement(self, *, rolling_back=False):
        self.managed = True
        self.failure = 'health' if rolling_back else ''
        original = host.Host.compose
        def interrupted(controller, *args, **kwargs):
            if args[0] == 'up' and '--no-start' in args and args[-1] == 'p2pstream' and (not rolling_back or controller.journal['phase'] == 'rolling_back'):
                # Compose config-change recreation can remove the old container
                # and fail before creating its replacement, without --force-recreate.
                self.assertFalse(self.info['State']['Running'])
                self.commands.append(('compose', args, kwargs.get('model', 'compose.json')))
                self.server_inventory = b''
                raise host.Failure('Compose replacement failed after old container removal')
            return original(controller, *args, **kwargs)
        with patch.object(host.Host, 'compose', interrupted):
            with self.assertRaisesRegex(host.Failure, 'Compose replacement failed|health failure'):
                self.install()
        self.assertEqual(self.server_inventory, b'')

    def assert_no_server_lifecycle(self, commands):
        self.assertFalse(any(item[0] == 'compose' and item[1][0] in {'up', 'stop', 'start'} and item[1][-1] == 'p2pstream' for item in commands))

    def test_missing_activation_replacement_repairs_through_failure_recovery(self):
        self.interrupt_activation_replacement()
        journal = host.read_json(self.state / 'host.json')
        self.assertNotIn('recovery_phase', journal)
        cfg = (self.state / 'config.json').read_bytes()
        model = Path(host.decode(cfg)['deployment_file']).read_bytes()
        # The public rollback dispatch must refuse absence without erasing the
        # interrupted activation that the public repair dispatch can resume.
        before_rollback = len(self.commands)
        with self.assertRaisesRegex(host.Failure, 'refusing to recreate a missing deployment'):
            host.manage(argparse.Namespace(action='rollback'))
        rollback_journal = host.read_json(self.state / 'host.json')
        self.assert_no_server_lifecycle(self.commands[before_rollback:])
        host.atomic(self.state / 'helper.json', {'name': 'p2pstream-server-update-host-' + ID, 'phase': 'activating', 'readonly': True})
        base = host.Host.__mro__[1]
        def identity(*args, **kwargs):
            self.assertFalse((self.state / 'helper.json').exists())
            return subprocess.CompletedProcess([], 0, (ID + '\n').encode(), b'')
        with patch.object(base, 'run_helper', side_effect=identity) as helper:
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(journal['phase'], 'activating')
        self.assertEqual(rollback_journal['phase'], 'activating')
        self.assertTrue(helper.call_args.kwargs['readonly'])
        self.assertIn('type=volume,src=custom-data,dst=/server-data,readonly', helper.call_args.kwargs['mounts'])
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
        self.assertEqual(Path(host.decode(cfg)['deployment_file']).read_bytes(), model)
        self.assertEqual(self.restart_count, 1)
        self.assertEqual(len(self.models_started), 1)
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 1)

    def test_missing_activation_replacement_can_be_interrupted_repeatedly(self):
        self.interrupt_activation_replacement()
        original = host.Host.compose
        def interrupted(controller, *args, **kwargs):
            if args[0] == 'up' and '--no-start' in args and args[-1] == 'p2pstream':
                self.commands.append(('compose', args, kwargs.get('model', 'compose.json')))
                self.server_inventory = b''
                raise host.Failure('Repeated replacement interruption')
            return original(controller, *args, **kwargs)
        with patch.object(host.Host, 'data_identity', return_value=ID):
            for _ in range(2):
                before = len(self.commands)
                with patch.object(host.Host, 'compose', interrupted):
                    with self.assertRaisesRegex(host.Failure, 'Repeated replacement interruption'):
                        host.manage(argparse.Namespace(action='repair'))
                self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'activating')
                self.assertEqual(sum(item[0] == 'compose' and item[1][0] == 'up' for item in self.commands[before:]), 1)
                self.assertEqual(self.restart_count, 0)
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 1)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_missing_activation_does_not_adopt_bare_stale_recovery(self):
        self.interrupt_activation_replacement()
        journal = host.read_json(self.state / 'host.json')
        journal['phase'] = 'recovery_required'
        host.atomic(self.state / 'host.json', journal)
        before = len(self.commands)
        with patch.object(host.Host, 'data_identity', return_value=ID) as identity:
            with self.assertRaisesRegex(host.Failure, 'refusing to recreate a missing deployment'):
                host.manage(argparse.Namespace(action='repair'))
        identity.assert_not_called()
        self.assert_no_server_lifecycle(self.commands[before:])

    def test_missing_activation_does_not_reinterpret_helper_recovery_phase(self):
        self.interrupt_activation_replacement()
        journal = host.read_json(self.state / 'host.json')
        journal.update(phase='recovery_required', recovery_phase='activating')
        host.atomic(self.state / 'host.json', journal)
        before = len(self.commands)
        with patch.object(host.Host, 'data_identity', return_value=ID) as identity:
            with self.assertRaisesRegex(host.Failure, 'refusing to recreate a missing deployment'):
                host.manage(argparse.Namespace(action='repair'))
        identity.assert_not_called()
        self.assertEqual(host.read_json(self.state / 'host.json'), journal)
        self.assert_no_server_lifecycle(self.commands[before:])

    def test_missing_activation_does_not_infer_absence_from_failed_inventory(self):
        self.interrupt_activation_replacement()
        original = host.Host.compose
        def unavailable(controller, *args, **kwargs):
            if args[0] == 'ps':
                raise host.Failure('Docker inventory unavailable')
            return original(controller, *args, **kwargs)
        before = len(self.commands)
        with patch.object(host.Host, 'compose', unavailable), patch.object(host.Host, 'data_identity', return_value=ID) as identity:
            with self.assertRaisesRegex(host.Failure, 'Docker inventory unavailable'):
                host.manage(argparse.Namespace(action='repair'))
        identity.assert_not_called()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'recovery_required')
        self.assert_no_server_lifecycle(self.commands[before:])

    def test_missing_activation_does_not_adopt_other_phases_or_protocol(self):
        self.interrupt_activation_replacement()
        original = host.read_json(self.state / 'host.json')
        cfg = host.read_json(self.state / 'config.json')
        for phase, protocol in [('prepared', 2), ('applying', 2), ('removing', 2), ('rolling_back', 2), ('rolled_back', 2), ('activating', 1)]:
            with self.subTest(phase=phase, protocol=protocol):
                host.atomic(self.state / 'host.json', dict(original, phase=phase))
                host.atomic(self.state / 'config.json', dict(cfg, deployment_protocol=protocol))
                before = len(self.commands)
                with self.assertRaises(host.Failure):
                    host.manage(argparse.Namespace(action='repair'))
                self.assert_no_server_lifecycle(self.commands[before:])
                self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'recovery_required' if phase in {'prepared', 'rolled_back', 'activating'} else phase)

    def test_missing_activation_refuses_changed_identity_and_foreign_writer(self):
        self.interrupt_activation_replacement()
        for identity, writer, message in [('wrong', False, 'Installation identity changed'), (ID, True, 'Another running container writes')]:
            with self.subTest(identity=identity, writer=writer):
                self.foreign_writer = writer
                before = len(self.commands)
                with patch.object(host.Host, 'data_identity', return_value=identity):
                    with self.assertRaisesRegex(host.Failure, message):
                        host.manage(argparse.Namespace(action='repair'))
                self.assert_no_server_lifecycle(self.commands[before:])
                self.assertEqual(self.restart_count, 0)
                self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'activating')

    def test_missing_activation_refuses_source_drift(self):
        self.interrupt_activation_replacement()
        self.raw += b' '
        before = len(self.commands)
        with patch.object(host.Host, 'data_identity', return_value=ID) as identity:
            with self.assertRaisesRegex(host.Failure, 'Compose inputs changed after setup was staged'):
                host.manage(argparse.Namespace(action='repair'))
        identity.assert_not_called()
        self.assert_no_server_lifecycle(self.commands[before:])

    def test_missing_activation_refuses_multiple_or_changed_image_containers(self):
        self.interrupt_activation_replacement()
        original = host.read_json(self.state / 'host.json')
        for inventory, image, message in [(b'one\ntwo\n', 'sha256:config', 'Server disappeared'), (b'container\n', 'sha256:external', 'Running image changed')]:
            with self.subTest(inventory=inventory):
                host.atomic(self.state / 'host.json', original)
                self.server_inventory = inventory
                self.info['Image'] = image
                before = len(self.commands)
                with self.assertRaisesRegex(host.Failure, message):
                    host.manage(argparse.Namespace(action='repair'))
                self.assert_no_server_lifecycle(self.commands[before:])
                self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'recovery_required')

    def test_missing_activation_never_preserves_provenance_after_rollback_started(self):
        self.interrupt_activation_replacement(rolling_back=True)
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['phase'], 'recovery_required')
        self.assertNotIn('recovery_phase', journal)
        current = Path(host.read_json(self.state / 'config.json')['deployment_file']).read_bytes()
        self.assertNotIn('p2pstream-server-updater', host.decode(current)['services'])
        before = len(self.commands)
        with self.assertRaisesRegex(host.Failure, 'refusing to recreate a missing deployment'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(Path(host.read_json(self.state / 'config.json')['deployment_file']).read_bytes(), current)
        self.assert_no_server_lifecycle(self.commands[before:])

    def test_missing_activation_explicit_rollback_failure_after_start_cannot_resume(self):
        self.interrupt_activation_replacement()
        # The old container becomes visible again, allowing rollback to start.
        # Its own Compose recreation then removes it and fails after model writes.
        self.server_inventory = None
        original = host.Host.compose
        def interrupted(controller, *args, **kwargs):
            if args[0] == 'up' and '--no-start' in args and args[-1] == 'p2pstream':
                self.assertEqual(controller.journal['phase'], 'rolling_back')
                self.commands.append(('compose', args, kwargs.get('model', 'compose.json')))
                self.server_inventory = b''
                raise host.Failure('Explicit rollback replacement failed after removal')
            return original(controller, *args, **kwargs)
        with patch.object(host.Host, 'compose', interrupted):
            with self.assertRaisesRegex(host.Failure, 'Explicit rollback replacement failed after removal'):
                host.manage(argparse.Namespace(action='rollback'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'recovery_required')
        current = Path(host.read_json(self.state / 'config.json')['deployment_file']).read_bytes()
        self.assertNotIn('p2pstream-server-updater', host.decode(current)['services'])
        before = len(self.commands)
        with self.assertRaisesRegex(host.Failure, 'refusing to recreate a missing deployment'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(Path(host.read_json(self.state / 'config.json')['deployment_file']).read_bytes(), current)
        self.assert_no_server_lifecycle(self.commands[before:])

    def test_missing_activation_does_not_resume_successful_enrollment_or_update_history(self):
        self.interrupt_activation_replacement()
        original = host.read_json(self.state / 'host.json')
        for values in [{'verified_version': 'v1.0.1'}, {'verified_commit': 'b' * 40}, {'deployment_migration': {'phase': 'started'}}, {'update_history': True}]:
            with self.subTest(values=values):
                host.atomic(self.state / 'host.json', dict(original, **values))
                if values.get('update_history'):
                    host.atomic(self.state / 'state.json', {'operation': {'phase': 'succeeded'}})
                before = len(self.commands)
                with patch.object(host.Host, 'data_identity', return_value=ID) as identity:
                    with self.assertRaises(host.Failure):
                        host.manage(argparse.Namespace(action='repair'))
                identity.assert_not_called()
                self.assert_no_server_lifecycle(self.commands[before:])
    def test_healthy_state_never_allows_installation_era_rollback(self):
        self.install()
        with self.assertRaisesRegex(host.Failure, 'cannot downgrade'):
            host.manage(argparse.Namespace(action='rollback'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
    def test_unreachable_socket_is_not_success(self):
        self.install()
        self.failure = 'socket'
        with self.assertRaisesRegex(host.Failure, 'socket unavailable'):
            self.install()
        with self.assertRaisesRegex(host.Failure, 'socket unavailable'):
            host.manage(argparse.Namespace(action='status'))
    def test_detached_same_image_repair_preserves_configuration_and_credentials(self):
        self.install()
        cfg = (self.state / 'config.json').read_bytes()
        model = (self.state / 'compose.json').read_bytes()
        self.detach()
        # A native Compose image tag is acceptable only because its immutable
        # running image ID still equals the current pinned image.
        self.info['Config']['Image'] = 'ghcr.io/test/repo:v1.0.1'
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 2)
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
        self.assertEqual((self.state / 'compose.json').read_bytes(), model)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertFalse(self.detached)
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 2)

    def test_managed_recreation_uses_current_image_without_touching_original_inputs(self):
        self.install()
        model = host.read_json(self.state / 'compose.json')
        current_image = 'ghcr.io/test/repo@sha256:' + 'e' * 64
        model['services']['p2pstream']['image'] = current_image
        host.atomic(self.state / 'compose.json', model)
        self.image['Id'] = self.info['Image'] = 'sha256:current'
        cfg = (self.state / 'config.json').read_bytes()
        sources = {path: path.read_bytes() for path in self.source.iterdir()}
        before = len(self.commands)
        host.manage(argparse.Namespace(action='recreate'))
        self.assertEqual(self.restart_count, 2)
        self.assertEqual(host.decode(self.models_started[-1])['services']['p2pstream']['image'], current_image)
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
        self.assertEqual({path: path.read_bytes() for path in self.source.iterdir()}, sources)
        self.assertFalse(any(item[0] == 'binary' and item[1][0] in {'enroll', 'recover'} for item in self.commands[before:]))

    def test_managed_recreate_accepts_verified_native_hash_and_rejects_unverified_drift(self):
        self.install()
        self.accepted_native_hash = 'e' * 64
        self.info['Config']['Labels']['com.docker.compose.config-hash'] = self.accepted_native_hash
        host.manage(argparse.Namespace(action='recreate'))
        self.assertEqual(self.restart_count, 2)
        self.info['Config']['Labels']['com.docker.compose.config-hash'] = 'f' * 64
        with self.assertRaisesRegex(host.Failure, 'unverified configuration hash'):
            host.manage(argparse.Namespace(action='recreate'))
        self.assertEqual(self.restart_count, 2)

    def test_operator_group_never_exposes_resolved_secrets_to_shared_primary_group(self):
        account = host.pwd.struct_passwd(('operator', 'x', 1001, 1001, '', '/home/operator', '/bin/bash'))
        other = host.pwd.struct_passwd(('other', 'x', 1002, 1001, '', '/home/other', '/bin/bash'))
        with patch.dict(os.environ, {'SUDO_UID': '1001', 'SUDO_GID': '1001'}), patch.object(host.pwd, 'getpwuid', return_value=account), patch.object(Path, 'lstat', side_effect=FileNotFoundError()), patch.object(host.grp, 'getgrgid', return_value=host.grp.struct_group(('users', 'x', 1001, []))):
            with patch.object(host.pwd, 'getpwall', return_value=[account, other]):
                self.assertEqual(host.operator_gid(), 0)
            with patch.object(host.pwd, 'getpwall', return_value=[account]):
                self.assertEqual(host.operator_gid(), 1001)
        socket_info = os.stat_result((stat.S_IFSOCK | 0o660, 0, 0, 1, 0, 999, 0, 0, 0, 0))
        with patch.dict(os.environ, {'SUDO_UID': '1001', 'SUDO_GID': '1001'}), patch.object(host.pwd, 'getpwuid', return_value=account), patch.object(Path, 'lstat', return_value=socket_info), patch.object(host.grp, 'getgrgid', return_value=host.grp.struct_group(('docker', 'x', 999, ['operator']))):
            self.assertEqual(host.operator_gid(), 999)

    def test_detached_repair_refuses_missing_or_ambiguous_server(self):
        self.install()
        self.detach()
        for inventory in [b'', b'one\ntwo\n']:
            self.server_inventory = inventory
            with self.assertRaisesRegex(host.Failure, 'exactly one existing server'):
                host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 1)

    def test_detached_repair_rejects_edited_inputs_instead_of_adopting_settings(self):
        self.install()
        self.detach()
        candidate = copy.deepcopy(self.model)
        candidate['services']['p2pstream']['environment']['SECRET'] = 'edited'
        self.raw = json.dumps(candidate).encode()
        with self.assertRaisesRegex(host.Failure, 'Restore the last applied inputs'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 1)
        self.assertTrue(self.detached)

    def test_detached_repair_rejects_changed_runtime_before_server_replacement(self):
        self.install()
        self.detach()
        original = copy.deepcopy(self.info)
        cases = [
            ('image', lambda: self.info.update(Image='sha256:other'), 'Running image changed'),
            ('project', lambda: self.info['Config']['Labels'].update({'com.docker.compose.project': 'other'}), 'another Compose deployment'),
            ('service', lambda: self.info['Config']['Labels'].update({'com.docker.compose.service': 'other'}), 'another Compose deployment'),
            ('hash', lambda: self.info['Config']['Labels'].update({'com.docker.compose.config-hash': 'd' * 64}), 'Running settings differ'),
            ('volume', lambda: self.info['Mounts'][0].update(Name='other'), 'data volume changed'),
            ('readonly', lambda: self.info['Mounts'][0].update(RW=False), 'data volume changed'),
            ('nested', lambda: self.info['Mounts'].append({'Destination': '/data/nested'}), 'Nested server data mounts'),
            ('stopped', lambda: self.info['State'].update(Running=False), 'Server is not running'),
        ]
        for name, change, message in cases:
            with self.subTest(name=name):
                self.info = copy.deepcopy(original)
                change()
                with self.assertRaisesRegex(host.Failure, message):
                    host.manage(argparse.Namespace(action='repair'))
                self.assertEqual(self.restart_count, 1)

    def test_recreation_refuses_foreign_writer_identity_and_unhealthy_server(self):
        self.install()
        self.detach()
        self.foreign_writer = True
        with self.assertRaisesRegex(host.Failure, 'Another running container writes'):
            host.manage(argparse.Namespace(action='repair'))
        self.foreign_writer = False
        self.identity = '22222222-2222-4222-8222-222222222222'
        with self.assertRaisesRegex(host.Failure, 'Installation identity changed'):
            host.manage(argparse.Namespace(action='repair'))
        self.identity = ID
        original_docker = self.docker
        def unhealthy(*args, **kwargs):
            if args[0] == 'exec' and args[-1] == 'server-health':
                return subprocess.CompletedProcess(args, 1, b'', b'')
            return original_docker(*args, **kwargs)
        with patch.object(host, 'docker', unhealthy):
            with self.assertRaisesRegex(host.Failure, 'Server health failed'):
                host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 1)

    def test_active_update_blocks_repair_and_managed_recreation_without_mutation(self):
        self.install()
        host.atomic(self.state / 'state.json', {'operation': {'id': 'active', 'phase': 'validating'}})
        before = len(self.commands)
        for action in ['repair', 'recreate']:
            with self.assertRaisesRegex(host.Failure, 'paused data recovery is active'):
                host.manage(argparse.Namespace(action=action))
        self.assertEqual(len(self.commands), before)
        self.assertEqual(self.restart_count, 1)

    def test_interrupted_recreation_resumes_canonical_replacement_without_restart(self):
        self.install()
        cfg = (self.state / 'config.json').read_bytes()
        self.detach()
        self.failure = 'crash'
        with self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='repair'))
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['phase'], 'recreating')
        self.assertEqual(journal['recreation']['container_id'], 'native-container')
        self.assertEqual(self.restart_count, 2)
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 2)
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_interrupted_before_replacement_resumes_only_recorded_same_image(self):
        self.install()
        original = host.Host.compose
        def interrupted(controller, *args, **kwargs):
            if args[0] == 'up' and args[-1] == 'p2pstream':
                raise KeyboardInterrupt()
            return original(controller, *args, **kwargs)
        with patch.object(host.Host, 'compose', interrupted):
            with self.assertRaises(KeyboardInterrupt):
                host.manage(argparse.Namespace(action='recreate'))
        self.assertEqual(self.restart_count, 1)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'recreating')
        self.info['Image'] = 'sha256:other'
        with self.assertRaisesRegex(host.Failure, 'Running image changed'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 1)
        self.info['Image'] = 'sha256:config'
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 2)

    def test_recreation_diagnostics_failure_cannot_use_installation_era_rollback(self):
        self.install()
        self.failure = 'crash'
        with self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='recreate'))
        journal = host.read_json(self.state / 'host.json')
        journal.update(phase='recovery_required', recovery_phase='recreating')
        host.atomic(self.state / 'host.json', journal)
        with self.assertRaisesRegex(host.Failure, 'no unfinished configuration transaction'):
            host.manage(argparse.Namespace(action='rollback'))
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 2)

    def test_interrupted_recreation_refuses_changed_model_credentials_or_replacement(self):
        self.install()
        self.failure = 'crash'
        with self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='recreate'))
        model = (self.state / 'compose.json').read_bytes()
        cfg = (self.state / 'config.json').read_bytes()
        for name in ['compose.json', 'config.json']:
            original = (self.state / name).read_bytes()
            host.atomic(self.state / name, original + b' ')
            with self.assertRaisesRegex(host.Failure, 'model or credentials changed'):
                host.manage(argparse.Namespace(action='repair'))
            host.atomic(self.state / name, original)
        self.info['Config']['Labels']['com.docker.compose.config-hash'] = 'd' * 64
        with self.assertRaisesRegex(host.Failure, 'Running settings differ'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 2)
        self.assertEqual((self.state / 'compose.json').read_bytes(), model)
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)

    def test_interrupted_recreation_reads_stopped_volume_identity_before_start(self):
        self.install()
        self.failure = 'crash'
        with self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='recreate'))
        self.info['State']['Running'] = False
        base = host.Host.__mro__[1]
        with patch.object(base, 'run_helper', return_value=subprocess.CompletedProcess([], 0, (ID + '\n').encode(), b'')) as helper:
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 2)
        self.assertTrue(helper.call_args.kwargs['readonly'])
        self.assertIn('type=volume,src=custom-data,dst=/server-data,readonly', helper.call_args.kwargs['mounts'])

    def test_interrupted_recreation_recovers_missing_container_only_with_proven_data(self):
        self.install()
        self.failure = 'crash'
        with self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='recreate'))
        self.server_inventory = b''
        base = host.Host.__mro__[1]
        with patch.object(base, 'run_helper', return_value=subprocess.CompletedProcess([], 0, b'wrong\n', b'')):
            with self.assertRaisesRegex(host.Failure, 'Installation identity changed'):
                host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 2)
        with patch.object(base, 'run_helper', return_value=subprocess.CompletedProcess([], 0, (ID + '\n').encode(), b'')):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 3)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_interrupted_recreation_does_not_infer_absence_from_failed_inventory(self):
        self.install()
        self.failure = 'crash'
        with self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='recreate'))
        original = host.Host.compose
        def unavailable(controller, *args, **kwargs):
            if args[0] == 'ps':
                raise host.Failure('daemon unavailable')
            return original(controller, *args, **kwargs)
        before = len(self.commands)
        with patch.object(host.Host, 'compose', unavailable):
            with self.assertRaisesRegex(host.Failure, 'daemon unavailable'):
                host.manage(argparse.Namespace(action='repair'))
        self.assertFalse(any(item[0] == 'compose' and item[1][0] in {'up', 'stop'} for item in self.commands[before:]))

    def test_interrupted_recreation_stopped_identity_mismatch_never_starts_server(self):
        self.install()
        self.failure = 'crash'
        with self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='recreate'))
        self.info['State']['Running'] = False
        before = len(self.commands)
        base = host.Host.__mro__[1]
        with patch.object(base, 'run_helper', return_value=subprocess.CompletedProcess([], 0, b'wrong\n', b'')):
            with self.assertRaisesRegex(host.Failure, 'Installation identity changed'):
                host.manage(argparse.Namespace(action='repair'))
        self.assertFalse(self.info['State']['Running'])
        self.assertFalse(any(item[0] == 'compose' and item[1][0] == 'up' and item[1][-1] == 'p2pstream' for item in self.commands[before:]))
    def test_concurrent_controller_does_not_activate(self):
        self.state.mkdir(mode=0o700)
        with open(self.state / 'host.lock', 'w') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(host.Failure, 'Another installation'):
                self.install()
        self.assertEqual(self.restart_count, 0)
    def test_removal_rollback_uses_pretransition_service_set(self):
        self.install()
        current = (self.state / 'compose.json').read_bytes()
        host.atomic(self.state / 'before-compose.json', current)
        host.atomic(self.state / 'compose.json', host.remove_model(host.decode(current)))
        journal = host.read_json(self.state / 'host.json')
        journal.update(phase='removing', before_sha256=host.digest(current), before_image_id='sha256:config')
        host.atomic(self.state / 'host.json', journal)
        host.manage(argparse.Namespace(action='rollback'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        stops = [item for item in self.commands if item[0] == 'compose' and item[1][0] == 'stop']
        self.assertEqual(stops[-1][2], 'before-compose.json')
        self.assertIn('p2pstream-server-updater', stops[-2][1])
    def test_pinned_tool_arguments_and_authority(self):
        self.install()
        controller = host.Host()
        with controller.lock():
            host.Host.__mro__[1].tools(controller, 'compose', 'ps', 'literal value', '$(not-executed)')
        args = [item[1] for item in self.commands if item[0] == 'docker' and item[1][0] == 'run'][-1]
        self.assertIn(HELPER, args)
        self.assertIn('type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock', args)
        self.assertEqual(args[-4:], ('compose', 'ps', 'literal value', '$(not-executed)'))

    def test_timed_out_helper_is_confirmed_stopped_before_return(self):
        self.install()
        controller = host.Host()
        base = host.Host.__mro__[1]
        calls = []
        running = False
        def daemon(*args, **kwargs):
            nonlocal running
            calls.append(args[0])
            if args[0] == 'run':
                running = True
                raise subprocess.TimeoutExpired(args, 1)
            if args[0] == 'ps':
                return subprocess.CompletedProcess(args, 0, (controller.helper_name() + '\n').encode() if running else b'', b'')
            if args[0] == 'kill':
                running = False
            if args[0] == 'inspect':
                return subprocess.CompletedProcess(args, 0, b'[{"State":{"Running":false}}]', b'')
            return subprocess.CompletedProcess(args, 0, b'', b'')
        with controller.lock(), patch.object(host, 'docker', daemon):
            with self.assertRaises(subprocess.TimeoutExpired):
                base.run_helper(controller, '/app/p2pstream', ['server-updater', 'configure'])
        self.assertEqual(calls, ['run', 'ps', 'kill', 'inspect', 'rm'])
        self.assertFalse((self.state / 'helper.json').exists())

    def test_helper_failure_retains_private_diagnostics_and_settles(self):
        self.install()
        controller = host.Host()
        base = host.Host.__mro__[1]
        def rejected(*args, **kwargs):
            if args[0] == 'run':
                return subprocess.CompletedProcess(args, 1, b'', b'private validator detail')
            return subprocess.CompletedProcess(args, 0, b'', b'')
        with controller.lock(), patch.object(host, 'docker', rejected):
            with self.assertRaisesRegex(host.Failure, 'private diagnostic'):
                base.run_helper(controller, '/app/p2pstream', ['server-updater', 'enroll', '--private-input', 'secret-sentinel'])
        diagnostic = self.state / 'helper-failure.log'
        self.assertEqual(diagnostic.stat().st_mode & 0o777, 0o600)
        self.assertIn(b'private validator detail', diagnostic.read_bytes())
        header = json.loads(diagnostic.read_bytes().splitlines()[0])
        self.assertEqual(header['check'], 'server-updater enroll')
        self.assertEqual(header['exit'], 1)
        self.assertRegex(header['time'], r'^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$')
        self.assertNotIn('secret-sentinel', json.dumps(header))
        self.assertFalse((self.state / 'helper.json').exists())

    def test_inspection_never_terminates_an_abandoned_recovery_helper(self):
        self.install()
        controller = host.Host()
        base = host.Host.__mro__[1]
        with controller.lock():
            host.atomic(self.state / 'helper.json', {'name': controller.helper_name(), 'phase': 'recovering_update'})
            with patch.object(host, 'docker') as daemon:
                with self.assertRaisesRegex(host.Failure, 'explicit repair or recover-update'):
                    base.run_helper(controller, '/app/p2pstream', ['server-updater', 'check'], readonly=True)
                daemon.assert_not_called()

    def test_daemon_loss_during_inspection_requires_repair_without_old_rollback(self):
        self.install()
        controller = host.Host()
        base = host.Host.__mro__[1]
        current = (self.state / 'compose.json').read_bytes()
        def unavailable(*args, **kwargs):
            raise host.Failure('daemon unavailable')
        with controller.lock(), patch.object(host, 'docker', unavailable):
            with self.assertRaises(host.UnsettledHelper):
                base.run_helper(controller, '/app/p2pstream', ['server-updater', 'check'], readonly=True)
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual((journal['phase'], journal['recovery_phase']), ('recovery_required', 'healthy'))
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual((self.state / 'compose.json').read_bytes(), current)
        self.assertEqual(self.restart_count, 1)

    def test_interrupted_data_recovery_resumes_only_recorded_operation(self):
        self.install()
        journal = host.read_json(self.state / 'host.json')
        journal.update(phase='recovery_required', recovery_phase='recovering_update', recovery_operation='operation-one')
        host.atomic(self.state / 'host.json', journal)
        host.atomic(self.state / 'state.json', {'operation': {'id': 'operation-two', 'phase': 'recovery_required'}})
        with self.assertRaisesRegex(host.Failure, 'recorded paused update'):
            host.manage(argparse.Namespace(action='recover-update'))
        host.atomic(self.state / 'state.json', {'operation': {'id': 'operation-one', 'phase': 'rolling_back'}})
        with self.assertRaisesRegex(host.Failure, 'recover-update to resume'):
            host.manage(argparse.Namespace(action='repair'))
        original = host.Host.command
        def recovered(controller, *args):
            if args[0] == 'recover':
                host.atomic(self.state / 'state.json', {'operation': {'id': 'operation-one', 'phase': 'rolled_back'}})
            return original(controller, *args)
        with patch.object(host.Host, 'command', recovered):
            host.manage(argparse.Namespace(action='recover-update'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual(self.restart_count, 1)

    def test_diagnostics_failure_during_paused_update_keeps_data_recovery_usable(self):
        self.install()
        controller = host.Host()
        base = host.Host.__mro__[1]
        host.atomic(self.state / 'state.json', {'operation': {'id': 'paused-update', 'phase': 'recovery_required'}})
        with controller.lock(), patch.object(host, 'docker', side_effect=host.Failure('daemon unavailable')):
            with self.assertRaises(host.UnsettledHelper):
                base.run_helper(controller, '/app/p2pstream', ['server-updater', 'check'], readonly=True)
        with self.assertRaisesRegex(host.Failure, 'paused data recovery is active'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        original = host.Host.command
        def recovered(controller, *args):
            if args[0] == 'recover':
                host.atomic(self.state / 'state.json', {'operation': {'id': 'paused-update', 'phase': 'rolled_back'}})
            return original(controller, *args)
        with patch.object(host.Host, 'command', recovered):
            host.manage(argparse.Namespace(action='recover-update'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_rejected_preparation_can_be_archived_then_fresh_setup_succeeds(self):
        self.failure = 'layout'
        with self.assertRaisesRegex(host.Failure, 'unsupported layout'):
            self.install()
        self.assertEqual(self.restart_count, 0)
        self.assertFalse((self.state / 'config.json').exists())
        host.manage(argparse.Namespace(action='discard-preparation'))
        self.assertFalse((self.state / 'host.json').exists())
        self.assertTrue((self.state / 'history').exists())
        self.assertEqual(self.restart_count, 0)
        self.failure = ''
        self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_configured_preparation_cannot_be_discarded(self):
        self.install()
        cfg = (self.state / 'config.json').read_bytes()
        with self.assertRaisesRegex(host.Failure, 'configuration exists'):
            host.manage(argparse.Namespace(action='discard-preparation'))
        self.assertEqual((self.state / 'config.json').read_bytes(), cfg)

    def test_invalid_discard_never_stops_enrolled_data_recovery(self):
        self.install()
        journal = host.read_json(self.state / 'host.json')
        journal.update(phase='recovering_update')
        host.atomic(self.state / 'host.json', journal)
        host.atomic(self.state / 'helper.json', {'name': 'p2pstream-server-update-host-' + ID, 'phase': 'recovering_update'})
        with patch.object(host.Host, 'settle_helper') as settle:
            with self.assertRaisesRegex(host.Failure, 'configuration exists'):
                host.manage(argparse.Namespace(action='discard-preparation'))
            settle.assert_not_called()

    def test_interrupted_preparation_archive_keeps_tools_and_can_resume(self):
        self.failure = 'layout'
        with self.assertRaises(host.Failure):
            self.install()
        original = host.shutil.copy2
        count = 0
        def interrupted(*args, **kwargs):
            nonlocal count
            count += 1
            if count == 3:
                raise KeyboardInterrupt()
            return original(*args, **kwargs)
        with patch.object(host.shutil, 'copy2', interrupted):
            with self.assertRaises(KeyboardInterrupt):
                host.manage(argparse.Namespace(action='discard-preparation'))
        self.assertTrue((self.state / 'manage').exists())
        self.assertTrue((self.state / 'input.json').exists())
        self.failure = ''
        with self.assertRaisesRegex(host.Failure, 'Interrupted archival must finish'):
            host.manage(argparse.Namespace(action='repair'))
        with self.assertRaisesRegex(host.Failure, 'Interrupted archival must finish'):
            self.install()
        self.assertEqual(self.restart_count, 0)
        host.manage(argparse.Namespace(action='discard-preparation'))
        self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_interrupted_removal_archive_resumes_after_journal_commit(self):
        self.install()
        host.manage(argparse.Namespace(action='remove'))
        with patch.object(host.Host, 'finish_archive', side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):
                self.install()
        self.assertFalse((self.state / 'host.json').exists())
        self.assertTrue((self.state / 'config.json').exists())
        self.assertEqual(host.read_json(self.state / 'archive.json')['stage'], 'copied')
        self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertFalse((self.state / 'archive.json').exists())

    def test_discard_refuses_update_artifacts_and_executor(self):
        self.failure = 'layout'
        with self.assertRaises(host.Failure):
            self.install()
        for relative in ['state.json', 'control/maintenance']:
            path = self.state / relative
            path.parent.mkdir(exist_ok=True)
            host.atomic(path, {})
            with self.assertRaisesRegex(host.Failure, 'Update or maintenance state'):
                host.manage(argparse.Namespace(action='discard-preparation'))
            path.unlink()
        original = host.docker
        def executor(*args, **kwargs):
            if args[0] == 'ps' and any('server-update.executor=' in str(value) for value in args):
                return subprocess.CompletedProcess(args, 0, b'existing-executor\n', b'')
            return original(*args, **kwargs)
        with patch.object(host, 'docker', executor):
            with self.assertRaisesRegex(host.Failure, 'enrolled executor exists'):
                host.manage(argparse.Namespace(action='discard-preparation'))
        self.assertTrue((self.state / 'host.json').exists())
        self.assertEqual(self.restart_count, 0)

    def test_discard_rechecks_config_after_settling_helper(self):
        self.failure = 'layout'
        with self.assertRaises(host.Failure):
            self.install()
        def finished(controller):
            host.atomic(self.state / 'config.json', {'token': 'just-completed'})
        with patch.object(host.Host, 'settle_helper', finished):
            with self.assertRaisesRegex(host.Failure, 'configuration exists'):
                host.manage(argparse.Namespace(action='discard-preparation'))
        self.assertTrue((self.state / 'config.json').exists())

class RetainedInstallation(unittest.TestCase):
    setUp = Lifecycle.setUp
    tearDown = Lifecycle.tearDown
    install = Lifecycle.install
    docker = Lifecycle.docker

    def observed(self):
        self.managed = True
        self.install()
        floor = dict(host.read_json(self.state / 'config.json')['bootstrap_floor'])
        observed = dict(floor, sequence=14, security_epoch=3, minimum_safe_version='v1.0.1', manifest_sha256='e' * 64)
        host.atomic(self.state / 'state.json', {'api': 1, 'instance_id': ID, 'channel': 'stable', 'floor': floor, 'observed_floor': observed})
        return observed

    def test_ordinary_remove_reenroll_cannot_forget_observed_floor(self):
        observed = self.observed()
        host.manage(argparse.Namespace(action='remove'))
        self.commands.clear()
        with self.assertRaisesRegex(host.Failure, 'retained enrollment floor'):
            self.install()
        self.assertFalse(any(c[0] == 'docker' and c[1][0] == 'pull' for c in self.commands))
        self.assertEqual(host.read_json(self.state / 'installation-receipt.json')['floor'], observed)


    def archive(self):
        controller = host.Host()
        with controller.lock(allow_archive=True):
            controller.archive_removed()

    def read_retained(self):
        return host.retained_installation(self.state, ID, 'test/repo', 'stable')

    def test_legacy_archive_recovers_observed_floor_without_image_inference(self):
        observed = self.observed()
        host.manage(argparse.Namespace(action='remove'))
        self.archive()
        (self.state / host.INSTALLATION_RECEIPT).unlink()
        self.assertEqual(self.read_retained()['floor'], observed)
        self.commands.clear()
        with self.assertRaisesRegex(host.Failure, 'retained enrollment floor'):
            self.install()
        self.assertFalse(any(c[0] == 'docker' and c[1][0] == 'pull' for c in self.commands))

    def test_retained_floor_survives_archive_commit_interruption(self):
        observed = self.observed()
        host.manage(argparse.Namespace(action='remove'))
        with patch.object(host.Host, 'finish_archive', side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):
                self.archive()
        self.assertFalse((self.state / 'host.json').exists())
        self.assertEqual(host.read_json(self.state / host.INSTALLATION_RECEIPT)['floor'], observed)
        # Also recover an archive committed by an older controller with no receipt.
        (self.state / host.INSTALLATION_RECEIPT).unlink()
        with host.Host().lock(allow_archive=True):
            pass
        self.assertEqual(host.read_json(self.state / host.INSTALLATION_RECEIPT)['floor'], observed)
        self.assertFalse((self.state / 'archive.json').exists())

    def test_receipt_is_durable_before_archive_copy_and_remove_commit(self):
        observed = self.observed()
        original = host.Host.phase
        def phase(controller, name, **values):
            if name == 'removed':
                self.assertEqual(host.read_json(self.state / host.INSTALLATION_RECEIPT)['floor'], observed)
                raise KeyboardInterrupt()
            return original(controller, name, **values)
        with patch.object(host.Host, 'phase', phase):
            with self.assertRaises(KeyboardInterrupt):
                host.manage(argparse.Namespace(action='remove'))
        journal = host.read_json(self.state / 'host.json')
        journal['phase'] = 'removed'
        host.atomic(self.state / 'host.json', journal)
        with patch.object(host.shutil, 'copy2', side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):
                self.archive()
        self.assertTrue((self.state / 'host.json').exists())
        self.assertEqual(self.read_retained()['floor'], observed)
        self.archive()
        self.assertEqual(self.read_retained()['floor'], observed)

    def test_interrupted_partial_archive_is_ignored_only_with_active_journal(self):
        observed = self.observed()
        host.manage(argparse.Namespace(action='remove'))
        with patch.object(host.shutil, 'copy2', side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):
                self.archive()
        self.assertEqual(self.read_retained()['floor'], observed)
        (self.state / 'host.json').unlink()
        with self.assertRaisesRegex(host.Failure, 'Incomplete retained archive'):
            self.read_retained()

    def test_receipt_write_failure_prevents_archive_commit(self):
        self.observed()
        host.manage(argparse.Namespace(action='remove'))
        original = host.atomic
        def fail(path, data, *args, **kwargs):
            if Path(path).name == host.INSTALLATION_RECEIPT:
                raise OSError('simulated durable write failure')
            return original(path, data, *args, **kwargs)
        with patch.object(host, 'atomic', fail):
            with self.assertRaises(OSError):
                self.archive()
        self.assertTrue((self.state / 'host.json').exists())
        self.assertFalse((self.state / 'archive.json').exists())

    def test_archive_requires_executor_lock(self):
        self.observed()
        host.manage(argparse.Namespace(action='remove'))
        with open(self.state / 'executor.lock', 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(host.Failure, 'executor still owns'):
                self.archive()
        self.assertTrue((self.state / 'host.json').exists())

    def test_matching_reenrollment_preserves_observed_floor_and_provenance(self):
        observed = self.observed()
        host.manage(argparse.Namespace(action='remove'))
        original = host.read_json(self.state / host.INSTALLATION_RECEIPT)
        self.manifest.update({k: observed[k] for k in ('sequence', 'security_epoch', 'minimum_safe_version')})
        self.args.manifest_sha256 = observed['manifest_sha256']
        with patch.object(host, 'release_metadata', return_value=(self.manifest, self.descriptor, REF, observed['manifest_sha256'])):
            self.install()
        receipt = self.read_retained()
        self.assertEqual(receipt['floor'], observed)
        self.assertTrue(all(source in receipt['sources'] for source in original['sources']))
        self.assertNotIn('created_at', receipt)
        self.assertEqual(host.read_json(self.state / 'config.json')['bootstrap_floor'], observed)

    def test_equal_sequence_manifest_conflict_in_older_history_is_not_hidden(self):
        self.observed()
        host.manage(argparse.Namespace(action='remove'))
        self.archive()
        archive = next((self.state / 'history').iterdir())
        config = host.read_json(archive / 'config.json')
        # Conflict below the winning observed sequence must still fail closed.
        config['bootstrap_floor']['manifest_sha256'] = '0' * 64
        host.atomic(archive / 'config.json', config)
        with self.assertRaisesRegex(host.Failure, 'Release identity differs'):
            self.read_retained()

    def test_each_effective_constraint_and_equal_sequence_hash_is_enforced(self):
        observed = self.observed()
        host.manage(argparse.Namespace(action='remove'))
        for key, value in [('sequence', 13), ('security_epoch', 2), ('minimum_safe_version', 'v1.0.0'), ('manifest_sha256', '0' * 64)]:
            with self.subTest(key=key):
                candidate = dict(observed, **{key: value})
                with self.assertRaisesRegex(host.Failure, 'retained enrollment floor'):
                    host.check_cancellation_floor(self.state, self.args, candidate, candidate['manifest_sha256'])

    def test_live_and_archived_identity_must_match_config_and_receipt(self):
        self.observed()
        for filename, field, value in [('state.json', 'instance_id', '22222222-2222-4222-8222-222222222222'), ('state.json', 'channel', 'staging'), ('config.json', 'repository', 'other/repo'), ('host.json', 'installation', '22222222-2222-4222-8222-222222222222')]:
            path = self.state / filename
            original = path.read_bytes()
            changed = host.decode(original)
            changed[field] = value
            host.atomic(path, changed)
            with self.subTest(filename=filename, field=field), self.assertRaisesRegex(host.Failure, 'another installation'):
                self.read_retained()
            host.atomic(path, original)
        host.manage(argparse.Namespace(action='remove'))
        self.archive()
        archive = next((self.state / 'history').iterdir())
        path = archive / 'config.json'
        changed = host.read_json(path)
        changed['repository'] = 'other/repo'
        host.atomic(path, changed)
        with self.assertRaisesRegex(host.Failure, 'another installation'):
            self.read_retained()

    def test_incomplete_history_cannot_be_replaced_by_current_image_or_receipt(self):
        self.observed()
        host.manage(argparse.Namespace(action='remove'))
        self.archive()
        archive = next((self.state / 'history').iterdir())
        (archive / 'config.json').unlink()
        with self.assertRaisesRegex(host.Failure, 'Missing retained configuration'):
            self.read_retained()
        (self.state / host.INSTALLATION_RECEIPT).unlink()
        with self.assertRaisesRegex(host.Failure, 'Missing retained configuration'):
            self.read_retained()

    def test_legacy_missing_or_zero_observed_uses_activated_floor(self):
        self.observed()
        state = host.read_json(self.state / 'state.json')
        state.pop('observed_floor')
        for observed in [None, {'sequence': 0, 'security_epoch': 0, 'minimum_safe_version': '', 'manifest_sha256': ''}]:
            if observed is not None:
                state['observed_floor'] = observed
            host.atomic(self.state / 'state.json', state)
            self.assertEqual(self.read_retained()['floor'], state['floor'])

    def test_invalid_floor_and_receipt_structures_fail_closed(self):
        self.observed()
        path = self.state / 'state.json'
        original = host.read_json(path)
        cases = [('sequence', 0), ('sequence', True), ('sequence', 2 ** 64), ('sequence', 1.5), ('security_epoch', False), ('security_epoch', -1), ('security_epoch', 2 ** 64), ('minimum_safe_version', 'v1.01.0'), ('minimum_safe_version', 'v1.0.0-staging.1'), ('minimum_safe_version', 'v' + '1' * 93 + '.0.0'), ('manifest_sha256', 'A' * 64), ('manifest_sha256', 'f' * 63)]
        for key, value in cases:
            state = copy.deepcopy(original)
            state['observed_floor'][key] = value
            host.atomic(path, state)
            with self.subTest(key=key, value=value), self.assertRaises(host.Failure):
                self.read_retained()
        host.atomic(path, original)
        receipt = host.persist_installation(self.state, ID, 'test/repo', 'stable')
        for change in [{'api': True}, {'api': 2}, {'sources': []}, {'floor': dict(receipt['floor'], sequence=15)}, {'repository': 'other/repo'}]:
            host.atomic(self.state / host.INSTALLATION_RECEIPT, dict(receipt, **change))
            with self.subTest(change=change), self.assertRaises(host.Failure):
                self.read_retained()

    def test_older_cancellation_receipt_is_migrated_without_losing_semantics(self):
        self.state.mkdir(mode=0o700)
        floor = {'sequence': 12, 'security_epoch': 2, 'minimum_safe_version': 'v1.0.0', 'manifest_sha256': 'f' * 64}
        old = {'api': 1, 'installation': ID, 'repository': 'test/repo', 'channel': 'stable', 'floor': floor, 'completed': True}
        host.atomic(self.state / host.CANCELLATION, old)
        receipt = host.persist_installation(self.state, ID, 'test/repo', 'stable')
        self.assertEqual(receipt['floor'], floor)
        self.assertEqual(host.read_json(self.state / host.CANCELLATION), old)
        self.assertEqual(receipt['sources'], [{'kind': 'cancellation', 'sha256': host.digest((self.state / host.CANCELLATION).read_bytes()), 'floor': floor}])

    def test_uint64_maximum_and_large_semver_components_are_not_truncated(self):
        floor = {'sequence': 2 ** 64 - 1, 'security_epoch': 2 ** 64 - 1, 'minimum_safe_version': 'v18446744073709551616.0.0', 'manifest_sha256': 'f' * 64}
        self.assertEqual(host.validate_floor(floor), floor)
        host.require_floor(floor, dict(floor, sequence=2 ** 64 - 2, minimum_safe_version='v18446744073709551615.9.9'))

    def test_missing_history_with_empty_history_directory_cannot_reset_receipt(self):
        self.state.mkdir(mode=0o700)
        (self.state / 'history').mkdir(mode=0o700)
        host.atomic(self.state / 'reenrollment.json', {'installation': ID})
        with self.assertRaisesRegex(host.Failure, 'Missing retained removal history'):
            self.read_retained()

    def test_selected_server_below_retained_minimum_safe_is_refused(self):
        observed = self.observed()
        self.args.expect_version = 'v1.0.0'
        with self.assertRaisesRegex(host.Failure, 'Selected server is below'):
            host.check_cancellation_floor(self.state, self.args, observed, observed['manifest_sha256'])



class EnrollmentPreflight(unittest.TestCase):
    setUp = Lifecycle.setUp
    tearDown = Lifecycle.tearDown
    install = Lifecycle.install
    docker = Lifecycle.docker

    def ready(self):
        return {'api': 1, 'installation_id': ID, 'version': self.args.expect_version, 'commit': self.args.expect_commit, 'ready': True, 'blockers': []}

    def blocked(self, code='agent_updates_active'):
        return dict(self.ready(), ready=False, blockers=[{'code': code, 'action': 'UNTRUSTED ACTION', 'message': 'UNTRUSTED MESSAGE'}])

    def probe(self, response, *, capability=None):
        original = self.docker
        def docker(*args, **kwargs):
            if args[0] == 'exec' and args[-1] == 'server-enrollment-preflight':
                self.commands.append(('docker', args))
                value = response() if callable(response) else response
                if isinstance(value, subprocess.CompletedProcess):
                    return value
                return subprocess.CompletedProcess(args, 0, value if isinstance(value, bytes) else json.dumps(value).encode(), b'')
            if capability is not None and args[0] == 'exec' and args[-1] in {'server-deployment-capabilities', 'server-enrollment-capabilities'}:
                return subprocess.CompletedProcess(args, 0, json.dumps(capability).encode(), b'')
            return original(*args, **kwargs)
        return patch.object(host, 'docker', docker)

    def test_initial_blocker_precedes_all_state_tools_pulls_and_mutations(self):
        sources = {p: p.read_bytes() for p in self.source.iterdir()}
        with self.probe(self.blocked()):
            with self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'Wait for agent updates') as error:
                self.install()
        self.assertNotIn('UNTRUSTED', str(error.exception))
        self.assertFalse(self.state.exists())
        self.assertEqual(sources, {p: p.read_bytes() for p in self.source.iterdir()})
        self.assertFalse(any(c[0] in {'binary', 'compose'} or c[0] == 'docker' and c[1][0] in {'pull', 'run'} for c in self.commands))

    def test_second_preflight_blocks_before_runtime_bridge_or_lifecycle(self):
        self.managed = True
        calls = 0
        def response():
            nonlocal calls
            calls += 1
            return self.ready() if calls == 1 else self.blocked('certificate_rotation_active')
        sources = {p: p.read_bytes() for p in self.source.iterdir()}
        with self.probe(response):
            with self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'certificate rotation'):
                self.install()
        self.assertEqual(calls, 2)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'prepared')
        self.assertEqual(sources, {p: p.read_bytes() for p in self.source.iterdir()})
        self.assertEqual(self.restart_count, 0)
        self.assertFalse(any(c[0] == 'binary' and c[1][0].startswith('runtime-') or c[0] == 'compose' and c[1][0] in {'up', 'stop', 'start'} for c in self.commands))
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_capability_is_explicit_and_missing_capability_fails_closed(self):
        for capability in [{}, {'enrollment_preflight': True}, {'enrollment_preflight': 2}]:
            with self.subTest(capability=capability), self.probe(self.ready(), capability=capability):
                with self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'capability 1'):
                    self.install()
                self.assertFalse(self.state.exists())

    def test_strict_response_shapes_identities_and_limit(self):
        valid = self.ready()
        invalid = [dict(valid, api=True), dict(valid, api=2), dict(valid, extra='unexpected'), dict(valid, ready=1), dict(valid, blockers=None), dict(valid, installation_id='foreign'), dict(valid, version='v9.0.0'), dict(valid, commit='c' * 40), dict(valid, ready=False), dict(valid, blockers=self.blocked()['blockers']), self.blocked('unknown'), dict(valid, ready=False, blockers=[{'code': 'agent_updates_active', 'action': 1, 'message': 'x'}]), b'{' + b' ' * (16 * 1024), b'not JSON', b'{"api":1,"api":1}', subprocess.CompletedProcess([], 1, b'UNTRUSTED', b'UNTRUSTED')]
        for value in invalid:
            with self.subTest(value=str(value)[:80]), self.probe(value):
                with self.assertRaises(host.EnrollmentPreflightFailure) as error:
                    self.install()
                self.assertNotIn('UNTRUSTED', str(error.exception))
                self.assertFalse(self.state.exists())

    def test_preflight_rechecks_actual_container_after_response(self):
        def changed():
            self.info['Image'] = 'sha256:changed'
            return self.ready()
        with self.probe(changed), self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'deployment changed'):
            self.install()
        self.assertFalse(self.state.exists())

    def test_stale_image_guard_precedes_preflight(self):
        self.image['Config']['Labels']['org.opencontainers.image.version'] = 'v1.0.2'
        with self.probe(lambda: self.fail('preflight must not execute')):
            with self.assertRaisesRegex(host.Failure, 'Stale command'):
                self.install()
        self.assertFalse(self.state.exists())

    def test_healthy_setup_and_repair_are_idempotent_despite_preflight_blocker(self):
        self.install()
        before = (self.state / 'host.json').read_bytes()
        self.commands.clear()
        with self.probe(lambda: self.fail('healthy enrollment must not run preflight')):
            self.install()
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual((self.state / 'host.json').read_bytes(), before)
        self.assertEqual(self.restart_count, 1)

    def test_saved_legacy_preparation_requires_capability_before_activation(self):
        self.failure = 'layout'
        with self.assertRaisesRegex(host.Failure, 'unsupported layout'):
            self.install()
        self.failure = ''
        with self.probe(self.ready(), capability={}):
            with self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'capability 1'):
                host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'prepared')
        self.assertEqual(self.restart_count, 0)


    def test_probe_failure_during_activation_does_not_fabricate_rollback(self):
        self.managed = True
        calls = 0
        def response():
            nonlocal calls
            calls += 1
            if calls == 2:
                raise host.Failure('UNTRUSTED server failure')
            return self.ready()
        with self.probe(response), self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'could not verify') as error:
            self.install()
        self.assertNotIn('UNTRUSTED', str(error.exception))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'prepared')
        self.assertEqual(self.restart_count, 0)

    def test_interrupted_stopped_replacement_uses_only_exact_preflight_provenance(self):
        self.managed = True
        original = host.Host.compose
        def interrupted(controller, *args, **kwargs):
            result = original(controller, *args, **kwargs)
            if args[0] == 'up' and '--no-start' in args and args[-1] == 'p2pstream':
                raise KeyboardInterrupt()
            return result
        with patch.object(host.Host, 'compose', interrupted), self.assertRaises(KeyboardInterrupt):
            self.install()
        self.assertFalse(self.info['State']['Running'])
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['phase'], 'activating')
        self.assertEqual(journal['activation_preflight']['installation'], ID)
        with patch.object(host.Host, 'data_identity', return_value=ID), self.probe(lambda: self.fail('stopped replacement cannot answer exec')):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')


    def test_rolled_back_repair_refusal_preserves_model_journal_and_lifecycle(self):
        self.managed = True
        self.failure = 'health'
        with self.assertRaises(host.Failure):
            self.install()
        cfg = host.read_json(self.state / 'config.json')
        model = Path(cfg['deployment_file'])
        before_model = model.read_bytes()
        before_journal = (self.state / 'host.json').read_bytes()
        before_inputs = {p: p.read_bytes() for p in self.source.iterdir()}
        self.commands.clear()
        with self.probe(self.blocked()), self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'agent updates'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(model.read_bytes(), before_model)
        self.assertEqual((self.state / 'host.json').read_bytes(), before_journal)
        self.assertEqual({p: p.read_bytes() for p in self.source.iterdir()}, before_inputs)
        self.assertFalse(any(c[0] == 'binary' or c[0] == 'compose' and c[1][0] in {'up', 'stop', 'start'} for c in self.commands))


class SingleFileLifecycle(unittest.TestCase):
    tearDown = Lifecycle.tearDown
    install = Lifecycle.install

    def setUp(self):
        Lifecycle.setUp(self)
        self.managed = True
        self.args.deployment_layout = host.SINGLE_LAYOUT
        self.options = []
        (self.source / 'prod.yaml').unlink()
        (self.source / '.env').unlink()
        self.source.chmod(0o2750)
        self.canonical = self.source / 'compose.yaml'
        host.atomic(self.canonical, self.raw, mode=0o640)
        self.inputs = self.path / 'compose.inputs.yaml'
        self.launcher = self.path / 'stable' / 'sbin' / 'p2pstream-server-update'
        self.controller = self.path / 'stable' / 'libexec' / 'p2pstream' / 'server-updater-host.py'
        for key, value in [('SINGLE_DIRECTORY', self.source), ('SINGLE_INPUTS', self.inputs), ('STABLE_LAUNCHER', self.launcher), ('STABLE_CONTROLLER', self.controller)]:
            self.stack.enter_context(patch.object(host, key, value))
        # Model a protected root for deterministic unprivileged tests. All actual
        # components inside this fixture still exercise their real modes/types.
        original_lstat = Path.lstat
        def protected_root(path, *args, **kwargs):
            info = original_lstat(path, *args, **kwargs)
            if path == Path(tempfile.gettempdir()):
                values = list(info)
                values[0] = stat.S_IFDIR | 0o755
                return os.stat_result(values)
            return info
        self.stack.enter_context(patch.object(Path, 'lstat', protected_root))
        fixture = host.Host
        outer = self
        class SingleHost(fixture):
            def command(self, *args):
                if args[0] != 'enroll':
                    return super().command(*args)
                outer.commands.append(('binary', args))
                outer.assertIn('--deployment-layout', args)
                outer.assertEqual(args[args.index('--deployment-layout') + 1], host.SINGLE_LAYOUT)
                if outer.failure == 'layout':
                    raise host.Failure('unsupported layout')
                source = host.read_json(outer.state / 'input.json')
                source['services']['p2pstream']['image'] = REF
                env = source['services']['p2pstream'].setdefault('environment', {})
                env.update(SERVER_UPDATE_INSTANCE_ID=ID, SERVER_UPDATE_TOKEN_FILE='/run/p2pstream-server-update/token', SERVER_UPDATE_SOCKET='/run/p2pstream-server-update/control.sock', SERVER_UPDATE_GATE_FILE='/run/p2pstream-server-update/maintenance', SERVER_UPDATE_RUNTIME_PROTOCOL='2', SERVER_UPDATE_GENERATION=ID)
                source['services']['p2pstream'].setdefault('labels', {})['p2pstream.server-update.instance'] = ID
                source['services']['p2pstream-server-updater'] = {'image': HELPER, 'volumes': []}
                cfg = {'instance_id': ID, 'repository': 'test/repo', 'project': 'custom-project', 'token': 'private-token' * 4, 'control_dir': str(outer.state / 'control'), 'data_volume': 'custom-data', 'channel': outer.args.expect_channel, 'bootstrap_floor': {key: outer.manifest[key] for key in ('sequence', 'security_epoch', 'minimum_safe_version')} | {'manifest_sha256': outer.args.manifest_sha256}, 'deployment_protocol': 2, 'deployment_layout': host.SINGLE_LAYOUT, 'deployment_file': str(outer.canonical)}
                (outer.state / 'control').mkdir(exist_ok=True)
                host.atomic(outer.state / 'control' / 'token', cfg['token'].encode(), mode=0o444)
                host.atomic(outer.state / 'original-compose.json', (outer.state / 'input.json').read_bytes())
                host.atomic(outer.state / 'staged-compose.yaml', (json.dumps(source, indent=2) + '\n').encode())
                host.atomic(outer.state / 'config.json', cfg)
                return subprocess.CompletedProcess(args, 0, b'{}', b'')
        self.stack.enter_context(patch.object(host, 'Host', SingleHost))

    def docker(self, *args, **kwargs):
        if args[0] in {'exec', 'run'} and args[-1] == 'server-enrollment-capabilities':
            self.commands.append(('docker', args))
            return subprocess.CompletedProcess(args, 0, json.dumps({'runtime_guard': 2, 'enrollment_preflight': 1, 'canonical_compose': 1, 'version': self.args.expect_version, 'commit': self.args.expect_commit}).encode(), b'')
        if args[0] == 'compose' and 'ps' in args:
            self.commands.append(('docker', args))
            return subprocess.CompletedProcess(args, 0, self.server_inventory if self.server_inventory is not None else (self.info['Id'] + '\n').encode(), b'')
        if args[0] == 'compose' and 'config' in args and '--hash' not in args and '--environment' not in args:
            self.commands.append(('docker', args))
            paths = [value for index, value in enumerate(args) if index and args[index - 1] == '-f']
            path = Path(paths[-1]) if paths else self.source / 'compose.yaml'
            raw = path.read_bytes()
            if not raw.lstrip().startswith(b'{'):
                raw = self.raw  # Native Compose resolves the published YAML fixture.
            return subprocess.CompletedProcess(args, 0, raw, b'')
        return Lifecycle.docker(self, *args, **kwargs)

    def test_initial_stage_block_stop_publish_order_and_no_bridges(self):
        original = self.canonical.read_bytes()
        command = host.Host.command
        compose = host.Host.compose
        seen = []
        def binary(controller, *args):
            if args[0] in {'enroll', 'runtime-block-staged'}:
                self.assertEqual(self.canonical.read_bytes(), original)
                if args[0] == 'runtime-block-staged':
                    self.assertEqual(controller.journal['phase'], 'activating')
                    self.assertTrue(self.launcher.exists())
                    self.assertEqual(controller.journal['single_initial']['candidate_sha256'], host.digest((self.state / 'staged-compose.yaml').read_bytes()))
                seen.append(args[0])
            return command(controller, *args)
        def composed(controller, *args, **kwargs):
            if args[0] == 'stop' and args[-1] == 'p2pstream' and kwargs.get('model') == 'before-compose.json':
                self.assertEqual(seen[-1], 'runtime-block-staged')
                self.assertEqual(self.canonical.read_bytes(), original)
                seen.append('original-stopped')
            if args[0] == 'up' and args[-1] == 'p2pstream':
                self.assertEqual(seen[-1], 'original-stopped')
                self.assertEqual(self.canonical.read_bytes(), (self.state / 'staged-compose.yaml').read_bytes())
            return compose(controller, *args, **kwargs)
        with patch.object(host.Host, 'command', binary), patch.object(host.Host, 'compose', composed), patch.object(host.Host, 'source_bridges', side_effect=AssertionError('no bridge allowed')):
            self.install()
        self.assertEqual(seen, ['enroll', 'runtime-block-staged', 'original-stopped'])
        self.assertEqual(stat.S_IMODE(self.canonical.stat().st_mode), 0o640)
        self.assertEqual(stat.S_IMODE(self.source.stat().st_mode), 0o2750)
        self.assertFalse(any('.p2pstream-input-' in p.name for p in self.source.iterdir()))
        self.assertNotIn('include', host.read_json(self.canonical))
        self.assertEqual(host.read_json(self.inputs)['services']['p2pstream']['environment']['SECRET'], '$$literal')
        before = self.canonical.read_bytes()
        self.install()
        self.assertEqual(self.restart_count, 1)
        self.assertEqual(self.canonical.read_bytes(), before)

    def test_original_published_yaml_is_preserved_until_publication(self):
        template = (self.bundle / 'compose.yaml').read_bytes()
        import re
        raw, count = re.subn(rb'(?m)^    image: [^\n]+$', ('    image: ' + REF).encode(), template)
        self.assertEqual(count, 1)
        self.model['services']['p2pstream']['image'] = REF
        self.raw = json.dumps(self.model).encode()
        host.atomic(self.canonical, raw, mode=0o640)
        original = host.Host.command
        def command(controller, *args):
            if args[0] == 'runtime-block-staged':
                self.assertEqual(self.canonical.read_bytes(), raw)
                self.assertEqual((self.state / 'original-source.yaml').read_bytes(), raw)
            return original(controller, *args)
        with patch.object(host.Host, 'command', command):
            self.install()
        self.assertTrue(self.canonical.read_bytes().startswith(b'{\n'))

    def test_apply_and_remove_keep_native_canonical_current_and_inputs_editable(self):
        self.install()
        before = host.read_json(self.canonical)
        edited = host.read_json(self.inputs)
        edited['services']['p2pstream']['image'] = 'ignored-image-edit'
        edited['services']['p2pstream']['environment']['SECRET'] = '$$new-value'
        host.atomic(self.inputs, edited, mode=0o640)
        with patch.object(host.Host, 'source_bridges', side_effect=AssertionError('no bridge allowed')):
            host.manage(argparse.Namespace(action='apply'))
        current = host.read_json(self.canonical)
        self.assertEqual(current['services']['p2pstream']['image'], before['services']['p2pstream']['image'])
        self.assertEqual(current['services']['p2pstream-server-updater'], before['services']['p2pstream-server-updater'])
        self.assertEqual(current['services']['p2pstream']['environment']['SECRET'], '$$new-value')
        self.assertTrue(self.canonical.read_bytes().startswith(b'{\n'))
        inputs = self.inputs.read_bytes()
        host.manage(argparse.Namespace(action='remove'))
        detached = host.read_json(self.canonical)
        self.assertEqual(detached['services']['p2pstream']['image'], REF)
        self.assertEqual(detached['services']['p2pstream']['environment']['SECRET'], '$$new-value')
        self.assertNotIn('p2pstream-server-updater', detached['services'])
        self.assertEqual(self.inputs.read_bytes(), inputs)
        self.assertTrue(self.canonical.read_bytes().startswith(b'{\n'))
        self.assertEqual(host.read_json(self.state / 'host.json')['source_directory'], str(self.source))
        self.assertEqual(host.read_json(self.state / host.INSTALLATION_RECEIPT)['installation'], ID)

    def test_preflight_refusal_before_staging_and_before_canonical_activation(self):
        original = self.docker
        for fail_at in [1, 2]:
            calls = 0
            def docker(*args, **kwargs):
                nonlocal calls
                if args[0] == 'exec' and args[-1] == 'server-enrollment-preflight':
                    calls += 1
                    if calls == fail_at:
                        return subprocess.CompletedProcess(args, 0, json.dumps({'api': 1, 'installation_id': ID, 'version': self.args.expect_version, 'commit': self.args.expect_commit, 'ready': False, 'blockers': [{'code': 'agent_updates_active', 'action': 'ignored', 'message': 'ignored'}]}).encode(), b'')
                return original(*args, **kwargs)
            before = self.canonical.read_bytes()
            with self.subTest(fail_at=fail_at), patch.object(host, 'docker', docker), self.assertRaises(host.EnrollmentPreflightFailure):
                self.install()
            self.assertEqual(self.canonical.read_bytes(), before)
            self.assertEqual(self.restart_count, 0)
            if fail_at == 1:
                self.assertFalse(self.state.exists())
                self.assertFalse(self.launcher.exists())
            else:
                self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'prepared')

    def test_initial_publication_interruptions_resume_exact_candidate(self):
        original = host.atomic
        def interrupt(path, data, *args, **kwargs):
            result = original(path, data, *args, **kwargs)
            if Path(path) == self.canonical and (self.state / 'host.json').exists() and host.read_json(self.state / 'host.json')['phase'] == 'activating':
                raise KeyboardInterrupt()
            return result
        with patch.object(host, 'atomic', interrupt), self.assertRaises(KeyboardInterrupt):
            self.install()
        self.assertFalse(self.info['State']['Running'])
        self.assertEqual(self.canonical.read_bytes(), (self.state / 'staged-compose.yaml').read_bytes())
        with patch.object(host.Host, 'data_identity', return_value=ID):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual(self.restart_count, 1)

    def test_source_drift_after_stopping_fails_closed_without_overwrite(self):
        original = host.Host.compose
        changed = b'operator canonical changes'
        def compose(controller, *args, **kwargs):
            result = original(controller, *args, **kwargs)
            if args[0] == 'stop' and args[-1] == 'p2pstream' and kwargs.get('model') == 'before-compose.json':
                self.canonical.write_bytes(changed)
            return result
        with patch.object(host.Host, 'compose', compose), self.assertRaises(host.CanonicalDrift):
            self.install()
        self.assertEqual(self.canonical.read_bytes(), changed)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'activating')
        with self.assertRaises(host.CanonicalDrift):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.canonical.read_bytes(), changed)

    def test_rollback_restores_same_path_and_reuses_recorded_fresh_generation(self):
        self.install()
        original_model = host.read_json(self.canonical)
        before_generation = original_model['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION']
        edit = host.read_json(self.inputs)
        edit['services']['p2pstream']['environment']['SECRET'] = 'new'
        host.atomic(self.inputs, edit, mode=0o640)
        original = host.Host.deploy_server
        def interrupted(controller, *args, **kwargs):
            if controller.journal['phase'] == 'applying':
                raise KeyboardInterrupt()
            return original(controller, *args, **kwargs)
        with patch.object(host.Host, 'deploy_server', interrupted), self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='apply'))
        inputs = self.inputs.read_bytes()
        original_atomic = host.atomic
        def crash(path, data, *args, **kwargs):
            result = original_atomic(path, data, *args, **kwargs)
            if Path(path) == self.canonical and host.read_json(self.state / 'host.json')['phase'] == 'rolling_back':
                raise KeyboardInterrupt()
            return result
        with patch.object(host, 'atomic', crash), self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='repair'))
        rollback = self.canonical.read_bytes()
        generation = host.decode(rollback)['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION']
        self.assertNotEqual(generation, before_generation)
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.canonical.read_bytes(), rollback)
        self.assertEqual(self.inputs.read_bytes(), inputs)
        self.assertEqual(host.read_json(self.canonical)['services']['p2pstream']['environment']['SECRET'], '$$literal')

    def test_launcher_survives_removal_and_history_archival(self):
        self.install()
        launcher, controller = self.launcher.read_bytes(), self.controller.read_bytes()
        host.manage(argparse.Namespace(action='remove'))
        instance = host.Host()
        with instance.lock():
            instance.archive_removed()
        self.assertEqual(self.launcher.read_bytes(), launcher)
        self.assertEqual(self.controller.read_bytes(), controller)
        self.assertFalse((self.state / 'manage').exists())
        with self.assertRaisesRegex(host.Failure, 'private enrollment history is missing'):
            host.manage(argparse.Namespace(action='repair'))

    def test_removed_reenrollment_retains_floor_and_uses_same_canonical(self):
        self.install()
        floor = host.read_json(self.state / 'config.json')['bootstrap_floor']
        observed = dict(floor, sequence=14, manifest_sha256='e' * 64)
        host.atomic(self.state / 'state.json', {'api': 1, 'instance_id': ID, 'channel': 'stable', 'floor': floor, 'observed_floor': observed})
        host.manage(argparse.Namespace(action='remove'))
        with self.assertRaisesRegex(host.Failure, 'retained enrollment floor'):
            self.install()
        # Use the current detached model as editable inputs (image edits cannot
        # silently get overwritten during reenrollment).
        host.atomic(self.inputs, self.canonical.read_bytes(), mode=0o640)
        self.manifest.update(sequence=14)
        self.args.manifest_sha256 = 'e' * 64
        with patch.object(host, 'release_metadata', return_value=(self.manifest, self.descriptor, REF, 'e' * 64)):
            self.install()
        self.assertEqual(host.read_json(self.state / host.INSTALLATION_RECEIPT)['floor'], observed)
        self.assertEqual(host.read_json(self.state / 'config.json')['deployment_file'], str(self.canonical))

    def test_unsafe_paths_overrides_and_multifile_refused_before_mutation(self):
        for kind in ['override', 'directory-mode', 'symlink', 'multiple', 'extra-service']:
            with self.subTest(kind=kind):
                if kind == 'override':
                    (self.source / 'compose.override.yaml').write_text('services: {}')
                elif kind == 'directory-mode':
                    self.path.chmod(0o777)
                elif kind == 'symlink':
                    self.canonical.rename(self.source / 'saved.yaml')
                    self.canonical.symlink_to(self.source / 'saved.yaml')
                elif kind == 'multiple':
                    self.options = ['-f', str(self.canonical), '-f', str(self.canonical)]
                else:
                    value = host.read_json(self.canonical)
                    value['services']['other'] = {'image': 'other'}
                    host.atomic(self.canonical, value, mode=0o640)
                with self.assertRaises((host.Failure, OSError)):
                    self.install()
                self.assertFalse(self.state.exists())
                self.assertFalse(self.launcher.exists())
                (self.source / 'compose.override.yaml').unlink(missing_ok=True)
                self.path.chmod(0o700)
                if self.canonical.is_symlink():
                    self.canonical.unlink()
                    (self.source / 'saved.yaml').rename(self.canonical)
                host.atomic(self.canonical, self.raw, mode=0o640)
                self.options = []

    def test_explicit_selection_requires_both_binary_capabilities(self):
        original = self.docker
        def docker(*args, **kwargs):
            if args[0] == 'run' and args[-1] == 'server-enrollment-capabilities':
                value = {'runtime_guard': 2, 'enrollment_preflight': 1, 'version': self.args.expect_version, 'commit': self.args.expect_commit}
                return subprocess.CompletedProcess(args, 0, json.dumps(value).encode(), b'')
            return original(*args, **kwargs)
        with patch.object(host, 'docker', docker), self.assertRaisesRegex(host.Failure, 'canonical Compose capability'):
            self.install()
        self.assertFalse(self.launcher.exists())
        self.assertFalse(self.state.exists())

    def test_layout_declaration_selects_standard_only_and_cannot_inject_paths(self):
        self.args.deployment_layout = ''
        receipt = {'api': 1, 'deployment_layout': host.SINGLE_LAYOUT, 'repository': 'test/repo', 'version': 'v1.0.1', 'commit': 'b' * 40, 'channel': 'stable', 'manifest_sha256': 'f' * 64, 'image': REF}
        host.atomic(self.source / 'release.json', dict(receipt, path='/untrusted'))
        with self.assertRaisesRegex(host.Failure, 'Invalid standard deployment layout declaration'):
            self.install()
        host.atomic(self.source / 'release.json', receipt)
        self.install()
        self.assertEqual(host.read_json(self.state / 'config.json')['deployment_layout'], host.SINGLE_LAYOUT)

    def test_missing_canonical_requires_recorded_initial_activation_and_candidate(self):
        self.install()
        self.canonical.unlink()
        with self.assertRaisesRegex(host.Failure, 'missing outside a recorded initial activation'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertFalse(self.canonical.exists())


    def test_rolled_back_single_repair_blocker_preserves_all_transaction_bytes(self):
        self.failure = 'health'
        with self.assertRaisesRegex(host.Failure, 'health failure'):
            self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'rolled_back')
        before = {path: path.read_bytes() for path in (self.canonical, self.inputs, self.state / 'host.json')}
        original = self.docker
        def blocked(*args, **kwargs):
            if args[0] == 'exec' and args[-1] == 'server-enrollment-preflight':
                return subprocess.CompletedProcess(args, 0, json.dumps({'api': 1, 'installation_id': ID, 'version': self.args.expect_version, 'commit': self.args.expect_commit, 'ready': False, 'blockers': [{'code': 'agent_updates_active', 'action': 'ignored', 'message': 'ignored'}]}).encode(), b'')
            return original(*args, **kwargs)
        self.commands.clear()
        with patch.object(host, 'docker', blocked), self.assertRaises(host.EnrollmentPreflightFailure):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual({path: path.read_bytes() for path in before}, before)
        self.assertFalse(any(c[0] == 'binary' or c[0] == 'compose' and c[1][0] in {'stop', 'start', 'up'} for c in self.commands))

    def test_repair_recovers_only_executor_without_server_replacement(self):
        self.install()
        before = {p: p.read_bytes() for p in (self.canonical, self.inputs, self.state / 'host.json')}
        self.failure = 'socket'
        original = host.Host.start_executor
        def restarted(controller):
            self.failure = ''
            return original(controller)
        self.commands.clear()
        with patch.object(host.Host, 'start_executor', restarted):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(self.restart_count, 1)
        self.assertEqual({p: p.read_bytes() for p in before}, before)
        self.assertTrue(any(c[0] == 'compose' and c[1] == ('stop', 'p2pstream-server-updater') for c in self.commands))
        self.assertFalse(any(c[0] == 'compose' and c[1][0] in {'up', 'stop', 'start'} and c[1][-1] == 'p2pstream' for c in self.commands))

    def test_executor_repair_refuses_native_hash_drift(self):
        self.install()
        self.failure = 'socket'
        self.info['Config']['Labels']['com.docker.compose.config-hash'] = '0' * 64
        self.commands.clear()
        with self.assertRaisesRegex(host.Failure, 'Running settings differ'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertFalse(any(c[0] == 'compose' and c[1][0] in {'up', 'stop', 'start'} for c in self.commands))

    def test_existing_owner_without_history_never_gets_new_generation(self):
        original = self.docker
        def owner(*args, **kwargs):
            if args[0] == 'exec' and args[-1] == host.OWNER_ABSENT:
                return subprocess.CompletedProcess(args, 1, b'', b'')
            return original(*args, **kwargs)
        with patch.object(host, 'docker', owner), self.assertRaisesRegex(host.Failure, 'Runtime ownership remains'):
            self.install()
        self.assertFalse(self.state.exists())
        self.assertFalse(self.launcher.exists())

    def test_lost_private_history_with_enrolled_canonical_is_not_fresh_install(self):
        self.install()
        # Emulate lost/moved private state without changing any runtime inputs.
        (self.state / 'host.json').rename(self.state / 'host-lost.json')
        original = self.canonical.read_bytes()
        with self.assertRaisesRegex(host.Failure, 'private enrollment history is missing'):
            self.install()
        self.assertEqual(self.canonical.read_bytes(), original)

    def test_prepare_interruption_keeps_yaml_then_repair_uses_private_candidate(self):
        self.failure = 'layout'
        original = self.canonical.read_bytes()
        with self.assertRaisesRegex(host.Failure, 'unsupported layout'):
            self.install()
        self.assertEqual(self.canonical.read_bytes(), original)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'preparing')
        self.failure = ''
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_original_missing_server_rollback_stays_strict(self):
        original = host.Host.compose
        def interrupted(controller, *args, **kwargs):
            if args[0] == 'up' and '--no-start' in args and args[-1] == 'p2pstream':
                self.server_inventory = b''
                raise host.Failure('missing replacement')
            return original(controller, *args, **kwargs)
        with patch.object(host.Host, 'compose', interrupted), self.assertRaisesRegex(host.Failure, 'missing replacement'):
            self.install()
        before = self.canonical.read_bytes()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'activating')
        with self.assertRaisesRegex(host.Failure, 'refusing to recreate a missing deployment'):
            host.manage(argparse.Namespace(action='rollback'))
        self.assertEqual(self.canonical.read_bytes(), before)
        with patch.object(host.Host, 'data_identity', return_value=ID):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_declared_deploy_prepares_only_protected_standard_inputs(self):
        self.canonical.unlink()
        args = argparse.Namespace(deployment_layout=host.SINGLE_LAYOUT, directory=str(self.source), repository='test/repo', release='v1.0.1')
        self.manifest.update(commit='b' * 40, channel='stable')
        with patch.object(host, '__file__', str(self.bundle / 'server-updater-host.py')):
            host.deploy(args)
        self.assertIn(REF.encode(), self.canonical.read_bytes())
        self.assertEqual(host.read_json(self.source / 'release.json')['deployment_layout'], host.SINGLE_LAYOUT)
        self.assertEqual(stat.S_IMODE(self.canonical.stat().st_mode), 0o640)
        self.assertFalse(self.state.exists())
        self.assertFalse(any(c[0] == 'compose' and c[1][0] in {'up', 'stop', 'start'} for c in self.commands))
        self.args.deployment_layout = ''
        self.model['services']['p2pstream']['image'] = REF
        self.raw = json.dumps(self.model).encode()
        self.install()
        self.assertEqual(host.read_json(self.state / 'config.json')['deployment_layout'], host.SINGLE_LAYOUT)

    def test_arbitrary_yaml_include_rejected_before_compose_or_state(self):
        self.canonical.write_text('services:\n  p2pstream: {}\n"include": other.yaml\n')
        self.commands.clear()
        with self.assertRaisesRegex(host.Failure, 'published standalone template'):
            self.install()
        self.assertEqual(self.commands, [])
        self.assertFalse(self.state.exists())


    def test_apply_rejects_hidden_input_includes_before_compose_resolution(self):
        self.install()
        model = host.read_json(self.inputs)
        model['include'] = ['/operator/unrelated.yaml']
        host.atomic(self.inputs, model, mode=0o640)
        before = self.canonical.read_bytes()
        self.commands.clear()
        with self.assertRaisesRegex(host.Failure, 'without includes'):
            host.manage(argparse.Namespace(action='apply'))
        self.assertEqual(self.canonical.read_bytes(), before)
        self.assertFalse(any(c[0] == 'docker' and 'config' in c[1] for c in self.commands))
        self.assertFalse(any(c[0] == 'compose' and c[1][0] in {'stop', 'start', 'up'} for c in self.commands))

    def test_unexpected_owner_after_preparation_cannot_be_adopted(self):
        original = self.docker
        def owner(*args, **kwargs):
            if args[0] == 'exec' and args[-1] == host.OWNER_ABSENT and (self.state / 'config.json').exists():
                return subprocess.CompletedProcess(args, 1, b'', b'')
            return original(*args, **kwargs)
        # Readiness succeeds, but public repair must not replace a new unknown
        # owner, including through its generic automatic rollback path.
        prepare = host.Host.prepare_single
        def stop_after_prepare(controller):
            prepare(controller)
            raise KeyboardInterrupt()
        with patch.object(host.Host, 'prepare_single', stop_after_prepare), self.assertRaises(KeyboardInterrupt):
            self.install()
        original_model = self.canonical.read_bytes()
        self.commands.clear()
        before_journal = (self.state / 'host.json').read_bytes()
        with patch.object(host, 'docker', owner), self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'Unexpected runtime ownership'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual((self.state / 'host.json').read_bytes(), before_journal)
        self.assertEqual(self.canonical.read_bytes(), original_model)
        self.assertFalse(any(c[0] == 'binary' and c[1][0].startswith('runtime-') for c in self.commands))


    def test_private_compose_operations_keep_recorded_project_context(self):
        self.install()
        controller = host.Host()
        with controller.lock():
            # FixtureHost implements native lifecycle results; invoke the real
            # host Compose wrapper to check the helper's exact argument contract.
            real_host = host.Host.__mro__[2]
            with patch.object(controller, 'tools', return_value=subprocess.CompletedProcess([], 0, b'{}', b'')) as tools:
                real_host.compose(controller, 'config', '--format', 'json', model='before-compose.json')
            args = tools.call_args.args
            self.assertEqual(args[args.index('--project-directory') + 1], str(self.source))
            self.assertEqual(args[args.index('-f') + 1], str(self.state / 'before-compose.json'))

    def test_stable_path_symlink_refused_without_chmod_or_journal(self):
        unrelated = self.path / 'unrelated'
        unrelated.mkdir(mode=0o700)
        self.controller.parent.parent.parent.mkdir(mode=0o750)
        (self.controller.parent.parent).symlink_to(unrelated, target_is_directory=True)
        with self.assertRaisesRegex(host.Failure, 'Unsafe protected deployment path'):
            self.install()
        self.assertFalse((self.state / 'host.json').exists())
        self.assertFalse(self.launcher.exists())
        self.assertEqual(stat.S_IMODE(unrelated.stat().st_mode), 0o700)


    def test_stopped_original_before_activation_does_not_trigger_rollback_mutations(self):
        original = host.Host.prepare_single
        def stopped(controller):
            original(controller)
            self.info['State']['Running'] = False
            raise KeyboardInterrupt()
        with patch.object(host.Host, 'prepare_single', stopped), self.assertRaises(KeyboardInterrupt):
            self.install()
        before = {p: p.read_bytes() for p in (self.canonical, self.inputs, self.state / 'host.json')}
        self.commands.clear()
        with self.assertRaisesRegex(host.EnrollmentPreflightFailure, 'Original server is stopped'):
            host.manage(argparse.Namespace(action='repair'))
        self.assertEqual({p: p.read_bytes() for p in before}, before)
        self.assertFalse(any(c[0] == 'binary' or c[0] == 'compose' and c[1][0] in {'stop', 'start', 'up'} for c in self.commands))


    def test_diagnostic_daemon_loss_repairs_healthy_without_transaction_rollback(self):
        self.install()
        before = {path: path.read_bytes() for path in (self.canonical, self.inputs, self.state / 'config.json')}
        restarts = self.restart_count
        real_host = host.Host.__mro__[2]
        def unavailable(*args, **kwargs):
            raise host.Failure('daemon unavailable')
        # Exercise public inspection and the real helper journal/settlement path,
        # not a fabricated phase or a direct call to the recovery method.
        with contextlib.redirect_stdout(io.TextIOWrapper(io.BytesIO())), patch.object(host.Host, 'compose', real_host.compose), patch.object(host, 'docker', unavailable):
            with self.assertRaises(host.UnsettledHelper):
                host.manage(argparse.Namespace(action='inspect', compose_args=['config', '--format', 'json']))
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual((journal['phase'], journal['recovery_phase']), ('recovery_required', 'healthy'))
        pending = host.read_json(self.state / 'helper.json')
        self.assertTrue(pending['readonly'])
        self.assertEqual(pending['phase'], 'healthy')
        self.commands.clear()
        host.manage(argparse.Namespace(action='repair'))
        self.assertEqual({path: path.read_bytes() for path in before}, before)
        self.assertEqual(self.restart_count, restarts)
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['phase'], 'healthy')
        self.assertNotIn('recovery_phase', journal)
        self.assertFalse((self.state / 'helper.json').exists())
        self.assertFalse(any(c[0] == 'binary' and c[1][0].startswith('runtime-') or c[0] == 'compose' and c[1][0] in {'up', 'stop', 'start'} and c[1][-1] == 'p2pstream' for c in self.commands))

    def cancel_single_public(self):
        # Fixture native Compose reports the standalone model's hash after the
        # actual controller rollback. All cancellation validation remains real.
        original = host.Host.compose
        def compose(controller, *args, **kwargs):
            result = original(controller, *args, **kwargs)
            if '--hash' in args and kwargs.get('model') == 'before-compose.json':
                return subprocess.CompletedProcess(args, 0, ('p2pstream ' + 'a' * 64 + '\n').encode(), b'')
            return result
        argv = ['server-updater-host.py', 'cancel-enrollment', '--expect-installation', ID,
                '--expect-channel', 'stable', '--repository', 'test/repo', '--bundle', str(self.bundle)]
        with patch.object(host.Host, 'compose', compose), patch.object(sys, 'argv', argv), patch.object(host.signal, 'signal'):
            host.main()

    def failed_tag_enrollment(self):
        self.assertEqual(host.read_json(self.canonical)['services']['p2pstream']['image'], 'ghcr.io/test/repo:v1.0.1')
        self.failure = 'health'
        with self.assertRaisesRegex(host.Failure, 'health failure'):
            self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'rolled_back')
        self.assertEqual(host.read_json(self.canonical)['services']['p2pstream']['image'], REF)
        self.assertEqual(host.read_json(self.state / 'input.json')['services']['p2pstream']['image'], 'ghcr.io/test/repo:v1.0.1')

    def test_tag_input_failed_enrollment_rollback_can_cancel_publicly(self):
        self.failed_tag_enrollment()
        before = {path: path.read_bytes() for path in (self.canonical, self.inputs, self.launcher, self.controller)}
        info = copy.deepcopy(self.info)
        restarts = self.restart_count
        floor = host.read_json(self.state / 'config.json')['bootstrap_floor']
        self.cancel_single_public()
        self.assertEqual({path: path.read_bytes() for path in before}, before)
        self.assertEqual(self.info, info)
        self.assertEqual(self.restart_count, restarts)
        receipt = host.read_json(self.state / host.CANCELLATION)
        self.assertTrue(receipt['completed'])
        self.assertEqual(receipt['floor'], floor)
        self.assertEqual(host.read_json(self.state / host.INSTALLATION_RECEIPT)['floor'], floor)
        self.assertFalse((self.state / 'host.json').exists())


    def lose_daemon_during_single_inspection(self):
        real_host = host.Host.__mro__[2]
        def unavailable(*args, **kwargs):
            raise host.Failure('daemon unavailable')
        with contextlib.redirect_stdout(io.TextIOWrapper(io.BytesIO())), patch.object(host.Host, 'compose', real_host.compose), patch.object(host, 'docker', unavailable):
            with self.assertRaises(host.UnsettledHelper):
                host.manage(argparse.Namespace(action='inspect', compose_args=['config']))

    def test_diagnostic_recovery_does_not_fabricate_missing_initial_activation(self):
        def removed(controller, *args, **kwargs):
            self.server_inventory = b''
            raise KeyboardInterrupt()
        with patch.object(host.Host, 'deploy_server', removed), self.assertRaises(KeyboardInterrupt):
            self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'activating')
        self.lose_daemon_during_single_inspection()
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual((journal['phase'], journal['recovery_phase']), ('recovery_required', 'activating'))
        canonical = self.canonical.read_bytes()
        self.commands.clear()
        with patch.object(host.Host, 'data_identity', side_effect=AssertionError('must not infer initial recovery')), self.assertRaisesRegex(host.MissingRollbackServer, 'refusing to recreate a missing deployment'):
            host.manage(argparse.Namespace(action='repair'))
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual((journal['phase'], journal['recovery_phase']), ('recovery_required', 'activating'))
        self.assertEqual(self.canonical.read_bytes(), canonical)
        self.assertFalse(any(c[0] == 'binary' or c[0] == 'compose' and c[1][0] in {'up', 'stop', 'start'} for c in self.commands))

    def test_diagnostic_loss_during_apply_still_uses_recorded_transaction_rollback(self):
        self.install()
        before = host.read_json(self.canonical)
        edited = host.read_json(self.inputs)
        edited['services']['p2pstream']['environment']['SECRET'] = 'pending apply'
        host.atomic(self.inputs, edited, mode=0o640)
        with patch.object(host.Host, 'deploy_server', side_effect=KeyboardInterrupt()), self.assertRaises(KeyboardInterrupt):
            host.manage(argparse.Namespace(action='apply'))
        self.lose_daemon_during_single_inspection()
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual((journal['phase'], journal['recovery_phase']), ('recovery_required', 'applying'))
        host.manage(argparse.Namespace(action='repair'))
        current = host.read_json(self.canonical)
        self.assertEqual(current['services']['p2pstream']['environment']['SECRET'], before['services']['p2pstream']['environment']['SECRET'])
        self.assertEqual(current['services']['p2pstream']['image'], before['services']['p2pstream']['image'])
        self.assertIn('p2pstream-server-updater', current['services'])
        self.assertEqual(host.read_json(self.inputs), edited)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_unrelated_recovery_routes_refuse_without_rollback_or_phase_fabrication(self):
        self.install()
        original = host.read_json(self.state / 'host.json')
        canonical = self.canonical.read_bytes()
        changes = [
            {'recovery_phase': 'recovering_update', 'recovery_operation': 'paused-update'},
            {'recovery_phase': 'healthy', 'recovery_operation': 'paused-update'},
            {'recovery_phase': 'removed'}, {'recovery_phase': 'recreating'},
            {'recovery_phase': 'migrating_deployment'}, {'recovery_phase': 'unknown'},
            {'recovery_phase': 'healthy', 'verified_version': ''},
        ]
        for change in changes:
            with self.subTest(change=change):
                host.atomic(self.state / 'host.json', dict(original, phase='recovery_required', **change))
                before = (self.state / 'host.json').read_bytes()
                self.commands.clear()
                with self.assertRaises(host.Failure):
                    host.manage(argparse.Namespace(action='repair'))
                self.assertEqual((self.state / 'host.json').read_bytes(), before)
                self.assertEqual(self.canonical.read_bytes(), canonical)
                self.assertFalse(any(c[0] == 'binary' or c[0] == 'compose' and c[1][0] in {'up', 'stop', 'start'} for c in self.commands))

    def test_tag_normalization_cancellation_refuses_changed_models_and_provenance(self):
        self.failed_tag_enrollment()
        paths = [self.canonical, self.inputs, self.state / 'host.json', self.state / 'config.json',
                 self.state / 'input.json', self.state / 'original-source.yaml', self.state / 'staged-compose.yaml',
                 self.state / 'before-compose.json', self.state / 'rollback-compose.json']
        original = {path: (path.read_bytes(), stat.S_IMODE(path.stat().st_mode)) for path in paths}
        original_info = copy.deepcopy(self.info)
        changes = ['canonical-settings', 'canonical-image', 'canonical-format', 'private-input', 'private-source',
                   'private-candidate', 'wrong-transaction', 'changed-running-image', 'unrelated-normalization']
        for change in changes:
            with self.subTest(change=change):
                for path, (data, mode) in original.items():
                    host.atomic(path, data, mode=mode)
                self.info = copy.deepcopy(original_info)
                if change in {'canonical-settings', 'canonical-image'}:
                    model = host.read_json(self.canonical)
                    if change == 'canonical-settings':
                        model['services']['p2pstream']['environment']['SECRET'] = 'unrelated edit'
                    else:
                        model['services']['p2pstream']['image'] = 'ghcr.io/test/repo@sha256:' + '0' * 64
                    host.atomic(self.canonical, model, mode=0o640)
                elif change == 'canonical-format':
                    host.atomic(self.canonical, self.canonical.read_bytes() + b'\n', mode=0o640)
                elif change == 'private-input':
                    value = host.read_json(self.state / 'input.json')
                    value['services']['p2pstream']['image'] = 'ghcr.io/test/repo:v9.0.0'
                    host.atomic(self.state / 'input.json', value)
                elif change in {'private-source', 'private-candidate'}:
                    path = self.state / ('original-source.yaml' if change == 'private-source' else 'staged-compose.yaml')
                    host.atomic(path, path.read_bytes() + b'\n')
                elif change == 'wrong-transaction':
                    journal = host.read_json(self.state / 'host.json')
                    journal['canonical_transaction']['kind'] = 'apply'
                    host.atomic(self.state / 'host.json', journal)
                elif change == 'changed-running-image':
                    self.info['Image'] = 'sha256:foreign'
                else:
                    # Even mutually matching before/rollback/current models do
                    # not authorize a change beyond the original image pin.
                    model = host.read_json(self.canonical)
                    model['services']['p2pstream']['environment']['SECRET'] = 'unrecorded normalization'
                    raw = (json.dumps(model, indent=2) + '\n').encode()
                    for path in (self.canonical, self.state / 'before-compose.json', self.state / 'rollback-compose.json'):
                        host.atomic(path, raw, mode=original[path][1])
                    journal = host.read_json(self.state / 'host.json')
                    journal.update(before_sha256=host.digest(raw), single_rollback_sha256=host.digest(raw))
                    host.atomic(self.state / 'host.json', journal)
                before = {path: path.read_bytes() for path in paths}
                self.commands.clear()
                with contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as refused:
                    self.cancel_single_public()
                self.assertEqual(refused.exception.code, 1)
                self.assertEqual({path: path.read_bytes() for path in paths}, before)
                self.assertFalse((self.state / host.CANCELLATION).exists())
                self.assertFalse((self.state / 'archive.json').exists())
                self.assertFalse(any(c[0] == 'binary' or c[0] == 'docker' and c[1][0] == 'rm' or c[0] == 'compose' and c[1][0] in {'stop', 'start', 'up'} for c in self.commands))

    def test_tag_normalization_cancellation_refuses_different_native_resolution(self):
        self.failed_tag_enrollment()
        original = self.docker
        for field in ['image', 'settings']:
            def changed(*args, **kwargs):
                result = original(*args, **kwargs)
                if args[0] == 'compose' and 'config' in args and '--format' in args:
                    value = host.decode(result.stdout)
                    if field == 'image':
                        value['services']['p2pstream']['image'] = 'ghcr.io/test/repo:v1.0.1'
                    else:
                        value['services']['p2pstream']['environment']['SECRET'] = 'different native settings'
                    return subprocess.CompletedProcess(args, 0, json.dumps(value).encode(), b'')
                return result
            before = self.canonical.read_bytes()
            with self.subTest(field=field), patch.object(host, 'docker', changed), contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                self.cancel_single_public()
            self.assertEqual(self.canonical.read_bytes(), before)
            self.assertFalse((self.state / host.CANCELLATION).exists())


class Metadata(unittest.TestCase):
    def test_duplicate_keys_and_restricted_compose_options(self):
        with self.assertRaisesRegex(host.Failure, 'Duplicate'):
            host.decode('{"version":"x","version":"y"}')
        for args in [['up'], ['--profile', 'custom'], ['-p', '--bad']]:
            with self.assertRaises(host.Failure):
                host.compose_options(args)
    def test_remove_preserves_current_image_ports_settings_and_other_services(self):
        model = {'services': {'p2pstream': {'image': REF, 'environment': {'SERVER_UPDATE_TOKEN': 'secret', 'CONFIG_DIR': '/data', 'OTHER': '$$secret'}, 'labels': {'p2pstream.server-update.instance': ID, 'operator': 'retained'}, 'volumes': [{'target': '/data', 'source': 'data'}, {'target': '/run/p2pstream-server-update'}], 'ports': [9443]}, 'p2pstream-server-updater': {'image': HELPER}, 'other': {'image': 'other'}}, 'volumes': {'data': {'name': 'data'}, 'server-updater-data': {'external': True}}}
        result = host.decode(host.remove_model(model))
        self.assertEqual(result['services']['p2pstream']['image'], REF)
        self.assertEqual(result['services']['p2pstream']['ports'], [9443])
        self.assertEqual(result['services']['p2pstream']['environment'], {'CONFIG_DIR': '/data', 'OTHER': '$$secret'})
        self.assertEqual(result['services']['other'], model['services']['other'])
        self.assertNotIn('p2pstream-server-updater', result['services'])

class CancellationLifecycle(unittest.TestCase):
    """Cancellation proves restoration before archiving a failed first enrollment."""
    tearDown = Lifecycle.tearDown
    install = Lifecycle.install

    def setUp(self):
        self.executor = None
        self.owner_exists = False
        self.helper_exists = False
        self.health_failed = False
        Lifecycle.setUp(self)
        self.managed = True
        self.failure = 'health'
        with self.assertRaises(host.Failure):
            self.install()
        cfg = host.read_json(self.state / 'config.json')
        self.floor = {'sequence': 12, 'security_epoch': 2, 'minimum_safe_version': 'v1.0.0', 'manifest_sha256': 'f' * 64}
        cfg.update(channel='stable', bootstrap_floor=self.floor)
        host.atomic(self.state / 'config.json', cfg)
        self.manifest.update(sequence=13, security_epoch=2, minimum_safe_version='v1.0.0')
        fixture_compose = host.Host.compose
        def compose(controller, *args, **kwargs):
            result = fixture_compose(controller, *args, **kwargs)
            if '--hash' in args and kwargs.get('model') == 'before-compose.json':
                return subprocess.CompletedProcess(args, 0, ('p2pstream ' + 'a' * 64 + '\n').encode(), b'')
            return result
        self.stack.enter_context(patch.object(host.Host, 'compose', compose))
        self.executor = {'Id': EXECUTOR_ID, 'Image': self.helper['Id'], 'State': {'Running': False}, 'Config': {'Labels': {'com.docker.compose.project': 'custom-project', 'com.docker.compose.service': 'p2pstream-server-updater', 'p2pstream.server-update.executor': ID}}}
        self.commands.clear()

    def docker(self, *args, **kwargs):
        if args[0] == 'inspect' and args[-1] in {EXECUTOR_ID, EXECUTOR_ID[:12]}:
            self.commands.append(('docker', args))
            return subprocess.CompletedProcess(args, 0, json.dumps([self.executor]).encode(), b'')
        if args[0] == 'ps' and any('server-update.executor=' in str(value) for value in args):
            self.commands.append(('docker', args))
            # Docker's quiet inventory abbreviates IDs unless --no-trunc is
            # explicit, while inspect always returns the full immutable ID.
            inventory_id = EXECUTOR_ID if '--no-trunc' in args else EXECUTOR_ID[:12]
            return subprocess.CompletedProcess(args, 0, (inventory_id + '\n').encode() if self.executor else b'', b'')
        if args[0] == 'ps' and any('host-helper=' in str(value) for value in args) and self.helper_exists:
            return subprocess.CompletedProcess(args, 0, b'abandoned-helper\n', b'')
        if args[0] == 'rm':
            self.commands.append(('docker', args))
            self.assertEqual(args, ('rm', EXECUTOR_ID))
            self.executor = None
            return subprocess.CompletedProcess(args, 0, b'', b'')
        if args[0] == 'exec' and (args[-1] == 'server-health' and self.health_failed or '/bin/sh' in args and self.owner_exists):
            return subprocess.CompletedProcess(args, 1, b'', b'')
        return Lifecycle.docker(self, *args, **kwargs)

    def cancel(self):
        controller = host.Host()
        with controller.lock(allow_archive=True):
            controller.cancel_enrollment(self.args)

    def assert_refused(self, message):
        with self.assertRaisesRegex(host.Failure, message):
            self.cancel()
        self.assertTrue((self.state / 'host.json').exists())
        self.assertTrue((self.state / 'config.json').exists())
        self.assertFalse((self.state / host.CANCELLATION).exists())
        self.assertFalse(any(command[:2] == ('docker', ('rm', EXECUTOR_ID)) for command in self.commands))

    def test_cancel_retains_original_server_sources_and_active_floor(self):
        before = copy.deepcopy(self.info)
        source = (self.source / 'compose.yaml').read_bytes()
        restarts = self.restart_count
        self.cancel()
        self.assertEqual(self.info, before)
        self.assertEqual(self.restart_count, restarts)
        self.assertEqual((self.source / 'compose.yaml').read_bytes(), source)
        self.assertIsNone(self.executor)
        self.assertFalse((self.state / 'host.json').exists())
        self.assertFalse((self.state / 'config.json').exists())
        receipt = host.read_json(self.state / host.CANCELLATION)
        self.assertEqual(receipt['floor'], self.floor)
        self.assertTrue(receipt['completed'])
        archives = list((self.state / 'history').iterdir())
        self.assertEqual(len(archives), 1)
        self.assertEqual(host.read_json(archives[0] / 'config.json')['bootstrap_floor'], self.floor)
        self.cancel()  # Explicit retry after completed cleanup remains harmless.

    def test_cancel_then_fresh_enrollment_enforces_and_retains_floor(self):
        self.cancel()
        self.executor = None
        self.failure = ''
        self.install()
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')
        self.assertEqual(host.read_json(self.state / host.CANCELLATION)['floor'], self.floor)

    def test_changed_source_refused(self):
        (self.source / 'compose.yaml').write_text('changed source')
        self.assert_refused('Compose source changed')

    def test_changed_resolved_settings_refused(self):
        self.model['services']['p2pstream']['environment']['SECRET'] = 'changed'
        self.raw = json.dumps(self.model).encode()
        self.assert_refused('Resolved original Compose inputs changed')

    def test_changed_immutable_image_refused(self):
        self.info['Image'] = 'sha256:other'
        self.assert_refused('Original running image')

    def test_changed_running_hash_refused(self):
        self.info['Config']['Labels']['com.docker.compose.config-hash'] = 'b' * 64
        self.assert_refused('Running original settings')

    def test_changed_data_volume_refused(self):
        self.info['Mounts'][0]['Name'] = 'other-data'
        self.assert_refused('Original data volume')

    def test_changed_identity_refused(self):
        self.identity = '22222222-2222-4222-8222-222222222222'
        self.assert_refused('Original installation identity')

    def test_wrong_selected_installation_refused(self):
        self.args.expect_installation = '22222222-2222-4222-8222-222222222222'
        self.assert_refused('Saved enrollment differs')

    def test_unhealthy_original_refused(self):
        self.health_failed = True
        self.assert_refused('Original server is unhealthy')

    def test_runtime_ownership_refused(self):
        self.owner_exists = True
        self.assert_refused('Runtime ownership remains')

    def test_other_data_writer_refused(self):
        self.foreign_writer = True
        self.assert_refused('Another running container writes')

    def test_running_executor_refused(self):
        self.executor['State']['Running'] = True
        self.assert_refused('executor is still active')

    def test_foreign_executor_refused(self):
        self.executor['Image'] = 'sha256:other'
        self.assert_refused('executor identity differs')

    def test_executor_lock_refused(self):
        with (self.state / 'executor.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.assert_refused('executor still owns')

    def test_pending_helper_refused(self):
        host.atomic(self.state / 'helper.json', {})
        self.assert_refused('not a recorded read-only cancellation')

    def test_helper_without_record_refused(self):
        self.helper_exists = True
        self.assert_refused('host helper still exists')

    def test_any_update_state_including_preview_refused(self):
        host.atomic(self.state / 'state.json', {'plan': {}, 'operation': None})
        self.assert_refused('Update history or maintenance')

    def test_maintenance_refused(self):
        (self.state / 'control' / 'maintenance').write_text('blocked')
        self.assert_refused('Update history or maintenance')

    def test_previous_success_refused(self):
        journal = host.read_json(self.state / 'host.json')
        journal['verified_version'] = 'v1.0.1'
        host.atomic(self.state / 'host.json', journal)
        self.assert_refused('previously enabled')

    def test_original_enrolled_model_refused(self):
        before = host.read_json(self.state / 'before-compose.json')
        before['services']['p2pstream-server-updater'] = {'image': HELPER}
        host.atomic(self.state / 'before-compose.json', before)
        journal = host.read_json(self.state / 'host.json')
        journal['before_sha256'] = host.digest((self.state / 'before-compose.json').read_bytes())
        host.atomic(self.state / 'host.json', journal)
        self.assert_refused('already enrolled')

    def test_running_binding_refused(self):
        self.info['Config']['Env'] = ['SERVER_UPDATE_TOKEN=unexpected']
        self.assert_refused('retains an updater binding')

    def test_archive_interrupted_before_commit_requires_cancellation_retry(self):
        copy2 = host.shutil.copy2
        def interrupted(source, target, **kwargs):
            if Path(source).name == 'host.json':
                # Exercise a partial copy containing the read-only token even
                # when filesystem iteration happens to visit host.json first.
                token = Path(target).parent / 'control' / 'token'
                if not token.exists():
                    token.parent.mkdir(exist_ok=True)
                    copy2(self.state / 'control' / 'token', token)
                raise KeyboardInterrupt()
            return copy2(source, target, **kwargs)
        with patch.object(host.shutil, 'copy2', side_effect=interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        self.assertTrue((self.state / 'host.json').exists())
        self.assertTrue((self.state / 'manage').exists())
        self.assertEqual(host.read_json(self.state / host.CANCELLATION)['floor'], self.floor)
        with self.assertRaisesRegex(host.Failure, 'cancellation is incomplete'):
            self.install()
        token = self.state / 'control' / 'token'
        archived_token = self.state / 'history' / host.read_json(self.state / 'archive.json')['id'] / 'control' / 'token'
        token_bytes = token.read_bytes()
        self.assertEqual(stat.S_IMODE(token.stat().st_mode), 0o444)
        self.assertEqual(stat.S_IMODE(archived_token.stat().st_mode), 0o444)
        # The production controller runs as root and can overwrite this 0444
        # copy. Model that privilege only on the fixture's existing destination
        # so unprivileged CI can retry; the source token remains read-only.
        archived_token.chmod(0o600)
        self.cancel()
        self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])
        self.assertFalse((self.state / 'host.json').exists())
        self.assertEqual(archived_token.read_bytes(), token_bytes)
        self.assertEqual(stat.S_IMODE(archived_token.stat().st_mode), 0o444)

    def test_pending_cancellation_blocks_repair_and_helper_backed_inspection(self):
        with patch.object(host.Host, 'archive_state', side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        self.assertFalse(host.read_json(self.state / host.CANCELLATION)['completed'])
        for action in ('repair', 'rollback', 'logs', 'inspect', 'status'):
            with self.subTest(action=action):
                self.commands.clear()
                with self.assertRaisesRegex(host.Failure, 'cancellation is incomplete'):
                    host.manage(argparse.Namespace(action=action, compose_args=['ps']))
                self.assertEqual(self.commands, [])
                self.assertFalse((self.state / 'helper.json').exists())
        self.cancel()
        self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])

    def pending_cancellation_helper(self):
        with patch.object(host.Host, 'archive_state', side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        controller = host.Host()
        with controller.lock(allow_archive=True):
            pending = {'name': controller.helper_name(), 'entrypoint': '/usr/local/bin/docker', 'phase': 'rolled_back', 'readonly': True,
                       'cancellation_validation': True, 'cancellation_args': list(controller.cancellation_helper_args()[0])}
        host.atomic(self.state / 'helper.json', pending)
        return pending

    def test_cancellation_retry_settles_only_its_recorded_readonly_helper(self):
        self.pending_cancellation_helper()
        self.cancel()
        self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])
        self.assertFalse((self.state / 'helper.json').exists())

    def first_cancellation_cleanup_failure(self, previous_receipt=False):
        if previous_receipt:
            host.atomic(self.state / host.CANCELLATION, {'api': 1, 'installation': ID, 'repository': 'test/repo', 'channel': 'stable', 'floor': self.floor, 'completed': True})
        original_host = host.Host.__bases__[0]
        fixture_compose = host.Host.compose
        docker = host.docker
        helper_started = False
        def compose(controller, *args, **kwargs):
            if args == ('ps', '--all', '--quiet', 'p2pstream'):
                return original_host.compose(controller, *args, **kwargs)
            return fixture_compose(controller, *args, **kwargs)
        def unavailable(*args, **kwargs):
            nonlocal helper_started
            if args[0] == 'run':
                helper_started = True
                return subprocess.CompletedProcess(args, 0, (self.info['Id'] + '\n').encode(), b'')
            if helper_started and args[0] == 'ps' and any('host-helper=' in str(value) for value in args):
                raise host.Failure('Docker inventory unavailable after validation helper')
            return docker(*args, **kwargs)
        with patch.object(host.Host, 'compose', compose), patch.object(host, 'docker', side_effect=unavailable):
            with self.assertRaisesRegex(host.UnsettledHelper, 'restore Docker access and retry the verified cancellation command'):
                self.cancel()
        self.assertEqual((self.state / host.CANCELLATION).exists(), previous_receipt)
        if previous_receipt:
            self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['phase'], 'recovery_required')
        self.assertEqual(journal['recovery_phase'], 'rolled_back')
        self.assertTrue((self.state / 'helper.json').exists())

    def assert_first_cancellation_cleanup_retries_without_restart(self, previous_receipt=False):
        before = copy.deepcopy(self.info)
        restarts = self.restart_count
        self.first_cancellation_cleanup_failure(previous_receipt)
        self.cancel()
        self.assertEqual(self.info, before)
        self.assertEqual(self.restart_count, restarts)
        self.assertFalse(any(command[0] == 'compose' and command[1][0] in {'up', 'stop', 'start'} for command in self.commands))
        self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])
        self.assertEqual(host.read_json(self.state / host.CANCELLATION)['floor'], self.floor)
        self.assertFalse((self.state / 'helper.json').exists())

    def test_first_cancellation_cleanup_failure_retries_without_receipt_or_restart(self):
        self.assert_first_cancellation_cleanup_retries_without_restart()

    def test_first_cancellation_cleanup_failure_with_earlier_receipt_retries(self):
        self.assert_first_cancellation_cleanup_retries_without_restart(previous_receipt=True)

    def test_first_cancellation_cleanup_retry_refuses_unsafe_helper(self):
        self.first_cancellation_cleanup_failure()
        pending = host.read_json(self.state / 'helper.json')
        for field, value in [('cancellation_validation', False), ('readonly', False), ('entrypoint', '/app/p2pstream'), ('phase', 'applying'), ('name', 'other-helper'), ('cancellation_args', ['compose', 'up', '-d'])]:
            with self.subTest(field=field):
                changed = dict(pending)
                changed[field] = value
                host.atomic(self.state / 'helper.json', changed)
                self.commands.clear()
                with self.assertRaisesRegex(host.Failure, 'not a recorded read-only cancellation'):
                    self.cancel()
                self.assertTrue((self.state / 'helper.json').exists())
                self.assertFalse((self.state / host.CANCELLATION).exists())
                self.assertEqual(self.commands, [])

    def test_first_cancellation_cleanup_retry_refuses_update_history(self):
        self.first_cancellation_cleanup_failure()
        host.atomic(self.state / 'state.json', {'plan': {}, 'operation': None})
        pending = (self.state / 'helper.json').read_bytes()
        self.commands.clear()
        with self.assertRaisesRegex(host.Failure, 'Update history or maintenance exists'):
            self.cancel()
        self.assertEqual((self.state / 'helper.json').read_bytes(), pending)
        self.assertFalse((self.state / host.CANCELLATION).exists())
        self.assertEqual(self.commands, [])

    def test_cancellation_helper_daemon_failure_preserves_then_resumes_recovery(self):
        self.pending_cancellation_helper()
        docker = host.docker
        def unavailable(*args, **kwargs):
            if args[0] == 'ps' and any('host-helper=' in str(value) for value in args):
                raise host.Failure('Docker unavailable')
            return docker(*args, **kwargs)
        with patch.object(host, 'docker', side_effect=unavailable):
            with self.assertRaisesRegex(host.UnsettledHelper, 'retry the verified cancellation command'):
                self.cancel()
        journal = host.read_json(self.state / 'host.json')
        self.assertEqual(journal['phase'], 'recovery_required')
        self.assertEqual(journal['recovery_phase'], 'rolled_back')
        self.assertTrue((self.state / 'helper.json').exists())
        self.assertEqual(host.read_json(self.state / host.CANCELLATION)['floor'], self.floor)
        self.cancel()
        self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])

    def test_cancellation_retry_refuses_unmarked_mutating_and_wrong_phase_helpers(self):
        original = self.pending_cancellation_helper()
        cases = [('cancellation_validation', False), ('readonly', False), ('entrypoint', '/app/p2pstream'), ('phase', 'applying'), ('name', 'other-helper'), ('cancellation_args', ['compose', 'up', '-d'])]
        for field, value in cases:
            with self.subTest(field=field):
                pending = dict(original)
                pending[field] = value
                host.atomic(self.state / 'helper.json', pending)
                self.commands.clear()
                with self.assertRaisesRegex(host.Failure, 'not a recorded read-only cancellation'):
                    self.cancel()
                self.assertTrue((self.state / 'helper.json').exists())
                self.assertEqual(self.commands, [])

    def test_actual_pinned_validation_helper_marks_exact_readonly_command(self):
        self.pending_cancellation_helper()
        (self.state / 'helper.json').unlink()
        controller = host.Host()
        original_host = host.Host.__bases__[0]
        docker = host.docker
        def interrupted(*args, **kwargs):
            if args[0] == 'run':
                raise KeyboardInterrupt()
            return docker(*args, **kwargs)
        with controller.lock(allow_archive=True):
            controller._cancellation_validation = True
            # Model a killed controller, whose normal exception cleanup did not
            # run. The production run_helper writes the real receipt first.
            with patch.object(controller, 'settle_helper', side_effect=[None, KeyboardInterrupt()]), patch.object(host, 'docker', side_effect=interrupted):
                with self.assertRaises(KeyboardInterrupt):
                    original_host.compose(controller, 'ps', '--all', '--quiet', 'p2pstream')
            pending = host.read_json(self.state / 'helper.json')
            self.assertTrue(pending['cancellation_validation'])
            self.assertTrue(pending['readonly'])
            self.assertEqual(tuple(pending['cancellation_args']), controller.cancellation_helper_args()[0])
        self.cancel()
        self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])

    def test_cancellation_helper_marker_cannot_authorize_other_docker_commands(self):
        self.pending_cancellation_helper()
        (self.state / 'helper.json').unlink()
        controller = host.Host()
        with controller.lock(allow_archive=True):
            controller._cancellation_validation = True
            with self.assertRaisesRegex(host.Failure, 'Unexpected cancellation validation helper'):
                host.Host.__bases__[0].compose(controller, 'logs', '--tail', '100', 'p2pstream')
            self.assertFalse((self.state / 'helper.json').exists())

    def test_archive_interrupted_after_commit_resumes_without_server_changes(self):
        with patch.object(host.Host, 'finish_archive', side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        self.assertFalse((self.state / 'host.json').exists())
        self.assertTrue((self.state / 'archive.json').exists())
        before = copy.deepcopy(self.info)
        self.cancel()
        self.assertEqual(self.info, before)
        self.assertFalse((self.state / 'archive.json').exists())
        self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])

    def test_new_setup_completes_committed_archive_and_retains_floor(self):
        with patch.object(host.Host, 'finish_archive', side_effect=KeyboardInterrupt()):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        self.install()
        self.assertTrue(host.read_json(self.state / host.CANCELLATION)['completed'])
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'healthy')

    def test_new_setup_rejects_each_floor_regression_before_pulls(self):
        self.cancel()
        original = copy.deepcopy(self.manifest)
        cases = [('sequence', 11), ('security_epoch', 1), ('minimum_safe_version', 'v0.9.0')]
        for field, value in cases:
            with self.subTest(field=field):
                self.manifest.clear()
                self.manifest.update(original)
                self.manifest[field] = value
                self.commands.clear()
                with self.assertRaisesRegex(host.Failure, 'retained enrollment floor'):
                    self.install()
                self.assertFalse(any(command[0] == 'docker' and command[1][0] == 'pull' for command in self.commands))
                self.assertFalse((self.state / 'host.json').exists())

    def test_same_sequence_changed_manifest_refused(self):
        self.cancel()
        self.manifest['sequence'] = self.floor['sequence']
        self.args.manifest_sha256 = 'e' * 64
        with patch.object(host, 'release_metadata', return_value=(self.manifest, self.descriptor, REF, 'e' * 64)):
            with self.assertRaisesRegex(host.Failure, 'Release identity differs'):
                self.install()

    def test_cancellation_receipt_identity_and_channel_enforced(self):
        self.cancel()
        receipt = host.read_json(self.state / host.CANCELLATION)
        for field, value in [('channel', 'staging'), ('repository', 'other/repo'), ('installation', '22222222-2222-4222-8222-222222222222')]:
            with self.subTest(field=field):
                changed = dict(receipt)
                changed[field] = value
                host.atomic(self.state / host.CANCELLATION, changed)
                with self.assertRaisesRegex(host.Failure, 'Cancellation receipt belongs'):
                    self.install()


class UpgradedCancellationLifecycle(unittest.TestCase):
    """Never infer rollback from a stale phase: prove the current deployment."""
    tearDown = Lifecycle.tearDown
    install = Lifecycle.install
    cancel = CancellationLifecycle.cancel

    def setUp(self):
        CancellationLifecycle.setUp(self)
        self.old_image = copy.deepcopy(self.image)
        self.current_ref = 'ghcr.io/test/repo@sha256:' + '9' * 64
        self.args.upgraded_deployment = True
        self.args.expect_version = 'v1.0.2'
        self.args.expect_commit = '9' * 40
        self.image['Id'] = self.info['Image'] = 'sha256:upgraded'
        self.image['Config']['Labels'].update({'org.opencontainers.image.version': self.args.expect_version, 'org.opencontainers.image.revision': self.args.expect_commit})
        self.model['services']['p2pstream']['image'] = 'ghcr.io/test/repo:v1.0.2'
        self.raw = json.dumps(self.model).encode()
        (self.source / 'compose.yaml').write_bytes(self.raw)
        self.stack.enter_context(patch.object(host, 'release_metadata', return_value=(self.manifest, self.descriptor, self.current_ref, 'f' * 64)))
        self.current_floor = dict(self.floor, sequence=13)
        self.commands.clear()

    def docker(self, *args, **kwargs):
        if args[:2] == ('image', 'inspect') and args[-1] == REF and hasattr(self, 'old_image'):
            self.commands.append(('docker', args))
            return subprocess.CompletedProcess(args, 0, json.dumps([self.old_image]).encode(), b'')
        return CancellationLifecycle.docker(self, *args, **kwargs)

    def files(self):
        return {str(p): p.read_bytes() for directory in (self.state, self.source, self.path / 'p2pstream-server-deployment') if directory.exists() for p in directory.rglob('*') if p.is_file() and p.name not in {'host.lock', 'executor.lock'}}

    def assert_refused(self, message):
        before = self.files()
        server = copy.deepcopy(self.info)
        restarts = self.restart_count
        with self.assertRaisesRegex(host.Failure, message):
            self.cancel()
        self.assertEqual(self.files(), before)
        self.assertEqual(self.info, server)
        self.assertEqual(self.restart_count, restarts)
        self.assertIsNotNone(self.executor)
        self.assertFalse(any(command[0] in {'binary', 'compose'} for command in self.commands))

    def legacy_failure(self):
        # Exact persisted consequence of .106 repairing a .103 enrollment after
        # manual upgrade: old enrolled model + bare recovery_required, no claim
        # that rollback or activation completed. Real Docker tests produce it.
        journal = host.read_json(self.state / 'host.json')
        journal.update(phase='recovery_required', error='Running image changed; configuration rollback failed')
        journal.pop('recovery_phase', None)
        host.atomic(self.state / 'host.json', journal)
        cfg = host.read_json(self.state / 'config.json')
        host.atomic(Path(cfg['deployment_file']), (self.state / 'enrolled-compose.json').read_bytes())

    def bridges(self, alias=False):
        self.legacy_failure()
        journal = host.read_json(self.state / 'host.json')
        if alias:
            path = self.source / 'docker-compose.yml'
            journal['source_bridge']['files'].append({'path': str(path), 'editable': None, 'original_sha256': None, 'original_backup': None, 'bridge': {'include': [host.read_json(self.state / 'config.json')['deployment_file']]}, 'mode': 0o644, 'uid': 0, 'gid': 0})
        for item in journal['source_bridge']['files']:
            host.atomic(Path(item['path']), json.dumps(item['bridge'], separators=(',', ':'), sort_keys=True).encode(), mode=item['mode'])
        host.atomic(self.state / 'host.json', journal)
        return journal

    def assert_retired(self, original_server, restarts):
        self.assertEqual(self.info, original_server)
        self.assertEqual(self.restart_count, restarts)
        self.assertIsNone(self.executor)
        self.assertFalse((self.state / 'host.json').exists())
        receipt = host.read_json(self.state / host.CANCELLATION)
        self.assertTrue(receipt['completed'])
        self.assertEqual(receipt['floor'], self.current_floor)
        self.assertFalse(any(command[0] in {'binary', 'compose'} for command in self.commands))
        archived = next((self.state / 'history').iterdir())
        self.assertEqual(host.read_json(archived / 'config.json')['bootstrap_floor'], self.floor)

    def test_manual_upgrade_then_retire_preserves_current_server_and_sources(self):
        server, restarts = copy.deepcopy(self.info), self.restart_count
        source = (self.source / 'compose.yaml').read_bytes()
        self.cancel()
        self.assert_retired(server, restarts)
        self.assertEqual((self.source / 'compose.yaml').read_bytes(), source)
        self.cancel()

    def test_bare_legacy_recovery_with_enrolled_model_is_proved_without_phase_rewrite(self):
        self.legacy_failure()
        journal = (self.state / 'host.json').read_bytes()
        server, restarts = copy.deepcopy(self.info), self.restart_count
        self.cancel()
        self.assert_retired(server, restarts)
        archived = next((self.state / 'history').iterdir())
        self.assertEqual((archived / 'host.json').read_bytes(), journal)

    def test_proven_bridge_restoration_preserves_server_and_original_input_metadata(self):
        journal = self.bridges(alias=True)
        expected = {item['path']: Path(item['editable']).read_bytes() for item in journal['source_bridge']['files'] if item['editable']}
        modes = {name: stat.S_IMODE(Path(name).stat().st_mode) for name in expected}
        server, restarts = copy.deepcopy(self.info), self.restart_count
        self.cancel()
        self.assert_retired(server, restarts)
        for name, raw in expected.items():
            self.assertEqual(Path(name).read_bytes(), raw)
            self.assertEqual(stat.S_IMODE(Path(name).stat().st_mode), modes[name])
        self.assertFalse((self.source / 'docker-compose.yml').exists())

    def test_bridge_restoration_interruption_reproves_mixed_state_and_retries(self):
        journal = self.bridges(alias=True)
        real_atomic = host.atomic
        first_source = journal['source_bridge']['files'][0]['path']
        def interrupted(path, *args, **kwargs):
            result = real_atomic(path, *args, **kwargs)
            if str(path) == first_source:
                raise KeyboardInterrupt()
            return result
        server, restarts = copy.deepcopy(self.info), self.restart_count
        with patch.object(host, 'atomic', side_effect=interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        self.assertFalse(host.read_json(self.state / host.CANCELLATION)['completed'])
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'recovery_required')
        self.cancel()
        self.assert_retired(server, restarts)

    def test_original_cancellation_does_not_adopt_manual_upgrade(self):
        self.args.upgraded_deployment = False
        self.assert_refused('Original Compose source changed')

    def test_stale_setup_rejects_before_saved_helper_model_or_phase_changes(self):
        before = self.files()
        server = copy.deepcopy(self.info)
        with self.assertRaises(host.Failure) as error:
            self.install()
        self.assertEqual(self.files(), before)
        self.assertEqual(self.info, server)
        self.assertFalse(any(command[0] in {'binary', 'compose'} for command in self.commands))
        self.assertIn('Running release changed after setup was staged', str(error.exception))

    def test_same_release_labels_cannot_hide_staged_image_change(self):
        self.args.expect_version = 'v1.0.1'
        self.args.expect_commit = 'b' * 40
        self.image['Config']['Labels'] = copy.deepcopy(self.old_image['Config']['Labels'])
        before = self.files()
        with self.assertRaises(host.Failure) as error:
            self.install()
        self.assertEqual(self.files(), before)
        self.assertFalse(any(command[0] in {'binary', 'compose'} for command in self.commands))
        self.assertIn('Running image changed after setup was staged', str(error.exception))

    def test_empty_inventory_cannot_redeploy_old_image_during_rollback(self):
        # Model the image edit through .env, leaving source bytes eligible for
        # old rollback. The before-controller must reach its empty-inventory
        # deployment path rather than fail on an unrelated source-byte guard.
        journal = host.read_json(self.state / 'host.json')
        for item in journal['source_bridge']['files']:
            if item['editable']:
                Path(item['path']).write_bytes(Path(item['editable']).read_bytes())
        self.server_inventory = b''
        server, restarts = copy.deepcopy(self.info), self.restart_count
        before_model = Path(host.read_json(self.state / 'config.json')['deployment_file']).read_bytes()
        with self.assertRaisesRegex(host.Failure, 'exactly one existing server for configuration rollback'):
            host.manage(argparse.Namespace(action='rollback'))
        self.assertEqual(self.info, server)
        self.assertEqual(self.restart_count, restarts)
        self.assertEqual(Path(host.read_json(self.state / 'config.json')['deployment_file']).read_bytes(), before_model)
        self.assertFalse(any(command[0] == 'compose' and command[1][0] in {'up', 'stop', 'start'} for command in self.commands))

    def test_failed_activation_cannot_roll_back_a_missing_external_upgrade(self):
        journal = host.read_json(self.state / 'host.json')
        for item in journal['source_bridge']['files']:
            if item['editable']:
                Path(item['path']).write_bytes(Path(item['editable']).read_bytes())
        self.server_inventory = b''
        server, restarts = copy.deepcopy(self.info), self.restart_count
        controller = host.Host()
        with controller.lock():
            with self.assertRaises(host.Failure):
                controller.recover_failure(host.Failure('Running image changed after setup was staged'))
        self.assertEqual(self.info, server)
        self.assertEqual(self.restart_count, restarts)
        self.assertEqual(host.read_json(self.state / 'host.json')['phase'], 'recovery_required')
        self.assertFalse(any(command[0] == 'compose' and command[1][0] in {'up', 'stop', 'start'} for command in self.commands))

    def test_non_image_source_edit_is_refused(self):
        self.model['services']['p2pstream']['environment']['SECRET'] = 'changed'
        self.raw = json.dumps(self.model).encode()
        self.assert_refused('Only the server image may change')

    def test_bridge_with_changed_editable_settings_is_refused(self):
        self.bridges()
        self.model['services']['p2pstream']['ports'][0]['published'] = '9444'
        self.raw = json.dumps(self.model).encode()
        self.assert_refused('Only the server image may change')

    def test_bridge_with_foreign_source_or_alias_is_refused(self):
        journal = self.bridges(alias=True)
        for name in [journal['source_bridge']['files'][0]['path'], str(self.source / 'docker-compose.yml')]:
            with self.subTest(name=name):
                path = Path(name)
                old = path.read_bytes()
                path.write_text('foreign change')
                self.assert_refused('not an owned bridge')
                path.write_bytes(old)

    def test_bridge_with_missing_editable_snapshot_is_refused(self):
        journal = self.bridges()
        Path(journal['source_bridge']['files'][0]['editable']).unlink()
        self.assert_refused('not an owned bridge')

    def test_source_snapshot_change_during_pull_is_refused_before_cleanup(self):
        self.bridges()
        real_docker = self.docker
        def drift(*args, **kwargs):
            if args[0] == 'pull':
                (self.source / 'prod.yaml').write_text('foreign change')
            return real_docker(*args, **kwargs)
        with patch.object(host, 'docker', side_effect=drift):
            with self.assertRaisesRegex(host.Failure, 'Compose source changed during cancellation validation'):
                self.cancel()
        self.assertEqual((self.source / 'prod.yaml').read_text(), 'foreign change')
        self.assertTrue((self.state / 'host.json').exists())

    def test_running_health_logs_may_advance_but_deployment_must_not(self):
        real_docker = self.docker
        def probes(*args, **kwargs):
            result = real_docker(*args, **kwargs)
            if args[0] == 'inspect' and args[-1] != EXECUTOR_ID:
                value = json.loads(result.stdout)
                value[0]['State']['Health'] = {'Status': 'healthy', 'Log': [len(self.commands)]}
                value[0]['ExecIDs'] = [str(len(self.commands))]
                result.stdout = json.dumps(value).encode()
            return result
        server, restarts = copy.deepcopy(self.info), self.restart_count
        with patch.object(host, 'docker', side_effect=probes):
            self.cancel()
        self.assert_retired(server, restarts)

    def test_runtime_start_or_config_drift_during_proof_is_refused(self):
        real_docker = self.docker
        for change in ('start', 'config', 'restart', 'network'):
            inspections = 0
            def drift(*args, **kwargs):
                nonlocal inspections
                result = real_docker(*args, **kwargs)
                if args[0] == 'inspect' and args[-1] != EXECUTOR_ID:
                    inspections += 1
                    if inspections > 1:
                        value = json.loads(result.stdout)
                        if change == 'start':
                            value[0]['State']['StartedAt'] = 'changed'
                        elif change == 'config':
                            value[0]['Config']['Env'] = ['CHANGED=1']
                        elif change == 'restart':
                            value[0]['RestartCount'] = 1
                        else:
                            value[0]['NetworkSettings'] = {'Networks': {'other': {}}}
                        result.stdout = json.dumps(value).encode()
                return result
            with self.subTest(change=change), patch.object(host, 'docker', side_effect=drift):
                self.assert_refused('Upgraded deployment changed')

    def test_release_floor_and_canonical_image_are_enforced(self):
        for field, value, message in [('sequence', 11, 'below the retained'), ('security_epoch', 1, 'below the retained'), ('minimum_safe_version', 'v0.9.0', 'below the retained')]:
            old = self.manifest[field]
            with self.subTest(field=field):
                self.manifest[field] = value
                self.assert_refused(message)
            self.manifest[field] = old
        self.image['Id'] = 'sha256:foreign'
        self.assert_refused('not the upgraded canonical release image')

    def test_current_image_labels_arch_and_publisher_are_enforced(self):
        mutations = [('Architecture', 'arm64', 'platform differs'), ('org.opencontainers.image.version', 'v9.0.0', 'Stale command'), ('org.opencontainers.image.revision', '0' * 40, 'Stale command'), ('org.opencontainers.image.source', 'https://github.com/foreign/repo', 'another publisher')]
        for key, value, message in mutations:
            target = self.image if key == 'Architecture' else self.image['Config']['Labels']
            old = target[key]
            with self.subTest(field=key):
                target[key] = value
                self.assert_refused(message)
            target[key] = old

    def test_wrong_recovery_phase_and_prior_success_are_refused(self):
        original = host.read_json(self.state / 'host.json')
        for change in [{'phase': 'healthy'}, {'phase': 'recovery_required', 'recovery_phase': 'activating'}, {'verified_version': 'v1.0.1'}, {'verified_commit': 'b' * 40}, {'deployment_migration': {'phase': 'started'}}]:
            with self.subTest(change=change):
                host.atomic(self.state / 'host.json', dict(original, **change))
                self.assert_refused('another host operation|previously enabled')
        host.atomic(self.state / 'host.json', original)

    def test_previous_verified_or_update_archive_is_refused(self):
        archive = self.state / 'history' / '22222222-2222-4222-8222-222222222222'
        archive.mkdir(parents=True)
        previous = host.read_json(self.state / 'host.json')
        host.atomic(archive / 'host.json', dict(previous, verified_version='v1.0.1'))
        self.assert_refused('Prior verified enrollment history')
        host.atomic(archive / 'host.json', previous)
        host.atomic(archive / 'state.json', {})
        self.assert_refused('Prior update history')

    def test_partial_archive_before_journal_copy_can_retry(self):
        real_copy = host.shutil.copy2
        def interrupted(source, target, **kwargs):
            if Path(source).name == 'host.json':
                raise KeyboardInterrupt()
            return real_copy(source, target, **kwargs)
        server, restarts = copy.deepcopy(self.info), self.restart_count
        with patch.object(host.shutil, 'copy2', side_effect=interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        for path in (self.state / 'history').rglob('*'):
            if path.is_file():
                path.chmod(0o600)
        self.cancel()
        self.assert_retired(server, restarts)

    def test_partial_archive_with_truncated_journal_can_retry(self):
        real_copy = host.shutil.copy2
        def interrupted(source, target, **kwargs):
            if Path(source).name == 'host.json':
                Path(target).write_bytes(b'{"phase":')
                raise KeyboardInterrupt()
            return real_copy(source, target, **kwargs)
        server, restarts = copy.deepcopy(self.info), self.restart_count
        active = (self.state / 'host.json').read_bytes()
        with patch.object(host.shutil, 'copy2', side_effect=interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        self.assertEqual((self.state / 'host.json').read_bytes(), active)
        archive = next((self.state / 'history').iterdir())
        self.assertEqual((archive / 'host.json').read_bytes(), b'{"phase":')
        for path in archive.rglob('*'):
            if path.is_file():
                path.chmod(0o600)
        self.cancel()
        self.assert_retired(server, restarts)
        self.assertEqual((archive / 'host.json').read_bytes(), active)

    def test_archive_copied_marker_before_commit_can_retry(self):
        real_atomic = host.atomic
        def interrupted(path, value, **kwargs):
            result = real_atomic(path, value, **kwargs)
            if Path(path).name == 'archive.json' and value['stage'] == 'copied':
                raise KeyboardInterrupt()
            return result
        server, restarts = copy.deepcopy(self.info), self.restart_count
        with patch.object(host, 'atomic', side_effect=interrupted):
            with self.assertRaises(KeyboardInterrupt):
                self.cancel()
        self.assertEqual(host.read_json(self.state / 'archive.json')['stage'], 'copied')
        for path in (self.state / 'history').rglob('*'):
            if path.is_file():
                path.chmod(0o600)
        self.cancel()
        self.assert_retired(server, restarts)

    def test_truncated_unrelated_history_is_not_exempted(self):
        archive = self.state / 'history' / '22222222-2222-4222-8222-222222222222'
        archive.mkdir(parents=True)
        (archive / 'host.json').write_bytes(b'{"phase":')
        before = self.files()
        with self.assertRaises(ValueError):
            self.cancel()
        self.assertEqual(self.files(), before)

    def test_cancellation_floor_prevents_old_fresh_enrollment(self):
        self.cancel()
        self.args.expect_version = 'v1.0.1'
        self.args.expect_commit = 'b' * 40
        self.manifest['sequence'] = 12
        self.image = copy.deepcopy(self.old_image)
        self.info['Image'] = self.old_image['Id']
        with self.assertRaisesRegex(host.Failure, 'below the retained enrollment floor'):
            self.install()

    test_changed_identity_refused = CancellationLifecycle.test_changed_identity_refused
    test_wrong_selected_installation_refused = CancellationLifecycle.test_wrong_selected_installation_refused
    test_runtime_ownership_refused = CancellationLifecycle.test_runtime_ownership_refused
    test_other_data_writer_refused = CancellationLifecycle.test_other_data_writer_refused
    test_running_executor_refused = CancellationLifecycle.test_running_executor_refused
    test_foreign_executor_refused = CancellationLifecycle.test_foreign_executor_refused
    test_executor_lock_refused = CancellationLifecycle.test_executor_lock_refused
    test_helper_without_record_refused = CancellationLifecycle.test_helper_without_record_refused
    test_any_update_state_including_preview_refused = CancellationLifecycle.test_any_update_state_including_preview_refused
    test_maintenance_refused = CancellationLifecycle.test_maintenance_refused
    test_original_enrolled_model_refused = CancellationLifecycle.test_original_enrolled_model_refused
    test_running_binding_refused = CancellationLifecycle.test_running_binding_refused

    def test_pending_helper_is_not_settled_by_upgraded_cancellation(self):
        host.atomic(self.state / 'helper.json', {'phase': 'rolled_back', 'readonly': True})
        self.assert_refused('An unfinished helper exists')

    def test_changed_data_volume_refused(self):
        self.info['Mounts'][0]['Name'] = 'other-data'
        self.assert_refused('Running data mount differs')

    def test_unhealthy_current_server_refused(self):
        self.health_failed = True
        self.assert_refused('Upgraded server is unhealthy')

    def test_current_cli_identity_is_required_only_in_explicit_mode(self):
        common = ['host', 'cancel-enrollment', '--expect-installation', ID, '--expect-channel', 'stable', '--repository', 'test/repo', '--bundle', str(self.bundle)]
        params = ['--expect-version', 'v1.0.2', '--expect-commit', '9' * 40, '--expect-arch', 'amd64', '--manifest-sha256', 'f' * 64]
        for index in range(0, len(params), 2):
            with self.subTest(missing=params[index]), patch.object(sys, 'argv', common + ['--upgraded-deployment'] + params[:index] + params[index + 2:]), contextlib.redirect_stderr(io.StringIO()) as error:
                with self.assertRaises(SystemExit):
                    host.main()
                self.assertIn('requires current version, commit, architecture and manifest SHA-256', error.getvalue())
        with patch.object(sys, 'argv', common + params), contextlib.redirect_stderr(io.StringIO()) as error:
            with self.assertRaises(SystemExit):
                host.main()
            self.assertIn('requires --upgraded-deployment', error.getvalue())

class BlockedInitialOwnerCancellation(unittest.TestCase):
    """Real native file commands, mocked Docker inventory and namespace locks."""
    tearDown = Lifecycle.tearDown
    install = Lifecycle.install
    cancel = CancellationLifecycle.cancel
    files = UpgradedCancellationLifecycle.files
    legacy_failure = UpgradedCancellationLifecycle.legacy_failure
    bridges = UpgradedCancellationLifecycle.bridges
    assert_retired = UpgradedCancellationLifecycle.assert_retired

    def setUp(self):
        UpgradedCancellationLifecycle.setUp(self)
        enrolled = host.read_json(self.state / 'enrolled-compose.json')
        enrolled['services']['p2pstream']['environment'].update(
            SERVER_UPDATE_INSTANCE_ID=ID,
            SERVER_UPDATE_SOCKET='/run/p2pstream-server-update/control.sock',
            SERVER_UPDATE_GATE_FILE='/run/p2pstream-server-update/maintenance')
        host.atomic(self.state / 'enrolled-compose.json', enrolled)
        self.legacy_failure()
        self.info['Config']['Cmd'] = ['/app/p2pstream', 'server']
        self.data = self.path / 'data'
        self.data.mkdir()
        self.info['Mounts'][0].update(Source=str(self.data), Type='volume')
        self.volume = {'Name': 'custom-data', 'Driver': 'local', 'Scope': 'local', 'Mountpoint': str(self.data)}
        self.marker = self.data / '.p2pstream-runtime-owner.json'
        self.runtime_lock = self.data / '.p2pstream-runtime.lock'
        self.runtime_lock.touch(mode=0o600)
        self.application = self.data / 'application.db'
        self.application.write_bytes(b'application data is never opened by cleanup')
        self.lock_table = self.path / 'proc-locks'
        lock = self.runtime_lock.stat()
        self.lock_table.write_text(f'42: FLOCK  ADVISORY  WRITE 1 {os.major(lock.st_dev):02x}:{os.minor(lock.st_dev):02x}:{lock.st_ino} 0 EOF\n')
        self.marker_value = {'api': 2, 'instance_id': ID, 'generation': ID, 'allowed': False, 'version': 'v1.0.1', 'commit': 'b' * 40}
        host.atomic(self.marker, self.marker_value, mode=0o644)
        self.original_marker = self.marker.read_bytes()
        self.original_lock = self.runtime_lock.stat()
        self.original_journal = (self.state / 'host.json').read_bytes()
        self.cleanup_fault = None
        self.cleanup_calls = 0
        self.cleanup_before = None
        clear = host.Host.clear_blocked_initial_owner
        def checked_clear(controller, directory, snapshot, lease):
            self.cleanup_calls += 1
            self.assertFalse(host.read_json(self.state / host.CANCELLATION)['completed'])
            self.assertEqual(host.read_json(self.state / host.CANCELLATION)['floor'], self.current_floor)
            archived = host.read_json(self.state / host.BLOCKED_INITIAL_OWNER)
            self.assertEqual(bytes.fromhex(archived['owner']['data_hex']), self.original_marker)
            self.assertEqual((self.state / 'host.json').read_bytes(), self.original_journal)
            for name in ('host.lock', 'executor.lock'):
                with (self.state / name).open('rb') as stream:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
            if self.cleanup_before:
                self.cleanup_before()
            if self.cleanup_fault == 'before':
                raise KeyboardInterrupt()
            if self.cleanup_fault == 'after-unlink':
                with patch.object(os, 'fsync', side_effect=KeyboardInterrupt):
                    clear(controller, directory, snapshot, lease)
            else:
                clear(controller, directory, snapshot, lease)
            if self.cleanup_fault == 'after-sync':
                raise KeyboardInterrupt()
        self.stack.enter_context(patch.object(host.Host, 'clear_blocked_initial_owner', checked_clear))

    def docker(self, *args, **kwargs):
        if args[:2] == ('volume', 'inspect'):
            self.commands.append(('docker', args))
            return subprocess.CompletedProcess(args, 0, json.dumps([self.volume]).encode(), b'')
        native = {host.OWNER_ABSENT, host.OWNER_PROBE, host.LEASE_PROBE}
        if hasattr(self, 'data') and args[:1] == ('exec',) and len(args) >= 5 and args[2:4] == ('/bin/sh', '-c') and args[4] in native:
            self.commands.append(('docker', args))
            script = args[4].replace('/data', str(self.data)).replace('/proc/locks', str(self.lock_table))
            return subprocess.run(['/bin/sh', '-c', script, *args[5:]], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=3)
        return UpgradedCancellationLifecycle.docker(self, *args, **kwargs)

    def assert_data_preserved(self):
        self.assertEqual(self.application.read_bytes(), b'application data is never opened by cleanup')
        lock = self.runtime_lock.stat()
        for field in ('st_dev', 'st_ino', 'st_mode', 'st_uid', 'st_gid', 'st_size', 'st_mtime_ns', 'st_ctime_ns'):
            self.assertEqual(getattr(lock, field), getattr(self.original_lock, field))
        self.assertEqual(self.runtime_lock.read_bytes(), b'')

    def assert_refused(self, message):
        before, server, restarts = self.files(), copy.deepcopy(self.info), self.restart_count
        marker = self.marker.read_bytes() if self.marker.is_file() else None
        with self.assertRaisesRegex((host.Failure, ValueError), message):
            self.cancel()
        self.assertEqual(self.files(), before)
        self.assertEqual(self.info, server)
        self.assertEqual(self.restart_count, restarts)
        if marker is not None:
            self.assertEqual(self.marker.read_bytes(), marker)
        self.assertEqual(self.cleanup_calls, 0)
        self.assert_data_preserved()

    def assert_cleanup_complete(self):
        self.assertFalse(os.path.lexists(self.marker))
        self.assert_data_preserved()
        archived = next((self.state / 'history').iterdir())
        record = host.read_json(archived / host.BLOCKED_INITIAL_OWNER)
        self.assertEqual(bytes.fromhex(record['owner']['data_hex']), self.original_marker)
        self.assertEqual(record['owner']['sha256'], host.digest(self.original_marker))
        self.assertEqual(record['provenance']['floor'], self.current_floor)
        self.assertEqual((archived / 'host.json').read_bytes(), self.original_journal)

    def test_exact_blocked_initial_owner_is_archived_and_removed_without_restart(self):
        server, restarts = copy.deepcopy(self.info), self.restart_count
        self.cancel()
        self.assert_retired(server, restarts)
        self.assert_cleanup_complete()
        self.assertEqual(self.cleanup_calls, 1)
        self.cancel()
        self.assertEqual(self.cleanup_calls, 1)

    def test_blocked_initial_owner_cleanup_and_owned_bridge_restoration(self):
        journal = self.bridges(alias=True)
        restored = {item['path']: Path(item['editable']).read_bytes() for item in journal['source_bridge']['files'] if item['editable']}
        self.original_journal = (self.state / 'host.json').read_bytes()
        server, restarts = copy.deepcopy(self.info), self.restart_count
        self.cancel()
        self.assert_retired(server, restarts)
        self.assert_cleanup_complete()
        for name, data in restored.items():
            self.assertEqual(Path(name).read_bytes(), data)

    def test_owner_schema_and_exact_initial_identity_are_mandatory(self):
        cases = [('api', 1), ('api', 2.0), ('allowed', True), ('allowed', 0), ('generation', '22222222-2222-4222-8222-222222222222'), ('instance_id', '22222222-2222-4222-8222-222222222222'), ('version', 'v1.0.2'), ('commit', '9' * 40), ('extra', 'unknown')]
        for key, value in cases:
            with self.subTest(key=key, value=value):
                host.atomic(self.marker, dict(self.marker_value, **{key: value}), mode=0o644)
                self.assert_refused('not the exact blocked initial enrollment')

    def test_malformed_duplicate_and_oversized_owner_refused(self):
        for raw in (b'{', b'{}', b'null', b'[]', b'{"api":2,"api":2}', b' ' * 4097):
            with self.subTest(raw=raw[:40]):
                host.atomic(self.marker, raw, mode=0o644)
                self.assert_refused('Expecting|not the exact|Duplicate|Unsafe')

    def test_owner_symlink_hardlink_fifo_and_writable_files_refused(self):
        self.marker.unlink()
        self.marker.symlink_to(self.application)
        self.assert_refused('Runtime ownership remains')
        self.marker.unlink()
        self.marker.symlink_to(self.data / 'missing')
        self.assert_refused('Runtime ownership remains')
        self.marker.unlink()
        os.mkfifo(self.marker)
        self.assert_refused('Runtime ownership remains')
        self.marker.unlink()
        host.atomic(self.marker, self.original_marker, mode=0o644)
        os.link(self.marker, self.data / 'linked-owner')
        self.assert_refused('Unsafe runtime ownership metadata')
        (self.data / 'linked-owner').unlink()
        self.marker.chmod(0o666)
        self.assert_refused('Unsafe runtime ownership metadata')

    def test_unknown_saved_generation_and_old_release_drift_refused(self):
        path = self.state / 'enrolled-compose.json'
        saved = host.read_json(path)
        changed = copy.deepcopy(saved)
        del changed['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION']
        host.atomic(path, changed)
        host.atomic(Path(host.read_json(self.state / 'config.json')['deployment_file']), changed)
        self.assert_refused('Unknown initial enrollment generation')
        host.atomic(path, saved)
        host.atomic(Path(host.read_json(self.state / 'config.json')['deployment_file']), saved)
        self.old_image['Config']['Labels']['org.opencontainers.image.revision'] = '8' * 40
        self.assert_refused('Initial enrolled image release identity differs')

    def test_missing_wrong_pid_wrong_inode_or_shared_lease_refused(self):
        good = self.lock_table.read_text()
        wrong_inode = good.replace(':' + str(self.runtime_lock.stat().st_ino) + ' ', ':999999999 ')
        for locks in ('', good.replace('WRITE 1 ', 'WRITE 9 '), wrong_inode, good.replace('WRITE', 'READ'), good.replace('FLOCK', 'POSIX'), good + good):
            with self.subTest(locks=locks):
                self.lock_table.write_text(locks)
                self.assert_refused('does not hold the stable lifetime lease')

    def test_noncanonical_server_process_or_namespace_refused(self):
        self.info['Config']['Cmd'] = ['/bin/sh', '-c', '/app/p2pstream server']
        self.assert_refused('Cannot prove the standalone server lifetime lease')
        self.info['Config']['Cmd'] = ['/app/p2pstream', 'server']
        for options in ({'Init': True}, {'PidMode': 'host'}):
            self.info['HostConfig'] = options
            self.assert_refused('Cannot prove the standalone server lifetime lease')

    def test_unsafe_stable_lock_refused_without_replacement(self):
        self.runtime_lock.chmod(0o666)
        self.original_lock = self.runtime_lock.stat()
        self.assert_refused('Unsafe runtime ownership metadata')

    def test_host_volume_path_must_match_container_directory_inode(self):
        other = self.path / 'unrelated-volume'
        other.mkdir()
        self.volume['Mountpoint'] = str(other)
        self.assert_refused('mountpoint differs from the local volume')
        self.info['Mounts'][0]['Source'] = str(other)
        self.assert_refused('directory differs from the running container')
        other.rmdir()
        other.symlink_to(self.data, target_is_directory=True)
        self.assert_refused('not locally accessible')
        other.unlink()
        self.assert_refused('not locally accessible')

    def test_volume_driver_and_host_lock_identity_are_not_assumed(self):
        self.volume['Driver'] = 'remote'
        self.assert_refused('Cannot prove the local runtime data volume')
        self.volume['Driver'] = 'local'
        real = host.Host.cancellation_live_lease
        def changed(controller, info):
            lease = real(controller, info)
            fields = lease['metadata'].split(':')
            fields[1] = str(int(fields[1]) + 1)
            lease['metadata'] = ':'.join(fields)
            return lease
        with patch.object(host.Host, 'cancellation_live_lease', changed):
            self.assert_refused('Local stable lifetime lock differs')

    def test_directory_descriptor_never_unlinks_a_replacement_path(self):
        moved = self.path / 'original-data'
        def replace_directory():
            self.data.rename(moved)
            self.data.mkdir()
            host.atomic(self.marker, dict(self.marker_value, allowed=True), mode=0o644)
        self.cleanup_before = replace_directory
        with self.assertRaisesRegex(host.Failure, 'Runtime ownership changed'):
            self.cancel()
        self.assertFalse((moved / self.marker.name).exists())
        self.assertTrue(host.read_json(self.marker)['allowed'])
        self.assertEqual((moved / self.runtime_lock.name).stat().st_ino, self.original_lock.st_ino)

    @unittest.skipUnless(hasattr(os, 'fork'), 'Requires Linux process and flock semantics')
    def test_controller_death_cannot_leave_a_mutator_that_removes_reenrolled_owner(self):
        ready_read, ready_write = os.pipe()
        resume_read, resume_write = os.pipe()
        pid = os.fork()
        if pid == 0:
            try:
                os.close(ready_read)
                os.close(resume_write)
                unlink = os.unlink
                def paused_unlink(path, *args, **kwargs):
                    if path == '.p2pstream-runtime-owner.json' and 'dir_fd' in kwargs:
                        os.write(ready_write, b'R')
                        os.read(resume_read, 1)
                    return unlink(path, *args, **kwargs)
                with patch.object(os, 'unlink', paused_unlink):
                    self.cancel()
                os._exit(0)
            except BaseException:
                os._exit(2)
        os.close(ready_write)
        os.close(resume_read)
        alive = True
        try:
            self.assertTrue(select.select([ready_read], [], [], 5)[0], 'Cleanup did not reach its final unlink')
            self.assertEqual(os.read(ready_read, 1), b'R')
            for name in ('host.lock', 'executor.lock'):
                with (self.state / name).open('rb') as stream:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.assertEqual(self.marker.read_bytes(), self.original_marker)
            os.kill(pid, signal.SIGKILL)
            self.assertEqual(os.waitpid(pid, 0)[1], signal.SIGKILL)
            alive = False
            self.cancel()
            self.assert_cleanup_complete()
            replacement = dict(self.marker_value, generation='22222222-2222-4222-8222-222222222222', allowed=True)
            host.atomic(self.marker, replacement, mode=0o644)
            # The only process that could perform the old unlink is dead. No
            # mutating Docker exec/helper can survive to remove this generation.
            with self.assertRaises(BrokenPipeError):
                os.write(resume_write, b'R')
            self.assertEqual(host.read_json(self.marker), replacement)
            for kind, *details in self.commands:
                if kind == 'docker' and details[0][:1] == ('exec',) and '/bin/sh' in details[0]:
                    self.assertIn(details[0][-1], {host.OWNER_ABSENT, host.OWNER_PROBE, host.LEASE_PROBE})
        finally:
            if alive:
                os.kill(pid, signal.SIGKILL)
                os.waitpid(pid, 0)
            os.close(ready_read)
            os.close(resume_write)

    def interrupted_cleanup(self, point):
        self.cleanup_fault = point
        with self.assertRaises((KeyboardInterrupt, host.Failure)):
            self.cancel()
        self.assertEqual((self.state / 'host.json').read_bytes(), self.original_journal)
        self.assertFalse(host.read_json(self.state / host.CANCELLATION)['completed'])
        self.assertTrue((self.state / host.BLOCKED_INITIAL_OWNER).exists())
        self.assert_data_preserved()
        self.cleanup_fault = None

    def test_interruption_before_cleanup_reproves_exact_archived_owner(self):
        server, restarts = copy.deepcopy(self.info), self.restart_count
        self.interrupted_cleanup('before')
        self.assertEqual(self.marker.read_bytes(), self.original_marker)
        self.cancel()
        self.assert_retired(server, restarts)
        self.assert_cleanup_complete()

    def test_interruption_after_unlink_reproves_absence_and_resyncs(self):
        server, restarts = copy.deepcopy(self.info), self.restart_count
        self.interrupted_cleanup('after-unlink')
        self.assertFalse(self.marker.exists())
        self.cancel()
        self.assert_retired(server, restarts)
        self.assert_cleanup_complete()
        self.assertEqual(self.cleanup_calls, 2)

    def test_interruption_after_sync_reproves_absence_and_receipt(self):
        self.interrupted_cleanup('after-sync')
        self.cancel()
        self.assert_cleanup_complete()

    def test_retry_cannot_refloor_or_discard_cleanup_provenance(self):
        self.interrupted_cleanup('after-unlink')
        self.manifest['sequence'] += 1
        with self.assertRaisesRegex(host.Failure, 'archive or retained floor differs'):
            self.cancel()
        self.manifest['sequence'] -= 1
        record = host.read_json(self.state / host.BLOCKED_INITIAL_OWNER)
        record['owner']['sha256'] = '0' * 64
        host.atomic(self.state / host.BLOCKED_INITIAL_OWNER, record)
        with self.assertRaisesRegex(host.Failure, 'archive hash differs'):
            self.cancel()
        self.assertEqual(self.cleanup_calls, 1)
        self.assert_data_preserved()

    def test_retry_cannot_accept_recreated_owner_or_changed_lifetime_lock(self):
        self.interrupted_cleanup('before')
        host.atomic(self.marker, self.original_marker, mode=0o644)
        with self.assertRaisesRegex(host.Failure, 'owner changed after cancellation was recorded'):
            self.cancel()
        self.marker.unlink()
        self.runtime_lock.write_bytes(b'changed')
        with self.assertRaisesRegex(host.Failure, 'archive or retained floor differs'):
            self.cancel()
        self.assertEqual(self.cleanup_calls, 1)

    def test_last_local_compare_refuses_changed_owner_and_preserves_archive(self):
        self.cleanup_before = lambda: host.atomic(self.marker, dict(self.marker_value, allowed=True), mode=0o644)
        with self.assertRaisesRegex(host.Failure, 'Local runtime owner differs'):
            self.cancel()
        self.assertTrue(host.read_json(self.marker)['allowed'])
        self.assertTrue((self.state / host.BLOCKED_INITIAL_OWNER).exists())
        self.assert_data_preserved()

    def test_all_existing_deployment_gates_precede_owner_cleanup(self):
        self.foreign_writer = True
        self.assert_refused('Another running container writes')
        self.foreign_writer = False
        self.executor['State']['Running'] = True
        self.assert_refused('saved executor is still active')
        self.executor['State']['Running'] = False
        self.image['Id'] = 'sha256:foreign'
        self.assert_refused('not the upgraded canonical release image')


class VerificationDiagnostics(unittest.TestCase):
    def timeout(self, controller):
        with patch.object(host.time, 'monotonic', side_effect=[0, 0, 121]), patch.object(host.time, 'sleep'):
            with self.assertRaises(host.Failure) as caught:
                controller.wait_healthy()
        self.assertNotIn('secret-sentinel', str(caught.exception))
        return str(caught.exception)

    def test_socket_failure_identifies_stage_without_stale_helper_output(self):
        controller = host.Host()
        controller._helper_failure_diagnostic = Path('/private/stale.log')
        with patch.object(controller, 'status_socket', side_effect=OSError(2, 'secret-sentinel')), patch.object(controller, 'command') as command:
            message = self.timeout(controller)
        self.assertIn('private updater authentication: local operating-system error (errno 2)', message)
        self.assertNotIn('private diagnostic', message)
        command.assert_not_called()

    def test_failed_check_identifies_fresh_private_diagnostic(self):
        controller = host.Host()
        def rejected(*args):
            controller._helper_failure_diagnostic = controller.directory / 'helper-failure.log'
            raise host.Failure('secret-sentinel')
        with patch.object(controller, 'status_socket'), patch.object(controller, 'command', side_effect=rejected):
            message = self.timeout(controller)
        self.assertIn('server readiness and deployment verification: verification refused', message)
        self.assertIn('private diagnostic: ' + str(controller.directory / 'helper-failure.log'), message)

    def test_invalid_response_identifies_decode_stage_without_echoing_content(self):
        controller = host.Host()
        with patch.object(controller, 'status_socket'), patch.object(controller, 'command', return_value=subprocess.CompletedProcess([], 0, b'secret-sentinel', b'')):
            message = self.timeout(controller)
        self.assertIn('verification response decoding: invalid verification response', message)
        self.assertNotIn('private diagnostic', message)

    def test_authentication_refusal_does_not_echo_response_or_run_check(self):
        controller = host.Host()
        with patch.object(controller, 'status_socket', side_effect=host.Failure('secret-sentinel')), patch.object(controller, 'command') as command:
            message = self.timeout(controller)
        self.assertIn('private updater authentication: verification refused', message)
        command.assert_not_called()

    def test_unsettled_helper_keeps_its_recovery_boundary_without_retry(self):
        controller = host.Host()
        error = host.UnsettledHelper('helper still running')
        with patch.object(controller, 'healthy', side_effect=error) as healthy, patch.object(host.time, 'sleep') as sleep:
            with self.assertRaises(host.UnsettledHelper) as caught:
                controller.wait_healthy()
        self.assertIs(caught.exception, error)
        self.assertEqual(healthy.call_count, 1)
        sleep.assert_not_called()

    def test_success_still_requires_three_consecutive_checks(self):
        controller = host.Host()
        status = {'ready': True}
        attempts = [status, status, host.Failure('temporary'), status, status, status]
        with patch.object(controller, 'healthy', side_effect=attempts) as healthy, patch.object(host.time, 'monotonic', return_value=0), patch.object(host.time, 'sleep'):
            self.assertEqual(controller.wait_healthy(), status)
        self.assertEqual(healthy.call_count, 6)


if __name__ == '__main__':
    unittest.main()

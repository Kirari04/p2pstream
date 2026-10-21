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
        self.manifest = {'oci_images': [{'platforms': [{'os': 'linux', 'arch': 'amd64'}]}], '_files': {'p2pstream_agent_update_manifest.json': b'fixture', 'p2pstream_install.json': b'fixture', 'p2pstream_server_update.json': b'fixture'}}
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
                    cfg = {'instance_id': ID, 'repository': 'test/repo', 'project': 'custom-project', 'token': 'private-token' * 4, 'control_dir': str(outer.state / 'control'), 'data_volume': 'custom-data'}
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
                base.run_helper(controller, '/app/p2pstream', ['server-updater', 'enroll'])
        diagnostic = self.state / 'helper-failure.log'
        self.assertEqual(diagnostic.stat().st_mode & 0o777, 0o600)
        self.assertIn(b'private validator detail', diagnostic.read_bytes())
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

if __name__ == '__main__':
    unittest.main()

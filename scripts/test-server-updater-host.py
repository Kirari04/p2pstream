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
        self.info = {'Image': 'sha256:config', 'State': {'Running': True}, 'Config': {'Labels': {'com.docker.compose.project': 'custom-project', 'com.docker.compose.service': 'p2pstream', 'com.docker.compose.config-hash': 'a' * 64}}, 'Mounts': [{'Destination': '/data', 'Name': 'custom-data', 'RW': True}]}
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
            def command(self, *args):
                outer.commands.append(('binary', args))
                if args[0] == 'enroll':
                    cfg = {'instance_id': ID, 'repository': 'test/repo', 'project': 'custom-project', 'token': 'private-token' * 4, 'control_dir': str(outer.state / 'control'), 'data_volume': 'custom-data'}
                    model = copy.deepcopy(outer.model)
                    model['services']['p2pstream']['image'] = REF
                    model['services']['p2pstream']['environment']['SERVER_UPDATE_TOKEN'] = cfg['token']
                    model['services']['p2pstream-server-updater'] = {'image': HELPER}
                    (outer.state / 'control').mkdir(exist_ok=True)
                    host.atomic(outer.state / 'config.json', cfg)
                    host.atomic(outer.state / 'compose.json', model)
                return subprocess.CompletedProcess(args, 0, b'{}', b'')
            def compose(self, *args, model='compose.json'):
                outer.commands.append(('compose', args, model))
                if args[0] == 'ps':
                    return subprocess.CompletedProcess(args, 0, b'container\n', b'')
                if args[0] == 'up' and args[-1] == 'p2pstream':
                    raw = (outer.state / model).read_bytes()
                    if not outer.models_started or outer.models_started[-1] != raw:
                        outer.restart_count += 1
                    outer.models_started.append(raw)
                return subprocess.CompletedProcess(args, 0, b'{}', b'')
            def status_socket(self):
                if outer.failure == 'socket':
                    raise host.Failure('socket unavailable')
            def wait_socket(self):
                self.status_socket()
            def healthy(self):
                self.status_socket()
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
        self.patches = [patch.object(host, 'STATE', self.state), patch.object(host, 'Host', FixtureHost), patch.object(host, 'docker', self.docker), patch.object(host, 'local_engine', return_value='amd64'), patch.object(host, 'release_metadata', return_value=(self.manifest, self.descriptor, REF, 'f' * 64)), patch.object(os, 'geteuid', return_value=0)]
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
                out = b'container\n'
            elif '--hash' in args:
                out = ('p2pstream ' + 'a' * 64 + '\n').encode()
            elif 'config' in args:
                self.config_reads += 1
                out = self.raw + (b' ' if self.drift and self.config_reads > 1 else b'')
        elif args[0] == 'inspect':
            out = json.dumps([self.info]).encode()
        elif args[0:2] == ('image', 'inspect'):
            out = json.dumps([self.helper if args[-1] == HELPER else self.image]).encode()
        elif args[0] == 'exec':
            out = (self.identity + '\n').encode() if args[-1] == 'server-installation-identity' else b'{}'
        elif args[0] == 'ps':
            out = b'' if any('host-helper=' in str(value) for value in args) else b'container\n'
        return subprocess.CompletedProcess(args, 0, out, b'')
    def install(self):
        host.install(self.args, self.options)
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

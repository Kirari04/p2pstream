#!/usr/bin/env python3
"""Focused canonical worker checks with deterministic host fixtures; no Docker."""
import argparse
import ast
import base64
import contextlib
import copy
import errno
import fcntl
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent


def setUpModule():
    # Install before any fixture setup. Tests may substitute an in-memory run
    # response; an escaped transport still cannot reach a real child process.
    stack = contextlib.ExitStack()
    unittest.addModuleCleanup(stack.close)
    for name in ('Popen', 'run', 'call', 'check_call', 'check_output', 'getoutput', 'getstatusoutput'):
        stack.enter_context(patch.object(subprocess, name, side_effect=AssertionError('Real subprocess transport forbidden')))


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


host_tests = load('lifecycle_host_fixtures', ROOT / 'test-server-updater-host.py')
harness = load('lifecycle_harness', ROOT / 'test-server-lifecycle-docker.py')


class RemovalRecoveryBoundaries(unittest.TestCase):
    """Execute the installer boundary function with the public controller fixture."""

    def exercise(self, *, fail_health=False, duplicate_lock=False):
        host = host_tests.host
        production_wait = host.Host.wait_original
        fixture = host_tests.Lifecycle()
        fixture.setUp()
        self.addCleanup(fixture.tearDown)
        fixture.managed = True
        fixture.install()
        controller = host.Host()
        with controller.lock():
            model = host.read_json(controller.current_model_path())
            server = model['services']['p2pstream']
            server['image'] = 'ghcr.io/test/repo@sha256:' + 'e' * 64
            server['environment']['SECRET'] = 'current-synthetic-setting'
            server['ports'][0]['published'] = '10443'
            server['volumes'].append({'type': 'bind', 'source': controller.config['control_dir'],
                                      'target': '/run/p2pstream-server-update', 'read_only': True})
            controller.write_model(model)
        config = (fixture.state / 'config.json').read_bytes()
        executor_removed = []
        original_compose = host.Host.compose
        original_begin, original_stop = host.Host.begin_detached_continuation, host.Host.stop_executor
        owned_fds, duplicates = {}, {}
        self.lock_trace = []
        stop_calls = 0

        def identity(fd):
            value = os.fstat(fd)
            return value.st_dev, value.st_ino, value.st_mode

        def tracked_stop(controller):
            nonlocal stop_calls
            fd = original_stop(controller)
            owned_fds[fd] = identity(fd)
            stop_calls += 1
            self.lock_trace.append({'event': 'stop-return', 'fd': fd, 'identity': owned_fds[fd]})
            if duplicate_lock and stop_calls == 2:
                duplicate = os.dup(fd)
                duplicates[duplicate] = identity(duplicate)
            return fd

        def assert_no_owned_fds():
            for fd in owned_fds:
                with self.assertRaises(OSError) as closed:
                    os.fstat(fd)
                self.assertEqual(closed.exception.errno, errno.EBADF)
            for name in os.listdir('/proc/self/fd'):
                try:
                    value = os.fstat(int(name))
                except OSError as error:
                    if error.errno == errno.EBADF:
                        continue
                    raise
                self.assertNotIn((value.st_dev, value.st_ino), [saved[:2] for saved in owned_fds.values()])
            self.lock_trace.append({'event': 'no-owned-lock-descriptors'})

        def probe_lock(*, blocked=False):
            fd = os.open(fixture.state / 'executor.lock', os.O_RDWR | os.O_NOFOLLOW)
            try:
                if blocked:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                else:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                self.lock_trace.append({'event': 'independent-flock', 'blocked': blocked})
            finally:
                os.close(fd)

        def cleanup_owned_fds():
            # The negative test owns the deliberate dup. Cleanup also keeps a
            # failed before-source replay from leaking its captured test fd.
            for fd, saved in (owned_fds | duplicates).items():
                try:
                    actual = identity(fd)
                except OSError as error:
                    if error.errno == errno.EBADF:
                        continue
                    raise
                self.assertEqual(actual, saved, 'Test-owned descriptor was reused; refusing cleanup')
                os.close(fd)

        def inspect_model():
            # The controller fixture models daemon state. Extend its inspection
            # from the active model for the real harness's preservation checks.
            config = host.read_json(fixture.state / 'config.json')
            model = host.read_json(Path(config['deployment_file']))
            server = model['services']['p2pstream']
            fixture.info['Config'].update(Image=server['image'], Env=[key + '=' + str(value) for key, value in server['environment'].items()])
            fixture.info['HostConfig'] = {'PortBindings': {str(port['target']) + '/tcp': [
                {'HostIp': port.get('host_ip', ''), 'HostPort': str(port['published'])}] for port in server.get('ports', [])}}
            fixture.info['Mounts'] = [{'Type': volume['type'], 'Destination': volume['target'],
                'Name': model['volumes'][volume['source']]['name'] if volume['type'] == 'volume' else '',
                'Source': '/synthetic-data' if volume['type'] == 'volume' else volume['source'],
                'RW': not volume.get('read_only', False)} for volume in server['volumes']]

        def composed(controller, *args, **kwargs):
            result = original_compose(controller, *args, **kwargs)
            if args[0] == 'up' and args[-1] == 'p2pstream':
                inspect_model()
            if args[0] == 'rm' and 'p2pstream-server-updater' in args:
                executor_removed.append(True)
            if args[0] == 'ps' and args[-1] == 'p2pstream-server-updater':
                return subprocess.CompletedProcess(args, 0, b'' if executor_removed else b'synthetic-executor\n', b'')
            return result

        def docker(*args):
            return fixture.docker(*args).stdout.decode().strip()

        def compose(*args):
            self.assertEqual(args, ('ps', '-q', 'observer'))
            return 'synthetic-observer'

        inspect_model()
        tree = ast.parse((ROOT / 'test-server-install-docker.py').read_text())
        function = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == 'removal_recovery_boundaries')
        namespace = dict(host=host, info=lambda: copy.deepcopy(fixture.info), docker=docker, compose=compose,
                         images={'candidate': server['image']}, installation=host_tests.ID, observer='synthetic-observer',
                         config=config, argparse=argparse, copy=copy, errno=errno, os=os, checks=[])
        exec(compile(ast.Module(body=[function], type_ignores=[]), str(ROOT / 'test-server-install-docker.py'), 'exec'), namespace)
        def refused_health(controller):
            raise host.Failure('synthetic health refusal')
        wait = refused_health if fail_health else production_wait
        try:
            with patch.object(host.Host, 'compose', composed), patch.object(host.Host, 'wait_original', wait), patch.object(host.Host, 'stop_executor', tracked_stop):
                if duplicate_lock:
                    with self.assertRaisesRegex(AssertionError, 'Another descriptor still references interrupted executor lock'):
                        namespace['removal_recovery_boundaries']()
                    self.assertEqual(len(duplicates), 1)
                    for fd, saved in duplicates.items():
                        self.assertEqual(identity(fd), saved)  # Harness must not close this fd.
                    probe_lock(blocked=True)
                elif fail_health:
                    with self.assertRaisesRegex(host.Failure, 'synthetic health refusal'):
                        namespace['removal_recovery_boundaries']()
                    self.assertFalse(executor_removed)
                    self.assertIn('detached_continuation', host.read_json(fixture.state / 'host.json'))
                    assert_no_owned_fds()
                    probe_lock()
                else:
                    namespace['removal_recovery_boundaries']()
                    self.assertEqual(len(executor_removed), 1)
                    self.assertEqual(host.read_json(fixture.state / 'host.json')['phase'], 'removed')
                    self.assertIn('removal-rollback-restores-current-image', namespace['checks'])
                    assert_no_owned_fds()
                    probe_lock()
                    current, commands = copy.deepcopy(fixture.info), len(fixture.commands)
                    controller = host.Host()
                    with controller.lock():
                        deployment = controller.current_model_path()
                        removed_model = deployment.read_bytes()
                        data = controller.replacement_data()
                        source = Path(controller.journal['source_layout']['primary'])
                        exported = source.read_bytes()
                        controller.archive_removed()  # Production health and, on #343, real flock.
                    self.assertFalse((fixture.state / 'host.json').exists())
                    saved_configs = list((fixture.state / 'history').glob('*/config.json'))
                    self.assertEqual(len(saved_configs), 1)
                    self.assertEqual(saved_configs[0].read_bytes(), config)
                    saved = host.read_json(saved_configs[0])
                    self.assertEqual(saved['instance_id'], fixture.identity)
                    self.assertEqual(saved['data_volume'], next(m['Name'] for m in current['Mounts'] if m['Destination'] == '/data'))
                    self.assertEqual(deployment.read_bytes(), removed_model)
                    self.assertEqual(source.read_bytes(), exported)
                    self.assertEqual(fixture.info, current)
                    self.assertIsNone(data['owner'])
                    self.assertFalse(any(item[0] == 'compose' and item[1][0] in {'up', 'start', 'stop'} for item in fixture.commands[commands:]))
                    assert_no_owned_fds()
                    self.lock_trace.append({'event': 'production-archive-completed', 'config-image-data-preserved': True})
                self.assertIs(host.Host.compose, composed)
                self.assertIs(host.Host.wait_original, wait)
                self.assertIs(host.Host.begin_detached_continuation, original_begin)
                self.assertIs(host.Host.stop_executor, tracked_stop)
        finally:
            cleanup_owned_fds()
        if duplicate_lock:
            assert_no_owned_fds()
            probe_lock()

    def test_literal_installer_removal_boundaries(self):
        self.exercise()

    def test_failed_health_retains_executor_and_continuation(self):
        self.exercise(fail_health=True)

    def test_extra_lock_descriptor_is_refused_without_closing_it(self):
        self.exercise(duplicate_lock=True)


class CanonicalRemovalBoundaries(unittest.TestCase):
    """Real driver orchestration/public controller; mocked Docker, real locks/files."""

    def setUp(self):
        self.host = host = host_tests.host
        self.production_wait = host.Host.wait_original
        self.fixture = f = host_tests.SingleFileLifecycle()
        f.setUp()
        self.addCleanup(f.tearDown)
        self.stack = stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        f.install()
        model = host.read_json(f.canonical)
        server = model['services']['p2pstream']
        self.candidate = 'ghcr.io/test/repo@sha256:' + 'e' * 64
        server.update(image=self.candidate, read_only=True, cap_drop=['ALL'],
                      cap_add=['NET_BIND_SERVICE'], security_opt=['no-new-privileges:true'],
                      tmpfs=['/tmp:rw,nosuid,nodev,mode=1777'])
        server['environment'].update(QUALIFICATION_SETTING='applied-after-update',
                                     REENROLLMENT_SETTING='native-after-remove')
        server['ports'][0]['published'] = '18102'
        server['volumes'] += [{'type': 'bind', 'source': str(f.state / 'control'),
                              'target': '/run/p2pstream-server-update', 'read_only': True},
                             {'type': 'bind', 'source': str(f.source),
                              'target': '/qualification', 'read_only': True}]
        model['services']['observer'] = {'image': 'synthetic-observer'}
        host.atomic(f.canonical, model, mode=0o640)
        f.info['Image'] = f.image['Id'] = 'sha256:current-candidate'
        f.info['RestartCount'] = 0
        f.info['State']['StartedAt'] = 'synthetic-start'
        self.observer = inspection()
        self.observer.update(Id='synthetic-observer')
        self.observer['HostConfig'] = {'ReadonlyRootfs': True}
        (f.state / 'history').mkdir()
        self.floor = host.read_json(f.state / 'config.json')['bootstrap_floor']
        self.pending, self.duplicates = [], []
        self.fail_health = False
        self.worker_calls, self.executor_removals, self.gates = [], [], []
        self.addCleanup(self.cleanup_fds)
        self.volume = [{'Name': 'custom-data', 'Mountpoint': '/synthetic-data'}]
        self.ca = 'synthetic-ca'
        harness.write(f.path / 'identity.json', {'installation': host_tests.ID,
                                               'ca_sha256': harness.sha(self.ca.encode())})
        compose, stop, target = host.Host.compose, host.Host.stop_executor, host.Host.detached_target
        docker, read = host.docker, harness.read
        release = {'manifest': f.manifest, 'descriptor': f.descriptor,
                   'release': {'image': self.candidate}}

        def projected():
            m = host.read_json(f.canonical); s = m['services']['p2pstream']
            f.info['Config'].update(Image=s['image'], Env=[k + '=' + str(v) for k, v in s['environment'].items()])
            f.info['HostConfig'] = {'ReadonlyRootfs': s.get('read_only', False),
                'CapDrop': s.get('cap_drop'), 'CapAdd': s.get('cap_add'),
                'SecurityOpt': s.get('security_opt'),
                'Tmpfs': dict(value.split(':', 1) for value in s.get('tmpfs', [])),
                'PortBindings': {str(p['target']) + '/tcp': [{'HostPort': str(p['published'])}] for p in s['ports']}}
            f.info['Mounts'] = [{'Type': v['type'], 'Destination': v['target'],
                'Name': m['volumes'][v['source']]['name'] if v['type'] == 'volume' else '',
                'Source': '/synthetic-data' if v['type'] == 'volume' else v['source'],
                'RW': not v.get('read_only', False)} for v in s['volumes']]

        def composed(controller, *args, **kwargs):
            result = compose(controller, *args, **kwargs)
            if args[0] == 'up' and args[-1] == 'p2pstream':
                projected()
                f.info['State']['StartedAt'] = '0001-01-01T00:00:00Z'
            if args[0] == 'start' and args[-1] == 'p2pstream':
                f.info['State']['StartedAt'] = 'synthetic-start-' + f.info['Id']
            if args[0] == 'rm' and args[-1] == 'p2pstream-server-updater':
                self.assertTrue(self.gates and self.gates[-1] in {'health', 'target'})
                if self.gates[-1] == 'target': self.assertEqual(self.gates[-2:], ['health', 'target'])
                self.executor_removals.append(True)
            return result

        def checked_target(controller):
            result = target(controller); self.gates.append('target'); return result

        def waited(controller):
            if self.fail_health:
                with patch.object(host.time, 'monotonic', side_effect=[0, 0, 91]), patch.object(host.time, 'sleep'):
                    self.production_wait(controller)
            else:
                self.production_wait(controller)
            self.gates.append('health')

        def stopped(controller):
            fd = stop(controller)
            self.pending.append((fd, self.fd_identity(fd)))
            return fd

        def observed_docker(*args, **kwargs):
            if args[0] == 'exec' and args[-1] == 'server-health' and self.fail_health:
                return subprocess.CompletedProcess(args, 1, b'', b'')
            return docker(*args, **kwargs)

        def harness_docker(*args, **kwargs):
            if args[:3] == ('ps', '--all', '--quiet') and args[-1] == 'label=com.docker.compose.service=p2pstream-server-updater':
                return '' if self.executor_removals else 'synthetic-executor'
            if args[:2] == ('volume', 'inspect'):
                return json.dumps(self.volume)
            if args[0] == 'inspect' and args[1:] == ('synthetic-observer',):
                return json.dumps([self.observer])
            if args[0] == 'exec' and args[-1] == '/data/certs/management/ca.crt.pem':
                return self.ca
            return observed_docker(*args, **kwargs).stdout.decode().strip()

        def harness_compose(*args, **kwargs):
            if args[-1] == 'observer':
                return 'synthetic-observer'
            self.assertEqual(args, ('ps', '--all', '--quiet', 'p2pstream-server-updater'))
            return '' if self.executor_removals else 'synthetic-executor'

        def metadata(path):
            if str(path) == '/review/images.json': return {'candidate': self.candidate}
            if path == f.path / 'catalog.json': return {'candidate': release}
            return read(path)

        def launcher(args, **kwargs):
            self.assertEqual(args, [harness.LAUNCHER, 'status'])
            return subprocess.CompletedProcess(args, 0, b'', b'')

        projected()
        stack.enter_context(patch.multiple(host.Host, compose=composed, stop_executor=stopped,
                                           wait_original=waited, detached_target=checked_target))
        stack.enter_context(patch.object(host, 'docker', observed_docker))
        stack.enter_context(patch.multiple(harness, ROOT=f.path, STATE=f.state, DEPLOYMENT=f.source,
                                           CANONICAL=f.canonical, INPUTS=f.inputs, VOLUME='custom-data',
                                           PROJECT='custom-project', CHECKS=[], STAGE='setup'))
        for name, replacement in [('read', metadata), ('load_host', lambda: host),
                ('install_arguments', lambda *unused: (f.args, release)),
                ('server_info', lambda **unused: copy.deepcopy(f.info)), ('docker', harness_docker),
                ('compose', harness_compose), ('owner_bytes', lambda *unused: b'ABSENT' if f.runtime_owner is None
                 else b'PRESENT:' + json.dumps(f.runtime_owner).encode()),
                ('fresh', self.fresh), ('health', lambda: True), ('run', launcher)]:
            stack.enter_context(patch.object(harness, name, replacement))

    @staticmethod
    def fd_identity(fd):
        value = os.fstat(fd)
        return value.st_dev, value.st_ino, value.st_mode

    def cleanup_fds(self):
        for fd, saved in self.pending + self.duplicates:
            try: actual = self.fd_identity(fd)
            except OSError as error:
                self.assertEqual(error.errno, errno.EBADF); continue
            self.assertEqual(actual, saved, 'Test-owned fd reused; refusing close')
            os.close(fd)

    def probe_lock(self, *, blocked=False):
        fd = os.open(self.fixture.state / 'executor.lock', os.O_RDWR | os.O_NOFOLLOW)
        try:
            if blocked:
                with self.assertRaises(BlockingIOError): fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            else:
                fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        finally: os.close(fd)

    def release_interrupted(self):
        self.assertEqual(len(self.pending), 1)
        fd, saved = self.pending[0]
        self.assertEqual(self.fd_identity(fd), saved)
        os.close(fd)
        for name in os.listdir('/proc/self/fd'):
            try: info = os.fstat(int(name))
            except OSError as error:
                self.assertEqual(error.errno, errno.EBADF); continue
            self.assertNotEqual((info.st_dev, info.st_ino), saved[:2],
                                'Unknown duplicate still references interrupted executor lock')
        self.probe_lock()
        self.pending.clear()

    def fresh(self, action, **kwargs):
        self.worker_calls.append((action, kwargs))
        options = argparse.Namespace(action=action, release='candidate', variant='', refusal='',
            block_after_prepare=False, interrupt=kwargs.get('interrupt', ''), label=kwargs['label'])
        originals = {name: getattr(self.host.Host, name) for name in
                     ('compose', 'command', 'prepare_single', 'write_model', 'wait_original',
                      'begin_detached_continuation', 'detached_target')}
        with patch.multiple(harness, CHECKS=[], STAGE='worker-test'):
            if options.interrupt:
                with self.assertRaises(harness.DeliberateInterruption): harness.worker(options)
                self.probe_lock(blocked=True)
                self.release_interrupted()  # Model process exit; never part of the real worker.
            else:
                harness.worker(options)
                for fd, _ in self.pending:
                    with self.assertRaises(OSError) as caught: os.fstat(fd)
                    self.assertEqual(caught.exception.errno, errno.EBADF)
                self.pending.clear()
        for name, original in originals.items(): self.assertIs(getattr(self.host.Host, name), original)
        harness.CHECKS.append('worker-' + options.label + '-expected-exit')

    def pre_and_uninterrupted(self):
        tree = ast.parse(Path(harness.__file__).read_bytes())
        body = next(n.body for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == 'main')
        def stage_name(n):
            if isinstance(n, ast.Expr) and isinstance(n.value, ast.Call) and isinstance(n.value.func, ast.Name) and n.value.func.id == 'stage':
                return n.value.args[0].value
        start = next(i for i, n in enumerate(body) if stage_name(n) == 'pre-declaration-interrupt-remove')
        end = next(i for i, n in enumerate(body) if stage_name(n) == 'stale-reenrollment-inputs')
        namespace = dict(vars(harness), images={'candidate': self.candidate},
                         expected_floor=self.floor, checks=harness.CHECKS)
        exec(compile(ast.Module(body=body[start:end], type_ignores=[]), harness.__file__, 'exec'), namespace)

    def test_real_predeclaration_worker_rollback_and_uninterrupted_removal(self):
        self.pre_and_uninterrupted()
        self.assertEqual([action for action, _ in self.worker_calls], ['remove', 'repair', 'remove'])
        self.assertEqual(self.worker_calls[0][1]['interrupt'], 'before-remove-continuation')
        self.assertNotIn('interrupt', self.worker_calls[-1][1])
        self.assertEqual(harness.read(self.fixture.path / 'trace-pre-declaration-interrupt-remove.json'),
                         ['interrupt-before-remove-continuation'])
        self.assertIn('interrupted-removal-rollback-fresh-generation-and-readable-model', harness.CHECKS)
        self.assertEqual(harness.read(self.fixture.state / 'host.json')['phase'], 'removed')
        self.assertEqual(len(list((self.fixture.state / 'history').iterdir())), 0)
        self.probe_lock()

    def assert_uninterrupted_guard(self, change, check):
        fresh = harness.fresh
        def corrupted(action, **kwargs):
            result = fresh(action, **kwargs)
            if kwargs['label'] == 'complete-remove': change()
            return result
        with patch.object(harness, 'fresh', corrupted), self.assertRaisesRegex(harness.CheckFailure, '^' + check + '$'):
            self.pre_and_uninterrupted()

    def test_uninterrupted_image_guard_is_preserved(self):
        self.assert_uninterrupted_guard(lambda: self.fixture.info['Config'].update(Image='foreign-image'),
                                        'remove-downgraded-image')

    def test_uninterrupted_applied_setting_guard_is_preserved(self):
        def change():
            self.fixture.info['Config']['Env'].remove('QUALIFICATION_SETTING=applied-after-update')
        self.assert_uninterrupted_guard(change, 'remove-dropped-applied-settings')

    def test_uninterrupted_binding_guard_is_preserved(self):
        self.assert_uninterrupted_guard(lambda: self.fixture.info['Config']['Env'].append('SERVER_UPDATE_TOKEN=foreign'),
                                        'remove-retained-private-binding')

    def test_uninterrupted_floor_guard_is_preserved(self):
        def change():
            path = self.fixture.state / 'installation-receipt.json'
            receipt = harness.read(path)
            receipt['floor']['sequence'] -= 1
            harness.write(path, receipt)
        self.assert_uninterrupted_guard(change, 'remove-dropped-release-floor')

    def test_real_postdeclaration_public_repair(self):
        harness.removal_forward_recovery({'candidate': self.candidate}, self.floor)
        self.assertEqual([a for a, _ in self.worker_calls], ['remove', 'repair'])
        self.assertEqual(len(self.executor_removals), 1)
        self.probe_lock()
        # Test-only archival proves no in-process interruption fd can block the
        # next real archive flock; the Docker driver's schedule adds no archive.
        config = (self.fixture.state / 'config.json').read_bytes()
        current = self.fixture.canonical.read_bytes(), self.fixture.inputs.read_bytes(), copy.deepcopy(self.fixture.info)
        controller = self.host.Host()
        with controller.lock(): controller.archive_removed()
        archives = list((self.fixture.state / 'history').iterdir())
        self.assertEqual(len(archives), 1)
        self.assertEqual((archives[0] / 'config.json').read_bytes(), config)
        self.assertEqual((self.fixture.canonical.read_bytes(), self.fixture.inputs.read_bytes(), self.fixture.info), current)
        self.assertFalse((self.fixture.state / 'host.json').exists())

    def test_real_postdeclaration_public_rollback(self):
        harness.removal_forward_recovery({'candidate': self.candidate}, self.floor, action='rollback')
        self.assertEqual([a for a, _ in self.worker_calls], ['remove', 'rollback'])
        self.probe_lock()

    def test_failed_production_health_retains_continuation_executor_and_target(self):
        original = harness.fresh
        def fresh(action, **kwargs):
            if action == 'repair': self.fail_health = True
            return original(action, **kwargs)
        with patch.object(harness, 'fresh', fresh), self.assertRaises(harness.CheckFailure):
            harness.removal_forward_recovery({'candidate': self.candidate}, self.floor)
        self.assertFalse(self.executor_removals)
        journal = harness.read(self.fixture.state / 'host.json')
        self.assertIn('detached_continuation', journal)
        target, model = copy.deepcopy(self.fixture.info), self.fixture.canonical.read_bytes()
        self.fail_health = False
        self.fresh('rollback', label='retry-unhealthy-remove')
        self.assertEqual(self.fixture.info, target)
        self.assertEqual(self.fixture.canonical.read_bytes(), model)
        self.assertEqual(harness.read(self.fixture.state / 'host.json')['phase'], 'removed')
        self.probe_lock()

    def test_changed_target_owner_data_and_runtime_refuse_both_commands(self):
        self.fresh('remove', label='drift-remove', interrupt='after-remove-deploy')
        f = self.fixture
        original = copy.deepcopy(f.info)
        for drift in ('missing', 'id', 'image', 'settings', 'mount', 'restart', 'owner', 'directory'):
            for action in ('repair', 'rollback'):
                with self.subTest(drift=drift, action=action):
                    f.info, f.server_inventory = copy.deepcopy(original), None
                    f.runtime_owner, f.data_directory = None, '7:11'
                    if drift == 'missing': f.server_inventory = b''
                    elif drift == 'id': f.info['Id'] = 'foreign-target'
                    elif drift == 'image': f.info['Image'] = 'foreign-image'
                    elif drift == 'settings': f.info['Config']['Labels']['com.docker.compose.config-hash'] = 'f' * 64
                    elif drift == 'mount': f.info['Mounts'] *= 2
                    elif drift == 'restart': f.info['RestartCount'] = 2
                    elif drift == 'owner': f.runtime_owner = {'allowed': False}
                    else: f.data_directory = '7:22'
                    commands = len(f.commands)
                    with self.assertRaises(harness.CheckFailure): self.fresh(action, label='refuse-drift')
                    host_tests.Lifecycle.assert_no_server_lifecycle(self, f.commands[commands:])
                    self.assertFalse(self.executor_removals)
                    self.assertIn('detached_continuation', harness.read(f.state / 'host.json'))
        self.probe_lock()

    def test_unknown_duplicate_lock_is_refused_and_not_closed(self):
        stopped = self.host.Host.stop_executor
        def duplicate(controller):
            fd = stopped(controller); extra = os.dup(fd)
            self.duplicates.append((extra, self.fd_identity(extra)))
            return fd
        with patch.object(self.host.Host, 'stop_executor', duplicate):
            with self.assertRaisesRegex(AssertionError, 'Unknown duplicate still references'):
                self.fresh('remove', label='duplicate-remove', interrupt='after-remove-deploy')
        for fd, saved in self.duplicates: self.assertEqual(self.fd_identity(fd), saved)
        self.probe_lock(blocked=True)
        self.cleanup_fds(); self.pending.clear(); self.duplicates.clear()
        self.probe_lock()

    def test_downstream_schedule_and_history_selection_remain(self):
        main = next(n for n in ast.parse(Path(harness.__file__).read_bytes()).body
                    if isinstance(n, ast.FunctionDef) and n.name == 'main')
        calls = [n.value for n in main.body if isinstance(n, ast.Expr) and isinstance(n.value, ast.Call)]
        names = [n.func.id if isinstance(n.func, ast.Name) else '' for n in calls]
        forward = names.index('removal_forward_recovery')
        self.assertEqual(names.count('removal_forward_recovery'), 1)
        self.assertEqual([name for name in names[forward + 1:] if name], ['stage', 'compose', 'write', 'print'])
        labels = [next((k.value.value for k in n.keywords if k.arg == 'label'), None)
                  for n in calls if isinstance(n.func, ast.Name) and n.func.id == 'fresh']
        self.assertEqual(labels[-3:], ['complete-remove', 'stale-reenrollment-inputs', 'reenroll'])
        floors = next(n for n in main.body if isinstance(n, ast.For) and isinstance(n.target, ast.Name) and n.target.id == 'variant')
        self.assertEqual(ast.literal_eval(floors.iter), ('sequence', 'epoch', 'minimum', 'identity'))
        self.assertEqual(sum(isinstance(n, ast.Call) and isinstance(n.func, ast.Name) and n.func.id == 'fresh'
                             and any(k.arg == 'label' and isinstance(k.value, ast.Constant) and k.value.value == 'lost-history'
                                     for k in n.keywords) for n in ast.walk(main)), 1)


class OfflineTransport(unittest.TestCase):
    def test_every_process_transport_is_blocked_before_fixture_setup(self):
        for name in ('Popen', 'run', 'call', 'check_call', 'check_output', 'getoutput', 'getstatusoutput'):
            with self.subTest(transport=name), self.assertRaisesRegex(AssertionError, '^Real subprocess transport forbidden$'):
                getattr(subprocess, name)(['unused-synthetic-command'])


def inspection():
    # Full objects, including an unknown field, catch accidental projection or
    # deduplication. Duplicate destinations are deliberately retained in tests.
    return {'Id': 'PRIVATE_ID', 'Image': 'PRIVATE_IMAGE',
            'State': {'Running': True, 'StartedAt': 'PRIVATE_TIME'}, 'RestartCount': 0,
            'Config': {'Labels': {'PRIVATE_LABEL': 'PRIVATE_VALUE'}},
            'Mounts': [{'Type': 'volume', 'Name': 'PRIVATE_VOLUME', 'Source': '/PRIVATE_SOURCE',
                        'Destination': '/data', 'Driver': 'local', 'Mode': 'z', 'RW': True,
                        'Propagation': '', 'PRIVATE_EXTRA': {'ordered': [1, 2]}},
                       {'Type': 'tmpfs', 'Destination': '/tmp', 'RW': True}]}


class BoundarySnapshots(unittest.TestCase):
    def setUp(self):
        stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        self.path = Path(stack.enter_context(tempfile.TemporaryDirectory()))
        self.info = inspection()
        self.model, self.owner = 'PRIVATE_MODEL', b'PRIVATE_OWNER'
        stack.enter_context(patch.multiple(harness, STATE=self.path, CANONICAL=self.path / 'compose.yaml',
                                           INPUTS=self.path / 'inputs.yaml'))
        stack.enter_context(patch.object(harness, 'server_info', side_effect=lambda **kw: self.info))
        stack.enter_context(patch.object(harness, 'compose', side_effect=lambda *a: self.model))
        stack.enter_context(patch.object(harness, 'owner_bytes', side_effect=lambda *a: self.owner))
        stack.enter_context(patch.object(harness, 'CHECKS', [], create=True))
        stack.enter_context(patch.object(harness, 'STAGE', 'initial-blocker', create=True))

    def snapshot(self):
        return harness.snapshot_boundary({}, private=True)

    def test_mount_order_and_object_key_order_are_not_mutations(self):
        before = self.snapshot()
        self.info['Mounts'] = [dict(reversed(list(m.items()))) for m in reversed(self.info['Mounts'])]
        harness.assert_unchanged(before, {}, private=True)

    def assert_changed(self, before, fields):
        with self.assertRaises(harness.CheckFailure) as caught:
            harness.assert_unchanged(before, {}, private=True)
        self.assertEqual(caught.exception.changed_fields, fields)
        report = json.dumps(harness.failure_report(caught.exception))
        self.assertNotIn('PRIVATE', report)
        self.assertNotIn(str(self.path), report)

    def test_all_container_identity_and_lifecycle_fields_remain_exact(self):
        for field in ('Id', 'Image', 'Running', 'StartedAt', 'RestartCount', 'Labels'):
            with self.subTest(field=field):
                self.info = inspection()
                before = self.snapshot()
                if field in ('Running', 'StartedAt'):
                    self.info['State'][field] = False if field == 'Running' else 'PRIVATE_CHANGED_TIME'
                elif field == 'Labels':
                    self.info['Config']['Labels']['PRIVATE_NEW_KEY'] = 'PRIVATE_NEW_VALUE'
                else:
                    self.info[field] = 1 if field == 'RestartCount' else 'PRIVATE_CHANGED_VALUE'
                self.assert_changed(before, ['container.' + field])

    def test_every_mount_field_and_duplicate_are_preserved(self):
        for field in inspection()['Mounts'][0]:
            with self.subTest(field=field):
                self.info = inspection()
                before = self.snapshot()
                self.info['Mounts'][0][field] = False if field == 'RW' else 'PRIVATE_CHANGED_VALUE'
                self.assert_changed(before, ['container.Mounts'])
        for mutation in ('add', 'remove', 'duplicate', 'nested-order'):
            with self.subTest(mutation=mutation):
                self.info = inspection()
                before = self.snapshot()
                if mutation == 'add':
                    self.info['Mounts'].append({'Destination': '/PRIVATE_ADDED'})
                elif mutation == 'remove':
                    self.info['Mounts'].pop()
                elif mutation == 'duplicate':
                    self.info['Mounts'].append(copy.deepcopy(self.info['Mounts'][0]))
                else:
                    self.info['Mounts'][0]['PRIVATE_EXTRA']['ordered'].reverse()
                self.assert_changed(before, ['container.Mounts'])

    def test_model_owner_and_all_private_source_files_remain_exact(self):
        for field in ('model', 'owner'):
            with self.subTest(field=field):
                before = self.snapshot()
                setattr(self, field, 'PRIVATE_CHANGED_VALUE')
                self.assert_changed(before, [field])
        for name in self.snapshot()['files']:
            for mutation in ('bytes', 'mode', 'removed'):
                with self.subTest(file=Path(name).name, mutation=mutation):
                    path = Path(name)
                    path.parent.mkdir(exist_ok=True)
                    path.write_bytes(b'PRIVATE_FILE')
                    path.chmod(0o600)
                    before = self.snapshot()
                    if mutation == 'bytes':
                        path.write_bytes(b'PRIVATE_CHANGED_FILE')
                    elif mutation == 'mode':
                        path.chmod(0o640)
                    else:
                        path.unlink()
                    self.assert_changed(before, ['files'])
                    path.unlink(missing_ok=True)

    def test_simultaneous_changes_report_all_fixed_field_names_only(self):
        before = self.snapshot()
        self.info['Id'] = 'PRIVATE_OTHER_ID'
        self.info['Config']['Labels']['PRIVATE_OTHER_KEY'] = 'PRIVATE_OTHER_VALUE'
        self.info['Mounts'][0]['Source'] = '/PRIVATE_OTHER_SOURCE'
        self.model, self.owner = 'PRIVATE_OTHER_MODEL', b'PRIVATE_OTHER_OWNER'
        self.assert_changed(before, ['container.Id', 'container.Labels', 'container.Mounts', 'model', 'owner'])

    def test_absent_file_creation_and_file_ownership_changes_are_detected(self):
        before = self.snapshot()
        path = harness.CANONICAL
        path.write_bytes(b'PRIVATE_FILE')
        self.assert_changed(before, ['files'])
        before = self.snapshot()
        original = harness.file_snapshot
        for index in (2, 3):  # uid and gid, without requiring chown privileges.
            def changed_metadata(candidate):
                value = original(candidate)
                if Path(candidate) == path:
                    value = list(value)
                    value[index] += 1
                    return tuple(value)
                return value
            with self.subTest(metadata=index), patch.object(harness, 'file_snapshot', changed_metadata):
                self.assert_changed(before, ['files'])

    def test_passed_boundaries_remain_visible_on_a_later_failure(self):
        before = self.snapshot()
        harness.assert_unchanged(before, {}, private=True)
        harness.stage('idempotent-1')
        self.info['RestartCount'] += 1
        with self.assertRaises(harness.CheckFailure) as caught:
            harness.assert_unchanged(before, {}, private=True)
        report = harness.failure_report(caught.exception)
        self.assertEqual(report['stage'], 'idempotent-1')
        self.assertEqual(report['checks'], ['initial-blocker-boundary-unchanged'])
        self.assertEqual(report['changed_fields'], ['container.RestartCount'])

    def test_complete_refusal_snapshot_detects_control_history_and_empty_directory_changes(self):
        for name in ('control/token', 'history/old/config.json', 'new-archive-directory'):
            with self.subTest(name=name):
                before = harness.snapshot_boundary({}, private=True, complete=True)
                path = self.path / name
                path.parent.mkdir(parents=True, exist_ok=True)
                if name.endswith('directory'):
                    path.mkdir()
                else:
                    path.write_bytes(b'PRIVATE_NEW_CONTENT')
                with self.assertRaises(harness.CheckFailure) as caught:
                    harness.assert_unchanged(before, {}, private=True, complete=True)
                self.assertIn('private_tree', caught.exception.changed_fields)
                report = json.dumps(harness.failure_report(caught.exception))
                self.assertNotIn('PRIVATE', report)
                self.assertNotIn(name, report)


class DefaultYamlFixture(unittest.TestCase):
    @staticmethod
    def capture_identity_probe(module=harness):
        """Stop at the actual launch site; never execute a Docker command."""
        config = {'project': 'legacy', 'data_volume': 'PRIVATE_VOLUME', 'instance_id': 'PRIVATE_INSTALLATION'}
        journal = {'phase': 'removed', 'installation': 'PRIVATE_INSTALLATION', 'before_image_id': 'PRIVATE_IMAGE'}
        info = inspection()
        info.update(Name='/p2pstream')
        info['State']['Running'] = False
        info['Config']['Labels'] = {'com.docker.compose.project': 'legacy', 'com.docker.compose.service': 'p2pstream'}
        captured = []

        class ProbeCaptured(BaseException):
            pass

        def docker(*args, **kwargs):
            if args[0] == 'run':
                captured.append((args, kwargs))
                raise ProbeCaptured()
            return 'PRIVATE_ID' if args[0] == 'ps' else json.dumps([info])

        with patch.object(module, 'read', side_effect=[config, journal]), patch.object(module, 'docker', docker), \
                patch.object(module, 'STAGE', 'setup'):
            try:
                module.default_yaml_case({}, None)
            except ProbeCaptured:
                return captured[0]
        raise AssertionError('synthetic probe capture was not reached')

    def test_identity_probe_retains_sandbox_and_both_required_capabilities(self):
        args, kwargs = self.capture_identity_probe()
        self.assertEqual([args[i + 1] for i, value in enumerate(args) if value == '--cap-add'],
                         ['DAC_READ_SEARCH', 'NET_BIND_SERVICE'])
        for flag, value in (('--cap-drop', 'ALL'), ('--network', 'none'), ('--user', '0:0'),
                            ('--security-opt', 'no-new-privileges'), ('--env', 'CONFIG_DIR=/server-data'),
                            ('--entrypoint', '/app/p2pstream')):
            self.assertEqual(args[args.index(flag) + 1], value)
        self.assertIn('--read-only', args)
        self.assertIn('--rm', args)
        self.assertEqual(args[args.index('--mount') + 1],
                         'type=volume,src=PRIVATE_VOLUME,dst=/server-data,readonly')
        self.assertEqual(args[-2:], ('PRIVATE_IMAGE', 'server-installation-identity'))
        self.assertEqual(kwargs, {'timeout': 30, 'command_label': 'default-yaml-legacy-identity'})

    def test_default_yaml_command_labels_are_explicit_fixed_allowlisted_literals(self):
        tree = ast.parse(Path(harness.__file__).read_text())
        case = next(node for node in tree.body if isinstance(node, ast.FunctionDef)
                    and node.name == 'default_yaml_case')
        for call in ast.walk(case):
            if not isinstance(call, ast.Call) or not isinstance(call.func, ast.Name):
                continue
            keys = ('command_label',) if call.func.id in {'docker', 'compose', 'fresh', 'run', 'owner_bytes'} \
                else (('list_label', 'inspect_label') if call.func.id == 'server_info' else ())
            for key in keys:
                value = next((kw.value for kw in call.keywords if kw.arg == key), None)
                self.assertIsInstance(value, ast.Constant)
                self.assertIn(value.value, harness.COMMAND_LABELS)
                self.assertNotEqual(value.value, 'command')

    def test_image_pin_changes_only_one_line_of_actual_bundle_source_template(self):
        template = (ROOT.parent / 'compose.yaml').read_bytes()
        replacement = harness.pinned_default_yaml(template, 'PRIVATE_PINNED_IMAGE')
        old_lines, new_lines = template.splitlines(keepends=True), replacement.splitlines(keepends=True)
        self.assertEqual(len(old_lines), len(new_lines))
        changed = [(old, new) for old, new in zip(old_lines, new_lines) if old != new]
        self.assertEqual(len(changed), 1)
        self.assertTrue(changed[0][0].startswith(b'    image: '))
        self.assertEqual(changed[0][1], b'    image: PRIVATE_PINNED_IMAGE\n')
        for setting in (b'read_only: true', b'tmpfs:', b'/tmp:rw,nosuid,nodev,mode=1777',
                        b'cap_drop:', b'ALL', b'cap_add:', b'NET_BIND_SERVICE',
                        b'security_opt:', b'no-new-privileges:true'):
            self.assertIn(setting, replacement)

    def test_hardening_checks_require_each_model_and_runtime_control(self):
        model = {'read_only': True, 'cap_drop': ['ALL'], 'cap_add': ['NET_BIND_SERVICE'],
                 'security_opt': ['no-new-privileges:true'],
                 'tmpfs': ['/tmp:rw,nosuid,nodev,mode=1777']}
        runtime = {'HostConfig': {'ReadonlyRootfs': True, 'CapDrop': ['ALL'],
                                 'CapAdd': ['CAP_NET_BIND_SERVICE'],
                                 'SecurityOpt': ['no-new-privileges:true'],
                                 'Tmpfs': {'/tmp': 'rw,nosuid,nodev,mode=1777'}}}
        harness.assert_hardened_model(model)
        harness.assert_hardened_runtime(runtime)
        for field in model:
            with self.subTest(model_control=field):
                changed = copy.deepcopy(model)
                del changed[field]
                with self.assertRaises(harness.CheckFailure):
                    harness.assert_hardened_model(changed)
        for field in runtime['HostConfig']:
            with self.subTest(runtime_control=field):
                changed = copy.deepcopy(runtime)
                changed['HostConfig'][field] = False if field == 'ReadonlyRootfs' else None
                with self.assertRaises(harness.CheckFailure):
                    harness.assert_hardened_runtime(changed)
        for option in ('rw', 'nosuid', 'nodev', 'mode=1777'):
            with self.subTest(tmpfs_option=option):
                changed = copy.deepcopy(runtime)
                changed['HostConfig']['Tmpfs']['/tmp'] = ','.join(
                    value for value in runtime['HostConfig']['Tmpfs']['/tmp'].split(',') if value != option)
                with self.assertRaises(harness.CheckFailure):
                    harness.assert_hardened_runtime(changed)

    def test_ambiguous_template_is_refused_before_any_replacement(self):
        for template in (b'services:\n', b'    image: one\n    image: two\n'):
            with self.subTest(template=template):
                with self.assertRaisesRegex(harness.CheckFailure, '^default-yaml-template-image-layout$'):
                    harness.pinned_default_yaml(template, 'PRIVATE_IMAGE')

    def test_stopped_legacy_requires_matching_retained_project_image_data_and_installation(self):
        config = {'project': 'legacy', 'data_volume': 'PRIVATE_VOLUME', 'instance_id': 'PRIVATE_INSTALLATION'}
        journal = {'phase': 'removed', 'installation': 'PRIVATE_INSTALLATION', 'before_image_id': 'PRIVATE_IMAGE'}
        info = inspection()
        info['Name'] = '/p2pstream'
        info['State']['Running'] = False
        info['Config']['Labels'] = {'com.docker.compose.project': 'legacy', 'com.docker.compose.service': 'p2pstream'}
        harness.prove_stopped_legacy(info, config, journal)
        mutations = [('running', lambda i, c, j: i['State'].update(Running=True)),
                     ('paused', lambda i, c, j: i['State'].update(Paused=True)),
                     ('restarting', lambda i, c, j: i['State'].update(Restarting=True)),
                     ('name', lambda i, c, j: i.update(Name='/unrelated')),
                     ('project', lambda i, c, j: i['Config']['Labels'].update({'com.docker.compose.project': 'unrelated'})),
                     ('image', lambda i, c, j: i.update(Image='unrelated')),
                     ('data', lambda i, c, j: i['Mounts'][0].update(Name='unrelated')),
                     ('readonly-data', lambda i, c, j: i['Mounts'][0].update(RW=False)),
                     ('identity', lambda i, c, j: j.update(installation='unrelated')),
                     ('phase', lambda i, c, j: j.update(phase='healthy'))]
        for name, mutate in mutations:
            with self.subTest(mutation=name):
                actual, saved_config, saved_journal = copy.deepcopy(info), dict(config), dict(journal)
                mutate(actual, saved_config, saved_journal)
                with self.assertRaises(harness.CheckFailure):
                    harness.prove_stopped_legacy(actual, saved_config, saved_journal)

    def test_default_worker_context_is_explicit_and_does_not_change_original_binding_guard(self):
        with patch.multiple(harness, ROOT=Path('/private/case'), PROJECT='original', VOLUME='original-volume', CHECKS=[]):
            with patch.object(harness, 'run', return_value=subprocess.CompletedProcess([], 0, b'', b'')) as run:
                harness.fresh('rollback', default_yaml=True)
                self.assertIn('--default-yaml', run.call_args.args[0])
                self.assertEqual(run.call_args.kwargs['timeout'], 600)
            harness.default_yaml_context()
            self.assertEqual(harness.ROOT, Path('/private/case/default-yaml'))
            self.assertEqual(harness.PROJECT, harness.DEPLOYMENT.name)
            self.assertEqual(harness.VOLUME, 'p2pstream-data')


class CommandFailureDiagnostics(unittest.TestCase):
    """Run the actual command wrapper and CLI summary writer with synthetic I/O."""

    def entrypoint(self, main, *, root=None):
        with tempfile.TemporaryDirectory() as directory, contextlib.ExitStack() as stack:
            path = Path(directory) if root is None else root
            stack.enter_context(patch.multiple(harness, ROOT=path, CHECKS=[], STAGE='default-yaml-preflight'))
            stack.enter_context(patch.object(sys, 'argv', [harness.__file__]))
            output = stack.enter_context(contextlib.redirect_stderr(io.StringIO()))
            entrypoint = ast.parse(Path(harness.__file__).read_text()).body[-1]
            namespace = dict(vars(harness), __name__='__main__', main=main, isolated=lambda: None)
            with self.assertRaises(SystemExit) as caught:
                exec(compile(ast.Module(body=[entrypoint], type_ignores=[]), harness.__file__, 'exec'), namespace)
            self.assertEqual(caught.exception.code, 1)
            return json.loads((path / 'summary.json').read_bytes()), output.getvalue()

    def failed_command(self, *, stderr=b'', stdout=b'PRIVATE_STDOUT', code=255,
                       label='default-yaml-legacy-identity', error=None):
        def main():
            harness.run(['PRIVATE_ARGUMENT', 'PRIVATE_TOKEN'], command_label=label)
        result = subprocess.CompletedProcess([], code, stdout, stderr)
        with patch.object(harness.subprocess, 'run', side_effect=error, return_value=result) as command:
            report, output = self.entrypoint(main)
        rendered = json.dumps(report) + output
        self.assertNotIn('PRIVATE', rendered)
        self.assertNotIn('/secret', rendered)
        if 'command_failure' in report:
            self.assertIn(' command=' + report['command_failure']['label'], output)
            self.assertIn(' exit=' + str(report['command_failure']['exit_code']), output)
            self.assertIn(' stderr=' + report['command_failure']['stderr_category'], output)
        return report, command

    def test_fixed_label_numeric_exit_and_first_line_category_reach_real_summary(self):
        cases = [
            (b'', 'empty'),
            (b'exec /secret/PRIVATE_TOKEN: operation not permitted\nPRIVATE_MODEL=secret', 'exec-permission-denied'),
            (b'PRIVATE_ENV=secret permission denied', 'permission-denied'),
            (b'Cannot connect to the Docker daemon at /secret/PRIVATE_SOCKET', 'daemon-unavailable'),
            (b'Error: lstat /secret/PRIVATE_FILE: no such file or directory', 'file-not-found'),
            (b'Error: invalid persisted installation identity PRIVATE_TOKEN', 'invalid-identity'),
            (b'Error: unsafe updater state file PRIVATE_MODEL', 'unsafe-state'),
            (b'PRIVATE_TOKEN PRIVATE_MODEL PRIVATE_ENV /secret/path', 'unclassified'),
            (b'PRIVATE_FIRST_LINE\nexec PRIVATE_TOKEN: operation not permitted', 'unclassified'),
            (b'\nexec PRIVATE_TOKEN: operation not permitted', 'empty'),
            (b' \t\r\nPRIVATE_TOKEN', 'empty'),
            (b'exec operation not permitted ' + b'P' * 2048 + b'PRIVATE_TOKEN', 'unclassified'),
            (b'P' * 1025 + b'\npermission denied PRIVATE_TOKEN', 'unclassified'),
            (b'P' * 1024 + b'\npermission denied PRIVATE_TOKEN', 'unclassified'),
        ]
        for index, (stderr, category) in enumerate(cases):
            with self.subTest(case=index):
                report, command = self.failed_command(stderr=stderr)
                self.assertEqual(report['check'], 'command-exit-255')
                self.assertEqual(report['command_failure'], {
                    'label': 'default-yaml-legacy-identity', 'exit_code': 255,
                    'kind': 'exit', 'stderr_category': category})
                command.assert_called_once()

    def test_unknown_labels_are_refused_without_execution_or_public_reflection(self):
        for label in ('PRIVATE_UNKNOWN_LABEL', None, []):
            report, command = self.failed_command(label=label)
            self.assertEqual(report['check'], 'invalid-command-label')
            self.assertNotIn('command_failure', report)
            command.assert_not_called()

    def test_generic_label_and_negative_exit_remain_bounded(self):
        report, _ = self.failed_command(label='command', code=-9)
        self.assertEqual(report['command_failure']['label'], 'command')
        self.assertEqual(report['command_failure']['exit_code'], -9)

    def test_timeout_and_oserror_never_reflect_exception_arguments_or_output(self):
        errors = [subprocess.TimeoutExpired(['PRIVATE_COMMAND'], 1, output=b'PRIVATE_STDOUT',
                                           stderr=b'exec /secret/PRIVATE_TOKEN: permission denied'),
                  OSError(errno.EACCES, 'PRIVATE_ENV /secret/path', 'PRIVATE_ARGUMENT')]
        for index, error in enumerate(errors):
            report, _ = self.failed_command(error=error)
            self.assertEqual(report['check'], 'command-unavailable-or-timed-out')
            self.assertIsNone(report['command_failure']['exit_code'])
            self.assertEqual(report['command_failure']['kind'], 'timeout' if index == 0 else 'unavailable')
            self.assertEqual(report['command_failure']['stderr_category'],
                             'exec-permission-denied' if index == 0 else 'unclassified')

    def test_success_still_enforces_output_limit_and_expected_exit(self):
        report, _ = self.failed_command(stdout=b'P' * (2 * 1024 * 1024 + 1), code=0)
        self.assertEqual(report['check'], 'command-output-limit')
        self.assertEqual(report['command_failure']['kind'], 'output-limit')
        result = subprocess.CompletedProcess([], 75, b'', b'')
        with patch.object(harness.subprocess, 'run', return_value=result):
            self.assertIs(harness.run(['PRIVATE_ARGUMENT'], expected=75), result)

    def test_primary_failure_survives_cleanup_command_or_refusal_without_retry(self):
        # The real default-YAML body reaches Compose configuration and then its
        # real finally block. Only subprocess transport and private read inputs
        # are synthetic; fixture writes use the real temporary filesystem.
        for cleanup in ('inspect', 'name-list', 'rename', 'identity-refusal', 'name-occupied'):
            with self.subTest(cleanup=cleanup), tempfile.TemporaryDirectory() as directory, \
                    contextlib.ExitStack() as stack:
                path = Path(directory)
                root = path / 'lifecycle'
                root.mkdir()
                retained = root / 'completed-legacy-state'
                retained.mkdir()
                info = inspection()
                info.update(Name='/p2pstream')
                info['State']['Running'] = False
                info['Config']['Labels'] = {'com.docker.compose.project': 'legacy', 'com.docker.compose.service': 'p2pstream'}
                config = {'project': 'legacy', 'data_volume': 'PRIVATE_VOLUME', 'instance_id': 'PRIVATE_INSTALLATION'}
                journal = {'phase': 'removed', 'installation': 'PRIVATE_INSTALLATION', 'before_image_id': 'PRIVATE_IMAGE'}
                (retained / 'config.json').write_text(json.dumps(config))
                (retained / 'host.json').write_text(json.dumps(journal))
                saved = False
                calls = []

                def command(args, **kwargs):
                    nonlocal saved
                    self.assertEqual(args[0], 'docker')
                    calls.append(args)
                    a, out, code = args[1:], b'', 0
                    if a[0] == 'rename':
                        if saved and cleanup == 'rename':
                            code = 255
                        else:
                            saved = a[-1] != 'p2pstream'
                    elif a[0] == 'ps':
                        if saved:
                            code = 255 if cleanup == 'name-list' else 0
                            out = b'PRIVATE_OCCUPANT' if cleanup == 'name-occupied' else b''
                        elif a[-1] == 'name=^/p2pstream$':
                            out = b'PRIVATE_ID'
                    elif a[0] == 'inspect':
                        current = copy.deepcopy(info)
                        if saved:
                            code = 255 if cleanup == 'inspect' else 0
                            current['Name'] = '/review-default-yaml-retained-legacy'
                            if cleanup == 'identity-refusal':
                                current['Image'] = 'PRIVATE_CHANGED_IMAGE'
                        out = json.dumps([current]).encode()
                    elif a[0] == 'run':
                        out = b'PRIVATE_INSTALLATION'
                    elif a[:2] == ['volume', 'ls']:
                        pass
                    elif a[:2] == ['compose', 'config']:
                        code = 17
                    else:
                        self.fail('unexpected synthetic command')
                    return subprocess.CompletedProcess(args, code, out, b'PRIVATE_TOKEN /secret/model')

                deployment = path / 'deployment'
                stack.enter_context(patch.multiple(harness, ROOT=root, STATE=path / 'state',
                                                   DEPLOYMENT=deployment, CANONICAL=deployment / 'compose.yaml',
                                                   INPUTS=path / 'inputs.yaml', BUNDLE=ROOT.parent))
                stack.enter_context(patch.object(harness.subprocess, 'run', side_effect=command))
                original = harness.ROOT, harness.PROJECT, harness.VOLUME
                def main():
                    harness.default_yaml_case({'baseline': 'PRIVATE_IMAGE'}, None)

                report, output = self.entrypoint(main, root=root)
                self.assertEqual(report['check'], 'command-exit-17')
                self.assertEqual(report['command_failure']['label'], 'default-yaml-compose-config')
                self.assertEqual((harness.ROOT, harness.PROJECT, harness.VOLUME), original)
                renames = [a for a in calls if a[1] == 'rename']
                self.assertEqual(len(renames), 2 if cleanup == 'rename' else 1)
                if cleanup == 'name-occupied':
                    self.assertNotIn('cleanup_failure', report)
                else:
                    failure = report['cleanup_failure']
                    if cleanup == 'identity-refusal':
                        self.assertEqual(failure['check'], 'default-yaml-retained-legacy-changed')
                        self.assertNotIn('command_failure', failure)
                        self.assertEqual(calls[-1][1], 'inspect')
                    else:
                        self.assertEqual(failure['check'], 'command-exit-255')
                        self.assertEqual(failure['command_failure']['exit_code'], 255)
                        self.assertEqual(failure['command_failure']['label'], {
                            'inspect': 'default-yaml-cleanup-legacy-inspect',
                            'name-list': 'default-yaml-cleanup-name-list',
                            'rename': 'default-yaml-restore-legacy-name'}[cleanup])
                self.assertNotIn('PRIVATE', json.dumps(report))
                self.assertNotIn('/secret', json.dumps(report))
                self.assertNotIn('PRIVATE', output)


class ReplacementRecoveryInputs(unittest.TestCase):
    """Check harness ordering/acceptance only; no product or Docker proof."""

    def run_case(self, operation, action, input_change, *, corrupt_recovery_inputs=False):
        with tempfile.TemporaryDirectory() as directory, contextlib.ExitStack() as stack:
            path = Path(directory)
            state = path / 'state'
            state.mkdir()
            canonical, inputs = path / 'compose.yaml', path / 'compose.inputs.yaml'
            model = {'services': {'p2pstream': {'image': 'PRIVATE_CANDIDATE',
                      'environment': {'APPLIED_SETTING': 'retained', 'SERVER_UPDATE_GENERATION': 'old'}}}}
            editable_model = copy.deepcopy(model)
            del editable_model['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION']
            harness.write(canonical, model)
            # Distinct formatting makes the revert case require exact original
            # bytes, rather than reconstructing an equivalent JSON object.
            original = json.dumps(editable_model, separators=(',', ':')).encode() + b'\n\n'
            inputs.write_bytes(original)
            (state / 'config.json').write_bytes(b'PRIVATE_CREDENTIALS')
            before = inspection()
            before['Config']['Env'] = ['APPLIED_SETTING=retained', 'SERVER_UPDATE_GENERATION=old']
            before['HostConfig'] = {'PortBindings': {'8081/tcp': [{'HostPort': '18101'}]}}
            after = copy.deepcopy(before)
            after['Id'] = 'PRIVATE_REPLACEMENT'
            after['Config']['Env'] = ['APPLIED_SETTING=retained', 'SERVER_UPDATE_GENERATION=new']
            calls, staged, recovered, gap_observed = [], None, False, False

            def docker(*args, **kwargs):
                nonlocal gap_observed
                if args[:2] == ('volume', 'inspect'):
                    return json.dumps([{'Name': 'PRIVATE_VOLUME', 'Mountpoint': '/PRIVATE_DATA'}])
                self.assertEqual(args[:3], ('ps', '--all', '--quiet'))
                self.assertFalse(recovered)
                self.assertIsNotNone(staged)
                self.assertEqual(inputs.read_bytes(), staged)
                gap_observed = True
                calls.append('confirmed-missing-server')
                return ''

            def fresh(actual_action, **kwargs):
                nonlocal staged, recovered
                self.assertEqual(kwargs['release'], 'candidate')
                if kwargs.get('interrupt'):
                    self.assertEqual((actual_action, kwargs['interrupt']), (operation, 'after-server-removal'))
                    self.assertEqual(inputs.read_text().count('UNCOMMITTED_SETTING'), 1)
                    staged = inputs.read_bytes()
                    harness.write(state / 'host.json', {'phase': 'applying' if operation == 'apply' else 'removing'})
                    (state / 'before-compose.json').write_bytes(canonical.read_bytes())
                    calls.append('interrupted-' + operation)
                    return
                self.assertEqual(actual_action, action)
                self.assertTrue(gap_observed)
                post_removal = inputs.read_bytes()
                if input_change == 'revert':
                    self.assertEqual(post_removal, original)
                elif input_change == 'edit':
                    self.assertNotEqual(post_removal, staged)
                    self.assertIn('operator-edited-after-' + operation + '-removal-before-' + action,
                                  post_removal.decode())
                else:
                    self.assertEqual(post_removal, staged)
                if corrupt_recovery_inputs:
                    inputs.write_bytes(staged)
                restored = copy.deepcopy(model)
                restored['services']['p2pstream']['environment']['SERVER_UPDATE_GENERATION'] = 'new'
                harness.write(canonical, restored)
                recovered = True
                calls.append('fresh-' + action)

            stack.enter_context(patch.multiple(harness, STATE=state, CANONICAL=canonical, INPUTS=inputs, CHECKS=[]))
            stack.enter_context(patch.object(harness, 'docker', side_effect=docker))
            stack.enter_context(patch.object(harness, 'fresh', side_effect=fresh))
            stack.enter_context(patch.object(harness, 'server_info', side_effect=lambda: after if recovered else before))
            stack.enter_context(patch.object(harness, 'assert_binding'))
            stack.enter_context(patch.object(harness, 'owner_bytes', return_value=b'PRESENT:{"allowed":true,"generation":"new"}'))
            harness.replacement_recovery({'candidate': 'PRIVATE_CANDIDATE'}, action,
                                         operation=operation, input_change=input_change)
            self.assertEqual(calls, ['interrupted-' + operation, 'confirmed-missing-server', 'fresh-' + action])
            self.assertEqual(len(harness.CHECKS), 1)
            return harness.CHECKS[0]

    def test_full_matrix_changes_or_reverts_inputs_only_after_confirmed_removal(self):
        for operation in ('apply', 'remove'):
            for action in ('repair', 'rollback'):
                for input_change in ('edit', 'revert'):
                    with self.subTest(operation=operation, action=action, input_change=input_change):
                        check = self.run_case(operation, action, input_change)
                        self.assertIn('preserves-post-removal-' + input_change + '-inputs', check)

    def test_preexisting_apply_cases_and_check_names_remain(self):
        for action in ('repair', 'rollback'):
            with self.subTest(action=action):
                self.assertEqual(self.run_case('apply', action, 'unchanged'),
                                 'fresh-' + action + '-after-real-owned-apply-removal-preserves-current-software-data-settings')

    def test_recovery_that_restores_staged_inputs_cannot_pass_after_operator_changes(self):
        for input_change in ('edit', 'revert'):
            with self.subTest(input_change=input_change):
                with self.assertRaisesRegex(harness.CheckFailure, '^missing-server-recovery-changed-credentials-or-editable-inputs$'):
                    self.run_case('remove', 'repair', input_change, corrupt_recovery_inputs=True)


class LegacyWorkerTransport(unittest.TestCase):
    def load_functions(self, *names):
        # Importing the real legacy driver would initialize its disposable
        # daemon. Compile only the selected pure harness definitions instead.
        source = ast.parse((ROOT / 'test-server-install-docker.py').read_bytes())
        functions = [node for node in source.body
                     if isinstance(node, (ast.FunctionDef, ast.ClassDef)) and node.name in names]
        self.assertEqual(len(functions), len(names))
        namespace = {'json': json, 'subprocess': subprocess, 'sys': sys, '__file__': 'legacy-driver.py',
                     'argparse': argparse, 'base64': base64, 'os': os, 'Path': Path}
        exec(compile(ast.Module(body=functions, type_ignores=[]), 'legacy-driver.py', 'exec'), namespace)
        return namespace

    def test_semantic_compose_rerender_preserves_every_field_array_order_and_real_status(self):
        rerender = self.load_functions('semantic_compose_result')['semantic_compose_result']
        model = {'z': {'unknown': [3, 1, 3, {'secret': 'PRIVATE_VALUE', 'null': None}]},
                 'services': {'p2pstream': {'ports': [{'published': '18102'}, {'published': '18101'}]}}}
        raw = json.dumps(model, separators=(',', ':')).encode()
        original = subprocess.CompletedProcess(['real', 'compose'], 0, raw, b'PRIVATE_STDERR')
        result = rerender(original)
        self.assertEqual(json.loads(result.stdout), model)
        self.assertNotEqual(result.stdout, raw)
        self.assertEqual((result.args, result.returncode, result.stderr),
                         (original.args, original.returncode, original.stderr))
        self.assertEqual(original.stdout, raw)
        with self.assertRaises(AssertionError):
            rerender(subprocess.CompletedProcess(['real', 'compose'], 1, raw, b'PRIVATE_ERROR'))

    def test_fresh_legacy_requires_actual_expected_process_exit_and_bounded_isolated_interpreter(self):
        namespace = self.load_functions('fresh_legacy')
        with tempfile.TemporaryDirectory() as directory:
            result_path = Path(directory) / 'result.json'
            namespace.update(WORKER_RESULT=result_path, INTERRUPTED=75)
            calls = []

            def run(arguments, **kwargs):
                calls.append((arguments, kwargs))
                result_path.write_text(json.dumps({'result': 'completed', 'events': []}))
                return subprocess.CompletedProcess(arguments, 0, b'PRIVATE_STDOUT', b'PRIVATE_STDERR')

            with patch.object(subprocess, 'run', side_effect=run):
                self.assertEqual(namespace['fresh_legacy']('repair')['result'], 'completed')
            self.assertEqual(calls[0][0][:4], [sys.executable, '-I', 'legacy-driver.py', '--legacy-worker'])
            self.assertEqual(calls[0][1], {'capture_output': True, 'timeout': 600})
            with patch.object(subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, b'PRIVATE', b'PRIVATE')):
                with self.assertRaisesRegex(AssertionError, '^fresh-legacy-repair-exit-1$'):
                    namespace['fresh_legacy']('repair')

    def test_all_docker_driver_subprocess_calls_have_explicit_deadlines(self):
        for name in ('test-server-install-docker.py', 'test-server-lifecycle-docker.py'):
            tree = ast.parse((ROOT / name).read_bytes())
            for node in ast.walk(tree):
                if (isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute)
                        and isinstance(node.func.value, ast.Name) and node.func.value.id == 'subprocess'
                        and node.func.attr in {'run', 'check_output', 'check_call', 'call'}):
                    self.assertIn('timeout', {item.arg for item in node.keywords}, (name, node.lineno))

    def test_detach_interruption_occurs_after_original_detach_and_never_reaches_start(self):
        namespace = self.load_functions('legacy_worker', 'DeliberateInterruption')
        with tempfile.TemporaryDirectory() as directory:
            path, calls = Path(directory), []
            context, result_path = path / 'context.json', path / 'result.json'
            context.write_text(json.dumps({'args': {}, 'manifest': {}, 'files': {}, 'descriptor': {},
                                           'image': 'PRIVATE_IMAGE', 'source': directory, 'options': []}))
            target = {'Id': 'PRIVATE_TARGET', 'Image': 'PRIVATE_IMAGE',
                      'State': {'Running': False, 'Status': 'created'},
                      'Config': {'Env': []}, 'Mounts': []}

            class Host:
                journal = {'phase': 'rolling_back', 'before_image_id': 'PRIVATE_IMAGE'}
                config = {'data_volume': 'PRIVATE_VOLUME'}

                def runtime(self, action):
                    calls.append(('actual-runtime', action))

                def compose(self, *args, **kwargs):
                    calls.append(('actual-compose', args))
                    return subprocess.CompletedProcess(args, 0, b'PRIVATE_TARGET\n', b'')

                def record_runtime(self):
                    raise AssertionError('unneeded-record-runtime')

                def clear_blocked_initial_owner(self, *args):
                    raise AssertionError('unneeded-owner-unlink')

            def docker(*args, **kwargs):
                calls.append(('actual-docker', args))
                if args[:1] == ('inspect',):
                    value = [target]
                else:
                    self.assertEqual(args[:2], ('volume', 'inspect'))
                    value = [{'Driver': 'local', 'Mountpoint': directory}]
                return subprocess.CompletedProcess(args, 0, json.dumps(value).encode(), b'')

            def install(*args):
                Host().runtime('detach')
                calls.append(('start-must-not-happen',))

            fake = types.SimpleNamespace(Host=Host, docker=docker, install=install,
                                         Failure=RuntimeError)
            namespace.update(host=fake, WORKER_CONTEXT=context, WORKER_RESULT=result_path,
                             INTERRUPTED=75, REPLACEMENT_FAULT='fixed', RECORD_FAULT='fixed')
            with patch.object(os, 'chdir'):
                self.assertEqual(namespace['legacy_worker'](['install', '--interrupt', 'after-runtime-detach']), 75)
            report = json.loads(result_path.read_bytes())
            self.assertEqual(report['result'], 'interrupted')
            self.assertEqual(report['checkpoint']['id'], 'PRIVATE_TARGET')
            self.assertEqual(calls[0], ('actual-runtime', 'detach'))
            self.assertEqual(report['events'], ['actual-created-standalone-target-detached-before-start'])
            self.assertNotIn(('start-must-not-happen',), calls)


class MainFailureSummary(unittest.TestCase):
    def test_real_entrypoint_keeps_main_stage_passes_and_fixed_changed_fields(self):
        with contextlib.ExitStack() as stack:
            path = Path(stack.enter_context(tempfile.TemporaryDirectory()))
            info = inspection()
            releases = {name: {'release': {'version': name, 'commit': name}}
                        for name in ('baseline', 'candidate')}

            def docker(*args):
                if args[0] == 'info':
                    return 'amd64'
                if args[-1] in {'server-deployment-capabilities', 'server-enrollment-capabilities'}:
                    value = {'runtime_guard': 2, 'version': args[-2], 'commit': args[-2]}
                    if args[-1] == 'server-enrollment-capabilities':
                        value.update(enrollment_preflight=1, canonical_compose=1)
                    return json.dumps(value)
                return 'PRIVATE_COMMAND_RESULT'

            def refused_worker(*args, **kwargs):
                # Fault injection after the real main snapshot, with no Docker.
                info['State']['StartedAt'] = 'PRIVATE_CHANGED_TIME'
                info['Mounts'][0]['Source'] = '/PRIVATE_CHANGED_SOURCE'

            stack.enter_context(patch.multiple(harness, ROOT=path / 'lifecycle', STATE=path / 'state',
                                               DEPLOYMENT=path / 'deployment',
                                               CANONICAL=path / 'deployment/compose.yaml',
                                               INPUTS=path / 'inputs.yaml'))
            stack.enter_context(patch.object(harness, 'CHECKS', [], create=True))
            stack.enter_context(patch.object(harness, 'STAGE', 'setup', create=True))
            replacements = {'isolated': lambda: None, 'load_host': lambda: None,
                            'default_yaml_case': lambda *args: None,
                            'read': lambda *a: {'baseline': 'baseline', 'candidate': 'candidate'},
                            'docker': docker, 'compose': lambda *a: 'PRIVATE_MODEL',
                            'server_info': lambda **kw: info, 'owner_bytes': lambda *a: b'PRIVATE_OWNER',
                            'wait': lambda *a: None, 'fixture_catalog': lambda *a: releases,
                            'fresh': refused_worker, 'preflight': lambda: {'ready': False}}
            for name, replacement in replacements.items():
                stack.enter_context(patch.object(harness, name, replacement))
            stack.enter_context(patch.object(harness, 'Management'))
            stack.enter_context(patch.object(sys, 'argv', [harness.__file__]))
            stack.enter_context(contextlib.redirect_stderr(io.StringIO()))
            # Execute the actual CLI exception handler, not a test reimplementation.
            entrypoint = ast.parse(Path(harness.__file__).read_text()).body[-1]
            namespace = dict(vars(harness), __name__='__main__')
            with self.assertRaises(SystemExit) as caught:
                exec(compile(ast.Module(body=[entrypoint], type_ignores=[]), harness.__file__, 'exec'), namespace)
            self.assertEqual(caught.exception.code, 1)
            report = json.loads((path / 'lifecycle/summary.json').read_bytes())
            self.assertEqual(report['check'], 'refusal-changed-container')
            self.assertEqual(report['stage'], 'initial-blocker')
            self.assertEqual(report['checks'], ['old-and-new-capability-contracts'])
            self.assertEqual(report['changed_fields'], ['container.StartedAt', 'container.Mounts'])
            self.assertNotIn('PRIVATE', json.dumps(report))
            self.assertNotIn(str(path), json.dumps(report))


class WorkerRefusals(unittest.TestCase):
    def setUp(self):
        # Reuse the deterministic host/Compose fixture, including real locks,
        # preparation, preflight parsing and journal/file safety checks. Its
        # Docker transport and Go enrollment command are fixtures, not engines.
        self.fixture = fixture = host_tests.SingleFileLifecycle()
        fixture.setUp()
        self.addCleanup(fixture.tearDown)
        self.stack = stack = contextlib.ExitStack()
        self.addCleanup(stack.close)
        self.blocked = False
        fixture.info['State']['StartedAt'] = 'fixture-start'
        fixture.info['RestartCount'] = 0
        stack.enter_context(patch.object(harness, 'CHECKS', [], create=True))
        stack.enter_context(patch.object(harness, 'STAGE', 'setup', create=True))
        self.options = argparse.Namespace(action='install', release='baseline', variant='',
                                          interrupt='', label='prepared-blocker',
                                          refusal='blocker', block_after_prepare=True)
        host = host_tests.host
        release = {'manifest': fixture.manifest, 'descriptor': fixture.descriptor,
                   'release': {'image': host_tests.REF}}
        original_read, original_docker = harness.read, host.docker

        def read(path):
            if str(path) == '/review/images.json':
                return {}
            if path == fixture.path / 'catalog.json':
                return {'baseline': release}
            if path == fixture.path / 'identity.json':
                return {'installation': host_tests.ID}
            return original_read(path)

        def docker(*args, **kwargs):
            if self.blocked and args[0] == 'exec' and args[-1] == 'server-enrollment-preflight':
                response = {'api': 1, 'installation_id': host_tests.ID,
                            'version': fixture.args.expect_version, 'commit': fixture.args.expect_commit,
                            'ready': False, 'blockers': [{'code': 'certificate_rotation_active',
                                                        'action': 'ignored', 'message': 'ignored'}]}
                return subprocess.CompletedProcess(args, 0, json.dumps(response).encode(), b'')
            return original_docker(*args, **kwargs)

        outer = self

        class Management:
            def login(self):
                pass

            def stage(self):
                outer.blocked = True

        def snapshot(*unused, **kwargs):
            return {'container': harness.container_snapshot(fixture.info),
                    'model': harness.file_snapshot(fixture.canonical), 'owner': b'ABSENT',
                    'files': {str(path): harness.file_snapshot(path)
                              for path in fixture.state.rglob('*') if path.is_file()}}

        stack.enter_context(patch.multiple(harness, ROOT=fixture.path, STATE=fixture.state,
                                           DEPLOYMENT=fixture.source, CANONICAL=fixture.canonical,
                                           BUNDLE=fixture.bundle))
        for name, value in [('read', read), ('load_host', lambda: host),
                            ('install_arguments', lambda *args: (fixture.args, release)),
                            ('owner_bytes', lambda *args: b'ABSENT'), ('Management', Management),
                            ('preflight', lambda: {'blockers': [{'code': 'certificate_rotation_active'}]}),
                            ('snapshot_boundary', snapshot)]:
            stack.enter_context(patch.object(harness, name, value))
        stack.enter_context(patch.object(host, 'docker', docker))

    def test_after_private_preparation_blocker_keeps_expected_refusal_and_boundaries(self):
        self.assertEqual(harness.worker(self.options), 0)
        events = harness.read(self.fixture.path / 'trace-prepared-blocker.json')
        self.assertEqual(events, ['private-prepared', 'real-blocker-after-private-prepare',
                                  'expected-blocker-refusal'])
        self.assertEqual(harness.read(self.fixture.state / 'host.json')['phase'], 'prepared')
        self.assertEqual(self.fixture.restart_count, 0)
        self.assertFalse(any(item[0] == 'binary' and item[1][0] == 'runtime-block-staged'
                             for item in self.fixture.commands))

    def replacement_worker(self, *, allowed=False, operation='apply'):
        host = host_tests.host
        self.options.action, self.options.refusal = operation, ''
        self.options.interrupt, self.options.block_after_prepare = 'after-server-removal', False
        info = copy.deepcopy(self.fixture.info)
        info['State']['Running'] = False
        info['Config']['Labels']['p2pstream.server-update.instance'] = host_tests.ID
        self.fixture.state.mkdir(mode=0o700, exist_ok=True)
        previous = b'{"fixture":"PRIVATE_SOURCE"}'
        (self.fixture.state / 'before-compose.json').write_bytes(previous)
        controller = types.SimpleNamespace(journal={'before_image_id': info['Image'],
                                                     'before_sha256': harness.sha(previous)},
                                           config={'instance_id': host_tests.ID})
        commands = []

        def docker(*args, **kwargs):
            commands.append(args)
            return ''

        def manage(*unused):
            host.Host.compose(controller, 'up', '--no-start', '--no-deps', 'p2pstream')

        with patch.object(harness, 'server_info', return_value=info), \
                patch.object(harness, 'owner_bytes', return_value=b'PRESENT:' + json.dumps({'allowed': allowed}).encode()), \
                patch.object(harness, 'docker', side_effect=docker), patch.object(host, 'manage', side_effect=manage):
            if allowed:
                with self.assertRaisesRegex(harness.CheckFailure, '^replacement-fault-owner-not-blocked$'):
                    harness.worker(self.options)
            else:
                with self.assertRaises(harness.DeliberateInterruption):
                    harness.worker(self.options)
        return commands

    def test_replacement_interruption_follows_confirmed_removal_without_returning_compose_success(self):
        for operation in ('apply', 'remove'):
            with self.subTest(operation=operation):
                commands = self.replacement_worker(operation=operation)
                self.assertEqual(commands[0], ('rm', self.fixture.info['Id']))
                self.assertEqual(commands[1][:3], ('ps', '--all', '--quiet'))
                self.assertEqual(harness.read(self.fixture.path / 'trace-prepared-blocker.json'),
                                 ['actual-owned-server-removed', 'interrupt-after-server-removal'])

    def test_replacement_fault_never_removes_a_server_without_actual_blocked_owner(self):
        for operation in ('apply', 'remove'):
            with self.subTest(operation=operation):
                self.assertEqual(self.replacement_worker(allowed=True, operation=operation), [])

    def test_worker_boundary_failure_retains_stage_passes_and_fields_through_fresh(self):
        original = host_tests.host.enrollment_preflight

        def preflight(*args, **kwargs):
            try:
                return original(*args, **kwargs)
            except host_tests.host.Failure:
                if self.blocked:
                    self.fixture.info['State']['StartedAt'] = 'PRIVATE_CHANGED_TIME'
                raise

        with patch.object(host_tests.host, 'enrollment_preflight', preflight):
            with self.assertRaises(harness.CheckFailure) as caught:
                harness.worker(self.options)
        report = harness.failure_report(caught.exception)
        self.assertEqual(report['stage'], 'after-private-preparation-boundary')
        self.assertEqual(report['checks'], ['private-prepared', 'real-blocker-after-private-prepare'])
        self.assertEqual(report['changed_fields'], ['container.StartedAt'])
        harness.write(self.fixture.path / 'failure-prepared-blocker.json', report)
        harness.stage('prepared-blocker')
        harness.CHECKS[:] = ['old-and-new-capability-contracts']
        with patch.object(harness, 'run', side_effect=harness.CheckFailure('worker-exit-1')):
            with self.assertRaises(harness.CheckFailure) as wrapped:
                harness.fresh('install', label='prepared-blocker', refusal='blocker', block=True)
        result = harness.failure_report(wrapped.exception)
        self.assertEqual(result['stage'], 'prepared-blocker')
        self.assertEqual(result['worker_stage'], report['stage'])
        self.assertEqual(result['worker_checks'], report['checks'])
        self.assertEqual(result['checks'], ['old-and-new-capability-contracts'])
        self.assertEqual(result['changed_fields'], ['container.StartedAt'])
        self.assertNotIn('PRIVATE', json.dumps(result))

    def test_unexpected_host_refusal_reports_source_sites_without_private_error(self):
        secret = 'PRIVATE_TOKEN_model_environment\nnot-for-output'
        duplicate = ('{' + json.dumps(secret) + ':1,' + json.dumps(secret) + ':2}').encode()

        def install(*unused):
            # Real host decode -> unique_pairs -> require: its refusal includes
            # an arbitrary metadata key, which must never enter the public code.
            host_tests.host.decode(duplicate)

        with patch.object(host_tests.host, 'install', install):
            with self.assertRaises(harness.CheckFailure) as raised:
                harness.worker(self.options)
        check = str(raised.exception)
        self.assertRegex(check, r'^unexpected-host-refusal-host-lines-[0-9]+(?:-[0-9]+)*$')
        self.assertNotIn(secret, check)
        self.assertNotIn('Duplicate metadata key', check)
        self.assertNotIn(str(ROOT), check)
        # The fresh-process wrapper must retain the safe diagnostic in the
        # main summary's check, not collapse it back to an unqualified refusal.
        harness.write(self.fixture.path / 'failure-prepared-blocker.json', {'check': check})
        with patch.object(harness, 'run', side_effect=harness.CheckFailure('worker-exit-1')):
            with self.assertRaises(harness.CheckFailure) as wrapped:
                harness.fresh('install', label='prepared-blocker', refusal='blocker', block=True)
        self.assertEqual(str(wrapped.exception), 'worker-prepared-blocker-' + check)

    def test_wrapped_host_failure_retains_bounded_cause_sites(self):
        host = host_tests.host
        try:
            try:
                host.validate_floor({})
            except host.Failure as error:
                controller = host.Host()
                controller.journal = {'phase': 'prepared'}
                controller.rollback = lambda: None
                controller.phase = lambda *args, **kwargs: None
                controller.recover_failure(error)
        except host.Failure as error:
            # A cycle must not grow the diagnostic or hang the worker.
            error.__cause__.__context__ = error
            check = harness.host_refusal_check(host, error)
        self.assertIn('-cause-host-lines-', check)
        self.assertTrue(re.fullmatch(r'unexpected-host-refusal-host-lines-[0-9-]+-cause-host-lines-[0-9-]+', check))
        self.assertLess(len(check), 256)
        self.assertNotIn(str(self.fixture.path), check)


if __name__ == '__main__':
    unittest.main()

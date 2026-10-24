#!/usr/bin/env python3
"""Focused canonical worker checks with deterministic host fixtures; no Docker."""
import argparse
import ast
import base64
import contextlib
import copy
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


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


host_tests = load('lifecycle_host_fixtures', ROOT / 'test-server-updater-host.py')
harness = load('lifecycle_harness', ROOT / 'test-server-lifecycle-docker.py')


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

    def replacement_worker(self, *, allowed=False):
        host = host_tests.host
        self.options.action, self.options.refusal = 'apply', ''
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
        commands = self.replacement_worker()
        self.assertEqual(commands[0], ('rm', self.fixture.info['Id']))
        self.assertEqual(commands[1][:3], ('ps', '--all', '--quiet'))
        self.assertEqual(harness.read(self.fixture.path / 'trace-prepared-blocker.json'),
                         ['actual-owned-server-removed', 'interrupt-after-server-removal'])

    def test_replacement_fault_never_removes_a_server_without_actual_blocked_owner(self):
        self.assertEqual(self.replacement_worker(allowed=True), [])

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

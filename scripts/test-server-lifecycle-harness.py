#!/usr/bin/env python3
"""Focused canonical worker checks with deterministic host fixtures; no Docker."""
import argparse
import contextlib
import copy
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys
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
            return {'container': copy.deepcopy(fixture.info),
                    'canonical': harness.file_snapshot(fixture.canonical),
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

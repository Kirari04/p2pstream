#!/usr/bin/env python3
"""Exercise the standalone setup shell and its pinned release download boundary."""
import base64
import copy
import contextlib
import datetime
import importlib.util
import gzip
import hashlib
import io
import json
import os
import shlex
import subprocess
import sys
import tarfile
import tempfile
import types
import unittest
import urllib.request
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parent
SCRIPT = (ROOT / 'setup-server-updater.sh').read_text()
CODE = SCRIPT.split("    python3 -I - \"$@\" <<'PY'\n", 1)[1].rsplit('\nPY\n}', 1)[0]
bootstrap = types.ModuleType('setup_bootstrap')
exec(compile(CODE.split('\ntry:\n    main(sys.argv[1:])', 1)[0], 'setup-server-updater.sh', 'exec'), bootstrap.__dict__)
ID = '11111111-1111-4111-8111-111111111111'
VERSION = 'v1.0.1'
BASE = 'https://github.com/test/repo/releases/download/' + VERSION + '/'
BUNDLE = 'p2pstream_' + VERSION + '_docker.tar.gz'


def digest(data):
    return hashlib.sha256(data).hexdigest()


def archive(entries):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode='w:gz', format=tarfile.USTAR_FORMAT) as stream:
        for name, data, kind in entries:
            member = tarfile.TarInfo(name)
            member.type = kind
            member.size = len(data)
            if kind in {tarfile.SYMTYPE, tarfile.LNKTYPE}:
                member.linkname = '/tmp/unsafe-setup-link'
            stream.addfile(member, io.BytesIO(data))
    return output.getvalue()


def cross_layer_wrapper(code, args, responses, result_path, scenario):
    """Real bootstrap program/parser; mock only transport, Docker and time.

    Keep one actual host lifecycle across two wrapper attempts so the second
    call must prove its durable API2 receipt, not an always-success stub.
    """
    spec = importlib.util.spec_from_file_location('host_lifecycle', ROOT / 'test-server-updater-host.py')
    lifecycle = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(lifecycle)
    case = lifecycle.BlockedInitialOwnerCancellation(methodName='runTest')
    case.setUp()
    host = lifecycle.host
    current_url = 'https://github.com/test/repo/releases/download/v1.0.2/p2pstream_agent_update_manifest.json'
    current_raw = base64.b64decode(responses[current_url])
    current = json.loads(current_raw)
    current_hash = digest(current_raw)
    case.args.manifest_sha256 = current_hash
    case.current_floor['manifest_sha256'] = current_hash
    clock = [datetime.datetime(2030, 1, 1, tzinfo=datetime.timezone.utc)]
    actual_datetime = datetime.datetime
    class FrozenDateTime(actual_datetime):
        @classmethod
        def now(cls, tz=None):
            return clock[0].astimezone(tz) if tz else clock[0].replace(tzinfo=None)
    class Opener:
        def open(self, url, **kwargs):
            result = io.BytesIO(base64.b64decode(responses[url]))
            result.geturl = lambda: url
            return result
    def admitted_metadata(repository, version, commit=None, channel=None, expected_manifest=None):
        host.require((repository, version, commit, channel, expected_manifest) == ('test/repo', current['version'], current['commit'], current['channel'], current_hash), 'Current release binding differs')
        expiry = actual_datetime.strptime(current['expires_at'], '%Y-%m-%dT%H:%M:%SZ').replace(tzinfo=datetime.timezone.utc)
        host.require(clock[0] < expiry, 'Release metadata expired; fresh controller admission refused')
        return case.manifest, case.descriptor, case.current_ref, current_hash
    actual_run = subprocess.run
    host_calls, controller_errors = [], []
    def dispatch(command, **kwargs):
        if len(command) >= 3 and command[1] == '-I' and Path(command[2]).name == 'server-updater-host.py':
            assert Path(command[2]).read_bytes() == (ROOT / 'server-updater-host.py').read_bytes()
            host_calls.append(command[3:])
            status = 0
            error = io.StringIO()
            with patch.object(sys, 'argv', command[2:]), contextlib.redirect_stderr(error):
                try:
                    host.main()  # Real argparse, receipt admission and mutation path.
                except KeyboardInterrupt:
                    status = 130
                except SystemExit as exited:
                    status = exited.code
            controller_errors.append(error.getvalue())
            if status and kwargs.get('check'):
                raise subprocess.CalledProcessError(status, command)
            return subprocess.CompletedProcess(command, status)
        return actual_run(command, **kwargs)
    statuses, wrapper_errors = [], []
    def attempt(values):
        status = 0
        errors = io.StringIO()
        with patch.object(sys, 'argv', ['-'] + values), contextlib.redirect_stderr(errors):
            try:
                exec(compile(code, 'downloaded-setup', 'exec'), {})
            except SystemExit as exited:
                status = exited.code
        statuses.append(status)
        wrapper_errors.append(errors.getvalue())
    try:
        with patch.object(datetime, 'datetime', FrozenDateTime), patch.object(urllib.request, 'build_opener', return_value=Opener()), patch.object(subprocess, 'run', side_effect=dispatch), patch.object(host, 'release_metadata', side_effect=admitted_metadata):
            retry = scenario not in {'fresh-expired', 'recovery-expired'}
            if retry:
                case.cleanup_fault = 'after-unlink'
                attempt(args)
                assert statuses == [1] and not case.marker.exists()
                assert not host.read_json(case.state / host.CANCELLATION)['completed']
                case.cleanup_fault = None
            clock[0] = actual_datetime(2030, 1, 3, tzinfo=datetime.timezone.utc)
            values = list(args)
            if scenario == 'wrong-installation':
                values[1] = '22222222-2222-4222-8222-222222222222'
            elif scenario == 'wrong-arch':
                values[3] = 'arm64'
            elif scenario == 'wrong-current-pin':
                values[-2] = '0' * 64
            elif scenario == 'wrong-current-commit':
                changed = dict(current, commit='8' * 40)
                raw = json.dumps(changed).encode()
                responses[current_url] = base64.b64encode(raw).decode()
                values[-2] = digest(raw)
            attempt(values)
        receipt_path = case.state / host.CANCELLATION
        receipt = host.read_json(receipt_path) if receipt_path.exists() else None
        result = {'statuses': statuses, 'host_calls': len(host_calls), 'controller_errors': controller_errors,
                  'wrapper_errors': wrapper_errors, 'completed': receipt and receipt['completed'],
                  'marker_exists': case.marker.exists()}
        Path(result_path).write_text(json.dumps(result))
    finally:
        case.tearDown()


class Bootstrap(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = Path(self.temp.name)
        self.receipt = self.path / 'receipt.json'
        # The final verified member really runs in a separate isolated Python
        # interpreter. No Docker, sudo privileges or live network is needed.
        host = ('import json, pathlib, sys\n'
                'root = pathlib.Path(__file__).parent\n'
                'pathlib.Path(' + repr(str(self.receipt)) + ').write_text(json.dumps({"args": sys.argv[1:], "files": sorted(p.name for p in root.iterdir()), "mode": root.stat().st_mode & 0o777}))\n').encode()
        self.entries = [(name, host if name == 'server-updater-host.py' else b'fixture', tarfile.REGTYPE) for name in sorted(bootstrap.NAMES)]
        self.descriptor = {'api': 1, 'version': VERSION, 'commit': 'b' * 40, 'channel': 'stable', 'bundle': BUNDLE,
                           'updater_images': {'linux/' + arch: 'ghcr.io/test/repo-updater@sha256:' + 'd' * 64 for arch in ('amd64', 'arm64')}}
        self.manifest = {'schema_version': 1, 'version': VERSION, 'commit': 'b' * 40, 'channel': 'stable', 'expires_at': '2099-01-01T00:00:00Z'}
        self.publish()
        self.calls = []
        self.current_files = {}

    def tearDown(self):
        self.temp.cleanup()

    def publish(self, bundle=None):
        self.files = {'p2pstream_install.json': json.dumps(self.descriptor, separators=(',', ':')).encode(),
                      BUNDLE: bundle if bundle is not None else archive(self.entries),
                      'setup-server-updater.sh': SCRIPT.encode()}
        self.manifest['release_assets'] = [{'name': name, 'size': len(data), 'sha256': digest(data)} for name, data in sorted(self.files.items())]
        self.raw = json.dumps(self.manifest, separators=(',', ':')).encode()
        self.files['p2pstream_agent_update_manifest.json'] = self.raw
        self.args = ['test/repo@' + VERSION, ID, digest(self.raw), 'arm64', '--']

    def fetch(self, url, maximum):
        self.calls.append((url, maximum))
        if url in self.current_files:
            data = self.current_files[url]
        else:
            self.assertTrue(url.startswith(BASE))
            data = self.files[url[len(BASE):]]
        self.assertLessEqual(len(data), maximum)
        return data

    def invoke(self, args=None):
        with patch.object(bootstrap, 'download', side_effect=self.fetch):
            bootstrap.main(args or self.args)
        return json.loads(self.receipt.read_text())

    def upgraded_args(self, **changes):
        current = dict(self.manifest, version='v1.0.0', commit='a' * 40)
        current.update(changes)
        raw = json.dumps(current).encode()
        self.current_files['https://github.com/test/repo/releases/download/v1.0.0/p2pstream_agent_update_manifest.json'] = raw
        return self.args[:4] + ['--cancel-enrollment', '--upgraded-deployment', 'v1.0.0', digest(raw), '--']

    def shell_controller_expiry(self, scenario):
        # The recovery bundle is real controller source; no success-only host
        # fixture can admit an expired fresh cancellation in this test.
        source = (ROOT / 'server-updater-host.py').read_bytes()
        self.entries = [(name, source if name == 'server-updater-host.py' else data, kind) for name, data, kind in self.entries]
        if scenario == 'recovery-expired':
            self.manifest['expires_at'] = '2030-01-02T00:00:00Z'
        self.publish()
        current = dict(self.manifest, version='v1.0.2', commit='9' * 40, expires_at='2030-01-02T00:00:00Z')
        raw = json.dumps(current).encode()
        self.current_files['https://github.com/test/repo/releases/download/v1.0.2/p2pstream_agent_update_manifest.json'] = raw
        args = self.args[:3] + ['amd64', '--cancel-enrollment', '--upgraded-deployment', 'v1.0.2', digest(raw), '--']
        responses = {BASE + name: base64.b64encode(data).decode() for name, data in self.files.items()}
        responses.update({url: base64.b64encode(data).decode() for url, data in self.current_files.items()})
        runner = self.path / 'controller-runner.py'
        runner.write_text('import importlib.util, sys\nsys.dont_write_bytecode = True\n'
            'spec = importlib.util.spec_from_file_location("wrapper_tests", ' + repr(str(Path(__file__).resolve())) + ')\n'
            'module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)\n'
            'module.cross_layer_wrapper(sys.stdin.read(), sys.argv[3:], ' + repr(responses) + ', ' + repr(str(self.receipt)) + ', ' + repr(scenario) + ')\n')
        python = self.path / 'python3'
        python.write_text('#!/bin/sh\nexec ' + shlex.quote(sys.executable) + ' -I ' + shlex.quote(str(runner)) + ' "$@"\n')
        python.chmod(0o700)
        env = dict(os.environ, PATH=str(self.path) + ':' + os.environ['PATH'])
        result = subprocess.run(['sh', '-s', '--'] + args, input=SCRIPT, text=True, capture_output=True, env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        return json.loads(self.receipt.read_text())

    def test_shell_current_expiry_retry_uses_real_controller_receipt(self):
        result = self.shell_controller_expiry('admitted')
        self.assertEqual(result['statuses'], [1, 0], result)
        self.assertEqual(result['host_calls'], 2)
        self.assertTrue(result['completed'])
        self.assertFalse(result['marker_exists'])

    def test_shell_expired_fresh_cancellation_is_refused_by_controller(self):
        result = self.shell_controller_expiry('fresh-expired')
        self.assertEqual(result['statuses'], [1], result)
        self.assertEqual(result['host_calls'], 1)
        self.assertIn('fresh controller admission refused', result['controller_errors'][0])
        self.assertIsNone(result['completed'])
        self.assertTrue(result['marker_exists'])

    def test_shell_retry_preserves_current_identity_and_pin_checks(self):
        for scenario in ('wrong-installation', 'wrong-arch', 'wrong-current-pin', 'wrong-current-commit'):
            with self.subTest(scenario=scenario):
                result = self.shell_controller_expiry(scenario)
                self.assertEqual(result['statuses'], [1, 1], result)
                self.assertEqual(result['host_calls'], 1 if scenario == 'wrong-current-pin' else 2)
                self.assertFalse(result['completed'])

    def test_shell_expired_recovery_bundle_never_reaches_controller(self):
        result = self.shell_controller_expiry('recovery-expired')
        self.assertEqual(result['statuses'], [1], result)
        self.assertEqual(result['host_calls'], 0)
        self.assertIn('Release metadata expired', result['wrapper_errors'][0])
        self.assertTrue(result['marker_exists'])

    def test_verified_host_receives_exact_bindings_and_compose_arguments(self):
        hostile = "project ' $(touch " + str(self.path / 'injected') + "); *"
        options = ['-p', hostile, '-f', 'relative compose.yaml', '--env-file', 'literal\tfile']
        for migrate in (False, True):
            with self.subTest(migrate=migrate):
                args = self.args[:4] + (['--migrate-deployment'] if migrate else []) + ['--'] + options
                receipt = self.invoke(args)
                command = receipt['args']
                bundle_dir = command[command.index('--bundle') + 1]
                self.assertFalse(Path(bundle_dir).exists(), 'private bundle must be cleaned after host returns')
                expected = ['install', '--bundle', bundle_dir, '--expect-installation', ID, '--expect-version', VERSION,
                            '--expect-commit', 'b' * 40, '--expect-channel', 'stable', '--expect-arch', 'arm64',
                            '--manifest-sha256', digest(self.raw), '--repository', 'test/repo']
                self.assertEqual(command, expected + (['--migrate-deployment'] if migrate else []) + ['--'] + options)
                self.assertEqual(receipt['files'], sorted(bootstrap.NAMES))
                self.assertEqual(receipt['mode'], 0o700)
                self.assertFalse((self.path / 'injected').exists())

    def test_cancel_enrollment_uses_verified_new_release_without_running_version_binding(self):
        receipt = self.invoke(self.args[:4] + ['--cancel-enrollment', '--'])
        command = receipt['args']
        bundle_dir = command[command.index('--bundle') + 1]
        self.assertEqual(command, ['cancel-enrollment', '--expect-installation', ID, '--repository', 'test/repo',
                                   '--expect-channel', 'stable', '--bundle', bundle_dir])
        self.assertFalse(Path(bundle_dir).exists())
        self.assertEqual(receipt['files'], sorted(bootstrap.NAMES))
        self.assertEqual(receipt['mode'], 0o700)
        self.assertEqual([url for url, _ in self.calls], [BASE + name for name in
                         ('p2pstream_agent_update_manifest.json', 'p2pstream_install.json', BUNDLE)])

    def test_cancel_enrollment_refuses_conflicting_modes_and_compose_options_before_download(self):
        for options in [['--cancel-enrollment', '--migrate-deployment', '--'],
                        ['--migrate-deployment', '--cancel-enrollment', '--'],
                        ['--cancel-enrollment', '--cancel-enrollment', '--'],
                        ['--migrate-deployment', '--migrate-deployment', '--'],
                        ['--cancel-enrollment'], ['--cancel-enrollment', '-p', 'project'],
                        ['--cancel-enrollment', '--', '-p', 'project'],
                        ['--cancel-enrollment', '--', '--migrate-deployment']]:
            with self.subTest(options=options):
                with self.assertRaises(ValueError):
                    self.invoke(self.args[:4] + options)
                self.assertEqual(self.calls, [])
                self.assertFalse(self.receipt.exists())

    def test_upgraded_cancellation_binds_current_release_separately_from_recovery_code(self):
        args = self.upgraded_args()
        receipt = self.invoke(args)
        command = receipt['args']
        bundle_dir = command[command.index('--bundle') + 1]
        self.assertEqual(command, ['cancel-enrollment', '--expect-installation', ID, '--repository', 'test/repo',
                                  '--expect-channel', 'stable', '--bundle', bundle_dir, '--upgraded-deployment',
                                  '--expect-version', 'v1.0.0', '--expect-commit', 'a' * 40, '--expect-arch', 'arm64',
                                  '--manifest-sha256', args[-2]])
        self.assertFalse(Path(bundle_dir).exists())
        self.assertEqual([url for url, _ in self.calls], [BASE + 'p2pstream_agent_update_manifest.json',
                         next(iter(self.current_files)), BASE + 'p2pstream_install.json', BASE + BUNDLE])

    def test_upgraded_cancellation_requires_explicit_complete_current_binding_before_download(self):
        valid = ['--cancel-enrollment', '--upgraded-deployment', 'v1.0.0', 'a' * 64, '--']
        cases = [valid[1:], ['--migrate-deployment'] + valid[1:], valid[:2] + ['--'],
                 valid[:3] + ['--'], valid[:-1] + ['--upgraded-deployment', 'v1.0.0', 'a' * 64, '--'],
                 valid + ['-p', 'project'], valid[:2] + ['v1.0.0-staging.1'] + valid[3:],
                 valid[:2] + ['v1.0.0/../../latest'] + valid[3:], valid[:3] + ['bad-hash', '--'],
                 valid[:-1] + ['--migrate-deployment', '--']]
        for options in cases:
            with self.subTest(options=options):
                with self.assertRaises(ValueError):
                    self.invoke(self.args[:4] + options)
                self.assertEqual(self.calls, [])
                self.assertFalse(self.receipt.exists())

    def test_upgraded_cancellation_rejects_current_manifest_discrepancies_before_extraction(self):
        for changes in ({'version': 'v2.0.0'}, {'channel': 'staging'}, {'commit': 'bad'},
                        {'schema_version': 2}, {'expires_at': 'invalid-timestamp'}):
            with self.subTest(changes=changes):
                args = self.upgraded_args(**changes)
                with patch.object(bootstrap.tempfile, 'TemporaryDirectory', side_effect=AssertionError('premature extraction')):
                    with self.assertRaises(ValueError):
                        self.invoke(args)
                self.assertFalse(self.receipt.exists())
        args = self.upgraded_args()
        self.current_files[next(iter(self.current_files))] = b'not JSON'
        with self.assertRaisesRegex(ValueError, 'Release changed'):
            self.invoke(args)
        self.assertFalse(self.receipt.exists())

    def test_cancel_enrollment_keeps_manifest_descriptor_and_bundle_verification(self):
        for name in ('p2pstream_agent_update_manifest.json', 'p2pstream_install.json', BUNDLE):
            with self.subTest(asset=name):
                original = self.files[name]
                self.files[name] = original + b'x'
                with self.assertRaisesRegex(ValueError, 'Release changed|Installation asset verification failed'):
                    self.invoke(self.args[:4] + ['--cancel-enrollment', '--'])
                self.assertFalse(self.receipt.exists())
                self.files[name] = original

    def test_cancel_enrollment_propagates_verified_host_refusal_and_cleans_bundle(self):
        self.entries = [(name, data + b'\nraise SystemExit(23)\n' if name == 'server-updater-host.py' else data, kind)
                        for name, data, kind in self.entries]
        self.publish()
        with self.assertRaises(subprocess.CalledProcessError) as caught:
            self.invoke(self.args[:4] + ['--cancel-enrollment', '--'])
        self.assertEqual(caught.exception.returncode, 23)
        command = json.loads(self.receipt.read_text())['args']
        self.assertEqual(command[0], 'cancel-enrollment')
        self.assertFalse(Path(command[command.index('--bundle') + 1]).exists())

    def test_manifest_pin_precedes_parsing_downloads_and_extraction(self):
        self.files['p2pstream_agent_update_manifest.json'] = b'not even JSON'
        with patch.object(bootstrap.tempfile, 'TemporaryDirectory', side_effect=AssertionError('premature extraction')):
            with self.assertRaisesRegex(ValueError, 'Release changed'):
                self.invoke()
        self.assertEqual(self.calls, [(BASE + 'p2pstream_agent_update_manifest.json', 65536)])
        self.assertFalse(self.receipt.exists())

    def test_descriptor_and_bundle_checksums_and_sizes_precede_host(self):
        for name in ('p2pstream_install.json', BUNDLE):
            original = self.files[name]
            for changed in (original + b'x', bytes([original[0] ^ 1]) + original[1:]):
                with self.subTest(name=name, size=len(changed)):
                    self.files[name] = changed
                    with self.assertRaisesRegex(ValueError, 'Installation asset verification failed'):
                        self.invoke()
                    self.assertFalse(self.receipt.exists())
            self.files[name] = original

    def test_descriptor_identity_and_image_binding(self):
        for field, value in [('commit', 'c' * 40), ('channel', 'staging'), ('version', 'v2.0.0'), ('bundle', '../bundle'), ('updater_images', {'linux/amd64': 'untrusted@sha256:' + 'a' * 64, 'linux/arm64': 'x'})]:
            with self.subTest(field=field):
                original = self.descriptor[field]
                self.descriptor[field] = value
                self.publish()
                with self.assertRaisesRegex(ValueError, 'descriptor identity|updater image binding'):
                    self.invoke()
                self.assertFalse(self.receipt.exists())
                self.descriptor[field] = original

    def test_expired_recovery_manifest_refuses_all_ordinary_modes(self):
        self.manifest['expires_at'] = '2000-01-01T00:00:00Z'
        self.publish()
        for mode in ([], ['--migrate-deployment'], ['--cancel-enrollment']):
            with self.subTest(mode=mode):
                with self.assertRaisesRegex(ValueError, 'Release metadata expired'):
                    self.invoke(self.args[:4] + mode + ['--'])
                self.assertFalse(self.receipt.exists())

    def test_manifest_identity_expiry_asset_duplicates_and_bounds(self):
        for field, value in [('commit', 'bad'), ('channel', 'staging'), ('version', 'v2.0.0'), ('expires_at', '2000-01-01T00:00:00Z')]:
            with self.subTest(field=field):
                original = self.manifest[field]
                self.manifest[field] = value
                self.publish()
                with self.assertRaises(ValueError):
                    self.invoke()
                self.manifest[field] = original
        self.publish()
        for mutation in ('duplicate', 'missing-bootstrap', 'oversized-bundle'):
            with self.subTest(mutation=mutation):
                original = copy.deepcopy(self.manifest['release_assets'])
                if mutation == 'duplicate':
                    self.manifest['release_assets'].append(self.manifest['release_assets'][0])
                elif mutation == 'missing-bootstrap':
                    self.manifest['release_assets'] = [a for a in original if a['name'] != 'setup-server-updater.sh']
                else:
                    next(a for a in self.manifest['release_assets'] if a['name'] == BUNDLE)['size'] = 1048577
                self.raw = json.dumps(self.manifest).encode()
                self.files['p2pstream_agent_update_manifest.json'] = self.raw
                self.args[2] = digest(self.raw)
                with self.assertRaises(ValueError):
                    self.invoke()
                self.assertFalse(self.receipt.exists())
                self.manifest['release_assets'] = original

    def test_strict_archive_inventory_types_paths_and_expansion_bounds(self):
        bad_entries = [self.entries[:-1], self.entries + [self.entries[0]],
                       [(name if i else '../compose.yaml', data, kind) for i, (name, data, kind) in enumerate(self.entries)],
                       [(name if i else '/compose.yaml', data, kind) for i, (name, data, kind) in enumerate(self.entries)],
                       [(name, data if i else b'x' * 262145, kind) for i, (name, data, kind) in enumerate(self.entries)]]
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.DIRTYPE, tarfile.FIFOTYPE, tarfile.CHRTYPE, tarfile.CONTTYPE):
            bad_entries.append([(name, data if i else b'', existing if i else kind) for i, (name, data, existing) in enumerate(self.entries)])
        for entries in bad_entries:
            with self.subTest(inventory=[(n, len(d), k) for n, d, k in entries]):
                self.publish(archive(entries))
                with self.assertRaisesRegex(ValueError, 'Unsafe Docker bundle'):
                    self.invoke()
                self.assertFalse(self.receipt.exists())
        self.publish(gzip.compress(b'\0' * 2097153))
        with self.assertRaisesRegex(ValueError, 'expansion exceeds limit'):
            self.invoke()
        self.assertFalse(self.receipt.exists())

    def test_option_boundary_and_invalid_public_arguments(self):
        for args in [self.args[:4], self.args[:4] + ['-p', 'context'], self.args[:4] + ['--migrate-deployment', '-p', 'context'],
                     ['test/repo@v1.0.1;touch /tmp/no'] + self.args[1:], ['../repo@v1.0.1'] + self.args[1:],
                     self.args[:1] + ['bad-identity'] + self.args[2:], self.args[:2] + ['bad-hash'] + self.args[3:],
                     self.args[:3] + ['386', '--']]:
            with self.subTest(args=args):
                with self.assertRaises(ValueError):
                    self.invoke(args)
                self.assertEqual(self.calls, [], 'malformed command performed network access')
                self.assertFalse(self.receipt.exists())

    def test_https_redirects_and_response_size(self):
        for url in ['http://github.com/file', 'https://evil.example/file', 'https://github.com:443/file', 'https://user@github.com/file']:
            with self.subTest(url=url):
                with self.assertRaises(ValueError):
                    bootstrap.Redirect().redirect_request(urllib.request.Request('https://github.com/file'), None, 302, '', {}, url)
                with self.assertRaises(ValueError):
                    bootstrap.download(url, 5)
        for host in bootstrap.HOSTS:
            url = 'https://' + host + '/file'
            request = bootstrap.Redirect().redirect_request(urllib.request.Request('https://github.com/file'), None, 302, '', {}, url)
            self.assertEqual(request.full_url, url)
        response = io.BytesIO(b'123456')
        response.geturl = lambda: 'https://github.com/file'
        opener = types.SimpleNamespace(open=lambda *args, **kwargs: response)
        with patch.object(bootstrap.urllib.request, 'build_opener', return_value=opener):
            with self.assertRaisesRegex(ValueError, 'exceeds limit'):
                bootstrap.download('https://github.com/file', 5)

    def test_real_shell_wrapper_runs_verified_fixture_and_never_partial_source(self):
        runner = self.path / 'runner.py'
        upgraded_args = self.upgraded_args()
        # Mock just the HTTPS transport; execute the unmodified embedded program
        # through sh -> python3 -I -> real verified host fixture subprocess.
        responses = {BASE + name: base64.b64encode(data).decode() for name, data in self.files.items()}
        responses.update({url: base64.b64encode(data).decode() for url, data in self.current_files.items()})
        runner.write_text('import base64, io, sys, urllib.request\n'
                          'responses = ' + repr(responses) + '\n'
                          'class Opener:\n'
                          '    def open(self, url, **kwargs):\n'
                          '        result = io.BytesIO(base64.b64decode(responses[url]))\n'
                          '        result.geturl = lambda: url\n'
                          '        return result\n'
                          'urllib.request.build_opener = lambda *args: Opener()\n'
                          'sys.argv = sys.argv[2:]\n'
                          'exec(compile(sys.stdin.read(), "downloaded-setup", "exec"))\n')
        python = self.path / 'python3'
        python.write_text('#!/bin/sh\nexec ' + shlex.quote(sys.executable) + ' -I ' + shlex.quote(str(runner)) + ' "$@"\n')
        python.chmod(0o700)
        env = dict(os.environ, PATH=str(self.path) + ':' + os.environ['PATH'])
        for mode in ('install', '--migrate-deployment', '--cancel-enrollment', '--upgraded-deployment'):
            cancel = mode in {'--cancel-enrollment', '--upgraded-deployment'}
            options = [] if cancel else ['-p', "project ' $(false); *"]
            args = upgraded_args if mode == '--upgraded-deployment' else self.args[:4] + ([] if mode == 'install' else [mode]) + ['--'] + options
            result = subprocess.run(['sh', '-s', '--'] + args, input=SCRIPT, text=True, capture_output=True, env=env)
            self.assertEqual(result.returncode, 0, result.stderr)
            receipt = json.loads(self.receipt.read_text())
            if cancel:
                self.assertEqual(receipt['args'][0], 'cancel-enrollment')
                self.assertEqual('--expect-version' in receipt['args'], mode == '--upgraded-deployment')
                if mode == '--upgraded-deployment':
                    self.assertIn('v1.0.0', receipt['args'])
                    self.assertNotIn(VERSION, receipt['args'])
                self.assertNotIn('--', receipt['args'])
            else:
                self.assertEqual(receipt['args'][-3:], ['--'] + options)
                self.assertEqual('--migrate-deployment' in receipt['args'], mode == '--migrate-deployment')
            self.receipt.unlink()
        for partial in ['', SCRIPT[:SCRIPT.index('\nPY\n}')], SCRIPT[:SCRIPT.rindex('\nmain "$@"')]]:
            result = subprocess.run(['sh', '-s', '--'] + self.args, input=partial, text=True, capture_output=True, env=env)
            self.assertFalse(self.receipt.exists(), 'partial script invoked the verified host')
            if partial and not partial.endswith('}'):
                self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()

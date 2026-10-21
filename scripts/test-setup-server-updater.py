#!/usr/bin/env python3
"""Exercise the standalone setup shell and its pinned release download boundary."""
import base64
import copy
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
        self.assertTrue(url.startswith(BASE))
        data = self.files[url[len(BASE):]]
        self.assertLessEqual(len(data), maximum)
        return data

    def invoke(self, args=None):
        with patch.object(bootstrap, 'download', side_effect=self.fetch):
            bootstrap.main(args or self.args)
        return json.loads(self.receipt.read_text())

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
        # Mock just the HTTPS transport; execute the unmodified embedded program
        # through sh -> python3 -I -> real verified host fixture subprocess.
        responses = {BASE + name: base64.b64encode(data).decode() for name, data in self.files.items()}
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
        for migrate in (False, True):
            args = self.args[:4] + (['--migrate-deployment'] if migrate else []) + ['--', '-p', "project ' $(false); *"]
            result = subprocess.run(['sh', '-s', '--'] + args, input=SCRIPT, text=True, capture_output=True, env=env)
            self.assertEqual(result.returncode, 0, result.stderr)
            receipt = json.loads(self.receipt.read_text())
            self.assertEqual(receipt['args'][-3:], ['--', '-p', "project ' $(false); *"])
            self.assertEqual('--migrate-deployment' in receipt['args'], migrate)
            self.receipt.unlink()
        for partial in ['', SCRIPT[:SCRIPT.index('\nPY\n}')], SCRIPT[:SCRIPT.rindex('\nmain "$@"')]]:
            result = subprocess.run(['sh', '-s', '--'] + self.args, input=partial, text=True, capture_output=True, env=env)
            self.assertFalse(self.receipt.exists(), 'partial script invoked the verified host')
            if partial and not partial.endswith('}'):
                self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()

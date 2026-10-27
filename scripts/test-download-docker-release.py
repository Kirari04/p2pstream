#!/usr/bin/env python3
"""Offline release fixtures exercise download integrity and deployment ownership."""
import copy
import gzip
import hashlib
import importlib.util
import io
import json
import os
import shutil
import subprocess
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('download_release', Path(__file__).with_name('download-docker-release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
VERSION = 'v0.1.53-staging.108'
REPOSITORY = 'Kirari04/p2pstream'
BASE = 'https://github.com/' + REPOSITORY + '/releases/download/' + VERSION + '/'
INDEX = 'p2pstream_' + VERSION + '_image_index.json'
BUNDLE = 'p2pstream_' + VERSION + '_docker.tar.gz'
MANIFEST = 'p2pstream_agent_update_manifest.json'
ROOT = Path(__file__).resolve().parent.parent


def archive(entries):
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode='w', format=tarfile.USTAR_FORMAT) as tar:
        for name, value in entries:
            info = tarfile.TarInfo(name)
            if isinstance(value, bytes):
                info.size = len(value)
                tar.addfile(info, io.BytesIO(value))
            else:
                info.type = tarfile.SYMTYPE
                info.linkname = value
                tar.addfile(info)
    return gzip.compress(raw.getvalue())


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.destination = Path(self.directory.name) / 'deployment'
        self.entries = [('compose.yaml', (ROOT / 'compose.yaml').read_bytes().replace(
            b'image: ghcr.io/kirari04/p2pstream:latest',
            b'image: "${P2PSTREAM_IMAGE:?Run verified release deployment preparation first}"')),
            ('.env.example', (ROOT / '.env.example').read_bytes())]
        platforms = [{'os': 'linux', 'arch': arch, 'digest': 'sha256:' + digit * 64,
                      'media_type': 'application/vnd.oci.image.manifest.v1+json', 'size': 123}
                     for arch, digit in [('amd64', 'a'), ('arm64', 'b')]]
        index = {'schemaVersion': 2, 'mediaType': 'application/vnd.oci.image.index.v1+json',
                 'manifests': [{'platform': {'os': p['os'], 'architecture': p['arch']},
                                'digest': p['digest'], 'mediaType': p['media_type'], 'size': p['size']}
                               for p in platforms]}
        raw_index = json.dumps(index).encode()
        self.assets = {INDEX: raw_index, BUNDLE: archive(self.entries)}
        self.manifest = {'schema_version': 1, 'version': VERSION, 'channel': 'staging', 'commit': 'c' * 40,
            'oci_images': [{'repository': 'ghcr.io/kirari04/p2pstream',
                            'digest': 'sha256:' + hashlib.sha256(raw_index).hexdigest(),
                            'media_type': index['mediaType'], 'size': len(raw_index), 'platforms': platforms}]}
        self.refresh()
        self.mock = patch.object(release, 'download', side_effect=self.download)
        self.mock.start()
        self.addCleanup(self.mock.stop)

    def refresh(self):
        self.manifest['release_assets'] = [{'name': name, 'size': len(raw), 'sha256': hashlib.sha256(raw).hexdigest()}
                                          for name, raw in self.assets.items() if name != MANIFEST]
        self.assets[MANIFEST] = json.dumps(self.manifest).encode()

    def download(self, url, maximum):
        self.assertTrue(url.startswith(BASE))
        raw = self.assets[url[len(BASE):]]
        self.assertLessEqual(len(raw), maximum)
        return raw

    def prepare(self):
        return release.prepare(REPOSITORY, VERSION, self.destination)

    def test_prepares_editable_compose_and_pinned_env_without_docker(self):
        self.prepare()
        self.assertEqual(set(p.name for p in self.destination.iterdir()), {'compose.yaml', '.env', 'release.json'})
        compose = (self.destination / 'compose.yaml').read_text()
        env = (self.destination / '.env').read_text()
        self.assertIn('${MANAGEMENT_UI_DISABLED:-false}', compose)
        self.assertIn('${P2PSTREAM_IMAGE:', compose)
        self.assertIn('P2PSTREAM_IMAGE=ghcr.io/kirari04/p2pstream@sha256:', env)
        self.assertNotIn('server-updater', compose)
        self.assertEqual((self.destination / '.env').stat().st_mode & 0o777, 0o600)

    @unittest.skipUnless(shutil.which('docker'), 'Docker Compose is not installed')
    def test_plain_compose_reads_changed_env_and_keeps_digest(self):
        self.prepare()
        env = self.destination / '.env'
        with env.open('a') as output:
            output.write('\nMANAGEMENT_UI_DISABLED=true\n')
        result = subprocess.run(['docker', 'compose', '--project-name', 'download-test',
                                 '--env-file', str(env), '-f', str(self.destination / 'compose.yaml'),
                                 'config', '--format', 'json'],
                                capture_output=True, text=True, check=True,
                                env={k: v for k, v in os.environ.items()
                                     if not k.startswith(('COMPOSE_', 'MANAGEMENT_', 'P2PSTREAM_'))})
        model = json.loads(result.stdout)
        server = model['services']['p2pstream']
        self.assertEqual(server['environment']['MANAGEMENT_UI_DISABLED'], 'true')
        self.assertEqual(server['image'], json.loads((self.destination / 'release.json').read_text())['image'])
        self.assertEqual(set(model['services']), {'p2pstream'})

    def test_existing_directory_is_untouched(self):
        self.destination.mkdir()
        marker = self.destination / '.env'
        marker.write_text('operator settings')
        with self.assertRaisesRegex(ValueError, 'already exists'):
            self.prepare()
        self.assertEqual(marker.read_text(), 'operator settings')

    def test_symlink_destination_is_refused(self):
        self.destination.symlink_to(self.destination.parent / 'missing')
        with self.assertRaisesRegex(ValueError, 'already exists'):
            self.prepare()

    def test_corrupt_bundle_has_no_filesystem_effects(self):
        self.assets[BUNDLE] += b'corrupted'
        with self.assertRaisesRegex(ValueError, 'verification failed'):
            self.prepare()
        self.assertFalse(self.destination.exists())

    def test_untrusted_bundle_members_are_refused(self):
        for extra in [('../outside', b'x'), ('compose.yaml', b'duplicate'), ('script.sh', b'echo unsafe')]:
            with self.subTest(extra=extra[0]):
                self.assets[BUNDLE] = archive(self.entries + [extra])
                self.refresh()
                with self.assertRaisesRegex(ValueError, 'standalone Docker bundle'):
                    self.prepare()
                self.assertFalse(self.destination.exists())
        self.assets[BUNDLE] = archive([('compose.yaml', '/etc/passwd'), self.entries[1]])
        self.refresh()
        with self.assertRaisesRegex(ValueError, 'standalone Docker bundle'):
            self.prepare()

    def test_expansion_bomb_refused(self):
        self.assets[BUNDLE] = gzip.compress(b'0' * 1048577)
        self.refresh()
        with self.assertRaisesRegex(ValueError, 'expansion exceeds'):
            self.prepare()

    def test_manifest_binding_mismatches_refused(self):
        original = copy.deepcopy(self.manifest)
        for key, value in [('version', 'v0.1.52'), ('channel', 'stable'), ('commit', 'invalid')]:
            self.manifest = copy.deepcopy(original)
            self.manifest[key] = value
            self.refresh()
            with self.assertRaisesRegex(ValueError, 'identity mismatch'):
                self.prepare()
        self.manifest = copy.deepcopy(original)
        self.manifest['oci_images'][0]['repository'] = 'ghcr.io/other/image'
        self.refresh()
        with self.assertRaisesRegex(ValueError, 'repository or digest'):
            self.prepare()

    def test_index_must_match_image_digest_and_platforms(self):
        self.manifest['oci_images'][0]['digest'] = 'sha256:' + 'e' * 64
        self.refresh()
        with self.assertRaisesRegex(ValueError, 'does not match'):
            self.prepare()

    def test_duplicate_json_and_untrusted_urls_refused(self):
        with self.assertRaisesRegex(ValueError, 'Duplicate JSON'):
            release.decode(b'{"version": 1, "version": 2}')
        for url in ['http://github.com/x', 'https://evil.test/x', 'https://user@github.com/x', 'https://github.com:444/x']:
            with self.assertRaises(ValueError):
                release.trusted_url(url)


if __name__ == '__main__':
    unittest.main()

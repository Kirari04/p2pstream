#!/usr/bin/env python3
"""Download a small verified deployment package; prepare, never start, Docker."""
import argparse
import hashlib
import io
import json
import re
import subprocess
import tarfile
import tempfile
import urllib.parse
import urllib.request
from pathlib import Path

NAMES = {'server-updater-host.py', 'install-server-updater.sh', 'server-updater-compose.sh', 'compose.yaml', '.env.example'}

class Redirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        parsed = urllib.parse.urlparse(newurl)
        if parsed.scheme != 'https' or parsed.hostname not in {'github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com'} or parsed.port or parsed.username:
            raise ValueError('Untrusted release redirect')
        return super().redirect_request(req, fp, code, msg, headers, newurl)

def download(url, maximum):
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), Redirect())
    with opener.open(url, timeout=30) as response:
        raw = response.read(maximum + 1)
    if len(raw) > maximum:
        raise ValueError('Release download exceeds limit')
    return raw

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', required=True)
    parser.add_argument('--repository', default='Kirari04/p2pstream')
    parser.add_argument('--directory', default='p2pstream')
    args = parser.parse_args()
    if not re.fullmatch(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-staging\.[1-9][0-9]*)?', args.release) or not re.fullmatch(r'[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*', args.repository):
        raise ValueError('An exact stable/staging release and GitHub repository are required')
    base = 'https://github.com/' + args.repository + '/releases/download/' + args.release + '/'
    raw = download(base + 'p2pstream_agent_update_manifest.json', 65536)
    manifest = json.loads(raw)
    if manifest['version'] != args.release:
        raise ValueError('Release manifest identity mismatch')
    assets = [a for a in manifest['release_assets'] if a['name'] == 'p2pstream_' + args.release + '_docker.tar.gz']
    if len(assets) != 1:
        raise ValueError('This release has no Docker bundle; select a newer published release with installation support')
    asset = assets[0]
    raw = download(base + asset['name'], 1048576)
    if len(raw) != asset['size'] or hashlib.sha256(raw).hexdigest() != asset['sha256']:
        raise ValueError('Docker bundle verification failed')
    with tempfile.TemporaryDirectory(prefix='p2pstream-release-') as directory:
        with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
            members = archive.getmembers()
            if len(members) != len(NAMES) or {m.name for m in members} != NAMES or not all(m.isfile() and m.size <= 262144 for m in members):
                raise ValueError('Unsafe Docker bundle contents')
            archive.extractall(directory, members=members)
        subprocess.run(['python3', '-I', str(Path(directory) / 'server-updater-host.py'), 'deploy', '--release', args.release, '--repository', args.repository, '--directory', str(Path(args.directory).resolve())], check=True)

if __name__ == '__main__':
    main()

#!/usr/bin/env python3
"""Prepare an operator-owned Compose deployment pinned to an exact release."""
import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import sys
import tarfile
import urllib.parse
import urllib.request

NAMES = {'compose.yaml', '.env.example'}
HOSTS = {'github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com'}
VERSION = r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-staging\.[1-9][0-9]*)?'


def require(condition, message):
    if not condition:
        raise ValueError(message)


def trusted_url(url):
    parsed = urllib.parse.urlparse(url)
    require(parsed.scheme == 'https' and parsed.hostname in HOSTS and
            parsed.port is None and parsed.username is None and parsed.password is None,
            'Untrusted release URL')


class Redirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        trusted_url(newurl)
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def download(url, maximum):
    trusted_url(url)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), Redirect())
    with opener.open(url, timeout=30) as response:
        trusted_url(response.geturl())
        raw = response.read(maximum + 1)
    require(len(raw) <= maximum, 'Release download exceeds limit')
    return raw


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'Duplicate JSON key: ' + key)
        result[key] = value
    return result


def decode(raw):
    return json.loads(raw, object_pairs_hook=unique_object)


def release_files(repository, version):
    require(re.fullmatch(VERSION, version), 'An exact stable or staging release is required')
    require(re.fullmatch(r'[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*', repository),
            'Invalid GitHub repository')
    base = 'https://github.com/' + repository + '/releases/download/' + version + '/'
    manifest_raw = download(base + 'p2pstream_agent_update_manifest.json', 65536)
    manifest = decode(manifest_raw)
    channel = 'staging' if '-staging.' in version else 'stable'
    require(manifest['schema_version'] == 1 and manifest['version'] == version and
            manifest['channel'] == channel and re.fullmatch(r'[0-9a-f]{40}', manifest['commit']),
            'Release manifest identity mismatch')
    # This is an explicit manual installation, not selection from the agent's
    # automatic update catalog. It may intentionally select an older release.
    assets = manifest['release_assets']
    require(isinstance(assets, list) and 0 < len(assets) <= 64, 'Invalid release assets')
    inventory = {}
    for asset in assets:
        require(set(asset) == {'name', 'size', 'sha256'} and
                re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,127}', asset['name']) and
                type(asset['size']) is int and asset['size'] > 0 and
                re.fullmatch(r'[0-9a-f]{64}', asset['sha256']) and asset['name'] not in inventory,
                'Invalid or duplicate release asset')
        inventory[asset['name']] = asset

    def verified(name, maximum):
        require(name in inventory and inventory[name]['size'] <= maximum,
                'Missing or oversized release asset: ' + name)
        raw = download(base + name, maximum)
        require(len(raw) == inventory[name]['size'] and
                hashlib.sha256(raw).hexdigest() == inventory[name]['sha256'],
                'Release asset verification failed: ' + name)
        return raw

    images = manifest['oci_images']
    require(isinstance(images, list) and len(images) == 1, 'Expected one release image')
    image = images[0]
    require(image['repository'] == 'ghcr.io/' + repository.lower() and
            re.fullmatch(r'sha256:[0-9a-f]{64}', image['digest']), 'Invalid image repository or digest')
    index_raw = verified('p2pstream_' + version + '_image_index.json', 65536)
    require(type(image['size']) is int and len(index_raw) == image['size'] and
            'sha256:' + hashlib.sha256(index_raw).hexdigest() == image['digest'],
            'Image index does not match the release image')
    index = decode(index_raw)
    require(index['schemaVersion'] == 2 and index['mediaType'] == image['media_type'] and
            image['media_type'] in {'application/vnd.oci.image.index.v1+json',
                                    'application/vnd.docker.distribution.manifest.list.v2+json'},
            'Unsupported image index')
    actual = [{'os': m['platform']['os'], 'arch': m['platform']['architecture'],
               'digest': m['digest'], 'media_type': m['mediaType'], 'size': m['size']}
              for m in index['manifests']]
    require(len(actual) == 2 and {(p['os'], p['arch']) for p in actual} ==
            {('linux', 'amd64'), ('linux', 'arm64')} and
            sorted(actual, key=lambda p: p['arch']) == sorted(image['platforms'], key=lambda p: p['arch']),
            'Image platforms do not match the release manifest')

    compressed = verified('p2pstream_' + version + '_docker.tar.gz', 1048576)
    with gzip.GzipFile(fileobj=io.BytesIO(compressed)) as stream:
        unpacked = stream.read(1048577)
    require(len(unpacked) <= 1048576, 'Docker bundle expansion exceeds limit')
    with tarfile.open(fileobj=io.BytesIO(unpacked), mode='r:') as archive:
        members = archive.getmembers()
        require(len(members) == len(NAMES) and {m.name for m in members} == NAMES and
                not archive.pax_headers and all(m.type == tarfile.REGTYPE and
                0 < m.size <= 262144 and not m.pax_headers for m in members),
                'Expected a standalone Docker bundle containing only compose.yaml and .env.example')
        files = {m.name: archive.extractfile(m).read() for m in members}
    image_ref = image['repository'] + '@' + image['digest']
    files['.env'] = files.pop('.env.example') + b'\nP2PSTREAM_IMAGE=' + image_ref.encode() + b'\n'
    files['release.json'] = (json.dumps({'repository': repository, 'version': version,
        'commit': manifest['commit'], 'channel': channel, 'image': image_ref,
        'manifest_sha256': hashlib.sha256(manifest_raw).hexdigest()}, indent=2) + '\n').encode()
    return files


def prepare(repository, version, destination):
    destination = Path(destination).absolute()
    require(not os.path.lexists(destination), 'Destination already exists; choose a new directory. Existing deployments are never overwritten.')
    files = release_files(repository, version)
    # No Docker commands or downloaded scripts execute. All downloads and checks
    # complete before any deployment file is written.
    destination.mkdir(mode=0o750, parents=True)
    created = []
    try:
        for name, data in files.items():
            path = destination / name
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            created.append(path)
            with os.fdopen(fd, 'wb') as output:
                output.write(data)
    except BaseException:
        for path in created:
            path.unlink()
        destination.rmdir()
        raise
    return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--release', required=True)
    parser.add_argument('--repository', default='Kirari04/p2pstream')
    parser.add_argument('--directory', default='p2pstream', help='new deployment directory')
    args = parser.parse_args()
    directory = prepare(args.repository, args.release, args.directory)
    print('Prepared ' + str(directory) + ' with release ' + args.release + ' pinned by digest.')
    print('Edit .env, then run docker compose up -d from that directory.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, TypeError, OSError, tarfile.TarError) as error:
        sys.exit('p2pstream: ' + str(error))

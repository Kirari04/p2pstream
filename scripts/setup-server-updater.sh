#!/bin/sh
# Installation uses the exact release command from the selected management server.
# Cancellation explicitly selects a compatible published recovery release.
# HTTPS and the operator's explicit execution trust this bootstrap. The manifest
# pin below binds subsequent inputs, not the script that is already executing.
# Keep every effect inside main, invoked last: a truncated download must not
# execute a partial installer before the shell has parsed this whole function.
main() {
    set -eu
    python3 -I - "$@" <<'PY'
import datetime
import gzip
import hashlib
import io
import json
import re
import subprocess
import sys
import tarfile
import tempfile
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

NAMES = {'server-updater-host.py', 'install-server-updater.sh', 'server-updater-compose.sh', 'compose.yaml', '.env.example'}
HOSTS = {'github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com'}
HEX = re.compile(r'[0-9a-f]{64}')
VERSION = re.compile(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-staging\.[1-9][0-9]*)?')


def require(condition, message):
    if not condition:
        raise ValueError(message)


def trusted_url(url):
    parsed = urllib.parse.urlparse(url)
    require(parsed.scheme == 'https' and parsed.hostname in HOSTS and parsed.port is None and parsed.username is None and parsed.password is None, 'Untrusted release URL')


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


def release_manifest(repository, version, manifest_hash, *, allow_expired_identity=False):
    base = 'https://github.com/' + repository + '/releases/download/' + version + '/'
    raw = download(base + 'p2pstream_agent_update_manifest.json', 65536)
    require(hashlib.sha256(raw).hexdigest() == manifest_hash, 'Release changed since this command was copied; refresh the selected server')
    manifest = json.loads(raw)
    channel = 'staging' if '-staging.' in version else 'stable'
    require(manifest['schema_version'] == 1 and manifest['version'] == version and manifest['channel'] == channel and re.fullmatch(r'[0-9a-f]{40}', manifest['commit']), 'Manifest identity mismatch')
    expiry = datetime.datetime.strptime(manifest['expires_at'], '%Y-%m-%dT%H:%M:%SZ').replace(tzinfo=datetime.timezone.utc)
    require(allow_expired_identity or expiry > datetime.datetime.now(datetime.timezone.utc), 'Release metadata expired; manually upgrade to a current published release')
    return base, manifest, channel


def main(args):
    require(len(args) >= 5, 'Expected repository@release, installation, manifest SHA-256, architecture, and --')
    binding, installation, manifest_hash, arch = args[:4]
    repository, separator, version = binding.partition('@')
    require(separator and re.fullmatch(r'[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*', repository), 'Invalid GitHub repository')
    require(VERSION.fullmatch(version), 'An exact release version is required')
    require(str(uuid.UUID(installation)) == installation and HEX.fullmatch(manifest_hash) and arch in {'amd64', 'arm64'}, 'Invalid installation binding')
    remaining = args[4:]
    migrate = cancel = False
    while remaining and remaining[0] in {'--migrate-deployment', '--cancel-enrollment'}:
        require(not migrate and not cancel, 'Choose only one of --migrate-deployment and --cancel-enrollment')
        migrate = remaining[0] == '--migrate-deployment'
        cancel = remaining[0] == '--cancel-enrollment'
        remaining = remaining[1:]
    current_version = current_hash = None
    if remaining and remaining[0] == '--upgraded-deployment':
        require(cancel and len(remaining) >= 4, '--upgraded-deployment requires --cancel-enrollment, current version, and current manifest SHA-256')
        current_version, current_hash = remaining[1:3]
        require(VERSION.fullmatch(current_version) and HEX.fullmatch(current_hash), 'Invalid current release binding')
        require(('-staging.' in current_version) == ('-staging.' in version), 'Current and recovery releases must use the same channel')
        remaining = remaining[3:]
    require(remaining and remaining[0] == '--', 'Expected -- before Compose options')
    options = remaining[1:]
    require(not cancel or not options, 'Enrollment cancellation does not accept Compose options')
    base, manifest, channel = release_manifest(repository, version, manifest_hash)
    current_manifest = None
    if current_version is not None:
        # Only explicit upgraded cancellation may pass an expired, pinned
        # current identity to the freshly verified recovery controller. Its
        # exact API2 receipt decides whether this is an admitted retry; without
        # that proof the controller still performs fresh expiry validation.
        _, current_manifest, _ = release_manifest(repository, current_version, current_hash, allow_expired_identity=True)
    assets = manifest['release_assets']
    require(isinstance(assets, list) and len(assets) <= 64, 'Invalid release assets')
    inventory = {}
    for asset in assets:
        require(set(asset) == {'name', 'size', 'sha256'} and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,127}', asset['name']) and type(asset['size']) is int and asset['size'] > 0 and HEX.fullmatch(asset['sha256']) and asset['name'] not in inventory, 'Invalid or duplicate release asset')
        inventory[asset['name']] = asset
    require('setup-server-updater.sh' in inventory and inventory['setup-server-updater.sh']['size'] <= 65536, 'Release lacks the standalone setup bootstrap')

    def verified(name, maximum):
        require(name in inventory and inventory[name]['size'] <= maximum, 'Missing or oversized installation asset')
        data = download(base + name, maximum)
        require(len(data) == inventory[name]['size'] and hashlib.sha256(data).hexdigest() == inventory[name]['sha256'], 'Installation asset verification failed: ' + name)
        return data

    descriptor = json.loads(verified('p2pstream_install.json', 16384))
    bundle = 'p2pstream_' + version + '_docker.tar.gz'
    require(set(descriptor) == {'api', 'version', 'commit', 'channel', 'bundle', 'updater_images'} and descriptor['api'] == 1 and (descriptor['version'], descriptor['commit'], descriptor['channel'], descriptor['bundle']) == (version, manifest['commit'], channel, bundle), 'Installation descriptor identity mismatch')
    require(set(descriptor['updater_images']) == {'linux/amd64', 'linux/arm64'} and all(re.fullmatch(re.escape('ghcr.io/' + repository.lower() + '-updater@sha256:') + '[0-9a-f]{64}', ref) for ref in descriptor['updater_images'].values()), 'Invalid updater image binding')
    compressed = verified(bundle, 1048576)
    # Bound expansion before tarfile processes headers (including PAX/long names).
    with gzip.GzipFile(fileobj=io.BytesIO(compressed)) as stream:
        unpacked = stream.read(2097153)
    require(len(unpacked) <= 2097152, 'Docker bundle expansion exceeds limit')
    with tempfile.TemporaryDirectory(prefix='p2pstream-setup-') as directory:
        with tarfile.open(fileobj=io.BytesIO(unpacked), mode='r:') as archive:
            members = archive.getmembers()
            require(len(members) == len(NAMES) and {m.name for m in members} == NAMES and not archive.pax_headers and all(m.type == tarfile.REGTYPE and 0 <= m.size <= 262144 and not m.pax_headers for m in members), 'Unsafe Docker bundle contents')
            for member in members:
                (Path(directory) / member.name).write_bytes(archive.extractfile(member).read())
        command = [sys.executable, '-I', str(Path(directory) / 'server-updater-host.py')]
        if cancel:
            # Recovery code comes from this verified release; the restored
            # server may still run the older release whose enrollment failed.
            command += ['cancel-enrollment', '--expect-installation', installation, '--repository', repository,
                        '--expect-channel', channel, '--bundle', directory]
            if current_manifest is not None:
                # The recovery code and already upgraded server have independent
                # release identities. Never substitute the recovery build's pin.
                command += ['--upgraded-deployment', '--expect-version', current_version,
                            '--expect-commit', current_manifest['commit'], '--expect-arch', arch,
                            '--manifest-sha256', current_hash]
        else:
            command += ['install', '--bundle', directory, '--expect-installation', installation,
                        '--expect-version', version, '--expect-commit', manifest['commit'], '--expect-channel', channel,
                        '--expect-arch', arch, '--manifest-sha256', manifest_hash, '--repository', repository]
            if migrate:
                command.append('--migrate-deployment')
            command += ['--'] + options
        subprocess.run(command, check=True)


try:
    main(sys.argv[1:])
except (ValueError, OSError, EOFError, KeyError, TypeError, tarfile.TarError, subprocess.CalledProcessError) as error:
    print('p2pstream setup: ' + str(error), file=sys.stderr)
    sys.exit(1)
PY
}
main "$@"

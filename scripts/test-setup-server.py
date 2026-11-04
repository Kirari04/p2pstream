#!/usr/bin/env python3
"""Finite quick-setup checks; all Docker, network and wait commands are stubbed."""
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parent.parent
SCRIPT = (SOURCE / "scripts/setup-server.sh").read_text()
COMPOSE = (SOURCE / "compose.yaml").read_text()
COMPOSE_KEYS = re.findall(r"\$\{([A-Z_]+)", COMPOSE)
STUB = r'''
import json, os, sys
from pathlib import Path
name = Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ['TEST_CALLS'], 'a') as out:
    out.write(json.dumps({'command': name, 'args': args,
                          'env': {k: v for k, v in os.environ.items()
                                  if k.startswith(('P2PSTREAM_', 'MANAGEMENT_', 'PUBLIC_',
                                                   'SERVER_TUNNEL_', 'COMPOSE_', 'DOCKER_'))}}) + '\n')
if name == 'docker':
    if args[:2] == ['compose', 'version']:
        sys.exit(int(os.environ.get('TEST_COMPOSE_VERSION_STATUS', '0')))
    if args[:2] == ['context', 'inspect']:
        print(os.environ.get('TEST_ENDPOINT', 'unix:///var/run/docker.sock'))
        sys.exit(int(os.environ.get('TEST_CONTEXT_STATUS', '0')))
    if args[:2] == ['container', 'ls']:
        count = sum(1 for line in Path(os.environ['TEST_CALLS']).read_text().splitlines() if json.loads(line)['args'][:2] == ['container', 'ls'])
        print(os.environ.get('TEST_LATE_CONTAINERS' if count > 1 else 'TEST_CONTAINERS', ''))
        sys.exit(int(os.environ.get('TEST_CONTAINER_STATUS', '0')))
    if args[:2] == ['volume', 'ls']:
        count = sum(1 for line in Path(os.environ['TEST_CALLS']).read_text().splitlines() if json.loads(line)['args'][:2] == ['volume', 'ls'])
        print(os.environ.get('TEST_LATE_VOLUMES' if count > 1 else 'TEST_VOLUMES', ''))
        sys.exit(int(os.environ.get('TEST_VOLUME_STATUS', '0')))
    for action in ('config', 'pull', 'up'):
        if action in args:
            # Mimic a subprocess trying to read script input: main must supply EOF.
            assert sys.stdin.read() == ''
            sys.exit(int(os.environ.get('TEST_' + action.upper() + '_STATUS', '0')))
    raise SystemExit('Unexpected Docker command: ' + repr(args))
if name == 'curl':
    if 'https://api.ipify.org' in args:
        sys.stdout.write(os.environ.get('TEST_PUBLIC_IP', '203.0.113.19'))
        sys.exit(int(os.environ.get('TEST_LOOKUP_STATUS', '0')))
    assert any(arg.startswith('https://127.0.0.1:') for arg in args), args
    sys.exit(int(os.environ.get('TEST_READY_STATUS', '0')))
if name == 'ip':
    print(os.environ.get('TEST_ROUTE', '1.1.1.1 via 192.168.1.1 dev eth0 src 192.168.1.20 uid 1000'))
    sys.exit(int(os.environ.get('TEST_ROUTE_STATUS', '0')))
if name == 'hostname':
    print(os.environ.get('TEST_LOCAL_IPS', ''))
if name == 'sleep':
    assert args == ['1']
'''


class SetupServerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="p2pstream-setup-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.calls_file = self.root / "calls.jsonl"
        for name in ("docker", "curl", "ip", "hostname", "sleep"):
            command = self.bin / name
            command.write_text(f"#!{sys.executable}\n" + STUB)
            command.chmod(0o755)
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith(('DOCKER_', 'COMPOSE_', 'TEST_'))}
        self.env.update(PATH=f"{self.bin}:{os.environ['PATH']}", TEST_CALLS=str(self.calls_file))
        self.destination = self.root / "p2pstream"

    def run_setup(self, *args, **env):
        # Equivalent to curl | sh -s --, with the entire script on stdin.
        return subprocess.run(["sh", "-s", "--", *args], input=SCRIPT, text=True,
                              capture_output=True, cwd=self.root,
                              env=dict(self.env, **env), timeout=15)

    def calls(self, command=None):
        calls = [json.loads(line) for line in self.calls_file.read_text().splitlines()] if self.calls_file.exists() else []
        return [c for c in calls if command is None or c['command'] == command]

    def dotenv(self, destination=None):
        return dict(line.split('=', 1) for line in ((destination or self.destination) / '.env').read_text().splitlines())

    def assert_early_failure(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertNotIn('Setup token:', result.stdout)
        self.assertFalse((self.destination / '.env').exists())
        self.assertFalse(any('up' in c['args'] or 'pull' in c['args'] for c in self.calls('docker')))

    def test_piped_success_private_files_canonical_compose_and_clean_environment(self):
        hostile = dict.fromkeys(COMPOSE_KEYS, 'inherited-must-not-win')
        hostile.update(COMPOSE_FILE='evil.yaml', COMPOSE_PROJECT_NAME='evil',
                       COMPOSE_ENV_FILES='evil.env', COMPOSE_PROFILES='evil', DOCKER_CONFIG='/kept-auth')
        result = self.run_setup(**hostile)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(set(p.name for p in self.destination.iterdir()), {'.env', 'compose.yaml'})
        self.assertEqual((self.destination / 'compose.yaml').read_text(), COMPOSE)
        self.assertEqual(self.destination.stat().st_mode & 0o777, 0o700)
        self.assertEqual((self.destination / '.env').stat().st_mode & 0o777, 0o600)
        env = self.dotenv()
        self.assertEqual(env['MANAGEMENT_PUBLIC_URL'], 'https://203.0.113.19:8081')
        self.assertEqual(env['P2PSTREAM_IMAGE'], 'ghcr.io/kirari04/p2pstream:latest')
        self.assertRegex(env['MANAGEMENT_SETUP_TOKEN'], r'^[0-9a-f]{64}$')
        self.assertIn(env['MANAGEMENT_SETUP_TOKEN'], result.stdout)
        self.assertIn('does not verify', result.stdout)
        commands = [c for c in self.calls('docker') if '--env-file' in c['args']]
        self.assertEqual([c['args'][-1] for c in commands], ['--quiet', 'pull', '-d'])
        for call in commands:
            for key in COMPOSE_KEYS + ['COMPOSE_FILE', 'COMPOSE_PROJECT_NAME', 'COMPOSE_ENV_FILES', 'COMPOSE_PROFILES']:
                self.assertNotIn(key, call['env'])
            self.assertEqual(call['env']['DOCKER_CONFIG'], '/kept-auth')
            self.assertEqual(Path(call['args'][call['args'].index('--env-file') + 1]), self.destination / '.env')
            self.assertEqual(Path(call['args'][call['args'].index('-f') + 1]), self.destination / 'compose.yaml')
            self.assertEqual(call['args'][call['args'].index('--project-name') + 1], env['COMPOSE_PROJECT_NAME'])
        lookup, probe = self.calls('curl')
        self.assertNotIn('--insecure', lookup['args'])
        self.assertIn('--max-filesize', lookup['args'])
        self.assertIn('--insecure', probe['args'])
        self.assertIn('--noproxy', probe['args'])

    def test_explicit_host_custom_directory_channel_and_ports(self):
        destination = self.root / 'directory with spaces'
        result = self.run_setup('--directory', str(destination), '--host', 'stream.example.com',
                                '--channel', 'staging', '--http-port', '8080', '--https-port', '8443',
                                '--management-port', '9443')
        self.assertEqual(result.returncode, 0, result.stderr)
        env = self.dotenv(destination)
        self.assertEqual(env['MANAGEMENT_PUBLIC_URL'], 'https://stream.example.com:9443')
        self.assertEqual(env['P2PSTREAM_IMAGE'], 'ghcr.io/kirari04/p2pstream:staging')
        self.assertEqual(env['P2PSTREAM_HTTP_PORT'], '8080')
        self.assertEqual(env['P2PSTREAM_HTTPS_PORT'], '8443')
        # A later plain docker compose invocation loads this project name from .env.
        self.assertRegex(env['COMPOSE_PROJECT_NAME'], r'^p2pstream-[0-9a-f]{12}$')
        for call in self.calls('docker'):
            if '--project-name' in call['args']:
                self.assertEqual(call['args'][call['args'].index('--project-name') + 1], env['COMPOSE_PROJECT_NAME'])
        self.assertEqual(len(self.calls('curl')), 1)
        self.assertFalse(self.calls('ip'))

    def test_bad_options_fail_without_mutations(self):
        cases = [('host', value) for value in ('', 'bad host', 'a\nb', '$(touch hacked)', '${EVIL}',
                                             "a'b", 'https://example.com', '-bad.test', 'bad-.test',
                                             'a..test', '256.1.2.3', '1.2.3.4.', '01.2.3.4',
                                             '127.0.0.1', '0.0.0.0', '::1', 'a' * 64 + '.com')]
        cases += [(option, value) for option in ('http-port', 'https-port', 'management-port')
                  for value in ('0', '65536', '-1', '80x', '80\nBAD=1', '00080', '12345678901234567890')]
        cases += [('channel', 'edge'), ('unknown', 'x'), ('directory', '')]
        for option, value in cases:
            with self.subTest(option=option, value=value):
                self.assert_early_failure(self.run_setup('--' + option, value))
        self.assert_early_failure(self.run_setup('--host'))
        self.assert_early_failure(self.run_setup('--http-port', '443'))

    def test_lookup_errors_and_malformed_responses_fall_back_to_route(self):
        cases = [dict(TEST_LOOKUP_STATUS='22'), dict(TEST_PUBLIC_IP='not an address'),
                 dict(TEST_PUBLIC_IP='1.2.3.4\nEVIL=1'), dict(TEST_PUBLIC_IP='1.2.3.4.'),
                 dict(TEST_PUBLIC_IP='a' * 10000), dict(TEST_PUBLIC_IP='224.0.0.1')]
        for index, env in enumerate(cases):
            with self.subTest(env={k: v[:40] for k, v in env.items()}):
                destination = self.root / f'fallback-{index}'
                result = self.run_setup('--directory', str(destination), **env)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.dotenv(destination)['MANAGEMENT_PUBLIC_URL'], 'https://192.168.1.20:8081')
                self.assertIn('using local IPv4', result.stdout)

    def test_hostname_fallback_and_no_address_failure(self):
        result = self.run_setup(TEST_LOOKUP_STATUS='1', TEST_ROUTE_STATUS='1',
                                TEST_LOCAL_IPS='::1 127.0.0.1 10.1.2.3')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.dotenv()['MANAGEMENT_PUBLIC_URL'], 'https://10.1.2.3:8081')
        result = self.run_setup('--directory', 'no-address', TEST_LOOKUP_STATUS='1', TEST_ROUTE_STATUS='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('--host', result.stderr)
        self.assertFalse((self.root / 'no-address').exists())

    def test_existing_directory_and_dangling_symlink_are_preserved(self):
        self.destination.mkdir()
        marker = self.destination / 'keep'
        marker.write_text('untouched')
        self.assert_early_failure(self.run_setup())
        self.assertEqual(marker.read_text(), 'untouched')
        link = self.root / 'dangling'
        link.symlink_to(self.root / 'absent')
        result = self.run_setup('--directory', str(link) + '/')
        self.assertNotEqual(result.returncode, 0)
        self.assertTrue(link.is_symlink())
        self.assertFalse(self.calls())

    def test_existing_named_resources_and_inventory_failures(self):
        for env in (dict(TEST_CONTAINERS='other\np2pstream\nlast'),
                    dict(TEST_VOLUMES='other\np2pstream-data\nlast'),
                    dict(TEST_CONTAINER_STATUS='1'), dict(TEST_VOLUME_STATUS='1'),
                    dict(TEST_COMPOSE_VERSION_STATUS='1'), dict(TEST_CONTEXT_STATUS='1')):
            with self.subTest(env=env):
                self.calls_file.unlink(missing_ok=True)
                self.assert_early_failure(self.run_setup(**env))
        self.assertFalse(self.calls('curl'))

    def test_resources_appearing_during_pull_stop_startup(self):
        for key, value in [('TEST_LATE_CONTAINERS', 'p2pstream'), ('TEST_LATE_VOLUMES', 'p2pstream-data')]:
            with self.subTest(key=key):
                self.calls_file.unlink(missing_ok=True)
                result = self.run_setup('--host', 'example.com', '--directory', key, **{key: value})
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('Setup token:', result.stdout)
                self.assertIn('retained', result.stderr)
                self.assertTrue((self.root / key / '.env').exists())
                self.assertTrue(any('pull' in c['args'] for c in self.calls('docker')))
                self.assertFalse(any('up' in c['args'] for c in self.calls('docker')))

    def test_similarly_named_resources_do_not_block_new_install(self):
        result = self.run_setup('--host', 'example.com', TEST_CONTAINERS='p2pstream-old',
                                TEST_VOLUMES='p2pstream-data-old')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_remote_docker_rejected_and_explicit_local_context_supported(self):
        self.assert_early_failure(self.run_setup(DOCKER_HOST='ssh://remote'))
        self.assert_early_failure(self.run_setup(TEST_ENDPOINT='tcp://remote:2376'))
        result = self.run_setup('--host', 'example.com', DOCKER_HOST='ssh://ignored', DOCKER_CONTEXT='local')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_config_pull_up_and_readiness_failures_retain_files_and_never_claim_success(self):
        for action in ('CONFIG', 'PULL', 'UP', 'READY'):
            with self.subTest(action=action):
                destination = self.root / action
                before = len(self.calls('docker'))
                result = self.run_setup('--host', 'example.com', '--directory', str(destination),
                                        **{'TEST_' + action + '_STATUS': '1'})
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn('Setup token:', result.stdout)
                self.assertIn('retained', result.stderr)
                self.assertIn('docker logs p2pstream', result.stderr)
                self.assertTrue((destination / '.env').exists())
                calls = self.calls('docker')[before:]
                self.assertFalse(any(word in c['args'] for c in calls for word in ('rm', 'down')))
                if action in ('CONFIG', 'PULL'):
                    self.assertFalse(any('up' in c['args'] for c in calls))
        self.assertEqual(len(self.calls('sleep')), 29)

    def test_fresh_tokens_differ_and_rerun_refuses_existing_install(self):
        result = self.run_setup('--host', 'example.com')
        self.assertEqual(result.returncode, 0, result.stderr)
        first = self.dotenv()['MANAGEMENT_SETUP_TOKEN']
        result = self.run_setup('--host', 'example.com', '--directory', 'second')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotEqual(first, self.dotenv(self.root / 'second')['MANAGEMENT_SETUP_TOKEN'])
        result = self.run_setup('--host', 'example.com')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(first, self.dotenv()['MANAGEMENT_SETUP_TOKEN'])

    def test_help_needs_no_docker_or_files(self):
        result = self.run_setup('--help')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('--channel stable|staging', result.stdout)
        self.assertFalse(self.destination.exists())
        self.assertFalse(self.calls())


if __name__ == '__main__':
    unittest.main()

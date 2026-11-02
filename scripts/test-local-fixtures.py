#!/usr/bin/env python3
"""Bounded credential and Make checks; never starts a development server."""
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parent.parent


class LocalFixtureTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="p2pstream-local-fixtures-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / "scripts").mkdir()
        for name in ("Makefile", "scripts/local-dev-env.sh", "scripts/local-dev-token.py",
                     "scripts/test-sites-e2e-server.sh"):
            shutil.copy2(SOURCE / name, self.root / name)
        self.env = {key: value for key, value in os.environ.items()
                    if key not in ("AGENT_TOKEN", "CONFIG_DIR", "MANAGEMENT_BIND_ADDRESS",
                                   "PLAYWRIGHT_SETUP_TOKEN", "MAKEFLAGS", "MFLAGS")}
        self.token_file = self.root / "p2pstream-data/.local-dev/agent-token"

    def run_command(self, args, **env):
        return subprocess.run(args, cwd=self.root, env=dict(self.env, **env),
                              capture_output=True, text=True, timeout=15)

    def prepare(self, **env):
        result = self.run_command(["make", "--no-print-directory", "dev-token-check"], **env)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "")
        return result

    def test_persistent_private_atomic_token_and_concurrent_readers(self):
        with ThreadPoolExecutor(max_workers=8) as pool:
            list(pool.map(lambda _: self.prepare(), range(8)))
        token = self.token_file.read_text()
        self.assertRegex(token, r"^[0-9a-f]{64}\n$")
        self.assertEqual(self.token_file.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.token_file.parent.stat().st_mode & 0o777, 0o700)
        self.prepare()
        self.assertEqual(self.token_file.read_text(), token)
        self.assertEqual(list(self.token_file.parent.iterdir()), [self.token_file])

    def test_explicit_env_and_dotenv_are_preserved_without_creating_token(self):
        token = "operator-token-'$(must-not-run)'-" + "a" * 32
        self.prepare(AGENT_TOKEN=token)
        self.assertFalse(self.token_file.exists())
        (self.root / ".env").write_text("AGENT_TOKEN=" + "b" * 64 + "\n")
        self.prepare()
        self.assertFalse(self.token_file.exists())
        result = self.run_command(["make", "-n", "dev", "run"])
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("b" * 64, result.stdout + result.stderr)
        self.assertEqual(result.stdout.count('. ./scripts/local-dev-env.sh;'), 2)
        self.assertEqual(result.stdout.count('BOOTSTRAP_AGENT_TOKEN="$AGENT_TOKEN"'), 2)
        self.assertEqual(result.stdout.count('MANAGEMENT_BIND_ADDRESS="$MANAGEMENT_BIND_ADDRESS"'), 2)
        self.assertNotIn("local-development-agent-token", result.stdout)
        self.assertNotIn("local-agent-token", result.stdout)

    def test_custom_config_dir_and_loopback_default_with_override(self):
        config = self.root / "custom config"
        command = ["sh", "-c", '. ./scripts/local-dev-env.sh; printf "%s" "$MANAGEMENT_BIND_ADDRESS"']
        result = self.run_command(command, CONFIG_DIR=str(config))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "127.0.0.1")
        self.assertTrue((config / ".local-dev/agent-token").exists())
        self.assertFalse(self.token_file.exists())
        result = self.run_command(command, CONFIG_DIR=str(config), MANAGEMENT_BIND_ADDRESS="::1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "::1")

    def test_short_explicit_token_fails_without_fallback(self):
        result = self.run_command(["make", "dev-token-check"], AGENT_TOKEN="short")
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("short", result.stdout + result.stderr)
        self.assertFalse(self.token_file.exists())

    def test_unsafe_or_corrupt_existing_token_is_never_replaced(self):
        self.prepare()
        for contents, mode in (("broken-secret\n", 0o600), ("a" * 64 + "\n", 0o644)):
            self.token_file.write_text(contents)
            self.token_file.chmod(mode)
            result = self.run_command(["make", "dev-token-check"])
            self.assertNotEqual(result.returncode, 0)
            self.assertNotIn(contents.strip(), result.stdout + result.stderr)
            self.assertEqual(self.token_file.read_text(), contents)
        self.token_file.unlink()
        outside = self.root / "outside-token"
        outside.write_text("c" * 64 + "\n")
        self.token_file.symlink_to(outside)
        self.assertNotEqual(self.run_command(["make", "dev-token-check"]).returncode, 0)
        self.token_file.unlink()
        self.token_file.parent.rmdir()
        self.token_file.parent.symlink_to(self.root, target_is_directory=True)
        self.assertNotEqual(self.run_command(["make", "dev-token-check"]).returncode, 0)

    def test_sites_runner_requires_and_forwards_setup_token_in_private_cwd(self):
        dist = self.root / "web/management/dist"
        dist.mkdir(parents=True)
        (dist / "index.html").write_text("fixture")
        stub_dir = self.root / "stubs"
        stub_dir.mkdir()
        go = stub_dir / "go"
        go.write_text('''#!/usr/bin/env python3
import os, sys
from pathlib import Path
if sys.argv[1] == 'run':
    sys.exit(0)
binary = Path(sys.argv[sys.argv.index('-o') + 1])
binary.write_text(''' + repr('''#!/usr/bin/env python3
import json, os
print(json.dumps({'cwd': os.getcwd(), 'env': dict(os.environ)}))
''') + ''')
binary.chmod(0o700)
''')
        go.chmod(0o700)
        command = ["bash", "scripts/test-sites-e2e-server.sh"]
        result = self.run_command(command, PATH=f"{stub_dir}:{self.env['PATH']}")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("must be supplied", result.stderr)
        token = "d" * 64
        result = self.run_command(command, PATH=f"{stub_dir}:{self.env['PATH']}",
                                  PLAYWRIGHT_SETUP_TOKEN=token, MANAGEMENT_BIND_ADDRESS="0.0.0.0",
                                  MANAGEMENT_SETUP_TOKEN="must-not-leak", DATABASE_URL="must-not-use")
        self.assertEqual(result.returncode, 0, result.stderr)
        invocation = json.loads(result.stdout)
        self.assertEqual(invocation["env"]["MANAGEMENT_SETUP_TOKEN"], token)
        self.assertEqual(invocation["env"]["MANAGEMENT_BIND_ADDRESS"], "127.0.0.1")
        self.assertIn(invocation["cwd"], invocation["env"]["DATABASE_URL"])
        self.assertFalse(Path(invocation["cwd"]).exists())
        self.assertNotIn("must-not", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()

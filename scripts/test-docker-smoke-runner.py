#!/usr/bin/env python3
"""Exercise the smoke credential boundary and cleanup without starting Docker."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class SmokeRunnerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="p2pstream-smoke-runner-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "scripts").mkdir()
        source = Path(__file__).resolve().parent.parent
        for name in ("Makefile", "docker-compose.test.yml", "scripts/run-docker-smoke.sh"):
            shutil.copy2(source / name, self.root / name)
        # GNU Make would evaluate this before running any recipe if it loaded .env.
        (self.root / ".env").write_text("$(error application .env must not be loaded)\n")
        self.calls = self.root / "calls.jsonl"
        docker = self.root / "docker"
        docker.write_text("""#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
keys = ('SMOKE_SETUP_TOKEN', 'SMOKE_AGENT_TOKEN', 'SMOKE_ADMIN_PASSWORD')
with Path(os.environ['SMOKE_TEST_CALLS']).open('a') as out:
    out.write(json.dumps({'args': sys.argv[1:], 'credentials': {k: os.environ[k] for k in keys}}) + '\\n')
sys.exit(int(os.environ.get('SMOKE_TEST_UP_STATUS', '0')) if 'up' in sys.argv else 0)
""")
        docker.chmod(0o755)
        self.env = dict(os.environ, PATH=f"{self.root}:{os.environ['PATH']}",
                        SMOKE_TEST_CALLS=str(self.calls),
                        SMOKE_SETUP_TOKEN="inherited-value-must-be-replaced",
                        SMOKE_AGENT_TOKEN="inherited-value-must-be-replaced",
                        SMOKE_ADMIN_PASSWORD="inherited-value-must-be-replaced")

    def run_target(self, target="docker-smoke", status=0):
        return subprocess.run(["make", "--no-print-directory", target], cwd=self.root,
                              env=dict(self.env, SMOKE_TEST_UP_STATUS=str(status)),
                              capture_output=True, text=True)

    def read_calls(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def test_credentials_are_shared_per_run_and_fresh_between_runs(self):
        results = []
        for _ in range(2):
            result = self.run_target()
            self.assertEqual(result.returncode, 0, result.stderr)
            results.append(result)
        calls = self.read_calls()
        self.assertEqual(len(calls), 6)
        for result, batch in zip(results, (calls[:3], calls[3:])):
            self.assertTrue(all(call["credentials"] == batch[0]["credentials"] for call in batch))
            values = list(batch[0]["credentials"].values())
            self.assertEqual(len(set(values)), 3)
            for value in values:
                self.assertRegex(value, r"^[0-9a-f]{64}$")
                self.assertNotIn(value, result.stdout + result.stderr)
            self.assertIn("up", batch[1]["args"])
            for call in batch:
                self.assertEqual(call["args"][:3], ["compose", "--env-file", "/dev/null"])
                self.assertIn("--project-name", call["args"])
            self.assertIn("down", batch[-1]["args"])
        self.assertTrue(all(a != b for a, b in zip(calls[0]["credentials"].values(),
                                                   calls[3]["credentials"].values())))

    def test_failed_smoke_still_cleans_up_and_reports_failure(self):
        result = self.run_target(status=17)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("Error 17", result.stderr)
        calls = self.read_calls()
        self.assertEqual(len(calls), 3)
        self.assertIn("down", calls[-1]["args"])
        self.assertIn("-v", calls[-1]["args"])

    def test_clean_works_without_saved_credentials_or_dotenv(self):
        result = self.run_target("docker-smoke-clean")
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.read_calls()
        self.assertEqual(len(calls), 1)
        self.assertIn("down", calls[0]["args"])
        self.assertIn("--remove-orphans", calls[0]["args"])


if __name__ == "__main__":
    unittest.main()

"""Run the operator-only Go evidence checks through the existing ops test gate."""
import os
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]


class MailActivationGuardTest(unittest.TestCase):
    def test_recovery_evidence_and_loopback_preflight(self):
        env = dict(os.environ)
        env["GOMAXPROCS"] = "2"
        result = subprocess.run(
            ["go", "test", "scripts/mail-activate-environment.go",
             "scripts/mail-activate-environment_test.go", "-count=1"],
            cwd=ROOT, env=env, capture_output=True, text=True, timeout=180,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()

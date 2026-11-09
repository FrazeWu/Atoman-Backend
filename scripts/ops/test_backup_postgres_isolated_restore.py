"""Mock-only verification that mail restores cannot touch live PostgreSQL."""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

s = importlib.util.spec_from_file_location("atoman_postgres_backup", Path(__file__).with_name("backup-postgres.py"))
pg = importlib.util.module_from_spec(s)
s.loader.exec_module(pg)


class IsolatedRestoreTests(unittest.TestCase):
    def test_success_uses_private_networkless_larger_low_memory_restore(self):
        fake = {"database": "atoman_mail", "dump": "atoman_mail.dump", "public_table_counts": {"mail": 2}, "bytes":123,"backup_seconds":1.0}
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(pg.sp, "check_output", return_value="restored-container\n") as check, mock.patch.object(pg.sp, "run", return_value=mock.Mock(returncode=0)) as runner, mock.patch.object(pg, "sql") as sql, mock.patch.object(pg, "counts", return_value={"mail": 2}):
            pg.verify("sha256:original-postgres", Path(directory), [fake])
        cmd = check.call_args.args[0]
        self.assertEqual(cmd[:2], ["docker", "run"])
        self.assertIn("--network", cmd)
        self.assertEqual(cmd[cmd.index("--network") + 1], "none")
        self.assertEqual(cmd[cmd.index("--memory") + 1], "3g")
        self.assertEqual(cmd[cmd.index("-c") + 1], "maintenance_work_mem=128MB")
        self.assertTrue(any(arg.endswith(":/backup:ro") for arg in cmd))
        self.assertEqual(cmd[cmd.index("--label") + 1], "atoman.ops=restore-verification")
        self.assertEqual(cmd[-3], "sha256:original-postgres")
        self.assertEqual(fake["restore_profile"], "full")
        self.assertTrue(fake["restore_verified"])
        self.assertTrue(any(call.args[0][:3] == ["docker", "rm", "-f"] for call in runner.call_args_list))
        self.assertEqual(sql.call_args.args[1], "atoman_restore")

    def test_early_terminated_restore_always_removes_isolated_container(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(pg.sp, "check_output", return_value="fake-restore-id\n"), mock.patch.object(pg.sp, "run", return_value=mock.Mock(returncode=0)) as runner, mock.patch.object(pg, "sql", side_effect=subprocess.CalledProcessError(1, "create")):
            with self.assertRaises(subprocess.CalledProcessError):
                pg.verify("sha256:isolated", Path(directory), [{"database": "atoman_mail"}])
        self.assertTrue(any(call.args[0] == ["docker", "rm", "-f", "-v", "fake-restore-id"] for call in runner.call_args_list))


if __name__ == "__main__":
    unittest.main()

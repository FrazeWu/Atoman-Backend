import hashlib
import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

module_path = Path(__file__).with_name("nas-pull-encrypted-backups.py")
spec = importlib.util.spec_from_file_location("nas_pull", module_path)
nas = importlib.util.module_from_spec(spec)
spec.loader.exec_module(nas)


class NasPullTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "AtomanBackups"
        self.root.mkdir(mode=0o700)
        self.root.chmod(0o700)
        self.key = self.root / ".ssh-key"
        self.key.write_text("only synthetic test fixture")
        self.key.chmod(0o600)
        self.known_hosts = self.root / ".known-hosts"
        self.known_hosts.write_text("synthetic host key")
        self.snapshot = "20261028T033423Z-ou85era0"
        self.payload = b"ciphertext fixture only, no production data"
        self.sha = hashlib.sha256(self.payload).hexdigest()
        self.receipt = {
            "format": "atoman-encrypted-export-v1",
            "ciphertext": "backup.tar.age", "bytes": len(self.payload),
            "sha256": self.sha, "offsite_verified": False,
            "source_snapshot_id": self.snapshot,
            "source_completed_at": "2026-10-28T03:49:00+00:00",
        }

    def download(self, host, identity, known_hosts, remote, local):
        if remote.endswith("receipt.json"):
            local.write_text(json.dumps(self.receipt))
        elif remote.endswith("backup.tar.age"):
            local.write_bytes(self.payload)
        else:
            raise AssertionError("Unexpected remote fetch")

    def pull(self):
        with mock.patch.object(nas, "list_snapshots", return_value=[self.snapshot]):
            with mock.patch.object(nas, "fetch", side_effect=self.download):
                return nas.pull(self.root, "backupread@vps.example", self.key, self.known_hosts)

    def test_valid_transfer_and_idempotent_repeat(self):
        self.assertEqual(self.pull(), [(self.snapshot, "verified-copy")])
        complete = self.root / "snapshots" / self.snapshot
        self.assertEqual((complete / "backup.tar.age").read_bytes(), self.payload)
        marker = json.loads((complete / "LOCAL_COMPLETE.json").read_text())
        self.assertTrue(marker["ciphertext_transferred"])
        self.assertFalse(marker["recovery_verified"])
        self.assertEqual(marker["source_snapshot_id"], self.snapshot)
        self.assertEqual(marker["source_completed_at"], self.receipt["source_completed_at"])
        self.assertEqual(self.pull(), [(self.snapshot, "already-verified")])

    def test_hash_mismatch_does_not_publish(self):
        self.receipt["sha256"] = "0" * 64
        with self.assertRaises(nas.PullError):
            self.pull()
        self.assertFalse((self.root / "snapshots" / self.snapshot).exists())

    def test_existing_copy_tampering_is_detected(self):
        self.pull()
        (self.root / "snapshots" / self.snapshot / "backup.tar.age").write_bytes(b"tampered")
        with self.assertRaises(nas.PullError):
            self.pull()

    def test_unexpected_remote_names_are_ignored(self):
        fake = "/exports/20261028T033423Z-ou85era0\n../../etc/passwd\n20261028T033423Z-bad;rm\n"
        with mock.patch.object(nas, "run_sftp", return_value=fake):
            self.assertEqual(nas.list_snapshots("host", self.key, self.known_hosts), [self.snapshot])

    def test_source_provenance_receipt_fail_closed(self):
        for change in [
            {"source_completed_at": "2026-10-28T03:49:00"},
            {"source_completed_at": "not a date"},
            {"source_snapshot_id": "../../etc/passwd"},
        ]:
            with self.subTest(change=change):
                record = {**self.receipt, **change}
                with self.assertRaises(nas.PullError):
                    nas.validate_receipt(record)
        incomplete = dict(self.receipt)
        incomplete.pop("source_completed_at")
        with self.assertRaises(nas.PullError):
            nas.validate_receipt(incomplete)

    def test_world_readable_backup_directory_rejected(self):
        self.root.chmod(0o755)
        with self.assertRaises(nas.PullError):
            self.pull()

    def test_symlink_backup_directory_rejected(self):
        link = Path(self.temp.name) / "link"
        link.symlink_to(self.root)
        with self.assertRaises(nas.PullError):
            nas.private_directory(link)

    def test_malformed_receipts_rejected(self):
        for defect in [
            {"bytes": -2},
            {"sha256": "zz"},
            {"ciphertext": "../escape.age"},
            {"offsite_verified": True},
            {"format": "other"},
        ]:
            with self.subTest(defect=defect):
                candidate = {**self.receipt, **defect}
                with self.assertRaises(nas.PullError):
                    nas.validate_receipt(candidate)

    def test_newest_snapshot_is_pulled_first(self):
        older = "20261027T033423Z-abcdef"
        newest = self.snapshot
        with mock.patch.object(nas, "list_snapshots", return_value=[older, newest]):
            with mock.patch.object(nas, "fetch", side_effect=self.download):
                copies = nas.pull(self.root, "backupread@vps.example", self.key, self.known_hosts)
        self.assertEqual([name for name, _ in copies], [newest, older])

    def test_missing_remote_snapshots_fail_loudly(self):
        with mock.patch.object(nas, "list_snapshots", return_value=[]):
            with self.assertRaises(nas.PullError):
                nas.pull(self.root, "backupread@vps.example", self.key, self.known_hosts)

    def test_latest_snapshot_freshness_and_stale_failure(self):
        self.pull()
        now = dt.datetime(2026, 10, 28, 4, tzinfo=dt.timezone.utc)
        result = nas.verify_freshness(self.root, max_age_hours=48, now=now)
        self.assertEqual(result["latest_snapshot"], self.snapshot)
        self.assertEqual(result["source_completed_at"], self.receipt["source_completed_at"])
        self.assertGreater(result["age_hours"], 0)
        with self.assertRaisesRegex(nas.PullError, "stale"):
            nas.verify_freshness(self.root, max_age_hours=48,
                                  now=now + dt.timedelta(days=3))

    def test_source_completion_time_not_export_directory_name_controls_rpo(self):
        self.pull()
        complete = self.root / "snapshots"
        legacy_export = "20261028T040000Z-abcdef"
        legacy = complete / legacy_export
        legacy.mkdir(mode=0o700)
        marker = {
            "format": "atoman-nas-copy-v1", "source": legacy_export,
            "source_snapshot_id": "20261027T150000Z-abcdef",
            "source_completed_at": "2026-10-27T15:58:53+00:00",
        }
        (legacy / "LOCAL_COMPLETE.json").write_text(json.dumps(marker))
        present = dt.datetime(2026, 10, 28, 5, tzinfo=dt.timezone.utc)
        actual = nas.verify_freshness(self.root, max_age_hours=48, now=present)
        self.assertEqual(actual["latest_snapshot"], self.snapshot)
        self.assertLess(actual["age_hours"], 2)

    def test_missing_source_provenance_cannot_be_declared_fresh(self):
        self.pull()
        marker_file = self.root / "snapshots" / self.snapshot / "LOCAL_COMPLETE.json"
        marker = json.loads(marker_file.read_text())
        del marker["source_completed_at"]
        marker_file.write_text(json.dumps(marker))
        with self.assertRaises(nas.PullError):
            nas.verify_freshness(self.root, 48)

    def test_status_write_private_atomic_and_symlink_rejected(self):
        nas.write_status(self.root, "ok", {"latest_snapshot": self.snapshot})
        status = self.root / "pull-status.json"
        self.assertEqual(status.stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads(status.read_text())["state"], "ok")
        status.unlink()
        status.symlink_to(self.key)
        with self.assertRaises(nas.PullError):
            nas.write_status(self.root, "error", {"message": "refusing symlink"})
        self.assertEqual(self.key.read_text(), "only synthetic test fixture")

    def test_ciphertext_fetch_resumes_partial_file(self):
        partial = self.root / "backup.tar.age"
        partial.write_bytes(b"incomplete ciphertext")
        with mock.patch.object(nas, "run_sftp") as sftp:
            nas.fetch("host", self.key, self.known_hosts,
                      "/exports/id/backup.tar.age", partial)
        self.assertTrue(sftp.call_args.args[3][0].startswith("reget "))

    def test_new_ciphertext_fetch_starts_from_zero(self):
        target = self.root / "backup.tar.age"
        with mock.patch.object(nas, "run_sftp") as sftp:
            nas.fetch("host", self.key, self.known_hosts,
                      "/exports/id/backup.tar.age", target)
        self.assertTrue(sftp.call_args.args[3][0].startswith("get "))

    def test_existing_symlink_is_rejected_before_fetch(self):
        outside = self.root / "outside"
        outside.write_bytes(b"data")
        path = self.root / "backup.tar.age"
        path.symlink_to(outside)
        with mock.patch.object(nas, "run_sftp") as sftp, self.assertRaises(nas.PullError):
            nas.fetch("host", self.key, self.known_hosts,
                      "/exports/id/backup.tar.age", path)
        sftp.assert_not_called()

    def test_sftp_uses_batch_mode_pinned_hosts_and_no_shell(self):
        result = mock.Mock(returncode=0, stdout=self.snapshot + "\n", stderr="")
        with mock.patch.object(nas.subprocess, "run", return_value=result) as process:
            names = nas.list_snapshots("backupread@vps.example", self.key, self.known_hosts)
        self.assertEqual(names, [self.snapshot])
        args, kwargs = process.call_args
        command = args[0]
        self.assertEqual(command[0], "sftp")
        self.assertIn("-oStrictHostKeyChecking=yes", command)
        self.assertIn("-oBatchMode=yes", command)
        self.assertNotIn("shell", kwargs)


if __name__ == "__main__":
    unittest.main()

import hashlib
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

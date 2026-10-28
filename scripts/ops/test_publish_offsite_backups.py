import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

path = Path(__file__).with_name("publish-offsite-backups.py")
spec = importlib.util.spec_from_file_location("atoman_pub", path)
pub = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pub)


class PublishTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.export_root = self.root / "exports"
        self.export_root.mkdir(mode=0o755)
        self.prepared = self.root / "prepared"
        self.prepared.mkdir()
        self.payload = b"age ciphertext fixture; no production data"
        (self.prepared / "backup.tar.age").write_bytes(self.payload)
        self.receipt = {
            "format": "atoman-encrypted-export-v1",
            "ciphertext": "backup.tar.age",
            "bytes": len(self.payload),
            "sha256": hashlib.sha256(self.payload).hexdigest(),
            "offsite_verified": False,
        }
        (self.prepared / "receipt.json").write_text(json.dumps(self.receipt))
        self.id = "20261028T033423Z-ou85era0"

    def test_publish_and_verify_repeat(self):
        with mock.patch.object(pub.os, "chown"):
            target, status = pub.publish(self.prepared, self.export_root, self.id, 985)
        self.assertEqual(status, "published")
        self.assertEqual(pub.validate_export(target), self.receipt)
        self.assertEqual((target / "backup.tar.age").read_bytes(), self.payload)
        with mock.patch.object(pub.os, "chown"):
            _, status = pub.publish(self.prepared, self.export_root, self.id, 985)
        self.assertEqual(status, "already-published")

    def test_mismatched_hash_rejected(self):
        self.receipt["sha256"] = "0" * 64
        (self.prepared / "receipt.json").write_text(json.dumps(self.receipt))
        with mock.patch.object(pub.os, "chown"):
            with self.assertRaises(ValueError):
                pub.publish(self.prepared, self.export_root, self.id, 985)
        self.assertFalse((self.export_root / self.id).exists())

    def test_invalid_id_rejected(self):
        with mock.patch.object(pub.os, "chown"):
            with self.assertRaises(ValueError):
                pub.publish(self.prepared, self.export_root, "../unsafe", 985)

    def test_prune_only_verified_managed_cache(self):
        names = ["20261025T010101Z-abcdef", "20261026T010101Z-abcdef", "20261027T010101Z-abcdef"]
        for name in names:
            directory = self.export_root / name
            directory.mkdir()
            (directory / "backup.tar.age").write_bytes(self.payload)
            (directory / "receipt.json").write_text(json.dumps(self.receipt))
        untouched = self.export_root / "manual-dir"
        untouched.mkdir()
        pub.prune_local_sftp_cache(self.export_root, keep=2)
        self.assertFalse((self.export_root / names[0]).exists())
        self.assertTrue((self.export_root / names[1]).exists())
        self.assertTrue((self.export_root / names[2]).exists())
        self.assertTrue(untouched.exists())

    def test_latest_verified_source_only(self):
        source = self.root / "postgres"
        source.mkdir()
        for name in ["20261027T010101Z-abcdef", "20261028T010101Z-abcdef"]:
            item = source / name
            item.mkdir()
            (item / "COMPLETE").touch()
        adapter = mock.Mock()
        result = pub.select_latest_verified(source, adapter)
        self.assertEqual(result.name, "20261028T010101Z-abcdef")
        adapter.validate_snapshot.assert_called_once()

if __name__ == "__main__":
    unittest.main()

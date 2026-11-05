import datetime as dt
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
spec = importlib.util.spec_from_file_location("health", Path(__file__).with_name("check-backup-health.py"))
health = importlib.util.module_from_spec(spec)
spec.loader.exec_module(health)

class BackupHealthTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.snapshots = self.root / "snapshots"
        self.snapshots.mkdir()
        self.snapshot = self.snapshots / "20261105T000000Z-abcdef"
        self.snapshot.mkdir()
        (self.snapshot / "COMPLETE").touch()
        self.now = dt.datetime(2026, 11, 5, 1, tzinfo=dt.timezone.utc)
        self.manifest = {"complete": True, "completed_at": "2026-11-05T00:00:00Z", "backups": [],
                         "mail_assets_verified": True, "additional_files": [{"name":"mail-assets.tar","verified":True,"bytes":3}]}
        for database in ("atoman_prod","atoman_dev","atoman_mail"):
            name = database + ".dump"
            (self.snapshot/name).write_bytes(b"fixture")
            self.manifest["backups"].append({"database":database,"dump":name,"bytes":7,"restore_verified":True})
        (self.snapshot/"mail-assets.tar").write_bytes(b"tar")
        self.write()
    def write(self):
        (self.snapshot/"manifest.json").write_text(json.dumps(self.manifest))
    def test_verified_paired_backup_is_healthy(self):
        result = health.latest_snapshot(self.snapshots, True, now=self.now)
        self.assertEqual(result["age_hours"],1)
        self.assertTrue(result["mail_assets_verified"])
    def test_stale_or_unpaired_backup_fails(self):
        with self.assertRaisesRegex(ValueError,"stale"):
            health.latest_snapshot(self.snapshots,True,now=self.now+dt.timedelta(days=3))
        self.manifest["mail_assets_verified"] = False
        self.write()
        with self.assertRaisesRegex(ValueError,"Paired"):
            health.latest_snapshot(self.snapshots,True,now=self.now)
    def test_fresh_export_cannot_hide_stale_or_wrong_source(self):
        exports = self.root/"exports"
        item = exports/"20261105T010000Z-abcdef"
        item.mkdir(parents=True)
        (item/"backup.tar.age").write_bytes(b"test")
        receipt = {"format":"atoman-encrypted-export-v1","ciphertext":"backup.tar.age","bytes":4,"source_snapshot_id":"20261104T000000Z-abcdef"}
        (item/"receipt.json").write_text(json.dumps(receipt))
        with self.assertRaisesRegex(ValueError,"has not been published"):
            health.encrypted_copy(exports,self.snapshot.name)
        receipt["source_snapshot_id"] = self.snapshot.name
        (item/"receipt.json").write_text(json.dumps(receipt))
        self.assertFalse(health.encrypted_copy(exports,self.snapshot.name)["offsite_transfer_confirmed"])
    def test_unverified_database_and_symlink_fail_closed(self):
        self.manifest["backups"][0]["restore_verified"] = False
        self.write()
        with self.assertRaisesRegex(ValueError,"restore verification"):
            health.latest_snapshot(self.snapshots,True,now=self.now)


    def publications(self, with_capsule=True):
        self.exports = self.root / "paired-exports"
        self.exports.mkdir()
        source = self.snapshot.name
        full_path = self.exports / source
        full_path.mkdir()
        full_payload = b"synthetic full ciphertext"
        (full_path / "backup.tar.age").write_bytes(full_payload)
        self.full_receipt = {
            "format": "atoman-encrypted-export-v1", "ciphertext": "backup.tar.age",
            "bytes": len(full_payload), "sha256": hashlib.sha256(full_payload).hexdigest(),
            "source_snapshot_id": source, "source_completed_at": self.manifest["completed_at"],
            "offsite_verified": False,
        }
        (full_path / "receipt.json").write_text(json.dumps(self.full_receipt))
        if with_capsule:
            self.capsule_path = self.exports / (source + "m")
            self.capsule_path.mkdir()
            payload = b"synthetic complete mail ciphertext"
            (self.capsule_path / "backup.tar.age").write_bytes(payload)
            self.capsule_receipt = {
                "format": "atoman-encrypted-export-v1", "ciphertext": "backup.tar.age",
                "bytes": len(payload), "sha256": hashlib.sha256(payload).hexdigest(),
                "source_snapshot_id": source + "m", "paired_source_snapshot_id": source,
                "source_completed_at": self.manifest["completed_at"],
                "backup_scope": "mail-complete", "scope": "post_activation_mail_capsule",
                "source_manifest_sha256": hashlib.sha256((self.snapshot / "manifest.json").read_bytes()).hexdigest(),
                "source_full_ciphertext_sha256": self.full_receipt["sha256"],
                "source_full_ciphertext_bytes": self.full_receipt["bytes"],
                "capsule_manifest_sha256": "a" * 64, "offsite_verified": False,
            }
            (self.capsule_path / "receipt.json").write_text(json.dumps(self.capsule_receipt))

    def check_publications(self):
        source = health.latest_snapshot(self.snapshots, True, now=self.now)
        return health.encrypted_mail_copies(self.exports, source["source_snapshot_id"],
                                            source["source_manifest_sha256"], source["source_completed_at"])

    def test_both_scopes_required_and_health_hashes_only_small_capsule(self):
        self.publications()
        with mock.patch.object(health, "checksum", wraps=health.checksum) as digest:
            result = self.check_publications()
        digest.assert_called_once_with(self.capsule_path / "backup.tar.age")
        self.assertTrue(result["encrypted_publication_ready"])
        self.assertTrue(result["mail_capsule_publication_ready"])
        self.assertFalse(result["offsite_transfer_confirmed"])

    def test_published_full_without_capsule_is_unhealthy(self):
        self.publications(with_capsule=False)
        with self.assertRaisesRegex(ValueError, "publication is missing"):
            self.check_publications()

    def test_capsule_source_manifest_and_full_binding_mismatch_fail(self):
        self.publications()
        original = dict(self.capsule_receipt)
        changes = {
            "paired_source_snapshot_id": "20261104T000000Z-abcdef",
            "source_manifest_sha256": "f" * 64,
            "source_full_ciphertext_sha256": "e" * 64,
            "source_full_ciphertext_bytes": self.full_receipt["bytes"] + 1,
        }
        for field, value in changes.items():
            with self.subTest(field=field):
                receipt = dict(original)
                receipt[field] = value
                (self.capsule_path / "receipt.json").write_text(json.dumps(receipt))
                with self.assertRaises(ValueError):
                    self.check_publications()
        (self.capsule_path / "receipt.json").write_text(json.dumps(original))
        # Same JSON values, different original bytes: the recorded source SHA must change.
        with (self.snapshot / "manifest.json").open("ab") as file:
            file.write(b"\n")
        with self.assertRaisesRegex(ValueError, "source manifest differs"):
            self.check_publications()

    def test_capsule_ciphertext_change_is_detected_at_same_length(self):
        self.publications()
        payload = self.capsule_path / "backup.tar.age"
        payload.write_bytes(b"x" * payload.stat().st_size)
        with self.assertRaisesRegex(ValueError, "checksum changed"):
            self.check_publications()

if __name__ == "__main__": unittest.main()

import datetime as dt
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
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

if __name__ == "__main__": unittest.main()

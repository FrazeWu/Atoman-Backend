import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tempfile
import types
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("mail_publisher", Path(__file__).with_name("publish-mail-backups.py"))
pub = importlib.util.module_from_spec(spec)
spec.loader.exec_module(pub)


class MailPublisherTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.sources = self.root / "sources"
        self.exports = self.root / "exports"
        self.capsules = self.root / "capsules"
        self.staging = self.root / "staging"
        for folder in (self.sources, self.exports, self.capsules, self.staging):
            folder.mkdir(mode=0o700)

    def write_json(self, path, value):
        path.write_text(json.dumps(value, sort_keys=True))
        path.chmod(0o600)

    def source(self, name):
        path = self.sources / name
        path.mkdir()
        (path / "COMPLETE").write_text("synthetic source\n")
        self.write_json(path / "manifest.json", {"complete": True, "completed_at": "2026-11-05T00:00:00Z"})
        return path

    def full(self, source):
        directory = self.exports / source.name
        directory.mkdir()
        payload = b"synthetic age ciphertext, not production data"
        (directory / "backup.tar.age").write_bytes(payload)
        receipt = {
            "format": "atoman-encrypted-export-v1", "ciphertext": "backup.tar.age",
            "bytes": len(payload), "sha256": hashlib.sha256(payload).hexdigest(),
            "offsite_verified": False, "source_snapshot_id": source.name,
            "source_completed_at": "2026-11-05T00:00:00Z",
        }
        self.write_json(directory / "receipt.json", receipt)
        return directory, receipt

    def capsule(self, source, full_receipt, *, scope="post_activation_mail_capsule", publish=False):
        directory = self.capsules / (source.name + "m")
        directory.mkdir(mode=0o700)
        payload = ("synthetic encrypted mail " + source.name).encode()
        (directory / "backup.tar.age").write_bytes(payload)
        application = {
            "source_snapshot_id": source.name, "source_dump_sha256": "a" * 64,
            "source_tables": {"Users": 1, "mail_accounts": 0}, "absent_mail_tables": [],
        }
        self.write_json(directory / "application-mail-manifest.json", application)
        capsule = {
            "format": "atoman-mail-recovery-capsule-v1", "scope": scope,
            "capsule_id": directory.name, "source_snapshot_id": source.name,
            "source_manifest_sha256": pub.publisher.checksum(source / "manifest.json"),
            "source_full_ciphertext_sha256": full_receipt["sha256"],
            "source_full_ciphertext_bytes": full_receipt["bytes"],
            "application_manifest_sha256": pub.publisher.checksum(directory / "application-mail-manifest.json"),
            "atoman_prod_dump_sha256": application["source_dump_sha256"],
            "application_source_tables": application["source_tables"],
            "application_absent_mail_tables": application["absent_mail_tables"],
        }
        self.write_json(directory / "capsule-manifest.json", capsule)
        receipt = {
            "format": "atoman-encrypted-export-v1", "ciphertext": "backup.tar.age",
            "bytes": len(payload), "sha256": hashlib.sha256(payload).hexdigest(),
            "offsite_verified": False, "source_snapshot_id": directory.name,
            "paired_source_snapshot_id": source.name,
            "source_completed_at": "2026-11-05T00:00:00Z",
            "backup_scope": "mail-complete", "scope": scope,
            "source_manifest_sha256": capsule["source_manifest_sha256"],
            "source_full_ciphertext_sha256": full_receipt["sha256"],
            "source_full_ciphertext_bytes": full_receipt["bytes"],
            "capsule_manifest_sha256": pub.publisher.checksum(directory / "capsule-manifest.json"),
        }
        self.write_json(directory / "receipt.json", receipt)
        (directory / "COMPLETE").write_text("synthetic encrypted capsule\n")
        for file in directory.iterdir():
            file.chmod(0o600)
        if publish:
            destination = self.exports / directory.name
            destination.mkdir()
            for name in ("backup.tar.age", "receipt.json"):
                shutil.copyfile(directory / name, destination / name)
        return directory, receipt

    def test_full_and_capsule_each_keep_one_and_private_cache_is_scoped(self):
        old = self.source("20261104T000000Z-abcdef")
        new = self.source("20261105T000000Z-abcdef")
        for source in (old, new):
            _full, receipt = self.full(source)
            self.capsule(source, receipt, publish=True)
        untouched = self.exports / "manual-files"
        untouched.mkdir()
        # A full export accidentally placed under the private root is not ours to prune.
        private_full = self.capsules / old.name
        private_full.mkdir()
        (private_full / "backup.tar.age").write_bytes(b"preserve")
        (private_full / "receipt.json").write_text("{}")
        removed = pub.prune_scoped_exports(self.exports, keep=1)
        self.assertEqual({row["scope"] for row in removed}, {"full", "mail-complete"})
        self.assertFalse((self.exports / old.name).exists())
        self.assertFalse((self.exports / (old.name + "m")).exists())
        self.assertTrue((self.exports / new.name).exists())
        self.assertTrue((self.exports / (new.name + "m")).exists())
        self.assertTrue(untouched.exists())
        self.assertEqual(pub.prune_private_capsules(self.capsules, keep=1), [old.name + "m"])
        self.assertTrue((self.capsules / (new.name + "m")).exists())
        self.assertTrue(private_full.exists())

    def test_existing_full_still_builds_and_publishes_capsule(self):
        source = self.source("20261105T000000Z-abcdef")
        _full, receipt = self.full(source)
        exporter = types.SimpleNamespace(
            validate_recipients=mock.Mock(), validate_snapshot=mock.Mock(),
            prepare=mock.Mock(side_effect=AssertionError("existing full must not be recreated")))
        builder = mock.Mock()
        builder.build.side_effect = lambda selected, full, target, recipient, scope: self.capsule(
            selected, receipt, scope=scope)[1]
        with mock.patch.object(pub.publisher.os, "chown"):
            result = pub.publish_latest(
                self.sources, self.staging, self.exports, self.capsules, "age-public-fixture",
                1, os.getgid(), "post_activation_mail_capsule", exporter, builder)
        builder.build.assert_called_once()
        exporter.prepare.assert_not_called()
        self.assertTrue((self.exports / source.name).exists())
        self.assertTrue((self.exports / (source.name + "m")).exists())
        self.assertTrue(result["mail_capsule_publication_ready"])
        self.assertFalse(result["offsite_transfer_confirmed"])

    def test_source_mismatch_fails_before_capsule_generation(self):
        source = self.source("20261105T000000Z-abcdef")
        folder, receipt = self.full(source)
        receipt["source_snapshot_id"] = "20261104T000000Z-abcdef"
        self.write_json(folder / "receipt.json", receipt)
        exporter = types.SimpleNamespace(validate_recipients=mock.Mock(), validate_snapshot=mock.Mock())
        builder = mock.Mock()
        with self.assertRaisesRegex(ValueError, "selected immutable source"):
            pub.publish_latest(self.sources, self.staging, self.exports, self.capsules,
                               "age-public-fixture", 1, os.getgid(),
                               "post_activation_mail_capsule", exporter, builder)
        builder.build.assert_not_called()

    def test_private_cache_is_not_deleted_after_hash_mismatch(self):
        old = self.source("20261104T000000Z-abcdef")
        new = self.source("20261105T000000Z-abcdef")
        for source in (old, new):
            _full, receipt = self.full(source)
            self.capsule(source, receipt)
        old_capsule = self.capsules / (old.name + "m")
        with (old_capsule / "application-mail-manifest.json").open("ab") as file:
            file.write(b" ")
        with self.assertRaisesRegex(ValueError, "manifest binding"):
            pub.prune_private_capsules(self.capsules, 1)
        self.assertTrue(old_capsule.exists())


if __name__ == "__main__":
    unittest.main()

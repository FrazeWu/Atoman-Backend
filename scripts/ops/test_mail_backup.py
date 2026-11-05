import datetime as dt
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import signal
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

mail = load("backup-mail")
exporter = load("prepare-encrypted-backup")
nas = load("nas-pull-encrypted-backups")

class MailBackupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.cache = self.root / "cache"
        self.cache.mkdir(mode=0o700)
        self.config = {"ATOMAN_MAIL_R2_ENDPOINT": "https://storage.invalid", "ATOMAN_MAIL_R2_ACCESS_KEY_ID": "fixture-access",
                       "ATOMAN_MAIL_R2_SECRET_ACCESS_KEY": "fixture-secret"}
        self.client = mail.R2(self.config)
        self.payload = b"Synthetic recovery message with attachment\x00\xff"
        self.meta = {"etag": '"fixed-etag"', "bytes": len(self.payload)}
        self.calls = []
        def fake_call(op, *args):
            self.calls.append((op, args))
            if op == "get-object":
                Path(args[-1]).write_bytes(self.payload)
            return {}
        self.client.call = fake_call

    def test_media_credentials_cannot_be_used(self):
        with self.assertRaisesRegex(ValueError, "Dedicated"):
            mail.R2({"S3_ENDPOINT": "https://storage.invalid", "AWS_ACCESS_KEY_ID": "media", "AWS_SECRET_ACCESS_KEY": "media"})
        with self.assertRaisesRegex(ValueError, "secure"):
            mail.R2({**self.config, "ATOMAN_MAIL_R2_ENDPOINT": "http://storage.invalid"})

    def test_env_parser_preserves_opaque_values_and_rejects_open_permissions(self):
        path = self.root / "config.env"
        path.write_text("# comment\nKEY='has=(opaque)$characters'\n")
        path.chmod(0o600)
        self.assertEqual(mail.parse_env([path]), {"KEY": "has=(opaque)$characters"})
        path.chmod(0o644)
        with self.assertRaises(ValueError):
            mail.parse_env([path])

    def test_object_keys_cannot_escape_cache_and_etag_is_required(self):
        path = self.client.fetch(mail.BUCKETS[0], "../../outside", self.meta, self.cache)
        self.assertEqual(path.parent, self.cache)
        self.assertEqual(path.name, mail.object_token(mail.BUCKETS[0], "../../outside", self.meta) + ".blob")
        self.assertIn("--if-match", self.calls[0][1])
        self.assertEqual(path.read_bytes(), self.payload)
        self.client.fetch(mail.BUCKETS[0], "../../outside", self.meta, self.cache)
        self.assertEqual(len(self.calls), 1)
        path.write_bytes(b"x" * len(self.payload))
        self.client.fetch(mail.BUCKETS[0], "../../outside", self.meta, self.cache)
        self.assertEqual(len(self.calls), 2)
        self.assertEqual(path.read_bytes(), self.payload)

    def test_runtime_config_symlink_is_rejected(self):
        source = self.root / "runtime"
        source.mkdir()
        (source / "linked").symlink_to("/etc/passwd")
        with self.assertRaises(ValueError):
            mail.collect_config([source])

    def make_assets(self):
        cfg = self.root / "backend.env"
        cfg.write_bytes(b"ENCRYPTION_KEY=synthetic-fixture-only\n")
        inventory = {mail.BUCKETS[0]: {"sensitive-key-path": self.meta}, mail.BUCKETS[1]: {"../attachment": self.meta}}
        record = mail.pack_assets(self.client, inventory, self.cache, [("etc/atoman-mail/backend.env", cfg)], self.root)
        return record, inventory

    def test_mail_assets_restore_bytes_without_extracting_untrusted_paths(self):
        record, _ = self.make_assets()
        self.assertTrue(record["verified"])
        self.assertEqual(mail.verify_assets(self.root / record["name"]), {"objects": 2, "runtime_files": 1})
        with tarfile.open(self.root / record["name"]) as tar:
            manifest = json.load(tar.extractfile("mail-assets-manifest.json"))
            for bucket in mail.BUCKETS:
                value = next(iter(manifest["buckets"][bucket].values()))
                self.assertEqual(tar.extractfile(value["member"]).read(), self.payload)
            self.assertEqual(tar.extractfile("runtime/etc/atoman-mail/backend.env").read(), b"ENCRYPTION_KEY=synthetic-fixture-only\n")

    def test_mail_archive_tamper_is_rejected(self):
        record, _ = self.make_assets()
        original = self.root / record["name"]
        altered = self.root / "altered.tar"
        with tarfile.open(original) as src, tarfile.open(altered, "w") as dst:
            for member in src.getmembers():
                data = src.extractfile(member).read()
                if member.name.startswith("blobs/"):
                    data = b"x" * len(data)
                dst.addfile(member, io.BytesIO(data))
        with self.assertRaisesRegex(ValueError, "checksum"):
            mail.verify_assets(altered)

    def test_quiesce_resumes_all_writers_in_reverse_order_after_failure(self):
        units = ["atoman-backend.service", "atoman-stalwart.service"]
        actions = []
        def run(command, **kwargs):
            actions.append(command)
            return subprocess.CompletedProcess(command, 0, stdout="loaded\n")
        previous = signal.getsignal(signal.SIGALRM)
        with mock.patch.object(mail.sp, "run", side_effect=run):
            with self.assertRaisesRegex(RuntimeError, "fixture failure"):
                with mail.quiesce(units, 10):
                    raise RuntimeError("fixture failure")
        self.assertEqual([a[2] for a in actions if a[1] == "stop"], units)
        self.assertEqual([a[2] for a in actions if a[1] == "start"], list(reversed(units)))
        self.assertEqual(signal.getsignal(signal.SIGALRM), previous)

    def test_unknown_writer_fails_before_stopping_any_service(self):
        with mock.patch.object(mail.sp, "run", return_value=subprocess.CompletedProcess([], 0, stdout="not-found\n")) as run:
            with self.assertRaisesRegex(RuntimeError, "not loaded"):
                with mail.quiesce(["atoman-typo.service"], 10):
                    pass
        self.assertEqual(run.call_count, 1)

    def test_cache_pruning_retains_current_objects_and_unknown_files(self):
        _, inventory = self.make_assets()
        before = {p.name for p in self.cache.iterdir()}
        (self.cache / ("a" * 64 + ".blob")).write_bytes(b"old fixture")
        (self.cache / "foreign.txt").write_bytes(b"leave alone")
        self.assertEqual(mail.prune_blob_cache(self.cache, inventory), 1)
        self.assertEqual({p.name for p in self.cache.iterdir()}, before | {"foreign.txt"})

    def test_export_requires_verified_additional_assets_and_detects_changes(self):
        record, _ = self.make_assets()
        (self.root / "COMPLETE").touch()
        (self.root / "roles.sql").write_text("-- fixtures only\n")
        dump = b"fixture dump"
        (self.root / "atoman_mail.dump").write_bytes(dump)
        manifest = {"format": "atoman-postgres-logical-v1", "complete": True,
                    "completed_at": "2026-11-05T00:00:00+00:00",
                    "backups": [{"dump": "atoman_mail.dump", "bytes": len(dump),
                                 "sha256": hashlib.sha256(dump).hexdigest(), "restore_verified": True}],
                    "additional_files": [record], "mail_assets_verified": True}
        path = self.root / "manifest.json"
        path.write_text(json.dumps(manifest))
        self.assertIn("mail-assets.tar", [n for n, _ in exporter.validate_snapshot(self.root)])
        manifest["mail_assets_verified"] = False
        path.write_text(json.dumps(manifest))
        with self.assertRaises(exporter.BackupError):
            exporter.validate_snapshot(self.root)
        manifest["mail_assets_verified"] = True
        path.write_text(json.dumps(manifest))
        (self.root / "mail-assets.tar").write_bytes(b"tamper")
        with self.assertRaises(exporter.BackupError):
            exporter.validate_snapshot(self.root)

class RetentionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "snapshots").mkdir(mode=0o700)
        self.now = dt.datetime(2026, 11, 5, tzinfo=dt.timezone.utc)

    def snapshot(self, name, age_days):
        p = self.root / "snapshots" / name
        p.mkdir(mode=0o700)
        data = b"synthetic encrypted bytes"
        (p / "backup.tar.age").write_bytes(data)
        (p / "receipt.json").write_text("{}")
        marker = {"format": "atoman-nas-copy-v1", "source": name, "bytes": len(data),
                  "sha256": hashlib.sha256(data).hexdigest(),
                  "source_completed_at": (self.now - dt.timedelta(days=age_days)).isoformat()}
        (p / "LOCAL_COMPLETE.json").write_text(json.dumps(marker))
        return p

    def test_fifteen_days_retains_recent_and_preserves_foreign_trees(self):
        old = self.snapshot("20261001T000000Z-abcdef", 35)
        current = self.snapshot("20261105T000000Z-abcdef", 0)
        boundary = self.snapshot("20261021T000000Z-abcdef", 15)
        unknown = self.root / "snapshots" / "foreign"
        unknown.mkdir()
        self.assertEqual(nas.prune_completed(self.root, 15, self.now), [old.name])
        self.assertTrue(current.exists() and boundary.exists() and unknown.exists())

    def test_newest_snapshot_survives_even_if_stale(self):
        newest = self.snapshot("20261001T000000Z-abcdef", 35)
        self.assertEqual(nas.prune_completed(self.root, 15, self.now), [])
        self.assertTrue(newest.exists())

    def test_corrupted_old_snapshot_is_not_deleted(self):
        old = self.snapshot("20261001T000000Z-abcdef", 35)
        self.snapshot("20261104T000000Z-abcdef", 1)
        self.snapshot("20261105T000000Z-abcdef", 0)
        (old / "backup.tar.age").write_bytes(b"changed")
        with self.assertRaises(nas.PullError):
            nas.prune_completed(self.root, 15, self.now)
        self.assertTrue(old.exists())

if __name__ == "__main__":
    unittest.main()

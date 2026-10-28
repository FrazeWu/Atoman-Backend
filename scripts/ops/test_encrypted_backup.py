import hashlib
import importlib.util
import io
import json
from pathlib import Path
import shutil
import stat
import subprocess as sp
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('encrypted_backup', Path(__file__).with_name('prepare-encrypted-backup.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class EncryptedBackupTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / '20261028T000000Z-abcdef'
        self.source.mkdir(mode=0o700)
        data = b'fixture-only database bytes; not production data'
        (self.source / 'example.dump').write_bytes(data)
        (self.source / 'roles.sql').write_text('-- fixture roles only\n')
        (self.source / 'COMPLETE').touch()
        self.manifest = {'format': 'atoman-postgres-logical-v1', 'complete': True, 'completed_at': '2026-10-28T00:15:00+00:00', 'backups': [{'dump': 'example.dump', 'bytes': len(data), 'sha256': hashlib.sha256(data).hexdigest(), 'restore_verified': True}]}
        self.write_manifest()

    def write_manifest(self):
        (self.source / 'manifest.json').write_text(json.dumps(self.manifest))

    def keypair(self, name='identity'):
        identity = self.root / name
        sp.run(['age-keygen', '-o', str(identity)], check=True, stdout=sp.DEVNULL, stderr=sp.DEVNULL)
        recipient = sp.check_output(['age-keygen', '-y', str(identity)], text=True).strip()
        return identity, recipient

    def test_verified_snapshot_members_are_exact(self):
        self.assertEqual([name for name, _ in module.validate_snapshot(self.source)], ['COMPLETE', 'example.dump', 'manifest.json', 'roles.sql'])

    def test_rejects_missing_completion_marker(self):
        (self.source / 'COMPLETE').unlink()
        with self.assertRaises(FileNotFoundError):
            module.validate_snapshot(self.source)

    def test_accepts_restore_verified_legacy_manifest_without_format_marker(self):
        del self.manifest['format']
        self.write_manifest()
        self.assertEqual([name for name, _ in module.validate_snapshot(self.source)],
                         ['COMPLETE', 'example.dump', 'manifest.json', 'roles.sql'])

    def test_rejects_missing_or_naive_source_timestamp(self):
        for completed in (None, '2026-10-28T00:15:00', 'not-a-timestamp'):
            with self.subTest(completed=completed):
                self.manifest['completed_at'] = completed
                self.write_manifest()
                with self.assertRaises(module.BackupError):
                    module.source_provenance(self.source)

    def test_rejects_unverified_restore(self):
        self.manifest['backups'][0]['restore_verified'] = False
        self.write_manifest()
        with self.assertRaises(module.BackupError):
            module.validate_snapshot(self.source)

    def test_rejects_changed_database(self):
        (self.source / 'example.dump').write_bytes(b'tampered')
        with self.assertRaises(module.BackupError):
            module.validate_snapshot(self.source)

    def test_rejects_path_traversal(self):
        self.manifest['backups'][0]['dump'] = '../example.dump'
        self.write_manifest()
        with self.assertRaises(module.BackupError):
            module.validate_snapshot(self.source)

    def test_rejects_symlink_payload(self):
        (self.source / 'roles.sql').unlink()
        (self.root / 'outside.sql').write_text('outside')
        (self.source / 'roles.sql').symlink_to(self.root / 'outside.sql')
        with self.assertRaises(module.BackupError):
            module.validate_snapshot(self.source)

    def test_rejects_private_keys_plugins_and_no_recipient(self):
        for recipients in [[], ['AGE-SECRET-KEY-1invalid'], ['age1plugin1invalid'], ['ssh-ed25519 invalid']]:
            with self.subTest(recipients=recipients), self.assertRaises(module.BackupError):
                module.validate_recipients(recipients)

    @unittest.skipUnless(shutil.which('age') and shutil.which('age-keygen'), 'age integration binaries unavailable')
    def test_real_encryption_restore_and_tamper_rejection(self):
        identity, recipient = self.keypair()
        output, receipt = module.prepare(self.source, self.root / 'exports', [recipient])
        ciphertext = output / receipt['ciphertext']
        self.assertFalse(receipt['offsite_verified'])
        self.assertEqual(receipt['source_snapshot_id'], self.source.name)
        self.assertEqual(receipt['source_completed_at'], '2026-10-28T00:15:00+00:00')
        self.assertEqual(receipt['sha256'], module.digest(ciphertext))
        self.assertEqual(stat.S_IMODE(output.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(ciphertext.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE((output / 'receipt.json').stat().st_mode), 0o600)
        plain = sp.check_output(['age', '--decrypt', '--identity', str(identity), str(ciphertext)])
        with tarfile.open(fileobj=io.BytesIO(plain)) as archive:
            self.assertEqual(archive.extractfile('example.dump').read(), (self.source / 'example.dump').read_bytes())
            inside = json.load(archive.extractfile('payload-manifest.json'))
            self.assertEqual(inside['files']['roles.sql']['sha256'], module.digest(self.source / 'roles.sql'))
            self.assertEqual(inside['source_snapshot_id'], self.source.name)
            self.assertEqual(inside['source_completed_at'], receipt['source_completed_at'])
        other, _ = self.keypair('other-identity')
        wrong = sp.run(['age', '--decrypt', '--identity', str(other), str(ciphertext)], stdout=sp.DEVNULL, stderr=sp.DEVNULL)
        self.assertNotEqual(wrong.returncode, 0)
        damaged = bytearray(ciphertext.read_bytes())
        damaged[-1] ^= 1
        ciphertext.write_bytes(damaged)
        rejected = sp.run(['age', '--decrypt', '--identity', str(identity), str(ciphertext)], stdout=sp.DEVNULL, stderr=sp.DEVNULL)
        self.assertNotEqual(rejected.returncode, 0)

    @unittest.skipUnless(shutil.which('age') and shutil.which('age-keygen'), 'age tools unavailable')
    def test_failed_encryption_never_leaves_a_completed_export(self):
        _, recipient = self.keypair()
        invalid = recipient[:-1] + ('q' if recipient[-1] != 'q' else 'p')
        output = self.root / 'failed-export'
        with self.assertRaises((module.BackupError, BrokenPipeError)):
            module.prepare(self.source, output, [invalid])
        self.assertEqual(list(output.iterdir()), [])

    @unittest.skipUnless(shutil.which('age-keygen'), 'age key generator unavailable')
    def test_refuses_shared_output_directory(self):
        _, recipient = self.keypair()
        target = self.root / 'shared'
        target.mkdir(mode=0o755)
        target.chmod(0o755)
        with self.assertRaises(module.BackupError):
            module.prepare(self.source, target, [recipient])
        self.assertEqual(list(target.iterdir()), [])


if __name__ == '__main__':
    unittest.main()

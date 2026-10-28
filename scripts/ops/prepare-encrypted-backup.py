#!/usr/bin/env python3
"""Prepare a verified PostgreSQL backup for off-host transfer, without uploading it.
Only public age recipients are accepted. Keep recovery identities off this host.
An encryption receipt is NOT proof of offsite storage or disaster recovery.
"""
import argparse
import datetime as dt
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess as sp
import tarfile
import tempfile


class BackupError(ValueError):
    pass


SNAPSHOT_ID = re.compile(r'^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$')


def source_provenance(source):
    if not SNAPSHOT_ID.fullmatch(source.name):
        raise BackupError('Backup source directory does not have a snapshot identity')
    manifest = json.loads(regular_file(source, 'manifest.json').read_text())
    raw = manifest.get('completed_at')
    if not isinstance(raw, str):
        raise BackupError('Snapshot completion time is missing')
    try:
        timestamp = dt.datetime.fromisoformat(raw)
    except ValueError as error:
        raise BackupError('Snapshot completion time is invalid') from error
    if timestamp.tzinfo is None:
        raise BackupError('Snapshot completion time must include a timezone')
    completed = timestamp.astimezone(dt.timezone.utc)
    if completed > dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=1):
        raise BackupError('Snapshot completion time is unexpectedly in the future')
    return {'source_snapshot_id': source.name,
            'source_completed_at': completed.isoformat()}


def digest(file):
    with file.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def regular_file(directory, name):
    if not name or Path(name).name != name or name in ('.', '..'):
        raise BackupError('Unsafe backup member name')
    file = directory / name
    if not stat.S_ISREG(file.lstat().st_mode):
        raise BackupError('Backup members must be regular files, not links')
    return file


def validate_snapshot(source):
    if source.is_symlink() or not source.is_dir():
        raise BackupError('Backup source must be a real directory')
    regular_file(source, 'COMPLETE')
    manifest_file = regular_file(source, 'manifest.json')
    if manifest_file.stat().st_size > 4 * 1024 * 1024:
        raise BackupError('Manifest too large')
    manifest = json.loads(manifest_file.read_text())
    # Snapshots predating the format marker remain valid only when all individual
    # dump records are restore-verified and their bytes match their checksums.
    if manifest.get('format') not in (None, 'atoman-postgres-logical-v1') or manifest.get('complete') is not True:
        raise BackupError('Backup is incomplete or has an unsupported format')
    records = manifest.get('backups')
    if not isinstance(records, list) or not records:
        raise BackupError('Missing database backup records')
    members = ['COMPLETE', 'manifest.json', 'roles.sql']
    for record in records:
        if record.get('restore_verified') is not True:
            raise BackupError('Only independently restore-verified snapshots can be exported')
        name = record.get('dump', '')
        if not name.endswith('.dump') or name in members:
            raise BackupError('Duplicate or invalid archive member')
        file = regular_file(source, name)
        if file.stat().st_size != record.get('bytes') or digest(file) != record.get('sha256'):
            raise BackupError('Backup archive size or checksum mismatch')
        members.append(name)
    return [(name, regular_file(source, name)) for name in sorted(members)]


def validate_recipients(recipients):
    if not recipients or len(set(recipients)) != len(recipients):
        raise BackupError('At least one unique public recipient is required')
    for recipient in recipients:
        if not re.fullmatch(r'age1[023456789acdefghjklmnpqrstuvwxyz]{58}', recipient):
            raise BackupError('Use a native age public recipient; secret keys and plugins are rejected')


def prepare(source, output_root, recipients):
    validate_recipients(recipients)
    members = validate_snapshot(source)
    provenance = source_provenance(source)
    if shutil.which('age') is None:
        raise BackupError('The age encryption tool is required')
    if output_root.is_symlink():
        raise BackupError('Output directory may not be a symlink')
    output_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    if output_root.stat().st_uid != os.geteuid() or stat.S_IMODE(output_root.stat().st_mode) & 0o077:
        raise BackupError('Output directory must be private (mode 0700)')
    required_bytes = sum(file.stat().st_size for _, file in members)
    if shutil.disk_usage(output_root).free < required_bytes + (64 << 20):
        raise BackupError('Not enough disk space for ciphertext')
    output = Path(tempfile.mkdtemp(prefix='encrypted-' + dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ-'), dir=output_root))
    partial = output / 'backup.tar.age.part'
    process = None
    try:
        with partial.open('xb') as destination, tempfile.TemporaryFile() as errors:
            os.chmod(partial, 0o600)
            command = ['age', '--encrypt']
            for recipient in recipients:
                command += ['--recipient', recipient]
            process = sp.Popen(command, stdin=sp.PIPE, stdout=destination, stderr=errors)
            try:
                with tarfile.open(fileobj=process.stdin, mode='w|', format=tarfile.PAX_FORMAT) as archive:
                    inner = {'format': 'atoman-encrypted-payload-v1', **provenance, 'files': {name: {'sha256': digest(file), 'bytes': file.stat().st_size} for name, file in members}}
                    data = json.dumps(inner, sort_keys=True).encode()
                    info = tarfile.TarInfo('payload-manifest.json')
                    info.size, info.mode = len(data), 0o600
                    archive.addfile(info, io.BytesIO(data))
                    for name, file in members:
                        # No directory recursion, symlink following, or plaintext tar staging.
                        info = archive.gettarinfo(str(file), arcname=name)
                        info.uid = info.gid = 0
                        info.uname = info.gname = ''
                        info.mode = 0o600
                        with file.open('rb') as stream:
                            archive.addfile(info, stream)
            finally:
                process.stdin.close()
            if process.wait(timeout=300) != 0:
                raise BackupError('Encryption failed; no transferable bundle was committed')
            destination.flush()
            os.fsync(destination.fileno())
        ciphertext = output / 'backup.tar.age'
        partial.rename(ciphertext)
        receipt = {'format': 'atoman-encrypted-export-v1', 'created_at': dt.datetime.now(dt.timezone.utc).isoformat(), **provenance, 'ciphertext': ciphertext.name, 'bytes': ciphertext.stat().st_size, 'sha256': digest(ciphertext), 'recipient_fingerprints': [hashlib.sha256(r.encode()).hexdigest() for r in recipients], 'offsite_verified': False}
        receipt_file = output / 'receipt.json'
        receipt_file.write_text(json.dumps(receipt, indent=2) + '\n')
        os.chmod(receipt_file, 0o600)
        return output, receipt
    except BaseException:
        if process is not None and process.poll() is None:
            process.kill()
            process.wait()
        shutil.rmtree(output)
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--output-directory', type=Path, required=True)
    parser.add_argument('--recipient', action='append', required=True, help='Native age PUBLIC key; never supply a recovery identity')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('Run as root to read the private database archives')
    os.umask(0o077)
    def interrupted(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    # Use the backup producer's lock so retention cannot remove a source mid-read.
    with open('/run/lock/atoman-backup.lock', 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        output, receipt = prepare(args.source, args.output_directory, args.recipient)
    print(json.dumps({'bundle_directory': str(output), 'ciphertext_sha256': receipt['sha256'], 'offsite_verified': False}))


if __name__ == '__main__':
    main()

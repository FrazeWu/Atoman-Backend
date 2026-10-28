#!/usr/bin/env python3
"""Publish a restore-verified Atoman snapshot as read-only SFTP ciphertext."""
import argparse
import fcntl
import grp
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import tempfile

SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$")
RECEIPT = "receipt.json"
CIPHERTEXT = "backup.tar.age"


def checksum(path):
    with path.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def load_exporter(path):
    spec = importlib.util.spec_from_file_location("atoman_encrypted_export", path)
    exporter = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(exporter)
    return exporter


def validate_export(folder):
    if folder.is_symlink() or not folder.is_dir():
        raise ValueError("Export directory absent or unsafe")
    receipt_file = folder / RECEIPT
    ciphertext = folder / CIPHERTEXT
    for file in (receipt_file, ciphertext):
        if not stat.S_ISREG(file.lstat().st_mode):
            raise ValueError("Export member is not a regular file")
    if receipt_file.stat().st_size > 1024 * 1024:
        raise ValueError("Oversize receipt")
    receipt = json.loads(receipt_file.read_text())
    if (receipt.get("format") != "atoman-encrypted-export-v1" or
            receipt.get("ciphertext") != CIPHERTEXT or
            receipt.get("offsite_verified") is not False):
        raise ValueError("Unexpected export receipt")
    if ciphertext.stat().st_size != receipt.get("bytes") or checksum(ciphertext) != receipt.get("sha256"):
        raise ValueError("Published ciphertext fails integrity check")
    return receipt


def select_latest_verified(source_root, exporter):
    candidates = sorted((p for p in source_root.iterdir()
                         if p.is_dir() and not p.is_symlink() and SNAPSHOT.fullmatch(p.name)),
                        key=lambda p: p.name, reverse=True)
    for source in candidates:
        if (source / "COMPLETE").is_file():
            exporter.validate_snapshot(source)
            return source
    raise ValueError("No completed, independently restore-verified backup")


def publish(prepared, export_root, snapshot_id, gid):
    if not SNAPSHOT.fullmatch(snapshot_id):
        raise ValueError("Invalid backup snapshot ID")
    if export_root.is_symlink() or not export_root.is_dir():
        raise ValueError("Missing or unsafe SFTP export root")
    if export_root.stat().st_uid != os.geteuid():
        raise ValueError("SFTP export root must be owned by the publisher")
    dest = export_root / snapshot_id
    if dest.exists():
        validate_export(dest)
        return dest, "already-published"
    validate_export(prepared)
    stage = Path(tempfile.mkdtemp(prefix=".partial-", dir=export_root))
    try:
        for name in (RECEIPT, CIPHERTEXT):
            out = stage / name
            with (prepared / name).open("rb") as reader, out.open("xb") as writer:
                shutil.copyfileobj(reader, writer, 8 << 20)
                writer.flush()
                os.fsync(writer.fileno())
            os.chown(out, 0, gid)
            os.chmod(out, 0o640)
        validate_export(stage)
        os.chown(stage, 0, gid)
        os.chmod(stage, 0o750)
        # This script holds the dedicated publisher lock; there is one writer.
        os.rename(stage, dest)
        return dest, "published"
    finally:
        if stage.exists():
            shutil.rmtree(stage)


def prune_local_sftp_cache(export_root, keep):
    """Prune only local published ciphertext, never NAS copies or DB snapshots."""
    if keep < 2:
        raise ValueError("Retain at least two published snapshots")
    managed = sorted((p for p in export_root.iterdir()
                      if p.is_dir() and not p.is_symlink() and SNAPSHOT.fullmatch(p.name)),
                     key=lambda p: p.name, reverse=True)
    for old in managed[keep:]:
        # Fail closed: never delete files we did not create and cannot validate.
        validate_export(old)
        if {p.name for p in old.iterdir()} != {RECEIPT, CIPHERTEXT}:
            raise ValueError("Unexpected cache members; aborting pruning")
        shutil.rmtree(old)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backup-root", type=Path, default=Path("/var/backups/atoman/postgres"))
    parser.add_argument("--staging-root", type=Path, default=Path("/var/backups/atoman/encrypted-export-staging"))
    parser.add_argument("--export-root", type=Path, default=Path("/srv/atoman-backup-sftp/exports"))
    parser.add_argument("--exporter", type=Path, default=Path("/usr/local/libexec/atoman/prepare-encrypted-backup.py"))
    parser.add_argument("--recipient", required=True)
    parser.add_argument("--keep", type=int, default=10)
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("Publisher must run as root to read private snapshots")
    os.umask(0o077)
    exporter = load_exporter(args.exporter)
    exporter.validate_recipients([args.recipient])
    gid = grp.getgrnam("atoman_backupread").gr_gid
    with open("/run/lock/atoman-backup.lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with open("/run/lock/atoman-offsite-publish.lock", "a") as pub_lock:
            fcntl.flock(pub_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            source = select_latest_verified(args.backup_root, exporter)
            dest = args.export_root / source.name
            if dest.exists():
                validate_export(dest)
                print(json.dumps({"snapshot": source.name, "status": "already-published"}))
                return
            prepared, _receipt = exporter.prepare(source, args.staging_root, [args.recipient])
            try:
                published, status = publish(prepared, args.export_root, source.name, gid)
                validate_export(published)
                prune_local_sftp_cache(args.export_root, args.keep)
            finally:
                shutil.rmtree(prepared)
            print(json.dumps({"snapshot": source.name, "status": status, "local_sftp_cache_retained": args.keep}))


if __name__ == "__main__":
    main()

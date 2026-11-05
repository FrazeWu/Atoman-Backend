#!/usr/bin/env python3
"""Publish a same-source full backup and complete-mail capsule under both locks.

The unchanged NAS pull sorts legal export IDs in reverse order. Appending "m"
puts the small capsule before its paired full ciphertext. Publication is local
evidence only; no NAS arrival or remote recovery is inferred.
"""
import argparse
import fcntl
import grp
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat

SCOPES = {"pre_activation_mail_capsule", "post_activation_mail_capsule"}
HASH = re.compile(r"^[0-9a-f]{64}$")
PRIVATE_MEMBERS = {
    "backup.tar.age", "receipt.json", "capsule-manifest.json",
    "application-mail-manifest.json", "COMPLETE",
}


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


publisher = load(Path(__file__).with_name("publish-offsite-backups.py"), "atoman_mail_base_publisher")


def read_json(path):
    if not stat.S_ISREG(path.lstat().st_mode) or path.stat().st_size > 1024 * 1024:
        raise ValueError("Publication metadata must be a bounded regular file")
    return json.loads(path.read_text())


def owned_root(root, private=False):
    if root.is_symlink() or not root.is_dir() or root.stat().st_uid != os.geteuid():
        raise ValueError("Publication root must be owned and not a symlink")
    if private and stat.S_IMODE(root.stat().st_mode) & 0o077:
        raise ValueError("Capsule cache root must remain private")


def receipt_scope(receipt, folder):
    scope = receipt.get("backup_scope", "full")
    if scope not in {"full", "mail-complete"}:
        return None
    if scope == "mail-complete":
        source = receipt.get("paired_source_snapshot_id")
        if (not isinstance(source, str) or not publisher.SNAPSHOT.fullmatch(source)
                or receipt.get("source_snapshot_id") != source + "m" or folder.name != source + "m"
                or receipt.get("scope") not in SCOPES):
            raise ValueError("Capsule export identity or scope differs")
    return scope


def validate_full(source, folder):
    manifest = read_json(source / "manifest.json")
    receipt = publisher.validate_export(folder)
    if (receipt_scope(receipt, folder) != "full"
            or receipt.get("source_snapshot_id") != source.name
            or receipt.get("source_completed_at") != manifest.get("completed_at")):
        raise ValueError("Full export differs from the selected immutable source")
    if ("source_manifest_sha256" in receipt
            and receipt["source_manifest_sha256"] != publisher.checksum(source / "manifest.json")):
        raise ValueError("Full export source manifest differs")
    return receipt


def validate_capsule_binding(source, full_receipt, folder, scope):
    receipt = publisher.validate_export(folder)
    manifest = read_json(source / "manifest.json")
    if (receipt_scope(receipt, folder) != "mail-complete"
            or receipt.get("paired_source_snapshot_id") != source.name
            or receipt.get("scope") != scope
            or receipt.get("source_completed_at") != manifest.get("completed_at")
            or receipt.get("source_manifest_sha256") != publisher.checksum(source / "manifest.json")
            or receipt.get("source_full_ciphertext_sha256") != full_receipt["sha256"]
            or receipt.get("source_full_ciphertext_bytes") != full_receipt["bytes"]
            or not HASH.fullmatch(str(receipt.get("capsule_manifest_sha256", "")))):
        raise ValueError("Capsule provenance differs from its source or full ciphertext")
    return receipt


def validate_private_capsule(folder):
    """Delete only a complete, known, authenticated private cache entry."""
    if ({member.name for member in folder.iterdir()} != PRIVATE_MEMBERS
            or folder.stat().st_uid != os.geteuid()):
        raise ValueError("Unexpected private capsule cache members")
    for member in folder.iterdir():
        info = member.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid()
                or stat.S_IMODE(info.st_mode) & 0o077):
            raise ValueError("Private capsule member is unsafe")
    receipt = publisher.validate_export(folder)
    if receipt_scope(receipt, folder) != "mail-complete":
        raise ValueError("Private entry is not a mail capsule")
    capsule = read_json(folder / "capsule-manifest.json")
    application = read_json(folder / "application-mail-manifest.json")
    source = receipt["paired_source_snapshot_id"]
    if (publisher.checksum(folder / "capsule-manifest.json") != receipt.get("capsule_manifest_sha256")
            or capsule.get("format") != "atoman-mail-recovery-capsule-v1"
            or capsule.get("capsule_id") != folder.name
            or capsule.get("source_snapshot_id") != source
            or capsule.get("scope") != receipt["scope"]
            or capsule.get("source_manifest_sha256") != receipt.get("source_manifest_sha256")
            or capsule.get("source_full_ciphertext_sha256") != receipt.get("source_full_ciphertext_sha256")
            or capsule.get("source_full_ciphertext_bytes") != receipt.get("source_full_ciphertext_bytes")
            or publisher.checksum(folder / "application-mail-manifest.json") != capsule.get("application_manifest_sha256")
            or application.get("source_snapshot_id") != source
            or application.get("source_dump_sha256") != capsule.get("atoman_prod_dump_sha256")
            or application.get("source_tables") != capsule.get("application_source_tables")
            or application.get("absent_mail_tables") != capsule.get("application_absent_mail_tables")):
        raise ValueError("Private capsule manifest binding differs")
    return receipt


def managed_folders(root, *, private=False):
    owned_root(root, private=private)
    allowed = PRIVATE_MEMBERS if private else {"receipt.json", "backup.tar.age"}
    for folder in root.iterdir():
        if (folder.is_symlink() or not folder.is_dir()
                or not publisher.SNAPSHOT.fullmatch(folder.name)
                or folder.stat().st_uid != os.geteuid()):
            continue
        # Unrecognized or incomplete directories are preserved, never swept.
        if {member.name for member in folder.iterdir()} != allowed:
            continue
        receipt = read_json(folder / "receipt.json")
        if receipt.get("format") != "atoman-encrypted-export-v1":
            continue
        scope = receipt_scope(receipt, folder)
        if scope is not None and (not private or scope == "mail-complete"):
            yield folder, scope


def validate_keep(keep):
    if isinstance(keep, bool) or not isinstance(keep, int) or not 1 <= keep <= 365:
        raise ValueError("Retain between one and 365 entries per scope")


def prune_scoped_exports(root, keep=1):
    validate_keep(keep)
    groups = {"full": [], "mail-complete": []}
    for folder, scope in managed_folders(root):
        groups[scope].append(folder)
    removed = []
    for scope, folders in groups.items():
        for folder in sorted(folders, key=lambda item: item.name, reverse=True)[keep:]:
            publisher.validate_export(folder)
            for member in folder.iterdir():
                if member.lstat().st_uid != os.geteuid():
                    raise ValueError("Published cache member is not owned")
            shutil.rmtree(folder)
            removed.append({"snapshot": folder.name, "scope": scope})
    return removed


def prune_private_capsules(root, keep=1):
    validate_keep(keep)
    folders = [folder for folder, _scope in managed_folders(root, private=True)]
    removed = []
    for folder in sorted(folders, key=lambda item: item.name, reverse=True)[keep:]:
        validate_private_capsule(folder)
        shutil.rmtree(folder)
        removed.append(folder.name)
    return removed


def publish_latest(backup_root, staging_root, export_root, capsule_root, recipient,
                   keep, gid, scope, exporter, builder):
    """Caller holds the backup and offsite-publisher exclusive locks."""
    validate_keep(keep)
    if scope not in SCOPES:
        raise ValueError("Unsupported complete-mail capsule scope")
    exporter.validate_recipients([recipient])
    source = publisher.select_latest_verified(backup_root, exporter)
    full = export_root / source.name
    full_status = "already-published"
    if full.exists():
        full_receipt = validate_full(source, full)
    else:
        prepared, _receipt = exporter.prepare(source, staging_root, [recipient])
        try:
            if prepared.parent.resolve() != staging_root.resolve():
                raise ValueError("Full export staging escaped its private root")
            full, full_status = publisher.publish(prepared, export_root, source.name, gid)
            full_receipt = validate_full(source, full)
        finally:
            if prepared.parent.resolve() == staging_root.resolve() and prepared.is_dir() and not prepared.is_symlink():
                shutil.rmtree(prepared)
    # Even a previously published full export still needs its matching capsule.
    capsule_root.mkdir(parents=True, exist_ok=True, mode=0o700)
    owned_root(capsule_root, private=True)
    builder.build(source, full, capsule_root, recipient, scope)
    prepared_capsule = capsule_root / (source.name + "m")
    validate_private_capsule(prepared_capsule)
    validate_capsule_binding(source, full_receipt, prepared_capsule, scope)
    capsule, capsule_status = publisher.publish(
        prepared_capsule, export_root, source.name + "m", gid)
    validate_capsule_binding(source, full_receipt, capsule, scope)
    pruned = prune_scoped_exports(export_root, keep)
    private_pruned = prune_private_capsules(capsule_root, keep)
    return {
        "source_snapshot_id": source.name, "full_export_snapshot_id": full.name,
        "capsule_export_snapshot_id": capsule.name, "scope": scope,
        "full_status": full_status, "capsule_status": capsule_status,
        "local_sftp_cache_retained_per_scope": keep, "private_capsules_retained": keep,
        "pruned_publications": pruned, "pruned_private_capsules": private_pruned,
        "encrypted_publication_ready": True, "mail_capsule_publication_ready": True,
        "offsite_transfer_confirmed": False,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backup-root", type=Path, default=Path("/var/backups/atoman/mail"))
    parser.add_argument("--staging-root", type=Path, default=Path("/var/backups/atoman/encrypted-export-staging"))
    parser.add_argument("--export-root", type=Path, default=Path("/srv/atoman-backup-sftp/exports"))
    parser.add_argument("--capsule-root", type=Path, default=Path("/var/backups/atoman/mail-capsules"))
    parser.add_argument("--exporter", type=Path, default=Path(__file__).with_name("prepare-encrypted-backup.py"))
    parser.add_argument("--capsule-builder", type=Path, default=Path(__file__).with_name("prepare-mail-recovery-capsule.py"))
    parser.add_argument("--recipient", required=True)
    parser.add_argument("--keep", type=int, default=1)
    parser.add_argument("--scope", choices=sorted(SCOPES), default="post_activation_mail_capsule")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("Publisher must run as root to read private snapshots")
    os.umask(0o077)

    def interrupted(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGINT, interrupted)
    signal.signal(signal.SIGTERM, interrupted)
    try:
        exporter = publisher.load_exporter(args.exporter)
        builder = load(args.capsule_builder, "atoman_mail_capsule_builder")
        gid = grp.getgrnam("atoman_backupread").gr_gid
        with open("/run/lock/atoman-backup.lock", "a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with open("/run/lock/atoman-offsite-publish.lock", "a") as pub_lock:
                fcntl.flock(pub_lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                result = publish_latest(
                    args.backup_root, args.staging_root, args.export_root,
                    args.capsule_root, args.recipient, args.keep, gid, args.scope, exporter, builder)
        print(json.dumps(result, sort_keys=True))
    except Exception as error:
        # The builder may handle sensitive archives; never relay command errors.
        print(json.dumps({"status": "error", "error_type": type(error).__name__}))
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()

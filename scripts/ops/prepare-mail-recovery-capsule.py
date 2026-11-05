#!/usr/bin/env python3
"""Build an age-encrypted complete-mail capsule from one immutable paired backup."""
import argparse
import datetime as dt
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import sys
import subprocess as sp
import tarfile
import tempfile

SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$")
MAIL_TABLES = {"mail_accounts", "mail_invites", "mail_access_credentials",
               "mail_pending_sends", "mail_drafts", "mail_uploads",
               "mail_preferences", "mail_action_receipts", "mail_retention"}

def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module

def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()

def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()

def record(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError("Capsule members must be regular files")
    return {"bytes": path.stat().st_size, "sha256": digest(path)}

def validate_file(path, expected):
    actual = record(path)
    if actual != {"bytes": expected["bytes"], "sha256": expected["sha256"]}:
        raise ValueError("Immutable paired source member differs")
    return actual

def write_json(path, value):
    path.write_bytes(canonical(value))
    path.chmod(0o600)

def build(source, full_export, destination, recipient, scope):
    mail = load("backup-mail")
    mail.private_dir(destination)
    if not SNAPSHOT.fullmatch(source.name) or not (source / "COMPLETE").is_file():
        raise ValueError("A completed immutable paired source is required")
    capsule_id = source.name + "m"
    if not SNAPSHOT.fullmatch(capsule_id):
        raise ValueError("Derived capsule export identity is too long")
    final = destination / capsule_id
    if final.exists():
        if final.is_symlink() or not (final / "COMPLETE").is_file():
            raise ValueError("An incomplete capsule destination already exists")
        receipt = json.loads((final / "receipt.json").read_text())
        validate_file(final / "backup.tar.age", receipt)
        original_export = json.loads((full_export / "receipt.json").read_text())
        if (receipt.get("paired_source_snapshot_id") != source.name or receipt.get("scope") != scope
                or receipt.get("source_manifest_sha256") != digest(source / "manifest.json")
                or original_export.get("source_snapshot_id") != source.name
                or receipt.get("source_full_ciphertext_sha256") != original_export.get("sha256")
                or receipt.get("source_full_ciphertext_bytes") != original_export.get("bytes")
                or receipt.get("capsule_manifest_sha256") != digest(final / "capsule-manifest.json")):
            raise ValueError("Existing capsule provenance differs")
        return receipt
    manifest = json.loads((source / "manifest.json").read_text())
    if manifest.get("complete") is not True or manifest.get("mail_assets_verified") is not True:
        raise ValueError("Unverified paired source")
    source_manifest_sha = digest(source / "manifest.json")
    dbs = {row["database"]: row for row in manifest["backups"]}
    if set(dbs) != {"atoman_prod", "atoman_dev", "atoman_mail"} or not all(r.get("restore_verified") is True for r in dbs.values()):
        raise ValueError("All paired source databases must already have passed restore")
    expected_tables = {k: v for k, v in dbs["atoman_prod"]["public_table_counts"].items()
                       if k == "Users" or k.startswith("mail_")}
    if "Users" not in expected_tables:
        raise ValueError("Application owner identities are missing")
    absent = sorted(MAIL_TABLES - expected_tables.keys())
    if scope == "post_activation_mail_capsule" and absent:
        raise ValueError("Post-activation source is missing required application mail tables")
    full_receipt = json.loads((full_export / "receipt.json").read_text())
    if (full_receipt.get("format") != "atoman-encrypted-export-v1"
            or full_receipt.get("source_snapshot_id") != source.name
            or full_receipt.get("source_completed_at") != manifest["completed_at"]):
        raise ValueError("Full encrypted export does not belong to this paired source")
    validate_file(full_export / "backup.tar.age", full_receipt)
    mail_record = dbs["atoman_mail"]
    validate_file(source / "atoman_mail.dump", mail_record)
    assets_record = next(r for r in manifest["additional_files"] if r["name"] == "mail-assets.tar")
    validate_file(source / "mail-assets.tar", assets_record)
    mail.verify_assets(source / "mail-assets.tar")
    with tarfile.open(source / "mail-assets.tar") as archive:
        asset_manifest = json.load(archive.extractfile("mail-assets-manifest.json"))
    if set(asset_manifest["buckets"]) != set(mail.BUCKETS):
        raise ValueError("Both complete mail bucket inventories are required")
    with tempfile.TemporaryDirectory(prefix=".mail-capsule-build-", dir=destination) as temporary:
        working = Path(temporary)
        appdir = working / "application"
        appdir.mkdir(mode=0o700)
        load("mail-capsule-application").build(source, appdir)
        app_dump = appdir / "application-mail.sql"
        app_manifest = appdir / "application-mail-manifest.json"
        if not app_dump.is_file() or not app_manifest.is_file():
            raise ValueError("Application mail capsule artifacts are incomplete")
        app_meta = json.loads(app_manifest.read_text())
        capsule = {
            "format": "atoman-mail-recovery-capsule-v1", "scope": scope,
            "capsule_id": capsule_id, "source_snapshot_id": source.name,
            "source_completed_at": manifest["completed_at"],
            "source_manifest_sha256": source_manifest_sha,
            "source_full_ciphertext_sha256": full_receipt["sha256"],
            "source_full_ciphertext_bytes": full_receipt["bytes"],
            "atoman_prod_dump_sha256": dbs["atoman_prod"]["sha256"],
            "atoman_mail_dump_sha256": mail_record["sha256"],
            "mail_assets_sha256": assets_record["sha256"],
            "application_payload_sha256": digest(app_dump),
            "application_manifest_sha256": digest(app_manifest),
            "application_source_tables": expected_tables,
            "application_absent_mail_tables": absent,
            "r2_inventory_sha256": hashlib.sha256(canonical(asset_manifest["buckets"])).hexdigest(),
            "r2_buckets": sorted(asset_manifest["buckets"]),
            "r2_object_counts": {bucket: len(rows) for bucket, rows in asset_manifest["buckets"].items()},
            "runtime_files_sha256": hashlib.sha256(canonical(asset_manifest["runtime_files"])).hexdigest(),
            "runtime_file_count": len(asset_manifest["runtime_files"]),
        }
        capsule_file = working / "capsule-manifest.json"
        write_json(capsule_file, capsule)
        files = {"manifest.json": source / "manifest.json",
                 "atoman_mail.dump": source / "atoman_mail.dump",
                 "mail-assets.tar": source / "mail-assets.tar",
                 "application-mail.sql": app_dump,
                 "application-mail-manifest.json": app_manifest,
                 "capsule-manifest.json": capsule_file}
        payload = {"format": "atoman-encrypted-payload-v1",
                   "source_snapshot_id": capsule_id,
                   "paired_source_snapshot_id": source.name,
                   "source_completed_at": manifest["completed_at"],
                   "backup_scope": "mail-complete",
                   "files": {name: record(path) for name, path in files.items()}}
        publication = working / "publication"
        publication.mkdir(mode=0o700)
        ciphertext = publication / "backup.tar.age"
        with tempfile.TemporaryFile() as errors:
            process = sp.Popen(["age", "--encrypt", "--recipient", recipient, "--output", str(ciphertext)],
                               stdin=sp.PIPE, stderr=errors)
            try:
                with tarfile.open(fileobj=process.stdin, mode="w|") as archive:
                    body = canonical(payload)
                    item = tarfile.TarInfo("payload-manifest.json")
                    item.size, item.mode, item.mtime = len(body), 0o600, 0
                    archive.addfile(item, io.BytesIO(body))
                    for name, path in files.items():
                        archive.add(path, arcname=name, recursive=False)
                process.stdin.close()
                if process.wait(timeout=120):
                    raise RuntimeError("Private capsule encryption failed")
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait()
        ciphertext.chmod(0o600)
        receipt = {"format": "atoman-encrypted-export-v1",
                   "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                   "source_snapshot_id": capsule_id,
                   "paired_source_snapshot_id": source.name,
                   "source_completed_at": manifest["completed_at"],
                   "backup_scope": "mail-complete", "scope": scope,
                   "ciphertext": "backup.tar.age", **record(ciphertext),
                   "source_manifest_sha256": source_manifest_sha,
                   "source_full_ciphertext_sha256": full_receipt["sha256"],
                   "source_full_ciphertext_bytes": full_receipt["bytes"],
                   "capsule_manifest_sha256": digest(capsule_file),
                   "offsite_verified": False}
        write_json(publication / "receipt.json", receipt)
        shutil.copyfile(capsule_file, publication / "capsule-manifest.json")
        shutil.copyfile(app_manifest, publication / "application-mail-manifest.json")
        for path in publication.iterdir():
            path.chmod(0o600)
        (publication / "COMPLETE").write_text("mail capsule encrypted; NAS recovery pending\n")
        (publication / "COMPLETE").chmod(0o600)
        publication.rename(final)
    return receipt

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--full-export", type=Path, required=True)
    parser.add_argument("--destination", type=Path, required=True)
    parser.add_argument("--recipient", required=True)
    parser.add_argument("--scope", choices=["pre_activation_mail_capsule", "post_activation_mail_capsule"], required=True)
    args = parser.parse_args()
    os.umask(0o077)
    receipt = build(args.source, args.full_export, args.destination, args.recipient, args.scope)
    print(json.dumps({k: receipt[k] for k in ["source_snapshot_id", "paired_source_snapshot_id", "scope", "bytes", "sha256", "capsule_manifest_sha256"]}))

if __name__ == "__main__":
    main()

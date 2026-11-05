#!/usr/bin/env python3
"""Verify a complete mail capsule actually returned from the NAS."""
import argparse
import datetime as dt
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sys
import tarfile
import tempfile

MAIL_TABLES = {"mail_accounts", "mail_invites", "mail_access_credentials",
               "mail_pending_sends", "mail_drafts", "mail_uploads",
               "mail_preferences", "mail_action_receipts", "mail_retention"}
SELECTED = {"manifest.json", "atoman_mail.dump", "mail-assets.tar",
            "application-mail.sql", "application-mail-manifest.json", "capsule-manifest.json"}

def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module

def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()

def digest(path):
    with path.open("rb") as reader:
        return hashlib.file_digest(reader, "sha256").hexdigest()

def verify_marker(marker, source_id, checksum, size):
    if (marker.get("format") != "atoman-nas-copy-v1" or marker.get("source") != source_id
            or marker.get("sha256") != checksum or marker.get("bytes") != size
            or marker.get("ciphertext_transferred") is not True
            or marker.get("source_snapshot_id", source_id) != source_id):
        raise ValueError("Actual NAS completion marker does not match its ciphertext")

def verify_main_witness(witness, capsule):
    source_id = capsule["source_snapshot_id"]
    checksum, size = capsule["source_full_ciphertext_sha256"], capsule["source_full_ciphertext_bytes"]
    if (witness.get("source_snapshot_id") != source_id or witness.get("sha256") != checksum
            or witness.get("bytes") != size or witness.get("mac_full_ciphertext_readback") is not False):
        raise ValueError("Main snapshot NAS observation has incorrect provenance")
    if witness.get("status") == "pending":
        if (type(witness.get("partial_bytes")) is not int or not 0 <= witness["partial_bytes"] <= size
                or not isinstance(witness.get("observed_at"), str)):
            raise ValueError("Pending full snapshot evidence is incomplete")
        return False
    if witness.get("status") != "verified":
        raise ValueError("Unknown full snapshot NAS evidence state")
    receipt, marker, status = witness["receipt"], witness["copy_marker"], witness["pull_status"]
    if (receipt.get("source_snapshot_id") != source_id or receipt.get("sha256") != checksum
            or receipt.get("bytes") != size or status.get("state") != "ok"
            or status.get("source_snapshot_id") != source_id):
        raise ValueError("Main snapshot NAS verification is not supported by its actual receipts")
    verify_marker(marker, source_id, checksum, size)
    return True

def main():
    recovery = load("verify-mail-recovery")
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("ciphertext", "receipt", "nas-transfer-receipt", "main-nas-witness", "identity", "workspace", "report"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--postgres-image", default=recovery.PG_IMAGE)
    parser.add_argument("--minio-image", default=recovery.MINIO_IMAGE)
    parser.add_argument("--mc-image", default="minio/mc@sha256:a7fe349ef4bd8521fb8497f55c6042871b2ae640607cf99d9bede5e9bdf11727")
    args = parser.parse_args()
    os.umask(0o077)
    mail = load("backup-mail")
    mail.private_dir(args.workspace)
    if args.identity.is_symlink() or not args.identity.is_file() or args.identity.stat().st_mode & 0o077:
        raise ValueError("Owner recovery identity must remain a private local regular file")
    receipt = json.loads(args.receipt.read_text())
    capsule_id, source_id = receipt.get("source_snapshot_id", ""), receipt.get("paired_source_snapshot_id", "")
    if (not recovery.SNAPSHOT.fullmatch(source_id) or capsule_id != source_id + "m"
            or receipt.get("format") != "atoman-encrypted-export-v1"
            or receipt.get("backup_scope") != "mail-complete"):
        raise ValueError("This is not a source-bound complete mail capsule")
    if args.ciphertext.is_symlink() or not args.ciphertext.is_file():
        raise ValueError("NAS ciphertext must be a regular file")
    checksum, size = digest(args.ciphertext), args.ciphertext.stat().st_size
    if checksum != receipt.get("sha256") or size != receipt.get("bytes"):
        raise ValueError("Actual NAS capsule bytes differ from the receipt")
    transfer = json.loads(args.nas_transfer_receipt.read_text())
    if (transfer.get("format") != "atoman-mail-nas-transfer-v1"
            or transfer.get("source_snapshot_id") != capsule_id
            or transfer.get("ciphertext_sha256") != checksum
            or transfer.get("ciphertext_bytes") != size
            or transfer.get("nas_roundtrip_verified") is not True
            or transfer.get("atomic_publication_verified") is not True):
        raise ValueError("Complete actual NAS roundtrip evidence is required")
    marker = json.loads(args.receipt.with_name("LOCAL_COMPLETE.json").read_text())
    verify_marker(marker, capsule_id, checksum, size)
    if shutil.disk_usage(args.workspace).free < 2 * size + (1 << 30):
        raise ValueError("Insufficient private recovery workspace")
    with tempfile.TemporaryDirectory(prefix="mail-capsule-recovery-", dir=args.workspace) as temporary:
        workspace = Path(temporary)
        source = workspace / "decrypted"
        source.mkdir(mode=0o700)
        provenance, files = recovery.decrypt_selected(args.ciphertext, args.identity, source, SELECTED)
        if (set(files) != SELECTED or provenance.get("source_snapshot_id") != capsule_id
                or provenance.get("paired_source_snapshot_id") != source_id
                or provenance.get("backup_scope") != "mail-complete"
                or provenance.get("source_completed_at") != receipt.get("source_completed_at")):
            raise ValueError("Decrypted capsule provenance differs from its NAS receipt")
        capsule_file = source / "capsule-manifest.json"
        capsule = json.loads(capsule_file.read_text())
        if (capsule_file.read_bytes() != canonical(capsule)
                or digest(capsule_file) != receipt.get("capsule_manifest_sha256")
                or capsule.get("format") != "atoman-mail-recovery-capsule-v1"
                or capsule.get("source_snapshot_id") != source_id
                or capsule.get("capsule_id") != capsule_id
                or capsule.get("scope") != receipt.get("scope")
                or capsule.get("source_manifest_sha256") != digest(source / "manifest.json")):
            raise ValueError("Capsule manifest is not bound to the exact immutable paired source")
        for key in ("source_completed_at", "source_manifest_sha256",
                    "source_full_ciphertext_sha256", "source_full_ciphertext_bytes"):
            if capsule.get(key) != receipt.get(key):
                raise ValueError("Capsule encrypted provenance does not match source export receipt")
        original = json.loads((source / "manifest.json").read_text())
        records = {row["database"]: row for row in original["backups"]}
        prod = records["atoman_prod"]
        expected = {k: v for k, v in prod["public_table_counts"].items() if k == "Users" or k.startswith("mail_")}
        absent = sorted(MAIL_TABLES - expected.keys())
        if (capsule["atoman_prod_dump_sha256"] != prod["sha256"]
                or capsule["atoman_mail_dump_sha256"] != records["atoman_mail"]["sha256"]
                or capsule["application_source_tables"] != expected
                or capsule["application_absent_mail_tables"] != absent
                or capsule["application_payload_sha256"] != digest(source / "application-mail.sql")
                or capsule["application_manifest_sha256"] != digest(source / "application-mail-manifest.json")):
            raise ValueError("Capsule application or mail source binding differs")
        if capsule["scope"] == "post_activation_mail_capsule" and absent:
            raise ValueError("Post-activation capsule is missing application mail tables")
        with tarfile.open(source / "mail-assets.tar") as archive:
            asset_manifest = json.load(archive.extractfile("mail-assets-manifest.json"))
        storage = {"atoman_mail_dump_sha256": digest(source / "atoman_mail.dump"),
                   "mail_assets_sha256": digest(source / "mail-assets.tar"),
                   "r2_inventory_sha256": hashlib.sha256(canonical(asset_manifest["buckets"])).hexdigest(),
                   "r2_buckets": sorted(asset_manifest["buckets"]),
                   "r2_object_counts": {k: len(v) for k, v in asset_manifest["buckets"].items()},
                   "runtime_files_sha256": hashlib.sha256(canonical(asset_manifest["runtime_files"])).hexdigest(),
                   "runtime_file_count": len(asset_manifest["runtime_files"])}
        if any(capsule.get(k) != v for k, v in storage.items()) or set(storage["r2_buckets"]) != set(mail.BUCKETS):
            raise ValueError("Complete object inventories or private runtime files differ")
        witness = json.loads(args.main_nas_witness.read_text())
        full_nas_verified = verify_main_witness(witness, capsule)
        print("CAPSULE_AUTHENTICATED_STARTING_APPLICATION_SQL_RESTORE", flush=True)
        metadata = load("mail-capsule-application").verify(source, args.postgres_image)
        if (metadata.get("source_database") != "atoman_prod" or metadata.get("source_dump_sha256") != prod["sha256"]
                or metadata.get("source_tables") != expected or metadata.get("restored_tables") != expected
                or sorted(metadata.get("absent_mail_tables", [])) != absent):
            raise ValueError("Restored application mail metadata does not match the exact source")
        for key in ("table_counts_match", "user_links_verified", "credential_account_links_verified",
                    "draft_owner_links_verified", "restore_succeeded"):
            if metadata.get(key) is not True:
                raise ValueError("Application mail recovery did not pass " + key)
        metadata.update(application_payload_sha256=capsule["application_payload_sha256"],
                        application_manifest_sha256=capsule["application_manifest_sha256"])
        print("APPLICATION_MAIL_SQL_VERIFIED_STARTING_STALWART_RESTORE", flush=True)
        stack = workspace / "stalwart"
        stack.mkdir(mode=0o700)
        recovery.DIAGNOSTICS = args.report.parent / "recovery-failure-diagnostics"
        mail.private_dir(recovery.DIAGNOSTICS)
        result = recovery.run_stack(source, stack, args.postgres_image, args.minio_image, args.mc_image)
        storage.update(all_r2_objects_restored=True, all_runtime_files_verified=True)
        capsule_nas = {"capsule_id": capsule_id, "paired_source_snapshot_id": source_id,
                       "nas_path": transfer["nas_path"], "sha256": checksum, "bytes": size,
                       "readback_sha256": checksum, "readback_bytes": size,
                       "verified_at": transfer["verified_at"], "receipt": receipt, "copy_marker": marker}
        report = {"format": "atoman-mail-nas-recovery-v2", "status": "ok", "restore_succeeded": True,
                  "scope": capsule["scope"], "source_snapshot_id": source_id,
                  "source_completed_at": capsule["source_completed_at"],
                  "source_manifest_sha256": capsule["source_manifest_sha256"],
                  "source_full_ciphertext_sha256": capsule["source_full_ciphertext_sha256"],
                  "source_full_ciphertext_bytes": capsule["source_full_ciphertext_bytes"],
                  "capsule_id": capsule_id, "capsule_sha256": checksum, "capsule_bytes": size,
                  "capsule_manifest": capsule, "capsule_manifest_sha256": digest(capsule_file),
                  "main_bundle_nas_verified": full_nas_verified, "full_snapshot_nas_verified": full_nas_verified,
                  "main_bundle_nas": witness, "capsule_nas_roundtrip_verified": True, "capsule_nas": capsule_nas,
                  "all_mail_payload_files_verified": True, "all_snapshot_files_sha256_verified": False,
                  "owner_offline_identity_verified": True, "production_data_modified": False,
                  "verified_file_count": len(files), "mail_metadata": metadata, "mail_storage": storage,
                  "nas_path": transfer["nas_path"], "verified_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                  **result}
        if args.report.is_symlink():
            raise ValueError("Recovery report cannot be a symlink")
        args.report.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        args.report.write_bytes(canonical(report))
        args.report.chmod(0o600)
        print(json.dumps(report))

if __name__ == "__main__":
    main()

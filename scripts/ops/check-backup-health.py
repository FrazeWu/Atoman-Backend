#!/usr/bin/env python3
"""Report backup freshness, restore verification and encrypted publication health.

Exit nonzero for systemd OnFailure/journal alerts. Never prints database rows,
object keys, runtime configuration or decrypted email content.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile

SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$")
HASH = re.compile(r"^[0-9a-f]{64}$")
UNIT = re.compile(r"^atoman-[a-z0-9-]+\.service$")

def timestamp(value):
    value = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if value.tzinfo is None:
        raise ValueError("Backup time must include a timezone")
    return value

def regular(path):
    if path.is_symlink() or not path.is_file():
        raise ValueError("Backup member must be a regular file")
    return path

def latest_snapshot(root, require_mail, max_age_hours=48, now=None):
    candidates = []
    if root.is_symlink() or not root.is_dir():
        raise ValueError("Snapshot root is unavailable")
    for path in root.iterdir():
        if path.is_symlink() or not path.is_dir() or not SNAPSHOT.fullmatch(path.name):
            continue
        if not (path / "COMPLETE").is_file():
            continue
        raw_manifest = regular(path / "manifest.json").read_bytes()
        manifest = json.loads(raw_manifest)
        if manifest.get("complete") is True:
            candidates.append((timestamp(manifest["completed_at"]), path, manifest, hashlib.sha256(raw_manifest).hexdigest()))
    if not candidates:
        raise ValueError("No completed backup exists")
    when, path, manifest, manifest_sha = max(candidates, key=lambda x: x[0])
    age = ((now or dt.datetime.now(dt.timezone.utc)) - when).total_seconds() / 3600
    if age < -1 or age > max_age_hours:
        raise ValueError("Latest backup is stale or future-dated")
    records = manifest.get("backups", [])
    required = {"atoman_prod", "atoman_dev", "atoman_mail"} if require_mail else {"atoman_prod", "atoman_dev"}
    if {r.get("database") for r in records} != required:
        raise ValueError("Latest backup has an unexpected database set")
    for record in records:
        name = record["dump"]
        if Path(name).name != name:
            raise ValueError("Invalid dump name")
        file = regular(path / name)
        if not record.get("restore_verified") or file.stat().st_size != record["bytes"]:
            raise ValueError("Database restore verification is missing or file size changed")
    if require_mail:
        assets = manifest.get("additional_files", [])
        if manifest.get("mail_assets_verified") is not True or len(assets) != 1:
            raise ValueError("Paired mail recovery data is missing")
        record = assets[0]
        if record.get("name") != "mail-assets.tar" or record.get("verified") is not True:
            raise ValueError("Mail assets are unverified")
        if regular(path / "mail-assets.tar").stat().st_size != record["bytes"]:
            raise ValueError("Mail asset archive size changed")
    return {"source_snapshot_id": path.name, "source_completed_at": when.isoformat(),
            "age_hours": round(age, 2), "databases": sorted(required),
            "source_manifest_sha256": manifest_sha,
            "mail_assets_verified": manifest.get("mail_assets_verified") is True}

def encrypted_copy(root, source_id):
    if root.is_symlink() or not root.is_dir():
        raise ValueError("Encrypted export root is unavailable")
    for path in root.iterdir():
        if path.is_symlink() or not path.is_dir() or not SNAPSHOT.fullmatch(path.name):
            continue
        receipt_file = path / "receipt.json"
        if not receipt_file.is_file() or receipt_file.is_symlink():
            continue
        receipt = json.loads(receipt_file.read_text())
        if receipt.get("source_snapshot_id") != source_id or receipt.get("backup_scope", "full") != "full":
            continue
        if receipt.get("format") != "atoman-encrypted-export-v1" or receipt.get("ciphertext") != "backup.tar.age":
            raise ValueError("Invalid encrypted receipt")
        if regular(path / "backup.tar.age").stat().st_size != receipt.get("bytes"):
            raise ValueError("Encrypted export length changed")
        return {"export_snapshot_id": path.name, "encrypted_publication_ready": True,
                "offsite_transfer_confirmed": False}
    raise ValueError("Latest completed backup has not been published as ciphertext")


def _published_receipt(path):
    if path.is_symlink() or not path.is_dir():
        raise ValueError("Required full or capsule publication is missing")
    receipt_path = regular(path / "receipt.json")
    if receipt_path.stat().st_size > 1024 * 1024:
        raise ValueError("Published receipt is too large")
    receipt = json.loads(receipt_path.read_bytes())
    size = receipt.get("bytes")
    if (receipt.get("format") != "atoman-encrypted-export-v1"
            or receipt.get("ciphertext") != "backup.tar.age"
            or receipt.get("offsite_verified") is not False
            or isinstance(size, bool) or not isinstance(size, int) or size <= 0
            or not HASH.fullmatch(str(receipt.get("sha256", "")))):
        raise ValueError("Published receipt is invalid")
    if regular(path / "backup.tar.age").stat().st_size != size:
        raise ValueError("Published ciphertext length changed")
    return receipt

def checksum(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()

def encrypted_mail_copies(root, source_id, source_manifest_sha256, source_completed_at):
    """Check both local publications without rehashing the large full package."""
    if root.is_symlink() or not root.is_dir() or not SNAPSHOT.fullmatch(source_id):
        raise ValueError("Paired encrypted publication root or source is invalid")
    if not HASH.fullmatch(str(source_manifest_sha256)):
        raise ValueError("Original source manifest hash is unavailable")
    full = _published_receipt(root / source_id)
    if (full.get("backup_scope", "full") != "full"
            or full.get("source_snapshot_id") != source_id
            or timestamp(full["source_completed_at"]) != timestamp(source_completed_at)):
        raise ValueError("Full publication source differs")
    if ("source_manifest_sha256" in full
            and full["source_manifest_sha256"] != source_manifest_sha256):
        raise ValueError("Full publication source manifest differs")
    capsule_id = source_id + "m"
    capsule_path = root / capsule_id
    capsule = _published_receipt(capsule_path)
    if (capsule.get("backup_scope") != "mail-complete"
            or capsule.get("scope") not in {"pre_activation_mail_capsule", "post_activation_mail_capsule"}
            or capsule.get("source_snapshot_id") != capsule_id
            or capsule.get("paired_source_snapshot_id") != source_id
            or timestamp(capsule["source_completed_at"]) != timestamp(source_completed_at)):
        raise ValueError("Capsule publication source or scope differs")
    if capsule.get("source_manifest_sha256") != source_manifest_sha256:
        raise ValueError("Capsule source manifest differs")
    if (capsule.get("source_full_ciphertext_sha256") != full["sha256"]
            or capsule.get("source_full_ciphertext_bytes") != full["bytes"]):
        raise ValueError("Capsule full-ciphertext binding differs")
    if not HASH.fullmatch(str(capsule.get("capsule_manifest_sha256", ""))):
        raise ValueError("Capsule manifest binding is unavailable")
    if checksum(capsule_path / "backup.tar.age") != capsule["sha256"]:
        raise ValueError("Capsule ciphertext checksum changed")
    return {"export_snapshot_id": source_id, "encrypted_publication_ready": True,
            "mail_capsule_export_snapshot_id": capsule_id,
            "mail_capsule_publication_ready": True,
            "mail_capsule_scope": capsule["scope"],
            "source_manifest_sha256": source_manifest_sha256,
            "offsite_transfer_confirmed": False}

class OffsiteEvidencePending(ValueError):
    """A fresh backup has not yet received a signed independent NAS recovery."""


def offsite_signed_evidence(directory, allowed_signers, exports, source_id, manifest_hash, source_completed_at, now=None):
    """Verify offline Mac recovery proof without trusting a copied JSON claim.

    A mail-only restore NEVER marks the whole-site offsite archive verified.
    The private signing key stays on the independent recovery Mac; this host
    has only an Ed25519 SSH allowed-signers public trust anchor.
    """
    if (directory.is_symlink() or not directory.is_dir() or
            allowed_signers.is_symlink() or not allowed_signers.is_file()):
        raise ValueError("Offsite evidence directory or pinned public key is missing")
    evidence_file = directory / "evidence.json"
    signature_file = directory / "evidence.json.sig"
    if not evidence_file.exists() and not signature_file.exists():
        raise OffsiteEvidencePending("Independent NAS restore evidence is pending")
    evidence_file = regular(evidence_file)
    signature_file = regular(signature_file)
    if (not 20 <= evidence_file.stat().st_size <= 16384 or
            not 20 <= signature_file.stat().st_size <= 16384):
        raise ValueError("Offsite evidence size is invalid")
    raw = evidence_file.read_bytes()
    try:
        signature_checked = subprocess.run(
            ["ssh-keygen", "-Y", "verify", "-f", str(allowed_signers),
             "-I", "atoman-mail-attestor", "-n", "atoman-mail-recovery",
             "-s", str(signature_file)], input=raw, stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL, timeout=10, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise ValueError("Offsite attestation signature verifier unavailable") from error
    if signature_checked.returncode:
        raise ValueError("Offsite recovery attestation signature is invalid")
    attestation = json.loads(raw)
    if (attestation.get("format") == "atoman-offsite-recovery-attestation-v1"
            and attestation.get("source_snapshot_id") != source_id):
        # A new backup can be published before its independent Mac restore.
        # Keep the older signed proof, but never apply it to the new snapshot.
        if not SNAPSHOT.fullmatch(str(attestation.get("source_snapshot_id", ""))):
            raise ValueError("Signed offsite source identity is invalid")
        raise OffsiteEvidencePending("Latest NAS restore evidence is pending")
    if (attestation.get("format") != "atoman-offsite-recovery-attestation-v1"
            or attestation.get("source_manifest_sha256") != manifest_hash
            or attestation.get("mail_capsule_snapshot_id") != source_id + "m"
            or attestation.get("mail_recovery_verified") is not True
            or attestation.get("production_data_modified") is not False
            or not HASH.fullmatch(str(attestation.get("mail_report_sha256", "")))):
        raise ValueError("Signed mail recovery claim is incomplete or stale")
    reported_at = timestamp(attestation["verified_at"])
    observed_at = now or dt.datetime.now(dt.timezone.utc)
    if (reported_at < timestamp(source_completed_at) or
            reported_at > observed_at + dt.timedelta(minutes=10)):
        raise ValueError("Signed offsite restore timestamp is outside the backup window")
    mail_receipt = _published_receipt(exports / (source_id + "m"))
    site_receipt = _published_receipt(exports / source_id)
    if (attestation.get("mail_ciphertext_sha256") != mail_receipt["sha256"] or
            attestation.get("full_ciphertext_sha256") != site_receipt["sha256"]):
        raise ValueError("Signed offsite ciphertext checksum does not match published backup")
    full = attestation.get("full_restore_verified") is True
    if type(attestation.get("full_restore_verified")) is not bool:
        raise ValueError("Signed full-site restoration status is absent")
    if full and not HASH.fullmatch(str(attestation.get("full_report_sha256", ""))):
        raise ValueError("Signed full-site restoration report is absent")
    return {"offsite_evidence_status": "verified_mail_and_site" if full else "verified_mail_only",
            "mail_capsule_offsite_recovery_confirmed": True,
            "full_site_offsite_recovery_confirmed": full,
            "offsite_transfer_confirmed": full,
            "offsite_evidence_verified_at": reported_at.isoformat()}


def atomic_status(path, result):
    if path.is_symlink() or path.parent.is_symlink():
        raise ValueError("Status path cannot be a symbolic link")
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, prefix=".backup-health-", delete=False) as file:
        temp = Path(file.name)
        os.chmod(temp, 0o600)
        json.dump(result, file, indent=2)
        file.flush()
        os.fsync(file.fileno())
    os.replace(temp, path)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot-root", type=Path, required=True)
    parser.add_argument("--exports-root", type=Path, required=True)
    parser.add_argument("--require-mail", action="store_true")
    parser.add_argument("--max-age-hours", type=int, default=48)
    parser.add_argument("--unit", action="append", default=[])
    parser.add_argument("--status-file", type=Path)
    parser.add_argument("--offsite-evidence-dir", type=Path)
    parser.add_argument("--offsite-allowed-signers", type=Path)
    parser.add_argument("--offsite-grace-hours", type=int, default=36)
    args = parser.parse_args()
    if ((args.offsite_evidence_dir is None) != (args.offsite_allowed_signers is None)
            or not 1 <= args.offsite_grace_hours <= 168):
        parser.error("Offsite attestation needs both paths and a bounded grace period")
    if not 1 <= args.max_age_hours <= 720 or any(not UNIT.fullmatch(x) for x in args.unit):
        parser.error("Invalid freshness limit or service name")
    result = {"format": "atoman-backup-health-v1", "checked_at": dt.datetime.now(dt.timezone.utc).isoformat()}
    try:
        result.update(latest_snapshot(args.snapshot_root, args.require_mail, args.max_age_hours))
        if args.require_mail:
            result.update(encrypted_mail_copies(args.exports_root, result["source_snapshot_id"],
                                               result["source_manifest_sha256"], result["source_completed_at"]))
        else:
            result.update(encrypted_copy(args.exports_root, result["source_snapshot_id"]))
        if args.offsite_evidence_dir:
            try:
                result.update(offsite_signed_evidence(
                    args.offsite_evidence_dir, args.offsite_allowed_signers,
                    args.exports_root, result["source_snapshot_id"],
                    result["source_manifest_sha256"], result["source_completed_at"]))
            except OffsiteEvidencePending:
                # New backups can take hours to reach NAS and undergo a real
                # offline restore; record pending rather than invent success.
                result.update(offsite_evidence_status="pending",
                              mail_capsule_offsite_recovery_confirmed=False,
                              full_site_offsite_recovery_confirmed=False,
                              offsite_transfer_confirmed=False)
                if result["age_hours"] > args.offsite_grace_hours:
                    raise ValueError("Independent NAS mail restore evidence is overdue")
            except (ValueError, KeyError, TypeError, json.JSONDecodeError):
                # Malformed or forged evidence is a security alert, never a
                # reason to substitute local publication as offsite proof.
                raise ValueError("Independent NAS restore evidence is invalid")
        for unit in args.unit:
            if subprocess.run(["systemctl", "is-active", "--quiet", unit], timeout=10).returncode:
                raise ValueError("Required service is not active: " + unit)
        result["status"] = "ok"
    except (ValueError, OSError, KeyError, TypeError, subprocess.TimeoutExpired) as error:
        result.update(status="error", message=str(error)[:180])
    if args.status_file:
        atomic_status(args.status_file, result)
    print(json.dumps(result, sort_keys=True))
    raise SystemExit(0 if result["status"] == "ok" else 1)

if __name__ == "__main__":
    main()

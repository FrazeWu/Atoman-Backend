#!/usr/bin/env python3
"""Report backup freshness, restore verification and encrypted publication health.

Exit nonzero for systemd OnFailure/journal alerts. Never prints database rows,
object keys, runtime configuration or decrypted email content.
"""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile

SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$")
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
        manifest = json.loads(regular(path / "manifest.json").read_text())
        if manifest.get("complete") is True:
            candidates.append((timestamp(manifest["completed_at"]), path, manifest))
    if not candidates:
        raise ValueError("No completed backup exists")
    when, path, manifest = max(candidates, key=lambda x: x[0])
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
        if receipt.get("source_snapshot_id") != source_id:
            continue
        if receipt.get("format") != "atoman-encrypted-export-v1" or receipt.get("ciphertext") != "backup.tar.age":
            raise ValueError("Invalid encrypted receipt")
        if regular(path / "backup.tar.age").stat().st_size != receipt.get("bytes"):
            raise ValueError("Encrypted export length changed")
        return {"export_snapshot_id": path.name, "encrypted_publication_ready": True,
                "offsite_transfer_confirmed": False}
    raise ValueError("Latest completed backup has not been published as ciphertext")

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
    args = parser.parse_args()
    if not 1 <= args.max_age_hours <= 720 or any(not UNIT.fullmatch(x) for x in args.unit):
        parser.error("Invalid freshness limit or service name")
    result = {"format": "atoman-backup-health-v1", "checked_at": dt.datetime.now(dt.timezone.utc).isoformat()}
    try:
        result.update(latest_snapshot(args.snapshot_root, args.require_mail, args.max_age_hours))
        result.update(encrypted_copy(args.exports_root, result["source_snapshot_id"]))
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

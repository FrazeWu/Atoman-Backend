#!/usr/bin/env python3
"""Copy one encrypted VPS backup to the existing fa-owned NAS over SFTP.

All remote operations are file transfers, metadata reads or an atomic rename
within the existing backup dataset. Strict host-key checks are mandatory.
Only a successful NAS byte-for-byte readback publishes LOCAL_COMPLETE.json.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess as sp
import uuid

SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$")
NAS_ROOT = "/mnt/basic_pool/AtomanBackups"
VPS_ROOT = "/srv/atoman-backup-sftp/exports"

def sha256(path):
    with path.open("rb") as file:
        return hashlib.file_digest(file, "sha256").hexdigest()

def quote(value):
    value = str(value)
    if any(c in value for c in ("\n", "\r", "\0")):
        raise ValueError("Invalid SFTP path")
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'

def private_dir(path):
    if path.is_symlink():
        raise ValueError("Workspace cannot be a symbolic link")
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.stat().st_uid != os.getuid() or stat.S_IMODE(path.stat().st_mode) & 0o077:
        raise ValueError("Workspace must be private and owned by the recovery user")

def sftp(host, known_hosts, commands, identity=None, *, missing_ok=False, timeout=1800):
    args = ["sftp", "-b", "-", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
            "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=4",
            "-o", "UserKnownHostsFile=" + str(known_hosts)]
    if identity is not None:
        args += ["-i", str(identity), "-o", "IdentitiesOnly=yes"]
    args.append(host)
    result = sp.run(args, input=("\n".join(commands) + "\n").encode(), capture_output=True, timeout=timeout)
    if result.returncode:
        error = result.stderr.decode(errors="replace")
        if missing_ok and result.returncode == 1 and ("No such file" in error or "not found" in error):
            return False
        raise RuntimeError("SFTP backup data transfer failed")
    return True

def write_json(path, value):
    if path.is_symlink():
        raise ValueError("Refusing a symlinked local receipt")
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)

def validate_receipt(receipt, source_id):
    if (receipt.get("format") != "atoman-encrypted-export-v1"
            or receipt.get("ciphertext") != "backup.tar.age"
            or receipt.get("source_snapshot_id") != source_id
            or not isinstance(receipt.get("bytes"), int) or receipt["bytes"] <= 0
            or not re.fullmatch(r"[0-9a-f]{64}", str(receipt.get("sha256", "")))):
        raise ValueError("Invalid encrypted source receipt")
    stamp = dt.datetime.fromisoformat(receipt["source_completed_at"].replace("Z", "+00:00"))
    if stamp.tzinfo is None:
        raise ValueError("Source time must have a timezone")

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--snapshot", required=True)
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--vps-host", default="Atoman-EU")
    parser.add_argument("--nas-host", default="fa@172.16.10.201")
    parser.add_argument("--nas-identity", type=Path, required=True)
    parser.add_argument("--known-hosts", type=Path, required=True)
    args = parser.parse_args()
    if not SNAPSHOT.fullmatch(args.snapshot):
        parser.error("A completed backup snapshot ID is required")
    if args.nas_host != "fa@172.16.10.201":
        parser.error("This workflow is restricted to the existing NAS dataset owner")
    for path in (args.nas_identity, args.known_hosts):
        if path.is_symlink() or not path.is_file():
            parser.error("Existing regular identity and known-hosts files are required")
    if args.nas_identity.stat().st_mode & 0o077:
        parser.error("Dedicated NAS identity must be private")
    os.umask(0o077)
    private_dir(args.workspace)
    local = args.workspace / args.snapshot
    private_dir(local)
    inbound = local / "vps-source"
    readback = local / "nas-readback"
    private_dir(inbound)
    private_dir(readback)
    source = VPS_ROOT + "/" + args.snapshot
    receipt_path = inbound / "receipt.json"
    sftp(args.vps_host, args.known_hosts, ["get " + quote(source + "/receipt.json") + " " + quote(receipt_path)])
    receipt = json.loads(receipt_path.read_text())
    validate_receipt(receipt, args.snapshot)
    ciphertext = inbound / "backup.tar.age"
    if ciphertext.is_symlink():
        raise ValueError("Ciphertext cannot be a symlink")
    if not ciphertext.is_file() or ciphertext.stat().st_size != receipt["bytes"] or sha256(ciphertext) != receipt["sha256"]:
        if shutil.disk_usage(local).free < receipt["bytes"] * 3 + (256 << 20):
            raise ValueError("Not enough private disk space for transfer and readback")
        partial = inbound / "backup.tar.age.part"
        if partial.is_symlink():
            raise ValueError("Partial ciphertext cannot be a symlink")
        sftp(args.vps_host, args.known_hosts, ["get -a " + quote(source + "/backup.tar.age") + " " + quote(partial)])
        if partial.stat().st_size != receipt["bytes"] or sha256(partial) != receipt["sha256"]:
            raise ValueError("Downloaded VPS ciphertext checksum differs")
        partial.replace(ciphertext)
        ciphertext.chmod(0o600)
    print("VPS_CIPHERTEXT_VERIFIED", flush=True)
    final = NAS_ROOT + "/snapshots/" + args.snapshot
    nas_args = (args.nas_host, args.known_hosts)
    exists = sftp(*nas_args, ["ls " + quote(final)], args.nas_identity, missing_ok=True)
    stage = final if exists else NAS_ROOT + "/.mail-stage-" + args.snapshot + "-" + uuid.uuid4().hex[:8]
    if exists:
        existing_receipt = readback / "receipt.json"
        sftp(*nas_args, ["get " + quote(final + "/receipt.json") + " " + quote(existing_receipt)], args.nas_identity)
        existing = json.loads(existing_receipt.read_text())
        validate_receipt(existing, args.snapshot)
        if existing["sha256"] != receipt["sha256"] or existing["bytes"] != receipt["bytes"]:
            raise ValueError("NAS already has a conflicting snapshot; refusing overwrite")
    else:
        sftp(*nas_args, [
            "mkdir " + quote(stage), "chmod 700 " + quote(stage),
            "put -p " + quote(ciphertext) + " " + quote(stage + "/backup.tar.age"),
            "put -p " + quote(receipt_path) + " " + quote(stage + "/receipt.json"),
        ], args.nas_identity)
    returned = readback / "backup.tar.age"
    partial = readback / "backup.tar.age.part"
    if returned.is_symlink() or partial.is_symlink():
        raise ValueError("NAS readback path cannot be a symlink")
    # Always perform an actual NAS read. A cached VPS download is not evidence
    # that the NAS has stored the same bytes.
    sftp(*nas_args, ["get " + quote(stage + "/backup.tar.age") + " " + quote(partial),
                    "get " + quote(stage + "/receipt.json") + " " + quote(readback / "receipt.json")],
         args.nas_identity)
    returned_receipt = json.loads((readback / "receipt.json").read_text())
    validate_receipt(returned_receipt, args.snapshot)
    if (returned_receipt["sha256"] != receipt["sha256"] or partial.stat().st_size != receipt["bytes"]
            or sha256(partial) != receipt["sha256"]):
        raise ValueError("Actual NAS ciphertext readback failed integrity verification")
    partial.replace(returned)
    returned.chmod(0o600)
    marker = {"format": "atoman-nas-copy-v1", "source": args.snapshot,
              "source_snapshot_id": args.snapshot, "source_completed_at": receipt["source_completed_at"],
              "downloaded_at": dt.datetime.now(dt.timezone.utc).isoformat(),
              "bytes": receipt["bytes"], "sha256": receipt["sha256"],
              "ciphertext_transferred": True, "recovery_verified": False}
    marker_path = local / "LOCAL_COMPLETE.json"
    write_json(marker_path, marker)
    if not exists:
        sftp(*nas_args, ["put -p " + quote(marker_path) + " " + quote(stage + "/LOCAL_COMPLETE.json"),
                        "rename " + quote(stage) + " " + quote(final)], args.nas_identity)
    committed = readback / "LOCAL_COMPLETE.json"
    sftp(*nas_args, ["get " + quote(final + "/LOCAL_COMPLETE.json") + " " + quote(committed)], args.nas_identity)
    confirmed = json.loads(committed.read_text())
    if confirmed.get("source") != args.snapshot or confirmed.get("sha256") != receipt["sha256"] or not confirmed.get("ciphertext_transferred"):
        raise ValueError("NAS atomic publication marker differs")
    transfer = {"format": "atoman-mail-nas-transfer-v1", "source_snapshot_id": args.snapshot,
                "source_completed_at": receipt["source_completed_at"],
                "verified_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                "nas_path": final + "/backup.tar.age", "readback_path": stage + "/backup.tar.age",
                "nas_account": "fa", "nas_roundtrip_verified": True,
                "ciphertext_bytes": receipt["bytes"], "ciphertext_sha256": receipt["sha256"],
                "atomic_publication_verified": True, "mailbox_recovery_verified": False}
    write_json(local / "nas-transfer-receipt.json", transfer)
    print(json.dumps(transfer), flush=True)

if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Unprivileged TrueNAS pull of ciphertext from an SSH restricted SFTP account.

No decryption key is stored on NAS or on the VPS. This script verifies transfer
bytes only; recovery verification requires independent access to an age identity.
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
import subprocess
import tempfile

SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$")
HASH = re.compile(r"^[0-9a-f]{64}$")


class PullError(RuntimeError):
    pass


def private_directory(path):
    if path.is_symlink() or not path.is_dir():
        raise PullError("Private backup directory missing or is a symlink")
    info = path.stat()
    if info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) & 0o077:
        raise PullError("Private backup directory must be owned by the task user and mode 0700")


def digest(path):
    with path.open("rb") as handle:
        return hashlib.file_digest(handle, "sha256").hexdigest()


def validate_receipt(data):
    if not isinstance(data, dict) or data.get("format") != "atoman-encrypted-export-v1":
        raise PullError("Invalid source receipt")
    if data.get("ciphertext") != "backup.tar.age":
        raise PullError("Unknown source ciphertext name")
    if not isinstance(data.get("bytes"), int) or data["bytes"] <= 0:
        raise PullError("Invalid ciphertext size")
    if not isinstance(data.get("sha256"), str) or not HASH.fullmatch(data["sha256"]):
        raise PullError("Invalid ciphertext checksum")
    if data.get("offsite_verified") is not False:
        raise PullError("Unexpected source verification flag")
    return data


def run_sftp(host, identity, known_hosts, instructions, timeout=3600):
    cmd = [
        "sftp", "-q", "-b", "-", "-i", str(identity),
        "-oBatchMode=yes", "-oIdentitiesOnly=yes", "-oStrictHostKeyChecking=yes",
        "-oKexAlgorithms=curve25519-sha256", "-oHostKeyAlgorithms=ssh-ed25519",
        "-oUserKnownHostsFile=" + str(known_hosts), "-oConnectTimeout=15",
        "-oServerAliveInterval=30", "-oServerAliveCountMax=3", host,
    ]
    result = subprocess.run(cmd, input="\n".join(instructions) + "\n",
                            text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise PullError(f"SFTP request failed (exit {result.returncode}); verify SSH authorization")
    return result.stdout


def quote_sftp(value):
    if "\n" in value or "\r" in value or '"' in value or "\\" in value:
        raise PullError("Unsafe SFTP path")
    return '"' + value + '"'


def list_snapshots(host, identity, known_hosts):
    output = run_sftp(host, identity, known_hosts, ["ls -1 /exports"], timeout=90)
    found = set()
    for line in output.splitlines():
        item = line.strip()
        # OpenSSH sftp may return absolute paths for `ls -1 /exports`.
        if item.startswith('/exports/'):
            item = item[len('/exports/'):]
        if SNAPSHOT.fullmatch(item):
            found.add(item)
    return sorted(found)


def fetch(host, identity, known_hosts, remote, local):
    if not local.parent.is_dir():
        raise PullError("Missing destination directory")
    run_sftp(host, identity, known_hosts,
             ["get " + quote_sftp(remote) + " " + quote_sftp(str(local))])


def pull(root, host, identity, known_hosts):
    private_directory(root)
    if not identity.is_file() or stat.S_IMODE(identity.stat().st_mode) & 0o077:
        raise PullError("Missing or accessible SSH private key")
    if not known_hosts.is_file() or known_hosts.stat().st_size == 0:
        raise PullError("Pinned SSH host key missing")
    complete = root / "snapshots"
    incoming = root / ".incoming"
    for path in (complete, incoming):
        path.mkdir(mode=0o700, exist_ok=True)
        private_directory(path)
    copies = []
    for name in list_snapshots(host, identity, known_hosts):
        target = complete / name
        if target.exists():
            if target.is_symlink() or not (target / "LOCAL_COMPLETE.json").is_file():
                raise PullError("Existing snapshot must be verified before proceeding")
            marker = json.loads((target / "LOCAL_COMPLETE.json").read_text())
            old_ciphertext = target / "backup.tar.age"
            if (marker.get("format") != "atoman-nas-copy-v1" or
                    marker.get("source") != name or
                    not old_ciphertext.is_file() or old_ciphertext.is_symlink() or
                    old_ciphertext.stat().st_size != marker.get("bytes") or
                    digest(old_ciphertext) != marker.get("sha256")):
                raise PullError("Existing encrypted copy failed integrity verification")
            copies.append((name, "already-verified"))
            continue
        tmp = incoming / name
        tmp.mkdir(mode=0o700, exist_ok=True)
        private_directory(tmp)
        receipt_path = tmp / "receipt.json"
        fetch(host, identity, known_hosts, "/exports/" + name + "/receipt.json", receipt_path)
        if receipt_path.is_symlink() or receipt_path.stat().st_size > 1024 * 1024:
            raise PullError("Receipt is unsafe or too large")
        receipt = validate_receipt(json.loads(receipt_path.read_text()))
        if shutil.disk_usage(root).free < receipt["bytes"] + 512 * 1024 * 1024:
            raise PullError("Not enough free space for encrypted backup")
        ciphertext = tmp / "backup.tar.age"
        fetch(host, identity, known_hosts, "/exports/" + name + "/backup.tar.age", ciphertext)
        if ciphertext.stat().st_size != receipt["bytes"] or digest(ciphertext) != receipt["sha256"]:
            raise PullError("Encrypted transfer checksum failed")
        local = {
            "format": "atoman-nas-copy-v1",
            "source": name,
            "downloaded_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "bytes": receipt["bytes"],
            "sha256": receipt["sha256"],
            "ciphertext_transferred": True,
            "recovery_verified": False,
        }
        marker = tmp / "LOCAL_COMPLETE.json"
        with marker.open("x") as stream:
            json.dump(local, stream, indent=2)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(tmp, target)
        copies.append((name, "verified-copy"))
    return copies


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--host", required=True, help="backupread@VPS_ADDRESS")
    parser.add_argument("--identity", type=Path, required=True)
    parser.add_argument("--known-hosts", type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    if os.geteuid() == 0:
        parser.error("Run as the NAS dataset owner, not root")
    copies = pull(args.root, args.host, args.identity, args.known_hosts)
    print(json.dumps({"copies": copies, "recovery_verified": False}))


if __name__ == "__main__":
    main()

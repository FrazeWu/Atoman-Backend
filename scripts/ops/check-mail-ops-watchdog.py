#!/usr/bin/env python3
"""Lightweight external-alert trigger for active Mail V2 service and backup freshness."""
import datetime as dt
import json
import socket
from pathlib import Path
import subprocess
import sys
import urllib.request

HEALTH_FILE = Path("/var/lib/atoman/mail-backup-health.json")
SERVICES = ("atoman-backend.service", "atoman-mail-outbox.service", "atoman-stalwart.service")
CHECK_AGE_LIMIT = dt.timedelta(minutes=95)
BACKUP_AGE_LIMIT = 48


def check(now=None, path=HEALTH_FILE, run=subprocess.run, open_url=urllib.request.urlopen, connect=socket.create_connection):
    now = now or dt.datetime.now(dt.timezone.utc)
    for service in SERVICES:
        proc = run(["systemctl", "is-active", "--quiet", service], timeout=8, check=False)
        if proc.returncode:
            raise RuntimeError("Required mail service inactive: " + service)
    # Use the private JMAP listener, not the Docker control socket. Hardened
    # systemd services cannot necessarily inspect Docker under sandboxing.
    try:
        with connect(("127.0.0.1", 18580), timeout=5):
            pass
    except OSError:
        raise RuntimeError("Stalwart private JMAP endpoint not reachable") from None
    with open_url("http://127.0.0.1:8080/readyz", timeout=6) as response:
        if response.status != 200:
            raise RuntimeError("Mail backend readiness failed")
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 1024 * 1024:
        raise RuntimeError("Backup health record missing or unsafe")
    data = json.loads(path.read_text())
    stamp = dt.datetime.fromisoformat(data["checked_at"].replace("Z", "+00:00"))
    if stamp.tzinfo is None or not dt.timedelta(0) <= now - stamp < CHECK_AGE_LIMIT:
        raise RuntimeError("Paired mail backup health check is stale")
    if data.get("status") != "ok" or data.get("mail_capsule_publication_ready") is not True:
        raise RuntimeError("Paired mail backup or encrypted capsule not healthy")
    if not isinstance(data.get("age_hours"), (int, float)) or not 0 <= data["age_hours"] < BACKUP_AGE_LIMIT:
        raise RuntimeError("Latest paired mail backup is stale")
    # NAS full-site transfer is a separate recovery gate; this watchdog does NOT
    # claim it passed and does not alarm repeatedly on that already known backlog.
    return "ok"


def main():
    try:
        check()
    except Exception as error:
        print("MAIL_WATCHDOG_FAILURE: " + str(error), file=sys.stderr)
        return 1
    print("MAIL_WATCHDOG_OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())

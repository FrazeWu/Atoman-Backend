#!/usr/bin/env python3
"""Send deduplicated operator-only outage alerts through the existing verified Resend integration.

Never prints mail provider credentials, owner address, provider response bodies,
application env or user messages. No new external notification account required.
"""
import argparse
import datetime as dt
from email.message import EmailMessage
from email.utils import parseaddr
import hashlib
import json
import os
from pathlib import Path
import re
import smtplib
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request

RESEND_URL = "https://api.resend.com/emails"
STATE = Path("/var/lib/atoman/mail-ops-alert-state.json")
VALID_EMAIL = re.compile(r"^[^@\s<>]+@[^@\s<>]+\.[^@\s<>]+$")
ALLOWED_UNITS = frozenset({
    "atoman-backup.service", "atoman-mail-backup-health.service",
    "atoman-mail-maintenance.service", "atoman-mail-ops-watchdog.service",
    "atoman-backend.service", "atoman-mail-outbox.service",
    "atoman-stalwart.service",
})
COOLDOWN_SECONDS = 3600


def failed_services(run=subprocess.run):
    command = ["systemctl", "list-units", "--failed", "--no-legend", "--plain", "--no-pager"]
    proc = run(command, text=True, capture_output=True, timeout=8, check=False)
    if proc.returncode:
        raise RuntimeError("Cannot inspect systemd failed units")
    names = set()
    for line in proc.stdout.splitlines():
        parts = line.strip().lstrip("● ").split()
        if parts and parts[0] in ALLOWED_UNITS:
            names.add(parts[0])
    return sorted(names)


def limited_state(path):
    if path.is_symlink() or not path.is_file():
        return {}
    if path.stat().st_size > 8192 or path.stat().st_mode & 0o077:
        raise RuntimeError("Unsafe private mail alert state")
    try:
        value = json.loads(path.read_text())
    except (ValueError, UnicodeError):
        return {}
    return value if isinstance(value, dict) else {}


def send_report(key, sender, recipient, subject, body, open_url=urllib.request.urlopen):
    payload = json.dumps({"from": sender, "to": [recipient], "subject": subject,
                          "text": body}, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(
        RESEND_URL, data=payload, method="POST",
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"})
    try:
        with open_url(request, timeout=18) as response:
            status = response.status
    except urllib.error.HTTPError as error:
        # Deliberately never output potentially sensitive provider diagnostics.
        raise RuntimeError("Resend rejected mail alert: HTTP " + str(error.code)) from None
    except (urllib.error.URLError, TimeoutError):
        raise RuntimeError("Resend alert delivery API not reachable") from None
    if status not in (200, 201, 202):
        raise RuntimeError("Resend alert was not accepted: HTTP " + str(status))


def send_local_smtp_report(_key, sender, recipient, subject, body, client=smtplib.SMTP):
    """Queue independently via local Exim MTA; acceptance is not Inbox delivery."""
    envelope = parseaddr(sender)[1]
    if not VALID_EMAIL.fullmatch(envelope) or not VALID_EMAIL.fullmatch(recipient):
        raise RuntimeError("Invalid local mail alert envelope")
    message = EmailMessage()
    message["From"] = sender
    message["To"] = recipient
    message["Subject"] = subject
    message.set_content(body, charset="utf-8")
    try:
        with client("127.0.0.1", 25, timeout=16) as transport:
            transport.ehlo()
            rejected = transport.send_message(
                message, from_addr=envelope, to_addrs=[recipient])
            if rejected:
                raise RuntimeError("Local mail server rejected operator recipient")
    except (smtplib.SMTPException, OSError):
        raise RuntimeError("Local SMTP alert queue did not accept message") from None


def save_state(path, data):
    parent = path.parent
    if parent.is_symlink() or not parent.is_dir():
        raise RuntimeError("Unsafe alert state directory")
    handle, name = tempfile.mkstemp(prefix=".mail-ops-alert-", dir=parent)
    try:
        with os.fdopen(handle, "w") as output:
            json.dump(data, output, separators=(",", ":"))
            output.flush()
            os.fsync(output.fileno())
        os.chmod(name, 0o600)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def trigger(test=False, *, environ=None, now=None, state=STATE,
            inspect=failed_services, send=None):
    environ = environ if environ is not None else os.environ
    now = now or dt.datetime.now(dt.timezone.utc)
    transport = environ.get("ATOMAN_MAIL_ALERT_TRANSPORT", "resend")
    if transport not in ("resend", "local_smtp"):
        raise RuntimeError("Unsupported operator alert transport")
    recipient = (environ.get("ATOMAN_MAIL_ALERT_RECIPIENT") or
                 (environ.get("OWNER_EMAIL", "") if transport == "resend" else "")).strip()
    sender = (environ.get("ATOMAN_MAIL_ALERT_FROM") or environ.get("FROM_EMAIL", "")).strip()
    key = environ.get("RESEND_API_KEY", "").strip() if transport == "resend" else "local_smtp"
    if not (VALID_EMAIL.fullmatch(recipient) and sender and key and
            VALID_EMAIL.fullmatch(parseaddr(sender)[1])):
        raise RuntimeError("Owner, sender or provider alert configuration missing")
    if send is None:
        send = send_report if transport == "resend" else send_local_smtp_report
    if any("\r" in x or "\n" in x for x in (recipient, sender, key)):
        raise RuntimeError("Invalid alert mail headers")
    failing = [] if test else inspect()
    fingerprint = "test" if test else (
        hashlib.sha256("|".join(failing).encode()).hexdigest() if failing else "unknown-trigger")
    stored = {} if test else limited_state(state)
    if not test and stored.get("fingerprint") == fingerprint:
        last = stored.get("sent_at", "")
        try:
            passed = (now - dt.datetime.fromisoformat(last)).total_seconds()
        except (TypeError, ValueError):
            passed = COOLDOWN_SECONDS + 1
        if 0 <= passed < COOLDOWN_SECONDS:
            return "deduplicated"
    subject = "[Atoman] 邮箱运维告警测试" if test else "[Atoman] 邮箱服务异常"
    service_list = ", ".join(failing) if failing else "邮件服务运维触发器（具体原因请检查日志）"
    body = (
        "Atoman 邮箱运维通知\n\n"
        "此通知由现有运维监控自动生成，收件人为已配置的系统所有者。\n"
        f"时间（UTC）：{now.strftime('%Y-%m-%d %H:%M:%S')}\n"
        f"状态：{'外部通知通道测试' if test else '需要排查的服务故障'}\n"
        f"相关系统服务：{service_list if not test else '测试模式（未触发真实故障）'}\n\n"
        "登录服务器后检查：systemctl --failed，以及 "
        "journalctl -u atoman-backup -u atoman-mail-backup-health "
        "-u atoman-mail-maintenance -u atoman-mail-ops-watchdog。\n"
        "通知中不包含任何用户邮件内容、配置密钥或私人数据。"
    )
    send(key, sender, recipient, subject, body)
    if not test:
        save_state(state, {"fingerprint": fingerprint, "sent_at": now.isoformat()})
    return "local_queue_accepted" if transport == "local_smtp" else "provider_accepted"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--test", action="store_true", help="Send one operator-addressed channel test")
    args = parser.parse_args()
    os.umask(0o077)
    try:
        result = trigger(args.test)
    except Exception as error:
        print("MAIL_OPERATOR_ALERT_FAILED: " + str(error), file=sys.stderr)
        return 1
    print("MAIL_OPERATOR_ALERT_" + result.upper())
    return 0


if __name__ == "__main__":
    sys.exit(main())

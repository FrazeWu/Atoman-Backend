#!/usr/bin/env python3
"""Production Stalwart SMTP->IMAP synthetic mail test: disposable accounts only.

Requires explicit operator invocation; no Atoman site users, personal mailboxes,
external recipients, or permanent messages. Never prints or persists secrets.
"""
import argparse
import base64
import datetime as dt
import email
from email.message import EmailMessage
import hashlib
import imaplib
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import smtplib
import socket
import ssl
import time
import urllib.request

DOMAIN = "atoman.org"
EXPECTED_PUBLIC_IP = "169.58.161.96"
ADMIN_ENDPOINT = "http://127.0.0.1:18580/jmap/"


def admin_credentials(path):
    values = {}
    if path.is_symlink() or not path.is_file() or path.stat().st_mode & 0o077:
        raise RuntimeError("Admin environment is not a private file")
    for line in path.read_text().splitlines():
        if '=' not in line or line.lstrip().startswith('#'):
            continue
        key, value = line.split('=', 1)
        if key not in {'ATOMAN_MAIL_STALWART_ADMIN_USER', 'ATOMAN_MAIL_STALWART_ADMIN_PASSWORD'}:
            continue
        tokens = shlex.split(value)
        if len(tokens) != 1 or not tokens[0]:
            raise RuntimeError("Admin credential has an unsupported format")
        values[key] = tokens[0]
    if len(values) != 2:
        raise RuntimeError("Admin credentials unavailable")
    return values


def admin_call(authorization, method, body):
    request = urllib.request.Request(ADMIN_ENDPOINT,
        data=json.dumps({"using": ["urn:ietf:params:jmap:core", "urn:stalwart:jmap"],
                         "methodCalls": [[method, body, "operation"]]}).encode(),
        headers={"Authorization": "Basic " + authorization, "Content-Type": "application/json"}, method="POST")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=15) as response:
        data = json.load(response)
    methods = data.get("methodResponses", [])
    if len(methods) != 1 or methods[0][0] != method or methods[0][2] != "operation":
        raise RuntimeError("Stalwart JMAP management response invalid")
    return methods[0][1]


def verified_domain(auth):
    results = admin_call(auth, "x:Domain/query", {"filter": {"name": DOMAIN}})
    ids = results.get("ids", [])
    if len(ids) != 1:
        raise RuntimeError("Expected domain is not unique")
    details = admin_call(auth, "x:Domain/get", {"ids": ids})
    matches = [entry for entry in details.get("list", []) if entry.get("name") == DOMAIN]
    if len(matches) != 1:
        raise RuntimeError("Expected domain is not configured")
    return matches[0]["id"]


def synthetic_account(local, domain_id, password):
    return {"@type": "User", "name": local, "domainId": domain_id,
            "description": "Atoman disposable synthetic-only SMTP/IMAP acceptance",
            "credentials": {"0": {"@type": "Password", "secret": password}},
            "aliases": {}, "memberGroupIds": {}, "roles": {"@type": "User"},
            "permissions": {"@type": "Inherit"},
            "quotas": {"maxDiskQuota": 64 * 1024 * 1024},
            "encryptionAtRest": {"@type": "Disabled"}}


def ensure_internal_tls_destination():
    ips = {sock[4][0] for sock in socket.getaddrinfo("mail.atoman.org", 465, type=socket.SOCK_STREAM)}
    if ips != {EXPECTED_PUBLIC_IP}:
        raise RuntimeError("Mail TLS endpoint does not resolve to the local VPS")


def send_synthetic(sender, password, recipient, marker, implicit):
    msg = EmailMessage()
    msg["From"] = sender
    msg["To"] = recipient
    msg["Subject"] = "Atoman isolated mail acceptance " + marker
    msg["Message-ID"] = "<" + marker + "@atoman.org>"
    msg.set_content("Synthetic mail only. Marker: " + marker + "\n")
    payload = ("Synthetic attachment for " + marker + "\n").encode()
    msg.add_attachment(payload, maintype="text", subtype="plain", filename="synthetic.txt")
    context = ssl.create_default_context()
    if implicit:
        client = smtplib.SMTP_SSL("mail.atoman.org", 465, context=context, timeout=15)
    else:
        client = smtplib.SMTP("mail.atoman.org", 587, timeout=15)
    try:
        client.ehlo()
        if not implicit:
            client.starttls(context=context)
            client.ehlo()
        client.login(sender, password)
        undelivered = client.send_message(msg, from_addr=sender, to_addrs=[recipient])
        if undelivered:
            raise RuntimeError("SMTP delivery rejected the synthetic recipient")
    finally:
        try:
            client.quit()
        except (OSError, smtplib.SMTPException):
            client.close()
    return hashlib.sha256(payload).hexdigest()


def receive_synthetic(recipient, password, sender, marker, payload_sha):
    context = ssl.create_default_context()
    with imaplib.IMAP4_SSL("mail.atoman.org", 993, ssl_context=context, timeout=20) as client:
        result, _ = client.login(recipient, password)
        if result != 'OK':
            raise RuntimeError("IMAPS login failed")
        result, _ = client.select("INBOX", readonly=True)
        if result != 'OK':
            raise RuntimeError("Disposable inbox missing")
        for attempt in range(25):
            result, ids = client.search(None, "SUBJECT", marker)
            if result != 'OK':
                raise RuntimeError("IMAP search failed")
            for number in ids[0].split():
                result, message = client.fetch(number, "(BODY.PEEK[])")
                if result != 'OK' or not message or not isinstance(message[0], tuple):
                    continue
                obj = email.message_from_bytes(message[0][1], policy=email.policy.default)
                if obj.get("From") != sender or obj.get("To") != recipient or marker not in obj.get("Subject", ""):
                    continue
                text = obj.get_body(preferencelist=("plain",))
                if not text or marker not in text.get_content():
                    raise RuntimeError("Synthetic mail body mismatch")
                attachments = list(obj.iter_attachments())
                if len(attachments) != 1 or hashlib.sha256(attachments[0].get_payload(decode=True)).hexdigest() != payload_sha:
                    raise RuntimeError("Synthetic attachment checksum mismatch")
                return
            time.sleep(1)
        raise RuntimeError("Synthetic mail not received within timeout")


def test_self_contained(admin_env, report_path):
    if report_path.exists() or report_path.is_symlink():
        raise RuntimeError("Refusing to overwrite acceptance receipt")
    ensure_internal_tls_destination()
    values = admin_credentials(admin_env)
    auth = base64.b64encode((values['ATOMAN_MAIL_STALWART_ADMIN_USER'] + ':' + values['ATOMAN_MAIL_STALWART_ADMIN_PASSWORD']).encode()).decode()
    domain_id = verified_domain(auth)
    token = secrets.token_hex(5)
    localparts = ["opscheck-" + token + "-a", "opscheck-" + token + "-b"]
    passwords = [secrets.token_urlsafe(28), secrets.token_urlsafe(28)]
    ids = []
    steps = []
    error = None
    clean = True
    try:
        for local, password in zip(localparts, passwords):
            created = admin_call(auth, "x:Account/set", {"create": {"probe": synthetic_account(local, domain_id, password)}})
            account = created.get("created", {}).get("probe")
            if not account or not account.get("id") or created.get("notCreated"):
                raise RuntimeError("Disposable account provisioning rejected: " + str(created.get("notCreated", {}).get("probe", {}).get("type", "unknown")))
            ids.append(account["id"])
        steps.append("two_disposable_accounts_created")
        sender, receiver = [part + "@" + DOMAIN for part in localparts]
        for implicit, src, pwd, dst, dstpwd in [(True, sender, passwords[0], receiver, passwords[1]),
                                                 (False, receiver, passwords[1], sender, passwords[0])]:
            marker = "internal" + secrets.token_hex(7)
            digest = send_synthetic(src, pwd, dst, marker, implicit)
            receive_synthetic(dst, dstpwd, src, marker, digest)
            steps.append("smtp465_to_imap993_verified" if implicit else "smtp587_starttls_to_imap993_verified")
    except Exception as caught:
        error = type(caught).__name__ + ": " + str(caught)[:180]
    finally:
        for account_id in reversed(ids):
            try:
                result = admin_call(auth, "x:Account/set", {"destroy": [account_id]})
                if account_id not in result.get("destroyed", []) or result.get("notDestroyed"):
                    clean = False
            except Exception:
                clean = False
        steps.append("disposable_accounts_cleaned" if clean else "cleanup_requires_operator_review")
        receipt = {"format": "atoman-internal-disposable-mail-acceptance-v1",
                   "completed_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                   "successful": error is None and clean,
                   "steps": steps,
                   "disposable_accounts_created": len(ids),
                   "disposable_accounts_deleted": len(ids) if clean else None,
                   "nonprivate_message_count": 2 if error is None else None,
                   "production_personal_accounts_touched": False,
                   "error": error if error else ("cleanup_failure" if not clean else None),
                   "remaining_disposable_account_ids": ids if not clean else []}
        report_path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        fd = os.open(report_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as f:
            json.dump(receipt, f, indent=2)
            f.flush()
            os.fsync(f.fileno())
        print(json.dumps({"successful": receipt['successful'], "steps": steps,
                          "created": len(ids), "cleaned": clean,
                          "error_type": error.split(':', 1)[0] if error else None}), flush=True)
        if not receipt['successful']:
            raise RuntimeError("Internal disposable mail acceptance incomplete; see private receipt")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--admin-env', type=Path, default=Path('/etc/atoman-mail/backend.env'))
    parser.add_argument('--receipt', type=Path, required=True)
    parser.add_argument('--confirm-disposable-only', action='store_true')
    args = parser.parse_args()
    if not args.confirm_disposable_only:
        parser.error('Disposable account creation requires explicit operator acknowledgement')
    test_self_contained(args.admin_env, args.receipt)

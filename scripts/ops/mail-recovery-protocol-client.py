#!/usr/bin/env python3
"""Protocol verifier running in the restored Stalwart network namespace."""
import hashlib
import importlib.util
import imaplib
import json
from pathlib import Path
import socket
import ssl
import sys
import time
from email import message_from_bytes

phase = "input"
def main():
    global phase
    data = json.load(sys.stdin)
    spec = importlib.util.spec_from_file_location("recovery", Path(__file__).with_name("verify-mail-recovery.py"))
    recovery = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(recovery)
    credential = data["credential"]
    base = "http://127.0.0.1:8080"
    phase = "http_readiness"
    recovery.wait_http(base + "/jmap/session", {200, 401})
    if data["action"] == "configure":
        phase = "isolated_datastore_configuration"
        result = recovery.jmap(base, credential, "x:DataStore/set", {"update": {"singleton": data["data_store"]}})
        if result.get("notUpdated") or "singleton" not in (result.get("updated") or {}):
            raise RuntimeError("Datastore configuration rejected")
        phase = "isolated_blobstore_configuration"
        result = recovery.jmap(base, credential, "x:BlobStore/set", {"update": {"singleton": data["blob_store"]}})
        if result.get("notUpdated") or "singleton" not in (result.get("updated") or {}):
            raise RuntimeError("Blob store configuration rejected")
        return {"storage_configuration_accepted": True}
    if data["action"] != "verify":
        raise ValueError("Unknown protocol action")
    canary = data["canary"]
    phase = "trusted_imap_tls"
    tls = ssl.create_default_context()
    class LoopbackIMAP(imaplib.IMAP4_SSL):
        def _create_socket(self, timeout):
            sock = socket.create_connection(("127.0.0.1", 993), timeout)
            return self.ssl_context.wrap_socket(sock, server_hostname="mail.atoman.org")
    for _ in range(45):
        try:
            client = LoopbackIMAP("mail.atoman.org", 993, ssl_context=tls, timeout=15)
            break
        except (OSError, ssl.SSLError):
            time.sleep(1)
    else:
        raise RuntimeError("Trusted isolated IMAPS unavailable")
    with client:
        phase = "restored_mailbox_login"
        client.login(credential["username"], credential["secret"])
        status, _ = client.select("INBOX", readonly=True)
        if status != "OK":
            raise RuntimeError("Restored INBOX unavailable")
        phase = "original_uid_readback"
        status, values = client.uid("fetch", canary["imap_uid"], "(BODY.PEEK[])")
        raw = next((value[1] for value in values if isinstance(value, tuple)), None)
        if status != "OK" or raw is None:
            raise RuntimeError("Original message UID unavailable")
        phase = "message_and_body_hash"
        if hashlib.sha256(raw).hexdigest() != canary["delivered_eml_sha256"]:
            raise ValueError("Restored message bytes differ")
        message = message_from_bytes(raw)
        if str(message.get("Message-ID", "")).strip() != canary["message_id"].strip():
            raise ValueError("Restored message identity differs")
        plaintext = [part.get_payload(decode=True) for part in message.walk()
                     if part.get_content_type() == "text/plain" and not part.get_filename()]
        expected_body = (b"SYNTHETIC recovery test only. This message and its attachment contain no user data.\n"
                         b"The attachment must survive SQL plus object-store backup and NAS recovery.\n")
        if len(plaintext) != 1:
            raise ValueError("Restored body missing")
        body_sha = hashlib.sha256(plaintext[0].replace(b"\r\n", b"\n")).hexdigest()
        expected_body_sha = hashlib.sha256(expected_body).hexdigest()
        if body_sha != expected_body_sha:
            raise ValueError("Restored body differs")
        phase = "attachment_hash"
        attachments = [part.get_payload(decode=True) for part in message.walk()
                       if part.get_filename() == "synthetic-recovery-canary.bin"]
        if len(attachments) != 1 or hashlib.sha256(attachments[0]).hexdigest() != canary["attachment_sha256"]:
            raise ValueError("Restored attachment differs")
    return {"mailbox": "INBOX", "canary_uid_preserved": True,
            "message_bytes_match": True, "message_sha256": hashlib.sha256(raw).hexdigest(),
            "message_expected_sha256": canary["delivered_eml_sha256"],
            "body_sha256_matches": True, "body_sha256": body_sha, "body_expected_sha256": expected_body_sha,
            "attachment_expected_sha256": canary["attachment_sha256"], "attachment_bytes": len(attachments[0]),
            "attachment_sha256": canary["attachment_sha256"], "attachment_bytes_match": True}

if __name__ == "__main__":
    try:
        print(json.dumps({"status": "ok", "result": main()}))
    except Exception as error:
        print(json.dumps({"status": "error", "phase": phase, "error_type": type(error).__name__}))
        raise SystemExit(1)

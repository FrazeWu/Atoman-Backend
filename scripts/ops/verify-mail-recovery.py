#!/usr/bin/env python3
"""Recover a NAS-returned age backup into isolated PostgreSQL, MinIO and Stalwart.

The offline age identity stays on the recovery computer. Production R2 keys are
never supplied to the recovery containers. Only loopback HTTP/IMAP ports are
published, and the temporary Docker network has external routing disabled.
"""
import argparse
import base64
import contextlib
import datetime as dt
from email import message_from_bytes
import hashlib
import importlib.util
import imaplib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import ssl
import subprocess as sp
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import urllib.parse
import uuid

SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$")
PG_IMAGE = "postgres@sha256:57c72fd2a128e416c7fcc499958864df5301e940bca0a56f58fddf30ffc07777"
MINIO_IMAGE = "minio/minio@sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"
LABEL = "atoman.ops=mail-isolated-recovery"
PYTHON_CLIENT_IMAGE = "python@sha256:eb2c62658b8d51bff0acc408504b76521d02030de56a8a3c1e830083e89b89d2"
DIAGNOSTICS = None

def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

def digest(path):
    with path.open("rb") as reader:
        return hashlib.file_digest(reader, "sha256").hexdigest()

def command(args, *, stdin=None, timeout=120, label="command"):
    try:
        proc = sp.run(args, stdin=stdin, capture_output=True, timeout=timeout)
    except sp.TimeoutExpired:
        raise RuntimeError("Isolated recovery timed out during " + label) from None
    if proc.returncode:
        # Never propagate command arguments or server diagnostics containing
        # credentials or message content into the operations log.
        raise RuntimeError("Isolated recovery failed during " + label)
    return proc.stdout

def decrypt_selected(ciphertext, identity, destination, selected=None):
    if selected is None:
        selected = {"manifest.json", "atoman_mail.dump", "mail-assets.tar"}
    observed = {}
    payload = None
    with tempfile.TemporaryFile() as errors:
        process = sp.Popen(["age", "--decrypt", "--identity", str(identity), str(ciphertext)],
                           stdout=sp.PIPE, stderr=errors)
        try:
            with tarfile.open(fileobj=process.stdout, mode="r|") as archive:
                for member in archive:
                    if not member.isfile() or not re.fullmatch(r"[A-Za-z0-9_.-]+", member.name) or member.name in (".", ".."):
                        raise ValueError("Unsafe encrypted archive member")
                    if member.name in observed or (member.name == "payload-manifest.json" and payload is not None):
                        raise ValueError("Duplicate encrypted archive member")
                    if payload is None and member.name != "payload-manifest.json":
                        raise ValueError("Payload manifest must come first")
                    stream = archive.extractfile(member)
                    if member.name == "payload-manifest.json":
                        if member.size > 4 << 20:
                            raise ValueError("Payload manifest is unexpectedly large")
                        payload = json.load(stream)
                        if payload.get("format") != "atoman-encrypted-payload-v1":
                            raise ValueError("Unknown payload format")
                        continue
                    hasher = hashlib.sha256()
                    size = 0
                    writer = (destination / member.name).open("xb") if member.name in selected else None
                    try:
                        for chunk in iter(lambda: stream.read(1 << 20), b""):
                            size += len(chunk)
                            hasher.update(chunk)
                            if writer is not None:
                                writer.write(chunk)
                    finally:
                        if writer is not None:
                            writer.close()
                    if size != member.size:
                        raise ValueError("Recovered member length differs")
                    observed[member.name] = {"bytes": size, "sha256": hasher.hexdigest()}
            # Drain tar padding so age authenticates the entire ciphertext.
            while process.stdout.read(1 << 20):
                pass
            if process.wait(timeout=30):
                raise RuntimeError("Age authentication failed")
        finally:
            if process.poll() is None:
                process.kill()
                process.wait()
    if payload is None or payload.get("files") != observed or not selected <= set(observed):
        raise ValueError("Recovered archive does not match its complete file manifest")
    return payload, observed

def ensure_image(image):
    result = sp.run(["docker", "image", "inspect", image], capture_output=True)
    if result.returncode:
        command(["docker", "pull", "--quiet", image], timeout=600, label="pulling a pinned recovery image")

def docker_json(args):
    return json.loads(command(["docker", *args], label="Docker metadata"))

def port(container, internal):
    record = docker_json(["inspect", container])[0]["NetworkSettings"]["Ports"][str(internal) + "/tcp"]
    if len(record) != 1 or record[0]["HostIp"] != "127.0.0.1":
        raise RuntimeError("Recovery endpoint is not restricted to loopback")
    return int(record[0]["HostPort"])

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        raise urllib.error.HTTPError(request.full_url, code, "Recovery redirects are disabled", headers, fp)

def loopback_open(request, timeout):
    value = request.full_url if isinstance(request, urllib.request.Request) else request
    parsed = urllib.parse.urlsplit(value)
    if parsed.scheme != "http" or parsed.hostname != "127.0.0.1" or parsed.username is not None:
        raise ValueError("Recovery management requests must stay on loopback")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    return opener.open(request, timeout=timeout)

def wait_http(url, accepted):
    for _ in range(60):
        try:
            with loopback_open(url, timeout=2) as response:
                code = response.status
        except urllib.error.HTTPError as error:
            code = error.code
        except (OSError, urllib.error.URLError):
            code = 0
        if code in accepted:
            return
        time.sleep(1)
    raise RuntimeError("Isolated service did not become ready")

def jmap(base, credential, method, args):
    authorization = base64.b64encode((credential["username"] + ":" + credential["secret"]).encode()).decode()
    payload = {"using": ["urn:ietf:params:jmap:core", "urn:stalwart:jmap"],
               "methodCalls": [[method, args, "recovery"]]}
    request = urllib.request.Request(base + "/jmap", json.dumps(payload).encode(),
        {"Authorization": "Basic " + authorization, "Content-Type": "application/json"})
    try:
        with loopback_open(request, timeout=30) as response:
            data = json.load(response)
    except (OSError, urllib.error.URLError):
        raise RuntimeError("Isolated Stalwart management request failed") from None
    results = data.get("methodResponses", [])
    if len(results) != 1 or results[0][0] != method:
        raise RuntimeError("Isolated Stalwart management method was rejected")
    return results[0][1]

def runtime_json(archive, name):
    item = archive.getmember("runtime/" + name)
    if not item.isfile() or item.size > 4 << 20:
        raise ValueError("Invalid private recovery metadata")
    return json.load(archive.extractfile(item))

def protocol_client(container, payload):
    if not re.fullmatch(r"atoman-mail-recovery-[0-9a-f]{12}-mail", container):
        raise ValueError("Protocol verifier requires its own restored container")
    inspected = docker_json(["inspect", container])[0]
    networks = inspected["NetworkSettings"]["Networks"]
    if len(networks) != 1 or not docker_json(["network", "inspect", next(iter(networks))])[0].get("Internal"):
        raise RuntimeError("Protocol verifier namespace is not isolated")
    if inspected["HostConfig"].get("PortBindings"):
        raise RuntimeError("Restored mail unexpectedly publishes host ports")
    name = container + "-check-" + uuid.uuid4().hex[:6]
    args = ["docker", "run", "--rm", "-i", "--name", name, "--label", LABEL,
            "--network", "container:" + container, "--user", str(os.getuid()) + ":" + str(os.getgid()),
            "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
            "--pids-limit", "64", "--cpus", "1", "--memory", "192m",
            "--env", "PYTHONDONTWRITEBYTECODE=1", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m",
            "--mount", "type=bind,src=" + str(Path(__file__).resolve()) + ",dst=/client/verify-mail-recovery.py,readonly",
            "--mount", "type=bind,src=" + str(Path(__file__).with_name("mail-recovery-protocol-client.py").resolve()) + ",dst=/client/mail-recovery-protocol-client.py,readonly",
            PYTHON_CLIENT_IMAGE, "python", "/client/mail-recovery-protocol-client.py"]
    try:
        try:
            proc = sp.run(args, input=json.dumps(payload).encode(), capture_output=True, timeout=240)
        except sp.TimeoutExpired:
            raise RuntimeError("Isolated protocol client timed out") from None
        try:
            result = json.loads(proc.stdout)
        except (ValueError, UnicodeDecodeError):
            result = {}
        if proc.returncode or result.get("status") != "ok":
            phase = result.get("phase", "startup")
            if not isinstance(phase, str) or not re.fullmatch(r"[a-z_]{1,80}", phase):
                phase = "protocol"
            raise RuntimeError("Isolated protocol client failed during " + phase)
        return result["result"]
    finally:
        sp.run(["docker", "rm", "-f", "-v", name], stdout=sp.DEVNULL, stderr=sp.DEVNULL)

def run_stack(source, workspace, pg_image, minio_image, mc_image):
    mail = load("backup-mail")
    pg = mail.load_pg()
    manifest = json.loads((source / "manifest.json").read_text())
    if manifest.get("complete") is not True or manifest.get("mail_assets_verified") is not True:
        raise ValueError("A completed paired backup is required")
    records = [r for r in manifest.get("backups", []) if r.get("database") == "atoman_mail"]
    if len(records) != 1 or records[0].get("restore_verified") is not True:
        raise ValueError("Verified Stalwart SQL recovery data is absent")
    record = records[0]
    if record.get("dump") != "atoman_mail.dump" or digest(source / "atoman_mail.dump") != record["sha256"]:
        raise ValueError("Stalwart SQL archive checksum differs")
    assets = source / "mail-assets.tar"
    asset_summary = mail.verify_assets(assets)
    with tarfile.open(assets) as archive:
        asset_manifest = json.load(archive.extractfile("mail-assets-manifest.json"))
        config = runtime_json(archive, "etc/atoman-mail/stalwart-config/config.json")
        credential = runtime_json(archive, "etc/atoman-mail/stalwart-admin.json")
        canaries = [name for name in asset_manifest["runtime_files"]
                    if name.startswith("var/backups/atoman/mail-canary/") and name.endswith(".json")]
        if len(canaries) != 1:
            raise ValueError("Exactly one archived recovery canary receipt is required")
        canary = runtime_json(archive, canaries[0])
        if not canary.get("imap_inbox_verified"):
            raise ValueError("Source mailbox canary had not passed readback")
        compose = archive.extractfile("runtime/etc/atoman-mail/compose.yml").read().decode()
        match = re.search(r"(?m)^\s*image:\s*(stalwartlabs/stalwart@sha256:[0-9a-f]{64})\s*$", compose)
        if not match:
            raise ValueError("Archived Stalwart image is not pinned by digest")
        stalwart_image = match.group(1)
        blob_sources = []
        objects = workspace / "objects"
        objects.mkdir(mode=0o700)
        for bucket, rows in asset_manifest["buckets"].items():
            if bucket not in mail.BUCKETS:
                raise ValueError("Unexpected recovery bucket")
            for key, value in rows.items():
                name = hashlib.sha256((bucket + "\0" + key).encode()).hexdigest() + ".blob"
                file = objects / name
                with archive.extractfile(value["member"]) as reader, file.open("xb") as writer:
                    shutil.copyfileobj(reader, writer)
                if digest(file) != value["sha256"]:
                    raise ValueError("Recovered object bytes differ")
                blob_sources.append((bucket, key, name))
    for image in (pg_image, minio_image, mc_image, stalwart_image, PYTHON_CLIENT_IMAGE):
        ensure_image(image)
    run_id = uuid.uuid4().hex[:12]
    prefix = "atoman-mail-recovery-" + run_id
    network, pg_name, minio_name, mail_name, config_volume = [prefix + "-" + suffix for suffix in ("net", "pg", "s3", "mail", "cfg")]
    containers = []
    network_created = volume_created = False
    password = secrets.token_hex(24)
    access = "recovery" + run_id
    secret = secrets.token_hex(32)
    pg_user = config["authUsername"]
    original_host = config["host"]
    if not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]{0,62}", pg_user) or not re.fullmatch(r"[A-Za-z][A-Za-z0-9.-]*", original_host):
        raise ValueError("Archived database account or hostname is unsuitable for isolation")
    pg_env = workspace / "postgres.env"
    pg_env.write_text("POSTGRES_USER=" + pg_user + "\nPOSTGRES_DB=atoman_mail\nPOSTGRES_PASSWORD=" + password + "\n")
    s3_env = workspace / "s3.env"
    s3_env.write_text("HOME=/tmp\nMINIO_ROOT_USER=" + access + "\nMINIO_ROOT_PASSWORD=" + secret + "\n")
    mc_env = workspace / "mc.env"
    mc_env.write_text("MC_HOST_recovery=http://" + access + ":" + secret + "@" + minio_name + ":9000\n")
    mail_env = workspace / "mail.env"
    mail_env.write_text("STALWART_PG_PASSWORD=" + password + "\nAWS_ACCESS_KEY_ID=" + access +
                        "\nAWS_SECRET_ACCESS_KEY=" + secret + "\nSTALWART_PUBLIC_URL=https://mail.atoman.org\n")
    cfg = workspace / "config"
    cfg.mkdir(mode=0o700)
    if config.get("@type") != "PostgreSql":
        raise ValueError("Unexpected archived Stalwart config store type")
    config.update(host=pg_name, port=5432, database="atoman_mail", authUsername=pg_user,
                  useTls=False, authSecret={"@type": "EnvironmentVariable", "variableName": "STALWART_PG_PASSWORD"})
    (cfg / "config.json").write_text(json.dumps(config))
    data = workspace / "minio-data"
    data.mkdir(mode=0o700)
    def mc(*args, timeout=300):
        return command(["docker", "run", "--rm", "--network", network, "--env-file", str(mc_env),
            "--mount", "type=bind,src=" + str(objects) + ",dst=/backup,readonly", mc_image, *args],
            timeout=timeout, label="restoring isolated S3 objects")
    try:
        command(["docker", "network", "create", "--internal", "--label", LABEL, network], label="creating isolated network")
        network_created = True
        if not docker_json(["network", "inspect", network])[0].get("Internal"):
            raise RuntimeError("Recovery network has external routing")
        command(["docker", "run", "-d", "--name", pg_name, "--label", LABEL, "--network", network,
            "--network-alias", original_host, "--cpus", "2", "--memory", "2g", "--env-file", str(pg_env),
            "--tmpfs", "/var/lib/postgresql/data:rw,nosuid,nodev,size=512m",
            pg_image, "-c", "maintenance_work_mem=256MB"], label="starting isolated PostgreSQL")
        containers.append(pg_name)
        for _ in range(60):
            if sp.run(["docker", "exec", pg_name, "pg_isready", "-q", "-U", pg_user, "-d", "atoman_mail"],
                      stdout=sp.DEVNULL, stderr=sp.DEVNULL).returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Isolated PostgreSQL is not ready")
        with (source / "atoman_mail.dump").open("rb") as dump:
            command(["docker", "exec", "-i", pg_name, "pg_restore", "-U", pg_user, "-d", "atoman_mail",
                "--exit-on-error", "--no-owner", "--no-acl"], stdin=dump, timeout=300, label="restoring Stalwart SQL")
        actual = pg.counts(pg_name, pg_user, "atoman_mail", record["public_table_counts"])
        if actual != record["public_table_counts"]:
            raise ValueError("Restored Stalwart row counts differ")
        command(["docker", "run", "-d", "--name", minio_name, "--label", LABEL, "--network", network,
            "--user", str(os.getuid()) + ":" + str(os.getgid()), "--cpus", "1", "--memory", "512m",
            "--env-file", str(s3_env), "--mount", "type=bind,src=" + str(data) + ",dst=/data",
            minio_image, "server", "/data", "--address", ":9000"], label="starting isolated S3")
        containers.append(minio_name)
        # MinIO is reachable only from the isolated network. The mc client is
        # already cached/pulled before private production inputs enter containers.
        for _ in range(60):
            try:
                mc("ready", "recovery", timeout=5)
                break
            except RuntimeError:
                time.sleep(1)
        else:
            raise RuntimeError("Isolated S3 is not ready")
        for bucket in mail.BUCKETS:
            mc("mb", "recovery/" + bucket)
        for bucket, key, name in blob_sources:
            mc("cp", "/backup/" + name, "recovery/" + bucket + "/" + key)
        command(["docker", "volume", "create", "--label", LABEL, config_volume], label="creating private recovery config")
        volume_created = True
        command(["docker", "run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh",
            "--mount", "type=volume,src=" + config_volume + ",dst=/out",
            "--mount", "type=bind,src=" + str(cfg) + ",dst=/in,readonly",
            pg_image, "-c", "cp /in/config.json /out/config.json && chown 2000:2000 /out /out/config.json && chmod 700 /out && chmod 600 /out/config.json"],
            label="preparing private Stalwart config")
        command(["docker", "run", "-d", "--name", mail_name, "--label", LABEL, "--network", network,
            "--cpus", "2", "--memory", "1536m", "--env-file", str(mail_env),
            "--user", "2000:2000",
            "--mount", "type=volume,src=" + config_volume + ",dst=/etc/stalwart",
            "--tmpfs", "/var/lib/stalwart:rw,nosuid,nodev,uid=2000,gid=2000,mode=0700,size=256m",
            stalwart_image], label="starting isolated Stalwart")
        containers.append(mail_name)
        store = {"@type": "S3", "bucket": mail.BUCKETS[0],
                 "region": {"@type": "Custom", "url": "http://" + minio_name + ":9000", "region": "us-east-1"},
                 "accessKeyId": {"@type": "EnvironmentVariable", "variableName": "AWS_ACCESS_KEY_ID"},
                 "secretKey": {"@type": "EnvironmentVariable", "variableName": "AWS_SECRET_ACCESS_KEY"},
                 "sessionToken": {"@type": "None"}}
        datastore = {"@type": "PostgreSql", "host": pg_name, "port": 5432, "database": "atoman_mail",
                     "authUsername": pg_user, "authSecret": {"@type": "EnvironmentVariable", "variableName": "STALWART_PG_PASSWORD"},
                     "useTls": False}
        protocol_client(mail_name, {"action": "configure", "credential": credential,
                                   "data_store": datastore, "blob_store": store})
        command(["docker", "restart", mail_name], label="applying isolated storage configuration")
        verified = protocol_client(mail_name, {"action": "verify", "credential": credential, "canary": canary})
        return {"isolated_network_internal": True, "s3_credentials_disposable": True,
                "postgres_restored_tables": len(actual), "postgres_row_counts_match": True,
                "mail_assets": asset_summary, "published_host_ports": False,
                "protocol_client_network_namespace": "restored_mail_container",
                "protocol_client_image": PYTHON_CLIENT_IMAGE,
                "stalwart_image": stalwart_image, "postgres_image": pg_image, "minio_image": minio_image,
                **verified}
    except Exception:
        if DIAGNOSTICS is not None:
            DIAGNOSTICS.mkdir(mode=0o700, parents=True, exist_ok=True)
            for name in containers:
                for kind, args in (("inspect", ["docker", "inspect", name]), ("logs", ["docker", "logs", name])):
                    try:
                        captured = sp.run(args, capture_output=True, timeout=10)
                        path = DIAGNOSTICS / (name + "." + kind + ".private")
                        path.write_bytes(captured.stdout + captured.stderr)
                        path.chmod(0o600)
                    except (OSError, sp.TimeoutExpired):
                        pass
            print("ISOLATED_FAILURE_DIAGNOSTICS_SAVED_PRIVATELY", flush=True)
        raise
    finally:
        for name in reversed(containers):
            sp.run(["docker", "rm", "-f", "-v", name], stdout=sp.DEVNULL, stderr=sp.DEVNULL)
        if volume_created:
            sp.run(["docker", "volume", "rm", config_volume], stdout=sp.DEVNULL, stderr=sp.DEVNULL)
        if network_created:
            sp.run(["docker", "network", "rm", network], stdout=sp.DEVNULL, stderr=sp.DEVNULL)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ciphertext", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--nas-transfer-receipt", type=Path, required=True)
    parser.add_argument("--identity", type=Path, required=True)
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--postgres-image", default=PG_IMAGE)
    parser.add_argument("--minio-image", default=MINIO_IMAGE)
    parser.add_argument("--mc-image", default="minio/mc:latest")
    args = parser.parse_args()
    os.umask(0o077)
    mail = load("backup-mail")
    mail.private_dir(args.workspace)
    if args.identity.is_symlink() or not args.identity.is_file() or args.identity.stat().st_mode & 0o077:
        raise ValueError("Recovery identity must remain a private local regular file")
    receipt = json.loads(args.receipt.read_text())
    source_id = receipt.get("source_snapshot_id", "")
    if not SNAPSHOT.fullmatch(source_id) or receipt.get("format") != "atoman-encrypted-export-v1":
        raise ValueError("Invalid encrypted backup receipt")
    if args.ciphertext.is_symlink() or not args.ciphertext.is_file():
        raise ValueError("Ciphertext must be a regular file")
    checksum = digest(args.ciphertext)
    if checksum != receipt.get("sha256") or args.ciphertext.stat().st_size != receipt.get("bytes"):
        raise ValueError("NAS-returned ciphertext checksum differs")
    transfer = json.loads(args.nas_transfer_receipt.read_text())
    if (transfer.get("format") != "atoman-mail-nas-transfer-v1" or transfer.get("source_snapshot_id") != source_id
            or transfer.get("ciphertext_sha256") != checksum or transfer.get("nas_roundtrip_verified") is not True):
        raise ValueError("Verified NAS roundtrip evidence is required")
    if shutil.disk_usage(args.workspace).free < args.ciphertext.stat().st_size * 2 + (1 << 30):
        raise ValueError("Insufficient private recovery workspace space")
    with tempfile.TemporaryDirectory(prefix="mail-isolated-recovery-", dir=args.workspace) as temporary:
        workspace = Path(temporary)
        source = workspace / "decrypted"
        source.mkdir(mode=0o700)
        provenance, files = decrypt_selected(args.ciphertext, args.identity, source)
        if provenance.get("source_snapshot_id") != source_id or provenance.get("source_completed_at") != receipt.get("source_completed_at"):
            raise ValueError("Decrypted provenance differs from the NAS receipt")
        result = run_stack(source, workspace, args.postgres_image, args.minio_image, args.mc_image)
        report = {"format": "atoman-mail-nas-recovery-v1", "status": "ok", "restore_succeeded": True,
                  "source_snapshot_id": source_id, "nas_path": transfer["nas_path"],
                  "source_completed_at": receipt["source_completed_at"],
                  "verified_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                  "ciphertext_sha256": checksum, "nas_roundtrip_verified": True,
                  "owner_offline_identity_verified": True,
                  "all_snapshot_files_sha256_verified": True, "verified_file_count": len(files),
                  "production_data_modified": False, **result}
        if args.report.is_symlink():
            raise ValueError("Recovery report cannot be a symbolic link")
        args.report.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        args.report.write_text(json.dumps(report, indent=2) + "\n")
        os.chmod(args.report, 0o600)
        print(json.dumps(report))

if __name__ == "__main__":
    main()

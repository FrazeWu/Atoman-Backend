#!/usr/bin/env python3
"""Consistent encrypted mail recovery inputs: SQL snapshots, R2 bytes, runtime config.

Prefetch before a short, bounded quiesce of explicitly named services. Hold SQL
exported snapshots while services resume and pg_dump/restore runs in isolation.
The source buckets are read-only; only local owned backup files may be pruned.
"""
import argparse
import contextlib
import datetime as dt
import fcntl
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess as sp
import tarfile
import tempfile
import time

BUCKETS = ("atoman-mail-prod", "atoman-mail-pending")
UNIT = re.compile(r"^atoman-[a-z0-9-]+\.service$")
SNAPSHOT = re.compile(r"^[0-9]{8}T[0-9]{6}Z-[a-z0-9]{6,20}$")

def load_pg():
    path = Path(__file__).with_name("backup-postgres.py")
    spec = importlib.util.spec_from_file_location("atoman_mail_pg_backup", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module

def digest(path):
    with path.open("rb") as reader:
        return hashlib.file_digest(reader, "sha256").hexdigest()

def private_dir(path):
    if path.is_symlink():
        raise ValueError("Backup directory cannot be a symbolic link")
    path.mkdir(parents=True, exist_ok=True, mode=0o700)
    if path.stat().st_uid != os.geteuid() or stat.S_IMODE(path.stat().st_mode) & 0o077:
        raise ValueError("Backup directory must be private and owned by the backup user")

def parse_env(paths):
    result = {}
    for path in paths:
        if path.is_symlink() or not path.is_file() or stat.S_IMODE(path.stat().st_mode) & 0o077:
            raise ValueError("Environment file must be a private regular file")
        for line in path.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            key, value = key.strip(), value.strip()
            if value[:1] in ("'", '"') and value[-1:] == value[:1]:
                value = value[1:-1]
            result[key] = value
    return result

class R2:
    def __init__(self, config, allow_http=False):
        self.endpoint = config.get("ATOMAN_MAIL_R2_ENDPOINT") or config.get("S3_ENDPOINT", "")
        if not self.endpoint.startswith("https://") and not (allow_http and self.endpoint.startswith("http://127.0.0.1:")):
            raise ValueError("A secure mail R2 endpoint is required")
        # Dedicated credentials only: never fall back to the media account.
        access = config.get("ATOMAN_MAIL_R2_ACCESS_KEY_ID")
        secret = config.get("ATOMAN_MAIL_R2_SECRET_ACCESS_KEY")
        if not access or not secret:
            raise ValueError("Dedicated mail R2 credentials are missing")
        self.env = {**os.environ, "AWS_ACCESS_KEY_ID": access, "AWS_SECRET_ACCESS_KEY": secret,
                    "AWS_DEFAULT_REGION": "auto", "AWS_EC2_METADATA_DISABLED": "true", "AWS_PAGER": ""}
        self.env.pop("AWS_SESSION_TOKEN", None)
    def call(self, op, *args):
        command = ["aws", "--endpoint-url", self.endpoint, "--no-cli-pager", "s3api", op, *args]
        proc = sp.run(command, env=self.env, capture_output=True, timeout=90)
        if proc.returncode:
            # AWS diagnostics can include endpoint URLs or payload values.
            raise RuntimeError("Private mail object-storage operation failed: " + op)
        return json.loads(proc.stdout or b"{}")
    def inventory(self, bucket):
        if bucket not in BUCKETS:
            raise ValueError("Unexpected bucket")
        raw = self.call("list-objects-v2", "--bucket", bucket)
        # aws automatically paginates; manual --no-paginate is deliberately absent.
        rows = raw.get("Contents", [])
        result = {}
        for row in rows:
            key, etag, size = row["Key"], row["ETag"], row["Size"]
            if not isinstance(key, str) or not isinstance(etag, str) or not isinstance(size, int) or size < 0:
                raise ValueError("Invalid object metadata")
            result[key] = {"etag": etag, "bytes": size}
        return result
    def fetch(self, bucket, key, meta, cache):
        token = object_token(bucket, key, meta)
        path = cache / (token + ".blob")
        hashfile = cache / (token + ".sha256")
        if path.is_symlink() or hashfile.is_symlink():
            raise ValueError("Symlinked blob cache is forbidden")
        if path.is_file() and hashfile.is_file() and path.stat().st_size == meta["bytes"] and digest(path) == hashfile.read_text().strip():
            return path
        temp = cache / (token + ".partial")
        if temp.is_symlink():
            raise ValueError("Symlinked partial blob is forbidden")
        self.call("get-object", "--bucket", bucket, "--key", key, "--if-match", meta["etag"], str(temp))
        if temp.stat().st_size != meta["bytes"]:
            raise ValueError("Downloaded mail object length differs")
        os.chmod(temp, 0o600)
        os.replace(temp, path)
        hashfile.write_text(digest(path) + "\n")
        os.chmod(hashfile, 0o600)
        return path

def object_token(bucket, key, meta):
    return hashlib.sha256((bucket + "\0" + key + "\0" + meta["etag"] + "\0" + str(meta["bytes"])).encode()).hexdigest()

def cached_inventory(client, cache):
    result = {bucket: client.inventory(bucket) for bucket in BUCKETS}
    required = sum(meta["bytes"] for bucket, values in result.items() for key, meta in values.items()
                   if not (cache / (object_token(bucket, key, meta) + ".blob")).is_file())
    if shutil.disk_usage(cache).free < required * 2 + (1 << 30):
        raise ValueError("Not enough disk space for mail objects and archive")
    for bucket, values in result.items():
        for key, meta in values.items():
            client.fetch(bucket, key, meta, cache)
    return result

def prune_blob_cache(cache, inventory):
    # A successful archive owns its bytes. Keep only cache entries for that
    # current inventory, and never follow links or touch foreign filenames.
    keep = {object_token(bucket, key, meta) for bucket, values in inventory.items() for key, meta in values.items()}
    removed = 0
    for path in cache.iterdir():
        if path.is_symlink() or not path.is_file() or not re.fullmatch(r"[0-9a-f]{64}\.(blob|sha256|partial)", path.name):
            continue
        if path.name.split(".")[0] not in keep:
            path.unlink()
            removed += 1
    return removed

@contextlib.contextmanager
def quiesce(units, max_seconds):
    if not units or len(set(units)) != len(units) or any(not UNIT.fullmatch(x) for x in units):
        raise ValueError("Explicit unique atoman systemd service names are required")
    if not 10 <= max_seconds <= 300:
        raise ValueError("Maintenance window must be bounded between 10 and 300 seconds")
    # Preflight every writer before stopping any of them. Misspelled/missing
    # units must not produce a falsely consistent recovery point.
    for unit in units:
        loaded = sp.run(["systemctl", "show", "-p", "LoadState", "--value", unit],
                        capture_output=True, text=True, timeout=10)
        if loaded.returncode or loaded.stdout.strip() != "loaded":
            raise RuntimeError("Mail writer unit is not loaded: " + unit)
        if sp.run(["systemctl", "is-active", "--quiet", unit], timeout=10).returncode != 0:
            raise RuntimeError("Mail writer unit is not active: " + unit)
    stopped = []
    previous = signal.getsignal(signal.SIGALRM)
    def expired(_number, _frame):
        raise TimeoutError("Mail snapshot maintenance window exceeded its limit")
    try:
        signal.signal(signal.SIGALRM, expired)
        signal.alarm(max_seconds)
        for unit in units:
            stopped.append(unit)
            sp.run(["systemctl", "stop", unit], check=True, timeout=min(max_seconds, 60))
        yield
    finally:
        signal.alarm(0)
        signal.signal(signal.SIGALRM, previous)
        failures = []
        for unit in reversed(stopped):
            try:
                sp.run(["systemctl", "start", unit], check=True, timeout=90)
            except (sp.CalledProcessError, sp.TimeoutExpired):
                failures.append(unit)
        if failures:
            raise RuntimeError("Services require recovery after backup: " + ", ".join(failures))

@contextlib.contextmanager
def exported_snapshot(pg, container, user, database):
    process = sp.Popen(["docker", "exec", "-i", container, "psql", "-X", "-qAt",
                       "-v", "ON_ERROR_STOP=1", "-U", user, "-d", database],
                       stdin=sp.PIPE, stdout=sp.PIPE, stderr=sp.PIPE, text=True)
    try:
        process.stdin.write("BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\nSELECT pg_export_snapshot();\n")
        process.stdin.flush()
        token = process.stdout.readline().strip()
        if not re.fullmatch(r"[0-9A-Fa-f-]+", token):
            raise RuntimeError("Could not export database snapshot")
        tables = json.loads(pg.sql(container, user, database,
            "SELECT coalesce(json_agg(tablename ORDER BY tablename),'[]') FROM pg_tables WHERE schemaname='public'"))
        yield {"database": database, "snapshot": token, "tables": tables}
    finally:
        if process.poll() is None:
            try:
                process.stdin.write("ROLLBACK;\n\\q\n")
                process.stdin.flush()
                process.communicate(timeout=10)
            except (BrokenPipeError, sp.TimeoutExpired):
                process.kill()
                process.communicate()

def dump_snapshot(pg, container, user, state, target):
    database = state["database"]
    expected = pg.counts(container, user, database, state["tables"], state["snapshot"])
    output = target / (database + ".dump")
    started = time.monotonic()
    with output.open("xb") as writer:
        sp.run(["docker", "exec", container, "pg_dump", "-U", user, "-d", database,
                "--format=custom", "--compress=1", "--lock-wait-timeout=15000",
                "--snapshot=" + state["snapshot"]], stdout=writer, check=True)
    return {"database": database, "dump": output.name, "bytes": output.stat().st_size,
            "sha256": digest(output), "public_table_counts": expected,
            "backup_seconds": round(time.monotonic() - started, 3)}

def collect_config(paths):
    files = []
    for source in paths:
        if source.is_symlink() or not source.exists():
            raise ValueError("Runtime config must exist and cannot be a symbolic link")
        candidates = sorted(source.rglob("*")) if source.is_dir() else [source]
        for path in candidates:
            if path.is_symlink():
                raise ValueError("Runtime config contains a symbolic link")
            if path.is_dir():
                continue
            if not stat.S_ISREG(path.stat().st_mode):
                raise ValueError("Runtime config must contain only regular files")
            if path.stat().st_size > 32 << 20:
                raise ValueError("Unexpectedly large runtime config file")
            files.append((path.as_posix().lstrip("/"), path))
    if len({name for name, _ in files}) != len(files):
        raise ValueError("Duplicate runtime configuration source")
    return files

def pack_assets(client, inventory, cache, configs, target):
    manifest = {"format": "atoman-mail-assets-v1", "buckets": {}, "runtime_files": {}}
    archive_path = target / "mail-assets.tar"
    with tarfile.open(archive_path, "x") as archive:
        for bucket, objects in inventory.items():
            manifest["buckets"][bucket] = {}
            for key, meta in sorted(objects.items()):
                file = client.fetch(bucket, key, meta, cache)
                name = "blobs/" + file.name
                archive.add(file, arcname=name, recursive=False)
                manifest["buckets"][bucket][key] = {**meta, "member": name, "sha256": digest(file)}
        for name, file in configs:
            member = "runtime/" + name
            archive.add(file, arcname=member, recursive=False)
            manifest["runtime_files"][name] = {"member": member, "bytes": file.stat().st_size, "sha256": digest(file)}
        data = json.dumps(manifest, sort_keys=True).encode()
        item = tarfile.TarInfo("mail-assets-manifest.json")
        item.mode, item.size = 0o600, len(data)
        archive.addfile(item, io.BytesIO(data))
    os.chmod(archive_path, 0o600)
    verify_assets(archive_path)
    return {"name": archive_path.name, "bytes": archive_path.stat().st_size,
            "sha256": digest(archive_path), "verified": True}

def verify_assets(path):
    with tarfile.open(path, "r") as archive:
        members = archive.getmembers()
        if len({m.name for m in members}) != len(members) or any(not m.isfile() for m in members):
            raise ValueError("Mail asset archive contains duplicates or non-files")
        manifest = json.load(archive.extractfile("mail-assets-manifest.json"))
        if manifest.get("format") != "atoman-mail-assets-v1" or set(manifest.get("buckets", {})) != set(BUCKETS):
            raise ValueError("Unexpected mail asset manifest")
        expected = {"mail-assets-manifest.json"}
        records = list(manifest.get("runtime_files", {}).values())
        for values in manifest["buckets"].values():
            records.extend(values.values())
        for record in records:
            name = record["member"]
            if name in expected or name.startswith("/") or ".." in Path(name).parts:
                raise ValueError("Unsafe or duplicate mail asset member")
            expected.add(name)
            member = archive.getmember(name)
            with archive.extractfile(member) as reader:
                actual = hashlib.file_digest(reader, "sha256").hexdigest()
            if member.size != record["bytes"] or actual != record["sha256"]:
                raise ValueError("Mail asset checksum differs")
        if expected != {m.name for m in members}:
            raise ValueError("Mail asset archive has unrecorded members")
    return {"objects": sum(len(x) for x in manifest["buckets"].values()), "runtime_files": len(manifest["runtime_files"])}

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", default="atoman-dev-postgres-1")
    parser.add_argument("--database", action="append", required=True)
    parser.add_argument("--destination", type=Path, required=True)
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--env-file", type=Path, action="append", required=True)
    parser.add_argument("--runtime-config", type=Path, action="append", required=True)
    parser.add_argument("--quiesce-unit", action="append", required=True,
                        help="Stop order; services are resumed in reverse order")
    parser.add_argument("--max-pause-seconds", type=int, default=120)
    parser.add_argument("--retain", type=int, default=1)
    parser.add_argument("--verification-profile", choices=("full", "data-and-mail"), default="full")
    parser.add_argument("--reference-full-backup", type=Path,
                        help="Required for the one-off data-and-mail post-deployment profile")
    args = parser.parse_args()
    if os.geteuid() != 0 or not 10 <= args.max_pause_seconds <= 300 or args.retain < 1:
        parser.error("Requires root, a bounded maintenance window and positive retention")
    if set(args.database) != {"atoman_prod", "atoman_dev", "atoman_mail"}:
        parser.error("Mail recovery must include all three existing Atoman databases")
    reference = None
    if args.verification_profile == "data-and-mail":
        source = args.reference_full_backup
        if source is None or source.is_symlink() or not (source / "COMPLETE").is_file():
            parser.error("The optimized profile requires a completed prior full paired recovery")
        old = json.loads((source / "manifest.json").read_text())
        if (old.get("complete") is not True or old.get("mail_assets_verified") is not True
                or old.get("verification_profile", "full") != "full"
                or {r.get("database") for r in old.get("backups", [])} != set(args.database)
                or any(r.get("restore_verified") is not True or r.get("restore_profile", "full") != "full"
                       for r in old.get("backups", []))):
            parser.error("Reference backup did not fully restore all three databases")
        reference = {"snapshot_id": source.name, "completed_at": old["completed_at"],
                     "manifest_sha256": digest(source / "manifest.json")}
    os.umask(0o077)
    for sig in (signal.SIGTERM, signal.SIGINT):
        signal.signal(sig, lambda number, frame: (_ for _ in ()).throw(SystemExit(128 + number)))
    private_dir(args.destination)
    private_dir(args.cache)
    client = R2(parse_env(args.env_file))
    pg = load_pg()
    info = json.loads(sp.check_output(["docker", "inspect", args.container]))[0]
    env = dict(x.split("=", 1) for x in info["Config"]["Env"] if "=" in x)
    user = env.get("POSTGRES_USER", "postgres")
    largest = max(int(pg.sql(args.container, user, db, "SELECT pg_database_size(current_database())")) for db in args.database)
    if shutil.disk_usage(args.destination).free < largest * 3 + (1 << 30):
        raise ValueError("Not enough free space for verified mail recovery")
    with open("/run/lock/atoman-backup.lock", "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        cached_inventory(client, args.cache)
        target = Path(tempfile.mkdtemp(prefix=dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ-"), dir=args.destination))
        manifest = {"format": "atoman-postgres-logical-v1", "complete": False,
                    "started_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                     "container": args.container, "image_id": info["Image"], "backups": [], "additional_files": [],
                    "verification_profile": args.verification_profile, "full_verification_reference": reference}
        try:
            with contextlib.ExitStack() as stack:
                paused_at = time.monotonic()
                with quiesce(args.quiesce_unit, args.max_pause_seconds):
                    states = [stack.enter_context(exported_snapshot(pg, args.container, user, db)) for db in args.database]
                    inventory = cached_inventory(client, args.cache)
                    for bucket in BUCKETS:
                        if client.inventory(bucket) != inventory[bucket]:
                            raise RuntimeError("Mail objects changed while writers were quiesced")
                    # Copy configs during quiesce; they may contain generated keys.
                    private_config = target / "config-capture"
                    private_dir(private_config)
                    captured = []
                    for name, file in collect_config(args.runtime_config):
                        copy = private_config / hashlib.sha256(name.encode()).hexdigest()
                        shutil.copyfile(file, copy)
                        captured.append((name, copy))
                    manifest["quiesced_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
                manifest["pause_seconds"] = round(time.monotonic() - paused_at, 3)
                print("MAIL_SERVICES_RESUMED", flush=True)
                # Every blob in inventory is cached; no source writes or service downtime below.
                manifest["additional_files"].append(pack_assets(client, inventory, args.cache, captured, target))
                shutil.rmtree(private_config)
                for state in states:
                    manifest["backups"].append(dump_snapshot(pg, args.container, user, state, target))
            with (target / "roles.sql").open("xb") as writer:
                sp.run(["docker", "exec", args.container, "pg_dumpall", "-U", user, "--globals-only"], stdout=writer, check=True)
            if args.verification_profile == "full":
                pg.verify(info["Image"], target, manifest["backups"])
            else:
                # Restore every application/dev row from the new snapshot and
                # compare every table, without repeating expensive site-wide
                # secondary indexes. Stalwart still receives a full restore.
                # The archive itself always contains every index/constraint.
                pg.verify(info["Image"], target, manifest["backups"],
                          data_only_databases={"atoman_prod", "atoman_dev"})
            manifest.update(complete=True, mail_assets_verified=True, completed_at=dt.datetime.now(dt.timezone.utc).isoformat())
            (target / "manifest.json").write_text(json.dumps(manifest, indent=2))
            (target / "COMPLETE").touch(mode=0o600)
            pg.prune_managed_backups(args.destination, args.retain)
            prune_blob_cache(args.cache, inventory)
            print(json.dumps({"backup_directory": str(target), "mail_assets_verified": True, "databases": args.database}), flush=True)
        finally:
            (target / "manifest.json").write_text(json.dumps(manifest, indent=2))

if __name__ == "__main__":
    main()


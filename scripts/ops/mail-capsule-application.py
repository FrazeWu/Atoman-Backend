#!/usr/bin/env python3
"""Build and verify the application half of a same-snapshot mail recovery capsule.

build(source_dir, destination, *, pg_image=None) reads ONLY a completed,
restore-verified atoman_prod.dump and its manifest. It exports Users plus every
actual public mail_* table, their owned objects and exact TOC dependencies.
No full-site table data or unrelated indexes are restored. Source absence is
preserved, not converted into newly created empty tables.

verify(directory, pg_image) restores application-mail.sql in a fresh,
network=none PostgreSQL container with tmpfs data and checks source row counts
and mailbox/user associations. Its return value is the v2 gate's mail_metadata.
The caller must independently bind this manifest and payload to its immutable
source manifest / encrypted NAS round-trip; this helper does not claim NAS I/O.
No dump contents, credentials, SQL errors or command stderr are printed.
"""
import argparse
import contextlib
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import tempfile
import threading
import time
import uuid

REQUIRED_MAIL_TABLES = (
    "mail_accounts", "mail_invites", "mail_access_credentials",
    "mail_pending_sends", "mail_drafts", "mail_uploads", "mail_preferences",
    "mail_action_receipts", "mail_retention",
)
MANIFEST_NAME = "application-mail-manifest.json"
PAYLOAD_NAME = "application-mail.sql"
MANIFEST_FORMAT = "atoman-mail-application-capsule-v1"
REPORT_FORMAT = "atoman-mail-application-recovery-v1"
HASH = re.compile(r"^[0-9a-f]{64}$")
TABLE = re.compile(r"^(?:Users|mail_[A-Za-z0-9_]+)$")
PINNED_IMAGE = re.compile(r"^(?:sha256:[0-9a-f]{64}|[A-Za-z0-9_./:-]+@sha256:[0-9a-f]{64})$")
TOC_KINDS = sorted((
    "MATERIALIZED VIEW DATA", "SEQUENCE OWNED BY", "PUBLICATION TABLES IN SCHEMA",
    "FOREIGN DATA WRAPPER", "PROCEDURAL LANGUAGE", "TEXT SEARCH CONFIGURATION",
    "TEXT SEARCH DICTIONARY", "TEXT SEARCH PARSER", "TEXT SEARCH TEMPLATE",
    "BLOB COMMENTS", "BLOB METADATA", "DATABASE PROPERTIES", "FOREIGN TABLE",
    "MATERIALIZED VIEW", "DOMAIN CONSTRAINT", "TABLE ATTACH", "TABLE DATA",
    "SEQUENCE SET", "FK CONSTRAINT", "INDEX ATTACH", "ROW SECURITY",
    "SECURITY LABEL", "OPERATOR CLASS", "OPERATOR FAMILY", "PUBLICATION TABLE",
    "USER MAPPING", "ACCESS METHOD", "EVENT TRIGGER", "SHELL TYPE",
    "DEFAULT ACL", "SCHEMA", "EXTENSION", "TYPE", "DOMAIN", "COLLATION",
    "FUNCTION", "PROCEDURE", "AGGREGATE", "OPERATOR", "TRANSFORM", "CAST",
    "TABLE", "VIEW", "SEQUENCE", "DEFAULT", "CONSTRAINT", "INDEX",
    "RULE", "TRIGGER", "POLICY", "STATISTICS", "COMMENT", "ACL",
    "SERVER", "PUBLICATION", "SUBSCRIPTION", "ENCODING", "STDSTRINGS",
    "SEARCHPATH", "DATABASE", "BLOB", "BLOBS",
), key=len, reverse=True)
RELATIONS = {"TABLE", "FOREIGN TABLE", "VIEW", "MATERIALIZED VIEW"}
TABLE_OWNED = {
    "DEFAULT", "CONSTRAINT", "FK CONSTRAINT", "RULE", "TRIGGER", "POLICY",
    "ROW SECURITY", "TABLE ATTACH",
}


class CapsuleError(RuntimeError):
    """Only fixed, non-sensitive diagnostic codes cross the operator boundary."""


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def regular(path):
    path = Path(path)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode):
        raise CapsuleError("capsule_regular_file_required")
    return info


def private_directory(path, create=False):
    path = Path(path).absolute()
    if create:
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or stat.S_IMODE(info.st_mode) & 0o077:
        raise CapsuleError("capsule_private_directory_required")
    return path.resolve()


def private_write(path, data):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def command(args, *, stdin=None, stdout=None, timeout=300, label="capsule_command"):
    try:
        result = subprocess.run(
            args, stdin=stdin, stdout=stdout if stdout is not None else subprocess.PIPE,
            stderr=subprocess.PIPE, timeout=timeout, check=False,
        )
    except (OSError, subprocess.TimeoutExpired):
        raise CapsuleError(label + "_unavailable") from None
    if result.returncode:
        raise CapsuleError(label + "_failed") from None
    return result.stdout or b""


def image_ref(value):
    if not isinstance(value, str) or not PINNED_IMAGE.fullmatch(value):
        raise CapsuleError("capsule_pinned_postgres_image_required")
    return value


def mount(directory, target):
    value = str(directory)
    if any(c in value for c in (",", "\n", "\r")):
        raise CapsuleError("capsule_unsafe_mount_path")
    return "type=bind,source=" + value + ",target=" + target + ",readonly"


@contextlib.contextmanager
def interruptions():
    previous = {}
    if threading.current_thread() is threading.main_thread():
        def stop(_signum, _frame):
            raise CapsuleError("capsule_interrupted")
        for signum in (signal.SIGTERM, signal.SIGINT):
            previous[signum] = signal.getsignal(signum)
            signal.signal(signum, stop)
    try:
        yield
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)


@contextlib.contextmanager
def command_container(image, mounts):
    """Name each disposable reader so failures/cancellation can remove it."""
    name = "atoman-app-capsule-read-" + uuid.uuid4().hex[:12]
    args = ["docker", "run", "--rm", "--name", name,
            "--label", "atoman.ops=application-mail-capsule",
            "--network", "none", "--memory", "1g", "--cpus", "2"]
    for directory, target in mounts:
        args += ["--mount", mount(directory, target)]
    try:
        yield args + [image_ref(image)]
    finally:
        subprocess.run(["docker", "rm", "-f", "-v", name],
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=30)


def parse_toc(text):
    entries, current = [], None
    version = re.search(r"^;\s*Dumped from database version:\s*(\d+)", text, re.M)
    if version is None:
        raise CapsuleError("capsule_source_postgres_version_missing")
    for line in text.splitlines():
        match = re.match(r"^(\d+);\s+\d+\s+\d+\s+(.*)$", line)
        if match:
            rest = match.group(2)
            kind = next((k for k in TOC_KINDS if rest.startswith(k + " ")), "UNKNOWN")
            fields = rest[len(kind):].strip().split(None, 1) if kind != "UNKNOWN" else []
            namespace = fields[0] if fields else ""
            tagged = fields[1] if len(fields) == 2 else ""
            # Owner is the final token; extension entries may have a blank owner.
            tag = tagged.rsplit(" ", 1)[0] if " " in tagged else tagged
            current = {"id": int(match.group(1)), "kind": kind, "namespace": namespace,
                       "tag": tag, "entry": line, "dependencies": []}
            entries.append(current)
        elif current is not None and line.startswith(";") and "depends on:" in line:
            current["dependencies"] = [int(n) for n in line.split("depends on:", 1)[1].split()]
    if not entries or len({e["id"] for e in entries}) != len(entries):
        raise CapsuleError("capsule_invalid_source_toc")
    return entries, int(version.group(1))


def select_toc(entries, source_tables):
    """Use pg_restore's dependency graph, never index-name/table-name guesses."""
    by_id = {entry["id"]: entry for entry in entries}
    relations = {e["id"]: e for e in entries if e["kind"] in RELATIONS}
    actual = {}
    for e in relations.values():
        if e["namespace"] == "public" and (e["tag"] == "Users" or e["tag"].startswith("mail_")):
            if e["kind"] != "TABLE" or not TABLE.fullmatch(e["tag"]) or e["tag"] in actual:
                raise CapsuleError("capsule_unsupported_mail_relation")
            actual[e["tag"]] = e["id"]
        elif e["namespace"] != "public" and e["tag"].startswith("mail_"):
            raise CapsuleError("capsule_nonpublic_mail_relation_requires_manifest")
    if set(actual) != set(source_tables) or "Users" not in actual:
        raise CapsuleError("capsule_source_table_presence_mismatch")
    roots = set(actual.values())
    chosen = set(roots)
    changed = True
    while changed:
        previous = set(chosen)
        for e in entries:
            deps = set(e["dependencies"])
            direct_tables = deps & set(relations)
            owned_name = e["tag"].split(" ", 1)[0]
            if e["kind"] in TABLE_OWNED:
                owned = e["namespace"] == "public" and owned_name in actual
            elif e["kind"] in {"TABLE DATA", "INDEX", "STATISTICS", "SEQUENCE"}:
                owned = bool(direct_tables) and direct_tables <= roots
            elif e["kind"] in {"SEQUENCE OWNED BY", "SEQUENCE SET"}:
                owned = any(d in chosen and by_id.get(d, {}).get("kind") == "SEQUENCE" for d in deps)
            elif e["kind"] == "INDEX ATTACH":
                owned = bool(deps) and deps <= chosen
            elif e["kind"] == "COMMENT":
                owned = bool(deps & chosen) and direct_tables <= roots
            else:
                owned = False
            if owned:
                chosen.add(e["id"])
        pending = list(chosen)
        while pending:
            entry = by_id[pending.pop()]
            if entry["kind"] == "UNKNOWN":
                raise CapsuleError("capsule_unsupported_toc_dependency")
            for dependency in entry["dependencies"]:
                if dependency not in by_id:
                    raise CapsuleError("capsule_toc_dependency_missing")
                target = by_id[dependency]
                if target["kind"] in RELATIONS and dependency not in roots:
                    raise CapsuleError("capsule_external_table_dependency")
                if target["kind"] in {"TABLE DATA", "MATERIALIZED VIEW DATA", "BLOBS", "BLOB"}:
                    if target["kind"] != "TABLE DATA" or not set(target["dependencies"]) & roots:
                        raise CapsuleError("capsule_external_data_dependency")
                if dependency not in chosen:
                    chosen.add(dependency)
                    pending.append(dependency)
        changed = chosen != previous
    selected = [e for e in entries if e["id"] in chosen]
    data_tables = {e["tag"] for e in selected if e["kind"] == "TABLE DATA" and e["namespace"] == "public"}
    if data_tables != set(source_tables):
        raise CapsuleError("capsule_source_data_presence_mismatch")
    if {e["id"] for e in selected if e["kind"] in RELATIONS} != roots:
        raise CapsuleError("capsule_unexpected_relation_selected")
    return selected


def validated_counts(value):
    if not isinstance(value, dict) or not value or "Users" not in value:
        raise CapsuleError("capsule_source_counts_missing")
    if any(not TABLE.fullmatch(k) or isinstance(v, bool) or not isinstance(v, int) or v < 0
           for k, v in value.items()):
        raise CapsuleError("capsule_invalid_table_counts")
    return dict(sorted(value.items()))


def build(source_dir: Path, destination: Path, *, pg_image=None) -> dict:
    source = private_directory(source_dir)
    target = private_directory(destination, create=True)
    original_manifest = source / "manifest.json"
    original_dump = source / "atoman_prod.dump"
    regular(original_manifest)
    dump_stat = regular(original_dump)
    raw_manifest = original_manifest.read_bytes()
    manifest = json.loads(raw_manifest)
    matches = [r for r in manifest.get("backups", []) if r.get("database") == "atoman_prod"]
    if manifest.get("complete") is not True or len(matches) != 1:
        raise CapsuleError("capsule_completed_source_required")
    record = matches[0]
    if record.get("dump") != "atoman_prod.dump" or record.get("restore_verified") is not True:
        raise CapsuleError("capsule_verified_source_required")
    expected_hash = record.get("sha256")
    if not isinstance(expected_hash, str) or not HASH.fullmatch(expected_hash):
        raise CapsuleError("capsule_source_hash_missing")
    if dump_stat.st_size != record.get("bytes") or digest(original_dump) != expected_hash:
        raise CapsuleError("capsule_source_dump_integrity_failed")
    raw_counts = record.get("public_table_counts")
    if not isinstance(raw_counts, dict):
        raise CapsuleError("capsule_source_counts_missing")
    counts = validated_counts({k: v for k, v in raw_counts.items()
                               if k == "Users" or k.startswith("mail_")})
    absent = sorted(set(REQUIRED_MAIL_TABLES) - set(counts))
    phase = "pre_activation" if absent else "active"
    image = image_ref(pg_image or manifest.get("image_id"))
    if (target / MANIFEST_NAME).exists() or (target / PAYLOAD_NAME).exists():
        raise CapsuleError("capsule_destination_has_outputs")
    lock = target / ".application-build.lock"
    lock_fd = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    os.close(lock_fd)
    try:
        with interruptions(), tempfile.TemporaryDirectory(prefix=".application-build-", dir=target) as work:
            work = Path(work)
            with command_container(image, [(source, "/source")]) as prefix:
                raw_toc = command(prefix + ["pg_restore", "--list", "--verbose", "/source/atoman_prod.dump"],
                                  label="capsule_read_source_toc").decode("utf-8")
            entries, major = parse_toc(raw_toc)
            selected = select_toc(entries, counts)
            toc_path = work / "selected.toc"
            private_write(toc_path, ("\n".join(e["entry"] for e in selected) + "\n").encode())
            sql_path = work / PAYLOAD_NAME
            fd = os.open(sql_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, "wb") as output:
                with command_container(image, [(source, "/source"), (work, "/selection")]) as prefix:
                    command(prefix + ["pg_restore", "--no-owner", "--no-acl", "--no-security-labels",
                                      "--use-list=/selection/selected.toc", "--file=-",
                                      "/source/atoman_prod.dump"], stdout=output, timeout=900,
                            label="capsule_extract_source_objects")
                output.flush()
                os.fsync(output.fileno())
            if digest(original_dump) != expected_hash or original_manifest.read_bytes() != raw_manifest:
                raise CapsuleError("capsule_source_changed_during_extraction")
            payload = {"name": PAYLOAD_NAME, "bytes": sql_path.stat().st_size, "sha256": digest(sql_path)}
            if payload["bytes"] == 0:
                raise CapsuleError("capsule_empty_payload")
            result = {
                "format": MANIFEST_FORMAT, "phase": phase,
                "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                "source_snapshot_id": source.name, "source_database": "atoman_prod",
                "source_dump_sha256": expected_hash, "source_dump_bytes": dump_stat.st_size,
                "source_manifest_sha256": hashlib.sha256(raw_manifest).hexdigest(),
                "source_pg_major": major, "extraction_image": image,
                "source_tables": counts, "expected_row_counts": counts,
                "required_mail_tables": list(REQUIRED_MAIL_TABLES),
                "absent_mail_tables": absent,
                "table_presence": {k: k in counts for k in sorted(set(counts) | set(REQUIRED_MAIL_TABLES))},
                "selected_toc": [{"entry": e["entry"], "dependencies": e["dependencies"]} for e in selected],
                "artifact": payload,
                "schema_normalization": {"ownership_and_acl_omitted": True, "security_labels_omitted": True},
            }
            private_write(work / MANIFEST_NAME, (json.dumps(result, indent=2, sort_keys=True) + "\n").encode())
            os.replace(sql_path, target / PAYLOAD_NAME)
            os.replace(work / MANIFEST_NAME, target / MANIFEST_NAME)
            return result
    finally:
        lock.unlink(missing_ok=True)


def sql(container, query):
    return command(["docker", "exec", container, "psql", "-X", "-qAt",
                    "-v", "ON_ERROR_STOP=1", "-U", "atoman_capsule", "-d", "atoman_capsule",
                    "-c", query], label="capsule_verification_query").decode().strip()


def quote_ident(name):
    return '"' + name.replace('"', '""') + '"'


@contextlib.contextmanager
def isolated_postgres(directory, image):
    name = "atoman-app-capsule-verify-" + uuid.uuid4().hex[:12]
    created = False
    with interruptions():
        try:
            command(["docker", "run", "-d", "--name", name,
                     "--label", "atoman.ops=application-mail-capsule",
                     "--network", "none", "--cpus", "2", "--memory", "2g", "--pids-limit", "128",
                     "--tmpfs", "/var/lib/postgresql/data:rw,nosuid,nodev,size=1024m",
                     "-e", "POSTGRES_USER=atoman_capsule", "-e", "POSTGRES_DB=atoman_capsule",
                     "-e", "POSTGRES_HOST_AUTH_METHOD=trust",
                     "--mount", mount(directory, "/capsule"), image_ref(image),
                     "-c", "maintenance_work_mem=128MB"], label="capsule_start_isolated_postgres")
            created = True
            inspection = json.loads(command(["docker", "inspect", name], label="capsule_inspect_isolation"))[0]
            if inspection["HostConfig"]["NetworkMode"] != "none" or inspection["HostConfig"].get("PortBindings"):
                raise CapsuleError("capsule_isolation_not_confirmed")
            for _ in range(60):
                ready = subprocess.run(["docker", "exec", name, "pg_isready", "-U", "atoman_capsule"],
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
                if ready.returncode == 0:
                    break
                time.sleep(1)
            else:
                raise CapsuleError("capsule_isolated_postgres_not_ready")
            yield name
        finally:
            removed = subprocess.run(["docker", "rm", "-f", "-v", name],
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=45)
            if created and removed.returncode:
                raise CapsuleError("capsule_isolated_cleanup_failed")


def count_rows(container, tables):
    query = "\n".join("SELECT json_build_array('%s',count(*)) FROM public.%s;" %
                      (name.replace("'", "''"), quote_ident(name)) for name in sorted(tables))
    return dict(json.loads(line) for line in sql(container, query).splitlines() if line)


def check_links(container, tables):
    columns = json.loads(sql(container,
        "SELECT coalesce(json_object_agg(table_name,cols),'{}') FROM "
        "(SELECT table_name,json_agg(column_name) cols FROM information_schema.columns "
        "WHERE table_schema='public' GROUP BY table_name) s"))
    if "uuid" not in columns.get("Users", []):
        raise CapsuleError("capsule_users_uuid_missing")
    if sql(container, 'SELECT (count(*)=count(uuid) AND count(*)=count(DISTINCT uuid)) FROM public."Users"') != "t":
        raise CapsuleError("capsule_users_uuid_not_unique")
    for table in tables:
        if table == "Users":
            continue
        known = ["target_user_id", "created_by"] if table == "mail_invites" else (
            ["user_id"] if table in REQUIRED_MAIL_TABLES else [])
        candidates = set(known) | ({"user_id", "target_user_id", "created_by"} & set(columns[table]))
        for field in sorted(candidates):
            if field not in columns[table]:
                raise CapsuleError("capsule_mail_user_column_missing")
            required = "m." + quote_ident(field) + " IS NULL OR " if field in known else ""
            query = ("SELECT count(*) FROM public.%s m LEFT JOIN public.\"Users\" u ON u.uuid=m.%s "
                     "WHERE %s(m.%s IS NOT NULL AND u.uuid IS NULL)") % (
                         quote_ident(table), quote_ident(field), required, quote_ident(field))
            if sql(container, query) != "0":
                raise CapsuleError("capsule_mail_user_link_mismatch")
    if "mail_accounts" in tables:
        if not {"user_id", "status", "stalwart_account_id"} <= set(columns["mail_accounts"]):
            raise CapsuleError("capsule_account_columns_missing")
        if sql(container, "SELECT count(*) FROM public.mail_accounts WHERE status='active' "
                          "AND nullif(stalwart_account_id,'') IS NULL") != "0":
            raise CapsuleError("capsule_active_account_link_missing")
    if "mail_access_credentials" in tables:
        if "mail_accounts" not in tables:
            if tables["mail_access_credentials"]:
                raise CapsuleError("capsule_credential_accounts_missing")
        elif sql(container, "SELECT count(*) FROM public.mail_access_credentials c "
                 "LEFT JOIN public.mail_accounts a ON a.user_id=c.user_id "
                 "AND a.stalwart_account_id=c.stalwart_account_id WHERE a.id IS NULL") != "0":
            raise CapsuleError("capsule_credential_account_link_mismatch")
    if {"mail_pending_sends", "mail_drafts"} <= set(tables) and "draft_id" in columns["mail_pending_sends"]:
        if sql(container, "SELECT count(*) FROM public.mail_pending_sends p "
               "JOIN public.mail_drafts d ON d.id=p.draft_id WHERE d.user_id<>p.user_id") != "0":
            raise CapsuleError("capsule_draft_owner_link_mismatch")


def verify(directory: Path, pg_image: str) -> dict:
    directory = private_directory(directory)
    manifest_path = directory / MANIFEST_NAME
    payload = directory / PAYLOAD_NAME
    for path in (manifest_path, payload):
        if regular(path).st_mode & 0o077:
            raise CapsuleError("capsule_private_file_required")
    raw_manifest = manifest_path.read_bytes()
    manifest = json.loads(raw_manifest)
    if manifest.get("format") != MANIFEST_FORMAT or manifest.get("source_database") != "atoman_prod":
        raise CapsuleError("capsule_manifest_format_invalid")
    tables = validated_counts(manifest.get("source_tables"))
    absent = sorted(set(REQUIRED_MAIL_TABLES) - set(tables))
    phase = "pre_activation" if absent else "active"
    if manifest.get("phase") != phase or manifest.get("absent_mail_tables") != absent:
        raise CapsuleError("capsule_table_presence_invalid")
    if manifest.get("expected_row_counts") != tables or not HASH.fullmatch(str(manifest.get("source_dump_sha256", ""))):
        raise CapsuleError("capsule_source_binding_invalid")
    artifact = manifest.get("artifact", {})
    if (artifact.get("name") != PAYLOAD_NAME or artifact.get("bytes") != payload.stat().st_size
            or not HASH.fullmatch(str(artifact.get("sha256", ""))) or digest(payload) != artifact["sha256"]):
        raise CapsuleError("capsule_payload_integrity_failed")
    with isolated_postgres(directory, pg_image) as container:
        major = int(sql(container, "SHOW server_version_num")) // 10000
        if major != manifest.get("source_pg_major"):
            raise CapsuleError("capsule_postgres_major_mismatch")
        command(["docker", "exec", container, "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1",
                 "-U", "atoman_capsule", "-d", "atoman_capsule", "-f", "/capsule/" + PAYLOAD_NAME],
                stdout=subprocess.DEVNULL, timeout=300, label="capsule_restore")
        actual_names = json.loads(sql(container,
            "SELECT coalesce(json_agg(tablename ORDER BY tablename),'[]') FROM pg_tables "
            "WHERE schemaname='public'"))
        if set(actual_names) != set(tables):
            raise CapsuleError("capsule_restored_table_presence_mismatch")
        actual = count_rows(container, tables)
        if actual != tables:
            raise CapsuleError("capsule_restored_row_counts_mismatch")
        check_links(container, actual)
        if digest(payload) != artifact["sha256"] or manifest_path.read_bytes() != raw_manifest:
            raise CapsuleError("capsule_changed_during_verification")
    return {
        "format": REPORT_FORMAT, "phase": phase, "source_database": "atoman_prod",
        "source_snapshot_id": manifest["source_snapshot_id"],
        "source_dump_sha256": manifest["source_dump_sha256"],
        "source_manifest_sha256": manifest["source_manifest_sha256"],
        "source_tables": tables, "restored_tables": actual,
        "expected_row_counts": tables, "restored_row_counts": actual,
        "absent_mail_tables": absent, "table_counts_match": True,
        "user_links_verified": True, "credential_account_links_verified": True,
        "draft_owner_links_verified": True, "restore_succeeded": True,
        "isolated_network_none": True, "cleanup_succeeded": True,
        "application_payload_sha256": artifact["sha256"],
        "application_manifest_sha256": hashlib.sha256(raw_manifest).hexdigest(),
        "verified_at": dt.datetime.now(dt.timezone.utc).isoformat(),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="operation", required=True)
    create = sub.add_parser("build")
    create.add_argument("--source-dir", type=Path, required=True)
    create.add_argument("--destination", type=Path, required=True)
    create.add_argument("--pg-image")
    check = sub.add_parser("verify")
    check.add_argument("--directory", type=Path, required=True)
    check.add_argument("--pg-image", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        if args.operation == "build":
            data = build(args.source_dir, args.destination, pg_image=args.pg_image)
            safe = {key: data[key] for key in ("format", "phase", "source_tables", "absent_mail_tables", "artifact")}
        else:
            safe = verify(args.directory, args.pg_image)
        print(json.dumps(safe, sort_keys=True))
    except CapsuleError as error:
        print(str(error), file=__import__("sys").stderr)
        raise SystemExit(1) from None
    except Exception:
        print("capsule_operation_failed", file=__import__("sys").stderr)
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()

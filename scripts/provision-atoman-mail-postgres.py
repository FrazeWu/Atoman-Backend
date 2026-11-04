#!/usr/bin/env python3
"""Provision one isolated mail database on Atoman's existing PostgreSQL instance.

This script never reads application credentials and does not modify existing
Atoman databases. No mailbox records are created by provisioning itself.
"""
import argparse
import os
from pathlib import Path
import secrets
import subprocess
import sys

ROLE = 'atoman_mail'
DATABASE = 'atoman_mail'
DEFAULT_CONTAINER = 'atoman-dev-postgres-1'
DEFAULT_CREDENTIAL_PATH = Path('/etc/atoman-mail/postgres.env')


def sql(container: str, query: str) -> str:
    process = subprocess.run(
        ['docker', 'exec', '-i', container, 'psql', '-X', '-qAt',
         '-v', 'ON_ERROR_STOP=1', '-U', 'atoman', '-d', 'postgres'],
        input=query + '\n', text=True, capture_output=True, timeout=30, check=False,
    )
    if process.returncode:
        raise RuntimeError('PostgreSQL admin query failed; inspect container health privately')
    return process.stdout.strip()


def state(container: str) -> tuple[bool, bool]:
    result = sql(container, "SELECT (SELECT count(*) FROM pg_roles WHERE rolname='atoman_mail'),"
                           " (SELECT count(*) FROM pg_database WHERE datname='atoman_mail');")
    if result not in ('0|0', '0|1', '1|0', '1|1'):
        raise RuntimeError('Unexpected PostgreSQL role/database state')
    role, database = result.split('|')
    return role == '1', database == '1'


def verify_client(container: str, password: str) -> None:
    # Password is supplied to the container through stdin, not through a CLI
    # argument, Git file, process environment of the caller, or output log.
    cmd = [
        'docker', 'exec', '-i', container, 'sh', '-c',
        "IFS= read -r PGPASSWORD; export PGPASSWORD; "
        "exec psql -h 127.0.0.1 -U atoman_mail -d atoman_mail -X -qAt "
        "-v ON_ERROR_STOP=1 -c \"select current_user, "
        "has_schema_privilege(current_user,'public','CREATE')\"",
    ]
    proc = subprocess.run(cmd, input=password+'\n', text=True,
                          capture_output=True, timeout=30, check=False)
    if proc.returncode or proc.stdout.strip() != 'atoman_mail|t':
        raise RuntimeError('Mail role could not authenticate or lacks CREATE on public schema')
    privileges = sql(container,
        "SELECT rolsuper,rolcreatedb,rolcreaterole,rolreplication "
        "FROM pg_roles WHERE rolname='atoman_mail';")
    if privileges != 'f|f|f|f':
        raise RuntimeError('Mail role has excessive PostgreSQL privileges')


def read_credential(path: Path) -> str:
    if path.is_symlink() or not path.is_file():
        raise RuntimeError('Mail password file missing or is a symbolic link')
    stat = path.stat()
    if stat.st_uid != 0 or stat.st_mode & 0o077:
        raise RuntimeError('Mail password file must be root owned and private')
    value = path.read_text().strip()
    prefix = 'STALWART_PG_PASSWORD='
    password = value[len(prefix):] if value.startswith(prefix) else ''
    if len(password) != 64 or any(x not in '0123456789abcdef' for x in password):
        raise RuntimeError('Mail password file has an invalid value')
    return password


def create_secret_file(path: Path, password: str) -> None:
    directory = path.parent
    if directory.is_symlink():
        raise RuntimeError('Refusing symlinked mail secret directory')
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(directory, 0o700)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'w') as f:
        f.write('STALWART_PG_PASSWORD=' + password + '\n')
        f.flush()
        os.fsync(f.fileno())


def run(container: str, path: Path, apply: bool) -> None:
    if os.geteuid() != 0:
        raise RuntimeError('Run as root to protect the mail PostgreSQL credential')
    role_exists, database_exists = state(container)
    if role_exists and database_exists:
        verify_client(container, read_credential(path))
        print('MAIL_POSTGRES_ALREADY_PROVISIONED_AND_VALIDATED')
        return
    if role_exists or database_exists or path.exists() or path.is_symlink():
        raise RuntimeError('Partial mail PostgreSQL provisioning detected; no changes made')
    if not apply:
        print('MAIL_POSTGRES_NOT_PROVISIONED_NO_CHANGES')
        return
    # Ensure this container is actually the Atoman PG cluster before changes.
    existing = sql(container, "SELECT count(*) FROM pg_database WHERE datname='atoman_prod';")
    if container == DEFAULT_CONTAINER and existing != '1':
        raise RuntimeError('Target container is not the expected Atoman PostgreSQL cluster')
    password = secrets.token_hex(32)
    create_secret_file(path, password)
    try:
        sql(container, "CREATE ROLE atoman_mail LOGIN NOSUPERUSER NOCREATEDB "
                       "NOCREATEROLE NOREPLICATION PASSWORD '" + password + "';")
        sql(container, "CREATE DATABASE atoman_mail OWNER atoman_mail;")
        sql(container, "REVOKE ALL ON DATABASE atoman_mail FROM PUBLIC;")
        verify_client(container, password)
    except Exception:
        # Do not automatically drop a database that may have been created: it
        # is safer to stop, preserve the credential, and reconcile manually.
        raise RuntimeError('Provisioning requires reconciliation; current mail role/database not dropped') from None
    print('MAIL_POSTGRES_CREATED_AND_VALIDATED')
    print('MAIL_POSTGRES_CREDENTIAL_SAVED_PRIVATELY')


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--container', default=DEFAULT_CONTAINER)
    parser.add_argument('--credential-file', type=Path, default=DEFAULT_CREDENTIAL_PATH)
    parser.add_argument('--apply', action='store_true', help='Create dedicated mail DB and role')
    args = parser.parse_args()
    if not args.container.startswith('atoman-') or '/' in args.container:
        parser.error('Expected an Atoman PostgreSQL container name')
    try:
        run(args.container, args.credential_file, args.apply)
        return 0
    except (RuntimeError, OSError, subprocess.TimeoutExpired) as exc:
        print('MAIL_POSTGRES_PROVISION_FAILED: ' + str(exc), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())

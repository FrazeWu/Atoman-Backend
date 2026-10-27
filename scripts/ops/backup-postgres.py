#!/usr/bin/env python3
"""Private logical backups with one exported snapshot and isolated restore checks.
Never restores into production. Explicit retention only prunes this tool's completed backups.
"""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess as sp
import tempfile
import time
import uuid


def run(args, **kw):
    return sp.run(args, check=True, **kw)


def sql(container, user, database, query):
    return sp.check_output(['docker', 'exec', container, 'psql', '-X', '-qAt',
                            '-v', 'ON_ERROR_STOP=1', '-U', user, '-d', database,
                            '-c', query], text=True).strip()


def quote_ident(value):
    return '"' + value.replace('"', '""') + '"'


def count_sql(tables):
    return '\n'.join("SELECT json_build_array('%s',count(*)) FROM public.%s;" %
                     (name.replace("'", "''"), quote_ident(name)) for name in tables)


def counts(container, user, database, tables, snapshot=None):
    query = 'BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\n'
    if snapshot:
        query += "SET TRANSACTION SNAPSHOT '%s';\n" % snapshot
    query += count_sql(tables) + '\nCOMMIT;'
    raw = sql(container, user, database, query)
    return dict(json.loads(line) for line in raw.splitlines() if line)


def backup(container, user, database, target):
    session = sp.Popen(['docker', 'exec', '-i', container, 'psql', '-X', '-qAt',
                        '-v', 'ON_ERROR_STOP=1', '-U', user, '-d', database],
                       stdin=sp.PIPE, stdout=sp.PIPE, stderr=sp.PIPE, text=True)
    try:
        session.stdin.write('BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\nSELECT pg_export_snapshot();\n')
        session.stdin.flush()
        snapshot = session.stdout.readline().strip()
        if not snapshot or not all(ch in '0123456789ABCDEFabcdef-' for ch in snapshot):
            raise RuntimeError('Could not export a valid snapshot')
        tables = json.loads(sql(container, user, database,
            "SELECT coalesce(json_agg(tablename ORDER BY tablename),'[]') FROM pg_tables WHERE schemaname='public'"))
        expected = counts(container, user, database, tables, snapshot)
        output = target / (database + '.dump')
        start = time.monotonic()
        with output.open('wb') as out:
            run(['docker', 'exec', container, 'pg_dump', '-U', user, '-d', database,
                 '--format=custom', '--compress=1',
                 '--lock-wait-timeout=15000', '--snapshot=' + snapshot], stdout=out)
        with output.open('rb') as inp:
            digest = hashlib.file_digest(inp, 'sha256').hexdigest()
        return {'database': database, 'dump': output.name, 'bytes': output.stat().st_size,
                'sha256': digest, 'public_table_counts': expected,
                'backup_seconds': round(time.monotonic() - start, 3)}
    finally:
        if session.poll() is None:
            try:
                session.stdin.write('ROLLBACK;\n\\q\n'); session.stdin.flush()
                session.communicate(timeout=10)
            except (BrokenPipeError, sp.TimeoutExpired):
                session.kill(); session.communicate()


def verify(image, target, records):
    name = 'atoman-restore-' + uuid.uuid4().hex[:12]
    cid = sp.check_output(['docker','run','--detach','--rm','--name',name,
        '--label','atoman.ops=restore-verification','--network','none',
        '--cpus','2','--memory','2g','-e','POSTGRES_USER=atoman_restore',
        '-e','POSTGRES_HOST_AUTH_METHOD=trust','-v',str(target)+':/backup:ro',image,'-c','maintenance_work_mem=256MB'], text=True).strip()
    try:
        for _ in range(60):
            if sp.run(['docker','exec',cid,'pg_isready','-U','atoman_restore'],stdout=sp.DEVNULL,stderr=sp.DEVNULL).returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('Isolated restore PostgreSQL did not become ready')
        for record in records:
            database = record['database']
            sql(cid,'atoman_restore','postgres','CREATE DATABASE ' + quote_ident(database))
            start = time.monotonic()
            run(['docker','exec',cid,'pg_restore','-U','atoman_restore','-d',database,
                 '--exit-on-error','--no-owner','--no-acl','/backup/'+record['dump']])
            actual = counts(cid,'atoman_restore',database,record['public_table_counts'])
            if actual != record['public_table_counts']:
                raise RuntimeError('Restore row counts differ for ' + database)
            record['restore_seconds'] = round(time.monotonic()-start,3)
            record['verified_tables'] = len(actual)
            record['restore_verified'] = True
            print(json.dumps({k:record[k] for k in ['database','bytes','backup_seconds','restore_seconds','verified_tables','restore_verified']}), flush=True)
    finally:
        sp.run(['docker','rm','-f',cid],stdout=sp.DEVNULL,stderr=sp.DEVNULL)


def prune_managed_backups(destination, keep):
    if keep <= 0:
        return
    managed = []
    for item in destination.iterdir():
        if item.is_symlink() or not item.is_dir() or not (item/'COMPLETE').is_file():
            continue
        try:
            info=json.loads((item/'manifest.json').read_text())
        except (OSError, ValueError):
            continue
        if info.get('format')=='atoman-postgres-logical-v1' and info.get('complete') is True:
            managed.append(item)
    for item in sorted(managed, reverse=True)[keep:]:
        if item.parent.resolve()!=destination.resolve():
            raise RuntimeError('Unexpected retention path')
        shutil.rmtree(item)


def terminate_on_signal(signum, _frame):
    # Unwind finally blocks so a timed-out restore removes its owned container.
    raise SystemExit(128 + signum)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--container',default='atoman-dev-postgres-1')
    parser.add_argument('--database',action='append',required=True)
    parser.add_argument('--destination',type=Path,required=True)
    parser.add_argument('--verify',action='store_true')
    parser.add_argument('--retain',type=int,default=0,help='Keep this many successful managed backups; zero disables pruning')
    args=parser.parse_args()
    signal.signal(signal.SIGTERM, terminate_on_signal)
    signal.signal(signal.SIGINT, terminate_on_signal)
    if args.retain < 0: parser.error('--retain cannot be negative')
    if os.geteuid()!=0:
        parser.error('Run as root to keep backup files private')
    os.umask(0o077)
    args.destination.mkdir(parents=True,exist_ok=True,mode=0o700)
    metadata=json.loads(sp.check_output(['docker','inspect',args.container],text=True))[0]
    env=dict(entry.split('=',1) for entry in metadata['Config']['Env'] if '=' in entry)
    user=env.get('POSTGRES_USER','postgres')
    for database in args.database:
        if not database or '/' in database or database in ['.', '..']:
            parser.error('Database name must be a safe filename')
    largest=max(int(sql(args.container,user,db,'SELECT pg_database_size(current_database())')) for db in args.database)
    if shutil.disk_usage(args.destination).free < largest*3+(1<<30):
        raise RuntimeError('Not enough disk for backup and isolated recovery')
    target=Path(tempfile.mkdtemp(prefix=dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ-'),dir=args.destination))
    manifest={'format':'atoman-postgres-logical-v1','started_at':dt.datetime.now(dt.timezone.utc).isoformat(),'container':args.container,
              'image_id':metadata['Image'],'backups':[],'complete':False}
    try:
        for database in args.database:
            manifest['backups'].append(backup(args.container,user,database,target))
        with (target/'roles.sql').open('wb') as out:
            run(['docker','exec',args.container,'pg_dumpall','-U',user,'--globals-only'],stdout=out)
        if args.verify:
            verify(metadata['Image'],target,manifest['backups'])
        manifest['complete']=True
        manifest['completed_at']=dt.datetime.now(dt.timezone.utc).isoformat()
        (target/'COMPLETE').touch(mode=0o600)
    finally:
        (target/'manifest.json').write_text(json.dumps(manifest,indent=2))
    if args.retain:
        prune_managed_backups(args.destination, args.retain)
    print('BACKUP_DIRECTORY='+str(target),flush=True)

if __name__=='__main__':
    main()

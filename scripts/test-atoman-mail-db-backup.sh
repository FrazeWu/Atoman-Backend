#!/bin/sh
# Back up a synthetic single-row table in the dedicated (empty) Atoman mail DB,
# restore it into the existing isolated verification container, then remove
# ONLY the probe table and temporary root-only backup artifacts created here.
set -eu
container=atoman-dev-postgres-1
database=atoman_mail
base=/var/backups/atoman
probe=mail_backup_probe_$(openssl rand -hex 8)
if [ ! -d "$base" ]; then echo 'Atoman backup directory is missing' >&2; exit 1; fi
if [ "$(docker exec "$container" psql -U atoman -d "$database" -At -c "select count(*) from pg_tables where schemaname='public';")" != 0 ]; then
  echo 'Mail database is no longer empty; skip disruptive synthetic backup probe' >&2
  exit 2
fi
workdir=$(mktemp -d "$base/mail-backup-check.XXXXXXXX")
chmod 700 "$workdir"
created=no
cleanup() {
  if [ "$created" = yes ]; then
    docker exec "$container" psql -U atoman -d "$database" -q -v ON_ERROR_STOP=1 -c "DROP TABLE IF EXISTS public.$probe" >/dev/null 2>&1 || true
  fi
  case "$workdir" in
    "$base"/mail-backup-check.*) rm -rf -- "$workdir" ;;
    *) echo 'Unsafe mail backup check temp directory, refusing cleanup' >&2 ;;
  esac
}
trap cleanup EXIT HUP INT TERM
created=yes
docker exec "$container" psql -U atoman -d "$database" -q -v ON_ERROR_STOP=1 \
  -c "CREATE TABLE public.$probe(id integer primary key, marker text not null); INSERT INTO public.$probe(id,marker) VALUES (1,'atoman-mail-backup-staging');" >/dev/null
python3 /usr/local/libexec/atoman/backup-postgres.py \
  --container "$container" --database "$database" \
  --destination "$workdir" --verify --retain 0
python3 - "$workdir" "$database" "$probe" <<'PY'
import json,sys
from pathlib import Path
root,db,probe=Path(sys.argv[1]),sys.argv[2],sys.argv[3]
items=[x for x in root.iterdir() if x.is_dir() and (x/'COMPLETE').exists()]
if len(items)!=1:raise SystemExit('Expected one complete isolated mail backup')
manifest=json.loads((items[0]/'manifest.json').read_text())
entries=manifest.get('backups',[])
if not manifest.get('complete') or len(entries)!=1:raise SystemExit('Backup manifest incomplete')
record=entries[0]
if record.get('database')!=db or not record.get('restore_verified'):
    raise SystemExit('Mail database restore verification missing')
if record.get('public_table_counts',{}).get(probe)!=1:
    raise SystemExit('Mail probe row not found in verified backup snapshot')
print('MAIL_PG_BACKUP_AND_ISOLATED_RESTORE_VERIFIED')
print('MAIL_PG_VERIFIED_TABLES',record.get('verified_tables'))
PY

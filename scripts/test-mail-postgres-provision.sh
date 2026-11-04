#!/bin/sh
set -eu
name=atoman-mail-provision-fixture-pg
if docker container inspect "$name" >/dev/null 2>&1; then
  echo 'Fixture name is already in use' >&2
  exit 2
fi
workdir=$(mktemp -d /tmp/atoman-mail-provision-fixture.XXXXXXXX)
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  rm -rf "$workdir"
}
trap cleanup EXIT HUP INT TERM
admin_password=$(openssl rand -hex 32)
docker run -d --name "$name" --network none \
  --tmpfs /var/lib/postgresql/data:rw,nosuid,nodev,size=384m \
  -e POSTGRES_USER=atoman -e POSTGRES_DB=postgres \
  -e POSTGRES_PASSWORD="$admin_password" postgres:16-alpine >/dev/null
n=0
until docker exec "$name" pg_isready -q -U atoman -d postgres; do
  n=$((n+1))
  if [ "$n" -ge 40 ]; then echo 'fixture pg readiness failed' >&2; exit 1; fi
  sleep 1
done
secret="$workdir/postgres.env"
python3 scripts/provision-atoman-mail-postgres.py --container "$name" --credential-file "$secret"
python3 scripts/provision-atoman-mail-postgres.py --container "$name" --credential-file "$secret" --apply
python3 scripts/provision-atoman-mail-postgres.py --container "$name" --credential-file "$secret" --apply
mode=$(stat -c '%a' "$secret")
if [ "$mode" != 600 ]; then echo 'fixture credential file permissions incorrect' >&2; exit 1; fi
role=$(docker exec "$name" psql -U atoman -d postgres -At -c "SELECT rolname FROM pg_roles WHERE rolname='atoman_mail'")
owner=$(docker exec "$name" psql -U atoman -d postgres -At -c "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname='atoman_mail'")
if [ "$role" != 'atoman_mail' ] || [ "$owner" != 'atoman_mail' ]; then
  echo 'mail role or database owner mismatch' >&2
  exit 1
fi
printf '%s\n' 'MAIL_PG_PROVISION_FIXTURE_OK'

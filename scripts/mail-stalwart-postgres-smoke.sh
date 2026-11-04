#!/bin/sh
# Disposable PostgreSQL data-store smoke for Stalwart v1.
# Uses an internal-only Docker network and fresh temporary credentials.
# No existing PostgreSQL, R2, external SMTP ports, or Atoman state are touched.
set -eu
net=atoman-mail-pg-smoke-net
pg=atoman-mail-pg-smoke-db
mail=atoman-mail-pg-smoke-server
cleanup() {
  docker rm -f "$mail" "$pg" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$workdir"
}
for name in "$mail" "$pg"; do
  if docker container inspect "$name" >/dev/null 2>&1; then
    echo 'Refusing to operate on pre-existing staging container' >&2
    exit 2
  fi
done
if docker network inspect "$net" >/dev/null 2>&1; then
  echo 'Refusing to operate on pre-existing staging network' >&2
  exit 2
fi
workdir=$(mktemp -d /tmp/atoman-mail-pg-smoke.XXXXXXXX)
chmod 700 "$workdir"
trap cleanup EXIT HUP INT TERM
password=$(openssl rand -hex 24)
docker network create --internal "$net" >/dev/null
docker run -d --name "$pg" --network "$net" \
  --tmpfs /var/lib/postgresql/data:rw,nosuid,nodev,size=384m \
  -e POSTGRES_USER=mail_stage -e POSTGRES_DB=mail_stage \
  -e POSTGRES_PASSWORD="$password" \
  postgres:16-alpine >/dev/null
count=0
until docker exec "$pg" pg_isready -q -U mail_stage -d mail_stage; do
  count=$((count+1))
  if [ "$count" -ge 40 ]; then echo 'PostgreSQL staging readiness failed' >&2; exit 1; fi
  sleep 1
done
cat > "$workdir/config.json" <<JSON
{"@type":"PostgreSql","host":"$pg","port":5432,"database":"mail_stage","authUsername":"mail_stage","authSecret":{"@type":"EnvironmentVariable","variableName":"MAIL_STAGE_PG_PASSWORD"},"poolMaxConnections":4}
JSON
chmod 644 "$workdir/config.json"
start_mail() {
  docker run -d --name "$mail" --init --network "$net" \
    --mount "type=bind,src=$workdir/config.json,dst=/etc/stalwart/config.json,readonly" \
    --tmpfs /var/lib/stalwart:rw,nosuid,nodev,size=64m \
    -e MAIL_STAGE_PG_PASSWORD="$password" \
    stalwartlabs/stalwart:v1 >/dev/null
}
wait_schema() {
  count=0
  while [ "$count" -lt 40 ]; do
    tables=$(docker exec "$pg" psql -U mail_stage -d mail_stage -At -c "select count(*) from information_schema.tables where table_schema='public';" 2>/dev/null || true)
    if [ "$tables" -ge 25 ] 2>/dev/null; then
      echo "STALWART_POSTGRES_TABLES=$tables"
      return 0
    fi
    if [ "$(docker inspect "$mail" --format '{{.State.Running}}' 2>/dev/null)" = false ]; then
      echo 'Stalwart exited before schema was ready' >&2
      docker logs "$mail" 2>&1 | tail -n 15 | sed -E 's/(password|secret|token)[^ ]*/[redacted]/Ig' >&2
      return 1
    fi
    count=$((count+1))
    sleep 1
  done
  echo 'Stalwart failed to create PostgreSQL tables within timeout' >&2
  return 1
}
start_mail
wait_schema
before=$(docker exec "$pg" psql -U mail_stage -d mail_stage -At -c "select count(*) from information_schema.tables where table_schema='public';")
docker rm -f "$mail" >/dev/null
start_mail
wait_schema
after=$(docker exec "$pg" psql -U mail_stage -d mail_stage -At -c "select count(*) from information_schema.tables where table_schema='public';")
if [ "$before" -ne "$after" ] || [ "$before" -lt 25 ]; then
  echo 'PostgreSQL persisted schema check failed' >&2
  exit 1
fi
if [ "$(docker inspect "$mail" --format '{{.State.Running}}')" != true ]; then
  echo 'Stalwart did not survive restart with PostgreSQL store' >&2
  exit 1
fi
printf '%s\n' 'STALWART_POSTGRES_SCHEMA_SURVIVES_RESTART'
printf '%s\n' 'STAGE_PRIVATE_NETWORK_ONLY'

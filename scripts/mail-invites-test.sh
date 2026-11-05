#!/bin/sh
# Runs mail invite and migration tests against disposable PostgreSQL only.
set -eu
name=atoman-mail-invites-test-pg
port=55452
if docker container inspect "$name" >/dev/null 2>&1; then
  echo 'Existing mail invitation test container; refusing to replace it' >&2
  exit 2
fi
if ss -ltn | grep -q ":$port "; then
  echo 'Mail test port already in use' >&2
  exit 2
fi
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT HUP INT TERM
password=$(openssl rand -hex 18)
docker run -d --init --name "$name" -p "127.0.0.1:$port:5432" \
  --tmpfs /var/lib/postgresql/data:rw,nosuid,nodev,size=384m \
  -e POSTGRES_USER=atoman_test -e POSTGRES_DB=atoman_mail_test \
  -e POSTGRES_PASSWORD="$password" postgres:16-alpine >/dev/null
count=0
until docker exec "$name" pg_isready -q -U atoman_test -d atoman_mail_test; do
  count=$((count+1))
  if [ "$count" -ge 45 ]; then echo 'Mail test database did not become ready' >&2; exit 1; fi
  sleep 1
done
export TEST_POSTGRES_DSN="postgres://atoman_test:$password@127.0.0.1:$port/atoman_mail_test?sslmode=disable"
GOMAXPROCS=2 go test ./internal/modules/mail -count=1 -v
GOMAXPROCS=2 go test ./internal/migrations -run '^TestRunMail(Accounts|Outbox)Migration$' -count=1
GOMAXPROCS=2 go test ./internal/app -run '^TestRegisterV1RoutesMountsAtomanMailUnderAuthentication$' -count=1
# Exercise the operator-only mail migration path against this disposable DB.
for attempt in 1 2; do
 DATABASE_TYPE=postgres DATABASE_URL="$TEST_POSTGRES_DSN" GOMAXPROCS=2 go run ./cmd/mail_admin --operation migrate-mail
done
nonmail_tables=$(docker exec "$name" psql -U atoman_test -d atoman_mail_test -Atc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name NOT LIKE 'mail_%'")
if [ "$nonmail_tables" != "0" ]; then echo 'Mail-only migration created unrelated tables' >&2; exit 1; fi
printf '%s\n' 'MAIL_INVITATION_ISOLATED_POSTGRES_TESTS_PASSED'

#!/usr/bin/env bash
# Disposable PostgreSQL and four distinct OS processes; no production access.
set -Eeuo pipefail
umask 077

if [[ "${PAPERS_P0_QUEUE_CONFIRM:-}" != 1 ]]; then
  echo 'Explicitly set PAPERS_P0_QUEUE_CONFIRM=1 for isolated P0 test.' >&2
  exit 2
fi
for exe in docker go mktemp; do
  command -v "$exe" >/dev/null || exit 2
done
readonly root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly temp="$(mktemp -d /var/tmp/atoman-papers-queue-test.XXXXXXXX)"
container_id=''
cleanup() {
  local rc=$?
  trap - EXIT INT TERM
  set +e
  local failed=0
  if [[ -n "$container_id" ]]; then
    docker rm -f "$container_id" >/dev/null 2>&1 || failed=1
  fi
  case "$temp" in
    /var/tmp/atoman-papers-queue-test.*) rm -rf -- "$temp" || failed=1 ;;
    *) echo 'unsafe test directory' >&2; failed=1 ;;
  esac
  if (( failed != 0 ));then echo 'P0 cleanup incomplete' >&2;exit 1;fi
  echo 'P0 disposable database and binary cleaned.'
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

container_id="$(docker run --detach --rm \
  --name "papers-p0-queue-$$" \
  --label atoman.test=isolated --label atoman.papers.queue=p0 \
  --tmpfs /var/lib/postgresql/data --shm-size=128m \
  -e POSTGRES_USER=atoman_test -e POSTGRES_PASSWORD=atoman_test \
  -e POSTGRES_DB=atoman_test -p 127.0.0.1::5432 postgres:16-alpine)"
ready=false
for ((i=0;i<45;i++));do
  if docker exec "$container_id" pg_isready -U atoman_test -d atoman_test >/dev/null 2>&1;then
    ready=true
    break
  fi
  sleep 1
done
[[ "$ready" == true ]] || { echo 'P0 isolated database did not start' >&2;exit 1; }
binding="$(docker port "$container_id" 5432/tcp)"
if [[ ! "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]];then
  echo 'P0 database was not bound to loopback; refusing' >&2
  exit 1
fi
port="${BASH_REMATCH[1]}"
export PAPERS_P0_TEST_DSN="postgres://atoman_test:atoman_test@127.0.0.1:$port/atoman_test?sslmode=disable"
echo 'Isolated P0 PostgreSQL available on ephemeral loopback port.'
cd "$root"
go build -o "$temp/queue-p0" ./cmd/papers_queue_p0
"$temp/queue-p0" seed
old_id="$("$temp/queue-p0" claim-crash)"
[[ "$old_id" =~ ^pc-[a-z0-9-]+$ ]] || { echo 'missing first sandbox identity' >&2;exit 1; }
echo 'Worker process 1 exited without acknowledgement.'
# The SQL operation below touches ONLY the newly created, isolated container.
docker exec "$container_id" psql -U atoman_test -d atoman_test -v ON_ERROR_STOP=1 -c \
  "UPDATE paper_compile_slots SET lease_expires_at=CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id=1" >/dev/null
"$temp/queue-p0" recover
new_id="$("$temp/queue-p0" complete)"
[[ "$new_id" =~ ^pc-[a-z0-9-]+$ && "$new_id" != "$old_id" ]] || {
 echo 'new attempt incorrectly reused old sandbox ID' >&2;exit 1;
}
"$temp/queue-p0" verify
printf 'P0_DISTINCT_WORKER_PROCESSES_PASS\n'

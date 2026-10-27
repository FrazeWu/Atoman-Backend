#!/usr/bin/env bash
# Use an explicitly supplied test database, or own one disposable container per run.
# Never stop another worktree's database or read production DATABASE_URL.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"
container_id=""
cleanup() {
  if [[ -n "$container_id" ]]; then
    docker rm -f "$container_id" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

parallelism="${GO_TEST_PARALLELISM:-1}"
if [[ ! "$parallelism" =~ ^[1-9][0-9]*$ ]]; then
  echo 'GO_TEST_PARALLELISM must be a positive integer' >&2
  exit 2
fi

dsn="${TEST_POSTGRES_DSN:-${TEST_DATABASE_URL:-}}"
if [[ -z "$dsn" ]]; then
  container_name="atoman-test-$(id -u)-$$-${RANDOM}"
  container_id="$(docker run --detach --rm \
    --name "$container_name" --label atoman.test=isolated \
    --tmpfs /var/lib/postgresql/data --shm-size=256m \
    -e POSTGRES_USER=atoman_test -e POSTGRES_PASSWORD=atoman_test \
    -e POSTGRES_DB=atoman_test -p 127.0.0.1::5432 \
    "${TEST_POSTGRES_IMAGE:-postgres:16-alpine}")"
  ready=false
  for ((attempt=0; attempt<60; attempt++)); do
    if docker exec "$container_id" pg_isready -U atoman_test -d atoman_test >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 1
  done
  if [[ "$ready" != true ]]; then
    echo 'Isolated test PostgreSQL did not become ready' >&2
    exit 1
  fi
  binding="$(docker port "$container_id" 5432/tcp)"
  if [[ ! "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]]; then
    echo 'Unexpected test PostgreSQL port binding; refusing to run tests' >&2
    exit 1
  fi
  port="${BASH_REMATCH[1]}"
  dsn="postgres://atoman_test:atoman_test@127.0.0.1:${port}/atoman_test?sslmode=disable"
  echo "Isolated PostgreSQL ready on loopback port $port"
else
  echo 'Using explicit PostgreSQL test configuration (not owned by this run)'
fi

export TEST_POSTGRES_DSN="$dsn" TEST_DATABASE_URL="$dsn"
export REFERENCE_TEST_POSTGRES_DSN="$dsn" FORUM_TEST_POSTGRES_DSN="$dsn" MUSIC_POSTGRES_TEST_DSN="$dsn"
if [[ $# -eq 0 ]]; then
  set -- ./...
fi
go test -p "$parallelism" "$@"

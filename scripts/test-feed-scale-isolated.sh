#!/usr/bin/env bash
# Disposable production-SCALE, SYNTHETIC-only PostgreSQL benchmark.
# Never reuses TEST_POSTGRES_DSN or production credentials. Host Go and isolated
# PostgreSQL each run under memory/CPU caps; schema cleanup is automatic.
set -euo pipefail
if [[ "$#" -ne 1 || ! "$1" =~ ^(10000|100000|360000)$ ]]; then
  echo 'Usage: scripts/test-feed-scale-isolated.sh 10000|100000|360000' >&2
  exit 2
fi
if [[ -n "${TEST_POSTGRES_DSN:-}" || -n "${TEST_DATABASE_URL:-}" ]]; then
  echo 'Refusing scale benchmark with any externally supplied test database DSN' >&2
  exit 2
fi
if [[ "$(id -u)" -ne 0 ]]; then
  echo 'The isolated runner requires root for a hard-capped transient systemd scope' >&2
  exit 2
fi
if [[ "$(findmnt -no FSTYPE /sys/fs/cgroup)" != 'cgroup2' ]]; then
  echo 'Cannot verify cgroup v2 process containment' >&2
  exit 2
fi
cd "$(dirname "$0")/.."
container_name="atoman-feed-scale-$(id -u)-$$-$RANDOM"
container_id=''
cleanup() {
  if [[ -n "$container_id" ]]; then
    docker rm -f "$container_id" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# 1.5 GiB DB container, no swap; transient Go scope capped at 2.2 GiB/no swap.
container_id="$(docker run -d --rm \
    --name "$container_name" --label atoman.benchmark=feed-isolated \
    --cpus 1.5 --memory 1536m --memory-swap 1536m --pids-limit 256 --shm-size 128m \
    -e POSTGRES_DB=atoman_scale_test -e POSTGRES_USER=scale_test -e POSTGRES_PASSWORD=scale_test \
    -p 127.0.0.1::5432 postgres:16-alpine \
    -c shared_buffers=128MB -c work_mem=8MB -c max_connections=40)"
ready=false
for attempt in {1..60}; do
  if docker exec "$container_id" pg_isready -U scale_test -d atoman_scale_test >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
if [[ "$ready" != true ]]; then echo 'Isolated database did not become ready' >&2;exit 1;fi
binding="$(docker port "$container_id" 5432/tcp)"
if [[ ! "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]]; then echo 'Unexpected port binding' >&2;exit 2;fi
port="${BASH_REMATCH[1]}"
export TEST_POSTGRES_DSN="postgres://scale_test:scale_test@127.0.0.1:${port}/atoman_scale_test?sslmode=disable"
export TEST_DATABASE_URL="$TEST_POSTGRES_DSN"
export REFERENCE_TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN"
export FORUM_TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN"
export MUSIC_POSTGRES_TEST_DSN="$TEST_POSTGRES_DSN"
profile="${FEED_SCALE_PROFILE:-baseline}"
if [[ "$profile" != 'baseline' && "$profile" != 'prodshape' && "$profile" != 'adversarial' ]]; then echo 'Invalid test profile' >&2; exit 2; fi
if [[ "$profile" == 'adversarial' && "$1" != 10000 ]]; then echo 'Adversarial title stress is limited to 10000 articles' >&2;exit 2;fi
export FEED_SCALE_PROFILE="$profile" FEED_SCALE_COUNT="$1" FEED_SCALE_ISOLATED=yes GOMAXPROCS=2 GOGC=75 GOMEMLIMIT=1250MiB
printf 'ISOLATED_SCALE count=%s postgres_container=%s port=%s\n' "$1" "$container_name" "$port"
# Optional CPU profiling is written only to /tmp, not the worktree.
profile_flags=()
if [[ "${FEED_SCALE_CPU_PROFILE:-}" == 'yes' ]]; then
  profile_flags=(-cpuprofile="/tmp/atoman-feed-scale-$(date -u +%Y%m%d)-${1}-$$.cpu" -outputdir=/tmp)
fi
# systemd-run is blocking in scope mode; timeout also bounds the wall-clock run.
systemd-run --quiet --scope -p MemoryMax=2200M -p MemorySwapMax=0 -p CPUQuota=150% \
  /usr/bin/timeout --signal=TERM --kill-after=15s 14m \
  /usr/bin/env TEST_POSTGRES_DSN="$TEST_POSTGRES_DSN" TEST_DATABASE_URL="$TEST_DATABASE_URL" \
  FEED_SCALE_PROFILE="$FEED_SCALE_PROFILE" FEED_SCALE_COUNT="$FEED_SCALE_COUNT" FEED_SCALE_ISOLATED=yes GOMAXPROCS=2 GOGC=75 GOMEMLIMIT=1250MiB \
  FEED_SNAPSHOT_STAGE_TRACE="${FEED_SNAPSHOT_STAGE_TRACE:-}" \
  go test -buildvcs=false -p 1 -count=1 -timeout 13m -v "${profile_flags[@]}" -run '^TestDedupIsolatedProductionScale$' ./internal/modules/feed
printf 'POSTGRES_MEM %s\n' "$(docker stats --no-stream --format '{{.MemUsage}}' "$container_id")"
printf 'TEST_PASSED scale=%s\n' "$1"

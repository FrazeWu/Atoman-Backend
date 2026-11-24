#!/usr/bin/env bash
# P0-only: kill a real Go worker inside its disposable test flow, recover the
# exact gVisor sandbox via PostgreSQL fencing, then compile the same snapshot.
set -Eeuo pipefail
umask 077

if [[ "${PAPERS_P0_DURABLE_CONFIRM:-}" != 1 || "$(id -u)" != 0 ]]; then
  echo 'Explicit P0 root confirmation required.' >&2
  exit 2
fi
for tool in docker curl sha512sum tar go systemctl mountpoint findmnt umount timeout od;do
  command -v "$tool" >/dev/null || { echo "Missing $tool" >&2;exit 2; }
done
readonly image="texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4"
readonly version="20261019.0"
readonly digest="bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915"
docker image inspect "$image" >/dev/null || { echo "Image must be preloaded by exact digest";exit 2; }
available="$(df -Pk /var/tmp | awk 'NR==2 {print $4}')"
if (( available < 15*1024*1024 ));then
  echo 'P0 requires 15 GiB free disk' >&2;exit 2
fi
readonly root="$(mktemp -d /var/tmp/atoman-papers-p0-durable.XXXXXXXX)"
readonly suffix="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
readonly container_name="papers-p0-durable-$suffix"
container_id=''
image_container=''
worker_pid=''
verified=false
cleanup(){
  local rc=$?
  trap - EXIT INT TERM
  set +e
  local failed=0
  if [[ -n "$worker_pid" ]] && kill -0 "$worker_pid" 2>/dev/null;then
    kill -KILL "$worker_pid" 2>/dev/null || true
    wait "$worker_pid" 2>/dev/null || true
  fi
  for dir in "$root"/runs/pc-*;do
    [[ -d "$dir" ]] || continue
    local id
    id="$(basename "$dir")"
    if [[ ! "$id" =~ ^pc-[a-f0-9-]+$ ]];then failed=1;continue;fi
    local state="$dir/state"
    if [[ -x "$root/bin/runsc" && -d "$state" ]];then
      local listed
      listed="$("$root/bin/runsc" --systemd-cgroup --root="$state" list 2>/dev/null)"
      if printf '%s\n' "$listed" | grep -qE "^$id[[:space:]]";then
        "$root/bin/runsc" --systemd-cgroup --root="$state" kill "$id" KILL >/dev/null 2>&1 || true
        "$root/bin/runsc" --systemd-cgroup --root="$state" delete --force "$id" >/dev/null 2>&1 || failed=1
      fi
      if "$root/bin/runsc" --systemd-cgroup --root="$state" list 2>/dev/null | grep -qE "^$id[[:space:]]";then failed=1;fi
      local ns="$state/null-netns"
      if mountpoint -q "$ns" 2>/dev/null;then
        if [[ "$(findmnt -n -o FSTYPE --target "$ns")" == nsfs ]];then
          umount "$ns" >/dev/null 2>&1 || failed=1
        else
          echo "Unexpected mount: $ns" >&2
          failed=1
        fi
      fi
    fi
    for unit in "atomanp0-$id.scope" "atoman-papers-p0-$id.scope";do
      if [[ "$(systemctl is-active "$unit" 2>/dev/null)" == active ]];then
        echo "P0 scope still active: $unit" >&2
        failed=1
      fi
    done
  done
  if [[ -n "$image_container" ]];then docker rm -f "$image_container" >/dev/null 2>&1 || failed=1;fi
  if [[ -n "$container_id" ]];then docker rm -f "$container_id" >/dev/null 2>&1 || failed=1;fi
  if (( rc!=0 ));then
    echo '== P0 failure diagnostics ==' >&2
    for log in "$root"/worker*.log "$root"/runs/pc-*/runtime-*.log;do
      [[ -f "$log" ]] || continue
      echo "--- $log" >&2
      tail -n 45 "$log" >&2
    done
  fi
  if (( failed==0 ));then
    case "$root" in
      /var/tmp/atoman-papers-p0-durable.*) rm -rf -- "$root" || failed=1 ;;
      *) failed=1;;
    esac
  fi
  if (( failed!=0 ));then
    echo "P0 cleanup not verified; preserve exact owned root: $root" >&2
    exit 1
  fi
  echo 'P0 disposable scopes, containers and workspace verified cleaned.'
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -m 700 "$root/bin" "$root/assets" "$root/runs"
# UID/GID 65534 gets pathname traversal only, not directory listing.
# Registry and offline archives remain root-private.
chgrp 65534 "$root" "$root/runs"
chmod 0710 "$root" "$root/runs"
readonly base_url="https://storage.googleapis.com/gvisor/releases/release/$version/x86_64"
curl -fsSL --retry 2 --connect-timeout 15 --max-time 180 "$base_url/gvisor.tar.zstd" -o "$root/gvisor.tar.zstd"
curl -fsSL --retry 2 --connect-timeout 15 --max-time 60 "$base_url/gvisor.tar.zstd.sha512" -o "$root/gvisor.tar.zstd.sha512"
printf '%s  %s\n' "$digest" "$root/gvisor.tar.zstd" | sha512sum -c -
(cd "$root" && sha512sum -c gvisor.tar.zstd.sha512)
tar --zstd -xf "$root/gvisor.tar.zstd" -C "$root/bin"
"$root/bin/runsc" --version | head -n 2

# Only this already pinned Docker image may create the offline environment.
image_container="$(docker create --label atoman.papers.p0=durable --label atoman.papers.p0.id="$suffix" "$image" /bin/sh)"
docker export "$image_container" > "$root/rootfs.tar"
docker rm "$image_container" >/dev/null
image_container=''

container_id="$(docker run --detach --rm --name "$container_name" \
  --label atoman.test=isolated --label atoman.papers.p0=durable \
  --tmpfs /var/lib/postgresql/data --shm-size=128m \
  -e POSTGRES_USER=atoman_test -e POSTGRES_PASSWORD=atoman_test \
  -e POSTGRES_DB=atoman_test -p 127.0.0.1::5432 postgres:16-alpine)"
ready=false
for ((attempt=0;attempt<50;attempt++));do
  if docker exec "$container_id" pg_isready -U atoman_test -d atoman_test >/dev/null 2>&1;then
    ready=true;break
  fi
  sleep 1
done
[[ "$ready" == true ]] || { echo 'P0 PostgreSQL did not start' >&2;exit 1; }
binding="$(docker port "$container_id" 5432/tcp)"
if [[ ! "$binding" =~ ^127\.0\.0\.1:([0-9]+)$ ]];then
  echo 'Unsafe database port binding';exit 1
fi
export PAPERS_P0_TEST_DSN="postgres://atoman_test:atoman_test@127.0.0.1:${BASH_REMATCH[1]}/atoman_test?sslmode=disable"
export PAPERS_P0_DURABLE_CONFIRM=1
cd "$(dirname "$0")/../.."
go build -o "$root/bin/p0-durable" ./cmd/papers_p0_durable_worker
readonly helper="$(pwd)/scripts/papers/p0-cgroup-scope-exec.sh"
run_step(){
  "$root/bin/p0-durable" -root "$root" -runsc "$root/bin/runsc" -helper "$helper" -worker "$1" "$2"
}
run_step seed-worker seed
if [[ "${PAPERS_P0_DURABLE_NO_KILL:-}" == 1 ]];then
  run_step baseline-worker run > "$root/worker1.log" 2>&1 || {
    echo 'Baseline real TeX run failed' >&2;exit 1;
  }
  run_step observer status
  echo '== BASELINE LAUNCHER RESULT =='
  cat "$root/worker1.log"
  for log in "$root"/runs/pc-*/runtime-*.log;do
    [[ -f "$log" ]] || continue
    echo "--- trusted runtime log: $log"
    tail -n 24 "$log"
  done
  stored_status="$(docker exec "$container_id" psql -U atoman_test -d atoman_test -At -c 'SELECT status FROM paper_compile_jobs LIMIT 1')"
  [[ "$stored_status" == succeeded ]] || {
    echo "P0 baseline failed with database status $stored_status" >&2
    exit 1
  }
  echo 'P0_REAL_TEX_BASELINE_SUCCEEDED'
  exit 0
fi
# Launch the Go worker directly. Backgrounding the run_step shell function
# would make $! point at a zsh subshell, allowing the real worker child to
# continue after SIGKILL and producing false recovery evidence.
"$root/bin/p0-durable" -root "$root" -runsc "$root/bin/runsc" \
  -helper "$helper" -worker first-worker run > "$root/worker1.log" 2>&1 &
worker_pid=$!
echo 'P0 first real Worker started; monitoring its dedicated gVisor Sentry scope.'
first_id=''
seen=false
for ((attempt=0;attempt<850;attempt++));do
  first_id="$(docker exec "$container_id" psql -U atoman_test -d atoman_test -At -c 'SELECT sandbox_id FROM paper_compile_jobs LIMIT 1' 2>/dev/null || true)"
  if [[ "$first_id" =~ ^pc-[a-f0-9-]+$ ]];then
    sentry="atomanp0-$first_id.scope"
    if [[ "$(systemctl is-active "$sentry" 2>/dev/null)" == active ]];then
      seen=true;break
    fi
  fi
  if ! kill -0 "$worker_pid" 2>/dev/null;then
    echo 'First worker exited before a live Sentry scope was observed' >&2
    exit 1
  fi
  sleep 0.15
done
[[ "$seen" == true ]] || { echo 'No real Sentry scope observed before deadline' >&2;exit 1; }
echo "P0_REAL_SENTRY_ACTIVE_BEFORE_KILL=$first_id"
before_kill_status="$(docker exec "$container_id" psql -U atoman_test -d atoman_test -At -c 'SELECT status FROM paper_compile_jobs LIMIT 1')"
[[ "$before_kill_status" == running ]] || {
  echo "Task completed too quickly before SIGKILL: $before_kill_status" >&2; exit 1;
}
kill -KILL "$worker_pid"
set +e
wait "$worker_pid" >/dev/null 2>&1
killed_rc=$?
set -e
worker_pid=''
[[ "$killed_rc" -eq 137 ]] || { echo "Unexpected killed worker exit $killed_rc";exit 1; }
echo 'P0_FIRST_WORKER_SIGKILL_CONFIRMED'
if grep -q 'P0_DURABLE_STEP_COMPLETE' "$root/worker1.log";then
  echo 'Worker had already returned before SIGKILL; refusing false crash evidence' >&2
  exit 1
fi
run_step observer status
lease_expired=false
for ((attempt=0;attempt<70;attempt++));do
  expired="$(docker exec "$container_id" psql -U atoman_test -d atoman_test -At -c 'SELECT (lease_expires_at < CURRENT_TIMESTAMP)::text FROM paper_compile_slots WHERE id=1' 2>/dev/null)"
  if [[ "$expired" == true ]];then lease_expired=true;break;fi
  sleep 0.4
done
[[ "$lease_expired" == true ]] || { echo 'Old lease did not expire after SIGKILL' >&2;exit 1; }
echo 'P0_OLD_LEASE_EXPIRED_BEFORE_REAPER'
run_step observer status
timeout 80s "$root/bin/p0-durable" -root "$root" -runsc "$root/bin/runsc" -helper "$helper" -worker reaper run > "$root/worker2.log" 2>&1 || {
  echo 'Real orphan sandbox cleanup failed; global queue must remain blocked' >&2
  exit 1
}
echo 'P0_REAL_ORPHAN_SANDBOX_REAPED'
run_step observer status
timeout 230s "$root/bin/p0-durable" -root "$root" -runsc "$root/bin/runsc" -helper "$helper" -worker next-worker run > "$root/worker3.log" 2>&1 || {
  echo 'Next real LaTeX compile failed' >&2;exit 1
}
echo 'P0_RETRY_REAL_PDFLATEX_FINISHED'
run_step observer verify
echo 'P0_DURABLE_QUEUE_REAL_SIGKILL_RECOVERY_PASS'

#!/usr/bin/env bash
# Bounded P0 scope helper, NOT a production Sandbox Launcher.
set -uo pipefail
if [[ $# -ne 7 || "$1" != PAPERS_P0_SCOPE_EXEC ]]; then
  echo "Refusing direct invocation: invalid P0 scope arguments" >&2
  exit 2
fi
shift
runtime="$1"; root="$2"; bundle="$3"; container_id="$4"; log_file="$5"; timeout_seconds="$6"
cgroup_path="$(awk -F: '$1 == "0" {print $3}' /proc/self/cgroup)"
cgroup_root="/sys/fs/cgroup$cgroup_path"
if [[ "$cgroup_path" != /system.slice/atoman-papers-p0-* || ! -f "$cgroup_root/memory.max" ]]; then
  echo "Not running in a dedicated Papers P0 systemd scope: $cgroup_path" >&2
  exit 3
fi
printf 'WRAPPER_CGROUP_PATH=%s\n' "$cgroup_path"
for f in memory.max memory.swap.max pids.max cpu.max; do
  printf 'WRAPPER_%s=%s\n' "$f" "$(cat "$cgroup_root/$f")"
done

# Real runsc Sentry scope should have its own cgroup, separate from wrapper.
sentry_unit="atomanp0-${container_id}.scope"
sentry_cg="/sys/fs/cgroup/system.slice/$sentry_unit"
rc=0
timeout --signal=TERM --kill-after=6s "$timeout_seconds" \
  "$runtime" --systemd-cgroup --root="$root/state" --platform=systrap \
  --network=none --overlay2=none \
  run --bundle "$bundle" "$container_id" 2>&1 | (
    # Drain all output but retain at most 1 MiB of guest-controlled diagnostics.
    # Preserve pipeline/timeout exit status; never terminate TeX via SIGPIPE.
    head -c 1048576 > "$log_file"
    log_rc=$?
    cat >/dev/null
    exit "$log_rc"
  ) &
wait_pid=$!

found=0
cgroup_mismatch=0
expected_memory="$(jq -r '.linux.resources.memory.limit' "$bundle/config.json")"
expected_pids="$(jq -r '.linux.resources.pids.limit' "$bundle/config.json")"
expected_cpu_quota="$(jq -r '.linux.resources.cpu.quota' "$bundle/config.json")"
expected_cpu_period="$(jq -r '.linux.resources.cpu.period' "$bundle/config.json")"
for attempt in $(seq 1 20); do
  if [[ -f "$sentry_cg/memory.max" ]]; then
    found=1
    printf 'SENTRY_CGROUP=%s\n' "$sentry_cg"
    for f in memory.max memory.swap.max pids.max cpu.max; do
      printf 'SENTRY_%s=%s\n' "$f" "$(cat "$sentry_cg/$f")"
    done
    if [[ "$(cat "$sentry_cg/memory.max")" != "$expected_memory" ||
          "$(cat "$sentry_cg/memory.swap.max")" != 0 ||
          "$(cat "$sentry_cg/pids.max")" != "$expected_pids" ||
          "$(cat "$sentry_cg/cpu.max")" != "$expected_cpu_quota $expected_cpu_period" ]]; then
      echo 'SENTRY_CGROUP_MISMATCH: refusing to accept limits' >&2
      cgroup_mismatch=1
    fi
    printf 'SENTRY_MEMORY_EVENTS_EARLY=%s\n' "$(tr '\n' ',' < "$sentry_cg/memory.events")"
    printf 'SENTRY_PIDS_EVENTS_EARLY=%s\n' "$(tr '\n' ',' < "$sentry_cg/pids.events")"
    printf 'SENTRY_CPU_STATS_EARLY=%s\n' "$(tr '\n' ',' < "$sentry_cg/cpu.stat")"
    break
  fi
  if ! kill -0 "$wait_pid" 2>/dev/null; then break; fi
  sleep 0.1
done
if [[ "$found" -eq 0 ]]; then
  echo 'SENTRY_CGROUP_NOT_OBSERVED (sandbox may have exited early)' >&2
else
  last_pids=''
  for sample in $(seq 1 22); do
    if [[ ! -f "$sentry_cg/pids.events" ]]; then break; fi
    pids_sample="$(tr '\n' ',' < "$sentry_cg/pids.events" 2>/dev/null || true)"
    if [[ -n "$pids_sample" && "$pids_sample" != "$last_pids" ]]; then
      printf 'SENTRY_PIDS_EVENTS_SAMPLED=%s\n' "$pids_sample"
      last_pids="$pids_sample"
    fi
    if [[ "$sample" -eq 8 && -f "$sentry_cg/memory.events" ]]; then
      printf 'SENTRY_MEMORY_EVENTS_MID=%s\n' "$(tr '\n' ',' < "$sentry_cg/memory.events" 2>/dev/null || true)"
      printf 'SENTRY_CPU_STATS_MID=%s\n' "$(tr '\n' ',' < "$sentry_cg/cpu.stat" 2>/dev/null || true)"
    fi
    if ! kill -0 "$wait_pid" 2>/dev/null; then break; fi
    sleep 0.05
  done
fi
wait "$wait_pid" || rc=$?
if [[ -r "$sentry_cg/memory.events" ]]; then
  memory_end="$(cat "$sentry_cg/memory.events" 2>/dev/null || true)"
  if [[ -n "$memory_end" ]]; then
    printf 'SENTRY_MEMORY_EVENTS_AFTER=%s\n' "$(printf '%s' "$memory_end" | tr '\n' ',')"
  fi
fi
if [[ -r "$sentry_cg/cpu.stat" ]]; then
  cpu_end="$(cat "$sentry_cg/cpu.stat" 2>/dev/null || true)"
  if [[ -n "$cpu_end" ]]; then
    printf 'SENTRY_CPU_STATS_AFTER=%s\n' "$(printf '%s' "$cpu_end" | tr '\n' ',')"
  fi
fi
printf 'CGROUP_RUNSC_EXIT=%s\n' "$rc"
if [[ "$found" -eq 0 || "$cgroup_mismatch" -ne 0 ]]; then
  echo 'P0_SCOPE_VALIDATION_FAILED: Sentry cgroup absent or unexpected limits' >&2
  exit 96
fi
exit "$rc"

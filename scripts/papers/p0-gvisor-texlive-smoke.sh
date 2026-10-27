#!/usr/bin/env bash
# Controlled P0 smoke test; NEVER invoked by the production API or compile worker.
# Requires the pinned, previously pulled TeX Live image and local Docker access.
# Only touches this run's /var/tmp directory and a labeled, non-running export container.
set -Eeuo pipefail
umask 077

if [[ "${PAPERS_P0_CONFIRM:-}" != 1 ]]; then
  echo 'Explicitly set PAPERS_P0_CONFIRM=1 to run this disposable P0 test.' >&2
  exit 2
fi

for tool in cc curl docker jq sha512sum tar timeout mktemp mountpoint findmnt umount; do
  command -v "$tool" >/dev/null || { echo "Missing executable: $tool" >&2; exit 2; }
done

readonly TEX_IMAGE='texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4'
readonly GVISOR_RELEASE='20261019.0'
readonly GVISOR_ARCHIVE_SHA512='bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915'
readonly GVISOR_URL="https://storage.googleapis.com/gvisor/releases/release/${GVISOR_RELEASE}/x86_64"
docker image inspect "$TEX_IMAGE" >/dev/null || {
  echo "Pinned TeX Live image not present: $TEX_IMAGE" >&2
  exit 2
}
avail_kb="$(df -Pk /var/tmp | awk 'NR==2 {print $4}')"
if (( avail_kb < 5 * 1024 * 1024 )); then
  echo 'Refusing P0 image export: less than 5 GiB free on /var/tmp.' >&2
  exit 2
fi

root="$(mktemp -d /var/tmp/atoman-papers-p0-texlive.XXXXXXXX)"
readonly root
readonly bundle="$root/bundle"
readonly runtime="$root/bin/runsc"
readonly sandbox_id="papers-p0-texlive-${BASHPID}"
docker_id=''
cleanup() {
  local original=$?
  trap - EXIT INT TERM
  set +e
  local cleanup_error=0
  if [[ -n "$docker_id" ]]; then
    docker rm -f "$docker_id" >/dev/null 2>&1 || cleanup_error=1
  fi
  if [[ -x "$runtime" && -d "$root/state" ]]; then
    # Only the container ID allocated in this test's dedicated runsc root.
    if "$runtime" --systemd-cgroup --root="$root/state" list 2>/dev/null | grep -q "^$sandbox_id[[:space:]]"; then
      "$runtime" --systemd-cgroup --root="$root/state" kill "$sandbox_id" KILL >/dev/null 2>&1 || true
      "$runtime" --systemd-cgroup --root="$root/state" delete --force "$sandbox_id" >/dev/null 2>&1 || true
    fi
    if "$runtime" --systemd-cgroup --root="$root/state" list 2>/dev/null | grep -q "^$sandbox_id[[:space:]]"; then
      cleanup_error=1
    fi
  fi
  local null_netns="$root/state/null-netns"
  if mountpoint -q "$null_netns" 2>/dev/null; then
    if [[ "$(findmnt -n -o FSTYPE --target "$null_netns" 2>/dev/null)" == nsfs ]]; then
      umount "$null_netns" || cleanup_error=1
    else
      echo 'Unexpected mount type in dedicated state root; refusing cleanup.' >&2
      cleanup_error=1
    fi
  fi
  if (( original != 0 )); then
    echo "P0 failed with status $original; recent sandbox output:" >&2
    if [[ -f "$root/runtime.log" ]]; then tail -n 25 "$root/runtime.log" >&2; fi
    for probe_log in "$root"/probe-*.log; do
      if [[ -f "$probe_log" ]]; then echo "=== $probe_log ===" >&2; tail -n 45 "$probe_log" >&2; fi
    done
    if [[ -f "$root/output/pdflatex.log" ]]; then tail -n 25 "$root/output/pdflatex.log" >&2; fi
  fi
  if (( cleanup_error == 0 )); then
    case "$root" in
      /var/tmp/atoman-papers-p0-texlive.*) rm -rf -- "$root" || cleanup_error=1 ;;
      *) echo "Unsafe temporary path: $root" >&2; cleanup_error=1 ;;
    esac
  fi
  if (( cleanup_error != 0 )); then
    echo "P0 cleanup incomplete; review ONLY dedicated test directory: $root" >&2
    exit 1
  fi
  echo 'P0 dedicated container, mounts and files: cleaned'
  exit "$original"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -m 700 "$root/bin" "$root/state" "$bundle" "$root/output"
# gVisor's unprivileged file gofer needs directory traversal to the exported OCI rootfs.
# Contents remain protected by their own per-file and per-directory permissions.
chmod 0711 "$root" "$bundle"
printf 'RUN_DIR=%s\n' "$root"
printf 'TEX_IMAGE=%s\n' "$TEX_IMAGE"

curl --fail --show-error --silent --location --retry 2 --connect-timeout 15 --max-time 180 \
  "$GVISOR_URL/gvisor.tar.zstd" -o "$root/gvisor.tar.zstd"
curl --fail --show-error --silent --location --retry 2 --connect-timeout 15 --max-time 60 \
  "$GVISOR_URL/gvisor.tar.zstd.sha512" -o "$root/gvisor.tar.zstd.sha512"
printf '%s  %s\n' "$GVISOR_ARCHIVE_SHA512" "$root/gvisor.tar.zstd" | sha512sum -c -
(cd "$root" && sha512sum -c gvisor.tar.zstd.sha512)
tar --zstd -xf "$root/gvisor.tar.zstd" -C "$root/bin"
"$runtime" --version | head -n 2

mkdir -p "$bundle/rootfs"
docker_id="$(docker create --label atoman.papers.p0=texlive-smoke "$TEX_IMAGE" /bin/sh)"
docker export "$docker_id" | tar -xf - -C "$bundle/rootfs"
chmod 0755 "$bundle/rootfs"
docker rm "$docker_id" >/dev/null
docker_id=''
mkdir -p "$bundle/rootfs/inputs" "$bundle/rootfs/output" "$bundle/rootfs/tmp"
# umask 077 protects host staging; the non-root OCI user must still traverse these mounts.
chmod 0755 "$bundle/rootfs/inputs" "$bundle/rootfs/output" "$bundle/rootfs/tmp"
cat > "$bundle/rootfs/inputs/main.tex" <<'TEX'
\documentclass{article}
\begin{document}
Atoman Papers gVisor isolation smoke test.
\section{Introduction}
A minimal equation is $E=mc^2$.
\end{document}
TEX
chmod 644 "$bundle/rootfs/inputs/main.tex"
cc -static -O2 -Wall -Wextra -o "$bundle/rootfs/inputs/p0-sandbox-probe" \
  "$(dirname "$0")/p0-sandbox-probe.c"
chmod 755 "$bundle/rootfs/inputs/p0-sandbox-probe"
chown 65534:65534 "$root/output"
chmod 0700 "$root/output"

(
  cd "$bundle"
  "$runtime" spec -- /bin/sh -c 'echo not-compiled' >/dev/null
)
jq \
  --arg out "$root/output" \
  --arg cg "system.slice:atomanp0:$sandbox_id" \
  '.linux.cgroupsPath = $cg |
   .process.args = ["/bin/sh","-c","set -eu; pdflatex --version | head -n 1; cd /inputs; pdflatex -no-shell-escape -interaction=nonstopmode -halt-on-error -output-directory=/output main.tex > /output/pdflatex.log 2>&1; test -s /output/main.pdf; echo PAPERS_P0_TEX_COMPILE_OK"] |
   .process.cwd = "/inputs" |
   .process.user = {"uid":65534,"gid":65534} |
   .process.noNewPrivileges = true |
   .process.capabilities = {"bounding":[],"effective":[],"inheritable":[],"permitted":[],"ambient":[]} |
   .process.env = ["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin","HOME=/tmp","LANG=C.UTF-8","TEXMFVAR=/tmp/texmf-var","TEXMFCONFIG=/tmp/texmf-config","TEXMFCACHE=/tmp/texmf-cache"] |
   .root.readonly = true |
   .mounts += [
     {"destination":"/output","type":"bind","source":$out,"options":["rbind","rw","nosuid","nodev"]},
     {"destination":"/tmp","type":"tmpfs","source":"tmpfs","options":["nosuid","nodev","mode=1777","size=67108864"]}
   ] |
   .linux.resources.memory = {"limit":805306368,"swap":805306368} |
   .linux.resources.pids = {"limit":256} |
   .linux.resources.cpu = {"quota":100000,"period":100000}' \
   "$bundle/config.json" > "$bundle/config.tmp"
mv "$bundle/config.tmp" "$bundle/config.json"

run_scoped() {
  local mode="$1" duration="$2" mem="$3" cpu="$4" tasks="$5" runtime_log="$6"
  local unit="atoman-papers-p0-${BASHPID}-${mode}"
  local scope_log="$root/scope-${mode}.log"
  local helper="$(dirname "$0")/p0-cgroup-scope-exec.sh"
  local result=0
  systemd-run --quiet --scope --collect --unit="$unit" \
    -p "MemoryMax=$mem" -p "MemorySwapMax=0" \
    -p "CPUQuota=$cpu" -p "TasksMax=$tasks" -p Delegate=yes \
    /bin/bash "$helper" PAPERS_P0_SCOPE_EXEC \
    "$runtime" "$root" "$bundle" "$sandbox_id" "$runtime_log" "$duration" \
    > "$scope_log" 2>&1 || result=$?
  cat "$scope_log"
  return "$result"
}

printf '=== REAL LATEX INSIDE gVISOR SYSTRAP (NO NETWORK) ===\n'
# The transient systemd scope is the hard host-side cgroup boundary.
if run_scoped latex 90 1024M 100% 256 "$root/runtime.log"; then
  cat "$root/runtime.log"
else
  status=$?
  echo "P0 scoped runsc exited with status $status" >&2
  exit "$status"
fi

test -s "$root/output/main.pdf"
head -c 5 "$root/output/main.pdf" | grep -q '%PDF-'
printf 'PDF_BYTES=%s\n' "$(wc -c < "$root/output/main.pdf")"
printf 'PDF_HEADER=%s\n' "$(head -c 8 "$root/output/main.pdf")"
printf 'IMAGE_DIGEST=%s\n' "$TEX_IMAGE"
printf 'P0_TEX_TEST_SUCCESS\n'

cp "$bundle/config.json" "$bundle/config.latex.json"
run_resource_probe() {
  local mode="$1"
  local exit_code=0
  local limit=805306368
  local pids=256
  local quota=100000
  case "$mode" in
    cpu) quota=50000 ;;
    memory) limit=201326592 ;;
    pids) pids=128 ;;
  esac
  jq --arg mode "$mode" --argjson limit "$limit" --argjson pids "$pids" --argjson quota "$quota" \
    '.process.args = ["/inputs/p0-sandbox-probe", $mode] |
     .linux.resources.memory.limit = $limit |
     .linux.resources.memory.swap = $limit |
     .linux.resources.pids.limit = $pids |
     .linux.resources.cpu.quota = $quota' \
    "$bundle/config.latex.json" > "$bundle/config.tmp"
  mv "$bundle/config.tmp" "$bundle/config.json"
  printf '=== RESOURCE PROBE: %s ===\n' "$mode"
  jq -c '{args:.process.args, cgroupsPath:.linux.cgroupsPath, pids:.linux.resources.pids}' "$bundle/config.json"
  local duration=14 memory_max=768M cpu_quota=100% tasks_max=256
  case "$mode" in
    cpu) cpu_quota=50% ;;
    memory) memory_max=256M ;;
    pids) tasks_max=192 ;;
    timeout) duration=2 ;;
  esac
  run_scoped "$mode" "$duration" "$memory_max" "$cpu_quota" "$tasks_max" \
    "$root/probe-${mode}.log" || exit_code=$?
  if [[ -f "$root/probe-${mode}.log" ]]; then cat "$root/probe-${mode}.log"; fi
  if [[ "$mode" == timeout ]]; then
    if [[ "$exit_code" -ne 124 ]]; then
      echo "Timeout not enforced: exit_code=$exit_code" >&2
      return 1
    fi
    if "$runtime" --systemd-cgroup --root="$root/state" list 2>/dev/null | grep -q "^$sandbox_id[[:space:]]"; then
      echo 'P0_TIMEOUT_SANDBOX_REQUIRES_EXPLICIT_CLEANUP'
      "$runtime" --systemd-cgroup --root="$root/state" kill "$sandbox_id" KILL >/dev/null 2>&1 || true
      "$runtime" --systemd-cgroup --root="$root/state" delete --force "$sandbox_id" >/dev/null 2>&1 || true
    fi
    for attempt in 1 2 3 4 5 6 7 8 9 10; do
      if ! "$runtime" --systemd-cgroup --root="$root/state" list 2>/dev/null | grep -q "^$sandbox_id[[:space:]]"; then break; fi
      sleep 0.1
    done
    if "$runtime" --systemd-cgroup --root="$root/state" list 2>/dev/null | grep -q "^$sandbox_id[[:space:]]"; then
      echo 'Timeout cleanup failed: sandbox still listed' >&2
      return 1
    fi
    printf 'P0_TIMEOUT_SANDBOX_CLEANUP_VERIFIED\n'
  elif [[ "$mode" == memory ]]; then
    if [[ "$exit_code" -ne 137 ]] || grep -q 'P0_MEMORY_LIMIT_FAILED' "$root/probe-${mode}.log"; then
      echo "Memory limit not proven (exit_code=$exit_code)" >&2
      return 1
    fi
  elif [[ "$mode" == pids && "$exit_code" -ne 0 ]]; then
    if grep -Eq 'SENTRY_PIDS_EVENTS_SAMPLED=max [1-9]' "$root/scope-pids.log"; then
      echo 'P0_PID_LIMIT_TRIGGERED_BUT_RUNTIME_ABORTED: kernel enforcement observed, graceful failure not verified' >&2
    fi
    echo "$mode limit failed (exit_code=$exit_code)" >&2
    return 1
  elif [[ "$exit_code" -ne 0 ]]; then
    echo "$mode limit failed (exit_code=$exit_code)" >&2
    return 1
  fi
  printf 'P0_RESOURCE_%s_PASS\n' "$mode"
}
case "${PAPERS_P0_RESOURCE_MODES:-all}" in
  all)
    run_resource_probe cpu
    run_resource_probe memory
    run_resource_probe pids
    run_resource_probe timeout ;;
  pids-timeout)
    run_resource_probe pids
    run_resource_probe timeout ;;
  pids) run_resource_probe pids ;;
  timeout) run_resource_probe timeout ;;
  *)
    echo 'Invalid PAPERS_P0_RESOURCE_MODES' >&2
    exit 2 ;;
esac
printf 'P0_ALL_SELECTED_RESOURCE_TESTS_PASS\n'

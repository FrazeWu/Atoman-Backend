#!/usr/bin/env bash
# Controlled P0-only integration: PID pressure -> Go Supervisor -> real LaTeX.
# NEVER expose this script to users, HTTP handlers, or production workers.
set -Eeuo pipefail
umask 077

if [[ "${PAPERS_P0_LIVE_CONFIRM:-}" != "1" || "$(id -u)" != 0 ]]; then
  echo 'P0 requires deliberate local root invocation: PAPERS_P0_LIVE_CONFIRM=1' >&2
  exit 2
fi
for cmd in docker curl sha512sum tar gcc go mountpoint findmnt umount timeout; do
  command -v "$cmd" >/dev/null || { echo "Missing required command: $cmd" >&2; exit 2; }
done
readonly image="texlive/texlive@sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4"
readonly version="20261019.0"
readonly digest="bcbf431b2f9a039f6ab23bfe23986031130c0f149294978cedfd46910901deba0737d8642c848f597ed4b5c72324885db4d75ee86a1f6a90915fbe1f2f2eb915"
docker image inspect "$image" >/dev/null || { echo "Pinned image not cached. Pull exact digest first." >&2; exit 2; }
available="$(df -Pk /var/tmp | awk 'NR==2 {print $4}')"
if (( available < 7*1024*1024 )); then
  echo 'P0 requires at least 7 GiB free; refusing.' >&2
  exit 2
fi

readonly suffix="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
readonly root="$(mktemp -d /var/tmp/atoman-papers-p0-live.XXXXXXXX)"
readonly first="p0-pids-$suffix"
readonly second="p0-tex-$suffix"
export_id=''
cleanup() {
  local original=$?
  trap - EXIT INT TERM
  set +e
  local failed=0
  if [[ -n "$export_id" ]]; then docker rm -f "$export_id" >/dev/null 2>&1 || failed=1; fi
  for id in "$first" "$second"; do
    local state="$root/$id/state"
    if [[ -x "$root/bin/runsc" && -d "$state" ]]; then
      local listed
      listed="$("$root/bin/runsc" --systemd-cgroup --root="$state" list 2>/dev/null)"
      if echo "$listed" | grep -qE "^$id[[:space:]]"; then
        "$root/bin/runsc" --systemd-cgroup --root="$state" kill "$id" KILL >/dev/null 2>&1 || true
        "$root/bin/runsc" --systemd-cgroup --root="$state" delete --force "$id" >/dev/null 2>&1 || failed=1
      fi
      if "$root/bin/runsc" --systemd-cgroup --root="$state" list 2>/dev/null | grep -qE "^$id[[:space:]]"; then failed=1; fi
      local ns="$state/null-netns"
      if mountpoint -q "$ns" 2>/dev/null; then
        if [[ "$(findmnt -n -o FSTYPE --target "$ns" 2>/dev/null)" == nsfs ]]; then
          umount "$ns" || failed=1
        else
          echo "Unexpected mount at $ns; refusing to remove it" >&2; failed=1
        fi
      fi
    fi
    for unit in "atomanp0-$id.scope" "atoman-papers-p0-$id.scope"; do
      if [[ "$(systemctl is-active "$unit" 2>/dev/null)" == active ]]; then
        echo "P0 scope remains active: $unit" >&2
        failed=1
      fi
    done
  done
  if (( original != 0 )) && [[ -f "$root/result.log" ]]; then
    echo 'P0 launcher diagnostics:' >&2
    tail -n 70 "$root/result.log" >&2
  fi
  if (( failed == 0 )); then
    case "$root" in
      /var/tmp/atoman-papers-p0-live.*) rm -rf -- "$root" || failed=1 ;;
      *) echo "Unsafe temporary path: $root" >&2;failed=1 ;;
    esac
  fi
  if (( failed != 0 )); then
    echo "P0 cleanup failed. Inspect dedicated test dir: $root" >&2
    exit 1
  fi
  echo "P0 cleanup verified; no test-owned processes, mounts or files."
  exit "$original"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

chmod 0711 "$root"
mkdir -m 700 "$root/bin"
printf 'P0_RUN_SUFFIX=%s\n' "$suffix"
printf 'P0_IMAGE=%s\n' "$image"
readonly base_url="https://storage.googleapis.com/gvisor/releases/release/$version/x86_64"
curl -fsSL --retry 2 --connect-timeout 15 --max-time 180 "$base_url/gvisor.tar.zstd" -o "$root/gvisor.tar.zstd"
curl -fsSL --retry 2 --connect-timeout 15 --max-time 60 "$base_url/gvisor.tar.zstd.sha512" -o "$root/gvisor.tar.zstd.sha512"
printf '%s  %s\n' "$digest" "$root/gvisor.tar.zstd" | sha512sum -c -
(cd "$root" && sha512sum -c gvisor.tar.zstd.sha512)
tar --zstd -xf "$root/gvisor.tar.zstd" -C "$root/bin"
"$root/bin/runsc" --version | head -n 2

mkdir -m 700 "$root/base-rootfs"
export_id="$(docker create --label atoman.papers.p0=live --label atoman.papers.p0.id="$suffix" "$image" /bin/sh)"
docker export "$export_id" | tar -xf - -C "$root/base-rootfs"
docker rm "$export_id" >/dev/null
export_id=''
chmod 0755 "$root/base-rootfs"

for id in "$first" "$second"; do
  mkdir -m 711 "$root/$id"
  mkdir -m 711 "$root/$id/bundle"
  mkdir -m 700 "$root/$id/state" "$root/$id/output"
  cp -al "$root/base-rootfs" "$root/$id/bundle/rootfs"
  chmod 0755 "$root/$id/bundle/rootfs"
  mkdir -p "$root/$id/bundle/rootfs/inputs"
  chmod 0755 "$root/$id/bundle/rootfs/inputs"
  chown 65534:65534 "$root/$id/output"
  chmod 0700 "$root/$id/output"
done
gcc -static -O2 -Wall -Wextra -o "$root/$first/bundle/rootfs/inputs/p0-sandbox-probe" "$(dirname "$0")/p0-sandbox-probe.c"
chmod 0755 "$root/$first/bundle/rootfs/inputs/p0-sandbox-probe"

cat > "$root/$second/bundle/rootfs/inputs/main.tex" <<'TEX'
\documentclass{article}
\begin{document}
Atoman Papers: actual gVisor sandbox follow-up compile.
\section{Recovery}
The next LaTeX compilation works after PID exhaustion. $E=mc^2$.
\end{document}
TEX
chmod 0644 "$root/$second/bundle/rootfs/inputs/main.tex"
printf 'P0_BUNDLE_STAGE_OK\n'

cd "$(dirname "$0")/../.."
PAPERS_P0_LIVE_CONFIRM=1 timeout 180s go run ./cmd/papers_p0_live_runner \
  -root "$root" -runsc "$root/bin/runsc" \
  -helper "$(pwd)/scripts/papers/p0-cgroup-scope-exec.sh" -suffix "$suffix" \
  > "$root/result.log" 2>&1
cat "$root/result.log"
grep -q 'P0_LIVE_PID_FAILURE_THEN_REAL_LATEX_SUCCESS' "$root/result.log"
printf 'P0_LIVE_FULL_SEQUENCE_PASS\n'

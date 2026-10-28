#!/usr/bin/env bash
# Read-only validation for reference-only service profiles. This does NOT
# install system accounts, run systemctl, or change host state.
set -Eeuo pipefail
readonly root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly units="$root/deploy/papers/systemd"
if [[ "${1:-}" != "--check" || "$#" != 1 ]];then
 echo 'Usage: check-papers-systemd-profiles.sh --check (read-only)' >&2
 exit 2
fi
for bin in systemd-analyze awk grep stat getent;do
 command -v "$bin" >/dev/null || { echo "Missing $bin" >&2;exit 2; }
done
for file in "$units/atoman-papers-launcher.service" "$units/atoman-papers-worker.service";do
 [[ -f "$file" && ! -L "$file" ]] || exit 2
 grep -Fq 'NEVER INSTALL OR ENABLE AS-IS' "$file" || {
  echo "Missing deployment warning in $file" >&2;exit 1;
 }
 grep -Fxq 'ExecStart=/usr/bin/false' "$file" || {
  echo "Unit is not forced fail-closed: $file" >&2;exit 1;
 }
 grep -Fxq 'ConditionPathExists=/etc/atoman/papers/SECURITY_REVIEW_APPROVED' "$file" || {
  echo "Unit lacks security signoff gate: $file" >&2;exit 1;
 }
 grep -Fxq 'ProtectSystem=strict' "$file" || exit 1
 grep -Fxq 'NoNewPrivileges=true' "$file" || exit 1
done
grep -Fxq 'User=atoman-papers-worker' "$units/atoman-papers-worker.service"
grep -Fxq 'Group=atoman-papers-worker' "$units/atoman-papers-worker.service"
grep -Fxq 'CapabilityBoundingSet=' "$units/atoman-papers-worker.service"
grep -Fxq 'User=root' "$units/atoman-papers-launcher.service"
grep -Fxq 'ReadWritePaths=/run/atoman-papers /var/lib/atoman-papers' "$units/atoman-papers-launcher.service"
systemd-analyze verify "$units/atoman-papers-launcher.service" "$units/atoman-papers-worker.service"
echo 'P0_SYSTEMD_REFERENCE_PROFILES_VALID_AND_FAIL_CLOSED'
echo 'Dedicated static system account is deliberately NOT created in this validation.'
if getent passwd atoman-papers-worker >/dev/null;then
 echo 'P0_WORKER_USER_PREEXISTS (review manually before deployment)'
else
 echo 'P0_WORKER_USER_NOT_PROVISIONED'
fi

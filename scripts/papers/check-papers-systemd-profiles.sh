#!/usr/bin/env bash
# Static read-only checks for four deliberately fail-closed reference profiles.
set -Eeuo pipefail
readonly root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
readonly units="$root/deploy/papers/systemd"
if [[ "$#" != 1 || "$1" != "--check" ]];then
  echo 'Usage: check-papers-systemd-profiles.sh --check (read-only)' >&2
  exit 2
fi
for bin in systemd-analyze grep getent;do
  command -v "$bin" >/dev/null || { echo "Missing $bin" >&2;exit 2; }
done
for name in launcher worker pubgate publisher;do
  file="$units/atoman-papers-$name.service"
  [[ -f "$file" && ! -L "$file" ]] || { echo "Missing reference unit: $name" >&2;exit 1; }
  grep -Fq 'NEVER INSTALL OR ENABLE AS-IS' "$file" || {
    echo "$name missing explicit reference-only warning" >&2;exit 1;
  }
  for mandatory in \
    'ExecStart=/usr/bin/false' \
    'ConditionPathExists=/etc/atoman/papers/SECURITY_REVIEW_APPROVED' \
    'ProtectSystem=strict' \
    'NoNewPrivileges=true';do
    grep -Fxq "$mandatory" "$file" || { echo "$name missing exact line: $mandatory" >&2;exit 1; }
  done
  if [[ "$(grep -Ec '^ExecStart=' "$file")" != 1 ]];then
    echo "$name has multiple ExecStart entries and is no longer fail-closed" >&2;exit 1;
  fi
done
require_line(){
  grep -Fxq "$2" "$units/atoman-papers-$1.service" || {
    echo "Unsafe $1 reference: expected $2" >&2;exit 1;
  }
}
require_line launcher User=root
require_line launcher 'ReadWritePaths=/run/atoman-papers /var/lib/atoman-papers'
require_line worker User=atoman-papers-worker
require_line worker Group=atoman-papers-worker
require_line worker CapabilityBoundingSet=
require_line pubgate User=atoman-papers-gateway
require_line pubgate Group=atoman-papers-publish
require_line pubgate CapabilityBoundingSet=
require_line pubgate RestrictNamespaces=true
require_line pubgate IPAddressDeny=any
require_line publisher User=atoman-papers-publisher
require_line publisher Group=atoman-papers-publish
require_line publisher CapabilityBoundingSet=
require_line publisher RestrictAddressFamilies=AF_UNIX
require_line publisher IPAddressDeny=any
require_line publisher Type=oneshot
if grep -Fq '[Install]' "$units/atoman-papers-publisher.service";then
  echo 'Publisher must never have an automatic install target' >&2;exit 1;
fi
systemd-analyze verify \
  "$units/atoman-papers-launcher.service" \
  "$units/atoman-papers-worker.service" \
  "$units/atoman-papers-pubgate.service" \
  "$units/atoman-papers-publisher.service"
echo 'PAPERS_SYSTEMD_FOUR_REFERENCE_PROFILES_FAIL_CLOSED_PASS'
echo 'Only reference units were parsed. No unit or identity has been installed.'
for account in atoman-papers-worker atoman-papers-gateway atoman-papers-publisher;do
  if getent passwd "$account" >/dev/null;then
    echo "PAPERS_ACCOUNT_PREEXISTS_REVIEW_REQUIRED $account"
  else
    echo "PAPERS_ACCOUNT_NOT_PROVISIONED $account"
  fi
done

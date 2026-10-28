#!/usr/bin/env bash
# Read-only, fail-closed HOST preflight. NOT a release authorization.
# It never inspects or echoes secrets, edits firewall rules, or installs units.
set -Eeuo pipefail
umask 077
if [[ "$#" != 1 || "$1" != "--check" ]];then
  echo 'Usage: audit-papers-production-preflight.sh --check (read-only)' >&2
  exit 2
fi
readonly root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
blockers=0
notes=0
block(){
  printf 'BLOCKER %s\n' "$*"
  blockers=$((blockers+1))
}
note(){
  printf 'NOTE %s\n' "$*"
  notes=$((notes+1))
}
pass(){ printf 'PASS %s\n' "$*"; }
echo 'PAPERS_PRODUCTION_PREFLIGHT_READ_ONLY_BEGIN'
echo 'No firewall, container, account, systemd, production database or credentials will be mutated.'

# Static policy: references are intentionally unusable until a separately
# reviewed deployment replaces *all* /usr/bin/false executables.
if "$root/scripts/papers/check-papers-systemd-profiles.sh" --check >/dev/null 2>&1;then
  pass 'Four systemd reference unit profiles parse and remain fail-closed'
else
  block 'Missing or unsafe systemd reference profile'
fi
for account in atoman-papers-worker atoman-papers-gateway atoman-papers-publisher;do
  if ! getent passwd "$account" >/dev/null 2>&1;then
    block "Unprovisioned system identity: $account"
    continue
  fi
  record="$(getent passwd "$account")"
  IFS=: read -r _ _ uid gid _ home shell <<< "$record"
  if [[ "$shell" != /usr/sbin/nologin && "$shell" != /bin/false ]];then
    block "$account has an interactive login shell"
  fi
  if (( uid < 100 || uid == 0 ));then
    block "$account has an unexpected system UID"
  fi
  groups=" $(id -nG "$account" 2>/dev/null || true) "
  for privileged in docker sudo wheel;do
    if [[ "$groups" == *" $privileged "* ]];then
      block "$account is a member of privileged group $privileged"
    fi
  done
  if [[ "$account" == atoman-papers-worker && "$groups" == *" atoman-papers-publish "* ]];then
    block 'Compile Worker shares the publisher Unix socket group'
  fi
done
if ! getent group atoman-papers-publish >/dev/null 2>&1;then
  block 'Missing publisher/gateway-only shared Unix group'
fi

# A loopback socket cannot be reached from a public interface without some
# other forwarder, but old firewall allows still violate the desired policy.
if command -v ss >/dev/null 2>&1;then
  if listen="$(ss -lntH 2>/dev/null)";then
    any=0
    while IFS= read -r addr;do
      [[ -n "$addr" ]] || continue
      case "$addr" in
        *:5432|*:9100|*:9101)
          any=1
          case "$addr" in
            127.0.0.1:*|'[::1]:'*|::1:*|localhost:*)
              pass "Sensitive listener bound to loopback: $addr" ;;
            *)
              block "Sensitive listener may be public: $addr" ;;
          esac
          ;;
      esac
    done < <(printf '%s\n' "$listen" | awk '{print $4}')
    if (( any == 0 ));then note 'No sensitive 5432/9100/9101 listeners observed';fi
  else
    block 'Could not inspect TCP listeners with ss'
  fi
else
  block 'ss is unavailable; listener exposure cannot be verified'
fi

if command -v docker >/dev/null 2>&1;then
  if bindings="$(docker ps --format '{{.Names}} {{.Ports}}' 2>/dev/null)";then
    exposed=0
    while IFS= read -r line;do
      case "$line" in
        *"0.0.0.0:"*|*":::"*|*"[::]:"*)
          if [[ "$line" == *"->5432/tcp"* || "$line" == *"->9000/tcp"* || "$line" == *"->9100/tcp"* || "$line" == *"->9101/tcp"* ]];then
            block 'Sensitive Docker port mapped to public interface'
            exposed=1
          fi
          ;;
      esac
    done <<< "$bindings"
    if (( exposed == 0 ));then pass 'No public Docker host mappings detected for Papers storage ports';fi
  else
    block 'Docker host port inspection unavailable'
  fi
else
  block 'Docker inspection unavailable (storage mapping unverified)'
fi

if command -v ufw >/dev/null 2>&1;then
  if ufw_rules="$(ufw status 2>/dev/null)";then
    if [[ "$ufw_rules" != *"Status: active"* ]];then
      note 'UFW inactive; upstream firewall must be independently reviewed'
    else
      for port in 5432 9100 9101;do
        if printf '%s\n' "$ufw_rules" | grep -Eq "^[[:space:]]*$port(/tcp)?[[:space:]]+ALLOW[[:space:]]+Anywhere";then
          block "Stale UFW Anywhere allow rule on sensitive port $port (IPv4/IPv6)"
        else
          pass "No UFW Anywhere allow rule on sensitive port $port"
        fi
      done
    fi
  else
    block 'Unable to read UFW rules'
  fi
else
  note 'UFW unavailable; all nftables/firewall sources require manual audit'
fi

if command -v systemctl >/dev/null 2>&1;then
  for svc in launcher worker pubgate publisher;do
    if systemctl is-active --quiet "atoman-papers-$svc.service" 2>/dev/null;then
      block "Papers $svc already active without final pre-production approval"
    fi
  done
fi
if command -v runsc >/dev/null 2>&1;then
  note 'runsc exists on PATH; version, signatures, permissions and cgroup setup still require review'
else
  block 'No audited installed gVisor runtime for production Launcher'
fi

# Today's backend foundation exposes edit APIs but not compile or PDF routes.
if grep -Fq 'private.POST("/projects/:id/compile"' "$root/internal/modules/papers/http.go";then
  note 'A compile route appears present; it still needs full authorization review'
else
  block 'Papers production compile API is not integrated'
fi
if grep -Fq 'private.GET("/projects/:id/compiles/:jobId/pdf"' "$root/internal/modules/papers/http.go";then
  note 'PDF route appears present; it still needs tenant isolation audit'
else
  block 'Papers production PDF retrieval route is not integrated'
fi

if dfline="$(df -Pk /var/tmp 2>/dev/null | awk 'NR==2{print $4}')";then
  if [[ "$dfline" =~ ^[0-9]+$ ]];then
    if (( dfline < 16*1024*1024 ));then
      block 'Less than 16 GiB free in /var/tmp for capped TeX image/staging'
    else
      pass 'At least 16 GiB available for disposable archive staging'
    fi
  else
    block 'Cannot measure staging disk availability'
  fi
else
  block 'Cannot inspect staging disk availability'
fi

note 'External/cloud firewall, TLS and actual MinIO IAM were not tested by this HOST-ONLY audit'
note 'Signing key custody, production binaries, concurrency quotas and change approval remain manual gates'
printf 'PAPERS_PRODUCTION_PREFLIGHT_SUMMARY blockers=%d notes=%d\n' "$blockers" "$notes"
if (( blockers > 0 ));then
  echo 'PAPERS_PRODUCTION_NO_GO'
  exit 1
fi
echo 'PAPERS_LOCAL_HOST_PREFLIGHT_CLEAR_BUT_EXTERNAL_APPROVAL_STILL_REQUIRED'

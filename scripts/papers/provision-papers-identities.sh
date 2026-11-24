#!/usr/bin/env bash
# Read-only plan, not a production provisioning program. There is intentionally
# no --apply option; this must never change NSS, systemd or filesystem state.
set -Eeuo pipefail
if [[ "$#" != 1 || "$1" != "--plan" ]];then
  echo 'Usage: provision-papers-identities.sh --plan (READ ONLY)' >&2
  exit 2
fi
for tool in getent id;do command -v "$tool" >/dev/null || exit 2;done
echo 'PAPERS_IDENTITY_PLAN_ONLY: no user/group installation or restart'
echo 'Proposed service accounts (all system users, no login, no sudo/docker):'
for account in atoman-papers-worker atoman-papers-gateway atoman-papers-publisher;do
  if getent passwd "$account" >/dev/null;then
    record="$(getent passwd "$account")"
    IFS=: read -r _ _ uid gid _ home shell <<< "$record"
    printf 'EXISTS_REVIEW_REQUIRED account=%s uid=%s gid=%s home=%s shell=%s\n' \
      "$account" "$uid" "$gid" "$home" "$shell"
    if [[ "$shell" != /usr/sbin/nologin && "$shell" != /bin/false ]];then
      echo "REFUSE: $account has an interactive shell" >&2
      exit 1
    fi
    groups=" $(id -nG "$account") "
    for privileged in docker sudo wheel;do
      if [[ "$groups" == *" $privileged "* ]];then
        echo "REFUSE: $account is in $privileged" >&2
        exit 1
      fi
    done
  else
    printf 'CREATE_LATER account=%s shell=/usr/sbin/nologin home=/nonexistent system=yes\n' "$account"
  fi
done
group=atoman-papers-publish
if getent group "$group" >/dev/null;then
  printf 'EXISTS_REVIEW_REQUIRED group=%s\n' "$group"
else
  printf 'CREATE_LATER group=%s (only publisher and gateway may join)\n' "$group"
fi
echo 'Gateway: atoman-papers-gateway, primary group atoman-papers-publish'
echo 'Publisher: atoman-papers-publisher, primary group atoman-papers-publish'
echo 'Worker: atoman-papers-worker, separate primary group; NO publish group'
echo 'Launcher: root only after gVisor-specific capability/namespace security review'
echo 'Publisher socket: parent gateway:atoman-papers-publish mode 0750; socket 0660'
echo 'Launcher socket: parent root:atoman-papers-worker mode 0710; socket 0660'
echo 'S3: Publisher/Worker do not possess write credentials; gateway scoped Put-only'
echo 'DB: Root Launcher read-only, nonroot Worker lease-write role (audited separately)'
echo 'Do not share signing secrets, S3 admin credentials or Unix socket group.'
echo 'NO_CHANGES: no account/group creation, unit install, reload or restart.'

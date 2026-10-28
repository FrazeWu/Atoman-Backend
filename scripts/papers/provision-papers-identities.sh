#!/usr/bin/env bash
# Safe account provisioner. Default is PLAN only. An explicit --apply is
# intentionally not provided until production runtime is independently
# reviewed; accidental use from P0 worktrees must not mutate /etc/passwd.
set -Eeuo pipefail
if [[ "$#" != 1 || "$1" != "--plan" ]];then
 echo "Read-only only: provision-papers-identities.sh --plan" >&2
 exit 2
fi
for tool in getent id;do command -v "$tool" >/dev/null || exit 2;done
account=atoman-papers-worker
echo 'P0 system account provisioning plan (NO MODIFICATIONS):'
if getent passwd "$account" >/dev/null;then
 echo "Existing account must be audited: $account"
 getent passwd "$account"
else
 echo 'User: atoman-papers-worker (system UID, shell /usr/sbin/nologin)'
 echo 'Primary group: atoman-papers-worker (system group)'
 echo 'Home: /nonexistent; no login, no Docker group, no sudo privileges'
fi
if getent group docker | grep -qw "$account";then
 echo 'REFUSE: account is in the Docker group' >&2
 exit 1
fi
echo 'Root Launcher: existing root identity, separate read-only PostgreSQL role'
echo 'Socket: /run/atoman-papers/uds/launcher.sock root:atoman-papers-worker mode 0660'
echo 'Worker: only socket and its own queue database credential'
echo 'Launcher: no writable queue DB credential; rootfs in private published registry'
echo 'No user/group creation, unit installation, daemon reload or restart performed.'

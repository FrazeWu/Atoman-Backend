# Managed production runtime

Application code is built in an isolated worktree. Runtime files are not executed
from `/root` and the production services do not need access to the source checkout.

## Activation

Build `start_server` and `music_import_worker` from a clean committed backend
revision, then invoke `scripts/ops/activate-runtime.py --kind backend` with the
binary directory, full revision and the existing private production environment
file. Use `--dry-run` to inspect the unit overrides without making changes.

The activator writes an immutable, root-owned release under `/opt/atoman`, a
private systemd-readable environment file under `/etc/atoman`, and a service
override. API and worker run as `atoman`, with writable state under
`/var/lib/atoman` and logs under `/var/log/atoman`. Mirror runs separately as
`atoman-mirror`; its existing configuration is supplied with `LoadCredential`.
The API and Mirror are reached through the local Nginx proxy, not public ports.

The activator deliberately does not run schema migrations. Validate migration
compatibility, backup recovery and the exact binary revision before activation.
A failed activation restores previous unit overrides and environment and attempts
to restart the previous release. Previous binaries/configuration remain under
`/var/backups/atoman`; retain them until acceptance is complete.

The legacy `deploy-production.sh` detects managed-runtime overrides and uses the
same activator. Do not replace only the source checkout binary and assume the
running service has changed. Verify `/proc/<MainPID>/exe` and `go version -m`.

## Backup and recovery

`backup-postgres.py` creates private custom-format PostgreSQL archives, global
roles, checksums and exact public-table counts from a shared exported snapshot.
`--verify` restores to an isolated, network-disabled Docker container and checks
all public-table row counts. It does not write restored rows to production.
All errors fail the run; the `COMPLETE` marker appears only after success.

The checked-in timer runs daily at 03:30 UTC with a small randomized delay.
The installed service uses a file lock and verifies restoration. `--retain 14`
keeps the newest 14 successful backups explicitly identified by this tool's
manifest format. Partial and unrelated directories are never pruned.

These are local backups: they do not protect against loss of the host or its disk.
Do not upload plaintext databases or credentials to a public asset bucket.
An independently controlled, encrypted off-host destination is still required
for disaster recovery. A daily schedule is not a guaranteed recovery-point
objective. Measured restore time excludes provisioning, DNS, object storage and
application acceptance and is not a complete service recovery-time objective.

The isolated verification restores normalized ownership; application role/grant
recovery must also use the protected globals archive and preserved ACL metadata.
Production object storage requires its own versioned or independently backed-up
recovery plan. A successful temporary-object smoke test is not a full object-store
backup or disaster-recovery drill.

# Papers release resource hardening — 2026-11-24

## Verified development-branch results

- Each durable-PDF compile now mounts a dedicated root-created 128 MiB tmpfs
  with at most 4,096 inodes, noswap, nodev, nosuid and noexec. Successful
  publication, publication failure, and recovery after verified runtime
  shutdown remove the owned mount and workspace. Unknown mounts or unverified
  shutdown remain fail-closed. The live kernel block/inode exhaustion and
  cleanup tests passed.
- Runtime diagnostics are drained while at most 1 MiB is retained. The PDF
  ceiling remains 50 MiB. A durable PDF publisher forces bounded workspaces.
- Strict queue admission now serializes under the global slot row, with
  2 active jobs per owner, 16 globally, 30 submissions per owner/hour,
  60 globally/hour, and 2 GiB/8 GiB owner/global persisted-PDF admission
  accounting (including maximum-output reservations for active jobs).
  Exact idempotent replay bypasses new admission. Independent object-storage
  quotas and retention are still required for unreferenced objects.
- The isolated PostgreSQL queue and Papers module regression suites passed.
- The opt-in real execution of TestP0RealPDFStorageAndAuthenticatedDownload
  passed: authenticated HTTP submission -> actual queue runner -> real pinned
  gVisor / TeX Live -> durable PDF publication gateway -> MinIO -> authenticated
  HTTP PDF download, with a 14,278-byte PDF and digest
  sha256:d58c8ee146972d89b05a33eb07d39ff3ae04b7d6902ec5808a755209653a7e42.
  It tested cross-account, anonymous and revoked-access denial and verified
  the workspace was removed. The local test release fixture is NOT the
  production signed archive distribution path. No production credentials
  were used.

- Real PID exhaustion recovery regression completed successfully: the
  sandbox reported resource_exhausted_pids with a verified pids.max event,
  no valid artifact, and verified cleanup; the immediately following actual
  LaTeX job succeeded. The isolated harness exited 0 and removed all its
  owned processes, mounts, and files.

## Remaining production gates

The production compile feature must remain disabled until all live recovery
and timeout tests, production service/identity and scoped IAM provisioning,
signed release distribution, independent storage quota, database restore
verification, and invite-only production acceptance pass. No production
deployment is performed by these development changes.

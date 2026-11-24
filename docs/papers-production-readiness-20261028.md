# Atoman Papers: production preflight and capacity gates (2026-10-28)

Target: vmi3410530.
Branch: feat/papers-production-preflight-20261028.
Base commit: 61fcc8c (full TeX Live to immutable MinIO gateway and SIGKILL recovery accepted).

## Status

**Production NO-GO**. This work adds a read-only local host audit, two
deliberately disabled systemd service references, explicit identity planning,
and request-time resource safeguards in the prototype publication gateway.

The worktree does not create users, install or enable systemd units, mutate
the firewall, read production MinIO credentials, restart Atoman, or merge main.
All production binary, signing and deployment decisions require review.

## Host evidence (read-only)

| Surface | Observed | Assessment |
|---|---|---|
| PostgreSQL | 127.0.0.1:5432 | Host binding is loopback-only |
| MinIO S3 | 127.0.0.1:9100 | Host binding is loopback-only |
| MinIO Console | 127.0.0.1:9101 | Host binding is loopback-only |
| Atoman API | 127.0.0.1:8080 | Host binding is loopback-only |
| UFW | IPv4/IPv6 ALLOW Anywhere for 5432, 9100 and 9101 | Stale broad allow rules; investigation required |
| Papers Worker, Gateway and Publisher users | None provisioned | Blocker |
| Dedicated publisher/gateway socket group | None provisioned | Blocker |
| Audited production runsc | Not installed on PATH | Blocker |
| Compile and PDF HTTP routes | Absent from Papers module | Blocker |
| Storage | 148 GB disk, approximately 28 GB free before testing | Suitable for disposable single-node testing only |
| Memory | Approximately 7.8 GiB physical RAM | Start with one heavyweight compile slot |

The local loopback bindings do NOT prove cloud ingress, reverse proxies,
external forwarders, or firewall rules are safe. Do not remove existing UFW
rules until all MinIO/PostgreSQL/1Panel/NAS consumers are inventoried.

The read-only preflight currently reports 10 blockers. Its nonzero exit code
is expected and MUST NOT be suppressed to claim production readiness.

## Identity separation and deliberately disabled deployment units

| Principal | Proposed production identity | Intended minimum privileges |
|---|---|---|
| Publisher | atoman-papers-publisher | Approved, offline release signing; Unix socket only, no S3 PUT |
| Write gateway | atoman-papers-gateway | Conditional S3 PUT/GET on immutable prefix, no Delete/List |
| Worker | atoman-papers-worker | Dedicated queue-write role and Launcher socket access |
| Launcher | Root only pending gVisor-specific privilege review | Read-only PostgreSQL, trusted environment and sandbox lifecycle |
| TeX sandbox | gVisor-internal UID | No network, only restricted input/output |

Publisher and Gateway share only the atoman-papers-publish Unix socket group.
Worker MUST NOT join this group. Both publishing and Launcher Unix sockets
also require kernel SO_PEERCRED UID checks; filesystem ACLs alone are
insufficient authorization.

Four reference units exist under deploy/papers/systemd. ALL intentionally
retain the security-approval condition and a failing executable placeholder.
The publisher is a one-shot operation with no auto-install section. Production
Papers binaries do not exist yet: P0 test executables hardcode temporary
bucket/UID/loopback restrictions and must not be installed as services.

## New gateway limits

- Before accepting the body, the gateway checks statfs for free space at
  least Content-Length plus an additional 2 GiB reserve. Insufficient
  capacity returns HTTP 507, unavailable statfs returns HTTP 503.
- If a write or fsync fails with ENOSPC, it returns HTTP 507 rather than
  misreporting an incomplete body.
- The existing single in-flight upload slot remains enforced; a second
  concurrent publisher receives HTTP 429, with no second staging file.
- Unix HTTP server timeouts: header 10 seconds, full body 6 minutes,
  write/verification 8 minutes, idle 10 seconds, maximum headers 16 KiB.
- S3 conditional PUT and authenticated full SHA-256 readback remain required.
- These are local process-level limits; other users, co-located MinIO
  volumes, independent gateway instances and disk pressure can still consume
  space. No cross-process reservation or production quota is claimed.

## Performance measurements and proposed future acceptance targets

| Metric | Previously measured on one isolated host | Proposed target (not an SLA yet) |
|---|---|---|
| 1.100 GB archive publication via gateway | 16.455, 17.506 and 11.583 seconds (latest guard regression) | Fixed-node p95 under 60s, also test WAN |
| Gateway peak RSS, sampled | ~20.3, ~18.6 and ~18.7 MiB | Less than 128 MiB peak, confirmed via cgroup |
| Basic pdfLaTeX | Valid 1-page, 31,860-byte PDF | Warm-cache p95 below 30s for simple templates |
| SIGKILL to verified retry completion | 38.973 seconds | p95 below 90s on isolated host, no orphans |
| Concurrent heavyweight compiles | Database-wide slot = 1 | Keep 1 on 8 GiB host until multi-user benchmark |
| Disk headroom | ~23 GiB observed minimum during P0 tests | At least 2 GiB spare after staging, S3 and cache |
| Multi-user queue/API | Not yet integrated | Idempotency, status/cancel, PDF download and grants |
| Capacity monitoring | Test logs | Queue depths/p95 wait, CPU/RSS peaks, 429/507, recovery |

The 1.1 GB TeX environment is a low-frequency signed release, not something
to re-upload for every user compile. Observed 11.6-17.5s publication metrics
include digest/signature verification and backend readback; they are NOT
pure public network throughput or a multi-tenant performance guarantee.

## Reproducible read-only commands

Working directory:
    /root/Atoman/.worktrees/papers-production-preflight-20261028-backend

Commands:
    bash scripts/papers/audit-papers-production-preflight.sh --check
    bash scripts/papers/provision-papers-identities.sh --plan
    bash scripts/papers/check-papers-systemd-profiles.sh --check
    go test -race ./internal/platform/paperspubgate
    go vet ./...
    go build ./...

Latest complete guard regression: the pinned 1,100,430,848-byte TAR
was published in 11,583 ms, gateway observed peak RSS was 19,176 KiB,
real TeX Live generated a valid 31,860-byte PDF, and the entire
disposable run exited 0 after 86.68 seconds with verified cleanup.

The host audit MUST return a nonzero code while blockers remain. The
disposable gateway/TeX full-env E2E scripts may be used for regression on
the authorized test machine only, never against production credentials.

## Next implementation sequence

1. Audit every consumer and forwarder of 5432/9100/9101, then conduct an
   approved firewall change. Confirm actual cloud firewall and TLS behavior.
2. Implement non-P0 Publisher/Gateway/Launcher/Worker CLIs, secret delivery
   and identities, verify strict S3 IAM and systemd sandboxing in a separate
   non-production environment.
3. Test multiple uploaders, bounded 429 client retries (not yet provided),
   storage depletion, hung requests,
   object retention, network failures, queue lease fencing and cleanup.
4. Integrate Papers project permissions with immutable snapshot publication,
   durable compile jobs, job status/cancel and protected PDF retrieval.
   The execution path must stay DISABLED in production until approval.
5. Add editor compile progress/errors and PDF preview, then TeX Live
   2025/2026 and pdfLaTeX/XeLaTeX/LuaLaTeX/Biber template compatibility.

**This document is an audit and rollout plan, not a production security approval.**

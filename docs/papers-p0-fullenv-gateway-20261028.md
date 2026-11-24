# Atoman Papers: full TeX Live through immutable MinIO gateway — P0 acceptance

Date: 2026-10-28
Host: vmi3410530
Branch: feat/papers-gateway-fullenv-20261028 (base d91b186)
Scope: disposable security and performance tests only.

## Verdict

**GO for the isolated combined P0 sub-gate; NO-GO for Atoman Papers production.**

The real 1.1 GB pinned TeX Live rootfs was normalized, signed by a nonroot
publisher, streamed to private MinIO through the SO_PEERCRED Unix gateway,
read by a separate, read-only root Launcher, verified through Ed25519 and
SHA-256, and used to compile a valid PDF in gVisor. The SIGKILL recovery
path also completed, with its stale sandbox reclaimed before retry.

No production MinIO, PostgreSQL, systemd service, API route, primary branch,
or production credentials were changed or read by this experiment.

## Design and newly implemented integration

- Nonroot publisher: UID/GID 65531, S3 read-only credentials, signing key
  generated in memory, publishing only through paperspubgate.Transport.
- Write-once gateway: UID 65532 / GID 65531, confined S3 write account.
  Kernel SO_PEERCRED permits only publisher UID 65531 on its Unix socket.
- Compile Worker: UID/GID 65534; independently denied socket access and
  cannot access a local rootfs or signing secret.
- The publisher stages its Ed25519 public key in an owner-only assets
  directory; test controller moves it to a root-only 0400 file for Launcher.
- Launcher explicitly rejects signer, gateway Writer and S3 administrator
  secrets. Its S3 account is read-only; queue database access is read-only.
- The original local rootfs.tar is deleted after signed release publication
  and before Worker execution, preventing a silent local-TAR fallback.
- A real direct S3 PutObject using the restricted publisher identity returned
  AccessDenied, even when bypassing the controlled gateway.
- Test-only separate gateway UID path is explicitly selected; the original
  disposable 65534 publisher gateway mode remains available and unchanged.
- The test refuses the older unsafe MinIO IAM demonstration mode.

## Fixed runtime and archive

- TeX Live container digest:
  sha256:5613d47067c98df80d6b3206c9a731ba6e5d3eb5828fea892afd270426de4bd4
- gVisor: release-20261019.0 with pinned SHA-512 verification.
- MinIO test image digest:
  sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e
- Normalized TAR: 1,100,430,848 bytes (~1.100 GB or ~1.025 GiB).
- Archive contains 23,805 entries, with 569 absolute symlinks rewritten,
  12 SUID/SGID permission fields removed, and one irrelevant systemd unit
  excluded. The strict trusted TAR validation remained enabled.
- Docker export timestamps/metadata are not guaranteed byte-identical across
  exports: every release must bind the actual computed archive digest.

## Actual isolated measurements

| Measure | Baseline success | Real SIGKILL recovery |
| --- | ---: | ---: |
| Signed archive through gateway, including verification | 16,455 ms | 17,506 ms |
| Gateway observed peak RSS | 20,760 KiB | 19,076 KiB |
| Full normalized archive | 1,100,430,848 B | 1,100,430,848 B |
| PDF output | 1 page, 31,860 B | Real PDFLaTeX retry succeeded |
| SIGKILL to verified recovery | not applicable | 38,973 ms |
| Full end-to-end test, including build/setup/cleanup | 106.20 s | 146.10 s |
| Exit code and cleanup | 0, verified | 0, verified |

Gateway RSS above corresponds to about 20.3 MiB / 18.6 MiB, sampled
every 0.5 second from /proc, not a guaranteed upper bound.
Publisher duration includes backend conditional PUT, authenticated readback
and signature/digest verification, NOT just network upload time.
Observed disk free space was at least about 23 GiB while active, compared
with about 28 GiB after cleanup; those are rounded df observations, not
exact disk peak measurements.

These are single-node loopback benchmarks and MUST NOT be read as a
production concurrency target, public network SLA or capacity guarantee.

## Direct test evidence

Baseline log:
  /tmp/atoman-papers-gateway-fullenv-baseline-20261028.log

Verified markers:
  P0_GATEWAY_SEPARATE_UID_AND_UNAUTHORIZED_WORKER_DENIED
  P0_REAL_TEX_GATEWAY_SIGNED_RELEASE_PUBLISHED
  P0_S3_ONLY_ARCHIVE_NO_LOCAL_FALLBACK_AVAILABLE
  P0_REAL_TEX_BASELINE_SUCCEEDED
  P0_REAL_TEX_GATEWAY_NONADMIN_S3_SIGNED_ARCHIVE_GVISOR_PDF_E2E_PASSED
  P0 disposable scopes, containers and workspace verified cleaned.

Recovery log:
  /tmp/atoman-papers-gateway-fullenv-recovery-20261028.log

Verified markers:
  P0_FULLENV_GATEWAY_PUBLISHER_DIRECT_S3_PUT_ACCESS_DENIED
  P0_REAL_SENTRY_ACTIVE_BEFORE_KILL=pc-51bcfea595134fe3a099d677
  P0_FIRST_WORKER_SIGKILL_CONFIRMED
  P0_OLD_LEASE_EXPIRED_BEFORE_REAPER
  P0_REAL_ORPHAN_SANDBOX_REAPED
  P0_RETRY_REAL_PDFLATEX_FINISHED
  P0_REAL_GVISOR_WORKER_KILLED_RECOVERED_AND_LATEX_SUCCEEDED
  P0_PERF_GATEWAY_WORKER_RECOVERY_MS=38973
  P0_GATEWAY_ROOT_LAUNCHER_UNPRIVILEGED_WORKER_REAL_RECOVERY_PASS
  P0 disposable scopes, containers and workspace verified cleaned.

The Worker was killed while the PostgreSQL job was still running. After
expiry, the lease was fenced and the previous sandbox was reaped. The
record returned to queued at epoch 2, then a new Worker completed compile.

## Repeatable test commands (ONLY disposable P0)

Working directory:
  /root/Atoman/.worktrees/papers-gateway-fullenv-20261028-backend

Baseline:
  PAPERS_P0_DURABLE_CONFIRM=1 PAPERS_P0_FULLENV_CONFIRM=1 PAPERS_P0_GATEWAY_CONFIRM=1 PAPERS_P0_DURABLE_NO_KILL=1 bash scripts/papers/p0-fullenv-gateway-gvisor-e2e.sh

Worker SIGKILL recovery:
  PAPERS_P0_DURABLE_CONFIRM=1 PAPERS_P0_FULLENV_CONFIRM=1 PAPERS_P0_GATEWAY_CONFIRM=1 bash scripts/papers/p0-fullenv-gateway-gvisor-e2e.sh

These require root for the isolated containers and gVisor test scopes,
refuse P0 unsafe IAM mode, enforce randomized loopback ports and
ephemeral buckets, and clean the exact resources they create.

## Remaining production NO-GO conditions

1. Production system identities and systemd sandboxing for Publisher,
   Gateway, Launcher and Worker; protected Unix socket parent, private
   credentials, service lifecycle, crash and drift monitoring.
2. Production MinIO network segmentation, TLS, dedicated bucket/policies,
   Object Lock retention, IAM enforcement, key rotations, audit and backups;
   separately verify all denied bypass operations against production policy.
3. Approved reproducible TeX environment builder/release process, signing
   provenance, HSM/KMS or secrets management, pinned publisher trust roots
   with key rotation/revocation and source supply-chain review.
4. WAN throughput, high concurrency, disk exhaustion, denial-of-service
   resistance, stalled request deadlines, object lifecycle cleanup, cold
   cache and multitenant quota/queue stress tests.
5. Production API, invitations, editing, persisted compile jobs, real
   PDF asset storage/download and business permission lifecycle integration.
6. Engine/template compatibility: TeX Live 2025/2026, pdfLaTeX,
   XeLaTeX, LuaLaTeX, BibTeX/Biber, fonts and conference templates.

Keep this branch out of production until those gates are separately accepted.

## Regression QA

- go test -race passed for paperspubgate, papersassets, papersqueue,
  papersworker, paperslauncher, Unix Launcher, gateway fullenv publisher,
  and the P0 gateway CLI.
- go vet ./... and go build ./... passed for the full Backend repository.
- The nested standalone P0 MinIO IAM module passed go test and go vet.
- Bash syntax and Git whitespace checks passed.
- The original small, disposable gateway MinIO test was rerun after the
  separate-UID CLI modification and passed exit 0 (16.33 s). Existing
  publisher mode remains functional.
- Explicit final marker: P0_GATEWAY_FULLENV_FINAL_QA_PASS.

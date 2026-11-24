# Papers release acceptance — 2026-11-24

**Decision: NO-GO for production enablement.** Isolated component/integration acceptance succeeded, but the full sandbox stress test did not pass and production composition remains deliberately disabled.

## Verified results

- Backend release branch 0d41fde: complete Go build, Swagger source consistency, six core platform packages, isolated PostgreSQL queue and Papers API tests passed.
- Additional PDF-only / snapshot-only publication gateway role test passed after correction of test-directory permissions.
- Frontend 88234dcc: type check, 11 Papers unit/router tests, production Vite build passed. Frontend 8a6193ab adds the passing real-Chrome test of the production bundle (intercepted API fixtures, genuine two-page PDF; actual nonwhite canvas pixels, pagination, byte-identical download and disabled-state gating).
- Pinned TeX Live 2026 image and pinned gVisor release 20261019.0 restored in a disposable test environment; archive SHA-512 verified. Actual offline pdfLaTeX compiled one page (41,292 bytes, PDF 1.7). The running cgroup demonstrated 768 MiB memory, no swap, 256 PIDs, one CPU.
- The full resource-smoke script returned exit 1: CPU and memory probes passed; the PID probe observed a kernel pids.max event but gVisor exited 2, not the graceful expected outcome. Timeout test was not reached. The script reported dedicated container, mount and file cleanup. This is NOT a passing full resource acceptance.
- New opt-in `scripts/papers/p0-pdf-result-e2e.sh` passed on isolated MinIO and PostgreSQL. A separately compiled fixed TeX fixture (31,946 bytes) was published via the PDF-only Unix socket gateway, replayed immutably, committed via a real fenced queue lease and retrieved using the authenticated API with exact bytes. Anonymous reads, read-only credential writes/deletes, other-account access and access after grant revocation were rejected. This test uses synthetic successful sandbox evidence for the queue commit; it does not prove a live worker-to-PDF publishing end-to-end flow or production IAM. No production credentials or DB used.
- Public main site returned HTTP 200. No production release performed.

## Remaining release gates

1. Complete the real integrated worker -> gVisor -> immutable PDF -> owner API acceptance (including timeout, cancellation, crash recovery, PID pressure and verified cleanup).
2. Bound aggregate per-job output and host diagnostic storage; enforce resource and queue quotas.
3. Implement and provision the production composition (dedicated least-privilege identities, read-only launcher DB role, restricted storage roles, verified signed TeX release, production binaries, service profiles and audited runtime); retain default-off feature gate.
4. Repair/verify the existing failed production database backup service and test recoverability before migration.
5. Re-run full isolation and release acceptance before enabling any invited production user.

The test scripts are deliberately isolated. Do not interpret their confirmation flags as production release authorization.

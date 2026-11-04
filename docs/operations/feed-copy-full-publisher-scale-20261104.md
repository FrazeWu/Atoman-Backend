# Feed full snapshot: isolated COPY publisher parity and 100k/360k scale tests (2026-11-04)

## Scope / production safety

- Branch `perf/feed-copy-full-publish-20261104` is based on `perf/feed-copy-benchmark-20261104` (`30e7ca3`). This worktree is independent of `main`, the running API/Worker, and the production database. **NO production schema change, migration, feature flag, deployment, merge, push, manual snapshot refresh, or data deletion.**
- Only `FEED_SCALE_ISOLATED=yes` **AND** `FEED_SNAPSHOT_COPY_EXPERIMENT=yes` enable `publishDedupSnapshotViaCopyExperiment`; production defaults remain unchanged. Before any feed scan, an explicit PostgreSQL query verifies the DB has an allowed `_test` name and the active schema is an ephemeral `test_<32 hexadecimal UUID>` created by `testdb.Open`. Otherwise fail closed.
- `scripts/test-feed-scale-isolated.sh` propagates only the explicitly supplied test COPY flag. The script itself creates a disposable PostgreSQL 16 container (1.5 CPUs / 1.5 GiB RAM / no swap); Go test runs in a bounded 1.5-CPU / 2.2 GB transient scope with 14-minute cap, and rejects external production/test DSNs.
- `COPY` writes generation metadata, 9-column representative rows, 6-column group members, and scope-head upsert inside **one pinned pgx connection and one transaction**. Raw JSONB, UUIDs, normalized strings, nullable first/tail timestamps, position base, source versions and revision checkpoint fields retain the GORM row schema and original assignments. Any failure rolls back all rows and the head pointer. No COPY path is registered in HTTP or the refresh worker.

## End-to-end data, 100k synthetic prodshape

| Metric | GORM first | COPY first | GORM second | COPY second |
|---|---:|---:|---:|---:|
| Build total | 16.582 s | 18.986 s | 19.989 s | 10.715 s |
| Fetch | 5.191 s | 7.395 s | 6.619 s | 1.424 s |
| Duplicate annotation | 4.544 s | 6.929 s | 5.213 s | 6.574 s |
| Projection materialization | 0.446 s | 0.673 s | 0.535 s | 0.532 s |
| Transaction write and publish | 6.389 s | 3.970 s | 7.615 s | 2.177 s |
| Representative groups | 91,667 | 91,667 | 91,924 | 91,924 |
| First cumulative Go allocation | 2442.8 MiB | 1824.9 MiB | - | - |
| First Go peak RSS | 401,212 KiB | 420,688 KiB | - | - |

Both independent 100k run pipelines passed deep page-seek, eight-source and same-source concurrent writes, stale revision protection, prefix refresh, and old generation GC; all ephemeral containers were removed. Total time varies strongly across cache/CPU load; the initial COPY full build is **slower** despite a faster write phase. Do not claim a stable 100k end-to-end speedup.

## End-to-end data, 360k synthetic prodshape

| Metric | GORM first | COPY first | GORM second | COPY second |
|---|---:|---:|---:|---:|
| Build total | 84.101 s | 59.103 s | 73.837 s | 54.894 s |
| Fetch | 28.619 s | 5.356 s | 5.653 s | 5.974 s |
| Duplicate annotation | 31.464 s | 40.192 s | 42.323 s | 37.359 s |
| Projection materialization | 1.798 s | 2.131 s | 2.177 s | 1.954 s |
| Transaction write and publish | 22.203 s | 11.411 s | 23.679 s | 9.604 s |
| Representative groups | 330,001 | 330,001 | 330,258 | 330,258 |
| First cumulative Go allocation | 8902.3 MiB | 6676.9 MiB | - | - |
| First Go peak RSS | 1,271,556 KiB | 1,237,480 KiB | - | - |
| Peak PostgreSQL test container RAM (reported end) | 229.2 MiB | 235.8 MiB | - | - |

Both 360k runs passed all checks, exit code 0: 961 synthetic feed sources with the measured article/date/body/URL skew, old-generation MVCC GC of 360,001 members, 360,257 active members at the end, 128 incremental writes across 8 sources, same-source concurrent writers, stale read blocking and deep seek to page 32999. The 360k COPY write transaction was 49%-59% faster in these samples (22.203 -> 11.411 s; 23.679 -> 9.604 s). Overall build elapsed dropped 26%-30% *in these two sample executions*; importantly initial fetch varied 28.6s vs 5.4s under mixed VPS load, so this does **not** establish a reproducible production speedup. Memory stayed inside the benchmark limits. The experimental 120s Worker budget was not exceeded by these four builds, but there is insufficient production margin evidence.

## Functional parity and fault containment

- `TestSnapshotCopyFullPublishParityAndFallback`: GORM -> COPY -> GORM, 3 generations with exact row/member SQL field parity, Unicode/emoji headline, duplicate URL variant, JSONB source versions, first/tail pointers, deep/empty pages, stale-revision rejection, fallback writer and old-generation retention.
- `TestSnapshotCopyFullPublishFailuresRollBack`: eight injected failures (`before_generation`, `before_rows`, `during_rows`, `after_rows`, `during_members`, `after_members`, `before_head`, `after_head`); each must return error, leave old head/revision/readable rows untouched and no generation/row/member residue; subsequent successful COPY recovers.
- `TestSnapshotCopyFullPublishEmptyProjection`: publishing a zero-item generation must succeed without inconsistent head/count.
- `TestSnapshotCopyExperimentGuard` and `TestSnapshotCopyExperimentTargetGuard`: both flags required; production database/schema layouts rejected before any source fetch; exact ephemeral test schema accepted.
- Previous independent `COPY`/GORM equivalence, PostgreSQL WAL, constraint rollback and concurrent MVCC reader benchmarks were kept untouched in the parent worktree and remain available for further validation.

## Current decision / next gates

**NO-GO production.** Need fully paired repeated measurements including reversed execution order and fluctuating load, representative real-data anonymized equivalence, absolute Worker timeout envelope with monitoring, DB WAL/locks and vacuum over multiple generations, abort/retry under cancellation, legacy feed/HTTP/search/subscription/seen state contracts, rollout/rollback and backup-recovery rehearsal. No new Go service command or production migration has been enabled.

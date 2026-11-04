# Feed snapshot write-path: isolated pgx COPY versus GORM INSERT (2026-11-04)

## Scope and safeguards

- Isolated branch `perf/feed-copy-benchmark-20261104` based on `8f8f9fe`. Only a gated test under `internal/modules/feed/dedup_snapshot_copy_benchmark_test.go`. **Production snapshot writer, public HTTP path, Worker, feature flags, database migrations, and `main` are unchanged.**
- Benchmark runs only with `FEED_COPY_BENCHMARK=yes`, within a throwaway PostgreSQL 16 `_test` DB whose `testdb.Open` creates a new test schema. Uses a single database/sql pinned connection and `sql.Conn.Raw` to reach underlying pgx/stdlib connection; starts a **pgx transaction** before generation metadata + both COPY operations. A transaction is either committed or rolled back as a whole.
- Inserts the same 9-column representative records and 6-column member records, including UUID, timestamp, JSONB, normalized URL/title/hash, in both paths. GORM uses existing 350/300 per INSERT; pgx uses a `CopyFromSlice` generator to avoid duplicating an entire 100k set of rows in memory. The benchmark constructs one synthetic article per row, deliberately without production data.
- Compares all corresponding representative and member fields in SQL with `IS DISTINCT FROM`, checks both generations have exactly N rows and N members, and verifies an explicitly aborted transaction and a duplicate-position PostgreSQL primary-key violation each leave **zero** metadata/rows/members. These compare an isolated write portion only; they do not exercise full publish head swap and concurrent stale-reader behavior with COPY.

## Measurements

| Isolated test | GORM INSERT total (rows + members + generation) | pgx COPY transaction (same content) | Speedup | Results |
|---|---:|---:|---:|---|
| 10,000 members and 10,000 representatives | 583 ms | 202 ms | 2.89x | equal data, intentional rollback |
| 100,000 each, first run | 5.792 s | 1.877 s | 3.09x | equal data, intentional rollback |
| 100,000 each, repeat | 4.938 s | 1.834 s | 2.69x | equal data, intentional rollback and constraint-error rollback |

The last 100k run also measured intentional abort at 1.519 s and constraint-error rollback at 0.622 s (neither is a normal commit latency). The transaction test passed with process exit code 0. All isolated databases were cleaned up. These times do not include real feed fetch, duplicate clustering, materialization, full GORM head swap, or generation GC. The COPY write-side improvement therefore **cannot be described as a 3x improvement of full rebuilds**. Independent runs also experience CPU/I/O variance.

## Next integration requirements (NO-GO production)

1. Build an isolated opt-in **full snapshot publisher** using a single pinned connection, pgx BEGIN, both COPYs, generation metadata and head UPSERT, with a single COMMIT; preserve the original old-generation retention, revision/stale protection, lock safety and tail/first checkpoint fields. The existing GORM writer must remain the default until an explicit rollout.
2. Prove full projection equivalence after source changes, duplicate grouping, page 1/deep pagination, 100k and 360k, zero-date, Unicode/JSONB, and old-generation MVCC readers. Include transactional fault injection before row COPY, mid-COPY, between both COPYs, and before pointer swap.
3. Compare total CPU/RSS, DB WAL bytes, transaction lock wait / writers, peak DB size, rollback recovery and GC under controlled repeatable workloads; prove timeout budget and monitoring. A 3x write-only speedup is promising but not sufficient to unlock production.
4. This is an experimentation branch only; no merged/production deployment approved.

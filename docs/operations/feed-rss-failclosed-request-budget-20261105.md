# RSS-only HTTP snapshot fallback: bounded request admission (2026-11-05)

Status: **isolated experiment; NO-GO production**. This branch combines the existing 10,000-record hard cap with a deadline, cooperative cancellation and overload admission. The experimental endpoint still rejects requests outside disposable PostgreSQL test schemas and explicit flags; mixed Explore, personalized timelines and production worker are unchanged.

## Failure policy

- Healthy snapshot: read the projection; do not enter legacy fallback.
- Missing/stale/slow snapshot: the isolated RSS HTTP route admits at most **2 concurrent fallback scans per process**, using a nonblocking semaphore. Overload returns HTTP **503** immediately.
- Admitted legacy read: **1.5-second** child context covers PostgreSQL query, annotation and hydration. Deadline failure is **503**, not a partial 200. Pre-canceled clients return **499**.
- Input cap: one ordered SQL query with **LIMIT 10,001**; above **10,000** visible external RSS items, fail with **503** before annotation. A smaller test-only cap is accepted via `FEED_RSS_LEGACY_MAX_ITEMS`.
- The error is `feed.rss_snapshot_unavailable`; HTTP 503 includes `Retry-After: 30`. No incomplete totals or pages are returned as valid.
- Batch annotation API remains unchanged. A context-aware annotation variant checks cancellation during processing; cancellation is cooperative rather than preempting an individual title comparison.
- The semaphore is per-process, not a distributed capacity control.

## Focused tests
- Normal small-corpus stale/missing snapshot fallback retains complete DTO and pagination.
- Above-cap and admission-overload return 503, with Retry-After.
- Canceled clients return 499 without starting expensive legacy scans.
- A real PostgreSQL exclusive table lock causes the request to hit the bounded deadline and return 503.
- Context-aware dedup preserves the existing annotations and stops before completing a canceled scan.

## Remaining release gates
Production read path remains NO-GO. The bounded fallback intentionally reduces availability for a missing snapshot on a large corpus, rather than returning a potentially very slow full scan. The 360k synthetic snapshot test on parent branch passed; it did not prove production rollback, global capacity, multiple instances, real data shape or live Worker p99. Distributed overload admission, backup/restore rehearsal, longer-term telemetry, and production-safe rollout switches are still required.


## Executed acceptance results
- Full `go test ./internal/service -count=1`: PASS.
- Full Feed package sharded tests (including added HTTP overload/lock tests): 13/13 shards PASS, 316 tests enumerated.
- `go build -buildvcs=false ./...` and `go vet -buildvcs=false ./internal/modules/feed ./internal/service`: PASS.
- Real PostgreSQL exclusive-lock test: HTTP 503 after the 1.5-second fallback deadline; canceled request yields 499.
- Synthetic 360k corpus: `FEED_RSS_LEGACY_BUDGET_PROBE=yes FEED_SNAPSHOT_COPY_EXPERIMENT=yes FEED_SNAPSHOT_STAGE_TRACE=yes ./scripts/test-feed-prodshape-isolated.sh 360000` exited 0. The fallback probe emitted `SCALE_FALLBACK_REJECT count=360000 elapsed=1.07858486s http_status=503 no_partial_results=true`.
- The same run had a first COPY rebuild of **52.21s**, second rebuild **51.69s**, GC **4.42s**, first-page p95 **14.81ms**, and deep-page p95 **11.86ms**; 961 synthetic RSS sources and 330,001 initial representatives. Disposable test database was auto-removed.
- CPU checks are cooperative at item/group boundaries, not an absolute hard preemption of individual worst-case title comparisons. The request-level 10k cap and two-scan admission limit still constrain memory and work concurrency.

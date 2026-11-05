# Feed isolated RSS fallback and COPY scale validation (2026-11-05)

## Status

Experimental only; **not deployed or merged**. Production routes and read flags remain unchanged.

## Bounded legacy fallback

- The RSS-only isolated HTTP legacy reader now counts visible `external_rss` articles before scanning or annotating them. Default maximum: 10,000 raw items. Above it the endpoint returns HTTP 503 with `feed.rss_snapshot_unavailable` instead of synchronous full-corpus deduplication after a stale/missing snapshot.
- The small-corpus fallback contract is unchanged. An explicitly isolated env override `FEED_RSS_LEGACY_MAX_ITEMS` accepts positive values up to the default, allowing deterministic tests; invalid/oversized overrides do not increase the cap.
- Regression `TestRSSRecentHTTPRealParityFallbackAndConcurrency` passed, including stale snapshot and legacy-only 503 assertions at an injected 8-item limit. The service dedup equivalence test selection also passed.
- This is a safety guard, **not** a proof of bounded latency: `COUNT` still scans potentially large index/table ranges, and a race can insert more items between count and fetch. Before deploying any production equivalent, prefer an index-bounded `LIMIT cap+1` probe and explicit request CPU/time controls. The experimental route still fails closed outside disposable test schemas.

## 360,000-item synthetic COPY comparison

- Job `feed-360k-copy-optimized-20261105-v1`, isolated PostgreSQL 16-alpine container (1.5 GiB RAM / 1.5 CPU), Go scope (2.2 GiB memory / 1.5 CPU), synthetic 961 RSS sources, 360,000 articles. `FEED_SNAPSHOT_COPY_EXPERIMENT=yes`; same service dedup optimization commit ancestry `38b0257`. Ended exit 0 with `TEST_PASSED scale=360000`, test DB container auto-cleaned.
- First full build 70.607s: fetch 25.939s, annotate 31.544s, materialize 2.162s, COPY commit 10.891s (representatives 2.797s; members 7.927s).
- Repeat full build stage timings after writes: fetch 6.082s, annotate 32.932s, materialize 1.818s, COPY commit 9.455s; no explicit aggregate test `SCALE_BUILD` for second build.
- 330,001 representatives, Go RSS HWM 1,284,348 KiB, heap_sys 1,279.6 MiB. Database 823,385,111 bytes after writes; PostgreSQL container memory 253.1 MiB at test end.
- First page p95 13.84ms / deep page p95 27.10ms (25 reads each); 8-source writes p95 107.2ms; same-source writes p95 53.86ms; all stale-read/deferred-rebuild assertions passed.
- Prior GORM batch test (shared server load differed) 142.945s full build, 63.255s DB publish. COPY first build 70.607s / 10.891s. **This is not a controlled A/B**; use a matched-load comparison before promising SLA improvements.

## Remaining production blockers

1. COPY is deliberately enabled only for disposable test schemas and databases. The production-safe transaction policy, configurable rollout flags, scheduling, observability, rollback and backup tests are not yet implemented or approved.
2. Request-path fallback must use a strictly bounded query, bounded CPU time and documented availability behavior at 360k; currently the test-only guard returns 503 above 10k.
3. Run production-shaped concurrent publish/read/GC and fault-injection at scale, then phased shadow rollout. Continue **NO-GO** on production switch until these items are complete.

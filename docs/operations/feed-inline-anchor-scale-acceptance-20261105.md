# Feed inline-anchor optimization acceptance — 2026-11-05

Status: **isolated functionality and 360k synthetic performance PASS; production rollout NO-GO**.
Branch: `perf/feed-inline-anchor-dedup-20261105`, based on `bc3b534`. No main merge, push, deployment, production schema changes or credentials.

## Change
Replaces per-group and per-request temporary anchor maps with an inline 32-key exact set and a hashmap overflow for long/adversarial titles. Preserves anchor semantics, candidate order, URL override, first representative, date window, and `group.matches`. New test covers 1,024 unique keys, duplicates, overflow and reset. The benchmark now uses a deterministic synthetic fixture.

## Controlled small-input ABBA benchmark
Same machine, `GOMAXPROCS=2`, `GOGC=75`, 5,000 items, 6 samples/version:
- Legacy `aec3b7d` vs bitmap `bc3b534`: median **12.18s vs 2.68s** (~4.5x); ~952 MB vs ~73 MB allocated per operation.
- Bitmap vs inline anchors: median **2.15s vs 1.69s** (~22% reduction); ~73 MB vs ~46 MB allocated, ~199k vs ~169k allocations.
- Measurements vary with server load. Do not infer production speedup from an uncontrolled host comparison.

## Disposable PostgreSQL 16 production-shaped test (exit 0)
`FEED_SNAPSHOT_COPY_EXPERIMENT=yes FEED_SNAPSHOT_STAGE_TRACE=yes scripts/test-feed-prodshape-isolated.sh 360000` used 961 synthetic sources, 360,000 items, 1/12 duplicate URLs, long bodies/readers, and auto-cleaned the container. Caps: DB 1.5 GiB and 1.5 CPUs; Go 2.2 GiB and 1.5 CPUs.
- Seed 32.28s; first full rebuild **43.19s** (fetch 7.53s, annotate 23.71s, materialize 2.11s, COPY commit 9.82s); 330,001 representatives.
- Second rebuild **50.13s**, 330,258 representatives; inactive-generation GC **1.96s**.
- Page 1 p95 41.44ms; deep page p95 30.38ms; cross-source write p95 53.01ms; same-source write p95 81.14ms.
- Stale read rejection, deferred rebuild and GC passed. Go peak RSS HWM 1,244,568 KiB (~1.19 GiB).
- Both isolated rebuilds met the nominal 120s Worker budget. Earlier 142.94s on a loaded host cannot be used as a controlled code-change comparison.

## Regression results
- `go test ./internal/service`: PASS.
- Critical RSS HTTP parity, COPY rollback, cancellation, MVCC, multigeneration GC, request gate: PASS.
- `go build -buildvcs=false ./...` and `go vet -buildvcs=false ./internal/modules/feed ./internal/service`: PASS.
- `scripts/test-feed-regression-shards.sh run 25`: **314/314 Feed tests PASS, 13/13 shards**.

## Remaining release blockers
1. Legacy HTTP fallback performs **unbounded synchronous full-corpus RSS scanning and deduplication** on a stale/missing snapshot. A bounded fallback or fail-fast product policy, deadline tests and worst-case measurements are needed.
2. Real multi-node concurrency/fault injection and production-equivalent backup/restore with rollback rehearsal are incomplete.
3. 24h WAL/vacuum/index telemetry, live Worker p99, and approved production read/write feature flags and canary are not in place.

**GO for isolated experimentation; NO-GO for activating the production snapshot path.**

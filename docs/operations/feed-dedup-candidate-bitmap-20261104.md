# Feed duplicate candidate-array optimization (2026-11-04)

Status: isolated performance worktree only. No production merge/deploy or snapshot route enablement.

## Change

`duplicateGroupIndex.firstMatch` now uses per-index, per-annotation reusable candidate IDs plus an epoch-marked `[]uint32` instead of allocating a new `map[int]struct{}` for every article. Duplicate candidate IDs are suppressed, then IDs are still sorted ascending before the unchanged representative `group.matches` check. An epoch wrap clears marks. The index is local to one annotation call, not shared across goroutines. Hot qgram/date bucketing, URL override, matching threshold, and first-representative semantics are unchanged.

## Isolated baseline and verification

- Exact 5,000 item benchmark (synthetic titles `Independent security report <UUID> <index>`): baseline 9.452669458 seconds versus optimized 2.172352651 seconds (single invocation each, shared server load varies). Approx 4.35x faster; not a controlled production latency claim.
- Targeted `internal/service` duplicate indexing, similarity, candidate and representative parity suite passed.
- RSS HTTP parity/fallback/concurrency, old MVCC reader and multi-generation GC tests passed: 64.396 seconds total. GC-only phase 47.80 seconds versus previous unoptimized stage-traced 104.42 seconds on the same shared host. Not a clean apples-to-apples production benchmark.
- 7 generations each of 5,000 RSS items, deleted 6 inactive generations, preserved an old MVCC reader, retained active 5,000 members; WAL delta 40.8 MiB. All test assertions passed with unchanged 120 second deadline.
- Stage `annotate` 7 generations: 4.535 / 2.924 / 2.941 / 3.343 / 2.023 / 2.959 / 2.500 seconds; before optimization 14.667 / 12.537 / 12.636 / 9.871 / 12.556 / 12.984 / 10.790 seconds.

## Risks / release gate

Run larger production-shaped isolated data, adversarial title collisions, full regression, cross-node concurrent rebuild and backup/rollback rehearsal. The legacy full-scan query remains unbounded on fallback. Production snapshot route remains disabled and is NO-GO until these gates are satisfied.

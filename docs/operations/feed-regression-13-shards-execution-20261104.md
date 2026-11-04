# Feed backend 13-shard regression execution — 2026-11-04

## Scope and environment

- Branch: `test/feed-rebuild-http-20261104`; parent commit `f1054f2`.
- `go test ./internal/modules/feed` test enumeration: **314 tests** in 13 shards, 25 per shard except the last shard with 14.
- Each shard ran serially under `FEED_SCALE_ISOLATED=yes`, `GOMAXPROCS=2`, `nice -n 10`, with no scale count or COPY-benchmark opt-in.
- Test database isolation remains enforced by `internal/testdb` (dedicated *_test DB and per-test schema).
- No production migration, data modification, code deployment, main-branch merge, or push occurred.

## Final results

| Shard | Tests | Result | Approx wall time (s) |
|---|---:|---|---:|
| 01 | 25 | PASS on retry | 92 (Go test 85.3) |
| 02 | 25 | PASS | 132 (Go test 112.57) |
| 03 | 25 | PASS | 112 |
| 04 | 25 | PASS | 73 |
| 05 | 25 | PASS | 70 |
| 06 | 25 | PASS | 34 |
| 07 | 25 | PASS | 26 |
| 08 | 25 | PASS | 40 |
| 09 | 25 | PASS | 16 |
| 10 | 25 | PASS | 50 |
| 11 | 25 | PASS | 50 |
| 12 | 25 | PASS | 35 |
| 13 | 14 | PASS | 33 |

**Final: 13/13 shards and 314/314 tests passed when rerun under lower host load.**

## First-run timeout and isolation evidence

Shard 01 originally failed the 140s Go test deadline. Stack was in `TestSnapshotCopyMultiGenerationGCAndWAL`, calling `AnnotateDuplicateFeedItems` / `duplicateGroupIndex.firstMatch` during a 5,000-item multi-generation snapshot rebuild.

To isolate the bottleneck:

- Other 24 tests from shard 01 passed together in 23.467s.
- `TestSnapshotCopyMultiGenerationGCAndWAL` alone passed in 57.33s.
- Full shard 01 then passed on retry, Go test duration 85.300s.

This confirms an **environment/load-sensitive timeout**, with dedup CPU work observable in the failed stack; it does **not** establish that the CPU hot spot is solved or that production performance is acceptable.

## Boundaries / follow-up

Full `go test ./internal/modules/feed -count=1` had previously timed out at 480s on a heavily loaded shared host; the sharded run resolves functional regression coverage, not full-suite single-process throughput. No race-detector, multi-node, 24h WAL/vacuum, full backup/restore, production worker p99, or formal migration/rollback signoff was performed in this execution.

Go/DB test logs were retained locally in `/tmp/atoman-feed-regression-shards-20261104` for this session (ephemeral; not committed). **Production rollout remains NO-GO.**

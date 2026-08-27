# Search Performance Record

## Implementation Record

This change keeps the existing API response shapes and records these measurable query-plan changes:

| Path | Before | After |
| --- | --- | --- |
| Global reference search | One ID query plus one `Resolve` query per returned row | One projection query per target type; comments keep the parent-resolution path |
| Public/explore Feed without duplicate merging | Load a broad or complete candidate set, then filter and paginate in Go | SQL search, visibility, category, language, read state, ordering, candidate pagination, and filtered counts |
| External Feed source exploration without category filter | Read all grouped sources and count through `ListExploreSources(100000, ...)` | SQL title/URL filtering, database pagination, and grouped count |
| Forum topic list search | Plain `LIKE` for the list endpoint | Shared PostgreSQL full-text/trigram search path with the dedicated search endpoint |
| Debate search | Case-sensitive `LIKE` and `ANY(tags)` | Escaped case-insensitive search and PostgreSQL array containment for GIN tags |
| Timeline search | Person name `ILIKE`; events had no keyword filter | Escaped lower-case search for person name/bio and event title/description/content |

The migrations add 27 trigram/tag indexes and 19 B-tree/relationship indexes. The new canonical content indexes cover `content_entries.title`, `content_entries.summary`, and `content_collections.name`; all PostgreSQL index creation uses `CREATE INDEX CONCURRENTLY` where supported by the migration runner.

The search runtime now also:

- Escapes `%`, `_`, and backslashes before every reference-search `LIKE` pattern.
- Orders exact and prefix matches before ordinary substring matches.
- Caches media schema capabilities per connection pool instead of calling `Migrator().HasColumn` on every media request.
- Uses legacy owner columns when the production canonical content schema is incomplete.
- Records per-type and aggregate reference-search duration, result count, failure state, and query length without logging the query text.
- Counts Feed Explore sources in SQL instead of loading up to 100,000 source rows into Go.
- Bounds per-request global-search fan-out at two database workers; external concurrent requests otherwise multiply the fixed fan-out against the connection pool.
- Caps process-wide active global-search queries at eight, preventing concurrent bursts from exhausting the database pool.

## Runtime Measurement

The repository includes a repeatable HTTP benchmark command:

```bash
cd Atoman-Backend
go run ./cmd/benchmark_search \
  -url 'http://localhost:8080/api/v1/references/search?type=user&type=post&type=thread&type=debate&type=feed&type=article&type=artist&type=album&type=song&type=playlist&type=podcast&type=episode&type=video&type=person&type=event&type=channel&type=collection&q=search' \
  -iterations 30 \
  -warmup 5 \
  -concurrency 1 > /tmp/search-after.json
```

Run the same URL and query against the previous deployment and save it as `/tmp/search-before.json`. The command outputs `p50_ms`, `p95_ms`, `p99_ms`, `min_ms`, and `max_ms`.

Calculate the two useful comparisons as:

```text
speedup = before_p95_ms / after_p95_ms
improvement_percent = (before_p95_ms - after_p95_ms) / before_p95_ms * 100
```

Use the same dataset, authentication state, query, page size, warmup count, and concurrency. Run separate measurements for:

- global reference search
- Feed explore with `q`, category, and language filters
- subscribed Feed with `q` and unread-only filters
- music search for song, album, and artist types
- forum topic search

For database-level evidence, capture the matching request SQL with `EXPLAIN (ANALYZE, BUFFERS)` and compare `pg_stat_statements` before and after. Important fields are total execution time, mean time, calls, shared reads, shared hits, rows removed by filter, and whether a trigram Bitmap Index Scan is selected.

## Current Measurement Status

The isolated local PostgreSQL database `atoman_test` is configured through `Atoman-Backend/.env.test.local`. A seeded measurement run used 200,000 canonical blog entries, 5,000 Feed sources, and 99,000 Feed items.

With the original four-worker per-request fan-out, the full 16-type search measured P50/P95/P99 at 112.5/310.4/432.4 ms at concurrency 4. Reducing the per-request fan-out to two workers measured 47.3/119.6/217.4 ms under the same workload and query. Adding an eight-query process-wide cap then measured 39.1/73.8/77.7 ms at concurrency 4, 99.2/203.0/258.6 ms at concurrency 8, and 179.5/218.9/240.6 ms at concurrency 16. Serial P50/P95 improved from 31.4/38.2 ms to 14.5/20.5 ms after cache warm-up. These numbers are local baselines, not production SLO claims.

Representative `EXPLAIN (ANALYZE, BUFFERS)` results on the seeded database selected `idx_content_entries_title_trgm` for canonical blog search; 2,000 matching rows were reduced by a top-N sort in about 20-38 ms. Feed article search selected `idx_feed_items_title_trgm` when the soft-delete predicate was included. `pg_stat_statements` could not be queried because the local PostgreSQL container does not load it through `shared_preload_libraries`.

The remaining performance work is intentionally gated on production-like measurements:

- Validate whether the trigram indexes are selected for short Chinese and ASCII queries at larger row counts.
- Compare the two-worker SearchMany fan-out plus eight-query global cap against database pool saturation at concurrency 1/4/8/16.
- Measure Feed duplicate annotation before moving its fuzzy URL/title matching into SQL; the current duplicate detector uses URL normalization and Levenshtein similarity, so a naive `DISTINCT ON` rewrite would change behavior.
- Resolve the blocked canonical-content migration on an isolated database before removing legacy compatibility paths.
- Introduce a PostgreSQL `search_documents` table only if query plans and `pg_stat_statements` show that the indexed projection queries still miss the agreed latency target.

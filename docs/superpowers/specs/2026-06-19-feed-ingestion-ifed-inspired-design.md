# Feed Ingestion Optimization Inspired by iFeed

## Goal

Improve Atoman's external feed ingestion quality by:

- increasing RSS/Atom parsing compatibility for real-world feeds,
- improving stored summary quality for timeline and list views,
- reducing unnecessary fulltext fetches when the feed already contains usable content.

This design is intentionally scoped to backend internal behavior. It does not change existing API response fields, frontend contracts, or subscription creation semantics.

## Current State

The active ingestion path is centered on `Atoman-Backend/internal/service/rss_cron.go`.

Current behavior:

- fetch the feed URL with a fixed user agent,
- parse the response by trying RSS XML first and Atom XML second,
- extract a narrow set of fields into `ExtRSSItem`,
- store `description` as `summary` with a hard rune truncation,
- mark eligible external RSS items as `full_text_status=pending`.

This works for straightforward feeds but is weak in three areas:

1. Parsing compatibility is narrow.
2. Summary generation is crude and often preserves low-quality HTML fragments.
3. Fulltext fetch is scheduled even when the feed already contains high-quality longform content.

## Reference: What to Borrow From iFeed

The useful part of `iFeed` is not its browser packaging. The relevant lesson is its feedparser-style normalization:

- multiple namespace-aware fallbacks for the same semantic field,
- explicit precedence rules for title, content, author, date, link, image, and enclosure data,
- better handling of RSS vs Atom field differences without branching application logic everywhere.

This design borrows that normalization approach while keeping Atoman's Go-native ingestion pipeline.

## Scope

In scope:

- backend parsing and normalization for external RSS and Atom feeds,
- summary generation used when persisting `FeedItem`,
- fulltext eligibility refinement based on feed-provided content quality,
- backend unit tests for parsing, normalization, summary cleaning, and fulltext policy.

Out of scope:

- frontend rendering changes,
- automatic feed discovery from arbitrary site URLs,
- changing database schema unless implementation reveals a strict need,
- replacing the current fulltext extraction pipeline,
- changing route shapes or API payload contracts.

## Recommended Approach

Use a unified normalization layer inside the backend ingestion flow.

The parser should still fetch raw feed XML and parse RSS/Atom in Go, but all extracted fields should be funneled into one normalized internal representation before persistence. That normalized representation should also carry enough content metadata to decide whether stored summary quality is already sufficient and whether fulltext fetching is still worth scheduling.

This is the best tradeoff because it improves compatibility and content quality without expanding into feed discovery or frontend changes.

## Design

### 1. Unified Normalized Feed Item

Introduce an internal normalized item shape used only inside the feed ingestion service layer.

It should represent:

- title,
- canonical item link,
- unique identifier,
- author,
- published timestamp string or parsed time,
- raw HTML content candidate,
- plain-text summary candidate,
- best image URL,
- enclosure metadata,
- feed source metadata such as source title and cover image when available,
- a signal describing whether the feed content looks like full article text or just a short excerpt.

This normalized structure is not an API model and should stay internal to service code.

### 2. Field Precedence Rules

Adopt explicit precedence rules similar to `iFeed/feedparser`.

#### Content

Use:

1. RSS `content:encoded`
2. Atom `content`
3. RSS/Atom `description` or `summary`

The main goal is to stop treating `description` as the only meaningful content field.

#### Author

Use:

1. item `author`
2. item `dc:creator`
3. Atom author name/email/uri fallback
4. feed title as a final fallback

#### Date

Use the first usable value from:

- `published`,
- `pubDate`,
- `updated`,
- `modified`,
- `issued`,
- `dc:date`.

If all parsing fails, retain the current fallback to ingestion time.

#### Link

For Atom entries, prefer `link rel="alternate"`.
If unavailable, fall back to the first usable link.
For RSS items, continue using the direct item link.

#### Image

Resolve from the best available source in this order:

1. item-level media or iTunes image fields when present,
2. channel/feed image fields,
3. first meaningful image found in content HTML.

Ad and tracker-like images should not be selected when they are obviously unsuitable.

#### Identifier

Keep the existing `GUID -> link` fallback, but centralize it in normalization so duplicate logic disappears from sync paths.

### 3. Summary Generation

Replace direct `description` truncation with a summary builder.

Behavior:

- choose the best available content candidate using the content precedence rules,
- if the candidate is HTML, convert it to clean plain text,
- collapse whitespace and remove obvious boilerplate noise,
- truncate after cleaning instead of before cleaning,
- preserve a stable maximum summary length consistent with current UI expectations.

The summary builder should prefer readability over preserving raw markup.

This improves timeline quality even when fulltext is never fetched.

### 4. Feed Content Quality Signal

Add a lightweight quality heuristic during normalization.

The heuristic should classify whether the feed content is likely:

- full article content,
- medium-quality excerpt,
- short or low-value snippet.

Signals can include:

- plain-text character count,
- paragraph count,
- markup richness,
- whether the content looks like a one-line teaser,
- whether it is mostly boilerplate or embed wrappers.

The heuristic should stay conservative and deterministic. It should not attempt ML-style scoring.

### 5. Fulltext Scheduling Refinement

Keep the current fulltext worker and sanitization pipeline intact.

Adjust the policy that marks a newly ingested item as `pending`:

- if the feed item is ineligible under existing URL or media rules, keep `disabled`,
- if the feed item is eligible but already contains high-quality longform content from the feed, do not eagerly schedule fulltext fetch,
- if the feed item only has excerpt-quality content, keep scheduling fulltext fetch as today.

This reduces unnecessary external fetches and lowers failure noise for feeds that already publish complete content.

Implementation should preserve security checks from `fulltext_policy.go`, especially URL validation and SSRF protections.

### 6. Shared Persistence Path

Refactor `syncAllRSSFeeds` and `SyncSingleRSS` to use the same item normalization and persistence helper.

The two functions currently duplicate:

- identifier selection,
- duplicate checks,
- date fallback,
- author fallback,
- summary truncation,
- `FeedItem` construction,
- fulltext defaulting.

They should share one persistence path so future feed fixes do not drift between bulk sync and immediate sync.

## Error Handling

Parsing should remain resilient:

- malformed or unsupported feeds should fail the current source fetch cleanly without crashing the worker,
- per-item normalization failure should skip that item and continue when safe,
- summary generation failure should degrade to a simpler cleaned text fallback instead of aborting the whole feed,
- parsing logs should stay actionable without leaking excessively noisy raw content.

The implementation should prefer partial ingestion over total failure when the feed itself is mostly valid.

## Testing

Add or extend backend tests for:

- RSS items using `content:encoded`,
- Atom entries with multiple links where `alternate` should win,
- author fallback from `dc:creator`,
- date parsing across multiple common field names,
- image fallback from feed image and content first image,
- summary cleaning from HTML-heavy descriptions,
- long-content heuristic classification,
- fulltext scheduling being suppressed for already-complete feed content,
- shared behavior parity between bulk sync and single-source sync.

Use focused fixture strings rather than broad integration fixtures where possible so failures stay easy to diagnose.

## Risks

### Over-suppressing fulltext

If the heuristic is too aggressive, some feeds that provide incomplete HTML could stop receiving fulltext extraction and regress content quality.

Mitigation:

- keep the heuristic conservative,
- bias toward continuing fulltext when uncertain,
- cover borderline cases in tests.

### Parsing drift across feed variants

Different feeds use inconsistent namespace and content fields.

Mitigation:

- centralize precedence rules in one place,
- add test fixtures for the specific field families being supported,
- avoid scattering fallback logic across sync functions.

### Dirty summary output

HTML-to-text cleaning can accidentally retain navigation text or remove meaningful formatting.

Mitigation:

- use deterministic cleaning,
- test representative HTML snippets,
- keep summary generation separate from fulltext sanitization so behavior is easier to tune.

## Implementation Notes

- Prefer adding small service-layer helpers rather than expanding `FetchAndParseRSS` into a larger monolith.
- Keep `ExtRSSItem` compatibility only if it still serves tests or intermediate parsing; otherwise use a more explicit normalized path internally.
- Avoid database schema changes unless existing `FeedItem` fields prove insufficient for the chosen heuristic.

## Acceptance Criteria

The work is complete when:

- common RSS and Atom namespace variants produce better normalized items,
- stored summaries are cleaner and more representative than raw `description` truncation,
- feeds containing near-complete article content no longer trigger unnecessary fulltext fetch by default,
- existing external API contracts remain unchanged,
- backend tests cover the new normalization and policy behavior.

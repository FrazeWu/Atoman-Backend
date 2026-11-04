# Books: private Work-level note timeline, 2026-11-04

## Shipped in isolated branch

- `GET /api/v1/books/me/works/:workId/notes` returns the requesting user's own **original** revision-level highlights + Work-level private notes in one ordered, server-paginated timeline. Independent notes without source revisions remain usable.
- Supports `kind=all|highlight|chapter_note`, `revision_id` and capped `limit`/`offset`. Output includes exact revision/asset/work IDs and original text anchors for highlights; it never merges/rewrites overlapping marks or upgrades an old file to current best.
- Merge aliases are resolved through existing `bookNoteWorkAliases`, matching private Work-note behavior. Records from removed/old files remain visible, but the source is flagged `can_open=false` if the revision, asset or Work is unavailable, or if file visibility was revoked. Without read permission, filename, format and edition label are redacted.
- New highlights and newly linked independent notes must pass `CanReadBookRevision` before association, preventing users from attaching private files belonging to someone else.
- The Work detail links to `/books/work/:workId/notes`, a lightweight per-Work private timeline with type filters and per-file filtering. Mark deep links use `/books/read/{originalWorkID}/{originalAssetID}/{originalRevisionID}?mark={originalMarkID}`; links are only enabled for readable files. Independent notes are intentionally not given fictional exact text positions.
- No public sharing, public snapshots, nor 30-day mark recovery are newly implemented here. Existing Work-private notes' own recovery behavior remains unchanged.

## Safety / verification

- The endpoint is authenticated, sets `Cache-Control: private, no-store`, checks `user_id` in both UNION branches, and uses bounded pagination.
- Postgres integration tests cover two concrete files, standalone chapter notes, pagination, filtering, isolation, unavailable sources, visibility revocation and source permission enforcement.
- Frontend tests cover exact origin IDs, disabled unavailable links, and type/revision filters.
- No production table reset, file deletion, push or deployment.

## Next milestones

- In-reader standalone chapter notes with exact chapter anchors (currently source revision may be stored but no exact text anchor is guaranteed).
- Work-level collections with ordering and snapshot publication (private/link/public), per-publication redaction/revocation coupling and 30-day trash for annotations.
- Genuine backend+browser+object-storage integration under a separate test environment, plus notes pagination/performance testing with larger records.

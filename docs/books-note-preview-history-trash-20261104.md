# Book-note sharing phase 2 — preview, history, highlight trash

Scope: isolated `feat/books-catalog-integrity-20261103` worktrees only. No
production migration, database reset, push or deployment was performed.

## 30-day highlight trash

- Deleting a private `highlight` first soft-deletes the versioned row. Any
  currently shared collection containing it is revoked by default inside the
  same transaction; an explicit `keep_shared=true` can retain its frozen copy.
- The owner-only `GET /books/me/reading-marks/trash` lists deleted highlights
  with original quote, note, work and restore deadline. Marks are never
  silently remapped to a different ebook revision or edition.
- `POST /books/me/reading-marks/:revisionId/:markId/restore` requires the
  current `base_version`, ownership and 30-day validity. Restore does NOT
  republish a previously revoked collection. Retried restore is idempotent.
- `POST .../erase` additionally requires `confirm_erase=true`. Permanent erase
  scrubs quote, note, label, anchors and styling while retaining the UUID,
  version and deleted timestamp so offline writes cannot resurrect the data.
- A daily processing-worker sweep plus on-demand list/get/restore checks scrub
  expired deleted marks after 30 days. The sweep is scoped only to book marks;
  it does not alter reading state or edition files.

## Permanent erasure of independent notes

- The existing 30-day independent-note trash now has a separate confirmed
  `POST /books/me/private-notes/:noteId/erase` operation. It only accepts an
  already-deleted note, the current version, and `confirm_erase=true`.
- Erasure scrubs the title, body, tags, source revision and exact chapter
  anchor; the anonymized ID/version tombstone remains to reject offline edits.
- The recycle-bin UI uses a second destructive confirmation instead of
  incorrectly retrying the normal soft-delete endpoint.

## Preview before sharing

- Owner sends a complete unsaved, selected collection draft to
  `POST /books/me/note-collections/preview`. The service verifies every
  selected item's owner/work and returns exactly the text that might become
  public, plus a SHA-256 digest. No database write or public link creation.
- The editor makes the owner inspect selected quotes and commentary in an
  explicit confirmation sheet before calling `publish` with the preview
  digest. The server recomputes the frozen content at publish time and rejects
  a mismatch with `books.note_preview_changed`. The client must re-preview.
- Only the owner-visible summary text is included; private file paths,
  precise reader anchors, unrelated highlights, and download links are absent.
  Unlisted link shares add a best-effort `noindex,nofollow` directive; direct
  links can still be copied, cached or independently redistributed.

## Immutable history

- Owner-only `GET /books/me/note-collections/:id/history` and
  `/history/:version` list/read previously published snapshots, even after
  revocation. A history read does not republish content or reactivate a link.
- The public endpoint always accesses the one active version through the
  stable link, or returns 404 after revoke. Only active `public` shares are
  included in discovery; link-only shares are not listed.

## Verification and limits

- Integration tests cover version conflicts, ownership, 30-day expiry,
  permanent erasure, preview immutability/digest mismatch, history permissions,
  server Gin routes and public revoke behavior.
- Headless Chrome smoke tests exercise the actual Vue frontend with a Vite
  dev server and mocked API responses; they are not a substitute for a
  production-backed browser/database/object-store end-to-end run.
- Full backend and frontend regressions must pass before deployment. Broader
  Markdown/JSON/PDF import/export format parity and a live staging deployment
  remain follow-up work.

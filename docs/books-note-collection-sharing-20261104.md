# Work-level private note collections and controlled snapshot sharing

This isolated branch builds on the fixed-revision note timeline. Nothing here
pushes to GitHub, modifies the deployed schema, publishes a real user note, or
erases production data.

## Private collection

- Collections belong to one authenticated user and one canonical Work. The
  private draft consists of a title, description and a manually ordered list of
  up to 40 `chapter_note` / `highlight` references. The exact source IDs remain
  private. Each selected item must be owned by that user and belong to the Work
  (including its historical merge aliases). A mark cannot be repurposed as a
  chapter note or selected if already deleted.
- Draft editing is separate from publishing. Optimistic `base_version` detects
  concurrent modifications. Retrying an unchanged prior draft is idempotent.
  A one-note collection is the single-note-sharing path; a multi-note
  collection supports a curated selection.

## Explicit snapshot publication and three levels

- Default visibility is `private`. `link` produces a 256-bit random URL-secret
  path, accessible to holders, excluded from the public index and search-engine
  indexing. `public` is accessible with the stable link and appears in a public
  index; the client must explicitly confirm publication.
- Publishing takes a new client request UUID, freezes the selected text in a
  separate immutable `book_note_share_snapshots` row, and atomically switches
  the active snapshot. The path is stable across publications. A request retry
  cannot revive a revoked version or re-read changed private content.
- Only the note author's selected quotes (limited to 350 Unicode code points)
  and own commentary (limited to 5000 code points), selected titles and chosen
  chapter labels are in the public JSON. File paths, source revision UUIDs,
  private tags, precise anchors and reading statistics are not public. Longer
  quotes/comments require an explicit private edit before publication rather
  than being silently truncated.
- Public GET only reads frozen snapshot JSON, not underlying private notes.
  Editing a private draft after publication does not change its active share.
  A revoked share returns not found; older immutable snapshot rows remain
  private for audit and history. Only active `public` shares are discoverable.

## Deletion and revocation

- A preflight endpoint tells the owner how many *active* collections include a
  given private note or highlight; deletion UI lists the affected collection
  names. The default is to revoke every affected active sharing link in the
  same DB transaction as source soft-delete.
- The user may explicitly pass `keep_shared=true` to retain an immutable public
  copy despite deleting the private original. This is opt-in, never default.
  Revocation does not delete the owner's private draft or immutable historical
  snapshot rows. Explicit revoke from the collection editor also keeps those.
- Historical shares are not accessible via the stable public route after
  withdrawal, and idempotent publish request retries do not revive them.

## Outstanding / verification

- Full isolated backend DB tests and frontend component tests cover permissions,
  stable links, frozen text, replay protection, re-publication, withdrawal,
  search visibility, and deletion defaults. A full real-browser/server/object-
  storage integration check is still required before deployment.
- Highlights' original annotation 30-day trash/restore and a user-facing
  archive of historical immutable snapshot versions remain separate phases.
- Public previews are limited to selected items; proactively auditing large
  copyright excerpts and search-availability rules remains important.

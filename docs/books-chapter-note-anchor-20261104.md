# Reader-independent chapter notes with exact file locations

This isolated-branch phase adds per-note `chapter_anchor` to `book_private_notes` and retains the existing Work identity, immutable revision ID, tags, 30-day private-note trash, optimistic versioning and private-only behavior.

## Behavior

- An independent private chapter note can be authored from the fixed-revision reader without selecting text; its source is **that exact revision**, not the Edition's changing recommended file.
- EPUB stores an EPUB CFI plus optional source href and human-readable chapter label. PDF stores physical page number and normalized vertical offset inside that page. TXT stores a text offset. Comics store a page number. Labels are descriptions, not evidence that the program detected a semantic chapter boundary.
- Only anchors that match the source file's actual format are accepted. New note anchors require `source_revision_id` and read permission. Invalid page numbers, offsets, cross-format anchors and unwarranted links are rejected.
- Older editors that omit the new optional chapter anchor field do not erase an existing anchor; an explicit clear is available. JSONB values are compared structurally for idempotent retries.
- The private Work notes timeline includes the original chapter anchor and an exact `?chapter_note=` deep link when its source file is accessible; unavailable old files retain private notes but no false open link.
- The fixed-revision cloud reader has a chapter-note editor (title and body), a per-file private note list with re-edit, retry using the same note UUID and request body after network failure, and 409 conflict protection.
- JSON note backup includes the anchor as an optional field, preserving compatibility with earlier schema-version-1 backup files that lacked anchors.

## Tests and outstanding work

- Unit and database tests check create/retry/edit/clear, source format validation, private Work timeline aggregation, JSON backup, reader action emission for EPUB/PDF/TXT, editor retry/conflict and deep-link restoration.
- A reader must not infer missing chapter labels in PDF/TXT; their location labels accurately reflect page or text position.
- Real browser + object-storage integration of the private-note authoring flow remains a separate staging requirement. Multi-note collections, snapshot publication permissions, revocation and annotation trash are separate pending phases.
- This change does not erase production book data, upload media, push branches or deploy.

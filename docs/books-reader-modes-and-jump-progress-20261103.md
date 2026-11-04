# Concrete book reader: mode switching and jump-safe furthest progress

Scope: isolated book-v2 fixed-revision reading path (`BookRevisionReaderView` > `BookCloudReader` > `BookPublicReaderView`) and the corresponding backend cloud reading-state endpoint. The separate old imported-private-book reader is not fully migrated in this phase.

## User behavior

- Opening a specific file resumes the last real position (EPUB CFI, PDF page, TXT UTF-16 offset), independently of Edition's current recommended file.
- Format-aware default: EPUB/PDF/TXT use continuous scroll, comics use paginated. A reader can explicitly switch mode; the choice and reading anchor persist per concrete revision.
- EPUB.js uses the continuous view manager for scrolling and the default paginated manager for paging. On mode change, recreate the rendition at the last CFI rather than resetting to the chapter start.
- TXT can scroll naturally or turn by viewport using wheel/keyboard; continuous is the default. PDF.js handles multiple lazily rendered pages in continuous mode; the original single-page canvas remains available for paging.
- A TOC/bookmark/search jump and initial restoration update the resume location without asserting that the intervening content was read.

## Consistent progress API

- `current_percent` stores the latest real position; `furthest_percent` stores separately observed natural reading progress, and may be **less than current_percent** immediately after a deliberate forward jump.
- `advance_furthest=false` explicitly tags a navigation-only update. Server preserves the previously observed furthest value or a higher independently reported value. Omitted/true values keep legacy natural-reading behavior.
- Client outbox preserves the Boolean flag and the maximum naturally observed point even when multiple updates are coalesced; request IDs and concurrency protections remain unchanged.
- The database check is `0 <= furthest_percent <= 1` independently of `0 <= current_percent <= 1`. The migration replaces only the older check that compared the two columns, leaving stored data intact.

## Remaining cautions

- A PDF's cloud position currently uses exact page number but not an independent within-page scroll offset; add this later with a compatible schema field.
- Full PDF.js/EPUB.js cross-browser interactions, input gesture edge cases, actual files and content storage must still be verified in a running browser. Component mocks and unit tests do not replace that validation.
- Existing earlier imported-private-book rendering routes remain separate; do not claim they have the full new modes until intentionally migrated.
- Local file display mode must be included in all progress saves to avoid restoring the generic pagination default by mistake.

No production data deletion, push or deployment is part of this phase.

# Edition ebook quality evidence (isolated branch)

## Scope

An Edition has exactly one user-confirmed best revision; other verified revisions are historical/alternate ebook files. Changing best never alters a shelf pin, private reading state or notes. No automated ranking by ebook file format or size is allowed.

Quality reports are immutable, versioned, derived after structural validation and the virus scan have succeeded, and stored by **revision ID** in `book_file_qualities`. The Edition files API includes each report only when the caller can read that exact revision. File object keys, bytes and owner IDs remain undisclosed.

## Actual evidence collected (v1)

- All ready revisions: exact uploaded byte size (NOT a quality score), successful virus scan and upload validation.
- EPUB: manifest/spine reading-order entry count and whether a navigation declaration is present. No claim that all chapters are intact or the TOC is usable.
- CBZ/CBR: number of image filenames in the validated archive; image resolution and actual page readability are not assessed.
- TXT: UTF-8 validation is done across the entire file during upload; when at most 2 MiB, count Unicode code points. The count alone does not prove completeness.
- PDF: header and safety scan only. Page count, OCR, text-layer extraction and scan quality are not known.

Quality is information for comparison, not a hidden auto-selection algorithm. The UI shows factual observations next to stated limitations, and requires explicit confirmation when changing the Edition's best revision. Existing revisions with no report display "not yet analyzed" rather than a fabricated score.

## Security & rollout

- Unsupported or suspect uploads remain unready and do not get a ready-file quality report.
- Changes to a file's public/private visibility continue to gate all file listing and content endpoints; the quality evidence cannot be read independently.
- Report persistence and file revision creation share one database transaction and scan lease; a stale worker cannot publish partial reports.
- No production data reset, object deletion, deploy or push is included in this work.

## Next quality improvements

Add bounded PDF text/page checks using a vetted parser, EPUB spine/manifest target verification, comic image dimension/decode checks, content-length similarity sampling, and quality comparison feedback with explicit uncertainty. Use representative real files for manual verification and never treat larger files or a particular format as inherently better.

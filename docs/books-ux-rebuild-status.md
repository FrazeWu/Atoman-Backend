# Books UX rebuild: isolated branch (2026-11-03)

## Confirmed contract

- Work: public searchable metadata, title and author required, automatically generated cover independent of editions; no book file required.
- Edition: new public API creates a private draft; only a file that passes safety scanning activates the Edition.
- Concrete ebook file: stable revision identity. Each Edition recommends exactly one verified file. Older files remain addressable and readable subject to access policy.
- Bookshelf: binds a particular file revision, not Work/Edition. Status and archive are personal flags. Changing the Edition best-file pointer never changes an existing shelf record.
- Files: uploads default to owner-only; public sharing is opt-in with explicit rights confirmation. A public catalog entry is not a permission to download ebook bytes.

## Implemented in backend worktree

- New best-file table, audit events, concrete-file shelf, edition cover object metadata, per-revision visibility, plus migration constraints.
- Draft Edition creation and activation on verified upload.
- Edition file listing, explicit selection, history, shelf and visibility HTTP routes.
- Bounded JPEG/PNG cover endpoint, revision-content access authorization.
- Targeted tests for draft activation, preserved historical files, bookshelf, visibility and revocation.

## Frontend worktree implementation

- Individual cards for concrete files, independent status/progress/archive and direct reader links.
- Edition page showing one recommended file and expandable historic file list.
- Work creation is independent; Edition creation requires choosing an ebook file and cover candidate.
- PDF first five pages, EPUB embedded cover / early images, CBZ/CBR first five images, manual cover upload; book metadata prefills only untouched fields.
- Optional, explicit ebook file sharing. Work cards never inherit edition covers.

## Remaining before release

- Explainable file quality analysis/comparison, not only manual selection.
- External ISBN lookup and stronger OCR extraction.
- Reader verification across formats, jump-safe actual furthest progress.
- Cross-file notes, work-level note collections and exact source deep links.
- Publication snapshots with private/link/public modes and associated revocation, import/export, 30-day trash.
- Multi-client integration and security tests against real object storage and a fresh database.

No production reset, deploy, or push has been performed. Old books-module data may be discarded at rollout, but production PostgreSQL is shared with other modules: never execute a blanket destructive schema reset. Keep the rollback/backup plan scoped to books.

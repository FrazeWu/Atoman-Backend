# Books notes — isolated real-stack acceptance (2026-11-04)

## Outcome

**PASS, with known scope limitations.** A real headless Chrome browser reached
real Atoman Gin handlers, an isolated ephemeral PostgreSQL 16 schema and real
MinIO S3 storage. All **4/4** Playwright scenarios passed. No real account,
production DB schema, public bucket, DNS/Cloudflare configuration, upload
media, or deployed server was modified.

Frontend branch: `feat/books-catalog-integrity-20261103`
Backend branch: `feat/books-catalog-integrity-20261103`

## Isolation and controls

- The QA PostgreSQL and MinIO containers had only `127.0.0.1`-bound ports,
  tmpfs storage, and dedicated test-only users/buckets. They are disposable.
- The Go acceptance test explicitly requires `BOOK_QA_REAL_STACK=1`,
  a PostgreSQL URL on the QA-only port `56543`, a MinIO endpoint at
  `127.0.0.1:19100` and externally supplied **test** credentials.
- `testdb.Open` automatically creates and drops a unique PostgreSQL schema.
  The test creates a synthetic work, editionless TXT resource, two owner-only
  note types, share drafts and snapshots, and a trashed highlight; all test
  objects are stored only in the test buckets.
- Browser authentication is represented by a **test-only** loopback Gin handler
  and synthetic owner/other user headers. It tests the books authorization
  handlers and business logic, but NOT production password login, live cookie
  session rotation, CSRF middleware or SSO.
- MinIO object content is inserted by the Go fixture (real S3 PutObject) and
  fetched through the real backend/read API. The production multipart upload
  workflow is **not** covered by this acceptance suite.

## Executed scenarios (all passed)

1. **Storage + browser renderer + anonymous privacy:** Chrome rendered a TXT
   book from an object fetched from MinIO through real backend APIs. Raw S3
   anonymous object access failed, while an explicitly rights-confirmed
   public revision could be read through the API; default-private access was
   rejected before the fixture opted into public distribution. Unlisted link
   shares displayed frozen excerpts with `noindex,nofollow`, omitted unrelated
   private notes, and remained absent from the public discovery index.
2. **Private draft -> preview -> publish -> revocation:** Real browser edited
   a private curated collection, inspected the server-generated preview,
   published a new immutable snapshot, verified no unrelated private text
   escaped, retrieved author-only history, rejected cross-user access, then
   revoked the link (old public URL returned 404). Historical snapshot stayed
   available to the author only.
3. **30-day highlight trash UX:** Real browser opened the authenticated notes
   page, recovered a deleted highlight from the trash, verified it was once
   again readable from its original revision, and confirmed it disappeared
   from trash. Restore did not republish a previously revoked share.
4. **Existing export/import:** Real JSON API exported standalone private notes,
   a second QA user imported them, and an identical request ID retry was
   idempotent. Real browser downloaded a ZIP containing Markdown notes.

Other existing backend integration/unit tests already cover preview hash
mismatch, optimistic version conflicts, default share revocation, private
note/mark permanent erasure, expiry purging, and ownership isolation; 30
calendar days were **not** actually elapsed in this browser suite.

## Outstanding release acceptance gates

| Area | Status | Follow-up |
| --- | --- | --- |
| Note sharing, access levels and revocation | Passed in isolated QA | Staging HTTPS + real-cookie auth check |
| Private-note and highlight recovery | Passed | Verify scheduled expiry in staging; manual destructive browser QA |
| Real TXT renderer over private S3 bucket | Passed | EPUB/PDF and multipart-upload process using real S3 fixtures |
| Standalone note JSON roundtrip | Passed | Add user-facing JSON import/export management |
| Standalone Markdown ZIP export | Passed | Add Markdown import/preview, conflict detection and mapping |
| Highlights and collections JSON/Markdown export | Not implemented to parity | Include color, anchors, exact ebook identity, snapshots and order |
| Note PDF export | Not implemented | Design export-only output and formatting |
| Real login/cookies/CSRF, HTTPS, CDN and deployed migration | Not tested | Staging-only integration; no production change until explicit approval |

**Decision:** The tested sharing, TXT reading, and highlight restore flows pass
an isolated real-stack acceptance. Full book-notes UX cannot be considered
feature-complete until the remaining import/export and staging security gates
are implemented and independently verified.

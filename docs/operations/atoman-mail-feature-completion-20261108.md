# Mail feature completion work — 2026-11-08

This task explicitly defers live production verification and keeps the production runtime, databases, and remote mailboxes unchanged. It is NOT a full-platform-complete receipt.

## Implemented in isolated feature branches

- Explicit native-to-external IMAP `Seen`/`Unseen` mutation from the unified inbox. Reading a plaintext preview does not update provider flags.
- The write only targets an allowed fixed TLS IMAP provider, the currently authenticated account, the cached UID and UIDVALIDITY, and the `INBOX`. The IMAP server verifies UIDVALIDITY again under the authenticated session before the change, and a subsequent FETCH confirms the flag value.
- The encrypted local snapshot is updated only after remote confirmation, using the account revision as optimistic concurrency guard. A partial provider/local conflict returns an error and requires resynchronization rather than falsely asserting success.
- The existing app-password encryption, user scoping, max-account limit, cached-only metadata and external HTML stripping remain unchanged.
- Added test coverage for cross-user denial, stale UIDVALIDITY, missing UID, rejected provider change, successful read/unread updates and encrypted snapshots.

## Checks

- Backend scoped external tests PASS; `go build ./...` PASS.
- Frontend `bun run type-check` PASS using the main worktree's pre-existing dependency directory (linked into the isolated worktree).

## Still not completed

- Historical resumable migration, continuous bidirectional folder/flag mirroring, provider-specific SMTP Send-as, independent aliases and provider-wide sending authorization. Live on-demand folder browsing and explicit safe MOVE are implemented later in this same task; they do not imply continuous two-way mirroring.
- Persistent server-side follow-up notifications while the user's browser is closed, any model-based mail assistance (requires informed user consent), and final real provider acceptance.
- Backups, recovery, deliverability and production deployment are separate from the implementation work, and are intentionally not claimed here.

## Follow-on feature implementation: live external IMAP folders and safe MOVE

The previous release record above describes the earlier commit. The following changes are new work in the same isolated task branch, not a production deployment.

### API contract (authenticated, owner-scoped)

- `GET /api/v1/mail/external/accounts/:id/folders`: discover selectable remote folders (bounded to at most 200), with name, delimiter and special-use role.
- `GET /api/v1/mail/external/accounts/:id/folder-messages?folder=...&position=0`: live metadata for up to 50 messages per page, with UIDVALIDITY, total, next_position and has_more. This is NOT a historical import and does not save full mailbox bodies.
- `GET /api/v1/mail/external/accounts/:id/messages/:uid?uid_validity=...&folder=...`: live plaintext-only preview in the selected folder. Omitting folder preserves the original encrypted Inbox-snapshot authorization path.
- `POST /api/v1/mail/external/accounts/:id/messages/:uid/seen`: `{uid_validity, seen, folder?}`. The remote server revalidates UIDVALIDITY, performs the exact UID flag mutation and confirms the result; omitting folder retains the original Inbox cached behavior.
- `POST /api/v1/mail/external/accounts/:id/messages/:uid/move`: `{uid_validity, source, destination}`. Returns `{moved, inbox_cache_updated, refresh_recommended}`. A missing MOVE capability fails closed; unlike go-imap's built-in fallback, the operation never uses broad EXPUNGE. The target must be returned by the provider as a selectable folder, the source must match UIDVALIDITY, and the exact UID must still exist. On provider success, an Inbox cache write failure does not misreport the actual MOVE as failed: it returns `refresh_recommended=true`. The UI shows an explicit confirmation before changing the original mailbox.

The external account remains credential-vault-encrypted and bounded by account ownership. No raw provider password is returned to the browser, no arbitrary IMAP endpoint is accepted, and Gmail/iCloud/QQ/163/Yahoo authorization stays opt-in. All new code runs under the existing app account, not as Stalwart admin.

### Limits and future work

Sequence-offset pagination can shift during concurrent provider expunges; UID and UIDVALIDITY remain authoritative for subsequent actions. Live folder browsing does not yet implement durable historical import, cross-folder bidirectional continuous mirroring, provider SMTP Send-as, account aliases or server-side offline reminders. Real external provider compatibility and production acceptance were intentionally deferred on user request.

### Source-only checks for the follow-on phase

- Go external mailbox tests: PASS, including account ownership, remote flags, folder browsing and confirmed MOVE cache changes.
- Go full build: PASS.
- Frontend TypeScript check: PASS.
- Frontend Vite production build: PASS.
- Focused frontend mail unit tests: 12/12 PASS. A stale mail settings test expectation was updated to match the signature field already sent by the client.
- No live IMAP credentials were supplied, no provider mailbox was mutated, no production DB migration, no Pages publish, no service restart.

The branches remain isolated while backend main contains unrelated untracked content and the repositories require a matched feature release.

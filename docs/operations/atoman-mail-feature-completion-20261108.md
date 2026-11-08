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

- All-mailbox external folder browsing, historical resumable migration, synchronized flags/move/delete across folders, verified external SMTP send-as, independent aliases and provider-wide sending authorization.
- Persistent server-side follow-up notifications while the user's browser is closed, any model-based mail assistance (requires informed user consent), and final real provider acceptance.
- Backups, recovery, deliverability and production deployment are separate from the implementation work, and are intentionally not claimed here.

# Atoman Mail V2 — full implementation plan and acceptance gates

Date: 2026-11-06. Owner: Atoman. This plan describes the entire requested scope, not an assertion that all stages have shipped.

## Current verified baseline

- Production Webmail at https://www.atoman.org/mail.
- User-scoped JMAP, encrypted drafts and preferences, single-user mailbox activation, folders, thread reading, restricted HTML rendering, safe attachments, server search, reply/forward, filters/vacation, app passwords, durable five-second undo-send.
- Existing shared frontend UI controls, Atoman auth/session, centralized notifications, Stalwart + private R2 blobs, encrypted mail capsule NAS recovery.
- Known limits: test outgoing mail reached Gmail SPAM although SPF/RSA-DKIM/DMARC passed; full-site NAS bundle not yet verified as restored after transport; Mac native mail through personal Clash proxy not verified.
- Never confuse successful synthetic browser fixtures with production SMTP, Gmail Inbox placement, or a full-site NAS recovery.

## Phase 0: reliability before feature expansion (P0)

0.1 Delivery diagnostics:
- Capture real original outgoing and incoming headers, Message-ID, SMTP response categories, reverse DNS, alignment, TLS, domain/IP reputation signals and Gmail Postmaster Tools if domain ownership and sufficient data are available.
- Compare measured deliverability across providers; isolate software formatting defects from server/domain reputation. No speculative DNS toggles; no personal Gmail spam-label manipulation.
- Acceptance: reproducible multi-provider delivery report with independent header evidence and explicit inbox/spam classifications. Inbox acceptance cannot be promised by code alone.

0.2 NAS and data protection:
- Break down slow full-site encrypted snapshot transfer; implement resumable transport if needed, checksum every completed bundle on NAS, perform full isolated restore and verify app databases, mail capsule, configuration and R2 objects, and separately record delivery timestamp and retention cleanup proof.
- Keep mail-recovery and full-site-recovery status separate, monitor retention/last-success freshness, simulate interruption without deleting last good copies.
- Acceptance: matching SHA-256 for a complete full-site bundle on both sides, sampled and whole restore receipts, actual retention cleanup proof, and alert when stale.

0.3 Operations:
- Alerts for backend, Stalwart, SMTP queue, TLS certificate expiration, disk/quota, jobs, backup corruption/freshness. Route to a verified external channel with dedup/rate limiting and test alert end-to-end.
- Acceptance: induced safe synthetic alert detected, delivered, deduplicated, cleared. No secrets in alerts.

## Phase 1: site-wide mailbox notifications (P1)

1.1 Reuse authenticated mail/me and folders to show server-confirmed Inbox unread count in topbar, mobile entry and reader; react to actual read/bulk/undo operations without optimistic false counts.
1.2 On a newly increased unread count, use the existing PToastHost infrastructure; optional browser Notification only after user permission.
1.3 The first poll must establish baseline with no fake alert; account switch/logout must reset state and discard stale in-flight responses.
1.4 User notification preferences; per-browser permission stays local, account-wide intent may be persisted in encrypted preferences.
1.5 Acceptance: real Inbox receipt updates badge while navigating another module; no cross-user counts or alert after logout; mobile route, reduced motion and denied permission cases.

## Phase 2: contacts and signatures (P1)

2.1 Contact model/API: owner UUID, name, normalized email, optional organization/note, timestamps, unique index per owner/email. Validate addresses; do not expose contacts across users.
2.2 Private contacts UI: create/edit/delete, duplicate detection, search, CSV and vCard import/export with escaped fields, batch limits and overwrite preview.
2.3 Composer To/Cc/Bcc autocomplete. No uncontrolled automatic contact creation from private Bcc. Debounce, keyboard support and send-time address validation.
2.4 Signature in encrypted per-account MailSettings: enabled, text, sanitized HTML, on_reply. Never repeat on reopening saved drafts; append before quoted content; don't overwrite user typing when preferences fetch is delayed.
2.5 Acceptance: per-user access tests, concurrent draft editing, signature survives refresh and multi-device session, malicious HTML stripped, plaintext fallback preserved, no data in browser localStorage except harmless UI choices.

## Phase 3: advanced JMAP search (P1)

3.1 Query representation: mailbox scope (including all), sender, recipient, subject/keywords, start/end RFC3339 timestamps, hasAttachment, isRead, isFlagged, sort and page cursor.
3.2 Server enforces account scope. Translate supported filters to JMAP Email/query; no client-only approximation or false 'no matches' from the first 50 emails.
3.3 Stable pagination, clear filters, persisted local search UI; optional recent searches local to current browser only, no sensitive search logging.
3.4 Acceptance: 200+ mail fixture, mixed mailboxes, same-date ordering, concurrent deletes, security boundary and escaped input.

## Phase 4: scheduled delivery (P2)

4.1 Extend EXISTING durable outbox, never create a second sender. Maintain separate schedule time and 5-second cancel window. Validate UTC and future bounds, display in user's timezone.
4.2 Pending/scheduled UI, change send time by reversioning unsent drafts, cancel/undo. Use immutable idempotent request IDs and worker claim leases.
4.3 Restart, clock skew, timezone DST, submit reconciliation and uncertain outcome all fail closed against duplicate sends.
4.4 Acceptance: synthetic worker clock tests, database restart fixture, one real user-owned end-to-end send with explicit confirmation, no duplicate Message-ID.

## Phase 5: attachment previews, shortcuts and quick replies (P2)

5.1 Owner-scoped attachment retrieval; allowlist image/PDF MIME and byte-size, Content-Disposition protection, sandboxed document viewer, never directly run SVG, JS, HTML or untrusted macros.
5.2 Desktop shortcut mode only when focused outside editable fields (j/k, reply, archive, compose, /). Accessible focus and discoverability, configurable opt-out.
5.3 One-step reply with same draft/revision guard and five-second cancel; preserve reply Message-ID/References and all attachments semantics.
5.4 Acceptance: malicious active file is never executed, oversized content gracefully falls back to download, mobile widths 320/390, keyboard+screen-reader regression.

## Phase 6: multi-mailbox and external migration (P3)

6.1 Distinguish independent Atoman mailboxes, alias identities, and external IMAP accounts; never collapse them into one authorization scope. Define whether sending through an external provider is in-scope separately.
6.2 Ingestion: scoped encrypted per-user external credentials, TLS with certificate verification, read-only source by default, throttling, backpressure, migration progress and safe cancellation.
6.3 Restore mailbox mapping, MIME, flags, date, folders and attachments. UIDVALIDITY/UID + content hash aid dedup; Message-ID alone cannot be trusted as unique. Resume after process restart, no arbitrary overwrite of existing messages.
6.4 Only enable two-way external sync after IMAP conflict policy is documented and integration-tested; don't imply migration means automatic bidirectional synchronization.
6.5 Acceptance: staged 1k/10k mixed folder fixtures, duplicated Message-ID, flaky IMAP network, revoked credential, users A/B isolation, quota limits, no credential logs.

## Shared rollout and test strategy

- Worktree for each domain, codegraph initialized, no changes to unrelated dirty worktrees.
- Contract tests, Go build, frontend typecheck, Vite build, headless browser desktop+real mobile, mailbox permissions, idempotency, escape/HTML injection and negative cases.
- Backfill migrations with a tested rollback and verified backup checkpoint before production schema changes.
- Promote backend API before any frontend requiring it. Use feature gating and controlled migrations; no destructive mail or credential changes without verified restoration.
- Document exact server/runtime commit, frontend commit, source CI, Cloudflare Pages success, monitored health, and remaining production caveats.
- No claim of full completion until all six feature phases plus reliability gates have production receipts.

## Status record (initial 2026-11-06)

- Phase 0: existing mail NAS capsule recovery PASS; full site NAS restore, external alerts, Gmail Inbox placement not complete.
- Phase 1: site-wide badge and toast implementation in isolated frontend worktree; tests and rollout still required.
- Phase 2: signature preferences/sanitization backend and composer/settings implementation in isolated worktrees; contacts not yet implemented.
- Phases 3–6: planning and acceptance gates defined; no production claims.

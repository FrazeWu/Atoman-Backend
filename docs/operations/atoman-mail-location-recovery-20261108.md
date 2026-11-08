# Atoman external folder move, missing-message tombstones and recoverable native trash (2026-11-08)

**Source-only development; not merged or deployed. No live provider acceptance and no production database migration.** This feature is disabled by default and MUST NOT be described as automatic two-way move/delete mirroring. It observes imported source IMAP identities and lets the authenticated owner explicitly resolve missing source UIDs while preserving the original provider mailbox. It never sends provider IMAP MOVE/STORE/EXPUNGE or permanent native JMAP Email/destroy.

## Fundamental identity constraint

An IMAP UID is only unique **inside one folder with its UIDVALIDITY**. Moving a message typically creates a new UID in another folder, and a missing UID may mean MOVE, DELETE, a provider reindex, a spam filter, temporary disappearance or an entire folder rotation. The original imported `(account, folder key, UIDVALIDITY, UID, MIME SHA-256, raw MIME size, JMAP email ID)` receipt is the sole source identity. Message-ID, sender, subject, similar timestamps and subject similarity MUST NOT be used for move/delete inference. SHA-256 equality alone is not proof of moving because duplicate exact MIME messages may legitimately exist in multiple folders.

An owner-requested scan reads at most **8** imported source UIDs per call, with an immutable UID-keyed cursor, a source folder `UIDVALIDITY` check, and no remote writes. An observed absence creates a database **tombstone for review**. If the original UID later reappears, `returned_at` is recorded for audit; unresolved cases change to `source_returned`, but a confirmed Trash action is never automatically undone. A second observation counts only after at least **5 minutes**, so repeated clicks cannot immediately justify an action. A candidate in another *already imported* folder is considered `possible_move` only if it is the sole matching hash + size candidate, its original destination job and UIDVALIDITY are still valid, the exact UID exists and its live source MIME hashes to the same SHA-256. Even then the user must confirm the choice. Ambiguous candidates, invalid UIDVALIDITY, native missing messages, unavailable destination folders and uncertain imports fail closed.

## Separate operator gate, owner scope and explicit actions

The operator must set `ATOMAN_MAIL_LOCATION_REVIEW_ENABLED=1` on a verified staging application instance. The server also requires a working user-owned Stalwart JMAP session, trusted external IMAP credentials, and completed or watching historical imports. The feature is not started by a mail sync cron or by upgrading binaries; no automatic observer or native deletion worker is enabled.

All routes are under authenticated `/api/v1/mail`:

| API | Behavior |
| --- | --- |
| `GET /external/accounts/:id/imports/:job_id/location-cases` | Returns last 50 owner-only tombstones, their status, observation counts and (if uniquely verified) the candidate folder name, but no raw email, secrets or mailbox IDs. |
| `POST /external/accounts/:id/imports/:job_id/location-scan` with `{ "after_uid": 0 }` | Observe next 8 imported source UIDs. `next_source_uid`, `has_more` and `checked_at` enable explicit pagination. Repeating later can advance the observation count. Source folder missing, UIDVALIDITY rotation and incomplete import return non-destructive errors. |
| `POST /external/accounts/:id/imports/:job_id/location-cases/:case_id/action` with `{ "action": "...", "confirmed": true }` | Owner-only, one-case confirmation: `keep_native`, `trash_native`, `resolve_move`, `restore_native`. |
| `GET /external/accounts/:id/imports/:job_id/location-cases/:case_id/actions` | Latest 20 location receipts, status, action and non-sensitive error code. |
| `POST /external/accounts/:id/imports/:job_id/location-cases/:case_id/actions/:attempt_id/reconcile` with `{}` | Only checks the current JMAP location against the encrypted recorded result. **Never reissues** an uncertain Email/set. |

Actions are deliberately narrow:

- **keep_native**: only when the originally imported Atoman copy still exists and the two-way flag writer is paused; keep that copy and persist the user choice, with no JMAP or IMAP changes.
- **trash_native**: after two separated missing-source observations and a fresh recheck, move *only* that exact Atoman imported copy to an **existing** user-owned Stalwart `trash` role mailbox using authenticated, conditional JMAP. Does not change the external provider source.
- **resolve_move**: only for one reverified `possible_move` candidate. Confirm the candidate's imported native copy is still in its own expected imported folder. Keep that new-position copy untouched, then move the *old-position* Atoman duplicate into native Trash. This is a conservative duplicate-resolution choice, not a claim that the source provider itself confirmed a move. Multiple identical candidates cannot be auto-selected. The source mailbox is not modified.
- **restore_native**: only after a successfully applied `trash_native` or `resolve_move`. Reopen the authenticated, encrypted original mailbox snapshot and conditionally restore the same old Atoman JMAP ID from Trash to its previously imported folder. Any later client move, missing original folder or missing target message causes a **non-destructive rejection**.

The frontend requires a separate confirmation checkbox for each mutating choice and clearly differentiates read-only scans/reconciliation from native Trash actions. The meaning of deletion is **recoverable Trash only**. Native Trash retention/automatic expiry belongs to the actual Stalwart operator policy; the source code does not guarantee a hard 30-day lifetime by itself. No purges or permanent deletes are initiated by Atoman's location module.

## Durable receipts and concurrency exclusions

The additive schema includes:

- `mail_external_location_cases`: one globally unique case per import item, owner/account/job, original source UID and UIDVALIDITY, original JMAP email ID, observation times/count, possible candidate item, chosen status and pending action ID.
- `mail_external_location_actions`: one per authorized action, immutable owner/account/case/item binding, encrypted `before`/`after` JMAP mailbox ID maps (sealed with existing user-scoped mail vault), `dispatching | aborted_prewrite | applied | needs_review`, timestamps, and reason code.
- `mail_external_import_items.location_mutation_id`: per-item durable operation lock, acquired atomically with case action ID and operation receipt *before* any JMAP call. Already-running read/star sync and external unlink operations cannot acquire or erase a claimed item. New flag sync is blocked while any location mutation remains pending. Resolved or user-kept missing-UID cases are excluded from subsequent source UID flag-write attempts.

The native adapter calls only user-scoped JMAP `Email/get`, `Mailbox/get`, and `Email/set`. A MOVE requires an exact known origin mailbox, an exact existing target mailbox, an unchanged original JMAP state using `ifInState`, a **mailboxIds-only** update (never arbitrary flags or destroy), a positive target-specific response, and a second read of the same JMAP ID to confirm the new location. It also rechecks source UID presence just before the external-side deletion inference becomes a native move. JMAP errors after dispatch or uncertain DB commit leave the case/action locked in `needs_review`: **no automatic resend**. Definitely rejected pre-write conditions are `aborted_prewrite`, and release the operation lock transactionally. A human-readable action receipt remains for review. A `dispatching` action must be old enough to no longer be in flight before read-only reconciliation is allowed.

## Tested and remaining release gates

Staging fake-provider/backend tests cover two observation intervals, owner scope, no deletion based on one missing UID, exact original-folder recovery, source UID reappearance, duplicate exact-MIME candidates, candidate verification and rejection, JMAP unknown response with persistent lock and read-only reconcile, pre-write abort, pending mutation disconnect protection, flag sync interference and disabled provider writes. Mocked JMAP wire tests verify authenticated user account scope, `ifInState`, mailboxIds-only patching and refusal on changed states.

Before any live enablement, perform a separate disposable-account acceptance against each supported IMAP provider and Stalwart version: UIDVALIDITY rotation, IMAP source deletion and MOVE, identical MIME duplicates, JMAP `Email/set` conflict and timeout, source backup/restore, native Trash expiration and restoration, PostgreSQL concurrent worker races, and audit monitoring. Document the actual configured Stalwart Trash retention policy; do not promise 30 days without a live policy check. Retain backups and the existing application baseline. Full source-folder automatic MOVE/DELETE propagation and provider-specific tombstone reconciliation remain **deliberately out of scope** until reliable cross-folder identifiers and a user-approved conflict policy are available.

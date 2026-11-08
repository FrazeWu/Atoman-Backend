# Atoman two-way read/star synchronization (source-only feature branch, 2026-11-08)

**Not merged, deployed or enabled. Do not call this production-verified.** The original source IMAP account, Stalwart account, database, DNS, current user messages and mail background services are unchanged. This implementation only modifies the development worktrees.

## Independent permission gates

Automatic status writes require **all three**:

1. Existing mail read, authenticated user mail account, external IMAP with validated provider app password, historical import and encrypted vault/R2 access are operational.
2. The backend process and the existing `mail_external_sync_worker` have `ATOMAN_MAIL_TWO_WAY_FLAGS_ENABLED=1` in an operator-approved staging configuration, and `ATOMAN_MAIL_EXTERNAL_IMAP_ENABLED=1` with `ATOMAN_MAIL_HISTORY_IMPORT_ENABLED=1`. The feature does NOT run just because the binary contains the implementation.
3. The mailbox owner explicitly enables the switch for one specific **completed** or **watching** imported folder using the confirmation modal. The consent is recorded as `flag_sync_enabled=true` on that import job. It does not enable other folders, accounts, or owners.

To stop future attempts, the owner disables that job's switch; the server increments a generation to invalidate old workers. A single already-dispatched conditional write can finish. Status flags are **never** automatically coupled to source moves, deletions or permanent expunge.

## Routes (all through authenticated `/api/v1/mail` middleware)

- `POST /external/accounts/:id/imports/:job_id/flag-sync` with `{ "enabled": true | false }`: current owner must own both account and imported folder. Enabled only after initial import is completed; conflicts if any uncertain receipt is pending. Returns the updated job's `flag_sync_enabled`, `flag_sync_generation`, `flag_sync_cursor_uid` and last poll/error fields.
- `GET /external/accounts/:id/imports/:job_id/flag-attempts`: last 40 **owner-scoped** write receipts (source UID, flag, direction, desired boolean, status, error code and timestamp). No credentials or raw MIME.
- `POST /external/accounts/:id/imports/:job_id/flag-attempts/:attempt_id/reconcile` with `{}`: read-only review of **one** uncertain receipt. It never sends IMAP STORE or JMAP Email/set; if and only if both current flags equal the recorded target value and import provenance still matches, it advances the baseline, marks the receipt applied and releases its lock. An active dispatch cannot be reconciled; only a stopped, more-than-20-minute-old dispatch can be escalated into review.
- The earlier `GET /external/accounts/:id/imports/:job_id/flag-preview` remains read-only and now labels an item `sync_needs_review` while an uncertain or in-flight receipt claims it.

## Database additions (additive migration)

- `mail_external_import_jobs`: `flag_sync_enabled`, `flag_sync_generation`, `flag_sync_cursor_uid`, `flag_last_polled_at`, `flag_last_error_code`. Default OFF for every existing/import job.
- `mail_external_import_items`: nullable `flag_mutation_id` is a per-item durable lock, never auto-cleared after an uncertain write.
- `mail_external_flag_attempts`: immutable identity and observed preconditions, source UID/UIDVALIDITY, JMAP ID, direction, flag, desired value, durable `dispatching` -> `applied` or `needs_review` status, timestamps and non-secret reason code. Historical attempts are retained.
- Original `flags_baseline_known` remains false for legacy imported rows without a provable import-time state; they are skipped, not guessed or destructively reimported.

## Synchronization execution and failure semantics

The existing scheduled `mail_external_sync_worker` invokes `RunExternalFlagSyncDue` only behind its operator flag. Each invocation claims at most **2** opted-in jobs, and each job inspects at most **8** imported messages per poll (interval approximately 5 minutes); the cursor wraps after the last receipt. Every source message is resolved by the original `(account, folder, UIDVALIDITY, UID)` and every native message by its user-owned imported JMAP email ID. No Message-ID/subject/date heuristic is allowed.

For each changed boolean flag:

1. Read both sides and compare to last agreed baseline. Two matching flags require no remote write; the baseline can be advanced after successful checks. Older/missing identities stop or skip safely.
2. Persist a `dispatching` intent and atomically set the import item's `flag_mutation_id` **before** contacting either remote provider. A second worker cannot claim that item or flag while the intent exists.
3. Check account ownership, job generation/permission and both current snapshots a second time. Abort without any write when they have changed.
4. For `copy_to_source`, IMAP 993 TLS **requires CONDSTORE** (select with CONDSTORE; fetch exact UID and MODSEQ; UID STORE `UNCHANGEDSINCE` +FLAGS/-FLAGS for exactly `\Seen` or `\Flagged`; verify exact UID flags afterwards). Providers lacking MODSEQ are refused, not downgraded to blind STORE. For `copy_to_native`, user-scoped JMAP `Email/get` verifies the whole expected flag snapshot and acquires `state`; narrow `Email/set` keyword patch includes `ifInState`, and a follow-up Email/get confirms the target bit. It never sends as Stalwart admin.
5. Re-read BOTH stores. Only if both now have the desired target value, atomically mark the attempt applied, update its baseline and release the item lock. A definitely rejected **pre-write** condition (such as missing CONDSTORE or a changed snapshot) is recorded as `aborted_prewrite`, releases its lock and pauses the job, because no STORE/Email/set was submitted. On timeout after submission, unknown result, failed confirmation or receipt commit failure, mark `needs_review`, disable that job and leave its item lock in place. Crash-recovered `dispatching` receipts also block further automatic attempts. **Never auto-retry a remote write whose result might already have been accepted.**

Stopping the source import, disconnecting a provider, or changing job status also gates the flag worker. An external account with an enabled two-way job or any claimed flag receipt **cannot** be disconnected. Provider-specific live writes must be tested with disposable staging IMAP mailboxes; not all IMAP providers support CONDSTORE or app-password SMTP/IMAP in the same way.

## Tests and release acceptance

Focused backend tests cover owner isolation, genuine source->native and native->source state propagation, baseline agreement without writes, feature off by default, repeated scheduler runs, uncertain JMAP replies, unsupported source writes, manual read-only reconciliation, stale dispatch protection, UID/source missing, legacy baseline, job-generation race and disconnect protection. A separate mocked JMAP transport test verifies `ifInState`, narrow keyword patches and rejection without silent resend. Frontend tests require explicit opt-in and cover pause, status, attempts and read-only reconciliation.

**Remaining to do before any live enablement**: disposable provider acceptance of real CONDSTORE and JMAP behavior, staging-only IMAP/JMAP write monitoring, migration/restore rehearsal, transaction-level race tests with concurrent PostgreSQL workers, an operator protocol for receipts that still diverge after manual review, alerting/monitoring, integration with each provider's quirks and eventual folder move/delete conflict and tombstone logic. In production, keep flag gates OFF until these are verified.

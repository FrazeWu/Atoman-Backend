# External-to-native message state difference preview (2026-11-08)

**Source implementation only. Not a two-way flag writer and not production verified.** Developed in an isolated follow-on worktree after native aliases / connected external SMTP Send-as / continuous IMAP history import. No production database migration or mailbox modification was performed.

## Intent and trust boundaries

Before attempting automatic two-way sync of read/star/move/delete states, users need a proven mapping between imported native JMAP messages and their source IMAP messages. Never use Message-ID, subject, sender, or approximate date as a cross-store identity. The earlier import receipts already persist the owner, source account, source folder, UIDVALIDITY, UID, and imported native email ID.

This change adds three **non-secret, additive** properties to each newly imported receipt: `flags_baseline_known`, `baseline_seen`, and `baseline_flagged`. Both values are captured from the exact source IMAP UID when the JMAP Email/import result is committed. Older imported receipts retain `flags_baseline_known=false`, so ambiguous legacy records are never guessed. There is no destructive auto-backfill.

The new endpoint `GET /api/v1/mail/external/accounts/:id/imports/:job_id/flag-preview?position=0` has these guarantees:

- Requires the current authorized owner, the exact account, exact import job, original source folder, matching UIDVALIDITY and native user-scoped `Email/get` access. It uses only the existing authenticated source vault and user JMAP bearer, never an admin bearer.
- Each call is bounded to eight imported receipts ordered by source UID. `next_position` and `has_more` support pagination with an upper bound of 10,000. The source adapter selects the folder read-only and fetches only the exact original UID; the native adapter fetches only `id` and `keywords` for user-owned IDs.
- Each flag is compared to the last known shared import baseline. If source changed and native stayed at baseline, the preview recommends `copy_to_native`. If native changed and source stayed at baseline, it recommends `copy_to_source`. Matching flags are `in_sync`; ambiguous differences fail closed with `conflict`.
- Explicit review states include `baseline_unknown`, `source_missing` (potential move/delete), `native_missing`, source UID identity changes and native errors. Missing email does NOT imply the user requested a delete. No missing record is automatically reimported or deleted.
- Neither endpoint nor UI calls `IMAP STORE`, `Email/set`, SMTP, mailbox MOVE, or permanent deletion. It is an inventory and planning stage only. UI clearly says **只读对比** and displays states without triggering any writes.

## Acceptance and next technical stage

Tests with fake source and native adapters cover user scoping, opposite read-flag changes, flag planner, missing source/native messages, unknown old baselines, UIDVALIDITY rotation, and no write operations. Frontend tests prove only the read API is invoked.

To proceed to **automatic** bidirectional state synchronization, a separate opt-in and durable per-item last-synced state are required; both IMAP and JMAP writes need concurrency guards, retry-ambiguity receipts, manual conflict resolution, provider capability testing, and a dedicated staging dry-run. Folder move/delete would require additional cross-folder UID tracking, tombstones, undo and restore semantics; these are **not implemented** by this preview stage. It must never be described as a full automatic two-way mirror.

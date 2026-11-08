# Atoman Mail: resumable external IMAP historical import (source implementation)

Date: 2026-11-08. Status: **implemented and tested in isolated feature branches; NOT deployed, NOT production-accepted.** The user explicitly deferred production verification. No external mailbox credentials or private MIME were accessed in this implementation phase.

## User flow

1. Sign in to Atoman; connect an external IMAP account using a provider-issued app password.
2. Open **聚合邮箱 → 管理账户 → 文件夹** and select one source folder.
3. Select **导入当前文件夹历史**, review the confirmation, and create an opt-in job. Only that folder is queued. A separate job may be created for each folder; there is no automatic bulk import of every folder.
4. Background worker copies source messages into a dedicated top-level Atoman folder named with a stable, short provider/folder designation and a per-account hash suffix. Original source folders and flags are never modified by this task.
5. Refresh job progress; pause, resume or cancel a job. Cancellation stops future copying and **does not roll back mail already imported**. An ambiguous destination result becomes `needs_review` and is not automatically sent again.

Each job captures source IMAP UIDVALIDITY and the highest UID issued at its creation. Fresh incoming mail is **not** automatically mirrored by this historical job. Jobs are persistent across worker restarts; UID and content hash are used as source identities, not Message-ID alone. Source folder name, provider app password and previews remain encrypted via the existing per-user vault. No message bodies are stored in PostgreSQL; the import item stores the SHA-256 hash, source UID, status and the opaque JMAP import receipt. The raw RFC822 MIME is buffered only in the bounded worker (at most 40 MiB per message) and uploaded to JMAP without being stored in application PostgreSQL.

## Source-only migration

`mail_admin --operation migrate-mail` will add `mail_external_import_jobs` and `mail_external_import_items` through `RunMailWorkspaceMigration`. Each task row is restricted by its owner UUID, source account UUID and hashed folder identity. Each message receipt has a unique index on (owner, external account, folder hash, UIDVALIDITY, UID). Schema changes are additive and require a verified encrypted backup checkpoint before application to production.

Existing generic `mail_*` backup discovery is intended to include both new tables; this **has not been independently confirmed by NAS restore**. Mail-only and full-site NAS recovery remain separate acceptance gates.

## Authenticated API contract

- `GET /api/v1/mail/external/accounts/:id/imports`: current user's jobs for the owned source account; sanitized summary fields only
- `POST /api/v1/mail/external/accounts/:id/imports` body `{ "folder": "INBOX" }`: validate source TLS IMAP and native JMAP ownership, capture UIDVALIDITY/UIDNEXT, queue an idempotence-guarded per-folder import
- `POST /api/v1/mail/external/accounts/:id/imports/:job_id/action` body `{ "action": "pause" | "resume" | "cancel" }`: state-checked transitions; no resume when an item may already have been imported without a receipt

An existing job for the same owner+account+folder cannot be restarted as a new job (even if cancelled or completed); this deliberately avoids accidental replay. Every completed/failed action is checked against owner and job state.

## Background worker

The existing non-root `mail_external_sync_worker` optionally enables migration via `ATOMAN_MAIL_HISTORY_IMPORT_ENABLED=1`, using **user-scoped** JMAP bearer credentials from the existing encrypted credential vault; Stalwart administrator secrets are not inherited. The release unit template opts it in **only when actually deployed**, and the timer template is set to 1 minute after the previous execution. This has not been installed on the live VPS.

Each execution claims at most 2 due jobs and processes at most 40 source messages per job in windows of 200 UIDs. Job leases expire after 10 minutes; a stale lease with `importing`/`uncertain` message rows is marked `needs_review`, never auto-retried. The worker checks the overall process deadline before starting another MIME transfer, and uses provider TLS and private JMAP URLs. Empty UID gaps advance the cursor; each successful import is separately committed. Messages over 40 MiB are recorded as `skipped` instead of being silently truncated. Metadata flags `$seen` and `$flagged`, original MIME and the source internal timestamp are passed to JMAP `Email/import`. Sending email is **not** requested.

If failure occurs before any `Email/import` call, the item can return to `prepared` for safe retry. Once `Email/import` may have been called, only an explicit persisted success receipt advances it; an unknown result remains `uncertain` and blocks automatic replay. A human must reconcile it using the original mailbox and Stalwart before restarting future import work. There is no unsafe automatic forced reconciliation endpoint.

## Source tests and limits

Tests include: per-user source scope, encrypted folder state, one job per folder, safe resumability after a simulated process restart, same Message-ID for different source UIDs, pause/resume/cancel, source UIDVALIDITY change, a pre-import retry, oversize skip, a simulated abandoned in-flight import, actual JMAP protocol fixture for upload/Email/import/keywords/receivedAt, and forbidden cross-account target access. Mail-related frontend tests cover opt-in confirmation, safe status display, and existing folder operations. No real provider mailbox was used.

**Outstanding product scope:** complete provider-to-native 1k/10k fixture benchmarking, provider-specific Gmail/iCloud/QQ/163/Yahoo integration and compatibility, IMAP historical folder state conflicts, continuous bidirectional synchronization, external SMTP Send-as, Atoman aliases, server-side persistent follow-up notifications and separate opt-in AI assistance. Production migrations, true external SMTP/IMAP acceptance, Gmail Inbox placement and full-site NAS restoration remain intentionally unverified.

## Release integration and rollback notes

The backend immutable runtime installer now recognizes optional staged mail worker/admin binaries in addition to the base server binaries. Future release staging must include `start_server`, `music_import_worker`, `mail_outbox_worker`, `mail_maintenance_worker`, `mail_admin` and `mail_external_sync_worker` built from the **same source revision**. Apply only mail-scoped additive migrations under the existing verified backup policy. Release the backend and source-matched frontend together, then install the mail external sync systemd template. Use the previous immutable runtime/rollback manifests on failure; do not roll back or delete populated migration tables automatically. No source branch was pushed or deployed as part of this work.

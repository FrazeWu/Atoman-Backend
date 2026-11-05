# Atoman Mail Stage 9 — server-durable 5-second Undo Send

## Completed in task branch

- Additive, idempotent PostgreSQL migration `RunMailOutboxMigration` adds `mail_pending_sends`. **It is not executed against production by this change.** Other Atoman data models are unaffected.
- Atoman authenticated endpoints: POST `/api/v1/mail/outbox` with `request_id` (UUID), recipient arrays, subject and UTF-8 plain text; GET `/api/v1/mail/outbox/:id` for status; POST `/api/v1/mail/outbox/:id/cancel` to undo before five seconds. There is no public dispatch endpoint. These routes return 503 by default; production does not construct the outbox sender until operational checks pass.
- Only server-generated Message-ID, fixed From derived from an **active** Atoman/Stalwart account, bounded recipients and text. Strict header-injection checks. Bcc is part of the encrypted transport envelope, **never the RFC5322 header**. The complete workspace extension adds authenticated private attachments and allowlisted sanitized HTML; see [workspace API](atoman-mail-workspace-api-20261105.md).
- Envelope, recipients and raw MIME are encrypted with a dedicated AES-256-GCM key, authenticated against Atoman user UUID and queue UUID. Ciphertext stored only in private `atoman-mail-pending` R2. Existing Atoman R2 S3 keys remain unchanged. The DB stores status, object key, request UUID, digest, due time and Stalwart mapping, not message text.
- Server sets `SendAfter = durable object storage completion + 5 seconds`, immediately before DB publish. Cancel uses an atomic PostgreSQL guarded update of status from queued to cancelled, strictly before deadline. Worker uses row locking + `SKIP LOCKED` and updates queued to dispatching. Multiple workers cannot claim the same item. Cancel after the claim or deadline returns 409, never falsely succeeds.
- Requests are idempotent by `(user_id, request_id)` unique index; repeated identical requests return the same queue item. Different content with the same request UUID returns 409. Competing requests use separate random R2 snapshot keys so cleanup cannot delete another user's item.
- After any external send attempt, errors or network timeouts mark `uncertain`, without automatic resubmission. A process crash after `dispatching` leaves a visible item for **manual reconciliation**, not automatic duplicate delivery. No exactly-once SMTP delivery claim is made. Successfully sent items and explicitly cancelled items delete only their own temporary R2 snapshot best-effort.
- A Stalwart JMAP submission adapter verifies the account's encrypted per-user key/session, imports a raw RFC5322 blob, submits with `EmailSubmission/set` and moves the copy into Sent. It never uses a privileged administrator API key to impersonate a sender. Synthetic Stalwart staging tests are required before accepting real delivery.

## Production release gates remain closed

1. Real existing-credential Put/Get/Delete in `atoman-mail-prod` and `atoman-mail-pending`, without reading secrets from process memory or protected configuration files.
2. Paired PostgreSQL + R2 recovery drill, including private blobs and encrypted queue key, verified from TrueNAS offsite copy. The existing backup timer is currently disabled; do not silently enable it.
3. Actual Stalwart service in protected network and authenticated JMAP submission, admin/account reconciliation and monitoring.
4. Existing Cloudflare support@atoman.org Email Routing remains intact until safe cutover; never replace MX without verified fallback and controlled smoke.
5. Only after all gates, configure a dedicated outbox worker with explicit secure secrets and restart/alert supervision. The worker is **not** started or installed by this code change.

## Tests

- `./scripts/mail-invites-test.sh`: temporary PostgreSQL with schema checks, 5-second time-control, cancel-vs-dispatch fencing, idempotency, encryption isolation, sender identity and failure handling.
- `ATOMAN_MAIL_STAGE5_JMAP=1 ATOMAN_MAIL_STAGE9_SUBMIT=1 ./scripts/mail-stalwart-s3-fixture-smoke.sh`: no production mail or Cloudflare secrets, tests local synthetic JMAP submission through disposable Stalwart/PostgreSQL/MinIO.
- `GOMAXPROCS=2 go build -buildvcs=false ./... && GOMAXPROCS=2 make swagger-check`.

## Gated runtime implementation

The application will only construct `MailOutboxService` if **all** of the following are provided by the authenticated server operator after real evidence has been archived:

- `ATOMAN_MAIL_SEND_ENABLED=1` (off by default)
- `ATOMAN_MAIL_R2_RW_VERIFIED=1` (real Cloudflare object read/write/delete proof)
- `ATOMAN_MAIL_TRUENAS_RESTORE_VERIFIED=1` (isolated restore of mail DB and private R2 objects, not just local dump)
- `ATOMAN_MAIL_STALWART_READY=1` (internal service + proper user auth + real send/receive)
- An independent server-only `ATOMAN_MAIL_OUTBOX_ENCRYPTION_KEY_B64` generated and backed up by the operator (not a new Cloudflare token)
- Existing `S3_ENDPOINT`, `S3_REGION`, `S3_BUCKET`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, plus `ATOMAN_MAIL_STALWART_URL`, `ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64`.

When flags or runtime secrets are absent, the API fails closed with 503, and **no SMTP worker starts automatically**. The separate `cmd/mail_outbox_worker` uses only the normal protected process environment and the existing `DATABASE_URL`; it does not parse `.env` files or emit credentials. It polls every 500ms and can run once with `--once --batch 20`. Install a supervised service only after all gates are verified. On an ambiguous upstream attempt the worker exits nonzero for operator review and does not auto-retry any `dispatching`/`uncertain` record.

**Critical:** This release does not provision production mail accounts, run the outbox migration against the production database, enable the outbox, start a worker, write to private production R2, change MX or affect Cloudflare Email Routing for `support@atoman.org`.

## Complete workspace extension (2026-11-05)

The [workspace API and runtime delivery contract](atoman-mail-workspace-api-20261105.md) supersedes the earlier plain-text-only surface. It documents persistent drafts, immutable attachment snapshots, reply headers, folder/search/action endpoints, Sieve settings, client passwords, real provisioning and mail-only migration/maintenance commands. Sent status now has private imported-email/submission receipt evidence, with a non-resending reconciliation endpoint. Native Stalwart retention owns message expiration.

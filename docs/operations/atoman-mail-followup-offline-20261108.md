# Durable browser-independent mail follow-up reminders — 2026-11-08

Status: **feature branch only**; no production database migration, systemd installation, mailbox mutation, notification delivery or live verification was performed. Matches the user's request to complete functionality ahead of acceptance.

## Behavior

- Existing Atoman mail organizer exposes Follow-up time, Clear follow-up, Snooze, and private labels. A follow-up is explicitly created by a logged-in user for one JMAP email ID proven to be owned by that user.
- When the user sets, changes or clears a follow-up, the same PostgreSQL transaction that updates the encrypted `mail_organizer_records` row creates/updates/removes a minimal `mail_followup_schedules` record keyed by the organizer's UUID. The due timestamp is truncated consistently to microsecond precision; updates reset `delivered_at`.
- The new server-side `mail_followup_worker` runs with the encrypted outbox vault, a PostgreSQL connection and no Stalwart administrative credentials or mail transport. A hardened systemd timer unit checks every minute (with jitter). No browser tab is required for the server process to persist the notification.
- Each check handles up to 100 due records and locks the owner organizer record and the schedule before deciding. If cancelled, rescheduled or absent, the stale schedule is deleted/corrected without notification. The notification insert and `delivered_at` update are in the same database transaction, avoiding duplicates across worker retries. The user can explicitly set another follow-up after one has fired; that is a new reminder.
- The native `model.Notification` record has `type=mail_followup`, `source_type=mail_followup`, `source_id=<opaque organizer UUID>`, owner `recipient_id`, and generic meta title/body/path `/mail`. It does **not** store the message subject, body, sender, remote account address, or encrypted follow-up payload. Existing notification preferences/mute/mark-read mechanisms handle it in the system category. The web notification card navigates to the authenticated `/mail` route, ignoring any arbitrary metadata path or source URL.

## Migration of existing reminders

The workspace mail-only migration adds `mail_followup_schedules` and `mail_followup_backfill_state`. On each worker tick, at most 200 legacy encrypted organizer records are scanned in ascending UUID order under a persistent cursor. They are decrypted by the existing owner-scoped vault and non-null follow-up timestamps are inserted idempotently, without overwriting schedules set by current clients. Completion is durable. Existing encrypted metadata remains private in its original table.

The scheduler checks tables exist before working. New Follow-up writes and historical backfill both use the same microsecond-normalized due timestamps to avoid precision-induced false reschedules. Failure to decrypt historical data is surfaced as an error, not silently treated as a successful reminder delivery.

## Source configuration and release

Build source-matched backend binaries including `mail_followup_worker`; the immutable runtime packager now allows that optional binary. Apply additive mail-only migrations only when production backup gating is separately approved. Install `scripts/ops/systemd/atoman-mail-followup.service` and `atoman-mail-followup.timer` only as part of an approved release. A worker requires `ATOMAN_MAIL_FOLLOWUP_ENABLED=1`, `ATOMAN_MAIL_OUTBOX_ENCRYPTION_KEY_B64`, `DATABASE_TYPE`, `DATABASE_URL`. Stalwart admin passwords are explicitly removed from its environment. The task did **not** apply any of these steps to the running VPS.

The browser-side change updates `src/utils/notificationHelpers.ts` to recognize a mail-followup notification, display a generic body and navigate safely to `/mail`. Existing notification pages/popovers already consume the common server notification model.

## Tests and limitations

Tests cover exact-once notification creation, duplicate worker reruns, no early delivery, owner isolation, reminder cancellation/rescheduling, new reminders after a prior reminder fires, and migration of an older ciphertext-only reminder. Frontend helper tests cover fixed route and generic notification presentation. These are isolated tests, not live production notifications.

This delivers a **durable site notification**, not an OS push notification while every Atoman tab is closed. Users see the reminder when they next visit Atoman or through already supported site notification transport. Outbound email reminders, mobile OS push, and sender-specific delivery are not claimed or implemented. Separate pending work includes provider SMTP Send-as and aliases, and authoritative bidirectional IMAP flag/delete/move reconciliation. No production verification was requested yet.

# Atoman Mail workspace API and runtime delivery

This document extends the Stage 9 durable outbox contract with the complete authenticated Webmail workspace. All paths below are relative to `/api/v1/mail`. JSON responses use the existing Atoman `httpx` response envelope. Authentication, active mailbox mapping and private/no-store cache headers apply. Browser requests never choose a Stalwart account, storage key or service credential.

## Reading, search, conversations and folders

| Method and path | Input | Result |
| --- | --- | --- |
| GET `/folders` | None | Existing mailbox list: id, name, parentId, role, totalEmails, unreadEmails |
| POST `/folders` | `{ "name": "Receipts" }` | 201 `{ "id": "..." }`; names 1–100 UTF-8 bytes; at most 100 folders |
| DELETE `/folders/:id` | Folder ID | `{ "deleted": true }`; only an owned custom empty folder without children; system roles forbidden; JMAP state and empty-folder guard remain authoritative |
| GET `/messages` | `mailbox_id`, existing bounded limit | Existing list route, now includes from/to, mailboxIds, keywords, isRead, isStarred |
| GET `/search` | Optional `mailbox_id`, `q` (max 512 bytes), `thread_id`; `position=0`, `limit=50` (1–100) | `{items,total,position,has_more}`; text queries search only the mapped account; thread lookup expands that user's conversation |
| GET `/message` | `mailbox_id`, `message_id` (`email_id` alias) | id, threadId, subject, receivedAt, from, to, cc, replyTo, messageId, plainText, sanitized html, bodyTruncated, hasPlainText, attachments, isRead |
| POST `/messages/read` | Existing `{mailbox_id,message_id}` | Explicitly mark seen after expansion; reading detail alone does not change state |
| GET `/attachment` | `message_id` (`email_id` alias), `part_id` | Actual owned message attachment bytes; attachment disposition, nosniff, restrictive sandbox CSP |
| GET `/raw` | `message_id` (`email_id` alias) | Owned RFC822 source download |

`EmailSummary.mailboxIds` is a JMAP ID-to-boolean map. Use one true ID for the existing scoped detail/read endpoints when displaying a cross-folder search or conversation. Search returns receivedAt and threadId without marking anything read. A supplied thread_id selects conversation expansion; q/mailbox_id filtering is not applied to that explicit conversation.

Message attachment metadata includes `partId`. Download verifies the requested message with the user's credential and chooses the blob only from that message's current attachments. Raw blob IDs and arbitrary download URLs are not accepted. HTML formatting is allowlisted; script, embedded active content, remote images, event attributes and style are stripped. Plain text remains available.

## Persistent drafts and private attachment upload

| Method and path | Input/result |
| --- | --- |
| GET `/drafts` | Latest 100 owned draft summaries `{id,revision,subject,updated_at}` |
| GET `/drafts/:id` | Summary plus `compose` |
| PUT `/drafts/:id` | Client-generated UUID; `{revision,compose}`; revision 0 creates, current revision updates with compare-and-swap; returns incremented revision |
| DELETE `/drafts/:id` | Deletes only the current user's draft |
| POST `/attachments` | Multipart field `file`; max 10 MiB; returns `{id,name,type,size,created_at}` |
| DELETE `/attachments/:id` | Deletes only the current user's uploaded compose attachment |

Compose shape:

```json
{
  "to": ["recipient@example.com"],
  "cc": [],
  "bcc": [],
  "subject": "Subject",
  "text": "Plain text",
  "html": "<p>Rich text</p>",
  "in_reply_to": "original-message-id@example.com",
  "references": ["original-message-id@example.com"],
  "attachments": [{"id": "owned-upload-uuid"}]
}
```

Draft contents are encrypted in PostgreSQL with a per-user/draft authenticated context. Uploaded bytes are encrypted in private mail-pending R2 and referenced by owner-scoped UUIDs. Client-supplied attachment names, sizes and object keys are not trusted; persisted upload metadata is authoritative. Each body form is at most 256 KiB, there are at most 20 attachments and 50 recipients. Draft conflicts return `mail.draft_conflict` (409), with no last-writer data loss. Draft requests have a 1 MiB HTTP JSON bound.

## Durable send, replies and forwards

POST `/outbox` accepts the existing request_id UUID plus to/cc/bcc, subject, text, optional html, in_reply_to, references, attachment_ids (owned upload UUID array), optional draft_id and optional draft_revision. To may be empty: the combined To+Cc+Bcc recipient count must be 1–50. Total attachment bytes must not exceed 25 MiB. Subject is at most 900 bytes; bodies are each at most 256 KiB. Unicode attachment names are MIME encoded. Bcc is retained only in the encrypted transport envelope and never appears in the MIME header.

Replies use the original `messageId` to populate in_reply_to/references; forwarding uses explicit compose body and uploaded attachments. There is no server-side execution of arbitrary HTML or remote fetch of inline images. A submitted attachment snapshot is immutable even if the draft or original upload is later removed.

The result is 202 `{id,status,send_after,created_at,draft_id?}`. The five-second cancellation deadline begins **after** encrypted object storage succeeds, immediately before publishing the DB row. The client must render the server send_after value. `(user_id,request_id)` remains the idempotency key; retries with identical content resolve to the same item and changed content with the same key is a 409.

| Method and path | Behavior |
| --- | --- |
| GET `/outbox` | Latest 100 owned entries from the last 30 days, including draft_id |
| GET `/outbox/:id` | Owner-scoped status |
| POST `/outbox/:id/cancel` | Atomic queued-to-cancelled transition strictly before send_after; 409 after deadline or claim |
| POST `/outbox/:id/reconcile` | At least two minutes after a dispatch claim, query existing Stalwart acceptance evidence; never resubmit |

States: queued, dispatching, cancelled, sent, blocked, uncertain. `sent` means accepted by Stalwart for submission, not proof of final recipient delivery. Imported Email ID and submission receipt are stored as private backend metadata. Interrupted delivery remains dispatching/uncertain until evidence confirms acceptance. Unknown outcomes are never retried automatically. On confirmed acceptance, only the exact draft revision captured when queued is deleted; later edits and cancelled-send drafts survive.

## Message and conversation actions

POST `/messages/actions` accepts `{ids:[...],action,mailbox_id?}` with 1–100 unique scoped Email IDs. Actions: read, unread, star, unstar, archive, trash, junk, move, restore. Restore targets Inbox; move requires an existing owned folder. For a conversation the client expands its thread and submits the explicit IDs. The response is `{updated,failed?,undo_token?}`. Partial upstream success is represented explicitly; undo_token is returned only for a fully successful set.

POST `/messages/actions/undo` accepts `{undo_token}`. Encrypted before/after snapshots expire after five minutes. Undo is owner scoped, one-use and refuses if any current mailbox/keyword state changed since the original action. JMAP ifInState guards the final mutation. This preserves edits from other clients instead of overwriting them.

## Sieve rules, blocked senders and automatic replies

GET/PUT `/settings` uses:

```json
{
  "conversation_mode": true,
  "blocked_senders": ["blocked@example.com"],
  "auto_reply": {"enabled": false,"subject": "Away","text": "Reply text"},
  "rules": [{"id": "client-rule-id","name": "Receipts","enabled": true,"field": "subject","contains": "receipt","action": "move","mailbox_id": "owned-folder-id"}]
}
```

At most 100 blocked addresses and 50 rules. Rule fields: from, to, subject. Actions: archive, trash, junk, move. Enabled rules run in order; the first match files the message and stops. Blocked senders go to Junk before vacation/other rules. Automatic reply and filters share one active managed Sieve script. JMAP compilation/activation must succeed before preferences are stored. Fileinto includes both the RFC9042 mailboxid and fallback mailbox argument. Concurrent changes from the same application user are serialized; a failed confirmation returns 503 and a retry reconciles the managed script.

## Client application passwords

GET `/app-passwords` returns `{id,name,created_at}` records without secrets. POST `/app-passwords` accepts `{name}` and returns `{id,name,created_at,password}` once. DELETE `/app-passwords/:id` revokes it. All x:AppPassword calls use the current user's Stalwart credential and mapped account. The site password is never copied into Stalwart. The disposable real fixture verifies IMAPS login succeeds after creation and is rejected after revocation.

## Provisioning and runtime configuration

POST `/provision` provisions only the authenticated user's existing reservation. POST `/admin/provision` accepts `{user_id}` and requires an active Atoman administrator. Invite redemption and administrator self-reservation attempt real provisioning when enabled. A missing operational gate never marks a reservation active. Partial/uncertain provisioning remains non-active for operator inspection.

Required runtime settings:

- Existing DATABASE_TYPE and DATABASE_URL.
- ATOMAN_MAIL_INVITES_ENABLED=1, ATOMAN_MAIL_READ_ENABLED=1, ATOMAN_MAIL_SEND_ENABLED=1, ATOMAN_MAIL_PROVISION_ENABLED=1.
- ATOMAN_MAIL_STALWART_URL: internal loopback service URL (production operator currently uses http://127.0.0.1:18580).
- ATOMAN_MAIL_STALWART_ADMIN_USER and ATOMAN_MAIL_STALWART_ADMIN_PASSWORD: server-only environment injection; never browser input or logs.
- ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64 and ATOMAN_MAIL_OUTBOX_ENCRYPTION_KEY_B64: separately generated 32-byte base64 secrets.
- Dedicated ATOMAN_MAIL_R2_* settings as documented in Stage 9/R2 operations. Site-wide existing R2 credentials are not reused.
- Verified gates ATOMAN_MAIL_STALWART_READY=1, ATOMAN_MAIL_R2_RW_VERIFIED=1, ATOMAN_MAIL_TRUENAS_RESTORE_VERIFIED=1.
- ATOMAN_MAIL_CLEANUP_ENABLED=1 only for the maintenance process.

Build the CLI and workers from the reviewed revision. `mail_admin --operation migrate-mail` executes only RunMailAccountsMigration (accounts, invitations and encrypted access credentials), RunMailOutboxMigration and RunMailWorkspaceMigration. It does not run the application-wide migration runner and accepts no account targets. Repeating it is idempotent.

`mail_admin --operation status --user <existing-user-uuid>` (or --address) displays only reservation metadata. `mail_admin --operation provision --user <existing-user-uuid> --actor <active-admin-uuid>` performs the guarded provisioning. Never invent a new mailbox if an existing chosen reservation is present.

The outbox worker must run continuously. Run `mail_maintenance_worker` periodically (hourly is sufficient) to remove unreferenced compose uploads older than 30 days, expire action receipts and reconcile bounded stale outbox entries. It never destroys mailbox messages. **Stalwart native retention is the only authority for 30-day Trash/Junk expiry**, covering all clients and avoiding stale application move timestamps. Production runtime activation remains the parent deployment workflow through scripts/ops/activate-runtime.py; this backend branch does not alter a running service.

## Verification

- Isolated PostgreSQL fixture: migration idempotence, owner boundaries, encrypted draft persistence, revision conflicts, attachment immutability, bulk undo conflict, delayed-storage cancellation window, draft send/edit/cancel lifecycle, no application message expunge.
- Real disposable Stalwart v1 + PostgreSQL16 + S3 fixture: process restart persistence, SMTP and IMAP attachment recovery, per-user JMAP mapping, internal self-submission, real Sieve compile and filter delivery, app-password create/IMAP login/list/revoke/rejection, search, raw and owned attachment download, folder guardrails, bulk actions and undo.
- Real durable outbox receipt reconciliation is additionally verified against EmailSubmission/query without retransmission.
- `scripts/mail-invites-test.sh` runs module/API/migration gates on a temporary PostgreSQL16 container. `ATOMAN_MAIL_FIXTURE_SLOT=workspace ATOMAN_MAIL_STAGE5_JMAP=1 ATOMAN_MAIL_STAGE9_SUBMIT=1 bash scripts/mail-stalwart-s3-fixture-smoke.sh` uses a separate disposable loopback fixture namespace.

### Artifact cleanup concurrency

Draft saves and orphan upload retirement take the same PostgreSQL advisory lock per user. Cleanup rechecks all encrypted draft references under that lock and commits a hidden `retired_at` marker before deleting the R2 object. New saves and sends reject retired upload IDs; failed object deletion is safely retried from its retained metadata row. A referenced draft attachment is never retired.

The bounded maintenance worker also retries deletion of encrypted send snapshots older than30days only for terminal `sent`, `cancelled`, or `blocked` rows, recording `snapshot_deleted_at` after success. It preserves every `queued`, `dispatching`, and `uncertain` snapshot. Each pass considers at most200 uploads and200 terminal snapshots. These are private application artifacts; Stalwart exclusively owns message retention.

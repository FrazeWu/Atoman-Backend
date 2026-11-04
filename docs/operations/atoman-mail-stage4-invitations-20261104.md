# Atoman Webmail: stage 4 — invitations and address reservations

## Integration boundary

The mail feature is part of the existing Atoman Backend. It reuses the existing site-session and API-token authentication, CSRF validation, `User.UUID` and admin role permissions. There is **no separate Atoman email website or login system**. Stalwart remains the mail protocol engine; these new Atoman tables store **only invitations and account mappings**, not message bodies or IMAP credentials.

## API contracts

All endpoints are under the existing `/api/v1` path, require the normal Atoman authenticated session or Bearer API token, send `Cache-Control: private, no-store` and use the standard `{"data":...}` response envelope.

| Method | Path | Operation | Result |
|---|---|---|---|
| GET | `/api/v1/mail/me` | Read current user's reservation | `{available:false,status:"not_invited"}` or `{available:false,status:"reserved",address:"...@atoman.org"}` |
| POST | `/api/v1/mail/admin/invites` | Admin/owner create one 7-day invite bound to an existing active Atoman user | 201 with user-targeted address, expiry and **one-time visible** random token |
| POST | `/api/v1/mail/activate` | Invited target user exchanges token | 201 with `status:"reserved"`; **does not create an active Stalwart mailbox** |

Example POST admin body: `{"target_user_id":"<Atoman User UUID>","local_part":"example"}`. Example POST activation body: `{"token":"<one-time-secret>"}`. Invite tokens are not sent as emails automatically. The admin must deliver them through an appropriate authenticated channel in later phases.

**Feature flag:** `ATOMAN_MAIL_INVITES_ENABLED=1` enables invitation creation/reservation; it is **unset by default and must remain unset in production** until R2 permissions, Stalwart provisioning, encrypted backup/restore and mailbox readiness have been verified. Read-only `/mail/me` always reports `available:false` in this stage, including when an address is reserved. Even intentionally enabling the invitation flag cannot activate an SMTP/IMAP mailbox. No live Stalwart user provisioning API is called.

## Database migration

`migrationrunner.Run()` invokes `RunMailAccountsMigration` after existing core schema migrations. The migration creates in Atoman's primary database:

- `mail_invites`: UUID, target Atoman user UUID, issuing admin UUID, address local part, SHA-256 token digest (never plaintext), expiration, redemption/revocation timestamps;
- `mail_accounts`: UUID, immutable local part and email address, Atoman user UUID, lifecycle status, creation/update timestamps.

Unique database indexes prevent duplicate `user_id`, `local_part`, `address` and invite token digest; PostgreSQL enforces valid account statuses (`reserved`, `provisioning`, `active`, `suspended`, `failed`). Only `reserved` is created by the invitation service. The mailbox address does not change if the Atoman profile username changes.

The **separate `atoman_mail` PostgreSQL database** previously created for Stalwart is not modified by this migration. Atoman primary tables are migrated only when the future backend binary is deployed or the migration command is deliberately run. This stage does not apply migrations to production.

## Security validations

- A normal authenticated user cannot issue invitations; a token can be redeemed only by its designated active Atoman account.
- Tokens are 32 cryptographically random bytes, encoded as 43 unpadded base64url characters; only SHA-256 hashes are stored in PostgreSQL.
- Invitations expire after seven days and can be used only once. Redemption occurs in a database transaction with row locking and uniqueness constraints; concurrent use has one winner.
- Local parts are ASCII lowercase-normalized, 3–32 characters, exclude consecutive dots, path separators, and system/reserved names.
- Cookie-based POSTs continue to require Atoman's trusted-Origin + CSRF validation; HTTP Bearer authentication follows existing Atoman API conventions.
- Operations that issue or redeem tokens return 503 unless the invitation feature flag is explicitly enabled; by default no live account can be invited or reserved.
- No outgoing email is sent, no DNS/MX/Exim4/SMTP ports change, no R2 private objects are accessed, no public-media R2 upload client is used.

## Tests and validation

From the Atoman-Backend task worktree, run `./scripts/mail-invites-test.sh`. This starts a dedicated temporary PostgreSQL 16 container on a loopback-only port with freshly generated test credentials, then removes the container. It tests migration idempotency and index constraints, API authentication/CSRF, admin authorization, expiration, one-time redemption, address immutability, reserved names, and simultaneous token redemption. Existing `atoman_prod`/`atoman_dev` and the separate Stalwart database are not touched.

For CI API docs consistency, run `make swagger` and commit changes to `docs/docs.go`, `docs/swagger.json`, and `docs/swagger.yaml`, then check `make swagger-check`. Follow with `go build ./...`, targeted tests and `go vet ./internal/modules/mail`.

## Remaining blockers for activating real mail

1. Authenticated Cloudflare R2 read/write permissions on both private mail buckets, without circumventing existing access restrictions.
2. Full Stalwart JMAP account provisioning, permissions, and IMAP/SMTP app-password security.
3. PostgreSQL+R2 consistent mail backup/restore to TrueNAS; existing Atoman backup timer was disabled at last check, and its default database list did not include `atoman_mail`.
4. Implement encrypted pending-send snapshots, five-second cancellation state machine, unknown-result reconciliation and safe message rendering.
5. End-to-end acceptance and controlled Exim4/MX transition, only after verified delivery and backup.

# Atoman Webmail — Stage 5: Stalwart JMAP adapter and encrypted account mapping

Date: 2026-11-04. This remains an **Atoman backend module**, not a separate application, login system, or Cloudflare project.

## Scope completed

- Verified official Stalwart v1 JMAP management endpoint `POST /jmap/` and used `x:Domain/set`, `x:Domain/query`, `x:Account/set`, and `x:ApiKey/set` in a **disposable Stalwart + PostgreSQL + S3 fixture**.
- Verified a Stalwart user-scoped API key reads its own `/jmap/session`, `Mailbox/get`, `Email/query`, and `Email/get` objects. A local synthetic SMTP message was categorized by anti-spam as Junk Mail; JMAP found the message there without disabling spam checks.
- Built an internal Go `StalwartJMAPClient` with authentication, strict JMAP method/call-id correlation, request and response bounds, TLS/loopback-only endpoint checks, no redirects, and sanitized errors. It supports domain lookup, user creation with **no copied Atoman password**, user API-key creation, compensating key revocation, session, folders, and mail metadata reads.
- Built `MailCredentialVault` using AES-256-GCM and authenticated encryption AAD bound to Atoman user UUID + Stalwart account ID. PostgreSQL stores ciphertext, nonce, key version and key ID, never plaintext Stalwart user keys.
- Added `MailProvisionService` with an explicit `MailActivationGates` readiness struct. Gate fields are `ProvisioningAllowed`, `R2ReadWriteVerified`, and `TrueNASRestoreVerified`, **all must be true**. There is **no production HTTP endpoint that constructs or invokes the provisioner in this stage**. No real mailbox is activated.
- Provisioner uses a DB row lock to claim a reserved mailbox once, creates a normal non-admin Stalwart User, records the remote account ID, creates a per-user API Key, encrypts/persists the key, and only then changes status to `active`. On ambiguous upstream outcomes, the account remains `provisioning` and cannot be blindly retried. A failed persistence attempt triggers a best-effort key revocation; manual reconciliation is still required if remote side effects cannot be proved absent.
- Added read-only Atoman endpoints `GET /api/v1/mail/folders` and `GET /api/v1/mail/messages?mailbox_id=<id>&limit=<1..100>`. They use Atoman's existing site login, CSRF/session infrastructure and user UUID. Each request requires an active local account, correctly encrypted scoped key and matching `JMAPSession.primaryAccounts[urn:ietf:params:jmap:mail]`; callers cannot supply a Stalwart account ID or Bearer key in an HTTP request.
- Existing API `/api/v1/mail/me` reports `available:true` **only** for genuinely `active` accounts (none exist in production at this stage). Reserved accounts remain unavailable.

## Production defaults and enablement boundaries

The existing invitation toggle remains `ATOMAN_MAIL_INVITES_ENABLED`, unset/disabled by default.

New **read-only** environment variables (only used in the Atoman backend process):

- `ATOMAN_MAIL_READ_ENABLED=1` — opt-in; **unset by default**.
- `ATOMAN_MAIL_STALWART_URL` — the server-only JMAP base URL. HTTP is rejected unless the hostname is loopback (`127.0.0.1`, `::1`, or `localhost`). For non-loopback, verified HTTPS is required. Do not expose Stalwart management or user-scoped API keys to browsers.
- `ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64` — server-only, independent random 32-byte AES key in standard base64 form. Do not reuse Atoman session tokens, Cloudflare R2 access keys, or the PostgreSQL password. Preserve this key in the encrypted offsite recovery materials; loss makes existing encrypted user API keys irrecoverable. Rotation still needs a versioned re-encryption workflow before production activation.

**Do not set** the new read toggle or supply the encryption key to the production process until real R2 access, mailbox provisioning, full backup restore, logging, and secret rotation are reviewed. The application fails closed (503) if read config is incomplete or the local user is not active.

The Stalwart admin key is not copied into Atoman sessions or persisted by this module. Any future production provisioner invocation must obtain a protected administrator credential from a server-only secret source, after documented operational approval. The admin key must never be returned to an Atoman browser.

No other production service configuration, DNS/MX, Exim4 listeners, Cloudflare Bucket, or TrueNAS Dataset is changed in this stage. The `atoman_mail` Stalwart database remains empty until the infrastructure release gates are satisfied.

## Database changes (Atoman primary PostgreSQL only)

`RunMailAccountsMigration` adds nullable unique `mail_accounts.stalwart_account_id` and creates `mail_access_credentials` (UUID, Atoman user UUID unique, Stalwart account ID, API key ID, AES-GCM ciphertext, nonce, key version and timestamps). A normal stage 5 application migration on a future backend deploy will add these objects to Atoman's primary database. Stalwart's own PostgreSQL database and mail Blob Store are separate. No migration was applied to production as part of the stage 5 integration work.

## Verification

Run tests from a task worktree:

```sh
./scripts/mail-invites-test.sh
ATOMAN_MAIL_STAGE5_JMAP=1 ./scripts/mail-stalwart-s3-fixture-smoke.sh
go test ./internal/modules/mail -count=1
go build ./...
make swagger-check
```

The first script launches a disposable PostgreSQL fixture for token replay, account lifecycle and owner-scope tests. The second launches disposable PostgreSQL + MinIO + Stalwart fixtures and executes a real management/API-key/JMAP read test. All disposable credentials, objects, databases, containers, networks and volumes are removed. The S3 fixture is not Cloudflare R2; it does **not** prove the existing Atoman R2 key may access the two private mail Buckets.

The tests cover gate rejection, administrator authorization, AES-GCM tampering and cross-user decryption failures, one-way account state transitions, no blind retry on ambiguous remote outcomes, refusal of forged mailbox IDs, and Stalwart session-account mismatch.

## Remaining blockers

1. Actual Cloudflare R2 authorization and private test-object read/write using existing Atoman credentials in an explicitly permitted environment. Previous runtime credential-access operations were restricted; this stage did not bypass that restriction.
2. Verified PostgreSQL + R2 consistent mailbox backup and TrueNAS offsite restore; Atoman's backup timer was disabled and the current backup service only included `atoman_prod` and `atoman_dev` at last inspection.
3. Stalwart production domain/listener/DKIM/SPF/DMARC/PTR and secure management setup, plus port 25 Exim4 cutover plan. Do not change MX until external delivery and restoration are tested.
4. Server-side vault encryption key lifecycle and rotation, reconciliation for orphaned Stalwart users/keys, full audit log and account suspension/revocation policy.
5. Scoped IMAP/SMTP app passwords, richer JMAP message detail and attachment access, 5-second undo-send queue, Sieve rules, search, notifications, and frontend UX.

Authoritative Stalwart 1.x API docs:
- https://stalw.art/docs/auth/accounts/manage/
- https://stalw.art/docs/ref/object/account/
- https://stalw.art/docs/ref/object/api-key/
- https://stalw.art/docs/http/jmap/

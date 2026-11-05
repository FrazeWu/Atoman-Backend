# Atoman Mail — production delivery plan and release gates

Date: 2026-11-05. Owner: Atoman. This is a **module of the existing Atoman application**, not a separate service login or website. The Stalwart + PostgreSQL 16 + Cloudflare R2 design is retained. Dedicated mail R2 credentials isolate the two private mail buckets from the existing public media bucket.

## Non-negotiable requirements

- A user chooses an available `local-part@atoman.org` address, permanently bound to the existing Atoman user UUID. Admin may reserve without a token; admin generates 7-day single-use targeted invitations for non-admin users. No public signup, shared credentials or cross-account read.
- Stalwart v1 owns actual mail accounts, mailboxes, JMAP/IMAP/SMTP submission, Sieve and mailbox content. Atoman backend stores account mappings, encrypted per-user JMAP API key and durable pending-send metadata. Use the same Postgres 16 **instance** but a dedicated `atoman_mail` database for Stalwart; Atoman production DB is independent.
- Stalwart blob/attachment objects go to private `atoman-mail-prod`; encrypted pending-send snapshots go to private `atoman-mail-pending`. Use the existing S3_ENDPOINT with dedicated ATOMAN_MAIL_R2_ACCESS_KEY_ID and ATOMAN_MAIL_R2_SECRET_ACCESS_KEY; do not change the public atoman media bucket, ACL, access methods or credentials. No browser exposure of R2/JMAP admin credentials.
- Webmail desktop three panes, mobile one pane, conversation mode by default, optional single mail view, bulk actions, archive, trash, undo and 30-day trash/junk retention; only expanded messages become read. Sieve rules include filters, blocking, auto-response.
- Compose must support autosaved drafts, reply/reply-all/forward, attachments, accessible plain text and sanitized HTML; **5-second server-persistent undo send** (atomic cancel-vs-dispatch, idempotent external send and crash recovery). Do not claim Undo works if only the browser delays submission.
- Invite-only accounts initially; IMAP/SMTP app-specific credentials must be revocable and never copy the Atoman website password into Stalwart.

## Milestones and done conditions

| Gate | Implementation work | Proof before production activation |
| --- | --- | --- |
| G0 Existing infrastructure | Postgres database and role, 2 private mail R2 buckets, admin invitation, encrypted account mapping | Live names/permissions confirmed; no public mail objects; existing support forwarding preserved |
| G1 Storage | Existing server credentials Head/Put/Get/Delete in both private R2 buckets; strict object isolation; no auto-create, ACL | Disposable known-prefix synthetic object test; no credential logging; explicit allowlist and cleanup |
| G2 Stalwart provisioning | Dedicated Stalwart DB, service-only admin auth, user/account/API key lifecycle, idempotent reconcile; no public listeners | Local SMTP→R2→Postgres→JMAP/IMAPS with attachment + restart recovery |
| G3 Webmail reading | Folders, pagination, plaintext safe detail, recipients, attachments through authenticated proxy, expand-only read marking, thread grouping, navigation/search | Unit and real Stalwart JMAP security tests, mobile/desktop tests, no unsafe HTML |
| G4 Drafts and sending | Draft persistence, compose, reply/forward, SMTP app identity, attachment import, 5-second durable undo queue, JMAP submission worker | Concurrent cancel/dispatch tests, crash recovery, no duplicated delivery, no secret/R2 exposures |
| G5 Operations | Continuous backup Stalwart + Atoman PG and both private buckets, encryption, retention, monitoring, alerts, recovery and TrueNAS copy | Full paired restoration into isolated stack, integrity and sample mailbox/attachment inspection |
| G6 Deliverability and cutover | TLS cert, SPF/DKIM/DMARC/PTR, MTA-STS or DANE as appropriate, secure IMAP/SMTP listeners, rate limits and spam, Cloudflare DNS | External delivery both ways, spam/misdelivery tests, HTTPS and IMAPS, support@atoman.org forwarding continuity, rollback DNS TTL plan |
| G7 Rollout | Admin internal mailbox canary, invitation feature on, rollout to invited accounts, UI feature flags | Verified send/receive/undo/autosave/recovery and production health; user-visible `active` only when all prior gates hold |
| G8 Maintenance | Sieve, audit, token rotation, retention workers, quotas, DKIM renewal, incident response and regression suite | Scheduled tests/backups with alarms, documented restore and key rotation |

## Explicit production safeguards

- Existing `support@atoman.org` Cloudflare Email Routing currently forwards to Gmail; **do not change the existing MX or disable Email Routing** until a safe forward or replacement has been tested. A domain-wide cutover has high blast radius.
- Invites and real mail reading default OFF. `reserved` must remain distinct from `active`; no provisioner is callable over public HTTP until storage + backup gates have been attested by operator-controlled configuration.
- Existing backup timer may be disabled. Do not silently enable it or claim that offsite/TrueNAS recovery worked from a local dump alone. Require fresh test results and a rollback snapshot prior to migration/upgrade.
- All source changes use separate task Git worktrees with CodeGraph initialization, then test, reviewed integration, ordinary push to GitHub; never overwrite another task's uncommitted work.

## Current implementation focus

G3: read single full plaintext email with sender/recipient + attachment metadata and perform `$seen` mutation **only after actual message expansion**, using a separate authenticated POST endpoint. JMAP `Email/get` must use bounded `fetchTextBodyValues` and `maxBodyValueBytes`, and text is rendered escaped rather than injecting HTML. The user-scoped JMAP credential remains on the backend. Stalwart fixture tests must be executed before merging.

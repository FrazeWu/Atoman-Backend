# Atoman production mail runtime

This directory is the reproducible runtime configuration for the existing
Atoman mail module. The mail module uses the site's normal authentication and
is available at https://www.atoman.org/mail. Its provisioning and sending
flags must remain disabled until the paired offsite recovery gate passes.

## Services and boundaries

- Stalwart is pinned by image digest. It runs as UID/GID 2000 in Docker, using
  the existing PostgreSQL instance but a dedicated atoman_mail database and
  role. Its raw-message blobs are in private atoman-mail-prod.
- The backend and durable outbox worker run as the unprivileged atoman user.
  Encrypted drafts, uploads and pending snapshots use private
  atoman-mail-pending. Dedicated mail credentials do not replace public media
  storage credentials.
- JMAP and administration bind only to host loopback port 18580. The backend
  sends requests with the mailbox owner's encrypted credential.
- Public mail ports are 25 (server delivery with STARTTLS), 465 (implicit
  TLS submission), 587 (STARTTLS submission), 993 (IMAPS) and 4190
  (ManageSieve). Submission requires authentication and a matching sender.
- mail.atoman.org redirects web requests to the existing Atoman mail module.
  Public nginx does not proxy the JMAP administration surface.
- The certificate covers mail.atoman.org and mta-sts.atoman.org. The
  renewal hook imports the renewed key and certificate into Stalwart before
  restarting its container and reloading nginx.

## Protected configuration

All files below are private and must never be committed or included in logs.

| Path | Purpose |
| --- | --- |
| /etc/atoman/backend.env | Existing application environment |
| /etc/atoman-mail/backend.env | Mail gates, private R2 keys and encryption keys |
| /etc/atoman-mail/postgres.env | Dedicated mail PostgreSQL password |
| /etc/atoman-mail/stalwart.env | Mail server storage environment |
| /etc/atoman-mail/stalwart-admin.json | Service provisioning administrator |
| /etc/atoman-mail/stalwart-config/config.json | Stalwart bootstrap store references |
| /var/lib/atoman-mail | Private operational evidence and local runtime state |

The existing runtime activator resets EnvironmentFile in the 90-managed-runtime
drop-in. Install 99-mail.conf after it so the mail environment remains loaded.
systemd reads protected EnvironmentFile files before dropping privileges.

cmd/mail_prepare parses dotenv without invoking the shell. With
--verify-objects it creates only disposable known-prefix objects in both
private buckets, verifies byte integrity, then deletes those objects.
--write-config additionally requires root and refuses to overwrite existing
keys. It prepares configuration with activation gates closed.

scripts/mail-stalwart-admin.py bootstraps once, imports certificates and
inspects selected non-secret fields. It must run locally as root. Recovery
bootstrap credentials are not attached to the running production container.

## Release order

1. Complete backend, UI and real Stalwart tests; create the paired database,
   private-object and runtime-configuration backup.
2. Verify the encrypted backup after copying it back from the offsite
   destination, including a message body and attachment. Preserve this
   recovery evidence before activating mail features.
3. Apply only the mail migrations. Build the server, music worker, mail
   outbox worker, mail maintenance worker and administration utility from
   the same reviewed commit.
4. Install immutable backend binaries with scripts/ops/activate-runtime.py.
   Preserve the existing application environment and load 99-mail.conf.
   Install the two mail workers from that same release, then start the
   supervised sender and maintenance timer.
5. Provision the existing user's reserved address. Confirm identity,
   folders, draft persistence, attachment authorization, undo send,
   persistent queue recovery, and accepted submission without duplication.
6. Cut over MX only after incoming delivery and the existing support
   forwarding have been exercised. Retain the prior DNS snapshot and
   forwarding rule during the transition.
7. Push the reviewed frontend main branch and verify Cloudflare Pages
   production built that exact revision. Verify the public mail route
   and API readiness.

## Operator commands

The three Go scripts under scripts/mail-*.go use explicit-file builds; their
ignore build tags keep them out of ordinary server binaries.

- mail-activate-environment.go validates the dated NAS recovery receipt, the
  exact reviewed snapshot and ciphertext hash, the original canary attachment
  hash, and the live local Stalwart administrator session. Without --apply it
  only verifies. Applying preserves a private pre-activation environment and
  atomically enables mail gates; it never prints credentials.
- mail-env-exec.go loads both protected dotenv files without shell evaluation
  and runs the explicitly supplied local operator command, such as mail_admin
  --operation migrate-mail or provision.
- mail-production-acceptance.go requires the exact existing administrator UUID,
  mailbox address and --confirm-self-send. It verifies draft revisions, a
  cancelled send, one delivered synthetic self-message and attachment, explicit
  read state, bulk undo, and IMAPS password revocation. A private receipt
  prevents blind repetition after an incomplete run.

## Retention and failure handling

Stalwart's DataRetention singleton is verified to keep Trash/Junk for
2592000000 milliseconds (30 days), with daily expiry at 00:00. This covers
moves made by IMAP clients as well as Webmail. Blob cleanup runs daily at
04:00 after no message references an object.

The outbox waits five seconds on the server. Cancellation and dispatch race
through an atomic database transition. An interrupted external submission
is reconciled against stored acceptance evidence; an unknown outcome must
never trigger an automatic duplicate send. The UI distinguishes accepted
submission from delivery to the recipient.

Backups must contain application mappings, Stalwart data, both private
buckets and encryption/configuration keys from a consistent boundary.
Recovery keys stay on the owner's recovery machine, not on the VPS.
Consult the mail operations runbook for the installed timers and recovery
commands.

## Rollback

Runtime activation retains the previous binaries, unit overrides and
application environment, and rolls back automatically if its health check
fails. The mail schema changes are additive; retain the new tables if
rolling back binaries. Stop the outbox worker before any manual runtime
rollback, and inspect queued/dispatching records before restarting it.

The pre-cutover DNS snapshot is stored privately at
/var/lib/atoman-mail/dns-precutover-20261105.json. Restore only the affected
apex MX records and mail routing configuration, preserving unrelated
Resend, website and storage records. Do not erase mail data or roll back
to an older database snapshot after real mail has been accepted. Once mail
has arrived, retain both systems and reconcile delivery before reverting
the MX destination.

## Verified transport observations, 2026-11-05

Authenticated external delivery and the replacement support forward both
reached the owner's Gmail inbox. Received headers reported SPF, RSA DKIM
and DMARC passing, over TLS 1.3. The configured outbound EHLO matches the
provider's forward-confirmed reverse hostname
vmi3410530.contaboserver.net. The customer-facing mail host is
mail.atoman.org.

A single inbox test does not establish reputation with every receiving
provider. Retain delivery monitoring and conservative invitation rollout.

Reference: https://stalw.art/docs/storage/maintenance/
Reference: https://stalw.art/docs/mta/inbound/auth/

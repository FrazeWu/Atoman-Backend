# Atoman Webmail — stage 3: dedicated PostgreSQL database, 2026-11-04

Atoman Webmail is a built-in module in the existing Atoman project. It uses the existing PostgreSQL 16 cluster and Atoman site authentication. No independent mail product or new Cloudflare account is created.

## Applied to the existing PostgreSQL instance

The shared PostgreSQL server runs in Docker container `atoman-dev-postgres-1`, which also hosts `atoman_prod` and `atoman_dev`. Its data directory is backed by the existing persistent Docker volume.

Created:

- Database `atoman_mail`, owned by `atoman_mail`.
- Login role `atoman_mail`, explicitly without superuser, createdb, createrole or replication rights.
- Private randomly generated database password, stored only on the VPS in `/etc/atoman-mail/postgres.env` (root-owned, mode 0600). No password is committed to Git, logged, or transmitted in chat.
- The email role can authenticate to its database and create schema objects in `public`, as required by Stalwart 1.x. It has no PostgreSQL cluster administration rights.
- The existing Docker bridge network `atoman-dev_default` exposes the database hostname `atoman-dev-postgres-1` to other containers on the same network; a password-free readiness probe from a temporary PostgreSQL 16 client succeeded.
- Both original Atoman databases and all existing PostgreSQL tables remain unchanged. The mail DB is deliberately **empty (0 public tables)** until the production Cloudflare R2 and backup pairing are approved.

## Validation performed

1. `scripts/test-mail-postgres-provision.sh`: generated fixture role/database with a fresh container, verified idempotent setup, minimal privileges, root-only credential file, and cleaned test resources.
2. `scripts/provision-atoman-mail-postgres.py --apply`: created the real empty mail database and restricted role, then `scripts/provision-atoman-mail-postgres.py` revalidated them without writing anything.
3. `scripts/test-atoman-mail-db-backup.sh`: created a synthetic temporary table in the empty mail DB, invoked Atoman's existing `backup-postgres.py` with `--verify`, restored it into a separate PostgreSQL container, confirmed exactly one test row, then removed the temporary table and local test backup artifacts.
4. After the tests, the mail database again contained 0 tables. No real mailbox, message content or attachment was created.

Test outcomes included `MAIL_POSTGRES_CREATED_AND_VALIDATED`, `MAIL_PG_PROVISION_FIXTURE_OK`, `MAIL_PG_BACKUP_AND_ISOLATED_RESTORE_VERIFIED`, and PostgreSQL reports `restore_verified: true`.

## Re-run and operational safety

Read-only state/credential validation:

```sh
python3 scripts/provision-atoman-mail-postgres.py
```

Isolated provisioning fixture (does not access real DB):

```sh
./scripts/test-mail-postgres-provision.sh
```

The mail-only backup/restore acceptance test requires `atoman_mail` to remain empty; it refuses to run against a mail DB with existing tables. It creates and deletes only its own synthetic table and temporary backup directories. Do not run it after real mail has been activated.

Do **not** run `--apply` with another database role/namespace, manually open the password file, share the credential, or connect the Stalwart container to production without completing the R2 validation and backup gates.

## Backup risk — release blocker

At the time of verification:

- `atoman-backup.timer` is **disabled**. It must not be described as a functioning daily backup schedule.
- `atoman-backup.service` targets only `atoman_prod` and `atoman_dev`; it **does not include `atoman_mail`**.
- On 2026-11-02, one backup execution failed with `Not enough disk for backup and isolated recovery`; a later manual run successfully restored the existing databases but took significant disk and memory resources.
- The mail-only synthetic PostgreSQL dump/restore is successful, but it is **not a paired PostgreSQL + Cloudflare R2 snapshot** and is not an offsite TrueNAS mailbox restore.
- Do not enable real mail accounts or send/receive data until scheduled database-plus-blob backups are configured, resource headroom is verified, and a complete TrueNAS restore has passed.

The previous normal backup pipeline encrypts completed PostgreSQL exports and makes them available for the existing NAS pull mechanism; it still needs an explicit mail-aware design so that any mail database snapshot is consistent with the external R2 blob objects.

## Remaining stage gates

1. Obtain authorized access to the existing Atoman R2 credentials and confirm object-level permissions on `atoman-mail-prod` and `atoman-mail-pending`. Previously denied credential tool paths must not be bypassed.
2. Configure Stalwart against the dedicated `atoman_mail` PostgreSQL database and Cloudflare R2; retain its `config.json` in a persistent, root-protected mount and avoid public listeners in staging.
3. Implement and test Atoman site-session JMAP adapter and invitation-to-account provisioning.
4. Implement the server-side 5-second undo-send queue and secure immutable pending-message snapshots.
5. Add mail-aware consistent backup / TrueNAS restore, resolve disabled backup scheduling and disk headroom, then validate real external mail delivery before any MX or Exim4 change.

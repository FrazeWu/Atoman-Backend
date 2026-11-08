# External IMAP incremental import — source implementation (2026-11-08)

**Deployment state:** development branch only; not merged, not deployed, not production validated. The user asked to complete features before production acceptance. Requires the prior source branches containing external folder browsing, safe MOVE and resumable history import.

## Scope and guarantees

Each opted-in user can copy historical messages from a single owned external IMAP folder to an isolated native Stalwart folder. The new `continuous` boolean makes that *same folder* continue watching for newly issued UIDs after the initial import finishes. No other folder is implicitly authorized. The source IMAP account is accessed using the encrypted provider app password; target JMAP import uses the same user's encrypted bearer credential. Neither credentials nor MIME pass through the browser.

- `continuous=false` (default): one-shot history import, existing behavior unchanged.
- `continuous=true`: initial bounded import then `watching`; the non-root worker polls the source UIDNEXT/UIDVALIDITY at most every five minutes per job, expands the persisted `upper_uid` only if the identity remains valid, then copies only new UIDs. No automatic replay of previously imported UIDs.
- Both the one-shot cursor and the continuous high-water cursor are in PostgreSQL. Source UIDVALIDITY changes, unexpected UID regression, and an ambiguous JMAP import result all stop copying and require human reconciliation (`needs_review`).
- `pause`/`resume` retain the existing historical progress. `enable_live` converts a completed job into a watcher without recopying history. `disable_live` stops a watcher and leaves its imported messages unchanged. Disabling during an in-flight import conservatively pauses the current job.
- A running or watching job prevents disconnecting its provider account, so its remote credentials cannot silently vanish during a background copy.
- Bounded worker: maximum 2 claimed jobs per invocation, 40 new emails per job, no more than 40 MiB per email. The systemd unit template was previously configured to run the worker periodically; it is not installed by this feature branch.

**Important exclusions:** this feature is *incremental inbound copying*, not yet an authoritative two-way mirror. Existing native-side read/star flag changes are not automatically sent to providers; external-side flag changes to *old* messages are not automatically propagated into already-imported native copies. Remote moves and deletions are never replayed as native deletions, nor vice versa. Explicit per-message IMAP Seen and MOVE actions remain available from the prior feature branch. Outbound provider SMTP, aliases and browser-closed follow-up alerts remain separate features.

## API extension

Existing authenticated owner-scoped historical import endpoints remain unchanged apart from the following additions:

- `POST /api/v1/mail/external/accounts/:id/imports` accepts `{ "folder": "INBOX", "continuous": true | false }`; default is false.
- `POST /api/v1/mail/external/accounts/:id/imports/:job_id/action` additionally accepts `enable_live`, `disable_live` alongside pause/resume/cancel.
- `GET /api/v1/mail/external/accounts/:id/imports` includes `continuous`, optional `last_polled_at`, and the `watching` status. The UI exposes an explicit opt-in checkbox, on/off controls, and a recent check time.

## Schema and rollback

Additive GORM migration modifies only `mail_external_import_jobs`: `continuous BOOLEAN NOT NULL DEFAULT FALSE` and nullable `last_polled_at`. Previous user jobs retain one-shot semantics. No migration was executed on production. Do not drop import job/item tables on rollback: they contain durable provenance and receipts. Source-matched mail binaries and frontend must be staged together after restoring the existing repository integration prerequisites.

## Source checks and remaining acceptance

Go unit tests simulate two polling cycles, a new UID-only delta, opt-in/out transitions, pause/resume, UIDVALIDITY rotation, account ownership, single-UID at-least-once avoidance, and source-side nonmutation. Frontend tests cover explicit per-folder opt-in, status display and disabling. These are source tests only. Real Gmail/iCloud/QQ/163/Yahoo provider and backend Stalwart production acceptance and NAS recovery are intentionally deferred.

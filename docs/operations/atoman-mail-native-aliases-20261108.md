# Native @atoman.org mail aliases (development source branch, 2026-11-08)

## Release state and prerequisites

**Not merged, deployed, enabled, or production tested.** The user requested finishing code before live acceptance. This feature requires the preceding external Send-as and offline follow-up branches, the existing active Stalwart/Atoman mailbox mapping, an approved Stalwart admin provisioning configuration and backup gates, and an additive schema migration. Production services, data and mailbox configuration must remain unchanged until approved acceptance.

An independent switch `ATOMAN_MAIL_ALIASES_ENABLED=1` is necessary for user-facing alias endpoints and selecting the alias for outgoing mail. Without it, native mailbox, IMAP, account password and external SMTP features retain their existing behavior. The optional service uses only server-side Stalwart admin credentials from `NewConfiguredMailProvision`; clients never supply domain IDs, Stalwart user IDs, alias slots, or credentials.

## Lifecycle and API

A single active Atoman account may reserve up to five extra local parts at `@atoman.org`. An alias delivers to **that same account**, not to a second mailbox. The existing local-part validation and reserved-name rules apply. New aliases are checked against current main mailbox reservations, previously created aliases (including disabled and needing review), and outstanding address-specific invites. All relevant address reservation paths use the same PostgreSQL transaction-scoped address-space advisory lock. Remote Stalwart uniqueness validation remains authoritative.

- `GET /api/v1/mail/aliases`: list only the current owner's aliases and statuses, no admin secrets.
- `POST /api/v1/mail/aliases` with `{ "local_part": "my-alias" }`: create an owner-only reservation, persist `needs_review` before `x:Account/set` appends the Stalwart alias, and transition to `active` only on an authenticated positive remote acknowledgment and a successful DB receipt.
- `POST /api/v1/mail/aliases/:id/action` with `{ "action": "disable" | "enable" }`: verify owner, Stalwart account mapping, current state and the stored alias slot; modify only the exact `aliases/<slot>/enable` remote path. Disabling is rejected when an alias is attached to queued/dispatching/uncertain outbound mail. Aliases never get deleted from the reservation registry; disabling does **not** make the address available to another user.
- If a remote result, final DB receipt, or account binding becomes uncertain, preserve `needs_review`. The user cannot blindly retry from that state; a separate trusted administrator reconciliation procedure is required. No side effect is automatically replayed.

## Native sender integration

The existing authenticated `GET /api/v1/mail/senders` now includes active owned aliases when the alias flag is enabled. The frontend selection is stored in the encrypted draft using `alias_id` and sent through the existing encrypted R2 outbox with the five-second undo window, delayed sends, idempotency and reconciliation. A message can select at most one of `alias_id` and `external_account_id`; a disabled, unauthorized or missing alias is rejected at queue time and again by the claimed send worker.

The actual native outbound transport is **still Stalwart JMAP**: it must provide a user-owned `Identity/get` identity whose exact email matches the selected alias. No arbitrary custom From string or Stalwart admin sending is allowed. If Stalwart does not expose that identity or blocks sending from it, the send fails closed and requires explicit setup/acceptance rather than forging an address.

## UI and tests

The mail settings page now includes an alias management panel: list/create, confirmation to disable, re-enable, and a visible needs-review state with no retry control. The compose sender dropdown handles both external account IDs and native alias IDs without ambiguity. Saved drafts with a no-longer-visible sender do not silently fall back to the primary address. Backend tests exercise owner isolation, uniqueness, pending-send protection, reversible alias enablement and fail-closed remote uncertainty using a fake Stalwart adapter; frontend tests verify the alias controls and draft/send selection.

## Acceptance tasks intentionally deferred

Requires a staging account with real Stalwart `x:Account/get`/`x:Account/set` alias behavior, real inbound delivery to the primary mailbox, `Identity/get` alias identity availability, successful native alias Send-as, SMTP/IMAP desktop-client checks, DNS routing, backup/restore proof and production roll-back plan. No claim of live end-to-end alias delivery is made in this source stage.

Remaining separate feature work: conflict-aware bidirectional IMAP flags, move and deletion synchronization; source import currently only copies new UIDs. Do not enable destructive two-way changes until a reviewed conflict policy, audit trail and live dry-run are available.

# Atoman Mail V2 production rollout and remaining acceptance gates

Date: 2026-11-06. This is a factual release receipt, **not an assertion that the entire roadmap is finished**. No personal credentials or message bodies appear here.

## Deployed source and baseline

- Backend source `64f938bc30286f3a258b45270bcaa56110fe1a64` (merged mail contacts, encrypted signatures, advanced JMAP search, scheduled outbox). Backend activated through the existing `scripts/ops/activate-runtime.py` at `/opt/atoman/backend/releases/64f938bc3028-49f03b67ae1c`.
- Production rollback record: `/var/backups/atoman/runtime-backend-20261106T090702Z` (retain together with previous runtime binaries).
- All five matching Go binaries compiled with `-buildvcs=false -trimpath`; mail_admin, mail_outbox_worker and mail_maintenance_worker staged in same immutable release as start_server and music_import_worker. The protected existing outbox service was restarted after activation.
- The mail-only additive migration completed using the existing operator `mail_admin --operation migrate-mail` in a non-root systemd transient unit with EnvironmentFile. Output: `{ "migrated": true, "scope": "mail-only" }`. Previous source snapshot at migration: `20261106T033122Z-wcm60eik` with both local encrypted exports verified, age under 24 h.
- Frontend source `09dd61374fa8b3011bdc336ce657e724bb838241` reached Cloudflare Pages production, deployment `9947c0d5-6596-445d-ba75-723f682e0b5e`, status success. Public `/mail` returned HTTP 200.

## Verified tests and smoke

- Go tests: `go test ./internal/modules/mail ./internal/app ./internal/migrations`; Go build: `go build ./cmd/...`; both passed.
- Frontend: type check and all mail-related unit tests passed. Real Chrome fixture checks desktop 3-pane layout, genuine independent mobile runtime at 320/390 pixels, private contacts, recipient autocomplete, advanced cross-folder search, durable scheduled queue cancellation, attachment MIME restrictions, HTML sanitization, safe no-remote-content mail rendering, standard drafts/undo and app-password revocation; all passed. These are **synthetic**, not live sending proof.
- Post activation: backend readiness HTTP 200. Backend, Stalwart, outbox and music-import services `active`; backend/outbox each `NRestarts=0`. New anonymous mail routes (unread, contacts, search, settings, drafts, outbox) returned HTTP 401, confirming route availability and auth gating. Existing `fazong@atoman.org` mailbox remained `active` and linked, checked through the existing operator tool without exposing its keys.

## Release scope versus unmet acceptance criteria

- **P1/P2 code and deployment:** Private encrypted contacts with ownership and revision, recipient completion, per-account signatures, site-wide unread and browser notifications, advanced JMAP search, durable scheduling, protected attachment preview and keyboard shortcuts are present in production code. Browser fixture success does not establish all authenticated production workflows.
- **Gmail deliverability:** Previously observed Gmail deliveries fell into SPAM even though SPF/RSA DKIM/DMARC and TLS succeeded. No new independent inbox-placement evidence; cannot claim resolved or guarantee Gmail Inbox. Do not whitelist test mail to manufacture success.
- **Backups:** Verified prior mail-only NAS recovery; full-site NAS archive arrival and independent full-site restore remain **unverified**. A separate post-V2 source backup run must be reviewed for actual completion, retention and offsite hash proof. The health flag `offsite_transfer_confirmed=false` must not be promoted to true without transport/restore receipts.
- **External alerts:** Existing systemd/journal and health timers are present, but no independently verified external notification destination and synthetic end-to-end alert proof. Do not mark external alerts complete.
- **External IMAP, multiple independent accounts, imports, AI:** These remain unimplemented or disabled pending account-provider authorization, SSRF/credential isolation, quotas, restart/resume tests, and privacy/model provider opt-in. Do not claim user-owned external account linkage, bidirectional sync, or AI processing.
- **Native client through private proxy:** No independent Mac client connection proof; ordinary IMAPS/SMTP server endpoints were previously verified outside that proxy.

## Next operational acceptance

1. Confirm the post-release paired source backup includes the new mail_contacts table, then verify a new NAS mail-capsule round trip and a separate full-site restore or preserve explicit gap.
2. Obtain genuine owner-session production acceptance for contact save/edit/delete, signature rendering, advanced search and scheduled-send cancel without sending unsolicited messages.
3. Verify deliverability over time to independent mailbox providers, document originals and provider classifications.
4. Configure a user-approved independent alert channel and test failure/recovery. Begin multi-account/IMAP migration only after an actual provider is authorized and scoped.

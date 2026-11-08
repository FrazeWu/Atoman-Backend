# Atoman Mail V3 private organizer — 2026-11-08

This is the first independently releasable portion of the five-feature plan. No NAS full-site recovery or external provider account was touched.

The additive workspace migration creates mail_organizer_records and mail_organizer_labels, keyed to an Atoman user. Reminder dates, custom label names and colors are protected by the existing authenticated outbox encryption vault, while opaque JMAP message IDs serve only as indexes. All JMAP message operations validate ownership against the current authenticated Stalwart account. User isolation, timestamp limits, record and label caps, encrypted contents, and optimistic revision checks are tested.

New private API endpoints:
- GET /api/v1/mail/organize
- POST /api/v1/mail/organize/labels
- PUT /api/v1/mail/organize/labels/:id
- DELETE /api/v1/mail/organize/labels/:id
- POST /api/v1/mail/organize/messages

The Vue interface reuses current PSheet, PButton, PInput and PDatetimePicker components. Users can create, rename, delete and filter by private labels, set Snooze or follow-up times on a mail item, clear a reminder, or restore a snoozed message. The Inbox hides conversations only when all their messages have a future snooze; after the deadline the message reappears automatically during an active client session. Selection for bulk actions excludes the hidden messages. Search and explicit label-filter views can still find these emails.

Caveat: this is LOCAL WEBMAIL organization. It does not move underlying Stalwart/IMAP messages and does not synchronize Snooze to other clients. Follow-up is a dashboard due-date reminder only, not a push alert from the server or automatic no-reply detection. All user-visible operations leave IMAP flags intact.

Release verification: Go mail/migration/app tests with isolated PostgreSQL; Vue type check, all mail-related Vitest tests, production build, and isolated real Chrome fixtures covering organizer and existing email workflows. Deploy an immutable backend build, apply only the additive migration through the existing non-root mail_admin tool, activate after a healthy snapshot baseline, then publish frontend Pages. Keep old runtime and the unit override as a rollback target.

Still pending from the five-feature plan: real Stalwart account aliases and verified send-as, complete external IMAP read/write and historical import, and persistent offline follow-up notifications. Do not claim these have shipped just because the organizer is deployed.

# External mailbox Send-as via existing encrypted outbox (2026-11-08)

Status: **code-only, not deployed or production-verified**. Existing active Atoman mailbox and storage/backup operational gates still apply; sending from a connected external account additionally requires `ATOMAN_MAIL_EXTERNAL_SMTP_ENABLED=1`. Do not enable this flag without approved SMTP provider integration testing.

- The UI offers only sender identities returned by the authenticated `GET /api/v1/mail/senders` route. The default is the native Atoman address. Non-native identities are listed only if a connected IMAP provider account exists in the same Atoman user scope AND external SMTP is explicitly enabled. The selected opaque `external_account_id` is stored inside an encrypted draft and included in the authenticated send request, never arbitrary From or SMTP host text.
- `POST /api/v1/mail/outbox` continues to use the existing R2 encrypted immutable MIME, persistent per-user idempotency identity, five-second undo window, and scheduled delivery. The server decrypts and verifies the connected provider's exact email address; the app-specific password is never returned to the frontend, injected into the mail envelope, logged or stored twice.
- A claimed `mail_pending_sends` job includes an additive nullable `external_account_id` and an encrypted payload that binds to the same sender. The worker validates that the selected provider credential still belongs to this user, the account is still active, the claimed pending job matches the source account, and the RFC822 From header equals the same address. No Stalwart administrator token is accepted. Unknown SMTP success/failure after the worker claim stays in the **uncertain** state; the system must not automatically re-send it.
- The SMTP transport accepts **only** a fixed catalog of providers, with TLS identity verification and minimum TLS 1.2; no user-supplied host, port or URL can enter a dial call. Gmail/QQ/163/Yahoo use implicit TLS on port 465; iCloud uses mandatory STARTTLS on port 587. SMTP `DATA` final acceptance means the provider accepted the envelope, so QUIT failure does not cause duplicate send retries.
- Attempting to disconnect a connected provider with a queued, dispatching or uncertain external outbound message returns a conflict. Cancel/reconcile the outbound job before disconnecting.
- For external sends, recipient counts, MIME sizes, server-side attachments, no-Bcc-header guarantee and scheduled send maximums are unchanged. Submission status is displayed through the existing outbox endpoints, not a second sending subsystem.

## Provider references

- iCloud SMTP official: https://support.apple.com/zh-cn/102525 (`smtp.mail.me.com` port 587, SSL/TLS, full email address and app-specific password)
- Yahoo outgoing SMTP official: https://help.yahoo.com/kb/new-yahoo-mail/imap-server-settings-yahoo-mail-sln4075.html (`smtp.mail.yahoo.com`, port 465/587, SSL and app password)
- Other catalog hosts require provider live testing in the eventual acceptance phase and may have account-specific SMTP restrictions.

## Important exclusions

This stage is authenticated Send-as for the **exact connected external address**, not verification of arbitrary third-party aliases. External SMTP acceptance does not guarantee insertion into every provider's Sent folder, nor does it copy the message into native Stalwart Sent automatically. Provider-specific SMTP throttling, authentication policies, and mail deliverability (SPF/DKIM/DMARC) need live acceptance. Native `@atoman.org` aliases and full provider IMAP flag/move/delete mirroring are separate stages. No migrations or production changes were run in this branch.

# Atoman Mail — Stage 7: administrator invitations and custom mailbox names

Date: 2026-11-04. Email remains integrated in existing Atoman identity, not a separate project or login. No SMTP/JMAP/IMAP/Cloudflare R2 mail delivery is enabled by these routes.

## New workflows

1. Site administrator or owner opens /site/setting#users, selects an active Atoman account, and uses 发放邮箱邀请码. Admin may use toolbar search or row action. POST /api/v1/mail/admin/invites accepts target_user_id only (optional legacy local_part). Only admins/owners can issue. The 32-byte random base64url code (43 chars) is returned exactly once, expires after seven days, and only a SHA-256 digest is stored. Deliver the secret manually by a trusted channel.
2. The invited account logs in and opens /mail, enters its invitation code AND chooses a custom 3–32 character ASCII mailbox local part. POST /api/v1/mail/activate now accepts token and local_part. Server verifies invitation ownership against the authenticated Atoman UUID, validates reserved names, uniqueness and other pending named invitations in a transaction, then creates exactly one mail_accounts reservation. Existing preselected-address invites may still be redeemed without local_part. Address is independent of website username changes.
3. Site admin/owner can directly reserve their OWN chosen address in /mail without an invitation via POST /api/v1/mail/admin/reserve-self. This bypasses only the invite requirement, not Stalwart readiness. It verifies admin/owner role and active Atoman account, reserved-name list, named pending invitations and unique indexes; this endpoint never accepts another user ID.

Both reservation paths return available=false, status=reserved, address=chosen@atoman.org. There is no active send/receive service before the Stalwart provisioning, private R2 and TrueNAS backup gates pass.

## Rollout and security

- Atoman site sessions, CSRF and role authorization still apply. API responses carry Cache-Control: private, no-store.
- No plaintext token is saved. Do not put invitation codes in links, logs, telemetry or other accounts.
- ATOMAN_MAIL_INVITES_ENABLED=1 remains an operator-controlled switch for issuing/redeeming invitations. When disabled the API returns 503 and the UI reports this clearly. Admin self-reservation requires no invite switch and never activates a real mailbox.
- No new tables or database migrations beyond existing stage-4/5 account/credential schema are required. Unique account user_id, local_part and address constraints apply.
- No Exim4, MX/DNS, R2 bucket, IMAP/SMTP state or Stalwart service is modified by stage 7.

## Verification

Run ./scripts/mail-invites-test.sh against temporary PostgreSQL; go build ./...; go vet ./internal/modules/mail; make swagger-check. Frontend runs Vitest on Settings users, invitation dialog, Webmail and API client; bun run type-check and bun run build. Do not declare production mail service ready based on only reservations.

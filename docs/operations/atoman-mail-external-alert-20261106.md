# Atoman Mail V2: independent owner alerts (2026-11-06)

## Implemented architecture and exact acceptance

Previously the mail operations alert service only wrote a daemon.err journal event. The initial attempt to reuse the site's existing Resend integration failed with HTTP 403. The existing Exim4 loopback MTA was then tested: it accepted an alert but rejected remote delivery because Debian Exim was configured with local-only routing.

The production Exim configuration is now outbound-capable for mail originating on the VPS loopback. It continues to listen ONLY on 127.0.0.1:25, while Stalwart keeps the public 169.58.161.96:25 listener. The Exim outbound route uses IPv4 and a valid fully-qualified HELO hostname. This avoids an earlier Gmail IPv6 SPF failure and invalid HELO failure. Its original configuration and generated file are backed up in /var/backups/atoman/exim-alert-20261106T124412Z.

A test message sent from the existing Atoman support sender to the already-known external owner Gmail mailbox was accepted by Exim, and accepted by Gmail MX with SMTP 250 2.0.0 OK using TLS 1.3. A direct Gmail search and original MIME read confirmed INBOX placement, SPF pass, DMARC pass, external receiver path and TLS 1.3. The message is a synthetic operator alert, not a user mailbox message. This does not show that regular fazong@atoman.org mail now reaches Gmail Inbox.

## Service and secret boundaries

- scripts/ops/send-mail-ops-alert.py sends a fixed operator-only alert with UTC timestamp, a whitelist of failed systemd units and diagnostic commands. No user mail, filenames, mailbox passwords, keys or raw provider diagnostic bodies are exposed.
- scripts/ops/check-mail-ops-watchdog.py checks active backend, outbox, and Stalwart services, Docker Stalwart running state, local backend readiness, and latest verified paired backup/capsule freshness.
- Systemd units at scripts/ops/systemd/atoman-mail-ops-alert.service, atoman-mail-ops-watchdog.service, and atoman-mail-ops-watchdog.timer implement OnFailure alerting and a ten-minute periodic watchdog with jitter.
- The sender runs non-root as atoman, with restricted systemd filesystem access. It reads only /etc/atoman-mail/ops-alert.env via the privileged systemd EnvironmentFile mechanism. The owner and sender addresses are intentionally absent from the public repository.
- The watchdog is a hardened root oneshot because the existing backup health receipt are root-only. It has no provider key or mailbox authorization.
- The alert sender records a private deduplication receipt only after the configured MTA accepts the message; identical failures are suppressed for 60 minutes. Local queue acceptance is explicitly named, and external Gmail acceptance is independently checked for the synthetic test.
- The known full-website NAS offsite gap remains a separate gate and is NOT silently converted to success by the watchdog.

## Tests and operational commands

The alert Python tests run without network and verify whitelisted unit parsing, provider request shaping, local SMTP envelope, deduplication, retry on failed delivery, and watchdog failure handling.

Use systemd-analyze verify for unit checks. After installation, systemctl start atoman-mail-ops-watchdog.service runs the first live health check; check its Result and ExecMainStatus. Confirm atoman-mail-ops-watchdog.timer is enabled/active and see its next scheduled check with systemctl list-timers. The failure notification service is a oneshot, normally inactive, and is invoked by existing maintenance/backup OnFailure edges or the new watchdog.

## Explicit limits

If the entire VPS or its network connection fails, it cannot send its own alert: an independent external uptime monitor is still recommended. Any future user mail deliverability, including previous Gmail SPAM classification, must be measured separately. A single successful synthetic alert does not guarantee all future alerts will arrive in Inbox. The complete WEBSITE ciphertext NAS arrival and independent all-site restore are still pending.

## Initial watchdog install correction

A first hardened execution could not inspect the Docker socket and triggered one genuine operator failure notification even though Stalwart was running. The check was corrected to probe the private Stalwart JMAP TCP listener at 127.0.0.1:18580 instead. The watchdog remains sandboxed and now has a unit test for listener failure. The affected systemd failed state is reset only after the corrected one-shot service passes; then the periodic timer is enabled.

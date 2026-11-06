# Atoman Mail V2 — verified NAS recovery and owner alerts

Audit: 2026-11-06 UTC. No provider credentials, recovery keys, or private message bodies are published.

## V2 paired backup and real NAS mail-only recovery

- New paired snapshot 20261106T090920Z-mi1frz51 completed successfully on Nov 6, 09:43 UTC. The three PostgreSQL databases atoman_prod, atoman_dev and atoman_mail each passed isolated full restoration including indexes and table counts. Both encrypted full-site and mail-only exports published on VPS.
- NAS mail capsule snapshots/20261106T090920Z-mi1frz51m/backup.tar.age is 1,372,680 bytes, SHA256 d3095f810a774e7d540feff1fbd8808ffe0f5fd9ec3a28f4acb2f1b4d12f617f. The actual NAS file hash matches receipt and LOCAL_COMPLETE.json.
- Actual NAS bytes were read back to Mac and decrypted using its existing offline age key. Separate isolated recovery verified 11 application user/mail tables including mail_contacts, 29 Stalwart PostgreSQL tables, 8 R2 objects, and 39 runtime files; all source/restored row counts and canary message/attachment checksums matched. No production data changed.
- Root-only receipt: /var/backups/atoman/mail-recovery/20261106T090920Z-mi1frz51/nas-recovery-receipt-v2.json. SHA256 92ef37a9219ab42db204c548e1f7a6f895011d2c31bb01a9a03fea7f611d4e6c, file mode 0600. Private age key remains on Mac, not VPS or NAS.
- The existing health field offsite_transfer_confirmed=false is NOT overwritten by mail-only restoration: full-site NAS is distinct.

## Full-site encrypted offsite evidence and remaining gate

- Older FULL website archive 20261106T033122Z-wcm60eik reached NAS; actual NAS ciphertext 829,345,656 bytes, SHA256 matched both receipt and complete marker. Downloaded 2026-11-06 04:32:59 UTC. Its independent NAS decrypt/restore check remains false.
- Newer full website archive 20261106T090920Z-mi1frz51 remains incomplete on NAS. At 2026-11-06 12:59 UTC, only 82,513,920 bytes were staged in NAS .incoming and the SFTP pull status said TimeoutExpired. Reget/resume preserves progress; only complete hash-checked downloads are atomically published.
- Mac/VPS relay throughput was also slow, and the probe was canceled and removed. Other user-owned ongoing Book sync was preserved and not disturbed. Do not claim the latest full-site archive is fully offsite or independently restored.

## Out-of-band owner alerting

- Previous alert only wrote daemon.err to local journal. Initial Resend send returned 403, so Resend was not treated as functional.
- Existing Exim4 was enabled for outbound delivery from loopback only, with IPv4, valid FQDN HELO, and intact public Stalwart SMTP listener. Original Exim settings and generated config are preserved at /var/backups/atoman/exim-alert-20261106T124412Z for rollback.
- Real external owner test: Gmail MX accepted SMTP with TLS 1.3 and status 250 2.0.0 OK. A focused Gmail API search and raw MIME read confirmed the synthetic owner alert in INBOX, SPF pass and DMARC pass. Two earlier regular fazong mailbox tests remain in SPAM; this independent alert test does NOT resolve routine mailbox placement.
- Alert script runs non-root as atoman, reads private /etc/atoman-mail/ops-alert.env, sends only whitelisted ops unit names and timestamps to configured external owner. Deduplicates same faults for 60 minutes; local queue acceptance does not by itself claim Inbox.
- Backup, health and maintenance OnFailure triggers are retained. Active periodic systemd watchdog checks backend, Stalwart, outbox, private JMAP port, HTTP readiness, and paired backup freshness approximately every 10 minutes.
- Initial watchdog sandbox check produced a false alert; corrected the Docker socket dependency and restricted health receipt permissions. Verified corrected oneshot Result=success and ExecMainStatus=0. Python test suite: 11/11 passed. Timer enabled and active.
- An independent ChatGPT hourly condition watch checks public /mail and /readyz URLs, notifying only on credible failures; this covers some total VPS outages that its own local notification cannot detect.

## Relevant sources and outstanding acceptance

Backend main after operational change: 450aa9cbf4b2210aefdcc6654d2a2e3fe8b1a40d. Live Go runtime 64f938bc30286f3a258b45270bcaa56110fe1a64. Frontend main d5f55b37b93f6f557c8b899870e1d7348b672976.

Outstanding: newest website ciphertext complete NAS transfer + independent full-site restore; genuine regular-user Gmail Inbox classification; and any external IMAP account import/aggregation or AI reading that depends on separate provider authorization.

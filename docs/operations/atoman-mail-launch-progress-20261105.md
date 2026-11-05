# Atoman mail release checkpoint — 2026-11-05

This checkpoint records observed production evidence. Remaining gates are
explicit; configuration preparation alone is not release completion.

## Verified

- Both dedicated private R2 mail buckets passed Head, disposable Put/Get
  with SHA-256 comparison, Delete and missing-object checks.
- Stalwart is bootstrapped on its dedicated PostgreSQL database and runs
  under an enabled systemd/Docker service with a pinned image digest.
- Public TLS submission and IMAPS use a trusted mail.atoman.org certificate.
  SMTP 25 does not permit unauthenticated relaying to external recipients.
- The owner's real external test and existing support forwarding both
  arrived in Gmail INBOX. SPF, RSA DKIM and DMARC passed; transport TLS 1.3.
- Public service DNS, certificate renewal hook and HTTPS MTA-STS policy are
  prepared. The previous Cloudflare MX destination remains in place at
  this checkpoint.
- Native Stalwart Trash/Junk retention is 30 days; daily expiry and blob
  cleanup schedules are active.
- The existing application reservation is fazong@atoman.org. It is still
  reserved; production activation awaits the recovery gate.

## In progress

- Complete backend/UI features, real Stalwart tests and reviewed commits.
- Paired application/mail/private-object recovery, including the actual
  NAS copy and owner-side decryption.
- Mail-only migrations, managed backend release, supervised sender and
  maintenance timer, exact-revision Cloudflare Pages release.
- Provision the existing reservation and validate public incoming mail,
  then cut over the apex MX and verify the live Webmail workflow.

## Operational notes

Production uses the non-root managed runtime under /opt/atoman/backend;
scripts/ops/activate-runtime.py is the release mechanism. The legacy
deploy-production.sh must not be used. Mail environment is loaded after
90-managed-runtime.conf via 99-mail.conf.

Private evidence, secrets and DNS rollback state remain on the server and
are excluded from Git. See deployment/mail/README.md.

The owner's Mac resolves the mail hostname through a personal Clash
fake-IP route; direct connections to the public server with the correct
TLS hostname succeed. Automatic approval rejected reading that unrelated
personal proxy configuration. No attempt was made to bypass that decision;
a client-side proxy change needs separate authorization and does not
affect the hosted Webmail route.

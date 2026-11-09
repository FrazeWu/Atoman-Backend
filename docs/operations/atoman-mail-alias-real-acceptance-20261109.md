# Stalwart native alias acceptance: disposable production-domain accounts (2026-11-09)

**Scope:** Real Stalwart 1.0.2 with two new synthetic-only accounts under the existing `atoman.org` domain. Never attach a personal account, change production domain settings, or send to external recipients. Both accounts were removed after testing, including the failed initial probe; credentials and message contents are not preserved.

## Compatibility issue and fix

The production Stalwart `x:Account/get` responds with numeric-keyed `aliases` objects such as `{"0":{"name":"test","domainId":"domain","enable":true}}`, **not** the JSON array assumed by the original Go adapter. This made owner-level alias creation and enable/disable reject a live Stalwart response. The adapter now safely decodes either numeric-keyed objects (as observed on Stalwart 1.0.2) or arrays for compatibility, preserving exact numeric slot identity. Sparse, nonnumeric, leading-zero, missing-domain, malformed and oversized slot collections fail closed. The frontend never supplies a remote slot or Stalwart admin credentials.

## Real disposable acceptance

New operator-only `scripts/ops/mail-alias-disposable-acceptance.py --confirm-disposable-only --receipt PRIVATE_NEW_PATH`:

- Requires a new nonexisting private receipt file and the root-readable Stalwart admin environment.
- Confirms the internal mail endpoint is the configured VPS with valid public TLS certificates.
- Creates two brand-new synthetic-only Stalwart accounts with randomly generated credentials and a unique alias.
- Creates and confirms the exact alias using real `x:Account/get` / `x:Account/set`.
- Delivers exactly one synthetic message from the other temporary account to the alias using SMTPS 465, and verifies delivery to the original mailbox via read-only IMAPS 993. Checks From/To, unique subject and body marker, and attachment SHA-256.
- Disables and re-enables that specific alias slot with readback checks.
- Checks the owner's read-only JMAP `Identity/get` for the exact alias sender identity. The script does **not** send as the alias or assume availability without checking.
- Always attempts to destroy exactly the two generated accounts in a `finally` block. If cleanup is uncertain, the private receipt identifies only those new account IDs. It never stores credentials.

Successful private receipt on VPS: `/var/lib/atoman/tmp/mail-alias-disposable-acceptance-20261109-v2.json` with `successful=true`, `created=2`, `deleted=2`, `alias_identity_available=true`, zero external messages and zero personal mailboxes touched. The initial v1 attempt exposed the mismatched JSON type before mutating aliases, and also confirmed the two generated accounts were cleaned.

Go contract tests verify object/array decoding, strict slot mapping and user-independent admin JMAP `get -> set -> get` calls. Existing mail alias service ownership, reservation and outbox tests still apply. This acceptance verifies the **underlying Stalwart alias routing**; it does not prove every authenticated Atoman Webmail UI action with a real site account, nor does it validate outbound `EmailSubmission/set` from an alias identity. Production alias flag should be enabled only after deploying the corrected Backend and completing the production feature prerequisites.

## Retained safety restrictions

No external Gmail/QQ/163 credential exists in the connected production account registry (0 linked accounts); such provider-specific IMAP/SMTP testing cannot be honestly declared complete. Do not use a real user's login, create unsupported provider accounts, or enable automated destructive cross-folder sync without the missing provider-specific acceptance.

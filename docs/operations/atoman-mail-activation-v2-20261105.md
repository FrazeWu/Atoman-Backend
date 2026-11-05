# Mail activation evidence v2

`mail-activate-environment.go` remains an operator-only, build-ignore program. It does not alter a running service or write the mail environment unless the operator passes `--apply`. Normal application binaries do not include this program.

## Scope and truthful claims

A v1 report retains the original full encrypted snapshot NAS roundtrip contract. A v2 report proves recovery of the complete mail capsule from NAS. The full website bundle can remain pending while every required mail recovery check succeeds. The v2 report must explicitly distinguish these states:

- `full_snapshot_nas_verified` and `main_bundle_nas_verified` are equal booleans. When false, `main_bundle_nas.status` is `pending`, with an actual observation time and partial byte count. It cannot contain success receipts or claim Mac readback.
- When those booleans are true, the main bundle needs its own matching NAS receipt, publication marker and successful pull status. This verifies NAS integrity; `mac_full_ciphertext_readback` remains false in the capsule flow.
- `capsule_nas_roundtrip_verified` and `all_mail_payload_files_verified` must be true. The capsule needs its own export ID, actual NAS readback checksum and byte count, receipt, and publication marker.
- `all_snapshot_files_sha256_verified` must be false for v2. Ambiguous top-level `ciphertext_sha256` and `nas_roundtrip_verified` are rejected. Capsule evidence cannot be relabelled as v1.

`pre_activation_mail_capsule` preserves the tables actually present in its immutable source snapshot and explicitly lists the absent new mail tables. It does not claim to restore state that did not yet exist. `post_activation_mail_capsule` requires all nine application mail tables. Both scopes restore the full `Users` table from the same source dump and verify mail ownership relationships.

The application tables are `mail_accounts`, `mail_invites`, `mail_access_credentials`, `mail_pending_sends`, `mail_drafts`, `mail_uploads`, `mail_preferences`, `mail_action_receipts`, and `mail_retention`. Every additional `mail_*` table present in the source manifest is also required. Source and restored table sets and counts must match exactly.

## Webmail application baseline

To restore the Webmail web entry point, first restore a compatible Atoman website baseline or initialize the complete application/authentication schema and configuration, then restore the capsule's mail data and `Users` at its declared snapshot point; the capsule is not an independent whole-site restore. `Users` preserves the UUIDs, password hashes, roles, and authentication versions needed for existing password users to sign in again once `auth_sessions` and `auth_login_events` exist, while continuity of old sessions, OAuth bindings and flows (`external_identities` and `oauth_flows`), and unrelated website state is the responsibility of the website baseline/full backup.

## Independent operator inputs

The existing `--recovery-report`, `--snapshot`, and `--ciphertext-sha256` arguments remain. **For v2, `--ciphertext-sha256` always denotes the original full source bundle, never the capsule.** V2 additionally requires:

| Argument | Meaning |
| --- | --- |
| `--source-manifest` | Original immutable snapshot `manifest.json`, whose raw bytes are hashed |
| `--source-export-receipt` | Independent original full bundle `receipt.json` |
| `--capsule-receipt` | Independent local capsule export `receipt.json` |

Metadata files may keep their existing read-only publisher permissions. The program rejects symlinks and group/other-writable evidence files; it does not change permissions. The recovery report and credential/environment files retain the existing private-file requirement.

The original snapshot ID, completion time, manifest hash, full ciphertext hash and byte count must agree across the source inputs, capsule manifest and recovery report. The original source manifest must record completed SQL restore checks and verified mail assets. The data-and-mail source profile keeps its prior full-restore reference requirement.

The capsule ID is the original snapshot ID plus `m`. Its receipt uses the generic encrypted export envelope with `backup_scope=mail-complete` and `paired_source_snapshot_id` pointing to the original snapshot. The independent capsule receipt binds `capsule_manifest_sha256`. The guard recomputes that digest from the embedded manifest using the producer's sorted, compact, ASCII JSON encoding. Numeric manifest fields are integers.

Native mail SQL and mail-assets hashes must match the original snapshot manifest. The application extraction must reference the exact original `atoman_prod.dump` hash. Both complete private R2 bucket inventories, runtime-file coverage, application payload and application manifest digests are bound through the capsule manifest. Count summaries must agree with the actual restoration report.

## Mail recovery checks retained

V2 still requires isolated recovery with disposable S3 credentials, matching PostgreSQL row counts, no production data modification, the original mailbox UID, matching complete message bytes, and matching plaintext-body and attachment hashes. The source and expected content hashes must be valid SHA-256 values and equal. The source, NAS observations and recovery report must be current within 48 hours, with at most five minutes of clock skew.

The complete application metadata report must confirm actual restore success, table counts, user links, credential-account links and draft ownership. An accepted send may legitimately refer to a draft that has subsequently been deleted; extraction verification must preserve the application's lifecycle semantics rather than invent new foreign-key requirements.

The admin preflight only accepts `http://127.0.0.1:18580`, does not use environment HTTP proxies, and rejects every redirect before forwarding credentials.

## Verification

The meaningful operator tests are run by:

```sh
go test scripts/mail-activate-environment.go scripts/mail-activate-environment_test.go -count=1
```

The existing Python ops-test discovery invokes these through `scripts/ops/test_mail_activation_guard.py`; no workflow or gate is disabled. Fixtures test truthful pending website bundles with complete mail recovery, both pre/post activation scopes, invalid or missing source/capsule/NAS/metadata evidence, content and manifest hash mixups, expiry, v1 compatibility, exact Python/Go canonical JSON interoperability, and proxy/redirect prevention. Test fixtures are not operational recovery evidence.

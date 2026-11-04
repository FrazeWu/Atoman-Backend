# Atoman Webmail R2 bootstrap (2026-11-04)

## Scope and boundaries

Atoman Webmail is an internal Atoman module, not a separate website, account system, or Git repository. This task establishes the private object-store configuration boundary only. It does not install Stalwart, provision mail users, introduce routes, change DNS, or send email.

## Existing identity and storage integration

- Reuse Atoman's current `S3_ENDPOINT`, `S3_REGION`, `AWS_ACCESS_KEY_ID`, and `AWS_SECRET_ACCESS_KEY` values in the **server environment**; no additional Cloudflare key is required if the current R2 credentials already authorize both mail buckets.
- Existing public media `S3_BUCKET` must never be used for mail. No public website storage client, `public-read` object ACL, public custom domain, browser R2 presigned URL, or automatic bucket creation should be reused for private email.
- `MAIL_R2_BLOB_BUCKET` defaults to `atoman-mail-prod`, assigned to Stalwart's S3 blob store.
- `MAIL_R2_PENDING_BUCKET` defaults to `atoman-mail-pending`, reserved for future encrypted immutable snapshots of the 5-second server-side undo-send queue.
- The primary Stalwart PostgreSQL data store, and Atoman's mail metadata/queue tables, are separate responsibilities even when hosted on the same PostgreSQL server.
- Reusing an existing key does not prove it can access new buckets. Access must be checked before enabling mail. **Do not widen a key's scope without reviewing the impact on existing public media.**

## Configuration safety

`internal/modules/mail/r2.go` defines `LoadR2ConfigFromEnv` and `VerifyBuckets`. These reject wrong or non-HTTPS endpoints, missing keys, invalid bucket names, and overlap between public media and private mail buckets. The read-only permission probe uses `HeadBucket` and intentionally never auto-creates or modifies storage.

The current Atoman media client is not suitable for mail: `storage.PutPublicObject` sends `ACL: public-read`, and `storage.ValidateS3Connection` attempts bucket creation on failure.

## Test procedure (when permitted)

Default offline tests:

```sh
go test ./internal/modules/mail -count=1
```

Optional read-only live permission check after the *existing* server environment is supplied through its normal secure mechanism:

```sh
ATOMAN_MAIL_R2_INTEGRATION=1 go test ./internal/modules/mail -run '^TestExistingAtomanCredentialsCanHeadPrivateMailBuckets$' -count=1
```

The live test **only** calls HeadBucket on the two known private mail buckets. It has not been run as part of this task because access to runtime credentials for remote testing was restricted.

## Future delivery gates

1. Prove current credentials can access both mail buckets, and confirm bucket policies/public domains remain private.
2. Validate Stalwart S3 operations against the current Cloudflare R2 implementation in isolated staging (including read-after-write, attachment retrieval, delete and network failure).
3. Implement encrypted immutable pending-send payloads and database-backed 5-second cancel/submit arbitration. Never store plaintext pending mail in R2.
4. Prove PostgreSQL+R2 consistent backup and TrueNAS restoration before moving any production mailbox into service.
5. Add Atoman Mail routes, frontend, accounts and subscriptions only after the backing mail service is verified; leave existing production MX unchanged until external delivery tests pass.

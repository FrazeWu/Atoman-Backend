# Atoman Webmail — isolated Stalwart/R2 stage 1 verification

Date: 2026-11-04. Mail belongs to the existing Atoman backend/frontend and shares Atoman's existing Cloudflare R2 S3 identity. This document is a record of staging checks, not a live-mail deployment record.

## Verified on the VPS

- Official Stalwart `stalwartlabs/stalwart:v1` was pulled and started in a disposable Docker container.
- Only `127.0.0.1:18580 -> container:8080` was published; the bootstrap `/admin` endpoint returned HTTP success.
- The disposable container was removed immediately after testing. No administrator password was extracted, copied or retained.
- No ports 25/465/587/993/443 were mapped from Stalwart; no MX, DNS, reverse proxy, or production PostgreSQL config was changed.
- The host's existing **loopback** SMTP port 25 is already owned by exim4, so normal production SMTP listeners need a deliberate, separately tested cutover plan. Do not stop exim4 during staging.
- An unauthenticated HTTPS request to the Cloudflare R2 account S3 endpoint successfully established TLS and received HTTP 400. This proves reachability only, NOT that Atoman's R2 credentials can access the mail buckets.

## R2 isolation and credential scope

The two private standard-class buckets already exist:

| Bucket | Role | Public website access |
|---|---|---|
| `atoman-mail-prod` | Stalwart mail raw content and attachments | None |
| `atoman-mail-pending` | future encrypted immutable pending send payloads | None |

Atoman already runs with `S3_ENDPOINT`, `S3_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` in its existing server-side configuration. These values should be reused, **only if the existing token permits access to both new buckets**. The actual authenticated S3 HeadBucket check is **unverified**: attempts to access runtime credentials for that live check were restricted. Do not obtain credentials through another route to circumvent that restriction. Never echo secrets, add them to Git, make mail buckets public, or reuse `storage.PutPublicObject`.

Current Cloudflare configuration retrieval confirms both mail buckets' managed public domains are disabled and no custom domains are attached. R2 permissions may still be narrower than account-level list permission.

## Stalwart 1.x BlobStore example (configuration plan, not applied)

Stalwart v1 uses the new JSON-typed Storage configuration; do not paste pre-1.0 TOML storage settings. The following example is specifically for the Stalwart **BlobStore** object, not `config.json` itself:

```json
{
  "@type": "S3",
  "bucket": "atoman-mail-prod",
  "region": {
    "@type": "Custom",
    "url": "https://<ACCOUNT_ID>.r2.cloudflarestorage.com",
    "region": "auto"
  },
  "accessKeyId": {"@type": "EnvironmentVariable", "variableName": "AWS_ACCESS_KEY_ID"},
  "secretKey": {"@type": "EnvironmentVariable", "variableName": "AWS_SECRET_ACCESS_KEY"},
  "sessionToken": {"@type": "None"}
}
```

Stalwart is a backend service of the Atoman project, not a second product or second identity provider. Never mount the entire Atoman application environment into Stalwart; pass only its required R2 variables by the platform's secure service configuration when permitted, and grant Stalwart no access to public media-upload code.

## Repeatable staging startup test

```sh
./scripts/mail-stalwart-bootstrap-smoke.sh
```

The test initializes a short-lived Stalwart container and probes its local bootstrap endpoint. The test does **not** enable production mail. It cleans up its container on normal exit; if interrupted by a hard kill, confirm the container is not still present before rerunning.

## Remaining blocking gates

1. Authorize/complete actual S3 `HeadBucket`, `PutObject`, `GetObject` and `DeleteObject` checks using the existing Atoman credentials in a permitted controlled environment. Only create test objects under a unique disposable prefix; never touch existing application data.
2. Create a **separate Stalwart PostgreSQL database** on the existing PostgreSQL instance (not a separate project), and configure Stalwart v1 via its documented Bootstrap/Storage API. Do not change Atoman's existing tables.
3. Verify actual Stalwart mail content and attachments are persisted into the private `atoman-mail-prod` bucket, and that storage failure prevents acceptance of new mail.
4. Verify encrypted, immutable snapshots and server-side state transitions for the 5-second Atoman undo-send queue before enabling sends.
5. Build encrypted, consistent PostgreSQL + R2 backups to an isolated TrueNAS dataset and successfully restore a full test mailbox.
6. Only after the above passes, add production JMAP and IMAP/SMTP configuration and plan the exim4 / DNS cutover. Do not modify current MX or production ports during staging.

Source docs: https://stalw.art/docs/install/platform/docker/ ; https://stalw.art/docs/configuration/bootstrap-mode/ ; https://stalw.art/docs/storage/backends/s3/

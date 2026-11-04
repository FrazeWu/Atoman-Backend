# Atoman Webmail — isolated PostgreSQL + S3 + IMAP acceptance, 2026-11-04

This is an integration test **inside the existing Atoman backend repository**. It is not a separate user account system or production deployment.

## Actual results

Two repeatable tests were executed on the Atoman VPS (vmi3410530), with temporary Docker resources and synthetic accounts only.

| Test | Verified result |
|---|---|
| `scripts/mail-stalwart-postgres-smoke.sh` | Stalwart v1 connected to PostgreSQL 16, automatically created its schema (29 tables), reconnected after restart and reused that schema |
| `scripts/mail-stalwart-s3-fixture-smoke.sh` | Stalwart v1 Bootstrap JMAP accepted PostgreSql DataStore + S3 BlobStore configuration |
| Synthetic SMTP delivery | Delivered an `example.test` test message with a byte-pattern attachment to the local test account |
| S3 object storage | Stalwart wrote objects to a disposable S3-compatible test bucket; the test downloaded and verified nonempty objects |
| IMAPS read | Used the generated staging account credentials to read the synthetic test message and verify both body text and exact attachment bytes |
| Restart recovery | Restarted Stalwart **after** accepting the message; IMAPS still retrieved the message, body and attachment |
| Cleanup | Containers, Docker networks, fixture volumes and generated temporary test credentials were removed |

The S3 fixture is a **disposable MinIO container**, used only as a compatibility fixture. The **production storage choice remains Cloudflare R2**. The S3 object inventory contains both mail and Stalwart service objects, so object count is not an independent mailbox count; the stronger evidence is that an authenticated IMAP read returned the original attachment bytes after restart.

## Isolation and security

- The PostgreSQL fixture is a **new disposable PostgreSQL 16 container**, not the Atoman production PostgreSQL instance. PostgreSQL temporary data is hosted on tmpfs.
- The S3 fixture uses a disposable bucket and named Docker volume; it never uses any real Atoman or Cloudflare keys.
- The SMTP, IMAPS, HTTP bootstrap and S3 ports are published on `127.0.0.1` only. The fixture uses a dedicated Docker bridge network, not host networking. Note that Docker's bridge network is not egress-blocked; no external mail recipients were used.
- Every stage password and generated admin credential is temporary and deleted by the cleanup handler. The script must not be modified to print secrets or reuse production credentials.
- The test may be rerun only when the named stage resources do not already exist; it refuses to take over another container/volume. For an interrupted force-killed test, inspect the resources before manual cleanup.
- No production MX/DNS, Exim4, 25/443 public port bindings, database schema, website code path or Cloudflare R2 object was modified.

## How to rerun

From the Atoman-Backend worktree, when Docker is available:

```sh
./scripts/mail-stalwart-postgres-smoke.sh
./scripts/mail-stalwart-s3-fixture-smoke.sh
```

The scripts fail closed on container/network collisions, validate S3 object retrieval, and report status without printing credentials. The S3 fixture uses `postgres:16-alpine`, `minio/minio:latest`, `stalwartlabs/stalwart:v1`, `aws` CLI and Python 3. For reproducibility in a later CI pipeline, pin image digests instead of mutable tags.

## Still unverified — release blockers

1. **Production Cloudflare R2 credentials:** The existing `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` must be allowed to access `atoman-mail-prod` and `atoman-mail-pending`. Previous online credential probes were blocked by the integration's security restrictions. This test intentionally does not work around that restriction or read the production environment.
2. **Actual R2:** Test Cloudflare R2 `HeadBucket`, `PutObject`, `GetObject`, delete and fault handling with a separately authorized runtime; do not infer R2 authorization merely from MinIO interoperability.
3. **Existing PostgreSQL instance:** Create a dedicated Stalwart DB and role using the existing instance after access, capacity and backup constraints are approved; the fixture has not touched the production instance.
4. **Atoman identity and app passwords:** Add secure provisioning, JMAP scoped credentials, invite-based activation, IMAP/SMTP app passwords, authentication/authorization tests.
5. **Five-second undo queue:** Verify immutable encrypted pending payloads, database transactional cancel/submit arbitration and unknown-result reconciliation.
6. **TrueNAS:** Complete paired PostgreSQL+R2 consistent backups and a full test restore.
7. **Production cutover:** Keep existing Exim4 loopback port 25 and current MX until public SMTP, SPF, DKIM, DMARC, PTR, relay, abuse controls and external delivery are verified.

References:
- https://stalw.art/docs/configuration/bootstrap-mode/
- https://stalw.art/docs/storage/backends/postgresql/
- https://stalw.art/docs/storage/backends/s3/
- https://stalw.art/docs/install/platform/docker/

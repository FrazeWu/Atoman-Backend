# Atoman Papers P0: MinIO immutable publication gateway (2026-10-28)

## Scope / disposition

- Branch: feat/papers-immutable-gateway-20261028, based on d1a88ac.
- Target: vmi3410530 isolated worktree only.
- **Result: disposable MinIO publisher-gateway sub-gate PASS. Papers production remains NO-GO.**
- No production MinIO account, bucket, firewall, systemd unit, frontend, API, or main branch was changed.

## Why this is needed

The pinned MinIO image used in our P0 tests rejects the IAM condition key
s3:if-none-match. Its versioned COMPLIANCE Object Lock retains each object
version but cannot block unconditional PutObject creating a newer current
version. Retaining the old client-only If-None-Match would expose an overwrite
bypass to a stolen publisher credential.

**New trust boundary:** publisher credentials no longer authorize ANY S3
PutObject. An independent, local-only gateway holds its own prefix-scoped S3
writer credential and accepts only a constrained conditional PUT protocol.
This protects against **publisher credential abuse and client bypass**, not
against a malicious gateway administrator, host root, or MinIO administrator.

## Design / privileges

| Principal | Identity in disposable test | Direct MinIO permissions |
| --- | --- | --- |
| Offline publisher | UID 65534, papersp0pub* | GetObject in private immutable prefix; GetBucketVersioning / GetObjectLockConfiguration; **no Put/Delete/Copy** |
| Trusted write gateway | UID 65532, papersp0gate* | PutObject/GetObject, private immutable prefix only, plus bucket versioning/lock configuration reads; **no Delete/List/Policy administration** |
| Compile reader | separate papersp0read* | GetObject, private immutable prefix only |
| Test bucket administrator | randomized papersp0root* | Used only to provision/delete a disposable MinIO container; **never passed to producer or gateway** |

- Gateway Unix socket ownership/mode: 65532:65534, 0660, parent 0750.
- Kernel SO_PEERCRED must report UID 65534. HTTP X-UID or other identity
  headers have no authority. TCP calls lack peer credentials and are denied.
- The gateway independently probes its target S3 provider before opening the socket: versioning enabled, verified atomic conditional 412 and original-byte readback, anonymous GET rejected, and minimum COMPLIANCE Object Lock. Any failed probe is startup fail-closed.
- The gateway accepts only PUT /v1/objects/papers/immutable/v1/{approved-key}
  with exactly If-None-Match: *. It has no delete, list, presign, copy,
  arbitrary bucket, or raw MinIO proxy endpoint.
- Approved keys: snapshots/<sha256>.json,
  environments/<sha256>.json, trusted-releases/<env-sha256>.json,
  trusted-archives/<archive-sha256>.tar and short random _probes/<hex>.
  Source/environment/archive body SHA-256 must match its content-derived key.
  Signed release envelope must identify its environment; real signing-key
  verification remains mandatory on the trusted reader.
- The gateway bounds Content-Length and spools streamed data to a private
  owner-only directory; a single upload can execute at a time. S3 requests
  set If-None-Match before SigV4 signing; a second write returns 412.
  A successful response requires authenticated, size-exact SHA-256 readback.
  No best-effort/HEAD-then-PUT fallback.
- Small JSON manifests stay within the pre-existing 4 MiB + 64 KiB cap;
  signed releases within 8 KiB, and archives within the existing 4 GiB cap.
  Bounded busy response 429 is treated as transient by the client adapter.
- Gateway Transport implements the existing TrustedArchiveTransport contract,
  so PrivateS3Registry and TrustedArchiveRegistry continue to validate
  private snapshots, COMPLIANCE retention, signed releases, and cache hashes.
  Producer-side S3 client is used ONLY for reads and capability checks.

## Verified behavior

To rerun in a disposable instance (root is required only for creating
ephemeral OS identities and containers):

    cd /root/Atoman/.worktrees/papers-immutable-gateway-20261028-backend
    PAPERS_P0_GATEWAY_CONFIRM=1 bash scripts/papers/p0-pubgate-minio-e2e.sh

- Pinned MinIO image, randomized loopback-only port/bucket/credentials,
  temporary tmpfs storage, COMPLIANCE default retention, three non-admin
  identities. The script does not read production secrets.
- A real gateway process running as UID 65532 accepts another process at
  UID 65534, which has **no direct S3 writer permission**.
- A separate unprivileged executable exercises real
  PrivateS3Registry.Probe, PublishSnapshot, PublishEnvironment,
  TrustedArchiveRegistry.PublishTrustedArchive, Ed25519 signed-release
  verification, and TrustedArchiveCache.FetchArchive. The small TAR is an
  explicitly synthetic fixture, **not** evidence of real TeX compiling.
- First conditional publication 201, duplicate 412, omitted precondition
  428, unauthorized peer UID 403, path escape/unknown kind refused.
- Publisher direct PutObject/CopyObject/DeleteObject/version-specific
  DeleteObject all rejected; post-attack GetObject retains original bytes;
  admin observes only one object version and no successful delete marker.
- The run exited 0, and the gateway process, MinIO container, and private
  working root were removed. Go unit tests passed with -race.

Proof markers:

    P0_GATEWAY_IAM_SEPARATE_READONLY_PUBLISHER_AND_WRITER_CREATED
    P0_GATEWAY_INDEPENDENT_PRIVATE_VERSIONING_CONDITIONAL_COMPLIANCE_PROBE_PASS
    P0_GATEWAY_SEPARATE_PROCESS_UNIX_SOCKET_PEERCRED_READY
    P0_GATEWAY_READONLY_PUBLISHER_SIGNED_SNAPSHOT_ENV_TAR_RELEASE_CACHE_PASS
    P0_GATEWAY_REAL_MINIO_CONDITIONAL_PUT_REPLAY_412_PASS
    P0_GATEWAY_PUBLISHER_DIRECT_PUT_COPY_DELETE_VERSION_DENIED
    P0_GATEWAY_PEERCRED_UNAUTHORIZED_UID_DENIED
    P0_GATEWAY_SERVICE_WRITER_PREFIX_SCOPED_AND_DELETE_DENIED
    P0_GATEWAY_PRIVATE_OBJECT_UNCHANGED_AFTER_BYPASS_ATTEMPTS
    P0_GATEWAY_EPHEMERAL_RESOURCES_REMOVED

## Performance and capacity

- The gateway does not buffer large rootfs TAR files into Go heap; it
  streams them into a bounded, private temporary file.
- A newly published archive incurs **one client-to-gateway transfer, one
  gateway-to-MinIO transfer and one full MinIO readback**. Temporary disk
  requires at most the allowed archive size per in-flight request plus
  filesystem overhead, and output is removed on completion/failure.
- The prototype intentionally has one in-flight publication slot and
  returns 429 when busy (safe retry). It is not a production throughput SLA.
- The separate earlier P0 test proved full 1.1 GB TeX Live signed
  S3-to-gVisor PDFLaTeX and SIGKILL recovery, but it used the earlier
  test-only MinIO publisher IAM. **The 1.1 GB end-to-end path has not yet
  been rerun through this new gateway**, so the combined production path
  cannot be called tested or fast until full-size measurements are made.

## Remaining production blockers (do not auto-deploy)

1. Provision dedicated real OS identities, group/UDS ACLs, hard systemd
   sandboxing, restricted Unix socket parent and staging directories. Never
   run the gateway as root or co-locate publisher and gateway credentials.
2. Verify the *actual* production MinIO/IAM permissions and prohibit alternate
   paths to writer credentials. Restrict backend networking, remove unsafe
   exposed ports, require TLS for cross-host traffic, key rotation and audit.
3. Protect the gateway executable/release supply chain; pin trusted signing
   public keys and enforce signer approval before creating a release key.
   Current gateway checks release envelope; trusted reader checks Ed25519.
4. Rate/space quotas, stalled-request deadlines, monitoring, reconciliation,
   backup/retention and multi-process race/fault-injection acceptance.
5. Rerun the complete 1.1 GB TeX Live → signed Gateway → private S3 →
   restricted launcher → gVisor compile → crash recovery sequence, benchmark
   time/CPU/memory/disk and check cleanup under failure.
6. Explicit security/operator review and controlled production canary.

Until all gates pass, keep Papers production NO-GO and do not install the
P0-specific scripts, numeric identities or test credentials as services.

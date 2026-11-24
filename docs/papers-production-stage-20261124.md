# Papers production composition and staged host — 2026-11-24

Production remains disabled. This is a candidate deployment, not a completed
production activation.

## Verified results

- Live timeout regression: gVisor exit 124; explicit sandbox teardown
  verified; harness exited 0 and cleaned its files and mounts.
- Previous resource-hardening branch: complete Go build and Swagger
  consistency passed.
- New production composition: default-off API, distinct role credentials,
  scoped snapshot, environment and PDF gates, non-root Worker, root Launcher
  restricted to a separate read-only DB identity and pinned signed environment.
- Real isolated PostgreSQL verified column-level permissions, denied immutable
  input mutation, deletion and unrelated-table reads, accepted Worker claim
  and heartbeat, and rejected a broadened role. Runtime and gateway tests passed.
- Candidate service executable and web composition built successfully.
- Five dedicated nologin identities created; no sudo, docker or wheel membership.
- Executable, scope helper, SHA-512-verified gVisor 20261019.0 installed under
  /opt/atoman/papers/bin. Six candidate units and inactive backend drop-in
  staged under /opt/atoman/papers/systemd; systemd-analyze verify returned 0.
- Runtime SHA-256: 5b4016fa43a9d35835253925cb6d05d3e42fdb9eb8bcd426c8b25444090e93e6
- Scope helper SHA-256: 11b7f0666fafd7a0437a243fc864c5a2d2a4db5fd996f8d07e1963a542c6b5f3
- Candidate service SHA-256: c4e39d723b493afc4edb9f0947c509653a691ada29ac824dbd7a7de43a1d6445
- Production MinIO uses the same image ID as the isolated IAM test.
- Approval marker absent; candidate units not active; live Atoman HTTP 200.

## Outstanding activation gates

1. Complete verified PostgreSQL restore, currently rebuilding indexes.
2. Provision a new private Papers bucket, scoped IAM credentials, storage quota,
   versioning, COMPLIANCE retention and lifecycle; test actual cross-prefix
   access denial. Do not alter the shared Atoman bucket.
3. Publish pinned normalized TeX archive via the separate environment gateway
   with independent signing-key custody; install verified reader trust roots.
4. Apply reviewed migrations and dedicated database roles after backup
   verification.
5. Validate actual candidate systemd profiles with a disposable fixture,
   including credential access, mount visibility across launcher restart,
   cancellation, recovery, signed archive fetch and authenticated PDF download.
   Syntax validation alone is insufficient.
6. Release scoped frontend/backend changes, verify rollback, then install
   and start units and enable invited production users after acceptance.

No production DB data, existing application services or live storage policy
was modified during staging.

# Atoman Papers: owner-scoped compile API (2026-10-28)

## Scope and change control

Branch: feat/papers-compile-api-slice-20261028; based on 8ab4340.
This is a **non-production, fail-closed API integration slice**, not
approval to run arbitrary TeX or deploy the P0 runtime.

Atoman's production composition continues to construct the Papers module
via NewService(db). CompileRuntime is nil by default and no public HTTP
request can inject a publisher, trusted root or environment digest.
Compile actions therefore report 503 papers.compile_disabled unless a
separately reviewed composition root injects the trusted dependencies.

## Routes, authentication and response contract

All routes use the existing Atoman session authentication, active Papers
invitation and private project ownership checks.

- GET /api/v1/papers/projects/:id/compile-capability
  returns enabled=false and pdf_available=false by default.
- POST /api/v1/papers/projects/:id/compiles
  accepts only base_revision and idempotency_key.
  On a fully wired isolated runtime, creates/reuses a persistent queue job.
- GET /api/v1/papers/projects/:id/compiles
  returns the latest ten owner-scoped jobs.
- GET /api/v1/papers/projects/:id/compiles/:jobId
  returns a sanitized public status projection.
- DELETE /api/v1/papers/projects/:id/compiles/:jobId
  requests cancellation through the persistent queue, never directly kills
  a sandbox.
- GET /api/v1/papers/projects/:id/compiles/:jobId/pdf
  verifies owner and job access but currently returns 503
  papers.pdf_persistence_pending. This is INTENTIONAL.

The status projection excludes worker ID, lease token, SandboxID, publisher
keys, queue internal fencing state and storage key paths. No arbitrary
environment, source digest, filesystem path or signed-release reference may
be chosen by the HTTP client.

## Submission flow and isolation

1. Require an authenticated, actively invited project owner.
2. Check for a previously accepted job by owner, project and idempotency
   key, returning the same immutable job even if edits occurred afterward.
3. Capture all project files inside a PostgreSQL transaction under a
   SHARE project-row lock. A PostgreSQL COUNT/SUM(octet_length) guard
   rejects more than 64 files or more than 4 MiB before loading the content.
   Editor writes lock the same project row FOR UPDATE.
4. Require the requested base revision, verified SHA256 of every file,
   the exact main.tex main_file_id, and P0 Snapshot.Validate constraints
   (<=64 files, <=4 MiB contents and conservative ASCII filename allowlist).
   Unsupported .md/.latexmkrc or Unicode paths are explicitly rejected 422,
   NEVER silently discarded.
5. After the database transaction commits, load and verify the operator-
   pinned signed TeX release via TrustedReleaseReader. No user override.
6. Publish the validated source snapshot with a 20-second request-scoped
   timeout and require its content-derived SHA256 digest.
7. Recompute the canonical source manifest SHA-256 and reject a publisher
   returning any different digest, even if it matches the digest syntax.
8. Submit to the PostgreSQL queue; it rechecks project head revision and
   invitation and atomically applies idempotency. Edits/deletions between
   snapshot publication and queueing cause a safe rejection rather than a
   job that compiles mismatched sources.
9. The existing queue retains its global one-heavyweight-job lease,
   worker fencing, recovery and cancellation semantics.

The test suite uses a disposable PostgreSQL instance and a unit-test-only
local Publisher/Release shim to exercise this HTTP flow. Those shims NEVER
enable real production routes and are NOT evidence of production signing-key
custody. The genuine Ed25519/S3/gVisor sequence was accepted separately
in the earlier P0 isolated test.

## Explicitly unfinished / NO-GO

- No production-approved Snapshot Publisher, signed-release reader, queue
  principal or service credentials are wired into NewService(db).
- The Worker validates temporary PDF outputs but has NOT yet atomically
  persisted an owner/job/digest-bound immutable PDF to private storage.
  Therefore there is no real authenticated PDF download/preview.
- No production user quota, rate limiting, template compatibility matrix,
  per-user concurrency scheduling, result retention/GC or audit dashboard.
- Existing project file API permits more formats than current P0 snapshots,
  so compile validation must return a clear 422 and later extend the
  trusted manifest format rather than silently omit files.
- Full build, swagger and isolated tests must pass before any merge.
  No migration was executed on a production DB.

## Tests

    GO_TEST_PARALLELISM=1 bash scripts/test-isolated.sh ./internal/modules/papers ./internal/platform/papersqueue
    GOFLAGS=-race GO_TEST_PARALLELISM=1 bash scripts/test-isolated.sh ./internal/modules/papers ./internal/platform/papersqueue
    go vet ./...
    go build ./...
    make swagger-check

Even a green test run does NOT authorize enabling the production compiler.

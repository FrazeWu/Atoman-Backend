# Backend Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix all confirmed backend review findings across owner bootstrap, OPML import, reading-list API behavior, Swagger contracts, and local generated-file ignore rules.

**Architecture:** Apply TDD per issue, starting with production safety and data consistency risks before documentation and test-hardening work. Keep changes surgical in existing files, avoiding refactors beyond the failing behavior.

**Tech Stack:** Go, Gin, GORM, SQLite tests via `internal/testdb`, Swagger JSON/docs generated from Go annotations.

---

### Task 1: Owner bootstrap safety

**Files:**
- Modify: `cmd/start_server/main.go`
- Modify: `cmd/start_server/main_test.go`
- Modify: `internal/service/owner_bootstrap_service.go`

- [ ] Add failing tests showing existing owner password is not reset by default and other owners are not demoted by startup bootstrap.
- [ ] Add failing test showing partial `OWNER_*` env config logs/skips instead of fataling startup.
- [ ] Change startup bootstrap to only create missing owner by default.
- [ ] Add explicit update path only behind a new env flag if needed.
- [ ] Run `GOCACHE="$PWD/.tmp/go-build" go test ./cmd/start_server ./internal/service`.

### Task 2: OPML URL validation and import consistency

**Files:**
- Modify: `internal/handlers/feed_handler.go`
- Modify: `internal/handlers/feed_handler_test.go`

- [ ] Add failing tests for `ImportGlobalOPML` rejecting relative URL, `file://`, and non-http schemes.
- [ ] Add failing test for admin success path through real `SetupFeedRoutes` + JWT + `AdminMiddleware`.
- [ ] Add failing test for duplicate URL import in one OPML counting as reused rather than failed.
- [ ] Validate `xmlUrl` inside `importFeedSourceFromURL` using trimmed absolute `http/https` URL.
- [ ] Handle duplicate hash create conflict by reloading existing source instead of returning failure.
- [ ] Run `GOCACHE="$PWD/.tmp/go-build" go test ./internal/handlers -run 'TestImport(GlobalOPML|OPML)'`.

### Task 3: Reading-list API contract

**Files:**
- Modify: `internal/modules/feed/http.go`
- Modify: `internal/modules/feed/repo.go`
- Modify: `internal/modules/feed/service.go`
- Modify: `internal/modules/feed/service_test.go`

- [ ] Add failing test that `ListReadingList` response includes `page_size` or uses common `httpx.List` shape.
- [ ] Add failing test that deleting a missing reading-list item returns not found instead of success.
- [ ] Update list response to common pagination shape.
- [ ] Update delete path to distinguish missing item from successful delete.
- [ ] Run `GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/feed`.

### Task 4: Swagger and generated docs alignment

**Files:**
- Modify: `internal/modules/blog/http.go`
- Modify: `internal/handlers/feed_handler.go`
- Modify: `docs/swagger.json`
- Modify if generated: `docs/docs.go`, `docs/swagger.yaml`

- [ ] Update or add Swagger annotations for modular `POST /api/v1/blog/posts` contract.
- [ ] Add Swagger annotations for admin `POST /api/v1/feed/sources/opml/import`.
- [ ] Regenerate or manually synchronize Swagger artifacts consistently.
- [ ] Verify `docs/swagger.json` contains no old `/api/*` path keys and includes `/api/v1/feed/sources/opml/import`.

### Task 5: Local generated files ignore rule

**Files:**
- Modify: `.gitignore`

- [ ] Restore `.codegraph/` ignore unless a narrower tracked allowlist is explicitly required.
- [ ] Confirm `.codegraph/` no longer appears as untracked in `git status --short`.

### Task 6: Final verification

**Files:**
- No code changes expected.

- [ ] Run `GOCACHE="$PWD/.tmp/go-build" go test ./...`.
- [ ] Run `GOCACHE="$PWD/.tmp/go-build" go build ./...`.
- [ ] Run scans for `Group("/api")` and old Swagger `/api/*` path keys.
- [ ] Clean `.tmp` Go cache.
- [ ] Report remaining concerns, if any.

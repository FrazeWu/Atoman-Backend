# Stable Feed Fulltext Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep noisy external RSS sources clean by rejecting login-wall or shell-like fulltext results and automatically falling back to RSS summaries.

**Architecture:** Add a conservative fulltext quality gate after sanitization and before success persistence. Introduce a `login_wall_detected` error code and treat repeated extraction-quality failures as a source-level signal to disable future fulltext fetches. Existing RSS summaries remain the fallback display content.

**Tech Stack:** Go, GORM, PostgreSQL models, built-in HTML parser, existing fulltext worker.

---

### Task 1: Detect Login-Wall Fulltext

**Files:**
- Modify: `internal/service/fulltext_types.go`
- Modify: `internal/service/fulltext_extractor.go`
- Test: `internal/service/fulltext_extractor_test.go`

- [ ] Add `FullTextErrorLoginWallDetected = "login_wall_detected"`.
- [ ] Add a failing extractor test with a 36Kr-like page where a long login prompt surrounds little or no article body.
- [ ] Implement a conservative text quality gate that rejects common login/app-wall phrases before returning success.
- [ ] Run `go test ./internal/service -run 'TestExtractAndSanitizeFullText'`.

### Task 2: Auto-Disable Repeated Bad Sources

**Files:**
- Modify: `internal/service/fulltext_worker.go`
- Test: `internal/service/fulltext_worker_test.go`

- [ ] Add a failing worker test showing a source with repeated login-wall failures is set to `full_text_enabled=false`.
- [ ] Implement source auto-disable for repeated non-recoverable extraction-quality failures.
- [ ] Keep individual item state as failed/retry according to existing retry policy.
- [ ] Run `go test ./internal/service -run 'Test.*FullText'`.

### Task 3: Verify Backend

- [ ] Run `go test ./internal/service`.
- [ ] Run `go build ./...`.

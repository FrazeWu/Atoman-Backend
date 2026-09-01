# Spec: Subscription Health Operations and Recovery

## Objective

Provide an operational recovery loop for external RSS subscriptions. A user can identify unhealthy sources, inspect recent fetch failures and recoveries, retry an affected source, and receive one actionable alert when a source remains unavailable.

## Capability Map

| Module | Responsibility | Depends on |
| --- | --- | --- |
| source-diagnostics | Persist RSS fetch failures and recovery records; return owner-scoped history | existing fetch state |
| recovery-notifications | Create one alert after the third consecutive failure and clear it on recovery | source-diagnostics |
| operations-ui | Summarize source health and expose per-source diagnostic history and retry | source-diagnostics |

Build order: source-diagnostics -> recovery-notifications -> operations-ui.

## Commands

- Backend focused tests: `go test ./internal/service ./internal/modules/feed`
- Backend build: `go build ./...`
- Swagger: `make swagger`
- Frontend focused tests: `bun run test:unit -- tests/unit/components/feed/SubscriptionManageSheet.spec.ts tests/unit/components/SubscriptionManageSheet.sync.spec.ts tests/unit/stores/feed.spec.ts`
- Frontend type check: `bun run type-check`
- Frontend build: `bun run build`

## Decisions

- The established RSS retry and backoff policy remains authoritative.
- A persistent incident begins on the third consecutive failure.
- Alerts are per active subscription recipient and feed source. A successful fetch clears prior active alerts so a later incident can alert again.
- Owner diagnostics expose only `rss_fetch_failure` and `rss_fetch_recovered` records, ordered newest first and capped at 20 entries.
- The operations view uses existing `FeedSource` scheduler fields for its health summary and loads history only after explicit user request.

## Boundaries

- Always: preserve RSS request security, validate ownership, add regression tests, regenerate Swagger for API changes.
- Ask first: database schema changes or dependency additions.
- Never: make direct HTTP health requests, weaken SSRF protections, or modify baseline main worktrees.

## Success Criteria

- A failed RSS fetch persists a diagnostic with error code, message, and attempt count.
- A later successful fetch persists a recovery diagnostic and clears the active incident notification.
- The third consecutive failed fetch creates no more than one active system notification per subscribed user and source.
- Only the subscription owner can read the recent RSS diagnostics.
- The manage sheet summarizes healthy, retrying, blocked, and failed sources; it can display recent diagnostics and retains the existing retry action.

## Plan: Subscription Priority Inbox

## Overview

Add a user-owned subscription priority and an opt-in Today inbox. The feature extends the existing subscription update and timeline contracts; it does not alter the chronological feed or use recommendation infrastructure.

## Task List

### Task 1: Persist and validate priority

- [x] Add the `high | normal | low` priority field with a `normal` default, a compatible migration, and owner-scoped update validation.
- [x] Verify defaulting, valid updates, and rejected values in feed HTTP tests.

### Task 2: Serve deterministic Today ranking

- [x] Extend timeline query and response DTOs with priority mode and per-item reason.
- [x] Rank unread subscribed items by priority, publication time, and stable identity; enforce the 20-item Today cap.
- [x] Verify priority ordering, cap, reasons, and unchanged chronological behavior.

### Task 3: Expose subscription priority management

- [x] Add priority to shared frontend subscription types and the management sheet's update payload and control.
- [x] Verify the selected value is rendered and emitted for an owned source.

### Task 4: Add the Today inbox mode

- [x] Add a compact mode selector to the timeline toolbar and carry priority mode through the controller query.
- [x] Render Today reason labels without changing default timeline interactions.
- [x] Verify query construction, mode switching, and reason rendering.

## Checkpoint

- [x] Backend focused tests and `go build ./...` pass.
- [x] Swagger is regenerated for API changes.
- [x] Frontend focused tests, type check, and build pass.
- [x] LSP diagnostics and `git diff --check` are clean.

## Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| Priority sorting changes the default reader flow | Activate only through explicit `sort=priority`; retain chronological mode as the default. |
| A source maps to multiple content kinds | Assign the maximum matching subscription priority and use stable tie-breaking. |
| Large unread backlogs make Today unbounded | Force unread-only semantics and cap Today at 20 server-side. |

## Plan: Source Favicon Fallback

## Overview

Render a source favicon only after an explicit feed cover is absent or fails. The favicon URL derives from the existing RSS URL origin, because the current external-source dataset has no persisted `site_url` values.

## Architecture Decisions

- Candidate order is feed cover, derived HTTP(S) `<origin>/favicon.ico`, then title initial.
- Favicon URLs are derived in the existing frontend source-presentation utility. No network request, source mutation, third-party favicon provider, or database backfill is introduced.
- A failed image advances one candidate at a time, so a broken cover does not suppress an available favicon.

## Task List

### Task 1: Derive a safe favicon candidate

- [x] Add a URL helper that accepts only valid HTTP(S) source URLs and returns their origin favicon path.
- [x] Verify invalid, non-HTTP(S), and empty values produce no candidate.

### Task 2: Apply candidate fallback to source cards

- [x] Update the source identity card to try cover, favicon, then initial.
- [x] Verify an image error advances to the next candidate and resets when the source changes.

### Task 3: Apply candidate fallback to source detail

- [x] Update the source articles sheet to use the same candidate order.
- [x] Verify both failed-image transitions and the existing title-initial state.

## Checkpoint: Completion

- [x] Focused source-avatar tests, type check, and production build pass.
- [x] `git diff --check` is clean and no fallback URL is persisted.

## Risks and Mitigations

| Risk | Mitigation |
| --- | --- |
| An RSS host differs from the publisher host | Preserve the explicit cover first; a failed or generic favicon still falls back to the initial. |
| A favicon is unavailable or blocked | Advance to the existing title-initial fallback through the image error handler. |
| HTTP favicon is blocked on an HTTPS page | Preserve the source URL scheme and fall back safely when the browser rejects it. |

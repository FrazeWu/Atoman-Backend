# Subscription Health Operations Tasks

## Task 1: Record fetch incidents

- [x] Persist `rss_fetch_failure` on each failed RSS fetch and `rss_fetch_recovered` after recovery.
- [x] Verify service tests assert diagnostic content and recovery state.

## Task 2: Notify persistent incidents

- [x] Create a single system notification for active subscribers at three consecutive failures.
- [x] Clear that incident notification after recovery.
- [x] Verify repeated failure does not duplicate alerts.

## Task 3: Expose owner-scoped history

- [x] Add a protected diagnostics endpoint for a subscription's recent RSS fetch records.
- [x] Verify owner access, non-owner rejection, filtering, ordering, and API documentation.

## Task 4: Present operations state

- [x] Add health summary counts and on-demand diagnostic history to the subscription manage sheet.
- [x] Keep retry as the sole active recovery action and route incident notifications to the manage sheet.
- [x] Verify component, store, route, type, and visual-state contracts.

## Checkpoint: Completion

- [x] Focused tests, backend build, Swagger generation, frontend type check and build pass.
- [x] Diff and diagnostics are clean; no user-owned untracked file is staged.

## Subscription Priority Inbox Tasks

## Task 1: Persist and validate priority

- [x] Add a defaulted subscription priority and reject unknown values.
- [x] Verify default, valid update, and invalid update behavior in backend tests.

## Task 2: Serve the Today inbox

- [x] Rank unread subscribed items deterministically and return an explainable reason.
- [x] Enforce the Today cap while keeping chronological requests unchanged.
- [x] Verify response ordering, cap, reasons, and compatibility.

## Task 3: Manage and consume priority

- [x] Add source-priority selection to subscription management.
- [x] Add a chronology/Today mode selector and render ranking reasons.
- [x] Verify component events, controller query parameters, and the visible feed state.

## Checkpoint: Completion

- [x] Focused backend and frontend tests pass.
- [x] Backend build, Swagger generation, frontend type check, and frontend build pass.
- [x] Diff and diagnostics are clean; no user-owned untracked file is staged.

## Source Favicon Fallback Tasks

## Task 1: Derive a safe favicon candidate

- [x] Derive an HTTP(S) favicon URL from the RSS source origin without persisting it.
- [x] Verify empty, malformed, and unsupported URLs return no candidate.

## Task 2: Add source-card fallback order

- [x] Render explicit cover, then favicon, then title initial in source identity cards.
- [x] Verify the candidate sequence and source-change reset behavior.

## Task 3: Add source-detail fallback order

- [x] Render the same candidate sequence in the source article sheet.
- [x] Verify cover and favicon failures each reach the correct fallback.

## Checkpoint: Completion

- [x] Focused source-avatar tests, type check, build, and diff check pass.
- [x] No source record is mutated and no third-party favicon service is introduced.

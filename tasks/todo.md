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

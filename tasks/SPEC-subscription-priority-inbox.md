# Spec: Subscription Priority Inbox

## Objective

Help signed-in users decide what to read first without changing the existing chronological timeline. Users assign each subscription `high`, `normal`, or `low` priority and can open a capped "Today" inbox containing their highest-priority unread items with a deterministic explanation for each result.

## Commands

- Backend focused tests: `go test ./internal/modules/feed`
- Backend build: `go build ./...`
- Backend API documentation: `make swagger`
- Frontend focused tests: `bun run test:unit -- tests/unit/composables/feed/useFeedTimelineController.spec.ts tests/unit/components/feed/FeedTimelineToolbar.spec.ts tests/unit/components/SubscriptionManageSheet.spec.ts tests/unit/views/feed/FeedView.spec.ts`
- Frontend type check: `bun run type-check`
- Frontend build: `bun run build`

## Contract

- `subscriptions.priority` is a string enum: `high`, `normal`, or `low`; existing and newly created subscriptions default to `normal`.
- `PUT /api/v1/feed/subscriptions/:id` accepts a validated optional `priority` field.
- `GET /api/v1/feed/timeline?sort=priority` is a signed-in, opt-in Today inbox. It includes unread items only, returns at most 20 items on the first page, and preserves the existing chronological response for every other query.
- Today ranking is deterministic: subscription priority descending, then publication time descending, then a stable item identity tie-breaker. Sources without a subscription priority, including followed users, are `normal`.
- Each Today item exposes a machine-readable `priority_reason` and the UI renders the corresponding concise label. No ranking model, AI service, new crawler, or behavior-tracking schema is introduced.

## Boundaries

- Always: preserve the current default timeline API and visual flow; validate user-owned updates; add backend and frontend regression tests; regenerate Swagger for the API change.
- Ask first: additional priority tiers, user-configurable ranking rules, notifications, AI summaries, or a new analytics schema.
- Never: alter read state while loading Today, hide items from the chronological timeline, or touch another task worktree.

## Success Criteria

- Existing subscriptions receive and return `normal` priority by default.
- A user can set an owned subscription to `high`, `normal`, or `low`; invalid values are rejected without changing data.
- The Today inbox contains only unread content, caps the response at 20, ranks `high` before `normal` before `low`, and explains the result.
- Chronological timeline requests retain their current sort order and pagination.
- The management sheet exposes priority selection and the timeline toolbar switches between chronological and Today modes without changing source, search, or duplicate filters.

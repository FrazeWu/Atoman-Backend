# Blog Handler Retirement Design

**Date:** 2026-06-16
**Status:** Draft for review

## Goal

Retire the legacy post-specific blog handler path in `internal/handlers/blog_post_handler.go` and make `internal/modules/blog/` the single source of truth for blog post routes, behavior, and Swagger contracts.

## Current State

The codebase is in a half-migrated state:

- `internal/modules/blog/http.go` already owns `POST /api/v1/blog/posts` and `PUT /api/v1/blog/posts/:id/rating`.
- `internal/handlers/blog_post_handler.go` still owns the rest of the post-oriented routes, including list/detail, drafts, publish/unpublish, pin/unpin, and collection operations.
- `internal/app/router.go` currently registers both:
  - `blog.RegisterRoutes(group.Group("/blog"), ...)`
  - `handlers.SetupBlogPostRoutes(r, db)`
- Swagger generation currently sees overlapping blog-post route declarations, which creates duplicate contract sources and makes route ownership unclear.

So the problem is not just “delete one old file”. The system first needs complete route coverage in the modular blog package, then the legacy handler can be safely removed.

## Decision

Use a full retirement approach with a safety-first migration sequence:

1. Inventory everything still provided by `SetupBlogPostRoutes`.
2. Compare that inventory with current `internal/modules/blog/` coverage.
3. Move or recreate all still-needed post routes in the modular package.
4. Verify route parity with tests.
5. Remove `SetupBlogPostRoutes` registration.
6. Delete or fully retire `internal/handlers/blog_post_handler.go`.
7. Regenerate Swagger so blog-post docs come from one place only.

## Scope

### In scope

- Blog **post** routes and behavior currently tied to `SetupBlogPostRoutes`
- Route registration cleanup in `internal/app/router.go`
- Modular blog route/test additions under `internal/modules/blog/`
- Swagger source cleanup for `/api/v1/blog/posts*`

### Out of scope

- Blog channel handlers
- Blog interaction handlers
- Blog upload handlers
- Non-post blog APIs unless they are discovered to be hard dependencies of post retirement

## Expected Route Ownership After Migration

After this work, all post-oriented routes should be owned by `internal/modules/blog/`.

That likely includes at least:

- `GET /api/v1/blog/posts`
- `POST /api/v1/blog/posts`
- `GET /api/v1/blog/posts/:id`
- `PUT /api/v1/blog/posts/:id`
- `DELETE /api/v1/blog/posts/:id`
- `GET /api/v1/blog/posts/drafts`
- `POST /api/v1/blog/posts/:id/publish`
- `POST /api/v1/blog/posts/:id/unpublish`
- `POST /api/v1/blog/posts/:id/pin`
- `POST /api/v1/blog/posts/:id/unpin`
- `POST /api/v1/blog/posts/:id/collections`
- `DELETE /api/v1/blog/posts/:id/collections/:collection_id`
- `PUT /api/v1/blog/posts/:id/rating`

If implementation confirms that some non-`/posts*` draft endpoints are still coupled to the legacy file, those must either move too or stay explicitly outside the retirement boundary. That decision should be made from code evidence, not assumption.

## Migration Strategy

### Phase 1: Inventory and parity map

Create a concrete matrix of:

- Route path
- HTTP method
- Current owner file
- Target owner file
- Existing tests
- Missing tests

This is the control document for safe retirement.

### Phase 2: Fill modular gaps

For each legacy-only post route:

- add the route to `internal/modules/blog/http.go`
- implement or delegate behavior in modular blog service/repo files
- keep behavior compatible with current clients unless an intentional change is required
- add focused route/service tests before removing the legacy implementation

### Phase 3: Switch ownership

Once the parity map is complete and tests pass:

- remove `handlers.SetupBlogPostRoutes(r, db)` from `internal/app/router.go`
- remove the no-longer-used post handlers and Swagger annotations from `internal/handlers/blog_post_handler.go`
- if the file becomes empty of real responsibility, delete it entirely

### Phase 4: Contract cleanup

Regenerate Swagger artifacts and confirm:

- blog post paths appear once
- contracts point to modular DTOs and responses
- no duplicate `POST /api/v1/blog/posts` source remains

## Testing Strategy

Use TDD for each migrated route group.

### Required verification

- Route-level tests for every migrated endpoint
- Behavior tests for authorization, ownership, and not-found paths
- Swagger verification for blog post paths after regeneration
- Full repository verification:
  - `go test ./...`
  - `go build ./...`

### Specific regression targets

- create post still works through modular route
- legacy list/detail behavior is preserved after migration
- draft listing remains available
- publish/unpublish still enforce ownership
- pin/unpin still enforce ownership
- collection add/remove still enforce ownership and channel constraints
- no route disappears during handler retirement

## Risks

### Main risk

Deleting the legacy handler too early will silently remove still-live blog post routes.

### Secondary risks

- Swagger may continue to compile against stale old DTOs if annotation sources are not fully removed.
- Tests may only prove route existence rather than route semantics unless ownership/forbidden/not-found cases are covered.
- Some helper functions in `blog_post_handler.go` may be reused by multiple routes, so retirement may require moving shared logic rather than deleting it inline.

## Success Criteria

The retirement is complete when all of the following are true:

1. `internal/app/router.go` no longer registers `SetupBlogPostRoutes`.
2. Blog post routes are owned by `internal/modules/blog/` only.
3. `internal/handlers/blog_post_handler.go` is either deleted or no longer contains active post-route responsibilities.
4. Swagger for `/api/v1/blog/posts*` is generated from one route source only.
5. `go test ./...` passes.
6. `go build ./...` passes.

## Notes for implementation planning

The implementation plan should break this into small, test-first steps by route cluster, not one giant rewrite. A safe order is:

1. inventory and parity tests
2. read routes
3. mutate routes
4. draft/publish/pin flows
5. collection operations
6. router switch
7. legacy file deletion
8. Swagger regeneration and final verification

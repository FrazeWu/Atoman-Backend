# Blog Handler Retirement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire `internal/handlers/blog_post_handler.go` by moving all remaining blog post routes into `internal/modules/blog/` and leaving Swagger with a single route source.

**Architecture:** Keep `internal/modules/blog/` as the single owner of blog post APIs. Migrate route clusters one at a time with TDD, then remove `SetupBlogPostRoutes` from `internal/app/router.go`, delete legacy post handler code, and regenerate Swagger only after route parity is verified.

**Tech Stack:** Go, Gin, GORM, existing `internal/modules/blog` service/repo pattern, `internal/app/router_test.go`, Swagger via `swag init -g cmd/start_server/main.go -o docs`.

---

### Task 1: Build route parity inventory and failing router coverage

**Files:**
- Modify: `internal/app/router_test.go:65-115`
- Test: `internal/app/router_test.go`

- [ ] **Step 1: Write failing route-parity assertions for legacy blog post endpoints**

Add route existence checks beside `TestRegisterV1RoutesMountsBlogCreatePost` for the endpoints still owned by `SetupBlogPostRoutes`:

```go
w = httptest.NewRecorder()
req = httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts", nil)
r.ServeHTTP(w, req)
if w.Code == http.StatusNotFound {
    t.Fatalf("expected blog posts list route to be mounted, got 404: %s", w.Body.String())
}

w = httptest.NewRecorder()
req = httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts/00000000-0000-0000-0000-000000000001", nil)
r.ServeHTTP(w, req)
if w.Code == http.StatusNotFound {
    t.Fatalf("expected blog post detail route to be mounted, got 404: %s", w.Body.String())
}

w = httptest.NewRecorder()
req = httptest.NewRequest(http.MethodPut, "/api/v1/blog/posts/00000000-0000-0000-0000-000000000001", bytes.NewBufferString(`{"title":"x","content":"y"}`))
req.Header.Set("Content-Type", "application/json")
r.ServeHTTP(w, req)
if w.Code == http.StatusNotFound {
    t.Fatalf("expected blog post update route to be mounted, got 404: %s", w.Body.String())
}
```

Also add checks for:
- `DELETE /api/v1/blog/posts/:id`
- `GET /api/v1/blog/posts/drafts`
- `GET /api/v1/blog/drafts?context_key=test`
- `PUT /api/v1/blog/drafts`
- `DELETE /api/v1/blog/drafts?context_key=test`
- `POST /api/v1/blog/posts/:id/publish`
- `POST /api/v1/blog/posts/:id/unpublish`
- `POST /api/v1/blog/posts/:id/pin`
- `POST /api/v1/blog/posts/:id/unpin`
- `POST /api/v1/blog/posts/:id/collections`
- `DELETE /api/v1/blog/posts/:id/collections/:collection_id`

- [ ] **Step 2: Run router test to verify baseline is green before switch**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/app -run TestRegisterV1RoutesMountsBlogCreatePost
```

Expected: PASS, proving the legacy handler is still supplying those routes today.

- [ ] **Step 3: Commit parity test expansion**

```bash
git add internal/app/router_test.go
git commit -m "test: expand blog route parity coverage"
```

### Task 2: Migrate read routes into modular blog HTTP

**Files:**
- Modify: `internal/modules/blog/http.go`
- Modify: `internal/modules/blog/http_test.go`
- Test: `internal/modules/blog/http_test.go`

- [ ] **Step 1: Write failing modular HTTP tests for list and detail routes**

Add tests that register `internal/modules/blog.RegisterRoutes` only and hit:
- `GET /api/v1/blog/posts`
- `GET /api/v1/blog/posts/:id`

Use the existing `newBlogHTTPRouter` helper so the tests prove the modular package owns the route itself.

Example structure:

```go
func TestRegisterRoutesListPostsReturnsPublishedPosts(t *testing.T) {
    service, db, user := newBlogHTTPTestService(t)
    post := model.Post{UserID: user.ID, Title: "Published", Content: "body", Status: "published", Visibility: "public"}
    if err := db.Create(&post).Error; err != nil {
        t.Fatalf("create post: %v", err)
    }

    r := newBlogHTTPRouter(service, &user)
    w := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/api/v1/blog/posts", nil)
    r.ServeHTTP(w, req)

    if w.Code != http.StatusOK {
        t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
    }
}
```

- [ ] **Step 2: Run tests to verify they fail on missing routes**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog -run 'TestRegisterRoutes(ListPostsReturnsPublishedPosts|GetPostReturnsDetail)'
```

Expected: FAIL with 404 or missing route behavior.

- [ ] **Step 3: Add minimal modular handlers for list and detail**

In `internal/modules/blog/http.go`, register and implement:

```go
group.GET("/posts", h.listPosts)
group.GET("/posts/:id", h.getPost)
```

Keep the logic behavior-compatible by moving or adapting the code paths from `handlers.GetPosts` and `handlers.GetPost`. Do not refactor semantics. Preserve visibility/ownership checks.

- [ ] **Step 4: Run tests to verify the new modular read routes pass**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog -run 'TestRegisterRoutes(ListPostsReturnsPublishedPosts|GetPostReturnsDetail)'
```

Expected: PASS.

- [ ] **Step 5: Commit read route migration**

```bash
git add internal/modules/blog/http.go internal/modules/blog/http_test.go
git commit -m "feat: move blog read routes to modular handler"
```

### Task 3: Migrate update and delete post routes

**Files:**
- Modify: `internal/modules/blog/http.go`
- Modify: `internal/modules/blog/http_test.go`
- Test: `internal/modules/blog/http_test.go`

- [ ] **Step 1: Write failing modular tests for update and delete routes**

Add tests for:
- `PUT /api/v1/blog/posts/:id`
- `DELETE /api/v1/blog/posts/:id`

Cover owner success and non-owner forbidden behavior.

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog -run 'TestRegisterRoutes(UpdatePost|DeletePost)'
```

Expected: FAIL due to missing route or behavior.

- [ ] **Step 3: Implement minimal modular update/delete handlers**

Register:

```go
group.PUT("/posts/:id", h.updatePost)
group.DELETE("/posts/:id", h.deletePost)
```

Move the exact ownership/channel/collection semantics from the legacy file. Reuse helper logic where possible, but keep the modular file authoritative.

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog -run 'TestRegisterRoutes(UpdatePost|DeletePost)'
```

Expected: PASS.

- [ ] **Step 5: Commit mutate route migration**

```bash
git add internal/modules/blog/http.go internal/modules/blog/http_test.go
git commit -m "feat: move blog update and delete routes to modular handler"
```

### Task 4: Migrate publish and pin state transitions

**Files:**
- Modify: `internal/modules/blog/http.go`
- Modify: `internal/modules/blog/http_test.go`
- Test: `internal/modules/blog/http_test.go`

- [ ] **Step 1: Write failing modular tests for publish/unpublish/pin/unpin**

Add tests for:
- `POST /api/v1/blog/posts/:id/publish`
- `POST /api/v1/blog/posts/:id/unpublish`
- `POST /api/v1/blog/posts/:id/pin`
- `POST /api/v1/blog/posts/:id/unpin`

Each test should verify status or pinned state changes for the owner and 403 for a non-owner.

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog -run 'TestRegisterRoutes(Post(Publish|Unpublish|Pin|Unpin))'
```

Expected: FAIL.

- [ ] **Step 3: Implement minimal transition handlers**

Register the four routes and port the legacy state-change logic into modular handlers.

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog -run 'TestRegisterRoutes(Post(Publish|Unpublish|Pin|Unpin))'
```

Expected: PASS.

- [ ] **Step 5: Commit state transition migration**

```bash
git add internal/modules/blog/http.go internal/modules/blog/http_test.go
git commit -m "feat: move blog publish and pin routes to modular handler"
```

### Task 5: Migrate draft and collection routes

**Files:**
- Modify: `internal/modules/blog/http.go`
- Modify: `internal/modules/blog/http_test.go`
- Test: `internal/modules/blog/http_test.go`

- [ ] **Step 1: Write failing modular tests for draft and collection routes**

Add tests for:
- `GET /api/v1/blog/posts/drafts`
- `GET /api/v1/blog/drafts`
- `PUT /api/v1/blog/drafts`
- `DELETE /api/v1/blog/drafts`
- `POST /api/v1/blog/posts/:id/collections`
- `DELETE /api/v1/blog/posts/:id/collections/:collection_id`

Use the same semantics already covered in legacy tests such as:
- `TestPutBlogDraftPersistsFollowersVisibility`
- collection ownership/channel constraint tests once migrated

- [ ] **Step 2: Run tests to verify they fail**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog -run 'TestRegisterRoutes((Get|Put|Delete)BlogDraft|GetDrafts|AddPostToCollection|RemovePostFromCollection)'
```

Expected: FAIL.

- [ ] **Step 3: Implement minimal modular draft and collection handlers**

Register and implement the missing draft and collection routes in `internal/modules/blog/http.go`, preserving payload shapes and validation behavior.

- [ ] **Step 4: Run tests to verify they pass**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog -run 'TestRegisterRoutes((Get|Put|Delete)BlogDraft|GetDrafts|AddPostToCollection|RemovePostFromCollection)'
```

Expected: PASS.

- [ ] **Step 5: Commit draft and collection migration**

```bash
git add internal/modules/blog/http.go internal/modules/blog/http_test.go
git commit -m "feat: move blog draft and collection routes to modular handler"
```

### Task 6: Switch router ownership to modular blog only

**Files:**
- Modify: `internal/app/router.go`
- Modify: `internal/app/router_test.go`
- Test: `internal/app/router_test.go`

- [ ] **Step 1: Remove legacy blog post route registration**

Edit `internal/app/router.go` to delete:

```go
handlers.SetupBlogPostRoutes(r, db)
```

Do not touch channel, interaction, or upload registrations.

- [ ] **Step 2: Run router parity test to verify modular ownership is complete**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/app -run 'TestRegisterV1RoutesMountsBlogCreatePost'
```

Expected: PASS, including all added route-parity assertions from Task 1.

- [ ] **Step 3: Commit router switch**

```bash
git add internal/app/router.go internal/app/router_test.go
git commit -m "refactor: retire legacy blog post route registration"
```

### Task 7: Delete legacy blog post handler and tests

**Files:**
- Delete: `internal/handlers/blog_post_handler.go`
- Delete: `internal/handlers/blog_post_handler_test.go`
- Test: `go test ./...`

- [ ] **Step 1: Delete the legacy handler and its legacy tests**

Remove the files entirely once route parity and router ownership are already proven.

- [ ] **Step 2: Run package tests to verify nothing still depends on them**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./internal/modules/blog ./internal/app ./internal/handlers
```

Expected: PASS.

- [ ] **Step 3: Commit legacy file deletion**

```bash
git add internal/handlers/blog_post_handler.go internal/handlers/blog_post_handler_test.go
git commit -m "refactor: delete legacy blog post handler"
```

### Task 8: Regenerate Swagger and verify single-source contracts

**Files:**
- Modify: `docs/swagger.json`
- Modify: `docs/swagger.yaml`
- Modify: `docs/docs.go`

- [ ] **Step 1: Regenerate Swagger**

Run:
```bash
swag init -g cmd/start_server/main.go -o docs
```

Expected: command succeeds and rewrites all three docs artifacts.

- [ ] **Step 2: Verify duplicate route warning is gone**

Run:
```bash
swag init -g cmd/start_server/main.go -o docs 2>&1 | rg 'declared multiple times' || true
```

Expected: no output.

- [ ] **Step 3: Verify blog post contract is single-sourced**

Run:
```bash
python3 - <<'PY'
import json
from pathlib import Path
data = json.loads(Path('docs/swagger.json').read_text())
print('/api/v1/blog/posts' in data.get('paths', {}))
print('/api/v1/blog/posts/{id}' in data.get('paths', {}))
print('/api/v1/blog/posts/drafts' in data.get('paths', {}))
PY
```

Expected:
```text
True
True
True
```

- [ ] **Step 4: Commit Swagger regeneration**

```bash
git add docs/swagger.json docs/swagger.yaml docs/docs.go
git commit -m "docs: regenerate blog swagger after handler retirement"
```

### Task 9: Final repository verification

**Files:**
- No functional code changes expected.

- [ ] **Step 1: Run full test suite**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go test ./...
```

Expected: PASS.

- [ ] **Step 2: Run full build**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go build ./...
```

Expected: PASS.

- [ ] **Step 3: Verify legacy registration and file are gone**

Run:
```bash
rg -n 'SetupBlogPostRoutes|blog_post_handler' internal --glob '*.go' --glob '*.md'
```

Expected: no results from active code paths; only historical docs/plans are acceptable.

- [ ] **Step 4: Clean temp build cache**

Run:
```bash
GOCACHE="$PWD/.tmp/go-build" go clean -cache
python3 - <<'PY'
import shutil
from pathlib import Path
p = Path('.tmp')
if p.exists():
    shutil.rmtree(p)
PY
```

Expected: `.tmp/` removed.

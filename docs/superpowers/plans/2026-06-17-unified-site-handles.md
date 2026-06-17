# Unified Site Handles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `u-*.atoman.org` and `c-*.atoman.org` with a shared `*.atoman.org` namespace where module names, usernames, and channel slugs cannot collide.

**Architecture:** The backend owns canonical namespace resolution and write-time collision checks. The frontend generates bare handle subdomain URLs and treats non-module subdomains as unresolved entities, preserving old `u-` and `c-` inputs only as compatibility aliases.

**Tech Stack:** Go, Gin, Gorm, SQLite/Postgres-compatible queries, Vue 3, Pinia, Vue Router, Vitest, Bun, Docker Compose.

---

## File Structure

- Create `Atoman-Backend/internal/service/site_namespace.go`: reserved module names, handle normalization, conflict checks, and user/channel resolver.
- Create `Atoman-Backend/internal/service/site_namespace_test.go`: unit tests for reserved names, user/channel conflicts, and resolution priority.
- Create `Atoman-Backend/internal/handlers/site_handler.go`: public `GET /api/v1/site/resolve/:handle` endpoint.
- Create `Atoman-Backend/internal/handlers/site_handler_test.go`: HTTP tests for module, user, channel, and unknown responses.
- Modify `Atoman-Backend/internal/app/router.go`: register site resolver routes under `/api/v1/site`.
- Modify `Atoman-Backend/internal/handlers/auth_handler.go`: reject usernames that collide with modules or channel slugs before creating a user.
- Modify `Atoman-Backend/internal/handlers/auth_handler_test.go`: registration conflict tests.
- Modify `Atoman-Backend/internal/handlers/blog_helpers.go` and `Atoman-Backend/internal/handlers/blog_channel_handler.go`: generate channel slugs that skip reserved names and usernames.
- Modify `Atoman-Backend/internal/handlers/blog_channel_handler_test.go`: channel create/update conflict tests.
- Modify `Atoman-Backend/internal/modules/blog/service.go`: default channel generation skips reserved names and usernames.
- Modify `Atoman-Backend/internal/modules/blog/service_test.go`: default channel slug conflict tests.
- Modify `Atoman-Frontend/src/router/siteContext.ts`: return `{ type: 'entity', handle }` for bare non-module subdomains and keep old prefixes as aliases.
- Modify `Atoman-Frontend/src/router/siteUrls.ts`: build `alice.atoman.org` and `design.atoman.org`, not `u-alice` or `c-design`.
- Modify `Atoman-Frontend/src/composables/useSubdomainNav.ts`: keep module default path behavior; entity subdomains do not map to module roots.
- Modify `Atoman-Frontend/src/api` or existing API URL helper file if needed: add `site.resolve(handle)` endpoint.
- Modify `Atoman-Frontend/tests/unit/router/siteContext.spec.ts`: update expected unified namespace behavior.
- Modify `Atoman-Frontend/tests/unit/router/siteUrls.spec.ts`: update URL builder assertions.
- Modify affected frontend component tests that still assert `?site=u-alice` or `u-alice.atoman.org`.

### Task 1: Backend Namespace Service

**Files:**
- Create: `Atoman-Backend/internal/service/site_namespace.go`
- Create: `Atoman-Backend/internal/service/site_namespace_test.go`

- [ ] **Step 1: Write failing namespace service tests**

Add tests covering:

```go
func TestSiteNamespaceReservedNames(t *testing.T) {
	db := setupServiceTestDB(t)
	ns := service.NewSiteNamespaceService(db)

	for _, name := range []string{
		"feed", "music", "blog", "forum", "debate", "timeline", "podcast", "video", "kanbo",
		"www", "api", "admin", "auth", "login", "register", "setting", "settings",
		"user", "users", "channel", "channels", "post", "posts", "collection", "collections",
		"article", "articles", "topic", "topics", "comment", "comments", "notification", "notifications",
		"inbox", "dm", "search", "explore", "upload", "static", "assets", "cdn", "status",
	} {
		result, err := ns.Resolve(context.Background(), name)
		require.NoError(t, err)
		require.Equal(t, "module", result.Type)
		require.Equal(t, name, result.Handle)
	}
}

func TestSiteNamespaceUserChannelConflictChecks(t *testing.T) {
	db := setupServiceTestDB(t)
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	channel := model.Channel{Name: "Design", Slug: "design", UserID: &user.UUID}
	require.NoError(t, db.Create(&channel).Error)

	ns := service.NewSiteNamespaceService(db)
	require.ErrorIs(t, ns.ValidateUsernameAvailable(context.Background(), "feed"), service.ErrSiteHandleReserved)
	require.ErrorIs(t, ns.ValidateUsernameAvailable(context.Background(), "design"), service.ErrSiteHandleTaken)
	require.ErrorIs(t, ns.ValidateChannelSlugAvailable(context.Background(), "music", nil), service.ErrSiteHandleReserved)
	require.ErrorIs(t, ns.ValidateChannelSlugAvailable(context.Background(), "alice", nil), service.ErrSiteHandleTaken)
}

func TestSiteNamespaceResolveUserChannelUnknown(t *testing.T) {
	db := setupServiceTestDB(t)
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	channel := model.Channel{Name: "Design", Slug: "design", UserID: &user.UUID}
	require.NoError(t, db.Create(&channel).Error)

	ns := service.NewSiteNamespaceService(db)
	userResult, err := ns.Resolve(context.Background(), "alice")
	require.NoError(t, err)
	require.Equal(t, "user", userResult.Type)
	require.Equal(t, "alice", userResult.Username)

	channelResult, err := ns.Resolve(context.Background(), "design")
	require.NoError(t, err)
	require.Equal(t, "channel", channelResult.Type)
	require.Equal(t, "design", channelResult.Slug)

	unknownResult, err := ns.Resolve(context.Background(), "missing")
	require.NoError(t, err)
	require.Equal(t, "unknown", unknownResult.Type)
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/service -run SiteNamespace -count=1
```

Expected: FAIL because `NewSiteNamespaceService`, `ErrSiteHandleReserved`, and `ErrSiteHandleTaken` do not exist.

- [ ] **Step 3: Implement namespace service**

Create a service with:

```go
type SiteResolution struct {
	Type     string `json:"type"`
	Handle   string `json:"handle"`
	Module   string `json:"module,omitempty"`
	Username string `json:"username,omitempty"`
	Slug     string `json:"slug,omitempty"`
}
```

Use these reserved names:

```go
var reservedSiteHandles = map[string]struct{}{
	"feed": {}, "kanbo": {}, "music": {}, "blog": {}, "forum": {},
	"debate": {}, "timeline": {}, "podcast": {}, "video": {},
	"www": {}, "api": {}, "admin": {}, "root": {}, "portal": {},
	"auth": {}, "login": {}, "logout": {}, "register": {}, "signup": {}, "signin": {},
	"account": {}, "accounts": {}, "setting": {}, "settings": {}, "profile": {}, "profiles": {},
	"user": {}, "users": {}, "member": {}, "members": {}, "channel": {}, "channels": {},
	"post": {}, "posts": {}, "article": {}, "articles": {}, "collection": {}, "collections": {},
	"topic": {}, "topics": {}, "comment": {}, "comments": {}, "bookmark": {}, "bookmarks": {},
	"notification": {}, "notifications": {}, "inbox": {}, "dm": {}, "message": {}, "messages": {},
	"search": {}, "explore": {}, "discover": {}, "upload": {}, "uploads": {}, "media": {},
	"song": {}, "songs": {}, "album": {}, "albums": {}, "artist": {}, "artists": {},
	"playlist": {}, "playlists": {}, "watch": {}, "episode": {}, "episodes": {},
	"subscription": {}, "subscriptions": {}, "rss": {}, "atom": {}, "feed-source": {},
	"help": {}, "about": {}, "terms": {}, "privacy": {}, "contact": {}, "support": {},
	"static": {}, "assets": {}, "cdn": {}, "status": {}, "health": {}, "metrics": {},
	"dev": {}, "test": {}, "stage": {}, "staging": {}, "prod": {}, "production": {},
}
```

Treat reserved names as website-owned handles, not only currently visible modules. Add a short code comment above the map explaining that new public feature names should be added here before launch.

Implement methods:

```go
func NewSiteNamespaceService(db *gorm.DB) *SiteNamespaceService
func (s *SiteNamespaceService) NormalizeHandle(raw string) (string, error)
func (s *SiteNamespaceService) Resolve(ctx context.Context, raw string) (SiteResolution, error)
func (s *SiteNamespaceService) ValidateUsernameAvailable(ctx context.Context, username string) error
func (s *SiteNamespaceService) ValidateChannelSlugAvailable(ctx context.Context, slug string, excludeChannelID *uuid.UUID) error
```

Normalize to lowercase trimmed values and accept only `^[a-z0-9][a-z0-9-]{1,29}$`.

- [ ] **Step 4: Run tests and verify pass**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/service -run SiteNamespace -count=1
```

Expected: PASS.

### Task 2: Backend Resolver Endpoint

**Files:**
- Create: `Atoman-Backend/internal/handlers/site_handler.go`
- Create: `Atoman-Backend/internal/handlers/site_handler_test.go`
- Modify: `Atoman-Backend/internal/app/router.go`

- [ ] **Step 1: Write failing handler tests**

Add tests that mount `app.RegisterV1Routes` and assert:

```go
GET /api/v1/site/resolve/feed
// 200 {"data":{"type":"module","handle":"feed","module":"feed"}}

GET /api/v1/site/resolve/alice
// 200 {"data":{"type":"user","handle":"alice","username":"alice"}}

GET /api/v1/site/resolve/design
// 200 {"data":{"type":"channel","handle":"design","slug":"design"}}

GET /api/v1/site/resolve/missing
// 404 {"error":"Site handle not found"}
```

- [ ] **Step 2: Run handler tests and verify failure**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/handlers ./internal/app -run 'SiteResolve|RegisterV1RoutesMountsSiteResolve' -count=1
```

Expected: FAIL with 404 route not found.

- [ ] **Step 3: Implement and register endpoint**

In `site_handler.go`:

```go
func SetupSiteRoutes(router *gin.Engine, db *gorm.DB) {
	group := router.Group("/api/v1/site")
	group.GET("/resolve/:handle", ResolveSiteHandle(db))
}

func ResolveSiteHandle(db *gorm.DB) gin.HandlerFunc {
	ns := service.NewSiteNamespaceService(db)
	return func(c *gin.Context) {
		result, err := ns.Resolve(c.Request.Context(), c.Param("handle"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid site handle"})
			return
		}
		if result.Type == "unknown" {
			c.JSON(http.StatusNotFound, gin.H{"error": "Site handle not found"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": result, "message": "ok"})
	}
}
```

Register `handlers.SetupSiteRoutes(r, db)` in `internal/app/router.go`.

- [ ] **Step 4: Run endpoint tests**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/handlers ./internal/app -run 'SiteResolve|RegisterV1RoutesMountsSiteResolve' -count=1
```

Expected: PASS.

### Task 3: Backend Write-Time Collision Checks

**Files:**
- Modify: `Atoman-Backend/internal/handlers/auth_handler.go`
- Modify: `Atoman-Backend/internal/handlers/auth_handler_test.go`
- Modify: `Atoman-Backend/internal/handlers/blog_helpers.go`
- Modify: `Atoman-Backend/internal/handlers/blog_channel_handler.go`
- Modify: `Atoman-Backend/internal/handlers/blog_channel_handler_test.go`
- Modify: `Atoman-Backend/internal/modules/blog/service.go`
- Modify: `Atoman-Backend/internal/modules/blog/service_test.go`

- [ ] **Step 1: Write failing auth tests**

Add tests that bypass Turnstile with test env and assert registration rejects:

```go
{"username":"feed","email":"feed-user@example.com","password":"secret123","verification_code":"123456"}
// 400, body contains "reserved"

{"username":"design","email":"design-user@example.com","password":"secret123","verification_code":"123456"}
// with existing channel slug "design": 400 or 409, body contains "already in use"
```

- [ ] **Step 2: Implement auth validation**

After JSON binding and before existing user lookup:

```go
input.Username = strings.ToLower(strings.TrimSpace(input.Username))
if err := service.NewSiteNamespaceService(db).ValidateUsernameAvailable(c.Request.Context(), input.Username); err != nil {
	c.JSON(http.StatusBadRequest, gin.H{"error": "Site handle is reserved or already in use"})
	return
}
```

- [ ] **Step 3: Write failing channel tests**

Add tests for authenticated create/update:

```go
POST /api/v1/blog/channels {"name":"Feed","slug":"feed"}
// 409, reserved

POST /api/v1/blog/channels {"name":"Alice Channel","slug":"alice"}
// with existing username alice: 409

PUT /api/v1/blog/channels/:id {"name":"Other","slug":"alice"}
// with existing username alice: 409
```

- [ ] **Step 4: Implement channel validation and generation**

Update `uniqueChannelSlug(db, base, excludeID)` and module blog service `uniqueChannelSlug(base)` so candidates are accepted only when:

```go
err := service.NewSiteNamespaceService(db).ValidateChannelSlugAvailable(ctx, candidate, excludeID)
```

For explicit `input.Slug`, preserve deterministic slugification but skip candidates that collide with reserved names or usernames.

- [ ] **Step 5: Run backend focused tests**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/service ./internal/handlers ./internal/modules/blog -run 'SiteNamespace|Register|Channel|DefaultChannel' -count=1
```

Expected: PASS.

### Task 4: Frontend Unified Subdomain URLs And Context

**Files:**
- Modify: `Atoman-Frontend/src/router/siteContext.ts`
- Modify: `Atoman-Frontend/src/router/siteUrls.ts`
- Modify: `Atoman-Frontend/tests/unit/router/siteContext.spec.ts`
- Modify: `Atoman-Frontend/tests/unit/router/siteUrls.spec.ts`
- Modify: frontend tests that assert `u-` or `c-` URLs.

- [ ] **Step 1: Write/update failing frontend tests**

Update assertions to:

```ts
expect(resolveSiteContext('alice.atoman.org')).toEqual({ type: 'entity', handle: 'alice' })
expect(resolveSiteContext('design.atoman.org')).toEqual({ type: 'entity', handle: 'design' })
expect(resolveSiteContext('u-alice.atoman.org')).toEqual({ type: 'entity', handle: 'alice', legacyType: 'user' })
expect(resolveSiteContext('c-design.atoman.org')).toEqual({ type: 'entity', handle: 'design', legacyType: 'channel' })
expect(userUrl('alice', 'https:', 'blog.atoman.org')).toBe('https://alice.atoman.org/')
expect(channelUrl('design', 'https:', 'blog.atoman.org')).toBe('https://design.atoman.org/')
expect(userUrl('alice', 'http:', 'localhost')).toBe('/?site=alice')
expect(channelUrl('design', 'http:', 'localhost')).toBe('/?site=design')
```

- [ ] **Step 2: Implement context change**

Change `SiteContext` to include:

```ts
| { type: 'entity'; handle: string; legacyType?: 'user' | 'channel' }
```

Make `contextFromLabel` return modules first, then:

```ts
if (label.startsWith('u-') && slugPattern.test(label.slice(2))) {
  return { type: 'entity', handle: label.slice(2), legacyType: 'user' }
}
if (label.startsWith('c-') && slugPattern.test(label.slice(2))) {
  return { type: 'entity', handle: label.slice(2), legacyType: 'channel' }
}
if (slugPattern.test(label)) {
  return { type: 'entity', handle: label }
}
return { type: 'unknown', subdomain: label }
```

- [ ] **Step 3: Implement URL helper change**

Change:

```ts
export function userUrl(username: string, protocol = currentProtocol(), hostname = currentHostname()) {
  return siteUrl(username, protocol, hostname)
}

export function channelUrl(slug: string, protocol = currentProtocol(), hostname = currentHostname()) {
  return siteUrl(slug, protocol, hostname)
}
```

- [ ] **Step 4: Run frontend routing tests**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/router/siteContext.spec.ts tests/unit/router/siteUrls.spec.ts
```

Expected: PASS.

### Task 5: Frontend Entity Resolution Integration

**Files:**
- Modify: existing API helper file that defines `api.blog`, `api.users`, and related endpoint paths.
- Modify: `Atoman-Frontend/src/views/blog/ProfileView.vue`
- Modify: `Atoman-Frontend/src/views/blog/ChannelView.vue`
- Create or modify focused tests if existing view tests cover these views.

- [ ] **Step 1: Add site resolver API helper**

Add:

```ts
site: {
  resolve: (handle: string) => `${base}/site/resolve/${encodeURIComponent(handle)}`,
}
```

- [ ] **Step 2: Update profile/channel views for `entity`**

In `ProfileView.vue`, if `siteContext.type === 'entity'`, call resolver first. If it resolves to `user`, load the profile. If it resolves to `channel`, redirect to the channel view path or render the channel view route. If it is 404, keep existing empty/not-found behavior.

In `ChannelView.vue`, if `siteContext.type === 'entity'`, call resolver first. If it resolves to `channel`, use the returned slug. If it resolves to `user`, redirect to the profile route or render the profile view route. Existing route params continue to work for `/channel/:slug`.

- [ ] **Step 3: Preserve legacy prefix redirects**

When `legacyType` is present in `SiteContext`, redirect in the browser from:

```ts
https://u-alice.atoman.org/
https://c-design.atoman.org/
```

to:

```ts
https://alice.atoman.org/
https://design.atoman.org/
```

Use `window.location.replace` only after computing the unified URL, so old links do not create new app state.

- [ ] **Step 4: Run frontend verification**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit -- tests/unit/router/siteContext.spec.ts tests/unit/router/siteUrls.spec.ts
bun run type-check
bun run build
```

Expected: all PASS.

### Task 6: End-To-End Deploy Verification

**Files:**
- No source changes unless earlier verification exposes a concrete bug.

- [ ] **Step 1: Run backend verification**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/service ./internal/handlers ./internal/modules/blog ./internal/app -count=1
go build ./...
```

Expected: PASS.

- [ ] **Step 2: Rebuild and redeploy Docker Compose**

Run:

```bash
cd /root/Atoman
docker compose --env-file ./Atoman-Backend/.env.prod -f docker-compose.prod.yml up -d --build
```

Expected: frontend, backend, db, nginx containers are up and healthy.

- [ ] **Step 3: Verify production endpoints**

Run:

```bash
curl -I https://atoman.org/
curl -s https://atoman.org/api/v1/site/resolve/feed
curl -s https://atoman.org/api/v1/site/resolve/music
curl -s https://atoman.org/api/v1/site/resolve/missing-handle
```

Expected: root returns 200, module handles return 200, missing handle returns 404.

- [ ] **Step 4: Verify DNS/TLS behavior**

Run:

```bash
curl -I https://music.atoman.org/
curl -I https://feed.atoman.org/
```

Expected: HTTP 200 or app-level redirect, with no `Secure Connection Failed`.

## Self-Review

- Spec coverage: The plan removes new `u-`/`c-` URL generation, reserves module names, enforces shared username/channel namespace, adds backend resolution, and keeps old prefixed links compatible.
- Placeholder scan: No TBD or open-ended implementation placeholders remain; each task includes exact files, commands, and expected outcomes.
- Type consistency: `SiteResolution`, `SiteContext`, and URL helper names are consistent across backend and frontend tasks.

# Auto Subscription Add Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge RSS URL, website discovery, and RSSHub-backed GitHub repository subscriptions behind a unified preflight-and-add flow.

**Architecture:** Add a focused feed-module HTTP implementation in `internal/modules/feed/auto_subscription.go` that reuses existing feed helpers for canonical URL lookup, discovery fetch, RSSHub URL building, source creation, duplicate detection, default groups, and sync. The frontend keeps `SubscriptionAddSheet.vue` as the shared UI but replaces three explicit modes with one debounced resolver and one auto-add submit path.

**Tech Stack:** Go, Gin, GORM, SQLite handler tests, Swagger via `swag init -g cmd/start_server/main.go -o docs`, Vue 3, Pinia, TypeScript, Vitest.

---

## File Structure

- Create: `internal/modules/feed/auto_subscription.go`
  - Owns `ResolveSubscriptionInput(db)`, `AutoAddSubscription(db)`, request/response DTOs, GitHub URL parsing, target resolution, existing-source lookup, current-user subscription status, discovery result enrichment, and final subscription creation.
- Modify: `internal/modules/feed/http.go`
  - Registers `POST /api/v1/feed/subscriptions/resolve` and `POST /api/v1/feed/subscriptions/auto-add`.
- Modify: `internal/modules/feed/legacy_compat_test.go`
  - Adds handler-level tests for resolve and auto-add behavior.
- Modify if Swagger generation changes it: `docs/docs.go`, `docs/swagger.json`, `docs/swagger.yaml`
  - Generated API docs for the two new routes.
- Modify: `../Atoman-Frontend/src/types.ts`
  - Adds resolved subscription input types and candidate status fields.
- Modify: `../Atoman-Frontend/src/stores/feed.ts`
  - Adds `resolveSubscriptionInput()` and `autoAddSubscription()`.
- Modify: `../Atoman-Frontend/src/components/feed/SubscriptionAddSheet.vue`
  - Replaces mode switch UI with one input, debounced resolver, status display, candidate selection, and unified submit event.
- Modify: `../Atoman-Frontend/src/views/feed/FeedView.vue`
  - Replaces RSS/discovered/provider handlers with unified submit handling.
- Modify: `../Atoman-Frontend/src/views/orbit/OrbitView.vue`
  - Replaces RSS/discovered/provider handlers with unified submit handling.
- Modify: `../Atoman-Frontend/tests/unit/stores/feed.spec.ts`
  - Adds store tests for resolve and auto-add.
- Modify: `../Atoman-Frontend/tests/unit/components/SubscriptionAddSheet.spec.ts`
  - Replaces old three-mode tests with resolver/status/candidate tests.
- Modify: `../Atoman-Frontend/tests/unit/views/feed/FeedView.spec.ts`
  - Verifies `FeedView` wires the new submit event to the unified store action.

## Task 1: Backend Resolve Endpoint

**Files:**
- Create: `internal/modules/feed/auto_subscription.go`
- Modify: `internal/modules/feed/http.go`
- Test: `internal/modules/feed/legacy_compat_test.go`

- [ ] **Step 1: Write failing tests for resolve states**

Append these tests to `internal/modules/feed/legacy_compat_test.go`:

```go
func TestResolveSubscriptionInputDetectsGithubRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/resolve", withFeedAuth(user.UUID, ResolveSubscriptionInput(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/resolve", strings.NewReader(`{"input":"https://github.com/DIYgod/RSSHub"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var payload AutoSubscriptionResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "new_source" {
		t.Fatalf("expected new_source status, got %q", payload.Status)
	}
	if payload.Source.Provider != "rsshub" {
		t.Fatalf("expected rsshub provider, got %q", payload.Source.Provider)
	}
	if payload.Source.RssURL != "https://rsshub.app/github/repo/DIYgod/RSSHub" {
		t.Fatalf("expected RSSHub URL, got %q", payload.Source.RssURL)
	}
	if payload.Source.SiteURL != "https://github.com/DIYgod/RSSHub" {
		t.Fatalf("expected GitHub site URL, got %q", payload.Source.SiteURL)
	}
}

func TestResolveSubscriptionInputReportsAlreadySubscribedSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	source := model.FeedSource{
		SourceType:   "external_rss",
		Provider:     "rss",
		RssURL:       "https://example.com/feed.xml",
		CanonicalURL: "https://example.com/feed.xml",
		Hash:         buildFeedSourceHash("external_rss", nil, "https://example.com/feed.xml"),
		Title:        "Example Feed",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	subscription := model.Subscription{UserID: user.UUID, FeedSourceID: source.ID, Title: "Example Feed"}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/resolve", withFeedAuth(user.UUID, ResolveSubscriptionInput(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/resolve", strings.NewReader(`{"input":"https://example.com/feed.xml/"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var payload AutoSubscriptionResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "already_subscribed" {
		t.Fatalf("expected already_subscribed status, got %q", payload.Status)
	}
	if payload.Subscription == nil || payload.Subscription.ID != subscription.ID {
		t.Fatalf("expected existing subscription in response, got %#v", payload.Subscription)
	}
}

func TestResolveSubscriptionInputReportsExistingSourceForAnotherUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	source := model.FeedSource{
		SourceType:   "external_rss",
		Provider:     "rss",
		RssURL:       "https://example.com/feed.xml",
		CanonicalURL: "https://example.com/feed.xml",
		Hash:         buildFeedSourceHash("external_rss", nil, "https://example.com/feed.xml"),
		Title:        "Example Feed",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/resolve", withFeedAuth(user.UUID, ResolveSubscriptionInput(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/resolve", strings.NewReader(`{"input":"https://example.com/feed.xml/"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var payload AutoSubscriptionResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "existing_source" {
		t.Fatalf("expected existing_source status, got %q", payload.Status)
	}
	if payload.Source.ID != source.ID {
		t.Fatalf("expected source id %s, got %s", source.ID, payload.Source.ID)
	}
}
```

- [ ] **Step 2: Run resolve tests and verify they fail**

Run:

```bash
go test ./internal/modules/feed -run 'TestResolveSubscriptionInput(DetectsGithubRepository|ReportsAlreadySubscribedSource|ReportsExistingSourceForAnotherUser)' -count=1 -v
```

Expected: FAIL because `ResolveSubscriptionInput` and `AutoSubscriptionResolveResponse` are undefined.

- [ ] **Step 3: Implement resolve DTOs, target parsing, and handler**

Create `internal/modules/feed/auto_subscription.go` with:

```go
package feed

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AutoSubscriptionResolveRequest struct {
	Input string `json:"input"`
}

type AutoSubscriptionAddRequest struct {
	Input            string     `json:"input"`
	CandidateFeedURL string     `json:"candidate_feed_url"`
	Title            string     `json:"title"`
	GroupID          *uuid.UUID `json:"group_id"`
}

type AutoSubscriptionCandidate struct {
	Title          string              `json:"title"`
	FeedURL        string              `json:"feed_url"`
	SiteURL        string              `json:"site_url,omitempty"`
	Kind           string              `json:"kind,omitempty"`
	Score          int                 `json:"score"`
	Reason         string              `json:"reason,omitempty"`
	IsDefault      bool                `json:"is_default"`
	Status         string              `json:"status"`
	Source         AutoSubscriptionSource `json:"source"`
	Subscription   *model.Subscription `json:"subscription,omitempty"`
}

type AutoSubscriptionSource struct {
	ID           uuid.UUID `json:"id,omitempty"`
	Provider     string    `json:"provider"`
	SourceType   string    `json:"source_type"`
	Title        string    `json:"title"`
	RssURL       string    `json:"rss_url"`
	SiteURL      string    `json:"site_url,omitempty"`
	CanonicalURL string    `json:"canonical_url"`
}

type AutoSubscriptionResolveResponse struct {
	Status       string                 `json:"status"`
	Source       AutoSubscriptionSource `json:"source"`
	Subscription *model.Subscription    `json:"subscription,omitempty"`
	Candidates   []AutoSubscriptionCandidate `json:"candidates"`
	Message      string                 `json:"message"`
}

type autoSubscriptionTarget struct {
	Provider   string
	SourceType string
	Title      string
	RssURL     string
	SiteURL    string
	Canonical  string
}

func ResolveSubscriptionInput(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input AutoSubscriptionResolveRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid resolve request body"})
			return
		}

		result, statusCode := resolveSubscriptionInputForUser(db, userID, strings.TrimSpace(input.Input))
		c.JSON(statusCode, result)
	}
}

func resolveSubscriptionInputForUser(db *gorm.DB, userID uuid.UUID, rawInput string) (AutoSubscriptionResolveResponse, int) {
	u, err := parseAutoSubscriptionURL(rawInput)
	if err != nil {
		return AutoSubscriptionResolveResponse{Status: "invalid", Message: "请输入有效的 http/https 地址"}, http.StatusOK
	}

	if target, ok := githubRepositoryTarget(u); ok {
		return classifyAutoSubscriptionTarget(db, userID, target), http.StatusOK
	}

	if isLikelyDirectFeedURL(u) {
		target := autoSubscriptionTarget{
			Provider:   "rss",
			SourceType: "external_rss",
			Title:      rawInput,
			RssURL:     rawInput,
			SiteURL:    rawInput,
			Canonical:  normalizeCanonicalFeedURL(rawInput),
		}
		return classifyAutoSubscriptionTarget(db, userID, target), http.StatusOK
	}

	return resolveDiscoveredSubscriptionInput(db, userID, rawInput, u)
}

func parseAutoSubscriptionURL(raw string) (*url.URL, error) {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || u == nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("url must be an absolute http/https URL")
	}
	return u, nil
}

func githubRepositoryTarget(u *url.URL) (autoSubscriptionTarget, bool) {
	if u == nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return autoSubscriptionTarget{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return autoSubscriptionTarget{}, false
	}
	feedURL, err := service.BuildRSSHubFeedURL("github/repo", map[string]string{
		"owner": parts[0],
		"repo":  parts[1],
	})
	if err != nil {
		return autoSubscriptionTarget{}, false
	}
	siteURL := "https://github.com/" + parts[0] + "/" + parts[1]
	return autoSubscriptionTarget{
		Provider:   "rsshub",
		SourceType: "external_rss",
		Title:      parts[1],
		RssURL:     feedURL,
		SiteURL:    siteURL,
		Canonical:  normalizeCanonicalFeedURL(feedURL),
	}, true
}

func classifyAutoSubscriptionTarget(db *gorm.DB, userID uuid.UUID, target autoSubscriptionTarget) AutoSubscriptionResolveResponse {
	source, found, err := findExistingAutoSubscriptionSource(db, target)
	if err != nil {
		return AutoSubscriptionResolveResponse{Status: "not_found", Message: "检测来源失败"}
	}
	responseSource := sourceDTOFromTarget(target)
	if found {
		responseSource = sourceDTOFromModel(source)
		if subscription, ok, err := findUserSubscriptionForSource(db, userID, source.ID); err == nil && ok {
			return AutoSubscriptionResolveResponse{
				Status:       "already_subscribed",
				Source:       responseSource,
				Subscription: &subscription,
				Candidates:   []AutoSubscriptionCandidate{},
				Message:      "你已订阅此来源",
			}
		}
		return AutoSubscriptionResolveResponse{
			Status:     "existing_source",
			Source:     responseSource,
			Candidates: []AutoSubscriptionCandidate{},
			Message:    "来源已存在，可添加到你的订阅",
		}
	}
	return AutoSubscriptionResolveResponse{
		Status:     "new_source",
		Source:     responseSource,
		Candidates: []AutoSubscriptionCandidate{},
		Message:    "可添加新来源",
	}
}

func findExistingAutoSubscriptionSource(db *gorm.DB, target autoSubscriptionTarget) (model.FeedSource, bool, error) {
	var source model.FeedSource
	if target.Canonical != "" {
		query := db.Where("canonical_url = ?", target.Canonical).Limit(1).Find(&source)
		if query.Error != nil {
			return model.FeedSource{}, false, query.Error
		}
		if query.RowsAffected > 0 {
			return source, true, nil
		}
	}
	hash := buildFeedSourceHash(target.SourceType, nil, target.RssURL)
	query := db.Where("hash = ?", hash).Limit(1).Find(&source)
	if query.Error != nil {
		return model.FeedSource{}, false, query.Error
	}
	return source, query.RowsAffected > 0, nil
}

func findUserSubscriptionForSource(db *gorm.DB, userID uuid.UUID, sourceID uuid.UUID) (model.Subscription, bool, error) {
	var subscription model.Subscription
	query := db.Where("user_id = ? AND feed_source_id = ?", userID, sourceID).Limit(1).Find(&subscription)
	if query.Error != nil {
		return model.Subscription{}, false, query.Error
	}
	return subscription, query.RowsAffected > 0, nil
}

func sourceDTOFromTarget(target autoSubscriptionTarget) AutoSubscriptionSource {
	return AutoSubscriptionSource{
		Provider:     target.Provider,
		SourceType:   target.SourceType,
		Title:        target.Title,
		RssURL:       target.RssURL,
		SiteURL:      target.SiteURL,
		CanonicalURL: target.Canonical,
	}
}

func sourceDTOFromModel(source model.FeedSource) AutoSubscriptionSource {
	return AutoSubscriptionSource{
		ID:           source.ID,
		Provider:     source.Provider,
		SourceType:   source.SourceType,
		Title:        source.Title,
		RssURL:       source.RssURL,
		SiteURL:      source.SiteURL,
		CanonicalURL: source.CanonicalURL,
	}
}
```

Modify `internal/modules/feed/http.go` inside the protected group near the existing subscriptions routes:

```go
protected.POST("/subscriptions/resolve", ResolveSubscriptionInput(service.db))
protected.POST("/subscriptions/auto-add", AutoAddSubscription(service.db))
```

At this step `AutoAddSubscription` is not implemented yet; add a temporary stub at the bottom of `auto_subscription.go` so this task compiles:

```go
func AutoAddSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "auto add subscription is not implemented"})
	}
}
```

- [ ] **Step 4: Add discovery resolve support**

Append these helpers to `internal/modules/feed/auto_subscription.go`:

```go
func resolveDiscoveredSubscriptionInput(db *gorm.DB, userID uuid.UUID, rawInput string, u *url.URL) (AutoSubscriptionResolveResponse, int) {
	if err := validateFeedDiscoveryFetchURL(u); err != nil {
		return AutoSubscriptionResolveResponse{Status: "invalid", Message: "url is not allowed for feed discovery"}, http.StatusOK
	}
	html, err := fetchFeedDiscoveryHTML(rawInput)
	if err != nil {
		return AutoSubscriptionResolveResponse{Status: "not_found", Message: "未找到可用订阅源"}, http.StatusOK
	}
	discovered := service.ExtractFeedCandidatesFromHTML(rawInput, html)
	if len(discovered) == 0 {
		return AutoSubscriptionResolveResponse{Status: "not_found", Message: "未找到可用订阅源"}, http.StatusOK
	}
	candidates := make([]AutoSubscriptionCandidate, 0, len(discovered))
	for _, candidate := range discovered {
		target := autoSubscriptionTarget{
			Provider:   "rss",
			SourceType: "external_rss",
			Title:      candidate.Title,
			RssURL:     candidate.FeedURL,
			SiteURL:    candidate.SiteURL,
			Canonical:  normalizeCanonicalFeedURL(candidate.FeedURL),
		}
		resolved := classifyAutoSubscriptionTarget(db, userID, target)
		candidates = append(candidates, AutoSubscriptionCandidate{
			Title:        candidate.Title,
			FeedURL:      candidate.FeedURL,
			SiteURL:      candidate.SiteURL,
			Kind:         candidate.Kind,
			Score:        candidate.Score,
			Reason:       candidate.Reason,
			IsDefault:    candidate.IsDefault,
			Status:       resolved.Status,
			Source:       resolved.Source,
			Subscription: resolved.Subscription,
		})
	}
	if len(candidates) == 1 {
		only := candidates[0]
		return AutoSubscriptionResolveResponse{
			Status:       only.Status,
			Source:       only.Source,
			Subscription: only.Subscription,
			Candidates:   []AutoSubscriptionCandidate{only},
			Message:      messageForAutoSubscriptionStatus(only.Status),
		}, http.StatusOK
	}
	return AutoSubscriptionResolveResponse{
		Status:     "multiple_candidates",
		Candidates: candidates,
		Message:    "请选择一个订阅源",
	}, http.StatusOK
}

func messageForAutoSubscriptionStatus(status string) string {
	switch status {
	case "already_subscribed":
		return "你已订阅此来源"
	case "existing_source":
		return "来源已存在，可添加到你的订阅"
	case "new_source":
		return "可添加新来源"
	case "multiple_candidates":
		return "请选择一个订阅源"
	case "invalid":
		return "请输入有效的 http/https 地址"
	default:
		return "未找到可用订阅源"
	}
}
```

- [ ] **Step 5: Write failing test for multiple discovered candidates**

Append this test to `internal/modules/feed/legacy_compat_test.go`:

```go
func TestResolveSubscriptionInputReturnsMultipleCandidatesForWebsite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body: io.NopCloser(strings.NewReader(`<html><head>
				<link rel="alternate" type="application/rss+xml" title="Main Feed" href="/feed.xml">
				<link rel="alternate" type="application/atom+xml" title="Updates" href="/updates.atom">
			</head></html>`)),
			Request: req,
		}, nil
	})}
	resolveFeedDiscoveryHostname = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() {
		feedDiscoveryHTTPClient = originalClient
		resolveFeedDiscoveryHostname = originalResolver
	}()

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/resolve", withFeedAuth(user.UUID, ResolveSubscriptionInput(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/resolve", strings.NewReader(`{"input":"https://example.com/blog"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var payload AutoSubscriptionResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "multiple_candidates" {
		t.Fatalf("expected multiple_candidates status, got %q", payload.Status)
	}
	if len(payload.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(payload.Candidates))
	}
	if payload.Candidates[0].Status != "new_source" {
		t.Fatalf("expected candidate status new_source, got %q", payload.Candidates[0].Status)
	}
}
```

- [ ] **Step 6: Run resolve tests and verify they pass**

Run:

```bash
go test ./internal/modules/feed -run 'TestResolveSubscriptionInput' -count=1 -v
```

Expected: PASS for all resolve tests.

- [ ] **Step 7: Commit backend resolve endpoint**

```bash
git add internal/modules/feed/auto_subscription.go internal/modules/feed/http.go internal/modules/feed/legacy_compat_test.go
git commit -m "feat(feed): resolve subscription input"
```

## Task 2: Backend Auto-Add Endpoint

**Files:**
- Modify: `internal/modules/feed/auto_subscription.go`
- Test: `internal/modules/feed/legacy_compat_test.go`

- [ ] **Step 1: Write failing auto-add tests**

Append these tests to `internal/modules/feed/legacy_compat_test.go`:

```go
func TestAutoAddSubscriptionCreatesRSSHubSourceFromGithubRepository(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://github.com/DIYgod/RSSHub","title":"RSSHub Repo"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var source model.FeedSource
	if err := db.First(&source).Error; err != nil {
		t.Fatalf("load source: %v", err)
	}
	if source.Provider != "rsshub" {
		t.Fatalf("expected rsshub provider, got %q", source.Provider)
	}
	if source.RssURL != "https://rsshub.app/github/repo/DIYgod/RSSHub" {
		t.Fatalf("expected RSSHub URL, got %q", source.RssURL)
	}
	if source.SiteURL != "https://github.com/DIYgod/RSSHub" {
		t.Fatalf("expected GitHub site URL, got %q", source.SiteURL)
	}
	if count := countSubscriptionsForUser(t, db, user.UUID); count != 1 {
		t.Fatalf("expected 1 subscription, got %d", count)
	}
}

func TestAutoAddSubscriptionReusesExistingSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	source := model.FeedSource{
		SourceType:   "external_rss",
		Provider:     "rss",
		RssURL:       "https://example.com/feed.xml",
		CanonicalURL: "https://example.com/feed.xml",
		Hash:         buildFeedSourceHash("external_rss", nil, "https://example.com/feed.xml"),
		Title:        "Example Feed",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://example.com/feed.xml/"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var sourceCount int64
	if err := db.Model(&model.FeedSource{}).Count(&sourceCount).Error; err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if sourceCount != 1 {
		t.Fatalf("expected source reused, got %d sources", sourceCount)
	}
}

func TestAutoAddSubscriptionRequiresCandidateWhenMultipleFeedsExist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body: io.NopCloser(strings.NewReader(`<html><head>
				<link rel="alternate" type="application/rss+xml" title="Main Feed" href="/feed.xml">
				<link rel="alternate" type="application/atom+xml" title="Updates" href="/updates.atom">
			</head></html>`)),
			Request: req,
		}, nil
	})}
	resolveFeedDiscoveryHostname = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() {
		feedDiscoveryHTTPClient = originalClient
		resolveFeedDiscoveryHostname = originalResolver
	}()

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://example.com/blog"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "candidate_feed_url is required") {
		t.Fatalf("expected candidate required message, got body %s", rr.Body.String())
	}
}

func TestAutoAddSubscriptionUsesSelectedCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://example.com/blog","candidate_feed_url":"https://example.com/feed.xml","title":"Main Feed"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var source model.FeedSource
	if err := db.First(&source).Error; err != nil {
		t.Fatalf("load source: %v", err)
	}
	if source.RssURL != "https://example.com/feed.xml" {
		t.Fatalf("expected selected candidate feed URL, got %q", source.RssURL)
	}
}
```

- [ ] **Step 2: Run auto-add tests and verify they fail**

Run:

```bash
go test ./internal/modules/feed -run 'TestAutoAddSubscription' -count=1 -v
```

Expected: FAIL because `AutoAddSubscription` still returns 501.

- [ ] **Step 3: Implement target selection for auto-add**

Replace the temporary `AutoAddSubscription` stub in `internal/modules/feed/auto_subscription.go` with:

```go
func AutoAddSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input AutoSubscriptionAddRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid auto add request body"})
			return
		}

		target, err := autoSubscriptionTargetForAdd(db, userID, input)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		subscription, err := createAutoSubscription(db, userID, target, input.Title, input.GroupID)
		if err != nil {
			if err.Error() == "Already subscribed to this source" {
				c.JSON(http.StatusConflict, gin.H{"error": "Already subscribed to this source"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": subscription, "message": "ok"})
	}
}

func autoSubscriptionTargetForAdd(db *gorm.DB, userID uuid.UUID, input AutoSubscriptionAddRequest) (autoSubscriptionTarget, error) {
	candidateURL := strings.TrimSpace(input.CandidateFeedURL)
	if candidateURL != "" {
		if _, err := parseAutoSubscriptionURL(candidateURL); err != nil {
			return autoSubscriptionTarget{}, errors.New("candidate_feed_url must be an absolute http/https URL")
		}
		return autoSubscriptionTarget{
			Provider:   "rss",
			SourceType: "external_rss",
			Title:      strings.TrimSpace(input.Title),
			RssURL:     candidateURL,
			SiteURL:    strings.TrimSpace(input.Input),
			Canonical:  normalizeCanonicalFeedURL(candidateURL),
		}, nil
	}

	rawInput := strings.TrimSpace(input.Input)
	u, err := parseAutoSubscriptionURL(rawInput)
	if err != nil {
		return autoSubscriptionTarget{}, errors.New("input must be an absolute http/https URL")
	}
	if target, ok := githubRepositoryTarget(u); ok {
		if strings.TrimSpace(input.Title) != "" {
			target.Title = strings.TrimSpace(input.Title)
		}
		return target, nil
	}
	if isLikelyDirectFeedURL(u) {
		return autoSubscriptionTarget{
			Provider:   "rss",
			SourceType: "external_rss",
			Title:      strings.TrimSpace(input.Title),
			RssURL:     rawInput,
			SiteURL:    rawInput,
			Canonical:  normalizeCanonicalFeedURL(rawInput),
		}, nil
	}

	resolved, _ := resolveSubscriptionInputForUser(db, userID, rawInput)
	if resolved.Status == "multiple_candidates" {
		return autoSubscriptionTarget{}, errors.New("candidate_feed_url is required when multiple candidates are available")
	}
	if resolved.Source.RssURL == "" {
		return autoSubscriptionTarget{}, errors.New("no subscribable feed source found")
	}
	return autoSubscriptionTarget{
		Provider:   resolved.Source.Provider,
		SourceType: resolved.Source.SourceType,
		Title:      resolved.Source.Title,
		RssURL:     resolved.Source.RssURL,
		SiteURL:    resolved.Source.SiteURL,
		Canonical:  resolved.Source.CanonicalURL,
	}, nil
}
```

The `errors` import was added in Task 1 and is reused here.

- [ ] **Step 4: Implement create/reuse subscription with group support**

Append this helper to `internal/modules/feed/auto_subscription.go`:

```go
func createAutoSubscription(db *gorm.DB, userID uuid.UUID, target autoSubscriptionTarget, title string, groupID *uuid.UUID) (model.Subscription, error) {
	var created model.Subscription
	err := db.Transaction(func(tx *gorm.DB) error {
		group, err := autoSubscriptionGroup(tx, userID, groupID)
		if err != nil {
			return err
		}

		source, err := findOrCreateFeedSource(tx, target.SourceType, nil, target.RssURL, firstNonBlank(title, target.Title), target.Provider)
		if err != nil {
			return err
		}
		updates := map[string]any{}
		if strings.TrimSpace(source.SiteURL) == "" && strings.TrimSpace(target.SiteURL) != "" {
			updates["site_url"] = strings.TrimSpace(target.SiteURL)
		}
		if strings.TrimSpace(source.Provider) == "" && strings.TrimSpace(target.Provider) != "" {
			updates["provider"] = strings.TrimSpace(target.Provider)
		}
		if len(updates) > 0 {
			if err := tx.Model(source).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", source.ID).First(source).Error; err != nil {
				return err
			}
		}

		if _, ok, err := findUserSubscriptionForSource(tx, userID, source.ID); err != nil {
			return err
		} else if ok {
			return errors.New("Already subscribed to this source")
		}

		created = model.Subscription{
			UserID:              userID,
			FeedSourceID:        source.ID,
			Title:               strings.TrimSpace(title),
			SubscriptionGroupID: &group.ID,
		}
		if err := tx.Create(&created).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.Subscription{}, err
	}

	var source model.FeedSource
	if err := db.First(&source, created.FeedSourceID).Error; err == nil && source.SourceType == "external_rss" {
		syncFeedSource(db, source)
	}
	return created, nil
}

func autoSubscriptionGroup(tx *gorm.DB, userID uuid.UUID, groupID *uuid.UUID) (*model.SubscriptionGroup, error) {
	if groupID == nil {
		return getOrCreateDefaultSubscriptionGroup(tx, userID)
	}
	var group model.SubscriptionGroup
	if err := tx.Where("id = ? AND user_id = ?", *groupID, userID).First(&group).Error; err != nil {
		return nil, errors.New("Subscription group not found")
	}
	return &group, nil
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
```

- [ ] **Step 5: Run auto-add tests and fix only minimal failures**

Run:

```bash
go test ./internal/modules/feed -run 'TestAutoAddSubscription' -count=1 -v
```

Expected: PASS. If any test fails, fix only the behavior covered by that test before proceeding.

- [ ] **Step 6: Run all feed module tests**

Run:

```bash
go test ./internal/modules/feed -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit backend auto-add endpoint**

```bash
git add internal/modules/feed/auto_subscription.go internal/modules/feed/legacy_compat_test.go
git commit -m "feat(feed): auto add resolved subscriptions"
```

## Task 3: Backend Routes, Swagger, and Build Verification

**Files:**
- Modify: `internal/modules/feed/auto_subscription.go`
- Modify if generated: `docs/docs.go`
- Modify if generated: `docs/swagger.json`
- Modify if generated: `docs/swagger.yaml`

- [ ] **Step 1: Add Swagger annotations above resolve handler**

Add this comment block immediately above `func ResolveSubscriptionInput` in `internal/modules/feed/auto_subscription.go`:

```go
// ResolveSubscriptionInput godoc
// @Summary 自动检测订阅来源
// @Description 检测输入 URL 是否对应已订阅来源、已有来源、新来源或多个候选，不创建订阅。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body AutoSubscriptionResolveRequest true "订阅来源输入"
// @Success 200 {object} AutoSubscriptionResolveResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/resolve [post]
```

- [ ] **Step 2: Add Swagger annotations above auto-add handler**

Add this comment block immediately above `func AutoAddSubscription`:

```go
// AutoAddSubscription godoc
// @Summary 自动添加订阅
// @Description 根据原始输入或用户选择的候选 feed URL 创建或复用来源并添加当前用户订阅。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body AutoSubscriptionAddRequest true "自动添加订阅输入"
// @Success 201 {object} SubscriptionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/auto-add [post]
```

- [ ] **Step 3: Regenerate Swagger docs**

Run:

```bash
swag init -g cmd/start_server/main.go -o docs
```

Expected: command succeeds and updates generated docs.

- [ ] **Step 4: Verify Swagger includes new routes**

Run:

```bash
rg -n '/api/v1/feed/subscriptions/(resolve|auto-add)' docs/docs.go docs/swagger.json docs/swagger.yaml
```

Expected: all three docs artifacts mention both routes.

- [ ] **Step 5: Run backend verification**

Run:

```bash
go test ./internal/modules/feed -count=1
go build ./...
```

Expected: both commands pass.

- [ ] **Step 6: Commit backend docs and verification changes**

```bash
git add internal/modules/feed/auto_subscription.go docs/docs.go docs/swagger.json docs/swagger.yaml
git commit -m "docs: document auto subscription endpoints"
```

## Task 4: Frontend Store and Types

**Files:**
- Modify: `../Atoman-Frontend/src/types.ts`
- Modify: `../Atoman-Frontend/src/stores/feed.ts`
- Test: `../Atoman-Frontend/tests/unit/stores/feed.spec.ts`

- [ ] **Step 1: Write failing store tests**

Append these tests to `../Atoman-Frontend/tests/unit/stores/feed.spec.ts`:

```ts
  it('resolves subscription input through the unified resolve endpoint', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({
      status: 'existing_source',
      source: {
        id: 'source-1',
        provider: 'rss',
        source_type: 'external_rss',
        title: 'Example Feed',
        rss_url: 'https://example.com/feed.xml',
        canonical_url: 'https://example.com/feed.xml',
      },
      candidates: [],
      message: '来源已存在，可添加到你的订阅',
    }), { status: 200 }))

    const feed = useFeedStore()
    const result = await feed.resolveSubscriptionInput('https://example.com/feed.xml')

    expect(result?.status).toBe('existing_source')
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/feed/subscriptions/resolve', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ input: 'https://example.com/feed.xml' }),
    }))
  })

  it('auto-adds subscriptions through the unified endpoint and moves selected group server-side', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: { id: 'sub-1' } }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ data: [] }), { status: 200 }))

    const feed = useFeedStore()
    const result = await feed.autoAddSubscription({
      input: 'https://github.com/DIYgod/RSSHub',
      title: 'RSSHub Repo',
      group_id: 'group-1',
    })

    expect(result).toBe(true)
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/feed/subscriptions/auto-add', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({
        input: 'https://github.com/DIYgod/RSSHub',
        title: 'RSSHub Repo',
        group_id: 'group-1',
      }),
    }))
    expect(fetchMock).not.toHaveBeenCalledWith('/api/v1/feed/subscriptions/sub-1/group', expect.anything())
  })
```

- [ ] **Step 2: Run store tests and verify they fail**

Run from `../Atoman-Frontend`:

```bash
bun run test:unit tests/unit/stores/feed.spec.ts -- --runInBand
```

Expected: FAIL because the store has no `resolveSubscriptionInput` or `autoAddSubscription`.

- [ ] **Step 3: Add frontend types**

In `../Atoman-Frontend/src/types.ts`, after `FeedDiscoveryCandidate`, add:

```ts
export type ResolvedSubscriptionStatus =
  | 'already_subscribed'
  | 'existing_source'
  | 'new_source'
  | 'multiple_candidates'
  | 'not_found'
  | 'invalid'

export interface ResolvedSubscriptionSource {
  id?: string
  provider: FeedSourceProvider
  source_type: 'external_rss' | 'internal_user' | 'internal_channel' | 'internal_collection'
  title: string
  rss_url: string
  site_url?: string
  canonical_url: string
}

export interface ResolvedSubscriptionCandidate extends FeedDiscoveryCandidate {
  status: ResolvedSubscriptionStatus
  source: ResolvedSubscriptionSource
  subscription?: Subscription
}

export interface ResolvedSubscriptionInput {
  status: ResolvedSubscriptionStatus
  source: ResolvedSubscriptionSource
  subscription?: Subscription
  candidates: ResolvedSubscriptionCandidate[]
  message: string
}

export interface AutoAddSubscriptionPayload {
  input: string
  candidate_feed_url?: string
  title?: string
  group_id?: string
}
```

- [ ] **Step 4: Add store actions**

In `../Atoman-Frontend/src/stores/feed.ts`, update the type imports:

```ts
import type {
  AutoAddSubscriptionPayload,
  FeedDiscoveryCandidate,
  FeedSourceProvider,
  FeedStarGroup,
  ResolvedSubscriptionInput,
  Subscription,
  SubscriptionGroup,
} from '@/types'
```

Add these actions near `discoverFeedCandidates`:

```ts
  const resolveSubscriptionInput = async (input: string): Promise<ResolvedSubscriptionInput | null> => {
    const authStore = useAuthStore()
    if (!authStore.isAuthenticated) return null

    error.value = null
    try {
      const res = await fetch(`${api.url}/feed/subscriptions/resolve`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${authStore.token}`,
        },
        body: JSON.stringify({ input }),
      })

      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        error.value = apiErrorMessage(data, '检测订阅源失败')
        return null
      }

      return data as ResolvedSubscriptionInput
    } catch (e) {
      console.error('Failed to resolve subscription input', e)
      error.value = '网络错误'
      return null
    }
  }

  const autoAddSubscription = async (payload: AutoAddSubscriptionPayload): Promise<boolean> => {
    const authStore = useAuthStore()
    if (!authStore.isAuthenticated) return false

    error.value = null
    try {
      const res = await fetch(`${api.url}/feed/subscriptions/auto-add`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${authStore.token}`,
        },
        body: JSON.stringify(payload),
      })

      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        error.value = apiErrorMessage(data, '添加失败')
        return false
      }

      await fetchSubscriptions()
      return true
    } catch (e) {
      console.error('Failed to auto add subscription', e)
      error.value = '网络错误'
      return false
    }
  }
```

Add both actions to the store return object:

```ts
    resolveSubscriptionInput,
    autoAddSubscription,
```

- [ ] **Step 5: Run store tests**

Run from `../Atoman-Frontend`:

```bash
bun run test:unit tests/unit/stores/feed.spec.ts -- --runInBand
```

Expected: PASS.

- [ ] **Step 6: Commit frontend store and types**

Run from `../Atoman-Frontend`:

```bash
git add src/types.ts src/stores/feed.ts tests/unit/stores/feed.spec.ts
git commit -m "feat(feed): add unified subscription store actions"
```

## Task 5: Frontend Unified Add Sheet and Page Wiring

**Files:**
- Modify: `../Atoman-Frontend/src/components/feed/SubscriptionAddSheet.vue`
- Modify: `../Atoman-Frontend/src/views/feed/FeedView.vue`
- Modify: `../Atoman-Frontend/src/views/orbit/OrbitView.vue`
- Test: `../Atoman-Frontend/tests/unit/components/SubscriptionAddSheet.spec.ts`
- Test: `../Atoman-Frontend/tests/unit/views/feed/FeedView.spec.ts`

- [ ] **Step 1: Replace add sheet tests with unified resolver tests**

Replace `../Atoman-Frontend/tests/unit/components/SubscriptionAddSheet.spec.ts` with:

```ts
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'

import SubscriptionAddSheet from '@/components/feed/SubscriptionAddSheet.vue'

const resolveSubscriptionInput = vi.fn()

vi.mock('@/stores/feed', () => ({
  useFeedStore: () => ({
    resolveSubscriptionInput,
    error: null,
  }),
}))

const mountSheet = (props = {}) => mount(SubscriptionAddSheet, {
  props: {
    show: true,
    groups: [],
    submitting: false,
    error: '',
    ...props,
  },
  global: {
    stubs: {
      PSheet: { template: '<div><slot /></div>' },
      PField: { template: '<label><slot /></label>' },
      PPress: {
        props: ['label', 'disabled', 'loading', 'loadingText'],
        emits: ['click'],
        template: '<button type="button" :disabled="disabled || loading" @click="$emit(\'click\')">{{ loading ? loadingText : label }}</button>',
      },
      PSelect: {
        props: ['modelValue', 'options'],
        emits: ['update:modelValue'],
        template: `
          <select
            data-testid="group-select"
            :value="modelValue"
            @change="$emit('update:modelValue', $event.target.value)"
          >
            <option v-for="option in options" :key="String(option.value)" :value="option.value">{{ option.label }}</option>
          </select>
        `,
      },
    },
  },
})

describe('SubscriptionAddSheet', () => {
  afterEach(() => {
    resolveSubscriptionInput.mockReset()
    vi.useRealTimers()
  })

  it('resolves input after typing and submits unified payload for an existing source', async () => {
    vi.useFakeTimers()
    resolveSubscriptionInput.mockResolvedValue({
      status: 'existing_source',
      source: {
        id: 'source-1',
        provider: 'rss',
        source_type: 'external_rss',
        title: 'Example Feed',
        rss_url: 'https://example.com/feed.xml',
        canonical_url: 'https://example.com/feed.xml',
      },
      candidates: [],
      message: '来源已存在，可添加到你的订阅',
    })
    const wrapper = mountSheet()

    await wrapper.get('input[placeholder="输入网站、RSS 或 GitHub 仓库地址"]').setValue('https://example.com/feed.xml')
    await vi.advanceTimersByTimeAsync(500)
    await flushPromises()

    expect(resolveSubscriptionInput).toHaveBeenCalledWith('https://example.com/feed.xml')
    expect(wrapper.text()).toContain('来源已存在，可添加到你的订阅')

    await wrapper.findAll('button').find((button) => button.text() === '确认订阅')!.trigger('click')

    expect(wrapper.emitted('submit')).toEqual([[
      {
        input: 'https://example.com/feed.xml',
        candidate_feed_url: undefined,
        title: 'Example Feed',
        group_id: '',
      },
    ]])
  })

  it('disables submit when the source is already subscribed', async () => {
    vi.useFakeTimers()
    resolveSubscriptionInput.mockResolvedValue({
      status: 'already_subscribed',
      source: {
        id: 'source-1',
        provider: 'rss',
        source_type: 'external_rss',
        title: 'Example Feed',
        rss_url: 'https://example.com/feed.xml',
        canonical_url: 'https://example.com/feed.xml',
      },
      subscription: { id: 'sub-1' },
      candidates: [],
      message: '你已订阅此来源',
    })
    const wrapper = mountSheet()

    await wrapper.get('input[placeholder="输入网站、RSS 或 GitHub 仓库地址"]').setValue('https://example.com/feed.xml')
    await vi.advanceTimersByTimeAsync(500)
    await flushPromises()

    const submit = wrapper.findAll('button').find((button) => button.text() === '确认订阅')!
    expect(submit.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('你已订阅此来源')
  })

  it('requires selecting a candidate when multiple feeds are discovered', async () => {
    vi.useFakeTimers()
    resolveSubscriptionInput.mockResolvedValue({
      status: 'multiple_candidates',
      source: {
        provider: 'rss',
        source_type: 'external_rss',
        title: '',
        rss_url: '',
        canonical_url: '',
      },
      candidates: [
        {
          title: 'Main Feed',
          feed_url: 'https://example.com/feed.xml',
          site_url: 'https://example.com',
          kind: 'main',
          score: 40,
          is_default: true,
          status: 'new_source',
          source: {
            provider: 'rss',
            source_type: 'external_rss',
            title: 'Main Feed',
            rss_url: 'https://example.com/feed.xml',
            canonical_url: 'https://example.com/feed.xml',
          },
        },
      ],
      message: '请选择一个订阅源',
    })
    const wrapper = mountSheet()

    await wrapper.get('input[placeholder="输入网站、RSS 或 GitHub 仓库地址"]').setValue('https://example.com')
    await vi.advanceTimersByTimeAsync(500)
    await flushPromises()

    expect(wrapper.text()).toContain('请选择一个订阅源')
    await wrapper.find('button.candidate-option').trigger('click')
    await wrapper.findAll('button').find((button) => button.text() === '确认订阅')!.trigger('click')

    expect(wrapper.emitted('submit')).toEqual([[
      {
        input: 'https://example.com',
        candidate_feed_url: 'https://example.com/feed.xml',
        title: 'Main Feed',
        group_id: '',
      },
    ]])
  })

  it('resets input, resolved state, and selected group when resetKey changes', async () => {
    const wrapper = mountSheet({
      groups: [
        { id: 'default-group', name: '默认分组' },
        { id: 'custom-group', name: '技术' },
      ],
      resetKey: 0,
    })

    await wrapper.get('input[placeholder="输入网站、RSS 或 GitHub 仓库地址"]').setValue('https://example.com/feed.xml')
    await wrapper.get('[data-testid="group-select"]').setValue('custom-group')
    await wrapper.get('input[placeholder="例如：GitHub Blog"]').setValue('Custom title')

    await wrapper.setProps({ resetKey: 1 })

    expect(wrapper.get('input[placeholder="输入网站、RSS 或 GitHub 仓库地址"]').element.value).toBe('')
    expect(wrapper.get('[data-testid="group-select"]').element.value).toBe('default-group')
    expect(wrapper.get('input[placeholder="例如：GitHub Blog"]').element.value).toBe('')
  })
})
```

- [ ] **Step 2: Run add sheet tests and verify they fail**

Run from `../Atoman-Frontend`:

```bash
bun run test:unit tests/unit/components/SubscriptionAddSheet.spec.ts -- --runInBand
```

Expected: FAIL because the component still renders mode switches and old events.

- [ ] **Step 3: Rewrite add sheet template**

In `../Atoman-Frontend/src/components/feed/SubscriptionAddSheet.vue`, replace the current mode sections with this structure:

```vue
<template>
  <PSheet
    :show="show"
    title="ADD_SUBSCRIPTION"
    close-type="header"
    :top="top"
    @close="$emit('close')"
  >
    <div class="add-sub-form">
      <h2 class="a-title-sm mb-8">添加订阅</h2>

      <div class="form-fields">
        <PField label="来源地址" required>
          <input
            v-model="sourceInput"
            placeholder="输入网站、RSS 或 GitHub 仓库地址"
            class="a-input"
          />
        </PField>

        <div v-if="resolving" class="resolve-status resolve-status--muted">检测中...</div>
        <div v-else-if="resolveMessage" class="resolve-status" :class="resolveStatusClass">
          {{ resolveMessage }}
        </div>

        <div v-if="resolvedSourceTitle" class="resolved-source">
          <div class="resolved-source__title">{{ resolvedSourceTitle }}</div>
          <div class="resolved-source__url">{{ resolvedSourceUrl }}</div>
        </div>

        <div v-if="resolved?.status === 'multiple_candidates'" class="candidate-list">
          <button
            v-for="candidate in resolved.candidates"
            :key="candidate.feed_url"
            type="button"
            class="candidate-option"
            :class="{ 'candidate-option--active': selectedCandidateUrl === candidate.feed_url }"
            @click="selectCandidate(candidate.feed_url)"
          >
            <div class="candidate-option__title-row">
              <span class="candidate-option__title">{{ candidate.title || candidate.feed_url }}</span>
              <span v-if="candidate.is_default" class="candidate-option__badge">默认</span>
            </div>
            <div class="candidate-option__meta">
              <span>{{ candidate.kind || candidate.status }}</span>
              <span>{{ candidate.feed_url }}</span>
            </div>
          </button>
        </div>

        <PField label="自定义名称（可选）">
          <input v-model="customTitle" placeholder="例如：GitHub Blog" class="a-input" />
        </PField>

        <PField v-if="groups.length" label="添加到分组（可选）">
          <PSelect
            v-model="selectedGroupId"
            :options="[
              { label: '默认分组', value: defaultGroupId || '' },
              ...nonDefaultGroups.map(group => ({ label: group.name, value: group.id }))
            ]"
          />
        </PField>
      </div>

      <div v-if="addError" class="a-error mb-6">{{ addError }}</div>

      <div class="form-actions">
        <PPress variant="secondary" label="取消" @click="$emit('close')" />
        <PPress
          :loading="submitting"
          loading-text="处理中..."
          label="确认订阅"
          :disabled="!canSubmit"
          @click="submitSubscription"
        />
      </div>
    </div>
  </PSheet>
</template>
```

- [ ] **Step 4: Rewrite add sheet script**

Replace the script body in `SubscriptionAddSheet.vue` with:

```ts
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type {
  AutoAddSubscriptionPayload,
  ResolvedSubscriptionCandidate,
  ResolvedSubscriptionInput,
  SubscriptionGroup,
} from '@/types'
import PSheet from '@/components/ui/PSheet.vue'
import PField from '@/components/ui/PField.vue'
import PPress from '@/components/ui/PPress.vue'
import PSelect from '@/components/ui/PSelect.vue'
import { useFeedStore } from '@/stores/feed'

const props = defineProps<{
  show: boolean
  top?: string
  groups: SubscriptionGroup[]
  submitting: boolean
  error?: string
  resetKey?: number
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'submit', payload: AutoAddSubscriptionPayload): void
}>()

const feedStore = useFeedStore()

const sourceInput = ref('')
const customTitle = ref('')
const selectedGroupId = ref('')
const resolved = ref<ResolvedSubscriptionInput | null>(null)
const selectedCandidateUrl = ref('')
const resolving = ref(false)
const localError = ref('')
let resolveTimer: ReturnType<typeof setTimeout> | null = null
let resolveSequence = 0

const defaultGroupId = computed(() => props.groups.find(g => g.name === '默认分组')?.id)
const nonDefaultGroups = computed(() => props.groups.filter(g => g.name !== '默认分组'))
const addError = computed(() => localError.value || props.error || '')
const selectedCandidate = computed(() => resolved.value?.candidates.find(
  (candidate) => candidate.feed_url === selectedCandidateUrl.value,
) || null)
const activeSource = computed(() => selectedCandidate.value?.source || resolved.value?.source || null)
const resolvedSourceTitle = computed(() => customTitle.value.trim() || activeSource.value?.title || selectedCandidate.value?.title || '')
const resolvedSourceUrl = computed(() => selectedCandidate.value?.feed_url || activeSource.value?.rss_url || '')
const resolveMessage = computed(() => resolved.value?.message || '')
const canSubmit = computed(() => {
  if (resolving.value || !sourceInput.value.trim() || !resolved.value) return false
  if (resolved.value.status === 'already_subscribed' || resolved.value.status === 'invalid' || resolved.value.status === 'not_found') return false
  if (resolved.value.status === 'multiple_candidates') return Boolean(selectedCandidateUrl.value)
  return true
})
const resolveStatusClass = computed(() => ({
  'resolve-status--ok': resolved.value?.status === 'existing_source' || resolved.value?.status === 'new_source',
  'resolve-status--blocked': resolved.value?.status === 'already_subscribed' || resolved.value?.status === 'invalid' || resolved.value?.status === 'not_found',
  'resolve-status--choice': resolved.value?.status === 'multiple_candidates',
}))

watch(defaultGroupId, (val) => {
  if (val && !selectedGroupId.value) {
    selectedGroupId.value = val
  }
}, { immediate: true })

watch(sourceInput, (value) => {
  localError.value = ''
  resolved.value = null
  selectedCandidateUrl.value = ''
  if (resolveTimer) clearTimeout(resolveTimer)
  const trimmed = value.trim()
  if (!trimmed) {
    resolving.value = false
    return
  }
  const currentSequence = ++resolveSequence
  resolving.value = true
  resolveTimer = setTimeout(async () => {
    const result = await feedStore.resolveSubscriptionInput(trimmed)
    if (currentSequence !== resolveSequence) return
    resolving.value = false
    resolved.value = result
    if (!result) {
      localError.value = feedStore.error || '检测失败，请稍后重试'
      return
    }
    if (result.status === 'multiple_candidates') {
      selectedCandidateUrl.value = ''
    }
  }, 500)
})

const resetForm = () => {
  sourceInput.value = ''
  customTitle.value = ''
  selectedGroupId.value = defaultGroupId.value || ''
  resolved.value = null
  selectedCandidateUrl.value = ''
  resolving.value = false
  localError.value = ''
  resolveSequence += 1
  if (resolveTimer) clearTimeout(resolveTimer)
}

const selectCandidate = (feedUrl: string) => {
  selectedCandidateUrl.value = feedUrl
  localError.value = ''
}

const submitSubscription = () => {
  if (!canSubmit.value) {
    localError.value = resolved.value?.status === 'multiple_candidates' ? '请选择一个订阅源' : '请先输入可订阅来源'
    return
  }

  const candidate = selectedCandidate.value as ResolvedSubscriptionCandidate | null
  emit('submit', {
    input: sourceInput.value.trim(),
    candidate_feed_url: candidate?.feed_url,
    title: customTitle.value.trim() || candidate?.title || activeSource.value?.title || '',
    group_id: selectedGroupId.value,
  })
}

watch(() => props.show, (val) => {
  if (val) localError.value = ''
})

watch(() => props.resetKey, () => {
  resetForm()
})
</script>
```

- [ ] **Step 5: Update component styles**

In `SubscriptionAddSheet.vue`, remove `.mode-switches` and `.discover-actions` rules. Keep candidate styles and add:

```css
.resolve-status {
  border: 1px solid var(--a-color-line-soft);
  padding: 0.85rem 1rem;
  font-size: 0.9rem;
  font-weight: 800;
}

.resolve-status--muted {
  color: var(--a-color-muted);
}

.resolve-status--ok {
  color: var(--a-color-success);
}

.resolve-status--blocked {
  color: var(--a-color-danger);
}

.resolve-status--choice {
  color: var(--a-color-ink);
}

.resolved-source {
  border: 1px solid var(--a-color-line-soft);
  padding: 0.9rem 1rem;
  background: var(--a-color-paper-soft);
}

.resolved-source__title {
  font-weight: 900;
  line-height: 1.4;
}

.resolved-source__url {
  margin-top: 0.35rem;
  overflow-wrap: anywhere;
  color: var(--a-color-muted);
  font-size: 0.82rem;
}
```

If `--a-color-danger` is not defined, use `var(--a-color-error, #b42318)` instead.

- [ ] **Step 6: Run add sheet tests**

Run from `../Atoman-Frontend`:

```bash
bun run test:unit tests/unit/components/SubscriptionAddSheet.spec.ts -- --runInBand
```

Expected: PASS.

- [ ] **Step 7: Write failing FeedView wiring test**

Append this test to `../Atoman-Frontend/tests/unit/views/feed/FeedView.spec.ts`:

```ts
  it('auto-adds subscriptions from the unified add sheet submit event', async () => {
    const wrapper = mount(FeedView, {
      global: {
        stubs: {
          PButton: true,
          PModal: true,
          PEmpty: true,
          PPageHeader: { template: '<header><slot /><slot name="action" /></header>' },
          PSelect: true,
          PField: true,
          PClip: true,
          PPress: true,
          PBadge: true,
          SubscriptionAddSheet: {
            name: 'SubscriptionAddSheet',
            emits: ['submit', 'close'],
            props: ['show'],
            template: '<div data-test="add-sheet" @click="$emit(\'submit\', { input: \'https://github.com/DIYgod/RSSHub\', title: \'RSSHub Repo\' })"></div>',
          },
          SubscriptionManageSheet: true,
          FeedArticleSheet: true,
        },
      },
    })

    await flushPromises()
    const feedStore = (await import('@/stores/feed')).useFeedStore()
    const autoAddSpy = vi.spyOn(feedStore, 'autoAddSubscription').mockResolvedValue(true)

    await wrapper.get('[data-test="add-sheet"]').trigger('click')
    await flushPromises()

    expect(autoAddSpy).toHaveBeenCalledWith({
      input: 'https://github.com/DIYgod/RSSHub',
      title: 'RSSHub Repo',
    })
  })
```

- [ ] **Step 8: Run FeedView wiring test and verify it fails**

Run from `../Atoman-Frontend`:

```bash
bun run test:unit tests/unit/views/feed/FeedView.spec.ts -- --runInBand
```

Expected: FAIL because `FeedView` still expects old submit payloads and old provider events.

- [ ] **Step 9: Update FeedView to use unified submit**

In `../Atoman-Frontend/src/views/feed/FeedView.vue`:

1. Change the `SubscriptionAddSheet` listeners to:

```vue
@submit="autoAddSubscription"
```

2. Remove `@submit-discovered` and `@submit-provider`.
3. Remove the `FeedSourceProvider` type import if unused.
4. Replace `addSubscription`, `handleDiscoveredSubscription`, and `handleProviderSubscription` with:

```ts
const autoAddSubscription = async (payload: AutoAddSubscriptionPayload) => {
  addSubscriptionError.value = ''
  addingSubscription.value = true
  try {
    const success = await feedStore.autoAddSubscription(payload)
    if (success) {
      addSubscriptionResetKey.value += 1
      showAddModal.value = false
      await fetchTimeline()
      await onboardingStore.handleSubscriptionSuccess()
    } else {
      addSubscriptionError.value = feedStore.error || '添加失败，请检查地址是否正确'
    }
  } catch (error) {
    addSubscriptionError.value = error instanceof Error ? error.message : '添加失败'
  } finally {
    addingSubscription.value = false
  }
}
```

5. Add `AutoAddSubscriptionPayload` to the type import from `@/types`.

- [ ] **Step 10: Update OrbitView to use unified submit**

In `../Atoman-Frontend/src/views/orbit/OrbitView.vue`:

1. Change `SubscriptionAddSheet` listeners to `@submit="autoAddSubscription"`.
2. Remove `@submit-discovered` and `@submit-provider`.
3. Replace old add/discovered/provider handlers with:

```ts
const autoAddSubscription = async (payload: AutoAddSubscriptionPayload) => {
  addSubscriptionError.value = ''
  addingSubscription.value = true
  try {
    const success = await feedStore.autoAddSubscription(payload)
    if (success) {
      addSubscriptionResetKey.value += 1
      showAddModal.value = false
      await fetchSubscriptions()
      await fetchTimeline()
    } else {
      addSubscriptionError.value = feedStore.error || '添加失败，请检查地址是否正确'
    }
  } catch (error) {
    addSubscriptionError.value = error instanceof Error ? error.message : '添加失败'
  } finally {
    addingSubscription.value = false
  }
}
```

4. Import `AutoAddSubscriptionPayload` from `@/types`.
5. Remove `FeedSourceProvider` import if unused.

- [ ] **Step 11: Run frontend focused tests**

Run from `../Atoman-Frontend`:

```bash
bun run test:unit tests/unit/components/SubscriptionAddSheet.spec.ts tests/unit/stores/feed.spec.ts tests/unit/views/feed/FeedView.spec.ts -- --runInBand
```

Expected: PASS.

- [ ] **Step 12: Run frontend type check**

Run from `../Atoman-Frontend`:

```bash
bun run type-check
```

Expected: PASS.

- [ ] **Step 13: Commit frontend unified add UI**

Run from `../Atoman-Frontend`:

```bash
git add src/components/feed/SubscriptionAddSheet.vue src/views/feed/FeedView.vue src/views/orbit/OrbitView.vue tests/unit/components/SubscriptionAddSheet.spec.ts tests/unit/views/feed/FeedView.spec.ts
git commit -m "feat(feed): unify add subscription input"
```

## Final Verification

- [ ] **Step 1: Run backend focused tests**

Run from `Atoman-Backend`:

```bash
go test ./internal/modules/feed -count=1
```

Expected: PASS.

- [ ] **Step 2: Run backend build**

Run from `Atoman-Backend`:

```bash
go build ./...
```

Expected: PASS.

- [ ] **Step 3: Run frontend focused tests**

Run from `Atoman-Frontend`:

```bash
bun run test:unit tests/unit/components/SubscriptionAddSheet.spec.ts tests/unit/stores/feed.spec.ts tests/unit/views/feed/FeedView.spec.ts -- --runInBand
```

Expected: PASS.

- [ ] **Step 4: Run frontend type check**

Run from `Atoman-Frontend`:

```bash
bun run type-check
```

Expected: PASS.

- [ ] **Step 5: Inspect git status in both repositories**

Run:

```bash
cd /root/Atoman/Atoman-Backend && git status --short
cd /root/Atoman/Atoman-Frontend && git status --short
```

Expected: only intentional task changes are present. Pre-existing unrelated dirty files may remain; do not revert them.

package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/service"
)

func newFeedHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.SubscriptionGroup{}, &model.Subscription{}, &model.FeedSource{}, &model.FeedItem{}, &model.FeedItemRead{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func createLegacyFeedSourcesTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`DROP TABLE IF EXISTS feed_sources`).Error; err != nil {
		t.Fatalf("drop feed_sources: %v", err)
	}
	if err := db.Exec(`
		CREATE TABLE feed_sources (
			id TEXT PRIMARY KEY,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME,
			source_type TEXT NOT NULL,
			source_id TEXT,
			rss_url TEXT,
			hash TEXT,
			title TEXT,
			cover_url TEXT,
			last_fetched_at DATETIME,
			full_text_enabled NUMERIC NOT NULL DEFAULT 0,
			full_text_success_count INTEGER NOT NULL DEFAULT 0,
			full_text_failure_count INTEGER NOT NULL DEFAULT 0,
			full_text_last_success_at DATETIME,
			full_text_last_failure_at DATETIME,
			full_text_last_error_code TEXT,
			full_text_last_error TEXT
		)
	`).Error; err != nil {
		t.Fatalf("create legacy feed_sources: %v", err)
	}
}

func seedFeedTestUser(t *testing.T, db *gorm.DB) model.User {
	t.Helper()

	user := model.User{
		Username: "feeduser_" + uuid.NewString()[:8],
		Email:    uuid.NewString() + "@example.com",
		Password: "secret",
		Role:     "user",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create feed test user: %v", err)
	}
	return user
}

func seedFeedAdminUser(t *testing.T, db *gorm.DB) model.User {
	t.Helper()

	user := model.User{
		Username: "feedadmin_" + uuid.NewString()[:8],
		Email:    uuid.NewString() + "@example.com",
		Password: "secret",
		Role:     "admin",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create feed admin user: %v", err)
	}
	return user
}

func seedAdminFeedSource(t *testing.T, db *gorm.DB, title string, hidden bool) model.FeedSource {
	t.Helper()

	now := time.Now().UTC().Truncate(time.Second)
	source := model.FeedSource{
		SourceType:    "external_rss",
		Provider:      "rsshub",
		RssURL:        "https://rsshub.app/example/" + uuid.NewString(),
		CanonicalURL:  "https://rsshub.app/example/" + uuid.NewString(),
		SiteURL:       "https://example.com/" + uuid.NewString(),
		Hash:          "admin-feed-source-" + uuid.NewString(),
		Title:         title,
		Hidden:        hidden,
		HealthStatus:  "healthy",
		LastFetchedAt: &now,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create admin feed source: %v", err)
	}
	return source
}

func withFeedAuth(userID uuid.UUID, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("user_id", userID)
		h(c)
	}
}

func withFeedAuthRole(userID uuid.UUID, role string, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("role", role)
		h(c)
	}
}

func TestGetFeedItemSummaryFallbackWhenFullTextStatusNotSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "feed-handler-source-retry",
		RssURL:          "https://example.com/feed.xml",
		Title:           "Example Feed",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	item := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "feed-item-retry-with-stale-html",
		Title:          "Retry Item",
		Link:           "https://example.com/post",
		Summary:        "<p>summary fallback</p>",
		FullTextHTML:   "<p>stale full text</p>",
		FullTextStatus: service.FullTextStatusRetry,
		PublishedAt:    now,
		FetchedAt:      now,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/feed/items/:id", GetFeedItem(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/items/"+item.ID.String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var payload struct {
		Data FeedItemDetailResponse `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Data.ContentHTML != item.Summary {
		t.Fatalf("expected summary fallback, got %q", payload.Data.ContentHTML)
	}
	if payload.Data.ContentSource != "summary" {
		t.Fatalf("expected content_source=summary, got %q", payload.Data.ContentSource)
	}
}

func TestNewExternalRSSSourceDefaultsFullTextEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)

	source, err := findOrCreateFeedSource(db, "external_rss", nil, "https://example.com/feed.xml", "Example Feed", "")
	if err != nil {
		t.Fatalf("create source: %v", err)
	}
	if !source.FullTextEnabled {
		t.Fatal("expected external_rss source full text enabled by default")
	}
}

func TestFindOrCreateFeedSourceReusesLegacyExternalRSSWithoutCanonicalURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)

	legacyURL := "https://example.com/feed/"
	legacyHashBytes := sha256.Sum256([]byte(legacyURL))
	legacyHash := hex.EncodeToString(legacyHashBytes[:])
	legacySource := model.FeedSource{
		SourceType:      "external_rss",
		Provider:        "rss",
		RssURL:          legacyURL,
		CanonicalURL:    "",
		Hash:            legacyHash,
		Title:           "Legacy Feed",
		FullTextEnabled: true,
	}
	if err := db.Create(&legacySource).Error; err != nil {
		t.Fatalf("create legacy source: %v", err)
	}

	source, err := findOrCreateFeedSource(db, "external_rss", nil, "https://example.com/feed", "Legacy Feed", "")
	if err != nil {
		t.Fatalf("find or create source: %v", err)
	}

	if source.ID != legacySource.ID {
		t.Fatalf("expected to reuse legacy source %s, got %s", legacySource.ID, source.ID)
	}

	var count int64
	if err := db.Model(&model.FeedSource{}).Where("source_type = ?", "external_rss").Count(&count).Error; err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 external_rss source, got %d", count)
	}

	var persisted model.FeedSource
	if err := db.First(&persisted, legacySource.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}
	if persisted.CanonicalURL != "https://example.com/feed" {
		t.Fatalf("expected canonical_url backfilled, got %q", persisted.CanonicalURL)
	}
}

func TestFeedSourceMVPFieldsPersist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	source := model.FeedSource{
		SourceType:    "external_rss",
		Provider:      "rss",
		RssURL:        "https://example.com/feed.xml",
		CanonicalURL:  "https://example.com/feed.xml",
		SiteURL:       "https://example.com",
		Title:         "Example Feed",
		Hash:          "feed-source-mvp-fields-persist",
		Hidden:        true,
		HealthStatus:  "healthy",
		LastError:     "upstream timeout",
		LastFetchedAt: &now,
	}

	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	var got model.FeedSource
	if err := db.First(&got, source.ID).Error; err != nil {
		t.Fatalf("load source: %v", err)
	}

	if got.Provider != "rss" {
		t.Fatalf("expected provider rss, got %q", got.Provider)
	}
	if got.SiteURL != "https://example.com" {
		t.Fatalf("expected site url persisted, got %q", got.SiteURL)
	}
	if got.CanonicalURL != "https://example.com/feed.xml" {
		t.Fatalf("expected canonical url persisted, got %q", got.CanonicalURL)
	}
	if !got.Hidden {
		t.Fatal("expected hidden persisted as true")
	}
	if got.HealthStatus != "healthy" {
		t.Fatalf("expected health status healthy, got %q", got.HealthStatus)
	}
	if got.LastError != "upstream timeout" {
		t.Fatalf("expected last error persisted, got %q", got.LastError)
	}
}

func TestDiscoverFeedCandidatesRejectsInvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/discover", withFeedAuth(user.UUID, DiscoverFeedCandidates()))

	body := strings.NewReader(`{"url":`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/discover", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid discover request body") {
		t.Fatalf("expected invalid discover request body message, got body %s", rr.Body.String())
	}
}

func TestDiscoverFeedCandidatesRejectsInvalidURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/discover", withFeedAuth(user.UUID, DiscoverFeedCandidates()))

	body := strings.NewReader(`{"url":"not-a-url"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/discover", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "url must be an absolute http/https URL") {
		t.Fatalf("expected invalid url message, got body %s", rr.Body.String())
	}
}

func TestFeedSourceMVPMigrationSupportsNewColumns(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	createLegacyFeedSourcesTable(t, db)

	if db.Migrator().HasColumn("feed_sources", "provider") {
		t.Fatal("expected legacy feed_sources table without provider column")
	}

	if err := migrations.Migrate20260603FeedSourceManagementMVP(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	for _, column := range []string{"provider", "canonical_url", "site_url", "hidden", "health_status", "last_error"} {
		if !db.Migrator().HasColumn("feed_sources", column) {
			t.Fatalf("expected feed_sources.%s column after migration", column)
		}
	}
}

func TestCreateSubscriptionReusesExistingFeedSourceForSameCanonicalURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions", withFeedAuth(user.UUID, CreateSubscription(db)))

	first := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions", strings.NewReader(`{"target_type":"external_rss","rss_url":"https://example.com/feed.xml"}`))
	first.Header.Set("Content-Type", "application/json")
	firstRR := httptest.NewRecorder()
	router.ServeHTTP(firstRR, first)
	if firstRR.Code != http.StatusCreated {
		t.Fatalf("expected first subscription status %d, got %d with body %s", http.StatusCreated, firstRR.Code, firstRR.Body.String())
	}

	second := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions", strings.NewReader(`{"target_type":"external_rss","rss_url":"https://example.com/feed.xml/"}`))
	second.Header.Set("Content-Type", "application/json")
	secondRR := httptest.NewRecorder()
	router.ServeHTTP(secondRR, second)
	if secondRR.Code != http.StatusBadRequest {
		t.Fatalf("expected second subscription status %d, got %d with body %s", http.StatusBadRequest, secondRR.Code, secondRR.Body.String())
	}

	var sources []model.FeedSource
	if err := db.Order("created_at ASC").Find(&sources).Error; err != nil {
		t.Fatalf("load feed sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 feed source, got %d", len(sources))
	}
	if sources[0].Provider != "rss" {
		t.Fatalf("expected provider rss, got %q", sources[0].Provider)
	}
	if sources[0].CanonicalURL != "https://example.com/feed.xml" {
		t.Fatalf("expected canonical url normalized, got %q", sources[0].CanonicalURL)
	}
}

func TestGetTimelineUnreadOnlyFiltersReadItems(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	source := model.FeedSource{
		SourceType:      "external_rss",
		Provider:        "rss",
		RssURL:          "https://example.com/feed.xml",
		CanonicalURL:    "https://example.com/feed.xml",
		Hash:            "timeline-unread-only-source",
		Title:           "Unread Only Feed",
		HealthStatus:    "healthy",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	subscription := model.Subscription{
		UserID:       user.UUID,
		FeedSourceID: source.ID,
		Title:        "Unread Only Feed",
	}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	unreadItem := model.FeedItem{
		FeedSourceID: source.ID,
		GUID:         "timeline-unread-item",
		Title:        "Unread Item",
		Link:         "https://example.com/unread",
		PublishedAt:  now,
		FetchedAt:    now,
	}
	readItem := model.FeedItem{
		FeedSourceID: source.ID,
		GUID:         "timeline-read-item",
		Title:        "Read Item",
		Link:         "https://example.com/read",
		PublishedAt:  now.Add(-time.Hour),
		FetchedAt:    now.Add(-time.Hour),
	}
	if err := db.Create(&unreadItem).Error; err != nil {
		t.Fatalf("create unread item: %v", err)
	}
	if err := db.Create(&readItem).Error; err != nil {
		t.Fatalf("create read item: %v", err)
	}

	readMark := model.FeedItemRead{
		UserID:     user.UUID,
		FeedItemID: readItem.ID,
		ReadAt:     now,
	}
	if err := db.Create(&readMark).Error; err != nil {
		t.Fatalf("create read mark: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.GET("/timeline", withFeedAuth(user.UUID, GetTimeline(db)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline?unread_only=true", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			IsRead bool   `json:"is_read"`
			Type   string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("expected 1 unread item, got %d with body %s", len(payload.Data), rr.Body.String())
	}
	if payload.Data[0].Type != "feed_item" {
		t.Fatalf("expected feed_item entry, got %q", payload.Data[0].Type)
	}
	if payload.Data[0].IsRead {
		t.Fatal("expected unread_only response to exclude read items")
	}
}

func TestCreateSubscriptionFromProviderCreatesRSSHubSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/sources/create-from-provider", withFeedAuth(user.UUID, CreateSubscriptionFromProvider(db)))

	body := strings.NewReader(`{"provider":"rsshub","template_key":"github/repo","params":{"owner":"DIYgod","repo":"RSSHub"},"title":"RSSHub Repo"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/sources/create-from-provider", body)
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
		t.Fatalf("expected provider rsshub, got %q", source.Provider)
	}
	if source.SourceType != "external_rss" {
		t.Fatalf("expected source type external_rss, got %q", source.SourceType)
	}
	if !strings.Contains(source.RssURL, "/github/repo/DIYgod/RSSHub") {
		t.Fatalf("expected rsshub github repo url, got %q", source.RssURL)
	}
}

func TestAdminListFeedSourcesRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	admin := router.Group("/api/v1/admin")
	admin.Use(withFeedAuth(user.UUID, middleware.AdminMiddleware(db)))
	admin.GET("/feed/sources", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/sources", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusForbidden, rr.Code, rr.Body.String())
	}
}

func TestAdminListFeedSourcesReturnsSourceRows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	adminUser := seedFeedAdminUser(t, db)
	seedAdminFeedSource(t, db, "RSSHub Source", false)

	router := gin.New()
	admin := router.Group("/api/v1/admin")
	admin.Use(withFeedAuthRole(adminUser.UUID, "admin", middleware.AdminMiddleware(db)))
	admin.GET("/feed/sources", AdminListFeedSources(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/sources", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload struct {
		Items []struct {
			Title         string     `json:"title"`
			Provider      string     `json:"provider"`
			SourceType    string     `json:"source_type"`
			HealthStatus  string     `json:"health_status"`
			LastFetchedAt *time.Time `json:"last_fetched_at"`
			Hidden        bool       `json:"hidden"`
			RssURL        string     `json:"rss_url"`
			SiteURL       string     `json:"site_url"`
			CanonicalURL  string     `json:"canonical_url"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("expected 1 source row, got %d", len(payload.Items))
	}
	if payload.Items[0].Provider != "rsshub" {
		t.Fatalf("expected provider rsshub, got %q", payload.Items[0].Provider)
	}
	if payload.Items[0].Title != "RSSHub Source" {
		t.Fatalf("expected title RSSHub Source, got %q", payload.Items[0].Title)
	}
	if payload.Items[0].SourceType != "external_rss" {
		t.Fatalf("expected source_type external_rss, got %q", payload.Items[0].SourceType)
	}
	if payload.Items[0].HealthStatus != "healthy" {
		t.Fatalf("expected health_status healthy, got %q", payload.Items[0].HealthStatus)
	}
	if payload.Items[0].LastFetchedAt == nil {
		t.Fatal("expected last_fetched_at populated")
	}
	if payload.Items[0].Hidden {
		t.Fatal("expected hidden false")
	}
	if payload.Items[0].RssURL == "" || payload.Items[0].SiteURL == "" || payload.Items[0].CanonicalURL == "" {
		t.Fatalf("expected url skeleton fields present, got %#v", payload.Items[0])
	}
}

func TestGetTimelineExcludesHiddenFeedSources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	visibleSource := seedAdminFeedSource(t, db, "Visible Source", false)
	hiddenSource := seedAdminFeedSource(t, db, "Hidden Source", true)

	visibleSub := model.Subscription{UserID: user.UUID, FeedSourceID: visibleSource.ID, Title: "Visible"}
	hiddenSub := model.Subscription{UserID: user.UUID, FeedSourceID: hiddenSource.ID, Title: "Hidden"}
	if err := db.Create(&visibleSub).Error; err != nil {
		t.Fatalf("create visible subscription: %v", err)
	}
	if err := db.Create(&hiddenSub).Error; err != nil {
		t.Fatalf("create hidden subscription: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	visibleItem := model.FeedItem{FeedSourceID: visibleSource.ID, GUID: "visible-guid", Title: "Visible Item", Link: "https://example.com/visible", PublishedAt: now, FetchedAt: now}
	hiddenItem := model.FeedItem{FeedSourceID: hiddenSource.ID, GUID: "hidden-guid", Title: "Hidden Item", Link: "https://example.com/hidden", PublishedAt: now.Add(-time.Minute), FetchedAt: now.Add(-time.Minute)}
	if err := db.Create(&visibleItem).Error; err != nil {
		t.Fatalf("create visible item: %v", err)
	}
	if err := db.Create(&hiddenItem).Error; err != nil {
		t.Fatalf("create hidden item: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.GET("/timeline", withFeedAuth(user.UUID, GetTimeline(db)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/timeline", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload struct {
		Data []struct {
			Type     string `json:"type"`
			FeedItem *struct {
				Title string `json:"title"`
			} `json:"feed_item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("expected only visible item in timeline, got %d with body %s", len(payload.Data), rr.Body.String())
	}
	if payload.Data[0].FeedItem == nil || payload.Data[0].FeedItem.Title != "Visible Item" {
		t.Fatalf("expected visible item only, got %+v", payload.Data)
	}
}

func TestFeedStarGroupModelMigratesWithFeedItemStarGroupID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)

	if err := db.AutoMigrate(&model.User{}, &model.FeedStarGroup{}, &model.FeedItemStar{}); err != nil {
		t.Fatalf("migrate star groups: %v", err)
	}

	if !db.Migrator().HasTable(&model.FeedStarGroup{}) {
		t.Fatal("expected feed_star_groups table")
	}
	if !db.Migrator().HasColumn(&model.FeedItemStar{}, "GroupID") {
		t.Fatal("expected feed_item_stars.group_id column")
	}
}

type legacyFeedItemStar struct {
	UserID     uuid.UUID `gorm:"type:uuid;not null;primaryKey;index"`
	FeedItemID uuid.UUID `gorm:"type:uuid;not null;primaryKey;index"`
	StarredAt  time.Time
}

func (legacyFeedItemStar) TableName() string { return "feed_item_stars" }

func TestFeedItemStarMigrationPreservesExistingRowsWithNullGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)

	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("migrate legacy users: %v", err)
	}

	user := model.User{
		Username: "feed-star-migration-user",
		Email:    "feed-star-migration@example.com",
		Password: "hashed-password",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "feed-star-migration-source",
		RssURL:     "https://example.com/feed.xml",
		Title:      "Example Feed",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	item := model.FeedItem{
		FeedSourceID: source.ID,
		GUID:         "feed-star-migration-item",
		Title:        "Migration Item",
		Link:         "https://example.com/item",
		PublishedAt:  now,
		FetchedAt:    now,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	if err := db.AutoMigrate(&legacyFeedItemStar{}); err != nil {
		t.Fatalf("migrate legacy feed_item_stars: %v", err)
	}
	if err := db.Create(&legacyFeedItemStar{
		UserID:     user.UUID,
		FeedItemID: item.ID,
		StarredAt:  now,
	}).Error; err != nil {
		t.Fatalf("insert legacy star: %v", err)
	}

	if err := db.AutoMigrate(&model.FeedStarGroup{}, &model.FeedItemStar{}); err != nil {
		t.Fatalf("migrate star groups: %v", err)
	}

	var star model.FeedItemStar
	if err := db.Where("user_id = ? AND feed_item_id = ?", user.UUID, item.ID).First(&star).Error; err != nil {
		t.Fatalf("find migrated star: %v", err)
	}
	if star.GroupID != nil {
		t.Fatalf("expected migrated star group_id nil, got %v", star.GroupID)
	}
	if !db.Migrator().HasColumn(&model.FeedItemStar{}, "GroupID") {
		t.Fatal("expected feed_item_stars.group_id column")
	}
}

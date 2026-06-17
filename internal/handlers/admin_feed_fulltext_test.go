package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/service"
	"atoman/internal/testdb"
)

func TestCreateAdminFeedSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	r := gin.New()
	r.POST("/api/v1/admin/feed/fulltext/sources", CreateAdminFeedSource(db))

	body := bytes.NewBufferString(`{"rss_url":"https://example.com/feed.xml","title":"Example Feed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/feed/fulltext/sources", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var created model.FeedSource
	if err := db.First(&created, "rss_url = ?", "https://example.com/feed.xml").Error; err != nil {
		t.Fatalf("load created source: %v", err)
	}
	if created.SourceType != "external_rss" {
		t.Fatalf("expected external_rss got %s", created.SourceType)
	}
	if created.Title != "Example Feed" {
		t.Fatalf("expected title preserved, got %s", created.Title)
	}
}

func TestCreateAdminFeedSourceRejectsInternalRSSURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	user := model.User{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hashed",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	r.POST("/api/v1/admin/feed/fulltext/sources", CreateAdminFeedSource(db))

	body := bytes.NewBufferString(`{"rss_url":"https://example.com/api/feed/rss/alice","title":"Internal Feed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/feed/fulltext/sources", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", w.Code, w.Body.String())
	}

	var count int64
	if err := db.Model(&model.FeedSource{}).Count(&count).Error; err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no sources created, got %d", count)
	}
}

func TestCreateAdminFeedSourceRejectsInternalRSSURLV1(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hashed"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	r.POST("/api/v1/admin/feed/fulltext/sources", CreateAdminFeedSource(db))

	body := bytes.NewBufferString(`{"rss_url":"https://example.com/api/v1/feed/rss/alice","title":"Internal Feed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/feed/fulltext/sources", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUpdateAdminFeedSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "update-admin-feed-source",
		RssURL:          "https://example.com/original.xml",
		Title:           "Original Feed",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	r := gin.New()
	r.PUT("/api/v1/admin/feed/fulltext/sources/:source_id", UpdateAdminFeedSource(db))

	body := bytes.NewBufferString(`{"rss_url":"https://example.com/updated.xml","title":"Updated Feed"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/feed/fulltext/sources/"+source.ID.String(), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var updated model.FeedSource
	if err := db.First(&updated, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}
	if updated.RssURL != "https://example.com/updated.xml" {
		t.Fatalf("expected rss_url updated, got %s", updated.RssURL)
	}
	if updated.Title != "Updated Feed" {
		t.Fatalf("expected title updated, got %s", updated.Title)
	}
}

func TestUpdateAdminFeedSourceRejectsBlankTitle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	source := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "admin-row-blank-title",
		RssURL:     "https://example.com/blank-title.xml",
		Title:      "Original Title",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	r := gin.New()
	r.PATCH("/api/v1/admin/feed/sources/:id", AdminUpdateFeedSourceRow(db))

	body := bytes.NewBufferString(`{"title":"   \t\n  "}`)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/feed/sources/"+source.ID.String(), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "title") {
		t.Fatalf("expected title validation error, got body=%s", w.Body.String())
	}

	var reloaded model.FeedSource
	if err := db.First(&reloaded, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}
	if reloaded.Title != "Original Title" {
		t.Fatalf("expected title unchanged, got %q", reloaded.Title)
	}
}

func TestAdminDeleteFeedSourceCleansDependentFeedItemTables(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	user := model.User{Username: "reader", Email: "reader@example.com", Password: "hashed"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "admin-delete-cleanup",
		RssURL:     "https://example.com/delete-cleanup.xml",
		Title:      "Delete Cleanup",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	item := model.FeedItem{
		FeedSourceID: source.ID,
		GUID:         "cleanup-item",
		Title:        "Cleanup Item",
		Link:         "https://example.com/items/cleanup",
		PublishedAt:  time.Now().UTC(),
		FetchedAt:    time.Now().UTC(),
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	subscription := model.Subscription{UserID: user.UUID, FeedSourceID: source.ID, Title: "Cleanup Subscription"}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	read := model.FeedItemRead{UserID: user.UUID, FeedItemID: item.ID, ReadAt: time.Now().UTC()}
	if err := db.Create(&read).Error; err != nil {
		t.Fatalf("create read: %v", err)
	}
	star := model.FeedItemStar{UserID: user.UUID, FeedItemID: item.ID, StarredAt: time.Now().UTC()}
	if err := db.Create(&star).Error; err != nil {
		t.Fatalf("create star: %v", err)
	}
	readingListItem := model.ReadingListItem{UserID: user.UUID, FeedItemID: item.ID, CreatedAt: time.Now().UTC()}
	if err := db.Create(&readingListItem).Error; err != nil {
		t.Fatalf("create reading list item: %v", err)
	}

	r := gin.New()
	r.DELETE("/api/v1/admin/feed/sources/:id", AdminDeleteFeedSourceRow(db))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/admin/feed/sources/"+source.ID.String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status=200 got=%d body=%s", w.Code, w.Body.String())
	}

	assertTableEmpty := func(name string, value any) {
		t.Helper()
		var count int64
		if err := db.Model(value).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("expected %s to be empty, got %d", name, count)
		}
	}

	assertTableEmpty("feed_sources", &model.FeedSource{})
	assertTableEmpty("subscriptions", &model.Subscription{})
	assertTableEmpty("feed_items", &model.FeedItem{})
	assertTableEmpty("feed_item_reads", &model.FeedItemRead{})
	assertTableEmpty("feed_item_stars", &model.FeedItemStar{})
	assertTableEmpty("reading_list_items", &model.ReadingListItem{})
}

func TestAdminListFeedSourcesRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)
	user := model.User{
		Username: "feeduser_" + uuid.NewString()[:8],
		Email:    uuid.NewString() + "@example.com",
		Password: "secret",
		Role:     "user",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	router := gin.New()
	admin := router.Group("/api/v1/admin")
	admin.Use(func(c *gin.Context) {
		c.Set("user_id", user.UUID)
		middleware.AdminMiddleware(db)(c)
	})
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
	db := newAdminFeedFullTextTestDB(t)
	adminUser := model.User{
		Username: "feedadmin_" + uuid.NewString()[:8],
		Email:    uuid.NewString() + "@example.com",
		Password: "secret",
		Role:     "admin",
		IsActive: true,
	}
	if err := db.Create(&adminUser).Error; err != nil {
		t.Fatalf("create admin user: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	source := model.FeedSource{
		SourceType:    "external_rss",
		Provider:      "rsshub",
		RssURL:        "https://rsshub.app/example/" + uuid.NewString(),
		CanonicalURL:  "https://rsshub.app/example/" + uuid.NewString(),
		SiteURL:       "https://example.com/" + uuid.NewString(),
		Hash:          "admin-feed-source-" + uuid.NewString(),
		Title:         "RSSHub Source",
		Hidden:        false,
		HealthStatus:  "healthy",
		LastFetchedAt: &now,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create admin feed source: %v", err)
	}

	router := gin.New()
	admin := router.Group("/api/v1/admin")
	admin.Use(func(c *gin.Context) {
		c.Set("user_id", adminUser.UUID)
		c.Set("role", "admin")
		middleware.AdminMiddleware(db)(c)
	})
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

func TestUpdateAdminFeedSourceRejectsInternalSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	source := model.FeedSource{
		SourceType: "internal_channel",
		Hash:       "update-admin-internal-source",
		Title:      "Internal Source",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	r := gin.New()
	r.PUT("/api/v1/admin/feed/fulltext/sources/:source_id", UpdateAdminFeedSource(db))

	body := bytes.NewBufferString(`{"rss_url":"https://example.com/updated.xml","title":"Updated Feed"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/feed/fulltext/sources/"+source.ID.String(), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", w.Code, w.Body.String())
	}
}

func TestSyncAdminFeedSourceRejectsInternalSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	source := model.FeedSource{
		SourceType: "internal_channel",
		Hash:       "sync-admin-internal-source",
		Title:      "Internal Source",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	r := gin.New()
	r.POST("/api/v1/admin/feed/fulltext/sources/:source_id/sync", SyncAdminFeedSource(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/feed/fulltext/sources/"+source.ID.String()+"/sync", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", w.Code, w.Body.String())
	}
}

func newAdminFeedFullTextTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.FeedSource{},
		&model.FeedItem{},
		&model.Subscription{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
		&model.ReadingListItem{},
	)
	return db
}

func TestGetAdminFeedFullTextHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	oldestCreatedAt := now.Add(-6 * time.Hour)
	newerCreatedAt := now.Add(-2 * time.Hour)
	lastAttempt := now.Add(-3 * time.Hour)
	nextAttempt := now.Add(2 * time.Hour)
	enabledSource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "enabled-source",
		RssURL:          "https://example.com/feed.xml",
		Title:           "Enabled Source",
		FullTextEnabled: true,
	}
	disabledSource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "disabled-source",
		RssURL:          "https://example.com/disabled.xml",
		Title:           "Disabled Source",
		FullTextEnabled: false,
	}
	if err := db.Create(&enabledSource).Error; err != nil {
		t.Fatalf("create enabled source: %v", err)
	}
	if err := db.Create(&disabledSource).Error; err != nil {
		t.Fatalf("create disabled source: %v", err)
	}

	items := []model.FeedItem{
		{Base: model.Base{CreatedAt: oldestCreatedAt}, FeedSourceID: enabledSource.ID, GUID: "pending-oldest", Title: "Pending Oldest", Link: "https://example.com/pending-oldest", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending, LastFullTextAttemptAt: &lastAttempt, NextFullTextAttemptAt: &nextAttempt},
		{Base: model.Base{CreatedAt: newerCreatedAt}, FeedSourceID: enabledSource.ID, GUID: "pending-newer", Title: "Pending Newer", Link: "https://example.com/pending-newer", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending},
		{FeedSourceID: enabledSource.ID, GUID: "fetching", Title: "Fetching", Link: "https://example.com/fetching", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusFetching},
		{FeedSourceID: enabledSource.ID, GUID: "retry", Title: "Retry", Link: "https://example.com/retry", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusRetry},
		{FeedSourceID: enabledSource.ID, GUID: "success", Title: "Success", Link: "https://example.com/success", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusSuccess},
		{FeedSourceID: enabledSource.ID, GUID: "failed", Title: "Failed", Link: "https://example.com/failed", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusFailed},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("create items: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/admin/feed/fulltext/health", GetAdminFeedFullTextHealth(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/fulltext/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var payload struct {
		EnabledSources    int64     `json:"enabled_sources"`
		DisabledSources   int64     `json:"disabled_sources"`
		PendingItems      int64     `json:"pending_items"`
		FetchingItems     int64     `json:"fetching_items"`
		RetryItems        int64     `json:"retry_items"`
		SuccessItems      int64     `json:"success_items"`
		FailedItems       int64     `json:"failed_items"`
		SuccessRate       float64   `json:"success_rate"`
		OldestPendingAt   time.Time `json:"oldest_pending_at"`
		WorkerEnabled     bool      `json:"enabled"`
		WorkerConcurrency int       `json:"concurrency"`
		WorkerTimeout     int       `json:"timeout_seconds"`
		WorkerMaxAttempts int       `json:"max_attempts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.EnabledSources != 1 || payload.DisabledSources != 1 {
		t.Fatalf("unexpected source counts: %+v", payload)
	}
	if payload.PendingItems != 2 || payload.FetchingItems != 1 || payload.RetryItems != 1 || payload.SuccessItems != 1 || payload.FailedItems != 1 {
		t.Fatalf("unexpected item counts: %+v", payload)
	}
	if payload.SuccessRate != 0.5 {
		t.Fatalf("expected success_rate=0.5 got %v", payload.SuccessRate)
	}
	if !payload.OldestPendingAt.Equal(oldestCreatedAt) {
		t.Fatalf("expected oldest_pending_at=%s got %s", oldestCreatedAt, payload.OldestPendingAt)
	}
	if payload.WorkerEnabled != service.FullTextWorkerEnabledDefault || payload.WorkerConcurrency != service.FullTextWorkerConcurrency || payload.WorkerTimeout != int(service.FullTextWorkerTimeout/time.Second) || payload.WorkerMaxAttempts != service.FullTextWorkerMaxAttempts {
		t.Fatalf("unexpected worker summary: %+v", payload)
	}
}

func TestRetryAdminFeedFullTextItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	nextAttempt := now.Add(6 * time.Hour)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "retry-source",
		RssURL:          "https://example.com/feed.xml",
		Title:           "Retry Source",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	item := model.FeedItem{
		FeedSourceID:          source.ID,
		GUID:                  "retry-item",
		Title:                 "Retry Item",
		Link:                  "https://93.184.216.34/post",
		PublishedAt:           now,
		FetchedAt:             now,
		FullTextStatus:        service.FullTextStatusFailed,
		FullTextErrorCode:     service.FullTextErrorRequestFailed,
		FullTextError:         "boom",
		FullTextAttemptCount:  3,
		NextFullTextAttemptAt: &nextAttempt,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	r := gin.New()
	r.POST("/api/v1/admin/feed/fulltext/items/:item_id/retry", RetryAdminFeedFullTextItem(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/feed/fulltext/items/"+item.ID.String()+"/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var got model.FeedItem
	if err := db.First(&got, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload item: %v", err)
	}
	if got.FullTextStatus != service.FullTextStatusPending {
		t.Fatalf("expected pending got %s", got.FullTextStatus)
	}
	if got.NextFullTextAttemptAt != nil {
		t.Fatalf("expected next attempt cleared, got %v", got.NextFullTextAttemptAt)
	}
}

func TestRetryAdminFeedFullTextItemRejectsDisabledSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "retry-disabled-source",
		RssURL:          "https://example.com/disabled.xml",
		Title:           "Disabled Retry Source",
		FullTextEnabled: false,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	item := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "retry-disabled-item",
		Title:          "Retry Disabled Item",
		Link:           "https://example.com/post-disabled",
		PublishedAt:    now,
		FetchedAt:      now,
		FullTextStatus: service.FullTextStatusFailed,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	r := gin.New()
	r.POST("/api/v1/admin/feed/fulltext/items/:item_id/retry", RetryAdminFeedFullTextItem(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/feed/fulltext/items/"+item.ID.String()+"/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRetryAdminFeedFullTextItemRejectsNonFailedStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "retry-pending-source",
		RssURL:          "https://example.com/pending.xml",
		Title:           "Pending Retry Source",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	item := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "retry-pending-item",
		Title:          "Retry Pending Item",
		Link:           "https://example.com/post-pending",
		PublishedAt:    now,
		FetchedAt:      now,
		FullTextStatus: service.FullTextStatusPending,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	r := gin.New()
	r.POST("/api/v1/admin/feed/fulltext/items/:item_id/retry", RetryAdminFeedFullTextItem(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/feed/fulltext/items/"+item.ID.String()+"/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status=400 got=%d body=%s", w.Code, w.Body.String())
	}

	var got model.FeedItem
	if err := db.First(&got, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload item: %v", err)
	}
	if got.FullTextStatus != service.FullTextStatusPending {
		t.Fatalf("expected status unchanged, got %s", got.FullTextStatus)
	}
}

func TestUpdateAdminFeedFullTextSourceSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	nextAttempt := now.Add(time.Hour)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "settings-source",
		RssURL:          "https://example.com/feed.xml",
		Title:           "Settings Source",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	item := model.FeedItem{
		FeedSourceID:          source.ID,
		GUID:                  "settings-item",
		Title:                 "Settings Item",
		Link:                  "https://example.com/settings-item",
		PublishedAt:           now,
		FetchedAt:             now,
		FullTextStatus:        service.FullTextStatusRetry,
		FullTextAttemptCount:  2,
		NextFullTextAttemptAt: &nextAttempt,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create item: %v", err)
	}

	body := bytes.NewBufferString(`{"full_text_enabled":false}`)
	r := gin.New()
	r.PUT("/api/v1/admin/feed/fulltext/sources/:source_id/settings", UpdateAdminFeedFullTextSourceSettings(db))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/feed/fulltext/sources/"+source.ID.String()+"/settings", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var got model.FeedSource
	if err := db.First(&got, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}
	if got.FullTextEnabled {
		t.Fatal("expected full_text_enabled=false")
	}

	var gotItem model.FeedItem
	if err := db.First(&gotItem, "id = ?", item.ID).Error; err != nil {
		t.Fatalf("reload item: %v", err)
	}
	if gotItem.FullTextStatus != service.FullTextStatusRetry {
		t.Fatalf("expected retry item unchanged, got %s", gotItem.FullTextStatus)
	}
	if gotItem.NextFullTextAttemptAt == nil || !gotItem.NextFullTextAttemptAt.Equal(nextAttempt) {
		t.Fatalf("expected next attempt unchanged, got %+v", gotItem.NextFullTextAttemptAt)
	}
}

func TestUpdateAdminFeedFullTextSourceSettingsReenablesDisabledItems(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	nextAttempt := now.Add(time.Hour)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "settings-reenable-source",
		RssURL:          "https://example.com/reenable.xml",
		Title:           "Reenable Source",
		FullTextEnabled: false,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	items := []model.FeedItem{
		{FeedSourceID: source.ID, GUID: "reenable-disabled", Title: "Disabled Item", Link: "https://example.com/disabled-item", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusDisabled, FullTextAttemptCount: 3, NextFullTextAttemptAt: &nextAttempt},
		{FeedSourceID: source.ID, GUID: "reenable-empty-link", Title: "Empty Link Item", Link: "", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusDisabled, NextFullTextAttemptAt: &nextAttempt},
		{FeedSourceID: source.ID, GUID: "reenable-failed", Title: "Failed Item", Link: "https://example.com/failed-item", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusFailed, NextFullTextAttemptAt: &nextAttempt},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("create items: %v", err)
	}

	body := bytes.NewBufferString(`{"full_text_enabled":true}`)
	r := gin.New()
	r.PUT("/api/v1/admin/feed/fulltext/sources/:source_id/settings", UpdateAdminFeedFullTextSourceSettings(db))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/feed/fulltext/sources/"+source.ID.String()+"/settings", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var gotSource model.FeedSource
	if err := db.First(&gotSource, "id = ?", source.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}
	if !gotSource.FullTextEnabled {
		t.Fatal("expected full_text_enabled=true")
	}

	var disabledItem model.FeedItem
	if err := db.First(&disabledItem, "guid = ?", "reenable-disabled").Error; err != nil {
		t.Fatalf("reload disabled item: %v", err)
	}
	if disabledItem.FullTextStatus != service.FullTextStatusPending {
		t.Fatalf("expected disabled item pending, got %s", disabledItem.FullTextStatus)
	}
	if disabledItem.NextFullTextAttemptAt != nil {
		t.Fatalf("expected disabled item next attempt cleared, got %+v", disabledItem.NextFullTextAttemptAt)
	}

	var emptyLinkItem model.FeedItem
	if err := db.First(&emptyLinkItem, "guid = ?", "reenable-empty-link").Error; err != nil {
		t.Fatalf("reload empty link item: %v", err)
	}
	if emptyLinkItem.FullTextStatus != service.FullTextStatusDisabled {
		t.Fatalf("expected empty link item unchanged, got %s", emptyLinkItem.FullTextStatus)
	}

	var failedItem model.FeedItem
	if err := db.First(&failedItem, "guid = ?", "reenable-failed").Error; err != nil {
		t.Fatalf("reload failed item: %v", err)
	}
	if failedItem.FullTextStatus != service.FullTextStatusFailed {
		t.Fatalf("expected failed item unchanged, got %s", failedItem.FullTextStatus)
	}
	if failedItem.NextFullTextAttemptAt == nil || !failedItem.NextFullTextAttemptAt.Equal(nextAttempt) {
		t.Fatalf("expected failed item next attempt unchanged, got %+v", failedItem.NextFullTextAttemptAt)
	}
}

func TestGetAdminFeedFullTextHealthCountsOnlyExternalRSS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	externalSuccessAt := now.Add(-90 * time.Minute)
	externalFailureAt := now.Add(-30 * time.Minute)
	externalPendingCreatedAt := now.Add(-4 * time.Hour)
	internalSuccessAt := now.Add(-10 * time.Minute)
	internalFailureAt := now.Add(-5 * time.Minute)
	internalPendingCreatedAt := now.Add(-8 * time.Hour)

	externalSource := model.FeedSource{
		SourceType:            "external_rss",
		Hash:                  "health-external-source",
		RssURL:                "https://example.com/external.xml",
		Title:                 "External Source",
		FullTextEnabled:       true,
		FullTextLastSuccessAt: &externalSuccessAt,
		FullTextLastFailureAt: &externalFailureAt,
	}
	internalSource := model.FeedSource{
		SourceType:            "internal_channel",
		Hash:                  "health-internal-source",
		Title:                 "Internal Source",
		FullTextEnabled:       true,
		FullTextLastSuccessAt: &internalSuccessAt,
		FullTextLastFailureAt: &internalFailureAt,
	}
	if err := db.Create(&externalSource).Error; err != nil {
		t.Fatalf("create external source: %v", err)
	}
	if err := db.Create(&internalSource).Error; err != nil {
		t.Fatalf("create internal source: %v", err)
	}

	items := []model.FeedItem{
		{Base: model.Base{CreatedAt: externalPendingCreatedAt}, FeedSourceID: externalSource.ID, GUID: "external-pending", Title: "External Pending", Link: "https://example.com/external-pending", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending},
		{FeedSourceID: externalSource.ID, GUID: "external-success", Title: "External Success", Link: "https://example.com/external-success", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusSuccess, FullTextFetchedAt: &externalSuccessAt},
		{FeedSourceID: externalSource.ID, GUID: "external-failed", Title: "External Failed", Link: "https://example.com/external-failed", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusFailed},
		{Base: model.Base{CreatedAt: internalPendingCreatedAt}, FeedSourceID: internalSource.ID, GUID: "internal-pending", Title: "Internal Pending", Link: "https://example.com/internal-pending", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending},
		{FeedSourceID: internalSource.ID, GUID: "internal-success", Title: "Internal Success", Link: "https://example.com/internal-success", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusSuccess, FullTextFetchedAt: &internalSuccessAt},
		{FeedSourceID: internalSource.ID, GUID: "internal-failed", Title: "Internal Failed", Link: "https://example.com/internal-failed", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusFailed},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("create items: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/admin/feed/fulltext/health", GetAdminFeedFullTextHealth(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/fulltext/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var payload struct {
		EnabledSources  int64     `json:"enabled_sources"`
		DisabledSources int64     `json:"disabled_sources"`
		PendingItems    int64     `json:"pending_items"`
		SuccessItems    int64     `json:"success_items"`
		FailedItems     int64     `json:"failed_items"`
		LatestSuccessAt time.Time `json:"latest_success_at"`
		LatestFailureAt time.Time `json:"latest_failure_at"`
		OldestPendingAt time.Time `json:"oldest_pending_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.EnabledSources != 1 || payload.DisabledSources != 0 {
		t.Fatalf("unexpected source counts: %+v", payload)
	}
	if payload.PendingItems != 1 || payload.SuccessItems != 1 || payload.FailedItems != 1 {
		t.Fatalf("unexpected item counts: %+v", payload)
	}
	if !payload.LatestSuccessAt.Equal(externalSuccessAt) {
		t.Fatalf("expected latest_success_at=%s got %s", externalSuccessAt, payload.LatestSuccessAt)
	}
	if !payload.LatestFailureAt.Equal(externalFailureAt) {
		t.Fatalf("expected latest_failure_at=%s got %s", externalFailureAt, payload.LatestFailureAt)
	}
	if !payload.OldestPendingAt.Equal(externalPendingCreatedAt) {
		t.Fatalf("expected oldest_pending_at=%s got %s", externalPendingCreatedAt, payload.OldestPendingAt)
	}
}

func TestGetAdminFeedFullTextHealthExcludesPodcastFeeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	blogSource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "health-blog-source",
		RssURL:          "https://example.com/blog.xml",
		Title:           "Blog Source",
		FullTextEnabled: true,
	}
	podcastSource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "health-podcast-source",
		RssURL:          "https://example.com/podcast.xml",
		Title:           "Podcast Source",
		FullTextEnabled: true,
	}
	if err := db.Create(&blogSource).Error; err != nil {
		t.Fatalf("create blog source: %v", err)
	}
	if err := db.Create(&podcastSource).Error; err != nil {
		t.Fatalf("create podcast source: %v", err)
	}
	items := []model.FeedItem{
		{FeedSourceID: blogSource.ID, GUID: "blog-pending", Title: "Blog Pending", Link: "https://example.com/post", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending},
		{FeedSourceID: podcastSource.ID, GUID: "podcast-pending", Title: "Podcast Pending", Link: "https://example.com/episode", EnclosureURL: "https://cdn.example.com/episode.mp3", EnclosureType: "audio/mpeg", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("create items: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/admin/feed/fulltext/health", GetAdminFeedFullTextHealth(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/fulltext/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var payload struct {
		EnabledSources int64 `json:"enabled_sources"`
		PendingItems   int64 `json:"pending_items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.EnabledSources != 1 || payload.PendingItems != 1 {
		t.Fatalf("expected only blog source/item counted, got %+v", payload)
	}
}

func TestUpdateAdminFeedFullTextSourceSettingsRequiresField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "settings-requires-field-source",
		RssURL:          "https://example.com/requires-field.xml",
		Title:           "Requires Field Source",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	r := gin.New()
	r.PUT("/api/v1/admin/feed/fulltext/sources/:source_id/settings", UpdateAdminFeedFullTextSourceSettings(db))

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty object", body: `{}`},
		{name: "missing field", body: `{"unexpected":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/feed/fulltext/sources/"+source.ID.String()+"/settings", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected status=400 got=%d body=%s", w.Code, w.Body.String())
			}

			var got model.FeedSource
			if err := db.First(&got, "id = ?", source.ID).Error; err != nil {
				t.Fatalf("reload source: %v", err)
			}
			if !got.FullTextEnabled {
				t.Fatalf("expected source settings unchanged for %s", tc.name)
			}
		})
	}
}

func TestGetAdminFeedFullTextItemsFilters(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "items-filter-source",
		RssURL:          "https://example.com/items.xml",
		Title:           "Items Filter Source",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	items := []model.FeedItem{
		{FeedSourceID: source.ID, GUID: "item-failed-timeout", Title: "Failed Timeout", Link: "https://example.com/failed-timeout", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusFailed, FullTextErrorCode: service.FullTextErrorRequestTimeout, FullTextError: "timeout while fetching", FullTextAttemptCount: 4},
		{FeedSourceID: source.ID, GUID: "item-failed-request", Title: "Failed Request", Link: "https://example.com/failed-request", PublishedAt: now.Add(-time.Minute), FetchedAt: now, FullTextStatus: service.FullTextStatusFailed, FullTextErrorCode: service.FullTextErrorRequestFailed, FullTextError: "upstream request failed", FullTextAttemptCount: 2},
		{FeedSourceID: source.ID, GUID: "item-success", Title: "Success", Link: "https://example.com/success", PublishedAt: now.Add(-2 * time.Minute), FetchedAt: now, FullTextStatus: service.FullTextStatusSuccess, FullTextAttemptCount: 0},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("create items: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/admin/feed/fulltext/items", GetAdminFeedFullTextItems(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/fulltext/items?status=failed&error_code="+service.FullTextErrorRequestTimeout, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []struct {
			ID             uuid.UUID `json:"id"`
			SourceID       uuid.UUID `json:"source_id"`
			SourceTitle    string    `json:"source_title"`
			FullTextStatus string    `json:"full_text_status"`
			AttemptCount   int       `json:"attempt_count"`
			ErrorCode      string    `json:"error_code"`
			ErrorMessage   string    `json:"error_message"`
		} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Meta.Total != 1 || len(payload.Data) != 1 {
		t.Fatalf("expected one filtered item, meta=%+v len=%d", payload.Meta, len(payload.Data))
	}
	row := payload.Data[0]
	if row.SourceID != source.ID || row.SourceTitle != source.Title {
		t.Fatalf("unexpected source payload: %+v", row)
	}
	if row.FullTextStatus != service.FullTextStatusFailed || row.ErrorCode != service.FullTextErrorRequestTimeout {
		t.Fatalf("unexpected filter result: %+v", row)
	}
	if row.AttemptCount != 4 || row.ErrorMessage == "" {
		t.Fatalf("expected semantic fields present, got %+v", row)
	}
}

func TestGetAdminFeedFullTextSourcesEnabledFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	enabledSource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "sources-enabled-true",
		RssURL:          "https://example.com/enabled.xml",
		Title:           "Enabled Source",
		FullTextEnabled: true,
	}
	disabledSource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "sources-enabled-false",
		RssURL:          "https://example.com/disabled.xml",
		Title:           "Disabled Source",
		FullTextEnabled: false,
	}
	if err := db.Create(&enabledSource).Error; err != nil {
		t.Fatalf("create enabled source: %v", err)
	}
	if err := db.Create(&disabledSource).Error; err != nil {
		t.Fatalf("create disabled source: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/admin/feed/fulltext/sources", GetAdminFeedFullTextSources(db))

	for _, tc := range []struct {
		name       string
		query      string
		expectedID uuid.UUID
	}{
		{name: "enabled true", query: "enabled=true", expectedID: enabledSource.ID},
		{name: "enabled false", query: "enabled=false", expectedID: disabledSource.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/fulltext/sources?"+tc.query, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}

			var payload struct {
				Data []struct {
					ID uuid.UUID `json:"id"`
				} `json:"data"`
				Meta struct {
					Total int `json:"total"`
				} `json:"meta"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if payload.Meta.Total != 1 || len(payload.Data) != 1 {
				t.Fatalf("expected one filtered source, meta=%+v len=%d", payload.Meta, len(payload.Data))
			}
			if payload.Data[0].ID != tc.expectedID {
				t.Fatalf("expected source %s got %s", tc.expectedID, payload.Data[0].ID)
			}
		})
	}
}

func TestGetAdminFeedFullTextSourcesStatusFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	healthySource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "sources-healthy",
		RssURL:          "https://example.com/healthy.xml",
		Title:           "Healthy Source",
		FullTextEnabled: true,
	}
	degradedSource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "sources-degraded",
		RssURL:          "https://example.com/degraded.xml",
		Title:           "Degraded Source",
		FullTextEnabled: true,
	}
	disabledSource := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "sources-disabled",
		RssURL:          "https://example.com/disabled.xml",
		Title:           "Disabled Source",
		FullTextEnabled: false,
	}
	if err := db.Create(&healthySource).Error; err != nil {
		t.Fatalf("create healthy source: %v", err)
	}
	if err := db.Create(&degradedSource).Error; err != nil {
		t.Fatalf("create degraded source: %v", err)
	}
	if err := db.Create(&disabledSource).Error; err != nil {
		t.Fatalf("create disabled source: %v", err)
	}
	items := []model.FeedItem{
		{FeedSourceID: healthySource.ID, GUID: "healthy-success", Title: "Healthy Success", Link: "https://example.com/healthy-success", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusSuccess},
		{FeedSourceID: degradedSource.ID, GUID: "degraded-retry", Title: "Degraded Retry", Link: "https://example.com/degraded-retry", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusRetry},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("create items: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/admin/feed/fulltext/sources", GetAdminFeedFullTextSources(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/fulltext/sources?status=degraded", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []struct {
			ID           uuid.UUID `json:"id"`
			PendingCount int64     `json:"pending_count"`
			Status       string    `json:"status"`
		} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Meta.Total != 1 || len(payload.Data) != 1 {
		t.Fatalf("expected filtered total/data to match, meta=%+v len=%d", payload.Meta, len(payload.Data))
	}
	if payload.Data[0].ID != degradedSource.ID || payload.Data[0].Status != "degraded" || payload.Data[0].PendingCount != 0 {
		t.Fatalf("unexpected source row: %+v", payload.Data[0])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/fulltext/sources?status=disabled", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	payload = struct {
		Data []struct {
			ID           uuid.UUID `json:"id"`
			PendingCount int64     `json:"pending_count"`
			Status       string    `json:"status"`
		} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode disabled payload: %v", err)
	}
	if payload.Meta.Total != 1 || len(payload.Data) != 1 {
		t.Fatalf("expected disabled total/data to match, meta=%+v len=%d", payload.Meta, len(payload.Data))
	}
	if payload.Data[0].ID != disabledSource.ID || payload.Data[0].Status != "disabled" {
		t.Fatalf("unexpected disabled source row: %+v", payload.Data[0])
	}
}

func TestGetAdminFeedFullTextSourcesSortByPendingCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminFeedFullTextTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	highPending := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "sources-pending-high",
		RssURL:          "https://example.com/high.xml",
		Title:           "High Pending Source",
		FullTextEnabled: true,
	}
	lowPending := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "sources-pending-low",
		RssURL:          "https://example.com/low.xml",
		Title:           "Low Pending Source",
		FullTextEnabled: true,
	}
	if err := db.Create(&highPending).Error; err != nil {
		t.Fatalf("create high pending source: %v", err)
	}
	if err := db.Create(&lowPending).Error; err != nil {
		t.Fatalf("create low pending source: %v", err)
	}
	items := []model.FeedItem{
		{FeedSourceID: highPending.ID, GUID: "high-1", Title: "High 1", Link: "https://example.com/high-1", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending},
		{FeedSourceID: highPending.ID, GUID: "high-2", Title: "High 2", Link: "https://example.com/high-2", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending},
		{FeedSourceID: lowPending.ID, GUID: "low-1", Title: "Low 1", Link: "https://example.com/low-1", PublishedAt: now, FetchedAt: now, FullTextStatus: service.FullTextStatusPending},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("create items: %v", err)
	}

	r := gin.New()
	r.GET("/api/v1/admin/feed/fulltext/sources", GetAdminFeedFullTextSources(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/feed/fulltext/sources?sort=pending_count", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []struct {
			ID           uuid.UUID `json:"id"`
			PendingCount int64     `json:"pending_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Data) < 2 {
		t.Fatalf("expected at least two rows, got %d", len(payload.Data))
	}
	if payload.Data[0].ID != highPending.ID || payload.Data[0].PendingCount != 2 {
		t.Fatalf("expected high pending source first, got %+v", payload.Data[0])
	}
	if payload.Data[1].ID != lowPending.ID || payload.Data[1].PendingCount != 1 {
		t.Fatalf("expected low pending source second, got %+v", payload.Data[1])
	}
}

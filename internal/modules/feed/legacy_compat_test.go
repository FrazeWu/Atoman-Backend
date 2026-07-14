package feed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

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
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	return db
}

func newFeedHandlerTestDBWithLogBuffer(t *testing.T, sink io.Writer) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())
	logger := gormlogger.New(log.New(sink, "", 0), gormlogger.Config{LogLevel: gormlogger.Info, Colorful: false})
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger})
	if err != nil {
		t.Fatalf("open sqlite with logger: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.SubscriptionGroup{}, &model.Subscription{}, &model.FeedSource{}, &model.FeedItem{}, &model.FeedItemRead{}); err != nil {
		t.Fatalf("migrate with logger: %v", err)
	}
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
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

type feedDiscoveryRoundTripFunc func(*http.Request) (*http.Response, error)

func (f feedDiscoveryRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newOPMLUploadRequest(t *testing.T, path string, opml string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", path)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader(opml)); err != nil {
		t.Fatalf("write opml body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

func countSubscriptionsForUser(t *testing.T, db *gorm.DB, userID uuid.UUID) int64 {
	t.Helper()
	var count int64
	if err := db.Model(&model.Subscription{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	return count
}

func disableFeedSourceSync(t *testing.T) {
	t.Helper()
	original := syncFeedSource
	syncFeedSource = func(db *gorm.DB, source model.FeedSource) {}
	t.Cleanup(func() {
		syncFeedSource = original
	})
}

func signedFeedTokenForTest(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func TestLegacyExploreFeedDefaultsAndUnknownSortUseRecentOrder(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{name: "default", path: "/api/v1/feed/explore?limit=2"},
		{name: "unknown", path: "/api/v1/feed/explore?sort=not-a-real-sort&limit=2"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			var logs bytes.Buffer
			db := newFeedHandlerTestDBWithLogBuffer(t, &logs)
			source := model.FeedSource{
				SourceType:   "external_rss",
				RssURL:       "https://legacy-sort.example.com/feed.xml",
				Hash:         "legacy-sort-" + tt.name,
				Title:        "Legacy Sort Feed",
				HealthStatus: "healthy",
			}
			if err := db.Create(&source).Error; err != nil {
				t.Fatalf("create source: %v", err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			if err := db.Create(&model.FeedItem{
				FeedSourceID: source.ID,
				GUID:         "legacy-sort-older-" + tt.name,
				Title:        "Older legacy sort item",
				Link:         "https://legacy-sort.example.com/older",
				PublishedAt:  now.Add(-time.Hour),
				FetchedAt:    now,
			}).Error; err != nil {
				t.Fatalf("create older item: %v", err)
			}
			if err := db.Create(&model.FeedItem{
				FeedSourceID: source.ID,
				GUID:         "legacy-sort-newer-" + tt.name,
				Title:        "Newer legacy sort item",
				Link:         "https://legacy-sort.example.com/newer",
				PublishedAt:  now,
				FetchedAt:    now,
			}).Error; err != nil {
				t.Fatalf("create newer item: %v", err)
			}

			router := gin.New()
			router.GET("/api/v1/feed/explore", GetExploreFeed(db))

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d with body %s", rr.Code, rr.Body.String())
			}
			sql := strings.ToUpper(logs.String())
			if strings.Contains(sql, "RANDOM()") {
				t.Fatalf("expected recent order, got random SQL:\n%s", logs.String())
			}
			if !strings.Contains(sql, "ORDER BY PUBLISHED_AT DESC") {
				t.Fatalf("expected published_at DESC order, got SQL:\n%s", logs.String())
			}
		})
	}
}

func TestImportGlobalOPMLCreatesFeedSourcesWithoutSubscriptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	admin := seedFeedAdminUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/sources/opml/import", withFeedAuth(admin.UUID, ImportGlobalOPML(db)))

	opml := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <head><title>Feeds</title></head>
  <body>
    <outline text="Tech">
      <outline text="Example Feed" type="rss" xmlUrl="https://example.com/feed.xml" />
      <outline text="Example Two" type="rss" xmlUrl="https://example.com/two.xml" />
    </outline>
  </body>
</opml>`

	beforeSubscriptions := countSubscriptionsForUser(t, db, admin.UUID)
	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Message  string `json:"message"`
		Imported int    `json:"imported"`
		Reused   int    `json:"reused"`
		Failed   int    `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Imported != 2 || payload.Reused != 0 || payload.Failed != 0 {
		t.Fatalf("unexpected import counts: %#v", payload)
	}

	var sources []model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").Order("created_at ASC").Find(&sources).Error; err != nil {
		t.Fatalf("load feed sources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 external feed sources, got %d", len(sources))
	}
	if sources[0].RssURL != "https://example.com/feed.xml" || sources[0].Title != "Example Feed" {
		t.Fatalf("unexpected first feed source: %#v", sources[0])
	}
	if afterSubscriptions := countSubscriptionsForUser(t, db, admin.UUID); afterSubscriptions != beforeSubscriptions {
		t.Fatalf("expected subscriptions to remain %d, got %d", beforeSubscriptions, afterSubscriptions)
	}
}

func TestImportGlobalOPMLReusesExistingFeedSources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	admin := seedFeedAdminUser(t, db)

	existing := model.FeedSource{
		SourceType:   "external_rss",
		Provider:     "rss",
		RssURL:       "https://example.com/feed.xml",
		CanonicalURL: "https://example.com/feed.xml",
		Hash:         buildFeedSourceHash("external_rss", nil, "https://example.com/feed.xml"),
		Title:        "Existing Feed",
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create existing feed source: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/sources/opml/import", withFeedAuth(admin.UUID, ImportGlobalOPML(db)))

	opml := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <body>
    <outline text="Existing Feed" type="rss" xmlUrl="https://example.com/feed.xml" />
    <outline text="Brand New" type="rss" xmlUrl="https://example.com/new.xml" />
  </body>
</opml>`

	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Imported int `json:"imported"`
		Reused   int `json:"reused"`
		Failed   int `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Imported != 1 || payload.Reused != 1 || payload.Failed != 0 {
		t.Fatalf("unexpected import counts: %#v", payload)
	}

	var count int64
	if err := db.Model(&model.FeedSource{}).Where("source_type = ?", "external_rss").Count(&count).Error; err != nil {
		t.Fatalf("count feed sources: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 external feed sources after reuse, got %d", count)
	}
}

func TestImportGlobalOPMLCanonicalURLMatchCountsAsReused(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	admin := seedFeedAdminUser(t, db)

	existing := model.FeedSource{
		SourceType:   "external_rss",
		Provider:     "rss",
		RssURL:       "https://legacy.example.com/feed.xml/",
		CanonicalURL: "https://example.com/feed.xml",
		Hash:         "legacy-canonical-match-without-normalized-hash",
		Title:        "Legacy Feed",
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatalf("create canonical-match source: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/sources/opml/import", withFeedAuth(admin.UUID, ImportGlobalOPML(db)))

	opml := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <body>
    <outline text="Legacy Feed" type="rss" xmlUrl="https://example.com/feed.xml" />
  </body>
</opml>`

	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Imported int `json:"imported"`
		Reused   int `json:"reused"`
		Failed   int `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Imported != 0 || payload.Reused != 1 || payload.Failed != 0 {
		t.Fatalf("unexpected import counts: %#v", payload)
	}

	var count int64
	if err := db.Model(&model.FeedSource{}).Where("source_type = ?", "external_rss").Count(&count).Error; err != nil {
		t.Fatalf("count feed sources: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected canonical_url match to reuse source, got %d sources", count)
	}

	var persisted model.FeedSource
	if err := db.First(&persisted, existing.ID).Error; err != nil {
		t.Fatalf("reload source: %v", err)
	}
	if persisted.ID != existing.ID {
		t.Fatalf("expected canonical_url hit to keep existing source %s, got %s", existing.ID, persisted.ID)
	}
	if persisted.CanonicalURL != "https://example.com/feed.xml" {
		t.Fatalf("expected canonical_url preserved, got %q", persisted.CanonicalURL)
	}
}

func TestImportGlobalOPMLRequiresAuthenticatedUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.Use(middleware.AuthMiddleware())
	feed.POST("/sources/opml/import", ImportGlobalOPML(db))

	opml := `<?xml version="1.0"?><opml version="2.0"><body></body></opml>`
	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestImportGlobalOPMLRejectsNonAdminUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), NewService(db))

	opml := `<?xml version="1.0"?><opml version="2.0"><body><outline text="User Feed" type="rss" xmlUrl="https://example.com/user.xml" /></body></opml>`
	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	req.Header.Set("Authorization", "Bearer "+signedFeedTokenForTest(t, user))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestImportGlobalOPMLAllowsAdminThroughRealRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	admin := seedFeedAdminUser(t, db)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), NewService(db))

	opml := `<?xml version="1.0"?><opml version="2.0"><body><outline text="Admin Feed" type="rss" xmlUrl="https://example.com/admin.xml" /></body></opml>`
	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	req.Header.Set("Authorization", "Bearer "+signedFeedTokenForTest(t, admin))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestImportGlobalOPMLSchedulesSyncAsynchronously(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	admin := seedFeedAdminUser(t, db)

	originalSync := syncFeedSource
	syncStarted := make(chan struct{}, 1)
	syncRelease := make(chan struct{})
	syncFeedSource = func(db *gorm.DB, source model.FeedSource) {
		syncStarted <- struct{}{}
		<-syncRelease
	}
	t.Cleanup(func() {
		syncFeedSource = originalSync
		close(syncRelease)
	})

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.Use(func(c *gin.Context) {
		c.Set("user_id", admin.UUID)
		c.Set("role", admin.Role)
		c.Next()
	})
	feed.POST("/sources/opml/import", ImportGlobalOPML(db))

	opml := `<?xml version="1.0"?><opml version="2.0"><body><outline text="Slow Feed" type="rss" xmlUrl="https://example.com/slow.xml" /></body></opml>`
	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(w, req)
		close(done)
	}()

	select {
	case <-syncStarted:
	case <-time.After(time.Second):
		t.Fatal("expected import to schedule feed sync")
	}

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected import response without waiting for feed sync")
	}

	if w.Code != http.StatusOK {
		t.Fatalf("expected import to return 200, got %d with body %s", w.Code, w.Body.String())
	}
}

func TestExportGlobalOPMLExportsExternalRSSOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)

	external := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "https://example.com/feed.xml",
		Title:      "Example Feed",
		Hash:       "export-external-" + uuid.NewString(),
	}
	if err := db.Create(&external).Error; err != nil {
		t.Fatalf("create external source: %v", err)
	}
	internal := model.FeedSource{
		SourceType: "internal_user",
		RssURL:     "https://example.com/internal.xml",
		Title:      "Internal Feed",
		Hash:       "export-internal-" + uuid.NewString(),
	}
	if err := db.Create(&internal).Error; err != nil {
		t.Fatalf("create internal source: %v", err)
	}
	blank := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "",
		Title:      "Blank Feed",
		Hash:       "export-blank-" + uuid.NewString(),
	}
	if err := db.Create(&blank).Error; err != nil {
		t.Fatalf("create blank source: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.GET("/sources/opml/export", ExportGlobalOPML(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/sources/opml/export", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "application/x-opml+xml") {
		t.Fatalf("expected OPML content type, got %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "atoman-feed-sources.opml") {
		t.Fatalf("expected OPML attachment filename, got %q", got)
	}
	var parsed OPML
	if err := xml.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode exported OPML: %v", err)
	}
	if parsed.Head.Title != "Atoman Feed Sources" {
		t.Fatalf("unexpected OPML title %q", parsed.Head.Title)
	}
	if len(parsed.Body.Outlines) != 1 {
		t.Fatalf("expected one exported source, got %d: %#v", len(parsed.Body.Outlines), parsed.Body.Outlines)
	}
	outline := parsed.Body.Outlines[0]
	if outline.Text != "Example Feed" || outline.Title != "Example Feed" || outline.Type != "rss" || outline.XMLURL != "https://example.com/feed.xml" {
		t.Fatalf("unexpected exported outline: %#v", outline)
	}
}

func TestExportGlobalOPMLRequiresAdminThroughRealRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)
	admin := seedFeedAdminUser(t, db)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), NewService(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/sources/opml/export", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedTokenForTest(t, user))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/feed/sources/opml/export", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedTokenForTest(t, admin))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin, got %d: %s", w.Code, w.Body.String())
	}
}

func TestImportGlobalOPMLRejectsInvalidFeedURLs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	admin := seedFeedAdminUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/sources/opml/import", withFeedAuth(admin.UUID, ImportGlobalOPML(db)))

	opml := `<?xml version="1.0"?><opml version="2.0"><body>
		<outline text="Relative" type="rss" xmlUrl="/api/feed/rss/alice" />
		<outline text="File" type="rss" xmlUrl="file:///etc/passwd" />
		<outline text="Script" type="rss" xmlUrl="javascript:alert(1)" />
	</body></opml>`
	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Imported int `json:"imported"`
		Reused   int `json:"reused"`
		Failed   int `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Imported != 0 || payload.Reused != 0 || payload.Failed != 3 {
		t.Fatalf("unexpected import counts: %#v", payload)
	}
	var count int64
	if err := db.Model(&model.FeedSource{}).Count(&count).Error; err != nil {
		t.Fatalf("count feed sources: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected invalid urls not to create sources, got %d", count)
	}
}

func TestImportGlobalOPMLReusesDuplicateURLsInSameUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	admin := seedFeedAdminUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/sources/opml/import", withFeedAuth(admin.UUID, ImportGlobalOPML(db)))

	opml := `<?xml version="1.0"?><opml version="2.0"><body>
		<outline text="First" type="rss" xmlUrl="https://example.com/feed.xml" />
		<outline text="Duplicate" type="rss" xmlUrl="https://example.com/feed.xml/" />
	</body></opml>`
	req := newOPMLUploadRequest(t, "/api/v1/feed/sources/opml/import", opml)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var payload struct {
		Imported int `json:"imported"`
		Reused   int `json:"reused"`
		Failed   int `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Imported != 1 || payload.Reused != 1 || payload.Failed != 0 {
		t.Fatalf("unexpected import counts: %#v", payload)
	}
	var count int64
	if err := db.Model(&model.FeedSource{}).Where("source_type = ?", "external_rss").Count(&count).Error; err != nil {
		t.Fatalf("count feed sources: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one source for duplicate urls, got %d", count)
	}
}

func TestImportOPMLStillCreatesUserSubscriptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/opml/import", withFeedAuth(user.UUID, ImportOPML(db)))

	opml := `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0">
  <body>
    <outline text="User Feed" type="rss" xmlUrl="https://example.com/user.xml" />
  </body>
</opml>`

	req := newOPMLUploadRequest(t, "/api/v1/feed/opml/import", opml)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if count := countSubscriptionsForUser(t, db, user.UUID); count != 1 {
		t.Fatalf("expected 1 subscription after legacy OPML import, got %d", count)
	}
	var payload struct {
		Message  string `json:"message"`
		Imported int    `json:"imported"`
		Reused   int    `json:"reused"`
		Failed   int    `json:"failed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode OPML import response: %v", err)
	}
	if payload.Imported != 1 || payload.Reused != 0 || payload.Failed != 0 {
		t.Fatalf("unexpected OPML import response: %#v", payload)
	}
}

func TestSubscribeChannelAcceptsExternalFeedSourceID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	if err := db.AutoMigrate(&model.Channel{}); err != nil {
		t.Fatalf("migrate channels: %v", err)
	}
	user := seedFeedTestUser(t, db)
	source := model.FeedSource{
		SourceType:   "external_rss",
		Title:        "Recommended RSS",
		RssURL:       "https://example.com/recommended.xml",
		CanonicalURL: "https://example.com/recommended.xml",
		Hash:         buildFeedSourceHash("external_rss", nil, "https://example.com/recommended.xml"),
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create feed source: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscribe/channel/:channel_id", withFeedAuth(user.UUID, SubscribeChannel(db)))
	feed.GET("/subscribe/channel/:channel_id/status", withFeedAuth(user.UUID, CheckChannelSubscription(db)))
	feed.DELETE("/subscribe/channel/:channel_id", withFeedAuth(user.UUID, UnsubscribeChannel(db)))

	statusBefore := httptest.NewRecorder()
	router.ServeHTTP(statusBefore, httptest.NewRequest(http.MethodGet, "/api/v1/feed/subscribe/channel/"+source.ID.String()+"/status", nil))
	if statusBefore.Code != http.StatusOK || !strings.Contains(statusBefore.Body.String(), `"subscribed":false`) {
		t.Fatalf("expected unsubscribed status for source id, got %d %s", statusBefore.Code, statusBefore.Body.String())
	}

	subscribe := httptest.NewRecorder()
	router.ServeHTTP(subscribe, httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscribe/channel/"+source.ID.String(), nil))
	if subscribe.Code != http.StatusOK {
		t.Fatalf("expected subscribe source status %d, got %d with body %s", http.StatusOK, subscribe.Code, subscribe.Body.String())
	}

	var activeCount int64
	if err := db.Model(&model.Subscription{}).
		Where("user_id = ? AND feed_source_id = ?", user.UUID, source.ID).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("expected source subscription, got %d", activeCount)
	}

	statusAfter := httptest.NewRecorder()
	router.ServeHTTP(statusAfter, httptest.NewRequest(http.MethodGet, "/api/v1/feed/subscribe/channel/"+source.ID.String()+"/status", nil))
	if statusAfter.Code != http.StatusOK || !strings.Contains(statusAfter.Body.String(), `"subscribed":true`) {
		t.Fatalf("expected subscribed status for source id, got %d %s", statusAfter.Code, statusAfter.Body.String())
	}

	unsubscribe := httptest.NewRecorder()
	router.ServeHTTP(unsubscribe, httptest.NewRequest(http.MethodDelete, "/api/v1/feed/subscribe/channel/"+source.ID.String(), nil))
	if unsubscribe.Code != http.StatusOK {
		t.Fatalf("expected unsubscribe source status %d, got %d with body %s", http.StatusOK, unsubscribe.Code, unsubscribe.Body.String())
	}
	if err := db.Model(&model.Subscription{}).
		Where("user_id = ? AND feed_source_id = ?", user.UUID, source.ID).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count subscriptions after unsubscribe: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("expected source subscription removed, got %d", activeCount)
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

func TestDiscoverFeedCandidatesAcceptsDirectFeedURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/discover", withFeedAuth(user.UUID, DiscoverFeedCandidates()))

	body := strings.NewReader(`{"url":"http://www.ruanyifeng.com/blog/atom.xml"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/discover", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"feed_url":"http://www.ruanyifeng.com/blog/atom.xml"`) {
		t.Fatalf("expected direct feed url candidate, got body %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"is_default":true`) {
		t.Fatalf("expected direct feed url candidate to be default, got body %s", rr.Body.String())
	}
}

func TestDiscoverFeedCandidatesFetchesWebsiteAlternateFeeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", req.Method)
		}
		if req.URL.String() != "https://example.com/blog" {
			t.Fatalf("expected request to target website, got %s", req.URL.String())
		}
		if req.Header.Get("User-Agent") == "" {
			t.Fatal("expected feed discovery request to set a user agent")
		}
		html := `<html><head>
			<link rel="alternate" type="application/rss+xml" title="Main Feed" href="/feed.xml">
			<link rel="alternate" type="application/atom+xml" title="Updates" href="atom.xml">
		</head></html>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(html)),
			Request:    req,
		}, nil
	})}
	resolveFeedDiscoveryHostname = func(host string) ([]net.IP, error) {
		if host != "example.com" {
			t.Fatalf("expected resolver to check example.com, got %s", host)
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() {
		feedDiscoveryHTTPClient = originalClient
		resolveFeedDiscoveryHostname = originalResolver
	}()

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/discover", withFeedAuth(user.UUID, DiscoverFeedCandidates()))

	body := strings.NewReader(`{"url":"https://example.com/blog"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/discover", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload struct {
		Candidates []service.FeedDiscoveryCandidate `json:"candidates"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Candidates) != 2 {
		t.Fatalf("expected 2 discovered candidates, got %d with body %s", len(payload.Candidates), rr.Body.String())
	}
	if payload.Candidates[0].FeedURL != "https://example.com/feed.xml" {
		t.Fatalf("expected main feed first, got %q", payload.Candidates[0].FeedURL)
	}
	if !payload.Candidates[0].IsDefault {
		t.Fatal("expected first discovered candidate to be default")
	}
}

func TestDiscoverFeedCandidatesReturnsEmptyArrayWhenWebsiteHasNoFeeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(`<html><head><title>No Feed</title></head></html>`)),
			Request:    req,
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
	feed.POST("/discover", withFeedAuth(user.UUID, DiscoverFeedCandidates()))

	body := strings.NewReader(`{"url":"https://example.com/no-feed"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/discover", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"candidates":[]`) {
		t.Fatalf("expected empty candidates array, got body %s", rr.Body.String())
	}
}

func TestDiscoverFeedCandidatesBlocksPrivateNetworkFetches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	called := false
	originalClient := feedDiscoveryHTTPClient
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(`<html></html>`)),
			Request:    req,
		}, nil
	})}
	defer func() {
		feedDiscoveryHTTPClient = originalClient
	}()

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/discover", withFeedAuth(user.UUID, DiscoverFeedCandidates()))

	body := strings.NewReader(`{"url":"http://127.0.0.1/page"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/discover", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if called {
		t.Fatal("expected private network URL to be rejected before HTTP fetch")
	}
	if !strings.Contains(rr.Body.String(), "url is not allowed for feed discovery") {
		t.Fatalf("expected blocked URL message, got body %s", rr.Body.String())
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

func TestFindOrCreateFeedSourceDoesNotLogRecordNotFoundOnExpectedMiss(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	db := newFeedHandlerTestDBWithLogBuffer(t, &logs)
	logs.Reset()

	source, err := findOrCreateFeedSource(db, "external_rss", nil, "https://example.com/feed.xml", "Example Feed", "")
	if err != nil {
		t.Fatalf("findOrCreateFeedSource returned error: %v", err)
	}
	if source == nil {
		t.Fatal("expected created feed source, got nil")
	}
	if strings.Contains(strings.ToLower(logs.String()), "record not found") {
		t.Fatalf("expected no record not found log on expected miss, got logs: %s", logs.String())
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

func TestCreateSubscriptionAllowsResubscribeAfterSoftDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions", withFeedAuth(user.UUID, CreateSubscription(db)))
	feed.DELETE("/subscriptions/:id", withFeedAuth(user.UUID, DeleteSubscription(db)))

	first := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions", strings.NewReader(`{"target_type":"external_rss","rss_url":"https://example.com/feed.xml","title":"Example Feed"}`))
	first.Header.Set("Content-Type", "application/json")
	firstRR := httptest.NewRecorder()
	router.ServeHTTP(firstRR, first)
	if firstRR.Code != http.StatusCreated {
		t.Fatalf("expected first subscription status %d, got %d with body %s", http.StatusCreated, firstRR.Code, firstRR.Body.String())
	}

	var firstPayload struct {
		Data model.Subscription `json:"data"`
	}
	if err := json.Unmarshal(firstRR.Body.Bytes(), &firstPayload); err != nil {
		t.Fatalf("decode first subscription: %v", err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/feed/subscriptions/"+firstPayload.Data.ID.String(), nil)
	deleteRR := httptest.NewRecorder()
	router.ServeHTTP(deleteRR, deleteReq)
	if deleteRR.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, deleteRR.Code, deleteRR.Body.String())
	}

	second := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions", strings.NewReader(`{"target_type":"external_rss","rss_url":"https://example.com/feed.xml","title":"Example Feed"}`))
	second.Header.Set("Content-Type", "application/json")
	secondRR := httptest.NewRecorder()
	router.ServeHTTP(secondRR, second)
	if secondRR.Code != http.StatusCreated {
		t.Fatalf("expected resubscribe status %d, got %d with body %s", http.StatusCreated, secondRR.Code, secondRR.Body.String())
	}

	var activeCount int64
	if err := db.Model(&model.Subscription{}).
		Where("user_id = ? AND feed_source_id = ?", user.UUID, firstPayload.Data.FeedSourceID).
		Count(&activeCount).Error; err != nil {
		t.Fatalf("count active subscriptions: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("expected 1 active subscription, got %d", activeCount)
	}
}

func TestCreateSubscriptionMapsDuplicateToAlreadySubscribed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions", withFeedAuth(user.UUID, CreateSubscription(db)))

	first := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions", strings.NewReader(`{"target_type":"external_rss","rss_url":"https://example.com/race.xml"}`))
	first.Header.Set("Content-Type", "application/json")
	firstRR := httptest.NewRecorder()
	router.ServeHTTP(firstRR, first)
	if firstRR.Code != http.StatusCreated {
		t.Fatalf("expected first subscription status %d, got %d with body %s", http.StatusCreated, firstRR.Code, firstRR.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions", strings.NewReader(`{"target_type":"external_rss","rss_url":"https://example.com/race.xml"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Already subscribed to this source") {
		t.Fatalf("expected already subscribed message, got body %s", rr.Body.String())
	}
}

func TestCreateSubscriptionTreatsAPIV1FeedRSSAsInternalUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)
	author := seedFeedTestUser(t, db)
	author.Username = "alice"
	if err := db.Model(&author).Update("username", "alice").Error; err != nil {
		t.Fatalf("rename author: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions", withFeedAuth(user.UUID, CreateSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions", strings.NewReader(`{"target_type":"external_rss","rss_url":"https://example.com/api/v1/feed/rss/alice"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var source model.FeedSource
	if err := db.First(&source).Error; err != nil {
		t.Fatalf("load source: %v", err)
	}
	if source.SourceType != "internal_user" {
		t.Fatalf("expected internal_user source type, got %q", source.SourceType)
	}
	if source.SourceID == nil || *source.SourceID != author.UUID {
		t.Fatalf("expected source_id %s, got %#v", author.UUID, source.SourceID)
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
	body := rr.Body.String()
	if strings.Contains(body, "00000000-0000-0000-0000-000000000000") {
		t.Fatalf("expected new source response to omit zero UUID, got body %s", body)
	}
	if !strings.Contains(body, `"candidates":[]`) {
		t.Fatalf("expected stable empty candidates array, got body %s", body)
	}
	if !strings.Contains(body, `"subscription":null`) {
		t.Fatalf("expected absent subscription to be null, got body %s", body)
	}

	var payload AutoSubscriptionResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != "new_source" {
		t.Fatalf("expected status new_source, got %q", payload.Status)
	}
	if payload.Source == nil {
		t.Fatal("expected source")
	}
	if payload.Source.Provider != "rsshub" {
		t.Fatalf("expected provider rsshub, got %q", payload.Source.Provider)
	}
	if payload.Source.RssURL != "https://rsshub.app/github/repo/DIYgod/RSSHub" {
		t.Fatalf("expected rsshub github repo url, got %q", payload.Source.RssURL)
	}
	if payload.Source.SiteURL != "https://github.com/DIYgod/RSSHub" {
		t.Fatalf("expected github site url, got %q", payload.Source.SiteURL)
	}
	if payload.Source.Category != "blog" {
		t.Fatalf("expected github repo category blog, got %q", payload.Source.Category)
	}
}

func TestResolveSubscriptionInputInvalidURLReturnsStatusResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/resolve", withFeedAuth(user.UUID, ResolveSubscriptionInput(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/resolve", strings.NewReader(`{"input":"not a url"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}

	var payload AutoSubscriptionResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != "invalid" {
		t.Fatalf("expected status invalid, got %q", payload.Status)
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
		HealthStatus: "healthy",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	subscription := model.Subscription{
		UserID:       user.UUID,
		FeedSourceID: source.ID,
		Title:        "Example Feed",
	}
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
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != "already_subscribed" {
		t.Fatalf("expected status already_subscribed, got %q", payload.Status)
	}
	if payload.Subscription == nil {
		t.Fatal("expected subscription")
	}
	if payload.Subscription.ID != subscription.ID {
		t.Fatalf("expected subscription ID %s, got %s", subscription.ID, payload.Subscription.ID)
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
		HealthStatus: "healthy",
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
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != "existing_source" {
		t.Fatalf("expected status existing_source, got %q", payload.Status)
	}
	if payload.Source == nil {
		t.Fatal("expected source")
	}
	if payload.Source.ID == nil {
		t.Fatal("expected source ID")
	}
	if *payload.Source.ID != source.ID {
		t.Fatalf("expected source ID %s, got %s", source.ID, *payload.Source.ID)
	}
	if payload.Source.Category != "blog" {
		t.Fatalf("expected existing source category blog, got %q", payload.Source.Category)
	}
}

func TestResolveSubscriptionInputReturnsMultipleCandidatesForWebsite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	originalClient := feedDiscoveryHTTPClient
	feedDiscoveryHTTPClient = &http.Client{
		Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			html := `<html><head>
<link rel="alternate" type="application/rss+xml" title="Main Feed" href="/feed.xml">
<link rel="alternate" type="application/atom+xml" title="Updates" href="/updates.atom">
</head><body></body></html>`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader(html)),
				Request:    req,
			}, nil
		}),
	}
	defer func() {
		feedDiscoveryHTTPClient = originalClient
	}()

	originalResolver := resolveFeedDiscoveryHostname
	resolveFeedDiscoveryHostname = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() {
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
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != "multiple_candidates" {
		t.Fatalf("expected status multiple_candidates, got %q", payload.Status)
	}
	if len(payload.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(payload.Candidates))
	}
	if payload.Candidates[0].Status != "new_source" {
		t.Fatalf("expected first candidate status new_source, got %q", payload.Candidates[0].Status)
	}
}

func TestResolveSubscriptionInputAcceptsDirectRSSWithoutFeedPathSuffix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)

	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://example.com/frontpage" {
			t.Fatalf("expected direct feed probe, got %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml"}},
			Body:       io.NopCloser(strings.NewReader(`<?xml version="1.0"?><rss version="2.0"><channel><title>Frontpage</title></channel></rss>`)),
			Request:    req,
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/resolve", strings.NewReader(`{"input":"https://example.com/frontpage"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rr.Code, rr.Body.String())
	}
	var payload AutoSubscriptionResolveResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Status != "new_source" {
		t.Fatalf("expected status new_source, got %q with body %s", payload.Status, rr.Body.String())
	}
	if payload.Source == nil || payload.Source.RssURL != "https://example.com/frontpage" {
		t.Fatalf("expected direct rss source, got %#v", payload.Source)
	}
	if len(payload.Candidates) != 0 {
		t.Fatalf("expected no candidates for direct rss, got %d", len(payload.Candidates))
	}
}

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

	var sources []model.FeedSource
	if err := db.Find(&sources).Error; err != nil {
		t.Fatalf("load feed sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 feed source, got %d", len(sources))
	}
	source := sources[0]
	if source.Provider != "rsshub" {
		t.Fatalf("expected provider rsshub, got %q", source.Provider)
	}
	if source.RssURL != "https://rsshub.app/github/repo/DIYgod/RSSHub" {
		t.Fatalf("expected rsshub github repo url, got %q", source.RssURL)
	}
	if source.SiteURL != "https://github.com/DIYgod/RSSHub" {
		t.Fatalf("expected github site url, got %q", source.SiteURL)
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
		HealthStatus: "healthy",
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

	var count int64
	if err := db.Model(&model.FeedSource{}).Count(&count).Error; err != nil {
		t.Fatalf("count feed sources: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected feed source count to remain 1, got %d", count)
	}
}

func TestAutoAddSubscriptionDoesNotFetchSourceTitleDuringCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	fetched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Remote Title</title></channel></rss>`))
	}))
	defer server.Close()

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(fmt.Sprintf(`{"input":%q,"title":"Request Title"}`, server.URL+"/feed.xml")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	if fetched {
		t.Fatal("expected auto-add source creation not to fetch RSS metadata synchronously")
	}

	var source model.FeedSource
	if err := db.First(&source).Error; err != nil {
		t.Fatalf("load feed source: %v", err)
	}
	if source.Title != "Request Title" {
		t.Fatalf("expected fallback title to be preserved, got %q", source.Title)
	}
}

func TestAutoAddSubscriptionAcceptsDirectRSSWithoutFeedPathSuffix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	originalClient := feedDiscoveryHTTPClient
	originalResolver := resolveFeedDiscoveryHostname
	feedDiscoveryHTTPClient = &http.Client{Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml"}},
			Body:       io.NopCloser(strings.NewReader(`<?xml version="1.0"?><rss version="2.0"><channel><title>Frontpage</title></channel></rss>`)),
			Request:    req,
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

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://example.com/frontpage","title":"Frontpage"}`))
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
	if source.RssURL != "https://example.com/frontpage" {
		t.Fatalf("expected unsuffixed direct rss url, got %q", source.RssURL)
	}
}

func TestAutoAddSubscriptionRequiresCandidateWhenMultipleFeedsExist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	originalClient := feedDiscoveryHTTPClient
	feedDiscoveryHTTPClient = &http.Client{
		Transport: feedDiscoveryRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			html := `<html><head>
<link rel="alternate" type="application/rss+xml" title="Main Feed" href="/feed.xml">
<link rel="alternate" type="application/atom+xml" title="Updates" href="/updates.atom">
</head><body></body></html>`
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader(html)),
				Request:    req,
			}, nil
		}),
	}
	defer func() {
		feedDiscoveryHTTPClient = originalClient
	}()

	originalResolver := resolveFeedDiscoveryHostname
	resolveFeedDiscoveryHostname = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	defer func() {
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
		t.Fatalf("load feed source: %v", err)
	}
	if source.RssURL != "https://example.com/feed.xml" {
		t.Fatalf("expected selected candidate rss url, got %q", source.RssURL)
	}
}

func TestAutoAddSubscriptionStoresSelectedCategory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://example.com/forum","candidate_feed_url":"https://example.com/forum/feed.xml","title":"Forum Feed","category":"forum"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusCreated, rr.Code, rr.Body.String())
	}

	var source model.FeedSource
	if err := db.First(&source).Error; err != nil {
		t.Fatalf("load feed source: %v", err)
	}
	if source.Category != "forum" {
		t.Fatalf("expected selected category forum, got %q", source.Category)
	}
}

func TestAutoAddSubscriptionRejectsAlreadySubscribedSource(t *testing.T) {
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
		HealthStatus: "healthy",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}
	subscription := model.Subscription{
		UserID:       user.UUID,
		FeedSourceID: source.ID,
		Title:        "Example Feed",
	}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://example.com/feed.xml/"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Already subscribed to this source") {
		t.Fatalf("expected already subscribed message, got body %s", rr.Body.String())
	}
}

func TestAutoAddSubscriptionMapsDuplicateSubscriptionInsertToConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	db.Config.Logger = gormlogger.Default.LogMode(gormlogger.Silent)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	source := model.FeedSource{
		SourceType:      "external_rss",
		Provider:        "rss",
		RssURL:          "https://example.com/race.xml",
		CanonicalURL:    "https://example.com/race.xml",
		Hash:            buildFeedSourceHash("external_rss", nil, "https://example.com/race.xml"),
		Title:           "Race Feed",
		HealthStatus:    "healthy",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create source: %v", err)
	}

	insertedCompetingSubscription := false
	callbackName := "auto_add_duplicate_subscription_race"
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "subscriptions" || insertedCompetingSubscription {
			return
		}
		insertedCompetingSubscription = true
		competing := model.Subscription{
			UserID:       user.UUID,
			FeedSourceID: source.ID,
			Title:        "Competing",
		}
		if err := tx.Session(&gorm.Session{NewDB: true, SkipHooks: true}).Create(&competing).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatalf("register create callback: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Callback().Create().Remove(callbackName); err != nil {
			t.Fatalf("remove create callback: %v", err)
		}
	})

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://example.com/race.xml"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "Already subscribed to this source") {
		t.Fatalf("expected already subscribed message, got body %s", rr.Body.String())
	}
}

func TestAutoAddSubscriptionRejectsHostlessCandidateFeedURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://example.com","candidate_feed_url":"https:/feed.xml"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "candidate_feed_url must be an absolute http/https URL") {
		t.Fatalf("expected absolute http/https candidate message, got body %s", rr.Body.String())
	}
}

func TestAutoAddSubscriptionRejectsHostlessDirectFeedInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https:/feed.xml"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "input must be an absolute http/https URL") {
		t.Fatalf("expected absolute http/https input message, got body %s", rr.Body.String())
	}
}

func TestAutoAddSubscriptionRejectsGithubRepositoryWithEncodedSlashSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://github.com/DIYgod%2Fbad/RSSHub"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "input must be an absolute http/https URL") {
		t.Fatalf("expected invalid input message, got body %s", rr.Body.String())
	}
}

func TestAutoAddSubscriptionRejectsGithubRepositoryWithEncodedURLDelimiterSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	disableFeedSourceSync(t)
	user := seedFeedTestUser(t, db)

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.POST("/subscriptions/auto-add", withFeedAuth(user.UUID, AutoAddSubscription(db)))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/feed/subscriptions/auto-add", strings.NewReader(`{"input":"https://github.com/DIYgod/RSSHub%3Fbad"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "input must be an absolute http/https URL") {
		t.Fatalf("expected invalid input message, got body %s", rr.Body.String())
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

func TestGetStarredItemsReturnsStableTotalAcrossPages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	if err := db.AutoMigrate(&model.FeedStarGroup{}, &model.FeedItemStar{}); err != nil {
		t.Fatalf("migrate starred models: %v", err)
	}

	user := seedFeedTestUser(t, db)
	source := seedAdminFeedSource(t, db, "Starred Feed", false)
	now := time.Now().UTC().Truncate(time.Second)

	for i := 1; i <= 3; i++ {
		item := model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         fmt.Sprintf("star-guid-%d", i),
			Title:        fmt.Sprintf("Starred Item %d", i),
			Link:         fmt.Sprintf("https://example.com/items/%d", i),
			PublishedAt:  now.Add(-time.Duration(i) * time.Minute),
			FetchedAt:    now,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create feed item %d: %v", i, err)
		}
		if err := db.Create(&model.FeedItemStar{
			UserID:     user.UUID,
			FeedItemID: item.ID,
			StarredAt:  now.Add(-time.Duration(i) * time.Minute),
		}).Error; err != nil {
			t.Fatalf("create star %d: %v", i, err)
		}
	}

	router := gin.New()
	router.GET("/api/v1/feed/stars", withFeedAuth(user.UUID, GetStarredItems(db)))

	type response struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		Page  int `json:"page"`
		Total int `json:"total"`
	}

	requestPage := func(page int) response {
		t.Helper()

		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/feed/stars?page=%d&limit=2", page), nil)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("page %d: expected status 200, got %d with body %s", page, rr.Code, rr.Body.String())
		}

		var payload response
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("page %d: decode response: %v", page, err)
		}
		return payload
	}

	page1 := requestPage(1)
	if page1.Total != 3 {
		t.Fatalf("expected page 1 total 3, got %d body=%+v", page1.Total, page1)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("expected page 1 to return 2 items, got %d", len(page1.Items))
	}

	page2 := requestPage(2)
	if page2.Total != 3 {
		t.Fatalf("expected page 2 total 3, got %d body=%+v", page2.Total, page2)
	}
	if len(page2.Items) != 1 {
		t.Fatalf("expected page 2 to return 1 item, got %d", len(page2.Items))
	}
}

func TestGetStarredItemsNormalizesInvalidPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)
	if err := db.AutoMigrate(&model.FeedStarGroup{}, &model.FeedItemStar{}); err != nil {
		t.Fatalf("migrate starred models: %v", err)
	}
	user := seedFeedTestUser(t, db)

	router := gin.New()
	router.GET("/api/v1/feed/stars", withFeedAuth(user.UUID, GetStarredItems(db)))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/stars?page=0&limit=-1", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Page int `json:"page"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Page != 1 {
		t.Fatalf("expected normalized page 1, got %d", payload.Page)
	}
}

package feed

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRSSRecentHTTPFallbackRejectsOverloadAndHonorsCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, _, _ := newFeedTestService(t)
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_RSS_HTTP_EXPERIMENT", "yes")
	t.Setenv("FEED_SNAPSHOT_READ_EXPERIMENT", "")
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), s)
	request := func(ctx context.Context) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/rss/recent?page=1&page_size=2", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	for i := 0; i < rssLegacyConcurrentScans; i++ {
		rssLegacyScanSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < rssLegacyConcurrentScans; i++ {
			<-rssLegacyScanSlots
		}
	}()
	busy := request(context.Background())
	if busy.Code != http.StatusServiceUnavailable || busy.Header().Get("Retry-After") != "30" || !strings.Contains(busy.Body.String(), "feed.rss_snapshot_unavailable") {
		t.Fatalf("overloaded legacy scan was not rejected: status=%d header=%q body=%s", busy.Code, busy.Header().Get("Retry-After"), busy.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gone := request(ctx)
	if gone.Code != 499 {
		t.Fatalf("canceled request returned %d, wanted 499", gone.Code)
	}
}

// A blocked PostgreSQL read must not tie up the HTTP request indefinitely.
// The row cap alone cannot protect against database lock contention.
func TestRSSRecentHTTPFallbackDatabaseLockHasDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, db, _ := newFeedTestService(t)
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_RSS_HTTP_EXPERIMENT", "yes")
	t.Setenv("FEED_SNAPSHOT_READ_EXPERIMENT", "")
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec("LOCK TABLE feed_items IN ACCESS EXCLUSIVE MODE").Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), s)
	rec := httptest.NewRecorder()
	started := time.Now()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/feed/rss/recent", nil))
	elapsed := time.Since(started)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("blocked PostgreSQL fallback returned HTTP %d after %s: %s", rec.Code, elapsed, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "feed.rss_snapshot_unavailable") {
		t.Fatalf("wrong error code on timed-out fallback: %s", rec.Body.String())
	}
	if elapsed > 5*time.Second {
		t.Fatalf("legacy fallback did not terminate promptly: %s", elapsed)
	}
}

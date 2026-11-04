package feed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestRSSRecentHTTPRealParityFallbackAndConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s, db, user := newFeedTestService(t)
	if err := db.Exec("DELETE FROM feed_item_reads").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM feed_items").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&feedDedupSnapshotHead{}, &feedDedupSnapshotGeneration{}, &feedDedupSnapshotRow{}, &feedDedupSnapshotMember{}); err != nil {
		t.Fatal(err)
	}
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	var rss model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&rss).Error; err != nil {
		t.Fatal(err)
	}
	mirror := model.FeedSource{SourceType: "external_rss", Title: "Mirrored RSS", Hash: uuid.NewString(), RssURL: "https://example.invalid/mirror"}
	internal := model.FeedSource{SourceType: "internal_feed", Title: "Internal", Hash: uuid.NewString(), RssURL: "https://example.invalid/internal"}
	for _, v := range []*model.FeedSource{&mirror, &internal} {
		if err := db.Create(v).Error; err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	var first uuid.UUID
	for i := 0; i < 16; i++ {
		source := rss.ID
		if i%2 != 0 {
			source = mirror.ID
		}
		link := fmt.Sprintf("https://example.invalid/rss/%d", i)
		if i%5 == 1 {
			link = fmt.Sprintf("https://example.invalid/rss/%d?utm_medium=test", i-1)
		}
		v := model.FeedItem{FeedSourceID: source, GUID: fmt.Sprintf("rss-http-%d", i), Title: fmt.Sprintf("RSS %d 中文 🔎", i), Link: link, FullTextHTML: fmt.Sprintf("<p>full %d</p>", i), ReaderHTML: fmt.Sprintf("<p>reader %d</p>", i), PublishedAt: base.Add(-time.Duration(i) * time.Minute)}
		if err := db.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = v.ID
		}
	}
	other := model.FeedItem{FeedSourceID: internal.ID, GUID: "internal-only", Title: "NON_RSS_DO_NOT_EXPOSE", Link: "https://example.invalid/rss/0", PublishedAt: base.Add(time.Hour)}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_RSS_HTTP_EXPERIMENT", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	t.Setenv("FEED_SNAPSHOT_READ_EXPERIMENT", "yes")
	count, err := s.repo.publishPublicRecentDedupSnapshotTracked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count < 10 || count >= 16 {
		t.Fatalf("unexpected RSS group count %d", count)
	}
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), s)
	token := signedFeedHTTPTokenForTest(t, db, user)
	endpoint := "/api/v1/feed/rss/recent"
	request := func(path string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	assertParity := func(path, expectedPath string) {
		t.Helper()
		rec := request(path, false)
		if rec.Code != 200 {
			t.Fatalf("HTTP status=%d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("X-Feed-Isolated-Read-Path"); got != expectedPath {
			t.Fatalf("read path=%q expected=%q", got, expectedPath)
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", path, nil)
		page, size := normalizedPageFromQuery(c), normalizedPageSizeFromQuery(c)
		expected, total, err := s.repo.listPublicRSSRecentLegacy(context.Background(), page, size)
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			Data []TimelineItemDTO `json:"data"`
			Meta httpx.PageMeta    `json:"meta"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		gotJSON, _ := json.Marshal(payload.Data)
		wantJSON, _ := json.Marshal(publicRSSDTO(expected))
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Fatalf("different full DTO page=%d size=%d\ngot=%s\nwant=%s", page, size, gotJSON, wantJSON)
		}
		if payload.Meta.Total != total || payload.Meta.Page != page || payload.Meta.PageSize != size {
			t.Fatalf("meta mismatch: %+v total=%d", payload.Meta, total)
		}
		for _, v := range payload.Data {
			if v.FeedItem == nil || v.FeedItem.FeedSource == nil || v.FeedItem.FeedSource.SourceType != "external_rss" || v.Post != nil {
				t.Fatalf("not RSS-only: %+v", v)
			}
		}
	}
	for _, size := range []int{1, 2, 5, 20, 100} {
		for _, page := range []int{1, 2, 4, 100} {
			assertParity(fmt.Sprintf("%s?page=%d&page_size=%d&sort=recent&hide_duplicates=true", endpoint, page, size), "snapshot")
		}
	}
	assertParity(endpoint, "snapshot")
	t.Setenv("FEED_SNAPSHOT_RSS_HTTP_EXPERIMENT", "")
	if rec := request(endpoint, false); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled endpoint should be 404, got %d", rec.Code)
	}
	t.Setenv("FEED_SNAPSHOT_RSS_HTTP_EXPERIMENT", "yes")
	invalid := []string{"?sort=popular", "?hide_duplicates=false", "?page=0", "?page=abc", "?page_size=101", "?page_size=0", "?page=1&page=2", "?q=private", "?source_id=" + rss.ID.String(), "?is_read=false", "?unread_only=true", "?source_type=external_rss", "?category=blog", "?language=en", "?future_filter=x"}
	for _, q := range invalid {
		rec := request(endpoint+q, false)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("unsafe query %s accepted: %d", q, rec.Code)
		}
	}
	authRec := request(endpoint+"?page=1&page_size=2", true)
	if authRec.Code != 200 || authRec.Header().Get("X-Feed-Isolated-Read-Path") != "" {
		t.Fatal("authenticated request entered snapshot")
	}
	t.Setenv("FEED_SNAPSHOT_READ_EXPERIMENT", "")
	assertParity(endpoint+"?page=2&page_size=3", "")
	t.Setenv("FEED_SNAPSHOT_READ_EXPERIMENT", "yes")
	if err := db.Model(&model.FeedItem{}).Where("id = ?", first).Update("title", "RSS UPDATED AFTER SNAPSHOT").Error; err != nil {
		t.Fatal(err)
	}
	assertParity(endpoint+"?page=1&page_size=3", "fallback")
	if err := db.Where("scope = ?", publicRecentDedupScope).Delete(&feedDedupSnapshotHead{}).Error; err != nil {
		t.Fatal(err)
	}
	assertParity(endpoint+"?page=1&page_size=3", "fallback")
	if _, err := s.repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	problems := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := request(fmt.Sprintf("%s?page=%d&page_size=3", endpoint, i%5+1), false)
			if rec.Code != 200 {
				problems <- fmt.Errorf("concurrent status %d", rec.Code)
				return
			}
			var p struct {
				Data []TimelineItemDTO `json:"data"`
				Meta httpx.PageMeta    `json:"meta"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				problems <- err
				return
			}
			if len(p.Data) > 3 || p.Meta.Total != count {
				problems <- fmt.Errorf("wrong page count %d/%d", len(p.Data), p.Meta.Total)
				return
			}
			for _, v := range p.Data {
				if v.FeedItem == nil || v.FeedItem.FeedSource == nil || v.FeedItem.FeedSource.SourceType != "external_rss" || strings.Contains(v.FeedItem.Title, "NON_RSS") {
					problems <- fmt.Errorf("bad RSS provenance")
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(problems)
	for err := range problems {
		t.Error(err)
	}
}

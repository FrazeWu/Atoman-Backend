package feed

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRSSRecentHTTPAtomicDuringRealCOPYPublish(t *testing.T) {
	repo, db, _, _, _ := copyCancelFixture(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FEED_SCALE_ISOLATED", "yes")
	t.Setenv("FEED_SNAPSHOT_COPY_EXPERIMENT", "yes")
	t.Setenv("FEED_SNAPSHOT_RSS_HTTP_EXPERIMENT", "yes")
	t.Setenv("FEED_SNAPSHOT_READ_EXPERIMENT", "yes")
	if _, err := repo.publishPublicRecentDedupSnapshotTracked(context.Background()); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(6)
	var old feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).First(&old).Error; err != nil {
		t.Fatal(err)
	}
	var rows []feedDedupSnapshotRow
	var members []feedDedupSnapshotMember
	if err := db.Where("generation_id=?", old.GenerationID).Order("position").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("generation_id=?", old.GenerationID).Find(&members).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || len(members) == 0 {
		t.Fatal("missing initial rows")
	}
	gen := uuid.New()
	for i := range rows {
		rows[i].GenerationID = gen
	}
	for i := range members {
		members[i].GenerationID = gen
	}
	next := old
	next.GenerationID = gen
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), &Service{db: db, repo: repo})
	readHTTP := func() error {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/feed/rss/recent?page=1&page_size=20", nil))
		if rec.Code != 200 {
			return fmt.Errorf("HTTP %d: %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Data []TimelineItemDTO `json:"data"`
			Meta struct {
				Total int64 `json:"total"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			return err
		}
		if body.Meta.Total != old.Total || int64(len(body.Data)) != old.Total {
			return fmt.Errorf("partial result: items=%d total=%d wanted=%d", len(body.Data), body.Meta.Total, old.Total)
		}
		for _, item := range body.Data {
			if item.FeedItem == nil || item.FeedItem.FeedSource == nil || item.FeedItem.FeedSource.SourceType != "external_rss" {
				return fmt.Errorf("unexpected provenance")
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ready, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	defer unlock()
	done := make(chan error, 1)
	go func() {
		done <- repo.publishDedupSnapshotViaCopyExperiment(ctx, gen, rows, members, next, func(stage string, _ time.Time) {
			if stage == "insert_members" {
				close(ready)
				select {
				case <-release:
				case <-ctx.Done():
				}
			}
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatalf("publisher never reached precommit: %v", ctx.Err())
	}
	var before feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&before).Error; err != nil {
		t.Fatal(err)
	}
	if before.GenerationID != old.GenerationID {
		t.Fatal("uncommitted head visible")
	}
	var unseen int64
	if err := db.Model(&feedDedupSnapshotRow{}).Where("generation_id=?", gen).Count(&unseen).Error; err != nil {
		t.Fatal(err)
	}
	if unseen != 0 {
		t.Fatalf("uncommitted rows visible: %d", unseen)
	}
	for i := 0; i < 3; i++ {
		if err := readHTTP(); err != nil {
			t.Fatalf("pre-commit read: %v", err)
		}
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("copy publish: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("copy commit deadline: %v", ctx.Err())
	}
	var after feedDedupSnapshotHead
	if err := db.Where("scope=?", publicRecentDedupScope).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.GenerationID != gen {
		t.Fatal("published head missing")
	}
	for i := 0; i < 3; i++ {
		if err := readHTTP(); err != nil {
			t.Fatalf("post-commit read: %v", err)
		}
	}
}

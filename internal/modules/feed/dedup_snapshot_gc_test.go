package feed

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestDedupSnapshotPrunesOnlyInactiveOldGenerations(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	ctx := context.Background()
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "gc-article", Title: "An article for projection garbage collection", Link: "https://gc.example.com/article", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshot(ctx, "gc-revision-old"); err != nil {
		t.Fatal(err)
	}
	var oldHead feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&oldHead).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&feedDedupSnapshotGeneration{}).Where("generation_id = ?", oldHead.GenerationID).
		Update("built_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.publishPublicRecentDedupSnapshot(ctx, "gc-revision-current"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.pruneInactiveDedupSnapshotGenerations(ctx, time.Minute); err == nil {
		t.Fatal("unsafe short grace period accepted")
	}
	removed, err := repo.pruneInactiveDedupSnapshotGenerations(ctx, time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("expected one expired generation removed, removed=%d err=%v", removed, err)
	}
	var oldCount int64
	if err := db.Model(&feedDedupSnapshotRow{}).Where("generation_id = ?", oldHead.GenerationID).Count(&oldCount).Error; err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatalf("old generation rows remain: %d", oldCount)
	}
	var memberCount int64
	if err := db.Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", oldHead.GenerationID).Count(&memberCount).Error; err != nil {
		t.Fatal(err)
	}
	if memberCount != 0 {
		t.Fatalf("old generation member metadata remain: %d", memberCount)
	}
	current, count, err := repo.readPublicRecentDedupSnapshotPage(ctx, "gc-revision-current", 1, 10)
	if err != nil || count != 1 || len(current) != 1 || current[0].ID != item.ID {
		t.Fatalf("active projection removed: rows=%+v count=%d err=%v", current, count, err)
	}
	removed, err = repo.pruneInactiveDedupSnapshotGenerations(ctx, time.Hour)
	if err != nil || removed != 0 {
		t.Fatalf("repeat cleanup was not idempotent: removed=%d err=%v", removed, err)
	}
}

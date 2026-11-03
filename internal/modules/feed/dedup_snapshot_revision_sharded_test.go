package feed

import (
	"context"
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	"gorm.io/gorm"
)

func TestDedupSourceRevisionsOneBumpPerStatement(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	initial, err := repo.dedupSourceRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	articles := make([]model.FeedItem, 200)
	for i := range articles {
		articles[i] = model.FeedItem{FeedSourceID: source.ID, GUID: fmt.Sprintf("shard-insert-%d", i), Title: fmt.Sprintf("Unrelated %d", i), Link: fmt.Sprintf("https://one.example/%d", i), PublishedAt: time.Now().UTC()}
	}
	if err := db.CreateInBatches(&articles, 200).Error; err != nil {
		t.Fatal(err)
	}
	var ledger feedDedupSourceRevision
	if err := db.Where("feed_source_id = ?", source.ID).Take(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.Revision != 1 {
		t.Fatalf("200 articles in one INSERT should advance revision once, got %d", ledger.Revision)
	}
	after, err := repo.dedupSourceRevision(context.Background())
	if err != nil || after == initial {
		t.Fatalf("insert not tracked: before=%s after=%s err=%v", initial, after, err)
	}
	// A non-deduplication column update (on every article) should not advance it.
	if err := db.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Update("full_text_html", "<p>updated</p>").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("feed_source_id = ?", source.ID).Take(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.Revision != 1 {
		t.Fatalf("content enrichment unexpectedly advanced source revision to %d", ledger.Revision)
	}
	if err := db.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Update("author", "new author").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("feed_source_id = ?", source.ID).Take(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.Revision != 2 {
		t.Fatalf("one UPDATE statement across 200 rows should advance once, got %d", ledger.Revision)
	}
	if err := db.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Update("author", "new author").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("feed_source_id = ?", source.ID).Take(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.Revision != 2 {
		t.Fatalf("no-op UPDATE should not increment revision, got %d", ledger.Revision)
	}
}

func TestDedupRevisionsAllowConcurrentDifferentSources(t *testing.T) {
	_, db, sourceA := prepareDedupSnapshotTest(t)
	sourceB := model.FeedSource{SourceType: "external_rss", Title: "Other stream", Hash: "dedup-shard-concurrent-source", RssURL: "https://other.example/rss"}
	if err := db.Create(&sourceB).Error; err != nil {
		t.Fatal(err)
	}
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	tx1 := db.Begin()
	if tx1.Error != nil {
		t.Fatal(tx1.Error)
	}
	defer tx1.Rollback()
	first := model.FeedItem{FeedSourceID: sourceA.ID, GUID: "shard-concurrent-a", Title: "First article", Link: "https://first.example/1", PublishedAt: time.Now().UTC()}
	if err := tx1.Create(&first).Error; err != nil {
		t.Fatal(err)
	}

	// Keeping source A's transaction uncommitted must not prevent source B
	// from committing: a global revision row would block this transaction.
	completed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
		defer cancel()
		second := model.FeedItem{FeedSourceID: sourceB.ID, GUID: "shard-concurrent-b", Title: "Second article", Link: "https://second.example/2", PublishedAt: time.Now().UTC()}
		completed <- db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return tx.Create(&second).Error })
	}()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("different-source writer blocked or failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source B blocked behind uncommitted source A transaction")
	}
	if err := tx1.Commit().Error; err != nil {
		t.Fatal(err)
	}
	var ledgers []feedDedupSourceRevision
	if err := db.Where("feed_source_id IN ?", []string{sourceA.ID.String(), sourceB.ID.String()}).Find(&ledgers).Error; err != nil {
		t.Fatal(err)
	}
	if len(ledgers) != 2 {
		t.Fatalf("expected two independent source revisions, got %d", len(ledgers))
	}
}

func TestDedupRevisionTracksChangedSourceOnMove(t *testing.T) {
	_, db, sourceA := prepareDedupSnapshotTest(t)
	sourceB := model.FeedSource{SourceType: "external_rss", Title: "Destination", Hash: "dedup-shard-move-source", RssURL: "https://destination.example/rss"}
	if err := db.Create(&sourceB).Error; err != nil {
		t.Fatal(err)
	}
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: sourceA.ID, GUID: "move-item", Title: "Article moving sources", Link: "https://move.example/a", PublishedAt: time.Now().UTC()}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&item).Update("feed_source_id", sourceB.ID).Error; err != nil {
		t.Fatal(err)
	}
	var left, right feedDedupSourceRevision
	if err := db.Where("feed_source_id = ?", sourceA.ID).First(&left).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("feed_source_id = ?", sourceB.ID).First(&right).Error; err != nil {
		t.Fatal(err)
	}
	if left.Revision != 2 || right.Revision != 1 {
		t.Fatalf("moved item must invalidate both source revisions; left=%d right=%d", left.Revision, right.Revision)
	}
}

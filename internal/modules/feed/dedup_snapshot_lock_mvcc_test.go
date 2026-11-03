package feed

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestDedupRevisionSameSourceWriteSerializesButDifferentSourceContinues(t *testing.T) {
	_, db, sourceA := prepareDedupSnapshotTest(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(6)
	sourceB := model.FeedSource{SourceType: "external_rss", Hash: "same-source-blocking-separate", Title: "Second independent source", RssURL: "https://synthetic.invalid/other/rss"}
	if err := db.Create(&sourceB).Error; err != nil {
		t.Fatal(err)
	}
	if err := installDedupRevisionTracking(db); err != nil {
		t.Fatal(err)
	}
	held := db.Begin()
	if held.Error != nil {
		t.Fatal(held.Error)
	}
	defer held.Rollback()
	first := model.FeedItem{FeedSourceID: sourceA.ID, GUID: "lock-held", Title: "Uncommitted source A", Link: "https://synthetic.invalid/lock/A", PublishedAt: time.Now().UTC()}
	if err := held.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	sameDone := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		close(started)
		next := model.FeedItem{FeedSourceID: sourceA.ID, GUID: "lock-waiter", Title: "Competing source A", Link: "https://synthetic.invalid/lock/A2", PublishedAt: time.Now().UTC()}
		sameDone <- db.WithContext(ctx).Create(&next).Error
	}()
	<-started
	select {
	case err := <-sameDone:
		t.Fatalf("same source bypassed revision lock before prior commit: %v", err)
	case <-time.After(160 * time.Millisecond):
	}
	differentDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		other := model.FeedItem{FeedSourceID: sourceB.ID, GUID: "lock-other", Title: "Independent source B", Link: "https://synthetic.invalid/lock/B", PublishedAt: time.Now().UTC()}
		differentDone <- db.WithContext(ctx).Create(&other).Error
	}()
	select {
	case err := <-differentDone:
		if err != nil {
			t.Fatalf("different source failed while source A locked: %v", err)
		}
	case <-time.After(1200 * time.Millisecond):
		t.Fatal("different source blocked behind source A transaction")
	}
	if err := held.Commit().Error; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sameDone:
		if err != nil {
			t.Fatalf("same-source waiter failed after release: %v", err)
		}
	case <-time.After(1200 * time.Millisecond):
		t.Fatal("same-source write did not continue after commit")
	}
	var ledgers []feedDedupSourceRevision
	if err := db.Where("feed_source_id IN ?", []string{sourceA.ID.String(), sourceB.ID.String()}).Find(&ledgers).Error; err != nil {
		t.Fatal(err)
	}
	if len(ledgers) != 2 {
		t.Fatalf("expected independent revision ledgers, got %d", len(ledgers))
	}
	var revisionA, revisionB int64
	for _, ledger := range ledgers {
		switch ledger.FeedSourceID {
		case sourceA.ID:
			revisionA = ledger.Revision
		case sourceB.ID:
			revisionB = ledger.Revision
		}
	}
	if revisionA != 2 || revisionB != 1 {
		t.Fatalf("revision serialization mismatch A=%d B=%d", revisionA, revisionB)
	}
}

func TestDedupSnapshotReaderMVCCSurvivesOldGenerationCleanup(t *testing.T) {
	repo, db, source := prepareDedupSnapshotTest(t)
	article := model.FeedItem{FeedSourceID: source.ID, GUID: "mvcc-cleanup", Title: "A stable publication across swap", Link: "https://synthetic.invalid/mvcc", PublishedAt: time.Now().UTC()}
	if err := db.Create(&article).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := repo.publishPublicRecentDedupSnapshot(ctx, "unchanged-revision"); err != nil {
		t.Fatal(err)
	}
	var oldHead feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&oldHead).Error; err != nil {
		t.Fatal(err)
	}
	readTx := db.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if readTx.Error != nil {
		t.Fatal(readTx.Error)
	}
	defer readTx.Rollback()
	// Establish the old MVCC snapshot before publishing a replacement.
	var observed feedDedupSnapshotHead
	if err := readTx.Where("scope = ?", publicRecentDedupScope).Take(&observed).Error; err != nil {
		t.Fatal(err)
	}
	if observed.GenerationID != oldHead.GenerationID {
		t.Fatal("read transaction did not establish the old generation")
	}
	if _, err := repo.publishPublicRecentDedupSnapshot(ctx, "unchanged-revision"); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&feedDedupSnapshotGeneration{}).Where("generation_id = ?", oldHead.GenerationID).Update("built_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	removed, err := repo.pruneInactiveDedupSnapshotGenerations(ctx, time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("expected retired generation cleanup: removed=%d err=%v", removed, err)
	}
	oldReader := &Repo{db: readTx}
	rows, total, err := oldReader.readPublicRecentDedupSnapshotPage(ctx, "unchanged-revision", 1, 10)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != article.ID {
		t.Fatalf("in-flight reader lost its MVCC generation: total=%d rows=%v err=%v", total, rows, err)
	}
	if err := readTx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	var current feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&current).Error; err != nil {
		t.Fatal(err)
	}
	if current.GenerationID == oldHead.GenerationID {
		t.Fatal("cleanup failed to preserve newer active generation")
	}
}

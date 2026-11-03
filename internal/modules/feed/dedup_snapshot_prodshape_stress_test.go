package feed

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func scaleSameSourceContention(t *testing.T, parent context.Context, db *gorm.DB, sourceID string) {
	t.Helper()
	const writers, statements = 8, 4
	results := make(chan struct {
		duration time.Duration
		err      error
	}, writers*statements)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			for batch := 0; batch < statements; batch++ {
				stories := make([]model.FeedItem, 4)
				for i := range stories {
					key := fmt.Sprintf("same-source-%d-%d-%d", w, batch, i)
					stories[i] = model.FeedItem{FeedSourceID: uuid.MustParse(sourceID), GUID: key,
						Title: key + uuid.NewString(), Link: "https://synthetic.invalid/same-source/" + key,
						PublishedAt: time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC).Add(time.Duration(w*100+batch*4+i) * time.Second)}
				}
				started := time.Now()
				ctx, cancel := context.WithTimeout(parent, 12*time.Second)
				err := db.WithContext(ctx).CreateInBatches(&stories, 4).Error
				cancel()
				results <- struct {
					duration time.Duration
					err      error
				}{time.Since(started), err}
				if err != nil {
					return
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()
	close(results)
	times := make([]time.Duration, 0, writers*statements)
	for item := range results {
		if item.err != nil {
			t.Fatalf("same-source concurrent writer failed: %v", item.err)
		}
		times = append(times, item.duration)
	}
	if len(times) != writers*statements {
		t.Fatalf("lost same-source statements: got %d want %d", len(times), writers*statements)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("SCALE_SAME_SOURCE sources=1 writers=%d statements=%d items=%d p50=%s p95=%s max=%s",
		writers, len(times), len(times)*4, times[len(times)/2], times[30], times[len(times)-1])
	var eventCount int64
	if err := db.Model(&feedDedupChangeEvent{}).Where("feed_source_id = ?", sourceID).Count(&eventCount).Error; err != nil {
		t.Fatal(err)
	}
	if eventCount != 1+writers*statements {
		t.Fatalf("same-source journal must contain latest prefix plus every write: got=%d", eventCount)
	}
}

func scaleHistoricalGenerationPrune(t *testing.T, ctx context.Context, repo *Repo, db *gorm.DB, oldGeneration uuid.UUID) {
	t.Helper()
	t0 := time.Now()
	newTotal, err := repo.publishPublicRecentDedupSnapshotTracked(ctx)
	if err != nil || newTotal < 1 {
		t.Fatalf("rebuild after same-source bursts: total=%d err=%v", newTotal, err)
	}
	t.Logf("SCALE_SECOND_REBUILD elapsed=%s total=%d", time.Since(t0), newTotal)
	var current feedDedupSnapshotHead
	if err := db.Where("scope = ?", publicRecentDedupScope).Take(&current).Error; err != nil {
		t.Fatal(err)
	}
	if current.GenerationID == oldGeneration {
		t.Fatal("rebuild did not publish new generation")
	}
	var oldMembers int64
	if err := db.Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", oldGeneration).Count(&oldMembers).Error; err != nil {
		t.Fatal(err)
	}
	if oldMembers < 1000 {
		t.Fatalf("old generation unexpectedly missing before prune: %d", oldMembers)
	}
	if err := db.Model(&feedDedupSnapshotGeneration{}).Where("generation_id = ?", oldGeneration).
		Update("built_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	beforeBytes := scaleDatabaseBytes(t, db)
	t0 = time.Now()
	removed, err := repo.pruneInactiveDedupSnapshotGenerations(ctx, time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("expected one old snapshot pruned: removed=%d err=%v", removed, err)
	}
	var afterMembers, afterRows, currentMembers int64
	if err := db.Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", oldGeneration).Count(&afterMembers).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&feedDedupSnapshotRow{}).Where("generation_id = ?", oldGeneration).Count(&afterRows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&feedDedupSnapshotMember{}).Where("generation_id = ?", current.GenerationID).Count(&currentMembers).Error; err != nil {
		t.Fatal(err)
	}
	if afterMembers != 0 || afterRows != 0 || currentMembers < oldMembers {
		t.Fatalf("generation cleanup lost active metadata: old members=%d rows=%d current=%d", afterMembers, afterRows, currentMembers)
	}
	t.Logf("SCALE_GC removed_generations=%d old_members=%d elapsed=%s db_bytes_before=%d db_bytes_after=%d active_members=%d",
		removed, oldMembers, time.Since(t0), beforeBytes, scaleDatabaseBytes(t, db), currentMembers)
	// PostgreSQL deleting rows does not immediately truncate heap files; the
	// database bytes after deletion are not a reliable disk-space reclaimed KPI.
}

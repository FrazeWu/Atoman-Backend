package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
)

func TestDuplicateAnnotationContextParityAndEarlyCancellation(t *testing.T) {
	base := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	original := make([]model.FeedItem, 220)
	for i := range original {
		link := fmt.Sprintf("https://test.invalid/story/%d", i)
		if i > 0 && i%11 == 0 {
			link = fmt.Sprintf("https://test.invalid/story/%d", i-1)
		}
		original[i] = model.FeedItem{
			Base: model.Base{ID: uuid.New()}, Title: fmt.Sprintf("Independent report about a new discovery %d", i),
			Link: link, PublishedAt: base.Add(-time.Duration(i) * time.Minute),
		}
	}
	old := append([]model.FeedItem(nil), original...)
	scoped := append([]model.FeedItem(nil), original...)
	AnnotateDuplicateFeedItems(old)
	if err := AnnotateDuplicateFeedItemsContext(context.Background(), scoped); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(old, scoped) {
		t.Fatal("context-scoped dedup changed representative ordering or duplicate metadata")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := append([]model.FeedItem(nil), original...)
	if err := AnnotateDuplicateFeedItemsContext(ctx, canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if !reflect.DeepEqual(canceled, original) {
		t.Fatal("pre-canceled annotation modified input")
	}
}

type annotationCancelAfterChecks struct {
	context.Context
	checks int
}

func (c *annotationCancelAfterChecks) Err() error {
	c.checks++
	if c.checks >= 5 {
		return context.Canceled
	}
	return nil
}
func TestDuplicateAnnotationChecksCancellationDuringScan(t *testing.T) {
	items := make([]model.FeedItem, 500)
	for i := range items {
		items[i] = model.FeedItem{Base: model.Base{ID: uuid.New()},
			Title:       fmt.Sprintf("Unduplicated report %d", i),
			Link:        fmt.Sprintf("https://example.invalid/%d", i),
			PublishedAt: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
	}
	ctx := &annotationCancelAfterChecks{Context: context.Background()}
	if err := AnnotateDuplicateFeedItemsContext(ctx, items); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during scan not propagated: %v", err)
	}
	if items[len(items)-1].DuplicateCount != 0 {
		t.Fatal("annotation kept modifying items after cancellation")
	}
}

package service

import (
	"atoman/internal/model"
	"fmt"
	"github.com/google/uuid"
	"testing"
	"time"
)

func BenchmarkDuplicateEndurance5000(b *testing.B) {
	base := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	items := make([]model.FeedItem, 5000)
	for i := range items {
		items[i] = model.FeedItem{Base: model.Base{ID: uuid.New()}, Title: fmt.Sprintf("Independent security report %s %d", uuid.NewString(), i), Link: fmt.Sprintf("https://synthetic.invalid/endurance/%05d", i), PublishedAt: base.Add(-time.Duration(i) * time.Second)}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := append([]model.FeedItem(nil), items...)
		AnnotateDuplicateFeedItems(batch)
	}
}

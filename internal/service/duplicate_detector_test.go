package service

import (
	"encoding/json"
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func duplicateTestItem(id uuid.UUID, title, link, sourceTitle string, publishedAt time.Time) model.FeedItem {
	return model.FeedItem{
		Base:        model.Base{ID: id},
		Title:       title,
		Link:        link,
		PublishedAt: publishedAt,
		FeedSource:  &model.FeedSource{Title: sourceTitle},
	}
}

func TestAnnotateDuplicateFeedItemsIncludesClusterMemberIDs(t *testing.T) {
	now := time.Now().UTC()
	firstID := uuid.New()
	secondID := uuid.New()
	items := []model.FeedItem{
		duplicateTestItem(firstID, "Same story", "https://example.com/story?utm_source=one", "Source A", now),
		duplicateTestItem(secondID, "Same story", "https://example.com/story", "Source B", now.Add(-time.Hour)),
	}

	AnnotateDuplicateFeedItems(items)

	payload, err := json.Marshal(items[0])
	if err != nil {
		t.Fatalf("marshal primary item: %v", err)
	}
	var decoded struct {
		DuplicateItemIDs []uuid.UUID `json:"duplicate_item_ids"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode primary item: %v", err)
	}
	if len(decoded.DuplicateItemIDs) != 2 || decoded.DuplicateItemIDs[0] != firstID || decoded.DuplicateItemIDs[1] != secondID {
		t.Fatalf("expected primary and duplicate IDs, got %#v", decoded.DuplicateItemIDs)
	}
}

func TestAnnotateDuplicateFeedItemsKeepsExistingMatchingRules(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name      string
		first     model.FeedItem
		second    model.FeedItem
		duplicate bool
	}{
		{
			name:      "same normalized URL",
			first:     duplicateTestItem(uuid.New(), "First title", "https://example.com/story?utm_source=a", "A", now),
			second:    duplicateTestItem(uuid.New(), "Different title", "https://example.com/story", "B", now.Add(-time.Hour)),
			duplicate: true,
		},
		{
			name:      "similar title within window",
			first:     duplicateTestItem(uuid.New(), "Atoman releases the new subscription reader today", "https://one.example/story", "A", now),
			second:    duplicateTestItem(uuid.New(), "Atoman releases the new subscription reader today!", "https://two.example/story", "B", now.Add(-time.Hour)),
			duplicate: true,
		},
		{
			name:      "similar title outside window",
			first:     duplicateTestItem(uuid.New(), "Atoman releases the new subscription reader today", "https://one.example/story", "A", now),
			second:    duplicateTestItem(uuid.New(), "Atoman releases the new subscription reader today!", "https://two.example/story", "B", now.Add(-73*time.Hour)),
			duplicate: false,
		},
		{
			name:      "different title",
			first:     duplicateTestItem(uuid.New(), "Atoman subscription update", "https://one.example/story", "A", now),
			second:    duplicateTestItem(uuid.New(), "Weather forecast for Berlin", "https://two.example/story", "B", now.Add(-time.Hour)),
			duplicate: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := []model.FeedItem{tt.first, tt.second}
			AnnotateDuplicateFeedItems(items)
			if items[1].IsDuplicate != tt.duplicate {
				t.Fatalf("expected duplicate=%v, got %#v", tt.duplicate, items)
			}
		})
	}
}

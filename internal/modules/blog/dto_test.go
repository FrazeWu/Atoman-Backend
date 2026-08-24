package blog

import (
	"encoding/json"
	"testing"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestNewPostDTOProducesFlatCanonicalResponse(t *testing.T) {
	postID := uuid.New()
	channelID := uuid.New()
	collectionID := uuid.New()
	publishedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	rating := 8
	post := model.Post{
		Base:               model.Base{ID: postID, CreatedAt: publishedAt, UpdatedAt: publishedAt},
		UserID:             uuid.New(),
		ChannelID:          &channelID,
		CollectionID:       &collectionID,
		CollectionPosition: 3,
		CollectionConflict: true,
		Title:              "Canonical title",
		Content:            "Canonical content",
		Summary:            "Canonical summary",
		LanguageCode:       "zh",
		CoverURL:           "https://example.com/cover.jpg",
		Status:             "published",
		Visibility:         "public",
		Pinned:             true,
		PublishedAt:        &publishedAt,
		ViewCount:          42,
		BookmarksCount:     7,
		RatingScore:        8.5,
		RatingCount:        2,
		ViewerRating:       &rating,
	}

	payload, err := json.Marshal(newPostDTO(post))
	if err != nil {
		t.Fatalf("marshal post DTO: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("unmarshal post DTO: %v", err)
	}

	if _, ok := response["Post"]; ok {
		t.Fatalf("legacy embedded post must not be exposed: %s", payload)
	}
	for _, field := range []string{
		"id", "user_id", "channel_id", "collection_id", "collection_position",
		"collection_conflict", "title", "content", "summary", "status", "visibility",
		"published_at", "view_count", "bookmarks_count", "rating_score", "rating_count",
		"viewer_rating", "references",
	} {
		if _, ok := response[field]; !ok {
			t.Errorf("expected %q in flat post DTO: %s", field, payload)
		}
	}
	if references, ok := response["references"].([]any); !ok || len(references) != 0 {
		t.Fatalf("expected an empty references array, got %#v", response["references"])
	}
}

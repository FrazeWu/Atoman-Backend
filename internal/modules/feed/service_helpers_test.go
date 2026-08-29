package feed

import (
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
)

func TestSubscriptionPriorityIndexUsesHighestMatchingInternalSource(t *testing.T) {
	userID := uuid.New()
	channelID := uuid.New()
	collectionID := uuid.New()
	userSourceID := uuid.New()
	channelSourceID := uuid.New()
	collectionSourceID := uuid.New()

	priorities := newSubscriptionPriorityIndex([]model.Subscription{
		{
			FeedSourceID: userSourceID,
			Priority:     "high",
			FeedSource:   &model.FeedSource{SourceType: "internal_user", SourceID: &userID},
		},
		{
			FeedSourceID: channelSourceID,
			Priority:     "normal",
			FeedSource:   &model.FeedSource{SourceType: "internal_channel", SourceID: &channelID},
		},
		{
			FeedSourceID: collectionSourceID,
			Priority:     "low",
			FeedSource:   &model.FeedSource{SourceType: "internal_collection", SourceID: &collectionID},
		},
	})
	items := []TimelineItemDTO{{
		Type: "post",
		Post: &TimelinePostDTO{Post: model.Post{
			UserID:       userID,
			ChannelID:    &channelID,
			CollectionID: &collectionID,
		}},
	}}

	applySubscriptionPriority(items, priorities)

	if items[0].PriorityReason != "subscription_priority_high" {
		t.Fatalf("expected highest matching priority reason, got %q", items[0].PriorityReason)
	}
}

package feed

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestSubscriptionHubUsesOneChannelSubscriptionAcrossContentTypes(t *testing.T) {
	service, db, viewer, creator, channel := newUnifiedSubscriptionFixture(t)
	_, episode, video := seedUnifiedChannelUpdates(t, db, creator, channel)
	testdb.Migrate(t, db, &model.SubscriptionGroup{})

	source := model.FeedSource{
		SourceType: "internal_channel",
		SourceID:   &channel.ID,
		Hash:       "subscription-hub-single-channel-subscription",
		Title:      channel.Name,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create channel source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: viewer.ID, FeedSourceID: source.ID, Title: source.Title}).Error; err != nil {
		t.Fatalf("create channel subscription: %v", err)
	}

	tree, err := service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get subscription hub tree: %v", err)
	}
	podcastGroup := firstSubscriptionHubGroup(tree, SubscriptionHubTypePodcast)
	videoGroup := firstSubscriptionHubGroup(tree, SubscriptionHubTypeVideo)
	if podcastGroup == nil || len(podcastGroup.Memberships) != 1 {
		t.Fatalf("podcast view is missing the channel subscription: %#v", podcastGroup)
	}
	if videoGroup == nil || len(videoGroup.Memberships) != 1 {
		t.Fatalf("video view is missing the channel subscription: %#v", videoGroup)
	}
	if podcastGroup.Memberships[0].FeedSourceID != videoGroup.Memberships[0].FeedSourceID {
		t.Fatalf("the same subscription must be reused across content views")
	}

	podcastItems, _, err := service.GetSubscriptionHubUpdates(viewer, SubscriptionHubUpdatesQuery{
		SubscriptionType: SubscriptionHubTypePodcast,
		GroupID:          podcastGroup.ID,
		Page:             1,
		PageSize:         20,
	})
	if err != nil {
		t.Fatalf("get imported podcast updates: %v", err)
	}
	if len(podcastItems) != 1 || podcastItems[0].PodcastEpisode == nil || podcastItems[0].PodcastEpisode.ID != episode.ID {
		t.Fatalf("unexpected imported podcast updates: %#v", podcastItems)
	}

	videoItems, _, err := service.GetSubscriptionHubUpdates(viewer, SubscriptionHubUpdatesQuery{
		SubscriptionType: SubscriptionHubTypeVideo,
		GroupID:          videoGroup.ID,
		Page:             1,
		PageSize:         20,
	})
	if err != nil {
		t.Fatalf("get imported video updates: %v", err)
	}
	if len(videoItems) != 1 || videoItems[0].Video == nil || videoItems[0].Video.ID != video.ID {
		t.Fatalf("unexpected imported video updates: %#v", videoItems)
	}
}

func firstSubscriptionHubGroup(tree SubscriptionHubTree, subscriptionType string) *SubscriptionHubGroupNode {
	for _, typeNode := range tree.Types {
		if typeNode.SubscriptionType == subscriptionType && len(typeNode.Groups) > 0 {
			return &typeNode.Groups[0]
		}
	}
	return nil
}

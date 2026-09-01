package feed

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestSubscriptionHubKeepsChannelContextsSeparatedByType(t *testing.T) {
	service, db, viewer, creator, channel := newUnifiedSubscriptionFixture(t)
	_, episode, video := seedUnifiedChannelUpdates(t, db, creator, channel)
	testdb.Migrate(t, db, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{})

	source := model.FeedSource{
		SourceType: "internal_channel",
		SourceID:   &channel.ID,
		Hash:       "subscription-hub-shared-channel",
		Title:      channel.Name,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create shared channel source: %v", err)
	}

	podcastGroup := model.SubscriptionHubGroup{
		UserID:           viewer.ID,
		SubscriptionType: SubscriptionHubTypePodcast,
		Name:             "Podcast group",
	}
	videoGroup := model.SubscriptionHubGroup{
		UserID:           viewer.ID,
		SubscriptionType: SubscriptionHubTypeVideo,
		Name:             "Video group",
	}
	if err := db.Create(&podcastGroup).Error; err != nil {
		t.Fatalf("create podcast group: %v", err)
	}
	if err := db.Create(&videoGroup).Error; err != nil {
		t.Fatalf("create video group: %v", err)
	}

	memberships := []model.SubscriptionHubMembership{
		{
			UserID:           viewer.ID,
			SubscriptionType: SubscriptionHubTypePodcast,
			GroupID:          podcastGroup.ID,
			FeedSourceID:     source.ID,
			Title:            source.Title,
		},
		{
			UserID:           viewer.ID,
			SubscriptionType: SubscriptionHubTypeVideo,
			GroupID:          videoGroup.ID,
			FeedSourceID:     source.ID,
			Title:            source.Title,
		},
	}
	if err := db.Create(&memberships).Error; err != nil {
		t.Fatalf("create type-scoped memberships: %v", err)
	}

	tree, err := service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get subscription hub tree: %v", err)
	}
	if group := tree.Group(SubscriptionHubTypePodcast, podcastGroup.ID); group == nil || len(group.Memberships) != 1 || group.Memberships[0].FeedSourceID != source.ID {
		t.Fatalf("podcast branch must contain only its own channel membership: %#v", group)
	}
	if group := tree.Group(SubscriptionHubTypeVideo, videoGroup.ID); group == nil || len(group.Memberships) != 1 || group.Memberships[0].FeedSourceID != source.ID {
		t.Fatalf("video branch must contain an independent channel membership: %#v", group)
	}

	podcastItems, _, err := service.GetSubscriptionHubUpdates(viewer, SubscriptionHubUpdatesQuery{
		SubscriptionType: SubscriptionHubTypePodcast,
		GroupID:          podcastGroup.ID,
		Page:             1,
		PageSize:         20,
	})
	if err != nil {
		t.Fatalf("get podcast updates: %v", err)
	}
	if len(podcastItems) != 1 || podcastItems[0].PodcastEpisode == nil || podcastItems[0].PodcastEpisode.ID != episode.ID || podcastItems[0].Video != nil {
		t.Fatalf("podcast branch leaked a different content type: %#v", podcastItems)
	}

	videoItems, _, err := service.GetSubscriptionHubUpdates(viewer, SubscriptionHubUpdatesQuery{
		SubscriptionType: SubscriptionHubTypeVideo,
		GroupID:          videoGroup.ID,
		Page:             1,
		PageSize:         20,
	})
	if err != nil {
		t.Fatalf("get video updates: %v", err)
	}
	if len(videoItems) != 1 || videoItems[0].Video == nil || videoItems[0].Video.ID != video.ID || videoItems[0].PodcastEpisode != nil {
		t.Fatalf("video branch leaked a different content type: %#v", videoItems)
	}
}

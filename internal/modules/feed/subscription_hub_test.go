package feed

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestSubscriptionHubReconcilesSubscriptionChanges(t *testing.T) {
	service, db, viewer, _, _ := newUnifiedSubscriptionFixture(t)
	testdb.Migrate(t, db, &model.SubscriptionGroup{}, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{})

	firstGroup := model.SubscriptionGroup{UserID: viewer.ID, Name: "第一组", Position: 0}
	secondGroup := model.SubscriptionGroup{UserID: viewer.ID, Name: "第二组", Position: 1}
	if err := db.Create(&firstGroup).Error; err != nil {
		t.Fatalf("create first subscription group: %v", err)
	}
	if err := db.Create(&secondGroup).Error; err != nil {
		t.Fatalf("create second subscription group: %v", err)
	}
	source := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "https://example.com/reconcile.xml",
		Hash:       "subscription-hub-reconcile-rss",
		Title:      "Reconcile RSS",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create RSS source: %v", err)
	}
	subscription := model.Subscription{
		UserID:              viewer.ID,
		FeedSourceID:        source.ID,
		SubscriptionGroupID: &firstGroup.ID,
		Title:               "旧名称",
	}
	if err := db.Create(&subscription).Error; err != nil {
		t.Fatalf("create subscription: %v", err)
	}

	tree, err := service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get initial tree: %v", err)
	}
	initial := firstSubscriptionHubGroup(tree, SubscriptionHubTypeRSS)
	if initial == nil || initial.Name != firstGroup.Name || len(initial.Memberships) != 1 {
		t.Fatalf("unexpected initial RSS branch: %#v", initial)
	}

	if err := db.Model(&subscription).Updates(map[string]any{
		"title":                 "新名称",
		"subscription_group_id": secondGroup.ID,
	}).Error; err != nil {
		t.Fatalf("update subscription: %v", err)
	}
	tree, err = service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get updated tree: %v", err)
	}
	updated := firstSubscriptionHubGroup(tree, SubscriptionHubTypeRSS)
	if updated == nil || updated.Name != secondGroup.Name || len(updated.Memberships) != 1 || updated.Memberships[0].Title != "新名称" {
		t.Fatalf("subscription changes were not reconciled: %#v", updated)
	}

	if err := db.Delete(&subscription).Error; err != nil {
		t.Fatalf("delete subscription: %v", err)
	}
	tree, err = service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get tree after delete: %v", err)
	}
	if group := firstSubscriptionHubGroup(tree, SubscriptionHubTypeRSS); group != nil {
		t.Fatalf("removed subscription remained in tree: %#v", group)
	}
}

func TestSubscriptionHubUsesCurrentSourceIdentityImages(t *testing.T) {
	service, db, viewer, creator, channel := newUnifiedSubscriptionFixture(t)
	testdb.Migrate(t, db, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{})

	creator.AvatarURL = "/media/users/creator.webp"
	if err := db.Model(&creator).Update("avatar_url", creator.AvatarURL).Error; err != nil {
		t.Fatalf("set creator avatar: %v", err)
	}
	channel.CoverURL = "/media/channels/unified.webp"
	if err := db.Model(&channel).Update("cover_url", channel.CoverURL).Error; err != nil {
		t.Fatalf("set channel cover: %v", err)
	}

	userSource := model.FeedSource{
		SourceType: "internal_user",
		SourceID:   &creator.UUID,
		Hash:       "subscription-hub-user-identity",
		Title:      creator.Username,
	}
	channelSource := model.FeedSource{
		SourceType: "internal_channel",
		SourceID:   &channel.ID,
		Hash:       "subscription-hub-channel-identity",
		Title:      channel.Name,
	}
	if err := db.Create(&userSource).Error; err != nil {
		t.Fatalf("create user identity source: %v", err)
	}
	if err := db.Create(&channelSource).Error; err != nil {
		t.Fatalf("create channel identity source: %v", err)
	}
	userSubscription := model.Subscription{UserID: viewer.ID, FeedSourceID: userSource.ID, Title: creator.Username}
	channelSubscription := model.Subscription{UserID: viewer.ID, FeedSourceID: channelSource.ID, Title: channel.Name}
	if err := db.Create(&userSubscription).Error; err != nil {
		t.Fatalf("create user identity subscription: %v", err)
	}
	if err := db.Create(&channelSubscription).Error; err != nil {
		t.Fatalf("create channel identity subscription: %v", err)
	}

	tree, err := service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get subscription tree: %v", err)
	}
	for _, typeNode := range tree.Types {
		for _, group := range typeNode.Groups {
			for _, membership := range group.Memberships {
				if membership.FeedSource == nil {
					continue
				}
				switch membership.FeedSourceID {
				case userSource.ID:
					if membership.FeedSource.CoverURL != creator.AvatarURL {
						t.Fatalf("user source image=%q, want %q", membership.FeedSource.CoverURL, creator.AvatarURL)
					}
				case channelSource.ID:
					if membership.FeedSource.CoverURL != channel.CoverURL {
						t.Fatalf("channel source image=%q, want %q", membership.FeedSource.CoverURL, channel.CoverURL)
					}
				}
			}
		}
	}
}

func TestSubscriptionHubKeepsChannelContextsSeparatedByType(t *testing.T) {
	service, db, viewer, creator, channel := newUnifiedSubscriptionFixture(t)
	_, episode, video := seedUnifiedChannelUpdates(t, db, creator, channel)
	testdb.Migrate(t, db, &model.ChannelBookmark{}, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{})
	if err := db.Create(&[]model.ChannelBookmark{
		{UserID: viewer.ID, ChannelID: channel.ID, Kind: "podcast_show"},
		{UserID: viewer.ID, ChannelID: channel.ID, Kind: "video_channel"},
	}).Error; err != nil {
		t.Fatalf("create type-scoped bookmarks: %v", err)
	}

	tree, err := service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get subscription hub tree: %v", err)
	}
	podcastGroup := firstSubscriptionHubGroup(tree, SubscriptionHubTypePodcast)
	videoGroup := firstSubscriptionHubGroup(tree, SubscriptionHubTypeVideo)
	if podcastGroup == nil || len(podcastGroup.Memberships) != 1 {
		t.Fatalf("podcast branch must contain only its own channel membership: %#v", podcastGroup)
	}
	if videoGroup == nil || len(videoGroup.Memberships) != 1 || videoGroup.Memberships[0].FeedSourceID != podcastGroup.Memberships[0].FeedSourceID {
		t.Fatalf("video branch must contain an independent channel membership: %#v", videoGroup)
	}
	for _, test := range []struct {
		subscriptionType string
		hasContent       bool
	}{
		{subscriptionType: SubscriptionHubTypePodcast, hasContent: true},
		{subscriptionType: SubscriptionHubTypeVideo, hasContent: true},
		{subscriptionType: SubscriptionHubTypeBlog, hasContent: false},
		{subscriptionType: SubscriptionHubTypeRSS, hasContent: false},
	} {
		var node *SubscriptionHubTypeNode
		for index := range tree.Types {
			if tree.Types[index].SubscriptionType == test.subscriptionType {
				node = &tree.Types[index]
				break
			}
		}
		if node == nil || node.HasContent != test.hasContent {
			t.Fatalf("unexpected content availability for %s: %#v", test.subscriptionType, node)
		}
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

func TestSubscriptionHubMarksSubscribedTypeWithoutContentEmpty(t *testing.T) {
	service, db, viewer, _, channel := newUnifiedSubscriptionFixture(t)
	testdb.Migrate(t, db, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{})

	source := model.FeedSource{
		SourceType: "internal_channel",
		SourceID:   &channel.ID,
		Hash:       "subscription-hub-empty-channel",
		Title:      channel.Name,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create empty channel source: %v", err)
	}
	group := model.SubscriptionHubGroup{
		UserID:           viewer.ID,
		SubscriptionType: SubscriptionHubTypeVideo,
		Name:             "Empty video group",
	}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create empty video group: %v", err)
	}
	if err := db.Create(&model.SubscriptionHubMembership{
		UserID:           viewer.ID,
		SubscriptionType: SubscriptionHubTypeVideo,
		GroupID:          group.ID,
		FeedSourceID:     source.ID,
		Title:            source.Title,
	}).Error; err != nil {
		t.Fatalf("create empty video membership: %v", err)
	}

	tree, err := service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get subscription hub tree: %v", err)
	}
	videoNode := tree.Types[1]
	if videoNode.SubscriptionType != SubscriptionHubTypeVideo || videoNode.HasContent {
		t.Fatalf("video type with no updates must be marked empty: %#v", videoNode)
	}
}

func TestSubscriptionHubImportsSelfSubscriptionIntoAllContentTypes(t *testing.T) {
	service, db, viewer, _, _ := newUnifiedSubscriptionFixture(t)
	testdb.Migrate(t, db, &model.SubscriptionHubGroup{}, &model.SubscriptionHubMembership{})

	source := model.FeedSource{
		SourceType: "internal_user",
		SourceID:   &viewer.ID,
		Hash:       "subscription-hub-self-user",
		Title:      viewer.Username,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatalf("create self source: %v", err)
	}
	if err := db.Create(&model.Subscription{
		UserID:       viewer.ID,
		FeedSourceID: source.ID,
		Title:        source.Title,
	}).Error; err != nil {
		t.Fatalf("create self subscription: %v", err)
	}

	tree, err := service.GetSubscriptionHubTree(viewer.ID)
	if err != nil {
		t.Fatalf("get subscription hub tree: %v", err)
	}
	for _, subscriptionType := range []string{SubscriptionHubTypeBlog, SubscriptionHubTypePodcast, SubscriptionHubTypeVideo} {
		group := firstSubscriptionHubGroup(tree, subscriptionType)
		if group == nil || len(group.Memberships) != 1 || group.Memberships[0].FeedSourceID != source.ID {
			t.Fatalf("self subscription should be available in %s: %#v", subscriptionType, group)
		}
	}
	if group := firstSubscriptionHubGroup(tree, SubscriptionHubTypeRSS); group != nil {
		t.Fatalf("self subscription must not be imported as RSS: %#v", group)
	}
}

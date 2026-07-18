package feed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newFeedTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.PodcastEpisode{},
		&model.Video{},
		&model.VideoCollection{},
		&model.Like{},
		&model.DiscussionTarget{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
		&model.ReadingListItem{},
		&model.SourceReadEvent{},
	)

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: authctx.RoleUser, DisplayName: "Alice", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	channel := model.Channel{Name: "Alice Channel", Slug: "alice-channel", UserID: &user.UUID}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	postCreatedAt := time.Now().Add(-2 * time.Hour).UTC()
	post := model.Post{
		UserID:    user.UUID,
		ChannelID: &channel.ID,
		Title:     "Post item",
		Content:   "post content",
		Status:    "published",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := db.Model(&post).Update("created_at", postCreatedAt).Error; err != nil {
		t.Fatalf("set post created_at: %v", err)
	}

	channelOnlyPost := model.Post{
		UserID:    user.UUID,
		ChannelID: &channel.ID,
		Title:     "Channel post",
		Content:   "channel content",
		Status:    "published",
	}
	if err := db.Create(&channelOnlyPost).Error; err != nil {
		t.Fatalf("create channel post: %v", err)
	}
	collection := model.Collection{ChannelID: channel.ID, Name: "Default Collection", Description: "desc"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatalf("create collection: %v", err)
	}
	collectionPost := model.Post{UserID: user.UUID, ChannelID: &channel.ID, Title: "Collection post", Content: "collection content", Status: "published"}
	if err := db.Create(&collectionPost).Error; err != nil {
		t.Fatalf("create collection post: %v", err)
	}
	if err := db.Create(&model.PostCollection{PostID: collectionPost.ID, CollectionID: collection.ID}).Error; err != nil {
		t.Fatalf("link post collection: %v", err)
	}

	internalSource := model.FeedSource{SourceType: "internal_user", SourceID: &user.UUID, Hash: "internal-user-hash", Title: "Alice"}
	if err := db.Create(&internalSource).Error; err != nil {
		t.Fatalf("create internal source: %v", err)
	}
	internalSubscription := model.Subscription{UserID: user.UUID, FeedSourceID: internalSource.ID, Title: "Alice posts"}
	if err := db.Create(&internalSubscription).Error; err != nil {
		t.Fatalf("create internal subscription: %v", err)
	}
	channelSource := model.FeedSource{SourceType: "internal_channel", SourceID: &channel.ID, Hash: "internal-channel-hash", Title: channel.Name}
	if err := db.Create(&channelSource).Error; err != nil {
		t.Fatalf("create channel source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.UUID, FeedSourceID: channelSource.ID, Title: channel.Name}).Error; err != nil {
		t.Fatalf("create channel subscription: %v", err)
	}
	collectionSource := model.FeedSource{SourceType: "internal_collection", SourceID: &collection.ID, Hash: "internal-collection-hash", Title: collection.Name}
	if err := db.Create(&collectionSource).Error; err != nil {
		t.Fatalf("create collection source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.UUID, FeedSourceID: collectionSource.ID, Title: collection.Name}).Error; err != nil {
		t.Fatalf("create collection subscription: %v", err)
	}

	externalSource := model.FeedSource{SourceType: "external_rss", RssURL: "https://example.com/feed.xml", Hash: "external-rss-hash", Title: "Example Feed"}
	if err := db.Create(&externalSource).Error; err != nil {
		t.Fatalf("create external source: %v", err)
	}
	publishedAt := time.Now().Add(-1 * time.Hour).UTC()
	feedItem := model.FeedItem{
		FeedSourceID: externalSource.ID,
		GUID:         "guid-1",
		Title:        "Feed item",
		Link:         "https://example.com/items/1",
		Summary:      "feed summary",
		PublishedAt:  publishedAt,
		FetchedAt:    publishedAt.Add(5 * time.Minute),
	}
	if err := db.Create(&feedItem).Error; err != nil {
		t.Fatalf("create feed item: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.UUID, FeedSourceID: externalSource.ID, Title: "Example Feed"}).Error; err != nil {
		t.Fatalf("create external subscription: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func newUnifiedSubscriptionFixture(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser, model.User, model.Channel) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PostCollection{},
		&model.PodcastEpisode{}, &model.Video{}, &model.VideoCollection{},
		&model.FeedSource{}, &model.Subscription{}, &model.FeedItem{}, &model.FeedItemRead{},
		&model.Like{}, &model.DiscussionTarget{},
	)
	viewer := model.User{Username: "unified-viewer", Email: "unified-viewer@example.com", Password: "hash", IsActive: true}
	creator := model.User{Username: "unified-creator", Email: "unified-creator@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&viewer).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&creator).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &creator.UUID, Name: "Unified Channel", Slug: "unified-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	return NewService(db), db, authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: authctx.RoleUser}, creator, channel
}

func seedUnifiedChannelUpdates(t *testing.T, db *gorm.DB, creator model.User, channel model.Channel) (model.Post, model.PodcastEpisode, model.Video) {
	t.Helper()
	blogCollection := model.Collection{ChannelID: channel.ID, ContentType: "blog", Name: "Articles"}
	podcastCollection := model.Collection{ChannelID: channel.ID, ContentType: "podcast", Name: "Episodes"}
	videoCollection := model.Collection{ChannelID: channel.ID, ContentType: "video", Name: "Videos"}
	for _, collection := range []*model.Collection{&blogCollection, &podcastCollection, &videoCollection} {
		if err := db.Create(collection).Error; err != nil {
			t.Fatal(err)
		}
	}
	blogPost := model.Post{UserID: creator.UUID, ChannelID: &channel.ID, CollectionID: &blogCollection.ID, Title: "Unified blog", Content: "body", Status: "published", Visibility: "public"}
	if err := db.Create(&blogPost).Error; err != nil {
		t.Fatal(err)
	}
	podcastPost := model.Post{UserID: creator.UUID, ChannelID: &channel.ID, Title: "Unified podcast", Content: "shownotes", Status: "published", Visibility: "public"}
	if err := db.Create(&podcastPost).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.PostCollection{PostID: podcastPost.ID, CollectionID: podcastCollection.ID}).Error; err != nil {
		t.Fatal(err)
	}
	episode := model.PodcastEpisode{PostID: podcastPost.ID, ChannelID: channel.ID, AudioURL: "https://example.com/episode.mp3"}
	if err := db.Create(&episode).Error; err != nil {
		t.Fatal(err)
	}
	video := model.Video{UserID: creator.UUID, ChannelID: &channel.ID, Title: "Unified video", StorageType: "external", VideoURL: "https://example.com/video", Status: "published", Visibility: "public"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.VideoCollection{VideoID: video.ID, CollectionID: videoCollection.ID}).Error; err != nil {
		t.Fatal(err)
	}
	return blogPost, episode, video
}

func TestChannelSubscriptionIncludesBlogPodcastAndVideoUpdates(t *testing.T) {
	service, db, viewer, creator, channel := newUnifiedSubscriptionFixture(t)
	blogPost, episode, video := seedUnifiedChannelUpdates(t, db, creator, channel)
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &channel.ID, Hash: "unified-channel-source", Title: channel.Name}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Subscription{UserID: viewer.ID, FeedSourceID: source.ID, Title: source.Title}).Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := service.GetSubscribedFeed(viewer, FeedQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("expected three module updates, total=%d items=%#v", total, items)
	}
	assertUnifiedUpdates(t, items, blogPost.ID, episode.ID, video.ID)
}

func TestFollowingUserIncludesUpdatesFromAllOwnedChannels(t *testing.T) {
	service, db, viewer, creator, firstChannel := newUnifiedSubscriptionFixture(t)
	blogPost, episode, _ := seedUnifiedChannelUpdates(t, db, creator, firstChannel)
	secondChannel := model.Channel{UserID: &creator.UUID, Name: "Second Channel", Slug: "second-channel"}
	if err := db.Create(&secondChannel).Error; err != nil {
		t.Fatal(err)
	}
	videoCollection := model.Collection{ChannelID: secondChannel.ID, ContentType: "video", Name: "Second Videos"}
	if err := db.Create(&videoCollection).Error; err != nil {
		t.Fatal(err)
	}
	video := model.Video{UserID: creator.UUID, ChannelID: &secondChannel.ID, Title: "Second video", StorageType: "external", VideoURL: "https://example.com/second", Status: "published", Visibility: "public"}
	if err := db.Create(&video).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.VideoCollection{VideoID: video.ID, CollectionID: videoCollection.ID}).Error; err != nil {
		t.Fatal(err)
	}
	source := model.FeedSource{SourceType: "internal_user", SourceID: &creator.UUID, Hash: "unified-user-source", Title: creator.Username}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Subscription{UserID: viewer.ID, FeedSourceID: source.ID, Title: source.Title}).Error; err != nil {
		t.Fatal(err)
	}

	items, total, err := service.GetSubscribedFeed(viewer, FeedQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(items) != 4 {
		t.Fatalf("expected all updates from all owned channels, total=%d items=%#v", total, items)
	}
	assertUnifiedUpdates(t, items, blogPost.ID, episode.ID, video.ID)
}

func assertUnifiedUpdates(t *testing.T, items []TimelineItemDTO, blogPostID, episodeID, videoID uuid.UUID) {
	t.Helper()
	var foundBlog, foundPodcast, foundVideo bool
	for _, item := range items {
		foundBlog = foundBlog || item.Post != nil && item.Post.ID == blogPostID
		foundPodcast = foundPodcast || item.PodcastEpisode != nil && item.PodcastEpisode.ID == episodeID
		foundVideo = foundVideo || item.Video != nil && item.Video.ID == videoID
	}
	if !foundBlog || !foundPodcast || !foundVideo {
		t.Fatalf("missing unified updates: blog=%v podcast=%v video=%v items=%#v", foundBlog, foundPodcast, foundVideo, items)
	}
}

func TestGetSubscribedFeedReturnsMixedTimelineItems(t *testing.T) {
	service, db, user := newFeedTestService(t)
	items, total, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("get subscribed feed: %v", err)
	}
	if total == 0 || len(items) == 0 {
		t.Fatalf("expected timeline items")
	}

	hasPost := false
	hasFeedItem := false
	hasChannelPost := false
	hasCollectionPost := false
	for _, item := range items {
		switch item.Type {
		case "post":
			if item.Post != nil {
				hasPost = true
				if item.Post.Title == "Channel post" {
					hasChannelPost = true
				}
				if item.Post.Title == "Collection post" {
					hasCollectionPost = true
				}
			}
		case "feed_item":
			if item.FeedItem != nil {
				hasFeedItem = true
			}
		}
	}
	if !hasPost || !hasFeedItem || !hasChannelPost || !hasCollectionPost {
		t.Fatalf("expected mixed post/feed data including channel and collection posts, got %#v", items)
	}

	duplicateSource := model.FeedSource{SourceType: "external_rss", RssURL: "https://mirror.example.com/feed.xml", Hash: "mirror-rss-hash", Title: "Mirror Feed"}
	if err := db.Create(&duplicateSource).Error; err != nil {
		t.Fatalf("create duplicate source: %v", err)
	}
	duplicateItem := model.FeedItem{FeedSourceID: duplicateSource.ID, GUID: "guid-dup", Title: "Feed item", Link: "https://example.com/items/1", Summary: "dup", PublishedAt: time.Now().Add(-30 * time.Minute).UTC(), FetchedAt: time.Now().UTC()}
	if err := db.Create(&duplicateItem).Error; err != nil {
		t.Fatalf("create duplicate feed item: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: duplicateSource.ID, Title: "Mirror Feed"}).Error; err != nil {
		t.Fatalf("create duplicate subscription: %v", err)
	}

	filtered, _, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 50, HideDuplicates: true})
	if err != nil {
		t.Fatalf("get subscribed feed with duplicate filter: %v", err)
	}
	feedItemCount := 0
	for _, item := range filtered {
		if item.Type == "feed_item" {
			feedItemCount++
		}
	}
	if feedItemCount != 1 {
		t.Fatalf("expected duplicate filter to leave 1 feed item, got %d", feedItemCount)
	}
}

func TestGetSubscribedBlogFeedExcludesExternalAndPodcastContent(t *testing.T) {
	service, db, user := newFeedTestService(t)
	pagedPostQueryHadLimit := false
	pagedPostQueryUsedDistinct := false
	if err := db.Callback().Query().Before("gorm:query").Register("test:blog_timeline_limit", func(tx *gorm.DB) {
		if tx.Statement.Table != "posts" || reflect.TypeOf(tx.Statement.Dest) != reflect.TypeOf(&[]model.Post{}) {
			return
		}
		_, pagedPostQueryHadLimit = tx.Statement.Clauses["LIMIT"]
		pagedPostQueryUsedDistinct = tx.Statement.Distinct
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}

	var author model.User
	if err := db.First(&author, "uuid = ?", user.ID).Error; err != nil {
		t.Fatalf("find author: %v", err)
	}
	var channel model.Channel
	if err := db.Where("user_id = ?", author.UUID).First(&channel).Error; err != nil {
		t.Fatalf("find shared channel: %v", err)
	}
	podcastPost := model.Post{UserID: author.UUID, ChannelID: &channel.ID, Title: "Podcast episode", Content: "shownotes", Status: "published", Visibility: "public"}
	if err := db.Create(&podcastPost).Error; err != nil {
		t.Fatalf("create podcast post: %v", err)
	}
	if err := db.Create(&model.PodcastEpisode{PostID: podcastPost.ID, ChannelID: channel.ID, AudioURL: "https://cdn.example.com/episode.mp3"}).Error; err != nil {
		t.Fatalf("create podcast episode: %v", err)
	}

	items, total, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 1, ContentType: "blog"})
	if err != nil {
		t.Fatalf("get subscribed blog feed: %v", err)
	}
	if total < 3 || len(items) != 1 {
		t.Fatalf("expected filtered total with paginated data, got total=%d len=%d", total, len(items))
	}
	if !pagedPostQueryHadLimit {
		t.Fatal("expected blog timeline posts to be paged by the database")
	}
	if pagedPostQueryUsedDistinct {
		t.Fatal("expected blog timeline page query not to use SELECT DISTINCT")
	}
	seen := make(map[uuid.UUID]struct{})
	allItems, allTotal, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 100, ContentType: "blog"})
	if err != nil {
		t.Fatalf("get all subscribed blog posts: %v", err)
	}
	if int64(len(allItems)) != allTotal {
		t.Fatalf("expected total to reflect filtered results, got total=%d len=%d", allTotal, len(allItems))
	}
	for _, item := range allItems {
		if item.Type != "post" || item.Post == nil {
			t.Fatalf("expected blog mode to exclude external RSS, got %#v", item)
		}
		if item.Post.ID == podcastPost.ID {
			t.Fatalf("expected blog subscription to exclude podcast episode post")
		}
		if _, exists := seen[item.Post.ID]; exists {
			t.Fatalf("expected post %s to be deduplicated", item.Post.ID)
		}
		seen[item.Post.ID] = struct{}{}
	}
}

func TestSubscribedBlogFeedUsesCanonicalCollectionWithoutLegacyJoinTable(t *testing.T) {
	service, db, user := newFeedTestService(t)
	if err := db.Unscoped().Where("user_id = ?", user.ID).Delete(&model.Subscription{}).Error; err != nil {
		t.Fatalf("clear subscriptions: %v", err)
	}
	var source model.FeedSource
	if err := db.Where("source_type = ?", "internal_collection").First(&source).Error; err != nil {
		t.Fatalf("find collection source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: source.ID, Title: source.Title}).Error; err != nil {
		t.Fatalf("create collection subscription: %v", err)
	}
	var collection model.Collection
	if err := db.First(&collection, "id = ?", *source.SourceID).Error; err != nil {
		t.Fatalf("find collection: %v", err)
	}
	post := model.Post{UserID: user.ID, ChannelID: &collection.ChannelID, CollectionID: &collection.ID, Title: "Canonical collection post", Content: "body", Status: "published", Visibility: "public"}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create canonical collection post: %v", err)
	}
	if err := db.Migrator().DropTable(&model.PostCollection{}); err != nil {
		t.Fatalf("drop legacy post_collections table: %v", err)
	}

	items, total, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20, ContentType: "blog"})
	if err != nil {
		t.Fatalf("get collection blog feed without legacy table: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Post == nil || items[0].Post.ID != post.ID {
		t.Fatalf("expected canonical collection post, got total=%d items=%#v", total, items)
	}
}

func TestSubscribedBlogFeedExcludesPostsFromDeletedChannels(t *testing.T) {
	service, db, user := newFeedTestService(t)
	if err := db.Unscoped().Where("user_id = ?", user.ID).Delete(&model.Subscription{}).Error; err != nil {
		t.Fatalf("clear subscriptions: %v", err)
	}
	var source model.FeedSource
	if err := db.Where("source_type = ?", "internal_channel").First(&source).Error; err != nil {
		t.Fatalf("find channel source: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: source.ID, Title: source.Title}).Error; err != nil {
		t.Fatalf("create channel subscription: %v", err)
	}
	var channel model.Channel
	if err := db.First(&channel, "id = ?", *source.SourceID).Error; err != nil {
		t.Fatalf("find channel: %v", err)
	}
	if err := db.Delete(&channel).Error; err != nil {
		t.Fatalf("soft delete channel: %v", err)
	}

	items, total, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20, ContentType: "blog"})
	if err != nil {
		t.Fatalf("get blog feed after channel deletion: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected deleted channel posts excluded, got total=%d items=%#v", total, items)
	}
}

func TestSubscribedBlogFeedFollowerVisibilityRequiresAuthorOrChannelSubscription(t *testing.T) {
	tests := []struct {
		name             string
		sourceType       string
		wantFollowerPost bool
	}{
		{name: "author subscription grants access", sourceType: "internal_user", wantFollowerPost: true},
		{name: "channel subscription grants access", sourceType: "internal_channel", wantFollowerPost: true},
		{name: "collection subscription does not grant access", sourceType: "internal_collection", wantFollowerPost: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service, db, user := newFeedTestService(t)
			if err := db.Unscoped().Where("user_id = ?", user.ID).Delete(&model.Subscription{}).Error; err != nil {
				t.Fatalf("clear subscriptions: %v", err)
			}
			var source model.FeedSource
			if err := db.Where("source_type = ?", tc.sourceType).First(&source).Error; err != nil {
				t.Fatalf("find %s source: %v", tc.sourceType, err)
			}
			if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: source.ID, Title: source.Title}).Error; err != nil {
				t.Fatalf("create subscription: %v", err)
			}

			var channel model.Channel
			if err := db.Where("user_id = ?", user.ID).Order("created_at ASC, id ASC").First(&channel).Error; err != nil {
				t.Fatalf("find shared channel: %v", err)
			}
			var collection model.Collection
			if err := db.Where("channel_id = ?", channel.ID).First(&collection).Error; err != nil {
				t.Fatalf("find collection: %v", err)
			}
			followerPost := model.Post{UserID: user.ID, ChannelID: &channel.ID, CollectionID: &collection.ID, Title: "Followers only", Content: "body", Status: "published", Visibility: "followers"}
			privatePost := model.Post{UserID: user.ID, ChannelID: &channel.ID, CollectionID: &collection.ID, Title: "Private only", Content: "body", Status: "published", Visibility: "private"}
			if err := db.Create(&followerPost).Error; err != nil {
				t.Fatalf("create follower post: %v", err)
			}
			if err := db.Create(&privatePost).Error; err != nil {
				t.Fatalf("create private post: %v", err)
			}

			items, _, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 100, ContentType: "blog"})
			if err != nil {
				t.Fatalf("get subscribed blog feed: %v", err)
			}
			foundFollower := false
			for _, item := range items {
				if item.Post == nil {
					continue
				}
				if item.Post.ID == privatePost.ID {
					t.Fatalf("private post must never appear in subscribed blog feed")
				}
				foundFollower = foundFollower || item.Post.ID == followerPost.ID
			}
			if foundFollower != tc.wantFollowerPost {
				t.Fatalf("followers post visibility = %v, want %v", foundFollower, tc.wantFollowerPost)
			}
		})
	}
}

func TestSubscribedBlogFeedCombinesSourceAndGroupFilters(t *testing.T) {
	service, db, user := newFeedTestService(t)
	group := model.SubscriptionGroup{UserID: user.ID, Name: "Blog authors"}
	if err := db.Create(&group).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	var subscription model.Subscription
	if err := db.Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
		Where("subscriptions.user_id = ? AND feed_sources.source_type = ?", user.ID, "internal_user").
		First(&subscription).Error; err != nil {
		t.Fatalf("find author subscription: %v", err)
	}
	if err := db.Model(&subscription).Update("subscription_group_id", group.ID).Error; err != nil {
		t.Fatalf("assign subscription group: %v", err)
	}

	items, total, err := service.GetSubscribedFeed(user, FeedQuery{
		Page:        1,
		PageSize:    100,
		ContentType: "blog",
		SourceType:  "internal_user",
		SourceID:    subscription.ID,
		GroupID:     group.ID,
	})
	if err != nil {
		t.Fatalf("get filtered blog feed: %v", err)
	}
	if total == 0 || len(items) == 0 {
		t.Fatalf("expected matching source and group to return posts")
	}
	for _, item := range items {
		if item.Post == nil || item.Post.UserID != user.ID {
			t.Fatalf("expected only author subscription posts, got %#v", item)
		}
	}

	items, total, err = service.GetSubscribedFeed(user, FeedQuery{
		Page:        1,
		PageSize:    100,
		ContentType: "blog",
		SourceType:  "internal_user",
		SourceID:    subscription.ID,
		GroupID:     uuid.New(),
	})
	if err != nil {
		t.Fatalf("get mismatched group blog feed: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected mismatched group to return no posts, got total=%d len=%d", total, len(items))
	}
}

func TestSubscribedBlogFeedSortsByPublishedAt(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var channel model.Channel
	if err := db.Where("user_id = ?", user.ID).Order("created_at ASC, id ASC").First(&channel).Error; err != nil {
		t.Fatalf("find shared channel: %v", err)
	}
	now := time.Now().UTC()
	latePublishedAt := now
	earlyPublishedAt := now.Add(-24 * time.Hour)
	latePublished := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "publish-order late", Content: "body", Status: "published", Visibility: "public", PublishedAt: &latePublishedAt}
	earlyPublished := model.Post{UserID: user.ID, ChannelID: &channel.ID, Title: "publish-order early", Content: "body", Status: "published", Visibility: "public", PublishedAt: &earlyPublishedAt}
	if err := db.Create(&latePublished).Error; err != nil {
		t.Fatalf("create late-published post: %v", err)
	}
	if err := db.Create(&earlyPublished).Error; err != nil {
		t.Fatalf("create early-published post: %v", err)
	}
	if err := db.Model(&latePublished).Update("created_at", now.Add(-48*time.Hour)).Error; err != nil {
		t.Fatalf("backdate late-published post creation: %v", err)
	}

	items, total, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 10, ContentType: "blog", Search: "publish-order"})
	if err != nil {
		t.Fatalf("get subscribed blog feed: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected two matching posts, got total=%d len=%d", total, len(items))
	}
	if items[0].Post == nil || items[0].Post.ID != latePublished.ID {
		t.Fatalf("expected most recently published post first, got %#v", items)
	}
	if !items[0].PublishedAt.Equal(latePublishedAt) {
		t.Fatalf("expected timeline published_at %s, got %s", latePublishedAt, items[0].PublishedAt)
	}
}

func TestGetSubscribedFeedSearchesTitleSummaryAndSource(t *testing.T) {
	service, db, user := newFeedTestService(t)

	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatalf("find external source: %v", err)
	}
	source.Title = "Example Source Search"
	if err := db.Save(&source).Error; err != nil {
		t.Fatalf("update source title: %v", err)
	}

	var feedItem model.FeedItem
	if err := db.Where("title = ?", "Feed item").First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	feedItem.Summary = "A rare citrus summary"
	if err := db.Save(&feedItem).Error; err != nil {
		t.Fatalf("update feed item summary: %v", err)
	}

	cases := []struct {
		name      string
		search    string
		wantType  string
		wantTitle string
	}{
		{name: "internal post title", search: "Channel post", wantType: "post", wantTitle: "Channel post"},
		{name: "external summary", search: "rare citrus", wantType: "feed_item", wantTitle: "Feed item"},
		{name: "external source", search: "source search", wantType: "feed_item", wantTitle: "Feed item"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items, total, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20, Search: tc.search})
			if err != nil {
				t.Fatalf("get subscribed feed: %v", err)
			}
			if total == 0 || len(items) == 0 {
				t.Fatalf("expected search results for %q", tc.search)
			}
			for _, item := range items {
				switch {
				case tc.wantType == "post" && item.Type == "post" && item.Post != nil && item.Post.Title == tc.wantTitle:
					return
				case tc.wantType == "feed_item" && item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.Title == tc.wantTitle:
					return
				}
			}
			t.Fatalf("expected %s title %q in results: %#v", tc.wantType, tc.wantTitle, items)
		})
	}

	items, total, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 20, Search: "definitely absent"})
	if err != nil {
		t.Fatalf("get subscribed feed: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("expected no results for absent query, got total=%d items=%#v", total, items)
	}
}

func TestGetSubscribedFeedLimitsFeedItemQueryToRequestedPage(t *testing.T) {
	service, db, user := newFeedTestService(t)

	if err := db.Exec("DELETE FROM subscriptions WHERE feed_source_id IN (SELECT id FROM feed_sources WHERE source_type <> ?)", "external_rss").Error; err != nil {
		t.Fatalf("delete internal subscriptions: %v", err)
	}

	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatalf("find external source: %v", err)
	}

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		item := model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         fmt.Sprintf("paged-guid-%d", i),
			Title:        fmt.Sprintf("Paged feed item %d", i),
			Link:         fmt.Sprintf("https://example.com/paged/%d", i),
			PublishedAt:  now.Add(time.Duration(i) * time.Minute),
			FetchedAt:    now.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create paged feed item: %v", err)
		}
	}

	var sql bytes.Buffer
	loggedDB := db.Session(&gorm.Session{
		Logger: logger.New(log.New(&sql, "", 0), logger.Config{
			LogLevel: logger.Info,
			Colorful: false,
		}),
	})
	service = NewService(loggedDB)

	items, total, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("get subscribed feed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one item on first page, got %d", len(items))
	}
	if total <= int64(len(items)) {
		t.Fatalf("expected total to reflect all matching items, got total=%d len=%d", total, len(items))
	}

	queries := sql.String()
	feedItemQueryStart := strings.Index(queries, "FROM `feed_items`")
	if feedItemQueryStart == -1 || !strings.Contains(strings.ToUpper(queries[feedItemQueryStart:]), "LIMIT 1") {
		t.Fatalf("expected feed item timeline query to be limited to requested page, got SQL:\n%s", queries)
	}
}

func TestGetPublicFeedLimitsFeedItemQueryToRequestedPage(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatalf("find external source: %v", err)
	}

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		item := model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         fmt.Sprintf("public-paged-guid-%d", i),
			Title:        fmt.Sprintf("Public paged feed item %d", i),
			Link:         fmt.Sprintf("https://example.com/public-paged/%d", i),
			PublishedAt:  now.Add(time.Duration(i) * time.Minute),
			FetchedAt:    now.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create public paged feed item: %v", err)
		}
	}

	var sql bytes.Buffer
	loggedDB := db.Session(&gorm.Session{
		Logger: logger.New(log.New(&sql, "", 0), logger.Config{
			LogLevel: logger.Info,
			Colorful: false,
		}),
	})
	service = NewService(loggedDB)

	items, total, err := service.GetPublicFeed(FeedQuery{Page: 1, PageSize: 1})
	if err != nil {
		t.Fatalf("get public feed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one item on first page, got %d", len(items))
	}
	if total <= int64(len(items)) {
		t.Fatalf("expected total to reflect all matching public items, got total=%d len=%d", total, len(items))
	}

	queries := sql.String()
	feedItemQueryStart := strings.Index(queries, "FROM `feed_items`")
	if feedItemQueryStart == -1 || !strings.Contains(strings.ToUpper(queries[feedItemQueryStart:]), "LIMIT 1") {
		t.Fatalf("expected public feed item query to be limited to requested page, got SQL:\n%s", queries)
	}
}

func TestGetPublicFeedBySourceIDReturnsOnlyRequestedSource(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatalf("find external source: %v", err)
	}

	otherSource := model.FeedSource{SourceType: "external_rss", RssURL: "https://other.example.com/feed.xml", Hash: "other-external-rss-hash", Title: "Other Feed"}
	if err := db.Create(&otherSource).Error; err != nil {
		t.Fatalf("create other source: %v", err)
	}

	otherPublishedAt := time.Now().Add(-10 * time.Minute).UTC()
	otherItem := model.FeedItem{
		FeedSourceID: otherSource.ID,
		GUID:         "other-guid-1",
		Title:        "Other feed item",
		Link:         "https://other.example.com/items/1",
		PublishedAt:  otherPublishedAt,
		FetchedAt:    otherPublishedAt,
	}
	if err := db.Create(&otherItem).Error; err != nil {
		t.Fatalf("create other feed item: %v", err)
	}

	items, total, err := service.GetPublicFeedBySourceID(source.ID, FeedQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("get public feed by source id: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected exactly one item from requested source, got total=%d len=%d", total, len(items))
	}
	if items[0].Type != "feed_item" || items[0].FeedItem == nil {
		t.Fatalf("expected feed item timeline entry, got %#v", items[0])
	}
	if items[0].FeedItem.FeedSourceID != source.ID {
		t.Fatalf("expected source %s, got %s", source.ID, items[0].FeedItem.FeedSourceID)
	}
	if items[0].FeedItem.Title != "Feed item" {
		t.Fatalf("expected original source item title, got %s", items[0].FeedItem.Title)
	}
}

func TestUnaggregatedTimelinePathsOmitEngagementCounts(t *testing.T) {
	service, _, user := newFeedTestService(t)

	tests := []struct {
		name string
		load func() ([]TimelineItemDTO, error)
	}{
		{
			name: "public",
			load: func() ([]TimelineItemDTO, error) {
				items, _, err := service.GetPublicFeed(FeedQuery{Page: 1, PageSize: 20})
				return items, err
			},
		},
		{
			name: "public with duplicate filtering",
			load: func() ([]TimelineItemDTO, error) {
				items, _, err := service.GetPublicFeed(FeedQuery{Page: 1, PageSize: 20, HideDuplicates: true})
				return items, err
			},
		},
		{
			name: "explore",
			load: func() ([]TimelineItemDTO, error) {
				items, _, err := service.GetExploreFeed(user, FeedQuery{Page: 1, PageSize: 20, Sort: "recent"})
				return items, err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			items, err := tc.load()
			if err != nil {
				t.Fatalf("load timeline: %v", err)
			}
			for _, item := range items {
				if item.Post == nil {
					continue
				}
				payload, err := json.Marshal(item.Post)
				if err != nil {
					t.Fatalf("marshal post: %v", err)
				}
				if strings.Contains(string(payload), "likes_count") || strings.Contains(string(payload), "comments_count") {
					t.Fatalf("unaggregated timeline must not expose engagement counts: %s", payload)
				}
				return
			}
			t.Fatal("expected at least one post")
		})
	}
}

func TestGetExploreFeedPaginatesAfterGlobalSort(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	if err := db.Exec("DELETE FROM feed_items").Error; err != nil {
		t.Fatalf("delete seed feed items: %v", err)
	}
	if err := db.Exec("DELETE FROM posts").Error; err != nil {
		t.Fatalf("delete seed posts: %v", err)
	}

	var sourceA model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&sourceA).Error; err != nil {
		t.Fatalf("find source A: %v", err)
	}
	sourceB := model.FeedSource{SourceType: "external_rss", RssURL: "https://second.example.com/feed.xml", Hash: "explore-page-source-b", Title: "Second Feed"}
	if err := db.Create(&sourceB).Error; err != nil {
		t.Fatalf("create source B: %v", err)
	}

	base := time.Date(2026, time.June, 1, 12, 0, 0, 0, time.UTC)
	seedExploreFeedItem(t, db, sourceA.ID, "a-new", "A newest", base.Add(4*time.Minute), "https://example.com/a-new")
	seedExploreFeedItem(t, db, sourceA.ID, "a-old", "A old", base.Add(1*time.Minute), "https://example.com/a-old")
	seedExploreFeedItem(t, db, sourceB.ID, "b-new", "B newest", base.Add(3*time.Minute), "https://example.com/b-new")
	seedExploreFeedItem(t, db, sourceB.ID, "b-mid", "B middle", base.Add(2*time.Minute), "https://example.com/b-mid")

	items, total, err := service.GetExploreFeed(authctx.CurrentUser{}, FeedQuery{Page: 2, PageSize: 2, Sort: "recent"})
	if err != nil {
		t.Fatalf("get explore feed: %v", err)
	}
	if total != 4 {
		t.Fatalf("expected total 4, got %d", total)
	}
	got := feedItemTitles(items)
	want := []string{"B middle", "A old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected global page 2 titles %#v, got %#v", want, got)
	}
}

func TestGetExploreFeedAppliesDuplicateAndReadFilters(t *testing.T) {
	service, db, user := newFeedTestService(t)

	if err := db.Exec("DELETE FROM feed_item_reads").Error; err != nil {
		t.Fatalf("delete seed reads: %v", err)
	}
	if err := db.Exec("DELETE FROM feed_items").Error; err != nil {
		t.Fatalf("delete seed feed items: %v", err)
	}
	if err := db.Exec("DELETE FROM posts").Error; err != nil {
		t.Fatalf("delete seed posts: %v", err)
	}

	var sourceA model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&sourceA).Error; err != nil {
		t.Fatalf("find source A: %v", err)
	}
	sourceB := model.FeedSource{SourceType: "external_rss", RssURL: "https://mirror-filter.example.com/feed.xml", Hash: "explore-filter-source-b", Title: "Mirror Filter Feed"}
	if err := db.Create(&sourceB).Error; err != nil {
		t.Fatalf("create source B: %v", err)
	}

	now := time.Now().UTC()
	readItem := seedExploreFeedItem(t, db, sourceA.ID, "read-guid", "Read item", now.Add(3*time.Minute), "https://example.com/read")
	seedExploreFeedItem(t, db, sourceA.ID, "canonical-guid", "Canonical item", now.Add(2*time.Minute), "https://example.com/duplicate")
	seedExploreFeedItem(t, db, sourceB.ID, "duplicate-guid", "Duplicate item", now.Add(time.Minute), "https://example.com/duplicate")
	unreadItem := seedExploreFeedItem(t, db, sourceA.ID, "unread-guid", "Unread item", now, "https://example.com/unread")
	if err := db.Create(&model.FeedItemRead{UserID: user.ID, FeedItemID: readItem.ID, ReadAt: now}).Error; err != nil {
		t.Fatalf("mark read item: %v", err)
	}

	readOnly := true
	readItems, readTotal, err := service.GetExploreFeed(user, FeedQuery{Page: 1, PageSize: 20, Sort: "recent", IsRead: &readOnly})
	if err != nil {
		t.Fatalf("get read explore feed: %v", err)
	}
	if readTotal != 1 || len(readItems) != 1 || readItems[0].FeedItem == nil || readItems[0].FeedItem.ID != readItem.ID {
		t.Fatalf("expected only read item %s, got total=%d items=%#v", readItem.ID, readTotal, readItems)
	}

	unreadOnly := false
	filtered, total, err := service.GetExploreFeed(user, FeedQuery{Page: 1, PageSize: 20, Sort: "recent", IsRead: &unreadOnly, HideDuplicates: true})
	if err != nil {
		t.Fatalf("get unread deduped explore feed: %v", err)
	}
	got := feedItemTitles(filtered)
	want := []string{"Canonical item", "Unread item"}
	if total != int64(len(want)) || !reflect.DeepEqual(got, want) {
		t.Fatalf("expected unread deduped titles %#v with total %d, got titles=%#v total=%d", want, len(want), got, total)
	}
	for _, item := range filtered {
		if item.FeedItem != nil && item.FeedItem.ID == unreadItem.ID && item.IsRead {
			t.Fatalf("expected unread item to be marked unread")
		}
	}
}

func seedExploreFeedItem(t *testing.T, db *gorm.DB, sourceID uuid.UUID, guid string, title string, publishedAt time.Time, link string) model.FeedItem {
	t.Helper()
	item := model.FeedItem{
		FeedSourceID: sourceID,
		GUID:         guid,
		Title:        title,
		Link:         link,
		PublishedAt:  publishedAt,
		FetchedAt:    publishedAt,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatalf("create explore feed item %q: %v", title, err)
	}
	return item
}

func feedItemTitles(items []TimelineItemDTO) []string {
	titles := make([]string, 0, len(items))
	for _, item := range items {
		if item.FeedItem != nil {
			titles = append(titles, item.FeedItem.Title)
		}
	}
	return titles
}

func TestParseExploreSourceTimestamp(t *testing.T) {
	raw := "2026-06-19 02:31:53.123456789+08:00"
	parsed, err := parseExploreSourceTimestamp(raw)
	if err != nil {
		t.Fatalf("parse explore source timestamp: %v", err)
	}
	expected := time.Date(2026, time.June, 19, 2, 31, 53, 123456789, time.FixedZone("", 8*60*60))
	if !parsed.Equal(expected) {
		t.Fatalf("expected %s, got %s", expected.Format(time.RFC3339Nano), parsed.Format(time.RFC3339Nano))
	}
}

func TestParseExploreSourceTimestampRFC3339Nano(t *testing.T) {
	raw := "2026-06-19T02:31:53.123456789Z"
	parsed, err := parseExploreSourceTimestamp(raw)
	if err != nil {
		t.Fatalf("parse RFC3339Nano explore source timestamp: %v", err)
	}
	expected := time.Date(2026, time.June, 19, 2, 31, 53, 123456789, time.UTC)
	if !parsed.Equal(expected) {
		t.Fatalf("expected %s, got %s", expected.Format(time.RFC3339Nano), parsed.Format(time.RFC3339Nano))
	}
}

func TestParseExploreSourceTimestampInvalid(t *testing.T) {
	if _, err := parseExploreSourceTimestamp("not-a-timestamp"); err == nil {
		t.Fatal("expected invalid timestamp to return an error")
	}
}

func TestListExploreSourcesExcludesHiddenSources(t *testing.T) {
	service, db, user := newFeedTestService(t)

	hiddenSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://hidden-explore.example.com/feed.xml",
		Hash:         "hidden-explore-source-hash",
		Title:        "Hidden Explore Source",
		Hidden:       true,
		HealthStatus: "healthy",
	}
	if err := db.Create(&hiddenSource).Error; err != nil {
		t.Fatalf("create hidden source: %v", err)
	}

	hiddenItem := model.FeedItem{
		FeedSourceID: hiddenSource.ID,
		GUID:         "hidden-explore-guid",
		Title:        "Hidden Explore Item",
		Link:         "https://hidden-explore.example.com/items/1",
		PublishedAt:  time.Now().UTC(),
		FetchedAt:    time.Now().UTC(),
	}
	if err := db.Create(&hiddenItem).Error; err != nil {
		t.Fatalf("create hidden item: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: hiddenSource.ID, Title: hiddenSource.Title}).Error; err != nil {
		t.Fatalf("create hidden subscription: %v", err)
	}

	rows, err := service.repo.ListExploreSources(20, 0, "")
	if err != nil {
		t.Fatalf("list explore sources: %v", err)
	}
	for _, row := range rows {
		if row.ID == hiddenSource.ID {
			t.Fatalf("hidden source leaked into explore sources: %#v", row)
		}
	}
}

func TestListExploreSourcesIncludesRecentItemPreviews(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	var source model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&source).Error; err != nil {
		t.Fatalf("find external source: %v", err)
	}

	publishedAt := time.Now().UTC()
	for i, title := range []string{"Fourth newest", "Third newest", "Second newest", "Newest"} {
		item := model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         fmt.Sprintf("preview-guid-%d", i),
			Title:        title,
			Link:         fmt.Sprintf("https://example.com/previews/%d", i),
			PublishedAt:  publishedAt.Add(time.Duration(i) * time.Minute),
			FetchedAt:    publishedAt.Add(time.Duration(i) * time.Minute),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create preview item %s: %v", title, err)
		}
	}

	rows, err := service.repo.ListExploreSources(20, 0, "")
	if err != nil {
		t.Fatalf("list explore sources: %v", err)
	}

	var target *ExploreSourceRow
	for i := range rows {
		if rows[i].ID == source.ID {
			target = &rows[i]
			break
		}
	}
	if target == nil {
		t.Fatalf("expected source %s in explore rows, got %#v", source.ID, rows)
	}

	if len(target.RecentItems) != 3 {
		t.Fatalf("expected 3 recent item previews, got %#v", target.RecentItems)
	}
	gotTitles := []string{target.RecentItems[0].Title, target.RecentItems[1].Title, target.RecentItems[2].Title}
	wantTitles := []string{"Newest", "Second newest", "Third newest"}
	for i := range wantTitles {
		if gotTitles[i] != wantTitles[i] {
			t.Fatalf("expected preview titles %#v, got %#v", wantTitles, gotTitles)
		}
	}
}

func TestListExploreSourcesFiltersByCategory(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	newsSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://news.example.com/feed.xml",
		Hash:         "category-news-source-hash",
		Title:        "Category News",
		Category:     "news",
		HealthStatus: "healthy",
	}
	if err := db.Create(&newsSource).Error; err != nil {
		t.Fatalf("create news source: %v", err)
	}
	blogSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://blog.example.com/feed.xml",
		Hash:         "category-blog-source-hash",
		Title:        "Category Blog",
		Category:     "blog",
		HealthStatus: "healthy",
	}
	if err := db.Create(&blogSource).Error; err != nil {
		t.Fatalf("create blog source: %v", err)
	}

	publishedAt := time.Now().UTC()
	for _, source := range []model.FeedSource{newsSource, blogSource} {
		item := model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         "category-item-" + source.ID.String(),
			Title:        source.Title + " Item",
			Link:         "https://example.com/category-item",
			PublishedAt:  publishedAt,
			FetchedAt:    publishedAt,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create category feed item: %v", err)
		}
	}

	rows, err := service.repo.ListExploreSources(20, 0, "news")
	if err != nil {
		t.Fatalf("list news explore sources: %v", err)
	}

	if len(rows) == 0 {
		t.Fatal("expected news category rows")
	}
	for _, row := range rows {
		if row.Category != "news" {
			t.Fatalf("expected only news rows, got %#v", rows)
		}
		if row.ID == blogSource.ID {
			t.Fatalf("blog source leaked into news category rows: %#v", rows)
		}
	}
}

func TestListExploreSourcesInfersCategoryForLegacyBlogDefaultSources(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	forumSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://v2ex.com/index.xml",
		Hash:         "legacy-default-forum-source-hash",
		Title:        "V2EX",
		Category:     "blog",
		HealthStatus: "healthy",
	}
	if err := db.Create(&forumSource).Error; err != nil {
		t.Fatalf("create legacy forum source: %v", err)
	}
	blogSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://daily-blog.example.com/feed.xml",
		Hash:         "legacy-default-blog-source-hash",
		Title:        "Daily Blog",
		Category:     "blog",
		HealthStatus: "healthy",
	}
	if err := db.Create(&blogSource).Error; err != nil {
		t.Fatalf("create legacy blog source: %v", err)
	}

	publishedAt := time.Now().UTC()
	for _, source := range []model.FeedSource{forumSource, blogSource} {
		item := model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         "legacy-category-item-" + source.ID.String(),
			Title:        source.Title + " Item",
			Link:         "https://example.com/legacy-category-item",
			PublishedAt:  publishedAt,
			FetchedAt:    publishedAt,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create legacy category feed item: %v", err)
		}
	}

	rows, err := service.repo.ListExploreSources(20, 0, "forum")
	if err != nil {
		t.Fatalf("list forum explore sources: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("expected one inferred forum row, got %#v", rows)
	}
	if rows[0].ID != forumSource.ID || rows[0].Category != "forum" {
		t.Fatalf("expected legacy default blog source to be inferred as forum, got %#v", rows[0])
	}
}

func TestListExploreSourcesBlogFilterExcludesLegacyInferredSocialSources(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	socialSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://x.com/example/rss",
		Hash:         "legacy-default-social-source-hash",
		Title:        "Example Social Feed",
		Category:     "",
		HealthStatus: "healthy",
	}
	if err := db.Create(&socialSource).Error; err != nil {
		t.Fatalf("create social source: %v", err)
	}

	blogSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://daily-blog.example.com/feed.xml",
		Hash:         "legacy-default-plain-blog-source-hash",
		Title:        "Daily Blog",
		Category:     "",
		HealthStatus: "healthy",
	}
	if err := db.Create(&blogSource).Error; err != nil {
		t.Fatalf("create blog source: %v", err)
	}

	publishedAt := time.Now().UTC()
	for _, source := range []model.FeedSource{socialSource, blogSource} {
		item := model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         "legacy-blog-filter-item-" + source.ID.String(),
			Title:        source.Title + " Item",
			Link:         "https://example.com/legacy-blog-filter-item",
			PublishedAt:  publishedAt,
			FetchedAt:    publishedAt,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create feed item: %v", err)
		}
	}

	rows, err := service.repo.ListExploreSources(20, 0, "blog")
	if err != nil {
		t.Fatalf("list blog explore sources: %v", err)
	}

	for _, row := range rows {
		if row.ID == socialSource.ID {
			t.Fatalf("legacy inferred social source leaked into blog category rows: %#v", rows)
		}
	}

	foundBlogSource := false
	for _, row := range rows {
		if row.ID == blogSource.ID {
			foundBlogSource = true
			break
		}
	}
	if !foundBlogSource {
		t.Fatalf("expected plain blog source to remain in blog rows, got %#v", rows)
	}
}

func TestListExploreSourcesSocialFilterCoversKnownSocialHosts(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	sources := []model.FeedSource{
		{
			SourceType:   "external_rss",
			RssURL:       "https://rsshub.app/twitter/user/openai",
			Hash:         "social-twitter-source-hash",
			Title:        "OpenAI on Twitter",
			Category:     "",
			HealthStatus: "healthy",
		},
		{
			SourceType:   "external_rss",
			RssURL:       "https://rsshub.app/jike/user/123456",
			Hash:         "social-jike-source-hash",
			Title:        "即刻用户动态",
			Category:     "",
			HealthStatus: "healthy",
		},
		{
			SourceType:   "external_rss",
			RssURL:       "https://www.zhihu.com/rss",
			Hash:         "social-zhihu-source-hash",
			Title:        "知乎回答",
			Category:     "",
			HealthStatus: "healthy",
		},
		{
			SourceType:   "external_rss",
			RssURL:       "https://www.reddit.com/r/programming/.rss",
			Hash:         "social-reddit-source-hash",
			Title:        "Reddit Programming",
			Category:     "",
			HealthStatus: "healthy",
		},
	}

	now := time.Now().UTC()
	for index, source := range sources {
		if err := db.Create(&source).Error; err != nil {
			t.Fatalf("create social source %d: %v", index, err)
		}
		if err := db.Create(&model.FeedItem{
			FeedSourceID: source.ID,
			GUID:         fmt.Sprintf("social-item-%d", index),
			Title:        source.Title + " Item",
			Link:         fmt.Sprintf("https://example.com/social-item-%d", index),
			PublishedAt:  now,
			FetchedAt:    now,
		}).Error; err != nil {
			t.Fatalf("create social feed item %d: %v", index, err)
		}
	}

	rows, err := service.repo.ListExploreSources(50, 0, "social")
	if err != nil {
		t.Fatalf("list social explore sources: %v", err)
	}

	found := make(map[string]bool, len(sources))
	for _, row := range rows {
		if row.Category != "social" {
			t.Fatalf("expected only social rows, got %#v", rows)
		}
		found[row.Title] = true
	}
	for _, source := range sources {
		if !found[source.Title] {
			t.Fatalf("expected source %q in social rows, got %#v", source.Title, rows)
		}
	}
}

func TestListExploreSourcesNewsFilterIgnoresPollutedStoredCategory(t *testing.T) {
	service, db, _ := newFeedTestService(t)

	pollutedNewsSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://stats.gov.cn/sj/zxfb/rss.xml",
		Hash:         "polluted-news-source-hash",
		Title:        "数据发布",
		Category:     "social",
		HealthStatus: "healthy",
	}
	if err := db.Create(&pollutedNewsSource).Error; err != nil {
		t.Fatalf("create polluted news source: %v", err)
	}

	now := time.Now().UTC()
	if err := db.Create(&model.FeedItem{
		FeedSourceID: pollutedNewsSource.ID,
		GUID:         "polluted-news-item",
		Title:        "统计公报",
		Link:         "https://stats.gov.cn/item",
		PublishedAt:  now,
		FetchedAt:    now,
	}).Error; err != nil {
		t.Fatalf("create polluted news item: %v", err)
	}

	newsRows, err := service.repo.ListExploreSources(20, 0, "news")
	if err != nil {
		t.Fatalf("list news explore sources: %v", err)
	}
	foundInNews := false
	for _, row := range newsRows {
		if row.ID == pollutedNewsSource.ID {
			foundInNews = true
			if row.Category != "news" {
				t.Fatalf("expected polluted news source to be inferred as news, got %#v", row)
			}
		}
	}
	if !foundInNews {
		t.Fatalf("expected polluted news source in news rows, got %#v", newsRows)
	}

	socialRows, err := service.repo.ListExploreSources(20, 0, "social")
	if err != nil {
		t.Fatalf("list social explore sources: %v", err)
	}
	for _, row := range socialRows {
		if row.ID == pollutedNewsSource.ID {
			t.Fatalf("polluted stored category leaked news source into social rows: %#v", socialRows)
		}
	}
}

func TestListExploreSourcesOrdersBySubscriptionCountThenFreshness(t *testing.T) {
	service, db, user := newFeedTestService(t)

	var baseline model.FeedSource
	if err := db.Where("source_type = ?", "external_rss").First(&baseline).Error; err != nil {
		t.Fatalf("find baseline source: %v", err)
	}

	secondUser := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, DisplayName: "Bob", IsActive: true}
	if err := db.Create(&secondUser).Error; err != nil {
		t.Fatalf("create second user: %v", err)
	}
	thirdUser := model.User{Username: "carol", Email: "carol@example.com", Password: "hash", Role: authctx.RoleUser, DisplayName: "Carol", IsActive: true}
	if err := db.Create(&thirdUser).Error; err != nil {
		t.Fatalf("create third user: %v", err)
	}

	mostSubscribed := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://ranked-most.example.com/feed.xml",
		Hash:         "ranked-most-source-hash",
		Title:        "Ranked Most Subscribed",
		HealthStatus: "healthy",
	}
	if err := db.Create(&mostSubscribed).Error; err != nil {
		t.Fatalf("create most subscribed source: %v", err)
	}
	if err := db.Model(&mostSubscribed).Update("created_at", time.Now().Add(-4*time.Hour).UTC()).Error; err != nil {
		t.Fatalf("set most subscribed created_at: %v", err)
	}

	tiedOlder := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://ranked-tied-older.example.com/feed.xml",
		Hash:         "ranked-tied-older-source-hash",
		Title:        "Ranked Tied Older",
		HealthStatus: "healthy",
	}
	if err := db.Create(&tiedOlder).Error; err != nil {
		t.Fatalf("create tied older source: %v", err)
	}
	if err := db.Model(&tiedOlder).Update("created_at", time.Now().Add(-3*time.Hour).UTC()).Error; err != nil {
		t.Fatalf("set tied older created_at: %v", err)
	}

	tiedNewer := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://ranked-tied-newer.example.com/feed.xml",
		Hash:         "ranked-tied-newer-source-hash",
		Title:        "Ranked Tied Newer",
		HealthStatus: "healthy",
	}
	if err := db.Create(&tiedNewer).Error; err != nil {
		t.Fatalf("create tied newer source: %v", err)
	}
	if err := db.Model(&tiedNewer).Update("created_at", time.Now().Add(-2*time.Hour).UTC()).Error; err != nil {
		t.Fatalf("set tied newer created_at: %v", err)
	}

	publishedAt := time.Now().Add(-30 * time.Minute).UTC()
	for _, tc := range []struct {
		source model.FeedSource
		guid   string
		title  string
	}{
		{source: mostSubscribed, guid: "ranked-most-guid", title: "Ranked Most Item"},
		{source: tiedOlder, guid: "ranked-tied-older-guid", title: "Ranked Tied Older Item"},
		{source: tiedNewer, guid: "ranked-tied-newer-guid", title: "Ranked Tied Newer Item"},
	} {
		item := model.FeedItem{
			FeedSourceID: tc.source.ID,
			GUID:         tc.guid,
			Title:        tc.title,
			Link:         fmt.Sprintf("https://example.com/%s", tc.guid),
			PublishedAt:  publishedAt,
			FetchedAt:    publishedAt.Add(5 * time.Minute),
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create ranked feed item for %s: %v", tc.source.Title, err)
		}
	}

	for _, sub := range []model.Subscription{
		{UserID: secondUser.UUID, FeedSourceID: mostSubscribed.ID, Title: mostSubscribed.Title},
		{UserID: thirdUser.UUID, FeedSourceID: mostSubscribed.ID, Title: mostSubscribed.Title},
		{UserID: secondUser.UUID, FeedSourceID: tiedOlder.ID, Title: tiedOlder.Title},
		{UserID: user.ID, FeedSourceID: tiedNewer.ID, Title: tiedNewer.Title},
	} {
		if err := db.Create(&sub).Error; err != nil {
			t.Fatalf("create ranked subscription %+v: %v", sub, err)
		}
	}

	rows, err := service.repo.ListExploreSources(20, 0, "")
	if err != nil {
		t.Fatalf("list explore sources: %v", err)
	}
	if len(rows) < 4 {
		t.Fatalf("expected at least four explore sources, got %d", len(rows))
	}

	rowIndexByID := make(map[any]int, len(rows))
	for i, row := range rows {
		rowIndexByID[row.ID] = i
	}

	if rows[0].SubscriptionCount < rows[1].SubscriptionCount {
		t.Fatalf("expected source ordering by subscription_count desc, got %#v", rows)
	}
	if rowIndexByID[mostSubscribed.ID] >= rowIndexByID[tiedNewer.ID] {
		t.Fatalf("expected most subscribed source before tied newer source, got %#v", rows)
	}
	if rowIndexByID[tiedNewer.ID] >= rowIndexByID[tiedOlder.ID] {
		t.Fatalf("expected newer created source to break complete ties, got %#v", rows)
	}
	if rowIndexByID[tiedOlder.ID] >= rowIndexByID[baseline.ID] {
		t.Fatalf("expected fresher source to outrank older baseline on equal subscriptions, got %#v", rows)
	}
}

func TestListReadingListReturnsPagedItems(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var feedItem model.FeedItem
	if err := db.First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	if err := db.Create(&model.ReadingListItem{UserID: user.ID, TargetType: "feed_item", TargetID: feedItem.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create reading list item: %v", err)
	}

	items, total, err := service.ListReadingList(user, FeedQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list reading list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one reading list item, got total=%d len=%d", total, len(items))
	}
	if items[0].TargetType != "feed_item" || items[0].TargetID != feedItem.ID {
		t.Fatalf("expected feed item target %s, got %#v", feedItem.ID, items[0])
	}
	if items[0].FeedItem == nil || items[0].FeedItem.Title != feedItem.Title {
		t.Fatalf("expected preloaded feed item, got %#v", items[0].FeedItem)
	}
}

func TestSubscribedFeedAndExploreExcludeHiddenFeedSources(t *testing.T) {
	service, db, user := newFeedTestService(t)

	hiddenSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://hidden.example.com/feed.xml",
		Hash:         "hidden-rss-hash",
		Title:        "Hidden Feed",
		Hidden:       true,
		HealthStatus: "healthy",
	}
	if err := db.Create(&hiddenSource).Error; err != nil {
		t.Fatalf("create hidden source: %v", err)
	}
	hiddenItem := model.FeedItem{
		FeedSourceID: hiddenSource.ID,
		GUID:         "hidden-guid",
		Title:        "Hidden item",
		Link:         "https://hidden.example.com/items/1",
		PublishedAt:  time.Now().UTC(),
		FetchedAt:    time.Now().UTC(),
	}
	if err := db.Create(&hiddenItem).Error; err != nil {
		t.Fatalf("create hidden feed item: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: hiddenSource.ID, Title: "Hidden Feed"}).Error; err != nil {
		t.Fatalf("create hidden subscription: %v", err)
	}

	subscribedItems, _, err := service.GetSubscribedFeed(user, FeedQuery{Page: 1, PageSize: 100})
	if err != nil {
		t.Fatalf("get subscribed feed: %v", err)
	}
	for _, item := range subscribedItems {
		if item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.ID == hiddenItem.ID {
			t.Fatalf("hidden feed item leaked into subscribed feed: %#v", item)
		}
	}

	exploreItems, _, err := service.GetExploreFeed(user, FeedQuery{Page: 1, PageSize: 100, Sort: "popular"})
	if err != nil {
		t.Fatalf("get explore feed: %v", err)
	}
	for _, item := range exploreItems {
		if item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.ID == hiddenItem.ID {
			t.Fatalf("hidden feed item leaked into explore feed: %#v", item)
		}
	}
}

func TestMarkAllReadSkipsHiddenFeedSources(t *testing.T) {
	service, db, user := newFeedTestService(t)

	hiddenSource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://hidden.example.com/feed.xml",
		Hash:         "hidden-mark-read-rss-hash",
		Title:        "Hidden Feed",
		Hidden:       true,
		HealthStatus: "healthy",
	}
	if err := db.Create(&hiddenSource).Error; err != nil {
		t.Fatalf("create hidden source: %v", err)
	}
	hiddenItem := model.FeedItem{
		FeedSourceID: hiddenSource.ID,
		GUID:         "hidden-mark-read-guid",
		Title:        "Hidden mark read item",
		Link:         "https://hidden.example.com/items/mark-read",
		PublishedAt:  time.Now().UTC(),
		FetchedAt:    time.Now().UTC(),
	}
	if err := db.Create(&hiddenItem).Error; err != nil {
		t.Fatalf("create hidden feed item: %v", err)
	}
	if err := db.Create(&model.Subscription{UserID: user.ID, FeedSourceID: hiddenSource.ID, Title: "Hidden Feed"}).Error; err != nil {
		t.Fatalf("create hidden subscription: %v", err)
	}

	if err := service.MarkAllRead(user); err != nil {
		t.Fatalf("mark all read: %v", err)
	}

	var count int64
	if err := db.Model(&model.FeedItemRead{}).Where("user_id = ? AND feed_item_id = ?", user.ID, hiddenItem.ID).Count(&count).Error; err != nil {
		t.Fatalf("count hidden read records: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected hidden feed item to stay unread, got %d read records", count)
	}
}

func TestRemoveReadingListItemDeletesUserItem(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var feedItem model.FeedItem
	if err := db.First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	if err := db.Create(&model.ReadingListItem{UserID: user.ID, TargetType: "feed_item", TargetID: feedItem.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create reading list item: %v", err)
	}

	if err := service.RemoveReadingListItem(user, "feed_item", feedItem.ID); err != nil {
		t.Fatalf("remove reading list item: %v", err)
	}
	var count int64
	if err := db.Model(&model.ReadingListItem{}).Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, "feed_item", feedItem.ID).Count(&count).Error; err != nil {
		t.Fatalf("count reading list item: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected reading list item to be deleted, got %d", count)
	}
}

func TestRemoveReadingListItemReturnsNotFoundWhenItemMissing(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var feedItem model.FeedItem
	if err := db.First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}

	err := service.RemoveReadingListItem(user, "feed_item", feedItem.ID)
	if err == nil {
		t.Fatal("expected missing reading list item to return an error")
	}
	appErr := apperr.FromError(err)
	if appErr == nil || appErr.Code != "feed.reading_list_item_not_found" {
		t.Fatalf("expected reading list not found error, got %#v", err)
	}
}

func TestGetExploreFeedUsesPageAndReturnsTotal(t *testing.T) {
	service, db, user := newFeedTestService(t)
	for i := 0; i < 3; i++ {
		post := model.Post{UserID: user.ID, Title: fmt.Sprintf("Explore %d", i), Content: "body", Status: "published"}
		if err := db.Create(&post).Error; err != nil {
			t.Fatalf("create explore post: %v", err)
		}
	}
	page1, total1, err := service.GetExploreFeed(user, FeedQuery{Page: 1, PageSize: 1, Sort: "popular"})
	if err != nil {
		t.Fatalf("page 1 explore: %v", err)
	}
	page2, total2, err := service.GetExploreFeed(user, FeedQuery{Page: 2, PageSize: 1, Sort: "popular"})
	if err != nil {
		t.Fatalf("page 2 explore: %v", err)
	}
	if len(page1) != 1 || len(page2) != 1 {
		t.Fatalf("expected single item pages, got %#v and %#v", page1, page2)
	}
	if total1 <= 1 || total2 != total1 {
		t.Fatalf("expected stable total > 1, got %d and %d", total1, total2)
	}
	if page1[0].PublishedAt.Equal(page2[0].PublishedAt) && page1[0].Type == page2[0].Type {
		t.Fatalf("expected page 2 to differ from page 1, got %#v and %#v", page1, page2)
	}
}

func TestGetExploreFeedGloballyPagesAcrossSources(t *testing.T) {
	service, db, user := newFeedTestService(t)

	secondarySource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://secondary.example.com/feed.xml",
		Hash:         "secondary-rss-hash",
		Title:        "Secondary Feed",
		HealthStatus: "healthy",
	}
	if err := db.Create(&secondarySource).Error; err != nil {
		t.Fatalf("create secondary source: %v", err)
	}

	now := time.Now().UTC()
	latestFeed := model.FeedItem{
		FeedSourceID: secondarySource.ID,
		GUID:         "explore-page-feed-1",
		Title:        "Explore page feed 1",
		Link:         "https://secondary.example.com/items/1",
		PublishedAt:  now.Add(3 * time.Hour),
		FetchedAt:    now,
	}
	if err := db.Create(&latestFeed).Error; err != nil {
		t.Fatalf("create latest feed item: %v", err)
	}

	middlePost := model.Post{
		UserID:  user.ID,
		Title:   "Explore page post",
		Content: "body",
		Status:  "published",
	}
	if err := db.Create(&middlePost).Error; err != nil {
		t.Fatalf("create middle post: %v", err)
	}
	if err := db.Model(&middlePost).Update("created_at", now.Add(2*time.Hour)).Error; err != nil {
		t.Fatalf("set middle post created_at: %v", err)
	}

	olderFeed := model.FeedItem{
		FeedSourceID: secondarySource.ID,
		GUID:         "explore-page-feed-2",
		Title:        "Explore page feed 2",
		Link:         "https://secondary.example.com/items/2",
		PublishedAt:  now.Add(1 * time.Hour),
		FetchedAt:    now,
	}
	if err := db.Create(&olderFeed).Error; err != nil {
		t.Fatalf("create older feed item: %v", err)
	}

	page1, total1, err := service.GetExploreFeed(user, FeedQuery{Page: 1, PageSize: 1, Sort: "popular"})
	if err != nil {
		t.Fatalf("page 1 explore: %v", err)
	}
	page2, total2, err := service.GetExploreFeed(user, FeedQuery{Page: 2, PageSize: 1, Sort: "popular"})
	if err != nil {
		t.Fatalf("page 2 explore: %v", err)
	}

	if total1 != total2 {
		t.Fatalf("expected stable totals across pages, got %d and %d", total1, total2)
	}
	if len(page1) != 1 || len(page2) != 1 {
		t.Fatalf("expected one item per page, got %#v and %#v", page1, page2)
	}
	if page1[0].Type != "feed_item" || page1[0].FeedItem == nil || page1[0].FeedItem.ID != latestFeed.ID {
		t.Fatalf("expected page 1 to return newest feed item, got %#v", page1[0])
	}
	if page2[0].Type != "post" || page2[0].Post == nil || page2[0].Post.ID != middlePost.ID {
		t.Fatalf("expected page 2 to return the middle post, got %#v", page2[0])
	}
}

func TestGetExploreFeedReturnsFilteredTotal(t *testing.T) {
	service, db, user := newFeedTestService(t)

	matchingPost := model.Post{UserID: user.ID, Title: "Needle match", Content: "body", Status: "published"}
	if err := db.Create(&matchingPost).Error; err != nil {
		t.Fatalf("create matching post: %v", err)
	}
	otherPost := model.Post{UserID: user.ID, Title: "Something else", Content: "body", Status: "published"}
	if err := db.Create(&otherPost).Error; err != nil {
		t.Fatalf("create other post: %v", err)
	}

	items, total, err := service.GetExploreFeed(user, FeedQuery{Page: 1, PageSize: 20, Sort: "popular", Search: "needle"})
	if err != nil {
		t.Fatalf("filtered explore feed: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected filtered total 1, got %d with items %#v", total, items)
	}
	if len(items) != 1 || items[0].Post == nil || items[0].Post.ID != matchingPost.ID {
		t.Fatalf("expected only matching post, got %#v", items)
	}
}

func TestGetExploreFeedRecentSortOrdersNewestFirst(t *testing.T) {
	service, db, user := newFeedTestService(t)

	secondarySource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://recent.example.com/feed.xml",
		Hash:         "recent-rss-hash",
		Title:        "Recent Feed",
		HealthStatus: "healthy",
	}
	if err := db.Create(&secondarySource).Error; err != nil {
		t.Fatalf("create recent source: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	feedTimes := []time.Time{
		now.Add(5 * time.Hour),
		now.Add(4 * time.Hour),
		now.Add(3 * time.Hour),
		now.Add(2 * time.Hour),
		now.Add(1 * time.Hour),
	}
	for i, publishedAt := range feedTimes {
		item := model.FeedItem{
			FeedSourceID: secondarySource.ID,
			GUID:         fmt.Sprintf("recent-guid-%d", i),
			Title:        fmt.Sprintf("Recent Item %d", i),
			Link:         fmt.Sprintf("https://recent.example.com/items/%d", i),
			PublishedAt:  publishedAt,
			FetchedAt:    now,
		}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("create recent feed item %d: %v", i, err)
		}
	}

	items, _, err := service.GetExploreFeed(user, FeedQuery{Page: 1, PageSize: 5, Sort: "recent"})
	if err != nil {
		t.Fatalf("recent explore feed: %v", err)
	}
	if len(items) < 5 {
		t.Fatalf("expected at least 5 items, got %#v", items)
	}
	for i := 1; i < 5; i++ {
		if items[i-1].PublishedAt.Before(items[i].PublishedAt) {
			t.Fatalf("expected recent sort descending, got %#v", items[:5])
		}
	}
}

func TestGetExploreFeedDefaultsUnknownSortToRecent(t *testing.T) {
	service, db, user := newFeedTestService(t)

	secondarySource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://sort.example.com/feed.xml",
		Hash:         "sort-rss-hash",
		Title:        "Sort Feed",
		HealthStatus: "healthy",
	}
	if err := db.Create(&secondarySource).Error; err != nil {
		t.Fatalf("create sort source: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	older := model.FeedItem{
		FeedSourceID: secondarySource.ID,
		GUID:         "sort-guid-older",
		Title:        "Older Sort Item",
		Link:         "https://sort.example.com/items/older",
		PublishedAt:  now.Add(-2 * time.Hour),
		FetchedAt:    now,
	}
	newer := model.FeedItem{
		FeedSourceID: secondarySource.ID,
		GUID:         "sort-guid-newer",
		Title:        "Newer Sort Item",
		Link:         "https://sort.example.com/items/newer",
		PublishedAt:  now.Add(-time.Hour),
		FetchedAt:    now,
	}
	if err := db.Create(&older).Error; err != nil {
		t.Fatalf("create older sort item: %v", err)
	}
	if err := db.Create(&newer).Error; err != nil {
		t.Fatalf("create newer sort item: %v", err)
	}

	items, _, err := service.GetExploreFeed(user, FeedQuery{
		Page:     1,
		PageSize: 2,
		Sort:     "not-a-real-sort",
		Search:   "Sort Item",
	})
	if err != nil {
		t.Fatalf("unknown sort explore feed: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("expected at least two items, got %#v", items)
	}
	if items[0].Type != "feed_item" || items[0].FeedItem == nil || items[0].FeedItem.ID != newer.ID {
		t.Fatalf("expected unknown sort to behave like recent, got first item %#v", items[0])
	}
}

func TestGetExploreFeedStableOrderForEqualTimestamps(t *testing.T) {
	service, db, user := newFeedTestService(t)

	secondarySource := model.FeedSource{
		SourceType:   "external_rss",
		RssURL:       "https://stable.example.com/feed.xml",
		Hash:         "stable-rss-hash",
		Title:        "Stable Feed",
		HealthStatus: "healthy",
	}
	if err := db.Create(&secondarySource).Error; err != nil {
		t.Fatalf("create stable source: %v", err)
	}

	publishedAt := time.Now().UTC().Truncate(time.Second)
	first := model.FeedItem{
		FeedSourceID: secondarySource.ID,
		GUID:         "stable-guid-1",
		Title:        "Stable Item 1",
		Link:         "https://stable.example.com/items/1",
		PublishedAt:  publishedAt,
		FetchedAt:    publishedAt,
	}
	second := model.FeedItem{
		FeedSourceID: secondarySource.ID,
		GUID:         "stable-guid-2",
		Title:        "Stable Item 2",
		Link:         "https://stable.example.com/items/2",
		PublishedAt:  publishedAt,
		FetchedAt:    publishedAt,
	}
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first stable item: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second stable item: %v", err)
	}

	items, _, err := service.GetExploreFeed(user, FeedQuery{
		Page:     1,
		PageSize: 2,
		Sort:     "recent",
		Search:   "Stable Item",
	})
	if err != nil {
		t.Fatalf("stable recent explore feed: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("expected at least two items, got %#v", items)
	}
	if items[0].PublishedAt != items[1].PublishedAt {
		t.Fatalf("expected equal timestamps for tie case, got %#v", items[:2])
	}
	if items[0].Type != "feed_item" || items[1].Type != "feed_item" {
		t.Fatalf("expected feed items in tie case, got %#v", items[:2])
	}
	if items[0].FeedItem == nil || items[1].FeedItem == nil {
		t.Fatalf("expected feed item payloads in tie case, got %#v", items[:2])
	}
	if items[0].FeedItem.ID != second.ID || items[1].FeedItem.ID != first.ID {
		t.Fatalf("expected deterministic descending id tie-breaker, got %#v", items[:2])
	}
}

func TestGetExploreFeedAllowsAnonymousPublicRead(t *testing.T) {
	service, _, _ := newFeedTestService(t)

	items, total, err := service.GetExploreFeed(authctx.CurrentUser{}, FeedQuery{Page: 1, PageSize: 20, Sort: "popular"})
	if err != nil {
		t.Fatalf("anonymous explore feed should be public: %v", err)
	}
	if total == 0 || len(items) == 0 {
		t.Fatalf("expected anonymous explore feed to include public items, got total=%d len=%d", total, len(items))
	}
	for _, item := range items {
		if item.IsRead {
			t.Fatalf("anonymous explore items should not include user read state: %#v", item)
		}
	}
}

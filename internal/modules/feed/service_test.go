package feed

import (
	"fmt"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"gorm.io/gorm"
)

func newFeedTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
		&model.FeedItem{},
		&model.FeedItemRead{},
		&model.FeedItemStar{},
		&model.ReadingListItem{},
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

func TestListReadingListReturnsPagedItems(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var feedItem model.FeedItem
	if err := db.First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	if err := db.Create(&model.ReadingListItem{UserID: user.ID, FeedItemID: feedItem.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create reading list item: %v", err)
	}

	items, total, err := service.ListReadingList(user, FeedQuery{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list reading list: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one reading list item, got total=%d len=%d", total, len(items))
	}
	if items[0].FeedItemID != feedItem.ID {
		t.Fatalf("expected feed item id %s, got %s", feedItem.ID, items[0].FeedItemID)
	}
	if items[0].FeedItem == nil || items[0].FeedItem.Title != feedItem.Title {
		t.Fatalf("expected preloaded feed item, got %#v", items[0].FeedItem)
	}
}

func TestRemoveReadingListItemDeletesUserItem(t *testing.T) {
	service, db, user := newFeedTestService(t)
	var feedItem model.FeedItem
	if err := db.First(&feedItem).Error; err != nil {
		t.Fatalf("find feed item: %v", err)
	}
	if err := db.Create(&model.ReadingListItem{UserID: user.ID, FeedItemID: feedItem.ID, CreatedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatalf("create reading list item: %v", err)
	}

	if err := service.RemoveReadingListItem(user, feedItem.ID); err != nil {
		t.Fatalf("remove reading list item: %v", err)
	}
	var count int64
	if err := db.Model(&model.ReadingListItem{}).Where("user_id = ? AND feed_item_id = ?", user.ID, feedItem.ID).Count(&count).Error; err != nil {
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

	err := service.RemoveReadingListItem(user, feedItem.ID)
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

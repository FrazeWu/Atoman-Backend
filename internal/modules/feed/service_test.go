package feed

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
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

	rows, err := service.repo.ListExploreSources(20, 0)
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

	rows, err := service.repo.ListExploreSources(20, 0)
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

	rows, err := service.repo.ListExploreSources(20, 0)
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

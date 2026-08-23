package portal

import (
	"sync/atomic"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func migratePortalHotContentTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Post{},
		&model.ContentEntry{}, &model.ContentBlogExtension{}, &model.ContentCollectionMembership{},
		&model.Video{}, &model.Artist{}, &model.Album{}, &model.AlbumArtist{},
		&model.ForumCategory{}, &model.ForumTopic{}, &model.ForumGroup{}, &model.ForumCategoryPermission{},
		&model.Debate{}, &model.PodcastEpisode{}, &model.FeedSource{}, &model.FeedItem{}, &model.TimelineEvent{},
	)
}

func TestHotContentCachesRepeatedRequests(t *testing.T) {
	db := testdb.Open(t)
	migratePortalHotContentTables(t, db)

	var queries atomic.Int64
	if err := db.Callback().Query().Before("gorm:query").Register("portal:count_queries", func(*gorm.DB) {
		queries.Add(1)
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}

	service := NewService(db)
	if _, err := service.HotContent(4); err != nil {
		t.Fatalf("first HotContent call returned error: %v", err)
	}
	firstQueryCount := queries.Load()
	if firstQueryCount == 0 {
		t.Fatal("expected first HotContent call to query the database")
	}
	if _, err := service.HotContent(4); err != nil {
		t.Fatalf("second HotContent call returned error: %v", err)
	}
	if queries.Load() != firstQueryCount {
		t.Fatalf("expected cached response without new queries, got %d additional queries", queries.Load()-firstQueryCount)
	}
}

func TestHotContentOrdersFeaturedBlogPostsByEngagement(t *testing.T) {
	db := testdb.Open(t)
	migratePortalHotContentTables(t, db)
	testdb.Migrate(t, db, &model.Like{}, &model.DiscussionTarget{})

	userID := uuid.Must(uuid.NewV7())
	if err := db.Create(&model.User{
		UUID:     userID,
		Username: "reader",
		Email:    "reader@example.com",
		Password: "pw",
		IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	channel := model.Channel{UserID: &userID, Name: "Portal blog channel", Slug: "portal-blog-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create blog channel: %v", err)
	}
	quiet := model.Post{
		UserID:     userID,
		Title:      "Quiet note",
		Content:    "quiet content",
		Status:     "published",
		Visibility: "public",
	}
	lively := model.Post{
		UserID:     userID,
		Title:      "Lively note",
		Content:    "lively content",
		Status:     "published",
		Visibility: "public",
		Summary:    "A lively post",
		CoverURL:   "/covers/lively.jpg",
	}
	if err := db.Create(&quiet).Error; err != nil {
		t.Fatalf("create quiet post: %v", err)
	}
	if err := db.Create(&lively).Error; err != nil {
		t.Fatalf("create lively post: %v", err)
	}
	for _, post := range []model.Post{quiet, lively} {
		entry := model.ContentEntry{Base: post.Base, AuthorID: &userID, ChannelID: channel.ID, Kind: "blog", Title: post.Title, Summary: post.Summary, CoverURL: post.CoverURL, Status: post.Status, Visibility: post.Visibility}
		if err := db.Create(&entry).Error; err != nil {
			t.Fatalf("create canonical portal entry: %v", err)
		}
		if err := db.Create(&model.ContentBlogExtension{ContentID: entry.ID, Content: post.Content}).Error; err != nil {
			t.Fatalf("create canonical portal extension: %v", err)
		}
	}

	if err := db.Create(&model.Like{UserID: userID, TargetType: "post", TargetID: lively.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.DiscussionTarget{
		Kind: "blog_post", ResourceID: lively.ID, ResourceKey: lively.ID.String(), CommentCount: 1, RootCount: 1,
	}).Error; err != nil {
		t.Fatalf("create discussion target: %v", err)
	}

	response, err := NewService(db).HotContent(4)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}
	if len(response.Featured) == 0 {
		t.Fatal("expected featured items")
	}
	if response.Featured[0].Title != "Lively note" {
		t.Fatalf("expected lively post first, got %q", response.Featured[0].Title)
	}
	if response.Featured[0].Module != "blog" {
		t.Fatalf("expected blog module, got %q", response.Featured[0].Module)
	}
	if response.Featured[0].TargetPath != "/posts/post/"+lively.ID.String() {
		t.Fatalf("unexpected target path: %q", response.Featured[0].TargetPath)
	}
	if response.Featured[0].ImageURL != "/covers/lively.jpg" {
		t.Fatalf("unexpected image URL: %q", response.Featured[0].ImageURL)
	}
}

func TestHotContentReturnsEmptyResponseWhenNoContentExists(t *testing.T) {
	db := testdb.Open(t)
	migratePortalHotContentTables(t, db)

	response, err := NewService(db).HotContent(4)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}
	if len(response.Featured) != 0 {
		t.Fatalf("expected no featured items, got %d", len(response.Featured))
	}
	if len(response.Sections) != 0 {
		t.Fatalf("expected no sections, got %d", len(response.Sections))
	}
}

func TestHotContentExcludesForumTopicsFromRestrictedCategories(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.ForumCategory{},
		&model.ForumTopic{},
		&model.ForumGroup{},
		&model.ForumCategoryPermission{},
	)
	if err := db.Exec("ALTER TABLE forum_topics ADD COLUMN reply_count integer NOT NULL DEFAULT 0").Error; err != nil {
		t.Fatalf("add forum topic reply count: %v", err)
	}

	publicCategory := model.ForumCategory{Name: "Public"}
	restrictedCategory := model.ForumCategory{Name: "Restricted"}
	group := model.ForumGroup{Name: "Private readers"}
	owner := model.User{Username: "forum-owner", Email: "forum-owner@example.test", Password: "test", IsActive: true}
	for _, record := range []any{&owner, &publicCategory, &restrictedCategory, &group} {
		if err := db.Create(record).Error; err != nil {
			t.Fatalf("create forum fixture: %v", err)
		}
	}
	if err := db.Create(&model.ForumCategoryPermission{
		CategoryID: restrictedCategory.ID,
		GroupID:    group.ID,
		CanView:    true,
	}).Error; err != nil {
		t.Fatalf("create restricted category permission: %v", err)
	}

	publicTopic := model.ForumTopic{
		UserID:     owner.UUID,
		CategoryID: publicCategory.ID,
		Title:      "Public topic",
		Content:    "visible to everyone",
	}
	restrictedTopic := model.ForumTopic{
		UserID:     owner.UUID,
		CategoryID: restrictedCategory.ID,
		Title:      "Restricted topic",
		Content:    "members only",
		ViewCount:  100,
	}
	for _, topic := range []*model.ForumTopic{&publicTopic, &restrictedTopic} {
		if err := db.Create(topic).Error; err != nil {
			t.Fatalf("create forum topic: %v", err)
		}
	}

	response, err := NewService(db).HotContent(4)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}
	var forumItems []HotItem
	for _, section := range response.Sections {
		if section.Module == "forum" {
			forumItems = section.Items
			break
		}
	}
	if len(forumItems) != 1 || forumItems[0].ID != publicTopic.ID.String() {
		t.Fatalf("expected only public forum topic, got %#v", forumItems)
	}
}

func TestHotContentExcludesItemsFromUnavailableFeedSources(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})

	visibleSource := model.FeedSource{Title: "Visible feed", Hash: "portal-visible-feed"}
	hiddenSource := model.FeedSource{Title: "Hidden feed", Hash: "portal-hidden-feed", Hidden: true}
	deletedSource := model.FeedSource{Title: "Deleted feed", Hash: "portal-deleted-feed"}
	for _, source := range []*model.FeedSource{&visibleSource, &hiddenSource, &deletedSource} {
		if err := db.Create(source).Error; err != nil {
			t.Fatalf("create feed source: %v", err)
		}
	}
	if err := db.Delete(&deletedSource).Error; err != nil {
		t.Fatalf("delete feed source: %v", err)
	}

	now := time.Now().UTC()
	items := []model.FeedItem{
		{FeedSourceID: visibleSource.ID, GUID: "portal-visible-item", Title: "Visible item", PublishedAt: now, FetchedAt: now},
		{FeedSourceID: hiddenSource.ID, GUID: "portal-hidden-item", Title: "Hidden item", PublishedAt: now.Add(time.Minute), FetchedAt: now},
		{FeedSourceID: deletedSource.ID, GUID: "portal-deleted-item", Title: "Deleted item", PublishedAt: now.Add(2 * time.Minute), FetchedAt: now},
	}
	if err := db.Create(&items).Error; err != nil {
		t.Fatalf("create feed items: %v", err)
	}

	response, err := NewService(db).HotContent(4)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}
	var feedItems []HotItem
	for _, section := range response.Sections {
		if section.Module == "feed" {
			feedItems = section.Items
			break
		}
	}
	if len(feedItems) != 1 || feedItems[0].ID != items[0].ID.String() {
		t.Fatalf("expected only visible feed item, got %#v", feedItems)
	}
}

func TestHotContentExcludesNonPublicMusicAlbums(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.Album{}, &model.AlbumArtist{})

	albums := []model.Album{
		{Title: "Public album", Status: "open", HotScore: 1},
		{Title: "Draft album", Status: "draft", HotScore: 100},
		{Title: "Rejected album", Status: "rejected", HotScore: 200},
	}
	if err := db.Create(&albums).Error; err != nil {
		t.Fatalf("create albums: %v", err)
	}

	response, err := NewService(db).HotContent(4)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}
	var musicItems []HotItem
	for _, section := range response.Sections {
		if section.Module == "music" {
			musicItems = section.Items
			break
		}
	}
	if len(musicItems) != 1 || musicItems[0].ID != albums[0].ID.String() {
		t.Fatalf("expected only public album, got %#v", musicItems)
	}
}

func TestHotContentUsesReachableTargetPaths(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Post{},
		&model.ContentEntry{},
		&model.ContentBlogExtension{},
		&model.ContentCollectionMembership{},
		&model.Video{},
		&model.FeedSource{},
		&model.FeedItem{},
		&model.PodcastEpisode{},
		&model.Artist{},
		&model.Album{},
		&model.AlbumArtist{},
		&model.TimelineEvent{},
	)

	userID := uuid.Must(uuid.NewV7())
	if err := db.Create(&model.User{
		UUID:     userID,
		Username: "owner",
		Email:    "owner@example.com",
		Password: "pw",
		IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	channel := model.Channel{UserID: &userID, Name: "Portal channel", Slug: "portal-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	post := model.Post{
		UserID:     userID,
		Title:      "Podcast post",
		Content:    "content",
		Status:     "published",
		Visibility: "public",
	}
	video := model.Video{
		UserID:      userID,
		Title:       "Portal video",
		Description: "desc",
		Status:      "published",
		Visibility:  "public",
		ViewCount:   42,
	}
	feedSource := model.FeedSource{Title: "Portal feed", Hash: "portal-path-feed"}
	if err := db.Create(&feedSource).Error; err != nil {
		t.Fatalf("create feed source: %v", err)
	}
	feedItem := model.FeedItem{FeedSourceID: feedSource.ID, Title: "Portal feed item", Summary: "summary"}
	album := model.Album{
		Title:    "Portal album",
		Status:   "open",
		HotScore: 12,
	}
	timelineEvent := model.TimelineEvent{
		UserID:    userID,
		Title:     "Portal timeline event",
		EventDate: time.Now(),
		Location:  "Berlin",
		Source:    "Archive",
		IsPublic:  true,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}
	if err := db.Create(&feedItem).Error; err != nil {
		t.Fatalf("create feed item: %v", err)
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Create(&timelineEvent).Error; err != nil {
		t.Fatalf("create timeline event: %v", err)
	}

	episode := model.PodcastEpisode{
		PostID:    post.ID,
		ChannelID: channel.ID,
	}
	if err := db.Create(&episode).Error; err != nil {
		t.Fatalf("create episode: %v", err)
	}

	response, err := NewService(db).HotContent(6)
	if err != nil {
		t.Fatalf("HotContent returned error: %v", err)
	}

	pathsByModule := map[string][]string{}
	for _, item := range response.Featured {
		pathsByModule[item.Module] = append(pathsByModule[item.Module], item.TargetPath)
	}
	for _, section := range response.Sections {
		for _, item := range section.Items {
			pathsByModule[item.Module] = append(pathsByModule[item.Module], item.TargetPath)
		}
	}

	assertContainsPath(t, pathsByModule["video"], "/videos/watch/"+video.ID.String())
	assertContainsPath(t, pathsByModule["feed"], "/feed/item/"+feedItem.ID.String())
	assertContainsPath(t, pathsByModule["podcast"], "/podcasts/episode/"+episode.ID.String())
	assertContainsPath(t, pathsByModule["music"], "/music/album/"+album.ID.String())
	assertContainsPath(t, pathsByModule["timeline"], "/timeline?event="+timelineEvent.ID.String())
}

func assertContainsPath(t *testing.T, paths []string, expected string) {
	t.Helper()
	for _, path := range paths {
		if path == expected {
			return
		}
	}
	t.Fatalf("expected path %q in %v", expected, paths)
}

package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type unifiedStudioFixture struct {
	user     model.User
	channels map[string]model.Channel
	items    map[string]uuid.UUID
}

func seedUnifiedStudioFixture(t *testing.T, db *gorm.DB) unifiedStudioFixture {
	t.Helper()
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&legacyUserDefaultChannel{},
		&model.Collection{},
		&model.Post{},
		&model.PodcastEpisode{},
		&model.Video{},
	)

	user := model.User{
		Username: "studio-migration",
		Email:    "studio-migration@example.com",
		Password: "hash",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	createdAt := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	channels := map[string]model.Channel{
		model.ChannelContentTypeBlog: {
			Base: model.Base{CreatedAt: createdAt}, UserID: &user.UUID,
			Name: "Studio Blog", Slug: "studio-blog", ContentType: model.ChannelContentTypeBlog, IsDefault: true,
		},
		model.ChannelContentTypePodcast: {
			Base: model.Base{CreatedAt: createdAt.Add(time.Hour)}, UserID: &user.UUID,
			Name: "Studio Podcast", Slug: "studio-podcast", ContentType: model.ChannelContentTypePodcast, IsDefault: true,
		},
		model.ChannelContentTypeVideo: {
			Base: model.Base{CreatedAt: createdAt.Add(2 * time.Hour)}, UserID: &user.UUID,
			Name: "Studio Video", Slug: "studio-video", ContentType: model.ChannelContentTypeVideo, IsDefault: true,
		},
	}
	for contentType, channel := range channels {
		if err := db.Create(&channel).Error; err != nil {
			t.Fatalf("create %s channel: %v", contentType, err)
		}
		channels[contentType] = channel
		if err := db.Create(&legacyUserDefaultChannel{
			UserID: user.UUID, ContentType: contentType, ChannelID: channel.ID,
		}).Error; err != nil {
			t.Fatalf("create %s selection: %v", contentType, err)
		}
		collection := model.Collection{
			ChannelID: channel.ID, CreatedBy: &user.UUID, Name: "Default " + contentType, IsDefault: true,
		}
		if err := db.Create(&collection).Error; err != nil {
			t.Fatalf("create %s collection: %v", contentType, err)
		}
	}

	blogChannel := channels[model.ChannelContentTypeBlog]
	post := model.Post{
		UserID: user.UUID, ChannelID: &blogChannel.ID,
		Title: "Article", Content: "body", Status: "published", Visibility: "public",
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	podcastChannel := channels[model.ChannelContentTypePodcast]
	episodePost := model.Post{
		UserID: user.UUID, ChannelID: &podcastChannel.ID,
		Title: "Episode", Content: "shownotes", Status: "published", Visibility: "public",
	}
	if err := db.Create(&episodePost).Error; err != nil {
		t.Fatalf("create episode post: %v", err)
	}
	episode := model.PodcastEpisode{
		PostID: episodePost.ID, ChannelID: podcastChannel.ID,
		AudioURL: "https://cdn.example.com/episode.mp3",
	}
	if err := db.Create(&episode).Error; err != nil {
		t.Fatalf("create episode: %v", err)
	}

	videoChannel := channels[model.ChannelContentTypeVideo]
	video := model.Video{
		ChannelID: &videoChannel.ID, UserID: user.UUID,
		Title: "Video", StorageType: "external", VideoURL: "https://example.com/video", Status: "published", Visibility: "public",
	}
	if err := db.Create(&video).Error; err != nil {
		t.Fatalf("create video: %v", err)
	}

	return unifiedStudioFixture{
		user:     user,
		channels: channels,
		items: map[string]uuid.UUID{
			"post": post.ID, "episode": episode.ID, "video": video.ID,
		},
	}
}

func TestRunUnifiedStudioMigrationPreservesChannelsCollectionsAndContent(t *testing.T) {
	db := testdb.Open(t)
	fixture := seedUnifiedStudioFixture(t, db)

	if err := RunUnifiedStudioMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	var channelCount int64
	if err := db.Model(&model.Channel{}).Where("user_id = ?", fixture.user.UUID).Count(&channelCount).Error; err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if channelCount != 3 {
		t.Fatalf("expected three preserved channels, got %d", channelCount)
	}
	for label, id := range fixture.items {
		var count int64
		table := map[string]string{"post": "posts", "episode": "podcast_episodes", "video": "videos"}[label]
		if err := db.Table(table).Where("id = ?", id).Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", label, err)
		}
		if count != 1 {
			t.Fatalf("expected %s %s to be preserved", label, id)
		}
	}
}

func TestRunUnifiedStudioMigrationBackfillsCollectionContentType(t *testing.T) {
	db := testdb.Open(t)
	fixture := seedUnifiedStudioFixture(t, db)

	if err := RunUnifiedStudioMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	for contentType, channel := range fixture.channels {
		var collection model.Collection
		if err := db.Where("channel_id = ?", channel.ID).First(&collection).Error; err != nil {
			t.Fatalf("load %s collection: %v", contentType, err)
		}
		if collection.ContentType != contentType {
			t.Fatalf("expected %s collection type, got %q", contentType, collection.ContentType)
		}
	}
}

func TestRunUnifiedStudioMigrationSelectsOneCurrentChannelPerUser(t *testing.T) {
	db := testdb.Open(t)
	fixture := seedUnifiedStudioFixture(t, db)

	if err := RunUnifiedStudioMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}

	var state model.UserStudioState
	if err := db.First(&state, "user_id = ?", fixture.user.UUID).Error; err != nil {
		t.Fatalf("load studio state: %v", err)
	}
	blogChannel := fixture.channels[model.ChannelContentTypeBlog]
	if state.ChannelID == nil || *state.ChannelID != blogChannel.ID {
		t.Fatalf("expected blog channel %s, got %#v", blogChannel.ID, state.ChannelID)
	}
}

func TestRunUnifiedStudioMigrationDoesNotMergeChannelsWithSameOwner(t *testing.T) {
	db := testdb.Open(t)
	fixture := seedUnifiedStudioFixture(t, db)
	originalIDs := map[uuid.UUID]struct{}{}
	for _, channel := range fixture.channels {
		originalIDs[channel.ID] = struct{}{}
	}

	if err := RunUnifiedStudioMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := RunUnifiedStudioMigration(db); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}

	var channels []model.Channel
	if err := db.Where("user_id = ?", fixture.user.UUID).Find(&channels).Error; err != nil {
		t.Fatalf("load channels: %v", err)
	}
	if len(channels) != len(originalIDs) {
		t.Fatalf("expected %d channels, got %d", len(originalIDs), len(channels))
	}
	for _, channel := range channels {
		if _, ok := originalIDs[channel.ID]; !ok {
			t.Fatalf("unexpected merged or replacement channel %s", channel.ID)
		}
	}
}

func TestRunUnifiedStudioMigrationDropsLegacyDefaultChannelSelections(t *testing.T) {
	db := testdb.Open(t)
	seedUnifiedStudioFixture(t, db)

	if err := RunUnifiedStudioMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if db.Migrator().HasTable("user_default_channels") {
		t.Fatal("expected legacy user_default_channels table to be dropped")
	}
}

func TestRunUnifiedStudioMigrationReplacesSingleDefaultCollectionIndex(t *testing.T) {
	db := testdb.Open(t)
	fixture := seedUnifiedStudioFixture(t, db)
	if err := db.Exec(`CREATE UNIQUE INDEX idx_collections_channel_default
		ON collections (channel_id) WHERE is_default = true`).Error; err != nil {
		t.Fatalf("create legacy default collection index: %v", err)
	}

	if err := RunUnifiedStudioMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	channel := fixture.channels[model.ChannelContentTypeBlog]
	for _, contentType := range []string{model.ChannelContentTypePodcast, model.ChannelContentTypeVideo} {
		collection := model.Collection{
			ChannelID: channel.ID, ContentType: contentType,
			Name: "Default " + contentType, IsDefault: true,
		}
		if err := db.Create(&collection).Error; err != nil {
			t.Fatalf("create %s default collection on unified channel: %v", contentType, err)
		}
	}
}

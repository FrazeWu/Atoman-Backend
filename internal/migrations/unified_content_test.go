package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEnsurePostContentEntryReusesSoftDeletedEntry(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Post{}, &model.ContentEntry{}, &model.ContentPostExtension{})

	owner := model.User{Username: "unified-content-soft-delete-owner", Email: "unified-content-soft-delete@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Soft deleted content", Slug: "soft-deleted-content"}
	require.NoError(t, db.Create(&channel).Error)
	post := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Deleted article", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)

	entry := model.ContentEntry{AuthorID: &owner.UUID, ChannelID: channel.ID, Kind: "blog", Title: post.Title, Status: post.Status, Visibility: post.Visibility}
	require.NoError(t, db.Create(&entry).Error)
	require.NoError(t, db.Delete(&entry).Error)
	require.NoError(t, db.Create(&model.ContentPostExtension{ContentID: entry.ID, PostID: post.ID}).Error)
	require.NoError(t, db.Delete(&post).Error)

	result, err := ensurePostContentEntry(db, post, "blog")
	require.NoError(t, err)
	require.Equal(t, entry.ID, result.ID)
	require.True(t, result.DeletedAt.Valid)

	var visible model.ContentEntry
	require.ErrorIs(t, db.First(&visible, "id = ?", entry.ID).Error, gorm.ErrRecordNotFound)
}

func TestRunUnifiedContentMigrationBackfillsMixedContentAndCollections(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.ContentEntry{}, &model.ContentPostExtension{}, &model.ContentBlogExtension{}, &model.ContentBlogVersion{}, &model.ContentBlogDraft{}, &model.ContentEpisodeExtension{}, &model.ContentVideoExtension{}, &model.ContentCollection{}, &model.ContentCollectionMembership{},
		&model.Post{}, &model.PostCollection{}, &model.BlogPostVersion{}, &model.BlogDraft{},
		&model.PodcastEpisode{}, &model.Video{}, &model.VideoCollection{}, &model.StudioModuleSettings{},
	)
	owner := model.User{Username: "unified-content-owner", Email: "unified-content-owner@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Unified content", Slug: "unified-content"}
	require.NoError(t, db.Create(&channel).Error)

	blogDefault := model.Collection{ChannelID: channel.ID, ContentType: "blog", CreatedBy: &owner.UUID, Name: "未分类文章", IsDefault: true}
	podcastDefault := model.Collection{ChannelID: channel.ID, ContentType: "podcast", CreatedBy: &owner.UUID, Name: "未分类单集", IsDefault: true}
	videoDefault := model.Collection{ChannelID: channel.ID, ContentType: "video", CreatedBy: &owner.UUID, Name: "未分类视频", IsDefault: true}
	for _, collection := range []*model.Collection{&blogDefault, &podcastDefault, &videoDefault} {
		require.NoError(t, db.Create(collection).Error)
	}
	customCollection := model.Collection{ChannelID: channel.ID, ContentType: "blog", CreatedBy: &owner.UUID, Name: "我的合集"}
	require.NoError(t, db.Create(&customCollection).Error)
	settings := model.StudioModuleSettings{UserID: owner.UUID, ChannelID: channel.ID, ContentType: "blog", DefaultCollectionID: &blogDefault.ID}
	require.NoError(t, db.Create(&settings).Error)

	post := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Article", Content: "body", Status: "published", Visibility: "public"}
	episodePost := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Episode", Content: "notes", Status: "published", Visibility: "public"}
	video := model.Video{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Video", VideoURL: "video.mp4", StorageType: "external", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)
	require.NoError(t, db.Create(&episodePost).Error)
	require.NoError(t, db.Create(&video).Error)
	orphanContentID := uuid.New()
	require.NoError(t, db.Create(&model.ContentPostExtension{ContentID: orphanContentID, PostID: post.ID}).Error)
	episode := model.PodcastEpisode{PostID: episodePost.ID, ChannelID: channel.ID, AudioURL: "episode.mp3"}
	require.NoError(t, db.Create(&episode).Error)
	require.NoError(t, db.Create(&model.PostCollection{PostID: post.ID, CollectionID: blogDefault.ID, Position: 2}).Error)
	require.NoError(t, db.Create(&model.PostCollection{PostID: episodePost.ID, CollectionID: podcastDefault.ID, Position: 1}).Error)
	require.NoError(t, db.Create(&model.VideoCollection{VideoID: video.ID, CollectionID: videoDefault.ID}).Error)

	require.NoError(t, db.Create(&model.BlogPostVersion{
		PostID: post.ID, Version: 1, EditorID: owner.UUID, Title: post.Title, Content: post.Content,
		Visibility: post.Visibility, CollectionID: blogDefault.ID,
	}).Error)
	require.NoError(t, db.Create(&model.BlogDraft{
		UserID: owner.UUID, ContextKey: "editor:article", SourcePostID: &post.ID,
		Title: post.Title, Content: post.Content, Visibility: post.Visibility,
		ChannelID: &channel.ID, CollectionID: &blogDefault.ID,
	}).Error)

	require.NoError(t, RunResourceManagementMigration(db))
	renamedDefault := model.Collection{ChannelID: channel.ID, ContentType: "blog", CreatedBy: &owner.UUID, Name: "我的旧合集", IsDefault: true}
	require.NoError(t, db.Create(&renamedDefault).Error)
	require.NoError(t, RunUnifiedContentMigration(db))
	var activeLegacyDefaults int64
	require.NoError(t, db.Model(&model.Collection{}).
		Where("channel_id = ? AND is_default = ?", channel.ID, true).
		Count(&activeLegacyDefaults).Error)
	require.Equal(t, int64(1), activeLegacyDefaults)
	var deletedLegacyDefaults []model.Collection
	require.NoError(t, db.Unscoped().Where("channel_id = ? AND is_default = ?", channel.ID, true).Find(&deletedLegacyDefaults).Error)
	require.Len(t, deletedLegacyDefaults, 4)
	var persistedCustom model.Collection
	require.NoError(t, db.First(&persistedCustom, "id = ?", customCollection.ID).Error)
	require.Equal(t, customCollection.Name, persistedCustom.Name)
	var defaults []model.ContentCollection
	require.NoError(t, db.Where("channel_id = ? AND is_default = ?", channel.ID, true).Find(&defaults).Error)
	require.Len(t, defaults, 1)
	var migratedSettings model.StudioModuleSettings
	require.NoError(t, db.First(&migratedSettings, "id = ?", settings.ID).Error)
	require.NotNil(t, migratedSettings.DefaultCollectionID)
	require.Equal(t, defaults[0].ID, *migratedSettings.DefaultCollectionID)

	require.NoError(t, db.Model(&model.Post{}).Where("id = ?", post.ID).Updates(map[string]any{
		"content":    "updated body",
		"view_count": 7,
	}).Error)
	require.NoError(t, RunUnifiedContentMigration(db))

	var restoredEntry model.ContentEntry
	require.NoError(t, db.Where("kind = ? AND title = ?", "blog", post.Title).First(&restoredEntry).Error)
	require.NoError(t, db.Delete(&restoredEntry).Error)
	require.NoError(t, RunUnifiedContentMigration(db))
	require.NoError(t, db.Where("id = ?", restoredEntry.ID).First(&restoredEntry).Error)

	var entries []model.ContentEntry
	require.NoError(t, db.Where("channel_id = ?", channel.ID).Find(&entries).Error)
	require.Len(t, entries, 3)
	require.NoError(t, db.Where("channel_id = ? AND is_default = ?", channel.ID, true).Find(&defaults).Error)
	require.Len(t, defaults, 1)
	var mappings []model.LegacyCollectionMapping
	require.NoError(t, db.Find(&mappings).Error)
	require.Len(t, mappings, 5)
	mappingByLegacyID := make(map[uuid.UUID]uuid.UUID, len(mappings))
	for _, mapping := range mappings {
		mappingByLegacyID[mapping.LegacyCollectionID] = mapping.ContentCollectionID
	}
	for _, legacyID := range []uuid.UUID{blogDefault.ID, podcastDefault.ID, videoDefault.ID, renamedDefault.ID} {
		require.Equal(t, defaults[0].ID, mappingByLegacyID[legacyID])
	}
	require.NotEqual(t, defaults[0].ID, mappingByLegacyID[customCollection.ID])
	var memberships []model.ContentCollectionMembership
	require.NoError(t, db.Where("collection_id = ?", defaults[0].ID).Find(&memberships).Error)
	require.Len(t, memberships, 3)
	var blogEntries []model.ContentEntry
	require.NoError(t, db.Where("kind = ?", "blog").Find(&blogEntries).Error)
	require.Len(t, blogEntries, 1)
	require.NotNil(t, blogEntries[0].AuthorID)
	require.Equal(t, owner.UUID, *blogEntries[0].AuthorID)
	var blogExtensions []model.ContentBlogExtension
	require.NoError(t, db.Find(&blogExtensions).Error)
	require.Len(t, blogExtensions, 1)
	require.Equal(t, "updated body", blogExtensions[0].Content)
	require.Equal(t, int64(7), blogExtensions[0].ViewCount)
	var postExtensions []model.ContentPostExtension
	var episodeExtensions []model.ContentEpisodeExtension
	var videoExtensions []model.ContentVideoExtension
	require.NoError(t, db.Find(&postExtensions).Error)
	require.NoError(t, db.Find(&episodeExtensions).Error)
	require.NoError(t, db.Find(&videoExtensions).Error)
	require.Len(t, postExtensions, 1)
	require.Len(t, episodeExtensions, 1)
	require.Len(t, videoExtensions, 1)
	require.Equal(t, video.VideoURL, videoExtensions[0].VideoURL)

	var blogVersions []model.ContentBlogVersion
	require.NoError(t, db.Find(&blogVersions).Error)
	require.Len(t, blogVersions, 1)
	require.Equal(t, blogEntries[0].ID, blogVersions[0].ContentID)
	require.Equal(t, defaults[0].ID, blogVersions[0].CollectionID)

	var blogDrafts []model.ContentBlogDraft
	require.NoError(t, db.Find(&blogDrafts).Error)
	require.Len(t, blogDrafts, 1)
	require.NotNil(t, blogDrafts[0].ContentID)
	require.Equal(t, blogEntries[0].ID, *blogDrafts[0].ContentID)
	require.Equal(t, defaults[0].ID, *blogDrafts[0].CollectionID)

	require.NoError(t, db.Model(&model.ContentVideoExtension{}).
		Where("video_id = ?", video.ID).
		Update("video_url", "").Error)
	require.NoError(t, RunUnifiedContentMigrationIfReady(db))
	require.NoError(t, db.Where("video_id = ?", video.ID).First(&videoExtensions[0]).Error)
	require.Equal(t, video.VideoURL, videoExtensions[0].VideoURL)
}

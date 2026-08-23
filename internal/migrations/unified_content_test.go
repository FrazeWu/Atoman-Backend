package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/stretchr/testify/require"
)

func TestRunUnifiedContentMigrationBackfillsMixedContentAndCollections(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.Post{}, &model.PostCollection{}, &model.BlogPostVersion{}, &model.BlogDraft{},
		&model.PodcastEpisode{}, &model.Video{}, &model.VideoCollection{},
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

	post := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Article", Content: "body", Status: "published", Visibility: "public"}
	episodePost := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Episode", Content: "notes", Status: "published", Visibility: "public"}
	video := model.Video{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Video", VideoURL: "video.mp4", StorageType: "external", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&post).Error)
	require.NoError(t, db.Create(&episodePost).Error)
	require.NoError(t, db.Create(&video).Error)
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

	require.NoError(t, RunUnifiedContentMigration(db))
	require.NoError(t, db.Model(&model.Post{}).Where("id = ?", post.ID).Updates(map[string]any{
		"content":    "updated body",
		"view_count": 7,
	}).Error)
	require.NoError(t, RunUnifiedContentMigration(db))

	var entries []model.ContentEntry
	require.NoError(t, db.Where("channel_id = ?", channel.ID).Find(&entries).Error)
	require.Len(t, entries, 3)
	var defaults []model.ContentCollection
	require.NoError(t, db.Where("channel_id = ? AND is_default = ?", channel.ID, true).Find(&defaults).Error)
	require.Len(t, defaults, 1)
	var mappings []model.LegacyCollectionMapping
	require.NoError(t, db.Find(&mappings).Error)
	require.Len(t, mappings, 3)
	for _, mapping := range mappings {
		require.Equal(t, defaults[0].ID, mapping.ContentCollectionID)
	}
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
}

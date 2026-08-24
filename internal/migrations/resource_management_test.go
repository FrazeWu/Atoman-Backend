package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestResourceManagementMigrationBuildsDefaultsAndScalarAssignments(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.ContentCollection{}, &model.StudioModuleSettings{},
		&model.Post{}, &model.PostCollection{}, &model.PodcastEpisode{},
		&model.Video{}, &model.VideoCollection{},
	)
	owner := model.User{Username: "resource-owner", Email: "resource-owner@example.com", Password: "hash", IsActive: true}
	other := model.User{Username: "resource-other", Email: "resource-other@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	require.NoError(t, db.Create(&other).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Resource Channel", Slug: "resource-channel"}
	require.NoError(t, db.Create(&channel).Error)

	podcastA := model.Collection{ChannelID: channel.ID, ContentType: "podcast", CreatedBy: &owner.UUID, Name: "Podcast A"}
	podcastB := model.Collection{ChannelID: channel.ID, ContentType: "podcast", CreatedBy: &owner.UUID, Name: "Podcast B"}
	videoA := model.Collection{ChannelID: channel.ID, ContentType: "video", CreatedBy: &owner.UUID, Name: "Video A"}
	videoB := model.Collection{ChannelID: channel.ID, ContentType: "video", CreatedBy: &owner.UUID, Name: "Video B"}
	for _, collection := range []*model.Collection{&podcastA, &podcastB, &videoA, &videoB} {
		require.NoError(t, db.Create(collection).Error)
	}

	singlePost := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Single", Content: "shownotes", Status: "published", Visibility: "public"}
	conflictPost := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Conflict", Content: "shownotes", Status: "published", Visibility: "public"}
	missingPost := model.Post{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Missing", Content: "shownotes", Status: "published", Visibility: "public"}
	for _, post := range []*model.Post{&singlePost, &conflictPost, &missingPost} {
		require.NoError(t, db.Create(post).Error)
		require.NoError(t, db.Create(&model.PodcastEpisode{PostID: post.ID, ChannelID: channel.ID, AudioURL: "episode.mp3"}).Error)
	}
	require.NoError(t, db.Create(&model.PostCollection{PostID: singlePost.ID, CollectionID: podcastA.ID}).Error)
	require.NoError(t, db.Create(&model.PostCollection{PostID: conflictPost.ID, CollectionID: podcastA.ID}).Error)
	require.NoError(t, db.Create(&model.PostCollection{PostID: conflictPost.ID, CollectionID: podcastB.ID}).Error)

	singleVideo := model.Video{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Single video", VideoURL: "video.mp4", StorageType: "external", Status: "published", Visibility: "public"}
	conflictVideo := model.Video{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Conflict video", VideoURL: "video.mp4", StorageType: "external", Status: "published", Visibility: "public"}
	missingVideo := model.Video{UserID: owner.UUID, ChannelID: &channel.ID, Title: "Missing video", VideoURL: "video.mp4", StorageType: "external", Status: "published", Visibility: "public"}
	require.NoError(t, db.Create(&singleVideo).Error)
	require.NoError(t, db.Create(&conflictVideo).Error)
	require.NoError(t, db.Create(&missingVideo).Error)
	require.NoError(t, db.Create(&model.VideoCollection{VideoID: singleVideo.ID, CollectionID: videoA.ID}).Error)
	require.NoError(t, db.Create(&model.VideoCollection{VideoID: conflictVideo.ID, CollectionID: videoA.ID}).Error)
	require.NoError(t, db.Create(&model.VideoCollection{VideoID: conflictVideo.ID, CollectionID: videoB.ID}).Error)

	ownerSettings := model.StudioModuleSettings{UserID: owner.UUID, ChannelID: channel.ID, ContentType: "blog", DefaultVisibility: "followers", DefaultPublishStatus: "draft"}
	otherSettings := model.StudioModuleSettings{UserID: other.UUID, ChannelID: channel.ID, ContentType: "blog", DefaultVisibility: "private", DefaultPublishStatus: "published"}
	require.NoError(t, db.Create(&otherSettings).Error)
	require.NoError(t, db.Create(&ownerSettings).Error)

	require.NoError(t, RunResourceManagementMigration(db))
	require.NoError(t, RunResourceManagementMigration(db))

	var defaultCount int64
	require.NoError(t, db.Model(&model.Collection{}).Where("channel_id = ? AND is_default = ?", channel.ID, true).Count(&defaultCount).Error)
	var unifiedDefaults int64
	require.NoError(t, db.Model(&model.ContentCollection{}).Where("channel_id = ? AND is_default = ?", channel.ID, true).Count(&unifiedDefaults).Error)
	require.Equal(t, int64(3), defaultCount)
	require.Equal(t, int64(1), unifiedDefaults)

	require.NoError(t, db.First(&singlePost, "id = ?", singlePost.ID).Error)
	require.NotNil(t, singlePost.CollectionID)
	require.Equal(t, podcastA.ID, *singlePost.CollectionID)
	require.False(t, singlePost.CollectionConflict)
	require.NoError(t, db.First(&conflictPost, "id = ?", conflictPost.ID).Error)
	require.Nil(t, conflictPost.CollectionID)
	require.True(t, conflictPost.CollectionConflict)
	require.NoError(t, db.First(&missingPost, "id = ?", missingPost.ID).Error)
	require.NotNil(t, missingPost.CollectionID)
	var missingPostLinks []model.PostCollection
	require.NoError(t, db.Where("post_id = ?", missingPost.ID).Find(&missingPostLinks).Error)
	require.Len(t, missingPostLinks, 1)
	require.Equal(t, *missingPost.CollectionID, missingPostLinks[0].CollectionID)

	require.NoError(t, db.First(&singleVideo, "id = ?", singleVideo.ID).Error)
	require.NotNil(t, singleVideo.CollectionID)
	require.Equal(t, videoA.ID, *singleVideo.CollectionID)
	require.NoError(t, db.First(&conflictVideo, "id = ?", conflictVideo.ID).Error)
	require.Nil(t, conflictVideo.CollectionID)
	require.True(t, conflictVideo.CollectionConflict)
	require.NoError(t, db.First(&missingVideo, "id = ?", missingVideo.ID).Error)
	require.NotNil(t, missingVideo.CollectionID)
	var missingVideoLinks []model.VideoCollection
	require.NoError(t, db.Where("video_id = ?", missingVideo.ID).Find(&missingVideoLinks).Error)
	require.Len(t, missingVideoLinks, 1)
	require.Equal(t, *missingVideo.CollectionID, missingVideoLinks[0].CollectionID)

	var settings []model.StudioModuleSettings
	require.NoError(t, db.Where("channel_id = ? AND content_type = ?", channel.ID, "blog").Find(&settings).Error)
	require.Len(t, settings, 1)
	require.Equal(t, owner.UUID, settings[0].UserID)
	require.Equal(t, "followers", settings[0].DefaultVisibility)
}

func TestResourceManagementMigrationProtectsDefaultAndReusesDeletedNames(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Channel{}, &model.Collection{}, &model.ContentCollection{}, &model.StudioModuleSettings{},
		&model.Post{}, &model.PostCollection{}, &model.PodcastEpisode{},
		&model.Video{}, &model.VideoCollection{},
	)
	ownerID := uuid.New()
	owner := model.User{UUID: ownerID, Username: "resource-constraints", Email: "resource-constraints@example.com", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&owner).Error)
	channel := model.Channel{UserID: &owner.UUID, Name: "Case Name", Slug: "case-name"}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, RunResourceManagementMigration(db))

	var collection model.ContentCollection
	require.NoError(t, db.Where("channel_id = ? AND is_default = ?", channel.ID, true).First(&collection).Error)
	require.Equal(t, "默认合集", collection.Name)
	require.Error(t, db.Delete(&collection).Error)
	require.Error(t, db.Model(&collection).Update("is_default", false).Error)
	require.Error(t, db.Create(&model.Channel{UserID: &owner.UUID, Name: "case name", Slug: "case-name-2"}).Error)

	normal := model.Collection{ChannelID: channel.ID, ContentType: "blog", CreatedBy: &owner.UUID, Name: "Reusable"}
	require.NoError(t, db.Create(&normal).Error)
	require.NoError(t, db.Delete(&normal).Error)
	require.NoError(t, db.Create(&model.Collection{ChannelID: channel.ID, ContentType: "blog", CreatedBy: &owner.UUID, Name: "reusable"}).Error)

	require.NoError(t, db.Delete(&channel).Error)
	require.NoError(t, db.Delete(&collection).Error)
}

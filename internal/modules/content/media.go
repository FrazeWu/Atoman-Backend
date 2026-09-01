package content

import (
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type podcastRow struct {
	ContentID          uuid.UUID  `gorm:"column:content_id"`
	EpisodeID          uuid.UUID  `gorm:"column:episode_id"`
	LegacyPostID       uuid.UUID  `gorm:"column:legacy_post_id"`
	EpisodeCreatedAt   modelTime  `gorm:"column:episode_created_at"`
	EpisodeUpdatedAt   modelTime  `gorm:"column:episode_updated_at"`
	AuthorID           uuid.UUID  `gorm:"column:author_id"`
	ChannelID          uuid.UUID  `gorm:"column:channel_id"`
	EntryCreatedAt     modelTime  `gorm:"column:entry_created_at"`
	EntryUpdatedAt     modelTime  `gorm:"column:entry_updated_at"`
	Title              string     `gorm:"column:title"`
	Summary            string     `gorm:"column:summary"`
	Status             string     `gorm:"column:status"`
	Visibility         string     `gorm:"column:visibility"`
	PublishedAt        *modelTime `gorm:"column:published_at"`
	ScheduledAt        *modelTime `gorm:"column:scheduled_at"`
	Shownotes          string     `gorm:"column:shownotes"`
	AudioURL           string     `gorm:"column:audio_url"`
	DurationSec        int        `gorm:"column:duration_sec"`
	EpisodeCoverURL    string     `gorm:"column:episode_cover_url"`
	SeasonNumber       int        `gorm:"column:season_number"`
	EpisodeNumber      int        `gorm:"column:episode_number"`
	ViewCount          int64      `gorm:"column:view_count"`
	CollectionConflict bool       `gorm:"column:collection_conflict"`
}

type videoRow struct {
	ContentID          uuid.UUID  `gorm:"column:content_id"`
	VideoID            uuid.UUID  `gorm:"column:video_id"`
	VideoCreatedAt     modelTime  `gorm:"column:video_created_at"`
	VideoUpdatedAt     modelTime  `gorm:"column:video_updated_at"`
	AuthorID           uuid.UUID  `gorm:"column:author_id"`
	ChannelID          *uuid.UUID `gorm:"column:channel_id"`
	EntryCreatedAt     modelTime  `gorm:"column:entry_created_at"`
	EntryUpdatedAt     modelTime  `gorm:"column:entry_updated_at"`
	Title              string     `gorm:"column:title"`
	Summary            string     `gorm:"column:summary"`
	Status             string     `gorm:"column:status"`
	Visibility         string     `gorm:"column:visibility"`
	PublishedAt        *modelTime `gorm:"column:published_at"`
	ScheduledAt        *modelTime `gorm:"column:scheduled_at"`
	StorageType        string     `gorm:"column:storage_type"`
	VideoURL           string     `gorm:"column:video_url"`
	ThumbnailURL       string     `gorm:"column:thumbnail_url"`
	DurationSec        int        `gorm:"column:duration_sec"`
	ProcessingStatus   string     `gorm:"column:processing_status"`
	ProcessingError    string     `gorm:"column:processing_error"`
	PreviewThumbnails  []byte     `gorm:"column:preview_thumbnails"`
	ViewCount          int        `gorm:"column:view_count"`
	CollectionConflict bool       `gorm:"column:collection_conflict"`
}

// modelTime keeps nullable SQL timestamps scannable without exposing database types to callers.
type modelTime = time.Time

func PodcastQuery(db *gorm.DB) *gorm.DB {
	capabilities := CurrentMediaSchema(db)
	if !capabilities.ContentEntryTable || !capabilities.ContentEpisodeExtensionTable {
		return legacyPodcastQuery(db)
	}

	query := db.Table("content_entries AS posts").
		Joins("JOIN content_episode_extensions AS episodes ON episodes.content_id = posts.id")
	authorColumn := "posts.author_id"
	legacyPostIDColumn := "episodes.legacy_post_id"
	if !capabilities.ContentEpisodeExtensionLegacyID {
		legacyPostIDColumn = "posts.id"
	}
	if !capabilities.ContentEntryAuthor {
		query = query.Joins("LEFT JOIN posts AS legacy_posts ON legacy_posts.id = " + legacyPostIDColumn + " AND legacy_posts.deleted_at IS NULL")
		authorColumn = "legacy_posts.user_id"
	}
	shownotesColumn := "episodes.shownotes"
	if !capabilities.ContentEpisodeExtensionData {
		shownotesColumn = "posts.content"
	}
	return query.
		Select(`posts.id AS content_id, episodes.episode_id, `+legacyPostIDColumn+` AS legacy_post_id,
			`+authorColumn+` AS author_id, posts.channel_id, posts.created_at AS entry_created_at,
			posts.updated_at AS entry_updated_at, posts.title, posts.summary,
			posts.status, posts.visibility, posts.published_at, posts.scheduled_at,
			episodes.created_at AS episode_created_at, episodes.updated_at AS episode_updated_at,
			`+shownotesColumn+` AS shownotes, episodes.audio_url, episodes.duration_sec,
			episodes.episode_cover_url, episodes.season_number, episodes.episode_number,
			episodes.view_count, episodes.collection_conflict`).
		Where("posts.kind = ? AND posts.deleted_at IS NULL", "podcast")
}

func legacyPodcastQuery(db *gorm.DB) *gorm.DB {
	return db.Table("posts AS posts").
		Joins(`JOIN (
			SELECT id, id AS episode_id, post_id, created_at, updated_at, audio_url,
				duration_sec, episode_cover_url, season_number, episode_number
			FROM podcast_episodes
			WHERE deleted_at IS NULL
		) AS episodes ON episodes.post_id = posts.id`).
		Select(`posts.id AS content_id, episodes.episode_id AS episode_id, posts.id AS legacy_post_id,
			posts.user_id AS author_id, posts.channel_id, posts.created_at AS entry_created_at,
			posts.updated_at AS entry_updated_at, posts.title, posts.summary,
			posts.status, posts.visibility, posts.published_at, posts.scheduled_at,
			episodes.created_at AS episode_created_at, episodes.updated_at AS episode_updated_at,
			posts.content AS shownotes, episodes.audio_url, episodes.duration_sec,
			episodes.episode_cover_url, episodes.season_number, episodes.episode_number,
			posts.view_count, posts.collection_conflict`).
		Where("posts.deleted_at IS NULL")
}

func VideoQuery(db *gorm.DB) *gorm.DB {
	capabilities := CurrentMediaSchema(db)
	if !capabilities.ContentEntryTable || !capabilities.ContentVideoExtensionTable || !capabilities.ContentVideoExtensionMedia {
		return db.Table("(SELECT videos.*, videos.user_id AS author_id FROM videos) AS posts").
			Joins(`JOIN (
				SELECT id, id AS video_id, created_at, updated_at, storage_type, video_url,
					thumbnail_url, duration_sec, processing_status, processing_error,
					preview_thumbnails, view_count
				FROM videos
			) AS videos ON videos.id = posts.id`).
			Select(`posts.id AS content_id, videos.video_id AS video_id, posts.author_id AS author_id, posts.channel_id,
				posts.created_at AS entry_created_at, posts.updated_at AS entry_updated_at,
				videos.created_at AS video_created_at, videos.updated_at AS video_updated_at,
				posts.title, posts.description AS summary, posts.status, posts.visibility,
				posts.published_at, posts.scheduled_at, videos.storage_type, videos.video_url,
				videos.thumbnail_url, videos.duration_sec, videos.processing_status,
				videos.processing_error, videos.preview_thumbnails, videos.view_count,
				0 AS collection_conflict`).
			Where("posts.deleted_at IS NULL")
	}

	query := db.Table("content_entries AS posts").
		Joins("JOIN content_video_extensions AS videos ON videos.content_id = posts.id")
	authorColumn := "posts.author_id"
	if !capabilities.ContentEntryAuthor {
		query = query.Joins("LEFT JOIN videos AS legacy_videos ON legacy_videos.id = videos.video_id")
		authorColumn = "legacy_videos.user_id"
	}
	return query.
		Select(`posts.id AS content_id, videos.video_id, `+authorColumn+` AS author_id, posts.channel_id,
			posts.created_at AS entry_created_at, posts.updated_at AS entry_updated_at,
			videos.created_at AS video_created_at, videos.updated_at AS video_updated_at,
			posts.title, posts.summary, posts.status, posts.visibility,
			posts.published_at, posts.scheduled_at, videos.storage_type, videos.video_url,
			videos.thumbnail_url, videos.duration_sec, videos.processing_status,
			videos.processing_error, videos.preview_thumbnails, videos.view_count,
			videos.collection_conflict`).
		Where("posts.kind = ? AND posts.deleted_at IS NULL", "video")
}

func LoadPodcastEpisodes(db *gorm.DB, query *gorm.DB) ([]model.PodcastEpisode, error) {
	var rows []podcastRow
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return hydratePodcastEpisodes(db, rows)
}

func PodcastContentID(db *gorm.DB, episodeID uuid.UUID) (uuid.UUID, error) {
	var row struct {
		ContentID uuid.UUID `gorm:"column:content_id"`
	}
	if !CurrentMediaSchema(db).ContentEpisodeExtensionTable {
		if err := db.Table("podcast_episodes").Select("post_id AS content_id").Where("id = ?", episodeID).First(&row).Error; err != nil {
			return uuid.Nil, err
		}
		return row.ContentID, nil
	}
	if err := db.Table("content_episode_extensions").Select("content_id").Where("episode_id = ?", episodeID).First(&row).Error; err != nil {
		return uuid.Nil, err
	}
	return row.ContentID, nil
}

func VideoContentID(db *gorm.DB, videoID uuid.UUID) (uuid.UUID, error) {
	var row struct {
		ContentID uuid.UUID `gorm:"column:content_id"`
	}
	if !CurrentMediaSchema(db).ContentVideoExtensionTable {
		if err := db.Table("videos").Select("id AS content_id").Where("id = ?", videoID).First(&row).Error; err != nil {
			return uuid.Nil, err
		}
		return row.ContentID, nil
	}
	if err := db.Table("content_video_extensions").Select("content_id").Where("video_id = ?", videoID).First(&row).Error; err != nil {
		return uuid.Nil, err
	}
	return row.ContentID, nil
}

func LoadPodcastEpisode(db *gorm.DB, query *gorm.DB) (model.PodcastEpisode, error) {
	episodes, err := LoadPodcastEpisodes(db, query.Limit(1))
	if err != nil {
		return model.PodcastEpisode{}, err
	}
	if len(episodes) == 0 {
		return model.PodcastEpisode{}, gorm.ErrRecordNotFound
	}
	return episodes[0], nil
}

func LoadVideos(db *gorm.DB, query *gorm.DB) ([]model.Video, error) {
	var rows []videoRow
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return hydrateVideos(db, rows)
}

func LoadVideo(db *gorm.DB, query *gorm.DB) (model.Video, error) {
	videos, err := LoadVideos(db, query.Limit(1))
	if err != nil {
		return model.Video{}, err
	}
	if len(videos) == 0 {
		return model.Video{}, gorm.ErrRecordNotFound
	}
	return videos[0], nil
}

type membershipRow struct {
	ContentID    uuid.UUID  `gorm:"column:content_id"`
	CollectionID uuid.UUID  `gorm:"column:collection_id"`
	Position     int        `gorm:"column:position"`
	Name         string     `gorm:"column:name"`
	Description  string     `gorm:"column:description"`
	CoverURL     string     `gorm:"column:cover_url"`
	ChannelID    uuid.UUID  `gorm:"column:channel_id"`
	CreatedBy    *uuid.UUID `gorm:"column:created_by"`
	IsDefault    bool       `gorm:"column:is_default"`
}

func loadMemberships(db *gorm.DB, contentIDs []uuid.UUID, contentType string) (map[uuid.UUID][]model.Collection, map[uuid.UUID]int, error) {
	collectionsByContent := make(map[uuid.UUID][]model.Collection)
	positions := make(map[uuid.UUID]int)
	if len(contentIDs) == 0 {
		return collectionsByContent, positions, nil
	}
	var rows []membershipRow
	capabilities := CurrentMediaSchema(db)
	if !capabilities.ContentCollectionTable || !capabilities.ContentCollectionMembershipTable {
		joinTable := "post_collections"
		contentColumn := "post_id"
		if contentType == "video" {
			joinTable = "video_collections"
			contentColumn = "video_id"
		}
		if err := db.Table(joinTable+" AS memberships").
			Select(`memberships.`+contentColumn+` AS content_id, memberships.collection_id, 0 AS position,
				collections.name, collections.description, collections.cover_url,
				collections.channel_id, collections.created_by, collections.is_default`).
			Joins("JOIN collections AS collections ON collections.id = memberships.collection_id").
			Where("memberships."+contentColumn+" IN ?", contentIDs).
			Scan(&rows).Error; err != nil {
			return nil, nil, err
		}
	} else if err := db.Table("content_collection_memberships AS memberships").
		Select(`memberships.content_id, memberships.collection_id, memberships.position,
			collections.name, collections.description, collections.cover_url,
			collections.channel_id, collections.created_by, collections.is_default`).
		Joins("JOIN content_collections AS collections ON collections.id = memberships.collection_id").
		Where("memberships.content_id IN ?", contentIDs).
		Order("memberships.position ASC, memberships.collection_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, nil, err
	}
	for _, row := range rows {
		collection := model.Collection{
			Base: model.Base{ID: row.CollectionID}, ChannelID: row.ChannelID,
			ContentType: contentType, CreatedBy: row.CreatedBy,
			Name: row.Name, Description: row.Description, CoverURL: row.CoverURL, IsDefault: row.IsDefault,
		}
		collectionsByContent[row.ContentID] = append(collectionsByContent[row.ContentID], collection)
		if _, ok := positions[row.ContentID]; !ok {
			positions[row.ContentID] = row.Position
		}
	}
	return collectionsByContent, positions, nil
}

func hydratePodcastEpisodes(db *gorm.DB, rows []podcastRow) ([]model.PodcastEpisode, error) {
	contentIDs := make([]uuid.UUID, 0, len(rows))
	channelIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		contentIDs = append(contentIDs, row.ContentID)
		channelIDs = append(channelIDs, row.ChannelID)
	}
	collections, positions, err := loadMemberships(db, contentIDs, "podcast")
	if err != nil {
		return nil, err
	}
	channels, err := loadChannels(db, channelIDs)
	if err != nil {
		return nil, err
	}
	result := make([]model.PodcastEpisode, 0, len(rows))
	for _, row := range rows {
		postID := row.LegacyPostID
		if postID == uuid.Nil {
			postID = row.ContentID
		}
		post := model.Post{
			Base:   model.Base{ID: postID, CreatedAt: row.EntryCreatedAt, UpdatedAt: row.EntryUpdatedAt},
			UserID: row.AuthorID, ChannelID: uuidPtr(row.ChannelID), Title: row.Title,
			Content: row.Shownotes, Summary: row.Summary, Status: row.Status,
			Visibility: row.Visibility, PublishedAt: timePtr(row.PublishedAt), ScheduledAt: timePtr(row.ScheduledAt),
			ViewCount: row.ViewCount, CollectionPosition: positions[row.ContentID],
			CollectionConflict: row.CollectionConflict,
		}
		post.Channel = channels[row.ChannelID]
		post.Collections = collections[row.ContentID]
		if len(post.Collections) > 0 {
			post.Collection = &post.Collections[0]
			post.CollectionID = &post.Collections[0].ID
		}
		episodeCreatedAt := row.EpisodeCreatedAt
		if episodeCreatedAt.IsZero() {
			episodeCreatedAt = row.EntryCreatedAt
		}
		episodeUpdatedAt := row.EpisodeUpdatedAt
		if episodeUpdatedAt.IsZero() {
			episodeUpdatedAt = row.EntryUpdatedAt
		}
		episode := model.PodcastEpisode{
			Base:   model.Base{ID: row.EpisodeID, CreatedAt: episodeCreatedAt, UpdatedAt: episodeUpdatedAt},
			PostID: postID, Post: &post, ChannelID: row.ChannelID, Channel: channels[row.ChannelID],
			AudioURL: row.AudioURL, DurationSec: row.DurationSec, EpisodeCoverURL: row.EpisodeCoverURL,
			SeasonNumber: row.SeasonNumber, EpisodeNumber: row.EpisodeNumber,
		}
		result = append(result, episode)
	}
	return result, nil
}

func hydrateVideos(db *gorm.DB, rows []videoRow) ([]model.Video, error) {
	contentIDs := make([]uuid.UUID, 0, len(rows))
	videoIDs := make([]uuid.UUID, 0, len(rows))
	channelIDs := make([]uuid.UUID, 0, len(rows))
	authorIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		contentIDs = append(contentIDs, row.ContentID)
		videoIDs = append(videoIDs, row.VideoID)
		authorIDs = append(authorIDs, row.AuthorID)
		if row.ChannelID != nil {
			channelIDs = append(channelIDs, *row.ChannelID)
		}
	}
	collections, positions, err := loadMemberships(db, contentIDs, "video")
	if err != nil {
		return nil, err
	}
	channels, err := loadChannels(db, channelIDs)
	if err != nil {
		return nil, err
	}
	users, err := loadMediaUsers(db, authorIDs)
	if err != nil {
		return nil, err
	}
	tags, err := loadVideoTags(db, videoIDs)
	if err != nil {
		return nil, err
	}
	result := make([]model.Video, 0, len(rows))
	for _, row := range rows {
		videoCreatedAt := row.VideoCreatedAt
		if videoCreatedAt.IsZero() {
			videoCreatedAt = row.EntryCreatedAt
		}
		videoUpdatedAt := row.VideoUpdatedAt
		if videoUpdatedAt.IsZero() {
			videoUpdatedAt = row.EntryUpdatedAt
		}
		video := model.Video{
			Base:      model.Base{ID: row.VideoID, CreatedAt: videoCreatedAt, UpdatedAt: videoUpdatedAt},
			ChannelID: row.ChannelID, Channel: channelForID(channels, row.ChannelID), UserID: row.AuthorID, User: users[row.AuthorID],
			Title: row.Title, Description: row.Summary, StorageType: row.StorageType, VideoURL: row.VideoURL,
			ThumbnailURL: row.ThumbnailURL, DurationSec: row.DurationSec, ProcessingStatus: row.ProcessingStatus,
			ProcessingError: row.ProcessingError, PreviewThumbnails: row.PreviewThumbnails,
			Visibility: row.Visibility, Status: row.Status, ScheduledAt: timePtr(row.ScheduledAt),
			PublishedAt: timePtr(row.PublishedAt), ViewCount: row.ViewCount,
			CollectionConflict: row.CollectionConflict, CollectionPosition: positions[row.ContentID],
			Tags: tags[row.VideoID], Collections: collections[row.ContentID],
		}
		if len(video.Collections) > 0 {
			video.Collection = &video.Collections[0]
			video.CollectionID = &video.Collections[0].ID
		}
		result = append(result, video)
	}
	return result, nil
}

func loadChannels(db *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]*model.Channel, error) {
	unique := uniqueIDs(ids)
	result := make(map[uuid.UUID]*model.Channel, len(unique))
	if len(unique) == 0 {
		return result, nil
	}
	var channels []model.Channel
	if err := db.Where("id IN ?", unique).Find(&channels).Error; err != nil {
		return nil, err
	}
	for index := range channels {
		result[channels[index].ID] = &channels[index]
	}
	return result, nil
}

func channelForID(channels map[uuid.UUID]*model.Channel, id *uuid.UUID) *model.Channel {
	if id == nil {
		return nil
	}
	return channels[*id]
}

func loadMediaUsers(db *gorm.DB, ids []uuid.UUID) (map[uuid.UUID]*model.User, error) {
	unique := uniqueIDs(ids)
	result := make(map[uuid.UUID]*model.User, len(unique))
	if len(unique) == 0 {
		return result, nil
	}
	var users []model.User
	if err := db.Where("uuid IN ?", unique).Find(&users).Error; err != nil {
		return nil, err
	}
	for index := range users {
		result[users[index].UUID] = &users[index]
	}
	return result, nil
}

func loadVideoTags(db *gorm.DB, videoIDs []uuid.UUID) (map[uuid.UUID][]model.VideoTag, error) {
	result := make(map[uuid.UUID][]model.VideoTag)
	if len(videoIDs) == 0 {
		return result, nil
	}
	var relations []model.VideoTagRelation
	if err := db.Where("video_id IN ?", videoIDs).Find(&relations).Error; err != nil {
		return nil, err
	}
	tagIDs := make([]uuid.UUID, 0, len(relations))
	for _, relation := range relations {
		tagIDs = append(tagIDs, relation.TagID)
	}
	var tags []model.VideoTag
	if len(tagIDs) > 0 {
		if err := db.Where("id IN ?", tagIDs).Find(&tags).Error; err != nil {
			return nil, err
		}
	}
	tagsByID := make(map[uuid.UUID]model.VideoTag, len(tags))
	for _, tag := range tags {
		tagsByID[tag.ID] = tag
	}
	for _, relation := range relations {
		if tag, ok := tagsByID[relation.TagID]; ok {
			result[relation.VideoID] = append(result[relation.VideoID], tag)
		}
	}
	return result, nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	result := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if id == uuid.Nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func uuidPtr(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func timePtr(value *modelTime) *time.Time {
	if value == nil {
		return nil
	}
	return value
}

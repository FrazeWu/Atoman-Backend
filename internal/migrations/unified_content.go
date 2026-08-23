package migrations

import (
	"errors"
	"fmt"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RunUnifiedContentMigration backfills canonical entries without changing legacy
// resources. Extension rows make the operation safe to run on every startup.
func RunUnifiedContentMigration(db *gorm.DB) error {
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(
			&model.ContentEntry{}, &model.ContentPostExtension{}, &model.ContentBlogExtension{}, &model.ContentBlogVersion{}, &model.ContentBlogDraft{}, &model.ContentEpisodeExtension{}, &model.ContentVideoExtension{},
			&model.ContentCollection{}, &model.ContentCollectionMembership{}, &model.LegacyCollectionMapping{},
		); err != nil {
			return err
		}
		if err := backfillPostContentEntries(tx); err != nil {
			return err
		}
		if err := backfillVideoContentEntries(tx); err != nil {
			return err
		}
		if err := backfillContentCollections(tx); err != nil {
			return err
		}
		if err := backfillContentBlogVersions(tx); err != nil {
			return err
		}
		return backfillContentBlogDrafts(tx)
	}); err != nil {
		return fmt.Errorf("unified content migration: %w", err)
	}
	return nil
}

func backfillPostContentEntries(tx *gorm.DB) error {
	var episodes []model.PodcastEpisode
	if err := tx.Find(&episodes).Error; err != nil {
		return err
	}
	episodeByPost := make(map[uuid.UUID]model.PodcastEpisode, len(episodes))
	for _, episode := range episodes {
		episodeByPost[episode.PostID] = episode
	}
	var posts []model.Post
	if err := tx.Where("channel_id IS NOT NULL").Find(&posts).Error; err != nil {
		return err
	}
	for _, post := range posts {
		kind := "blog"
		if _, ok := episodeByPost[post.ID]; ok {
			kind = "podcast"
		}
		entry, err := ensurePostContentEntry(tx, post, kind)
		if err != nil {
			return err
		}
		if kind == "podcast" {
			episode := episodeByPost[post.ID]
			if err := ensureEpisodeExtension(tx, entry.ID, episode.ID, post); err != nil {
				return err
			}
		} else {
			if err := ensurePostExtension(tx, entry.ID, post.ID); err != nil {
				return err
			}
			if err := ensureBlogExtension(tx, entry.ID, post); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensurePostContentEntry(tx *gorm.DB, post model.Post, kind string) (model.ContentEntry, error) {
	var extensionContentID uuid.UUID
	var err error
	if kind == "podcast" {
		var episode model.PodcastEpisode
		if err = tx.Where("post_id = ?", post.ID).First(&episode).Error; err == nil {
			var extension model.ContentEpisodeExtension
			if err = tx.Where("episode_id = ?", episode.ID).First(&extension).Error; err == nil {
				extensionContentID = extension.ContentID
			}
		}
	} else {
		var extension model.ContentPostExtension
		err = tx.Where("post_id = ?", post.ID).First(&extension).Error
		if err == nil {
			extensionContentID = extension.ContentID
		}
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.ContentEntry{}, err
	}
	if extensionContentID != uuid.Nil {
		var entry model.ContentEntry
		if err := tx.First(&entry, "id = ?", extensionContentID).Error; err != nil {
			return model.ContentEntry{}, err
		}
		if err := syncContentEntryFromPost(tx, &entry, post, kind); err != nil {
			return model.ContentEntry{}, err
		}
		return entry, nil
	}
	if post.ChannelID == nil {
		return model.ContentEntry{}, fmt.Errorf("post %s has no channel", post.ID)
	}
	authorID := post.UserID
	entry := model.ContentEntry{Base: post.Base, AuthorID: &authorID, ChannelID: *post.ChannelID, Kind: kind, Title: post.Title, Summary: post.Summary, CoverURL: post.CoverURL, Status: post.Status, Visibility: post.Visibility, PublishedAt: post.PublishedAt, ScheduledAt: post.ScheduledAt}
	return entry, tx.Create(&entry).Error
}

func backfillContentCollections(tx *gorm.DB) error {
	var legacy []model.Collection
	if err := tx.Find(&legacy).Error; err != nil {
		return err
	}
	defaults := make(map[uuid.UUID]uuid.UUID)
	for _, collection := range legacy {
		var unified model.ContentCollection
		if collection.IsDefault {
			if id, ok := defaults[collection.ChannelID]; ok {
				unified.ID = id
			} else {
				unified = model.ContentCollection{ChannelID: collection.ChannelID, CreatedBy: collection.CreatedBy, Name: "未分类", IsDefault: true}
				if err := tx.Where("channel_id = ? AND is_default = ?", collection.ChannelID, true).FirstOrCreate(&unified).Error; err != nil {
					return err
				}
				defaults[collection.ChannelID] = unified.ID
			}
		} else {
			unified = model.ContentCollection{ChannelID: collection.ChannelID, CreatedBy: collection.CreatedBy, Name: collection.Name, Description: collection.Description, CoverURL: collection.CoverURL}
			if err := tx.Where("channel_id = ? AND name = ? AND is_default = ?", collection.ChannelID, collection.Name, false).FirstOrCreate(&unified).Error; err != nil {
				return err
			}
		}
		mapping := model.LegacyCollectionMapping{LegacyCollectionID: collection.ID, ContentCollectionID: unified.ID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&mapping).Error; err != nil {
			return err
		}
	}
	if err := backfillContentCollectionMemberships(tx); err != nil {
		return fmt.Errorf("backfill content collection memberships: %w", err)
	}
	return nil
}

func backfillContentCollectionMemberships(tx *gorm.DB) error {
	var mappings []model.LegacyCollectionMapping
	if err := tx.Find(&mappings).Error; err != nil {
		return err
	}
	collectionByLegacyID := make(map[uuid.UUID]uuid.UUID, len(mappings))
	for _, mapping := range mappings {
		collectionByLegacyID[mapping.LegacyCollectionID] = mapping.ContentCollectionID
	}
	contentByPostID, err := contentIDsByPost(tx)
	if err != nil {
		return err
	}
	var postLinks []model.PostCollection
	if err := tx.Find(&postLinks).Error; err != nil {
		return err
	}
	for _, link := range postLinks {
		if err := createContentCollectionMembership(tx, contentByPostID[link.PostID], collectionByLegacyID[link.CollectionID], link.Position); err != nil {
			return err
		}
	}
	var videoExtensions []model.ContentVideoExtension
	if err := tx.Find(&videoExtensions).Error; err != nil {
		return err
	}
	contentByVideoID := make(map[uuid.UUID]uuid.UUID, len(videoExtensions))
	for _, extension := range videoExtensions {
		contentByVideoID[extension.VideoID] = extension.ContentID
	}
	var videoLinks []model.VideoCollection
	if err := tx.Find(&videoLinks).Error; err != nil {
		return err
	}
	for _, link := range videoLinks {
		if err := createContentCollectionMembership(tx, contentByVideoID[link.VideoID], collectionByLegacyID[link.CollectionID], 0); err != nil {
			return err
		}
	}
	return nil
}

func contentIDsByPost(tx *gorm.DB) (map[uuid.UUID]uuid.UUID, error) {
	var postExtensions []model.ContentPostExtension
	if err := tx.Find(&postExtensions).Error; err != nil {
		return nil, err
	}
	contentByPostID := make(map[uuid.UUID]uuid.UUID, len(postExtensions))
	for _, extension := range postExtensions {
		contentByPostID[extension.PostID] = extension.ContentID
	}
	var episodes []model.PodcastEpisode
	if err := tx.Find(&episodes).Error; err != nil {
		return nil, err
	}
	postByEpisodeID := make(map[uuid.UUID]uuid.UUID, len(episodes))
	for _, episode := range episodes {
		postByEpisodeID[episode.ID] = episode.PostID
	}
	var episodeExtensions []model.ContentEpisodeExtension
	if err := tx.Find(&episodeExtensions).Error; err != nil {
		return nil, err
	}
	for _, extension := range episodeExtensions {
		if postID, ok := postByEpisodeID[extension.EpisodeID]; ok {
			contentByPostID[postID] = extension.ContentID
		}
	}
	return contentByPostID, nil
}

func createContentCollectionMembership(tx *gorm.DB, contentID, collectionID uuid.UUID, position int) error {
	if contentID == uuid.Nil || collectionID == uuid.Nil {
		return nil
	}
	membership := model.ContentCollectionMembership{ContentID: contentID, CollectionID: collectionID, Position: position}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&membership).Error
}

func backfillContentBlogVersions(tx *gorm.DB) error {
	contentByPostID, err := contentIDsByPost(tx)
	if err != nil {
		return err
	}
	collectionByLegacyID, err := legacyContentCollectionIDs(tx)
	if err != nil {
		return err
	}
	var versions []model.BlogPostVersion
	if err := tx.Find(&versions).Error; err != nil {
		return err
	}
	for _, version := range versions {
		contentID, ok := contentByPostID[version.PostID]
		if !ok {
			return fmt.Errorf("blog version %s references post %s without a content entry", version.ID, version.PostID)
		}
		collectionID, ok := collectionByLegacyID[version.CollectionID]
		if !ok {
			return fmt.Errorf("blog version %s references collection %s without a content collection", version.ID, version.CollectionID)
		}
		canonical := model.ContentBlogVersion{
			Base:         version.Base,
			ContentID:    contentID,
			Version:      version.Version,
			EditorID:     version.EditorID,
			Title:        version.Title,
			Content:      version.Content,
			Summary:      version.Summary,
			CoverURL:     version.CoverURL,
			Visibility:   version.Visibility,
			CollectionID: collectionID,
			PublishedAt:  version.PublishedAt,
		}
		var existing model.ContentBlogVersion
		result := tx.First(&existing, "id = ?", version.ID)
		switch {
		case errors.Is(result.Error, gorm.ErrRecordNotFound):
			if err := tx.Create(&canonical).Error; err != nil {
				return err
			}
		case result.Error != nil:
			return result.Error
		default:
			if err := tx.Model(&existing).Updates(map[string]any{
				"content_id": contentID, "version": version.Version, "editor_id": version.EditorID,
				"title": version.Title, "content": version.Content, "summary": version.Summary,
				"cover_url": version.CoverURL, "visibility": version.Visibility,
				"collection_id": collectionID, "published_at": version.PublishedAt,
			}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func backfillContentBlogDrafts(tx *gorm.DB) error {
	contentByPostID, err := contentIDsByPost(tx)
	if err != nil {
		return err
	}
	collectionByLegacyID, err := legacyContentCollectionIDs(tx)
	if err != nil {
		return err
	}
	var drafts []model.BlogDraft
	if err := tx.Find(&drafts).Error; err != nil {
		return err
	}
	for _, draft := range drafts {
		var contentID *uuid.UUID
		if draft.SourcePostID != nil {
			id, ok := contentByPostID[*draft.SourcePostID]
			if !ok {
				return fmt.Errorf("blog draft %s references post %s without a content entry", draft.ID, *draft.SourcePostID)
			}
			contentID = &id
		}
		var collectionID *uuid.UUID
		if draft.CollectionID != nil {
			id, ok := collectionByLegacyID[*draft.CollectionID]
			if !ok {
				return fmt.Errorf("blog draft %s references collection %s without a content collection", draft.ID, *draft.CollectionID)
			}
			collectionID = &id
		}
		canonical := model.ContentBlogDraft{
			Base:         draft.Base,
			UserID:       draft.UserID,
			ContentID:    contentID,
			ContextKey:   draft.ContextKey,
			Title:        draft.Title,
			Content:      draft.Content,
			Summary:      draft.Summary,
			CoverURL:     draft.CoverURL,
			Visibility:   draft.Visibility,
			ChannelID:    draft.ChannelID,
			CollectionID: collectionID,
		}
		var existing model.ContentBlogDraft
		result := tx.First(&existing, "id = ?", draft.ID)
		switch {
		case errors.Is(result.Error, gorm.ErrRecordNotFound):
			if err := tx.Create(&canonical).Error; err != nil {
				return err
			}
		case result.Error != nil:
			return result.Error
		default:
			if err := tx.Model(&existing).Updates(map[string]any{
				"user_id": draft.UserID, "content_id": contentID, "context_key": draft.ContextKey,
				"title": draft.Title, "content": draft.Content, "summary": draft.Summary,
				"cover_url": draft.CoverURL, "visibility": draft.Visibility,
				"channel_id": draft.ChannelID, "collection_id": collectionID,
			}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func legacyContentCollectionIDs(tx *gorm.DB) (map[uuid.UUID]uuid.UUID, error) {
	var mappings []model.LegacyCollectionMapping
	if err := tx.Find(&mappings).Error; err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]uuid.UUID, len(mappings))
	for _, mapping := range mappings {
		result[mapping.LegacyCollectionID] = mapping.ContentCollectionID
	}
	return result, nil
}

func syncContentEntryFromPost(tx *gorm.DB, entry *model.ContentEntry, post model.Post, kind string) error {
	if post.ChannelID == nil {
		return fmt.Errorf("post %s has no channel", post.ID)
	}
	updates := map[string]any{
		"author_id":    post.UserID,
		"channel_id":   *post.ChannelID,
		"kind":         kind,
		"title":        post.Title,
		"summary":      post.Summary,
		"cover_url":    post.CoverURL,
		"status":       post.Status,
		"visibility":   post.Visibility,
		"published_at": post.PublishedAt,
		"scheduled_at": post.ScheduledAt,
	}
	return tx.Model(entry).Updates(updates).Error
}

func ensureBlogExtension(tx *gorm.DB, contentID uuid.UUID, post model.Post) error {
	var extension model.ContentBlogExtension
	result := tx.Where("content_id = ?", contentID).First(&extension)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return tx.Create(&model.ContentBlogExtension{
			ContentID:          contentID,
			Content:            post.Content,
			LanguageCode:       post.LanguageCode,
			Pinned:             post.Pinned,
			ViewCount:          post.ViewCount,
			CollectionConflict: post.CollectionConflict,
		}).Error
	}
	if result.Error != nil {
		return result.Error
	}
	return tx.Model(&extension).Updates(map[string]any{
		"content":             post.Content,
		"language_code":       post.LanguageCode,
		"pinned":              post.Pinned,
		"view_count":          post.ViewCount,
		"collection_conflict": post.CollectionConflict,
	}).Error
}

func ensurePostExtension(tx *gorm.DB, contentID, postID uuid.UUID) error {
	return tx.Where(model.ContentPostExtension{PostID: postID}).FirstOrCreate(&model.ContentPostExtension{ContentID: contentID, PostID: postID}).Error
}

func ensureEpisodeExtension(tx *gorm.DB, contentID, episodeID uuid.UUID, post model.Post) error {
	extension := model.ContentEpisodeExtension{
		ContentID: contentID, EpisodeID: episodeID, LegacyPostID: post.ID,
		AudioURL: "", DurationSec: 0, EpisodeCoverURL: "", SeasonNumber: 1,
		EpisodeNumber: 0, Shownotes: post.Content, ViewCount: post.ViewCount, CollectionConflict: post.CollectionConflict,
	}
	var episode model.PodcastEpisode
	if err := tx.First(&episode, "id = ?", episodeID).Error; err != nil {
		return err
	}
	extension.AudioURL = episode.AudioURL
	extension.DurationSec = episode.DurationSec
	extension.EpisodeCoverURL = episode.EpisodeCoverURL
	extension.SeasonNumber = episode.SeasonNumber
	extension.EpisodeNumber = episode.EpisodeNumber
	var existing model.ContentEpisodeExtension
	err := tx.Where("episode_id = ?", episodeID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&extension).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&existing).Updates(map[string]any{
		"content_id":          existing.ContentID,
		"legacy_post_id":      extension.LegacyPostID,
		"audio_url":           extension.AudioURL,
		"duration_sec":        extension.DurationSec,
		"episode_cover_url":   extension.EpisodeCoverURL,
		"season_number":       extension.SeasonNumber,
		"episode_number":      extension.EpisodeNumber,
		"shownotes":           extension.Shownotes,
		"view_count":          extension.ViewCount,
		"collection_conflict": extension.CollectionConflict,
	}).Error
}

func syncContentEntryFromVideo(tx *gorm.DB, entry *model.ContentEntry, video model.Video) error {
	if video.ChannelID == nil {
		return fmt.Errorf("video %s has no channel", video.ID)
	}
	return tx.Model(entry).Updates(map[string]any{
		"author_id":    video.UserID,
		"channel_id":   *video.ChannelID,
		"kind":         "video",
		"title":        video.Title,
		"summary":      video.Description,
		"cover_url":    video.ThumbnailURL,
		"status":       video.Status,
		"visibility":   video.Visibility,
		"published_at": video.PublishedAt,
		"scheduled_at": video.ScheduledAt,
	}).Error
}

func syncContentVideoExtension(tx *gorm.DB, contentID uuid.UUID, video model.Video) error {
	extension := model.ContentVideoExtension{
		ContentID: contentID, VideoID: video.ID, StorageType: video.StorageType,
		VideoURL: video.VideoURL, ThumbnailURL: video.ThumbnailURL, DurationSec: video.DurationSec,
		ProcessingStatus: video.ProcessingStatus, ProcessingError: video.ProcessingError,
		PreviewThumbnails: video.PreviewThumbnails, ViewCount: video.ViewCount,
		CollectionConflict: video.CollectionConflict,
	}
	var existing model.ContentVideoExtension
	err := tx.Where("video_id = ?", video.ID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(&extension).Error
	}
	if err != nil {
		return err
	}
	return tx.Model(&existing).Updates(map[string]any{
		"content_id":          existing.ContentID,
		"storage_type":        extension.StorageType,
		"video_url":           extension.VideoURL,
		"thumbnail_url":       extension.ThumbnailURL,
		"duration_sec":        extension.DurationSec,
		"processing_status":   extension.ProcessingStatus,
		"processing_error":    extension.ProcessingError,
		"preview_thumbnails":  extension.PreviewThumbnails,
		"view_count":          extension.ViewCount,
		"collection_conflict": extension.CollectionConflict,
	}).Error
}

func backfillVideoContentEntries(tx *gorm.DB) error {
	var videos []model.Video
	if err := tx.Where("channel_id IS NOT NULL").Find(&videos).Error; err != nil {
		return err
	}
	for _, video := range videos {
		var contentID uuid.UUID
		var extension model.ContentVideoExtension
		if err := tx.Where("video_id = ?", video.ID).First(&extension).Error; err == nil {
			contentID = extension.ContentID
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if contentID != uuid.Nil {
			var entry model.ContentEntry
			if err := tx.First(&entry, "id = ?", contentID).Error; err != nil {
				return err
			}
			if err := syncContentEntryFromVideo(tx, &entry, video); err != nil {
				return err
			}
			if err := syncContentVideoExtension(tx, entry.ID, video); err != nil {
				return err
			}
			continue
		}
		authorID := video.UserID
		entry := model.ContentEntry{AuthorID: &authorID, ChannelID: *video.ChannelID, Kind: "video", Title: video.Title, Summary: video.Description, CoverURL: video.ThumbnailURL, Status: video.Status, Visibility: video.Visibility, PublishedAt: video.PublishedAt, ScheduledAt: video.ScheduledAt}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.ContentVideoExtension{ContentID: entry.ID, VideoID: video.ID}).Error; err != nil {
			return err
		}
		if err := syncContentVideoExtension(tx, entry.ID, video); err != nil {
			return err
		}
	}
	return nil
}

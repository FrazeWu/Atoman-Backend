package handlers

import (
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func registerVideoCanonicalTestCallbacks(t *testing.T, db *gorm.DB) {
	t.Helper()
	collectionCallback := "test:handlers-video-canonical-collection"
	if err := db.Callback().Create().After("gorm:create").Register(collectionCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "collections" {
			return
		}
		if collection, ok := tx.Statement.Dest.(*model.Collection); ok {
			syncVideoTestCollection(tx, *collection)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(collectionCallback) })

	videoCallback := "test:handlers-video-canonical-video"
	if err := db.Callback().Create().After("gorm:create").Register(videoCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "videos" {
			return
		}
		if video, ok := tx.Statement.Dest.(*model.Video); ok && video.ID != uuid.Nil {
			syncVideoTestRecord(tx, video.ID)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(videoCallback) })

	updateCallback := "test:handlers-video-canonical-update"
	if err := db.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case "videos":
			if video, ok := tx.Statement.Model.(*model.Video); ok {
				syncVideoTestRecord(tx, video.ID)
			}
		case "video_collections":
			if link, ok := tx.Statement.Dest.(*model.VideoCollection); ok {
				syncVideoTestMembership(tx, link.VideoID, link.CollectionID, 0)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(updateCallback) })

	linkCallback := "test:handlers-video-canonical-link"
	if err := db.Callback().Create().After("gorm:create").Register(linkCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "video_collections" {
			return
		}
		if link, ok := tx.Statement.Dest.(*model.VideoCollection); ok {
			syncVideoTestMembership(tx, link.VideoID, link.CollectionID, 0)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(linkCallback) })
}

func syncVideoTestCollection(db *gorm.DB, legacy model.Collection) {
	db = db.Session(&gorm.Session{NewDB: true})
	canonical := model.ContentCollection{
		Base: legacy.Base, ChannelID: legacy.ChannelID, CreatedBy: legacy.CreatedBy,
		Name: legacy.Name, Description: legacy.Description, CoverURL: legacy.CoverURL, IsDefault: legacy.IsDefault,
	}
	var existing model.ContentCollection
	if err := db.First(&existing, "id = ?", legacy.ID).Error; err == gorm.ErrRecordNotFound {
		_ = db.Create(&canonical).Error
	} else if err == nil {
		_ = db.Model(&existing).Updates(map[string]any{
			"channel_id": legacy.ChannelID, "created_by": legacy.CreatedBy, "name": legacy.Name,
			"description": legacy.Description, "cover_url": legacy.CoverURL, "is_default": legacy.IsDefault,
		}).Error
	}
}

func syncVideoTestRecord(db *gorm.DB, videoID uuid.UUID) {
	db = db.Session(&gorm.Session{NewDB: true})
	var video model.Video
	if db.First(&video, "id = ?", videoID).Error != nil {
		return
	}
	channelID := uuid.Nil
	if video.ChannelID != nil {
		channelID = *video.ChannelID
	} else {
		var channel model.Channel
		if db.Where("user_id = ?", video.UserID).First(&channel).Error == gorm.ErrRecordNotFound {
			channel = model.Channel{Base: model.Base{ID: uuid.New()}, Name: "Test Video Channel", Slug: "test-video-channel-" + uuid.NewString()[:8]}
			if err := db.Create(&channel).Error; err != nil {
				return
			}
		}
		channelID = channel.ID
	}
	visibility := video.Visibility
	if visibility == "" {
		visibility = "public"
	}
	var authorID *uuid.UUID
	if video.UserID != uuid.Nil {
		author := video.UserID
		authorID = &author
	}
	entry := model.ContentEntry{
		Base: video.Base, AuthorID: authorID, ChannelID: channelID, Kind: "video",
		Title: video.Title, Summary: video.Description, CoverURL: video.ThumbnailURL,
		Status: video.Status, Visibility: visibility, PublishedAt: video.PublishedAt, ScheduledAt: video.ScheduledAt,
	}
	var existing model.ContentEntry
	if db.First(&existing, "id = ?", video.ID).Error == gorm.ErrRecordNotFound {
		_ = db.Create(&entry).Error
	} else {
		_ = db.Model(&existing).Updates(map[string]any{
			"author_id": authorID, "channel_id": channelID, "kind": "video", "title": video.Title,
			"summary": video.Description, "cover_url": video.ThumbnailURL, "status": video.Status,
			"visibility": visibility, "published_at": video.PublishedAt, "scheduled_at": video.ScheduledAt,
		}).Error
	}
	extension := model.ContentVideoExtension{
		ContentID: video.ID, VideoID: video.ID, CreatedAt: video.CreatedAt, UpdatedAt: video.UpdatedAt, StorageType: video.StorageType, VideoURL: video.VideoURL,
		ThumbnailURL: video.ThumbnailURL, DurationSec: video.DurationSec, ProcessingStatus: video.ProcessingStatus,
		ProcessingError: video.ProcessingError, PreviewThumbnails: video.PreviewThumbnails,
		ViewCount: video.ViewCount, CollectionConflict: video.CollectionConflict,
	}
	var existingExtension model.ContentVideoExtension
	if db.Where("video_id = ?", video.ID).First(&existingExtension).Error == gorm.ErrRecordNotFound {
		_ = db.Create(&extension).Error
	} else {
		_ = db.Model(&existingExtension).Updates(map[string]any{
			"content_id": video.ID, "created_at": video.CreatedAt, "updated_at": video.UpdatedAt, "storage_type": video.StorageType, "video_url": video.VideoURL,
			"thumbnail_url": video.ThumbnailURL, "duration_sec": video.DurationSec,
			"processing_status": video.ProcessingStatus, "processing_error": video.ProcessingError,
			"preview_thumbnails": video.PreviewThumbnails, "view_count": video.ViewCount,
			"collection_conflict": video.CollectionConflict,
		}).Error
	}
	_ = db.Where("content_id = ?", video.ID).Delete(&model.ContentCollectionMembership{}).Error
	if video.CollectionID != nil {
		syncVideoTestMembership(db, video.ID, *video.CollectionID, video.CollectionPosition)
	}
	var links []model.VideoCollection
	if db.Where("video_id = ?", video.ID).Find(&links).Error == nil {
		for _, link := range links {
			syncVideoTestMembership(db, video.ID, link.CollectionID, video.CollectionPosition)
		}
	}
}

func syncVideoTestMembership(db *gorm.DB, contentID, collectionID uuid.UUID, position int) {
	db = db.Session(&gorm.Session{NewDB: true})
	var legacy model.Collection
	if db.First(&legacy, "id = ?", collectionID).Error != nil {
		return
	}
	syncVideoTestCollection(db, legacy)
	_ = db.Where("content_id = ? AND collection_id = ?", contentID, collectionID).
		FirstOrCreate(&model.ContentCollectionMembership{ContentID: contentID, CollectionID: collectionID, Position: position}).Error
}

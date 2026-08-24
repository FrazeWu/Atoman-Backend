package handlers

import (
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func registerPodcastCanonicalTestCallbacks(t *testing.T, db *gorm.DB) {
	t.Helper()
	collectionCallback := "test:handlers-podcast-canonical-collection"
	if err := db.Callback().Create().After("gorm:create").Register(collectionCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "collections" {
			return
		}
		collection, ok := tx.Statement.Dest.(*model.Collection)
		if !ok {
			return
		}
		syncPodcastTestCollection(tx, *collection)
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(collectionCallback) })

	episodeCallback := "test:handlers-podcast-canonical-episode"
	if err := db.Callback().Create().After("gorm:create").Register(episodeCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "podcast_episodes" {
			return
		}
		if episode, ok := tx.Statement.Dest.(*model.PodcastEpisode); ok {
			syncPodcastTestEpisode(tx, episode.ID)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(episodeCallback) })

	updateCallback := "test:handlers-podcast-canonical-update"
	if err := db.Callback().Update().After("gorm:update").Register(updateCallback, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case "posts":
			if post, ok := tx.Statement.Dest.(*model.Post); ok {
				var episode model.PodcastEpisode
				if tx.First(&episode, "post_id = ?", post.ID).Error == nil {
					syncPodcastTestEpisode(tx, episode.ID)
				}
			}
		case "podcast_episodes":
			if episode, ok := tx.Statement.Dest.(*model.PodcastEpisode); ok {
				syncPodcastTestEpisode(tx, episode.ID)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(updateCallback) })
}

func syncPodcastTestCollection(db *gorm.DB, legacy model.Collection) {
	db = db.Session(&gorm.Session{NewDB: true})
	canonical := model.ContentCollection{
		Base: legacy.Base, ChannelID: legacy.ChannelID, CreatedBy: legacy.CreatedBy,
		Name: legacy.Name, Description: legacy.Description, CoverURL: legacy.CoverURL, IsDefault: legacy.IsDefault,
	}
	var existing model.ContentCollection
	if err := db.First(&existing, "id = ?", legacy.ID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			_ = db.Create(&canonical).Error
		}
		return
	}
	_ = db.Model(&existing).Updates(map[string]any{
		"channel_id": legacy.ChannelID, "created_by": legacy.CreatedBy, "name": legacy.Name,
		"description": legacy.Description, "cover_url": legacy.CoverURL, "is_default": legacy.IsDefault,
	}).Error
}

func syncPodcastTestEpisode(db *gorm.DB, episodeID uuid.UUID) {
	db = db.Session(&gorm.Session{NewDB: true})
	var episode model.PodcastEpisode
	if db.First(&episode, "id = ?", episodeID).Error != nil {
		return
	}
	var post model.Post
	if db.First(&post, "id = ?", episode.PostID).Error != nil || post.ChannelID == nil {
		return
	}
	visibility := post.Visibility
	if visibility == "" {
		visibility = "public"
	}
	authorID := post.UserID
	entry := model.ContentEntry{
		Base: post.Base, AuthorID: &authorID, ChannelID: *post.ChannelID, Kind: "podcast",
		Title: post.Title, Summary: post.Summary, Status: post.Status, Visibility: visibility,
		PublishedAt: post.PublishedAt, ScheduledAt: post.ScheduledAt,
	}
	var existing model.ContentEntry
	if db.First(&existing, "id = ?", post.ID).Error == gorm.ErrRecordNotFound {
		_ = db.Create(&entry).Error
	} else {
		_ = db.Model(&existing).Updates(map[string]any{
			"author_id": authorID, "channel_id": *post.ChannelID, "kind": "podcast", "title": post.Title,
			"summary": post.Summary, "status": post.Status, "visibility": visibility,
			"published_at": post.PublishedAt, "scheduled_at": post.ScheduledAt,
		}).Error
	}
	extension := model.ContentEpisodeExtension{
		ContentID: post.ID, EpisodeID: episode.ID, LegacyPostID: post.ID, CreatedAt: episode.CreatedAt, UpdatedAt: episode.UpdatedAt, AudioURL: episode.AudioURL,
		DurationSec: episode.DurationSec, EpisodeCoverURL: episode.EpisodeCoverURL,
		SeasonNumber: episode.SeasonNumber, EpisodeNumber: episode.EpisodeNumber,
		Shownotes: post.Content, ViewCount: post.ViewCount, CollectionConflict: post.CollectionConflict,
	}
	var existingExtension model.ContentEpisodeExtension
	if db.Where("episode_id = ?", episode.ID).First(&existingExtension).Error == gorm.ErrRecordNotFound {
		_ = db.Create(&extension).Error
	} else {
		_ = db.Model(&existingExtension).Updates(map[string]any{
			"content_id": post.ID, "legacy_post_id": post.ID, "created_at": episode.CreatedAt, "updated_at": episode.UpdatedAt, "audio_url": episode.AudioURL,
			"duration_sec": episode.DurationSec, "episode_cover_url": episode.EpisodeCoverURL,
			"season_number": episode.SeasonNumber, "episode_number": episode.EpisodeNumber,
			"shownotes": post.Content, "view_count": post.ViewCount, "collection_conflict": post.CollectionConflict,
		}).Error
	}
	_ = db.Where("content_id = ?", post.ID).Delete(&model.ContentCollectionMembership{}).Error
	if post.CollectionID != nil {
		syncPodcastTestCollectionMembership(db, post.ID, *post.CollectionID)
	}
	var links []model.PostCollection
	if db.Where("post_id = ?", post.ID).Find(&links).Error == nil {
		for _, link := range links {
			syncPodcastTestCollectionMembership(db, post.ID, link.CollectionID)
		}
	}
}

func syncPodcastTestCollectionMembership(db *gorm.DB, contentID, collectionID uuid.UUID) {
	db = db.Session(&gorm.Session{NewDB: true})
	var legacy model.Collection
	if db.First(&legacy, "id = ?", collectionID).Error != nil {
		return
	}
	syncPodcastTestCollection(db, legacy)
	_ = db.Where("content_id = ? AND collection_id = ?", contentID, collectionID).
		FirstOrCreate(&model.ContentCollectionMembership{ContentID: contentID, CollectionID: collectionID}).Error
}

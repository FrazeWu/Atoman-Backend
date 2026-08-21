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
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(
			&model.ContentEntry{}, &model.ContentPostExtension{}, &model.ContentEpisodeExtension{}, &model.ContentVideoExtension{},
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
		return backfillContentCollections(tx)
	})
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
			if err := ensureEpisodeExtension(tx, entry.ID, episode.ID); err != nil {
				return err
			}
		} else if err := ensurePostExtension(tx, entry.ID, post.ID); err != nil {
			return err
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
		return entry, nil
	}
	if post.ChannelID == nil {
		return model.ContentEntry{}, fmt.Errorf("post %s has no channel", post.ID)
	}
	entry := model.ContentEntry{ChannelID: *post.ChannelID, Kind: kind, Title: post.Title, Summary: post.Summary, CoverURL: post.CoverURL, Status: post.Status, Visibility: post.Visibility, PublishedAt: post.PublishedAt, ScheduledAt: post.ScheduledAt}
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
	return backfillContentCollectionMemberships(tx)
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

func ensurePostExtension(tx *gorm.DB, contentID, postID uuid.UUID) error {
	return tx.Where(model.ContentPostExtension{PostID: postID}).FirstOrCreate(&model.ContentPostExtension{ContentID: contentID, PostID: postID}).Error
}

func ensureEpisodeExtension(tx *gorm.DB, contentID, episodeID uuid.UUID) error {
	return tx.Where(model.ContentEpisodeExtension{EpisodeID: episodeID}).FirstOrCreate(&model.ContentEpisodeExtension{ContentID: contentID, EpisodeID: episodeID}).Error
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
			continue
		}
		entry := model.ContentEntry{ChannelID: *video.ChannelID, Kind: "video", Title: video.Title, Summary: video.Description, CoverURL: video.ThumbnailURL, Status: video.Status, Visibility: video.Visibility, PublishedAt: video.PublishedAt, ScheduledAt: video.ScheduledAt}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.ContentVideoExtension{ContentID: entry.ID, VideoID: video.ID}).Error; err != nil {
			return err
		}
	}
	return nil
}

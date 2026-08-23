package studio

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) ReorderCollectionContents(user authctx.CurrentUser, module Module, collectionID uuid.UUID, orderedIDs []uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	if len(orderedIDs) == 0 {
		return apperr.BadRequest("validation.invalid_request", "content_ids is required")
	}
	if module == ModuleBlog {
		return s.ReorderUnifiedCollectionContents(user, collectionID, orderedIDs)
	}
	collection, err := s.collectionInModule(collectionID, module)
	if err != nil {
		return err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return err
	}
	seen := make(map[uuid.UUID]struct{}, len(orderedIDs))
	for _, id := range orderedIDs {
		if id == uuid.Nil {
			return apperr.BadRequest("validation.invalid_request", "content_ids must be valid UUIDs")
		}
		if _, ok := seen[id]; ok {
			return apperr.BadRequest("validation.invalid_request", "content_ids must be unique")
		}
		seen[id] = struct{}{}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		switch module {
		case ModulePodcast:
			var episodes []model.PodcastEpisode
			if err := tx.Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.deleted_at IS NULL").Where("posts.user_id = ? AND posts.collection_id = ? AND posts.collection_conflict = ?", user.ID, collection.ID, false).Find(&episodes).Error; err != nil {
				return err
			}
			if err := requireExactOrder(episodes, orderedIDs, func(episode model.PodcastEpisode) uuid.UUID { return episode.ID }); err != nil {
				return err
			}
			for position, id := range orderedIDs {
				if err := tx.Model(&model.Post{}).Where("id = (SELECT post_id FROM podcast_episodes WHERE id = ?)", id).Update("collection_position", position).Error; err != nil {
					return err
				}
			}
		case ModuleVideo:
			var videos []model.Video
			if err := tx.Where("user_id = ? AND collection_id = ? AND collection_conflict = ?", user.ID, collection.ID, false).Find(&videos).Error; err != nil {
				return err
			}
			if err := requireExactOrder(videos, orderedIDs, func(video model.Video) uuid.UUID { return video.ID }); err != nil {
				return err
			}
			for position, id := range orderedIDs {
				if err := tx.Model(&model.Video{}).Where("id = ?", id).Update("collection_position", position).Error; err != nil {
					return err
				}
			}
		default:
			return apperr.BadRequest("studio.invalid_module", "module must be blog, podcast, or video")
		}
		return recordStudioAudit(tx, user.ID, "studio.collection_contents_reordered", "collection", collection.ID, map[string]any{
			"module": module, "content_count": len(orderedIDs),
		})
	})
}

func requireExactOrder[T any](items []T, orderedIDs []uuid.UUID, idOf func(T) uuid.UUID) error {
	if len(items) != len(orderedIDs) {
		return apperr.BadRequest("validation.invalid_request", "content_ids must include every item in the collection")
	}
	available := make(map[uuid.UUID]struct{}, len(items))
	for _, item := range items {
		available[idOf(item)] = struct{}{}
	}
	for _, id := range orderedIDs {
		if _, ok := available[id]; !ok {
			return apperr.BadRequest("validation.invalid_request", "content_ids contains an item outside this collection")
		}
	}
	return nil
}

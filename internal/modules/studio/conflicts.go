package studio

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type collectionConflictResolution struct {
	ContentID    uuid.UUID `json:"content_id"`
	CollectionID uuid.UUID `json:"collection_id"`
}

func (s *Service) ResolveCollectionConflicts(user authctx.CurrentUser, module Module, items []collectionConflictResolution) error {
	if err := requireUser(user); err != nil {
		return err
	}
	if len(items) == 0 {
		return apperr.BadRequest("validation.invalid_request", "items is required")
	}
	seen := make(map[uuid.UUID]struct{}, len(items))
	return s.db.Transaction(func(tx *gorm.DB) error {
		txService := *s
		txService.db = tx
		for _, item := range items {
			if item.ContentID == uuid.Nil || item.CollectionID == uuid.Nil {
				return apperr.BadRequest("validation.invalid_request", "content_id and collection_id are required")
			}
			if _, exists := seen[item.ContentID]; exists {
				return apperr.BadRequest("validation.invalid_request", "content_id must be unique")
			}
			seen[item.ContentID] = struct{}{}
			if err := txService.ResolveCollectionConflict(user, module, item.ContentID, item.CollectionID); err != nil {
				return err
			}
		}
		return recordStudioAudit(tx, user.ID, "studio.collection_conflicts_resolved", "collection_conflict_batch", uuid.Nil, map[string]any{"module": module, "content_count": len(items)})
	})
}

func (s *Service) ResolveCollectionConflict(user authctx.CurrentUser, module Module, contentID, collectionID uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	collection, err := s.collectionInModule(collectionID, module)
	if err != nil {
		return err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		switch module {
		case ModuleBlog:
			var post model.Post
			if err := tx.Preload("Collections").First(&post, "id = ? AND user_id = ?", contentID, user.ID).Error; err != nil {
				return contentNotFound(err)
			}
			if !post.CollectionConflict {
				return apperr.Conflict("studio.collection_not_conflicted", "content has no collection conflict")
			}
			if !containsCollection(post.Collections, collectionID) {
				return apperr.BadRequest("studio.invalid_collection", "collection is not a conflict candidate")
			}
			if err := tx.Model(&post).Updates(map[string]any{"collection_id": collectionID, "collection_conflict": false}).Error; err != nil {
				return err
			}
			if err := tx.Model(&post).Association("Collections").Replace([]model.Collection{collection}); err != nil {
				return err
			}
			return recordStudioAudit(tx, user.ID, "studio.collection_conflict_resolved", "post", post.ID, map[string]any{"module": module, "collection_id": collectionID.String()})
		case ModulePodcast:
			var episode model.PodcastEpisode
			if err := tx.Preload("Post.Collections").First(&episode, "id = ?", contentID).Error; err != nil {
				return contentNotFound(err)
			}
			if episode.Post == nil || episode.Post.UserID != user.ID {
				return apperr.NotFound("studio.content_not_found", "content not found")
			}
			if !episode.Post.CollectionConflict {
				return apperr.Conflict("studio.collection_not_conflicted", "content has no collection conflict")
			}
			if !containsCollection(episode.Post.Collections, collectionID) {
				return apperr.BadRequest("studio.invalid_collection", "collection is not a conflict candidate")
			}
			if err := tx.Model(episode.Post).Updates(map[string]any{"collection_id": collectionID, "collection_conflict": false}).Error; err != nil {
				return err
			}
			if err := tx.Model(episode.Post).Association("Collections").Replace([]model.Collection{collection}); err != nil {
				return err
			}
			return recordStudioAudit(tx, user.ID, "studio.collection_conflict_resolved", "podcast_episode", episode.ID, map[string]any{"module": module, "collection_id": collectionID.String()})
		case ModuleVideo:
			var video model.Video
			if err := tx.Preload("Collections").First(&video, "id = ? AND user_id = ?", contentID, user.ID).Error; err != nil {
				return contentNotFound(err)
			}
			if !video.CollectionConflict {
				return apperr.Conflict("studio.collection_not_conflicted", "content has no collection conflict")
			}
			if !containsCollection(video.Collections, collectionID) {
				return apperr.BadRequest("studio.invalid_collection", "collection is not a conflict candidate")
			}
			if err := tx.Model(&video).Updates(map[string]any{"collection_id": collectionID, "collection_conflict": false}).Error; err != nil {
				return err
			}
			if err := tx.Model(&video).Association("Collections").Replace([]model.Collection{collection}); err != nil {
				return err
			}
			return recordStudioAudit(tx, user.ID, "studio.collection_conflict_resolved", "video", video.ID, map[string]any{"module": module, "collection_id": collectionID.String()})
		default:
			return apperr.BadRequest("studio.invalid_module", "module must be blog, podcast, or video")
		}
	})
}

func containsCollection(collections []model.Collection, id uuid.UUID) bool {
	for _, collection := range collections {
		if collection.ID == id {
			return true
		}
	}
	return false
}
func contentNotFound(err error) error {
	if err == gorm.ErrRecordNotFound {
		return apperr.NotFound("studio.content_not_found", "content not found")
	}
	return err
}

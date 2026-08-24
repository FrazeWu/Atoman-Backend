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
	if module != ModuleBlog && module != ModulePodcast && module != ModuleVideo {
		return apperr.BadRequest("studio.invalid_module", "module must be blog, podcast, or video")
	}
	if module == ModuleBlog {
		return s.ReorderUnifiedCollectionContents(user, collectionID, orderedIDs)
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
		collection, err := s.collectionInModule(collectionID, module)
		if err != nil {
			return err
		}
		if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
			return err
		}
		type contentLink struct {
			ContentID uuid.UUID `gorm:"column:content_id"`
			PublicID  uuid.UUID `gorm:"column:public_id"`
		}
		var links []contentLink
		query := tx.Table("content_entries AS posts").
			Select("posts.id AS content_id, posts.id AS public_id").
			Joins("JOIN content_collection_memberships memberships ON memberships.content_id = posts.id").
			Where("memberships.collection_id = ? AND posts.author_id = ? AND posts.kind = ? AND posts.deleted_at IS NULL", collectionID, user.ID, string(module))
		switch module {
		case ModulePodcast:
			query = query.Joins("JOIN content_episode_extensions episodes ON episodes.content_id = posts.id").Select("posts.id AS content_id, episodes.episode_id AS public_id")
		case ModuleVideo:
			query = query.Joins("JOIN content_video_extensions videos ON videos.content_id = posts.id").Select("posts.id AS content_id, videos.video_id AS public_id")
		}
		if err := query.Find(&links).Error; err != nil {
			return err
		}
		if err := requireExactOrder(links, orderedIDs, func(link contentLink) uuid.UUID { return link.PublicID }); err != nil {
			return err
		}
		contentByPublicID := make(map[uuid.UUID]uuid.UUID, len(links))
		for _, link := range links {
			contentByPublicID[link.PublicID] = link.ContentID
		}
		for position, publicID := range orderedIDs {
			if err := tx.Model(&model.ContentCollectionMembership{}).
				Where("content_id = ? AND collection_id = ?", contentByPublicID[publicID], collectionID).
				Update("position", position).Error; err != nil {
				return err
			}
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

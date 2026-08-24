package studio

import (
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ListUnifiedCollectionContents returns every active member in the persisted cross-module order.
func (s *Service) ListUnifiedCollectionContents(user authctx.CurrentUser, collectionID uuid.UUID) ([]StudioCollectionContentItem, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}

	var collection model.ContentCollection
	if err := s.db.First(&collection, "id = ?", collectionID).Error; err != nil {
		return nil, err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return nil, err
	}

	var items []StudioCollectionContentItem
	err := s.db.Table("content_collection_memberships AS memberships").
		Joins("JOIN content_entries AS posts ON posts.id = memberships.content_id").
		Joins("LEFT JOIN content_episode_extensions AS episodes ON episodes.content_id = posts.id").
		Joins("LEFT JOIN content_video_extensions AS videos ON videos.content_id = posts.id").
		Select(`memberships.content_id,
			COALESCE(episodes.episode_id, videos.video_id, posts.id) AS id,
			posts.kind AS module, posts.title, posts.status, memberships.position,
			CASE
				WHEN posts.kind = 'podcast' THEN COALESCE(NULLIF(episodes.episode_cover_url, ''), posts.cover_url)
				WHEN posts.kind = 'video' THEN COALESCE(NULLIF(videos.thumbnail_url, ''), posts.cover_url)
				ELSE posts.cover_url
			END AS cover_url,
			CASE
				WHEN posts.kind = 'podcast' THEN GREATEST(posts.updated_at, episodes.updated_at)
				WHEN posts.kind = 'video' THEN GREATEST(posts.updated_at, videos.updated_at)
				ELSE posts.updated_at
			END AS updated_at`).
		Where("memberships.collection_id = ? AND posts.deleted_at IS NULL", collectionID).
		Order("memberships.position ASC, posts.id ASC").
		Scan(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) ListUnifiedCollectionCandidates(user authctx.CurrentUser, collectionID uuid.UUID, search string) ([]StudioCollectionContentCandidate, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}
	var collection model.ContentCollection
	if err := s.db.First(&collection, "id = ?", collectionID).Error; err != nil {
		return nil, err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return nil, err
	}
	var candidates []StudioCollectionContentCandidate
	query := s.db.Table("content_entries AS posts").
		Joins("LEFT JOIN content_episode_extensions AS episodes ON episodes.content_id = posts.id").
		Joins("LEFT JOIN content_video_extensions AS videos ON videos.content_id = posts.id").
		Select(`posts.id AS content_id, COALESCE(episodes.episode_id, videos.video_id, posts.id) AS id,
			posts.kind AS module, posts.title, posts.status,
			CASE
				WHEN posts.kind = 'podcast' THEN COALESCE(NULLIF(episodes.episode_cover_url, ''), posts.cover_url)
				WHEN posts.kind = 'video' THEN COALESCE(NULLIF(videos.thumbnail_url, ''), posts.cover_url)
				ELSE posts.cover_url
			END AS cover_url`).
		Where("posts.channel_id = ? AND posts.author_id = ? AND posts.deleted_at IS NULL", collection.ChannelID, user.ID)
	if value := strings.TrimSpace(search); value != "" {
		query = query.Where("posts.title ILIKE ?", "%"+value+"%")
	}
	if err := query.Order("posts.updated_at DESC, posts.id DESC").Limit(50).Scan(&candidates).Error; err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []StudioCollectionContentCandidate{}, nil
	}
	contentIDs := make([]uuid.UUID, 0, len(candidates))
	for _, candidate := range candidates {
		contentIDs = append(contentIDs, candidate.ContentID)
	}
	var memberships []model.ContentCollectionMembership
	if err := s.db.Where("content_id IN ?", contentIDs).Order("position ASC").Find(&memberships).Error; err != nil {
		return nil, err
	}
	collectionIDs := make([]uuid.UUID, 0, len(memberships))
	membershipByContent := make(map[uuid.UUID]model.ContentCollectionMembership, len(memberships))
	for _, membership := range memberships {
		if _, exists := membershipByContent[membership.ContentID]; exists {
			continue
		}
		membershipByContent[membership.ContentID] = membership
		collectionIDs = append(collectionIDs, membership.CollectionID)
	}
	var collections []model.ContentCollection
	if len(collectionIDs) > 0 {
		if err := s.db.Select("id", "name").Where("id IN ?", collectionIDs).Find(&collections).Error; err != nil {
			return nil, err
		}
	}
	collectionNames := make(map[uuid.UUID]string, len(collections))
	for _, current := range collections {
		collectionNames[current.ID] = current.Name
	}
	for index := range candidates {
		if membership, exists := membershipByContent[candidates[index].ContentID]; exists {
			candidates[index].CurrentCollectionID = membership.CollectionID
			candidates[index].CurrentCollectionName = collectionNames[membership.CollectionID]
		}
	}
	return candidates, nil
}

func (s *Service) AddUnifiedCollectionContent(user authctx.CurrentUser, collectionID, contentID uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	var collection model.ContentCollection
	if err := s.db.First(&collection, "id = ?", collectionID).Error; err != nil {
		return err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return err
	}
	var content model.ContentEntry
	if err := s.db.Where("id = ? AND channel_id = ? AND author_id = ? AND deleted_at IS NULL", contentID, collection.ChannelID, user.ID).First(&content).Error; err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var maxPosition int
		if err := tx.Model(&model.ContentCollectionMembership{}).
			Where("collection_id = ?", collection.ID).
			Select("COALESCE(MAX(position), -1)").Scan(&maxPosition).Error; err != nil {
			return err
		}
		if err := tx.Where("content_id = ?", content.ID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.ContentCollectionMembership{ContentID: content.ID, CollectionID: collection.ID, Position: maxPosition + 1}).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.collection_content_moved", "collection", collection.ID, map[string]any{"content_id": content.ID})
	})
}

func (s *Service) RemoveUnifiedCollectionContent(user authctx.CurrentUser, collectionID, contentID uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	var collection model.ContentCollection
	if err := s.db.First(&collection, "id = ?", collectionID).Error; err != nil {
		return err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return err
	}
	result := s.db.Where("collection_id = ? AND content_id = ?", collection.ID, contentID).Delete(&model.ContentCollectionMembership{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("studio.collection_content_not_found", "content is not in this collection")
	}
	return recordStudioAudit(s.db, user.ID, "studio.collection_content_removed", "collection", collection.ID, map[string]any{"content_id": contentID})
}

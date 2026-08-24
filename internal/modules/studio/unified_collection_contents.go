package studio

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
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

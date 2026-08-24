package studio

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type dashboardRecentRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	ChannelID uuid.UUID `gorm:"column:channel_id"`
	Title     string    `gorm:"column:title"`
	CoverURL  string    `gorm:"column:cover_url"`
	Status    string    `gorm:"column:status"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

// dashboardRecentContents intentionally selects only fields rendered by the overview.
// Every module uses the same effective update timestamp before the five-item limit is applied.
func (s *Service) dashboardRecentContents(userID, channelID uuid.UUID, module Module, limit int) ([]StudioContentItem, error) {
	if limit < 1 {
		return []StudioContentItem{}, nil
	}

	var rows []dashboardRecentRow
	var err error
	switch module {
	case ModuleBlog:
		err = s.db.Table("content_entries AS posts").
			Select("posts.id, posts.channel_id, posts.title, posts.cover_url, posts.status, posts.created_at, posts.updated_at").
			Where("posts.kind = ? AND posts.deleted_at IS NULL AND posts.author_id = ? AND posts.channel_id = ?", "blog", userID, channelID).
			Order("posts.updated_at DESC, posts.id DESC").
			Limit(limit).
			Scan(&rows).Error
	case ModulePodcast:
		err = s.db.Table("content_entries AS posts").
			Joins("JOIN content_episode_extensions AS episodes ON episodes.content_id = posts.id").
			Select(`episodes.episode_id AS id, posts.channel_id, posts.title,
				COALESCE(NULLIF(episodes.episode_cover_url, ''), posts.cover_url) AS cover_url,
				posts.status, posts.created_at,
				GREATEST(posts.updated_at, episodes.updated_at) AS updated_at`).
			Where("posts.kind = ? AND posts.deleted_at IS NULL AND posts.author_id = ? AND posts.channel_id = ?", "podcast", userID, channelID).
			Order("GREATEST(posts.updated_at, episodes.updated_at) DESC, episodes.episode_id DESC").
			Limit(limit).
			Scan(&rows).Error
	case ModuleVideo:
		err = s.db.Table("content_entries AS posts").
			Joins("JOIN content_video_extensions AS videos ON videos.content_id = posts.id").
			Select(`videos.video_id AS id, posts.channel_id, posts.title,
				COALESCE(NULLIF(videos.thumbnail_url, ''), posts.cover_url) AS cover_url,
				posts.status, posts.created_at,
				GREATEST(posts.updated_at, videos.updated_at) AS updated_at`).
			Where("posts.kind = ? AND posts.deleted_at IS NULL AND posts.author_id = ? AND posts.channel_id = ?", "video", userID, channelID).
			Order("GREATEST(posts.updated_at, videos.updated_at) DESC, videos.video_id DESC").
			Limit(limit).
			Scan(&rows).Error
	default:
		return nil, fmt.Errorf("invalid Studio module %q", module)
	}
	if err != nil {
		return nil, err
	}

	items := make([]StudioContentItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, StudioContentItem{
			ID: row.ID, Module: module, ChannelID: row.ChannelID, Title: row.Title,
			CoverURL: row.CoverURL, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			Collections: []StudioCollectionSummary{},
		})
	}
	return items, nil
}

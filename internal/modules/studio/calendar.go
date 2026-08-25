package studio

import (
	"strings"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

const maxCalendarRange = 93 * 24 * time.Hour

type studioCalendarRow struct {
	ContentID        uuid.UUID `gorm:"column:content_id"`
	ID               uuid.UUID `gorm:"column:id"`
	Module           Module    `gorm:"column:module"`
	Title            string    `gorm:"column:title"`
	ScheduledAt      time.Time `gorm:"column:scheduled_at"`
	CoverURL         string    `gorm:"column:cover_url"`
	HasCollection    bool      `gorm:"column:has_collection"`
	AudioURL         string    `gorm:"column:audio_url"`
	StorageType      string    `gorm:"column:storage_type"`
	VideoURL         string    `gorm:"column:video_url"`
	ProcessingStatus string    `gorm:"column:processing_status"`
}

func (s *Service) ListCalendar(user authctx.CurrentUser, query StudioCalendarQuery) ([]StudioCalendarItem, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}
	if query.From.IsZero() || query.To.IsZero() || !query.From.Before(query.To) {
		return nil, apperr.BadRequest("studio.invalid_calendar_range", "from and to must define a valid range")
	}
	if query.To.Sub(query.From) > maxCalendarRange {
		return nil, apperr.BadRequest("studio.invalid_calendar_range", "calendar range must not exceed 93 days")
	}
	channel, err := s.resolveContentChannel(user.ID, query.ChannelID)
	if err != nil {
		return nil, err
	}
	var rows []studioCalendarRow
	if err := s.db.Table("content_entries AS posts").
		Select(`posts.id AS content_id, COALESCE(episodes.episode_id, videos.video_id, posts.id) AS id,
			posts.kind AS module, posts.title, posts.scheduled_at,
			COALESCE(NULLIF(episodes.episode_cover_url, ''), NULLIF(videos.thumbnail_url, ''), posts.cover_url) AS cover_url,
			EXISTS (SELECT 1 FROM content_collection_memberships memberships WHERE memberships.content_id = posts.id) AS has_collection,
			COALESCE(episodes.audio_url, '') AS audio_url, COALESCE(videos.storage_type, '') AS storage_type,
			COALESCE(videos.video_url, '') AS video_url, COALESCE(videos.processing_status, '') AS processing_status`).
		Joins("LEFT JOIN content_episode_extensions AS episodes ON episodes.content_id = posts.id").
		Joins("LEFT JOIN content_video_extensions AS videos ON videos.content_id = posts.id").
		Where("posts.channel_id = ? AND posts.author_id = ? AND posts.status = ? AND posts.scheduled_at >= ? AND posts.scheduled_at < ? AND posts.deleted_at IS NULL", channel.ID, user.ID, "scheduled", query.From.UTC(), query.To.UTC()).
		Order("posts.scheduled_at ASC, posts.id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]StudioCalendarItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, StudioCalendarItem{
			ContentID:   row.ContentID,
			ID:          row.ID,
			Module:      row.Module,
			Title:       row.Title,
			ScheduledAt: row.ScheduledAt,
			Preflight:   calendarPreflight(row),
		})
	}
	return items, nil
}

func calendarPreflight(row studioCalendarRow) []StudioPreflightIssue {
	issues := make([]StudioPreflightIssue, 0, 4)
	add := func(code string, invalid bool) {
		if invalid {
			issues = append(issues, StudioPreflightIssue{Code: code})
		}
	}
	add("missing_title", strings.TrimSpace(row.Title) == "")
	add("missing_cover", strings.TrimSpace(row.CoverURL) == "")
	add("missing_collection", !row.HasCollection)
	switch row.Module {
	case ModulePodcast:
		add("missing_audio", strings.TrimSpace(row.AudioURL) == "")
	case ModuleVideo:
		add("processing_failed", row.ProcessingStatus == "failed")
		add("external_unplayable", row.StorageType == "external" && strings.TrimSpace(row.VideoURL) == "")
	}
	return issues
}

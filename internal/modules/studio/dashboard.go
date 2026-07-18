package studio

import (
	"fmt"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
)

type dashboardCounts struct {
	Contents           int64
	Published          int64
	Drafts             int64
	Views              int64
	MissingCover       int64
	MissingCollection  int64
	MissingAudio       int64
	ProcessingFailed   int64
	ExternalUnplayable int64
}

func (s *Service) GetDashboard(user authctx.CurrentUser, channelID uuid.UUID) (DashboardResponse, error) {
	if err := requireUser(user); err != nil {
		return DashboardResponse{}, err
	}
	channel, err := s.resolveContentChannel(user.ID, channelID)
	if err != nil {
		return DashboardResponse{}, err
	}
	subscriberCount, err := s.channelSubscriberCount(channel.ID)
	if err != nil {
		return DashboardResponse{}, err
	}
	response := DashboardResponse{
		ChannelSubscriberCount: subscriberCount,
		Sections:               make([]DashboardSection, 0, 3),
	}
	failed := 0
	var firstErr error
	for _, module := range []Module{ModuleBlog, ModulePodcast, ModuleVideo} {
		section, sectionErr := s.dashboardSection(user.ID, channel, module)
		if sectionErr != nil {
			failed++
			if firstErr == nil {
				firstErr = sectionErr
			}
			section = DashboardSection{
				Module: module, Metrics: map[string]int64{}, Recent: []StudioContentItem{}, Issues: []StudioContentIssue{},
				Error: fmt.Sprintf("Failed to load %s dashboard", module),
			}
		}
		response.Sections = append(response.Sections, section)
	}
	if failed == 3 {
		return DashboardResponse{}, firstErr
	}
	return response, nil
}

func (s *Service) dashboardSection(userID uuid.UUID, channel model.Channel, module Module) (DashboardSection, error) {
	query := ContentQuery{ChannelID: channel.ID, Page: 1, PageSize: 3}
	recent, _, err := s.listContentsForChannel(userID, module, query)
	if err != nil {
		return DashboardSection{}, err
	}
	counts, err := s.dashboardCounts(userID, channel, module)
	if err != nil {
		return DashboardSection{}, err
	}
	metrics := map[string]int64{
		"contents": counts.Contents, "published": counts.Published, "drafts": counts.Drafts,
	}
	if module == ModulePodcast {
		metrics["plays"] = counts.Views
	} else {
		metrics["views"] = counts.Views
	}
	issues := make([]StudioContentIssue, 0, 5)
	appendIssue := func(code string, count int64) {
		if count > 0 {
			issues = append(issues, StudioContentIssue{Code: code, Count: count})
		}
	}
	appendIssue("draft", counts.Drafts)
	appendIssue("missing_cover", counts.MissingCover)
	appendIssue("missing_collection", counts.MissingCollection)
	appendIssue("missing_audio", counts.MissingAudio)
	appendIssue("processing_failed", counts.ProcessingFailed)
	appendIssue("external_unplayable", counts.ExternalUnplayable)
	return DashboardSection{Module: module, Metrics: metrics, Recent: recent, Issues: issues}, nil
}

func (s *Service) dashboardCounts(userID uuid.UUID, channel model.Channel, module Module) (dashboardCounts, error) {
	var counts dashboardCounts
	var err error
	switch module {
	case ModuleBlog:
		err = s.db.Model(&model.Post{}).
			Select(`COUNT(*) AS contents,
				COALESCE(SUM(CASE WHEN posts.status = 'published' THEN 1 ELSE 0 END), 0) AS published,
				COALESCE(SUM(CASE WHEN posts.status = 'draft' THEN 1 ELSE 0 END), 0) AS drafts,
				COALESCE(SUM(posts.view_count), 0) AS views,
				COALESCE(SUM(CASE WHEN TRIM(COALESCE(posts.cover_url, '')) = '' THEN 1 ELSE 0 END), 0) AS missing_cover,
				COALESCE(SUM(CASE WHEN posts.collection_id IS NULL THEN 1 ELSE 0 END), 0) AS missing_collection`).
			Where("posts.user_id = ? AND posts.channel_id = ?", userID, channel.ID).
			Where("NOT EXISTS (SELECT 1 FROM podcast_episodes WHERE podcast_episodes.post_id = posts.id AND podcast_episodes.deleted_at IS NULL)").
			Scan(&counts).Error
	case ModulePodcast:
		err = s.db.Model(&model.PodcastEpisode{}).
			Select(`COUNT(*) AS contents,
				COALESCE(SUM(CASE WHEN posts.status = 'published' THEN 1 ELSE 0 END), 0) AS published,
				COALESCE(SUM(CASE WHEN posts.status = 'draft' THEN 1 ELSE 0 END), 0) AS drafts,
				COALESCE(SUM(posts.view_count), 0) AS views,
				COALESCE(SUM(CASE WHEN TRIM(COALESCE(podcast_episodes.episode_cover_url, '')) = '' AND ? = '' THEN 1 ELSE 0 END), 0) AS missing_cover,
				COALESCE(SUM(CASE WHEN posts.collection_id IS NULL AND NOT EXISTS (SELECT 1 FROM post_collections WHERE post_collections.post_id = posts.id) THEN 1 ELSE 0 END), 0) AS missing_collection,
				COALESCE(SUM(CASE WHEN TRIM(COALESCE(podcast_episodes.audio_url, '')) = '' THEN 1 ELSE 0 END), 0) AS missing_audio`, strings.TrimSpace(channel.CoverURL)).
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.deleted_at IS NULL").
			Where("posts.user_id = ? AND podcast_episodes.channel_id = ?", userID, channel.ID).
			Scan(&counts).Error
	case ModuleVideo:
		err = s.db.Model(&model.Video{}).
			Select(`COUNT(*) AS contents,
				COALESCE(SUM(CASE WHEN videos.status = 'published' THEN 1 ELSE 0 END), 0) AS published,
				COALESCE(SUM(CASE WHEN videos.status = 'draft' THEN 1 ELSE 0 END), 0) AS drafts,
				COALESCE(SUM(videos.view_count), 0) AS views,
				COALESCE(SUM(CASE WHEN TRIM(COALESCE(videos.thumbnail_url, '')) = '' THEN 1 ELSE 0 END), 0) AS missing_cover,
				COALESCE(SUM(CASE WHEN NOT EXISTS (SELECT 1 FROM video_collections WHERE video_collections.video_id = videos.id) THEN 1 ELSE 0 END), 0) AS missing_collection,
				COALESCE(SUM(CASE WHEN videos.processing_status = 'failed' THEN 1 ELSE 0 END), 0) AS processing_failed,
				COALESCE(SUM(CASE WHEN videos.storage_type = 'external' AND TRIM(COALESCE(videos.video_url, '')) = '' THEN 1 ELSE 0 END), 0) AS external_unplayable`).
			Where("videos.user_id = ? AND videos.channel_id = ?", userID, channel.ID).
			Scan(&counts).Error
	default:
		return dashboardCounts{}, fmt.Errorf("invalid Studio module %q", module)
	}
	return counts, err
}

func (s *Service) channelSubscriberCount(channelID uuid.UUID) (int64, error) {
	var count int64
	err := s.db.Model(&model.Subscription{}).
		Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id AND feed_sources.deleted_at IS NULL").
		Where("subscriptions.deleted_at IS NULL AND feed_sources.source_type = ? AND feed_sources.source_id = ?", "internal_channel", channelID).
		Count(&count).Error
	return count, err
}

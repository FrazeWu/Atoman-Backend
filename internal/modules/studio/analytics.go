package studio

import (
	"errors"
	"sort"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var metricNamesByModule = map[Module][]string{
	ModuleBlog:    {"view", "comment", "like", "bookmark", "share"},
	ModulePodcast: {"play", "complete", "comment", "bookmark", "share"},
	ModuleVideo:   {"play", "comment", "like", "bookmark", "share"},
}

func (s *Service) GetAnalytics(user authctx.CurrentUser, module Module, query AnalyticsQuery) (AnalyticsResponse, error) {
	if err := requireUser(user); err != nil {
		return AnalyticsResponse{}, err
	}
	if _, err := ParseModule(string(module)); err != nil {
		return AnalyticsResponse{}, err
	}
	days := query.Range
	if days == 0 {
		days = 28
	}
	if days != 7 && days != 28 && days != 90 {
		return AnalyticsResponse{}, apperr.BadRequest("studio.invalid_analytics_range", "range must be 7, 28, or 90")
	}
	channel, err := s.resolveContentChannel(user.ID, query.ChannelID)
	if err != nil {
		return AnalyticsResponse{}, err
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -(days - 1))
	to := today.AddDate(0, 0, 1)
	metrics := metricNamesByModule[module]
	totals := emptyMetricMap(metrics)
	trend := make([]AnalyticsPoint, 0, days)
	pointByDate := make(map[string]map[string]int64, days)
	for index := 0; index < days; index++ {
		date := from.AddDate(0, 0, index).Format("2006-01-02")
		pointMetrics := emptyMetricMap(metrics)
		pointByDate[date] = pointMetrics
		trend = append(trend, AnalyticsPoint{Date: date, Metrics: pointMetrics})
	}
	titles, contentMetrics, err := s.analyticsContentMetrics(user.ID, channel.ID, module)
	if err != nil {
		return AnalyticsResponse{}, err
	}
	var events []model.StudioMetricEvent
	if err := s.db.Where(
		"channel_id = ? AND content_type = ? AND created_at >= ? AND created_at < ?",
		channel.ID, module, from, to,
	).Find(&events).Error; err != nil {
		return AnalyticsResponse{}, err
	}
	for _, event := range events {
		addAnalyticsMetric(event.Metric, event.ContentID, event.CreatedAt, totals, pointByDate, contentMetrics)
	}
	if err := s.addExistingAnalytics(module, titles, from, to, totals, pointByDate, contentMetrics); err != nil {
		return AnalyticsResponse{}, err
	}
	return AnalyticsResponse{
		Range: days, From: from, To: to, Totals: totals, Trend: trend,
		Top: rankedContentMetrics(titles, contentMetrics),
	}, nil
}

func (s *Service) RecordMetricEvent(channelID uuid.UUID, module Module, contentID uuid.UUID, metric string) error {
	if channelID == uuid.Nil || contentID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "channel_id and content_id are required")
	}
	if _, err := ParseModule(string(module)); err != nil {
		return err
	}
	if !containsMetric(metricNamesByModule[module], metric) {
		return apperr.BadRequest("studio.invalid_metric", "metric is not supported for this module")
	}
	return s.db.Create(&model.StudioMetricEvent{
		ChannelID: channelID, ContentType: string(module), ContentID: contentID, Metric: metric,
	}).Error
}

func (s *Service) ShareContent(user authctx.CurrentUser, module Module, channelID, contentID uuid.UUID) (ShareResponse, error) {
	if err := requireUser(user); err != nil {
		return ShareResponse{}, err
	}
	if _, err := ParseModule(string(module)); err != nil {
		return ShareResponse{}, err
	}
	channel, err := s.resolveContentChannel(user.ID, channelID)
	if err != nil {
		return ShareResponse{}, err
	}
	visibility, status, path, err := s.shareableContent(user.ID, channel.ID, module, contentID)
	if err != nil {
		return ShareResponse{}, err
	}
	if status != "published" || visibility == "private" {
		return ShareResponse{}, apperr.BadRequest("studio.content_not_shareable", "Only published non-private content can be shared")
	}
	if err := s.RecordMetricEvent(channel.ID, module, contentID, "share"); err != nil {
		return ShareResponse{}, err
	}
	return ShareResponse{Path: path}, nil
}

func (s *Service) shareableContent(userID, channelID uuid.UUID, module Module, contentID uuid.UUID) (string, string, string, error) {
	switch module {
	case ModuleBlog:
		var post model.Post
		if err := s.db.First(&post, "id = ?", contentID).Error; err != nil {
			return "", "", "", contentLookupError(err)
		}
		if post.UserID != userID || post.ChannelID == nil || *post.ChannelID != channelID {
			return "", "", "", apperr.Forbidden("studio.content_forbidden", "You do not have permission to share this content")
		}
		var episodeCount int64
		if err := s.db.Model(&model.PodcastEpisode{}).Where("post_id = ?", post.ID).Count(&episodeCount).Error; err != nil {
			return "", "", "", err
		}
		if episodeCount > 0 {
			return "", "", "", apperr.BadRequest("studio.content_module_mismatch", "Content does not belong to this module")
		}
		return post.Visibility, post.Status, "/post/" + post.ID.String(), nil
	case ModulePodcast:
		var episode model.PodcastEpisode
		if err := s.db.Preload("Post").First(&episode, "id = ?", contentID).Error; err != nil {
			return "", "", "", contentLookupError(err)
		}
		if episode.Post == nil || episode.Post.UserID != userID || episode.ChannelID != channelID {
			return "", "", "", apperr.Forbidden("studio.content_forbidden", "You do not have permission to share this content")
		}
		return episode.Post.Visibility, episode.Post.Status, "/podcasts/episode/" + episode.ID.String(), nil
	case ModuleVideo:
		var video model.Video
		if err := s.db.First(&video, "id = ?", contentID).Error; err != nil {
			return "", "", "", contentLookupError(err)
		}
		if video.UserID != userID || video.ChannelID == nil || *video.ChannelID != channelID {
			return "", "", "", apperr.Forbidden("studio.content_forbidden", "You do not have permission to share this content")
		}
		return video.Visibility, video.Status, "/videos/watch/" + video.ID.String(), nil
	default:
		return "", "", "", apperr.BadRequest("studio.invalid_module", "module must be blog, podcast, or video")
	}
}

func contentLookupError(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("studio.content_not_found", "Content not found")
	}
	return err
}

func (s *Service) analyticsContentMetrics(userID, channelID uuid.UUID, module Module) (map[uuid.UUID]string, map[uuid.UUID]map[string]int64, error) {
	_, titles, err := s.interactionContentTitles(userID, channelID, module)
	if err != nil {
		return nil, nil, err
	}
	metrics := make(map[uuid.UUID]map[string]int64, len(titles))
	for id := range titles {
		metrics[id] = emptyMetricMap(metricNamesByModule[module])
	}
	return titles, metrics, nil
}

func (s *Service) addExistingAnalytics(
	module Module,
	titles map[uuid.UUID]string,
	from, to time.Time,
	totals map[string]int64,
	points map[string]map[string]int64,
	contentMetrics map[uuid.UUID]map[string]int64,
) error {
	if len(titles) == 0 {
		return nil
	}
	contentIDs := make([]uuid.UUID, 0, len(titles))
	for id := range titles {
		contentIDs = append(contentIDs, id)
	}
	targetKind := map[Module]string{ModuleBlog: "blog_post", ModulePodcast: "podcast_episode", ModuleVideo: "video"}[module]
	var targets []model.DiscussionTarget
	if err := s.db.Select("id", "resource_id").Where("kind = ? AND resource_id IN ?", targetKind, contentIDs).Find(&targets).Error; err != nil {
		return err
	}
	targetResource := make(map[uuid.UUID]uuid.UUID, len(targets))
	targetIDs := make([]uuid.UUID, 0, len(targets))
	for _, target := range targets {
		targetResource[target.ID] = target.ResourceID
		targetIDs = append(targetIDs, target.ID)
	}
	if len(targetIDs) > 0 {
		var comments []model.CommentEntry
		if err := s.db.Select("target_id", "created_at").Where("target_id IN ? AND status = ? AND created_at >= ? AND created_at < ?", targetIDs, "active", from, to).Find(&comments).Error; err != nil {
			return err
		}
		for _, comment := range comments {
			addAnalyticsMetric("comment", targetResource[comment.TargetID], comment.CreatedAt, totals, points, contentMetrics)
		}
	}
	return s.addExistingReactions(module, contentIDs, from, to, totals, points, contentMetrics)
}

func (s *Service) addExistingReactions(
	module Module,
	contentIDs []uuid.UUID,
	from, to time.Time,
	totals map[string]int64,
	points map[string]map[string]int64,
	contentMetrics map[uuid.UUID]map[string]int64,
) error {
	switch module {
	case ModuleBlog:
		var likes []model.Like
		if err := s.db.Select("target_id", "created_at").Where("target_type = ? AND target_id IN ? AND created_at >= ? AND created_at < ?", "post", contentIDs, from, to).Find(&likes).Error; err != nil {
			return err
		}
		for _, like := range likes {
			addAnalyticsMetric("like", like.TargetID, like.CreatedAt, totals, points, contentMetrics)
		}
		var bookmarks []model.Bookmark
		if err := s.db.Select("post_id", "created_at").Where("post_id IN ? AND created_at >= ? AND created_at < ?", contentIDs, from, to).Find(&bookmarks).Error; err != nil {
			return err
		}
		for _, bookmark := range bookmarks {
			addAnalyticsMetric("bookmark", bookmark.PostID, bookmark.CreatedAt, totals, points, contentMetrics)
		}
	case ModulePodcast:
		var bookmarks []model.PodcastEpisodeBookmark
		if err := s.db.Select("episode_id", "created_at").Where("episode_id IN ? AND created_at >= ? AND created_at < ?", contentIDs, from, to).Find(&bookmarks).Error; err != nil {
			return err
		}
		for _, bookmark := range bookmarks {
			addAnalyticsMetric("bookmark", bookmark.EpisodeID, bookmark.CreatedAt, totals, points, contentMetrics)
		}
	case ModuleVideo:
		var bookmarks []model.VideoBookmark
		if err := s.db.Select("video_id", "created_at").Where("video_id IN ? AND created_at >= ? AND created_at < ?", contentIDs, from, to).Find(&bookmarks).Error; err != nil {
			return err
		}
		for _, bookmark := range bookmarks {
			addAnalyticsMetric("bookmark", bookmark.VideoID, bookmark.CreatedAt, totals, points, contentMetrics)
		}
		var likes []model.Like
		if err := s.db.Select("target_id", "created_at").Where("target_type = ? AND target_id IN ? AND created_at >= ? AND created_at < ?", "video", contentIDs, from, to).Find(&likes).Error; err != nil {
			return err
		}
		for _, like := range likes {
			addAnalyticsMetric("like", like.TargetID, like.CreatedAt, totals, points, contentMetrics)
		}
	}
	return nil
}

func addAnalyticsMetric(
	metric string,
	contentID uuid.UUID,
	createdAt time.Time,
	totals map[string]int64,
	points map[string]map[string]int64,
	contentMetrics map[uuid.UUID]map[string]int64,
) {
	if _, exists := totals[metric]; !exists {
		return
	}
	date := createdAt.UTC().Format("2006-01-02")
	point, exists := points[date]
	if !exists {
		return
	}
	totals[metric]++
	point[metric]++
	if metrics, exists := contentMetrics[contentID]; exists {
		metrics[metric]++
	}
}

func rankedContentMetrics(titles map[uuid.UUID]string, metrics map[uuid.UUID]map[string]int64) []AnalyticsContentMetric {
	items := make([]AnalyticsContentMetric, 0, len(metrics))
	for id, values := range metrics {
		items = append(items, AnalyticsContentMetric{ID: id, Title: titles[id], Metrics: values})
	}
	sort.Slice(items, func(left, right int) bool {
		leftTotal, rightTotal := int64(0), int64(0)
		for _, value := range items[left].Metrics {
			leftTotal += value
		}
		for _, value := range items[right].Metrics {
			rightTotal += value
		}
		if leftTotal == rightTotal {
			return items[left].Title < items[right].Title
		}
		return leftTotal > rightTotal
	})
	if len(items) > 10 {
		items = items[:10]
	}
	return items
}

func emptyMetricMap(names []string) map[string]int64 {
	result := make(map[string]int64, len(names))
	for _, name := range names {
		result[name] = 0
	}
	return result
}

func containsMetric(metrics []string, value string) bool {
	for _, metric := range metrics {
		if metric == value {
			return true
		}
	}
	return false
}

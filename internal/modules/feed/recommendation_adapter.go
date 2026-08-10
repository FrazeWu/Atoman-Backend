package feed

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	recommendationArticleCandidateLimit         = 5000
	recommendationInternalArticleCandidateLimit = 2500
)

func parseRecommendationMode(raw string) (recommendation.Mode, error) {
	switch recommendation.Mode(strings.TrimSpace(strings.ToLower(raw))) {
	case recommendation.ModeHot:
		return recommendation.ModeHot, nil
	case recommendation.ModeFeatured:
		return recommendation.ModeFeatured, nil
	case recommendation.ModeDiscover:
		return recommendation.ModeDiscover, nil
	default:
		return "", apperr.BadRequest("validation.invalid_request", "mode must be one of hot, featured, discover")
	}
}

func (s *Service) RecommendArticles(mode recommendation.Mode, category string, theme string, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
	normalizedCategory := normalizeSourceCategory(category)
	keywords, validTheme := recommendationThemeKeywords(normalizedCategory, theme)
	if !validTheme {
		return []RecommendationItemDTO{}, 0, nil
	}
	includeText := len(keywords) > 0
	publishedAfter := time.Now().Add(-recommendationArticleCandidateWindow(mode))

	posts := []RecommendationArticlePostRow{}
	if normalizedCategory == "blog" {
		var err error
		posts, err = s.repo.ListRecommendationArticlePosts(includeText, publishedAfter, keywords, recommendationInternalArticleCandidateLimit)
		if err != nil {
			return nil, 0, err
		}
	}
	feedItemLimit := recommendationArticleCandidateLimit - len(posts)
	feedItems, err := s.repo.ListRecommendationArticleFeedItems(includeText, normalizedCategory, publishedAfter, keywords, feedItemLimit)
	if err != nil {
		return nil, 0, err
	}

	candidates := make([]recommendation.Candidate, 0, len(posts)+len(feedItems))
	postByID := make(map[string]RecommendationArticlePostRow, len(posts))
	feedItemByID := make(map[string]RecommendationArticleFeedItemRow, len(feedItems))
	for _, post := range posts {
		candidate := recommendation.Candidate{
			Module:          "feed",
			EntityType:      recommendation.EntityArticle,
			EntityID:        post.ID.String(),
			SourceKey:       recommendationSourceKeyForPost(post),
			QualityScore:    normalizeArticleQuality(post),
			TrendScore:      0.5,
			FreshnessScore:  0.5,
			AuthorityScore:  normalizeArticleAuthority(post),
			ExposureScore:   0,
			EditorialScore:  0,
			PublishedAtUnix: post.CreatedAt.Unix(),
		}
		candidates = append(candidates, candidate)
		postByID[candidate.EntityID] = post
	}
	for _, feedItem := range feedItems {
		candidate := recommendation.Candidate{
			Module:          "feed",
			EntityType:      recommendation.EntityArticle,
			EntityID:        feedItem.ID.String(),
			SourceKey:       recommendationSourceKeyForFeedItem(feedItem),
			QualityScore:    normalizeFeedItemQuality(feedItem),
			TrendScore:      normalizeFeedItemTrend(feedItem),
			FreshnessScore:  normalizePostRecency(feedItem.PublishedAt, 14*24*time.Hour),
			AuthorityScore:  normalizeFeedItemAuthority(feedItem),
			ExposureScore:   0,
			EditorialScore:  0,
			PublishedAtUnix: feedItem.PublishedAt.Unix(),
		}
		candidates = append(candidates, candidate)
		feedItemByID[candidate.EntityID] = feedItem
	}

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		post, ok := postByID[item.EntityID]
		if ok {
			items = append(items, RecommendationItemDTO{
				ID:          post.ID.String(),
				Title:       post.Title,
				Summary:     post.Summary,
				ContentType: "blog",
				TargetPath:  "/posts/post/" + post.ID.String(),
				ScoreLabel:  recommendationScoreLabel(mode, item.FinalScore),
			})
			continue
		}
		feedItem, ok := feedItemByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, RecommendationItemDTO{
			ID:          feedItem.ID.String(),
			Title:       feedItem.Title,
			Summary:     feedItem.Summary,
			ContentType: inferFeedItemRecommendationType(feedItem),
			TargetPath:  "/feed/item/" + feedItem.ID.String(),
			ScoreLabel:  recommendationScoreLabel(mode, item.FinalScore),
		})
	}

	items = filterRecommendationItems(items, category, theme)
	items, total, err := paginateRecommendationItems(items, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	if err := s.hydrateRecommendationArticles(items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) RecommendChannels(mode recommendation.Mode, category string, theme string, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
	rows, err := s.repo.ListRecommendationChannels()
	if err != nil {
		return nil, 0, err
	}
	sourceRows, err := s.repo.ListExploreSources(100000, 0, "")
	if err != nil {
		return nil, 0, err
	}

	candidates := make([]recommendation.Candidate, 0, len(rows)+len(sourceRows))
	rowByID := make(map[string]RecommendationChannelRow, len(rows))
	sourceByID := make(map[string]ExploreSourceRow, len(sourceRows))
	for _, row := range rows {
		publishedAt := time.Now()
		if row.LatestPublishedAtUnix.Valid {
			publishedAt = time.Unix(row.LatestPublishedAtUnix.Int64, 0)
		}
		candidate := recommendation.Candidate{
			Module:          "feed",
			EntityType:      recommendation.EntityChannel,
			EntityID:        row.ChannelID.String(),
			SourceKey:       row.ChannelID.String(),
			QualityScore:    clamp01(row.AverageViews / 100),
			TrendScore:      clamp01(float64(row.RecentPostCount) / 5),
			FreshnessScore:  normalizePostRecency(publishedAt, 14*24*time.Hour),
			AuthorityScore:  clamp01(float64(row.PublishedCount) / 10),
			ExposureScore:   0,
			EditorialScore:  0,
			PublishedAtUnix: publishedAt.Unix(),
		}
		candidates = append(candidates, candidate)
		rowByID[candidate.EntityID] = row
	}
	for _, row := range sourceRows {
		publishedAt := time.Now()
		if row.LastPublishedAt != nil {
			publishedAt = row.LastPublishedAt.UTC()
		}
		candidate := recommendation.Candidate{
			Module:          "feed",
			EntityType:      recommendation.EntityChannel,
			EntityID:        row.ID.String(),
			SourceKey:       row.ID.String(),
			QualityScore:    normalizeSourceRecommendationQuality(row),
			TrendScore:      clamp01(float64(row.RecentItemCount) / 10),
			FreshnessScore:  normalizePostRecency(publishedAt, 14*24*time.Hour),
			AuthorityScore:  clamp01(float64(row.SubscriptionCount) / 20),
			ExposureScore:   0,
			EditorialScore:  0,
			PublishedAtUnix: publishedAt.Unix(),
		}
		candidates = append(candidates, candidate)
		sourceByID[candidate.EntityID] = row
	}

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		row, ok := rowByID[item.EntityID]
		if ok {
			targetPath := "/channels/" + strings.TrimSpace(row.Slug)
			if strings.TrimSpace(row.Slug) == "" {
				targetPath = "/channels/" + row.ChannelID.String()
			}
			var lastPublishedAt *time.Time
			if row.LatestPublishedAtUnix.Valid {
				value := time.Unix(row.LatestPublishedAtUnix.Int64, 0).UTC()
				lastPublishedAt = &value
			}
			items = append(items, RecommendationItemDTO{
				ID:              row.ChannelID.String(),
				Title:           row.Name,
				Summary:         row.Description,
				Description:     strings.TrimSpace(row.Description),
				ContentType:     "blog",
				ImageURL:        row.CoverURL,
				TargetPath:      targetPath,
				ScoreLabel:      recommendationScoreLabel(mode, item.FinalScore),
				LastPublishedAt: lastPublishedAt,
			})
			continue
		}
		source, ok := sourceByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, RecommendationItemDTO{
			ID:              source.ID.String(),
			Title:           source.Title,
			Summary:         recommendationSourceSummary(source),
			Description:     recommendationSourceDescription(source),
			ContentType:     normalizeSourceCategory(source.Category),
			ImageURL:        "",
			TargetPath:      "/feed?source_id=" + source.ID.String(),
			ScoreLabel:      recommendationScoreLabel(mode, item.FinalScore),
			BookmarkCount:   source.SubscriptionCount,
			LastPublishedAt: source.LastPublishedAt,
		})
	}

	items = filterRecommendationItems(items, category, theme)
	items, total, err := paginateRecommendationItems(items, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	if err := s.enrichRecommendationChannels(items, rowByID, sourceByID); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *Service) hydrateRecommendationArticles(items []RecommendationItemDTO) error {
	postIDs := make([]uuid.UUID, 0, len(items))
	feedItemIDs := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		id, err := uuid.Parse(item.ID)
		if err != nil {
			return err
		}
		if strings.HasPrefix(item.TargetPath, "/posts/post/") {
			postIDs = append(postIDs, id)
		} else {
			feedItemIDs = append(feedItemIDs, id)
		}
	}

	posts, err := s.repo.ListRecommendationPostsByIDs(postIDs)
	if err != nil {
		return err
	}
	feedItems, err := s.repo.ListRecommendationFeedItemsByIDs(feedItemIDs)
	if err != nil {
		return err
	}
	postByID := make(map[string]model.Post, len(posts))
	for _, post := range posts {
		postByID[post.ID.String()] = post
	}
	feedItemByID := make(map[string]model.FeedItem, len(feedItems))
	for _, feedItem := range feedItems {
		feedItemByID[feedItem.ID.String()] = feedItem
	}

	for i := range items {
		if post, ok := postByID[items[i].ID]; ok {
			items[i].Title = post.Title
			items[i].Summary = post.Summary
			items[i].ImageURL = post.CoverURL
			continue
		}
		if feedItem, ok := feedItemByID[items[i].ID]; ok {
			items[i].Title = feedItem.Title
			items[i].Summary = feedItem.Summary
			items[i].ImageURL = feedItem.ImageURL
		}
	}
	return nil
}

func (s *Service) enrichRecommendationChannels(items []RecommendationItemDTO, rowByID map[string]RecommendationChannelRow, sourceByID map[string]ExploreSourceRow) error {
	for i := range items {
		if row, ok := rowByID[items[i].ID]; ok {
			recentPosts, err := s.repo.ListRecentPublishedPostsByChannelID(row.ChannelID, 3)
			if err != nil {
				return err
			}
			publishedTimes := make([]time.Time, 0, len(recentPosts))
			for _, post := range recentPosts {
				items[i].RecentItems = append(items[i].RecentItems, RecommendationPreviewDTO{ID: post.ID.String(), Title: post.Title})
				if !post.CreatedAt.IsZero() {
					publishedTimes = append(publishedTimes, post.CreatedAt)
				}
			}
			feedSourceID, err := s.findInternalChannelFeedSourceID(row.ChannelID)
			if err != nil {
				return err
			}
			items[i].BookmarkCount, err = s.repo.CountSubscriptionsByFeedSourceID(feedSourceID)
			if err != nil {
				return err
			}
			items[i].ReadCount, err = s.repo.CountReadEvents("internal_channel", row.ChannelID.String())
			if err != nil {
				return err
			}
			items[i].UpdateFrequencyLabel = describeUpdateFrequency(publishedTimes)
			continue
		}

		source, ok := sourceByID[items[i].ID]
		if !ok {
			continue
		}
		publishedTimes := make([]time.Time, 0, len(source.RecentItems))
		for _, recent := range source.RecentItems {
			items[i].RecentItems = append(items[i].RecentItems, RecommendationPreviewDTO{ID: recent.ID.String(), Title: recent.Title})
			if !recent.PublishedAt.IsZero() {
				publishedTimes = append(publishedTimes, recent.PublishedAt)
			}
		}
		readCount, err := s.repo.CountReadEvents("external_rss", source.ID.String())
		if err != nil {
			return err
		}
		items[i].ReadCount = readCount
		items[i].UpdateFrequencyLabel = describeUpdateFrequency(publishedTimes)
	}
	return nil
}

func paginateRecommendationItems(items []RecommendationItemDTO, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
	page = normalizedPage(page)
	pageSize = normalizedPageSize(pageSize)
	total := int64(len(items))
	start := (page - 1) * pageSize
	if start > len(items) {
		start = len(items)
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

func recommendationSourceKeyForPost(post RecommendationArticlePostRow) string {
	if post.ChannelID != nil {
		return post.ChannelID.String()
	}
	return post.UserID.String()
}

func recommendationSourceKeyForFeedItem(item RecommendationArticleFeedItemRow) string {
	if item.FeedSourceID != uuid.Nil {
		return item.FeedSourceID.String()
	}
	return item.ID.String()
}

func normalizeArticleQuality(post RecommendationArticlePostRow) float64 {
	return clamp01(float64(post.ViewCount) / 100)
}

func normalizeFeedItemQuality(item RecommendationArticleFeedItemRow) float64 {
	score := 0.35
	if item.HasSummary {
		score += 0.2
	}
	if item.HasImage {
		score += 0.15
	}
	if item.HasFullText {
		score += 0.2
	}
	return clamp01(score)
}

func normalizeFeedItemTrend(item RecommendationArticleFeedItemRow) float64 {
	if !item.PublishedAt.IsZero() {
		return normalizePostRecency(item.PublishedAt, 7*24*time.Hour)
	}
	return 0.4
}

func normalizeArticleAuthority(post RecommendationArticlePostRow) float64 {
	if post.ChannelID != nil {
		return 0.6
	}
	return 0.4
}

func normalizeFeedItemAuthority(item RecommendationArticleFeedItemRow) float64 {
	switch strings.TrimSpace(strings.ToLower(item.SourceCategory)) {
	case "news":
		return 0.65
	case "podcast", "video":
		return 0.55
	default:
		return 0.5
	}
}

func inferFeedItemRecommendationType(item RecommendationArticleFeedItemRow) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.EnclosureType)), "video/") {
		return "video"
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.EnclosureType)), "audio/") {
		return "podcast"
	}
	return normalizeSourceCategory(item.SourceCategory)
}

func normalizeSourceCategory(category string) string {
	switch strings.TrimSpace(strings.ToLower(category)) {
	case "news":
		return "news"
	case "social":
		return "social"
	case "video":
		return "video"
	case "forum":
		return "forum"
	case "podcast":
		return "podcast"
	default:
		return "blog"
	}
}

func normalizeSourceRecommendationQuality(row ExploreSourceRow) float64 {
	score := 0.35
	if strings.TrimSpace(row.Title) != "" {
		score += 0.15
	}
	if len(row.RecentItems) >= 3 {
		score += 0.2
	}
	if row.LastPublishedAt != nil {
		score += 0.1
	}
	return clamp01(score)
}

func recommendationSourceSummary(row ExploreSourceRow) string {
	parts := make([]string, 0, 3)
	if category := strings.TrimSpace(row.Category); category != "" {
		parts = append(parts, category)
	}
	if row.SubscriptionCount > 0 {
		parts = append(parts, fmt.Sprintf("%d 订阅", row.SubscriptionCount))
	}
	if row.RecentItemCount > 0 {
		parts = append(parts, fmt.Sprintf("%d 条更新", row.RecentItemCount))
	}
	if len(parts) == 0 {
		return "打开后查看该来源的最新条目。"
	}
	return strings.Join(parts, " · ")
}

func recommendationSourceDescription(row ExploreSourceRow) string {
	if category := normalizeSourceCategory(row.Category); category != "" {
		switch category {
		case "news":
			return "关注新闻动态、公共议题与持续更新。"
		case "social":
			return "关注社交平台动态、创作者表达与社区讨论。"
		case "video":
			return "关注视频内容、影像作品与持续更新。"
		case "forum":
			return "关注社区讨论、问答交流与论坛话题。"
		case "podcast":
			return "关注播客更新、访谈内容与长期节目。"
		default:
			return "关注文章、观点与持续写作输出。"
		}
	}
	return "关注近期内容更新与持续发布。"
}

func normalizePostRecency(publishedAt time.Time, horizon time.Duration) float64 {
	if publishedAt.IsZero() || horizon <= 0 {
		return 0
	}
	age := time.Since(publishedAt)
	if age <= 0 {
		return 1
	}
	score := 1 - (float64(age) / float64(horizon))
	return clamp01(score)
}

func recommendationScoreLabel(mode recommendation.Mode, score float64) string {
	prefix := map[recommendation.Mode]string{
		recommendation.ModeHot:      "热度",
		recommendation.ModeFeatured: "精选",
		recommendation.ModeDiscover: "探索",
	}[mode]
	if prefix == "" {
		prefix = "推荐"
	}
	return fmt.Sprintf("%s %.0f", prefix, math.Round(score*100))
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func describeUpdateFrequency(publishedTimes []time.Time) string {
	if len(publishedTimes) < 2 {
		return "更新较少"
	}
	var total time.Duration
	var intervals int
	for i := 1; i < len(publishedTimes); i++ {
		if publishedTimes[i-1].IsZero() || publishedTimes[i].IsZero() {
			continue
		}
		diff := publishedTimes[i-1].Sub(publishedTimes[i])
		if diff < 0 {
			diff = -diff
		}
		total += diff
		intervals++
	}
	if intervals == 0 {
		return "更新较少"
	}
	average := total / time.Duration(intervals)
	switch {
	case average <= 36*time.Hour:
		return "日更"
	case average <= 4*24*time.Hour:
		return "每周多次"
	case average <= 10*24*time.Hour:
		return "偶尔更新"
	default:
		return "更新较少"
	}
}

func (s *Service) findInternalChannelFeedSourceID(channelID uuid.UUID) (uuid.UUID, error) {
	var source model.FeedSource
	if err := s.db.Where("source_type = ? AND source_id = ?", "internal_channel", channelID).First(&source).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, err
		}
		var channel model.Channel
		if err := s.db.First(&channel, "id = ?", channelID).Error; err != nil {
			return uuid.Nil, err
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("internal_channel:%s", channelID.String()))))
		source = model.FeedSource{
			SourceType: "internal_channel",
			SourceID:   &channelID,
			Title:      channel.Name,
			Hash:       hash,
		}
		if err := s.db.Where("hash = ?", hash).FirstOrCreate(&source).Error; err != nil {
			return uuid.Nil, err
		}
	}
	return source.ID, nil
}

func filterRecommendationItems(items []RecommendationItemDTO, category string, theme string) []RecommendationItemDTO {
	normalizedCategory := normalizeSourceCategory(category)
	if normalizedCategory == "" {
		normalizedCategory = "blog"
	}

	filtered := make([]RecommendationItemDTO, 0, len(items))
	for _, item := range items {
		if normalizeSourceCategory(item.ContentType) != normalizedCategory {
			continue
		}
		filtered = append(filtered, item)
	}

	normalizedTheme := strings.TrimSpace(strings.ToLower(theme))
	if normalizedTheme == "" || normalizedTheme == "all" {
		return filtered
	}

	definition, ok := findRecommendationTheme(normalizedCategory, normalizedTheme)
	if !ok {
		return []RecommendationItemDTO{}
	}

	themeFiltered := make([]RecommendationItemDTO, 0, len(filtered))
	for _, item := range filtered {
		if recommendationItemMatchesTheme(item, definition) {
			themeFiltered = append(themeFiltered, item)
		}
	}
	return themeFiltered
}

func recommendationThemeKeywords(category string, theme string) ([]string, bool) {
	normalized := strings.TrimSpace(strings.ToLower(theme))
	if normalized == "" || normalized == "all" {
		return nil, true
	}
	definition, ok := findRecommendationTheme(category, normalized)
	if !ok {
		return nil, false
	}
	for _, keyword := range definition.Keywords {
		if strings.Contains(strings.ToLower(category), strings.ToLower(keyword)) {
			return nil, true
		}
	}
	return definition.Keywords, true
}

func recommendationArticleCandidateWindow(mode recommendation.Mode) time.Duration {
	switch mode {
	case recommendation.ModeFeatured:
		return 180 * 24 * time.Hour
	case recommendation.ModeDiscover:
		return 90 * 24 * time.Hour
	default:
		return 30 * 24 * time.Hour
	}
}

func recommendationItemMatchesTheme(item RecommendationItemDTO, definition recommendationThemeDefinition) bool {
	haystack := strings.ToLower(strings.Join([]string{
		item.Title,
		item.Summary,
		item.ContentType,
	}, " "))
	for _, keyword := range definition.Keywords {
		if strings.Contains(haystack, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

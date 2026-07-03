package feed

import (
	"fmt"
	"math"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
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
	posts, err := s.repo.ListRecommendationPosts()
	if err != nil {
		return nil, 0, err
	}
	feedItems, err := s.repo.ListExploreFeedItemsAll("recent")
	if err != nil {
		return nil, 0, err
	}

	candidates := make([]recommendation.Candidate, 0, len(posts)+len(feedItems))
	postByID := make(map[string]model.Post, len(posts))
	feedItemByID := make(map[string]model.FeedItem, len(feedItems))
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
				ID:         post.ID.String(),
				Title:      post.Title,
				Summary:    post.Summary,
				ContentType: "blog",
				ImageURL:   post.CoverURL,
				TargetPath: "/posts/post/" + post.ID.String(),
				ScoreLabel: recommendationScoreLabel(mode, item.FinalScore),
			})
			continue
		}
		feedItem, ok := feedItemByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, RecommendationItemDTO{
			ID:         feedItem.ID.String(),
			Title:      feedItem.Title,
			Summary:    feedItem.Summary,
			ContentType: inferFeedItemRecommendationType(feedItem),
			ImageURL:   feedItem.ImageURL,
			TargetPath: "/feed/item/" + feedItem.ID.String(),
			ScoreLabel: recommendationScoreLabel(mode, item.FinalScore),
		})
	}

	items = filterRecommendationItems(items, category, theme)
	return paginateRecommendationItems(items, page, pageSize)
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
			QualityScore:    clamp01(row.AverageRating / 100),
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
			recentPosts, err := s.repo.ListRecentPublishedPostsByChannelID(row.ChannelID, 3)
			if err != nil {
				return nil, 0, err
			}
			recentItems := make([]RecommendationPreviewDTO, 0, len(recentPosts))
			publishedTimes := make([]time.Time, 0, len(recentPosts))
			for _, post := range recentPosts {
				recentItems = append(recentItems, RecommendationPreviewDTO{
					ID:    post.ID.String(),
					Title: post.Title,
				})
				if !post.CreatedAt.IsZero() {
					publishedTimes = append(publishedTimes, post.CreatedAt)
				}
			}
			feedSourceID, err := s.findInternalChannelFeedSourceID(row.ChannelID)
			if err != nil {
				return nil, 0, err
			}
			bookmarkCount, err := s.repo.CountSubscriptionsByFeedSourceID(feedSourceID)
			if err != nil {
				return nil, 0, err
			}
			readCount, err := s.repo.CountReadEvents("internal_channel", row.ChannelID.String())
			if err != nil {
				return nil, 0, err
			}
			var lastPublishedAt *time.Time
			if row.LatestPublishedAtUnix.Valid {
				value := time.Unix(row.LatestPublishedAtUnix.Int64, 0).UTC()
				lastPublishedAt = &value
			}
			items = append(items, RecommendationItemDTO{
				ID:                   row.ChannelID.String(),
				Title:                row.Name,
				Summary:              row.Description,
				Description:          strings.TrimSpace(row.Description),
				ContentType:          "blog",
				ImageURL:             row.CoverURL,
				TargetPath:           targetPath,
				ScoreLabel:           recommendationScoreLabel(mode, item.FinalScore),
				BookmarkCount:        bookmarkCount,
				ReadCount:            readCount,
				UpdateFrequencyLabel: describeUpdateFrequency(publishedTimes),
				LastPublishedAt:      lastPublishedAt,
				RecentItems:          recentItems,
			})
			continue
		}
		source, ok := sourceByID[item.EntityID]
		if !ok {
			continue
		}
		bookmarkCount, err := s.repo.CountSubscriptionsByFeedSourceID(source.ID)
		if err != nil {
			return nil, 0, err
		}
		readCount, err := s.repo.CountReadEvents("external_rss", source.ID.String())
		if err != nil {
			return nil, 0, err
		}
		recentItems := make([]RecommendationPreviewDTO, 0, len(source.RecentItems))
		publishedTimes := make([]time.Time, 0, len(source.RecentItems))
		for _, recent := range source.RecentItems {
			recentItems = append(recentItems, RecommendationPreviewDTO{
				ID:    recent.ID.String(),
				Title: recent.Title,
			})
			if !recent.PublishedAt.IsZero() {
				publishedTimes = append(publishedTimes, recent.PublishedAt)
			}
		}
		items = append(items, RecommendationItemDTO{
			ID:                   source.ID.String(),
			Title:                source.Title,
			Summary:              recommendationSourceSummary(source),
			Description:          recommendationSourceDescription(source),
			ContentType:          normalizeSourceCategory(source.Category),
			ImageURL:             "",
			TargetPath:           "/feed?source_id=" + source.ID.String(),
			ScoreLabel:           recommendationScoreLabel(mode, item.FinalScore),
			BookmarkCount:        bookmarkCount,
			ReadCount:            readCount,
			UpdateFrequencyLabel: describeUpdateFrequency(publishedTimes),
			LastPublishedAt:      source.LastPublishedAt,
			RecentItems:          recentItems,
		})
	}

	items = filterRecommendationItems(items, category, theme)
	return paginateRecommendationItems(items, page, pageSize)
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

func recommendationSourceKeyForPost(post model.Post) string {
	if post.ChannelID != nil {
		return post.ChannelID.String()
	}
	return post.UserID.String()
}

func recommendationSourceKeyForFeedItem(item model.FeedItem) string {
	if item.FeedSourceID.String() != "" {
		return item.FeedSourceID.String()
	}
	return item.ID.String()
}

func normalizeArticleQuality(post model.Post) float64 {
	ratingComponent := clamp01(float64(post.RatingAverageScore) / 100)
	countComponent := clamp01(float64(post.RatingCount) / 10)
	return clamp01(0.7*ratingComponent + 0.3*countComponent)
}

func normalizeFeedItemQuality(item model.FeedItem) float64 {
	score := 0.35
	if strings.TrimSpace(item.Summary) != "" {
		score += 0.2
	}
	if strings.TrimSpace(item.ImageURL) != "" {
		score += 0.15
	}
	if strings.TrimSpace(item.FullTextHTML) != "" {
		score += 0.2
	}
	return clamp01(score)
}

func normalizeFeedItemTrend(item model.FeedItem) float64 {
	if !item.PublishedAt.IsZero() {
		return normalizePostRecency(item.PublishedAt, 7*24*time.Hour)
	}
	return 0.4
}

func normalizeArticleAuthority(post model.Post) float64 {
	if post.ChannelID != nil {
		return 0.6
	}
	return 0.4
}

func normalizeFeedItemAuthority(item model.FeedItem) float64 {
	if item.FeedSource != nil {
		switch strings.TrimSpace(strings.ToLower(item.FeedSource.Category)) {
		case "news":
			return 0.65
		case "podcast", "video":
			return 0.55
		default:
			return 0.5
		}
	}
	return 0.45
}

func inferFeedItemRecommendationType(item model.FeedItem) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.EnclosureType)), "video/") {
		return "video"
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.EnclosureType)), "audio/") {
		return "podcast"
	}
	if item.FeedSource != nil {
		return normalizeSourceCategory(item.FeedSource.Category)
	}
	return "blog"
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
		return uuid.Nil, err
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

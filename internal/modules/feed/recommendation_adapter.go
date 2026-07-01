package feed

import (
	"fmt"
	"math"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"
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

func (s *Service) RecommendArticles(mode recommendation.Mode, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
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

	return paginateRecommendationItems(items, page, pageSize)
}

func (s *Service) RecommendChannels(mode recommendation.Mode, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
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
			items = append(items, RecommendationItemDTO{
				ID:         row.ChannelID.String(),
				Title:      row.Name,
				Summary:    row.Description,
				ContentType: "blog",
				ImageURL:   row.CoverURL,
				TargetPath: targetPath,
				ScoreLabel: recommendationScoreLabel(mode, item.FinalScore),
			})
			continue
		}
		source, ok := sourceByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, RecommendationItemDTO{
			ID:         source.ID.String(),
			Title:      source.Title,
			Summary:    recommendationSourceSummary(source),
			ContentType: normalizeSourceCategory(source.Category),
			ImageURL:   "",
			TargetPath: "/feed?source_id=" + source.ID.String(),
			ScoreLabel: recommendationScoreLabel(mode, item.FinalScore),
		})
	}

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

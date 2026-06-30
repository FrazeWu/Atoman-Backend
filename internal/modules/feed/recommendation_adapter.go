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

	candidates := make([]recommendation.Candidate, 0, len(posts))
	postByID := make(map[string]model.Post, len(posts))
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

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		post, ok := postByID[item.EntityID]
		if !ok {
			continue
		}
		items = append(items, RecommendationItemDTO{
			ID:         post.ID.String(),
			Title:      post.Title,
			Summary:    post.Summary,
			ImageURL:   post.CoverURL,
			TargetPath: "/posts/post/" + post.ID.String(),
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

	candidates := make([]recommendation.Candidate, 0, len(rows))
	rowByID := make(map[string]RecommendationChannelRow, len(rows))
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

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]RecommendationItemDTO, 0, len(ranked))
	for _, item := range ranked {
		row, ok := rowByID[item.EntityID]
		if !ok {
			continue
		}
		targetPath := "/channels/" + strings.TrimSpace(row.Slug)
		if strings.TrimSpace(row.Slug) == "" {
			targetPath = "/channels/" + row.ChannelID.String()
		}
		items = append(items, RecommendationItemDTO{
			ID:         row.ChannelID.String(),
			Title:      row.Name,
			Summary:    row.Description,
			ImageURL:   row.CoverURL,
			TargetPath: targetPath,
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

func normalizeArticleQuality(post model.Post) float64 {
	ratingComponent := clamp01(float64(post.RatingAverageScore) / 100)
	countComponent := clamp01(float64(post.RatingCount) / 10)
	return clamp01(0.7*ratingComponent + 0.3*countComponent)
}

func normalizeArticleAuthority(post model.Post) float64 {
	if post.ChannelID != nil {
		return 0.6
	}
	return 0.4
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

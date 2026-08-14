package blog

import (
	"fmt"
	"math"
	"sort"
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

type blogRecommendationRow struct {
	model.Post
	LikesCount            int64
	CommentsCount         int64
	BookmarksCount        int64
	ChannelFollowersCount int64
}

type blogEngagementSignals struct {
	Reads       float64
	Bookmarks   float64
	Likes       float64
	Comments    float64
	Subscribers float64
}

type blogRankedPost struct {
	ID            string
	ChannelID     string
	Score         float64
	PublishedAt   time.Time
	Post          model.Post
	LikesCount    int64
	CommentsCount int64
}

const blogRecommendationCandidateLimit = 2000

func (s *Service) RecommendPostsByMode(mode recommendation.Mode, viewerID *uuid.UUID, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var rows []blogRecommendationRow
	if err := s.db.Model(&model.Post{}).Select(`posts.*,
		(SELECT COUNT(*) FROM likes WHERE likes.target_type = 'post' AND likes.target_id = posts.id AND likes.deleted_at IS NULL) AS likes_count,
		COALESCE((SELECT targets.comment_count FROM discussion_targets AS targets WHERE targets.kind = 'blog_post' AND targets.resource_id = posts.id AND targets.deleted_at IS NULL LIMIT 1), 0) AS comments_count,
		(SELECT COUNT(*) FROM bookmarks WHERE bookmarks.post_id = posts.id AND bookmarks.deleted_at IS NULL) AS bookmarks_count,
		(SELECT COUNT(*) FROM subscriptions JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id
		 WHERE feed_sources.source_type = 'internal_channel' AND feed_sources.source_id = posts.channel_id
		 AND subscriptions.deleted_at IS NULL AND feed_sources.deleted_at IS NULL) AS channel_followers_count`).
		Where("posts.status = ? AND posts.visibility = ?", "published", "public").
		Order("COALESCE(posts.published_at, posts.created_at) DESC").
		Limit(blogRecommendationCandidateLimit).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	subscribedChannels := map[uuid.UUID]struct{}{}
	if viewerID != nil {
		var channelIDs []uuid.UUID
		if err := s.db.Table("feed_sources").Select("feed_sources.source_id").
			Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
			Where("subscriptions.user_id = ? AND feed_sources.source_type = ?", *viewerID, "internal_channel").
			Where("subscriptions.deleted_at IS NULL AND feed_sources.deleted_at IS NULL").
			Scan(&channelIDs).Error; err != nil {
			return nil, 0, err
		}
		for _, channelID := range channelIDs {
			subscribedChannels[channelID] = struct{}{}
		}
	}

	reads, likes, comments, bookmarks, subscribers := make([]float64, len(rows)), make([]float64, len(rows)), make([]float64, len(rows)), make([]float64, len(rows)), make([]float64, len(rows))
	for i, row := range rows {
		reads[i], likes[i], comments[i], bookmarks[i], subscribers[i] = float64(row.ViewCount), float64(row.LikesCount), float64(row.CommentsCount), float64(row.BookmarksCount), float64(row.ChannelFollowersCount)
	}

	readScores := percentileScores(reads)
	likeScores := percentileScores(likes)
	commentScores := percentileScores(comments)
	bookmarkScores := percentileScores(bookmarks)
	subscriberScores := percentileScores(subscribers)

	now := time.Now().UTC()
	ranked := make([]blogRankedPost, 0, len(rows))
	for i, row := range rows {
		signals := blogEngagementSignals{
			Reads: readScores[i], Bookmarks: bookmarkScores[i],
			Likes: likeScores[i], Comments: commentScores[i],
			Subscribers: subscriberScores[i],
		}
		composite := blogCompositeScore(signals)
		publishedAt := blogPublishedAt(row.Post)
		_, subscribed := subscribedChannels[uuidValue(row.ChannelID)]
		score := composite
		switch mode {
		case recommendation.ModeHot:
			score = blogHotScore(composite, publishedAt, now)
		case recommendation.ModeFeatured:
			score = blogRecommendedScore(composite, subscribed, publishedAt, now)
		case recommendation.ModeDiscover:
			score = blogRecommendedScore(composite, subscribed, publishedAt, now) + 0.10*(1-signals.Reads)
		}
		ranked = append(ranked, blogRankedPost{
			ID: row.ID.String(), ChannelID: blogRecommendationSourceKey(row.Post), Score: score,
			PublishedAt: publishedAt, Post: row.Post,
			LikesCount: row.LikesCount, CommentsCount: row.CommentsCount,
		})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].PublishedAt.After(ranked[j].PublishedAt)
		}
		return ranked[i].Score > ranked[j].Score
	})
	ranked = rerankBlogDiversity(ranked, 2)

	items := make([]RecommendationItemDTO, 0, len(ranked))
	for _, rankedItem := range ranked {
		post := rankedItem.Post
		items = append(items, RecommendationItemDTO{
			ID:            post.ID.String(),
			Title:         post.Title,
			Summary:       post.Summary,
			ContentType:   "blog",
			ImageURL:      post.CoverURL,
			TargetPath:    "/post/" + post.ID.String(),
			ScoreLabel:    blogRecommendationLabel(mode, rankedItem.Score),
			LikesCount:    rankedItem.LikesCount,
			CommentsCount: rankedItem.CommentsCount,
		})
	}

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

func uuidValue(value *uuid.UUID) uuid.UUID {
	if value == nil {
		return uuid.Nil
	}
	return *value
}

func percentileScores(values []float64) []float64 {
	scores := make([]float64, len(values))
	if len(values) <= 1 {
		for index := range scores {
			scores[index] = 0.5
		}
		return scores
	}
	sortedValues := append([]float64(nil), values...)
	sort.Float64s(sortedValues)
	for index, value := range values {
		lower := sort.SearchFloat64s(sortedValues, value)
		scores[index] = float64(lower) / float64(len(values)-1)
	}
	return scores
}

func percentileScore(value float64, values []float64) float64 {
	if len(values) <= 1 {
		return 0.5
	}
	sortedValues := append([]float64(nil), values...)
	sort.Float64s(sortedValues)
	return float64(sort.SearchFloat64s(sortedValues, value)) / float64(len(values)-1)
}

func blogCompositeScore(signals blogEngagementSignals) float64 {
	return 0.20*signals.Reads + 0.30*signals.Bookmarks + 0.20*signals.Likes + 0.20*signals.Comments + 0.10*signals.Subscribers
}

func blogHotScore(composite float64, publishedAt time.Time, now time.Time) float64 {
	age := now.Sub(publishedAt)
	if age < 0 {
		age = 0
	}
	return composite * math.Exp(-float64(age)/(float64(7*24*time.Hour)))
}

func blogRecommendedScore(composite float64, subscribed bool, publishedAt time.Time, now time.Time) float64 {
	score := composite + 0.05*math.Exp(-math.Max(0, float64(now.Sub(publishedAt)))/float64(14*24*time.Hour))
	if subscribed {
		score += 0.15
	}
	return score
}

func blogPublishedAt(post model.Post) time.Time {
	if post.PublishedAt != nil {
		return *post.PublishedAt
	}
	return post.CreatedAt
}

func rerankBlogDiversity(items []blogRankedPost, maxConsecutive int) []blogRankedPost {
	remaining := append([]blogRankedPost(nil), items...)
	result := make([]blogRankedPost, 0, len(items))
	lastChannel, consecutive := "", 0
	for len(remaining) > 0 {
		pick := 0
		if maxConsecutive > 0 && consecutive >= maxConsecutive {
			for i, item := range remaining {
				if item.ChannelID != lastChannel {
					pick = i
					break
				}
			}
		}
		item := remaining[pick]
		remaining = append(remaining[:pick], remaining[pick+1:]...)
		if item.ChannelID == lastChannel {
			consecutive++
		} else {
			lastChannel, consecutive = item.ChannelID, 1
		}
		result = append(result, item)
	}
	return result
}

func blogRecommendationSourceKey(post model.Post) string {
	if post.ChannelID != nil {
		return post.ChannelID.String()
	}
	return post.UserID.String()
}

func normalizeBlogRecommendationQuality(post model.Post) float64 {
	readComponent := clampBlogRecommendation(math.Log1p(float64(post.ViewCount)) / math.Log1p(1000))
	summaryComponent := 0.0
	if strings.TrimSpace(post.Summary) != "" {
		summaryComponent = 0.15
	}
	return clampBlogRecommendation(0.85*readComponent + summaryComponent)
}

func normalizeBlogRecommendationTrend(post model.Post) float64 {
	return clampBlogRecommendation(0.6*normalizeBlogRecommendationFreshness(post.CreatedAt, 7*24*time.Hour) + 0.4*clampBlogRecommendation(math.Log1p(float64(post.ViewCount))/math.Log1p(1000)))
}

func normalizeBlogRecommendationFreshness(createdAt time.Time, horizon time.Duration) float64 {
	if createdAt.IsZero() || horizon <= 0 {
		return 0
	}
	age := time.Since(createdAt)
	if age <= 0 {
		return 1
	}
	return clampBlogRecommendation(1 - float64(age)/float64(horizon))
}

func normalizeBlogRecommendationAuthority(post model.Post) float64 {
	if post.Pinned {
		return 0.8
	}
	if post.ChannelID != nil {
		return 0.6
	}
	return 0.4
}

func clampBlogRecommendation(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func blogRecommendationLabel(mode recommendation.Mode, score float64) string {
	prefix := "推荐"
	switch mode {
	case recommendation.ModeHot:
		prefix = "热度"
	case recommendation.ModeFeatured:
		prefix = "精选"
	case recommendation.ModeDiscover:
		prefix = "探索"
	}
	return fmt.Sprintf("%s %.0f", prefix, math.Round(score*100))
}

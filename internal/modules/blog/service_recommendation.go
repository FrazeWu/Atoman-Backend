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
	"gorm.io/gorm/clause"
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
	BlogContent
	LikesCount            int64
	CommentsCount         int64
	BookmarksCount        int64
	ChannelFollowersCount int64
	RatingScore           float64
	RatingCount           int64
}

type blogEngagementSignals struct {
	Reads       float64
	Bookmarks   float64
	Likes       float64
	Comments    float64
	Subscribers float64
}

type blogRankedPost struct {
	ID             string
	ChannelID      string
	Score          float64
	PublishedAt    time.Time
	Post           BlogContent
	LikesCount     int64
	CommentsCount  int64
	BookmarksCount int64
	RatingScore    float64
	RatingCount    int64
}

const blogRecommendationCandidateLimit = 2000

func (s *Service) RecommendPostsByMode(mode recommendation.Mode, viewerID *uuid.UUID, page int, pageSize int, queryText string) ([]RecommendationItemDTO, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var hiddenContentIDs []uuid.UUID
	if viewerID != nil && s.db.Migrator().HasTable(&model.BlogRecommendationFeedback{}) {
		if err := s.db.Model(&model.BlogRecommendationFeedback{}).
			Where("user_id = ? AND action = ?", *viewerID, "hide").
			Pluck("content_id", &hiddenContentIDs).Error; err != nil {
			return nil, 0, err
		}
	}

	type candidateRow struct {
		ID                    uuid.UUID  `gorm:"column:id"`
		CreatedAt             time.Time  `gorm:"column:created_at"`
		UpdatedAt             time.Time  `gorm:"column:updated_at"`
		AuthorID              *uuid.UUID `gorm:"column:author_id"`
		ChannelID             uuid.UUID  `gorm:"column:channel_id"`
		Title                 string     `gorm:"column:title"`
		Summary               string     `gorm:"column:summary"`
		CoverURL              string     `gorm:"column:cover_url"`
		Status                string     `gorm:"column:status"`
		Visibility            string     `gorm:"column:visibility"`
		PublishedAt           *time.Time `gorm:"column:published_at"`
		ScheduledAt           *time.Time `gorm:"column:scheduled_at"`
		Content               string     `gorm:"column:content"`
		LanguageCode          string     `gorm:"column:language_code"`
		Pinned                bool       `gorm:"column:pinned"`
		ViewCount             int64      `gorm:"column:view_count"`
		CollectionConflict    bool       `gorm:"column:collection_conflict"`
		CollectionID          *uuid.UUID `gorm:"column:collection_id"`
		CollectionPosition    int        `gorm:"column:collection_position"`
		LikesCount            int64      `gorm:"column:likes_count"`
		CommentsCount         int64      `gorm:"column:comments_count"`
		BookmarksCount        int64      `gorm:"column:bookmarks_count"`
		ChannelFollowersCount int64      `gorm:"column:channel_followers_count"`
	}
	var candidates []candidateRow
	query := canonicalBlogPostsQuery(s.db).Select(`posts.id, posts.created_at, posts.updated_at, posts.author_id, posts.channel_id,
		posts.title, posts.summary, posts.cover_url, posts.status, posts.visibility, posts.published_at, posts.scheduled_at,
		blog_extensions.content, blog_extensions.language_code, blog_extensions.pinned, blog_extensions.view_count,
		blog_extensions.collection_conflict, memberships.collection_id, memberships.position AS collection_position,
		(SELECT COUNT(*) FROM likes WHERE likes.target_type = 'post' AND likes.target_id = posts.id AND likes.deleted_at IS NULL) AS likes_count,
		COALESCE((SELECT targets.comment_count FROM discussion_targets AS targets WHERE targets.kind = 'blog_post' AND targets.resource_id = posts.id AND targets.deleted_at IS NULL LIMIT 1), 0) AS comments_count,
		(SELECT COUNT(*) FROM bookmarks WHERE bookmarks.content_id = posts.id AND bookmarks.deleted_at IS NULL) AS bookmarks_count,
		(SELECT COUNT(*) FROM subscriptions JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id
			 WHERE feed_sources.source_type = 'internal_channel' AND feed_sources.source_id = posts.channel_id
			 AND subscriptions.deleted_at IS NULL AND feed_sources.deleted_at IS NULL) AS channel_followers_count`)
	if len(hiddenContentIDs) > 0 {
		query = query.Where("posts.id NOT IN ?", hiddenContentIDs)
	}
	if searchQuery := strings.TrimSpace(queryText); searchQuery != "" {
		searchLike := "%" + searchQuery + "%"
		query = query.Where("(LOWER(posts.title) LIKE LOWER(?) OR LOWER(posts.summary) LIKE LOWER(?) OR LOWER(blog_extensions.content) LIKE LOWER(?))", searchLike, searchLike, searchLike)
	}
	if err := query.
		Where("posts.status = ? AND (posts.visibility = ? OR posts.visibility = ?)", "published", "", "public").
		Order("COALESCE(posts.published_at, posts.created_at) DESC").
		Limit(blogRecommendationCandidateLimit).
		Scan(&candidates).Error; err != nil {
		return nil, 0, err
	}
	canonicalRows := make([]canonicalBlogPostRow, 0, len(candidates))
	for _, candidate := range candidates {
		canonicalRows = append(canonicalRows, canonicalBlogPostRow{
			ID: candidate.ID, CreatedAt: candidate.CreatedAt, UpdatedAt: candidate.UpdatedAt,
			AuthorID: candidate.AuthorID, ChannelID: candidate.ChannelID, Title: candidate.Title,
			Summary: candidate.Summary, CoverURL: candidate.CoverURL, Status: candidate.Status,
			Visibility: candidate.Visibility, PublishedAt: candidate.PublishedAt, ScheduledAt: candidate.ScheduledAt,
			Content: candidate.Content, LanguageCode: candidate.LanguageCode, Pinned: candidate.Pinned,
			ViewCount: candidate.ViewCount, CollectionConflict: candidate.CollectionConflict,
			CollectionID: candidate.CollectionID, CollectionPosition: candidate.CollectionPosition,
		})
	}
	contents, err := hydrateCanonicalBlogContents(s.db, canonicalRows)
	if err != nil {
		return nil, 0, err
	}
	contentsByID := make(map[uuid.UUID]BlogContent, len(contents))
	for _, content := range contents {
		contentsByID[content.ID] = content
	}
	rows := make([]blogRecommendationRow, 0, len(candidates))
	for _, candidate := range candidates {
		content, ok := contentsByID[candidate.ID]
		if !ok {
			continue
		}
		rows = append(rows, blogRecommendationRow{
			BlogContent: content, LikesCount: candidate.LikesCount, CommentsCount: candidate.CommentsCount,
			BookmarksCount: candidate.BookmarksCount, ChannelFollowersCount: candidate.ChannelFollowersCount,
		})
	}

	ratingsByPostID, err := s.recommendationRatings(rows)
	if err != nil {
		return nil, 0, err
	}
	for index := range rows {
		if rating, ok := ratingsByPostID[rows[index].ID]; ok {
			rows[index].RatingScore = rating.Score
			rows[index].RatingCount = rating.Count
		}
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
		publishedAt := blogContentPublishedAt(row.BlogContent)
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
			ID: row.ID.String(), ChannelID: blogContentRecommendationSourceKey(row.BlogContent), Score: score,
			PublishedAt: publishedAt, Post: row.BlogContent,
			LikesCount: row.LikesCount, CommentsCount: row.CommentsCount,
			BookmarksCount: row.BookmarksCount, RatingScore: row.RatingScore, RatingCount: row.RatingCount,
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
			ID: post.ID.String(), Title: post.Title, Summary: seoPostDescription(post), ContentType: "blog",
			ImageURL: post.CoverURL, TargetPath: "/posts/post/" + post.ID.String(),
			ScoreLabel: blogRecommendationLabel(mode, rankedItem.Score), LikesCount: rankedItem.LikesCount,
			CommentsCount: rankedItem.CommentsCount, CreatedAt: post.CreatedAt, PublishedAt: post.PublishedAt,
			User: recommendationAuthor(post.User), Channel: recommendationChannel(post.Channel), ViewCount: post.ViewCount, BookmarksCount: rankedItem.BookmarksCount,
			RatingScore: rankedItem.RatingScore, RatingCount: rankedItem.RatingCount,
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

func (s *Service) RelatedPosts(postID uuid.UUID, viewerID *uuid.UUID, limit int) ([]RecommendationItemDTO, error) {
	if limit < 1 || limit > 12 {
		limit = 6
	}

	anchorQuery := ApplyPublishedPostListVisibility(
		canonicalBlogPostsQuery(s.db).Where("posts.id = ? AND posts.status = ?", postID, "published"), viewerID,
	)
	anchors, err := LoadCanonicalBlogContents(s.db, anchorQuery.Limit(1))
	if err != nil {
		return nil, err
	}
	if len(anchors) == 0 {
		return nil, apperr.NotFound("blog.post_not_found", "Post not found")
	}
	anchor := anchors[0]

	orderSQL := "CASE "
	orderVars := make([]interface{}, 0, 2)
	if len(anchor.Tags) > 0 {
		orderSQL += "WHEN EXISTS (SELECT 1 FROM content_blog_tags related_tags WHERE related_tags.content_id = posts.id AND related_tags.name IN ?) THEN 0 "
		orderVars = append(orderVars, anchor.Tags)
	}
	if anchor.ChannelID != nil && *anchor.ChannelID != uuid.Nil {
		orderSQL += "WHEN posts.channel_id = ? THEN 1 "
		orderVars = append(orderVars, *anchor.ChannelID)
	}
	if anchor.UserID != uuid.Nil {
		orderSQL += "WHEN posts.author_id = ? THEN 2 "
		orderVars = append(orderVars, anchor.UserID)
	}
	orderSQL += "ELSE 3 END, COALESCE(posts.published_at, posts.created_at) DESC, posts.id DESC"

	query := ApplyPublishedPostListVisibility(
		canonicalBlogPostsQuery(s.db).Where("posts.id <> ? AND posts.status = ?", postID, "published"), viewerID,
	).Order(clause.OrderBy{Expression: clause.Expr{SQL: orderSQL, Vars: orderVars}}).Limit(limit)
	var rows []canonicalBlogPostRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	contents, err := hydrateCanonicalBlogContents(s.db, rows)
	if err != nil {
		return nil, err
	}

	items := make([]RecommendationItemDTO, 0, len(contents))
	for _, content := range contents {
		reason := "更多文章"
		if sharesBlogTag(anchor.Tags, content.Tags) {
			reason = "同主题"
		} else if anchor.ChannelID != nil && content.ChannelID != nil && *anchor.ChannelID == *content.ChannelID {
			reason = "同频道"
		} else if anchor.UserID != uuid.Nil && anchor.UserID == content.UserID {
			reason = "同作者"
		}
		items = append(items, RecommendationItemDTO{
			ID: content.ID.String(), Title: content.Title, Summary: content.Summary, ContentType: "blog",
			ImageURL: content.CoverURL, TargetPath: "/posts/post/" + content.ID.String(), ScoreLabel: reason,
			CreatedAt: content.CreatedAt, PublishedAt: content.PublishedAt,
			User: recommendationAuthor(content.User), Channel: recommendationChannel(content.Channel),
		})
	}
	return items, nil
}

func sharesBlogTag(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, tag := range left {
		seen[tag] = struct{}{}
	}
	for _, tag := range right {
		if _, ok := seen[tag]; ok {
			return true
		}
	}
	return false
}

func recommendationAuthor(user *model.User) *RecommendationAuthorDTO {
	if user == nil {
		return nil
	}
	return &RecommendationAuthorDTO{
		UUID: user.UUID, Username: user.Username, DisplayName: user.DisplayName, AvatarURL: user.AvatarURL,
	}
}

func recommendationChannel(channel *model.Channel) *RecommendationChannelDTO {
	if channel == nil {
		return nil
	}
	return &RecommendationChannelDTO{
		ID: channel.ID, Name: channel.Name, Slug: channel.Slug,
		Description: channel.Description, CoverURL: channel.CoverURL,
	}
}

type recommendationRating struct {
	Score float64
	Count int64
}

func (s *Service) recommendationRatings(rows []blogRecommendationRow) (map[uuid.UUID]recommendationRating, error) {
	result := make(map[uuid.UUID]recommendationRating)
	if len(rows) == 0 || !s.db.Migrator().HasTable(&model.PostRating{}) {
		return result, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	type aggregate struct {
		ContentID uuid.UUID `gorm:"column:content_id"`
		Score     float64   `gorm:"column:score"`
		Count     int64     `gorm:"column:count"`
	}
	var aggregates []aggregate
	if err := s.db.Model(&model.PostRating{}).
		Select("content_id, AVG(score) AS score, COUNT(*) AS count").
		Where("content_id IN ?", ids).
		Group("content_id").
		Scan(&aggregates).Error; err != nil {
		return nil, err
	}
	for _, aggregate := range aggregates {
		result[aggregate.ContentID] = recommendationRating{
			Score: math.Round(aggregate.Score*10) / 10,
			Count: aggregate.Count,
		}
	}
	return result, nil
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

func blogContentPublishedAt(content BlogContent) time.Time {
	if content.PublishedAt != nil {
		return *content.PublishedAt
	}
	return content.CreatedAt
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

func blogContentRecommendationSourceKey(content BlogContent) string {
	if content.ChannelID != nil {
		return content.ChannelID.String()
	}
	return content.UserID.String()
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

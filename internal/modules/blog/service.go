package blog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/sitehandle"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var slugInvalidChars = regexp.MustCompile(`[^a-z0-9一-龥]+`)

var allowedPostStatuses = map[string]struct{}{
	"draft":     {},
	"published": {},
}

var allowedPostVisibilities = map[string]struct{}{
	"public":    {},
	"followers": {},
	"private":   {},
}

type Service struct {
	db   *gorm.DB
	repo *Repo
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

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
		(SELECT COUNT(*) FROM comments WHERE comments.target_type = 'post' AND comments.target_id = posts.id AND comments.status = 'visible' AND comments.deleted_at IS NULL) AS comments_count,
		(SELECT COUNT(*) FROM bookmarks WHERE bookmarks.post_id = posts.id AND bookmarks.deleted_at IS NULL) AS bookmarks_count,
		(SELECT COUNT(*) FROM subscriptions JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id
		 WHERE feed_sources.source_type = 'internal_channel' AND feed_sources.source_id = posts.channel_id
		 AND subscriptions.deleted_at IS NULL AND feed_sources.deleted_at IS NULL) AS channel_followers_count`).
		Where("posts.status = ? AND posts.visibility = ?", "published", "public").
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

	now := time.Now().UTC()
	ranked := make([]blogRankedPost, 0, len(rows))
	for i, row := range rows {
		signals := blogEngagementSignals{
			Reads: percentileScore(reads[i], reads), Bookmarks: percentileScore(bookmarks[i], bookmarks),
			Likes: percentileScore(likes[i], likes), Comments: percentileScore(comments[i], comments),
			Subscribers: percentileScore(subscribers[i], subscribers),
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

func percentileScore(value float64, values []float64) float64 {
	if len(values) <= 1 {
		return 0.5
	}
	lower := 0
	for _, candidate := range values {
		if candidate < value {
			lower++
		}
	}
	return float64(lower) / float64(len(values)-1)
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

func (s *Service) ListChannels(userID *uuid.UUID) ([]model.Channel, error) {
	return s.repo.ListChannels(userID)
}

func (s *Service) GetChannel(id uuid.UUID) (model.Channel, error) {
	if id == uuid.Nil {
		return model.Channel{}, apperr.BadRequest("validation.invalid_request", "channel_id is required")
	}
	channel, err := s.repo.GetChannel(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Channel{}, apperr.NotFound("blog.channel_not_found", "Channel not found")
	}
	return channel, err
}

func (s *Service) GetChannelBySlug(slug string) (model.Channel, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return model.Channel{}, apperr.BadRequest("validation.invalid_request", "slug is required")
	}
	channel, err := s.repo.GetChannelBySlug(slug)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Channel{}, apperr.NotFound("blog.channel_not_found", "Channel not found")
	}
	return channel, err
}

func (s *Service) ListCollectionsByChannel(channelID uuid.UUID) ([]model.Collection, error) {
	if _, err := s.GetChannel(channelID); err != nil {
		return nil, err
	}
	return s.repo.ListCollectionsByChannel(channelID)
}

func (s *Service) ListCollectionsByChannelSlug(slug string) (model.Channel, []model.Collection, error) {
	channel, err := s.GetChannelBySlug(slug)
	if err != nil {
		return model.Channel{}, nil, err
	}
	collections, err := s.repo.ListCollectionsByChannel(channel.ID)
	return channel, collections, err
}

func (s *Service) GetCollection(id uuid.UUID) (model.Collection, error) {
	if id == uuid.Nil {
		return model.Collection{}, apperr.BadRequest("validation.invalid_request", "collection_id is required")
	}
	collection, err := s.repo.GetCollection(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Collection{}, apperr.NotFound("blog.collection_not_found", "Collection not found")
	}
	return collection, err
}

func (s *Service) ListUserCollections(userID uuid.UUID) ([]model.Collection, error) {
	if userID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	return s.repo.ListUserCollections(userID)
}

func (s *Service) CreateChannel(user authctx.CurrentUser, name string, slug string, description string, coverURL string, contentType string) (model.Channel, error) {
	if user.ID == uuid.Nil {
		return model.Channel{}, apperr.Unauthorized("Login required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Channel{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	if slug = strings.TrimSpace(slug); slug == "" {
		var err error
		slug, err = s.uniqueChannelSlug(name)
		if err != nil {
			return model.Channel{}, err
		}
	}
	contentType = model.NormalizeChannelContentType(contentType)
	if contentType == "" {
		contentType = model.ChannelContentTypeBlog
	}
	if !model.IsValidChannelContentType(contentType) {
		return model.Channel{}, apperr.BadRequest("validation.invalid_request", "content_type must be blog, podcast, or video")
	}
	channel := model.Channel{
		UserID:      &user.ID,
		Name:        name,
		Slug:        slug,
		Description: strings.TrimSpace(description),
		CoverURL:    strings.TrimSpace(coverURL),
		ContentType: contentType,
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&channel).Error; err != nil {
			return err
		}
		return s.ensureDefaultCollectionForChannelDB(tx, channel.ID)
	}); err != nil {
		return model.Channel{}, err
	}
	return s.repo.GetChannel(channel.ID)
}

func (s *Service) UpdateChannel(user authctx.CurrentUser, channelID uuid.UUID, name string, slug string, description string, coverURL string) (model.Channel, error) {
	channel, err := s.GetChannel(channelID)
	if err != nil {
		return model.Channel{}, err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return model.Channel{}, apperr.Forbidden("blog.channel_forbidden", "You do not have permission to modify this channel")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Channel{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	channel.Name = name
	if strings.TrimSpace(slug) != "" {
		channel.Slug = strings.TrimSpace(slug)
	}
	channel.Description = strings.TrimSpace(description)
	channel.CoverURL = strings.TrimSpace(coverURL)
	if err := s.repo.SaveChannel(&channel); err != nil {
		return model.Channel{}, err
	}
	return s.repo.GetChannel(channel.ID)
}

func (s *Service) DeleteChannel(user authctx.CurrentUser, channelID uuid.UUID) error {
	channel, err := s.GetChannel(channelID)
	if err != nil {
		return err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return apperr.Forbidden("blog.channel_forbidden", "You do not have permission to delete this channel")
	}
	return s.repo.DeleteChannel(channel.ID)
}

func (s *Service) CreateCollection(user authctx.CurrentUser, channelID uuid.UUID, name string, description string, coverURL string) (model.Collection, error) {
	channel, err := s.GetChannel(channelID)
	if err != nil {
		return model.Collection{}, err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return model.Collection{}, apperr.Forbidden("blog.channel_forbidden", "You do not have permission to add collections to this channel")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Collection{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	collection := model.Collection{
		ChannelID:   channelID,
		Name:        name,
		Description: strings.TrimSpace(description),
		CoverURL:    strings.TrimSpace(coverURL),
	}
	if err := s.repo.CreateCollection(&collection); err != nil {
		return model.Collection{}, err
	}
	return s.repo.GetCollection(collection.ID)
}

func (s *Service) UpdateCollection(user authctx.CurrentUser, collectionID uuid.UUID, name string, description string, coverURL string) (model.Collection, error) {
	collection, err := s.GetCollection(collectionID)
	if err != nil {
		return model.Collection{}, err
	}
	channel, err := s.GetChannel(collection.ChannelID)
	if err != nil {
		return model.Collection{}, err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return model.Collection{}, apperr.Forbidden("blog.collection_forbidden", "You do not have permission to modify this collection")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Collection{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	collection.Name = name
	collection.Description = strings.TrimSpace(description)
	collection.CoverURL = strings.TrimSpace(coverURL)
	if err := s.repo.SaveCollection(&collection); err != nil {
		return model.Collection{}, err
	}
	return s.repo.GetCollection(collection.ID)
}

func (s *Service) DeleteCollection(user authctx.CurrentUser, collectionID uuid.UUID) error {
	collection, err := s.GetCollection(collectionID)
	if err != nil {
		return err
	}
	channel, err := s.GetChannel(collection.ChannelID)
	if err != nil {
		return err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return apperr.Forbidden("blog.collection_forbidden", "You do not have permission to delete this collection")
	}
	return s.repo.DeleteCollection(collection.ID)
}

func (s *Service) CountPostLikes(postID uuid.UUID) (int64, error) {
	if postID == uuid.Nil {
		return 0, apperr.BadRequest("validation.invalid_request", "post_id is required")
	}
	return s.repo.CountPostLikes(postID)
}

func (s *Service) ListBookmarks(user authctx.CurrentUser, folderID *uuid.UUID, sort string) ([]model.Bookmark, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	return s.repo.ListBookmarks(user.ID, folderID, sort)
}

func (s *Service) ListBookmarkItems(user authctx.CurrentUser, folderID *uuid.UUID, sort string) ([]BookmarkListItemDTO, error) {
	bookmarks, err := s.ListBookmarks(user, folderID, sort)
	if err != nil {
		return nil, err
	}

	postIDs := make([]uuid.UUID, 0, len(bookmarks))
	seen := make(map[uuid.UUID]struct{}, len(bookmarks))
	for _, bookmark := range bookmarks {
		if bookmark.Post == nil {
			continue
		}
		if _, exists := seen[bookmark.PostID]; exists {
			continue
		}
		seen[bookmark.PostID] = struct{}{}
		postIDs = append(postIDs, bookmark.PostID)
	}

	type engagementCount struct {
		PostID        uuid.UUID `gorm:"column:post_id"`
		LikesCount    int64     `gorm:"column:likes_count"`
		CommentsCount int64     `gorm:"column:comments_count"`
	}
	countsByPostID := make(map[uuid.UUID]engagementCount, len(postIDs))
	if len(postIDs) > 0 {
		var counts []engagementCount
		if err := s.db.Model(&model.Post{}).Select(`posts.id AS post_id,
			(SELECT COUNT(*) FROM likes WHERE likes.target_type = 'post' AND likes.target_id = posts.id AND likes.deleted_at IS NULL) AS likes_count,
			(SELECT COUNT(*) FROM comments WHERE comments.target_type = 'post' AND comments.target_id = posts.id AND comments.status = 'visible' AND comments.deleted_at IS NULL) AS comments_count`).
			Where("posts.id IN ?", postIDs).
			Scan(&counts).Error; err != nil {
			return nil, err
		}
		for _, count := range counts {
			countsByPostID[count.PostID] = count
		}
	}

	items := make([]BookmarkListItemDTO, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		item := BookmarkListItemDTO{Bookmark: bookmark}
		if bookmark.Post != nil {
			count := countsByPostID[bookmark.PostID]
			item.Bookmark.Post = nil
			item.Post = &BookmarkPostDTO{
				Post:          *bookmark.Post,
				LikesCount:    count.LikesCount,
				CommentsCount: count.CommentsCount,
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) CreateBookmark(user authctx.CurrentUser, postID uuid.UUID, folderID *uuid.UUID) (model.Bookmark, error) {
	if user.ID == uuid.Nil {
		return model.Bookmark{}, apperr.Unauthorized("Login required")
	}
	if folderID == nil || *folderID == uuid.Nil {
		return model.Bookmark{}, apperr.BadRequest("validation.invalid_request", "bookmark_folder_id is required")
	}
	post, err := s.repo.GetPost(postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Bookmark{}, apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return model.Bookmark{}, err
	}
	if post.Status == "draft" {
		if post.UserID != user.ID {
			return model.Bookmark{}, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
		}
	} else {
		allowed, err := canViewPublishedPost(s.db, &user.ID, post)
		if err != nil {
			return model.Bookmark{}, err
		}
		if !allowed {
			return model.Bookmark{}, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
		}
	}
	var folder model.BookmarkFolder
	if err := s.db.Where("id = ? AND user_id = ?", *folderID, user.ID).First(&folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Bookmark{}, apperr.NotFound("blog.bookmark_folder_not_found", "Bookmark folder not found")
		}
		return model.Bookmark{}, err
	}
	var bookmark model.Bookmark
	err = s.db.Where("user_id = ? AND post_id = ?", user.ID, postID).First(&bookmark).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		bookmark = model.Bookmark{UserID: user.ID, PostID: postID, BookmarkFolderID: folderID}
		if err := s.db.Create(&bookmark).Error; err != nil {
			return model.Bookmark{}, err
		}
		return bookmark, nil
	}
	if err != nil {
		return model.Bookmark{}, err
	}
	if bookmark.BookmarkFolderID == nil || *bookmark.BookmarkFolderID != *folderID {
		if err := s.db.Model(&bookmark).Update("bookmark_folder_id", *folderID).Error; err != nil {
			return model.Bookmark{}, err
		}
		bookmark.BookmarkFolderID = folderID
	}
	return bookmark, nil
}

func (s *Service) DeleteBookmark(user authctx.CurrentUser, bookmarkID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteBookmark(bookmarkID, user.ID)
}

func (s *Service) ListBookmarkFolders(user authctx.CurrentUser) ([]model.BookmarkFolder, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	return s.repo.ListBookmarkFolders(user.ID)
}

func (s *Service) CreateBookmarkFolder(user authctx.CurrentUser, name string) (model.BookmarkFolder, error) {
	if user.ID == uuid.Nil {
		return model.BookmarkFolder{}, apperr.Unauthorized("Login required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return model.BookmarkFolder{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	folder := model.BookmarkFolder{UserID: user.ID, Name: name}
	if err := s.repo.CreateBookmarkFolder(&folder); err != nil {
		return model.BookmarkFolder{}, err
	}
	return folder, nil
}

func (s *Service) DeleteBookmarkFolder(user authctx.CurrentUser, folderID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.repo.DeleteBookmarkFolder(folderID, user.ID)
}

func (s *Service) ListComments(postID uuid.UUID, viewerID *uuid.UUID) ([]model.Comment, error) {
	post, err := s.repo.GetPost(postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return nil, err
	}
	if post.Status == "draft" {
		if viewerID == nil || post.UserID != *viewerID {
			return nil, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
		}
	} else {
		allowed, err := canViewPublishedPost(s.db, viewerID, post)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return nil, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
		}
	}

	var comments []model.Comment
	if err := s.db.Preload("User").Where("target_type = ? AND target_id = ? AND status = ?", "post", postID, "visible").Order("created_at ASC").Find(&comments).Error; err != nil {
		return nil, err
	}
	return comments, nil
}

func (s *Service) CreateComment(user *authctx.CurrentUser, postID uuid.UUID, guestName string, content string, timestampSec *int) (model.Comment, error) {
	post, err := s.repo.GetPost(postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Comment{}, apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return model.Comment{}, err
	}
	if !post.AllowComments {
		return model.Comment{}, apperr.Forbidden("blog.comments_disabled", "Comments are disabled for this post")
	}

	var viewerID *uuid.UUID
	if user != nil && user.ID != uuid.Nil {
		viewerID = &user.ID
	}
	if post.Status == "draft" {
		if viewerID == nil || post.UserID != *viewerID {
			return model.Comment{}, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
		}
	} else {
		allowed, err := canViewPublishedPost(s.db, viewerID, post)
		if err != nil {
			return model.Comment{}, err
		}
		if !allowed {
			return model.Comment{}, apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
		}
	}

	comment := model.Comment{
		TargetType:   "post",
		TargetID:     post.ID,
		UserID:       model.NullableUserUUID{},
		GuestName:    strings.TrimSpace(guestName),
		Content:      strings.TrimSpace(content),
		TimestampSec: timestampSec,
		Status:       "visible",
	}
	if user != nil && user.ID != uuid.Nil {
		comment.UserID = model.NewNullableUserUUID(user.ID)
	}
	if err := s.db.Create(&comment).Error; err != nil {
		return model.Comment{}, err
	}
	return comment, nil
}

func (s *Service) DeleteComment(user authctx.CurrentUser, commentID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	var comment model.Comment
	if err := s.db.First(&comment, "id = ?", commentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("blog.comment_not_found", "Comment not found")
		}
		return err
	}
	isPostOwner := false
	if comment.TargetType == "post" {
		var post model.Post
		if err := s.db.Select("user_id").Where("id = ?", comment.TargetID).First(&post).Error; err == nil {
			isPostOwner = post.UserID == user.ID
		}
	}
	if !comment.UserID.Valid {
		if !isPostOwner {
			return apperr.Forbidden("blog.comment_forbidden", "You don't have permission to delete this comment")
		}
	} else if comment.UserID.UUID != user.ID && !isPostOwner {
		return apperr.Forbidden("blog.comment_forbidden", "You don't have permission to delete this comment")
	}
	return s.db.Delete(&comment).Error
}

func (s *Service) ToggleLike(user authctx.CurrentUser, targetType string, targetID uuid.UUID, isLike bool) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	switch targetType {
	case "post":
		post, err := s.repo.GetPost(targetID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("blog.post_not_found", "Post not found")
			}
			return err
		}
		if post.Status == "draft" {
			if post.UserID != user.ID {
				return apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
			}
		} else {
			allowed, err := canViewPublishedPost(s.db, &user.ID, post)
			if err != nil {
				return err
			}
			if !allowed {
				return apperr.Forbidden("blog.post_forbidden", "You don't have permission to interact with this post")
			}
		}
	case "comment":
		var comment model.Comment
		if err := s.db.First(&comment, targetID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("blog.comment_not_found", "Comment not found")
			}
			return err
		}
	default:
		return apperr.BadRequest("validation.invalid_request", "target_type is invalid")
	}

	if isLike {
		like := model.Like{UserID: user.ID, TargetType: targetType, TargetID: targetID}
		return s.db.Where(model.Like{UserID: user.ID, TargetType: targetType, TargetID: targetID}).FirstOrCreate(&like).Error
	}
	return s.db.Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, targetType, targetID).Delete(&model.Like{}).Error
}

func (s *Service) CreateDefaultChannelForUser(userID uuid.UUID, displayName string) (model.Channel, error) {
	if userID == uuid.Nil {
		return model.Channel{}, apperr.BadRequest("validation.invalid_request", "user_id is required")
	}

	var existing model.Channel
	err := s.db.Where("user_id = ? AND is_default = ?", userID, true).First(&existing).Error
	if err == nil {
		if ensureErr := s.ensureDefaultCollectionForChannel(existing.ID); ensureErr != nil {
			return model.Channel{}, ensureErr
		}
		if existing.ContentType == "" {
			if saveErr := s.db.Model(&existing).Update("content_type", model.ChannelContentTypeBlog).Error; saveErr != nil {
				return model.Channel{}, saveErr
			}
			existing.ContentType = model.ChannelContentTypeBlog
		}
		if ensureErr := s.upsertUserDefaultChannelSelection(userID, model.ChannelContentTypeBlog, existing.ID); ensureErr != nil {
			return model.Channel{}, ensureErr
		}
		return existing, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Channel{}, err
	}

	baseName := strings.TrimSpace(displayName)
	if baseName == "" {
		baseName = "默认频道"
	}

	name, err := s.uniqueChannelName(baseName)
	if err != nil {
		return model.Channel{}, err
	}
	slug, err := s.uniqueChannelSlug(baseName)
	if err != nil {
		return model.Channel{}, err
	}

	channel := model.Channel{
		UserID:      &userID,
		Name:        name,
		Slug:        slug,
		Description: "默认合集",
		ContentType: model.ChannelContentTypeBlog,
		IsDefault:   true,
	}
	if err := s.db.Create(&channel).Error; err != nil {
		return model.Channel{}, err
	}
	if err := s.ensureDefaultCollectionForChannel(channel.ID); err != nil {
		return model.Channel{}, err
	}
	if err := s.upsertUserDefaultChannelSelection(userID, model.ChannelContentTypeBlog, channel.ID); err != nil {
		return model.Channel{}, err
	}
	return channel, nil
}

func (s *Service) CreatePost(user authctx.CurrentUser, req CreatePostRequest) (model.Post, error) {
	if user.ID == uuid.Nil {
		return model.Post{}, apperr.Unauthorized("Login required")
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" || req.CollectionID == uuid.Nil {
		return model.Post{}, apperr.BadRequest("validation.invalid_request", "title, content and collection_id are required")
	}
	if len(req.CollectionIDs) > 0 {
		return model.Post{}, apperr.BadRequest("validation.invalid_request", "collection_ids is no longer supported")
	}

	var collection model.Collection
	if err := s.db.Preload("Channel").First(&collection, "id = ?", req.CollectionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Post{}, apperr.NotFound("blog.collection_not_found", "Collection not found")
		}
		return model.Post{}, err
	}
	if collection.Channel == nil {
		return model.Post{}, apperr.NotFound("blog.channel_not_found", "Channel not found")
	}
	channel, err := s.repo.GetChannel(collection.ChannelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Post{}, apperr.NotFound("blog.channel_not_found", "Channel not found")
		}
		return model.Post{}, err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return model.Post{}, apperr.Forbidden("blog.channel_forbidden", "You don't have permission to create post in this channel")
	}
	if req.ChannelID != uuid.Nil && req.ChannelID != channel.ID {
		return model.Post{}, apperr.BadRequest("validation.invalid_request", "collection does not belong to selected channel")
	}
	if isChannelBanned(channel) && strings.TrimSpace(req.Status) == "published" {
		return model.Post{}, apperr.Forbidden("blog.channel_banned", "Banned channel cannot publish posts")
	}

	visibility := strings.TrimSpace(req.Visibility)
	if visibility == "" {
		visibility = "public"
	}
	if _, ok := allowedPostVisibilities[visibility]; !ok {
		return model.Post{}, apperr.BadRequest("blog.invalid_visibility", "visibility is invalid")
	}
	status := strings.TrimSpace(req.Status)
	if status == "" {
		status = "draft"
	}
	if _, ok := allowedPostStatuses[status]; !ok {
		return model.Post{}, apperr.BadRequest("blog.invalid_status_transition", "status is invalid")
	}

	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = strings.TrimSpace(req.Excerpt)
	}

	post := model.Post{
		UserID:       user.ID,
		ChannelID:    &channel.ID,
		CollectionID: &collection.ID,
		Title:        strings.TrimSpace(req.Title),
		Content:      strings.TrimSpace(req.Content),
		Summary:      summary,
		CoverURL:     strings.TrimSpace(req.CoverURL),
		Visibility:   visibility,
		Status:       status,
	}
	if status == "published" {
		now := time.Now().UTC()
		post.PublishedAt = &now
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var maxPosition int
		if err := tx.Model(&model.Post{}).Where("collection_id = ?", collection.ID).Select("COALESCE(MAX(collection_position), -1)").Scan(&maxPosition).Error; err != nil {
			return err
		}
		post.CollectionPosition = maxPosition + 1
		if err := tx.Create(&post).Error; err != nil {
			return err
		}
		if post.Status == "published" {
			return saveBlogPostVersion(tx, post, user.ID)
		}
		return nil
	}); err != nil {
		return model.Post{}, err
	}
	post.Channel = &channel
	post.Collection = &collection
	return post, nil
}

func saveBlogPostVersion(tx *gorm.DB, post model.Post, editorID uuid.UUID) error {
	if post.CollectionID == nil || *post.CollectionID == uuid.Nil {
		return apperr.BadRequest("validation.invalid_request", "collection_id is required")
	}
	var maxVersion int
	if err := tx.Model(&model.BlogPostVersion{}).Where("post_id = ?", post.ID).Select("COALESCE(MAX(version), 0)").Scan(&maxVersion).Error; err != nil {
		return err
	}
	version := model.BlogPostVersion{
		PostID:        post.ID,
		Version:       maxVersion + 1,
		EditorID:      editorID,
		Title:         post.Title,
		Content:       post.Content,
		Summary:       post.Summary,
		CoverURL:      post.CoverURL,
		Visibility:    post.Visibility,
		AllowComments: post.AllowComments,
		CollectionID:  *post.CollectionID,
		PublishedAt:   post.PublishedAt,
	}
	return tx.Create(&version).Error
}

func (s *Service) ListPostVersions(user authctx.CurrentUser, postID uuid.UUID) ([]model.BlogPostVersion, error) {
	var post model.Post
	if err := s.db.First(&post, "id = ?", postID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return nil, err
	}
	if post.UserID != user.ID {
		return nil, apperr.Forbidden("blog.post_forbidden", "You don't have permission to view post versions")
	}
	var versions []model.BlogPostVersion
	if err := s.db.Where("post_id = ?", postID).Order("version DESC").Find(&versions).Error; err != nil {
		return nil, err
	}
	return versions, nil
}

func (s *Service) RestorePostVersion(user authctx.CurrentUser, postID uuid.UUID, versionNumber int) (model.Post, error) {
	var restored model.Post
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var post model.Post
		if err := tx.First(&post, "id = ?", postID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("blog.post_not_found", "Post not found")
			}
			return err
		}
		if post.UserID != user.ID {
			return apperr.Forbidden("blog.post_forbidden", "You don't have permission to restore this post")
		}
		var version model.BlogPostVersion
		if err := tx.Where("post_id = ? AND version = ?", postID, versionNumber).First(&version).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("blog.version_not_found", "Post version not found")
			}
			return err
		}
		var collection model.Collection
		if err := tx.First(&collection, "id = ?", version.CollectionID).Error; err != nil {
			return err
		}
		updates := map[string]any{
			"title": version.Title, "content": version.Content, "summary": version.Summary,
			"cover_url": version.CoverURL, "visibility": version.Visibility,
			"allow_comments": version.AllowComments, "collection_id": version.CollectionID,
			"channel_id": collection.ChannelID,
		}
		if err := tx.Model(&post).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Preload("Channel").Preload("Collection").First(&restored, "id = ?", postID).Error; err != nil {
			return err
		}
		return saveBlogPostVersion(tx, restored, user.ID)
	})
	return restored, err
}

func (s *Service) reorderCollectionPosts(collection model.Collection, orderedPostIDs []uuid.UUID, userID uuid.UUID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var posts []model.Post
		if err := tx.Where("collection_id = ?", collection.ID).Find(&posts).Error; err != nil {
			return err
		}
		if len(posts) != len(orderedPostIDs) {
			return apperr.BadRequest("validation.invalid_request", "post_ids must include every post in the collection")
		}

		postSet := make(map[uuid.UUID]model.Post, len(posts))
		for _, post := range posts {
			postSet[post.ID] = post
		}

		for _, postID := range orderedPostIDs {
			post, exists := postSet[postID]
			if !exists {
				return apperr.BadRequest("validation.invalid_request", "post_ids contains a post outside this collection")
			}
			if post.UserID != userID {
				return apperr.Forbidden("blog.post_forbidden", "You don't have permission to reorder this collection")
			}
			if post.CollectionID == nil || *post.CollectionID != collection.ID {
				return apperr.BadRequest("validation.invalid_request", "post_ids contains a post outside this collection channel")
			}
		}

		for position, postID := range orderedPostIDs {
			if err := tx.Model(&model.Post{}).
				Where("collection_id = ? AND id = ?", collection.ID, postID).
				Update("collection_position", position).Error; err != nil {
				return err
			}
		}

		return nil
	})
}

func ensureDefaultCollectionName() string {
	return "默认专栏"
}

func (s *Service) ensureDefaultCollectionForChannel(channelID uuid.UUID) error {
	return s.ensureDefaultCollectionForChannelDB(s.db, channelID)
}

func (s *Service) ensureDefaultCollectionForChannelDB(db *gorm.DB, channelID uuid.UUID) error {
	var collection model.Collection
	err := db.Where("channel_id = ? AND is_default = ?", channelID, true).First(&collection).Error
	if err == nil {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	name := ensureDefaultCollectionName()
	var softDeleted model.Collection
	softErr := db.Unscoped().Where("channel_id = ? AND name = ?", channelID, name).First(&softDeleted).Error
	if softErr == nil && softDeleted.DeletedAt.Valid {
		return db.Unscoped().Model(&softDeleted).Updates(map[string]any{
			"deleted_at": nil,
			"is_default": true,
			"name":       name,
		}).Error
	}
	if softErr != nil && !errors.Is(softErr, gorm.ErrRecordNotFound) {
		return softErr
	}

	collection = model.Collection{
		ChannelID:   channelID,
		Name:        name,
		Description: "默认合集",
		IsDefault:   true,
	}
	return db.Create(&collection).Error
}

func isChannelBanned(channel model.Channel) bool {
	if channel.BanUntil != nil {
		return channel.BanUntil.After(time.Now())
	}
	return strings.TrimSpace(channel.BanReason) != ""
}

func slugify(value string) string {
	slug := strings.ToLower(strings.TrimSpace(value))
	slug = slugInvalidChars.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "channel"
	}
	return slug
}

func (s *Service) uniqueChannelSlug(base string) (string, error) {
	baseSlug := slugify(base)
	candidate := baseSlug
	counter := 2
	namespace := sitehandle.NewService(s.db)
	for {
		err := namespace.ValidateChannelSlugAvailable(context.Background(), candidate, nil)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, sitehandle.ErrReserved) && !errors.Is(err, sitehandle.ErrTaken) {
			return "", err
		}
		candidate = fmt.Sprintf("%s-%d", baseSlug, counter)
		counter++
	}
}

func (s *Service) uniqueChannelName(base string) (string, error) {
	candidate := base
	counter := 2
	for {
		var count int64
		if err := s.db.Model(&model.Channel{}).Where("LOWER(name) = LOWER(?)", candidate).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s %d", base, counter)
		counter++
	}
}

func (s *Service) upsertUserDefaultChannelSelection(userID uuid.UUID, contentType string, channelID uuid.UUID) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "content_type"}},
		DoUpdates: clause.AssignmentColumns([]string{"channel_id", "updated_at"}),
	}).Create(&model.UserDefaultChannel{
		UserID:      userID,
		ContentType: contentType,
		ChannelID:   channelID,
	}).Error
}

func dedupeUUIDs(values []uuid.UUID) []uuid.UUID {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

package blog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
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
	"archived":  {},
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

func (s *Service) RecommendPostsByMode(mode recommendation.Mode, page int, pageSize int) ([]RecommendationItemDTO, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	var posts []model.Post
	if err := s.db.
		Preload("User").
		Preload("Channel").
		Where("status = ? AND visibility = ?", "published", "public").
		Order("pinned DESC, created_at DESC").
		Find(&posts).Error; err != nil {
		return nil, 0, err
	}

	candidates := make([]recommendation.Candidate, 0, len(posts))
	postByID := make(map[string]model.Post, len(posts))
	for _, post := range posts {
		candidates = append(candidates, recommendation.Candidate{
			Module:          "blog",
			EntityType:      recommendation.EntityBlog,
			EntityID:        post.ID.String(),
			SourceKey:       blogRecommendationSourceKey(post),
			QualityScore:    normalizeBlogRecommendationQuality(post),
			TrendScore:      normalizeBlogRecommendationTrend(post),
			FreshnessScore:  normalizeBlogRecommendationFreshness(post.CreatedAt, 14*24*time.Hour),
			AuthorityScore:  normalizeBlogRecommendationAuthority(post),
			ExposureScore:   0,
			EditorialScore:  0,
			PublishedAtUnix: post.CreatedAt.Unix(),
		})
		postByID[post.ID.String()] = post
	}

	ranked := recommendation.RankCandidates(mode, candidates, 0)
	items := make([]RecommendationItemDTO, 0, len(ranked))
	for _, rankedItem := range ranked {
		post, ok := postByID[rankedItem.EntityID]
		if !ok {
			continue
		}
		items = append(items, RecommendationItemDTO{
			ID:          post.ID.String(),
			Title:       post.Title,
			Summary:     post.Summary,
			ContentType: "blog",
			ImageURL:    post.CoverURL,
			TargetPath:  "/post/" + post.ID.String(),
			ScoreLabel:  blogRecommendationLabel(mode, rankedItem.FinalScore),
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

func blogRecommendationSourceKey(post model.Post) string {
	if post.ChannelID != nil {
		return post.ChannelID.String()
	}
	return post.UserID.String()
}

func normalizeBlogRecommendationQuality(post model.Post) float64 {
	ratingComponent := clampBlogRecommendation(float64(post.RatingAverageScore) / 100)
	countComponent := clampBlogRecommendation(float64(post.RatingCount) / 10)
	summaryComponent := 0.0
	if strings.TrimSpace(post.Summary) != "" {
		summaryComponent = 0.15
	}
	return clampBlogRecommendation(0.55*ratingComponent + 0.30*countComponent + summaryComponent)
}

func normalizeBlogRecommendationTrend(post model.Post) float64 {
	return clampBlogRecommendation(0.6*normalizeBlogRecommendationFreshness(post.CreatedAt, 7*24*time.Hour) + 0.4*clampBlogRecommendation(float64(post.RatingCount)/10))
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

func (s *Service) CreateChannel(user authctx.CurrentUser, name string, slug string, description string, coverURL string) (model.Channel, error) {
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
	channel := model.Channel{
		UserID:      &user.ID,
		Name:        name,
		Slug:        slug,
		Description: strings.TrimSpace(description),
		CoverURL:    strings.TrimSpace(coverURL),
		ContentType: model.ChannelContentTypeBlog,
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

func (s *Service) CreateBookmark(user authctx.CurrentUser, postID uuid.UUID, folderID *uuid.UUID) (model.Bookmark, error) {
	if user.ID == uuid.Nil {
		return model.Bookmark{}, apperr.Unauthorized("Login required")
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
	if folderID != nil && *folderID != uuid.Nil {
		var folder model.BookmarkFolder
		if err := s.db.Where("id = ? AND user_id = ?", *folderID, user.ID).First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.Bookmark{}, apperr.NotFound("blog.bookmark_folder_not_found", "Bookmark folder not found")
			}
			return model.Bookmark{}, err
		}
	}
	bookmark := model.Bookmark{UserID: user.ID, PostID: postID, BookmarkFolderID: folderID}
	if err := s.db.Where(model.Bookmark{UserID: user.ID, PostID: postID}).FirstOrCreate(&bookmark).Error; err != nil {
		return model.Bookmark{}, err
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
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Content) == "" || req.ChannelID == uuid.Nil {
		return model.Post{}, apperr.BadRequest("validation.invalid_request", "title, content and channel_id are required")
	}

	channel, err := s.repo.GetChannel(req.ChannelID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Post{}, apperr.NotFound("blog.channel_not_found", "Channel not found")
		}
		return model.Post{}, err
	}
	if channel.UserID == nil || *channel.UserID != user.ID {
		return model.Post{}, apperr.Forbidden("blog.channel_forbidden", "You don't have permission to create post in this channel")
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

	collectionIDs := dedupeUUIDs(req.CollectionIDs)
	if len(collectionIDs) > 0 {
		var collections []model.Collection
		if err := s.db.Where("id IN ?", collectionIDs).Find(&collections).Error; err != nil {
			return model.Post{}, err
		}
		if len(collections) != len(collectionIDs) {
			return model.Post{}, apperr.NotFound("blog.collection_not_found", "Collection not found")
		}
		for _, collection := range collections {
			if collection.ChannelID != channel.ID {
				return model.Post{}, apperr.BadRequest("validation.invalid_request", "collection does not belong to selected channel")
			}
		}
	}

	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		summary = strings.TrimSpace(req.Excerpt)
	}

	post := model.Post{
		UserID:     user.ID,
		ChannelID:  &channel.ID,
		Title:      strings.TrimSpace(req.Title),
		Content:    strings.TrimSpace(req.Content),
		Summary:    summary,
		CoverURL:   strings.TrimSpace(req.CoverURL),
		Visibility: visibility,
		Status:     status,
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&post).Error; err != nil {
			return err
		}

		if err := s.ensureDefaultCollectionForChannelDB(tx, channel.ID); err != nil {
			return err
		}
		var defaultCollection model.Collection
		if err := tx.Where("channel_id = ? AND is_default = ?", channel.ID, true).First(&defaultCollection).Error; err != nil {
			return err
		}
		collectionsToAssign := []model.Collection{defaultCollection}
		if len(collectionIDs) > 0 {
			collections := make([]model.Collection, 0, len(collectionIDs))
			if err := tx.Where("id IN ?", collectionIDs).Find(&collections).Error; err != nil {
				return err
			}
			for _, collection := range collections {
				if collection.ID == defaultCollection.ID {
					continue
				}
				collectionsToAssign = append(collectionsToAssign, collection)
			}
		}
		return tx.Model(&post).Association("Collections").Append(collectionsToAssign)
	}); err != nil {
		return model.Post{}, err
	}

	return post, nil
}

func (s *Service) SetRating(user authctx.CurrentUser, postID uuid.UUID, score int) (RatingSummary, error) {
	if user.ID == uuid.Nil {
		return RatingSummary{}, apperr.Unauthorized("Login required")
	}
	if score < 1 || score > 10 {
		return RatingSummary{}, apperr.BadRequest("blog.rating_invalid_score", "score must be between 1 and 10")
	}

	post, err := s.repo.GetPost(postID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RatingSummary{}, apperr.NotFound("blog.post_not_found", "Post not found")
		}
		return RatingSummary{}, err
	}
	if post.UserID == user.ID {
		return RatingSummary{}, apperr.Forbidden("blog.rating_self_forbidden", "Authors cannot rate their own posts")
	}
	if post.Status != "published" || post.Visibility != "public" {
		return RatingSummary{}, apperr.Forbidden("blog.rating_forbidden", "Only published public posts can be rated")
	}

	rating := model.BlogPostRating{PostID: postID, UserID: user.ID}
	assignments := map[string]any{"score": score}
	if err := s.db.Where("post_id = ? AND user_id = ?", postID, user.ID).Assign(assignments).FirstOrCreate(&rating).Error; err != nil {
		return RatingSummary{}, err
	}

	myScore := score
	return s.recalculateRating(postID, &myScore)
}

func (s *Service) recalculateRating(postID uuid.UUID, myScore *int) (RatingSummary, error) {
	type aggregate struct {
		Count int
		Avg   float64
	}

	var agg aggregate
	if err := s.db.Model(&model.BlogPostRating{}).
		Select("COUNT(*) as count, AVG(score) as avg").
		Where("post_id = ?", postID).
		Scan(&agg).Error; err != nil {
		return RatingSummary{}, err
	}

	averageScore := int(agg.Avg * 10)
	summary := RatingSummary{
		AverageScore: averageScore,
		AverageStars: float64(averageScore) / 20,
		RatingCount:  agg.Count,
		MyScore:      myScore,
	}

	if err := s.db.Model(&model.Post{}).Where("id = ?", postID).Updates(map[string]any{
		"rating_average_score": averageScore,
		"rating_count":         agg.Count,
	}).Error; err != nil {
		return RatingSummary{}, err
	}

	if myScore != nil {
		_ = audit.Record(s.db, audit.Entry{Action: "blog.rating.set", EntityType: "post", EntityID: &postID})
	}

	return summary, nil
}

func (s *Service) appendPostCollectionAtTail(postID uuid.UUID, collectionID uuid.UUID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var link model.PostCollection
		if err := tx.Where("post_id = ? AND collection_id = ?", postID, collectionID).First(&link).Error; err != nil {
			return err
		}

		var maxPosition int
		if err := tx.Model(&model.PostCollection{}).
			Where("collection_id = ?", collectionID).
			Select("COALESCE(MAX(position), -1)").
			Scan(&maxPosition).Error; err != nil {
			return err
		}

		return tx.Model(&model.PostCollection{}).
			Where("post_id = ? AND collection_id = ?", postID, collectionID).
			Update("position", maxPosition+1).Error
	})
}

func (s *Service) reorderCollectionPosts(collection model.Collection, orderedPostIDs []uuid.UUID, userID uuid.UUID) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var links []model.PostCollection
		if err := tx.Where("collection_id = ?", collection.ID).Find(&links).Error; err != nil {
			return err
		}
		if len(links) != len(orderedPostIDs) {
			return apperr.BadRequest("validation.invalid_request", "post_ids must include every post in the collection")
		}

		linkSet := make(map[uuid.UUID]model.PostCollection, len(links))
		for _, link := range links {
			linkSet[link.PostID] = link
		}

		for _, postID := range orderedPostIDs {
			link, exists := linkSet[postID]
			if !exists {
				return apperr.BadRequest("validation.invalid_request", "post_ids contains a post outside this collection")
			}
			if link.CollectionID != collection.ID {
				return apperr.BadRequest("validation.invalid_request", "post_ids contains a post outside this collection")
			}
		}

		var posts []model.Post
		if err := tx.Where("id IN ?", orderedPostIDs).Find(&posts).Error; err != nil {
			return err
		}
		if len(posts) != len(orderedPostIDs) {
			return apperr.BadRequest("validation.invalid_request", "post_ids contains an unknown post")
		}
		for _, post := range posts {
			if post.UserID != userID {
				return apperr.Forbidden("blog.post_forbidden", "You don't have permission to reorder this collection")
			}
			if post.ChannelID == nil || *post.ChannelID != collection.ChannelID {
				return apperr.BadRequest("validation.invalid_request", "post_ids contains a post outside this collection channel")
			}
		}

		for position, postID := range orderedPostIDs {
			if err := tx.Model(&model.PostCollection{}).
				Where("collection_id = ? AND post_id = ?", collection.ID, postID).
				Update("position", position).Error; err != nil {
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

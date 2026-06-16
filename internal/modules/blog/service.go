package blog

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
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
		IsDefault:   true,
	}
	if err := s.db.Create(&channel).Error; err != nil {
		return model.Channel{}, err
	}
	if err := s.ensureDefaultCollectionForChannel(channel.ID); err != nil {
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

	post := model.Post{
		UserID:     user.ID,
		ChannelID:  &channel.ID,
		Title:      strings.TrimSpace(req.Title),
		Content:    strings.TrimSpace(req.Content),
		Summary:    strings.TrimSpace(req.Excerpt),
		CoverURL:   strings.TrimSpace(req.CoverURL),
		Visibility: visibility,
		Status:     status,
	}
	if err := s.db.Create(&post).Error; err != nil {
		return model.Post{}, err
	}

	if err := s.ensureDefaultCollectionForChannel(channel.ID); err != nil {
		return model.Post{}, err
	}
	var defaultCollection model.Collection
	if err := s.db.Where("channel_id = ? AND is_default = ?", channel.ID, true).First(&defaultCollection).Error; err != nil {
		return model.Post{}, err
	}
	collectionsToAssign := []model.Collection{defaultCollection}
	if len(collectionIDs) > 0 {
		collections := make([]model.Collection, 0, len(collectionIDs))
		if err := s.db.Where("id IN ?", collectionIDs).Find(&collections).Error; err != nil {
			return model.Post{}, err
		}
		for _, collection := range collections {
			if collection.ID == defaultCollection.ID {
				continue
			}
			collectionsToAssign = append(collectionsToAssign, collection)
		}
	}
	if err := s.db.Model(&post).Association("Collections").Append(collectionsToAssign); err != nil {
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

func ensureDefaultCollectionName() string {
	return "默认专栏"
}

func (s *Service) ensureDefaultCollectionForChannel(channelID uuid.UUID) error {
	var collection model.Collection
	err := s.db.Where("channel_id = ? AND is_default = ?", channelID, true).First(&collection).Error
	if err == nil {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	name := ensureDefaultCollectionName()
	var softDeleted model.Collection
	softErr := s.db.Unscoped().Where("channel_id = ? AND name = ?", channelID, name).First(&softDeleted).Error
	if softErr == nil && softDeleted.DeletedAt.Valid {
		return s.db.Unscoped().Model(&softDeleted).Updates(map[string]any{
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
	return s.db.Create(&collection).Error
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
	for {
		var count int64
		if err := s.db.Model(&model.Channel{}).Where("slug = ?", candidate).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
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

package blog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"atoman/internal/model"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/sitehandle"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var slugInvalidChars = regexp.MustCompile(`[^a-z0-9一-龥]+`)

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
	requestedSlug := strings.TrimSpace(slug)
	if requestedSlug == "" {
		requestedSlug = name
	}
	resolvedSlug, err := s.uniqueChannelSlug(requestedSlug)
	if err != nil {
		return model.Channel{}, err
	}
	channel := model.Channel{
		UserID:      &user.ID,
		Name:        name,
		Slug:        resolvedSlug,
		Description: strings.TrimSpace(description),
		CoverURL:    strings.TrimSpace(coverURL),
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
	if requestedSlug := strings.TrimSpace(slug); requestedSlug != "" && requestedSlug != channel.Slug {
		resolvedSlug, err := s.uniqueChannelSlug(requestedSlug, &channel.ID)
		if err != nil {
			return model.Channel{}, err
		}
		channel.Slug = resolvedSlug
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
	return studioapi.NewService(s.db).DeleteChannel(user, channel.ID)
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
		ContentType: "blog",
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
	return studioapi.NewService(s.db).DeleteCollection(user, studioapi.ModuleBlog, collection.ID)
}

func (s *Service) CreateDefaultChannelForUser(userID uuid.UUID, displayName string) (model.Channel, error) {
	if userID == uuid.Nil {
		return model.Channel{}, apperr.BadRequest("validation.invalid_request", "user_id is required")
	}

	var state model.UserStudioState
	err := s.db.Preload("Channel").First(&state, "user_id = ?", userID).Error
	if err == nil && state.Channel != nil && state.Channel.UserID != nil && *state.Channel.UserID == userID {
		existing := *state.Channel
		if ensureErr := s.ensureDefaultCollectionForChannel(existing.ID); ensureErr != nil {
			return model.Channel{}, ensureErr
		}
		return existing, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Channel{}, err
	}

	var existing model.Channel
	err = s.db.Where("user_id = ?", userID).Order("created_at ASC, id ASC").First(&existing).Error
	if err == nil {
		if ensureErr := s.ensureDefaultCollectionForChannel(existing.ID); ensureErr != nil {
			return model.Channel{}, ensureErr
		}
		if saveErr := s.saveStudioState(userID, existing.ID); saveErr != nil {
			return model.Channel{}, saveErr
		}
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
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
	}
	if err := s.db.Create(&channel).Error; err != nil {
		return model.Channel{}, err
	}
	if err := s.ensureDefaultCollectionForChannel(channel.ID); err != nil {
		return model.Channel{}, err
	}
	if err := s.ensureStudioState(userID, channel.ID); err != nil {
		return model.Channel{}, err
	}
	return channel, nil
}

func ensureDefaultCollectionName() string {
	return "默认专栏"
}

func (s *Service) ensureDefaultCollectionForChannel(channelID uuid.UUID) error {
	return s.ensureDefaultCollectionForChannelDB(s.db, channelID)
}

func (s *Service) ensureDefaultCollectionForChannelDB(db *gorm.DB, channelID uuid.UUID) error {
	var collection model.Collection
	err := db.Where("channel_id = ? AND content_type = ? AND is_default = ?", channelID, "blog", true).First(&collection).Error
	if err == nil {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	name := ensureDefaultCollectionName()
	var softDeleted model.Collection
	softErr := db.Unscoped().Where("channel_id = ? AND content_type = ? AND name = ?", channelID, "blog", name).First(&softDeleted).Error
	if softErr == nil && softDeleted.DeletedAt.Valid {
		return db.Unscoped().Model(&softDeleted).Updates(map[string]any{
			"deleted_at":   nil,
			"content_type": "blog",
			"is_default":   true,
			"name":         name,
		}).Error
	}
	if softErr != nil && !errors.Is(softErr, gorm.ErrRecordNotFound) {
		return softErr
	}

	collection = model.Collection{
		ChannelID:   channelID,
		ContentType: "blog",
		Name:        name,
		Description: "默认合集",
		IsDefault:   true,
	}
	return db.Create(&collection).Error
}

func (s *Service) saveStudioState(userID, channelID uuid.UUID) error {
	return s.db.Save(&model.UserStudioState{UserID: userID, ChannelID: &channelID}).Error
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

func (s *Service) uniqueChannelSlug(base string, excludeID ...*uuid.UUID) (string, error) {
	baseSlug := slugify(base)
	if _, err := sitehandle.Normalize(baseSlug); err != nil {
		baseSlug = "channel"
	}
	candidate := baseSlug
	counter := 2
	namespace := sitehandle.NewService(s.db)
	var excluded *uuid.UUID
	if len(excludeID) > 0 {
		excluded = excludeID[0]
	}
	for {
		err := namespace.ValidateChannelSlugAvailable(context.Background(), candidate, excluded)
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, sitehandle.ErrInvalid) {
			return "", apperr.BadRequest("validation.invalid_request", "slug is invalid")
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

func (s *Service) ensureStudioState(userID, channelID uuid.UUID) error {
	var state model.UserStudioState
	err := s.db.First(&state, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(&model.UserStudioState{UserID: userID, ChannelID: &channelID}).Error
	}
	if err != nil {
		return err
	}
	if state.ChannelID == nil {
		return s.db.Model(&state).Update("channel_id", channelID).Error
	}
	return nil
}

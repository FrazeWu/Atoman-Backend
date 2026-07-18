package studio

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/sitehandle"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var channelSlugInvalidChars = regexp.MustCompile(`[^a-z0-9一-龥]+`)

type Service struct {
	db   *gorm.DB
	repo *Repo
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db, repo: NewRepo(db)}
}

func (s *Service) GetState(user authctx.CurrentUser) (StateResponse, error) {
	if err := requireUser(user); err != nil {
		return StateResponse{}, err
	}
	channels, err := s.repo.ListOwnedChannels(user.ID)
	if err != nil {
		return StateResponse{}, err
	}
	response := StateResponse{Channels: summarizeChannels(channels)}
	state, err := s.repo.GetState(user.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) || state.ChannelID == nil {
		return response, nil
	}
	if err != nil {
		return StateResponse{}, err
	}
	for _, channel := range channels {
		if channel.ID == *state.ChannelID {
			summary := summarizeChannel(channel)
			response.CurrentChannel = &summary
			break
		}
	}
	return response, nil
}

func (s *Service) SetState(user authctx.CurrentUser, channelID uuid.UUID) (StateResponse, error) {
	if err := requireUser(user); err != nil {
		return StateResponse{}, err
	}
	channel, err := s.ownedChannel(user.ID, channelID)
	if err != nil {
		return StateResponse{}, err
	}
	state := model.UserStudioState{UserID: user.ID, ChannelID: &channel.ID}
	if err := s.db.Save(&state).Error; err != nil {
		return StateResponse{}, err
	}
	return s.GetState(user)
}

func (s *Service) ListChannels(user authctx.CurrentUser) ([]ChannelSummary, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}
	channels, err := s.repo.ListOwnedChannels(user.ID)
	if err != nil {
		return nil, err
	}
	return summarizeChannels(channels), nil
}

func (s *Service) CreateChannel(user authctx.CurrentUser, input CreateChannelInput) (ChannelSummary, error) {
	if err := requireUser(user); err != nil {
		return ChannelSummary{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return ChannelSummary{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	slug, err := s.availableChannelSlug(input.Slug, name, nil)
	if err != nil {
		return ChannelSummary{}, err
	}
	channel := model.Channel{
		UserID:      &user.ID,
		Name:        name,
		Slug:        slug,
		Description: strings.TrimSpace(input.Description),
		CoverURL:    strings.TrimSpace(input.CoverURL),
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&channel).Error; err != nil {
			return err
		}
		var state model.UserStudioState
		result := tx.First(&state, "user_id = ?", user.ID)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return tx.Create(&model.UserStudioState{UserID: user.ID, ChannelID: &channel.ID}).Error
		}
		if state.ChannelID == nil {
			return tx.Model(&state).Update("channel_id", channel.ID).Error
		}
		return nil
	})
	if err != nil {
		return ChannelSummary{}, err
	}
	return summarizeChannel(channel), nil
}

func (s *Service) UpdateChannel(user authctx.CurrentUser, channelID uuid.UUID, input UpdateChannelInput) (ChannelSummary, error) {
	if err := requireUser(user); err != nil {
		return ChannelSummary{}, err
	}
	channel, err := s.ownedChannel(user.ID, channelID)
	if err != nil {
		return ChannelSummary{}, err
	}
	if input.Name != nil {
		channel.Name = strings.TrimSpace(*input.Name)
		if channel.Name == "" {
			return ChannelSummary{}, apperr.BadRequest("validation.invalid_request", "name is required")
		}
	}
	if input.Slug != nil {
		channel.Slug, err = s.availableChannelSlug(*input.Slug, channel.Name, &channel.ID)
		if err != nil {
			return ChannelSummary{}, err
		}
	}
	if input.Description != nil {
		channel.Description = strings.TrimSpace(*input.Description)
	}
	if input.CoverURL != nil {
		channel.CoverURL = strings.TrimSpace(*input.CoverURL)
	}
	if err := s.db.Save(&channel).Error; err != nil {
		return ChannelSummary{}, err
	}
	return summarizeChannel(channel), nil
}

func (s *Service) DeleteChannel(user authctx.CurrentUser, channelID uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	channel, err := s.ownedChannel(user.ID, channelID)
	if err != nil {
		return err
	}
	var contentCount int64
	if err := s.db.Model(&model.Post{}).Where("channel_id = ?", channel.ID).Count(&contentCount).Error; err != nil {
		return err
	}
	if contentCount == 0 {
		if err := s.db.Model(&model.Video{}).Where("channel_id = ?", channel.ID).Count(&contentCount).Error; err != nil {
			return err
		}
	}
	if contentCount > 0 {
		return apperr.Conflict("studio.channel_not_empty", "Channel must be empty before deletion")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", channel.ID).Delete(&model.Collection{}).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channel.ID).Delete(&model.StudioModuleSettings{}).Error; err != nil {
			return err
		}
		var replacement model.Channel
		replacementResult := tx.Where("user_id = ? AND id <> ?", user.ID, channel.ID).
			Order("created_at ASC, id ASC").First(&replacement)
		if replacementResult.Error != nil && !errors.Is(replacementResult.Error, gorm.ErrRecordNotFound) {
			return replacementResult.Error
		}
		var replacementID *uuid.UUID
		if replacementResult.Error == nil {
			replacementID = &replacement.ID
		}
		if err := tx.Model(&model.UserStudioState{}).Where("user_id = ? AND channel_id = ?", user.ID, channel.ID).
			Update("channel_id", replacementID).Error; err != nil {
			return err
		}
		return tx.Delete(&channel).Error
	})
}

func (s *Service) ListCollections(user authctx.CurrentUser, channelID uuid.UUID, module Module) ([]model.Collection, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}
	if _, err := s.ownedChannel(user.ID, channelID); err != nil {
		return nil, err
	}
	return s.repo.ListCollections(channelID, module)
}

func (s *Service) CreateCollection(user authctx.CurrentUser, module Module, input CreateCollectionInput) (model.Collection, error) {
	if err := requireUser(user); err != nil {
		return model.Collection{}, err
	}
	if _, err := s.ownedChannel(user.ID, input.ChannelID); err != nil {
		return model.Collection{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return model.Collection{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}
	collection := model.Collection{
		ChannelID:   input.ChannelID,
		ContentType: string(module),
		CreatedBy:   &user.ID,
		Name:        name,
		Description: strings.TrimSpace(input.Description),
		CoverURL:    strings.TrimSpace(input.CoverURL),
	}
	if err := s.db.Create(&collection).Error; err != nil {
		return model.Collection{}, err
	}
	return collection, nil
}

func (s *Service) UpdateCollection(user authctx.CurrentUser, module Module, collectionID uuid.UUID, input UpdateCollectionInput) (model.Collection, error) {
	if err := requireUser(user); err != nil {
		return model.Collection{}, err
	}
	collection, err := s.collectionInModule(collectionID, module)
	if err != nil {
		return model.Collection{}, err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return model.Collection{}, err
	}
	if input.Name != nil {
		collection.Name = strings.TrimSpace(*input.Name)
		if collection.Name == "" {
			return model.Collection{}, apperr.BadRequest("validation.invalid_request", "name is required")
		}
	}
	if input.Description != nil {
		collection.Description = strings.TrimSpace(*input.Description)
	}
	if input.CoverURL != nil {
		collection.CoverURL = strings.TrimSpace(*input.CoverURL)
	}
	if err := s.db.Save(&collection).Error; err != nil {
		return model.Collection{}, err
	}
	return collection, nil
}

func (s *Service) DeleteCollection(user authctx.CurrentUser, module Module, collectionID uuid.UUID) error {
	if err := requireUser(user); err != nil {
		return err
	}
	collection, err := s.collectionInModule(collectionID, module)
	if err != nil {
		return err
	}
	if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.Post{}).Where("collection_id = ?", collection.ID).Update("collection_id", nil).Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable(&model.BlogDraft{}) {
			if err := tx.Model(&model.BlogDraft{}).Where("collection_id = ?", collection.ID).Update("collection_id", nil).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable(&model.PostCollection{}) {
			if err := tx.Where("collection_id = ?", collection.ID).Delete(&model.PostCollection{}).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable(&model.VideoCollection{}) {
			if err := tx.Where("collection_id = ?", collection.ID).Delete(&model.VideoCollection{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.StudioModuleSettings{}).Where("default_collection_id = ?", collection.ID).
			Update("default_collection_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&collection).Error
	})
}

func (s *Service) ValidateContentScope(userID, channelID uuid.UUID, module Module, collectionIDs []uuid.UUID, publishing bool) error {
	if userID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if _, err := s.ownedChannel(userID, channelID); err != nil {
		return err
	}
	collectionIDs = uniqueUUIDs(collectionIDs)
	if len(collectionIDs) == 0 {
		if publishing {
			return apperr.BadRequest("studio.collection_required", "At least one collection is required before publishing")
		}
		return nil
	}
	var count int64
	if err := s.db.Model(&model.Collection{}).
		Where("id IN ? AND channel_id = ? AND content_type = ?", collectionIDs, channelID, module).
		Count(&count).Error; err != nil {
		return err
	}
	if count != int64(len(collectionIDs)) {
		return apperr.BadRequest("studio.invalid_collection_scope", "Collections must belong to the selected channel and module")
	}
	return nil
}

func (s *Service) ownedChannel(userID, channelID uuid.UUID) (model.Channel, error) {
	if channelID == uuid.Nil {
		return model.Channel{}, apperr.BadRequest("validation.invalid_request", "channel_id is required")
	}
	channel, err := s.repo.GetChannel(channelID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Channel{}, apperr.NotFound("studio.channel_not_found", "Channel not found")
	}
	if err != nil {
		return model.Channel{}, err
	}
	if channel.UserID == nil || *channel.UserID != userID {
		return model.Channel{}, apperr.Forbidden("studio.channel_forbidden", "You do not have permission to manage this channel")
	}
	return channel, nil
}

func (s *Service) collectionInModule(collectionID uuid.UUID, module Module) (model.Collection, error) {
	if collectionID == uuid.Nil {
		return model.Collection{}, apperr.BadRequest("validation.invalid_request", "collection_id is required")
	}
	collection, err := s.repo.GetCollection(collectionID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Collection{}, apperr.NotFound("studio.collection_not_found", "Collection not found")
	}
	if err != nil {
		return model.Collection{}, err
	}
	if collection.ContentType != string(module) {
		return model.Collection{}, apperr.BadRequest("studio.collection_module_mismatch", "Collection does not belong to this module")
	}
	return collection, nil
}

func (s *Service) availableChannelSlug(requested, name string, excludeID *uuid.UUID) (string, error) {
	namespace := sitehandle.NewService(s.db)
	requested = strings.TrimSpace(requested)
	if requested != "" {
		if err := namespace.ValidateChannelSlugAvailable(context.Background(), requested, excludeID); err != nil {
			return "", apperr.BadRequest("studio.slug_unavailable", "Channel slug is invalid or unavailable")
		}
		return requested, nil
	}
	base := slugifyChannel(name)
	for counter := 1; ; counter++ {
		candidate := base
		if counter > 1 {
			candidate = fmt.Sprintf("%s-%d", base, counter)
		}
		err := namespace.ValidateChannelSlugAvailable(context.Background(), candidate, excludeID)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, sitehandle.ErrReserved) && !errors.Is(err, sitehandle.ErrTaken) {
			return "", err
		}
	}
}

func slugifyChannel(value string) string {
	slug := strings.ToLower(strings.TrimSpace(value))
	slug = channelSlugInvalidChars.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "channel"
	}
	return slug
}

func requireUser(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return nil
}

func summarizeChannels(channels []model.Channel) []ChannelSummary {
	summaries := make([]ChannelSummary, 0, len(channels))
	for _, channel := range channels {
		summaries = append(summaries, summarizeChannel(channel))
	}
	return summaries
}

func summarizeChannel(channel model.Channel) ChannelSummary {
	return ChannelSummary{
		ID:          channel.ID,
		Name:        channel.Name,
		Slug:        channel.Slug,
		Description: channel.Description,
		CoverURL:    channel.CoverURL,
	}
}

func uniqueUUIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

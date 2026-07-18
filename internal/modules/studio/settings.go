package studio

import (
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) GetSettings(user authctx.CurrentUser, module Module, channelID uuid.UUID) (SettingsResponse, error) {
	if err := requireUser(user); err != nil {
		return SettingsResponse{}, err
	}
	if _, err := ParseModule(string(module)); err != nil {
		return SettingsResponse{}, err
	}
	channel, err := s.resolveContentChannel(user.ID, channelID)
	if err != nil {
		return SettingsResponse{}, err
	}
	settings, err := s.settingsRecord(user.ID, channel.ID, module)
	if err != nil {
		return SettingsResponse{}, err
	}
	return settingsResponse(settings, module), nil
}

func (s *Service) SaveSettings(user authctx.CurrentUser, module Module, input SettingsInput) (SettingsResponse, error) {
	if err := requireUser(user); err != nil {
		return SettingsResponse{}, err
	}
	if _, err := ParseModule(string(module)); err != nil {
		return SettingsResponse{}, err
	}
	channel, err := s.resolveContentChannel(user.ID, input.ChannelID)
	if err != nil {
		return SettingsResponse{}, err
	}
	if input.DefaultCollectionID != nil {
		collection, err := s.repo.GetCollection(*input.DefaultCollectionID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return SettingsResponse{}, apperr.NotFound("studio.collection_not_found", "Collection not found")
		}
		if err != nil {
			return SettingsResponse{}, err
		}
		if _, err := s.ownedChannel(user.ID, collection.ChannelID); err != nil {
			return SettingsResponse{}, err
		}
		if collection.ChannelID != channel.ID || collection.ContentType != string(module) {
			return SettingsResponse{}, apperr.BadRequest("studio.invalid_collection_scope", "Default collection must belong to the selected channel and module")
		}
	}
	settings, err := s.settingsRecord(user.ID, channel.ID, module)
	if err != nil {
		return SettingsResponse{}, err
	}
	settings.DefaultCollectionID = input.DefaultCollectionID
	if input.DefaultVisibility != nil {
		visibility, err := studioVisibilityToDB(*input.DefaultVisibility)
		if err != nil || visibility == "" {
			if err != nil {
				return SettingsResponse{}, err
			}
			return SettingsResponse{}, apperr.BadRequest("studio.invalid_visibility", "default_visibility is required")
		}
		settings.DefaultVisibility = visibility
	}
	if input.DefaultPublishStatus != nil {
		status := strings.TrimSpace(*input.DefaultPublishStatus)
		if status != "draft" && status != "published" {
			return SettingsResponse{}, apperr.BadRequest("studio.invalid_publish_status", "default_publish_status must be draft or published")
		}
		settings.DefaultPublishStatus = status
	}
	if module == ModuleBlog {
		settings.AutoplayEnabled = false
	} else if input.AutoplayEnabled != nil {
		settings.AutoplayEnabled = *input.AutoplayEnabled
	}
	if err := s.db.Save(&settings).Error; err != nil {
		return SettingsResponse{}, err
	}
	return settingsResponse(settings, module), nil
}

func (s *Service) settingsRecord(userID, channelID uuid.UUID, module Module) (model.StudioModuleSettings, error) {
	var settings model.StudioModuleSettings
	err := s.db.Where("user_id = ? AND channel_id = ? AND content_type = ?", userID, channelID, module).First(&settings).Error
	if err == nil {
		return settings, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.StudioModuleSettings{}, err
	}
	settings = model.StudioModuleSettings{
		UserID: userID, ChannelID: channelID, ContentType: string(module),
		DefaultVisibility: "public", DefaultPublishStatus: "published",
	}
	if err := s.db.Create(&settings).Error; err != nil {
		return model.StudioModuleSettings{}, err
	}
	return settings, nil
}

func settingsResponse(settings model.StudioModuleSettings, module Module) SettingsResponse {
	autoplay := settings.AutoplayEnabled
	if module == ModuleBlog {
		autoplay = false
	}
	return SettingsResponse{
		ChannelID: settings.ChannelID, Module: module, DefaultCollectionID: settings.DefaultCollectionID,
		DefaultVisibility:    studioVisibilityFromDB(settings.DefaultVisibility),
		DefaultPublishStatus: settings.DefaultPublishStatus, AutoplayEnabled: autoplay,
	}
}

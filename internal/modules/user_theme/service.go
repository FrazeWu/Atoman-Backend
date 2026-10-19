package user_theme

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Service provides business logic for user theme configuration and styling.
type Service struct {
	db *gorm.DB
}

// NewService creates a new user theme service instance.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// DefaultThemeDTO returns the default uncustomized theme response.
func DefaultThemeDTO() UserThemeDTO {
	return UserThemeDTO{
		IsEnabled:    false,
		Mode:         "preset",
		PresetConfig: json.RawMessage("{}"),
		CustomCSS:    "",
		SanitizedCSS: "",
	}
}

func toDTO(theme model.UserTheme) UserThemeDTO {
	cfg := theme.PresetConfig
	if len(cfg) == 0 || string(cfg) == "null" {
		cfg = json.RawMessage("{}")
	}
	return UserThemeDTO{
		IsEnabled:    theme.IsEnabled,
		Mode:         theme.Mode,
		PresetConfig: cfg,
		CustomCSS:    theme.CustomCSS,
		SanitizedCSS: theme.SanitizedCSS,
	}
}

// GetByHandle retrieves the theme for a user by handle (username or UUID).
// If the user does not exist, it returns 404 (user.not_found).
// If the user exists but hasn't customized their theme (or deleted it),
// it returns the default theme (is_enabled: false, mode: "preset", preset_config: {}).
func (s *Service) GetByHandle(ctx context.Context, handle string) (*UserThemeDTO, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return nil, apperr.NotFound("user.not_found", "User not found")
	}

	var user model.User
	query := s.db.WithContext(ctx)
	if parsedUUID, err := uuid.Parse(handle); err == nil {
		query = query.Where("uuid = ? OR LOWER(username) = LOWER(?)", parsedUUID, handle)
	} else {
		query = query.Where("LOWER(username) = LOWER(?)", handle)
	}

	if err := query.First(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperr.NotFound("user.not_found", "User not found")
		}
		return nil, apperr.Internal(err)
	}

	var theme model.UserTheme
	if err := s.db.WithContext(ctx).Where("user_id = ?", user.UUID).First(&theme).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			dto := DefaultThemeDTO()
			return &dto, nil
		}
		return nil, apperr.Internal(err)
	}

	dto := toDTO(theme)
	return &dto, nil
}

// UpdateTheme updates or creates (upserts) the theme configuration for the specified user.
// It validates mode and JSON configuration, runs CSS sanitization and scoping,
// and returns the updated theme.
func (s *Service) UpdateTheme(ctx context.Context, userID uuid.UUID, input UpdateThemeInput) (*UserThemeDTO, error) {
	if userID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}

	mode := strings.TrimSpace(input.Mode)
	if mode == "" {
		mode = "preset"
	} else if mode != "preset" && mode != "custom_css" {
		return nil, apperr.BadRequest("theme.invalid_mode", "mode must be 'preset' or 'custom_css'")
	}

	presetConfig := input.PresetConfig
	if len(presetConfig) == 0 || string(presetConfig) == "null" {
		presetConfig = json.RawMessage("{}")
	} else if !json.Valid(presetConfig) {
		return nil, apperr.BadRequest("theme.invalid_preset_config", "preset_config must be valid JSON")
	}

	sanitizedCSS, err := SanitizeAndScopeCSS(input.CustomCSS, userID)
	if err != nil {
		return nil, apperr.BadRequest("theme.invalid_css", err.Error())
	}

	isEnabled := true
	if input.IsEnabled != nil {
		isEnabled = *input.IsEnabled
	}

	var theme model.UserTheme
	err = s.db.WithContext(ctx).Where("user_id = ?", userID).First(&theme).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Check if a soft-deleted record exists to revive
		var softDeleted model.UserTheme
		if errSoft := s.db.WithContext(ctx).Unscoped().Where("user_id = ?", userID).First(&softDeleted).Error; errSoft == nil {
			softDeleted.DeletedAt = gorm.DeletedAt{}
			softDeleted.IsEnabled = isEnabled
			softDeleted.Mode = mode
			softDeleted.PresetConfig = presetConfig
			softDeleted.CustomCSS = input.CustomCSS
			softDeleted.SanitizedCSS = sanitizedCSS
			if err := s.db.WithContext(ctx).Unscoped().Model(&softDeleted).Where("id = ?", softDeleted.ID).Updates(map[string]interface{}{
				"deleted_at":    nil,
				"is_enabled":    isEnabled,
				"mode":          mode,
				"preset_config": presetConfig,
				"custom_css":    input.CustomCSS,
				"sanitized_css": sanitizedCSS,
			}).Error; err != nil {
				return nil, apperr.Internal(err)
			}
			theme = softDeleted
		} else {
			theme = model.UserTheme{
				UserID:       userID,
				IsEnabled:    isEnabled,
				Mode:         mode,
				PresetConfig: presetConfig,
				CustomCSS:    input.CustomCSS,
				SanitizedCSS: sanitizedCSS,
			}
			if err := s.db.WithContext(ctx).Create(&theme).Error; err != nil {
				return nil, apperr.Internal(err)
			}
		}
	} else if err != nil {
		return nil, apperr.Internal(err)
	} else {
		theme.IsEnabled = isEnabled
		theme.Mode = mode
		theme.PresetConfig = presetConfig
		theme.CustomCSS = input.CustomCSS
		theme.SanitizedCSS = sanitizedCSS
		if err := s.db.WithContext(ctx).Save(&theme).Error; err != nil {
			return nil, apperr.Internal(err)
		}
	}

	dto := toDTO(theme)
	return &dto, nil
}

// ResetTheme resets (soft-deletes) the theme configuration for the specified user.
func (s *Service) ResetTheme(ctx context.Context, userID uuid.UUID) error {
	if userID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}

	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&model.UserTheme{}).Error; err != nil {
		return apperr.Internal(err)
	}
	return nil
}

package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"atoman/internal/model"

	"gorm.io/gorm"
)

const SiteAccessSettingKey = "site.module_access"

const SiteAccessPayloadVersion = 1

const (
	SiteAccessFeedFullTextModeDisabled  = "disabled"
	SiteAccessFeedFullTextModePerSource = "per_source"
	SiteAccessBlogCommentModeAll        = "all"
	SiteAccessBlogCommentModeAuth       = "authenticated"
	SiteAccessBlogCommentModeDisabled   = "disabled"
)

var ErrInvalidSiteAccessPayload = errors.New("invalid_site_access_payload")

type SiteAccessMatrix struct {
	Version  int                 `json:"version"`
	Modules  map[string]SiteAccessModule `json:"modules"`
	Settings SiteAccessSettings  `json:"settings"`
}

type SiteAccessModule struct {
	Enabled  *bool           `json:"enabled"`
	Features map[string]bool `json:"features"`
}

type SiteAccessSettings struct {
	Feed  SiteAccessFeedSettings  `json:"feed"`
	Blog  SiteAccessBlogSettings  `json:"blog"`
	Forum SiteAccessForumSettings `json:"forum"`
}

type SiteAccessFeedSettings struct {
	AllowManageSources bool   `json:"allow_manage_sources"`
	AllowAddSource     bool   `json:"allow_add_source"`
	FullTextMode       string `json:"full_text_mode"`
}

type SiteAccessBlogSettings struct {
	CommentMode string `json:"comment_mode"`
}

type SiteAccessForumSettings struct {
	AllowCategoryRequest bool                                `json:"allow_category_request"`
	ModeratorPermissions SiteAccessForumModeratorPermissions `json:"moderator_permissions"`
}

type SiteAccessForumModeratorPermissions struct {
	ReviewCategoryRequest bool `json:"review_category_request"`
	PinTopic              bool `json:"pin_topic"`
	LockTopic             bool `json:"lock_topic"`
}

type SiteAccessMatrixInput struct {
	Version  int                            `json:"version"`
	Modules  map[string]SiteAccessModuleInput `json:"modules"`
	Settings *SiteAccessSettingsInput       `json:"settings"`
}

type SiteAccessModuleInput struct {
	Enabled  *bool           `json:"enabled"`
	Visible  *bool           `json:"visible"`
	Features map[string]bool `json:"features"`
}

type SiteAccessSettingsInput struct {
	Feed  *SiteAccessFeedSettingsInput  `json:"feed"`
	Blog  *SiteAccessBlogSettingsInput  `json:"blog"`
	Forum *SiteAccessForumSettingsInput `json:"forum"`
}

type SiteAccessFeedSettingsInput struct {
	AllowManageSources *bool   `json:"allow_manage_sources"`
	AllowAddSource     *bool   `json:"allow_add_source"`
	FullTextMode       *string `json:"full_text_mode"`
}

type SiteAccessBlogSettingsInput struct {
	CommentMode *string `json:"comment_mode"`
}

type SiteAccessForumSettingsInput struct {
	AllowCategoryRequest *bool                                     `json:"allow_category_request"`
	ModeratorPermissions *SiteAccessForumModeratorPermissionsInput `json:"moderator_permissions"`
}

type SiteAccessForumModeratorPermissionsInput struct {
	ReviewCategoryRequest *bool `json:"review_category_request"`
	PinTopic              *bool `json:"pin_topic"`
	LockTopic             *bool `json:"lock_topic"`
}

type legacySiteAccessMatrix struct {
	Version int                               `json:"version"`
	Modules map[string]legacySiteAccessModule `json:"modules"`
}

type legacySiteAccessModule struct {
	Enabled  *bool           `json:"enabled"`
	Visible  *bool           `json:"visible"`
	Features map[string]bool `json:"features"`
}

type SiteAccessService struct {
	db *gorm.DB
}

func NewSiteAccessService(db *gorm.DB) *SiteAccessService {
	return &SiteAccessService{db: db}
}

func DefaultSiteAccessMatrix() SiteAccessMatrix {
	matrix := SiteAccessMatrix{
		Version: SiteAccessPayloadVersion,
		Modules: map[string]SiteAccessModule{
			"feed":     defaultAccessModule("subscription.manage"),
			"media":    defaultAccessModule(),
			"music":    defaultAccessModule("music.submit", "music.review"),
			"blog":     defaultAccessModule("post.create", "channel.manage"),
			"forum":    defaultAccessModule("topic.create", "category.request"),
			"debate":   defaultAccessModule("debate.create", "argument.create"),
			"timeline": defaultAccessModule("timeline.edit"),
			"podcast":  defaultAccessModule("podcast.publish"),
			"video":    defaultAccessModule("video.publish"),
		},
		Settings: SiteAccessSettings{
			Feed: SiteAccessFeedSettings{
				AllowManageSources: true,
				AllowAddSource:     true,
				FullTextMode:       SiteAccessFeedFullTextModePerSource,
			},
			Blog: SiteAccessBlogSettings{
				CommentMode: SiteAccessBlogCommentModeAuth,
			},
			Forum: SiteAccessForumSettings{
				AllowCategoryRequest: true,
				ModeratorPermissions: SiteAccessForumModeratorPermissions{
					ReviewCategoryRequest: true,
					PinTopic:              true,
					LockTopic:             true,
				},
			},
		},
	}
	return synchronizeForumCategoryRequest(matrix)
}

func defaultAccessModule(features ...string) SiteAccessModule {
	featureMap := make(map[string]bool, len(features))
	for _, feature := range features {
		featureMap[feature] = true
	}
	enabled := true
	return SiteAccessModule{Enabled: &enabled, Features: featureMap}
}

func (s *SiteAccessService) Load() (SiteAccessMatrix, error) {
	matrix := DefaultSiteAccessMatrix()

	var setting model.SiteSetting
	if err := s.db.First(&setting, "key = ?", SiteAccessSettingKey).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return matrix, nil
		}
		return matrix, err
	}

	var stored SiteAccessMatrixInput
	if err := json.Unmarshal([]byte(setting.Value), &stored); err == nil {
		loaded, err := mergeStoredSiteAccessInput(matrix, stored)
		if err != nil {
			return matrix, err
		}
		return loaded, nil
	}

	var legacy legacySiteAccessMatrix
	if err := json.Unmarshal([]byte(setting.Value), &legacy); err != nil {
		return matrix, err
	}
	return mergeStoredLegacySiteAccess(matrix, legacy), nil
}

func (s *SiteAccessService) Save(matrix SiteAccessMatrix) error {
	input := matrix.ToInput()
	return s.SaveInput(input)
}

func (s *SiteAccessService) SaveInput(input SiteAccessMatrixInput) error {
	if err := validateSiteAccessInput(input); err != nil {
		return err
	}

	base, err := s.Load()
	if err != nil {
		return err
	}

	merged, err := mergeInputSiteAccess(base, input)
	if err != nil {
		return err
	}
	if err := validateSiteAccess(merged); err != nil {
		return err
	}

	value, err := json.Marshal(merged)
	if err != nil {
		return err
	}

	setting := model.SiteSetting{
		Key:         SiteAccessSettingKey,
		Value:       string(value),
		Description: "模块可见性与功能开放配置",
	}
	return s.db.Save(&setting).Error
}

func (s *SiteAccessService) SaveLegacyPayload(value []byte) error {
	var stored legacySiteAccessMatrix
	if err := json.Unmarshal(value, &stored); err != nil {
		return fmt.Errorf("%w: invalid json", ErrInvalidSiteAccessPayload)
	}
	if stored.Version != 0 && stored.Version != SiteAccessPayloadVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidSiteAccessPayload, stored.Version)
	}

	base, err := s.Load()
	if err != nil {
		return err
	}

	matrix, err := mergeLegacyInputSiteAccess(base, stored)
	if err != nil {
		return err
	}
	if err := validateSiteAccess(matrix); err != nil {
		return err
	}

	payload, err := json.Marshal(matrix)
	if err != nil {
		return err
	}

	setting := model.SiteSetting{
		Key:         SiteAccessSettingKey,
		Value:       string(payload),
		Description: "模块可见性与功能开放配置",
	}
	return s.db.Save(&setting).Error
}

func (s *SiteAccessService) PublicMatrix() (SiteAccessMatrix, error) {
	return s.Load()
}

func (s *SiteAccessService) IsModuleEnabled(module string) (bool, error) {
	matrix, err := s.Load()
	if err != nil {
		return false, err
	}
	entry, ok := matrix.Modules[module]
	if !ok {
		return false, fmt.Errorf("unknown module %q", module)
	}
	return entry.Enabled != nil && *entry.Enabled, nil
}

func (s *SiteAccessService) IsFeatureEnabled(module string, feature string) (bool, error) {
	matrix, err := s.Load()
	if err != nil {
		return false, err
	}
	entry, ok := matrix.Modules[module]
	if !ok {
		return false, fmt.Errorf("unknown module %q", module)
	}
	if entry.Enabled == nil || !*entry.Enabled {
		return false, nil
	}
	value, ok := entry.Features[feature]
	if !ok {
		return false, fmt.Errorf("unknown feature %q", feature)
	}
	return value, nil
}

func (m SiteAccessMatrix) ToInput() SiteAccessMatrixInput {
	modules := make(map[string]SiteAccessModuleInput, len(m.Modules))
	for key, module := range m.Modules {
		features := cloneFeatureMap(module.Features)
		modules[key] = SiteAccessModuleInput{
			Enabled:  copyBoolPtr(module.Enabled),
			Features: features,
		}
	}

	fullTextMode := m.Settings.Feed.FullTextMode
	commentMode := m.Settings.Blog.CommentMode
	allowManageSources := m.Settings.Feed.AllowManageSources
	allowAddSource := m.Settings.Feed.AllowAddSource
	allowCategoryRequest := m.Settings.Forum.AllowCategoryRequest
	reviewCategoryRequest := m.Settings.Forum.ModeratorPermissions.ReviewCategoryRequest
	pinTopic := m.Settings.Forum.ModeratorPermissions.PinTopic
	lockTopic := m.Settings.Forum.ModeratorPermissions.LockTopic

	return SiteAccessMatrixInput{
		Version: m.Version,
		Modules: modules,
		Settings: &SiteAccessSettingsInput{
			Feed: &SiteAccessFeedSettingsInput{
				AllowManageSources: &allowManageSources,
				AllowAddSource:     &allowAddSource,
				FullTextMode:       &fullTextMode,
			},
			Blog: &SiteAccessBlogSettingsInput{
				CommentMode: &commentMode,
			},
			Forum: &SiteAccessForumSettingsInput{
				AllowCategoryRequest: &allowCategoryRequest,
				ModeratorPermissions: &SiteAccessForumModeratorPermissionsInput{
					ReviewCategoryRequest: &reviewCategoryRequest,
					PinTopic:              &pinTopic,
					LockTopic:             &lockTopic,
				},
			},
		},
	}
}

func mergeStoredLegacySiteAccess(defaults SiteAccessMatrix, stored legacySiteAccessMatrix) SiteAccessMatrix {
	defaults.Version = SiteAccessPayloadVersion
	if stored.Modules == nil {
		return synchronizeForumCategoryRequest(defaults)
	}

	for module, storedModule := range stored.Modules {
		current, ok := defaults.Modules[module]
		if !ok {
			continue
		}
		if storedModule.Enabled != nil {
			current.Enabled = boolPtr(*storedModule.Enabled)
		} else if storedModule.Visible != nil {
			current.Enabled = boolPtr(*storedModule.Visible)
		}
		if current.Features == nil {
			current.Features = map[string]bool{}
		}
		for feature, enabled := range storedModule.Features {
			if _, ok := current.Features[feature]; !ok {
				continue
			}
			current.Features[feature] = enabled
		}
		defaults.Modules[module] = current
	}

	return synchronizeForumCategoryRequest(defaults)
}

func mergeStoredSiteAccessInput(defaults SiteAccessMatrix, stored SiteAccessMatrixInput) (SiteAccessMatrix, error) {
	defaults.Version = SiteAccessPayloadVersion

	for module, storedModule := range stored.Modules {
		current, ok := defaults.Modules[module]
		if !ok {
			continue
		}
		if storedModule.Enabled != nil {
			current.Enabled = boolPtr(*storedModule.Enabled)
		} else if storedModule.Visible != nil {
			current.Enabled = boolPtr(*storedModule.Visible)
		}
		if current.Features == nil {
			current.Features = map[string]bool{}
		}
		for feature, enabled := range storedModule.Features {
			if _, ok := current.Features[feature]; !ok {
				continue
			}
			current.Features[feature] = enabled
		}
		defaults.Modules[module] = current
	}

	if stored.Settings != nil {
		if err := applySettingsInput(&defaults, stored.Settings); err != nil {
			return defaults, err
		}
	}
	if stored.Settings != nil && stored.Settings.Forum != nil && stored.Settings.Forum.AllowCategoryRequest != nil {
		forum := defaults.Modules["forum"]
		if forum.Features == nil {
			forum.Features = map[string]bool{}
		}
		forum.Features["category.request"] = *stored.Settings.Forum.AllowCategoryRequest
		defaults.Modules["forum"] = forum
	}

	return synchronizeForumCategoryRequest(defaults), nil
}

func mergeInputSiteAccess(base SiteAccessMatrix, incoming SiteAccessMatrixInput) (SiteAccessMatrix, error) {
	base.Version = SiteAccessPayloadVersion
	if incoming.Version != 0 && incoming.Version != SiteAccessPayloadVersion {
		return base, fmt.Errorf("%w: unsupported version %d", ErrInvalidSiteAccessPayload, incoming.Version)
	}

	defaults := DefaultSiteAccessMatrix()
	for module, incomingModule := range incoming.Modules {
		defaultEntry, ok := defaults.Modules[module]
		if !ok {
			return base, fmt.Errorf("%w: unknown module %q", ErrInvalidSiteAccessPayload, module)
		}
		current := base.Modules[module]
		if incomingModule.Enabled != nil {
			current.Enabled = boolPtr(*incomingModule.Enabled)
		} else if incomingModule.Visible != nil {
			current.Enabled = boolPtr(*incomingModule.Visible)
		}
		if current.Features == nil {
			current.Features = map[string]bool{}
		}
		for feature, enabled := range incomingModule.Features {
			if _, ok := defaultEntry.Features[feature]; !ok {
				return base, fmt.Errorf("%w: unknown feature %q", ErrInvalidSiteAccessPayload, feature)
			}
			current.Features[feature] = enabled
		}
		base.Modules[module] = current
	}

	if incoming.Settings != nil {
		if err := applySettingsInput(&base, incoming.Settings); err != nil {
			return base, err
		}
	}
	if incoming.Settings != nil && incoming.Settings.Forum != nil && incoming.Settings.Forum.AllowCategoryRequest != nil {
		forum := base.Modules["forum"]
		if forum.Features == nil {
			forum.Features = map[string]bool{}
		}
		forum.Features["category.request"] = *incoming.Settings.Forum.AllowCategoryRequest
		base.Modules["forum"] = forum
	}

	return synchronizeForumCategoryRequest(base), nil
}

func mergeLegacyInputSiteAccess(base SiteAccessMatrix, incoming legacySiteAccessMatrix) (SiteAccessMatrix, error) {
	base.Version = SiteAccessPayloadVersion
	if incoming.Modules == nil {
		return base, nil
	}

	defaults := DefaultSiteAccessMatrix()
	for module, incomingModule := range incoming.Modules {
		defaultEntry, ok := defaults.Modules[module]
		if !ok {
			return base, fmt.Errorf("%w: unknown module %q", ErrInvalidSiteAccessPayload, module)
		}
		current := base.Modules[module]
		if incomingModule.Enabled != nil {
			current.Enabled = boolPtr(*incomingModule.Enabled)
		} else if incomingModule.Visible != nil {
			current.Enabled = boolPtr(*incomingModule.Visible)
		}
		if current.Features == nil {
			current.Features = map[string]bool{}
		}
		for feature, enabled := range incomingModule.Features {
			if _, ok := defaultEntry.Features[feature]; !ok {
				return base, fmt.Errorf("%w: unknown feature %q", ErrInvalidSiteAccessPayload, feature)
			}
			current.Features[feature] = enabled
		}
		base.Modules[module] = current
	}

	return synchronizeForumCategoryRequest(base), nil
}

func applySettingsInput(base *SiteAccessMatrix, input *SiteAccessSettingsInput) error {
	if input == nil {
		return nil
	}

	if input.Feed != nil {
		if input.Feed.AllowManageSources != nil {
			base.Settings.Feed.AllowManageSources = *input.Feed.AllowManageSources
		}
		if input.Feed.AllowAddSource != nil {
			base.Settings.Feed.AllowAddSource = *input.Feed.AllowAddSource
		}
		if input.Feed.FullTextMode != nil {
			base.Settings.Feed.FullTextMode = *input.Feed.FullTextMode
		}
	}

	if input.Blog != nil && input.Blog.CommentMode != nil {
		base.Settings.Blog.CommentMode = *input.Blog.CommentMode
	}

	if input.Forum != nil {
		if input.Forum.AllowCategoryRequest != nil {
			base.Settings.Forum.AllowCategoryRequest = *input.Forum.AllowCategoryRequest
		}
		if input.Forum.ModeratorPermissions != nil {
			if input.Forum.ModeratorPermissions.ReviewCategoryRequest != nil {
				base.Settings.Forum.ModeratorPermissions.ReviewCategoryRequest = *input.Forum.ModeratorPermissions.ReviewCategoryRequest
			}
			if input.Forum.ModeratorPermissions.PinTopic != nil {
				base.Settings.Forum.ModeratorPermissions.PinTopic = *input.Forum.ModeratorPermissions.PinTopic
			}
			if input.Forum.ModeratorPermissions.LockTopic != nil {
				base.Settings.Forum.ModeratorPermissions.LockTopic = *input.Forum.ModeratorPermissions.LockTopic
			}
		}
	}

	return nil
}

func synchronizeForumCategoryRequest(matrix SiteAccessMatrix) SiteAccessMatrix {
	forum, ok := matrix.Modules["forum"]
	if !ok {
		return matrix
	}
	if forum.Features == nil {
		forum.Features = map[string]bool{}
	}

	if value, ok := forum.Features["category.request"]; ok {
		matrix.Settings.Forum.AllowCategoryRequest = value
	} else {
		forum.Features["category.request"] = matrix.Settings.Forum.AllowCategoryRequest
	}
	forum.Features["category.request"] = matrix.Settings.Forum.AllowCategoryRequest
	matrix.Modules["forum"] = forum
	return matrix
}

func cloneFeatureMap(input map[string]bool) map[string]bool {
	if input == nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func copyBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func boolPtr(value bool) *bool {
	return &value
}

func validateSiteAccessInput(input SiteAccessMatrixInput) error {
	if input.Version != 0 && input.Version != SiteAccessPayloadVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidSiteAccessPayload, input.Version)
	}

	defaults := DefaultSiteAccessMatrix()
	for module, entry := range input.Modules {
		defaultEntry, ok := defaults.Modules[module]
		if !ok {
			return fmt.Errorf("%w: unknown module %q", ErrInvalidSiteAccessPayload, module)
		}
		for feature := range entry.Features {
			if _, ok := defaultEntry.Features[feature]; !ok {
				return fmt.Errorf("%w: unknown feature %q", ErrInvalidSiteAccessPayload, feature)
			}
		}
	}
	return validateSettingsInput(input.Settings)
}

func validateSettingsInput(input *SiteAccessSettingsInput) error {
	if input == nil {
		return nil
	}
	if input.Feed != nil && input.Feed.FullTextMode != nil {
		switch *input.Feed.FullTextMode {
		case SiteAccessFeedFullTextModeDisabled, SiteAccessFeedFullTextModePerSource:
		default:
			return fmt.Errorf("%w: invalid feed full_text_mode %q", ErrInvalidSiteAccessPayload, *input.Feed.FullTextMode)
		}
	}
	if input.Blog != nil && input.Blog.CommentMode != nil {
		switch *input.Blog.CommentMode {
		case SiteAccessBlogCommentModeAll, SiteAccessBlogCommentModeAuth, SiteAccessBlogCommentModeDisabled:
		default:
			return fmt.Errorf("%w: invalid blog comment_mode %q", ErrInvalidSiteAccessPayload, *input.Blog.CommentMode)
		}
	}
	return nil
}

func validateSiteAccess(matrix SiteAccessMatrix) error {
	if matrix.Version != SiteAccessPayloadVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidSiteAccessPayload, matrix.Version)
	}

	defaults := DefaultSiteAccessMatrix()
	for module, entry := range matrix.Modules {
		defaultEntry, ok := defaults.Modules[module]
		if !ok {
			return fmt.Errorf("%w: unknown module %q", ErrInvalidSiteAccessPayload, module)
		}
		for feature := range entry.Features {
			if _, ok := defaultEntry.Features[feature]; !ok {
				return fmt.Errorf("%w: unknown feature %q", ErrInvalidSiteAccessPayload, feature)
			}
		}
	}

	if matrix.Settings.Feed.FullTextMode != SiteAccessFeedFullTextModeDisabled &&
		matrix.Settings.Feed.FullTextMode != SiteAccessFeedFullTextModePerSource {
		return fmt.Errorf("%w: invalid feed full_text_mode %q", ErrInvalidSiteAccessPayload, matrix.Settings.Feed.FullTextMode)
	}

	switch matrix.Settings.Blog.CommentMode {
	case SiteAccessBlogCommentModeAll, SiteAccessBlogCommentModeAuth, SiteAccessBlogCommentModeDisabled:
	default:
		return fmt.Errorf("%w: invalid blog comment_mode %q", ErrInvalidSiteAccessPayload, matrix.Settings.Blog.CommentMode)
	}

	return nil
}

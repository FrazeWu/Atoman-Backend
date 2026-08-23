package studio

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
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

func recordStudioAudit(tx *gorm.DB, userID uuid.UUID, action, entityType string, entityID uuid.UUID, metadata map[string]any) error {
	return audit.Record(tx, audit.Entry{ActorID: &userID, Action: action, EntityType: entityType, EntityID: &entityID, Metadata: metadata})
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
	if err := s.ensureChannelNameAvailable(name, nil); err != nil {
		return ChannelSummary{}, err
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
		if err := createSystemDefaultCollections(tx, channel.ID, user.ID); err != nil {
			return err
		}
		if err := recordStudioAudit(tx, user.ID, "studio.channel_created", "channel", channel.ID, map[string]any{"slug": channel.Slug}); err != nil {
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
		if err := s.ensureChannelNameAvailable(channel.Name, &channel.ID); err != nil {
			return ChannelSummary{}, err
		}
	}
	if input.Slug != nil {
		requestedSlug := strings.TrimSpace(*input.Slug)
		if requestedSlug != channel.Slug {
			locked, err := s.channelSlugLocked(channel.ID)
			if err != nil {
				return ChannelSummary{}, err
			}
			if locked {
				return ChannelSummary{}, apperr.Conflict("studio.channel_slug_locked", "Channel slug cannot change after content is published")
			}
		}
		channel.Slug, err = s.availableChannelSlug(requestedSlug, channel.Name, &channel.ID)
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
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&channel).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.channel_updated", "channel", channel.ID, map[string]any{"slug": channel.Slug})
	}); err != nil {
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
	if err := s.db.Model(&model.ContentEntry{}).Where("channel_id = ?", channel.ID).Count(&contentCount).Error; err != nil {
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
	if s.db.Migrator().HasTable(&model.VideoProcessingJob{}) {
		var activeJobCount int64
		if err := s.db.Unscoped().Model(&model.VideoProcessingJob{}).
			Joins("JOIN videos ON videos.id = video_processing_jobs.video_id").
			Where("videos.channel_id = ? AND video_processing_jobs.status IN ?", channel.ID, []string{"pending", "processing"}).
			Count(&activeJobCount).Error; err != nil {
			return err
		}
		if activeJobCount > 0 {
			return apperr.Conflict("studio.channel_processing_in_progress", "Channel processing must finish before deletion")
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&channel).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channel.ID).Delete(&model.ContentCollection{}).Error; err != nil {
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
		return recordStudioAudit(tx, user.ID, "studio.channel_deleted", "channel", channel.ID, map[string]any{"slug": channel.Slug})
	})
}

func (s *Service) ListUnifiedContents(user authctx.CurrentUser, channelID uuid.UUID, kind, status string, collectionID uuid.UUID) ([]model.ContentEntry, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}
	if _, err := s.ownedChannel(user.ID, channelID); err != nil {
		return nil, err
	}
	return s.repo.ListUnifiedContents(channelID, kind, status, collectionID)
}

func (s *Service) ListUnifiedCollections(user authctx.CurrentUser, channelID uuid.UUID) ([]model.ContentCollection, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}
	if _, err := s.ownedChannel(user.ID, channelID); err != nil {
		return nil, err
	}
	return s.repo.ListUnifiedCollections(channelID)
}

func (s *Service) ListCollections(user authctx.CurrentUser, channelID uuid.UUID, module Module) ([]model.Collection, error) {
	if err := requireUser(user); err != nil {
		return nil, err
	}
	if _, err := s.ownedChannel(user.ID, channelID); err != nil {
		return nil, err
	}
	if module == ModuleBlog {
		collections, err := s.repo.ListUnifiedCollections(channelID)
		if err != nil {
			return nil, err
		}
		result := make([]model.Collection, 0, len(collections))
		for _, collection := range collections {
			result = append(result, studioCollectionFromContentCollection(collection, module))
		}
		return result, nil
	}
	return s.repo.ListCollections(channelID, module)
}

func (s *Service) CreateCollection(user authctx.CurrentUser, module Module, input CreateCollectionInput) (model.Collection, error) {
	if module == ModuleBlog {
		if err := s.ensureCollectionNameAvailable(input.ChannelID, module, strings.TrimSpace(input.Name), nil); err != nil {
			return model.Collection{}, err
		}
		collection, err := s.CreateUnifiedCollection(user, input)
		if err != nil {
			return model.Collection{}, err
		}
		return studioCollectionFromContentCollection(collection, module), nil
	}
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
	if err := s.ensureCollectionNameAvailable(input.ChannelID, module, name, nil); err != nil {
		return model.Collection{}, err
	}
	collection := model.Collection{
		ChannelID:   input.ChannelID,
		ContentType: string(module),
		CreatedBy:   &user.ID,
		Name:        name,
		Description: strings.TrimSpace(input.Description),
		CoverURL:    strings.TrimSpace(input.CoverURL),
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&collection).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.collection_created", "collection", collection.ID, map[string]any{"module": module, "channel_id": collection.ChannelID.String()})
	}); err != nil {
		return model.Collection{}, err
	}
	return collection, nil
}

func (s *Service) UpdateCollection(user authctx.CurrentUser, module Module, collectionID uuid.UUID, input UpdateCollectionInput) (model.Collection, error) {
	if module == ModuleBlog {
		if input.Name != nil {
			var existing model.ContentCollection
			if err := s.db.First(&existing, "id = ?", collectionID).Error; err != nil {
				return model.Collection{}, err
			}
			if err := s.ensureCollectionNameAvailable(existing.ChannelID, module, strings.TrimSpace(*input.Name), &collectionID); err != nil {
				return model.Collection{}, err
			}
		}
		collection, err := s.UpdateUnifiedCollection(user, collectionID, input)
		if err != nil {
			return model.Collection{}, err
		}
		return studioCollectionFromContentCollection(collection, module), nil
	}
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
		if err := s.ensureCollectionNameAvailable(collection.ChannelID, module, collection.Name, &collection.ID); err != nil {
			return model.Collection{}, err
		}
	}
	if input.Description != nil {
		collection.Description = strings.TrimSpace(*input.Description)
	}
	if input.CoverURL != nil {
		collection.CoverURL = strings.TrimSpace(*input.CoverURL)
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&collection).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.collection_updated", "collection", collection.ID, map[string]any{"module": module})
	}); err != nil {
		return model.Collection{}, err
	}
	return collection, nil
}

func (s *Service) DeleteCollection(user authctx.CurrentUser, module Module, collectionID uuid.UUID) error {
	if module == ModuleBlog {
		return s.DeleteUnifiedCollection(user, collectionID)
	}
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
	if collection.IsDefault {
		return apperr.Conflict("studio.default_collection_protected", "The system default collection cannot be deleted")
	}
	var contentCount int64
	countQueries := []struct {
		model any
		where string
	}{
		{&model.Post{}, "collection_id = ?"},
		{&model.ContentBlogDraft{}, "collection_id = ?"},
		{&model.Video{}, "collection_id = ?"},
		{&model.PostCollection{}, "collection_id = ?"},
		{&model.VideoCollection{}, "collection_id = ?"},
	}
	for _, query := range countQueries {
		if !s.db.Migrator().HasTable(query.model) {
			continue
		}
		if err := s.db.Model(query.model).Where(query.where, collection.ID).Count(&contentCount).Error; err != nil {
			return err
		}
		if contentCount > 0 {
			return apperr.Conflict("studio.collection_not_empty", "Collection content must be moved before deletion")
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.StudioModuleSettings{}).Where("default_collection_id = ?", collection.ID).
			Update("default_collection_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Delete(&collection).Error; err != nil {
			return err
		}
		return recordStudioAudit(tx, user.ID, "studio.collection_deleted", "collection", collection.ID, map[string]any{"module": module})
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
	if len(collectionIDs) > 1 {
		return apperr.Unprocessable("studio.multiple_collections_not_supported", "Only one collection can be selected")
	}
	if len(collectionIDs) == 0 {
		if publishing {
			return apperr.BadRequest("studio.collection_required", "A collection is required before publishing")
		}
		return nil
	}
	_, err := s.ResolveContentCollection(userID, channelID, module, &collectionIDs[0], nil, false)
	return err
}

// ResolveContentCollection normalizes the scalar contract and the temporary legacy array contract.
func (s *Service) ResolveContentCollection(userID, channelID uuid.UUID, module Module, collectionID *uuid.UUID, legacyIDs []uuid.UUID, publishing bool) (*uuid.UUID, error) {
	if userID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	if _, err := s.ownedChannel(userID, channelID); err != nil {
		return nil, err
	}
	legacyIDs = uniqueUUIDs(legacyIDs)
	if len(legacyIDs) > 1 {
		return nil, apperr.Unprocessable("studio.multiple_collections_not_supported", "Only one collection can be selected")
	}
	if collectionID != nil && len(legacyIDs) == 1 && *collectionID != legacyIDs[0] {
		return nil, apperr.BadRequest("studio.collection_input_conflict", "collection_id and collection_ids must identify the same collection")
	}
	if collectionID == nil && len(legacyIDs) == 1 {
		id := legacyIDs[0]
		collectionID = &id
	}
	if collectionID != nil {
		var count int64
		if err := s.db.Model(&model.Collection{}).
			Where("id = ? AND channel_id = ? AND content_type = ?", *collectionID, channelID, module).
			Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 1 {
			return nil, apperr.BadRequest("studio.invalid_collection_scope", "Collection must belong to the selected channel and module")
		}
		return collectionID, nil
	}
	if !publishing {
		return nil, nil
	}
	if s.db.Migrator().HasTable(&model.StudioModuleSettings{}) {
		var settings model.StudioModuleSettings
		if err := s.db.Where("channel_id = ? AND content_type = ?", channelID, module).First(&settings).Error; err == nil && settings.DefaultCollectionID != nil {
			if resolved, resolveErr := s.ResolveContentCollection(userID, channelID, module, settings.DefaultCollectionID, nil, false); resolveErr == nil {
				return resolved, nil
			}
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	fallback, err := s.ensureSystemDefaultCollection(channelID, module, userID)
	if err != nil {
		return nil, err
	}
	return &fallback.ID, nil
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
	if module == ModuleBlog {
		var collection model.ContentCollection
		if err := s.db.First(&collection, "id = ?", collectionID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return model.Collection{}, apperr.NotFound("studio.collection_not_found", "Collection not found")
			}
			return model.Collection{}, err
		}
		return studioCollectionFromContentCollection(collection, module), nil
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

func (s *Service) ensureSystemDefaultCollection(channelID uuid.UUID, module Module, ownerID uuid.UUID) (model.Collection, error) {
	if module == ModuleBlog {
		var collection model.ContentCollection
		err := s.db.Where("channel_id = ? AND is_default = ?", channelID, true).First(&collection).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			collection = model.ContentCollection{ChannelID: channelID, CreatedBy: &ownerID, Name: "未分类文章", IsDefault: true}
			if err := s.db.Create(&collection).Error; err != nil {
				return model.Collection{}, err
			}
			return studioCollectionFromContentCollection(collection, module), nil
		}
		if err != nil {
			return model.Collection{}, err
		}
		return studioCollectionFromContentCollection(collection, module), nil
	}
	var collection model.Collection
	err := s.db.Where("channel_id = ? AND content_type = ? AND is_default = ?", channelID, module, true).First(&collection).Error
	if err == nil {
		return collection, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Collection{}, err
	}
	names := map[Module]string{ModuleBlog: "未分类文章", ModulePodcast: "未分类单集", ModuleVideo: "未分类视频"}
	name := names[module]
	err = s.db.Where("channel_id = ? AND content_type = ? AND LOWER(name) = LOWER(?)", channelID, module, name).First(&collection).Error
	if err == nil {
		if err := s.db.Model(&collection).Update("is_default", true).Error; err != nil {
			return model.Collection{}, err
		}
		collection.IsDefault = true
		return collection, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Collection{}, err
	}
	collection = model.Collection{
		ChannelID: channelID, ContentType: string(module), CreatedBy: &ownerID,
		Name: name, IsDefault: true,
	}
	if err := s.db.Create(&collection).Error; err != nil {
		return model.Collection{}, err
	}
	return collection, nil
}

func createSystemDefaultCollections(tx *gorm.DB, channelID, ownerID uuid.UUID) error {
	names := map[Module]string{
		ModulePodcast: "未分类单集",
		ModuleVideo:   "未分类视频",
	}
	blogCollection := model.ContentCollection{ChannelID: channelID, CreatedBy: &ownerID, Name: "未分类文章", IsDefault: true}
	if err := tx.Create(&blogCollection).Error; err != nil {
		return fmt.Errorf("create %s system default collection: %w", ModuleBlog, err)
	}
	for _, module := range []Module{ModulePodcast, ModuleVideo} {
		collection := model.Collection{
			ChannelID: channelID, ContentType: string(module), CreatedBy: &ownerID,
			Name: names[module], IsDefault: true,
		}
		if err := tx.Create(&collection).Error; err != nil {
			return fmt.Errorf("create %s system default collection: %w", module, err)
		}
	}
	return nil
}

func (s *Service) ensureChannelNameAvailable(name string, excludeID *uuid.UUID) error {
	query := s.db.Model(&model.Channel{}).Where("LOWER(name) = LOWER(?)", strings.TrimSpace(name))
	if excludeID != nil {
		query = query.Where("id <> ?", *excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return apperr.Conflict("studio.channel_name_taken", "Channel name is already in use")
	}
	return nil
}

func (s *Service) ensureCollectionNameAvailable(channelID uuid.UUID, module Module, name string, excludeID *uuid.UUID) error {
	var query *gorm.DB
	if module == ModuleBlog {
		query = s.db.Model(&model.ContentCollection{}).Where("channel_id = ? AND LOWER(name) = LOWER(?)", channelID, strings.TrimSpace(name))
	} else {
		query = s.db.Model(&model.Collection{}).Where("channel_id = ? AND content_type = ? AND LOWER(name) = LOWER(?)", channelID, module, strings.TrimSpace(name))
	}
	if excludeID != nil {
		query = query.Where("id <> ?", *excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return apperr.Conflict("studio.collection_name_taken", "Collection name is already in use")
	}
	return nil
}

func (s *Service) channelSlugLocked(channelID uuid.UUID) (bool, error) {
	var count int64
	if err := s.db.Model(&model.ContentEntry{}).Where("channel_id = ? AND kind = ? AND status = ?", channelID, "blog", "published").Count(&count).Error; err != nil {
		return false, err
	}
	if count == 0 {
		if err := s.db.Model(&model.Video{}).Where("channel_id = ? AND status = ?", channelID, "published").Count(&count).Error; err != nil {
			return false, err
		}
	}
	return count > 0, nil
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

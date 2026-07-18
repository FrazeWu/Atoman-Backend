package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/service"
)

const defaultCollectionName = "默认专栏"

var slugInvalidChars = regexp.MustCompile(`[^a-z0-9一-龥]+`)

func ownsChannel(channelUserID *uuid.UUID, userID uuid.UUID) bool {
	return channelUserID != nil && *channelUserID == userID
}

func normalizeName(name string) string {
	return strings.TrimSpace(name)
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

func uniqueChannelSlug(db *gorm.DB, base string, excludeID *uuid.UUID) (string, error) {
	baseSlug := slugify(base)
	candidate := baseSlug
	counter := 2
	namespace := service.NewSiteNamespaceService(db)

	for {
		err := namespace.ValidateChannelSlugAvailable(context.Background(), candidate, excludeID)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, service.ErrSiteHandleReserved) && !errors.Is(err, service.ErrSiteHandleTaken) {
			return "", err
		}

		candidate = fmt.Sprintf("%s-%d", baseSlug, counter)
		counter++
	}
}

func validateExplicitChannelSlug(db *gorm.DB, raw string, excludeID *uuid.UUID) (string, error) {
	slug := slugify(raw)
	if err := service.NewSiteNamespaceService(db).ValidateChannelSlugAvailable(context.Background(), slug, excludeID); err != nil {
		return "", err
	}
	return slug, nil
}

func channelNameExists(db *gorm.DB, name string, excludeID *uuid.UUID) (bool, error) {
	query := db.Model(&model.Channel{}).Where("LOWER(name) = LOWER(?)", normalizeName(name))
	if excludeID != nil {
		query = query.Where("id <> ?", *excludeID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}

	return count > 0, nil
}

func collectionNameExists(db *gorm.DB, channelID uuid.UUID, name string, excludeID *uuid.UUID) (bool, error) {
	query := db.Model(&model.Collection{}).
		Where("channel_id = ?", channelID).
		Where("name = ?", normalizeName(name))
	if excludeID != nil {
		query = query.Where("id <> ?", *excludeID)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}

	return count > 0, nil
}

func ensureDefaultCollection(db *gorm.DB, channelID uuid.UUID) (*model.Collection, error) {
	var collection model.Collection

	err := db.Where("channel_id = ? AND content_type = ? AND is_default = ?", channelID, "blog", true).First(&collection).Error
	if err == nil {
		if strings.TrimSpace(collection.Name) == "" {
			collection.Name = defaultCollectionName
			if saveErr := db.Model(&collection).Update("name", defaultCollectionName).Error; saveErr != nil {
				return nil, saveErr
			}
		}
		return &collection, nil
	}

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	err = db.Where("channel_id = ? AND content_type = ? AND name = ?", channelID, "blog", defaultCollectionName).First(&collection).Error
	if err == nil {
		if !collection.IsDefault {
			if saveErr := db.Model(&collection).Update("is_default", true).Error; saveErr != nil {
				return nil, saveErr
			}
			collection.IsDefault = true
		}
		return &collection, nil
	}

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Check for soft-deleted record that would block the unique index
	var softDeleted model.Collection
	softErr := db.Unscoped().
		Where("channel_id = ? AND content_type = ? AND (is_default = ? OR name = ?)", channelID, "blog", true, defaultCollectionName).
		First(&softDeleted).Error
	if softErr == nil && softDeleted.DeletedAt.Valid {
		if restoreErr := db.Unscoped().Model(&softDeleted).Updates(map[string]interface{}{
			"deleted_at":   nil,
			"content_type": "blog",
			"is_default":   true,
			"name":         defaultCollectionName,
		}).Error; restoreErr != nil {
			return nil, restoreErr
		}
		softDeleted.DeletedAt = gorm.DeletedAt{}
		softDeleted.IsDefault = true
		softDeleted.Name = defaultCollectionName
		return &softDeleted, nil
	}

	collection = model.Collection{
		ChannelID:   channelID,
		ContentType: "blog",
		Name:        defaultCollectionName,
		Description: "合集默认子合集",
		IsDefault:   true,
	}

	if err := db.Create(&collection).Error; err != nil {
		return nil, err
	}

	return &collection, nil
}

// EnsureDefaultChannelForUser resolves the user's current Studio channel or creates the first one.
func EnsureDefaultChannelForUser(db *gorm.DB, userID uuid.UUID, username string) (*model.Channel, error) {
	var state model.UserStudioState
	err := db.Preload("Channel").First(&state, "user_id = ?", userID).Error
	if err == nil && state.Channel != nil && ownsChannel(state.Channel.UserID, userID) {
		if _, ensureErr := ensureDefaultCollection(db, state.Channel.ID); ensureErr != nil {
			return nil, ensureErr
		}
		return state.Channel, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var channel model.Channel
	err = db.Where("user_id = ?", userID).Order("created_at ASC, id ASC").First(&channel).Error
	if err == nil {
		if _, ensureErr := ensureDefaultCollection(db, channel.ID); ensureErr != nil {
			return nil, ensureErr
		}
		if err := saveStudioState(db, userID, channel.ID); err != nil {
			return nil, err
		}
		return &channel, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Create a new default channel
	defaultChannelName := fmt.Sprintf("%s 的合集", username)
	channelSlug, err := uniqueChannelSlug(db, defaultChannelName, nil)
	if err != nil {
		return nil, err
	}

	channel = model.Channel{
		UserID:      &userID,
		Name:        defaultChannelName,
		Slug:        channelSlug,
		Description: "默认合集",
	}

	if err := db.Create(&channel).Error; err != nil {
		return nil, err
	}

	// Create default collection for the channel
	if _, err := ensureDefaultCollection(db, channel.ID); err != nil {
		return nil, err
	}
	if err := ensureStudioState(db, userID, channel.ID); err != nil {
		return nil, err
	}

	// Auto-subscribe user to their own channel
	if err := autoSubscribeToChannel(db, userID, channel.ID); err != nil {
		// Log error but don't fail the operation
		fmt.Printf("Warning: Failed to auto-subscribe to channel: %v\n", err)
	}

	return &channel, nil
}

func ensureStudioState(db *gorm.DB, userID uuid.UUID, channelID uuid.UUID) error {
	var state model.UserStudioState
	err := db.First(&state, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Create(&model.UserStudioState{UserID: userID, ChannelID: &channelID}).Error
	}
	if err != nil {
		return err
	}
	if state.ChannelID == nil {
		return db.Model(&state).Update("channel_id", channelID).Error
	}
	return nil
}

func saveStudioState(db *gorm.DB, userID uuid.UUID, channelID uuid.UUID) error {
	return db.Save(&model.UserStudioState{UserID: userID, ChannelID: &channelID}).Error
}

// autoSubscribeToChannel creates a feed subscription for user to their own channel
func autoSubscribeToChannel(db *gorm.DB, userID uuid.UUID, channelID uuid.UUID) error {
	// Check if subscription already exists via FeedSource
	var existingCount int64
	if err := db.Table("feed_sources").Where(
		"source_type = ? AND source_id = ?",
		"internal_channel", channelID,
	).Count(&existingCount).Error; err != nil {
		return err
	}

	if existingCount > 0 {
		// Feed source exists, check if user is already subscribed
		var source model.FeedSource
		if err := db.Where("source_type = ? AND source_id = ?", "internal_channel", channelID).First(&source).Error; err == nil {
			var subCount int64
			if err := db.Model(&model.Subscription{}).Where(
				"user_id = ? AND feed_source_id = ?",
				userID, source.ID,
			).Count(&subCount).Error; err == nil && subCount > 0 {
				return nil // Already subscribed
			}
		}
	}

	// Get or create default subscription group
	var group model.SubscriptionGroup
	if err := db.Where("user_id = ? AND name = ?", userID, "默认分组").First(&group).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			group = model.SubscriptionGroup{
				UserID: userID,
				Name:   "默认分组",
			}
			if err := db.Create(&group).Error; err != nil {
				return err
			}
		} else {
			return err
		}
	}

	// Create feed source if not exists
	var source model.FeedSource
	sourceHash := fmt.Sprintf("%s:%s", "internal_channel", channelID.String())
	h := sha256.New()
	h.Write([]byte(sourceHash))
	hash := hex.EncodeToString(h.Sum(nil))

	if err := db.Where("hash = ?", hash).First(&source).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var ch model.Channel
			if err := db.First(&ch, channelID).Error; err == nil {
				source = model.FeedSource{
					SourceType: "internal_channel",
					SourceID:   &channelID,
					Title:      ch.Name,
					Hash:       hash,
				}
				if err := db.Create(&source).Error; err != nil {
					return err
				}
			}
		} else {
			return err
		}
	}

	// Create subscription
	subscription := model.Subscription{
		UserID:              userID,
		FeedSourceID:        source.ID,
		SubscriptionGroupID: &group.ID,
		Title:               source.Title,
	}

	return db.Create(&subscription).Error
}

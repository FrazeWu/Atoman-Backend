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
	"gorm.io/gorm/clause"

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

	err := db.Where("channel_id = ? AND is_default = ?", channelID, true).First(&collection).Error
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

	err = db.Where("channel_id = ? AND name = ?", channelID, defaultCollectionName).First(&collection).Error
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
		Where("channel_id = ? AND (is_default = ? OR name = ?)", channelID, true, defaultCollectionName).
		First(&softDeleted).Error
	if softErr == nil && softDeleted.DeletedAt.Valid {
		if restoreErr := db.Unscoped().Model(&softDeleted).Updates(map[string]interface{}{
			"deleted_at": nil,
			"is_default": true,
			"name":       defaultCollectionName,
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
		Name:        defaultCollectionName,
		Description: "合集默认子合集",
		IsDefault:   true,
	}

	if err := db.Create(&collection).Error; err != nil {
		return nil, err
	}

	return &collection, nil
}

// EnsureDefaultChannelForUser creates a default channel for a user if they don't have one
func EnsureDefaultChannelForUser(db *gorm.DB, userID uuid.UUID, username string) (*model.Channel, error) {
	var channel model.Channel

	// Check if user already has a default channel
	err := db.Where("user_id = ? AND is_default = ?", userID, true).First(&channel).Error
	if err == nil {
		if channel.ContentType == "" {
			if saveErr := db.Model(&channel).Update("content_type", model.ChannelContentTypeBlog).Error; saveErr != nil {
				return nil, saveErr
			}
			channel.ContentType = model.ChannelContentTypeBlog
		}
		if err := upsertDefaultChannelSelection(db, userID, channel.ID); err != nil {
			return nil, err
		}
		return &channel, nil
	}

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// Check if user has any channel
	var channels []model.Channel
	if err := db.Where("user_id = ?", userID).Find(&channels).Error; err != nil {
		return nil, err
	}

	// If user has channels but none marked as default, mark the first one as default
	if len(channels) > 0 {
		channels[0].IsDefault = true
		updates := map[string]any{"is_default": true}
		if channels[0].ContentType == "" {
			updates["content_type"] = model.ChannelContentTypeBlog
		}
		if err := db.Model(&channels[0]).Updates(updates).Error; err != nil {
			return nil, err
		}
		if channels[0].ContentType == "" {
			channels[0].ContentType = model.ChannelContentTypeBlog
		}
		if err := upsertDefaultChannelSelection(db, userID, channels[0].ID); err != nil {
			return nil, err
		}
		return &channels[0], nil
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
		ContentType: model.ChannelContentTypeBlog,
		IsDefault:   true,
	}

	if err := db.Create(&channel).Error; err != nil {
		return nil, err
	}

	// Create default collection for the channel
	if _, err := ensureDefaultCollection(db, channel.ID); err != nil {
		return nil, err
	}
	if err := upsertDefaultChannelSelection(db, userID, channel.ID); err != nil {
		return nil, err
	}

	// Auto-subscribe user to their own channel
	if err := autoSubscribeToChannel(db, userID, channel.ID); err != nil {
		// Log error but don't fail the operation
		fmt.Printf("Warning: Failed to auto-subscribe to channel: %v\n", err)
	}

	return &channel, nil
}

func upsertDefaultChannelSelection(db *gorm.DB, userID uuid.UUID, channelID uuid.UUID) error {
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "content_type"}},
		DoUpdates: clause.AssignmentColumns([]string{"channel_id", "updated_at"}),
	}).Create(&model.UserDefaultChannel{
		UserID:      userID,
		ContentType: model.ChannelContentTypeBlog,
		ChannelID:   channelID,
	}).Error
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

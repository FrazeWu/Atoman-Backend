package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultSubscriptionGroupName = "默认分组"
	defaultBookmarkFolderName    = "默认收藏夹"
	defaultChannelDescription    = "默认合集"
	defaultCollectionName        = "默认合集"
)

type UserBootstrapService struct {
	db *gorm.DB
}

func NewUserBootstrapService(db *gorm.DB) *UserBootstrapService {
	return &UserBootstrapService{db: db}
}

func (s *UserBootstrapService) EnsureDefaults(userID uuid.UUID, username string) error {
	for _, contentType := range []string{
		model.ChannelContentTypeBlog,
		model.ChannelContentTypePodcast,
		model.ChannelContentTypeVideo,
	} {
		channel, err := s.ensureDefaultChannel(userID, username, contentType)
		if err != nil {
			return err
		}
		if err := s.ensureDefaultCollectionForChannel(channel.ID); err != nil {
			return err
		}
		if err := s.upsertUserDefaultChannelSelection(userID, contentType, channel.ID); err != nil {
			return err
		}
	}

	group, err := s.ensureDefaultSubscriptionGroup(userID)
	if err != nil {
		return err
	}
	if err := s.ensureSelfSubscription(userID, username, group.ID); err != nil {
		return err
	}
	if err := s.ensureDefaultBookmarkFolder(userID); err != nil {
		return err
	}
	if err := s.ensureFavoritePlaylist(userID); err != nil {
		return err
	}

	return nil
}

func (s *UserBootstrapService) ensureDefaultChannel(userID uuid.UUID, username string, contentType string) (*model.Channel, error) {
	var selection model.UserDefaultChannel
	if err := s.db.Preload("Channel").Where("user_id = ? AND content_type = ?", userID, contentType).First(&selection).Error; err == nil {
		if selection.Channel != nil && selection.Channel.UserID != nil && *selection.Channel.UserID == userID && model.NormalizeChannelContentType(selection.Channel.ContentType) == contentType {
			return selection.Channel, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	var channel model.Channel
	err := s.db.Where("user_id = ? AND content_type = ?", userID, contentType).Order("created_at ASC").First(&channel).Error
	if err == nil {
		return &channel, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	baseName := strings.TrimSpace(username)
	if baseName == "" {
		baseName = defaultChannelDescription
	}
	slugBase := strings.TrimSpace(username)
	if slugBase == "" {
		slugBase = "channel"
	}
	switch contentType {
	case model.ChannelContentTypePodcast:
		baseName += " 播客"
		slugBase += "-podcast"
	case model.ChannelContentTypeVideo:
		baseName += " 视频"
		slugBase += "-video"
	}

	name, err := s.uniqueChannelName(baseName)
	if err != nil {
		return nil, err
	}
	slug, err := s.uniqueChannelSlug(slugBase)
	if err != nil {
		return nil, err
	}

	channel = model.Channel{
		UserID:      &userID,
		Name:        name,
		Slug:        slug,
		Description: defaultChannelDescription,
		ContentType: contentType,
		IsDefault:   contentType == model.ChannelContentTypeBlog,
	}
	if err := s.db.Create(&channel).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}

func (s *UserBootstrapService) upsertUserDefaultChannelSelection(userID uuid.UUID, contentType string, channelID uuid.UUID) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "content_type"}},
		DoUpdates: clause.AssignmentColumns([]string{"channel_id", "updated_at"}),
	}).Create(&model.UserDefaultChannel{
		UserID:      userID,
		ContentType: contentType,
		ChannelID:   channelID,
	}).Error
}

func (s *UserBootstrapService) ensureDefaultCollectionForChannel(channelID uuid.UUID) error {
	var collection model.Collection
	err := s.db.Where("channel_id = ? AND is_default = ?", channelID, true).First(&collection).Error
	if err == nil {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	var softDeleted model.Collection
	softErr := s.db.Unscoped().Where("channel_id = ? AND name = ?", channelID, defaultCollectionName).First(&softDeleted).Error
	if softErr == nil && softDeleted.DeletedAt.Valid {
		return s.db.Unscoped().Model(&softDeleted).Updates(map[string]any{
			"deleted_at": nil,
			"is_default": true,
			"name":       defaultCollectionName,
		}).Error
	}
	if softErr != nil && !errors.Is(softErr, gorm.ErrRecordNotFound) {
		return softErr
	}

	collection = model.Collection{
		ChannelID:   channelID,
		Name:        defaultCollectionName,
		Description: defaultChannelDescription,
		IsDefault:   true,
	}
	return s.db.Create(&collection).Error
}

func (s *UserBootstrapService) ensureDefaultSubscriptionGroup(userID uuid.UUID) (*model.SubscriptionGroup, error) {
	var group model.SubscriptionGroup
	err := s.db.Where("user_id = ? AND name = ?", userID, defaultSubscriptionGroupName).First(&group).Error
	if err == nil {
		return &group, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	group = model.SubscriptionGroup{
		UserID: userID,
		Name:   defaultSubscriptionGroupName,
	}
	result := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "name"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil},
		}},
		DoNothing: true,
	}).Create(&group)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		err = s.db.Where("user_id = ? AND name = ?", userID, defaultSubscriptionGroupName).First(&group).Error
		if err != nil {
			return nil, err
		}
	}

	return &group, nil
}

func (s *UserBootstrapService) ensureSelfSubscription(userID uuid.UUID, username string, groupID uuid.UUID) error {
	source, err := s.ensureInternalUserFeedSource(userID, username)
	if err != nil {
		return err
	}

	var existing model.Subscription
	err = s.db.Where("user_id = ? AND feed_source_id = ?", userID, source.ID).First(&existing).Error
	if err == nil {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	subscription := model.Subscription{
		UserID:              userID,
		FeedSourceID:        source.ID,
		Title:               source.Title,
		SubscriptionGroupID: &groupID,
	}
	return s.db.Create(&subscription).Error
}

func (s *UserBootstrapService) ensureInternalUserFeedSource(userID uuid.UUID, username string) (*model.FeedSource, error) {
	hash := buildUserBootstrapFeedSourceHash("internal_user", userID)

	var source model.FeedSource
	err := s.db.Where("hash = ?", hash).First(&source).Error
	if err == nil {
		return &source, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	title := strings.TrimSpace(username)
	if title == "" {
		title = "默认频道"
	}

	source = model.FeedSource{
		SourceType: "internal_user",
		SourceID:   &userID,
		Title:      title,
		Hash:       hash,
	}
	if err := s.db.Create(&source).Error; err != nil {
		return nil, err
	}

	return &source, nil
}

func (s *UserBootstrapService) ensureDefaultBookmarkFolder(userID uuid.UUID) error {
	var folder model.BookmarkFolder
	err := s.db.Where("user_id = ? AND name = ?", userID, defaultBookmarkFolderName).First(&folder).Error
	if err == nil {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	folder = model.BookmarkFolder{
		UserID: userID,
		Name:   defaultBookmarkFolderName,
	}
	return s.db.Create(&folder).Error
}

func (s *UserBootstrapService) ensureFavoritePlaylist(userID uuid.UUID) error {
	var playlist model.Playlist
	err := s.db.Where("user_id = ? AND is_favorite = ?", userID, true).First(&playlist).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = s.db.Where("user_id = ? AND name = ?", userID, "最爱").First(&playlist).Error
	}
	if err == nil {
		return s.db.Model(&playlist).Updates(map[string]any{"is_favorite": true, "is_public": false}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.db.Create(&model.Playlist{UserID: userID, Name: "最爱", IsFavorite: true}).Error
}

func buildUserBootstrapFeedSourceHash(sourceType string, sourceID uuid.UUID) string {
	raw := fmt.Sprintf("%s:%s", sourceType, sourceID.String())
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *UserBootstrapService) uniqueChannelSlug(base string) (string, error) {
	baseSlug := slugifyChannelName(base)
	candidate := baseSlug
	counter := 2
	namespace := NewSiteNamespaceService(s.db)
	for {
		err := namespace.ValidateChannelSlugAvailable(context.Background(), candidate, nil)
		if err == nil {
			return candidate, nil
		}
		if !errors.Is(err, ErrSiteHandleReserved) && !errors.Is(err, ErrSiteHandleTaken) {
			return "", err
		}
		candidate = fmt.Sprintf("%s-%d", baseSlug, counter)
		counter++
	}
}

func (s *UserBootstrapService) uniqueChannelName(base string) (string, error) {
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

func slugifyChannelName(value string) string {
	slug := strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r >= '一' && r <= '龥':
			b.WriteRune(r)
			lastDash = false
		default:
			if b.Len() == 0 || lastDash {
				continue
			}
			b.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "channel"
	}
	return result
}

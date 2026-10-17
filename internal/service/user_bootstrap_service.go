package service

import (
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
)

type UserBootstrapService struct {
	db *gorm.DB
}

func NewUserBootstrapService(db *gorm.DB) *UserBootstrapService {
	return &UserBootstrapService{db: db}
}

func (s *UserBootstrapService) EnsureDefaults(userID uuid.UUID, displayName, username string) error {
	name := model.DisplayNameOrUsername(displayName, username)
	if name == "" {
		name = "默认频道"
	}
	group, err := s.ensureDefaultSubscriptionGroup(userID)
	if err != nil {
		return err
	}
	if err := s.ensureSelfSubscription(userID, name, group.ID); err != nil {
		return err
	}
	if err := s.ensureDefaultBookmarkFolder(userID); err != nil {
		return err
	}
	return nil
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

func (s *UserBootstrapService) ensureSelfSubscription(userID uuid.UUID, name string, groupID uuid.UUID) error {
	source, err := s.ensureInternalUserFeedSource(userID, name)
	if err != nil {
		return err
	}

	var existing model.Subscription
	err = s.db.Where("user_id = ? AND feed_source_id = ?", userID, source.ID).First(&existing).Error
	if err == nil {
		if existing.Title == source.Title {
			return nil
		}
		return s.db.Model(&existing).Update("title", source.Title).Error
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

func (s *UserBootstrapService) ensureInternalUserFeedSource(userID uuid.UUID, name string) (*model.FeedSource, error) {
	hash := buildUserBootstrapFeedSourceHash("internal_user", userID)

	var source model.FeedSource
	err := s.db.Where("hash = ?", hash).First(&source).Error
	if err == nil {
		if source.Title != name {
			if err := s.db.Model(&source).Update("title", name).Error; err != nil {
				return nil, err
			}
			source.Title = name
		}
		return &source, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	title := strings.TrimSpace(name)
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

func buildUserBootstrapFeedSourceHash(sourceType string, sourceID uuid.UUID) string {
	raw := fmt.Sprintf("%s:%s", sourceType, sourceID.String())
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

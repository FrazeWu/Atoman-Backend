package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	blog "atoman/internal/modules/blog"
	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultSubscriptionGroupName = "默认分组"
	defaultBookmarkFolderName    = "默认收藏"
)

type UserBootstrapService struct {
	db *gorm.DB
}

func NewUserBootstrapService(db *gorm.DB) *UserBootstrapService {
	return &UserBootstrapService{db: db}
}

func (s *UserBootstrapService) EnsureDefaults(userID uuid.UUID, username string) error {
	if _, err := blog.NewService(s.db).CreateDefaultChannelForUser(userID, username); err != nil {
		return err
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
	if err := s.db.Create(&group).Error; err != nil {
		return nil, err
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

func buildUserBootstrapFeedSourceHash(sourceType string, sourceID uuid.UUID) string {
	raw := fmt.Sprintf("%s:%s", sourceType, sourceID.String())
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

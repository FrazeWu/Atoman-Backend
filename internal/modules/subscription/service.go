package subscription

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const defaultSubscriptionGroupName = "默认分组"

type Service struct {
	db   *gorm.DB
	repo *Repo
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

func (s *Service) CreateSubscription(user authctx.CurrentUser, req CreateSubscriptionRequest) (model.Subscription, error) {
	if user.ID == uuid.Nil {
		return model.Subscription{}, apperr.Unauthorized("Login required")
	}

	targetType := strings.TrimSpace(req.TargetType)
	if targetType == "" {
		return model.Subscription{}, apperr.BadRequest("validation.invalid_request", "target_type is required")
	}
	if targetType != "external_rss" {
		return model.Subscription{}, apperr.BadRequest("subscription.unsupported_target_type", "target_type is not supported")
	}

	rssURL := strings.TrimSpace(req.RSSURL)
	if rssURL == "" {
		return model.Subscription{}, apperr.BadRequest("validation.invalid_request", "rss_url is required")
	}
	parsed, err := url.ParseRequestURI(rssURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return model.Subscription{}, apperr.BadRequest("validation.invalid_request", "rss_url must be an absolute http/https URL")
	}

	title := strings.TrimSpace(req.Title)

	var created model.Subscription
	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		group, err := ensureDefaultGroup(repo, user.ID)
		if err != nil {
			return err
		}

		source, err := findOrCreateExternalFeedSource(repo, rssURL, title)
		if err != nil {
			return err
		}

		if _, err := repo.FindSubscriptionByUserAndSource(user.ID, source.ID); err == nil {
			return apperr.Conflict("subscription.already_exists", "Already subscribed to this source")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		created = model.Subscription{
			UserID:              user.ID,
			FeedSourceID:        source.ID,
			Title:               title,
			SubscriptionGroupID: &group.ID,
		}
		if err := repo.CreateSubscription(&created); err != nil {
			if _, lookupErr := repo.FindSubscriptionByUserAndSource(user.ID, source.ID); lookupErr == nil {
				return apperr.Conflict("subscription.already_exists", "Already subscribed to this source")
			}
			return err
		}
		return nil
	})
	if err != nil {
		return model.Subscription{}, err
	}

	return created, nil
}

func (s *Service) CreateSubscriptionGroup(user authctx.CurrentUser, req CreateSubscriptionGroupRequest) (model.SubscriptionGroup, error) {
	if user.ID == uuid.Nil {
		return model.SubscriptionGroup{}, apperr.Unauthorized("Login required")
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return model.SubscriptionGroup{}, apperr.BadRequest("validation.invalid_request", "name is required")
	}

	groups, err := s.repo.FindDefaultGroup(user.ID, name)
	if err != nil {
		return model.SubscriptionGroup{}, err
	}
	if len(groups) > 0 {
		if name == defaultSubscriptionGroupName {
			return model.SubscriptionGroup{}, apperr.Conflict("subscription_group.default_conflict", "Default group already exists")
		}
		return model.SubscriptionGroup{}, apperr.Conflict("subscription_group.name_conflict", "Subscription group name already exists")
	}

	group := model.SubscriptionGroup{UserID: user.ID, Name: name}
	if err := s.repo.CreateGroup(&group); err != nil {
		groups, lookupErr := s.repo.FindDefaultGroup(user.ID, name)
		if lookupErr == nil && len(groups) > 0 {
			if name == defaultSubscriptionGroupName {
				return model.SubscriptionGroup{}, apperr.Conflict("subscription_group.default_conflict", "Default group already exists")
			}
			return model.SubscriptionGroup{}, apperr.Conflict("subscription_group.name_conflict", "Subscription group name already exists")
		}
		return model.SubscriptionGroup{}, err
	}

	return group, nil
}

func ensureDefaultGroup(repo *Repo, userID uuid.UUID) (model.SubscriptionGroup, error) {
	groups, err := repo.FindDefaultGroup(userID, defaultSubscriptionGroupName)
	if err != nil {
		return model.SubscriptionGroup{}, err
	}
	if len(groups) == 0 {
		group := model.SubscriptionGroup{UserID: userID, Name: defaultSubscriptionGroupName}
		if err := repo.CreateGroup(&group); err != nil {
			return model.SubscriptionGroup{}, err
		}
		return group, nil
	}

	canonical := groups[0]
	if len(groups) == 1 {
		return canonical, nil
	}

	duplicateIDs := make([]uuid.UUID, 0, len(groups)-1)
	for _, group := range groups[1:] {
		duplicateIDs = append(duplicateIDs, group.ID)
	}
	if err := repo.ReassignSubscriptionsToGroup(userID, duplicateIDs, canonical.ID); err != nil {
		return model.SubscriptionGroup{}, err
	}
	if err := repo.DeleteGroups(userID, duplicateIDs); err != nil {
		return model.SubscriptionGroup{}, err
	}
	return canonical, nil
}

func findOrCreateExternalFeedSource(repo *Repo, rssURL string, title string) (model.FeedSource, error) {
	hash := buildFeedSourceHash("external_rss", nil, rssURL)
	source, err := repo.FindFeedSourceByHash(hash)
	if err == nil {
		return source, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.FeedSource{}, err
	}

	source = model.FeedSource{
		SourceType: "external_rss",
		RssURL:     rssURL,
		Hash:       hash,
		Title:      title,
	}
	if err := repo.CreateFeedSource(&source); err != nil {
		if existing, reloadErr := repo.FindFeedSourceByHash(hash); reloadErr == nil {
			return existing, nil
		}
		return model.FeedSource{}, err
	}
	return source, nil
}

func buildFeedSourceHash(targetType string, targetID *uuid.UUID, rssURL string) string {
	var raw string
	if targetType == "external_rss" {
		raw = strings.TrimSpace(rssURL)
	} else {
		raw = fmt.Sprintf("%s:%s", targetType, targetID.String())
	}

	h := sha256.New()
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

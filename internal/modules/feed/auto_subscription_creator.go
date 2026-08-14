package feed

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"atoman/internal/model"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

// AutoAddSubscription godoc
// @Summary 自动添加订阅
// @Description 根据原始输入或用户选择的候选 feed URL 创建或复用来源并添加当前用户订阅。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body AutoSubscriptionAddRequest true "自动添加订阅输入"
// @Success 201 {object} SubscriptionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/auto-add [post]
func AutoAddSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input AutoSubscriptionAddRequest
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid auto-add request body"})
			return
		}

		userIDVal, ok := c.Get("user_id")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Login required"})
			return
		}
		userID, ok := userIDVal.(uuid.UUID)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Login required"})
			return
		}

		target, err := autoSubscriptionTargetForAdd(db, userID, input)
		if err != nil {
			writeAutoSubscriptionError(c, err)
			return
		}

		subscription, err := createAutoSubscription(db, userID, target, input.Title, input.GroupID)
		if err != nil {
			writeAutoSubscriptionError(c, err)
			return
		}

		if target.SourceType == "external_rss" && subscription.FeedSource != nil {
			syncFeedSource(db, *subscription.FeedSource)
		}

		c.JSON(http.StatusCreated, gin.H{"data": subscription, "message": "ok"})
	}
}

func autoSubscriptionTargetForAdd(db *gorm.DB, userID uuid.UUID, input AutoSubscriptionAddRequest) (autoSubscriptionTarget, error) {
	if candidateFeedURL := strings.TrimSpace(input.CandidateFeedURL); candidateFeedURL != "" {
		u, err := parseAutoSubscriptionURL(candidateFeedURL)
		if err != nil {
			return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "candidate_feed_url must be an absolute http/https URL")
		}
		rssURL := normalizeCanonicalFeedURL(u.String())
		return autoSubscriptionTarget{
			Provider:   "rss",
			SourceType: "external_rss",
			Category:   defaultFeedSourceCategory(input.Category),
			Title:      firstNonBlank(input.Title, rssURL),
			RssURL:     rssURL,
			SiteURL:    validAutoSubscriptionSiteURL(input.Input),
			Canonical:  normalizeCanonicalFeedURL(rssURL),
		}, nil
	}

	u, err := parseAutoSubscriptionURL(input.Input)
	if err != nil {
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "input must be an absolute http/https URL")
	}

	if target, ok := githubRepositoryTarget(u); ok {
		target.Title = firstNonBlank(input.Title, target.Title)
		target.Category = defaultFeedSourceCategory(input.Category)
		return target, nil
	}
	if malformedGithubRepositoryPath(u) {
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "input must be an absolute http/https URL")
	}

	canonicalInput := normalizeCanonicalFeedURL(u.String())
	if directURL, err := url.Parse(canonicalInput); err == nil && isLikelyDirectFeedURL(directURL) {
		target := autoSubscriptionTargetFromDirectFeedURL(canonicalInput, firstNonBlank(input.Title, canonicalInput))
		target.Category = defaultFeedSourceCategory(input.Category)
		return target, nil
	}
	if ok, err := probeAutoSubscriptionDirectFeedURL(u); err != nil {
		return autoSubscriptionTarget{}, err
	} else if ok {
		target := autoSubscriptionTargetFromDirectFeedURL(canonicalInput, firstNonBlank(input.Title, canonicalInput))
		target.Category = defaultFeedSourceCategory(input.Category)
		return target, nil
	}

	response, _, err := resolveDiscoveredSubscriptionInput(db, userID, u)
	if err != nil {
		return autoSubscriptionTarget{}, err
	}
	switch response.Status {
	case "multiple_candidates":
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "candidate_feed_url is required when multiple feed candidates are available")
	case "invalid":
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "input must be an absolute http/https URL")
	case "not_found":
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "no feed candidates found for input")
	case "error":
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusInternalServerError, "failed to resolve subscription input")
	}

	if len(response.Candidates) != 1 {
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "no feed candidates found for input")
	}

	candidate := response.Candidates[0]
	if candidate.Source != nil {
		target := autoSubscriptionTargetFromSource(*candidate.Source, input.Title)
		target.Category = defaultFeedSourceCategory(input.Category)
		return target, nil
	}

	feedURL := normalizeCanonicalFeedURL(candidate.FeedURL)
	if feedURL == "" {
		return autoSubscriptionTarget{}, newAutoSubscriptionHTTPError(http.StatusBadRequest, "no feed candidates found for input")
	}
	return autoSubscriptionTarget{
		Provider:   "rss",
		SourceType: "external_rss",
		Category:   defaultFeedSourceCategory(input.Category),
		Title:      firstNonBlank(input.Title, candidate.Title, feedURL),
		RssURL:     feedURL,
		SiteURL:    candidate.SiteURL,
		Canonical:  normalizeCanonicalFeedURL(feedURL),
	}, nil
}

func createAutoSubscription(db *gorm.DB, userID uuid.UUID, target autoSubscriptionTarget, title string, groupID *uuid.UUID) (model.Subscription, error) {
	var subscription model.Subscription
	source, err := findOrCreateAutoAddFeedSource(db, target, firstNonBlank(title, target.Title, target.RssURL))
	if err != nil {
		return subscription, err
	}

	err = db.Transaction(func(tx *gorm.DB) error {
		group, err := autoSubscriptionGroup(tx, userID, groupID)
		if err != nil {
			return err
		}

		var existing model.Subscription
		lookup := tx.Where("user_id = ? AND feed_source_id = ?", userID, source.ID).Limit(1).Find(&existing)
		if lookup.Error != nil {
			return lookup.Error
		}
		if lookup.RowsAffected > 0 {
			return newAutoSubscriptionHTTPError(http.StatusConflict, "Already subscribed to this source")
		}

		subscription = model.Subscription{
			UserID:              userID,
			FeedSourceID:        source.ID,
			Title:               firstNonBlank(title, target.Title, source.Title),
			SubscriptionGroupID: &group.ID,
			Position:            nextSubscriptionPosition(tx, userID, &group.ID),
		}
		if err := tx.Create(&subscription).Error; err != nil {
			if isAutoSubscriptionDuplicateSubscriptionError(err) {
				return newAutoSubscriptionHTTPError(http.StatusConflict, "Already subscribed to this source")
			}
			return err
		}
		applySubscriptionRulesForSubscription(tx, subscription)
		return tx.Preload("FeedSource").Preload("SubscriptionGroup").First(&subscription, "id = ?", subscription.ID).Error
	})
	return subscription, err
}

func findOrCreateAutoAddFeedSource(db *gorm.DB, target autoSubscriptionTarget, fallbackTitle string) (*model.FeedSource, error) {
	if target.SourceType != "external_rss" {
		return findOrCreateFeedSource(db, target.SourceType, nil, target.RssURL, fallbackTitle, target.Provider)
	}

	provider := firstNonBlank(target.Provider, "rss")
	rssURL := strings.TrimSpace(target.RssURL)
	canonicalURL := normalizeCanonicalFeedURL(firstNonBlank(target.Canonical, rssURL))
	sourceHash := buildFeedSourceHash(target.SourceType, nil, rssURL)

	source, found, err := findReusableAutoAddFeedSource(db, target.SourceType, rssURL, canonicalURL, sourceHash)
	if err != nil {
		return nil, err
	}
	if found {
		if err := updateAutoAddFeedSource(db, source, target, provider, rssURL, canonicalURL, sourceHash, fallbackTitle); err != nil {
			return nil, err
		}
		return source, nil
	}

	source = &model.FeedSource{
		SourceType:      target.SourceType,
		Provider:        provider,
		RssURL:          rssURL,
		CanonicalURL:    canonicalURL,
		SiteURL:         firstNonBlank(target.SiteURL),
		Category:        defaultFeedSourceCategory(target.Category),
		Hash:            sourceHash,
		Title:           firstNonBlank(fallbackTitle, target.Title, rssURL),
		HealthStatus:    "healthy",
		FullTextEnabled: service.DefaultFullTextEnabled(target.SourceType),
	}
	if err := db.Create(source).Error; err != nil {
		if existing, found, lookupErr := findReusableAutoAddFeedSource(db, target.SourceType, rssURL, canonicalURL, sourceHash); lookupErr != nil {
			return nil, lookupErr
		} else if found {
			if updateErr := updateAutoAddFeedSource(db, existing, target, provider, rssURL, canonicalURL, sourceHash, fallbackTitle); updateErr != nil {
				return nil, updateErr
			}
			return existing, nil
		}
		return nil, err
	}
	return source, nil
}

func findReusableAutoAddFeedSource(db *gorm.DB, sourceType, rssURL, canonicalURL, sourceHash string) (*model.FeedSource, bool, error) {
	if canonicalURL != "" {
		var source model.FeedSource
		lookup := db.Where("canonical_url = ?", canonicalURL).Order("created_at ASC").Limit(1).Find(&source)
		if lookup.Error != nil {
			return nil, false, lookup.Error
		}
		if lookup.RowsAffected > 0 {
			return &source, true, nil
		}

		legacyURLs := uniqueNonBlankStrings(rssURL, canonicalURL, canonicalURL+"/")
		if len(legacyURLs) > 0 {
			lookup = db.Where("source_type = ? AND (canonical_url = '' OR canonical_url IS NULL) AND rss_url IN ?", sourceType, legacyURLs).
				Order("created_at ASC").
				Limit(1).
				Find(&source)
			if lookup.Error != nil {
				return nil, false, lookup.Error
			}
			if lookup.RowsAffected > 0 {
				return &source, true, nil
			}
		}
	}

	if sourceHash == "" {
		return nil, false, nil
	}
	var source model.FeedSource
	lookup := db.Where("hash = ?", sourceHash).Order("created_at ASC").Limit(1).Find(&source)
	if lookup.Error != nil {
		return nil, false, lookup.Error
	}
	if lookup.RowsAffected > 0 {
		return &source, true, nil
	}
	return nil, false, nil
}

func updateAutoAddFeedSource(db *gorm.DB, source *model.FeedSource, target autoSubscriptionTarget, provider, rssURL, canonicalURL, sourceHash, fallbackTitle string) error {
	updates := map[string]any{}
	if strings.TrimSpace(source.SourceType) == "" {
		updates["source_type"] = target.SourceType
		source.SourceType = target.SourceType
	}
	if strings.TrimSpace(source.Provider) == "" || (provider == "rsshub" && strings.TrimSpace(source.Provider) == "rss") {
		updates["provider"] = provider
		source.Provider = provider
	}
	if strings.TrimSpace(source.CanonicalURL) == "" && canonicalURL != "" {
		updates["canonical_url"] = canonicalURL
		source.CanonicalURL = canonicalURL
	}
	if strings.TrimSpace(source.RssURL) == "" && rssURL != "" {
		updates["rss_url"] = rssURL
		source.RssURL = rssURL
	}
	if strings.TrimSpace(source.Hash) == "" && sourceHash != "" {
		updates["hash"] = sourceHash
		source.Hash = sourceHash
	}
	if strings.TrimSpace(source.SiteURL) == "" && strings.TrimSpace(target.SiteURL) != "" {
		updates["site_url"] = target.SiteURL
		source.SiteURL = target.SiteURL
	}
	if defaultFeedSourceCategory(source.Category) == "blog" && defaultFeedSourceCategory(target.Category) != "blog" {
		updates["category"] = defaultFeedSourceCategory(target.Category)
		source.Category = defaultFeedSourceCategory(target.Category)
	}
	if strings.TrimSpace(source.Title) == "" {
		title := firstNonBlank(fallbackTitle, target.Title, rssURL)
		if title != "" {
			updates["title"] = title
			source.Title = title
		}
	}
	if len(updates) == 0 {
		return nil
	}
	if err := db.Model(source).Updates(updates).Error; err != nil {
		return err
	}
	return db.Where("id = ?", source.ID).First(source).Error
}

func uniqueNonBlankStrings(values ...string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func isAutoSubscriptionDuplicateSubscriptionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}

	var sqliteErr interface {
		error
		Code() int
	}
	if errors.As(err, &sqliteErr) && sqliteErr.Code() == 2067 {
		return strings.Contains(strings.ToLower(sqliteErr.Error()), "subscriptions.user_id") &&
			strings.Contains(strings.ToLower(sqliteErr.Error()), "subscriptions.feed_source_id")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		constraint := strings.ToLower(pgErr.ConstraintName)
		detail := strings.ToLower(pgErr.Detail)
		return constraint == "idx_subscriptions_user_source" ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "feed_source_id"))
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" {
		constraint := strings.ToLower(pqErr.Constraint)
		detail := strings.ToLower(pqErr.Detail)
		return constraint == "idx_subscriptions_user_source" ||
			(strings.Contains(detail, "user_id") && strings.Contains(detail, "feed_source_id"))
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "idx_subscriptions_user_source") ||
		(strings.Contains(message, "unique constraint failed") &&
			strings.Contains(message, "subscriptions.user_id") &&
			strings.Contains(message, "subscriptions.feed_source_id")) ||
		(strings.Contains(message, "duplicate key") &&
			strings.Contains(message, "user_id") &&
			strings.Contains(message, "feed_source_id"))
}

func autoSubscriptionGroup(tx *gorm.DB, userID uuid.UUID, groupID *uuid.UUID) (*model.SubscriptionGroup, error) {
	if groupID == nil {
		return getOrCreateDefaultSubscriptionGroup(tx, userID)
	}

	var group model.SubscriptionGroup
	lookup := tx.Where("id = ? AND user_id = ?", *groupID, userID).Limit(1).Find(&group)
	if lookup.Error != nil {
		return nil, lookup.Error
	}
	if lookup.RowsAffected == 0 {
		return nil, newAutoSubscriptionHTTPError(http.StatusBadRequest, "Subscription group not found")
	}
	return &group, nil
}

func autoSubscriptionTargetFromSource(source AutoSubscriptionSource, title string) autoSubscriptionTarget {
	rssURL := normalizeCanonicalFeedURL(source.RssURL)
	return autoSubscriptionTarget{
		Provider:   firstNonBlank(source.Provider, "rss"),
		SourceType: firstNonBlank(source.SourceType, "external_rss"),
		Category:   defaultFeedSourceCategory(source.Category),
		Title:      firstNonBlank(title, source.Title, rssURL),
		RssURL:     rssURL,
		SiteURL:    source.SiteURL,
		Canonical:  normalizeCanonicalFeedURL(firstNonBlank(source.CanonicalURL, rssURL)),
	}
}

func validAutoSubscriptionSiteURL(rawInput string) string {
	siteURL := strings.TrimSpace(rawInput)
	if siteURL == "" {
		return ""
	}
	if _, err := parseAutoSubscriptionURL(siteURL); err != nil {
		return ""
	}
	return siteURL
}

func writeAutoSubscriptionError(c *gin.Context, err error) {
	var httpErr autoSubscriptionHTTPError
	if errors.As(err, &httpErr) {
		c.JSON(httpErr.statusCode, gin.H{"error": httpErr.message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to auto-add subscription"})
}

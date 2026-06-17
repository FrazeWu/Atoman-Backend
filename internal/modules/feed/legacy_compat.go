package feed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/service"
)

const defaultSubscriptionGroupName = "默认分组"

var syncFeedSource = func(db *gorm.DB, source model.FeedSource) {
	go service.SyncSingleRSS(db, source)
}

func getOrCreateDefaultSubscriptionGroup(db *gorm.DB, userID uuid.UUID) (*model.SubscriptionGroup, error) {
	var canonical model.SubscriptionGroup

	err := db.Transaction(func(tx *gorm.DB) error {
		var groups []model.SubscriptionGroup
		if err := tx.Where("user_id = ? AND name = ?", userID, defaultSubscriptionGroupName).
			Order("created_at ASC").Find(&groups).Error; err != nil {
			return err
		}

		switch len(groups) {
		case 0:
			canonical = model.SubscriptionGroup{
				UserID: userID,
				Name:   defaultSubscriptionGroupName,
			}
			if err := tx.Create(&canonical).Error; err != nil {
				return err
			}
		case 1:
			canonical = groups[0]
		default:
			canonical = groups[0]
			duplicateIDs := make([]uuid.UUID, 0, len(groups)-1)
			for _, g := range groups[1:] {
				duplicateIDs = append(duplicateIDs, g.ID)
			}

			if err := tx.Model(&model.Subscription{}).
				Where("user_id = ? AND subscription_group_id IN ?", userID, duplicateIDs).
				Update("subscription_group_id", canonical.ID).Error; err != nil {
				return err
			}

			if err := tx.Where("user_id = ? AND id IN ?", userID, duplicateIDs).
				Delete(&model.SubscriptionGroup{}).Error; err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &canonical, nil
}

type SubscriptionInput struct {
	TargetType string     `json:"target_type" binding:"required,oneof=internal_user internal_channel internal_collection external_rss"`
	TargetID   *uuid.UUID `json:"target_id"`
	RssURL     string     `json:"rss_url"`
	Title      string     `json:"title"`
}

type ErrorResponse struct {
	Error string `json:"error" example:"invalid request"`
}

type MessageResponse struct {
	Message string `json:"message" example:"ok"`
}

type SubscriptionResponse struct {
	Data    model.Subscription `json:"data"`
	Message string             `json:"message" example:"ok"`
}

type SubscriptionListResponse struct {
	Data    []model.Subscription `json:"data"`
	Message string               `json:"message" example:"ok"`
}

type TimelineResponse struct {
	Data    []TimelineItem `json:"data"`
	Total   int            `json:"total" example:"20"`
	Page    int            `json:"page" example:"1"`
	Limit   int            `json:"limit" example:"20"`
	Message string         `json:"message" example:"ok"`
}

type FeedStatsResponse struct {
	Data    FeedStatsData `json:"data"`
	Message string        `json:"message" example:"ok"`
}

type SubscriptionGroupResponse struct {
	Data    model.SubscriptionGroup `json:"data"`
	Message string                  `json:"message" example:"ok"`
}

type SubscriptionGroupListResponse struct {
	Data    []model.SubscriptionGroup `json:"data"`
	Message string                    `json:"message" example:"ok"`
}

type OPMLImportResponse struct {
	Message  string `json:"message" example:"OPML import completed"`
	Imported int    `json:"imported" example:"12"`
	Reused   int    `json:"reused" example:"3"`
	Failed   int    `json:"failed" example:"1"`
}

type StarToggleInput struct {
	FeedItemID string `json:"feed_item_id" format:"uuid" binding:"required" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
}

type StarToggleResponse struct {
	Starred bool   `json:"starred" example:"true"`
	Message string `json:"message" example:"Item starred"`
}

type StarredFeedItem struct {
	ID             string `json:"id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	Title          string `json:"title" example:"An interesting post"`
	Link           string `json:"link" example:"https://example.com/post"`
	Summary        string `json:"summary" example:"Summary text"`
	Author         string `json:"author" example:"Fafa"`
	PublishedAt    string `json:"published_at" format:"date-time" example:"2026-05-25T11:00:00Z"`
	SourceTitle    string `json:"source_title" example:"My Feed"`
	SourceSiteURL  string `json:"source_site_url" example:"https://example.com/feed.xml"`
	SourceImageURL string `json:"source_image_url" example:"https://example.com/logo.png"`
}

type StarredItemsResponse struct {
	Items []StarredFeedItem `json:"items"`
	Page  int               `json:"page" example:"1"`
	Total int               `json:"total" example:"20"`
}

type SaveToggleResponse struct {
	Saved bool `json:"saved" example:"true"`
}

type ReadingListResponse struct {
	Items []model.ReadingListItem `json:"items"`
	Page  int                     `json:"page" example:"1"`
	Total int64                   `json:"total" example:"12"`
}

type FeedHealthCheckResponse struct {
	SubscriptionID string `json:"subscription_id" format:"uuid" example:"018f6f6d-b0de-7b8f-bf91-43bc0b8f4c8a"`
	HealthStatus   string `json:"health_status" example:"healthy"`
	ErrorMessage   string `json:"error_message,omitempty" example:""`
	LastChecked    string `json:"last_checked,omitempty" format:"date-time" example:"2026-05-25T11:00:00Z"`
	Skipped        bool   `json:"skipped,omitempty" example:"true"`
	Reason         string `json:"reason,omitempty" example:"internal subscription has no external URL"`
}

type FeedHealthCheckListResponse struct {
	CheckedCount int                       `json:"checked_count" example:"5"`
	Results      []FeedHealthCheckResponse `json:"results"`
}

type FeedItemResponse struct {
	Data FeedItemDetailResponse `json:"data"`
}

type SearchSubscriptionsResponse struct {
	Data  []model.Subscription `json:"data"`
	Count int                  `json:"count" example:"5"`
}

type SubscriptionActionResponse struct {
	Message      string             `json:"message" example:"Subscribed successfully"`
	Subscription model.Subscription `json:"subscription"`
}

type SubscriptionStatusResponse struct {
	Subscribed   bool               `json:"subscribed" example:"true"`
	Subscription model.Subscription `json:"subscription,omitempty"`
}

func normalizeCanonicalFeedURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimRight(trimmed, "/")
	return trimmed
}

func buildFeedSourceHash(targetType string, targetID *uuid.UUID, rssURL string) string {
	var raw string
	if targetType == "external_rss" {
		raw = normalizeCanonicalFeedURL(rssURL)
	} else {
		raw = fmt.Sprintf("%s:%s", targetType, targetID.String())
	}

	h := sha256.New()
	h.Write([]byte(raw))
	return hex.EncodeToString(h.Sum(nil))
}

func BuildFeedSourceHash(targetType string, targetID *uuid.UUID, rssURL string) string {
	return buildFeedSourceHash(targetType, targetID, rssURL)
}

func populateFeedSourceTitle(db *gorm.DB, source *model.FeedSource, fallbackTitle string) {
	if strings.TrimSpace(source.Title) != "" {
		return
	}

	switch source.SourceType {
	case "internal_user":
		if source.SourceID == nil {
			break
		}
		var user model.User
		if err := db.Where("uuid = ?", source.SourceID).First(&user).Error; err == nil {
			source.Title = user.Username
		}
	case "internal_channel":
		if source.SourceID == nil {
			break
		}
		var channel model.Channel
		if err := db.First(&channel, source.SourceID).Error; err == nil {
			source.Title = channel.Name
		}
	case "internal_collection":
		if source.SourceID == nil {
			break
		}
		var collection model.Collection
		if err := db.First(&collection, source.SourceID).Error; err == nil {
			source.Title = collection.Name
		}
	case "external_rss":
		if source.RssURL == "" {
			break
		}
		if _, sourceTitle, _, err := service.FetchAndParseRSS(source.RssURL); err == nil && strings.TrimSpace(sourceTitle) != "" {
			source.Title = sourceTitle
		}
	}

	if strings.TrimSpace(source.Title) == "" {
		source.Title = strings.TrimSpace(fallbackTitle)
	}
}

func findOrCreateFeedSource(db *gorm.DB, targetType string, targetID *uuid.UUID, rssURL, fallbackTitle, providerOverride string) (*model.FeedSource, error) {
	sourceHash := buildFeedSourceHash(targetType, targetID, rssURL)
	trimmedURL := strings.TrimSpace(rssURL)
	canonicalURL := ""
	provider := "internal"
	if targetType == "external_rss" {
		canonicalURL = normalizeCanonicalFeedURL(rssURL)
		provider = "rss"
		if strings.TrimSpace(providerOverride) != "" {
			provider = strings.TrimSpace(providerOverride)
		}
		if canonicalURL != "" {
			var existing model.FeedSource
			canonicalLookup := db.Where("canonical_url = ?", canonicalURL).Limit(1).Find(&existing)
			if canonicalLookup.Error != nil {
				return nil, canonicalLookup.Error
			}
			if canonicalLookup.RowsAffected > 0 {
				updates := map[string]any{}
				if strings.TrimSpace(existing.Provider) == "" || (provider == "rsshub" && strings.TrimSpace(existing.Provider) == "rss") {
					updates["provider"] = provider
					existing.Provider = provider
				}
				if strings.TrimSpace(existing.CanonicalURL) == "" {
					updates["canonical_url"] = canonicalURL
					existing.CanonicalURL = canonicalURL
				}
				if strings.TrimSpace(existing.RssURL) == "" && trimmedURL != "" {
					updates["rss_url"] = trimmedURL
					existing.RssURL = trimmedURL
				}
				if strings.TrimSpace(existing.Hash) == "" {
					updates["hash"] = sourceHash
					existing.Hash = sourceHash
				}
				if strings.TrimSpace(existing.Title) == "" {
					populateFeedSourceTitle(db, &existing, fallbackTitle)
					if strings.TrimSpace(existing.Title) != "" {
						updates["title"] = existing.Title
					}
				}
				if len(updates) > 0 {
					if err := db.Model(&existing).Updates(updates).Error; err != nil {
						return nil, err
					}
					if err := db.Where("id = ?", existing.ID).First(&existing).Error; err != nil {
						return nil, err
					}
				}
				return &existing, nil
			}

			legacyURLs := []string{trimmedURL, canonicalURL}
			if canonicalURL+"/" != "" {
				legacyURLs = append(legacyURLs, canonicalURL+"/")
			}
			legacyLookup := db.Where("source_type = ? AND (canonical_url = '' OR canonical_url IS NULL) AND rss_url IN ?", targetType, legacyURLs).
				Order("created_at ASC").
				Limit(1).
				Find(&existing)
			if legacyLookup.Error != nil {
				return nil, legacyLookup.Error
			}
			if legacyLookup.RowsAffected > 0 {
				updates := map[string]any{
					"provider":      provider,
					"canonical_url": canonicalURL,
					"hash":          sourceHash,
				}
				if trimmedURL != "" {
					updates["rss_url"] = trimmedURL
					existing.RssURL = trimmedURL
				}
				existing.Provider = provider
				existing.CanonicalURL = canonicalURL
				existing.Hash = sourceHash
				if strings.TrimSpace(existing.Title) == "" {
					populateFeedSourceTitle(db, &existing, fallbackTitle)
					if strings.TrimSpace(existing.Title) != "" {
						updates["title"] = existing.Title
					}
				}
				if err := db.Model(&existing).Updates(updates).Error; err != nil {
					return nil, err
				}
				if err := db.Where("id = ?", existing.ID).First(&existing).Error; err != nil {
					return nil, err
				}
				return &existing, nil
			}
		}
	}

	var source model.FeedSource
	hashLookup := db.Where("hash = ?", sourceHash).Limit(1).Find(&source)
	if hashLookup.Error != nil {
		return nil, hashLookup.Error
	}
	if hashLookup.RowsAffected > 0 {
		updates := map[string]any{}
		if strings.TrimSpace(source.SourceType) == "" {
			updates["source_type"] = targetType
		}
		if targetType == "external_rss" {
			if strings.TrimSpace(source.Provider) == "" {
				updates["provider"] = provider
			}
			if strings.TrimSpace(source.CanonicalURL) == "" && canonicalURL != "" {
				updates["canonical_url"] = canonicalURL
			}
			if strings.TrimSpace(source.RssURL) == "" && trimmedURL != "" {
				updates["rss_url"] = trimmedURL
			}
		} else if source.SourceID == nil && targetID != nil {
			updates["source_id"] = *targetID
		}
		if strings.TrimSpace(source.Hash) == "" {
			updates["hash"] = sourceHash
		}
		if strings.TrimSpace(source.Title) == "" {
			populateFeedSourceTitle(db, &source, fallbackTitle)
			if strings.TrimSpace(source.Title) != "" {
				updates["title"] = source.Title
			}
		}
		if len(updates) > 0 {
			if err := db.Model(&source).Updates(updates).Error; err != nil {
				return nil, err
			}
			if err := db.Where("id = ?", source.ID).First(&source).Error; err != nil {
				return nil, err
			}
		}
		return &source, nil
	}

	source = model.FeedSource{
		SourceType:      targetType,
		SourceID:        targetID,
		Provider:        provider,
		RssURL:          trimmedURL,
		CanonicalURL:    canonicalURL,
		SiteURL:         "",
		Hash:            sourceHash,
		HealthStatus:    "healthy",
		FullTextEnabled: service.DefaultFullTextEnabled(targetType),
	}
	populateFeedSourceTitle(db, &source, fallbackTitle)
	if err := db.Create(&source).Error; err != nil {
		var existing model.FeedSource
		if loadErr := db.Where("hash = ?", sourceHash).First(&existing).Error; loadErr == nil {
			return &existing, nil
		}
		if canonicalURL != "" {
			if loadErr := db.Where("canonical_url = ?", canonicalURL).First(&existing).Error; loadErr == nil {
				return &existing, nil
			}
		}
		return nil, err
	}
	return &source, nil
}

func FindOrCreateFeedSource(db *gorm.DB, targetType string, targetID *uuid.UUID, rssURL, fallbackTitle, providerOverride string) (*model.FeedSource, error) {
	return findOrCreateFeedSource(db, targetType, targetID, rssURL, fallbackTitle, providerOverride)
}

// internalRSSPattern 匹配 /api/feed/rss/:username、/api/v1/feed/rss/:username 及对应绝对 URL
var internalRSSPattern = regexp.MustCompile(`(?:^|/)api(?:/v1)?/feed/rss/([^/?#]+)$`)

// resolveInternalRSSURL 检测 URL 是否为站内 RSS 地址，如果是则返回对应用户的 UUID。
func resolveInternalRSSURL(db *gorm.DB, rawURL string) (uuid.UUID, error) {
	m := internalRSSPattern.FindStringSubmatch(rawURL)
	if len(m) < 2 {
		return uuid.UUID{}, fmt.Errorf("not an internal RSS URL")
	}
	username := m[1]
	var user model.User
	if err := db.Where("username = ?", username).First(&user).Error; err != nil {
		return uuid.UUID{}, err
	}
	return user.UUID, nil
}

func ResolveInternalRSSURL(db *gorm.DB, rawURL string) (uuid.UUID, error) {
	return resolveInternalRSSURL(db, rawURL)
}

// CreateSubscription godoc
// @Summary 创建订阅
// @Description 创建站内或外部 RSS 订阅。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body SubscriptionInput true "订阅输入"
// @Success 201 {object} SubscriptionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions [post]
func CreateSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input SubscriptionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// 检测内部 RSS URL（/api/feed/rss/:username），自动转换为 internal_user 类型
		if input.TargetType == "external_rss" && input.RssURL != "" {
			if userID, err := resolveInternalRSSURL(db, input.RssURL); err == nil {
				input.TargetType = "internal_user"
				input.TargetID = &userID
				input.RssURL = ""
			}
		}

		if input.TargetType != "external_rss" && input.TargetID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "target_id is required for internal subscriptions"})
			return
		}
		if input.TargetType == "external_rss" && input.RssURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "rss_url is required for external subscriptions"})
			return
		}
		if input.TargetType == "external_rss" {
			if u, err := url.ParseRequestURI(input.RssURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "rss_url must be an absolute http/https URL"})
				return
			}
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var subscriptionGroupID *uuid.UUID
		if input.TargetType == "external_rss" || input.TargetType == "internal_user" {
			defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
				return
			}
			subscriptionGroupID = &defaultGroup.ID
		}

		provider := ""
		if input.TargetType == "external_rss" {
			if override, ok := c.Get("provider_override"); ok {
				if value, ok := override.(string); ok {
					provider = strings.TrimSpace(value)
				}
			}
		}

		source, err := findOrCreateFeedSource(db, input.TargetType, input.TargetID, input.RssURL, input.Title, provider)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
			return
		}

		var existingSub model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, source.ID).First(&existingSub).Error; err == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Already subscribed to this source"})
			return
		}

		subscription := model.Subscription{
			UserID:              userID,
			FeedSourceID:        source.ID,
			Title:               input.Title,
			SubscriptionGroupID: subscriptionGroupID,
		}

		if err := db.Create(&subscription).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
			return
		}

		if input.TargetType == "external_rss" {
			syncFeedSource(db, *source)
		}

		c.JSON(http.StatusCreated, gin.H{"data": subscription, "message": "ok"})
	}
}

func DiscoverFeedCandidates() gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			URL string `json:"url"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid discover request body"})
			return
		}

		rawURL := strings.TrimSpace(input.URL)
		u, err := url.ParseRequestURI(rawURL)
		if err != nil || u == nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "url must be an absolute http/https URL"})
			return
		}

		if isLikelyDirectFeedURL(u) {
			c.JSON(http.StatusOK, gin.H{"candidates": []service.FeedDiscoveryCandidate{
				{
					Title:     rawURL,
					FeedURL:   rawURL,
					SiteURL:   rawURL,
					Kind:      "direct",
					Score:     100,
					Reason:    "direct feed URL",
					IsDefault: true,
				},
			}})
			return
		}

		c.JSON(http.StatusOK, gin.H{"candidates": []service.FeedDiscoveryCandidate{}})
	}
}

func isLikelyDirectFeedURL(u *url.URL) bool {
	path := strings.ToLower(strings.TrimSpace(u.Path))
	if path == "" {
		return false
	}
	return strings.HasSuffix(path, ".xml") ||
		strings.HasSuffix(path, ".rss") ||
		strings.HasSuffix(path, ".atom") ||
		strings.HasSuffix(path, "/rss") ||
		strings.HasSuffix(path, "/atom") ||
		strings.HasSuffix(path, "/feed")
}

func CreateSubscriptionFromProvider(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			Provider    string            `json:"provider" binding:"required"`
			TemplateKey string            `json:"template_key" binding:"required"`
			Params      map[string]string `json:"params"`
			Title       string            `json:"title"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		if strings.TrimSpace(input.Provider) != "rsshub" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported provider"})
			return
		}

		feedURL, err := service.BuildRSSHubFeedURL(input.TemplateKey, input.Params)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		c.Set("provider_override", "rsshub")
		c.Request.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"target_type":"external_rss","rss_url":%q,"title":%q}`, feedURL, input.Title)))
		CreateSubscription(db)(c)
	}
}

// DeleteSubscription godoc
// @Summary 删除订阅
// @Description 删除当前用户的一个订阅。
// @Tags feed
// @Produce json
// @Param id path string true "订阅 UUID"
// @Success 200 {object} MessageResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id} [delete]
func DeleteSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.Subscription{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete subscription"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// UpdateSubscription godoc
// @Summary 更新订阅
// @Description 更新当前用户订阅的显示标题或分组。
// @Tags feed
// @Accept json
// @Produce json
// @Param id path string true "订阅 UUID"
// @Param input body object true "订阅更新输入"
// @Success 200 {object} object
// @Failure 400 {object} object
// @Failure 404 {object} object
// @Failure 500 {object} object
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id} [put]
func UpdateSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid subscription id"})
			return
		}

		var input struct {
			Title   *string    `json:"title"`
			GroupID *uuid.UUID `json:"group_id"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		var sub model.Subscription
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&sub).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
			return
		}

		updates := map[string]interface{}{}
		if input.Title != nil {
			updates["title"] = strings.TrimSpace(*input.Title)
		}
		if input.GroupID != nil {
			var group model.SubscriptionGroup
			if err := db.Where("id = ? AND user_id = ?", *input.GroupID, userID).First(&group).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription group not found"})
				return
			}
			updates["subscription_group_id"] = *input.GroupID
		}
		if len(updates) > 0 {
			if err := db.Model(&sub).Updates(updates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscription"})
				return
			}
		}
		if err := db.Preload("FeedSource").Preload("SubscriptionGroup").First(&sub, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload subscription"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": sub, "message": "ok"})
	}
}

// GetSubscriptions godoc
// @Summary 获取订阅列表
// @Description 返回当前用户的订阅列表。
// @Tags feed
// @Produce json
// @Success 200 {object} SubscriptionListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions [get]
func GetSubscriptions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
			return
		}

		// Keep old data compatible: migrate NULL group subscriptions to default group.
		if err := db.Model(&model.Subscription{}).
			Where("user_id = ? AND subscription_group_id IS NULL", userID).
			Update("subscription_group_id", defaultGroup.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to normalize subscriptions"})
			return
		}

		var subscriptions []model.Subscription
		if err := db.Preload("FeedSource").Preload("SubscriptionGroup").Where("user_id = ?", userID).Find(&subscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscriptions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": subscriptions, "message": "ok"})
	}
}

type TimelineItem struct {
	Type        string          `json:"type"`
	Post        *model.Post     `json:"post,omitempty"`
	FeedItem    *model.FeedItem `json:"feed_item,omitempty"`
	PublishedAt time.Time       `json:"published_at"`
	IsRead      bool            `json:"is_read"`
}

type FeedStatsPoint struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type FeedSourceStat struct {
	FeedSourceID uuid.UUID `json:"feed_source_id"`
	Title        string    `json:"title"`
	Count        int       `json:"count"`
}

type FeedStatsData struct {
	Period          string           `json:"period"`
	TotalRead       int              `json:"total_read"`
	Points          []FeedStatsPoint `json:"points"`
	SourceBreakdown []FeedSourceStat `json:"source_breakdown"`
}

type FeedItemDetailResponse struct {
	ID            uuid.UUID         `json:"id"`
	Title         string            `json:"title"`
	Summary       string            `json:"summary"`
	Link          string            `json:"link"`
	Author        string            `json:"author"`
	PublishedAt   time.Time         `json:"published_at"`
	ImageURL      string            `json:"image_url"`
	EnclosureURL  string            `json:"enclosure_url"`
	EnclosureType string            `json:"enclosure_type"`
	Duration      string            `json:"duration"`
	ContentHTML   string            `json:"content_html"`
	ContentSource string            `json:"content_source"`
	FeedSource    *model.FeedSource `json:"feed_source,omitempty"`
	FeedItem      *model.FeedItem   `json:"feed_item,omitempty"`
}

type feedReadEvent struct {
	ReadAt       time.Time `gorm:"column:read_at"`
	FeedSourceID uuid.UUID `gorm:"column:feed_source_id"`
	SourceTitle  string    `gorm:"column:source_title"`
}

// GetTimeline godoc
// @Summary 获取订阅时间线
// @Description 聚合博客文章与外部 RSS 条目时间线，支持分页和筛选。
// @Tags feed
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Param source_type query string false "源类型"
// @Param source_id query string false "订阅 UUID"
// @Param group_id query string false "分组 UUID"
// @Param is_read query string false "是否已读" Enums(true,false)
// @Param hide_duplicates query bool false "隐藏重复项"
// @Success 200 {object} TimelineResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline [get]
func GetTimeline(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if limit > 100 {
			limit = 100
		}
		offset := (page - 1) * limit

		sourceType := c.Query("source_type")
		sourceID := c.Query("source_id")
		groupID := c.Query("group_id")
		isReadFilter := c.Query("is_read") // "true", "false", or "" for all
		unreadOnly := strings.EqualFold(c.Query("unread_only"), "true")
		hideDuplicates := c.Query("hide_duplicates") == "true"

		var userSubscriptions []model.Subscription
		query := db.Where("subscriptions.user_id = ?", userID)

		if sourceType != "" {
			query = query.Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
				Where("feed_sources.source_type = ?", sourceType)
		}
		if sourceID != "" {
			query = query.Where("subscriptions.id = ?", sourceID)
		}
		if groupID != "" {
			query = query.Where("subscriptions.subscription_group_id = ?", groupID)
		}

		if err := query.Preload("FeedSource").Find(&userSubscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscriptions"})
			return
		}

		if len(userSubscriptions) == 0 {
			c.JSON(http.StatusOK, gin.H{"data": []TimelineItem{}, "total": 0, "page": page, "limit": limit, "message": "ok"})
			return
		}

		var userIDs []uuid.UUID
		var channelIDs []uuid.UUID
		var collectionIDs []uuid.UUID
		var feedSourceIDs []uuid.UUID

		for _, sub := range userSubscriptions {
			fs := sub.FeedSource
			if fs == nil {
				continue
			}
			switch fs.SourceType {
			case "internal_user":
				if fs.SourceID != nil {
					userIDs = append(userIDs, *fs.SourceID)
				}
			case "internal_channel":
				if fs.SourceID != nil {
					channelIDs = append(channelIDs, *fs.SourceID)
				}
			case "internal_collection":
				if fs.SourceID != nil {
					collectionIDs = append(collectionIDs, *fs.SourceID)
				}
			case "external_rss":
				feedSourceIDs = append(feedSourceIDs, fs.ID)
			}
		}

		var posts []model.Post
		var orConditions []string
		var orArgs []interface{}

		if len(userIDs) > 0 {
			orConditions = append(orConditions, "user_id IN ?")
			orArgs = append(orArgs, userIDs)
		}

		if len(channelIDs) > 0 {
			orConditions = append(orConditions, "channel_id IN ?")
			orArgs = append(orArgs, channelIDs)

			var channelCollections []model.Collection
			db.Where("channel_id IN ?", channelIDs).Find(&channelCollections)
			for _, col := range channelCollections {
				collectionIDs = append(collectionIDs, col.ID)
			}
		}

		if len(collectionIDs) > 0 {
			var postCollections []model.PostCollection
			db.Where("collection_id IN ?", collectionIDs).Find(&postCollections)
			var postIDs []uuid.UUID
			for _, pc := range postCollections {
				postIDs = append(postIDs, pc.PostID)
			}
			if len(postIDs) > 0 {
				orConditions = append(orConditions, "id IN ?")
				orArgs = append(orArgs, postIDs)
			}
		}

		if len(orConditions) > 0 {
			combined := "(" + strings.Join(orConditions, " OR ") + ")"
			db.Preload("User").Where("status = ?", "published").Where(combined, orArgs...).Find(&posts)
		}

		var feedItems []model.FeedItem
		if len(feedSourceIDs) > 0 {
			db.Preload("FeedSource").
				Joins("JOIN feed_sources ON feed_sources.id = feed_items.feed_source_id").
				Where("feed_items.feed_source_id IN ?", feedSourceIDs).
				Where("feed_sources.hidden = ?", false).
				Order("feed_items.published_at DESC").
				Find(&feedItems)
			service.AnnotateDuplicateFeedItems(feedItems)
		}

		var readFeedItemIDs map[uuid.UUID]bool
		if len(feedItems) > 0 {
			var feedItemIDs []uuid.UUID
			for _, fi := range feedItems {
				feedItemIDs = append(feedItemIDs, fi.ID)
			}
			var reads []model.FeedItemRead
			db.Where("user_id = ? AND feed_item_id IN ?", userID, feedItemIDs).Find(&reads)
			readFeedItemIDs = make(map[uuid.UUID]bool, len(reads))
			for _, r := range reads {
				readFeedItemIDs[r.FeedItemID] = true
			}
		}

		var timeline []TimelineItem

		for i := range posts {
			timeline = append(timeline, TimelineItem{
				Type:        "post",
				Post:        &posts[i],
				PublishedAt: posts[i].CreatedAt,
				IsRead:      false,
			})
		}

		for i := range feedItems {
			timeline = append(timeline, TimelineItem{
				Type:        "feed_item",
				FeedItem:    &feedItems[i],
				PublishedAt: feedItems[i].PublishedAt,
				IsRead:      readFeedItemIDs[feedItems[i].ID],
			})
		}

		sort.Slice(timeline, func(i, j int) bool {
			return timeline[i].PublishedAt.After(timeline[j].PublishedAt)
		})

		if isReadFilter == "true" || isReadFilter == "false" {
			want := isReadFilter == "true"
			filtered := timeline[:0]
			for _, item := range timeline {
				if item.IsRead == want {
					filtered = append(filtered, item)
				}
			}
			timeline = filtered
		}

		if unreadOnly {
			filtered := timeline[:0]
			for _, item := range timeline {
				if !item.IsRead {
					filtered = append(filtered, item)
				}
			}
			timeline = filtered
		}

		if hideDuplicates {
			filtered := timeline[:0]
			for _, item := range timeline {
				if item.Type == "feed_item" && item.FeedItem != nil && item.FeedItem.IsDuplicate {
					continue
				}
				filtered = append(filtered, item)
			}
			timeline = filtered
		}

		total := len(timeline)
		start := offset
		if start > total {
			start = total
		}
		end := start + limit
		if end > total {
			end = total
		}
		paged := timeline[start:end]

		c.JSON(http.StatusOK, gin.H{
			"data":    paged,
			"total":   total,
			"page":    page,
			"limit":   limit,
			"message": "ok",
		})
	}
}

// GetFeedStats godoc
// @Summary 获取订阅统计
// @Description 返回阅读统计数据和来源分布。
// @Tags feed
// @Produce json
// @Param period query string false "统计周期" Enums(day,week,month)
// @Success 200 {object} FeedStatsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/stats [get]
func GetFeedStats(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		period := strings.ToLower(c.DefaultQuery("period", "day"))

		points, pointIndex, rangeStart, err := buildFeedStatsBuckets(period, time.Now())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid period"})
			return
		}

		var events []feedReadEvent
		if err := db.Table("feed_item_reads AS fir").
			Select("fir.read_at, fi.feed_source_id, COALESCE(NULLIF(subscriptions.title, ''), fs.title, '未命名源') AS source_title").
			Joins("JOIN feed_items fi ON fi.id = fir.feed_item_id").
			Joins("LEFT JOIN feed_sources fs ON fs.id = fi.feed_source_id").
			Joins("LEFT JOIN subscriptions ON subscriptions.feed_source_id = fi.feed_source_id AND subscriptions.user_id = ?", userID).
			Where("fir.user_id = ? AND fir.read_at >= ?", userID, rangeStart).
			Order("fir.read_at ASC").
			Scan(&events).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed stats"})
			return
		}

		totalRead := 0
		sourceCounts := make(map[uuid.UUID]*FeedSourceStat)

		for _, event := range events {
			bucketKey := feedStatsBucketKey(period, event.ReadAt)
			if pointPos, ok := pointIndex[bucketKey]; ok {
				points[pointPos].Count++
				totalRead++
			}

			stat, ok := sourceCounts[event.FeedSourceID]
			if !ok {
				stat = &FeedSourceStat{
					FeedSourceID: event.FeedSourceID,
					Title:        event.SourceTitle,
				}
				sourceCounts[event.FeedSourceID] = stat
			}
			stat.Count++
		}

		sourceBreakdown := make([]FeedSourceStat, 0, len(sourceCounts))
		for _, stat := range sourceCounts {
			sourceBreakdown = append(sourceBreakdown, *stat)
		}

		sort.Slice(sourceBreakdown, func(i, j int) bool {
			if sourceBreakdown[i].Count == sourceBreakdown[j].Count {
				return sourceBreakdown[i].Title < sourceBreakdown[j].Title
			}
			return sourceBreakdown[i].Count > sourceBreakdown[j].Count
		})

		if len(sourceBreakdown) > 10 {
			sourceBreakdown = sourceBreakdown[:10]
		}

		c.JSON(http.StatusOK, gin.H{
			"data": FeedStatsData{
				Period:          period,
				TotalRead:       totalRead,
				Points:          points,
				SourceBreakdown: sourceBreakdown,
			},
			"message": "ok",
		})
	}
}

func buildFeedStatsBuckets(period string, now time.Time) ([]FeedStatsPoint, map[string]int, time.Time, error) {
	now = now.Local()

	switch period {
	case "day":
		const bucketCount = 7
		start := startOfDay(now).AddDate(0, 0, -(bucketCount - 1))
		points, pointIndex, rangeStart := feedStatsPoints(bucketCount, start, func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }, func(t time.Time) string {
			return t.Format("01-02")
		}, func(t time.Time) string {
			return startOfDay(t).Format("2006-01-02")
		})
		return points, pointIndex, rangeStart, nil
	case "week":
		const bucketCount = 8
		start := startOfISOWeek(now).AddDate(0, 0, -7*(bucketCount-1))
		points, pointIndex, rangeStart := feedStatsPoints(bucketCount, start, func(t time.Time) time.Time { return t.AddDate(0, 0, 7) }, func(t time.Time) string {
			return t.Format("01-02")
		}, func(t time.Time) string {
			return startOfISOWeek(t).Format("2006-01-02")
		})
		return points, pointIndex, rangeStart, nil
	case "month":
		const bucketCount = 6
		start := startOfMonth(now).AddDate(0, -(bucketCount - 1), 0)
		points, pointIndex, rangeStart := feedStatsPoints(bucketCount, start, func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }, func(t time.Time) string {
			return t.Format("2006-01")
		}, func(t time.Time) string {
			return startOfMonth(t).Format("2006-01")
		})
		return points, pointIndex, rangeStart, nil
	default:
		return nil, nil, time.Time{}, fmt.Errorf("unsupported period: %s", period)
	}
}

func feedStatsPoints(
	bucketCount int,
	start time.Time,
	next func(time.Time) time.Time,
	labelFor func(time.Time) string,
	keyFor func(time.Time) string,
) ([]FeedStatsPoint, map[string]int, time.Time) {
	points := make([]FeedStatsPoint, 0, bucketCount)
	pointIndex := make(map[string]int, bucketCount)
	current := start

	for i := 0; i < bucketCount; i++ {
		key := keyFor(current)
		pointIndex[key] = i
		points = append(points, FeedStatsPoint{Label: labelFor(current), Count: 0})
		current = next(current)
	}

	return points, pointIndex, start
}

func feedStatsBucketKey(period string, t time.Time) string {
	switch period {
	case "week":
		return startOfISOWeek(t).Format("2006-01-02")
	case "month":
		return startOfMonth(t).Format("2006-01")
	default:
		return startOfDay(t).Format("2006-01-02")
	}
}

func startOfDay(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

func startOfISOWeek(t time.Time) time.Time {
	start := startOfDay(t)
	weekday := int(start.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	return start.AddDate(0, 0, -(weekday - 1))
}

func startOfMonth(t time.Time) time.Time {
	year, month, _ := t.Date()
	return time.Date(year, month, 1, 0, 0, 0, 0, t.Location())
}

type MarkReadInput struct {
	FeedItemIDs []uuid.UUID `json:"feed_item_ids" binding:"required"`
}

// MarkItemsRead godoc
// @Summary 标记条目已读
// @Description 批量将指定 feed item 标记为已读。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body MarkReadInput true "已读输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline/mark-read [post]
func MarkItemsRead(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input MarkReadInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		now := time.Now()

		for _, itemID := range input.FeedItemIDs {
			read := model.FeedItemRead{
				UserID:     userID,
				FeedItemID: itemID,
				ReadAt:     now,
			}
			db.Where("user_id = ? AND feed_item_id = ?", userID, itemID).
				FirstOrCreate(&read)
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// MarkAllRead godoc
// @Summary 全部标记已读
// @Description 将当前用户外部 RSS 条目全部标记为已读。
// @Tags feed
// @Produce json
// @Success 200 {object} MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline/mark-all-read [post]
func MarkAllRead(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var userSubscriptions []model.Subscription
		db.Where("user_id = ?", userID).Preload("FeedSource").Find(&userSubscriptions)

		var feedSourceIDs []uuid.UUID
		for _, sub := range userSubscriptions {
			if sub.FeedSource != nil && sub.FeedSource.SourceType == "external_rss" {
				feedSourceIDs = append(feedSourceIDs, sub.FeedSource.ID)
			}
		}

		if len(feedSourceIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{"message": "ok"})
			return
		}

		var unreadItems []model.FeedItem
		db.Where("feed_source_id IN ?", feedSourceIDs).Find(&unreadItems)

		now := time.Now()
		for _, item := range unreadItems {
			read := model.FeedItemRead{
				UserID:     userID,
				FeedItemID: item.ID,
				ReadAt:     now,
			}
			db.Where("user_id = ? AND feed_item_id = ?", userID, item.ID).
				FirstOrCreate(&read)
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// MarkAllUnread godoc
// @Summary 全部标记未读
// @Description 将当前用户外部 RSS 条目全部标记为未读。
// @Tags feed
// @Produce json
// @Success 200 {object} MessageResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline/mark-all-unread [post]
func MarkAllUnread(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var userSubscriptions []model.Subscription
		db.Where("user_id = ?", userID).Preload("FeedSource").Find(&userSubscriptions)

		var feedSourceIDs []uuid.UUID
		for _, sub := range userSubscriptions {
			if sub.FeedSource != nil && sub.FeedSource.SourceType == "external_rss" {
				feedSourceIDs = append(feedSourceIDs, sub.FeedSource.ID)
			}
		}

		if len(feedSourceIDs) == 0 {
			c.JSON(http.StatusOK, gin.H{"message": "ok"})
			return
		}

		var feedItems []model.FeedItem
		db.Where("feed_source_id IN ?", feedSourceIDs).Find(&feedItems)

		var itemIDs []uuid.UUID
		for _, item := range feedItems {
			itemIDs = append(itemIDs, item.ID)
		}

		if len(itemIDs) > 0 {
			db.Where("user_id = ? AND feed_item_id IN ?", userID, itemIDs).Delete(&model.FeedItemRead{})
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// GetSubscriptionGroups godoc
// @Summary 获取订阅分组列表
// @Description 返回当前用户的订阅分组。
// @Tags feed
// @Produce json
// @Success 200 {object} SubscriptionGroupListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups [get]
func GetSubscriptionGroups(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if _, err := getOrCreateDefaultSubscriptionGroup(db, userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
			return
		}

		var groups []model.SubscriptionGroup
		if err := db.Where("user_id = ?", userID).Find(&groups).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch groups"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": groups, "message": "ok"})
	}
}

type GroupInput struct {
	Name string `json:"name" binding:"required"`
}

// CreateSubscriptionGroup godoc
// @Summary 创建订阅分组
// @Description 为当前用户创建一个订阅分组。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body GroupInput true "分组输入"
// @Success 201 {object} SubscriptionGroupResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups [post]
func CreateSubscriptionGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input GroupInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group name is required"})
			return
		}

		if name == defaultSubscriptionGroupName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Default group already exists"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var existing model.SubscriptionGroup
		if err := db.Where("user_id = ? AND name = ?", userID, name).First(&existing).Error; err == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group name already exists"})
			return
		} else if err != gorm.ErrRecordNotFound {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate group name"})
			return
		}

		group := model.SubscriptionGroup{
			UserID: userID,
			Name:   name,
		}

		if err := db.Create(&group).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create group"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": group, "message": "ok"})
	}
}

// UpdateSubscriptionGroup godoc
// @Summary 更新订阅分组
// @Description 重命名当前用户的订阅分组。
// @Tags feed
// @Accept json
// @Produce json
// @Param id path string true "分组 UUID"
// @Param input body GroupInput true "分组输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups/{id} [put]
func UpdateSubscriptionGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input GroupInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group name is required"})
			return
		}

		var target model.SubscriptionGroup
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&target).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
			return
		}

		if target.Name == defaultSubscriptionGroupName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Default group cannot be renamed"})
			return
		}

		if name == defaultSubscriptionGroupName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Default group name is reserved"})
			return
		}

		var existing model.SubscriptionGroup
		if err := db.Where("user_id = ? AND name = ? AND id <> ?", userID, name, id).First(&existing).Error; err == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group name already exists"})
			return
		} else if err != gorm.ErrRecordNotFound {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate group name"})
			return
		}

		if err := db.Model(&model.SubscriptionGroup{}).Where("id = ? AND user_id = ?", id, userID).Update("name", name).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update group"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// DeleteSubscriptionGroup godoc
// @Summary 删除订阅分组
// @Description 删除分组并将其中订阅迁回默认分组。
// @Tags feed
// @Produce json
// @Param id path string true "分组 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/groups/{id} [delete]
func DeleteSubscriptionGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var targetGroup model.SubscriptionGroup
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&targetGroup).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
			return
		}

		if targetGroup.Name == defaultSubscriptionGroupName {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Default group cannot be deleted"})
			return
		}

		defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
			return
		}

		db.Model(&model.Subscription{}).
			Where("subscription_group_id = ? AND user_id = ?", id, userID).
			Update("subscription_group_id", defaultGroup.ID)

		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.SubscriptionGroup{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete group"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

type SetGroupInput struct {
	GroupID *uuid.UUID `json:"group_id"`
}

// SetSubscriptionGroup godoc
// @Summary 设置订阅分组
// @Description 为某个订阅设置所属分组。
// @Tags feed
// @Accept json
// @Produce json
// @Param id path string true "订阅 UUID"
// @Param input body SetGroupInput true "分组设置输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id}/group [put]
func SetSubscriptionGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		subID := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input SetGroupInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var targetGroupID *uuid.UUID
		if input.GroupID == nil {
			defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare default group"})
				return
			}
			targetGroupID = &defaultGroup.ID
		} else {
			var group model.SubscriptionGroup
			if err := db.Where("id = ? AND user_id = ?", *input.GroupID, userID).First(&group).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription group not found"})
				return
			}
			targetGroupID = input.GroupID
		}

		if err := db.Model(&model.Subscription{}).
			Where("id = ? AND user_id = ?", subID, userID).
			Update("subscription_group_id", targetGroupID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update subscription group"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

type RSS struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel RSSChannel `xml:"channel"`
}

type RSSChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []RSSItem `xml:"item"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
}

// GetUserRSS godoc
// @Summary 获取用户博客 RSS
// @Description 输出指定用户的博客 RSS。
// @Tags feed
// @Produce application/rss+xml
// @Param username path string true "用户名"
// @Success 200 {string} string "RSS XML"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/feed/rss/{username} [get]
func GetUserRSS(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		username := c.Param("username")

		var user model.User
		if err := db.Where("username = ?", username).First(&user).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}

		var posts []model.Post
		if err := db.Where("user_id = ? AND status = ?", user.UUID, "published").Order("created_at DESC").Limit(50).Find(&posts).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch posts"})
			return
		}

		baseURL := os.Getenv("BASE_URL")
		if baseURL == "" {
			baseURL = "http://localhost:8080"
		}

		rss := RSS{
			Version: "2.0",
			Channel: RSSChannel{
				Title:       user.DisplayName + " 的博客 - Atoman",
				Link:        baseURL + "/users/" + user.Username,
				Description: user.DisplayName + " 的博客订阅",
				Items:       []RSSItem{},
			},
		}

		for _, post := range posts {
			itemURL := baseURL + "/blog/posts/" + post.ID.String()
			rss.Channel.Items = append(rss.Channel.Items, RSSItem{
				Title:       post.Title,
				Link:        itemURL,
				Description: post.Summary,
				PubDate:     post.CreatedAt.Format(time.RFC1123Z),
				GUID:        itemURL,
			})
		}

		c.Header("Content-Type", "application/rss+xml")
		c.XML(http.StatusOK, rss)
	}
}

type OPML struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr,omitempty"`
	Head    OPMLHead `xml:"head"`
	Body    OPMLBody `xml:"body"`
}

type OPMLHead struct {
	Title string `xml:"title,omitempty"`
}

type OPMLBody struct {
	Outlines []OPMLOutline `xml:"outline"`
}

type OPMLOutline struct {
	Text     string        `xml:"text,attr,omitempty"`
	Title    string        `xml:"title,attr,omitempty"`
	Type     string        `xml:"type,attr,omitempty"`
	XMLURL   string        `xml:"xmlUrl,attr,omitempty"`
	Outlines []OPMLOutline `xml:"outline,omitempty"`
}

type importedFeedSourceResult struct {
	Source   *model.FeedSource
	Imported bool
}

func importFeedSourceFromURL(db *gorm.DB, title, xmlURL string) (importedFeedSourceResult, error) {
	trimmedURL := strings.TrimSpace(xmlURL)
	u, err := url.ParseRequestURI(trimmedURL)
	if err != nil || u == nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return importedFeedSourceResult{}, fmt.Errorf("feed url must be an absolute http/https URL")
	}
	sourceHash := buildFeedSourceHash("external_rss", nil, trimmedURL)
	canonicalURL := normalizeCanonicalFeedURL(trimmedURL)

	var existing model.FeedSource
	if err := db.Where("hash = ?", sourceHash).First(&existing).Error; err == nil {
		return importedFeedSourceResult{Source: &existing, Imported: false}, nil
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return importedFeedSourceResult{}, err
	}

	if canonicalURL != "" {
		if err := db.Where("canonical_url = ?", canonicalURL).First(&existing).Error; err == nil {
			feedSource, err := findOrCreateFeedSource(db, "external_rss", nil, trimmedURL, title, "")
			if err != nil {
				return importedFeedSourceResult{}, err
			}
			return importedFeedSourceResult{Source: feedSource, Imported: false}, nil
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return importedFeedSourceResult{}, err
		}
	}

	legacyURLs := []string{}
	for _, candidate := range []string{trimmedURL, canonicalURL, canonicalURL + "/"} {
		if candidate == "" {
			continue
		}
		duplicate := false
		for _, existingURL := range legacyURLs {
			if existingURL == candidate {
				duplicate = true
				break
			}
		}
		if !duplicate {
			legacyURLs = append(legacyURLs, candidate)
		}
	}
	if len(legacyURLs) > 0 {
		if err := db.Where("source_type = ? AND (canonical_url = '' OR canonical_url IS NULL) AND rss_url IN ?", "external_rss", legacyURLs).
			Order("created_at ASC").
			First(&existing).Error; err == nil {
			feedSource, err := findOrCreateFeedSource(db, "external_rss", nil, trimmedURL, title, "")
			if err != nil {
				return importedFeedSourceResult{}, err
			}
			return importedFeedSourceResult{Source: feedSource, Imported: false}, nil
		} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return importedFeedSourceResult{}, err
		}
	}

	feedSource, err := findOrCreateFeedSource(db, "external_rss", nil, trimmedURL, title, "")
	if err != nil {
		return importedFeedSourceResult{}, err
	}

	return importedFeedSourceResult{Source: feedSource, Imported: true}, nil
}

func importFeedFromURL(db *gorm.DB, userID uuid.UUID, title, xmlURL string) error {
	defaultGroup, err := getOrCreateDefaultSubscriptionGroup(db, userID)
	if err != nil {
		return err
	}

	result, err := importFeedSourceFromURL(db, title, xmlURL)
	if err != nil {
		return err
	}
	feedSource := result.Source

	var existingSub model.Subscription
	if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&existingSub).Error; err == nil {
		return nil
	}

	subscription := model.Subscription{
		UserID:              userID,
		FeedSourceID:        feedSource.ID,
		Title:               title,
		SubscriptionGroupID: &defaultGroup.ID,
	}

	if err := db.Create(&subscription).Error; err != nil {
		return err
	}

	go service.SyncSingleRSS(db, *feedSource)
	return nil
}

// ImportOPML godoc
// @Summary 导入 OPML
// @Description 上传 OPML 文件并批量导入 RSS 订阅。
// @Tags feed
// @Accept mpfd
// @Produce json
// @Param file formData file true "OPML 文件"
// @Success 200 {object} OPMLImportResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/opml/import [post]
func ImportOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get uploaded file"})
			return
		}
		defer file.Close()

		if header.Size > 10<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 10MB limit"})
			return
		}

		data := make([]byte, header.Size)
		if _, err := file.Read(data); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file"})
			return
		}

		var opml OPML
		if err := xml.Unmarshal(data, &opml); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid OPML format"})
			return
		}

		imported := 0
		failed := 0

		for _, outline := range opml.Body.Outlines {
			if outline.XMLURL != "" {
				if err := importFeedFromURL(db, userID, outline.Text, outline.XMLURL); err != nil {
					failed++
				} else {
					imported++
				}
			}

			for _, subOutline := range outline.Outlines {
				if subOutline.XMLURL != "" {
					if err := importFeedFromURL(db, userID, subOutline.Text, subOutline.XMLURL); err != nil {
						failed++
					} else {
						imported++
					}
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"message":  "OPML import completed",
			"imported": imported,
			"failed":   failed,
		})
	}
}

// ImportGlobalOPML godoc
// @Summary 导入全局 OPML 订阅源
// @Description 管理员上传 OPML 文件，批量创建或复用全局 RSS 源，不创建用户订阅。
// @Tags feed
// @Accept mpfd
// @Produce json
// @Param file formData file true "OPML 文件"
// @Success 200 {object} OPMLImportResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/sources/opml/import [post]
func ImportGlobalOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get uploaded file"})
			return
		}
		defer file.Close()

		if header.Size > 10<<20 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 10MB limit"})
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read file"})
			return
		}

		var opml OPML
		if err := xml.Unmarshal(data, &opml); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid OPML format"})
			return
		}

		imported := 0
		reused := 0
		failed := 0

		importOutline := func(outline OPMLOutline) {
			if strings.TrimSpace(outline.XMLURL) == "" {
				return
			}
			result, err := importFeedSourceFromURL(db, strings.TrimSpace(outline.Text), strings.TrimSpace(outline.XMLURL))
			if err != nil {
				failed++
				return
			}
			if result.Imported {
				imported++
			} else {
				reused++
			}
			if result.Source != nil {
				go syncFeedSource(db, *result.Source)
			}
		}

		for _, outline := range opml.Body.Outlines {
			importOutline(outline)
			for _, subOutline := range outline.Outlines {
				importOutline(subOutline)
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"message":  "OPML import completed",
			"imported": imported,
			"reused":   reused,
			"failed":   failed,
		})
	}
}

// ExportGlobalOPML godoc
// @Summary 导出全局 OPML 订阅源
// @Description 管理员导出全站 external_rss 订阅源为 OPML 文件，不包含用户订阅关系。
// @Tags feed
// @Produce application/x-opml+xml
// @Success 200 {string} string "OPML XML"
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/sources/opml/export [get]
func ExportGlobalOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var sources []model.FeedSource
		if err := db.
			Where("source_type = ? AND COALESCE(rss_url, '') <> ?", "external_rss", "").
			Order("title ASC, created_at ASC").
			Find(&sources).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed sources"})
			return
		}

		opml := OPML{
			Version: "2.0",
			Head: OPMLHead{
				Title: "Atoman Feed Sources",
			},
		}

		for _, source := range sources {
			title := strings.TrimSpace(source.Title)
			if title == "" {
				title = strings.TrimSpace(source.RssURL)
			}
			opml.Body.Outlines = append(opml.Body.Outlines, OPMLOutline{
				Text:   title,
				Title:  title,
				Type:   "rss",
				XMLURL: source.RssURL,
			})
		}

		output, err := xml.MarshalIndent(opml, "", "  ")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate OPML"})
			return
		}

		c.Header("Content-Type", "application/x-opml+xml")
		c.Header("Content-Disposition", "attachment; filename=\"atoman-feed-sources.opml\"")
		c.Data(http.StatusOK, "application/x-opml+xml", output)
	}
}

// ExportOPML godoc
// @Summary 导出 OPML
// @Description 导出当前用户的订阅为 OPML 文件。
// @Tags feed
// @Produce application/x-opml+xml
// @Success 200 {string} string "OPML XML"
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/opml/export [get]
func ExportOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var subscriptions []model.Subscription
		if err := db.Preload("FeedSource").Preload("SubscriptionGroup").Where("user_id = ?", userID).Find(&subscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscriptions"})
			return
		}

		opml := OPML{
			Version: "2.0",
			Head: OPMLHead{
				Title: "Atoman RSS Exports",
			},
		}

		grouped := make(map[uuid.UUID][]model.Subscription)
		var ungrouped []model.Subscription

		for _, sub := range subscriptions {
			if sub.SubscriptionGroupID != nil {
				grouped[*sub.SubscriptionGroupID] = append(grouped[*sub.SubscriptionGroupID], sub)
			} else {
				ungrouped = append(ungrouped, sub)
			}
		}

		for groupID, subs := range grouped {
			var group model.SubscriptionGroup
			if err := db.First(&group, groupID).Error; err != nil {
				continue
			}

			outline := OPMLOutline{
				Text:     group.Name,
				Title:    group.Name,
				Type:     "rss",
				Outlines: []OPMLOutline{},
			}

			for _, sub := range subs {
				if sub.FeedSource != nil {
					outline.Outlines = append(outline.Outlines, OPMLOutline{
						Text:   sub.FeedSource.Title,
						Title:  sub.FeedSource.Title,
						Type:   "rss",
						XMLURL: sub.FeedSource.RssURL,
					})
				}
			}
			opml.Body.Outlines = append(opml.Body.Outlines, outline)
		}

		// Add ungrouped subscriptions
		for _, sub := range ungrouped {
			if sub.FeedSource != nil {
				opml.Body.Outlines = append(opml.Body.Outlines, OPMLOutline{
					Text:   sub.FeedSource.Title,
					Title:  sub.FeedSource.Title,
					Type:   "rss",
					XMLURL: sub.FeedSource.RssURL,
				})
			}
		}

		output, err := xml.MarshalIndent(opml, "", "  ")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate OPML"})
			return
		}

		c.Header("Content-Type", "application/x-opml+xml")
		c.Header("Content-Disposition", "attachment; filename=\"atoman-export.opml\"")
		c.Data(http.StatusOK, "application/x-opml+xml", output)
	}
}

// ToggleStar godoc
// @Summary 切换条目星标
// @Description 为 feed item 添加或取消星标。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body StarToggleInput true "星标输入"
// @Success 200 {object} StarToggleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/timeline/star [post]
func ToggleStar(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input struct {
			FeedItemID uuid.UUID `json:"feed_item_id" binding:"required"`
		}

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "feed_item_id is required"})
			return
		}

		var feedItem model.FeedItem
		if err := db.First(&feedItem, input.FeedItemID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Feed item not found"})
			return
		}

		var existing model.FeedItemStar
		err := db.Where("user_id = ? AND feed_item_id = ?", userID, input.FeedItemID).First(&existing).Error

		if err == nil {
			if err := db.Delete(&existing).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove star"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"starred": false, "message": "Star removed"})
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			star := model.FeedItemStar{
				UserID:     userID,
				FeedItemID: input.FeedItemID,
				StarredAt:  time.Now(),
			}
			if err := db.Create(&star).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add star"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"starred": true, "message": "Item starred"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
		}
	}
}

func GetFeedStarGroups(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var groups []model.FeedStarGroup
		if err := db.Where("user_id = ?", userID).Order("created_at ASC").Find(&groups).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch star groups"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": groups})
	}
}

func CreateFeedStarGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
			return
		}

		group := model.FeedStarGroup{UserID: userID, Name: name}
		if err := db.Create(&group).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create star group"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": group, "message": "ok"})
	}
}

func UpdateFeedStarGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid star group id"})
			return
		}

		var input struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		name := strings.TrimSpace(input.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
			return
		}

		var group model.FeedStarGroup
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Star group not found"})
			return
		}

		if err := db.Model(&group).Update("name", name).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update star group"})
			return
		}
		group.Name = name

		c.JSON(http.StatusOK, gin.H{"data": group, "message": "ok"})
	}
}

func DeleteFeedStarGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid star group id"})
			return
		}

		var group model.FeedStarGroup
		if err := db.Where("id = ? AND user_id = ?", id, userID).First(&group).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Star group not found"})
			return
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.FeedItemStar{}).
				Where("user_id = ? AND group_id = ?", userID, id).
				Update("group_id", nil).Error; err != nil {
				return err
			}
			return tx.Delete(&group).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete star group"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

func SetFeedStarGroup(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		feedItemID, err := uuid.Parse(c.Param("feedItemId"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid feed item id"})
			return
		}

		var input struct {
			GroupID *uuid.UUID `json:"group_id"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		if input.GroupID != nil {
			var group model.FeedStarGroup
			if err := db.Where("id = ? AND user_id = ?", *input.GroupID, userID).First(&group).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Star group not found"})
				return
			}
		}

		result := db.Model(&model.FeedItemStar{}).
			Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).
			Update("group_id", input.GroupID)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update star group"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "Star not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// GetStarredItems godoc
// @Summary 获取星标条目
// @Description 分页返回当前用户已星标的 feed item。
// @Tags feed
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} StarredItemsResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/stars [get]
func GetStarredItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		offset := (page - 1) * limit

		starQuery := db.Where("user_id = ?", userID)
		if groupIDParam := strings.TrimSpace(c.Query("group_id")); groupIDParam != "" {
			groupID, err := uuid.Parse(groupIDParam)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid group_id"})
				return
			}
			starQuery = starQuery.Where("group_id = ?", groupID)
		}

		var stars []model.FeedItemStar
		if err := starQuery.Order("starred_at DESC").Offset(offset).Limit(limit).Find(&stars).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch starred items"})
			return
		}

		feedItemIDs := make([]uuid.UUID, len(stars))
		for i, star := range stars {
			feedItemIDs[i] = star.FeedItemID
		}

		var feedItems []model.FeedItem
		if err := db.Where("id IN ?", feedItemIDs).Find(&feedItems).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed items"})
			return
		}

		type FeedItemWithSource struct {
			model.FeedItem
			GroupID        *uuid.UUID `json:"group_id"`
			SourceTitle    string     `json:"source_title"`
			SourceSiteURL  string     `json:"source_site_url"`
			SourceImageURL string     `json:"source_image_url"`
		}

		starGroups := make(map[uuid.UUID]*uuid.UUID, len(stars))
		for _, star := range stars {
			starGroups[star.FeedItemID] = star.GroupID
		}

		feedItemsByID := make(map[uuid.UUID]model.FeedItem, len(feedItems))
		for _, item := range feedItems {
			feedItemsByID[item.ID] = item
		}

		result := make([]FeedItemWithSource, 0, len(feedItems))
		for _, star := range stars {
			item, ok := feedItemsByID[star.FeedItemID]
			if !ok {
				continue
			}

			var source model.FeedSource
			if err := db.First(&source, item.FeedSourceID).Error; err == nil {
				result = append(result, FeedItemWithSource{
					FeedItem:       item,
					GroupID:        starGroups[item.ID],
					SourceTitle:    source.Title,
					SourceSiteURL:  source.RssURL, // Use RSS URL as site URL
					SourceImageURL: "",            // No image field in FeedSource
				})
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"items": result,
			"page":  page,
			"total": len(result),
		})
	}
}

// ToggleReadingListItem godoc
// @Summary 切换稍后读条目
// @Description 为 feed item 添加或移除稍后读标记。
// @Tags feed
// @Accept json
// @Produce json
// @Param input body StarToggleInput true "稍后读输入"
// @Success 200 {object} SaveToggleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/reading-list [post]
func ToggleReadingListItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var input struct {
			FeedItemID uuid.UUID `json:"feed_item_id" binding:"required"`
		}

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "feed_item_id is required"})
			return
		}

		var feedItem model.FeedItem
		if err := db.First(&feedItem, input.FeedItemID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Feed item not found"})
			return
		}

		var existing model.ReadingListItem
		err := db.Where("user_id = ? AND feed_item_id = ?", userID, input.FeedItemID).First(&existing).Error

		if err == nil {
			if err := db.Delete(&existing).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove reading list item"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"saved": false})
			return
		}

		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error"})
			return
		}

		item := model.ReadingListItem{
			UserID:     userID,
			FeedItemID: input.FeedItemID,
			CreatedAt:  time.Now(),
		}
		if err := db.Create(&item).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add reading list item"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"saved": true})
	}
}

// GetReadingListItems godoc
// @Summary 获取稍后读列表
// @Description 分页返回当前用户的稍后读条目。
// @Tags feed
// @Produce json
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} ReadingListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/reading-list [get]
func GetReadingListItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if page < 1 {
			page = 1
		}
		if limit < 1 || limit > 100 {
			limit = 20
		}
		offset := (page - 1) * limit

		var total int64
		db.Model(&model.ReadingListItem{}).Where("user_id = ?", userID).Count(&total)

		var listItems []model.ReadingListItem
		if err := db.Preload("FeedItem").Preload("FeedItem.FeedSource").
			Where("user_id = ?", userID).
			Order("created_at DESC").
			Offset(offset).
			Limit(limit).
			Find(&listItems).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch reading list"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"items": listItems,
			"page":  page,
			"total": total,
		})
	}
}

func RemoveReadingListItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		feedItemID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid feed item id"})
			return
		}

		if err := db.Where("user_id = ? AND feed_item_id = ?", userID, feedItemID).Delete(&model.ReadingListItem{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove reading list item"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"removed": true})
	}
}

// CheckSubscriptionHealth checks the health of a specific subscription by attempting to fetch the feed
// CheckSubscriptionHealth godoc
// @Summary 检查单个订阅健康状态
// @Description 检测订阅源可访问性并更新健康状态。
// @Tags feed
// @Produce json
// @Param id path string true "订阅 UUID"
// @Success 200 {object} FeedHealthCheckResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id}/health [post]
func CheckSubscriptionHealth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		subscriptionIDStr := c.Param("id")
		subscriptionID, err := uuid.Parse(subscriptionIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid subscription ID"})
			return
		}

		// Get subscription
		var subscription model.Subscription
		if err := db.Preload("FeedSource").First(&subscription, "id = ?", subscriptionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
			return
		}

		// Verify ownership
		if subscription.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "Access denied"})
			return
		}

		// Internal subscriptions (user/channel/collection) read directly from DB — no network check needed
		if subscription.FeedSource != nil && subscription.FeedSource.SourceType != "external_rss" {
			now := time.Now()
			subscription.HealthStatus = "healthy"
			subscription.ErrorMessage = ""
			subscription.LastChecked = &now
			db.Save(&subscription)
			c.JSON(http.StatusOK, gin.H{
				"subscription_id": subscriptionID.String(),
				"health_status":   "healthy",
				"error_message":   "",
				"last_checked":    subscription.LastChecked,
				"skipped":         true,
				"reason":          "internal subscription — no external URL to check",
			})
			return
		}

		// Attempt to fetch the external RSS feed
		healthStatus := "healthy"
		errorMessage := ""

		resp, err := http.Get(subscription.FeedSource.RssURL)
		if err != nil {
			healthStatus = "error"
			errorMessage = fmt.Sprintf("Failed to fetch feed: %v", err)
		} else if resp.StatusCode >= 400 {
			healthStatus = "warning"
			errorMessage = fmt.Sprintf("HTTP status: %d", resp.StatusCode)
		} else {
			defer resp.Body.Close()
			// Try to parse XML to verify it's valid
			decoder := xml.NewDecoder(resp.Body)
			_, err := decoder.Token()
			if err != nil {
				healthStatus = "error"
				errorMessage = fmt.Sprintf("Invalid XML: %v", err)
			}
		}

		// Update subscription health status
		subscription.HealthStatus = healthStatus
		subscription.ErrorMessage = errorMessage
		now := time.Now()
		subscription.LastChecked = &now
		db.Save(&subscription)

		c.JSON(http.StatusOK, gin.H{
			"subscription_id": subscriptionID.String(),
			"health_status":   healthStatus,
			"error_message":   errorMessage,
			"last_checked":    subscription.LastChecked,
		})
	}
}

// CheckAllSubscriptionsHealth checks health of all user's subscriptions
// CheckAllSubscriptionsHealth godoc
// @Summary 检查全部订阅健康状态
// @Description 批量检测当前用户所有订阅的健康状态。
// @Tags feed
// @Produce json
// @Success 200 {object} FeedHealthCheckListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/health/check-all [post]
func CheckAllSubscriptionsHealth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var subscriptions []model.Subscription
		if err := db.Where("user_id = ?", userID).Find(&subscriptions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch subscriptions"})
			return
		}

		results := make([]gin.H, 0)
		for _, sub := range subscriptions {
			// Fetch feed source
			var source model.FeedSource
			if err := db.First(&source, sub.FeedSourceID).Error; err != nil {
				continue
			}

			// Internal subscriptions read directly from DB — skip network check, always healthy
			if source.SourceType != "external_rss" {
				now := time.Now()
				sub.HealthStatus = "healthy"
				sub.ErrorMessage = ""
				sub.LastChecked = &now
				db.Save(&sub)
				results = append(results, gin.H{
					"subscription_id": sub.ID,
					"health_status":   "healthy",
					"error_message":   "",
					"skipped":         true,
				})
				continue
			}

			healthStatus := "healthy"
			errorMessage := ""

			client := &http.Client{Timeout: 10 * time.Second}
			resp, err := client.Get(source.RssURL)
			if err != nil {
				healthStatus = "error"
				errorMessage = fmt.Sprintf("Failed to fetch: %v", err)
			} else if resp.StatusCode >= 400 {
				healthStatus = "warning"
				errorMessage = fmt.Sprintf("HTTP %d", resp.StatusCode)
			} else {
				defer resp.Body.Close()
			}

			// Update subscription
			sub.HealthStatus = healthStatus
			sub.ErrorMessage = errorMessage
			now := time.Now()
			sub.LastChecked = &now
			db.Save(&sub)

			results = append(results, gin.H{
				"subscription_id": sub.ID,
				"health_status":   healthStatus,
				"error_message":   errorMessage,
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"checked_count": len(results),
			"results":       results,
		})
	}
}

// SearchSubscriptions searches user's subscriptions by title or feed source title
// SearchSubscriptions godoc
// @Summary 搜索订阅
// @Description 按标题搜索当前用户的订阅。
// @Tags feed
// @Produce json
// @Param q query string true "搜索关键字"
// @Param limit query int false "返回数量"
// @Success 200 {object} SearchSubscriptionsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/search [get]
func SearchSubscriptions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		query := c.Query("q")
		if query == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Query parameter 'q' is required"})
			return
		}

		limitStr := c.DefaultQuery("limit", "20")
		limit, err := strconv.Atoi(limitStr)
		if err != nil || limit < 1 || limit > 100 {
			limit = 20
		}

		var subscriptions []model.Subscription
		err = db.Where("user_id = ?", userID).
			Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
			Where("subscriptions.title ILIKE ? OR feed_sources.title ILIKE ?", "%"+query+"%", "%"+query+"%").
			Limit(limit).
			Preload("FeedSource").
			Find(&subscriptions).Error

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to search subscriptions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":  subscriptions,
			"count": len(subscriptions),
		})
	}
}

// GetFeedItem retrieves a single feed item by ID
// GetFeedItem godoc
// @Summary 获取单个 feed 条目
// @Description 返回单个 feed item 详情。
// @Tags feed
// @Produce json
// @Param id path string true "Feed item UUID"
// @Success 200 {object} FeedItemResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/items/{id} [get]
func GetFeedItem(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		itemID := c.Param("id")
		id, err := uuid.Parse(itemID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid item ID"})
			return
		}

		var item model.FeedItem
		err = db.Preload("FeedSource").First(&item, "id = ?", id).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Feed item not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed item"})
			return
		}

		contentHTML := strings.TrimSpace(item.Summary)
		contentSource := "summary"
		fullTextHTML := strings.TrimSpace(item.FullTextHTML)
		if item.FullTextStatus == service.FullTextStatusSuccess && fullTextHTML != "" {
			contentHTML = fullTextHTML
			contentSource = "full_text"
		}

		c.JSON(http.StatusOK, gin.H{
			"data": FeedItemDetailResponse{
				ID:            item.ID,
				Title:         item.Title,
				Summary:       item.Summary,
				Link:          item.Link,
				Author:        item.Author,
				PublishedAt:   item.PublishedAt,
				ImageURL:      item.ImageURL,
				EnclosureURL:  item.EnclosureURL,
				EnclosureType: item.EnclosureType,
				Duration:      item.Duration,
				ContentHTML:   contentHTML,
				ContentSource: contentSource,
				FeedSource:    item.FeedSource,
				FeedItem:      &item,
			},
		})
	}
}

// SubscribeChannel subscribes the current user to a channel
// SubscribeChannel godoc
// @Summary 订阅频道
// @Description 将当前用户订阅到指定频道。
// @Tags feed
// @Produce json
// @Param channel_id path string true "频道 UUID"
// @Success 200 {object} SubscriptionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/channel/{channel_id} [post]
func SubscribeChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		channelIDStr := c.Param("channel_id")
		channelID, err := uuid.Parse(channelIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel ID"})
			return
		}

		// Verify channel exists
		var channel model.Channel
		if err := db.First(&channel, "id = ?", channelID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
			return
		}

		// Find or create FeedSource for this channel
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_channel:%s", channelIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			// Create new FeedSource
			feedSource = model.FeedSource{
				SourceType: "internal_channel",
				SourceID:   &channelID,
				Title:      channel.Name,
				Hash:       hash,
			}
			if err := db.Create(&feedSource).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
				return
			}
		}

		// Check if already subscribed
		var existingSub model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&existingSub).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Already subscribed"})
			return
		}

		// Create subscription
		subscription := model.Subscription{
			UserID:       userID,
			FeedSourceID: feedSource.ID,
			Title:        channel.Name,
		}
		if err := db.Create(&subscription).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Subscribed successfully", "subscription": subscription})
	}
}

// UnsubscribeChannel unsubscribes the current user from a channel
// UnsubscribeChannel godoc
// @Summary 取消订阅频道
// @Description 将当前用户从指定频道取消订阅。
// @Tags feed
// @Produce json
// @Param channel_id path string true "频道 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/channel/{channel_id} [delete]
func UnsubscribeChannel(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		channelIDStr := c.Param("channel_id")
		_, err := uuid.Parse(channelIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel ID"})
			return
		}

		// Find FeedSource for this channel
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_channel:%s", channelIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
			return
		}

		// Delete subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).Delete(&model.Subscription{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unsubscribe"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Unsubscribed successfully"})
	}
}

// SubscribeCollection subscribes the current user to a collection
// SubscribeCollection godoc
// @Summary 订阅合集
// @Description 将当前用户订阅到指定合集。
// @Tags feed
// @Produce json
// @Param collection_id path string true "合集 UUID"
// @Success 200 {object} SubscriptionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/collection/{collection_id} [post]
func SubscribeCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		collectionIDStr := c.Param("collection_id")
		collectionID, err := uuid.Parse(collectionIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection ID"})
			return
		}

		// Verify collection exists
		var collection model.Collection
		if err := db.First(&collection, "id = ?", collectionID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Collection not found"})
			return
		}

		// Find or create FeedSource for this collection
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_collection:%s", collectionIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			// Create new FeedSource
			feedSource = model.FeedSource{
				SourceType: "internal_collection",
				SourceID:   &collectionID,
				Title:      collection.Name,
				Hash:       hash,
			}
			if err := db.Create(&feedSource).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
				return
			}
		}

		// Check if already subscribed
		var existingSub model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&existingSub).Error; err == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Already subscribed"})
			return
		}

		// Create subscription
		subscription := model.Subscription{
			UserID:       userID,
			FeedSourceID: feedSource.ID,
			Title:        collection.Name,
		}
		if err := db.Create(&subscription).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create subscription"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Subscribed successfully", "subscription": subscription})
	}
}

// UnsubscribeCollection unsubscribes the current user from a collection
// UnsubscribeCollection godoc
// @Summary 取消订阅合集
// @Description 将当前用户从指定合集取消订阅。
// @Tags feed
// @Produce json
// @Param collection_id path string true "合集 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/collection/{collection_id} [delete]
func UnsubscribeCollection(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		collectionIDStr := c.Param("collection_id")
		_, err := uuid.Parse(collectionIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection ID"})
			return
		}

		// Find FeedSource for this collection
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_collection:%s", collectionIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Subscription not found"})
			return
		}

		// Delete subscription
		if err := db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).Delete(&model.Subscription{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unsubscribe"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Unsubscribed successfully"})
	}
}

// CheckChannelSubscription checks if the current user is subscribed to a channel
// CheckChannelSubscription godoc
// @Summary 查询频道订阅状态
// @Description 返回当前用户是否已订阅指定频道。
// @Tags feed
// @Produce json
// @Param channel_id path string true "频道 UUID"
// @Success 200 {object} SubscriptionStatusResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/channel/{channel_id}/status [get]
func CheckChannelSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		channelIDStr := c.Param("channel_id")
		_, err := uuid.Parse(channelIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel ID"})
			return
		}

		// Find FeedSource for this channel
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_channel:%s", channelIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			c.JSON(http.StatusOK, gin.H{"subscribed": false})
			return
		}

		// Check subscription
		var sub model.Subscription
		err = db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&sub).Error
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"subscribed": false})
			return
		}

		c.JSON(http.StatusOK, gin.H{"subscribed": true, "subscription": sub})
	}
}

// CheckCollectionSubscription checks if the current user is subscribed to a collection
// CheckCollectionSubscription godoc
// @Summary 查询合集订阅状态
// @Description 返回当前用户是否已订阅指定合集。
// @Tags feed
// @Produce json
// @Param collection_id path string true "合集 UUID"
// @Success 200 {object} SubscriptionStatusResponse
// @Failure 400 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscribe/collection/{collection_id}/status [get]
func CheckCollectionSubscription(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		collectionIDStr := c.Param("collection_id")
		_, err := uuid.Parse(collectionIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid collection ID"})
			return
		}

		// Find FeedSource for this collection
		var feedSource model.FeedSource
		hashStr := fmt.Sprintf("internal_collection:%s", collectionIDStr)
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(hashStr)))

		if err := db.Where("hash = ?", hash).First(&feedSource).Error; err != nil {
			c.JSON(http.StatusOK, gin.H{"subscribed": false})
			return
		}

		// Check subscription
		var sub model.Subscription
		err = db.Where("user_id = ? AND feed_source_id = ?", userID, feedSource.ID).First(&sub).Error
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"subscribed": false})
			return
		}

		c.JSON(http.StatusOK, gin.H{"subscribed": true, "subscription": sub})
	}
}

// GetExploreFeed godoc
// @Summary 探索订阅条目
// @Description 分页返回推荐条目，支持按随机 (random) 或热门 (popular) 排序。
// @Tags feed
// @Produce json
// @Param sort query string false "排序方式" Enums(random,popular)
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} TimelineResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/explore [get]
func GetExploreFeed(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		sort := c.DefaultQuery("sort", "random")
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
		if limit > 100 {
			limit = 100
		}
		offset := (page - 1) * limit

		var items []model.FeedItem
		query := db.Preload("FeedSource")

		if sort == "popular" {
			// Join with stars count to determine popularity
			query = query.Select("feed_items.*, (SELECT COUNT(*) FROM feed_item_stars WHERE feed_item_stars.feed_item_id = feed_items.id) as star_count").
				Order("star_count DESC, published_at DESC")
		} else {
			// Random
			query = query.Order("RANDOM()")
		}

		if err := query.Offset(offset).Limit(limit).Find(&items).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch explore feed"})
			return
		}

		// Convert to TimelineItem format
		var data []any
		for _, item := range items {
			data = append(data, gin.H{
				"type":         "feed_item",
				"feed_item":    item,
				"published_at": item.PublishedAt,
			})
		}

		c.JSON(http.StatusOK, gin.H{"data": data, "message": "ok"})
	}
}

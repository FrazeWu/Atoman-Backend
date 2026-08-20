package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	feedmodule "atoman/internal/modules/feed"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"atoman/internal/service"
)

type adminFeedSourceListRow struct {
	ID            uuid.UUID  `json:"id"`
	Title         string     `json:"title"`
	Provider      string     `json:"provider"`
	SourceType    string     `json:"source_type"`
	HealthStatus  string     `json:"health_status"`
	LastFetchedAt *time.Time `json:"last_fetched_at"`
	Hidden        bool       `json:"hidden"`
	RssURL        string     `json:"rss_url"`
	SiteURL       string     `json:"site_url"`
	CanonicalURL  string     `json:"canonical_url"`
	BookmarkCount int64      `json:"bookmark_count"`
	ReadCount     int64      `json:"read_count"`
	RecentEvents  []struct {
		EventType string    `json:"event_type"`
		CreatedAt time.Time `json:"created_at"`
	} `json:"recent_events"`
}

type adminFeedSourceUpdateInput struct {
	Title  *string `json:"title"`
	Hidden *bool   `json:"hidden"`
}

type adminFeedSourceDeleteInput struct {
	ConfirmTitle string `json:"confirm_title" binding:"required"`
}

type adminFeedSourceImpact struct {
	Subscriptions    int64 `json:"subscriptions"`
	Items            int64 `json:"items"`
	ReadRecords      int64 `json:"read_records"`
	StarredItems     int64 `json:"starred_items"`
	ReadingListItems int64 `json:"reading_list_items"`
}

func requireFeedSourceOwner(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok || user.Role != authctx.RoleOwner {
		c.JSON(http.StatusForbidden, gin.H{"error": "owner role is required"})
		return authctx.CurrentUser{}, false
	}
	return user, true
}

func feedSourceImpact(db *gorm.DB, sourceID uuid.UUID) (adminFeedSourceImpact, error) {
	impact := adminFeedSourceImpact{}
	if err := db.Model(&model.Subscription{}).Where("feed_source_id = ?", sourceID).Count(&impact.Subscriptions).Error; err != nil {
		return impact, err
	}
	if err := db.Model(&model.FeedItem{}).Where("feed_source_id = ?", sourceID).Count(&impact.Items).Error; err != nil {
		return impact, err
	}
	itemQuery := db.Model(&model.FeedItem{}).Select("id").Where("feed_source_id = ?", sourceID)
	if err := db.Model(&model.FeedItemRead{}).Where("feed_item_id IN (?)", itemQuery).Count(&impact.ReadRecords).Error; err != nil {
		return impact, err
	}
	if err := db.Model(&model.FeedItemStar{}).Where("feed_item_id IN (?)", itemQuery).Count(&impact.StarredItems).Error; err != nil {
		return impact, err
	}
	if err := db.Model(&model.ReadingListItem{}).Where("target_type = ? AND target_id IN (?)", "feed_item", itemQuery).Count(&impact.ReadingListItems).Error; err != nil {
		return impact, err
	}
	return impact, nil
}

type adminFeedSourceInput struct {
	RssURL string `json:"rss_url"`
	Title  string `json:"title"`
}

// GetAdminFeedSourceImpact godoc
// @Summary 预览订阅源永久删除影响
// @Tags admin
// @Produce json
// @Param id path string true "订阅源 UUID"
// @Success 200 {object} adminFeedSourceImpact
// @Security BearerAuth
// @Router /api/v1/admin/feed/sources/{id}/impact [get]
func GetAdminFeedSourceImpact(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		var source model.FeedSource
		if err := db.First(&source, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "feed source not found"})
			return
		}
		impact, err := feedSourceImpact(db, source.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load source impact"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"source": gin.H{"id": source.ID, "title": source.Title}, "impact": impact})
	}
}

// GetAdminFeedSourceDiagnostics godoc
// @Summary 获取订阅源诊断历史
// @Description 返回最近 90 天的全文抓取失败和恢复记录。
// @Tags admin
// @Produce json
// @Param id path string true "订阅源 UUID"
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} object
// @Security BearerAuth
// @Router /api/v1/admin/feed/sources/{id}/diagnostics [get]
func GetAdminFeedSourceDiagnostics(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		sourceID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		page, limit := parseAdminListParams(c)
		var total int64
		query := db.Model(&model.FeedSourceDiagnostic{}).
			Where("feed_source_id = ? AND created_at >= ?", sourceID, time.Now().UTC().Add(-90*24*time.Hour))
		if err := query.Count(&total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count diagnostics"})
			return
		}
		var diagnostics []model.FeedSourceDiagnostic
		if err := query.Order("created_at DESC").Offset((page - 1) * limit).Limit(limit).Find(&diagnostics).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load diagnostics"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": diagnostics, "meta": gin.H{"page": page, "limit": limit, "total": total}})
	}
}

func normalizeExternalRSSURL(db *gorm.DB, rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", errors.New("rss_url is required")
	}
	if _, err := feedmodule.ResolveInternalRSSURL(db, trimmed); err == nil {
		return "", errors.New("Internal RSS sources are managed separately")
	}

	u, err := url.ParseRequestURI(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("rss_url must be an absolute http/https URL")
	}

	return trimmed, nil
}

func AdminListFeedSources(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var sources []model.FeedSource
		if err := db.Order("updated_at DESC").Find(&sources).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load feed sources"})
			return
		}

		items := make([]adminFeedSourceListRow, 0, len(sources))
		for _, source := range sources {
			var bookmarkCount int64
			_ = db.Model(&model.Subscription{}).Where("feed_source_id = ?", source.ID).Count(&bookmarkCount).Error

			var readCount int64
			_ = db.Model(&model.SourceReadEvent{}).Where("source_id = ?", source.ID.String()).Count(&readCount).Error

			var recentSourceEvents []model.SourceReadEvent
			_ = db.Where("source_id = ?", source.ID.String()).Order("created_at DESC").Limit(5).Find(&recentSourceEvents).Error
			recentEvents := make([]struct {
				EventType string    `json:"event_type"`
				CreatedAt time.Time `json:"created_at"`
			}, 0, len(recentSourceEvents))
			for _, event := range recentSourceEvents {
				recentEvents = append(recentEvents, struct {
					EventType string    `json:"event_type"`
					CreatedAt time.Time `json:"created_at"`
				}{
					EventType: event.EventType,
					CreatedAt: event.CreatedAt,
				})
			}

			items = append(items, adminFeedSourceListRow{
				ID:            source.ID,
				Title:         source.Title,
				Provider:      source.Provider,
				SourceType:    source.SourceType,
				HealthStatus:  source.HealthStatus,
				LastFetchedAt: source.LastFetchedAt,
				Hidden:        source.Hidden,
				RssURL:        source.RssURL,
				SiteURL:       source.SiteURL,
				CanonicalURL:  source.CanonicalURL,
				BookmarkCount: bookmarkCount,
				ReadCount:     readCount,
				RecentEvents:  recentEvents,
			})
		}

		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}

func AdminUpdateFeedSourceRow(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.Param("id"))
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "source id is required"})
			return
		}

		var input adminFeedSourceUpdateInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}

		updates := map[string]any{}
		if input.Title != nil {
			trimmedTitle := strings.TrimSpace(*input.Title)
			if trimmedTitle == "" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "title must not be blank"})
				return
			}
			updates["title"] = trimmedTitle
		}
		if input.Hidden != nil {
			updates["hidden"] = *input.Hidden
		}
		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no fields to update"})
			return
		}

		result := db.Model(&model.FeedSource{}).Where("id = ?", id).Updates(updates)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update feed source"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "feed source not found"})
			return
		}

		var source model.FeedSource
		if err := db.First(&source, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reload feed source"})
			return
		}
		user, _ := authctx.Current(c)
		if err := audit.Record(db, audit.Entry{
			ActorID: &user.ID, Action: "feed_source.updated", EntityType: "feed_source", EntityID: &source.ID,
			Metadata: map[string]any{"fields": updates},
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to audit feed source update"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"item": source})
	}
}

func AdminDeleteFeedSourceRow(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		owner, ok := requireFeedSourceOwner(c)
		if !ok {
			return
		}
		id, err := uuid.Parse(strings.TrimSpace(c.Param("id")))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		var input adminFeedSourceDeleteInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "confirmation is required"})
			return
		}

		err = db.Transaction(func(tx *gorm.DB) error {
			var source model.FeedSource
			if err := tx.First(&source, "id = ?", id).Error; err != nil {
				return err
			}
			if strings.TrimSpace(input.ConfirmTitle) != source.Title {
				return fmt.Errorf("source title confirmation does not match")
			}
			impact, err := feedSourceImpact(tx, source.ID)
			if err != nil {
				return err
			}
			if impact.StarredItems > 0 || impact.ReadingListItems > 0 {
				return fmt.Errorf("source has user saved items")
			}
			if err := audit.Record(tx, audit.Entry{
				ActorID: &owner.ID, Action: "feed_source.deleted", EntityType: "feed_source", EntityID: &source.ID,
				Metadata: map[string]any{"title": source.Title, "impact": impact},
			}); err != nil {
				return err
			}
			if err := tx.Where("feed_source_id = ?", source.ID).Delete(&model.Subscription{}).Error; err != nil {
				return err
			}
			var itemIDs []uuid.UUID
			if err := tx.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Pluck("id", &itemIDs).Error; err != nil {
				return err
			}
			if len(itemIDs) > 0 {
				if err := tx.Where("feed_item_id IN ?", itemIDs).Delete(&model.FeedItemRead{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("feed_source_id = ?", source.ID).Delete(&model.FeedSourceDiagnostic{}).Error; err != nil {
				return err
			}
			if err := tx.Where("feed_source_id = ?", source.ID).Delete(&model.FeedItem{}).Error; err != nil {
				return err
			}
			return tx.Delete(&source).Error
		})
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "feed source not found"})
				return
			}
			if strings.Contains(err.Error(), "confirmation") || strings.Contains(err.Error(), "saved items") {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete feed source"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

func CreateAdminFeedSource(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input adminFeedSourceInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid source payload"})
			return
		}

		rssURL, err := normalizeExternalRSSURL(db, input.RssURL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		source, err := feedmodule.FindOrCreateFeedSource(db, "external_rss", nil, rssURL, input.Title, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
			return
		}

		title := strings.TrimSpace(input.Title)
		if title != "" && title != source.Title {
			if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Update("title", title).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create feed source"})
				return
			}
			source.Title = title
		}

		c.JSON(http.StatusCreated, gin.H{
			"id":                source.ID,
			"title":             source.Title,
			"rss_url":           source.RssURL,
			"source_type":       source.SourceType,
			"full_text_enabled": source.FullTextEnabled,
		})
	}
}

func UpdateAdminFeedSource(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input adminFeedSourceInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid source payload"})
			return
		}

		var source model.FeedSource
		if err := db.First(&source, "id = ?", c.Param("source_id")).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load source"})
			return
		}
		if source.SourceType != "external_rss" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only external RSS sources can be updated here"})
			return
		}

		rssURL, err := normalizeExternalRSSURL(db, input.RssURL)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		updates := map[string]any{
			"rss_url": rssURL,
			"hash":    feedmodule.BuildFeedSourceHash("external_rss", nil, rssURL),
			"title":   strings.TrimSpace(input.Title),
		}
		if strings.TrimSpace(input.Title) == "" {
			delete(updates, "title")
		}

		if err := db.Model(&model.FeedSource{}).Where("id = ?", source.ID).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update feed source"})
			return
		}
		if err := db.First(&source, "id = ?", source.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load source"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"id":                source.ID,
			"title":             source.Title,
			"rss_url":           source.RssURL,
			"source_type":       source.SourceType,
			"full_text_enabled": source.FullTextEnabled,
		})
	}
}

func SyncAdminFeedSource(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var source model.FeedSource
		if err := db.First(&source, "id = ?", c.Param("source_id")).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "Source not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load source"})
			return
		}
		if source.SourceType != "external_rss" || strings.Contains(source.RssURL, "/api/v1/feed/rss/") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only external blog RSS sources can be synced manually"})
			return
		}

		go func() {
			service.SyncSingleRSS(db, source)
			service.RequestFullTextWorkerRun()
		}()

		c.JSON(http.StatusOK, gin.H{
			"id":      source.ID,
			"message": "sync_started",
		})
	}
}

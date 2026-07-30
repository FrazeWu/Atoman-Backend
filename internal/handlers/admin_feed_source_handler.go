package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	feedmodule "atoman/internal/modules/feed"
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

type adminFeedSourceInput struct {
	RssURL string `json:"rss_url"`
	Title  string `json:"title"`
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
		c.JSON(http.StatusOK, gin.H{"item": source})
	}
}

func AdminDeleteFeedSourceRow(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.Param("id"))
		if id == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "source id is required"})
			return
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			var source model.FeedSource
			if err := tx.First(&source, "id = ?", id).Error; err != nil {
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
				if err := tx.Where("feed_item_id IN ?", itemIDs).Delete(&model.FeedItemStar{}).Error; err != nil {
					return err
				}
				if err := tx.Where("target_type = ? AND target_id IN ?", "feed_item", itemIDs).Delete(&model.ReadingListItem{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("feed_source_id = ?", source.ID).Delete(&model.FeedItem{}).Error; err != nil {
				return err
			}
			return tx.Delete(&source).Error
		}); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "feed source not found"})
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

		go service.SyncSingleRSS(db, source)

		c.JSON(http.StatusOK, gin.H{
			"id":      source.ID,
			"message": "sync_started",
		})
	}
}

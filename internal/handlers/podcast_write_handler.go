package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
	"atoman/internal/modules/lifecycle"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/indexnow"
)

// CreatePodcastEpisode godoc
// @Summary 创建播客单集
// @Description 创建 Post 与 PodcastEpisode 记录。
// @Tags podcast
// @Accept json
// @Produce json
// @Param input body PodcastEpisodeCreateInput true "播客单集创建输入"
// @Success 201 {object} model.PodcastEpisode
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/episodes [post]
func CreatePodcastEpisode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		idVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		userID := idVal.(uuid.UUID)

		var input struct {
			ChannelID       string      `json:"channel_id" binding:"required"`
			Title           string      `json:"title" binding:"required"`
			Shownotes       string      `json:"shownotes"`
			AudioURL        string      `json:"audio_url" binding:"required"`
			DurationSec     int         `json:"duration_sec"`
			EpisodeCoverURL string      `json:"episode_cover_url"`
			SeasonNumber    int         `json:"season_number"`
			EpisodeNumber   int         `json:"episode_number"`
			Status          string      `json:"status"`
			Visibility      string      `json:"visibility"`
			CollectionID    *uuid.UUID  `json:"collection_id"`
			CollectionIDs   []uuid.UUID `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		chID, err := uuid.Parse(input.ChannelID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel_id"})
			return
		}

		var channel model.Channel
		if err := db.First(&channel, "id = ?", chID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		if !ownsChannel(channel.UserID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		status := input.Status
		if status == "" {
			status = "draft"
		}
		visibility := input.Visibility
		if visibility == "" {
			visibility = "public"
		}
		if visibility != "public" && visibility != "followers" && visibility != "private" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid visibility"})
			return
		}
		collectionID, err := studioapi.NewService(db).ResolveContentCollection(
			userID, chID, studioapi.ModulePodcast, input.CollectionID, input.CollectionIDs, status == "published",
		)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		seasonNum := input.SeasonNumber
		if seasonNum < 1 {
			seasonNum = 1
		}

		var ep model.PodcastEpisode
		var episodeID uuid.UUID
		txErr := db.Transaction(func(tx *gorm.DB) error {
			contentID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			episodeID, err = uuid.NewV7()
			if err != nil {
				return err
			}
			entry := model.ContentEntry{
				Base: model.Base{ID: contentID}, AuthorID: &userID, ChannelID: chID,
				Kind: "podcast", Title: strings.TrimSpace(input.Title), Summary: "", Status: status,
				Visibility: visibility,
			}
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
			extension := model.ContentEpisodeExtension{
				ContentID: contentID, EpisodeID: episodeID, LegacyPostID: contentID,
				CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
				AudioURL: input.AudioURL, DurationSec: input.DurationSec, EpisodeCoverURL: input.EpisodeCoverURL,
				SeasonNumber: seasonNum, EpisodeNumber: input.EpisodeNumber, Shownotes: input.Shownotes,
			}
			if err := tx.Create(&extension).Error; err != nil {
				return err
			}
			if collectionID != nil {
				if err := tx.Create(&model.ContentCollectionMembership{ContentID: contentID, CollectionID: *collectionID}).Error; err != nil {
					return err
				}
			}
			if status == "published" {
				return lifecycle.NewService(tx).EnqueuePublication("podcast", episodeID)
			}
			return nil
		})
		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": txErr.Error()})
			return
		}

		ep, txErr = contentmodule.LoadPodcastEpisode(db, contentmodule.PodcastQuery(db).Where("episodes.episode_id = ?", episodeID))
		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": txErr.Error()})
			return
		}
		c.JSON(http.StatusCreated, ep)
	}
}

// UpdatePodcastEpisode updates episode metadata and shownotes.
// UpdatePodcastEpisode godoc
// @Summary 更新播客单集
// @Description 更新当前用户拥有的播客单集。
// @Tags podcast
// @Accept json
// @Produce json
// @Param id path string true "单集 UUID"
// @Param input body PodcastEpisodeUpdateInput true "播客单集更新输入"
// @Success 200 {object} model.PodcastEpisode
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/episodes/{id} [put]
func UpdatePodcastEpisode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		idVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		userID := idVal.(uuid.UUID)
		episodeID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}

		ep, err := contentmodule.LoadPodcastEpisode(db, contentmodule.PodcastQuery(db).Where("episodes.episode_id = ?", episodeID))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		if ep.Post == nil || ep.Post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		contentID, err := contentmodule.PodcastContentID(db, episodeID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		wasPublic := ep.Post.Status == "published" && (ep.Post.Visibility == "" || ep.Post.Visibility == "public")

		var input struct {
			Title           *string           `json:"title"`
			Shownotes       *string           `json:"shownotes"`
			AudioURL        *string           `json:"audio_url"`
			EpisodeCoverURL *string           `json:"episode_cover_url"`
			DurationSec     *int              `json:"duration_sec"`
			SeasonNumber    *int              `json:"season_number"`
			EpisodeNumber   *int              `json:"episode_number"`
			Status          *string           `json:"status"`
			Visibility      *string           `json:"visibility"`
			CollectionID    nullableUUIDInput `json:"collection_id"`
			CollectionIDs   []uuid.UUID       `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		effectiveStatus := ep.Post.Status
		if input.Status != nil {
			effectiveStatus = *input.Status
		}
		wasPublished := ep.Post.Status == "published"
		collectionChanged := input.CollectionID.Set || input.CollectionIDs != nil
		shouldResolveCollection := collectionChanged || (!wasPublished && effectiveStatus == "published")
		resolvedCollectionID := ep.Post.CollectionID
		if shouldResolveCollection {
			if ep.Post.CollectionConflict && !collectionChanged {
				httpx.Error(c, apperr.Conflict("studio.collection_conflict", "Choose one collection before publishing"))
				return
			}
			requestedCollectionID := ep.Post.CollectionID
			legacyCollectionIDs := []uuid.UUID(nil)
			if collectionChanged {
				requestedCollectionID = nil
				if input.CollectionID.Set {
					requestedCollectionID = input.CollectionID.Value
				}
				legacyCollectionIDs = input.CollectionIDs
			}
			resolvedCollectionID, err = studioapi.NewService(db).ResolveContentCollection(
				userID, ep.ChannelID, studioapi.ModulePodcast, requestedCollectionID, legacyCollectionIDs, effectiveStatus == "published",
			)
			if err != nil {
				httpx.Error(c, err)
				return
			}
		}

		entryUpdates := map[string]any{}
		if input.Title != nil {
			entryUpdates["title"] = strings.TrimSpace(*input.Title)
		}
		if input.Status != nil {
			entryUpdates["status"] = *input.Status
		}
		if input.Visibility != nil {
			if *input.Visibility != "public" && *input.Visibility != "followers" && *input.Visibility != "private" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid visibility"})
				return
			}
			entryUpdates["visibility"] = *input.Visibility
		}
		extensionUpdates := map[string]any{}
		if input.Shownotes != nil {
			extensionUpdates["shownotes"] = *input.Shownotes
		}
		if input.AudioURL != nil {
			extensionUpdates["audio_url"] = strings.TrimSpace(*input.AudioURL)
		}
		if input.EpisodeCoverURL != nil {
			extensionUpdates["episode_cover_url"] = *input.EpisodeCoverURL
		}
		if input.DurationSec != nil {
			extensionUpdates["duration_sec"] = *input.DurationSec
		}
		if input.SeasonNumber != nil {
			extensionUpdates["season_number"] = *input.SeasonNumber
		}
		if input.EpisodeNumber != nil {
			extensionUpdates["episode_number"] = *input.EpisodeNumber
		}
		if shouldResolveCollection {
			extensionUpdates["collection_conflict"] = false
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			if len(entryUpdates) > 0 {
				if err := tx.Model(&model.ContentEntry{}).Where("id = ?", contentID).Updates(entryUpdates).Error; err != nil {
					return err
				}
			}
			if len(extensionUpdates) > 0 {
				if err := tx.Model(&model.ContentEpisodeExtension{}).Where("content_id = ?", contentID).Updates(extensionUpdates).Error; err != nil {
					return err
				}
			}
			if shouldResolveCollection {
				if err := tx.Where("content_id = ?", contentID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
					return err
				}
				if resolvedCollectionID != nil {
					if err := tx.Create(&model.ContentCollectionMembership{ContentID: contentID, CollectionID: *resolvedCollectionID}).Error; err != nil {
						return err
					}
				}
			}
			if effectiveStatus == "published" && !wasPublished {
				if err := lifecycle.NewService(tx).EnqueuePublication("podcast", episodeID); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		ep, err = contentmodule.LoadPodcastEpisode(db, contentmodule.PodcastQuery(db).Where("episodes.episode_id = ?", episodeID))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if wasPublic || (ep.Post != nil && ep.Post.Status == "published" && (ep.Post.Visibility == "" || ep.Post.Visibility == "public")) {
			indexnow.NotifyPaths("/podcasts/episode/" + ep.ID.String())
		}
		c.JSON(http.StatusOK, ep)
	}
}

// DeletePodcastEpisode soft-deletes the episode and its associated Post.
// DeletePodcastEpisode godoc
// @Summary 删除播客单集
// @Description 软删除单集及其关联 Post。
// @Tags podcast
// @Produce json
// @Param id path string true "单集 UUID"
// @Success 200 {object} MessageResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/episodes/{id} [delete]
func DeletePodcastEpisode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		idVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		userID := idVal.(uuid.UUID)
		episodeID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		ep, err := contentmodule.LoadPodcastEpisode(db, contentmodule.PodcastQuery(db).Where("episodes.episode_id = ?", episodeID))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		if ep.Post == nil || ep.Post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		wasPublic := ep.Post.Status == "published" && (ep.Post.Visibility == "" || ep.Post.Visibility == "public")
		contentID, err := contentmodule.PodcastContentID(db, episodeID)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("content_id = ?", contentID).Delete(&model.ContentCollectionMembership{}).Error; err != nil {
				return err
			}
			return tx.Delete(&model.ContentEntry{}, "id = ?", contentID).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if wasPublic {
			indexnow.NotifyPaths("/podcasts/episode/" + ep.ID.String())
		}
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	}
}

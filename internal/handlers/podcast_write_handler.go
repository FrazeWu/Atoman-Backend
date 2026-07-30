package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/modules/lifecycle"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/httpx"
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
		if err := studioapi.NewService(db).ValidateContentScope(userID, chID, studioapi.ModulePodcast, input.CollectionIDs, status == "published"); err != nil {
			httpx.Error(c, err)
			return
		}
		seasonNum := input.SeasonNumber
		if seasonNum < 1 {
			seasonNum = 1
		}

		var ep model.PodcastEpisode
		txErr := db.Transaction(func(tx *gorm.DB) error {
			post := model.Post{
				UserID:     userID,
				ChannelID:  &chID,
				Title:      strings.TrimSpace(input.Title),
				Content:    input.Shownotes,
				Status:     status,
				Visibility: visibility,
			}
			if err := tx.Create(&post).Error; err != nil {
				return err
			}
			ep = model.PodcastEpisode{
				PostID:          post.ID,
				ChannelID:       chID,
				AudioURL:        input.AudioURL,
				DurationSec:     input.DurationSec,
				EpisodeCoverURL: input.EpisodeCoverURL,
				SeasonNumber:    seasonNum,
				EpisodeNumber:   input.EpisodeNumber,
			}
			if len(input.CollectionIDs) > 0 {
				if err := assignPodcastPostCollections(tx, &post, chID, input.CollectionIDs); err != nil {
					return err
				}
			}
			if err := tx.Create(&ep).Error; err != nil {
				return err
			}
			if status == "published" {
				return lifecycle.NewService(tx).EnqueuePublication("podcast", ep.ID)
			}
			return nil
		})
		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": txErr.Error()})
			return
		}

		db.Preload("Post.Collection").Preload("Channel").First(&ep, "podcast_episodes.id = ?", ep.ID)
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
		id := c.Param("id")

		var ep model.PodcastEpisode
		if err := db.Preload("Post").Preload("Post.Collections").First(&ep, "podcast_episodes.id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		if ep.Post == nil || ep.Post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		var input struct {
			Title           *string     `json:"title"`
			Shownotes       *string     `json:"shownotes"`
			AudioURL        *string     `json:"audio_url"`
			EpisodeCoverURL *string     `json:"episode_cover_url"`
			DurationSec     *int        `json:"duration_sec"`
			SeasonNumber    *int        `json:"season_number"`
			EpisodeNumber   *int        `json:"episode_number"`
			Status          *string     `json:"status"`
			Visibility      *string     `json:"visibility"`
			CollectionIDs   []uuid.UUID `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		effectiveStatus := ep.Post.Status
		wasPublished := ep.Post.Status == "published"
		if input.Status != nil {
			effectiveStatus = *input.Status
		}
		effectiveCollectionIDs := input.CollectionIDs
		if input.CollectionIDs == nil {
			effectiveCollectionIDs = make([]uuid.UUID, 0, len(ep.Post.Collections)+1)
			for _, collection := range ep.Post.Collections {
				effectiveCollectionIDs = append(effectiveCollectionIDs, collection.ID)
			}
			if ep.Post.CollectionID != nil {
				effectiveCollectionIDs = append(effectiveCollectionIDs, *ep.Post.CollectionID)
			}
		}
		if err := studioapi.NewService(db).ValidateContentScope(userID, ep.ChannelID, studioapi.ModulePodcast, effectiveCollectionIDs, effectiveStatus == "published"); err != nil {
			httpx.Error(c, err)
			return
		}

		postUpdates := map[string]interface{}{}
		if input.Title != nil {
			postUpdates["title"] = strings.TrimSpace(*input.Title)
		}
		if input.Shownotes != nil {
			postUpdates["content"] = *input.Shownotes
		}
		if input.Status != nil {
			postUpdates["status"] = *input.Status
		}
		if input.Visibility != nil {
			if *input.Visibility != "public" && *input.Visibility != "followers" && *input.Visibility != "private" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid visibility"})
				return
			}
			postUpdates["visibility"] = *input.Visibility
		}
		epUpdates := map[string]interface{}{}
		if input.AudioURL != nil {
			epUpdates["audio_url"] = strings.TrimSpace(*input.AudioURL)
		}
		if input.EpisodeCoverURL != nil {
			epUpdates["episode_cover_url"] = *input.EpisodeCoverURL
		}
		if input.DurationSec != nil {
			epUpdates["duration_sec"] = *input.DurationSec
		}
		if input.SeasonNumber != nil {
			epUpdates["season_number"] = *input.SeasonNumber
		}
		if input.EpisodeNumber != nil {
			epUpdates["episode_number"] = *input.EpisodeNumber
		}

		statusCode := http.StatusInternalServerError
		if err := db.Transaction(func(tx *gorm.DB) error {
			if len(postUpdates) > 0 {
				if err := tx.Model(ep.Post).Updates(postUpdates).Error; err != nil {
					return err
				}
			}
			if len(epUpdates) > 0 {
				if err := tx.Model(&ep).Updates(epUpdates).Error; err != nil {
					return err
				}
			}
			if input.CollectionIDs != nil {
				if len(input.CollectionIDs) == 0 {
					if err := tx.Model(ep.Post).Association("Collections").Clear(); err != nil {
						return err
					}
				} else if err := assignPodcastPostCollections(tx, ep.Post, ep.ChannelID, input.CollectionIDs); err != nil {
					if errors.Is(err, errInvalidPodcastCollections) {
						statusCode = http.StatusBadRequest
					}
					return err
				}
			}

			if err := tx.Preload("Post.Collection").Preload("Channel").First(&ep, "podcast_episodes.id = ?", ep.ID).Error; err != nil {
				return err
			}
			if effectiveStatus == "published" && !wasPublished {
				return lifecycle.NewService(tx).EnqueuePublication("podcast", ep.ID)
			}
			return nil
		}); err != nil {
			c.JSON(statusCode, gin.H{"error": err.Error()})
			return
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
		id := c.Param("id")

		var ep model.PodcastEpisode
		if err := db.Preload("Post").First(&ep, "podcast_episodes.id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		if ep.Post == nil || ep.Post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		db.Delete(&ep)
		db.Delete(ep.Post)
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	}
}

var errInvalidPodcastCollections = errors.New("存在无效合集或合集不属于当前频道")

func assignPodcastPostCollections(db *gorm.DB, post *model.Post, channelID uuid.UUID, ids []uuid.UUID) error {
	var collections []model.Collection
	if err := db.Where("id IN ? AND channel_id = ?", ids, channelID).Find(&collections).Error; err != nil {
		return err
	}
	if len(collections) != len(ids) {
		return errInvalidPodcastCollections
	}

	return db.Model(post).Association("Collections").Replace(collections)
}

// GetPodcastRSS returns a standards-compliant podcast RSS with <enclosure> tags.

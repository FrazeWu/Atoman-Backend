package handlers

import (
	"errors"
	"net/http"

	"atoman/internal/model"
	"atoman/internal/modules/blog"
	contentmodule "atoman/internal/modules/content"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type podcastRatingInput struct {
	Score int `json:"score" binding:"required,min=1,max=10"`
}

// GetPodcastRating godoc
// @Summary 获取播客单集评分
// @Description 返回已发布且当前用户可访问的单集评分；匿名读取不包含个人评分。
// @Tags podcast
// @Produce json
// @Param id path string true "单集 UUID"
// @Success 200 {object} contentRatingSummary
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/podcast/episodes/{id}/rating [get]
func GetPodcastRating(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		contentID, ok := rateablePodcastContentID(db, c)
		if !ok {
			return
		}
		writePodcastRatingSummary(db, c, contentID)
	}
}

// SetPodcastRating godoc
// @Summary 设置播客单集评分
// @Description 提交或更新 1–10 分（0.5–5 星）；每用户每单集只有一个有效评分，草稿不可评分。
// @Tags podcast
// @Accept json
// @Produce json
// @Param id path string true "单集 UUID"
// @Param input body podcastRatingInput true "评分"
// @Success 200 {object} contentRatingSummary
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/episodes/{id}/rating [put]
func SetPodcastRating(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var input podcastRatingInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "score must be an integer between 1 and 10"})
			return
		}
		contentID, ok := rateablePodcastContentID(db, c)
		if !ok {
			return
		}
		rating := model.PostRating{UserID: userID, ContentID: contentID, Score: input.Score}
		if err := db.Clauses(contentRatingUpsertConflict(input.Score)).Create(&rating).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save podcast rating"})
			return
		}
		writePodcastRatingSummary(db, c, contentID)
	}
}

// DeletePodcastRating godoc
// @Summary 撤销播客单集评分
// @Tags podcast
// @Produce json
// @Param id path string true "单集 UUID"
// @Success 200 {object} contentRatingSummary
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/episodes/{id}/rating [delete]
func DeletePodcastRating(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		contentID, ok := rateablePodcastContentID(db, c)
		if !ok {
			return
		}
		if err := db.Where("user_id = ? AND content_id = ?", userID, contentID).Delete(&model.PostRating{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear podcast rating"})
			return
		}
		writePodcastRatingSummary(db, c, contentID)
	}
}

func rateablePodcastContentID(db *gorm.DB, c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
		return uuid.Nil, false
	}
	ep, err := contentmodule.LoadPodcastEpisode(db, contentmodule.PodcastQuery(db).
		Where("episodes.episode_id = ? AND posts.status = ?", id, "published"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"error": "episode not found"})
		return uuid.Nil, false
	}
	allowed, err := blog.CanViewPublishedPost(db, currentPodcastViewerID(c), *ep.Post)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check episode access"})
		return uuid.Nil, false
	}
	if !allowed {
		c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
		return uuid.Nil, false
	}
	contentID, err := contentmodule.PodcastContentID(db, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to resolve episode content"})
		return uuid.Nil, false
	}
	return contentID, true
}

func writePodcastRatingSummary(db *gorm.DB, c *gin.Context, contentID uuid.UUID) {
	summary, err := contentRatingSummaryForContent(db, contentID, currentPodcastViewerID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load podcast rating"})
		return
	}
	c.JSON(http.StatusOK, summary)
}

package handlers

import (
	"errors"
	"math"
	"net/http"
	"time"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type videoRatingInput struct {
	Score int `json:"score" binding:"required,min=1,max=10"`
}

type videoRatingSummary struct {
	RatingScore  float64 `json:"rating_score"`
	RatingCount  int64   `json:"rating_count"`
	ViewerRating *int    `json:"viewer_rating,omitempty"`
}

// SetVideoRating godoc
// @Summary 设置视频评分
// @Description 为可观看视频提交或更新 0.5 至 5 星评分，分值使用 1 至 10 的半星单位。
// @Tags videos
// @Accept json
// @Produce json
// @Param id path string true "视频 UUID"
// @Param input body videoRatingInput true "视频评分"
// @Success 200 {object} videoRatingSummary
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/videos/{id}/rating [put]
func SetVideoRating(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		videoID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		var input videoRatingInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		contentID, ok := rateableVideoContentID(db, userID, videoID, c)
		if !ok {
			return
		}
		rating := model.PostRating{UserID: userID, ContentID: contentID, Score: input.Score}
		if err := db.Clauses(videoRatingUpsertConflict(input.Score)).Create(&rating).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save video rating"})
			return
		}
		summary, err := videoRatingSummaryForContent(db, contentID, &userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load video rating"})
			return
		}
		c.JSON(http.StatusOK, summary)
	}
}

// DeleteVideoRating godoc
// @Summary 清除视频评分
// @Description 清除当前用户对可观看视频的评分。
// @Tags videos
// @Produce json
// @Param id path string true "视频 UUID"
// @Success 200 {object} videoRatingSummary
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/videos/{id}/rating [delete]
func DeleteVideoRating(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		videoID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		contentID, ok := rateableVideoContentID(db, userID, videoID, c)
		if !ok {
			return
		}
		if err := db.Where("user_id = ? AND content_id = ?", userID, contentID).Delete(&model.PostRating{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear video rating"})
			return
		}
		summary, err := videoRatingSummaryForContent(db, contentID, &userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load video rating"})
			return
		}
		c.JSON(http.StatusOK, summary)
	}
}

func rateableVideoContentID(db *gorm.DB, viewerID, videoID uuid.UUID, c *gin.Context) (uuid.UUID, bool) {
	video, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", videoID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
		return uuid.Nil, false
	}
	allowed, err := canViewVideo(db, &viewerID, video)
	if err != nil || !allowed {
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
		return uuid.Nil, false
	}
	contentID, err := contentmodule.VideoContentID(db, videoID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
		return uuid.Nil, false
	}
	return contentID, true
}

func videoRatingUpsertConflict(score int) clause.OnConflict {
	return clause.OnConflict{
		Columns:     []clause.Column{{Name: "user_id"}, {Name: "content_id"}},
		TargetWhere: clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "deleted_at IS NULL"}}},
		DoUpdates: clause.Assignments(map[string]any{
			"score":      score,
			"updated_at": time.Now(),
		}),
	}
}

func videoRatingSummaryForContent(db *gorm.DB, contentID uuid.UUID, viewerID *uuid.UUID) (videoRatingSummary, error) {
	if !db.Migrator().HasTable(&model.PostRating{}) {
		return videoRatingSummary{}, nil
	}
	var aggregate struct {
		RatingScore float64 `gorm:"column:rating_score"`
		RatingCount int64   `gorm:"column:rating_count"`
	}
	if err := db.Model(&model.PostRating{}).
		Select("COALESCE(AVG(score), 0) AS rating_score, COUNT(*) AS rating_count").
		Where("content_id = ?", contentID).
		Scan(&aggregate).Error; err != nil {
		return videoRatingSummary{}, err
	}
	summary := videoRatingSummary{
		RatingScore: math.Round(aggregate.RatingScore*10) / 10,
		RatingCount: aggregate.RatingCount,
	}
	if viewerID == nil {
		return summary, nil
	}
	var rating model.PostRating
	if err := db.Where("user_id = ? AND content_id = ?", *viewerID, contentID).First(&rating).Error; err == nil {
		score := rating.Score
		summary.ViewerRating = &score
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return videoRatingSummary{}, err
	}
	return summary, nil
}

package handlers

import (
	"errors"
	"net/http"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func SetupOnboardingRoutes(router *gin.Engine, db *gorm.DB) {
	group := router.Group("/api/v1/auth/onboarding")
	group.Use(middleware.AuthMiddleware())
	group.POST("/complete", CompleteOnboardingHandler(db))

	recommendations := router.Group("/api/v1/feed/onboarding")
	recommendations.Use(middleware.AuthMiddleware())
	recommendations.GET("/recommendations", GetOnboardingFeedRecommendations(db))
}

type onboardingFeedRecommendationDTO struct {
	ID           string `json:"id"`
	FeedSourceID string `json:"feed_source_id"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int    `json:"sort_order"`
	Title        string `json:"title"`
	Category     string `json:"category"`
	RSSURL       string `json:"rss_url"`
	CoverURL     string `json:"cover_url"`
	HealthStatus string `json:"health_status"`
}

func GetOnboardingFeedRecommendations(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var recommendations []model.OnboardingFeedRecommendation
		if err := db.Preload("FeedSource").
			Where("enabled = ?", true).
			Joins("JOIN feed_sources ON feed_sources.id = onboarding_feed_recommendations.feed_source_id").
			Where("feed_sources.source_type = ? AND feed_sources.hidden = ?", "external_rss", false).
			Order("onboarding_feed_recommendations.sort_order ASC, onboarding_feed_recommendations.created_at ASC").
			Find(&recommendations).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load onboarding recommendations"})
			return
		}

		items := make([]onboardingFeedRecommendationDTO, 0, len(recommendations))
		for _, recommendation := range recommendations {
			if recommendation.FeedSource == nil {
				continue
			}
			source := recommendation.FeedSource
			items = append(items, onboardingFeedRecommendationDTO{
				ID: recommendation.ID.String(), FeedSourceID: source.ID.String(), Enabled: recommendation.Enabled,
				SortOrder: recommendation.SortOrder, Title: source.Title, Category: source.Category,
				RSSURL: source.RssURL, CoverURL: source.CoverURL, HealthStatus: source.HealthStatus,
			})
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}

func ListAdminOnboardingFeedRecommendations(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var recommendations []model.OnboardingFeedRecommendation
		if err := db.Preload("FeedSource").Order("sort_order ASC, created_at ASC").Find(&recommendations).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load onboarding recommendations"})
			return
		}
		items := make([]onboardingFeedRecommendationDTO, 0, len(recommendations))
		for _, recommendation := range recommendations {
			if recommendation.FeedSource == nil {
				continue
			}
			source := recommendation.FeedSource
			items = append(items, onboardingFeedRecommendationDTO{
				ID: recommendation.ID.String(), FeedSourceID: source.ID.String(), Enabled: recommendation.Enabled,
				SortOrder: recommendation.SortOrder, Title: source.Title, Category: source.Category,
				RSSURL: source.RssURL, CoverURL: source.CoverURL, HealthStatus: source.HealthStatus,
			})
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	}
}

type onboardingFeedRecommendationInput struct {
	FeedSourceID string `json:"feed_source_id" binding:"required"`
	Enabled      *bool  `json:"enabled"`
	SortOrder    *int   `json:"sort_order"`
}

func CreateAdminOnboardingFeedRecommendation(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input onboardingFeedRecommendationInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "feed_source_id is required"})
			return
		}
		sourceID, err := uuid.Parse(input.FeedSourceID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "feed_source_id must be a valid uuid"})
			return
		}
		var source model.FeedSource
		if err := db.Where("id = ? AND source_type = ?", sourceID, "external_rss").First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "external rss source not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load feed source"})
			return
		}
		var count int64
		db.Model(&model.OnboardingFeedRecommendation{}).Where("enabled = ?", true).Count(&count)
		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		if enabled && count >= 5 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "at most 5 onboarding recommendations can be enabled"})
			return
		}
		sortOrder := 0
		if input.SortOrder != nil {
			sortOrder = *input.SortOrder
		}
		recommendation := model.OnboardingFeedRecommendation{FeedSourceID: source.ID, Enabled: enabled, SortOrder: sortOrder}
		if err := db.Create(&recommendation).Error; err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "feed source is already recommended"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"item": recommendation})
	}
}

func UpdateAdminOnboardingFeedRecommendation(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input struct {
			Enabled   *bool `json:"enabled"`
			SortOrder *int  `json:"sort_order"`
		}
		if err := c.ShouldBindJSON(&input); err != nil || (input.Enabled == nil && input.SortOrder == nil) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "enabled or sort_order is required"})
			return
		}
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "recommendation id must be a valid uuid"})
			return
		}
		var recommendation model.OnboardingFeedRecommendation
		if err := db.First(&recommendation, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "onboarding recommendation not found"})
			return
		}
		if input.Enabled != nil && *input.Enabled && !recommendation.Enabled {
			var count int64
			db.Model(&model.OnboardingFeedRecommendation{}).Where("enabled = ?", true).Count(&count)
			if count >= 5 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "at most 5 onboarding recommendations can be enabled"})
				return
			}
		}
		updates := map[string]any{}
		if input.Enabled != nil {
			updates["enabled"] = *input.Enabled
		}
		if input.SortOrder != nil {
			updates["sort_order"] = *input.SortOrder
		}
		if err := db.Model(&recommendation).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update onboarding recommendation"})
			return
		}
		db.First(&recommendation, "id = ?", id)
		c.JSON(http.StatusOK, gin.H{"item": recommendation})
	}
}

func DeleteAdminOnboardingFeedRecommendation(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "recommendation id must be a valid uuid"})
			return
		}
		result := db.Delete(&model.OnboardingFeedRecommendation{}, "id = ?", id)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete onboarding recommendation"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "onboarding recommendation not found"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// CompleteOnboardingHandler godoc
// @Summary 完成首次登录引导
// @Description 将当前登录用户的 onboarding_completed_at 设置为当前 UTC 时间。
// @Tags auth
// @Produce json
// @Success 200 {object} OnboardingCompleteResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/auth/onboarding/complete [post]
func CompleteOnboardingHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		currentUser, ok := authctx.Current(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
			return
		}

		completedAt := time.Now().UTC()
		if err := db.Model(&model.User{}).
			Where("uuid = ?", currentUser.ID).
			Update("onboarding_completed_at", completedAt).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to complete onboarding"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"onboarding_completed_at": completedAt,
		})
	}
}

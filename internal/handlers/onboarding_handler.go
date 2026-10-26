package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// GetOnboardingFeedRecommendations godoc
// @Summary 获取入门订阅推荐
// @Tags feed
// @Produce json
// @Success 200 "成功"
// @Router /api/v1/feed/onboarding/recommendations [get]
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

// ListAdminOnboardingFeedRecommendations godoc
// @Summary 列出管理员入门推荐
// @Tags admin
// @Produce json
// @Success 200 "成功"
// @Router /api/v1/admin/feed/onboarding/recommendations [get]
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

const onboardingRecommendationLockKey int64 = 2026102602

var errOnboardingRecommendationLimit = errors.New("at most 5 onboarding recommendations can be enabled")

func lockOnboardingRecommendationCapacity(tx *gorm.DB) error {
	return tx.Exec("SELECT pg_advisory_xact_lock(?)", onboardingRecommendationLockKey).Error
}

func checkOnboardingRecommendationCapacity(tx *gorm.DB) error {
	var count int64
	if err := tx.Model(&model.OnboardingFeedRecommendation{}).Where("enabled = ?", true).Count(&count).Error; err != nil {
		return err
	}
	if count >= 5 {
		return errOnboardingRecommendationLimit
	}
	return nil
}

type onboardingFeedRecommendationInput struct {
	FeedSourceID string `json:"feed_source_id" binding:"required"`
	Enabled      *bool  `json:"enabled"`
	SortOrder    *int   `json:"sort_order"`
}

// CreateAdminOnboardingFeedRecommendation godoc
// @Summary 创建入门订阅推荐
// @Tags admin
// @Produce json
// @Accept json
// @Param input body onboardingFeedRecommendationInput true "请求参数"
// @Success 201 "成功"
// @Router /api/v1/admin/feed/onboarding/recommendations [post]
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
		enabled := true
		if input.Enabled != nil {
			enabled = *input.Enabled
		}
		sortOrder := 0
		if input.SortOrder != nil {
			sortOrder = *input.SortOrder
		}
		recommendation := model.OnboardingFeedRecommendation{FeedSourceID: source.ID, Enabled: enabled, SortOrder: sortOrder}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := lockOnboardingRecommendationCapacity(tx); err != nil {
				return err
			}
			if enabled {
				if err := checkOnboardingRecommendationCapacity(tx); err != nil {
					return err
				}
			}
			if err := tx.Create(&recommendation).Error; err != nil {
				return err
			}
			if !enabled {
				// GORM's default:true field tag otherwise overrides false on INSERT.
				if err := tx.Model(&recommendation).Update("enabled", false).Error; err != nil {
					return err
				}
				recommendation.Enabled = false
			}
			return nil
		}); err != nil {
			switch {
			case errors.Is(err, errOnboardingRecommendationLimit):
				c.JSON(http.StatusBadRequest, gin.H{"error": errOnboardingRecommendationLimit.Error()})
			case isDuplicateOnboardingRecommendation(err):
				c.JSON(http.StatusConflict, gin.H{"error": "feed source is already recommended"})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create onboarding recommendation"})
			}
			return
		}
		c.JSON(http.StatusCreated, gin.H{"item": recommendation})
	}
}

// UpdateAdminOnboardingFeedRecommendation godoc
// @Summary 更新入门订阅推荐
// @Tags admin
// @Produce json
// @Param id path string true "id"
// @Success 200 "成功"
// @Router /api/v1/admin/feed/onboarding/recommendations/{id} [patch]
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
		err = db.Transaction(func(tx *gorm.DB) error {
			if err := lockOnboardingRecommendationCapacity(tx); err != nil {
				return err
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&recommendation, "id = ?", id).Error; err != nil {
				return err
			}
			if input.Enabled != nil && *input.Enabled && !recommendation.Enabled {
				if err := checkOnboardingRecommendationCapacity(tx); err != nil {
					return err
				}
			}
			updates := map[string]any{}
			if input.Enabled != nil {
				updates["enabled"] = *input.Enabled
			}
			if input.SortOrder != nil {
				updates["sort_order"] = *input.SortOrder
			}
			if err := tx.Model(&recommendation).Updates(updates).Error; err != nil {
				return err
			}
			return tx.First(&recommendation, "id = ?", id).Error
		})
		if err != nil {
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				c.JSON(http.StatusNotFound, gin.H{"error": "onboarding recommendation not found"})
			case errors.Is(err, errOnboardingRecommendationLimit):
				c.JSON(http.StatusBadRequest, gin.H{"error": errOnboardingRecommendationLimit.Error()})
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update onboarding recommendation"})
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"item": recommendation})
	}
}

// DeleteAdminOnboardingFeedRecommendation godoc
// @Summary 删除入门订阅推荐
// @Tags admin
// @Produce json
// @Param id path string true "id"
// @Success 204 "成功"
// @Router /api/v1/admin/feed/onboarding/recommendations/{id} [delete]
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

func isDuplicateOnboardingRecommendation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") &&
		strings.Contains(message, "onboarding_feed_recommendations")
}

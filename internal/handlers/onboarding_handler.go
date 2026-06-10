package handlers

import (
	"net/http"
	"time"

	"atoman/internal/platform/authctx"
	"atoman/internal/middleware"
	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func SetupOnboardingRoutes(router *gin.Engine, db *gorm.DB) {
	group := router.Group("/api/v1/auth/onboarding")
	group.Use(middleware.AuthMiddleware())
	group.POST("/complete", CompleteOnboardingHandler(db))
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

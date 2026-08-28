package feed

import (
	"net/http"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// CheckSubscriptionHealth godoc
// @Summary 检查单个订阅健康状态
// @Description 使用受保护的 RSS 同步管线立即检查订阅源，并更新统一抓取状态。
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
func CheckSubscriptionHealth(service *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := authctx.Current(c)
		if !ok {
			httpx.Error(c, apperr.Unauthorized("Login required"))
			return
		}
		subscriptionID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "subscription id must be a valid uuid"))
			return
		}

		result, err := service.SyncSubscription(user, subscriptionID)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		c.JSON(http.StatusOK, healthCheckResponseFromSync(result))
	}
}

// CheckAllSubscriptionsHealth godoc
// @Summary 检查全部外部订阅健康状态
// @Description 使用受限并发的 RSS 同步管线检查当前用户的全部外部订阅源，并更新统一抓取状态。
// @Tags feed
// @Produce json
// @Success 200 {object} FeedHealthCheckListResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/health/check-all [post]
func CheckAllSubscriptionsHealth(service *Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := authctx.Current(c)
		if !ok {
			httpx.Error(c, apperr.Unauthorized("Login required"))
			return
		}

		summary, err := service.SyncAllSubscriptions(user)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		results := make([]FeedHealthCheckResponse, 0, len(summary.Results))
		for _, result := range summary.Results {
			results = append(results, healthCheckResponseFromSync(result))
		}
		c.JSON(http.StatusOK, FeedHealthCheckListResponse{
			CheckedCount: len(results),
			Results:      results,
		})
	}
}

func healthCheckResponseFromSync(result SubscriptionSyncResult) FeedHealthCheckResponse {
	checkedAt := result.SyncedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	response := FeedHealthCheckResponse{
		SubscriptionID: result.SubscriptionID.String(),
		HealthStatus:   "healthy",
		LastChecked:    checkedAt.Format(time.RFC3339),
	}
	if !result.Success {
		response.HealthStatus = "error"
		response.ErrorMessage = result.Error
	}
	return response
}

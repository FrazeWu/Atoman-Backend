package feed

import (
	"errors"
	"net/http"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	feedservice "atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const subscriptionDiagnosticsLimit = 20

// listSubscriptionDiagnostics godoc
// @Summary 获取订阅源抓取诊断
// @Description 返回当前用户订阅的外部 RSS 来源最近抓取失败与恢复记录。
// @Tags feed
// @Produce json
// @Param id path string true "订阅 UUID"
// @Success 200 {array} model.FeedSourceDiagnostic
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscriptions/{id}/diagnostics [get]
func (h *Handler) listSubscriptionDiagnostics(c *gin.Context) {
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
	diagnostics, err := h.service.ListSubscriptionRSSDiagnostics(user, subscriptionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, diagnostics)
}

func (s *Service) ListSubscriptionRSSDiagnostics(user authctx.CurrentUser, subscriptionID uuid.UUID) ([]model.FeedSourceDiagnostic, error) {
	var subscription model.Subscription
	err := s.db.Model(&model.Subscription{}).
		Select("subscriptions.id", "subscriptions.feed_source_id").
		Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
		Where("subscriptions.id = ? AND subscriptions.user_id = ? AND feed_sources.source_type = ?", subscriptionID, user.ID, "external_rss").
		First(&subscription).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperr.NotFound("feed.subscription_not_found", "Subscription not found")
	}
	if err != nil {
		return nil, err
	}

	var diagnostics []model.FeedSourceDiagnostic
	if err := s.db.Where("feed_source_id = ? AND kind IN ?", subscription.FeedSourceID, []string{
		feedservice.RSSFetchFailureDiagnosticKind,
		feedservice.RSSFetchRecoveredDiagnosticKind,
	}).Order("created_at DESC").Limit(subscriptionDiagnosticsLimit).Find(&diagnostics).Error; err != nil {
		return nil, err
	}
	return diagnostics, nil
}

package feed

import (
	"net/http"
	"strings"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// getSubscriptionHubTree godoc
// @Summary 获取订阅中心树
// @Description 返回按播客、视频、博客和 RSS 分离的分组与订阅叶子。
// @Tags feed
// @Produce json
// @Success 200 {object} SubscriptionHubTree
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscription-hub/tree [get]
func (h *Handler) getSubscriptionHubTree(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	tree, err := h.service.GetSubscriptionHubTree(user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, tree)
}

// getSubscriptionHubUpdates godoc
// @Summary 获取订阅中心更新流
// @Description 返回选中类型和分组的最新更新；可进一步按订阅叶子收窄。
// @Tags feed
// @Produce json
// @Param type query string true "订阅类型" Enums(podcast,video,blog,rss)
// @Param group_id query string false "分组 UUID"
// @Param membership_id query string false "订阅叶子 UUID"
// @Param page query int false "页码"
// @Param limit query int false "每页数量"
// @Success 200 {object} TimelineListResponseDTO
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/subscription-hub/updates [get]
func (h *Handler) getSubscriptionHubUpdates(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	query, err := subscriptionHubUpdatesQueryFromContext(c)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	items, total, err := h.service.GetSubscriptionHubUpdates(user, query)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	writeTimelineList(c, items, normalizedPageFromQuery(c), normalizedPageSizeFromQuery(c), total, time.Now().UTC())
}

func subscriptionHubUpdatesQueryFromContext(c *gin.Context) (SubscriptionHubUpdatesQuery, error) {
	query := SubscriptionHubUpdatesQuery{
		SubscriptionType: strings.TrimSpace(c.Query("type")),
		Page:             normalizedPageFromQuery(c),
		PageSize:         normalizedPageSizeFromQuery(c),
	}
	for rawName, target := range map[string]*uuid.UUID{
		"group_id":      &query.GroupID,
		"membership_id": &query.MembershipID,
	} {
		rawValue := strings.TrimSpace(c.Query(rawName))
		if rawValue == "" {
			continue
		}
		parsed, err := uuid.Parse(rawValue)
		if err != nil {
			return SubscriptionHubUpdatesQuery{}, apperr.BadRequest("validation.invalid_request", rawName+" must be a valid uuid")
		}
		*target = parsed
	}
	if query.SubscriptionType == "" {
		return SubscriptionHubUpdatesQuery{}, apperr.BadRequest("validation.invalid_request", "type is required")
	}
	return query, nil
}

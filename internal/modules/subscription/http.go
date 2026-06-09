package subscription

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.POST("/subscriptions", h.createSubscription)
	group.POST("/subscription-groups", h.createSubscriptionGroup)
}

func (h *Handler) createSubscription(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var req CreateSubscriptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}

	subscription, err := h.service.CreateSubscription(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, subscription)
}

func (h *Handler) createSubscriptionGroup(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var req CreateSubscriptionGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}

	group, err := h.service.CreateSubscriptionGroup(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, group)
}

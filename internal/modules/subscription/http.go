package subscription

import (
	"net/http"

	"atoman/internal/middleware"
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
	protected := group.Group("")
	protected.Use(requireCurrentUser())
	protected.POST("/subscriptions", h.createSubscription)
	protected.POST("/subscription-groups", h.createSubscriptionGroup)
}

func requireCurrentUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := authctx.Current(c); ok {
			c.Next()
			return
		}
		middleware.AuthMiddleware()(c)
	}
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

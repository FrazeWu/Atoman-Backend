package debate

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
	group.POST("/debates", h.createDebate)
}

func (h *Handler) createDebate(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateDebateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	debate, err := h.service.CreateDebate(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, debate)
}

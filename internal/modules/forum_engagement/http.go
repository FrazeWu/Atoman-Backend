package forum_engagement

import (
	"net/http"

	"atoman/internal/modules/comment"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.POST("/topics/:topicID/like", h.toggleTopicLike)
	group.POST("/topics/:topicID/bookmark", h.toggleTopicBookmark)
	group.POST("/replies/:replyID/like", h.toggleReplyLike)
}

func (h *Handler) toggleTopicLike(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	topicID, err := uuid.Parse(c.Param("topicID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "topicID must be a valid uuid"))
		return
	}
	state, err := h.service.ToggleTopicLike(user, topicID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, state)
}

func (h *Handler) toggleTopicBookmark(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	topicID, err := uuid.Parse(c.Param("topicID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "topicID must be a valid uuid"))
		return
	}
	state, err := h.service.ToggleTopicBookmark(user, topicID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, state)
}

func (h *Handler) toggleReplyLike(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	replyID, err := uuid.Parse(c.Param("replyID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "replyID must be a valid uuid"))
		return
	}
	state, err := h.service.ToggleReplyLike(user, replyID)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, state)
}

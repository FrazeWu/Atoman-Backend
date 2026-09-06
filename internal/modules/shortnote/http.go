package shortnote

import (
	"net/http"

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
	group.GET("", h.list)
	group.GET("/:id", h.get)
	group.POST("", h.create)
	group.PUT("/:id", h.update)
	group.DELETE("/:id", h.delete)
	group.POST("/:id/like", h.like)
	group.DELETE("/:id/like", h.unlike)
	group.PUT("/:id/vote", h.setVote)
	group.DELETE("/:id/vote", h.clearVote)
}

// list godoc
// @Summary 获取短笺列表
// @Description 返回公开短笺，可按作者筛选。
// @Tags shortnote
// @Produce json
// @Param user_id query string false "用户 UUID"
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {array} NoteDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/short-notes [get]
func (h *Handler) list(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	var authorID *uuid.UUID
	if rawUserID := c.Query("user_id"); rawUserID != "" {
		parsedUserID, err := uuid.Parse(rawUserID)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "user_id must be a valid uuid"))
			return
		}
		authorID = &parsedUserID
	}
	user, _ := authctx.Current(c)
	items, total, err := h.service.List(page, pageSize, user.ID, authorID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

func (h *Handler) get(c *gin.Context) {
	user, _ := authctx.Current(c)
	note, err := h.service.Get(noteID(c), user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, note)
}

func (h *Handler) create(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input noteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	note, err := h.service.Create(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, note)
}

func (h *Handler) update(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input noteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	note, err := h.service.Update(user, noteID(c), input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, note)
}

func (h *Handler) delete(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if err := h.service.Delete(user, noteID(c)); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) like(c *gin.Context)   { h.toggleLike(c, true) }
func (h *Handler) unlike(c *gin.Context) { h.toggleLike(c, false) }

func (h *Handler) toggleLike(c *gin.Context, liked bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if err := h.service.ToggleLike(user, noteID(c), liked); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "ok"})
}

func (h *Handler) setVote(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var input voteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	note, err := h.service.SetVote(user, noteID(c), input.Direction)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, note)
}

func (h *Handler) clearVote(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	note, err := h.service.SetVote(user, noteID(c), "none")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, note)
}

func noteID(c *gin.Context) uuid.UUID {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil
	}
	return id
}

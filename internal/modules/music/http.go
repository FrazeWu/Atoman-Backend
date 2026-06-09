package music

import (
	"errors"
	"io"
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.POST("/edits", h.submitEdit)
	group.GET("/edits/:editId", h.getEdit)
	group.POST("/edits/:editId/votes", h.voteEdit)
	group.POST("/edits/:editId/approve", h.approveEdit)
	group.POST("/edits/:editId/reject", h.rejectEdit)
	group.POST("/edits/:editId/cancel", h.cancelEdit)
}

func (h *Handler) submitEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	var req SubmitEditRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.SubmitEdit(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, edit)
}

func (h *Handler) getEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("music.edit_not_found", "Edit not found"))
			return
		}
		httpx.Error(c, err)
		return
	}
	if edit.SubmittedBy != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		httpx.Error(c, apperr.Forbidden("music.edit_forbidden", "You cannot view this edit"))
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) voteEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req VoteRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.Vote(user, editID, req); err != nil {
		httpx.Error(c, err)
		return
	}

	edit, err := h.service.repo.GetEdit(editID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) approveEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.ApproveEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) rejectEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.RejectEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func (h *Handler) cancelEdit(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}

	editID, err := parseEditID(c.Param("editId"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	var req DecisionRequest
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	edit, err := h.service.CancelEdit(user, editID, req.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edit)
}

func parseEditID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", "editId must be a valid UUID")
	}
	return id, nil
}

func bindJSON(c *gin.Context, dst any) error {
	if err := c.ShouldBindJSON(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return apperr.BadRequest("validation.invalid_request", "request body must be valid JSON")
	}
	return nil
}

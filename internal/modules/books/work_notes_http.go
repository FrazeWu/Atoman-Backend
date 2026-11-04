package books

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) listWorkNotes(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	filter := WorkNotesFilter{Kind: strings.ToLower(strings.TrimSpace(c.DefaultQuery("kind", "all")))}
	if raw := strings.TrimSpace(c.Query("revision_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil || parsed == uuid.Nil {
			httpx.Error(c, apperr.BadRequest("books.invalid_revision", "Invalid revision ID"))
			return
		}
		filter.RevisionID = parsed
	}
	if raw := c.Query("limit"); raw != "" {
		number, err := strconv.Atoi(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("books.invalid_pagination", "Invalid limit"))
			return
		}
		filter.Limit = number
	}
	if raw := c.Query("offset"); raw != "" {
		number, err := strconv.Atoi(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("books.invalid_pagination", "Invalid offset"))
			return
		}
		filter.Offset = number
	}
	result, err := h.service.ListWorkNotes(c.Request.Context(), user, workID, filter)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

package books

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

func (h *Handler) previewNoteCollection(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	var input PreviewNoteCollectionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid preview draft"))
		return
	}
	result, err := h.service.PreviewNoteCollection(c.Request.Context(), user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) listNoteShareHistory(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "collectionId")
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	result, err := h.service.ListNoteShareHistory(c.Request.Context(), user, id, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) getNoteShareHistoryVersion(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "collectionId")
	if !ok {
		return
	}
	version, err := strconv.ParseInt(c.Param("version"), 10, 64)
	if err != nil || version < 1 {
		httpx.Error(c, apperr.BadRequest("books.invalid_note_version", "Positive version required"))
		return
	}
	result, err := h.service.GetNoteShareHistoryVersion(c.Request.Context(), user, id, version)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

package books

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) listReadingMarkTrash(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	result, err := h.service.ListBookReadingMarkTrash(c.Request.Context(), user, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) restoreReadingMark(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	revisionID, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	markID, ok := parseCatalogID(c, "markId")
	if !ok {
		return
	}
	var request struct {
		BaseVersion int64 `json:"base_version"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid restore request"))
		return
	}
	result, err := h.service.RestoreBookReadingMark(c.Request.Context(), user, revisionID, markID, request.BaseVersion)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) eraseReadingMark(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	revisionID, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	markID, ok := parseCatalogID(c, "markId")
	if !ok {
		return
	}
	var request struct {
		BaseVersion  int64 `json:"base_version"`
		ConfirmErase bool  `json:"confirm_erase"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.ConfirmErase {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Permanent erasure needs explicit confirmation"))
		return
	}
	result, err := h.service.PermanentlyEraseBookReadingMark(c.Request.Context(), user, revisionID, markID, request.BaseVersion)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

package books

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http"
	"strings"
)

func noteCollectionHTTPUser(c *gin.Context) (string, bool) {
	c.Header("Cache-Control", "private, no-store")
	_, ok := currentBookUser(c)
	if !ok {
		return "", false
	}
	return "ok", true
}
func (h *Handler) saveNoteCollection(c *gin.Context) {
	if _, ok := noteCollectionHTTPUser(c); !ok {
		return
	}
	user, _ := currentBookUser(c)
	id, ok := parseCatalogID(c, "collectionId")
	if !ok {
		return
	}
	var input SaveNoteCollectionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid collection draft"))
		return
	}
	result, err := h.service.SaveNoteCollection(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) getNoteCollection(c *gin.Context) {
	if _, ok := noteCollectionHTTPUser(c); !ok {
		return
	}
	user, _ := currentBookUser(c)
	id, ok := parseCatalogID(c, "collectionId")
	if !ok {
		return
	}
	result, err := h.service.GetNoteCollection(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) listNoteCollections(c *gin.Context) {
	if _, ok := noteCollectionHTTPUser(c); !ok {
		return
	}
	user, _ := currentBookUser(c)
	var workID uuid.UUID
	if raw := strings.TrimSpace(c.Query("work_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil || parsed == uuid.Nil {
			httpx.Error(c, apperr.BadRequest("books.invalid_work", "Invalid work ID"))
			return
		}
		workID = parsed
	}
	result, err := h.service.ListNoteCollections(c.Request.Context(), user, workID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) publishNoteCollection(c *gin.Context) {
	if _, ok := noteCollectionHTTPUser(c); !ok {
		return
	}
	user, _ := currentBookUser(c)
	id, ok := parseCatalogID(c, "collectionId")
	if !ok {
		return
	}
	var input PublishNoteCollectionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid publication"))
		return
	}
	result, err := h.service.PublishNoteCollection(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) revokeNoteCollection(c *gin.Context) {
	if _, ok := noteCollectionHTTPUser(c); !ok {
		return
	}
	user, _ := currentBookUser(c)
	id, ok := parseCatalogID(c, "collectionId")
	if !ok {
		return
	}
	var input struct {
		BaseVersion int64 `json:"base_version"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid revoke request"))
		return
	}
	result, err := h.service.RevokeNoteCollection(c.Request.Context(), user, id, input.BaseVersion)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}
func (h *Handler) getSharedNoteCollection(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	result, err := h.service.GetSharedNoteCollection(c.Request.Context(), c.Param("shareKey"))
	if err != nil {
		c.Header("X-Robots-Tag", "noindex, nofollow")
		httpx.Error(c, err)
		return
	}
	if result.Visibility == "link" {
		c.Header("X-Robots-Tag", "noindex, nofollow")
	}
	httpx.OK(c, http.StatusOK, result)
}

func (h *Handler) searchPublicNoteShares(c *gin.Context) {
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	if limit > 50 {
		limit = 50
	}
	result, err := h.service.SearchPublicNoteShares(c.Request.Context(), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

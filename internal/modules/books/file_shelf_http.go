package books

import (
	"atoman/internal/platform/authctx"
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) listEditionFiles(c *gin.Context) {
	id, ok := parseCatalogID(c, "editionId")
	if !ok {
		return
	}
	viewer, _ := authctx.Current(c)
	items, err := h.service.ListBookEditionFilesForViewer(c.Request.Context(), id, viewer.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items})
}

func (h *Handler) setEditionBestFile(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	editionID, ok := parseCatalogID(c, "editionId")
	if !ok {
		return
	}
	var input struct {
		RevisionID uuid.UUID `json:"revision_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid file revision"))
		return
	}
	result, err := h.service.SetBookEditionBestFile(c.Request.Context(), user, editionID, input.RevisionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

func (h *Handler) listMyFileShelf(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items, total, err := h.service.ListBookFileShelf(c.Request.Context(), user, c.Query("status"), c.DefaultQuery("sort", "activity"), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (h *Handler) saveMyFileShelf(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	revisionID, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	var input SaveBookFileShelfInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid shelf update"))
		return
	}
	result, err := h.service.SaveBookFileShelf(c.Request.Context(), user, revisionID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

func (h *Handler) deleteMyFileShelf(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	revisionID, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	if err := h.service.DeleteBookFileShelf(c.Request.Context(), user, revisionID); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

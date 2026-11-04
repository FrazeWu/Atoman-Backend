package books

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) noteShareImpact(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "itemId")
	if !ok {
		return
	}
	result, err := h.service.NoteShareImpact(c.Request.Context(), user, BookNoteReference{Kind: c.Param("kind"), ID: id})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

var _ = apperr.Unauthorized

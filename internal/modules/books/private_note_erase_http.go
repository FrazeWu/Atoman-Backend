package books

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) eraseBookPrivateNote(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "noteId")
	if !ok {
		return
	}
	var request struct {
		BaseVersion  int64 `json:"base_version"`
		ConfirmErase bool  `json:"confirm_erase"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.ConfirmErase {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Permanent erasure requires explicit confirmation"))
		return
	}
	result, err := h.service.PermanentlyEraseBookPrivateNote(c.Request.Context(), user, id, request.BaseVersion)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

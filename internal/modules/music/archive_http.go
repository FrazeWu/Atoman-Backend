package music

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) archiveMusicEntry(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	entityType := c.Param("entityType")
	id, err := parseMusicID(c.Param("entityId"), "entityId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var input struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.ArchiveMusicEntry(user, entityType, id, input.Reason); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

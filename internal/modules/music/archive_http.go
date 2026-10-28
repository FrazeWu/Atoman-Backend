package music

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *Handler) archiveMusicEntry(c *gin.Context) { h.mutateMusicArchive(c, false) }
func (h *Handler) restoreMusicEntry(c *gin.Context) { h.mutateMusicArchive(c, true) }

func (h *Handler) mutateMusicArchive(c *gin.Context, restoring bool) {
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
	if restoring {
		err = h.service.RestoreArchivedMusicEntry(user, entityType, id, input.Reason)
	} else {
		err = h.service.ArchiveMusicEntry(user, entityType, id, input.Reason)
	}
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) listArchivedMusicEntries(c *gin.Context) {
	user, ok := currentMusicUser(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	items, total, err := h.service.ListArchivedMusicEntries(user, page, pageSize, c.DefaultQuery("entity_type", "all"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, items, page, pageSize, total)
}

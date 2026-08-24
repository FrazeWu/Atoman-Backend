package studio

import (
	"net/http"

	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

func (h *Handler) createUnifiedCollection(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	var input CreateCollectionInput
	if !bindJSON(c, &input) {
		return
	}
	collection, err := h.service.CreateUnifiedCollection(user, input)
	respond(c, http.StatusCreated, collection, err)
}

func (h *Handler) updateUnifiedCollection(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input UpdateCollectionInput
	if !bindJSON(c, &input) {
		return
	}
	collection, err := h.service.UpdateUnifiedCollection(user, id, input)
	respond(c, http.StatusOK, collection, err)
}

// listUnifiedCollectionContents godoc
// @Summary 获取统一合集成员
// @Tags studio
// @Produce json
// @Param id path string true "合集 UUID"
// @Success 200 {array} StudioCollectionContentItem
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Router /api/v1/studio/collections/{id}/contents [get]
func (h *Handler) listUnifiedCollectionContents(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	items, err := h.service.ListUnifiedCollectionContents(user, id)
	respond(c, http.StatusOK, items, err)
}

func (h *Handler) reorderUnifiedCollectionContents(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	var input reorderCollectionContentsInput
	if !bindJSON(c, &input) {
		return
	}
	if err := h.service.ReorderUnifiedCollectionContents(user, id, input.ContentIDs); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"reordered": true})
}

func (h *Handler) deleteUnifiedCollection(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	id, ok := uuidParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteUnifiedCollection(user, id); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

package books

import (
	"net/http"

	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// getBookRevisionContent godoc
// @Summary 读取经过安全检查且未下架的电子书修订
// @Description 正文通过后端流式读取，不返回存储地址，禁止缓存。
// @Tags books-reading
// @Produce application/octet-stream
// @Param revisionId path string true "文件修订 UUID"
// @Success 200 {file} binary
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/asset-revisions/{revisionId}/content [get]
func (h *Handler) getBookRevisionContent(c *gin.Context) {
	id, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	content, err := h.service.OpenBookRevision(c.Request.Context(), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer content.Body.Close()
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Disposition", contentDispositionForBook(content.Revision.OriginalFilename))
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, content.Size, content.ContentType, content.Body, nil)
}

// listBookAssetRevisions godoc
// @Summary 获取公共电子书的可读文件修订
// @Tags books-reading
// @Produce json
// @Param assetId path string true "电子书 UUID"
// @Success 200 {object} map[string]interface{}
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/assets/{assetId}/revisions [get]
func (h *Handler) listBookAssetRevisions(c *gin.Context) {
	id, ok := parseCatalogID(c, "assetId")
	if !ok {
		return
	}
	items, err := h.service.ListBookAssetRevisions(c.Request.Context(), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items})
}

package books

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

// listBookWikiMerges godoc
// @Summary 获取实体合并日志，不返回关系快照或私人数据
// @Tags books-wiki
// @Produce json
// @Param entityType path string true "work、person 或 edition"
// @Param entityId path string true "实体 UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/wiki/{entityType}/{entityId}/merges [get]
func (h *Handler) listBookWikiMerges(c *gin.Context) {
	id, ok := parseCatalogID(c, "entityId")
	if !ok {
		return
	}
	kind := c.Param("entityType")
	if _, _, err := h.service.ListBookWikiRevisions(c.Request.Context(), kind, id, 1, 0); err != nil {
		httpx.Error(c, err)
		return
	}
	rows := []model.BookWikiMerge{}
	err := h.service.db.WithContext(c.Request.Context()).Where("(source_id = ? OR target_id = ?) AND EXISTS (SELECT 1 FROM revisions r WHERE r.id = book_wiki_merges.source_revision_id AND r.content_type = ?)", id, id, "book_"+kind).Order("created_at DESC").Limit(50).Find(&rows).Error
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": rows})
}

// getBookWikiMetadata godoc
// @Summary 获取作者或出版版本及其当前修订，解析永久重定向
// @Tags books-wiki
// @Produce json
// @Param entityType path string true "person 或 edition"
// @Param entityId path string true "实体 UUID"
// @Success 200 {object} BookWikiMetadata
// @Router /api/v1/books/wiki/{entityType}/{entityId} [get]
func (h *Handler) getBookWikiMetadata(c *gin.Context) {
	id, ok := parseCatalogID(c, "entityId")
	if !ok {
		return
	}
	result, err := h.service.GetBookWikiMetadata(c.Request.Context(), c.Param("entityType"), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// patchBookMetadata godoc
// @Summary 即时编辑作者或出版版本，基础修订过期返回 409
// @Tags books-wiki
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param entityType path string true "person 或 edition"
// @Param entityId path string true "实体 UUID"
// @Param input body PatchBookMetadataInput true "基础修订和可编辑字段"
// @Success 200 {object} BookWikiMetadata
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/wiki/{entityType}/{entityId} [patch]
func (h *Handler) patchBookMetadata(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "entityId")
	if !ok {
		return
	}
	var input PatchBookMetadataInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid metadata patch"))
		return
	}
	result, err := h.service.PatchBookMetadata(c.Request.Context(), user, c.Param("entityType"), id, input, "edit")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

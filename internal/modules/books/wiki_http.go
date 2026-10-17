package books

import (
	"atoman/internal/model"
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// patchBookWork godoc
// @Summary 即时编辑作品，使用基础修订防止并发覆盖
// @Tags books-wiki
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param input body PatchBookWorkInput true "变更字段及基础修订"
// @Success 200 {object} BookWorkDetail
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/works/{workId} [patch]
func (h *Handler) patchBookWork(c *gin.Context) {
	user, id, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	var input PatchBookWorkInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid work patch"))
		return
	}
	work, err := h.service.PatchBookWork(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, work)
}

// listBookWikiRevisions godoc
// @Summary 获取书目修订、快照和真实字段差异
// @Tags books-wiki
// @Produce json
// @Param entityType path string true "work、edition 或 person"
// @Param entityId path string true "条目 UUID"
// @Param limit query int false "每页数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/wiki/{entityType}/{entityId}/revisions [get]
func (h *Handler) listBookWikiRevisions(c *gin.Context) {
	id, ok := parseCatalogID(c, "entityId")
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items, total, err := h.service.ListBookWikiRevisions(c.Request.Context(), c.Param("entityType"), id, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total})
}

// revertBookWikiRevision godoc
// @Summary 回退作品元数据，保留完整历史并执行并发检查
// @Tags books-wiki
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param revisionId path string true "目标作品修订 UUID"
// @Param input body RevertBookWikiInput true "当前基础修订"
// @Success 200 {object} BookWorkDetail
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/wiki/revisions/{revisionId}/revert [post]
func (h *Handler) revertBookWikiRevision(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	var input RevertBookWikiInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid revert request"))
		return
	}
	var target model.Revision
	if err := h.service.db.Where("id = ? AND content_type IN ? AND status = 'approved'", id, []string{"book_person", "book_edition"}).First(&target).Error; err == nil {
		result, err := h.service.RevertBookMetadata(c.Request.Context(), user, target, input)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.OK(c, http.StatusOK, result)
		return
	}
	work, err := h.service.RevertBookWikiRevision(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, work)
}

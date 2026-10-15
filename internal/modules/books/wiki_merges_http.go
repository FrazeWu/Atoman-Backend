package books

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// previewBookWorkMerge godoc
// @Summary 预览作品合并（仅返回数量，不公开私人记录）
// @Tags books-wiki
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body PreviewBookWorkMergeInput true "来源及目标作品"
// @Success 200 {object} BookWorkMergePreview
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/wiki/merges/preview [post]
func (h *Handler) previewBookWorkMerge(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	var input PreviewBookWorkMergeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid merge preview"))
		return
	}
	preview, err := h.service.PreviewBookWorkMerge(c.Request.Context(), user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, preview)
}

// mergeBookWorks godoc
// @Summary 合并作品并保留可撤销的事务快照
// @Description 重复评分、书架、默认资源和个人选择保留目标记录；电子书不去重。预览过期返回 409。
// @Tags books-wiki
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body MergeBookWorksInput true "预览令牌及双方基础修订"
// @Success 201 {object} model.BookWikiMerge
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/wiki/merges [post]
func (h *Handler) mergeBookWorks(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	var input MergeBookWorksInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid merge request"))
		return
	}
	merge, err := h.service.MergeBookWorks(c.Request.Context(), user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, merge)
}

// revertBookWorkMerge godoc
// @Summary 撤销作品合并；后续数据有变化则拒绝覆盖
// @Tags books-wiki
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param mergeId path string true "合并 UUID"
// @Success 200 {object} model.BookWikiMerge
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/wiki/merges/{mergeId}/revert [post]
func (h *Handler) revertBookWorkMerge(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "mergeId")
	if !ok {
		return
	}
	merge, err := h.service.RevertBookWorkMerge(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, merge)
}

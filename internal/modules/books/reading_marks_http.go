package books

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// listBookRevisionMarks godoc
// @Summary 读取本人的修订级书签、划线、批注及无损合并显示分组
// @Tags books-reading
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param revisionId path string true "文件修订 UUID"
// @Success 200 {object} BookReadingMarksResult
// @Router /api/v1/books/me/reading-marks/{revisionId} [get]
func (h *Handler) listBookRevisionMarks(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	revisionID, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	result, err := h.service.ListBookReadingMarks(c.Request.Context(), user, revisionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// saveBookRevisionMark godoc
// @Summary 幂等创建或修改阅读标记，版本冲突不覆盖
// @Description markId 由客户端生成；创建 base_version=0，更新使用当前 version。EPUB 使用两个文本点 CFI，PDF 使用页码和归一化矩形，TXT 使用 UTF-16 半开区间偏移。
// @Tags books-reading
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param revisionId path string true "文件修订 UUID"
// @Param markId path string true "客户端生成标记 UUID"
// @Param input body SaveBookReadingMarkInput true "完整标记及基础版本"
// @Success 200 {object} BookReadingMarkDTO
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/reading-marks/{revisionId}/{markId} [put]
func (h *Handler) saveBookRevisionMark(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	revisionID, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	markID, ok := parseCatalogID(c, "markId")
	if !ok {
		return
	}
	var input SaveBookReadingMarkInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid reading mark"))
		return
	}
	result, err := h.service.SaveBookReadingMark(c.Request.Context(), user, revisionID, markID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// deleteBookRevisionMark godoc
// @Summary 删除阅读标记并保留带版本的删除记录，防止离线旧请求复活
// @Tags books-reading
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param revisionId path string true "文件修订 UUID"
// @Param markId path string true "标记 UUID"
// @Param base_version query int true "标记当前版本"
// @Success 200 {object} BookReadingMarkTombstone
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/reading-marks/{revisionId}/{markId} [delete]
func (h *Handler) deleteBookRevisionMark(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	revisionID, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	markID, ok := parseCatalogID(c, "markId")
	if !ok {
		return
	}
	version, err := strconv.ParseInt(c.Query("base_version"), 10, 64)
	if err != nil || version < 1 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "base_version must be a positive integer"))
		return
	}
	result, err := h.service.DeleteBookReadingMark(c.Request.Context(), user, revisionID, markID, version)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

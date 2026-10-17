package books

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// listBookLibraryEntries godoc
// @Summary 获取本人作品级书架，按状态和私人标签筛选
// @Tags books-library
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param status query string false "书架状态"
// @Param tag query string false "私人标签"
// @Param limit query int false "分页大小"
// @Param offset query int false "分页偏移"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/me/library [get]
func (h *Handler) listBookLibraryEntries(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items, total, err := h.service.ListBookLibraryEntries(c.Request.Context(), user, c.Query("status"), c.Query("tag"), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

// saveBookLibraryEntry godoc
// @Summary 保存作品级书架；更新需条目 ID 和版本，避免合并后旧请求改错条目
// @Tags books-library
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param input body SaveBookLibraryEntryInput true "状态、私人标签和基础版本"
// @Success 200 {object} BookLibraryEntryDTO
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/library/works/{workId} [put]
func (h *Handler) saveBookLibraryEntry(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, id, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	var input SaveBookLibraryEntryInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid library entry"))
		return
	}
	result, err := h.service.SaveBookLibraryEntry(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

func bookPrivateBaseVersion(c *gin.Context) (int64, bool) {
	version, err := strconv.ParseInt(c.Query("base_version"), 10, 64)
	if err != nil || version < 1 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "base_version must be a positive integer"))
		return 0, false
	}
	return version, true
}

// deleteBookLibraryEntry godoc
// @Summary 移出书架，不删除阅读进度、文件选择或私人笔记
// @Tags books-library
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param entryId path string true "书架条目 UUID"
// @Param base_version query int true "当前版本"
// @Success 200 {object} BookReadingMarkTombstone
// @Router /api/v1/books/me/library/entries/{entryId} [delete]
func (h *Handler) deleteBookLibraryEntry(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "entryId")
	if !ok {
		return
	}
	version, ok := bookPrivateBaseVersion(c)
	if !ok {
		return
	}
	result, err := h.service.DeleteBookLibraryEntry(c.Request.Context(), user, id, version)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// listBookPrivateNotes godoc
// @Summary 获取本人跨修订笔记，支持作品、标签、关键词和 30 天回收站
// @Tags books-notes
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param work_id query string false "作品 UUID，包含合并别名"
// @Param tag query string false "私人标签"
// @Param q query string false "标题或正文搜索"
// @Param trash query bool false "是否读取回收站"
// @Param limit query int false "分页大小"
// @Param offset query int false "分页偏移"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/me/private-notes [get]
func (h *Handler) listBookPrivateNotes(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	filter := BookPrivateNoteFilter{Tag: c.Query("tag"), Query: c.Query("q"), Limit: limit, Offset: offset}
	if value := c.Query("work_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid work_id"))
			return
		}
		filter.WorkID = id
	}
	if value := c.Query("trash"); value != "" {
		trash, err := strconv.ParseBool(value)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid trash flag"))
			return
		}
		filter.Trash = trash
	}
	items, total, err := h.service.ListBookPrivateNotes(c.Request.Context(), user, filter)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

// getBookPrivateNote godoc
// @Summary 读取本人的私人笔记；过期回收站内容返回 410
// @Tags books-notes
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param noteId path string true "私人笔记 UUID"
// @Success 200 {object} BookPrivateNoteDTO
// @Failure 410 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/private-notes/{noteId} [get]
func (h *Handler) getBookPrivateNote(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "noteId")
	if !ok {
		return
	}
	result, err := h.service.GetBookPrivateNote(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// saveBookPrivateNote godoc
// @Summary 幂等创建或编辑私人笔记，不发布正文或私人标签
// @Tags books-notes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param noteId path string true "客户端生成笔记 UUID"
// @Param input body SaveBookPrivateNoteInput true "作品、标题、正文、标签和基础版本"
// @Success 200 {object} BookPrivateNoteDTO
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/private-notes/{noteId} [put]
func (h *Handler) saveBookPrivateNote(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "noteId")
	if !ok {
		return
	}
	var input SaveBookPrivateNoteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid private note"))
		return
	}
	result, err := h.service.SaveBookPrivateNote(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// deleteBookPrivateNote godoc
// @Summary 将笔记移入 30 天回收站
// @Tags books-notes
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param noteId path string true "私人笔记 UUID"
// @Param base_version query int true "当前版本"
// @Success 200 {object} BookReadingMarkTombstone
// @Router /api/v1/books/me/private-notes/{noteId} [delete]
func (h *Handler) deleteBookPrivateNote(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "noteId")
	if !ok {
		return
	}
	version, ok := bookPrivateBaseVersion(c)
	if !ok {
		return
	}
	result, err := h.service.DeleteBookPrivateNote(c.Request.Context(), user, id, version)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

type RestoreBookPrivateNoteInput struct {
	BaseVersion int64 `json:"base_version"`
}

// restoreBookPrivateNote godoc
// @Summary 恢复 30 天内删除的笔记，过期正文不可恢复
// @Tags books-notes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param noteId path string true "私人笔记 UUID"
// @Param input body RestoreBookPrivateNoteInput true "删除记录当前版本"
// @Success 200 {object} BookPrivateNoteDTO
// @Failure 410 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/private-notes/{noteId}/restore [post]
func (h *Handler) restoreBookPrivateNote(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "noteId")
	if !ok {
		return
	}
	var input RestoreBookPrivateNoteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid restore request"))
		return
	}
	result, err := h.service.RestoreBookPrivateNote(c.Request.Context(), user, id, input.BaseVersion)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// exportBookPrivateNotes godoc
// @Summary 导出本人的全部有效笔记，不含回收站或他人数据
// @Tags books-notes
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} BookNoteExport
// @Router /api/v1/books/me/private-notes/export [get]
func (h *Handler) exportBookPrivateNotes(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	result, err := h.service.ExportBookPrivateNotes(c.Request.Context(), user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// importBookPrivateNotes godoc
// @Summary 幂等导入 JSON 笔记；保留已有数据并报告冲突，错误批次原子回滚
// @Tags books-notes
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body ImportBookPrivateNotesInput true "版本 1、请求 UUID 和最多 500 条笔记；上限 8 MiB"
// @Success 200 {object} BookNoteImportResult
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/private-notes/import [post]
func (h *Handler) importBookPrivateNotes(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024*1024)
	var input ImportBookPrivateNotesInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_note_import", "Invalid note import or body exceeds 8 MiB"))
		return
	}
	result, err := h.service.ImportBookPrivateNotes(c.Request.Context(), user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

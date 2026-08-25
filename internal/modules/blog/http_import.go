package blog

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type markdownImportConfirmInput struct {
	ContentID uuid.UUID `json:"content_id" binding:"required"`
}

// getMarkdownImport godoc
// @Summary 查询 Markdown 导入预览
// @Tags blog
// @Produce json
// @Param id path string true "导入 UUID"
// @Success 200 {object} MarkdownImportDetails
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/imports/markdown/{id} [get]
func (h *Handler) getMarkdownImport(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	importID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("blog.import_invalid_id", "Markdown import ID is invalid"))
		return
	}
	details, err := h.service.GetMarkdownImport(user, importID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, details)
}

// confirmMarkdownImport godoc
// @Summary 确认 Markdown 导入
// @Description 将已由当前作者创建的 canonical 草稿关联到导入任务。
// @Tags blog
// @Accept json
// @Produce json
// @Param id path string true "导入 UUID"
// @Param input body markdownImportConfirmInput true "确认内容"
// @Success 200 {object} MarkdownImportDetails
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/imports/markdown/{id}/confirm [post]
func (h *Handler) confirmMarkdownImport(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	importID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("blog.import_invalid_id", "Markdown import ID is invalid"))
		return
	}
	var input markdownImportConfirmInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "content_id is required"))
		return
	}
	details, err := h.service.ConfirmMarkdownImport(user, importID, input.ContentID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, details)
}

func (h *Handler) previewMarkdownImport(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		httpx.Error(c, apperr.BadRequest("blog.import_invalid_file", "Markdown file is required"))
		return
	}
	defer file.Close()
	name := strings.TrimSpace(header.Filename)
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".txt":
	default:
		httpx.Error(c, apperr.BadRequest("blog.import_invalid_file", "File must be Markdown text"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBlogMarkdownImportBytes+1))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	preview, err := h.service.PreviewMarkdownImport(user, name, raw)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, preview)
}

package books

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// createBookResource godoc
// @Summary 为作品新增独立公共电子书（不要求出版版本）
// @Tags books-assets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param input body CreateBookResourceInput true "电子书格式与语言"
// @Success 201 {object} model.BookAsset
// @Router /api/v1/books/works/{workId}/assets [post]
func (h *Handler) createBookResource(c *gin.Context) {
	user, id, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	var input CreateBookResourceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid ebook metadata"))
		return
	}
	asset, err := h.service.CreateBookResource(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, asset)
}

// listBookResources godoc
// @Summary 列出作品的可读电子书
// @Tags books-assets
// @Produce json
// @Param workId path string true "作品 UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/works/{workId}/assets [get]
func (h *Handler) listBookResources(c *gin.Context) {
	id, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	items, err := h.service.ListBookResources(c.Request.Context(), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items})
}

// createBookResourceUpload godoc
// @Summary 为指定电子书上传新修订（替换必须明确指定资源）
// @Tags books-assets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "电子书 UUID"
// @Param input body CreateBookResourceUploadInput true "文件信息及公开分享确认"
// @Success 201 {object} BookResourceUploadDTO
// @Router /api/v1/books/assets/{assetId}/upload-sessions [post]
func (h *Handler) createBookResourceUpload(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "assetId")
	if !ok {
		return
	}
	var input CreateBookResourceUploadInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid file metadata"))
		return
	}
	session, err := h.service.CreateBookResourceUpload(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, session)
}

// getBookResourceUpload godoc
// @Summary 获取当前用户的上传与安全检查状态
// @Tags books-assets
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传 UUID"
// @Success 200 {object} BookResourceUploadDTO
// @Router /api/v1/books/upload-sessions/{uploadId} [get]
func (h *Handler) getBookResourceUpload(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "uploadId")
	if !ok {
		return
	}
	session, err := h.service.GetBookResourceUpload(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}

// createBookResourceUploadPart godoc
// @Summary 获取指定分片的短期上传地址
// @Tags books-assets
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传 UUID"
// @Param partNumber path int true "分片号"
// @Success 200 {object} BookUploadPartURL
// @Router /api/v1/books/upload-sessions/{uploadId}/parts/{partNumber} [post]
func (h *Handler) createBookResourceUploadPart(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "uploadId")
	if !ok {
		return
	}
	number, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_part", "Invalid part number"))
		return
	}
	part, err := h.service.CreateBookResourceUploadPart(c.Request.Context(), user, id, number)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, part)
}

// completeBookResourceUploadPart godoc
// @Summary 保存上传分片的完成标记（可重复提交同一分片）
// @Tags books-assets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传 UUID"
// @Param partNumber path int true "分片号"
// @Param input body BookUploadPart true "ETag 与实际分片字节数"
// @Success 200 {object} BookResourceUploadDTO
// @Router /api/v1/books/upload-sessions/{uploadId}/parts/{partNumber}/complete [post]
func (h *Handler) completeBookResourceUploadPart(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "uploadId")
	if !ok {
		return
	}
	number, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_part", "Invalid part number"))
		return
	}
	var input BookUploadPart
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("books.invalid_part", "Invalid upload part"))
		return
	}
	session, err := h.service.CompleteBookResourceUploadPart(c.Request.Context(), user, id, number, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}

// completeBookResourceUpload godoc
// @Summary 完成分片上传，进入后台自动安全检查
// @Tags books-assets
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param uploadId path string true "上传 UUID"
// @Success 202 {object} BookResourceUploadDTO
// @Router /api/v1/books/upload-sessions/{uploadId}/complete [post]
func (h *Handler) completeBookResourceUpload(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "uploadId")
	if !ok {
		return
	}
	session, err := h.service.CompleteBookResourceUpload(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusAccepted, session)
}

// setBookDefaultAsset godoc
// @Summary 按语言设置社区默认电子书，不修改个人选择
// @Tags books-assets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param language path string true "语言代码"
// @Param input body SetBookDefaultAssetInput true "可读电子书 UUID"
// @Success 200 {object} model.BookDefaultAsset
// @Router /api/v1/books/works/{workId}/default-assets/{language} [put]
func (h *Handler) setBookDefaultAsset(c *gin.Context) {
	user, id, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	var input SetBookDefaultAssetInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid default ebook"))
		return
	}
	value, err := h.service.SetBookDefaultAsset(c.Request.Context(), user, id, c.Param("language"), input.AssetID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, value)
}

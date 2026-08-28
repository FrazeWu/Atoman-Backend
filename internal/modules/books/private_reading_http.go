package books

import (
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// getBookAsset godoc
// @Summary 获取私有电子书资源状态
// @Description 只返回当前用户拥有的私有资源元数据，不返回对象存储 key。
// @Tags books-reading
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "私有资源 UUID"
// @Success 200 {object} BookPrivateAssetDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/assets/{assetId} [get]
func (h *Handler) getBookAsset(c *gin.Context) {
	user, assetID, ok := bookAssetRouteUser(c)
	if !ok {
		return
	}
	asset, err := h.service.GetBookAsset(user, assetID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, asset)
}

// getBookAssetContent godoc
// @Summary 读取私有电子书正文
// @Description 只有资源所有者可以读取已完成处理的私有原始文件；响应不缓存且不生成公共 URL。
// @Tags books-reading
// @Produce application/epub+zip
// @Produce application/pdf
// @Produce text/plain
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "私有资源 UUID"
// @Success 200 {file} binary
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/assets/{assetId}/content [get]
func (h *Handler) getBookAssetContent(c *gin.Context) {
	user, assetID, ok := bookAssetRouteUser(c)
	if !ok {
		return
	}
	content, err := h.service.OpenBookAsset(user, assetID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer content.Body.Close()
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Disposition", contentDispositionForBook(content.Asset.OriginalFilename))
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, content.Asset.SizeBytes, content.Asset.ContentType, content.Body, nil)
}

// getBookReadingState godoc
// @Summary 获取私有电子书阅读位置
// @Tags books-reading
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "私有资源 UUID"
// @Success 200 {object} BookReadingStateDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Router /api/v1/books/assets/{assetId}/reading-state [get]
func (h *Handler) getBookReadingState(c *gin.Context) {
	user, assetID, ok := bookAssetRouteUser(c)
	if !ok {
		return
	}
	state, err := h.service.GetBookReadingState(user, assetID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, state)
}

// saveBookReadingState godoc
// @Summary 保存私有电子书阅读位置
// @Description 阅读位置、笔记和偏好均只属于当前用户。
// @Tags books-reading
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "私有资源 UUID"
// @Param input body SaveBookReadingStateInput true "阅读状态"
// @Success 200 {object} BookReadingStateDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Router /api/v1/books/assets/{assetId}/reading-state [put]
func (h *Handler) saveBookReadingState(c *gin.Context) {
	user, assetID, ok := bookAssetRouteUser(c)
	if !ok {
		return
	}
	var input SaveBookReadingStateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	state, err := h.service.SaveBookReadingState(user, assetID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, state)
}

func bookAssetRouteUser(c *gin.Context) (user authctx.CurrentUser, assetID uuid.UUID, ok bool) {
	user, ok = currentBookUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	assetID, err := uuid.Parse(strings.TrimSpace(c.Param("assetId")))
	if err != nil || assetID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "assetId must be a valid UUID"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, assetID, true
}

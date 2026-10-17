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

// recommendBookReading godoc
// @Summary 推荐阅读文件，保留个人固定修订，GET 不改变选择
// @Tags books-reading
// @Produce json
// @Param workId path string true "作品 UUID"
// @Param language query string false "优先语言 BCP47"
// @Param formats query string false "客户端支持的格式，逗号分隔，默认 epub,pdf,txt"
// @Success 200 {object} BookReadingRecommendation
// @Router /api/v1/books/works/{workId}/reading-recommendation [get]
func (h *Handler) recommendBookReading(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	id, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	userID := uuid.Nil
	if user, ok := authctx.Current(c); ok {
		userID = user.ID
	}
	var formats []string
	if value := strings.TrimSpace(c.Query("formats")); value != "" {
		formats = strings.Split(value, ",")
	}
	result, err := h.service.RecommendBookReading(c.Request.Context(), userID, id, c.Query("language"), formats)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// getBookReadingSelection godoc
// @Summary 获取当前用户的文件选择，没有选择时返回 null
// @Tags books-reading
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Success 200 {object} model.BookReadingSelection
// @Router /api/v1/books/me/selections/{workId} [get]
func (h *Handler) getBookReadingSelection(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, id, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	result, err := h.service.GetBookReadingSelection(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// setBookReadingSelection godoc
// @Summary 显式选择电子书及不可变修订，不改变社区默认
// @Tags books-reading
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param input body SetBookReadingSelectionInput true "电子书和修订 UUID"
// @Success 200 {object} model.BookReadingSelection
// @Router /api/v1/books/me/selections/{workId} [put]
func (h *Handler) setBookReadingSelection(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, id, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	var input SetBookReadingSelectionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid reading selection"))
		return
	}
	result, err := h.service.SetBookReadingSelection(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// createBookReadingSession godoc
// @Summary 创建幂等设备阅读会话并获取云端进度
// @Tags books-reading
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body CreateBookReadingSessionInput true "客户端会话、设备与文件修订"
// @Success 200 {object} BookReadingSessionDTO
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/reading-sessions [post]
func (h *Handler) createBookReadingSession(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	var input CreateBookReadingSessionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid reading session"))
		return
	}
	result, err := h.service.CreateBookReadingSession(c.Request.Context(), user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// getBookCloudReadingState godoc
// @Summary 获取当前用户修订级阅读状态，文件下架后保留已有历史
// @Tags books-reading
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param revisionId path string true "文件修订 UUID"
// @Success 200 {object} model.BookReadingState
// @Router /api/v1/books/me/reading-states/{revisionId} [get]
func (h *Handler) getBookCloudReadingState(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	result, err := h.service.GetBookCloudReadingState(c.Request.Context(), user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// saveBookCloudReadingState godoc
// @Summary 幂等保存阅读状态，当前位置可倒退，最远进度单调增长；冲突需用户选择
// @Tags books-reading
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param revisionId path string true "文件修订 UUID"
// @Param input body SaveBookCloudReadingStateInput true "设备会话、请求 UUID、序号、基础版本及位置"
// @Success 200 {object} BookReadingWriteResult
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/me/reading-states/{revisionId} [put]
func (h *Handler) saveBookCloudReadingState(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	id, ok := parseCatalogID(c, "revisionId")
	if !ok {
		return
	}
	var input SaveBookCloudReadingStateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid reading update"))
		return
	}
	result, err := h.service.SaveBookCloudReadingState(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

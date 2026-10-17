package books

import (
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// listBookNotes godoc
// @Summary 列出我的所有阅读笔记与划线
// @Description 跨书聚合查询当前用户的阅读高亮与批注，支持关键词搜索、单书筛选、有想法筛选。
// @Tags books-annotations
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param q query string false "搜索正文或笔记文本"
// @Param asset_id query string false "过滤单本图书资产 UUID"
// @Param has_note query bool false "只查看包含想法的划线"
// @Param color query string false "过滤高亮色彩 (yellow, green, blue, pink)"
// @Param limit query int false "分页大小 (默认 20, 最大 100)"
// @Param offset query int false "分页偏移量"
// @Success 200 {object} BookAnnotationListResult
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
func (h *Handler) listBookNotes(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}

	filter := ListGlobalNotesFilter{
		Keyword: strings.TrimSpace(c.Query("q")),
		Color:   strings.TrimSpace(c.Query("color")),
		Limit:   parsePositiveInt(c.Query("limit"), 20),
		Offset:  parseNonNegativeInt(c.Query("offset"), 0),
	}

	if raw := strings.TrimSpace(c.Query("asset_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "asset_id must be a valid UUID"))
			return
		}
		filter.AssetID = &id
	}

	if raw := strings.TrimSpace(c.Query("has_note")); raw != "" {
		b := raw == "true" || raw == "1"
		filter.HasNote = &b
	}

	result, err := h.service.ListGlobalNotes(user, filter)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// listAssetAnnotations godoc
// @Summary 获取指定图书资源的划线批注
// @Description 按照创建顺序返回属于当前用户的该资源所有划线及想法。
// @Tags books-annotations
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "图书资源 UUID"
// @Success 200 {array} BookAnnotationDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
func (h *Handler) listAssetAnnotations(c *gin.Context) {
	user, assetID, ok := bookAssetRouteUser(c)
	if !ok {
		return
	}

	items, err := h.service.ListAssetAnnotations(user, assetID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, items)
}

// createBookAnnotation godoc
// @Summary 新增阅读划线或笔记
// @Tags books-annotations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "图书资源 UUID"
// @Param input body CreateBookAnnotationInput true "划线与批注内容"
// @Success 201 {object} BookAnnotationDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
func (h *Handler) createBookAnnotation(c *gin.Context) {
	user, assetID, ok := bookAssetRouteUser(c)
	if !ok {
		return
	}

	var input CreateBookAnnotationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}

	item, err := h.service.CreateAnnotation(user, assetID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, item)
}

// updateBookAnnotation godoc
// @Summary 更新阅读划线批注
// @Description 支持更新随手想法文本、高亮色彩与划线样式。
// @Tags books-annotations
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param annotationId path string true "划线 UUID"
// @Param input body UpdateBookAnnotationInput true "更新内容"
// @Success 200 {object} BookAnnotationDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
func (h *Handler) updateBookAnnotation(c *gin.Context) {
	user, annotationID, ok := bookAnnotationRouteUser(c)
	if !ok {
		return
	}

	var input UpdateBookAnnotationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}

	item, err := h.service.UpdateAnnotation(user, annotationID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, item)
}

// deleteBookAnnotation godoc
// @Summary 删除划线批注
// @Tags books-annotations
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param annotationId path string true "划线 UUID"
// @Success 200 {object} map[string]boolean
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
func (h *Handler) deleteBookAnnotation(c *gin.Context) {
	user, annotationID, ok := bookAnnotationRouteUser(c)
	if !ok {
		return
	}

	if err := h.service.DeleteAnnotation(user, annotationID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func bookAnnotationRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := currentBookUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("annotationId")))
	if err != nil || id == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "annotationId must be a valid UUID"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, id, true
}

func parseNonNegativeInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val < 0 {
		return fallback
	}
	return val
}

func parsePositiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val < 1 {
		return fallback
	}
	return val
}

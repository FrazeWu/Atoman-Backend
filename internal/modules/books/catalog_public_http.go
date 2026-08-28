package books

import (
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// searchPublicCatalog godoc
// @Summary 搜索公共书目
// @Description 只搜索已公开的作品元数据，不包含私有导入和正文。
// @Tags books-catalog
// @Produce json
// @Param q query string false "标题、作者或副标题"
// @Param limit query int false "每页数量，最大 50"
// @Param offset query int false "偏移量"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/catalog/search [get]
func (h *Handler) searchPublicCatalog(c *gin.Context) {
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items, total, err := h.service.SearchPublicCatalog(c.Request.Context(), c.Query("q"), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

// getPublicWork godoc
// @Summary 获取公共作品
// @Tags books-catalog
// @Produce json
// @Param workId path string true "公共作品 UUID"
// @Success 200 {object} BookPublicWorkDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId} [get]
func (h *Handler) getPublicWork(c *gin.Context) {
	workID, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	work, err := h.service.GetPublicWork(c.Request.Context(), workID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, work)
}

// getPublicEdition godoc
// @Summary 获取公共版本
// @Tags books-catalog
// @Produce json
// @Param editionId path string true "公共版本 UUID"
// @Success 200 {object} BookPublicEditionDetailDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/editions/{editionId} [get]
func (h *Handler) getPublicEdition(c *gin.Context) {
	editionID, ok := parseCatalogID(c, "editionId")
	if !ok {
		return
	}
	edition, err := h.service.GetPublicEdition(c.Request.Context(), editionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, edition)
}

func catalogPaginationParams(c *gin.Context) (int, int, bool) {
	limit, offset := 20, 0
	var err error
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "limit must be an integer"))
			return 0, 0, false
		}
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "offset must be an integer"))
			return 0, 0, false
		}
	}
	limit, offset = normalizeCatalogPagination(limit, offset)
	return limit, offset, true
}

func parseCatalogID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(strings.TrimSpace(c.Param(name)))
	if err != nil || id == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", name+" must be a valid UUID"))
		return uuid.Nil, false
	}
	return id, true
}

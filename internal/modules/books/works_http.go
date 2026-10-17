package books

import (
	"net/http"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

// searchBookWorks godoc
// @Summary 搜索新版公开作品，不迁移或兼容旧版条目
// @Tags books-catalog
// @Produce json
// @Param q query string false "标题或作者"
// @Param limit query int false "每页数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/works [get]
func (h *Handler) searchBookWorks(c *gin.Context) {
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items, total, err := h.service.SearchBookWorks(c.Request.Context(), c.Query("q"), limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total, "limit": limit, "offset": offset})
}

// createBookWork godoc
// @Summary 使用书名和作者创建公开作品，无需文件或出版版本
// @Tags books-catalog
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body CreateBookWorkInput true "作品元数据"
// @Success 201 {object} BookWorkDetail
// @Failure 400 {object} handlers.ErrorResponse
// @Router /api/v1/books/works [post]
func (h *Handler) createBookWork(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	var input CreateBookWorkInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid work metadata"))
		return
	}
	work, err := h.service.CreateBookWork(c.Request.Context(), user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, work)
}

// getBookWork godoc
// @Summary 获取公开作品及当前修订标识
// @Tags books-catalog
// @Produce json
// @Param workId path string true "作品 UUID"
// @Success 200 {object} BookWorkDetail
// @Success 308 {string} string "作品已合并，Location 指向规范作品"
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/works/{workId} [get]
func (h *Handler) getBookWork(c *gin.Context) {
	id, ok := parseCatalogID(c, "workId")
	if !ok {
		return
	}
	work, err := h.service.GetBookWork(c.Request.Context(), id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if work.RedirectedFrom != "" {
		c.Redirect(http.StatusPermanentRedirect, "/api/v1/books/works/"+work.ID)
		return
	}
	httpx.OK(c, http.StatusOK, work)
}

// createBookPerson godoc
// @Summary 快速创建公开作者
// @Tags books-catalog
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body CreateBookPersonInput true "作者信息"
// @Success 201 {object} model.BookPerson
// @Router /api/v1/books/people [post]
func (h *Handler) createBookPerson(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	var input CreateBookPersonInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid author metadata"))
		return
	}
	person, err := h.service.CreateBookPerson(c.Request.Context(), user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, person)
}

// searchBookPeople godoc
// @Summary 搜索公开作者
// @Tags books-catalog
// @Produce json
// @Param q query string false "作者姓名"
// @Param limit query int false "每页数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/books/people [get]
func (h *Handler) searchBookPeople(c *gin.Context) {
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items := make([]model.BookPerson, 0)
	query := h.service.db.WithContext(c.Request.Context()).Model(&model.BookPerson{}).Where("lifecycle_status = ?", model.BookLifecycleStatusActive)
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		query = query.Where("name ILIKE ?", "%"+escapeBookCatalogQuery(q)+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	if err := query.Order("name ASC, id ASC").Offset(offset).Limit(limit).Find(&items).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"items": items, "total": total})
}

// createBookEdition godoc
// @Summary 为已有作品独立添加出版版本
// @Tags books-catalog
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param workId path string true "作品 UUID"
// @Param input body CreateBookEditionInput true "出版版本信息"
// @Success 201 {object} BookPublicEditionDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/works/{workId}/editions [post]
func (h *Handler) createBookEdition(c *gin.Context) {
	user, id, ok := bookWorkRouteUser(c)
	if !ok {
		return
	}
	var input CreateBookEditionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Invalid edition metadata"))
		return
	}
	edition, err := h.service.CreateBookEdition(c.Request.Context(), user, id, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, edition)
}

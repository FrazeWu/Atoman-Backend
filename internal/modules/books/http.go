package books

import (
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/middleware"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RegisterRoutes registers private import routes under /api/v1/books.
func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	handler := &Handler{service: service}
	public := group.Group("")
	public.GET("/catalog/search", handler.searchPublicCatalog)
	public.GET("/catalog/works/:workId", handler.getPublicWork)
	public.GET("/catalog/works/:workId/reviews", handler.listPublicBookReviews)
	public.GET("/catalog/works/:workId/posts", handler.listRelatedBookPosts)
	public.GET("/catalog/works/:workId/assets", handler.listPublishedBookAssets)
	public.GET("/catalog/assets/:assetId", handler.getPublishedBookAsset)
	public.GET("/catalog/assets/:assetId/content", handler.getPublishedBookAssetContent)
	public.GET("/catalog/editions/:editionId", handler.getPublicEdition)

	protected := group.Group("")
	protected.Use(requireCurrentUser())
	protected.POST("/imports", handler.createBookImport)
	protected.GET("/imports", handler.listBookImports)
	protected.GET("/imports/:importId", handler.getBookImport)
	protected.POST("/imports/:importId/parts/:partNumber", handler.createBookUploadPart)
	protected.POST("/imports/:importId/parts/:partNumber/complete", handler.completeBookUploadPart)
	protected.POST("/imports/:importId/complete", handler.completeBookImport)
	protected.POST("/imports/:importId/retry", handler.retryBookImport)
	protected.PUT("/imports/:importId/catalog-link", handler.linkBookImportToCatalog)
	protected.DELETE("/imports/:importId", handler.deleteBookImport)
	protected.GET("/assets/:assetId", handler.getBookAsset)
	protected.GET("/assets/:assetId/content", handler.getBookAssetContent)
	protected.GET("/assets/:assetId/reading-state", handler.getBookReadingState)
	protected.PUT("/assets/:assetId/reading-state", handler.saveBookReadingState)
	protected.GET("/library", handler.listBookShelf)
	protected.GET("/library/continue", handler.listContinueReading)
	protected.PUT("/library/works/:workId", handler.saveBookShelf)
	protected.DELETE("/library/works/:workId", handler.deleteBookShelf)
	protected.PUT("/catalog/works/:workId/rating", handler.setBookRating)
	protected.PUT("/catalog/works/:workId/review", handler.saveBookReview)
	protected.GET("/catalog/works/:workId/review", handler.getMyBookReview)
	protected.DELETE("/catalog/works/:workId/review", handler.deleteBookReview)
	protected.POST("/contributions/:editId/vote", handler.voteBookEdit)
	protected.POST("/catalog/assets/:assetId/reports", handler.reportPublishedBookAsset)

	publish := protected.Group("")
	publish.Use(middleware.RequireSiteFeature(service.db, "books", "books.publish_asset"))
	publish.POST("/assets/:assetId/publication-requests", handler.submitPublicationRequest)
	publish.POST("/publication-requests/:requestId/evidence", handler.uploadPublicationEvidence)
	publish.GET("/publication-requests/:requestId/evidence", handler.getPublicationEvidence)
	publish.POST("/publication-requests/:requestId/appeals", handler.submitPublicationAppeal)
	publish.GET("/publication-requests/:requestId/appeals", handler.listMyPublicationAppeals)
	publish.GET("/publication-requests", handler.listMyPublicationRequests)

	submit := protected.Group("")
	submit.Use(middleware.RequireSiteFeature(service.db, "books", "books.submit"))
	submit.POST("/contributions", handler.submitBookEdit)
	submit.GET("/contributions", handler.listMyBookEdits)
	submit.POST("/contributions/:editId/withdraw", handler.withdrawBookEdit)
	submit.PUT("/catalog/works/:workId/posts/:postId", handler.linkBookPost)
	submit.DELETE("/catalog/works/:workId/posts/:postId", handler.unlinkBookPost)

	review := protected.Group("")
	review.Use(middleware.RequireSiteFeature(service.db, "books", "books.review"))
	review.GET("/review/contributions", handler.listBookEditReviewQueue)
	review.POST("/review/contributions/:editId/decision", handler.reviewBookEdit)
	review.GET("/review/publication-requests", handler.listPublicationReviewQueue)
	review.GET("/review/publication-requests/:requestId/evidence", handler.getPublicationEvidence)
	review.GET("/review/publication-appeals", handler.listPublicationAppealReviewQueue)
	review.POST("/review/publication-appeals/:appealId/decision", handler.reviewPublicationAppeal)
	review.POST("/review/publication-requests/:requestId/decision", handler.reviewPublicationRequest)
	review.POST("/review/publication-requests/:requestId/retention-hold", handler.setPublicationRetentionHold)
	review.POST("/review/published-assets/:assetId/status", handler.setPublishedAssetStatus)
	review.GET("/review/publication-reports", handler.listPublicationReports)
	review.POST("/review/publication-reports/:reportId/decision", handler.reviewPublicationReport)
}

type Handler struct {
	service *Service
}

func requireCurrentUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := authctx.Current(c); ok {
			c.Next()
			return
		}
		middleware.AuthMiddleware()(c)
	}
}

// createBookImport godoc
// @Summary 创建私有电子书导入
// @Description 创建用户隔离的 EPUB 或 PDF 分片上传会话。正文不会进入公共书目。
// @Tags books-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body CreateBookImportInput true "电子书文件信息"
// @Success 201 {object} BookImportSessionDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports [post]
func (h *Handler) createBookImport(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	var input CreateBookImportInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	session, err := h.service.CreateBookImport(user, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, session)
}

// listBookImports godoc
// @Summary 列出我的私有电子书导入
// @Tags books-imports
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {array} BookImportSessionDTO
// @Failure 401 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports [get]
func (h *Handler) listBookImports(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	imports, err := h.service.ListBookImports(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, imports)
}

// getBookImport godoc
// @Summary 获取私有电子书导入状态
// @Tags books-imports
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param importId path string true "导入会话 UUID"
// @Success 200 {object} BookImportSessionDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports/{importId} [get]
func (h *Handler) getBookImport(c *gin.Context) {
	user, id, ok := bookImportRouteUser(c)
	if !ok {
		return
	}
	session, err := h.service.GetBookImport(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}

// createBookUploadPart godoc
// @Summary 获取私有电子书分片上传地址
// @Tags books-imports
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param importId path string true "导入会话 UUID"
// @Param partNumber path int true "分片序号"
// @Success 200 {object} BookUploadPartURL
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports/{importId}/parts/{partNumber} [post]
func (h *Handler) createBookUploadPart(c *gin.Context) {
	user, id, ok := bookImportRouteUser(c)
	if !ok {
		return
	}
	partNumber, ok := parseBookPartNumber(c)
	if !ok {
		return
	}
	part, err := h.service.CreateBookUploadPart(user, id, partNumber)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, part)
}

// completeBookUploadPart godoc
// @Summary 记录私有电子书分片上传结果
// @Tags books-imports
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param importId path string true "导入会话 UUID"
// @Param partNumber path int true "分片序号"
// @Param input body BookUploadPart true "分片 ETag 与大小"
// @Success 200 {object} BookImportSessionDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports/{importId}/parts/{partNumber}/complete [post]
func (h *Handler) completeBookUploadPart(c *gin.Context) {
	user, id, ok := bookImportRouteUser(c)
	if !ok {
		return
	}
	partNumber, ok := parseBookPartNumber(c)
	if !ok {
		return
	}
	var part BookUploadPart
	if err := c.ShouldBindJSON(&part); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	session, err := h.service.CompleteBookUploadPart(user, id, partNumber, part)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}

// completeBookImport godoc
// @Summary 完成私有电子书上传
// @Description 完成对象存储分片合并并校验文件魔数。文件进入扫描状态后才会继续解析。
// @Tags books-imports
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param importId path string true "导入会话 UUID"
// @Success 200 {object} BookImportSessionDTO
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports/{importId}/complete [post]
func (h *Handler) completeBookImport(c *gin.Context) {
	user, id, ok := bookImportRouteUser(c)
	if !ok {
		return
	}
	session, err := h.service.CompleteBookImport(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, session)
}

// deleteBookImport godoc
// @Summary 删除私有电子书导入
// @Description 立即撤销读取状态，并删除或终止对应的私有对象存储上传。
// @Tags books-imports
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param importId path string true "导入会话 UUID"
// @Success 200 {object} map[string]boolean
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/imports/{importId} [delete]
func (h *Handler) deleteBookImport(c *gin.Context) {
	user, id, ok := bookImportRouteUser(c)
	if !ok {
		return
	}
	if err := h.service.DeleteBookImport(user, id); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"deleted": true})
}

func currentBookUser(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
	}
	return user, ok
}

func bookImportRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := currentBookUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	id, err := uuid.Parse(strings.TrimSpace(c.Param("importId")))
	if err != nil || id == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "importId must be a valid UUID"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, id, true
}

func parseBookPartNumber(c *gin.Context) (int, bool) {
	partNumber, err := strconv.Atoi(strings.TrimSpace(c.Param("partNumber")))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "partNumber must be an integer"))
		return 0, false
	}
	return partNumber, true
}

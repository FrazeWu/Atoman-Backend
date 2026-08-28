package books

import (
	"io"
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// submitPublicationRequest godoc
// @Summary 申请发布公共正文
// @Description 提交权利声明后等待审核。审核通过会创建独立公共资源，原私有资源仍由原用户控制。
// @Tags books-publication
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "私有资源 UUID"
// @Param input body SubmitPublicationInput true "授权声明"
// @Success 201 {object} BookPublicationRequestDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 422 {object} handlers.ErrorResponse
// @Router /api/v1/books/assets/{assetId}/publication-requests [post]
func (h *Handler) submitPublicationRequest(c *gin.Context) {
	user, assetID, ok := bookAssetRouteUser(c)
	if !ok {
		return
	}
	var input SubmitPublicationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	request, err := h.service.SubmitPublicationRequest(user, assetID, input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, request)
}

// uploadPublicationEvidence godoc
// @Summary 上传公共正文授权证据
// @Description 证据文件仅保存到私有 R2 对象存储，不会出现在公共书目接口中。
// @Tags books-publication
// @Accept multipart/form-data
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param requestId path string true "申请 UUID"
// @Param evidence formData file true "授权证据文件"
// @Success 200 {object} BookPublicationRequestDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/publication-requests/{requestId}/evidence [post]
func (h *Handler) uploadPublicationEvidence(c *gin.Context) {
	user, requestID, ok := publicationRequestRouteUser(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, bookPublicationEvidenceMaxSize+1024*1024)
	file, header, err := c.Request.FormFile("evidence")
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "evidence file is required and must be within the size limit"))
		return
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > bookPublicationEvidenceMaxSize {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "evidence file size is invalid"))
		return
	}
	head := make([]byte, 512)
	n, readErr := file.Read(head)
	if readErr != nil && readErr != io.EOF {
		httpx.Error(c, apperr.BadRequest("books.evidence_read_failed", "evidence file could not be read"))
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		httpx.Error(c, apperr.BadRequest("books.evidence_read_failed", "evidence file could not be read"))
		return
	}
	contentType := http.DetectContentType(head[:n])
	request, err := h.service.UploadPublicationEvidence(user, requestID, header.Filename, contentType, header.Size, file)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, request)
}

// getPublicationEvidence godoc
// @Summary 获取公共正文授权证据
// @Description 仅申请人和审核人员可读取私有授权证据。
// @Tags books-review
// @Produce application/octet-stream
// @Security BearerAuth
// @Security CookieAuth
// @Param requestId path string true "申请 UUID"
// @Success 200 {file} binary
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/publication-requests/{requestId}/evidence [get]
// @Router /api/v1/books/publication-requests/{requestId}/evidence [get]
func (h *Handler) getPublicationEvidence(c *gin.Context) {
	user, requestID, ok := publicationRequestRouteUser(c)
	if !ok {
		return
	}
	evidence, body, err := h.service.OpenPublicationEvidence(user, requestID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer body.Close()
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Disposition", contentDispositionForBook(evidence.FileName))
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, evidence.Size, evidence.ContentType, body, nil)
}

// @Summary 列出我的公共正文申请
// @Tags books-publication
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} BookPublicationRequestListResult
// @Router /api/v1/books/publication-requests [get]
func (h *Handler) listMyPublicationRequests(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	items, total, err := h.service.ListPublicationRequests(c.Request.Context(), user, false, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookPublicationRequestListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// listPublicationReviewQueue godoc
// @Summary 列出公共正文审核申请
// @Tags books-review
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} BookPublicationRequestListResult
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/publication-requests [get]
func (h *Handler) listPublicationReviewQueue(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	items, total, err := h.service.ListPublicationRequests(c.Request.Context(), user, true, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookPublicationRequestListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// reviewPublicationRequest godoc
// @Summary 审核公共正文申请
// @Description 审核通过会复制到独立公共对象存储键，不会把私有对象直接改为公共对象。
// @Tags books-review
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param requestId path string true "申请 UUID"
// @Param input body reviewPublicationInput true "审核决定"
// @Success 200 {object} BookPublicationRequestDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/publication-requests/{requestId}/decision [post]
func (h *Handler) reviewPublicationRequest(c *gin.Context) {
	user, requestID, ok := publicationRequestRouteUser(c)
	if !ok {
		return
	}
	var input reviewPublicationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	request, err := h.service.ReviewPublicationRequest(user, requestID, strings.TrimSpace(input.Decision), input.Note)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, request)
}

// listPublishedBookAssets godoc
// @Summary 列出公共正文资源
// @Tags books-reading
// @Produce json
// @Param workId path string true "作品 UUID"
// @Success 200 {object} PublishedBookAssetListResult
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/works/{workId}/assets [get]
func (h *Handler) listPublishedBookAssets(c *gin.Context) {
	workID, err := uuid.Parse(strings.TrimSpace(c.Param("workId")))
	if err != nil || workID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "workId must be a valid UUID"))
		return
	}
	items, total, err := h.service.ListPublishedBookAssets(c.Request.Context(), workID, uuid.Nil, 20, 0)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, PublishedBookAssetListResult{Items: items, Total: total, Limit: 20, Offset: 0})
}

// getPublishedBookAsset godoc
// @Summary 获取公共正文元数据
// @Tags books-reading
// @Produce json
// @Param assetId path string true "公共资源 UUID"
// @Success 200 {object} BookPublishedAssetDTO
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/assets/{assetId} [get]
func (h *Handler) getPublishedBookAsset(c *gin.Context) {
	assetID, ok := parsePublicAssetID(c)
	if !ok {
		return
	}
	asset, err := h.service.GetPublishedBookAsset(c.Request.Context(), assetID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, asset)
}

// getPublishedBookAssetContent godoc
// @Summary 读取公共正文
// @Tags books-reading
// @Produce application/epub+zip
// @Produce application/pdf
// @Produce text/plain
// @Param assetId path string true "公共资源 UUID"
// @Success 200 {file} binary
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 503 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/assets/{assetId}/content [get]
func (h *Handler) getPublishedBookAssetContent(c *gin.Context) {
	assetID, ok := parsePublicAssetID(c)
	if !ok {
		return
	}
	asset, body, err := h.service.OpenPublishedBookAsset(c.Request.Context(), assetID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	defer body.Close()
	c.Header("Cache-Control", "public, max-age=3600")
	c.Header("Content-Disposition", contentDispositionForBook(asset.FileName))
	c.Header("X-Content-Type-Options", "nosniff")
	c.DataFromReader(http.StatusOK, asset.Size, asset.ContentType, body, nil)
}

// setPublishedAssetStatus godoc
// @Summary 下架或恢复公共正文
// @Tags books-review
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "公共资源 UUID"
// @Param input body reviewPublicationInput true "资源状态"
// @Success 200 {object} map[string]boolean
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/published-assets/{assetId}/status [post]
func (h *Handler) setPublishedAssetStatus(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	assetID, ok := parsePublicAssetID(c)
	if !ok {
		return
	}
	var input reviewPublicationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	if err := h.service.SetPublishedBookAssetStatus(user, assetID, strings.TrimSpace(input.Decision), input.Note); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"updated": true})
}

type PublishedBookAssetListResult struct {
	Items  []BookPublishedAssetDTO `json:"items"`
	Total  int64                   `json:"total"`
	Limit  int                     `json:"limit"`
	Offset int                     `json:"offset"`
}

func publicationRequestRouteUser(c *gin.Context) (authctx.CurrentUser, uuid.UUID, bool) {
	user, ok := currentBookUser(c)
	if !ok {
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	requestID, err := uuid.Parse(strings.TrimSpace(c.Param("requestId")))
	if err != nil || requestID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "requestId must be a valid UUID"))
		return authctx.CurrentUser{}, uuid.Nil, false
	}
	return user, requestID, true
}

func parsePublicAssetID(c *gin.Context) (uuid.UUID, bool) {
	assetID, err := uuid.Parse(strings.TrimSpace(c.Param("assetId")))
	if err != nil || assetID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "assetId must be a valid UUID"))
		return uuid.Nil, false
	}
	return assetID, true
}

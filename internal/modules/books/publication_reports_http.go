package books

import (
	"net/http"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type bookPublicationReportInput struct {
	Reason string `json:"reason"`
}

// reportPublishedBookAsset godoc
// @Summary 举报公共正文
// @Tags books-publication
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "公共资源 UUID"
// @Param input body bookPublicationReportInput true "举报理由"
// @Success 201 {object} BookPublicationReportDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/catalog/assets/{assetId}/reports [post]
func (h *Handler) reportPublishedBookAsset(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	assetID, ok := parsePublicAssetID(c)
	if !ok {
		return
	}
	var input bookPublicationReportInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	report, err := h.service.ReportPublishedBookAsset(user, assetID, input.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, report)
}

// listPublicationReports godoc
// @Summary 列出公共正文举报
// @Tags books-review
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} BookPublicationReportListResult
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/publication-reports [get]
func (h *Handler) listPublicationReports(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset := bookPagination(c)
	reports, total, err := h.service.ListPublicationReports(c.Request.Context(), user, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookPublicationReportListResult{Items: reports, Total: total, Limit: limit, Offset: offset})
}

// reviewPublicationReport godoc
// @Summary 处理公共正文举报
// @Tags books-review
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param reportId path string true "举报 UUID"
// @Param input body reviewPublicationInput true "举报处理决定"
// @Success 200 {object} BookPublicationReportDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Router /api/v1/books/review/publication-reports/{reportId}/decision [post]
func (h *Handler) reviewPublicationReport(c *gin.Context) {
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	reportID, err := uuid.Parse(strings.TrimSpace(c.Param("reportId")))
	if err != nil || reportID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "reportId must be a valid UUID"))
		return
	}
	var input reviewPublicationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	report, err := h.service.ReviewPublicationReport(user, reportID, strings.TrimSpace(input.Decision), input.Note)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, report)
}

package books

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

const (
	bookPublicationReportLimit     = 5
	bookPublicationReportWindow    = time.Minute
	bookPublicationReportNoteRunes = 1000
)

var bookPublicationReportReasons = map[string]struct{}{
	"spam": {}, "harassment": {}, "hate": {}, "sexual": {}, "violence": {},
	"misinformation": {}, "copyright": {}, "illegal": {}, "other": {},
}

type reportPublishedBookInput struct {
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

type reviewPublicationReportInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// reportPublishedBookAsset godoc
// @Summary 举报已公开的电子书
// @Description 登录用户举报一篇已发布的公共电子书。同一用户对同一文件只保留一条举报，被驳回后可以重新提交。每分钟最多 5 次。
// @Tags books-assets
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param assetId path string true "电子书 UUID"
// @Param input body reportPublishedBookInput true "举报原因。other 必须填写 note"
// @Success 201 {object} BookPublicationReportDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Failure 429 {object} handlers.ErrorResponse
// @Router /api/v1/books/assets/{assetId}/reports [post]
func (h *Handler) reportPublishedBookAsset(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	assetID, ok := parseCatalogID(c, "assetId")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	var input reportPublishedBookInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "invalid report"))
		return
	}
	reason, err := composeBookPublicationReportReason(input.Reason, input.Note)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if h.reportLimiter != nil {
		allowed, retryAfter := h.reportLimiter.Allow("books:publication-report:"+user.ID.String(), bookPublicationReportLimit, bookPublicationReportWindow)
		if !allowed {
			seconds := int((retryAfter + time.Second - 1) / time.Second)
			if seconds < 1 {
				seconds = 1
			}
			c.Header("Retry-After", strconv.Itoa(seconds))
			httpx.Error(c, apperr.New(http.StatusTooManyRequests, "books.report_rate_limited", "Too many publication reports", map[string]any{"retry_after": seconds}))
			return
		}
	}
	report, err := h.service.ReportPublishedBookAsset(user, assetID, reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, report)
}

// listPublicationReports godoc
// @Summary 获取待处理的公共电子书举报
// @Description 仅返回仍待处理、且不是当前审核人自己提交的举报。版主、管理员和站主可访问。
// @Tags books-admin
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param limit query int false "每页数量，默认 20，最多 50"
// @Param offset query int false "偏移量"
// @Success 200 {object} BookPublicationReportListResult
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Router /api/v1/books/admin/publication-reports [get]
func (h *Handler) listPublicationReports(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	limit, offset, ok := catalogPaginationParams(c)
	if !ok {
		return
	}
	items, total, err := h.service.ListPublicationReports(c.Request.Context(), user, limit, offset)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, BookPublicationReportListResult{Items: items, Total: total, Limit: limit, Offset: offset})
}

// reviewPublicationReport godoc
// @Summary 处理公共电子书举报
// @Description decision 为 removed 时下架对应公共电子书，为 rejected 时驳回举报。不能处理自己提交的举报。版主、管理员和站主可访问。
// @Tags books-admin
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param reportId path string true "举报 UUID"
// @Param input body reviewPublicationReportInput true "处理决定"
// @Success 200 {object} BookPublicationReportDTO
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Router /api/v1/books/admin/publication-reports/{reportId} [put]
func (h *Handler) reviewPublicationReport(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	user, ok := currentBookUser(c)
	if !ok {
		return
	}
	reportID, ok := parseCatalogID(c, "reportId")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	var input reviewPublicationReportInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "invalid report decision"))
		return
	}
	report, err := h.service.ReviewPublicationReport(user, reportID, strings.TrimSpace(input.Decision), input.Note)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, report)
}

func composeBookPublicationReportReason(reason, note string) (string, error) {
	reason = strings.ToLower(strings.TrimSpace(reason))
	note = strings.TrimSpace(note)
	if _, ok := bookPublicationReportReasons[reason]; !ok {
		return "", apperr.BadRequest("validation.invalid_request", "report reason is invalid")
	}
	if utf8.RuneCountInString(note) > bookPublicationReportNoteRunes {
		return "", apperr.BadRequest("validation.invalid_request", "report note is invalid")
	}
	if reason == "other" && note == "" {
		return "", apperr.BadRequest("validation.invalid_request", "report note is required")
	}
	if note == "" {
		return reason, nil
	}
	return reason + "\n" + note, nil
}

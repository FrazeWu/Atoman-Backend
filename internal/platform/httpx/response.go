package httpx

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"

	"github.com/gin-gonic/gin"
)

type Meta gin.H

type PageMeta struct {
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
	HasMore  bool  `json:"has_more"`
}

func OK(c *gin.Context, status int, data any) {
	c.JSON(status, gin.H{"data": data})
}

func OKMeta(c *gin.Context, status int, data any, meta any) {
	c.JSON(status, gin.H{"data": data, "meta": meta})
}

func List(c *gin.Context, data any, page int, pageSize int, total int64) {
	// Avoid overflow in page*pageSize for large client-supplied page values.
	hasMore := page > 0 && pageSize > 0 && total > 0 &&
		int64(page) <= (total-1)/int64(pageSize)
	meta := PageMeta{Page: page, PageSize: pageSize, Total: total, HasMore: hasMore}
	OKMeta(c, http.StatusOK, data, meta)
}

// clientClosedRequest is nginx's convention for a client that hung up before
// the response was written.
const clientClosedRequest = 499

func Error(c *gin.Context, err error) {
	// The client is already gone: it navigated away, canceled the request, or
	// stopped waiting mid-flight, and the aborted query came back with
	// "context canceled". Nobody can read the response and this must not be
	// reported as a server error, so record 499 and stop.
	// c.Request is nil in bare gin.CreateTestContext test contexts.
	if c.Request != nil && c.Request.Context().Err() != nil {
		c.AbortWithStatus(clientClosedRequest)
		return
	}
	app := apperr.FromError(err)
	if app == nil {
		app = apperr.Internal(nil)
	}
	c.JSON(app.HTTPStatus, gin.H{"error": gin.H{"code": app.Code, "message": app.Message, "details": app.Details}})
}

func PageParams(c *gin.Context) (int, int) {
	page := parsePositiveInt(c.Query("page"), 1)
	pageSize := parsePositiveInt(c.Query("page_size"), 20)
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func PageParamsWith(c *gin.Context, sizeKey string, defaultSize, maxSize int) (int, int) {
	page := parsePositiveInt(c.Query("page"), 1)
	pageSize := parsePositiveInt(c.Query(sizeKey), defaultSize)
	if pageSize > maxSize {
		pageSize = defaultSize
	}
	return page, pageSize
}

func Offset(page int, pageSize int) int {
	if page <= 1 || pageSize <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if page-1 > maxInt/pageSize {
		return maxInt
	}
	return (page - 1) * pageSize
}

func parsePositiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

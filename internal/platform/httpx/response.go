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
	meta := PageMeta{Page: page, PageSize: pageSize, Total: total, HasMore: int64(page*pageSize) < total}
	OKMeta(c, http.StatusOK, data, meta)
}

func Error(c *gin.Context, err error) {
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

func Offset(page int, pageSize int) int {
	if page < 1 {
		page = 1
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

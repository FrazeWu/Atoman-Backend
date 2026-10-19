package books

import (
	"strconv"
	"strings"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

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

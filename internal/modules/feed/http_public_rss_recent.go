package feed

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
)

// @Summary Public external RSS articles, recent and deduplicated
// @Description RSS-only, not the mixed Feed Explore, subscriptions, or a personalized reading list. Snapshot reads are disabled by default and require disposable-test-only flags.
// @Tags feed
// @Produce json
// @Param page query int false "Page (>=1)"
// @Param page_size query int false "Page size (1-100)"
// @Param sort query string false "Only recent" Enums(recent)
// @Param hide_duplicates query bool false "Only true" Enums(true)
// @Success 200 {object} TimelineListResponseDTO
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Router /api/v1/feed/rss/recent [get]
func (h *Handler) getPublicRSSRecent(c *gin.Context) {
	// Fail closed: the entire endpoint is unavailable outside disposable
	// test schemas, regardless of read-path flags.
	if os.Getenv("FEED_SCALE_ISOLATED") != "yes" || os.Getenv("FEED_SNAPSHOT_RSS_HTTP_EXPERIMENT") != "yes" {
		c.Status(http.StatusNotFound)
		return
	}
	if err := c.Request.Context().Err(); err != nil {
		httpx.Error(c, err)
		return
	}
	var target struct {
		Database string `gorm:"column:database"`
		Schema   string `gorm:"column:schema"`
	}
	if err := h.service.db.WithContext(c.Request.Context()).Raw("SELECT current_database() AS database, current_schema() AS schema").Scan(&target).Error; err != nil || !snapshotCopyTargetAllowed(target.Database, target.Schema) {
		c.Status(http.StatusNotFound)
		return
	}
	// Reject unsupported/unknown filters rather than reinterpret them as global RSS.
	for key, values := range c.Request.URL.Query() {
		if len(values) != 1 {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "duplicate RSS query parameter"))
			return
		}
		switch key {
		case "page":
			v, err := strconv.Atoi(values[0])
			if err != nil || v < 1 {
				httpx.Error(c, apperr.BadRequest("validation.invalid_request", "page must be a positive integer"))
				return
			}
		case "page_size":
			v, err := strconv.Atoi(values[0])
			if err != nil || v < 1 || v > 100 {
				httpx.Error(c, apperr.BadRequest("validation.invalid_request", "page_size must be 1-100"))
				return
			}
		case "sort":
			if values[0] != "recent" {
				httpx.Error(c, apperr.BadRequest("validation.invalid_request", "RSS route supports only recent sorting"))
				return
			}
		case "hide_duplicates":
			if values[0] != "true" {
				httpx.Error(c, apperr.BadRequest("validation.invalid_request", "RSS route always deduplicates"))
				return
			}
		default:
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "RSS route does not support this filter"))
			return
		}
	}
	query := FeedQuery{Page: normalizedPageFromQuery(c), PageSize: normalizedPageSizeFromQuery(c), Sort: "recent", HideDuplicates: true}
	legacy := func(ctx context.Context, page, size int) ([]model.FeedItem, int64, error) {
		return h.service.repo.listPublicRSSRecentWithinRequestBudget(ctx, page, size)
	}
	// Future production rollout must use independent reviewed flags. These
	// experimental flags are only permitted on disposable test databases.
	user, _ := authctx.Current(c)
	eligible := eligiblePublicRecentDedupSnapshotRequest(c, query, user, snapshotReadScopeVerifiedPublicRSSOnly)
	active := eligible && os.Getenv("FEED_SNAPSHOT_READ_EXPERIMENT") == "yes"
	var (
		rows  []model.FeedItem
		total int64
		err   error
	)
	readPath := "legacy"
	if active {
		readPath = "snapshot"
		rows, total, err = h.service.repo.readPublicRecentDedupPageWithFallback(c.Request.Context(), query.Page, query.PageSize, 500*time.Millisecond, legacy, func(_ error) { readPath = "fallback" })
	} else {
		rows, total, err = legacy(c.Request.Context(), query.Page, query.PageSize)
	}
	if err != nil {
		var appErr *apperr.AppError
		if errors.As(err, &appErr) && appErr.HTTPStatus == http.StatusServiceUnavailable {
			c.Header("Retry-After", "30")
		}
		httpx.Error(c, err)
		return
	}
	// Debug-only response header for isolated end-to-end verification; never
	// export the execution mode on the normal production legacy path.
	if active {
		c.Header("X-Feed-Isolated-Read-Path", readPath)
	}
	httpx.List(c, publicRSSDTO(rows), query.Page, query.PageSize, total)
}

package handlers

import (
	"net/http"
	"time"

	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RunAdminFeedFullTextCrawl godoc
// @Summary Start a managed feed article crawl
// @Tags admin-feed-fulltext
// @Security BearerAuth
// @Produce json
// @Success 202 {object} map[string]interface{}
// @Failure 500 {object} map[string]string
// @Router /api/v1/admin/feed/fulltext/crawl [post]
func RunAdminFeedFullTextCrawl(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		settings, err := service.LoadFeedFullTextSettings(db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "settings_load_failed"})
			return
		}
		result, err := service.RunFeedReaderCrawl(db, settings, time.Now().UTC())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "crawl_prepare_failed"})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"scanned":         result.Scanned,
			"updated":         result.Updated,
			"requeued":        result.Requeued,
			"skipped":         result.Skipped,
			"worker_notified": service.RequestFullTextWorkerRun(),
		})
	}
}

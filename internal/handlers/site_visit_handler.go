package handlers

import (
	"net/http"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SiteVisitStats struct {
	Total int64 `json:"total"`
	Today int64 `json:"today"`
}

// GetSiteVisitStats godoc
// @Summary 获取站点访问统计
// @Description 返回站点累计访问量和 UTC 当日访问量。
// @Tags site
// @Produce json
// @Success 200 {object} SiteVisitStats
// @Failure 500 {object} map[string]string
// @Router /site/visits [get]
func GetSiteVisitStats(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		today := time.Now().UTC().Format("2006-01-02")
		var stats SiteVisitStats
		if err := db.Model(&model.SiteVisitDaily{}).Select("COALESCE(SUM(view_count), 0)").Scan(&stats.Total).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load site visit stats"})
			return
		}
		if err := db.Model(&model.SiteVisitDaily{}).Where("date = ?", today).Select("COALESCE(SUM(view_count), 0)").Scan(&stats.Today).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load today's site visit stats"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": stats, "message": "ok"})
	}
}

// RecordSiteVisit godoc
// @Summary 记录一次站点访问
// @Description 增加当前 UTC 日期的站点页面访问量。
// @Tags site
// @Success 204
// @Failure 500 {object} map[string]string
// @Router /site/visits [post]
func RecordSiteVisit(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		date := time.Now().UTC().Format("2006-01-02")
		entry := model.SiteVisitDaily{Date: date, ViewCount: 1}
		result := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "date"}},
			DoUpdates: clause.Assignments(map[string]any{
				"view_count": gorm.Expr("site_visit_daily.view_count + 1"),
			}),
		}).Create(&entry)
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record site visit"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

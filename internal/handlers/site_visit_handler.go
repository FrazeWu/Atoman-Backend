package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	siteVisitorBaseline int64 = 15_040
	siteVisitBaseline   int64 = 640_050
)

type SiteVisitStats struct {
	Users int64 `json:"users"`
	Total int64 `json:"total"`
	Today int64 `json:"today"`
}

type recordSiteVisitRequest struct {
	VisitorID string `json:"visitor_id"`
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
		stats.Total += siteVisitBaseline
		stats.Users = siteVisitorBaseline
		var visitors int64
		if err := db.Model(&model.SiteVisitor{}).Count(&visitors).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load site visitor stats"})
			return
		}
		stats.Users += visitors
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
		var input recordSiteVisitRequest
		if c.Request.ContentLength > 0 {
			if err := c.ShouldBindJSON(&input); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid site visit payload"})
				return
			}
		}
		visitorID := strings.TrimSpace(input.VisitorID)
		visitorHash := ""
		if visitorID != "" {
			digest := sha256.Sum256([]byte(visitorID))
			visitorHash = hex.EncodeToString(digest[:])
		}
		date := time.Now().UTC().Format("2006-01-02")
		err := db.Transaction(func(tx *gorm.DB) error {
			entry := model.SiteVisitDaily{Date: date, ViewCount: 1}
			result := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "date"}},
				DoUpdates: clause.Assignments(map[string]any{
					"view_count": gorm.Expr("site_visit_daily.view_count + 1"),
				}),
			}).Create(&entry)
			if result.Error != nil {
				return result.Error
			}
			if visitorHash != "" {
				visitor := model.SiteVisitor{ID: visitorHash, FirstSeenAt: time.Now().UTC()}
				if result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&visitor); result.Error != nil {
					return result.Error
				}
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record site visit"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

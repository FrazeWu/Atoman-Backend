package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
)

// GetVideoRSS godoc
// @Summary 获取频道视频 RSS
// @Description 输出指定频道的公开视频 RSS。
// @Tags videos
// @Produce application/rss+xml
// @Param slug path string true "频道 slug"
// @Success 200 {string} string "RSS XML"
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/channels/slug/{slug}/rss/video [get]
func GetVideoRSS(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := c.Param("slug")
		var channel model.Channel
		if err := db.Where("slug = ?", slug).First(&channel).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}

		videos, err := contentmodule.LoadVideos(db, contentmodule.VideoQuery(db).
			Where("posts.channel_id = ? AND posts.status = ? AND posts.visibility = ?", channel.ID, "published", "public").
			Order("posts.created_at DESC").Limit(100))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load videos"})
			return
		}

		scheme := c.Request.Header.Get("X-Forwarded-Proto")
		if scheme == "" {
			scheme = "https"
		}
		siteURL := fmt.Sprintf("%s://%s", scheme, c.Request.Host)

		c.Header("Content-Type", "application/rss+xml; charset=utf-8")
		c.String(http.StatusOK, buildVideoRSS(channel, videos, siteURL))
	}
}

func buildVideoRSS(ch model.Channel, videos []model.Video, siteURL string) string {
	var items strings.Builder
	for _, v := range videos {
		pubDate := v.CreatedAt.Format(time.RFC1123Z)
		enclosure := ""
		if v.StorageType == "local" {
			enclosure = fmt.Sprintf(`<enclosure url="%s" type="video/mp4"/>`, v.VideoURL)
		}
		items.WriteString(fmt.Sprintf(`
    <item>
      <title><![CDATA[%s]]></title>
      <link>%s/video/%s</link>
      <guid>%s/video/%s</guid>
      <pubDate>%s</pubDate>
      <description><![CDATA[%s]]></description>
      %s
    </item>`, v.Title, siteURL, v.ID, siteURL, v.ID, pubDate, v.Description, enclosure))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title><![CDATA[%s - Videos]]></title>
    <link>%s/channel/%s</link>
    <description><![CDATA[%s]]></description>
    %s
  </channel>
</rss>`, ch.Name, siteURL, ch.Slug, ch.Description, items.String())
}

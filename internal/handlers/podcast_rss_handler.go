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

// GetPodcastRSS godoc
// @Summary 获取播客 RSS
// @Description 输出指定频道的播客 RSS。
// @Tags podcast
// @Produce application/rss+xml
// @Param slug path string true "频道 slug"
// @Success 200 {string} string "Podcast RSS XML"
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/channels/{slug}/rss/podcast [get]
func GetPodcastRSS(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := c.Param("slug")
		var channel model.Channel
		if err := db.Where("slug = ?", slug).First(&channel).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}

		episodes, err := contentmodule.LoadPodcastEpisodes(db, contentmodule.PodcastQuery(db).
			Where("episodes.channel_id = ? AND posts.status = ? AND posts.visibility IN ?", channel.ID, "published", []string{"", "public"}).
			Order("episodes.season_number ASC, episodes.episode_number ASC"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load podcast episodes"})
			return
		}

		scheme := c.Request.Header.Get("X-Forwarded-Proto")
		if scheme == "" {
			scheme = "https"
		}
		siteURL := fmt.Sprintf("%s://%s", scheme, c.Request.Host)

		c.Header("Content-Type", "application/rss+xml; charset=utf-8")
		c.String(http.StatusOK, buildPodcastRSS(channel, episodes, siteURL))
	}
}

func buildPodcastRSS(ch model.Channel, episodes []model.PodcastEpisode, siteURL string) string {
	coverURL := ch.CoverURL
	if coverURL == "" {
		coverURL = siteURL + "/default-podcast-cover.png"
	}

	var items strings.Builder
	for _, ep := range episodes {
		if ep.Post == nil {
			continue
		}
		pubDate := ep.CreatedAt.Format(time.RFC1123Z)
		epCover := ep.EpisodeCoverURL
		if epCover == "" && ep.Post != nil && ep.Post.Collection != nil {
			epCover = ep.Post.Collection.CoverURL
		}
		if epCover == "" {
			epCover = coverURL
		}
		items.WriteString(fmt.Sprintf(`
    <item>
      <title><![CDATA[%s]]></title>
      <link>%s/podcast/%s</link>
      <guid isPermaLink="false">%s</guid>
      <pubDate>%s</pubDate>
      <description><![CDATA[%s]]></description>
      <enclosure url="%s" length="0" type="audio/mpeg"/>
      <itunes:image href="%s"/>
      <itunes:duration>%d</itunes:duration>
      <itunes:episode>%d</itunes:episode>
      <itunes:season>%d</itunes:season>
    </item>`,
			ep.Post.Title, siteURL, ep.ID, ep.ID, pubDate,
			ep.Post.Content, ep.AudioURL, epCover,
			ep.DurationSec, ep.EpisodeNumber, ep.SeasonNumber))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
     xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"
     xmlns:content="http://purl.org/rss/1.0/modules/content/">
  <channel>
    <title><![CDATA[%s]]></title>
    <link>%s/podcast/show/%s</link>
    <description><![CDATA[%s]]></description>
    <itunes:image href="%s"/>
    <language>zh-cn</language>
    %s
  </channel>
</rss>`, ch.Name, siteURL, ch.Slug, ch.Description, coverURL, items.String())
}

// UploadPodcastAudio accepts a multipart audio file and stores it locally or in S3.
// Field name: "audio". Returns { "url": "..." }.

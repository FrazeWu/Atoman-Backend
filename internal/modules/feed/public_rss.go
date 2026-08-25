package feed

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const publicRSSItemLimit = 100

// RegisterPublicRSSRoutes mounts stable, publicly consumable RSS URLs for creator content.
func RegisterPublicRSSRoutes(group *gin.RouterGroup, db *gorm.DB) {
	group.GET("/users/:username", GetPublicUserRSS(db))
	group.GET("/channels/:slug", GetPublicChannelRSS(db))
	group.GET("/collections/:id", GetPublicCollectionRSS(db))
}

type publicRSSDocument struct {
	XMLName          xml.Name             `xml:"rss"`
	Version          string               `xml:"version,attr"`
	ContentNamespace string               `xml:"xmlns:content,attr"`
	Channel          publicRSSFeedChannel `xml:"channel"`
}

type publicRSSFeedChannel struct {
	Title         string          `xml:"title"`
	Link          string          `xml:"link"`
	Description   string          `xml:"description"`
	LastBuildDate string          `xml:"lastBuildDate"`
	Items         []publicRSSItem `xml:"item"`
}

type publicRSSItem struct {
	Title       string              `xml:"title"`
	Link        string              `xml:"link"`
	GUID        publicRSSGUID       `xml:"guid"`
	PubDate     string              `xml:"pubDate"`
	Description string              `xml:"description"`
	Category    string              `xml:"category"`
	Content     string              `xml:"content:encoded,omitempty"`
	Enclosure   *publicRSSEnclosure `xml:"enclosure,omitempty"`
}

type publicRSSGUID struct {
	Value       string `xml:",chardata"`
	IsPermaLink bool   `xml:"isPermaLink,attr"`
}

type publicRSSEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type publicRSSRow struct {
	ContentID   uuid.UUID  `gorm:"column:content_id"`
	Kind        string     `gorm:"column:kind"`
	Title       string     `gorm:"column:title"`
	Summary     string     `gorm:"column:summary"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	PublishedAt *time.Time `gorm:"column:published_at"`
	BlogContent string     `gorm:"column:blog_content"`
	EpisodeID   *uuid.UUID `gorm:"column:episode_id"`
	AudioURL    string     `gorm:"column:audio_url"`
	VideoID     *uuid.UUID `gorm:"column:video_id"`
	StorageType string     `gorm:"column:storage_type"`
	VideoURL    string     `gorm:"column:video_url"`
}

type publicRSSFilter struct {
	AuthorID     *uuid.UUID
	ChannelID    *uuid.UUID
	CollectionID *uuid.UUID
}

// GetPublicUserRSS godoc
// @Summary 获取用户公开内容 RSS
// @Description 输出指定用户已发布且公开可见的博客、播客与视频内容。
// @Tags feed
// @Produce application/rss+xml
// @Param username path string true "用户名"
// @Success 200 {string} string "RSS XML"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/rss/users/{username}.xml [get]
func GetPublicUserRSS(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var user model.User
		if err := db.Where("username = ? AND is_active = ?", publicRSSParam(c, "username"), true).First(&user).Error; err != nil {
			publicRSSNotFound(c, "user not found")
			return
		}
		name := strings.TrimSpace(user.DisplayName)
		if name == "" {
			name = user.Username
		}
		baseURL := publicRSSSiteURL(c)
		servePublicRSS(c, db, publicRSSFilter{AuthorID: &user.UUID}, publicRSSFeedSource{
			Title:       name + " - Atoman",
			Link:        baseURL + "/users/" + url.PathEscape(user.Username),
			Description: name + " 的公开内容",
			UpdatedAt:   user.UpdatedAt,
		})
	}
}

// GetPublicChannelRSS godoc
// @Summary 获取频道公开内容 RSS
// @Description 输出指定频道已发布且公开可见的博客、播客与视频内容。
// @Tags feed
// @Produce application/rss+xml
// @Param slug path string true "频道 slug"
// @Success 200 {string} string "RSS XML"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/rss/channels/{slug}.xml [get]
func GetPublicChannelRSS(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var channel model.Channel
		if err := db.Where("slug = ?", publicRSSParam(c, "slug")).First(&channel).Error; err != nil {
			publicRSSNotFound(c, "channel not found")
			return
		}
		baseURL := publicRSSSiteURL(c)
		servePublicRSS(c, db, publicRSSFilter{ChannelID: &channel.ID}, publicRSSFeedSource{
			Title:       channel.Name + " - Atoman",
			Link:        baseURL + "/channels/" + url.PathEscape(channel.Slug),
			Description: publicRSSDescription(channel.Name, channel.Description),
			UpdatedAt:   channel.UpdatedAt,
		})
	}
}

// GetPublicCollectionRSS godoc
// @Summary 获取统一合集公开内容 RSS
// @Description 输出指定统一合集内已发布且公开可见的博客、播客与视频内容。
// @Tags feed
// @Produce application/rss+xml
// @Param id path string true "统一合集 UUID"
// @Success 200 {string} string "RSS XML"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/rss/collections/{id}.xml [get]
func GetPublicCollectionRSS(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		collectionID, err := uuid.Parse(publicRSSParam(c, "id"))
		if err != nil {
			publicRSSNotFound(c, "collection not found")
			return
		}
		var collection model.ContentCollection
		if err := db.First(&collection, "id = ?", collectionID).Error; err != nil {
			publicRSSNotFound(c, "collection not found")
			return
		}
		baseURL := publicRSSSiteURL(c)
		servePublicRSS(c, db, publicRSSFilter{CollectionID: &collection.ID}, publicRSSFeedSource{
			Title:       collection.Name + " - Atoman",
			Link:        fmt.Sprintf("%s/posts/collection/%s", baseURL, collection.ID),
			Description: publicRSSDescription(collection.Name, collection.Description),
			UpdatedAt:   collection.UpdatedAt,
		})
	}
}

type publicRSSFeedSource struct {
	Title       string
	Link        string
	Description string
	UpdatedAt   time.Time
}

func servePublicRSS(c *gin.Context, db *gorm.DB, filter publicRSSFilter, source publicRSSFeedSource) {
	rows, err := listPublicRSSRows(db, filter)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load rss content"})
		return
	}
	document := buildPublicRSSDocument(publicRSSSiteURL(c), source, rows)
	c.Header("Cache-Control", "public, max-age=300")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Type", "application/rss+xml; charset=utf-8")
	c.XML(http.StatusOK, document)
}

func listPublicRSSRows(db *gorm.DB, filter publicRSSFilter) ([]publicRSSRow, error) {
	query := db.Table("content_entries AS contents").
		Select(`contents.id AS content_id, contents.kind, contents.title, contents.summary,
			contents.created_at, contents.published_at, blog_extensions.content AS blog_content,
			episodes.episode_id, episodes.audio_url, videos.video_id, videos.storage_type, videos.video_url`).
		Joins("LEFT JOIN content_blog_extensions AS blog_extensions ON blog_extensions.content_id = contents.id").
		Joins("LEFT JOIN content_episode_extensions AS episodes ON episodes.content_id = contents.id").
		Joins("LEFT JOIN content_video_extensions AS videos ON videos.content_id = contents.id").
		Where("contents.deleted_at IS NULL AND contents.status = ?", "published").
		Where("COALESCE(contents.visibility, '') IN ?", []string{"", "public"})
	if filter.AuthorID != nil {
		query = query.Where("contents.author_id = ?", *filter.AuthorID)
	}
	if filter.ChannelID != nil {
		query = query.Where("contents.channel_id = ?", *filter.ChannelID)
	}
	if filter.CollectionID != nil {
		query = query.Joins("JOIN content_collection_memberships AS memberships ON memberships.content_id = contents.id").
			Where("memberships.collection_id = ?", *filter.CollectionID)
	}
	var rows []publicRSSRow
	err := query.Order("COALESCE(contents.published_at, contents.created_at) DESC").
		Order("contents.id DESC").
		Limit(publicRSSItemLimit).
		Scan(&rows).Error
	return rows, err
}

func buildPublicRSSDocument(siteURL string, source publicRSSFeedSource, rows []publicRSSRow) publicRSSDocument {
	items := make([]publicRSSItem, 0, len(rows))
	lastBuildAt := source.UpdatedAt
	for _, row := range rows {
		item, ok := publicRSSItemFromRow(siteURL, row)
		if !ok {
			continue
		}
		items = append(items, item)
		if publishedAt := publicRSSPublishedAt(row); publishedAt.After(lastBuildAt) {
			lastBuildAt = publishedAt
		}
	}
	return publicRSSDocument{
		Version:          "2.0",
		ContentNamespace: "http://purl.org/rss/1.0/modules/content/",
		Channel: publicRSSFeedChannel{
			Title:         source.Title,
			Link:          source.Link,
			Description:   source.Description,
			LastBuildDate: lastBuildAt.Format(time.RFC1123Z),
			Items:         items,
		},
	}
}

func publicRSSItemFromRow(siteURL string, row publicRSSRow) (publicRSSItem, bool) {
	var link string
	var enclosure *publicRSSEnclosure
	switch row.Kind {
	case "blog":
		link = fmt.Sprintf("%s/posts/post/%s", siteURL, row.ContentID)
	case "podcast":
		if row.EpisodeID == nil || *row.EpisodeID == uuid.Nil {
			return publicRSSItem{}, false
		}
		link = fmt.Sprintf("%s/podcasts/episode/%s", siteURL, *row.EpisodeID)
		if audioURL := strings.TrimSpace(row.AudioURL); audioURL != "" {
			enclosure = &publicRSSEnclosure{URL: audioURL, Length: 0, Type: "audio/mpeg"}
		}
	case "video":
		if row.VideoID == nil || *row.VideoID == uuid.Nil {
			return publicRSSItem{}, false
		}
		link = fmt.Sprintf("%s/videos/watch/%s", siteURL, *row.VideoID)
		if strings.EqualFold(strings.TrimSpace(row.StorageType), "local") && strings.TrimSpace(row.VideoURL) != "" {
			enclosure = &publicRSSEnclosure{URL: row.VideoURL, Length: 0, Type: "video/mp4"}
		}
	default:
		return publicRSSItem{}, false
	}
	item := publicRSSItem{
		Title:       row.Title,
		Link:        link,
		GUID:        publicRSSGUID{Value: link, IsPermaLink: true},
		PubDate:     publicRSSPublishedAt(row).Format(time.RFC1123Z),
		Description: publicRSSSummary(row),
		Category:    row.Kind,
		Enclosure:   enclosure,
	}
	if row.Kind == "blog" {
		item.Content = row.BlogContent
	}
	return item, true
}

func publicRSSPublishedAt(row publicRSSRow) time.Time {
	if row.PublishedAt != nil {
		return *row.PublishedAt
	}
	return row.CreatedAt
}

func publicRSSSummary(row publicRSSRow) string {
	summary := strings.TrimSpace(row.Summary)
	if summary == "" {
		summary = strings.TrimSpace(row.BlogContent)
	}
	return truncateRSSSummary(summary, 500)
}

func truncateRSSSummary(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func publicRSSDescription(name, description string) string {
	if description = strings.TrimSpace(description); description != "" {
		return description
	}
	return name + " 的公开内容"
}

func publicRSSSiteURL(c *gin.Context) string {
	scheme := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0])
	if scheme != "http" && scheme != "https" {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}

func publicRSSParam(c *gin.Context, key string) string {
	return strings.TrimSuffix(c.Param(key), ".xml")
}

func publicRSSNotFound(c *gin.Context, message string) {
	c.JSON(http.StatusNotFound, gin.H{"error": message})
}

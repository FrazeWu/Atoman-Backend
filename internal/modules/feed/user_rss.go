package feed

import (
	"encoding/xml"
	"net/http"
	"os"
	"time"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type RSS struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel RSSChannel `xml:"channel"`
}

type RSSChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []RSSItem `xml:"item"`
}

type RSSItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
}

// GetUserRSS godoc
// @Summary 获取用户博客 RSS
// @Description 输出指定用户的博客 RSS。
// @Tags feed
// @Produce application/rss+xml
// @Param username path string true "用户名"
// @Success 200 {string} string "RSS XML"
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/feed/rss/{username} [get]
func GetUserRSS(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		username := c.Param("username")

		var user model.User
		if err := db.Where("username = ?", username).First(&user).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}

		var posts []model.Post
		if err := db.Where("user_id = ? AND status = ?", user.UUID, "published").Order("created_at DESC").Limit(50).Find(&posts).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch posts"})
			return
		}

		baseURL := os.Getenv("BASE_URL")
		if baseURL == "" {
			baseURL = "http://localhost:8080"
		}

		rss := RSS{
			Version: "2.0",
			Channel: RSSChannel{
				Title:       user.DisplayName + " 的博客 - Atoman",
				Link:        baseURL + "/users/" + user.Username,
				Description: user.DisplayName + " 的博客订阅",
				Items:       []RSSItem{},
			},
		}

		for _, post := range posts {
			itemURL := baseURL + "/blog/posts/" + post.ID.String()
			rss.Channel.Items = append(rss.Channel.Items, RSSItem{
				Title:       post.Title,
				Link:        itemURL,
				Description: post.Summary,
				PubDate:     post.CreatedAt.Format(time.RFC1123Z),
				GUID:        itemURL,
			})
		}

		c.Header("Content-Type", "application/rss+xml")
		c.XML(http.StatusOK, rss)
	}
}

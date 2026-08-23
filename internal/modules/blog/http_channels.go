package blog

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (h *Handler) listChannels(c *gin.Context) {
	var userID *uuid.UUID
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "user_id must be a valid uuid"))
			return
		}
		userID = &parsed
	}
	channels, err := h.service.ListChannels(userID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channels)
}

func (h *Handler) getChannel(c *gin.Context) {
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	channel, err := h.service.GetChannel(channelID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) getChannelCollections(c *gin.Context) {
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	collections, err := h.service.ListCollectionsByChannel(channelID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) getChannelBySlug(c *gin.Context) {
	channel, err := h.service.GetChannelBySlug(c.Param("slug"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) getChannelCollectionsBySlug(c *gin.Context) {
	_, collections, err := h.service.ListCollectionsByChannelSlug(c.Param("slug"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) getCollection(c *gin.Context) {
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	collection, err := h.service.GetCollection(collectionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collection)
}

func (h *Handler) getChannelArticleRSS(c *gin.Context) {
	channel, err := h.service.GetChannelBySlug(c.Param("slug"))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	posts, err := loadCanonicalBlogPosts(h.service.db, canonicalBlogPostsQuery(h.service.db).
		Where("posts.channel_id = ? AND posts.status = ?", channel.ID, "published").
		Where("posts.visibility = ? OR posts.visibility = ?", "", "public").
		Order("COALESCE(posts.published_at, posts.created_at) DESC").
		Order("posts.created_at DESC").
		Order("posts.id DESC").
		Limit(50))
	if err != nil {
		httpx.Error(c, err)
		return
	}

	scheme := c.Request.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "https"
	}
	siteURL := fmt.Sprintf("%s://%s", scheme, c.Request.Host)

	c.Header("Content-Type", "application/rss+xml; charset=utf-8")
	c.String(http.StatusOK, buildArticleRSS(channel, posts, siteURL))
}

func (h *Handler) listUserCollections(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collections, err := h.service.ListUserCollections(user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collections)
}

func (h *Handler) ensureDefaultChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channel, err := h.service.CreateDefaultChannelForUser(user.ID, user.Username)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) createChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req channelInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	channel, err := h.service.CreateChannel(user, req.Name, req.Slug, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, channel)
}

func (h *Handler) updateChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req channelInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	channel, err := h.service.UpdateChannel(user, channelID, req.Name, req.Slug, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, channel)
}

func (h *Handler) deleteChannel(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteChannel(user, channelID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Channel deleted"})
}

func (h *Handler) createCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	channelID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req collectionInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	collection, err := h.service.CreateCollection(user, channelID, req.Name, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, collection)
}

func (h *Handler) updateCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	var req collectionInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	collection, err := h.service.UpdateCollection(user, collectionID, req.Name, req.Description, req.CoverURL)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, collection)
}

func (h *Handler) deleteCollection(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	collectionID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
		return
	}
	if err := h.service.DeleteCollection(user, collectionID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Collection deleted"})
}

func buildArticleRSS(ch model.Channel, posts []model.Post, siteURL string) string {
	var items strings.Builder
	for _, p := range posts {
		publishedAt := p.CreatedAt
		if p.PublishedAt != nil {
			publishedAt = *p.PublishedAt
		}
		pubDate := publishedAt.Format(time.RFC1123Z)
		summary := p.Summary
		if summary == "" {
			summary = p.Content
			if utf8.RuneCountInString(summary) > 280 {
				summary = string([]rune(summary)[:280]) + "…"
			}
		}
		authorName := ""
		if p.User != nil {
			authorName = p.User.DisplayName
			if authorName == "" {
				authorName = p.User.Username
			}
		}
		items.WriteString(fmt.Sprintf(`
    <item>
      <title>%s</title>
      <link>%s/posts/post/%s</link>
      <guid isPermaLink="true">%s/posts/post/%s</guid>
      <pubDate>%s</pubDate>
      <description>%s</description>
      <author>%s</author>
    </item>`, rssCDATA(p.Title), siteURL, p.ID, siteURL, p.ID, pubDate, rssCDATA(summary), rssCDATA(authorName)))
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>%s</title>
    <link>%s/channel/%s</link>
    <description>%s</description>
    <language>zh-cn</language>
    <lastBuildDate>%s</lastBuildDate>
    %s
  </channel>
</rss>`, rssCDATA(ch.Name), siteURL, ch.Slug, rssCDATA(ch.Description),
		time.Now().Format(time.RFC1123Z), items.String())
}

func rssCDATA(value string) string {
	return "<![CDATA[" + strings.ReplaceAll(value, "]]>", "]]]]><![CDATA[>") + "]" + "]>"
}

func ensureDefaultCollection(db *gorm.DB, channelID uuid.UUID) (*model.Collection, error) {
	var collection model.ContentCollection
	err := db.Where("channel_id = ? AND is_default = ?", channelID, true).First(&collection).Error
	if err == nil {
		result := blogCollectionDTO(collection)
		return &result, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	collection = model.ContentCollection{
		ChannelID:   channelID,
		Name:        ensureDefaultCollectionName(),
		Description: "默认合集",
		IsDefault:   true,
	}
	if err := db.Create(&collection).Error; err != nil {
		return nil, err
	}
	result := blogCollectionDTO(collection)
	return &result, nil
}

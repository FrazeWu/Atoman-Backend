package handlers

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/modules/recommendation"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"atoman/internal/storage"
)

func SetupPodcastRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	p := router.Group("/api/v1/podcast")
	{
		p.GET("/episodes", GetPodcastEpisodes(db))
		p.GET("/recommend/episodes", GetRecommendedPodcastEpisodes(db))
		p.GET("/shows/:channelSlug/episodes", GetShowEpisodes(db))
		p.GET("/episodes/:id", GetPodcastEpisode(db))
		p.GET("/bookmarks", middleware.AuthMiddleware(), GetPodcastEpisodeBookmarks(db))
		p.POST("/bookmarks", middleware.AuthMiddleware(), CreatePodcastEpisodeBookmark(db))
		p.DELETE("/bookmarks/:id", middleware.AuthMiddleware(), DeletePodcastEpisodeBookmark(db))
		p.GET("/show-bookmarks", middleware.AuthMiddleware(), GetPodcastShowBookmarks(db))
		p.POST("/show-bookmarks", middleware.AuthMiddleware(), CreatePodcastShowBookmark(db))
		p.DELETE("/show-bookmarks/:id", middleware.AuthMiddleware(), DeletePodcastShowBookmark(db))
		p.POST("/episodes", middleware.AuthMiddleware(), CreatePodcastEpisode(db))
		p.PUT("/episodes/:id", middleware.AuthMiddleware(), UpdatePodcastEpisode(db))
		p.DELETE("/episodes/:id", middleware.AuthMiddleware(), DeletePodcastEpisode(db))
		// File upload endpoints
		p.POST("/upload-audio", middleware.AuthMiddleware(), UploadPodcastAudio(s3Client))
		p.POST("/upload-cover", middleware.AuthMiddleware(), UploadPodcastCover(s3Client))
	}
	router.GET("/api/v1/channels/:slug/rss/podcast", GetPodcastRSS(db))
}

type recommendationItemDTO struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	ContentType string `json:"content_type"`
	ImageURL    string `json:"image_url"`
	TargetPath  string `json:"target_path"`
	ScoreLabel  string `json:"score_label"`
}

func parseRecommendationMode(raw string) (recommendation.Mode, error) {
	switch recommendation.Mode(strings.TrimSpace(strings.ToLower(raw))) {
	case recommendation.ModeHot:
		return recommendation.ModeHot, nil
	case recommendation.ModeFeatured:
		return recommendation.ModeFeatured, nil
	case recommendation.ModeDiscover:
		return recommendation.ModeDiscover, nil
	default:
		return "", apperr.BadRequest("validation.invalid_request", "mode must be one of hot, featured, discover")
	}
}

func GetRecommendedPodcastEpisodes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		mode, err := parseRecommendationMode(c.DefaultQuery("mode", "hot"))
		if err != nil {
			httpx.Error(c, err)
			return
		}
		page, pageSize := httpx.PageParams(c)

		var episodes []model.PodcastEpisode
		if err := db.Preload("Post.Collection").Preload("Channel").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.status = 'published' AND posts.deleted_at IS NULL").
			Where("posts.visibility = ?", "public").
			Order("podcast_episodes.created_at DESC").
			Find(&episodes).Error; err != nil {
			httpx.Error(c, err)
			return
		}

		candidates := make([]recommendation.Candidate, 0, len(episodes))
		episodeByID := make(map[string]model.PodcastEpisode, len(episodes))
		for _, episode := range episodes {
			candidates = append(candidates, recommendation.Candidate{
				Module:          "podcast",
				EntityType:      recommendation.EntityPodcast,
				EntityID:        episode.ID.String(),
				SourceKey:       episode.ChannelID.String(),
				QualityScore:    normalizePodcastRecommendationQuality(episode),
				TrendScore:      normalizePodcastRecommendationTrend(episode),
				FreshnessScore:  normalizePodcastRecommendationFreshness(episode.CreatedAt, 14*24*time.Hour),
				AuthorityScore:  0.6,
				ExposureScore:   0,
				EditorialScore:  0,
				PublishedAtUnix: episode.CreatedAt.Unix(),
			})
			episodeByID[episode.ID.String()] = episode
		}

		ranked := recommendation.RankCandidates(mode, candidates, 0)
		items := make([]recommendationItemDTO, 0, len(ranked))
		for _, rankedItem := range ranked {
			episode, ok := episodeByID[rankedItem.EntityID]
			if !ok {
				continue
			}
			title := "未命名单集"
			summary := ""
			if episode.Post != nil {
				if strings.TrimSpace(episode.Post.Title) != "" {
					title = episode.Post.Title
				}
				summary = episode.Post.Summary
			}
			items = append(items, recommendationItemDTO{
				ID:          episode.ID.String(),
				Title:       title,
				Summary:     summary,
				ContentType: "podcast",
				ImageURL:    firstNonEmpty(episode.EpisodeCoverURL, channelCoverURL(episode.Channel)),
				TargetPath:  "/podcasts/episode/" + episode.ID.String(),
				ScoreLabel:  recommendationScoreLabel(mode, rankedItem.FinalScore),
			})
		}

		total := int64(len(items))
		start := (page - 1) * pageSize
		if start > len(items) {
			start = len(items)
		}
		end := start + pageSize
		if end > len(items) {
			end = len(items)
		}
		httpx.List(c, items[start:end], page, pageSize, total)
	}
}

func normalizePodcastRecommendationQuality(episode model.PodcastEpisode) float64 {
	score := 0.35
	if episode.DurationSec > 0 {
		score += 0.15
	}
	if strings.TrimSpace(episode.EpisodeCoverURL) != "" {
		score += 0.15
	}
	if episode.Post != nil {
		score += 0.35 * clampRecommendation(float64(episode.Post.ViewCount)/100)
	}
	return clampRecommendation(score)
}

func normalizePodcastRecommendationTrend(episode model.PodcastEpisode) float64 {
	base := normalizePodcastRecommendationFreshness(episode.CreatedAt, 7*24*time.Hour)
	if episode.Post != nil {
		return clampRecommendation(0.6*base + 0.4*clampRecommendation(float64(episode.Post.ViewCount)/100))
	}
	return base
}

func normalizePodcastRecommendationFreshness(createdAt time.Time, horizon time.Duration) float64 {
	if createdAt.IsZero() || horizon <= 0 {
		return 0
	}
	age := time.Since(createdAt)
	if age <= 0 {
		return 1
	}
	return clampRecommendation(1 - float64(age)/float64(horizon))
}

func clampRecommendation(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func recommendationScoreLabel(mode recommendation.Mode, score float64) string {
	prefix := "推荐"
	switch mode {
	case recommendation.ModeHot:
		prefix = "热度"
	case recommendation.ModeFeatured:
		prefix = "精选"
	case recommendation.ModeDiscover:
		prefix = "探索"
	}
	return fmt.Sprintf("%s %.0f", prefix, math.Round(score*100))
}

func channelCoverURL(channel *model.Channel) string {
	if channel == nil {
		return ""
	}
	return channel.CoverURL
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// GetPodcastEpisodes lists all published episodes across all shows.
// GetPodcastEpisodes godoc
// @Summary 获取播客单集列表
// @Description 返回所有已发布的播客单集。
// @Tags podcast
// @Produce json
// @Param channel_id query string false "频道 UUID"
// @Param sort query string false "排序方式" Enums(latest,random)
// @Param limit query int false "返回数量上限"
// @Success 200 {array} model.PodcastEpisode
// @Router /api/v1/podcast/episodes [get]
func GetPodcastEpisodes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channelID := c.Query("channel_id")
		sort := c.DefaultQuery("sort", "latest")
		limit := boundedListLimit(c.Query("limit"), 40, 40)

		var episodes []model.PodcastEpisode
		q := db.Preload("Post.Collection").Preload("Channel").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.status = 'published' AND posts.deleted_at IS NULL")
		if channelID != "" {
			q = q.Where("podcast_episodes.channel_id = ?", channelID)
		}
		if sort == "random" {
			q = q.Order("RANDOM()")
		} else {
			q = q.Order("podcast_episodes.created_at DESC")
		}
		q.Limit(limit).Find(&episodes)
		c.JSON(http.StatusOK, episodes)
	}
}

// GetShowEpisodes returns all published episodes for a specific channel (show).
// GetShowEpisodes godoc
// @Summary 获取节目单集列表
// @Description 返回某个频道下已发布的播客单集。
// @Tags podcast
// @Produce json
// @Param channelSlug path string true "频道 slug"
// @Success 200 {object} ShowEpisodesResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/podcast/shows/{channelSlug}/episodes [get]
func GetShowEpisodes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		slug := c.Param("channelSlug")
		var channel model.Channel
		if err := db.Where("slug = ?", slug).First(&channel).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "show not found"})
			return
		}
		var episodes []model.PodcastEpisode
		db.Where("podcast_episodes.channel_id = ?", channel.ID).
			Preload("Post.Collection").Preload("Channel").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.status = 'published' AND posts.deleted_at IS NULL").
			Order("podcast_episodes.season_number ASC, podcast_episodes.episode_number ASC").
			Find(&episodes)
		c.JSON(http.StatusOK, gin.H{"channel": channel, "episodes": episodes})
	}
}

// GetPodcastEpisode returns a single episode by ID.
// GetPodcastEpisode godoc
// @Summary 获取播客单集详情
// @Description 已发布单集可公开读取；作者可读取自己的草稿。
// @Tags podcast
// @Produce json
// @Param id path string true "单集 UUID"
// @Success 200 {object} model.PodcastEpisode
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/podcast/episodes/{id} [get]
func GetPodcastEpisode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var ep model.PodcastEpisode
		query := db.Preload("Post.Collection").Preload("Channel")
		if viewer, ok := authctx.Current(c); ok {
			query = query.Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.deleted_at IS NULL AND (posts.status = 'published' OR (posts.status = 'draft' AND posts.user_id = ?))", viewer.ID)
		} else {
			query = query.Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.status = 'published' AND posts.deleted_at IS NULL")
		}
		if err := query.
			First(&ep, "podcast_episodes.id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		c.JSON(http.StatusOK, ep)
	}
}

// CreatePodcastEpisode creates a Post and linked PodcastEpisode in one transaction.
// CreatePodcastEpisode godoc
// @Summary 创建播客单集
// @Description 创建 Post 与 PodcastEpisode 记录。
// @Tags podcast
// @Accept json
// @Produce json
// @Param input body PodcastEpisodeCreateInput true "播客单集创建输入"
// @Success 201 {object} model.PodcastEpisode
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/episodes [post]
func CreatePodcastEpisode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		idVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		userID := idVal.(uuid.UUID)

		var input struct {
			ChannelID       string      `json:"channel_id" binding:"required"`
			Title           string      `json:"title" binding:"required"`
			Shownotes       string      `json:"shownotes"`
			AudioURL        string      `json:"audio_url" binding:"required"`
			DurationSec     int         `json:"duration_sec"`
			EpisodeCoverURL string      `json:"episode_cover_url"`
			SeasonNumber    int         `json:"season_number"`
			EpisodeNumber   int         `json:"episode_number"`
			Status          string      `json:"status"`
			CollectionIDs   []uuid.UUID `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		chID, err := uuid.Parse(input.ChannelID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel_id"})
			return
		}

		var channel model.Channel
		if err := db.First(&channel, "id = ?", chID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}
		if !ownsChannel(channel.UserID, userID) {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		if model.NormalizeChannelContentType(channel.ContentType) != model.ChannelContentTypePodcast {
			c.JSON(http.StatusBadRequest, gin.H{"error": "channel content type mismatch"})
			return
		}

		status := input.Status
		if status == "" {
			status = "draft"
		}
		seasonNum := input.SeasonNumber
		if seasonNum < 1 {
			seasonNum = 1
		}

		var ep model.PodcastEpisode
		txErr := db.Transaction(func(tx *gorm.DB) error {
			post := model.Post{
				UserID:    userID,
				ChannelID: &chID,
				Title:     strings.TrimSpace(input.Title),
				Content:   input.Shownotes,
				Status:    status,
			}
			if err := tx.Create(&post).Error; err != nil {
				return err
			}
			ep = model.PodcastEpisode{
				PostID:          post.ID,
				ChannelID:       chID,
				AudioURL:        input.AudioURL,
				DurationSec:     input.DurationSec,
				EpisodeCoverURL: input.EpisodeCoverURL,
				SeasonNumber:    seasonNum,
				EpisodeNumber:   input.EpisodeNumber,
			}
			if len(input.CollectionIDs) > 0 {
				if err := assignPodcastPostCollections(tx, &post, chID, input.CollectionIDs); err != nil {
					return err
				}
			}
			return tx.Create(&ep).Error
		})
		if txErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": txErr.Error()})
			return
		}

		db.Preload("Post.Collection").Preload("Channel").First(&ep, "podcast_episodes.id = ?", ep.ID)
		c.JSON(http.StatusCreated, ep)
	}
}

// UpdatePodcastEpisode updates episode metadata and shownotes.
// UpdatePodcastEpisode godoc
// @Summary 更新播客单集
// @Description 更新当前用户拥有的播客单集。
// @Tags podcast
// @Accept json
// @Produce json
// @Param id path string true "单集 UUID"
// @Param input body PodcastEpisodeUpdateInput true "播客单集更新输入"
// @Success 200 {object} model.PodcastEpisode
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/episodes/{id} [put]
func UpdatePodcastEpisode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		idVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		userID := idVal.(uuid.UUID)
		id := c.Param("id")

		var ep model.PodcastEpisode
		if err := db.Preload("Post").First(&ep, "podcast_episodes.id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		if ep.Post == nil || ep.Post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		var input struct {
			Title           *string     `json:"title"`
			Shownotes       *string     `json:"shownotes"`
			AudioURL        *string     `json:"audio_url"`
			EpisodeCoverURL *string     `json:"episode_cover_url"`
			DurationSec     *int        `json:"duration_sec"`
			SeasonNumber    *int        `json:"season_number"`
			EpisodeNumber   *int        `json:"episode_number"`
			Status          *string     `json:"status"`
			CollectionIDs   []uuid.UUID `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		postUpdates := map[string]interface{}{}
		if input.Title != nil {
			postUpdates["title"] = strings.TrimSpace(*input.Title)
		}
		if input.Shownotes != nil {
			postUpdates["content"] = *input.Shownotes
		}
		if input.Status != nil {
			postUpdates["status"] = *input.Status
		}
		epUpdates := map[string]interface{}{}
		if input.AudioURL != nil {
			epUpdates["audio_url"] = strings.TrimSpace(*input.AudioURL)
		}
		if input.EpisodeCoverURL != nil {
			epUpdates["episode_cover_url"] = *input.EpisodeCoverURL
		}
		if input.DurationSec != nil {
			epUpdates["duration_sec"] = *input.DurationSec
		}
		if input.SeasonNumber != nil {
			epUpdates["season_number"] = *input.SeasonNumber
		}
		if input.EpisodeNumber != nil {
			epUpdates["episode_number"] = *input.EpisodeNumber
		}

		statusCode := http.StatusInternalServerError
		if err := db.Transaction(func(tx *gorm.DB) error {
			if len(postUpdates) > 0 {
				if err := tx.Model(ep.Post).Updates(postUpdates).Error; err != nil {
					return err
				}
			}
			if len(epUpdates) > 0 {
				if err := tx.Model(&ep).Updates(epUpdates).Error; err != nil {
					return err
				}
			}
			if input.CollectionIDs != nil {
				if len(input.CollectionIDs) == 0 {
					if err := tx.Model(ep.Post).Association("Collections").Clear(); err != nil {
						return err
					}
				} else if err := assignPodcastPostCollections(tx, ep.Post, ep.ChannelID, input.CollectionIDs); err != nil {
					if errors.Is(err, errInvalidPodcastCollections) {
						statusCode = http.StatusBadRequest
					}
					return err
				}
			}

			return tx.Preload("Post.Collection").Preload("Channel").First(&ep, "podcast_episodes.id = ?", ep.ID).Error
		}); err != nil {
			c.JSON(statusCode, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, ep)
	}
}

// DeletePodcastEpisode soft-deletes the episode and its associated Post.
// DeletePodcastEpisode godoc
// @Summary 删除播客单集
// @Description 软删除单集及其关联 Post。
// @Tags podcast
// @Produce json
// @Param id path string true "单集 UUID"
// @Success 200 {object} MessageResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/episodes/{id} [delete]
func DeletePodcastEpisode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		idVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		userID := idVal.(uuid.UUID)
		id := c.Param("id")

		var ep model.PodcastEpisode
		if err := db.Preload("Post").First(&ep, "podcast_episodes.id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}
		if ep.Post == nil || ep.Post.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		db.Delete(&ep)
		db.Delete(ep.Post)
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	}
}

var errInvalidPodcastCollections = errors.New("存在无效合集或合集不属于当前频道")

func assignPodcastPostCollections(db *gorm.DB, post *model.Post, channelID uuid.UUID, ids []uuid.UUID) error {
	var collections []model.Collection
	if err := db.Where("id IN ? AND channel_id = ?", ids, channelID).Find(&collections).Error; err != nil {
		return err
	}
	if len(collections) != len(ids) {
		return errInvalidPodcastCollections
	}

	return db.Model(post).Association("Collections").Replace(collections)
}

// GetPodcastRSS returns a standards-compliant podcast RSS with <enclosure> tags.
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

		var episodes []model.PodcastEpisode
		db.Where("podcast_episodes.channel_id = ?", channel.ID).
			Preload("Post.Collection").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.status = 'published' AND posts.deleted_at IS NULL").
			Order("podcast_episodes.season_number ASC, podcast_episodes.episode_number ASC").
			Limit(100).Find(&episodes)

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
// UploadPodcastAudio godoc
// @Summary 上传播客音频
// @Description 上传播客音频文件，支持本地或 S3 存储。
// @Tags podcast
// @Accept mpfd
// @Produce json
// @Param audio formData file true "音频文件"
// @Success 200 {object} UploadURLResponse
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/upload-audio [post]
func UploadPodcastAudio(s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		userID := fmt.Sprintf("%v", userIDVal)

		file, header, err := c.Request.FormFile("audio")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "音频文件必填（字段名：audio）"})
			return
		}
		defer file.Close()

		ct := header.Header.Get("Content-Type")
		allowedAudio := map[string]string{
			"audio/mpeg":  ".mp3",
			"audio/mp3":   ".mp3",
			"audio/ogg":   ".ogg",
			"audio/wav":   ".wav",
			"audio/x-wav": ".wav",
			"audio/aac":   ".aac",
			"audio/m4a":   ".m4a",
			"audio/x-m4a": ".m4a",
			"audio/flac":  ".flac",
			"audio/webm":  ".webm",
		}
		ext, ok := allowedAudio[ct]
		if !ok {
			orig := strings.ToLower(header.Filename)
			switch {
			case strings.HasSuffix(orig, ".mp3"):
				ext = ".mp3"
			case strings.HasSuffix(orig, ".ogg"):
				ext = ".ogg"
			case strings.HasSuffix(orig, ".wav"):
				ext = ".wav"
			case strings.HasSuffix(orig, ".aac"):
				ext = ".aac"
			case strings.HasSuffix(orig, ".m4a"):
				ext = ".m4a"
			case strings.HasSuffix(orig, ".flac"):
				ext = ".flac"
			case strings.HasSuffix(orig, ".webm"):
				ext = ".webm"
			default:
				c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 MP3、OGG、WAV、AAC、M4A、FLAC、WebM 格式"})
				return
			}
		}
		if !podcastAudioContentAllowed(file, ct, ext) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "音频文件内容与类型不匹配"})
			return
		}

		const maxSize = 500 * 1024 * 1024 // 500 MB
		if header.Size > maxSize {
			c.JSON(http.StatusBadRequest, gin.H{"error": "音频文件不能超过 500 MB"})
			return
		}

		filename := uuid.New().String() + ext
		s3Key := "podcast/audio/" + userID + "/" + filename

		if os.Getenv("STORAGE_TYPE") == "local" {
			localDir := filepath.Join("uploads", "podcast", "audio", userID)
			if err := os.MkdirAll(localDir, 0o755); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "创建目录失败"})
				return
			}
			destPath := filepath.Join(localDir, filename)
			if err := storage.SaveFileToPath(file, destPath); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "保存音频失败"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"url": "/uploads/podcast/audio/" + userID + "/" + filename})
			return
		}

		if !requireS3(c, s3Client) {
			return
		}
		if _, err := s3Client.PutObject(&s3.PutObjectInput{
			Bucket:      aws.String(os.Getenv("S3_BUCKET")),
			Key:         aws.String(s3Key),
			Body:        file,
			ContentType: aws.String(ct),
			ACL:         aws.String("public-read"),
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "上传至存储失败"})
			return
		}
		audioURL := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + s3Key
		c.JSON(http.StatusOK, gin.H{"url": audioURL})
	}
}

// UploadPodcastCover accepts a multipart image and stores it as episode cover art.
// Field name: "cover". Returns { "url": "..." }.
// UploadPodcastCover godoc
// @Summary 上传播客封面
// @Description 上传播客单集封面图。
// @Tags podcast
// @Accept mpfd
// @Produce json
// @Param cover formData file true "封面文件"
// @Success 200 {object} UploadURLResponse
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/podcast/upload-cover [post]
func UploadPodcastCover(s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		userID := fmt.Sprintf("%v", userIDVal)

		file, header, err := c.Request.FormFile("cover")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "封面图片必填（字段名：cover）"})
			return
		}
		defer file.Close()

		ct := header.Header.Get("Content-Type")
		allowedImg := map[string]bool{
			"image/jpeg": true, "image/png": true,
			"image/webp": true, "image/gif": true,
		}
		if !allowedImg[ct] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "封面仅支持 JPEG、PNG、WebP、GIF"})
			return
		}
		if !podcastUploadContentTypeMatches(file, ct) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "封面图片内容与类型不匹配"})
			return
		}

		const maxSize = 5 * 1024 * 1024 // 5 MB
		if header.Size > maxSize {
			c.JSON(http.StatusBadRequest, gin.H{"error": "封面图片不能超过 5 MB"})
			return
		}

		ext := contentTypeToExt(ct)
		filename := uuid.New().String() + ext
		s3Key := "podcast/covers/" + userID + "/" + filename

		if os.Getenv("STORAGE_TYPE") == "local" {
			localDir := filepath.Join("uploads", "podcast", "covers", userID)
			if err := os.MkdirAll(localDir, 0o755); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "创建目录失败"})
				return
			}
			destPath := filepath.Join(localDir, filename)
			if err := storage.SaveFileToPath(file, destPath); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "保存封面失败"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"url": "/uploads/podcast/covers/" + userID + "/" + filename})
			return
		}

		if !requireS3(c, s3Client) {
			return
		}
		if _, err := s3Client.PutObject(&s3.PutObjectInput{
			Bucket:      aws.String(os.Getenv("S3_BUCKET")),
			Key:         aws.String(s3Key),
			Body:        file,
			ContentType: aws.String(ct),
			ACL:         aws.String("public-read"),
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "上传至存储失败"})
			return
		}
		coverURL := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + s3Key
		c.JSON(http.StatusOK, gin.H{"url": coverURL})
	}
}

type podcastEpisodeBookmarkInput struct {
	EpisodeID uuid.UUID `json:"episode_id" binding:"required"`
}

type podcastShowBookmarkInput struct {
	ChannelID uuid.UUID `json:"channel_id" binding:"required"`
}

func GetPodcastEpisodeBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
		var bookmarks []model.PodcastEpisodeBookmark
		query := db.Preload("Episode").Preload("Episode.Post").Preload("Episode.Channel").Where("podcast_episode_bookmarks.user_id = ?", userID)
		if sort == "popular" {
			query = query.
				Joins("JOIN podcast_episodes ON podcast_episodes.id = podcast_episode_bookmarks.episode_id").
				Joins("JOIN posts ON posts.id = podcast_episodes.post_id").
				Order("COALESCE(posts.view_count, 0) DESC").
				Order("podcast_episode_bookmarks.created_at DESC")
		} else {
			query = query.Order("podcast_episode_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch podcast bookmarks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

func CreatePodcastEpisodeBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		var input podcastEpisodeBookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var episode model.PodcastEpisode
		if err := db.First(&episode, "id = ?", input.EpisodeID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
			return
		}

		bookmark := model.PodcastEpisodeBookmark{UserID: userID, EpisodeID: input.EpisodeID}
		if err := db.Where(model.PodcastEpisodeBookmark{UserID: userID, EpisodeID: input.EpisodeID}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast bookmark"})
			return
		}
		if err := db.Preload("Episode").Preload("Episode.Post").Preload("Episode.Channel").First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast bookmark"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

func DeletePodcastEpisodeBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}
		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.PodcastEpisodeBookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete podcast bookmark"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

func GetPodcastShowBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
		var bookmarks []model.ChannelBookmark
		query := db.Preload("Channel").Where("user_id = ? AND kind = ?", userID, "podcast_show")
		if sort == "popular" {
			query = query.Order("channel_bookmarks.created_at DESC")
		} else {
			query = query.Order("channel_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch podcast show bookmarks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

func CreatePodcastShowBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		var input podcastShowBookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var channel model.Channel
		if err := db.First(&channel, "id = ?", input.ChannelID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "show not found"})
			return
		}

		bookmark := model.ChannelBookmark{UserID: userID, ChannelID: input.ChannelID, Kind: "podcast_show"}
		if err := db.Where(model.ChannelBookmark{UserID: userID, ChannelID: input.ChannelID, Kind: "podcast_show"}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast show bookmark"})
			return
		}
		if err := db.Preload("Channel").First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create podcast show bookmark"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

func DeletePodcastShowBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}
		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.ChannelBookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete podcast show bookmark"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

func podcastUploadContentTypeMatches(file interface {
	Read([]byte) (int, error)
	Seek(int64, int) (int64, error)
}, expected string) bool {
	var header [512]byte
	n, err := file.Read(header[:])
	if err != nil && err != io.EOF {
		return false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false
	}
	return http.DetectContentType(header[:n]) == expected
}

func podcastAudioContentAllowed(file interface {
	Read([]byte) (int, error)
	Seek(int64, int) (int64, error)
}, declared string, ext string) bool {
	sniffable := map[string]bool{
		"audio/mpeg":     true,
		"audio/mp3":      true,
		"audio/wav":      true,
		"audio/x-wav":    true,
		"audio/vnd.wave": true,
	}
	if !sniffable[declared] && ext != ".mp3" && ext != ".wav" {
		return true
	}

	var header [512]byte
	n, err := file.Read(header[:])
	if err != nil && err != io.EOF {
		return false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false
	}

	detected := http.DetectContentType(header[:n])
	switch ext {
	case ".mp3":
		return detected == "audio/mpeg"
	case ".wav":
		return detected == "audio/wave"
	default:
		return false
	}
}

package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
	blog "atoman/internal/modules/blog"
	"atoman/internal/modules/recommendation"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
)

type podcastPlaybackInput struct {
	Event string `json:"event" binding:"required"`
}

// RecordPodcastPlayback godoc
// @Summary 记录播客播放事件
// @Tags podcast
// @Accept json
// @Produce json
// @Param id path string true "单集 UUID"
// @Param input body podcastPlaybackInput true "播放事件"
// @Success 200 {object} map[string]bool
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/podcast/episodes/{id}/playback [post]
func RecordPodcastPlayback(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "id must be a valid uuid"))
			return
		}
		var input podcastPlaybackInput
		if err := c.ShouldBindJSON(&input); err != nil || (input.Event != "play" && input.Event != "complete") {
			httpx.Error(c, apperr.BadRequest("studio.invalid_metric", "event must be play or complete"))
			return
		}
		episode, err := contentmodule.LoadPodcastEpisode(db, contentmodule.PodcastQuery(db).
			Where("posts.status = ? AND posts.deleted_at IS NULL AND episodes.episode_id = ?", "published", id))
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				httpx.Error(c, apperr.NotFound("podcast.episode_not_found", "Episode not found"))
				return
			}
			httpx.Error(c, err)
			return
		}
		if err := studioapi.NewService(db).RecordMetricEvent(episode.ChannelID, studioapi.ModulePodcast, episode.ID, input.Event); err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.OK(c, http.StatusOK, gin.H{"ok": true})
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

		episodes, err := contentmodule.LoadPodcastEpisodes(db, contentmodule.PodcastQuery(db).
			Where("posts.status = ? AND posts.visibility = ?", "published", "public").
			Order("posts.created_at DESC"))
		if err != nil {
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
// @Param page query int false "页码；random 排序仅支持第 1 页" default(1)
// @Param limit query int false "返回数量上限"
// @Success 200 {array} model.PodcastEpisode
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/podcast/episodes [get]
func GetPodcastEpisodes(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channelID := c.Query("channel_id")
		sort := c.DefaultQuery("sort", "latest")
		page, _ := httpx.PageParams(c)
		limit := boundedListLimit(c.Query("limit"), 40, 40)
		if sort == "random" && page > 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "random sorting only supports page 1"})
			return
		}

		var episodes []model.PodcastEpisode
		q := db.Preload("Post.Collection").Preload("Channel").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.status = 'published' AND posts.deleted_at IS NULL")
		q = blog.ApplyPublishedPostListVisibility(q, currentPodcastViewerID(c))
		if channelID != "" {
			q = q.Where("podcast_episodes.channel_id = ?", channelID)
		}
		if sort == "random" {
			q = q.Order("RANDOM()")
		} else {
			q = q.Order("podcast_episodes.created_at DESC, podcast_episodes.id DESC")
		}
		if err := q.Offset(httpx.Offset(page, limit)).Limit(limit).Find(&episodes).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load podcast episodes"})
			return
		}
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
		q := db.Where("podcast_episodes.channel_id = ?", channel.ID).
			Preload("Post.Collection").Preload("Channel").
			Joins("JOIN posts ON posts.id = podcast_episodes.post_id AND posts.status = 'published' AND posts.deleted_at IS NULL")
		q = blog.ApplyPublishedPostListVisibility(q, currentPodcastViewerID(c))
		err := q.
			Order("podcast_episodes.season_number ASC, podcast_episodes.episode_number ASC").
			Find(&episodes).Error
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load show episodes"})
			return
		}
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
		if ep.Post.Status == "published" {
			allowed, err := blog.CanViewPublishedPost(db, currentPodcastViewerID(c), *ep.Post)
			if err != nil || !allowed {
				c.JSON(http.StatusNotFound, gin.H{"error": "episode not found"})
				return
			}
		}
		c.JSON(http.StatusOK, ep)
	}
}

// CreatePodcastEpisode creates a Post and linked PodcastEpisode in one transaction.
func currentPodcastViewerID(c *gin.Context) *uuid.UUID {
	viewer, ok := authctx.Current(c)
	if !ok || viewer.ID == uuid.Nil {
		return nil
	}
	return &viewer.ID
}

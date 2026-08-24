package handlers

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
	"atoman/internal/modules/recommendation"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/httpx"
)

func GetRecommendedVideoItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		mode, err := parseRecommendationMode(c.DefaultQuery("mode", "hot"))
		if err != nil {
			httpx.Error(c, err)
			return
		}
		page, pageSize := httpx.PageParams(c)

		videos, err := contentmodule.LoadVideos(db, contentmodule.VideoQuery(db).
			Where("posts.status = ? AND posts.visibility = ?", "published", "public").
			Order("videos.created_at DESC"))
		if err != nil {
			httpx.Error(c, err)
			return
		}

		candidates := make([]recommendation.Candidate, 0, len(videos))
		videoByID := make(map[string]model.Video, len(videos))
		for _, video := range videos {
			candidates = append(candidates, recommendation.Candidate{
				Module:          "video",
				EntityType:      recommendation.EntityVideo,
				EntityID:        video.ID.String(),
				SourceKey:       videoRecommendationSourceKey(video),
				QualityScore:    normalizeVideoRecommendationQuality(video),
				TrendScore:      normalizeVideoRecommendationTrend(video),
				FreshnessScore:  normalizeVideoRecommendationFreshness(video.CreatedAt, 14*24*time.Hour),
				AuthorityScore:  normalizeVideoRecommendationAuthority(video),
				ExposureScore:   0,
				EditorialScore:  0,
				PublishedAtUnix: video.CreatedAt.Unix(),
			})
			videoByID[video.ID.String()] = video
		}

		ranked := recommendation.RankCandidates(mode, candidates, 0)
		items := make([]recommendationItemDTO, 0, len(ranked))
		for _, rankedItem := range ranked {
			video, ok := videoByID[rankedItem.EntityID]
			if !ok {
				continue
			}
			videoCopy := video
			items = append(items, recommendationItemDTO{
				ID:          video.ID.String(),
				Title:       video.Title,
				Summary:     video.Description,
				ContentType: "video",
				ImageURL:    video.ThumbnailURL,
				TargetPath:  "/videos/watch/" + video.ID.String(),
				ScoreLabel:  recommendationScoreLabel(mode, rankedItem.FinalScore),
				Video:       &videoCopy,
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

func videoRecommendationSourceKey(video model.Video) string {
	if video.ChannelID != nil {
		return video.ChannelID.String()
	}
	return video.UserID.String()
}

func normalizeVideoRecommendationQuality(video model.Video) float64 {
	score := 0.35
	if strings.TrimSpace(video.ThumbnailURL) != "" {
		score += 0.15
	}
	if strings.TrimSpace(video.Description) != "" {
		score += 0.15
	}
	score += 0.35 * clampRecommendation(float64(video.ViewCount)/1000)
	return clampRecommendation(score)
}

func normalizeVideoRecommendationTrend(video model.Video) float64 {
	return clampRecommendation(0.6*normalizeVideoRecommendationFreshness(video.CreatedAt, 7*24*time.Hour) + 0.4*clampRecommendation(float64(video.ViewCount)/500))
}

func normalizeVideoRecommendationFreshness(createdAt time.Time, horizon time.Duration) float64 {
	if createdAt.IsZero() || horizon <= 0 {
		return 0
	}
	age := time.Since(createdAt)
	if age <= 0 {
		return 1
	}
	return clampRecommendation(1 - float64(age)/float64(horizon))
}

func normalizeVideoRecommendationAuthority(video model.Video) float64 {
	if video.ChannelID != nil {
		return 0.6
	}
	return 0.4
}

// GetVideos godoc
// @Summary 获取视频列表
// @Description 匿名返回公开已发布视频；有效认证按本人频道或合集筛选时也返回本人的非公开视频。
// @Tags videos
// @Produce json
// @Param channel_id query string false "频道 UUID"
// @Param collection_id query string false "合集 UUID"
// @Param tag query string false "标签"
// @Param sort query string false "排序方式" Enums(latest,popular)
// @Param page query int false "页码" default(1)
// @Param limit query int false "返回数量上限"
// @Param subscribed query bool false "仅返回当前用户订阅频道的视频"
// @Success 200 {array} model.Video
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/videos [get]
func GetVideos(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		channelID := c.Query("channel_id")
		collectionID := c.Query("collection_id")
		tag := c.Query("tag")
		sort := c.DefaultQuery("sort", "latest")
		page, _ := httpx.PageParams(c)
		limit := boundedListLimit(c.Query("limit"), 40, 40)
		subscribedOnly := c.Query("subscribed") == "true"

		viewerID := currentBlogViewerID(c)
		if subscribedOnly && viewerID == nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		q := contentmodule.VideoQuery(db)
		isOwnerView := false
		if (channelID != "" || collectionID != "") && viewerID != nil {
			if userID := c.Query("user_id"); userID != "" && userID == viewerID.String() {
				isOwnerView = true
			} else if channelID != "" {
				var channel model.Channel
				if err := db.First(&channel, "id = ?", channelID).Error; err == nil && channel.UserID != nil && *channel.UserID == *viewerID {
					isOwnerView = true
				}
			} else if collectionID != "" {
				var collection model.ContentCollection
				if err := db.Preload("Channel").First(&collection, "id = ?", collectionID).Error; err == nil && collection.Channel != nil && collection.Channel.UserID != nil && *collection.Channel.UserID == *viewerID {
					isOwnerView = true
				}
			}
		}

		if !isOwnerView {
			if viewerID == nil {
				q = q.Where("posts.status = ? AND posts.visibility = ?", "published", "public")
			} else {
				subscribedChannelIDs := db.Model(&model.FeedSource{}).
					Select("feed_sources.source_id").
					Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
					Where("subscriptions.user_id = ?", viewerID).
					Where("subscriptions.deleted_at IS NULL").
					Where("feed_sources.source_type = ?", "internal_channel")
				q = q.Where("posts.status = ? AND (posts.visibility = ? OR (posts.visibility = ? AND posts.channel_id IN (?)))",
					"published", "public", "followers", subscribedChannelIDs)
			}
		} else {
			q = q.Where("posts.author_id = ?", viewerID)
		}
		if channelID != "" {
			q = q.Where("posts.channel_id = ?", channelID)
		}
		if collectionID != "" {
			q = q.Where("EXISTS (SELECT 1 FROM content_collection_memberships memberships WHERE memberships.content_id = posts.id AND memberships.collection_id = ?)", collectionID)
		}
		if subscribedOnly {
			subscribedChannelIDs := db.Model(&model.FeedSource{}).
				Select("feed_sources.source_id").
				Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
				Where("subscriptions.user_id = ?", viewerID).
				Where("subscriptions.deleted_at IS NULL").
				Where("feed_sources.source_type = ?", "internal_channel")
			q = q.Where("posts.channel_id IN (?)", subscribedChannelIDs)
		}
		if tag != "" {
			q = q.Joins("JOIN video_tag_relations vtr ON vtr.video_id = videos.video_id").
				Joins("JOIN video_tags vt ON vt.id = vtr.tag_id AND vt.name = ?", tag)
		}
		if sort == "popular" {
			q = q.Order("videos.view_count DESC, videos.video_id DESC")
		} else {
			q = q.Order("videos.created_at DESC, videos.video_id DESC")
		}

		videos, err := contentmodule.LoadVideos(db, q.Offset(httpx.Offset(page, limit)).Limit(limit))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, videos)
	}
}

// GetVideo returns a single video by ID.
// GetVideo godoc
// @Summary 获取视频详情
// @Description 按 UUID 返回单个视频详情。
// @Tags videos
// @Produce json
// @Param id path string true "视频 UUID"
// @Success 200 {object} model.Video
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/videos/{id} [get]
func GetVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		viewerID := currentBlogViewerID(c)
		video, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", id))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		allowed, err := canViewVideo(db, viewerID, video)
		if err != nil || !allowed {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		var likeCount int64
		db.Model(&model.Like{}).Where("target_type = ? AND target_id = ?", "video", video.ID).Count(&likeCount)
		video.LikeCount = int(likeCount)
		if viewerID != nil {
			var liked int64
			db.Model(&model.Like{}).Where("user_id = ? AND target_type = ? AND target_id = ?", *viewerID, "video", video.ID).Count(&liked)
			video.Liked = liked > 0
		}
		c.JSON(http.StatusOK, video)
	}
}

func canViewVideo(db *gorm.DB, viewerID *uuid.UUID, video model.Video) (bool, error) {
	if video.Status != "published" {
		return viewerID != nil && video.UserID == *viewerID, nil
	}
	if video.UserID != uuid.Nil && viewerID != nil && video.UserID == *viewerID {
		return true, nil
	}
	switch video.Visibility {
	case "", "public":
		return true, nil
	case "private":
		return false, nil
	case "followers":
		if viewerID == nil || video.ChannelID == nil {
			return false, nil
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte("internal_channel:"+video.ChannelID.String())))
		var source model.FeedSource
		if err := db.Where("hash = ?", hash).First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		var subscription model.Subscription
		if err := db.Where("user_id = ? AND feed_source_id = ? AND deleted_at IS NULL", *viewerID, source.ID).First(&subscription).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

type VideoViewCountResponse struct {
	OK        bool `json:"ok"`
	ViewCount int  `json:"view_count"`
}

// IncrementVideoView adds 1 to view_count. No auth required.
// IncrementVideoView godoc
// @Summary 增加视频播放量
// @Description 为指定视频增加一次播放计数。
// @Tags videos
// @Produce json
// @Param id path string true "视频 UUID"
// @Success 200 {object} VideoViewCountResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/videos/{id}/view [post]
func IncrementVideoView(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		video, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).
			Where("videos.video_id = ? AND posts.status = ? AND posts.visibility = ?", id, "published", "public"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		contentID, err := contentmodule.VideoContentID(db, id)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		result := db.Model(&model.ContentVideoExtension{}).Where("content_id = ?", contentID).
			UpdateColumn("view_count", gorm.Expr("view_count + 1"))
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to increment view count"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		video.ViewCount++
		if video.ChannelID != nil {
			if err := studioapi.NewService(db).RecordMetricEvent(*video.ChannelID, studioapi.ModuleVideo, video.ID, "play"); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to record video play"})
				return
			}
		}
		c.JSON(http.StatusOK, VideoViewCountResponse{OK: true, ViewCount: video.ViewCount})
	}
}

// CreateVideo creates a new video.
// GetRecommendedVideos godoc
// @Summary 获取推荐视频
// @Description 基于同频道和同标签返回推荐视频。
// @Tags videos
// @Produce json
// @Param id path string true "视频 UUID"
// @Success 200 {array} model.Video
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/videos/{id}/recommended [get]
func GetRecommendedVideos(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		source, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", id))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}

		var tagIDs []uuid.UUID
		for _, tag := range source.Tags {
			tagIDs = append(tagIDs, tag.ID)
		}
		var channelCandidates, tagCandidates []model.Video
		if source.ChannelID != nil {
			channelCandidates, _ = contentmodule.LoadVideos(db, contentmodule.VideoQuery(db).
				Where("posts.channel_id = ? AND videos.video_id <> ? AND posts.status = ? AND posts.visibility = ?", *source.ChannelID, id, "published", "public").
				Order("videos.created_at DESC").Limit(20))
		}
		if len(tagIDs) > 0 {
			tagCandidates, _ = contentmodule.LoadVideos(db, contentmodule.VideoQuery(db).
				Joins("JOIN video_tag_relations vtr ON vtr.video_id = videos.video_id").
				Where("vtr.tag_id IN ? AND videos.video_id <> ? AND posts.status = ? AND posts.visibility = ?", tagIDs, id, "published", "public").
				Order("videos.created_at DESC").Limit(20))
		}

		scores := map[uuid.UUID]int{}
		seen := map[uuid.UUID]model.Video{}
		for _, video := range channelCandidates {
			scores[video.ID] += 60
			seen[video.ID] = video
		}
		for _, video := range tagCandidates {
			scores[video.ID] += 40
			seen[video.ID] = video
		}

		var results []model.Video
		if len(seen) == 0 {
			results, _ = contentmodule.LoadVideos(db, contentmodule.VideoQuery(db).
				Where("videos.video_id <> ? AND posts.status = ? AND posts.visibility = ?", id, "published", "public").
				Order("videos.created_at DESC").Limit(8))
			c.JSON(http.StatusOK, results)
			return
		}

		type scoredID struct {
			id    uuid.UUID
			score int
		}
		ranked := make([]scoredID, 0, len(scores))
		for videoID, score := range scores {
			ranked = append(ranked, scoredID{videoID, score})
		}
		for i := 1; i < len(ranked); i++ {
			for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
				ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
			}
		}
		if len(ranked) > 8 {
			ranked = ranked[:8]
		}
		for _, rankedVideo := range ranked {
			results = append(results, seen[rankedVideo.id])
		}
		c.JSON(http.StatusOK, results)
	}
}

// GetVideoRSS outputs a Media RSS feed for all published videos in a channel.

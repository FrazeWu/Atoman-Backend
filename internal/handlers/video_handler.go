package handlers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/modules/lifecycle"
	"atoman/internal/modules/recommendation"
	studioapi "atoman/internal/modules/studio"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"atoman/internal/service"
	"atoman/internal/storage"
)

func SetupVideoRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	v := router.Group("/api/v1/videos")
	{
		v.GET("", middleware.OptionalAuthMiddleware(), GetVideos(db))
		v.GET("/recommend/items", GetRecommendedVideoItems(db))
		v.POST("/:id/reprocess", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), ReprocessVideo(db))
		v.GET("/:id", GetVideo(db))
		v.GET("/:id/recommended", GetRecommendedVideos(db))
		v.POST("/:id/view", IncrementVideoView(db))
		v.POST("", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), CreateVideo(db))
		v.PUT("/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UpdateVideo(db))
		v.DELETE("/:id", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), DeleteVideo(db))
		v.GET("/bookmarks", middleware.AuthMiddleware(), GetVideoBookmarks(db))
		v.POST("/bookmarks", middleware.AuthMiddleware(), CreateVideoBookmark(db))
		v.DELETE("/bookmarks/:id", middleware.AuthMiddleware(), DeleteVideoBookmark(db))
		v.GET("/channel-bookmarks", middleware.AuthMiddleware(), GetChannelBookmarks(db))
		v.POST("/channel-bookmarks", middleware.AuthMiddleware(), CreateChannelBookmark(db))
		v.DELETE("/channel-bookmarks/:id", middleware.AuthMiddleware(), DeleteChannelBookmark(db))
		// File upload endpoints
		v.POST("/upload-video", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UploadVideoFile(s3Client))
		v.POST("/upload-cover", middleware.AuthMiddleware(), middleware.RequireSiteFeature(db, "video", "video.publish"), UploadVideoCover(s3Client))
	}
	// Per-channel Video RSS feed
	router.GET("/api/v1/channels/slug/:slug/rss/video", GetVideoRSS(db))
}

// ReprocessVideo godoc
// @Summary 重新处理视频预览
// @Tags videos
// @Produce json
// @Param id path string true "视频 UUID"
// @Success 200 {object} map[string]bool
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Router /api/v1/videos/{id}/reprocess [post]
func ReprocessVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var video model.Video
		if err := db.First(&video, "id = ?", c.Param("id")).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if video.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}
		if err := service.EnsureVideoPreviewJob(db, &video); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reprocess video"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func GetRecommendedVideoItems(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		mode, err := parseRecommendationMode(c.DefaultQuery("mode", "hot"))
		if err != nil {
			httpx.Error(c, err)
			return
		}
		page, pageSize := httpx.PageParams(c)

		var videos []model.Video
		if err := db.Preload("Channel").Preload("Tags").
			Where("status = ? AND visibility = ?", "published", "public").
			Order("created_at DESC").
			Find(&videos).Error; err != nil {
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
			items = append(items, recommendationItemDTO{
				ID:          video.ID.String(),
				Title:       video.Title,
				Summary:     video.Description,
				ContentType: "video",
				ImageURL:    video.ThumbnailURL,
				TargetPath:  "/videos/watch/" + video.ID.String(),
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

func boundedListLimit(raw string, fallback int, max int) int {
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	if limit > max {
		return max
	}
	return limit
}

func sniffMultipartContentType(file multipartFile) (string, error) {
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return http.DetectContentType(buffer[:n]), nil
}

type multipartFile interface {
	io.Reader
	io.Seeker
}

// UploadVideoFile accepts a multipart video file and stores it locally or in S3.
// Field name: "video". Returns { "url": "..." }.
// UploadVideoFile godoc
// @Summary 上传视频文件
// @Description 上传视频源文件，支持本地或 S3 存储。
// @Tags videos
// @Accept mpfd
// @Produce json
// @Param video formData file true "视频文件"
// @Success 200 {object} UploadURLResponse
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/upload-video [post]
func UploadVideoFile(s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := fmt.Sprintf("%v", c.MustGet("userID"))

		file, header, err := c.Request.FormFile("video")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "视频文件必填（字段名：video）"})
			return
		}
		defer file.Close()

		ct, err := sniffMultipartContentType(file)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取视频文件"})
			return
		}
		allowedVideo := map[string]string{
			"video/mp4":       ".mp4",
			"video/webm":      ".webm",
			"video/ogg":       ".ogv",
			"video/quicktime": ".mov",
			"video/x-msvideo": ".avi",
		}
		ext, ok := allowedVideo[ct]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持 MP4、WebM、MOV 格式"})
			return
		}

		const maxSize = 2 * 1024 * 1024 * 1024 // 2 GB
		if header.Size > maxSize {
			c.JSON(http.StatusBadRequest, gin.H{"error": "视频文件不能超过 2 GB"})
			return
		}

		filename := uuid.New().String() + ext
		s3Key := "video/files/" + userID + "/" + filename

		if os.Getenv("STORAGE_TYPE") == "local" {
			localDir := filepath.Join("uploads", "video", "files", userID)
			if err := os.MkdirAll(localDir, 0o755); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "创建目录失败"})
				return
			}
			destPath := filepath.Join(localDir, filename)
			if err := storage.SaveFileToPath(file, destPath); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "保存视频失败"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"url": "/uploads/video/files/" + userID + "/" + filename})
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
		videoURL := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + s3Key
		c.JSON(http.StatusOK, gin.H{"url": videoURL})
	}
}

// UploadVideoCover accepts a multipart image and stores it as video cover art.
// Field name: "cover". Returns { "url": "..." }.
// UploadVideoCover godoc
// @Summary 上传视频封面
// @Description 上传视频封面图，支持 JPEG、PNG、WebP、GIF。
// @Tags videos
// @Accept mpfd
// @Produce json
// @Param cover formData file true "封面文件"
// @Success 200 {object} UploadURLResponse
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/upload-cover [post]
func UploadVideoCover(s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := fmt.Sprintf("%v", c.MustGet("userID"))

		file, header, err := c.Request.FormFile("cover")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "封面图片必填（字段名：cover）"})
			return
		}
		defer file.Close()

		ct, err := sniffMultipartContentType(file)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法读取封面图片"})
			return
		}
		allowedImg := map[string]bool{
			"image/jpeg": true, "image/png": true,
			"image/webp": true, "image/gif": true,
		}
		if !allowedImg[ct] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "封面仅支持 JPEG、PNG、WebP、GIF"})
			return
		}

		const maxSize = 5 * 1024 * 1024 // 5 MB
		if header.Size > maxSize {
			c.JSON(http.StatusBadRequest, gin.H{"error": "封面图片不能超过 5 MB"})
			return
		}

		ext := contentTypeToExt(ct)
		filename := uuid.New().String() + ext
		s3Key := "video/covers/" + userID + "/" + filename

		if os.Getenv("STORAGE_TYPE") == "local" {
			localDir := filepath.Join("uploads", "video", "covers", userID)
			if err := os.MkdirAll(localDir, 0o755); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "创建目录失败"})
				return
			}
			destPath := filepath.Join(localDir, filename)
			if err := storage.SaveFileToPath(file, destPath); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "保存封面失败"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"url": "/uploads/video/covers/" + userID + "/" + filename})
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

// GetVideos returns published videos. Supports ?channel_id=&tag=&sort=latest|popular&page=1&limit=40
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

		q := db.Model(&model.Video{}).
			Preload("Channel").
			Preload("Tags")

		// If viewing a specific collection or channel, and we are the owner, we can see non-public videos
		// For general browsing, we only see published and public
		isOwnerView := false
		if (channelID != "" || collectionID != "") && viewerID != nil {
			// Check if user owns the channel or collection
			// Simplified: if we have a viewerID, we allow filtering by user_id in the query
			// or we check ownership. For now, let's just add logic to include private/drafts if it's the owner's request.
			if userID := c.Query("user_id"); userID != "" && userID == viewerID.String() {
				isOwnerView = true
			} else if channelID != "" {
				var channel model.Channel
				if err := db.First(&channel, "id = ?", channelID).Error; err == nil && channel.UserID != nil && *channel.UserID == *viewerID {
					isOwnerView = true
				}
			} else if collectionID != "" {
				var collection model.Collection
				if err := db.Preload("Channel").First(&collection, "id = ?", collectionID).Error; err == nil && collection.Channel != nil && collection.Channel.UserID != nil && *collection.Channel.UserID == *viewerID {
					isOwnerView = true
				}
			}
		}

		if !isOwnerView {
			q = q.Where("videos.status = ?", "published").
				Where("videos.visibility = ?", "public")
		} else {
			// In owner view, we might still want to filter by user_id explicitly if not already implied
			q = q.Where("videos.user_id = ?", viewerID)
		}

		if channelID != "" {
			q = q.Where("videos.channel_id = ?", channelID)
		}
		if collectionID != "" {
			q = q.Joins("JOIN video_collections vc ON vc.video_id = videos.id").
				Where("vc.collection_id = ?", collectionID)
		}
		if subscribedOnly {
			subscribedChannelIDs := db.Model(&model.FeedSource{}).
				Select("feed_sources.source_id").
				Joins("JOIN subscriptions ON subscriptions.feed_source_id = feed_sources.id").
				Where("subscriptions.user_id = ?", viewerID).
				Where("subscriptions.deleted_at IS NULL").
				Where("feed_sources.source_type = ?", "internal_channel")
			q = q.Where("videos.channel_id IN (?)", subscribedChannelIDs)
		}
		if tag != "" {
			q = q.Joins("JOIN video_tag_relations vtr ON vtr.video_id = videos.id").
				Joins("JOIN video_tags vt ON vt.id = vtr.tag_id AND vt.name = ?", tag)
		}
		if sort == "popular" {
			q = q.Order("videos.view_count DESC, videos.id DESC")
		} else {
			q = q.Order("videos.created_at DESC, videos.id DESC")
		}

		var videos []model.Video
		if err := q.Offset(httpx.Offset(page, limit)).Limit(limit).Find(&videos).Error; err != nil {
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
		id := c.Param("id")
		var video model.Video
		if err := db.Preload("Channel").Preload("Tags").Preload("Collections").
			Where("status = ? AND visibility = ?", "published", "public").
			First(&video, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		c.JSON(http.StatusOK, video)
	}
}

func firstPublicVideo(db *gorm.DB, id uuid.UUID, video *model.Video) error {
	return db.Where("status = ? AND visibility = ?", "published", "public").
		First(video, "id = ?", id).Error
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
		id := c.Param("id")
		publicVideo := func() *gorm.DB {
			return db.Model(&model.Video{}).
				Where("id = ? AND status = ? AND visibility = ?", id, "published", "public")
		}
		result := publicVideo().UpdateColumn("view_count", gorm.Expr("view_count + 1"))
		if result.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to increment view count"})
			return
		}
		if result.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}

		var video model.Video
		readResult := publicVideo().Select("id", "channel_id", "view_count").First(&video)
		if readResult.Error != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load view count"})
			return
		}
		if readResult.RowsAffected == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
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
// CreateVideo godoc
// @Summary 创建视频
// @Description 创建一条新视频记录并关联标签与合集。
// @Tags videos
// @Accept json
// @Produce json
// @Param input body VideoCreateInput true "视频创建输入"
// @Success 201 {object} model.Video
// @Failure 400 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos [post]
func CreateVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var input struct {
			ChannelID     *uuid.UUID  `json:"channel_id"`
			Title         string      `json:"title" binding:"required"`
			Description   string      `json:"description"`
			StorageType   string      `json:"storage_type"`
			VideoURL      string      `json:"video_url" binding:"required"`
			ThumbnailURL  string      `json:"thumbnail_url"`
			DurationSec   int         `json:"duration_sec"`
			Visibility    string      `json:"visibility"`
			Status        string      `json:"status"`
			Tags          []string    `json:"tags"`
			CollectionIDs []uuid.UUID `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		storageType := input.StorageType
		if storageType == "" {
			storageType = "external"
		}
		visibility := input.Visibility
		if visibility == "" {
			visibility = "public"
		}
		status := input.Status
		if status == "" {
			status = "draft"
		}

		if input.ChannelID != nil {
			var channel model.Channel
			if err := db.First(&channel, "id = ?", *input.ChannelID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if !ownsChannel(channel.UserID, userID) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
		}
		var channelID uuid.UUID
		if input.ChannelID != nil {
			channelID = *input.ChannelID
		}
		if err := studioapi.NewService(db).ValidateContentScope(userID, channelID, studioapi.ModuleVideo, input.CollectionIDs, status == "published"); err != nil {
			httpx.Error(c, err)
			return
		}

		video := model.Video{
			UserID:       userID,
			ChannelID:    input.ChannelID,
			Title:        strings.TrimSpace(input.Title),
			Description:  input.Description,
			StorageType:  storageType,
			VideoURL:     input.VideoURL,
			ThumbnailURL: input.ThumbnailURL,
			DurationSec:  input.DurationSec,
			Visibility:   visibility,
			Status:       status,
		}

		statusCode := http.StatusInternalServerError
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&video).Error; err != nil {
				return err
			}

			if err := service.EnsureVideoPreviewJob(tx, &video); err != nil {
				return fmt.Errorf("processing job failed: %w", err)
			}

			if len(input.Tags) > 0 {
				if err := attachVideoTags(tx, &video, input.Tags); err != nil {
					return fmt.Errorf("tags failed: %w", err)
				}
			}

			if len(input.CollectionIDs) > 0 {
				if err := assignVideoCollections(tx, &video, input.CollectionIDs); err != nil {
					statusCode = http.StatusBadRequest
					return err
				}
			}
			if status == "published" {
				lifecycleService := lifecycle.NewService(tx)
				if err := lifecycleService.ValidatePublishable("video", video.ID); err != nil {
					return err
				}
				return lifecycleService.EnqueuePublication("video", video.ID)
			}
			return nil
		}); err != nil {
			if apperr.FromError(err) != nil {
				httpx.Error(c, err)
				return
			}
			c.JSON(statusCode, gin.H{"error": err.Error()})
			return
		}

		db.Preload("Channel").Preload("Tags").Preload("Collections").First(&video, "id = ?", video.ID)
		c.JSON(http.StatusCreated, video)
	}
}

// UpdateVideo updates a video's fields.
// UpdateVideo godoc
// @Summary 更新视频
// @Description 更新当前用户拥有的视频。
// @Tags videos
// @Accept json
// @Produce json
// @Param id path string true "视频 UUID"
// @Param input body VideoUpdateInput true "视频更新输入"
// @Success 200 {object} model.Video
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/{id} [put]
func UpdateVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		id := c.Param("id")

		var video model.Video
		if err := db.Preload("Collections").First(&video, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if video.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		var input struct {
			ChannelID     *uuid.UUID  `json:"channel_id"`
			Title         *string     `json:"title"`
			Description   *string     `json:"description"`
			ThumbnailURL  *string     `json:"thumbnail_url"`
			Visibility    *string     `json:"visibility"`
			Status        *string     `json:"status"`
			Tags          []string    `json:"tags"`
			CollectionIDs []uuid.UUID `json:"collection_ids"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if input.ChannelID != nil {
			var channel model.Channel
			if err := db.First(&channel, "id = ?", *input.ChannelID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
					return
				}
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if !ownsChannel(channel.UserID, userID) {
				c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
				return
			}
		}
		effectiveChannelID := uuid.Nil
		if video.ChannelID != nil {
			effectiveChannelID = *video.ChannelID
		}
		if input.ChannelID != nil {
			effectiveChannelID = *input.ChannelID
		}
		effectiveStatus := video.Status
		wasPublished := video.Status == "published"
		if input.Status != nil {
			effectiveStatus = *input.Status
		}
		effectiveCollectionIDs := input.CollectionIDs
		if input.CollectionIDs == nil {
			effectiveCollectionIDs = make([]uuid.UUID, 0, len(video.Collections))
			for _, collection := range video.Collections {
				effectiveCollectionIDs = append(effectiveCollectionIDs, collection.ID)
			}
		}
		if err := studioapi.NewService(db).ValidateContentScope(userID, effectiveChannelID, studioapi.ModuleVideo, effectiveCollectionIDs, effectiveStatus == "published"); err != nil {
			httpx.Error(c, err)
			return
		}

		updates := map[string]interface{}{}
		if input.ChannelID != nil {
			updates["channel_id"] = *input.ChannelID
		}
		if input.Title != nil {
			updates["title"] = strings.TrimSpace(*input.Title)
		}
		if input.Description != nil {
			updates["description"] = *input.Description
		}
		if input.ThumbnailURL != nil {
			updates["thumbnail_url"] = *input.ThumbnailURL
		}
		if input.Visibility != nil {
			updates["visibility"] = *input.Visibility
		}
		if input.Status != nil {
			updates["status"] = *input.Status
		}

		statusCode := http.StatusInternalServerError
		if err := db.Transaction(func(tx *gorm.DB) error {
			if len(updates) > 0 {
				if err := tx.Model(&video).Updates(updates).Error; err != nil {
					return err
				}
				if input.ChannelID != nil {
					video.ChannelID = input.ChannelID
				}
			}

			if input.Tags != nil {
				if err := tx.Model(&video).Association("Tags").Unscoped().Clear(); err != nil {
					return err
				}
				if len(input.Tags) > 0 {
					if err := attachVideoTags(tx, &video, input.Tags); err != nil {
						return err
					}
				}
			}

			if input.CollectionIDs != nil {
				if len(input.CollectionIDs) == 0 {
					if err := tx.Model(&video).Association("Collections").Clear(); err != nil {
						return err
					}
				} else {
					if err := assignVideoCollections(tx, &video, input.CollectionIDs); err != nil {
						statusCode = http.StatusBadRequest
						return err
					}
				}
			}
			if effectiveStatus == "published" && !wasPublished {
				lifecycleService := lifecycle.NewService(tx)
				if err := lifecycleService.ValidatePublishable("video", video.ID); err != nil {
					return err
				}
				return lifecycleService.EnqueuePublication("video", video.ID)
			}
			return nil
		}); err != nil {
			if apperr.FromError(err) != nil {
				httpx.Error(c, err)
				return
			}
			c.JSON(statusCode, gin.H{"error": err.Error()})
			return
		}

		db.Preload("Channel").Preload("Tags").Preload("Collections").First(&video, "id = ?", video.ID)
		c.JSON(http.StatusOK, video)
	}
}

// DeleteVideo soft-deletes a video.
// DeleteVideo godoc
// @Summary 删除视频
// @Description 软删除当前用户拥有的视频。
// @Tags videos
// @Produce json
// @Param id path string true "视频 UUID"
// @Success 200 {object} MessageResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/{id} [delete]
func DeleteVideo(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		id := c.Param("id")

		var video model.Video
		if err := db.First(&video, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}
		if video.UserID != userID {
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
			return
		}

		db.Delete(&video)
		c.JSON(http.StatusOK, gin.H{"message": "deleted"})
	}
}

type videoBookmarkInput struct {
	VideoID uuid.UUID `json:"video_id" binding:"required"`
}

type channelBookmarkInput struct {
	ChannelID uuid.UUID `json:"channel_id" binding:"required"`
}

func GetVideoBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
		var bookmarks []model.VideoBookmark
		query := db.Preload("Video").Where("video_bookmarks.user_id = ?", userID)
		if sort == "popular" {
			query = query.
				Joins("JOIN videos ON videos.id = video_bookmarks.video_id").
				Order("videos.view_count DESC").
				Order("video_bookmarks.created_at DESC")
		} else {
			query = query.Order("video_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch video bookmarks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

func CreateVideoBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var input videoBookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var video model.Video
		if err := db.First(&video, "id = ?", input.VideoID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}

		bookmark := model.VideoBookmark{UserID: userID, VideoID: input.VideoID}
		if err := db.Where(model.VideoBookmark{UserID: userID, VideoID: input.VideoID}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create video bookmark"})
			return
		}
		if err := db.Preload("Video").First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create video bookmark"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

func DeleteVideoBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}
		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.VideoBookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete video bookmark"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

func GetChannelBookmarks(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		sort := strings.TrimSpace(c.DefaultQuery("sort", "latest"))
		var bookmarks []model.ChannelBookmark
		query := db.Preload("Channel").Where("user_id = ? AND kind = ?", userID, "video_channel")
		if sort == "popular" {
			query = query.Order("channel_bookmarks.created_at DESC")
		} else {
			query = query.Order("channel_bookmarks.created_at DESC")
		}
		if err := query.Find(&bookmarks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch channel bookmarks"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": bookmarks, "message": "ok"})
	}
}

func CreateChannelBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var input channelBookmarkInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var channel model.Channel
		if err := db.First(&channel, "id = ?", input.ChannelID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "channel not found"})
			return
		}

		bookmark := model.ChannelBookmark{UserID: userID, ChannelID: input.ChannelID, Kind: "video_channel"}
		if err := db.Where(model.ChannelBookmark{UserID: userID, ChannelID: input.ChannelID, Kind: "video_channel"}).FirstOrCreate(&bookmark).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create channel bookmark"})
			return
		}
		if err := db.Preload("Channel").First(&bookmark, "id = ?", bookmark.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create channel bookmark"})
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": bookmark, "message": "ok"})
	}
}

func DeleteChannelBookmark(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid bookmark id"})
			return
		}
		if err := db.Where("id = ? AND user_id = ?", id, userID).Delete(&model.ChannelBookmark{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete channel bookmark"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// attachVideoTags upserts VideoTag rows and links them to the video.
func attachVideoTags(db *gorm.DB, video *model.Video, names []string) error {
	var tags []model.VideoTag
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		var tag model.VideoTag
		db.Where("name = ?", name).FirstOrCreate(&tag, model.VideoTag{Name: name})
		tags = append(tags, tag)
	}
	return db.Model(video).Association("Tags").Append(tags)
}

func assignVideoCollections(db *gorm.DB, video *model.Video, ids []uuid.UUID) error {
	if video.ChannelID == nil {
		return fmt.Errorf("请选择频道后再关联合集")
	}

	var collections []model.Collection
	if err := db.Where("id IN ? AND channel_id = ?", ids, *video.ChannelID).Find(&collections).Error; err != nil {
		return err
	}
	if len(collections) != len(ids) {
		return fmt.Errorf("存在无效合集或合集不属于当前频道")
	}

	return db.Model(video).Association("Collections").Replace(collections)
}

// GetRecommendedVideos returns up to 8 recommended videos based on same channel (score 60) and same tags (score 40).
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
		id := c.Param("id")
		var source model.Video
		if err := db.Preload("Tags").First(&source, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "video not found"})
			return
		}

		var tagIDs []uuid.UUID
		for _, t := range source.Tags {
			tagIDs = append(tagIDs, t.ID)
		}

		var channelCandidates, tagCandidates []model.Video
		if source.ChannelID != nil {
			db.Model(&model.Video{}).
				Where("channel_id = ? AND id <> ? AND status = ? AND visibility = ?",
					source.ChannelID, id, "published", "public").
				Preload("Tags").Preload("Channel").Limit(20).Find(&channelCandidates)
		}
		if len(tagIDs) > 0 {
			db.Model(&model.Video{}).
				Joins("JOIN video_tag_relations vtr ON vtr.video_id = videos.id").
				Where("vtr.tag_id IN ? AND videos.id <> ? AND videos.status = ? AND videos.visibility = ?",
					tagIDs, id, "published", "public").
				Preload("Tags").Preload("Channel").Limit(20).Find(&tagCandidates)
		}

		scores := map[uuid.UUID]int{}
		seen := map[uuid.UUID]model.Video{}
		for _, v := range channelCandidates {
			scores[v.ID] += 60
			seen[v.ID] = v
		}
		for _, v := range tagCandidates {
			scores[v.ID] += 40
			seen[v.ID] = v
		}

		var results []model.Video
		if len(seen) == 0 {
			// Fallback: latest public videos
			db.Model(&model.Video{}).
				Where("id <> ? AND status = ? AND visibility = ?", id, "published", "public").
				Order("created_at DESC").Preload("Channel").Preload("Tags").Limit(8).Find(&results)
			c.JSON(http.StatusOK, results)
			return
		}

		type scoredID struct {
			id    uuid.UUID
			score int
		}
		var ranked []scoredID
		for vid, score := range scores {
			ranked = append(ranked, scoredID{vid, score})
		}
		for i := 1; i < len(ranked); i++ {
			for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
				ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
			}
		}
		if len(ranked) > 8 {
			ranked = ranked[:8]
		}
		for _, r := range ranked {
			results = append(results, seen[r.id])
		}

		c.JSON(http.StatusOK, results)
	}
}

// GetVideoRSS outputs a Media RSS feed for all published videos in a channel.
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

		var videos []model.Video
		db.Where("channel_id = ? AND status = ? AND visibility = ?",
			channel.ID, "published", "public").
			Order("created_at DESC").Limit(100).Find(&videos)

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

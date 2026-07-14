package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type uploadPurposeConfig struct {
	keyKind            string
	allowedContentType map[string]bool
	maxSize            int64
}

var uploadPurposes = map[string]uploadPurposeConfig{
	"music.cover": {
		keyKind:            "covers",
		allowedContentType: allowedImageUploadTypes(),
		maxSize:            10 * 1024 * 1024,
	},
	"music.audio": {
		keyKind: "audio",
		allowedContentType: map[string]bool{
			"audio/aac":       true,
			"audio/flac":      true,
			"audio/mpeg":      true,
			"audio/mp4":       true,
			"audio/ogg":       true,
			"audio/wav":       true,
			"audio/webm":      true,
			"audio/x-m4a":     true,
			"audio/x-wav":     true,
			"audio/vnd.wave":  true,
			"application/ogg": true,
		},
		maxSize: 200 * 1024 * 1024,
	},
	"comment.image": {
		keyKind:            "comments",
		allowedContentType: allowedImageUploadTypes(),
		maxSize:            10 * 1024 * 1024,
	},
}

type uploadAssetResponse struct {
	ID          uuid.UUID `json:"id"`
	URL         string    `json:"url"`
	Key         string    `json:"key"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
}

// SetupUploadRoutes configures S3-only generic media upload routes.
func SetupUploadRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	uploads := router.Group("/api/v1")
	uploads.Use(middleware.AuthMiddleware())
	uploads.POST("/uploads", UploadAsset(db, s3Client))
}

// UploadAsset handles media uploads for newer API clients.
// UploadAsset godoc
// @Summary 上传媒体资源
// @Description 上传音乐封面、音频或评论图片。该接口只使用 S3 兼容存储，不回退到 /uploads 本地目录。
// @Tags uploads
// @Accept mpfd
// @Produce json
// @Param file formData file true "文件"
// @Param purpose formData string true "用途：music.cover / music.audio / comment.image"
// @Success 201 {object} uploadAssetResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/uploads [post]
func UploadAsset(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		current, ok := authctx.Current(c)
		if !ok || current.ID == uuid.Nil {
			httpx.Error(c, apperr.Unauthorized("Authentication is required"))
			return
		}
		if !requireS3(c, s3Client) {
			return
		}
		bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
		urlPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
		if bucket == "" || urlPrefix == "" {
			httpx.Error(c, apperr.Internal(fmt.Errorf("s3 bucket or URL prefix is not configured")))
			return
		}

		purpose := strings.TrimSpace(c.PostForm("purpose"))
		config, ok := uploadPurposes[purpose]
		if !ok {
			httpx.Error(c, apperr.BadRequest("upload.invalid_purpose", "Unsupported upload purpose"))
			return
		}

		file, header, err := c.Request.FormFile("file")
		if err != nil {
			httpx.Error(c, apperr.BadRequest("upload.file_required", "File is required"))
			return
		}
		defer file.Close()

		contentType := strings.TrimSpace(header.Header.Get("Content-Type"))
		if !config.allowedContentType[contentType] {
			httpx.Error(c, apperr.BadRequest("upload.invalid_content_type", "Unsupported file type"))
			return
		}
		if !uploadContentMatchesDeclared(file, contentType) {
			httpx.Error(c, apperr.BadRequest("upload.content_type_mismatch", "File content does not match declared type"))
			return
		}
		if header.Size <= 0 {
			httpx.Error(c, apperr.BadRequest("upload.empty_file", "File is empty"))
			return
		}
		if header.Size > config.maxSize {
			httpx.Error(c, apperr.BadRequest("upload.file_too_large", "File exceeds the upload size limit"))
			return
		}

		filename := uniqueUploadFilename(header.Filename, contentType)
		key := storage.BuildMusicUploadKey(config.keyKind, current.ID.String(), filename, time.Now())
		if purpose == "comment.image" {
			key = storage.BuildUserMediaKey("comments", "images", current.ID.String(), filename, time.Now())
		}
		if _, err := s3Client.PutObject(&s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(key),
			Body:        file,
			ContentType: aws.String(contentType),
			ACL:         aws.String("public-read"),
		}); err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}

		url := urlPrefix + "/" + key
		asset := model.MediaAsset{
			UserID:      &current.ID,
			Purpose:     purpose,
			URL:         url,
			Key:         key,
			ContentType: contentType,
			Size:        header.Size,
		}
		if err := db.Create(&asset).Error; err != nil {
			httpx.Error(c, err)
			return
		}

		httpx.OK(c, http.StatusCreated, uploadAssetResponse{
			ID:          asset.ID,
			URL:         asset.URL,
			Key:         asset.Key,
			ContentType: asset.ContentType,
			Size:        asset.Size,
		})
	}
}

func allowedImageUploadTypes() map[string]bool {
	return map[string]bool{
		"image/jpeg": true,
		"image/png":  true,
		"image/gif":  true,
		"image/webp": true,
	}
}

func uploadContentMatchesDeclared(file interface {
	Read([]byte) (int, error)
	Seek(int64, int) (int64, error)
}, declared string) bool {
	if !strings.HasPrefix(declared, "image/") {
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

	return http.DetectContentType(header[:n]) == declared
}

func uniqueUploadFilename(originalName string, contentType string) string {
	ext := strings.ToLower(filepath.Ext(originalName))
	if ext == "" {
		ext = contentTypeToExt(contentType)
	}
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	return uuid.NewString() + ext
}

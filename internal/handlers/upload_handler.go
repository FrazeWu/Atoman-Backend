package handlers

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/musicmedia"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
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
	"user.avatar": {
		keyKind:            "avatars",
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

type avatarRestoreResponse struct {
	URL         string `json:"url"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

type avatarRestoreAvailabilityResponse struct {
	Available bool `json:"available"`
}

// SetupUploadRoutes configures S3-only generic media upload routes.
func SetupUploadRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	uploads := router.Group("/api/v1")
	uploads.Use(middleware.AuthMiddleware())
	uploads.GET("/uploads/avatar/restore-available", AvatarRestoreAvailable(db, s3Client))
	uploads.POST("/uploads/avatar/restore", RestoreUserAvatar(db, s3Client))
	uploads.POST("/uploads", UploadAsset(db, s3Client))
}

// UploadAsset handles media uploads for newer API clients.
// UploadAsset godoc
// @Summary 上传媒体资源
// @Description 上传音乐封面、音频、评论图片或用户头像。该接口只使用 S3 兼容存储，不回退到 /uploads 本地目录。
// @Tags uploads
// @Accept mpfd
// @Produce json
// @Param file formData file true "文件"
// @Param purpose formData string true "用途：music.cover / music.audio / comment.image / user.avatar"
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
		var legacyAvatarSourceKey string
		if purpose == "comment.image" {
			key = storage.BuildUserMediaKey("comments", "images", current.ID.String(), filename, time.Now())
		} else if purpose == "user.avatar" {
			key = storage.BuildUserAvatarSlotKey(current.ID.String(), storage.UserAvatarNewSlot)
			var user model.User
			if err := db.Select("uuid, avatar_url").Where("uuid = ?", current.ID).First(&user).Error; err != nil {
				httpx.Error(c, err)
				return
			}
			legacyAvatarSourceKey, err = preservePreviousUserAvatar(s3Client, bucket, urlPrefix, user)
			if err != nil {
				httpx.Error(c, apperr.Internal(err))
				return
			}
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
			_, _ = s3Client.DeleteObject(&s3.DeleteObjectInput{
				Bucket: aws.String(bucket),
				Key:    aws.String(key),
			})
			httpx.Error(c, err)
			return
		}

		if purpose == "user.avatar" {
			if err := db.Where("user_id = ? AND purpose = ? AND id <> ?", current.ID, purpose, asset.ID).Delete(&model.MediaAsset{}).Error; err != nil {
				log.Printf("failed to prune previous avatar media assets for user %s: %v", current.ID, err)
			}
			if legacyAvatarSourceKey != "" {
				if _, err := s3Client.DeleteObject(&s3.DeleteObjectInput{
					Bucket: aws.String(bucket),
					Key:    aws.String(legacyAvatarSourceKey),
				}); err != nil {
					log.Printf("failed to remove legacy avatar object %s: %v", legacyAvatarSourceKey, err)
				}
			}
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

// AvatarRestoreAvailable godoc
// @Summary 检查上一张头像
// @Description 检查当前用户是否存在可恢复的上一张头像。
// @Tags uploads
// @Produce json
// @Success 200 {object} avatarRestoreAvailabilityResponse
// @Failure 401 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/uploads/avatar/restore-available [get]
func AvatarRestoreAvailable(_ *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
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
		if bucket == "" || strings.TrimSpace(os.Getenv("S3_URL_PREFIX")) == "" {
			httpx.Error(c, apperr.Internal(fmt.Errorf("s3 bucket or URL prefix is not configured")))
			return
		}

		oldKey := storage.BuildUserAvatarSlotKey(current.ID.String(), storage.UserAvatarOldSlot)
		available, err := s3ObjectExists(s3Client, bucket, oldKey)
		if err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
		httpx.OK(c, http.StatusOK, avatarRestoreAvailabilityResponse{Available: available})
	}
}

// RestoreUserAvatar godoc
// @Summary 恢复上一张头像
// @Description 将当前头像与上一张头像交换，并将上一张头像设为当前头像。
// @Tags uploads
// @Produce json
// @Success 200 {object} avatarRestoreResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/uploads/avatar/restore [post]
func RestoreUserAvatar(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
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

		var user model.User
		if err := db.Where("uuid = ?", current.ID).First(&user).Error; err != nil {
			httpx.Error(c, err)
			return
		}

		newKey := storage.BuildUserAvatarSlotKey(current.ID.String(), storage.UserAvatarNewSlot)
		oldKey := storage.BuildUserAvatarSlotKey(current.ID.String(), storage.UserAvatarOldSlot)
		newExists, err := s3ObjectExists(s3Client, bucket, newKey)
		if err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
		oldExists, err := s3ObjectExists(s3Client, bucket, oldKey)
		if err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
		if !newExists || !oldExists {
			httpx.Error(c, apperr.BadRequest("avatar.previous_unavailable", "No previous avatar is available"))
			return
		}

		temporaryKey := storage.BuildUserAvatarSlotKey(current.ID.String(), "swap-"+uuid.NewString())
		defer func() {
			if _, deleteErr := s3Client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(temporaryKey)}); deleteErr != nil {
				log.Printf("failed to remove avatar swap object %s: %v", temporaryKey, deleteErr)
			}
		}()
		if err := copyS3Object(s3Client, bucket, newKey, temporaryKey); err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
		if err := copyS3Object(s3Client, bucket, oldKey, newKey); err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
		if err := copyS3Object(s3Client, bucket, temporaryKey, oldKey); err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}

		head, err := s3Client.HeadObject(&s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(newKey)})
		if err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
		avatarURL := urlPrefix + "/" + newKey
		if err := db.Model(&user).Updates(map[string]interface{}{"avatar_url": avatarURL}).Error; err != nil {
			httpx.Error(c, err)
			return
		}
		var asset model.MediaAsset
		if err := db.Where("user_id = ? AND purpose = ?", current.ID, "user.avatar").Order("created_at DESC").First(&asset).Error; err == nil {
			if err := db.Model(&asset).Updates(map[string]interface{}{
				"url":          avatarURL,
				"key":          newKey,
				"content_type": aws.StringValue(head.ContentType),
				"size":         aws.Int64Value(head.ContentLength),
			}).Error; err != nil {
				httpx.Error(c, err)
				return
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, err)
			return
		}

		httpx.OK(c, http.StatusOK, avatarRestoreResponse{
			URL:         avatarURL,
			Key:         newKey,
			ContentType: aws.StringValue(head.ContentType),
			Size:        aws.Int64Value(head.ContentLength),
		})
	}
}

func preservePreviousUserAvatar(client *s3.S3, bucket, urlPrefix string, user model.User) (string, error) {
	newKey := storage.BuildUserAvatarSlotKey(user.UUID.String(), storage.UserAvatarNewSlot)
	oldKey := storage.BuildUserAvatarSlotKey(user.UUID.String(), storage.UserAvatarOldSlot)
	newExists, err := s3ObjectExists(client, bucket, newKey)
	if err != nil {
		return "", err
	}
	if newExists {
		return "", copyS3Object(client, bucket, newKey, oldKey)
	}

	sourceKey, ok := configuredS3ObjectKey(user.AvatarURL, urlPrefix)
	if !ok || sourceKey == oldKey || !isUserAvatarObjectKey(user.UUID, sourceKey) {
		return "", nil
	}
	copied, err := copyS3ObjectIfExists(client, bucket, sourceKey, oldKey)
	if err != nil || !copied {
		return "", err
	}
	return sourceKey, nil
}

func configuredS3ObjectKey(rawURL, urlPrefix string) (string, bool) {
	trimmed := strings.TrimSpace(rawURL)
	prefix := strings.TrimRight(strings.TrimSpace(urlPrefix), "/")
	if trimmed == "" || prefix == "" || !strings.HasPrefix(trimmed, prefix+"/") {
		return "", false
	}
	key, err := url.PathUnescape(strings.TrimPrefix(trimmed, prefix+"/"))
	return strings.TrimLeft(key, "/"), err == nil && key != ""
}

func isUserAvatarObjectKey(userID uuid.UUID, key string) bool {
	canonicalPrefix := "users/avatars/" + userID.String() + "/"
	legacyPrefix := "users/avatars/users/" + userID.String() + "/"
	return strings.HasPrefix(key, canonicalPrefix) || strings.HasPrefix(key, legacyPrefix)
}

func copyS3ObjectIfExists(client *s3.S3, bucket, sourceKey, destinationKey string) (bool, error) {
	exists, err := s3ObjectExists(client, bucket, sourceKey)
	if err != nil || !exists {
		return false, err
	}
	return true, copyS3Object(client, bucket, sourceKey, destinationKey)
}

func copyS3Object(client *s3.S3, bucket, sourceKey, destinationKey string) error {
	copySource := strings.ReplaceAll(url.PathEscape(bucket+"/"+sourceKey), "%2F", "/")
	_, err := client.CopyObject(&s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		CopySource: aws.String(copySource),
		Key:        aws.String(destinationKey),
		ACL:        aws.String("public-read"),
	})
	return err
}

func s3ObjectExists(client *s3.S3, bucket, key string) (bool, error) {
	_, err := client.HeadObject(&s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		return true, nil
	}
	if isS3NotFound(err) {
		return false, nil
	}
	return false, err
}

func isS3NotFound(err error) bool {
	var requestFailure awserr.RequestFailure
	if errors.As(err, &requestFailure) && requestFailure.StatusCode() == http.StatusNotFound {
		return true
	}
	var awsError awserr.Error
	if errors.As(err, &awsError) {
		code := strings.ToLower(awsError.Code())
		return code == "nosuchkey" || code == "notfound" || code == "404"
	}
	return false
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
	var header [512]byte
	n, err := file.Read(header[:])
	if err != nil && err != io.EOF {
		return false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return false
	}
	if strings.HasPrefix(declared, "image/") {
		return http.DetectContentType(header[:n]) == declared
	}
	if strings.HasPrefix(declared, "audio/") || declared == "application/ogg" {
		return musicmedia.AudioHeaderMatches(header[:n], declared)
	}
	return false
}

func uniqueUploadFilename(originalName string, contentType string) string {
	ext := strings.ToLower(filepath.Ext(originalName))
	if strings.HasPrefix(contentType, "image/") {
		ext = contentTypeToExt(contentType)
	} else if ext == "" {
		ext = contentTypeToExt(contentType)
	}
	if ext == ".jpeg" {
		ext = ".jpg"
	}
	return uuid.NewString() + ext
}

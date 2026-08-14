package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/modules/lifecycle"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const videoImportPartSize int64 = 10 * 1024 * 1024
const videoImportMaxSize int64 = 2 * 1024 * 1024 * 1024

const (
	videoImportPendingUpload = "pending_upload"
	videoImportUploading     = "uploading"
	videoImportCompleting    = "completing"
	videoImportAwaiting      = "awaiting_submit"
	videoImportPublishing    = "publishing"
	videoImportPublished     = "published"
	videoImportDraft         = "draft"
	videoImportScheduled     = "scheduled"
	videoImportFailed        = "failed"
	videoImportCanceled      = "canceled"
)

var (
	errInvalidVideoImportPartNumber = errors.New("invalid video import part number")
	errInvalidVideoImportPartSize   = errors.New("invalid video import part size")
	errVideoImportNotUploading      = errors.New("video import is not uploading")
)

type VideoImportPayload struct {
	ChannelID     *uuid.UUID  `json:"channel_id"`
	Title         string      `json:"title"`
	Description   string      `json:"description"`
	ThumbnailURL  string      `json:"thumbnail_url"`
	DurationSec   int         `json:"duration_sec"`
	Visibility    string      `json:"visibility"`
	Tags          []string    `json:"tags"`
	CollectionID  *uuid.UUID  `json:"collection_id"`
	CollectionIDs []uuid.UUID `json:"collection_ids"`
}

type CreateVideoImportInput struct {
	ChannelID   *uuid.UUID `json:"channel_id"`
	FileName    string     `json:"file_name" binding:"required"`
	FileSize    int64      `json:"file_size" binding:"required"`
	ContentType string     `json:"content_type" binding:"required"`
}

type SubmitVideoImportInput struct {
	Payload     VideoImportPayload `json:"payload" binding:"required"`
	PublishMode string             `json:"publish_mode" binding:"required"`
	ScheduledAt *time.Time         `json:"scheduled_at"`
}

type CompleteVideoImportPartInput struct {
	ETag string `json:"etag" binding:"required"`
	Size int64  `json:"size" binding:"required"`
}

type videoImportPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

type VideoImportDTO struct {
	ID                 uuid.UUID          `json:"id"`
	Status             string             `json:"status"`
	FileName           string             `json:"file_name"`
	FileSize           int64              `json:"file_size"`
	ContentType        string             `json:"content_type"`
	PartSize           int64              `json:"part_size"`
	ProgressCurrent    int64              `json:"progress_current"`
	ProgressTotal      int64              `json:"progress_total"`
	CompletedParts     []int              `json:"completed_parts"`
	Payload            VideoImportPayload `json:"payload"`
	PublishMode        string             `json:"publish_mode"`
	ScheduledAt        *time.Time         `json:"scheduled_at,omitempty"`
	ErrorMessage       string             `json:"error_message"`
	TargetVideoID      *uuid.UUID         `json:"target_video_id,omitempty"`
	UploadCompletedAt  *time.Time         `json:"upload_completed_at,omitempty"`
	PublishRequestedAt *time.Time         `json:"publish_requested_at,omitempty"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
}

type VideoImportPartUploadDTO struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url"`
}

func RegisterVideoImportRoutes(group *gin.RouterGroup, db *gorm.DB, s3Client *s3.S3) {
	imports := group.Group("/imports")
	imports.POST("", CreateVideoImport(db, s3Client))
	imports.GET("", ListVideoImports(db))
	imports.GET("/:id", GetVideoImport(db))
	imports.PUT("/:id", UpdateVideoImport(db))
	imports.POST("/:id/submit", SubmitVideoImport(db))
	imports.POST("/:id/parts/:partNumber", CreateVideoImportPartUpload(db, s3Client))
	imports.POST("/:id/parts/:partNumber/complete", CompleteVideoImportPart(db))
	imports.POST("/:id/complete", CompleteVideoImport(db, s3Client))
	imports.POST("/:id/retry", RetryVideoImport(db))
	imports.DELETE("/:id", CancelVideoImport(db, s3Client))
	imports.DELETE("/:id/record", DeleteVideoImportRecord(db))
}

// registerVideoImportRoutes keeps package-local callers stable while route ownership
// moves to internal/modules/video.
func registerVideoImportRoutes(group *gin.RouterGroup, db *gorm.DB, s3Client *s3.S3) {
	RegisterVideoImportRoutes(group, db, s3Client)
}

// CreateVideoImport godoc
// @Summary 创建视频导入任务
// @Tags video-imports
// @Accept json
// @Produce json
// @Param input body CreateVideoImportInput true "视频文件信息"
// @Success 201 {object} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports [post]
func CreateVideoImport(db *gorm.DB, client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		if client == nil || strings.TrimSpace(os.Getenv("S3_BUCKET")) == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "存储服务不可用"})
			return
		}
		var input CreateVideoImportInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		ext, ok := videoImportExtension(input.FileName, input.ContentType)
		if !ok || input.FileSize <= 0 || input.FileSize > videoImportMaxSize {
			c.JSON(http.StatusBadRequest, gin.H{"error": "视频文件格式或大小无效"})
			return
		}
		userID := c.MustGet("userID").(uuid.UUID)
		id, err := uuid.NewV7()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		objectKey := fmt.Sprintf("video/imports/%s/%s/source%s", userID, id, ext)
		if input.ChannelID != nil {
			var channel model.Channel
			if err := db.First(&channel, "id = ? AND user_id = ?", *input.ChannelID, userID).Error; err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "频道无效"})
				return
			}
		}
		created, err := client.CreateMultipartUpload(&s3.CreateMultipartUploadInput{
			Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(objectKey),
			ContentType: aws.String(input.ContentType),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "无法开始上传"})
			return
		}
		session := model.VideoImportSession{
			UserID: userID, ChannelID: input.ChannelID, Status: videoImportUploading,
			FileName: filepath.Base(input.FileName), FileSize: input.FileSize, ContentType: input.ContentType,
			ObjectKey: objectKey, UploadID: aws.StringValue(created.UploadId), PartSize: videoImportPartSize,
			CompletedPartsJSON: "[]", PayloadJSON: "{}",
		}
		session.ID = id
		if err := db.Create(&session).Error; err != nil {
			_, _ = client.AbortMultipartUpload(&s3.AbortMultipartUploadInput{
				Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(objectKey), UploadId: created.UploadId,
			})
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusCreated, videoImportDTO(session))
	}
}

// ListVideoImports godoc
// @Summary 获取视频导入任务
// @Tags video-imports
// @Produce json
// @Success 200 {array} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports [get]
func ListVideoImports(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var sessions []model.VideoImportSession
		if err := db.Where("user_id = ?", userID).Order("created_at DESC").Limit(100).Find(&sessions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		items := make([]VideoImportDTO, 0, len(sessions))
		for _, session := range sessions {
			items = append(items, videoImportDTO(session))
		}
		c.JSON(http.StatusOK, items)
	}
}

// GetVideoImport godoc
// @Summary 获取视频导入任务详情
// @Tags video-imports
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Success 200 {object} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id} [get]
func GetVideoImport(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := loadVideoImport(db, c)
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		c.JSON(http.StatusOK, videoImportDTO(session))
	}
}

// UpdateVideoImport godoc
// @Summary 保存视频导入资料
// @Tags video-imports
// @Accept json
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Param input body VideoImportPayload true "视频资料"
// @Success 200 {object} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id} [put]
func UpdateVideoImport(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var payload VideoImportPayload
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		session, err := loadVideoImport(db, c)
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		if session.TargetVideoID != nil || session.Status == videoImportCanceled {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "导入任务不能再编辑"})
			return
		}
		encoded, _ := json.Marshal(payload)
		session.PayloadJSON = string(encoded)
		session.ChannelID = payload.ChannelID
		if err := db.Save(&session).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, videoImportDTO(session))
	}
}

// SubmitVideoImport godoc
// @Summary 提交视频导入任务
// @Tags video-imports
// @Accept json
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Param input body SubmitVideoImportInput true "发布资料"
// @Success 200 {object} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id}/submit [post]
func SubmitVideoImport(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input SubmitVideoImportInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := validateVideoImportSubmission(input); err != nil {
			var appErr *apperr.AppError
			if errors.As(err, &appErr) {
				httpx.Error(c, appErr)
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		session, err := loadVideoImport(db, c)
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		if session.TargetVideoID != nil || session.Status == videoImportCanceled {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "导入任务不能再次提交"})
			return
		}
		encoded, _ := json.Marshal(input.Payload)
		now := time.Now().UTC()
		updates := map[string]any{
			"payload_json": string(encoded), "channel_id": input.Payload.ChannelID,
			"publish_mode": input.PublishMode, "publish_requested_at": now,
			"scheduled_at": input.ScheduledAt, "error_message": "",
		}
		if err := db.Model(&session).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if session.UploadCompletedAt != nil {
			if err := finalizeVideoImport(db, session.ID, session.UserID); err != nil {
				videoImportHTTPError(c, err)
				return
			}
		}
		db.First(&session, "id = ?", session.ID)
		c.JSON(http.StatusOK, videoImportDTO(session))
	}
}

// CreateVideoImportPartUpload godoc
// @Summary 获取视频分片上传地址
// @Tags video-imports
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Param partNumber path int true "分片序号"
// @Success 200 {object} VideoImportPartUploadDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id}/parts/{partNumber} [post]
func CreateVideoImportPartUpload(db *gorm.DB, client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		if client == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "存储服务不可用"})
			return
		}
		session, err := loadVideoImport(db, c)
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		partNumber, err := strconv.Atoi(c.Param("partNumber"))
		maxParts := int((session.FileSize + session.PartSize - 1) / session.PartSize)
		if err != nil || partNumber < 1 || partNumber > maxParts || session.UploadCompletedAt != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分片序号无效"})
			return
		}
		req, _ := client.UploadPartRequest(&s3.UploadPartInput{
			Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(session.ObjectKey),
			UploadId: aws.String(session.UploadID), PartNumber: aws.Int64(int64(partNumber)),
		})
		url, err := req.Presign(15 * time.Minute)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "无法创建上传地址"})
			return
		}
		c.JSON(http.StatusOK, VideoImportPartUploadDTO{PartNumber: partNumber, UploadURL: url})
	}
}

// CompleteVideoImportPart godoc
// @Summary 确认视频分片上传完成
// @Tags video-imports
// @Accept json
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Param partNumber path int true "分片序号"
// @Param input body CompleteVideoImportPartInput true "分片结果"
// @Success 200 {object} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id}/parts/{partNumber}/complete [post]
func CompleteVideoImportPart(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		partNumber, err := strconv.Atoi(c.Param("partNumber"))
		var input CompleteVideoImportPartInput
		if err != nil || c.ShouldBindJSON(&input) != nil || input.Size <= 0 || strings.TrimSpace(input.ETag) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分片结果无效"})
			return
		}
		userID := c.MustGet("userID").(uuid.UUID)
		var session model.VideoImportSession
		err = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND user_id = ?", c.Param("id"), userID).First(&session).Error; err != nil {
				return err
			}
			if session.Status == videoImportCanceled || session.UploadCompletedAt != nil {
				return errVideoImportNotUploading
			}
			maxParts := int((session.FileSize + session.PartSize - 1) / session.PartSize)
			if partNumber < 1 || partNumber > maxParts {
				return errInvalidVideoImportPartNumber
			}
			expectedSize := session.PartSize
			if partNumber == maxParts {
				expectedSize = session.FileSize - int64(partNumber-1)*session.PartSize
			}
			if input.Size != expectedSize {
				return errInvalidVideoImportPartSize
			}
			parts := readVideoImportParts(session.CompletedPartsJSON)
			part := videoImportPart{PartNumber: partNumber, ETag: input.ETag, Size: input.Size}
			replaced := false
			for index := range parts {
				if parts[index].PartNumber == partNumber {
					parts[index], replaced = part, true
					break
				}
			}
			if !replaced {
				parts = append(parts, part)
			}
			sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
			encoded, _ := json.Marshal(parts)
			session.CompletedPartsJSON = string(encoded)
			session.Status = videoImportUploading
			return tx.Save(&session).Error
		})
		if errors.Is(err, errInvalidVideoImportPartNumber) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分片序号无效"})
			return
		}
		if errors.Is(err, errInvalidVideoImportPartSize) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "分片大小无效"})
			return
		}
		if errors.Is(err, errVideoImportNotUploading) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "导入任务不能再确认分片"})
			return
		}
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		if err := db.First(&session, "id = ?", session.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, videoImportDTO(session))
	}
}

// CompleteVideoImport godoc
// @Summary 完成视频文件上传
// @Tags video-imports
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Success 200 {object} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id}/complete [post]
func CompleteVideoImport(db *gorm.DB, client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		if client == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "存储服务不可用"})
			return
		}
		session, err := loadVideoImport(db, c)
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		if session.UploadCompletedAt == nil {
			parts := readVideoImportParts(session.CompletedPartsJSON)
			expectedParts := int((session.FileSize + session.PartSize - 1) / session.PartSize)
			if len(parts) != expectedParts || videoImportProgress(parts) != session.FileSize {
				c.JSON(http.StatusBadRequest, gin.H{"error": "视频分片尚未全部上传"})
				return
			}
			_ = db.Model(&session).Update("status", videoImportCompleting).Error
			completed := make([]*s3.CompletedPart, 0, len(parts))
			for _, part := range parts {
				completed = append(completed, &s3.CompletedPart{ETag: aws.String(part.ETag), PartNumber: aws.Int64(int64(part.PartNumber))})
			}
			_, completeErr := client.CompleteMultipartUpload(&s3.CompleteMultipartUploadInput{
				Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(session.ObjectKey), UploadId: aws.String(session.UploadID),
				MultipartUpload: &s3.CompletedMultipartUpload{Parts: completed},
			})
			if completeErr != nil {
				head, headErr := client.HeadObject(&s3.HeadObjectInput{Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(session.ObjectKey)})
				if headErr != nil || aws.Int64Value(head.ContentLength) != session.FileSize {
					_ = db.Model(&session).Updates(map[string]any{"status": videoImportFailed, "error_message": "完成文件上传失败"}).Error
					c.JSON(http.StatusInternalServerError, gin.H{"error": "完成文件上传失败"})
					return
				}
			}
			if err := validateStoredVideoImport(client, session); err != nil {
				_, _ = client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(session.ObjectKey)})
				_ = db.Model(&session).Updates(map[string]any{"status": videoImportFailed, "error_message": "视频文件内容无效"}).Error
				c.JSON(http.StatusBadRequest, gin.H{"error": "视频文件内容无效"})
				return
			}
			now := time.Now().UTC()
			status := videoImportAwaiting
			if session.PublishRequestedAt != nil {
				status = videoImportPublishing
			}
			if err := db.Model(&session).Updates(map[string]any{"upload_completed_at": now, "status": status, "error_message": ""}).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			session.UploadCompletedAt = &now
		}
		if session.PublishRequestedAt != nil {
			if err := finalizeVideoImport(db, session.ID, session.UserID); err != nil {
				videoImportHTTPError(c, err)
				return
			}
		}
		db.First(&session, "id = ?", session.ID)
		c.JSON(http.StatusOK, videoImportDTO(session))
	}
}

// RetryVideoImport godoc
// @Summary 重试视频导入发布
// @Tags video-imports
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Success 200 {object} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id}/retry [post]
func RetryVideoImport(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := loadVideoImport(db, c)
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		if session.UploadCompletedAt == nil || session.PublishRequestedAt == nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "任务尚不能重试发布"})
			return
		}
		if err := finalizeVideoImport(db, session.ID, session.UserID); err != nil {
			videoImportHTTPError(c, err)
			return
		}
		db.First(&session, "id = ?", session.ID)
		c.JSON(http.StatusOK, videoImportDTO(session))
	}
}

// CancelVideoImport godoc
// @Summary 取消视频导入任务
// @Tags video-imports
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Success 200 {object} VideoImportDTO
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id} [delete]
func CancelVideoImport(db *gorm.DB, client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := loadVideoImport(db, c)
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		if session.TargetVideoID != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "已创建的视频不能取消"})
			return
		}
		if client != nil && session.UploadCompletedAt == nil {
			_, _ = client.AbortMultipartUpload(&s3.AbortMultipartUploadInput{
				Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(session.ObjectKey), UploadId: aws.String(session.UploadID),
			})
		} else if client != nil && session.TargetVideoID == nil {
			_, _ = client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(session.ObjectKey)})
		}
		if err := db.Model(&session).Updates(map[string]any{"status": videoImportCanceled, "error_message": ""}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		session.Status = videoImportCanceled
		c.JSON(http.StatusOK, videoImportDTO(session))
	}
}

// DeleteVideoImportRecord godoc
// @Summary 删除视频导入记录
// @Tags video-imports
// @Produce json
// @Param id path string true "导入任务 UUID"
// @Success 200 {object} map[string]bool
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/imports/{id}/record [delete]
func DeleteVideoImportRecord(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, err := loadVideoImport(db, c)
		if err != nil {
			videoImportHTTPError(c, err)
			return
		}
		if session.Status != videoImportCanceled && session.Status != videoImportPublished && session.Status != videoImportDraft && session.Status != videoImportScheduled {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "请先取消任务"})
			return
		}
		if err := db.Delete(&session).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func finalizeVideoImport(db *gorm.DB, id, userID uuid.UUID) error {
	err := db.Transaction(func(tx *gorm.DB) error {
		var session model.VideoImportSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, userID).First(&session).Error; err != nil {
			return err
		}
		if session.TargetVideoID != nil {
			return nil
		}
		if session.UploadCompletedAt == nil || session.PublishRequestedAt == nil {
			return nil
		}
		session.Status = videoImportPublishing
		session.ErrorMessage = ""
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		payload := readVideoImportPayload(session.PayloadJSON)
		status := session.PublishMode
		if status == "scheduled" {
			status = "draft"
		}
		video, _, err := createVideoRecord(tx, userID, videoCreateParams{
			ChannelID: payload.ChannelID, Title: payload.Title, Description: payload.Description,
			StorageType: "local", VideoURL: videoImportPublicURL(session.ObjectKey), ThumbnailURL: payload.ThumbnailURL,
			DurationSec: payload.DurationSec,
			Visibility:  payload.Visibility, Status: status, Tags: payload.Tags,
			CollectionID: payload.CollectionID, CollectionIDs: payload.CollectionIDs,
		})
		if err != nil {
			return err
		}
		if session.PublishMode == "scheduled" {
			if session.ScheduledAt == nil {
				return errors.New("scheduled_at is required")
			}
			if _, err := lifecycle.NewService(tx).ScheduleContent(authctx.CurrentUser{ID: userID}, lifecycle.ScheduleInput{
				Module: "video", ContentID: video.ID, PublishAt: *session.ScheduledAt,
			}); err != nil {
				return err
			}
		}
		session.TargetVideoID = &video.ID
		session.Status = map[string]string{"published": videoImportPublished, "scheduled": videoImportScheduled, "draft": videoImportDraft}[session.PublishMode]
		return tx.Save(&session).Error
	})
	if err != nil {
		_ = db.Model(&model.VideoImportSession{}).Where("id = ? AND user_id = ?", id, userID).
			Updates(map[string]any{"status": videoImportFailed, "error_message": err.Error()}).Error
	}
	return err
}

func validateVideoImportSubmission(input SubmitVideoImportInput) error {
	if strings.TrimSpace(input.Payload.Title) == "" || input.Payload.ChannelID == nil {
		return errors.New("标题和频道不能为空")
	}
	if input.PublishMode != "draft" && input.PublishMode != "published" && input.PublishMode != "scheduled" {
		return errors.New("发布方式无效")
	}
	if len(input.Payload.CollectionIDs) > 1 {
		return apperr.Unprocessable("studio.multiple_collections_not_supported", "Only one collection can be selected")
	}
	if input.Payload.CollectionID != nil && len(input.Payload.CollectionIDs) == 1 && *input.Payload.CollectionID != input.Payload.CollectionIDs[0] {
		return apperr.BadRequest("studio.collection_input_conflict", "collection_id and collection_ids must identify the same collection")
	}
	if input.PublishMode == "scheduled" && (input.ScheduledAt == nil || !input.ScheduledAt.After(time.Now())) {
		return errors.New("请选择未来的发布时间")
	}
	return nil
}

func loadVideoImport(db *gorm.DB, c *gin.Context) (model.VideoImportSession, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return model.VideoImportSession{}, gorm.ErrRecordNotFound
	}
	userID := c.MustGet("userID").(uuid.UUID)
	var session model.VideoImportSession
	err = db.Where("id = ? AND user_id = ?", id, userID).First(&session).Error
	return session, err
}

func videoImportHTTPError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "导入任务不存在"})
		return
	}
	if apperr.FromError(err) != nil {
		httpx.Error(c, err)
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func videoImportDTO(session model.VideoImportSession) VideoImportDTO {
	parts := readVideoImportParts(session.CompletedPartsJSON)
	completed := make([]int, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, part.PartNumber)
	}
	return VideoImportDTO{
		ID: session.ID, Status: session.Status, FileName: session.FileName, FileSize: session.FileSize,
		ContentType: session.ContentType, PartSize: session.PartSize, ProgressCurrent: videoImportProgress(parts),
		ProgressTotal: session.FileSize, CompletedParts: completed, Payload: readVideoImportPayload(session.PayloadJSON),
		PublishMode: session.PublishMode, ScheduledAt: session.ScheduledAt, ErrorMessage: session.ErrorMessage,
		TargetVideoID: session.TargetVideoID, UploadCompletedAt: session.UploadCompletedAt,
		PublishRequestedAt: session.PublishRequestedAt, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
	}
}

func readVideoImportParts(raw string) []videoImportPart {
	var parts []videoImportPart
	if json.Unmarshal([]byte(raw), &parts) != nil {
		return []videoImportPart{}
	}
	return parts
}

func readVideoImportPayload(raw string) VideoImportPayload {
	var payload VideoImportPayload
	_ = json.Unmarshal([]byte(raw), &payload)
	return payload
}

func videoImportProgress(parts []videoImportPart) int64 {
	var total int64
	for _, part := range parts {
		total += part.Size
	}
	return total
}

func videoImportExtension(fileName, contentType string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(filepath.Base(fileName)))
	allowed := map[string]string{"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov"}
	expected, ok := allowed[strings.ToLower(strings.TrimSpace(contentType))]
	return ext, ok && ext == expected
}

func videoImportPublicURL(key string) string {
	return strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + strings.TrimLeft(key, "/")
}

func validateStoredVideoImport(client *s3.S3, session model.VideoImportSession) error {
	object, err := client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(os.Getenv("S3_BUCKET")), Key: aws.String(session.ObjectKey), Range: aws.String("bytes=0-31"),
	})
	if err != nil {
		return err
	}
	defer object.Body.Close()
	buffer := make([]byte, 32)
	n, _ := object.Body.Read(buffer)
	buffer = buffer[:n]
	ext := strings.ToLower(filepath.Ext(session.FileName))
	switch ext {
	case ".mp4", ".mov":
		return assertVideoSignature(len(buffer) >= 8 && bytes.Equal(buffer[4:8], []byte("ftyp")))
	case ".webm":
		return assertVideoSignature(len(buffer) >= 4 && bytes.Equal(buffer[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}))
	default:
		return errors.New("unsupported video format")
	}
}

func assertVideoSignature(valid bool) error {
	if !valid {
		return errors.New("invalid video signature")
	}
	return nil
}

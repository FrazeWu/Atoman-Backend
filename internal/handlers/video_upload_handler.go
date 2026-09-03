package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"atoman/internal/storage"
)

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

// UploadVideoFile accepts a multipart video file and stores it in object storage.
// Field name: "video". Returns { "url": "..." }.
// UploadVideoFile godoc
// @Summary 上传视频文件
// @Description 上传视频源文件到 R2 兼容对象存储。
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

		if !requireS3(c, s3Client) {
			return
		}
		if err := storage.PutPublicObject(s3Client, os.Getenv("S3_BUCKET"), s3Key, ct, file); err != nil {
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

		if !requireS3(c, s3Client) {
			return
		}
		if err := storage.PutPublicObject(s3Client, os.Getenv("S3_BUCKET"), s3Key, ct, file); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "上传至存储失败"})
			return
		}
		coverURL := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + s3Key
		c.JSON(http.StatusOK, gin.H{"url": coverURL})
	}
}

// UploadVideoSubtitle stores a verified WebVTT subtitle file in R2.
func UploadVideoSubtitle(s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		file, header, err := c.Request.FormFile("subtitle")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "字幕文件必填（字段名：subtitle）"})
			return
		}
		defer file.Close()
		if header.Size > 5*1024*1024 || !strings.HasSuffix(strings.ToLower(header.Filename), ".vtt") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "仅支持不超过 5 MB 的 VTT 字幕"})
			return
		}
		if !requireS3(c, s3Client) {
			return
		}
		userID := fmt.Sprintf("%v", c.MustGet("userID"))
		key := "video/subtitles/" + userID + "/" + uuid.New().String() + ".vtt"
		if err := storage.PutPublicObject(s3Client, os.Getenv("S3_BUCKET"), key, "text/vtt", file); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "上传字幕失败"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"url": strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + key})
	}
}

// GetVideos returns published videos. Supports ?channel_id=&tag=&sort=latest|popular&page=1&limit=40

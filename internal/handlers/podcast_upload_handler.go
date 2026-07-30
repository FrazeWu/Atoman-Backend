package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"atoman/internal/storage"
)

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

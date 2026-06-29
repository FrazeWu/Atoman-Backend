package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/storage"
)

// SetupBlogUploadRoutes configures blog media upload routes
func SetupBlogUploadRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	blog := router.Group("/api/v1/blog")
	protected := blog.Group("")
	protected.Use(middleware.AuthMiddleware())
	{
		protected.POST("/upload-image", UploadBlogImage(db, s3Client))
	}
}

// UploadBlogImage handles image uploads for blog posts
// UploadBlogImage godoc
// @Summary 上传博客图片
// @Description 上传博客编辑器使用的图片，支持 JPEG、PNG、GIF、WebP。
// @Tags blog-upload
// @Accept mpfd
// @Produce json
// @Param image formData file true "图片文件"
// @Success 200 {object} UploadURLResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/blog/upload-image [post]
func UploadBlogImage(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get authenticated user ID
		userIDVal, exists := c.Get("user_id")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}
		userID := fmt.Sprintf("%v", userIDVal)

		// Parse multipart image field
		file, header, err := c.Request.FormFile("image")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Image file is required (field name: image)"})
			return
		}
		defer file.Close()

		// Validate content type — use header from HTTP request, not the user-supplied one
		contentType := header.Header.Get("Content-Type")
		allowed := map[string]bool{
			"image/jpeg": true,
			"image/png":  true,
			"image/gif":  true,
			"image/webp": true,
		}
		if !allowed[contentType] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only JPEG, PNG, GIF, and WebP images are allowed"})
			return
		}

		// Validate file size (max 5 MB)
		const maxSize = 5 * 1024 * 1024
		if header.Size > maxSize {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Image must be smaller than 5 MB"})
			return
		}

		// Derive safe extension from content type
		ext := contentTypeToExt(contentType)
		filename := uuid.New().String() + ext
		s3Key := "blog/images/" + userID + "/" + filename

		// Choose storage path
		if os.Getenv("STORAGE_TYPE") == "local" {
			localDir := filepath.Join("uploads", "blog", "images", userID)
			if err := os.MkdirAll(localDir, 0o755); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create upload directory"})
				return
			}
			destPath := filepath.Join(localDir, filename)
			if err := storage.SaveFileToPath(file, destPath); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save image"})
				return
			}
			c.JSON(http.StatusOK, gin.H{"url": "/uploads/blog/images/" + userID + "/" + filename})
			return
		}

		// S3 upload
		if !requireS3(c, s3Client) {
			return
		}
		_, err = s3Client.PutObject(&s3.PutObjectInput{
			Bucket:      aws.String(os.Getenv("S3_BUCKET")),
			Key:         aws.String(s3Key),
			Body:        file,
			ContentType: aws.String(contentType),
			ACL:         aws.String("public-read"),
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload image to storage"})
			return
		}

		imageURL := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/") + "/" + s3Key
		c.JSON(http.StatusOK, gin.H{"url": imageURL})
	}
}

func contentTypeToExt(ct string) string {
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}

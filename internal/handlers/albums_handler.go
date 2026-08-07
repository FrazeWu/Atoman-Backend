package handlers

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/storage"
)

type AlbumInput struct {
	Title       string `form:"title"`
	Artist      string `form:"artist"`
	Year        int    `form:"year"`
	ReleaseDate string `form:"release_date"`
	CoverURL    string `form:"cover_url"`
	AlbumType   string `form:"album_type"`
	EditSummary string `form:"edit_summary"`
}

func SetupAlbumRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	albums := router.Group("/api/v1/albums")
	{
		albums.GET("", GetAlbumsHandler(db))
		albums.GET("/:id", GetAlbumHandler(db))
		albums.POST("", middleware.AuthMiddleware(), CreateAlbumHandler(db, s3Client))
		albums.DELETE("/:id", middleware.AuthMiddleware(), middleware.AdminMiddleware(db), DeleteAlbumHandler(db, s3Client))
	}
}

// GetAlbumsHandler godoc
// @Summary 获取专辑列表
// @Description 返回所有未关闭的专辑列表。
// @Tags music-albums
// @Produce json
// @Success 200 {array} model.Album
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/albums [get]
func GetAlbumsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var albums []model.Album
		if err := db.Where("status NOT IN ?", []string{"closed", "rejected", "draft"}).Preload("Artists").Order("release_date ASC, title ASC").Find(&albums).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch albums"})
			return
		}
		for i := range albums {
			albums[i].Status = normalizeMusicStatus(albums[i].Status)
		}
		c.JSON(http.StatusOK, albums)
	}
}

// GetAlbumHandler godoc
// @Summary 获取专辑详情
// @Description 按 UUID 返回专辑详情。
// @Tags music-albums
// @Produce json
// @Param id path string true "专辑 UUID"
// @Success 200 {object} model.Album
// @Failure 404 {object} ErrorResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/albums/{id} [get]
func GetAlbumHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var album model.Album
		if err := db.Preload("Artists").First(&album, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "Album not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch album"})
			return
		}
		album.Status = normalizeMusicStatus(album.Status)
		c.JSON(http.StatusOK, album)
	}
}

// CreateAlbumHandler godoc
// @Summary 创建专辑
// @Description 通过 multipart form 创建专辑，可上传封面或复用已有封面 URL。
// @Tags music-albums
// @Accept mpfd
// @Produce json
// @Param title formData string true "专辑标题"
// @Param artist formData string true "艺人名称，多个用逗号分隔"
// @Param year formData int false "年份"
// @Param release_date formData string false "发行日期 YYYY-MM-DD"
// @Param cover_url formData string false "已存在封面 URL"
// @Param album_type formData string false "专辑类型"
// @Param cover formData file false "封面文件"
// @Success 201 {object} model.Album
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ConflictWithIDResponse
// @Failure 503 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums [post]
func CreateAlbumHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input AlbumInput
		if err := c.ShouldBind(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		input.Title = strings.TrimSpace(input.Title)
		artistNames := splitArtistNames(input.Artist)
		if input.Title == "" || len(artistNames) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "title and artist are required"})
			return
		}

		releaseDate := time.Now()
		if input.ReleaseDate != "" {
			parsedDate, err := time.Parse("2006-01-02", input.ReleaseDate)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "release_date must be YYYY-MM-DD"})
				return
			}
			releaseDate = parsedDate
		} else if input.Year > 0 {
			releaseDate = time.Date(input.Year, 1, 1, 0, 0, 0, 0, time.UTC)
		}

		year := input.Year
		if year == 0 {
			year = releaseDate.Year()
		}

		var userID *uuid.UUID
		if idVal, exists := c.Get("user_id"); exists {
			if uid, ok := idVal.(uuid.UUID); ok {
				userID = &uid
			}
		}

		coverURL := strings.TrimSpace(input.CoverURL)
		coverSource := ""
		if coverURL != "" {
			if strings.HasPrefix(coverURL, "/uploads/") {
				coverSource = "local"
			} else {
				coverSource = "s3"
			}
		}

		coverFile, coverHeader, err := c.Request.FormFile("cover")
		if err == nil {
			defer coverFile.Close()

			if status, message := validateUploadedImageFile(coverFile, coverHeader); status != http.StatusOK {
				c.JSON(status, gin.H{"error": message})
				return
			}

			safeArtist := storage.SanitizeName(artistNames[0])
			safeAlbum := storage.SanitizeName(input.Title)
			coverKey := "music/" + safeArtist + "/" + safeAlbum + "/cover_" + coverHeader.Filename

			if os.Getenv("STORAGE_TYPE") == "s3" {
				if !requireS3(c, s3Client) {
					return
				}

				_, err = s3Client.PutObject(&s3.PutObjectInput{
					Bucket: aws.String(os.Getenv("S3_BUCKET")),
					Key:    aws.String(coverKey),
					Body:   coverFile,
					ACL:    aws.String("public-read"),
				})
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload cover to S3"})
					return
				}

				coverURL = os.Getenv("S3_URL_PREFIX") + "/" + coverKey
				coverSource = "s3"
			} else {
				_, localURL, err := storage.SaveFileLocally(coverFile, "cover_"+coverHeader.Filename, safeArtist, input.Title)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save cover locally"})
					return
				}
				coverURL = localURL
				coverSource = "local"
			}
		}

		tx := db.Begin()

		var existing model.Album
		if err := tx.Where("title = ? AND year = ?", input.Title, year).First(&existing).Error; err == nil {
			tx.Rollback()
			c.JSON(http.StatusConflict, gin.H{"error": "Album already exists", "id": existing.ID})
			return
		} else if err != gorm.ErrRecordNotFound {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check album"})
			return
		}

		albumType := strings.TrimSpace(input.AlbumType)
		if albumType == "" {
			albumType = "album"
		}

		album := model.Album{
			Title:       input.Title,
			Year:        year,
			ReleaseDate: releaseDate,
			CoverURL:    coverURL,
			CoverSource: coverSource,
			Status:      "open",
			EntryStatus: "open",
			AlbumType:   albumType,
			UploadedBy:  userID,
		}
		if album.CoverSource == "" {
			album.CoverSource = "local"
		}

		if err := tx.Create(&album).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create album"})
			return
		}

		for _, name := range artistNames {
			var artist model.Artist
			if err := tx.FirstOrCreate(&artist, model.Artist{Name: name}).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process artist"})
				return
			}
			if err := tx.Model(&album).Association("Artists").Append(&artist); err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to link album to artist"})
				return
			}
		}

		if err := tx.Commit().Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create album"})
			return
		}

		db.Preload("Artists").First(&album, "id = ?", album.ID)
		c.JSON(http.StatusCreated, album)
	}
}

func splitArtistNames(value string) []string {
	parts := strings.Split(value, ",")
	names := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		key := strings.ToLower(name)
		if name != "" && !seen[key] {
			seen[key] = true
			names = append(names, name)
		}
	}
	return names
}

// DeleteAlbumHandler godoc
// @Summary 删除专辑
// @Description 删除指定专辑，仅管理员可执行。
// @Tags music-albums
// @Produce json
// @Param id path string true "专辑 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums/{id} [delete]
func DeleteAlbumHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var album model.Album
		if err := db.First(&album, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Album not found"})
			return
		}

		if err := db.Delete(&album).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete album"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Album deleted successfully"})
	}
}

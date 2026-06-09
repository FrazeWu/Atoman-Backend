package handlers

import (
	"log"
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

// SongInput represents song creation request
type SongInput struct {
	Title       string `form:"title" binding:"required"`
	Artist      string `form:"artist" binding:"required"`
	Album       string `form:"album"`
	ReleaseDate string `form:"release_date"` // YYYY-MM-DD
	TrackNumber int    `form:"track_number"`
	Lyrics      string `form:"lyrics"`
	BatchID     string `form:"batch_id"`
	AudioURL    string `form:"audio_url"` // For reusing existing audio
	CoverURL    string `form:"cover_url"` // For reusing existing cover
}

func resolveMediaURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "/uploads/") {
		base := strings.TrimRight(os.Getenv("PUBLIC_UPLOADS_BASE_URL"), "/")
		if base == "" {
			return trimmed
		}
		return base + trimmed
	}
	if strings.HasPrefix(trimmed, "uploads/") {
		base := strings.TrimRight(os.Getenv("PUBLIC_UPLOADS_BASE_URL"), "/")
		if base == "" {
			return "/" + trimmed
		}
		return base + "/" + strings.TrimLeft(trimmed, "/")
	}
	if os.Getenv("STORAGE_TYPE") == "s3" {
		s3Prefix := strings.TrimRight(os.Getenv("S3_URL_PREFIX"), "/")
		if s3Prefix != "" {
			return s3Prefix + "/" + strings.TrimLeft(trimmed, "/")
		}
	}
	return trimmed
}

func normalizeMusicStatus(status string) string {
	switch status {
	case "closed", "rejected", "draft":
		return "closed"
	default:
		return "open"
	}
}

// SetupSongRoutes configures song-related routes
func SetupSongRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	songs := router.Group("/api/songs")
	{
		songs.GET("", GetSongsHandler(db))
		songs.GET("/:id", GetSongHandler(db))
		songs.POST("", middleware.AuthMiddleware(), CreateSongHandler(db, s3Client))
		songs.PUT("/:id", middleware.AuthMiddleware(), UpdateSongHandler(db, s3Client))
		songs.DELETE("/:id", middleware.AuthMiddleware(), DeleteSongHandler(db, s3Client))
	}
}

// GetSongsHandler retrieves all non-closed songs
// GetSongsHandler godoc
// @Summary 获取歌曲列表
// @Description 返回所有未关闭的歌曲公开列表。
// @Tags music-songs
// @Produce json
// @Success 200 {array} SongPublicItem
// @Failure 500 {object} ErrorResponse
// @Router /api/songs [get]
func GetSongsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var songs []model.Song

		if err := db.Where("status NOT IN ?", []string{"closed", "rejected", "draft"}).
			Preload("Album").
			Preload("Album.Artists").
			Preload("Artists").
			Order("release_date ASC, track_number ASC, title ASC").
			Find(&songs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch songs"})
			return
		}

		for i := range songs {
			songs[i].Status = normalizeMusicStatus(songs[i].Status)
			if songs[i].Album != nil {
				songs[i].Album.Status = normalizeMusicStatus(songs[i].Album.Status)
			}
		}

		response := make([]map[string]interface{}, len(songs))
		for i, song := range songs {
			artistName := "Unknown Artist"
			albumTitle := "Unknown Album"
			albumYear := 0
			releaseDate := song.ReleaseDate.Format("2006-01-02")
			coverURL := song.CoverURL

			if song.Album != nil {
				albumTitle = song.Album.Title
				albumYear = song.Album.Year
				if albumYear == 0 && !song.ReleaseDate.IsZero() {
					albumYear = song.ReleaseDate.Year()
				}
				if song.Album.CoverURL != "" {
					coverURL = song.Album.CoverURL
				}
				if len(song.Album.Artists) > 0 && song.Album.Artists[0].Name != "" {
					artistName = song.Album.Artists[0].Name
				}
			}

			response[i] = map[string]interface{}{
				"id":           song.ID,
				"title":        song.Title,
				"artist":       artistName,
				"album":        albumTitle,
				"album_id":     song.AlbumID,
				"year":         albumYear,
				"release_date": releaseDate,
				"lyrics":       song.Lyrics,
				"audio_url":    resolveMediaURL(song.AudioURL),
				"cover_url":    resolveMediaURL(coverURL),
				"status":       song.Status,
			}
		}

		c.JSON(http.StatusOK, response)
	}
}

// GetSongHandler retrieves a single song by ID
// GetSongHandler godoc
// @Summary 获取歌曲详情
// @Description 按 UUID 返回单首歌曲的公开信息。
// @Tags music-songs
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Success 200 {object} SongPublicItem
// @Failure 404 {object} ErrorResponse
// @Router /api/songs/{id} [get]
func GetSongHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var song model.Song
		if err := db.Preload("Album").Preload("Album.Artists").Preload("Artists").First(&song, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Song not found"})
			return
		}

		song.Status = normalizeMusicStatus(song.Status)
		if song.Album != nil {
			song.Album.Status = normalizeMusicStatus(song.Album.Status)
		}

		artistName := "Unknown Artist"
		albumTitle := "Unknown Album"
		albumYear := 0
		releaseDate := song.ReleaseDate.Format("2006-01-02")
		coverURL := song.CoverURL

		if song.Album != nil {
			albumTitle = song.Album.Title
			albumYear = song.Album.Year
			if albumYear == 0 && !song.ReleaseDate.IsZero() {
				albumYear = song.ReleaseDate.Year()
			}
			if song.Album.CoverURL != "" {
				coverURL = song.Album.CoverURL
			}
			if len(song.Album.Artists) > 0 && song.Album.Artists[0].Name != "" {
				artistName = song.Album.Artists[0].Name
			}
		}

		response := map[string]interface{}{
			"id":           song.ID,
			"title":        song.Title,
			"artist":       artistName,
			"album":        albumTitle,
			"album_id":     song.AlbumID,
			"year":         albumYear,
			"release_date": releaseDate,
			"lyrics":       song.Lyrics,
			"audio_url":    resolveMediaURL(song.AudioURL),
			"cover_url":    resolveMediaURL(coverURL),
			"status":       song.Status,
		}

		c.JSON(http.StatusOK, response)
	}
}

// CreateSongHandler creates a new song with optional audio upload
// CreateSongHandler godoc
// @Summary 创建歌曲
// @Description 通过 multipart form 创建歌曲，可上传音频和封面或复用已有 URL。
// @Tags music-songs
// @Accept mpfd
// @Produce json
// @Param title formData string true "歌曲标题"
// @Param artist formData string true "艺人名称"
// @Param album formData string false "专辑名称"
// @Param release_date formData string false "发行日期 YYYY-MM-DD"
// @Param track_number formData int false "曲目序号"
// @Param lyrics formData string false "歌词"
// @Param batch_id formData string false "批次 ID"
// @Param audio_url formData string false "已存在音频 URL"
// @Param cover_url formData string false "已存在封面 URL"
// @Param audio formData file false "音频文件"
// @Param cover formData file false "封面文件"
// @Success 201 {object} model.Song
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/songs [post]
func CreateSongHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input SongInput

		if err := c.ShouldBind(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Parse ReleaseDate
		var releaseDate time.Time
		var err error
		if input.ReleaseDate != "" {
			releaseDate, err = time.Parse("2006-01-02", input.ReleaseDate)
			if err != nil {
				releaseDate = time.Now()
			}
		} else {
			releaseDate = time.Now()
		}

		// Check for duplicate song before uploading
		checkAlbum := input.Album
		if checkAlbum == "" {
			checkAlbum = "Unknown Album"
		}

		var existingCount int64
		if err := db.Table("Songs").
			Joins("JOIN Albums ON Albums.id = Songs.album_id").
			Joins("JOIN album_artists ON album_artists.album_id = Albums.id").
			Joins("JOIN Artists ON Artists.id = album_artists.artist_id").
			Where("Songs.title = ? AND Albums.title = ? AND Artists.name = ? AND Songs.status NOT IN ?",
				input.Title, checkAlbum, input.Artist, []string{"closed", "rejected", "draft"}).
			Count(&existingCount).Error; err != nil {
			log.Printf("Error checking for duplicates: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Database error checking duplicates"})
			return
		}

		if existingCount > 0 {
			log.Printf("Skipping duplicate song: %s - %s - %s", input.Title, checkAlbum, input.Artist)

			var existingSong model.Song
			db.Table("Songs").
				Joins("JOIN Albums ON Albums.id = Songs.album_id").
				Joins("JOIN album_artists ON album_artists.album_id = Albums.id").
				Joins("JOIN Artists ON Artists.id = album_artists.artist_id").
				Where("Songs.title = ? AND Albums.title = ? AND Artists.name = ? AND Songs.status NOT IN ?",
					input.Title, checkAlbum, input.Artist, []string{"closed", "rejected", "draft"}).
				First(&existingSong)

			c.JSON(http.StatusCreated, existingSong)
			return
		}

		// Handle File Upload Logic
		var audioURL string
		var audioSource string
		var coverURL string
		var coverSource string

		// Audio file handling
		if input.AudioURL != "" {
			audioURL = input.AudioURL
			if strings.HasPrefix(audioURL, "/uploads/") {
				audioSource = "local"
			} else {
				audioSource = "s3"
			}
		} else {
			file, header, err := c.Request.FormFile("audio")
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Audio file is required"})
				return
			}
			defer file.Close()

			if s3Client != nil && os.Getenv("STORAGE_TYPE") == "s3" {
				safeArtist := storage.SanitizeName(input.Artist)
				safeAlbum := storage.SanitizeName(input.Album)
				key := "music/" + safeArtist + "/" + safeAlbum + "/" + header.Filename
				_, err = s3Client.PutObject(&s3.PutObjectInput{
					Bucket: aws.String(os.Getenv("S3_BUCKET")),
					Key:    aws.String(key),
					Body:   file,
					ACL:    aws.String("public-read"),
				})
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload file to S3"})
					return
				}
				audioURL = os.Getenv("S3_URL_PREFIX") + "/" + key
				audioSource = "s3"
			} else {
				_, localURL, err := storage.SaveFileLocally(file, header.Filename, input.Artist, input.Album)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file locally"})
					return
				}
				audioURL = localURL
				audioSource = "local"
			}
		}

		// Cover file handling (similar logic)
		if input.CoverURL != "" {
			coverURL = input.CoverURL
			if strings.HasPrefix(coverURL, "/uploads/") {
				coverSource = "local"
			} else {
				coverSource = "s3"
			}
		} else {
			coverFile, coverHeader, err := c.Request.FormFile("cover")
			if err == nil {
				defer coverFile.Close()

				if s3Client != nil && os.Getenv("STORAGE_TYPE") == "s3" {
					safeArtist := storage.SanitizeName(input.Artist)
					safeAlbum := storage.SanitizeName(input.Album)
					coverKey := "music/" + safeArtist + "/" + safeAlbum + "/cover_" + coverHeader.Filename
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
					_, localURL, err := storage.SaveFileLocally(coverFile, "cover_"+coverHeader.Filename, input.Artist, input.Album)
					if err != nil {
						c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save cover locally"})
						return
					}
					coverURL = localURL
					coverSource = "local"
				}
			}
		}

		// Transaction to ensure atomicity
		tx := db.Begin()

		// 1. Find or Create Artist
		var artist model.Artist
		if err := tx.FirstOrCreate(&artist, model.Artist{Name: input.Artist}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process artist"})
			return
		}

		// 2. Find or Create Album
		var album model.Album
		albumTitle := input.Album
		if albumTitle == "" {
			albumTitle = "Unknown Album"
		}

		// Get User ID from context
		var userID *uuid.UUID
		if idVal, exists := c.Get("user_id"); exists {
			uid := idVal.(uuid.UUID)
			userID = &uid
		}

		status := "open"

		if err := tx.Where("title = ? AND year = ?", albumTitle, releaseDate.Year()).FirstOrCreate(&album, model.Album{Title: albumTitle, Year: releaseDate.Year(), ReleaseDate: releaseDate, UploadedBy: userID, Status: status}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process album"})
			return
		}
		if album.ID != uuid.Nil {
			var existingAssoc int64
			tx.Table("album_artists").Where("album_id = ? AND artist_id = ?", album.ID, artist.ID).Count(&existingAssoc)
			if existingAssoc == 0 {
				if err := tx.Model(&album).Association("Artists").Append(&artist); err != nil {
					tx.Rollback()
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to link album to artist"})
					return
				}
			}
		}

		if coverURL != "" && album.CoverURL == "" {
			album.CoverURL = coverURL
			album.CoverSource = coverSource
			if err := tx.Save(&album).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update album cover"})
				return
			}
		}

		song := model.Song{
			Title:       input.Title,
			ReleaseDate: releaseDate,
			TrackNumber: input.TrackNumber,
			Lyrics:      input.Lyrics,
			AudioURL:    audioURL,
			AudioSource: audioSource,
			CoverURL:    coverURL,
			CoverSource: coverSource,
			Status:      status,
			AlbumID:     &album.ID,
			UploadedBy:  userID,
			BatchID:     input.BatchID,
		}

		if err := tx.Create(&song).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create song"})
			return
		}

		if err := tx.Model(&song).Association("Artists").Append(&artist); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to associate artist"})
			return
		}

		tx.Commit()

		db.Preload("Album").Preload("Artists").First(&song, "id = ?", song.ID)
		c.JSON(http.StatusCreated, song)
	}
}

// UpdateSongHandler updates an existing song
// UpdateSongHandler godoc
// @Summary 更新歌曲
// @Description 通过 multipart form 更新歌曲信息，仅上传者或管理员可执行。
// @Tags music-songs
// @Accept mpfd
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param title formData string false "歌曲标题"
// @Param artist formData string false "艺人名称"
// @Param album formData string false "专辑名称"
// @Param release_date formData string false "发行日期 YYYY-MM-DD"
// @Param track_number formData int false "曲目序号"
// @Param lyrics formData string false "歌词"
// @Param cover formData file false "封面文件"
// @Success 200 {object} model.Song
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/songs/{id} [put]
func UpdateSongHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var song model.Song
		if err := db.Preload("Album").Preload("Album.Artists").First(&song, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "Song not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch song"})
			return
		}

		var input SongInput
		if err := c.ShouldBind(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID, userExists := c.Get("user_id")
		userRole := "anonymous"
		if roleVal, exists := c.Get("role"); exists {
			if role, ok := roleVal.(string); ok {
				userRole = role
			}
		}

		if userRole != "admin" {
			if !userExists {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
				return
			}
			if song.UploadedBy != nil && *song.UploadedBy != userID.(uuid.UUID) {
				c.JSON(http.StatusForbidden, gin.H{"error": "You can only edit your own songs"})
				return
			}
			if song.UploadedBy == nil {
				c.JSON(http.StatusForbidden, gin.H{"error": "Cannot edit legacy songs without owner information"})
				return
			}
		}

		var releaseDate time.Time
		var err error
		if input.ReleaseDate != "" {
			releaseDate, err = time.Parse("2006-01-02", input.ReleaseDate)
			if err != nil {
				releaseDate = time.Now()
			}
		} else {
			releaseDate = time.Now()
		}

		var coverURL string
		var coverSource string

		coverFile, coverHeader, err := c.Request.FormFile("cover")
		if err == nil {
			defer coverFile.Close()

			safeArtist := strings.ReplaceAll(input.Artist, "/", "-")
			if safeArtist == "" {
				safeArtist = "Unknown Artist"
			}
			safeAlbum := strings.ReplaceAll(input.Album, "/", "-")
			if safeAlbum == "" {
				safeAlbum = "Unknown Album"
			}

			if s3Client != nil && os.Getenv("STORAGE_TYPE") == "s3" {
				coverKey := "music/" + safeArtist + "/" + safeAlbum + "/cover_" + coverHeader.Filename

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
				_, localURL, err := storage.SaveFileLocally(coverFile, "cover_"+coverHeader.Filename, input.Artist, input.Album)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save cover locally"})
					return
				}
				coverURL = localURL
				coverSource = "local"
			}
		}

		tx := db.Begin()

		var artist model.Artist
		if err := tx.FirstOrCreate(&artist, model.Artist{Name: input.Artist}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process artist"})
			return
		}

		var album model.Album
		albumTitle := input.Album
		if albumTitle == "" {
			albumTitle = "Unknown Album"
		}

		if err := tx.Where("title = ? AND year = ?", albumTitle, releaseDate.Year()).FirstOrCreate(&album, model.Album{Title: albumTitle, Year: releaseDate.Year(), ReleaseDate: releaseDate}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process album"})
			return
		}
		if album.ID != uuid.Nil {
			var existingAssoc int64
			tx.Table("album_artists").Where("album_id = ? AND artist_id = ?", album.ID, artist.ID).Count(&existingAssoc)
			if existingAssoc == 0 {
				if err := tx.Model(&album).Association("Artists").Append(&artist); err != nil {
					tx.Rollback()
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to link album to artist"})
					return
				}
			}
		}

		if coverURL != "" {
			album.CoverURL = coverURL
			album.CoverSource = coverSource
			if err := tx.Save(&album).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update album cover"})
				return
			}
		}

		song.Title = input.Title
		song.ReleaseDate = releaseDate
		song.TrackNumber = input.TrackNumber
		song.Lyrics = input.Lyrics
		song.AlbumID = &album.ID

		if err := tx.Save(&song).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update song"})
			return
		}

		if err := tx.Model(&song).Association("Artists").Clear(); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to clear artist associations"})
			return
		}

		if err := tx.Model(&song).Association("Artists").Append(&artist); err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to associate artist"})
			return
		}

		tx.Commit()

		db.Preload("Album").Preload("Album.Artists").Preload("Artists").First(&song, "id = ?", song.ID)
		c.JSON(http.StatusOK, song)
	}
}

// DeleteSongHandler deletes a song
// DeleteSongHandler godoc
// @Summary 删除歌曲
// @Description 删除指定歌曲。
// @Tags music-songs
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/songs/{id} [delete]
func DeleteSongHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var song model.Song
		if err := db.First(&song, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Song not found"})
			return
		}

		if err := db.Delete(&song).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete song"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Song deleted successfully"})
	}
}

package handlers

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/musiclyrics"
	"atoman/internal/storage"
)

func canUploadToS3(s3Client *s3.S3) bool {
	return s3Client != nil && os.Getenv("S3_BUCKET") != "" && os.Getenv("S3_URL_PREFIX") != ""
}

// GetPendingSongsHandler godoc
// @Summary 获取待审核歌曲列表
// @Description 返回所有待审核歌曲及其关联用户、专辑和艺人信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.Song
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/songs [get]
func GetPendingSongsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var songs []model.Song
		if err := db.Where("status = ?", "pending").
			Preload("User").
			Preload("Album").
			Preload("Album.Artists").
			Preload("Artists").
			Order("created_at desc").
			Find(&songs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending songs"})
			return
		}
		c.JSON(http.StatusOK, songs)
	}
}

// ApproveSongHandler godoc
// @Summary 审核通过歌曲
// @Description 将待审核歌曲标记为 approved，并在可用时把本地文件迁移到 S3。
// @Tags admin
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/songs/{id}/approve [post]
func ApproveSongHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var song model.Song
		if err := db.First(&song, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Song not found"})
			return
		}

		if canUploadToS3(s3Client) && song.AudioSource == "local" && song.AudioURL != "" {
			localPath := storage.GetLocalPathFromURL(song.AudioURL)
			if localPath != "" {
				s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload audio to S3"})
					return
				}
				song.AudioURL = s3URL
				song.AudioSource = "s3"
				storage.DeleteLocalFile(localPath)
			}
		}

		if canUploadToS3(s3Client) && song.CoverSource == "local" && song.CoverURL != "" {
			localPath := storage.GetLocalPathFromURL(song.CoverURL)
			if localPath != "" {
				s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload cover to S3"})
					return
				}
				song.CoverURL = s3URL
				song.CoverSource = "s3"
				storage.DeleteLocalFile(localPath)
			}
		}

		song.Status = "approved"

		if err := db.Save(&song).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve song"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Song approved"})
	}
}

// RejectSongHandler godoc
// @Summary 驳回歌曲
// @Description 删除待审核歌曲及其关联本地或对象存储文件。
// @Tags admin
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/songs/{id}/reject [post]
func RejectSongHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var song model.Song
		if err := db.Preload("Album").First(&song, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Song not found"})
			return
		}

		if err := storage.DeleteSongAndS3Objects(db, s3Client, &song); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete song and associated files"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Song rejected and deleted"})
	}
}

// GetPendingSongCorrectionsHandler godoc
// @Summary 获取待审核歌曲纠错列表
// @Description 返回所有待审核歌曲纠错及其关联歌曲、提交用户信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.SongCorrection
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/song-corrections [get]
func GetPendingSongCorrectionsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var corrections []model.SongCorrection
		if err := db.Where("status = ?", "pending").
			Preload("User").
			Preload("Song").
			Order("created_at desc").
			Find(&corrections).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending song corrections"})
			return
		}
		c.JSON(http.StatusOK, corrections)
	}
}

// ApproveSongCorrectionHandler godoc
// @Summary 审核通过歌曲纠错
// @Description 将歌曲纠错标记为 approved，并把支持的字段改动应用到歌曲实体。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/song-corrections/{id}/approve [post]
func ApproveSongCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		var correction model.SongCorrection
		if err := db.Preload("Song").First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		tx := db.Begin()

		if err := tx.Model(&model.SongCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "approved",
			"approved_by": adminID,
			"approved_at": now,
		}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve correction"})
			return
		}

		song := correction.Song
		updated := false

		switch correction.FieldName {
		case "title":
			song.Title = correction.CorrectedValue
			updated = true
		case "lyrics":
			song.Lyrics = correction.CorrectedValue
			updated = true
		}

		if updated {
			if err := tx.Save(&song).Error; err != nil {
				tx.Rollback()
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply correction"})
				return
			}
			if correction.FieldName == "lyrics" {
				if err := musiclyrics.SyncLegacySongLyrics(tx, adminID, song.ID, correction.CorrectedValue, "通过歌词纠错更新"); err != nil {
					tx.Rollback()
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply correction"})
					return
				}
			}
		}

		if err := tx.Commit().Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply correction"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Song correction approved and applied"})
	}
}

// RejectSongCorrectionHandler godoc
// @Summary 驳回歌曲纠错
// @Description 将歌曲纠错标记为 rejected。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/song-corrections/{id}/reject [post]
func RejectSongCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		if err := db.Model(&model.SongCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "rejected",
			"rejected_by": adminID,
			"rejected_at": now,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject correction"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Song correction rejected"})
	}
}

// GetPendingAlbumsHandler godoc
// @Summary 获取待审核专辑列表
// @Description 返回所有待审核专辑及其关联艺人、用户和歌曲信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.Album
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/albums [get]
func GetPendingAlbumsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var albums []model.Album
		if err := db.Where("status = ?", "pending").
			Preload("Artists").
			Preload("User").
			Preload("Songs").
			Order("created_at desc").
			Find(&albums).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending albums"})
			return
		}
		c.JSON(http.StatusOK, albums)
	}
}

// ApproveAlbumHandler godoc
// @Summary 审核通过专辑
// @Description 将待审核专辑及其附属歌曲标记为 approved，并在可用时迁移本地文件到 S3。
// @Tags admin
// @Produce json
// @Param id path string true "专辑 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/albums/{id}/approve [post]
func ApproveAlbumHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var album model.Album
		if err := db.Preload("Songs").First(&album, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Album not found"})
			return
		}

		// Upload album cover to S3 if local
		if canUploadToS3(s3Client) && album.CoverSource == "local" && album.CoverURL != "" {
			localPath := storage.GetLocalPathFromURL(album.CoverURL)
			if localPath != "" {
				s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
				if err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload cover to S3"})
					return
				}
				album.CoverURL = s3URL
				album.CoverSource = "s3"
				storage.DeleteLocalFile(localPath)
			}
		}

		// Upload all songs' local files to S3
		for i := range album.Songs {
			song := &album.Songs[i]
			if canUploadToS3(s3Client) && song.AudioSource == "local" && song.AudioURL != "" {
				localPath := storage.GetLocalPathFromURL(song.AudioURL)
				if localPath != "" {
					s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
					if err != nil {
						log.Printf("Failed to upload song audio to S3: %v", err)
						continue
					}
					song.AudioURL = s3URL
					song.AudioSource = "s3"
					storage.DeleteLocalFile(localPath)
				}
			}
			if canUploadToS3(s3Client) && song.CoverSource == "local" && song.CoverURL != "" {
				localPath := storage.GetLocalPathFromURL(song.CoverURL)
				if localPath != "" {
					s3URL, err := storage.UploadLocalFileToS3(s3Client, localPath)
					if err != nil {
						log.Printf("Failed to upload song cover to S3: %v", err)
						continue
					}
					song.CoverURL = s3URL
					song.CoverSource = "s3"
					storage.DeleteLocalFile(localPath)
				}
			}
			song.Status = "approved"
			db.Save(song)
		}

		album.Status = "approved"

		if err := db.Save(&album).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve album"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Album approved"})
	}
}

// RejectAlbumHandler godoc
// @Summary 驳回专辑
// @Description 删除待审核专辑及其关联存储对象。
// @Tags admin
// @Produce json
// @Param id path string true "专辑 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/albums/{id}/reject [post]
func RejectAlbumHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var album model.Album
		if err := db.First(&album, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Album not found"})
			return
		}

		if err := storage.DeleteAlbumAndS3Objects(db, s3Client, &album); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete album and associated files"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Album rejected and deleted"})
	}
}

// GetPendingAlbumCorrectionsHandler godoc
// @Summary 获取待审核专辑纠错列表
// @Description 返回所有待审核专辑纠错及其关联专辑信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.AlbumCorrection
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/album-corrections [get]
func GetPendingAlbumCorrectionsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var corrections []model.AlbumCorrection
		if err := db.Where("status = ?", "pending").
			Preload("User").
			Preload("Album").
			Preload("Album.Artists").
			Order("created_at desc").
			Find(&corrections).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending album corrections"})
			return
		}
		c.JSON(http.StatusOK, corrections)
	}
}

// ApproveAlbumCorrectionHandler godoc
// @Summary 审核通过专辑纠错
// @Description 将专辑纠错标记为 approved，并把改动应用到专辑实体。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/album-corrections/{id}/approve [post]
func ApproveAlbumCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		var correction model.AlbumCorrection
		if err := db.Preload("Album").Preload("Album.Artists").First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		tx := db.Begin()

		if err := tx.Model(&model.AlbumCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "approved",
			"approved_by": adminID,
			"approved_at": now,
		}).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve correction"})
			return
		}

		var album model.Album
		if err := tx.First(&album, "id = ?", correction.AlbumID).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusNotFound, gin.H{"error": "Album not found"})
			return
		}

		if correction.CorrectedTitle != "" {
			album.Title = correction.CorrectedTitle
		}
		if correction.CorrectedCoverURL != "" {
			album.CoverURL = correction.CorrectedCoverURL
			album.CoverSource = correction.CorrectedCoverSource
		}
		if correction.CorrectedReleaseDate != nil {
			album.ReleaseDate = *correction.CorrectedReleaseDate
		}

		if err := tx.Save(&album).Error; err != nil {
			tx.Rollback()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to apply album correction"})
			return
		}

		tx.Commit()
		c.JSON(http.StatusOK, gin.H{"message": "Album correction approved and applied"})
	}
}

// RejectAlbumCorrectionHandler godoc
// @Summary 驳回专辑纠错
// @Description 将专辑纠错标记为 rejected。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/album-corrections/{id}/reject [post]
func RejectAlbumCorrectionHandler(db *gorm.DB, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		var correction model.AlbumCorrection
		if err := db.First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		if err := db.Model(&model.AlbumCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "rejected",
			"rejected_by": adminID,
			"rejected_at": now,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject correction"})
			return
		}

		if correction.CorrectedCoverURL != "" && correction.CorrectedCoverSource == "s3" {
			log.Printf("Note: Should delete S3 object for rejected cover: %s", correction.CorrectedCoverURL)
		}

		c.JSON(http.StatusOK, gin.H{"message": "Album correction rejected"})
	}
}

// GetPendingArtistCorrectionsHandler godoc
// @Summary 获取待审核艺人纠错列表
// @Description 返回所有待审核艺人纠错及其关联艺人、提交用户信息。
// @Tags admin
// @Produce json
// @Success 200 {array} model.ArtistCorrection
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/artist-corrections [get]
func GetPendingArtistCorrectionsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var corrections []model.ArtistCorrection
		if err := db.Where("status = ?", "pending").
			Preload("Artist").
			Preload("User").
			Order("created_at asc").
			Find(&corrections).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch pending artist corrections"})
			return
		}
		c.JSON(http.StatusOK, corrections)
	}
}

// ApproveArtistCorrectionHandler godoc
// @Summary 审核通过艺人纠错
// @Description 将艺人纠错标记为 approved。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/artist-corrections/{id}/approve [post]
func ApproveArtistCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		adminIDVal, _ := c.Get("user_id")
		adminID := adminIDVal.(uuid.UUID)
		now := time.Now()

		var correction model.ArtistCorrection
		if err := db.First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		if err := db.Model(&model.ArtistCorrection{}).Where("id = ?", id).Updates(map[string]interface{}{
			"status":      "approved",
			"approved_by": adminID,
			"approved_at": now,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve correction"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Artist correction approved"})
	}
}

// RejectArtistCorrectionHandler godoc
// @Summary 驳回艺人纠错
// @Description 将艺人纠错标记为 rejected。
// @Tags admin
// @Produce json
// @Param id path string true "纠错 UUID"
// @Success 200 {object} MessageResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/reviews/artist-corrections/{id}/reject [post]
func RejectArtistCorrectionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")

		var correction model.ArtistCorrection
		if err := db.First(&correction, "id = ?", id).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Correction not found"})
			return
		}

		if err := db.Model(&model.ArtistCorrection{}).Where("id = ?", id).Update("status", "rejected").Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reject correction"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Artist correction rejected"})
	}
}

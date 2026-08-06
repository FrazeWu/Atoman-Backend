package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
)

func SetupEntryStatusRoutes(router *gin.Engine, db *gorm.DB) {
	albums := router.Group("/api/v1/albums/:id")
	albums.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	{
		albums.PUT("/entry-status", ChangeAlbumStatusHandler(db))
	}

	artists := router.Group("/api/v1/artists/:id")
	artists.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	{
		artists.PUT("/entry-status", ChangeArtistStatusHandler(db))
	}

	admin := router.Group("/api/v1/admin/music")
	admin.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	{
		admin.GET("/entries", ListMusicEntriesHandler(db))
		admin.GET("/quality", ListMusicQualityIssuesHandler(db))
	}
}

type MusicQualityIssue struct {
	Type       string `json:"type"`
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Title      string `json:"title"`
}

// ListMusicQualityIssues godoc
// @Summary 获取音乐资料问题
// @Description 管理员查看缺失封面、曲目、音频和失败导入的音乐资料。
// @Tags music-entry-status
// @Produce json
// @Param type query string false "问题类型" Enums(all,missing_cover,missing_tracks,missing_audio,import_failed)
// @Success 200 {object} map[string]interface{}
// @Security BearerAuth
// @Router /api/v1/admin/music/quality [get]
func ListMusicQualityIssuesHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		filter := c.DefaultQuery("type", "all")
		issues := make([]MusicQualityIssue, 0)
		appendAlbums := func(issueType string, query *gorm.DB) error {
			if filter != "all" && filter != issueType {
				return nil
			}
			var albums []model.Album
			if err := query.Limit(100).Find(&albums).Error; err != nil {
				return err
			}
			for _, album := range albums {
				issues = append(issues, MusicQualityIssue{Type: issueType, EntityType: "album", EntityID: album.ID.String(), Title: album.Title})
			}
			return nil
		}
		if err := appendAlbums("missing_cover", db.Model(&model.Album{}).Where("COALESCE(cover_url, '') = '' AND COALESCE(entry_status, '') <> 'closed'")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
			return
		}
		if err := appendAlbums("missing_tracks", db.Model(&model.Album{}).Where("COALESCE(entry_status, '') <> 'closed' AND NOT EXISTS (SELECT 1 FROM \"Songs\" WHERE \"Songs\".album_id = \"Albums\".id AND \"Songs\".deleted_at IS NULL)")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
			return
		}
		if filter == "all" || filter == "missing_audio" {
			var songs []model.Song
			if err := db.Where("COALESCE(audio_url, '') = '' AND COALESCE(status, '') <> 'closed'").Limit(100).Find(&songs).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
				return
			}
			for _, song := range songs {
				issues = append(issues, MusicQualityIssue{Type: "missing_audio", EntityType: "song", EntityID: song.ID.String(), Title: song.Title})
			}
		}
		if filter == "all" || filter == "import_failed" {
			var sessions []model.AlbumImportSession
			if err := db.Where("status IN ?", []string{"failed", "needs_attention"}).Limit(100).Find(&sessions).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
				return
			}
			for _, session := range sessions {
				issues = append(issues, MusicQualityIssue{Type: "import_failed", EntityType: "import", EntityID: session.ID.String(), Title: session.ErrorMessage})
			}
		}
		c.JSON(http.StatusOK, gin.H{"data": issues, "total": len(issues)})
	}
}

// ChangeAlbumStatusHandler godoc
// @Summary 修改专辑条目状态
// @Description 管理员修改专辑 wiki 条目的 entry_status。
// @Tags music-entry-status
// @Accept json
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param input body EntryStatusInput true "条目状态输入"
// @Success 200 {object} EntryStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums/{id}/entry-status [put]
func ChangeAlbumStatusHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}
		var input struct {
			Status string `json:"status" binding:"required"`
			Reason string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		validStatuses := map[string]bool{"open": true, "confirmed": true, "disputed": true}
		if !validStatuses[input.Status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status. Must be open, confirmed, or disputed"})
			return
		}
		if err := db.Model(&model.Album{}).Where("id = ?", albumID).
			Update("entry_status", input.Status).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update status"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Album entry status updated", "status": input.Status})
	}
}

// ChangeArtistStatusHandler godoc
// @Summary 修改艺人条目状态
// @Description 管理员修改艺人 wiki 条目的 entry_status。
// @Tags music-entry-status
// @Accept json
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param input body EntryStatusInput true "条目状态输入"
// @Success 200 {object} EntryStatusResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id}/entry-status [put]
func ChangeArtistStatusHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}
		var input struct {
			Status string `json:"status" binding:"required"`
			Reason string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		validStatuses := map[string]bool{"open": true, "confirmed": true, "disputed": true}
		if !validStatuses[input.Status] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status. Must be open, confirmed, or disputed"})
			return
		}
		if err := db.Model(&model.Artist{}).Where("id = ?", artistID).
			Update("entry_status", input.Status).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update status"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "Artist entry status updated", "status": input.Status})
	}
}

type MusicEntryItem struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	EntryStatus         string `json:"entry_status"`
	AlbumType           string `json:"album_type,omitempty"`
	UpdatedAt           string `json:"updated_at"`
	LastEditor          string `json:"last_editor,omitempty"`
	OpenDiscussionCount int64  `json:"open_discussion_count"`
}

// ListMusicEntriesHandler godoc
// @Summary 获取音乐条目后台列表
// @Description 管理员按类型和状态筛选音乐 wiki 条目。
// @Tags music-entry-status
// @Produce json
// @Param type query string false "条目类型" Enums(all,album,artist)
// @Param status query string false "条目状态" Enums(all,open,confirmed,disputed)
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {object} MusicEntryListResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/music/entries [get]
func ListMusicEntriesHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		entryType := c.DefaultQuery("type", "all")
		statusFilter := c.DefaultQuery("status", "all")
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		if page < 1 {
			page = 1
		}
		offset := (page - 1) * pageSize

		var results []MusicEntryItem
		var total int64

		if entryType == "all" || entryType == "album" {
			var albums []model.Album
			q := db.Model(&model.Album{})
			if statusFilter != "all" {
				q = q.Where("entry_status = ?", statusFilter)
			}
			q.Count(&total)
			q.Offset(offset).Limit(pageSize).Order("updated_at DESC").Find(&albums)
			for _, a := range albums {
				var discCount int64
				db.Model(&model.DiscussionTarget{}).Select("COALESCE(MAX(comment_count), 0)").
					Where("kind = ? AND resource_id = ?", "music_album", a.ID).Scan(&discCount)
				var latestRev model.Revision
				lastEditor := ""
				if err := db.Where("content_id = ? AND content_type = ?", a.ID, "album").
					Order("version_number DESC").Preload("Editor").First(&latestRev).Error; err == nil && latestRev.Editor != nil {
					lastEditor = latestRev.Editor.Username
				}
				results = append(results, MusicEntryItem{
					ID:                  a.ID.String(),
					Name:                a.Title,
					Type:                "album",
					EntryStatus:         a.EntryStatus,
					AlbumType:           a.AlbumType,
					UpdatedAt:           a.UpdatedAt.Format("2006-01-02T15:04:05Z"),
					LastEditor:          lastEditor,
					OpenDiscussionCount: discCount,
				})
			}
		}

		if entryType == "all" || entryType == "artist" {
			var artists []model.Artist
			q := db.Model(&model.Artist{})
			if statusFilter != "all" {
				q = q.Where("entry_status = ?", statusFilter)
			}
			var artistCount int64
			q.Count(&artistCount)
			total += artistCount
			q.Offset(offset).Limit(pageSize).Order("updated_at DESC").Find(&artists)
			for _, a := range artists {
				var discCount int64
				db.Model(&model.DiscussionTarget{}).Select("COALESCE(MAX(comment_count), 0)").
					Where("kind = ? AND resource_id = ?", "music_artist", a.ID).Scan(&discCount)
				results = append(results, MusicEntryItem{
					ID:                  a.ID.String(),
					Name:                a.Name,
					Type:                "artist",
					EntryStatus:         a.EntryStatus,
					UpdatedAt:           a.UpdatedAt.Format("2006-01-02T15:04:05Z"),
					OpenDiscussionCount: discCount,
				})
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"data":      results,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		})
	}
}

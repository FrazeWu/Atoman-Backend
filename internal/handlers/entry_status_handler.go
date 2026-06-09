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
	albums := router.Group("/api/albums/:id")
	albums.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	{
		albums.PUT("/entry-status", ChangeAlbumStatusHandler(db))
	}

	artists := router.Group("/api/artists/:id")
	artists.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	{
		artists.PUT("/entry-status", ChangeArtistStatusHandler(db))
	}

	admin := router.Group("/api/admin/music")
	admin.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	{
		admin.GET("/entries", ListMusicEntriesHandler(db))
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
// @Router /api/albums/{id}/entry-status [put]
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
// @Router /api/artists/{id}/entry-status [put]
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
// @Router /api/admin/music/entries [get]
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
				db.Model(&model.Discussion{}).Where("content_id = ? AND content_type = ?", a.ID, "album").Count(&discCount)
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
				db.Model(&model.Discussion{}).Where("content_id = ? AND content_type = ?", a.ID, "artist").Count(&discCount)
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

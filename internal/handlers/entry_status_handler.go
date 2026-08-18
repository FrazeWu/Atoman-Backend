package handlers

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/httpx"
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
// @Description 管理员查看缺失封面、曲目、音频、关键元数据、重复候选和失败导入的音乐资料。
// @Tags music-entry-status
// @Produce json
// @Param type query string false "问题类型" Enums(all,missing_cover,missing_tracks,missing_audio,missing_metadata,duplicate_candidate,processing_failed,import_failed)
// @Param page query int false "页码"
// @Param page_size query int false "每页数量"
// @Success 200 {object} map[string]interface{}
// @Security BearerAuth
// @Router /api/v1/admin/music/quality [get]
func ListMusicQualityIssuesHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		filter := c.DefaultQuery("type", "all")
		page, pageSize := httpx.PageParams(c)
		issues := make([]MusicQualityIssue, 0)
		appendAlbums := func(issueType string, query *gorm.DB) error {
			if filter != "all" && filter != issueType {
				return nil
			}
			var albums []model.Album
			if err := query.Order("title ASC, id ASC").Find(&albums).Error; err != nil {
				return err
			}
			for _, album := range albums {
				issues = append(issues, MusicQualityIssue{Type: issueType, EntityType: "album", EntityID: album.ID.String(), Title: album.Title})
			}
			return nil
		}
		appendArtists := func(issueType string, query *gorm.DB) error {
			if filter != "all" && filter != issueType {
				return nil
			}
			var artists []model.Artist
			if err := query.Order("name ASC, id ASC").Find(&artists).Error; err != nil {
				return err
			}
			for _, artist := range artists {
				issues = append(issues, MusicQualityIssue{Type: issueType, EntityType: "artist", EntityID: artist.ID.String(), Title: artist.Name})
			}
			return nil
		}
		if err := appendAlbums("missing_cover", db.Model(&model.Album{}).Where("COALESCE(cover_url, '') = '' AND lifecycle_status = ?", model.MusicLifecycleActive)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
			return
		}
		if err := appendAlbums("missing_tracks", db.Model(&model.Album{}).Where("lifecycle_status = ? AND NOT EXISTS (SELECT 1 FROM \"Songs\" WHERE \"Songs\".album_id = \"Albums\".id AND \"Songs\".deleted_at IS NULL AND \"Songs\".lifecycle_status = 'active')", model.MusicLifecycleActive)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
			return
		}
		if err := appendAlbums("missing_metadata", db.Model(&model.Album{}).Where("lifecycle_status = ? AND (TRIM(COALESCE(title, '')) = '' OR (COALESCE(release_year, 0) = 0 AND COALESCE(year, 0) = 0) OR NOT EXISTS (SELECT 1 FROM album_artists WHERE album_artists.album_id = \"Albums\".id))", model.MusicLifecycleActive)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
			return
		}
		if err := appendAlbums("duplicate_candidate", db.Model(&model.Album{}).Where("lifecycle_status = ? AND TRIM(COALESCE(title, '')) <> '' AND EXISTS (SELECT 1 FROM \"Albums\" AS duplicate_album WHERE duplicate_album.id <> \"Albums\".id AND duplicate_album.deleted_at IS NULL AND duplicate_album.lifecycle_status = 'active' AND LOWER(TRIM(duplicate_album.title)) = LOWER(TRIM(\"Albums\".title)))", model.MusicLifecycleActive)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
			return
		}
		if err := appendArtists("duplicate_candidate", db.Model(&model.Artist{}).Where("lifecycle_status = ? AND TRIM(COALESCE(name, '')) <> '' AND EXISTS (SELECT 1 FROM \"Artists\" AS duplicate_artist WHERE duplicate_artist.id <> \"Artists\".id AND duplicate_artist.deleted_at IS NULL AND duplicate_artist.lifecycle_status = 'active' AND LOWER(TRIM(duplicate_artist.name)) = LOWER(TRIM(\"Artists\".name)))", model.MusicLifecycleActive)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
			return
		}
		if filter == "all" || filter == "missing_audio" {
			var songs []model.Song
			if err := db.Where("COALESCE(audio_url, '') = '' AND lifecycle_status = ?", model.MusicLifecycleActive).Order("title ASC, id ASC").Find(&songs).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
				return
			}
			for _, song := range songs {
				issues = append(issues, MusicQualityIssue{Type: "missing_audio", EntityType: "song", EntityID: song.ID.String(), Title: song.Title})
			}
		}
		if filter == "all" || filter == "import_failed" {
			var sessions []model.AlbumImportSession
			if err := db.Where("status IN ?", []string{"failed", "needs_attention"}).Order("updated_at DESC, id ASC").Find(&sessions).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
				return
			}
			for _, session := range sessions {
				issues = append(issues, MusicQualityIssue{Type: "import_failed", EntityType: "import", EntityID: session.ID.String(), Title: session.ErrorMessage})
			}
		}
		if filter == "all" || filter == "processing_failed" {
			var replacements []model.SongAudioReplacement
			if db.Migrator().HasTable(&model.SongAudioReplacement{}) {
				if err := db.Preload("Song").Where("status = ?", "failed").Order("updated_at DESC, id ASC").Find(&replacements).Error; err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load music quality issues"})
					return
				}
			}
			for _, replacement := range replacements {
				title := "音频替换失败"
				if replacement.Song != nil && replacement.Song.Title != "" {
					title = replacement.Song.Title
				}
				issues = append(issues, MusicQualityIssue{Type: "processing_failed", EntityType: "song", EntityID: replacement.SongID.String(), Title: title})
			}
		}
		sort.SliceStable(issues, func(i, j int) bool {
			if issues[i].Type != issues[j].Type {
				return issues[i].Type < issues[j].Type
			}
			if issues[i].EntityType != issues[j].EntityType {
				return issues[i].EntityType < issues[j].EntityType
			}
			if issues[i].Title != issues[j].Title {
				return issues[i].Title < issues[j].Title
			}
			return issues[i].EntityID < issues[j].EntityID
		})
		total := len(issues)
		start := (page - 1) * pageSize
		if start > total {
			start = total
		}
		end := start + pageSize
		if end > total {
			end = total
		}
		c.JSON(http.StatusOK, gin.H{"data": issues[start:end], "page": page, "page_size": pageSize, "total": total, "has_more": end < total})
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
func ChangeAlbumStatusHandler(_ *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusGone, gin.H{"error": "This endpoint is retired; use music Wiki state requests"})
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
func ChangeArtistStatusHandler(_ *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusGone, gin.H{"error": "This endpoint is retired; use music Wiki state requests"})
	}
}

type MusicEntryItem struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	EntryStatus         string `json:"entry_status"`
	LifecycleStatus     string `json:"lifecycle_status"`
	EditStatus          string `json:"edit_status"`
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
// @Param type query string false "条目类型" Enums(all,album,artist,song)
// @Param status query string false "编辑状态" Enums(all,development,locked,closed)
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
				q = q.Where("edit_status = ?", statusFilter)
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
					LifecycleStatus:     a.LifecycleStatus,
					EditStatus:          a.EditStatus,
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
				q = q.Where("edit_status = ?", statusFilter)
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
					LifecycleStatus:     a.LifecycleStatus,
					EditStatus:          a.EditStatus,
					UpdatedAt:           a.UpdatedAt.Format("2006-01-02T15:04:05Z"),
					OpenDiscussionCount: discCount,
				})
			}
		}

		if entryType == "all" || entryType == "song" {
			var songs []model.Song
			q := db.Model(&model.Song{})
			if statusFilter != "all" {
				q = q.Where("edit_status = ?", statusFilter)
			}
			var songCount int64
			q.Count(&songCount)
			total += songCount
			q.Offset(offset).Limit(pageSize).Order("updated_at DESC").Find(&songs)
			for _, song := range songs {
				results = append(results, MusicEntryItem{
					ID: song.ID.String(), Name: song.Title, Type: "song",
					EntryStatus: song.Status, LifecycleStatus: song.LifecycleStatus, EditStatus: song.EditStatus,
					UpdatedAt: song.UpdatedAt.Format("2006-01-02T15:04:05Z"),
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

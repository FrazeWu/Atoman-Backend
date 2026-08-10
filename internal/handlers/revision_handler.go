package handlers

import (
	"encoding/json"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/service"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/service/s3"
)

// SetupRevisionRoutes registers revision-related routes
func SetupRevisionRoutes(router *gin.Engine, db *gorm.DB, s3Client *s3.S3) {
	revisionService := service.NewRevisionService(db)

	// Album revisions
	albums := router.Group("/api/v1/albums/:id")
	{
		albums.GET("/contributors", GetAlbumContributorsHandler(revisionService))
		albums.GET("/revisions", GetAlbumRevisionsHandler(revisionService))
		albums.GET("/revisions/:version", GetAlbumRevisionHandler(revisionService))
		albums.GET("/revisions/diff", GetAlbumRevisionDiffHandler(revisionService))
		albums.POST("/revisions", middleware.AuthMiddleware(), CreateAlbumRevisionHandler(db, revisionService, s3Client))
		albums.POST("/revisions/:version/revert", middleware.AuthMiddleware(), RevertAlbumHandler(revisionService))
	}

	// Song revisions
	songs := router.Group("/api/v1/songs/:id")
	{
		songs.GET("/revisions", GetSongRevisionsHandler(revisionService))
		songs.GET("/revisions/:version", GetSongRevisionHandler(revisionService))
		songs.GET("/revisions/diff", GetSongRevisionDiffHandler(revisionService))
		songs.POST("/revisions", middleware.AuthMiddleware(), CreateSongRevisionHandler(db, revisionService, s3Client))
		songs.POST("/revisions/:version/revert", middleware.AuthMiddleware(), RevertSongHandler(revisionService))
	}

}

// GetAlbumContributorsHandler godoc
// @Summary 获取专辑贡献者
// @Description 返回最近参与创建或修改专辑的用户，最多 10 人。
// @Tags music-revisions
// @Produce json
// @Param id path string true "专辑 UUID"
// @Success 200 {object} RevisionContributorListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/albums/{id}/contributors [get]
func GetAlbumContributorsHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return getRevisionContributorsHandler(revisionService, "album")
}

func getRevisionContributorsHandler(revisionService *service.RevisionService, contentType string) gin.HandlerFunc {
	return func(c *gin.Context) {
		contentID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid content ID"})
			return
		}

		contributors, total, err := revisionService.GetContributors(contentType, contentID, 10)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch contributors"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": contributors, "meta": gin.H{"total": total}})
	}
}

type CreateRevisionInput struct {
	BaseRevision int                    `json:"base_revision"`
	Changes      map[string]interface{} `json:"changes" binding:"required"`
	EditSummary  string                 `json:"edit_summary" binding:"required"`
}

// GetAlbumRevisionsHandler godoc
// @Summary 获取专辑修订历史
// @Description 分页返回专辑条目的 revision 历史。
// @Tags music-revisions
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param limit query int false "返回数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} RevisionListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/albums/{id}/revisions [get]
// GetAlbumRevisionsHandler returns revision history for an album
func GetAlbumRevisionsHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}

		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

		revisions, total, err := revisionService.GetRevisions("album", albumID, limit, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch revisions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":   revisions,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	}
}

// GetAlbumRevisionHandler godoc
// @Summary 获取专辑指定修订
// @Description 返回专辑某个版本号对应的 revision 详情。
// @Tags music-revisions
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param version path int true "版本号"
// @Success 200 {object} RevisionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/albums/{id}/revisions/{version} [get]
// GetAlbumRevisionHandler returns a specific revision
func GetAlbumRevisionHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}

		version, err := strconv.Atoi(c.Param("version"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid version number"})
			return
		}

		revision, err := revisionService.GetRevision("album", albumID, version)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Revision not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": revision})
	}
}

// GetAlbumRevisionDiffHandler godoc
// @Summary 比较专辑修订差异
// @Description 比较专辑两个版本之间的字段差异。
// @Tags music-revisions
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param v1 query int true "起始版本号"
// @Param v2 query int true "目标版本号"
// @Success 200 {object} RevisionDiffResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/albums/{id}/revisions/diff [get]
// GetAlbumRevisionDiffHandler compares two versions
func GetAlbumRevisionDiffHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}

		v1, err := strconv.Atoi(c.Query("v1"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid v1 parameter"})
			return
		}

		v2, err := strconv.Atoi(c.Query("v2"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid v2 parameter"})
			return
		}

		diff, err := revisionService.GetRevisionDiff("album", albumID, v1, v2)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": diff})
	}
}

// CreateAlbumRevisionHandler godoc
// @Summary 提交专辑修订
// @Description 为专辑创建并直接应用一条新的修订；发生冲突时返回冲突详情。
// @Tags music-revisions
// @Accept json
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param input body CreateRevisionInput true "修订输入"
// @Success 200 {object} RevisionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} RevisionConflictResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums/{id}/revisions [post]
// CreateAlbumRevisionHandler creates a new album revision
func CreateAlbumRevisionHandler(db *gorm.DB, revisionService *service.RevisionService, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}

		var input CreateRevisionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Get user info
		userID := authctx.CurrentUserIDString(c)
		editorUUID, _ := uuid.Parse(userID)

		userRole := c.GetString("role")

		// Check protection level
		var protection model.ContentProtection
		protectionLevel := "none"
		if err := db.Where("content_id = ? AND content_type = ?", albumID, "album").
			First(&protection).Error; err == nil {
			protectionLevel = protection.ProtectionLevel
		}

		// Apply protection rules
		if protectionLevel == "full" && !authctx.RoleAtLeast(userRole, authctx.RoleAdmin) {
			c.JSON(http.StatusForbidden, gin.H{"error": "This album is fully protected. Only admins can edit."})
			return
		}
		oldObjectKeys, newObjectKeys, err := promoteAlbumRevisionAssets(s3Client, albumID, input.Changes)
		if err != nil {
			storage.DeleteMusicObjects(s3Client, newObjectKeys)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Create revision
		revision, conflicts, err := revisionService.CreateRevision(
			"album",
			albumID,
			editorUUID,
			input.Changes,
			input.EditSummary,
			input.BaseRevision,
			true,
		)

		if err != nil {
			storage.DeleteMusicObjects(s3Client, newObjectKeys)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// If conflicts exist, return them
		if len(conflicts) > 0 {
			storage.DeleteMusicObjects(s3Client, newObjectKeys)
			c.JSON(http.StatusConflict, gin.H{
				"error":     "Edit conflicts detected",
				"conflicts": conflicts,
			})
			return
		}
		storage.DeleteMusicObjects(s3Client, oldObjectKeys)

		c.JSON(http.StatusOK, gin.H{
			"data":    revision,
			"message": "Changes saved",
		})
	}
}

// RevertAlbumHandler godoc
// @Summary 回滚专辑到指定版本
// @Description 以新的 revision 形式将专辑内容回滚到历史版本。
// @Tags music-revisions
// @Accept json
// @Produce json
// @Param id path string true "专辑 UUID"
// @Param version path int true "版本号"
// @Param input body RevisionRevertInput false "回滚说明"
// @Success 200 {object} RevisionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/albums/{id}/revisions/{version}/revert [post]
// RevertAlbumHandler reverts album to a previous version
func RevertAlbumHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		albumID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid album ID"})
			return
		}

		version, err := strconv.Atoi(c.Param("version"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid version number"})
			return
		}

		var input struct {
			EditSummary string `json:"edit_summary"`
		}
		c.ShouldBindJSON(&input)

		userID := authctx.CurrentUserIDString(c)
		editorUUID, _ := uuid.Parse(userID)

		revision, err := revisionService.RevertToRevision(
			"album",
			albumID,
			version,
			editorUUID,
			input.EditSummary,
		)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":    revision,
			"message": "Album reverted successfully",
		})
	}
}

// Song handlers (similar structure to album handlers)
// GetSongRevisionsHandler godoc
// @Summary 获取歌曲修订历史
// @Description 分页返回歌曲条目的 revision 历史。
// @Tags music-revisions
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param limit query int false "返回数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} RevisionListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/songs/{id}/revisions [get]
func GetSongRevisionsHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))

		revisions, total, err := revisionService.GetRevisions("song", songID, limit, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch revisions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":  revisions,
			"total": total,
		})
	}
}

// GetSongRevisionHandler godoc
// @Summary 获取歌曲指定修订
// @Description 返回歌曲某个版本号对应的 revision 详情。
// @Tags music-revisions
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param version path int true "版本号"
// @Success 200 {object} RevisionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/songs/{id}/revisions/{version} [get]
func GetSongRevisionHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		version, err := strconv.Atoi(c.Param("version"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid version number"})
			return
		}

		revision, err := revisionService.GetRevision("song", songID, version)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Revision not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": revision})
	}
}

// GetSongRevisionDiffHandler godoc
// @Summary 比较歌曲修订差异
// @Description 比较歌曲两个版本之间的字段差异。
// @Tags music-revisions
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param v1 query int true "起始版本号"
// @Param v2 query int true "目标版本号"
// @Success 200 {object} RevisionDiffResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/songs/{id}/revisions/diff [get]
func GetSongRevisionDiffHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		v1, _ := strconv.Atoi(c.Query("v1"))
		v2, _ := strconv.Atoi(c.Query("v2"))

		diff, err := revisionService.GetRevisionDiff("song", songID, v1, v2)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": diff})
	}
}

// CreateSongRevisionHandler godoc
// @Summary 提交歌曲修订
// @Description 为歌曲创建并直接应用一条新的修订；发生冲突时返回冲突详情。
// @Tags music-revisions
// @Accept json
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param input body CreateRevisionInput true "修订输入"
// @Success 200 {object} RevisionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} RevisionConflictResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/songs/{id}/revisions [post]
func CreateSongRevisionHandler(db *gorm.DB, revisionService *service.RevisionService, s3Client *s3.S3) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		var input CreateRevisionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID := authctx.CurrentUserIDString(c)
		editorUUID, _ := uuid.Parse(userID)
		userRole := c.GetString("role")

		var protection model.ContentProtection
		if err := db.Where("content_id = ? AND content_type = ?", songID, "song").
			First(&protection).Error; err == nil {
			if protection.ProtectionLevel == "full" && !authctx.RoleAtLeast(userRole, authctx.RoleAdmin) {
				c.JSON(http.StatusForbidden, gin.H{"error": "This song is fully protected"})
				return
			}
		}
		oldObjectKeys, newObjectKeys, err := promoteSongRevisionAssets(db, s3Client, songID, input.Changes)
		if err != nil {
			storage.DeleteMusicObjects(s3Client, newObjectKeys)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		revision, conflicts, err := revisionService.CreateRevision(
			"song",
			songID,
			editorUUID,
			input.Changes,
			input.EditSummary,
			input.BaseRevision,
			true,
		)

		if err != nil {
			storage.DeleteMusicObjects(s3Client, newObjectKeys)
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if len(conflicts) > 0 {
			storage.DeleteMusicObjects(s3Client, newObjectKeys)
			c.JSON(http.StatusConflict, gin.H{
				"error":     "Edit conflicts detected",
				"conflicts": conflicts,
			})
			return
		}
		storage.DeleteMusicObjects(s3Client, oldObjectKeys)

		c.JSON(http.StatusOK, gin.H{
			"data":    revision,
			"message": "Changes saved",
		})
	}
}

func promoteAlbumRevisionAssets(s3Client *s3.S3, albumID uuid.UUID, changes map[string]interface{}) ([]string, []string, error) {
	oldKeys := []string{}
	newKeys := []string{}
	promote := func(rawURL, destinationKey string) (string, error) {
		asset, err := storage.PromoteMusicUploadAsset(s3Client, rawURL, destinationKey)
		if err != nil {
			return "", err
		}
		oldKeys = append(oldKeys, asset.SourceKey)
		newKeys = append(newKeys, asset.DestinationKey)
		return asset.URL, nil
	}

	if coverURL, ok := changes["cover_url"].(string); ok && strings.TrimSpace(coverURL) != "" {
		promotedURL, err := promote(coverURL, storage.BuildMusicAlbumCoverVersionKey(albumID.String(), uuid.NewString(), path.Ext(coverURL)))
		if err != nil {
			return oldKeys, newKeys, err
		}
		changes["cover_url"] = promotedURL
	}
	rawTracks, ok := changes["tracks"]
	if !ok {
		return oldKeys, newKeys, nil
	}
	encoded, err := json.Marshal(rawTracks)
	if err != nil {
		return oldKeys, newKeys, err
	}
	var tracks []map[string]interface{}
	if err := json.Unmarshal(encoded, &tracks); err != nil {
		return oldKeys, newKeys, err
	}
	for _, track := range tracks {
		if removed, _ := track["removed"].(bool); removed {
			continue
		}
		songID, _ := track["id"].(string)
		if strings.TrimSpace(songID) == "" {
			songID = uuid.NewString()
			track["id"] = songID
		}
		for _, media := range []struct {
			field string
			key   func(string) string
		}{
			{field: "audio_url", key: func(rawURL string) string {
				return storage.BuildMusicAlbumTrackVersionKey(albumID.String(), songID, uuid.NewString(), path.Ext(rawURL))
			}},
			{field: "cover_url", key: func(rawURL string) string {
				return storage.BuildMusicAlbumCoverVersionKey(albumID.String(), uuid.NewString(), path.Ext(rawURL))
			}},
		} {
			rawURL, _ := track[media.field].(string)
			if strings.TrimSpace(rawURL) == "" {
				continue
			}
			promotedURL, err := promote(rawURL, media.key(rawURL))
			if err != nil {
				return oldKeys, newKeys, err
			}
			track[media.field] = promotedURL
		}
	}
	changes["tracks"] = tracks
	return oldKeys, newKeys, nil
}

func promoteSongRevisionAssets(db *gorm.DB, s3Client *s3.S3, songID uuid.UUID, changes map[string]interface{}) ([]string, []string, error) {
	coverURL, ok := changes["cover_url"].(string)
	if !ok || strings.TrimSpace(coverURL) == "" {
		return nil, nil, nil
	}
	var song model.Song
	if err := db.Select("id", "album_id").First(&song, "id = ?", songID).Error; err != nil {
		return nil, nil, err
	}
	destinationKey := storage.BuildMusicSongCoverVersionKey(song.ID.String(), uuid.NewString(), path.Ext(coverURL))
	if song.AlbumID != nil {
		destinationKey = storage.BuildMusicAlbumCoverVersionKey(song.AlbumID.String(), uuid.NewString(), path.Ext(coverURL))
	}
	asset, err := storage.PromoteMusicUploadAsset(
		s3Client, coverURL, destinationKey,
	)
	if err != nil {
		return nil, nil, err
	}
	changes["cover_url"] = asset.URL
	return []string{asset.SourceKey}, []string{asset.DestinationKey}, nil
}

// RevertSongHandler godoc
// @Summary 回滚歌曲到指定版本
// @Description 以新的 revision 形式将歌曲内容回滚到历史版本。
// @Tags music-revisions
// @Accept json
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param version path int true "版本号"
// @Param input body RevisionRevertInput false "回滚说明"
// @Success 200 {object} RevisionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/songs/{id}/revisions/{version}/revert [post]
func RevertSongHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		version, _ := strconv.Atoi(c.Param("version"))

		var input struct {
			EditSummary string `json:"edit_summary"`
		}
		c.ShouldBindJSON(&input)

		userID := authctx.CurrentUserIDString(c)
		editorUUID, _ := uuid.Parse(userID)

		revision, err := revisionService.RevertToRevision(
			"song",
			songID,
			version,
			editorUUID,
			input.EditSummary,
		)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"data":    revision,
			"message": "Song reverted successfully",
		})
	}
}

package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/service"
)

func SetupArtistWikiRoutes(router *gin.Engine, db *gorm.DB) {
	revisionService := service.NewRevisionService(db)

	artists := router.Group("/api/v1/artists")
	{
		artists.GET("/:id", GetArtistByIDHandler(db))
		artists.PUT("/:id", middleware.AuthMiddleware(), UpdateArtistHandler(db, revisionService))
		artists.GET("/:id/revisions", GetArtistRevisionsHandler(revisionService))
		artists.GET("/:id/revisions/:version", GetArtistRevisionHandler(revisionService))
		artists.POST("/:id/edit", middleware.AuthMiddleware(), CreateArtistRevisionHandler(db, revisionService))
		artists.POST("/:id/revert/:version", middleware.AuthMiddleware(), RevertArtistHandler(revisionService))
		artists.GET("/:id/aliases", GetArtistAliasesHandler(db))
		artists.POST("/:id/aliases", middleware.AuthMiddleware(), AddArtistAliasHandler(db))
		artists.DELETE("/:id/aliases/:aliasId", middleware.AuthMiddleware(), DeleteArtistAliasHandler(db))
	}

	admin := router.Group("/api/v1/admin/artists")
	admin.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	{
		admin.POST("/:id/merge", MergeArtistsHandler(db))
	}
}

// GetArtistByIDHandler godoc
// @Summary 获取艺人详情
// @Description 返回艺人详情、别名与专辑信息；若该艺人已合并则附带 redirect_to。
// @Tags music-artists
// @Produce json
// @Param id path string true "艺人 UUID"
// @Success 200 {object} ArtistWikiResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/artists/{id} [get]
func GetArtistByIDHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		var artist model.Artist
		if err := db.Preload("Aliases").Preload("Albums").Preload("Albums.Artists").
			First(&artist, "id = ?", artistID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Artist not found"})
			return
		}

		if artist.RedirectTo != nil {
			c.JSON(http.StatusOK, gin.H{
				"data":        artist,
				"redirect_to": artist.RedirectTo,
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": artist})
	}
}

// UpdateArtistHandler godoc
// @Summary 更新艺人信息
// @Description 更新艺人 wiki 条目，并在存在 revision 基线时记录一条 revision。
// @Tags music-artists
// @Accept json
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param input body ArtistUpdateInput true "艺人更新输入"
// @Success 200 {object} ArtistWikiResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} RevisionConflictResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id} [put]
func UpdateArtistHandler(db *gorm.DB, revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		var input struct {
			Name        string `json:"name"`
			Bio         string `json:"bio"`
			Nationality string `json:"nationality"`
			BirthYear   int    `json:"birth_year"`
			DeathYear   int    `json:"death_year"`
			Members     string `json:"members"`
			ImageURL    string `json:"image_url"`
			EditSummary string `json:"edit_summary"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var artist model.Artist
		if err := db.First(&artist, "id = ?", artistID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Artist not found"})
			return
		}

		updates := map[string]interface{}{}
		if input.Name != "" {
			updates["name"] = input.Name
		}
		if input.Bio != "" {
			updates["bio"] = input.Bio
		}
		if input.Nationality != "" {
			updates["nationality"] = input.Nationality
		}
		if input.BirthYear != 0 {
			updates["birth_year"] = input.BirthYear
		}
		if input.DeathYear != 0 {
			updates["death_year"] = input.DeathYear
		}
		if input.Members != "" {
			updates["members"] = input.Members
		}
		if input.ImageURL != "" {
			updates["image_url"] = input.ImageURL
		}

		if len(updates) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "No changes provided"})
			return
		}

		if err := db.Model(&artist).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update artist"})
			return
		}

		userID := c.GetString("user_id")
		editorUUID, _ := uuid.Parse(userID)
		userRole := c.GetString("role")
		autoApprove := userRole == "admin"

		var latestRev model.Revision
		var baseVersion int
		if err := db.Where("content_id = ? AND content_type = ?", artistID, "artist").
			Order("version_number DESC").First(&latestRev).Error; err == nil {
			baseVersion = latestRev.VersionNumber
		}

		if baseVersion > 0 {
			_, conflicts, err := revisionService.CreateRevision(
				"artist",
				artistID,
				editorUUID,
				updates,
				input.EditSummary,
				baseVersion,
				autoApprove,
			)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			if len(conflicts) > 0 {
				c.JSON(http.StatusConflict, gin.H{"error": "Edit conflicts detected", "conflicts": conflicts})
				return
			}
		}

		if err := db.Preload("Aliases").Preload("Albums").First(&artist, "id = ?", artistID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload artist"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": artist})
	}
}

// GetArtistRevisionsHandler godoc
// @Summary 获取艺人修订历史
// @Description 分页返回艺人条目的 revision 历史。
// @Tags music-artists
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param limit query int false "返回数量"
// @Param offset query int false "偏移量"
// @Success 200 {object} RevisionListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/artists/{id}/revisions [get]
func GetArtistRevisionsHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
		offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
		revisions, total, err := revisionService.GetRevisions("artist", artistID, limit, offset)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch revisions"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": revisions, "total": total, "limit": limit, "offset": offset})
	}
}

// GetArtistRevisionHandler godoc
// @Summary 获取单个艺人修订版本
// @Description 按版本号返回艺人 revision 详情。
// @Tags music-artists
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param version path int true "版本号"
// @Success 200 {object} RevisionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/artists/{id}/revisions/{version} [get]
func GetArtistRevisionHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		version, err := strconv.Atoi(c.Param("version"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid version number"})
			return
		}

		var revision model.Revision
		if err := revisionService.GetDB().
			Where("content_id = ? AND content_type = ? AND version_number = ?", artistID, "artist", version).
			Preload("Editor").
			First(&revision).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Revision not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": revision})
	}
}

// CreateArtistRevisionHandler godoc
// @Summary 创建艺人修订
// @Description 基于指定基线 revision 提交艺人修订。
// @Tags music-artists
// @Accept json
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param input body CreateRevisionInput true "修订输入"
// @Success 200 {object} RevisionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} RevisionConflictResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id}/edit [post]
func CreateArtistRevisionHandler(db *gorm.DB, revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		var input CreateRevisionInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID := c.GetString("user_id")
		editorUUID, _ := uuid.Parse(userID)
		userRole := c.GetString("role")
		autoApprove := userRole == "admin"

		var protection model.ContentProtection
		if err := db.Where("content_id = ? AND content_type = ?", artistID, "artist").
			First(&protection).Error; err == nil {
			if protection.ProtectionLevel == "full" && userRole != "admin" {
				c.JSON(http.StatusForbidden, gin.H{"error": "This artist is fully protected"})
				return
			}
			if protection.ProtectionLevel == "semi" {
				autoApprove = false
			}
		}

		revision, conflicts, err := revisionService.CreateRevision(
			"artist", artistID, editorUUID, input.Changes, input.EditSummary, input.BaseRevision, autoApprove,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(conflicts) > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Edit conflicts detected", "conflicts": conflicts})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": revision, "message": statusMessage(autoApprove)})
	}
}

// RevertArtistHandler godoc
// @Summary 回滚艺人到指定版本
// @Description 将艺人条目回滚到某个 revision 版本。
// @Tags music-artists
// @Accept json
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param version path int true "版本号"
// @Param input body RevisionRevertInput false "可选编辑摘要"
// @Success 200 {object} RevisionActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id}/revert/{version} [post]
func RevertArtistHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		version, _ := strconv.Atoi(c.Param("version"))
		var input struct {
			EditSummary string `json:"edit_summary"`
		}
		c.ShouldBindJSON(&input)

		userID := c.GetString("user_id")
		editorUUID, _ := uuid.Parse(userID)
		revision, err := revisionService.RevertToRevision("artist", artistID, version, editorUUID, input.EditSummary)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": revision, "message": "Artist reverted successfully"})
	}
}

// GetArtistAliasesHandler godoc
// @Summary 获取艺人别名列表
// @Description 返回指定艺人的所有别名。
// @Tags music-artists
// @Produce json
// @Param id path string true "艺人 UUID"
// @Success 200 {object} ArtistAliasListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/artists/{id}/aliases [get]
func GetArtistAliasesHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		var aliases []model.ArtistAlias
		if err := db.Where("artist_id = ?", artistID).Find(&aliases).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch aliases"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": aliases})
	}
}

// AddArtistAliasHandler godoc
// @Summary 添加艺人别名
// @Description 为指定艺人创建一个别名。
// @Tags music-artists
// @Accept json
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param input body ArtistAliasInput true "艺人别名输入"
// @Success 201 {object} ArtistAliasResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id}/aliases [post]
func AddArtistAliasHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		var input struct {
			Alias      string `json:"alias" binding:"required"`
			IsMainName bool   `json:"is_main_name"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		alias := model.ArtistAlias{
			ArtistID:   artistID,
			Alias:      input.Alias,
			IsMainName: input.IsMainName,
		}
		if err := db.Create(&alias).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create alias"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"data": alias})
	}
}

// DeleteArtistAliasHandler godoc
// @Summary 删除艺人别名
// @Description 删除指定艺人上的某个别名。
// @Tags music-artists
// @Produce json
// @Param id path string true "艺人 UUID"
// @Param aliasId path string true "别名 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists/{id}/aliases/{aliasId} [delete]
func DeleteArtistAliasHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		artistID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid artist ID"})
			return
		}

		aliasID, err := uuid.Parse(c.Param("aliasId"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid alias ID"})
			return
		}

		if err := db.Where("id = ? AND artist_id = ?", aliasID, artistID).
			Delete(&model.ArtistAlias{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete alias"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Alias deleted"})
	}
}

// MergeArtistsHandler godoc
// @Summary 合并艺人
// @Description 将源艺人的关联关系、修订和讨论迁移到目标艺人，并设置 redirect_to。
// @Tags music-artists
// @Accept json
// @Produce json
// @Param id path string true "目标艺人 UUID"
// @Param input body ArtistMergeInput true "艺人合并输入"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/artists/{id}/merge [post]
func MergeArtistsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		targetID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid target artist ID"})
			return
		}

		var input struct {
			SourceID uuid.UUID `json:"source_id" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID := c.GetString("user_id")
		mergedByUUID, _ := uuid.Parse(userID)

		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("UPDATE album_artists SET artist_id = ? WHERE artist_id = ?", targetID, input.SourceID).Error; err != nil {
				return err
			}
			if err := tx.Exec("UPDATE song_artists SET artist_id = ? WHERE artist_id = ?", targetID, input.SourceID).Error; err != nil {
				return err
			}
			if err := tx.Exec(
				"UPDATE revisions SET content_id = ? WHERE content_id = ? AND content_type = 'artist'",
				targetID,
				input.SourceID,
			).Error; err != nil {
				return err
			}
			if err := tx.Exec(
				"UPDATE discussions SET content_id = ? WHERE content_id = ? AND content_type = 'artist'",
				targetID,
				input.SourceID,
			).Error; err != nil {
				return err
			}

			merge := model.ArtistMerge{
				SourceArtistID: input.SourceID,
				TargetArtistID: targetID,
				MergedBy:       mergedByUUID,
				MergedAt:       time.Now(),
			}
			if err := tx.Create(&merge).Error; err != nil {
				return err
			}

			if err := tx.Model(&model.Artist{}).Where("id = ?", input.SourceID).
				Update("redirect_to", targetID).Error; err != nil {
				return err
			}

			return nil
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to merge artists"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Artists merged successfully"})
	}
}

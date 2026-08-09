package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/modules/music"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"atoman/internal/service"
)

func SetupArtistWikiRoutes(router *gin.Engine, db *gorm.DB) {
	revisionService := service.NewRevisionService(db)

	artists := router.Group("/api/v1/artists")
	{
		artists.GET("/:id", GetArtistByIDHandler(db))
		artists.GET("/:id/contributors", GetArtistContributorsHandler(revisionService))
		artists.GET("/:id/revisions", GetArtistRevisionsHandler(revisionService))
		artists.GET("/:id/revisions/:version", GetArtistRevisionHandler(revisionService))
		artists.POST("/:id/revisions", middleware.AuthMiddleware(), CreateArtistRevisionHandler(db, revisionService))
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

// GetArtistContributorsHandler godoc
// @Summary 获取艺术家贡献者
// @Description 返回最近参与创建或修改艺术家资料的用户，最多 10 人。
// @Tags music-artists
// @Produce json
// @Param id path string true "艺术家 UUID"
// @Success 200 {object} RevisionContributorListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/artists/{id}/contributors [get]
func GetArtistContributorsHandler(revisionService *service.RevisionService) gin.HandlerFunc {
	return getRevisionContributorsHandler(revisionService, "artist")
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

		revision, err := revisionService.GetRevision("artist", artistID, version)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Revision not found"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": revision})
	}
}

// CreateArtistRevisionHandler godoc
// @Summary 创建艺人修订
// @Description 基于指定基线 revision 创建并直接应用艺人修订。
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
// @Router /api/v1/artists/{id}/revisions [post]
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

		userID := authctx.CurrentUserIDString(c)
		editorUUID, _ := uuid.Parse(userID)
		userRole := c.GetString("role")

		var protection model.ContentProtection
		if err := db.Where("content_id = ? AND content_type = ?", artistID, "artist").
			First(&protection).Error; err == nil {
			if protection.ProtectionLevel == "full" && !authctx.RoleAtLeast(userRole, authctx.RoleAdmin) {
				c.JSON(http.StatusForbidden, gin.H{"error": "This artist is fully protected"})
				return
			}
		}

		revision, conflicts, err := revisionService.CreateRevision(
			"artist", artistID, editorUUID, input.Changes, input.EditSummary, input.BaseRevision, true,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(conflicts) > 0 {
			c.JSON(http.StatusConflict, gin.H{"error": "Edit conflicts detected", "conflicts": conflicts})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": revision, "message": "Changes saved"})
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

		userID := authctx.CurrentUserIDString(c)
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
// @Description 将源艺人的音乐关联、收藏和别名迁移到目标艺人，并设置 redirect_to。
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

		user, ok := authctx.Current(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Login required"})
			return
		}
		if err := music.NewService(db).MergeArtists(user, input.SourceID, targetID); err != nil {
			httpx.Error(c, err)
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Artists merged successfully"})
	}
}

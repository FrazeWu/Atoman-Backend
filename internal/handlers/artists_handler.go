package handlers

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
)

type ArtistInput struct {
	Name string `json:"name" binding:"required"`
	Bio  string `json:"bio"`
}

func SetupArtistRoutes(router *gin.Engine, db *gorm.DB) {
	artists := router.Group("/api/v1/artists")
	{
		artists.GET("", GetArtistsHandler(db))
		artists.POST("", middleware.AuthMiddleware(), CreateArtistHandler(db))
	}
}

// GetArtistsHandler godoc
// @Summary 获取艺人列表
// @Description 返回艺人列表，可按名称或别名搜索。
// @Tags music-artists
// @Produce json
// @Param q query string false "搜索关键字"
// @Success 200 {array} model.Artist
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/artists [get]
func GetArtistsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		var artists []model.Artist
		if q != "" {
			like := "%" + strings.ToLower(q) + "%"
			if err := db.Raw(`SELECT DISTINCT "Artists".* FROM "Artists"
				LEFT JOIN artist_aliases ON artist_aliases.artist_id = "Artists".id
				WHERE LOWER("Artists".name) LIKE ? OR LOWER(artist_aliases.alias) LIKE ?
				ORDER BY "Artists".name ASC`, like, like).Scan(&artists).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch artists"})
				return
			}
		} else {
			if err := db.Order("name ASC").Find(&artists).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch artists"})
				return
			}
		}
		c.JSON(http.StatusOK, artists)
	}
}

// CreateArtistHandler godoc
// @Summary 创建艺人
// @Description 创建一个新的艺人条目。
// @Tags music-artists
// @Accept json
// @Produce json
// @Param input body ArtistInput true "艺人输入"
// @Success 201 {object} model.Artist
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ConflictWithIDResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/artists [post]
func CreateArtistHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input ArtistInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		var existingArtist model.Artist
		result := db.Where("name = ?", input.Name).First(&existingArtist)
		if result.Error == nil {
			c.JSON(http.StatusConflict, gin.H{"error": "Artist already exists", "id": existingArtist.ID})
			return
		}

		artist := model.Artist{
			Name: input.Name,
			Bio:  input.Bio,
		}

		if err := db.Create(&artist).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create artist"})
			return
		}

		c.JSON(http.StatusCreated, artist)
	}
}

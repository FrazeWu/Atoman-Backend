package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
)

func SetupLyricAnnotationRoutes(router *gin.Engine, db *gorm.DB) {
	songs := router.Group("/api/v1/songs/:id")
	{
		songs.GET("/annotations", GetSongAnnotationsHandler(db))
		songs.POST("/annotations", middleware.AuthMiddleware(), CreateSongAnnotationHandler(db))
		songs.PUT("/annotations/:annotationId", middleware.AuthMiddleware(), UpdateSongAnnotationHandler(db))
		songs.DELETE("/annotations/:annotationId", middleware.AuthMiddleware(), DeleteSongAnnotationHandler(db))
	}
}

type AnnotationGroup struct {
	LineNumber  int                     `json:"line_number"`
	Annotations []model.LyricAnnotation `json:"annotations"`
}

// GetSongAnnotationsHandler godoc
// @Summary 获取歌曲逐行注释
// @Description 按行号分组返回某首歌曲的歌词注释。
// @Tags music-annotations
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Success 200 {object} SongAnnotationGroupListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/songs/{id}/annotations [get]
func GetSongAnnotationsHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		var annotations []model.LyricAnnotation
		if err := db.Where("song_id = ?", songID).Preload("User").
			Order("line_number ASC, created_at ASC").Find(&annotations).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch annotations"})
			return
		}

		// Group by line number
		groupMap := map[int]*AnnotationGroup{}
		var groupOrder []int
		for _, a := range annotations {
			if _, ok := groupMap[a.LineNumber]; !ok {
				groupMap[a.LineNumber] = &AnnotationGroup{LineNumber: a.LineNumber}
				groupOrder = append(groupOrder, a.LineNumber)
			}
			groupMap[a.LineNumber].Annotations = append(groupMap[a.LineNumber].Annotations, a)
		}

		groups := make([]AnnotationGroup, 0, len(groupOrder))
		for _, ln := range groupOrder {
			groups = append(groups, *groupMap[ln])
		}

		c.JSON(http.StatusOK, gin.H{"data": groups})
	}
}

// CreateSongAnnotationHandler godoc
// @Summary 创建歌词注释
// @Description 为指定歌曲的某一行歌词创建注释。
// @Tags music-annotations
// @Accept json
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param input body SongAnnotationInput true "歌词注释输入"
// @Success 201 {object} SongAnnotationResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/songs/{id}/annotations [post]
func CreateSongAnnotationHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}

		var input struct {
			LineNumber int    `json:"line_number" binding:"required"`
			Content    string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userID := c.GetString("user_id")
		userUUID, _ := uuid.Parse(userID)

		annotation := model.LyricAnnotation{
			SongID:     songID,
			LineNumber: input.LineNumber,
			Content:    input.Content,
			UserID:     userUUID,
		}
		if err := db.Create(&annotation).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create annotation"})
			return
		}

		db.Preload("User").First(&annotation, "id = ?", annotation.ID)
		c.JSON(http.StatusCreated, gin.H{"data": annotation})
	}
}

// UpdateSongAnnotationHandler godoc
// @Summary 更新歌词注释
// @Description 更新当前用户创建的歌词注释内容。
// @Tags music-annotations
// @Accept json
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param annotationId path string true "注释 UUID"
// @Param input body SongAnnotationUpdateInput true "歌词注释更新输入"
// @Success 200 {object} SongAnnotationResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/songs/{id}/annotations/{annotationId} [put]
func UpdateSongAnnotationHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}
		annotationID, err := uuid.Parse(c.Param("annotationId"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid annotation ID"})
			return
		}

		userID := c.GetString("user_id")
		userUUID, _ := uuid.Parse(userID)

		var annotation model.LyricAnnotation
		if err := db.Where("id = ? AND song_id = ?", annotationID, songID).First(&annotation).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Annotation not found"})
			return
		}

		if annotation.UserID != userUUID {
			c.JSON(http.StatusForbidden, gin.H{"error": "You can only edit your own annotations"})
			return
		}

		var input struct {
			Content string `json:"content" binding:"required"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if err := db.Model(&annotation).Update("content", input.Content).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update annotation"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": annotation})
	}
}

// DeleteSongAnnotationHandler godoc
// @Summary 删除歌词注释
// @Description 删除当前用户自己的注释，管理员也可删除任意注释。
// @Tags music-annotations
// @Produce json
// @Param id path string true "歌曲 UUID"
// @Param annotationId path string true "注释 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/songs/{id}/annotations/{annotationId} [delete]
func DeleteSongAnnotationHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		songID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid song ID"})
			return
		}
		annotationID, err := uuid.Parse(c.Param("annotationId"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid annotation ID"})
			return
		}

		userID := c.GetString("user_id")
		userUUID, _ := uuid.Parse(userID)
		userRole := c.GetString("role")

		var annotation model.LyricAnnotation
		if err := db.Where("id = ? AND song_id = ?", annotationID, songID).First(&annotation).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "Annotation not found"})
			return
		}

		if annotation.UserID != userUUID && userRole != "admin" {
			c.JSON(http.StatusForbidden, gin.H{"error": "Permission denied"})
			return
		}

		if err := db.Delete(&annotation).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete annotation"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Annotation deleted"})
	}
}

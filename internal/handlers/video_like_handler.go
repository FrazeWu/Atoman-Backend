package handlers

import (
	"net/http"

	"atoman/internal/model"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type VideoLikeInput struct {
	TargetID string `json:"target_id" binding:"required"`
}

// ToggleVideoLike godoc
// @Summary 点赞或取消点赞视频
// @Description 根据请求方法为视频添加或删除当前用户的点赞。
// @Tags videos
// @Accept json
// @Produce json
// @Param input body VideoLikeInput true "视频 ID"
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/videos/likes [post]
// @Router /api/v1/videos/likes [delete]
func ToggleVideoLike(db *gorm.DB, liked bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("userID").(uuid.UUID)
		var input VideoLikeInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "视频 ID 无效"})
			return
		}
		videoID, err := uuid.Parse(input.TargetID)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "视频 ID 无效"})
			return
		}
		var video model.Video
		if err := db.First(&video, "id = ?", videoID).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "视频不存在"})
			return
		}
		if video.UserID != userID && (video.Status != "published" || video.Visibility != "public") {
			c.JSON(http.StatusForbidden, gin.H{"error": "无权操作此视频"})
			return
		}
		if liked {
			like := model.Like{UserID: userID, TargetType: "video", TargetID: videoID}
			if err := db.Where(model.Like{UserID: userID, TargetType: "video", TargetID: videoID}).FirstOrCreate(&like).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "点赞失败"})
				return
			}
		} else if err := db.Where("user_id = ? AND target_type = ? AND target_id = ?", userID, "video", videoID).Delete(&model.Like{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "取消点赞失败"})
			return
		}
		var count int64
		db.Model(&model.Like{}).Where("target_type = ? AND target_id = ?", "video", videoID).Count(&count)
		c.JSON(http.StatusOK, gin.H{"data": gin.H{"liked": liked, "like_count": count}})
	}
}

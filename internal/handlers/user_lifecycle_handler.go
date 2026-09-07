package handlers

import (
	"net/http"

	"atoman/internal/model"
	"atoman/internal/modules/blog"
	"atoman/internal/platform/authsession"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DeleteCurrentUser immediately deactivates the current account and keeps published content anonymously.
// @Summary 注销当前账户
// @Description 立即停用账号，撤销全部会话并将公开内容与频道匿名化保留。
// @Tags users
// @Produce json
// @Success 204
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/me [delete]
func DeleteCurrentUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		if err := blog.NewService(db).AnonymizeUserChannels(userID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to anonymize channels"})
			return
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			if tx.Migrator().HasTable(&model.ContentEntry{}) {
				if err := tx.Model(&model.ContentEntry{}).Where("author_id = ?", userID).Update("author_id", nil).Error; err != nil {
					return err
				}
			}
			var user model.User
			if err := tx.Where("uuid = ?", userID).First(&user).Error; err != nil {
				return err
			}
			updates := map[string]any{
				"username":     "deleted-" + userID.String(),
				"email":        "deleted-" + userID.String() + "@invalid.local",
				"password":     "",
				"display_name": "已注销用户",
				"avatar_url":   "",
				"bio":          "",
				"website":      "",
				"location":     "",
				"is_active":    false,
				"auth_version": gorm.Expr("auth_version + 1"),
			}
			if err := tx.Model(&user).Updates(updates).Error; err != nil {
				return err
			}
			return authsession.New(tx).RevokeUser(userID)
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete account"})
			return
		}

		clearAuthTokenCookie(c)
		c.Status(http.StatusNoContent)
	}
}

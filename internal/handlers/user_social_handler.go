package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
)

// FollowUser godoc
// @Summary 关注用户
// @Description 当前用户关注指定 UUID 用户。
// @Tags users
// @Produce json
// @Param id path string true "目标用户 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/{id}/follow [post]
func FollowUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		targetIDStr := c.Param("id")
		targetID, err := uuid.Parse(targetIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user UUID"})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if userID == targetID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "You cannot follow yourself"})
			return
		}

		// Check if target user exists
		var targetUser model.User
		if err := db.Where("uuid = ?", targetID).First(&targetUser).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}

		follow := model.Follow{
			FollowerID:  userID,
			FollowingID: targetID,
		}

		if err := db.Where(model.Follow{FollowerID: userID, FollowingID: targetID}).FirstOrCreate(&follow).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to follow user"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// UnfollowUser removes a follow relationship
// UnfollowUser godoc
// @Summary 取消关注用户
// @Description 当前用户取消关注指定 UUID 用户。
// @Tags users
// @Produce json
// @Param id path string true "目标用户 UUID"
// @Success 200 {object} MessageResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/{id}/follow [delete]
func UnfollowUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		targetID := c.Param("id")
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		if err := db.Where("follower_id = ? AND following_id = ?", userID, targetID).Delete(&model.Follow{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unfollow user"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// ListBlockedUsers returns users blocked by the current user.
func ListBlockedUsers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		var blocks []model.UserBlock
		if err := db.Preload("Blocked").Where("blocker_id = ?", userID).Order("created_at DESC").Find(&blocks).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch blocked users"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": blocks, "message": "ok"})
	}
}

// BlockUser blocks a user for private messages.
func BlockUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		targetID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user UUID"})
			return
		}
		if userID == targetID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "You cannot block yourself"})
			return
		}

		var target model.User
		if err := db.Where("uuid = ?", targetID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to find user"})
			return
		}

		block := model.UserBlock{BlockerID: userID, BlockedID: target.UUID}
		if err := db.Where(model.UserBlock{BlockerID: userID, BlockedID: target.UUID}).FirstOrCreate(&block).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to block user"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// UnblockUser removes a private-message block.
func UnblockUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		targetID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user UUID"})
			return
		}
		if err := db.Where("blocker_id = ? AND blocked_id = ?", userID, targetID).Delete(&model.UserBlock{}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unblock user"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	}
}

// GetUserFollowers returns a list of users following the specified user
// GetUserFollowers godoc
// @Summary 获取用户粉丝列表
// @Description 返回关注该用户的用户列表。
// @Tags users
// @Produce json
// @Param id path string true "用户 UUID"
// @Success 200 {object} UserListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/users/{id}/followers [get]
func GetUserFollowers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var follows []model.Follow

		if err := db.Where("following_id = ?", id).Find(&follows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch followers"})
			return
		}

		// Get user details for followers
		var followerIDs []uuid.UUID
		for _, f := range follows {
			followerIDs = append(followerIDs, f.FollowerID)
		}

		var users []model.User
		if len(followerIDs) > 0 {
			db.Where("uuid IN ?", followerIDs).Find(&users)
		}

		c.JSON(http.StatusOK, gin.H{"data": users, "message": "ok"})
	}
}

// GetUserFollowing returns a list of users the specified user is following
// GetUserFollowing godoc
// @Summary 获取用户关注列表
// @Description 返回该用户正在关注的用户列表。
// @Tags users
// @Produce json
// @Param id path string true "用户 UUID"
// @Success 200 {object} UserListResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/users/{id}/following [get]
func GetUserFollowing(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var follows []model.Follow

		if err := db.Where("follower_id = ?", id).Find(&follows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch following"})
			return
		}

		// Get user details for following
		var followingIDs []uuid.UUID
		for _, f := range follows {
			followingIDs = append(followingIDs, f.FollowingID)
		}

		var users []model.User
		if len(followingIDs) > 0 {
			db.Where("uuid IN ?", followingIDs).Find(&users)
		}

		c.JSON(http.StatusOK, gin.H{"data": users, "message": "ok"})
	}
}

// SearchUsers returns users matching the query string.
// GET /api/users/search?q=<query>&limit=<n>&scope=mention

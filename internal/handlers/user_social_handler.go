package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
)

type publicRelationUser struct {
	UUID        uuid.UUID `json:"uuid"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
}

func relationUser(user model.User) publicRelationUser {
	return publicRelationUser{UUID: user.UUID, Username: user.Username, DisplayName: user.DisplayName, AvatarURL: user.AvatarURL}
}

type publicRelationChannel struct {
	Kind     string              `json:"kind"`
	ID       uuid.UUID           `json:"id"`
	Name     string              `json:"name"`
	Slug     string              `json:"slug,omitempty"`
	CoverURL string              `json:"cover_url,omitempty"`
	Owner    *publicRelationUser `json:"owner,omitempty"`
}

func canViewRelations(c *gin.Context, db *gorm.DB, targetID uuid.UUID) bool {
	viewer, ok := authctx.Current(c)
	if ok && viewer.ID == targetID {
		return true
	}
	privateProfile, showRelations := publicUserPrivacy(db, targetID)
	return !privateProfile && showRelations
}

func relationTargetID(c *gin.Context) (uuid.UUID, bool) {
	targetID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user UUID"})
		return uuid.Nil, false
	}
	return targetID, true
}

// FollowUser godoc
// @Summary 关注用户
// @Description 当前用户关注指定 UUID 用户。
// @Tags users
// @Produce json
// @Param id path string true "目标用户 UUID"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
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
		privateProfile, _ := publicUserPrivacy(db, targetID)
		if privateProfile {
			var existing model.Follow
			err := db.Where("follower_id = ? AND following_id = ?", userID, targetID).First(&existing).Error
			switch {
			case err == nil:
				// Existing relationships remain valid when the target becomes private.
			case errors.Is(err, gorm.ErrRecordNotFound):
				c.JSON(http.StatusForbidden, gin.H{"error": "User profile is private"})
				return
			default:
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check user privacy"})
				return
			}
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
		targetID, ok := relationTargetID(c)
		if !ok || !canViewRelations(c, db, targetID) {
			if ok {
				c.JSON(http.StatusForbidden, gin.H{"error": "User relations are private"})
			}
			return
		}

		var follows []model.Follow
		if err := db.Where("following_id = ?", targetID).Find(&follows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch followers"})
			return
		}

		followerIDs := make([]uuid.UUID, 0, len(follows))
		for _, f := range follows {
			followerIDs = append(followerIDs, f.FollowerID)
		}

		var channelIDs []uuid.UUID
		if err := db.Model(&model.Channel{}).Where("user_id = ?", targetID).Pluck("id", &channelIDs).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch follower channels"})
			return
		}
		if len(channelIDs) > 0 && db.Migrator().HasTable(&model.Subscription{}) && db.Migrator().HasTable(&model.FeedSource{}) {
			var subscriberIDs []uuid.UUID
			err := db.Table("subscriptions").
				Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").
				Where("subscriptions.deleted_at IS NULL AND subscriptions.user_id <> ? AND feed_sources.deleted_at IS NULL AND feed_sources.source_type = ? AND feed_sources.source_id IN ?", targetID, "internal_channel", channelIDs).
				Pluck("subscriptions.user_id", &subscriberIDs).Error
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch channel subscribers"})
				return
			}
			followerIDs = append(followerIDs, subscriberIDs...)
		}

		users := loadPublicRelationUsers(db, followerIDs)
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
		targetID, ok := relationTargetID(c)
		if !ok || !canViewRelations(c, db, targetID) {
			if ok {
				c.JSON(http.StatusForbidden, gin.H{"error": "User relations are private"})
			}
			return
		}
		var follows []model.Follow

		if err := db.Where("follower_id = ?", targetID).Find(&follows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch following"})
			return
		}

		followingIDs := make([]uuid.UUID, 0, len(follows))
		for _, f := range follows {
			followingIDs = append(followingIDs, f.FollowingID)
		}

		items := make([]any, 0, len(followingIDs))
		for _, user := range loadPublicRelationUsers(db, followingIDs) {
			items = append(items, user)
		}
		if db.Migrator().HasTable(&model.Subscription{}) && db.Migrator().HasTable(&model.FeedSource{}) {
			var subscriptions []model.Subscription
			if err := db.Preload("FeedSource").Where("user_id = ?", targetID).Find(&subscriptions).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch channel subscriptions"})
				return
			}
			channelIDs := make([]uuid.UUID, 0)
			for _, subscription := range subscriptions {
				if subscription.FeedSource == nil || subscription.FeedSource.SourceType != "internal_channel" || subscription.FeedSource.SourceID == nil {
					continue
				}
				channelIDs = append(channelIDs, *subscription.FeedSource.SourceID)
			}
			if len(channelIDs) > 0 {
				var channels []model.Channel
				if err := db.Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch followed channels"})
					return
				}
				owners := make(map[uuid.UUID]publicRelationUser)
				ownerIDs := make([]uuid.UUID, 0, len(channels))
				for _, channel := range channels {
					if channel.UserID != nil {
						ownerIDs = append(ownerIDs, *channel.UserID)
					}
				}
				for _, user := range loadPublicRelationUsers(db, ownerIDs) {
					owners[user.UUID] = user
				}
				for _, channel := range channels {
					item := publicRelationChannel{Kind: "channel", ID: channel.ID, Name: channel.Name, Slug: channel.Slug, CoverURL: channel.CoverURL}
					if channel.UserID != nil {
						if owner, exists := owners[*channel.UserID]; exists {
							item.Owner = &owner
						}
					}
					items = append(items, item)
				}
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": items, "message": "ok"})
	}
}

func loadPublicRelationUsers(db *gorm.DB, ids []uuid.UUID) []publicRelationUser {
	if len(ids) == 0 {
		return []publicRelationUser{}
	}
	var users []model.User
	if err := db.Select("uuid, username, display_name, avatar_url").Where("uuid IN ? AND is_active = ?", ids, true).Find(&users).Error; err != nil {
		return []publicRelationUser{}
	}
	seen := make(map[uuid.UUID]struct{}, len(users))
	result := make([]publicRelationUser, 0, len(users))
	for _, user := range users {
		if _, exists := seen[user.UUID]; exists {
			continue
		}
		seen[user.UUID] = struct{}{}
		result = append(result, relationUser(user))
	}
	return result
}

func countUniqueRelationUsers(db *gorm.DB, userID uuid.UUID, followers bool) int64 {
	ids := make(map[uuid.UUID]struct{})
	var followIDs []uuid.UUID
	followQuery := db.Model(&model.Follow{})
	if followers {
		followQuery = followQuery.Where("following_id = ?", userID)
		if err := followQuery.Pluck("follower_id", &followIDs).Error; err != nil {
			return 0
		}
	} else {
		followQuery = followQuery.Where("follower_id = ?", userID)
		if err := followQuery.Pluck("following_id", &followIDs).Error; err != nil {
			return 0
		}
	}
	for _, id := range followIDs {
		ids[id] = struct{}{}
	}
	if !db.Migrator().HasTable(&model.Subscription{}) || !db.Migrator().HasTable(&model.FeedSource{}) || !db.Migrator().HasTable(&model.Channel{}) {
		return int64(len(ids))
	}
	var subscriptionIDs []uuid.UUID
	query := db.Table("subscriptions").Joins("JOIN feed_sources ON feed_sources.id = subscriptions.feed_source_id").Joins("JOIN channels ON channels.id = feed_sources.source_id").Where("subscriptions.deleted_at IS NULL AND feed_sources.deleted_at IS NULL AND channels.deleted_at IS NULL AND feed_sources.source_type = ?", "internal_channel")
	if followers {
		query = query.Where("channels.user_id = ? AND subscriptions.user_id <> ?", userID, userID)
		if err := query.Pluck("subscriptions.user_id", &subscriptionIDs).Error; err != nil {
			return int64(len(ids))
		}
	} else {
		query = query.Where("subscriptions.user_id = ?", userID)
		if err := query.Pluck("channels.user_id", &subscriptionIDs).Error; err != nil {
			return int64(len(ids))
		}
	}
	for _, id := range subscriptionIDs {
		if id != uuid.Nil {
			ids[id] = struct{}{}
		}
	}
	return int64(len(ids))
}

// SearchUsers returns users matching the query string.
// GET /api/users/search?q=<query>&limit=<n>&scope=mention

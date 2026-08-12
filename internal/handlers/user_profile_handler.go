package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/service"
)

type UserProfileInput struct {
	DisplayName *string `json:"display_name"`
	AvatarURL   *string `json:"avatar_url"`
	Bio         *string `json:"bio"`
	Website     *string `json:"website"`
	Location    *string `json:"location"`
}

// UserSettingsInput represents the request body for updating user settings
type UserSettingsInput struct {
	PrivateProfile *bool `json:"private_profile"`
}

func isUserSettingsDuplicateError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return true
	}

	var pqErr *pq.Error
	if errors.As(err, &pqErr) && string(pqErr.Code) == "23505" {
		return true
	}

	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") &&
		strings.Contains(message, "user_settings")
}

func isMissingTableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") ||
		strings.Contains(message, "does not exist")
}

func loadOrCreateUserSettings(db *gorm.DB, userID uuid.UUID) (model.UserSettings, error) {
	var settings model.UserSettings
	if err := db.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return model.UserSettings{}, err
		}

		if err := db.Create(&model.UserSettings{UserID: userID}).Error; err != nil && !isUserSettingsDuplicateError(err) {
			return model.UserSettings{}, err
		}

		if err := db.Where("user_id = ?", userID).First(&settings).Error; err != nil {
			return model.UserSettings{}, err
		}
	}

	return settings, nil
}

// GetCurrentUser returns the authenticated user's own full profile
// GetCurrentUser godoc
// @Summary 获取当前用户
// @Description 返回当前登录用户的完整资料。
// @Tags users
// @Produce json
// @Success 200 {object} UserResponse
// @Failure 404 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/me [get]
func GetCurrentUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var user model.User
		if err := db.Where("uuid = ?", userID).First(&user).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		user.AvatarURL = service.ResolveUserAvatarURL(db, user)

		c.JSON(http.StatusOK, gin.H{"data": user, "message": "ok"})
	}
}

// GetUserByUsername looks up a user by their username (public)
// GetUserByUsername godoc
// @Summary 按用户名获取用户摘要
// @Description 返回公开的用户摘要信息和关注统计。
// @Tags users
// @Produce json
// @Param username path string true "用户名"
// @Success 200 {object} UserLookupResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/users/by-username/{username} [get]
func GetUserByUsername(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		username := c.Param("username")
		var user model.User

		if err := db.Where("username = ?", username).First(&user).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		avatarURL := service.ResolveUserAvatarURL(db, user)

		var followersCount, followingCount, postsCount int64
		db.Model(&model.Follow{}).Where("following_id = ?", user.UUID).Count(&followersCount)
		db.Model(&model.Follow{}).Where("follower_id = ?", user.UUID).Count(&followingCount)
		db.Model(&model.Post{}).Where("user_id = ? AND status = ?", user.UUID, "published").Count(&postsCount)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"id":              user.ID,
				"uuid":            user.UUID,
				"username":        user.Username,
				"display_name":    user.DisplayName,
				"avatar_url":      avatarURL,
				"bio":             user.Bio,
				"website":         user.Website,
				"role":            user.Role,
				"created_at":      user.CreatedAt,
				"followers_count": followersCount,
				"following_count": followingCount,
				"posts_count":     postsCount,
			},
			"message": "ok",
		})
	}
}

// GetUserProfile returns public profile information for a user
// GetUserProfile godoc
// @Summary 获取用户公开资料
// @Description 通过 UUID 或用户名获取公开资料、统计信息和频道列表。
// @Tags users
// @Produce json
// @Param id path string true "用户 UUID 或用户名"
// @Success 200 {object} UserProfileResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/users/{id}/profile [get]
func GetUserProfile(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var user model.User

		if err := db.Where("uuid = ? OR username = ?", id, id).First(&user).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}
		avatarURL := service.ResolveUserAvatarURL(db, user)

		// Get counts
		var followersCount int64
		var followingCount int64
		var postsCount int64

		db.Model(&model.Follow{}).Where("following_id = ?", user.UUID).Count(&followersCount)
		db.Model(&model.Follow{}).Where("follower_id = ?", user.UUID).Count(&followingCount)
		db.Model(&model.Post{}).Where("user_id = ? AND status = ?", user.UUID, "published").Count(&postsCount)

		// Get user's channels
		var channels []model.Channel
		db.Where("user_id = ?", user.UUID).Find(&channels)

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"user": gin.H{
					"id":           user.ID,
					"uuid":         user.UUID,
					"username":     user.Username,
					"display_name": user.DisplayName,
					"avatar_url":   avatarURL,
					"bio":          user.Bio,
					"website":      user.Website,
					"location":     user.Location,
					"created_at":   user.CreatedAt,
				},
				"stats": gin.H{
					"followers_count": followersCount,
					"following_count": followingCount,
					"posts_count":     postsCount,
				},
				"channels": channels,
			},
			"message": "ok",
		})
	}
}

// UpdateUserProfile updates the authenticated user's profile
// UpdateUserProfile godoc
// @Summary 更新当前用户资料
// @Description 更新显示名、头像、简介、网站和所在地。
// @Tags users
// @Accept json
// @Produce json
// @Param input body UserProfileInput true "用户资料"
// @Success 200 {object} UserResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/me [put]
func UpdateUserProfile(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input UserProfileInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		var user model.User
		if err := db.Where("uuid = ?", userID).First(&user).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
			return
		}

		updates := map[string]interface{}{}
		if input.DisplayName != nil {
			updates["display_name"] = strings.TrimSpace(*input.DisplayName)
		}
		if input.AvatarURL != nil {
			updates["avatar_url"] = strings.TrimSpace(*input.AvatarURL)
		}
		if input.Bio != nil {
			updates["bio"] = strings.TrimSpace(*input.Bio)
		}
		if input.Website != nil {
			updates["website"] = strings.TrimSpace(*input.Website)
		}
		if input.Location != nil {
			updates["location"] = strings.TrimSpace(*input.Location)
		}

		if err := db.Model(&user).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update profile"})
			return
		}
		if err := db.First(&user, "uuid = ?", userID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load updated profile"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": user, "message": "ok"})
	}
}

// GetUserSettings returns the authenticated user's settings
// GetUserSettings godoc
// @Summary 获取当前用户设置
// @Description 返回当前登录用户的隐私设置。
// @Tags users
// @Produce json
// @Success 200 {object} UserSettingsResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/me/settings [get]
func GetUserSettings(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		settings, err := loadOrCreateUserSettings(db, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch settings"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"private_profile": settings.PrivateProfile}, "message": "ok"})
	}
}

// UpdateUserSettings updates the authenticated user's settings
// UpdateUserSettings godoc
// @Summary 更新当前用户设置
// @Description 更新私密资料开关。
// @Tags users
// @Accept json
// @Produce json
// @Param input body UserSettingsInput true "用户设置"
// @Success 200 {object} UserSettingsResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/me/settings [put]
func UpdateUserSettings(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input UserSettingsInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		userIDVal, _ := c.Get("user_id")
		userID := userIDVal.(uuid.UUID)

		settings, err := loadOrCreateUserSettings(db, userID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update settings"})
			return
		}

		updates := map[string]interface{}{}
		if input.PrivateProfile != nil {
			updates["private_profile"] = *input.PrivateProfile
		}

		if len(updates) > 0 {
			if err := db.Model(&settings).Updates(updates).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update settings"})
				return
			}

			settings, err = loadOrCreateUserSettings(db, userID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update settings"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"data": gin.H{"private_profile": settings.PrivateProfile}, "message": "ok"})
	}
}

// FollowUser creates a follow relationship

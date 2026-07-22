package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/httpx"
	"atoman/internal/service"
	"golang.org/x/crypto/bcrypt"
)

// SetupUserRoutes configures user-related routes
func SetupUserRoutes(router *gin.Engine, db *gorm.DB) {
	users := router.Group("/api/v1/users")
	{
		// Public routes — lookup by username (must come before /:id routes)
		users.GET("/search", middleware.OptionalAuthMiddleware(), SearchUsers(db))
		users.GET("/by-username/:username", GetUserByUsername(db))
		users.GET("/blocked", middleware.AuthMiddleware(), ListBlockedUsers(db))
		users.GET("/:id/profile", GetUserProfile(db))
		users.GET("/:id/followers", GetUserFollowers(db))
		users.GET("/:id/following", GetUserFollowing(db))

		// Protected routes
		protected := users.Group("")
		protected.Use(middleware.AuthMiddleware())
		{
			protected.GET("/me", GetCurrentUser(db))
			protected.PUT("/me", UpdateUserProfile(db))
			protected.GET("/me/settings", GetUserSettings(db))
			protected.PUT("/me/settings", UpdateUserSettings(db))
			protected.POST("/me/password", SetPassword(db))
			protected.PUT("/me/password", ChangePassword(db))
			protected.PUT("/me/email", ChangeEmail(db))
			protected.POST("/me/email/send-code", SendEmailChangeCode(db))
			protected.GET("/me/sessions", ListSessions(db))
			protected.DELETE("/me/sessions/:id", RevokeSession(db))
			protected.GET("/me/security-activities", ListSecurityActivities(db))
			protected.POST("/:id/follow", FollowUser(db))
			protected.DELETE("/:id/follow", UnfollowUser(db))
			protected.POST("/:id/block", BlockUser(db))
			protected.DELETE("/:id/block", UnblockUser(db))
		}

		owner := users.Group("")
		owner.Use(middleware.AuthMiddleware())
		owner.Use(RequireOwner())
		{
			owner.GET("/roles", ListUsersForRoleManagement(db))
			owner.PUT("/:id/role", UpdateUserRole(db))
		}
	}
}

func RequireOwner() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authctx.RoleAtLeast(c.GetString("role"), authctx.RoleOwner) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Owner access required"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// UserProfileInput represents the request body for updating user profile
type UserProfileInput struct {
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
	Bio         string `json:"bio"`
	Website     string `json:"website"`
	Location    string `json:"location"`
}

// UserSettingsInput represents the request body for updating user settings
type UserSettingsInput struct {
	PrivateProfile *bool   `json:"private_profile"`
	DMPermission   *string `json:"dm_permission"`
}

type ChangePasswordInput struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
	PasswordConfirm string `json:"password_confirm" binding:"required"`
}

type SetPasswordInput struct {
	Password        string `json:"password" binding:"required"`
	PasswordConfirm string `json:"password_confirm" binding:"required"`
}

type ChangeEmailInput struct {
	Email           string `json:"email" binding:"required,email"`
	Code            string `json:"code" binding:"required,len=6"`
	CurrentPassword string `json:"current_password" binding:"required"`
}

type SendEmailChangeCodeInput struct {
	Email string `json:"email" binding:"required,email"`
}

func SendEmailChangeCode(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input SendEmailChangeCodeInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "validation.invalid_request", "error": "请输入有效邮箱"})
			return
		}
		email := strings.ToLower(strings.TrimSpace(input.Email))
		var count int64
		if err := db.Model(&model.User{}).Where("LOWER(email) = ?", email).Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "发送验证码失败"})
			return
		}
		if count > 0 {
			c.JSON(http.StatusConflict, gin.H{"code": "auth.email_taken", "error": "该邮箱已被使用"})
			return
		}
		if _, err := service.NewEmailServiceWithoutRedis(db).SendVerificationCodeForPurpose(email, service.VerificationPurposeEmailChange); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "发送验证码失败"})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

func ListSessions(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		current, _ := middleware.CurrentAuthSession(c)
		items, err := authsession.New(db).List(userID, "")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "无法加载登录设备"})
			return
		}
		for index := range items {
			items[index].Current = items[index].ID == current.ID
		}
		c.JSON(http.StatusOK, gin.H{"sessions": items})
	}
}

func RevokeSession(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		current, _ := middleware.CurrentAuthSession(c)
		sessionID, err := uuid.Parse(c.Param("id"))
		if err != nil || sessionID == current.ID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "无法退出当前设备"})
			return
		}
		if err := authsession.New(db).RevokeUserSession(userID, sessionID); err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "登录设备不存在"})
			return
		}
		_ = recordSecurityActivity(db, userID, "auth.session_revoked")
		c.Status(http.StatusNoContent)
	}
}

func ListSecurityActivities(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.MustGet("user_id").(uuid.UUID)
		var entries []model.AuditLog
		if err := db.Where("actor_id = ? AND action LIKE ?", userID, "auth.%").Order("created_at DESC").Limit(50).Find(&entries).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "无法加载安全活动"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"activities": entries})
	}
}

func recordSecurityActivity(db *gorm.DB, userID uuid.UUID, action string) error {
	return audit.Record(db, audit.Entry{ActorID: &userID, Action: action, EntityType: "user", EntityID: &userID})
}

func ChangeEmail(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input ChangeEmailInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "validation.invalid_request", "error": "请填写完整的邮箱信息"})
			return
		}
		userID, ok := c.Get("user_id")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": "auth.required", "error": "请先登录"})
			return
		}
		currentSession, ok := middleware.CurrentAuthSession(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": "auth.invalid_token", "error": "登录状态已失效"})
			return
		}
		email := strings.ToLower(strings.TrimSpace(input.Email))
		err := db.Transaction(func(tx *gorm.DB) error {
			var user model.User
			if err := tx.First(&user, "uuid = ?", userID.(uuid.UUID)).Error; err != nil {
				return err
			}
			if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.CurrentPassword)) != nil {
				return bcrypt.ErrMismatchedHashAndPassword
			}
			valid, err := service.NewEmailServiceWithoutRedis(tx).VerifyCodeForPurpose(email, input.Code, service.VerificationPurposeEmailChange)
			if err != nil {
				return err
			}
			if !valid {
				return gorm.ErrRecordNotFound
			}
			var count int64
			if err := tx.Model(&model.User{}).Where("LOWER(email) = ? AND uuid <> ?", email, user.UUID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return gorm.ErrDuplicatedKey
			}
			if err := tx.Model(&user).Updates(map[string]any{"email": email, "auth_version": gorm.Expr("auth_version + 1")}).Error; err != nil {
				return err
			}
			return authsession.New(tx).RevokeUserExcept(user.UUID, currentSession.ID)
		})
		switch {
		case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
			c.JSON(http.StatusUnauthorized, gin.H{"code": "auth.password_mismatch", "error": "当前密码不正确"})
		case errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusBadRequest, gin.H{"code": "auth.email_code_invalid", "error": "验证码无效或已过期"})
		case errors.Is(err, gorm.ErrDuplicatedKey):
			c.JSON(http.StatusConflict, gin.H{"code": "auth.email_taken", "error": "该邮箱已被使用"})
		case err != nil:
			c.JSON(http.StatusInternalServerError, gin.H{"code": "auth.email_change_failed", "error": "修改邮箱失败"})
		default:
			_ = recordSecurityActivity(db, userID.(uuid.UUID), "auth.email_changed")
			c.Status(http.StatusNoContent)
		}
	}
}

func SetPassword(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input SetPasswordInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "validation.invalid_request", "error": "请填写完整的密码信息"})
			return
		}
		if !validPasswordLength(input.Password) {
			c.JSON(http.StatusBadRequest, gin.H{"code": "auth.password_invalid", "error": "密码长度需为 6–72 字节"})
			return
		}
		if input.Password != input.PasswordConfirm {
			c.JSON(http.StatusBadRequest, gin.H{"code": "auth.password_mismatch", "error": "两次输入的密码不一致"})
			return
		}
		userID, ok := c.Get("user_id")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": "auth.required", "error": "请先登录"})
			return
		}
		currentSession, ok := middleware.CurrentAuthSession(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": "auth.invalid_token", "error": "登录状态已失效"})
			return
		}
		hash, err := HashPassword(input.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": "auth.password_change_failed", "error": "设置密码失败"})
			return
		}
		err = db.Transaction(func(tx *gorm.DB) error {
			updated := tx.Model(&model.User{}).Where("uuid = ? AND password = ?", userID.(uuid.UUID), "").Updates(map[string]any{
				"password":     hash,
				"auth_version": gorm.Expr("auth_version + 1"),
			})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return gorm.ErrDuplicatedKey
			}
			return authsession.New(tx).RevokeUserExcept(userID.(uuid.UUID), currentSession.ID)
		})
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			c.JSON(http.StatusConflict, gin.H{"code": "auth.password_already_set", "error": "密码已设置，请使用修改密码"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": "auth.password_change_failed", "error": "设置密码失败"})
			return
		}
		_ = recordSecurityActivity(db, userID.(uuid.UUID), "auth.password_set")
		c.Status(http.StatusNoContent)
	}
}

// ChangePassword godoc
// @Summary 修改当前账号密码
// @Tags users
// @Accept json
// @Param input body ChangePasswordInput true "密码"
// @Security BearerAuth
// @Security CookieAuth
// @Success 204
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Router /api/v1/users/me/password [put]
func ChangePassword(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input ChangePasswordInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "validation.invalid_request", "error": "请填写完整的密码信息"})
			return
		}
		if !validPasswordLength(input.NewPassword) {
			c.JSON(http.StatusBadRequest, gin.H{"code": "auth.password_invalid", "error": "密码长度需为 6–72 字节"})
			return
		}
		if input.NewPassword != input.PasswordConfirm {
			c.JSON(http.StatusBadRequest, gin.H{"code": "auth.password_mismatch", "error": "两次输入的密码不一致"})
			return
		}
		userID, ok := c.Get("user_id")
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": "auth.required", "error": "请先登录"})
			return
		}
		currentSession, ok := middleware.CurrentAuthSession(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"code": "auth.invalid_token", "error": "登录状态已失效"})
			return
		}

		err := db.Transaction(func(tx *gorm.DB) error {
			var user model.User
			if err := tx.First(&user, "uuid = ?", userID.(uuid.UUID)).Error; err != nil {
				return err
			}
			if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.CurrentPassword)); err != nil {
				return bcrypt.ErrMismatchedHashAndPassword
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			if err := tx.Model(&user).Updates(map[string]any{
				"password":     string(hash),
				"auth_version": gorm.Expr("auth_version + 1"),
			}).Error; err != nil {
				return err
			}
			return authsession.New(tx).RevokeUserExcept(user.UUID, currentSession.ID)
		})
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			c.JSON(http.StatusUnauthorized, gin.H{"code": "auth.password_mismatch", "error": "当前密码不正确"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"code": "auth.password_change_failed", "error": "修改密码失败"})
			return
		}
		_ = recordSecurityActivity(db, userID.(uuid.UUID), "auth.password_changed")
		c.Status(http.StatusNoContent)
	}
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
				"avatar_url":      user.AvatarURL,
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
					"avatar_url":   user.AvatarURL,
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

		if err := db.Model(&user).Updates(map[string]interface{}{
			"display_name": input.DisplayName,
			"avatar_url":   input.AvatarURL,
			"bio":          input.Bio,
			"website":      input.Website,
			"location":     input.Location,
		}).Error; err != nil {
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
// @Description 返回当前登录用户的隐私与私信设置。
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

		c.JSON(http.StatusOK, gin.H{"data": settings, "message": "ok"})
	}
}

// UpdateUserSettings updates the authenticated user's settings
// UpdateUserSettings godoc
// @Summary 更新当前用户设置
// @Description 更新私密资料开关和私信权限设置。
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
		if input.DMPermission != nil {
			permission := strings.TrimSpace(*input.DMPermission)
			switch permission {
			case "anyone", "following_only", "one_before_reply":
				updates["dm_permission"] = permission
			default:
				c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid dm_permission"})
				return
			}
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

		c.JSON(http.StatusOK, gin.H{"data": settings, "message": "ok"})
	}
}

// FollowUser creates a follow relationship
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
// SearchUsers godoc
// @Summary 搜索用户
// @Description 按用户名或显示名搜索活跃用户；scope=mention 时要求登录并搜索全部活跃用户。
// @Tags users
// @Produce json
// @Param q query string false "搜索关键字"
// @Param limit query int false "结果数量上限，1-20" default(5)
// @Param scope query string false "搜索范围，例如 mention"
// @Success 200 {object} SearchUsersResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/users/search [get]
func SearchUsers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := strings.TrimSpace(c.Query("q"))
		scope := strings.TrimSpace(c.Query("scope"))
		limit := 5
		if raw := c.Query("limit"); raw != "" {
			l, parseErr := strconv.Atoi(raw)
			if scope == "mention" && (parseErr != nil || l < 1) {
				httpx.Error(c, apperr.BadRequest("user.invalid_limit", "Limit must be a positive integer"))
				return
			}
			if parseErr == nil && l > 0 {
				limit = l
				if limit > 20 {
					limit = 20
				}
			}
		}

		type UserResult struct {
			UUID        string `json:"uuid"`
			Username    string `json:"username"`
			DisplayName string `json:"display_name"`
			AvatarURL   string `json:"avatar_url"`
			Role        string `json:"role"`
		}

		query := db.Model(&model.User{}).
			Select("Users.uuid, Users.username, Users.display_name, Users.avatar_url, Users.role").
			Where("Users.is_active = ?", true)

		if scope == "mention" {
			if current, ok := authctx.Current(c); !ok || current.ID == uuid.Nil {
				httpx.Error(c, apperr.Unauthorized("Authentication is required"))
				return
			}
		}

		if q != "" {
			like := "%" + q + "%"
			query = query.Where("LOWER(Users.username) LIKE LOWER(?) OR LOWER(Users.display_name) LIKE LOWER(?)", like, like)
			query = query.Order(clause.Expr{SQL: "CASE WHEN LOWER(Users.username) LIKE LOWER(?) THEN 0 ELSE 1 END", Vars: []any{q + "%"}, WithoutParentheses: true})
		}
		query = query.Order("LOWER(Users.username) ASC").Order("Users.uuid ASC")

		var results []UserResult
		if err := query.Limit(limit).Scan(&results).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "search failed"})
			return
		}
		if results == nil {
			results = []UserResult{}
		}
		c.JSON(http.StatusOK, gin.H{"data": results})
	}
}

// ListUsersForRoleManagement godoc
// @Summary 获取用户角色列表
// @Description 仅站长可用，按用户名、邮箱、显示名搜索用户并返回当前角色。
// @Tags users
// @Produce json
// @Param q query string false "搜索关键字"
// @Param limit query int false "结果数量上限，1-100" default(20)
// @Success 200 {object} UserRoleListResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/roles [get]
func ListUsersForRoleManagement(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := strings.TrimSpace(c.Query("q"))
		limit := 20
		if l, err := strconv.Atoi(c.Query("limit")); err == nil && l > 0 && l <= 100 {
			limit = l
		}

		query := db.Model(&model.User{}).
			Select("uuid, username, email, display_name, avatar_url, role, created_at").
			Where("is_active = ?", true).
			Order("created_at DESC").
			Limit(limit)

		if q != "" {
			like := "%" + q + "%"
			query = query.Where("LOWER(username) LIKE LOWER(?) OR LOWER(email) LIKE LOWER(?) OR LOWER(display_name) LIKE LOWER(?)", like, like, like)
		}

		var users []struct {
			UUID        uuid.UUID `json:"uuid"`
			Username    string    `json:"username"`
			Email       string    `json:"email"`
			DisplayName string    `json:"display_name"`
			AvatarURL   string    `json:"avatar_url"`
			Role        string    `json:"role"`
			CreatedAt   time.Time `json:"created_at"`
		}
		if err := query.Scan(&users).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to search users"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"data": users})
	}
}

// UpdateUserRole godoc
// @Summary 更新用户角色
// @Description 仅站长可用，可将指定用户设置为 user 或 admin，站长账号本身不可降级。
// @Tags users
// @Accept json
// @Produce json
// @Param id path string true "用户 UUID"
// @Param input body UpdateUserRoleInput true "角色更新请求"
// @Success 200 {object} UserRoleResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/users/{id}/role [put]
func UpdateUserRole(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		targetID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user UUID"})
			return
		}

		var input struct {
			Role string `json:"role"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role payload"})
			return
		}
		input.Role = strings.TrimSpace(input.Role)
		if input.Role != authctx.RoleUser && input.Role != authctx.RoleAdmin {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Role must be user or admin"})
			return
		}

		currentUserID, _ := c.Get("user_id")
		ownerID, ok := currentUserID.(uuid.UUID)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			return
		}

		var user model.User
		if err := db.Where("uuid = ?", targetID).First(&user).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
			return
		}
		if user.Role == authctx.RoleOwner {
			c.JSON(http.StatusForbidden, gin.H{"error": "Owner role cannot be changed here"})
			return
		}
		if user.UUID == ownerID {
			c.JSON(http.StatusForbidden, gin.H{"error": "Owner role cannot be changed here"})
			return
		}

		if err := db.Model(&user).Update("role", input.Role).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update user role"})
			return
		}
		user.Role = input.Role

		c.JSON(http.StatusOK, gin.H{
			"data": gin.H{
				"uuid":         user.UUID,
				"username":     user.Username,
				"email":        user.Email,
				"display_name": user.DisplayName,
				"avatar_url":   user.AvatarURL,
				"role":         user.Role,
				"created_at":   user.CreatedAt,
			},
		})
	}
}

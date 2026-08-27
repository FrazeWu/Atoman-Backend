package handlers

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/ratelimit"
	"atoman/internal/platform/requestmeta"
	"atoman/internal/service"
	"golang.org/x/crypto/bcrypt"
)

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
	emailLimiter := ratelimit.New()
	ipLimiter := ratelimit.New()
	return func(c *gin.Context) {
		var input SendEmailChangeCodeInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": "validation.invalid_request", "error": "请输入有效邮箱"})
			return
		}
		email := strings.ToLower(strings.TrimSpace(input.Email))
		requestInfo := requestmeta.FromGin(c)
		if !allowAuthRequest(c, ipLimiter, "email-change-send:ip:"+requestIPAddress(c, requestInfo), 10, time.Hour) ||
			!allowAuthRequest(c, emailLimiter, "email-change-send:email:"+email, 3, 15*time.Minute) {
			return
		}
		var count int64
		if err := db.Unscoped().Model(&model.User{}).Where("LOWER(email) = ?", email).Count(&count).Error; err != nil {
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
			if err := tx.Unscoped().Model(&model.User{}).Where("LOWER(email) = ? AND uuid <> ?", email, user.UUID).Count(&count).Error; err != nil {
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

package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/service"
)

const (
	authTokenCookieName = "atoman_token"
	authTokenTTL        = 30 * 24 * time.Hour
)

type authErrorCode string

const (
	authRequired              authErrorCode = "auth.required"
	authInvalidToken          authErrorCode = "auth.invalid_token"
	authInvalidClaims         authErrorCode = "auth.invalid_claims"
	authUserNotFound          authErrorCode = "auth.user_not_found"
	authAccountNotFound       authErrorCode = "auth.account_not_found"
	authPasswordNotSet        authErrorCode = "auth.password_not_set"
	authPasswordMismatch      authErrorCode = "auth.password_mismatch"
	authTokenGenerationFailed authErrorCode = "auth.token_generation_failed"
)

func authError(c *gin.Context, status int, code authErrorCode, message string) {
	c.JSON(status, gin.H{"code": string(code), "error": message})
}

func clearSessionAndAuthError(c *gin.Context, code authErrorCode, message string) {
	clearAuthTokenCookie(c)
	authError(c, http.StatusUnauthorized, code, message)
}

func HashPassword(password string) (string, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hashedPassword), nil
}

func generateAuthToken(user model.User) (string, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return "", fmt.Errorf("JWT_SECRET is not configured")
	}

	role := user.Role
	if role == "" {
		role = "user"
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":      user.UUID.String(),
		"username":     user.Username,
		"role":         role,
		"auth_version": user.AuthVersion,
		"exp":          time.Now().Add(authTokenTTL).Unix(),
	})

	return token.SignedString([]byte(secret))
}

func setAuthTokenCookie(c *gin.Context, tokenString string) {
	domain := os.Getenv("AUTH_COOKIE_DOMAIN")
	secure := os.Getenv("ENV") == "production" || domain != ""
	cookie := http.Cookie{
		Name:     authTokenCookieName,
		Value:    tokenString,
		Path:     "/",
		Domain:   domain,
		MaxAge:   int(authTokenTTL.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(c.Writer, &cookie)
}

func clearAuthTokenCookie(c *gin.Context) {
	domain := os.Getenv("AUTH_COOKIE_DOMAIN")
	secure := os.Getenv("ENV") == "production" || domain != ""
	cookie := http.Cookie{
		Name:     authTokenCookieName,
		Value:    "",
		Path:     "/",
		Domain:   domain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(c.Writer, &cookie)
}

func userAuthResponse(user model.User, tokenString string) gin.H {
	return gin.H{
		"token": tokenString,
		"user": gin.H{
			"uuid":                    user.UUID,
			"id":                      user.ID,
			"username":                user.Username,
			"email":                   user.Email,
			"role":                    user.Role,
			"display_name":            user.DisplayName,
			"avatar_url":              user.AvatarURL,
			"is_active":               user.IsActive,
			"onboarding_completed_at": user.OnboardingCompletedAt,
		},
	}
}

// RegisterInput represents user registration request
type RegisterInput struct {
	Username         string `json:"username" binding:"required"`
	Email            string `json:"email" binding:"required,email"`
	Password         string `json:"password" binding:"required,min=6"`
	PasswordConfirm  string `json:"password_confirm" binding:"required,eqfield=Password"`
	VerificationCode string `json:"verification_code" binding:"required,len=6"`
}

// LoginInput represents user login request
type LoginInput struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// SendVerificationInput represents email verification code request
type SendVerificationInput struct {
	Email          string `json:"email" binding:"required,email"`
	TurnstileToken string `json:"turnstile_token"`
}

// VerifyEmailInput represents email verification request
type VerifyEmailInput struct {
	Email string `json:"email" binding:"required,email"`
	Code  string `json:"code" binding:"required,len=6"`
}

type CheckEmailInput struct {
	Email string `json:"email" binding:"required,email"`
}

type CheckUsernameInput struct {
	Username string `json:"username" binding:"required"`
}

type PasswordResetSendCodeInput struct {
	Email          string `json:"email" binding:"required,email"`
	TurnstileToken string `json:"turnstile_token"`
}

type PasswordResetInput struct {
	Email           string `json:"email" binding:"required,email"`
	Code            string `json:"code" binding:"required,len=6"`
	Password        string `json:"password" binding:"required,min=6"`
	PasswordConfirm string `json:"password_confirm" binding:"required,eqfield=Password"`
}

// SetupAuthRoutes configures authentication routes
func SetupAuthRoutes(router *gin.Engine, db *gorm.DB, emailService *service.EmailService) {
	middleware.SetAuthDB(db)

	auth := router.Group("/api/v1/auth")
	{
		auth.POST("/register", RegisterHandler(db, emailService))
		auth.POST("/login", LoginHandler(db))
		auth.POST("/logout", LogoutHandler())
		auth.GET("/session", SessionHandler(db))
		auth.POST("/check-email", CheckEmailHandler(db))
		auth.POST("/check-username", CheckUsernameHandler(db))
		auth.POST("/send-verification", SendVerificationHandler(emailService))
		auth.POST("/verify-email", VerifyEmailHandler(emailService))
		auth.POST("/password-reset/send-code", PasswordResetSendCodeHandler(db, emailService))
		auth.POST("/password-reset", PasswordResetHandler(db))
	}
	RegisterOAuthRoutes(auth, service.NewOAuthService(db, configuredOAuthRegistry()), configuredOAuthFrontendURL())
}

// RegisterHandler godoc
// @Summary 注册新用户
// @Description 验证邮箱验证码后创建账号并返回登录态。
// @Tags auth
// @Accept json
// @Produce json
// @Param input body RegisterInput true "注册请求"
// @Success 201 {object} AuthSuccessResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/auth/register [post]
func RegisterHandler(db *gorm.DB, emailService *service.EmailService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input RegisterInput

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		input.Username = strings.ToLower(strings.TrimSpace(input.Username))
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		if err := service.NewSiteNamespaceService(db).ValidateUsernameAvailable(c.Request.Context(), input.Username); err != nil {
			if errors.Is(err, service.ErrSiteHandleReserved) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Site handle is reserved"})
				return
			}
			if errors.Is(err, service.ErrSiteHandleTaken) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Site handle is already in use"})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid site handle"})
			return
		}

		// Check if user exists
		var existingUser model.User
		if err := db.Where("LOWER(username) = ? OR LOWER(email) = ?", input.Username, input.Email).First(&existingUser).Error; err == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "User already exists"})
			return
		}

		// Verify email verification code after availability checks so duplicate
		// submissions do not consume a valid code.
		valid, err := emailService.VerifyCode(input.Email, input.VerificationCode)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify email code"})
			return
		}
		if !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or expired verification code"})
			return
		}

		// Hash password
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
			return
		}

		user := model.User{
			Username: input.Username,
			Email:    input.Email,
			Password: string(hashedPassword),
			Role:     "user",
		}

		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			if err := tx.Create(&model.UserSettings{UserID: user.UUID}).Error; err != nil {
				return err
			}
			if err := service.NewUserBootstrapService(tx).EnsureDefaults(user.UUID, user.Username); err != nil {
				return err
			}
			return nil
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create default channel"})
			return
		}

		tokenString, err := generateAuthToken(user)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate token"})
			return
		}

		setAuthTokenCookie(c, tokenString)

		c.JSON(http.StatusCreated, userAuthResponse(user, tokenString))
	}
}

// LogoutHandler godoc
// @Summary 退出登录
// @Description 清除认证 Cookie。
// @Tags auth
// @Produce json
// @Success 204
// @Router /api/v1/auth/logout [post]
func LogoutHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		clearAuthTokenCookie(c)
		c.Status(http.StatusNoContent)
	}
}

// SessionHandler godoc
// @Summary 获取当前会话
// @Description 读取当前登录用户信息。
// @Tags auth
// @Produce json
// @Success 200 {object} AuthSuccessResponse
// @Failure 401 {object} ErrorResponse
// @Security CookieAuth
// @Router /api/v1/auth/session [get]
func SessionHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie(authTokenCookieName)
		if err != nil {
			c.Status(http.StatusNoContent)
			return
		}

		token, err := jwt.Parse(cookie, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(os.Getenv("JWT_SECRET")), nil
		})
		if err != nil || !token.Valid {
			clearSessionAndAuthError(c, authInvalidToken, "登录状态已失效，请重新登录")
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			clearSessionAndAuthError(c, authInvalidClaims, "登录信息异常，请重新登录")
			return
		}
		userID, ok := claims["user_id"].(string)
		if !ok {
			clearSessionAndAuthError(c, authInvalidClaims, "登录信息异常，请重新登录")
			return
		}
		var user model.User
		if err := db.Where("uuid = ? AND is_active = ?", userID, true).First(&user).Error; err != nil {
			clearSessionAndAuthError(c, authUserNotFound, "账号不存在或已被移除，请重新登录")
			return
		}
		authVersion, validVersion := middleware.ClaimsAuthVersion(claims)
		if !validVersion || authVersion != user.AuthVersion {
			clearSessionAndAuthError(c, authInvalidToken, "登录状态已失效，请重新登录")
			return
		}
		if user.Role == "" {
			user.Role = "user"
		}
		c.JSON(http.StatusOK, userAuthResponse(user, cookie))
	}
}

// LoginHandler godoc
// @Summary 用户登录
// @Description 使用用户名或邮箱登录并返回登录态。
// @Tags auth
// @Accept json
// @Produce json
// @Param input body LoginInput true "登录请求"
// @Success 200 {object} AuthSuccessResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/auth/login [post]
func LoginHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input LoginInput

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		normalizedLogin := strings.ToLower(strings.TrimSpace(input.Username))

		var user model.User
		if err := db.Where("(LOWER(username) = ? OR LOWER(email) = ?) AND is_active = ?", normalizedLogin, normalizedLogin, true).First(&user).Error; err != nil {
			authError(c, http.StatusUnauthorized, authAccountNotFound, "账号不存在")
			return
		}
		if user.Role == "" {
			user.Role = "user"
		}
		if user.Password == "" {
			authError(c, http.StatusUnauthorized, authPasswordNotSet, "请使用第三方账号登录")
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
			authError(c, http.StatusUnauthorized, authPasswordMismatch, "密码不正确")
			return
		}

		tokenString, err := generateAuthToken(user)
		if err != nil {
			authError(c, http.StatusInternalServerError, authTokenGenerationFailed, "登录服务暂时不可用，请稍后重试")
			return
		}

		setAuthTokenCookie(c, tokenString)

		c.JSON(http.StatusOK, userAuthResponse(user, tokenString))
	}
}

func CheckEmailHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input CheckEmailInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		email := strings.ToLower(strings.TrimSpace(input.Email))
		var count int64
		if err := db.Model(&model.User{}).Where("LOWER(email) = ?", email).Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check email"})
			return
		}

		if count > 0 {
			c.JSON(http.StatusOK, gin.H{"available": false, "reason": "registered"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"available": true})
	}
}

func CheckUsernameHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input CheckUsernameInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		username := strings.TrimSpace(input.Username)
		if err := service.NewSiteNamespaceService(db).ValidateUsernameAvailable(c.Request.Context(), username); err != nil {
			if errors.Is(err, service.ErrSiteHandleReserved) {
				c.JSON(http.StatusOK, gin.H{"available": false, "reason": "reserved"})
				return
			}
			if errors.Is(err, service.ErrSiteHandleTaken) {
				c.JSON(http.StatusOK, gin.H{"available": false, "reason": "taken"})
				return
			}
			if errors.Is(err, service.ErrSiteHandleInvalid) {
				c.JSON(http.StatusOK, gin.H{"available": false, "reason": "invalid"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check username"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"available": true})
	}
}

// SendVerificationHandler godoc
// @Summary 发送邮箱验证码
// @Description 向指定邮箱发送 6 位验证码。
// @Tags auth
// @Accept json
// @Produce json
// @Param input body SendVerificationInput true "验证码请求"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/auth/send-verification [post]
func SendVerificationHandler(emailService *service.EmailService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input SendVerificationInput

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if err := verifyTurnstileToken(input.TurnstileToken, c.ClientIP()); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}

		// Send verification code
		_, err := emailService.SendVerificationCode(input.Email)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to send verification code", "details": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Verification code sent"})
	}
}

// VerifyEmailHandler godoc
// @Summary 校验邮箱验证码
// @Description 校验邮箱与验证码是否匹配。
// @Tags auth
// @Accept json
// @Produce json
// @Param input body VerifyEmailInput true "邮箱验证请求"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/auth/verify-email [post]
func VerifyEmailHandler(emailService *service.EmailService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input VerifyEmailInput

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		// Verify the code
		valid, err := emailService.VerifyCode(input.Email, input.Code)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to verify code"})
			return
		}

		if !valid {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid or expired verification code"})
			return
		}

		c.JSON(http.StatusOK, gin.H{"message": "Email verified successfully"})
	}
}

// PasswordResetSendCodeHandler godoc
// @Summary 发送密码重置验证码
// @Description 若邮箱对应有效账号，则发送密码重置验证码；响应不暴露账号是否存在。
// @Tags auth
// @Accept json
// @Produce json
// @Param input body PasswordResetSendCodeInput true "密码重置验证码请求"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/auth/password-reset/send-code [post]
func PasswordResetSendCodeHandler(db *gorm.DB, emailService *service.EmailService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input PasswordResetSendCodeInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := verifyTurnstileToken(input.TurnstileToken, c.ClientIP()); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}

		email := strings.ToLower(strings.TrimSpace(input.Email))
		var count int64
		if err := db.Model(&model.User{}).Where("LOWER(email) = ? AND is_active = ?", email, true).Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "发送验证码失败"})
			return
		}
		if count > 0 {
			if _, err := emailService.SendVerificationCodeForPurpose(email, service.VerificationPurposePasswordReset); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "发送验证码失败"})
				return
			}
		}

		c.JSON(http.StatusOK, gin.H{"message": "如果该邮箱已注册，验证码将发送至邮箱"})
	}
}

// PasswordResetHandler godoc
// @Summary 重置密码
// @Description 使用邮箱验证码设置新密码，并使该账号的既有登录全部失效。
// @Tags auth
// @Accept json
// @Produce json
// @Param input body PasswordResetInput true "密码重置请求"
// @Success 200 {object} MessageResponse
// @Failure 400 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/auth/password-reset [post]
func PasswordResetHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input PasswordResetInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		email := strings.ToLower(strings.TrimSpace(input.Email))
		hashedPassword, err := HashPassword(input.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "重置密码失败"})
			return
		}

		err = db.Transaction(func(tx *gorm.DB) error {
			now := time.Now().UTC()
			consumed := tx.Model(&model.EmailVerificationCode{}).
				Where("email = ? AND code = ? AND purpose = ? AND used = ? AND expires_at > ?", email, input.Code, service.VerificationPurposePasswordReset, false, now).
				Update("used", true)
			if consumed.Error != nil {
				return consumed.Error
			}
			if consumed.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}

			updated := tx.Model(&model.User{}).
				Where("LOWER(email) = ? AND is_active = ?", email, true).
				Updates(map[string]any{
					"password":     hashedPassword,
					"auth_version": gorm.Expr("auth_version + 1"),
				})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
			return nil
		})
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "验证码无效或已过期"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "重置密码失败"})
			return
		}

		clearAuthTokenCookie(c)
		c.JSON(http.StatusOK, gin.H{"message": "密码已重置，请重新登录"})
	}
}

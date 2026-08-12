package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authlogin"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/ratelimit"
	"atoman/internal/platform/requestmeta"
	"atoman/internal/service"
)

const authTokenCookieName = middleware.AuthSessionCookieName

type authErrorCode string

const (
	authRequired              authErrorCode = "auth.required"
	authInvalidToken          authErrorCode = "auth.invalid_token"
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

func validPasswordLength(password string) bool {
	length := len([]byte(password))
	return length >= 6 && length <= 72
}

func clearAuthTokenCookie(c *gin.Context) {
	secure := os.Getenv("ENV") == "production"
	cookie := http.Cookie{
		Name:     authTokenCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(c.Writer, &cookie)
}

func userAuthResponse(db *gorm.DB, user model.User, csrfToken string) gin.H {
	user.AvatarURL = service.ResolveUserAvatarURL(db, user)
	return gin.H{
		"csrf_token": csrfToken,
		"user": gin.H{
			"uuid":                    user.UUID,
			"id":                      user.ID,
			"username":                user.Username,
			"email":                   user.Email,
			"has_password":            user.Password != "",
			"role":                    user.Role,
			"display_name":            user.DisplayName,
			"avatar_url":              user.AvatarURL,
			"is_active":               user.IsActive,
			"onboarding_completed_at": user.OnboardingCompletedAt,
		},
	}
}

func apiAuthResponse(db *gorm.DB, user model.User, credentials authsession.Credentials) gin.H {
	return gin.H{
		"token":      credentials.Token,
		"expires_at": credentials.ExpiresAt,
		"user":       userAuthResponse(db, user, "")["user"],
	}
}

func setAuthSessionCookie(c *gin.Context, credentials authsession.Credentials) {
	secure := os.Getenv("ENV") == "production"
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     middleware.AuthSessionCookieName,
		Value:    credentials.Token,
		Path:     "/",
		MaxAge:   int(time.Until(credentials.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func authSessionMetadata(info requestmeta.Info) authsession.Metadata {
	return authsession.Metadata{UserAgent: info.UserAgent, IPAddress: info.IPAddress, IPPrefix: info.IPPrefix}
}

func recordLoginFailure(db *gorm.DB, userID uuid.UUID, method, failureCode string, info requestmeta.Info) {
	if err := authlogin.Record(db, userID, nil, method, model.LoginResultFailed, failureCode, info); err != nil {
		log.Printf("[Auth] failed to record login failure: %v", err)
	}
}

func revokeReplacedWebSession(c *gin.Context, db *gorm.DB, newToken string) {
	existing := webSessionCookie(c)
	if existing != "" && existing != newToken {
		_ = authsession.New(db).Revoke(existing)
	}
}

func webSessionCookie(c *gin.Context) string {
	existing, err := c.Cookie(authTokenCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(existing)
}

func requestIPAddress(c *gin.Context, info requestmeta.Info) string {
	if info.IPAddress != "" {
		return info.IPAddress
	}
	return c.ClientIP()
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
		auth.POST("/register", middleware.TrustedOriginMiddleware(), RegisterHandler(db, emailService))
		auth.POST("/login", middleware.TrustedOriginMiddleware(), LoginHandler(db))
		auth.POST("/token", TokenLoginHandler(db))
		auth.POST("/logout", middleware.AuthMiddleware(), LogoutHandler(db))
		auth.GET("/session", SessionHandler(db))
		auth.POST("/check-email", middleware.TrustedOriginMiddleware(), CheckEmailHandler(db))
		auth.POST("/check-username", middleware.TrustedOriginMiddleware(), CheckUsernameHandler(db))
		auth.POST("/send-verification", middleware.TrustedOriginMiddleware(), SendVerificationHandler(emailService))
		auth.POST("/verify-email", middleware.TrustedOriginMiddleware(), VerifyEmailHandler(emailService))
		auth.POST("/password-reset/send-code", middleware.TrustedOriginMiddleware(), PasswordResetSendCodeHandler(db, emailService))
		auth.POST("/password-reset", middleware.TrustedOriginMiddleware(), PasswordResetHandler(db))
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
	emailLimiter := ratelimit.New()
	ipLimiter := ratelimit.New()
	return func(c *gin.Context) {
		var input RegisterInput
		requestInfo := requestmeta.FromGin(c)

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		input.Username = strings.ToLower(strings.TrimSpace(input.Username))
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		if !validPasswordLength(input.Password) {
			c.JSON(http.StatusBadRequest, gin.H{"code": "auth.password_invalid", "error": "密码长度需为 6–72 字节"})
			return
		}
		if !allowAuthRequest(c, ipLimiter, "register:ip:"+requestIPAddress(c, requestInfo), 10, time.Hour) ||
			!allowAuthRequest(c, emailLimiter, "register:email:"+input.Email, 5, 15*time.Minute) {
			return
		}
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
		if err := db.Unscoped().Where("LOWER(username) = ? OR LOWER(email) = ?", input.Username, input.Email).First(&existingUser).Error; err == nil {
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

		var credentials authsession.Credentials
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
			created, err := authsession.New(tx).Create(user.UUID, authsession.KindWeb, authSessionMetadata(requestInfo))
			if err != nil {
				return err
			}
			credentials = created
			return authlogin.Record(tx, user.UUID, &credentials.SessionID, "register", model.LoginResultSucceeded, "", requestInfo)
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create default channel"})
			return
		}

		revokeReplacedWebSession(c, db, credentials.Token)
		setAuthSessionCookie(c, credentials)
		c.JSON(http.StatusCreated, userAuthResponse(db, user, credentials.CSRFToken))
	}
}

// LogoutHandler godoc
// @Summary 退出登录
// @Description 清除认证 Cookie。
// @Tags auth
// @Produce json
// @Success 204
// @Router /api/v1/auth/logout [post]
func LogoutHandler(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, ok := middleware.CurrentAuthSession(c)
		if !ok {
			authError(c, http.StatusUnauthorized, authRequired, "请先登录")
			return
		}
		if err := authsession.New(db).RevokeID(session.ID); err != nil {
			authError(c, http.StatusInternalServerError, authInvalidToken, "退出失败，请稍后重试")
			return
		}
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
		cookie, err := c.Cookie(middleware.AuthSessionCookieName)
		if err != nil {
			c.Status(http.StatusNoContent)
			return
		}
		resolved, err := authsession.New(db).Authenticate(cookie, authsession.KindWeb)
		if err != nil {
			clearSessionAndAuthError(c, authInvalidToken, "登录状态已失效，请重新登录")
			return
		}
		csrfToken, err := authsession.New(db).RotateCSRF(resolved.Session.ID)
		if err != nil {
			clearSessionAndAuthError(c, authInvalidToken, "登录状态已失效，请重新登录")
			return
		}
		c.JSON(http.StatusOK, userAuthResponse(db, resolved.User, csrfToken))
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
	accountLimiter := ratelimit.New()
	ipLimiter := ratelimit.New()
	return func(c *gin.Context) {
		var input LoginInput
		requestInfo := requestmeta.FromGin(c)

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		normalizedLogin := strings.ToLower(strings.TrimSpace(input.Username))
		if !allowAuthRequest(c, ipLimiter, "login:ip:"+requestIPAddress(c, requestInfo), 20, 15*time.Minute) ||
			!allowAuthRequest(c, accountLimiter, "login:account:"+normalizedLogin, 10, 15*time.Minute) {
			return
		}

		var user model.User
		if err := db.Where("LOWER(username) = ? OR LOWER(email) = ?", normalizedLogin, normalizedLogin).First(&user).Error; err != nil {
			authError(c, http.StatusUnauthorized, authAccountNotFound, "账号不存在")
			return
		}
		if !user.IsActive {
			recordLoginFailure(db, user.UUID, "password", "account_inactive", requestInfo)
			authError(c, http.StatusUnauthorized, authAccountNotFound, "账号不存在")
			return
		}
		if user.Role == "" {
			user.Role = "user"
		}
		if user.Password == "" {
			recordLoginFailure(db, user.UUID, "password", string(authPasswordNotSet), requestInfo)
			authError(c, http.StatusUnauthorized, authPasswordNotSet, "请使用第三方账号登录")
			return
		}

		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
			recordLoginFailure(db, user.UUID, "password", string(authPasswordMismatch), requestInfo)
			authError(c, http.StatusUnauthorized, authPasswordMismatch, "密码不正确")
			return
		}

		var credentials authsession.Credentials
		err := db.Transaction(func(tx *gorm.DB) error {
			created, err := authsession.New(tx).CreateAtVersion(user.UUID, authsession.KindWeb, user.AuthVersion, authSessionMetadata(requestInfo))
			if err != nil {
				return err
			}
			credentials = created
			return authlogin.Record(tx, user.UUID, &credentials.SessionID, "password", model.LoginResultSucceeded, "", requestInfo)
		})
		if err != nil {
			authError(c, http.StatusInternalServerError, authTokenGenerationFailed, "登录服务暂时不可用，请稍后重试")
			return
		}

		revokeReplacedWebSession(c, db, credentials.Token)
		setAuthSessionCookie(c, credentials)

		c.JSON(http.StatusOK, userAuthResponse(db, user, credentials.CSRFToken))
	}
}

func allowAuthRequest(c *gin.Context, limiter *ratelimit.Limiter, key string, limit int, window time.Duration) bool {
	allowed, retryAfter := limiter.Allow(key, limit, window)
	if allowed {
		return true
	}
	seconds := int(retryAfter.Seconds())
	if retryAfter > time.Duration(seconds)*time.Second {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	c.Header("Retry-After", fmt.Sprintf("%d", seconds))
	c.JSON(http.StatusTooManyRequests, gin.H{
		"code":  "auth.rate_limited",
		"error": "尝试次数过多，请稍后重试",
	})
	return false
}

// TokenLoginHandler godoc
// @Summary 获取 API Token
// @Description 供 iOS 和自动化客户端使用用户名或邮箱换取可撤销 Bearer Token。
// @Tags auth
// @Accept json
// @Produce json
// @Param input body LoginInput true "登录请求"
// @Success 200 {object} APIAuthSuccessResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 429 {object} ErrorResponse
// @Router /api/v1/auth/token [post]
func TokenLoginHandler(db *gorm.DB) gin.HandlerFunc {
	accountLimiter := ratelimit.New()
	ipLimiter := ratelimit.New()
	return func(c *gin.Context) {
		var input LoginInput
		requestInfo := requestmeta.FromGin(c)
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		normalizedLogin := strings.ToLower(strings.TrimSpace(input.Username))
		if !allowAuthRequest(c, ipLimiter, "token:ip:"+requestIPAddress(c, requestInfo), 20, 15*time.Minute) ||
			!allowAuthRequest(c, accountLimiter, "token:account:"+normalizedLogin, 10, 15*time.Minute) {
			return
		}
		var user model.User
		if err := db.Where("LOWER(username) = ? OR LOWER(email) = ?", normalizedLogin, normalizedLogin).First(&user).Error; err != nil {
			authError(c, http.StatusUnauthorized, authAccountNotFound, "账号不存在")
			return
		}
		if !user.IsActive {
			recordLoginFailure(db, user.UUID, "api_token", "account_inactive", requestInfo)
			authError(c, http.StatusUnauthorized, authAccountNotFound, "账号不存在")
			return
		}
		if user.Role == "" {
			user.Role = "user"
		}
		if user.Password == "" {
			recordLoginFailure(db, user.UUID, "api_token", string(authPasswordNotSet), requestInfo)
			authError(c, http.StatusUnauthorized, authPasswordNotSet, "请使用第三方账号登录")
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Password)); err != nil {
			recordLoginFailure(db, user.UUID, "api_token", string(authPasswordMismatch), requestInfo)
			authError(c, http.StatusUnauthorized, authPasswordMismatch, "密码不正确")
			return
		}

		var credentials authsession.Credentials
		err := db.Transaction(func(tx *gorm.DB) error {
			created, err := authsession.New(tx).CreateAtVersion(user.UUID, authsession.KindAPI, user.AuthVersion, authSessionMetadata(requestInfo))
			if err != nil {
				return err
			}
			credentials = created
			return authlogin.Record(tx, user.UUID, &credentials.SessionID, "api_token", model.LoginResultSucceeded, "", requestInfo)
		})
		if err != nil {
			authError(c, http.StatusInternalServerError, authTokenGenerationFailed, "登录服务暂时不可用，请稍后重试")
			return
		}
		c.JSON(http.StatusOK, apiAuthResponse(db, user, credentials))
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
		if err := db.Unscoped().Model(&model.User{}).Where("LOWER(email) = ?", email).Count(&count).Error; err != nil {
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
	emailLimiter := ratelimit.New()
	ipLimiter := ratelimit.New()
	return func(c *gin.Context) {
		var input SendVerificationInput

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		email := strings.ToLower(strings.TrimSpace(input.Email))
		if !allowAuthRequest(c, ipLimiter, "verification-send:ip:"+c.ClientIP(), 10, time.Hour) ||
			!allowAuthRequest(c, emailLimiter, "verification-send:email:"+email, 3, 15*time.Minute) {
			return
		}
		if err := verifyTurnstileToken(input.TurnstileToken, c.ClientIP()); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}

		// Send verification code
		_, err := emailService.SendVerificationCode(email)
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
	emailLimiter := ratelimit.New()
	ipLimiter := ratelimit.New()
	return func(c *gin.Context) {
		var input VerifyEmailInput

		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		email := strings.ToLower(strings.TrimSpace(input.Email))
		if !allowAuthRequest(c, ipLimiter, "verification-check:ip:"+c.ClientIP(), 30, 15*time.Minute) ||
			!allowAuthRequest(c, emailLimiter, "verification-check:email:"+email, 10, 15*time.Minute) {
			return
		}
		valid, err := emailService.VerifyCode(email, input.Code)
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
	emailLimiter := ratelimit.New()
	ipLimiter := ratelimit.New()
	return func(c *gin.Context) {
		var input PasswordResetSendCodeInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		email := strings.ToLower(strings.TrimSpace(input.Email))
		if !allowAuthRequest(c, ipLimiter, "password-reset-send:ip:"+c.ClientIP(), 10, time.Hour) ||
			!allowAuthRequest(c, emailLimiter, "password-reset-send:email:"+email, 3, 15*time.Minute) {
			return
		}
		if err := verifyTurnstileToken(input.TurnstileToken, c.ClientIP()); err != nil {
			c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
			return
		}
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
	emailLimiter := ratelimit.New()
	ipLimiter := ratelimit.New()
	return func(c *gin.Context) {
		var input PasswordResetInput
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		email := strings.ToLower(strings.TrimSpace(input.Email))
		if !allowAuthRequest(c, ipLimiter, "password-reset:ip:"+c.ClientIP(), 20, 15*time.Minute) ||
			!allowAuthRequest(c, emailLimiter, "password-reset:email:"+email, 5, 15*time.Minute) {
			return
		}
		if !validPasswordLength(input.Password) {
			c.JSON(http.StatusBadRequest, gin.H{"code": "auth.password_invalid", "error": "密码长度需为 6–72 字节"})
			return
		}
		hashedPassword, err := HashPassword(input.Password)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "重置密码失败"})
			return
		}

		err = db.Transaction(func(tx *gorm.DB) error {
			valid, err := service.NewEmailServiceWithoutRedis(tx).VerifyCodeForPurpose(email, input.Code, service.VerificationPurposePasswordReset)
			if err != nil {
				return err
			}
			if !valid {
				return gorm.ErrRecordNotFound
			}
			var user model.User
			if err := tx.Where("LOWER(email) = ? AND is_active = ?", email, true).First(&user).Error; err != nil {
				return err
			}
			updated := tx.Model(&model.User{}).Where("uuid = ?", user.UUID).
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
			return authsession.New(tx).RevokeUser(user.UUID)
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

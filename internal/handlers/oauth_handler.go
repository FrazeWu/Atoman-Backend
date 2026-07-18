package handlers

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/oauthprovider"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
)

type OAuthHandler struct {
	service     *service.OAuthService
	frontendURL string
}

const oauthFlowCookieName = "atoman_oauth_flow"

func configuredOAuthRegistry() *oauthprovider.Registry {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("BASE_URL")), "/")
	if baseURL == "" {
		return oauthprovider.NewRegistry()
	}
	providers := make([]oauthprovider.Provider, 0, 4)
	add := func(provider oauthprovider.Provider, err error) {
		if err != nil {
			log.Printf("[OAuth] provider disabled: %v", err)
			return
		}
		providers = append(providers, provider)
	}

	googleID := os.Getenv("GOOGLE_OAUTH_CLIENT_ID")
	googleSecret := os.Getenv("GOOGLE_OAUTH_CLIENT_SECRET")
	if googleID != "" || googleSecret != "" {
		add(oauthprovider.NewGoogleProvider(oauthprovider.GoogleConfig{
			ClientID: googleID, ClientSecret: googleSecret,
			RedirectURL: baseURL + "/api/v1/auth/oauth/google/callback",
		}))
	}
	githubID := os.Getenv("GITHUB_OAUTH_CLIENT_ID")
	githubSecret := os.Getenv("GITHUB_OAUTH_CLIENT_SECRET")
	if githubID != "" || githubSecret != "" {
		add(oauthprovider.NewGitHubProvider(oauthprovider.GitHubConfig{
			ClientID: githubID, ClientSecret: githubSecret,
			RedirectURL: baseURL + "/api/v1/auth/oauth/github/callback",
		}))
	}
	microsoftID := os.Getenv("MICROSOFT_OAUTH_CLIENT_ID")
	microsoftSecret := os.Getenv("MICROSOFT_OAUTH_CLIENT_SECRET")
	if microsoftID != "" || microsoftSecret != "" {
		add(oauthprovider.NewMicrosoftProvider(oauthprovider.MicrosoftConfig{
			ClientID: microsoftID, ClientSecret: microsoftSecret,
			RedirectURL: baseURL + "/api/v1/auth/oauth/microsoft/callback",
			Tenant:      os.Getenv("MICROSOFT_OAUTH_TENANT"),
		}))
	}
	appleID := os.Getenv("APPLE_OAUTH_CLIENT_ID")
	appleTeamID := os.Getenv("APPLE_OAUTH_TEAM_ID")
	appleKeyID := os.Getenv("APPLE_OAUTH_KEY_ID")
	applePrivateKey := os.Getenv("APPLE_OAUTH_PRIVATE_KEY")
	if appleID != "" || appleTeamID != "" || appleKeyID != "" || applePrivateKey != "" {
		add(oauthprovider.NewAppleProvider(oauthprovider.AppleConfig{
			ClientID: appleID, TeamID: appleTeamID, KeyID: appleKeyID, PrivateKey: applePrivateKey,
			RedirectURL: baseURL + "/api/v1/auth/oauth/apple/callback",
		}))
	}
	return oauthprovider.NewRegistry(providers...)
}

func configuredOAuthFrontendURL() string {
	frontendURL := strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_URL")), "/")
	if frontendURL == "" {
		return "http://localhost:5173"
	}
	return frontendURL
}

func RegisterOAuthRoutes(group *gin.RouterGroup, oauthService *service.OAuthService, frontendURL string) {
	handler := &OAuthHandler{service: oauthService, frontendURL: strings.TrimRight(frontendURL, "/")}
	group.GET("/oauth/providers", handler.providers)
	group.GET("/oauth/:provider/start", middleware.OptionalAuthMiddleware(), handler.start)
	group.GET("/oauth/:provider/callback", handler.callback)
	group.POST("/oauth/:provider/callback", handler.callback)
	group.GET("/oauth/pending", handler.pending)
	group.POST("/oauth/pending/complete-profile", handler.completeProfile)
	group.POST("/oauth/pending/confirm-account", handler.confirmAccount)
	group.DELETE("/oauth/pending", handler.cancelPending)
	group.GET("/oauth/identities", middleware.StableAuthMiddleware(), handler.identities)
	group.DELETE("/oauth/:provider", middleware.StableAuthMiddleware(), handler.unlink)
}

// providers godoc
// @Summary 获取可用第三方登录平台
// @Tags auth-oauth
// @Produce json
// @Success 200 {object} OAuthProvidersResponse
// @Router /api/v1/auth/oauth/providers [get]
func (h *OAuthHandler) providers(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, OAuthProvidersResponse{Providers: h.service.ProviderNames()})
}

// start godoc
// @Summary 开始第三方登录或绑定
// @Tags auth-oauth
// @Produce json
// @Param provider path string true "平台" Enums(google,apple,github,microsoft)
// @Param purpose query string false "用途" Enums(login,link)
// @Param return_to query string false "站内返回路径"
// @Success 302
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Router /api/v1/auth/oauth/{provider}/start [get]
func (h *OAuthHandler) start(c *gin.Context) {
	purpose := strings.TrimSpace(c.Query("purpose"))
	if purpose == "" {
		purpose = model.OAuthPurposeLogin
	}
	input := service.OAuthBeginInput{
		Provider: c.Param("provider"),
		Purpose:  purpose,
		ReturnTo: c.Query("return_to"),
	}
	if purpose == model.OAuthPurposeLink {
		current, ok := authctx.Current(c)
		if !ok {
			httpx.Error(c, apperr.Unauthorized("Login required"))
			return
		}
		input.UserID = &current.ID
	}
	result, err := h.service.Begin(c.Request.Context(), input)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, result.AuthorizationURL)
}

// callback godoc
// @Summary 接收第三方登录回调
// @Tags auth-oauth
// @Param provider path string true "平台" Enums(google,apple,github,microsoft)
// @Param state query string false "OAuth state"
// @Param code query string false "Authorization code"
// @Success 302
// @Router /api/v1/auth/oauth/{provider}/callback [get]
// @Router /api/v1/auth/oauth/{provider}/callback [post]
func (h *OAuthHandler) callback(c *gin.Context) {
	if c.Request.FormValue("error") != "" {
		h.redirectFailure(c)
		return
	}
	result, err := h.service.HandleCallback(c.Request.Context(), service.OAuthCallbackInput{
		Provider: c.Param("provider"),
		State:    c.Request.FormValue("state"),
		Code:     c.Request.FormValue("code"),
		RawUser:  c.Request.FormValue("user"),
	})
	if err != nil {
		log.Printf("[OAuth] callback failed for %s: %v", c.Param("provider"), err)
		h.redirectFailure(c)
		return
	}
	if result.Status == service.OAuthCallbackPending {
		setOAuthFlowCookie(c, result.PendingToken)
		switch result.Stage {
		case model.OAuthStageCompleteProfile:
			h.redirect(c, "/auth/oauth/complete-profile")
		case model.OAuthStageConfirmAccount:
			h.redirect(c, "/auth/oauth/confirm-account")
		default:
			h.redirectFailure(c)
		}
		return
	}
	if result.Status != service.OAuthCallbackAuthenticated || result.User == nil {
		h.redirectFailure(c)
		return
	}
	token, err := generateAuthToken(*result.User)
	if err != nil {
		log.Printf("[OAuth] token generation failed: %v", err)
		h.redirectFailure(c)
		return
	}
	setAuthTokenCookie(c, token)
	clearOAuthFlowCookie(c)
	target := "/auth/oauth/callback?result=success"
	if result.ReturnTo != "" && result.ReturnTo != "/" {
		target += "&return_to=" + url.QueryEscape(result.ReturnTo)
	}
	h.redirect(c, target)
}

// pending godoc
// @Summary 获取待完成的第三方登录
// @Tags auth-oauth
// @Produce json
// @Success 200 {object} OAuthPendingResponse
// @Failure 400 {object} ErrorResponse
// @Router /api/v1/auth/oauth/pending [get]
func (h *OAuthHandler) pending(c *gin.Context) {
	token, err := c.Cookie(oauthFlowCookieName)
	if err != nil {
		httpx.Error(c, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired"))
		return
	}
	info, err := h.service.PendingInfo(c.Request.Context(), token)
	if err != nil {
		clearOAuthFlowCookie(c)
		httpx.Error(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"provider": info.Provider,
		"stage":    info.Stage,
		"email":    maskOAuthEmail(info.Email),
	})
}

type oauthCompleteProfileRequest struct {
	Username string `json:"username" binding:"required"`
}

// completeProfile godoc
// @Summary 完成第三方新账号资料
// @Tags auth-oauth
// @Accept json
// @Produce json
// @Param input body OAuthCompleteProfileRequest true "用户名"
// @Success 200 {object} OAuthCompletionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /api/v1/auth/oauth/pending/complete-profile [post]
func (h *OAuthHandler) completeProfile(c *gin.Context) {
	var input oauthCompleteProfileRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Username is required"))
		return
	}
	pendingToken, ok := h.pendingToken(c)
	if !ok {
		return
	}
	result, err := h.service.CompleteProfile(c.Request.Context(), service.OAuthCompleteProfileInput{
		PendingToken: pendingToken,
		Username:     input.Username,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	h.writeCompletion(c, result)
}

type oauthConfirmAccountRequest struct {
	Password string `json:"password" binding:"required"`
}

// confirmAccount godoc
// @Summary 验证原密码并绑定第三方身份
// @Tags auth-oauth
// @Accept json
// @Produce json
// @Param input body OAuthConfirmAccountRequest true "原账号密码"
// @Success 200 {object} OAuthCompletionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Router /api/v1/auth/oauth/pending/confirm-account [post]
func (h *OAuthHandler) confirmAccount(c *gin.Context) {
	var input oauthConfirmAccountRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "Password is required"))
		return
	}
	pendingToken, ok := h.pendingToken(c)
	if !ok {
		return
	}
	result, err := h.service.ConfirmAccount(c.Request.Context(), service.OAuthConfirmAccountInput{
		PendingToken: pendingToken,
		Password:     input.Password,
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	h.writeCompletion(c, result)
}

// cancelPending godoc
// @Summary 取消待完成的第三方登录
// @Tags auth-oauth
// @Success 204
// @Router /api/v1/auth/oauth/pending [delete]
func (h *OAuthHandler) cancelPending(c *gin.Context) {
	token, _ := c.Cookie(oauthFlowCookieName)
	if err := h.service.CancelPending(c.Request.Context(), token); err != nil {
		httpx.Error(c, err)
		return
	}
	clearOAuthFlowCookie(c)
	c.Status(http.StatusNoContent)
}

// identities godoc
// @Summary 获取当前账号的第三方登录方式
// @Tags auth-oauth
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {object} OAuthIdentitiesResponse
// @Failure 401 {object} ErrorResponse
// @Router /api/v1/auth/oauth/identities [get]
func (h *OAuthHandler) identities(c *gin.Context) {
	current, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	identities, err := h.service.ListIdentities(c.Request.Context(), current.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	type identityResponse struct {
		Provider    string     `json:"provider"`
		Email       string     `json:"email"`
		LastLoginAt *time.Time `json:"last_login_at"`
	}
	items := make([]identityResponse, 0, len(identities))
	for _, identity := range identities {
		items = append(items, identityResponse{
			Provider: identity.Provider, Email: identity.Email, LastLoginAt: identity.LastLoginAt,
		})
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"identities": items})
}

// unlink godoc
// @Summary 取消绑定第三方登录方式
// @Tags auth-oauth
// @Security BearerAuth
// @Security CookieAuth
// @Param provider path string true "平台" Enums(google,apple,github,microsoft)
// @Success 204
// @Failure 401 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /api/v1/auth/oauth/{provider} [delete]
func (h *OAuthHandler) unlink(c *gin.Context) {
	current, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if err := h.service.Unlink(c.Request.Context(), current.ID, c.Param("provider")); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *OAuthHandler) pendingToken(c *gin.Context) (string, bool) {
	token, err := c.Cookie(oauthFlowCookieName)
	if err != nil || token == "" {
		httpx.Error(c, apperr.BadRequest("oauth.invalid_flow", "OAuth session is invalid or expired"))
		return "", false
	}
	return token, true
}

func (h *OAuthHandler) writeCompletion(c *gin.Context, result service.OAuthCompletionResult) {
	token, err := generateAuthToken(result.User)
	if err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	setAuthTokenCookie(c, token)
	clearOAuthFlowCookie(c)
	payload := userAuthResponse(result.User, token)
	payload["return_to"] = result.ReturnTo
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, payload)
}

func (h *OAuthHandler) redirectFailure(c *gin.Context) {
	clearOAuthFlowCookie(c)
	h.redirect(c, "/auth/oauth/callback?result=failed")
}

func (h *OAuthHandler) redirect(c *gin.Context, path string) {
	status := http.StatusFound
	if c.Request.Method == http.MethodPost {
		status = http.StatusSeeOther
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(status, h.frontendURL+path)
}

func setOAuthFlowCookie(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: oauthFlowCookieName, Value: token, Path: "/api/v1/auth/oauth",
		Domain: os.Getenv("AUTH_COOKIE_DOMAIN"), MaxAge: int((10 * time.Minute).Seconds()),
		HttpOnly: true, Secure: oauthCookieSecure(), SameSite: http.SameSiteLaxMode,
	})
}

func clearOAuthFlowCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: oauthFlowCookieName, Value: "", Path: "/api/v1/auth/oauth",
		Domain: os.Getenv("AUTH_COOKIE_DOMAIN"), MaxAge: -1,
		HttpOnly: true, Secure: oauthCookieSecure(), SameSite: http.SameSiteLaxMode,
	})
}

func oauthCookieSecure() bool {
	return os.Getenv("ENV") == "production" || os.Getenv("AUTH_COOKIE_DOMAIN") != ""
}

func maskOAuthEmail(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 || parts[0] == "" {
		return ""
	}
	return string([]rune(parts[0])[0]) + "***@" + parts[1]
}

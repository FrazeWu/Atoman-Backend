package middleware

import (
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	AuthSessionCookieName = "atoman_session"
	CSRFHeaderName        = "X-CSRF-Token"
	authSessionContextKey = "auth_session"
)

var (
	authDBMu sync.RWMutex
	authDB   *gorm.DB
)

func SetAuthDB(db *gorm.DB) {
	authDBMu.Lock()
	defer authDBMu.Unlock()
	authDB = db
}

func currentAuthDB() *gorm.DB {
	authDBMu.RLock()
	defer authDBMu.RUnlock()
	return authDB
}

type requestCredential struct {
	token   string
	kind    string
	present bool
}

func credentialFromRequest(c *gin.Context) requestCredential {
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if authorization != "" {
		parts := strings.Fields(authorization)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return requestCredential{kind: authsession.KindAPI, present: true}
		}
		return requestCredential{
			token:   strings.TrimSpace(parts[1]),
			kind:    authsession.KindAPI,
			present: true,
		}
	}
	cookie, err := c.Cookie(AuthSessionCookieName)
	if err != nil || strings.TrimSpace(cookie) == "" {
		return requestCredential{}
	}
	return requestCredential{token: cookie, kind: authsession.KindWeb, present: true}
}

func resolveRequestSession(c *gin.Context) (authsession.Resolved, requestCredential, bool) {
	credential := credentialFromRequest(c)
	db := currentAuthDB()
	if !credential.present || credential.token == "" || db == nil {
		return authsession.Resolved{}, credential, false
	}
	resolved, err := authsession.New(db).Authenticate(credential.token, credential.kind)
	if err != nil {
		return authsession.Resolved{}, credential, false
	}
	return resolved, credential, true
}

func setAuthContext(c *gin.Context, resolved authsession.Resolved) {
	role := resolved.User.Role
	if role == "" {
		role = authctx.RoleUser
	}
	authctx.SetCurrentUser(c, authctx.CurrentUser{
		ID:       resolved.User.UUID,
		Username: resolved.User.Username,
		Role:     role,
	})
	c.Set("user_id", resolved.User.UUID)
	c.Set("username", resolved.User.Username)
	c.Set("role", role)
	c.Set(authSessionContextKey, resolved.Session)
}

func CurrentAuthSession(c *gin.Context) (model.AuthSession, bool) {
	value, exists := c.Get(authSessionContextKey)
	if !exists {
		return model.AuthSession{}, false
	}
	session, ok := value.(model.AuthSession)
	return session, ok
}

func IsTrustedWebOrigin(rawOrigin string) bool {
	rawOrigin = strings.TrimRight(strings.TrimSpace(rawOrigin), "/")
	if rawOrigin == "" {
		return false
	}
	configured := strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_URL")), "/")
	if configured != "" && rawOrigin == configured {
		return true
	}
	if rawOrigin == "https://www.atoman.org" || rawOrigin == "https://atoman.org" {
		return true
	}
	parsed, err := url.Parse(rawOrigin)
	if err != nil {
		return false
	}
	if parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1") {
		port, err := strconv.Atoi(parsed.Port())
		return err == nil && ((port >= 5173 && port <= 5180) || port == 52310)
	}
	if os.Getenv("ENV") == "production" {
		return false
	}
	return parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")
}

func TrustedOriginMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requiresCSRF(c.Request.Method) {
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if IsTrustedWebOrigin(origin) || (os.Getenv("ENV") != "production" && strings.TrimSpace(origin) == "") {
			c.Next()
			return
		}
		httpx.Error(c, apperr.Forbidden("auth.origin_invalid", "请求来源无效"))
		c.Abort()
	}
}

func requiresCSRF(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func verifyWebRequest(c *gin.Context, resolved authsession.Resolved, credential requestCredential) bool {
	if credential.kind != authsession.KindWeb || !requiresCSRF(c.Request.Method) {
		return true
	}
	if !IsTrustedWebOrigin(c.GetHeader("Origin")) || !authsession.New(currentAuthDB()).VerifyCSRF(resolved.Session, c.GetHeader(CSRFHeaderName)) {
		httpx.Error(c, apperr.Forbidden("auth.csrf_invalid", "请求已失效，请刷新页面后重试"))
		c.Abort()
		return false
	}
	return true
}

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		resolved, credential, ok := resolveRequestSession(c)
		if !credential.present {
			httpx.Error(c, apperr.Unauthorized("Authorization required"))
			c.Abort()
			return
		}
		if !ok {
			httpx.Error(c, apperr.Unauthorized("Invalid token"))
			c.Abort()
			return
		}
		if !verifyWebRequest(c, resolved, credential) {
			return
		}
		setAuthContext(c, resolved)
		c.Next()
	}
}

func StableAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		resolved, credential, ok := resolveRequestSession(c)
		if !credential.present {
			httpx.Error(c, apperr.Unauthorized("Authorization required"))
			c.Abort()
			return
		}
		if !ok {
			httpx.Error(c, apperr.Unauthorized("Invalid token"))
			c.Abort()
			return
		}
		if !verifyWebRequest(c, resolved, credential) {
			return
		}
		setAuthContext(c, resolved)
		c.Next()
	}
}

func OptionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		resolved, credential, ok := resolveRequestSession(c)
		if !credential.present {
			c.Next()
			return
		}
		if !ok {
			log.Printf("[Auth] No valid auth session found")
			c.Next()
			return
		}
		if !verifyWebRequest(c, resolved, credential) {
			return
		}
		setAuthContext(c, resolved)
		c.Next()
	}
}

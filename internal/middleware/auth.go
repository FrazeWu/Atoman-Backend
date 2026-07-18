package middleware

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
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

func jwtSecret() []byte {
	return []byte(os.Getenv("JWT_SECRET"))
}

func parseAuthToken(tokenString string) (*jwt.Token, error) {
	return jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return jwtSecret(), nil
	})
}

func ClaimsAuthVersion(claims jwt.MapClaims) (uint, bool) {
	raw, exists := claims["auth_version"]
	if !exists {
		return 0, true
	}
	switch value := raw.(type) {
	case float64:
		if value < 0 || value != float64(uint(value)) {
			return 0, false
		}
		return uint(value), true
	case uint:
		return value, true
	case int:
		if value < 0 {
			return 0, false
		}
		return uint(value), true
	default:
		return 0, false
	}
}

func authTokenCandidatesFromRequest(c *gin.Context) []string {
	candidates := make([]string, 0, 2)
	tokenString := c.GetHeader("Authorization")
	if strings.HasPrefix(tokenString, "Bearer ") {
		tokenString = strings.TrimPrefix(tokenString, "Bearer ")
	}
	if tokenString != "" {
		candidates = append(candidates, tokenString)
	}
	cookie, err := c.Cookie("atoman_token")
	if err == nil && cookie != "" {
		candidates = append(candidates, cookie)
	}
	return candidates
}

func resolveAuthClaims(c *gin.Context) (jwt.MapClaims, bool) {
	for _, tokenString := range authTokenCandidatesFromRequest(c) {
		token, err := parseAuthToken(tokenString)
		if err != nil || !token.Valid {
			continue
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok || !setAuthContext(c, claims) {
			continue
		}
		return claims, true
	}
	return nil, false
}

func setAuthContext(c *gin.Context, claims jwt.MapClaims) bool {
	var userIDStr string
	switch v := claims["user_id"].(type) {
	case string:
		userIDStr = v
	case float64:
		userIDStr = fmt.Sprintf("%v", v)
	default:
		return false
	}

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return false
	}

	db := currentAuthDB()
	if db == nil {
		return false
	}

	var user model.User
	if err := db.Where("uuid = ? AND is_active = ?", userID, true).First(&user).Error; err != nil {
		return false
	}
	authVersion, ok := ClaimsAuthVersion(claims)
	if !ok || authVersion != user.AuthVersion {
		return false
	}
	username := user.Username
	role := user.Role
	if role == "" {
		role = "user"
	}

	authctx.SetCurrentUser(c, authctx.CurrentUser{ID: userID, Username: username, Role: role})
	return true
}

// AuthMiddleware validates JWT tokens and sets user context
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(authTokenCandidatesFromRequest(c)) == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}
		if _, ok := resolveAuthClaims(c); !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func StableAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(authTokenCandidatesFromRequest(c)) == 0 {
			httpx.Error(c, apperr.Unauthorized("Authorization header required"))
			c.Abort()
			return
		}
		if _, ok := resolveAuthClaims(c); !ok {
			httpx.Error(c, apperr.Unauthorized("Invalid token"))
			c.Abort()
			return
		}
		c.Next()
	}
}

// OptionalAuthMiddleware validates JWT if present, but does not block if missing
func OptionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(authTokenCandidatesFromRequest(c)) == 0 {
			c.Next()
			return
		}
		if _, ok := resolveAuthClaims(c); !ok {
			log.Printf("[Auth] No valid auth token found in request candidates")
		}
		c.Next()
	}
}

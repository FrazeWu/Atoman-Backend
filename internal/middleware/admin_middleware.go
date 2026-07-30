package middleware

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
)

// AdminMiddleware ensures the current user has admin role
func AdminMiddleware(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		roleVal, roleExists := c.Get("role")
		if roleExists {
			if role, ok := roleVal.(string); ok && authctx.RoleAtLeast(role, authctx.RoleAdmin) {
				c.Next()
				return
			}
		}

		userIDVal, ok := c.Get("user_id")
		if !ok {
			httpx.Error(c, apperr.Unauthorized("Unauthorized"))
			c.Abort()
			return
		}

		userID, err := normalizeUserID(userIDVal)
		if err != nil {
			httpx.Error(c, apperr.Unauthorized("Unauthorized"))
			c.Abort()
			return
		}

		var user model.User
		if err := findUserByContextID(db, userIDVal, userID, &user); err != nil {
			if err == gorm.ErrRecordNotFound {
				httpx.Error(c, apperr.Unauthorized("Unauthorized"))
			} else {
				httpx.Error(c, apperr.Internal(err))
			}
			c.Abort()
			return
		}

		if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
			httpx.Error(c, apperr.Forbidden("auth.admin_required", "Admin access required"))
			c.Abort()
			return
		}

		c.Next()
	}
}

func findUserByContextID(db *gorm.DB, raw interface{}, numericID uint, user *model.User) error {
	if uid, ok := raw.(uuid.UUID); ok {
		return db.Where("uuid = ?", uid).First(user).Error
	}
	return db.First(user, numericID).Error
}

func normalizeUserID(value interface{}) (uint, error) {
	switch v := value.(type) {
	case uuid.UUID:
		return 0, nil
	case float64:
		return uint(v), nil
	case float32:
		return uint(v), nil
	case int:
		return uint(v), nil
	case int64:
		return uint(v), nil
	case uint:
		return v, nil
	case uint64:
		return uint(v), nil
	case string:
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, err
		}
		return uint(parsed), nil
	default:
		return 0, strconv.ErrSyntax
	}
}

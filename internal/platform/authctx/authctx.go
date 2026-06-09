package authctx

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const currentUserKey = "current_user"

const (
	RoleAnonymous = "anonymous"
	RoleUser      = "user"
	RoleModerator = "moderator"
	RoleAdmin     = "admin"
	RoleOwner     = "owner"
)

type CurrentUser struct {
	ID       uuid.UUID
	Username string
	Role     string
}

func SetCurrentUser(c *gin.Context, user CurrentUser) {
	if user.Role == "" {
		user.Role = RoleUser
	}
	c.Set(currentUserKey, user)
	// Temporary compatibility for legacy handlers. New code must use Current().
	c.Set("user_id", user.ID)
	c.Set("userID", user.ID)
	c.Set("username", user.Username)
	c.Set("role", user.Role)
}

func Current(c *gin.Context) (CurrentUser, bool) {
	value, ok := c.Get(currentUserKey)
	if !ok {
		return CurrentUser{Role: RoleAnonymous}, false
	}
	user, ok := value.(CurrentUser)
	if !ok {
		return CurrentUser{Role: RoleAnonymous}, false
	}
	if user.Role == "" {
		user.Role = RoleUser
	}
	return user, true
}

func RoleAtLeast(actual string, required string) bool {
	return roleRank(actual) >= roleRank(required)
}

func roleRank(role string) int {
	switch role {
	case RoleOwner:
		return 50
	case RoleAdmin:
		return 40
	case RoleModerator:
		return 30
	case RoleUser:
		return 20
	default:
		return 10
	}
}

package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/platform/authctx"
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

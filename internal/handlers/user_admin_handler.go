package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"
)

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

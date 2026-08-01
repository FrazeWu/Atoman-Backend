package handlers

import (
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/httpx"
	"atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AdminUserDTO struct {
	UUID              uuid.UUID  `json:"uuid" format:"uuid"`
	Username          string     `json:"username"`
	Email             string     `json:"email"`
	DisplayName       string     `json:"display_name"`
	AvatarURL         string     `json:"avatar_url"`
	Role              string     `json:"role"`
	IsActive          bool       `json:"is_active"`
	LastLoginAt       *time.Time `json:"last_login_at"`
	LastLoginIP       string     `json:"last_login_ip"`
	LastLoginLocation string     `json:"last_login_location"`
	ActiveSessions    int64      `json:"active_sessions"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type AdminUserListResponse struct {
	Data []AdminUserDTO `json:"data"`
	Meta httpx.PageMeta `json:"meta"`
}

type AdminUserResponse struct {
	Data AdminUserDTO `json:"data"`
}

type CreateAdminUserInput struct {
	Username    string `json:"username" binding:"required"`
	Email       string `json:"email" binding:"required,email"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password" binding:"required"`
	Role        string `json:"role"`
}

type UpdateAdminUserInput struct {
	Username    *string `json:"username"`
	Email       *string `json:"email" binding:"omitempty,email"`
	DisplayName *string `json:"display_name"`
	Role        *string `json:"role"`
}

type UpdateAdminUserStatusInput struct {
	IsActive *bool `json:"is_active" binding:"required"`
}

type ResetAdminUserPasswordInput struct {
	Password string `json:"password" binding:"required"`
}

type adminUserHandler struct {
	db *gorm.DB
}

func SetupAdminUserRoutes(router *gin.Engine, db *gorm.DB) {
	handler := &adminUserHandler{db: db}
	admin := router.Group("/api/v1/admin")
	admin.Use(middleware.AuthMiddleware(), middleware.AdminMiddleware(db))
	users := admin.Group("/users")
	users.GET("", handler.list)
	users.POST("", handler.create)
	users.GET("/:id", handler.get)
	users.PATCH("/:id", handler.update)
	users.PUT("/:id/status", handler.updateStatus)
	users.PUT("/:id/password", handler.resetPassword)
	users.GET("/:id/login-events", handler.listLoginEvents)
	users.GET("/:id/sessions", handler.listSessions)
	users.DELETE("/:id/sessions/:sessionID", handler.revokeSession)
	users.DELETE("/:id/sessions", handler.revokeAllSessions)
	users.GET("/:id/audit-logs", handler.listUserAuditLogs)
	users.DELETE("/:id", handler.delete)
	admin.GET("/user-audit-logs", handler.listAuditLogs)
}

// list godoc
// @Summary 管理员获取用户列表
// @Description 管理员可分页搜索未删除用户，并按角色和账号状态筛选。
// @Tags admin-users
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param q query string false "用户名、邮箱或显示名"
// @Param role query string false "角色" Enums(user,moderator,admin,owner)
// @Param status query string false "账号状态" Enums(all,active,inactive) default(all)
// @Param activity query string false "登录时间" Enums(all,7d,inactive_30d,never) default(all)
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量，最大 100" default(20)
// @Success 200 {object} AdminUserListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/admin/users [get]
func (handler *adminUserHandler) list(c *gin.Context) {
	page, pageSize := httpx.PageParams(c)
	query := handler.db.Model(&model.User{})
	if q := strings.TrimSpace(c.Query("q")); q != "" {
		like := "%" + q + "%"
		query = query.Where("LOWER(username) LIKE LOWER(?) OR LOWER(email) LIKE LOWER(?) OR LOWER(display_name) LIKE LOWER(?)", like, like, like)
	}
	if role := strings.TrimSpace(c.Query("role")); role != "" {
		if !isKnownUserRole(role) {
			httpx.Error(c, apperr.BadRequest("admin_user.invalid_role", "角色筛选无效"))
			return
		}
		query = query.Where("role = ?", role)
	}
	switch status := strings.TrimSpace(c.DefaultQuery("status", "all")); status {
	case "all":
	case "active":
		query = query.Where("is_active = ?", true)
	case "inactive":
		query = query.Where("is_active = ?", false)
	default:
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_status", "账号状态筛选无效"))
		return
	}
	switch activity := strings.TrimSpace(c.DefaultQuery("activity", "all")); activity {
	case "all":
	case "7d":
		query = query.Where(`EXISTS (SELECT 1 FROM auth_login_events WHERE auth_login_events.user_id = "Users".uuid AND auth_login_events.result = ? AND auth_login_events.created_at >= ?)`, model.LoginResultSucceeded, time.Now().UTC().Add(-7*24*time.Hour))
	case "inactive_30d":
		query = query.Where(`NOT EXISTS (SELECT 1 FROM auth_login_events WHERE auth_login_events.user_id = "Users".uuid AND auth_login_events.result = ? AND auth_login_events.created_at >= ?)`, model.LoginResultSucceeded, time.Now().UTC().Add(-30*24*time.Hour))
	case "never":
		query = query.Where(`NOT EXISTS (SELECT 1 FROM auth_login_events WHERE auth_login_events.user_id = "Users".uuid AND auth_login_events.result = ?)`, model.LoginResultSucceeded)
	default:
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_activity", "登录时间筛选无效"))
		return
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	users := make([]AdminUserDTO, 0)
	if err := query.Select("uuid, username, email, display_name, avatar_url, role, is_active, created_at, updated_at").
		Order("created_at DESC").Order("uuid DESC").
		Offset(httpx.Offset(page, pageSize)).Limit(pageSize).Scan(&users).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	pointers := make([]*AdminUserDTO, 0, len(users))
	for index := range users {
		pointers = append(pointers, &users[index])
	}
	if err := handler.enrichUserSummaries(pointers); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	httpx.List(c, users, page, pageSize, total)
}

// create godoc
// @Summary 管理员创建用户
// @Description 管理员只能创建普通用户，站长可创建普通用户或管理员。
// @Tags admin-users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param input body CreateAdminUserInput true "用户信息"
// @Success 201 {object} AdminUserResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/admin/users [post]
func (handler *adminUserHandler) create(c *gin.Context) {
	var input CreateAdminUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_input", "请检查用户信息"))
		return
	}
	actor, _ := authctx.Current(c)
	input.Role = strings.TrimSpace(input.Role)
	if input.Role == "" {
		input.Role = authctx.RoleUser
	}
	if input.Role != authctx.RoleUser && input.Role != authctx.RoleAdmin {
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_role", "角色只能是普通用户或管理员"))
		return
	}
	if input.Role == authctx.RoleAdmin && actor.Role != authctx.RoleOwner {
		httpx.Error(c, apperr.Forbidden("admin_user.role_forbidden", "只有站长可以创建管理员"))
		return
	}
	username, err := handler.availableUsername(c, input.Username, nil)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	email, err := handler.availableEmail(input.Email, nil)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if !validPasswordLength(input.Password) {
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_password", "密码长度需为 6-72 字节"))
		return
	}
	password, err := HashPassword(input.Password)
	if err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	user := model.User{
		Username:    username,
		Email:       email,
		DisplayName: strings.TrimSpace(input.DisplayName),
		Password:    password,
		Role:        input.Role,
		IsActive:    true,
	}
	if err := handler.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		if err := tx.Create(&model.UserSettings{UserID: user.UUID}).Error; err != nil {
			return err
		}
		if err := service.NewUserBootstrapService(tx).EnsureDefaults(user.UUID, user.Username); err != nil {
			return err
		}
		return handler.recordAdminAudit(c, tx, actor, user, "admin_user.created", "", map[string]any{
			"after": adminUserAuditSnapshot(user),
		})
	}); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	httpx.OK(c, http.StatusCreated, adminUserDTO(user))
}

// update godoc
// @Summary 管理员编辑用户
// @Description 管理员可编辑普通用户，站长还可编辑管理员和调整角色。
// @Tags admin-users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Param input body UpdateAdminUserInput true "用户信息"
// @Success 200 {object} AdminUserResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/admin/users/{id} [patch]
func (handler *adminUserHandler) update(c *gin.Context) {
	user, actor, ok := handler.managedUser(c)
	if !ok {
		return
	}
	var input UpdateAdminUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_input", "请检查用户信息"))
		return
	}
	updates := map[string]any{}
	if input.Username != nil {
		username := strings.ToLower(strings.TrimSpace(*input.Username))
		if !strings.EqualFold(username, user.Username) {
			validated, err := handler.availableUsername(c, username, &user.UUID)
			if err != nil {
				httpx.Error(c, err)
				return
			}
			username = validated
		}
		updates["username"] = username
	}
	if input.Email != nil {
		email, err := handler.availableEmail(*input.Email, &user.UUID)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		updates["email"] = email
	}
	if input.DisplayName != nil {
		updates["display_name"] = strings.TrimSpace(*input.DisplayName)
	}
	if input.Role != nil {
		role := strings.TrimSpace(*input.Role)
		if actor.Role != authctx.RoleOwner {
			httpx.Error(c, apperr.Forbidden("admin_user.role_forbidden", "只有站长可以调整角色"))
			return
		}
		if role != authctx.RoleUser && role != authctx.RoleAdmin {
			httpx.Error(c, apperr.BadRequest("admin_user.invalid_role", "角色只能是普通用户或管理员"))
			return
		}
		updates["role"] = role
	}
	if len(updates) > 0 {
		before := adminUserAuditSnapshot(user)
		if err := handler.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&user).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.First(&user, "uuid = ?", user.UUID).Error; err != nil {
				return err
			}
			return handler.recordAdminAudit(c, tx, actor, user, "admin_user.updated", "", map[string]any{
				"before": before,
				"after":  adminUserAuditSnapshot(user),
			})
		}); err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
	}
	httpx.OK(c, http.StatusOK, adminUserDTO(user))
}

// updateStatus godoc
// @Summary 管理员停用或恢复用户
// @Description 停用账号时立即撤销该用户的全部登录会话。
// @Tags admin-users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Param input body UpdateAdminUserStatusInput true "账号状态"
// @Success 200 {object} AdminUserResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/admin/users/{id}/status [put]
func (handler *adminUserHandler) updateStatus(c *gin.Context) {
	user, actor, ok := handler.managedUser(c)
	if !ok {
		return
	}
	var input UpdateAdminUserStatusInput
	if err := c.ShouldBindJSON(&input); err != nil || input.IsActive == nil {
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_input", "请选择账号状态"))
		return
	}
	if err := handler.db.Transaction(func(tx *gorm.DB) error {
		updates := map[string]any{"is_active": *input.IsActive}
		if !*input.IsActive {
			updates["auth_version"] = gorm.Expr("auth_version + 1")
		}
		if err := tx.Model(&user).Updates(updates).Error; err != nil {
			return err
		}
		if !*input.IsActive {
			if err := authsession.New(tx).RevokeUser(user.UUID); err != nil {
				return err
			}
		}
		action := "admin_user.restored"
		if !*input.IsActive {
			action = "admin_user.deactivated"
		}
		return handler.recordAdminAudit(c, tx, actor, user, action, "", map[string]any{
			"before_active": user.IsActive,
			"after_active":  *input.IsActive,
		})
	}); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	if err := handler.db.First(&user, "uuid = ?", user.UUID).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	httpx.OK(c, http.StatusOK, adminUserDTO(user))
}

// resetPassword godoc
// @Summary 管理员重置用户密码
// @Description 重置密码后立即撤销该用户的全部登录会话。
// @Tags admin-users
// @Accept json
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Param input body ResetAdminUserPasswordInput true "新密码"
// @Success 204
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/admin/users/{id}/password [put]
func (handler *adminUserHandler) resetPassword(c *gin.Context) {
	user, actor, ok := handler.managedUser(c)
	if !ok {
		return
	}
	var input ResetAdminUserPasswordInput
	if err := c.ShouldBindJSON(&input); err != nil || !validPasswordLength(input.Password) {
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_password", "密码长度需为 6-72 字节"))
		return
	}
	password, err := HashPassword(input.Password)
	if err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	if err := handler.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&user).Updates(map[string]any{
			"password":     password,
			"auth_version": gorm.Expr("auth_version + 1"),
		}).Error; err != nil {
			return err
		}
		if err := authsession.New(tx).RevokeUser(user.UUID); err != nil {
			return err
		}
		return handler.recordAdminAudit(c, tx, actor, user, "admin_user.password_reset", "", nil)
	}); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// delete godoc
// @Summary 管理员删除用户
// @Description 不可恢复地软删除账号，保留历史内容和用户名、邮箱占用关系。
// @Tags admin-users
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Success 204
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/admin/users/{id} [delete]
func (handler *adminUserHandler) delete(c *gin.Context) {
	user, actor, ok := handler.managedUser(c)
	if !ok {
		return
	}
	if err := handler.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&user).Updates(map[string]any{
			"is_active":    false,
			"auth_version": gorm.Expr("auth_version + 1"),
		}).Error; err != nil {
			return err
		}
		if err := authsession.New(tx).RevokeUser(user.UUID); err != nil {
			return err
		}
		if err := handler.recordAdminAudit(c, tx, actor, user, "admin_user.deleted", "", map[string]any{
			"before": adminUserAuditSnapshot(user),
		}); err != nil {
			return err
		}
		return tx.Delete(&user).Error
	}); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	c.Status(http.StatusNoContent)
}

func (handler *adminUserHandler) managedUser(c *gin.Context) (model.User, authctx.CurrentUser, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_id", "用户 ID 无效"))
		return model.User{}, authctx.CurrentUser{}, false
	}
	var user model.User
	if err := handler.db.First(&user, "uuid = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			httpx.Error(c, apperr.NotFound("admin_user.not_found", "用户不存在"))
		} else {
			httpx.Error(c, apperr.Internal(err))
		}
		return model.User{}, authctx.CurrentUser{}, false
	}
	actor, _ := authctx.Current(c)
	if !canManageAdminUser(actor, user) {
		httpx.Error(c, apperr.Forbidden("admin_user.target_forbidden", "不能管理该用户"))
		return model.User{}, actor, false
	}
	return user, actor, true
}

func canManageAdminUser(actor authctx.CurrentUser, target model.User) bool {
	if target.Role == authctx.RoleOwner || actor.ID == target.UUID {
		return false
	}
	if actor.Role == authctx.RoleOwner {
		return true
	}
	return actor.Role == authctx.RoleAdmin && target.Role == authctx.RoleUser
}

func isKnownUserRole(role string) bool {
	switch role {
	case authctx.RoleUser, authctx.RoleModerator, authctx.RoleAdmin, authctx.RoleOwner:
		return true
	default:
		return false
	}
}

func (handler *adminUserHandler) availableUsername(c *gin.Context, raw string, exclude *uuid.UUID) (string, error) {
	username := strings.ToLower(strings.TrimSpace(raw))
	if err := service.NewSiteNamespaceService(handler.db).ValidateUsernameAvailable(c.Request.Context(), username); err != nil {
		switch {
		case errors.Is(err, service.ErrSiteHandleInvalid), errors.Is(err, service.ErrSiteHandleReserved):
			return "", apperr.BadRequest("admin_user.invalid_username", "用户名格式无效或不可用")
		case errors.Is(err, service.ErrSiteHandleTaken):
			return "", apperr.Conflict("admin_user.username_taken", "用户名已被占用")
		default:
			return "", apperr.Internal(err)
		}
	}
	query := handler.db.Unscoped().Model(&model.User{}).Where("LOWER(username) = ?", username)
	if exclude != nil {
		query = query.Where("uuid <> ?", *exclude)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return "", apperr.Internal(err)
	}
	if count > 0 {
		return "", apperr.Conflict("admin_user.username_taken", "用户名已被占用")
	}
	return username, nil
}

func (handler *adminUserHandler) availableEmail(raw string, exclude *uuid.UUID) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", apperr.BadRequest("admin_user.invalid_email", "邮箱格式无效")
	}
	query := handler.db.Unscoped().Model(&model.User{}).Where("LOWER(email) = ?", email)
	if exclude != nil {
		query = query.Where("uuid <> ?", *exclude)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return "", apperr.Internal(err)
	}
	if count > 0 {
		return "", apperr.Conflict("admin_user.email_taken", "邮箱已被占用")
	}
	return email, nil
}

func adminUserDTO(user model.User) AdminUserDTO {
	return AdminUserDTO{
		UUID:        user.UUID,
		Username:    user.Username,
		Email:       user.Email,
		DisplayName: user.DisplayName,
		AvatarURL:   user.AvatarURL,
		Role:        user.Role,
		IsActive:    user.IsActive,
		CreatedAt:   user.CreatedAt,
		UpdatedAt:   user.UpdatedAt,
	}
}

func adminUserAuditSnapshot(user model.User) map[string]any {
	return map[string]any{
		"username":     user.Username,
		"email":        user.Email,
		"display_name": user.DisplayName,
		"role":         user.Role,
		"is_active":    user.IsActive,
	}
}

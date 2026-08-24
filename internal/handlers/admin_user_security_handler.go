package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/platform/httpx"
	"atoman/internal/platform/requestmeta"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AdminUserDetailDTO struct {
	AdminUserDTO
	Bio             string   `json:"bio"`
	Website         string   `json:"website"`
	ProfileLocation string   `json:"profile_location"`
	HasPassword     bool     `json:"has_password"`
	AuthProviders   []string `json:"auth_providers"`
}

type AdminLoginEventDTO struct {
	ID          uuid.UUID  `json:"id"`
	SessionID   *uuid.UUID `json:"session_id,omitempty"`
	Method      string     `json:"method"`
	Result      string     `json:"result"`
	FailureCode string     `json:"failure_code,omitempty"`
	IPAddress   string     `json:"ip_address"`
	IPPrefix    string     `json:"ip_prefix"`
	Location    string     `json:"location"`
	DeviceName  string     `json:"device_name"`
	UserAgent   string     `json:"user_agent"`
	CreatedAt   time.Time  `json:"created_at"`
}

type AdminSessionDTO struct {
	ID           uuid.UUID `json:"id"`
	Kind         string    `json:"kind"`
	DeviceName   string    `json:"device_name"`
	UserAgent    string    `json:"user_agent"`
	IPAddress    string    `json:"ip_address"`
	IPPrefix     string    `json:"ip_prefix"`
	Location     string    `json:"location"`
	CreatedAt    time.Time `json:"created_at"`
	LastActiveAt time.Time `json:"last_active_at"`
}

type AdminAuditLogDTO struct {
	ID             uuid.UUID      `json:"id"`
	ActorID        *uuid.UUID     `json:"actor_id,omitempty"`
	ActorUsername  string         `json:"actor_username"`
	TargetUserID   *uuid.UUID     `json:"target_user_id,omitempty"`
	TargetUsername string         `json:"target_username"`
	Action         string         `json:"action"`
	Reason         string         `json:"reason"`
	IPAddress      string         `json:"ip_address"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      time.Time      `json:"created_at"`
}

type AdminUserDetailResponse struct {
	Data AdminUserDetailDTO `json:"data"`
}

type AdminLoginEventListResponse struct {
	Data []AdminLoginEventDTO `json:"data"`
	Meta httpx.PageMeta       `json:"meta"`
}

type AdminSessionListResponse struct {
	Data []AdminSessionDTO `json:"data"`
}

type AdminAuditLogListResponse struct {
	Data []AdminAuditLogDTO `json:"data"`
	Meta httpx.PageMeta     `json:"meta"`
}

func (handler *adminUserHandler) enrichUserSummaries(users []*AdminUserDTO) error {
	if len(users) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(users))
	byID := make(map[uuid.UUID]*AdminUserDTO, len(users))
	for _, user := range users {
		ids = append(ids, user.UUID)
		byID[user.UUID] = user
	}
	var events []model.LoginEvent
	if err := handler.db.Where("user_id IN ? AND result = ?", ids, model.LoginResultSucceeded).
		Order("created_at DESC").Order("id DESC").Find(&events).Error; err != nil {
		return err
	}
	seen := map[uuid.UUID]bool{}
	for _, event := range events {
		if seen[event.UserID] {
			continue
		}
		user := byID[event.UserID]
		if user == nil {
			continue
		}
		loggedAt := event.CreatedAt
		user.LastLoginAt = &loggedAt
		user.LastLoginIP = event.IPAddress
		user.LastLoginLocation = loginEventLocation(event)
		seen[event.UserID] = true
	}
	type sessionCount struct {
		UserID         uuid.UUID
		ActiveSessions int64
	}
	var counts []sessionCount
	if err := handler.db.Model(&model.AuthSession{}).
		Select("user_id, COUNT(*) AS active_sessions").
		Where("user_id IN ? AND revoked_at IS NULL AND expires_at > ?", ids, time.Now().UTC()).
		Group("user_id").Scan(&counts).Error; err != nil {
		return err
	}
	for _, count := range counts {
		if user := byID[count.UserID]; user != nil {
			user.ActiveSessions = count.ActiveSessions
		}
	}
	return nil
}

// get godoc
// @Summary 管理员获取用户详情
// @Tags admin-users
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Success 200 {object} AdminUserDetailResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/admin/users/{id} [get]
func (handler *adminUserHandler) get(c *gin.Context) {
	user, _, ok := handler.sensitiveUser(c)
	if !ok {
		return
	}
	detail := AdminUserDetailDTO{
		AdminUserDTO:    adminUserDTO(user),
		Bio:             user.Bio,
		Website:         user.Website,
		ProfileLocation: user.Location,
		HasPassword:     user.Password != "",
		AuthProviders:   []string{},
	}
	if err := handler.enrichUserSummaries([]*AdminUserDTO{&detail.AdminUserDTO}); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	if err := handler.db.Model(&model.ExternalIdentity{}).
		Where("user_id = ?", user.UUID).Order("provider ASC").Pluck("provider", &detail.AuthProviders).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	httpx.OK(c, http.StatusOK, detail)
}

// listLoginEvents godoc
// @Summary 管理员获取用户登录记录
// @Tags admin-users
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Param result query string false "登录结果" Enums(succeeded,failed)
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量" default(20)
// @Success 200 {object} AdminLoginEventListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /api/v1/admin/users/{id}/login-events [get]
func (handler *adminUserHandler) listLoginEvents(c *gin.Context) {
	user, _, ok := handler.sensitiveUser(c)
	if !ok {
		return
	}
	page, pageSize := httpx.PageParams(c)
	query := handler.db.Model(&model.LoginEvent{}).Where("user_id = ?", user.UUID)
	if result := strings.TrimSpace(c.Query("result")); result != "" {
		if result != model.LoginResultSucceeded && result != model.LoginResultFailed {
			httpx.Error(c, apperr.BadRequest("admin_user.invalid_login_result", "登录结果筛选无效"))
			return
		}
		query = query.Where("result = ?", result)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	var events []model.LoginEvent
	if err := query.Order("created_at DESC").Order("id DESC").
		Offset(httpx.Offset(page, pageSize)).Limit(pageSize).Find(&events).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	items := make([]AdminLoginEventDTO, 0, len(events))
	for _, event := range events {
		items = append(items, adminLoginEventDTO(event))
	}
	httpx.List(c, items, page, pageSize, total)
}

// listSessions godoc
// @Summary 管理员获取用户有效会话
// @Tags admin-users
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Success 200 {object} AdminSessionListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /api/v1/admin/users/{id}/sessions [get]
func (handler *adminUserHandler) listSessions(c *gin.Context) {
	user, _, ok := handler.sensitiveUser(c)
	if !ok {
		return
	}
	sessions, err := authsession.New(handler.db).List(user.UUID, "")
	if err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	sessionIDs := make([]uuid.UUID, 0, len(sessions))
	for _, session := range sessions {
		sessionIDs = append(sessionIDs, session.ID)
	}
	locations := map[uuid.UUID]string{}
	if len(sessionIDs) > 0 {
		var events []model.LoginEvent
		if err := handler.db.Where("session_id IN ?", sessionIDs).Order("created_at DESC").Find(&events).Error; err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
		for _, event := range events {
			if event.SessionID != nil {
				if _, exists := locations[*event.SessionID]; !exists {
					locations[*event.SessionID] = loginEventLocation(event)
				}
			}
		}
	}
	items := make([]AdminSessionDTO, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, AdminSessionDTO{
			ID: session.ID, Kind: session.Kind, DeviceName: session.DeviceName, UserAgent: session.UserAgent,
			IPAddress: session.IPAddress, IPPrefix: session.IPPrefix, Location: locations[session.ID],
			CreatedAt: session.CreatedAt, LastActiveAt: session.LastActiveAt,
		})
	}
	httpx.OK(c, http.StatusOK, items)
}

// revokeSession godoc
// @Summary 管理员撤销用户单个会话
// @Tags admin-users
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Param sessionID path string true "会话 UUID"
// @Success 204
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/admin/users/{id}/sessions/{sessionID} [delete]
func (handler *adminUserHandler) revokeSession(c *gin.Context) {
	user, actor, ok := handler.managedUser(c)
	if !ok {
		return
	}
	sessionID, err := uuid.Parse(c.Param("sessionID"))
	if err != nil || sessionID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("admin_user.invalid_session_id", "会话 ID 无效"))
		return
	}
	err = handler.db.Transaction(func(tx *gorm.DB) error {
		if err := authsession.New(tx).RevokeUserSession(user.UUID, sessionID); err != nil {
			return err
		}
		return handler.recordAdminAudit(c, tx, actor, user, "admin_user.session_revoked", "", map[string]any{"session_id": sessionID})
	})
	if errors.Is(err, authsession.ErrInvalid) {
		httpx.Error(c, apperr.NotFound("admin_user.session_not_found", "登录会话不存在"))
		return
	}
	if err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// revokeAllSessions godoc
// @Summary 管理员撤销用户全部会话
// @Tags admin-users
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Success 204
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /api/v1/admin/users/{id}/sessions [delete]
func (handler *adminUserHandler) revokeAllSessions(c *gin.Context) {
	user, actor, ok := handler.managedUser(c)
	if !ok {
		return
	}
	if err := handler.db.Transaction(func(tx *gorm.DB) error {
		if err := authsession.New(tx).RevokeUser(user.UUID); err != nil {
			return err
		}
		return handler.recordAdminAudit(c, tx, actor, user, "admin_user.sessions_revoked", "", nil)
	}); err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	c.Status(http.StatusNoContent)
}

// listUserAuditLogs godoc
// @Summary 管理员获取单个用户的管理记录
// @Tags admin-users
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "用户 UUID"
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量" default(20)
// @Success 200 {object} AdminAuditLogListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /api/v1/admin/users/{id}/audit-logs [get]
func (handler *adminUserHandler) listUserAuditLogs(c *gin.Context) {
	user, _, ok := handler.sensitiveUser(c)
	if !ok {
		return
	}
	handler.writeAuditLogList(c, handler.db.Model(&model.AuditLog{}).
		Where("entity_type = ? AND entity_id = ? AND action LIKE ?", "user", user.UUID, "admin_user.%"))
}

// listAuditLogs godoc
// @Summary 管理员获取用户管理操作记录
// @Tags admin-users
// @Produce json
// @Security BearerAuth
// @Security CookieAuth
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量" default(20)
// @Success 200 {object} AdminAuditLogListResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /api/v1/admin/user-audit-logs [get]
func (handler *adminUserHandler) listAuditLogs(c *gin.Context) {
	query := handler.db.Model(&model.AuditLog{}).Where("action LIKE ?", "admin_user.%")
	actor, _ := authctx.Current(c)
	if actor.Role != authctx.RoleOwner {
		query = query.Where("actor_id = ?", actor.ID)
	}
	handler.writeAuditLogList(c, query)
}

func (handler *adminUserHandler) writeAuditLogList(c *gin.Context, query *gorm.DB) {
	page, pageSize := httpx.PageParams(c)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	var logs []model.AuditLog
	if err := query.Order("created_at DESC").Order("id DESC").
		Offset(httpx.Offset(page, pageSize)).Limit(pageSize).Find(&logs).Error; err != nil {
		httpx.Error(c, apperr.Internal(err))
		return
	}
	actorIDs := make([]uuid.UUID, 0, len(logs))
	for _, entry := range logs {
		if entry.ActorID != nil {
			actorIDs = append(actorIDs, *entry.ActorID)
		}
	}
	actors := map[uuid.UUID]string{}
	if len(actorIDs) > 0 {
		var users []model.User
		if err := handler.db.Unscoped().Select("uuid, username").Where("uuid IN ?", actorIDs).Find(&users).Error; err != nil {
			httpx.Error(c, apperr.Internal(err))
			return
		}
		for _, user := range users {
			actors[user.UUID] = user.Username
		}
	}
	items := make([]AdminAuditLogDTO, 0, len(logs))
	for _, entry := range logs {
		metadata := map[string]any{}
		_ = json.Unmarshal([]byte(entry.Metadata), &metadata)
		targetUsername, _ := metadata["target_username"].(string)
		ipAddress, _ := metadata["ip_address"].(string)
		actorUsername := "系统"
		if entry.ActorID != nil {
			actorUsername = actors[*entry.ActorID]
			if actorUsername == "" {
				actorUsername = "未知用户"
			}
		}
		items = append(items, AdminAuditLogDTO{
			ID: entry.ID, ActorID: entry.ActorID, ActorUsername: actorUsername,
			TargetUserID: entry.EntityID, TargetUsername: targetUsername, Action: entry.Action,
			Reason: entry.Reason, IPAddress: ipAddress, Metadata: metadata, CreatedAt: entry.CreatedAt,
		})
	}
	httpx.List(c, items, page, pageSize, total)
}

func (handler *adminUserHandler) sensitiveUser(c *gin.Context) (model.User, authctx.CurrentUser, bool) {
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
	if actor.Role != authctx.RoleOwner && !(actor.Role == authctx.RoleAdmin && user.Role == authctx.RoleUser) {
		httpx.Error(c, apperr.Forbidden("admin_user.target_forbidden", "不能查看该用户的安全信息"))
		return model.User{}, actor, false
	}
	return user, actor, true
}

func (handler *adminUserHandler) recordAdminAudit(
	c *gin.Context,
	db *gorm.DB,
	actor authctx.CurrentUser,
	target model.User,
	action string,
	reason string,
	metadata map[string]any,
) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["target_username"] = target.Username
	metadata["target_email"] = target.Email
	metadata["target_role"] = target.Role
	metadata["ip_address"] = requestmeta.FromGin(c).IPAddress
	actorID := actor.ID
	targetID := target.UUID
	return audit.Record(db, audit.Entry{
		ActorID: &actorID, Action: action, EntityType: "user", EntityID: &targetID,
		Reason: strings.TrimSpace(reason), Metadata: metadata,
	})
}

func adminLoginEventDTO(event model.LoginEvent) AdminLoginEventDTO {
	return AdminLoginEventDTO{
		ID: event.ID, SessionID: event.SessionID, Method: event.Method, Result: event.Result,
		FailureCode: event.FailureCode, IPAddress: event.IPAddress, IPPrefix: event.IPPrefix,
		Location: loginEventLocation(event), DeviceName: authsession.DeviceName(event.UserAgent),
		UserAgent: event.UserAgent, CreatedAt: event.CreatedAt,
	}
}

func loginEventLocation(event model.LoginEvent) string {
	location := requestmeta.Info{CountryCode: event.CountryCode, Region: event.Region, City: event.City}
	if location.CountryCode == "" || location.Region == "" || location.City == "" {
		resolved := requestmeta.FromIPAddress(event.IPAddress)
		if location.CountryCode == "" {
			location.CountryCode = resolved.CountryCode
		}
		if location.Region == "" {
			location.Region = resolved.Region
		}
		if location.City == "" {
			location.City = resolved.City
		}
	}
	return location.Location()
}

package forum

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// listGroups godoc
// @Summary 获取论坛用户组
// @Description 返回论坛用户组及成员，仅管理员可访问。
// @Tags forum
// @Produce json
// @Success 200 {object} handlers.ForumGroupListResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/groups [get]
func (h *Handler) listGroups(c *gin.Context) {
	groups, err := h.service.ListGroups(currentForumUser(c))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, groups)
}

// createGroup godoc
// @Summary 创建论坛用户组
// @Description 创建论坛权限用户组，仅管理员可访问。
// @Tags forum
// @Accept json
// @Produce json
// @Param input body handlers.ForumGroupInput true "用户组信息"
// @Success 201 {object} handlers.ForumGroupResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/groups [post]
func (h *Handler) createGroup(c *gin.Context) {
	var req UpsertForumGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	group, err := h.service.CreateGroup(currentForumUser(c), req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, group)
}

// updateGroup godoc
// @Summary 更新论坛用户组
// @Description 更新指定论坛用户组，仅管理员可访问。
// @Tags forum
// @Accept json
// @Produce json
// @Param groupID path string true "用户组 UUID"
// @Param input body handlers.ForumGroupInput true "用户组信息"
// @Success 200 {object} handlers.ForumGroupResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 409 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/groups/{groupID} [put]
func (h *Handler) updateGroup(c *gin.Context) {
	groupID, ok := parseForumUUID(c, "groupID")
	if !ok {
		return
	}
	var req UpsertForumGroupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	group, err := h.service.UpdateGroup(currentForumUser(c), groupID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, group)
}

// deleteGroup godoc
// @Summary 删除论坛用户组
// @Description 删除指定论坛用户组及其权限关系，仅管理员可访问。
// @Tags forum
// @Produce json
// @Param groupID path string true "用户组 UUID"
// @Success 200 {object} handlers.BoolStatusResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/groups/{groupID} [delete]
func (h *Handler) deleteGroup(c *gin.Context) {
	groupID, ok := parseForumUUID(c, "groupID")
	if !ok {
		return
	}
	if err := h.service.DeleteGroup(currentForumUser(c), groupID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// addGroupMember godoc
// @Summary 添加论坛用户组成员
// @Description 将用户加入指定论坛用户组，仅管理员可访问。
// @Tags forum
// @Produce json
// @Param groupID path string true "用户组 UUID"
// @Param userID path string true "用户 UUID"
// @Success 200 {object} handlers.BoolStatusResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/groups/{groupID}/members/{userID} [put]
func (h *Handler) addGroupMember(c *gin.Context) {
	groupID, userID, ok := parseGroupMemberIDs(c)
	if !ok {
		return
	}
	if err := h.service.AddGroupMember(currentForumUser(c), groupID, userID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// removeGroupMember godoc
// @Summary 移除论坛用户组成员
// @Description 将用户从指定论坛用户组移除，仅管理员可访问。
// @Tags forum
// @Produce json
// @Param groupID path string true "用户组 UUID"
// @Param userID path string true "用户 UUID"
// @Success 200 {object} handlers.BoolStatusResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/groups/{groupID}/members/{userID} [delete]
func (h *Handler) removeGroupMember(c *gin.Context) {
	groupID, userID, ok := parseGroupMemberIDs(c)
	if !ok {
		return
	}
	if err := h.service.RemoveGroupMember(currentForumUser(c), groupID, userID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// listCategoryPermissions godoc
// @Summary 获取论坛分类权限
// @Description 返回论坛分类与用户组的权限关系，仅管理员可访问。
// @Tags forum
// @Produce json
// @Param category_id query string false "分类 UUID"
// @Success 200 {object} handlers.ForumCategoryPermissionListResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/category-permissions [get]
func (h *Handler) listCategoryPermissions(c *gin.Context) {
	var categoryID uuid.UUID
	if raw := c.Query("category_id"); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "category_id must be a valid uuid"))
			return
		}
		categoryID = parsed
	}
	permissions, err := h.service.ListCategoryPermissions(currentForumUser(c), categoryID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, permissions)
}

// putCategoryPermission godoc
// @Summary 设置论坛分类权限
// @Description 创建或更新分类与用户组的权限关系，仅管理员可访问。
// @Tags forum
// @Accept json
// @Produce json
// @Param input body handlers.ForumCategoryPermissionInput true "分类权限"
// @Success 200 {object} handlers.ForumCategoryPermissionResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/category-permissions [put]
func (h *Handler) putCategoryPermission(c *gin.Context) {
	var req PutCategoryPermissionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	permission, err := h.service.PutCategoryPermission(currentForumUser(c), req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, permission)
}

// deleteCategoryPermission godoc
// @Summary 删除论坛分类权限
// @Description 删除指定分类权限关系，仅管理员可访问。
// @Tags forum
// @Produce json
// @Param permissionID path string true "分类权限 UUID"
// @Success 200 {object} handlers.BoolStatusResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/category-permissions/{permissionID} [delete]
func (h *Handler) deleteCategoryPermission(c *gin.Context) {
	permissionID, ok := parseForumUUID(c, "permissionID")
	if !ok {
		return
	}
	if err := h.service.DeleteCategoryPermission(currentForumUser(c), permissionID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func parseGroupMemberIDs(c *gin.Context) (uuid.UUID, uuid.UUID, bool) {
	groupID, ok := parseForumUUID(c, "groupID")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	userID, ok := parseForumUUID(c, "userID")
	return groupID, userID, ok
}

func parseForumUUID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", name+" must be a valid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

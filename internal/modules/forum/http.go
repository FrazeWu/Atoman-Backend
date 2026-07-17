package forum

import (
	"net/http"
	"strconv"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.GET("/categories", h.listCategories)
	group.GET("/categories/:categoryID", h.getCategory)
	group.GET("/search", h.searchTopics)
	group.GET("/topics", h.listTopics)
	group.GET("/topics/:topicID", h.getTopic)
	group.POST("/topics", h.createTopic)
	group.PUT("/topics/:topicID", h.updateTopic)
	group.DELETE("/topics/:topicID", h.deleteTopic)
	group.POST("/category-requests", h.createCategoryRequest)
	group.GET("/drafts", h.listDrafts)
	group.PUT("/drafts", h.saveDraft)
	group.DELETE("/drafts", h.deleteDraftByContext)
	group.DELETE("/drafts/:draftID", h.deleteDraft)
	group.GET("/follows", h.listFollows)
	group.PUT("/follows/:targetType", h.follow)
	group.DELETE("/follows/:targetType", h.unfollow)
	group.PUT("/follows/:targetType/:targetKey", h.follow)
	group.DELETE("/follows/:targetType/:targetKey", h.unfollow)
	group.GET("/groups", h.listGroups)
	group.POST("/groups", h.createGroup)
	group.PUT("/groups/:groupID", h.updateGroup)
	group.DELETE("/groups/:groupID", h.deleteGroup)
	group.PUT("/groups/:groupID/members/:userID", h.addGroupMember)
	group.DELETE("/groups/:groupID/members/:userID", h.removeGroupMember)
	group.GET("/category-permissions", h.listCategoryPermissions)
	group.PUT("/category-permissions", h.putCategoryPermission)
	group.DELETE("/category-permissions/:permissionID", h.deleteCategoryPermission)
	group.GET("/trust/me", h.getMyTrust)
	group.GET("/trust/users/:userID", h.getUserTrust)
	group.POST("/trust/users/:userID/evaluate", h.evaluateUserTrust)
}

type trustResponse struct {
	Level       int       `json:"level"`
	EvaluatedAt time.Time `json:"evaluated_at"`
	NextLevel   *int      `json:"next_level,omitempty"`
}

func trustResponseFromModel(trust model.ForumUserTrust) trustResponse {
	response := trustResponse{Level: trust.Level, EvaluatedAt: trust.EvaluatedAt}
	if trust.Level < 3 {
		next := trust.Level + 1
		response.NextLevel = &next
	}
	return response
}

// getMyTrust godoc
// @Summary 获取我的论坛信任等级
// @Description 重新评估并返回当前登录用户的论坛信任等级。
// @Tags forum
// @Produce json
// @Success 200 {object} handlers.ForumTrustResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/trust/me [get]
func (h *Handler) getMyTrust(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	trust, err := h.service.trust.Evaluate(user.ID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, trustResponseFromModel(trust))
}

// getUserTrust godoc
// @Summary 获取用户论坛信任等级
// @Description 返回指定用户的论坛信任等级，仅管理员和站点所有者可访问。
// @Tags forum
// @Produce json
// @Param userID path string true "用户 UUID"
// @Success 200 {object} handlers.ForumTrustResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/trust/users/{userID} [get]
func (h *Handler) getUserTrust(c *gin.Context) {
	h.handleAdminTrust(c, false)
}

// evaluateUserTrust godoc
// @Summary 重新评估用户论坛信任等级
// @Description 根据当前论坛贡献和获赞数据重新评估指定用户，仅管理员和站点所有者可访问。
// @Tags forum
// @Produce json
// @Param userID path string true "用户 UUID"
// @Success 200 {object} handlers.ForumTrustResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/trust/users/{userID}/evaluate [post]
func (h *Handler) evaluateUserTrust(c *gin.Context) {
	h.handleAdminTrust(c, true)
}

func (h *Handler) handleAdminTrust(c *gin.Context, evaluate bool) {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		httpx.Error(c, apperr.Forbidden("auth.forbidden", "Admin access required"))
		return
	}
	userID, err := uuid.Parse(c.Param("userID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "userID must be a valid uuid"))
		return
	}
	var trust model.ForumUserTrust
	if evaluate {
		trust, err = h.service.trust.Evaluate(userID)
	} else {
		trust, err = h.service.trust.Get(userID)
	}
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, trustResponseFromModel(trust))
}

func (h *Handler) listCategories(c *gin.Context) {
	categories, err := h.service.ListCategories(currentForumUser(c))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, categories)
}

func (h *Handler) getCategory(c *gin.Context) {
	categoryID, err := uuid.Parse(c.Param("categoryID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "categoryID must be a valid uuid"))
		return
	}
	category, err := h.service.GetCategory(currentForumUser(c), categoryID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, category)
}

func (h *Handler) listTopics(c *gin.Context) {
	query := ListTopicsQuery{
		Sort:     c.Query("sort"),
		Tag:      c.Query("tag"),
		Search:   c.Query("search"),
		Page:     page(c),
		PageSize: pageSize(c),
	}
	if raw := c.Query("category_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.Error(c, apperr.BadRequest("validation.invalid_request", "category_id must be a valid uuid"))
			return
		}
		query.CategoryID = id
	}
	topics, total, err := h.service.ListTopics(currentForumUser(c), query)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, topics, query.Page, query.PageSize, total)
}

// searchTopics godoc
// @Summary 搜索论坛帖子
// @Description 按关键词搜索当前用户可见的论坛帖子。
// @Tags forum
// @Produce json
// @Param q query string true "搜索关键词"
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量" default(20)
// @Success 200 {object} handlers.ForumSearchResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Router /api/v1/forum/search [get]
func (h *Handler) searchTopics(c *gin.Context) {
	query := ListTopicsQuery{
		Search:   c.Query("q"),
		Page:     page(c),
		PageSize: pageSize(c),
	}
	topics, total, err := h.service.SearchTopics(currentForumUser(c), query)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, topics, query.Page, query.PageSize, total)
}

func (h *Handler) getTopic(c *gin.Context) {
	topicID, err := uuid.Parse(c.Param("topicID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "topicID must be a valid uuid"))
		return
	}
	topic, err := h.service.GetTopic(currentForumUser(c), topicID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, topic)
}

func (h *Handler) createTopic(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateTopicRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	topic, err := h.service.CreateTopic(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, topic)
}

func (h *Handler) createCategoryRequest(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateCategoryRequestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	request, err := h.service.CreateCategoryRequest(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, request)
}

func (h *Handler) updateTopic(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	topicID, err := uuid.Parse(c.Param("topicID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "topicID must be a valid uuid"))
		return
	}
	var req UpdateTopicRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	topic, err := h.service.UpdateTopic(user, topicID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, topic)
}

func (h *Handler) deleteTopic(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	topicID, err := uuid.Parse(c.Param("topicID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "topicID must be a valid uuid"))
		return
	}
	if err := h.service.DeleteTopic(user, topicID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func currentForumUser(c *gin.Context) authctx.CurrentUser {
	user, _ := authctx.Current(c)
	return user
}

func (h *Handler) listDrafts(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	contextKey := c.Query("context_key")
	if contextKey != "" {
		draft, err := h.service.GetDraft(user, contextKey)
		if err != nil {
			httpx.Error(c, err)
			return
		}
		httpx.OK(c, http.StatusOK, draft)
		return
	}
	drafts, err := h.service.ListDrafts(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, drafts)
}

func (h *Handler) deleteDraftByContext(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if err := h.service.DeleteDraftByContext(user, c.Query("context_key")); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// listFollows godoc
// @Summary 获取论坛关注列表
// @Description 返回当前用户关注的帖子、分类和标签。
// @Tags forum
// @Produce json
// @Success 200 {object} handlers.ForumFollowListResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/follows [get]
func (h *Handler) listFollows(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	follows, err := h.service.ListFollows(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, follows)
}

// follow godoc
// @Summary 关注论坛对象
// @Description 关注指定帖子、分类或标签。
// @Tags forum
// @Produce json
// @Param targetType path string true "关注类型" Enums(topic,category,tag)
// @Param target_key query string true "帖子 UUID、分类 UUID 或标签名"
// @Success 200 {object} handlers.ForumFollowResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/follows/{targetType} [put]
func (h *Handler) follow(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	follow, err := h.service.Follow(user, c.Param("targetType"), followTargetKey(c))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, follow)
}

// unfollow godoc
// @Summary 取消关注论坛对象
// @Description 取消关注指定帖子、分类或标签。
// @Tags forum
// @Produce json
// @Param targetType path string true "关注类型" Enums(topic,category,tag)
// @Param target_key query string true "帖子 UUID、分类 UUID 或标签名"
// @Success 200 {object} handlers.BoolStatusResponse
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/follows/{targetType} [delete]
func (h *Handler) unfollow(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	if err := h.service.Unfollow(user, c.Param("targetType"), followTargetKey(c)); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func followTargetKey(c *gin.Context) string {
	if key, exists := c.GetQuery("target_key"); exists {
		return key
	}
	return c.Param("targetKey")
}

func (h *Handler) saveDraft(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req SaveDraftRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	if err := h.service.SaveDraft(user, req); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) deleteDraft(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	draftID, err := uuid.Parse(c.Param("draftID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "draftID must be a valid uuid"))
		return
	}
	if err := h.service.DeleteDraft(user, draftID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

func page(c *gin.Context) int {
	page, _ := httpx.PageParams(c)
	return page
}

func pageSize(c *gin.Context) int {
	_, size := httpx.PageParams(c)
	if c.Query("page_size") != "" || c.Query("limit") == "" {
		return size
	}
	legacy, err := strconv.Atoi(c.Query("limit"))
	if err != nil || legacy < 1 {
		return size
	}
	if legacy > 100 {
		return 100
	}
	return legacy
}

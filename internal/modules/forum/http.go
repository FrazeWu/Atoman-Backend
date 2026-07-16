package forum

import (
	"net/http"
	"strconv"

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
}

func (h *Handler) listCategories(c *gin.Context) {
	categories, err := h.service.ListCategories()
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
	category, err := h.service.GetCategory(categoryID)
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
	topics, total, err := h.service.ListTopics(query)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, topics, query.Page, query.PageSize, total)
}

func (h *Handler) searchTopics(c *gin.Context) {
	query := ListTopicsQuery{
		Search:   c.Query("q"),
		Page:     page(c),
		PageSize: pageSize(c),
	}
	topics, total, err := h.service.ListTopics(query)
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
	topic, err := h.service.GetTopic(topicID)
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

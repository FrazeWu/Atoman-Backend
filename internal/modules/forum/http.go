package forum

import (
	"net/http"

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
	group.GET("/topics", h.listTopics)
	group.GET("/topics/:topicID", h.getTopic)
	group.POST("/topics", h.createTopic)
	group.PUT("/topics/:topicID", h.updateTopic)
	group.DELETE("/topics/:topicID", h.deleteTopic)
	group.GET("/topics/:topicID/replies", h.listReplies)
	group.POST("/replies", h.createReply)
	group.PUT("/replies/:replyID", h.updateReply)
	group.DELETE("/replies/:replyID", h.deleteReply)
	group.GET("/drafts", h.listDrafts)
	group.PUT("/drafts", h.saveDraft)
	group.DELETE("/drafts/:draftID", h.deleteDraft)
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
	query := ListTopicsQuery{Page: page(c), PageSize: pageSize(c)}
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

func (h *Handler) listReplies(c *gin.Context) {
	topicID, err := uuid.Parse(c.Param("topicID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "topicID must be a valid uuid"))
		return
	}
	replies, err := h.service.ListReplies(topicID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, replies)
}

func (h *Handler) createReply(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req CreateReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	reply, err := h.service.CreateReply(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, reply)
}

func (h *Handler) updateReply(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	replyID, err := uuid.Parse(c.Param("replyID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "replyID must be a valid uuid"))
		return
	}
	var req UpdateReplyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	reply, err := h.service.UpdateReply(user, replyID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, reply)
}

func (h *Handler) deleteReply(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	replyID, err := uuid.Parse(c.Param("replyID"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "replyID must be a valid uuid"))
		return
	}
	if err := h.service.DeleteReply(user, replyID); err != nil {
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
	drafts, err := h.service.ListDrafts(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, drafts)
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
	_, pageSize := httpx.PageParams(c)
	return pageSize
}

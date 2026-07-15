package forum_moderation

import (
	"net/http"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
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
	group.POST("/topics/:topicID/close", h.lockTopic)
	group.POST("/topics/:topicID/feature", h.pinTopic)
	group.DELETE("/topics/:topicID/feature", h.unpinTopic)
	group.POST("/report", h.createReport)
	group.GET("/category-requests", h.listCategoryRequests)
	group.POST("/category-requests/:requestID/review", h.reviewCategoryRequestLegacy)
	group.POST("/topics/:topicID/lock", h.lockTopic)
	group.POST("/topics/:topicID/unlock", h.unlockTopic)
	group.POST("/topics/:topicID/pin", h.pinTopic)
	group.POST("/topics/:topicID/unpin", h.unpinTopic)
	group.POST("/topics/:topicID/hide", h.hideTopic)
	group.POST("/topics/:topicID/restore", h.restoreTopic)
	group.POST("/replies/:replyID/solve", h.solveReply)
	group.DELETE("/replies/:replyID/solve", h.unsolveReply)
	group.POST("/replies/:replyID/hide", h.hideReply)
	group.POST("/replies/:replyID/restore", h.restoreReply)
	group.GET("/reports", h.listReports)
	group.POST("/reports/:reportID/resolve", h.resolveReport)
	group.POST("/category-requests/:requestID/approve", h.approveCategoryRequest)
	group.POST("/category-requests/:requestID/reject", h.rejectCategoryRequest)
	group.GET("/moderators", h.listModeratorAssignments)
	group.POST("/moderators", h.createModeratorAssignment)
	group.PUT("/moderators/:assignmentID", h.updateModeratorAssignment)
	group.DELETE("/moderators/:assignmentID", h.deleteModeratorAssignment)
}

func (h *Handler) lockTopic(c *gin.Context) {
	h.handleTopicAction(c, h.service.LockTopic)
}

func (h *Handler) unlockTopic(c *gin.Context) {
	h.handleTopicAction(c, h.service.UnlockTopic)
}

func (h *Handler) pinTopic(c *gin.Context) {
	h.handleTopicAction(c, h.service.PinTopic)
}

func (h *Handler) unpinTopic(c *gin.Context) {
	h.handleTopicAction(c, h.service.UnpinTopic)
}

func (h *Handler) hideTopic(c *gin.Context) {
	h.handleTopicAction(c, h.service.HideTopic)
}

func (h *Handler) restoreTopic(c *gin.Context) {
	h.handleTopicAction(c, h.service.RestoreTopic)
}

func (h *Handler) hideReply(c *gin.Context) {
	h.handleReplyAction(c, h.service.HideReply)
}

func (h *Handler) restoreReply(c *gin.Context) {
	h.handleReplyAction(c, h.service.RestoreReply)
}

func (h *Handler) solveReply(c *gin.Context) {
	h.handleReplyAction(c, h.service.SolveReply)
}

func (h *Handler) unsolveReply(c *gin.Context) {
	h.handleReplyAction(c, h.service.UnsolveReply)
}

func (h *Handler) listReports(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	page, pageSize := httpx.PageParams(c)
	reports, total, err := h.service.ListReports(user, ListReportsQuery{
		Page:     page,
		PageSize: pageSize,
		Status:   c.Query("status"),
	})
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.List(c, reports, page, pageSize, total)
}

func (h *Handler) createReport(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var raw struct {
		TargetType string `json:"target_type"`
		TargetID   string `json:"target_id"`
		Reason     string `json:"reason"`
		Note       string `json:"note"`
	}
	if err := c.ShouldBindJSON(&raw); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	targetID, err := parseUUIDParam(raw.TargetID, "target_id")
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	report, err := h.service.CreateReport(user, CreateReportRequest{
		TargetType: raw.TargetType,
		TargetID:   targetID,
		Reason:     raw.Reason,
		Note:       raw.Note,
	})
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusCreated, report)
}

func (h *Handler) listCategoryRequests(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	requests, err := h.service.ListCategoryRequests(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, requests)
}

func (h *Handler) reviewCategoryRequestLegacy(c *gin.Context) {
	var req struct {
		Action     string `json:"action"`
		ReviewNote string `json:"review_note"`
		Color      string `json:"color"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	switch req.Action {
	case "approve":
		h.approveCategoryRequest(c)
	case "reject":
		h.rejectCategoryRequest(c)
	default:
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "action must be approve or reject"))
	}
}

func (h *Handler) resolveReport(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	reportID, err := parseUUIDParam(c.Param("reportID"), "reportID")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req ResolveReportRequest
	if err := c.ShouldBindJSON(&req); err != nil && c.Request.ContentLength > 0 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	report, err := h.service.ResolveReportWithNote(user, reportID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, report)
}

func (h *Handler) approveCategoryRequest(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	requestID, err := parseUUIDParam(c.Param("requestID"), "requestID")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req ReviewCategoryRequestInput
	if err := c.ShouldBindJSON(&req); err != nil && c.Request.ContentLength > 0 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	reviewed, category, err := h.service.ApproveCategoryRequest(user, requestID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OKMeta(c, http.StatusOK, reviewed, gin.H{"category": category})
}

func (h *Handler) rejectCategoryRequest(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	requestID, err := parseUUIDParam(c.Param("requestID"), "requestID")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req ReviewCategoryRequestInput
	if err := c.ShouldBindJSON(&req); err != nil && c.Request.ContentLength > 0 {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	reviewed, err := h.service.RejectCategoryRequest(user, requestID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, reviewed)
}

// listModeratorAssignments godoc
// @Summary 获取论坛版主分配列表
// @Description 返回论坛版主分配、分类范围和权限位，仅管理员可访问。
// @Tags forum-moderation
// @Produce json
// @Success 200 {array} model.ForumModeratorAssignment
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/moderation/moderators [get]
func (h *Handler) listModeratorAssignments(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	assignments, err := h.service.ListModeratorAssignments(user)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, assignments)
}

// createModeratorAssignment godoc
// @Summary 创建论坛版主分配
// @Description 为指定 moderator/admin 用户创建论坛版主分配，仅管理员可访问。
// @Tags forum-moderation
// @Accept json
// @Produce json
// @Param input body ModeratorAssignmentInput true "版主分配输入"
// @Success 201 {object} model.ForumModeratorAssignment
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/moderation/moderators [post]
func (h *Handler) createModeratorAssignment(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	var req ModeratorAssignmentInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	assignment, err := h.service.CreateModeratorAssignment(user, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, assignment)
}

// updateModeratorAssignment godoc
// @Summary 更新论坛版主分配
// @Description 更新指定论坛版主分配的分类范围和权限位，仅管理员可访问。
// @Tags forum-moderation
// @Accept json
// @Produce json
// @Param assignmentID path string true "版主分配 UUID"
// @Param input body ModeratorAssignmentInput true "版主分配输入"
// @Success 200 {object} model.ForumModeratorAssignment
// @Failure 400 {object} handlers.ErrorResponse
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/moderation/moderators/{assignmentID} [put]
func (h *Handler) updateModeratorAssignment(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	assignmentID, err := parseUUIDParam(c.Param("assignmentID"), "assignmentID")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req ModeratorAssignmentInput
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	assignment, err := h.service.UpdateModeratorAssignment(user, assignmentID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, assignment)
}

// deleteModeratorAssignment godoc
// @Summary 删除论坛版主分配
// @Description 删除指定论坛版主分配，仅管理员可访问。
// @Tags forum-moderation
// @Produce json
// @Param assignmentID path string true "版主分配 UUID"
// @Success 204 {string} string ""
// @Failure 401 {object} handlers.ErrorResponse
// @Failure 403 {object} handlers.ErrorResponse
// @Failure 404 {object} handlers.ErrorResponse
// @Failure 500 {object} handlers.ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/forum/moderation/moderators/{assignmentID} [delete]
func (h *Handler) deleteModeratorAssignment(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	assignmentID, err := parseUUIDParam(c.Param("assignmentID"), "assignmentID")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	if err := h.service.DeleteModeratorAssignment(user, assignmentID); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) handleTopicAction(c *gin.Context, fn func(authctx.CurrentUser, uuid.UUID) (model.ForumTopic, error)) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	topicID, err := parseUUIDParam(c.Param("topicID"), "topicID")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	result, err := fn(user, topicID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

func (h *Handler) handleReplyAction(c *gin.Context, fn func(authctx.CurrentUser, uuid.UUID) (model.ForumReply, error)) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	replyID, err := parseUUIDParam(c.Param("replyID"), "replyID")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	result, err := fn(user, replyID)
	if err != nil {
		httpx.Error(c, comment.AppError(err))
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

func parseUUIDParam(raw string, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperr.BadRequest("validation.invalid_request", name+" must be a valid uuid")
	}
	return id, nil
}

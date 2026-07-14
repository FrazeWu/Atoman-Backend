package comment

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/middleware"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type HTTP struct{ service *Service }

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &HTTP{service: service}
	group.GET("/discussions/:kind/:resource_id/comments", middleware.OptionalAuthMiddleware(), h.list)
	group.GET("/comments/:root_comment_id/replies", middleware.OptionalAuthMiddleware(), h.listReplies)

	mutations := group.Group("")
	mutations.Use(middleware.AuthMiddleware())
	mutations.POST("/discussions/:kind/:resource_id/comments", h.create)
	mutations.PATCH("/comments/:comment_id", h.edit)
	mutations.DELETE("/comments/:comment_id", h.delete)
	mutations.PUT("/comments/:comment_id/like", h.like)
	mutations.DELETE("/comments/:comment_id/like", h.unlike)
	mutations.PUT("/comments/:comment_id/report", h.report)
	mutations.PUT("/discussions/:kind/:resource_id/pinned-comment", h.mark)
	mutations.DELETE("/discussions/:kind/:resource_id/pinned-comment", h.unmark)
	mutations.GET("/admin/comment-reports", h.listReports)
	mutations.PUT("/admin/comments/:comment_id/moderation", h.moderate)
}

func currentUser(c *gin.Context) authctx.CurrentUser {
	user, _ := authctx.Current(c)
	return user
}

func currentViewer(c *gin.Context) Viewer {
	user, ok := authctx.Current(c)
	if !ok || user.ID == uuid.Nil {
		return Viewer{}
	}
	return Viewer{UserID: &user.ID}
}

func parseTarget(c *gin.Context) (TargetRef, error) {
	id, err := uuid.Parse(c.Param("resource_id"))
	if err != nil || id == uuid.Nil {
		return TargetRef{}, ErrInvalidTargetResource
	}
	return TargetRef{Kind: strings.TrimSpace(c.Param("kind")), ResourceID: id}, nil
}

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil || id == uuid.Nil {
		return uuid.Nil, ErrInvalidTargetResource
	}
	return id, nil
}

func parsePage(raw string, fallback, capValue int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, ErrInvalidListOptions
	}
	if value > capValue {
		value = capValue
	}
	return value, nil
}

func decodeJSONStrict(reader io.Reader, output any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func writeCommentError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "comment.internal_error", "Internal server error"
	switch {
	case errors.Is(err, ErrAuthenticationRequired):
		status, code, message = 401, "comment.authentication_required", "Authentication is required"
	case errors.Is(err, ErrTargetNotVisible), errors.Is(err, ErrCommentForbidden):
		status, code, message = 403, "comment.forbidden", "Comment operation is forbidden"
	case errors.Is(err, ErrTargetNotFound):
		status, code, message = 404, "comment.target_not_found", "Discussion target not found"
	case errors.Is(err, ErrCommentNotFound):
		status, code, message = 404, "comment.not_found", "Comment not found"
	case errors.Is(err, ErrCommentRateLimited):
		status, code, message = 429, "comment.rate_limited", "Comment rate limit exceeded"
	case errors.Is(err, ErrDuplicateComment):
		status, code, message = 409, "comment.duplicate", "Duplicate comment"
	case errors.Is(err, ErrUnknownTargetKind), errors.Is(err, ErrInvalidTargetResource):
		status, code, message = 400, "comment.invalid_target", "Invalid discussion target"
	case errors.Is(err, ErrInvalidContent):
		status, code, message = 400, "comment.invalid_content", "Invalid comment content"
	case errors.Is(err, ErrInvalidReply):
		status, code, message = 400, "comment.invalid_reply", "Invalid comment reply"
	case errors.Is(err, ErrInvalidAttachment):
		status, code, message = 400, "comment.invalid_attachment", "Invalid comment attachment"
	case errors.Is(err, ErrInvalidMention):
		status, code, message = 400, "comment.invalid_mention", "Invalid comment mention"
	case errors.Is(err, ErrInvalidReport):
		status, code, message = 400, "comment.invalid_report", "Invalid comment report"
	case errors.Is(err, ErrInvalidModeration):
		status, code, message = 400, "comment.invalid_moderation", "Invalid moderation action"
	case errors.Is(err, ErrInvalidMark):
		status, code, message = 400, "comment.invalid_mark", "Invalid marked comment"
	case errors.Is(err, ErrInvalidListOptions):
		status, code, message = 400, "comment.invalid_list", "Invalid list options"
	}
	httpx.Error(c, apperr.New(status, code, message, nil))
}

func writeInvalidJSON(c *gin.Context) {
	httpx.Error(c, apperr.BadRequest("comment.invalid_json", "Invalid request body"))
}

// list godoc
// @Summary 获取评论楼层
// @Description 按目标获取一级评论与前三条楼中楼回复。
// @Tags comments
// @Produce json
// @Param kind path string true "目标类型"
// @Param resource_id path string true "目标公开 UUID"
// @Param sort query string false "排序：oldest/newest/hot" default(oldest)
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量，最多20" default(20)
// @Success 200 {object} CommentListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/discussions/{kind}/{resource_id}/comments [get]
func (h *HTTP) list(c *gin.Context) {
	target, err := parseTarget(c)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	page, err := parsePage(c.Query("page"), 1, int(^uint(0)>>1))
	if err != nil {
		writeCommentError(c, err)
		return
	}
	pageSize, err := parsePage(c.Query("page_size"), 20, 20)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	_ = pageSize
	sort := c.Query("sort")
	result, err := h.service.List(currentUser(c), target, ListCommentsInput{Page: page, Sort: sort})
	if err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// listReplies godoc
// @Summary 获取楼中楼回复
// @Tags comments
// @Produce json
// @Param root_comment_id path string true "父楼 UUID"
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量，最多50" default(20)
// @Success 200 {object} ReplyListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Router /api/v1/comments/{root_comment_id}/replies [get]
func (h *HTTP) listReplies(c *gin.Context) {
	id, err := parseUUIDParam(c, "root_comment_id")
	if err != nil {
		writeCommentError(c, ErrInvalidReply)
		return
	}
	page, err := parsePage(c.Query("page"), 1, int(^uint(0)>>1))
	if err != nil {
		writeCommentError(c, err)
		return
	}
	pageSize, err := parsePage(c.Query("page_size"), 20, 50)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	result, err := h.service.ListReplies(currentViewer(c), id, page, pageSize)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// create godoc
// @Summary 发表评论或回复
// @Tags comments
// @Accept json
// @Produce json
// @Param kind path string true "目标类型"
// @Param resource_id path string true "目标公开 UUID"
// @Param body body CreateCommentInput true "评论内容"
// @Success 201 {object} CommentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Failure 429 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/discussions/{kind}/{resource_id}/comments [post]
func (h *HTTP) create(c *gin.Context) {
	target, err := parseTarget(c)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	var input CreateCommentInput
	if err := decodeJSONStrict(c.Request.Body, &input); err != nil {
		writeInvalidJSON(c)
		return
	}
	result, err := h.service.Create(currentUser(c), target, input)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, result)
}

// edit godoc
// @Summary 编辑评论
// @Tags comments
// @Accept json
// @Produce json
// @Param comment_id path string true "评论 UUID"
// @Param body body EditCommentInput true "评论内容"
// @Success 200 {object} CommentResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/comments/{comment_id} [patch]
func (h *HTTP) edit(c *gin.Context) {
	id, err := parseUUIDParam(c, "comment_id")
	if err != nil {
		writeCommentError(c, ErrInvalidContent)
		return
	}
	var input EditCommentInput
	if err := decodeJSONStrict(c.Request.Body, &input); err != nil {
		writeInvalidJSON(c)
		return
	}
	result, err := h.service.Edit(currentUser(c), id, input)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// delete godoc
// @Summary 删除评论
// @Tags comments
// @Produce json
// @Param comment_id path string true "评论 UUID"
// @Success 200 {object} ActionResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/comments/{comment_id} [delete]
func (h *HTTP) delete(c *gin.Context) {
	h.commentAction(c, func(user authctx.CurrentUser, id uuid.UUID) error { return h.service.Delete(user, id) })
}

// like godoc
// @Summary 点赞评论
// @Tags comments
// @Produce json
// @Param comment_id path string true "评论 UUID"
// @Success 200 {object} ActionResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/comments/{comment_id}/like [put]
func (h *HTTP) like(c *gin.Context) {
	h.commentAction(c, func(user authctx.CurrentUser, id uuid.UUID) error { return h.service.Like(user, id) })
}

// unlike godoc
// @Summary 取消点赞评论
// @Tags comments
// @Produce json
// @Param comment_id path string true "评论 UUID"
// @Success 200 {object} ActionResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/comments/{comment_id}/like [delete]
func (h *HTTP) unlike(c *gin.Context) {
	h.commentAction(c, func(user authctx.CurrentUser, id uuid.UUID) error { return h.service.Unlike(user, id) })
}
func (h *HTTP) commentAction(c *gin.Context, action func(authctx.CurrentUser, uuid.UUID) error) {
	id, err := parseUUIDParam(c, "comment_id")
	if err != nil {
		writeCommentError(c, ErrCommentNotFound)
		return
	}
	if err := action(currentUser(c), id); err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// report godoc
// @Summary 举报评论
// @Tags comments
// @Accept json
// @Produce json
// @Param comment_id path string true "评论 UUID"
// @Param body body ReportInput true "举报信息"
// @Success 200 {object} ActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/comments/{comment_id}/report [put]
func (h *HTTP) report(c *gin.Context) {
	id, err := parseUUIDParam(c, "comment_id")
	if err != nil {
		writeCommentError(c, ErrInvalidReport)
		return
	}
	var input ReportInput
	if err := decodeJSONStrict(c.Request.Body, &input); err != nil {
		writeInvalidJSON(c)
		return
	}
	if err := h.service.Report(currentUser(c), id, input); err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// mark godoc
// @Summary 标记父楼
// @Description 内容作者可置顶；论坛作者可标记最佳回答。
// @Tags comments
// @Accept json
// @Produce json
// @Param kind path string true "目标类型"
// @Param resource_id path string true "目标公开 UUID"
// @Param body body PinCommentInput true "父楼 UUID"
// @Success 200 {object} ActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/discussions/{kind}/{resource_id}/pinned-comment [put]
func (h *HTTP) mark(c *gin.Context) {
	target, err := parseTarget(c)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	var input PinCommentInput
	if err := decodeJSONStrict(c.Request.Body, &input); err != nil || input.CommentID == uuid.Nil {
		writeInvalidJSON(c)
		return
	}
	if err := h.service.Mark(currentUser(c), target, input.CommentID); err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// unmark godoc
// @Summary 取消父楼标记
// @Tags comments
// @Produce json
// @Param kind path string true "目标类型"
// @Param resource_id path string true "目标公开 UUID"
// @Success 200 {object} ActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/discussions/{kind}/{resource_id}/pinned-comment [delete]
func (h *HTTP) unmark(c *gin.Context) {
	target, err := parseTarget(c)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	if err := h.service.Unmark(currentUser(c), target); err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

// listReports godoc
// @Summary 获取评论举报队列
// @Tags comment-admin
// @Produce json
// @Param status query string false "状态：pending/upheld/rejected"
// @Param page query int false "页码" default(1)
// @Param page_size query int false "每页数量，最多50" default(20)
// @Success 200 {object} ReportQueueResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/comment-reports [get]
func (h *HTTP) listReports(c *gin.Context) {
	page, err := parsePage(c.Query("page"), 1, int(^uint(0)>>1))
	if err != nil {
		writeCommentError(c, err)
		return
	}
	pageSize, err := parsePage(c.Query("page_size"), 20, 50)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	result, err := h.service.ListReports(currentUser(c), c.Query("status"), page, pageSize)
	if err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// moderate godoc
// @Summary 审核评论或举报
// @Tags comment-admin
// @Accept json
// @Produce json
// @Param comment_id path string true "评论 UUID"
// @Param body body ModerateInput true "审核动作"
// @Success 200 {object} ActionResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/comments/{comment_id}/moderation [put]
func (h *HTTP) moderate(c *gin.Context) {
	id, err := parseUUIDParam(c, "comment_id")
	if err != nil {
		writeCommentError(c, ErrInvalidModeration)
		return
	}
	var input ModerateInput
	if err := decodeJSONStrict(c.Request.Body, &input); err != nil {
		writeInvalidJSON(c)
		return
	}
	if err := h.service.Moderate(currentUser(c), id, input); err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

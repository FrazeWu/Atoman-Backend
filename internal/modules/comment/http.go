package comment

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"atoman/internal/middleware"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type HTTP struct{ service *Service }

var (
	ErrInvalidJSON          = errors.New("invalid comment JSON")
	ErrUnsupportedMediaType = errors.New("unsupported comment media type")
	ErrRequestTooLarge      = errors.New("comment request too large")
)

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &HTTP{service: service}
	group.GET("/discussions/:kind/:resource_id/comments", middleware.OptionalAuthMiddleware(), h.list)
	group.GET("/comments/:root_comment_id/replies", middleware.OptionalAuthMiddleware(), h.listReplies)

	mutations := group.Group("")
	mutations.Use(middleware.StableAuthMiddleware())
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
	user, _ := authctx.Current(c)
	return viewerFromUser(user)
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
		return uuid.Nil, ErrInvalidCommentID
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

func decodeJSONStrict(c *gin.Context, output any) error {
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/json" && !(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json")) {
		return ErrUnsupportedMediaType
	}
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrRequestTooLarge
		}
		return ErrInvalidJSON
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrRequestTooLarge
		}
		return ErrInvalidJSON
	}
	return nil
}

func writeCommentError(c *gin.Context, err error) {
	httpx.Error(c, AppError(err))
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
	sort := c.Query("sort")
	result, err := h.service.List(currentUser(c), target, ListCommentsInput{Page: page, PageSize: pageSize, Sort: sort})
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
		writeCommentError(c, err)
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
// @Failure 413 {object} ErrorResponse
// @Failure 415 {object} ErrorResponse
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
	if err := decodeJSONStrict(c, &input); err != nil {
		writeCommentError(c, err)
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
// @Failure 413 {object} ErrorResponse
// @Failure 415 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/comments/{comment_id} [patch]
func (h *HTTP) edit(c *gin.Context) {
	id, err := parseUUIDParam(c, "comment_id")
	if err != nil {
		writeCommentError(c, err)
		return
	}
	var input EditCommentInput
	if err := decodeJSONStrict(c, &input); err != nil {
		writeCommentError(c, err)
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
		writeCommentError(c, err)
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
// @Failure 413 {object} ErrorResponse
// @Failure 415 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/comments/{comment_id}/report [put]
func (h *HTTP) report(c *gin.Context) {
	id, err := parseUUIDParam(c, "comment_id")
	if err != nil {
		writeCommentError(c, err)
		return
	}
	var input ReportInput
	if err := decodeJSONStrict(c, &input); err != nil {
		writeCommentError(c, err)
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
// @Failure 413 {object} ErrorResponse
// @Failure 415 {object} ErrorResponse
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
	if err := decodeJSONStrict(c, &input); err != nil {
		writeCommentError(c, err)
		return
	}
	if input.CommentID == uuid.Nil {
		writeCommentError(c, ErrInvalidJSON)
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
// @Failure 413 {object} ErrorResponse
// @Failure 415 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/admin/comments/{comment_id}/moderation [put]
func (h *HTTP) moderate(c *gin.Context) {
	id, err := parseUUIDParam(c, "comment_id")
	if err != nil {
		writeCommentError(c, err)
		return
	}
	var input ModerateInput
	if err := decodeJSONStrict(c, &input); err != nil {
		writeCommentError(c, err)
		return
	}
	if err := h.service.Moderate(currentUser(c), id, input); err != nil {
		writeCommentError(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"ok": true})
}

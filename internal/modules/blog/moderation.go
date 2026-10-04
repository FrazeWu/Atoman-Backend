package blog

import (
	"net/http"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var blogReportReasons = map[string]struct{}{
	"spam": {}, "harassment": {}, "copyright": {}, "misinformation": {}, "other": {},
}

type blogPostReportInput struct {
	Reason string `json:"reason" binding:"required"`
	Note   string `json:"note"`
}

type blogPostAppealInput struct {
	Reason string `json:"reason" binding:"required"`
}

type blogPostModerationInput struct {
	Action string `json:"action" binding:"required"`
}

// reportBlogPost godoc
// @Summary 举报博客文章
// @Tags blog
// @Accept json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "文章 UUID"
// @Param input body blogPostReportInput true "举报信息"
// @Success 201 {object} model.BlogPostReport
// @Router /api/v1/blog/posts/{id}/reports [post]
func (h *Handler) reportBlogPost(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	content, _, loaded := h.loadPublicTagContent(c)
	if !loaded {
		return
	}
	if content.UserID == user.ID {
		httpx.Error(c, apperr.BadRequest("blog.report_own_post", "cannot report your own post"))
		return
	}
	var input blogPostReportInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	reason := strings.ToLower(strings.TrimSpace(input.Reason))
	if _, valid := blogReportReasons[reason]; !valid {
		httpx.Error(c, apperr.BadRequest("blog.invalid_report_reason", "unsupported report reason"))
		return
	}
	var report model.BlogPostReport
	if err := h.service.db.Where("content_id = ? AND reporter_id = ?", content.ID, user.ID).First(&report).Error; err == nil {
		httpx.Error(c, apperr.Conflict("blog.report_exists", "you already reported this post"))
		return
	} else if err != nil && err != gorm.ErrRecordNotFound {
		httpx.Error(c, err)
		return
	}
	report = model.BlogPostReport{ContentID: content.ID, ReporterID: user.ID, Reason: reason, Note: strings.TrimSpace(input.Note), Status: "pending"}
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&report).Error; err != nil {
			return err
		}
		var reporters int64
		if err := tx.Model(&model.BlogPostReport{}).Where("content_id = ? AND status = ? AND created_at >= ?", content.ID, "pending", time.Now().UTC().Add(-7*24*time.Hour)).Distinct("reporter_id").Count(&reporters).Error; err != nil {
			return err
		}
		if reporters >= 3 {
			return tx.Model(&model.ContentEntry{}).Where("id = ? AND status = ?", content.ID, "published").Update("status", "moderated_hidden").Error
		}
		return nil
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, report)
}

// appealBlogPost godoc
// @Summary 申诉被隐藏的博客文章
// @Tags blog
// @Accept json
// @Security BearerAuth
// @Security CookieAuth
// @Param id path string true "文章 UUID"
// @Param input body blogPostAppealInput true "申诉信息"
// @Success 201 {object} model.BlogPostAppeal
// @Router /api/v1/blog/posts/{id}/appeals [post]
func (h *Handler) appealBlogPost(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	id, err := parsePostID(c.Param("id"))
	if err != nil {
		httpx.Error(c, err)
		return
	}
	content, err := loadCanonicalBlogContent(h.service.db, id)
	if err != nil || content.UserID != user.ID {
		httpx.Error(c, apperr.NotFound("blog.post_not_found", "Post not found"))
		return
	}
	var input blogPostAppealInput
	if err := bindJSON(c, &input); err != nil || strings.TrimSpace(input.Reason) == "" {
		httpx.Error(c, apperr.BadRequest("blog.invalid_appeal", "appeal reason is required"))
		return
	}
	var existing model.BlogPostAppeal
	if err := h.service.db.Where("content_id = ? AND author_id = ?", id, user.ID).First(&existing).Error; err == nil {
		httpx.Error(c, apperr.Conflict("blog.appeal_exists", "only one appeal is allowed"))
		return
	}
	appeal := model.BlogPostAppeal{ContentID: id, AuthorID: user.ID, Reason: strings.TrimSpace(input.Reason), Status: "pending"}
	if err := h.service.db.Create(&appeal).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, appeal)
}

// listBlogPostReports godoc
// @Summary 获取博客文章举报队列
// @Tags blog
// @Security BearerAuth
// @Security CookieAuth
// @Success 200 {array} model.BlogPostReport
// @Router /api/v1/blog/admin/post-reports [get]
func (h *Handler) listBlogPostReports(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		httpx.Error(c, apperr.Forbidden("blog.moderation_forbidden", "moderator access required"))
		return
	}
	var reports []model.BlogPostReport
	if err := h.service.db.Order("created_at ASC").Limit(100).Find(&reports).Error; err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, reports)
}

// moderateBlogPostReport godoc
// @Summary 审核博客文章举报
// @Tags blog
// @Accept json
// @Security BearerAuth
// @Security CookieAuth
// @Param report_id path string true "举报 UUID"
// @Param input body blogPostModerationInput true "审核动作"
// @Success 204
// @Router /api/v1/blog/admin/post-reports/{report_id}/moderation [put]
func (h *Handler) moderateBlogPostReport(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok || !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		httpx.Error(c, apperr.Forbidden("blog.moderation_forbidden", "moderator access required"))
		return
	}
	reportID, err := uuid.Parse(c.Param("report_id"))
	if err != nil || reportID == uuid.Nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "report_id must be a valid UUID"))
		return
	}
	var input blogPostModerationInput
	if err := bindJSON(c, &input); err != nil {
		httpx.Error(c, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "restore" && action != "archive" && action != "reject" {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "action must be restore, archive, or reject"))
		return
	}
	if err := h.service.db.Transaction(func(tx *gorm.DB) error {
		var report model.BlogPostReport
		if err := tx.First(&report, "id = ?", reportID).Error; err != nil {
			return apperr.NotFound("blog.report_not_found", "report not found")
		}
		now := time.Now().UTC()
		status := "rejected"
		if action == "restore" {
			status = "upheld"
		}
		if err := tx.Model(&report).Updates(map[string]any{"status": status, "reviewer_id": user.ID, "reviewed_at": now}).Error; err != nil {
			return err
		}
		if action == "restore" {
			return tx.Model(&model.ContentEntry{}).Where("id = ?", report.ContentID).Update("status", "published").Error
		}
		if action == "archive" {
			return tx.Model(&model.ContentEntry{}).Where("id = ?", report.ContentID).Update("status", "archived").Error
		}
		return nil
	}); err != nil {
		httpx.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

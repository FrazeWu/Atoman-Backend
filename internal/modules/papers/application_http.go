package papers

import (
	"net/http"
	"strconv"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
)

// @Summary 查看当前用户的 Papers 申请
// @Tags papers
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/application [get]
func (h *handler) application(c *gin.Context) {
	u, ok := current(c)
	if !ok {
		return
	}
	a, err := h.service.Application(u)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"application": a})
}

type applyInput struct {
	Reason string `json:"reason"`
}

// @Summary 提交 Papers 使用申请
// @Tags papers
// @Security BearerAuth
// @Accept json
// @Param input body applyInput true "Application"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/papers/application [post]
func (h *handler) apply(c *gin.Context) {
	u, ok := current(c)
	if !ok {
		return
	}
	var in applyInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	if !httpx.BindRequiredJSON(c, &in) {
		return
	}
	a, err := h.service.Apply(u, in.Reason)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, a)
}

// @Summary 管理员查看 Papers 申请列表
// @Tags papers
// @Security BearerAuth
// @Param status query string false "Status"
// @Param page query int false "Page"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/admin/applications [get]
func (h *handler) applications(c *gin.Context) {
	u, ok := current(c)
	if !ok {
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("papers.invalid_page", "Invalid page"))
		return
	}
	rows, err := h.service.Applications(u, c.DefaultQuery("status", "pending"), page)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, rows)
}

type reviewInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// @Summary 管理员审核 Papers 使用申请
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Application UUID"
// @Param input body reviewInput true "Review"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/admin/applications/{id}/review [post]
func (h *handler) reviewApplication(c *gin.Context) {
	u, ok := current(c)
	if !ok {
		return
	}
	id, ok := httpx.UUIDParam(c, "id")
	if !ok {
		return
	}
	var in reviewInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	if !httpx.BindRequiredJSON(c, &in) {
		return
	}
	a, err := h.service.ReviewApplication(u, id, in.Decision, in.Note)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, a)
}

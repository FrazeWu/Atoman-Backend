package papers

import (
	"net/http"

	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

type submitCompileInput struct {
	BaseRevision   int64  `json:"base_revision"`
	IdempotencyKey string `json:"idempotency_key"`
}

// @Summary Check whether the isolated LaTeX compile service is available
// @Tags papers
// @Security BearerAuth
// @Router /api/v1/papers/projects/{id}/compile-capability [get]
func (h *handler) compileCapability(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	projectID, ok := projectID(c)
	if !ok {
		return
	}
	enabled, err := h.service.CompileCapability(user, projectID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"enabled": enabled, "pdf_available": enabled && h.service.compile.Results != nil})
}

// @Summary Enqueue compilation of one consistent project revision
// @Tags papers
// @Security BearerAuth
// @Accept json
// @Produce json
// @Router /api/v1/papers/projects/{id}/compiles [post]
func (h *handler) submitCompile(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	projectID, ok := projectID(c)
	if !ok {
		return
	}
	var input submitCompileInput
	if !httpx.BindRequiredJSON(c, &input) {
		return
	}
	job, err := h.service.SubmitCompile(c.Request.Context(), user, projectID, input.BaseRevision, input.IdempotencyKey)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusAccepted, job)
}

// @Summary List ten most recent compilation jobs owned by the project owner
// @Tags papers
// @Security BearerAuth
// @Router /api/v1/papers/projects/{id}/compiles [get]
func (h *handler) compileHistory(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	projectID, ok := projectID(c)
	if !ok {
		return
	}
	jobs, err := h.service.CompileHistory(c.Request.Context(), user, projectID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, jobs)
}

// @Summary Read sanitized compilation status for a job owned by the caller
// @Tags papers
// @Security BearerAuth
// @Router /api/v1/papers/projects/{id}/compiles/{jobId} [get]
func (h *handler) compileStatus(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	projectID, ok := projectID(c)
	if !ok {
		return
	}
	jobID, ok := httpx.UUIDParam(c, "jobId")
	if !ok {
		return
	}
	job, err := h.service.CompileStatus(c.Request.Context(), user, projectID, jobID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, job)
}

// @Summary Request cancellation; never directly kills a sandbox
// @Tags papers
// @Security BearerAuth
// @Router /api/v1/papers/projects/{id}/compiles/{jobId} [delete]
func (h *handler) cancelCompile(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	projectID, ok := projectID(c)
	if !ok {
		return
	}
	jobID, ok := httpx.UUIDParam(c, "jobId")
	if !ok {
		return
	}
	job, err := h.service.CancelCompile(c.Request.Context(), user, projectID, jobID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, job)
}

// @Summary Download a previously verified, durable, owner-scoped compile PDF
// @Tags papers
// @Security BearerAuth
// @Router /api/v1/papers/projects/{id}/compiles/{jobId}/pdf [get]
func (h *handler) compilePDF(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	projectID, ok := projectID(c)
	if !ok {
		return
	}
	jobID, ok := httpx.UUIDParam(c, "jobId")
	if !ok {
		return
	}
	c.Header("Cache-Control", "private, no-store")
	raw, err := h.service.CompilePDF(c.Request.Context(), user, projectID, jobID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="paper.pdf"`)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'")
	c.Data(http.StatusOK, "application/pdf", raw)
}

// @Summary Read private SyncTeX line positions for an owned verified PDF
// @Tags papers
// @Security BearerAuth
// @Router /api/v1/papers/projects/{id}/compiles/{jobId}/source-map [get]
func (h *handler) compileSourceMap(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	project, ok := projectID(c)
	if !ok {
		return
	}
	job, ok := httpx.UUIDParam(c, "jobId")
	if !ok {
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	raw, err := h.service.CompileSourceMap(c.Request.Context(), user, project, job)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

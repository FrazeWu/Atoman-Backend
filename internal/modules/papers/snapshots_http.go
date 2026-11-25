package papers

import (
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

type snapshotCreateInput struct {
	Name         string `json:"name"`
	BaseRevision int64  `json:"base_revision"`
}
type snapshotRestoreInput struct {
	BaseRevision int64 `json:"base_revision"`
}

// @Summary List private project checkpoints
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/snapshots [get]
func (h *handler) projectSnapshots(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	rows, err := h.service.ListProjectSnapshots(user, pid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, rows)
}

// @Summary Create named project checkpoint
// @Tags papers
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Project UUID"
// @Param input body snapshotCreateInput true "Name and saved project revision"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/snapshots [post]
func (h *handler) createSnapshot(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var in snapshotCreateInput
	if !httpx.BindRequiredJSON(c, &in) {
		return
	}
	result, err := h.service.CreateProjectSnapshot(user, pid, in.Name, in.BaseRevision)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, result)
}

// @Summary Read checkpoint file metadata
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Param snapshotId path string true "Checkpoint UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/snapshots/{snapshotId} [get]
func (h *handler) snapshotDetail(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	sid, ok := httpx.UUIDParam(c, "snapshotId")
	if !ok {
		return
	}
	result, err := h.service.GetProjectSnapshot(user, pid, sid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

// @Summary Remove checkpoint reference without deleting file history
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Project UUID"
// @Param snapshotId path string true "Checkpoint UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/snapshots/{snapshotId} [delete]
func (h *handler) deleteSnapshot(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	sid, ok := httpx.UUIDParam(c, "snapshotId")
	if !ok {
		return
	}
	if err := h.service.DeleteProjectSnapshot(user, pid, sid); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, map[string]bool{"deleted": true})
}

// @Summary Atomically restore checkpoint and retain a safety checkpoint
// @Tags papers
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Project UUID"
// @Param snapshotId path string true "Checkpoint UUID"
// @Param input body snapshotRestoreInput true "Saved project revision"
// @Success 200 {object} map[string]interface{}
// @Failure 409 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/snapshots/{snapshotId}/restore [post]
func (h *handler) restoreSnapshot(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	sid, ok := httpx.UUIDParam(c, "snapshotId")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var in snapshotRestoreInput
	if !httpx.BindRequiredJSON(c, &in) {
		return
	}
	result, err := h.service.RestoreProjectSnapshot(user, pid, sid, in.BaseRevision)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, result)
}

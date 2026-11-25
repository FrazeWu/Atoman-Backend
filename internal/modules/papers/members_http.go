package papers

import (
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

type memberInput struct {
	Role string `json:"role"`
}

// @Summary List project members (owner only)
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/members [get]
func (h *handler) members(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	out, err := h.service.ListMembers(user, pid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

// @Summary Grant or change private project membership (owner only)
// @Tags papers
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Project UUID"
// @Param userId path string true "Eligible account UUID"
// @Param input body memberInput true "editor or viewer"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/members/{userId} [put]
func (h *handler) setMember(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	target, ok := httpx.UUIDParam(c, "userId")
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var in memberInput
	if !httpx.BindRequiredJSON(c, &in) {
		return
	}
	out, err := h.service.SetMember(user, pid, target, in.Role)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

// @Summary Revoke private project membership (owner only)
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Param userId path string true "Account UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/members/{userId} [delete]
func (h *handler) removeMember(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	target, ok := httpx.UUIDParam(c, "userId")
	if !ok {
		return
	}
	if err := h.service.RemoveMember(user, pid, target); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, map[string]bool{"deleted": true})
}

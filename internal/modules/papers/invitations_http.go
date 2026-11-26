package papers

import (
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

type invitationAcceptInput struct {
	Token string `json:"token"`
}

// @Summary List active project invitation links (owner only, no tokens)
// @Tags papers
// @Security BearerAuth
// @Produce json
// @Param id path string true "Project UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/invitations [get]
func (h *handler) invitations(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	out, err := h.service.ListInvitations(user, pid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

// @Summary Create a seven-day single-redemption project invitation (owner only)
// @Tags papers
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param id path string true "Project UUID"
// @Param input body memberInput true "editor or viewer"
// @Success 201 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/invitations [post]
func (h *handler) createInvitation(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var in memberInput
	if !httpx.BindRequiredJSON(c, &in) {
		return
	}
	out, err := h.service.CreateInvitation(user, pid, in.Role)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusCreated, out)
}

// @Summary Revoke an unused project invitation (owner only)
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Project UUID"
// @Param invitationId path string true "Invitation UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/invitations/{invitationId} [delete]
func (h *handler) revokeInvitation(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	iid, ok := httpx.UUIDParam(c, "invitationId")
	if !ok {
		return
	}
	if err := h.service.RevokeInvitation(user, pid, iid); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, map[string]bool{"revoked": true})
}

// @Summary Accept a private project invitation (eligible authenticated account)
// @Tags papers
// @Security BearerAuth
// @Accept json
// @Produce json
// @Param input body invitationAcceptInput true "Opaque invitation token"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/invitations/accept [post]
func (h *handler) acceptInvitation(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var in invitationAcceptInput
	if !httpx.BindRequiredJSON(c, &in) {
		return
	}
	out, err := h.service.AcceptInvitation(user, in.Token)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

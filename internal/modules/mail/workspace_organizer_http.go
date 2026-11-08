package mail

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *AccountHandler) mailOrganizerState(c *gin.Context) {
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	value, err := h.workspace.MailOrganizer(c.Request.Context(), user)
	mailResult(c, http.StatusOK, value, err)
}
func (h *AccountHandler) createMailOrganizerLabel(c *gin.Context) {
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var input OrganizerLabelInput
	if !mailJSON(c, &input) {
		return
	}
	value, err := h.workspace.CreateOrganizerLabel(c.Request.Context(), user, input)
	mailResult(c, http.StatusCreated, value, err)
}
func (h *AccountHandler) updateMailOrganizerLabel(c *gin.Context) {
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	var input OrganizerLabelInput
	if !mailJSON(c, &input) {
		return
	}
	value, err := h.workspace.UpdateOrganizerLabel(c.Request.Context(), user, id, input)
	mailResult(c, http.StatusOK, value, err)
}
func (h *AccountHandler) deleteMailOrganizerLabel(c *gin.Context) {
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	err := h.workspace.DeleteOrganizerLabel(c.Request.Context(), user, id)
	mailResult(c, http.StatusOK, gin.H{"deleted": err == nil}, err)
}
func (h *AccountHandler) mutateMailOrganizerMessage(c *gin.Context) {
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var input OrganizerActionInput
	if !mailJSON(c, &input) {
		return
	}
	value, err := h.workspace.MutateOrganizer(c.Request.Context(), user, input)
	mailResult(c, http.StatusOK, value, err)
}

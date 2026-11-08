package mail

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *AccountHandler) aliasOwner(c *gin.Context) bool {
	if h.aliases == nil {
		httpx.Error(c, apperr.New(503, "mail.aliases_disabled", "Mail alias management is disabled", nil))
		return false
	}
	return true
}
func (h *AccountHandler) listMailAliases(c *gin.Context) {
	if !h.aliasOwner(c) {
		return
	}
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	rows, err := h.aliases.List(c.Request.Context(), u)
	mailResult(c, http.StatusOK, rows, err)
}
func (h *AccountHandler) createMailAlias(c *gin.Context) {
	if !h.aliasOwner(c) {
		return
	}
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	var body struct {
		LocalPart string `json:"local_part" binding:"required"`
	}
	if !mailJSON(c, &body) {
		return
	}
	alias, err := h.aliases.Create(c.Request.Context(), u, body.LocalPart)
	mailResult(c, http.StatusCreated, alias, err)
}
func (h *AccountHandler) actionMailAlias(c *gin.Context) {
	if !h.aliasOwner(c) {
		return
	}
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	var body struct {
		Action string `json:"action" binding:"required"`
	}
	if !mailJSON(c, &body) {
		return
	}
	var enabled bool
	switch body.Action {
	case "enable":
		enabled = true
	case "disable":
		enabled = false
	default:
		httpx.Error(c, apperr.BadRequest("mail.alias_bad_action", "Only enable or disable is supported"))
		return
	}
	alias, err := h.aliases.SetEnabled(c.Request.Context(), u, id, enabled)
	mailResult(c, http.StatusOK, alias, err)
}

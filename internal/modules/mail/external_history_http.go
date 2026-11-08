package mail

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *AccountHandler) externalHistoryService(c *gin.Context) (*MailExternalHistoryService, bool) {
	if _, ok := h.externalUser(c); !ok {
		return nil, false
	}
	service := NewMailExternalHistoryService(h.external, nil, nil)
	if service == nil {
		httpx.Error(c, apperr.New(503, "mail.external_history_disabled", "History import unavailable", nil))
		return nil, false
	}
	return service, true
}
func (h *AccountHandler) externalHistoryList(c *gin.Context) {
	service, ok := h.externalHistoryService(c)
	if !ok {
		return
	}
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	list, err := service.List(c.Request.Context(), user, id)
	mailResult(c, http.StatusOK, list, err)
}
func (h *AccountHandler) externalHistoryStart(c *gin.Context) {
	service, ok := h.externalHistoryService(c)
	if !ok {
		return
	}
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	var body struct {
		Folder     string `json:"folder" binding:"required"`
		Continuous bool   `json:"continuous"`
	}
	if !mailJSON(c, &body) {
		return
	}
	job, err := service.StartWithOptions(c.Request.Context(), user, id, body.Folder, body.Continuous)
	mailResult(c, http.StatusCreated, job, err)
}
func (h *AccountHandler) externalHistoryAction(c *gin.Context) {
	service, ok := h.externalHistoryService(c)
	if !ok {
		return
	}
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	id, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	jobID, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	var body struct {
		Action string `json:"action" binding:"required"`
	}
	if !mailJSON(c, &body) {
		return
	}
	job, err := service.Action(c.Request.Context(), user, id, jobID, body.Action)
	mailResult(c, http.StatusOK, job, err)
}

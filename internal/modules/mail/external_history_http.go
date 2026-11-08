package mail

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
	"os"
	"strconv"
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
	if os.Getenv("ATOMAN_MAIL_TWO_WAY_FLAGS_ENABLED") == "1" && h.external.workspace.read != nil {
		service.EnableFlagSync(liveExternalHistoryRemote{}, stalwartMailFlagWriter{read: h.external.workspace.read})
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

// externalFlagPreview compares the owner-approved source folder and its
// imported native copies. The endpoint is intentionally read-only.
func (h *AccountHandler) externalFlagPreview(c *gin.Context) {
	service, ok := h.externalHistoryService(c)
	if !ok {
		return
	}
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	accountID, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	jobID, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	position := 0
	if raw := c.Query("position"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > externalFlagPreviewMaxPosition {
			httpx.Error(c, apperr.BadRequest("mail.external_flag_position", "Invalid preview position"))
			return
		}
		position = n
	}
	result, err := service.FlagPreview(c.Request.Context(), user, accountID, jobID, position)
	mailResult(c, http.StatusOK, result, err)
}

// Optional flag write automation is gated by server configuration and an
// explicit opt-in for the exact authenticated history job.
func (h *AccountHandler) externalFlagSyncConfig(c *gin.Context) {
	service, ok := h.externalHistoryService(c)
	if !ok {
		return
	}
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	accountID, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	jobID, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled" binding:"required"`
	}
	if !mailJSON(c, &in) {
		return
	}
	if in.Enabled == nil {
		httpx.Error(c, apperr.BadRequest("mail.flag_sync_invalid", "Specify enabled=true or false"))
		return
	}
	result, err := service.ConfigureFlagSync(c.Request.Context(), user, accountID, jobID, *in.Enabled)
	mailResult(c, http.StatusOK, result, err)
}
func (h *AccountHandler) externalFlagSyncAttempts(c *gin.Context) {
	service, ok := h.externalHistoryService(c)
	if !ok {
		return
	}
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	accountID, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	jobID, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	result, err := service.FlagSyncAttempts(c.Request.Context(), user, accountID, jobID)
	mailResult(c, http.StatusOK, result, err)
}
func (h *AccountHandler) externalFlagSyncReconcile(c *gin.Context) {
	service, ok := h.externalHistoryService(c)
	if !ok {
		return
	}
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	accountID, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	jobID, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	attemptID, ok := parseMailUUID(c, c.Param("attempt_id"))
	if !ok {
		return
	}
	result, err := service.ReconcileFlagAttempt(c.Request.Context(), user, accountID, jobID, attemptID)
	mailResult(c, http.StatusOK, result, err)
}

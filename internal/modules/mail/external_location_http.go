package mail

import (
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/httpx"
	"github.com/gin-gonic/gin"
	"net/http"
	"os"
)

func (h *AccountHandler) externalLocationService(c *gin.Context) (*MailExternalLocationService, bool) {
	if os.Getenv("ATOMAN_MAIL_LOCATION_REVIEW_ENABLED") != "1" {
		httpx.Error(c, apperr.New(503, "mail.location_disabled", "Location review is not enabled", nil))
		return nil, false
	}
	history, ok := h.externalHistoryService(c)
	if !ok {
		return nil, false
	}
	if h.workspace == nil || h.workspace.read == nil {
		httpx.Error(c, apperr.New(503, "mail.location_native_unavailable", "Native mail is not available", nil))
		return nil, false
	}
	service := NewMailExternalLocationService(history, stalwartMailNativeLocation{read: h.workspace.read})
	if err := service.ready(); err != nil {
		httpx.Error(c, err)
		return nil, false
	}
	return service, true
}
func (h *AccountHandler) externalLocationCases(c *gin.Context) {
	s, ok := h.externalLocationService(c)
	if !ok {
		return
	}
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	a, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	j, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	result, err := s.Cases(c.Request.Context(), u, a, j)
	mailResult(c, http.StatusOK, result, err)
}
func (h *AccountHandler) externalLocationScan(c *gin.Context) {
	s, ok := h.externalLocationService(c)
	if !ok {
		return
	}
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	a, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	j, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	var in struct {
		AfterUID uint32 `json:"after_uid"`
	}
	if !mailJSON(c, &in) {
		return
	}
	result, err := s.Scan(c.Request.Context(), u, a, j, in.AfterUID)
	mailResult(c, http.StatusOK, result, err)
}
func (h *AccountHandler) externalLocationAction(c *gin.Context) {
	s, ok := h.externalLocationService(c)
	if !ok {
		return
	}
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	a, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	j, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	caseID, ok := parseMailUUID(c, c.Param("case_id"))
	if !ok {
		return
	}
	var in struct {
		Action    string `json:"action" binding:"required"`
		Confirmed bool   `json:"confirmed"`
	}
	if !mailJSON(c, &in) {
		return
	}
	if !in.Confirmed {
		httpx.Error(c, apperr.BadRequest("mail.location_confirmation_required", "An explicit confirmation is required"))
		return
	}
	result, err := s.Resolve(c.Request.Context(), u, a, j, caseID, in.Action)
	mailResult(c, http.StatusOK, result, err)
}
func (h *AccountHandler) externalLocationActions(c *gin.Context) {
	s, ok := h.externalLocationService(c)
	if !ok {
		return
	}
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	a, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	j, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	caseID, ok := parseMailUUID(c, c.Param("case_id"))
	if !ok {
		return
	}
	result, err := s.Actions(c.Request.Context(), u, a, j, caseID)
	mailResult(c, http.StatusOK, result, err)
}
func (h *AccountHandler) externalLocationReconcile(c *gin.Context) {
	s, ok := h.externalLocationService(c)
	if !ok {
		return
	}
	u, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	a, ok := parseMailUUID(c, c.Param("id"))
	if !ok {
		return
	}
	j, ok := parseMailUUID(c, c.Param("job_id"))
	if !ok {
		return
	}
	caseID, ok := parseMailUUID(c, c.Param("case_id"))
	if !ok {
		return
	}
	attempt, ok := parseMailUUID(c, c.Param("attempt_id"))
	if !ok {
		return
	}
	result, err := s.Reconcile(c.Request.Context(), u, a, j, caseID, attempt)
	mailResult(c, http.StatusOK, result, err)
}

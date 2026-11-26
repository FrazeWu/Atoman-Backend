package papers

import (
	"atoman/internal/platform/httpx"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
)

// @Summary Check whether owner-initiated AI suggestions are available
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Project UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/ai/capability [get]
func (h *handler) aiCapability(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	if err := h.service.checkAIOwner(c.Request.Context(), user, pid); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, map[string]any{"enabled": paperAIEnabled(), "base_urls": paperAIBases(), "max_source_bytes": paperAISourceLimit, "max_context_bytes": paperAIContextLimit, "max_extra_files": paperAIExtraLimit, "max_history_turns": paperAIHistoryTurns, "max_history_bytes": paperAIHistoryBytes, "project_edits": true, "activity": true, "agent": paperAIIdentity(), "historical_changes": true})
}

// @Summary Request a read-only AI suggestion; no automatic manuscript mutation
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Project UUID"
// @Param input body paperAIInput true "Explicit user-supplied credentials and approved source"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/ai/suggestions [post]
func (h *handler) aiSuggestion(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	var in paperAIInput
	dec := json.NewDecoder(c.Request.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httpx.Error(c, paperAIInvalid())
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		httpx.Error(c, paperAIInvalid())
		return
	}
	out, err := h.service.suggestAI(c.Request.Context(), user, pid, in)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

// @Summary List recent server-recorded AI activity (metadata only, owner private)
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Project UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/ai/activity [get]
func (h *handler) aiActivity(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	events, err := h.service.listPaperAIActivity(user, pid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, events)
}

// @Summary Read immutable versions bound to an approved AI change
// @Tags papers
// @Security BearerAuth
// @Param id path string true "Project UUID"
// @Param eventId path string true "Applied or rolled-back AI event UUID"
// @Param fileId path string true "File UUID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/papers/projects/{id}/ai/activity/{eventId}/changes/{fileId} [get]
func (h *handler) aiHistoricalChange(c *gin.Context) {
	user, ok := current(c)
	if !ok {
		return
	}
	pid, ok := projectID(c)
	if !ok {
		return
	}
	event, ok := httpx.UUIDParam(c, "eventId")
	if !ok {
		return
	}
	fid, ok := httpx.UUIDParam(c, "fileId")
	if !ok {
		return
	}
	out, err := h.service.paperAIHistoricalChange(c.Request.Context(), user, pid, event, fid)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

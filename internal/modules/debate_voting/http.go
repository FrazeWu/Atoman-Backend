package debate_voting

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
)

type voteRequest struct {
	Direction string `json:"direction"`
}

type Handler struct{ service *Service }

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.GET("/debate/topics/:id/votes", h.getVotes)
	group.PUT("/debate/topics/:id/vote", h.setVote)
	group.DELETE("/debate/topics/:id/vote", h.deleteVote)
	group.GET("/debate/topics/:id/conclusions", h.listConclusions)
}

// getVotes godoc
// @Summary Get community vote statistics
// @Tags Debate
// @Produce json
// @Param id path string true "Debate ID"
// @Success 200 {object} handlers.DebateVoteResponse
// @Router /api/v1/debate/topics/{id}/votes [get]
func (h *Handler) getVotes(c *gin.Context) {
	id, ok := httpx.UUIDParam(c, "id")
	if !ok {
		return
	}
	user, _ := authctx.Current(c)
	stats, err := h.service.GetVotes(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, stats)
}

// setVote godoc
// @Summary Set a community debate vote
// @Tags Debate
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Debate ID"
// @Param body body voteRequest true "Vote"
// @Success 200 {object} handlers.DebateVoteResponse
// @Router /api/v1/debate/topics/{id}/vote [put]
func (h *Handler) setVote(c *gin.Context) {
	user, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := httpx.UUIDParam(c, "id")
	if !ok {
		return
	}
	var req voteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return
	}
	stats, err := h.service.SetVote(user, id, req.Direction)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, stats)
}

// deleteVote godoc
// @Summary Delete the current user's debate vote
// @Tags Debate
// @Produce json
// @Security BearerAuth
// @Param id path string true "Debate ID"
// @Success 200 {object} handlers.DebateVoteResponse
// @Router /api/v1/debate/topics/{id}/vote [delete]
func (h *Handler) deleteVote(c *gin.Context) {
	user, ok := requireUser(c)
	if !ok {
		return
	}
	id, ok := httpx.UUIDParam(c, "id")
	if !ok {
		return
	}
	stats, err := h.service.DeleteVote(user, id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, stats)
}

// listConclusions godoc
// @Summary List immutable debate conclusion events
// @Tags Debate
// @Produce json
// @Param id path string true "Debate ID"
// @Success 200 {object} handlers.DebateConclusionListResponse
// @Router /api/v1/debate/topics/{id}/conclusions [get]
func (h *Handler) listConclusions(c *gin.Context) {
	id, ok := httpx.UUIDParam(c, "id")
	if !ok {
		return
	}
	events, err := h.service.ListConclusions(id)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, events)
}

func requireUser(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
	}
	return user, ok
}

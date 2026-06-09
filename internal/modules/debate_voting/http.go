package debate_voting

import (
	"net/http"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/httpx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type voteRequest struct {
	VoteType int `json:"vote_type"`
}

type Handler struct {
	service *Service
}

func RegisterRoutes(group *gin.RouterGroup, service *Service) {
	h := &Handler{service: service}
	group.POST("/debates/:debateID/vote", h.setDebateVote)
	group.DELETE("/debates/:debateID/vote", h.removeDebateVote)
	group.POST("/debate-arguments/:argumentID/vote", h.setArgumentVote)
	group.DELETE("/debate-arguments/:argumentID/vote", h.removeArgumentVote)
	group.POST("/debates/:debateID/conclusion-vote", h.setConclusionVote)
	group.DELETE("/debates/:debateID/conclusion-vote", h.removeConclusionVote)
}

func (h *Handler) setDebateVote(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	debateID, ok := parseUUIDParam(c, "debateID")
	if !ok {
		return
	}
	req, ok := bindVoteRequest(c)
	if !ok {
		return
	}
	vote, err := h.service.SetDebateVote(user, debateID, req.VoteType)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, vote)
}

func (h *Handler) removeDebateVote(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	debateID, ok := parseUUIDParam(c, "debateID")
	if !ok {
		return
	}
	if err := h.service.RemoveDebateVote(user, debateID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Vote removed"})
}

func (h *Handler) setArgumentVote(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	argumentID, ok := parseUUIDParam(c, "argumentID")
	if !ok {
		return
	}
	req, ok := bindVoteRequest(c)
	if !ok {
		return
	}
	vote, err := h.service.SetArgumentVote(user, argumentID, req.VoteType)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, vote)
}

func (h *Handler) removeArgumentVote(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	argumentID, ok := parseUUIDParam(c, "argumentID")
	if !ok {
		return
	}
	if err := h.service.RemoveArgumentVote(user, argumentID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Vote removed"})
}

func (h *Handler) setConclusionVote(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	debateID, ok := parseUUIDParam(c, "debateID")
	if !ok {
		return
	}
	state, err := h.service.SetConclusionVote(user, debateID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, state)
}

func (h *Handler) removeConclusionVote(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		return
	}
	debateID, ok := parseUUIDParam(c, "debateID")
	if !ok {
		return
	}
	if err := h.service.RemoveConclusionVote(user, debateID); err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, gin.H{"message": "Conclusion vote removed"})
}

func currentUser(c *gin.Context) (authctx.CurrentUser, bool) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return authctx.CurrentUser{}, false
	}
	return user, true
}

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", name+" must be a valid uuid"))
		return uuid.Nil, false
	}
	return id, true
}

func bindVoteRequest(c *gin.Context) (voteRequest, bool) {
	var req voteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "request body must be valid JSON"))
		return voteRequest{}, false
	}
	return req, true
}

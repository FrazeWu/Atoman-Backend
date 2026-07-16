package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"atoman/internal/modules/comment"
	"atoman/internal/platform/authctx"
	proposalservice "atoman/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type timelineProposalRequest struct {
	Content       string                 `json:"content"`
	Evidence      string                 `json:"evidence"`
	Patch         map[string]any         `json:"patch"`
	Mentions      []comment.MentionInput `json:"mentions"`
	AttachmentIDs []uuid.UUID            `json:"attachment_ids"`
}

// CreateTimelineEventProposal godoc
// @Summary Create a timeline event revision proposal
// @Tags Timeline
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Event ID"
// @Param body body timelineProposalRequest true "Revision proposal"
// @Success 201 {object} TimelineProposalResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/timeline/events/{id}/revision-proposals [post]
func CreateTimelineEventProposal(service *proposalservice.TimelineRevisionProposalService) gin.HandlerFunc {
	return createTimelineProposal(service, comment.TargetKindTimelineEvent)
}

// CreateTimelinePersonProposal godoc
// @Summary Create a timeline person revision proposal
// @Tags Timeline
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param id path string true "Person ID"
// @Param body body timelineProposalRequest true "Revision proposal"
// @Success 201 {object} TimelineProposalResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/timeline/persons/{id}/revision-proposals [post]
func CreateTimelinePersonProposal(service *proposalservice.TimelineRevisionProposalService) gin.HandlerFunc {
	return createTimelineProposal(service, comment.TargetKindTimelinePerson)
}

// ListTimelineEventProposals godoc
// @Summary List timeline event revision proposals
// @Tags Timeline
// @Produce json
// @Param id path string true "Event ID"
// @Param page query int false "Page"
// @Param page_size query int false "Page size"
// @Success 200 {object} TimelineProposalListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/timeline/events/{id}/revision-proposals [get]
func ListTimelineEventProposals(service *proposalservice.TimelineRevisionProposalService) gin.HandlerFunc {
	return listTimelineProposals(service, comment.TargetKindTimelineEvent)
}

// ListTimelinePersonProposals godoc
// @Summary List timeline person revision proposals
// @Tags Timeline
// @Produce json
// @Param id path string true "Person ID"
// @Param page query int false "Page"
// @Param page_size query int false "Page size"
// @Success 200 {object} TimelineProposalListResponse
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/v1/timeline/persons/{id}/revision-proposals [get]
func ListTimelinePersonProposals(service *proposalservice.TimelineRevisionProposalService) gin.HandlerFunc {
	return listTimelineProposals(service, comment.TargetKindTimelinePerson)
}

func listTimelineProposals(service *proposalservice.TimelineRevisionProposalService, kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, _ := authctx.Current(c)
		targetID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			writeTimelineProposalError(c, proposalservice.ErrTimelineProposalInvalid)
			return
		}
		page := 1
		if _, err := fmt.Sscanf(c.DefaultQuery("page", "1"), "%d", &page); err != nil {
			writeTimelineProposalError(c, proposalservice.ErrTimelineProposalInvalid)
			return
		}
		pageSize := 20
		if _, err := fmt.Sscanf(c.DefaultQuery("page_size", "20"), "%d", &pageSize); err != nil {
			writeTimelineProposalError(c, proposalservice.ErrTimelineProposalInvalid)
			return
		}
		proposals, err := service.List(user, kind, targetID, page, pageSize)
		if err != nil {
			writeTimelineProposalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": proposals})
	}
}

func createTimelineProposal(service *proposalservice.TimelineRevisionProposalService, kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := authctx.Current(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "comment.authentication_required", "message": "Login required"}})
			return
		}
		targetID, err := uuid.Parse(c.Param("id"))
		if err != nil {
			writeTimelineProposalError(c, proposalservice.ErrTimelineProposalInvalid)
			return
		}
		var request timelineProposalRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			writeTimelineProposalError(c, proposalservice.ErrTimelineProposalInvalid)
			return
		}
		input := proposalservice.TimelineProposalInput{Content: request.Content, Evidence: request.Evidence, Patch: request.Patch, Mentions: request.Mentions, AttachmentIDs: request.AttachmentIDs}
		var proposal proposalservice.TimelineProposal
		if kind == comment.TargetKindTimelineEvent {
			proposal, err = service.CreateEventProposal(user, targetID, input)
		} else {
			proposal, err = service.CreatePersonProposal(user, targetID, input)
		}
		if err != nil {
			writeTimelineProposalError(c, err)
			return
		}
		c.JSON(http.StatusCreated, gin.H{"data": proposal})
	}
}

// DecideTimelineRevisionProposal godoc
// @Summary Accept or reject a timeline revision proposal
// @Tags Timeline
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param comment_id path string true "Proposal comment ID"
// @Param body body TimelineProposalDecisionInput true "Decision"
// @Success 200 {object} TimelineProposalResponse
// @Failure 400 {object} ErrorResponse
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /api/v1/timeline/revision-proposals/{comment_id}/decision [put]
func DecideTimelineRevisionProposal(service *proposalservice.TimelineRevisionProposalService) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := authctx.Current(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"code": "comment.authentication_required", "message": "Login required"}})
			return
		}
		commentID, err := uuid.Parse(c.Param("comment_id"))
		if err != nil {
			writeTimelineProposalError(c, proposalservice.ErrTimelineProposalInvalid)
			return
		}
		var request struct {
			Decision string `json:"decision"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			writeTimelineProposalError(c, proposalservice.ErrTimelineProposalInvalid)
			return
		}
		proposal, err := service.Decide(user, commentID, request.Decision)
		if err != nil {
			writeTimelineProposalError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"data": proposal})
	}
}

func writeTimelineProposalError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "timeline_proposal.internal", "Failed to process revision proposal"
	switch {
	case errors.Is(err, proposalservice.ErrTimelineProposalInvalid), errors.Is(err, comment.ErrInvalidContent), errors.Is(err, comment.ErrInvalidMention), errors.Is(err, comment.ErrInvalidAttachment):
		status, code, message = http.StatusBadRequest, "timeline_proposal.invalid", "Invalid revision proposal"
	case errors.Is(err, comment.ErrInvalidListOptions):
		status, code, message = http.StatusBadRequest, "timeline_proposal.invalid_page", "Invalid pagination"
	case errors.Is(err, proposalservice.ErrTimelineProposalForbidden), errors.Is(err, comment.ErrCommentForbidden):
		status, code, message = http.StatusForbidden, "timeline_proposal.forbidden", "Not authorized"
	case errors.Is(err, proposalservice.ErrTimelineProposalNotFound), errors.Is(err, comment.ErrTargetNotFound):
		status, code, message = http.StatusNotFound, "timeline_proposal.not_found", "Revision proposal not found"
	case errors.Is(err, proposalservice.ErrTimelineProposalNotPending):
		status, code, message = http.StatusConflict, "timeline_proposal.not_pending", "Revision proposal is no longer pending"
	}
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
}

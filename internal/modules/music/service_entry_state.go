package music

import (
	"errors"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	revisionservice "atoman/internal/service"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CreateMusicStateRequestInput struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

type ReviewMusicStateRequestInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type EmergencyMusicStateInput struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (s *Service) CreateMusicEntryStateRequest(user authctx.CurrentUser, entityType string, entityID uuid.UUID, input CreateMusicStateRequestInput) (model.MusicEntryStateRequest, error) {
	if user.ID == uuid.Nil {
		return model.MusicEntryStateRequest{}, apperr.Unauthorized("Login required")
	}
	input.Action = strings.TrimSpace(input.Action)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		return model.MusicEntryStateRequest{}, apperr.BadRequest("validation.invalid_request", "reason is required")
	}
	if !validMusicStateRequestAction(input.Action) {
		return model.MusicEntryStateRequest{}, apperr.BadRequest("validation.invalid_request", "invalid state request action")
	}

	var request model.MusicEntryStateRequest
	err := s.db.Transaction(func(tx *gorm.DB) error {
		state, err := revisionservice.LoadMusicEntryState(tx, entityType, entityID, true)
		if err != nil {
			return err
		}
		if state.LifecycleStatus != model.MusicLifecycleActive {
			return apperr.Conflict("music.entry_not_active", "Only active music entries can change edit state")
		}
		if err := validateMusicStateAction(state.EditStatus, input.Action); err != nil {
			return err
		}
		var pending model.MusicEntryStateRequest
		if err := tx.Where("entity_type = ? AND entity_id = ? AND status = ?", entityType, entityID, model.MusicStateRequestPending).First(&pending).Error; err == nil {
			return apperr.Conflict("music.state_request_pending", "A state request is already pending")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		request = model.MusicEntryStateRequest{
			EntityType: entityType, EntityID: entityID, Action: input.Action,
			Status: model.MusicStateRequestPending, RequestedBy: user.ID, RequestReason: input.Reason,
		}
		if input.Action == model.MusicStateActionClose {
			var current model.Revision
			if err := tx.Where("content_type = ? AND content_id = ? AND is_current = ?", entityType, entityID, true).
				Order("version_number DESC").First(&current).Error; err != nil {
				return apperr.Conflict("music.current_revision_required", "A current revision is required before closing")
			}
			request.BaseRevisionID = &current.ID
		}
		if err := tx.Create(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) || strings.Contains(strings.ToLower(err.Error()), "unique") {
				return apperr.Conflict("music.state_request_pending", "A state request is already pending")
			}
			return err
		}
		return nil
	})
	return request, err
}

func (s *Service) ReviewMusicEntryStateRequest(user authctx.CurrentUser, requestID uuid.UUID, input ReviewMusicStateRequestInput) (model.MusicEntryStateRequest, error) {
	if user.Role != authctx.RoleAdmin && user.Role != authctx.RoleOwner {
		return model.MusicEntryStateRequest{}, apperr.Forbidden("music.state_review_forbidden", "Administrator access is required")
	}
	input.Decision = strings.TrimSpace(input.Decision)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Decision != model.MusicStateRequestApproved && input.Decision != model.MusicStateRequestRejected {
		return model.MusicEntryStateRequest{}, apperr.BadRequest("validation.invalid_request", "decision must be approved or rejected")
	}
	if input.Reason == "" {
		return model.MusicEntryStateRequest{}, apperr.BadRequest("validation.invalid_request", "reason is required")
	}

	var request model.MusicEntryStateRequest
	stale := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		query := tx.Preload("Requester").Where("id = ?", requestID)
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.state_request_not_found", "State request not found")
			}
			return err
		}
		if request.Status != model.MusicStateRequestPending {
			return apperr.Conflict("music.state_request_resolved", "State request has already been resolved")
		}
		if request.RequestedBy == user.ID {
			return apperr.Forbidden("music.state_self_review_forbidden", "You cannot review your own state request")
		}
		state, err := revisionservice.LoadMusicEntryState(tx, request.EntityType, request.EntityID, true)
		if err != nil {
			return err
		}
		if err := validateMusicStateAction(state.EditStatus, request.Action); err != nil {
			return err
		}
		now := time.Now().UTC()
		request.ReviewedBy = &user.ID
		request.ReviewReason = input.Reason
		request.ReviewedAt = &now
		if input.Decision == model.MusicStateRequestRejected {
			request.Status = model.MusicStateRequestRejected
			return tx.Save(&request).Error
		}
		if request.Action == model.MusicStateActionClose {
			var current model.Revision
			if err := tx.Select("id").Where("content_type = ? AND content_id = ? AND is_current = ?", request.EntityType, request.EntityID, true).First(&current).Error; err != nil || request.BaseRevisionID == nil || current.ID != *request.BaseRevisionID {
				request.Status = model.MusicStateRequestSuperseded
				stale = true
				return tx.Save(&request).Error
			}
		}
		target := musicStateActionTarget(request.Action)
		if err := revisionservice.UpdateMusicEntryEditStatus(tx, request.EntityType, request.EntityID, target); err != nil {
			return err
		}
		request.Status = model.MusicStateRequestApproved
		if err := tx.Save(&request).Error; err != nil {
			return err
		}
		event := model.MusicEntryStateEvent{
			EntityType: request.EntityType, EntityID: request.EntityID,
			FromStatus: state.EditStatus, ToStatus: target, Trigger: "request", ActorID: &user.ID,
			RequestID: &request.ID, Reason: input.Reason,
		}
		return tx.Create(&event).Error
	})
	if err != nil {
		return model.MusicEntryStateRequest{}, err
	}
	if stale {
		return request, apperr.Conflict("music.state_request_superseded", "The entry changed after this close request was created")
	}
	return request, nil
}

func (s *Service) CancelMusicEntryStateRequest(user authctx.CurrentUser, requestID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var request model.MusicEntryStateRequest
		query := tx.Where("id = ?", requestID)
		if tx.Dialector.Name() != "sqlite" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		if err := query.First(&request).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.state_request_not_found", "State request not found")
			}
			return err
		}
		if request.Status != model.MusicStateRequestPending {
			return apperr.Conflict("music.state_request_resolved", "State request has already been resolved")
		}
		if request.RequestedBy != user.ID && user.Role != authctx.RoleAdmin && user.Role != authctx.RoleOwner {
			return apperr.Forbidden("music.state_cancel_forbidden", "You cannot cancel this state request")
		}
		return tx.Model(&request).Update("status", model.MusicStateRequestCancelled).Error
	})
}

func (s *Service) EmergencyMusicEntryState(user authctx.CurrentUser, entityType string, entityID uuid.UUID, input EmergencyMusicStateInput) error {
	if user.Role != authctx.RoleAdmin && user.Role != authctx.RoleOwner {
		return apperr.Forbidden("music.state_emergency_forbidden", "Administrator access is required")
	}
	input.Status = strings.TrimSpace(input.Status)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		return apperr.BadRequest("validation.invalid_request", "reason is required")
	}
	if input.Status != model.MusicEditDevelopment && input.Status != model.MusicEditLocked && input.Status != model.MusicEditClosed {
		return apperr.BadRequest("validation.invalid_request", "invalid edit status")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		state, err := revisionservice.LoadMusicEntryState(tx, entityType, entityID, true)
		if err != nil {
			return err
		}
		if state.LifecycleStatus != model.MusicLifecycleActive {
			return apperr.Conflict("music.entry_not_active", "Only active music entries can change edit state")
		}
		if state.EditStatus == input.Status {
			return apperr.Conflict("music.state_unchanged", "Music entry already has this edit status")
		}
		if err := revisionservice.UpdateMusicEntryEditStatus(tx, entityType, entityID, input.Status); err != nil {
			return err
		}
		if err := tx.Model(&model.MusicEntryStateRequest{}).
			Where("entity_type = ? AND entity_id = ? AND status = ?", entityType, entityID, model.MusicStateRequestPending).
			Updates(map[string]any{"status": model.MusicStateRequestSuperseded, "reviewed_by": user.ID, "review_reason": input.Reason, "reviewed_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		event := model.MusicEntryStateEvent{
			EntityType: entityType, EntityID: entityID, FromStatus: state.EditStatus,
			ToStatus: input.Status, Trigger: "emergency", ActorID: &user.ID, Reason: input.Reason,
		}
		return tx.Create(&event).Error
	})
}

func (s *Service) ListMusicEntryStateRequests(user authctx.CurrentUser, entityType string, entityID uuid.UUID, status string) ([]model.MusicEntryStateRequest, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	query := s.db.Preload("Requester").Preload("Reviewer").Order("created_at DESC")
	if entityType != "" {
		query = query.Where("entity_type = ?", entityType)
	}
	if entityID != uuid.Nil {
		query = query.Where("entity_id = ?", entityID)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	if user.Role != authctx.RoleAdmin && user.Role != authctx.RoleOwner {
		query = query.Where("requested_by = ?", user.ID)
	}
	var requests []model.MusicEntryStateRequest
	if err := query.Limit(200).Find(&requests).Error; err != nil {
		return nil, err
	}
	return requests, nil
}

func validateMusicStateAction(current, action string) error {
	expected := map[string]string{
		model.MusicStateActionClose:  model.MusicEditDevelopment,
		model.MusicStateActionReopen: model.MusicEditClosed,
		model.MusicStateActionUnlock: model.MusicEditLocked,
	}[action]
	if expected == "" || current != expected {
		return apperr.Conflict("music.invalid_state_transition", "This state request is not valid for the current edit status")
	}
	return nil
}

func musicStateActionTarget(action string) string {
	if action == model.MusicStateActionClose {
		return model.MusicEditClosed
	}
	return model.MusicEditDevelopment
}

func validMusicStateRequestAction(action string) bool {
	return action == model.MusicStateActionClose || action == model.MusicStateActionReopen || action == model.MusicStateActionUnlock
}

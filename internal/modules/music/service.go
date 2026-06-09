package music

import (
	"encoding/json"
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	db   *gorm.DB
	repo *Repo
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

func (s *Service) SubmitEdit(user authctx.CurrentUser, req SubmitEditRequest) (model.MusicEdit, error) {
	if user.ID == uuid.Nil {
		return model.MusicEdit{}, apperr.Unauthorized("Login required")
	}
	if req.Type == "" || req.EntityType == "" || req.Reason == "" {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "type, entity_type and reason are required")
	}

	payloadJSON, err := marshalObject(req.Payload, map[string]any{})
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "payload must be an object")
	}
	changesJSON, err := marshalObject(req.Changes, map[string]any{})
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "changes must be an object")
	}
	sourcesJSON, err := json.Marshal(req.Sources)
	if err != nil {
		return model.MusicEdit{}, apperr.BadRequest("validation.invalid_request", "sources are invalid")
	}

	edit := model.MusicEdit{
		Type:        req.Type,
		EntityType:  req.EntityType,
		EntityID:    req.EntityID,
		SubmittedBy: user.ID,
		Status:      "open",
		Reason:      req.Reason,
		PayloadJSON: string(payloadJSON),
		ChangesJSON: string(changesJSON),
		SourcesJSON: string(sourcesJSON),
		Votable:     true,
	}
	if err := s.repo.CreateEdit(&edit); err != nil {
		return model.MusicEdit{}, err
	}
	return edit, nil
}

func (s *Service) Vote(user authctx.CurrentUser, editID uuid.UUID, req VoteRequest) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	if req.Vote != "yes" && req.Vote != "no" {
		return apperr.BadRequest("validation.invalid_request", "vote must be yes or no")
	}

	edit, err := s.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("music.edit_not_found", "Edit not found")
		}
		return err
	}
	if edit.Status != "open" {
		return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
	}

	vote := model.MusicEditVote{EditID: editID, UserID: user.ID, Vote: req.Vote, Comment: req.Comment}
	return s.db.Where("edit_id = ? AND user_id = ?", editID, user.ID).Assign(map[string]any{"vote": req.Vote, "comment": req.Comment}).FirstOrCreate(&vote).Error
}

func (s *Service) ApproveEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Moderator role required")
	}

	var out model.MusicEdit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		edit, err := repo.GetEdit(editID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			}
			return err
		}
		if edit.Status != "open" {
			return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
		}
		if err := applyEdit(tx, &edit); err != nil {
			return err
		}

		edit.Status = "applied"
		if err := repo.SaveEdit(&edit); err != nil {
			return err
		}
		decision := model.MusicEditDecision{EditID: edit.ID, DeciderID: user.ID, Decision: "approve", Reason: reason}
		if err := tx.Create(&decision).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "music.edit.approve", EntityType: "music_edit", EntityID: &edit.ID, Reason: reason}); err != nil {
			return err
		}
		out = edit
		return nil
	})
	if err != nil {
		failed := model.MusicEdit{}
		if getErr := s.db.First(&failed, "id = ?", editID).Error; getErr == nil && failed.Status == "open" {
			failed.Status = "failed_prerequisite"
			failed.FailureReason = err.Error()
			if saveErr := s.repo.SaveEdit(&failed); saveErr == nil {
				out = failed
			}
		}
		return out, err
	}
	return out, nil
}

func (s *Service) RejectEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Moderator role required")
	}

	var out model.MusicEdit
	err := s.db.Transaction(func(tx *gorm.DB) error {
		repo := NewRepo(tx)
		edit, err := repo.GetEdit(editID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("music.edit_not_found", "Edit not found")
			}
			return err
		}
		if edit.Status != "open" {
			return apperr.Unprocessable("music.edit_not_open", "Edit is not open")
		}

		edit.Status = "rejected"
		if err := repo.SaveEdit(&edit); err != nil {
			return err
		}
		if err := tx.Create(&model.MusicEditDecision{EditID: edit.ID, DeciderID: user.ID, Decision: "reject", Reason: reason}).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "music.edit.reject", EntityType: "music_edit", EntityID: &edit.ID, Reason: reason}); err != nil {
			return err
		}
		out = edit
		return nil
	})
	return out, err
}

func (s *Service) CancelEdit(user authctx.CurrentUser, editID uuid.UUID, reason string) (model.MusicEdit, error) {
	_ = reason
	if user.ID == uuid.Nil {
		return model.MusicEdit{}, apperr.Unauthorized("Login required")
	}

	edit, err := s.repo.GetEdit(editID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.MusicEdit{}, apperr.NotFound("music.edit_not_found", "Edit not found")
		}
		return model.MusicEdit{}, err
	}
	if edit.Status != "open" {
		return model.MusicEdit{}, apperr.Unprocessable("music.edit_not_open", "Edit is not open")
	}
	if edit.SubmittedBy != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return model.MusicEdit{}, apperr.Forbidden("music.edit_forbidden", "Only submitter or moderator can cancel")
	}

	edit.Status = "cancelled"
	if err := s.repo.SaveEdit(&edit); err != nil {
		return model.MusicEdit{}, err
	}
	return edit, nil
}

func marshalObject(value map[string]any, fallback map[string]any) ([]byte, error) {
	if value == nil {
		value = fallback
	}
	return json.Marshal(value)
}

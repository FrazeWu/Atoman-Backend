package papers

import (
	"context"
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// An attribution principal, not an auth account, member, session,
// credential, or autonomous writer. Accepted versions retain the owner.
type paperAIContributor struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Kind        string `json:"kind"`
	Permission  string `json:"permission"`
}

func paperAIIdentity() paperAIContributor {
	return paperAIContributor{ID: "papers-agent", DisplayName: "Papers Agent", Kind: "ai", Permission: "propose_only"}
}
func paperAIActor(action string) string {
	switch action {
	case aiStarted:
		return "owner_requested"
	case aiProposed:
		return "ai_proposed"
	case aiFailed:
		return "request_unconfirmed"
	case aiApplied:
		return "owner_approved"
	case aiRolledBack:
		return "owner_rolled_back"
	default:
		return "unknown"
	}
}

type paperAIHistoricalChange struct {
	EventID        uuid.UUID          `json:"event_id"`
	FileID         uuid.UUID          `json:"file_id"`
	Action         string             `json:"action"`
	Contributor    paperAIContributor `json:"contributor"`
	Decision       string             `json:"decision"`
	Path           string             `json:"path"`
	BeforeRevision int64              `json:"before_revision"`
	AfterRevision  int64              `json:"after_revision"`
	Before         string             `json:"before"`
	After          string             `json:"after"`
}

func (s *Service) paperAIHistoricalChange(ctx context.Context, user authctx.CurrentUser, pid, eventID, fileID uuid.UUID) (paperAIHistoricalChange, error) {
	var out paperAIHistoricalChange
	if err := s.RequireAccess(user); err != nil {
		return out, err
	}
	missing := func() error { return apperr.NotFound("papers.ai_change_not_found", "AI change not found") }
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockOwned(tx, user, pid); err != nil {
			return err
		}
		if err := lockInvitationEligibility(tx, user.ID); err != nil {
			return err
		}
		var event model.AuditLog
		err := paperAIEventQuery(tx, user.ID, pid).Where("id=? AND action IN ?", eventID, []string{aiApplied, aiRolledBack}).First(&event).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return missing()
		}
		if err != nil {
			return err
		}
		meta, err := readPaperAIEvent(event)
		if err != nil {
			return err
		}
		var binding *paperAIAuditFile
		for _, f := range meta.Files {
			if f.FileID == fileID {
				copy := f
				binding = &copy
				break
			}
		}
		if binding == nil {
			return missing()
		}
		// Respect ordinary file-history access: do not reopen a deleted file.
		var current model.PaperFile
		err = tx.Select("id", "kind").Where("id=? AND project_id=? AND kind='text'", fileID, pid).First(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return missing()
		}
		if err != nil {
			return err
		}
		var before, after model.PaperFileVersion
		for _, v := range []struct {
			revision int64
			sha      string
			dest     *model.PaperFileVersion
		}{{binding.BeforeRevision, binding.BeforeSHA, &before}, {binding.AfterRevision, binding.AfterSHA, &after}} {
			err = tx.Where("file_id=? AND revision=?", fileID, v.revision).First(v.dest).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return missing()
			}
			if err != nil {
				return err
			}
			if validateText(v.dest.Content) != nil || v.dest.SHA256 != v.sha || hashText(v.dest.Content) != v.sha {
				return aiAuditConflict()
			}
		}
		if before.Path != after.Path {
			return aiAuditConflict()
		}
		out = paperAIHistoricalChange{EventID: event.ID, FileID: fileID, Action: event.Action, Contributor: paperAIIdentity(), Decision: paperAIActor(event.Action), Path: before.Path, BeforeRevision: before.Revision, AfterRevision: after.Revision, Before: before.Content, After: after.Content}
		return nil
	})
	return out, err
}

package debate

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Service) SaveWiki(user authctx.CurrentUser, debateID uuid.UUID, req SaveWikiRequest) (DebateDTO, error) {
	if err := s.requireActiveUser(user); err != nil {
		return DebateDTO{}, err
	}
	snapshot, err := normalizedSnapshot(req.Title, req.Description, req.Content, req.Tags)
	if err != nil {
		return DebateDTO{}, err
	}
	if strings.TrimSpace(req.EditSummary) == "" {
		return DebateDTO{}, apperr.BadRequest("validation.invalid_request", "edit_summary is required")
	}
	if req.BaseRevisionID == uuid.Nil {
		return DebateDTO{}, apperr.BadRequest("validation.invalid_request", "base_revision is required")
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		debate, current, err := lockWikiForEdit(tx, debateID, req.BaseRevisionID)
		if err != nil {
			return err
		}
		if err := ensureWikiEditable(tx, user, debate); err != nil {
			return err
		}
		if err := tx.Model(&model.Revision{}).Where("id = ?", current.ID).Update("is_current", false).Error; err != nil {
			return err
		}
		revision, err := createRevision(tx, debate.ID, user.ID, &current.ID, current.VersionNumber+1, snapshot, strings.TrimSpace(req.EditSummary), "edit")
		if err != nil {
			return err
		}
		debate.Title, debate.Description, debate.Content = snapshot.Title, snapshot.Description, snapshot.Content
		debate.Tags = pq.StringArray(snapshot.Tags)
		debate.CurrentRevisionID = &revision.ID
		if err := tx.Model(&debate).Updates(map[string]any{
			"title": snapshot.Title, "description": snapshot.Description, "content": snapshot.Content,
			"tags": pq.StringArray(snapshot.Tags), "current_revision_id": revision.ID,
		}).Error; err != nil {
			return err
		}
		_, err = s.projectReferences(tx, user, debate, revision, current.ID)
		return err
	})
	if err != nil {
		return DebateDTO{}, err
	}
	return s.GetDebate(debateID)
}

func (s *Service) ListRevisions(debateID uuid.UUID) ([]DebateRevisionDTO, error) {
	if _, err := s.GetDebate(debateID); err != nil {
		return nil, err
	}
	var revisions []model.Revision
	if err := s.db.Preload("Editor").Where("content_type = ? AND content_id = ?", debateContentType, debateID).Order("version_number DESC").Find(&revisions).Error; err != nil {
		return nil, err
	}
	result := make([]DebateRevisionDTO, 0, len(revisions))
	for _, revision := range revisions {
		item, err := revisionDTO(revision)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) GetRevision(debateID, revisionID uuid.UUID) (DebateRevisionDTO, error) {
	var revision model.Revision
	err := s.db.Preload("Editor").First(&revision, "id = ? AND content_type = ? AND content_id = ?", revisionID, debateContentType, debateID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DebateRevisionDTO{}, apperr.NotFound("debate.revision_not_found", "Revision not found")
		}
		return DebateRevisionDTO{}, err
	}
	result, err := revisionDTO(revision)
	if err != nil {
		return DebateRevisionDTO{}, err
	}
	var refs []model.DebateRevisionReference
	if err := s.db.Where("revision_id = ?", revision.ID).Order("created_at ASC, id ASC").Find(&refs).Error; err != nil {
		return DebateRevisionDTO{}, err
	}
	result.References = referenceDTOs(refs)
	return result, nil
}

func (s *Service) DiffRevisions(debateID, revisionID, againstID uuid.UUID) (RevisionDiffDTO, error) {
	revision, err := s.GetRevision(debateID, revisionID)
	if err != nil {
		return RevisionDiffDTO{}, err
	}
	against, err := s.GetRevision(debateID, againstID)
	if err != nil {
		return RevisionDiffDTO{}, err
	}
	return RevisionDiffDTO{
		RevisionID: revision.ID, AgainstID: against.ID,
		Changes: map[string]RevisionFieldDiff{
			"title":       fieldDiff(against.Snapshot.Title, revision.Snapshot.Title),
			"description": fieldDiff(against.Snapshot.Description, revision.Snapshot.Description),
			"content":     fieldDiff(against.Snapshot.Content, revision.Snapshot.Content),
			"tags":        fieldDiff(against.Snapshot.Tags, revision.Snapshot.Tags),
		},
	}, nil
}

func (s *Service) RevertRevision(user authctx.CurrentUser, debateID, revisionID uuid.UUID, req RevertRevisionRequest) (DebateDTO, error) {
	target, err := s.GetRevision(debateID, revisionID)
	if err != nil {
		return DebateDTO{}, err
	}
	if strings.TrimSpace(req.EditSummary) == "" {
		return DebateDTO{}, apperr.BadRequest("validation.invalid_request", "edit_summary is required")
	}
	if err := s.requireActiveUser(user); err != nil {
		return DebateDTO{}, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		debate, current, err := lockWikiForEdit(tx, debateID, req.BaseRevisionID)
		if err != nil {
			return err
		}
		if err := ensureWikiEditable(tx, user, debate); err != nil {
			return err
		}
		if err := tx.Model(&current).Update("is_current", false).Error; err != nil {
			return err
		}
		revision, err := createRevision(tx, debate.ID, user.ID, &current.ID, current.VersionNumber+1, target.Snapshot, strings.TrimSpace(req.EditSummary), "revert")
		if err != nil {
			return err
		}
		if err := tx.Model(&debate).Updates(map[string]any{
			"title": target.Snapshot.Title, "description": target.Snapshot.Description,
			"content": target.Snapshot.Content, "tags": pq.StringArray(target.Snapshot.Tags),
			"current_revision_id": revision.ID,
		}).Error; err != nil {
			return err
		}
		debate.Title, debate.Description, debate.Content = target.Snapshot.Title, target.Snapshot.Description, target.Snapshot.Content
		debate.Tags, debate.CurrentRevisionID = pq.StringArray(target.Snapshot.Tags), &revision.ID
		_, err = s.projectReferences(tx, user, debate, revision, current.ID)
		return err
	})
	if err != nil {
		return DebateDTO{}, err
	}
	return s.GetDebate(debateID)
}

func createRevision(tx *gorm.DB, debateID, editorID uuid.UUID, previousID *uuid.UUID, version int, snapshot DebateSnapshot, summary, editType string) (model.Revision, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return model.Revision{}, err
	}
	revision := model.Revision{
		ContentType: debateContentType, ContentID: debateID, VersionNumber: version,
		PreviousRevisionID: previousID, ContentSnapshot: raw, EditorID: editorID,
		EditSummary: summary, EditType: editType, Status: "approved", IsCurrent: true,
	}
	err = tx.Create(&revision).Error
	return revision, err
}

func revisionDTO(revision model.Revision) (DebateRevisionDTO, error) {
	var snapshot DebateSnapshot
	if err := json.Unmarshal(revision.ContentSnapshot, &snapshot); err != nil {
		return DebateRevisionDTO{}, err
	}
	return DebateRevisionDTO{
		ID: revision.ID, VersionNumber: revision.VersionNumber, PreviousRevisionID: revision.PreviousRevisionID,
		EditorID: revision.EditorID, Editor: revision.Editor, EditSummary: revision.EditSummary,
		EditType: revision.EditType, Status: revision.Status, IsCurrent: revision.IsCurrent,
		CreatedAt: revision.CreatedAt, Snapshot: snapshot,
	}, nil
}

func fieldDiff(before, after any) RevisionFieldDiff {
	return RevisionFieldDiff{Before: before, After: after, Changed: !reflect.DeepEqual(before, after)}
}

func lockWikiForEdit(tx *gorm.DB, debateID, baseRevisionID uuid.UUID) (model.Debate, model.Revision, error) {
	var debate model.Debate
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&debate, "id = ?", debateID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Debate{}, model.Revision{}, apperr.NotFound("debate.not_found", "Debate not found")
		}
		return model.Debate{}, model.Revision{}, err
	}
	if debate.CurrentRevisionID == nil {
		return model.Debate{}, model.Revision{}, apperr.Conflict("debate.edit_conflict", "Debate has no current revision")
	}
	var current model.Revision
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ? AND content_type = ? AND content_id = ?", *debate.CurrentRevisionID, debateContentType, debate.ID).Error; err != nil {
		return model.Debate{}, model.Revision{}, err
	}
	if baseRevisionID != current.ID {
		err := apperr.Conflict("debate.edit_conflict", "The debate changed while you were editing")
		err.Details["base_revision_id"] = baseRevisionID
		err.Details["current_revision_id"] = current.ID
		return model.Debate{}, model.Revision{}, err
	}
	return debate, current, nil
}

func ensureWikiEditable(tx *gorm.DB, user authctx.CurrentUser, debate model.Debate) error {
	if debate.Status == model.DebateStatusArchived {
		return apperr.Conflict("debate.archived", "Archived debates cannot be edited")
	}
	if authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return nil
	}
	var protection model.ContentProtection
	result := tx.Where("content_type = ? AND content_id = ?", debateContentType, debate.ID).Limit(1).Find(&protection)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 && protection.ProtectionLevel == "full" && (protection.ExpiresAt == nil || protection.ExpiresAt.After(time.Now())) {
		return apperr.Forbidden("debate.protected", "This debate is protected")
	}
	return nil
}

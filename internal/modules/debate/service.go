package debate

import (
	"errors"
	"strings"

	"atoman/internal/model"
	"atoman/internal/modules/comment"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/resourceref"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const debateContentType = "debate"

type Service struct {
	db        *gorm.DB
	repo      *Repo
	resources *resourceref.Registry
}

func NewService(db *gorm.DB, _ ...*comment.Service) *Service {
	return &Service{db: db, repo: NewRepo(db), resources: NewResourceRegistry(db)}
}

func (s *Service) ListDebates(query ListDebatesQuery) ([]model.Debate, int64, error) {
	return s.repo.ListDebates(query)
}

func (s *Service) GetDebate(id uuid.UUID) (DebateDTO, error) {
	if id == uuid.Nil {
		return DebateDTO{}, apperr.BadRequest("validation.invalid_request", "debate_id is required")
	}
	debate, err := s.repo.GetDebate(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DebateDTO{}, apperr.NotFound("debate.not_found", "Debate not found")
		}
		return DebateDTO{}, err
	}
	return s.attachCurrentReferences(debate)
}

func (s *Service) CreateDebate(user authctx.CurrentUser, req CreateDebateRequest) (DebateDTO, error) {
	if err := s.requireActiveUser(user); err != nil {
		return DebateDTO{}, err
	}
	snapshot, err := normalizedSnapshot(req.Title, req.Description, req.Content, req.Tags)
	if err != nil {
		return DebateDTO{}, err
	}
	sourceIDs, err := debateReferenceIDs(snapshot.Content)
	if err != nil {
		return DebateDTO{}, err
	}
	var debateID uuid.UUID
	err = s.db.Transaction(func(tx *gorm.DB) error {
		lockedDebates, err := lockDebatesInOrder(tx, sourceIDs)
		if err != nil {
			return err
		}
		debate := model.Debate{
			UserID: user.ID, Title: snapshot.Title, Description: snapshot.Description,
			Content: snapshot.Content, Tags: pq.StringArray(snapshot.Tags), Status: model.DebateStatusActive,
		}
		if err := tx.Create(&debate).Error; err != nil {
			return err
		}
		revision, err := createRevision(tx, debate.ID, user.ID, nil, 1, snapshot, "Created", "creation")
		if err != nil {
			return err
		}
		// The new target row is owned by this transaction; source rows were locked first.
		lockedDebates[debate.ID] = debate
		if _, err := s.projectReferences(tx, user, debate, revision, uuid.Nil, lockedDebates); err != nil {
			return err
		}
		if err := tx.Model(&debate).Update("current_revision_id", revision.ID).Error; err != nil {
			return err
		}
		debateID = debate.ID
		return nil
	})
	if err != nil {
		return DebateDTO{}, err
	}
	return s.GetDebate(debateID)
}

func (s *Service) ArchiveDebate(user authctx.CurrentUser, debateID uuid.UUID) (DebateDTO, error) {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return DebateDTO{}, apperr.Forbidden("debate.admin_required", "Administrator access required")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var debate model.Debate
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&debate, "id = ?", debateID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return apperr.NotFound("debate.not_found", "Debate not found")
			}
			return err
		}
		if err := tx.Model(&debate).Update("status", model.DebateStatusArchived).Error; err != nil {
			return err
		}
		return tx.Model(&model.DebateRelation{}).
			Where("source_debate_id = ? AND status = ?", debateID, model.DebateRelationActive).
			Update("status", model.DebateRelationUnavailable).Error
	})
	if err != nil {
		return DebateDTO{}, err
	}
	return s.GetDebate(debateID)
}

func (s *Service) SetProtection(user authctx.CurrentUser, debateID uuid.UUID, req ProtectionRequest) error {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("debate.admin_required", "Administrator access required")
	}
	if req.ProtectionLevel != "none" && req.ProtectionLevel != "semi" && req.ProtectionLevel != "full" {
		return apperr.BadRequest("validation.invalid_request", "invalid protection_level")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		locked, err := lockDebatesInOrder(tx, []uuid.UUID{debateID})
		if err != nil {
			return err
		}
		if _, ok := locked[debateID]; !ok {
			return apperr.NotFound("debate.not_found", "Debate not found")
		}
		if err := tx.Unscoped().Where("content_type = ? AND content_id = ?", debateContentType, debateID).Delete(&model.ContentProtection{}).Error; err != nil {
			return err
		}
		if req.ProtectionLevel == "none" {
			return nil
		}
		return tx.Create(&model.ContentProtection{
			ContentType: debateContentType, ContentID: debateID, ProtectionLevel: req.ProtectionLevel,
			ProtectedBy: user.ID, Reason: strings.TrimSpace(req.Reason), ExpiresAt: req.ExpiresAt,
		}).Error
	})
}

func (s *Service) DeleteProtection(user authctx.CurrentUser, debateID uuid.UUID) error {
	if !authctx.RoleAtLeast(user.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("debate.admin_required", "Administrator access required")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		locked, err := lockDebatesInOrder(tx, []uuid.UUID{debateID})
		if err != nil {
			return err
		}
		if _, ok := locked[debateID]; !ok {
			return apperr.NotFound("debate.not_found", "Debate not found")
		}
		return tx.Where("content_type = ? AND content_id = ?", debateContentType, debateID).Delete(&model.ContentProtection{}).Error
	})
}

func (s *Service) requireActiveUser(user authctx.CurrentUser) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	var count int64
	if err := s.db.Model(&model.User{}).Where("uuid = ? AND is_active = ?", user.ID, true).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.Forbidden("auth.inactive", "Account is inactive")
	}
	return nil
}

func normalizedSnapshot(title, description, content string, tags []string) (DebateSnapshot, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return DebateSnapshot{}, apperr.BadRequest("validation.invalid_request", "title is required")
	}
	if refs, err := resourceref.Parse(title); err != nil || len(refs) > 0 {
		return DebateSnapshot{}, apperr.BadRequest("debate.title_reference", "Title cannot contain resource references")
	}
	cleanTags := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			cleanTags = append(cleanTags, tag)
		}
	}
	return DebateSnapshot{Title: title, Description: strings.TrimSpace(description), Content: strings.TrimSpace(content), Tags: cleanTags}, nil
}

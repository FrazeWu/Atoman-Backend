package papers

import (
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	RoleProjectOwner  = "owner"
	RoleProjectEditor = "editor"
	RoleProjectViewer = "viewer"
	maxProjectMembers = 20
)

func memberRoleValid(role string) bool { return role == RoleProjectEditor || role == RoleProjectViewer }
func projectMissing() error            { return apperr.NotFound("papers.project_not_found", "Project not found") }

// All member permission checks for source reads and saves share the same
// project row lock as membership changes, restore and file mutations. Do not
// change lockOwned: compilation, snapshots and structural edits stay owner-only.
func (s *Service) lockAccessible(tx *gorm.DB, user authctx.CurrentUser, pid uuid.UUID, write bool) (model.PaperProject, error) {
	var p model.PaperProject
	strength := "SHARE"
	if write {
		strength = "UPDATE"
	}
	// Authorize against the current project and membership, not a client role.
	err := tx.Clauses(clause.Locking{Strength: strength}).
		Where("id=? AND (owner_id=? OR EXISTS (SELECT 1 FROM paper_project_members m WHERE m.project_id=paper_projects.id AND m.user_id=? AND m.role IN ('editor','viewer')))", pid, user.ID, user.ID).
		First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, projectMissing()
	}
	if err != nil {
		return p, err
	}
	if p.OwnerID == user.ID {
		p.AccessRole = RoleProjectOwner
		return p, nil
	}
	// Re-read after locking: a concurrent removal may have completed while
	// the first SELECT was waiting for the project row.
	var member model.PaperProjectMember
	err = tx.Where("project_id=? AND user_id=?", pid, user.ID).First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return p, projectMissing()
	}
	if err != nil {
		return p, err
	}
	if !memberRoleValid(member.Role) {
		return p, projectMissing()
	}
	if write && member.Role != RoleProjectEditor {
		return p, apperr.Forbidden("papers.read_only", "This project membership is read-only")
	}
	p.AccessRole = member.Role
	return p, nil
}

func (s *Service) ReadProject(user authctx.CurrentUser, pid uuid.UUID) (model.PaperProject, error) {
	var p model.PaperProject
	if err := s.RequireAccess(user); err != nil {
		return p, err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error { var err error; p, err = s.lockAccessible(tx, user, pid, false); return err })
	return p, err
}

func (s *Service) ListMembers(user authctx.CurrentUser, pid uuid.UUID) ([]model.PaperProjectMember, error) {
	if err := s.RequireAccess(user); err != nil {
		return nil, err
	}
	rows := make([]model.PaperProjectMember, 0)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockOwned(tx, user, pid); err != nil {
			return err
		}
		return tx.Where("project_id=?", pid).Order("created_at ASC,user_id ASC").Limit(maxProjectMembers).Find(&rows).Error
	})
	return rows, err
}

func (s *Service) SetMember(user authctx.CurrentUser, pid, target uuid.UUID, role string) (model.PaperProjectMember, error) {
	var member model.PaperProjectMember
	if err := s.RequireAccess(user); err != nil {
		return member, err
	}
	if target == uuid.Nil || !memberRoleValid(role) {
		return member, apperr.BadRequest("papers.invalid_member", "Use an existing account UUID and editor or viewer role")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		p, err := s.lockOwned(tx, user, pid)
		if err != nil {
			return err
		}
		if p.OwnerID == target {
			return apperr.BadRequest("papers.invalid_member", "Owner cannot be changed through membership")
		}
		// Sharing cannot bypass the module's private invitation gate.
		var eligible int64
		if err := tx.Model(&model.PaperAccessGrant{}).
			Where("user_id=? AND status='active' AND (expires_at IS NULL OR expires_at>?) AND EXISTS (?)", target, time.Now().UTC(), tx.Model(&model.User{}).Select("1").Where("uuid=? AND is_active=?", target, true)).Count(&eligible).Error; err != nil {
			return err
		}
		if eligible != 1 {
			return apperr.BadRequest("papers.member_ineligible", "Account is not eligible for Papers access")
		}
		var existing model.PaperProjectMember
		err = tx.Where("project_id=? AND user_id=?", pid, target).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var count int64
			if err := tx.Model(&model.PaperProjectMember{}).Where("project_id=?", pid).Count(&count).Error; err != nil {
				return err
			}
			if count >= maxProjectMembers {
				return apperr.Conflict("papers.member_limit", "Project membership limit reached")
			}
		}
		member = model.PaperProjectMember{ProjectID: pid, UserID: target, Role: role}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_id"}, {Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"role", "updated_at"})}).Create(&member).Error; err != nil {
			return err
		}
		if err := tx.Where("project_id=? AND user_id=?", pid, target).First(&member).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.member." + role, EntityType: "paper_project", EntityID: &pid, Metadata: map[string]any{"user_id": target.String()}})
	})
	return member, err
}

func (s *Service) RemoveMember(user authctx.CurrentUser, pid, target uuid.UUID) error {
	if err := s.RequireAccess(user); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockOwned(tx, user, pid); err != nil {
			return err
		}
		result := tx.Where("project_id=? AND user_id=?", pid, target).Delete(&model.PaperProjectMember{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.NotFound("papers.member_not_found", "Project member not found")
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.member.remove", EntityType: "paper_project", EntityID: &pid, Metadata: map[string]any{"user_id": target.String()}})
	})
}

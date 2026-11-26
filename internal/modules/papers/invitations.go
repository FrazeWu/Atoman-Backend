package papers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

const invitationLifetime = 7 * 24 * time.Hour
const maxOpenInvitations = 20

type CreatedInvitation struct {
	model.PaperProjectInvitation
	Token string `json:"token"`
}
type AcceptedInvitation struct {
	ProjectID uuid.UUID `json:"project_id"`
	Role      string    `json:"role"`
}

func invitationMissing() error {
	return apperr.NotFound("papers.invitation_unavailable", "Invitation is invalid, expired, used or revoked")
}
func invitationHash(token string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return "", invitationMissing()
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Check active account and module grant under the same project lock ordering
// as collaborative writes. Links cannot bypass module-level eligibility.
func lockInvitationEligibility(tx *gorm.DB, uid uuid.UUID) error {
	return lockInvitationEligibilityWithAccountLock(tx, uid, "SHARE")
}

// Grant before account is the common lock order. Project creation takes
// UPDATE directly, instead of upgrading a shared account lock.
func lockInvitationEligibilityWithAccountLock(tx *gorm.DB, uid uuid.UUID, accountStrength string) error {
	var grant model.PaperAccessGrant
	err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("user_id=? AND status='active'", uid).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && grant.ExpiresAt != nil && !grant.ExpiresAt.After(time.Now().UTC())) {
		return apperr.Forbidden("papers.invite_required", "Papers requires an active invitation")
	}
	if err != nil {
		return err
	}
	var account model.User
	err = tx.Clauses(clause.Locking{Strength: accountStrength}).Select("uuid").Where("uuid=? AND is_active=?", uid, true).First(&account).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.Forbidden("papers.invite_required", "Active account required")
	}
	return err
}

func (s *Service) CreateInvitation(user authctx.CurrentUser, pid uuid.UUID, role string) (CreatedInvitation, error) {
	var out CreatedInvitation
	if err := s.RequireAccess(user); err != nil {
		return out, err
	}
	if !memberRoleValid(role) {
		return out, apperr.BadRequest("papers.invalid_member", "Choose editor or viewer")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return out, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash, err := invitationHash(token)
	if err != nil {
		return out, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockOwned(tx, user, pid); err != nil {
			return err
		}
		now := time.Now().UTC()
		// Keep bounded records for each project; the audit trail remains separate.
		if err := tx.Where("project_id=? AND (expires_at<=? OR used_at IS NOT NULL OR revoked_at IS NOT NULL)", pid, now).Delete(&model.PaperProjectInvitation{}).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&model.PaperProjectInvitation{}).Where("project_id=?", pid).Count(&count).Error; err != nil {
			return err
		}
		if count >= maxOpenInvitations {
			return apperr.Conflict("papers.invitation_limit", "Too many active invitation links")
		}
		row := model.PaperProjectInvitation{ID: uuid.New(), ProjectID: pid, TokenHash: hash, Role: role, ExpiresAt: now.Add(invitationLifetime), CreatedAt: now}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.invitation.create", EntityType: "paper_project", EntityID: &pid, Metadata: map[string]any{"invitation_id": row.ID.String(), "role": role}}); err != nil {
			return err
		}
		out = CreatedInvitation{PaperProjectInvitation: row, Token: token}
		return nil
	})
	if err != nil {
		return CreatedInvitation{}, err
	}
	return out, nil
}
func (s *Service) ListInvitations(user authctx.CurrentUser, pid uuid.UUID) ([]model.PaperProjectInvitation, error) {
	rows := make([]model.PaperProjectInvitation, 0)
	if err := s.RequireAccess(user); err != nil {
		return nil, err
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockOwned(tx, user, pid); err != nil {
			return err
		}
		return tx.Where("project_id=? AND used_at IS NULL AND revoked_at IS NULL AND expires_at>?", pid, time.Now().UTC()).Order("created_at DESC").Limit(maxOpenInvitations).Find(&rows).Error
	})
	return rows, err
}
func (s *Service) RevokeInvitation(user authctx.CurrentUser, pid, iid uuid.UUID) error {
	if err := s.RequireAccess(user); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockOwned(tx, user, pid); err != nil {
			return err
		}
		result := tx.Model(&model.PaperProjectInvitation{}).Where("project_id=? AND id=? AND used_at IS NULL AND revoked_at IS NULL", pid, iid).Update("revoked_at", time.Now().UTC())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return invitationMissing()
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.invitation.revoke", EntityType: "paper_project", EntityID: &pid, Metadata: map[string]any{"invitation_id": iid.String()}})
	})
}

func (s *Service) AcceptInvitation(user authctx.CurrentUser, token string) (AcceptedInvitation, error) {
	var out AcceptedInvitation
	if err := s.RequireAccess(user); err != nil {
		return out, err
	}
	hash, err := invitationHash(token)
	if err != nil {
		return out, err
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Locate the project, then lock the project before re-reading the link.
		// All membership mutations, trash, revocation and accept serialize here.
		var link model.PaperProjectInvitation
		err := tx.Where("token_hash=?", hash).First(&link).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return invitationMissing()
		}
		if err != nil {
			return err
		}
		var project model.PaperProject
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", link.ProjectID).First(&project).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return invitationMissing()
		}
		if err != nil {
			return err
		}
		err = tx.Where("id=? AND token_hash=? AND used_at IS NULL AND revoked_at IS NULL AND expires_at>?", link.ID, hash, time.Now().UTC()).First(&link).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return invitationMissing()
		}
		if err != nil {
			return err
		}
		if !memberRoleValid(link.Role) {
			return invitationMissing()
		}
		if err := lockInvitationEligibility(tx, user.ID); err != nil {
			return err
		}
		if project.OwnerID != user.ID {
			if err := lockInvitationEligibility(tx, project.OwnerID); err != nil {
				return invitationMissing()
			}
		} else {
			return apperr.BadRequest("papers.invitation_owner", "You already own this project")
		}
		var member model.PaperProjectMember
		err = tx.Where("project_id=? AND user_id=?", project.ID, user.ID).First(&member).Error
		if err == nil {
			// Never overwrite an explicit role set by the owner using an old link.
			// The owner must change existing member permissions directly.
			if !memberRoleValid(member.Role) {
				return invitationMissing()
			}
			used := time.Now().UTC()
			result := tx.Model(&model.PaperProjectInvitation{}).Where("id=? AND used_at IS NULL AND revoked_at IS NULL AND expires_at>?", link.ID, used).Update("used_at", used)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return invitationMissing()
			}
			if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.invitation.accept_existing", EntityType: "paper_project", EntityID: &project.ID, Metadata: map[string]any{"invitation_id": link.ID.String(), "role": member.Role}}); err != nil {
				return err
			}
			out = AcceptedInvitation{ProjectID: project.ID, Role: member.Role}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var count int64
		if err := tx.Model(&model.PaperProjectMember{}).Where("project_id=?", project.ID).Count(&count).Error; err != nil {
			return err
		}
		if count >= maxProjectMembers {
			return apperr.Conflict("papers.member_limit", "Project membership limit reached")
		}
		now := time.Now().UTC()
		result := tx.Model(&model.PaperProjectInvitation{}).Where("id=? AND used_at IS NULL AND revoked_at IS NULL AND expires_at>?", link.ID, now).Update("used_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return invitationMissing()
		}
		member = model.PaperProjectMember{ProjectID: project.ID, UserID: user.ID, Role: link.Role}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.invitation.accept", EntityType: "paper_project", EntityID: &project.ID, Metadata: map[string]any{"invitation_id": link.ID.String(), "role": link.Role}}); err != nil {
			return err
		}
		out = AcceptedInvitation{ProjectID: project.ID, Role: link.Role}
		return nil
	})
	if err != nil {
		return AcceptedInvitation{}, err
	}
	return out, nil
}

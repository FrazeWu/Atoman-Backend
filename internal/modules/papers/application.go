package papers

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/audit"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ApplicationView struct {
	model.PaperAccessApplication
	Username string `json:"username,omitempty"`
}

func requireReviewer(actor authctx.CurrentUser) error {
	if actor.ID == uuid.Nil || !authctx.RoleAtLeast(actor.Role, authctx.RoleAdmin) {
		return apperr.Forbidden("papers.admin_required", "Administrator permission required")
	}
	return nil
}

func (s *Service) Application(user authctx.CurrentUser) (*ApplicationView, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	var a model.PaperAccessApplication
	err := s.db.Where("user_id=?", user.ID).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ApplicationView{PaperAccessApplication: a}, nil
}

func (s *Service) Apply(user authctx.CurrentUser, reason string) (*ApplicationView, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	reason = strings.TrimSpace(reason)
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) < 5 || utf8.RuneCountInString(reason) > 1000 || strings.ContainsRune(reason, 0) {
		return nil, apperr.BadRequest("papers.invalid_application", "Application reason must contain 5–1000 characters")
	}
	var a model.PaperAccessApplication
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var owner model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid=? AND is_active=?", user.ID, true).First(&owner).Error; err != nil {
			return err
		}
		var count int64
		now := time.Now().UTC()
		if err := tx.Model(&model.PaperAccessGrant{}).Where("user_id=? AND status='active' AND (expires_at IS NULL OR expires_at>?)", user.ID, now).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return apperr.Conflict("papers.already_approved", "Papers access is already active")
		}
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=?", user.ID).First(&a).Error
		if err == nil {
			if a.Status == "pending" {
				return nil
			}
			if a.Status == "approved" {
				return apperr.Forbidden("papers.access_revoked", "Contact an administrator to restore access")
			}
			if a.Status != "rejected" || a.ReviewedAt == nil || now.Sub(*a.ReviewedAt) < 7*24*time.Hour {
				return apperr.Conflict("papers.reapply_later", "A rejected application may be resubmitted after seven days")
			}
			// Rejected records cannot have queued approval mail. Preserve the
			// history in audit, renew the application ID for a new review.
			if err := tx.Delete(&a).Error; err != nil {
				return err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		a = model.PaperAccessApplication{ID: uuid.New(), UserID: user.ID, Reason: reason, Status: "pending"}
		if err := tx.Create(&a).Error; err != nil {
			return err
		}
		return audit.Record(tx, audit.Entry{ActorID: &user.ID, Action: "papers.access.apply", EntityType: "paper_access_application", EntityID: &a.ID})
	})
	if err != nil {
		return nil, err
	}
	return &ApplicationView{PaperAccessApplication: a}, nil
}

func (s *Service) Applications(actor authctx.CurrentUser, status string, page int) ([]ApplicationView, error) {
	if err := requireReviewer(actor); err != nil {
		return nil, err
	}
	if status != "pending" && status != "approved" && status != "rejected" {
		return nil, apperr.BadRequest("papers.invalid_status", "Invalid application status")
	}
	if page < 1 || page > 1000 {
		return nil, apperr.BadRequest("papers.invalid_page", "Invalid application page")
	}
	rows := []ApplicationView{}
	err := s.db.Table("paper_access_applications AS a").Select("a.*, u.username").
		Joins("JOIN \"Users\" u ON u.uuid=a.user_id").Where("a.status=?", status).
		Order("a.created_at DESC,a.id DESC").Limit(50).Offset((page - 1) * 50).Scan(&rows).Error
	return rows, err
}

func (s *Service) ReviewApplication(actor authctx.CurrentUser, id uuid.UUID, decision, note string) (*ApplicationView, error) {
	if err := requireReviewer(actor); err != nil {
		return nil, err
	}
	note = strings.TrimSpace(note)
	if (decision != "approved" && decision != "rejected") || !utf8.ValidString(note) || utf8.RuneCountInString(note) > 1000 || strings.ContainsRune(note, 0) || (decision == "rejected" && note == "") {
		return nil, apperr.BadRequest("papers.invalid_review", "Invalid review decision or reason")
	}
	var a model.PaperAccessApplication
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// User-before-application lock order also used by Apply.
		if err := tx.Where("id=?", id).First(&a).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return apperr.NotFound("papers.application_not_found", "Application not found")
		} else if err != nil {
			return err
		}
		var owner model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("uuid=? AND is_active=?", a.UserID, true).First(&owner).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).First(&a).Error; err != nil {
			return err
		}
		if a.Status != "pending" {
			if a.Status == decision {
				return nil
			} // exact same review is idempotent
			return apperr.Conflict("papers.review_completed", "Application already reviewed")
		}
		now := time.Now().UTC()
		updates := map[string]any{"status": decision, "reviewer_id": actor.ID, "review_note": note, "reviewed_at": now}
		if decision == "approved" {
			grant := model.PaperAccessGrant{UserID: a.UserID, Status: "active", GrantedBy: &actor.ID}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "user_id"}}, DoUpdates: clause.AssignmentColumns([]string{"status", "granted_by", "expires_at", "updated_at"})}).Create(&grant).Error; err != nil {
				return err
			}
			// The recipient is from the authenticated account, never request input.
			updates["notification_status"] = "pending"
			updates["notification_recipient"] = owner.Email
			updates["notification_next_at"] = now
		}
		if err := tx.Model(&a).Updates(updates).Error; err != nil {
			return err
		}
		if err := audit.Record(tx, audit.Entry{ActorID: &actor.ID, Action: "papers.access." + decision, EntityType: "paper_access_application", EntityID: &a.ID}); err != nil {
			return err
		}
		return tx.Where("id=?", id).First(&a).Error
	})
	if err != nil {
		return nil, err
	}
	return &ApplicationView{PaperAccessApplication: a}, nil
}

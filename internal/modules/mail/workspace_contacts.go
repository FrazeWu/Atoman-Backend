package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const mailContactLimit = 500

// Contacts are private to one Atoman user and encrypted with the same
// authenticated, per-record envelope as private drafts.
type MailContactInfo struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Notes     string    `json:"notes,omitempty"`
	Revision  uint64    `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}
type MailContactInput struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Notes    string `json:"notes"`
	Revision uint64 `json:"revision"`
}

func validateMailContact(c MailContactInput) (MailContactInput, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	c.Notes = strings.TrimSpace(c.Notes)
	if c.Email == "" || len(c.Email) > 254 || len(c.Name) > 200 || len(c.Notes) > 2048 ||
		!utf8.ValidString(c.Name) || !utf8.ValidString(c.Notes) || strings.ContainsAny(c.Name+c.Notes, "\x00") ||
		strings.ContainsAny(c.Email, "\r\n\x00 ") {
		return c, apperr.BadRequest("mail.invalid_contact", "Invalid contact")
	}
	parsed, err := mail.ParseAddress(c.Email)
	if err != nil || parsed.Address != c.Email {
		return c, apperr.BadRequest("mail.invalid_contact", "Invalid email address")
	}
	for _, r := range c.Email {
		if r > 127 {
			return c, apperr.BadRequest("mail.invalid_contact", "Use an ASCII email address")
		}
	}
	return c, nil
}
func (s *MailWorkspaceService) contactsWithDB(db *gorm.DB, userID uuid.UUID) ([]MailContactInfo, error) {
	var rows []model.MailContact
	if err := db.Where("user_id=?", userID).Order("updated_at DESC").Limit(mailContactLimit + 1).Find(&rows).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	if len(rows) > mailContactLimit {
		return nil, apperr.New(503, "mail.contact_limit", "Address book exceeds limit", nil)
	}
	out := make([]MailContactInfo, 0, len(rows))
	for _, row := range rows {
		raw, err := s.vault.Open(userID, row.ID, row.Ciphertext)
		if err != nil {
			return nil, workspaceErr(err)
		}
		var data MailContactInput
		if err = json.Unmarshal(raw, &data); err != nil {
			return nil, workspaceErr(err)
		}
		out = append(out, MailContactInfo{ID: row.ID, Name: data.Name, Email: data.Email, Notes: data.Notes, Revision: row.Revision, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}
func (s *MailWorkspaceService) Contacts(ctx context.Context, u authctx.CurrentUser) ([]MailContactInfo, error) {
	if _, _, err := s.auth(ctx, u); err != nil {
		return nil, err
	}
	return s.contactsWithDB(s.db.WithContext(ctx), u.ID)
}
func (s *MailWorkspaceService) SaveContact(ctx context.Context, u authctx.CurrentUser, id uuid.UUID, in MailContactInput) (MailContactInfo, error) {
	if _, _, err := s.auth(ctx, u); err != nil {
		return MailContactInfo{}, err
	}
	normalized, err := validateMailContact(in)
	if err != nil {
		return MailContactInfo{}, err
	}
	var result MailContactInfo
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", "mail-contacts:"+u.ID.String()).Error; err != nil {
				return err
			}
		}
		list, err := s.contactsWithDB(tx, u.ID)
		if err != nil {
			return err
		}
		for _, c := range list {
			if c.Email == normalized.Email && c.ID != id {
				return apperr.Conflict("mail.contact_duplicate", "Contact already exists")
			}
		}
		now := time.Now().UTC()
		if id == uuid.Nil {
			if in.Revision != 0 || len(list) >= mailContactLimit {
				return apperr.BadRequest("mail.contact_limit", "Contact limit reached")
			}
			id = uuid.New()
		} else {
			if in.Revision == 0 {
				return apperr.Conflict("mail.contact_conflict", "Refresh contact before editing")
			}
			match := false
			for _, c := range list {
				if c.ID == id {
					match = true
					break
				}
			}
			if !match {
				return apperr.NotFound("mail.contact_missing", "Contact unavailable")
			}
		}
		plaintext, _ := json.Marshal(normalized)
		cipher, err := s.vault.Seal(u.ID, id, plaintext)
		if err != nil {
			return workspaceErr(err)
		}
		if in.Revision == 0 {
			row := model.MailContact{ID: id, UserID: u.ID, Ciphertext: cipher, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if err = tx.Create(&row).Error; err != nil {
				return err
			}
			result = MailContactInfo{ID: id, Name: normalized.Name, Email: normalized.Email, Notes: normalized.Notes, Revision: 1, UpdatedAt: now}
		} else {
			updated := tx.Model(&model.MailContact{}).Where("id=? AND user_id=? AND revision=?", id, u.ID, in.Revision).Updates(map[string]any{"ciphertext": cipher, "revision": in.Revision + 1, "updated_at": now})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				return apperr.Conflict("mail.contact_conflict", "Contact changed; reload")
			}
			result = MailContactInfo{ID: id, Name: normalized.Name, Email: normalized.Email, Notes: normalized.Notes, Revision: in.Revision + 1, UpdatedAt: now}
		}
		return nil
	})
	if err != nil {
		return MailContactInfo{}, err
	}
	return result, nil
}
func (s *MailWorkspaceService) DeleteContact(ctx context.Context, u authctx.CurrentUser, id uuid.UUID) error {
	if _, _, err := s.auth(ctx, u); err != nil {
		return err
	}
	if id == uuid.Nil {
		return apperr.BadRequest("mail.invalid_contact", "Invalid contact")
	}
	result := s.db.WithContext(ctx).Where("id=? AND user_id=?", id, u.ID).Delete(&model.MailContact{})
	if result.Error != nil {
		return apperr.Internal(result.Error)
	}
	if result.RowsAffected == 0 {
		return apperr.NotFound("mail.contact_missing", "Contact unavailable")
	}
	return nil
}

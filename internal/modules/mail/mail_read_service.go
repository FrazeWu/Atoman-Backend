package mail

import (
	"context"
	"errors"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MailReadService maps the authenticated Atoman user to the *same* Stalwart
// account. Browsers never pass credentials or choose the JMAP account ID.
type MailReadService struct {
	db      *gorm.DB
	client  *StalwartJMAPClient
	vault   *MailCredentialVault
	enabled bool
}

func NewMailReadService(db *gorm.DB, client *StalwartJMAPClient, vault *MailCredentialVault, enabled bool) *MailReadService {
	return &MailReadService{db: db, client: client, vault: vault, enabled: enabled}
}

func (s *MailReadService) authorizedClient(ctx context.Context, user authctx.CurrentUser) (StalwartCredential, string, error) {
	if user.ID == uuid.Nil {
		return StalwartCredential{}, "", apperr.Unauthorized("Login required")
	}
	if s == nil || !s.enabled || s.db == nil || s.client == nil || s.vault == nil {
		return StalwartCredential{}, "", apperr.New(503, "mail.read_not_ready", "Mail reading is not enabled", nil)
	}
	var mailAccount model.MailAccount
	err := s.db.Where("user_id = ? AND status = ?", user.ID, "active").Take(&mailAccount).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return StalwartCredential{}, "", apperr.Forbidden("mail.mailbox_inactive", "Mailbox is not active")
	}
	if err != nil {
		return StalwartCredential{}, "", apperr.Internal(err)
	}
	if mailAccount.StalwartAccountID == nil || *mailAccount.StalwartAccountID == "" {
		return StalwartCredential{}, "", apperr.New(503, "mail.account_mapping_unavailable", "Mailbox mapping is incomplete", nil)
	}
	var stored model.MailAccessCredential
	if err := s.db.Where("user_id = ?", user.ID).Take(&stored).Error; err != nil {
		return StalwartCredential{}, "", apperr.New(503, "mail.credential_unavailable", "Mailbox credential is unavailable", nil)
	}
	if stored.KeyVersion != 1 || stored.StalwartAccountID != *mailAccount.StalwartAccountID {
		return StalwartCredential{}, "", apperr.New(503, "mail.credential_mismatch", "Mailbox credential mapping is invalid", nil)
	}
	key, err := s.vault.Open(user.ID, stored.StalwartAccountID, stored.Ciphertext, stored.Nonce)
	if err != nil {
		return StalwartCredential{}, "", apperr.New(503, "mail.credential_decryption_failed", "Mailbox credential is unavailable", nil)
	}
	userCredential := StalwartBearer(key)
	session, err := s.client.Session(ctx, userCredential)
	if err != nil {
		return StalwartCredential{}, "", apperr.New(503, "mail.upstream_unavailable", "Mailbox service unavailable", nil)
	}
	if session.PrimaryAccounts[jmapMailCapability] != stored.StalwartAccountID {
		return StalwartCredential{}, "", apperr.Forbidden("mail.account_scope_mismatch", "Mailbox session cannot access this account")
	}
	return userCredential, stored.StalwartAccountID, nil
}

func (s *MailReadService) Folders(ctx context.Context, user authctx.CurrentUser) ([]MailboxInfo, error) {
	credential, accountID, err := s.authorizedClient(ctx, user)
	if err != nil {
		return nil, err
	}
	folders, err := s.client.Mailboxes(ctx, credential, accountID)
	if err != nil {
		return nil, apperr.New(503, "mail.upstream_unavailable", "Mailbox folders unavailable", nil)
	}
	return folders, nil
}

func (s *MailReadService) Messages(ctx context.Context, user authctx.CurrentUser, mailboxID string, limit int) ([]EmailSummary, error) {
	if mailboxID == "" || limit < 1 || limit > 100 {
		return nil, apperr.BadRequest("mail.invalid_query", "Invalid mailbox or page size")
	}
	credential, accountID, err := s.authorizedClient(ctx, user)
	if err != nil {
		return nil, err
	}
	folders, err := s.client.Mailboxes(ctx, credential, accountID)
	if err != nil {
		return nil, apperr.New(503, "mail.upstream_unavailable", "Mailbox folders unavailable", nil)
	}
	allowed := false
	for _, folder := range folders {
		if folder.ID == mailboxID {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, apperr.NotFound("mail.folder_not_found", "Mailbox folder not found")
	}
	messages, err := s.client.ListEmails(ctx, credential, accountID, mailboxID, limit)
	if err != nil {
		return nil, apperr.New(503, "mail.upstream_unavailable", "Mailbox messages unavailable", nil)
	}
	return messages, nil
}

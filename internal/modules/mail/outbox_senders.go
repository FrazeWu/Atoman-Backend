package mail

import (
	"atoman/internal/platform/httpx"
	"context"
	"github.com/gin-gonic/gin"
	"net/http"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
)

// The browser receives only owner-visible addresses, not passwords, provider
// endpoints, SMTP credentials or JMAP identities. An opaque ID identifies a
// connected external account, and Queue reauthorizes it on every submission.
type MailSenderInfo struct {
	Address           string     `json:"address"`
	Provider          string     `json:"provider"`
	ExternalAccountID *uuid.UUID `json:"external_account_id,omitempty"`
	AliasID           *uuid.UUID `json:"alias_id,omitempty"`
}

func (s *MailOutboxService) Senders(ctx context.Context, u authctx.CurrentUser) ([]MailSenderInfo, error) {
	if u.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	if !s.ready() {
		return nil, apperr.New(503, "mail.send_not_ready", "Mail sending is not enabled", nil)
	}
	var own model.MailAccount
	if err := s.db.WithContext(ctx).Where("user_id=? AND status=?", u.ID, "active").Take(&own).Error; err != nil {
		return nil, apperr.Forbidden("mail.mailbox_inactive", "Active mailbox required")
	}
	if own.StalwartAccountID == nil || !validJMAPScopedID(*own.StalwartAccountID) {
		return nil, apperr.New(503, "mail.mailbox_unavailable", "Mailbox is not provisioned", nil)
	}
	out := []MailSenderInfo{{Address: own.Address, Provider: "atoman"}}
	if s.aliasesAllowed {
		var aliases []model.MailAlias
		if err := s.db.WithContext(ctx).Where("user_id=? AND stalwart_account_id=? AND status=?", u.ID, *own.StalwartAccountID, "active").Order("created_at ASC").Limit(maxMailAliases + 1).Find(&aliases).Error; err != nil {
			return nil, apperr.Internal(err)
		}
		if len(aliases) > maxMailAliases {
			return nil, apperr.New(503, "mail.alias_limit", "Too many aliases", nil)
		}
		for _, alias := range aliases {
			id := alias.ID
			out = append(out, MailSenderInfo{Address: alias.Address, Provider: "atoman-alias", AliasID: &id})
		}
	}
	if !s.externalSMTPAllowed {
		return out, nil
	}
	var rows []model.MailExternalAccount
	if err := s.db.WithContext(ctx).Where("user_id=?", u.ID).Order("created_at ASC").Limit(maxExternalAccounts + 1).Find(&rows).Error; err != nil {
		return nil, apperr.Internal(err)
	}
	if len(rows) > maxExternalAccounts {
		return nil, apperr.New(503, "mail.external_limit", "Too many external accounts", nil)
	}
	for _, row := range rows {
		creds, err := openExternalSMTPSecrets(s.vault, row)
		if err != nil {
			return nil, apperr.New(503, "mail.external_credential_unavailable", "Cannot resolve connected sender", nil)
		}
		id := row.ID
		out = append(out, MailSenderInfo{Address: creds.Address, Provider: row.Provider, ExternalAccountID: &id})
	}
	return out, nil
}

func (h *AccountHandler) mailSenders(c *gin.Context) {
	user, ok := h.workspaceUser(c)
	if !ok {
		return
	}
	if h.outbox == nil {
		httpx.Error(c, apperr.New(503, "mail.send_not_ready", "Mail sending is not enabled", nil))
		return
	}
	senders, err := h.outbox.Senders(c.Request.Context(), user)
	mailResult(c, http.StatusOK, senders, err)
}

package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"context"
	"errors"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"time"
)

// Reconcile only reads Stalwart acceptance evidence. It never resubmits an
// uncertain message: absence of a receipt cannot establish non-delivery.
func (s *MailOutboxService) Reconcile(ctx context.Context, user authctx.CurrentUser, id uuid.UUID) (MailOutboxInfo, error) {
	if s == nil || s.db == nil || user.ID == uuid.Nil {
		return MailOutboxInfo{}, apperr.Unauthorized("Login required")
	}
	var row model.MailPendingSend
	if e := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, user.ID).Take(&row).Error; e != nil {
		if errors.Is(e, gorm.ErrRecordNotFound) {
			return MailOutboxInfo{}, apperr.NotFound("mail.outbox_not_found", "Pending mail not found")
		}
		return MailOutboxInfo{}, workspaceErr(e)
	}
	if row.Status != "dispatching" && row.Status != "uncertain" {
		return mailOutboxInfo(row), nil
	}
	if row.ClaimedAt == nil || row.ClaimedAt.After(s.now().Add(-2*time.Minute)) {
		return mailOutboxInfo(row), apperr.Conflict("mail.reconcile_too_soon", "Wait two minutes before checking an interrupted submission")
	}
	sender, ok := s.deliverer.(*StalwartOutboxSubmitter)
	if !ok {
		return mailOutboxInfo(row), apperr.New(503, "mail.reconcile_unavailable", "Submission evidence is unavailable", nil)
	}
	reader := NewMailReadService(s.db, sender.client, sender.vault, true)
	cred, aid, e := reader.authorizedClient(ctx, user)
	if e != nil {
		return mailOutboxInfo(row), e
	}
	if aid != row.AccountID {
		return mailOutboxInfo(row), apperr.Forbidden("mail.account_mismatch", "Mail account changed")
	}
	emailID := row.ImportedEmailID
	if emailID == "" {
		var q struct {
			IDs []string `json:"ids"`
		}
		e = sender.client.call(ctx, cred, false, "Email/query", map[string]any{"accountId": aid, "filter": map[string]any{"header": []string{"Message-ID", row.ID.String() + "@" + MailDomain}}, "limit": 2}, &q)
		if e != nil {
			return mailOutboxInfo(row), workspaceErr(e)
		}
		if len(q.IDs) == 1 {
			emailID = q.IDs[0]
		}
	}
	accepted := row.SubmissionID != ""
	if !accepted && emailID != "" {
		var q struct {
			IDs []string `json:"ids"`
		}
		e = sender.client.jmapSubmissionCall(ctx, cred, "EmailSubmission/query", map[string]any{"accountId": aid, "filter": map[string]any{"emailIds": []string{emailID}}, "limit": 2}, &q)
		if e != nil {
			return mailOutboxInfo(row), workspaceErr(e)
		}
		if len(q.IDs) > 0 {
			accepted = true
			row.SubmissionID = q.IDs[0]
		}
	}
	status := "uncertain"
	updates := map[string]any{"status": status}
	if accepted {
		status = "sent"
		updates["status"] = status
		updates["submission_id"] = row.SubmissionID
		updates["imported_email_id"] = emailID
		updates["sent_at"] = s.now()
	}
	r := s.db.WithContext(ctx).Model(&model.MailPendingSend{}).Where("id = ? AND user_id = ? AND status IN ?", row.ID, user.ID, []string{"dispatching", "uncertain"}).Updates(updates)
	if r.Error != nil {
		return mailOutboxInfo(row), workspaceErr(r.Error)
	}
	if r.RowsAffected != 1 {
		return s.Status(ctx, user, id)
	}
	row.Status = status
	if accepted {
		if e := deleteAcceptedDraft(s.db.WithContext(ctx), row); e != nil {
			return mailOutboxInfo(row), workspaceErr(e)
		}
		_ = s.store.Delete(ctx, row.SnapshotKey)
	}
	return mailOutboxInfo(row), nil
}

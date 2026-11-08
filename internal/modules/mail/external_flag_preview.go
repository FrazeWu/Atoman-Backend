package mail

import (
	"context"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
)

// Source and native flags are compared against the original imported state.
// The preview makes NO IMAP STORE, JMAP Email/set, folder move or deletion.
const externalFlagPreviewLimit = 8
const externalFlagPreviewMaxPosition = 10000

type ExternalFlagSnapshot struct {
	Seen    bool `json:"seen"`
	Flagged bool `json:"flagged"`
}
type ExternalFlagPlan struct {
	Source   bool   `json:"source"`
	Native   bool   `json:"native"`
	Baseline bool   `json:"baseline"`
	Decision string `json:"decision"`
}
type ExternalFlagPreviewItem struct {
	SourceUID     uint32           `json:"source_uid"`
	NativeEmailID string           `json:"native_email_id"`
	Status        string           `json:"status"`
	Seen          ExternalFlagPlan `json:"seen"`
	Flagged       ExternalFlagPlan `json:"flagged"`
}
type ExternalFlagPreviewPage struct {
	JobID        uuid.UUID                 `json:"job_id"`
	Folder       string                    `json:"folder"`
	Items        []ExternalFlagPreviewItem `json:"items"`
	NextPosition int                       `json:"next_position"`
	HasMore      bool                      `json:"has_more"`
	CheckedAt    time.Time                 `json:"checked_at"`
}

type MailFlagTargetReader interface {
	ReadFlags(context.Context, authctx.CurrentUser, []string) (map[string]ExternalFlagSnapshot, error)
}

// A boolean flag can only have one non-matching side at a time. Distinguish
// the side changed from the last agreed baseline; never decide to overwrite
// either side without an explicit future write contract.
func chooseExternalFlagPlan(baseline, source, native bool) ExternalFlagPlan {
	state := ExternalFlagPlan{Baseline: baseline, Source: source, Native: native, Decision: "in_sync"}
	if source == native {
		return state
	}
	switch {
	case source != baseline && native == baseline:
		state.Decision = "copy_to_native"
	case native != baseline && source == baseline:
		state.Decision = "copy_to_source"
	default:
		state.Decision = "conflict"
	}
	return state
}

func (s *MailExternalHistoryService) FlagPreview(ctx context.Context, user authctx.CurrentUser, accountID, jobID uuid.UUID, position int) (ExternalFlagPreviewPage, error) {
	out := ExternalFlagPreviewPage{JobID: jobID, Items: []ExternalFlagPreviewItem{}, NextPosition: position, CheckedAt: time.Now().UTC()}
	if err := s.valid(); err != nil {
		return out, err
	}
	if position < 0 || position > externalFlagPreviewMaxPosition {
		return out, apperr.BadRequest("mail.external_flag_position", "Invalid preview position")
	}
	if _, err := s.external.ownRow(ctx, user, accountID); err != nil {
		return out, err
	}
	if err := s.external.access(ctx, user); err != nil {
		return out, err
	}
	var job model.MailExternalImportJob
	if err := s.external.workspace.db.WithContext(ctx).Where("id=? AND user_id=? AND external_account_id=?", jobID, user.ID, accountID).Take(&job).Error; err != nil {
		return out, apperr.NotFound("mail.external_history_missing", "Import job unavailable")
	}
	info, err := s.info(job)
	if err != nil {
		return out, err
	}
	out.Folder = info.Folder
	if s.flagReader == nil {
		return out, apperr.New(503, "mail.external_flag_unavailable", "Flag comparison is unavailable", nil)
	}
	var receipts []model.MailExternalImportItem
	err = s.external.workspace.db.WithContext(ctx).Where("job_id=? AND user_id=? AND status=?", jobID, user.ID, "imported").Order("source_uid ASC").Offset(position).Limit(externalFlagPreviewLimit + 1).Find(&receipts).Error
	if err != nil {
		return out, apperr.Internal(err)
	}
	out.HasMore = len(receipts) > externalFlagPreviewLimit
	if out.HasMore {
		receipts = receipts[:externalFlagPreviewLimit]
	}
	out.NextPosition = position + len(receipts)
	if len(receipts) == 0 {
		return out, nil
	}
	account, err := s.external.ownRow(ctx, user, accountID)
	if err != nil {
		return out, err
	}
	creds, err := s.external.secrets(account)
	if err != nil {
		return out, err
	}
	probe, err := s.source.Inspect(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder)
	if err != nil {
		return out, apperr.New(502, "mail.external_flag_source_failed", "Source folder cannot be inspected", nil)
	}
	if probe.UIDValidity != job.UIDValidity || probe.UpperUID < job.UpperUID {
		return out, apperr.Conflict("mail.external_flag_identity_changed", "Source folder UID identity has changed")
	}
	ids := make([]string, 0, len(receipts))
	for _, row := range receipts {
		if row.UserID != user.ID || row.ExternalAccountID != accountID || row.UIDValidity != job.UIDValidity || row.FolderKey != job.FolderKey || row.SourceUID == 0 || !validJMAPScopedID(row.ImportedEmailID) {
			return out, apperr.Conflict("mail.external_flag_invalid_receipt", "Stored import provenance needs reconciliation")
		}
		ids = append(ids, row.ImportedEmailID)
	}
	natives, err := s.flagReader.ReadFlags(ctx, user, ids)
	if err != nil {
		return out, apperr.New(502, "mail.external_flag_target_failed", "Native mail flags unavailable", nil)
	}
	// Read the exact source UID, never guess from Message-ID or subject. Each
	// lookup is read-only and UIDVALIDITY checked by the underlying IMAP adapter.
	for _, row := range receipts {
		item := ExternalFlagPreviewItem{SourceUID: row.SourceUID, NativeEmailID: row.ImportedEmailID, Status: "ready"}
		if row.FlagMutationID != nil {
			item.Status = "sync_needs_review"
			out.Items = append(out.Items, item)
			continue
		}
		if !row.FlagsBaselineKnown {
			item.Status = "baseline_unknown"
			out.Items = append(out.Items, item)
			continue
		}
		original, ok := natives[row.ImportedEmailID]
		if !ok {
			item.Status = "native_missing"
			out.Items = append(out.Items, item)
			continue
		}
		records, e := s.source.ListWindow(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder, job.UIDValidity, row.SourceUID, row.SourceUID)
		if e != nil {
			return out, apperr.New(502, "mail.external_flag_source_failed", "Source flags unavailable", nil)
		}
		if len(records) == 0 {
			item.Status = "source_missing"
			out.Items = append(out.Items, item)
			continue
		}
		if len(records) != 1 || records[0].UID != row.SourceUID {
			return out, apperr.Conflict("mail.external_flag_identity_changed", "Source message identity changed")
		}
		source := records[0]
		item.Seen = chooseExternalFlagPlan(row.BaselineSeen, source.Seen, original.Seen)
		item.Flagged = chooseExternalFlagPlan(row.BaselineFlagged, source.Flagged, original.Flagged)
		out.Items = append(out.Items, item)
	}
	return out, nil
}

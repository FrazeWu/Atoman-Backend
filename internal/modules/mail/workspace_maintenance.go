package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"context"
	"errors"
	"gorm.io/gorm"
	"os"
	"time"
)

type MailMaintenanceResult struct {
	ExpiredSnapshots int   `json:"expired_snapshots"`
	ExpiredUploads   int   `json:"expired_uploads"`
	ExpiredReceipts  int64 `json:"expired_receipts"`
	ReconciledOutbox int   `json:"reconciled_outbox"`
}

func NewConfiguredMailWorkspace(db *gorm.DB) (*MailWorkspaceService, *MailOutboxService, error) {
	sender, e := NewConfiguredMailOutbox(db)
	if e != nil || sender == nil {
		return nil, nil, errors.New("Mail storage and sending operational gates unavailable")
	}
	client, e := NewStalwartJMAPClient(os.Getenv("ATOMAN_MAIL_STALWART_URL"), nil)
	if e != nil {
		return nil, nil, e
	}
	vault, e := NewMailCredentialVault(os.Getenv("ATOMAN_MAIL_JMAP_ENCRYPTION_KEY_B64"))
	if e != nil {
		return nil, nil, e
	}
	return NewMailWorkspaceService(NewMailReadService(db, client, vault, true), sender), sender, nil
}

// RunMaintenance only removes expired application artifacts and reconciles
// submission receipts. Message expiry belongs exclusively to Stalwart native
// retention so moves by other IMAP/JMAP clients cannot cause premature deletion.
func (s *MailWorkspaceService) RunMaintenance(ctx context.Context, outbox *MailOutboxService) (MailMaintenanceResult, error) {
	var result MailMaintenanceResult
	if s == nil || s.db == nil {
		return result, errors.New("Mail maintenance unavailable")
	}
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	var uploads []model.MailUpload
	if e := s.db.WithContext(ctx).Where("retired_at IS NOT NULL OR created_at < ?", cutoff).Order("COALESCE(retired_at, created_at) ASC").Limit(200).Find(&uploads).Error; e != nil {
		return result, e
	}
	for _, upload := range uploads {
		retired := false
		e := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if e := lockMailDraftArtifacts(tx, upload.UserID); e != nil {
				return e
			}
			// Re-read under the same lock as SaveDraft, including the references.
			var current model.MailUpload
			if e := tx.Where("id = ? AND user_id = ?", upload.ID, upload.UserID).Take(&current).Error; e != nil {
				return e
			}
			if current.RetiredAt != nil {
				retired = true
				return nil
			}
			if !current.CreatedAt.Before(cutoff) {
				return nil
			}
			var drafts []model.MailDraft
			if e := tx.Where("user_id = ?", upload.UserID).Find(&drafts).Error; e != nil {
				return e
			}
			for _, d := range drafts {
				v, e := s.openDraft(d, true)
				if e != nil {
					return e // Fail closed if a draft cannot be authenticated.
				}
				if v.Compose != nil {
					for _, a := range v.Compose.Attachments {
						if a.ID == upload.ID {
							return nil
						}
					}
				}
			}
			r := tx.Model(&model.MailUpload{}).Where("id = ? AND user_id = ? AND retired_at IS NULL", upload.ID, upload.UserID).UpdateColumn("retired_at", time.Now().UTC())
			retired = r.RowsAffected == 1
			return r.Error
		})
		if e != nil || !retired {
			continue
		}
		// If this fails, keep the retired row for an idempotent later retry.
		if e := s.store.Delete(ctx, upload.ObjectKey); e != nil {
			continue
		}
		if r := s.db.WithContext(ctx).Where("id = ? AND user_id = ? AND retired_at IS NOT NULL", upload.ID, upload.UserID).Delete(&model.MailUpload{}); r.Error == nil {
			result.ExpiredUploads += int(r.RowsAffected)
		}
	}
	// Fast sent/cancel cleanup is best effort. Bound retries to terminal states
	// older than 30 days; preserve uncertain evidence and every active snapshot.
	terminal := []string{"sent", "cancelled", "blocked"}
	var snapshots []model.MailPendingSend
	if e := s.db.WithContext(ctx).Where("status IN ? AND updated_at < ? AND snapshot_deleted_at IS NULL", terminal, cutoff).Order("updated_at ASC").Limit(200).Find(&snapshots).Error; e != nil {
		return result, e
	}
	for _, snapshot := range snapshots {
		if e := s.store.Delete(ctx, snapshot.SnapshotKey); e != nil {
			continue
		}
		r := s.db.WithContext(ctx).Model(&model.MailPendingSend{}).Where("id = ? AND status IN ? AND snapshot_deleted_at IS NULL", snapshot.ID, terminal).UpdateColumn("snapshot_deleted_at", time.Now().UTC())
		if r.Error == nil {
			result.ExpiredSnapshots += int(r.RowsAffected)
		}
	}
	r := s.db.Where("expires_at < ?", time.Now().UTC()).Delete(&model.MailActionReceipt{})
	if r.Error != nil {
		return result, r.Error
	}
	result.ExpiredReceipts = r.RowsAffected
	if outbox != nil {
		var pending []model.MailPendingSend
		if e := s.db.Where("status IN ? AND claimed_at < ?", []string{"dispatching", "uncertain"}, time.Now().Add(-2*time.Minute)).Order("updated_at ASC").Limit(10).Find(&pending).Error; e != nil {
			return result, e
		}
		for _, p := range pending {
			v, e := outbox.Reconcile(ctx, authctx.CurrentUser{ID: p.UserID}, p.ID)
			if e == nil && v.Status == "sent" {
				result.ReconciledOutbox++
			}
		}
	}
	return result, nil
}

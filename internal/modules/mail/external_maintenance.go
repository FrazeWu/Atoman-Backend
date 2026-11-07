package mail

import (
	"atoman/internal/model"
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"time"
)

type ExternalSyncReport struct {
	Attempted int `json:"attempted"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

// One-shot, bounded, non-root automated sync. Keeps existing encrypted
// snapshots if a provider fails. No session, log, or response exposes secrets.
func RunExternalSyncDue(ctx context.Context, db *gorm.DB, vault *MailOutboxVault, remote ExternalIMAPRemote) (ExternalSyncReport, error) {
	report := ExternalSyncReport{}
	if db == nil || vault == nil {
		return report, errors.New("External mail database and vault required")
	}
	if remote == nil {
		remote = liveExternalIMAPRemote{}
	}
	now := time.Now().UTC()
	var rows []model.MailExternalAccount
	if err := db.WithContext(ctx).Where("last_attempt_at IS NULL OR last_attempt_at < ?", now.Add(-15*time.Minute)).
		Order("COALESCE(last_attempt_at,created_at) ASC").Limit(10).Find(&rows).Error; err != nil {
		return report, err
	}
	svc := &MailExternalService{workspace: &MailWorkspaceService{db: db, vault: vault}, remote: remote}
	for _, row := range rows {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		report.Attempted++
		creds, err := svc.secrets(row)
		if err == nil {
			_, allowed := externalProviderHost(row.Provider)
			if allowed {
				var snap ExternalInboxSnapshot
				snap, err = remote.Sync(ctx, row.Provider, creds.Address, creds.AppPassword)
				if err == nil && len(snap.Messages) <= 60 {
					var serialized []byte
					serialized, err = json.Marshal(snap)
					if err == nil {
						var ciphertext []byte
						ciphertext, err = vault.Seal(row.UserID, row.ID, serialized)
						if err == nil {
							timestamp := time.Now().UTC()
							updated := db.WithContext(ctx).Model(&model.MailExternalAccount{}).
								Where("id=? AND user_id=? AND revision=?", row.ID, row.UserID, row.Revision).
								Updates(map[string]any{"snapshot_ciphertext": ciphertext,
									"revision": row.Revision + 1, "last_synced_at": timestamp,
									"last_attempt_at": timestamp, "last_error_code": "", "updated_at": timestamp})
							if updated.Error != nil {
								return report, updated.Error
							}
							if updated.RowsAffected == 1 {
								report.Succeeded++
							}
							continue // Concurrent manual sync wins without data loss.
						}
					}
				} else if err == nil {
					err = errors.New("Unexpected provider response length")
				}
			} else {
				err = errors.New("Provider not allowed")
			}
		}
		report.Failed++
		updated := db.WithContext(ctx).Model(&model.MailExternalAccount{}).
			Where("id=? AND user_id=? AND revision=?", row.ID, row.UserID, row.Revision).
			Updates(map[string]any{"last_attempt_at": time.Now().UTC(), "last_error_code": "connection_failed"})
		if updated.Error != nil {
			return report, updated.Error
		}
	}
	return report, nil
}

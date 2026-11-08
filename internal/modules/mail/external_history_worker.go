package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ExternalHistoryReport struct {
	JobsClaimed int `json:"jobs_claimed"`
	Imported    int `json:"imported"`
	Skipped     int `json:"skipped"`
	NeedReview  int `json:"need_review"`
	Failed      int `json:"failed"`
	Completed   int `json:"completed"`
}

const historyRunLimit = 40
const historyStaleLease = 10 * time.Minute

// RunExternalHistoryDue processes opt-in migration jobs only. No background
// provider or Stalwart account is touched until an owner creates a job.
func RunExternalHistoryDue(ctx context.Context, service *MailExternalHistoryService) (ExternalHistoryReport, error) {
	report := ExternalHistoryReport{}
	if e := service.valid(); e != nil {
		return report, e
	}
	db := service.external.workspace.db.WithContext(ctx)
	cutoff := time.Now().UTC().Add(-historyStaleLease)
	var jobs []model.MailExternalImportJob
	if e := db.Where("status=? OR (status=? AND claimed_at < ?)", "queued", "running", cutoff).Order("updated_at ASC").Limit(2).Find(&jobs).Error; e != nil {
		return report, e
	}
	for _, job := range jobs {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		now := time.Now().UTC()
		claimed := db.Model(&model.MailExternalImportJob{}).Where("id=? AND user_id=? AND (status=? OR (status=? AND claimed_at < ?))", job.ID, job.UserID, "queued", "running", cutoff).
			Updates(map[string]any{"status": "running", "claimed_at": now, "updated_at": now})
		if claimed.Error != nil {
			return report, claimed.Error
		}
		if claimed.RowsAffected == 0 {
			continue
		}
		report.JobsClaimed++
		// A process crash during Email/import might have delivered a message even
		// if no result was persisted. Never automatically reissue that UID.
		var inFlight int64
		if e := db.Model(&model.MailExternalImportItem{}).Where("job_id=? AND status IN ?", job.ID, []string{"importing", "uncertain"}).Count(&inFlight).Error; e != nil {
			return report, e
		}
		if inFlight > 0 {
			if e := service.historyMark(job.ID, "needs_review", "ambiguous_import"); e != nil {
				return report, e
			}
			report.NeedReview++
			continue
		}
		outcome, e := service.processHistoryJob(ctx, job.ID, &report)
		if e != nil {
			return report, e
		}
		switch outcome {
		case "needs_review":
			report.NeedReview++
		case "failed":
			report.Failed++
		case "completed":
			report.Completed++
		}
	}
	return report, nil
}
func (s *MailExternalHistoryService) historyMark(id uuid.UUID, status, code string) error {
	db := s.external.workspace.db
	updates := map[string]any{"status": status, "last_error_code": code, "claimed_at": nil, "updated_at": time.Now().UTC()}
	res := db.Model(&model.MailExternalImportJob{}).Where("id=? AND status=?", id, "running").Updates(updates)
	return res.Error
}
func (s *MailExternalHistoryService) historyAdvance(jobID uuid.UUID, next uint32, imported, skipped bool) error {
	return s.external.workspace.db.Transaction(func(tx *gorm.DB) error {
		var job model.MailExternalImportJob
		if e := tx.Where("id=?", jobID).Take(&job).Error; e != nil {
			return e
		}
		if job.Status != "running" {
			return nil
		} // Paused/cancelled concurrently.
		if next <= job.NextUID || next > job.UpperUID+1 {
			return errors.New("Invalid migration cursor")
		}
		changes := map[string]any{"next_uid": next, "updated_at": time.Now().UTC()}
		if imported {
			changes["imported"] = job.Imported + 1
		}
		if skipped {
			changes["skipped"] = job.Skipped + 1
		}
		return tx.Model(&model.MailExternalImportJob{}).Where("id=? AND status=? AND next_uid=?", jobID, "running", job.NextUID).Updates(changes).Error
	})
}
func (s *MailExternalHistoryService) historyJob(id uuid.UUID) (model.MailExternalImportJob, error) {
	var job model.MailExternalImportJob
	e := s.external.workspace.db.Where("id=?", id).Take(&job).Error
	return job, e
}
func (s *MailExternalHistoryService) historyItem(job model.MailExternalImportJob, uid uint32) (model.MailExternalImportItem, bool, error) {
	var item model.MailExternalImportItem
	query := s.external.workspace.db.Where("user_id=? AND external_account_id=? AND folder_key=? AND uid_validity=? AND source_uid=?", job.UserID, job.ExternalAccountID, job.FolderKey, job.UIDValidity, uid).Limit(1).Find(&item)
	if query.Error != nil {
		return item, false, query.Error
	}
	return item, query.RowsAffected == 1, nil
}
func (s *MailExternalHistoryService) historyStop(id uuid.UUID, code string, review bool) (string, error) {
	status := "failed"
	if review {
		status = "needs_review"
	}
	return status, s.historyMark(id, status, code)
}
func (s *MailExternalHistoryService) processHistoryJob(ctx context.Context, id uuid.UUID, report *ExternalHistoryReport) (string, error) {
	job, e := s.historyJob(id)
	if e != nil {
		return "", e
	}
	if job.Status != "running" {
		return "paused", nil
	}
	owner := authctx.CurrentUser{ID: job.UserID}
	account, e := s.external.ownRow(ctx, owner, job.ExternalAccountID)
	if e != nil {
		return s.historyStop(id, "account_disconnected", false)
	}
	creds, e := s.external.secrets(account)
	if e != nil {
		return s.historyStop(id, "credential_unavailable", false)
	}
	info, e := s.info(job)
	if e != nil {
		return s.historyStop(id, "folder_unavailable", false)
	}
	probe, e := s.source.Inspect(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder)
	if e != nil {
		return s.historyStop(id, "provider_unavailable", false)
	}
	if probe.UIDValidity != job.UIDValidity || probe.UpperUID < job.UpperUID {
		return s.historyStop(id, "source_identity_changed", true)
	}
	if job.NextUID > job.UpperUID {
		return "completed", s.historyMark(id, "completed", "")
	}
	targetName := externalHistoryFolderName(account.Provider, job.FolderKey, info.Folder)
	targetFolder, e := s.target.EnsureFolder(ctx, owner, targetName)
	if e != nil {
		return s.historyStop(id, "target_folder_unavailable", false)
	}
	scanned := 0
	handled := 0
	for scanned < 15 && handled < historyRunLimit && ctx.Err() == nil {
		job, e = s.historyJob(id)
		if e != nil {
			return "", e
		}
		if job.Status != "running" {
			return "paused", nil
		}
		if job.NextUID > job.UpperUID {
			return "completed", s.historyMark(id, "completed", "")
		}
		end := job.NextUID + externalImportUIDWindow - 1
		if end < job.NextUID || end > job.UpperUID {
			end = job.UpperUID
		}
		entries, err := s.source.ListWindow(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder, job.UIDValidity, job.NextUID, end)
		if err != nil {
			return s.historyStop(id, "source_window_failed", false)
		}
		scanned++
		for _, msg := range entries {
			job, e = s.historyJob(id)
			if e != nil {
				return "", e
			}
			if job.Status != "running" {
				return "paused", nil
			}
			if msg.UID < job.NextUID {
				continue
			}
			if msg.UID > end || msg.UID > job.UpperUID {
				return s.historyStop(id, "source_window_invalid", true)
			}
			if msg.UID > job.NextUID {
				if e = s.historyAdvance(job.ID, msg.UID, false, false); e != nil {
					return "", e
				}
			}
			job, e = s.historyJob(id)
			if e != nil {
				return "", e
			}
			item, exists, err := s.historyItem(job, msg.UID)
			if err != nil {
				return "", err
			}
			if exists {
				switch item.Status {
				case "imported", "skipped":
					// A user may pause while one target import is finishing.
					// In that case the item receipt is committed but the job
					// cursor/count are deliberately left unchanged until resume.
					if e = s.historyAdvance(id, msg.UID+1, item.Status == "imported", item.Status == "skipped"); e != nil {
						return "", e
					}
					handled++
					if handled >= historyRunLimit {
						return "queued", s.historyMark(id, "queued", "")
					}
					continue
				case "importing", "uncertain":
					return s.historyStop(id, "ambiguous_import", true)
				case "prepared": // Safe to retry only if no Email/import began.
				default:
					return s.historyStop(id, "invalid_item_state", true)
				}
			}
			if msg.Size <= 0 || msg.Size > maxExternalHistoryMIMEBytes {
				if !exists {
					record := model.MailExternalImportItem{ID: uuid.New(), JobID: id, UserID: job.UserID, ExternalAccountID: job.ExternalAccountID, FolderKey: job.FolderKey, UIDValidity: job.UIDValidity, SourceUID: msg.UID, Status: "skipped", Size: uint64(max(0, msg.Size)), ErrorCode: "unsupported_size"}
					if e = s.external.workspace.db.Create(&record).Error; e != nil {
						return "", e
					}
				}
				if e = s.historyAdvance(id, msg.UID+1, false, true); e != nil {
					return "", e
				}
				report.Skipped++
				handled++
				if handled >= historyRunLimit {
					return "queued", s.historyMark(id, "queued", "")
				}
				continue
			}
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < 120*time.Second {
				return "queued", s.historyMark(id, "queued", "")
			}
			raw, err := s.source.ReadRaw(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder, job.UIDValidity, msg.UID)
			if err != nil {
				return s.historyStop(id, "source_mime_failed", false)
			}
			sum := sha256.Sum256(raw)
			digest := hex.EncodeToString(sum[:])
			if exists && item.MIMEHash != "" && item.MIMEHash != digest {
				return s.historyStop(id, "source_content_changed", true)
			}
			if !exists {
				item = model.MailExternalImportItem{ID: uuid.New(), JobID: id, UserID: job.UserID, ExternalAccountID: job.ExternalAccountID, FolderKey: job.FolderKey, UIDValidity: job.UIDValidity, SourceUID: msg.UID, MIMEHash: digest, Size: uint64(len(raw)), Status: "prepared"}
				if e = s.external.workspace.db.Create(&item).Error; e != nil {
					return "", e
				}
			}
			// Mark BEFORE the upstream call; after a process crash we must not assume
			// that retrying this UID is safe. A crashed job stops in needs_review.
			claimed := s.external.workspace.db.Model(&model.MailExternalImportItem{}).Where("id=? AND status=?", item.ID, "prepared").Updates(map[string]any{"status": "importing", "updated_at": time.Now().UTC()})
			if claimed.Error != nil {
				return "", claimed.Error
			}
			if claimed.RowsAffected != 1 {
				return s.historyStop(id, "item_race", true)
			}
			importedID, uncertain, err := s.target.ImportRaw(ctx, owner, targetFolder, raw, msg.Date, msg.Seen, msg.Flagged)
			if err != nil {
				nextStatus := "prepared"
				jobStatus := "failed"
				code := "target_not_ready"
				if uncertain {
					nextStatus = "uncertain"
					jobStatus = "needs_review"
					code = "ambiguous_import"
				}
				updated := s.external.workspace.db.Model(&model.MailExternalImportItem{}).Where("id=? AND status=?", item.ID, "importing").Updates(map[string]any{"status": nextStatus, "error_code": code, "updated_at": time.Now().UTC()})
				if updated.Error != nil {
					return "", updated.Error
				}
				return jobStatus, s.historyMark(id, jobStatus, code)
			}
			if !validJMAPScopedID(importedID) {
				return s.historyStop(id, "ambiguous_import", true)
			}
			// Persist the receipt BEFORE advancing the source cursor. A DB failure
			// preserves importing state; the next worker will fail closed.
			e = s.external.workspace.db.Transaction(func(tx *gorm.DB) error {
				upd := tx.Model(&model.MailExternalImportItem{}).Where("id=? AND status=?", item.ID, "importing").Updates(map[string]any{"status": "imported", "imported_email_id": importedID, "error_code": "", "updated_at": time.Now().UTC()})
				if upd.Error != nil {
					return upd.Error
				}
				if upd.RowsAffected != 1 {
					return errors.New("Import receipt conflict")
				}
				var latest model.MailExternalImportJob
				if x := tx.Where("id=?", job.ID).Take(&latest).Error; x != nil {
					return x
				}
				if latest.Status == "running" && latest.NextUID == msg.UID {
					return tx.Model(&model.MailExternalImportJob{}).Where("id=? AND status=? AND next_uid=?", job.ID, "running", msg.UID).Updates(map[string]any{"next_uid": msg.UID + 1, "imported": latest.Imported + 1, "updated_at": time.Now().UTC()}).Error
				}
				return nil // User paused while upload was in progress; resume sees receipt.
			})
			if e != nil {
				return "", e
			}
			report.Imported++
			handled++
			if handled >= historyRunLimit {
				return "queued", s.historyMark(id, "queued", "")
			}
		}
		job, e = s.historyJob(id)
		if e != nil {
			return "", e
		}
		if job.Status != "running" {
			return "paused", nil
		}
		if job.NextUID <= end {
			if e = s.historyAdvance(id, end+1, false, false); e != nil {
				return "", e
			}
		}
	}
	if ctx.Err() != nil {
		return s.historyStop(id, "worker_interrupted", false)
	}
	return "queued", s.historyMark(id, "queued", "")
}

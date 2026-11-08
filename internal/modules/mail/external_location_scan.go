package mail

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const locationScanPageSize = 8
const locationObservationGap = 5 * time.Minute
const locationCaseListLimit = 50

type MailExternalLocationService struct {
	history *MailExternalHistoryService
	native  MailNativeLocation
}

func NewMailExternalLocationService(history *MailExternalHistoryService, native MailNativeLocation) *MailExternalLocationService {
	if history == nil || history.external == nil || history.external.workspace == nil || native == nil {
		return nil
	}
	return &MailExternalLocationService{history: history, native: native}
}
func (s *MailExternalLocationService) ready() error {
	if s == nil || s.history == nil || s.native == nil {
		return apperr.New(503, "mail.location_disabled", "Location review disabled", nil)
	}
	if err := s.history.valid(); err != nil {
		return err
	}
	db := s.history.external.workspace.db
	if !db.Migrator().HasTable(&model.MailExternalLocationCase{}) || !db.Migrator().HasTable(&model.MailExternalLocationAction{}) || !db.Migrator().HasColumn(&model.MailExternalImportItem{}, "location_mutation_id") {
		return apperr.New(503, "mail.location_migration_required", "Location review migration required", nil)
	}
	return nil
}

type MailLocationCaseInfo struct {
	ID                  uuid.UUID  `json:"id"`
	JobID               uuid.UUID  `json:"job_id"`
	SourceUID           uint32     `json:"source_uid"`
	Status              string     `json:"status"`
	MissingObservations uint32     `json:"missing_observations"`
	FirstMissingAt      time.Time  `json:"first_missing_at"`
	LastObservedAt      time.Time  `json:"last_observed_at"`
	ReturnedAt          *time.Time `json:"returned_at,omitempty"`
	LastCheckedAt       time.Time  `json:"last_checked_at"`
	CandidateFolder     string     `json:"candidate_folder,omitempty"`
	ActionID            *uuid.UUID `json:"action_id,omitempty"`
	ResolvedAt          *time.Time `json:"resolved_at,omitempty"`
}
type MailLocationScanPage struct {
	JobID         uuid.UUID              `json:"job_id"`
	Items         []MailLocationCaseInfo `json:"items"`
	NextSourceUID uint32                 `json:"next_source_uid"`
	HasMore       bool                   `json:"has_more"`
	CheckedAt     time.Time              `json:"checked_at"`
}

func (s *MailExternalLocationService) caseInfo(ctx context.Context, job model.MailExternalImportJob, c model.MailExternalLocationCase) (MailLocationCaseInfo, error) {
	result := MailLocationCaseInfo{ID: c.ID, JobID: c.JobID, SourceUID: c.SourceUID, Status: c.Status, MissingObservations: c.MissingObservations, FirstMissingAt: c.FirstMissingAt, LastObservedAt: c.LastObservedAt, LastCheckedAt: c.LastCheckedAt, ReturnedAt: c.ReturnedAt, ActionID: c.ActionID, ResolvedAt: c.ResolvedAt}
	if c.CandidateItemID == nil {
		return result, nil
	}
	var candidate model.MailExternalImportItem
	err := s.history.external.workspace.db.WithContext(ctx).Where("id=? AND user_id=? AND external_account_id=? AND status=?", *c.CandidateItemID, c.UserID, c.ExternalAccountID, "imported").Take(&candidate).Error
	if err != nil {
		return result, nil
	}
	var dest model.MailExternalImportJob
	err = s.history.external.workspace.db.WithContext(ctx).Where("id=? AND user_id=? AND external_account_id=?", candidate.JobID, c.UserID, c.ExternalAccountID).Take(&dest).Error
	if err != nil {
		return result, nil
	}
	info, err := s.history.info(dest)
	if err != nil {
		return result, nil
	}
	result.CandidateFolder = info.Folder
	return result, nil
}
func (s *MailExternalLocationService) Cases(ctx context.Context, u authctx.CurrentUser, accountID, jobID uuid.UUID) ([]MailLocationCaseInfo, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	job, err := s.history.flagJob(ctx, u, accountID, jobID)
	if err != nil {
		return nil, err
	}
	var rows []model.MailExternalLocationCase
	err = s.history.external.workspace.db.WithContext(ctx).Where("user_id=? AND external_account_id=? AND job_id=?", u.ID, accountID, jobID).Order("updated_at DESC").Limit(locationCaseListLimit).Find(&rows).Error
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]MailLocationCaseInfo, 0, len(rows))
	for _, row := range rows {
		info, e := s.caseInfo(ctx, job, row)
		if e != nil {
			return nil, e
		}
		items = append(items, info)
	}
	return items, nil
}

// The source is inspected only by exact folder+UIDVALIDITY+UID. Its absence is
// an observation, not proof that a user deleted a message. No remote write.
func (s *MailExternalLocationService) Scan(ctx context.Context, u authctx.CurrentUser, accountID, jobID uuid.UUID, afterUID uint32) (MailLocationScanPage, error) {
	page := MailLocationScanPage{JobID: jobID, Items: []MailLocationCaseInfo{}, NextSourceUID: afterUID, CheckedAt: time.Now().UTC()}
	if err := s.ready(); err != nil {
		return page, err
	}
	job, err := s.history.flagJob(ctx, u, accountID, jobID)
	if err != nil {
		return page, err
	}
	if job.Status != "completed" && job.Status != "watching" {
		return page, apperr.Conflict("mail.location_import_incomplete", "Finish historical import before comparing locations")
	}
	account, err := s.history.external.ownRow(ctx, u, accountID)
	if err != nil {
		return page, err
	}
	creds, err := s.history.external.secrets(account)
	if err != nil {
		return page, err
	}
	info, err := s.history.info(job)
	if err != nil {
		return page, err
	}
	probe, err := s.history.source.Inspect(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder)
	if err != nil || probe.UIDValidity != job.UIDValidity || probe.UpperUID < job.UpperUID {
		return page, apperr.Conflict("mail.location_uid_changed", "Source UID identity changed; review before scanning")
	}
	db := s.history.external.workspace.db.WithContext(ctx)
	var imported []model.MailExternalImportItem
	err = db.Where("user_id=? AND external_account_id=? AND job_id=? AND status=? AND source_uid>?", u.ID, accountID, jobID, "imported", afterUID).
		Order("source_uid ASC").Limit(locationScanPageSize + 1).Find(&imported).Error
	if err != nil {
		return page, apperr.Internal(err)
	}
	page.HasMore = len(imported) > locationScanPageSize
	if page.HasMore {
		imported = imported[:locationScanPageSize]
	}
	for _, item := range imported {
		if err = ctx.Err(); err != nil {
			return page, err
		}
		page.NextSourceUID = item.SourceUID
		if item.UIDValidity != job.UIDValidity || item.FolderKey != job.FolderKey || item.UserID != u.ID || item.ExternalAccountID != accountID || !validJMAPScopedID(item.ImportedEmailID) {
			return page, apperr.Conflict("mail.location_provenance_invalid", "Imported message identity changed")
		}
		messages, e := s.history.source.ListWindow(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder, job.UIDValidity, item.SourceUID, item.SourceUID)
		if e != nil {
			return page, apperr.New(502, "mail.location_source_unavailable", "Cannot verify source UID", nil)
		}
		if len(messages) > 1 || (len(messages) == 1 && messages[0].UID != item.SourceUID) {
			return page, apperr.Conflict("mail.location_uid_changed", "Source message identity changed")
		}
		if len(messages) == 1 {
			// If a once-missing UID reappears, retain the audit trail. This does not
			// automatically undo an owner-requested trash action.
			if stampErr := db.Model(&model.MailExternalLocationCase{}).Where("user_id=? AND external_account_id=? AND job_id=? AND import_item_id=?", u.ID, accountID, jobID, item.ID).Updates(map[string]any{"returned_at": page.CheckedAt, "last_checked_at": page.CheckedAt, "updated_at": page.CheckedAt}).Error; stampErr != nil {
				return page, apperr.Internal(stampErr)
			}
			if updateErr := db.Model(&model.MailExternalLocationCase{}).Where("user_id=? AND job_id=? AND import_item_id=? AND status IN ?", u.ID, jobID, item.ID, []string{"observed", "missing_unclassified", "possible_move", "ambiguous_candidates", "candidate_unverified", "native_missing", "native_unavailable"}).Updates(map[string]any{"status": "source_returned", "candidate_item_id": nil, "last_checked_at": page.CheckedAt, "updated_at": page.CheckedAt}).Error; updateErr != nil {
				return page, apperr.Internal(updateErr)
			}
			continue
		}
		row, e := s.recordMissing(ctx, job, item, page.CheckedAt)
		if e != nil {
			return page, e
		}
		if row.ActionID == nil && row.ResolvedAt == nil && row.MissingObservations >= 2 {
			next, candidateErr := s.classifyMissing(ctx, job, item, row, account.Provider, creds, info.Folder)
			if candidateErr != nil {
				return page, candidateErr
			}
			row = next
		}
		out, e := s.caseInfo(ctx, job, row)
		if e != nil {
			return page, e
		}
		page.Items = append(page.Items, out)
	}
	if !page.HasMore {
		page.NextSourceUID = 0
	}
	return page, nil
}
func (s *MailExternalLocationService) recordMissing(ctx context.Context, job model.MailExternalImportJob, item model.MailExternalImportItem, now time.Time) (model.MailExternalLocationCase, error) {
	db := s.history.external.workspace.db.WithContext(ctx)
	row := model.MailExternalLocationCase{}
	err := db.Transaction(func(tx *gorm.DB) error {
		found := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id=? AND external_account_id=? AND job_id=? AND import_item_id=?", job.UserID, job.ExternalAccountID, job.ID, item.ID).First(&row)
		if errors.Is(found.Error, gorm.ErrRecordNotFound) {
			row = model.MailExternalLocationCase{ID: uuid.New(), UserID: job.UserID, ExternalAccountID: job.ExternalAccountID, JobID: job.ID, ImportItemID: item.ID, SourceUID: item.SourceUID, UIDValidity: item.UIDValidity, NativeEmailID: item.ImportedEmailID, Status: "observed", MissingObservations: 1, FirstMissingAt: now, LastObservedAt: now, LastCheckedAt: now, CreatedAt: now, UpdatedAt: now}
			return tx.Create(&row).Error
		}
		if found.Error != nil {
			return found.Error
		}
		if row.SourceUID != item.SourceUID || row.UIDValidity != item.UIDValidity || row.NativeEmailID != item.ImportedEmailID {
			return errors.New("Stored location provenance changed")
		}
		if row.Status == "source_returned" {
			row.Status = "observed"
			row.MissingObservations = 1
			row.FirstMissingAt = now
			row.CandidateItemID = nil
			row.ResolvedAt = nil
		} else if row.ResolvedAt == nil && row.ActionID == nil && now.Sub(row.LastObservedAt) >= locationObservationGap {
			row.MissingObservations++
		}
		if now.Sub(row.LastObservedAt) >= locationObservationGap || row.LastObservedAt.IsZero() {
			row.LastObservedAt = now
		}
		row.ReturnedAt = nil
		row.LastCheckedAt = now
		row.UpdatedAt = now
		return tx.Save(&row).Error
	})
	if err != nil {
		return row, apperr.Conflict("mail.location_receipt_changed", "Location receipt changed; retry scan")
	}
	return row, nil
}
func (s *MailExternalLocationService) classifyMissing(ctx context.Context, job model.MailExternalImportJob, item model.MailExternalImportItem, row model.MailExternalLocationCase, provider string, creds externalSecrets, oldFolder string) (model.MailExternalLocationCase, error) {
	db := s.history.external.workspace.db.WithContext(ctx)
	status := "missing_unclassified"
	var match *uuid.UUID
	_, nativeErr := s.native.ReadLocation(ctx, authctx.CurrentUser{ID: job.UserID}, item.ImportedEmailID)
	if nativeErr != nil {
		if errors.Is(nativeErr, ErrMailNativeMessageMissing) {
			status = "native_missing"
		} else {
			status = "native_unavailable"
		}
		updated := db.Model(&model.MailExternalLocationCase{}).Where("id=? AND user_id=? AND action_id IS NULL AND resolved_at IS NULL AND missing_observations>=?", row.ID, job.UserID, 2).Updates(map[string]any{"status": status, "candidate_item_id": nil, "updated_at": time.Now().UTC()})
		if updated.Error != nil {
			return row, apperr.Internal(updated.Error)
		}
		if updated.RowsAffected != 1 {
			return row, apperr.Conflict("mail.location_receipt_changed", "Location record changed")
		}
		row.Status = status
		row.CandidateItemID = nil
		return row, nil
	}
	if len(item.MIMEHash) == 64 && item.Size > 0 {
		var candidates []model.MailExternalImportItem
		err := db.Where("user_id=? AND external_account_id=? AND status=? AND job_id<>? AND mime_hash=? AND size=?", job.UserID, job.ExternalAccountID, "imported", job.ID, item.MIMEHash, item.Size).
			Order("created_at ASC").Limit(3).Find(&candidates).Error
		if err != nil {
			return row, apperr.Internal(err)
		}
		if len(candidates) > 1 {
			status = "ambiguous_candidates"
		}
		if len(candidates) == 1 {
			candidate := candidates[0]
			var destination model.MailExternalImportJob
			err = db.Where("id=? AND user_id=? AND external_account_id=?", candidate.JobID, job.UserID, job.ExternalAccountID).Take(&destination).Error
			if err != nil || (destination.Status != "completed" && destination.Status != "watching") {
				status = "candidate_unverified"
			} else {
				name, e := s.history.info(destination)
				if e != nil || name.Folder == oldFolder || destination.UIDValidity != candidate.UIDValidity || candidate.FolderKey != destination.FolderKey {
					status = "candidate_unverified"
				} else {
					probe, e := s.history.source.Inspect(ctx, provider, creds.Address, creds.AppPassword, name.Folder)
					if e != nil || probe.UIDValidity != candidate.UIDValidity || probe.UpperUID < candidate.SourceUID {
						status = "candidate_unverified"
					} else {
						messages, e := s.history.source.ListWindow(ctx, provider, creds.Address, creds.AppPassword, name.Folder, candidate.UIDValidity, candidate.SourceUID, candidate.SourceUID)
						if e != nil || len(messages) != 1 || messages[0].UID != candidate.SourceUID {
							status = "candidate_unverified"
						} else {
							raw, e := s.history.source.ReadRaw(ctx, provider, creds.Address, creds.AppPassword, name.Folder, candidate.UIDValidity, candidate.SourceUID)
							sum := sha256.Sum256(raw)
							if e != nil || uint64(len(raw)) != item.Size || hex.EncodeToString(sum[:]) != item.MIMEHash {
								status = "candidate_unverified"
							} else {
								status = "possible_move"
								match = &candidate.ID
							}
						}
					}
				}
			}
		}
	}
	updated := db.Model(&model.MailExternalLocationCase{}).Where("id=? AND user_id=? AND action_id IS NULL AND resolved_at IS NULL AND missing_observations>=?", row.ID, job.UserID, 2).
		Updates(map[string]any{"status": status, "candidate_item_id": match, "updated_at": time.Now().UTC()})
	if updated.Error != nil {
		return row, apperr.Internal(updated.Error)
	}
	if updated.RowsAffected != 1 {
		return row, apperr.Conflict("mail.location_receipt_changed", "Location record changed")
	}
	row.Status = status
	row.CandidateItemID = match
	return row, nil
}

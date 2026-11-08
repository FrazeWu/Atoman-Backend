package mail

import (
	"context"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const externalFlagSyncPageSize = 8
const externalFlagSyncInterval = 5 * time.Minute

// EnableFlagSync is an operational switch in addition to each user's explicit
// opt-in. It does not schedule any job or trigger any remote writes itself.
func (s *MailExternalHistoryService) EnableFlagSync(source MailExternalFlagSourceWriter, target MailExternalFlagTargetWriter) {
	if s == nil {
		return
	}
	s.flagsAllowed = source != nil && target != nil
	s.flagSourceWriter = source
	s.flagTargetWriter = target
}
func (s *MailExternalHistoryService) flagSyncReady() error {
	if err := s.valid(); err != nil {
		return err
	}
	if !s.flagsAllowed || s.flagSourceWriter == nil || s.flagTargetWriter == nil || s.flagReader == nil {
		return apperr.New(503, "mail.external_flag_sync_disabled", "Two-way flag changes are disabled", nil)
	}
	if !s.external.workspace.db.Migrator().HasColumn(&model.MailExternalImportJob{}, "flag_sync_enabled") || !s.external.workspace.db.Migrator().HasColumn(&model.MailExternalImportItem{}, "flag_mutation_id") || !s.external.workspace.db.Migrator().HasColumn(&model.MailExternalImportItem{}, "location_mutation_id") || !s.external.workspace.db.Migrator().HasTable(&model.MailExternalFlagAttempt{}) {
		return apperr.New(503, "mail.external_flag_migration_required", "Flag receipt migration is required", nil)
	}
	return nil
}
func (s *MailExternalHistoryService) flagJob(ctx context.Context, u authctx.CurrentUser, accountID, jobID uuid.UUID) (model.MailExternalImportJob, error) {
	var job model.MailExternalImportJob
	if err := s.external.access(ctx, u); err != nil {
		return job, err
	}
	if _, err := s.external.ownRow(ctx, u, accountID); err != nil {
		return job, err
	}
	err := s.external.workspace.db.WithContext(ctx).Where("id=? AND user_id=? AND external_account_id=?", jobID, u.ID, accountID).Take(&job).Error
	if err != nil {
		return job, apperr.NotFound("mail.external_history_missing", "Import job unavailable")
	}
	return job, nil
}
func (s *MailExternalHistoryService) ConfigureFlagSync(ctx context.Context, u authctx.CurrentUser, accountID, jobID uuid.UUID, enabled bool) (ExternalHistoryJobInfo, error) {
	if err := s.flagSyncReady(); err != nil {
		return ExternalHistoryJobInfo{}, err
	}
	job, err := s.flagJob(ctx, u, accountID, jobID)
	if err != nil {
		return ExternalHistoryJobInfo{}, err
	}
	if enabled && job.Status != "completed" && job.Status != "watching" {
		return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_flag_sync_not_ready", "Complete the source import first")
	}
	if enabled {
		var pending int64
		if err = s.external.workspace.db.WithContext(ctx).Model(&model.MailExternalImportItem{}).Where("job_id=? AND user_id=? AND (flag_mutation_id IS NOT NULL OR location_mutation_id IS NOT NULL)", jobID, u.ID).Count(&pending).Error; err != nil {
			return ExternalHistoryJobInfo{}, apperr.Internal(err)
		}
		if pending != 0 {
			return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_flag_reconcile_required", "Reconcile uncertain flag changes before enabling")
		}
	}
	now := time.Now().UTC()
	changes := map[string]any{"flag_sync_enabled": enabled, "flag_sync_generation": gorm.Expr("flag_sync_generation+1"), "updated_at": now}
	if enabled {
		changes["flag_last_polled_at"] = nil
		changes["flag_last_error_code"] = ""
	}
	result := s.external.workspace.db.WithContext(ctx).Model(&model.MailExternalImportJob{}).Where("id=? AND user_id=? AND external_account_id=? AND flag_sync_enabled=?", jobID, u.ID, accountID, job.FlagSyncEnabled).Updates(changes)
	if result.Error != nil {
		return ExternalHistoryJobInfo{}, apperr.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return ExternalHistoryJobInfo{}, apperr.Conflict("mail.external_flag_sync_state_changed", "Flag synchronization state changed")
	}
	job.FlagSyncEnabled = enabled
	job.FlagSyncGeneration++
	if enabled {
		job.FlagLastPolledAt = nil
		job.FlagLastErrorCode = ""
	}
	return s.info(job)
}

type ExternalFlagAttemptInfo struct {
	ID        uuid.UUID `json:"id"`
	SourceUID uint32    `json:"source_uid"`
	Flag      string    `json:"flag"`
	Direction string    `json:"direction"`
	Desired   bool      `json:"desired"`
	Status    string    `json:"status"`
	ErrorCode string    `json:"error_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func flagAttemptInfo(v model.MailExternalFlagAttempt) ExternalFlagAttemptInfo {
	return ExternalFlagAttemptInfo{ID: v.ID, SourceUID: v.SourceUID, Flag: v.Flag, Direction: v.Direction, Desired: v.Desired, Status: v.Status, ErrorCode: v.ErrorCode, CreatedAt: v.CreatedAt}
}
func (s *MailExternalHistoryService) FlagSyncAttempts(ctx context.Context, u authctx.CurrentUser, accountID, jobID uuid.UUID) ([]ExternalFlagAttemptInfo, error) {
	if err := s.flagSyncReady(); err != nil {
		return nil, err
	}
	if _, err := s.flagJob(ctx, u, accountID, jobID); err != nil {
		return nil, err
	}
	var rows []model.MailExternalFlagAttempt
	err := s.external.workspace.db.WithContext(ctx).Where("user_id=? AND external_account_id=? AND job_id=?", u.ID, accountID, jobID).Order("created_at DESC").Limit(40).Find(&rows).Error
	if err != nil {
		return nil, apperr.Internal(err)
	}
	result := make([]ExternalFlagAttemptInfo, 0, len(rows))
	for _, row := range rows {
		result = append(result, flagAttemptInfo(row))
	}
	return result, nil
}

// One stale or uncertain cross-provider operation permanently claims that
// particular import item until its owner explicitly reconciles the receipt.
func (s *MailExternalHistoryService) prepareFlagAttempt(ctx context.Context, job model.MailExternalImportJob, item model.MailExternalImportItem, flag, direction string, source, native ExternalFlagSnapshot, desired bool) (model.MailExternalFlagAttempt, error) {
	record := model.MailExternalFlagAttempt{ID: uuid.New(), UserID: job.UserID, ExternalAccountID: job.ExternalAccountID, JobID: job.ID, ImportItemID: item.ID, SourceUID: item.SourceUID, UIDValidity: item.UIDValidity, NativeEmailID: item.ImportedEmailID, Flag: flag, Direction: direction, ExpectedSource: false, ExpectedNative: false, Desired: desired, Status: "dispatching", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	record.ExpectedSource, _ = flagValue(source, flag)
	record.ExpectedNative, _ = flagValue(native, flag)
	baseline := item.BaselineSeen
	if flag == "flagged" {
		baseline = item.BaselineFlagged
	}
	err := s.external.workspace.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// An in-progress receipt is a durable per-item lease with no expiry: a
		// restart can NEVER reinterpret it as a new permission to retry.
		current := tx.Model(&model.MailExternalImportItem{}).Where("id=? AND job_id=? AND user_id=? AND status=? AND flags_baseline_known=? AND flag_mutation_id IS NULL AND location_mutation_id IS NULL", item.ID, job.ID, job.UserID, "imported", true)
		if flag == "seen" {
			current = current.Where("baseline_seen=?", baseline)
		} else {
			current = current.Where("baseline_flagged=?", baseline)
		}
		result := current.Update("flag_mutation_id", record.ID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("Import receipt changed or another write is pending")
		}
		return tx.Create(&record).Error
	})
	return record, err
}

func (s *MailExternalHistoryService) commitFlagAttempt(ctx context.Context, record model.MailExternalFlagAttempt) error {
	now := time.Now().UTC()
	return s.external.workspace.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		field := "baseline_seen"
		if record.Flag == "flagged" {
			field = "baseline_flagged"
		}
		current := tx.Model(&model.MailExternalImportItem{}).Where("id=? AND job_id=? AND user_id=? AND flag_mutation_id=? AND status=?", record.ImportItemID, record.JobID, record.UserID, record.ID, "imported")
		result := current.Updates(map[string]any{field: record.Desired, "flag_mutation_id": nil, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("Flag receipt lost its owner lock")
		}
		outcome := tx.Model(&model.MailExternalFlagAttempt{}).Where("id=? AND user_id=? AND status IN ?", record.ID, record.UserID, []string{"dispatching", "needs_review"}).Updates(map[string]any{"status": "applied", "error_code": "", "updated_at": now})
		if outcome.Error != nil {
			return outcome.Error
		}
		if outcome.RowsAffected != 1 {
			return errors.New("Flag attempt state changed")
		}
		return nil
	})
}
func (s *MailExternalHistoryService) markFlagNeedsReview(ctx context.Context, record model.MailExternalFlagAttempt, reason string) {
	if reason == "" {
		reason = "remote_outcome_unknown"
	}
	_ = s.external.workspace.db.WithContext(ctx).Model(&model.MailExternalFlagAttempt{}).Where("id=? AND status=?", record.ID, "dispatching").Updates(map[string]any{"status": "needs_review", "error_code": reason, "updated_at": time.Now().UTC()}).Error
	_ = s.external.workspace.db.WithContext(ctx).Model(&model.MailExternalImportJob{}).Where("id=? AND user_id=?", record.JobID, record.UserID).Updates(map[string]any{"flag_sync_enabled": false, "flag_sync_generation": gorm.Expr("flag_sync_generation+1"), "flag_last_error_code": reason, "updated_at": time.Now().UTC()}).Error
}

func (s *MailExternalHistoryService) flagCurrent(ctx context.Context, u authctx.CurrentUser, job model.MailExternalImportJob, item model.MailExternalImportItem) (ExternalFlagSnapshot, ExternalFlagSnapshot, string, error) {
	zero := ExternalFlagSnapshot{}
	if !validJMAPScopedID(item.ImportedEmailID) || item.UserID != u.ID || item.ExternalAccountID != job.ExternalAccountID || item.JobID != job.ID || item.FolderKey != job.FolderKey || item.UIDValidity != job.UIDValidity || item.SourceUID == 0 || item.Status != "imported" {
		return zero, zero, "", errors.New("Import provenance is invalid")
	}
	account, err := s.external.ownRow(ctx, u, job.ExternalAccountID)
	if err != nil {
		return zero, zero, "", err
	}
	secrets, err := s.external.secrets(account)
	if err != nil {
		return zero, zero, "", err
	}
	info, err := s.info(job)
	if err != nil {
		return zero, zero, "", err
	}
	probe, err := s.source.Inspect(ctx, account.Provider, secrets.Address, secrets.AppPassword, info.Folder)
	if err != nil || probe.UIDValidity != job.UIDValidity || probe.UpperUID < item.SourceUID {
		return zero, zero, "", errors.New("Source UID identity changed")
	}
	messages, err := s.source.ListWindow(ctx, account.Provider, secrets.Address, secrets.AppPassword, info.Folder, job.UIDValidity, item.SourceUID, item.SourceUID)
	if err != nil || len(messages) != 1 || messages[0].UID != item.SourceUID {
		return zero, zero, "", errors.New("Source UID unavailable")
	}
	snapshots, err := s.flagReader.ReadFlags(ctx, u, []string{item.ImportedEmailID})
	if err != nil {
		return zero, zero, "", errors.New("Native flags unavailable")
	}
	native, ok := snapshots[item.ImportedEmailID]
	if !ok {
		return zero, zero, "", errors.New("Native imported email unavailable")
	}
	source := ExternalFlagSnapshot{Seen: messages[0].Seen, Flagged: messages[0].Flagged}
	return source, native, info.Folder, nil
}

// Reconciliation makes no external writes: it only proves both sides already
// have the intended flag and updates the durable baseline once. Otherwise the
// uncertain receipt stays claimed for manual inspection.
func (s *MailExternalHistoryService) ReconcileFlagAttempt(ctx context.Context, u authctx.CurrentUser, accountID, jobID, attemptID uuid.UUID) (ExternalFlagAttemptInfo, error) {
	if err := s.flagSyncReady(); err != nil {
		return ExternalFlagAttemptInfo{}, err
	}
	job, err := s.flagJob(ctx, u, accountID, jobID)
	if err != nil {
		return ExternalFlagAttemptInfo{}, err
	}
	var record model.MailExternalFlagAttempt
	if err = s.external.workspace.db.WithContext(ctx).Where("id=? AND user_id=? AND external_account_id=? AND job_id=?", attemptID, u.ID, accountID, jobID).Take(&record).Error; err != nil {
		return ExternalFlagAttemptInfo{}, apperr.NotFound("mail.external_flag_attempt_missing", "Receipt unavailable")
	}
	if record.Status == "applied" {
		return flagAttemptInfo(record), nil
	}
	if record.Status == "dispatching" && !job.FlagSyncEnabled && time.Since(record.UpdatedAt) >= 20*time.Minute {
		// The transport has a 45-second hard cap. Escalate a long-abandoned
		// in-flight intent to review without sending anything a second time.
		changed := s.external.workspace.db.WithContext(ctx).Model(&model.MailExternalFlagAttempt{}).Where("id=? AND user_id=? AND status=?", record.ID, u.ID, "dispatching").Updates(map[string]any{"status": "needs_review", "error_code": "stale_dispatch", "updated_at": time.Now().UTC()})
		if changed.Error != nil || changed.RowsAffected != 1 {
			return flagAttemptInfo(record), apperr.Conflict("mail.external_flag_attempt_state", "Receipt changed")
		}
		record.Status = "needs_review"
	}
	if record.Status != "needs_review" {
		return flagAttemptInfo(record), apperr.Conflict("mail.external_flag_attempt_state", "Receipt cannot be reconciled")
	}
	var item model.MailExternalImportItem
	if err = s.external.workspace.db.WithContext(ctx).Where("id=? AND user_id=? AND job_id=? AND flag_mutation_id=?", record.ImportItemID, u.ID, jobID, record.ID).Take(&item).Error; err != nil {
		return flagAttemptInfo(record), apperr.Conflict("mail.external_flag_attempt_state", "Receipt ownership changed")
	}
	source, native, _, err := s.flagCurrent(ctx, u, job, item)
	if err != nil {
		return flagAttemptInfo(record), apperr.Conflict("mail.external_flag_review_required", "Source or native message needs review")
	}
	sv, _ := flagValue(source, record.Flag)
	nv, _ := flagValue(native, record.Flag)
	if sv != record.Desired || nv != record.Desired {
		return flagAttemptInfo(record), apperr.Conflict("mail.external_flag_review_required", "Source and native flags are not both confirmed")
	}
	if err = s.commitFlagAttempt(ctx, record); err != nil {
		return flagAttemptInfo(record), apperr.New(503, "mail.external_flag_review_required", "Receipt update could not be confirmed", nil)
	}
	record.Status = "applied"
	record.ErrorCode = ""
	return flagAttemptInfo(record), nil
}

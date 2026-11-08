package mail

import (
	"context"
	"errors"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"gorm.io/gorm"
)

type ExternalFlagSyncReport struct {
	JobsClaimed       int `json:"jobs_claimed"`
	Checked           int `json:"checked"`
	Applied           int `json:"applied"`
	BaselinesAdvanced int `json:"baselines_advanced"`
	Skipped           int `json:"skipped"`
	NeedReview        int `json:"need_review"`
}

// RunExternalFlagSyncDue is called ONLY when the operator has explicitly
// enabled the global feature gate and an account owner opted in for this job.
// A job at most scans eight import receipts and conditionally writes one tracked flag per message
// per run. It never issues folder MOVE, deletion, or a second write after a
// persistent uncertain receipt is found.
func RunExternalFlagSyncDue(ctx context.Context, s *MailExternalHistoryService) (ExternalFlagSyncReport, error) {
	result := ExternalFlagSyncReport{}
	if err := s.flagSyncReady(); err != nil {
		return result, err
	}
	now := time.Now().UTC()
	cutoff := now.Add(-externalFlagSyncInterval)
	db := s.external.workspace.db.WithContext(ctx)
	var jobs []model.MailExternalImportJob
	err := db.Where("flag_sync_enabled=? AND status IN ? AND (flag_last_polled_at IS NULL OR flag_last_polled_at < ?)", true, []string{"completed", "watching"}, cutoff).Order("updated_at ASC").Limit(2).Find(&jobs).Error
	if err != nil {
		return result, err
	}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		claim := db.Model(&model.MailExternalImportJob{}).Where("id=? AND user_id=? AND flag_sync_enabled=? AND status IN ? AND (flag_last_polled_at IS NULL OR flag_last_polled_at < ?)", job.ID, job.UserID, true, []string{"completed", "watching"}, cutoff).Where("flag_sync_generation=?", job.FlagSyncGeneration).Update("flag_last_polled_at", time.Now().UTC())
		if claim.Error != nil {
			return result, claim.Error
		}
		if claim.RowsAffected != 1 {
			continue
		}
		result.JobsClaimed++
		u := authctx.CurrentUser{ID: job.UserID, Role: "user"}
		if err := s.processFlagJob(ctx, u, job, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}
func (s *MailExternalHistoryService) disableFlagJob(ctx context.Context, job model.MailExternalImportJob, code string) error {
	return s.external.workspace.db.WithContext(ctx).Model(&model.MailExternalImportJob{}).Where("id=? AND user_id=?", job.ID, job.UserID).Updates(map[string]any{"flag_sync_enabled": false, "flag_sync_generation": gorm.Expr("flag_sync_generation+1"), "flag_last_error_code": code, "updated_at": time.Now().UTC()}).Error
}
func (s *MailExternalHistoryService) advanceFlagBaseline(ctx context.Context, item model.MailExternalImportItem, source ExternalFlagSnapshot) error {
	db := s.external.workspace.db.WithContext(ctx)
	res := db.Model(&model.MailExternalImportItem{}).Where("id=? AND user_id=? AND status=? AND flag_mutation_id IS NULL AND flags_baseline_known=? AND baseline_seen=? AND baseline_flagged=?", item.ID, item.UserID, "imported", true, item.BaselineSeen, item.BaselineFlagged).
		Updates(map[string]any{"baseline_seen": source.Seen, "baseline_flagged": source.Flagged, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errors.New("Flag baseline changed concurrently")
	}
	return nil
}
func (s *MailExternalHistoryService) abortPreparedWithoutWrite(ctx context.Context, attempt model.MailExternalFlagAttempt) error {
	return s.external.workspace.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.MailExternalImportItem{}).Where("id=? AND user_id=? AND flag_mutation_id=?", attempt.ImportItemID, attempt.UserID, attempt.ID).Update("flag_mutation_id", nil)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("Pending intent changed")
		}
		result := tx.Model(&model.MailExternalFlagAttempt{}).Where("id=? AND user_id=? AND status=?", attempt.ID, attempt.UserID, "dispatching").Updates(map[string]any{"status": "aborted_prewrite", "error_code": "state_changed", "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("Write attempt changed")
		}
		return nil
	})
}
func (s *MailExternalHistoryService) processFlagJob(ctx context.Context, u authctx.CurrentUser, job model.MailExternalImportJob, report *ExternalFlagSyncReport) error {
	db := s.external.workspace.db.WithContext(ctx)
	if _, err := s.flagJob(ctx, u, job.ExternalAccountID, job.ID); err != nil {
		return s.disableFlagJob(ctx, job, "owner_unavailable")
	}
	var pending int64
	if err := db.Model(&model.MailExternalImportItem{}).Where("job_id=? AND user_id=? AND flag_mutation_id IS NOT NULL", job.ID, u.ID).Count(&pending).Error; err != nil {
		return err
	}
	if pending != 0 {
		report.NeedReview++
		return s.disableFlagJob(ctx, job, "flag_receipt_pending")
	}
	var items []model.MailExternalImportItem
	err := db.Where("job_id=? AND user_id=? AND status=? AND source_uid > ?", job.ID, u.ID, "imported", job.FlagSyncCursorUID).Order("source_uid ASC").Limit(externalFlagSyncPageSize).Find(&items).Error
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return db.Model(&model.MailExternalImportJob{}).Where("id=? AND user_id=? AND flag_sync_enabled=? AND flag_sync_generation=?", job.ID, u.ID, true, job.FlagSyncGeneration).Update("flag_sync_cursor_uid", 0).Error
	}
	lastUID := job.FlagSyncCursorUID
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		var currently model.MailExternalImportJob
		if err := db.Select("flag_sync_enabled", "flag_sync_generation", "status").Where("id=? AND user_id=?", job.ID, u.ID).Take(&currently).Error; err != nil {
			return err
		}
		if !currently.FlagSyncEnabled || currently.FlagSyncGeneration != job.FlagSyncGeneration || (currently.Status != "completed" && currently.Status != "watching") {
			break
		}
		report.Checked++
		lastUID = item.SourceUID
		if !item.FlagsBaselineKnown {
			report.Skipped++
			continue
		}
		if item.FlagMutationID != nil {
			report.NeedReview++
			return s.disableFlagJob(ctx, job, "flag_receipt_pending")
		}
		source, native, folder, readErr := s.flagCurrent(ctx, u, job, item)
		if readErr != nil {
			report.NeedReview++
			return s.disableFlagJob(ctx, job, "flag_identity_unavailable")
		}
		planSeen := chooseExternalFlagPlan(item.BaselineSeen, source.Seen, native.Seen)
		planFlagged := chooseExternalFlagPlan(item.BaselineFlagged, source.Flagged, native.Flagged)
		if planSeen.Decision == "conflict" || planFlagged.Decision == "conflict" {
			report.NeedReview++
			return s.disableFlagJob(ctx, job, "flag_conflict")
		}
		if planSeen.Decision == "in_sync" && planFlagged.Decision == "in_sync" {
			if source.Seen != item.BaselineSeen || source.Flagged != item.BaselineFlagged {
				if err := s.advanceFlagBaseline(ctx, item, source); err != nil {
					report.NeedReview++
					return s.disableFlagJob(ctx, job, "baseline_changed")
				}
				report.BaselinesAdvanced++
			}
			continue
		}
		// Only one flag change per item per sweep: the other flag is revisited
		// when the cursor wraps, after both sides have been observed again.
		flag := "seen"
		plan := planSeen
		if plan.Decision == "in_sync" {
			flag = "flagged"
			plan = planFlagged
		}
		desired := plan.Source
		if plan.Decision == "copy_to_source" {
			desired = plan.Native
		}
		if plan.Decision != "copy_to_source" && plan.Decision != "copy_to_native" {
			report.NeedReview++
			return s.disableFlagJob(ctx, job, "flag_conflict")
		}
		attempt, prepareErr := s.prepareFlagAttempt(ctx, job, item, flag, plan.Decision, source, native, desired)
		if prepareErr != nil {
			report.NeedReview++
			return s.disableFlagJob(ctx, job, "flag_claim_conflict")
		}
		// Do not even begin a remote write after a concurrent opt-out or a change
		// in either observed state. A known pre-write abort is safe to retry later.
		var gate model.MailExternalImportJob
		gateErr := db.Select("flag_sync_enabled", "flag_sync_generation", "status").Where("id=? AND user_id=?", job.ID, u.ID).Take(&gate).Error
		if gateErr != nil || !gate.FlagSyncEnabled || gate.FlagSyncGeneration != job.FlagSyncGeneration || (gate.Status != "completed" && gate.Status != "watching") {
			if err := s.abortPreparedWithoutWrite(ctx, attempt); err != nil {
				s.markFlagNeedsReview(ctx, attempt, "abort_unconfirmed")
				report.NeedReview++
			}
			return nil
		}
		sourceNow, nativeNow, _, readErr := s.flagCurrent(ctx, u, job, item)
		if readErr != nil || sourceNow != source || nativeNow != native {
			if err := s.abortPreparedWithoutWrite(ctx, attempt); err != nil {
				s.markFlagNeedsReview(ctx, attempt, "abort_unconfirmed")
				report.NeedReview++
			}
			report.Skipped++
			continue
		}
		account, err := s.external.ownRow(ctx, u, job.ExternalAccountID)
		if err != nil {
			s.markFlagNeedsReview(ctx, attempt, "account_unavailable")
			report.NeedReview++
			return nil
		}
		secrets, err := s.external.secrets(account)
		if err != nil {
			s.markFlagNeedsReview(ctx, attempt, "credential_unavailable")
			report.NeedReview++
			return nil
		}
		if plan.Decision == "copy_to_source" {
			err = s.flagSourceWriter.SetFlagIfUnchanged(ctx, account.Provider, secrets.Address, secrets.AppPassword, folder, job.UIDValidity, item.SourceUID, flag, source, desired)
		} else {
			err = s.flagTargetWriter.SetFlagIfUnchanged(ctx, u, item.ImportedEmailID, flag, native, desired)
		}
		if err != nil {
			if errors.Is(err, ErrMailFlagPrecondition) {
				if abortErr := s.abortPreparedWithoutWrite(ctx, attempt); abortErr != nil {
					s.markFlagNeedsReview(ctx, attempt, "abort_unconfirmed")
					report.NeedReview++
					return nil
				}
				report.Skipped++
				return s.disableFlagJob(ctx, job, "conditional_precondition_failed")
			}
			s.markFlagNeedsReview(ctx, attempt, "conditional_write_unknown")
			report.NeedReview++
			return nil
		}
		confirmedSource, confirmedNative, _, readErr := s.flagCurrent(ctx, u, job, item)
		sv, _ := flagValue(confirmedSource, flag)
		nv, _ := flagValue(confirmedNative, flag)
		if readErr != nil || sv != desired || nv != desired {
			s.markFlagNeedsReview(ctx, attempt, "confirmation_unknown")
			report.NeedReview++
			return nil
		}
		if err = s.commitFlagAttempt(ctx, attempt); err != nil {
			s.markFlagNeedsReview(ctx, attempt, "receipt_commit_unknown")
			report.NeedReview++
			return nil
		}
		report.Applied++
	}
	nextCursor := lastUID
	if len(items) < externalFlagSyncPageSize {
		nextCursor = 0
	}
	return db.Model(&model.MailExternalImportJob{}).Where("id=? AND user_id=? AND flag_sync_enabled=? AND flag_sync_generation=?", job.ID, u.ID, true, job.FlagSyncGeneration).Updates(map[string]any{"flag_sync_cursor_uid": nextCursor, "flag_last_error_code": "", "updated_at": time.Now().UTC()}).Error
}

package mail

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type mailNativeLocationEnvelope struct {
	Before map[string]bool `json:"before"`
	After  map[string]bool `json:"after"`
}
type MailLocationActionInfo struct {
	ID        uuid.UUID `json:"id"`
	CaseID    uuid.UUID `json:"case_id"`
	Action    string    `json:"action"`
	Status    string    `json:"status"`
	ErrorCode string    `json:"error_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func externalLocationActionInfo(a model.MailExternalLocationAction) MailLocationActionInfo {
	return MailLocationActionInfo{ID: a.ID, CaseID: a.CaseID, Action: a.Action, Status: a.Status, ErrorCode: a.ErrorCode, CreatedAt: a.CreatedAt}
}
func (s *MailExternalLocationService) ownedCase(ctx context.Context, u authctx.CurrentUser, accountID, jobID, caseID uuid.UUID) (model.MailExternalImportJob, model.MailExternalLocationCase, model.MailExternalImportItem, error) {
	job, c, item := model.MailExternalImportJob{}, model.MailExternalLocationCase{}, model.MailExternalImportItem{}
	if err := s.ready(); err != nil {
		return job, c, item, err
	}
	var err error
	job, err = s.history.flagJob(ctx, u, accountID, jobID)
	if err != nil {
		return job, c, item, err
	}
	db := s.history.external.workspace.db.WithContext(ctx)
	err = db.Where("id=? AND user_id=? AND external_account_id=? AND job_id=?", caseID, u.ID, accountID, jobID).Take(&c).Error
	if err != nil {
		return job, c, item, apperr.NotFound("mail.location_case_missing", "Location case unavailable")
	}
	err = db.Where("id=? AND job_id=? AND user_id=? AND external_account_id=? AND status=?", c.ImportItemID, jobID, u.ID, accountID, "imported").Take(&item).Error
	if err != nil || item.SourceUID != c.SourceUID || item.ImportedEmailID != c.NativeEmailID || item.UIDValidity != c.UIDValidity || item.FolderKey != job.FolderKey {
		return job, c, item, apperr.Conflict("mail.location_receipt_invalid", "Origin provenance no longer matches")
	}
	return job, c, item, nil
}
func (s *MailExternalLocationService) sourceAbsent(ctx context.Context, u authctx.CurrentUser, job model.MailExternalImportJob, item model.MailExternalImportItem) (bool, error) {
	account, err := s.history.external.ownRow(ctx, u, job.ExternalAccountID)
	if err != nil {
		return false, err
	}
	creds, err := s.history.external.secrets(account)
	if err != nil {
		return false, err
	}
	info, err := s.history.info(job)
	if err != nil {
		return false, err
	}
	probe, err := s.history.source.Inspect(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder)
	if err != nil || probe.UIDValidity != job.UIDValidity || probe.UpperUID < job.UpperUID {
		return false, apperr.Conflict("mail.location_uid_changed", "Original folder UID identity changed")
	}
	rows, err := s.history.source.ListWindow(ctx, account.Provider, creds.Address, creds.AppPassword, info.Folder, job.UIDValidity, item.SourceUID, item.SourceUID)
	if err != nil {
		return false, apperr.New(502, "mail.location_source_unavailable", "Original source cannot be verified", nil)
	}
	if len(rows) > 1 || (len(rows) == 1 && rows[0].UID != item.SourceUID) {
		return false, apperr.Conflict("mail.location_uid_changed", "Source UID identity changed")
	}
	return len(rows) == 0, nil
}
func (s *MailExternalLocationService) caseActionAllowed(c model.MailExternalLocationCase, action string) bool {
	if c.ActionID != nil {
		return false
	}
	if action == "restore_native" {
		return c.Status == "trashed" || c.Status == "moved_resolved"
	}
	if c.ResolvedAt != nil || c.MissingObservations < 2 {
		return false
	}
	switch action {
	case "keep_native":
		return c.Status == "missing_unclassified" || c.Status == "possible_move" || c.Status == "ambiguous_candidates" || c.Status == "candidate_unverified"
	case "trash_native":
		return c.Status == "missing_unclassified" || c.Status == "possible_move"
	case "resolve_move":
		return c.Status == "possible_move" && c.CandidateItemID != nil
	}
	return false
}
func (s *MailExternalLocationService) verifyCandidate(ctx context.Context, u authctx.CurrentUser, job model.MailExternalImportJob, item model.MailExternalImportItem, c model.MailExternalLocationCase) error {
	if c.CandidateItemID == nil {
		return apperr.Conflict("mail.location_candidate_missing", "Unique moved destination not available")
	}
	account, err := s.history.external.ownRow(ctx, u, job.ExternalAccountID)
	if err != nil {
		return err
	}
	creds, err := s.history.external.secrets(account)
	if err != nil {
		return err
	}
	var candidate model.MailExternalImportItem
	db := s.history.external.workspace.db.WithContext(ctx)
	err = db.Where("id=? AND user_id=? AND external_account_id=? AND status=?", *c.CandidateItemID, u.ID, job.ExternalAccountID, "imported").Take(&candidate).Error
	if err != nil || candidate.JobID == job.ID || candidate.MIMEHash != item.MIMEHash || candidate.Size != item.Size || candidate.ImportedEmailID == item.ImportedEmailID {
		return apperr.Conflict("mail.location_candidate_changed", "Potential moved destination needs rechecking")
	}
	updated, err := s.classifyMissing(ctx, job, item, c, account.Provider, creds, "")
	if err != nil {
		return err
	}
	if updated.Status != "possible_move" || updated.CandidateItemID == nil || *updated.CandidateItemID != candidate.ID {
		return apperr.Conflict("mail.location_candidate_changed", "Potential moved destination was not uniquely verified")
	}
	// Two distinct imported copies may exist. Confirm the destination copy is
	// still in its own imported folder; never delete it during reconciliation.
	var destination model.MailExternalImportJob
	err = db.Where("id=? AND user_id=? AND external_account_id=?", candidate.JobID, u.ID, job.ExternalAccountID).Take(&destination).Error
	if err != nil {
		return apperr.Conflict("mail.location_candidate_changed", "Destination history job unavailable")
	}
	info, err := s.history.info(destination)
	if err != nil {
		return err
	}
	targetName := externalHistoryFolderName(account.Provider, destination.FolderKey, info.Folder)
	targetID, err := s.native.FindImportedFolder(ctx, u, targetName)
	if err != nil {
		return apperr.Conflict("mail.location_candidate_changed", "Destination native folder unavailable")
	}
	native, err := s.native.ReadLocation(ctx, u, candidate.ImportedEmailID)
	if err != nil || !reflect.DeepEqual(native.MailboxIDs, map[string]bool{targetID: true}) {
		return apperr.Conflict("mail.location_candidate_changed", "Destination imported copy changed")
	}
	return nil
}
func (s *MailExternalLocationService) Actions(ctx context.Context, u authctx.CurrentUser, accountID, jobID, caseID uuid.UUID) ([]MailLocationActionInfo, error) {
	_, _, _, err := s.ownedCase(ctx, u, accountID, jobID, caseID)
	if err != nil {
		return nil, err
	}
	var actions []model.MailExternalLocationAction
	err = s.history.external.workspace.db.WithContext(ctx).Where("user_id=? AND external_account_id=? AND job_id=? AND case_id=?", u.ID, accountID, jobID, caseID).Order("created_at DESC").Limit(20).Find(&actions).Error
	if err != nil {
		return nil, apperr.Internal(err)
	}
	result := make([]MailLocationActionInfo, 0, len(actions))
	for _, v := range actions {
		result = append(result, externalLocationActionInfo(v))
	}
	return result, nil
}
func (s *MailExternalLocationService) loadEnvelope(u authctx.CurrentUser, attempt model.MailExternalLocationAction) (mailNativeLocationEnvelope, error) {
	output := mailNativeLocationEnvelope{}
	raw, err := s.history.external.workspace.vault.Open(u.ID, attempt.ID, attempt.BeforeAfterCiphertext)
	if err != nil {
		return output, err
	}
	if json.Unmarshal(raw, &output) != nil || len(output.Before) == 0 || len(output.After) == 0 {
		return output, errors.New("Invalid encrypted location snapshot")
	}
	for _, m := range []map[string]bool{output.Before, output.After} {
		if len(m) > 10 {
			return output, errors.New("Unbounded location snapshot")
		}
		for id, val := range m {
			if !val || !validJMAPScopedID(id) {
				return output, errors.New("Untrusted mailbox snapshot")
			}
		}
	}
	return output, nil
}
func (s *MailExternalLocationService) oldestTrashAction(ctx context.Context, u authctx.CurrentUser, c model.MailExternalLocationCase) (model.MailExternalLocationAction, error) {
	result := model.MailExternalLocationAction{}
	err := s.history.external.workspace.db.WithContext(ctx).Where("user_id=? AND case_id=? AND status=? AND action IN ?", u.ID, c.ID, "applied", []string{"trash_native", "resolve_move"}).Order("created_at DESC").Take(&result).Error
	return result, err
}
func (s *MailExternalLocationService) Resolve(ctx context.Context, u authctx.CurrentUser, accountID, jobID, caseID uuid.UUID, action string) (MailLocationCaseInfo, error) {
	zero := MailLocationCaseInfo{}
	if action != "keep_native" && action != "trash_native" && action != "resolve_move" && action != "restore_native" {
		return zero, apperr.BadRequest("mail.location_invalid_action", "Unsupported location action")
	}
	job, c, item, err := s.ownedCase(ctx, u, accountID, jobID, caseID)
	if err != nil {
		return zero, err
	}
	if !s.caseActionAllowed(c, action) {
		return zero, apperr.Conflict("mail.location_state_changed", "Location case is not ready for action")
	}
	if job.Status != "completed" && job.Status != "watching" {
		return zero, apperr.Conflict("mail.location_import_incomplete", "Source import must be complete")
	}
	if action != "restore_native" {
		missing, e := s.sourceAbsent(ctx, u, job, item)
		if e != nil {
			return zero, e
		}
		if !missing {
			return zero, apperr.Conflict("mail.location_source_returned", "Original source UID is present; no deletion inferred")
		}
	}
	// Keep is also a user decision that closes the source/native link.
	// Never mark a nonexistent copy "kept", and do not race an already
	// authorized flag writer against this change of interpretation.
	if item.FlagMutationID != nil || item.LocationMutationID != nil || job.FlagSyncEnabled {
		return zero, apperr.Conflict("mail.location_flag_sync_active", "Pause or reconcile flag changes before resolving a location")
	}
	if action == "keep_native" {
		if _, nativeErr := s.native.ReadLocation(ctx, u, item.ImportedEmailID); nativeErr != nil {
			return zero, apperr.Conflict("mail.location_native_missing", "Native imported copy cannot be confirmed")
		}
		now := time.Now().UTC()
		result := s.history.external.workspace.db.WithContext(ctx).Model(&model.MailExternalLocationCase{}).Where("id=? AND user_id=? AND action_id IS NULL AND resolved_at IS NULL AND status=? AND missing_observations>=?", c.ID, u.ID, c.Status, 2).
			Updates(map[string]any{"status": "kept", "resolved_at": now, "updated_at": now})
		if result.Error != nil {
			return zero, apperr.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return zero, apperr.Conflict("mail.location_state_changed", "Location case changed")
		}
		c.Status = "kept"
		c.ResolvedAt = &now
		return s.caseInfo(ctx, job, c)
	}
	if action == "resolve_move" {
		if err := s.verifyCandidate(ctx, u, job, item, c); err != nil {
			return zero, err
		}
	}
	before, err := s.native.ReadLocation(ctx, u, item.ImportedEmailID)
	if err != nil {
		return zero, apperr.Conflict("mail.location_native_missing", "Original imported native email unavailable")
	}
	var target map[string]bool
	if action == "restore_native" {
		previous, e := s.oldestTrashAction(ctx, u, c)
		if e != nil {
			return zero, apperr.Conflict("mail.location_restore_missing", "No confirmed reversible trash action")
		}
		envelope, e := s.loadEnvelope(u, previous)
		if e != nil {
			return zero, apperr.Conflict("mail.location_restore_missing", "Original mailbox snapshot invalid")
		}
		if !reflect.DeepEqual(copyFlags(before.MailboxIDs), copyFlags(envelope.After)) {
			return zero, apperr.Conflict("mail.location_restore_changed", "Native email was moved after trash")
		}
		target = envelope.Before
		if e = s.native.ValidateMailboxes(ctx, u, target); e != nil {
			return zero, apperr.Conflict("mail.location_restore_missing", "Original native folder no longer exists")
		}
	} else {
		account, e := s.history.external.ownRow(ctx, u, accountID)
		if e != nil {
			return zero, e
		}
		info, e := s.history.info(job)
		if e != nil {
			return zero, e
		}
		original := externalHistoryFolderName(account.Provider, job.FolderKey, info.Folder)
		originalID, e := s.native.FindImportedFolder(ctx, u, original)
		if e != nil {
			return zero, apperr.Conflict("mail.location_native_changed", "Original native folder unavailable")
		}
		if !reflect.DeepEqual(copyFlags(before.MailboxIDs), map[string]bool{originalID: true}) {
			return zero, apperr.Conflict("mail.location_native_changed", "Native copy was already moved by another client")
		}
		trash, e := s.native.TrashMailbox(ctx, u)
		if e != nil {
			return zero, apperr.Conflict("mail.location_trash_missing", "Existing native trash folder unavailable")
		}
		target = map[string]bool{trash: true}
	}
	if reflect.DeepEqual(copyFlags(before.MailboxIDs), copyFlags(target)) {
		return zero, apperr.Conflict("mail.location_state_changed", "Native copy already in target folder")
	}
	attempt := model.MailExternalLocationAction{ID: uuid.New(), UserID: u.ID, ExternalAccountID: accountID, JobID: jobID, CaseID: c.ID, ImportItemID: item.ID, NativeEmailID: item.ImportedEmailID, Action: action, Status: "dispatching", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	raw, _ := json.Marshal(mailNativeLocationEnvelope{Before: copyFlags(before.MailboxIDs), After: copyFlags(target)})
	attempt.BeforeAfterCiphertext, err = s.history.external.workspace.vault.Seal(u.ID, attempt.ID, raw)
	if err != nil {
		return zero, apperr.New(503, "mail.location_vault_unavailable", "Location receipt encryption failed", nil)
	}
	db := s.history.external.workspace.db.WithContext(ctx)
	err = db.Transaction(func(tx *gorm.DB) error {
		locked := tx.Model(&model.MailExternalImportItem{}).Where("id=? AND user_id=? AND job_id=? AND status=? AND flag_mutation_id IS NULL AND location_mutation_id IS NULL", item.ID, u.ID, jobID, "imported").Update("location_mutation_id", attempt.ID)
		if locked.Error != nil {
			return locked.Error
		}
		if locked.RowsAffected != 1 {
			return errors.New("Imported copy is already being changed")
		}
		claim := tx.Model(&model.MailExternalLocationCase{}).Where("id=? AND user_id=? AND job_id=? AND status=? AND action_id IS NULL", c.ID, u.ID, jobID, c.Status).Update("action_id", attempt.ID)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return errors.New("Case already has a pending action")
		}
		return tx.Create(&attempt).Error
	})
	if err != nil {
		return zero, apperr.Conflict("mail.location_state_changed", "A location action is already pending")
	}
	if action != "restore_native" {
		stillAbsent, checkErr := s.sourceAbsent(ctx, u, job, item)
		if checkErr != nil || !stillAbsent {
			if e := s.abortPrewrite(ctx, attempt); e != nil {
				s.markLocationNeedsReview(ctx, attempt, "abort_unconfirmed")
			}
			return zero, apperr.Conflict("mail.location_source_returned", "Original source UID changed before native write")
		}
	}
	if err = s.native.MoveLocation(ctx, u, item.ImportedEmailID, before, target); err != nil {
		if errors.Is(err, ErrMailFlagPrecondition) {
			if e := s.abortPrewrite(ctx, attempt); e != nil {
				s.markLocationNeedsReview(ctx, attempt, "abort_unconfirmed")
			}
		} else {
			s.markLocationNeedsReview(ctx, attempt, "remote_move_unknown")
		}
		return zero, apperr.Conflict("mail.location_move_unconfirmed", "Native mailbox change could not be confirmed; review receipt")
	}
	current, err := s.native.ReadLocation(ctx, u, item.ImportedEmailID)
	if err != nil || !reflect.DeepEqual(copyFlags(current.MailboxIDs), copyFlags(target)) {
		s.markLocationNeedsReview(ctx, attempt, "confirmation_unknown")
		return zero, apperr.Conflict("mail.location_move_unconfirmed", "Native mailbox confirmation unavailable")
	}
	if err = s.commitLocation(ctx, attempt); err != nil {
		s.markLocationNeedsReview(ctx, attempt, "receipt_commit_unknown")
		return zero, apperr.New(503, "mail.location_receipt_unknown", "Location receipt requires review", nil)
	}
	c.ActionID = nil
	now := time.Now().UTC()
	c.ResolvedAt = &now
	switch action {
	case "restore_native":
		c.Status = "restored"
	case "resolve_move":
		c.Status = "moved_resolved"
	default:
		c.Status = "trashed"
	}
	return s.caseInfo(ctx, job, c)
}
func (s *MailExternalLocationService) abortPrewrite(ctx context.Context, a model.MailExternalLocationAction) error {
	return s.history.external.workspace.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item := tx.Model(&model.MailExternalImportItem{}).Where("id=? AND user_id=? AND job_id=? AND location_mutation_id=?", a.ImportItemID, a.UserID, a.JobID, a.ID).Update("location_mutation_id", nil)
		if item.Error != nil {
			return item.Error
		}
		if item.RowsAffected != 1 {
			return errors.New("Import item location claim changed")
		}
		first := tx.Model(&model.MailExternalLocationCase{}).Where("id=? AND user_id=? AND action_id=?", a.CaseID, a.UserID, a.ID).Update("action_id", nil)
		if first.Error != nil {
			return first.Error
		}
		if first.RowsAffected != 1 {
			return errors.New("Action claim changed")
		}
		second := tx.Model(&model.MailExternalLocationAction{}).Where("id=? AND user_id=? AND status=?", a.ID, a.UserID, "dispatching").Updates(map[string]any{"status": "aborted_prewrite", "error_code": "state_changed", "updated_at": time.Now().UTC()})
		if second.Error != nil {
			return second.Error
		}
		if second.RowsAffected != 1 {
			return errors.New("Action status changed")
		}
		return nil
	})
}
func (s *MailExternalLocationService) markLocationNeedsReview(ctx context.Context, a model.MailExternalLocationAction, code string) {
	db := s.history.external.workspace.db.WithContext(ctx)
	_ = db.Model(&model.MailExternalLocationAction{}).Where("id=? AND user_id=? AND status=?", a.ID, a.UserID, "dispatching").Updates(map[string]any{"status": "needs_review", "error_code": code, "updated_at": time.Now().UTC()}).Error
	_ = db.Model(&model.MailExternalLocationCase{}).Where("id=? AND user_id=? AND action_id=?", a.CaseID, a.UserID, a.ID).Updates(map[string]any{"status": "action_needs_review", "updated_at": time.Now().UTC()}).Error
}
func (s *MailExternalLocationService) commitLocation(ctx context.Context, a model.MailExternalLocationAction) error {
	status := "trashed"
	if a.Action == "resolve_move" {
		status = "moved_resolved"
	}
	if a.Action == "restore_native" {
		status = "restored"
	}
	now := time.Now().UTC()
	return s.history.external.workspace.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		item := tx.Model(&model.MailExternalImportItem{}).Where("id=? AND user_id=? AND job_id=? AND location_mutation_id=?", a.ImportItemID, a.UserID, a.JobID, a.ID).Update("location_mutation_id", nil)
		if item.Error != nil {
			return item.Error
		}
		if item.RowsAffected != 1 {
			return errors.New("Import item location claim changed")
		}
		row := tx.Model(&model.MailExternalLocationCase{}).Where("id=? AND user_id=? AND job_id=? AND action_id=?", a.CaseID, a.UserID, a.JobID, a.ID).Updates(map[string]any{"status": status, "action_id": nil, "resolved_at": now, "updated_at": now})
		if row.Error != nil {
			return row.Error
		}
		if row.RowsAffected != 1 {
			return errors.New("Case action claim changed")
		}
		attempt := tx.Model(&model.MailExternalLocationAction{}).Where("id=? AND user_id=? AND status IN ?", a.ID, a.UserID, []string{"dispatching", "needs_review"}).Updates(map[string]any{"status": "applied", "error_code": "", "updated_at": now})
		if attempt.Error != nil {
			return attempt.Error
		}
		if attempt.RowsAffected != 1 {
			return errors.New("Action receipt status changed")
		}
		return nil
	})
}
func (s *MailExternalLocationService) Reconcile(ctx context.Context, u authctx.CurrentUser, accountID, jobID, caseID, actionID uuid.UUID) (MailLocationActionInfo, error) {
	_, c, item, err := s.ownedCase(ctx, u, accountID, jobID, caseID)
	if err != nil {
		return MailLocationActionInfo{}, err
	}
	var action model.MailExternalLocationAction
	db := s.history.external.workspace.db.WithContext(ctx)
	err = db.Where("id=? AND user_id=? AND external_account_id=? AND job_id=? AND case_id=? AND import_item_id=?", actionID, u.ID, accountID, jobID, caseID, item.ID).Take(&action).Error
	if err != nil {
		return MailLocationActionInfo{}, apperr.NotFound("mail.location_action_missing", "Location receipt unavailable")
	}
	if action.Status == "applied" {
		return externalLocationActionInfo(action), nil
	}
	if c.ActionID == nil || *c.ActionID != action.ID || item.LocationMutationID == nil || *item.LocationMutationID != action.ID {
		return externalLocationActionInfo(action), apperr.Conflict("mail.location_action_state_changed", "Receipt no longer owns native copy")
	}
	if action.Status == "dispatching" && time.Since(action.UpdatedAt) < 20*time.Minute {
		return externalLocationActionInfo(action), apperr.Conflict("mail.location_action_running", "Location action may still be running")
	}
	if action.Status != "needs_review" && action.Status != "dispatching" {
		return externalLocationActionInfo(action), apperr.Conflict("mail.location_action_state_changed", "Receipt cannot be reconciled")
	}
	envelope, err := s.loadEnvelope(u, action)
	if err != nil {
		return externalLocationActionInfo(action), apperr.Conflict("mail.location_action_state_changed", "Action snapshot invalid")
	}
	current, err := s.native.ReadLocation(ctx, u, action.NativeEmailID)
	if err != nil || !reflect.DeepEqual(copyFlags(current.MailboxIDs), copyFlags(envelope.After)) {
		return externalLocationActionInfo(action), apperr.Conflict("mail.location_action_review_required", "Native mailbox does not match recorded target; no retry performed")
	}
	if err = s.commitLocation(ctx, action); err != nil {
		return externalLocationActionInfo(action), apperr.New(503, "mail.location_receipt_unknown", "Receipt reconciliation could not be confirmed", nil)
	}
	action.Status = "applied"
	action.ErrorCode = ""
	return externalLocationActionInfo(action), nil
}

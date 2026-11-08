package mail

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type mutableFlagSource struct {
	base       *fakeHistorySource
	overrides  map[uint32]ExternalFlagSnapshot
	writes     int
	failAfter  bool
	failBefore bool
}

func (f *mutableFlagSource) state(uid uint32) ExternalFlagSnapshot {
	if v, ok := f.overrides[uid]; ok {
		return v
	}
	return ExternalFlagSnapshot{Seen: uid%2 == 0, Flagged: uid%3 == 0}
}
func (f *mutableFlagSource) Inspect(ctx context.Context, p, u, pw, folder string) (ExternalHistoryProbe, error) {
	return f.base.Inspect(ctx, p, u, pw, folder)
}
func (f *mutableFlagSource) ReadRaw(ctx context.Context, p, u, pw, folder string, val, uid uint32) ([]byte, error) {
	return f.base.ReadRaw(ctx, p, u, pw, folder, val, uid)
}
func (f *mutableFlagSource) ListWindow(ctx context.Context, p, u, pw, folder string, val, start, end uint32) ([]ExternalHistoryMessage, error) {
	rows, err := f.base.ListWindow(ctx, p, u, pw, folder, val, start, end)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		v := f.state(rows[i].UID)
		rows[i].Seen = v.Seen
		rows[i].Flagged = v.Flagged
	}
	return rows, nil
}
func (f *mutableFlagSource) SetFlagIfUnchanged(_ context.Context, _, _, _, _ string, val, uid uint32, flag string, expected ExternalFlagSnapshot, desired bool) error {
	f.writes++
	if f.failBefore {
		return ErrMailFlagPrecondition
	}
	if val != f.base.UIDValidity || uid == 0 || f.state(uid) != expected {
		return errors.New("conditional source conflict")
	}
	newState := expected
	switch flag {
	case "seen":
		newState.Seen = desired
	case "flagged":
		newState.Flagged = desired
	default:
		return errors.New("unsupported flag")
	}
	f.overrides[uid] = newState
	if f.failAfter {
		return errors.New("synthetic lost IMAP STORE result")
	}
	return nil
}

type mutableFlagTarget struct {
	values     map[string]ExternalFlagSnapshot
	writes     int
	failAfter  bool
	failBefore bool
	owner      uuid.UUID
}

func (f *mutableFlagTarget) ReadFlags(_ context.Context, u authctx.CurrentUser, ids []string) (map[string]ExternalFlagSnapshot, error) {
	if u.ID != f.owner {
		return nil, errors.New("not owner")
	}
	out := map[string]ExternalFlagSnapshot{}
	for _, id := range ids {
		if v, ok := f.values[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}
func (f *mutableFlagTarget) SetFlagIfUnchanged(_ context.Context, u authctx.CurrentUser, emailID, flag string, expected ExternalFlagSnapshot, desired bool) error {
	f.writes++
	if f.failBefore {
		return errors.New("synthetic JMAP precondition rejected")
	}
	v, ok := f.values[emailID]
	if !ok || u.ID != f.owner || v != expected {
		return errors.New("conditional native conflict")
	}
	switch flag {
	case "seen":
		v.Seen = desired
	case "flagged":
		v.Flagged = desired
	default:
		return errors.New("unsupported flag")
	}
	f.values[emailID] = v
	if f.failAfter {
		return errors.New("synthetic lost JMAP ack")
	}
	return nil
}

func flagSyncFixture(t *testing.T, count uint32) (*MailExternalHistoryService, *mutableFlagSource, *mutableFlagTarget, authctx.CurrentUser, authctx.CurrentUser, uuid.UUID, uuid.UUID) {
	t.Helper()
	history, base, _, alice, bob, account := historyFixture(t, count)
	start, err := history.Start(context.Background(), alice, account, "INBOX")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		report, err := RunExternalHistoryDue(context.Background(), history)
		require.NoError(t, err)
		if report.Completed > 0 {
			break
		}
	}
	src := &mutableFlagSource{base: base, overrides: map[uint32]ExternalFlagSnapshot{}}
	target := &mutableFlagTarget{owner: alice.ID, values: map[string]ExternalFlagSnapshot{}}
	for uid := uint32(1); uid <= count; uid++ {
		target.values["imported-email-"+stringUID(uid)] = src.state(uid)
	}
	history.source = src
	history.flagReader = target
	history.EnableFlagSync(src, target)
	require.NoError(t, history.external.workspace.db.AutoMigrate(&model.MailExternalFlagAttempt{}))
	return history, src, target, alice, bob, account, start.ID
}
func stringUID(uid uint32) string { return strconv.FormatUint(uint64(uid), 10) }
func forcedFlagPoll(t *testing.T, service *MailExternalHistoryService, jobID uuid.UUID) {
	t.Helper()
	require.NoError(t, service.external.workspace.db.Model(&model.MailExternalImportJob{}).Where("id=?", jobID).Update("flag_last_polled_at", nil).Error)
}

func TestTwoWayFlagSyncOptInOwnerAndSafeDirections(t *testing.T) {
	svc, source, target, alice, bob, account, jobID := flagSyncFixture(t, 3)
	ctx := context.Background()
	_, err := svc.ConfigureFlagSync(ctx, bob, account, jobID, true)
	assertAppCode(t, err, "mail.external_account_missing")
	first, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Zero(t, first.JobsClaimed)
	source.overrides[1] = ExternalFlagSnapshot{Seen: true, Flagged: false}
	target.values["imported-email-2"] = ExternalFlagSnapshot{Seen: false, Flagged: false}
	updated, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	require.True(t, updated.FlagSyncEnabled)
	report, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.JobsClaimed)
	require.Equal(t, 3, report.Checked)
	require.Equal(t, 2, report.Applied)
	require.Equal(t, 1, source.writes)
	require.Equal(t, 1, target.writes)
	require.True(t, target.values["imported-email-1"].Seen)
	require.False(t, source.state(2).Seen)
	attempts, err := svc.FlagSyncAttempts(ctx, alice, account, jobID)
	require.NoError(t, err)
	require.Len(t, attempts, 2)
	require.Equal(t, "applied", attempts[0].Status)
	forcedFlagPoll(t, svc, jobID)
	report, err = RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Zero(t, report.Applied)
	require.Equal(t, 1, source.writes)
	require.Equal(t, 1, target.writes)
	_, err = svc.ConfigureFlagSync(ctx, alice, account, jobID, false)
	require.NoError(t, err)
	forcedFlagPoll(t, svc, jobID)
	report, err = RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Zero(t, report.JobsClaimed)
}
func TestTwoWayFlagSyncUnknownNativeResultNeverRetriesAndCanReconcile(t *testing.T) {
	svc, source, target, alice, _, account, jobID := flagSyncFixture(t, 1)
	ctx := context.Background()
	source.overrides[1] = ExternalFlagSnapshot{Seen: true, Flagged: false}
	target.failAfter = true
	_, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	report, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.NeedReview)
	require.Equal(t, 1, target.writes)
	var item model.MailExternalImportItem
	require.NoError(t, svc.external.workspace.db.Where("job_id=? AND source_uid=?", jobID, 1).Take(&item).Error)
	require.NotNil(t, item.FlagMutationID)
	var attempt model.MailExternalFlagAttempt
	require.NoError(t, svc.external.workspace.db.Where("id=?", *item.FlagMutationID).Take(&attempt).Error)
	require.Equal(t, "needs_review", attempt.Status)
	var job model.MailExternalImportJob
	require.NoError(t, svc.external.workspace.db.Where("id=?", jobID).Take(&job).Error)
	require.False(t, job.FlagSyncEnabled)
	_, err = svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	assertAppCode(t, err, "mail.external_flag_reconcile_required")
	forcedFlagPoll(t, svc, jobID)
	report, err = RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Zero(t, report.JobsClaimed)
	require.Equal(t, 1, target.writes)
	receipt, err := svc.ReconcileFlagAttempt(ctx, alice, account, jobID, attempt.ID)
	require.NoError(t, err)
	require.Equal(t, "applied", receipt.Status)
	item = model.MailExternalImportItem{}
	require.NoError(t, svc.external.workspace.db.Where("id=?", attempt.ImportItemID).Take(&item).Error)
	require.Nil(t, item.FlagMutationID)
	require.True(t, item.BaselineSeen)
	_, err = svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	report, err = RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Zero(t, report.Applied)
	require.Equal(t, 1, target.writes)
}
func TestTwoWayFlagSyncUnsupportedProviderNeedsReviewAndNeverReissues(t *testing.T) {
	svc, source, target, alice, bob, account, jobID := flagSyncFixture(t, 2)
	ctx := context.Background()
	target.values["imported-email-2"] = ExternalFlagSnapshot{Seen: false, Flagged: false}
	source.failBefore = true
	_, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	report, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.Skipped)
	require.Equal(t, 0, report.NeedReview)
	require.Equal(t, 1, source.writes)
	attempts, err := svc.FlagSyncAttempts(ctx, alice, account, jobID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Equal(t, "aborted_prewrite", attempts[0].Status)
	var free model.MailExternalImportItem
	require.NoError(t, svc.external.workspace.db.Where("job_id=? AND source_uid=?", jobID, 2).Take(&free).Error)
	require.Nil(t, free.FlagMutationID)
	_, err = svc.ReconcileFlagAttempt(ctx, bob, account, jobID, attempts[0].ID)
	assertAppCode(t, err, "mail.external_account_missing")
	_, err = svc.ReconcileFlagAttempt(ctx, alice, account, jobID, attempts[0].ID)
	assertAppCode(t, err, "mail.external_flag_attempt_state")
	require.Equal(t, 1, source.writes)
}
func TestTwoWayFlagSyncUnknownLegacyBaselinesSkippedAndMissingMessagePauses(t *testing.T) {
	svc, source, target, alice, _, account, jobID := flagSyncFixture(t, 2)
	ctx := context.Background()
	require.NoError(t, svc.external.workspace.db.Model(&model.MailExternalImportItem{}).Where("job_id=? AND source_uid=?", jobID, 1).Update("flags_baseline_known", false).Error)
	delete(target.values, "imported-email-2")
	_, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	report, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.Skipped)
	require.Equal(t, 1, report.NeedReview)
	require.Zero(t, source.writes)
	require.Zero(t, target.writes)
	var job model.MailExternalImportJob
	require.NoError(t, svc.external.workspace.db.Where("id=?", jobID).Take(&job).Error)
	require.False(t, job.FlagSyncEnabled)
	require.Equal(t, "flag_identity_unavailable", job.FlagLastErrorCode)
}
func TestTwoWayFlagSyncAdvancesAgreedBaselineWithoutRemoteWrites(t *testing.T) {
	svc, source, target, alice, _, account, jobID := flagSyncFixture(t, 1)
	ctx := context.Background()
	source.overrides[1] = ExternalFlagSnapshot{Seen: true, Flagged: false}
	target.values["imported-email-1"] = ExternalFlagSnapshot{Seen: true, Flagged: false}
	_, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	report, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.BaselinesAdvanced)
	require.Zero(t, source.writes)
	require.Zero(t, target.writes)
	var item model.MailExternalImportItem
	require.NoError(t, svc.external.workspace.db.Where("job_id=?", jobID).Take(&item).Error)
	require.True(t, item.BaselineSeen)
}
func TestTwoWayFlagSyncReconcileRejectsActiveDispatchAndStaleIdentity(t *testing.T) {
	svc, source, target, alice, _, account, jobID := flagSyncFixture(t, 1)
	ctx := context.Background()
	_, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	var job model.MailExternalImportJob
	require.NoError(t, svc.external.workspace.db.Where("id=?", jobID).Take(&job).Error)
	var item model.MailExternalImportItem
	require.NoError(t, svc.external.workspace.db.Where("job_id=?", jobID).Take(&item).Error)
	record, err := svc.prepareFlagAttempt(ctx, job, item, "seen", "copy_to_native", source.state(1), target.values["imported-email-1"], true)
	require.NoError(t, err)
	_, err = svc.ReconcileFlagAttempt(ctx, alice, account, jobID, record.ID)
	assertAppCode(t, err, "mail.external_flag_attempt_state")
	_, err = svc.ConfigureFlagSync(ctx, alice, account, jobID, false)
	require.NoError(t, err)
	require.NoError(t, svc.external.workspace.db.Model(&model.MailExternalFlagAttempt{}).Where("id=?", record.ID).Update("updated_at", time.Now().Add(-21*time.Minute)).Error)
	_, err = svc.ReconcileFlagAttempt(ctx, alice, account, jobID, record.ID)
	assertAppCode(t, err, "mail.external_flag_review_required")
	var changed model.MailExternalFlagAttempt
	require.NoError(t, svc.external.workspace.db.Where("id=?", record.ID).Take(&changed).Error)
	require.Equal(t, "needs_review", changed.Status)
}

func TestTwoWayFlagSyncGenerationRejectsStaleWorkerAndDisconnectionWhileEnabled(t *testing.T) {
	svc, source, target, alice, _, account, jobID := flagSyncFixture(t, 1)
	ctx := context.Background()
	source.overrides[1] = ExternalFlagSnapshot{Seen: true, Flagged: false}
	first, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	require.True(t, first.FlagSyncEnabled)
	var stale model.MailExternalImportJob
	require.NoError(t, svc.external.workspace.db.Where("id=?", jobID).Take(&stale).Error)
	err = svc.external.Delete(ctx, alice, account)
	assertAppCode(t, err, "mail.external_flag_sync_active")
	off, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, false)
	require.NoError(t, err)
	require.Greater(t, off.FlagSyncGeneration, first.FlagSyncGeneration)
	current, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	require.Greater(t, current.FlagSyncGeneration, off.FlagSyncGeneration)
	var report ExternalFlagSyncReport
	require.NoError(t, svc.processFlagJob(ctx, alice, stale, &report))
	require.Zero(t, report.Applied)
	require.Zero(t, target.writes)
	require.Zero(t, source.writes)
	report, err = RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.Applied)
}
func TestTwoWayFlagSyncDisconnectionBlockedByUncertainReceipt(t *testing.T) {
	svc, source, target, alice, _, account, jobID := flagSyncFixture(t, 1)
	ctx := context.Background()
	source.overrides[1] = ExternalFlagSnapshot{Seen: true, Flagged: false}
	target.failAfter = true
	_, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	report, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.NeedReview)
	err = svc.external.Delete(ctx, alice, account)
	assertAppCode(t, err, "mail.external_flag_reconcile_required")
	attempts, err := svc.FlagSyncAttempts(ctx, alice, account, jobID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	_, err = svc.ReconcileFlagAttempt(ctx, alice, account, jobID, attempts[0].ID)
	require.NoError(t, err)
	// With the write confirmed and the flag job already disabled, credentials
	// can be disconnected without deleting the original provider mailbox.
	require.NoError(t, svc.external.Delete(ctx, alice, account))
}

func TestTwoWayFlagSyncSparseUIDCursorDoesNotSkipBeyondEightRecords(t *testing.T) {
	svc, source, target, alice, _, account, jobID := flagSyncFixture(t, 20)
	ctx := context.Background()
	// Sparse source identities must use monotonically increasing UID keys, not
	// an OFFSET over a changing import list.
	for _, uid := range []uint32{2, 4, 6, 8, 10, 12, 14, 16, 18, 20} {
		require.NoError(t, svc.external.workspace.db.Where("job_id=? AND source_uid=?", jobID, uid).Delete(&model.MailExternalImportItem{}).Error)
	}
	_, err := svc.ConfigureFlagSync(ctx, alice, account, jobID, true)
	require.NoError(t, err)
	first, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 8, first.Checked)
	require.Zero(t, first.Applied)
	var job model.MailExternalImportJob
	require.NoError(t, svc.external.workspace.db.Where("id=?", jobID).Take(&job).Error)
	require.Equal(t, uint32(15), job.FlagSyncCursorUID)
	forcedFlagPoll(t, svc, jobID)
	second, err := RunExternalFlagSyncDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 2, second.Checked)
	job = model.MailExternalImportJob{}
	require.NoError(t, svc.external.workspace.db.Where("id=?", jobID).Take(&job).Error)
	require.Zero(t, job.FlagSyncCursorUID)
	require.Zero(t, source.writes)
	require.Zero(t, target.writes)
}

func TestTwoWayFlagSyncRequiresOperationalGateAndFinishedImport(t *testing.T) {
	svc, src, _, alice, _, accountID := historyFixture(t, 1)
	ctx := context.Background()
	started, err := svc.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, err)
	_, err = svc.ConfigureFlagSync(ctx, alice, accountID, started.ID, true)
	assertAppCode(t, err, "mail.external_flag_sync_disabled")
	_, err = RunExternalFlagSyncDue(ctx, svc)
	assertAppCode(t, err, "mail.external_flag_sync_disabled")
	live := &mutableFlagSource{base: src, overrides: map[uint32]ExternalFlagSnapshot{}}
	native := &mutableFlagTarget{owner: alice.ID, values: map[string]ExternalFlagSnapshot{"imported-email-1": live.state(1)}}
	svc.source = live
	svc.flagReader = native
	svc.EnableFlagSync(live, native)
	require.NoError(t, svc.external.workspace.db.AutoMigrate(&model.MailExternalFlagAttempt{}))
	_, err = svc.ConfigureFlagSync(ctx, alice, accountID, started.ID, true)
	assertAppCode(t, err, "mail.external_flag_sync_not_ready")
	report, err := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.Completed)
	_, err = svc.ConfigureFlagSync(ctx, alice, accountID, started.ID, true)
	require.NoError(t, err)
}

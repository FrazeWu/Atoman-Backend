package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeHistorySource struct {
	UIDValidity uint32
	LastUID     uint32
	Changed     bool
	FailRead    bool
	LastReadUID uint32
	OversizeUID uint32
}

func (s *fakeHistorySource) Inspect(_ context.Context, _, _, _, _ string) (ExternalHistoryProbe, error) {
	if s.Changed {
		return ExternalHistoryProbe{UIDValidity: s.UIDValidity + 1, UpperUID: s.LastUID}, nil
	}
	return ExternalHistoryProbe{UIDValidity: s.UIDValidity, UpperUID: s.LastUID}, nil
}
func (s *fakeHistorySource) ListWindow(_ context.Context, _, _, _, _ string, validity, start, end uint32) ([]ExternalHistoryMessage, error) {
	if validity != s.UIDValidity {
		return nil, errors.New("UIDVALIDITY mismatch")
	}
	out := []ExternalHistoryMessage{}
	for uid := start; uid <= end; uid++ {
		if uid <= s.LastUID {
			size := int64(120)
			if uid == s.OversizeUID {
				size = maxExternalHistoryMIMEBytes + 1
			}
			out = append(out, ExternalHistoryMessage{UID: uid, Size: size, Date: time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC), Seen: uid%2 == 0, Flagged: uid%3 == 0})
		}
	}
	return out, nil
}
func (s *fakeHistorySource) ReadRaw(_ context.Context, _, _, _, _ string, validity, uid uint32) ([]byte, error) {
	s.LastReadUID = uid
	if s.FailRead || validity != s.UIDValidity {
		return nil, errors.New("Synthetic source error")
	}
	return []byte(fmt.Sprintf("From: a@example.test\r\nTo: b@example.test\r\nMessage-ID: <duplicated@example.test>\r\nSubject: source %d\r\n\r\nUnique body %d", uid, uid)), nil
}

type fakeHistoryTarget struct {
	User        uuid.UUID
	Imports     []string
	FailureAt   int
	Uncertain   bool
	AfterImport func()
}

func (t *fakeHistoryTarget) EnsureFolder(_ context.Context, u authctx.CurrentUser, name string) (string, error) {
	if u.ID != t.User || !strings.HasPrefix(name, "Imported ") {
		return "", errors.New("Not authorized")
	}
	return "history-destination-folder", nil
}
func (t *fakeHistoryTarget) ImportRaw(_ context.Context, u authctx.CurrentUser, folder string, raw []byte, _ time.Time, seen, flagged bool) (string, bool, error) {
	if u.ID != t.User || folder != "history-destination-folder" {
		return "", false, errors.New("Incorrect target")
	}
	if t.FailureAt == len(t.Imports)+1 {
		return "", t.Uncertain, errors.New("Synthetic target failure")
	}
	t.Imports = append(t.Imports, string(raw))
	if t.AfterImport != nil {
		t.AfterImport()
	}
	return fmt.Sprintf("imported-email-%d", len(t.Imports)), false, nil
}
func historyFixture(t *testing.T, count uint32) (*MailExternalHistoryService, *fakeHistorySource, *fakeHistoryTarget, authctx.CurrentUser, authctx.CurrentUser, uuid.UUID) {
	t.Helper()
	workspace, _, _, alice, bob := workspaceFixture(t, nil)
	ext := NewMailExternalService(workspace, &fakeExternalIMAP{})
	a, e := ext.Connect(context.Background(), alice, ExternalConnectInput{Provider: "gmail", Address: "alice@gmail.com", AppPassword: "own-app-password"})
	require.NoError(t, e)
	src := &fakeHistorySource{UIDValidity: 27, LastUID: count}
	target := &fakeHistoryTarget{User: alice.ID}
	history := NewMailExternalHistoryService(ext, src, target)
	require.NotNil(t, history)
	return history, src, target, alice, bob, a.ID
}
func TestExternalHistoryResumesAfterRestartAndDistinguishesSameMessageID(t *testing.T) {
	svc, src, target, alice, bob, accountID := historyFixture(t, 44)
	ctx := context.Background()
	started, e := svc.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, e)
	require.Equal(t, "queued", started.Status)
	var stored model.MailExternalImportJob
	require.NoError(t, svc.external.workspace.db.Where("id=?", started.ID).Take(&stored).Error)
	require.NotContains(t, string(stored.FolderCiphertext), "INBOX")
	_, e = svc.Start(ctx, alice, accountID, "INBOX")
	assertAppCode(t, e, "mail.external_history_exists")
	_, e = svc.List(ctx, bob, accountID)
	assertAppCode(t, e, "mail.external_account_missing")
	e = svc.external.Delete(ctx, alice, accountID)
	assertAppCode(t, e, "mail.external_import_active")
	report, e := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Equal(t, 40, report.Imported)
	require.Len(t, target.Imports, 40)
	got, e := svc.List(ctx, alice, accountID)
	require.NoError(t, e)
	require.Equal(t, uint32(41), got[0].NextUID)
	// Simulate a new process with the same persisted database and a new service.
	resumed := NewMailExternalHistoryService(svc.external, src, target)
	report, e = RunExternalHistoryDue(ctx, resumed)
	require.NoError(t, e)
	require.Equal(t, 4, report.Imported)
	require.Len(t, target.Imports, 44)
	require.Equal(t, 1, report.Completed)
	report, e = RunExternalHistoryDue(ctx, resumed)
	require.NoError(t, e)
	require.Zero(t, report.JobsClaimed)
	got, e = resumed.List(ctx, alice, accountID)
	require.NoError(t, e)
	require.Equal(t, "completed", got[0].Status)
	require.Equal(t, uint32(44), got[0].Imported)
	// Identical Message-ID headers must NOT cause distinct source UIDs to vanish.
	require.Contains(t, target.Imports[0], "duplicated@example.test")
	require.Contains(t, target.Imports[43], "duplicated@example.test")
	var imported int64
	require.NoError(t, svc.external.workspace.db.Model(&model.MailExternalImportItem{}).Where("job_id=? AND status=?", started.ID, "imported").Count(&imported).Error)
	require.Equal(t, int64(44), imported)
	_, e = resumed.Action(ctx, alice, accountID, started.ID, "resume")
	assertAppCode(t, e, "mail.external_history_state_changed")
}
func TestExternalHistoryPauseResumeAndAccountCancel(t *testing.T) {
	svc, _, target, alice, _, accountID := historyFixture(t, 42)
	ctx := context.Background()
	job, e := svc.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, e)
	paused, e := svc.Action(ctx, alice, accountID, job.ID, "pause")
	require.NoError(t, e)
	require.Equal(t, "paused", paused.Status)
	r, e := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Zero(t, r.JobsClaimed)
	require.Empty(t, target.Imports)
	_, e = svc.Action(ctx, alice, accountID, job.ID, "resume")
	require.NoError(t, e)
	r, e = RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Equal(t, 40, r.Imported)
	_, e = svc.Action(ctx, alice, accountID, job.ID, "cancel")
	require.NoError(t, e)
	_, e = svc.Action(ctx, alice, accountID, job.ID, "resume")
	assertAppCode(t, e, "mail.external_history_state_changed")
	r, e = RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Zero(t, r.JobsClaimed)
}
func TestExternalHistoryUncertainDoesNotResendAndSourceIdentityChange(t *testing.T) {
	svc, src, target, alice, _, accountID := historyFixture(t, 8)
	ctx := context.Background()
	job, e := svc.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, e)
	target.FailureAt = 2
	target.Uncertain = true
	r, e := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Equal(t, 1, r.Imported)
	require.Equal(t, 1, r.NeedReview)
	require.Len(t, target.Imports, 1)
	again, e := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Zero(t, again.JobsClaimed)
	_, e = svc.Action(ctx, alice, accountID, job.ID, "resume")
	assertAppCode(t, e, "mail.external_history_reconcile_required")
	var row model.MailExternalImportItem
	require.NoError(t, svc.external.workspace.db.Where("job_id=? AND source_uid=?", job.ID, 2).Take(&row).Error)
	require.Equal(t, "uncertain", row.Status)
	// An independently started folder with a changed UIDVALIDITY is blocked.
	job2, e := svc.Start(ctx, alice, accountID, "Sent")
	require.NoError(t, e)
	src.Changed = true
	r, e = RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Equal(t, 1, r.NeedReview)
	got, e := svc.List(ctx, alice, accountID)
	require.NoError(t, e)
	for _, j := range got {
		if j.ID == job2.ID {
			require.Equal(t, "needs_review", j.Status)
			require.Equal(t, "source_identity_changed", j.LastErrorCode)
		}
	}
}

func TestExternalHistoryRetriesOnlyBeforeImportAndSkipsOversized(t *testing.T) {
	svc, src, target, alice, _, accountID := historyFixture(t, 3)
	src.OversizeUID = 2
	ctx := context.Background()
	job, e := svc.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, e)
	target.FailureAt = 1
	target.Uncertain = false
	report, e := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Equal(t, 1, report.Failed)
	require.Empty(t, target.Imports)
	var prepared model.MailExternalImportItem
	require.NoError(t, svc.external.workspace.db.Where("job_id=? AND source_uid=?", job.ID, 1).Take(&prepared).Error)
	require.Equal(t, "prepared", prepared.Status)
	target.FailureAt = 0
	_, e = svc.Action(ctx, alice, accountID, job.ID, "resume")
	require.NoError(t, e)
	report, e = RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Equal(t, 2, report.Imported)
	require.Equal(t, 1, report.Skipped)
	require.Len(t, target.Imports, 2)
	require.Equal(t, uint32(3), src.LastReadUID) // Oversized UID 2 was never read.
	var skipped model.MailExternalImportItem
	require.NoError(t, svc.external.workspace.db.Where("job_id=? AND source_uid=?", job.ID, 2).Take(&skipped).Error)
	require.Equal(t, "skipped", skipped.Status)
	jobInfo, e := svc.List(ctx, alice, accountID)
	require.NoError(t, e)
	require.Equal(t, uint32(1), jobInfo[0].Skipped)
}
func TestExternalHistoryStaleLeaseWithInFlightItemDoesNotDuplicate(t *testing.T) {
	svc, _, target, alice, _, accountID := historyFixture(t, 3)
	ctx := context.Background()
	job, e := svc.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, e)
	old := time.Now().Add(-15 * time.Minute)
	require.NoError(t, svc.external.workspace.db.Model(&model.MailExternalImportJob{}).Where("id=?", job.ID).Updates(map[string]any{"status": "running", "claimed_at": old}).Error)
	item := model.MailExternalImportItem{ID: uuid.New(), JobID: job.ID, UserID: alice.ID, ExternalAccountID: accountID,
		FolderKey: externalHistoryFolderKey(accountID, "INBOX"), UIDValidity: job.UIDValidity, SourceUID: 1, Status: "importing"}
	require.NoError(t, svc.external.workspace.db.Create(&item).Error)
	report, e := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Equal(t, 1, report.NeedReview)
	require.Empty(t, target.Imports)
	rows, e := svc.List(ctx, alice, accountID)
	require.NoError(t, e)
	require.Equal(t, "needs_review", rows[0].Status)
	_, e = svc.Action(ctx, alice, accountID, job.ID, "resume")
	assertAppCode(t, e, "mail.external_history_reconcile_required")
}

func TestExternalHistoryPauseDuringImportRetainsReceiptAndAccurateCounts(t *testing.T) {
	svc, _, target, alice, _, accountID := historyFixture(t, 1)
	ctx := context.Background()
	job, e := svc.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, e)
	target.AfterImport = func() { _, err := svc.Action(ctx, alice, accountID, job.ID, "pause"); require.NoError(t, err) }
	report, e := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Equal(t, 1, report.Imported)
	require.Len(t, target.Imports, 1)
	info, e := svc.List(ctx, alice, accountID)
	require.NoError(t, e)
	require.Equal(t, "paused", info[0].Status)
	require.Equal(t, uint32(0), info[0].Imported)
	target.AfterImport = nil
	_, e = svc.Action(ctx, alice, accountID, job.ID, "resume")
	require.NoError(t, e)
	report, e = RunExternalHistoryDue(ctx, svc)
	require.NoError(t, e)
	require.Zero(t, report.Imported) // No second remote delivery.
	require.Len(t, target.Imports, 1)
	info, e = svc.List(ctx, alice, accountID)
	require.NoError(t, e)
	require.Equal(t, "completed", info[0].Status)
	require.Equal(t, uint32(1), info[0].Imported)
}

func TestExternalContinuousImportOnlyNewUIDsAndStopsWhenDisabled(t *testing.T) {
	service, source, target, alice, bob, accountID := historyFixture(t, 2)
	ctx := context.Background()
	job, err := service.StartWithOptions(ctx, alice, accountID, "INBOX", true)
	require.NoError(t, err)
	require.True(t, job.Continuous)
	report, err := RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 2, report.Imported)
	require.Equal(t, 1, report.Watching)
	require.Len(t, target.Imports, 2)
	list, err := service.List(ctx, alice, accountID)
	require.NoError(t, err)
	require.Equal(t, "watching", list[0].Status)
	require.NotNil(t, list[0].LastPolledAt)
	// The watch uses a cooldown and must not rescan on every worker tick.
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Zero(t, report.JobsClaimed)
	_, err = service.Action(ctx, bob, accountID, job.ID, "disable_live")
	assertAppCode(t, err, "mail.external_account_missing")
	source.LastUID = 4
	last := time.Now().UTC().Add(-6 * time.Minute)
	require.NoError(t, service.external.workspace.db.Model(&model.MailExternalImportJob{}).Where("id=?", job.ID).Update("last_polled_at", last).Error)
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 2, report.Imported)
	require.Equal(t, 1, report.Watching)
	require.Len(t, target.Imports, 4)
	list, err = service.List(ctx, alice, accountID)
	require.NoError(t, err)
	require.Equal(t, uint32(5), list[0].NextUID)
	require.Equal(t, uint32(4), list[0].UpperUID)
	require.Equal(t, uint32(4), list[0].Imported)
	stopped, err := service.Action(ctx, alice, accountID, job.ID, "disable_live")
	require.NoError(t, err)
	require.Equal(t, "completed", stopped.Status)
	require.False(t, stopped.Continuous)
	source.LastUID = 6
	require.NoError(t, service.external.workspace.db.Model(&model.MailExternalImportJob{}).Where("id=?", job.ID).Update("last_polled_at", last).Error)
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Zero(t, report.JobsClaimed)
	require.Len(t, target.Imports, 4)
	// A previously completed job is reactivated without repeating historical UIDs.
	enabled, err := service.Action(ctx, alice, accountID, job.ID, "enable_live")
	require.NoError(t, err)
	require.Equal(t, "watching", enabled.Status)
	require.True(t, enabled.Continuous)
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 2, report.Imported)
	require.Len(t, target.Imports, 6)
}

func TestExternalContinuousSourceIdentityChangeRequiresReview(t *testing.T) {
	service, source, target, alice, _, accountID := historyFixture(t, 0)
	ctx := context.Background()
	job, err := service.StartWithOptions(ctx, alice, accountID, "INBOX", true)
	require.NoError(t, err)
	report, err := RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 1, report.Watching)
	require.Empty(t, target.Imports)
	last := time.Now().UTC().Add(-6 * time.Minute)
	require.NoError(t, service.external.workspace.db.Model(&model.MailExternalImportJob{}).Where("id=?", job.ID).Update("last_polled_at", last).Error)
	source.Changed = true
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 1, report.NeedReview)
	list, err := service.List(ctx, alice, accountID)
	require.NoError(t, err)
	require.Equal(t, "needs_review", list[0].Status)
	require.Equal(t, "source_identity_changed", list[0].LastErrorCode)
	_, err = service.Action(ctx, alice, accountID, job.ID, "enable_live")
	assertAppCode(t, err, "mail.external_history_state_changed")
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Zero(t, report.JobsClaimed)
}

func TestExternalContinuousCanPauseAndResumeWithoutDuplicate(t *testing.T) {
	service, source, target, alice, _, accountID := historyFixture(t, 1)
	ctx := context.Background()
	job, err := service.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, err)
	report, err := RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 1, report.Imported)
	list, err := service.List(ctx, alice, accountID)
	require.NoError(t, err)
	require.Equal(t, "completed", list[0].Status)
	enabled, err := service.Action(ctx, alice, accountID, job.ID, "enable_live")
	require.NoError(t, err)
	require.True(t, enabled.Continuous)
	paused, err := service.Action(ctx, alice, accountID, job.ID, "pause")
	require.NoError(t, err)
	require.Equal(t, "paused", paused.Status)
	source.LastUID = 3
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Zero(t, report.JobsClaimed)
	_, err = service.Action(ctx, alice, accountID, job.ID, "resume")
	require.NoError(t, err)
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 0, report.Imported) // source growth isn't polled by the backfill resume
	// Once a completed backfill becomes a watcher, it may fetch the new high-water UID.
	list, err = service.List(ctx, alice, accountID)
	require.NoError(t, err)
	require.Equal(t, "watching", list[0].Status)
	require.NoError(t, service.external.workspace.db.Model(&model.MailExternalImportJob{}).Where("id=?", job.ID).Update("last_polled_at", time.Now().UTC().Add(-6*time.Minute)).Error)
	report, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 2, report.Imported)
	require.Len(t, target.Imports, 3)
}

func TestExternalContinuousDisableDuringInFlightImportPausesSafely(t *testing.T) {
	svc, src, target, alice, _, accountID := historyFixture(t, 1)
	ctx := context.Background()
	job, err := svc.StartWithOptions(ctx, alice, accountID, "INBOX", true)
	require.NoError(t, err)
	target.AfterImport = func() {
		disabled, changeErr := svc.Action(ctx, alice, accountID, job.ID, "disable_live")
		require.NoError(t, changeErr)
		require.False(t, disabled.Continuous)
		require.Equal(t, "paused", disabled.Status)
	}
	report, err := RunExternalHistoryDue(ctx, svc)
	require.NoError(t, err)
	require.Equal(t, 1, report.Imported)
	require.Len(t, target.Imports, 1)
	target.AfterImport = nil
	_, err = svc.Action(ctx, alice, accountID, job.ID, "resume")
	require.NoError(t, err)
	report, err = RunExternalHistoryDue(ctx, svc)
	require.NoError(t, err)
	require.Zero(t, report.Imported)
	require.Equal(t, 1, report.Completed)
	require.Len(t, target.Imports, 1)
	src.LastUID = 3
	report, err = RunExternalHistoryDue(ctx, svc)
	require.NoError(t, err)
	require.Zero(t, report.JobsClaimed)
	require.Len(t, target.Imports, 1)
}

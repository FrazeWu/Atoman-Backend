package mail

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type locationTestSource struct {
	base    *fakeHistorySource
	missing map[string]map[uint32]bool
}

func (f *locationTestSource) Inspect(ctx context.Context, p, u, pw, folder string) (ExternalHistoryProbe, error) {
	return f.base.Inspect(ctx, p, u, pw, folder)
}
func (f *locationTestSource) ReadRaw(ctx context.Context, p, u, pw, folder string, validity, uid uint32) ([]byte, error) {
	return f.base.ReadRaw(ctx, p, u, pw, folder, validity, uid)
}
func (f *locationTestSource) ListWindow(ctx context.Context, p, u, pw, folder string, validity, start, end uint32) ([]ExternalHistoryMessage, error) {
	rows, err := f.base.ListWindow(ctx, p, u, pw, folder, validity, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]ExternalHistoryMessage, 0, len(rows))
	for _, m := range rows {
		if !f.missing[folder][m.UID] {
			out = append(out, m)
		}
	}
	return out, nil
}

type fakeMailNativeLocation struct {
	items      map[string]MailNativeLocationSnapshot
	folders    map[string]string
	writes     int
	failBefore bool
	failAfter  bool
	revision   int
	owner      uuid.UUID
}

func (f *fakeMailNativeLocation) ReadLocation(_ context.Context, u authctx.CurrentUser, id string) (MailNativeLocationSnapshot, error) {
	if u.ID != f.owner {
		return MailNativeLocationSnapshot{}, errors.New("not owner")
	}
	row, ok := f.items[id]
	if !ok {
		return MailNativeLocationSnapshot{}, ErrMailNativeMessageMissing
	}
	return MailNativeLocationSnapshot{MailboxIDs: copyFlags(row.MailboxIDs), State: row.State}, nil
}
func (f *fakeMailNativeLocation) TrashMailbox(_ context.Context, u authctx.CurrentUser) (string, error) {
	if u.ID != f.owner {
		return "", errors.New("not owner")
	}
	return "fake-native-trash", nil
}
func (f *fakeMailNativeLocation) FindImportedFolder(_ context.Context, u authctx.CurrentUser, name string) (string, error) {
	if u.ID != f.owner {
		return "", errors.New("not owner")
	}
	v := f.folders[name]
	if v == "" {
		return "", errors.New("original imported folder missing")
	}
	return v, nil
}
func (f *fakeMailNativeLocation) ValidateMailboxes(_ context.Context, u authctx.CurrentUser, ids map[string]bool) error {
	if u.ID != f.owner || len(ids) == 0 {
		return errors.New("forbidden")
	}
	allowed := map[string]bool{"fake-native-trash": true}
	for _, id := range f.folders {
		allowed[id] = true
	}
	for id, yes := range ids {
		if !yes || !allowed[id] {
			return errors.New("foreign mailbox")
		}
	}
	return nil
}
func (f *fakeMailNativeLocation) MoveLocation(ctx context.Context, u authctx.CurrentUser, emailID string, expected MailNativeLocationSnapshot, target map[string]bool) error {
	f.writes++
	if f.failBefore {
		return ErrMailFlagPrecondition
	}
	if e := f.ValidateMailboxes(ctx, u, target); e != nil {
		return e
	}
	before, err := f.ReadLocation(ctx, u, emailID)
	if err != nil || !reflect.DeepEqual(before, expected) {
		return ErrMailFlagPrecondition
	}
	f.revision++
	f.items[emailID] = MailNativeLocationSnapshot{MailboxIDs: copyFlags(target), State: fmt.Sprintf("state-%d", f.revision)}
	if f.failAfter {
		return errors.New("lost JMAP Email/set response")
	}
	return nil
}
func locationFixture(t *testing.T, count uint32) (*MailExternalLocationService, *locationTestSource, *fakeMailNativeLocation, authctx.CurrentUser, authctx.CurrentUser, uuid.UUID, uuid.UUID) {
	t.Helper()
	history, base, _, alice, bob, accountID := historyFixture(t, count)
	source := &locationTestSource{base: base, missing: map[string]map[uint32]bool{}}
	history.source = source
	job, err := history.Start(context.Background(), alice, accountID, "INBOX")
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		rep, e := RunExternalHistoryDue(context.Background(), history)
		require.NoError(t, e)
		if rep.Completed > 0 {
			break
		}
	}
	var complete model.MailExternalImportJob
	require.NoError(t, history.external.workspace.db.Where("id=?", job.ID).Take(&complete).Error)
	require.Equal(t, "completed", complete.Status)
	native := &fakeMailNativeLocation{owner: alice.ID, items: map[string]MailNativeLocationSnapshot{}, folders: map[string]string{}}
	name := externalHistoryFolderName("gmail", complete.FolderKey, "INBOX")
	native.folders[name] = "fake-import-inbox"
	for i := uint32(1); i <= count; i++ {
		native.items[fmt.Sprintf("imported-email-%d", i)] = MailNativeLocationSnapshot{MailboxIDs: map[string]bool{"fake-import-inbox": true}, State: "state-0"}
	}
	service := NewMailExternalLocationService(history, native)
	require.NotNil(t, service)
	require.NoError(t, service.ready())
	return service, source, native, alice, bob, accountID, job.ID
}
func locationSecondObservation(t *testing.T, s *MailExternalLocationService, u authctx.CurrentUser, accountID, jobID uuid.UUID, uid uint32) MailLocationCaseInfo {
	t.Helper()
	page, err := s.Scan(context.Background(), u, accountID, jobID, 0)
	require.NoError(t, err)
	var found MailLocationCaseInfo
	for _, row := range page.Items {
		if row.SourceUID == uid {
			found = row
		}
	}
	require.NotEqual(t, uuid.Nil, found.ID)
	require.NoError(t, s.history.external.workspace.db.Model(&model.MailExternalLocationCase{}).Where("id=?", found.ID).Update("last_observed_at", time.Now().Add(-6*time.Minute)).Error)
	page, err = s.Scan(context.Background(), u, accountID, jobID, 0)
	require.NoError(t, err)
	for _, row := range page.Items {
		if row.SourceUID == uid {
			return row
		}
	}
	t.Fatal("case not present in second scan")
	return MailLocationCaseInfo{}
}
func TestLocationScanTwoObservationsNotAutomaticallyDeletedAndOwnerScoped(t *testing.T) {
	s, src, native, alice, bob, account, job := locationFixture(t, 2)
	src.missing["INBOX"] = map[uint32]bool{1: true}
	ctx := context.Background()
	_, err := s.Scan(ctx, bob, account, job, 0)
	assertAppCode(t, err, "mail.external_account_missing")
	first, err := s.Scan(ctx, alice, account, job, 0)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.Equal(t, "observed", first.Items[0].Status)
	require.Equal(t, uint32(1), first.Items[0].MissingObservations)
	second, err := s.Scan(ctx, alice, account, job, 0)
	require.NoError(t, err)
	require.Equal(t, uint32(1), second.Items[0].MissingObservations)
	require.Zero(t, native.writes)
	require.NoError(t, s.history.external.workspace.db.Model(&model.MailExternalLocationCase{}).Where("id=?", first.Items[0].ID).Update("last_observed_at", time.Now().Add(-6*time.Minute)).Error)
	matured, err := s.Scan(ctx, alice, account, job, 0)
	require.NoError(t, err)
	require.Equal(t, "missing_unclassified", matured.Items[0].Status)
	require.Equal(t, uint32(2), matured.Items[0].MissingObservations)
	require.Zero(t, native.writes)
	list, err := s.Cases(ctx, alice, account, job)
	require.NoError(t, err)
	require.Len(t, list, 1)
	kept, err := s.Resolve(ctx, alice, account, job, list[0].ID, "keep_native")
	require.NoError(t, err)
	require.Equal(t, "kept", kept.Status)
	require.Zero(t, native.writes)
	_, err = s.Resolve(ctx, alice, account, job, list[0].ID, "trash_native")
	assertAppCode(t, err, "mail.location_state_changed")
}
func TestLocationNativeTrashAndRestoreExactOriginalFolder(t *testing.T) {
	s, src, native, alice, _, account, job := locationFixture(t, 1)
	src.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, job, 1)
	require.Equal(t, "missing_unclassified", c.Status)
	trashed, err := s.Resolve(context.Background(), alice, account, job, c.ID, "trash_native")
	require.NoError(t, err)
	require.Equal(t, "trashed", trashed.Status)
	require.Equal(t, map[string]bool{"fake-native-trash": true}, native.items["imported-email-1"].MailboxIDs)
	attempts, err := s.Actions(context.Background(), alice, account, job, c.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Equal(t, "applied", attempts[0].Status)
	restored, err := s.Resolve(context.Background(), alice, account, job, c.ID, "restore_native")
	require.NoError(t, err)
	require.Equal(t, "restored", restored.Status)
	require.Equal(t, map[string]bool{"fake-import-inbox": true}, native.items["imported-email-1"].MailboxIDs)
	require.Equal(t, 2, native.writes)
	_, err = s.Resolve(context.Background(), alice, account, job, c.ID, "restore_native")
	assertAppCode(t, err, "mail.location_state_changed")
}
func TestLocationLostJMAPReplyNeverRetriesAndCanReconcile(t *testing.T) {
	s, src, native, alice, bob, account, job := locationFixture(t, 1)
	src.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, job, 1)
	native.failAfter = true
	_, err := s.Resolve(context.Background(), alice, account, job, c.ID, "trash_native")
	assertAppCode(t, err, "mail.location_move_unconfirmed")
	require.Equal(t, 1, native.writes)
	_, err = s.Resolve(context.Background(), alice, account, job, c.ID, "trash_native")
	assertAppCode(t, err, "mail.location_state_changed")
	var stored model.MailExternalLocationCase
	require.NoError(t, s.history.external.workspace.db.Where("id=?", c.ID).Take(&stored).Error)
	require.Equal(t, "action_needs_review", stored.Status)
	require.NotNil(t, stored.ActionID)
	_, err = s.Reconcile(context.Background(), bob, account, job, c.ID, *stored.ActionID)
	assertAppCode(t, err, "mail.external_account_missing")
	approved, err := s.Reconcile(context.Background(), alice, account, job, c.ID, *stored.ActionID)
	require.NoError(t, err)
	require.Equal(t, "applied", approved.Status)
	require.NoError(t, s.history.external.workspace.db.Where("id=?", c.ID).Take(&stored).Error)
	// Reload into a fresh struct so GORM does not preserve a stale pointer.
	stored = model.MailExternalLocationCase{}
	require.NoError(t, s.history.external.workspace.db.Where("id=?", c.ID).Take(&stored).Error)
	require.Nil(t, stored.ActionID)
	native.failAfter = false
	_, err = s.Resolve(context.Background(), alice, account, job, c.ID, "restore_native")
	require.NoError(t, err)
	require.Equal(t, 2, native.writes)
}
func TestLocationPrewriteRejectReleasesOnlyUnsentIntent(t *testing.T) {
	s, src, native, alice, _, account, job := locationFixture(t, 1)
	src.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, job, 1)
	native.failBefore = true
	_, err := s.Resolve(context.Background(), alice, account, job, c.ID, "trash_native")
	assertAppCode(t, err, "mail.location_move_unconfirmed")
	var stored model.MailExternalLocationCase
	require.NoError(t, s.history.external.workspace.db.Where("id=?", c.ID).Take(&stored).Error)
	require.Nil(t, stored.ActionID)
	actions, err := s.Actions(context.Background(), alice, account, job, c.ID)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	require.Equal(t, "aborted_prewrite", actions[0].Status)
	require.Equal(t, map[string]bool{"fake-import-inbox": true}, native.items["imported-email-1"].MailboxIDs)
}
func TestLocationSourceReturnsOrNativeMovedBlocksTrash(t *testing.T) {
	s, src, native, alice, _, account, job := locationFixture(t, 1)
	src.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, job, 1)
	delete(src.missing["INBOX"], 1)
	_, err := s.Resolve(context.Background(), alice, account, job, c.ID, "trash_native")
	assertAppCode(t, err, "mail.location_source_returned")
	require.Zero(t, native.writes)
	src.missing["INBOX"][1] = true
	native.items["imported-email-1"] = MailNativeLocationSnapshot{MailboxIDs: map[string]bool{"fake-native-trash": true}, State: "state-user-changed"}
	_, err = s.Resolve(context.Background(), alice, account, job, c.ID, "trash_native")
	assertAppCode(t, err, "mail.location_native_changed")
	require.Zero(t, native.writes)
}
func TestLocationCandidateFromOtherFolderOnlyProposesPossibleMove(t *testing.T) {
	s, src, native, alice, _, account, original := locationFixture(t, 1)
	next, err := s.history.Start(context.Background(), alice, account, "Archive")
	require.NoError(t, err)
	report, err := RunExternalHistoryDue(context.Background(), s.history)
	require.NoError(t, err)
	require.Equal(t, 1, report.Imported)
	var dest model.MailExternalImportJob
	require.NoError(t, s.history.external.workspace.db.Where("id=?", next.ID).Take(&dest).Error)
	native.folders[externalHistoryFolderName("gmail", dest.FolderKey, "Archive")] = "fake-import-archive"
	native.items["imported-email-2"] = MailNativeLocationSnapshot{MailboxIDs: map[string]bool{"fake-import-archive": true}, State: "state-0"}
	src.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, original, 1)
	require.Equal(t, "possible_move", c.Status)
	require.Equal(t, "Archive", c.CandidateFolder)
	require.Zero(t, native.writes)
	confirmed, err := s.Resolve(context.Background(), alice, account, original, c.ID, "resolve_move")
	require.NoError(t, err)
	require.Equal(t, "moved_resolved", confirmed.Status)
	require.Equal(t, map[string]bool{"fake-native-trash": true}, native.items["imported-email-1"].MailboxIDs)
	require.Equal(t, map[string]bool{"fake-import-archive": true}, native.items["imported-email-2"].MailboxIDs)
	require.Equal(t, 1, native.writes)
	_, err = s.Resolve(context.Background(), alice, account, original, c.ID, "restore_native")
	require.NoError(t, err)
	require.Equal(t, 2, native.writes)
}
func TestLocationMultipleIdenticalCandidatesNeverAutoResolveMove(t *testing.T) {
	s, src, native, alice, _, account, original := locationFixture(t, 1)
	for i, folder := range []string{"Archive", "Sent"} {
		job, err := s.history.Start(context.Background(), alice, account, folder)
		require.NoError(t, err)
		_, err = RunExternalHistoryDue(context.Background(), s.history)
		require.NoError(t, err)
		var destination model.MailExternalImportJob
		require.NoError(t, s.history.external.workspace.db.Where("id=?", job.ID).Take(&destination).Error)
		id := fmt.Sprintf("fake-folder-%d", i)
		native.folders[externalHistoryFolderName("gmail", destination.FolderKey, folder)] = id
		native.items[fmt.Sprintf("imported-email-%d", i+2)] = MailNativeLocationSnapshot{MailboxIDs: map[string]bool{id: true}, State: "state-0"}
	}
	src.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, original, 1)
	require.Equal(t, "ambiguous_candidates", c.Status)
	require.Empty(t, c.CandidateFolder)
	_, err := s.Resolve(context.Background(), alice, account, original, c.ID, "resolve_move")
	assertAppCode(t, err, "mail.location_state_changed")
	require.Zero(t, native.writes)
}

func TestLocationPendingMoveBlocksFlagSyncAndAccountDisconnect(t *testing.T) {
	s, source, native, alice, _, account, job := locationFixture(t, 1)
	source.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, job, 1)
	native.failAfter = true
	_, err := s.Resolve(context.Background(), alice, account, job, c.ID, "trash_native")
	assertAppCode(t, err, "mail.location_move_unconfirmed")
	var item model.MailExternalImportItem
	require.NoError(t, s.history.external.workspace.db.Where("job_id=? AND source_uid=?", job, 1).Take(&item).Error)
	require.NotNil(t, item.LocationMutationID)
	s.history.EnableFlagSync(&mutableFlagSource{base: source.base, overrides: map[uint32]ExternalFlagSnapshot{}}, &mutableFlagTarget{owner: alice.ID, values: map[string]ExternalFlagSnapshot{}})
	_, err = s.history.ConfigureFlagSync(context.Background(), alice, account, job, true)
	assertAppCode(t, err, "mail.external_flag_reconcile_required")
	err = s.history.external.Delete(context.Background(), alice, account)
	assertAppCode(t, err, "mail.external_flag_reconcile_required")
	var attempt model.MailExternalLocationAction
	require.NoError(t, s.history.external.workspace.db.Where("case_id=?", c.ID).Take(&attempt).Error)
	_, err = s.Reconcile(context.Background(), alice, account, job, c.ID, attempt.ID)
	require.NoError(t, err)
	item = model.MailExternalImportItem{}
	require.NoError(t, s.history.external.workspace.db.Where("job_id=? AND source_uid=?", job, 1).Take(&item).Error)
	require.Nil(t, item.LocationMutationID)
	_, err = s.history.ConfigureFlagSync(context.Background(), alice, account, job, true)
	require.NoError(t, err)
	report, err := RunExternalFlagSyncDue(context.Background(), s.history)
	require.NoError(t, err)
	require.Equal(t, 1, report.Skipped)
	require.Zero(t, report.Applied)
	require.Zero(t, report.NeedReview)
}
func TestLocationKeepCaseIsExcludedFromAutomaticFlagMutations(t *testing.T) {
	s, source, _, alice, _, account, job := locationFixture(t, 1)
	source.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, job, 1)
	_, err := s.Resolve(context.Background(), alice, account, job, c.ID, "keep_native")
	require.NoError(t, err)
	target := &mutableFlagTarget{owner: alice.ID, values: map[string]ExternalFlagSnapshot{"imported-email-1": {Seen: false, Flagged: false}}}
	srcWriter := &mutableFlagSource{base: source.base, overrides: map[uint32]ExternalFlagSnapshot{}}
	s.history.EnableFlagSync(srcWriter, target)
	_, err = s.history.ConfigureFlagSync(context.Background(), alice, account, job, true)
	require.NoError(t, err)
	report, err := RunExternalFlagSyncDue(context.Background(), s.history)
	require.NoError(t, err)
	require.Equal(t, 1, report.Skipped)
	require.Zero(t, report.Applied)
	require.Zero(t, report.NeedReview)
	require.Zero(t, srcWriter.writes)
	require.Zero(t, target.writes)
}

func TestLocationReappearanceAuditDoesNotUndoConfirmedNativeTrash(t *testing.T) {
	s, src, native, alice, _, account, job := locationFixture(t, 1)
	src.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, s, alice, account, job, 1)
	trashed, err := s.Resolve(context.Background(), alice, account, job, c.ID, "trash_native")
	require.NoError(t, err)
	require.Equal(t, "trashed", trashed.Status)
	delete(src.missing["INBOX"], 1)
	_, err = s.Scan(context.Background(), alice, account, job, 0)
	require.NoError(t, err)
	cases, err := s.Cases(context.Background(), alice, account, job)
	require.NoError(t, err)
	require.Len(t, cases, 1)
	require.Equal(t, "trashed", cases[0].Status)
	require.NotNil(t, cases[0].ReturnedAt)
	require.Equal(t, 1, native.writes)
	_, err = s.Resolve(context.Background(), alice, account, job, c.ID, "restore_native")
	require.NoError(t, err)
	require.Equal(t, map[string]bool{"fake-import-inbox": true}, native.items["imported-email-1"].MailboxIDs)
}

func TestLocationKeepNativeRejectsDeletedCopyOrActiveFlagSync(t *testing.T) {
	svc, source, native, alice, _, account, jobID := locationFixture(t, 1)
	source.missing["INBOX"] = map[uint32]bool{1: true}
	c := locationSecondObservation(t, svc, alice, account, jobID, 1)
	delete(native.items, "imported-email-1")
	_, err := svc.Resolve(context.Background(), alice, account, jobID, c.ID, "keep_native")
	assertAppCode(t, err, "mail.location_native_missing")
	native.items["imported-email-1"] = MailNativeLocationSnapshot{MailboxIDs: map[string]bool{"fake-import-inbox": true}, State: "restored-state"}
	svc.history.EnableFlagSync(&mutableFlagSource{base: source.base, overrides: map[uint32]ExternalFlagSnapshot{}}, &mutableFlagTarget{owner: alice.ID, values: map[string]ExternalFlagSnapshot{}})
	_, err = svc.history.ConfigureFlagSync(context.Background(), alice, account, jobID, true)
	require.NoError(t, err)
	_, err = svc.Resolve(context.Background(), alice, account, jobID, c.ID, "keep_native")
	assertAppCode(t, err, "mail.location_flag_sync_active")
	_, err = svc.history.ConfigureFlagSync(context.Background(), alice, account, jobID, false)
	require.NoError(t, err)
	kept, err := svc.Resolve(context.Background(), alice, account, jobID, c.ID, "keep_native")
	require.NoError(t, err)
	require.Equal(t, "kept", kept.Status)
	require.Zero(t, native.writes)
}

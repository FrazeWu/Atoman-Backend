package mail

import (
	"context"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type fakeFlagTarget struct {
	State    map[string]ExternalFlagSnapshot
	Calls    int
	LastUser uuid.UUID
	Fail     bool
}

func (t *fakeFlagTarget) ReadFlags(_ context.Context, u authctx.CurrentUser, ids []string) (map[string]ExternalFlagSnapshot, error) {
	t.Calls++
	t.LastUser = u.ID
	results := map[string]ExternalFlagSnapshot{}
	for _, id := range ids {
		if value, ok := t.State[id]; ok {
			results[id] = value
		}
	}
	return results, nil
}

type changedHistorySource struct {
	base       *fakeHistorySource
	Seen       map[uint32]bool
	Flagged    map[uint32]bool
	MissingUID uint32
}

func (s changedHistorySource) Inspect(ctx context.Context, p, u, pw, f string) (ExternalHistoryProbe, error) {
	return s.base.Inspect(ctx, p, u, pw, f)
}
func (s changedHistorySource) ReadRaw(ctx context.Context, p, u, pw, f string, val, uid uint32) ([]byte, error) {
	return s.base.ReadRaw(ctx, p, u, pw, f, val, uid)
}
func (s changedHistorySource) ListWindow(ctx context.Context, p, u, pw, f string, val, start, end uint32) ([]ExternalHistoryMessage, error) {
	rows, err := s.base.ListWindow(ctx, p, u, pw, f, val, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]ExternalHistoryMessage, 0, len(rows))
	for _, m := range rows {
		if m.UID == s.MissingUID {
			continue
		}
		if value, ok := s.Seen[m.UID]; ok {
			m.Seen = value
		}
		if value, ok := s.Flagged[m.UID]; ok {
			m.Flagged = value
		}
		out = append(out, m)
	}
	return out, nil
}
func TestMailFlagPlannerOnlyRecommendsOneChangedSide(t *testing.T) {
	checks := []struct {
		base, source, native bool
		result               string
	}{
		{false, false, false, "in_sync"},
		{true, true, true, "in_sync"},
		{false, true, false, "copy_to_native"},
		{true, false, true, "copy_to_native"},
		{false, false, true, "copy_to_source"},
		{true, true, false, "copy_to_source"},
		{false, true, true, "in_sync"},
		{true, false, false, "in_sync"},
	}
	for _, test := range checks {
		result := chooseExternalFlagPlan(test.base, test.source, test.native)
		require.Equal(t, test.result, result.Decision)
	}
}
func TestExternalHistoryFlagPreviewOwnerScopedReadOnlyAndMissingStates(t *testing.T) {
	service, src, _, alice, bob, accountID := historyFixture(t, 3)
	ctx := context.Background()
	job, err := service.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, err)
	report, err := RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	require.Equal(t, 3, report.Imported)
	native := &fakeFlagTarget{State: map[string]ExternalFlagSnapshot{
		"imported-email-1": {Seen: false, Flagged: false},
		"imported-email-2": {Seen: false, Flagged: false},
		"imported-email-3": {Seen: false, Flagged: true},
	}}
	service.flagReader = native
	service.source = changedHistorySource{base: src, Seen: map[uint32]bool{1: true}}
	page, err := service.FlagPreview(ctx, alice, accountID, job.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "INBOX", page.Folder)
	require.Len(t, page.Items, 3)
	require.Equal(t, "ready", page.Items[0].Status)
	require.Equal(t, "copy_to_native", page.Items[0].Seen.Decision)
	require.Equal(t, "copy_to_source", page.Items[1].Seen.Decision)
	require.Equal(t, "in_sync", page.Items[2].Flagged.Decision)
	require.Equal(t, 1, native.Calls)
	require.Equal(t, alice.ID, native.LastUser)
	require.False(t, page.HasMore)
	require.Equal(t, 3, page.NextPosition)
	_, err = service.FlagPreview(ctx, bob, accountID, job.ID, 0)
	assertAppCode(t, err, "mail.external_account_missing")
	_, err = service.FlagPreview(ctx, alice, accountID, uuid.New(), 0)
	assertAppCode(t, err, "mail.external_history_missing")
	_, err = service.FlagPreview(ctx, alice, accountID, job.ID, -1)
	assertAppCode(t, err, "mail.external_flag_position")
	// Source message disappeared: never guess that the user requested deletion.
	service.source = changedHistorySource{base: src, MissingUID: 1}
	page, err = service.FlagPreview(ctx, alice, accountID, job.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "source_missing", page.Items[0].Status)
	// Native message disappeared: preserve an explicit review status.
	delete(native.State, "imported-email-2")
	page, err = service.FlagPreview(ctx, alice, accountID, job.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "native_missing", page.Items[1].Status)
	var count int64
	require.NoError(t, service.external.workspace.db.Model(&model.MailExternalImportItem{}).Where("job_id=?", job.ID).Count(&count).Error)
	require.Equal(t, int64(3), count)
}
func TestExternalHistoryFlagPreviewLegacyBaselineUnknownDoesNotGuess(t *testing.T) {
	service, src, _, alice, _, accountID := historyFixture(t, 1)
	ctx := context.Background()
	job, err := service.Start(ctx, alice, accountID, "INBOX")
	require.NoError(t, err)
	_, err = RunExternalHistoryDue(ctx, service)
	require.NoError(t, err)
	service.flagReader = &fakeFlagTarget{State: map[string]ExternalFlagSnapshot{"imported-email-1": {Seen: true, Flagged: true}}}
	require.NoError(t, service.external.workspace.db.Model(&model.MailExternalImportItem{}).Where("job_id=?", job.ID).Update("flags_baseline_known", false).Error)
	page, err := service.FlagPreview(ctx, alice, accountID, job.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "baseline_unknown", page.Items[0].Status)
	src.Changed = true
	_, err = service.FlagPreview(ctx, alice, accountID, job.ID, 0)
	assertAppCode(t, err, "mail.external_flag_identity_changed")
}

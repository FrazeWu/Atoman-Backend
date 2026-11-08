package mail

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func userForOrganizer(id uuid.UUID) authctx.CurrentUser { return authctx.CurrentUser{ID: id} }
func organizerFixture(t *testing.T) (*MailWorkspaceService, context.Context, uuid.UUID, uuid.UUID) {
	t.Helper()
	respond := func(method string, args map[string]any) any {
		if method != "Email/get" {
			t.Errorf("unexpected JMAP method %s", method)
			return map[string]any{}
		}
		id := args["ids"].([]any)[0].(string)
		return map[string]any{"list": []any{map[string]any{
			"id": id, "mailboxIds": map[string]any{"mail-inbox": true}, "keywords": map[string]any{},
		}}, "state": "state1", "notFound": []string{}}
	}
	s, _, _, alice, bob := workspaceFixture(t, respond)
	return s, context.Background(), alice.ID, bob.ID
}
func TestMailOrganizerPrivateLabelsAndSnooze(t *testing.T) {
	s, ctx, aid, bid := organizerFixture(t)
	alice, bob := userForOrganizer(aid), userForOrganizer(bid)
	label, e := s.CreateOrganizerLabel(ctx, alice, OrganizerLabelInput{Name: "  重要  ", Color: "blue"})
	require.NoError(t, e)
	require.Equal(t, "重要", label.Name)
	require.Equal(t, uint64(1), label.Revision)
	var db model.MailOrganizerLabel
	require.NoError(t, s.db.Where("id=?", label.ID).Take(&db).Error)
	require.NotContains(t, string(db.Ciphertext), "重要")
	_, e = s.CreateOrganizerLabel(ctx, alice, OrganizerLabelInput{Name: "重要", Color: "blue"})
	assertAppCode(t, e, "mail.organizer_duplicate_label")
	_, e = s.CreateOrganizerLabel(ctx, alice, OrganizerLabelInput{Name: "Bad", Color: "<script>"})
	assertAppCode(t, e, "mail.organizer_invalid_color")
	state, e := s.MailOrganizer(ctx, bob)
	require.NoError(t, e)
	require.Empty(t, state.Labels)
	updated, e := s.UpdateOrganizerLabel(ctx, bob, label.ID, OrganizerLabelInput{Name: "bad", Color: "blue", Revision: 1})
	require.Error(t, e)
	require.Equal(t, uuid.Nil, updated.ID)
	due := time.Now().Add(24 * time.Hour).UTC()
	record, e := s.MutateOrganizer(ctx, alice, OrganizerActionInput{MessageID: "mail-owned-1", Action: "snooze", DueAt: &due})
	require.NoError(t, e)
	require.Equal(t, uint64(1), record.Revision)
	require.WithinDuration(t, due, *record.SnoozeUntil, time.Second)
	require.Empty(t, record.LabelIDs)
	record, e = s.MutateOrganizer(ctx, alice, OrganizerActionInput{MessageID: "mail-owned-1", Action: "label_add", LabelID: label.ID, Revision: 1})
	require.NoError(t, e)
	require.Equal(t, uint64(2), record.Revision)
	require.Equal(t, []uuid.UUID{label.ID}, record.LabelIDs)
	record, e = s.MutateOrganizer(ctx, alice, OrganizerActionInput{MessageID: "mail-owned-1", Action: "label_add", LabelID: label.ID, Revision: 2})
	require.NoError(t, e)
	require.Equal(t, uint64(2), record.Revision, "duplicate label add must be idempotent")
	_, e = s.MutateOrganizer(ctx, alice, OrganizerActionInput{MessageID: "mail-owned-1", Action: "clear_follow_up", Revision: 1})
	assertAppCode(t, e, "mail.organizer_conflict")
	_, e = s.MutateOrganizer(ctx, bob, OrganizerActionInput{MessageID: "mail-owned-1", Action: "label_add", LabelID: label.ID})
	assertAppCode(t, e, "mail.organizer_label_missing")
	follow := time.Now().Add(2 * time.Hour).UTC()
	record, e = s.MutateOrganizer(ctx, alice, OrganizerActionInput{MessageID: "mail-owned-1", Action: "follow_up", DueAt: &follow, Revision: 2})
	require.NoError(t, e)
	require.Equal(t, uint64(3), record.Revision)
	require.WithinDuration(t, follow, *record.FollowUpAt, time.Second)
	state, e = s.MailOrganizer(ctx, alice)
	require.NoError(t, e)
	require.Len(t, state.Records, 1)
	var row model.MailOrganizerRecord
	require.NoError(t, s.db.Where("user_id=? AND email_id=?", alice.ID, "mail-owned-1").Take(&row).Error)
	plain, _ := json.Marshal(record)
	require.NotContains(t, string(row.Ciphertext), string(plain))
	require.NotContains(t, string(row.Ciphertext), "follow_up_at")
	require.NoError(t, s.DeleteOrganizerLabel(ctx, alice, label.ID))
	state, e = s.MailOrganizer(ctx, alice)
	require.NoError(t, e)
	require.Empty(t, state.Labels)
	require.Empty(t, state.Records[0].LabelIDs, "deleted labels must not be exposed")
	record, e = s.MutateOrganizer(ctx, alice, OrganizerActionInput{MessageID: "mail-owned-1", Action: "unsnooze", Revision: 3})
	require.NoError(t, e)
	require.Nil(t, record.SnoozeUntil)
	record, e = s.MutateOrganizer(ctx, alice, OrganizerActionInput{MessageID: "mail-owned-1", Action: "clear_follow_up", Revision: 4})
	require.NoError(t, e)
	require.Nil(t, record.FollowUpAt)
}

func TestMailOrganizerRejectsForgedMessagesAndOutOfBoundsTimes(t *testing.T) {
	s, ctx, aid, _ := organizerFixture(t)
	u := userForOrganizer(aid)
	short := time.Now().Add(2 * time.Minute).UTC()
	far := time.Now().Add(180 * 24 * time.Hour).UTC()
	for _, in := range []OrganizerActionInput{
		{MessageID: "mail-ok", Action: "snooze", DueAt: &short},
		{MessageID: "mail-ok", Action: "follow_up", DueAt: &far},
		{MessageID: "mail-ok", Action: "send_me_a_secret", DueAt: &far},
		{MessageID: "\r\n", Action: "unsnooze"},
	} {
		_, err := s.MutateOrganizer(ctx, u, in)
		require.Error(t, err)
	}
	for _, v := range []OrganizerLabelInput{
		{Name: "", Color: "red"}, {Name: "oops\ninject", Color: "red"},
		{Name: "hello", Color: "green<script>"},
	} {
		_, err := s.CreateOrganizerLabel(ctx, u, v)
		require.Error(t, err)
	}
}

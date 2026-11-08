package mail

import (
	"context"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func followUpFixture(t *testing.T) (*MailWorkspaceService, context.Context, uuid.UUID, uuid.UUID) {
	t.Helper()
	s, ctx, alice, bob := organizerFixture(t)
	require.NoError(t, s.db.AutoMigrate(&model.Notification{}))
	return s, ctx, alice, bob
}

func followUpCount(t *testing.T, s *MailWorkspaceService, user uuid.UUID) int64 {
	t.Helper()
	var count int64
	require.NoError(t, s.db.Model(&model.Notification{}).Where("recipient_id=? AND source_type=?", user, "mail_followup").Count(&count).Error)
	return count
}

func TestMailFollowUpDurableDeliveryIsExactlyOnce(t *testing.T) {
	s, ctx, alice, bob := followUpFixture(t)
	due := time.Now().UTC().Add(10 * time.Minute)
	record, err := s.MutateOrganizer(ctx, userForOrganizer(alice), OrganizerActionInput{MessageID: "mail-owned-1", Action: "follow_up", DueAt: &due})
	require.NoError(t, err)
	require.Equal(t, uint64(1), record.Revision)
	var stored model.MailOrganizerRecord
	require.NoError(t, s.db.Where("user_id=? AND email_id=?", alice, "mail-owned-1").Take(&stored).Error)
	require.NotContains(t, string(stored.Ciphertext), "follow_up")
	var schedule model.MailFollowUpSchedule
	require.NoError(t, s.db.Where("record_id=?", stored.ID).Take(&schedule).Error)
	require.Equal(t, alice, schedule.UserID)
	require.WithinDuration(t, due, schedule.DueAt, time.Second)
	early, err := RunMailFollowUpDue(ctx, s.db, s.vault, due.Add(-1*time.Minute))
	require.NoError(t, err)
	require.Zero(t, early.Delivered)
	result, err := RunMailFollowUpDue(ctx, s.db, s.vault, due.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, result.Delivered)
	require.Equal(t, int64(1), followUpCount(t, s, alice))
	require.Equal(t, int64(0), followUpCount(t, s, bob))
	result, err = RunMailFollowUpDue(ctx, s.db, s.vault, due.Add(time.Minute*3))
	require.NoError(t, err)
	require.Zero(t, result.Delivered)
	require.Equal(t, int64(1), followUpCount(t, s, alice))
	var n model.Notification
	require.NoError(t, s.db.Where("recipient_id=? AND source_type=?", alice, "mail_followup").Take(&n).Error)
	require.Equal(t, stored.ID, n.SourceID)
	require.Equal(t, "/mail", n.Meta["path"])
	require.NotContains(t, n.Meta, "mail-owned-1")
	require.Equal(t, "邮件跟进提醒", n.Meta["title"])
	require.NotContains(t, n.Meta["body"], "mail-owned-1")
}

func TestMailFollowUpCancelAndRescheduleAreTransactional(t *testing.T) {
	s, ctx, alice, _ := followUpFixture(t)
	user := userForOrganizer(alice)
	first := time.Now().UTC().Add(15 * time.Minute)
	item, err := s.MutateOrganizer(ctx, user, OrganizerActionInput{MessageID: "mail-owned-2", Action: "follow_up", DueAt: &first})
	require.NoError(t, err)
	second := first.Add(30 * time.Minute)
	item, err = s.MutateOrganizer(ctx, user, OrganizerActionInput{MessageID: "mail-owned-2", Action: "follow_up", DueAt: &second, Revision: item.Revision})
	require.NoError(t, err)
	old, err := RunMailFollowUpDue(ctx, s.db, s.vault, first.Add(time.Minute))
	require.NoError(t, err)
	require.Zero(t, old.Delivered)
	newDelivery, err := RunMailFollowUpDue(ctx, s.db, s.vault, second.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, newDelivery.Delivered)
	// Repeated setting after the first reminder has fired authorizes a second
	// future follow-up. It is not a duplicate from a crashed worker.
	third := time.Now().UTC().Add(75 * time.Minute)
	item, err = s.MutateOrganizer(ctx, user, OrganizerActionInput{MessageID: "mail-owned-2", Action: "follow_up", DueAt: &third, Revision: item.Revision})
	require.NoError(t, err)
	report, err := RunMailFollowUpDue(ctx, s.db, s.vault, third.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, report.Delivered)
	require.Equal(t, int64(2), followUpCount(t, s, alice))
	_, err = s.MutateOrganizer(ctx, user, OrganizerActionInput{MessageID: "mail-owned-2", Action: "clear_follow_up", Revision: item.Revision})
	require.NoError(t, err)
	var schedules int64
	require.NoError(t, s.db.Model(&model.MailFollowUpSchedule{}).Where("user_id=?", alice).Count(&schedules).Error)
	require.Zero(t, schedules)
	report, err = RunMailFollowUpDue(ctx, s.db, s.vault, third.Add(3*time.Minute))
	require.NoError(t, err)
	require.Zero(t, report.Delivered)
}

func TestMailFollowUpLegacyEncryptedRecordsBackfillWithoutDuplication(t *testing.T) {
	s, ctx, alice, _ := followUpFixture(t)
	due := time.Now().UTC().Add(10 * time.Minute)
	_, err := s.MutateOrganizer(ctx, userForOrganizer(alice), OrganizerActionInput{MessageID: "mail-old-1", Action: "follow_up", DueAt: &due})
	require.NoError(t, err)
	require.NoError(t, s.db.Where("user_id=?", alice).Delete(&model.MailFollowUpSchedule{}).Error)
	result, err := RunMailFollowUpDue(ctx, s.db, s.vault, due.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, 1, result.Backfilled)
	require.Equal(t, 1, result.Delivered)
	again, err := RunMailFollowUpDue(ctx, s.db, s.vault, due.Add(time.Minute*2))
	require.NoError(t, err)
	require.Zero(t, again.Delivered)
	require.Zero(t, again.Backfilled)
	require.Equal(t, int64(1), followUpCount(t, s, alice))
}

func TestMailFollowUpNoNotificationForOtherUserOrCancelledOldIndex(t *testing.T) {
	s, ctx, alice, bob := followUpFixture(t)
	due := time.Now().UTC().Add(10 * time.Minute)
	record, err := s.MutateOrganizer(ctx, userForOrganizer(alice), OrganizerActionInput{MessageID: "mail-private-a", Action: "follow_up", DueAt: &due})
	require.NoError(t, err)
	_, err = s.MutateOrganizer(ctx, userForOrganizer(alice), OrganizerActionInput{MessageID: "mail-private-a", Action: "clear_follow_up", Revision: record.Revision})
	require.NoError(t, err)
	report, err := RunMailFollowUpDue(ctx, s.db, s.vault, due.Add(time.Minute))
	require.NoError(t, err)
	require.Zero(t, report.Delivered)
	require.Zero(t, followUpCount(t, s, alice))
	require.Zero(t, followUpCount(t, s, bob))
}

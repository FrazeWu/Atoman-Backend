package mail

import (
	"atoman/internal/model"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMailContactsEncryptedScopedAndRevisioned(t *testing.T) {
	s, _, _, alice, bob := workspaceFixture(t, nil)
	ctx := context.Background()
	saved, err := s.SaveContact(ctx, alice, uuid.Nil, MailContactInput{Name: "同事", Email: "  Friend@Example.Test  ", Notes: "private notes"})
	require.NoError(t, err)
	require.Equal(t, uint64(1), saved.Revision)
	require.Equal(t, "friend@example.test", saved.Email)
	require.Equal(t, "同事", saved.Name)
	var record model.MailContact
	require.NoError(t, s.db.Where("id=?", saved.ID).Take(&record).Error)
	require.NotContains(t, string(record.Ciphertext), "friend@example.test")
	require.NotContains(t, string(record.Ciphertext), "private notes")
	mine, err := s.Contacts(ctx, alice)
	require.NoError(t, err)
	require.Len(t, mine, 1)
	other, err := s.Contacts(ctx, bob)
	require.NoError(t, err)
	require.Empty(t, other)
	_, err = s.SaveContact(ctx, bob, saved.ID, MailContactInput{Name: "steal", Email: "steal@example.test", Revision: saved.Revision})
	assertAppCode(t, err, "mail.contact_missing")
	_, err = s.SaveContact(ctx, alice, uuid.Nil, MailContactInput{Email: "friend@example.test"})
	assertAppCode(t, err, "mail.contact_duplicate")
	_, err = s.SaveContact(ctx, alice, saved.ID, MailContactInput{Email: "friend@example.test", Revision: 0})
	assertAppCode(t, err, "mail.contact_conflict")
	updated, err := s.SaveContact(ctx, alice, saved.ID, MailContactInput{Name: "New", Email: "friend@example.test", Revision: 1})
	require.NoError(t, err)
	require.Equal(t, uint64(2), updated.Revision)
	_, err = s.SaveContact(ctx, alice, saved.ID, MailContactInput{Name: "Lost", Email: "friend@example.test", Revision: 1})
	assertAppCode(t, err, "mail.contact_conflict")
	err = s.DeleteContact(ctx, bob, saved.ID)
	assertAppCode(t, err, "mail.contact_missing")
	require.NoError(t, s.DeleteContact(ctx, alice, saved.ID))
	left, err := s.Contacts(ctx, alice)
	require.NoError(t, err)
	require.Empty(t, left)
}

func TestMailContactsValidationAndSignatureSanitization(t *testing.T) {
	for _, raw := range []string{"", "bad", "user@example.test\r\nBcc:attacker@example.test", strings.Repeat("a", 260) + "@test.example"} {
		_, err := validateMailContact(MailContactInput{Email: raw})
		require.Error(t, err, raw)
	}
	settings := MailSettings{Signature: MailSignature{Enabled: true, Text: "Best regards", HTML: "<p>Hello</p><script>alert(1)</script>"}}
	require.NoError(t, validateSettings(&settings))
	require.NotContains(t, settings.Signature.HTML, "<script")
	require.Contains(t, settings.Signature.HTML, "Hello")
	settings.Signature.Text = strings.Repeat("z", 8193)
	require.Error(t, validateSettings(&settings))
}

func TestMailAdvancedFilterValidationAndConjunction(t *testing.T) {
	yes, no := true, false
	filter, err := buildMailQueryFilter(MailSearchOptions{
		From: "alice@example.test", To: "team@example.test", Subject: "Quarterly",
		After: "2026-11-01T00:00:00Z", Before: "2026-11-30T00:00:00Z",
		Unread: &yes, Starred: &yes, Attachment: &no,
	})
	require.NoError(t, err)
	require.Equal(t, "AND", filter["operator"])
	conditions := filter["conditions"].([]map[string]any)
	require.Len(t, conditions, 8)
	require.Equal(t, map[string]any{"notKeyword": "$seen"}, conditions[6])
	for _, options := range []MailSearchOptions{
		{After: "2026-11-01", Limit: 50},
		{After: "2026-11-05T00:00:00Z", Before: "2026-11-03T00:00:00Z"},
		{From: "x\r\nBcc: y"},
		{Subject: strings.Repeat("a", 513)},
		{Folder: "bad\nfolder"},
	} {
		_, err := buildMailQueryFilter(options)
		require.Error(t, err)
	}
}

func TestScheduledMailUsesDurableQueueAndCanCancel(t *testing.T) {
	_, sender, _, _, alice, _, _ := mailOutboxFixture(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	sender.now = func() time.Time { return now }
	input := composeFixture()
	due := now.Add(24 * time.Hour)
	input.SendAt = &due
	queued, err := sender.Queue(context.Background(), alice, input)
	require.NoError(t, err)
	require.True(t, queued.Scheduled)
	require.WithinDuration(t, due, queued.SendAfter, 0)
	var record model.MailPendingSend
	require.NoError(t, sender.db.Where("id=?", queued.ID).Take(&record).Error)
	require.Equal(t, "queued", record.Status)
	sender.now = func() time.Time { return due.Add(-time.Second) }
	// Scheduled send is not eligible before the server-side deadline.
	claimed, err := sender.ClaimDue(context.Background())
	require.NoError(t, err)
	require.Nil(t, claimed)
	cancelled, err := sender.Cancel(context.Background(), alice, queued.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", cancelled.Status)
	sender.now = func() time.Time { return due.Add(time.Second) }
	claimed, err = sender.ClaimDue(context.Background())
	require.NoError(t, err)
	require.Nil(t, claimed)
}

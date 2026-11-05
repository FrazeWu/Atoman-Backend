package mail

import (
	"atoman/internal/model"
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testMailWorkspaceRealFixture(t *testing.T, ctx context.Context, client *StalwartJMAPClient, aid string, cred StalwartCredential, messageID, mailboxID string) {
	t.Helper()
	s, outbox, _, alice, _ := workspaceFixture(t, nil)
	s.read.client = client
	require.NoError(t, s.db.Model(&model.MailAccount{}).Where("user_id = ?", alice.ID).Updates(map[string]any{"stalwart_account_id": aid, "address": "stage-reader@atoman-stage.test"}).Error)
	cipher, nonce, e := s.read.vault.Seal(alice.ID, aid, cred.secret)
	require.NoError(t, e)
	require.NoError(t, s.db.Model(&model.MailAccessCredential{}).Where("user_id = ?", alice.ID).Updates(map[string]any{"stalwart_account_id": aid, "ciphertext": cipher, "nonce": nonce}).Error)
	results, e := s.Search(ctx, alice, "", "Atoman JMAP synthetic", "", 0, 30)
	require.NoError(t, e)
	require.NotEmpty(t, results.Items)
	require.NotEmpty(t, results.Items[0].MailboxIDs)
	folder, e := s.CreateFolder(ctx, alice, "Atoman fixture custom")
	require.NoError(t, e)
	e = s.DeleteFolder(ctx, alice, mailboxID)
	require.Error(t, e, "system mailboxes must never be deletable")
	prefs := defaultMailSettings()
	prefs.AutoReply = MailAutoReply{Enabled: true, Subject: "Fixture reply", Text: "Synthetic automatic reply\nNo external mail during this check."}
	prefs.BlockedSenders = []string{"blocked@external.test"}
	prefs.Rules = []MailFilterRule{{ID: "fixture-move", Name: "Fixture move", Enabled: true, Field: "subject", Contains: "Atoman fixture filter", Action: "move", MailboxID: folder}}
	_, e = s.SaveSettings(ctx, alice, prefs)
	require.NoError(t, e, "Sieve compilation and activation")
	prefs.AutoReply.Enabled = false
	_, e = s.SaveSettings(ctx, alice, prefs)
	require.NoError(t, e, "disable auto-reply before synthetic SMTP")
	got, e := s.Settings(ctx, alice)
	require.NoError(t, e)
	require.False(t, got.AutoReply.Enabled)
	require.Len(t, got.Rules, 1)
	password, e := s.CreateAppPassword(ctx, alice, "Fixture mail client")
	require.NoError(t, e)
	require.NotEmpty(t, password.Password)
	fixtureIMAPPassword(t, "stage-reader@atoman-stage.test", password.Password, true)
	devices, e := s.AppPasswords(ctx, alice)
	require.NoError(t, e)
	found := false
	for _, d := range devices {
		require.Empty(t, d.Password)
		if d.ID == password.ID {
			found = true
		}
	}
	require.True(t, found)
	require.NoError(t, s.DeleteAppPassword(ctx, alice, password.ID))
	fixtureIMAPPassword(t, "stage-reader@atoman-stage.test", password.Password, false)
	devices, e = s.AppPasswords(ctx, alice)
	require.NoError(t, e)
	for _, d := range devices {
		require.NotEqual(t, password.ID, d.ID)
	}
	t.Log("STALWART_APP_PASSWORD_CREATE_IMAP_LOGIN_LIST_REVOKE_REJECTION_VERIFIED")
	action, e := s.Actions(ctx, alice, MailActionInput{IDs: []string{messageID}, Action: "unread"})
	require.NoError(t, e)
	require.NotEmpty(t, action.UndoToken)
	token, e := uuid.Parse(action.UndoToken)
	require.NoError(t, e)
	_, e = s.UndoAction(ctx, alice, token)
	require.NoError(t, e)
	t.Log("STALWART_BULK_ACTION_AND_CONFLICT_GUARDED_UNDO_VERIFIED")
	content := "From: stage-reader@atoman-stage.test\r\nTo: stage-reader@atoman-stage.test\r\nSubject: Atoman fixture filter attachment\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=fixture-boundary\r\n\r\n--fixture-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nPrivate fixture body\r\n--fixture-boundary\r\nContent-Type: text/plain; name=fixture.txt\r\nContent-Disposition: attachment; filename=fixture.txt\r\nContent-Transfer-Encoding: base64\r\n\r\nQXR0YWNobWVudCBmaXh0dXJlIGJ5dGVz\r\n--fixture-boundary--\r\n"
	fixtureSMTP(ctx, t, content)
	var imported string
	for i := 0; i < 25 && imported == ""; i++ {
		r, e := s.Search(ctx, alice, folder, "", "", 0, 30)
		require.NoError(t, e)
		for _, m := range r.Items {
			if m.Subject == "Atoman fixture filter attachment" {
				imported = m.ID
				break
			}
		}
		if imported == "" {
			time.Sleep(time.Second)
		}
	}
	require.NotEmpty(t, imported, "compiled Sieve must actually route SMTP to selected custom mailbox")
	require.Error(t, s.DeleteFolder(ctx, alice, folder), "nonempty custom mailbox must be protected")
	detail, e := client.ReadEmailDetail(ctx, cred, aid, folder, imported)
	require.NoError(t, e)
	require.Len(t, detail.Attachments, 1)
	attachment, name, ct, e := s.Download(ctx, alice, imported, detail.Attachments[0].PartID, false)
	require.NoError(t, e)
	require.Equal(t, "Attachment fixture bytes", strings.TrimSpace(string(attachment)))
	require.NotEmpty(t, ct)
	require.Equal(t, "fixture.txt", name)
	raw, _, _, e := s.Download(ctx, alice, imported, "", true)
	require.NoError(t, e)
	require.Contains(t, string(raw), "Atoman fixture filter attachment")
	_, _, _, e = s.Download(ctx, alice, imported, "not-an-owned-part", false)
	require.Error(t, e)
	t.Log("STALWART_SIEVE_DELIVERY_SEARCH_RAW_ATTACHMENT_OWNERSHIP_VERIFIED")
	// Move synthetic fixture mail before deleting our temporary folder.
	_, e = s.Actions(ctx, alice, MailActionInput{IDs: []string{imported}, Action: "move", MailboxID: mailboxID})
	require.NoError(t, e)
	_, e = s.SaveSettings(ctx, alice, defaultMailSettings())
	require.NoError(t, e)
	require.NoError(t, s.DeleteFolder(ctx, alice, folder))
	t.Log("STALWART_CUSTOM_FOLDER_EMPTY_DELETE_AND_SYSTEM_PROTECTION_VERIFIED")
	// Exercise the application durable queue and recover acceptance using the real
	// EmailSubmission/query receipt, without a second external submission.
	outbox.deliverer = &StalwartOutboxSubmitter{db: s.db, client: client, vault: s.read.vault}
	clock := time.Now().UTC()
	outbox.now = func() time.Time { return clock }
	draftID := uuid.New()
	_, e = s.SaveDraft(ctx, alice, draftID, SaveMailDraftInput{Compose: DraftCompose{To: []string{"stage-reader@atoman-stage.test"}, Subject: "Atoman durable receipt", Text: "Local self mail only"}})
	require.NoError(t, e)
	queued, e := outbox.Queue(ctx, alice, ComposeMailInput{RequestID: uuid.New(), DraftID: &draftID, DraftRevision: 1, To: []string{"stage-reader@atoman-stage.test"}, Subject: "Atoman durable receipt", Text: "Local self mail only"})
	require.NoError(t, e)
	clock = clock.Add(6 * time.Second)
	n, e := outbox.RunDue(ctx, 1)
	require.NoError(t, e)
	require.Equal(t, 1, n)
	var pending model.MailPendingSend
	require.NoError(t, s.db.Where("id = ?", queued.ID).Take(&pending).Error)
	require.Equal(t, "sent", pending.Status)
	require.NotEmpty(t, pending.ImportedEmailID)
	require.NotEmpty(t, pending.SubmissionID)
	// Deliberately remove the local final receipt to exercise upstream evidence.
	require.NoError(t, s.db.Model(&model.MailPendingSend{}).Where("id = ?", queued.ID).Updates(map[string]any{"status": "uncertain", "submission_id": "", "sent_at": nil, "claimed_at": clock.Add(-3 * time.Minute)}).Error)
	recovered, e := outbox.Reconcile(ctx, alice, queued.ID)
	require.NoError(t, e)
	require.Equal(t, "sent", recovered.Status)
	n, e = outbox.RunDue(ctx, 1)
	require.NoError(t, e)
	require.Zero(t, n)
	_, e = s.Draft(ctx, alice, draftID)
	require.Error(t, e)
	t.Log("STALWART_DURABLE_OUTBOX_RECEIPT_RECONCILIATION_WITHOUT_RESEND_VERIFIED")
}

func fixtureSMTP(ctx context.Context, t *testing.T, body string) {
	t.Helper()
	conn, e := (&net.Dialer{Timeout: 6 * time.Second}).DialContext(ctx, "tcp", fixtureSMTPAddress(t))
	require.NoError(t, e)
	client, e := smtp.NewClient(conn, "127.0.0.1")
	require.NoError(t, e)
	defer client.Close()
	require.NoError(t, client.Hello("fixture.example.test"))
	require.NoError(t, client.Mail("stage-reader@atoman-stage.test"))
	require.NoError(t, client.Rcpt("stage-reader@atoman-stage.test"))
	w, e := client.Data()
	require.NoError(t, e)
	_, e = fmt.Fprint(w, body)
	require.NoError(t, e)
	require.NoError(t, w.Close())
	require.NoError(t, client.Quit())
}

func fixtureSMTPAddress(t *testing.T) string {
	t.Helper()
	v := os.Getenv("ATOMAN_MAIL_STAGING_SMTP_ADDR")
	if v == "" {
		v = "127.0.0.1:19025"
	}
	if v != "127.0.0.1:19025" && v != "127.0.0.1:19027" {
		t.Fatal("unexpected disposable fixture SMTP endpoint")
	}
	return v
}

// This deliberately accepts the disposable fixture's self-signed certificate;
// the address is selected only from two hardcoded loopback fixture listeners.
func fixtureIMAPPassword(t *testing.T, username, password string, wantSuccess bool) {
	t.Helper()
	address := "127.0.0.1:19993"
	if fixtureSMTPAddress(t) == "127.0.0.1:19027" {
		address = "127.0.0.1:19995"
	}
	conn, e := tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", address, &tls.Config{InsecureSkipVerify: true})
	require.NoError(t, e)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(8*time.Second)))
	r := bufio.NewReader(conn)
	line, e := r.ReadString('\n')
	require.NoError(t, e)
	require.True(t, strings.HasPrefix(line, "* OK"))
	_, e = fmt.Fprintf(conn, "a1 LOGIN %s %s\r\n", strconv.Quote(username), strconv.Quote(password))
	require.NoError(t, e)
	for i := 0; i < 30; i++ {
		line, e = r.ReadString('\n')
		require.NoError(t, e)
		if strings.HasPrefix(line, "a1 ") {
			require.Equal(t, wantSuccess, strings.HasPrefix(line, "a1 OK"), "application password login result must match revocation state")
			return
		}
	}
	t.Fatal("No tagged fixture IMAP authentication result")
}

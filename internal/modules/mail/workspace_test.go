package mail

import (
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	stdmail "net/mail"
	"strings"
	"testing"
	"time"
)

type workspaceResponder func(string, map[string]any) any

func workspaceFixture(t *testing.T, reply workspaceResponder) (*MailWorkspaceService, *MailOutboxService, *fakePendingStorage, authctx.CurrentUser, authctx.CurrentUser) {
	t.Helper()
	db, sender, storage, _, alice, bob, _ := mailOutboxFixture(t)
	require.NoError(t, migrations.RunMailWorkspaceMigration(db))
	require.NoError(t, migrations.RunMailWorkspaceMigration(db))
	bobAccount := "workspace-bob-account"
	var existing model.MailAccount
	require.NoError(t, db.Where("user_id = ?", bob.ID).Assign(map[string]any{"status": "active", "stalwart_account_id": bobAccount}).FirstOrCreate(&existing, model.MailAccount{ID: uuid.New(), UserID: bob.ID, LocalPart: "workspace-bob", Address: "workspace-bob@atoman.org", Status: "active", StalwartAccountID: &bobAccount}).Error)
	encrypted, nonce, e := fixtureCredentialVault(t).Seal(bob.ID, bobAccount, "synthetic-bob-key")
	require.NoError(t, e)
	require.NoError(t, db.Create(&model.MailAccessCredential{ID: uuid.New(), UserID: bob.ID, StalwartAccountID: bobAccount, APIKeyID: "bob-key-id", Ciphertext: encrypted, Nonce: nonce, KeyVersion: 1}).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aid := "stalwart-user-123"
		if r.Header.Get("Authorization") == "Bearer synthetic-bob-key" {
			aid = bobAccount
		} else {
			require.Equal(t, "Bearer one-time-staging-secret-value", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/jmap/session" {
			json.NewEncoder(w).Encode(map[string]any{"primaryAccounts": map[string]string{jmapMailCapability: aid}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/jmap/upload/") {
			require.Equal(t, "/jmap/upload/"+aid+"/", r.URL.Path)
			b, e := io.ReadAll(r.Body)
			require.NoError(t, e)
			if reply != nil {
				reply("upload", map[string]any{"body": string(b), "type": r.Header.Get("Content-Type")})
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"accountId": aid, "blobId": "test-blob", "size": len(b)})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/jmap/download/") {
			require.Equal(t, "/jmap/download/"+aid+"/owned-blob/download", r.URL.Path)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Write([]byte("owned attachment"))
			return
		}
		var request struct {
			Calls [][]json.RawMessage `json:"methodCalls"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		var method string
		var args map[string]any
		require.NoError(t, json.Unmarshal(request.Calls[0][0], &method))
		require.NoError(t, json.Unmarshal(request.Calls[0][1], &args))
		require.Equal(t, aid, args["accountId"])
		if reply == nil {
			t.Errorf("Unexpected method: %s", method)
			http.Error(w, "unexpected", 500)
			return
		}
		data := reply(method, args)
		json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, data, "c1"}}})
	}))
	t.Cleanup(server.Close)
	client, e := NewStalwartJMAPClient(server.URL, nil)
	require.NoError(t, e)
	reader := NewMailReadService(db, client, fixtureCredentialVault(t), true)
	return NewMailWorkspaceService(reader, sender), sender, storage, alice, bob
}
func TestMailWorkspaceDraftEncryptionRevisionAndOwnership(t *testing.T) {
	s, _, storage, alice, bob := workspaceFixture(t, nil)
	ctx := context.Background()
	upload, e := s.Upload(ctx, alice, "report.pdf", "application/pdf", strings.NewReader("private draft attachment"))
	require.NoError(t, e)
	encrypted, e := storage.Get(ctx, upload.ObjectKey)
	require.NoError(t, e)
	require.NotContains(t, string(encrypted), "private draft attachment")
	id := uuid.New()
	in := SaveMailDraftInput{Compose: DraftCompose{To: []string{"target@example.test"}, Subject: "private subject", Text: "private draft text", Attachments: []model.MailUpload{upload}}}
	saved, e := s.SaveDraft(ctx, alice, id, in)
	require.NoError(t, e)
	require.Equal(t, uint64(1), saved.Revision)
	var row model.MailDraft
	require.NoError(t, s.db.Where("id = ?", id).Take(&row).Error)
	require.NotContains(t, string(row.Ciphertext), "private subject")
	reopened := NewMailWorkspaceService(s.read, &MailOutboxService{vault: s.vault, store: s.store})
	got, e := reopened.Draft(ctx, alice, id)
	require.NoError(t, e)
	require.Equal(t, "private draft text", got.Compose.Text)
	require.Equal(t, upload.ID, got.Compose.Attachments[0].ID)
	_, e = s.Draft(ctx, bob, id)
	assertAppCode(t, e, "mail.draft_missing")
	in.Revision = 1
	in.Compose.Text = "updated"
	_, e = s.SaveDraft(ctx, alice, id, in)
	require.NoError(t, e)
	_, e = s.SaveDraft(ctx, alice, id, in)
	assertAppCode(t, e, "mail.draft_conflict")
	_, e = s.SaveDraft(ctx, bob, uuid.New(), SaveMailDraftInput{Compose: DraftCompose{Attachments: []model.MailUpload{upload}}})
	assertAppCode(t, e, "mail.invalid_attachment")
	e = s.DeleteUpload(ctx, bob, upload.ID)
	assertAppCode(t, e, "mail.attachment_missing")
	require.NoError(t, s.DeleteDraft(ctx, bob, id))
	_, e = s.Draft(ctx, alice, id)
	require.NoError(t, e)
	require.NoError(t, s.DeleteDraft(ctx, alice, id))
	_, e = s.Draft(ctx, alice, id)
	assertAppCode(t, e, "mail.draft_missing")
}
func TestMailWorkspaceUploadBoundAndAuthenticatedSendSnapshot(t *testing.T) {
	s, sender, storage, alice, bob := workspaceFixture(t, nil)
	ctx := context.Background()
	_, e := s.Upload(ctx, alice, "huge.bin", "application/octet-stream", strings.NewReader(strings.Repeat("a", maxMailAttachmentBytes+1)))
	require.Error(t, e)
	a, e := s.Upload(ctx, alice, "../report.txt", "text/plain", strings.NewReader("snapshot attachment"))
	require.NoError(t, e)
	require.Equal(t, "report.txt", a.Name)
	input := composeFixture()
	input.AttachmentIDs = []uuid.UUID{a.ID}
	_, e = sender.Queue(ctx, bob, input)
	require.Error(t, e)
	queued, e := sender.Queue(ctx, alice, input)
	require.NoError(t, e)
	var pending model.MailPendingSend
	require.NoError(t, s.db.Where("id = ?", queued.ID).Take(&pending).Error)
	snapshot, e := storage.Get(ctx, pending.SnapshotKey)
	require.NoError(t, e)
	raw, e := sender.vault.Open(alice.ID, pending.ID, snapshot)
	require.NoError(t, e)
	var payload outboxPayload
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.Contains(t, string(payload.MIME), "report.txt")
	require.NoError(t, s.DeleteUpload(ctx, alice, a.ID))
	_, e = storage.Get(ctx, pending.SnapshotKey)
	require.NoError(t, e, "queued snapshot is independent from source upload")
}
func TestMailMultipartAlternativeAttachmentsReplyHeadersAndBcc(t *testing.T) {
	input := composeFixture()
	input.HTML = "<p>Hello <b>world</b></p><img src='https://tracker.invalid'><script>bad()</script>"
	input.InReplyTo = "original@example.test"
	input.References = []string{"older@example.test", "original@example.test"}
	input.attachments = []composeAttachment{{Name: "notes-附件.txt", Type: "text/plain", Data: []byte("attachment content")}}
	p, e := buildOutboxPayload("alice@atoman.org", uuid.New(), input, time.Now())
	require.NoError(t, e)
	require.NotContains(t, string(p.MIME), "Bcc:")
	for _, line := range strings.Split(string(p.MIME), "\r\n") {
		require.LessOrEqual(t, len(line), 998)
	}
	message, e := stdmail.ReadMessage(bytes.NewReader(p.MIME))
	require.NoError(t, e)
	require.Equal(t, "<original@example.test>", message.Header.Get("In-Reply-To"))
	ct, params, e := mime.ParseMediaType(message.Header.Get("Content-Type"))
	require.NoError(t, e)
	require.Equal(t, "multipart/mixed", ct)
	parts := multipart.NewReader(message.Body, params["boundary"])
	part, e := parts.NextPart()
	require.NoError(t, e)
	ct, params, e = mime.ParseMediaType(part.Header.Get("Content-Type"))
	require.NoError(t, e)
	require.Equal(t, "multipart/alternative", ct)
	alt := multipart.NewReader(part, params["boundary"])
	plain, e := alt.NextPart()
	require.NoError(t, e)
	body, e := io.ReadAll(base64.NewDecoder(base64.StdEncoding, plain))
	require.NoError(t, e)
	require.Equal(t, input.Text, string(body))
	rich, e := alt.NextPart()
	require.NoError(t, e)
	body, e = io.ReadAll(base64.NewDecoder(base64.StdEncoding, rich))
	require.NoError(t, e)
	require.Contains(t, string(body), "<b>world</b>")
	require.NotContains(t, string(body), "tracker.invalid")
	require.NotContains(t, string(body), "bad()")
	attachment, e := parts.NextPart()
	require.NoError(t, e)
	require.Equal(t, "notes-附件.txt", attachment.FileName())
	body, e = io.ReadAll(base64.NewDecoder(base64.StdEncoding, attachment))
	require.NoError(t, e)
	require.Equal(t, "attachment content", string(body))
	input.InReplyTo = "ok@example.test\r\nBcc: evil@example.test"
	_, e = buildOutboxPayload("alice@atoman.org", uuid.New(), input, time.Now())
	require.Error(t, e)
}
func TestMailWorkspaceBulkUndoRefusesConcurrentMutation(t *testing.T) {
	state := 1
	emails := map[string]emailState{"e1": {ID: "e1", MailboxIDs: map[string]bool{"inbox": true}, Keywords: map[string]bool{}}}
	reply := func(method string, args map[string]any) any {
		switch method {
		case "Email/get":
			list := []emailState{}
			missing := []string{}
			for _, raw := range args["ids"].([]any) {
				id := raw.(string)
				if m, ok := emails[id]; ok {
					list = append(list, m)
				} else {
					missing = append(missing, id)
				}
			}
			return map[string]any{"list": list, "state": fmt.Sprint(state), "notFound": missing}
		case "Email/set":
			require.Equal(t, fmt.Sprint(state), args["ifInState"])
			updated := map[string]any{}
			for id, raw := range args["update"].(map[string]any) {
				v := raw.(map[string]any)
				m := emails[id]
				m.Keywords = map[string]bool{}
				m.MailboxIDs = map[string]bool{}
				for k, b := range v["keywords"].(map[string]any) {
					m.Keywords[k] = b.(bool)
				}
				for k, b := range v["mailboxIds"].(map[string]any) {
					m.MailboxIDs[k] = b.(bool)
				}
				emails[id] = m
				updated[id] = nil
			}
			state++
			return map[string]any{"updated": updated}
		default:
			t.Fatalf("unexpected method %s", method)
			return nil
		}
	}
	s, _, _, alice, bob := workspaceFixture(t, reply)
	ctx := context.Background()
	action, e := s.Actions(ctx, alice, MailActionInput{IDs: []string{"e1"}, Action: "read"})
	require.NoError(t, e)
	require.True(t, emails["e1"].Keywords["$seen"])
	token, e := uuid.Parse(action.UndoToken)
	require.NoError(t, e)
	_, e = s.UndoAction(ctx, bob, token)
	require.Error(t, e)
	_, e = s.UndoAction(ctx, alice, token)
	require.NoError(t, e)
	require.False(t, emails["e1"].Keywords["$seen"])
	action, e = s.Actions(ctx, alice, MailActionInput{IDs: []string{"e1"}, Action: "read"})
	require.NoError(t, e)
	m := emails["e1"]
	m.Keywords["$flagged"] = true
	emails["e1"] = m
	state++
	token = uuid.MustParse(action.UndoToken)
	_, e = s.UndoAction(ctx, alice, token)
	require.Error(t, e)
	require.True(t, emails["e1"].Keywords["$flagged"])
	_, e = s.Actions(ctx, alice, MailActionInput{IDs: []string{"e1", "e1"}, Action: "read"})
	require.Error(t, e)
}

type delayedPendingStorage struct {
	*fakePendingStorage
	clock *time.Time
}

func (s *delayedPendingStorage) Put(ctx context.Context, key string, b []byte) error {
	e := s.fakePendingStorage.Put(ctx, key, b)
	*s.clock = s.clock.Add(7 * time.Second)
	return e
}
func TestMailOutboxUndoWindowStartsAfterDurableStorage(t *testing.T) {
	_, sender, store, _, alice, _, clock := mailOutboxFixture(t)
	sender.store = &delayedPendingStorage{fakePendingStorage: store, clock: clock}
	queued, e := sender.Queue(context.Background(), alice, ComposeMailInput{RequestID: uuid.New(), Cc: []string{"copy@example.test"}, Subject: "Delayed durable save", Text: "The full undo window starts after storage succeeds."})
	require.NoError(t, e)
	require.Equal(t, clock.Add(5*time.Second), queued.SendAfter)
	require.Equal(t, *clock, queued.CreatedAt)
	_, e = sender.Cancel(context.Background(), alice, queued.ID)
	require.NoError(t, e)
}
func TestMailOutboxDraftOwnershipAndRevisionLifecycle(t *testing.T) {
	for _, mode := range []string{"sent", "edited-after-queue", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, sender, _, alice, bob := workspaceFixture(t, nil)
			ctx := context.Background()
			now := time.Now().UTC()
			sender.now = func() time.Time { return now }
			draftID := uuid.New()
			compose := DraftCompose{Bcc: []string{"hidden@example.test"}, Subject: "Durable draft", Text: "Original body"}
			_, e := s.SaveDraft(ctx, alice, draftID, SaveMailDraftInput{Compose: compose})
			require.NoError(t, e)
			input := ComposeMailInput{RequestID: uuid.New(), DraftID: &draftID, DraftRevision: 1, Bcc: compose.Bcc, Subject: compose.Subject, Text: compose.Text}
			_, e = sender.Queue(ctx, bob, input)
			assertAppCode(t, e, "mail.draft_missing")
			input.DraftRevision = 2
			_, e = sender.Queue(ctx, alice, input)
			assertAppCode(t, e, "mail.draft_conflict")
			input.DraftRevision = 1
			queued, e := sender.Queue(ctx, alice, input)
			require.NoError(t, e)
			require.NotNil(t, queued.DraftID)
			require.Equal(t, draftID, *queued.DraftID)
			if mode == "edited-after-queue" {
				compose.Text = "New unsent edit"
				_, e = s.SaveDraft(ctx, alice, draftID, SaveMailDraftInput{Revision: 1, Compose: compose})
				require.NoError(t, e)
			}
			if mode == "cancelled" {
				_, e = sender.Cancel(ctx, alice, queued.ID)
				require.NoError(t, e)
			} else {
				now = now.Add(6 * time.Second)
				_, e = sender.RunDue(ctx, 1)
				require.NoError(t, e)
			}
			got, e := s.Draft(ctx, alice, draftID)
			if mode == "sent" {
				assertAppCode(t, e, "mail.draft_missing")
			} else {
				require.NoError(t, e)
				require.Equal(t, compose.Text, got.Compose.Text)
			}
			// A transport retry must still resolve the original request even if its sent draft is gone.
			same, e := sender.Queue(ctx, alice, input)
			require.NoError(t, e)
			require.Equal(t, queued.ID, same.ID)
		})
	}
}
func TestMailMaintenanceDoesNotExpungeMailboxMessages(t *testing.T) {
	s, _, _, alice, _ := workspaceFixture(t, func(method string, _ map[string]any) any {
		t.Fatalf("maintenance must not read or destroy message through JMAP: %s", method)
		return nil
	})
	require.NoError(t, s.db.Create(&model.MailRetention{UserID: alice.ID, EmailID: "old-trash", Role: "trash", MovedAt: time.Now().Add(-60 * 24 * time.Hour)}).Error)
	_, e := s.RunMaintenance(context.Background(), nil)
	require.NoError(t, e)
	var count int64
	require.NoError(t, s.db.Model(&model.MailRetention{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

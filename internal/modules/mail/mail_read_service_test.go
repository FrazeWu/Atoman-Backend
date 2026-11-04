package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMailReadServiceScopesUserToOwnEncryptedStalwartAccount(t *testing.T) {
	db, vault, admin, alice, bob := mailProvisionTestSetup(t)
	provider := &fakeMailProvisioner{}
	provisioner := NewMailProvisionService(db, provider, vault, StalwartBasic("admin", "fixture-secret"), MailActivationGates{true, true, true})
	_, err := provisioner.ProvisionReserved(context.Background(), admin, alice.ID)
	require.NoError(t, err)

	var queryCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer one-time-staging-secret-value", r.Header.Get("Authorization"))
		if r.URL.Path == "/jmap/session" {
			_ = json.NewEncoder(w).Encode(map[string]any{"primaryAccounts": map[string]string{jmapMailCapability: "stalwart-user-123"}})
			return
		}
		var input struct {
			Calls [][]json.RawMessage `json:"methodCalls"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		var method string
		require.NoError(t, json.Unmarshal(input.Calls[0][0], &method))
		var args map[string]any
		require.NoError(t, json.Unmarshal(input.Calls[0][1], &args))
		require.Equal(t, "stalwart-user-123", args["accountId"])
		var result any
		switch method {
		case "Mailbox/get":
			result = map[string]any{"list": []map[string]any{{"id": "alice-inbox", "name": "Inbox", "role": "inbox", "totalEmails": 1, "unreadEmails": 1}}}
		case "Email/query":
			queryCount++
			require.Equal(t, map[string]any{"inMailbox": "alice-inbox"}, args["filter"])
			result = map[string]any{"ids": []string{"alice-message"}}
		case "Email/get":
			result = map[string]any{"list": []map[string]any{{"id": "alice-message", "threadId": "alice-thread", "subject": "hello alice", "receivedAt": "2026-11-04T01:00:00Z", "preview": "text only"}}}
		default:
			t.Fatalf("unexpected mail query method %q", method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "c1"}}})
	}))
	defer server.Close()
	client, err := NewStalwartJMAPClient(server.URL, server.Client())
	require.NoError(t, err)
	read := NewMailReadService(db, client, vault, true)
	folders, err := read.Folders(context.Background(), alice)
	require.NoError(t, err)
	require.Len(t, folders, 1)
	require.Equal(t, "alice-inbox", folders[0].ID)
	messages, err := read.Messages(context.Background(), alice, "alice-inbox", 25)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "hello alice", messages[0].Subject)
	require.Equal(t, 1, queryCount)
	_, err = read.Messages(context.Background(), alice, "bob-private-inbox", 25)
	assertAppCode(t, err, "mail.folder_not_found")
	require.Equal(t, 1, queryCount)
	_, err = read.Folders(context.Background(), bob)
	assertAppCode(t, err, "mail.mailbox_inactive")
	_, err = read.Messages(context.Background(), bob, "alice-inbox", 25)
	assertAppCode(t, err, "mail.mailbox_inactive")
	_, err = NewMailReadService(db, client, vault, false).Folders(context.Background(), alice)
	assertAppCode(t, err, "mail.read_not_ready")
}

func TestMailReadServiceFailsClosedOnUpstreamAccountMismatch(t *testing.T) {
	db, vault, admin, alice, _ := mailProvisionTestSetup(t)
	_, err := NewMailProvisionService(db, &fakeMailProvisioner{}, vault, StalwartBasic("admin", "fixture-secret"), MailActivationGates{true, true, true}).ProvisionReserved(context.Background(), admin, alice.ID)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/jmap/session", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"primaryAccounts": map[string]string{jmapMailCapability: "other-user"}})
	}))
	defer server.Close()
	client, err := NewStalwartJMAPClient(server.URL, nil)
	require.NoError(t, err)
	read := NewMailReadService(db, client, vault, true)
	_, err = read.Folders(context.Background(), alice)
	assertAppCode(t, err, "mail.account_scope_mismatch")
}

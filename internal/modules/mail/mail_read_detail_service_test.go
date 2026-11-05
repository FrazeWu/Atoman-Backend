package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMailReadDetailRequiresOwnActiveAccountAndFolder(t *testing.T) {
	db, vault, admin, alice, bob := mailProvisionTestSetup(t)
	_, err := NewMailProvisionService(db, &fakeMailProvisioner{}, vault, StalwartBasic("admin", "secret"), MailActivationGates{true, true, true}).ProvisionReserved(context.Background(), admin, alice.ID)
	require.NoError(t, err)
	var setCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			result = map[string]any{"list": []any{map[string]any{"id": "alice-folder", "name": "Inbox", "role": "inbox"}}}
		case "Email/get":
			result = map[string]any{"list": []any{map[string]any{"id": "alice-message", "mailboxIds": map[string]bool{"alice-folder": true}, "textBody": []any{map[string]string{"partId": "1", "type": "text/plain"}}, "bodyValues": map[string]any{"1": map[string]any{"value": "private alice-only body"}}}}}
		case "Email/set":
			setCalls++
			result = map[string]any{"updated": map[string]any{"alice-message": nil}}
		default:
			t.Fatalf("unexpected JMAP method %q", method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "c1"}}})
	}))
	defer upstream.Close()
	client, err := NewStalwartJMAPClient(upstream.URL, nil)
	require.NoError(t, err)
	read := NewMailReadService(db, client, vault, true)
	_, err = read.Detail(context.Background(), bob, "alice-folder", "alice-message")
	assertAppCode(t, err, "mail.mailbox_inactive")
	_, err = read.Detail(context.Background(), alice, "bob-folder", "alice-message")
	assertAppCode(t, err, "mail.folder_not_found")
	detail, err := read.Detail(context.Background(), alice, "alice-folder", "alice-message")
	require.NoError(t, err)
	require.Equal(t, "private alice-only body", detail.PlainText)
	require.Equal(t, 0, setCalls, "fetching mail detail itself must not change read status")
	require.NoError(t, read.MarkSeen(context.Background(), alice, "alice-folder", "alice-message"))
	require.Equal(t, 1, setCalls)
	err = read.MarkSeen(context.Background(), bob, "alice-folder", "alice-message")
	assertAppCode(t, err, "mail.mailbox_inactive")
	require.Equal(t, 1, setCalls)
}

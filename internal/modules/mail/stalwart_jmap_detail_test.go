package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMailDetailSanitizesHTMLAndUsesExplicitReadMutation(t *testing.T) {
	var seenCalls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer synthetic-own-mail-key", r.Header.Get("Authorization"))
		var req struct {
			Calls [][]json.RawMessage `json:"methodCalls"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var method string
		require.NoError(t, json.Unmarshal(req.Calls[0][0], &method))
		var args map[string]any
		require.NoError(t, json.Unmarshal(req.Calls[0][1], &args))
		require.Equal(t, "account-own", args["accountId"])
		seenCalls = append(seenCalls, method)
		var result any
		switch method {
		case "Email/get":
			require.Equal(t, []any{"message-own"}, args["ids"])
			props := args["properties"].([]any)
			if len(props) > 2 {
				require.Equal(t, true, args["fetchTextBodyValues"])
				require.Equal(t, true, args["fetchHTMLBodyValues"])
				require.Equal(t, float64(maxMailTextBodyBytes), args["maxBodyValueBytes"])
				result = map[string]any{"list": []any{map[string]any{
					"id": "message-own", "threadId": "thread-own", "subject": "hello", "receivedAt": "2026-11-05T00:00:00Z",
					"mailboxIds": map[string]bool{"inbox-own": true}, "keywords": map[string]bool{"$seen": false},
					"from":        []any{map[string]string{"name": "Sender", "email": "sender@example.test"}},
					"to":          []any{map[string]string{"email": "reader@example.test"}},
					"cc":          []any{},
					"textBody":    []any{map[string]string{"partId": "1", "type": "text/plain"}, map[string]string{"partId": "2", "type": "text/html"}},
					"htmlBody":    []any{map[string]string{"partId": "2", "type": "text/html"}},
					"bodyValues":  map[string]any{"1": map[string]any{"value": "Hello <script>unsafe</script> as literal text", "isTruncated": false}, "2": map[string]any{"value": "<p onclick='bad()'>Safe HTML</p><script>bad()</script><img src='https://tracker.invalid'>"}},
					"attachments": []any{map[string]any{"name": "note.pdf", "type": "application/pdf", "size": 1234}},
				}}}
			} else {
				result = map[string]any{"list": []any{map[string]any{"id": "message-own", "mailboxIds": map[string]bool{"inbox-own": true}}}}
			}
		case "Email/set":
			require.Equal(t, map[string]any{"keywords/$seen": true}, args["update"].(map[string]any)["message-own"])
			result = map[string]any{"updated": map[string]any{"message-own": nil}}
		default:
			t.Fatalf("unexpected JMAP method %s", method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "c1"}}})
	}))
	defer server.Close()
	client, err := NewStalwartJMAPClient(server.URL, nil)
	require.NoError(t, err)
	cred := StalwartBearer("synthetic-own-mail-key")
	detail, err := client.ReadEmailDetail(context.Background(), cred, "account-own", "inbox-own", "message-own")
	require.NoError(t, err)
	require.Equal(t, "Hello <script>unsafe</script> as literal text", detail.PlainText)
	require.Equal(t, []MailAddress{{Name: "Sender", Email: "sender@example.test"}}, detail.From)
	require.Len(t, detail.Attachments, 1)
	require.Contains(t, detail.HTML, "<p>Safe HTML</p>")
	require.NotContains(t, detail.HTML, "bad()")
	require.NotContains(t, detail.HTML, "tracker.invalid")
	require.False(t, detail.IsRead)
	require.False(t, detail.BodyTruncated)
	require.Equal(t, []string{"Email/get"}, seenCalls, "read-only detail must not mark $seen")
	require.NoError(t, client.MarkEmailSeen(context.Background(), cred, "account-own", "inbox-own", "message-own"))
	require.Equal(t, []string{"Email/get", "Email/get", "Email/set"}, seenCalls)
}

func TestMailDetailFailsClosedForIncorrectMailboxAndMissingEmail(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result map[string]any
	}{
		{"other_mailbox", map[string]any{"list": []any{map[string]any{"id": "message-own", "mailboxIds": map[string]bool{"inbox-other": true}, "textBody": []any{}}}}},
		{"missing", map[string]any{"list": []any{}, "notFound": []string{"message-own"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Calls [][]json.RawMessage `json:"methodCalls"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				var method string
				require.NoError(t, json.Unmarshal(req.Calls[0][0], &method))
				require.Equal(t, "Email/get", method, "must never mark missing or cross-folder messages read")
				_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{"Email/get", tc.result, "c1"}}})
			}))
			defer server.Close()
			client, err := NewStalwartJMAPClient(server.URL, nil)
			require.NoError(t, err)
			cred := StalwartBearer("synthetic")
			_, err = client.ReadEmailDetail(context.Background(), cred, "account-own", "inbox-own", "message-own")
			require.Error(t, err)
			require.Error(t, client.MarkEmailSeen(context.Background(), cred, "account-own", "inbox-own", "message-own"))
		})
	}
}

func TestMailDetailInputAndOversizeFailures(t *testing.T) {
	client, err := NewStalwartJMAPClient("http://127.0.0.1:18582", nil)
	require.NoError(t, err)
	for _, id := range []string{"", " bad", "\r", "id\n2", strings.Repeat("x", 513)} {
		_, err = client.ReadEmailDetail(context.Background(), StalwartBearer("key"), "account", "folder", id)
		require.Error(t, err)
		require.Error(t, client.MarkEmailSeen(context.Background(), StalwartBearer("key"), "account", "folder", id))
	}
}

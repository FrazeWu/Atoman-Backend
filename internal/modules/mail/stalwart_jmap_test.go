package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestStalwartJMAPRejectsUnsafeEndpoints(t *testing.T) {
	for _, raw := range []string{
		"", "http://mail.example.com", "http://10.0.0.2:8080", "ftp://localhost",
		"http://localhost:8080/jmap/", "https://user:pass@mail.example.com",
		"https://mail.example.com/?token=abc", "https://mail.example.com/#fragment",
	} {
		_, err := NewStalwartJMAPClient(raw, nil)
		require.Error(t, err, raw)
	}
	for _, raw := range []string{"http://127.0.0.1:18582", "http://[::1]:18582", "https://mail.example.com"} {
		_, err := NewStalwartJMAPClient(raw, nil)
		require.NoError(t, err, raw)
	}
}

func TestStalwartJMAPProvisionAndReadEnvelope(t *testing.T) {
	userID := uuid.New()
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jmap/session" {
			require.Equal(t, "GET", r.Method)
			require.Equal(t, "Bearer user-key-synthetic", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{"username": "reader@example.test", "primaryAccounts": map[string]string{jmapMailCapability: "user-123"}, "accounts": map[string]any{"user-123": map[string]string{"name": "reader@example.test"}}})
			return
		}
		require.Equal(t, "/jmap/", r.URL.Path)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var envelope struct {
			Using []string            `json:"using"`
			Calls [][]json.RawMessage `json:"methodCalls"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
		require.Len(t, envelope.Calls, 1)
		var method string
		require.NoError(t, json.Unmarshal(envelope.Calls[0][0], &method))
		methods = append(methods, method)
		var args map[string]any
		require.NoError(t, json.Unmarshal(envelope.Calls[0][1], &args))
		if strings.HasPrefix(method, "x:") {
			require.Equal(t, "Basic YWRtaW46c3RhZ2Utc2VjcmV0", r.Header.Get("Authorization"))
			require.Contains(t, envelope.Using, jmapAdminCapability)
		} else {
			require.Equal(t, "Bearer user-key-synthetic", r.Header.Get("Authorization"))
			require.Contains(t, envelope.Using, jmapMailCapability)
		}
		var result any
		switch method {
		case "x:Domain/query":
			require.Equal(t, map[string]any{"name": "example.test"}, args["filter"])
			result = map[string]any{"ids": []string{"domain-123"}}
		case "x:Domain/get":
			result = map[string]any{"list": []map[string]string{{"id": "domain-123", "name": "example.test"}}}
		case "x:Account/set":
			create := args["create"].(map[string]any)["u1"].(map[string]any)
			require.Equal(t, "User", create["@type"])
			require.Equal(t, "reader", create["name"])
			require.Equal(t, "domain-123", create["domainId"])
			require.Equal(t, map[string]any{}, create["credentials"])
			require.Equal(t, map[string]any{"@type": "User"}, create["roles"])
			require.Equal(t, map[string]any{"@type": "Inherit"}, create["permissions"])
			require.Equal(t, "Atoman user "+userID.String(), create["description"])
			result = map[string]any{"created": map[string]any{"u1": map[string]string{"id": "user-123"}}}
		case "x:ApiKey/set":
			require.Equal(t, "user-123", args["accountId"])
			result = map[string]any{"created": map[string]any{"k1": map[string]string{"id": "key-456", "secret": "user-key-synthetic"}}}
		case "Mailbox/get":
			require.Equal(t, "user-123", args["accountId"])
			result = map[string]any{"list": []map[string]any{{"id": "inbox-id", "name": "Inbox", "role": "inbox", "totalEmails": 1, "unreadEmails": 1}}}
		case "Email/query":
			require.Equal(t, "user-123", args["accountId"])
			require.Equal(t, map[string]any{"inMailbox": "inbox-id"}, args["filter"])
			result = map[string]any{"ids": []string{"message-1"}}
		case "Email/get":
			require.Equal(t, []any{"message-1"}, args["ids"])
			result = map[string]any{"list": []map[string]any{{"id": "message-1", "threadId": "thread-1", "subject": "hello", "receivedAt": "2026-11-04T00:00:00Z", "preview": "body", "hasAttachment": true}}}
		default:
			t.Fatalf("Unexpected JMAP method %q", method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, result, "c1"}}})
	}))
	defer server.Close()
	client, err := NewStalwartJMAPClient(server.URL, server.Client())
	require.NoError(t, err)
	ctx := context.Background()
	admin := StalwartBasic("admin", "stage-secret")
	domainID, err := client.FindDomainID(ctx, admin, "example.test")
	require.NoError(t, err)
	require.Equal(t, "domain-123", domainID)
	created, err := client.CreateUser(ctx, admin, domainID, "reader", userID, 2<<30)
	require.NoError(t, err)
	require.Equal(t, "user-123", created.AccountID)
	keyID, secret, err := client.CreateUserAPIKey(ctx, admin, created.AccountID)
	require.NoError(t, err)
	require.Equal(t, "key-456", keyID)
	require.Equal(t, "user-key-synthetic", secret)
	session, err := client.Session(ctx, StalwartBearer(secret))
	require.NoError(t, err)
	require.Equal(t, "user-123", session.PrimaryAccounts[jmapMailCapability])
	folders, err := client.Mailboxes(ctx, StalwartBearer(secret), created.AccountID)
	require.NoError(t, err)
	require.Len(t, folders, 1)
	messages, err := client.ListEmails(ctx, StalwartBearer(secret), created.AccountID, folders[0].ID, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	require.Equal(t, "hello", messages[0].Subject)
	require.Equal(t, []string{"x:Domain/query", "x:Domain/get", "x:Account/set", "x:ApiKey/set", "Mailbox/get", "Email/query", "Email/get"}, methods)
}

func TestStalwartJMAPFailsClosedAndDoesNotLeakSecrets(t *testing.T) {
	secret := "STALWART_PRIVATE_API_KEY_TEST_MARKER"
	for _, tc := range []struct {
		name   string
		status int
		reply  string
	}{
		{"unauthorized", http.StatusUnauthorized, "denied: " + secret},
		{"unexpected_jmap_method", http.StatusOK, `{"methodResponses":[["Other/get",{},"c1"]]}`},
		{"method_error", http.StatusOK, `{"methodResponses":[["error",{"type":"forbidden","description":"` + secret + `"},"c1"]]}`},
		{"wrong_correlation", http.StatusOK, `{"methodResponses":[["Mailbox/get",{"list":[]},"wrong"]]}`},
		{"bad_json", http.StatusOK, "{garbled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer server.Close()
			client, err := NewStalwartJMAPClient(server.URL, nil)
			require.NoError(t, err)
			_, err = client.Mailboxes(context.Background(), StalwartBearer(secret), "staging-account")
			require.Error(t, err)
			require.NotContains(t, err.Error(), secret)
		})
	}
	client, err := NewStalwartJMAPClient("http://127.0.0.1:18080", nil)
	require.NoError(t, err)
	_, err = client.ListEmails(context.Background(), StalwartBearer(secret), "account", "inbox", 0)
	require.Error(t, err)
	_, err = client.ListEmails(context.Background(), StalwartBearer(secret), "account", "inbox", 101)
	require.Error(t, err)
}

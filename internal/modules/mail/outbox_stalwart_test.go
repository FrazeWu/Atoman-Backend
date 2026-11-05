package mail

import (
	"atoman/internal/model"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStalwartOutboxSubmissionUsesOnlyMappedUserAndNoBccHeader(t *testing.T) {
	db, queue, _, _, alice, bob, _ := mailOutboxFixture(t)
	const account = "stalwart-user-123"
	const bearer = "Bearer one-time-staging-secret-value"
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, bearer, r.Header.Get("Authorization"))
		if r.URL.Path == "/jmap/session" {
			require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"primaryAccounts": map[string]string{jmapMailCapability: account}}))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/jmap/upload/") {
			require.Equal(t, "POST", r.Method)
			require.Equal(t, "message/rfc822", r.Header.Get("Content-Type"))
			raw, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Contains(t, string(raw), "Subject:")
			require.NotContains(t, string(raw), "Bcc:")
			calls = append(calls, "blob-upload")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"accountId": account, "blobId": "synthetic-blob", "size": len(raw)})
			return
		}
		var req struct {
			Calls [][]json.RawMessage `json:"methodCalls"`
			Using []string            `json:"using"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		var method string
		require.NoError(t, json.Unmarshal(req.Calls[0][0], &method))
		var args map[string]any
		require.NoError(t, json.Unmarshal(req.Calls[0][1], &args))
		require.Equal(t, account, args["accountId"])
		calls = append(calls, method)
		var data any
		switch method {
		case "Mailbox/get":
			data = map[string]any{"list": []any{map[string]any{"id": "drafts", "role": "drafts"}, map[string]any{"id": "sent", "role": "sent"}}}
		case "Identity/get":
			require.Contains(t, req.Using, "urn:ietf:params:jmap:submission")
			data = map[string]any{"list": []any{map[string]string{"id": "identity-own", "email": "stage-alice@atoman.org"}}}
		case "Email/import":
			data = map[string]any{"created": map[string]any{"m1": map[string]string{"id": "imported-draft"}}}
		case "EmailSubmission/set":
			require.Contains(t, req.Using, "urn:ietf:params:jmap:submission")
			create := args["create"].(map[string]any)["s1"].(map[string]any)
			envelope := create["envelope"].(map[string]any)
			require.Equal(t, "stage-alice@atoman.org", envelope["mailFrom"].(map[string]any)["email"])
			recipients := envelope["rcptTo"].([]any)
			require.Len(t, recipients, 2)
			data = map[string]any{"created": map[string]any{"s1": map[string]string{"id": "submitted-id"}}}
		case "Email/set":
			data = map[string]any{"updated": map[string]any{"imported-draft": nil}}
		default:
			t.Fatalf("unexpected transport method %q", method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"methodResponses": []any{[]any{method, data, "c1"}}})
	}))
	defer upstream.Close()
	client, err := NewStalwartJMAPClient(upstream.URL, nil)
	require.NoError(t, err)
	transport := NewStalwartOutboxSubmitter(db, client, fixtureCredentialVault(t))
	input := composeFixture()
	queued, err := queue.Queue(context.Background(), alice, input)
	require.NoError(t, err)
	job := queued.ID
	require.NoError(t, db.Model(&model.MailPendingSend{}).Where("id = ?", job).Update("status", "dispatching").Error)
	payload, err := buildOutboxPayload("stage-alice@atoman.org", job, input, time.Now())
	require.NoError(t, err)
	require.Error(t, transport.Submit(context.Background(), bob.ID, account, job, payload), "other Atoman user cannot use sender key")
	require.Empty(t, calls)
	require.NoError(t, transport.Submit(context.Background(), alice.ID, account, job, payload))
	require.Equal(t, []string{"Mailbox/get", "Identity/get", "blob-upload", "Email/import", "EmailSubmission/set", "Email/set"}, calls)
	var receipt model.MailPendingSend
	require.NoError(t, db.Where("id = ?", job).Take(&receipt).Error)
	require.Equal(t, "imported-draft", receipt.ImportedEmailID)
	require.Equal(t, "submitted-id", receipt.SubmissionID)
	payload.Sender = "forged@atoman.org"
	require.Error(t, transport.Submit(context.Background(), alice.ID, account, job, payload))
}

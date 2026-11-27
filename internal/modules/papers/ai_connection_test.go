package papers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"atoman/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPaperAIConnectionTransport(t *testing.T) {
	const key = "synthetic-test-key"
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{200, `{"choices":[{"finish_reason":"stop","message":{"content":"OK"}}]}`, ""},
		{200, `{"choices":[{"finish_reason":"length","message":{"content":"OK"}}]}`, ""},
		{200, `{"choices":[{"finish_reason":"length","message":{"content":""}}]}`, "papers.ai_incomplete_response"},
		{200, `{"choices":[{"finish_reason":"stop","message":{"content":"synthetic-test-key"}}]}`, "papers.ai_invalid_response"},
		{200, "<html>not API</html>", "papers.ai_invalid_response"},
		{401, key, "papers.ai_provider_auth"}, {429, key, "papers.ai_provider_rate"}, {404, key, "papers.ai_chat_unsupported"}, {400, key, "papers.ai_chat_unsupported"}, {302, key, "papers.ai_provider_redirect"}, {502, key, "papers.ai_provider_server"},
	} {
		for _, base := range []string{"https://provider.example.net/v1", paperAIDefaultBase} {
			calls := 0
			client := &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "POST", r.Method)
				require.Equal(t, base+"/chat/completions", r.URL.String())
				require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
				var p map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&p))
				maxKey := "max_tokens"
				if base == paperAIDefaultBase {
					maxKey = "max_completion_tokens"
				}
				require.Equal(t, map[string]any{"model": "test-chat", "stream": false, maxKey: float64(128), "messages": []any{map[string]any{"role": "user", "content": "Reply with OK."}}}, p)
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			err := callPaperAIConnection(context.Background(), client, base, "test-chat", key)
			require.Equal(t, 1, calls)
			if tc.code == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.code)
				require.NotContains(t, err.Error(), key)
			}
		}
	}
}

func TestPaperAIConnectionOwnerConsentAndNoMutation(t *testing.T) {
	t.Setenv("PAPERS_AI_ENABLE", "true")
	t.Setenv("PAPERS_AI_CUSTOM_BASE_URLS_ENABLE", "true")
	f := newCompileHTTPFixture(t, false)
	const key = "synthetic-test-key"
	calls := 0
	f.svc.aiTest = func(_ context.Context, base, model, secret string) error {
		calls++
		require.Equal(t, "https://provider.example.net/v1", base)
		require.Equal(t, "chat-model", model)
		require.Equal(t, key, secret)
		return nil
	}
	in := paperAIConnectionInput{BaseURL: "https://provider.example.net/v1", Model: "chat-model", APIKey: key, Consent: true}
	route := f.base() + "/ai/connection-test"
	call := func(v paperAIConnectionInput, token string, status int) string {
		t.Helper()
		raw, err := json.Marshal(v)
		require.NoError(t, err)
		res := f.request("POST", route, string(raw), token)
		require.Equal(t, status, res.Code, res.Body.String())
		require.NotContains(t, res.Body.String(), key)
		require.Equal(t, "private, no-store", res.Header().Get("Cache-Control"))
		return res.Body.String()
	}
	require.Equal(t, 401, f.request("POST", route, "{}", "").Code)
	for _, role := range []string{"viewer", "editor"} {
		require.Equal(t, 200, f.request("PUT", f.base()+"/members/"+f.other.UUID.String(), `{"role":"`+role+`"}`, f.ownerToken).Code)
		call(in, f.otherToken, 404)
	}
	copy := in
	copy.Consent = false
	call(copy, f.ownerToken, 400)
	copy = in
	copy.BaseURL = "https://169.254.169.254/v1"
	call(copy, f.ownerToken, 400)
	copy = in
	copy.Model = key
	call(copy, f.ownerToken, 400)
	copy = in
	copy.APIKey = "bad"
	call(copy, f.ownerToken, 400)
	require.Equal(t, 400, f.request("POST", route, `{"base_url":"https://provider.example.net/v1","api_key":"synthetic-test-key","model":"chat-model","consent":true,"source":"not allowed"}`, f.ownerToken).Code)
	t.Setenv("PAPERS_AI_ENABLE", "false")
	call(in, f.ownerToken, 503)
	t.Setenv("PAPERS_AI_ENABLE", "true")
	require.Zero(t, calls)
	require.Contains(t, call(in, f.ownerToken, 200), `"connected":true`)
	require.Equal(t, 1, calls)
	var count int64
	require.NoError(t, f.db.Model(&model.AuditLog{}).Where("entity_id=? AND action IN ?", f.project.ID, paperAIEventActions).Count(&count).Error)
	require.Zero(t, count)
}

package papers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"atoman/internal/model"
	"github.com/stretchr/testify/require"
)

func TestPaperAICustomBasePolicy(t *testing.T) {
	t.Setenv("PAPERS_AI_CUSTOM_BASE_URLS_ENABLE", "true")
	for _, raw := range []string{"https://api.deepseek.com/v1/", "https://open.bigmodel.cn/api/paas/v4", "https://api.example.net", "https://api.openai.com/v1"} {
		base, ok := paperAIAllowedBase(raw)
		require.True(t, ok, raw)
		require.Equal(t, strings.TrimSuffix(raw, "/"), base)
	}
	for _, raw := range []string{"https://api.example.net/v1/chat/completions", "https://api.example.net/v1/models/"} {
		base, ok := paperAIAllowedBase(raw)
		require.True(t, ok)
		require.Equal(t, "https://api.example.net/v1", base)
	}
	for _, raw := range []string{"http://public.example.net/v1", "https://127.0.0.1/v1", "https://[::1]/v1", "https://169.254.169.254/v1", "https://router.local/v1", "https://metadata.google.internal/v1", "https://api.atoman.org/v1", "https://atoman.org/v1", "https://api.example.net:8443/v1", "https://api.example.net/v1?key=secret", "https://api.example.net/v1#token", "https://user:pass@api.example.net/v1", "https://api.example.net/%2e%2e/v1", "https://a..example.net/v1", "https://-a.example.net/v1", "https://api.example.net/" + strings.Repeat("a", 513)} {
		_, ok := paperAIAllowedBase(raw)
		require.False(t, ok, raw)
	}
	t.Setenv("PAPERS_AI_CUSTOM_BASE_URLS_ENABLE", "false")
	_, ok := paperAIAllowedBase("https://api.example.net/v1")
	require.False(t, ok)
	_, ok = paperAIAllowedBase(paperAIDefaultBase)
	require.True(t, ok)
	for _, key := range []string{"short", "synthetic key", "synthetic\rkey", "synthetic\u007fkey", "synthetic\nkey", "密钥synthetic"} {
		require.False(t, paperAIValidKey(key))
	}
	require.True(t, paperAIValidKey("synthetic-test-key"))
}
func TestPaperAIModelDiscoveryTransport(t *testing.T) {
	const key = "synthetic-test-key"
	const base = "https://api.example.net/v1"
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
		models     []string
	}{
		{"deduplicated", `{"data":[{"id":"z-chat","owned_by":"ignored"},{"id":"a/chat"},{"id":"z-chat"},{"id":"bad id"}]}`, 200, "", []string{"a/chat", "z-chat"}},
		{"empty", `{"data":[]}`, 200, "", []string{}},
		{"null", `{"data":null}`, 200, "papers.ai_invalid_models", nil},
		{"secret", `{"data":[{"id":"synthetic-test-key"}]}`, 200, "papers.ai_invalid_models", nil},
		{"escaped secret", `{"data":[{"id":"synthetic-\u0074est-key"}]}`, 200, "papers.ai_invalid_models", nil},
		{"auth", `{"error":"synthetic-test-key"}`, 401, "papers.ai_provider_auth", nil},
		{"unsupported", `{"error":"synthetic-test-key"}`, 404, "papers.ai_models_unsupported", nil},
		{"rate", `{}`, 429, "papers.ai_provider_rate", nil},
		{"redirect", `{}`, 302, "papers.ai_provider_redirect", nil},
		{"server", `{"error":"synthetic-test-key"}`, 502, "papers.ai_provider_server", nil},
		{"direct IDs", `["z-chat","a/chat","z-chat"]`, 200, "", []string{"a/chat", "z-chat"}},
		{"models envelope", `{"models":[{"id":"gateway/chat"}]}`, 200, "", []string{"gateway/chat"}},
		{"string data", `{"data":["chat-a","chat-b"]}`, 200, "", []string{"chat-a", "chat-b"}},
		{"secret in alternate envelope", `{"models":["synthetic-test-key"]}`, 200, "papers.ai_invalid_models", nil},
		{"oversized", strings.Repeat("x", (1<<20)+1), 200, "papers.ai_invalid_models", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "GET", r.Method)
				require.Equal(t, base+"/models", r.URL.String())
				require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
				require.Nil(t, r.Body)
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			result, err := fetchPaperAIModels(context.Background(), client, base, key)
			if tc.code != "" {
				require.ErrorContains(t, err, tc.code)
				require.NotContains(t, err.Error(), key)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.models, result.Models)
		})
	}
	var data []map[string]string
	for i := 0; i < 600; i++ {
		data = append(data, map[string]string{"id": fmt.Sprintf("model-%04d", i)})
	}
	body, err := json.Marshal(map[string]any{"data": data})
	require.NoError(t, err)
	client := &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
	result, err := fetchPaperAIModels(context.Background(), client, base, key)
	require.NoError(t, err)
	require.Len(t, result.Models, 512)
	require.True(t, result.Truncated)
}
func TestPaperAIModelDiscoveryOwnerAndConsent(t *testing.T) {
	t.Setenv("PAPERS_AI_ENABLE", "true")
	t.Setenv("PAPERS_AI_CUSTOM_BASE_URLS_ENABLE", "true")
	f := newCompileHTTPFixture(t, false)
	calls := 0
	f.svc.aiModels = func(_ context.Context, base, key string) (paperAIModelsResult, error) {
		calls++
		require.Equal(t, "https://provider.example.net/v1", base)
		require.Equal(t, "synthetic-test-key", key)
		return paperAIModelsResult{Models: []string{"provider/chat-model"}}, nil
	}
	in := paperAIModelsInput{BaseURL: "https://provider.example.net/v1/", APIKey: "synthetic-test-key", Consent: true}
	call := func(value paperAIModelsInput, token string, status int) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		res := f.request("POST", f.base()+"/ai/models", string(raw), token)
		require.Equal(t, status, res.Code, res.Body.String())
		require.NotContains(t, res.Body.String(), in.APIKey)
		require.Equal(t, "private, no-store", res.Header().Get("Cache-Control"))
		return res.Body.Bytes()
	}
	require.Equal(t, 401, f.request("POST", f.base()+"/ai/models", "{}", "").Code)
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
	copy.APIKey = "bad"
	call(copy, f.ownerToken, 400)
	t.Setenv("PAPERS_AI_CUSTOM_BASE_URLS_ENABLE", "false")
	call(in, f.ownerToken, 400)
	t.Setenv("PAPERS_AI_CUSTOM_BASE_URLS_ENABLE", "true")
	t.Setenv("PAPERS_AI_ENABLE", "false")
	call(in, f.ownerToken, 503)
	t.Setenv("PAPERS_AI_ENABLE", "true")
	require.Zero(t, calls)
	require.Contains(t, string(call(in, f.ownerToken, 200)), "provider/chat-model")
	require.Equal(t, 1, calls)
	var count int64
	require.NoError(t, f.db.Model(&model.AuditLog{}).Where("entity_id=? AND action IN ?", f.project.ID, paperAIEventActions).Count(&count).Error)
	require.Zero(t, count)
}

func TestPaperAIModelDiscoveryTransportDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{context.DeadlineExceeded, "papers.ai_provider_timeout"},
		{errPaperAIEndpointDNS, "papers.ai_provider_dns"},
		{errPaperAIEndpointDenied, "papers.ai_endpoint_denied"},
		{errPaperAIEndpointConnect, "papers.ai_unavailable"},
	} {
		client := &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) { return nil, tc.err })}
		_, err := fetchPaperAIModels(context.Background(), client, "https://api.example.net/v1", "synthetic-test-key")
		require.ErrorContains(t, err, tc.code)
		require.NotContains(t, err.Error(), "synthetic-test-key")
		require.NotContains(t, err.Error(), "api.example.net")
	}
}

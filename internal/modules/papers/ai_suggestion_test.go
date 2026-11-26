package papers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"atoman/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPaperAIEndpointPolicy(t *testing.T) {
	t.Setenv("PAPERS_AI_CUSTOM_BASE_URLS_ENABLE", "false")
	t.Setenv("PAPERS_AI_ALLOWED_BASE_URLS", "https://approved.example.net/v1/,http://private.example.net/v1,https://127.0.0.1/v1")
	require.Equal(t, []string{paperAIDefaultBase, "https://approved.example.net/v1"}, paperAIBases())
	for _, value := range []string{"http://api.openai.com/v1", "https://evil.example.net/v1", "https://api.openai.com:443/v1", "https://api.openai.com@evil.example.net/v1", "https://api.openai.com/v1?foo=bar", "https://api.openai.com/v1#secret", "https://api.openai.com/v1/../v1", "https://api.openai.com/%76%31", "https://127.0.0.1/v1", "https://[::1]/v1", "https://169.254.169.254/v1", "https://api.openai.com.evil.example/v1"} {
		_, ok := paperAIAllowedBase(value)
		require.False(t, ok, value)
	}
	for _, value := range []string{paperAIDefaultBase, "https://approved.example.net/v1/"} {
		_, ok := paperAIAllowedBase(value)
		require.True(t, ok, value)
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "0.1.1.1", "198.18.1.1", "192.0.2.1", "224.1.1.1", "240.1.1.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::a00:1", "2002:7f00:1::1", "2001:db8::1"} {
		require.False(t, paperAIPublicIP(netip.MustParseAddr(ip)), ip)
	}
	require.True(t, paperAIPublicIP(netip.MustParseAddr("8.8.8.8")))
	require.True(t, paperAIPublicIP(netip.MustParseAddr("2606:4700:4700::1111")))
	client := paperAIHTTPClient()
	require.ErrorIs(t, client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
	require.Nil(t, client.Transport.(*http.Transport).Proxy)
}

type aiRoundTripper func(*http.Request) (*http.Response, error)

func (f aiRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPaperAIProviderBoundedResponse(t *testing.T) {
	const syntheticKey = "synthetic-test-key"
	prompt := paperAIPrompt{Task: "revise", Instruction: "Revise", Source: paperAIContextText{Path: "main.tex", Text: "Original"}}
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"success", `{"choices":[{"finish_reason":"stop","message":{"content":"Replacement"}}],"usage":{"prompt_tokens":12,"completion_tokens":4}}`, 200, ""},
		{"unauthorized", `{"error":"synthetic-test-key"}`, 401, "papers.ai_provider_auth"},
		{"echoed credential", `{"choices":[{"finish_reason":"stop","message":{"content":"synthetic-test-key"}}]}`, 200, "papers.ai_invalid_response"},
		{"rate limited", "sensitive provider response", 429, "papers.ai_provider_rate"},
		{"redirect", "sensitive provider response", 302, "papers.ai_provider_rejected"},
		{"truncated", `{"choices":[{"finish_reason":"length","message":{"content":"Partial"}}]}`, 200, "papers.ai_incomplete_response"},
		{"code fences", `{"choices":[{"finish_reason":"stop","message":{"content":"` + "\x60\x60\x60" + `latex\ntext\n` + "\x60\x60\x60" + `"}}]}`, 200, "papers.ai_invalid_response"},
		{"oversize", strings.Repeat("x", 513<<10), 200, "papers.ai_invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: aiRoundTripper(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, paperAIDefaultBase+"/chat/completions", r.URL.String())
				require.Equal(t, "Bearer "+syntheticKey, r.Header.Get("Authorization"))
				require.Equal(t, "POST", r.Method)
				var payload map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				require.Equal(t, float64(4096), payload["max_completion_tokens"])
				require.False(t, payload["stream"].(bool))
				require.NotContains(t, payload, "tools")
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			got, err := callPaperAIWithClient(context.Background(), client, paperAIDefaultBase, "test-model", syntheticKey, prompt)
			if tc.want == "" {
				require.NoError(t, err)
				require.Equal(t, "Replacement", got.Text)
				require.EqualValues(t, 12, got.InputTokens)
				return
			}
			require.ErrorContains(t, err, tc.want)
			require.NotContains(t, err.Error(), syntheticKey)
			require.Empty(t, got.Text)
		})
	}
}
func TestPaperAIAdmission(t *testing.T) {
	uid := uuid.New()
	release, ok := takePaperAI(uid)
	require.True(t, ok)
	defer release()
	_, ok = takePaperAI(uid)
	require.False(t, ok)
	for i := 0; i < 3; i++ {
		release, ok := takePaperAI(uuid.New())
		require.True(t, ok)
		defer release()
	}
	_, ok = takePaperAI(uuid.New())
	require.False(t, ok)
}
func TestPaperAISuggestionOwnerConsentAndRevision(t *testing.T) {
	t.Setenv("PAPERS_AI_ENABLE", "true")
	f := newCompileHTTPFixture(t, false)
	base := f.base()
	fileID := f.project.MainFileID
	var original model.PaperFile
	require.NoError(t, f.db.Where("id=?", fileID).First(&original).Error)
	var contextFile model.PaperFile
	contextFile.ProjectID = f.project.ID
	contextFile.ID = uuid.New()
	contextFile.Path = "refs.bib"
	contextFile.Kind = "text"
	contextFile.Content = "Approved extra context"
	contextFile.Revision = 1
	require.NoError(t, f.db.Create(&contextFile).Error)
	calls := 0
	f.svc.aiCall = func(_ context.Context, base, modelName, key string, prompt paperAIPrompt) (paperAISuggestion, error) {
		calls++
		require.Equal(t, paperAIDefaultBase, base)
		require.Equal(t, "test-model", modelName)
		require.Equal(t, "synthetic-test-key", key)
		require.Equal(t, "Local unsaved selection", prompt.Source.Text)
		require.Equal(t, original.Path, prompt.Source.Path)
		require.Len(t, prompt.Context, 1)
		require.Equal(t, "refs.bib", prompt.Context[0].Path)
		require.Equal(t, "Approved extra context", prompt.Context[0].Text)
		return paperAISuggestion{Text: "Suggested replacement"}, nil
	}
	in := paperAIInput{BaseURL: paperAIDefaultBase, Model: "test-model", APIKey: "synthetic-test-key", FileID: fileID, BaseRevision: original.Revision, Source: "Local unsaved selection", Task: "revise", Instruction: "Polish", Context: []paperAIContextRef{{FileID: contextFile.ID, Revision: 1}}, Consent: true}
	request := func(input paperAIInput, token string, status int) {
		t.Helper()
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		res := f.request("POST", base+"/ai/suggestions", string(raw), token)
		require.Equal(t, status, res.Code, res.Body.String())
		require.Equal(t, "private, no-store", res.Header().Get("Cache-Control"))
		require.NotContains(t, res.Body.String(), input.APIKey)
		if status == 200 {
			require.Contains(t, res.Body.String(), "Suggested replacement")
		}
	}
	require.Equal(t, 401, f.request("GET", base+"/ai/capability", "", "").Code)
	require.Equal(t, 200, f.request("GET", base+"/ai/capability", "", f.ownerToken).Code)
	member := base + "/members/" + f.other.UUID.String()
	for _, role := range []string{"viewer", "editor"} {
		require.Equal(t, 200, f.request("PUT", member, `{"role":"`+role+`"}`, f.ownerToken).Code)
		request(in, f.otherToken, 404)
	}
	copy := in
	copy.Consent = false
	request(copy, f.ownerToken, 400)
	copy = in
	copy.BaseURL = "https://169.254.169.254/v1"
	request(copy, f.ownerToken, 400)
	copy = in
	copy.BaseRevision++
	request(copy, f.ownerToken, 409)
	copy = in
	copy.Context = []paperAIContextRef{{FileID: contextFile.ID, Revision: 99}}
	request(copy, f.ownerToken, 409)
	copy = in
	copy.Context = []paperAIContextRef{{FileID: uuid.New(), Revision: 1}}
	request(copy, f.ownerToken, 404)
	copy = in
	copy.Context = []paperAIContextRef{{FileID: fileID, Revision: original.Revision}}
	request(copy, f.ownerToken, 400)
	copy = in
	copy.Source = strings.Repeat("x", paperAISourceLimit+1)
	request(copy, f.ownerToken, 400)
	t.Setenv("PAPERS_AI_ENABLE", "false")
	request(in, f.ownerToken, 503)
	require.Zero(t, calls)
	t.Setenv("PAPERS_AI_ENABLE", "true")
	request(in, f.ownerToken, 200)
	require.Equal(t, 1, calls)
	var after model.PaperFile
	require.NoError(t, f.db.Where("id=?", fileID).First(&after).Error)
	require.Equal(t, original.Content, after.Content)
	require.Equal(t, original.Revision, after.Revision)
}

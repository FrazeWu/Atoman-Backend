package papers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
)

type paperAIConnectionInput struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key"`
	Consent bool   `json:"consent"`
}
type paperAIConnectionResult struct {
	Connected bool `json:"connected"`
}
type paperAIConnectionCaller func(context.Context, string, string, string) error

func (s *Service) testAIConnection(ctx context.Context, user authctx.CurrentUser, pid uuid.UUID, in paperAIConnectionInput) (paperAIConnectionResult, error) {
	empty := paperAIConnectionResult{}
	if err := s.checkAIOwner(ctx, user, pid); err != nil {
		return empty, err
	}
	if !paperAIEnabled() {
		return empty, paperAIOff()
	}
	if !in.Consent {
		return empty, apperr.BadRequest("papers.ai_consent_required", "Explicit consent to a potentially billable connection test is required")
	}
	base, ok := paperAIAllowedBase(in.BaseURL)
	if !ok {
		return empty, apperr.BadRequest("papers.ai_endpoint_denied", "AI provider endpoint must be an allowed public HTTPS base URL")
	}
	if !paperAIValidKey(in.APIKey) || !aiModelPattern.MatchString(in.Model) || strings.Contains(base, in.APIKey) || strings.Contains(in.Model, in.APIKey) {
		return empty, paperAIInvalid()
	}
	release, ok := takePaperAI(user.ID)
	if !ok {
		return empty, apperr.New(http.StatusTooManyRequests, "papers.ai_busy", "AI request capacity reached", nil)
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call := s.aiTest
	if call == nil {
		call = func(ctx context.Context, base, model, key string) error {
			return callPaperAIConnection(ctx, paperAIHTTPClient(), base, model, key)
		}
	}
	// Do not probe additional paths or retry potentially billable POSTs.
	if err := call(ctx, base, in.Model, in.APIKey); err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, paperAITransportError(err)
	}
	if err := s.checkAIOwner(ctx, user, pid); err != nil {
		return empty, err
	}
	return paperAIConnectionResult{Connected: true}, nil
}

func callPaperAIConnection(ctx context.Context, client *http.Client, base, model, key string) error {
	// The probe is independent of the project, user instruction, history, and
	// manuscript. Return only a boolean, never upstream text or credentials.
	payload := map[string]any{"model": model, "stream": false, "messages": []map[string]string{{"role": "user", "content": "Reply with OK."}}}
	if base == paperAIDefaultBase {
		payload["max_completion_tokens"] = 128
	} else {
		payload["max_tokens"] = 128
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return paperAIProviderError("papers.ai_invalid_request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return paperAIProviderError("papers.ai_unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return paperAITransportError(err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 200:
	case 401, 403:
		return paperAIProviderError("papers.ai_provider_auth")
	case 429:
		return paperAIProviderError("papers.ai_provider_rate")
	case 400, 404, 405, 422:
		return paperAIProviderError("papers.ai_chat_unsupported")
	default:
		if res.StatusCode >= 500 {
			return paperAIProviderError("papers.ai_provider_server")
		}
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			return paperAIProviderError("papers.ai_provider_redirect")
		}
		return paperAIProviderError("papers.ai_provider_rejected")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (128<<10)+1))
	if err != nil {
		if ctx.Err() != nil {
			return paperAITransportError(ctx.Err())
		}
		return paperAIProviderError("papers.ai_invalid_response")
	}
	if len(body) > 128<<10 {
		return paperAIProviderError("papers.ai_invalid_response")
	}
	var decoded struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &decoded) != nil || len(decoded.Choices) != 1 {
		return paperAIProviderError("papers.ai_invalid_response")
	}
	choice := decoded.Choices[0]
	if choice.FinishReason != "stop" && choice.FinishReason != "length" {
		return paperAIProviderError("papers.ai_incomplete_response")
	}
	text := choice.Message.Content
	if !paperAIValidText(text, paperAIResponseLimit) || strings.Contains(text, key) {
		return paperAIProviderError("papers.ai_invalid_response")
	}
	if strings.TrimSpace(text) == "" {
		return paperAIProviderError("papers.ai_incomplete_response")
	}
	return nil
}

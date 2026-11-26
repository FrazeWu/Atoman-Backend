package papers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
)

const paperAIModelsLimit = 512

type paperAIModelsInput struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Consent bool   `json:"consent"`
}
type paperAIModelsResult struct {
	Models    []string `json:"models"`
	Truncated bool     `json:"truncated"`
}
type paperAIModelCaller func(context.Context, string, string) (paperAIModelsResult, error)

func paperAIValidKey(key string) bool {
	if len(key) < 8 || len(key) > 2048 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}
func (s *Service) discoverAIModels(ctx context.Context, user authctx.CurrentUser, pid uuid.UUID, in paperAIModelsInput) (paperAIModelsResult, error) {
	empty := paperAIModelsResult{}
	if err := s.checkAIOwner(ctx, user, pid); err != nil {
		return empty, err
	}
	if !paperAIEnabled() {
		return empty, paperAIOff()
	}
	if !in.Consent {
		return empty, apperr.BadRequest("papers.ai_consent_required", "Explicit consent to contact this provider is required")
	}
	base, ok := paperAIAllowedBase(in.BaseURL)
	if !ok {
		return empty, apperr.BadRequest("papers.ai_endpoint_denied", "AI provider endpoint must be an allowed public HTTPS base URL")
	}
	if !paperAIValidKey(in.APIKey) || strings.Contains(base, in.APIKey) {
		return empty, paperAIInvalid()
	}
	release, ok := takePaperAI(user.ID)
	if !ok {
		return empty, apperr.New(http.StatusTooManyRequests, "papers.ai_busy", "AI request capacity reached", nil)
	}
	defer release()
	call := s.aiModels
	if call == nil {
		call = func(ctx context.Context, base, key string) (paperAIModelsResult, error) {
			return fetchPaperAIModels(ctx, paperAIHTTPClient(), base, key)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := call(ctx, base, in.APIKey)
	if err != nil {
		return empty, err
	}
	// Never return provider secrets, arbitrary provider metadata, or excessive IDs.
	if len(result.Models) > paperAIModelsLimit {
		return empty, paperAIProviderError("papers.ai_invalid_models")
	}
	for _, id := range result.Models {
		if !aiModelPattern.MatchString(id) || strings.Contains(id, in.APIKey) {
			return empty, paperAIProviderError("papers.ai_invalid_models")
		}
	}
	if err := s.checkAIOwner(ctx, user, pid); err != nil {
		return empty, err
	}
	return result, nil
}
func fetchPaperAIModels(ctx context.Context, client *http.Client, base, key string) (paperAIModelsResult, error) {
	empty := paperAIModelsResult{}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return empty, paperAIProviderError("papers.ai_unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return empty, paperAIProviderError("papers.ai_unavailable")
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return empty, paperAIProviderError("papers.ai_provider_auth")
	case http.StatusTooManyRequests:
		return empty, paperAIProviderError("papers.ai_provider_rate")
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return empty, paperAIProviderError("papers.ai_models_unsupported")
	default:
		return empty, paperAIProviderError("papers.ai_provider_rejected")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return empty, paperAIProviderError("papers.ai_invalid_models")
	}
	var data struct {
		Data *[]struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &data) != nil || data.Data == nil {
		return empty, paperAIProviderError("papers.ai_invalid_models")
	}
	ids := map[string]bool{}
	for _, item := range *data.Data {
		if strings.Contains(item.ID, key) {
			return empty, paperAIProviderError("papers.ai_invalid_models")
		}
		if aiModelPattern.MatchString(item.ID) {
			ids[item.ID] = true
		}
	}
	result := paperAIModelsResult{Models: make([]string, 0, len(ids))}
	for id := range ids {
		result.Models = append(result.Models, id)
	}
	sort.Strings(result.Models)
	if len(result.Models) > paperAIModelsLimit {
		result.Truncated = true
		result.Models = result.Models[:paperAIModelsLimit]
	}
	return result, nil
}

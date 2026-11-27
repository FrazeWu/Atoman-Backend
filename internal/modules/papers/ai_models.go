package papers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
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
	Models          []string `json:"models"`
	Truncated       bool     `json:"truncated"`
	ResolvedBaseURL string   `json:"resolved_base_url,omitempty"`
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
	result, err := probePaperAIModelBases(ctx, base, in.APIKey, call)
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

// Try a small, deterministic set on the same HTTPS origin. Preserve an
// explicitly supplied version or custom base when it already works. Do not
// follow redirects, guess another provider, or retry auth/rate/network errors.
func paperAIModelBases(base string) []string {
	u, err := url.Parse(base)
	if err != nil {
		return []string{base}
	}
	out := []string{base}
	add := func(candidate string) {
		if len(out) >= 4 {
			return
		}
		checked, ok := paperAIAllowedBase(candidate)
		if !ok {
			return
		}
		v, err := url.Parse(checked)
		if err != nil || v.Scheme != u.Scheme || v.Host != u.Host {
			return
		}
		for _, prior := range out {
			if prior == checked {
				return
			}
		}
		out = append(out, checked)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if u.Path == "" {
		add(base + "/v1")
		return out
	}
	// A pasted resource suffix after an explicit version is a path error,
	// but try the entered custom path first so existing gateways keep working.
	for i := len(parts) - 1; i >= 0; i-- {
		if aiVersionSegment.MatchString(parts[i]) {
			versionBase := "https://" + u.Host + "/" + strings.Join(parts[:i+1], "/")
			if i < len(parts)-1 {
				add(versionBase)
			}
			// Some providers use an unversioned endpoint. A mistakenly added
			// /v1 is removable, but never guess another numbered API version.
			if parts[i] == "v1" {
				add(strings.TrimSuffix(versionBase, "/v1"))
			}
			return out
		}
	}
	add(base + "/v1")
	return out
}

func probePaperAIModelBases(ctx context.Context, base, key string, call paperAIModelCaller) (paperAIModelsResult, error) {
	var last error
	for _, candidate := range paperAIModelBases(base) {
		if ctx.Err() != nil {
			return paperAIModelsResult{}, paperAITransportError(ctx.Err())
		}
		if strings.Contains(candidate, key) {
			return paperAIModelsResult{}, paperAIInvalid()
		}
		result, err := call(ctx, candidate, key)
		if ctx.Err() != nil {
			return paperAIModelsResult{}, paperAITransportError(ctx.Err())
		}
		if err == nil {
			result.ResolvedBaseURL = candidate
			return result, nil
		}
		last = err
		// HTML from a website fallback and missing endpoints indicate a path
		// mismatch. A malformed model list, auth failure, network error, etc.
		// must not trigger more credential-bearing requests.
		code := apperr.FromError(err).Code
		if code != "papers.ai_models_unsupported" && code != "papers.ai_models_wrong_path" {
			break
		}
	}
	return paperAIModelsResult{}, last
}

// Report stable, non-sensitive categories only. Never include the upstream URL,
// response body, certificate details, or raw transport error.
func paperAITransportError(err error) error {
	var timeout net.Error
	var dns *net.DNSError
	var tlsVerify *tls.CertificateVerificationError
	var unknownCA x509.UnknownAuthorityError
	var invalidCert x509.CertificateInvalidError
	var invalidHost x509.HostnameError
	switch {
	case errors.Is(err, errPaperAIEndpointDenied):
		return paperAIProviderError("papers.ai_endpoint_denied")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return paperAIProviderError("papers.ai_provider_timeout")
	case errors.Is(err, errPaperAIEndpointDNS), errors.As(err, &dns):
		return paperAIProviderError("papers.ai_provider_dns")
	case errors.As(err, &tlsVerify), errors.As(err, &unknownCA), errors.As(err, &invalidCert), errors.As(err, &invalidHost):
		return paperAIProviderError("papers.ai_provider_tls")
	default:
		return paperAIProviderError("papers.ai_unavailable")
	}
}

// Some OpenAI-compatible gateways return model IDs directly or under a models
// envelope. Read only explicit IDs (never arbitrary display metadata).
func decodePaperAIModelIDs(body []byte, key string) ([]string, bool) {
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Models json.RawMessage `json:"models"`
	}
	raw := json.RawMessage(body)
	if strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		if json.Unmarshal(body, &envelope) != nil {
			return nil, false
		}
		raw = envelope.Data
		if len(raw) == 0 {
			raw = envelope.Models
		}
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil || entries == nil {
		return nil, false
	}
	ids := map[string]bool{}
	for _, entry := range entries {
		var id string
		if len(entry) > 0 && entry[0] == '"' {
			if json.Unmarshal(entry, &id) != nil {
				return nil, false
			}
		} else {
			var item struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(entry, &item) != nil {
				return nil, false
			}
			id = item.ID
		}
		if strings.Contains(id, key) {
			return nil, false
		}
		if aiModelPattern.MatchString(id) {
			ids[id] = true
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, true
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
		return empty, paperAITransportError(err)
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
		if res.StatusCode >= 500 {
			return empty, paperAIProviderError("papers.ai_provider_server")
		}
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			return empty, paperAIProviderError("papers.ai_provider_redirect")
		}
		return empty, paperAIProviderError("papers.ai_provider_rejected")
	}
	mediaType, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if mediaType == "text/html" {
		return empty, paperAIProviderError("papers.ai_models_wrong_path")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return empty, paperAIProviderError("papers.ai_invalid_models")
	}
	// Web frontends often answer unknown API routes with a successful HTML
	// page. This is a path mismatch, never a model-list success.
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "<") {
		return empty, paperAIProviderError("papers.ai_models_wrong_path")
	}
	ids, ok := decodePaperAIModelIDs(body, key)
	if !ok {
		return empty, paperAIProviderError("papers.ai_invalid_models")
	}
	result := paperAIModelsResult{Models: ids}
	if len(result.Models) > paperAIModelsLimit {
		result.Truncated = true
		result.Models = result.Models[:paperAIModelsLimit]
	}
	return result, nil
}

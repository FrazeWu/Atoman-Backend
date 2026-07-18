package oauthprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestGitHubProviderUsesVerifiedEmailAndNumericSubject(t *testing.T) {
	var tokenForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token form: %v", err)
			}
			tokenForm = r.Form
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "github-access", "token_type": "bearer"})
		case "/user":
			if r.Header.Get("Authorization") != "Bearer github-access" {
				t.Fatalf("unexpected authorization header: %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 12345, "login": "octocat", "name": "Octo Cat", "avatar_url": "https://images.example/octo.png",
			})
		case "/user/emails":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"email": "unverified@example.com", "primary": false, "verified": false},
				{"email": "octo@example.com", "primary": true, "verified": true},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider, err := NewGitHubProvider(GitHubConfig{
		ClientID: "github-client", ClientSecret: "github-secret", RedirectURL: "https://api.example.com/api/v1/auth/oauth/github/callback",
	})
	if err != nil {
		t.Fatalf("new github provider: %v", err)
	}
	provider.tokenURL = server.URL + "/token"
	provider.apiURL = server.URL
	provider.httpClient = server.Client()

	authorizeURL, err := provider.AuthorizationURL(AuthorizationRequest{State: "state", CodeChallenge: "challenge"})
	if err != nil {
		t.Fatalf("authorization url: %v", err)
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorization url: %v", err)
	}
	if parsed.Host != "github.com" || parsed.Query().Get("scope") != "read:user user:email" || parsed.Query().Get("code_challenge") != "challenge" {
		t.Fatalf("unexpected authorization url: %s", authorizeURL)
	}

	profile, err := provider.Exchange(context.Background(), CallbackRequest{Code: "github-code", CodeVerifier: "verifier"})
	if err != nil {
		t.Fatalf("exchange github identity: %v", err)
	}
	if tokenForm.Get("code") != "github-code" || tokenForm.Get("code_verifier") != "verifier" {
		t.Fatalf("unexpected token form: %#v", tokenForm)
	}
	if profile.Issuer != "https://github.com" || profile.Subject != "12345" || profile.Email != "octo@example.com" || !profile.EmailVerified {
		t.Fatalf("unexpected github profile: %#v", profile)
	}
}

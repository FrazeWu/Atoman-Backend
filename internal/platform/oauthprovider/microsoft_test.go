package oauthprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestMicrosoftProviderValidatesTenantIssuerAndUsesPreferredUsername(t *testing.T) {
	var tokenForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token form: %v", err)
		}
		tokenForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": "signed-microsoft-token"})
	}))
	defer server.Close()

	verifier := &fakeIDTokenVerifier{claims: oidcIdentityClaims{
		Issuer: "https://login.microsoftonline.com/tenant-id/v2.0", Subject: "microsoft-subject",
		TenantID: "tenant-id", PreferredUsername: "person@example.com", Name: "Microsoft Person", Nonce: "nonce",
	}}
	provider, err := NewMicrosoftProvider(MicrosoftConfig{
		ClientID: "microsoft-client", ClientSecret: "microsoft-secret",
		RedirectURL: "https://api.example.com/api/v1/auth/oauth/microsoft/callback", Tenant: "common",
	})
	if err != nil {
		t.Fatalf("new microsoft provider: %v", err)
	}
	provider.tokenURL = server.URL
	provider.httpClient = server.Client()
	provider.verifier = verifier

	authorizeURL, err := provider.AuthorizationURL(AuthorizationRequest{
		State: "state", CodeChallenge: "challenge", Nonce: "nonce",
	})
	if err != nil {
		t.Fatalf("authorization url: %v", err)
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorization url: %v", err)
	}
	if parsed.Host != "login.microsoftonline.com" || parsed.Path != "/common/oauth2/v2.0/authorize" {
		t.Fatalf("unexpected microsoft authorization url: %s", authorizeURL)
	}
	if parsed.Query().Get("code_challenge") != "challenge" || parsed.Query().Get("nonce") != "nonce" {
		t.Fatalf("missing PKCE or nonce: %s", authorizeURL)
	}

	profile, err := provider.Exchange(context.Background(), CallbackRequest{
		Code: "microsoft-code", CodeVerifier: "verifier", NonceHash: hashNonceForTest("nonce"),
	})
	if err != nil {
		t.Fatalf("exchange microsoft identity: %v", err)
	}
	if tokenForm.Get("code_verifier") != "verifier" || verifier.token != "signed-microsoft-token" {
		t.Fatalf("unexpected microsoft exchange: form=%#v token=%q", tokenForm, verifier.token)
	}
	if profile.Issuer != "https://login.microsoftonline.com/tenant-id/v2.0" || profile.Subject != "microsoft-subject" {
		t.Fatalf("unexpected microsoft identity: %#v", profile)
	}
	if profile.Email != "person@example.com" || !profile.EmailVerified {
		t.Fatalf("expected trusted microsoft email: %#v", profile)
	}
}

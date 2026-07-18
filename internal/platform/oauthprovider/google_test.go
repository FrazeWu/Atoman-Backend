package oauthprovider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func hashNonceForTest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

type fakeIDTokenVerifier struct {
	claims oidcIdentityClaims
	err    error
	token  string
}

func (v *fakeIDTokenVerifier) Verify(_ context.Context, token string) (oidcIdentityClaims, error) {
	v.token = token
	return v.claims, v.err
}

func TestGoogleProviderBuildsAuthorizationURLAndExchangesIdentity(t *testing.T) {
	var tokenForm url.Values
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token form: %v", err)
		}
		tokenForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": "signed-google-token"})
	}))
	defer tokenServer.Close()

	verifier := &fakeIDTokenVerifier{claims: oidcIdentityClaims{
		Issuer: "https://accounts.google.com", Subject: "google-subject",
		Email: "person@example.com", EmailVerified: true,
		Name: "Person", Picture: "https://images.example/person.png", Nonce: "nonce-value",
	}}
	provider, err := NewGoogleProvider(GoogleConfig{
		ClientID: "google-client", ClientSecret: "google-secret", RedirectURL: "https://api.example.com/api/v1/auth/oauth/google/callback",
	})
	if err != nil {
		t.Fatalf("new google provider: %v", err)
	}
	provider.tokenURL = tokenServer.URL
	provider.httpClient = tokenServer.Client()
	provider.verifier = verifier

	authorizeURL, err := provider.AuthorizationURL(AuthorizationRequest{
		State: "state-value", CodeChallenge: "challenge-value", Nonce: "nonce-value",
	})
	if err != nil {
		t.Fatalf("authorization url: %v", err)
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorization url: %v", err)
	}
	query := parsed.Query()
	if parsed.Host != "accounts.google.com" || query.Get("client_id") != "google-client" || query.Get("state") != "state-value" {
		t.Fatalf("unexpected authorization url: %s", authorizeURL)
	}
	if query.Get("code_challenge") != "challenge-value" || query.Get("code_challenge_method") != "S256" || query.Get("nonce") != "nonce-value" {
		t.Fatalf("missing PKCE or nonce: %s", authorizeURL)
	}
	if !strings.Contains(query.Get("scope"), "openid") || !strings.Contains(query.Get("scope"), "email") {
		t.Fatalf("unexpected scopes: %q", query.Get("scope"))
	}

	profile, err := provider.Exchange(context.Background(), CallbackRequest{
		Code: "google-code", CodeVerifier: "verifier-value", NonceHash: hashNonceForTest("nonce-value"),
	})
	if err != nil {
		t.Fatalf("exchange google identity: %v", err)
	}
	if tokenForm.Get("code") != "google-code" || tokenForm.Get("code_verifier") != "verifier-value" {
		t.Fatalf("unexpected token exchange form: %#v", tokenForm)
	}
	if verifier.token != "signed-google-token" {
		t.Fatalf("unexpected verified token: %q", verifier.token)
	}
	if profile.Issuer != "https://accounts.google.com" || profile.Subject != "google-subject" || profile.Email != "person@example.com" || !profile.EmailVerified {
		t.Fatalf("unexpected profile: %#v", profile)
	}
}

package oauthprovider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestAppleProviderUsesFormPostAndSignedClientSecret(t *testing.T) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate apple key: %v", err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal apple key: %v", err)
	}
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey}))

	var tokenForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse token form: %v", err)
		}
		tokenForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": "signed-apple-token"})
	}))
	defer server.Close()

	verifier := &fakeIDTokenVerifier{claims: oidcIdentityClaims{
		Issuer: "https://appleid.apple.com", Subject: "apple-subject",
		Email: "relay@privaterelay.appleid.com", EmailVerified: true, Nonce: "nonce",
	}}
	provider, err := NewAppleProvider(AppleConfig{
		ClientID: "com.example.web", TeamID: "TEAM123", KeyID: "KEY123",
		PrivateKey: privateKeyPEM, RedirectURL: "https://api.example.com/api/v1/auth/oauth/apple/callback",
	})
	if err != nil {
		t.Fatalf("new apple provider: %v", err)
	}
	provider.tokenURL = server.URL
	provider.httpClient = server.Client()
	provider.verifier = verifier

	authorizeURL, err := provider.AuthorizationURL(AuthorizationRequest{State: "state", Nonce: "nonce"})
	if err != nil {
		t.Fatalf("authorization url: %v", err)
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("parse authorization url: %v", err)
	}
	if parsed.Host != "appleid.apple.com" || parsed.Query().Get("response_mode") != "form_post" || parsed.Query().Get("scope") != "name email" {
		t.Fatalf("unexpected apple authorization url: %s", authorizeURL)
	}

	profile, err := provider.Exchange(context.Background(), CallbackRequest{
		Code: "apple-code", NonceHash: hashNonceForTest("nonce"),
		RawUser: `{"name":{"firstName":"Ada","lastName":"Lovelace"},"email":"relay@privaterelay.appleid.com"}`,
	})
	if err != nil {
		t.Fatalf("exchange apple identity: %v", err)
	}
	if tokenForm.Get("client_id") != "com.example.web" || tokenForm.Get("code") != "apple-code" {
		t.Fatalf("unexpected apple token form: %#v", tokenForm)
	}
	clientSecret := tokenForm.Get("client_secret")
	parsedSecret, err := jwt.Parse(clientSecret, func(token *jwt.Token) (any, error) {
		return &privateKey.PublicKey, nil
	}, jwt.WithAudience("https://appleid.apple.com"), jwt.WithIssuer("TEAM123"))
	if err != nil || !parsedSecret.Valid {
		t.Fatalf("invalid apple client secret: %v", err)
	}
	if parsedSecret.Header["kid"] != "KEY123" {
		t.Fatalf("unexpected apple key id: %#v", parsedSecret.Header)
	}
	claims, ok := parsedSecret.Claims.(jwt.MapClaims)
	if !ok || claims["sub"] != "com.example.web" {
		t.Fatalf("unexpected apple client secret claims: %#v", parsedSecret.Claims)
	}
	if verifier.token != "signed-apple-token" || profile.Subject != "apple-subject" || profile.DisplayName != "Ada Lovelace" {
		t.Fatalf("unexpected apple profile: %#v", profile)
	}
}

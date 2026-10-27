package oauthprovider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOAuthProvidersUseBoundedHTTPClients(t *testing.T) {
	google, err := NewGoogleProvider(GoogleConfig{ClientID: "client", ClientSecret: "secret", RedirectURL: "https://example.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	microsoft, err := NewMicrosoftProvider(MicrosoftConfig{ClientID: "client", ClientSecret: "secret", RedirectURL: "https://example.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	github, err := NewGitHubProvider(GitHubConfig{ClientID: "client", ClientSecret: "secret", RedirectURL: "https://example.test/callback"})
	if err != nil {
		t.Fatal(err)
	}
	for name, client := range map[string]*http.Client{"google": google.httpClient, "microsoft": microsoft.httpClient, "github": github.httpClient, "jwks-factory": newOAuthHTTPClient()} {
		if client == http.DefaultClient || client.Timeout != oauthHTTPTimeout || client.Timeout <= 0 {
			t.Fatalf("%s does not use a bounded client", name)
		}
	}
}

func TestOAuthHTTPClientCancelsStalledProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	client := newOAuthHTTPClient()
	client.Timeout = 20 * time.Millisecond
	_, err := client.Get(server.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

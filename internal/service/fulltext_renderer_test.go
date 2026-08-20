package service

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchRenderedFullTextDisabled(t *testing.T) {
	t.Setenv("FULLTEXT_RENDERER_URL", "")
	body, attempted, err := fetchRenderedFullText("https://example.com/post")
	if err != nil || attempted || body != nil {
		t.Fatalf("body=%q attempted=%t err=%v", body, attempted, err)
	}
}

func TestFetchRenderedFullTextUsesConfiguredAuthenticatedRenderer(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	t.Cleanup(func() { resolveFullTextHostname = originalResolver })

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method=%s", request.Method)
		}
		if request.Header.Get("Authorization") != "Bearer renderer-secret" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		var payload fullTextRendererRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.URL != "https://example.com/post" {
			t.Fatalf("url=%q", payload.URL)
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(fullTextRendererResponse{HTML: "<html><body><article><p>rendered article body</p></article></body></html>"})
	}))
	t.Cleanup(server.Close)

	t.Setenv("FULLTEXT_RENDERER_URL", server.URL)
	t.Setenv("FULLTEXT_RENDERER_TOKEN", "renderer-secret")
	originalClient := fullTextRendererHTTPClient
	fullTextRendererHTTPClient = &http.Client{Timeout: time.Second}
	t.Cleanup(func() { fullTextRendererHTTPClient = originalClient })

	body, attempted, err := fetchRenderedFullText("https://example.com/post")
	if err != nil {
		t.Fatal(err)
	}
	if !attempted {
		t.Fatal("renderer should be attempted")
	}
	if string(body) != "<html><body><article><p>rendered article body</p></article></body></html>" {
		t.Fatalf("body=%q", body)
	}
}

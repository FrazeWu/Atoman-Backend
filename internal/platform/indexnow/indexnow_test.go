package indexnow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSubmitBatchesCanonicalSameHostURLs(t *testing.T) {
	var received submission
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Fatalf("content type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	submitter, err := New(Config{
		Key: "abcDEF-12345678", SiteURL: "https://www.atoman.org", Endpoint: server.URL,
	}, server.Client())
	if err != nil {
		t.Fatalf("new submitter: %v", err)
	}
	if err := submitter.Submit(context.Background(), []string{
		"/posts/post/2", "https://www.atoman.org/posts/post/1", "/posts/post/2",
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if received.Host != "www.atoman.org" || received.Key != "abcDEF-12345678" {
		t.Fatalf("unexpected identity: %#v", received)
	}
	if received.KeyLocation != "https://www.atoman.org/abcDEF-12345678.txt" {
		t.Fatalf("key location = %q", received.KeyLocation)
	}
	want := []string{"https://www.atoman.org/posts/post/1", "https://www.atoman.org/posts/post/2"}
	if len(received.URLList) != len(want) {
		t.Fatalf("URL list = %#v", received.URLList)
	}
	for index := range want {
		if received.URLList[index] != want[index] {
			t.Fatalf("URL list = %#v", received.URLList)
		}
	}
}

func TestSubmitRejectsForeignHost(t *testing.T) {
	submitter, err := New(Config{Key: "12345678", SiteURL: "https://www.atoman.org"}, nil)
	if err != nil {
		t.Fatalf("new submitter: %v", err)
	}
	if err := submitter.Submit(context.Background(), []string{"https://example.com/post"}); err == nil {
		t.Fatal("expected foreign host rejection")
	}
}

func TestNewRejectsInvalidKey(t *testing.T) {
	if _, err := New(Config{Key: "short", SiteURL: "https://www.atoman.org"}, nil); err == nil {
		t.Fatal("expected invalid key rejection")
	}
}

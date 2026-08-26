package service

import "testing"

func TestNormalizeFeedSourceURL(t *testing.T) {
	tests := map[string]string{
		"HTTP://WWW.Example.com/feed.xml/":             "https://example.com/feed.xml",
		" https://example.com/feed.xml?tag=go#latest ": "https://example.com/feed.xml?tag=go",
		"https://example.com/feed.xml/":                "https://example.com/feed.xml",
	}
	for input, want := range tests {
		if got := NormalizeFeedSourceURL(input); got != want {
			t.Fatalf("NormalizeFeedSourceURL(%q) = %q, want %q", input, got, want)
		}
	}
}

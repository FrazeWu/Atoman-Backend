package service

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestCalculateNextFullTextRetryAt(t *testing.T) {
	now := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: time.Hour},
		{attempt: 2, want: 6 * time.Hour},
		{attempt: 3, want: 24 * time.Hour},
		{attempt: 4, want: 72 * time.Hour},
	}

	for _, tc := range cases {
		got, terminal := CalculateNextFullTextRetryAt(now, tc.attempt)
		if terminal {
			t.Fatalf("attempt=%d should not be terminal", tc.attempt)
		}
		if got.Sub(now) != tc.want {
			t.Fatalf("attempt=%d retry=%s want=%s", tc.attempt, got.Sub(now), tc.want)
		}
	}

	_, terminal := CalculateNextFullTextRetryAt(now, 5)
	if !terminal {
		t.Fatal("attempt=5 should be terminal")
	}
}

func TestValidateFullTextTargetURL(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		switch host {
		case "allowed.example":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		case "blocked.example":
			return []net.IP{net.ParseIP("192.168.1.12")}, nil
		case "unresolved.example":
			return nil, errors.New("lookup failed")
		default:
			return nil, errors.New("unexpected host: " + host)
		}
	}
	defer func() {
		resolveFullTextHostname = originalResolver
	}()

	allowed := []string{
		"https://allowed.example/article",
		"http://allowed.example/post/1",
	}
	for _, raw := range allowed {
		if err := ValidateFullTextTargetURL(raw); err != nil {
			t.Fatalf("expected %s allowed, got %v", raw, err)
		}
	}

	blocked := []string{
		"/relative/path",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"http://localhost:8080/private",
		"http://127.0.0.1:3000/",
		"http://0.0.0.0/private",
		"http://[::]/private",
		"http://blocked.example/feed",
		"https://unresolved.example/article",
	}
	for _, raw := range blocked {
		if err := ValidateFullTextTargetURL(raw); err == nil {
			t.Fatalf("expected %s blocked", raw)
		}
	}
}

func TestIsFeedItemEligibleForFullText(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		if host == "allowed.example" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("unexpected host: " + host)
	}
	defer func() {
		resolveFullTextHostname = originalResolver
	}()

	source := model.FeedSource{
		SourceType:      "external_rss",
		FullTextEnabled: true,
	}
	blogItem := model.FeedItem{Link: "https://allowed.example/article"}
	if !IsFeedItemEligibleForFullText(source, blogItem) {
		t.Fatal("expected external blog item eligible for full text")
	}

	podcastItem := model.FeedItem{
		Link:          "https://allowed.example/episode",
		EnclosureURL:  "https://cdn.example.com/episode.mp3",
		EnclosureType: "audio/mpeg",
	}
	if IsFeedItemEligibleForFullText(source, podcastItem) {
		t.Fatal("expected podcast enclosure item excluded from full text")
	}

	internalSource := model.FeedSource{
		SourceType:      "internal_channel",
		FullTextEnabled: true,
	}
	if IsFeedItemEligibleForFullText(internalSource, blogItem) {
		t.Fatal("expected internal source excluded from full text")
	}
}

func TestInferFeedContentQualityReturnsFalseForExcerptLikeContent(t *testing.T) {
	content := "<p>short teaser only</p>"

	if inferFeedContentQuality(content) {
		t.Fatal("expected excerpt-like content to stay below fulltext threshold")
	}
}

func TestInferFeedContentQualityReturnsTrueForLongArticleLikeContent(t *testing.T) {
	content := "<p>" + strings.Repeat("complete article body. ", 30) + "</p>"

	if !inferFeedContentQuality(content) {
		t.Fatal("expected article-like content to cross fulltext threshold")
	}
}

func TestDefaultFullTextStatusForSourceSkipsCompleteFeedContent(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		if host == "allowed.example" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("unexpected host: " + host)
	}
	defer func() {
		resolveFullTextHostname = originalResolver
	}()

	source := model.FeedSource{
		SourceType:      "external_rss",
		FullTextEnabled: true,
	}
	item := model.FeedItem{
		Link:    "https://allowed.example/article",
		Summary: strings.Repeat("complete article body ", 20),
	}

	if status := defaultFullTextStatusForSource(source, item, true); status != FullTextStatusDisabled {
		t.Fatalf("status=%q", status)
	}
}

func TestDefaultFullTextStatusForSourceKeepsPendingForExcerptFeedContent(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		if host == "allowed.example" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("unexpected host: " + host)
	}
	defer func() {
		resolveFullTextHostname = originalResolver
	}()

	source := model.FeedSource{
		SourceType:      "external_rss",
		FullTextEnabled: true,
	}
	item := model.FeedItem{
		Link:    "https://allowed.example/article",
		Summary: "short teaser",
	}

	if status := defaultFullTextStatusForSource(source, item, false); status != FullTextStatusPending {
		t.Fatalf("status=%q", status)
	}
}

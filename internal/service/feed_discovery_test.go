package service

import "testing"

func TestExtractFeedCandidatesFromHTMLFindsAndRanksAlternateFeeds(t *testing.T) {
	html := `<html><head>
	<link rel="alternate" type="application/rss+xml" title="Comments Feed" href="/comments.xml">
	<link rel="stylesheet" href="/site.css">
	<link rel="alternate" type="application/rss+xml" title="Main Feed" href="https://example.com/feed.xml">
	<link rel="alternate" type="application/atom+xml" title="Updates" href="atom.xml">
	</head></html>`

	got := ExtractFeedCandidatesFromHTML("https://example.com/blog/post", html)

	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}
	if got[0].FeedURL != "https://example.com/feed.xml" {
		t.Fatalf("expected top-ranked main feed first, got %q", got[0].FeedURL)
	}
	if !got[0].IsDefault {
		t.Fatal("expected top-ranked candidate marked as default")
	}
	if got[1].FeedURL != "https://example.com/comments.xml" {
		t.Fatalf("expected root-relative URL to resolve, got %q", got[1].FeedURL)
	}
	if got[2].FeedURL != "https://example.com/blog/atom.xml" {
		t.Fatalf("expected path-relative URL to resolve, got %q", got[2].FeedURL)
	}
}

func TestExtractFeedCandidatesFromHTMLDeduplicatesDuplicateFeedLinks(t *testing.T) {
	html := `<html><head>
	<link rel="alternate" type="application/rss+xml" title="Main Feed" href="/feed.xml">
	<link rel="alternate" type="application/rss+xml" title="Main Feed Mirror" href="https://example.com/feed.xml">
	<link rel="alternate" type="application/atom+xml" title="Updates" href="/updates.atom">
	</head></html>`

	got := ExtractFeedCandidatesFromHTML("https://example.com/blog/post", html)

	if len(got) != 2 {
		t.Fatalf("expected duplicate feed links to be deduplicated down to 2 candidates, got %d", len(got))
	}
	if got[0].FeedURL != "https://example.com/feed.xml" {
		t.Fatalf("expected deduplicated main feed URL first, got %q", got[0].FeedURL)
	}
	if got[1].FeedURL != "https://example.com/updates.atom" {
		t.Fatalf("expected secondary candidate to remain after deduplication, got %q", got[1].FeedURL)
	}
}

func TestExtractFeedCandidatesFromHTMLDecodesHTMLEntities(t *testing.T) {
	html := `<html><head>
	<link rel="alternate" type="application/rss+xml" title="Main &amp; Updates" href="/feed?x=1&amp;y=2">
	</head></html>`

	got := ExtractFeedCandidatesFromHTML("https://example.com/blog/post", html)

	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	if got[0].Title != "Main & Updates" {
		t.Fatalf("expected HTML entity decoded title, got %q", got[0].Title)
	}
	if got[0].FeedURL != "https://example.com/feed?x=1&y=2" {
		t.Fatalf("expected HTML entity decoded href, got %q", got[0].FeedURL)
	}
}

func TestExtractFeedCandidatesFromHTMLIgnoresUnsupportedAlternateLinks(t *testing.T) {
	html := `<html><head>
	<link rel="alternate" type="text/html" title="Homepage" href="/">
	<link rel="alternate" type="application/json" title="JSON Feed" href="/feed.json">
	</head></html>`

	got := ExtractFeedCandidatesFromHTML("https://example.com/blog", html)

	if len(got) != 0 {
		t.Fatalf("expected no supported feed candidates, got %d", len(got))
	}
}

func TestRankDiscoveryCandidatesMarksBestCandidateAsDefault(t *testing.T) {
	candidates := []FeedDiscoveryCandidate{
		{Title: "Comments Feed", FeedURL: "https://example.com/comments.xml", Kind: "comments", Score: 10},
		{Title: "Main Feed", FeedURL: "https://example.com/feed.xml", Kind: "main", Score: 100},
	}

	got := RankDiscoveryCandidates(candidates)

	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	if !got[0].IsDefault {
		t.Fatal("expected top-ranked candidate marked as default")
	}
	if got[0].FeedURL != "https://example.com/feed.xml" {
		t.Fatalf("expected main feed first, got %q", got[0].FeedURL)
	}
	if got[1].IsDefault {
		t.Fatal("expected only top-ranked candidate marked as default")
	}
}

func TestRankDiscoveryCandidatesBreaksScoreTiesByFeedURL(t *testing.T) {
	candidates := []FeedDiscoveryCandidate{
		{Title: "Z Feed", FeedURL: "https://example.com/z.xml", Score: 100},
		{Title: "A Feed", FeedURL: "https://example.com/a.xml", Score: 100},
		{Title: "M Feed", FeedURL: "https://example.com/m.xml", Score: 100},
	}

	got := RankDiscoveryCandidates(candidates)

	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}

	wantOrder := []string{
		"https://example.com/a.xml",
		"https://example.com/m.xml",
		"https://example.com/z.xml",
	}
	for i, want := range wantOrder {
		if got[i].FeedURL != want {
			t.Fatalf("expected candidate %d feed_url %q, got %q", i, want, got[i].FeedURL)
		}
	}
	if !got[0].IsDefault {
		t.Fatal("expected first tie-broken candidate marked as default")
	}
	if got[1].IsDefault || got[2].IsDefault {
		t.Fatal("expected only first tie-broken candidate marked as default")
	}
}

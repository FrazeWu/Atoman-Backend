package service

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestFetchAndParseRSSParsesPodcastImages(t *testing.T) {
	originalClient := rssFetchHTTPClient
	rssFetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://feeds.example.com/podcast.xml" {
			return nil, errors.New("unexpected feed url: " + req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml; charset=utf-8"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
  xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
  <channel>
    <title>不明白播客</title>
    <itunes:image href="https://cdn.example.com/show-cover.jpg"/>
    <item>
      <title>第 1 期</title>
      <link>https://example.com/ep-1</link>
      <guid>ep-1</guid>
      <description>summary</description>
      <pubDate>Mon, 02 Jun 2026 08:00:00 +0000</pubDate>
      <enclosure url="https://cdn.example.com/ep-1.mp3" type="audio/mpeg"/>
      <itunes:image href="https://cdn.example.com/ep-1-cover.jpg"/>
    </item>
  </channel>
</rss>`)),
		}, nil
	})}
	defer func() {
		rssFetchHTTPClient = originalClient
	}()

	items, title, coverURL, err := FetchAndParseRSS("https://feeds.example.com/podcast.xml")
	if err != nil {
		t.Fatalf("FetchAndParseRSS returned error: %v", err)
	}
	if title != "不明白播客" {
		t.Fatalf("expected title 不明白播客, got %q", title)
	}
	if coverURL != "https://cdn.example.com/show-cover.jpg" {
		t.Fatalf("expected show cover URL, got %q", coverURL)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].ITunesImage.Href != "https://cdn.example.com/ep-1-cover.jpg" {
		t.Fatalf("expected item cover URL, got %q", items[0].ITunesImage.Href)
	}
}

func TestFetchAndParseRSSPrefersContentEncodedAndCreator(t *testing.T) {
	originalClient := rssFetchHTTPClient
	rssFetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml; charset=utf-8"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
	xmlns:content="http://purl.org/rss/1.0/modules/content/"
	xmlns:dc="http://purl.org/dc/elements/1.1/">
	<channel>
		<title>Example Feed</title>
		<item>
			<title>Entry One</title>
			<link>https://example.com/posts/1</link>
			<guid>post-1</guid>
			<description><![CDATA[<p>short teaser</p>]]></description>
			<content:encoded><![CDATA[<div><p>Long body for the article.</p><p>Second paragraph with more detail.</p></div>]]></content:encoded>
			<dc:creator>Alice</dc:creator>
			<pubDate>Mon, 02 Jun 2026 08:00:00 +0000</pubDate>
		</item>
	</channel>
</rss>`)),
		}, nil
	})}
	defer func() { rssFetchHTTPClient = originalClient }()

	items, title, coverURL, err := FetchAndParseRSS("https://example.com/feed.xml")
	if err != nil {
		t.Fatalf("FetchAndParseRSS returned error: %v", err)
	}
	if title != "Example Feed" {
		t.Fatalf("title=%q", title)
	}
	if coverURL != "" {
		t.Fatalf("coverURL=%q", coverURL)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	if !strings.Contains(items[0].Content, "Second paragraph") {
		t.Fatalf("expected content:encoded body, got %q", items[0].Content)
	}
	if items[0].Creator != "Alice" {
		t.Fatalf("creator=%q", items[0].Creator)
	}
}

func TestFetchAndParseRSSPrefersAtomAlternateLinkAndContent(t *testing.T) {
	originalClient := rssFetchHTTPClient
	rssFetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/atom+xml; charset=utf-8"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
	<title>Atom Feed</title>
	<entry>
		<title>Atom Entry</title>
		<id>tag:example.com,2026:1</id>
		<link rel="self" href="https://example.com/feed.atom"/>
		<link rel="alternate" href="https://example.com/posts/atom-entry"/>
		<updated>2026-06-02T08:00:00Z</updated>
		<author><name>Bob</name></author>
		<summary>short atom summary</summary>
		<content type="html">&lt;p&gt;Long atom content body.&lt;/p&gt;&lt;p&gt;More text.&lt;/p&gt;</content>
	</entry>
</feed>`)),
		}, nil
	})}
	defer func() { rssFetchHTTPClient = originalClient }()

	items, title, _, err := FetchAndParseRSS("https://example.com/feed.atom")
	if err != nil {
		t.Fatalf("FetchAndParseRSS returned error: %v", err)
	}
	if title != "Atom Feed" {
		t.Fatalf("title=%q", title)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	if items[0].Link != "https://example.com/posts/atom-entry" {
		t.Fatalf("link=%q", items[0].Link)
	}
	if !strings.Contains(items[0].Description, "Long atom content body") {
		t.Fatalf("description=%q", items[0].Description)
	}
	if items[0].Author != "Bob" {
		t.Fatalf("author=%q", items[0].Author)
	}
}

func TestNormalizeAtomEntryPrefersAlternateLinkAndContent(t *testing.T) {
	fallback := time.Date(2026, 6, 19, 8, 0, 0, 0, time.UTC)

	got := normalizeAtomEntry(ExtAtomEntry{
		Title:   " Atom Entry ",
		ID:      " tag:example.com,2026:1 ",
		Updated: "2026-06-02T08:00:00Z",
		Summary: "short atom summary",
		Content: " <p>Long atom content body.</p><p>More text.</p> ",
		Links: []ExtAtomLink{
			{Rel: "self", Href: "https://example.com/feed.atom"},
			{Rel: "alternate", Href: " https://example.com/posts/atom-entry "},
		},
		Author: ExtAtomAuthor{Name: " Bob "},
	}, "Feed Title", "", fallback)

	if got.Link != "https://example.com/posts/atom-entry" {
		t.Fatalf("link=%q", got.Link)
	}
	if got.Identifier != "tag:example.com,2026:1" {
		t.Fatalf("identifier=%q", got.Identifier)
	}
	if got.Author != "Bob" {
		t.Fatalf("author=%q", got.Author)
	}
	if !strings.Contains(got.ContentHTML, "Long atom content body") {
		t.Fatalf("content_html=%q", got.ContentHTML)
	}
	if got.SummaryText != "short atom summary" {
		t.Fatalf("summary_text=%q", got.SummaryText)
	}
	expectedPublishedAt := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	if !got.PublishedAt.Equal(expectedPublishedAt) {
		t.Fatalf("published_at=%s", got.PublishedAt.Format(time.RFC3339))
	}
}

func TestNormalizeRSSItemUsesDCDateAndImageFallbackChain(t *testing.T) {
	fallback := time.Date(2026, 6, 19, 8, 0, 0, 0, time.UTC)

	withChannelFallback := normalizeRSSItem(ExtRSSItem{
		Title:       "Entry One",
		Link:        "https://example.com/posts/1",
		GUID:        "post-1",
		Description: `<p>short teaser</p><img src="https://cdn.example.com/content-image.jpg" alt="cover">`,
		Content:     `<div><p>Long body</p><img src="https://cdn.example.com/content-image.jpg" alt="cover"></div>`,
		Creator:     "Alice",
		DCDate:      "2026-06-03T09:30:00Z",
	}, "Feed Title", "https://cdn.example.com/channel-cover.jpg", fallback)

	if withChannelFallback.ImageURL != "https://cdn.example.com/channel-cover.jpg" {
		t.Fatalf("channel image fallback=%q", withChannelFallback.ImageURL)
	}
	expectedPublishedAt := time.Date(2026, 6, 3, 9, 30, 0, 0, time.UTC)
	if !withChannelFallback.PublishedAt.Equal(expectedPublishedAt) {
		t.Fatalf("dc:date published_at=%s", withChannelFallback.PublishedAt.Format(time.RFC3339))
	}

	withContentFallback := normalizeRSSItem(ExtRSSItem{
		Title:       "Entry Two",
		Link:        "https://example.com/posts/2",
		GUID:        "post-2",
		Description: `<p>teaser</p>`,
		Content:     `<div><p>Body</p><img src="https://cdn.example.com/content-only.jpg" alt="hero"></div>`,
		Creator:     "Bob",
	}, "Feed Title", "", fallback)
	if withContentFallback.ImageURL != "https://cdn.example.com/content-only.jpg" {
		t.Fatalf("content image fallback=%q", withContentFallback.ImageURL)
	}

	withItemImage := normalizeRSSItem(ExtRSSItem{
		Title:       "Entry Three",
		Link:        "https://example.com/posts/3",
		GUID:        "post-3",
		Description: `<p>teaser</p>`,
		Content:     `<div><p>Body</p><img src="https://cdn.example.com/content-secondary.jpg" alt="hero"></div>`,
		ITunesImage: ExtRSSITunesImageRef{Href: "https://cdn.example.com/item-cover.jpg"},
		MediaThumbnail: ExtRSSMediaImageRef{
			URL: "https://cdn.example.com/media-thumb.jpg",
		},
	}, "Feed Title", "https://cdn.example.com/channel-cover.jpg", fallback)
	if withItemImage.ImageURL != "https://cdn.example.com/item-cover.jpg" {
		t.Fatalf("item image precedence=%q", withItemImage.ImageURL)
	}
}

func TestNormalizeRSSItemFallsBackToLaterValidDateWhenEarlierDateIsInvalid(t *testing.T) {
	fallback := time.Date(2026, 6, 19, 8, 0, 0, 0, time.UTC)

	got := normalizeRSSItem(ExtRSSItem{
		Title:       "Entry One",
		Link:        "https://example.com/posts/1",
		GUID:        "post-1",
		PubDate:     "not-a-date",
		DCDate:      "2026-06-03T09:30:00Z",
		Description: `<p>Body</p>`,
	}, "Feed Title", "", fallback)

	expectedPublishedAt := time.Date(2026, 6, 3, 9, 30, 0, 0, time.UTC)
	if !got.PublishedAt.Equal(expectedPublishedAt) {
		t.Fatalf("published_at=%s", got.PublishedAt.Format(time.RFC3339))
	}
}

func TestNormalizeAtomEntrySupportsDateAndAuthorFallbacks(t *testing.T) {
	fallback := time.Date(2026, 6, 19, 8, 0, 0, 0, time.UTC)

	modifiedEntry := normalizeAtomEntry(ExtAtomEntry{
		Title:    "Modified Entry",
		ID:       "modified-1",
		Modified: "2026-06-04T07:15:00Z",
		Content:  `<p>Body</p><img src="https://cdn.example.com/atom-content.jpg" alt="hero">`,
		Links: []ExtAtomLink{
			{Rel: "alternate", Href: "https://example.com/posts/modified"},
		},
		Author: ExtAtomAuthor{Email: "editor@example.com"},
	}, "Feed Title", "", fallback)
	if modifiedEntry.Author != "editor@example.com" {
		t.Fatalf("modified author=%q", modifiedEntry.Author)
	}
	expectedModified := time.Date(2026, 6, 4, 7, 15, 0, 0, time.UTC)
	if !modifiedEntry.PublishedAt.Equal(expectedModified) {
		t.Fatalf("modified published_at=%s", modifiedEntry.PublishedAt.Format(time.RFC3339))
	}
	if modifiedEntry.ImageURL != "https://cdn.example.com/atom-content.jpg" {
		t.Fatalf("modified image=%q", modifiedEntry.ImageURL)
	}

	issuedEntry := normalizeAtomEntry(ExtAtomEntry{
		Title:   "Issued Entry",
		ID:      "issued-1",
		Issued:  "2026-06-05T06:00:00Z",
		Summary: `<p>Summary only</p>`,
		Links: []ExtAtomLink{
			{Rel: "alternate", Href: "https://example.com/posts/issued"},
		},
		Author: ExtAtomAuthor{URI: "https://example.com/authors/carol"},
	}, "Feed Title", "https://cdn.example.com/feed-logo.png", fallback)
	if issuedEntry.Author != "https://example.com/authors/carol" {
		t.Fatalf("issued author=%q", issuedEntry.Author)
	}
	expectedIssued := time.Date(2026, 6, 5, 6, 0, 0, 0, time.UTC)
	if !issuedEntry.PublishedAt.Equal(expectedIssued) {
		t.Fatalf("issued published_at=%s", issuedEntry.PublishedAt.Format(time.RFC3339))
	}
	if issuedEntry.ImageURL != "https://cdn.example.com/feed-logo.png" {
		t.Fatalf("issued image=%q", issuedEntry.ImageURL)
	}
}

func TestBuildFeedItemSummaryStripsHTMLAndCollapsesWhitespace(t *testing.T) {
	summary := buildFeedItemSummary("<div><p>Hello <strong>world</strong>.</p><p>Second line.</p></div>")
	if summary != "Hello world. Second line." {
		t.Fatalf("summary=%q", summary)
	}
}

func TestBuildModelFeedItemUsesSharedDefaults(t *testing.T) {
	now := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	normalized := normalizedFeedItem{
		Title:       "Entry One",
		Link:        "https://example.com/posts/1",
		Identifier:  "post-1",
		Author:      "Alice",
		PublishedAt: now,
		ContentHTML: "<p>Hello world.</p><p>Second line.</p>",
	}

	item := buildModelFeedItem(model.FeedSource{SourceType: "external_rss"}, normalized, now)
	if item.Summary != "Hello world. Second line." {
		t.Fatalf("summary=%q", item.Summary)
	}
	if item.GUID != "post-1" {
		t.Fatalf("guid=%q", item.GUID)
	}
	if !item.PublishedAt.Equal(now) {
		t.Fatalf("published_at=%s", item.PublishedAt)
	}
}

func TestPersistNormalizedFeedItemUsesSharedPersistencePath(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "shared-persistence-source",
		RssURL:     "https://example.com/feed.xml",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	normalized := normalizedFeedItem{
		Title:       "Entry One",
		Link:        "https://example.com/posts/1",
		Identifier:  "post-1",
		Author:      "Alice",
		PublishedAt: now,
		ContentHTML: "<p>Hello world.</p><p>Second line.</p>",
	}

	if err := persistNormalizedFeedItem(db, source, normalized, now); err != nil {
		t.Fatalf("persistNormalizedFeedItem returned error: %v", err)
	}
	if err := persistNormalizedFeedItem(db, source, normalized, now); err != nil {
		t.Fatalf("persistNormalizedFeedItem second call returned error: %v", err)
	}

	var items []model.FeedItem
	if err := db.Where("feed_source_id = ?", source.ID).Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	if items[0].Summary != "Hello world. Second line." {
		t.Fatalf("summary=%q", items[0].Summary)
	}
	if items[0].GUID != "post-1" {
		t.Fatalf("guid=%q", items[0].GUID)
	}
}

func TestPersistNormalizedFeedItemSkipsEmptyIdentifierAndEmptyLink(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "shared-persistence-skip-source",
		RssURL:     "https://example.com/feed.xml",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	if err := persistNormalizedFeedItem(db, source, normalizedFeedItem{
		Title:       "Missing Identifier",
		Link:        "https://example.com/posts/missing-identifier",
		PublishedAt: now,
		ContentHTML: "<p>Hello world.</p>",
	}, now); err != nil {
		t.Fatalf("persistNormalizedFeedItem missing identifier returned error: %v", err)
	}
	if err := persistNormalizedFeedItem(db, source, normalizedFeedItem{
		Title:       "Missing Link",
		Identifier:  "post-2",
		PublishedAt: now,
		ContentHTML: "<p>Hello world.</p>",
	}, now); err != nil {
		t.Fatalf("persistNormalizedFeedItem missing link returned error: %v", err)
	}

	var count int64
	if err := db.Model(&model.FeedItem{}).Where("feed_source_id = ?", source.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("count=%d", count)
	}
}

func TestSyncSingleRSSPersistsNormalizedRSSContentAndMetadata(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "rss-normalization-source",
		RssURL:     "https://example.com/feed.xml",
		Title:      "Fallback Source Title",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}

	originalClient := rssFetchHTTPClient
	rssFetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml; charset=utf-8"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
	xmlns:content="http://purl.org/rss/1.0/modules/content/"
	xmlns:dc="http://purl.org/dc/elements/1.1/"
	xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
	<channel>
		<title>Example Feed</title>
		<itunes:image href="https://cdn.example.com/channel-cover.jpg"/>
		<item>
			<title>Entry One</title>
			<link>https://example.com/posts/1</link>
			<guid>post-1</guid>
			<description><![CDATA[<p>short teaser</p>]]></description>
			<content:encoded><![CDATA[<div><p>Long body for the article.</p><p>Second paragraph with more detail.</p></div>]]></content:encoded>
			<dc:creator>Alice</dc:creator>
			<dc:date>2026-06-02T08:00:00Z</dc:date>
			<itunes:duration>12:34</itunes:duration>
		</item>
	</channel>
</rss>`)),
		}, nil
	})}
	defer func() { rssFetchHTTPClient = originalClient }()

	SyncSingleRSS(db, source)

	var items []model.FeedItem
	if err := db.Where("feed_source_id = ?", source.ID).Find(&items).Error; err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d", len(items))
	}
	item := items[0]
	if item.GUID != "post-1" {
		t.Fatalf("guid=%q", item.GUID)
	}
	if item.Author != "Alice" {
		t.Fatalf("author=%q", item.Author)
	}
	if !strings.Contains(item.Summary, "Second paragraph with more detail.") {
		t.Fatalf("summary=%q", item.Summary)
	}
	if strings.Contains(item.Summary, "short teaser") {
		t.Fatalf("expected content:encoded to be preferred, summary=%q", item.Summary)
	}
	if item.Duration != "12:34" {
		t.Fatalf("duration=%q", item.Duration)
	}
	if item.ImageURL != "https://cdn.example.com/channel-cover.jpg" {
		t.Fatalf("image_url=%q", item.ImageURL)
	}
	expectedPublishedAt := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	if !item.PublishedAt.Equal(expectedPublishedAt) {
		t.Fatalf("published_at=%s", item.PublishedAt.Format(time.RFC3339))
	}
}

func TestSyncSingleRSSDoesNotMarkSourceFetchedWhenPersistenceFails(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{
		SourceType: "external_rss",
		Hash:       "rss-persistence-failure-source",
		RssURL:     "https://example.com/feed.xml",
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&model.FeedItem{}); err != nil {
		t.Fatal(err)
	}

	originalClient := rssFetchHTTPClient
	rssFetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml; charset=utf-8"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
	<channel>
		<title>Example Feed</title>
		<item>
			<title>Entry One</title>
			<link>https://example.com/posts/1</link>
			<guid>post-1</guid>
			<description><![CDATA[<p>Long body for the article.</p>]]></description>
			<pubDate>Mon, 02 Jun 2026 08:00:00 +0000</pubDate>
		</item>
	</channel>
</rss>`)),
		}, nil
	})}
	defer func() { rssFetchHTTPClient = originalClient }()

	SyncSingleRSS(db, source)

	var refreshed model.FeedSource
	if err := db.First(&refreshed, "id = ?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if refreshed.Title != "" {
		t.Fatalf("title=%q", refreshed.Title)
	}
	if refreshed.CoverURL != "" {
		t.Fatalf("cover_url=%q", refreshed.CoverURL)
	}
	if refreshed.LastFetchedAt != nil {
		t.Fatalf("last_fetched_at=%v", refreshed.LastFetchedAt)
	}
}

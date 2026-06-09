package service

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func openFullTextWorkerTestDB(t *testing.T) (*gorm.DB, error) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.FeedSource{}, &model.FeedItem{})
	return db, nil
}

func TestMarkFullTextFailureSchedulesRetry(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 30, 10, 0, 0, 0, time.UTC)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "worker-source-retry",
		RssURL:          "https://example.com/feed.xml",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{
		FeedSourceID:         source.ID,
		GUID:                 "worker-item-retry",
		Link:                 "https://example.com/post",
		FullTextStatus:       FullTextStatusFetching,
		FullTextAttemptCount: 1,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := markFullTextFailure(db, &item, &source, FullTextErrorRequestTimeout, "timeout", now); err != nil {
		t.Fatal(err)
	}

	var got model.FeedItem
	if err := db.First(&got, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.FullTextStatus != FullTextStatusRetry {
		t.Fatalf("status=%s", got.FullTextStatus)
	}
	if got.NextFullTextAttemptAt == nil || got.NextFullTextAttemptAt.Sub(now) != time.Hour {
		t.Fatalf("expected retry scheduled in 1 hour, got %+v", got.NextFullTextAttemptAt)
	}
	if got.FullTextErrorCode != FullTextErrorRequestTimeout {
		t.Fatalf("error_code=%s", got.FullTextErrorCode)
	}

	var gotSource model.FeedSource
	if err := db.First(&gotSource, "id = ?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotSource.FullTextFailureCount != 1 {
		t.Fatalf("failure_count=%d", gotSource.FullTextFailureCount)
	}
}

func TestMarkFullTextSuccessAndDisabled(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "worker-source-success",
		RssURL:          "https://example.com/feed.xml",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{
		FeedSourceID:         source.ID,
		GUID:                 "worker-item-success",
		Link:                 "https://example.com/post",
		FullTextStatus:       FullTextStatusFetching,
		FullTextAttemptCount: 2,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	result := FullTextResult{HTML: "<p>hello</p>", WordCount: 321}
	if err := markFullTextSuccess(db, &item, &source, result, now); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&item, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FullTextStatus != FullTextStatusSuccess || item.FullTextHTML != result.HTML || item.FullTextWordCount != result.WordCount {
		t.Fatalf("unexpected success state: %+v", item)
	}
	if item.FullTextAttemptCount != 2 {
		t.Fatalf("attempt_count=%d", item.FullTextAttemptCount)
	}

	previousFetchAt := now.Add(-2 * time.Hour)
	item2 := model.FeedItem{
		FeedSourceID:         source.ID,
		GUID:                 "worker-item-disabled",
		Link:                 "notaurl",
		FullTextStatus:       FullTextStatusFetching,
		FullTextHTML:         "<p>stale full text</p>",
		FullTextWordCount:    88,
		FullTextFetchedAt:    &previousFetchAt,
		FullTextErrorCode:    FullTextErrorInvalidURL,
		FullTextError:        "invalid",
		FullTextAttemptCount: 1,
	}
	if err := db.Create(&item2).Error; err != nil {
		t.Fatal(err)
	}
	if err := markFullTextDisabled(db, &item2); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&item2, "id = ?", item2.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item2.FullTextStatus != FullTextStatusDisabled {
		t.Fatalf("disabled status=%s", item2.FullTextStatus)
	}
	if item2.FullTextErrorCode != "" || item2.FullTextError != "" {
		t.Fatalf("expected disabled item error cleared, got code=%q message=%q", item2.FullTextErrorCode, item2.FullTextError)
	}
	if item2.FullTextHTML != "" || item2.FullTextWordCount != 0 || item2.FullTextFetchedAt != nil {
		t.Fatalf("expected disabled item full text cleared, got html=%q word_count=%d fetched_at=%v", item2.FullTextHTML, item2.FullTextWordCount, item2.FullTextFetchedAt)
	}
}

func TestRecoverStaleFullTextFetchesSchedulesRetryUsingPolicy(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 30, 15, 0, 0, 0, time.UTC)
	source := model.FeedSource{SourceType: "external_rss", Hash: "worker-source-stale", RssURL: "https://example.com/feed.xml", FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	lastAttempt := now.Add(-fullTextStaleFetchAfter - time.Minute)
	item := model.FeedItem{
		FeedSourceID:          source.ID,
		GUID:                  "worker-item-stale",
		Link:                  "https://example.com/post",
		FullTextStatus:        FullTextStatusFetching,
		FullTextAttemptCount:  2,
		LastFullTextAttemptAt: &lastAttempt,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := recoverStaleFullTextFetches(db, now); err != nil {
		t.Fatal(err)
	}

	var got model.FeedItem
	if err := db.First(&got, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.FullTextStatus != FullTextStatusRetry {
		t.Fatalf("status=%s", got.FullTextStatus)
	}
	if got.NextFullTextAttemptAt == nil || got.NextFullTextAttemptAt.Sub(now) != 6*time.Hour {
		t.Fatalf("expected retry scheduled in 6 hours, got %+v", got.NextFullTextAttemptAt)
	}
	if got.FullTextErrorCode != FullTextErrorRequestTimeout {
		t.Fatalf("error_code=%s", got.FullTextErrorCode)
	}

	var gotSource model.FeedSource
	if err := db.First(&gotSource, "id = ?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if gotSource.FullTextFailureCount != 1 {
		t.Fatalf("failure_count=%d", gotSource.FullTextFailureCount)
	}
	if gotSource.FullTextLastFailureAt == nil || !gotSource.FullTextLastFailureAt.Equal(now) {
		t.Fatalf("last_failure_at=%+v", gotSource.FullTextLastFailureAt)
	}
	if gotSource.FullTextLastErrorCode != FullTextErrorRequestTimeout {
		t.Fatalf("last_error_code=%s", gotSource.FullTextLastErrorCode)
	}
	if gotSource.FullTextLastError != "stale full text fetch recovered" {
		t.Fatalf("last_error=%s", gotSource.FullTextLastError)
	}
}

func TestProcessFullTextItemMarksDisabledWhenSourceDisabled(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 30, 16, 0, 0, 0, time.UTC)
	source := model.FeedSource{SourceType: "external_rss", Hash: "worker-source-disabled", RssURL: "https://example.com/feed.xml", FullTextEnabled: false}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "worker-item-disabled-source", Link: "https://example.com/post", FullTextStatus: FullTextStatusFetching}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := processFullTextItem(db, &item, &source, now); err != nil {
		t.Fatal(err)
	}

	var got model.FeedItem
	if err := db.First(&got, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.FullTextStatus != FullTextStatusDisabled {
		t.Fatalf("status=%s", got.FullTextStatus)
	}
	if got.FullTextErrorCode != "" || got.FullTextError != "" {
		t.Fatalf("expected disabled source to clear error semantics, got code=%q message=%q", got.FullTextErrorCode, got.FullTextError)
	}
}

func TestProcessFullTextItemMarksTooManyRedirects(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		if host == "example.com" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("unexpected host: " + host)
	}
	defer func() {
		resolveFullTextHostname = originalResolver
	}()

	originalClient := fullTextHTTPClient
	fullTextHTTPClient = &http.Client{Timeout: time.Second, Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: req.Method, URL: req.URL.String(), Err: errors.New(fullTextRedirectLimitMessage)}
	})}
	defer func() {
		fullTextHTTPClient = originalClient
	}()

	now := time.Date(2026, 5, 30, 17, 0, 0, 0, time.UTC)
	source := model.FeedSource{SourceType: "external_rss", Hash: "worker-source-redirects", RssURL: "https://example.com/feed.xml", FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "worker-item-redirects", Link: "https://example.com/post", FullTextStatus: FullTextStatusFetching, FullTextAttemptCount: 1}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	if err := processFullTextItem(db, &item, &source, now); err != nil {
		t.Fatal(err)
	}

	var got model.FeedItem
	if err := db.First(&got, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.FullTextStatus != FullTextStatusRetry {
		t.Fatalf("status=%s", got.FullTextStatus)
	}
	if got.FullTextErrorCode != FullTextErrorTooManyRedirects {
		t.Fatalf("error_code=%s", got.FullTextErrorCode)
	}
}

func TestClaimNextFullTextItemReturnsFalseWhenQueueEmpty(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 30, 18, 0, 0, 0, time.UTC)
	claimed, source, ok, err := claimNextFullTextItem(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected no claim, got item=%s source=%s", claimed.ID, source.ID)
	}
}

func TestClaimNextFullTextItemDoesNotDoubleClaim(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 5, 30, 18, 0, 0, 0, time.UTC)
	source := model.FeedSource{SourceType: "external_rss", Hash: "worker-source-claim", RssURL: "https://example.com/feed.xml", FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{FeedSourceID: source.ID, GUID: "worker-item-claim", Link: "https://example.com/post", FullTextStatus: FullTextStatusPending}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	const workers = 2
	results := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			claimed, _, ok, err := claimNextFullTextItem(db, now)
			if err != nil {
				errs <- err
				return
			}
			if ok {
				results <- claimed.ID.String()
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var claimedIDs []string
	for id := range results {
		claimedIDs = append(claimedIDs, id)
	}
	if len(claimedIDs) != 1 {
		t.Fatalf("claimed_ids=%v", claimedIDs)
	}
	if claimedIDs[0] != item.ID.String() {
		t.Fatalf("claimed_id=%s want=%s", claimedIDs[0], item.ID.String())
	}

	var got model.FeedItem
	if err := db.First(&got, "id = ?", item.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.FullTextStatus != FullTextStatusFetching {
		t.Fatalf("status=%s", got.FullTextStatus)
	}
	if got.FullTextAttemptCount != 1 {
		t.Fatalf("attempt_count=%d", got.FullTextAttemptCount)
	}
}

func TestClaimNextFullTextItemSkipsPodcastEnclosures(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	source := model.FeedSource{SourceType: "external_rss", Hash: "worker-source-podcast", RssURL: "https://example.com/podcast.xml", FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	item := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "worker-podcast-item",
		Title:          "Podcast Episode",
		Link:           "https://example.com/episode",
		EnclosureURL:   "https://cdn.example.com/episode.mp3",
		EnclosureType:  "audio/mpeg",
		FullTextStatus: FullTextStatusPending,
		PublishedAt:    now,
		FetchedAt:      now,
	}
	if err := db.Create(&item).Error; err != nil {
		t.Fatal(err)
	}

	claimed, source, ok, err := claimNextFullTextItem(db, now)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected podcast item skipped, got item=%s source=%s", claimed.ID, source.ID)
	}
}

func TestSyncSingleRSSSetsInitialFullTextStatus(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		if host == "example.com" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("unexpected host: " + host)
	}
	defer func() {
		resolveFullTextHostname = originalResolver
	}()

	originalClient := rssFetchHTTPClient
	rssFetchHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://feeds.example.com/rss.xml" {
			return nil, errors.New("unexpected feed url: " + req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/rss+xml"}},
			Body: io.NopCloser(strings.NewReader(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Example Feed</title><item><title>Post</title><link>https://example.com/post</link><guid>guid-1</guid><description>Summary</description></item></channel></rss>`)),
		}, nil
	})}
	defer func() {
		rssFetchHTTPClient = originalClient
	}()

	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{SourceType: "external_rss", Hash: "worker-source-rss", RssURL: "https://feeds.example.com/rss.xml", FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}

	SyncSingleRSS(db, source)

	var item model.FeedItem
	if err := db.First(&item, "feed_source_id = ?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FullTextStatus != FullTextStatusPending {
		t.Fatalf("status=%s", item.FullTextStatus)
	}
}

func TestSyncSingleRSSFailureDoesNotMutateSourceState(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "worker-source-sync-failure",
		RssURL:          "http://127.0.0.1:1/unreachable.xml",
		Title:           "Original Title",
		CoverURL:        "https://example.com/original.png",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}

	beforeFetchedAt := source.LastFetchedAt
	SyncSingleRSS(db, source)

	var got model.FeedSource
	if err := db.First(&got, "id = ?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.LastFetchedAt != beforeFetchedAt {
		t.Fatalf("expected last_fetched_at unchanged on sync failure, got %v want %v", got.LastFetchedAt, beforeFetchedAt)
	}
	if got.Title != source.Title {
		t.Fatalf("expected title unchanged on sync failure, got %q want %q", got.Title, source.Title)
	}
	if got.CoverURL != source.CoverURL {
		t.Fatalf("expected cover_url unchanged on sync failure, got %q want %q", got.CoverURL, source.CoverURL)
	}
}

func TestSyncSingleRSSDisablesPodcastFullTextStatus(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(host string) ([]net.IP, error) {
		if host == "example.com" {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("unexpected host: " + host)
	}
	defer func() {
		resolveFullTextHostname = originalResolver
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Podcast Feed</title><item><title>Episode</title><link>https://example.com/episode</link><guid>episode-1</guid><description>Audio summary</description><enclosure url="https://cdn.example.com/episode.mp3" type="audio/mpeg" /></item></channel></rss>`))
	}))
	defer server.Close()

	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{SourceType: "external_rss", Hash: "worker-source-podcast-rss", RssURL: server.URL, FullTextEnabled: true}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}

	SyncSingleRSS(db, source)

	var item model.FeedItem
	if err := db.First(&item, "feed_source_id = ?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if item.FullTextStatus != FullTextStatusDisabled {
		t.Fatalf("status=%s", item.FullTextStatus)
	}
}

func TestSyncSingleRSSRecordsSourceFailureState(t *testing.T) {
	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}

	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "worker-source-sync-failure",
		RssURL:          "http://127.0.0.1:1/unreachable.xml",
		Title:           "Original Title",
		CoverURL:        "https://example.com/original.png",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}

	beforeFetchedAt := source.LastFetchedAt
	SyncSingleRSS(db, source)

	var got model.FeedSource
	if err := db.First(&got, "id = ?", source.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.LastFetchedAt != beforeFetchedAt {
		t.Fatalf("expected last_fetched_at unchanged on sync failure, got %v want %v", got.LastFetchedAt, beforeFetchedAt)
	}
	if got.Title != source.Title {
		t.Fatalf("expected title unchanged on sync failure, got %q want %q", got.Title, source.Title)
	}
	if got.CoverURL != source.CoverURL {
		t.Fatalf("expected cover_url unchanged on sync failure, got %q want %q", got.CoverURL, source.CoverURL)
	}
}

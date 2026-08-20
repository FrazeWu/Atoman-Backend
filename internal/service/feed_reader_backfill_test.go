package service

import (
	"net"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestBackfillFeedReaderContentRebuildsStoredCandidatesAndRequeuesMissing(t *testing.T) {
	originalResolver := resolveFullTextHostname
	resolveFullTextHostname = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	t.Cleanup(func() { resolveFullTextHostname = originalResolver })

	db, err := openFullTextWorkerTestDB(t)
	if err != nil {
		t.Fatal(err)
	}
	source := model.FeedSource{
		SourceType:      "external_rss",
		Hash:            "reader-backfill-source",
		RssURL:          "https://example.com/feed.xml",
		FullTextEnabled: true,
	}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	stored := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "stored-candidate",
		Link:           "https://example.com/stored",
		FullTextStatus: FullTextStatusSuccess,
		FullTextHTML:   `<article><p>` + strings.Repeat("Stored article sentence. ", 20) + `</p></article>`,
		PublishedAt:    now,
		FetchedAt:      now,
	}
	missing := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "missing-candidate",
		Link:           "https://example.com/missing",
		FullTextStatus: FullTextStatusFailed,
		PublishedAt:    now.Add(-time.Minute),
		FetchedAt:      now,
	}
	podcast := model.FeedItem{
		FeedSourceID:   source.ID,
		GUID:           "podcast-candidate",
		Link:           "https://example.com/podcast",
		EnclosureURL:   "https://example.com/podcast.mp3",
		EnclosureType:  "audio/mpeg",
		FullTextStatus: FullTextStatusDisabled,
		PublishedAt:    now.Add(-2 * time.Minute),
		FetchedAt:      now,
	}
	if err := db.Create(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&missing).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&podcast).Error; err != nil {
		t.Fatal(err)
	}

	result, err := BackfillFeedReaderContent(db, FeedReaderBackfillOptions{Limit: 10, Apply: true, Requeue: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.Requeued != 1 || result.Skipped != 1 {
		t.Fatalf("result=%+v", result)
	}
	if err := db.First(&stored, "id = ?", stored.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ReaderHTML == "" || stored.ReaderSource != ReaderSourcePage || stored.ReaderVersion != ReaderVersionCurrent {
		t.Fatalf("stored=%+v", stored)
	}
	if err := db.First(&missing, "id = ?", missing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if missing.FullTextStatus != FullTextStatusPending || missing.FullTextAttemptCount != 0 {
		t.Fatalf("missing=%+v", missing)
	}
	if err := db.First(&podcast, "id = ?", podcast.ID).Error; err != nil {
		t.Fatal(err)
	}
	if podcast.FullTextStatus != FullTextStatusDisabled || podcast.ReaderVersion != ReaderVersionCurrent {
		t.Fatalf("podcast status=%q reader_version=%d", podcast.FullTextStatus, podcast.ReaderVersion)
	}
	secondPass, err := BackfillFeedReaderContent(db, FeedReaderBackfillOptions{Limit: 10, Apply: true, Requeue: true})
	if err != nil {
		t.Fatal(err)
	}
	if secondPass.Scanned != 0 {
		t.Fatalf("second pass should advance past evaluated items: %+v", secondPass)
	}
}

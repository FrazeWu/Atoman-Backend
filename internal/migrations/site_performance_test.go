package migrations

import (
	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSitePerformanceIndexesAreIdempotent(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.BookWork{}, &model.BookPerson{}, &model.FeedSourceDiagnostic{}, &model.FeedItem{}, &model.Subscription{})
	for range 2 {
		require.NoError(t, RunSitePerformanceIndexes(db))
	}
	for _, index := range []string{"idx_book_works_title_trgm", "idx_book_works_original_title_trgm", "idx_book_works_subtitle_trgm", "idx_book_people_name_trgm", "idx_feed_diagnostics_created", "idx_feed_diagnostics_source_time", "idx_subscriptions_source_live", "idx_feed_items_fulltext_source_ready"} {
		require.True(t, db.Migrator().HasIndex(indexTable(index), index), index)
	}
}

func indexTable(index string) string {
	switch index {
	case "idx_book_people_name_trgm":
		return "book_people"
	case "idx_feed_diagnostics_created", "idx_feed_diagnostics_source_time":
		return "feed_source_diagnostics"
	case "idx_subscriptions_source_live":
		return "subscriptions"
	case "idx_feed_items_fulltext_source_ready":
		return "feed_items"
	default:
		return "book_works"
	}
}

package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// RunFeedFullTextSearchIndexes adds the feed-item document used by subscribed
// full-text search. It does not enable the dedup snapshot read path.
func RunFeedFullTextSearchIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}
	operatorClass, err := pgTrgmOperatorClass(db)
	if err != nil {
		return err
	}
	for _, statement := range feedFullTextSearchStatements(operatorClass) {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create feed full-text search index: %w", err)
		}
	}
	return nil
}

func feedFullTextSearchStatements(operatorClass string) []string {
	return []string{
		`CREATE OR REPLACE FUNCTION public.atoman_feed_search_document_v1(
			title text, summary text, author text, reader_html text, full_text_html text, feed_content_html text
		) RETURNS text
		LANGUAGE sql
		IMMUTABLE
		PARALLEL SAFE
		AS $fn$
			SELECT lower(regexp_replace(concat_ws(' ',
				COALESCE(title, ''),
				COALESCE(summary, ''),
				COALESCE(author, ''),
				COALESCE(reader_html, ''),
				COALESCE(full_text_html, ''),
				COALESCE(feed_content_html, '')
			), '<[^>]+>', ' ', 'g'))
		$fn$`,
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_feed_items_search_document_trgm_v1
			ON feed_items USING GIN (public.atoman_feed_search_document_v1(title, summary, author, reader_html, full_text_html, feed_content_html) %s)
			WHERE deleted_at IS NULL`, operatorClass),
	}
}

package migrations

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// RunBlogSearchIndexes adds trigram indexes for the case-insensitive substring
// search used by the public post list.
func RunBlogSearchIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}
	if !db.Migrator().HasTable("content_entries") || !db.Migrator().HasTable("content_blog_extensions") {
		return nil
	}

	operatorClass, err := pgTrgmOperatorClass(db)
	if err != nil {
		return err
	}
	for _, statement := range blogSearchIndexStatements(operatorClass)[1:] {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create canonical blog search index: %w", err)
		}
	}
	return nil
}

func blogSearchIndexStatements(operatorClass ...string) []string {
	trigramOperatorClass := "gin_trgm_ops"
	if len(operatorClass) > 0 {
		trigramOperatorClass = operatorClass[0]
	}
	return []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public`,
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_entries_blog_title_trgm
			ON content_entries USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL AND kind = 'blog' AND status = 'published' AND COALESCE(visibility, '') IN ('', 'public')`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_entries_blog_summary_trgm
			ON content_entries USING GIN (LOWER(summary) %s)
			WHERE deleted_at IS NULL AND kind = 'blog' AND status = 'published' AND COALESCE(visibility, '') IN ('', 'public')`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_blog_extensions_content_trgm
			ON content_blog_extensions USING GIN (LOWER(content) %s)`, trigramOperatorClass),
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_blog_extensions_search_vector
			ON content_blog_extensions USING GIN (to_tsvector('simple', COALESCE(content, '')))`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_entries_blog_search_vector
			ON content_entries USING GIN (to_tsvector('simple', COALESCE(title, '') || ' ' || COALESCE(summary, '')))
			WHERE deleted_at IS NULL AND kind = 'blog' AND status = 'published' AND COALESCE(visibility, '') IN ('', 'public')`,
		`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_content_entries_blog_public_channel_published
			ON content_entries (channel_id, published_at DESC, created_at DESC, id DESC)
			WHERE deleted_at IS NULL AND kind = 'blog' AND status = 'published' AND COALESCE(visibility, '') IN ('', 'public')`,
	}
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

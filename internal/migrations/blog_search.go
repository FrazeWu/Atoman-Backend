package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// RunBlogSearchIndexes adds trigram indexes for the case-insensitive substring
// search used by the public post list.
func RunBlogSearchIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}
	if !db.Migrator().HasTable("posts") {
		return nil
	}

	statements := blogSearchIndexStatements()
	if err := db.Exec(statements[0]).Error; err != nil {
		return fmt.Errorf("enable pg_trgm extension: %w", err)
	}
	for _, statement := range statements[1:] {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create blog search index: %w", err)
		}
	}
	return nil
}

func blogSearchIndexStatements() []string {
	return []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		`CREATE INDEX IF NOT EXISTS idx_posts_title_trgm
			ON posts USING GIN (LOWER(title) gin_trgm_ops)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_posts_summary_trgm
			ON posts USING GIN (LOWER(summary) gin_trgm_ops)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_posts_content_trgm
			ON posts USING GIN (LOWER(content) gin_trgm_ops)
			WHERE deleted_at IS NULL`,
	}
}

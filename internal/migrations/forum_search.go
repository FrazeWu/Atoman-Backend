package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunForumSearchIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}

	statements := forumSearchIndexStatements()
	if err := db.Exec(statements[0]).Error; err != nil {
		return fmt.Errorf("enable pg_trgm extension: %w", err)
	}
	if db.Migrator().HasTable("forum_topics") {
		for _, statement := range []string{statements[1], statements[3], statements[4]} {
			if err := db.Exec(statement).Error; err != nil {
				return fmt.Errorf("create forum topic search index: %w", err)
			}
		}
	}
	if db.Migrator().HasTable("comment_entries") {
		for _, statement := range []string{statements[2], statements[5]} {
			if err := db.Exec(statement).Error; err != nil {
				return fmt.Errorf("create forum comment search index: %w", err)
			}
		}
	}
	return nil
}

func forumSearchIndexStatements() []string {
	return []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		`CREATE INDEX IF NOT EXISTS idx_forum_topics_search
			ON forum_topics USING GIN (
				to_tsvector('simple', COALESCE(title, '') || ' ' || COALESCE(content, ''))
			)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_forum_comments_search
			ON comment_entries USING GIN (
				to_tsvector('simple', COALESCE(content, ''))
			)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_forum_topics_title_trgm
			ON forum_topics USING GIN (title gin_trgm_ops)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_forum_topics_content_trgm
			ON forum_topics USING GIN (content gin_trgm_ops)
			WHERE deleted_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_forum_comments_content_trgm
			ON comment_entries USING GIN (content gin_trgm_ops)
			WHERE deleted_at IS NULL`,
	}
}

package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

func RunForumSearchIndexes(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}

	operatorClass, err := pgTrgmOperatorClass(db)
	if err != nil {
		return fmt.Errorf("forum search indexes: %w", err)
	}
	statements := forumSearchIndexStatements(operatorClass)
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

func forumSearchIndexStatements(operatorClass ...string) []string {
	trigramOperatorClass := "gin_trgm_ops"
	if len(operatorClass) > 0 {
		trigramOperatorClass = operatorClass[0]
	}
	return []string{
		`CREATE EXTENSION IF NOT EXISTS pg_trgm SCHEMA public`,
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
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_forum_topics_title_trgm
			ON forum_topics USING GIN (title %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_forum_topics_content_trgm
			ON forum_topics USING GIN (content %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_forum_comments_content_trgm
			ON comment_entries USING GIN (content %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
	}
}

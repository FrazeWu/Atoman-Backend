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
	if !db.Migrator().HasTable("posts") {
		return nil
	}

	statements := blogSearchIndexStatements()
	if err := db.Exec(statements[0]).Error; err != nil {
		return fmt.Errorf("enable pg_trgm extension: %w", err)
	}
	var extensionSchema string
	if err := db.Raw(`SELECT n.nspname
		FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_trgm'`).Scan(&extensionSchema).Error; err != nil {
		return fmt.Errorf("resolve pg_trgm schema: %w", err)
	}
	if extensionSchema == "" {
		return fmt.Errorf("resolve pg_trgm schema: extension is not installed")
	}
	operatorClass := quotePostgresIdentifier(extensionSchema) + ".gin_trgm_ops"
	for _, statement := range blogSearchIndexStatements(operatorClass)[1:] {
		if err := db.Exec(statement).Error; err != nil {
			return fmt.Errorf("create blog search index: %w", err)
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
		`CREATE EXTENSION IF NOT EXISTS pg_trgm`,
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_posts_title_trgm
			ON posts USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_posts_summary_trgm
			ON posts USING GIN (LOWER(summary) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_posts_content_trgm
			ON posts USING GIN (LOWER(content) %s)
			WHERE deleted_at IS NULL`, trigramOperatorClass),
	}
}

func quotePostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

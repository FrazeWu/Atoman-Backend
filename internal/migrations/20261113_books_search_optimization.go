package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// RunBooksSearchOptimizationMigration adds GIN trigram, full-text search,
// and JSON path indexes for the books module to accelerate work discovery,
// author search, public book lists, personal long notes, and reading marks.
func RunBooksSearchOptimizationMigration(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" && db.Dialector.Name() != "pgx" {
		return nil
	}

	operatorClass, err := pgTrgmOperatorClass(db)
	if err != nil {
		return err
	}

	indexStatements := []struct {
		table string
		sql   string
	}{
		// book_works: Trigram indexes for title, subtitle, and original_title
		{"book_works", fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_book_works_title_trgm
			ON book_works USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL`, operatorClass)},
		{"book_works", fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_book_works_subtitle_trgm
			ON book_works USING GIN (LOWER(subtitle) %s)
			WHERE deleted_at IS NULL AND subtitle <> ''`, operatorClass)},
		{"book_works", fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_book_works_original_title_trgm
			ON book_works USING GIN (LOWER(original_title) %s)
			WHERE deleted_at IS NULL AND original_title <> ''`, operatorClass)},

		// book_people: Trigram index for author/contributor name
		{"book_people", fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_book_people_name_trgm
			ON book_people USING GIN (LOWER(name) %s)
			WHERE deleted_at IS NULL`, operatorClass)},

		// book_lists: Trigram expression index on published_json title
		{"book_lists", fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_book_lists_published_title_trgm
			ON book_lists USING GIN (LOWER(published_json->>'title') %s)
			WHERE deleted_at IS NULL AND published_at IS NOT NULL`, operatorClass)},

		// book_private_notes: Full-text search on title + body, Trigram on title, GIN on tags_json
		{"book_private_notes", `CREATE INDEX IF NOT EXISTS idx_book_private_notes_fts
			ON book_private_notes USING GIN (to_tsvector('simple', COALESCE(title, '') || ' ' || COALESCE(body, '')))
			WHERE deleted_at IS NULL AND purged_at IS NULL`},
		{"book_private_notes", fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_book_private_notes_title_trgm
			ON book_private_notes USING GIN (LOWER(title) %s)
			WHERE deleted_at IS NULL AND purged_at IS NULL`, operatorClass)},
		{"book_private_notes", `CREATE INDEX IF NOT EXISTS idx_book_private_notes_tags_json
			ON book_private_notes USING GIN (tags_json jsonb_path_ops)
			WHERE deleted_at IS NULL AND purged_at IS NULL`},

		// book_reading_marks: Full-text search on quote + note, Trigram on quote
		{"book_reading_marks", `CREATE INDEX IF NOT EXISTS idx_book_reading_marks_fts
			ON book_reading_marks USING GIN (to_tsvector('simple', COALESCE(quote, '') || ' ' || COALESCE(note, '')))
			WHERE deleted_at IS NULL AND purged_at IS NULL`},
		{"book_reading_marks", fmt.Sprintf(`CREATE INDEX IF NOT EXISTS idx_book_reading_marks_quote_trgm
			ON book_reading_marks USING GIN (LOWER(quote) %s)
			WHERE deleted_at IS NULL AND purged_at IS NULL`, operatorClass)},
	}

	for _, stmt := range indexStatements {
		if db.Migrator().HasTable(stmt.table) {
			if err := db.Exec(stmt.sql).Error; err != nil {
				return fmt.Errorf("create book search index on %s: %w", stmt.table, err)
			}
		}
	}

	return nil
}

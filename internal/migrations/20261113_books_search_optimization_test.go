package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunBooksSearchOptimizationMigrationCreatesIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.BookWork{},
		&model.BookPerson{},
		&model.BookList{},
		&model.BookPrivateNote{},
		&model.BookReadingMark{},
	)
	if err := RunBooksSearchOptimizationMigration(db); err != nil {
		t.Fatalf("run books search optimization migration: %v", err)
	}

	for _, expected := range []struct{ table, index string }{
		{"book_works", "idx_book_works_title_trgm"},
		{"book_works", "idx_book_works_subtitle_trgm"},
		{"book_works", "idx_book_works_original_title_trgm"},
		{"book_people", "idx_book_people_name_trgm"},
		{"book_lists", "idx_book_lists_published_title_trgm"},
		{"book_private_notes", "idx_book_private_notes_fts"},
		{"book_private_notes", "idx_book_private_notes_title_trgm"},
		{"book_private_notes", "idx_book_private_notes_tags_json"},
		{"book_reading_marks", "idx_book_reading_marks_fts"},
		{"book_reading_marks", "idx_book_reading_marks_quote_trgm"},
	} {
		assertIndexExists(t, db, expected.table, expected.index)
	}
}

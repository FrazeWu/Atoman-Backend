package migrations

import (
	"testing"

	"atoman/internal/testdb"
)

func TestRunShortNoteMediaAttachmentMigration(t *testing.T) {
	db := testdb.OpenPostgres(t, "short_note_media_migration")

	// Ensure base short_notes table exists
	if err := db.Exec(`
CREATE TABLE IF NOT EXISTS short_notes (
	id uuid PRIMARY KEY,
	created_at timestamptz,
	updated_at timestamptz,
	deleted_at timestamptz,
	user_id uuid NOT NULL,
	content text NOT NULL
);
`).Error; err != nil {
		t.Fatalf("setup base short_notes table: %v", err)
	}

	if err := RunShortNoteMediaAttachmentMigration(db); err != nil {
		t.Fatalf("run short note media attachment migration: %v", err)
	}

	if !db.Migrator().HasColumn("short_notes", "media_attachment_type") {
		t.Fatal("expected media_attachment_type column on short_notes")
	}
	if !db.Migrator().HasColumn("short_notes", "media_attachment_id") {
		t.Fatal("expected media_attachment_id column on short_notes")
	}
	if !db.Migrator().HasIndex("short_notes", "idx_short_notes_media_attachment_id") {
		t.Fatal("expected idx_short_notes_media_attachment_id index on short_notes")
	}
}

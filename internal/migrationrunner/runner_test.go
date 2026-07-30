package migrationrunner

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunCreatesCoreSchema(t *testing.T) {
	db := testdb.Open(t)

	if err := Run(db); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, schemaModel := range []any{
		&model.User{},
		&model.AuthSession{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.SongCorrection{},
		&model.AlbumCorrection{},
		&model.ArtistCorrection{},
		&model.ArtistAlias{},
		&model.ArtistMerge{},
		&model.BlogDraft{},
		&model.Like{},
		&model.Follow{},
		&model.ActivityLog{},
		&model.TimelineEvent{},
		&model.TimelinePerson{},
		&model.PersonLocation{},
		&model.FeedSource{},
		&model.DMConversation{},
		&model.DiscussionTarget{},
	} {
		if !db.Migrator().HasTable(schemaModel) {
			t.Fatalf("expected table for %T", schemaModel)
		}
	}
	if !db.Migrator().HasIndex("content_publication_events", "idx_content_publication_events_dispatch_candidates") {
		t.Fatal("expected content publication dispatch candidate index")
	}
}

func TestRunCreatesShortNoteSchema(t *testing.T) {
	db := testdb.Open(t)

	if err := Run(db); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, schemaModel := range []any{
		&model.ShortNote{},
		&model.ShortNoteMedia{},
	} {
		if !db.Migrator().HasTable(schemaModel) {
			t.Fatalf("expected table for %T", schemaModel)
		}
	}
	if !db.Migrator().HasIndex("short_note_media", "idx_short_note_media_short_note_id") {
		t.Fatal("expected short_note_media short_note_id index")
	}

	note := model.ShortNote{UserID: uuid.New(), Content: "short note"}
	if err := db.Create(&note).Error; err != nil {
		t.Fatalf("create short note: %v", err)
	}
	media := model.ShortNoteMedia{ShortNoteID: note.ID, URL: "https://example.com/media.jpg", Position: 1}
	if err := db.Create(&media).Error; err != nil {
		t.Fatalf("create short note media: %v", err)
	}

	var loaded model.ShortNote
	if err := db.Preload("Media").First(&loaded, "id = ?", note.ID).Error; err != nil {
		t.Fatalf("load short note with media: %v", err)
	}
	if len(loaded.Media) != 1 {
		t.Fatalf("expected 1 media item, got %d", len(loaded.Media))
	}
	if loaded.Media[0].ShortNoteID != note.ID || loaded.Media[0].URL != media.URL || loaded.Media[0].Position != media.Position {
		t.Fatalf("unexpected preloaded media: %+v", loaded.Media[0])
	}
}

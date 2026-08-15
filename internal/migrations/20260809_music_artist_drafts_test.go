package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicArtistDraftsMigrationAllowsSameNameArtists(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{})
	if err := db.Exec(`CREATE UNIQUE INDEX idx_artists_display_name_unique
		ON "Artists" (LOWER(TRIM(name)), LOWER(TRIM(COALESCE(disambiguation, ''))))
		WHERE deleted_at IS NULL`).Error; err != nil {
		t.Fatalf("create legacy artist name index: %v", err)
	}

	if err := RunMusicArtistDraftsMigration(db); err != nil {
		t.Fatalf("run artist drafts migration: %v", err)
	}
	if err := RunMusicArtistDraftsMigration(db); err != nil {
		t.Fatalf("rerun artist drafts migration: %v", err)
	}
	if db.Migrator().HasIndex(&model.Artist{}, "idx_artists_display_name_unique") {
		t.Fatal("legacy artist name index still exists")
	}

	for i := 0; i < 2; i++ {
		if err := db.Create(&model.Artist{Name: "Same Name"}).Error; err != nil {
			t.Fatalf("create same-name artist %d: %v", i+1, err)
		}
	}
}

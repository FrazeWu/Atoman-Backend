package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicTagsMigrationCreatesTagTables(t *testing.T) {
	db := testdb.OpenPostgres(t, "music_tags_migration")

	if err := RunMusicTagsMigration(db); err != nil {
		t.Fatalf("run music tags migration: %v", err)
	}
	if !db.Migrator().HasTable(&model.MusicTag{}) {
		t.Fatal("expected music_tags table")
	}
	if !db.Migrator().HasTable(&model.MusicTagAssignment{}) {
		t.Fatal("expected music_tag_assignments table")
	}
	if !db.Migrator().HasTable(&model.MusicTagVote{}) {
		t.Fatal("expected music_tag_votes table")
	}
}

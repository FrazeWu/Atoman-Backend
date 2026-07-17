package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicListeningMigrationCreatesHistoryAndPlaylistPosition(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Artist{}, &model.Album{}, &model.Song{}, &model.Playlist{}, &model.PlaylistSong{})

	if err := RunMusicListeningMigration(db); err != nil {
		t.Fatalf("run music listening migration: %v", err)
	}

	if !db.Migrator().HasTable("music_listening_histories") {
		t.Fatal("expected music_listening_histories table")
	}
	if !db.Migrator().HasColumn(&model.PlaylistSong{}, "Position") {
		t.Fatal("expected playlist song position column")
	}
	assertIndexExists(t, db, "music_listening_histories", "idx_music_listening_history_user_song")
}

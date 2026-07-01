package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicBookmarksPlaylistsMigrationCreatesTablesAndIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Artist{}, &model.Album{}, &model.Song{})

	if err := RunMusicBookmarksPlaylistsMigration(db); err != nil {
		t.Fatalf("run music bookmarks playlists migration: %v", err)
	}

	for _, table := range []string{
		"music_artist_bookmarks",
		"music_album_bookmarks",
		"music_song_bookmarks",
		"music_playlists",
		"music_playlist_songs",
	} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected table %s to exist", table)
		}
	}

	assertIndexExists(t, db, "music_artist_bookmarks", "idx_music_artist_bookmarks_user_artist")
	assertIndexExists(t, db, "music_album_bookmarks", "idx_music_album_bookmarks_user_album")
	assertIndexExists(t, db, "music_song_bookmarks", "idx_music_song_bookmarks_user_song")
	assertIndexExists(t, db, "music_playlist_songs", "idx_music_playlist_songs_playlist_song")
}

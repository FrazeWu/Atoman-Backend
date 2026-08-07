package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicCatalogIndexesMigrationCreatesIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.AlbumImportSession{}, &model.MusicSearchInteraction{})
	if err := RunMusicCatalogIndexesMigration(db); err != nil {
		t.Fatalf("run catalog indexes: %v", err)
	}
	for _, expected := range []struct{ table, index string }{
		{"Albums", "idx_music_albums_visible_release"},
		{"Songs", "idx_music_songs_album_track"},
		{"music_album_import_sessions", "idx_music_import_sessions_user_status_updated"},
		{"music_search_interactions", "idx_music_search_interactions_user_created"},
	} {
		assertIndexExists(t, db, expected.table, expected.index)
	}
}

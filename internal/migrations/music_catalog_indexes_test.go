package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicCatalogIndexesMigrationCreatesIndexes(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{}, &model.Song{}, &model.AlbumImportSession{})
	if err := RunMusicCatalogIndexesMigration(db); err != nil {
		t.Fatalf("run catalog indexes: %v", err)
	}
	for _, expected := range []struct{ table, index string }{
		{"Albums", "idx_music_albums_visible_release"},
		{"Songs", "idx_music_songs_album_track"},
		{"music_album_import_sessions", "idx_music_import_sessions_user_status_updated"},
	} {
		assertIndexExists(t, db, expected.table, expected.index)
	}
}

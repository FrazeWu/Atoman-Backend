package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicSongCreditsMigrationPreservesLegacyCreditAndAllowsMultipleRoles(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.Album{}, &model.Song{})
	if err := db.Migrator().DropTable(&model.SongArtist{}); err != nil {
		t.Fatalf("drop current song artists: %v", err)
	}
	if err := db.Exec(`CREATE TABLE song_artists (
		song_id uuid NOT NULL,
		artist_id uuid NOT NULL,
		role text,
		position integer,
		created_at timestamp with time zone,
		updated_at timestamp with time zone,
		PRIMARY KEY (song_id, artist_id)
	)`).Error; err != nil {
		t.Fatalf("create legacy song artists: %v", err)
	}
	artist := model.Artist{Name: "Artist", EntryStatus: "open"}
	song := model.Song{Title: "Song", Status: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Exec("INSERT INTO song_artists (song_id, artist_id, role, position) VALUES (?, ?, '', 0)", song.ID, artist.ID).Error; err != nil {
		t.Fatalf("insert legacy credit: %v", err)
	}

	if err := RunMusicSongCreditsMigration(db); err != nil {
		t.Fatalf("run song credits migration: %v", err)
	}
	var credits []model.SongArtist
	if err := db.Where("song_id = ?", song.ID).Find(&credits).Error; err != nil {
		t.Fatalf("load migrated credits: %v", err)
	}
	if len(credits) != 1 || credits[0].Role != "primary" || credits[0].CustomRole != "" || credits[0].Position != 1 {
		t.Fatalf("unexpected migrated credit: %#v", credits)
	}
	second := model.SongArtist{SongID: song.ID, ArtistID: artist.ID, Role: "producer", Position: 1}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second role after migration: %v", err)
	}
}

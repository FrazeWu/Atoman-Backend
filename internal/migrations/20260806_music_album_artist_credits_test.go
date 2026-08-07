package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicAlbumArtistCreditsMigrationPreservesLegacyLinksAndAllowsMultipleRoles(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.Artist{}, &model.Album{}); err != nil {
		t.Fatalf("migrate catalog: %v", err)
	}
	if err := db.Migrator().DropTable("album_artists"); err != nil {
		t.Fatalf("drop generated album artists: %v", err)
	}
	if err := db.Exec(`CREATE TABLE album_artists (
		album_id uuid NOT NULL,
		artist_id uuid NOT NULL,
		role text DEFAULT 'primary',
		position integer DEFAULT 0,
		created_at datetime,
		updated_at datetime,
		PRIMARY KEY (album_id, artist_id)
	)`).Error; err != nil {
		t.Fatalf("create legacy album artists: %v", err)
	}

	artist := model.Artist{Name: "Credit Artist", EntryStatus: "open"}
	album := model.Album{Title: "Credit Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Exec("INSERT INTO album_artists (album_id, artist_id, role, position) VALUES (?, ?, '', 0)", album.ID, artist.ID).Error; err != nil {
		t.Fatalf("create legacy link: %v", err)
	}

	if err := RunMusicAlbumArtistCreditsMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := RunMusicAlbumArtistCreditsMigration(db); err != nil {
		t.Fatalf("run migration twice: %v", err)
	}

	var legacy model.AlbumArtist
	if err := db.First(&legacy, "album_id = ? AND artist_id = ?", album.ID, artist.ID).Error; err != nil {
		t.Fatalf("load migrated link: %v", err)
	}
	if legacy.Role != "primary" || legacy.CustomRole != "" || legacy.Position != 1 {
		t.Fatalf("unexpected migrated link: %#v", legacy)
	}
	if err := db.Create(&model.AlbumArtist{
		AlbumID: album.ID, ArtistID: artist.ID, Role: "producer", Position: 1,
	}).Error; err != nil {
		t.Fatalf("create second role: %v", err)
	}
	var count int64
	if err := db.Model(&model.AlbumArtist{}).Where("album_id = ? AND artist_id = ?", album.ID, artist.ID).Count(&count).Error; err != nil {
		t.Fatalf("count roles: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two roles, got %d", count)
	}
}

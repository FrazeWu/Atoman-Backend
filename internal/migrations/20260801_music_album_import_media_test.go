package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestRunMusicAlbumImportMediaMigrationBackfillsCoverFromCommittedSession(t *testing.T) {
	db := testdb.Open(t)
	if err := db.AutoMigrate(&model.Artist{}, &model.Album{}, &model.AlbumArtist{}, &model.Song{}, &model.AlbumImportSession{}, &model.MediaAsset{}); err != nil {
		t.Fatalf("migrate test schema: %v", err)
	}
	t.Setenv("S3_URL_PREFIX", "https://assets.atoman.test")

	userID := uuid.New()
	sessionCreatedAt := time.Now().UTC().Add(-30 * time.Minute)
	session := model.AlbumImportSession{
		Base:        model.Base{CreatedAt: sessionCreatedAt},
		UserID:      &userID,
		Status:      "committed",
		PayloadJSON: `{"cover_key":"music/album-imports/playback/sessions/import-1/cover/cover.webp","commit_request":{"artists":[{"artist_id":"","artist_form":"person"}]}}`,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create import session: %v", err)
	}
	album := model.Album{Title: "Imported Album", Status: "open", EntryStatus: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	artist := model.Artist{Name: "Imported Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	if err := db.Create(&model.AlbumArtist{AlbumID: album.ID, ArtistID: artist.ID}).Error; err != nil {
		t.Fatalf("link artist: %v", err)
	}
	avatar := model.MediaAsset{
		Base:        model.Base{CreatedAt: sessionCreatedAt.Add(-time.Minute)},
		UserID:      &userID,
		Purpose:     "music.cover",
		URL:         "https://assets.atoman.test/music/covers/uploads/avatar.jpg",
		Key:         "music/covers/uploads/avatar.jpg",
		ContentType: "image/jpeg",
		Size:        1024,
	}
	if err := db.Create(&avatar).Error; err != nil {
		t.Fatalf("create avatar asset: %v", err)
	}
	otherAlbum := model.Album{Title: "Other Album", Status: "open", EntryStatus: "open"}
	if err := db.Create(&otherAlbum).Error; err != nil {
		t.Fatalf("create other album: %v", err)
	}
	sessionAudioURL := "https://assets.atoman.test/music/album-imports/playback/sessions/" + session.ID.String() + "/files/track.mp3"
	if err := db.Create(&model.Song{Title: "Track", AlbumID: &album.ID, AudioURL: sessionAudioURL}).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Create(&model.Song{Title: "Other", AlbumID: &otherAlbum.ID, AudioURL: "https://assets.atoman.test/music/other.mp3"}).Error; err != nil {
		t.Fatalf("create other song: %v", err)
	}

	if err := RunMusicAlbumImportMediaMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := RunMusicAlbumImportMediaMigration(db); err != nil {
		t.Fatalf("run migration again: %v", err)
	}

	if err := db.First(&album, "id = ?", album.ID).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	want := "https://assets.atoman.test/music/album-imports/playback/sessions/import-1/cover/cover.webp"
	if album.CoverURL != want || album.CoverSource != "s3" {
		t.Fatalf("unexpected backfilled cover: url=%q source=%q", album.CoverURL, album.CoverSource)
	}
	if err := db.First(&otherAlbum, "id = ?", otherAlbum.ID).Error; err != nil {
		t.Fatalf("load other album: %v", err)
	}
	if otherAlbum.CoverURL != "" {
		t.Fatalf("unrelated album cover changed: %q", otherAlbum.CoverURL)
	}
	if err := db.First(&artist, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("load artist: %v", err)
	}
	if artist.ImageURL != avatar.URL {
		t.Fatalf("unexpected backfilled artist image: %q", artist.ImageURL)
	}

	var count int64
	if err := db.Model(&model.Album{}).Where("id IN ?", []uuid.UUID{album.ID, otherAlbum.ID}).Count(&count).Error; err != nil {
		t.Fatalf("count albums: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected both albums to remain, got %d", count)
	}
}

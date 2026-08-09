package migrations

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicPartialDatesMigrationBackfillsExistingDates(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.ArtistMember{}, &model.Album{}, &model.Song{})

	birthDate, _ := time.Parse("2006-01-02", "1990-05-20")
	artist := model.Artist{Name: "Existing Artist", BirthDate: &birthDate, ArtistForm: "person"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{Title: "Existing Album", ReleaseDate: birthDate}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}

	if err := RunMusicPartialDatesMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := db.First(&artist, "id = ?", artist.ID).Error; err != nil {
		t.Fatalf("reload artist: %v", err)
	}
	if err := db.First(&album, "id = ?", album.ID).Error; err != nil {
		t.Fatalf("reload album: %v", err)
	}
	if artist.BirthDatePrecision != "day" || album.ReleaseDatePrecision != "day" {
		t.Fatalf("expected day precision, got artist=%q album=%q", artist.BirthDatePrecision, album.ReleaseDatePrecision)
	}
}

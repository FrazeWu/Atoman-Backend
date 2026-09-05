package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicLyricsSourceMigrationBackfillsOnlyAutomaticLyrics(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{}, &model.MusicSongLyric{})
	user := model.User{Username: "lyrics-source", Email: "lyrics-source@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	automaticSong := model.Song{Title: "Automatic", AudioURL: "/automatic.mp3", UploadedBy: &user.UUID}
	manualSong := model.Song{Title: "Manual", AudioURL: "/manual.mp3", UploadedBy: &user.UUID}
	if err := db.Create(&automaticSong).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&manualSong).Error; err != nil {
		t.Fatal(err)
	}
	automatic := model.MusicSongLyric{SongID: automaticSong.ID, UpdatedBy: user.UUID, EditSummary: "自动匹配歌词"}
	manual := model.MusicSongLyric{SongID: manualSong.ID, UpdatedBy: user.UUID, EditSummary: "手动补充歌词"}
	if err := db.Create(&automatic).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&manual).Error; err != nil {
		t.Fatal(err)
	}
	if err := RunMusicLyricsSourceMigration(db); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&automatic, "id = ?", automatic.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&manual, "id = ?", manual.ID).Error; err != nil {
		t.Fatal(err)
	}
	if automatic.Source != "lrclib" || manual.Source != "" {
		t.Fatalf("unexpected sources: automatic=%q manual=%q", automatic.Source, manual.Source)
	}
}

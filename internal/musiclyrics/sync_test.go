package musiclyrics

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
)

func TestSyncLegacySongLyricsVersionsContentAndMarksInvalidAnchors(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Song{}, &model.MusicSongLyric{},
		&model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
	)
	user := model.User{Username: "legacy-sync", Email: "legacy-sync@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Legacy Sync", AudioURL: "/sync.mp3"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	if err := SyncLegacySongLyrics(db, user.UUID, song.ID, "hello", "initial"); err != nil {
		t.Fatal(err)
	}
	var line model.MusicSongLyricLine
	if err := db.First(&line).Error; err != nil {
		t.Fatal(err)
	}
	annotation := model.MusicLyricAnnotation{
		SongID: song.ID, LineID: line.ID, SelectedText: "hello",
		StartOffset: 0, EndOffset: 5, Body: "note", CreatedBy: user.UUID, Status: "active",
	}
	if err := db.Create(&annotation).Error; err != nil {
		t.Fatal(err)
	}
	if err := SyncLegacySongLyrics(db, user.UUID, song.ID, "goodbye", "legacy edit"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&annotation, "id = ?", annotation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if annotation.Status != "needs_rebind" {
		t.Fatalf("annotation status = %q, want needs_rebind", annotation.Status)
	}
	var versionCount int64
	db.Model(&model.MusicSongLyricVersion{}).Where("song_id = ?", song.ID).Count(&versionCount)
	if versionCount != 2 {
		t.Fatalf("version count = %d, want 2", versionCount)
	}
	var mirrored model.Song
	if err := db.First(&mirrored, "id = ?", song.ID).Error; err != nil {
		t.Fatal(err)
	}
	if mirrored.Lyrics != "goodbye" || mirrored.ID == uuid.Nil {
		t.Fatalf("unexpected legacy mirror: %#v", mirrored)
	}
}

package musiclyrics

import (
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestSyncLegacySongLyricsVersionsContentAndMarksInvalidAnchors(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Song{}, &model.MusicSongLyric{},
		&model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
		&model.Notification{},
	)
	actor := model.User{Username: "legacy-sync", Email: "legacy-sync@example.com", Password: "hash", IsActive: true}
	owner := model.User{Username: "annotation-owner", Email: "annotation-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Legacy Sync", AudioURL: "/sync.mp3"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return SyncLegacySongLyrics(tx, actor.UUID, song.ID, "hello", "initial")
	}); err != nil {
		t.Fatal(err)
	}
	var line model.MusicSongLyricLine
	if err := db.First(&line).Error; err != nil {
		t.Fatal(err)
	}
	annotation := model.MusicLyricAnnotation{
		SongID: song.ID, LineID: line.ID, SelectedText: "hello",
		StartOffset: 0, EndOffset: 5, Body: "note", CreatedBy: owner.UUID, Status: "active",
	}
	if err := db.Create(&annotation).Error; err != nil {
		t.Fatal(err)
	}
	actorAnnotation := model.MusicLyricAnnotation{
		SongID: song.ID, LineID: line.ID, SelectedText: "hello",
		StartOffset: 0, EndOffset: 5, Body: "own note", CreatedBy: actor.UUID, Status: "active",
	}
	if err := db.Create(&actorAnnotation).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return SyncLegacySongLyrics(tx, actor.UUID, song.ID, "goodbye", "legacy edit")
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&annotation, "id = ?", annotation.ID).Error; err != nil {
		t.Fatal(err)
	}
	if annotation.Status != "needs_rebind" {
		t.Fatalf("annotation status = %q, want needs_rebind", annotation.Status)
	}
	var notification model.Notification
	if err := db.First(&notification, "recipient_id = ? AND source_type = ? AND source_id = ?", owner.UUID, "music_lyrics", annotation.ID).Error; err != nil {
		t.Fatalf("load rebind notification: %v", err)
	}
	if notification.ActorID == nil || *notification.ActorID != actor.UUID || notification.ReadAt != nil || notification.Type != "collaboration.required" {
		t.Fatalf("unexpected rebind notification: %#v", notification)
	}
	var actorNotificationCount int64
	db.Model(&model.Notification{}).Where("source_id = ?", actorAnnotation.ID).Count(&actorNotificationCount)
	if actorNotificationCount != 0 {
		t.Fatalf("actor received %d notification(s) for own annotation", actorNotificationCount)
	}

	now := time.Now()
	if err := db.Model(&notification).Update("read_at", &now).Error; err != nil {
		t.Fatal(err)
	}
	var currentLine model.MusicSongLyricLine
	if err := db.Where("lyric_id = ?", line.LyricID).First(&currentLine).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&annotation).Updates(map[string]any{
		"line_id": currentLine.ID, "selected_text": "goodbye", "start_offset": 0, "end_offset": 7, "status": "active",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return SyncLegacySongLyrics(tx, actor.UUID, song.ID, "changed", "legacy edit again")
	}); err != nil {
		t.Fatal(err)
	}
	var notifications []model.Notification
	if err := db.Where("source_id = ?", annotation.ID).Find(&notifications).Error; err != nil {
		t.Fatal(err)
	}
	if len(notifications) != 1 || notifications[0].ID != notification.ID || notifications[0].ReadAt != nil {
		t.Fatalf("expected stable unread notification reuse, got %#v", notifications)
	}
	var versionCount int64
	db.Model(&model.MusicSongLyricVersion{}).Where("song_id = ?", song.ID).Count(&versionCount)
	if versionCount != 3 {
		t.Fatalf("version count = %d, want 3", versionCount)
	}
	var mirrored model.Song
	if err := db.First(&mirrored, "id = ?", song.ID).Error; err != nil {
		t.Fatal(err)
	}
	if mirrored.Lyrics != "changed" || mirrored.ID == uuid.Nil {
		t.Fatalf("unexpected legacy mirror: %#v", mirrored)
	}
}

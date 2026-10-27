package music

import (
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"testing"
)

func TestArchiveMusicEntryAdminOnlyAndHidesAlbumSongs(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	album := model.Album{Title: "Archive QA album", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Archive QA song", AlbumID: &album.ID, AudioURL: "https://example.test/audio.mp3", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveMusicEntry(user, "album", album.ID, "test"); err == nil {
		t.Fatal("ordinary user archived album")
	}
	var before model.Album
	if err := db.First(&before, "id = ?", album.ID).Error; err != nil {
		t.Fatal(err)
	}
	if before.LifecycleStatus != model.MusicLifecycleActive {
		t.Fatal("unauthorized mutation")
	}
	user.Role = authctx.RoleAdmin
	if err := svc.ArchiveMusicEntry(user, "album", album.ID, "Archive test work"); err != nil {
		t.Fatal(err)
	}
	var after model.Album
	var updatedSong model.Song
	if err := db.First(&after, "id = ?", album.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&updatedSong, "id = ?", song.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.LifecycleStatus != model.MusicLifecycleRetired || updatedSong.LifecycleStatus != model.MusicLifecycleRetired {
		t.Fatal("archive did not hide album and track")
	}
	if updatedSong.AudioURL != song.AudioURL {
		t.Fatal("archive removed audio reference")
	}
	var visible int64
	scopeVisibleMusicEntries(db.Model(&model.Album{}), "\"Albums\"", "uploaded_by", nil, false).Where("\"Albums\".id = ?", album.ID).Count(&visible)
	if visible != 0 {
		t.Fatal("archived album publicly visible")
	}
	scopeVisibleMusicEntries(db.Model(&model.Song{}), "\"Songs\"", "uploaded_by", nil, false).Where("\"Songs\".id = ?", song.ID).Count(&visible)
	if visible != 0 {
		t.Fatal("archived song publicly visible")
	}
	var audits int64
	if err := db.Model(&model.AuditLog{}).Where("action = ? AND entity_id = ? AND actor_id = ?", "music.archive", album.ID, user.ID).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatal("audit was not recorded")
	}
	if err := svc.ArchiveMusicEntry(user, "album", album.ID, "again"); err == nil {
		t.Fatal("duplicate archive should conflict")
	}
}

func TestArchiveMusicEntrySongOnly(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	user.Role = authctx.RoleAdmin
	album := model.Album{Title: "Preserved album", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	song := model.Song{Title: "Archived song", AlbumID: &album.ID, AudioURL: "audio.mp3", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveMusicEntry(user, "song", song.ID, "cleanup reason"); err != nil {
		t.Fatal(err)
	}
	var storedAlbum model.Album
	var storedSong model.Song
	db.First(&storedAlbum, "id = ?", album.ID)
	db.First(&storedSong, "id = ?", song.ID)
	if storedAlbum.LifecycleStatus != model.MusicLifecycleActive || storedSong.LifecycleStatus != model.MusicLifecycleRetired {
		t.Fatal("song archive changed album status")
	}
	if err := svc.ArchiveMusicEntry(user, "artist", uuid.New(), "not supported"); err == nil {
		t.Fatal("unexpected artist archive")
	}
}

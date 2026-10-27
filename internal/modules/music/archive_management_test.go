package music

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
)

func TestArchiveManagementRestoresOnlySongsArchivedWithAlbum(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	album := model.Album{Title: "Archive restore album", Status: "open", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	songA := model.Song{Title: "Public song", AlbumID: &album.ID, AudioURL: "media/a.mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
	songB := model.Song{Title: "Separately archived", AlbumID: &album.ID, AudioURL: "media/b.mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
	for _, song := range []*model.Song{&songA, &songB} {
		if err := db.Create(song).Error; err != nil {
			t.Fatal(err)
		}
	}
	user.Role = authctx.RoleAdmin
	if err := svc.ArchiveMusicEntry(user, "song", songB.ID, "individual moderation"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ArchiveMusicEntry(user, "album", album.ID, "album moderation"); err != nil {
		t.Fatal(err)
	}

	reader := user
	reader.Role = authctx.RoleUser
	if _, _, err := svc.ListArchivedMusicEntries(reader, 1, 20); err == nil {
		t.Fatal("ordinary user may list hidden works")
	}
	if err := svc.RestoreArchivedMusicEntry(reader, "album", album.ID, "unauthorized"); err == nil {
		t.Fatal("ordinary user may restore")
	}

	items, total, err := svc.ListArchivedMusicEntries(user, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if total < 3 || len(items) < 3 {
		t.Fatalf("expected album and two songs, total=%d items=%d", total, len(items))
	}
	found := false
	for _, item := range items {
		if item.ID == album.ID {
			found = true
			if item.Reason != "album moderation" {
				t.Fatalf("unexpected archive reason: %q", item.Reason)
			}
		}
	}
	if !found {
		t.Fatal("archived album missing from admin list")
	}

	if err := svc.RestoreArchivedMusicEntry(user, "song", songB.ID, "too early"); err == nil {
		t.Fatal("must not restore song while album is retired")
	}
	if err := svc.RestoreArchivedMusicEntry(user, "album", album.ID, "resolved issue"); err != nil {
		t.Fatal(err)
	}
	var restoredAlbum model.Album
	var restoredA, stillRetiredB model.Song
	if err := db.First(&restoredAlbum, "id = ?", album.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&restoredA, "id = ?", songA.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&stillRetiredB, "id = ?", songB.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restoredAlbum.LifecycleStatus != model.MusicLifecycleActive || restoredA.LifecycleStatus != model.MusicLifecycleActive || stillRetiredB.LifecycleStatus != model.MusicLifecycleRetired {
		t.Fatal("album restore reactivated an individually archived song or failed to restore the album")
	}
	if restoredAlbum.Status != "open" || restoredA.Status != "open" || restoredA.AudioURL != "media/a.mp3" || stillRetiredB.AudioURL != "media/b.mp3" {
		t.Fatal("restore mutated original data or audio")
	}
	if err := svc.RestoreArchivedMusicEntry(user, "song", songB.ID, "appeal accepted"); err != nil {
		t.Fatal(err)
	}
	var restoredB model.Song
	if err := db.First(&restoredB, "id = ?", songB.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restoredB.LifecycleStatus != model.MusicLifecycleActive {
		t.Fatal("individually archived song not restored")
	}
	var audits int64
	if err := db.Model(&model.AuditLog{}).Where("action = ? AND actor_id = ?", "music.restore", user.ID).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("expected 2 restore audit logs, got %d", audits)
	}
}

func TestArchiveManagementRejectsLegacyMissingRestorationSnapshot(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	user.Role = authctx.RoleAdmin
	album := model.Album{Title: "Legacy archive", Status: "closed", EntryStatus: "closed", LifecycleStatus: model.MusicLifecycleRetired}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AuditLog{ActorID: &user.ID, Action: "music.archive", EntityType: "album", EntityID: &album.ID, Reason: "legacy"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreArchivedMusicEntry(user, "album", album.ID, "restore"); err == nil {
		t.Fatal("legacy archive without song snapshot must not be automatically restored")
	}
	var stored model.Album
	if err := db.First(&stored, "id = ?", album.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LifecycleStatus != model.MusicLifecycleRetired {
		t.Fatal("legacy restore partially mutated album")
	}
	if err := svc.RestoreArchivedMusicEntry(user, "artist", uuid.New(), "invalid"); err == nil {
		t.Fatal("unsupported restore type")
	}
}

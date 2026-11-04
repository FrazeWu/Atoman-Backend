package music

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// This test uses testdb.OpenPostgres in newMusicTestService. Every test runs
// in a disposable database/schema and never accesses production user data.
func TestPlaylistLifecyclePersistentStorageAcceptance(t *testing.T) {
	svc, db, owner := newMusicTestService(t)
	playlist, err := svc.CreatePlaylist(owner, CreatePlaylistRequest{Name: "QA lifecycle original", Description: "isolated", IsPublic: false})
	if err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	if playlist.UserID != owner.ID {
		t.Fatalf("playlist owner mismatch")
	}
	assertPlaylistListed := func(name string, present bool) {
		t.Helper()
		rows, _, err := svc.ListPlaylists(owner, 1, 100, "latest")
		if err != nil {
			t.Fatalf("list playlists: %v", err)
		}
		found := false
		for _, item := range rows {
			if item.ID == playlist.ID {
				found = true
				if item.Name != name {
					t.Fatalf("playlist name got %q want %q", item.Name, name)
				}
			}
		}
		if found != present {
			t.Fatalf("playlist listed=%t expected=%t", found, present)
		}
	}
	assertPlaylistListed("QA lifecycle original", true)
	// Simulate a fresh request context from another device by rebuilding the
	// service over the same test database (not relying on any view cache).
	fromSecondDevice := NewService(db.Session(&gorm.Session{NewDB: true}))
	again, err := fromSecondDevice.GetPlaylist(owner, playlist.ID)
	if err != nil || again.Name != "QA lifecycle original" {
		t.Fatalf("second session could not read created playlist: %+v err=%v", again, err)
	}
	other := authctx.CurrentUser{ID: uuid.New(), Username: "unrelated"}
	if _, err := fromSecondDevice.GetPlaylist(other, playlist.ID); err == nil {
		t.Fatal("private playlist leaked to another user")
	}
	if err := fromSecondDevice.DeletePlaylist(other, playlist.ID); err == nil {
		t.Fatal("non-owner could delete playlist")
	}
	name, description, public := "QA lifecycle renamed", "persisted description", true
	updated, err := svc.UpdatePlaylist(owner, playlist.ID, UpdatePlaylistRequest{Name: &name, Description: &description, IsPublic: &public})
	if err != nil {
		t.Fatalf("update playlist: %v", err)
	}
	if updated.Name != name || updated.Description != description || !updated.IsPublic {
		t.Fatalf("unexpected updated metadata: %+v", updated)
	}
	assertPlaylistListed(name, true)
	fresh, err := fromSecondDevice.GetPlaylist(owner, playlist.ID)
	if err != nil || fresh.Name != name || fresh.Description != description || !fresh.IsPublic {
		t.Fatalf("metadata missing on second session: %+v err=%v", fresh, err)
	}
	for _, title := range []string{"QA lifecycle alpha", "QA lifecycle beta"} {
		song := model.Song{Title: title, AudioURL: "/qa/" + uuid.NewString() + ".mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
		if err := db.Create(&song).Error; err != nil {
			t.Fatalf("seed isolated song: %v", err)
		}
		if _, err := svc.AddPlaylistSong(owner, playlist.ID, song.ID); err != nil {
			t.Fatalf("add song: %v", err)
		}
	}
	items, total, err := svc.ListPlaylistSongs(owner, playlist.ID, 1, 100)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("songs after add: total=%d items=%d err=%v", total, len(items), err)
	}
	originalOrder := []uuid.UUID{items[0].SongID, items[1].SongID}
	reverse := []uuid.UUID{originalOrder[1], originalOrder[0]}
	if err := svc.ReorderPlaylistSongs(owner, playlist.ID, reverse); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	items, total, err = fromSecondDevice.ListPlaylistSongs(owner, playlist.ID, 1, 100)
	if err != nil || total != 2 || len(items) != 2 || items[0].SongID != reverse[0] || items[1].SongID != reverse[1] {
		t.Fatalf("reorder did not persist: items=%+v total=%d err=%v", items, total, err)
	}
	if err := svc.DeletePlaylistSong(owner, playlist.ID, reverse[0]); err != nil {
		t.Fatalf("delete playlist song: %v", err)
	}
	items, total, err = svc.ListPlaylistSongs(owner, playlist.ID, 1, 100)
	if err != nil || total != 1 || len(items) != 1 || items[0].SongID != reverse[1] {
		t.Fatalf("song removal not persisted: total=%d items=%+v err=%v", total, items, err)
	}
	if err := svc.DeletePlaylist(owner, playlist.ID); err != nil {
		t.Fatalf("delete playlist: %v", err)
	}
	assertPlaylistListed(name, false)
	_, err = fromSecondDevice.GetPlaylist(owner, playlist.ID)
	if err == nil {
		t.Fatal("deleted playlist still accessible to another session")
	}
	var relations int64
	if err := db.Model(&model.PlaylistSong{}).Where("playlist_id = ?", playlist.ID).Count(&relations).Error; err != nil {
		t.Fatal(err)
	}
	if relations != 0 {
		t.Fatalf("orphan playlist relations remain: %d", relations)
	}
}

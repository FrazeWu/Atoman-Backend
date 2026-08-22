package migrations

import (
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func TestRunMusicArtistAlbumVisibilityMigrationPublishesReferencedDrafts(t *testing.T) {
	db := testdb.OpenPostgres(t, "music_artist_visibility")
	testdb.Migrate(t, db, &model.Artist{}, &model.Album{}, &model.AlbumArtist{})

	referenced := model.Artist{Name: "Referenced Draft", EntryStatus: "draft", LifecycleStatus: model.MusicLifecycleDraft}
	orphan := model.Artist{Name: "Orphan Draft", EntryStatus: "draft", LifecycleStatus: model.MusicLifecycleDraft}
	if err := db.Create(&referenced).Error; err != nil {
		t.Fatalf("create referenced artist: %v", err)
	}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatalf("create orphan artist: %v", err)
	}
	album := model.Album{Title: "Referenced Album", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Create(&model.AlbumArtist{AlbumID: album.ID, ArtistID: referenced.ID, Role: "primary"}).Error; err != nil {
		t.Fatalf("link artist album: %v", err)
	}

	if err := RunMusicArtistAlbumVisibilityMigration(db); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if err := RunMusicArtistAlbumVisibilityMigration(db); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}

	var published, remaining model.Artist
	if err := db.First(&published, "id = ?", referenced.ID).Error; err != nil {
		t.Fatalf("reload referenced artist: %v", err)
	}
	if err := db.First(&remaining, "id = ?", orphan.ID).Error; err != nil {
		t.Fatalf("reload orphan artist: %v", err)
	}
	if published.EntryStatus != "open" || published.LifecycleStatus != model.MusicLifecycleActive {
		t.Fatalf("expected referenced artist to be public, got entry=%q lifecycle=%q", published.EntryStatus, published.LifecycleStatus)
	}
	if remaining.EntryStatus != "draft" || remaining.LifecycleStatus != model.MusicLifecycleDraft {
		t.Fatalf("expected orphan artist to remain draft, got entry=%q lifecycle=%q", remaining.EntryStatus, remaining.LifecycleStatus)
	}
}

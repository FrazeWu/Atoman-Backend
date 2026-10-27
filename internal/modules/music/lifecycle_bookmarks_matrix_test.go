package music

import (
	"atoman/internal/model"
	"testing"
)

func TestBookmarkLifecycleMatrix(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	states := []string{model.MusicLifecycleActive, model.MusicLifecycleDraft, model.MusicLifecycleRetired, model.MusicLifecycleMerged}
	for _, state := range states {
		artist := model.Artist{Name: "Matrix artist " + state, LifecycleStatus: state, CreatedBy: &user.ID}
		album := model.Album{Title: "Matrix album " + state, LifecycleStatus: state, UploadedBy: &user.ID}
		if err := db.Create(&artist).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&album).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.ArtistBookmark{UserID: user.ID, ArtistID: artist.ID}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.AlbumBookmark{UserID: user.ID, AlbumID: album.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	deletedArtist := model.Artist{Name: "Matrix deleted artist", LifecycleStatus: model.MusicLifecycleActive}
	deletedAlbum := model.Album{Title: "Matrix deleted album", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&deletedArtist).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&deletedAlbum).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ArtistBookmark{UserID: user.ID, ArtistID: deletedArtist.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.AlbumBookmark{UserID: user.ID, AlbumID: deletedAlbum.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&deletedArtist).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&deletedAlbum).Error; err != nil {
		t.Fatal(err)
	}
	artists, atotal, err := svc.ListArtistBookmarks(user, 1, 30, "latest")
	if err != nil {
		t.Fatal(err)
	}
	albums, btotal, err := svc.ListAlbumBookmarks(user, 1, 30, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if atotal != 1 || len(artists) != 1 || artists[0].Artist == nil || artists[0].Artist.LifecycleStatus != model.MusicLifecycleActive {
		t.Fatalf("artist bookmark states count=%d rows=%+v", atotal, artists)
	}
	if btotal != 1 || len(albums) != 1 || albums[0].Album == nil || albums[0].Album.LifecycleStatus != model.MusicLifecycleActive {
		t.Fatalf("album bookmark states count=%d rows=%+v", btotal, albums)
	}
}

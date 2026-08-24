package music

import (
	"errors"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"

	"gorm.io/gorm"
)

func TestConvertStandaloneSongToAlbumMovesOwnership(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Conversion Artist", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	releaseType := "single"
	song := model.Song{
		Title: "Standalone", Description: "Song description", ReleaseType: &releaseType,
		ReleaseDate: time.Date(2025, time.January, 2, 0, 0, 0, 0, time.UTC), ReleaseDatePrecision: "day",
		CoverURL: "https://cdn.test/song.jpg", AudioURL: "https://cdn.test/song.mp3",
		Status: "open", LifecycleStatus: model.MusicLifecycleActive, EditStatus: model.MusicEditDevelopment,
		SourcesJSON: `[{"type":"url","url":"https://example.test/song"}]`, UploadedBy: &user.ID,
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	if err := db.Create(&model.SongArtist{SongID: song.ID, ArtistID: artist.ID, Role: "primary", Position: 1}).Error; err != nil {
		t.Fatalf("create song credit: %v", err)
	}
	session := model.AlbumImportSession{TargetSongID: &song.ID, PayloadJSON: `{}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create import session: %v", err)
	}

	albumID, err := service.ConvertStandaloneSongToAlbum(user, song.ID, MusicReleaseConversionRequest{
		Title: "Standalone", Description: "Album description", ReleaseDate: "2025-01-02",
		ReleaseType: "ep", CoverURL: "https://cdn.test/album.jpg",
		ArtistCredits: []AlbumArtistCreditInput{{ArtistID: artist.ID.String(), Roles: []AlbumArtistRoleInput{{Role: "primary"}}, Position: 1}},
		Sources:       []Source{{URL: "https://example.test/album"}},
		Reason:        "转换为专辑",
	})
	if err != nil {
		t.Fatalf("convert song to album: %v", err)
	}
	var album model.Album
	if err := db.Preload("ArtistCredits").First(&album, "id = ?", albumID).Error; err != nil {
		t.Fatalf("load converted album: %v", err)
	}
	if album.AlbumType != "ep" || album.Title != song.Title || album.CoverURL != "https://cdn.test/album.jpg" || len(album.ArtistCredits) != 1 {
		t.Fatalf("unexpected converted album: %#v", album)
	}
	var converted model.Song
	if err := db.First(&converted, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("load converted song: %v", err)
	}
	if converted.AlbumID == nil || *converted.AlbumID != albumID || converted.ReleaseType != nil || converted.CoverURL != "" {
		t.Fatalf("unexpected converted song ownership: %#v", converted)
	}
	var updatedSession model.AlbumImportSession
	if err := db.First(&updatedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load converted session: %v", err)
	}
	if updatedSession.TargetSongID != nil || updatedSession.TargetAlbumID == nil || *updatedSession.TargetAlbumID != albumID {
		t.Fatalf("unexpected converted session target: %#v", updatedSession)
	}
}

func TestConvertAlbumToStandaloneSongRequiresOneTrackAndRemovesAlbum(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Single Artist", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	album := model.Album{
		Title: "One Track Album", AlbumType: "album", CoverURL: "https://cdn.test/album.jpg",
		Status: "open", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive,
		EditStatus: model.MusicEditDevelopment, UploadedBy: &user.ID,
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	if err := db.Create(&model.AlbumArtist{AlbumID: album.ID, ArtistID: artist.ID, Role: "primary", Position: 1}).Error; err != nil {
		t.Fatalf("create album credit: %v", err)
	}
	song := model.Song{
		Title: album.Title, AlbumID: &album.ID, AudioURL: "https://cdn.test/song.mp3",
		Status: "open", LifecycleStatus: model.MusicLifecycleActive, EditStatus: model.MusicEditDevelopment,
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create album song: %v", err)
	}
	session := model.AlbumImportSession{TargetAlbumID: &album.ID, PayloadJSON: `{}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create import session: %v", err)
	}

	songID, err := service.ConvertAlbumToStandaloneSong(user, album.ID, MusicReleaseConversionRequest{
		Title: "One Track Album", Description: "Optional", ReleaseDate: "2025-02-03",
		ReleaseType: "leak", CoverURL: "https://cdn.test/single.jpg",
		ArtistCredits: []AlbumArtistCreditInput{{ArtistID: artist.ID.String(), Roles: []AlbumArtistRoleInput{{Role: "primary"}}, Position: 1}},
		Sources:       []Source{{URL: "https://example.test/single"}},
		Reason:        "转换为独立歌曲",
	})
	if err != nil {
		t.Fatalf("convert album to song: %v", err)
	}
	if songID != song.ID {
		t.Fatalf("converted song id = %s, want %s", songID, song.ID)
	}
	if err := db.Unscoped().First(&model.Album{}, "id = ?", album.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected album deletion, got %v", err)
	}
	var converted model.Song
	if err := db.Preload("ArtistCredits").First(&converted, "id = ?", song.ID).Error; err != nil {
		t.Fatalf("load standalone song: %v", err)
	}
	if converted.AlbumID != nil || converted.ReleaseType == nil || *converted.ReleaseType != "leak" || converted.Title != "One Track Album" {
		t.Fatalf("unexpected standalone song: %#v", converted)
	}
	if converted.Description != "Optional" || converted.CoverURL != "https://cdn.test/single.jpg" || len(converted.ArtistCredits) != 1 {
		t.Fatalf("unexpected standalone metadata: %#v", converted)
	}
	var updatedSession model.AlbumImportSession
	if err := db.First(&updatedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load converted session: %v", err)
	}
	if updatedSession.TargetAlbumID != nil || updatedSession.TargetSongID == nil || *updatedSession.TargetSongID != song.ID {
		t.Fatalf("unexpected converted session target: %#v", updatedSession)
	}
}

func TestConvertAlbumToStandaloneSongRejectsMultipleTracks(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	album := model.Album{Title: "Multi", AlbumType: "album", Status: "open", EntryStatus: "open", LifecycleStatus: model.MusicLifecycleActive, EditStatus: model.MusicEditDevelopment}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	for _, title := range []string{"One", "Two"} {
		song := model.Song{Title: title, AlbumID: &album.ID, AudioURL: "/" + title + ".mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
		if err := db.Create(&song).Error; err != nil {
			t.Fatalf("create track: %v", err)
		}
	}
	_, err := service.ConvertAlbumToStandaloneSong(user, album.ID, MusicReleaseConversionRequest{
		Title: "Multi", ReleaseDate: "2025-01-01", ReleaseType: "single", CoverURL: "/cover.jpg",
		Sources: []Source{{URL: "https://example.test/source"}},
		Reason:  "修正发行类型",
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one song") {
		t.Fatalf("expected one-track validation, got %v", err)
	}
}

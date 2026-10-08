package music

import (
	"atoman/internal/model"
	"encoding/json"
	"testing"
)

func TestCommitAlbumImportSessionFallsBackFromBrowserCover(t *testing.T) {
	service, db, user := newMusicTestService(t)
	payload, _ := json.Marshal(map[string]any{
		"derived_cover":  "https://cdn.test/processed.webp",
		"derived_tracks": []map[string]any{{"title": "Welcome to C4", "audio_url": "https://cdn.test/welcome.mp3"}},
	})
	session := model.AlbumImportSession{UserID: &user.ID, Status: AlbumImportStatusReady, PayloadJSON: string(payload)}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	committed, err := service.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: completeAlbumImportArtistPayload("Kendrick Lamar"), ArtistSource: "artist source", AlbumSource: "album source",
		Album: AlbumImportAlbumPayload{Title: "C4", CoverURL: "blob:https://site.test/preview", ReleaseDate: "2009-01-01", Tracks: []AlbumImportTrackPayload{{Title: "Welcome to C4", TrackNumber: 1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var album model.Album
	if err := db.First(&album, "id = ?", committed.TargetAlbumID).Error; err != nil {
		t.Fatal(err)
	}
	if album.CoverURL != "https://cdn.test/processed.webp" {
		t.Fatalf("保存的封面错误: %s", album.CoverURL)
	}
}

func TestAlbumImportCoverUsesProcessedCoverInsteadOfBrowserPreview(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.test")
	payload := map[string]any{"cover_url": "blob:https://site.test/preview", "derived_cover": "data:image/png;base64,preview", "cover_key": "music/album-imports/cover.webp"}
	if got := resolveAlbumImportCoverURL(payload); got != "https://cdn.example.test/music/album-imports/cover.webp" {
		t.Fatalf("浏览器预览不能作为封面: %s", got)
	}
}

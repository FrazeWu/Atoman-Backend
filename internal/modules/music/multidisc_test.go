package music

import (
	"encoding/json"
	"testing"

	"atoman/internal/model"
)

func TestCommitAlbumImportSessionPreservesDiscNumberWhenMatchingAudio(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	payloadJSON, err := json.Marshal(map[string]any{
		"derived_tracks": []map[string]any{
			{"title": "Intro", "disc_number": 1, "track_number": 1, "audio_url": "https://cdn.test/disc-1.mp3"},
			{"title": "Intro", "disc_number": 2, "track_number": 1, "audio_url": "https://cdn.test/disc-2.mp3"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := model.AlbumImportSession{UserID: &user.ID, Status: AlbumImportStatusReady, PayloadJSON: string(payloadJSON)}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}

	_, err = svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: completeAlbumImportArtistPayload("Multi Disc Artist"),
		Album: AlbumImportAlbumPayload{Title: "Multi Disc Album", CoverURL: "/cover.jpg", ReleaseDate: "2020-01-01", Tracks: []AlbumImportTrackPayload{
			{Title: "Intro", DiscNumber: 1, TrackNumber: 1},
			{Title: "Intro", DiscNumber: 2, TrackNumber: 1},
		}},
		ArtistSource: "artist source", AlbumSource: "album source",
	})
	if err != nil {
		t.Fatal(err)
	}

	var songs []model.Song
	if err := db.Order("disc_number ASC").Find(&songs).Error; err != nil {
		t.Fatal(err)
	}
	if len(songs) != 2 || songs[0].DiscNumber != 1 || songs[1].DiscNumber != 2 {
		t.Fatalf("unexpected disc numbers: %#v", songs)
	}
	if songs[0].AudioURL != "https://cdn.test/disc-1.mp3" || songs[1].AudioURL != "https://cdn.test/disc-2.mp3" {
		t.Fatalf("audio matched to the wrong disc: %#v", songs)
	}
}

func TestLoadAdjacentAlbumSongsUsesDiscThenTrackOrder(t *testing.T) {
	_, db, _ := newMusicTestService(t)
	album := model.Album{Title: "Multi Disc", Status: "open", EntryStatus: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatal(err)
	}
	songs := []model.Song{
		{Title: "D1T1", AlbumID: &album.ID, DiscNumber: 1, TrackNumber: 1, Status: "open"},
		{Title: "D1T2", AlbumID: &album.ID, DiscNumber: 1, TrackNumber: 2, Status: "open"},
		{Title: "D2T1", AlbumID: &album.ID, DiscNumber: 2, TrackNumber: 1, Status: "open"},
		{Title: "D2T2", AlbumID: &album.ID, DiscNumber: 2, TrackNumber: 2, Status: "open"},
	}
	if err := db.Create(&songs).Error; err != nil {
		t.Fatal(err)
	}

	previous, next := loadAdjacentAlbumSongs(db, songs[2])
	if previous.Title != "D1T2" || next.Title != "D2T2" {
		t.Fatalf("unexpected adjacent tracks: previous=%q next=%q", previous.Title, next.Title)
	}
}

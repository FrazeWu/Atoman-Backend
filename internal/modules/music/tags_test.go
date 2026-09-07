package music

import (
	"encoding/json"
	"net/http"
	"testing"

	"atoman/internal/model"
)

func TestMusicTagsSupportSongAlbumKindsAndVotes(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	album := model.Album{Title: "Tagged Album", LifecycleStatus: model.MusicLifecycleActive, Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	song := model.Song{Title: "Tagged Song", AlbumID: &album.ID, AudioURL: "/tagged.mp3", LifecycleStatus: model.MusicLifecycleActive, Status: "open"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	router := newMusicHTTPRouter(service, &user)

	createdSongTag := performMusicJSONRequest(t, router, http.MethodPost,
		"/api/v1/music/songs/"+song.ID.String()+"/tags", `{"kind":"mood","name":"  治愈  "}`)
	if createdSongTag.Code != http.StatusCreated {
		t.Fatalf("expected song tag creation 201, got %d: %s", createdSongTag.Code, createdSongTag.Body.String())
	}
	var songTagEnvelope struct {
		Data MusicTagDTO `json:"data"`
	}
	if err := json.Unmarshal(createdSongTag.Body.Bytes(), &songTagEnvelope); err != nil {
		t.Fatalf("decode created song tag: %v", err)
	}
	if songTagEnvelope.Data.Name != "治愈" || songTagEnvelope.Data.Kind != "mood" {
		t.Fatalf("unexpected song tag: %#v", songTagEnvelope.Data)
	}

	duplicate := performMusicJSONRequest(t, router, http.MethodPost,
		"/api/v1/music/songs/"+song.ID.String()+"/tags", `{"kind":"mood","name":"治愈"}`)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("expected duplicate tag 409, got %d: %s", duplicate.Code, duplicate.Body.String())
	}

	createdAlbumTag := performMusicJSONRequest(t, router, http.MethodPost,
		"/api/v1/music/albums/"+album.ID.String()+"/tags", `{"kind":"type","name":"现场"}`)
	if createdAlbumTag.Code != http.StatusCreated {
		t.Fatalf("expected album tag creation 201, got %d: %s", createdAlbumTag.Code, createdAlbumTag.Body.String())
	}

	voted := performMusicJSONRequest(t, router, http.MethodPut,
		"/api/v1/music/songs/"+song.ID.String()+"/tags/"+songTagEnvelope.Data.ID.String()+"/vote", `{"vote":"up"}`)
	if voted.Code != http.StatusOK {
		t.Fatalf("expected tag upvote 200, got %d: %s", voted.Code, voted.Body.String())
	}
	if err := json.Unmarshal(voted.Body.Bytes(), &songTagEnvelope); err != nil {
		t.Fatalf("decode upvote result: %v", err)
	}
	if songTagEnvelope.Data.Upvotes != 1 || songTagEnvelope.Data.ViewerVote != "up" {
		t.Fatalf("unexpected upvote result: %#v", songTagEnvelope.Data)
	}

	switched := performMusicJSONRequest(t, router, http.MethodPut,
		"/api/v1/music/songs/"+song.ID.String()+"/tags/"+songTagEnvelope.Data.ID.String()+"/vote", `{"vote":"down"}`)
	if switched.Code != http.StatusOK {
		t.Fatalf("expected tag downvote 200, got %d: %s", switched.Code, switched.Body.String())
	}
	if err := json.Unmarshal(switched.Body.Bytes(), &songTagEnvelope); err != nil {
		t.Fatalf("decode downvote result: %v", err)
	}
	if songTagEnvelope.Data.Upvotes != 0 || songTagEnvelope.Data.Downvotes != 1 || songTagEnvelope.Data.ViewerVote != "down" {
		t.Fatalf("expected vote switch, got %#v", songTagEnvelope.Data)
	}
}

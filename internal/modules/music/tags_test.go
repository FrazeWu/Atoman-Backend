package music

import (
	"encoding/json"
	"net/http"
	"testing"

	"atoman/internal/model"

	"github.com/google/uuid"
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

func TestSearchMusicTagsFiltersByKindAndQuery(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	for _, tag := range []model.MusicTag{
		{Name: "治愈", NormalizedName: "治愈", Kind: model.MusicTagKindMood, CreatedBy: user.ID},
		{Name: "治愈系", NormalizedName: "治愈系", Kind: model.MusicTagKindMood, CreatedBy: user.ID},
		{Name: "治愈现场", NormalizedName: "治愈现场", Kind: model.MusicTagKindType, CreatedBy: user.ID},
	} {
		if err := db.Create(&tag).Error; err != nil {
			t.Fatalf("create tag: %v", err)
		}
	}

	router := newMusicHTTPRouter(service, nil)
	response := performMusicJSONRequest(t, router, http.MethodGet,
		"/api/v1/music/tags?kind=mood&q=治愈", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected tag search 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []MusicTagOptionDTO `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tag search response: %v", err)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("expected two mood tags, got %#v", payload.Data)
	}
	for _, tag := range payload.Data {
		if tag.Kind != model.MusicTagKindMood {
			t.Fatalf("expected mood result, got %#v", tag)
		}
	}

	typeResponse := performMusicJSONRequest(t, router, http.MethodGet,
		"/api/v1/music/tags?kind=type&q=治愈", "")
	if typeResponse.Code != http.StatusOK {
		t.Fatalf("expected type tag search 200, got %d: %s", typeResponse.Code, typeResponse.Body.String())
	}
	if err := json.Unmarshal(typeResponse.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode type tag search response: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].Kind != model.MusicTagKindType {
		t.Fatalf("expected one type tag, got %#v", payload.Data)
	}
}

func TestGetMusicTagReturnsPublicTagDetails(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	tag := model.MusicTag{
		Name:           "治愈",
		NormalizedName: "治愈",
		Kind:           model.MusicTagKindMood,
		CreatedBy:      user.ID,
	}
	if err := db.Create(&tag).Error; err != nil {
		t.Fatalf("create tag: %v", err)
	}

	response := performMusicJSONRequest(t, newMusicHTTPRouter(service, nil), http.MethodGet,
		"/api/v1/music/tags/"+tag.ID.String(), "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected tag detail 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data MusicTagOptionDTO `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tag detail response: %v", err)
	}
	if payload.Data.ID != tag.ID || payload.Data.Name != tag.Name || payload.Data.Kind != tag.Kind {
		t.Fatalf("unexpected tag detail: %#v", payload.Data)
	}

	missing := performMusicJSONRequest(t, newMusicHTTPRouter(service, nil), http.MethodGet,
		"/api/v1/music/tags/"+uuid.NewString(), "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected missing tag 404, got %d: %s", missing.Code, missing.Body.String())
	}
}

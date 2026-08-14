package music

import (
	"encoding/json"
	"net/http"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
)

func TestMusicAssetUploadRoutesCreateReadPartAndIsolateOwner(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://assets.example.test")
	service, db, user := newMusicHTTPTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-http", signedURL: "https://storage.example.test/part-1"}
	service.assetUploadMultipart = store
	router := newMusicHTTPRouter(service, &user)

	created := performMusicJSONRequest(t, router, http.MethodPost, "/api/v1/music/uploads", `{"file_name":"track.mp3","content_type":"audio/mpeg","size":16777216}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d: %s", created.Code, created.Body.String())
	}
	var payload struct {
		Data MusicAssetUploadSessionDTO `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Data.ID == "" || payload.Data.PartSize != musicAssetUploadPartSize {
		t.Fatalf("unexpected upload session: %#v", payload.Data)
	}

	read := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/uploads/"+payload.Data.ID, "")
	if read.Code != http.StatusOK {
		t.Fatalf("expected get 200, got %d: %s", read.Code, read.Body.String())
	}
	part := performMusicJSONRequest(t, router, http.MethodPost, "/api/v1/music/uploads/"+payload.Data.ID+"/parts/1", `{}`)
	if part.Code != http.StatusOK || !json.Valid(part.Body.Bytes()) || store.presignPartNumber != 1 {
		t.Fatalf("expected signed part URL, got %d: %s", part.Code, part.Body.String())
	}

	other := model.User{Username: "other", Email: "other@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherRouter := newMusicHTTPRouter(service, &authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: authctx.RoleUser})
	missing := performMusicJSONRequest(t, otherRouter, http.MethodGet, "/api/v1/music/uploads/"+payload.Data.ID, "")
	assertMusicHTTPError(t, missing, http.StatusNotFound, "music.upload_not_found")
}

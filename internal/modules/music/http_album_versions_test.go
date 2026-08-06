package music

import (
	"encoding/json"
	"net/http"
	"testing"

	"atoman/internal/model"
)

func TestRegisterRoutesAlbumDetailIncludesOtherVersions(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	original := model.Album{Title: "Original", Status: "open", EntryStatus: "open", EditionType: "original"}
	if err := db.Create(&original).Error; err != nil {
		t.Fatalf("create original: %v", err)
	}
	deluxe := model.Album{Title: "Deluxe", Status: "open", EntryStatus: "open", EditionType: "deluxe", CanonicalAlbumID: &original.ID}
	if err := db.Create(&deluxe).Error; err != nil {
		t.Fatalf("create deluxe: %v", err)
	}
	unrelated := model.Album{Title: "Unrelated", Status: "open", EntryStatus: "open"}
	if err := db.Create(&unrelated).Error; err != nil {
		t.Fatalf("create unrelated: %v", err)
	}

	router := newMusicHTTPRouter(service, &user)
	response := performMusicJSONRequest(t, router, http.MethodGet, "/api/v1/music/albums/"+original.ID.String(), "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected album detail 200, got %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			OtherVersions []struct {
				ID string `json:"id"`
			} `json:"other_versions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode album detail: %v", err)
	}
	if len(body.Data.OtherVersions) != 1 || body.Data.OtherVersions[0].ID != deluxe.ID.String() {
		t.Fatalf("expected only deluxe version, got %#v", body.Data.OtherVersions)
	}
}

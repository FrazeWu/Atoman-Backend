package music

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"
)

func TestGroupWorksSongsUseMembershipWithoutChangingPersonalCredits(t *testing.T) {
	service, db, _ := newMusicHTTPTestService(t)
	member := model.Artist{Name: "Member", EntryStatus: "open", LifecycleStatus: "active"}
	if err := db.Create(&member).Error; err != nil {
		t.Fatal(err)
	}
	future := time.Now().AddDate(1, 0, 0)
	for _, scenario := range []struct {
		name, lifecycle string
		join            *time.Time
		deleted         bool
	}{
		{name: "Visible group", lifecycle: "active"},
		{name: "Private group", lifecycle: "draft"},
		{name: "Future group", lifecycle: "active", join: &future},
		{name: "Deleted membership", lifecycle: "active", deleted: true},
	} {
		group := model.Artist{Name: scenario.name, ArtistForm: "group", EntryStatus: "open", LifecycleStatus: scenario.lifecycle}
		if err := db.Create(&group).Error; err != nil {
			t.Fatal(err)
		}
		membership := model.ArtistMember{GroupArtistID: group.ID, MemberArtistID: member.ID, JoinDate: scenario.join}
		if err := db.Create(&membership).Error; err != nil {
			t.Fatal(err)
		}
		if scenario.deleted {
			if err := db.Delete(&membership).Error; err != nil {
				t.Fatal(err)
			}
		}
		releaseType := "single"
		song := model.Song{Title: scenario.name, ReleaseType: &releaseType, Status: "open", LifecycleStatus: "active"}
		if err := db.Create(&song).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.SongArtist{SongID: song.ID, ArtistID: group.ID, Role: "primary"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := newMusicHTTPRouter(service, nil)
	for _, scenario := range []struct {
		query    string
		expected int
	}{
		{query: "&artist_relation=participating", expected: 1},
		{query: "", expected: 0},
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/music/songs?release_type=single,leak&artist_id="+member.ID.String()+scenario.query, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%d: %s", response.Code, response.Body.String())
		}
		var result struct {
			Data []model.Song `json:"data"`
			Meta struct {
				Total int `json:"total"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Data) != scenario.expected || result.Meta.Total != scenario.expected {
			t.Fatalf("unexpected group works: %s", response.Body.String())
		}
		if scenario.expected > 0 && result.Data[0].Title != "Visible group" {
			t.Fatalf("unexpected song: %s", result.Data[0].Title)
		}
	}
	for _, path := range []string{"/api/v1/music/songs?artist_relation=participating", "/api/v1/music/albums?artist_relation=participating", "/api/v1/music/songs?artist_relation=invalid"} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d", path, response.Code)
		}
	}
}

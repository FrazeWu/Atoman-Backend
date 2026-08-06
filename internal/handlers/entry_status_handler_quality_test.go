package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestListMusicQualityIssuesIncludesMetadataAndDuplicateCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.Album{}, &model.Song{}, &model.AlbumArtist{}, &model.AlbumImportSession{})

	missingMetadata := model.Album{Title: "Missing metadata", EntryStatus: "open"}
	firstDuplicate := model.Album{Title: "Same Album", EntryStatus: "open", ReleaseYear: 2024}
	secondDuplicate := model.Album{Title: "same album", EntryStatus: "open", ReleaseYear: 2024}
	for _, album := range []*model.Album{&missingMetadata, &firstDuplicate, &secondDuplicate} {
		if err := db.Create(album).Error; err != nil {
			t.Fatalf("create album: %v", err)
		}
	}
	firstArtist := model.Artist{Name: "Duplicate Artist", EntryStatus: "open"}
	secondArtist := model.Artist{Name: "duplicate artist", EntryStatus: "open"}
	for _, artist := range []*model.Artist{&firstArtist, &secondArtist} {
		if err := db.Create(artist).Error; err != nil {
			t.Fatalf("create artist: %v", err)
		}
	}

	router := gin.New()
	router.GET("/quality", ListMusicQualityIssuesHandler(db))

	metadataIssues := requestMusicQualityIssues(t, router, "missing_metadata")
	if !hasMusicQualityIssue(metadataIssues, "missing_metadata", missingMetadata.ID.String()) {
		t.Fatalf("expected missing metadata issue, got %#v", metadataIssues)
	}
	duplicateIssues := requestMusicQualityIssues(t, router, "duplicate_candidate")
	if !hasMusicQualityIssue(duplicateIssues, "duplicate_candidate", firstDuplicate.ID.String()) || !hasMusicQualityIssue(duplicateIssues, "duplicate_candidate", secondDuplicate.ID.String()) {
		t.Fatalf("expected both duplicate candidates, got %#v", duplicateIssues)
	}
	if !hasMusicQualityIssue(duplicateIssues, "duplicate_candidate", firstArtist.ID.String()) || !hasMusicQualityIssue(duplicateIssues, "duplicate_candidate", secondArtist.ID.String()) {
		t.Fatalf("expected both duplicate artists, got %#v", duplicateIssues)
	}
}

func requestMusicQualityIssues(t *testing.T, router *gin.Engine, issueType string) []MusicQualityIssue {
	t.Helper()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/quality?type="+issueType, nil)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("quality response = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Data []MusicQualityIssue `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode quality response: %v", err)
	}
	return body.Data
}

func hasMusicQualityIssue(issues []MusicQualityIssue, issueType, entityID string) bool {
	for _, issue := range issues {
		if issue.Type == issueType && issue.EntityID == entityID {
			return true
		}
	}
	return false
}

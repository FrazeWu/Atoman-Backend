package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestListMusicQualityIssuesIncludesMetadataAndDuplicateCandidates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Artist{}, &model.Album{}, &model.Song{}, &model.AlbumArtist{}, &model.AlbumImportSession{}, &model.SongAudioReplacement{})

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
	failedSong := model.Song{Title: "Failed replacement", Status: "open"}
	if err := db.Create(&failedSong).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.SongAudioReplacement{SongID: failedSong.ID, RequestedBy: uuid.New(), AudioURL: "/new.mp3", Status: "failed"}).Error; err != nil {
		t.Fatal(err)
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
	processingIssues := requestMusicQualityIssues(t, router, "processing_failed")
	if !hasMusicQualityIssue(processingIssues, "processing_failed", failedSong.ID.String()) {
		t.Fatalf("expected failed replacement issue, got %#v", processingIssues)
	}
}

func TestListMusicQualityIssuesPaginatesWithAccurateTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Album{})
	for _, title := range []string{"A", "B", "C"} {
		if err := db.Create(&model.Album{Title: title, EntryStatus: "open"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	router.GET("/quality", ListMusicQualityIssuesHandler(db))
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/quality?type=missing_cover&page=2&page_size=2", nil)
	router.ServeHTTP(response, request)
	var body struct {
		Data    []MusicQualityIssue `json:"data"`
		Total   int                 `json:"total"`
		HasMore bool                `json:"has_more"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Total != 3 || len(body.Data) != 1 || body.HasMore {
		t.Fatalf("unexpected quality page: %d %#v", response.Code, body)
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

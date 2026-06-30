package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"
	"github.com/gin-gonic/gin"
)

func TestParseAlbumCorrectionReleaseDateRejectsInvalidNonEmptyDate(t *testing.T) {
	_, err := parseAlbumCorrectionReleaseDate("not-a-date")
	if err == nil {
		t.Fatal("expected invalid non-empty date to return an error")
	}
}

func TestParseAlbumCorrectionReleaseDateAllowsEmptyDate(t *testing.T) {
	parsed, err := parseAlbumCorrectionReleaseDate("")
	if err != nil {
		t.Fatalf("expected empty date to be allowed, got %v", err)
	}
	if parsed != nil {
		t.Fatalf("expected empty date to return nil, got %v", parsed)
	}
}

func TestCreateAlbumCorrectionRejectsInvalidReleaseDateWithoutCreatingRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Album{}, &model.AlbumCorrection{})

	user := model.User{
		Username: "correction-user",
		Email:    "correction-user@example.com",
		Password: "password",
		Role:     "user",
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	album := model.Album{
		Title:       "Original Album",
		Year:        2024,
		ReleaseDate: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("seed album: %v", err)
	}

	form := url.Values{}
	form.Set("album_id", album.ID.String())
	form.Set("corrected_release_date", "not-a-date")
	form.Set("reason", "fix release date")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/corrections/album", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("user_id", user.UUID)
	c.Set("role", user.Role)

	CreateAlbumCorrectionHandler(db, nil)(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d with body %s", w.Code, w.Body.String())
	}

	var count int64
	if err := db.Model(&model.AlbumCorrection{}).Count(&count).Error; err != nil {
		t.Fatalf("count album corrections: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no album correction to be created, got %d", count)
	}
}

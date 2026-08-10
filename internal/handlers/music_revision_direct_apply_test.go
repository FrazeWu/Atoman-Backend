package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestSemiProtectedMusicRevisionsApplyDirectly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.ArtistMember{},
		&model.Album{},
		&model.Song{},
		&model.AlbumArtist{},
		&model.SongArtist{},
		&model.MusicSongLyric{},
		&model.Revision{},
		&model.EditConflict{},
		&model.ContentProtection{},
	)

	user := model.User{Username: "music-editor", Email: "music-editor@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	artist := model.Artist{Name: "Artist Before", ArtistForm: "person", EntryStatus: "open"}
	album := model.Album{Title: "Album Before", AlbumType: "album", Status: "open", EntryStatus: "open"}
	song := model.Song{Title: "Song Before", Status: "open"}
	for _, value := range []any{&artist, &album, &song} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("create music entity: %v", err)
		}
	}
	protections := []struct {
		contentType string
		contentID   uuid.UUID
	}{
		{contentType: "artist", contentID: artist.ID},
		{contentType: "album", contentID: album.ID},
		{contentType: "song", contentID: song.ID},
	}
	for _, protection := range protections {
		if err := db.Create(&model.ContentProtection{
			ContentType:     protection.contentType,
			ContentID:       protection.contentID,
			ProtectionLevel: "semi",
			ProtectedBy:     user.UUID,
		}).Error; err != nil {
			t.Fatalf("create protection: %v", err)
		}
	}

	revisionService := service.NewRevisionService(db)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser})
		c.Next()
	})
	router.POST("/api/v1/artists/:id/revisions", CreateArtistRevisionHandler(db, revisionService, nil))
	router.POST("/api/v1/albums/:id/revisions", CreateAlbumRevisionHandler(db, revisionService, nil))
	router.POST("/api/v1/songs/:id/revisions", CreateSongRevisionHandler(db, revisionService, nil))

	tests := []struct {
		name        string
		contentType string
		path        string
		body        string
		contentID   string
		assert      func(*testing.T)
	}{
		{name: "artist", contentType: "artist", path: "/api/v1/artists/" + artist.ID.String() + "/revisions", body: `{"changes":{"name":"Artist After"},"edit_summary":"update"}`, contentID: artist.ID.String(), assert: func(t *testing.T) {
			var got model.Artist
			if err := db.First(&got, "id = ?", artist.ID).Error; err != nil || got.Name != "Artist After" {
				t.Fatalf("artist revision not applied: %#v, %v", got, err)
			}
		}},
		{name: "album", contentType: "album", path: "/api/v1/albums/" + album.ID.String() + "/revisions", body: `{"changes":{"title":"Album After"},"edit_summary":"update"}`, contentID: album.ID.String(), assert: func(t *testing.T) {
			var got model.Album
			if err := db.First(&got, "id = ?", album.ID).Error; err != nil || got.Title != "Album After" {
				t.Fatalf("album revision not applied: %#v, %v", got, err)
			}
		}},
		{name: "song", contentType: "song", path: "/api/v1/songs/" + song.ID.String() + "/revisions", body: `{"changes":{"title":"Song After"},"edit_summary":"update"}`, contentID: song.ID.String(), assert: func(t *testing.T) {
			var got model.Song
			if err := db.First(&got, "id = ?", song.ID).Error; err != nil || got.Title != "Song After" {
				t.Fatalf("song revision not applied: %#v, %v", got, err)
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("expected direct apply, got %d: %s", response.Code, response.Body.String())
			}
			test.assert(t)

			var revision model.Revision
			if err := db.Where("content_type = ? AND content_id = ? AND is_current = ?", test.contentType, test.contentID, true).
				Order("version_number DESC").First(&revision).Error; err != nil {
				t.Fatalf("load current revision: %v", err)
			}
			if revision.Status != "approved" {
				t.Fatalf("expected approved revision, got %q", revision.Status)
			}
		})
	}
}

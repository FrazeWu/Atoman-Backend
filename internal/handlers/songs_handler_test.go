package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestLegacySongCreateAndUpdateKeepLyricsWikiInSync(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Artist{}, &model.Album{}, &model.Song{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
	)
	user := createDeleteSongTestUser(t, db, "legacy-editor", authctx.RoleUser)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", user.UUID)
		c.Set("role", user.Role)
		c.Next()
	})
	r.POST("/api/v1/songs", CreateSongHandler(db, nil))
	r.PUT("/api/v1/songs/:id", UpdateSongHandler(db, nil))

	create := url.Values{"title": {"Legacy API Song"}, "artist": {"Artist"}, "audio_url": {"/song.mp3"}, "lyrics": {"first\nsecond"}}
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/songs", strings.NewReader(create.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createW := httptest.NewRecorder()
	r.ServeHTTP(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create song: %d %s", createW.Code, createW.Body.String())
	}
	var song model.Song
	if err := json.Unmarshal(createW.Body.Bytes(), &song); err != nil {
		t.Fatalf("decode created song: %v", err)
	}

	update := url.Values{"title": {"Legacy API Song"}, "artist": {"Artist"}, "album": {"Unknown Album"}, "audio_url": {"/song.mp3"}, "lyrics": {"updated"}}
	updateReq := httptest.NewRequest(http.MethodPut, "/api/v1/songs/"+song.ID.String(), strings.NewReader(update.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateW := httptest.NewRecorder()
	r.ServeHTTP(updateW, updateReq)
	if updateW.Code != http.StatusOK {
		t.Fatalf("update song: %d %s", updateW.Code, updateW.Body.String())
	}

	var lyric model.MusicSongLyric
	if err := db.First(&lyric, "song_id = ?", song.ID).Error; err != nil {
		t.Fatalf("load wiki lyric: %v", err)
	}
	var versionCount int64
	db.Model(&model.MusicSongLyricVersion{}).Where("song_id = ?", song.ID).Count(&versionCount)
	if lyric.Content != "updated" || lyric.Version != 2 || versionCount != 2 || lyric.UpdatedBy != user.UUID {
		t.Fatalf("unexpected synchronized lyric: %#v, versions=%d", lyric, versionCount)
	}
}

func TestApproveSongLyricsCorrectionKeepsLyricsWikiInSync(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.Song{}, &model.SongCorrection{},
		&model.MusicSongLyric{}, &model.MusicSongLyricLine{}, &model.MusicSongLyricVersion{},
		&model.MusicLyricAnnotation{}, &model.MusicLyricAnnotationVote{},
	)
	admin := createDeleteSongTestUser(t, db, "correction-admin", authctx.RoleAdmin)
	song := model.Song{Title: "Corrected", AudioURL: "/corrected.mp3", Lyrics: "old"}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	correction := model.SongCorrection{SongID: song.ID, Status: "pending", FieldName: "lyrics", CurrentValue: "old", CorrectedValue: "new"}
	if err := db.Create(&correction).Error; err != nil {
		t.Fatalf("create correction: %v", err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", admin.UUID)
		c.Next()
	})
	r.POST("/api/v1/admin/song-corrections/:id/approve", ApproveSongCorrectionHandler(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/admin/song-corrections/"+correction.ID.String()+"/approve", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("approve correction: %d %s", w.Code, w.Body.String())
	}
	var lyric model.MusicSongLyric
	if err := db.First(&lyric, "song_id = ?", song.ID).Error; err != nil {
		t.Fatalf("load corrected wiki lyric: %v", err)
	}
	if lyric.Content != "new" || lyric.UpdatedBy != admin.UUID || lyric.EditSummary != "通过歌词纠错更新" {
		t.Fatalf("unexpected corrected wiki lyric: %#v", lyric)
	}
}

func newDeleteSongTestRouter(t *testing.T, current authctx.CurrentUser) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Song{})

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, current)
		c.Next()
	})
	r.DELETE("/api/v1/songs/:id", DeleteSongHandler(db, nil))

	return r, db
}

func createDeleteSongTestUser(t *testing.T, db *gorm.DB, username, role string) model.User {
	t.Helper()

	user := model.User{
		Username: username,
		Email:    username + "@example.com",
		Password: "hash",
		Role:     role,
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func createDeleteSongTestSong(t *testing.T, db *gorm.DB, owner model.User) model.Song {
	t.Helper()

	song := model.Song{
		Title:      "Delete me",
		AudioURL:   "/uploads/song.mp3",
		UploadedBy: &owner.UUID,
	}
	if err := db.Create(&song).Error; err != nil {
		t.Fatalf("create song: %v", err)
	}
	return song
}

func TestDeleteSongHandlerRejectsNonOwnerUser(t *testing.T) {
	r, db := newDeleteSongTestRouter(t, authctx.CurrentUser{})
	owner := createDeleteSongTestUser(t, db, "owner", authctx.RoleUser)
	viewer := createDeleteSongTestUser(t, db, "viewer", authctx.RoleUser)
	song := createDeleteSongTestSong(t, db, owner)

	r = gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: viewer.UUID, Username: viewer.Username, Role: viewer.Role})
		c.Next()
	})
	r.DELETE("/api/v1/songs/:id", DeleteSongHandler(db, nil))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/songs/"+song.ID.String(), nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}

	var count int64
	if err := db.Model(&model.Song{}).Where("id = ?", song.ID).Count(&count).Error; err != nil {
		t.Fatalf("count song: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected song to remain, got count %d", count)
	}
}

func TestDeleteSongHandlerAllowsOwnerAdminAndSiteOwner(t *testing.T) {
	tests := []struct {
		name string
		role string
	}{
		{name: "song owner", role: authctx.RoleUser},
		{name: "admin", role: authctx.RoleAdmin},
		{name: "site owner", role: authctx.RoleOwner},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, db := newDeleteSongTestRouter(t, authctx.CurrentUser{})
			owner := createDeleteSongTestUser(t, db, "owner", authctx.RoleUser)
			current := owner
			if tt.role != authctx.RoleUser {
				current = createDeleteSongTestUser(t, db, "current", tt.role)
			}
			song := createDeleteSongTestSong(t, db, owner)

			r = gin.New()
			r.Use(func(c *gin.Context) {
				authctx.SetCurrentUser(c, authctx.CurrentUser{ID: current.UUID, Username: current.Username, Role: current.Role})
				c.Next()
			})
			r.DELETE("/api/v1/songs/:id", DeleteSongHandler(db, nil))

			req := httptest.NewRequest(http.MethodDelete, "/api/v1/songs/"+song.ID.String(), nil)
			w := httptest.NewRecorder()

			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

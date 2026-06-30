package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

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

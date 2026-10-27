package music

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
)

func TestAdminArchiveManagementRoutesRejectUsersAndRestoreWithoutDeletingMedia(t *testing.T) {
	svc, db, user := newMusicHTTPTestService(t)
	song := model.Song{Title: "Management example", AudioURL: "https://cdn.example.test/song.mp3", Status: "open", LifecycleStatus: model.MusicLifecycleActive}
	if err := db.Create(&song).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/music/entries/song/" + song.ID.String()
	get := func(actor *authctx.CurrentUser, endpoint string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		newMusicHTTPRouter(svc, actor).ServeHTTP(w, httptest.NewRequest(http.MethodGet, endpoint, nil))
		return w
	}
	if w := get(nil, "/api/v1/music/admin/archived"); w.Code != http.StatusUnauthorized {
		t.Fatalf("guest list got %d", w.Code)
	}
	if w := get(&user, "/api/v1/music/admin/archived"); w.Code != http.StatusForbidden {
		t.Fatalf("user list got %d", w.Code)
	}
	user.Role = authctx.RoleAdmin
	request := func(endpoint string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		newMusicHTTPRouter(svc, &user).ServeHTTP(w, httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"reason":"moderation test"}`)))
		return w
	}
	if w := request(path + "/archive"); w.Code != http.StatusNoContent {
		t.Fatalf("archive got %d: %s", w.Code, w.Body.String())
	}
	if w := get(&user, "/api/v1/music/admin/archived"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Management example") || !strings.Contains(w.Body.String(), "moderation test") {
		t.Fatalf("admin list got %d: %s", w.Code, w.Body.String())
	}
	reader := user
	reader.Role = authctx.RoleUser
	if w := get(&reader, "/api/v1/music/songs/"+song.ID.String()); w.Code != http.StatusNotFound {
		t.Fatalf("archived song leaked to normal user: %d", w.Code)
	}
	if w := request(path + "/restore"); w.Code != http.StatusNoContent {
		t.Fatalf("restore got %d: %s", w.Code, w.Body.String())
	}
	var restored model.Song
	if err := db.First(&restored, "id = ?", song.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.LifecycleStatus != model.MusicLifecycleActive || restored.AudioURL != song.AudioURL {
		t.Fatal("restore failed or altered media")
	}
}

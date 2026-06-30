package music

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newMusicHTTPTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Artist{},
		&model.Album{},
		&model.Song{},
		&model.MusicEdit{},
		&model.MusicEditVote{},
		&model.MusicEditDecision{},
		&model.MusicEditChange{},
		&model.AuditLog{},
	)

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func newMusicHTTPRouter(service *Service, current *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if current != nil {
			authctx.SetCurrentUser(c, *current)
		}
		c.Next()
	})
	v1 := r.Group("/api/v1")
	RegisterRoutes(v1.Group("/music"), service)
	return r
}

func TestRegisterRoutesSubmitEditReturnsCreatedAppliedEditForMainWikiFlow(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	body := map[string]any{
		"type":        "create_artist",
		"entity_type": "artist",
		"payload": map[string]any{
			"name": "HTTP Artist",
		},
		"changes": map[string]any{},
		"reason":  "new artist",
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/edits", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data model.MusicEdit `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.ID == uuid.Nil || resp.Data.Status != "applied" || !resp.Data.AutoApplied || resp.Data.SubmittedBy != user.ID {
		t.Fatalf("unexpected response edit: %#v", resp.Data)
	}

	var persisted model.MusicEdit
	if err := db.First(&persisted, "id = ?", resp.Data.ID).Error; err != nil {
		t.Fatalf("load persisted edit: %v", err)
	}
	if persisted.Status != "applied" || persisted.Type != "create_artist" {
		t.Fatalf("unexpected persisted edit: %#v", persisted)
	}
}

func TestRegisterRoutesListsArtistsThroughMusicV1(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Visible Artist", Bio: "bio", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/artists?q=visible", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Artist `json:"data"`
		Meta struct {
			Page     int  `json:"page"`
			PageSize int  `json:"page_size"`
			Total    int  `json:"total"`
			HasMore  bool `json:"has_more"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Name != "Visible Artist" {
		t.Fatalf("unexpected artists response: %#v", resp.Data)
	}
	if resp.Meta.Page != 1 || resp.Meta.PageSize != 20 || resp.Meta.Total != 1 || resp.Meta.HasMore {
		t.Fatalf("unexpected pagination meta: %#v", resp.Meta)
	}
}

func TestRegisterRoutesListsMusicEditsForModerator(t *testing.T) {
	service, db, _ := newMusicHTTPTestService(t)
	moderator := authctx.CurrentUser{ID: uuid.New(), Username: "mod", Role: authctx.RoleModerator}
	if err := db.Create(&model.User{
		UUID:     moderator.ID,
		Username: moderator.Username,
		Email:    "mod@example.com",
		Password: "hash",
		Role:     moderator.Role,
		IsActive: true,
	}).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}
	edit := model.MusicEdit{
		Type:        "create_artist",
		EntityType:  "artist",
		SubmittedBy: moderator.ID,
		Status:      "open",
		Reason:      "seed review queue",
		PayloadJSON: "{}",
		ChangesJSON: "{}",
		SourcesJSON: "[]",
		Votable:     true,
	}
	if err := db.Create(&edit).Error; err != nil {
		t.Fatalf("create music edit: %v", err)
	}
	r := newMusicHTTPRouter(service, &moderator)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/edits?status=open&page_size=10", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []model.MusicEdit `json:"data"`
		Meta struct {
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Total    int64 `json:"total"`
			HasMore  bool  `json:"has_more"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != edit.ID {
		t.Fatalf("unexpected edits response: %#v", resp.Data)
	}
	if resp.Meta.Total != 1 || resp.Meta.Page != 1 || resp.Meta.PageSize != 10 || resp.Meta.HasMore {
		t.Fatalf("unexpected meta: %#v", resp.Meta)
	}
}

func TestRegisterRoutesListAlbumsSortsByHotScore(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Discovery Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	albums := []model.Album{
		{Title: "Low Heat", EntryStatus: "open", Status: "open", HotScore: 1.5},
		{Title: "High Heat", EntryStatus: "open", Status: "open", HotScore: 42.25},
		{Title: "Mid Heat", EntryStatus: "open", Status: "open", HotScore: 7},
	}
	for i := range albums {
		if err := db.Create(&albums[i]).Error; err != nil {
			t.Fatalf("create album %d: %v", i, err)
		}
		if err := db.Model(&albums[i]).Association("Artists").Append(&artist); err != nil {
			t.Fatalf("append artist to album %d: %v", i, err)
		}
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums?sort=hot&page_size=10", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Album `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 3 || resp.Meta.Total != 3 {
		t.Fatalf("unexpected album count: data=%#v meta=%#v", resp.Data, resp.Meta)
	}
	gotTitles := []string{resp.Data[0].Title, resp.Data[1].Title, resp.Data[2].Title}
	wantTitles := []string{"High Heat", "Mid Heat", "Low Heat"}
	for i := range wantTitles {
		if gotTitles[i] != wantTitles[i] {
			t.Fatalf("expected hot order %v, got %v", wantTitles, gotTitles)
		}
	}
	if resp.Data[0].HotScore != 42.25 {
		t.Fatalf("expected hot_score in response, got %#v", resp.Data[0])
	}
}

func TestRegisterRoutesListAlbumsSearchesArtistNames(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	visibleArtist := model.Artist{Name: "Searchable Artist", EntryStatus: "open"}
	otherArtist := model.Artist{Name: "Other Artist", EntryStatus: "open"}
	if err := db.Create(&visibleArtist).Error; err != nil {
		t.Fatalf("create visible artist: %v", err)
	}
	if err := db.Create(&otherArtist).Error; err != nil {
		t.Fatalf("create other artist: %v", err)
	}
	visibleAlbum := model.Album{Title: "Title Does Not Match", EntryStatus: "open", Status: "open"}
	otherAlbum := model.Album{Title: "Other Album", EntryStatus: "open", Status: "open"}
	if err := db.Create(&visibleAlbum).Error; err != nil {
		t.Fatalf("create visible album: %v", err)
	}
	if err := db.Create(&otherAlbum).Error; err != nil {
		t.Fatalf("create other album: %v", err)
	}
	if err := db.Model(&visibleAlbum).Association("Artists").Append(&visibleArtist); err != nil {
		t.Fatalf("append visible artist: %v", err)
	}
	if err := db.Model(&otherAlbum).Association("Artists").Append(&otherArtist); err != nil {
		t.Fatalf("append other artist: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums?q=searchable", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Album `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Meta.Total != 1 || resp.Data[0].Title != "Title Does Not Match" {
		t.Fatalf("unexpected artist-name search response: data=%#v meta=%#v", resp.Data, resp.Meta)
	}
}

func TestRegisterRoutesListAlbumsSearchesArtistNamesWithHotSort(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	artist := model.Artist{Name: "Ranked Artist", EntryStatus: "open"}
	if err := db.Create(&artist).Error; err != nil {
		t.Fatalf("create artist: %v", err)
	}
	lowAlbum := model.Album{Title: "Low Ranked", EntryStatus: "open", Status: "open", HotScore: 1}
	highAlbum := model.Album{Title: "High Ranked", EntryStatus: "open", Status: "open", HotScore: 9}
	if err := db.Create(&lowAlbum).Error; err != nil {
		t.Fatalf("create low album: %v", err)
	}
	if err := db.Create(&highAlbum).Error; err != nil {
		t.Fatalf("create high album: %v", err)
	}
	if err := db.Model(&lowAlbum).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append low artist: %v", err)
	}
	if err := db.Model(&highAlbum).Association("Artists").Append(&artist); err != nil {
		t.Fatalf("append high artist: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/albums?q=ranked&sort=hot", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []model.Album `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 2 || resp.Data[0].Title != "High Ranked" || resp.Data[1].Title != "Low Ranked" {
		t.Fatalf("expected searched albums in hot order, got %#v", resp.Data)
	}
}

func TestMusicRecommendationModeValidation(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/recommend/albums?mode=bad", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMusicRecommendationAlbumsReturnsData(t *testing.T) {
	service, db, user := newMusicHTTPTestService(t)
	album := model.Album{
		Title:       "Recommend Me",
		EntryStatus: "open",
		Status:      "open",
		HotScore:    8.5,
	}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/recommend/albums?mode=hot", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			Summary    string `json:"summary"`
			ImageURL   string `json:"image_url"`
			TargetPath string `json:"target_path"`
			ScoreLabel string `json:"score_label"`
		} `json:"data"`
		Meta struct {
			Total int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) == 0 {
		t.Fatalf("expected recommendation data, got %s", w.Body.String())
	}
	first := resp.Data[0]
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" {
		t.Fatalf("expected lightweight recommendation dto fields, got %#v", first)
	}
	if first.TargetPath != "/music/album/"+album.ID.String() {
		t.Fatalf("expected target path %s, got %s", "/music/album/"+album.ID.String(), first.TargetPath)
	}
	if resp.Meta.Total == 0 {
		t.Fatalf("expected total > 0, got %#v", resp.Meta)
	}
}

func TestAlbumSortOrdersSupportsRandomMode(t *testing.T) {
	got := albumSortOrders("random")

	if len(got) != 1 || got[0] != "RANDOM()" {
		t.Fatalf("expected RANDOM() order, got %#v", got)
	}
}

func TestRegisterRoutesSubmitEditRequiresCurrentUser(t *testing.T) {
	service, _, _ := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/edits", bytes.NewBufferString(`{"type":"create_artist","entity_type":"artist","reason":"new"}`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRegisterRoutesSubmitEditRejectsInvalidJSON(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/edits", bytes.NewBufferString(`{"type":`))
	req.Header.Set("Content-Type", "application/json")

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "validation.invalid_request") {
		t.Fatalf("expected validation.invalid_request, got %s", w.Body.String())
	}
}

func TestRegisterRoutesGetEditRejectsInvalidUUID(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	r := newMusicHTTPRouter(service, &user)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/music/edits/not-a-uuid", nil)

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "validation.invalid_request") {
		t.Fatalf("expected validation.invalid_request, got %s", w.Body.String())
	}
}

func TestRegisterRoutesGetEditRequiresSubmitterOrModerator(t *testing.T) {
	service, db, submitter := newMusicHTTPTestService(t)
	edit, err := service.SubmitEdit(submitter, SubmitEditRequest{Type: "create_artist", EntityType: "artist", Payload: map[string]any{"name": "HTTP Artist"}, Reason: "new artist"})
	if err != nil {
		t.Fatalf("submit edit: %v", err)
	}

	otherUserModel := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&otherUserModel).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	otherUser := authctx.CurrentUser{ID: otherUserModel.UUID, Username: otherUserModel.Username, Role: authctx.RoleUser}
	moderatorModel := model.User{Username: "mod", Email: "mod-http@example.com", Password: "hash", Role: authctx.RoleModerator, IsActive: true}
	if err := db.Create(&moderatorModel).Error; err != nil {
		t.Fatalf("create moderator: %v", err)
	}
	moderator := authctx.CurrentUser{ID: moderatorModel.UUID, Username: moderatorModel.Username, Role: authctx.RoleModerator}

	rSubmitter := newMusicHTTPRouter(service, &submitter)
	wSubmitter := httptest.NewRecorder()
	rSubmitter.ServeHTTP(wSubmitter, httptest.NewRequest(http.MethodGet, "/api/v1/music/edits/"+edit.ID.String(), nil))
	if wSubmitter.Code != http.StatusOK {
		t.Fatalf("expected submitter 200, got %d: %s", wSubmitter.Code, wSubmitter.Body.String())
	}

	rOther := newMusicHTTPRouter(service, &otherUser)
	wOther := httptest.NewRecorder()
	rOther.ServeHTTP(wOther, httptest.NewRequest(http.MethodGet, "/api/v1/music/edits/"+edit.ID.String(), nil))
	if wOther.Code != http.StatusForbidden {
		t.Fatalf("expected other user 403, got %d: %s", wOther.Code, wOther.Body.String())
	}

	rModerator := newMusicHTTPRouter(service, &moderator)
	wModerator := httptest.NewRecorder()
	rModerator.ServeHTTP(wModerator, httptest.NewRequest(http.MethodGet, "/api/v1/music/edits/"+edit.ID.String(), nil))
	if wModerator.Code != http.StatusOK {
		t.Fatalf("expected moderator 200, got %d: %s", wModerator.Code, wModerator.Body.String())
	}
}

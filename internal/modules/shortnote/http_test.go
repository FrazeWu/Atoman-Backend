package shortnote

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func newShortNoteHTTPTestService(t *testing.T) (*Service, *gorm.DB, authctx.CurrentUser) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.ShortNote{}, &model.ShortNoteMedia{}, &model.Like{}, &model.DiscussionTarget{})
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewService(db), db, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser}
}

func newShortNoteHTTPRouter(service *Service, current *authctx.CurrentUser) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if current != nil {
			authctx.SetCurrentUser(c, *current)
		}
		c.Next()
	})
	RegisterRoutes(r.Group("/api/v1/short-notes"), service)
	return r
}

func shortNoteRequest(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func responseID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var payload struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.ID == "" {
		t.Fatalf("missing id: %s", w.Body.String())
	}
	return payload.Data.ID
}

func TestShortNoteCreateAndDetailIncludesMediaAndCounts(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	r := newShortNoteHTTPRouter(service, &user)
	w := shortNoteRequest(t, r, http.MethodPost, "/api/v1/short-notes", `{"content":"hello","media_urls":["https://example.test/one.jpg","https://example.test/two.jpg"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	id := responseID(t, w)

	var note model.ShortNote
	if err := db.Preload("Media").First(&note, "id = ?", id).Error; err != nil {
		t.Fatalf("load note: %v", err)
	}
	if note.UserID != user.ID || note.Content != "hello" || len(note.Media) != 2 || note.Media[0].Position != 0 || note.Media[1].Position != 1 {
		t.Fatalf("unexpected persisted note: %#v", note)
	}
	if err := db.Create(&model.Like{UserID: user.ID, TargetType: "short_note", TargetID: note.ID}).Error; err != nil {
		t.Fatalf("create like: %v", err)
	}
	if err := db.Create(&model.DiscussionTarget{Kind: "short_note", ResourceID: note.ID, ResourceKey: note.ID.String(), CommentCount: 3, NextFloor: 1}).Error; err != nil {
		t.Fatalf("create target: %v", err)
	}
	w = shortNoteRequest(t, r, http.MethodGet, "/api/v1/short-notes/"+id, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"likes_count":1`) || !strings.Contains(w.Body.String(), `"comments_count":3`) {
		t.Fatalf("unexpected detail: %d %s", w.Code, w.Body.String())
	}
}

func TestShortNoteRejectsBlankOverlongAndTooManyImages(t *testing.T) {
	service, _, user := newShortNoteHTTPTestService(t)
	r := newShortNoteHTTPRouter(service, &user)
	for _, body := range []string{
		`{"content":"   "}`,
		`{"content":"` + strings.Repeat("字", 501) + `"}`,
		`{"content":"valid","media_urls":["1","2","3","4","5","6","7","8","9","10"]}`,
	} {
		w := shortNoteRequest(t, r, http.MethodPost, "/api/v1/short-notes", body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	}
}

func TestShortNoteTimelineOrdersByCreationAndPaginates(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	r := newShortNoteHTTPRouter(service, nil)
	first := model.ShortNote{UserID: user.ID, Content: "first"}
	second := model.ShortNote{UserID: user.ID, Content: "second"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&first).Update("created_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	w := shortNoteRequest(t, r, http.MethodGet, "/api/v1/short-notes?page=1&page_size=1", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"content":"second"`) || strings.Contains(w.Body.String(), `"content":"first"`) || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatalf("unexpected timeline: %d %s", w.Code, w.Body.String())
	}
}

func TestShortNoteOnlyAuthorCanUpdateOrDeleteAndUpdateMarksEdited(t *testing.T) {
	service, db, owner := newShortNoteHTTPTestService(t)
	note := model.ShortNote{UserID: owner.ID, Content: "before"}
	if err := db.Create(&note).Error; err != nil {
		t.Fatal(err)
	}
	other := model.User{Username: "bob", Email: "bob@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	otherCurrent := authctx.CurrentUser{ID: other.UUID, Username: other.Username, Role: other.Role}
	foreign := newShortNoteHTTPRouter(service, &otherCurrent)
	if w := shortNoteRequest(t, foreign, http.MethodPut, "/api/v1/short-notes/"+note.ID.String(), `{"content":"after"}`); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 update, got %d: %s", w.Code, w.Body.String())
	}
	if w := shortNoteRequest(t, foreign, http.MethodDelete, "/api/v1/short-notes/"+note.ID.String(), ""); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 delete, got %d: %s", w.Code, w.Body.String())
	}

	r := newShortNoteHTTPRouter(service, &owner)
	w := shortNoteRequest(t, r, http.MethodPut, "/api/v1/short-notes/"+note.ID.String(), `{"content":"after","media_urls":["https://example.test/new.jpg"]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"edited":true`) {
		t.Fatalf("unexpected update: %d %s", w.Code, w.Body.String())
	}
	if w = shortNoteRequest(t, r, http.MethodDelete, "/api/v1/short-notes/"+note.ID.String(), ""); w.Code != http.StatusOK {
		t.Fatalf("unexpected delete: %d %s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&model.ShortNote{}).Where("id = ?", note.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expected soft deletion, count=%d err=%v", count, err)
	}
}

func TestShortNoteLikeIsIdempotent(t *testing.T) {
	service, db, user := newShortNoteHTTPTestService(t)
	note := model.ShortNote{UserID: user.ID, Content: "note"}
	if err := db.Create(&note).Error; err != nil {
		t.Fatal(err)
	}
	r := newShortNoteHTTPRouter(service, &user)
	path := "/api/v1/short-notes/" + note.ID.String() + "/like"
	for range 2 {
		if w := shortNoteRequest(t, r, http.MethodPost, path, ""); w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}
	var count int64
	if err := db.Model(&model.Like{}).Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, "short_note", note.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one like, count=%d err=%v", count, err)
	}
	for range 2 {
		if w := shortNoteRequest(t, r, http.MethodDelete, path, ""); w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
	}
	if err := db.Model(&model.Like{}).Where("user_id = ? AND target_type = ? AND target_id = ?", user.ID, "short_note", note.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("expected no active likes, count=%d err=%v", count, err)
	}
}

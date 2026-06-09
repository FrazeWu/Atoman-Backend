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

func TestRegisterRoutesSubmitEditReturnsCreatedOpenEdit(t *testing.T) {
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
	if resp.Data.ID == uuid.Nil || resp.Data.Status != "open" || resp.Data.SubmittedBy != user.ID {
		t.Fatalf("unexpected response edit: %#v", resp.Data)
	}

	var persisted model.MusicEdit
	if err := db.First(&persisted, "id = ?", resp.Data.ID).Error; err != nil {
		t.Fatalf("load persisted edit: %v", err)
	}
	if persisted.Status != "open" || persisted.Type != "create_artist" {
		t.Fatalf("unexpected persisted edit: %#v", persisted)
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

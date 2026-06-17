package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"
)

func newBlogChannelTestRouter(t *testing.T, dbModels ...any) (*gin.Engine, *gorm.DB, model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")

	db := testdb.Open(t)
	models := []any{&model.User{}, &model.Channel{}, &model.Collection{}, &model.FeedSource{}, &model.SubscriptionGroup{}, &model.Subscription{}}
	models = append(models, dbModels...)
	testdb.Migrate(t, db, models...)

	user := model.User{Username: "owner", Email: "owner@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	SetupBlogChannelRoutes(r, db)
	return r, db, user
}

func blogChannelAuthHeader(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return "Bearer " + signed
}

func TestCreateChannelRejectsReservedSlug(t *testing.T) {
	r, _, user := newBlogChannelTestRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels", strings.NewReader(`{"name":"Feed","slug":"feed"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", blogChannelAuthHeader(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "reserved") {
		t.Fatalf("expected reserved error, got %s", w.Body.String())
	}
}

func TestCreateChannelRejectsSlugMatchingUsername(t *testing.T) {
	r, db, user := newBlogChannelTestRouter(t)
	other := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/channels", strings.NewReader(`{"name":"Alice Channel","slug":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", blogChannelAuthHeader(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already in use") {
		t.Fatalf("expected already in use error, got %s", w.Body.String())
	}
}

func TestUpdateChannelRejectsSlugMatchingUsername(t *testing.T) {
	r, db, user := newBlogChannelTestRouter(t)
	channel := model.Channel{UserID: &user.UUID, Name: "Original", Slug: "original"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	other := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&other).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/v1/blog/channels/"+channel.ID.String(), strings.NewReader(`{"name":"Original","slug":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", blogChannelAuthHeader(t, user))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already in use") {
		t.Fatalf("expected already in use error, got %s", w.Body.String())
	}
}

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"gorm.io/gorm"

	"atoman/internal/middleware"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/testdb"
)

func newBlogInteractionTestDB(t *testing.T) (*gin.Engine, *gorm.DB, model.User, model.Post) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Post{},
		&model.Comment{},
		&model.Like{},
		&model.Bookmark{},
		&model.BookmarkFolder{},
		&model.SiteSetting{},
	)

	if err := migrations.RunBlogInteractionUniqueIndexes(db); err != nil {
		t.Fatalf("run blog interaction unique indexes migration: %v", err)
	}

	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	post := model.Post{UserID: user.UUID, Title: "Public Post", Content: "Body", Status: "published", Visibility: "public", AllowComments: true}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", user.UUID)
		c.Next()
	})
	SetupBlogInteractionRoutes(r, db)
	return r, db, user, post
}

func blogInteractionAuthHeader(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return "Bearer " + signed
}

func TestToggleLikeIsIdempotentWithRepeatedCreateRequests(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	r, db, user, post := newBlogInteractionTestDB(t)

	payload := map[string]any{
		"target_type": "post",
		"target_id":   post.ID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/likes", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", blogInteractionAuthHeader(t, user))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("request %d status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}

	var count int64
	if err := db.Model(&model.Like{}).Where("target_type = ? AND target_id = ?", "post", post.ID).Count(&count).Error; err != nil {
		t.Fatalf("count likes: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 like row, got %d", count)
	}
}

func TestCreateBookmarkIsIdempotentWithRepeatedCreateRequests(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	r, db, user, post := newBlogInteractionTestDB(t)

	payload := map[string]any{
		"post_id": post.ID,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/bookmarks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", blogInteractionAuthHeader(t, user))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("request %d status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}

	var count int64
	if err := db.Model(&model.Bookmark{}).Where("post_id = ?", post.ID).Count(&count).Error; err != nil {
		t.Fatalf("count bookmarks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 bookmark row, got %d", count)
	}
}

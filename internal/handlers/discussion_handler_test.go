package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestAlbumDiscussionUnreadCountUsesPerUserReadState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Discussion{}, &model.DiscussionReadState{})

	albumID := uuid.New()
	discussion := model.Discussion{
		ContentType: "album",
		ContentID:   albumID,
		UserID:      uuid.New(),
		Content:     "needs review",
		Status:      "active",
	}
	if err := db.Create(&discussion).Error; err != nil {
		t.Fatalf("create discussion: %v", err)
	}

	userA := uuid.New()
	userB := uuid.New()
	if err := db.Create(&model.DiscussionReadState{
		DiscussionID: discussion.ID,
		UserID:       userA,
		ReadAt:       time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("insert read state: %v", err)
	}

	if got := requestAlbumDiscussionUnreadCount(t, db, albumID, userA); got != 0 {
		t.Fatalf("expected user A unread count 0 after reading discussion, got %d", got)
	}
	if got := requestAlbumDiscussionUnreadCount(t, db, albumID, userB); got != 1 {
		t.Fatalf("expected user B unread count 1 when only user A read discussion, got %d", got)
	}
}

func requestAlbumDiscussionUnreadCount(t *testing.T, db *gorm.DB, albumID uuid.UUID, userID uuid.UUID) int64 {
	t.Helper()

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Next()
	})
	r.GET("/albums/:id/discussions/unread-count", GetAlbumDiscussionUnreadCountHandler(db))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/albums/"+albumID.String()+"/discussions/unread-count", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Data struct {
			UnreadCount int64 `json:"unread_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response.Data.UnreadCount
}

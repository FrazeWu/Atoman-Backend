package notification

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestUnreadCountsRequiresAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.Notification{}, &model.DMConversation{}, &model.DMMessage{})

	r := gin.New()
	RegisterRoutes(r.Group("/api/v1"), NewService(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-counts", nil))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestUnreadCountsReturnsNotificationCategoriesAndDMTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{}, &model.DMConversation{}, &model.DMMessage{})
	user := model.User{Username: "notify-count-user", Email: "notify-count@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	other := model.User{Username: "notify-count-other", Email: "notify-count-other@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&[]model.User{user, other}).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	if err := db.Where("username = ?", user.Username).First(&user).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if err := db.Where("username = ?", other.Username).First(&other).Error; err != nil {
		t.Fatalf("reload other: %v", err)
	}
	readAt := time.Now()
	notifications := []model.Notification{
		{RecipientID: user.UUID, Type: "comment_like"},
		{RecipientID: user.UUID, Type: "comment_marked"},
		{RecipientID: user.UUID, Type: "forum_follow"},
		{RecipientID: user.UUID, Type: "comment_mention"},
		{RecipientID: user.UUID, Type: "comment_reply"},
		{RecipientID: user.UUID, Type: "collaboration.required"},
		{RecipientID: user.UUID, Type: "future_unknown"},
		{RecipientID: user.UUID, Type: "comment_like", ReadAt: &readAt},
		{RecipientID: other.UUID, Type: "comment_like"},
	}
	if err := db.Create(&notifications).Error; err != nil {
		t.Fatalf("create notifications: %v", err)
	}
	conversation := model.DMConversation{ParticipantA: user.UUID, ParticipantB: other.UUID}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := db.Create(&[]model.DMMessage{
		{ConversationID: conversation.ID, SenderID: other.UUID, Content: "unread"},
		{ConversationID: conversation.ID, SenderID: user.UUID, Content: "outgoing"},
		{ConversationID: conversation.ID, SenderID: other.UUID, Content: "read", ReadAt: &readAt},
	}).Error; err != nil {
		t.Fatalf("create messages: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role})
		c.Next()
	})
	RegisterRoutes(r.Group("/api/v1"), NewService(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-counts", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Total int64            `json:"total"`
			Items map[string]int64 `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := map[string]int64{"like": 1, "interaction": 2, "mention": 1, "reply": 1, "collaboration": 1, "system": 1, "dm": 1}
	if response.Data.Total != 8 {
		t.Fatalf("expected total 8, got %d", response.Data.Total)
	}
	for category, count := range want {
		if response.Data.Items[category] != count {
			t.Fatalf("expected %s count %d, got %d", category, count, response.Data.Items[category])
		}
	}
}

func TestMarkAllReadReturnsRemainingUnreadTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{})
	user := model.User{Username: "notify-user", Email: "notify@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&[]model.Notification{
		{RecipientID: user.UUID, Type: "forum_reply"},
		{RecipientID: user.UUID, Type: "dm_message"},
	}).Error; err != nil {
		t.Fatalf("create notifications: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role})
		c.Next()
	})
	RegisterRoutes(r.Group("/api/v1"), NewService(db))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/notifications/read-all?type=forum_reply", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			UnreadTotal int64 `json:"unread_total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.UnreadTotal != 1 {
		t.Fatalf("expected one remaining unread notification, got %d", response.Data.UnreadTotal)
	}
}

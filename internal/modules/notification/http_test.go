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
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Notification{}, &model.DMConversation{}, &model.DMMessage{})
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
		{RecipientID: user.UUID, Type: "content_mention"},
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
	want := map[string]int64{"like": 1, "interaction": 2, "mention": 2, "reply": 1, "collaboration": 1, "system": 1, "dm": 1}
	if response.Data.Total != 9 {
		t.Fatalf("expected total 9, got %d", response.Data.Total)
	}
	for category, count := range want {
		if response.Data.Items[category] != count {
			t.Fatalf("expected %s count %d, got %d", category, count, response.Data.Items[category])
		}
	}
}

func TestUnreadCountsIncludesMessagesToOwnedChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Channel{}, &model.Notification{}, &model.DMConversation{}, &model.DMMessage{})
	owner := model.User{Username: "notify-channel-owner", Email: "owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	sender := model.User{Username: "notify-channel-sender", Email: "sender@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	if err := db.Create(&sender).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	channel := model.Channel{UserID: &owner.UUID, Name: "notify-owned-channel", Slug: "notify-owned-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: sender.UUID, ParticipantBType: model.DMPartyChannel, ParticipantB: channel.ID}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if err := db.Create(&model.DMMessage{ConversationID: conversation.ID, SenderType: model.DMPartyUser, SenderID: sender.UUID, ActorUserID: sender.UUID, Content: "unread"}).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: owner.UUID, Username: owner.Username, Role: owner.Role})
		c.Next()
	})
	RegisterRoutes(r.Group("/api/v1"), NewService(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/notifications/unread-counts", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data UnreadCountsDTO `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.Items["dm"] != 1 || response.Data.Total != 1 {
		t.Fatalf("expected one owned-channel unread, got %#v", response.Data)
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

func TestNotificationCategoryEndpointsFilterAndMarkCategory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Notification{})
	user := model.User{Username: "notify-category-user", Email: "notify-category@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&[]model.Notification{
		{RecipientID: user.UUID, Type: "comment_reply", SourceType: "test", SourceID: user.UUID},
		{RecipientID: user.UUID, Type: "comment_like", SourceType: "test", SourceID: user.UUID},
		{RecipientID: user.UUID, Type: "future.notification", SourceType: "test", SourceID: user.UUID},
	}).Error; err != nil {
		t.Fatalf("create notifications: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: user.Role})
		c.Next()
	})
	RegisterRoutes(r.Group("/api/v1"), NewService(db))

	list := httptest.NewRecorder()
	r.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/notifications?category=reply", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", list.Code, list.Body.String())
	}
	var listed struct {
		Data []NotificationDTO `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Data) != 1 || listed.Data[0].Type != "comment_reply" {
		t.Fatalf("expected only reply notification, got %#v", listed.Data)
	}

	mark := httptest.NewRecorder()
	r.ServeHTTP(mark, httptest.NewRequest(http.MethodPut, "/api/v1/notifications/read-all?category=system", nil))
	if mark.Code != http.StatusOK {
		t.Fatalf("expected mark 200, got %d: %s", mark.Code, mark.Body.String())
	}
	var unread int64
	if err := db.Model(&model.Notification{}).Where("recipient_id = ? AND read_at IS NULL", user.UUID).Count(&unread).Error; err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if unread != 2 {
		t.Fatalf("expected two known-category notifications to remain unread, got %d", unread)
	}
}

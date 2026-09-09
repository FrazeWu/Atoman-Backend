package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestUserRelationsRespectPrivacyAndIncludeChannelSubscriptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.UserSettings{}, &model.Follow{}, &model.Channel{}, &model.FeedSource{}, &model.Subscription{})

	target := model.User{Username: "relation-target", Email: "relation-target@example.com", Password: "hash", Role: "user", IsActive: true}
	follower := model.User{Username: "relation-follower", Email: "relation-follower@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&follower).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.UserSettings{UserID: target.UUID, ShowRelations: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Follow{FollowerID: target.UUID, FollowingID: follower.UUID}).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &target.UUID, Name: "目标频道", Slug: "relation-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	source := model.FeedSource{SourceType: "internal_channel", SourceID: &channel.ID, Provider: "internal", Category: "mixed", Title: channel.Name, Hash: uuid.NewString()}
	if err := db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Subscription{UserID: target.UUID, FeedSourceID: source.ID}).Error; err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.GET("/users/:id/following", GetUserFollowing(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/"+target.UUID.String()+"/following", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("expected user and channel, got %#v", response.Data)
	}
	if _, leaked := response.Data[0]["email"]; leaked {
		t.Fatalf("public relation leaked email: %#v", response.Data[0])
	}

	if err := db.Model(&model.UserSettings{}).Where("user_id = ?", target.UUID).Update("show_relations", false).Error; err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/"+target.UUID.String()+"/following", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected private relations to return 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestFollowUserRejectsNewFollowToPrivateProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.UserSettings{}, &model.Follow{})
	actor := model.User{Username: "private-follow-actor", Email: "private-follow-actor@example.com", Password: "hash", Role: "user", IsActive: true}
	target := model.User{Username: "private-follow-target", Email: "private-follow-target@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&actor).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&target).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.UserSettings{UserID: target.UUID, PrivateProfile: true}).Error; err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: actor.UUID, Username: actor.Username, Role: authctx.RoleUser})
		c.Next()
	})
	r.POST("/users/:id/follow", FollowUser(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+target.UUID.String()+"/follow", nil))

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
	var follow model.Follow
	if err := db.Where("follower_id = ? AND following_id = ?", actor.UUID, target.UUID).First(&follow).Error; err == nil {
		t.Fatal("private profile should not create a new follow")
	}
	if err := db.Create(&model.Follow{FollowerID: actor.UUID, FollowingID: target.UUID}).Error; err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+target.UUID.String()+"/follow", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("existing relationship should remain valid, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDeleteCurrentUserAnonymizesContentAndRevokesSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{}, &model.Channel{}, &model.ContentEntry{}, &model.Follow{}, &model.UserSettings{})

	user := model.User{Username: "delete-me", Email: "delete-me@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &user.UUID, Name: "我的频道", Slug: "delete-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	entry := model.ContentEntry{AuthorID: &user.UUID, ChannelID: channel.ID, Kind: "blog", Title: "保留内容", Status: "published", Visibility: "public"}
	if err := db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	credentials, err := authsession.New(db).Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", user.UUID); c.Next() })
	r.DELETE("/users/me", DeleteCurrentUser(db))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/users/me", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var updated model.User
	if err := db.First(&updated, "uuid = ?", user.UUID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.IsActive || updated.Username == user.Username || updated.DisplayName != "已注销用户" {
		t.Fatalf("user was not anonymized: %#v", updated)
	}
	var updatedEntry model.ContentEntry
	if err := db.First(&updatedEntry, "id = ?", entry.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedEntry.AuthorID != nil {
		t.Fatalf("content author should be cleared: %#v", updatedEntry)
	}
	var updatedChannel model.Channel
	if err := db.First(&updatedChannel, "id = ?", channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updatedChannel.UserID != nil || !updatedChannel.IsAnonymous {
		t.Fatalf("channel was not anonymized: %#v", updatedChannel)
	}
	if _, err := authsession.New(db).Authenticate(credentials.Token, authsession.KindWeb); err == nil {
		t.Fatal("deleted user's session should be revoked")
	}
}

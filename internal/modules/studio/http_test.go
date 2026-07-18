package studio

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type studioHTTPFixture struct {
	db      *gorm.DB
	router  *gin.Engine
	owner   model.User
	foreign model.User
}

func newStudioHTTPFixture(t *testing.T) studioHTTPFixture {
	t.Helper()
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.PodcastEpisode{},
		&model.Video{},
		&model.VideoCollection{},
		&model.UserStudioState{},
		&model.StudioModuleSettings{},
		&model.StudioMetricEvent{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
	)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })

	owner := model.User{Username: "studio-owner", Email: "studio-owner@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	foreign := model.User{Username: "studio-foreign", Email: "studio-foreign@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Create(&foreign).Error; err != nil {
		t.Fatalf("create foreign user: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/studio"), NewService(db))
	return studioHTTPFixture{db: db, router: router, owner: owner, foreign: foreign}
}

func studioToken(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": user.UUID.String(), "username": user.Username, "role": user.Role,
		"auth_version": user.AuthVersion, "exp": time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func studioRequest(t *testing.T, fixture studioHTTPFixture, user model.User, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+studioToken(t, user))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	fixture.router.ServeHTTP(recorder, request)
	return recorder
}

func createStudioChannel(t *testing.T, db *gorm.DB, user model.User, name string) model.Channel {
	t.Helper()
	channel := model.Channel{UserID: &user.UUID, Name: name, Slug: name + "-" + uuid.NewString()[:8]}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return channel
}

func TestStudioStateReturnsCurrentChannelAndOwnedChannels(t *testing.T) {
	fixture := newStudioHTTPFixture(t)
	current := createStudioChannel(t, fixture.db, fixture.owner, "Current")
	other := createStudioChannel(t, fixture.db, fixture.owner, "Other")
	createStudioChannel(t, fixture.db, fixture.foreign, "Foreign")
	if err := fixture.db.Create(&model.UserStudioState{UserID: fixture.owner.UUID, ChannelID: &current.ID}).Error; err != nil {
		t.Fatalf("create state: %v", err)
	}

	response := studioRequest(t, fixture, fixture.owner, http.MethodGet, "/api/v1/studio/state", "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data StateResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.CurrentChannel == nil || payload.Data.CurrentChannel.ID != current.ID {
		t.Fatalf("expected current channel %s, got %#v", current.ID, payload.Data.CurrentChannel)
	}
	if len(payload.Data.Channels) != 2 {
		t.Fatalf("expected two owned channels, got %#v", payload.Data.Channels)
	}
	if payload.Data.Channels[0].ID != current.ID && payload.Data.Channels[0].ID != other.ID {
		t.Fatalf("unexpected channel list: %#v", payload.Data.Channels)
	}
}

func TestStudioStatePatchRejectsForeignChannel(t *testing.T) {
	fixture := newStudioHTTPFixture(t)
	foreign := createStudioChannel(t, fixture.db, fixture.foreign, "Foreign")

	response := studioRequest(t, fixture, fixture.owner, http.MethodPatch, "/api/v1/studio/state", `{"channel_id":"`+foreign.ID.String()+`"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", response.Code, response.Body.String())
	}
}

func TestStudioChannelCreateBecomesCurrentWhenStateIsEmpty(t *testing.T) {
	fixture := newStudioHTTPFixture(t)

	response := studioRequest(t, fixture, fixture.owner, http.MethodPost, "/api/v1/studio/channels", `{"name":"First Studio","description":"desc"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data ChannelSummary `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var state model.UserStudioState
	if err := fixture.db.First(&state, "user_id = ?", fixture.owner.UUID).Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.ChannelID == nil || *state.ChannelID != payload.Data.ID {
		t.Fatalf("expected current channel %s, got %#v", payload.Data.ID, state.ChannelID)
	}
}

func TestStudioChannelDeleteRejectsNonEmptyChannel(t *testing.T) {
	fixture := newStudioHTTPFixture(t)
	channel := createStudioChannel(t, fixture.db, fixture.owner, "Non Empty")
	post := model.Post{UserID: fixture.owner.UUID, ChannelID: &channel.ID, Title: "Article", Content: "body", Status: "draft", Visibility: "public"}
	if err := fixture.db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	response := studioRequest(t, fixture, fixture.owner, http.MethodDelete, "/api/v1/studio/channels/"+channel.ID.String(), "")
	if response.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", response.Code, response.Body.String())
	}
	var count int64
	if err := fixture.db.Model(&model.Channel{}).Where("id = ?", channel.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected channel to remain, count=%d err=%v", count, err)
	}
}

func TestStudioCollectionsAreScopedByChannelAndModule(t *testing.T) {
	fixture := newStudioHTTPFixture(t)
	channel := createStudioChannel(t, fixture.db, fixture.owner, "Mixed")
	blogCollection := model.Collection{ChannelID: channel.ID, ContentType: string(ModuleBlog), Name: "Articles"}
	videoCollection := model.Collection{ChannelID: channel.ID, ContentType: string(ModuleVideo), Name: "Videos"}
	if err := fixture.db.Create(&blogCollection).Error; err != nil {
		t.Fatalf("create blog collection: %v", err)
	}
	if err := fixture.db.Create(&videoCollection).Error; err != nil {
		t.Fatalf("create video collection: %v", err)
	}

	response := studioRequest(t, fixture, fixture.owner, http.MethodGet, "/api/v1/studio/blog/collections?channel_id="+channel.ID.String(), "")
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data []model.Collection `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != blogCollection.ID {
		t.Fatalf("expected only blog collection, got %#v", payload.Data)
	}
}

func TestStudioCollectionMutationRejectsWrongModule(t *testing.T) {
	fixture := newStudioHTTPFixture(t)
	channel := createStudioChannel(t, fixture.db, fixture.owner, "Mixed")
	collection := model.Collection{ChannelID: channel.ID, ContentType: string(ModuleBlog), Name: "Articles"}
	if err := fixture.db.Create(&collection).Error; err != nil {
		t.Fatalf("create collection: %v", err)
	}

	response := studioRequest(t, fixture, fixture.owner, http.MethodPatch, "/api/v1/studio/video/collections/"+collection.ID.String(), `{"name":"Wrong"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestStudioDashboardAndContentsRoutesUseCurrentChannel(t *testing.T) {
	fixture := newStudioHTTPFixture(t)
	channel := createStudioChannel(t, fixture.db, fixture.owner, "Dashboard")
	collection := model.Collection{ChannelID: channel.ID, ContentType: string(ModuleBlog), Name: "Articles"}
	if err := fixture.db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	post := model.Post{
		UserID: fixture.owner.UUID, ChannelID: &channel.ID, CollectionID: &collection.ID,
		Title: "Studio Draft", Content: "body", Status: "draft", Visibility: "public",
	}
	if err := fixture.db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Create(&model.UserStudioState{UserID: fixture.owner.UUID, ChannelID: &channel.ID}).Error; err != nil {
		t.Fatal(err)
	}

	dashboard := studioRequest(t, fixture, fixture.owner, http.MethodGet, "/api/v1/studio/dashboard", "")
	if dashboard.Code != http.StatusOK {
		t.Fatalf("expected dashboard 200, got %d: %s", dashboard.Code, dashboard.Body.String())
	}
	contents := studioRequest(t, fixture, fixture.owner, http.MethodGet, "/api/v1/studio/blog/contents?status=draft&page=1&page_size=10", "")
	if contents.Code != http.StatusOK {
		t.Fatalf("expected contents 200, got %d: %s", contents.Code, contents.Body.String())
	}
	var payload struct {
		Data []StudioContentItem `json:"data"`
		Meta struct {
			Page     int   `json:"page"`
			PageSize int   `json:"page_size"`
			Total    int64 `json:"total"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(contents.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != post.ID || payload.Meta.Page != 1 || payload.Meta.PageSize != 10 || payload.Meta.Total != 1 {
		t.Fatalf("unexpected contents response: %#v", payload)
	}
}

func TestStudioAnalyticsInteractionsSettingsAndShareRoutes(t *testing.T) {
	fixture := newStudioHTTPFixture(t)
	channel := createStudioChannel(t, fixture.db, fixture.owner, "Creator Tools")
	if err := fixture.db.Create(&model.UserStudioState{UserID: fixture.owner.UUID, ChannelID: &channel.ID}).Error; err != nil {
		t.Fatal(err)
	}

	analytics := studioRequest(t, fixture, fixture.owner, http.MethodGet, "/api/v1/studio/blog/analytics?range=7", "")
	if analytics.Code != http.StatusOK {
		t.Fatalf("expected analytics 200, got %d: %s", analytics.Code, analytics.Body.String())
	}
	interactions := studioRequest(t, fixture, fixture.owner, http.MethodGet, "/api/v1/studio/blog/interactions?unreplied=true&page=1&page_size=10", "")
	if interactions.Code != http.StatusOK {
		t.Fatalf("expected interactions 200, got %d: %s", interactions.Code, interactions.Body.String())
	}
	settings := studioRequest(t, fixture, fixture.owner, http.MethodGet, "/api/v1/studio/blog/settings", "")
	if settings.Code != http.StatusOK {
		t.Fatalf("expected settings 200, got %d: %s", settings.Code, settings.Body.String())
	}
	updatedSettings := studioRequest(t, fixture, fixture.owner, http.MethodPatch, "/api/v1/studio/blog/settings", `{"default_visibility":"subscribers","default_publish_status":"draft","autoplay_enabled":true}`)
	if updatedSettings.Code != http.StatusOK {
		t.Fatalf("expected settings patch 200, got %d: %s", updatedSettings.Code, updatedSettings.Body.String())
	}
	var settingsPayload struct {
		Data SettingsResponse `json:"data"`
	}
	if err := json.Unmarshal(updatedSettings.Body.Bytes(), &settingsPayload); err != nil {
		t.Fatal(err)
	}
	if settingsPayload.Data.DefaultVisibility != "subscribers" || settingsPayload.Data.DefaultPublishStatus != "draft" || settingsPayload.Data.AutoplayEnabled {
		t.Fatalf("unexpected blog settings: %#v", settingsPayload.Data)
	}

	post := model.Post{
		UserID: fixture.owner.UUID, ChannelID: &channel.ID,
		Title: "Public post", Content: "body", Status: "published", Visibility: "public",
	}
	if err := fixture.db.Create(&post).Error; err != nil {
		t.Fatal(err)
	}
	share := studioRequest(t, fixture, fixture.owner, http.MethodPost, "/api/v1/studio/blog/contents/"+post.ID.String()+"/share", `{}`)
	if share.Code != http.StatusOK {
		t.Fatalf("expected share 200, got %d: %s", share.Code, share.Body.String())
	}
	var event model.StudioMetricEvent
	if err := fixture.db.First(&event, "content_type = ? AND content_id = ? AND metric = ?", ModuleBlog, post.ID, "share").Error; err != nil {
		t.Fatalf("expected share metric event: %v", err)
	}
}

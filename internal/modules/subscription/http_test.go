package subscription

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func signedSubscriptionTokenForTest(t *testing.T, db *gorm.DB, user model.User) string {
	t.Helper()
	if err := db.AutoMigrate(&model.AuthSession{}); err != nil {
		t.Fatalf("migrate auth sessions: %v", err)
	}
	credentials, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatalf("create api auth session: %v", err)
	}
	return credentials.Token
}

func TestCreateSubscriptionRouteAcceptsBearerAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	testdb.Migrate(t, db,
		&model.User{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
	)
	user := model.User{Username: "alice_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewBufferString(`{"target_type":"external_rss","rss_url":"https://example.com/feed.xml","title":"Example Feed"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+signedSubscriptionTokenForTest(t, db, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected bearer-authenticated subscription create to return 201, got %d with body %s", rr.Code, rr.Body.String())
	}

	var payload struct {
		Data model.Subscription `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.UserID != user.UUID {
		t.Fatalf("expected subscription for user %s, got %s", user.UUID, payload.Data.UserID)
	}
	if payload.Data.SubscriptionGroupID == nil {
		t.Fatalf("expected subscription to be assigned to default group")
	}
}

func TestCreateSubscriptionRouteCanUseExistingAuthContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.FeedSource{},
		&model.SubscriptionGroup{},
		&model.Subscription{},
	)
	user := model.User{Username: "alice_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		authctx.SetCurrentUser(c, authctx.CurrentUser{ID: user.UUID, Username: user.Username, Role: authctx.RoleUser})
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1"), NewService(db))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/subscriptions", bytes.NewBufferString(`{"target_type":"external_rss","rss_url":"https://example.com/feed.xml"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+signedSubscriptionTokenForTest(t, db, user))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected existing auth context to create subscription, got %d with body %s", rr.Code, rr.Body.String())
	}
}

package collab

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func userHubGinContextForTest(req *http.Request) *gin.Context {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	return c
}

func newUserHubAuthFixture(t *testing.T) (*gorm.DB, model.User) {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	user := model.User{Username: "socket-user", Email: "socket@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return db, user
}

func TestExtractUserIDFromRequestAcceptsTrustedWebCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("FRONTEND_URL", "https://www.atoman.org")
	db, user := newUserHubAuthFixture(t)
	credentials, err := authsession.New(db).Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ws/user", nil)
	req.Header.Set("Origin", "https://www.atoman.org")
	req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: credentials.Token})
	got, err := extractUserIDFromRequest(userHubGinContextForTest(req), db)
	if err != nil || got != user.UUID {
		t.Fatalf("expected cookie session user %s, got %s err=%v", user.UUID, got, err)
	}
}

func TestExtractUserIDFromRequestAcceptsAPIBearerAndRejectsQueryToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, user := newUserHubAuthFixture(t)
	credentials, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatal(err)
	}
	headerReq := httptest.NewRequest(http.MethodGet, "/ws/user", nil)
	headerReq.Header.Set("Authorization", "Bearer "+credentials.Token)
	headerGot, err := extractUserIDFromRequest(userHubGinContextForTest(headerReq), db)
	if err != nil || headerGot != user.UUID {
		t.Fatalf("expected api bearer user %s, got %s err=%v", user.UUID, headerGot, err)
	}
	queryReq := httptest.NewRequest(http.MethodGet, "/ws/user?token="+credentials.Token, nil)
	if _, err := extractUserIDFromRequest(userHubGinContextForTest(queryReq), db); err == nil {
		t.Fatal("query tokens must be rejected")
	}
}

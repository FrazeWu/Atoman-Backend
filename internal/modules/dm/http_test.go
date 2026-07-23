package dm

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
)

func TestHTTPRegistersDMV2RoutesAndRemovesLegacyPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{}, &model.Channel{}, &model.UserSettings{}, &model.DMConversation{}, &model.DMMessage{}, &model.DMImage{}, &model.DMMessageReport{}, &model.DMChannelSettings{})
	user := model.User{Username: "dm-http", Email: "dm-http@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	credentials, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1"), NewService(NewRepo(db), NewImageStoreFromEnv(nil), nil, nil))

	request := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+credentials.Token)
		router.ServeHTTP(w, req)
		return w
	}
	if got := request(http.MethodGet, "/api/v1/dm/mailboxes").Code; got != http.StatusOK {
		t.Fatalf("mailboxes status=%d", got)
	}
	if got := request(http.MethodGet, "/api/v1/dm/conversations").Code; got != http.StatusNotFound {
		t.Fatalf("legacy conversations status=%d", got)
	}
	if got := request(http.MethodPost, "/api/v1/dm/upload").Code; got != http.StatusNotFound {
		t.Fatalf("legacy upload status=%d", got)
	}
	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/dm/mailboxes", nil))
	if unauthorized.Code != http.StatusUnauthorized || !bytes.Contains(unauthorized.Body.Bytes(), []byte(`"code":"auth.unauthorized"`)) {
		t.Fatalf("expected stable unauthorized error, got %d: %s", unauthorized.Code, unauthorized.Body.String())
	}
	web, err := authsession.New(db).Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatal(err)
	}
	mutation := func(origin, csrf string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/dm/settings", bytes.NewBufferString(`{"permission":"anyone"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", csrf)
		req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: web.Token})
		router.ServeHTTP(w, req)
		return w
	}
	if got := mutation("http://localhost:5173", ""); got.Code != http.StatusForbidden {
		t.Fatalf("csrf rejection status=%d", got.Code)
	}
	if got := mutation("http://localhost:5173", web.CSRFToken); got.Code != http.StatusOK {
		t.Fatalf("csrf success status=%d: %s", got.Code, got.Body.String())
	}
	adminReport := request(http.MethodGet, "/api/v1/admin/dm/reports")
	if adminReport.Code != http.StatusForbidden || !bytes.Contains(adminReport.Body.Bytes(), []byte(`"code":"dm.permission_denied"`)) {
		t.Fatalf("expected admin permission error, got %d: %s", adminReport.Code, adminReport.Body.String())
	}
}

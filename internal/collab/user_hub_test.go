package collab

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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

func TestExtractUserIDFromRequestAcceptsBrowserAPISubprotocol(t *testing.T) {
	db, user := newUserHubAuthFixture(t)
	for _, kind := range []string{authsession.KindAPI, authsession.KindWeb} {
		credentials, err := authsession.New(db).Create(user.UUID, kind)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, "/ws/user", nil)
		req.Header.Set("Origin", "https://third-party.example")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Sec-WebSocket-Protocol", "atoman, atoman.api."+credentials.Token)
		got, err := extractUserIDFromRequest(userHubGinContextForTest(req), db)
		if kind == authsession.KindAPI && (err != nil || got != user.UUID) {
			t.Fatalf("browser API token was rejected: %s %v", got, err)
		}
		if kind == authsession.KindWeb && err == nil {
			t.Fatal("web cookie credentials must not work as third-party API tokens")
		}
	}
}

func TestBrowserWebSocketHandshakeAuthentication(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "https://www.atoman.org")
	db, user := newUserHubAuthFixture(t)
	api, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
	if err != nil {
		t.Fatal(err)
	}
	web, err := authsession.New(db).Create(user.UUID, authsession.KindWeb)
	if err != nil {
		t.Fatal(err)
	}
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	router := gin.New()
	upgrade := func(c *gin.Context) {
		conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
		if err == nil {
			conn.Close()
		}
	}
	router.GET("/user", func(c *gin.Context) {
		if _, err := extractUserIDFromRequest(c, db); err != nil {
			c.Status(http.StatusUnauthorized)
			return
		}
		upgrade(c)
	})
	router.GET("/authenticated", middleware.AuthMiddleware(), upgrade)
	server := httptest.NewServer(router)
	defer server.Close()
	for _, path := range []string{"/user", "/authenticated"} {
		for _, tc := range []struct {
			name, token, origin, cookie string
			status                      int
		}{
			{name: "API", token: api.Token, origin: "https://third-party.example", status: http.StatusSwitchingProtocols},
			{name: "invalid API", token: "invalid", origin: "https://third-party.example", cookie: web.Token, status: http.StatusUnauthorized},
			{name: "web token as API", token: web.Token, origin: "https://third-party.example", status: http.StatusUnauthorized},
			{name: "third-party cookie", origin: "https://third-party.example", cookie: web.Token, status: http.StatusForbidden},
			{name: "official cookie", origin: "https://www.atoman.org", cookie: web.Token, status: http.StatusSwitchingProtocols},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				headers := http.Header{"Origin": {tc.origin}}
				if tc.cookie != "" {
					headers.Set("Cookie", middleware.AuthSessionCookieName+"="+tc.cookie)
				}
				dialer := websocket.Dialer{Subprotocols: []string{"atoman"}}
				if tc.token != "" {
					dialer.Subprotocols = append(dialer.Subprotocols, "atoman.api."+tc.token)
				}
				conn, resp, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+path, headers)
				wantStatus := tc.status
				if path == "/user" && tc.name == "third-party cookie" {
					wantStatus = http.StatusUnauthorized
				}
				if resp == nil || resp.StatusCode != wantStatus {
					t.Fatalf("unexpected handshake: %v %v", resp, err)
				}
				if conn != nil {
					defer conn.Close()
					if conn.Subprotocol() != "atoman" {
						t.Fatalf("credential subprotocol must not be echoed: %q", conn.Subprotocol())
					}
				} else if wantStatus == http.StatusSwitchingProtocols {
					t.Fatal(err)
				}
			})
		}
	}
}

package mail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMailAccountHTTPUsesAtomanSessionsAndCSRF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	require.NoError(t, migrations.RunMailAccountsMigration(db))

	admin := model.User{Username: "webmail-admin", Email: "webmail-admin@example.test", Password: "hash", Role: "admin", IsActive: true}
	alice := model.User{Username: "webmail-alice", Email: "webmail-alice@example.test", Password: "hash", Role: "user", IsActive: true}
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, db.Create(&alice).Error)
	apiAdmin, err := authsession.New(db).Create(admin.UUID, authsession.KindAPI)
	require.NoError(t, err)
	apiAlice, err := authsession.New(db).Create(alice.UUID, authsession.KindAPI)
	require.NoError(t, err)
	webAlice, err := authsession.New(db).Create(alice.UUID, authsession.KindWeb)
	require.NoError(t, err)

	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	r := gin.New()
	RegisterAccountRoutesWithService(r.Group("/api/v1/mail"), NewAccountService(db, true))
	req := func(method, path, body, bearer string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		httpReq := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if bearer != "" {
			httpReq.Header.Set("Authorization", "Bearer "+bearer)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(response, httpReq)
		return response
	}

	unauthed := req(http.MethodGet, "/api/v1/mail/me", "", "")
	require.Equal(t, http.StatusUnauthorized, unauthed.Code)
	inviteBody := fmt.Sprintf(`{"target_user_id":%q,"local_part":"mail-alice"}`, alice.UUID.String())
	restricted := req(http.MethodPost, "/api/v1/mail/admin/invites", inviteBody, apiAlice.Token)
	require.Equal(t, http.StatusForbidden, restricted.Code)
	created := req(http.MethodPost, "/api/v1/mail/admin/invites", inviteBody, apiAdmin.Token)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.Contains(t, created.Header().Get("Cache-Control"), "no-store")
	var issued struct {
		Data IssuedInvite `json:"data"`
	}
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &issued))
	require.Equal(t, "mail-alice@atoman.org", issued.Data.Address)
	require.Len(t, issued.Data.Token, 43)

	activationPayload, err := json.Marshal(ReserveInput{Token: issued.Data.Token})
	require.NoError(t, err)
	// Web cookie requests must include both trusted Origin and CSRF header.
	cookieRequest := func(csrf string) *httptest.ResponseRecorder {
		httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/mail/activate", bytes.NewReader(activationPayload))
		httpReq.Header.Set("Origin", "https://atoman.org")
		httpReq.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			httpReq.Header.Set(middleware.CSRFHeaderName, csrf)
		}
		httpReq.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: webAlice.Token})
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httpReq)
		return response
	}
	withoutCSRF := cookieRequest("")
	require.Equal(t, http.StatusForbidden, withoutCSRF.Code)
	withCSRF := cookieRequest(webAlice.CSRFToken)
	require.Equal(t, http.StatusCreated, withCSRF.Code, withCSRF.Body.String())

	var reserved struct {
		Data AccountInfo `json:"data"`
	}
	require.NoError(t, json.Unmarshal(withCSRF.Body.Bytes(), &reserved))
	require.Equal(t, "reserved", reserved.Data.Status)
	require.False(t, reserved.Data.Available)

	for _, path := range []string{"/api/v1/mail/message?mailbox_id=stage-folder&message_id=stage-id", "/api/v1/mail/folders"} {
		before := req(http.MethodGet, path, "", apiAlice.Token)
		require.Equal(t, http.StatusServiceUnavailable, before.Code)
		require.Contains(t, before.Body.String(), "mail.read_not_ready")
	}
	readPost := req(http.MethodPost, "/api/v1/mail/messages/read", `{"mailbox_id":"inbox","message_id":"message"}`, apiAlice.Token)
	require.Equal(t, http.StatusServiceUnavailable, readPost.Code)

	readBeforeActivation := req(http.MethodGet, "/api/v1/mail/folders", "", apiAlice.Token)
	require.Equal(t, http.StatusServiceUnavailable, readBeforeActivation.Code)
	require.Contains(t, readBeforeActivation.Body.String(), "mail.read_not_ready")

	me := req(http.MethodGet, "/api/v1/mail/me", "", apiAlice.Token)
	require.Equal(t, http.StatusOK, me.Code)
	require.Contains(t, me.Header().Get("Cache-Control"), "no-store")
	require.Contains(t, me.Body.String(), "mail-alice@atoman.org")
	again := req(http.MethodPost, "/api/v1/mail/activate", string(activationPayload), apiAlice.Token)
	require.Equal(t, http.StatusConflict, again.Code)
}

func TestMailAccountHTTPDefaultClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	require.NoError(t, migrations.RunMailAccountsMigration(db))
	user := model.User{Username: "closed-mail-admin", Email: "closed-mail-admin@example.test", Password: "hash", Role: "admin", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	apiToken, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
	require.NoError(t, err)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	r := gin.New()
	RegisterAccountRoutesWithService(r.Group("/api/v1/mail"), NewAccountService(db, false))
	body := fmt.Sprintf(`{"target_user_id":%q,"local_part":"closed-admin"}`, user.UUID.String())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/admin/invites", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+apiToken.Token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	r.ServeHTTP(res, req)
	require.Equal(t, http.StatusServiceUnavailable, res.Code)
	require.Contains(t, res.Body.String(), "mail.not_ready")
	var count int64
	require.NoError(t, db.Model(&model.MailInvite{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestMailAdminSelfReservationHTTPSecureWhileInvitesClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	require.NoError(t, migrations.RunMailAccountsMigration(db))
	admin := model.User{Username: "admin-only", Email: "admin-only@example.test", Password: "hash", Role: "admin", IsActive: true}
	alice := model.User{Username: "mail-other", Email: "mail-other@example.test", Password: "hash", Role: "user", IsActive: true}
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, db.Create(&alice).Error)
	adminWeb, err := authsession.New(db).Create(admin.UUID, authsession.KindWeb)
	require.NoError(t, err)
	aliceWeb, err := authsession.New(db).Create(alice.UUID, authsession.KindWeb)
	require.NoError(t, err)
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	r := gin.New()
	RegisterAccountRoutesWithService(r.Group("/api/v1/mail"), NewAccountService(db, false))
	request := func(token, csrf string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/admin/reserve-self", strings.NewReader(`{"local_part":"my.personal.name"}`))
		req.Header.Set("Origin", "https://atoman.org")
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set(middleware.CSRFHeaderName, csrf)
		}
		if token != "" {
			req.AddCookie(&http.Cookie{Name: middleware.AuthSessionCookieName, Value: token})
		}
		r.ServeHTTP(w, req)
		return w
	}
	require.Equal(t, http.StatusUnauthorized, request("", "").Code)
	require.Equal(t, http.StatusForbidden, request(aliceWeb.Token, aliceWeb.CSRFToken).Code)
	require.Equal(t, http.StatusForbidden, request(adminWeb.Token, "").Code)
	created := request(adminWeb.Token, adminWeb.CSRFToken)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.Contains(t, created.Body.String(), "my.personal.name@atoman.org")
	require.Contains(t, created.Body.String(), `"available":false`)
	require.Equal(t, http.StatusConflict, request(adminWeb.Token, adminWeb.CSRFToken).Code)
}

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/platform/oauthprovider"
	"atoman/internal/service"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type handlerOAuthProvider struct {
	name          string
	authorizeReq  oauthprovider.AuthorizationRequest
	profile       oauthprovider.Profile
	exchangeCalls int
}

func (p *handlerOAuthProvider) Name() string {
	return p.name
}

func (p *handlerOAuthProvider) AuthorizationURL(req oauthprovider.AuthorizationRequest) (string, error) {
	p.authorizeReq = req
	return "https://provider.example/authorize?state=" + url.QueryEscape(req.State), nil
}

func (p *handlerOAuthProvider) Exchange(_ context.Context, _ oauthprovider.CallbackRequest) (oauthprovider.Profile, error) {
	p.exchangeCalls++
	return p.profile, nil
}

func newOAuthHandlerTestService(t *testing.T, provider oauthprovider.Provider) *service.OAuthService {
	t.Helper()
	return service.NewOAuthService(newOAuthHandlerTestDB(t), oauthprovider.NewRegistry(provider))
}

func newOAuthHandlerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{}, &model.UserSettings{}, &model.ExternalIdentity{}, &model.OAuthFlow{},
		&model.Channel{}, &model.Collection{}, &model.UserStudioState{}, &model.StudioModuleSettings{},
		&model.FeedSource{}, &model.SubscriptionGroup{}, &model.Subscription{},
		&model.BookmarkFolder{}, &model.Playlist{}, &model.PlaylistSong{},
	)
	return db
}

func TestOAuthRoutesListProvidersAndStartAuthorization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &handlerOAuthProvider{name: model.OAuthProviderGoogle}
	svc := newOAuthHandlerTestService(t, provider)
	router := gin.New()
	RegisterOAuthRoutes(router.Group("/api/v1/auth"), svc, "https://app.example.com")

	providersResponse := httptest.NewRecorder()
	router.ServeHTTP(providersResponse, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/providers", nil))
	if providersResponse.Code != http.StatusOK {
		t.Fatalf("expected provider list 200, got %d: %s", providersResponse.Code, providersResponse.Body.String())
	}
	var providersPayload struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(providersResponse.Body.Bytes(), &providersPayload); err != nil {
		t.Fatalf("decode providers: %v", err)
	}
	if len(providersPayload.Providers) != 1 || providersPayload.Providers[0] != model.OAuthProviderGoogle {
		t.Fatalf("unexpected providers: %#v", providersPayload.Providers)
	}

	startResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/start?return_to=%2Fforum", nil)
	router.ServeHTTP(startResponse, request)
	if startResponse.Code != http.StatusFound {
		t.Fatalf("expected start redirect, got %d: %s", startResponse.Code, startResponse.Body.String())
	}
	location, err := url.Parse(startResponse.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if location.Host != "provider.example" || provider.authorizeReq.State == "" {
		t.Fatalf("unexpected authorization redirect: %s", location.String())
	}
}

func TestOAuthCallbackCreatesPendingCookieAndReturnsMaskedFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &handlerOAuthProvider{
		name: model.OAuthProviderGoogle,
		profile: oauthprovider.Profile{
			Issuer: "https://accounts.google.com", Subject: "new-subject",
			Email: "person@example.com", EmailVerified: true,
		},
	}
	svc := newOAuthHandlerTestService(t, provider)
	router := gin.New()
	RegisterOAuthRoutes(router.Group("/api/v1/auth"), svc, "https://app.example.com")

	startResponse := httptest.NewRecorder()
	router.ServeHTTP(startResponse, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/start", nil))
	if startResponse.Code != http.StatusFound {
		t.Fatalf("start oauth: %d %s", startResponse.Code, startResponse.Body.String())
	}

	callbackURL := "/api/v1/auth/oauth/google/callback?state=" + url.QueryEscape(provider.authorizeReq.State) + "&code=code"
	callbackResponse := httptest.NewRecorder()
	callbackRequest := httptest.NewRequest(http.MethodGet, callbackURL, nil)
	callbackRequest.AddCookie(requireOAuthStateCookie(t, startResponse))
	router.ServeHTTP(callbackResponse, callbackRequest)
	if callbackResponse.Code != http.StatusFound {
		t.Fatalf("callback oauth: %d %s", callbackResponse.Code, callbackResponse.Body.String())
	}
	if callbackResponse.Header().Get("Location") != "https://app.example.com/auth/oauth/complete-profile" {
		t.Fatalf("unexpected callback redirect: %q", callbackResponse.Header().Get("Location"))
	}
	var pendingCookie *http.Cookie
	for _, cookie := range callbackResponse.Result().Cookies() {
		if cookie.Name == oauthFlowCookieName {
			pendingCookie = cookie
			break
		}
	}
	if pendingCookie == nil || pendingCookie.Value == "" || !pendingCookie.HttpOnly {
		t.Fatalf("missing secure pending cookie: %#v", pendingCookie)
	}

	pendingRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/pending", nil)
	pendingRequest.AddCookie(pendingCookie)
	pendingResponse := httptest.NewRecorder()
	router.ServeHTTP(pendingResponse, pendingRequest)
	if pendingResponse.Code != http.StatusOK {
		t.Fatalf("pending flow: %d %s", pendingResponse.Code, pendingResponse.Body.String())
	}
	if !strings.Contains(pendingResponse.Body.String(), `"stage":"complete_profile"`) || !strings.Contains(pendingResponse.Body.String(), `"email":"p***@example.com"`) {
		t.Fatalf("unexpected pending payload: %s", pendingResponse.Body.String())
	}
}

func TestOAuthCallbackRejectsStateFromAnotherBrowser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &handlerOAuthProvider{
		name: model.OAuthProviderGoogle,
		profile: oauthprovider.Profile{
			Issuer: "https://accounts.google.com", Subject: "attacker-subject",
			Email: "attacker@example.com", EmailVerified: true,
		},
	}
	svc := newOAuthHandlerTestService(t, provider)
	router := gin.New()
	RegisterOAuthRoutes(router.Group("/api/v1/auth"), svc, "https://app.example.com")

	startResponse := httptest.NewRecorder()
	router.ServeHTTP(startResponse, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/start", nil))
	if startResponse.Code != http.StatusFound {
		t.Fatalf("start oauth: %d %s", startResponse.Code, startResponse.Body.String())
	}

	callbackURL := "/api/v1/auth/oauth/google/callback?state=" + url.QueryEscape(provider.authorizeReq.State) + "&code=code"
	callbackResponse := httptest.NewRecorder()
	router.ServeHTTP(callbackResponse, httptest.NewRequest(http.MethodGet, callbackURL, nil))

	if callbackResponse.Header().Get("Location") != "https://app.example.com/auth/oauth/callback?result=failed" {
		t.Fatalf("expected callback without browser state to fail, got %q", callbackResponse.Header().Get("Location"))
	}
	if provider.exchangeCalls != 0 {
		t.Fatalf("expected provider exchange to be skipped, got %d calls", provider.exchangeCalls)
	}
}

func TestOAuthCompleteProfileReturnsAtomanSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	provider := &handlerOAuthProvider{
		name: model.OAuthProviderGoogle,
		profile: oauthprovider.Profile{
			Issuer: "https://accounts.google.com", Subject: "new-subject",
			Email: "person@example.com", EmailVerified: true, DisplayName: "Person",
		},
	}
	svc := newOAuthHandlerTestService(t, provider)
	router := gin.New()
	RegisterOAuthRoutes(router.Group("/api/v1/auth"), svc, "https://app.example.com")

	startResponse := httptest.NewRecorder()
	router.ServeHTTP(startResponse, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/google/start?return_to=%2Fforum", nil))
	callbackResponse := httptest.NewRecorder()
	callbackURL := "/api/v1/auth/oauth/google/callback?state=" + url.QueryEscape(provider.authorizeReq.State) + "&code=code"
	callbackRequest := httptest.NewRequest(http.MethodGet, callbackURL, nil)
	callbackRequest.AddCookie(requireOAuthStateCookie(t, startResponse))
	router.ServeHTTP(callbackResponse, callbackRequest)
	var pendingCookie *http.Cookie
	for _, cookie := range callbackResponse.Result().Cookies() {
		if cookie.Name == oauthFlowCookieName && cookie.Value != "" {
			pendingCookie = cookie
		}
	}
	if pendingCookie == nil {
		t.Fatal("missing pending cookie")
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth/pending/complete-profile", bytes.NewBufferString(`{"username":"person"}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(pendingCookie)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("complete oauth profile: %d %s", response.Code, response.Body.String())
	}
	var payload struct {
		Token    string `json:"token"`
		ReturnTo string `json:"return_to"`
		User     struct {
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode auth session: %v", err)
	}
	if payload.Token == "" || payload.ReturnTo != "/forum" || payload.User.Username != "person" || payload.User.Email != "person@example.com" {
		t.Fatalf("unexpected auth session: %#v", payload)
	}
	var authCookie, clearedFlow bool
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == authTokenCookieName && cookie.Value != "" {
			authCookie = true
		}
		if cookie.Name == oauthFlowCookieName && cookie.MaxAge < 0 {
			clearedFlow = true
		}
	}
	if !authCookie || !clearedFlow {
		t.Fatalf("expected auth cookie and cleared flow cookie: %#v", response.Result().Cookies())
	}
}

func TestOAuthIdentityRoutesListAndUnlinkCurrentUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newOAuthHandlerTestDB(t)
	middleware.SetAuthDB(db)
	user := model.User{Username: "settings-user", Email: "settings@example.com", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	identity := model.ExternalIdentity{
		UserID: user.UUID, Provider: model.OAuthProviderGitHub,
		Issuer: "https://github.com", Subject: "github-subject", Email: user.Email, EmailVerified: true,
	}
	if err := db.Create(&identity).Error; err != nil {
		t.Fatalf("create identity: %v", err)
	}
	token, err := generateAuthToken(user)
	if err != nil {
		t.Fatalf("generate auth token: %v", err)
	}

	svc := service.NewOAuthService(db, oauthprovider.NewRegistry())
	router := gin.New()
	RegisterOAuthRoutes(router.Group("/api/v1/auth"), svc, "https://app.example.com")

	listRequest := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/identities", nil)
	listRequest.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: token})
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"provider":"github"`) {
		t.Fatalf("list identities: %d %s", listResponse.Code, listResponse.Body.String())
	}

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/v1/auth/oauth/github", nil)
	deleteRequest.AddCookie(&http.Cookie{Name: authTokenCookieName, Value: token})
	deleteResponse := httptest.NewRecorder()
	router.ServeHTTP(deleteResponse, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("unlink identity: %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	var count int64
	if err := db.Model(&model.ExternalIdentity{}).Where("user_id = ?", user.UUID).Count(&count).Error; err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected identity to be removed, got %d", count)
	}
}

func TestSetupAuthRoutesEnablesConfiguredOAuthProviders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("BASE_URL", "https://api.example.com")
	t.Setenv("FRONTEND_URL", "https://app.example.com")
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "google-client")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "google-secret")
	db := newAuthTestDB(t)
	router := gin.New()
	SetupAuthRoutes(router, db, service.NewEmailServiceWithoutRedis(db))

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/oauth/providers", nil))
	if response.Code != http.StatusOK || response.Body.String() != `{"providers":["google"]}` {
		t.Fatalf("unexpected configured providers: %d %s", response.Code, response.Body.String())
	}
}

func requireOAuthStateCookie(t *testing.T, response *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == oauthStateCookieName && cookie.Value != "" {
			return cookie
		}
	}
	t.Fatal("missing oauth state cookie")
	return nil
}

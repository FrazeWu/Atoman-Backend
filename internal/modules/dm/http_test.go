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
	"github.com/google/uuid"
)

func TestHTTPRegistersDMV2RoutesAndRemovesLegacyPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{}, &model.Channel{}, &model.UserSettings{}, &model.UserBlock{}, &model.DMConversation{}, &model.DMMessage{}, &model.DMImage{}, &model.DMMessageReport{}, &model.DMChannelSettings{})
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
	t.Setenv("STORAGE_TYPE", "local")
	t.Setenv("DM_LOCAL_DIR", t.TempDir())
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
	owner := model.User{Username: "dm-owner", Email: "dm-owner@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{UserID: &owner.UUID, Name: "owner channel", Slug: "owner-channel"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodGet, "/api/v1/dm/channels/"+channel.ID.String()+"/settings"); got.Code != http.StatusForbidden {
		t.Fatalf("non-owner channel settings status=%d", got.Code)
	}
	image := model.DMImage{UploadedByUserID: owner.UUID, ObjectKey: "private.png", ContentType: "image/png", SizeBytes: 1}
	if err := db.Create(&image).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(http.MethodGet, "/api/v1/dm/images/"+image.ID.String()+"/content"); got.Code != http.StatusForbidden || !bytes.Contains(got.Body.Bytes(), []byte(`"code":"dm.conversation_forbidden"`)) {
		t.Fatalf("private image status=%d: %s", got.Code, got.Body.String())
	}
	recipient := model.User{Username: "dm-recipient", Email: "dm-recipient@example.com", Password: "hash", IsActive: true}
	if err := db.Create(&recipient).Error; err != nil {
		t.Fatal(err)
	}
	conversation := model.DMConversation{ParticipantAType: model.DMPartyUser, ParticipantA: user.UUID, ParticipantBType: model.DMPartyUser, ParticipantB: recipient.UUID}
	if err := db.Create(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	message := model.DMMessage{ConversationID: conversation.ID, SenderType: model.DMPartyUser, SenderID: recipient.UUID, ActorUserID: recipient.UUID, ClientMessageID: uuid.New(), Content: "reportable"}
	if err := db.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	jsonRequest := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+credentials.Token)
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}
	if got := jsonRequest(http.MethodPut, "/api/v1/dm/conversations/"+conversation.ID.String()+"/block", ""); got.Code != http.StatusOK {
		t.Fatalf("block status=%d: %s", got.Code, got.Body.String())
	}
	if got := jsonRequest(http.MethodPost, "/api/v1/dm/messages/"+message.ID.String()+"/reports", `{"reason":"spam"}`); got.Code != http.StatusCreated {
		t.Fatalf("report status=%d: %s", got.Code, got.Body.String())
	}
}

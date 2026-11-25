package papers

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
	"github.com/stretchr/testify/require"
)

func TestPapersHTTPRequiresInviteAndOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{}, &model.AuditLog{}, &model.PaperAccessGrant{}, &model.PaperProject{}, &model.PaperProjectMember{}, &model.PaperFile{}, &model.PaperFileVersion{})
	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })
	admin := model.User{Username: "paper-admin", Email: uuid.NewString() + "@example.org", Password: "hash", Role: authctx.RoleAdmin, IsActive: true}
	owner := model.User{Username: "paper-owner", Email: uuid.NewString() + "@example.org", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	other := model.User{Username: "paper-other", Email: uuid.NewString() + "@example.org", Password: "hash", Role: authctx.RoleUser, IsActive: true}
	for _, u := range []*model.User{&admin, &owner, &other} {
		require.NoError(t, db.Create(u).Error)
	}
	token := func(user model.User) string {
		credentials, err := authsession.New(db).Create(user.UUID, authsession.KindAPI)
		require.NoError(t, err)
		return credentials.Token
	}
	adminToken := token(admin)
	ownerToken := token(owner)
	otherToken := token(other)
	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/papers"), NewService(db))
	request := func(method, path, body, accessToken string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if accessToken != "" {
			req.Header.Set("Authorization", "Bearer "+accessToken)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		router.ServeHTTP(w, req)
		return w
	}
	base := "/api/v1/papers"
	require.Equal(t, http.StatusUnauthorized, request("GET", base+"/projects", "", "").Code)
	require.Equal(t, http.StatusForbidden, request("POST", base+"/projects", `{"name":"Denied"}`, ownerToken).Code)
	require.Equal(t, http.StatusForbidden, request("POST", base+"/admin/grants", `{"user_id":"`+owner.UUID.String()+`"}`, otherToken).Code)
	for _, u := range []model.User{owner, other} {
		body, _ := json.Marshal(map[string]string{"user_id": u.UUID.String()})
		require.Equal(t, http.StatusOK, request("POST", base+"/admin/grants", string(body), adminToken).Code)
	}
	created := request("POST", base+"/projects", `{"name":"Secure Work"}`, ownerToken)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var envelope struct{ Data model.PaperProject }
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &envelope))
	require.NotEqual(t, uuid.Nil, envelope.Data.ID)
	path := base + "/projects/" + envelope.Data.ID.String()
	require.Equal(t, http.StatusNotFound, request("GET", path, "", otherToken).Code)
	require.Equal(t, http.StatusNotFound, request("GET", path+"/files/"+envelope.Data.MainFileID.String(), "", otherToken).Code)
	require.Equal(t, http.StatusOK, request("GET", path, "", ownerToken).Code)
	require.Equal(t, http.StatusOK, request("GET", path+"/files/"+envelope.Data.MainFileID.String(), "", ownerToken).Code)
	require.Equal(t, http.StatusNotFound, request("PUT", path+"/files/"+envelope.Data.MainFileID.String(), `{"content":"bad","base_revision":1}`, otherToken).Code)
	require.Equal(t, http.StatusOK, request("PUT", path+"/files/"+envelope.Data.MainFileID.String(), `{"content":"new","base_revision":1}`, ownerToken).Code)
	require.Equal(t, http.StatusConflict, request("PUT", path+"/files/"+envelope.Data.MainFileID.String(), `{"content":"stale","base_revision":1}`, ownerToken).Code)
	require.Equal(t, http.StatusNoContent, request("DELETE", base+"/admin/grants/"+owner.UUID.String(), "", adminToken).Code)
	require.Equal(t, http.StatusForbidden, request("GET", path, "", ownerToken).Code)
}

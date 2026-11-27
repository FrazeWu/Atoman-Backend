package mail

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/migrations"
	"atoman/internal/model"
	"atoman/internal/platform/authsession"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAdminMailApprovalLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.AuthSession{})
	require.NoError(t, migrations.RunMailAccountsMigration(db))

	admin := model.User{Username: "mail-approver", DisplayName: "审批管理员", Email: "approver@example.test", Password: "hash", Role: "admin", IsActive: true}
	alice := model.User{Username: "mail-applicant", DisplayName: "申请用户", Email: "applicant@example.test", Password: "hash", Role: "user", IsActive: true}
	require.NoError(t, db.Create(&admin).Error)
	require.NoError(t, db.Create(&alice).Error)

	reserved := model.MailAccount{
		ID:        uuid.New(),
		UserID:    alice.UUID,
		LocalPart: "applicant.box",
		Address:   "applicant.box@atoman.org",
		Status:    "reserved",
	}
	require.NoError(t, db.Create(&reserved).Error)

	apiAdmin, err := authsession.New(db).Create(admin.UUID, authsession.KindAPI)
	require.NoError(t, err)
	apiAlice, err := authsession.New(db).Create(alice.UUID, authsession.KindAPI)
	require.NoError(t, err)

	middleware.SetAuthDB(db)
	t.Cleanup(func() { middleware.SetAuthDB(nil) })

	// Production / closed mode: listing works, dev approval stays closed (503).
	closedRouter := gin.New()
	registerAccountRoutesWithDevMode(closedRouter.Group("/api/v1/mail"), NewAccountService(db, true), nil, nil, nil, false)
	doReq := func(r *gin.Engine, method, path, body, bearer string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(res, req)
		return res
	}

	require.Equal(t, http.StatusForbidden, doReq(closedRouter, http.MethodGet, "/api/v1/mail/admin/accounts", "", apiAlice.Token).Code)
	require.Equal(t, http.StatusServiceUnavailable, doReq(closedRouter, http.MethodPost, "/api/v1/mail/admin/accounts/"+reserved.ID.String()+"/approve", "{}", apiAdmin.Token).Code)

	// Development mode: admin lists, approves, suspends, restores, and revokes invites.
	devRouter := gin.New()
	registerAccountRoutesWithDevMode(devRouter.Group("/api/v1/mail"), NewAccountService(db, true), nil, nil, nil, true)

	listRes := doReq(devRouter, http.MethodGet, "/api/v1/mail/admin/accounts?status=pending", "", apiAdmin.Token)
	require.Equal(t, http.StatusOK, listRes.Code, listRes.Body.String())
	var listBody struct {
		Data []AdminMailAccountSummary `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listRes.Body.Bytes(), &listBody))
	require.Len(t, listBody.Data, 1)
	require.Equal(t, "applicant.box@atoman.org", listBody.Data[0].Address)
	require.Equal(t, "reserved", listBody.Data[0].Status)

	approveRes := doReq(devRouter, http.MethodPost, "/api/v1/mail/admin/accounts/"+reserved.ID.String()+"/approve", "{}", apiAdmin.Token)
	require.Equal(t, http.StatusOK, approveRes.Code, approveRes.Body.String())
	var approved struct {
		Data AdminMailAccountSummary `json:"data"`
	}
	require.NoError(t, json.Unmarshal(approveRes.Body.Bytes(), &approved))
	require.Equal(t, "active", approved.Data.Status)

	// Active account enters dev preview folders cleanly.
	foldersRes := doReq(devRouter, http.MethodGet, "/api/v1/mail/folders", "", apiAlice.Token)
	require.Equal(t, http.StatusOK, foldersRes.Code, foldersRes.Body.String())
	require.Contains(t, foldersRes.Body.String(), "inbox")

	// Active account cannot be rejected, only suspended/restored.
	require.Equal(t, http.StatusConflict, doReq(devRouter, http.MethodPost, "/api/v1/mail/admin/accounts/"+reserved.ID.String()+"/reject", "{}", apiAdmin.Token).Code)

	suspendRes := doReq(devRouter, http.MethodPost, "/api/v1/mail/admin/accounts/"+reserved.ID.String()+"/suspend", "{}", apiAdmin.Token)
	require.Equal(t, http.StatusOK, suspendRes.Code)
	require.Equal(t, http.StatusForbidden, doReq(devRouter, http.MethodGet, "/api/v1/mail/folders", "", apiAlice.Token).Code)

	restoreRes := doReq(devRouter, http.MethodPost, "/api/v1/mail/admin/accounts/"+reserved.ID.String()+"/restore", "{}", apiAdmin.Token)
	require.Equal(t, http.StatusOK, restoreRes.Code)
	require.Equal(t, http.StatusOK, doReq(devRouter, http.MethodGet, "/api/v1/mail/folders", "", apiAlice.Token).Code)
}

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newPublicationReportRouter(t *testing.T, service *Service) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		raw := strings.TrimSpace(c.GetHeader("X-Test-User"))
		if raw != "" {
			parts := strings.SplitN(raw, "|", 2)
			authctx.SetCurrentUser(c, authctx.CurrentUser{ID: uuid.MustParse(parts[0]), Role: parts[1]})
		}
		c.Next()
	})
	RegisterRoutes(router.Group("/api/v1/books"), service)
	return router
}

func publicationReportRequest(router http.Handler, method, path, body, user string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if user != "" {
		request.Header.Set("X-Test-User", user)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func seedPublishedBookAsset(t *testing.T, service *Service) uuid.UUID {
	t.Helper()
	assetID := uuid.New()
	require.NoError(t, service.db.Create(&model.PublishedBookAsset{
		Base:          model.Base{ID: assetID},
		SourceAssetID: uuid.New(),
		Format:        "txt",
		ObjectKey:     "public/" + assetID.String() + ".txt",
		Status:        model.BookPublicationStatusPublished,
	}).Error)
	return assetID
}

func TestPublicationReportHTTPRequiresAuthAndModeratorReview(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.PublishedBookAsset{}, &model.BookPublicationReport{})
	service := NewService(db)
	router := newPublicationReportRouter(t, service)
	assetID := seedPublishedBookAsset(t, service)
	reporter := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleUser}
	moderator := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleModerator}
	reporterHeader := reporter.ID.String() + "|" + reporter.Role
	moderatorHeader := moderator.ID.String() + "|" + moderator.Role
	reportPath := "/api/v1/books/assets/" + assetID.String() + "/reports"
	listPath := "/api/v1/books/admin/publication-reports"

	anonymous := publicationReportRequest(router, http.MethodPost, reportPath, `{"reason":"copyright"}`, "")
	require.Equal(t, http.StatusUnauthorized, anonymous.Code, anonymous.Body.String())
	require.Contains(t, anonymous.Body.String(), `"code":"auth.unauthorized"`)

	invalid := publicationReportRequest(router, http.MethodPost, "/api/v1/books/assets/not-a-uuid/reports", `{"reason":"copyright"}`, reporterHeader)
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	badReason := publicationReportRequest(router, http.MethodPost, reportPath, `{"reason":"rights concern"}`, reporterHeader)
	require.Equal(t, http.StatusBadRequest, badReason.Code, badReason.Body.String())
	require.Contains(t, badReason.Body.String(), `"code":"validation.invalid_request"`)
	missingNote := publicationReportRequest(router, http.MethodPost, reportPath, `{"reason":"other"}`, reporterHeader)
	require.Equal(t, http.StatusBadRequest, missingNote.Code, missingNote.Body.String())

	created := publicationReportRequest(router, http.MethodPost, reportPath, `{"reason":"copyright","note":"不是公有领域"}`, reporterHeader)
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	require.Equal(t, "private, no-store", created.Header().Get("Cache-Control"))
	require.Contains(t, created.Body.String(), `"reason":"copyright\n不是公有领域"`)
	require.Contains(t, created.Body.String(), `"status":"pending"`)
	duplicate := publicationReportRequest(router, http.MethodPost, reportPath, `{"reason":"spam"}`, reporterHeader)
	require.Equal(t, http.StatusConflict, duplicate.Code, duplicate.Body.String())
	require.Contains(t, duplicate.Body.String(), `"code":"books.report_already_exists"`)

	forbiddenList := publicationReportRequest(router, http.MethodGet, listPath, "", reporterHeader)
	require.Equal(t, http.StatusForbidden, forbiddenList.Code, forbiddenList.Body.String())
	require.Contains(t, forbiddenList.Body.String(), `"code":"books.review_forbidden"`)
	anonymousList := publicationReportRequest(router, http.MethodGet, listPath, "", "")
	require.Equal(t, http.StatusUnauthorized, anonymousList.Code, anonymousList.Body.String())

	listed := publicationReportRequest(router, http.MethodGet, listPath, "", moderatorHeader)
	require.Equal(t, http.StatusOK, listed.Code, listed.Body.String())
	require.Contains(t, listed.Body.String(), assetID.String())
	require.Contains(t, listed.Body.String(), `"total":1`)
	var listedBody struct {
		Data struct {
			Items []BookPublicationReportDTO `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(listed.Body.Bytes(), &listedBody))
	require.Len(t, listedBody.Data.Items, 1)

	ownReview := publicationReportRequest(router, http.MethodPut, "/api/v1/books/admin/publication-reports/"+listedBody.Data.Items[0].ID, `{"decision":"removed"}`, reporterHeader)
	require.Equal(t, http.StatusForbidden, ownReview.Code, ownReview.Body.String())
	badDecision := publicationReportRequest(router, http.MethodPut, "/api/v1/books/admin/publication-reports/"+listedBody.Data.Items[0].ID, `{"decision":"archive"}`, moderatorHeader)
	require.Equal(t, http.StatusBadRequest, badDecision.Code, badDecision.Body.String())
	reviewed := publicationReportRequest(router, http.MethodPut, "/api/v1/books/admin/publication-reports/"+listedBody.Data.Items[0].ID, `{"decision":"removed","note":"权利主张成立"}`, moderatorHeader)
	require.Equal(t, http.StatusOK, reviewed.Code, reviewed.Body.String())
	require.Contains(t, reviewed.Body.String(), `"status":"removed"`)
	require.Contains(t, reviewed.Body.String(), `"decision_note":"权利主张成立"`)
	var published model.PublishedBookAsset
	require.NoError(t, db.First(&published, "id = ?", assetID).Error)
	require.Equal(t, model.BookPublicationStatusRemoved, published.Status)

	admin := authctx.CurrentUser{ID: uuid.New(), Role: authctx.RoleAdmin}
	empty := publicationReportRequest(router, http.MethodGet, listPath+"?limit=10&offset=0", "", admin.ID.String()+"|"+admin.Role)
	require.Equal(t, http.StatusOK, empty.Code, empty.Body.String())
	require.Contains(t, empty.Body.String(), `"total":0`)
}

func TestPublicationReportHTTPRateLimitsRepeatedReports(t *testing.T) {
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.PublishedBookAsset{}, &model.BookPublicationReport{})
	service := NewService(db)
	router := newPublicationReportRouter(t, service)
	reporter := uuid.New().String() + "|" + authctx.RoleUser
	var last *httptest.ResponseRecorder
	for i := 0; i < bookPublicationReportLimit+1; i++ {
		assetID := seedPublishedBookAsset(t, service)
		last = publicationReportRequest(router, http.MethodPost, "/api/v1/books/assets/"+assetID.String()+"/reports", `{"reason":"spam"}`, reporter)
		if i < bookPublicationReportLimit {
			require.Equal(t, http.StatusCreated, last.Code, last.Body.String())
		}
	}
	require.Equal(t, http.StatusTooManyRequests, last.Code, last.Body.String())
	require.Contains(t, last.Body.String(), `"code":"books.report_rate_limited"`)
	require.NotEmpty(t, last.Header().Get("Retry-After"))
}

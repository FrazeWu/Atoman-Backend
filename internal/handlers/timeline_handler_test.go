package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"
)

func newTimelineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.TimelineEvent{}, &model.TimelinePerson{}, &model.PersonLocation{}, &model.TimelineRevision{})
	return db
}

func seedTimelineUser(t *testing.T, db *gorm.DB) model.User {
	t.Helper()
	user := model.User{
		Username: "timeline_" + uuid.NewString()[:8],
		Email:    uuid.NewString() + "@test.com",
		Password: "x",
		IsActive: true,
	}
	require.NoError(t, db.Create(&user).Error)
	return user
}

func TestTimelinePublicRoutesRejectPrivateRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTimelineTestDB(t)
	user := seedTimelineUser(t, db)

	event := model.TimelineEvent{
		UserID:    user.UUID,
		Title:     "private event",
		EventDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Location:  "private place",
		Source:    "private source",
		IsPublic:  false,
	}
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Model(&event).Update("is_public", false).Error)

	person := model.TimelinePerson{
		UserID:   user.UUID,
		Name:     "private person",
		IsPublic: false,
	}
	require.NoError(t, db.Create(&person).Error)
	require.NoError(t, db.Model(&person).Update("is_public", false).Error)

	location := model.PersonLocation{
		PersonID:  person.ID,
		Date:      time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		PlaceName: "private location",
		Latitude:  1,
		Longitude: 2,
		Source:    "private source",
	}
	require.NoError(t, db.Create(&location).Error)

	r := gin.New()
	SetupTimelineRoutes(r, db)

	cases := []struct {
		path           string
		expectedStatus int
	}{
		{path: "/api/v1/timeline/events", expectedStatus: http.StatusOK},
		{path: "/api/v1/timeline/events/" + event.ID.String(), expectedStatus: http.StatusNotFound},
		{path: "/api/v1/timeline/persons", expectedStatus: http.StatusOK},
		{path: "/api/v1/timeline/persons/" + person.ID.String(), expectedStatus: http.StatusNotFound},
		{path: "/api/v1/timeline/persons/" + person.ID.String() + "/locations", expectedStatus: http.StatusNotFound},
	}

	for _, testCase := range cases {
		t.Run(testCase.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, testCase.expectedStatus, w.Code, w.Body.String())
			require.NotContains(t, w.Body.String(), "private event")
			require.NotContains(t, w.Body.String(), "private person")
			require.NotContains(t, w.Body.String(), "private location")
			require.NotContains(t, w.Body.String(), `"email"`)
		})
	}
}

func TestTimelinePublicRoutesReturnPublicRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTimelineTestDB(t)
	user := seedTimelineUser(t, db)

	event := model.TimelineEvent{
		UserID:    user.UUID,
		Title:     "public event",
		EventDate: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Location:  "public place",
		Source:    "public source",
		IsPublic:  true,
	}
	require.NoError(t, db.Create(&event).Error)

	person := model.TimelinePerson{
		UserID:   user.UUID,
		Name:     "public person",
		IsPublic: true,
	}
	require.NoError(t, db.Create(&person).Error)

	location := model.PersonLocation{
		PersonID:  person.ID,
		Date:      time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC),
		PlaceName: "public location",
		Latitude:  1,
		Longitude: 2,
		Source:    "public source",
	}
	require.NoError(t, db.Create(&location).Error)

	r := gin.New()
	SetupTimelineRoutes(r, db)

	cases := []string{
		"/api/v1/timeline/events/" + event.ID.String(),
		"/api/v1/timeline/persons/" + person.ID.String(),
		"/api/v1/timeline/persons/" + person.ID.String() + "/locations",
	}

	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			for _, privateField := range []string{`"email"`, `"password"`, `"auth_version"`} {
				require.NotContains(t, w.Body.String(), privateField, "response %s leaked a private user field", path)
			}
		})
	}
}

func TestGetTimelineEventsPaginatesSameDateWithStableOrdering(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTimelineTestDB(t)
	user := seedTimelineUser(t, db)
	eventDate := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	ids := map[int]uuid.UUID{
		1: uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		2: uuid.MustParse("00000000-0000-4000-8000-000000000002"),
		3: uuid.MustParse("00000000-0000-4000-8000-000000000003"),
		4: uuid.MustParse("00000000-0000-4000-8000-000000000004"),
	}
	for _, number := range []int{2, 4, 1, 3} {
		event := model.TimelineEvent{
			Base:      model.Base{ID: ids[number]},
			UserID:    user.UUID,
			Title:     "stable event",
			EventDate: eventDate,
			Location:  "stable place",
			Source:    "stable source",
			IsPublic:  true,
		}
		require.NoError(t, db.Create(&event).Error)
	}

	r := gin.New()
	r.GET("/api/v1/timeline/events", GetTimelineEvents(db))
	requestPage := func(page int) []uuid.UUID {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/timeline/events?page=%d&limit=2", page), nil)
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var response struct {
			Data []model.TimelineEvent `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		result := make([]uuid.UUID, 0, len(response.Data))
		for _, event := range response.Data {
			result = append(result, event.ID)
		}
		return result
	}

	expected := []uuid.UUID{ids[1], ids[2], ids[3], ids[4]}
	pageOne := requestPage(1)
	pageTwo := requestPage(2)
	require.Equal(t, expected[:2], pageOne)
	require.Equal(t, expected[2:], pageTwo)
	require.Equal(t, expected, append(pageOne, pageTwo...))
}

func TestTimelineHistoryUsesCurrentVisibilityAndOmitsPrivateVersions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTimelineTestDB(t)
	owner := seedTimelineUser(t, db)
	reader := seedTimelineUser(t, db)
	admin := model.User{Username: "timeline_admin", Email: "admin@test.com", Password: "x", Role: authctx.RoleAdmin, IsActive: true}
	require.NoError(t, db.Create(&admin).Error)
	event := model.TimelineEvent{
		UserID: owner.UUID, Title: "public event", EventDate: time.Now().UTC(), Location: "place", Source: "source", IsPublic: true,
	}
	require.NoError(t, db.Create(&event).Error)
	privateRevision := model.TimelineRevision{EventID: event.ID, EditorID: owner.UUID, Title: "private version", Content: "private body", IsPublic: false}
	publicRevision := model.TimelineRevision{EventID: event.ID, EditorID: owner.UUID, Title: "public version", Content: "public body", IsPublic: true}
	require.NoError(t, db.Create(&privateRevision).Error)
	require.NoError(t, db.Create(&publicRevision).Error)

	requestHistory := func(user authctx.CurrentUser) *httptest.ResponseRecorder {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			authctx.SetCurrentUser(c, user)
			c.Next()
		})
		router.GET("/events/:id/history", GetTimelineEventHistory(db))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/events/"+event.ID.String()+"/history", nil))
		return response
	}

	readerResponse := requestHistory(authctx.CurrentUser{ID: reader.UUID, Username: reader.Username, Role: authctx.RoleUser})
	require.Equal(t, http.StatusOK, readerResponse.Code, readerResponse.Body.String())
	require.Contains(t, readerResponse.Body.String(), "public version")
	require.NotContains(t, readerResponse.Body.String(), "private version")
	require.NotContains(t, readerResponse.Body.String(), `"email"`)

	require.NoError(t, db.Model(&event).Update("is_public", false).Error)
	readerResponse = requestHistory(authctx.CurrentUser{ID: reader.UUID, Username: reader.Username, Role: authctx.RoleUser})
	require.Equal(t, http.StatusNotFound, readerResponse.Code, readerResponse.Body.String())

	ownerResponse := requestHistory(authctx.CurrentUser{ID: owner.UUID, Username: owner.Username, Role: authctx.RoleUser})
	require.Equal(t, http.StatusOK, ownerResponse.Code, ownerResponse.Body.String())
	require.Contains(t, ownerResponse.Body.String(), "private version")
	adminResponse := requestHistory(authctx.CurrentUser{ID: admin.UUID, Username: admin.Username, Role: admin.Role})
	require.Equal(t, http.StatusOK, adminResponse.Code, adminResponse.Body.String())
	require.Contains(t, adminResponse.Body.String(), "private version")
}

func TestTimelineDatabaseFailuresReturnServerErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newTimelineTestDB(t)
	user := seedTimelineUser(t, db)
	event := model.TimelineEvent{UserID: user.UUID, Title: "event", EventDate: time.Now().UTC(), Location: "place", Source: "source", IsPublic: true}
	person := model.TimelinePerson{UserID: user.UUID, Name: "person", IsPublic: true}
	require.NoError(t, db.Create(&event).Error)
	require.NoError(t, db.Create(&person).Error)
	router := gin.New()
	SetupTimelineRoutes(router, db)

	for _, testCase := range []struct {
		path  string
		table string
	}{
		{path: "/api/v1/timeline/events", table: "timeline_events"},
		{path: "/api/v1/timeline/persons", table: "timeline_persons"},
		{path: "/api/v1/timeline/persons/" + person.ID.String(), table: "person_locations"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			callbackName := "test:timeline-query-failure-" + uuid.NewString()
			queryFailure := errors.New("injected timeline query failure")
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Table == testCase.table {
					tx.AddError(queryFailure)
				}
			}))
			defer func() { _ = db.Callback().Query().Remove(callbackName) }()
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, testCase.path, nil))
			require.Equal(t, http.StatusInternalServerError, response.Code, response.Body.String())
		})
	}
}

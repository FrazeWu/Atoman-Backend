package handlers

import (
	"encoding/json"
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
	"atoman/internal/testdb"
)

func newTimelineTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.TimelineEvent{}, &model.TimelinePerson{}, &model.PersonLocation{})
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

			require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
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

package handlers

import (
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

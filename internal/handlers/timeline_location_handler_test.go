package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func locationTestContext(t *testing.T) (*gorm.DB, *gin.Engine, model.TimelinePerson) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.TimelinePerson{}, &model.PersonLocation{})
	user := model.User{Username: "location-editor", Email: "location-editor@example.test", Password: "hash", IsActive: true}
	require.NoError(t, db.Create(&user).Error)
	person := model.TimelinePerson{UserID: user.UUID, Name: "Traveling person", IsPublic: true}
	require.NoError(t, db.Create(&person).Error)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", user.UUID)
		c.Set("role", "user")
		c.Next()
	})
	router.POST("/persons/:id/locations", AddPersonLocation(db))
	router.PUT("/locations/:id", UpdatePersonLocation(db))
	router.DELETE("/locations/:id", DeletePersonLocation(db))
	return db, router, person
}

func TestPersonLocationRejectsMalformedEndDates(t *testing.T) {
	db, router, person := locationTestContext(t)
	invalid := `{"date":"2020-01-01","end_date":"not-a-date","place_name":"Paris","latitude":1.2,"longitude":2.3,"source":"book"}`
	post := httptest.NewRecorder()
	router.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/persons/"+person.ID.String()+"/locations", strings.NewReader(invalid)))
	require.Equal(t, http.StatusBadRequest, post.Code, post.Body.String())
	var count int64
	require.NoError(t, db.Model(&model.PersonLocation{}).Count(&count).Error)
	require.Zero(t, count)

	valid := `{"date":"2020-01-01","end_date":"2021-01-01","place_name":"Paris","latitude":1.2,"longitude":2.3,"source":"book"}`
	created := httptest.NewRecorder()
	router.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/persons/"+person.ID.String()+"/locations", strings.NewReader(valid)))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	var location model.PersonLocation
	require.NoError(t, db.First(&location).Error)
	update := httptest.NewRecorder()
	router.ServeHTTP(update, httptest.NewRequest(http.MethodPut, "/locations/"+location.ID.String(), strings.NewReader(invalid)))
	require.Equal(t, http.StatusBadRequest, update.Code, update.Body.String())

	var persisted model.PersonLocation
	require.NoError(t, db.First(&persisted, "id = ?", location.ID).Error)
	require.NotNil(t, persisted.EndDate)
	require.Equal(t, 2021, persisted.EndDate.Year())
}

func TestDeletePersonLocationReportsDatabaseFailure(t *testing.T) {
	db, router, person := locationTestContext(t)
	location := model.PersonLocation{PersonID: person.ID, PlaceName: "Paris", Latitude: 1.2, Longitude: 2.3, Source: "book"}
	require.NoError(t, db.Create(&location).Error)

	const hook = "audit:test_failed_location_delete"
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register(hook, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "person_locations" {
			tx.AddError(errors.New("simulated storage failure"))
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Delete().Remove(hook) })

	record := httptest.NewRecorder()
	router.ServeHTTP(record, httptest.NewRequest(http.MethodDelete, "/locations/"+location.ID.String(), nil))
	require.Equal(t, http.StatusInternalServerError, record.Code, record.Body.String())
}

func TestPersonLocationAllowsZeroCoordinates(t *testing.T) {
	db, router, person := locationTestContext(t)
	valid := `{"date":"2020-01-01","place_name":"Equator","latitude":0,"longitude":0,"source":"atlas"}`
	created := httptest.NewRecorder()
	router.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/persons/"+person.ID.String()+"/locations", strings.NewReader(valid)))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	var location model.PersonLocation
	require.NoError(t, db.First(&location).Error)
	require.Zero(t, location.Latitude)
	require.Zero(t, location.Longitude)

	updated := httptest.NewRecorder()
	router.ServeHTTP(updated, httptest.NewRequest(http.MethodPut, "/locations/"+location.ID.String(), strings.NewReader(valid)))
	require.Equal(t, http.StatusOK, updated.Code, updated.Body.String())

	missing := `{"date":"2020-01-01","place_name":"Incomplete","latitude":0,"source":"atlas"}`
	rejected := httptest.NewRecorder()
	router.ServeHTTP(rejected, httptest.NewRequest(http.MethodPost, "/persons/"+person.ID.String()+"/locations", strings.NewReader(missing)))
	require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
}

func TestPersonLocationRejectsOutOfRangeCoordinates(t *testing.T) {
	db, router, person := locationTestContext(t)
	for _, payload := range []string{
		`{"date":"2020-01-01","place_name":"Invalid","latitude":90.1,"longitude":0,"source":"atlas"}`,
		`{"date":"2020-01-01","place_name":"Invalid","latitude":0,"longitude":-180.1,"source":"atlas"}`,
		`{"date":"2020-01-01","place_name":"Invalid","latitude":-90.1,"longitude":0,"source":"atlas"}`,
		`{"date":"2020-01-01","place_name":"Invalid","latitude":0,"longitude":180.1,"source":"atlas"}`,
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/persons/"+person.ID.String()+"/locations", strings.NewReader(payload)))
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
	var count int64
	require.NoError(t, db.Model(&model.PersonLocation{}).Count(&count).Error)
	require.Zero(t, count)
}

package books

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestBookReadingHeartbeatCountsOnlyActiveIntervals(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	session := newBookCloudTestSession(t, service, user, revision.ID, "web")
	start := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	beat := func(sequence int64, elapsed time.Duration, visible, active bool) BookReadingHeartbeatResult {
		t.Helper()
		result, err := service.recordBookReadingHeartbeatAt(ctx, user, session.Session.ID, BookReadingHeartbeatInput{Sequence: sequence, Visible: visible, Active: active}, start.Add(elapsed))
		require.NoError(t, err)
		return result
	}
	require.Zero(t, beat(1, 0, true, true).CreditedSeconds)
	require.EqualValues(t, 15, beat(2, 15*time.Second, true, true).CreditedSeconds)
	retry := beat(2, 20*time.Second, true, true)
	require.True(t, retry.Ignored)
	require.Zero(t, retry.CreditedSeconds)
	require.True(t, beat(1, 21*time.Second, true, true).Ignored)
	require.Zero(t, beat(3, 25*time.Second, false, true).CreditedSeconds)
	require.Zero(t, beat(4, 30*time.Second, true, true).CreditedSeconds)
	require.EqualValues(t, 15, beat(5, 45*time.Second, true, true).CreditedSeconds)
	require.Zero(t, beat(6, 10*time.Minute, true, true).CreditedSeconds, "long idle gaps are not credited")
	require.EqualValues(t, 15, beat(7, 10*time.Minute+15*time.Second, true, true).CreditedSeconds)
	require.Zero(t, beat(8, 10*time.Minute+30*time.Second, true, false).CreditedSeconds)
	stats, err := service.GetBookReadingStatistics(ctx, user, "2026-10-15", "2026-10-15")
	require.NoError(t, err)
	require.EqualValues(t, 45, stats.TotalSeconds)
	require.Len(t, stats.Days, 1)
	require.Len(t, stats.Works, 1)
	require.Equal(t, uuid.MustParse(work.ID), stats.Works[0].WorkID)
	other := newBookLibraryTestReader(t, service)
	stats, err = service.GetBookReadingStatistics(ctx, other, "2026-10-15", "2026-10-15")
	require.NoError(t, err)
	require.Zero(t, stats.TotalSeconds)
	_, err = service.RecordBookReadingHeartbeat(ctx, other, session.Session.ID, BookReadingHeartbeatInput{Sequence: 1, Visible: true, Active: true})
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
}

func TestBookReadingHeartbeatDevicesDoNotDoubleCountAndSplitUTCDays(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, aRevision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	_, bRevision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "en", "pdf")
	a := newBookCloudTestSession(t, service, user, aRevision.ID, "a")
	b := newBookCloudTestSession(t, service, user, bRevision.ID, "b")
	start := time.Date(2026, 10, 15, 23, 59, 50, 0, time.UTC)
	beat := func(session uuid.UUID, sequence int64, seconds int, active bool) BookReadingHeartbeatResult {
		t.Helper()
		r, err := service.recordBookReadingHeartbeatAt(ctx, user, session, BookReadingHeartbeatInput{Sequence: sequence, Visible: active, Active: active}, start.Add(time.Duration(seconds)*time.Second))
		require.NoError(t, err)
		return r
	}
	beat(a.Session.ID, 1, 0, true)
	beat(b.Session.ID, 1, 5, false) // A background tab must not steal A's clock.
	require.EqualValues(t, 20, beat(a.Session.ID, 2, 20, true).CreditedSeconds)
	require.Zero(t, beat(b.Session.ID, 2, 20, true).CreditedSeconds)
	require.EqualValues(t, 15, beat(b.Session.ID, 3, 35, true).CreditedSeconds)
	require.Zero(t, beat(a.Session.ID, 3, 35, true).CreditedSeconds)
	stats, err := service.GetBookReadingStatistics(ctx, user, "2026-10-15", "2026-10-16")
	require.NoError(t, err)
	require.EqualValues(t, 35, stats.TotalSeconds)
	require.Equal(t, []BookReadingDayDTO{{Day: "2026-10-15", Seconds: 10}, {Day: "2026-10-16", Seconds: 25}}, stats.Days)
	require.Len(t, stats.Works, 1, "different revisions still belong to one work")
	_, err = service.GetBookReadingStatistics(ctx, user, "2026-10-17", "2026-10-16")
	require.Error(t, err)
	_, err = service.GetBookReadingStatistics(ctx, user, "bad", "2026-10-16")
	require.Error(t, err)
	_, err = service.GetBookReadingStatistics(ctx, user, "2020-01-01", "2026-10-16")
	require.Error(t, err)
}

func TestBookReadingHeartbeatConcurrentRequestsAreIdempotent(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	ctx := context.Background()
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	session := newBookCloudTestSession(t, service, user, revision.ID, "web")
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	_, err := service.recordBookReadingHeartbeatAt(ctx, user, session.Session.ID, BookReadingHeartbeatInput{Sequence: 1, Visible: true, Active: true}, now)
	require.NoError(t, err)
	start := make(chan struct{})
	results := make(chan BookReadingHeartbeatResult, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			result, err := service.recordBookReadingHeartbeatAt(ctx, user, session.Session.ID, BookReadingHeartbeatInput{Sequence: 2, Visible: true, Active: true}, now.Add(15*time.Second))
			results <- result
			errors <- err
		}()
	}
	close(start)
	var credited int64
	ignored := 0
	for range 2 {
		require.NoError(t, <-errors)
		result := <-results
		credited += result.CreditedSeconds
		if result.Ignored {
			ignored++
		}
	}
	require.EqualValues(t, 15, credited)
	require.Equal(t, 1, ignored)
}

func TestBookReadingActivityHTTP(t *testing.T) {
	service, user, work, _ := newBookResourceTestService(t)
	_, revision := readyBookReadingFile(t, service, user, uuid.MustParse(work.ID), "zh", "txt")
	session := newBookCloudTestSession(t, service, user, revision.ID, "web")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { authctx.SetCurrentUser(c, user); c.Next() })
	RegisterRoutes(router.Group("/api/v1/books"), service)
	url := "/api/v1/books/me/reading-sessions/" + session.Session.ID.String() + "/heartbeat"
	r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(`{"sequence":1,"visible":true,"active":true,"duration":99999}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"credited_seconds":0`)
	require.Equal(t, "private, no-store", w.Header().Get("Cache-Control"))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/books/me/reading-statistics", nil))
	require.Equal(t, 200, w.Code)
	require.Contains(t, w.Body.String(), `"timezone":"UTC"`)
	var count int64
	require.NoError(t, service.db.Model(&model.BookReadingDaily{}).Count(&count).Error)
	require.Zero(t, count)
}

package books

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newBookRatingTestWork(t *testing.T, service *Service, user authctx.CurrentUser, title string) uuid.UUID {
	t.Helper()
	work, err := service.CreateBookWork(context.Background(), user, CreateBookWorkInput{Title: title, Authors: []string{"作者"}})
	require.NoError(t, err)
	return uuid.MustParse(work.ID)
}

func TestBookWorkRatingUsesPublishedReputationWeightsAndSixPersonGate(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	workID := newBookRatingTestWork(t, service, user, "信誉评分")
	testdb.Migrate(t, service.db, &model.ReputationRun{}, &model.UserReputationSnapshot{})
	now := time.Now().UTC()
	old := model.ReputationRun{Status: model.ReputationRunPublished, StartedAt: now.Add(-time.Hour), PublishedAt: &now}
	require.NoError(t, service.db.Create(&old).Error)
	later := now.Add(time.Minute)
	run := model.ReputationRun{Status: model.ReputationRunPublished, StartedAt: later, PublishedAt: &later}
	require.NoError(t, service.db.Create(&run).Error)
	require.NoError(t, service.db.Create(&model.UserReputationSnapshot{RunID: old.ID, UserID: user.ID, Quality: 0, EvaluationWeight: 0}).Error)
	require.NoError(t, service.db.Create(&model.UserReputationSnapshot{RunID: run.ID, UserID: user.ID, Quality: 100, EvaluationWeight: 1}).Error)
	// An unpublished newer calculation must never affect the public result.
	pending := model.ReputationRun{Status: model.ReputationRunRunning, StartedAt: later.Add(time.Hour)}
	require.NoError(t, service.db.Create(&pending).Error)
	require.NoError(t, service.db.Create(&model.UserReputationSnapshot{RunID: pending.ID, UserID: user.ID, EvaluationWeight: 0}).Error)
	summary, err := service.SetBookWorkRating(ctx, user, workID, 10)
	require.NoError(t, err)
	require.False(t, summary.RatingActive)
	require.EqualValues(t, 6, summary.MinimumRatingCount)
	for range 4 {
		reader := newBookLibraryTestReader(t, service)
		_, err := service.SetBookWorkRating(ctx, reader, workID, 2)
		require.NoError(t, err)
	}
	list, err := service.ListHighRatedBookWorks(ctx, 20, 0)
	require.NoError(t, err)
	require.Zero(t, list.Total, "five people must not activate the official ranking")
	reader := newBookLibraryTestReader(t, service)
	summary, err = service.SetBookWorkRating(ctx, reader, workID, 2)
	require.NoError(t, err)
	require.EqualValues(t, 6, summary.RatingCount)
	require.Equal(t, 3.3, summary.RatingScore)
	require.NotNil(t, summary.WeightedRatingScore)
	require.Equal(t, 6.0, *summary.WeightedRatingScore, "(10*1 + 5*2*.2)/(1+5*.2)")
	require.True(t, summary.RatingActive)
	list, err = service.ListHighRatedBookWorks(ctx, 1, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, list.Total)
	require.Len(t, list.Items, 1)
	require.Equal(t, workID, list.Items[0].WorkID)
	require.Nil(t, list.Items[0].ViewerRating)
	// Deactivation immediately removes a vote, without waiting for a reputation job.
	require.NoError(t, service.db.Model(&model.User{}).Where("uuid = ?", reader.ID).Update("is_active", false).Error)
	summary, err = service.GetBookWorkRating(ctx, workID, nil)
	require.NoError(t, err)
	require.EqualValues(t, 5, summary.RatingCount)
	require.False(t, summary.RatingActive)
	list, err = service.ListHighRatedBookWorks(ctx, 20, 0)
	require.NoError(t, err)
	require.Empty(t, list.Items)
	_, err = service.SetBookWorkRating(ctx, reader, workID, 8)
	require.Equal(t, 403, apperr.FromError(err).HTTPStatus)
	// Deletion is excluded too, even if the account had a high published weight.
	require.NoError(t, service.db.Where("uuid = ?", user.ID).Delete(&model.User{}).Error)
	summary, err = service.GetBookWorkRating(ctx, workID, nil)
	require.NoError(t, err)
	require.EqualValues(t, 4, summary.RatingCount)
}

func TestBookWorkRatingConcurrentUpsertsDeleteRestoreAndPrivacy(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	workID := newBookRatingTestWork(t, service, user, "评分幂等")
	for _, score := range []int{0, 11, -1} {
		_, err := service.SetBookWorkRating(ctx, user, workID, score)
		require.Equal(t, 400, apperr.FromError(err).HTTPStatus)
	}
	_, err := service.SetBookWorkRating(ctx, authctx.CurrentUser{}, workID, 8)
	require.Equal(t, 401, apperr.FromError(err).HTTPStatus)
	start := make(chan struct{})
	errors := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			_, err := service.SetBookWorkRating(ctx, user, workID, 8)
			errors <- err
		}()
	}
	close(start)
	for range 8 {
		require.NoError(t, <-errors)
	}
	var count int64
	require.NoError(t, service.db.Unscoped().Model(&model.BookRating{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	summary, err := service.GetBookWorkRating(ctx, workID, &user.ID)
	require.NoError(t, err)
	require.Equal(t, 8, *summary.ViewerRating)
	other := newBookLibraryTestReader(t, service)
	summary, err = service.GetBookWorkRating(ctx, workID, &other.ID)
	require.NoError(t, err)
	require.Nil(t, summary.ViewerRating)
	for range 2 {
		summary, err = service.DeleteBookWorkRating(ctx, user, workID)
		require.NoError(t, err)
		require.Zero(t, summary.RatingCount)
		require.Nil(t, summary.WeightedRatingScore)
		require.Nil(t, summary.ViewerRating)
	}
	summary, err = service.SetBookWorkRating(ctx, user, workID, 6)
	require.NoError(t, err)
	require.EqualValues(t, 1, summary.RatingCount)
	require.Equal(t, 6, *summary.ViewerRating)
	_, err = service.GetBookWorkRating(ctx, uuid.New(), nil)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
	// Old records without a new wiki revision are not migrated into the new API.
	legacy := model.BookWork{Title: "旧条目", LifecycleStatus: model.BookLifecycleStatusActive}
	require.NoError(t, service.db.Create(&legacy).Error)
	_, err = service.SetBookWorkRating(ctx, user, legacy.ID, 8)
	require.Equal(t, 404, apperr.FromError(err).HTTPStatus)
}

func TestBookWorkRatingRankingSortsBeforePaginationAndExcludesZeroWeight(t *testing.T) {
	service, user := newBookWikiTestService(t)
	ctx := context.Background()
	low := newBookRatingTestWork(t, service, user, "低分")
	high := newBookRatingTestWork(t, service, user, "高分")
	closed := newBookRatingTestWork(t, service, user, "已关闭")
	readers := []authctx.CurrentUser{user}
	for range 5 {
		readers = append(readers, newBookLibraryTestReader(t, service))
	}
	for _, reader := range readers {
		for _, vote := range []model.BookRating{{UserID: reader.ID, WorkID: low, Score: 6}, {UserID: reader.ID, WorkID: high, Score: 9}, {UserID: reader.ID, WorkID: closed, Score: 10}} {
			require.NoError(t, service.db.Create(&vote).Error)
		}
	}
	require.NoError(t, service.db.Model(&model.BookWork{}).Where("id = ?", closed).Update("edit_status", model.BookEditStatusClosed).Error)
	list, err := service.ListHighRatedBookWorks(ctx, 1, 0)
	require.NoError(t, err)
	require.EqualValues(t, 2, list.Total)
	require.Len(t, list.Items, 1)
	require.Equal(t, high, list.Items[0].WorkID)
	list, err = service.ListHighRatedBookWorks(ctx, 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, list.Total)
	require.Equal(t, low, list.Items[0].WorkID)
	list, err = service.ListHighRatedBookWorks(ctx, 1, 2)
	require.NoError(t, err)
	require.Empty(t, list.Items)
	testdb.Migrate(t, service.db, &model.ReputationRun{}, &model.UserReputationSnapshot{})
	now := time.Now().UTC()
	run := model.ReputationRun{Status: model.ReputationRunPublished, StartedAt: now, PublishedAt: &now}
	require.NoError(t, service.db.Create(&run).Error)
	for _, reader := range readers {
		require.NoError(t, service.db.Create(&model.UserReputationSnapshot{RunID: run.ID, UserID: reader.ID, Quality: 0, EvaluationWeight: 0}).Error)
	}
	summary, err := service.GetBookWorkRating(ctx, high, nil)
	require.NoError(t, err)
	require.EqualValues(t, 6, summary.RatingCount)
	require.Nil(t, summary.WeightedRatingScore)
	require.False(t, summary.RatingActive)
	list, err = service.ListHighRatedBookWorks(ctx, 20, 0)
	require.NoError(t, err)
	require.Zero(t, list.Total)
	require.Empty(t, list.Items)
}

func TestBookWorkRatingsFollowMergeAndUndoWithoutDoubleVotes(t *testing.T) {
	service, user, source, target := newBookMergeTestService(t)
	ctx := context.Background()
	sourceID, targetID := uuid.MustParse(source.ID), uuid.MustParse(target.ID)
	_, err := service.SetBookWorkRating(ctx, user, sourceID, 4)
	require.NoError(t, err)
	_, err = service.SetBookWorkRating(ctx, user, targetID, 8)
	require.NoError(t, err)
	other := newBookLibraryTestReader(t, service)
	_, err = service.SetBookWorkRating(ctx, other, sourceID, 6)
	require.NoError(t, err)
	merge := mergeBookTestWorks(t, service, user, source, target)
	summary, err := service.GetBookWorkRating(ctx, sourceID, &user.ID)
	require.NoError(t, err)
	require.Equal(t, targetID, summary.WorkID)
	require.EqualValues(t, 2, summary.RatingCount)
	require.Equal(t, 7.0, summary.RatingScore)
	require.Equal(t, 8, *summary.ViewerRating)
	_, err = service.RevertBookWorkMerge(ctx, user, merge.ID)
	require.NoError(t, err)
	summary, err = service.GetBookWorkRating(ctx, sourceID, &user.ID)
	require.NoError(t, err)
	require.Equal(t, sourceID, summary.WorkID)
	require.EqualValues(t, 2, summary.RatingCount)
	require.Equal(t, 4, *summary.ViewerRating)
	// Writes through the old address target the canonical work, never a shadow vote.
	source, err = service.GetBookWork(ctx, sourceID)
	require.NoError(t, err)
	target, err = service.GetBookWork(ctx, targetID)
	require.NoError(t, err)
	secondMerge := mergeBookTestWorks(t, service, user, source, target)
	summary, err = service.SetBookWorkRating(ctx, user, sourceID, 10)
	require.NoError(t, err)
	require.Equal(t, targetID, summary.WorkID)
	require.EqualValues(t, 2, summary.RatingCount)
	_, err = service.RevertBookWorkMerge(ctx, user, secondMerge.ID)
	require.Equal(t, 409, apperr.FromError(err).HTTPStatus, "undo must not overwrite a newer rating")
}

func TestBookWorkRatingHTTPDoesNotLeakViewerRating(t *testing.T) {
	service, user := newBookWikiTestService(t)
	workID := newBookRatingTestWork(t, service, user, "接口测试")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-User") != "" {
			authctx.SetCurrentUser(c, user)
		}
	})
	RegisterRoutes(router.Group("/api/v1/books"), service)
	url := "/api/v1/books/works/" + workID.String() + "/rating"
	request := func(method, path, body string, login bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if login {
			req.Header.Set("X-Test-User", "1")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	require.Equal(t, 401, request(http.MethodPut, url, `{"score":8}`, false).Code)
	require.Equal(t, 400, request(http.MethodPut, url, `{"score":11}`, true).Code)
	require.Equal(t, 200, request(http.MethodPut, url, `{"score":8}`, true).Code)
	anonymous := request(http.MethodGet, url, "", false)
	require.Equal(t, 200, anonymous.Code)
	require.Equal(t, "private, no-store", anonymous.Header().Get("Cache-Control"))
	require.NotContains(t, anonymous.Body.String(), "viewer_rating")
	require.NotContains(t, anonymous.Body.String(), user.ID.String())
	loggedIn := request(http.MethodGet, url, "", true)
	require.Equal(t, 200, loggedIn.Code)
	var payload struct {
		Data BookWorkRatingDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(loggedIn.Body.Bytes(), &payload))
	require.Equal(t, 8, *payload.Data.ViewerRating)
	require.Equal(t, 200, request(http.MethodDelete, url, "", true).Code)
	require.Equal(t, 200, request(http.MethodGet, "/api/v1/books/rankings/high-rated", "", false).Code)
	require.Equal(t, 400, request(http.MethodGet, "/api/v1/books/rankings/high-rated?limit=no", "", false).Code)
}

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPodcastRatingColdStart(t *testing.T) {
	for _, schema := range []string{"canonical", "distinct-content-id", "legacy"} {
		t.Run(schema, func(t *testing.T) {
			router, db, user, channel := newPodcastHandlerTestDB(t)
			testdb.Migrate(t, db, &model.PostRating{})
			episode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
			if schema == "legacy" {
				require.NoError(t, db.Migrator().DropTable(&model.ContentEpisodeExtension{}, &model.ContentEntry{}))
			} else if schema == "distinct-content-id" {
				var entry model.ContentEntry
				require.NoError(t, db.First(&entry, "id = ?", episode.PostID).Error)
				entry.ID = uuid.New()
				require.NoError(t, db.Create(&entry).Error)
				require.NoError(t, db.Model(&model.ContentEpisodeExtension{}).Where("episode_id = ?", episode.ID).Update("content_id", entry.ID).Error)
			}
			path := "/api/v1/podcast/episodes/" + episode.ID.String() + "/rating"
			token := podcastAuthHeader(t, db, user)
			request := func(method, url, body, auth string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, url, bytes.NewBufferString(body))
				req.Header.Set("Content-Type", "application/json")
				if auth != "" {
					req.Header.Set("Authorization", auth)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				return w
			}
			summary := func(method, url, body, auth string) contentRatingSummary {
				w := request(method, url, body, auth)
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var result contentRatingSummary
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &result))
				return result
			}
			initial := summary(http.MethodGet, path, "", "")
			require.Zero(t, initial.RatingCount)
			require.Zero(t, initial.RatingScore)
			require.Nil(t, initial.ViewerRating)
			require.Equal(t, http.StatusUnauthorized, request(http.MethodPut, path, `{"score":8}`, "").Code)
			require.Equal(t, http.StatusUnauthorized, request(http.MethodDelete, path, "", "").Code)
			for _, score := range []string{"0", "11", "1.5"} {
				require.Equal(t, http.StatusBadRequest, request(http.MethodPut, path, `{"score":`+score+`}`, token).Code)
			}
			for _, score := range []int{1, 10, 8} {
				result := summary(http.MethodPut, path, `{"score":`+strconv.Itoa(score)+`}`, token)
				require.EqualValues(t, 1, result.RatingCount)
				require.EqualValues(t, score, result.RatingScore)
				require.Equal(t, score, *result.ViewerRating)
			}
			cleared := summary(http.MethodDelete, path, "", token)
			require.Zero(t, cleared.RatingCount)
			require.Nil(t, cleared.ViewerRating)

			// 首次并发提交与软删除后重新评分都只能留下一个有效记录。
			contentID, err := contentmodule.PodcastContentID(db, episode.ID)
			require.NoError(t, err)
			var wg sync.WaitGroup
			errs := make(chan error, 10)
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- db.Clauses(contentRatingUpsertConflict(9)).Create(&model.PostRating{UserID: user.UUID, ContentID: contentID, Score: 9}).Error
				}()
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				require.NoError(t, err)
			}
			result := summary(http.MethodGet, path, "", token)
			require.EqualValues(t, 1, result.RatingCount)
			require.Equal(t, 9, *result.ViewerRating)
			require.Nil(t, summary(http.MethodGet, path, "", "").ViewerRating)

			// 换连接、重建路由后仍能读到持久化的评分及详情摘要。
			reopened, err := gorm.Open(postgres.Open(db.Dialector.(*postgres.Dialector).DSN), &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := reopened.DB()
			require.NoError(t, err)
			defer sqlDB.Close()
			router = gin.New()
			SetupPodcastRoutes(router, reopened, nil)
			detail := summary(http.MethodGet, "/api/v1/podcast/episodes/"+episode.ID.String(), "", token)
			require.Equal(t, result, detail)

			for i := 0; i < 4; i++ {
				other := model.User{Username: uuid.NewString(), Email: uuid.NewString() + "@example.com", Password: "hash", Role: "user", IsActive: true}
				require.NoError(t, db.Create(&other).Error)
				summary(http.MethodPut, path, `{"score":7}`, podcastAuthHeader(t, db, other))
			}
			require.EqualValues(t, 5, summary(http.MethodGet, path, "", "").RatingCount)
			require.EqualValues(t, 4, summary(http.MethodDelete, path, "", token).RatingCount)
			require.Equal(t, http.StatusNotFound, request(http.MethodGet, "/api/v1/podcast/episodes/"+uuid.NewString()+"/rating", "", "").Code)
		})
	}
}

func TestPodcastRatingRejectsUnpublishedAndInvisibleEpisodes(t *testing.T) {
	router, db, owner, channel := newPodcastHandlerTestDB(t)
	testdb.Migrate(t, db, &model.PostRating{})
	other := model.User{Username: "other-rater", Email: "other-rater@example.com", Password: "hash", Role: "user", IsActive: true}
	require.NoError(t, db.Create(&other).Error)
	for _, state := range []string{"draft", "private", "followers", "deleted"} {
		episode := createPodcastEpisodeForPostStatus(t, db, owner, channel, "published")
		updates := map[string]any{}
		switch state {
		case "draft":
			updates["status"] = "draft"
		case "deleted":
			updates["deleted_at"] = gorm.Expr("CURRENT_TIMESTAMP")
		default:
			updates["visibility"] = state
		}
		require.NoError(t, db.Model(&model.ContentEntry{}).Where("id = ?", episode.PostID).Updates(updates).Error)
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			token := podcastAuthHeader(t, db, other)
			if state == "draft" {
				token = podcastAuthHeader(t, db, owner)
			}
			req := httptest.NewRequest(method, "/api/v1/podcast/episodes/"+episode.ID.String()+"/rating", bytes.NewBufferString(`{"score":8}`))
			req.Header.Set("Authorization", token)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, http.StatusNotFound, w.Code, state+": "+w.Body.String())
		}
	}
}

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newPodcastHandlerTestDB(t *testing.T) (*gin.Engine, *gorm.DB, model.User, model.Channel) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")

	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.PodcastEpisode{},
		&model.StudioMetricEvent{},
		&model.PodcastEpisodeBookmark{},
		&model.ChannelBookmark{},
		&model.DiscussionTarget{},
		&model.CommentEntry{},
		&model.CommentTimeAnchor{},
	)

	user := model.User{Username: "podcast-user", Email: "podcast-user@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	channel := model.Channel{UserID: &user.UUID, Name: "Podcast", Slug: "podcast"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	r := gin.New()
	r.Use(middleware.OptionalAuthMiddleware())
	SetupPodcastRoutes(r, db, nil)
	return r, db, user, channel
}

func TestSetupPodcastRoutesDoesNotMountLegacyCreatorRoutes(t *testing.T) {
	r, _, _, _ := newPodcastHandlerTestDB(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/podcast/creator/dashboard", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected legacy creator route to return 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestStudioPodcastPlaybackRecordsPlayAndCompleteMetrics(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	episode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")

	for _, event := range []string{"play", "complete"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/episodes/"+episode.ID.String()+"/playback", bytes.NewBufferString(`{"event":"`+event+`"}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}

	var events []model.StudioMetricEvent
	require.NoError(t, db.Where("channel_id = ? AND content_type = ? AND content_id = ?", channel.ID, "podcast", episode.ID).Order("created_at ASC").Find(&events).Error)
	require.Len(t, events, 2)
	require.Equal(t, "play", events[0].Metric)
	require.Equal(t, "complete", events[1].Metric)
}

func createPodcastEpisodeForPostStatus(t *testing.T, db *gorm.DB, user model.User, channel model.Channel, status string) model.PodcastEpisode {
	t.Helper()

	post := model.Post{
		UserID:    user.UUID,
		ChannelID: &channel.ID,
		Title:     status + " episode",
		Content:   "private shownotes for " + status,
		Status:    status,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create %s post: %v", status, err)
	}

	episode := model.PodcastEpisode{
		PostID:          post.ID,
		ChannelID:       channel.ID,
		AudioURL:        "https://cdn.example.com/" + status + ".mp3",
		EpisodeCoverURL: "https://cdn.example.com/" + status + ".jpg",
		SeasonNumber:    1,
		EpisodeNumber:   1,
	}
	if err := db.Create(&episode).Error; err != nil {
		t.Fatalf("create %s episode: %v", status, err)
	}
	return episode
}

func podcastAuthHeader(t *testing.T, user model.User) string {
	t.Helper()
	return "Bearer " + signedAuthClaimsTokenForTest(t, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
	})
}

func multipartPodcastUploadBody(t *testing.T, field, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="` + field + `"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	if err != nil {
		t.Fatalf("create %s part: %v", field, err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write %s part: %v", field, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func newPodcastUploadTestRouter(t *testing.T, path string, handler gin.HandlerFunc) (*gin.Engine, model.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	testdb.Migrate(t, db, &model.User{})

	user := model.User{Username: "podcast-upload-user", Email: uuid.NewString() + "@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create upload user: %v", err)
	}

	r := gin.New()
	r.POST(path, middleware.AuthMiddleware(), handler)
	return r, user
}

func validMP3Bytes() []byte {
	return append([]byte("ID3\x03\x00\x00\x00\x00\x00!"), bytes.Repeat([]byte{0}, 64)...)
}

func validWAVBytes() []byte {
	return append([]byte("RIFF$\x00\x00\x00WAVEfmt "), bytes.Repeat([]byte{0}, 64)...)
}

func TestGetPodcastEpisodesReturnsInternalServerErrorWhenQueryFails(t *testing.T) {
	r, db, _, _ := newPodcastHandlerTestDB(t)

	callbackName := "podcast_episode_list_error_" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "podcast_episodes" {
			tx.AddError(errors.New("injected episode list error"))
		}
	}); err != nil {
		t.Fatalf("register query error callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/podcast/episodes", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetPodcastEpisodesPaginatesLatestEpisodesWithStableOrdering(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	createdAt := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	ids := map[int]uuid.UUID{
		1: uuid.MustParse("00000000-0000-4000-8000-000000000001"),
		2: uuid.MustParse("00000000-0000-4000-8000-000000000002"),
		3: uuid.MustParse("00000000-0000-4000-8000-000000000003"),
		4: uuid.MustParse("00000000-0000-4000-8000-000000000004"),
	}
	for _, number := range []int{2, 4, 1, 3} {
		post := model.Post{
			UserID:    user.UUID,
			ChannelID: &channel.ID,
			Title:     fmt.Sprintf("paged episode %d", number),
			Content:   "shownotes",
			Status:    "published",
		}
		require.NoError(t, db.Create(&post).Error)
		episode := model.PodcastEpisode{
			Base:      model.Base{ID: ids[number], CreatedAt: createdAt},
			PostID:    post.ID,
			ChannelID: channel.ID,
			AudioURL:  fmt.Sprintf("https://cdn.example.com/paged-%d.mp3", number),
		}
		require.NoError(t, db.Create(&episode).Error)
	}

	request := func(path string) (int, []model.PodcastEpisode) {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var episodes []model.PodcastEpisode
		if w.Code == http.StatusOK {
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &episodes))
		}
		return w.Code, episodes
	}

	expected := []uuid.UUID{ids[4], ids[3], ids[2], ids[1]}
	pageOneStatus, pageOne := request("/api/v1/podcast/episodes?sort=latest&page=1&limit=2")
	pageTwoStatus, pageTwo := request("/api/v1/podcast/episodes?sort=latest&page=2&limit=2")
	require.Equal(t, http.StatusOK, pageOneStatus)
	require.Equal(t, http.StatusOK, pageTwoStatus)
	require.Equal(t, expected[:2], []uuid.UUID{pageOne[0].ID, pageOne[1].ID})
	require.Equal(t, expected[2:], []uuid.UUID{pageTwo[0].ID, pageTwo[1].ID})
	require.Equal(t, expected, []uuid.UUID{pageOne[0].ID, pageOne[1].ID, pageTwo[0].ID, pageTwo[1].ID})

	defaultStatus, defaultEpisodes := request("/api/v1/podcast/episodes")
	require.Equal(t, http.StatusOK, defaultStatus)
	require.Len(t, defaultEpisodes, 4)
}

func TestGetPodcastEpisodesRejectsRandomPaginationAfterFirstPage(t *testing.T) {
	r, _, _, _ := newPodcastHandlerTestDB(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/podcast/episodes?sort=random&page=2&limit=20", nil))

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

func TestGetPodcastEpisodeAllowsPublishedOrAuthorDraftOnly(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	otherUser := model.User{Username: "podcast-other-user", Email: "podcast-other-user@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&otherUser).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	published := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
	draft := createPodcastEpisodeForPostStatus(t, db, user, channel, "draft")
	scheduled := createPodcastEpisodeForPostStatus(t, db, user, channel, "scheduled")
	deletedPostEpisode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
	if err := db.Delete(&model.Post{}, "id = ?", deletedPostEpisode.PostID).Error; err != nil {
		t.Fatalf("soft delete post: %v", err)
	}
	deletedEpisode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
	if err := db.Delete(&deletedEpisode).Error; err != nil {
		t.Fatalf("soft delete episode: %v", err)
	}

	cases := []struct {
		name       string
		id         string
		authHeader string
		want       int
	}{
		{name: "published post is visible", id: published.ID.String(), want: http.StatusOK},
		{name: "draft post is hidden", id: draft.ID.String(), want: http.StatusNotFound},
		{name: "draft post is visible to its author", id: draft.ID.String(), authHeader: podcastAuthHeader(t, user), want: http.StatusOK},
		{name: "draft post is hidden from another user", id: draft.ID.String(), authHeader: podcastAuthHeader(t, otherUser), want: http.StatusNotFound},
		{name: "scheduled post is hidden from its author", id: scheduled.ID.String(), authHeader: podcastAuthHeader(t, user), want: http.StatusNotFound},
		{name: "soft deleted post is hidden", id: deletedPostEpisode.ID.String(), want: http.StatusNotFound},
		{name: "soft deleted episode is hidden", id: deletedEpisode.ID.String(), want: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/podcast/episodes/"+tc.id, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}

			r.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
			if tc.want == http.StatusNotFound {
				body := w.Body.String()
				for _, leaked := range []string{"audio_url", "episode_cover_url", "shownotes", "https://cdn.example.com/"} {
					if strings.Contains(body, leaked) {
						t.Fatalf("404 response leaked %q: %s", leaked, body)
					}
				}
			}
		})
	}
}

func TestUploadPodcastCoverRejectsSpoofedPNGContent(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")

	var s3Path string
	var s3ContentType string
	r, user := newPodcastUploadTestRouter(t, "/api/v1/podcast/upload-cover", UploadPodcastCover(fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType)))

	body, contentType := multipartPodcastUploadBody(t, "cover", "spoof.png", "image/png", []byte("not really a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/upload-cover", body)
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for spoofed cover content, got %d: %s", w.Code, w.Body.String())
	}
	if s3Path != "" || s3ContentType != "" {
		t.Fatalf("expected spoofed cover to be rejected before S3, got path=%q contentType=%q", s3Path, s3ContentType)
	}
}

func TestUploadPodcastAudioRejectsSpoofedMP3Content(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")

	var s3Path string
	var s3ContentType string
	r, user := newPodcastUploadTestRouter(t, "/api/v1/podcast/upload-audio", UploadPodcastAudio(fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType)))

	body, contentType := multipartPodcastUploadBody(t, "audio", "spoof.mp3", "audio/mpeg", []byte("not really an mp3"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/upload-audio", body)
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for spoofed audio content, got %d: %s", w.Code, w.Body.String())
	}
	if s3Path != "" || s3ContentType != "" {
		t.Fatalf("expected spoofed audio to be rejected before S3, got path=%q contentType=%q", s3Path, s3ContentType)
	}
}

func TestUploadPodcastAudioAllowsRealMP3Header(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")

	var s3Path string
	var s3ContentType string
	r, user := newPodcastUploadTestRouter(t, "/api/v1/podcast/upload-audio", UploadPodcastAudio(fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType)))

	body, contentType := multipartPodcastUploadBody(t, "audio", "episode.mp3", "audio/mpeg", validMP3Bytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/upload-audio", body)
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid mp3, got %d: %s", w.Code, w.Body.String())
	}
	if s3ContentType != "audio/mpeg" {
		t.Fatalf("expected S3 content type audio/mpeg, got %q", s3ContentType)
	}
	if s3Path == "" {
		t.Fatal("expected valid podcast audio to be uploaded to S3")
	}
}

func TestUploadPodcastAudioAllowsRealWAVHeader(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")

	var s3Path string
	var s3ContentType string
	r, user := newPodcastUploadTestRouter(t, "/api/v1/podcast/upload-audio", UploadPodcastAudio(fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType)))

	body, contentType := multipartPodcastUploadBody(t, "audio", "episode.wav", "audio/wav", validWAVBytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/upload-audio", body)
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid wav, got %d: %s", w.Code, w.Body.String())
	}
	if s3ContentType != "audio/wav" {
		t.Fatalf("expected S3 content type audio/wav, got %q", s3ContentType)
	}
	if s3Path == "" {
		t.Fatal("expected valid podcast audio to be uploaded to S3")
	}
}

func TestSetupPodcastRoutesMountsRecommendationEpisodesEndpoint(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)

	post := model.Post{
		UserID:     user.UUID,
		ChannelID:  &channel.ID,
		Title:      "推荐播客",
		Content:    "这是适合推荐的播客 shownotes。",
		Summary:    "推荐摘要",
		Status:     "published",
		Visibility: "public",
		ViewCount:  82,
	}
	if err := db.Create(&post).Error; err != nil {
		t.Fatalf("create post: %v", err)
	}

	episode := model.PodcastEpisode{
		PostID:          post.ID,
		ChannelID:       channel.ID,
		AudioURL:        "https://cdn.example.com/recommend.mp3",
		EpisodeCoverURL: "https://cdn.example.com/recommend.jpg",
		DurationSec:     1800,
		SeasonNumber:    1,
		EpisodeNumber:   1,
	}
	if err := db.Create(&episode).Error; err != nil {
		t.Fatalf("create episode: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/podcast/recommend/episodes?mode=hot&page=1&page_size=20", nil)
	r.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("expected recommendation route to be mounted, got 404: %s", w.Body.String())
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Summary     string `json:"summary"`
			ContentType string `json:"content_type"`
			TargetPath  string `json:"target_path"`
			ScoreLabel  string `json:"score_label"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data) == 0 {
		t.Fatalf("expected recommendation items, got %s", w.Body.String())
	}
	first := payload.Data[0]
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" || first.ContentType != "podcast" {
		t.Fatalf("expected recommendation dto fields, got %#v", first)
	}
}

func TestCreatePodcastEpisodeBookmarkIsIdempotentWithRepeatedRequests(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	episode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")

	body, err := json.Marshal(map[string]any{"episode_id": episode.ID})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/bookmarks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", podcastAuthHeader(t, user))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("request %d status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}

	var count int64
	if err := db.Model(&model.PodcastEpisodeBookmark{}).Where("user_id = ? AND episode_id = ?", user.UUID, episode.ID).Count(&count).Error; err != nil {
		t.Fatalf("count bookmarks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 podcast episode bookmark row, got %d", count)
	}
}

func TestListAndDeletePodcastEpisodeBookmarks(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	otherUser := model.User{Username: "podcast-other-user", Email: "podcast-other@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&otherUser).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	episode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
	otherEpisode := createPodcastEpisodeForPostStatus(t, db, otherUser, channel, "published")
	bookmark := model.PodcastEpisodeBookmark{UserID: user.UUID, EpisodeID: episode.ID}
	otherBookmark := model.PodcastEpisodeBookmark{UserID: otherUser.UUID, EpisodeID: otherEpisode.ID}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	if err := db.Create(&otherBookmark).Error; err != nil {
		t.Fatalf("create other bookmark: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/podcast/bookmarks", nil)
	listReq.Header.Set("Authorization", podcastAuthHeader(t, user))
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listW.Code, listW.Body.String())
	}
	if !strings.Contains(listW.Body.String(), bookmark.ID.String()) {
		t.Fatalf("expected response to contain bookmark %s: %s", bookmark.ID, listW.Body.String())
	}
	if strings.Contains(listW.Body.String(), otherBookmark.ID.String()) {
		t.Fatalf("expected response to exclude other bookmark %s: %s", otherBookmark.ID, listW.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/podcast/bookmarks/"+bookmark.ID.String(), nil)
	deleteReq.Header.Set("Authorization", podcastAuthHeader(t, user))
	deleteW := httptest.NewRecorder()
	r.ServeHTTP(deleteW, deleteReq)
	if deleteW.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteW.Code, deleteW.Body.String())
	}

	var count int64
	if err := db.Model(&model.PodcastEpisodeBookmark{}).Where("id = ?", bookmark.ID).Count(&count).Error; err != nil {
		t.Fatalf("count remaining bookmark: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected bookmark to be deleted, got count=%d", count)
	}
}

func TestPodcastEpisodeBookmarksSupportPopularSort(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	hotEpisode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
	coldEpisode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
	if err := db.Create(&model.PodcastEpisodeBookmark{UserID: user.UUID, EpisodeID: coldEpisode.ID}).Error; err != nil {
		t.Fatalf("create cold bookmark: %v", err)
	}
	if err := db.Create(&model.PodcastEpisodeBookmark{UserID: user.UUID, EpisodeID: hotEpisode.ID}).Error; err != nil {
		t.Fatalf("create hot bookmark: %v", err)
	}
	if err := db.Model(&model.Post{}).Where("id = ?", hotEpisode.PostID).Update("view_count", 95).Error; err != nil {
		t.Fatalf("update hot post view count: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/podcast/bookmarks?sort=popular", nil)
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			EpisodeID string `json:"episode_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) < 2 {
		t.Fatalf("expected 2 bookmarks, got %s", w.Body.String())
	}
	if resp.Data[0].EpisodeID != hotEpisode.ID.String() {
		t.Fatalf("expected hot episode first, got %#v", resp.Data)
	}
}

func TestCreatePodcastShowBookmarkIsIdempotentWithRepeatedRequests(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)

	body, err := json.Marshal(map[string]any{"channel_id": channel.ID})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/show-bookmarks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", podcastAuthHeader(t, user))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("request %d status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}

	var count int64
	if err := db.Model(&model.ChannelBookmark{}).Where("user_id = ? AND channel_id = ?", user.UUID, channel.ID).Count(&count).Error; err != nil {
		t.Fatalf("count show bookmarks: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 show bookmark row, got %d", count)
	}
}

func TestListAndDeletePodcastShowBookmarks(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	otherChannel := model.Channel{Name: "Other Show", Slug: "other-show"}
	if err := db.Create(&otherChannel).Error; err != nil {
		t.Fatalf("create other channel: %v", err)
	}
	otherUser := model.User{Username: "show-other-user", Email: "show-other@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&otherUser).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	bookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: channel.ID, Kind: "podcast_show"}
	otherBookmark := model.ChannelBookmark{UserID: otherUser.UUID, ChannelID: otherChannel.ID}
	if err := db.Create(&bookmark).Error; err != nil {
		t.Fatalf("create bookmark: %v", err)
	}
	if err := db.Create(&otherBookmark).Error; err != nil {
		t.Fatalf("create other bookmark: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/podcast/show-bookmarks", nil)
	listReq.Header.Set("Authorization", podcastAuthHeader(t, user))
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listW.Code, listW.Body.String())
	}
	if !strings.Contains(listW.Body.String(), bookmark.ID.String()) {
		t.Fatalf("expected response to contain bookmark %s: %s", bookmark.ID, listW.Body.String())
	}
	if strings.Contains(listW.Body.String(), otherBookmark.ID.String()) {
		t.Fatalf("expected response to exclude other bookmark %s: %s", otherBookmark.ID, listW.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/podcast/show-bookmarks/"+bookmark.ID.String(), nil)
	deleteReq.Header.Set("Authorization", podcastAuthHeader(t, user))
	deleteW := httptest.NewRecorder()
	r.ServeHTTP(deleteW, deleteReq)
	if deleteW.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteW.Code, deleteW.Body.String())
	}

	var count int64
	if err := db.Model(&model.ChannelBookmark{}).Where("id = ?", bookmark.ID).Count(&count).Error; err != nil {
		t.Fatalf("count remaining show bookmark: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected show bookmark to be deleted, got count=%d", count)
	}
}

func TestPodcastShowBookmarksExcludeVideoChannelBookmarks(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	videoChannel := model.Channel{Name: "Video Channel", Slug: "video-channel"}
	if err := db.Create(&videoChannel).Error; err != nil {
		t.Fatalf("create video channel: %v", err)
	}

	showBookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: channel.ID, Kind: "podcast_show"}
	videoBookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: videoChannel.ID, Kind: "video_channel"}
	if err := db.Create(&showBookmark).Error; err != nil {
		t.Fatalf("create show bookmark: %v", err)
	}
	if err := db.Create(&videoBookmark).Error; err != nil {
		t.Fatalf("create video bookmark: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/podcast/show-bookmarks", nil)
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), showBookmark.ID.String()) {
		t.Fatalf("expected response to contain show bookmark %s: %s", showBookmark.ID, w.Body.String())
	}
	if strings.Contains(w.Body.String(), videoBookmark.ID.String()) {
		t.Fatalf("expected response to exclude video bookmark %s: %s", videoBookmark.ID, w.Body.String())
	}
}

func TestCreatePodcastEpisodeAcceptsOwnedGlobalChannel(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	r, db, user, channel := newPodcastHandlerTestDB(t)

	owner := model.User{Username: "podcast-owner", Email: "podcast-owner@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Model(&channel).Update("user_id", owner.UUID).Error; err != nil {
		t.Fatalf("assign channel owner: %v", err)
	}
	globalChannel := model.Channel{Name: "Global Channel", Slug: "global-channel-" + uuid.NewString()[:8], UserID: &user.UUID}
	if err := db.Create(&globalChannel).Error; err != nil {
		t.Fatalf("create global channel: %v", err)
	}
	podcastCollection := model.Collection{ChannelID: globalChannel.ID, ContentType: "podcast", Name: "Episodes"}
	if err := db.Create(&podcastCollection).Error; err != nil {
		t.Fatalf("create podcast collection: %v", err)
	}

	cases := []struct {
		name      string
		channelID string
		want      int
	}{
		{name: "owned global channel succeeds", channelID: globalChannel.ID.String(), want: http.StatusCreated},
		{name: "other user's channel is forbidden", channelID: channel.ID.String(), want: http.StatusForbidden},
		{name: "missing channel is not found", channelID: uuid.NewString(), want: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"channel_id":"` + tc.channelID + `","title":"Episode","audio_url":"https://cdn.example.com/episode.mp3","status":"published","collection_ids":["` + podcastCollection.ID.String() + `"]}`)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/episodes", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", podcastAuthHeader(t, user))

			r.ServeHTTP(w, req)

			if w.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

func TestCreatePodcastEpisodePersistsVisibility(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	collection := model.Collection{ChannelID: channel.ID, ContentType: "podcast", Name: "Private episodes"}
	if err := db.Create(&collection).Error; err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"channel_id":"` + channel.ID.String() + `","title":"Private","audio_url":"episode.mp3","status":"published","visibility":"private","collection_ids":["` + collection.ID.String() + `"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/podcast/episodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var post model.Post
	if err := db.Where("title = ?", "Private").First(&post).Error; err != nil {
		t.Fatal(err)
	}
	if post.Visibility != "private" {
		t.Fatalf("expected private visibility, got %q", post.Visibility)
	}
}

func TestUpdatePodcastEpisodeReturnsInternalServerErrorAndRollsBackWhenEpisodeUpdateFails(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	episode := createPodcastEpisodeForPostStatus(t, db, user, channel, "draft")

	callbackName := "podcast_episode_update_error_" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "podcast_episodes" {
			tx.AddError(errors.New("injected episode update error"))
		}
	}); err != nil {
		t.Fatalf("register update error callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(callbackName)
	})

	body := []byte(`{"title":"updated before failure","duration_sec":120}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/podcast/episodes/"+episode.ID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var post model.Post
	if err := db.First(&post, "id = ?", episode.PostID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if post.Title != "draft episode" {
		t.Errorf("expected post update to roll back, got title %q", post.Title)
	}
}

func TestUpdatePodcastEpisodeKeepsBadCollectionAsBadRequestAndRollsBack(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	episode := createPodcastEpisodeForPostStatus(t, db, user, channel, "draft")

	body := []byte(`{"title":"updated before validation failure","collection_ids":["` + uuid.NewString() + `"]}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/podcast/episodes/"+episode.ID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var post model.Post
	if err := db.First(&post, "id = ?", episode.PostID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if post.Title != "draft episode" {
		t.Errorf("expected post update to roll back, got title %q", post.Title)
	}
}

func TestUpdatePodcastEpisodePublishRequiresCollection(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	episode := createPodcastEpisodeForPostStatus(t, db, user, channel, "draft")

	request := httptest.NewRequest(http.MethodPut, "/api/v1/podcast/episodes/"+episode.ID.String(), bytes.NewBufferString(`{"status":"published"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", podcastAuthHeader(t, user))
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected collectionless publish 400, got %d: %s", response.Code, response.Body.String())
	}
	var post model.Post
	if err := db.First(&post, "id = ?", episode.PostID).Error; err != nil {
		t.Fatal(err)
	}
	if post.Status != "draft" {
		t.Fatalf("expected status to remain draft, got %q", post.Status)
	}
}

func TestUpdatePodcastEpisodeReturnsInternalServerErrorWhenReloadFails(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)
	episode := createPodcastEpisodeForPostStatus(t, db, user, channel, "draft")

	episodeQueries := 0
	callbackName := "podcast_episode_reload_error_" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != "podcast_episodes" {
			return
		}
		episodeQueries++
		if episodeQueries == 2 {
			tx.AddError(errors.New("injected episode reload error"))
		}
	}); err != nil {
		t.Fatalf("register query error callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})

	body := []byte(`{"title":"updated title"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/podcast/episodes/"+episode.ID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", podcastAuthHeader(t, user))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d: %s", w.Code, w.Body.String())
	}

	var post model.Post
	if err := db.First(&post, "id = ?", episode.PostID).Error; err != nil {
		t.Fatalf("reload post: %v", err)
	}
	if post.Title != "draft episode" {
		t.Errorf("expected post update to roll back, got title %q", post.Title)
	}
}

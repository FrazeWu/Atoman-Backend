package handlers

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"atoman/internal/model"
	"atoman/internal/testdb"
)

func newVideoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testdb.Open(t)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.UserDefaultChannel{},
		&model.Video{},
		&model.VideoBookmark{},
		&model.ChannelBookmark{},
		&model.VideoProcessingJob{},
		&model.VideoTag{},
		&model.VideoCollection{},
		&model.VideoTagRelation{},
	)
	return db
}

func seedVideoUser(t *testing.T, db *gorm.DB) model.User {
	t.Helper()
	u := model.User{Username: "vuser_" + uuid.NewString()[:8], Email: uuid.NewString() + "@test.com", Password: "x", IsActive: true}
	require.NoError(t, db.Create(&u).Error)
	return u
}

func seedVideo(t *testing.T, db *gorm.DB, userID uuid.UUID) model.Video {
	t.Helper()
	v := model.Video{
		UserID:      userID,
		Title:       "test video",
		StorageType: "local",
		VideoURL:    "https://example.com/test.mp4",
		Status:      "published",
		Visibility:  "public",
	}
	require.NoError(t, db.Create(&v).Error)
	return v
}

func seedVideoWithState(t *testing.T, db *gorm.DB, userID uuid.UUID, status string, visibility string) model.Video {
	t.Helper()
	v := model.Video{
		UserID:      userID,
		Title:       "test video",
		StorageType: "local",
		VideoURL:    "https://example.com/test.mp4",
		Status:      status,
		Visibility:  visibility,
	}
	require.NoError(t, db.Create(&v).Error)
	return v
}

func withVideoAuth(userID uuid.UUID, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", userID)
		h(c)
	}
}

func seedVideoChannel(t *testing.T, db *gorm.DB, userID uuid.UUID, name string) model.Channel {
	t.Helper()
	channel := model.Channel{
		UserID:      &userID,
		Name:        name,
		Slug:        strings.ToLower(strings.ReplaceAll(name, " ", "-")) + "-" + uuid.NewString()[:8],
		ContentType: "video",
	}
	require.NoError(t, db.Create(&channel).Error)
	return channel
}

func seedVideoCollection(t *testing.T, db *gorm.DB, channelID uuid.UUID, name string) model.Collection {
	t.Helper()
	collection := model.Collection{
		ChannelID: channelID,
		Name:      name,
	}
	require.NoError(t, db.Create(&collection).Error)
	return collection
}

func TestCreateVideoBookmarkIsIdempotentWithRepeatedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)

	r := gin.New()
	r.POST("/api/v1/videos/bookmarks", withVideoAuth(user.UUID, CreateVideoBookmark(db)))

	body, err := json.Marshal(map[string]any{"video_id": video.ID})
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/bookmarks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
	}

	var count int64
	require.NoError(t, db.Model(&model.VideoBookmark{}).Where("user_id = ? AND video_id = ?", user.UUID, video.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestListAndDeleteVideoBookmarks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	otherUser := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)
	otherVideo := seedVideo(t, db, otherUser.UUID)
	bookmark := model.VideoBookmark{UserID: user.UUID, VideoID: video.ID}
	otherBookmark := model.VideoBookmark{UserID: otherUser.UUID, VideoID: otherVideo.ID}
	require.NoError(t, db.Create(&bookmark).Error)
	require.NoError(t, db.Create(&otherBookmark).Error)

	r := gin.New()
	r.GET("/api/v1/videos/bookmarks", withVideoAuth(user.UUID, GetVideoBookmarks(db)))
	r.DELETE("/api/v1/videos/bookmarks/:id", withVideoAuth(user.UUID, DeleteVideoBookmark(db)))

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/videos/bookmarks", nil)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	require.Equal(t, http.StatusOK, listW.Code, "body=%s", listW.Body.String())
	require.Contains(t, listW.Body.String(), bookmark.ID.String())
	require.NotContains(t, listW.Body.String(), otherBookmark.ID.String())

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/videos/bookmarks/"+bookmark.ID.String(), nil)
	deleteW := httptest.NewRecorder()
	r.ServeHTTP(deleteW, deleteReq)
	require.Equal(t, http.StatusOK, deleteW.Code, "body=%s", deleteW.Body.String())

	var count int64
	require.NoError(t, db.Model(&model.VideoBookmark{}).Where("id = ?", bookmark.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestVideoBookmarksSupportPopularSort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	hotVideo := seedVideo(t, db, user.UUID)
	coldVideo := seedVideo(t, db, user.UUID)
	require.NoError(t, db.Model(&hotVideo).Update("view_count", 100).Error)
	require.NoError(t, db.Model(&coldVideo).Update("view_count", 10).Error)
	require.NoError(t, db.Create(&model.VideoBookmark{UserID: user.UUID, VideoID: coldVideo.ID}).Error)
	require.NoError(t, db.Create(&model.VideoBookmark{UserID: user.UUID, VideoID: hotVideo.ID}).Error)

	r := gin.New()
	r.GET("/api/v1/videos/bookmarks", withVideoAuth(user.UUID, GetVideoBookmarks(db)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/bookmarks?sort=popular", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

	var resp struct {
		Data []struct {
			VideoID string `json:"video_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 2)
	require.Equal(t, hotVideo.ID.String(), resp.Data[0].VideoID)
}

func TestCreateChannelBookmarkIsIdempotentWithRepeatedRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	channel := seedVideoChannel(t, db, user.UUID, "Bookmarks Channel")

	r := gin.New()
	r.POST("/api/v1/videos/channel-bookmarks", withVideoAuth(user.UUID, CreateChannelBookmark(db)))

	body, err := json.Marshal(map[string]any{"channel_id": channel.ID})
	require.NoError(t, err)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/channel-bookmarks", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusCreated, w.Code, "body=%s", w.Body.String())
	}

	var count int64
	require.NoError(t, db.Model(&model.ChannelBookmark{}).Where("user_id = ? AND channel_id = ?", user.UUID, channel.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestListAndDeleteChannelBookmarks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	otherUser := seedVideoUser(t, db)
	channel := seedVideoChannel(t, db, user.UUID, "Bookmarked Channel")
	otherChannel := seedVideoChannel(t, db, otherUser.UUID, "Other Channel")
	bookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: channel.ID, Kind: "video_channel"}
	otherBookmark := model.ChannelBookmark{UserID: otherUser.UUID, ChannelID: otherChannel.ID}
	require.NoError(t, db.Create(&bookmark).Error)
	require.NoError(t, db.Create(&otherBookmark).Error)

	r := gin.New()
	r.GET("/api/v1/videos/channel-bookmarks", withVideoAuth(user.UUID, GetChannelBookmarks(db)))
	r.DELETE("/api/v1/videos/channel-bookmarks/:id", withVideoAuth(user.UUID, DeleteChannelBookmark(db)))

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/videos/channel-bookmarks", nil)
	listW := httptest.NewRecorder()
	r.ServeHTTP(listW, listReq)
	require.Equal(t, http.StatusOK, listW.Code, "body=%s", listW.Body.String())
	require.Contains(t, listW.Body.String(), bookmark.ID.String())
	require.NotContains(t, listW.Body.String(), otherBookmark.ID.String())

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/videos/channel-bookmarks/"+bookmark.ID.String(), nil)
	deleteW := httptest.NewRecorder()
	r.ServeHTTP(deleteW, deleteReq)
	require.Equal(t, http.StatusOK, deleteW.Code, "body=%s", deleteW.Body.String())

	var count int64
	require.NoError(t, db.Model(&model.ChannelBookmark{}).Where("id = ?", bookmark.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestVideoChannelBookmarksExcludePodcastShowBookmarks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	videoChannel := seedVideoChannel(t, db, user.UUID, "Video Channel")
	podcastShow := seedVideoChannel(t, db, user.UUID, "Podcast Show")

	videoBookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: videoChannel.ID, Kind: "video_channel"}
	podcastBookmark := model.ChannelBookmark{UserID: user.UUID, ChannelID: podcastShow.ID, Kind: "podcast_show"}
	require.NoError(t, db.Create(&videoBookmark).Error)
	require.NoError(t, db.Create(&podcastBookmark).Error)

	r := gin.New()
	r.GET("/api/v1/videos/channel-bookmarks", withVideoAuth(user.UUID, GetChannelBookmarks(db)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/channel-bookmarks", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())
	require.Contains(t, w.Body.String(), videoBookmark.ID.String())
	require.NotContains(t, w.Body.String(), podcastBookmark.ID.String())
}

func TestSetupVideoRoutesMountsRecommendationItemsEndpoint(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	video := model.Video{
		UserID:       user.UUID,
		Title:        "推荐视频",
		Description:  "这是一个适合推荐的视频。",
		StorageType:  "local",
		VideoURL:     "https://example.com/recommend.mp4",
		ThumbnailURL: "https://example.com/recommend.jpg",
		Status:       "published",
		Visibility:   "public",
		ViewCount:    120,
	}
	require.NoError(t, db.Create(&video).Error)

	r := gin.New()
	SetupVideoRoutes(r, db, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/recommend/items?mode=hot&page=1&page_size=20", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code == http.StatusNotFound {
		t.Fatalf("expected recommendation route to be mounted, got 404: %s", w.Body.String())
	}
	require.Equal(t, http.StatusOK, w.Code, "body=%s", w.Body.String())

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
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	require.NotEmpty(t, payload.Data, "body=%s", w.Body.String())

	first := payload.Data[0]
	if first.ID == "" || first.Title == "" || first.TargetPath == "" || first.ScoreLabel == "" || first.ContentType != "video" {
		t.Fatalf("expected recommendation dto fields, got %#v", first)
	}
}

func videoMultipartBody(t *testing.T, field, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="` + field + `"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	return body, writer.FormDataContentType()
}

func TestUploadVideoCoverRejectsForgedPNGContentType(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "local")
	gin.SetMode(gin.TestMode)
	userID := uuid.New()

	r := gin.New()
	r.POST("/api/v1/videos/upload-cover", withVideoAuth(userID, UploadVideoCover(nil)))

	body, contentType := videoMultipartBody(t, "cover", "cover.png", "image/png", []byte("not a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/upload-cover", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestUploadVideoFileRejectsForgedMP4ContentType(t *testing.T) {
	t.Setenv("STORAGE_TYPE", "local")
	gin.SetMode(gin.TestMode)
	userID := uuid.New()

	r := gin.New()
	r.POST("/api/v1/videos/upload-video", withVideoAuth(userID, UploadVideoFile(nil)))

	body, contentType := videoMultipartBody(t, "video", "clip.mp4", "video/mp4", []byte("not an mp4"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/upload-video", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateVideoRollsBackWhenCollectionAssignmentFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	r := gin.New()
	r.POST("/api/v1/videos", withVideoAuth(user.UUID, CreateVideo(db)))

	body := strings.NewReader(`{
		"title":"rollback video",
		"storage_type":"local",
		"video_url":"https://example.com/test.mp4",
		"tags":["rollback-tag"],
		"collection_ids":["` + uuid.NewString() + `"]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var videoCount int64
	require.NoError(t, db.Model(&model.Video{}).Where("title = ?", "rollback video").Count(&videoCount).Error)
	require.Zero(t, videoCount)

	var jobCount int64
	require.NoError(t, db.Model(&model.VideoProcessingJob{}).Count(&jobCount).Error)
	require.Zero(t, jobCount)

	var tagCount int64
	require.NoError(t, db.Model(&model.VideoTag{}).Where("name = ?", "rollback-tag").Count(&tagCount).Error)
	require.Zero(t, tagCount)
}

func TestUpdateVideoUsesNewChannelWhenAssigningCollections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	oldChannel := seedVideoChannel(t, db, user.UUID, "Old Channel")
	newChannel := seedVideoChannel(t, db, user.UUID, "New Channel")
	newCollection := seedVideoCollection(t, db, newChannel.ID, "New Collection")
	video := seedVideo(t, db, user.UUID)
	require.NoError(t, db.Model(&video).Update("channel_id", oldChannel.ID).Error)

	r := gin.New()
	r.PUT("/api/v1/videos/:id", withVideoAuth(user.UUID, UpdateVideo(db)))

	body := strings.NewReader(`{
		"channel_id":"` + newChannel.ID.String() + `",
		"collection_ids":["` + newCollection.ID.String() + `"]
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/videos/"+video.ID.String(), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var updated model.Video
	require.NoError(t, db.Preload("Collections").First(&updated, "id = ?", video.ID).Error)
	require.NotNil(t, updated.ChannelID)
	require.Equal(t, newChannel.ID, *updated.ChannelID)
	require.Len(t, updated.Collections, 1)
	require.Equal(t, newCollection.ID, updated.Collections[0].ID)
}

func TestUpdateVideoRollsBackWhenCollectionAssignmentFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	channel := seedVideoChannel(t, db, user.UUID, "Rollback Channel")
	video := seedVideo(t, db, user.UUID)
	require.NoError(t, db.Model(&video).Update("channel_id", channel.ID).Error)

	r := gin.New()
	r.PUT("/api/v1/videos/:id", withVideoAuth(user.UUID, UpdateVideo(db)))

	body := strings.NewReader(`{
		"title":"updated before failure",
		"tags":["updated-tag"],
		"collection_ids":["` + uuid.NewString() + `"]
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/videos/"+video.ID.String(), body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var updated model.Video
	require.NoError(t, db.First(&updated, "id = ?", video.ID).Error)
	require.Equal(t, "test video", updated.Title)

	var tagCount int64
	require.NoError(t, db.Model(&model.VideoTag{}).Where("name = ?", "updated-tag").Count(&tagCount).Error)
	require.Zero(t, tagCount)
}

func TestCreateVideoRequiresOwnedVideoChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	otherUser := seedVideoUser(t, db)
	ownedVideoChannel := seedVideoChannel(t, db, user.UUID, "Owned Video Channel")
	otherUsersVideoChannel := seedVideoChannel(t, db, otherUser.UUID, "Other Users Video Channel")
	mismatchedChannel := model.Channel{
		UserID:      &user.UUID,
		Name:        "Users Blog Channel",
		Slug:        "users-blog-channel-" + uuid.NewString()[:8],
		ContentType: "blog",
	}
	require.NoError(t, db.Create(&mismatchedChannel).Error)

	r := gin.New()
	r.POST("/api/v1/videos", withVideoAuth(user.UUID, CreateVideo(db)))

	cases := []struct {
		name      string
		channelID string
		want      int
	}{
		{name: "owned video channel succeeds", channelID: ownedVideoChannel.ID.String(), want: http.StatusCreated},
		{name: "other users channel is forbidden", channelID: otherUsersVideoChannel.ID.String(), want: http.StatusForbidden},
		{name: "content type mismatch is rejected", channelID: mismatchedChannel.ID.String(), want: http.StatusBadRequest},
		{name: "missing channel is not found", channelID: uuid.NewString(), want: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(`{
				"channel_id":"` + tc.channelID + `",
				"title":"channel-bound video",
				"storage_type":"local",
				"video_url":"https://example.com/test.mp4"
			}`)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/videos", body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, tc.want, w.Code, "body=%s", w.Body.String())
		})
	}
}

func TestUpdateVideoRejectsChannelOwnershipAndTypeMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	otherUser := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)
	currentChannel := seedVideoChannel(t, db, user.UUID, "Current Video Channel")
	otherUsersVideoChannel := seedVideoChannel(t, db, otherUser.UUID, "Other Update Video Channel")
	mismatchedChannel := model.Channel{
		UserID:      &user.UUID,
		Name:        "User Podcast Channel",
		Slug:        "user-podcast-channel-" + uuid.NewString()[:8],
		ContentType: "podcast",
	}
	require.NoError(t, db.Create(&mismatchedChannel).Error)
	require.NoError(t, db.Model(&video).Update("channel_id", currentChannel.ID).Error)

	r := gin.New()
	r.PUT("/api/v1/videos/:id", withVideoAuth(user.UUID, UpdateVideo(db)))

	cases := []struct {
		name      string
		channelID string
		want      int
	}{
		{name: "other users channel is forbidden", channelID: otherUsersVideoChannel.ID.String(), want: http.StatusForbidden},
		{name: "content type mismatch is rejected", channelID: mismatchedChannel.ID.String(), want: http.StatusBadRequest},
		{name: "missing channel is not found", channelID: uuid.NewString(), want: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(`{"channel_id":"` + tc.channelID + `"}`)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/videos/"+video.ID.String(), body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, tc.want, w.Code, "body=%s", w.Body.String())
		})
	}
}

func TestGetVideoReturnsPublishedPublicVideo(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideoWithState(t, db, user.UUID, "published", "public")

	r := gin.New()
	r.GET("/api/v1/videos/:id", GetVideo(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), video.ID.String())
}

func TestGetVideoRejectsAnonymousAccessToNonPublicVideos(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)

	cases := []struct {
		name       string
		status     string
		visibility string
	}{
		{name: "draft", status: "draft", visibility: "public"},
		{name: "private", status: "published", visibility: "private"},
		{name: "followers", status: "published", visibility: "followers"},
		{name: "unpublished_private", status: "draft", visibility: "private"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video := seedVideoWithState(t, db, user.UUID, tc.status, tc.visibility)

			r := gin.New()
			r.GET("/api/v1/videos/:id", GetVideo(db))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String(), nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusNotFound, w.Code)
			require.NotContains(t, w.Body.String(), video.ID.String())
			require.NotContains(t, w.Body.String(), video.VideoURL)
		})
	}
}

func TestLegacyVideoCommentRoutesAreRemoved(t *testing.T) {
	db := newVideoTestDB(t)
	r := gin.New()
	SetupVideoRoutes(r, db, nil)
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/videos/" + uuid.NewString() + "/comments"},
		{http.MethodPost, "/api/v1/videos/" + uuid.NewString() + "/comments"},
		{http.MethodDelete, "/api/v1/videos/comments/" + uuid.NewString()},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(request.method, request.path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected legacy route %s to return 404, got %d", request.path, w.Code)
		}
	}
}

package handlers

import (
	"bytes"
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
		&model.Video{},
		&model.VideoProcessingJob{},
		&model.VideoTag{},
		&model.VideoCollection{},
		&model.VideoTagRelation{},
		&model.Comment{},
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
		UserID: &userID,
		Name:   name,
		Slug:   strings.ToLower(strings.ReplaceAll(name, " ", "-")) + "-" + uuid.NewString()[:8],
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

func TestGetVideoCommentsReturnsMixedComments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)

	// 普通评论
	c1 := model.Comment{TargetType: "video", TargetID: video.ID, UserID: model.NewNullableUserUUID(user.UUID), Content: "great video", Status: "visible"}
	// 时间点评论
	ts := 92
	c2 := model.Comment{TargetType: "video", TargetID: video.ID, UserID: model.NewNullableUserUUID(user.UUID), Content: "this part!", TimestampSec: &ts, Status: "visible"}
	require.NoError(t, db.Create(&c1).Error)
	require.NoError(t, db.Create(&c2).Error)

	r := gin.New()
	r.GET("/api/v1/videos/:id/comments", GetVideoComments(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String()+"/comments", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "great video")
	require.Contains(t, w.Body.String(), "this part!")
}

func TestGetVideoCommentsRejectsNonPublicVideos(t *testing.T) {
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video := seedVideoWithState(t, db, user.UUID, tc.status, tc.visibility)
			comment := model.Comment{TargetType: "video", TargetID: video.ID, UserID: model.NewNullableUserUUID(user.UUID), Content: "hidden comment", Status: "visible"}
			require.NoError(t, db.Create(&comment).Error)

			r := gin.New()
			r.GET("/api/v1/videos/:id/comments", GetVideoComments(db))

			req := httptest.NewRequest(http.MethodGet, "/api/v1/videos/"+video.ID.String()+"/comments", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusNotFound, w.Code)
			require.NotContains(t, w.Body.String(), "hidden comment")
		})
	}
}

func TestCreateVideoCommentWithTimestamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)

	r := gin.New()
	r.POST("/api/v1/videos/:id/comments", withVideoAuth(user.UUID, CreateVideoComment(db)))

	body := strings.NewReader(`{"content":"这一段很强","timestamp_sec":92}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+video.ID.String()+"/comments", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code)

	var comment model.Comment
	require.NoError(t, db.First(&comment).Error)
	require.Equal(t, "video", comment.TargetType)
	require.Equal(t, video.ID, comment.TargetID)
	require.NotNil(t, comment.TimestampSec)
	require.Equal(t, 92, *comment.TimestampSec)
}

func TestCreateVideoCommentRejectsNegativeTimestamp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	user := seedVideoUser(t, db)
	video := seedVideo(t, db, user.UUID)

	r := gin.New()
	r.POST("/api/v1/videos/:id/comments", withVideoAuth(user.UUID, CreateVideoComment(db)))

	body := strings.NewReader(`{"content":"bad timestamp","timestamp_sec":-1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+video.ID.String()+"/comments", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreateVideoCommentRejectsNonPublicVideos(t *testing.T) {
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			video := seedVideoWithState(t, db, user.UUID, tc.status, tc.visibility)

			r := gin.New()
			r.POST("/api/v1/videos/:id/comments", withVideoAuth(user.UUID, CreateVideoComment(db)))

			body := strings.NewReader(`{"content":"should not write"}`)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/videos/"+video.ID.String()+"/comments", body)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusNotFound, w.Code)

			var count int64
			require.NoError(t, db.Model(&model.Comment{}).Where("target_type = ? AND target_id = ?", "video", video.ID).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

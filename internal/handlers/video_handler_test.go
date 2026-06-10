package handlers

import (
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
	testdb.Migrate(t, db, &model.User{}, &model.Video{}, &model.Comment{})
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

func withVideoAuth(userID uuid.UUID, h gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("userID", userID)
		h(c)
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

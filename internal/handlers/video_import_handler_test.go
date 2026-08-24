package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"
)

func fakeVideoImportS3(t *testing.T) *s3.S3 {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = fmt.Fprint(w, `<InitiateMultipartUploadResult><Bucket>atoman-test</Bucket><Key>video.mp4</Key><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPost && r.URL.Query().Get("uploadId") == "upload-1":
			_, _ = fmt.Fprint(w, `<CompleteMultipartUploadResult><Location>https://cdn.example.com/video.mp4</Location><Bucket>atoman-test</Bucket><Key>video.mp4</Key><ETag>etag</ETag></CompleteMultipartUploadResult>`)
		case r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "video/mp4")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'})
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected S3 request: %s %s", r.Method, r.URL.String())
		}
	}))
	t.Cleanup(server.Close)
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String("us-test-1"), Endpoint: aws.String(server.URL),
		Credentials: credentials.NewStaticCredentials("access", "secret", ""), S3ForcePathStyle: aws.Bool(true),
	})
	require.NoError(t, err)
	return s3.New(sess)
}

func videoImportTestRouter(db *gorm.DB, client *s3.S3, userID uuid.UUID) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("userID", userID)
		c.Next()
	})
	registerVideoImportRoutes(router.Group("/api/v1/videos"), db, client)
	return router
}

func performVideoImportRequest(t *testing.T, router http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}

func TestVideoImportCompletesUploadAndPublishesOnce(t *testing.T) {
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com")
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	owner := seedVideoUser(t, db)
	channel := seedVideoChannel(t, db, owner.UUID, "Import Channel")
	collection := model.Collection{ChannelID: channel.ID, Name: "Imports", ContentType: "video"}
	require.NoError(t, db.Create(&collection).Error)
	router := videoImportTestRouter(db, fakeVideoImportS3(t), owner.UUID)

	created := performVideoImportRequest(t, router, http.MethodPost, "/api/v1/videos/imports", CreateVideoImportInput{
		ChannelID: &channel.ID, FileName: "clip.mp4", FileSize: 5, ContentType: "video/mp4",
	})
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	var task VideoImportDTO
	require.NoError(t, json.Unmarshal(created.Body.Bytes(), &task))
	require.Equal(t, videoImportUploading, task.Status)

	submitted := performVideoImportRequest(t, router, http.MethodPost, "/api/v1/videos/imports/"+task.ID.String()+"/submit", SubmitVideoImportInput{
		Payload: VideoImportPayload{
			ChannelID: &channel.ID, Title: "Imported video", Description: "Ready after upload",
			DurationSec: 214, Visibility: "public", CollectionIDs: []uuid.UUID{collection.ID}, Tags: []string{"imported"},
		},
		PublishMode: "published",
	})
	require.Equal(t, http.StatusOK, submitted.Code, submitted.Body.String())

	part := performVideoImportRequest(t, router, http.MethodPost, "/api/v1/videos/imports/"+task.ID.String()+"/parts/1/complete", CompleteVideoImportPartInput{ETag: `"etag-1"`, Size: 5})
	require.Equal(t, http.StatusOK, part.Code, part.Body.String())
	require.Contains(t, part.Body.String(), `"completed_parts":[1]`)

	completed := performVideoImportRequest(t, router, http.MethodPost, "/api/v1/videos/imports/"+task.ID.String()+"/complete", nil)
	require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
	require.NoError(t, json.Unmarshal(completed.Body.Bytes(), &task))
	require.Equal(t, videoImportPublished, task.Status)
	require.NotNil(t, task.TargetVideoID)

	video, err := contentmodule.LoadVideo(db, contentmodule.VideoQuery(db).Where("videos.video_id = ?", *task.TargetVideoID))
	require.NoError(t, err)
	require.Equal(t, "Imported video", video.Title)
	require.Equal(t, 214, video.DurationSec)
	require.Equal(t, "published", video.Status)
	require.Contains(t, video.VideoURL, "/video/imports/")
	require.Len(t, video.Collections, 1)
	require.Len(t, video.Tags, 1)

	again := performVideoImportRequest(t, router, http.MethodPost, "/api/v1/videos/imports/"+task.ID.String()+"/complete", nil)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	var count int64
	require.NoError(t, db.Model(&model.ContentVideoExtension{}).Where("video_id = ?", *task.TargetVideoID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestVideoImportIsScopedToOwner(t *testing.T) {
	t.Setenv("S3_BUCKET", "atoman-test")
	gin.SetMode(gin.TestMode)
	db := newVideoTestDB(t)
	owner := seedVideoUser(t, db)
	other := seedVideoUser(t, db)
	session := model.VideoImportSession{
		UserID: owner.UUID, Status: videoImportUploading, FileName: "clip.mp4", FileSize: 5,
		ContentType: "video/mp4", ObjectKey: "video/imports/source.mp4", UploadID: "upload-1",
		PartSize: videoImportPartSize, CompletedPartsJSON: "[]", PayloadJSON: "{}",
	}
	require.NoError(t, db.Create(&session).Error)

	response := performVideoImportRequest(t, videoImportTestRouter(db, nil, other.UUID), http.MethodGet, "/api/v1/videos/imports/"+session.ID.String(), nil)
	require.Equal(t, http.StatusNotFound, response.Code)
}

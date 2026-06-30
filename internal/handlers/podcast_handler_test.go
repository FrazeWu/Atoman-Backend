package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func newPodcastHandlerTestDB(t *testing.T) (*gin.Engine, *gorm.DB, model.User, model.Channel) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	testdb.Migrate(t, db,
		&model.User{},
		&model.Channel{},
		&model.Collection{},
		&model.Post{},
		&model.PostCollection{},
		&model.PodcastEpisode{},
	)

	user := model.User{Username: "podcast-user", Email: "podcast-user@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	channel := model.Channel{Name: "Podcast", Slug: "podcast"}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	r := gin.New()
	SetupPodcastRoutes(r, db, nil)
	return r, db, user, channel
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

func TestGetPodcastEpisodePublicDetailRequiresPublishedLivePost(t *testing.T) {
	r, db, user, channel := newPodcastHandlerTestDB(t)

	published := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
	draft := createPodcastEpisodeForPostStatus(t, db, user, channel, "draft")
	deletedPostEpisode := createPodcastEpisodeForPostStatus(t, db, user, channel, "published")
	if err := db.Delete(&model.Post{}, "id = ?", deletedPostEpisode.PostID).Error; err != nil {
		t.Fatalf("soft delete post: %v", err)
	}

	cases := []struct {
		name string
		id   string
		want int
	}{
		{name: "published post is visible", id: published.ID.String(), want: http.StatusOK},
		{name: "draft post is hidden", id: draft.ID.String(), want: http.StatusNotFound},
		{name: "soft deleted post is hidden", id: deletedPostEpisode.ID.String(), want: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/podcast/episodes/"+tc.id, nil)

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

func TestCreatePodcastEpisodeRequiresOwnedChannel(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	r, db, user, channel := newPodcastHandlerTestDB(t)

	owner := model.User{Username: "podcast-owner", Email: "podcast-owner@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Model(&channel).Update("user_id", owner.UUID).Error; err != nil {
		t.Fatalf("assign channel owner: %v", err)
	}

	cases := []struct {
		name      string
		channelID string
		want      int
	}{
		{name: "other user's channel is forbidden", channelID: channel.ID.String(), want: http.StatusForbidden},
		{name: "missing channel is not found", channelID: uuid.NewString(), want: http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"channel_id":"` + tc.channelID + `","title":"Episode","audio_url":"https://cdn.example.com/episode.mp3"}`)
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

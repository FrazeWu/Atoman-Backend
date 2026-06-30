package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func multipartLegacyMusicCoverBody(t *testing.T, fields map[string]string, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="cover"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	if err != nil {
		t.Fatalf("create cover part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write cover part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func legacyMusicUploadUser(t *testing.T, db *gorm.DB) model.User {
	t.Helper()

	user := model.User{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hash",
		Role:     "user",
		IsActive: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func withLegacyMusicUser(user model.User) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("user_id", user.UUID)
		c.Set("role", user.Role)
		c.Next()
	}
}

func TestCreateSongRejectsSpoofedCoverImageContentType(t *testing.T) {
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")
	t.Setenv("STORAGE_TYPE", "s3")
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Artist{}, &model.Album{}, &model.Song{})
	user := legacyMusicUploadUser(t, db)
	var s3Path string
	var s3ContentType string

	r := gin.New()
	r.Use(withLegacyMusicUser(user))
	r.POST("/api/v1/songs", CreateSongHandler(db, fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType)))

	body, contentType := multipartLegacyMusicCoverBody(t, map[string]string{
		"title":     "Spoofed Song",
		"artist":    "Alice",
		"album":     "Spoofed Album",
		"audio_url": "/uploads/song.mp3",
	}, "cover.png", "image/png", []byte("not really a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/songs", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for spoofed song cover content, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("File content does not match declared image type")) {
		t.Fatalf("expected content mismatch error, got: %s", w.Body.String())
	}
	if s3Path != "" || s3ContentType != "" {
		t.Fatalf("expected spoofed song cover to be rejected before S3, got path=%q contentType=%q", s3Path, s3ContentType)
	}
}

func TestCreateAlbumRejectsSpoofedCoverImageContentType(t *testing.T) {
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")
	t.Setenv("STORAGE_TYPE", "s3")
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Artist{}, &model.Album{})
	user := legacyMusicUploadUser(t, db)
	var s3Path string
	var s3ContentType string

	r := gin.New()
	r.Use(withLegacyMusicUser(user))
	r.POST("/api/v1/albums", CreateAlbumHandler(db, fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType)))

	body, contentType := multipartLegacyMusicCoverBody(t, map[string]string{
		"title":  "Spoofed Album",
		"artist": "Alice",
		"year":   "2024",
	}, "cover.png", "image/png", []byte("not really a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/albums", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for spoofed album cover content, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("File content does not match declared image type")) {
		t.Fatalf("expected content mismatch error, got: %s", w.Body.String())
	}
	if s3Path != "" || s3ContentType != "" {
		t.Fatalf("expected spoofed album cover to be rejected before S3, got path=%q contentType=%q", s3Path, s3ContentType)
	}
}

func TestCreateAlbumCorrectionRejectsSpoofedCoverImageContentType(t *testing.T) {
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")
	gin.SetMode(gin.TestMode)

	db := testdb.Open(t)
	testdb.Migrate(t, db, &model.User{}, &model.Artist{}, &model.Album{}, &model.AlbumCorrection{})
	user := legacyMusicUploadUser(t, db)
	album := model.Album{Title: "Original Album", Year: 2024}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	var s3Path string
	var s3ContentType string

	r := gin.New()
	r.Use(withLegacyMusicUser(user))
	r.POST("/api/v1/corrections/album", CreateAlbumCorrectionHandler(db, fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType)))

	body, contentType := multipartLegacyMusicCoverBody(t, map[string]string{
		"album_id": album.ID.String(),
		"reason":   "cover is wrong",
	}, "cover.png", "image/png", []byte("not really a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/corrections/album", body)
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for spoofed album correction cover content, got %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("File content does not match declared image type")) {
		t.Fatalf("expected content mismatch error, got: %s", w.Body.String())
	}
	if s3Path != "" || s3ContentType != "" {
		t.Fatalf("expected spoofed correction cover to be rejected before S3, got path=%q contentType=%q", s3Path, s3ContentType)
	}
}

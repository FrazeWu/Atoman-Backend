package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func multipartBlogImageBody(t *testing.T, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="image"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	if err != nil {
		t.Fatalf("create image part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write image part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func newBlogUploadTestUser(t *testing.T) (*gorm.DB, model.User) {
	t.Helper()
	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	testdb.Migrate(t, db, &model.User{})

	user := model.User{Username: "alice_" + uuid.NewString()[:8], Email: uuid.NewString() + "@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create upload user: %v", err)
	}
	return db, user
}

func TestUploadBlogImageRejectsSpoofedImageContentType(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")
	gin.SetMode(gin.TestMode)

	db, user := newBlogUploadTestUser(t)
	var s3Path string
	var s3ContentType string
	r := gin.New()
	SetupBlogUploadRoutes(r, db, fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType))

	body, contentType := multipartBlogImageBody(t, "spoof.png", "image/png", []byte("not really a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/upload-image", body)
	req.Header.Set("Authorization", "Bearer "+signedUploadTokenForTest(t, user))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for spoofed image content, got %d: %s", w.Code, w.Body.String())
	}
	if s3Path != "" || s3ContentType != "" {
		t.Fatalf("expected spoofed upload to be rejected before S3, got path=%q contentType=%q", s3Path, s3ContentType)
	}
}

func TestUploadBlogImageAllowsRealPNGHeader(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")
	gin.SetMode(gin.TestMode)

	db, user := newBlogUploadTestUser(t)
	var s3Path string
	var s3ContentType string
	r := gin.New()
	SetupBlogUploadRoutes(r, db, fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType))

	body, contentType := multipartBlogImageBody(t, "avatar.png", "image/png", validPNGBytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/blog/upload-image", body)
	req.Header.Set("Authorization", "Bearer "+signedUploadTokenForTest(t, user))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid png, got %d: %s", w.Code, w.Body.String())
	}
	if s3ContentType != "image/png" {
		t.Fatalf("expected S3 content type image/png, got %q", s3ContentType)
	}
	if s3Path == "" {
		t.Fatal("expected valid blog image to be uploaded to S3")
	}
}

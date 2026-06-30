package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/middleware"
	"atoman/internal/model"
	"atoman/internal/testdb"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func signedUploadTokenForTest(t *testing.T, user model.User) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  user.UUID.String(),
		"username": user.Username,
		"role":     user.Role,
		"exp":      time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func multipartUploadBody(t *testing.T, purpose, filename, contentType string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("purpose", purpose); err != nil {
		t.Fatalf("write purpose: %v", err)
	}
	part, err := writer.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="file"; filename="` + filename + `"`},
		"Content-Type":        {contentType},
	})
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return body, writer.FormDataContentType()
}

func validPNGBytes() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89,
	}
}

func fakeS3ClientForUploadTest(t *testing.T, capturedPath *string, capturedContentType *string) *s3.S3 {
	return fakeS3ClientForUploadTestWithACL(t, capturedPath, capturedContentType, nil)
}

func fakeS3ClientForUploadTestWithACL(t *testing.T, capturedPath *string, capturedContentType *string, capturedACL *string) *s3.S3 {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("expected S3 PUT, got %s", r.Method)
		}
		*capturedPath = r.URL.EscapedPath()
		*capturedContentType = r.Header.Get("Content-Type")
		if capturedACL != nil {
			*capturedACL = r.Header.Get("X-Amz-Acl")
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	sess, err := session.NewSession(&aws.Config{
		Region:           aws.String("us-test-1"),
		Endpoint:         aws.String(server.URL),
		Credentials:      credentials.NewStaticCredentials("access", "secret", ""),
		S3ForcePathStyle: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("new s3 session: %v", err)
	}
	return s3.New(sess)
}

func TestUploadMusicAssetStoresInS3AndPersistsMediaAsset(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	testdb.Migrate(t, db, &model.User{}, &model.MediaAsset{})
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	var s3Path string
	var s3ContentType string
	r := gin.New()
	SetupUploadRoutes(r, db, fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType))

	body, contentType := multipartUploadBody(t, "music.cover", "avatar.png", "image/png", validPNGBytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", body)
	req.Header.Set("Authorization", "Bearer "+signedUploadTokenForTest(t, user))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			URL         string `json:"url"`
			Key         string `json:"key"`
			ContentType string `json:"content_type"`
			Size        int64  `json:"size"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if strings.HasPrefix(resp.Data.URL, "/uploads/") {
		t.Fatalf("upload returned local uploads URL: %s", resp.Data.URL)
	}
	wantKeyPrefix := "music/covers/uploads/users/" + user.UUID.String() + "/"
	if !strings.HasPrefix(resp.Data.Key, wantKeyPrefix) {
		t.Fatalf("expected key prefix %q, got %q", wantKeyPrefix, resp.Data.Key)
	}
	if !strings.HasSuffix(resp.Data.Key, ".png") {
		t.Fatalf("expected key to keep png extension, got %q", resp.Data.Key)
	}
	if resp.Data.URL != "https://cdn.example.com/assets/"+resp.Data.Key {
		t.Fatalf("unexpected URL %q for key %q", resp.Data.URL, resp.Data.Key)
	}
	if resp.Data.ContentType != "image/png" || resp.Data.Size != int64(len(validPNGBytes())) {
		t.Fatalf("unexpected upload metadata: %#v", resp.Data)
	}
	if s3Path != "/atoman-test/"+resp.Data.Key {
		t.Fatalf("expected S3 path to include key, got %q", s3Path)
	}
	if s3ContentType != "image/png" {
		t.Fatalf("expected S3 content type image/png, got %q", s3ContentType)
	}

	var asset model.MediaAsset
	if err := db.First(&asset, "key = ?", resp.Data.Key).Error; err != nil {
		t.Fatalf("load media asset: %v", err)
	}
	if asset.UserID == nil || *asset.UserID != user.UUID || asset.Purpose != "music.cover" || asset.URL != resp.Data.URL {
		t.Fatalf("unexpected persisted media asset: %#v", asset)
	}
}

func TestUploadMusicCoverRejectsSpoofedImageContentType(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.example.com/assets")
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	testdb.Migrate(t, db, &model.User{}, &model.MediaAsset{})
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	var s3Path string
	var s3ContentType string
	r := gin.New()
	SetupUploadRoutes(r, db, fakeS3ClientForUploadTest(t, &s3Path, &s3ContentType))

	body, contentType := multipartUploadBody(t, "music.cover", "avatar.png", "image/png", []byte("not really a png"))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", body)
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
	var count int64
	if err := db.Model(&model.MediaAsset{}).Count(&count).Error; err != nil {
		t.Fatalf("count media assets: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no media asset for spoofed upload, got %d", count)
	}
}

func TestUploadMusicAssetRequiresS3Storage(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	gin.SetMode(gin.TestMode)
	db := testdb.Open(t)
	middleware.SetAuthDB(db)
	testdb.Migrate(t, db, &model.User{}, &model.MediaAsset{})
	user := model.User{Username: "alice", Email: "alice@example.com", Password: "hash", Role: "user", IsActive: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	r := gin.New()
	SetupUploadRoutes(r, db, nil)

	body, contentType := multipartUploadBody(t, "music.cover", "avatar.png", "image/png", validPNGBytes())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/uploads", body)
	req.Header.Set("Authorization", "Bearer "+signedUploadTokenForTest(t, user))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when S3 is unavailable, got %d: %s", w.Code, w.Body.String())
	}
	if w.Body.String() != `{"code":"storage.unavailable","error":"Storage service is unavailable"}` {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
	var count int64
	if err := db.Model(&model.MediaAsset{}).Count(&count).Error; err != nil {
		t.Fatalf("count media assets: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no local media asset fallback, got %d persisted assets", count)
	}
}

# Music Album Multipart Upload Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement resumable multipart zip upload for the music album creation flow, supporting archives up to 2GB through browser-to-R2 part uploads.

**Architecture:** Keep `music_album_import_sessions` as the workflow owner and store multipart state inside `PayloadJSON`. The Go backend creates/restores R2 multipart uploads, signs part URLs, records part ETags, completes the upload, extracts the merged zip from a temporary file, and deletes the source zip after successful extraction. The Vue frontend replaces the archive `XMLHttpRequest` whole-file upload with a small multipart uploader that validates file size, resumes same-name same-size uploads, uploads parts directly to R2, and reuses the existing import snapshot application.

**Tech Stack:** Go, Gin, GORM, AWS SDK for Go S3/R2 API, PostgreSQL/Neon, Cloudflare R2, Vue 3, TypeScript, Vite, Vitest.

---

## File Structure

- Modify `Atoman-Backend/internal/modules/music/import_types.go`: add multipart request/response DTOs and status constants.
- Create `Atoman-Backend/internal/modules/music/import_multipart_store.go`: wrap S3/R2 multipart operations behind a small interface and production implementation.
- Modify `Atoman-Backend/internal/modules/music/service.go`: add the multipart store field and initialize it from the S3 client.
- Modify `Atoman-Backend/internal/modules/music/import_service.go`: add multipart session methods and refactor zip derivation to accept a temporary zip file path.
- Modify `Atoman-Backend/internal/modules/music/import_http.go`: add HTTP handlers for multipart init, part URL, part completion, and final complete.
- Modify `Atoman-Backend/internal/modules/music/http.go`: register the new endpoints.
- Modify `Atoman-Backend/internal/modules/music/import_service_test.go`: service-level multipart state and extraction tests.
- Modify `Atoman-Backend/internal/modules/music/http_test.go`: HTTP contract tests for multipart endpoints.
- Modify `Atoman-Frontend/src/api/musicV1.ts`: add multipart DTOs, endpoints, API adapters, file validation, and `uploadMusicAlbumArchiveMultipart`.
- Modify `Atoman-Frontend/src/components/music/MusicCreationAlbumSeedStep.vue`: call the multipart uploader and display upload/extracting/retry states.
- Modify `Atoman-Frontend/tests/unit/api/musicV1.spec.ts`: API adapter and uploader behavior tests.
- Modify `Atoman-Frontend/tests/unit/components/music/MusicCreationAlbumImportStep.spec.ts`: component behavior for progress, size rejection, and snapshot application.

---

## Task 1: Backend Multipart Types And Store Boundary

**Files:**
- Modify: `Atoman-Backend/internal/modules/music/import_types.go`
- Create: `Atoman-Backend/internal/modules/music/import_multipart_store.go`
- Modify: `Atoman-Backend/internal/modules/music/service.go`

- [ ] **Step 1: Add multipart DTOs and status constants**

In `Atoman-Backend/internal/modules/music/import_types.go`, extend the status constants and add DTOs near the existing import types:

```go
type StartAlbumImportMultipartInput struct {
	FileName    string `json:"fileName"`
	FileSize    int64  `json:"fileSize"`
	ContentType string `json:"contentType"`
}

type AlbumImportMultipartPartDTO struct {
	PartNumber int    `json:"partNumber"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

type AlbumImportMultipartDTO struct {
	ImportID       string                        `json:"importId"`
	FileName       string                        `json:"fileName"`
	FileSize       int64                         `json:"fileSize"`
	ObjectKey      string                        `json:"objectKey"`
	PartSize       int64                         `json:"partSize"`
	CompletedParts []AlbumImportMultipartPartDTO `json:"completedParts"`
}

type CreateAlbumImportMultipartPartInput struct {
	PartSize int64 `json:"partSize"`
}

type AlbumImportMultipartPartUploadDTO struct {
	PartNumber int    `json:"partNumber"`
	UploadURL  string `json:"uploadUrl"`
}

type CompleteAlbumImportMultipartPartInput struct {
	ETag string `json:"etag"`
	Size int64  `json:"size"`
}
```

Update constants:

```go
const (
	AlbumImportStatusPendingUpload = "pending_upload"
	AlbumImportStatusUploading     = "uploading"
	AlbumImportStatusUploaded      = "uploaded"
	AlbumImportStatusExtracting    = "extracting"
	AlbumImportStatusReady         = "ready"
	AlbumImportStatusFailed        = "failed"
	AlbumImportStatusCommitted     = "committed"
)
```

- [ ] **Step 2: Create the multipart store interface**

Create `Atoman-Backend/internal/modules/music/import_multipart_store.go`:

```go
package music

import (
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"atoman/internal/platform/apperr"
	"net/http"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
)

type albumImportMultipartStore interface {
	CreateMultipartUpload(key string, contentType string) (string, error)
	PresignUploadPart(key string, uploadID string, partNumber int, expires time.Duration) (string, error)
	CompleteMultipartUpload(key string, uploadID string, parts []AlbumImportMultipartPartDTO) error
	AbortMultipartUpload(key string, uploadID string) error
	OpenObject(key string) (io.ReadCloser, error)
	DeleteObject(key string) error
}

type s3AlbumImportMultipartStore struct {
	client *s3.S3
	bucket string
}

func newS3AlbumImportMultipartStore(client *s3.S3) albumImportMultipartStore {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if client == nil || bucket == "" {
		return nil
	}
	return &s3AlbumImportMultipartStore{client: client, bucket: bucket}
}

func (s *s3AlbumImportMultipartStore) CreateMultipartUpload(key string, contentType string) (string, error) {
	out, err := s.client.CreateMultipartUpload(&s3.CreateMultipartUploadInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return aws.StringValue(out.UploadId), nil
}

func (s *s3AlbumImportMultipartStore) PresignUploadPart(key string, uploadID string, partNumber int, expires time.Duration) (string, error) {
	req, _ := s.client.UploadPartRequest(&s3.UploadPartInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int64(int64(partNumber)),
	})
	signedURL, err := req.Presign(expires)
	if err != nil {
		return "", err
	}
	if _, err := url.ParseRequestURI(signedURL); err != nil {
		return "", err
	}
	return signedURL, nil
}

func (s *s3AlbumImportMultipartStore) CompleteMultipartUpload(key string, uploadID string, parts []AlbumImportMultipartPartDTO) error {
	completedParts := make([]*s3.CompletedPart, 0, len(parts))
	for _, part := range parts {
		completedParts = append(completedParts, &s3.CompletedPart{
			ETag:       aws.String(part.ETag),
			PartNumber: aws.Int64(int64(part.PartNumber)),
		})
	}
	_, err := s.client.CompleteMultipartUpload(&s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
		MultipartUpload: &s3.CompletedMultipartUpload{
			Parts: completedParts,
		},
	})
	return err
}

func (s *s3AlbumImportMultipartStore) AbortMultipartUpload(key string, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(&s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	return err
}

func (s *s3AlbumImportMultipartStore) OpenObject(key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(&s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (s *s3AlbumImportMultipartStore) DeleteObject(key string) error {
	_, err := s.client.DeleteObject(&s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	return err
}

func requireAlbumImportMultipartStore(store albumImportMultipartStore) error {
	if store == nil {
		return apperr.New(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", nil)
	}
	return nil
}
```

- [ ] **Step 3: Wire the store into the service**

In `Atoman-Backend/internal/modules/music/service.go`, update `Service` and constructor:

```go
type Service struct {
	db                   *gorm.DB
	repo                 *Repo
	s3                   *s3.S3
	albumImportMultipart albumImportMultipartStore
}

func NewService(db *gorm.DB) *Service { return &Service{db: db, repo: NewRepo(db)} }

func NewServiceWithS3(db *gorm.DB, s3Client *s3.S3) *Service {
	return &Service{
		db:                   db,
		repo:                 NewRepo(db),
		s3:                   s3Client,
		albumImportMultipart: newS3AlbumImportMultipartStore(s3Client),
	}
}
```

- [ ] **Step 4: Run compile check for expected missing methods**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
go test ./internal/modules/music -run TestRegisterRoutesCreateAlbumImportSessionSupportsArchiveUpload -count=1
```

Expected: compile succeeds and the existing archive upload route test passes.

- [ ] **Step 5: Commit**

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
git add internal/modules/music/import_types.go internal/modules/music/import_multipart_store.go internal/modules/music/service.go
git commit -m "feat: add music album multipart store boundary"
```

---

## Task 2: Backend Multipart Session State

**Files:**
- Modify: `Atoman-Backend/internal/modules/music/import_service.go`
- Modify: `Atoman-Backend/internal/modules/music/import_service_test.go`

- [ ] **Step 1: Add failing tests for init, restore, and part completion**

Add tests to `Atoman-Backend/internal/modules/music/import_service_test.go`:

```go
func TestStartAlbumImportMultipartRejectsOversizedArchive(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName:    "huge.zip",
		FileSize:    maxAlbumImportArchiveSize + 1,
		ContentType: "application/zip",
	})
	var appErr *apperr.AppError
	if err == nil || !errors.As(err, &appErr) || appErr.Code != "validation.invalid_request" {
		t.Fatalf("expected invalid request for oversized archive, got %#v", err)
	}
}

func TestStartAlbumImportMultipartRestoresSameFileUpload(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-1"}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	first, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName:    "album.zip",
		FileSize:    64 * 1024 * 1024,
		ContentType: "application/zip",
	})
	if err != nil {
		t.Fatalf("start multipart: %v", err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: "\"etag-1\"", Size: first.PartSize}); err != nil {
		t.Fatalf("complete part: %v", err)
	}

	restored, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName:    "album.zip",
		FileSize:    64 * 1024 * 1024,
		ContentType: "application/zip",
	})
	if err != nil {
		t.Fatalf("restore multipart: %v", err)
	}
	if restored.ObjectKey != first.ObjectKey || restored.CompletedParts[0].ETag != "\"etag-1\"" {
		t.Fatalf("expected restored upload state, first=%#v restored=%#v", first, restored)
	}
	if store.createCalls != 1 {
		t.Fatalf("expected one create call, got %d", store.createCalls)
	}
}

func TestCreateAlbumImportMultipartPartUploadReturnsSignedURL(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{uploadID: "upload-1", signedURL: "https://r2.example.com/part-2"}
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 32 * 1024 * 1024, ContentType: "application/zip"}); err != nil {
		t.Fatalf("start multipart: %v", err)
	}

	part, err := svc.CreateAlbumImportMultipartPartUpload(user, session.ID, 2, CreateAlbumImportMultipartPartInput{PartSize: albumImportMultipartPartSize})
	if err != nil {
		t.Fatalf("create part upload: %v", err)
	}
	if part.PartNumber != 2 || part.UploadURL != "https://r2.example.com/part-2" {
		t.Fatalf("unexpected signed part: %#v", part)
	}
}
```

Also add the fake store in the same test file:

```go
type fakeAlbumImportMultipartStore struct {
	uploadID    string
	signedURL   string
	createCalls int
}

func (f *fakeAlbumImportMultipartStore) CreateMultipartUpload(key string, contentType string) (string, error) {
	f.createCalls++
	if f.uploadID == "" {
		return "upload-test", nil
	}
	return f.uploadID, nil
}

func (f *fakeAlbumImportMultipartStore) PresignUploadPart(key string, uploadID string, partNumber int, expires time.Duration) (string, error) {
	if f.signedURL != "" {
		return f.signedURL, nil
	}
	return fmt.Sprintf("https://r2.example.com/%s?uploadId=%s&partNumber=%d", key, uploadID, partNumber), nil
}

func (f *fakeAlbumImportMultipartStore) CompleteMultipartUpload(key string, uploadID string, parts []AlbumImportMultipartPartDTO) error {
	return nil
}

func (f *fakeAlbumImportMultipartStore) AbortMultipartUpload(key string, uploadID string) error {
	return nil
}

func (f *fakeAlbumImportMultipartStore) OpenObject(key string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (f *fakeAlbumImportMultipartStore) DeleteObject(key string) error {
	return nil
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
go test ./internal/modules/music -run 'TestStartAlbumImportMultipart|TestCreateAlbumImportMultipartPartUpload' -count=1
```

Expected: FAIL because multipart service methods and constants are missing.

- [ ] **Step 3: Implement multipart state helpers and service methods**

In `Atoman-Backend/internal/modules/music/import_service.go`, add constants and helpers near import code:

```go
const (
	maxAlbumImportArchiveSize int64 = 2 * 1024 * 1024 * 1024
	albumImportMultipartPartSize int64 = 16 * 1024 * 1024
)

type albumImportMultipartPayload struct {
	ArchiveName       string                        `json:"archiveName"`
	ArchiveSize       int64                         `json:"archiveSize"`
	ArchiveKey        string                        `json:"archiveKey"`
	MultipartUploadID string                        `json:"multipartUploadId"`
	MultipartPartSize int64                         `json:"multipartPartSize"`
	MultipartParts    []AlbumImportMultipartPartDTO `json:"multipartParts"`
	ErrorMessage      string                        `json:"errorMessage"`
}

func loadAlbumImportPayloadMap(session model.AlbumImportSession) (map[string]any, error) {
	payload := map[string]any{}
	if strings.TrimSpace(session.PayloadJSON) == "" {
		return payload, nil
	}
	if err := json.Unmarshal([]byte(session.PayloadJSON), &payload); err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "payload is not valid JSON")
	}
	return payload, nil
}

func multipartPartsFromPayload(payload map[string]any) []AlbumImportMultipartPartDTO {
	rawParts, ok := payload["multipartParts"].([]any)
	if !ok {
		return []AlbumImportMultipartPartDTO{}
	}
	parts := make([]AlbumImportMultipartPartDTO, 0, len(rawParts))
	for _, rawPart := range rawParts {
		partMap, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		parts = append(parts, AlbumImportMultipartPartDTO{
			PartNumber: int(floatValue(partMap["partNumber"])),
			ETag:       stringValue(partMap["etag"]),
			Size:       int64(floatValue(partMap["size"])),
		})
	}
	return parts
}
```

Add methods:

```go
func (s *Service) StartAlbumImportMultipart(user authctx.CurrentUser, id uuid.UUID, input StartAlbumImportMultipartInput) (AlbumImportMultipartDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportMultipartDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportMultipartDTO{}, err
	}
	fileName := strings.TrimSpace(input.FileName)
	if fileName == "" || strings.ToLower(filepath.Ext(fileName)) != ".zip" {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "archive must be a zip file")
	}
	if input.FileSize <= 0 || input.FileSize > maxAlbumImportArchiveSize {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "archive must be 2GB or smaller")
	}

	var out AlbumImportMultipartDTO
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_not_found", "Import session not found")
			}
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload && session.Status != AlbumImportStatusUploading && session.Status != AlbumImportStatusFailed {
			return apperr.Unprocessable("music.import_invalid_status", "Import session is not ready for upload")
		}

		payload, err := loadAlbumImportPayloadMap(session)
		if err != nil {
			return err
		}
		existingName := stringValue(payload["archiveName"])
		existingSize := int64(floatValue(payload["archiveSize"]))
		existingUploadID := stringValue(payload["multipartUploadId"])
		existingKey := stringValue(payload["archiveKey"])
		if existingName == fileName && existingSize == input.FileSize && existingUploadID != "" && existingKey != "" {
			out = AlbumImportMultipartDTO{
				ImportID:       session.ID.String(),
				FileName:       fileName,
				FileSize:       input.FileSize,
				ObjectKey:      existingKey,
				PartSize:       int64(floatValue(payload["multipartPartSize"])),
				CompletedParts: multipartPartsFromPayload(payload),
			}
			return nil
		}

		objectKey := "music/imports/albums/" + session.ID.String() + "/" + uuid.NewString() + ".zip"
		contentType := strings.TrimSpace(input.ContentType)
		if contentType == "" {
			contentType = "application/zip"
		}
		uploadID, err := s.albumImportMultipart.CreateMultipartUpload(objectKey, contentType)
		if err != nil {
			return err
		}
		payload["archiveName"] = fileName
		payload["archiveSize"] = input.FileSize
		payload["archiveKey"] = objectKey
		payload["multipartUploadId"] = uploadID
		payload["multipartPartSize"] = albumImportMultipartPartSize
		payload["multipartParts"] = []AlbumImportMultipartPartDTO{}
		payload["upload_progress"] = 0
		payload["errorMessage"] = ""
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.Status = AlbumImportStatusUploading
		session.PayloadJSON = string(raw)
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = AlbumImportMultipartDTO{
			ImportID:       session.ID.String(),
			FileName:       fileName,
			FileSize:       input.FileSize,
			ObjectKey:      objectKey,
			PartSize:       albumImportMultipartPartSize,
			CompletedParts: []AlbumImportMultipartPartDTO{},
		}
		return nil
	})
	return out, err
}

func (s *Service) CreateAlbumImportMultipartPartUpload(user authctx.CurrentUser, id uuid.UUID, partNumber int, input CreateAlbumImportMultipartPartInput) (AlbumImportMultipartPartUploadDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportMultipartPartUploadDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	if partNumber <= 0 {
		return AlbumImportMultipartPartUploadDTO{}, apperr.BadRequest("validation.invalid_request", "part number is required")
	}
	session, err := s.GetAlbumImportSession(id)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	payload, err := loadAlbumImportPayloadMap(session)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	key := stringValue(payload["archiveKey"])
	uploadID := stringValue(payload["multipartUploadId"])
	if key == "" || uploadID == "" {
		return AlbumImportMultipartPartUploadDTO{}, apperr.Unprocessable("music.import_invalid_status", "Upload has not started")
	}
	signedURL, err := s.albumImportMultipart.PresignUploadPart(key, uploadID, partNumber, 15*time.Minute)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	return AlbumImportMultipartPartUploadDTO{PartNumber: partNumber, UploadURL: signedURL}, nil
}

func (s *Service) CompleteAlbumImportMultipartPart(user authctx.CurrentUser, id uuid.UUID, partNumber int, input CompleteAlbumImportMultipartPartInput) (AlbumImportMultipartDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportMultipartDTO{}, apperr.Unauthorized("Login required")
	}
	if partNumber <= 0 || strings.TrimSpace(input.ETag) == "" || input.Size <= 0 {
		return AlbumImportMultipartDTO{}, apperr.BadRequest("validation.invalid_request", "part metadata is required")
	}
	var out AlbumImportMultipartDTO
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return apperr.NotFound("music.import_not_found", "Import session not found")
			}
			return err
		}
		payload, err := loadAlbumImportPayloadMap(session)
		if err != nil {
			return err
		}
		parts := multipartPartsFromPayload(payload)
		next := AlbumImportMultipartPartDTO{PartNumber: partNumber, ETag: strings.TrimSpace(input.ETag), Size: input.Size}
		replaced := false
		for index := range parts {
			if parts[index].PartNumber == partNumber {
				parts[index] = next
				replaced = true
			}
		}
		if !replaced {
			parts = append(parts, next)
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
		payload["multipartParts"] = parts
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.Status = AlbumImportStatusUploading
		session.PayloadJSON = string(raw)
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = AlbumImportMultipartDTO{
			ImportID:       session.ID.String(),
			FileName:       stringValue(payload["archiveName"]),
			FileSize:       int64(floatValue(payload["archiveSize"])),
			ObjectKey:      stringValue(payload["archiveKey"]),
			PartSize:       int64(floatValue(payload["multipartPartSize"])),
			CompletedParts: parts,
		}
		return nil
	})
	return out, err
}
```

Add imports if missing: `sort`.

- [ ] **Step 4: Run focused tests**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
go test ./internal/modules/music -run 'TestStartAlbumImportMultipart|TestCreateAlbumImportMultipartPartUpload' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
git add internal/modules/music/import_service.go internal/modules/music/import_service_test.go
git commit -m "feat: persist music album multipart state"
```

---

## Task 3: Backend Multipart Complete And Temp-File Extraction

**Files:**
- Modify: `Atoman-Backend/internal/modules/music/import_service.go`
- Modify: `Atoman-Backend/internal/modules/music/import_service_test.go`

- [ ] **Step 1: Add failing tests for sorted completion and temp-file extraction**

Add tests:

```go
func TestCompleteAlbumImportMultipartSortsPartsAndDeletesArchive(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	archive := newImportTestZipArchive(t, map[string]string{"01 - Track.mp3": ""})
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-1", objectBody: archive}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: int64(len(archive)), ContentType: "application/zip"}); err != nil {
		t.Fatalf("start multipart: %v", err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 2, CompleteAlbumImportMultipartPartInput{ETag: "\"etag-2\"", Size: 1}); err != nil {
		t.Fatalf("complete part 2: %v", err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: "\"etag-1\"", Size: 1}); err != nil {
		t.Fatalf("complete part 1: %v", err)
	}

	updated, err := svc.CompleteAlbumImportMultipart(user, session.ID)
	if err != nil {
		t.Fatalf("complete multipart: %v", err)
	}
	if updated.Status != AlbumImportStatusReady {
		t.Fatalf("expected ready status, got %#v", updated)
	}
	if got := store.completedPartNumbers; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("expected sorted completed parts, got %#v", got)
	}
	if !store.deleted {
		t.Fatal("expected source zip object to be deleted after extraction")
	}
}
```

Extend `fakeAlbumImportMultipartStore`:

```go
objectBody           []byte
completedPartNumbers []int
deleted              bool

func (f *fakeAlbumImportMultipartStore) CompleteMultipartUpload(key string, uploadID string, parts []AlbumImportMultipartPartDTO) error {
	f.completedPartNumbers = f.completedPartNumbers[:0]
	for _, part := range parts {
		f.completedPartNumbers = append(f.completedPartNumbers, part.PartNumber)
	}
	return nil
}

func (f *fakeAlbumImportMultipartStore) OpenObject(key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.objectBody)), nil
}

func (f *fakeAlbumImportMultipartStore) DeleteObject(key string) error {
	f.deleted = true
	return nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
go test ./internal/modules/music -run TestCompleteAlbumImportMultipartSortsPartsAndDeletesArchive -count=1
```

Expected: FAIL because `CompleteAlbumImportMultipart` is missing.

- [ ] **Step 3: Refactor derivation to support temp zip files**

In `import_service.go`, keep `UploadAlbumImportArchive` behavior but route through a temp file:

```go
func (s *Service) deriveAlbumImportPayloadFromReader(user authctx.CurrentUser, archiveName string, reader io.Reader) (map[string]any, error) {
	tmp, err := os.CreateTemp("", "atoman-album-import-*.zip")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := io.Copy(tmp, reader); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	return s.deriveAlbumImportPayloadFromZipFile(user, archiveName, tmpPath)
}

func (s *Service) deriveAlbumImportPayload(user authctx.CurrentUser, archiveName string, body []byte) (map[string]any, error) {
	return s.deriveAlbumImportPayloadFromReader(user, archiveName, bytes.NewReader(body))
}

func (s *Service) deriveAlbumImportPayloadFromZipFile(user authctx.CurrentUser, archiveName string, zipPath string) (map[string]any, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "archive must be a valid zip file")
	}
	defer func() {
		_ = reader.Close()
	}()

	derivedTracks := make([]map[string]any, 0)
	var coverURL string

	for _, file := range reader.File {
		// Move the existing loop body from deriveAlbumImportPayload here unchanged.
	}

	return map[string]any{
		"archiveName":        archiveName,
		"derived_album_title": strings.TrimSpace(strings.TrimSuffix(archiveName, filepath.Ext(archiveName))),
		"derived_tracks":      derivedTracks,
		"derived_cover":       coverURL,
		"upload_progress":     100,
		"upload_speed":        0,
	}, nil
}
```

When moving the loop body, replace `reader.File` access accordingly and remove the old `zip.NewReader(bytes.NewReader(body), int64(len(body)))` path.

- [ ] **Step 4: Implement final multipart complete**

Add to `import_service.go`:

```go
func (s *Service) CompleteAlbumImportMultipart(user authctx.CurrentUser, id uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return model.AlbumImportSession{}, err
	}

	var session model.AlbumImportSession
	if err := s.db.First(&session, "id = ?", id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.AlbumImportSession{}, apperr.NotFound("music.import_not_found", "Import session not found")
		}
		return model.AlbumImportSession{}, err
	}
	payload, err := loadAlbumImportPayloadMap(session)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	key := stringValue(payload["archiveKey"])
	uploadID := stringValue(payload["multipartUploadId"])
	archiveName := stringValue(payload["archiveName"])
	parts := multipartPartsFromPayload(payload)
	if key == "" || uploadID == "" || archiveName == "" || len(parts) == 0 {
		return model.AlbumImportSession{}, apperr.Unprocessable("music.import_invalid_status", "Upload is incomplete")
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	if err := s.albumImportMultipart.CompleteMultipartUpload(key, uploadID, parts); err != nil {
		return model.AlbumImportSession{}, err
	}
	if err := s.updateAlbumImportStatusAndPayload(id, AlbumImportStatusUploaded, payload); err != nil {
		return model.AlbumImportSession{}, err
	}
	if err := s.updateAlbumImportStatusAndPayload(id, AlbumImportStatusExtracting, payload); err != nil {
		return model.AlbumImportSession{}, err
	}

	body, err := s.albumImportMultipart.OpenObject(key)
	if err != nil {
		_ = s.markAlbumImportFailed(id, "压缩包读取失败")
		return model.AlbumImportSession{}, err
	}
	defer func() {
		_ = body.Close()
	}()
	derived, err := s.deriveAlbumImportPayloadFromReader(user, archiveName, body)
	if err != nil {
		_ = s.markAlbumImportFailed(id, "压缩包解析失败，请检查文件后重试")
		_ = s.albumImportMultipart.DeleteObject(key)
		return model.AlbumImportSession{}, err
	}
	for k, v := range derived {
		payload[k] = v
	}
	delete(payload, "multipartUploadId")
	payload["archiveKey"] = key
	if err := s.albumImportMultipart.DeleteObject(key); err != nil {
		return model.AlbumImportSession{}, err
	}
	return s.updateAlbumImportStatusAndPayload(id, AlbumImportStatusReady, payload)
}
```

Add helpers:

```go
func (s *Service) updateAlbumImportStatusAndPayload(id uuid.UUID, status string, payload map[string]any) (model.AlbumImportSession, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	var session model.AlbumImportSession
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&session, "id = ?", id).Error; err != nil {
			return err
		}
		session.Status = status
		session.PayloadJSON = string(raw)
		return tx.Save(&session).Error
	})
	return session, err
}

func (s *Service) markAlbumImportFailed(id uuid.UUID, message string) error {
	var session model.AlbumImportSession
	if err := s.db.First(&session, "id = ?", id).Error; err != nil {
		return err
	}
	payload, err := loadAlbumImportPayloadMap(session)
	if err != nil {
		payload = map[string]any{}
	}
	payload["errorMessage"] = message
	_, err = s.updateAlbumImportStatusAndPayload(id, AlbumImportStatusFailed, payload)
	return err
}
```

- [ ] **Step 5: Run focused tests**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
go test ./internal/modules/music -run 'TestCompleteAlbumImportMultipartSortsPartsAndDeletesArchive|TestUploadAlbumImportArchiveTransitionsPendingUploadToReady' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
git add internal/modules/music/import_service.go internal/modules/music/import_service_test.go
git commit -m "feat: complete music album multipart imports"
```

---

## Task 4: Backend HTTP Endpoints

**Files:**
- Modify: `Atoman-Backend/internal/modules/music/import_http.go`
- Modify: `Atoman-Backend/internal/modules/music/http.go`
- Modify: `Atoman-Backend/internal/modules/music/http_test.go`

- [ ] **Step 1: Add failing HTTP tests**

Add tests to `http_test.go`:

```go
func TestRegisterRoutesStartsAlbumImportMultipart(t *testing.T) {
	service, _, user := newMusicHTTPTestService(t)
	service.albumImportMultipart = &fakeAlbumImportMultipartStore{uploadID: "upload-1"}
	r := newMusicHTTPRouter(service, &user)
	createBody, _ := json.Marshal(CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected create 201, got %d: %s", w.Code, w.Body.String())
	}
	var createResp struct{ Data AlbumImportDTO `json:"data"` }
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	startBody, _ := json.Marshal(StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 1024, ContentType: "application/zip"})
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/music/imports/albums/"+createResp.Data.ImportID+"/multipart", bytes.NewReader(startBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct{ Data AlbumImportMultipartDTO `json:"data"` }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.FileName != "album.zip" || resp.Data.PartSize != albumImportMultipartPartSize {
		t.Fatalf("unexpected multipart response: %#v", resp.Data)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
go test ./internal/modules/music -run TestRegisterRoutesStartsAlbumImportMultipart -count=1
```

Expected: FAIL with 404 because routes are missing.

- [ ] **Step 3: Add handlers**

In `import_http.go`, add:

```go
func (h *Handler) startAlbumImportMultipart(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	var req StartAlbumImportMultipartInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	out, err := h.service.StartAlbumImportMultipart(user, sessionID, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

func (h *Handler) createAlbumImportMultipartPartUpload(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "part number is invalid"))
		return
	}
	var req CreateAlbumImportMultipartPartInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	out, err := h.service.CreateAlbumImportMultipartPartUpload(user, sessionID, partNumber, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

func (h *Handler) completeAlbumImportMultipartPart(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	partNumber, err := strconv.Atoi(c.Param("partNumber"))
	if err != nil {
		httpx.Error(c, apperr.BadRequest("validation.invalid_request", "part number is invalid"))
		return
	}
	var req CompleteAlbumImportMultipartPartInput
	if err := bindJSON(c, &req); err != nil {
		httpx.Error(c, err)
		return
	}
	out, err := h.service.CompleteAlbumImportMultipartPart(user, sessionID, partNumber, req)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, out)
}

func (h *Handler) completeAlbumImportMultipart(c *gin.Context) {
	user, ok := authctx.Current(c)
	if !ok {
		httpx.Error(c, apperr.Unauthorized("Login required"))
		return
	}
	sessionID, err := parseMusicID(c.Param("sessionId"), "sessionId")
	if err != nil {
		httpx.Error(c, err)
		return
	}
	session, err := h.service.CompleteAlbumImportMultipart(user, sessionID)
	if err != nil {
		httpx.Error(c, err)
		return
	}
	httpx.OK(c, http.StatusOK, buildAlbumImportDTO(session))
}
```

Add `strconv` to imports.

- [ ] **Step 4: Register routes**

In `http.go`, add routes after the existing upload route:

```go
group.POST("/imports/albums/:sessionId/multipart", h.startAlbumImportMultipart)
group.POST("/imports/albums/:sessionId/multipart/parts/:partNumber", h.createAlbumImportMultipartPartUpload)
group.POST("/imports/albums/:sessionId/multipart/parts/:partNumber/complete", h.completeAlbumImportMultipartPart)
group.POST("/imports/albums/:sessionId/multipart/complete", h.completeAlbumImportMultipart)
```

- [ ] **Step 5: Run HTTP tests**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
go test ./internal/modules/music -run 'TestRegisterRoutesStartsAlbumImportMultipart|TestRegisterRoutesCreateAlbumImportSessionSupportsArchiveUpload' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
git add internal/modules/music/import_http.go internal/modules/music/http.go internal/modules/music/http_test.go
git commit -m "feat: expose music album multipart import endpoints"
```

---

## Task 5: Frontend Multipart API And Uploader

**Files:**
- Modify: `Atoman-Frontend/src/api/musicV1.ts`
- Modify: `Atoman-Frontend/tests/unit/api/musicV1.spec.ts`

- [ ] **Step 1: Add failing API and uploader tests**

Add tests under `describe('music v1 adapter')`:

```typescript
it('starts album multipart imports through the music namespace', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(
    JSON.stringify({ data: { importId: 'import-1', fileName: 'album.zip', fileSize: 1024, objectKey: 'music/imports/albums/import-1/archive.zip', partSize: 16_777_216, completedParts: [] } }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )))

  const result = await musicV1.startMusicAlbumImportMultipart('import-1', {
    fileName: 'album.zip',
    fileSize: 1024,
    contentType: 'application/zip',
  })

  expect(fetch).toHaveBeenCalledWith('/api/v1/music/imports/albums/import-1/multipart', expect.objectContaining({
    method: 'POST',
    body: JSON.stringify({ fileName: 'album.zip', fileSize: 1024, contentType: 'application/zip' }),
  }))
  expect(result.partSize).toBe(16_777_216)
})

it('rejects album archives over 2GB before upload', async () => {
  const file = new File(['zip'], 'huge.zip', { type: 'application/zip' })
  Object.defineProperty(file, 'size', { value: 2 * 1024 * 1024 * 1024 + 1 })

  await expect(musicV1.uploadMusicAlbumArchiveMultipart('import-1', file)).rejects.toThrow('文件需在 2GB 以内，请转换或压缩后上传')
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Frontend
bun test tests/unit/api/musicV1.spec.ts --runInBand
```

Expected: FAIL because new functions are missing.

- [ ] **Step 3: Add frontend types, endpoints, and adapters**

In `musicV1.ts`, add:

```typescript
const MAX_MUSIC_ALBUM_ARCHIVE_BYTES = 2 * 1024 * 1024 * 1024
const MULTIPART_UPLOAD_CONCURRENCY = 3
const MULTIPART_UPLOAD_RETRIES = 2

export type MusicAlbumImportMultipartPart = {
  partNumber: number
  etag: string
  size: number
}

export type StartMusicAlbumImportMultipartInput = {
  fileName: string
  fileSize: number
  contentType: string
}

export type MusicAlbumImportMultipart = {
  importId: string
  fileName: string
  fileSize: number
  objectKey: string
  partSize: number
  completedParts: MusicAlbumImportMultipartPart[]
}

export type MusicAlbumImportMultipartPartUpload = {
  partNumber: number
  uploadUrl: string
}
```

Add endpoints:

```typescript
albumImportMultipart: (importId: string) => `${apiV1Base()}/music/imports/albums/${importId}/multipart`,
albumImportMultipartPart: (importId: string, partNumber: number) => `${apiV1Base()}/music/imports/albums/${importId}/multipart/parts/${partNumber}`,
albumImportMultipartPartComplete: (importId: string, partNumber: number) => `${apiV1Base()}/music/imports/albums/${importId}/multipart/parts/${partNumber}/complete`,
albumImportMultipartComplete: (importId: string) => `${apiV1Base()}/music/imports/albums/${importId}/multipart/complete`,
```

Add API functions:

```typescript
export function validateMusicAlbumArchiveFile(file: File) {
  const lowerName = file.name.toLowerCase()
  if (!lowerName.endsWith('.zip')) {
    throw new Error('请选择 zip 压缩包')
  }
  if (file.size > MAX_MUSIC_ALBUM_ARCHIVE_BYTES) {
    throw new Error('文件需在 2GB 以内，请转换或压缩后上传')
  }
}

export async function startMusicAlbumImportMultipart(
  importId: string,
  input: StartMusicAlbumImportMultipartInput,
): Promise<MusicAlbumImportMultipart> {
  return apiPostJson<MusicAlbumImportMultipart>(musicV1Endpoints.albumImportMultipart(importId), input)
}

export async function createMusicAlbumImportMultipartPartUpload(
  importId: string,
  partNumber: number,
  partSize: number,
): Promise<MusicAlbumImportMultipartPartUpload> {
  return apiPostJson<MusicAlbumImportMultipartPartUpload>(
    musicV1Endpoints.albumImportMultipartPart(importId, partNumber),
    { partSize },
  )
}

export async function completeMusicAlbumImportMultipartPart(
  importId: string,
  partNumber: number,
  input: { etag: string; size: number },
): Promise<MusicAlbumImportMultipart> {
  return apiPostJson<MusicAlbumImportMultipart>(
    musicV1Endpoints.albumImportMultipartPartComplete(importId, partNumber),
    input,
  )
}

export async function completeMusicAlbumImportMultipart(importId: string): Promise<MusicAlbumImport> {
  return apiPostJson<MusicAlbumImport>(musicV1Endpoints.albumImportMultipartComplete(importId), {})
}
```

- [ ] **Step 4: Add multipart uploader**

Add:

```typescript
async function uploadPartWithRetry(url: string, body: Blob, retries = MULTIPART_UPLOAD_RETRIES): Promise<string> {
  let lastError: unknown
  for (let attempt = 0; attempt <= retries; attempt += 1) {
    try {
      const response = await fetch(url, { method: 'PUT', body })
      if (!response.ok) {
        throw new Error(`分片上传失败 (${response.status})`)
      }
      const etag = response.headers.get('ETag') || response.headers.get('etag') || ''
      if (!etag) {
        throw new Error('分片上传失败，缺少 ETag')
      }
      return etag
    } catch (error) {
      lastError = error
    }
  }
  throw lastError instanceof Error ? lastError : new Error('分片上传失败')
}

export async function uploadMusicAlbumArchiveMultipart(
  importId: string,
  file: File,
  options: UploadMusicAlbumArchiveOptions = {},
): Promise<MusicAlbumImport> {
  validateMusicAlbumArchiveFile(file)
  const startedAt = Date.now()
  const session = await startMusicAlbumImportMultipart(importId, {
    fileName: file.name,
    fileSize: file.size,
    contentType: file.type || 'application/zip',
  })
  const completed = new Map(session.completedParts.map((part) => [part.partNumber, part]))
  let uploadedBytes = session.completedParts.reduce((sum, part) => sum + part.size, 0)
  const totalParts = Math.ceil(file.size / session.partSize)
  const pendingParts = Array.from({ length: totalParts }, (_, index) => index + 1).filter((partNumber) => !completed.has(partNumber))

  async function uploadOne(partNumber: number) {
    const start = (partNumber - 1) * session.partSize
    const end = Math.min(start + session.partSize, file.size)
    const blob = file.slice(start, end)
    const signed = await createMusicAlbumImportMultipartPartUpload(importId, partNumber, blob.size)
    const etag = await uploadPartWithRetry(signed.uploadUrl, blob)
    await completeMusicAlbumImportMultipartPart(importId, partNumber, { etag, size: blob.size })
    uploadedBytes += blob.size
    const elapsedSeconds = Math.max((Date.now() - startedAt) / 1000, 0.001)
    options.onProgress?.({
      loaded: uploadedBytes,
      total: file.size,
      bytesPerSecond: uploadedBytes / elapsedSeconds,
    })
  }

  const workers = Array.from({ length: Math.min(MULTIPART_UPLOAD_CONCURRENCY, pendingParts.length) }, async () => {
    while (pendingParts.length > 0) {
      const partNumber = pendingParts.shift()
      if (!partNumber) return
      await uploadOne(partNumber)
    }
  })
  await Promise.all(workers)
  return completeMusicAlbumImportMultipart(importId)
}
```

- [ ] **Step 5: Run focused tests**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Frontend
bun test tests/unit/api/musicV1.spec.ts --runInBand
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/fafa/projects/Atoman/Atoman-Frontend
git add src/api/musicV1.ts tests/unit/api/musicV1.spec.ts
git commit -m "feat: add music album multipart uploader"
```

---

## Task 6: Frontend Creation Flow Integration

**Files:**
- Modify: `Atoman-Frontend/src/components/music/MusicCreationAlbumSeedStep.vue`
- Modify: `Atoman-Frontend/tests/unit/components/music/MusicCreationAlbumImportStep.spec.ts`

- [ ] **Step 1: Add failing component tests**

Update existing test mocks from `uploadMusicAlbumArchive` to `uploadMusicAlbumArchiveMultipart`, then add:

```typescript
it('rejects zip files over 2GB before creating an import session', async () => {
  const createSpy = vi.spyOn(musicApi, 'createMusicAlbumImport')
  const wrapper = mount(MusicCreationAlbumSeedStep)
  const file = new File(['zip'], 'huge.zip', { type: 'application/zip' })
  Object.defineProperty(file, 'size', { value: 2 * 1024 * 1024 * 1024 + 1 })
  const input = wrapper.get('[data-testid="album-import-archive-input"]').element as HTMLInputElement
  Object.defineProperty(input, 'files', { configurable: true, value: [file] })

  await wrapper.get('[data-testid="album-import-archive-input"]').trigger('change')

  expect(wrapper.text()).toContain('文件需在 2GB 以内，请转换或压缩后上传')
  expect(createSpy).not.toHaveBeenCalled()
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Frontend
bun test tests/unit/components/music/MusicCreationAlbumImportStep.spec.ts --runInBand
```

Expected: FAIL because the component still calls whole-file upload and does not prevalidate size.

- [ ] **Step 3: Use multipart uploader in the component**

In `MusicCreationAlbumSeedStep.vue`, replace import:

```typescript
import {
  createMusicAlbumImport,
  uploadMusicAlbumArchiveMultipart,
  validateMusicAlbumArchiveFile,
} from '@/api/musicV1'
```

In `handleArchiveChange`, validate before creating a session:

```typescript
try {
  validateMusicAlbumArchiveFile(file)
} catch (error) {
  errorMessage.value = error instanceof Error ? error.message : '压缩包上传失败'
  input.value = ''
  return
}
```

Replace upload call:

```typescript
const snapshot = await uploadMusicAlbumArchiveMultipart(session.importId, file, {
  onProgress(progress) {
    if (!albumImportDraft.value) return
    albumImportDraft.value.status = 'uploading'
    albumImportDraft.value.uploadProgress = progress.total > 0
      ? Math.round((progress.loaded / progress.total) * 100)
      : 0
    albumImportDraft.value.uploadSpeed = progress.bytesPerSecond
  },
})

albumImportDraft.value.status = 'extracting'
applyImportSnapshot(snapshot)
```

Remove the separate `getMusicAlbumImport(session.importId)` after upload, because multipart complete returns the ready snapshot.

- [ ] **Step 4: Run component tests**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Frontend
bun test tests/unit/components/music/MusicCreationAlbumImportStep.spec.ts --runInBand
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/fafa/projects/Atoman/Atoman-Frontend
git add src/components/music/MusicCreationAlbumSeedStep.vue tests/unit/components/music/MusicCreationAlbumImportStep.spec.ts
git commit -m "feat: use multipart upload in music creation flow"
```

---

## Task 7: Verification, CORS Check, And Deployment

**Files:**
- No required source file changes unless tests reveal issues.
- Optional external config: Cloudflare R2 bucket CORS in dashboard or wrangler/API.

- [ ] **Step 1: Run backend verification**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Backend
go test ./internal/modules/music -count=1
go build ./...
```

Expected: both commands pass.

- [ ] **Step 2: Run frontend verification**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Frontend
bun run type-check
bun test tests/unit/api/musicV1.spec.ts tests/unit/components/music/MusicCreationAlbumImportStep.spec.ts --runInBand
```

Expected: all commands pass.

- [ ] **Step 3: Confirm R2 CORS**

Confirm the production R2 bucket allows:

```json
[
  {
    "AllowedOrigins": ["https://www.atoman.org", "http://localhost:5173"],
    "AllowedMethods": ["PUT"],
    "AllowedHeaders": ["*"],
    "ExposeHeaders": ["ETag"],
    "MaxAgeSeconds": 3600
  }
]
```

If local dev uses a different Vite port, include that origin. Keep the API origin out of this CORS rule unless browser direct upload uses it.

- [ ] **Step 4: Deploy backend**

Run the existing production deployment process for `Atoman-Backend`. After deploy, verify:

```bash
curl -s https://api.atoman.org/api/v1/music/albums | head
```

Expected: JSON API response, not an HTML error page.

- [ ] **Step 5: Deploy frontend**

Run:

```bash
cd /Users/fafa/projects/Atoman/Atoman-Frontend
bun run build
wrangler pages deploy dist --project-name atoman-frontend
```

Expected: Cloudflare Pages deployment succeeds.

- [ ] **Step 6: Manual production smoke test**

Use `https://www.atoman.org/music`:

1. Start the music creation flow.
2. Select a zip smaller than 2GB.
3. Confirm upload progress advances.
4. Refresh during upload, select the same file again, and confirm progress resumes instead of restarting completed parts.
5. Confirm the flow reaches editable album details with derived tracks and cover.
6. Commit the album.

- [ ] **Step 7: Commit any verification fixes**

If verification required code fixes, commit only the touched task files:

```bash
git status --short
git add <fixed-files>
git commit -m "fix: stabilize music album multipart upload"
```

---

## Self-Review

Spec coverage:

- 2GB zip limit is covered in Task 2 and Task 5.
- Same-name same-size resume is covered in Task 2 and Task 5.
- Browser direct-to-R2 upload is covered in Task 1 and Task 5.
- Backend completion, temp-file zip extraction, and source zip deletion are covered in Task 3.
- HTTP endpoints are covered in Task 4.
- Frontend creation flow integration is covered in Task 6.
- R2 CORS and deploy order are covered in Task 7.

Placeholder scan:

- Placeholder scan passed: the plan has no incomplete sections or vague implementation steps.
- Each task includes concrete files, code shape, commands, and expected results.

Type consistency:

- Backend DTO names use `StartAlbumImportMultipartInput`, `AlbumImportMultipartDTO`, `AlbumImportMultipartPartDTO`, and `AlbumImportMultipartPartUploadDTO` consistently.
- Frontend adapter names use `startMusicAlbumImportMultipart`, `createMusicAlbumImportMultipartPartUpload`, `completeMusicAlbumImportMultipartPart`, `completeMusicAlbumImportMultipart`, and `uploadMusicAlbumArchiveMultipart` consistently.

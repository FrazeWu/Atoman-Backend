package books

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	bookUploadPartSize          int64 = 16 * 1024 * 1024
	bookUploadDefaultMaxSize    int64 = 200 * 1024 * 1024
	bookUploadLifetime                = 24 * time.Hour
	bookUploadPartURLLifetime         = 15 * time.Minute
	bookUploadMaxParts                = 10000
	bookUploadMaxFilenameLength       = 255
	bookUploadMaxActiveSessions       = 20
	bookUploadMaxErrorLength          = 512
)

const bookUploadMaxSizeEnv = "BOOK_IMPORT_MAX_SIZE"

type BookUploadPart struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

type CreateBookImportInput struct {
	Title       string `json:"title"`
	Author      string `json:"author"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

type BookUploadPartURL struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url"`
}

type BookImportSessionDTO struct {
	ID               string           `json:"id"`
	Title            string           `json:"title"`
	Author           string           `json:"author,omitempty"`
	FileName         string           `json:"file_name"`
	Format           string           `json:"format"`
	ContentType      string           `json:"content_type"`
	Size             int64            `json:"size"`
	Status           string           `json:"status"`
	PartSize         int64            `json:"part_size"`
	CompletedParts   []BookUploadPart `json:"completed_parts"`
	ExpiresAt        time.Time        `json:"expires_at"`
	AssetID          string           `json:"asset_id,omitempty"`
	WorkID           string           `json:"work_id,omitempty"`
	EditionID        string           `json:"edition_id,omitempty"`
	ProcessingStatus string           `json:"processing_status,omitempty"`
	ErrorCode        string           `json:"error_code,omitempty"`
	ErrorMessage     string           `json:"error_message,omitempty"`
}

type bookUploadStore interface {
	CreateMultipartUpload(key, contentType string) (string, error)
	PresignUploadPart(key, uploadID string, partNumber int, expires time.Duration) (string, error)
	CompleteMultipartUpload(key, uploadID string, parts []BookUploadPart) error
	ObjectSize(key string) (int64, error)
	PutObject(key, contentType string, body io.ReadSeeker, size int64) error
	AbortMultipartUpload(key, uploadID string) error
	OpenObject(key string) (io.ReadCloser, error)
	DeleteObject(key string) error
	CopyObject(sourceKey, destinationKey, contentType string) error
}

type s3BookUploadStore struct {
	client *s3.S3
	bucket string
}

func newS3BookUploadStore(client *s3.S3) bookUploadStore {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	if client == nil || bucket == "" {
		return nil
	}
	return &s3BookUploadStore{client: client, bucket: bucket}
}

func (s *s3BookUploadStore) CreateMultipartUpload(key, contentType string) (string, error) {
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

func (s *s3BookUploadStore) PresignUploadPart(key, uploadID string, partNumber int, expires time.Duration) (string, error) {
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

func (s *s3BookUploadStore) CompleteMultipartUpload(key, uploadID string, parts []BookUploadPart) error {
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

func (s *s3BookUploadStore) ObjectSize(key string) (int64, error) {
	out, err := s.client.HeadObject(&s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return 0, err
	}
	return aws.Int64Value(out.ContentLength), nil
}

func (s *s3BookUploadStore) PutObject(key, contentType string, body io.ReadSeeker, size int64) error {
	_, err := s.client.PutObject(&s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	return err
}

func (s *s3BookUploadStore) AbortMultipartUpload(key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(&s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	return err
}

func (s *s3BookUploadStore) OpenObject(key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(&s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (s *s3BookUploadStore) DeleteObject(key string) error {
	_, err := s.client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	return err
}

func (s *s3BookUploadStore) CopyObject(sourceKey, destinationKey, contentType string) error {
	_, err := s.client.CopyObject(&s3.CopyObjectInput{
		Bucket:            aws.String(s.bucket),
		Key:               aws.String(destinationKey),
		CopySource:        aws.String(url.PathEscape(s.bucket + "/" + sourceKey)),
		ContentType:       aws.String(contentType),
		MetadataDirective: aws.String("REPLACE"),
	})
	return err
}

func requireBookUploadStore(store bookUploadStore) error {
	if store == nil {
		return apperr.New(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", nil)
	}
	return nil
}

func bookUploadMaxSize() int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(bookUploadMaxSizeEnv)), 10, 64)
	if err != nil || value <= 0 {
		return bookUploadDefaultMaxSize
	}
	return value
}

func validateBookUploadMetadata(fileName, contentType string, size int64) (string, error) {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" || len(fileName) > bookUploadMaxFilenameLength || strings.ContainsAny(fileName, `/\\`) || strings.IndexFunc(fileName, unicode.IsControl) >= 0 {
		return "", apperr.BadRequest("validation.invalid_request", "book file name is invalid")
	}
	if size <= 0 || size > bookUploadMaxSize() {
		return "", apperr.BadRequest("books.upload_too_large", "book file size is invalid or exceeds the limit")
	}
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".")
	if format != "epub" && format != "pdf" {
		return "", apperr.BadRequest("books.unsupported_format", "only EPUB and PDF files are supported")
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return "", apperr.BadRequest("validation.invalid_request", "book content type is invalid")
	}
	mediaType = strings.ToLower(mediaType)
	validType := (format == "epub" && (mediaType == "application/epub+zip" || mediaType == "application/zip")) ||
		(format == "pdf" && mediaType == "application/pdf")
	if !validType {
		return "", apperr.BadRequest("books.content_type_mismatch", "book extension and content type do not match")
	}
	return format, nil
}

func bookPrivateObjectKey(userID, importID uuid.UUID, format string) string {
	return storage.BuildBookPrivateObjectKey(userID.String(), importID.String(), format)
}

func bookUploadPartCount(size, partSize int64) int {
	return int((size + partSize - 1) / partSize)
}

func bookUploadExpectedPartSize(size, partSize int64, partNumber int) int64 {
	start := int64(partNumber-1) * partSize
	return min(partSize, size-start)
}

func (s *Service) CreateBookImport(user authctx.CurrentUser, input CreateBookImportInput) (BookImportSessionDTO, error) {
	if user.ID == uuid.Nil {
		return BookImportSessionDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return BookImportSessionDTO{}, err
	}
	format, err := validateBookUploadMetadata(input.FileName, input.ContentType, input.Size)
	if err != nil {
		return BookImportSessionDTO{}, err
	}
	var active int64
	if err := s.db.Model(&model.UserBookImport{}).Where("user_id = ? AND status IN ?", user.ID, []string{
		model.BookImportStatusUploading, model.BookImportStatusCompleting, model.BookImportStatusUploaded,
		model.BookImportStatusScanning, model.BookImportStatusMetadataReady,
	}).Count(&active).Error; err != nil {
		return BookImportSessionDTO{}, err
	}
	if active >= bookUploadMaxActiveSessions {
		return BookImportSessionDTO{}, apperr.Conflict("books.upload_quota_exceeded", "Too many active book imports")
	}

	fileName := strings.TrimSpace(input.FileName)
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(fileName), filepath.Ext(fileName))
	}
	importID := uuid.New()
	key := bookPrivateObjectKey(user.ID, importID, format)
	uploadID, err := s.bookUpload.CreateMultipartUpload(key, input.ContentType)
	if err != nil {
		return BookImportSessionDTO{}, apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", err)
	}
	now := time.Now().UTC()
	session := model.UserBookImport{
		Base:               model.Base{ID: importID},
		UserID:             user.ID,
		Title:              title,
		Author:             strings.TrimSpace(input.Author),
		OriginalFilename:   fileName,
		Format:             format,
		ContentType:        strings.TrimSpace(input.ContentType),
		SizeBytes:          input.Size,
		ObjectKey:          key,
		UploadID:           uploadID,
		PartSize:           bookUploadPartSize,
		CompletedPartsJSON: "[]",
		ExpiresAt:          now.Add(bookUploadLifetime),
		MetadataJSON:       "{}",
		Status:             model.BookImportStatusUploading,
	}
	if err := s.db.Create(&session).Error; err != nil {
		_ = s.bookUpload.AbortMultipartUpload(key, uploadID)
		return BookImportSessionDTO{}, err
	}
	return buildBookImportSessionDTO(session, nil), nil
}

func (s *Service) ListBookImports(user authctx.CurrentUser) ([]BookImportSessionDTO, error) {
	if user.ID == uuid.Nil {
		return nil, apperr.Unauthorized("Login required")
	}
	var sessions []model.UserBookImport
	if err := s.db.Where("user_id = ? AND status <> ?", user.ID, model.BookImportStatusDeleted).Order("created_at DESC").Find(&sessions).Error; err != nil {
		return nil, err
	}
	result := make([]BookImportSessionDTO, 0, len(sessions))
	for _, session := range sessions {
		asset, err := s.findBookAsset(session.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, buildBookImportSessionDTO(session, asset))
	}
	return result, nil
}

func (s *Service) GetBookImport(user authctx.CurrentUser, id uuid.UUID) (BookImportSessionDTO, error) {
	session, err := s.loadBookImport(user, id)
	if err != nil {
		return BookImportSessionDTO{}, err
	}
	asset, err := s.findBookAsset(session.ID)
	if err != nil {
		return BookImportSessionDTO{}, err
	}
	return buildBookImportSessionDTO(session, asset), nil
}

func (s *Service) CreateBookUploadPart(user authctx.CurrentUser, id uuid.UUID, partNumber int) (BookUploadPartURL, error) {
	if partNumber <= 0 {
		return BookUploadPartURL{}, apperr.BadRequest("validation.invalid_request", "part number is invalid")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return BookUploadPartURL{}, err
	}
	session, err := s.loadBookImport(user, id)
	if err != nil {
		return BookUploadPartURL{}, err
	}
	if session.Status != model.BookImportStatusUploading || !session.ExpiresAt.After(time.Now().UTC()) {
		return BookUploadPartURL{}, apperr.Unprocessable("books.upload_invalid_status", "Book upload is not available")
	}
	if partNumber > bookUploadPartCount(session.SizeBytes, session.PartSize) || partNumber > bookUploadMaxParts {
		return BookUploadPartURL{}, apperr.BadRequest("validation.invalid_request", "part number is invalid")
	}
	uploadURL, err := s.bookUpload.PresignUploadPart(session.ObjectKey, session.UploadID, partNumber, bookUploadPartURLLifetime)
	if err != nil {
		return BookUploadPartURL{}, apperr.Wrap(http.StatusServiceUnavailable, "storage.unavailable", "Storage is unavailable", err)
	}
	return BookUploadPartURL{PartNumber: partNumber, UploadURL: uploadURL}, nil
}

func (s *Service) CompleteBookUploadPart(user authctx.CurrentUser, id uuid.UUID, partNumber int, part BookUploadPart) (BookImportSessionDTO, error) {
	if user.ID == uuid.Nil {
		return BookImportSessionDTO{}, apperr.Unauthorized("Login required")
	}
	if partNumber <= 0 || strings.TrimSpace(part.ETag) == "" || len(part.ETag) > 256 || part.Size <= 0 {
		return BookImportSessionDTO{}, apperr.BadRequest("validation.invalid_request", "completed part is invalid")
	}
	var session model.UserBookImport
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			return bookImportNotFound(err)
		}
		if session.Status != model.BookImportStatusUploading || !session.ExpiresAt.After(time.Now().UTC()) {
			return apperr.Unprocessable("books.upload_invalid_status", "Book upload is not available")
		}
		if partNumber > bookUploadPartCount(session.SizeBytes, session.PartSize) || part.Size != bookUploadExpectedPartSize(session.SizeBytes, session.PartSize, partNumber) {
			return apperr.BadRequest("validation.invalid_request", "completed part size is invalid")
		}
		parts, err := bookUploadParts(session.CompletedPartsJSON)
		if err != nil {
			return err
		}
		part.PartNumber = partNumber
		found := false
		for index := range parts {
			if parts[index].PartNumber == partNumber {
				parts[index] = part
				found = true
				break
			}
		}
		if !found {
			parts = append(parts, part)
		}
		sort.Slice(parts, func(left, right int) bool { return parts[left].PartNumber < parts[right].PartNumber })
		encoded, err := json.Marshal(parts)
		if err != nil {
			return err
		}
		session.CompletedPartsJSON = string(encoded)
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return BookImportSessionDTO{}, err
	}
	asset, err := s.findBookAsset(session.ID)
	if err != nil {
		return BookImportSessionDTO{}, err
	}
	return buildBookImportSessionDTO(session, asset), nil
}

func (s *Service) CompleteBookImport(user authctx.CurrentUser, id uuid.UUID) (BookImportSessionDTO, error) {
	if user.ID == uuid.Nil {
		return BookImportSessionDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return BookImportSessionDTO{}, err
	}
	var session model.UserBookImport
	var parts []BookUploadPart
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			return bookImportNotFound(err)
		}
		if session.Status == model.BookImportStatusScanning || session.Status == model.BookImportStatusMetadataReady {
			return nil
		}
		if session.Status != model.BookImportStatusUploading && session.Status != model.BookImportStatusUploaded {
			return apperr.Unprocessable("books.upload_invalid_status", "Book upload cannot be completed")
		}
		if !session.ExpiresAt.After(time.Now().UTC()) {
			return apperr.Unprocessable("books.upload_expired", "Book upload has expired")
		}
		var err error
		parts, err = bookUploadParts(session.CompletedPartsJSON)
		if err != nil {
			return err
		}
		if err := validateBookUploadParts(session.SizeBytes, session.PartSize, parts); err != nil {
			return err
		}
		session.Status = model.BookImportStatusCompleting
		return tx.Save(&session).Error
	}); err != nil {
		return BookImportSessionDTO{}, err
	}
	if session.Status == model.BookImportStatusScanning || session.Status == model.BookImportStatusMetadataReady {
		asset, err := s.findBookAsset(session.ID)
		if err != nil {
			return BookImportSessionDTO{}, err
		}
		return buildBookImportSessionDTO(session, asset), nil
	}

	if err := s.bookUpload.CompleteMultipartUpload(session.ObjectKey, session.UploadID, parts); err != nil {
		return BookImportSessionDTO{}, s.failBookImport(session, err, false)
	}
	objectSize, err := s.bookUpload.ObjectSize(session.ObjectKey)
	if err != nil {
		return BookImportSessionDTO{}, s.failBookImport(session, err, true)
	}
	if objectSize != session.SizeBytes {
		err := fmt.Errorf("uploaded object size %d does not match expected size %d", objectSize, session.SizeBytes)
		return BookImportSessionDTO{}, s.failBookImport(session, err, true)
	}
	object, err := s.bookUpload.OpenObject(session.ObjectKey)
	if err != nil {
		return BookImportSessionDTO{}, s.failBookImport(session, err, true)
	}
	sha, inspectErr := inspectBookObject(object, session.Format, session.SizeBytes)
	closeErr := object.Close()
	if inspectErr == nil {
		inspectErr = closeErr
	}
	if inspectErr != nil {
		return BookImportSessionDTO{}, s.failBookImport(session, inspectErr, true)
	}

	var asset model.UserBookAsset
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var locked model.UserBookImport
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&locked).Error; err != nil {
			return bookImportNotFound(err)
		}
		if err := tx.Where("import_id = ?", locked.ID).First(&asset).Error; err == nil {
			locked.Status = model.BookImportStatusScanning
			if err := tx.Save(&locked).Error; err != nil {
				return err
			}
			session = locked
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		asset = model.UserBookAsset{
			ImportID:         locked.ID,
			UserID:           locked.UserID,
			OriginalFilename: locked.OriginalFilename,
			ContentType:      locked.ContentType,
			Format:           locked.Format,
			SizeBytes:        locked.SizeBytes,
			SHA256:           sha,
			ObjectKey:        locked.ObjectKey,
			ScanStatus:       "pending",
			ProcessingStatus: model.BookAssetStatusScanning,
		}
		if err := tx.Create(&asset).Error; err != nil {
			return err
		}
		locked.Status = model.BookImportStatusScanning
		now := time.Now().UTC()
		locked.CompletedAt = &now
		if err := tx.Save(&locked).Error; err != nil {
			return err
		}
		session = locked
		return nil
	}); err != nil {
		return BookImportSessionDTO{}, s.failBookImport(session, err, true)
	}
	return buildBookImportSessionDTO(session, &asset), nil
}

func (s *Service) DeleteBookImport(user authctx.CurrentUser, id uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	var session model.UserBookImport
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
			return bookImportNotFound(err)
		}
		if session.Status == model.BookImportStatusDeleted {
			return nil
		}
		var assets []model.UserBookAsset
		if err := tx.Where("import_id = ? AND user_id = ?", session.ID, user.ID).Find(&assets).Error; err != nil {
			return err
		}
		for _, asset := range assets {
			if err := tx.Where("asset_id = ? AND user_id = ?", asset.ID, user.ID).Delete(&model.UserBookReadingState{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&model.UserBookAsset{}).Where("import_id = ? AND user_id = ?", session.ID, user.ID).
			Updates(map[string]any{"processing_status": model.BookAssetStatusRemoved, "error_message": "deleted by owner"}).Error; err != nil {
			return err
		}
		session.Status = model.BookImportStatusDeleted
		session.ErrorCode = ""
		session.ErrorMessage = ""
		return tx.Save(&session).Error
	}); err != nil {
		return err
	}
	if s.bookUpload == nil {
		return nil
	}
	return s.cleanupBookImport(context.Background(), session.ID)
}

func (s *Service) loadBookImport(user authctx.CurrentUser, id uuid.UUID) (model.UserBookImport, error) {
	if user.ID == uuid.Nil {
		return model.UserBookImport{}, apperr.Unauthorized("Login required")
	}
	var session model.UserBookImport
	if err := s.db.Where("id = ? AND user_id = ?", id, user.ID).First(&session).Error; err != nil {
		return model.UserBookImport{}, bookImportNotFound(err)
	}
	return session, nil
}

func (s *Service) findBookAsset(importID uuid.UUID) (*model.UserBookAsset, error) {
	var asset model.UserBookAsset
	if err := s.db.Where("import_id = ?", importID).First(&asset).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &asset, nil
}

func (s *Service) failBookImport(session model.UserBookImport, cause error, deleteObject bool) error {
	if session.UploadID != "" {
		_ = s.bookUpload.AbortMultipartUpload(session.ObjectKey, session.UploadID)
	}
	if deleteObject && session.ObjectKey != "" {
		_ = s.bookUpload.DeleteObject(session.ObjectKey)
	}
	message := truncateBookUploadError(cause.Error())
	if err := s.db.Model(&model.UserBookImport{}).Where("id = ? AND status = ?", session.ID, model.BookImportStatusCompleting).
		Updates(map[string]any{"status": model.BookImportStatusFailed, "error_code": "books.upload_failed", "error_message": message}).Error; err != nil {
		return err
	}
	return apperr.Wrap(http.StatusUnprocessableEntity, "books.upload_failed", "Book upload failed", cause)
}

func bookImportNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperr.NotFound("books.import_not_found", "Book import not found")
	}
	return err
}

func buildBookImportSessionDTO(session model.UserBookImport, asset *model.UserBookAsset) BookImportSessionDTO {
	parts, _ := bookUploadParts(session.CompletedPartsJSON)
	dto := BookImportSessionDTO{
		ID:             session.ID.String(),
		Title:          session.Title,
		Author:         session.Author,
		FileName:       session.OriginalFilename,
		Format:         session.Format,
		ContentType:    session.ContentType,
		Size:           session.SizeBytes,
		Status:         session.Status,
		PartSize:       session.PartSize,
		CompletedParts: parts,
		ExpiresAt:      session.ExpiresAt,
		ErrorCode:      session.ErrorCode,
		ErrorMessage:   session.ErrorMessage,
	}
	if session.WorkID != nil {
		dto.WorkID = session.WorkID.String()
	}
	if session.EditionID != nil {
		dto.EditionID = session.EditionID.String()
	}
	if asset != nil {
		dto.AssetID = asset.ID.String()
		dto.ProcessingStatus = asset.ProcessingStatus
	}
	return dto
}

func bookUploadParts(raw string) ([]BookUploadPart, error) {
	parts := []BookUploadPart{}
	if strings.TrimSpace(raw) == "" {
		return parts, nil
	}
	if err := json.Unmarshal([]byte(raw), &parts); err != nil {
		return nil, err
	}
	return parts, nil
}

func validateBookUploadParts(size, partSize int64, parts []BookUploadPart) error {
	if partSize <= 0 || len(parts) != bookUploadPartCount(size, partSize) {
		return apperr.Unprocessable("books.upload_incomplete", "Book upload is incomplete")
	}
	for index, part := range parts {
		partNumber := index + 1
		if part.PartNumber != partNumber || strings.TrimSpace(part.ETag) == "" || len(part.ETag) > 256 || part.Size != bookUploadExpectedPartSize(size, partSize, partNumber) {
			return apperr.Unprocessable("books.upload_incomplete", "Book upload is incomplete")
		}
	}
	return nil
}

func inspectBookObject(reader io.Reader, format string, expectedSize int64) (string, error) {
	if reader == nil {
		return "", errors.New("book object is empty")
	}
	hasher := sha256.New()
	header := make([]byte, 512)
	headerSize, err := io.ReadFull(reader, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return "", err
	}
	var textValidator *utf8StreamValidator
	switch format {
	case "epub":
		if headerSize < 4 || !bytes.Equal(header[:4], []byte("PK\x03\x04")) {
			return "", errors.New("EPUB magic does not match")
		}
	case "pdf":
		if headerSize < 5 || !bytes.Equal(header[:5], []byte("%PDF-")) {
			return "", errors.New("PDF magic does not match")
		}
	case "txt":
		textValidator = &utf8StreamValidator{}
	default:
		return "", errors.New("unsupported book format")
	}
	if _, err := hasher.Write(header[:headerSize]); err != nil {
		return "", err
	}
	if textValidator != nil {
		if _, err := textValidator.Write(header[:headerSize]); err != nil {
			return "", err
		}
	}
	var destination io.Writer = hasher
	if textValidator != nil {
		destination = io.MultiWriter(hasher, textValidator)
	}
	bodySize, err := io.CopyBuffer(destination, reader, make([]byte, 64*1024))
	if err != nil {
		return "", err
	}
	if textValidator != nil {
		if err := textValidator.Finalize(); err != nil {
			return "", err
		}
	}
	if int64(headerSize)+bodySize != expectedSize {
		return "", errors.New("book object size changed while reading")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type utf8StreamValidator struct {
	pending []byte
}

func (validator *utf8StreamValidator) Write(chunk []byte) (int, error) {
	data := make([]byte, 0, len(validator.pending)+len(chunk))
	data = append(data, validator.pending...)
	data = append(data, chunk...)
	consumed := 0
	for consumed < len(data) {
		if !utf8.FullRune(data[consumed:]) {
			break
		}
		runeValue, size := utf8.DecodeRune(data[consumed:])
		if runeValue == utf8.RuneError && size == 1 {
			return 0, errors.New("TXT is not valid UTF-8")
		}
		consumed += size
	}
	validator.pending = append(validator.pending[:0], data[consumed:]...)
	return len(chunk), nil
}

func (validator *utf8StreamValidator) Finalize() error {
	if len(validator.pending) != 0 {
		return errors.New("TXT is not valid UTF-8")
	}
	return nil
}

func truncateBookUploadError(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > bookUploadMaxErrorLength {
		return message[:bookUploadMaxErrorLength]
	}
	return message
}

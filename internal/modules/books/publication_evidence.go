package books

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"path/filepath"
	"strings"
	"unicode"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const bookPublicationEvidenceMaxSize int64 = 25 * 1024 * 1024

type BookPublicationEvidenceDTO struct {
	RequestID   string `json:"request_id"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
}

func (s *Service) UploadPublicationEvidence(user authctx.CurrentUser, requestID uuid.UUID, fileName, contentType string, size int64, body io.Reader) (BookPublicationRequestDTO, error) {
	if user.ID == uuid.Nil {
		return BookPublicationRequestDTO{}, apperr.Unauthorized("Login required")
	}
	if requestID == uuid.Nil {
		return BookPublicationRequestDTO{}, apperr.BadRequest("validation.invalid_request", "request_id is required")
	}
	fileName, contentType, err := validateBookPublicationEvidence(fileName, contentType, size)
	if err != nil {
		return BookPublicationRequestDTO{}, err
	}
	if body == nil {
		return BookPublicationRequestDTO{}, apperr.BadRequest("validation.invalid_request", "evidence file is required")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return BookPublicationRequestDTO{}, err
	}
	data, err := io.ReadAll(io.LimitReader(body, bookPublicationEvidenceMaxSize+1))
	if err != nil {
		return BookPublicationRequestDTO{}, apperr.BadRequest("books.evidence_read_failed", "evidence file could not be read")
	}
	if int64(len(data)) != size {
		return BookPublicationRequestDTO{}, apperr.BadRequest("validation.invalid_request", "evidence file size is invalid")
	}
	if err := validateBookPublicationEvidenceContent(contentType, data); err != nil {
		return BookPublicationRequestDTO{}, err
	}

	var request model.BookPublicationRequest
	if err := s.db.Where("id = ? AND submitted_by = ?", requestID, user.ID).First(&request).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BookPublicationRequestDTO{}, apperr.NotFound("books.publication_request_not_found", "Publication request not found")
		}
		return BookPublicationRequestDTO{}, err
	}
	if request.Status != model.BookPublicationStatusPendingReview {
		return BookPublicationRequestDTO{}, apperr.Conflict("books.publication_not_pending", "Evidence can only be uploaded for a pending publication request")
	}
	var rights model.BookRightsDeclaration
	if err := s.db.Where("request_id = ?", request.ID).First(&rights).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BookPublicationRequestDTO{}, apperr.NotFound("books.rights_declaration_not_found", "Rights declaration not found")
		}
		return BookPublicationRequestDTO{}, err
	}

	newKey := storage.BuildBookPublicationEvidenceObjectKey(request.ID.String(), uuid.NewString(), bookPublicationEvidenceExtension(contentType))
	if err := s.bookUpload.PutObject(newKey, contentType, bytes.NewReader(data), size); err != nil {
		return BookPublicationRequestDTO{}, apperr.Wrap(503, "storage.unavailable", "Storage is unavailable", err)
	}
	oldKey := rights.EvidenceObjectKey
	updates := map[string]any{
		"evidence_object_key":   newKey,
		"evidence_file_name":    fileName,
		"evidence_content_type": contentType,
		"evidence_size_bytes":   size,
	}
	result := s.db.Model(&rights).Where("id = ? AND EXISTS (SELECT 1 FROM book_publication_requests WHERE id = ? AND status = ?)", rights.ID, request.ID, model.BookPublicationStatusPendingReview).Updates(updates)
	if result.Error != nil {
		_ = s.bookUpload.DeleteObject(newKey)
		return BookPublicationRequestDTO{}, result.Error
	}
	if result.RowsAffected == 0 {
		_ = s.bookUpload.DeleteObject(newKey)
		return BookPublicationRequestDTO{}, apperr.Conflict("books.publication_not_pending", "Evidence can only be uploaded for a pending publication request")
	}
	if oldKey != "" && oldKey != newKey {
		_ = s.bookUpload.DeleteObject(oldKey)
	}
	return buildBookPublicationRequestDTO(s.db, request), nil
}

func (s *Service) OpenPublicationEvidence(user authctx.CurrentUser, requestID uuid.UUID) (BookPublicationEvidenceDTO, io.ReadCloser, error) {
	if user.ID == uuid.Nil {
		return BookPublicationEvidenceDTO{}, nil, apperr.Unauthorized("Login required")
	}
	var request model.BookPublicationRequest
	if err := s.db.Where("id = ?", requestID).First(&request).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BookPublicationEvidenceDTO{}, nil, apperr.NotFound("books.publication_request_not_found", "Publication request not found")
		}
		return BookPublicationEvidenceDTO{}, nil, err
	}
	if request.SubmittedBy != user.ID && !authctx.RoleAtLeast(user.Role, authctx.RoleModerator) {
		return BookPublicationEvidenceDTO{}, nil, apperr.Forbidden("books.evidence_forbidden", "Evidence access is not allowed")
	}
	var rights model.BookRightsDeclaration
	if err := s.db.Where("request_id = ?", request.ID).First(&rights).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return BookPublicationEvidenceDTO{}, nil, apperr.NotFound("books.rights_declaration_not_found", "Rights declaration not found")
		}
		return BookPublicationEvidenceDTO{}, nil, err
	}
	if strings.TrimSpace(rights.EvidenceObjectKey) == "" {
		return BookPublicationEvidenceDTO{}, nil, apperr.NotFound("books.evidence_not_found", "Publication evidence not found")
	}
	if err := requireBookUploadStore(s.bookUpload); err != nil {
		return BookPublicationEvidenceDTO{}, nil, err
	}
	reader, err := s.bookUpload.OpenObject(rights.EvidenceObjectKey)
	if err != nil {
		return BookPublicationEvidenceDTO{}, nil, apperr.Wrap(503, "storage.unavailable", "Storage is unavailable", err)
	}
	return BookPublicationEvidenceDTO{RequestID: request.ID.String(), FileName: rights.EvidenceFileName, ContentType: rights.EvidenceContentType, Size: rights.EvidenceSizeBytes}, reader, nil
}

func validateBookPublicationEvidence(fileName, contentType string, size int64) (string, string, error) {
	fileName = strings.TrimSpace(fileName)
	if fileName == "" || len(fileName) > 255 || strings.ContainsAny(fileName, `/\\`) || strings.IndexFunc(fileName, unicode.IsControl) >= 0 {
		return "", "", apperr.BadRequest("validation.invalid_request", "evidence file name is invalid")
	}
	contentType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return "", "", apperr.BadRequest("validation.invalid_request", "evidence content type is invalid")
	}
	contentType = strings.ToLower(contentType)
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".pdf":
		if contentType != "application/pdf" {
			return "", "", apperr.BadRequest("validation.invalid_request", "evidence file type does not match its extension")
		}
	case ".epub":
		if contentType != "application/epub+zip" && contentType != "application/zip" {
			return "", "", apperr.BadRequest("validation.invalid_request", "evidence file type does not match its extension")
		}
		contentType = "application/epub+zip"
	default:
		return "", "", apperr.BadRequest("validation.invalid_request", "only EPUB and PDF evidence files are supported")
	}
	if size <= 0 || size > bookPublicationEvidenceMaxSize {
		return "", "", apperr.BadRequest("validation.invalid_request", "evidence file size is invalid")
	}
	return filepath.Base(fileName), contentType, nil
}

func validateBookPublicationEvidenceContent(contentType string, data []byte) error {
	switch contentType {
	case "application/pdf":
		if len(data) < len("%PDF-") || string(data[:len("%PDF-")]) != "%PDF-" {
			return apperr.BadRequest("validation.invalid_request", "evidence PDF signature is invalid")
		}
	case "application/epub+zip":
		if _, err := validateBookEPUB(bytes.NewReader(data), int64(len(data))); err != nil {
			return apperr.BadRequest("validation.invalid_request", "evidence EPUB structure is invalid")
		}
	default:
		return apperr.BadRequest("validation.invalid_request", "evidence file type is not supported")
	}
	return nil
}

func bookPublicationEvidenceExtension(contentType string) string {
	if contentType == "application/epub+zip" {
		return "epub"
	}
	return "pdf"
}

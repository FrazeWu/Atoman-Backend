package music

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func archiveContentType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "zip":
		return "application/zip"
	case "rar":
		return "application/vnd.rar"
	case "7z":
		return "application/x-7z-compressed"
	case "tar":
		return "application/x-tar"
	case "tar.gz", "tgz":
		return "application/gzip"
	case "tar.bz2":
		return "application/x-bzip2"
	case "tar.xz":
		return "application/x-xz"
	default:
		return "application/octet-stream"
	}
}
func (s *Service) UploadAlbumImportArchive(user authctx.CurrentUser, id uuid.UUID, archiveName string, reader io.Reader) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	archiveName = strings.TrimSpace(archiveName)
	role, format, formatErr := detectAlbumImportFileRole(archiveName)
	if archiveName == "" || formatErr != nil || role != AlbumImportFileRoleArchive {
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "archive format is not supported")
	}
	if _, err := s.GetAlbumImportSessionForUser(user, id); err != nil {
		return model.AlbumImportSession{}, err
	}

	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return model.AlbumImportSession{}, err
	}
	archiveID := uuid.New()
	objectKey := albumImportSourceKey(user.ID, id, archiveID, format)
	limited := &albumImportLimitedReader{reader: reader, limit: albumImportUploadLimitsFromEnv().MaxFileBytes}
	contentType := archiveContentType(format)
	err := s.albumImportMultipart.PutObject(objectKey, contentType, limited)
	if err != nil {
		if cleanupErr := s.deleteAlbumImportSessionObjectOrRecord(id, objectKey); cleanupErr != nil {
			return model.AlbumImportSession{}, cleanupErr
		}
		return model.AlbumImportSession{}, err
	}
	if limited.exceeded {
		if cleanupErr := s.deleteAlbumImportSessionObjectOrRecord(id, objectKey); cleanupErr != nil {
			return model.AlbumImportSession{}, cleanupErr
		}
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "archive file size is invalid")
	}
	if limited.count <= 0 {
		if cleanupErr := s.deleteAlbumImportSessionObjectOrRecord(id, objectKey); cleanupErr != nil {
			return model.AlbumImportSession{}, cleanupErr
		}
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "archive file size is invalid")
	}

	var out model.AlbumImportSession
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload {
			return apperr.Unprocessable("music.import_invalid_status", "Import session is not pending upload")
		}

		file := model.AlbumImportFile{Base: model.Base{ID: archiveID}, ImportID: session.ID, RelativePath: archiveName, FileName: archiveName, Role: AlbumImportFileRoleArchive, DetectedFormat: format, ContentType: contentType, SourceKey: objectKey, Size: limited.count, UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending, CompletedPartsJSON: "[]", CleanupJSON: "[]", MetadataJSON: "{}"}
		if err := tx.Create(&file).Error; err != nil {
			return err
		}
		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		payload["archive_name"] = archiveName
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.PayloadJSON = string(payloadJSON)
		if err := queueAlbumImportSession(tx, &session, true); err != nil {
			return err
		}
		out = session
		return nil
	})
	if err != nil {
		if cleanupErr := s.deleteAlbumImportSessionObjectOrRecord(id, objectKey); cleanupErr != nil {
			return model.AlbumImportSession{}, cleanupErr
		}
		return model.AlbumImportSession{}, err
	}
	return out, nil
}

type albumImportLimitedReader struct {
	reader       io.Reader
	limit, count int64
	exceeded     bool
}

func (r *albumImportLimitedReader) Read(p []byte) (int, error) {
	if r.exceeded {
		return 0, errors.New("album import archive exceeds size limit")
	}
	remaining := r.limit - r.count
	if remaining <= 0 {
		var one [1]byte
		n, err := r.reader.Read(one[:])
		if n > 0 {
			r.exceeded = true
			return 0, errors.New("album import archive exceeds size limit")
		}
		return n, err
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.reader.Read(p)
	r.count += int64(n)
	return n, err
}

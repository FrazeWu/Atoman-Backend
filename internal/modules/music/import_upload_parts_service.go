package music

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) CreateAlbumImportFilePartUpload(user authctx.CurrentUser, sessionID, fileID uuid.UUID, partNumber int, _ CreateAlbumImportMultipartPartInput) (AlbumImportMultipartPartUploadDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportMultipartPartUploadDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	if partNumber <= 0 {
		return AlbumImportMultipartPartUploadDTO{}, apperr.BadRequest("validation.invalid_request", "part number is invalid")
	}
	session, file, err := loadAlbumImportFileForUser(s.db, user.ID, sessionID, fileID)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	if session.Status != AlbumImportStatusUploading || file.UploadStatus != AlbumImportFileUploadStatusUploading || file.UploadID == "" || file.SourceKey == "" {
		return AlbumImportMultipartPartUploadDTO{}, apperr.Unprocessable("music.import_invalid_status", "Album import file is not uploading")
	}
	uploadURL, err := s.albumImportMultipart.PresignUploadPart(file.SourceKey, file.UploadID, partNumber, 15*time.Minute)
	if err != nil {
		return AlbumImportMultipartPartUploadDTO{}, err
	}
	return AlbumImportMultipartPartUploadDTO{PartNumber: partNumber, UploadURL: uploadURL}, nil
}

func (s *Service) CompleteAlbumImportFilePart(user authctx.CurrentUser, sessionID, fileID uuid.UUID, partNumber int, input CompleteAlbumImportMultipartPartInput) (AlbumImportFileDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportFileDTO{}, apperr.Unauthorized("Login required")
	}
	etag := strings.TrimSpace(input.ETag)
	if partNumber <= 0 || etag == "" || input.Size <= 0 {
		return AlbumImportFileDTO{}, apperr.BadRequest("validation.invalid_request", "completed part is invalid")
	}

	var out model.AlbumImportFile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, file, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusUploading || file.UploadStatus != AlbumImportFileUploadStatusUploading || file.UploadID == "" {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file is not uploading")
		}
		parts, err := albumImportFileCompletedParts(file)
		if err != nil {
			return err
		}
		replaced := false
		for index := range parts {
			if parts[index].PartNumber == partNumber {
				parts[index] = AlbumImportMultipartPartDTO{PartNumber: partNumber, ETag: etag, Size: input.Size}
				replaced = true
				break
			}
		}
		if !replaced {
			parts = append(parts, AlbumImportMultipartPartDTO{PartNumber: partNumber, ETag: etag, Size: input.Size})
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
		encoded, err := json.Marshal(parts)
		if err != nil {
			return err
		}
		file.CompletedPartsJSON = string(encoded)
		if err := tx.Save(&file).Error; err != nil {
			return err
		}
		out = file
		return nil
	})
	if err != nil {
		return AlbumImportFileDTO{}, err
	}
	return buildAlbumImportFileDTO(out), nil
}

func (s *Service) CompleteAlbumImportFile(user authctx.CurrentUser, sessionID, fileID uuid.UUID) (AlbumImportFileDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportFileDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportFileDTO{}, err
	}

	var upload model.AlbumImportFile
	var parts []AlbumImportMultipartPartDTO
	alreadyUploaded := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, file, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		if file.UploadStatus == AlbumImportFileUploadStatusUploaded {
			upload = file
			alreadyUploaded = true
			return nil
		}
		if session.Status != AlbumImportStatusUploading || (file.UploadStatus != AlbumImportFileUploadStatusUploading && file.UploadStatus != AlbumImportFileUploadStatusCompleting) || file.UploadID == "" || file.SourceKey == "" {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file is not uploading")
		}
		parts, err = albumImportFileCompletedParts(file)
		if err != nil {
			return err
		}
		if len(parts) == 0 {
			return apperr.Unprocessable("music.import_invalid_status", "Multipart upload is incomplete")
		}
		if file.UploadStatus == AlbumImportFileUploadStatusUploading {
			file.UploadStatus = AlbumImportFileUploadStatusCompleting
			if err := tx.Save(&file).Error; err != nil {
				return err
			}
		}
		upload = file
		return nil
	})
	if err != nil {
		return AlbumImportFileDTO{}, err
	}
	if alreadyUploaded {
		return buildAlbumImportFileDTO(upload), nil
	}
	if err := s.reconcileAlbumImportObject(upload, parts); err != nil {
		if errors.Is(err, errAlbumImportObjectSizeMismatch) {
			if err := s.failAlbumImportFileUpload(user.ID, sessionID, upload, "uploaded file size does not match"); err != nil {
				return AlbumImportFileDTO{}, err
			}
			s.deleteAlbumImportObjectOrRecord(upload)
			return AlbumImportFileDTO{}, apperr.Unprocessable("music.import_file_size_mismatch", "Uploaded file size does not match")
		}
		return AlbumImportFileDTO{}, err
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, file, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusUploading || file.UploadStatus != AlbumImportFileUploadStatusCompleting {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot finish upload")
		}
		file.UploadStatus = AlbumImportFileUploadStatusUploaded
		file.ErrorMessage = ""
		if err := tx.Save(&file).Error; err != nil {
			return err
		}
		if err := refreshAlbumImportUploadProgress(tx, &session); err != nil {
			return err
		}
		upload = file
		return nil
	})
	if err != nil {
		return AlbumImportFileDTO{}, err
	}
	return buildAlbumImportFileDTO(upload), nil
}

func (s *Service) failAlbumImportFileUpload(userID, sessionID uuid.UUID, upload model.AlbumImportFile, message string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, sessionID, userID)
		if err != nil {
			return err
		}
		result := tx.Model(&model.AlbumImportFile{}).Where("id = ? AND import_id = ? AND source_key = ? AND upload_status = ?", upload.ID, sessionID, upload.SourceKey, AlbumImportFileUploadStatusCompleting).Updates(map[string]any{
			"upload_status": AlbumImportFileUploadStatusFailed, "error_message": message,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file upload changed")
		}
		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		payload["error_message"] = message
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		session.Status = AlbumImportStatusFailed
		session.Stage = AlbumImportStageFailed
		session.ErrorMessage = message
		expiresAt := albumImportFailureExpiresAt()
		session.ExpiresAt = &expiresAt
		session.PayloadJSON = string(payloadJSON)
		return tx.Save(&session).Error
	})
}

func (s *Service) reconcileAlbumImportObject(file model.AlbumImportFile, parts []AlbumImportMultipartPartDTO) error {
	size, headErr := s.albumImportMultipart.ObjectSize(file.SourceKey)
	if headErr != nil {
		if err := s.albumImportMultipart.CompleteMultipartUpload(file.SourceKey, file.UploadID, parts); err != nil {
			if recoveredSize, recoveredErr := s.albumImportMultipart.ObjectSize(file.SourceKey); recoveredErr == nil {
				size = recoveredSize
			} else {
				return err
			}
		} else {
			var err error
			size, err = s.albumImportMultipart.ObjectSize(file.SourceKey)
			if err != nil {
				return err
			}
		}
	}
	if size != file.Size {
		return errAlbumImportObjectSizeMismatch
	}
	return nil
}

func (s *Service) deleteAlbumImportObjectOrRecord(file model.AlbumImportFile) {
	if err := s.albumImportMultipart.DeleteObject(file.SourceKey); err != nil {
		_ = recordAlbumImportCleanupTarget(s.db, file.ID, albumImportCleanupTarget{Action: "delete", Key: file.SourceKey})
	}
}

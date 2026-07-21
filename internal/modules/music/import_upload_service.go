package music

import (
	"encoding/json"
	"errors"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultAlbumImportMaxTotalBytes int64 = 10 * 1024 * 1024 * 1024
	defaultAlbumImportMaxFileBytes  int64 = 4 * 1024 * 1024 * 1024
	defaultAlbumImportMaxFiles            = 5000
	defaultAlbumImportMaxTracks           = 300

	albumImportMaxTotalBytesEnv = "MUSIC_ALBUM_IMPORT_MAX_TOTAL_BYTES"
	albumImportMaxFileBytesEnv  = "MUSIC_ALBUM_IMPORT_MAX_FILE_BYTES"
	albumImportMaxFilesEnv      = "MUSIC_ALBUM_IMPORT_MAX_FILES"
	albumImportMaxTracksEnv     = "MUSIC_ALBUM_IMPORT_MAX_TRACKS"
)

type albumImportUploadLimits struct {
	MaxTotalBytes int64
	MaxFileBytes  int64
	MaxFiles      int
	MaxTracks     int
}

type normalizedAlbumImportFile struct {
	AlbumImportFileInput
	Role   string
	Format string
}

type albumImportCleanupTarget struct {
	Action   string `json:"action"`
	Key      string `json:"key"`
	UploadID string `json:"uploadId,omitempty"`
}

var errAlbumImportObjectSizeMismatch = errors.New("album import object size mismatch")

func albumImportUploadLimitsFromEnv() albumImportUploadLimits {
	return albumImportUploadLimits{
		MaxTotalBytes: positiveInt64Env(albumImportMaxTotalBytesEnv, defaultAlbumImportMaxTotalBytes),
		MaxFileBytes:  positiveInt64Env(albumImportMaxFileBytesEnv, defaultAlbumImportMaxFileBytes),
		MaxFiles:      int(positiveInt64Env(albumImportMaxFilesEnv, defaultAlbumImportMaxFiles)),
		MaxTracks:     int(positiveInt64Env(albumImportMaxTracksEnv, defaultAlbumImportMaxTracks)),
	}
}

func positiveInt64Env(key string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func detectAlbumImportFileRole(fileName string) (string, string, error) {
	lower := strings.ToLower(strings.TrimSpace(fileName))
	archives := []string{"tar.bz2", "tar.gz", "tar.xz", "tgz", "zip", "rar", "7z", "tar"}
	for _, format := range archives {
		if strings.HasSuffix(lower, "."+format) {
			return AlbumImportFileRoleArchive, format, nil
		}
	}
	audio := map[string]bool{
		"mp3": true, "flac": true, "wav": true, "m4a": true, "aac": true, "ogg": true,
		"opus": true, "aiff": true, "aif": true, "wma": true, "ape": true, "alac": true,
	}
	covers := map[string]bool{
		"jpg": true, "jpeg": true, "png": true, "webp": true, "avif": true,
		"heic": true, "heif": true, "tiff": true, "tif": true, "bmp": true,
	}
	extension := strings.TrimPrefix(path.Ext(lower), ".")
	switch {
	case audio[extension]:
		return AlbumImportFileRoleAudio, extension, nil
	case extension == "cue":
		return AlbumImportFileRoleCue, extension, nil
	case covers[extension]:
		return AlbumImportFileRoleCover, extension, nil
	default:
		return "", "", apperr.BadRequest("validation.invalid_request", "unsupported album import file type")
	}
}

func normalizeAlbumImportFiles(input []AlbumImportFileInput, limits albumImportUploadLimits) ([]normalizedAlbumImportFile, string, error) {
	if len(input) == 0 || len(input) > limits.MaxFiles {
		return nil, "", apperr.BadRequest("validation.invalid_request", "album import file count is invalid")
	}

	files := make([]normalizedAlbumImportFile, 0, len(input))
	var totalSize int64
	hasFolder := false
	archiveCount := 0
	trackCount := 0
	folderRoot := ""
	for _, raw := range input {
		fileName := strings.TrimSpace(raw.FileName)
		relativePath := strings.ReplaceAll(strings.TrimSpace(raw.RelativePath), `\`, "/")
		if fileName == "" || relativePath == "" || strings.ContainsAny(fileName, `/\`) {
			return nil, "", apperr.BadRequest("validation.invalid_request", "album import file name is invalid")
		}
		if isAbsoluteAlbumImportPath(relativePath) {
			return nil, "", apperr.BadRequest("validation.invalid_request", "album import relative path is invalid")
		}
		for _, segment := range strings.Split(relativePath, "/") {
			if segment == ".." {
				return nil, "", apperr.BadRequest("validation.invalid_request", "album import relative path is invalid")
			}
		}
		relativePath = path.Clean(relativePath)
		if relativePath == "." || path.Base(relativePath) != fileName {
			return nil, "", apperr.BadRequest("validation.invalid_request", "album import relative path is invalid")
		}
		if raw.FileSize <= 0 || raw.FileSize > limits.MaxFileBytes || totalSize > limits.MaxTotalBytes-raw.FileSize {
			return nil, "", apperr.BadRequest("validation.invalid_request", "album import file size is invalid")
		}
		role, format, err := detectAlbumImportFileRole(fileName)
		if err != nil {
			return nil, "", err
		}
		if role == AlbumImportFileRoleArchive {
			archiveCount++
		} else if role == AlbumImportFileRoleAudio {
			trackCount++
			if trackCount > limits.MaxTracks {
				return nil, "", apperr.BadRequest("validation.invalid_request", "album import track count is invalid")
			}
		}
		totalSize += raw.FileSize
		hasFolder = hasFolder || strings.Contains(relativePath, "/")
		files = append(files, normalizedAlbumImportFile{
			AlbumImportFileInput: AlbumImportFileInput{
				RelativePath: relativePath,
				FileName:     fileName,
				FileSize:     raw.FileSize,
				ContentType:  strings.TrimSpace(raw.ContentType),
			},
			Role: role, Format: format,
		})
	}

	if archiveCount > 0 {
		if archiveCount != 1 || len(files) != 1 {
			return nil, "", apperr.BadRequest("validation.invalid_request", "archive cannot be mixed with other files")
		}
		return files, AlbumImportInputModeArchive, nil
	}
	if hasFolder {
		for _, file := range files {
			segments := strings.Split(file.RelativePath, "/")
			if len(segments) < 2 {
				return nil, "", apperr.BadRequest("validation.invalid_request", "folder import must use one album root")
			}
			if folderRoot == "" {
				folderRoot = segments[0]
			} else if segments[0] != folderRoot {
				return nil, "", apperr.BadRequest("validation.invalid_request", "folder import must use one album root")
			}
		}
		return files, AlbumImportInputModeFolder, nil
	}
	return files, AlbumImportInputModeFiles, nil
}

func isAbsoluteAlbumImportPath(value string) bool {
	if strings.HasPrefix(value, "/") {
		return true
	}
	return len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && value[2] == '/'
}

func albumImportSourceKey(userID, sessionID, fileID uuid.UUID, format string) string {
	return "music/album-imports/source/users/" + userID.String() + "/sessions/" + sessionID.String() + "/files/" + fileID.String() + "/objects/" + uuid.NewString() + "/source." + format
}

func (s *Service) RegisterAlbumImportFiles(user authctx.CurrentUser, id uuid.UUID, input RegisterAlbumImportFilesInput) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return model.AlbumImportSession{}, err
	}
	files, inputMode, err := normalizeAlbumImportFiles(input.Files, albumImportUploadLimitsFromEnv())
	if err != nil {
		return model.AlbumImportSession{}, err
	}

	createdUploads := make([]model.AlbumImportFile, 0, len(files))
	var out model.AlbumImportSession
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload || len(session.Files) != 0 {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot register files")
		}

		for _, descriptor := range files {
			file := model.AlbumImportFile{
				ImportID:           session.ID,
				RelativePath:       descriptor.RelativePath,
				FileName:           descriptor.FileName,
				Role:               descriptor.Role,
				DetectedFormat:     descriptor.Format,
				ContentType:        descriptor.ContentType,
				Size:               descriptor.FileSize,
				PartSize:           albumImportMultipartPartSize,
				CompletedPartsJSON: "[]",
				CleanupJSON:        "[]",
				UploadStatus:       AlbumImportFileUploadStatusUploading,
				ProcessingStatus:   AlbumImportFileProcessingStatusPending,
				MetadataJSON:       "{}",
			}
			file.ID = uuid.New()
			file.SourceKey = albumImportSourceKey(user.ID, session.ID, file.ID, descriptor.Format)
			if err := tx.Create(&file).Error; err != nil {
				return err
			}
			createdUploads = append(createdUploads, file)
		}
		session.InputMode = inputMode
		session.Stage = AlbumImportStageUpload
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		out.Files = createdUploads
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	for index := range createdUploads {
		contentType := createdUploads[index].ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		uploadID, createErr := s.albumImportMultipart.CreateMultipartUpload(createdUploads[index].SourceKey, contentType)
		if createErr != nil {
			for _, file := range createdUploads[:index] {
				s.abortAlbumImportMultipartOrRecord(id, file)
			}
			if err := s.failRegisteredAlbumImportFiles(user.ID, id, createdUploads, createErr.Error()); err != nil {
				return model.AlbumImportSession{}, err
			}
			return model.AlbumImportSession{}, createErr
		}
		createdUploads[index].UploadID = uploadID
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, id, user.ID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot register files")
		}
		var totalSize int64
		for _, file := range createdUploads {
			result := tx.Model(&model.AlbumImportFile{}).Where("id = ? AND import_id = ? AND upload_id = ?", file.ID, id, "").Update("upload_id", file.UploadID)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return apperr.Unprocessable("music.import_invalid_status", "Import session cannot register files")
			}
			totalSize += file.Size
		}
		session.Status = AlbumImportStatusUploading
		session.Stage = AlbumImportStageUpload
		session.ProgressCurrent = 0
		session.ProgressTotal = totalSize
		if err := tx.Save(&session).Error; err != nil {
			return err
		}
		out = session
		out.Files = createdUploads
		return nil
	})
	if err != nil {
		for _, file := range createdUploads {
			s.abortAlbumImportMultipartOrRecord(id, file)
		}
		return model.AlbumImportSession{}, err
	}
	return out, nil
}

func (s *Service) failRegisteredAlbumImportFiles(userID, sessionID uuid.UUID, files []model.AlbumImportFile, message string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, sessionID, userID)
		if err != nil {
			return err
		}
		if err := tx.Model(&model.AlbumImportFile{}).Where("import_id = ?", sessionID).Updates(map[string]any{"upload_status": AlbumImportFileUploadStatusFailed, "error_message": message}).Error; err != nil {
			return err
		}
		session.Status, session.Stage, session.ErrorMessage = AlbumImportStatusFailed, AlbumImportStageFailed, message
		return tx.Save(&session).Error
	})
}

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

func (s *Service) CompleteAlbumImportSession(user authctx.CurrentUser, sessionID uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, sessionID, user.ID)
		if err != nil {
			return err
		}
		if session.Status == AlbumImportStatusCanceled || session.Status == AlbumImportStatusCommitted {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot be queued")
		}
		if session.Status == AlbumImportStatusQueued {
			return nil
		}
		if session.Status != AlbumImportStatusUploading {
			return apperr.Unprocessable("music.import_invalid_status", "Import session cannot be queued")
		}
		if len(session.Files) == 0 {
			return apperr.Unprocessable("music.import_invalid_status", "Album import has no files")
		}
		hasImportSource := false
		for _, file := range session.Files {
			if file.UploadStatus != AlbumImportFileUploadStatusUploaded {
				return apperr.Unprocessable("music.import_invalid_status", "Album import uploads are incomplete")
			}
			hasImportSource = hasImportSource || file.Role == AlbumImportFileRoleArchive || file.Role == AlbumImportFileRoleAudio
		}
		if !hasImportSource {
			return apperr.BadRequest("validation.invalid_request", "Album import requires an archive or audio file")
		}
		if err := queueAlbumImportSession(tx, &session, true); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	return loadAlbumImportSession(s.db, sessionID, &user.ID)
}

func (s *Service) CancelAlbumImportSession(user authctx.CurrentUser, sessionID uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	var files []model.AlbumImportFile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, err := loadAlbumImportSessionForUpdate(tx, sessionID, user.ID)
		if err != nil {
			return err
		}
		if session.Status == AlbumImportStatusCommitted {
			return apperr.Unprocessable("music.import_invalid_status", "Committed import cannot be canceled")
		}
		if session.Status == AlbumImportStatusCanceled {
			files = nil
			return nil
		}
		for _, file := range session.Files {
			if file.UploadStatus == AlbumImportFileUploadStatusCompleting {
				return apperr.Unprocessable("music.import_invalid_status", "Import session has a file completing upload")
			}
		}
		for _, file := range session.Files {
			if file.UploadStatus != AlbumImportFileUploadStatusUploaded && file.SourceKey != "" && file.UploadID != "" {
				if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
					return err
				}
				break
			}
		}
		files = append([]model.AlbumImportFile{}, session.Files...)
		expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
		if err := tx.Model(&model.AlbumImportSession{}).Where("id = ? AND user_id = ?", sessionID, user.ID).Updates(map[string]any{
			"status": AlbumImportStatusCanceled, "stage": AlbumImportStageCanceled, "error_message": "", "expires_at": expiresAt,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.AlbumImportJob{}).Where("import_id = ?", sessionID).Updates(map[string]any{
			"status": AlbumImportJobStatusCanceled, "stage": AlbumImportStageCanceled,
			"locked_by": "", "locked_at": nil, "heartbeat_at": nil, "finished_at": nil,
		}).Error
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	if s.albumImportMultipart != nil {
		for _, file := range files {
			if file.UploadStatus != AlbumImportFileUploadStatusUploaded && file.SourceKey != "" && file.UploadID != "" {
				s.cleanupAlbumImportObject(file)
			}
		}
	}
	return loadAlbumImportSession(s.db, sessionID, &user.ID)
}

func (s *Service) DeleteAlbumImportFile(user authctx.CurrentUser, sessionID, fileID uuid.UUID) error {
	if user.ID == uuid.Nil {
		return apperr.Unauthorized("Login required")
	}
	var file model.AlbumImportFile
	err := s.db.Transaction(func(tx *gorm.DB) error {
		session, lockedFile, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		if session.Status != AlbumImportStatusPendingUpload && session.Status != AlbumImportStatusUploading {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be deleted after processing starts")
		}
		if lockedFile.UploadStatus == AlbumImportFileUploadStatusCompleting {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file is completing upload")
		}
		file = lockedFile
		if err := tx.Delete(&model.AlbumImportFile{}, "id = ? AND import_id = ?", fileID, sessionID).Error; err != nil {
			return err
		}
		return refreshAlbumImportFileSet(tx, &session)
	})
	if err != nil {
		return err
	}
	s.cleanupAlbumImportObject(file)
	return nil
}

func (s *Service) RetryAlbumImportFile(user authctx.CurrentUser, sessionID, fileID uuid.UUID) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	session, file, err := loadAlbumImportFileForUser(s.db, user.ID, sessionID, fileID)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	if file.UploadStatus == AlbumImportFileUploadStatusFailed {
		return s.retryAlbumImportFileUpload(user, session, file)
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		session, file, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		if (session.Status != AlbumImportStatusFailed && session.Status != AlbumImportStatusNeedsAttention) || file.UploadStatus != AlbumImportFileUploadStatusUploaded || (file.ProcessingStatus != AlbumImportFileProcessingStatusFailed && file.ErrorMessage == "") {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be retried")
		}
		file.ProcessingStatus = AlbumImportFileProcessingStatusPending
		file.ErrorMessage = ""
		if err := tx.Save(&file).Error; err != nil {
			return err
		}
		if err := queueAlbumImportSession(tx, &session, true); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	return loadAlbumImportSession(s.db, sessionID, &user.ID)
}

func (s *Service) retryAlbumImportFileUpload(user authctx.CurrentUser, session model.AlbumImportSession, file model.AlbumImportFile) (model.AlbumImportSession, error) {
	if session.Status != AlbumImportStatusFailed && session.Status != AlbumImportStatusNeedsAttention {
		return model.AlbumImportSession{}, apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be retried")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return model.AlbumImportSession{}, err
	}
	newSourceKey := albumImportSourceKey(user.ID, session.ID, file.ID, file.DetectedFormat)
	contentType := strings.TrimSpace(file.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	newUploadID, err := s.albumImportMultipart.CreateMultipartUpload(newSourceKey, contentType)
	if err != nil {
		return model.AlbumImportSession{}, err
	}
	oldFile := file
	err = s.db.Transaction(func(tx *gorm.DB) error {
		lockedSession, lockedFile, err := loadAlbumImportFileForUser(tx, user.ID, session.ID, file.ID)
		if err != nil {
			return err
		}
		if (lockedSession.Status != AlbumImportStatusFailed && lockedSession.Status != AlbumImportStatusNeedsAttention) || lockedFile.UploadStatus != AlbumImportFileUploadStatusFailed || lockedFile.SourceKey != file.SourceKey {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be retried")
		}
		lockedFile.SourceKey = newSourceKey
		lockedFile.UploadID = newUploadID
		lockedFile.PartSize = albumImportMultipartPartSize
		lockedFile.CompletedPartsJSON = "[]"
		lockedFile.UploadStatus = AlbumImportFileUploadStatusUploading
		lockedFile.ProcessingStatus = AlbumImportFileProcessingStatusPending
		lockedFile.ErrorMessage = ""
		if err := tx.Save(&lockedFile).Error; err != nil {
			return err
		}
		payload, err := readAlbumImportPayloadMap(lockedSession.PayloadJSON)
		if err != nil {
			return err
		}
		delete(payload, "error_message")
		payloadJSON, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		lockedSession.Status = AlbumImportStatusUploading
		lockedSession.Stage = AlbumImportStageUpload
		lockedSession.ErrorMessage = ""
		lockedSession.PayloadJSON = string(payloadJSON)
		return refreshAlbumImportUploadProgress(tx, &lockedSession)
	})
	if err != nil {
		s.abortAlbumImportMultipartOrRecord(session.ID, model.AlbumImportFile{SourceKey: newSourceKey, UploadID: newUploadID})
		return model.AlbumImportSession{}, err
	}
	s.cleanupAlbumImportObject(oldFile)
	return loadAlbumImportSession(s.db, session.ID, &user.ID)
}

func (s *Service) ReplaceAlbumImportFile(user authctx.CurrentUser, sessionID, fileID uuid.UUID, input AlbumImportFileInput) (AlbumImportFileDTO, error) {
	if user.ID == uuid.Nil {
		return AlbumImportFileDTO{}, apperr.Unauthorized("Login required")
	}
	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return AlbumImportFileDTO{}, err
	}
	session, existing, err := loadAlbumImportFileForUser(s.db, user.ID, sessionID, fileID)
	if err != nil {
		return AlbumImportFileDTO{}, err
	}
	if session.Status != AlbumImportStatusPendingUpload && session.Status != AlbumImportStatusUploading && session.Status != AlbumImportStatusFailed && session.Status != AlbumImportStatusNeedsAttention {
		return AlbumImportFileDTO{}, apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be replaced")
	}
	if existing.UploadStatus == AlbumImportFileUploadStatusCompleting {
		return AlbumImportFileDTO{}, apperr.Unprocessable("music.import_invalid_status", "Album import file is completing upload")
	}
	oldFile := existing
	descriptors := make([]AlbumImportFileInput, 0, len(session.Files))
	for _, file := range session.Files {
		if file.ID == fileID {
			descriptors = append(descriptors, input)
			continue
		}
		descriptors = append(descriptors, AlbumImportFileInput{RelativePath: file.RelativePath, FileName: file.FileName, FileSize: file.Size, ContentType: file.ContentType})
	}
	normalized, _, err := normalizeAlbumImportFiles(descriptors, albumImportUploadLimitsFromEnv())
	if err != nil {
		return AlbumImportFileDTO{}, err
	}
	var replacement normalizedAlbumImportFile
	for index, file := range session.Files {
		if file.ID == fileID {
			replacement = normalized[index]
			break
		}
	}
	newSourceKey := albumImportSourceKey(user.ID, sessionID, fileID, replacement.Format)
	contentType := replacement.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	newUploadID, err := s.albumImportMultipart.CreateMultipartUpload(newSourceKey, contentType)
	if err != nil {
		return AlbumImportFileDTO{}, err
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		lockedSession, file, err := loadAlbumImportFileForUser(tx, user.ID, sessionID, fileID)
		if err != nil {
			return err
		}
		if lockedSession.Status != AlbumImportStatusPendingUpload && lockedSession.Status != AlbumImportStatusUploading && lockedSession.Status != AlbumImportStatusFailed && lockedSession.Status != AlbumImportStatusNeedsAttention {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file cannot be replaced")
		}
		if file.UploadStatus == AlbumImportFileUploadStatusCompleting {
			return apperr.Unprocessable("music.import_invalid_status", "Album import file is completing upload")
		}
		lockedDescriptors := make([]AlbumImportFileInput, 0, len(lockedSession.Files))
		for _, current := range lockedSession.Files {
			if current.ID == fileID {
				lockedDescriptors = append(lockedDescriptors, input)
			} else {
				lockedDescriptors = append(lockedDescriptors, AlbumImportFileInput{RelativePath: current.RelativePath, FileName: current.FileName, FileSize: current.Size, ContentType: current.ContentType})
			}
		}
		_, lockedInputMode, err := normalizeAlbumImportFiles(lockedDescriptors, albumImportUploadLimitsFromEnv())
		if err != nil {
			return err
		}
		oldFile = file
		file.RelativePath = replacement.RelativePath
		file.FileName = replacement.FileName
		file.Role = replacement.Role
		file.DetectedFormat = replacement.Format
		file.ContentType = replacement.ContentType
		file.Size = replacement.FileSize
		file.SourceKey = newSourceKey
		file.UploadID = newUploadID
		file.PartSize = albumImportMultipartPartSize
		file.CompletedPartsJSON = "[]"
		file.UploadStatus = AlbumImportFileUploadStatusUploading
		file.ProcessingStatus = AlbumImportFileProcessingStatusPending
		file.PlaybackKey = ""
		file.ErrorMessage = ""
		if err := tx.Save(&file).Error; err != nil {
			return err
		}
		lockedSession.InputMode = lockedInputMode
		lockedSession.Status = AlbumImportStatusUploading
		lockedSession.Stage = AlbumImportStageUpload
		lockedSession.ErrorMessage = ""
		if err := refreshAlbumImportUploadProgress(tx, &lockedSession); err != nil {
			return err
		}
		existing = file
		return nil
	})
	if err != nil {
		s.abortAlbumImportMultipartOrRecord(sessionID, model.AlbumImportFile{SourceKey: newSourceKey, UploadID: newUploadID})
		return AlbumImportFileDTO{}, err
	}
	s.cleanupAlbumImportObject(oldFile)
	return buildAlbumImportFileDTO(existing), nil
}

func loadAlbumImportFileForUser(db *gorm.DB, userID, sessionID, fileID uuid.UUID) (model.AlbumImportSession, model.AlbumImportFile, error) {
	session, err := loadAlbumImportSessionForUpdate(db, sessionID, userID)
	if err != nil {
		return model.AlbumImportSession{}, model.AlbumImportFile{}, err
	}
	var file model.AlbumImportFile
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&file, "id = ? AND import_id = ?", fileID, sessionID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return model.AlbumImportSession{}, model.AlbumImportFile{}, apperr.NotFound("music.import_file_not_found", "Album import file not found")
		}
		return model.AlbumImportSession{}, model.AlbumImportFile{}, err
	}
	return session, file, nil
}

func loadAlbumImportSessionForUpdate(db *gorm.DB, sessionID, userID uuid.UUID) (model.AlbumImportSession, error) {
	var session model.AlbumImportSession
	err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Files").Preload("Job").Where("user_id = ?", userID).First(&session, "id = ?", sessionID).Error
	if err == gorm.ErrRecordNotFound {
		return model.AlbumImportSession{}, apperr.NotFound("music.import_not_found", "Import session not found")
	}
	return session, err
}

func (s *Service) cleanupAlbumImportObject(file model.AlbumImportFile) {
	if s.albumImportMultipart == nil || file.SourceKey == "" {
		return
	}
	target := albumImportCleanupTarget{Key: file.SourceKey}
	var err error
	if file.UploadStatus == AlbumImportFileUploadStatusUploaded {
		target.Action = "delete"
		err = s.albumImportMultipart.DeleteObject(file.SourceKey)
	} else if file.UploadID != "" {
		target.Action = "abort"
		target.UploadID = file.UploadID
		err = s.albumImportMultipart.AbortMultipartUpload(file.SourceKey, file.UploadID)
	}
	if err != nil && target.Action != "" {
		_ = recordAlbumImportCleanupTarget(s.db, file.ID, target)
	}
}

func (s *Service) abortAlbumImportMultipartOrRecord(sessionID uuid.UUID, file model.AlbumImportFile) {
	if s.albumImportMultipart == nil || file.SourceKey == "" || file.UploadID == "" {
		return
	}
	if err := s.albumImportMultipart.AbortMultipartUpload(file.SourceKey, file.UploadID); err != nil {
		_ = recordAlbumImportSessionCleanupTarget(s.db, sessionID, albumImportCleanupTarget{Action: "abort", Key: file.SourceKey, UploadID: file.UploadID})
	}
}

func recordAlbumImportCleanupTarget(db *gorm.DB, fileID uuid.UUID, target albumImportCleanupTarget) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var file model.AlbumImportFile
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).First(&file, "id = ?", fileID).Error; err != nil {
			return err
		}
		targets := []albumImportCleanupTarget{}
		if strings.TrimSpace(file.CleanupJSON) != "" {
			_ = json.Unmarshal([]byte(file.CleanupJSON), &targets)
		}
		targets = append(targets, target)
		encoded, err := json.Marshal(targets)
		if err != nil {
			return err
		}
		return tx.Unscoped().Model(&model.AlbumImportFile{}).Where("id = ?", fileID).Update("cleanup_json", string(encoded)).Error
	})
}

func recordAlbumImportSessionCleanupTarget(db *gorm.DB, sessionID uuid.UUID, target albumImportCleanupTarget) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var session model.AlbumImportSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, "id = ?", sessionID).Error; err != nil {
			return err
		}
		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		targets := []albumImportCleanupTarget{}
		if rawTargets, ok := payload["cleanup_targets"]; ok {
			raw, _ := json.Marshal(rawTargets)
			_ = json.Unmarshal(raw, &targets)
		}
		targets = append(targets, target)
		payload["cleanup_targets"] = targets
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return tx.Model(&model.AlbumImportSession{}).Where("id = ?", sessionID).Update("payload_json", string(encoded)).Error
	})
}

func albumImportFileCompletedParts(file model.AlbumImportFile) ([]AlbumImportMultipartPartDTO, error) {
	parts := []AlbumImportMultipartPartDTO{}
	if strings.TrimSpace(file.CompletedPartsJSON) == "" {
		return parts, nil
	}
	if err := json.Unmarshal([]byte(file.CompletedPartsJSON), &parts); err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "Completed parts are invalid")
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}

func refreshAlbumImportUploadProgress(tx *gorm.DB, session *model.AlbumImportSession) error {
	var files []model.AlbumImportFile
	if err := tx.Where("import_id = ?", session.ID).Find(&files).Error; err != nil {
		return err
	}
	var current, total int64
	for _, file := range files {
		total += file.Size
		if file.UploadStatus == AlbumImportFileUploadStatusUploaded {
			current += file.Size
		}
	}
	session.ProgressCurrent = current
	session.ProgressTotal = total
	return tx.Save(session).Error
}

func refreshAlbumImportFileSet(tx *gorm.DB, session *model.AlbumImportSession) error {
	var files []model.AlbumImportFile
	if err := tx.Where("import_id = ?", session.ID).Find(&files).Error; err != nil {
		return err
	}
	if len(files) == 0 {
		session.InputMode = AlbumImportInputModeAuto
		session.Status = AlbumImportStatusPendingUpload
		session.ProgressCurrent = 0
		session.ProgressTotal = 0
		return tx.Save(session).Error
	}
	descriptors := make([]AlbumImportFileInput, 0, len(files))
	for _, file := range files {
		descriptors = append(descriptors, AlbumImportFileInput{RelativePath: file.RelativePath, FileName: file.FileName, FileSize: file.Size, ContentType: file.ContentType})
	}
	_, inputMode, err := normalizeAlbumImportFiles(descriptors, albumImportUploadLimitsFromEnv())
	if err != nil {
		return err
	}
	session.InputMode = inputMode
	return refreshAlbumImportUploadProgress(tx, session)
}

func queueAlbumImportSession(tx *gorm.DB, session *model.AlbumImportSession, resetJob bool) error {
	payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return err
	}
	delete(payload, "error_message")
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	session.PayloadJSON = string(payloadJSON)
	session.Status = AlbumImportStatusQueued
	session.Stage = AlbumImportStageQueued
	session.ProgressCurrent = 0
	session.ProgressTotal = 0
	session.ErrorMessage = ""
	if err := tx.Save(session).Error; err != nil {
		return err
	}
	var job model.AlbumImportJob
	err = tx.First(&job, "import_id = ?", session.ID).Error
	if err == gorm.ErrRecordNotFound {
		return tx.Create(&model.AlbumImportJob{ImportID: session.ID, Status: AlbumImportJobStatusQueued, Stage: AlbumImportStageQueued, MaxAttempts: 3}).Error
	}
	if err != nil || !resetJob {
		return err
	}
	job.Status = AlbumImportJobStatusQueued
	job.Stage = AlbumImportStageQueued
	job.Attempts = 0
	job.MaxAttempts = 3
	job.LockedBy = ""
	job.LockedAt = nil
	job.HeartbeatAt = nil
	job.StartedAt = nil
	job.FinishedAt = nil
	job.LastError = ""
	return tx.Save(&job).Error
}

func buildAlbumImportFileDTO(file model.AlbumImportFile) AlbumImportFileDTO {
	parts := []AlbumImportMultipartPartDTO{}
	if strings.TrimSpace(file.CompletedPartsJSON) != "" {
		_ = json.Unmarshal([]byte(file.CompletedPartsJSON), &parts)
	}
	if parts == nil {
		parts = []AlbumImportMultipartPartDTO{}
	}
	return AlbumImportFileDTO{
		ID: file.ID.String(), RelativePath: file.RelativePath, FileName: file.FileName,
		Role: file.Role, DetectedFormat: file.DetectedFormat, ContentType: file.ContentType,
		Size: file.Size, SourceKey: file.SourceKey, PartSize: file.PartSize, CompletedParts: parts,
		PlaybackKey: file.PlaybackKey, UploadStatus: file.UploadStatus,
		ProcessingStatus: file.ProcessingStatus, DiscNumber: file.DiscNumber,
		TrackNumber: file.TrackNumber, Title: file.Title, DurationSeconds: file.DurationSeconds,
		ErrorMessage: file.ErrorMessage,
	}
}

func saveLegacyAlbumImportFile(tx *gorm.DB, file *model.AlbumImportFile, sessionID uuid.UUID, state albumImportMultipartState, contentType string) error {
	if file.ID == uuid.Nil {
		file.ID = uuid.New()
	}
	encodedParts, err := json.Marshal(state.CompletedParts)
	if err != nil {
		return err
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/zip"
	}
	file.ImportID = sessionID
	file.RelativePath = state.FileName
	file.FileName = state.FileName
	file.Role = AlbumImportFileRoleArchive
	file.DetectedFormat = "zip"
	file.ContentType = contentType
	file.Size = state.FileSize
	file.SourceKey = state.ObjectKey
	file.UploadID = state.UploadID
	file.PartSize = state.PartSize
	file.CompletedPartsJSON = string(encodedParts)
	file.UploadStatus = AlbumImportFileUploadStatusUploading
	if file.ProcessingStatus == "" {
		file.ProcessingStatus = AlbumImportFileProcessingStatusPending
	}
	if file.MetadataJSON == "" {
		file.MetadataJSON = "{}"
	}
	if file.CleanupJSON == "" {
		file.CleanupJSON = "[]"
	}
	return tx.Save(file).Error
}

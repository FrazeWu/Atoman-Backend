package music

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"
	"atoman/internal/storage"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func (s *Service) UploadAlbumImportArchive(user authctx.CurrentUser, id uuid.UUID, archiveName string, reader io.Reader) (model.AlbumImportSession, error) {
	if user.ID == uuid.Nil {
		return model.AlbumImportSession{}, apperr.Unauthorized("Login required")
	}
	if strings.TrimSpace(archiveName) == "" {
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "archive file name is required")
	}
	if strings.ToLower(filepath.Ext(strings.TrimSpace(archiveName))) != ".zip" {
		return model.AlbumImportSession{}, apperr.BadRequest("validation.invalid_request", "archive must be a zip file")
	}
	if _, err := s.GetAlbumImportSessionForUser(user, id); err != nil {
		return model.AlbumImportSession{}, err
	}

	if err := requireAlbumImportMultipartStore(s.albumImportMultipart); err != nil {
		return model.AlbumImportSession{}, err
	}
	archiveID := uuid.New()
	objectKey := albumImportSourceKey(user.ID, id, archiveID, "zip")
	limited := &albumImportLimitedReader{reader: reader, limit: albumImportUploadLimitsFromEnv().MaxFileBytes}
	err := s.albumImportMultipart.PutObject(objectKey, "application/zip", limited)
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

		file := model.AlbumImportFile{Base: model.Base{ID: archiveID}, ImportID: session.ID, RelativePath: strings.TrimSpace(archiveName), FileName: strings.TrimSpace(archiveName), Role: AlbumImportFileRoleArchive, DetectedFormat: "zip", ContentType: "application/zip", SourceKey: objectKey, Size: limited.count, UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: AlbumImportFileProcessingStatusPending, CompletedPartsJSON: "[]", CleanupJSON: "[]", MetadataJSON: "{}"}
		if err := tx.Create(&file).Error; err != nil {
			return err
		}
		payload, err := readAlbumImportPayloadMap(session.PayloadJSON)
		if err != nil {
			return err
		}
		payload["archive_name"] = strings.TrimSpace(archiveName)
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

func (s *Service) deriveAlbumImportPayload(user authctx.CurrentUser, archiveName string, body []byte) (map[string]any, error) {
	return s.deriveAlbumImportPayloadFromReader(user, archiveName, bytes.NewReader(body))
}

func (s *Service) deriveAlbumImportPayloadFromReader(user authctx.CurrentUser, archiveName string, body io.Reader) (map[string]any, error) {
	tmpFile, err := os.CreateTemp("", "atoman-album-import-*.zip")
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, body); err != nil {
		tmpFile.Close()
		return nil, err
	}
	if err := tmpFile.Close(); err != nil {
		return nil, err
	}
	return s.deriveAlbumImportPayloadFromZipFile(user, archiveName, tmpPath)
}

func (s *Service) deriveAlbumImportPayloadFromZipFile(user authctx.CurrentUser, archiveName string, zipPath string) (map[string]any, error) {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, apperr.BadRequest("validation.invalid_request", "archive must be a valid zip file")
	}
	defer reader.Close()

	derivedTracks := make([]map[string]any, 0)
	var coverURL string

	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if shouldIgnoreAlbumImportPath(file.Name) {
			continue
		}

		base := filepath.Base(file.Name)
		ext := strings.ToLower(filepath.Ext(base))

		// Check if it's an audio track
		if trackTitle, trackNumber, ok := deriveTrackFromArchiveEntry(file.Name); ok {
			audioKey := ""
			audioURL := ""
			if rc, err := file.Open(); err == nil {
				audioBytes, readErr := io.ReadAll(rc)
				rc.Close()
				if readErr == nil {
					filename := uuid.NewString() + ext
					uploadedURL, uploadedKey, uploadErr := s.storeImportedAudio(user, filename, ext, audioBytes)
					if uploadErr == nil {
						audioURL = uploadedURL
						audioKey = uploadedKey
					}
				}
			}
			derivedTracks = append(derivedTracks, map[string]any{
				"title":        trackTitle,
				"track_number": trackNumber,
				"audio_key":    audioKey,
				"audio_url":    audioURL,
				"origin":       file.Name,
			})
			continue
		}

		// Check if it's a cover image
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" {
			lowerBase := strings.ToLower(base)
			// Prefer files with cover, folder, front in name, or fallback to any first image
			if strings.Contains(lowerBase, "cover") || strings.Contains(lowerBase, "folder") || strings.Contains(lowerBase, "front") || coverURL == "" {
				rc, err := file.Open()
				if err != nil {
					continue
				}
				imgBytes, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					continue
				}

				uploadedURL, err := s.storeImportedCover(user, uuid.NewString()+ext, ext, imgBytes)
				if err != nil {
					continue
				}
				coverURL = uploadedURL
			}
		}
	}

	return map[string]any{
		"archive_name":        archiveName,
		"derived_album_title": strings.TrimSpace(strings.TrimSuffix(archiveName, filepath.Ext(archiveName))),
		"derived_tracks":      derivedTracks,
		"derived_cover":       coverURL,
		"upload_progress":     100,
		"upload_speed":        0,
	}, nil
}

func (s *Service) storeImportedAudio(user authctx.CurrentUser, filename string, ext string, content []byte) (string, string, error) {
	if s.s3 != nil && strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
		urlPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
		if bucket != "" && urlPrefix != "" {
			key := storage.BuildMusicUploadKey("audio", user.ID.String(), filename, time.Now())
			contentType := "audio/mpeg"
			switch strings.ToLower(ext) {
			case ".flac":
				contentType = "audio/flac"
			case ".wav":
				contentType = "audio/wav"
			case ".m4a":
				contentType = "audio/x-m4a"
			case ".aac":
				contentType = "audio/aac"
			case ".ogg":
				contentType = "audio/ogg"
			}
			if _, err := s.s3.PutObject(&s3.PutObjectInput{
				Bucket:      aws.String(bucket),
				Key:         aws.String(key),
				Body:        bytes.NewReader(content),
				ContentType: aws.String(contentType),
				ACL:         aws.String("public-read"),
			}); err == nil {
				return urlPrefix + "/" + key, key, nil
			}
		}
	}
	return "", "", nil
}

func (s *Service) storeImportedCover(user authctx.CurrentUser, filename string, ext string, content []byte) (string, error) {
	if s.s3 != nil && strings.EqualFold(strings.TrimSpace(os.Getenv("STORAGE_TYPE")), "s3") {
		bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
		urlPrefix := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_URL_PREFIX")), "/")
		if bucket != "" && urlPrefix != "" {
			key := storage.BuildMusicUploadKey("covers", user.ID.String(), filename, time.Now())
			contentType := "image/jpeg"
			switch strings.ToLower(ext) {
			case ".png":
				contentType = "image/png"
			case ".webp":
				contentType = "image/webp"
			}
			if _, err := s.s3.PutObject(&s3.PutObjectInput{
				Bucket:      aws.String(bucket),
				Key:         aws.String(key),
				Body:        bytes.NewReader(content),
				ContentType: aws.String(contentType),
				ACL:         aws.String("public-read"),
			}); err == nil {
				return urlPrefix + "/" + key, nil
			}
		}
	}

	destDir := filepath.Join(".", "uploads", "music", "covers")
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", err
	}
	destPath := filepath.Join(destDir, filename)
	if err := os.WriteFile(destPath, content, 0644); err != nil {
		return "", err
	}
	return "/uploads/music/covers/" + filename, nil
}

func deriveTrackFromArchiveEntry(name string) (string, int, bool) {
	if shouldIgnoreAlbumImportPath(name) {
		return "", 0, false
	}
	base := filepath.Base(name)
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".mp3", ".flac", ".wav", ".m4a", ".aac", ".ogg":
	default:
		return "", 0, false
	}

	title, _, trackNumber := albumImportTrackInfoFromFileName(base)
	if title == "" {
		return "", 0, false
	}
	return title, trackNumber, true
}

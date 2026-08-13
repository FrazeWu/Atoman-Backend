package music

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestBuildAlbumImportMultipartDTOSerializesEmptyCompletedPartsAsArray(t *testing.T) {
	dto := buildAlbumImportMultipartDTO(model.AlbumImportSession{}.ID, albumImportMultipartState{})

	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal multipart DTO: %v", err)
	}
	if !strings.Contains(string(body), `"completedParts":[]`) {
		t.Fatalf("expected completedParts to be an array, got %s", body)
	}
}

func TestBuildAlbumImportDTOSerializesV2ListsAsArrays(t *testing.T) {
	dto := buildAlbumImportDTO(model.AlbumImportSession{})
	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal album import DTO: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal album import DTO: %v", err)
	}
	for _, field := range []string{"files", "tracks", "errors"} {
		values, ok := payload[field].([]any)
		if !ok || values == nil {
			t.Fatalf("expected %s to be an array, got %s", field, body)
		}
	}
	for _, field := range []string{"inputMode", "stage", "progress"} {
		if _, ok := payload[field]; !ok {
			t.Fatalf("expected %s field, got %s", field, body)
		}
	}
}

func TestBuildAlbumImportDTOUsesTargetAlbumTitle(t *testing.T) {
	dto := buildAlbumImportDTO(model.AlbumImportSession{
		TargetAlbum: &model.Album{Title: "Late Registration"},
		PayloadJSON: `{"derived_album_title":"Archive Guess"}`,
	})

	if dto.AlbumTitle != "Late Registration" {
		t.Fatalf("expected target album title, got %q", dto.AlbumTitle)
	}
	if dto.DerivedAlbumTitle != "Archive Guess" {
		t.Fatalf("expected recognized title to remain available, got %q", dto.DerivedAlbumTitle)
	}
}

func TestBuildAlbumImportDTOUsesDeferredCommitTitle(t *testing.T) {
	dto := buildAlbumImportDTO(model.AlbumImportSession{
		PayloadJSON: `{"commit_request":{"album":{"title":"再想想"}},"derived_album_title":"Archive Guess"}`,
	})

	if dto.AlbumTitle != "再想想" {
		t.Fatalf("expected deferred commit title, got %q", dto.AlbumTitle)
	}
}

func TestBuildAlbumImportDTOFallsBackToArchiveName(t *testing.T) {
	dto := buildAlbumImportDTO(model.AlbumImportSession{
		Files: []model.AlbumImportFile{{Role: AlbumImportFileRoleArchive, FileName: "再想想.zip"}},
	})

	if dto.AlbumTitle != "再想想" {
		t.Fatalf("expected archive title fallback, got %q", dto.AlbumTitle)
	}
}

func TestDeleteAlbumImportRecordRemovesItsNotification(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	session := model.AlbumImportSession{UserID: &user.ID, Status: AlbumImportStatusCanceled, PayloadJSON: `{}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	notification := model.Notification{RecipientID: user.ID, Type: musicImportNotificationType, SourceType: "music_album_import", SourceID: session.ID}
	if err := db.Create(&notification).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAlbumImportRecord(user, session.ID); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&model.Notification{}).Where("source_type = ? AND source_id = ?", "music_album_import", session.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected import notification to be deleted, got %d", count)
	}
}

func TestListAlbumImportSessionsForUserPreloadsTargetAlbum(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	album := model.Album{Title: "Late Registration", EntryStatus: "open", Status: "open"}
	if err := db.Create(&album).Error; err != nil {
		t.Fatalf("create album: %v", err)
	}
	session := model.AlbumImportSession{
		UserID:        &user.ID,
		PayloadJSON:   `{}`,
		TargetAlbumID: &album.ID,
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create album import session: %v", err)
	}

	sessions, err := svc.ListAlbumImportSessionsForUser(user)
	if err != nil {
		t.Fatalf("list album import sessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].TargetAlbum == nil {
		t.Fatalf("expected target album to be preloaded, got %#v", sessions)
	}
	if sessions[0].TargetAlbum.Title != "Late Registration" {
		t.Fatalf("expected target album title, got %q", sessions[0].TargetAlbum.Title)
	}
}

func TestCreateAlbumImportSessionStoresOwnerAndExpiration(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
	})
	if err != nil {
		t.Fatalf("create album import session: %v", err)
	}
	if session.UserID == nil || *session.UserID != user.ID {
		t.Fatalf("expected owner %s, got %#v", user.ID, session.UserID)
	}
	if session.InputMode != AlbumImportInputModeAuto || session.Stage != AlbumImportStageUpload {
		t.Fatalf("expected auto/upload defaults, got %q/%q", session.InputMode, session.Stage)
	}
	if session.ExpiresAt == nil || session.ExpiresAt.Before(time.Now().UTC().Add(6*24*time.Hour)) {
		t.Fatalf("expected expiration about seven days ahead, got %#v", session.ExpiresAt)
	}
}

func TestCreateAlbumImportSessionStoresInputContext(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		ArtistID:  "artist-existing",
		InputMode: AlbumImportInputModeFolder,
	})
	if err != nil {
		t.Fatalf("create album import session: %v", err)
	}
	if session.InputMode != AlbumImportInputModeFolder {
		t.Fatalf("expected folder input mode, got %q", session.InputMode)
	}

	dto := buildAlbumImportDTO(session)
	if dto.ArtistID != "artist-existing" {
		t.Fatalf("expected artist id in DTO, got %q", dto.ArtistID)
	}
}

func TestCreateAlbumImportSessionRejectsInvalidInputMode(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	_, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{InputMode: "disc"})
	if err == nil {
		t.Fatal("expected invalid input mode error")
	}
}

func TestBuildAlbumImportDTOMapsV2StateAndFiles(t *testing.T) {
	session := model.AlbumImportSession{
		InputMode:       AlbumImportInputModeAuto,
		Status:          AlbumImportStatusExtracting,
		Stage:           AlbumImportStageAnalyzing,
		ProgressCurrent: 2,
		ProgressTotal:   5,
		PayloadJSON:     "{}",
		ErrorMessage:    "archive warning",
		Files: []model.AlbumImportFile{{
			FileName:         "01 - Track.flac",
			Role:             "audio",
			DetectedFormat:   "flac",
			UploadStatus:     "uploaded",
			ProcessingStatus: "failed",
			ErrorMessage:     "transcode failed",
		}},
	}

	dto := buildAlbumImportDTO(session)
	if dto.InputMode != session.InputMode || dto.Stage != session.Stage {
		t.Fatalf("expected mode/stage %q/%q, got %q/%q", session.InputMode, session.Stage, dto.InputMode, dto.Stage)
	}
	if dto.Progress.Current != 2 || dto.Progress.Total != 5 {
		t.Fatalf("expected progress 2/5, got %#v", dto.Progress)
	}
	if len(dto.Files) != 1 || dto.Files[0].FileName != "01 - Track.flac" {
		t.Fatalf("expected mapped file, got %#v", dto.Files)
	}
	if len(dto.Errors) != 2 {
		t.Fatalf("expected session and file errors, got %#v", dto.Errors)
	}
}

func TestBuildAlbumImportDTOResolvesProcessedCoverKey(t *testing.T) {
	t.Setenv("S3_URL_PREFIX", "https://assets.atoman.test")
	session := model.AlbumImportSession{
		PayloadJSON: `{"cover_key":"music/album-imports/playback/sessions/import-1/cover/cover.webp"}`,
	}

	dto := buildAlbumImportDTO(session)
	want := "https://assets.atoman.test/music/album-imports/playback/sessions/import-1/cover/cover.webp"
	if dto.CoverURL != want {
		t.Fatalf("expected resolved cover URL %q, got %q", want, dto.CoverURL)
	}
}

func TestGetAlbumImportSessionPreloadsFilesAndJob(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
	})
	if err != nil {
		t.Fatalf("create album import session: %v", err)
	}
	if err := db.Create(&model.AlbumImportFile{
		ImportID:         session.ID,
		FileName:         "album.flac",
		Role:             "audio",
		UploadStatus:     "uploaded",
		ProcessingStatus: "pending",
	}).Error; err != nil {
		t.Fatalf("create album import file: %v", err)
	}
	if err := db.Create(&model.AlbumImportJob{
		ImportID:    session.ID,
		Status:      "queued",
		Stage:       AlbumImportStageAnalyzing,
		MaxAttempts: 3,
	}).Error; err != nil {
		t.Fatalf("create album import job: %v", err)
	}

	loaded, err := svc.GetAlbumImportSession(session.ID)
	if err != nil {
		t.Fatalf("get album import session: %v", err)
	}
	if len(loaded.Files) != 1 || loaded.Job == nil {
		t.Fatalf("expected preloaded files and job, got files=%#v job=%#v", loaded.Files, loaded.Job)
	}
}

func TestAlbumImportUserOperationsRejectAnotherUsersSession(t *testing.T) {
	tests := []struct {
		name string
		call func(*Service, authctx.CurrentUser, uuid.UUID) error
	}{
		{
			name: "archive upload",
			call: func(svc *Service, user authctx.CurrentUser, id uuid.UUID) error {
				archive := newImportTestZipArchive(t, map[string]string{"01 - Track.mp3": ""})
				_, err := svc.UploadAlbumImportArchive(user, id, "album.zip", bytes.NewReader(archive))
				return err
			},
		},
		{
			name: "multipart start",
			call: func(svc *Service, user authctx.CurrentUser, id uuid.UUID) error {
				_, err := svc.StartAlbumImportMultipart(user, id, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 1024})
				return err
			},
		},
		{
			name: "multipart part upload",
			call: func(svc *Service, user authctx.CurrentUser, id uuid.UUID) error {
				_, err := svc.CreateAlbumImportMultipartPartUpload(user, id, 1, CreateAlbumImportMultipartPartInput{PartSize: 1024})
				return err
			},
		},
		{
			name: "multipart part complete",
			call: func(svc *Service, user authctx.CurrentUser, id uuid.UUID) error {
				_, err := svc.CompleteAlbumImportMultipartPart(user, id, 1, CompleteAlbumImportMultipartPartInput{ETag: "etag-1", Size: 1024})
				return err
			},
		},
		{
			name: "multipart complete",
			call: func(svc *Service, user authctx.CurrentUser, id uuid.UUID) error {
				_, err := svc.CompleteAlbumImportMultipart(user, id)
				return err
			},
		},
		{
			name: "commit",
			call: func(svc *Service, user authctx.CurrentUser, id uuid.UUID) error {
				_, err := svc.CommitAlbumImportSession(user, id, CommitAlbumImportSessionInput{
					Artist: AlbumImportArtistPayload{Name: "Other Artist"},
					Album:  AlbumImportAlbumPayload{Title: "Other Album"},
				})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, _, owner := newMusicTestService(t)
			svc.albumImportMultipart = &fakeAlbumImportMultipartStore{uploadID: "upload-owner"}
			session, err := svc.CreateAlbumImportSession(owner, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
			if err != nil {
				t.Fatalf("create owner session: %v", err)
			}
			other := authctx.CurrentUser{ID: uuid.New(), Username: "other", Role: authctx.RoleUser}

			err = test.call(svc, other, session.ID)
			var appErr *apperr.AppError
			if !errors.As(err, &appErr) || appErr.Code != "music.import_not_found" {
				t.Fatalf("expected hidden owner session, got %v", err)
			}
		})
	}
}

func TestUpdateAlbumImportStatusAndPayloadSynchronizesSessionColumns(t *testing.T) {
	tests := []struct {
		status      string
		wantStage   string
		wantCurrent int64
		wantTotal   int64
		wantError   string
	}{
		{status: AlbumImportStatusUploading, wantStage: AlbumImportStageUpload, wantCurrent: 128, wantTotal: 1024},
		{status: AlbumImportStatusUploaded, wantStage: AlbumImportStageUpload, wantCurrent: 1024, wantTotal: 1024},
		{status: AlbumImportStatusQueued, wantStage: AlbumImportStageQueued},
		{status: AlbumImportStatusExtracting, wantStage: AlbumImportStageExtracting},
		{status: AlbumImportStatusReady, wantStage: AlbumImportStageReady, wantCurrent: 2, wantTotal: 2},
	}

	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			svc, db, _ := newMusicTestService(t)
			session := model.AlbumImportSession{
				Status:          AlbumImportStatusPendingUpload,
				Stage:           "stale",
				ProgressCurrent: 7,
				ProgressTotal:   10,
				PayloadJSON:     "{}",
				ErrorMessage:    "stale error",
			}
			if err := db.Create(&session).Error; err != nil {
				t.Fatalf("create session: %v", err)
			}
			payload := map[string]any{
				"multipart_file_size": float64(1024),
				"multipart_completed_parts": []map[string]any{{
					"partNumber": float64(1), "etag": "etag-1", "size": float64(128),
				}},
				"derived_tracks": []map[string]any{{"title": "One"}, {"title": "Two"}},
			}

			updated, err := svc.updateAlbumImportStatusAndPayload(session.ID, test.status, payload)
			if err != nil {
				t.Fatalf("update import status: %v", err)
			}
			if updated.Stage != test.wantStage || updated.ProgressCurrent != test.wantCurrent || updated.ProgressTotal != test.wantTotal || updated.ErrorMessage != test.wantError {
				t.Fatalf("unexpected synchronized state: %#v", updated)
			}
		})
	}
}

func TestMarkAlbumImportFailedSynchronizesFailureColumns(t *testing.T) {
	svc, db, _ := newMusicTestService(t)
	session := model.AlbumImportSession{
		Status:          AlbumImportStatusUploading,
		Stage:           AlbumImportStageUpload,
		ProgressCurrent: 128,
		ProgressTotal:   1024,
		PayloadJSON:     "{}",
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := svc.markAlbumImportFailed(session.ID, "upload failed"); err != nil {
		t.Fatalf("mark import failed: %v", err)
	}
	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load failed session: %v", err)
	}
	if stored.Status != AlbumImportStatusFailed || stored.Stage != "failed" || stored.ErrorMessage != "upload failed" {
		t.Fatalf("unexpected failure state: %#v", stored)
	}
	if stored.ProgressCurrent != 128 || stored.ProgressTotal != 1024 {
		t.Fatalf("expected failure to preserve progress, got %#v", stored)
	}
}

func TestStartAlbumImportMultipartRejectsOversizedArchive(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName: "Untrue.zip",
		FileSize: maxAlbumImportArchiveSize + 1,
	})
	if err == nil {
		t.Fatal("expected oversized archive to fail")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected app error, got %v", err)
	}
	if appErr.Code != "validation.invalid_request" {
		t.Fatalf("expected validation.invalid_request, got %#v", appErr)
	}
}

func TestStartAlbumImportMultipartRestoresExistingUploadStateForSameFile(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-1"}
	svc.albumImportMultipart = store

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	first, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName:    "Untrue.zip",
		FileSize:    64 * 1024 * 1024,
		ContentType: "application/zip",
	})
	if err != nil {
		t.Fatalf("start multipart: %v", err)
	}
	var uploading model.AlbumImportSession
	if err := db.First(&uploading, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load uploading session: %v", err)
	}
	if uploading.Stage != AlbumImportStageUpload || uploading.ProgressCurrent != 0 || uploading.ProgressTotal != 64*1024*1024 || uploading.ErrorMessage != "" {
		t.Fatalf("unexpected uploading state: %#v", uploading)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 2, CompleteAlbumImportMultipartPartInput{
		ETag: "etag-2",
		Size: albumImportMultipartPartSize,
	}); err != nil {
		t.Fatalf("complete part: %v", err)
	}
	if err := db.First(&uploading, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("reload uploading session: %v", err)
	}
	if uploading.ProgressCurrent != albumImportMultipartPartSize || uploading.ProgressTotal != 64*1024*1024 {
		t.Fatalf("unexpected multipart byte progress: %#v", uploading)
	}

	restored, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName:    "Untrue.zip",
		FileSize:    64 * 1024 * 1024,
		ContentType: "application/zip",
	})
	if err != nil {
		t.Fatalf("restore multipart: %v", err)
	}
	if store.createCalls != 1 {
		t.Fatalf("expected CreateMultipartUpload once, got %d", store.createCalls)
	}
	if restored.ObjectKey != first.ObjectKey {
		t.Fatalf("expected restored object key %q, got %q", first.ObjectKey, restored.ObjectKey)
	}
	if len(restored.CompletedParts) != 1 || restored.CompletedParts[0].PartNumber != 2 || restored.CompletedParts[0].ETag != "etag-2" {
		t.Fatalf("expected completed part to be preserved, got %#v", restored.CompletedParts)
	}
}

func TestStartAlbumImportMultipartRestoresFailedSessionToUploading(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-1"}
	svc.albumImportMultipart = store

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName: "Untrue.zip",
		FileSize: 64 * 1024 * 1024,
	}); err != nil {
		t.Fatalf("start multipart: %v", err)
	}

	var failed model.AlbumImportSession
	if err := db.First(&failed, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load started session: %v", err)
	}
	payload, err := readAlbumImportPayloadMap(failed.PayloadJSON)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	payload["error_message"] = "network failed"
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"status":       AlbumImportStatusFailed,
		"payload_json": string(payloadJSON),
	}).Error; err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName: "Untrue.zip",
		FileSize: 64 * 1024 * 1024,
	}); err != nil {
		t.Fatalf("restore failed multipart: %v", err)
	}
	if store.createCalls != 1 {
		t.Fatalf("expected restore to reuse existing upload, got create calls %d", store.createCalls)
	}

	var restored model.AlbumImportSession
	if err := db.First(&restored, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load restored session: %v", err)
	}
	if restored.Status != AlbumImportStatusUploading {
		t.Fatalf("expected failed session restored to uploading, got %#v", restored)
	}
	restoredPayload, err := readAlbumImportPayloadMap(restored.PayloadJSON)
	if err != nil {
		t.Fatalf("read restored payload: %v", err)
	}
	if stringValue(restoredPayload["error_message"]) != "" {
		t.Fatalf("expected error_message cleared, got %#v", restoredPayload["error_message"])
	}
}

func TestCreateAlbumImportMultipartPartUploadReturnsSignedURL(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-1", signedURL: "https://storage.test/upload-part-1"}
	svc.albumImportMultipart = store

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	started, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName: "Untrue.zip",
		FileSize: 64 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("start multipart: %v", err)
	}

	upload, err := svc.CreateAlbumImportMultipartPartUpload(user, session.ID, 1, CreateAlbumImportMultipartPartInput{
		PartSize: albumImportMultipartPartSize,
	})
	if err != nil {
		t.Fatalf("create part upload: %v", err)
	}
	if upload.PartNumber != 1 || upload.UploadURL != store.signedURL {
		t.Fatalf("unexpected part upload dto %#v", upload)
	}
	if store.presignKey != started.ObjectKey || store.presignUploadID != "upload-1" || store.presignPartNumber != 1 {
		t.Fatalf("unexpected presign call key=%q uploadID=%q part=%d", store.presignKey, store.presignUploadID, store.presignPartNumber)
	}
}

func TestCreateAlbumImportMultipartPartUploadRejectsFinishedStatuses(t *testing.T) {
	for _, status := range []string{AlbumImportStatusReady, AlbumImportStatusCommitted} {
		t.Run(status, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			svc.albumImportMultipart = &fakeAlbumImportMultipartStore{uploadID: "upload-1"}

			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
				Status: AlbumImportStatusPendingUpload,
				Payload: AlbumImportPayload{
					Artist: AlbumImportArtistPayload{Name: "Burial"},
				},
			})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
				FileName: "Untrue.zip",
				FileSize: 64 * 1024 * 1024,
			}); err != nil {
				t.Fatalf("start multipart: %v", err)
			}
			if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("status", status).Error; err != nil {
				t.Fatalf("set status: %v", err)
			}

			_, err = svc.CreateAlbumImportMultipartPartUpload(user, session.ID, 1, CreateAlbumImportMultipartPartInput{
				PartSize: albumImportMultipartPartSize,
			})
			if err == nil {
				t.Fatal("expected part upload to fail for finished status")
			}

			var stored model.AlbumImportSession
			if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
				t.Fatalf("load session: %v", err)
			}
			if stored.Status != status {
				t.Fatalf("expected status to remain %q, got %#v", status, stored)
			}
		})
	}
}

func TestCompleteAlbumImportMultipartPartRejectsFinishedStatuses(t *testing.T) {
	for _, status := range []string{AlbumImportStatusReady, AlbumImportStatusCommitted} {
		t.Run(status, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			svc.albumImportMultipart = &fakeAlbumImportMultipartStore{uploadID: "upload-1"}

			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
				Status: AlbumImportStatusPendingUpload,
				Payload: AlbumImportPayload{
					Artist: AlbumImportArtistPayload{Name: "Burial"},
				},
			})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
				FileName: "Untrue.zip",
				FileSize: 64 * 1024 * 1024,
			}); err != nil {
				t.Fatalf("start multipart: %v", err)
			}
			if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("status", status).Error; err != nil {
				t.Fatalf("set status: %v", err)
			}

			_, err = svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{
				ETag: "etag-1",
				Size: albumImportMultipartPartSize,
			})
			if err == nil {
				t.Fatal("expected complete part to fail for finished status")
			}

			var stored model.AlbumImportSession
			if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
				t.Fatalf("load session: %v", err)
			}
			if stored.Status != status {
				t.Fatalf("expected status to remain %q, got %#v", status, stored)
			}
		})
	}
}

func TestCompleteAlbumImportMultipartPartReplacesAndSortsParts(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{uploadID: "upload-1"}

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName: "Untrue.zip",
		FileSize: 64 * 1024 * 1024,
	}); err != nil {
		t.Fatalf("start multipart: %v", err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 2, CompleteAlbumImportMultipartPartInput{
		ETag: "etag-2-old",
		Size: albumImportMultipartPartSize,
	}); err != nil {
		t.Fatalf("complete part 2: %v", err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{
		ETag: "etag-1",
		Size: albumImportMultipartPartSize,
	}); err != nil {
		t.Fatalf("complete part 1: %v", err)
	}
	updated, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 2, CompleteAlbumImportMultipartPartInput{
		ETag: "etag-2-new",
		Size: albumImportMultipartPartSize + 1,
	})
	if err != nil {
		t.Fatalf("replace part 2: %v", err)
	}

	if len(updated.CompletedParts) != 2 {
		t.Fatalf("expected 2 completed parts, got %#v", updated.CompletedParts)
	}
	if updated.CompletedParts[0].PartNumber != 1 || updated.CompletedParts[0].ETag != "etag-1" {
		t.Fatalf("expected part 1 first, got %#v", updated.CompletedParts)
	}
	if updated.CompletedParts[1].PartNumber != 2 || updated.CompletedParts[1].ETag != "etag-2-new" || updated.CompletedParts[1].Size != albumImportMultipartPartSize+1 {
		t.Fatalf("expected part 2 replaced, got %#v", updated.CompletedParts)
	}
}

func TestCompleteAlbumImportMultipartCompletesSortedPartsAndQueuesWorker(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	archiveBody := newImportTestZipArchive(t, map[string]string{
		"02 - Archangel.flac": "",
		"01 - Untitled.mp3":   "",
	})
	store := &fakeAlbumImportMultipartStore{
		uploadID:   "upload-1",
		objectBody: archiveBody,
	}
	svc.albumImportMultipart = store

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	started, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName: "Untrue.zip",
		FileSize: 64 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("start multipart: %v", err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 2, CompleteAlbumImportMultipartPartInput{
		ETag: "etag-2",
		Size: 2 * albumImportMultipartPartSize,
	}); err != nil {
		t.Fatalf("complete part 2: %v", err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{
		ETag: "etag-1",
		Size: 2 * albumImportMultipartPartSize,
	}); err != nil {
		t.Fatalf("complete part 1: %v", err)
	}

	updated, err := svc.CompleteAlbumImportMultipart(user, session.ID)
	if err != nil {
		t.Fatalf("complete multipart: %v", err)
	}
	if updated.Status != AlbumImportStatusQueued {
		t.Fatalf("expected queued status, got %#v", updated)
	}
	if updated.Stage != AlbumImportStageQueued || updated.ErrorMessage != "" || updated.Job == nil || updated.Job.Status != AlbumImportJobStatusQueued {
		t.Fatalf("unexpected queued state: %#v", updated)
	}
	if store.completeKey != started.ObjectKey || store.completeUploadID != "upload-1" {
		t.Fatalf("unexpected complete call key=%q uploadID=%q", store.completeKey, store.completeUploadID)
	}
	if fmt.Sprint(store.completedPartNumbers) != "[1 2]" {
		t.Fatalf("expected sorted completed parts [1 2], got %#v", store.completedPartNumbers)
	}
	if store.openCalls != 0 || len(store.deletedKeys) != 0 {
		t.Fatalf("expected source object to be retained for worker, got %#v", store)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(updated.PayloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload json: %v", err)
	}
	if payload["archive_name"] != "Untrue.zip" {
		t.Fatalf("expected archive_name preserved, got %#v", payload["archive_name"])
	}
	if _, ok := payload["derived_tracks"]; ok {
		t.Fatalf("HTTP completion must not derive tracks, got %#v", payload["derived_tracks"])
	}
}

func TestCompleteAlbumImportMultipartDoesNotDeleteSourceObject(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	archiveBody := newImportTestZipArchive(t, map[string]string{
		"01 - Untitled.mp3": "",
	})
	store := &fakeAlbumImportMultipartStore{
		uploadID:   "upload-1",
		objectBody: archiveBody,
		deleteErr:  errors.New("delete failed"),
	}
	svc.albumImportMultipart = store

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{
		FileName: "Untrue.zip",
		FileSize: 64 * 1024 * 1024,
	}); err != nil {
		t.Fatalf("start multipart: %v", err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{
		ETag: "etag-1",
		Size: 4 * albumImportMultipartPartSize,
	}); err != nil {
		t.Fatalf("complete part 1: %v", err)
	}

	updated, err := svc.CompleteAlbumImportMultipart(user, session.ID)
	if err != nil {
		t.Fatalf("complete multipart should ignore cleanup failure: %v", err)
	}
	if updated.Status != AlbumImportStatusQueued {
		t.Fatalf("expected returned session queued, got %#v", updated)
	}

	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load stored session: %v", err)
	}
	if stored.Status != AlbumImportStatusQueued {
		t.Fatalf("expected stored session to remain queued, got %#v", stored)
	}
	payload, err := readAlbumImportPayloadMap(stored.PayloadJSON)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if store.openCalls != 0 || len(store.deletedKeys) != 0 {
		t.Fatalf("source object should not be read or deleted, got %#v", store)
	}
	if stringValue(payload["error_message"]) != "" {
		t.Fatalf("expected no error_message after cleanup failure, got %#v", payload["error_message"])
	}
}

func TestCompleteAlbumImportMultipartRejectsMissingArchiveName(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{
		uploadID:   "upload-1",
		objectBody: newImportTestZipArchive(t, map[string]string{"01 - Untitled.mp3": ""}),
	}

	payload := map[string]any{
		"multipart_file_name":  "Untrue.zip",
		"multipart_file_size":  float64(64 * 1024 * 1024),
		"multipart_object_key": "music/album-imports/test.zip",
		"multipart_upload_id":  "upload-1",
		"multipart_part_size":  float64(albumImportMultipartPartSize),
		"multipart_completed_parts": []map[string]any{
			{"partNumber": float64(1), "etag": "etag-1", "size": float64(albumImportMultipartPartSize)},
		},
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	session := model.AlbumImportSession{
		UserID:      &user.ID,
		Status:      AlbumImportStatusUploading,
		PayloadJSON: string(payloadJSON),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = svc.CompleteAlbumImportMultipart(user, session.ID)
	if err == nil {
		t.Fatal("expected complete multipart to reject missing archive_name")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) {
		t.Fatalf("expected app error, got %v", err)
	}
	if appErr.Code != "music.import_invalid_status" && appErr.Code != "validation.invalid_request" {
		t.Fatalf("expected import_invalid_status or validation.invalid_request, got %#v", appErr)
	}

	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load stored session: %v", err)
	}
	if stored.Status == AlbumImportStatusReady {
		t.Fatalf("expected session not to become ready, got %#v", stored)
	}
}

func TestCommitAlbumImportSessionReadyCreatesArtistAndAlbum(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusReady,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{
				Name:      "FKA twigs",
				LegalName: "Tahliah Debrett Barnett",
				StageNames: []ArtistStageNamePayload{
					{Name: "FKA twigs", IsPrimary: true, StartDateText: "2012"},
					{Name: "Twigs", IsPrimary: false, EndDateText: "2012"},
				},
				BirthPlace: "Cheltenham, England",
			},
			Album: AlbumImportAlbumPayload{
				Title:       "LP1",
				ReleaseDate: "2014-08-06",
				ReleaseYear: 2014,
				Tracks: []AlbumImportTrackPayload{
					{Title: "Preface", TrackNumber: 1},
					{Title: "Lights On", TrackNumber: 2},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedReadyImportMedia(t, db, session.ID, "https://cdn.example.com/lp1.jpg", "Preface", "Lights On")

	committed, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{
			Name:        "FKA twigs",
			LegalName:   "Tahliah Debrett Barnett",
			Bio:         "English singer",
			Nationality: "British",
			BirthDate:   "1988-01-16",
			ImageURL:    "https://cdn.example.com/fka-twigs.jpg",
			StageNames: []ArtistStageNamePayload{
				{Name: "FKA twigs", IsPrimary: true, StartDateText: "2012"},
				{Name: "Twigs", IsPrimary: false, EndDateText: "2012"},
			},
			BirthPlace: "Cheltenham, England",
		},
		Album: AlbumImportAlbumPayload{
			Title:       "LP1",
			Description: "Debut studio album",
			AlbumType:   "ep",
			CoverURL:    "https://cdn.example.com/lp1.jpg",
			ReleaseDate: "2014-08-06",
			ReleaseYear: 2014,
			Tracks: []AlbumImportTrackPayload{
				{Title: "Preface", TrackNumber: 1},
				{Title: "Lights On", TrackNumber: 2},
			},
		},
		ArtistSource: "https://example.com/fka-twigs",
		AlbumSource:  "https://example.com/lp1",
	})
	if err != nil {
		t.Fatalf("commit session: %v", err)
	}
	if committed.Status != AlbumImportStatusCommitted {
		t.Fatalf("expected committed status, got %#v", committed)
	}
	if committed.Stage != AlbumImportStageCompleted || committed.ProgressCurrent != 1 || committed.ProgressTotal != 1 || committed.ErrorMessage != "" {
		t.Fatalf("unexpected committed state: %#v", committed)
	}

	var artist model.Artist
	if err := db.Where("name = ?", "FKA twigs").First(&artist).Error; err != nil {
		t.Fatalf("load artist: %v", err)
	}
	if artist.LegalName != "Tahliah Debrett Barnett" {
		t.Fatalf("expected legal name persisted, got %#v", artist)
	}
	if artist.ImageURL != "https://cdn.example.com/fka-twigs.jpg" {
		t.Fatalf("expected artist image persisted, got %q", artist.ImageURL)
	}
	var stageNames []ArtistStageNamePayload
	if err := json.Unmarshal([]byte(artist.StageNamesJSON), &stageNames); err != nil {
		t.Fatalf("unmarshal stage names json: %v", err)
	}
	if len(stageNames) != 2 || !stageNames[0].IsPrimary || stageNames[0].Name != "FKA twigs" || stageNames[0].StartDateText != "2012" || stageNames[1].EndDateText != "2012" {
		t.Fatalf("expected structured stage names persisted, got %#v", stageNames)
	}
	if artist.BirthPlace != "Cheltenham, England" {
		t.Fatalf("expected birth place persisted, got %#v", artist)
	}
	if artist.Bio != "English singer" || artist.Nationality != "British" || artist.BirthDate == nil || artist.BirthDate.Format("2006-01-02") != "1988-01-16" || artist.BirthYear != 1988 {
		t.Fatalf("expected artist supplement fields persisted, got %#v", artist)
	}

	var album model.Album
	if err := db.Preload("Artists").Where("title = ?", "LP1").First(&album).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	if album.ReleaseYear != 2014 {
		t.Fatalf("expected release year persisted, got %#v", album)
	}
	if got := album.ReleaseDate.Format("2006-01-02"); got != "2014-08-06" {
		t.Fatalf("expected release date persisted, got %q", got)
	}
	if album.Year != 2014 {
		t.Fatalf("expected year persisted, got %#v", album)
	}
	if album.Description != "Debut studio album" || album.AlbumType != "ep" {
		t.Fatalf("expected album supplement fields persisted, got %#v", album)
	}
	if len(album.Artists) != 1 || album.Artists[0].ID != artist.ID {
		t.Fatalf("expected album linked to artist, got %#v", album.Artists)
	}

	var songs []model.Song
	if err := db.Where("album_id = ?", album.ID).Order("track_number ASC").Find(&songs).Error; err != nil {
		t.Fatalf("load songs: %v", err)
	}
	if len(songs) != 2 {
		t.Fatalf("expected 2 songs, got %#v", songs)
	}
	if album.CoverURL != "https://cdn.example.com/lp1.jpg" {
		t.Fatalf("expected submitted album cover, got %q", album.CoverURL)
	}
	for _, song := range songs {
		if song.AudioURL == "" {
			t.Fatalf("expected processed song audio, got %q", song.AudioURL)
		}
	}
	var committedPayload map[string]any
	if err := json.Unmarshal([]byte(committed.PayloadJSON), &committedPayload); err != nil {
		t.Fatalf("decode committed payload: %v", err)
	}
	if committedPayload["artist_source"] != "https://example.com/fka-twigs" || committedPayload["album_source"] != "https://example.com/lp1" {
		t.Fatalf("expected sources persisted in import session, got %#v", committedPayload)
	}
}

func TestCommitAlbumImportSessionUploadingPersistsCommitRequest(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"status": AlbumImportStatusUploading, "stage": AlbumImportStageUpload,
	}).Error; err != nil {
		t.Fatalf("set session uploading: %v", err)
	}

	queued, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{Name: "Queued Artist"},
		Album:  AlbumImportAlbumPayload{Title: "Queued Album"},
	})
	if err != nil {
		t.Fatalf("request early commit: %v", err)
	}
	if queued.Status != AlbumImportStatusUploading || queued.CommittedAt != nil {
		t.Fatalf("expected uploading session with deferred commit, got %#v", queued)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(queued.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if _, ok := payload["commit_request"]; !ok {
		t.Fatalf("expected deferred commit request, got %#v", payload)
	}
	var albums int64
	if err := db.Model(&model.Album{}).Count(&albums).Error; err != nil {
		t.Fatalf("count albums: %v", err)
	}
	if albums != 0 {
		t.Fatalf("expected no album before import is ready, got %d", albums)
	}
}

func TestFinalizeSubmittedAlbumImportCommitsReadySession(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"commit_request": CommitAlbumImportSessionInput{
		Artist: completeAlbumImportArtistPayload("Finalized Artist"),
		Album: AlbumImportAlbumPayload{
			Title: "Finalized Album", CoverURL: "https://cdn.test/finalized.jpg", ReleaseDate: "2020-01-01",
			Tracks: []AlbumImportTrackPayload{{Title: "Finalized Track", TrackNumber: 1}},
		},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
	}, "derived_tracks": []map[string]any{{"title": "Finalized Track", "track_number": 1, "audio_url": "https://cdn.test/finalized.mp3"}},
	})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("payload_json", string(payload)).Error; err != nil {
		t.Fatalf("save deferred commit: %v", err)
	}

	if err := svc.FinalizeSubmittedAlbumImport(session.ID); err != nil {
		t.Fatalf("finalize import: %v", err)
	}
	var albums int64
	if err := db.Model(&model.Album{}).Where("title = ?", "Finalized Album").Count(&albums).Error; err != nil {
		t.Fatalf("count finalized album: %v", err)
	}
	if albums != 1 {
		t.Fatalf("expected finalized album, got %d", albums)
	}
}

func TestCommitAlbumImportSessionPromotesS3AssetsAndDeletesUploads(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	var copiedSources []string
	var copiedDestinations []string
	var deletedKeys []string
	svc.s3 = fakeMusicPromotionS3Client(t, &copiedSources, &copiedDestinations, &deletedKeys)
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.atoman.test")

	sessionID := uuid.New()
	coverKey := "music/album-imports/playback/sessions/" + sessionID.String() + "/cover/cover.jpg"
	audioKey := "music/album-imports/playback/sessions/" + sessionID.String() + "/files/audio.mp3"
	payloadJSON, err := json.Marshal(map[string]any{
		"cover_key": coverKey,
		"derived_tracks": []map[string]any{{
			"title":        "Archangel",
			"track_number": 1,
			"audio_key":    audioKey,
			"audio_url":    "https://cdn.atoman.test/" + audioKey,
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	session := model.AlbumImportSession{Base: model.Base{ID: sessionID}, UserID: &user.ID, Status: AlbumImportStatusReady, PayloadJSON: string(payloadJSON)}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}
	sourceKey := "music/album-imports/source/users/" + user.ID.String() + "/sessions/" + sessionID.String() + "/album.zip"
	importFile := model.AlbumImportFile{
		ImportID: sessionID, FileName: "album.zip", SourceKey: sourceKey, PlaybackKey: audioKey,
		UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: "completed",
	}
	if err := db.Create(&importFile).Error; err != nil {
		t.Fatalf("create import file: %v", err)
	}

	if _, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: completeAlbumImportArtistPayload("Burial"),
		Album: AlbumImportAlbumPayload{
			Title: "Untrue", ReleaseDate: "2007-11-05",
		},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
	}); err != nil {
		t.Fatalf("commit session: %v", err)
	}

	var album model.Album
	if err := db.Where("title = ?", "Untrue").First(&album).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	var song model.Song
	if err := db.Where("album_id = ?", album.ID).First(&song).Error; err != nil {
		t.Fatalf("load song: %v", err)
	}
	coverPrefix := "music/albums/" + album.ID.String() + "/covers/"
	audioPrefix := "music/albums/" + album.ID.String() + "/tracks/" + song.ID.String() + "/"
	if !strings.HasPrefix(album.CoverURL, "https://cdn.atoman.test/"+coverPrefix) || !strings.HasSuffix(album.CoverURL, ".jpg") {
		t.Fatalf("unexpected album cover URL: %s", album.CoverURL)
	}
	if !strings.HasPrefix(song.AudioURL, "https://cdn.atoman.test/"+audioPrefix) || !strings.HasSuffix(song.AudioURL, ".mp3") {
		t.Fatalf("unexpected song audio URL: %s", song.AudioURL)
	}
	if len(copiedSources) != 2 || len(copiedDestinations) != 2 {
		t.Fatalf("expected 2 copied objects, got sources=%#v destinations=%#v", copiedSources, copiedDestinations)
	}
	if !strings.HasPrefix(copiedDestinations[0], coverPrefix) || !strings.HasPrefix(copiedDestinations[1], audioPrefix) {
		t.Fatalf("unexpected copy destinations: %#v", copiedDestinations)
	}
	didCleanup, err := NewImportWorker(db, NewMusicImportObjectStore(svc.s3), "test-worker").CleanupCommitted(context.Background())
	if err != nil || !didCleanup {
		t.Fatalf("cleanup committed import: cleaned=%v err=%v", didCleanup, err)
	}
	if !containsString(deletedKeys, coverKey) || !containsString(deletedKeys, audioKey) || !containsString(deletedKeys, sourceKey) {
		t.Fatalf("expected upload objects deleted, got %#v", deletedKeys)
	}
	var cleaned model.AlbumImportFile
	if err := db.First(&cleaned, "id = ?", importFile.ID).Error; err != nil {
		t.Fatalf("load cleaned import file: %v", err)
	}
	if cleaned.SourceKey != "" || cleaned.PlaybackKey != "" {
		t.Fatalf("temporary keys were retained: %#v", cleaned)
	}
}

func TestCommitAlbumImportSessionRejectsNonReadyStatus(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
			Album:  AlbumImportAlbumPayload{Title: "Untrue"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{Name: "Burial"},
		Album:  AlbumImportAlbumPayload{Title: "Untrue"},
	})
	var appErr *apperr.AppError
	if err == nil {
		t.Fatal("expected commit to fail for non-ready session")
	}
	if !errors.As(err, &appErr) {
		t.Fatalf("expected app error, got %v", err)
	}
	if appErr.Code != "music.import_invalid_status" {
		t.Fatalf("expected import_invalid_status, got %#v", appErr)
	}
}

func TestCommitAlbumImportSessionIsIdempotentAfterCommit(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedReadyImportMedia(t, db, session.ID, "https://cdn.test/discovery.jpg", "One More Time")
	input := CommitAlbumImportSessionInput{
		Artist:       completeAlbumImportArtistPayload("Daft Punk"),
		Album:        AlbumImportAlbumPayload{Title: "Discovery", CoverURL: "https://cdn.test/discovery.jpg", ReleaseDate: "2001-03-12", Tracks: []AlbumImportTrackPayload{{Title: "One More Time", TrackNumber: 1}}},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
	}
	first, err := svc.CommitAlbumImportSession(user, session.ID, input)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second, err := svc.CommitAlbumImportSession(user, session.ID, input)
	if err != nil {
		t.Fatalf("repeat commit: %v", err)
	}
	if first.TargetAlbumID == nil || second.TargetAlbumID == nil || *first.TargetAlbumID != *second.TargetAlbumID {
		t.Fatalf("expected the same target album, got %#v and %#v", first.TargetAlbumID, second.TargetAlbumID)
	}
	var albums int64
	if err := db.Model(&model.Album{}).Where("title = ?", "Discovery").Count(&albums).Error; err != nil {
		t.Fatalf("count albums: %v", err)
	}
	if albums != 1 {
		t.Fatalf("expected one album after repeat commit, got %d", albums)
	}
}

func TestRepairAlbumImportSessionUpdatesOriginalAlbum(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	var copiedSources []string
	var copiedDestinations []string
	var deletedKeys []string
	svc.s3 = fakeMusicPromotionS3Client(t, &copiedSources, &copiedDestinations, &deletedKeys)
	t.Setenv("STORAGE_TYPE", "s3")
	t.Setenv("S3_BUCKET", "atoman-test")
	t.Setenv("S3_URL_PREFIX", "https://cdn.atoman.test")
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedReadyImportMedia(t, db, session.ID, "https://cdn.test/discovery.jpg", "One More Time", "Aerodynamic")
	initial := CommitAlbumImportSessionInput{
		Artist: completeAlbumImportArtistPayload("Daft Punk"),
		Album: AlbumImportAlbumPayload{Title: "Discovery", CoverURL: "https://cdn.test/discovery.jpg", ReleaseDate: "2001-03-12", Tracks: []AlbumImportTrackPayload{
			{Title: "One More Time", TrackNumber: 1},
			{Title: "Aerodynamic", TrackNumber: 2},
		}},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
	}
	committed, err := svc.CommitAlbumImportSession(user, session.ID, initial)
	if err != nil {
		t.Fatalf("commit session: %v", err)
	}
	if committed.TargetAlbumID == nil {
		t.Fatal("expected target album")
	}

	ready, err := svc.RepairAlbumImportSession(user, session.ID)
	if err != nil {
		t.Fatalf("start repair: %v", err)
	}
	if ready.Status != AlbumImportStatusReady || ready.TargetAlbumID == nil || *ready.TargetAlbumID != *committed.TargetAlbumID {
		t.Fatalf("unexpected repair state: %#v", ready)
	}
	var beforeAlbum model.Album
	if err := db.First(&beforeAlbum, "id = ?", *committed.TargetAlbumID).Error; err != nil {
		t.Fatalf("load album before repair: %v", err)
	}
	var beforeSongs []model.Song
	if err := db.Where("album_id = ?", beforeAlbum.ID).Order("track_number ASC").Find(&beforeSongs).Error; err != nil {
		t.Fatalf("load songs before repair: %v", err)
	}
	playlist := model.Playlist{UserID: user.ID, Name: "Keep identity"}
	if err := db.Create(&playlist).Error; err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	if err := db.Create(&model.PlaylistSong{PlaylistID: playlist.ID, SongID: beforeSongs[1].ID, Position: 1}).Error; err != nil {
		t.Fatalf("add song to playlist: %v", err)
	}
	if err := db.Create(&model.MusicListeningHistory{UserID: user.ID, SongID: beforeSongs[1].ID, PlayCount: 3, LastPlayedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create listening history: %v", err)
	}
	lyrics, err := svc.SaveSongLyrics(user, beforeSongs[1].ID, SaveLyricsInput{Content: "work it", Format: "plain", EditSummary: "initial lyrics"})
	if err != nil {
		t.Fatalf("save lyrics: %v", err)
	}
	annotation, err := svc.CreateLyricAnnotation(user, beforeSongs[1].ID, CreateAnnotationInput{
		LineID: lyrics.Lines[0].ID, SelectedText: "work", StartOffset: 0, EndOffset: 4, Body: "annotation",
	})
	if err != nil {
		t.Fatalf("create lyric annotation: %v", err)
	}
	newAudioKey := "music/album-imports/playback/sessions/" + session.ID.String() + "/repair/track.mp3"
	payload, err := readAlbumImportPayloadMap(ready.PayloadJSON)
	if err != nil {
		t.Fatalf("read repair payload: %v", err)
	}
	payload["derived_tracks"] = []map[string]any{{
		"song_id": beforeSongs[1].ID.String(), "title": "Aerodynamic (Remastered)", "track_number": 1,
		"audio_url": "https://cdn.atoman.test/" + newAudioKey,
	}}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal repair payload: %v", err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("payload_json", string(payloadJSON)).Error; err != nil {
		t.Fatalf("update repair payload: %v", err)
	}
	var artist model.Artist
	if err := db.Joins("JOIN album_artists ON album_artists.artist_id = Artists.id").Where("album_artists.album_id = ?", *committed.TargetAlbumID).First(&artist).Error; err != nil {
		t.Fatalf("load artist: %v", err)
	}
	_, err = svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artists: []CommitAlbumImportArtistInput{{ArtistID: artist.ID.String()}},
		Album: AlbumImportAlbumPayload{Title: "Discovery (Remastered)", Description: "Updated metadata", CoverURL: "https://cdn.atoman.test/music/covers/uploads/users/test/repair.jpg", ReleaseDate: "2001-03-12", Tracks: []AlbumImportTrackPayload{
			{SongID: beforeSongs[1].ID.String(), Title: "Aerodynamic (Remastered)", TrackNumber: 1},
		}},
		AlbumSource: "album source",
	})
	if err != nil {
		t.Fatalf("submit repair: %v", err)
	}

	var album model.Album
	if err := db.First(&album, "id = ?", *committed.TargetAlbumID).Error; err != nil {
		t.Fatalf("load target album: %v", err)
	}
	if album.Title != "Discovery (Remastered)" || album.Description != "Updated metadata" {
		t.Fatalf("expected original album updated, got %#v", album)
	}
	if !strings.Contains(album.CoverURL, "/music/albums/"+album.ID.String()+"/covers/") {
		t.Fatalf("repair cover was not promoted: %s", album.CoverURL)
	}
	var songs []model.Song
	if err := db.Where("album_id = ? AND status <> ?", album.ID, "closed").Order("track_number ASC").Find(&songs).Error; err != nil {
		t.Fatalf("load songs: %v", err)
	}
	if len(songs) != 1 || songs[0].ID != beforeSongs[1].ID || songs[0].Title != "Aerodynamic (Remastered)" {
		t.Fatalf("expected repaired tracks, got %#v", songs)
	}
	var playlistSong model.PlaylistSong
	if err := db.First(&playlistSong, "playlist_id = ? AND song_id = ?", playlist.ID, beforeSongs[1].ID).Error; err != nil {
		t.Fatalf("playlist relation did not survive repair: %v", err)
	}
	var history model.MusicListeningHistory
	if err := db.First(&history, "user_id = ? AND song_id = ?", user.ID, beforeSongs[1].ID).Error; err != nil || history.PlayCount != 3 {
		t.Fatalf("listening history did not survive repair: %#v, err=%v", history, err)
	}
	var savedLyrics model.MusicSongLyric
	if err := db.First(&savedLyrics, "song_id = ?", beforeSongs[1].ID).Error; err != nil || savedLyrics.Content != "work it" {
		t.Fatalf("lyrics did not survive repair: %#v, err=%v", savedLyrics, err)
	}
	var savedAnnotation model.MusicLyricAnnotation
	if err := db.First(&savedAnnotation, "id = ? AND song_id = ?", annotation.ID, beforeSongs[1].ID).Error; err != nil {
		t.Fatalf("lyric annotation did not survive repair: %v", err)
	}
	var removedSong model.Song
	if err := db.First(&removedSong, "id = ?", beforeSongs[0].ID).Error; err != nil || removedSong.Status != "closed" {
		t.Fatalf("expected removed track identity to be preserved as closed: %#v, err=%v", removedSong, err)
	}
	if !strings.Contains(songs[0].AudioURL, "/music/albums/"+album.ID.String()+"/tracks/"+songs[0].ID.String()+"/") {
		t.Fatalf("repair audio was not promoted: %s", songs[0].AudioURL)
	}
	var count int64
	if err := db.Model(&model.Album{}).Count(&count).Error; err != nil {
		t.Fatalf("count albums: %v", err)
	}
	if count != 1 {
		t.Fatalf("repair created duplicate albums: %d", count)
	}
	var revisions []model.Revision
	if err := db.Where("content_type = ? AND content_id = ?", "album", album.ID).Order("version_number ASC").Find(&revisions).Error; err != nil {
		t.Fatalf("load album revisions: %v", err)
	}
	if len(revisions) != 2 || !strings.Contains(string(revisions[0].ContentSnapshot), beforeAlbum.CoverURL) || !strings.Contains(string(revisions[0].ContentSnapshot), beforeSongs[0].AudioURL) {
		t.Fatalf("repair did not preserve the original media revision: %#v", revisions)
	}
	if !strings.Contains(string(revisions[1].ContentSnapshot), album.CoverURL) || !strings.Contains(string(revisions[1].ContentSnapshot), songs[0].AudioURL) {
		t.Fatalf("repair revision does not reference promoted media: %#v", revisions[1])
	}
}

func TestRepairAlbumImportSessionRejectsInvalidSongIdentity(t *testing.T) {
	tests := []struct {
		name   string
		tracks func(existing []model.Song, foreign model.Song) []AlbumImportTrackPayload
		want   string
	}{
		{
			name: "invalid id",
			tracks: func(_ []model.Song, _ model.Song) []AlbumImportTrackPayload {
				return []AlbumImportTrackPayload{{SongID: "not-a-uuid", Title: "Changed", TrackNumber: 1, AudioURL: "/changed.mp3"}}
			},
			want: "invalid song id",
		},
		{
			name: "duplicate id",
			tracks: func(existing []model.Song, _ model.Song) []AlbumImportTrackPayload {
				return []AlbumImportTrackPayload{
					{SongID: existing[0].ID.String(), Title: "Changed 1", TrackNumber: 1, AudioURL: "/changed-1.mp3"},
					{SongID: existing[0].ID.String(), Title: "Changed 2", TrackNumber: 2, AudioURL: "/changed-2.mp3"},
				}
			},
			want: "duplicate song id",
		},
		{
			name: "song from another album",
			tracks: func(_ []model.Song, foreign model.Song) []AlbumImportTrackPayload {
				return []AlbumImportTrackPayload{{SongID: foreign.ID.String(), Title: "Changed", TrackNumber: 1, AudioURL: "/changed.mp3"}}
			},
			want: "song does not belong to target album",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
			if err != nil {
				t.Fatal(err)
			}
			seedReadyImportMedia(t, db, session.ID, "/cover.jpg", "Original")
			committed, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
				Artist: completeAlbumImportArtistPayload("Identity Artist"),
				Album: AlbumImportAlbumPayload{
					Title: "Original Album", CoverURL: "/cover.jpg", ReleaseDate: "2020-01-01",
					Tracks: []AlbumImportTrackPayload{{Title: "Original", TrackNumber: 1}},
				},
				ArtistSource: "artist source", AlbumSource: "album source",
			})
			if err != nil || committed.TargetAlbumID == nil {
				t.Fatalf("initial commit: %#v, %v", committed, err)
			}
			if _, err := svc.RepairAlbumImportSession(user, session.ID); err != nil {
				t.Fatalf("start repair: %v", err)
			}

			var existing []model.Song
			if err := db.Where("album_id = ?", *committed.TargetAlbumID).Find(&existing).Error; err != nil {
				t.Fatal(err)
			}
			foreignAlbum := model.Album{Title: "Foreign Album"}
			if err := db.Create(&foreignAlbum).Error; err != nil {
				t.Fatal(err)
			}
			foreignSong := model.Song{Title: "Foreign", AudioURL: "/foreign.mp3", Status: "open", AlbumID: &foreignAlbum.ID}
			if err := db.Create(&foreignSong).Error; err != nil {
				t.Fatal(err)
			}
			var artist model.Artist
			if err := db.Joins("JOIN album_artists ON album_artists.artist_id = Artists.id").Where("album_artists.album_id = ?", *committed.TargetAlbumID).First(&artist).Error; err != nil {
				t.Fatal(err)
			}

			_, err = svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
				Artists: []CommitAlbumImportArtistInput{{ArtistID: artist.ID.String()}},
				Album: AlbumImportAlbumPayload{
					Title: "Changed Album", CoverURL: "/changed-cover.jpg", ReleaseDate: "2021-01-01",
					Tracks: tc.tracks(existing, foreignSong),
				},
				AlbumSource: "album source",
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			var album model.Album
			if err := db.First(&album, "id = ?", *committed.TargetAlbumID).Error; err != nil {
				t.Fatal(err)
			}
			if album.Title != "Original Album" {
				t.Fatalf("failed repair partially updated album: %#v", album)
			}
			var unchanged model.Song
			if err := db.First(&unchanged, "id = ?", existing[0].ID).Error; err != nil || unchanged.Title != "Original" || unchanged.Status != "open" {
				t.Fatalf("failed repair partially updated song: %#v, err=%v", unchanged, err)
			}
		})
	}
}

func TestCommitAlbumImportSessionUsesExistingArtistWhenArtistIDProvided(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	existingArtist := model.Artist{
		Name:        "Kanye West",
		LegalName:   "Kanye Omari West",
		EntryStatus: "open",
	}
	if err := db.Create(&existingArtist).Error; err != nil {
		t.Fatalf("create existing artist: %v", err)
	}

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusReady,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Ignored Name"},
			Album: AlbumImportAlbumPayload{
				Title:       "Graduation",
				ReleaseYear: 2007,
				Tracks: []AlbumImportTrackPayload{
					{Title: "Good Morning", TrackNumber: 1},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedReadyImportMedia(t, db, session.ID, "https://cdn.test/graduation.jpg", "Good Morning")

	committed, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		ArtistID: existingArtist.ID.String(),
		Album: AlbumImportAlbumPayload{
			Title:       "Graduation",
			CoverURL:    "https://cdn.test/graduation.jpg",
			ReleaseDate: "2007-09-11",
			ReleaseYear: 2007,
			Tracks: []AlbumImportTrackPayload{
				{Title: "Good Morning", TrackNumber: 1},
			},
		},
		AlbumSource: "album source",
	})
	if err != nil {
		t.Fatalf("commit session with existing artist: %v", err)
	}
	if committed.Status != AlbumImportStatusCommitted {
		t.Fatalf("expected committed status, got %#v", committed)
	}

	var artistCount int64
	if err := db.Model(&model.Artist{}).Count(&artistCount).Error; err != nil {
		t.Fatalf("count artists: %v", err)
	}
	if artistCount != 1 {
		t.Fatalf("expected existing artist to be reused, got artist_count=%d", artistCount)
	}

	var album model.Album
	if err := db.Preload("Artists").Where("title = ?", "Graduation").First(&album).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	if len(album.Artists) != 1 || album.Artists[0].ID != existingArtist.ID {
		t.Fatalf("expected album linked to existing artist, got %#v", album.Artists)
	}
}

func TestCommitAlbumImportSessionSupportsMultipleCreators(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	existingArtist := model.Artist{
		Name:        "Existing Creator",
		EntryStatus: "open",
	}
	if err := db.Create(&existingArtist).Error; err != nil {
		t.Fatalf("create existing artist: %v", err)
	}

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusReady,
		Payload: AlbumImportPayload{
			Artists: []AlbumImportArtistPayload{
				{Name: "Existing Creator"},
				{Name: "New Creator", ArtistForm: "person"},
			},
			Album: AlbumImportAlbumPayload{
				Title:       "Joint Album",
				ReleaseDate: "2022-09-09",
				Tracks: []AlbumImportTrackPayload{
					{Title: "Together", TrackNumber: 1},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	seedReadyImportMedia(t, db, session.ID, "https://cdn.test/joint.jpg", "Together")

	_, err = svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artists: []CommitAlbumImportArtistInput{
			{
				ArtistID: existingArtist.ID.String(),
				Roles: []AlbumArtistRoleInput{
					{Role: "primary"},
					{Role: "producer"},
				},
			},
			{
				Name:       "New Creator",
				ArtistForm: "person",
				Roles: []AlbumArtistRoleInput{
					{Role: "featured"},
					{Role: "custom", Label: "Mix Engineer"},
				},
			},
		},
		Album: AlbumImportAlbumPayload{
			Title:       "Joint Album",
			CoverURL:    "https://cdn.test/joint.jpg",
			ReleaseDate: "2022-09-09",
			Tracks: []AlbumImportTrackPayload{
				{Title: "Together", TrackNumber: 1},
			},
		},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
	})
	if err != nil {
		t.Fatalf("commit multi-creator session: %v", err)
	}

	var album model.Album
	if err := db.Preload("Artists").Where("title = ?", "Joint Album").First(&album).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	if len(album.Artists) != 2 {
		t.Fatalf("expected album linked to 2 creators, got %#v", album.Artists)
	}
	var credits []model.AlbumArtist
	if err := db.Where("album_id = ?", album.ID).Order("position ASC, role ASC").Find(&credits).Error; err != nil {
		t.Fatalf("load album credits: %v", err)
	}
	if len(credits) != 4 {
		t.Fatalf("expected four album credits, got %#v", credits)
	}

	var songs []model.Song
	if err := db.Preload("Artists").Where("album_id = ?", album.ID).Find(&songs).Error; err != nil {
		t.Fatalf("load songs: %v", err)
	}
	if len(songs) != 1 || len(songs[0].Artists) != 2 {
		t.Fatalf("expected song linked to 2 creators, got %#v", songs)
	}
}

func TestCommitAlbumImportSessionUsesResolvedSourceKindsAndTrackNumberAwareAudioMatch(t *testing.T) {
	svc, db, user := newMusicTestService(t)

	sessionPayload := map[string]any{
		"derived_cover": "https://cdn.example.com/covers/joint-album.jpg",
		"derived_tracks": []map[string]any{
			{
				"title":        "Intro",
				"track_number": 1,
				"audio_url":    "s3/music/audio/intro-1.mp3",
			},
			{
				"title":        "Intro",
				"track_number": 2,
				"audio_url":    "https://cdn.example.com/audio/intro-2.mp3",
			},
		},
	}
	payloadJSON, err := json.Marshal(sessionPayload)
	if err != nil {
		t.Fatalf("marshal session payload: %v", err)
	}
	session := model.AlbumImportSession{
		UserID:      &user.ID,
		Status:      AlbumImportStatusReady,
		PayloadJSON: string(payloadJSON),
	}
	if err := db.Create(&session).Error; err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: completeAlbumImportArtistPayload("Source Artist"),
		Album: AlbumImportAlbumPayload{
			Title:       "Source Album",
			ReleaseDate: "2024-01-02",
			Tracks: []AlbumImportTrackPayload{
				{Title: "Intro", TrackNumber: 1},
				{Title: "Intro", TrackNumber: 2},
			},
		},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
	})
	if err != nil {
		t.Fatalf("commit session: %v", err)
	}

	var album model.Album
	if err := db.Where("title = ?", "Source Album").First(&album).Error; err != nil {
		t.Fatalf("load album: %v", err)
	}
	if album.CoverSource != "external" {
		t.Fatalf("expected derived cover source external, got %#v", album)
	}

	var songs []model.Song
	if err := db.Where("album_id = ?", album.ID).Order("track_number ASC").Find(&songs).Error; err != nil {
		t.Fatalf("load songs: %v", err)
	}
	if len(songs) != 2 {
		t.Fatalf("expected 2 songs, got %#v", songs)
	}
	if songs[0].AudioURL != "s3/music/audio/intro-1.mp3" || songs[0].AudioSource != "s3" {
		t.Fatalf("unexpected first song source: %#v", songs[0])
	}
	if songs[1].AudioURL != "https://cdn.example.com/audio/intro-2.mp3" || songs[1].AudioSource != "external" {
		t.Fatalf("unexpected second song source: %#v", songs[1])
	}
}

func TestUploadAlbumImportArchiveQueuesSourceWithoutDerivingIt(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Burial"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	archiveName := "Untrue (Deluxe).zip"
	archiveBody := newImportTestZipArchive(t, map[string]string{
		"01 - Untitled.mp3":   "",
		"02 - Archangel.flac": "",
		"booklet/cover.jpg":   "",
	})

	updated, err := svc.UploadAlbumImportArchive(user, session.ID, archiveName, bytes.NewReader(archiveBody))
	if err != nil {
		t.Fatalf("upload archive: %v", err)
	}
	if updated.Status != AlbumImportStatusQueued {
		t.Fatalf("expected queued status, got %#v", updated)
	}
	if updated.Stage != AlbumImportStageQueued || updated.ProgressCurrent != 0 || updated.ProgressTotal != 0 || updated.ErrorMessage != "" {
		t.Fatalf("unexpected direct upload queued state: %#v", updated)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(updated.PayloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload json: %v", err)
	}

	if _, ok := payload["derived_tracks"]; ok {
		t.Fatalf("direct upload must not derive tracks: %#v", payload)
	}
	var files []model.AlbumImportFile
	if err := db.Where("import_id = ?", session.ID).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Role != AlbumImportFileRoleArchive || files[0].UploadStatus != AlbumImportFileUploadStatusUploaded {
		t.Fatalf("archive source not persisted: %#v", files)
	}
	var jobs []model.AlbumImportJob
	if err := db.Where("import_id = ?", session.ID).Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Status != AlbumImportJobStatusQueued {
		t.Fatalf("archive upload was not queued: %#v", jobs)
	}
}

func TestUploadAlbumImportArchiveRejectsActualStreamSizeOverLimit(t *testing.T) {
	t.Setenv(albumImportMaxFileBytesEnv, "4")
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadAlbumImportArchive(user, session.ID, "album.zip", strings.NewReader("12345")); err == nil {
		t.Fatal("expected streamed size limit error")
	}
	if len(store.deletedKeys) != 1 {
		t.Fatalf("oversized source must be cleaned up: %#v", store.deletedKeys)
	}
	var files int64
	if err := db.Model(&model.AlbumImportFile{}).Where("import_id = ?", session.ID).Count(&files).Error; err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Fatalf("oversized source must not create file: %d", files)
	}
}

func TestUploadAlbumImportArchiveRejectsNonZipAndEmptyStreamsBeforeQueuing(t *testing.T) {
	for _, test := range []struct {
		name, archive string
		body          io.Reader
	}{
		{"non zip", "album.rar", strings.NewReader("data")},
		{"empty", "album.zip", strings.NewReader("")},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			store := &fakeAlbumImportMultipartStore{}
			svc.albumImportMultipart = store
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.UploadAlbumImportArchive(user, session.ID, test.archive, test.body); err == nil {
				t.Fatal("expected validation error")
			}
			var jobs int64
			if err := db.Model(&model.AlbumImportJob{}).Where("import_id = ?", session.ID).Count(&jobs).Error; err != nil {
				t.Fatal(err)
			}
			if jobs != 0 || len(store.objectBody) != 0 {
				t.Fatalf("invalid archive must not be stored or queued: jobs=%d body=%q", jobs, store.objectBody)
			}
		})
	}
}

func TestUploadAlbumImportArchiveRecordsCleanupWhenEmptyStreamDeleteFails(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{deleteErr: errors.New("delete failed")}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadAlbumImportArchive(user, session.ID, "album.zip", strings.NewReader("")); err == nil {
		t.Fatal("expected empty stream error")
	}
	var persisted model.AlbumImportSession
	if err := db.First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(persisted.PayloadJSON, "cleanup_targets") || len(store.deletedKeys) != 1 || !strings.Contains(persisted.PayloadJSON, store.deletedKeys[0]) {
		t.Fatalf("failed delete was not recorded: session=%s store=%#v", persisted.PayloadJSON, store)
	}
}

func TestUploadAlbumImportArchiveQueuesCoverSourceWithoutDeriving(t *testing.T) {
	testQueuedArchiveUploadContract(t, AlbumImportPayload{Artist: AlbumImportArtistPayload{Name: "Burial"}}, "Untrue.zip", map[string]string{"cover.jpg": "cover-bytes"})
	return
	/*
		svc, _, user := newMusicTestService(t)
		var uploadedPath string
		var uploadedContentType string
		svc.s3 = fakeMusicImportS3Client(t, &uploadedPath, &uploadedContentType)
		t.Setenv("STORAGE_TYPE", "s3")
		t.Setenv("S3_BUCKET", "atoman-test")
		t.Setenv("S3_URL_PREFIX", "http://localhost:9100/atoman-dev")

		session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
			Status: AlbumImportStatusPendingUpload,
			Payload: AlbumImportPayload{
				Artist: AlbumImportArtistPayload{Name: "Burial"},
			},
		})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}

		archiveBody := newImportTestZipArchive(t, map[string]string{
			"cover.jpg": "cover-bytes",
		})

		updated, err := svc.UploadAlbumImportArchive(user, session.ID, "Untrue.zip", bytes.NewReader(archiveBody))
		if err != nil {
			t.Fatalf("upload archive: %v", err)
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(updated.PayloadJSON), &payload); err != nil {
			t.Fatalf("unmarshal payload json: %v", err)
		}

		derivedCover, _ := payload["derived_cover"].(string)
		if derivedCover == "" {
			t.Fatalf("expected derived_cover from s3 upload, got %#v", payload["derived_cover"])
		}
		if !strings.HasPrefix(derivedCover, "http://localhost:9100/atoman-dev/music/covers/uploads/users/") {
			t.Fatalf("unexpected derived_cover %q", derivedCover)
		}
		if uploadedPath == "" || uploadedContentType != "image/jpeg" {
			t.Fatalf("expected s3 upload, got path=%q contentType=%q", uploadedPath, uploadedContentType)
		}
	*/
}

func TestUploadAlbumImportArchiveQueuesAudioSourceAndPreservesMetadata(t *testing.T) {
	testQueuedArchiveUploadContract(t, AlbumImportPayload{Artist: AlbumImportArtistPayload{Name: "Ye"}, Album: AlbumImportAlbumPayload{Title: "2049"}}, "2049.zip", map[string]string{"01 - Bound 2049.mp3": "audio-1"})
	return
	/*
			svc, db, user := newMusicTestService(t)
			var uploadedPath string
			var uploadedContentType string
			svc.s3 = fakeMusicImportS3Client(t, &uploadedPath, &uploadedContentType)
			t.Setenv("STORAGE_TYPE", "s3")
			t.Setenv("S3_BUCKET", "atoman-test")
			t.Setenv("S3_URL_PREFIX", "http://localhost:9100/atoman-dev")

			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
				Status: AlbumImportStatusPendingUpload,
				Payload: AlbumImportPayload{
					Artist: AlbumImportArtistPayload{Name: "Ye"},
					Album:  AlbumImportAlbumPayload{Title: "2049"},
				},
			})
			if err != nil {
				t.Fatalf("create session: %v", err)
			}

			archiveBody := newImportTestZipArchive(t, map[string]string{
				"01 - Bound 2049.mp3":  "audio-1",
				"02 - Jesus Walks.mp3": "audio-2",
			})

			updated, err := svc.UploadAlbumImportArchive(user, session.ID, "2049.zip", bytes.NewReader(archiveBody))
			if err != nil {
				t.Fatalf("upload archive: %v", err)
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(updated.PayloadJSON), &payload); err != nil {
				t.Fatalf("unmarshal payload json: %v", err)
			}
			derivedTracks, ok := payload["derived_tracks"].([]any)
			if !ok || len(derivedTracks) != 2 {
				t.Fatalf("expected 2 derived tracks, got %#v", payload["derived_tracks"])
			}
			for _, rawTrack := range derivedTracks {
				trackMap, ok := rawTrack.(map[string]any)
				if !ok {
					t.Fatalf("expected track map, got %#v", rawTrack)
				}
				if stringValue(trackMap["audio_key"]) == "" || stringValue(trackMap["audio_url"]) == "" {
					t.Fatalf("expected audio upload metadata on derived track, got %#v", trackMap)
				}
			}

			if _, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
				Artist: AlbumImportArtistPayload{Name: "Ye"},
				Album: AlbumImportAlbumPayload{
					Title:       "2049",
					ReleaseYear: 2026,
					Tracks: []AlbumImportTrackPayload{
						{Title: "Bound 2049", TrackNumber: 1},
						{Title: "Jesus Walks", TrackNumber: 2},
					},
				},
			}); err != nil {
				t.Fatalf("commit session: %v", err)
			}

			var songs []model.Song
			if err := db.Joins("JOIN \"Albums\" ON \"Albums\".id = \"Songs\".album_id").
				Where("\"Albums\".title = ?", "2049").
				Order("\"Songs\".track_number ASC").
				Find(&songs).Error; err != nil {
				t.Fatalf("load songs: %v", err)
			}
			if len(songs) != 2 {
				t.Fatalf("expected 2 songs, got %#v", songs)
			}
			for _, song := range songs {
				if song.AudioURL == "" {
					t.Fatalf("expected persisted song audio url, got %#v", song)
				}
				if !strings.HasPrefix(song.AudioURL, "http://localhost:9100/atoman-dev/music/albums/") {
					t.Fatalf("unexpected persisted song audio url %q", song.AudioURL)
				}
			}
			if uploadedPath == "" || uploadedContentType == "" {
				t.Fatalf("expected s3 audio upload, got path=%q contentType=%q", uploadedPath, uploadedContentType)
			}
		}

		func testQueuedArchiveUploadContract(t *testing.T, submitted AlbumImportPayload, archiveName string, entries map[string]string) {
			t.Helper()
			svc, db, user := newMusicTestService(t)
			svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload, Payload: submitted})
			if err != nil {
				t.Fatal(err)
			}
			updated, err := svc.UploadAlbumImportArchive(user, session.ID, archiveName, bytes.NewReader(newImportTestZipArchive(t, entries)))
			if err != nil {
				t.Fatal(err)
			}
			payload, err := readAlbumImportPayloadMap(updated.PayloadJSON)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status != AlbumImportStatusQueued || payload["archive_name"] != archiveName || payload["derived_tracks"] != nil || payload["derived_cover"] != nil {
				t.Fatalf("request must only queue source: %#v", updated)
			}
			artist, _ := payload["artist"].(map[string]any)
			album, _ := payload["album"].(map[string]any)
			if stringValue(artist["name"]) != submitted.Artist.Name || stringValue(album["title"]) != submitted.Album.Title {
				t.Fatalf("submitted metadata was lost: %#v", payload)
			}
			var files []model.AlbumImportFile
			var jobs []model.AlbumImportJob
			if err := db.Where("import_id = ?", session.ID).Find(&files).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Where("import_id = ?", session.ID).Find(&jobs).Error; err != nil {
				t.Fatal(err)
			}
			if len(files) != 1 || files[0].UploadStatus != AlbumImportFileUploadStatusUploaded || len(jobs) != 1 || jobs[0].Status != AlbumImportJobStatusQueued {
				t.Fatalf("queued source state invalid: files=%#v jobs=%#v", files, jobs)
			}
	*/
}

func testQueuedArchiveUploadContract(t *testing.T, submitted AlbumImportPayload, archiveName string, entries map[string]string) {
	t.Helper()
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusPendingUpload, Payload: submitted})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UploadAlbumImportArchive(user, session.ID, archiveName, bytes.NewReader(newImportTestZipArchive(t, entries)))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := readAlbumImportPayloadMap(updated.PayloadJSON)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != AlbumImportStatusQueued || payload["archive_name"] != archiveName || payload["derived_tracks"] != nil || payload["derived_cover"] != nil {
		t.Fatalf("request must only queue source: %#v", updated)
	}
	artist, _ := payload["artist"].(map[string]any)
	album, _ := payload["album"].(map[string]any)
	if stringValue(artist["name"]) != submitted.Artist.Name || stringValue(album["title"]) != submitted.Album.Title {
		t.Fatalf("submitted metadata was lost: %#v", payload)
	}
	var files []model.AlbumImportFile
	var jobs []model.AlbumImportJob
	if err := db.Where("import_id = ?", session.ID).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("import_id = ?", session.ID).Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].UploadStatus != AlbumImportFileUploadStatusUploaded || len(jobs) != 1 || jobs[0].Status != AlbumImportJobStatusQueued {
		t.Fatalf("queued source state invalid: files=%#v jobs=%#v", files, jobs)
	}
}

func TestCommitAlbumImportSessionRollsBackArtistWhenAlbumCreateFails(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	prevHook := albumImportCreateAlbumHook
	albumImportCreateAlbumHook = func(_ *gorm.DB, _ *model.Album) error {
		return fmt.Errorf("forced album create failure")
	}
	defer func() {
		albumImportCreateAlbumHook = prevHook
	}()

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusReady,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{
				Name:      "Rollback Artist",
				LegalName: "Rollback Legal",
			},
			Album: AlbumImportAlbumPayload{
				Title: "LP1",
			},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if _, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{
			Name:      "Rollback Artist",
			LegalName: "Rollback Legal",
		},
		Album: AlbumImportAlbumPayload{
			Title: "LP1",
		},
	}); err == nil {
		t.Fatal("expected commit to fail when album create fails")
	}

	var artists int64
	if err := db.Model(&model.Artist{}).Where("name = ?", "Rollback Artist").Count(&artists).Error; err != nil {
		t.Fatalf("count artists: %v", err)
	}
	if artists != 0 {
		t.Fatalf("expected rollback artist not persisted, got %d", artists)
	}
}

func newImportTestZipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %s: %v", name, err)
		}
		if _, err := io.WriteString(w, content); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func assertDerivedTrackPresent(t *testing.T, tracks []any, title string, trackNumber int) {
	t.Helper()

	for _, raw := range tracks {
		track, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if track["title"] == title && track["track_number"] == float64(trackNumber) {
			return
		}
	}
	t.Fatalf("expected derived track %q #%d in %#v", title, trackNumber, tracks)
}

func fakeMusicImportS3Client(t *testing.T, capturedPath *string, capturedContentType *string) *s3.S3 {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPut {
			t.Fatalf("expected S3 PUT or DELETE, got %s", r.Method)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Header.Get("X-Amz-Copy-Source") != "" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<CopyObjectResult><ETag>"etag"</ETag><LastModified>2026-07-15T00:00:00Z</LastModified></CopyObjectResult>`)
			return
		}
		*capturedPath = r.URL.EscapedPath()
		*capturedContentType = r.Header.Get("Content-Type")
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

func fakeMusicPromotionS3Client(t *testing.T, sources, destinations, deleted *[]string) *s3.S3 {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/atoman-test/")
		switch r.Method {
		case http.MethodPut:
			*sources = append(*sources, r.Header.Get("X-Amz-Copy-Source"))
			*destinations = append(*destinations, key)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<CopyObjectResult><ETag>"etag"</ETag><LastModified>2026-07-15T00:00:00Z</LastModified></CopyObjectResult>`)
		case http.MethodDelete:
			*deleted = append(*deleted, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected S3 method %s", r.Method)
		}
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type fakeAlbumImportMultipartStore struct {
	uploadID   string
	signedURL  string
	objectBody []byte

	createCalls int
	createErr   error
	createErrAt int
	onCreate    func()

	createKey            string
	createContentType    string
	presignKey           string
	presignUploadID      string
	presignPartNumber    int
	completeKey          string
	completeUploadID     string
	completeCalls        int
	completedPartNumbers []int
	completedSize        int64
	abortedKeys          []string
	abortedUploadIDs     []string
	abortErr             error
	openCalls            int
	headCalls            int
	objectCompleted      bool
	objectSizeOverride   int64
	deletedKeys          []string
	deleteErr            error
}

func (f *fakeAlbumImportMultipartStore) CreateMultipartUpload(key string, contentType string) (string, error) {
	f.createCalls++
	if f.onCreate != nil {
		f.onCreate()
	}
	f.createKey = key
	f.createContentType = contentType
	if f.createErr != nil && (f.createErrAt == 0 || f.createCalls == f.createErrAt) {
		return "", f.createErr
	}
	if f.uploadID == "" {
		return "upload-test", nil
	}
	return f.uploadID, nil
}

func (f *fakeAlbumImportMultipartStore) PutObject(_ string, _ string, body io.Reader) error {
	data, err := io.ReadAll(body)
	f.objectBody = data
	return err
}

func (f *fakeAlbumImportMultipartStore) PresignUploadPart(key string, uploadID string, partNumber int, _ time.Duration) (string, error) {
	f.presignKey = key
	f.presignUploadID = uploadID
	f.presignPartNumber = partNumber
	if f.signedURL == "" {
		return "https://storage.test/upload", nil
	}
	return f.signedURL, nil
}

func (f *fakeAlbumImportMultipartStore) CompleteMultipartUpload(key string, uploadID string, parts []AlbumImportMultipartPartDTO) error {
	f.completeCalls++
	f.objectCompleted = true
	f.completeKey = key
	f.completeUploadID = uploadID
	f.completedPartNumbers = nil
	f.completedSize = 0
	for _, part := range parts {
		f.completedPartNumbers = append(f.completedPartNumbers, part.PartNumber)
		f.completedSize += part.Size
	}
	return nil
}

func (f *fakeAlbumImportMultipartStore) ObjectSize(_ string) (int64, error) {
	f.headCalls++
	if !f.objectCompleted {
		return 0, errors.New("object not found")
	}
	if f.objectSizeOverride > 0 {
		return f.objectSizeOverride, nil
	}
	return f.completedSize, nil
}

func (f *fakeAlbumImportMultipartStore) AbortMultipartUpload(key string, uploadID string) error {
	f.abortedKeys = append(f.abortedKeys, key)
	f.abortedUploadIDs = append(f.abortedUploadIDs, uploadID)
	return f.abortErr
}

func (f *fakeAlbumImportMultipartStore) OpenObject(_ string) (io.ReadCloser, error) {
	f.openCalls++
	return io.NopCloser(bytes.NewReader(f.objectBody)), nil
}

func (f *fakeAlbumImportMultipartStore) DeleteObject(key string) error {
	f.deletedKeys = append(f.deletedKeys, key)
	return f.deleteErr
}

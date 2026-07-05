package music

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"gorm.io/gorm"
)

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
	svc, _, user := newMusicTestService(t)
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
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 2, CompleteAlbumImportMultipartPartInput{
		ETag: "etag-2",
		Size: albumImportMultipartPartSize,
	}); err != nil {
		t.Fatalf("complete part: %v", err)
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

func TestCompleteAlbumImportMultipartCompletesSortedPartsExtractsArchiveAndDeletesObject(t *testing.T) {
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

	updated, err := svc.CompleteAlbumImportMultipart(user, session.ID)
	if err != nil {
		t.Fatalf("complete multipart: %v", err)
	}
	if updated.Status != AlbumImportStatusReady {
		t.Fatalf("expected ready status, got %#v", updated)
	}
	if store.completeKey != started.ObjectKey || store.completeUploadID != "upload-1" {
		t.Fatalf("unexpected complete call key=%q uploadID=%q", store.completeKey, store.completeUploadID)
	}
	if fmt.Sprint(store.completedPartNumbers) != "[1 2]" {
		t.Fatalf("expected sorted completed parts [1 2], got %#v", store.completedPartNumbers)
	}
	if len(store.deletedKeys) != 1 || store.deletedKeys[0] != started.ObjectKey {
		t.Fatalf("expected original archive deleted, got %#v", store.deletedKeys)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(updated.PayloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload json: %v", err)
	}
	if payload["archive_name"] != "Untrue.zip" {
		t.Fatalf("expected archive_name preserved, got %#v", payload["archive_name"])
	}
	if stringValue(payload["multipart_upload_id"]) != "" {
		t.Fatalf("expected multipart_upload_id removed, got %#v", payload["multipart_upload_id"])
	}
	derivedTracks, ok := payload["derived_tracks"].([]any)
	if !ok || len(derivedTracks) != 2 {
		t.Fatalf("expected 2 derived tracks, got %#v", payload["derived_tracks"])
	}
	assertDerivedTrackPresent(t, derivedTracks, "Untitled", 1)
	assertDerivedTrackPresent(t, derivedTracks, "Archangel", 2)
}

func TestCompleteAlbumImportMultipartKeepsReadyWhenCleanupDeleteFails(t *testing.T) {
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
		Size: albumImportMultipartPartSize,
	}); err != nil {
		t.Fatalf("complete part 1: %v", err)
	}

	updated, err := svc.CompleteAlbumImportMultipart(user, session.ID)
	if err != nil {
		t.Fatalf("complete multipart should ignore cleanup failure: %v", err)
	}
	if updated.Status != AlbumImportStatusReady {
		t.Fatalf("expected returned session ready, got %#v", updated)
	}

	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load stored session: %v", err)
	}
	if stored.Status != AlbumImportStatusReady {
		t.Fatalf("expected stored session to remain ready, got %#v", stored)
	}
	payload, err := readAlbumImportPayloadMap(stored.PayloadJSON)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	derivedTracks, ok := payload["derived_tracks"].([]any)
	if !ok || len(derivedTracks) != 1 {
		t.Fatalf("expected derived tracks preserved, got %#v", payload["derived_tracks"])
	}
	assertDerivedTrackPresent(t, derivedTracks, "Untitled", 1)
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

	committed, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
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
	})
	if err != nil {
		t.Fatalf("commit session: %v", err)
	}
	if committed.Status != AlbumImportStatusCommitted {
		t.Fatalf("expected committed status, got %#v", committed)
	}

	var artist model.Artist
	if err := db.Where("name = ?", "FKA twigs").First(&artist).Error; err != nil {
		t.Fatalf("load artist: %v", err)
	}
	if artist.LegalName != "Tahliah Debrett Barnett" {
		t.Fatalf("expected legal name persisted, got %#v", artist)
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
	if album.CoverURL != "" {
		t.Fatalf("expected empty album cover placeholder fallback, got %q", album.CoverURL)
	}
	for _, song := range songs {
		if song.AudioURL != "" {
			t.Fatalf("expected empty song audio placeholder fallback, got %q", song.AudioURL)
		}
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

	committed, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		ArtistID: existingArtist.ID.String(),
		Album: AlbumImportAlbumPayload{
			Title:       "Graduation",
			ReleaseYear: 2007,
			Tracks: []AlbumImportTrackPayload{
				{Title: "Good Morning", TrackNumber: 1},
			},
		},
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

func TestUploadAlbumImportArchiveTransitionsPendingUploadToReady(t *testing.T) {
	svc, _, user := newMusicTestService(t)

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
	if updated.Status != AlbumImportStatusReady {
		t.Fatalf("expected ready status, got %#v", updated)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(updated.PayloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload json: %v", err)
	}

	if payload["archive_name"] != archiveName {
		t.Fatalf("expected archive_name %q, got %#v", archiveName, payload["archive_name"])
	}
	if payload["derived_album_title"] != "Untrue (Deluxe)" {
		t.Fatalf("expected derived_album_title, got %#v", payload["derived_album_title"])
	}
	derivedTracks, ok := payload["derived_tracks"].([]any)
	if !ok {
		t.Fatalf("expected derived_tracks array, got %#v", payload["derived_tracks"])
	}
	if len(derivedTracks) != 2 {
		t.Fatalf("expected 2 derived tracks, got %#v", derivedTracks)
	}
	assertDerivedTrackPresent(t, derivedTracks, "Untitled", 1)
	assertDerivedTrackPresent(t, derivedTracks, "Archangel", 2)
}

func TestUploadAlbumImportArchiveStoresDerivedCoverInS3(t *testing.T) {
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
		"01 - Untitled.mp3": "",
		"cover.jpg":         "cover-bytes",
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
}

func TestUploadAlbumImportArchiveStoresDerivedAudioInS3AndCommitPersistsSongURLs(t *testing.T) {
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
		if !strings.HasPrefix(song.AudioURL, "http://localhost:9100/atoman-dev/music/audio/uploads/users/") {
			t.Fatalf("unexpected persisted song audio url %q", song.AudioURL)
		}
	}
	if uploadedPath == "" || uploadedContentType == "" {
		t.Fatalf("expected s3 audio upload, got path=%q contentType=%q", uploadedPath, uploadedContentType)
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
		if r.Method != http.MethodPut {
			t.Fatalf("expected S3 PUT, got %s", r.Method)
		}
		*capturedPath = r.URL.EscapedPath()
		*capturedContentType = r.Header.Get("Content-Type")
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

type fakeAlbumImportMultipartStore struct {
	uploadID   string
	signedURL  string
	objectBody []byte

	createCalls int

	createKey            string
	createContentType    string
	presignKey           string
	presignUploadID      string
	presignPartNumber    int
	completeKey          string
	completeUploadID     string
	completedPartNumbers []int
	deletedKeys          []string
	deleteErr            error
}

func (f *fakeAlbumImportMultipartStore) CreateMultipartUpload(key string, contentType string) (string, error) {
	f.createCalls++
	f.createKey = key
	f.createContentType = contentType
	if f.uploadID == "" {
		return "upload-test", nil
	}
	return f.uploadID, nil
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
	f.completeKey = key
	f.completeUploadID = uploadID
	f.completedPartNumbers = nil
	for _, part := range parts {
		f.completedPartNumbers = append(f.completedPartNumbers, part.PartNumber)
	}
	return nil
}

func (f *fakeAlbumImportMultipartStore) AbortMultipartUpload(_ string, _ string) error {
	return nil
}

func (f *fakeAlbumImportMultipartStore) OpenObject(_ string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(f.objectBody)), nil
}

func (f *fakeAlbumImportMultipartStore) DeleteObject(key string) error {
	f.deletedKeys = append(f.deletedKeys, key)
	return f.deleteErr
}

func TestUploadAlbumImportArchiveWithRealHeyMamaZip(t *testing.T) {
	svc, _, user := newMusicTestService(t)

	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{
		Status: AlbumImportStatusPendingUpload,
		Payload: AlbumImportPayload{
			Artist: AlbumImportArtistPayload{Name: "Kanye West"},
		},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	archivePath := filepath.Clean(os.ExpandEnv("$HOME/Downloads/12-hey-mama-uploadable.zip"))
	file, err := os.Open(archivePath)
	if err != nil {
		t.Fatalf("open real zip: %v", err)
	}
	defer file.Close()

	updated, err := svc.UploadAlbumImportArchive(user, session.ID, filepath.Base(archivePath), file)
	if err != nil {
		t.Fatalf("upload real zip: %v", err)
	}
	if updated.Status != AlbumImportStatusReady {
		t.Fatalf("expected ready status, got %#v", updated.Status)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(updated.PayloadJSON), &payload); err != nil {
		t.Fatalf("unmarshal payload json: %v", err)
	}
	derivedTracks, ok := payload["derived_tracks"].([]any)
	if !ok {
		t.Fatalf("expected derived_tracks array, got %#v", payload["derived_tracks"])
	}
	if len(derivedTracks) == 0 {
		t.Fatalf("expected derived tracks from real zip, got none")
	}
}

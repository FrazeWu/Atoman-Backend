package music

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"atoman/internal/model"
	"atoman/internal/platform/apperr"
	"atoman/internal/platform/authctx"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestRegisterAlbumImportFilesDetectsSupportedRolesAndInputModes(t *testing.T) {
	tests := []struct {
		name     string
		files    []AlbumImportFileInput
		wantMode string
		roles    []string
	}{
		{
			name:     "single archive",
			files:    []AlbumImportFileInput{albumImportFileInput("album.tar.gz", 1024)},
			wantMode: AlbumImportInputModeArchive,
			roles:    []string{AlbumImportFileRoleArchive},
		},
		{
			name: "direct files",
			files: []AlbumImportFileInput{
				albumImportFileInput("01.flac", 1024),
				albumImportFileInput("album.cue", 128),
				albumImportFileInput("01.lrc", 128),
				albumImportFileInput("cover.avif", 256),
			},
			wantMode: AlbumImportInputModeFiles,
			roles:    []string{AlbumImportFileRoleAudio, AlbumImportFileRoleCue, AlbumImportFileRoleLyrics, AlbumImportFileRoleCover},
		},
		{
			name: "folder",
			files: []AlbumImportFileInput{
				albumImportFileInput("Album/CD1/01.opus", 1024),
				albumImportFileInput("Album/CD2/01.aiff", 1024),
			},
			wantMode: AlbumImportInputModeFolder,
			roles:    []string{AlbumImportFileRoleAudio, AlbumImportFileRoleAudio},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, _, user := newMusicTestService(t)
			svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
			if err != nil {
				t.Fatal(err)
			}

			registered, err := svc.RegisterAlbumImportFiles(user, session.ID, RegisterAlbumImportFilesInput{Files: test.files})
			if err != nil {
				t.Fatalf("register files: %v", err)
			}
			if registered.InputMode != test.wantMode || len(registered.Files) != len(test.files) {
				t.Fatalf("unexpected session: %#v", registered)
			}
			for index, file := range registered.Files {
				if file.Role != test.roles[index] || file.UploadID == "" || file.PartSize <= 0 || file.CompletedPartsJSON != "[]" {
					t.Fatalf("unexpected registered file %d: %#v", index, file)
				}
				for _, component := range []string{user.ID.String(), session.ID.String(), file.ID.String()} {
					if !strings.Contains(file.SourceKey, component) {
						t.Fatalf("source key %q does not contain %q", file.SourceKey, component)
					}
				}
				if strings.Contains(file.SourceKey, file.RelativePath) {
					t.Fatalf("source key must not use relative path: %q", file.SourceKey)
				}
			}
		})
	}
}

func TestRegisterAlbumImportFilesPersistsCleanupWhenDatabaseAndAbortFail(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{abortErr: errors.New("abort failed")}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	callback := "test:fail_album_import_file_writeback"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AlbumImportFile{}).TableName() {
			tx.AddError(errors.New("file writeback failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })

	if _, err := svc.RegisterAlbumImportFiles(user, session.ID, RegisterAlbumImportFilesInput{Files: []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)}}); err == nil {
		t.Fatal("expected file insert failure")
	}
	if len(store.abortedKeys) != 1 {
		t.Fatalf("expected created multipart to be aborted, got %#v", store)
	}
	var persisted model.AlbumImportSession
	if err := db.First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(persisted.PayloadJSON, "cleanup_targets") || !strings.Contains(persisted.PayloadJSON, store.abortedKeys[0]) {
		t.Fatalf("failed abort was not persisted on session: %s", persisted.PayloadJSON)
	}
}

func TestRegisterAlbumImportFilesAbortsCreatedUploadsWhenStorageCreationFails(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{createErr: errors.New("storage create failed"), createErrAt: 2}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RegisterAlbumImportFiles(user, session.ID, RegisterAlbumImportFilesInput{Files: []AlbumImportFileInput{albumImportFileInput("one.flac", 1024), albumImportFileInput("two.flac", 1024)}}); err == nil {
		t.Fatal("expected storage creation failure")
	}
	var persisted model.AlbumImportSession
	if err := db.Preload("Files").First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if len(store.abortedKeys) != 1 || persisted.Status != AlbumImportStatusFailed || len(persisted.Files) != 2 || persisted.Files[0].UploadStatus != AlbumImportFileUploadStatusFailed {
		t.Fatalf("failed registration left completable session: %#v", persisted)
	}
}

func TestRegisterAlbumImportFilesCreatesMultipartAfterPreparationTransaction(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeAlbumImportMultipartStore{onCreate: func() {
		var prepared model.AlbumImportSession
		if err := db.Preload("Files").First(&prepared, "id = ?", session.ID).Error; err != nil {
			t.Errorf("read prepared state: %v", err)
			return
		}
		if prepared.Status != AlbumImportStatusPendingUpload || len(prepared.Files) != 1 || prepared.Files[0].UploadID != "" {
			t.Errorf("multipart creation occurred before preparation committed: %#v", prepared)
		}
	}}
	svc.albumImportMultipart = store
	if _, err := svc.RegisterAlbumImportFiles(user, session.ID, RegisterAlbumImportFilesInput{Files: []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)}}); err != nil {
		t.Fatal(err)
	}
}

func TestDetectAlbumImportFileRoleSupportsConfiguredWhitelist(t *testing.T) {
	tests := map[string]string{
		"zip": AlbumImportFileRoleArchive, "rar": AlbumImportFileRoleArchive, "7z": AlbumImportFileRoleArchive,
		"tar": AlbumImportFileRoleArchive, "tar.gz": AlbumImportFileRoleArchive, "tgz": AlbumImportFileRoleArchive,
		"tar.bz2": AlbumImportFileRoleArchive, "tar.xz": AlbumImportFileRoleArchive,
		"mp3": AlbumImportFileRoleAudio, "flac": AlbumImportFileRoleAudio, "wav": AlbumImportFileRoleAudio,
		"m4a": AlbumImportFileRoleAudio, "aac": AlbumImportFileRoleAudio, "ogg": AlbumImportFileRoleAudio,
		"opus": AlbumImportFileRoleAudio, "aiff": AlbumImportFileRoleAudio, "aif": AlbumImportFileRoleAudio,
		"wma": AlbumImportFileRoleAudio, "ape": AlbumImportFileRoleAudio, "alac": AlbumImportFileRoleAudio,
		"cue": AlbumImportFileRoleCue,
		"lrc": AlbumImportFileRoleLyrics, "txt": AlbumImportFileRoleLyrics,
		"jpg": AlbumImportFileRoleCover, "jpeg": AlbumImportFileRoleCover, "png": AlbumImportFileRoleCover,
		"webp": AlbumImportFileRoleCover, "avif": AlbumImportFileRoleCover, "heic": AlbumImportFileRoleCover,
		"heif": AlbumImportFileRoleCover, "tiff": AlbumImportFileRoleCover, "tif": AlbumImportFileRoleCover, "bmp": AlbumImportFileRoleCover,
	}
	for extension, wantRole := range tests {
		t.Run(extension, func(t *testing.T) {
			role, format, err := detectAlbumImportFileRole("ALBUM." + extension)
			if err != nil || role != wantRole || format != extension {
				t.Fatalf("detect .%s: role=%q format=%q err=%v", extension, role, format, err)
			}
		})
	}
	if _, _, err := detectAlbumImportFileRole("album.exe"); err == nil {
		t.Fatal("expected unsupported type to fail")
	}
	for _, extension := range []string{"tbz2", "txz"} {
		if _, _, err := detectAlbumImportFileRole("album." + extension); err == nil {
			t.Fatalf("expected unconfirmed alias .%s to fail", extension)
		}
	}
}

func TestNormalizeAlbumImportFilesIgnoresSystemMetadata(t *testing.T) {
	files, inputMode, err := normalizeAlbumImportFiles([]AlbumImportFileInput{
		albumImportFileInput("Album/01 - Intro.flac", 1024),
		albumImportFileInput("Album/._01 - Intro.flac", 512),
		albumImportFileInput("Album/__MACOSX/._02 - Hidden.mp3", 512),
		albumImportFileInput("Album/.DS_Store", 512),
		albumImportFileInput("Album/Thumbs.db", 512),
		albumImportFileInput("Album/.hidden/03 - Hidden.flac", 512),
	}, albumImportUploadLimitsFromEnv())
	if err != nil {
		t.Fatal(err)
	}
	if inputMode != AlbumImportInputModeFolder || len(files) != 1 || files[0].RelativePath != "Album/01 - Intro.flac" {
		t.Fatalf("unexpected normalized files: mode=%q files=%#v", inputMode, files)
	}

	_, _, err = normalizeAlbumImportFiles([]AlbumImportFileInput{
		albumImportFileInput("Album/._01 - Intro.flac", 512),
	}, albumImportUploadLimitsFromEnv())
	assertAlbumImportValidationError(t, err)
}

func TestRegisterAlbumImportFilesRejectsInvalidDescriptors(t *testing.T) {
	tests := []struct {
		name  string
		files []AlbumImportFileInput
	}{
		{name: "empty list"},
		{name: "empty filename", files: []AlbumImportFileInput{{RelativePath: "track.flac", FileSize: 1}}},
		{name: "empty path", files: []AlbumImportFileInput{{FileName: "track.flac", FileSize: 1}}},
		{name: "absolute unix path", files: []AlbumImportFileInput{{RelativePath: "/tmp/track.flac", FileName: "track.flac", FileSize: 1}}},
		{name: "absolute windows path", files: []AlbumImportFileInput{{RelativePath: `C:\\music\\track.flac`, FileName: "track.flac", FileSize: 1}}},
		{name: "parent segment", files: []AlbumImportFileInput{{RelativePath: "disc/../track.flac", FileName: "track.flac", FileSize: 1}}},
		{name: "mismatched basename", files: []AlbumImportFileInput{{RelativePath: "disc/track.flac", FileName: "other.flac", FileSize: 1}}},
		{name: "zero size", files: []AlbumImportFileInput{{RelativePath: "track.flac", FileName: "track.flac"}}},
		{name: "unsupported", files: []AlbumImportFileInput{albumImportFileInput("notes.exe", 1)}},
		{name: "archive mixed with audio", files: []AlbumImportFileInput{albumImportFileInput("album.zip", 1), albumImportFileInput("track.flac", 1)}},
		{name: "multiple archives", files: []AlbumImportFileInput{albumImportFileInput("one.zip", 1), albumImportFileInput("two.rar", 1)}},
		{name: "multiple folder roots", files: []AlbumImportFileInput{albumImportFileInput("AlbumA/01.flac", 1), albumImportFileInput("AlbumB/02.flac", 1)}},
		{name: "folder mixed with flat", files: []AlbumImportFileInput{albumImportFileInput("Album/01.flac", 1), albumImportFileInput("02.flac", 1)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, _, user := newMusicTestService(t)
			store := &fakeAlbumImportMultipartStore{}
			svc.albumImportMultipart = store
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = svc.RegisterAlbumImportFiles(user, session.ID, RegisterAlbumImportFilesInput{Files: test.files})
			assertAlbumImportValidationError(t, err)
			if store.createCalls != 0 {
				t.Fatalf("validation must happen before multipart creation, got %d calls", store.createCalls)
			}
		})
	}
}

func TestRegisterAlbumImportFilesEnforcesConfigurableLimits(t *testing.T) {
	const gib = int64(1024 * 1024 * 1024)
	tests := []struct {
		name  string
		files []AlbumImportFileInput
		env   map[string]string
	}{
		{name: "default single file", files: []AlbumImportFileInput{albumImportFileInput("track.flac", 4*gib+1)}},
		{name: "default total", files: []AlbumImportFileInput{albumImportFileInput("one.flac", 4*gib), albumImportFileInput("two.flac", 4*gib), albumImportFileInput("three.flac", 2*gib+1)}},
		{name: "default count", files: albumImportFileInputs(5001)},
		{name: "default track count", files: albumImportFileInputs(301)},
		{name: "overridden file bytes", files: []AlbumImportFileInput{albumImportFileInput("track.flac", 1025)}, env: map[string]string{albumImportMaxFileBytesEnv: "1024"}},
		{name: "overridden total bytes", files: []AlbumImportFileInput{albumImportFileInput("one.flac", 600), albumImportFileInput("two.flac", 425)}, env: map[string]string{albumImportMaxTotalBytesEnv: "1024"}},
		{name: "overridden count", files: albumImportFileInputs(3), env: map[string]string{albumImportMaxFilesEnv: "2"}},
		{name: "overridden track count", files: albumImportFileInputs(3), env: map[string]string{albumImportMaxTracksEnv: "2"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			svc, _, user := newMusicTestService(t)
			store := &fakeAlbumImportMultipartStore{}
			svc.albumImportMultipart = store
			session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = svc.RegisterAlbumImportFiles(user, session.ID, RegisterAlbumImportFilesInput{Files: test.files})
			assertAlbumImportValidationError(t, err)
			if store.createCalls != 0 {
				t.Fatalf("limits must be checked before multipart creation, got %d calls", store.createCalls)
			}
		})
	}
}

func TestAlbumImportUploadLimitsInvalidEnvironmentFallsBackToDefaults(t *testing.T) {
	t.Setenv(albumImportMaxTotalBytesEnv, "invalid")
	t.Setenv(albumImportMaxFileBytesEnv, "-1")
	t.Setenv(albumImportMaxFilesEnv, "0")
	t.Setenv(albumImportMaxTracksEnv, "invalid")
	limits := albumImportUploadLimitsFromEnv()
	if limits.MaxTotalBytes != defaultAlbumImportMaxTotalBytes || limits.MaxFileBytes != defaultAlbumImportMaxFileBytes || limits.MaxFiles != defaultAlbumImportMaxFiles || limits.MaxTracks != defaultAlbumImportMaxTracks {
		t.Fatalf("unexpected fallback limits: %#v", limits)
	}
}

func TestBuildAlbumImportFileDTODoesNotExposeUploadIDAndUsesEmptyPartsArray(t *testing.T) {
	dto := buildAlbumImportFileDTO(model.AlbumImportFile{UploadID: "secret-upload-id"})
	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret-upload-id") || strings.Contains(string(body), "uploadId") {
		t.Fatalf("upload id leaked: %s", body)
	}
	if !strings.Contains(string(body), `"completedParts":[]`) {
		t.Fatalf("expected completedParts array: %s", body)
	}
}

func TestAlbumImportFileMultipartSupportsResumeAndCompletion(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "upload-file", signedURL: "https://storage.test/part-2"}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 32*1024*1024)})

	upload, err := svc.CreateAlbumImportFilePartUpload(user, session.ID, file.ID, 2, CreateAlbumImportMultipartPartInput{})
	if err != nil {
		t.Fatalf("presign file part: %v", err)
	}
	if upload.PartNumber != 2 || upload.UploadURL != store.signedURL || store.presignKey != file.SourceKey || store.presignUploadID != file.UploadID {
		t.Fatalf("unexpected presign result=%#v store=%#v", upload, store)
	}

	for _, part := range []CompleteAlbumImportMultipartPartInput{
		{ETag: "etag-2", Size: albumImportMultipartPartSize},
		{ETag: "etag-1", Size: albumImportMultipartPartSize},
		{ETag: "etag-2-new", Size: albumImportMultipartPartSize},
	} {
		partNumber := 2
		if part.ETag == "etag-1" {
			partNumber = 1
		}
		if _, err := svc.CompleteAlbumImportFilePart(user, session.ID, file.ID, partNumber, part); err != nil {
			t.Fatalf("complete part %d: %v", partNumber, err)
		}
	}

	var persisted model.AlbumImportFile
	if err := db.First(&persisted, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	dto := buildAlbumImportFileDTO(persisted)
	if len(dto.CompletedParts) != 2 || dto.CompletedParts[0].PartNumber != 1 || dto.CompletedParts[1].ETag != "etag-2-new" {
		t.Fatalf("expected sorted resumable parts, got %#v", dto.CompletedParts)
	}

	completed, err := svc.CompleteAlbumImportFile(user, session.ID, file.ID)
	if err != nil {
		t.Fatalf("complete file multipart: %v", err)
	}
	if completed.UploadStatus != AlbumImportFileUploadStatusUploaded || fmt.Sprint(store.completedPartNumbers) != "[1 2]" {
		t.Fatalf("unexpected completed file=%#v store=%#v", completed, store)
	}
}

func TestCompleteLastAlbumImportFileQueuesEarlySubmission(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := svc.RegisterAlbumImportFiles(user, session.ID, RegisterAlbumImportFilesInput{Files: []AlbumImportFileInput{
		albumImportFileInput("01.flac", 1024),
		albumImportFileInput("02.flac", 1024),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{Name: "Early Artist"},
		Album:  AlbumImportAlbumPayload{Title: "Early Album"},
	}); err != nil {
		t.Fatalf("submit import before upload completion: %v", err)
	}

	for index, file := range registered.Files {
		if _, err := svc.CompleteAlbumImportFilePart(user, session.ID, file.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: fmt.Sprintf("etag-%d", index), Size: file.Size}); err != nil {
			t.Fatalf("complete file part %d: %v", index, err)
		}
		if _, err := svc.CompleteAlbumImportFile(user, session.ID, file.ID); err != nil {
			t.Fatalf("complete file %d: %v", index, err)
		}
		var persisted model.AlbumImportSession
		if err := db.First(&persisted, "id = ?", session.ID).Error; err != nil {
			t.Fatal(err)
		}
		if index == 0 && persisted.Status != AlbumImportStatusUploading {
			t.Fatalf("import queued before all uploads completed: %s", persisted.Status)
		}
	}

	var persisted model.AlbumImportSession
	if err := db.First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != AlbumImportStatusQueued || persisted.Stage != AlbumImportStageQueued {
		t.Fatalf("early submission was not queued after final upload: %#v", persisted)
	}
	if _, err := svc.CompleteAlbumImportSession(user, session.ID); err != nil {
		t.Fatalf("queued completion should be idempotent: %v", err)
	}
	var jobs int64
	if err := db.Model(&model.AlbumImportJob{}).Where("import_id = ?", session.ID).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("expected one import job, got %d", jobs)
	}
}

func TestCommitAlbumImportQueuesWhenFilesWereAlreadyUploaded(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{
		albumImportFileInput("Album/01 - Intro.flac", 1024),
	})
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
		t.Fatal(err)
	}

	committed, err := svc.CommitAlbumImportSession(user, session.ID, CommitAlbumImportSessionInput{
		Artist: AlbumImportArtistPayload{Name: "Folder Artist"},
		Album:  AlbumImportAlbumPayload{Title: "Folder Album"},
	})
	if err != nil {
		t.Fatalf("submit completed folder import: %v", err)
	}
	if committed.Status != AlbumImportStatusQueued || committed.Stage != AlbumImportStageQueued {
		t.Fatalf("completed upload was not queued: %#v", committed)
	}

	var jobs int64
	if err := db.Model(&model.AlbumImportJob{}).Where("import_id = ?", session.ID).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("expected one import job, got %d", jobs)
	}
}

func TestCompleteAlbumImportSessionRequiresUploadedArchiveOrAudio(t *testing.T) {
	tests := []struct {
		name      string
		fileName  string
		markReady bool
	}{
		{name: "upload incomplete", fileName: "track.flac"},
		{name: "no archive or audio", fileName: "album.cue", markReady: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
			session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput(test.fileName, 1024)})
			if test.markReady {
				if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
					t.Fatal(err)
				}
			}
			_, err := svc.CompleteAlbumImportSession(user, session.ID)
			if err == nil {
				t.Fatal("expected session complete validation error")
			}
		})
	}
}

func TestCompleteAlbumImportFileRejectsActualObjectSizeMismatch(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{objectSizeOverride: 2048}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	if _, err := svc.CompleteAlbumImportFilePart(user, session.ID, file.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: "etag", Size: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteAlbumImportFile(user, session.ID, file.ID); err == nil {
		t.Fatal("expected trusted object size mismatch to fail")
	}
	var persisted model.AlbumImportFile
	if err := db.First(&persisted, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.UploadStatus != AlbumImportFileUploadStatusFailed || len(store.deletedKeys) != 1 || store.deletedKeys[0] != file.SourceKey || store.headCalls == 0 {
		t.Fatalf("size mismatch was not rejected and cleaned: file=%#v store=%#v", persisted, store)
	}
	var failedSession model.AlbumImportSession
	if err := db.First(&failedSession, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if failedSession.Status != AlbumImportStatusFailed || failedSession.Stage != AlbumImportStageFailed || failedSession.ErrorMessage == "" {
		t.Fatalf("size mismatch did not fail session: %#v", failedSession)
	}
	if failedSession.ExpiresAt == nil || failedSession.ExpiresAt.Before(time.Now().UTC().Add(6*24*time.Hour)) {
		t.Fatalf("failed upload must retain source for seven days: %#v", failedSession.ExpiresAt)
	}
}

func TestCompleteAlbumImportFileKeepsObjectWhenFailureStateCannotPersist(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{objectSizeOverride: 2048}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	if _, err := svc.CompleteAlbumImportFilePart(user, session.ID, file.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: "etag", Size: 1024}); err != nil {
		t.Fatal(err)
	}
	callback := "test:fail_new_size_mismatch_state"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AlbumImportSession{}).TableName() {
			tx.AddError(errors.New("failure state write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	if _, err := svc.CompleteAlbumImportFile(user, session.ID, file.ID); err == nil || len(store.deletedKeys) != 0 {
		t.Fatalf("must retain object when failure state write fails: err=%v deleted=%#v", err, store.deletedKeys)
	}
}

func TestRetryAlbumImportFileRecreatesMultipartAfterUploadSizeMismatch(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{objectSizeOverride: 2048}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	if _, err := svc.CompleteAlbumImportFilePart(user, session.ID, file.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: "etag", Size: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteAlbumImportFile(user, session.ID, file.ID); err == nil {
		t.Fatal("expected upload size mismatch")
	}
	store.objectCompleted = false
	store.objectSizeOverride = 0

	retrying, err := svc.RetryAlbumImportFile(user, session.ID, file.ID)
	if err != nil {
		t.Fatalf("retry failed upload: %v", err)
	}
	if retrying.Status != AlbumImportStatusUploading || retrying.Stage != AlbumImportStageUpload || retrying.ErrorMessage != "" || len(retrying.Files) != 1 {
		t.Fatalf("unexpected retry session: %#v", retrying)
	}
	retried := retrying.Files[0]
	if retried.UploadStatus != AlbumImportFileUploadStatusUploading || retried.SourceKey == file.SourceKey || retried.ErrorMessage != "" || len(buildAlbumImportFileDTO(retried).CompletedParts) != 0 || store.createCalls != 2 {
		t.Fatalf("failed upload was not rebuilt: file=%#v store=%#v", retried, store)
	}
	var jobs int64
	if err := db.Model(&model.AlbumImportJob{}).Where("import_id = ?", session.ID).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("upload retry must wait for completion before queueing, got %d jobs", jobs)
	}
}

func TestCompleteAlbumImportFileRecoversCompletedObjectFromCompletingState(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{objectCompleted: true, objectSizeOverride: 1024}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Updates(map[string]any{
		"upload_status":        AlbumImportFileUploadStatusCompleting,
		"completed_parts_json": `[{"partNumber":1,"etag":"etag","size":1024}]`,
	}).Error; err != nil {
		t.Fatal(err)
	}

	completed, err := svc.CompleteAlbumImportFile(user, session.ID, file.ID)
	if err != nil {
		t.Fatalf("recover completing file: %v", err)
	}
	if completed.UploadStatus != AlbumImportFileUploadStatusUploaded || store.completeCalls != 0 || store.headCalls == 0 {
		t.Fatalf("completed object was not reconciled: file=%#v store=%#v", completed, store)
	}
}

func TestCompleteAlbumImportSessionRejectsProcessingAndTerminalStatuses(t *testing.T) {
	for _, status := range []string{
		AlbumImportStatusExtracting, AlbumImportStatusAnalyzing, AlbumImportStatusTranscoding,
		AlbumImportStatusReady, AlbumImportStatusNeedsAttention, AlbumImportStatusFailed,
	} {
		t.Run(status, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
			session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
			if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("status", status).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := svc.CompleteAlbumImportSession(user, session.ID); err == nil {
				t.Fatalf("expected complete to reject %s", status)
			}
			var jobs int64
			if err := db.Model(&model.AlbumImportJob{}).Where("import_id = ?", session.ID).Count(&jobs).Error; err != nil {
				t.Fatal(err)
			}
			if jobs != 0 {
				t.Fatalf("status %s unexpectedly queued a job", status)
			}
		})
	}
}

func TestCompleteAlbumImportSessionQueuesOneJobIdempotently(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
		t.Fatal(err)
	}

	first, err := svc.CompleteAlbumImportSession(user, session.ID)
	if err != nil {
		t.Fatalf("first complete session: %v", err)
	}
	second, err := svc.CompleteAlbumImportSession(user, session.ID)
	if err != nil {
		t.Fatalf("idempotent complete session: %v", err)
	}
	if first.Status != AlbumImportStatusQueued || second.Status != AlbumImportStatusQueued || first.Stage != AlbumImportStageQueued {
		t.Fatalf("unexpected queued sessions: first=%#v second=%#v", first, second)
	}
	var jobs []model.AlbumImportJob
	if err := db.Where("import_id = ?", session.ID).Find(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Status != AlbumImportJobStatusQueued || jobs[0].MaxAttempts != 3 {
		t.Fatalf("expected one queued job, got %#v", jobs)
	}
}

func TestCancelAlbumImportSessionAbortsOnlyUnfinishedFiles(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, _ := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{
		albumImportFileInput("one.flac", 1024),
		albumImportFileInput("two.flac", 1024),
	})
	var files []model.AlbumImportFile
	if err := db.Where("import_id = ?", session.ID).Order("file_name").Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&files[0]).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
		t.Fatal(err)
	}
	expiredAt := time.Now().UTC().Add(-time.Hour)
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("expires_at", expiredAt).Error; err != nil {
		t.Fatal(err)
	}

	cancelStartedAt := time.Now().UTC()
	canceled, err := svc.CancelAlbumImportSession(user, session.ID)
	if err != nil {
		t.Fatalf("cancel session: %v", err)
	}
	if canceled.Status != AlbumImportStatusCanceled || canceled.Stage != AlbumImportStageCanceled {
		t.Fatalf("unexpected canceled state: %#v", canceled)
	}
	if len(store.abortedKeys) != 1 || store.abortedKeys[0] != files[1].SourceKey {
		t.Fatalf("expected only unfinished upload aborted, got %#v", store.abortedKeys)
	}
	var persisted model.AlbumImportSession
	if err := db.Preload("Files").First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	minimumExpiresAt := cancelStartedAt.Add(7*24*time.Hour - time.Hour)
	if len(persisted.Files) != 2 || persisted.ExpiresAt == nil || persisted.ExpiresAt.Before(minimumExpiresAt) || len(store.deletedKeys) != 0 {
		t.Fatalf("canceled import must retain sources for seven-day cleanup: session=%#v store=%#v", persisted, store)
	}
	if _, err := svc.CompleteAlbumImportSession(user, session.ID); err == nil {
		t.Fatal("canceled session must not complete")
	}
}

func TestCancelAlbumImportSessionRequiresStorageForUnfinishedMultipart(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
	session, _ := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	svc.albumImportMultipart = nil

	_, err := svc.CancelAlbumImportSession(user, session.ID)
	appErr := apperr.FromError(err)
	if appErr == nil || appErr.HTTPStatus != http.StatusServiceUnavailable || appErr.Code != "storage.unavailable" {
		t.Fatalf("expected storage.unavailable 503, got %#v", appErr)
	}
	var persisted model.AlbumImportSession
	if err := db.Preload("Files").First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != AlbumImportStatusUploading || persisted.Stage != AlbumImportStageUpload || len(persisted.Files) != 1 || persisted.Files[0].UploadStatus != AlbumImportFileUploadStatusUploading {
		t.Fatalf("storage failure must leave import uploading: %#v", persisted)
	}
}

func TestCancelAlbumImportSessionRejectsCompletingFile(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusCompleting).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CancelAlbumImportSession(user, session.ID); err == nil {
		t.Fatal("expected cancel to reject completing file")
	}
	var persisted model.AlbumImportSession
	if err := db.First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != AlbumImportStatusUploading || len(store.abortedKeys) != 0 || len(store.deletedKeys) != 0 {
		t.Fatalf("cancel raced with completing upload: session=%#v store=%#v", persisted, store)
	}
}

func TestDeleteAlbumImportFileAbortsAndDeletesBeforeProcessing(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})

	if err := svc.DeleteAlbumImportFile(user, session.ID, file.ID); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	var count int64
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 || len(store.abortedKeys) != 1 || store.abortedKeys[0] != file.SourceKey {
		t.Fatalf("file not deleted cleanly: count=%d store=%#v", count, store)
	}
}

func TestDeleteAlbumImportFileDeletesCompletedSourceObject(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAlbumImportFile(user, session.ID, file.ID); err != nil {
		t.Fatal(err)
	}
	if len(store.deletedKeys) != 1 || store.deletedKeys[0] != file.SourceKey {
		t.Fatalf("completed source object was orphaned: %#v", store.deletedKeys)
	}
}

func TestCompletingAlbumImportFileCannotBeDeletedOrReplaced(t *testing.T) {
	for _, operation := range []string{"delete", "replace"} {
		t.Run(operation, func(t *testing.T) {
			svc, db, user := newMusicTestService(t)
			store := &fakeAlbumImportMultipartStore{}
			svc.albumImportMultipart = store
			session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
			if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusCompleting).Error; err != nil {
				t.Fatal(err)
			}
			var err error
			if operation == "delete" {
				err = svc.DeleteAlbumImportFile(user, session.ID, file.ID)
			} else {
				_, err = svc.ReplaceAlbumImportFile(user, session.ID, file.ID, albumImportFileInput("new.flac", 1024))
			}
			if err == nil {
				t.Fatalf("expected %s to reject a completing file", operation)
			}
			var count int64
			if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 1 || len(store.abortedKeys) != 0 || len(store.deletedKeys) != 0 {
				t.Fatalf("completing file was mutated: count=%d store=%#v", count, store)
			}
		})
	}
}

func TestRetryAlbumImportFileReusesAndResetsFailedJob(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	now := time.Now().UTC()
	job := model.AlbumImportJob{
		ImportID: session.ID, Status: AlbumImportJobStatusFailed, Stage: AlbumImportStageFailed,
		Attempts: 2, MaxAttempts: 3, LockedBy: "dead-worker", LockedAt: &now, HeartbeatAt: &now,
		StartedAt: &now, FinishedAt: &now, LastError: "transcode failed",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Updates(map[string]any{
		"upload_status": AlbumImportFileUploadStatusUploaded, "processing_status": AlbumImportFileProcessingStatusFailed, "error_message": "transcode failed",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"status": AlbumImportStatusNeedsAttention, "stage": AlbumImportStageReady, "error_message": "track failed", "payload_json": `{"error_message":"track failed"}`,
	}).Error; err != nil {
		t.Fatal(err)
	}

	queued, err := svc.RetryAlbumImportFile(user, session.ID, file.ID)
	if err != nil {
		t.Fatalf("retry file: %v", err)
	}
	if queued.Status != AlbumImportStatusQueued {
		t.Fatalf("expected queued session, got %#v", queued)
	}
	queuedDTO := buildAlbumImportDTO(queued)
	if len(queuedDTO.Errors) != 0 || len(queuedDTO.Files) != 1 || queuedDTO.Files[0].ProcessingStatus != AlbumImportFileProcessingStatusPending {
		t.Fatalf("retry response retained stale errors: %#v", queuedDTO)
	}
	var retriedFile model.AlbumImportFile
	if err := db.First(&retriedFile, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retriedFile.ProcessingStatus != AlbumImportFileProcessingStatusPending || retriedFile.ErrorMessage != "" {
		t.Fatalf("file was not reset: %#v", retriedFile)
	}
	var retriedJob model.AlbumImportJob
	if err := db.First(&retriedJob, "import_id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retriedJob.ID != job.ID || retriedJob.Status != AlbumImportJobStatusQueued || retriedJob.Attempts != 0 || retriedJob.LockedBy != "" || retriedJob.LockedAt != nil || retriedJob.LastError != "" {
		t.Fatalf("job was not reused and reset: %#v", retriedJob)
	}
}

func TestRetryAlbumImportFileRequeuesSessionLevelProcessingFailure(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	svc.albumImportMultipart = &fakeAlbumImportMultipartStore{}
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("album.zip", 1024)})
	job := model.AlbumImportJob{
		ImportID: session.ID, Status: AlbumImportJobStatusFailed, Stage: AlbumImportStageFailed,
		Attempts: 3, MaxAttempts: 3, LastError: "no space left on device",
	}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Updates(map[string]any{
		"upload_status": AlbumImportFileUploadStatusUploaded, "processing_status": AlbumImportFileProcessingStatusPending,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"status": AlbumImportStatusNeedsAttention, "stage": AlbumImportStageFailed, "error_message": "no space left on device",
	}).Error; err != nil {
		t.Fatal(err)
	}

	queued, err := svc.RetryAlbumImportFile(user, session.ID, file.ID)
	if err != nil {
		t.Fatalf("retry session-level failure: %v", err)
	}
	if queued.Status != AlbumImportStatusQueued || queued.ErrorMessage != "" {
		t.Fatalf("expected clean queued session, got %#v", queued)
	}
	var retriedJob model.AlbumImportJob
	if err := db.First(&retriedJob, "import_id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retriedJob.Status != AlbumImportJobStatusQueued || retriedJob.Attempts != 0 || retriedJob.LastError != "" {
		t.Fatalf("job was not reset: %#v", retriedJob)
	}
}

func TestReplaceAlbumImportFileResetsRecordAndCreatesNewMultipart(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("old.mp3", 1024)})

	replaced, err := svc.ReplaceAlbumImportFile(user, session.ID, file.ID, albumImportFileInput("disc/new.flac", 2048))
	if err != nil {
		t.Fatalf("replace file: %v", err)
	}
	if replaced.ID != file.ID.String() || replaced.RelativePath != "disc/new.flac" || replaced.Role != AlbumImportFileRoleAudio || replaced.DetectedFormat != "flac" || replaced.Size != 2048 || replaced.UploadStatus != AlbumImportFileUploadStatusUploading || len(replaced.CompletedParts) != 0 {
		t.Fatalf("unexpected replacement: %#v", replaced)
	}
	if store.createCalls != 2 || len(store.abortedKeys) != 1 || store.abortedKeys[0] != file.SourceKey {
		t.Fatalf("unexpected storage calls: %#v", store)
	}
	var persisted model.AlbumImportFile
	if err := db.First(&persisted, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.ErrorMessage != "" || persisted.ProcessingStatus != AlbumImportFileProcessingStatusPending || persisted.CompletedPartsJSON != "[]" {
		t.Fatalf("replacement state was not reset: %#v", persisted)
	}
}

func TestReplaceAlbumImportFileDeletesCompletedOldSourceObject(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("old.mp3", 1024)})
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReplaceAlbumImportFile(user, session.ID, file.ID, albumImportFileInput("new.flac", 2048)); err != nil {
		t.Fatal(err)
	}
	if len(store.deletedKeys) != 1 || store.deletedKeys[0] != file.SourceKey {
		t.Fatalf("completed old source object was orphaned: %#v", store.deletedKeys)
	}
}

func TestReplaceAlbumImportFileUsesNewGenerationKeyForSameFormat(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("old.flac", 1024)})
	replaced, err := svc.ReplaceAlbumImportFile(user, session.ID, file.ID, albumImportFileInput("new.flac", 1024))
	if err != nil {
		t.Fatal(err)
	}
	if replaced.SourceKey == file.SourceKey {
		t.Fatalf("replacement reused source key %q", replaced.SourceKey)
	}
}

func TestReplaceAlbumImportFilePersistsFailedCleanupForSameFormatAndSize(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{deleteErr: errors.New("cleanup failed")}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("old.flac", 1024)})
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
		t.Fatal(err)
	}
	replaced, err := svc.ReplaceAlbumImportFile(user, session.ID, file.ID, albumImportFileInput("new.flac", 1024))
	if err != nil {
		t.Fatal(err)
	}
	if replaced.SourceKey == file.SourceKey {
		t.Fatalf("same-format replacement reused old key %q", file.SourceKey)
	}
	var persisted model.AlbumImportFile
	if err := db.First(&persisted, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(persisted.CleanupJSON, file.SourceKey) || !strings.Contains(persisted.CleanupJSON, "delete") {
		t.Fatalf("failed cleanup target was not persisted: %#v", persisted)
	}
}

func TestReplaceAlbumImportFilePersistsNewUploadCleanupWhenDatabaseAndAbortFail(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("old.flac", 1024)})
	store.abortErr = errors.New("abort failed")
	callback := "test:fail_album_import_file_replace"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AlbumImportFile{}).TableName() {
			tx.AddError(errors.New("file update failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })

	if _, err := svc.ReplaceAlbumImportFile(user, session.ID, file.ID, albumImportFileInput("new.flac", 1024)); err == nil {
		t.Fatal("expected replacement database failure")
	}
	if len(store.abortedKeys) != 1 || store.abortedKeys[0] != store.createKey {
		t.Fatalf("new multipart was not aborted: %#v", store)
	}
	var persisted model.AlbumImportSession
	if err := db.First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(persisted.PayloadJSON, "cleanup_targets") || !strings.Contains(persisted.PayloadJSON, store.createKey) {
		t.Fatalf("failed replacement cleanup was not persisted: %s", persisted.PayloadJSON)
	}
}

func TestDeleteAlbumImportFilePersistsFailedCleanupOnSoftDeletedRow(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{deleteErr: errors.New("cleanup failed")}
	svc.albumImportMultipart = store
	session, file := registerAlbumImportFilesForTest(t, svc, user, []AlbumImportFileInput{albumImportFileInput("track.flac", 1024)})
	if err := db.Model(&model.AlbumImportFile{}).Where("id = ?", file.ID).Update("upload_status", AlbumImportFileUploadStatusUploaded).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteAlbumImportFile(user, session.ID, file.ID); err != nil {
		t.Fatal(err)
	}
	var deleted model.AlbumImportFile
	if err := db.Unscoped().First(&deleted, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(deleted.CleanupJSON, file.SourceKey) || !strings.Contains(deleted.CleanupJSON, "delete") {
		t.Fatalf("soft-deleted cleanup target was not persisted: %#v", deleted)
	}
}

func TestAlbumImportFileOperationsRejectCrossUserAndMismatchedSession(t *testing.T) {
	svc, _, owner := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	firstSession, firstFile := registerAlbumImportFilesForTest(t, svc, owner, []AlbumImportFileInput{albumImportFileInput("one.flac", 1024)})
	secondSession, secondFile := registerAlbumImportFilesForTest(t, svc, owner, []AlbumImportFileInput{albumImportFileInput("two.flac", 1024)})
	other := authctx.CurrentUser{ID: uuid.New(), Username: "other", Role: authctx.RoleUser}

	calls := []struct {
		name string
		call func(authctx.CurrentUser, uuid.UUID, uuid.UUID) error
	}{
		{name: "part URL", call: func(user authctx.CurrentUser, sessionID, fileID uuid.UUID) error {
			_, err := svc.CreateAlbumImportFilePartUpload(user, sessionID, fileID, 1, CreateAlbumImportMultipartPartInput{})
			return err
		}},
		{name: "part complete", call: func(user authctx.CurrentUser, sessionID, fileID uuid.UUID) error {
			_, err := svc.CompleteAlbumImportFilePart(user, sessionID, fileID, 1, CompleteAlbumImportMultipartPartInput{ETag: "etag", Size: 1})
			return err
		}},
		{name: "file complete", call: func(user authctx.CurrentUser, sessionID, fileID uuid.UUID) error {
			_, err := svc.CompleteAlbumImportFile(user, sessionID, fileID)
			return err
		}},
		{name: "delete", call: func(user authctx.CurrentUser, sessionID, fileID uuid.UUID) error {
			return svc.DeleteAlbumImportFile(user, sessionID, fileID)
		}},
		{name: "retry", call: func(user authctx.CurrentUser, sessionID, fileID uuid.UUID) error {
			_, err := svc.RetryAlbumImportFile(user, sessionID, fileID)
			return err
		}},
		{name: "replace", call: func(user authctx.CurrentUser, sessionID, fileID uuid.UUID) error {
			_, err := svc.ReplaceAlbumImportFile(user, sessionID, fileID, albumImportFileInput("new.flac", 1))
			return err
		}},
	}

	for _, test := range calls {
		t.Run(test.name+" cross user", func(t *testing.T) {
			if err := test.call(other, firstSession.ID, firstFile.ID); err == nil {
				t.Fatal("expected cross-user access to fail")
			}
		})
		t.Run(test.name+" mismatched session", func(t *testing.T) {
			if err := test.call(owner, firstSession.ID, secondFile.ID); err == nil {
				t.Fatalf("expected file from session %s to be rejected for %s", secondSession.ID, firstSession.ID)
			}
		})
	}
}

func TestLegacyAlbumImportMultipartPersistsArchiveFileAndQueuesWithoutReadingObject(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{uploadID: "legacy-upload", objectBody: []byte("must not be read")}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 1024, ContentType: "application/zip"})
	if err != nil {
		t.Fatalf("legacy start: %v", err)
	}
	var files []model.AlbumImportFile
	if err := db.Where("import_id = ?", session.ID).Find(&files).Error; err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Role != AlbumImportFileRoleArchive || files[0].UploadID != "legacy-upload" || files[0].SourceKey != started.ObjectKey {
		t.Fatalf("legacy start did not persist archive file: %#v", files)
	}
	for _, component := range []string{user.ID.String(), session.ID.String(), files[0].ID.String()} {
		if !strings.Contains(files[0].SourceKey, component) {
			t.Fatalf("legacy source key %q missing %q", files[0].SourceKey, component)
		}
	}

	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: "etag-1", Size: 1024}); err != nil {
		t.Fatalf("legacy part complete: %v", err)
	}
	if err := db.First(&files[0], "id = ?", files[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if parts := buildAlbumImportFileDTO(files[0]).CompletedParts; len(parts) != 1 || parts[0].ETag != "etag-1" {
		t.Fatalf("legacy part was not synchronized: %#v", parts)
	}

	queued, err := svc.CompleteAlbumImportMultipart(user, session.ID)
	if err != nil {
		t.Fatalf("legacy complete: %v", err)
	}
	again, err := svc.CompleteAlbumImportMultipart(user, session.ID)
	if err != nil {
		t.Fatalf("legacy idempotent complete: %v", err)
	}
	if queued.Status != AlbumImportStatusQueued || again.Status != AlbumImportStatusQueued || store.completeCalls != 1 || store.openCalls != 0 || len(store.deletedKeys) != 0 {
		t.Fatalf("legacy complete performed synchronous processing: queued=%#v again=%#v store=%#v", queued, again, store)
	}
	var jobs int64
	if err := db.Model(&model.AlbumImportJob{}).Where("import_id = ?", session.ID).Count(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	if jobs != 1 {
		t.Fatalf("expected one job, got %d", jobs)
	}
}

func TestLegacyAlbumImportMultipartUsesConfigurableFileLimit(t *testing.T) {
	t.Setenv(albumImportMaxFileBytesEnv, "1024")
	svc, _, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 1025})
	assertAlbumImportValidationError(t, err)
	if store.createCalls != 0 {
		t.Fatalf("legacy limit must be checked before storage, got %d calls", store.createCalls)
	}
}

func TestLegacyAlbumImportMultipartRejectsActualSizeAndCanRestart(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{objectSizeOverride: 2048}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: "etag", Size: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CompleteAlbumImportMultipart(user, session.ID); err == nil {
		t.Fatal("expected legacy actual size mismatch")
	}
	var failed model.AlbumImportSession
	if err := db.Preload("Files").First(&failed, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if failed.Status != AlbumImportStatusFailed || len(failed.Files) != 1 || failed.Files[0].UploadStatus != AlbumImportFileUploadStatusFailed || len(store.deletedKeys) != 1 {
		t.Fatalf("legacy mismatch was not persisted: session=%#v store=%#v", failed, store)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 1024}); err != nil {
		t.Fatalf("restart legacy upload: %v", err)
	}
	if store.createCalls != 2 {
		t.Fatalf("expected a new multipart upload after mismatch, got %d", store.createCalls)
	}
}

func TestLegacyAlbumImportMultipartKeepsObjectWhenFailureStateCannotPersist(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{objectSizeOverride: 2048}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 1024}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CompleteAlbumImportMultipartPart(user, session.ID, 1, CompleteAlbumImportMultipartPartInput{ETag: "etag", Size: 1024}); err != nil {
		t.Fatal(err)
	}
	callback := "test:fail_legacy_size_mismatch_state"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AlbumImportSession{}).TableName() {
			tx.AddError(errors.New("failure state write failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
	if _, err := svc.CompleteAlbumImportMultipart(user, session.ID); err == nil || len(store.deletedKeys) != 0 {
		t.Fatalf("must retain object when failure state write fails: err=%v deleted=%#v", err, store.deletedKeys)
	}
}

func TestLegacyAlbumImportMultipartPersistsCleanupWhenDatabaseAndAbortFail(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{abortErr: errors.New("abort failed")}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	callback := "test:fail_legacy_album_import_file_create"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.AlbumImportFile{}).TableName() {
			tx.AddError(errors.New("legacy file insert failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })

	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "album.zip", FileSize: 1024}); err == nil {
		t.Fatal("expected legacy database failure")
	}
	if len(store.abortedKeys) != 1 {
		t.Fatalf("legacy multipart was not aborted: %#v", store)
	}
	var persisted model.AlbumImportSession
	if err := db.First(&persisted, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(persisted.PayloadJSON, "cleanup_targets") || !strings.Contains(persisted.PayloadJSON, store.abortedKeys[0]) {
		t.Fatalf("legacy cleanup failure was not persisted: %s", persisted.PayloadJSON)
	}
}

func TestLegacyAlbumImportMultipartAbortsOverwrittenUpload(t *testing.T) {
	svc, _, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "first.zip", FileSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "second.zip", FileSize: 1024}); err != nil {
		t.Fatal(err)
	}
	if len(store.abortedKeys) != 1 || store.abortedKeys[0] != first.ObjectKey {
		t.Fatalf("overwritten legacy upload was not aborted: %#v", store.abortedKeys)
	}
}

func TestLegacyAlbumImportMultipartRejectsReplacementWhileArchiveCompleting(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	store := &fakeAlbumImportMultipartStore{}
	svc.albumImportMultipart = store
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "first.zip", FileSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AlbumImportFile{}).Where("import_id = ?", session.ID).Update("upload_status", AlbumImportFileUploadStatusCompleting).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartAlbumImportMultipart(user, session.ID, StartAlbumImportMultipartInput{FileName: "second.zip", FileSize: 1024}); err == nil || store.createCalls != 1 {
		t.Fatalf("completing upload must not be replaced: err=%v store=%#v", err, store)
	}
	var persisted model.AlbumImportFile
	if err := db.First(&persisted, "import_id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.SourceKey != first.ObjectKey || persisted.UploadID == "" {
		t.Fatalf("completing archive was overwritten: %#v", persisted)
	}
}

func registerAlbumImportFilesForTest(t *testing.T, svc *Service, user authctx.CurrentUser, files []AlbumImportFileInput) (model.AlbumImportSession, model.AlbumImportFile) {
	t.Helper()
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := svc.RegisterAlbumImportFiles(user, session.ID, RegisterAlbumImportFilesInput{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if len(registered.Files) == 0 {
		t.Fatal("expected at least one registered file")
	}
	return registered, registered.Files[0]
}

func albumImportFileInput(relativePath string, size int64) AlbumImportFileInput {
	parts := strings.Split(strings.ReplaceAll(relativePath, `\\`, "/"), "/")
	return AlbumImportFileInput{RelativePath: relativePath, FileName: parts[len(parts)-1], FileSize: size, ContentType: "application/octet-stream"}
}

func albumImportFileInputs(count int) []AlbumImportFileInput {
	files := make([]AlbumImportFileInput, 0, count)
	for index := 0; index < count; index++ {
		files = append(files, albumImportFileInput(fmt.Sprintf("%03d.flac", index), 1))
	}
	return files
}

func assertAlbumImportValidationError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected validation error")
	}
	var appErr *apperr.AppError
	if !errors.As(err, &appErr) || appErr.Code != "validation.invalid_request" {
		t.Fatalf("expected validation.invalid_request, got %v", err)
	}
}

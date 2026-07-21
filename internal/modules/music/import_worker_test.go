package music

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type importWorkerStore struct {
	mu        sync.Mutex
	deleted   []string
	aborted   []string
	deleteErr error
}

func (s *importWorkerStore) DeleteObject(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted = append(s.deleted, key)
	return nil
}

func (s *importWorkerStore) AbortMultipartUpload(key, uploadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aborted = append(s.aborted, key+":"+uploadID)
	return nil
}

func newImportWorkerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	_, db, _ := newMusicTestService(t)
	return db
}

func createImportWorkerJob(t *testing.T, db *gorm.DB, status string) model.AlbumImportJob {
	t.Helper()
	session := model.AlbumImportSession{Status: status, Stage: AlbumImportStageQueued, PayloadJSON: "{}"}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	job := model.AlbumImportJob{ImportID: session.ID, Status: AlbumImportJobStatusQueued, Stage: AlbumImportStageQueued, MaxAttempts: 2}
	if err := db.Create(&job).Error; err != nil {
		t.Fatal(err)
	}
	return job
}

func TestImportWorkerClaimDoesNotDuplicateJob(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	w := NewImportWorker(db, &importWorkerStore{}, "worker-a")
	var wg sync.WaitGroup
	claimed := make(chan bool, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := w.Claim(context.Background())
			if err != nil {
				t.Error(err)
			}
			claimed <- ok
		}()
	}
	wg.Wait()
	close(claimed)
	count := 0
	for ok := range claimed {
		if ok {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("claimed %d jobs, want 1", count)
	}
	var stored model.AlbumImportJob
	if err := db.First(&stored, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != AlbumImportJobStatusRunning || stored.Attempts != 1 || stored.LockedBy == "" {
		t.Fatalf("unexpected claimed job: %#v", stored)
	}
}

func TestImportWorkerDoesNotClaimCanceledSession(t *testing.T) {
	db := newImportWorkerTestDB(t)
	createImportWorkerJob(t, db, AlbumImportStatusCanceled)
	_, ok, err := NewImportWorker(db, nil, "worker").Claim(context.Background())
	if err != nil || ok {
		t.Fatalf("claim canceled job: ok=%v err=%v", ok, err)
	}
}

func TestImportWorkerHeartbeatAndRetryLimit(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	w := NewImportWorker(db, nil, "worker")
	claimed, ok, err := w.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := w.Heartbeat(context.Background(), claimed.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.Retry(context.Background(), claimed.ID, errors.New("temporary")); err != nil {
		t.Fatal(err)
	}
	var retried model.AlbumImportJob
	if err := db.First(&retried, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retried.Status != AlbumImportJobStatusQueued || retried.NextAttemptAt == nil || retried.Attempts != 1 {
		t.Fatalf("unexpected retry: %#v", retried)
	}
	if err := db.Model(&model.AlbumImportJob{}).Where("id = ?", job.ID).Update("next_attempt_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	claimed, ok, err = w.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("second claim: %v %v", ok, err)
	}
	if err := w.Retry(context.Background(), claimed.ID, errors.New("permanent")); err != nil {
		t.Fatal(err)
	}
	var failed model.AlbumImportJob
	var session model.AlbumImportSession
	_ = db.First(&failed, "id = ?", job.ID).Error
	_ = db.First(&session, "id = ?", job.ImportID).Error
	if failed.Status != AlbumImportJobStatusFailed || session.Status != AlbumImportStatusNeedsAttention || session.ExpiresAt == nil {
		t.Fatalf("limit not applied: job=%#v session=%#v", failed, session)
	}
}

func TestImportWorkerCleanupRetriesTargets(t *testing.T) {
	db := newImportWorkerTestDB(t)
	expires := time.Now().UTC().Add(-time.Minute)
	session := model.AlbumImportSession{Status: AlbumImportStatusCanceled, Stage: AlbumImportStageCanceled, ExpiresAt: &expires, PayloadJSON: `{"cleanup_targets":[{"action":"delete","key":"source.zip"}]}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{ImportID: session.ID, FileName: "song.flac", SourceKey: "part.flac", UploadID: "upload-1", CleanupJSON: `[{"action":"abort","key":"part.flac","upload_id":"upload-1"}]`}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	store := &importWorkerStore{}
	w := NewImportWorker(db, store, "worker")
	if err := w.CleanupExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.deleted) != 1 || len(store.aborted) != 1 {
		t.Fatalf("cleanup calls: delete=%v abort=%v", store.deleted, store.aborted)
	}
	var after model.AlbumImportSession
	if err := db.First(&after, "id = ?", session.ID).Error; err == nil {
		t.Fatal("expected cleaned session to be soft deleted")
	}

	failed := model.AlbumImportSession{Status: AlbumImportStatusFailed, Stage: AlbumImportStageFailed, ExpiresAt: &expires, PayloadJSON: `{"cleanup_targets":[{"action":"delete","key":"retry.zip"}]}`}
	if err := db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}
	store.deleteErr = errors.New("storage down")
	if err := w.CleanupExpired(context.Background()); err == nil {
		t.Fatal("expected cleanup error")
	}
	var retained model.AlbumImportSession
	if err := db.First(&retained, "id = ?", failed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retained.ID == uuid.Nil {
		t.Fatal("cleanup target was lost")
	}
}

func TestImportWorkerCleanupRemovesCanceledFileSourceWithoutRecordedTarget(t *testing.T) {
	db := newImportWorkerTestDB(t)
	expires := time.Now().UTC().Add(-time.Minute)
	session := model.AlbumImportSession{Status: AlbumImportStatusCanceled, Stage: AlbumImportStageCanceled, ExpiresAt: &expires, PayloadJSON: `{}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{ImportID: session.ID, FileName: "uploaded.flac", SourceKey: "uploaded.flac", UploadStatus: AlbumImportFileUploadStatusUploaded, CleanupJSON: "[]"}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	store := &importWorkerStore{}
	if err := NewImportWorker(db, store, "worker").CleanupExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "uploaded.flac" {
		t.Fatalf("source object was not deleted: %v", store.deleted)
	}
}

func TestImportWorkerRunOnceWithoutProcessorDoesNotConsumeAttempt(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	w := NewImportWorker(db, nil, "worker")
	processed, err := w.RunOnce(context.Background(), nil)
	if err != nil || !processed {
		t.Fatalf("run once: processed=%v err=%v", processed, err)
	}
	var stored model.AlbumImportJob
	if err := db.First(&stored, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != AlbumImportJobStatusQueued || stored.Attempts != 0 || stored.NextAttemptAt != nil {
		t.Fatalf("placeholder consumed job: %#v", stored)
	}
}

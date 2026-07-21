package music

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"atoman/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type importWorkerStore struct {
	mu           sync.Mutex
	deleted      []string
	aborted      []string
	deleteErr    error
	deleteErrFor map[string]error
}

func (s *importWorkerStore) DeleteObject(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	if err := s.deleteErrFor[key]; err != nil {
		return err
	}
	s.deleted = append(s.deleted, key)
	return nil
}

type importProcessorFunc func(context.Context, model.AlbumImportJob, func() error) error

func (f importProcessorFunc) Process(ctx context.Context, job model.AlbumImportJob, heartbeat func() error) error {
	return f(ctx, job, heartbeat)
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
	if err != nil || processed {
		t.Fatalf("run once: processed=%v err=%v", processed, err)
	}
	var stored model.AlbumImportJob
	if err := db.First(&stored, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != AlbumImportJobStatusQueued || stored.Attempts != 0 || stored.NextAttemptAt != nil || stored.StartedAt != nil {
		t.Fatalf("placeholder consumed job: %#v", stored)
	}
}

func TestImportWorkerRunOnceWithoutProcessorStillCleansExpiredSession(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	expires := time.Now().UTC().Add(-time.Minute)
	session := model.AlbumImportSession{Status: AlbumImportStatusCanceled, ExpiresAt: &expires, PayloadJSON: `{"cleanup_targets":[{"action":"delete","key":"expired"}]}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	store := &importWorkerStore{}
	processed, err := NewImportWorker(db, store, "worker").RunOnce(context.Background(), nil)
	if err != nil || processed {
		t.Fatalf("run once: processed=%v err=%v", processed, err)
	}
	if len(store.deleted) != 1 || store.deleted[0] != "expired" {
		t.Fatalf("cleanup was skipped: %v", store.deleted)
	}
	var stored model.AlbumImportJob
	if err := db.First(&stored, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Attempts != 0 || stored.StartedAt != nil {
		t.Fatalf("nil processor claimed job: %#v", stored)
	}
}

func TestImportWorkerCompleteDoesNotOverwriteCanceledSession(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	w := NewImportWorker(db, nil, "worker")
	claimed, ok, err := w.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", job.ImportID).Updates(map[string]any{"status": AlbumImportStatusCanceled, "stage": AlbumImportStageCanceled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := w.Complete(context.Background(), claimed.ID); err == nil {
		t.Fatal("expected lost lease error")
	}
	var stored model.AlbumImportJob
	var session model.AlbumImportSession
	_ = db.First(&stored, "id = ?", job.ID).Error
	_ = db.First(&session, "id = ?", job.ImportID).Error
	if stored.Status != AlbumImportJobStatusRunning || session.Status != AlbumImportStatusCanceled {
		t.Fatalf("cancellation overwritten: job=%s session=%s", stored.Status, session.Status)
	}
}

func TestImportWorkerRetryDoesNotOverwriteCanceledSession(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	w := NewImportWorker(db, nil, "worker")
	claimed, ok, err := w.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", job.ImportID).Update("status", AlbumImportStatusCanceled).Error; err != nil {
		t.Fatal(err)
	}
	if err := w.Retry(context.Background(), claimed.ID, errors.New("retry")); err == nil {
		t.Fatal("expected lost lease error")
	}
	var stored model.AlbumImportJob
	_ = db.First(&stored, "id = ?", job.ID).Error
	if stored.Status != AlbumImportJobStatusRunning {
		t.Fatalf("retry changed canceled job: %s", stored.Status)
	}
}

func TestImportWorkerCleanupPersistsPartialProgress(t *testing.T) {
	db := newImportWorkerTestDB(t)
	expires := time.Now().UTC().Add(-time.Minute)
	session := model.AlbumImportSession{Status: AlbumImportStatusFailed, ExpiresAt: &expires, PayloadJSON: `{"cleanup_targets":[{"action":"delete","key":"first"},{"action":"delete","key":"second"}]}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	store := &importWorkerStore{deleteErrFor: map[string]error{"second": errors.New("down")}}
	w := NewImportWorker(db, store, "worker")
	if err := w.CleanupExpired(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	var retained model.AlbumImportSession
	if err := db.First(&retained, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(retained.PayloadJSON, "first") || !strings.Contains(retained.PayloadJSON, "second") {
		t.Fatalf("cleanup progress not persisted: %s", retained.PayloadJSON)
	}
	store.deleteErrFor = nil
	if err := w.CleanupExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(store.deleted, ","); got != "first,second" {
		t.Fatalf("retried completed target: %s", got)
	}
}

func TestImportWorkerCleanupPersistsFileTargetsAndTreatsMissingObjectAsSuccess(t *testing.T) {
	db := newImportWorkerTestDB(t)
	expires := time.Now().UTC().Add(-time.Minute)
	session := model.AlbumImportSession{Status: AlbumImportStatusFailed, ExpiresAt: &expires, PayloadJSON: `{}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{ImportID: session.ID, FileName: "part", SourceKey: "part", UploadID: "upload", CleanupJSON: `[{"action":"abort","key":"part","upload_id":"upload"},{"action":"delete","key":"later"}]`}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	store := &importWorkerStore{deleteErrFor: map[string]error{"later": errors.New("down")}}
	w := NewImportWorker(db, store, "worker")
	if err := w.CleanupExpired(context.Background()); err == nil {
		t.Fatal("expected failure")
	}
	var retained model.AlbumImportFile
	if err := db.First(&retained, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(retained.CleanupJSON, `"key":"part"`) || !strings.Contains(retained.CleanupJSON, "later") {
		t.Fatalf("file cleanup progress not persisted: %s", retained.CleanupJSON)
	}
	store.deleteErrFor = map[string]error{"later": errors.New("NoSuchKey")}
	if err := w.CleanupExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestImportWorkerProcessorReceivesWorkingHeartbeat(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	w := NewImportWorker(db, nil, "worker")
	processor := importProcessorFunc(func(ctx context.Context, claimed model.AlbumImportJob, heartbeat func() error) error {
		if err := db.Model(&model.AlbumImportJob{}).Where("id = ?", claimed.ID).Update("heartbeat_at", nil).Error; err != nil {
			return err
		}
		return heartbeat()
	})
	processed, err := w.RunOnce(context.Background(), processor)
	if err != nil || !processed {
		t.Fatalf("run: processed=%v err=%v", processed, err)
	}
	var stored model.AlbumImportJob
	if err := db.First(&stored, "id = ?", job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.HeartbeatAt == nil {
		t.Fatal("processor heartbeat did not update lease")
	}
}

func TestImportWorkerAutomaticallyHeartbeatsBlockingProcessor(t *testing.T) {
	db := newImportWorkerTestDB(t)
	createImportWorkerJob(t, db, AlbumImportStatusQueued)
	w := NewImportWorker(db, nil, "worker")
	w.heartbeatInterval = 5 * time.Millisecond
	processor := importProcessorFunc(func(ctx context.Context, claimed model.AlbumImportJob, heartbeat func() error) error {
		if err := db.Model(&model.AlbumImportJob{}).Where("id = ?", claimed.ID).Update("heartbeat_at", nil).Error; err != nil {
			return err
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			var job model.AlbumImportJob
			if err := db.First(&job, "id = ?", claimed.ID).Error; err != nil {
				return err
			}
			if job.HeartbeatAt != nil {
				return nil
			}
			time.Sleep(5 * time.Millisecond)
		}
		return errors.New("automatic heartbeat was not written")
	})
	if _, err := w.RunOnce(context.Background(), processor); err != nil {
		t.Fatal(err)
	}
}

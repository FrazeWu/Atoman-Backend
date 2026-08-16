package music

import (
	"context"
	"encoding/json"
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

func TestImportWorkerFinalizesSubmittedImportAfterProcessing(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	called := uuid.Nil
	worker := NewImportWorker(db, nil, "worker").WithCompletionFinalizer(func(_ context.Context, importID uuid.UUID) error {
		called = importID
		return nil
	})
	processed, err := worker.RunOnce(context.Background(), importProcessorFunc(func(context.Context, model.AlbumImportJob, func() error) error {
		return nil
	}))
	if err != nil || !processed {
		t.Fatalf("process job: processed=%v err=%v", processed, err)
	}
	if called != job.ImportID {
		t.Fatalf("finalizer import id = %s, want %s", called, job.ImportID)
	}
}

func TestImportWorkerSurfacesBackgroundHeartbeatFailure(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	worker := NewImportWorker(db, nil, "worker")
	claimed, ok, err := worker.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	worker.heartbeatInterval = time.Millisecond
	if err := db.Model(&model.AlbumImportJob{}).Where("id = ?", job.ID).Update("locked_by", "replacement").Error; err != nil {
		t.Fatal(err)
	}
	err = worker.processWithHeartbeat(context.Background(), importProcessorFunc(func(context.Context, model.AlbumImportJob, func() error) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	}), claimed)
	if err == nil || !strings.Contains(err.Error(), "heartbeat") {
		t.Fatalf("expected background heartbeat error, got %v", err)
	}
}

func TestImportWorkerLogsQueueWaitDurationAndErrorKind(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	var events []importWorkerEvent
	previousSink := logMusicImportEventSink
	logMusicImportEventSink = func(entry importWorkerEvent) {
		events = append(events, entry)
	}
	t.Cleanup(func() { logMusicImportEventSink = previousSink })

	now := job.CreatedAt.Add(3 * time.Minute)
	worker := NewImportWorker(db, nil, "worker")
	worker.now = func() time.Time { return now }
	processed, err := worker.RunOnce(context.Background(), importProcessorFunc(func(context.Context, model.AlbumImportJob, func() error) error {
		now = now.Add(2 * time.Second)
		return errors.New("object storage unavailable")
	}))
	if err != nil || !processed {
		t.Fatalf("run worker: processed=%v err=%v", processed, err)
	}
	if len(events) != 2 {
		t.Fatalf("expected claimed and requeued events, got %#v", events)
	}
	if events[0].event != "claimed" || events[0].queueWait < 3*time.Minute || events[0].queueWait > 3*time.Minute+time.Millisecond {
		t.Fatalf("unexpected claimed event: %#v", events[0])
	}
	if events[1].event != "requeued" || events[1].duration < 2*time.Second || events[1].duration > 2*time.Second+time.Millisecond || events[1].errorKind != "storage" {
		t.Fatalf("unexpected requeued event: %#v", events[1])
	}
}

func TestImportWorkerRetriesSubmittedReadyImportFinalization(t *testing.T) {
	svc, db, user := newMusicTestService(t)
	session, err := svc.CreateAlbumImportSession(user, CreateAlbumImportSessionInput{Status: AlbumImportStatusReady})
	if err != nil {
		t.Fatalf("create import session: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"commit_request": CommitAlbumImportSessionInput{
		Artist: completeAlbumImportArtistPayload("Retry Artist"),
		Album: AlbumImportAlbumPayload{
			Title: "Retry Album", CoverURL: "https://cdn.test/retry.jpg", ReleaseDate: "2020-01-01",
			Tracks: []AlbumImportTrackPayload{{Title: "Retry Track", TrackNumber: 1}},
		},
		ArtistSource: "artist source",
		AlbumSource:  "album source",
	}, "derived_tracks": []map[string]any{{"title": "Retry Track", "track_number": 1, "audio_url": "https://cdn.test/retry.mp3"}}})
	if err != nil {
		t.Fatalf("encode commit request: %v", err)
	}
	if err := db.Model(&model.AlbumImportSession{}).Where("id = ?", session.ID).Update("payload_json", string(payload)).Error; err != nil {
		t.Fatalf("save commit request: %v", err)
	}

	previousHook := albumImportCreateAlbumHook
	albumImportCreateAlbumHook = func(_ *gorm.DB, _ *model.Album) error {
		return errors.New("temporary database failure")
	}
	t.Cleanup(func() { albumImportCreateAlbumHook = previousHook })

	worker := NewImportWorker(db, nil, "worker").WithCompletionFinalizer(func(_ context.Context, importID uuid.UUID) error {
		return svc.FinalizeSubmittedAlbumImport(importID)
	})
	finalized, err := worker.FinalizeSubmittedReady(context.Background())
	if err == nil || finalized != 0 {
		t.Fatalf("first finalization should fail: finalized=%d err=%v", finalized, err)
	}

	albumImportCreateAlbumHook = previousHook
	finalized, err = worker.FinalizeSubmittedReady(context.Background())
	if err != nil || finalized != 1 {
		t.Fatalf("retry finalization: finalized=%d err=%v", finalized, err)
	}
	var stored model.AlbumImportSession
	if err := db.First(&stored, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("load finalized session: %v", err)
	}
	if stored.Status != AlbumImportStatusCommitted || stored.TargetAlbumID == nil {
		t.Fatalf("ready import was not committed on retry: %#v", stored)
	}
}

func TestImportWorkerClaimRecoversExpiredLease(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	expired := time.Now().UTC().Add(-time.Hour)
	if err := db.Model(&model.AlbumImportJob{}).Where("id = ?", job.ID).Updates(map[string]any{"status": AlbumImportJobStatusRunning, "locked_by": "crashed", "locked_at": expired, "heartbeat_at": expired, "attempts": 1}).Error; err != nil {
		t.Fatal(err)
	}
	w := NewImportWorker(db, nil, "replacement")
	w.leaseTimeout = time.Minute
	claimed, ok, err := w.Claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim recovered lease: ok=%v err=%v", ok, err)
	}
	if claimed.ID != job.ID || claimed.Attempts != 2 || claimed.LockedBy != "replacement" {
		t.Fatalf("unexpected reclaimed job: %#v", claimed)
	}
}

func TestImportWorkerLeaseRecoveryFailsAtAttemptLimit(t *testing.T) {
	db := newImportWorkerTestDB(t)
	job := createImportWorkerJob(t, db, AlbumImportStatusQueued)
	expired := time.Now().UTC().Add(-time.Hour)
	if err := db.Model(&model.AlbumImportJob{}).Where("id = ?", job.ID).Updates(map[string]any{"status": AlbumImportJobStatusRunning, "locked_by": "crashed", "locked_at": expired, "heartbeat_at": expired, "attempts": 2, "max_attempts": 2}).Error; err != nil {
		t.Fatal(err)
	}
	w := NewImportWorker(db, nil, "replacement")
	w.leaseTimeout = time.Minute
	_, ok, err := w.Claim(context.Background())
	if err != nil || ok {
		t.Fatalf("limited lease claim: ok=%v err=%v", ok, err)
	}
	var stored model.AlbumImportJob
	var session model.AlbumImportSession
	_ = db.First(&stored, "id = ?", job.ID).Error
	_ = db.First(&session, "id = ?", job.ImportID).Error
	if stored.Status != AlbumImportJobStatusFailed || stored.LockedBy != "" || session.Status != AlbumImportStatusNeedsAttention {
		t.Fatalf("expired lease not failed: job=%#v session=%#v", stored, session)
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
	if err := db.First(&after, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.ExpiresAt != nil {
		t.Fatalf("cleaned session must remain as history without another expiration: %#v", after.ExpiresAt)
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

func TestImportWorkerCleanupCommittedDeletesSourcesAndPlaybackAfterSuccess(t *testing.T) {
	db := newImportWorkerTestDB(t)
	now := time.Now().UTC()
	session := model.AlbumImportSession{Status: AlbumImportStatusCommitted, CommittedAt: &now, PayloadJSON: `{}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	file := model.AlbumImportFile{
		ImportID: session.ID, FileName: "album.flac", SourceKey: "source/album.flac", PlaybackKey: "playback/album.mp3",
		UploadStatus: AlbumImportFileUploadStatusUploaded, ProcessingStatus: "completed", CleanupJSON: "[]",
	}
	if err := db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	store := &importWorkerStore{}
	cleaned, err := NewImportWorker(db, store, "worker").CleanupCommitted(context.Background())
	if err != nil || !cleaned {
		t.Fatalf("cleanup committed: cleaned=%v err=%v", cleaned, err)
	}
	if !containsString(store.deleted, file.SourceKey) || !containsString(store.deleted, file.PlaybackKey) {
		t.Fatalf("temporary objects were not deleted: %v", store.deleted)
	}
	var after model.AlbumImportFile
	if err := db.First(&after, "id = ?", file.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.SourceKey != "" || after.PlaybackKey != "" {
		t.Fatalf("temporary keys were retained: %#v", after)
	}
}

func TestImportWorkerCleanupSkipsSessionCommittedAfterScan(t *testing.T) {
	db := newImportWorkerTestDB(t)
	expires := time.Now().UTC().Add(-time.Minute)
	session := model.AlbumImportSession{Status: AlbumImportStatusCanceled, ExpiresAt: &expires, PayloadJSON: `{"cleanup_targets":[{"action":"delete","key":"must-keep"}]}`}
	if err := db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	store := &importWorkerStore{}
	w := NewImportWorker(db, store, "worker")
	w.beforeCleanupSession = func(id uuid.UUID) {
		committed := time.Now().UTC()
		_ = db.Model(&model.AlbumImportSession{}).Where("id = ?", id).Updates(map[string]any{"committed_at": committed, "status": AlbumImportStatusCommitted}).Error
	}
	if err := w.CleanupExpired(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("committed source was deleted: %v", store.deleted)
	}
	var retained model.AlbumImportSession
	if err := db.First(&retained, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if retained.CommittedAt == nil {
		t.Fatal("session commit was lost")
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

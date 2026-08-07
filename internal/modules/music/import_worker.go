package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	importWorkerRetryBase = time.Minute
	importWorkerRetryMax  = time.Hour
	importWorkerRetention = 7 * 24 * time.Hour
)

// MusicImportObjectStore is the narrow storage contract required by the worker.
type MusicImportObjectStore interface {
	DeleteObject(key string) error
	AbortMultipartUpload(key, uploadID string) error
}

// ImportProcessor owns media extraction and analysis. It is intentionally separate
// from queue mechanics so the worker can be deployed before media tooling exists.
type ImportProcessor interface {
	Process(context.Context, model.AlbumImportJob, func() error) error
}

type ImportWorker struct {
	db                   *gorm.DB
	store                MusicImportObjectStore
	workerID             string
	now                  func() time.Time
	heartbeatInterval    time.Duration
	leaseTimeout         time.Duration
	beforeCleanupSession func(uuid.UUID)
	completionFinalizer  func(context.Context, uuid.UUID) error
}

func NewImportWorker(db *gorm.DB, store MusicImportObjectStore, workerID string) *ImportWorker {
	return &ImportWorker{db: db, store: store, workerID: strings.TrimSpace(workerID), now: func() time.Time { return time.Now().UTC() }, heartbeatInterval: 30 * time.Second, leaseTimeout: 5 * time.Minute}
}

func (w *ImportWorker) WithCompletionFinalizer(finalizer func(context.Context, uuid.UUID) error) *ImportWorker {
	w.completionFinalizer = finalizer
	return w
}

func (w *ImportWorker) Claim(ctx context.Context) (model.AlbumImportJob, bool, error) {
	if w.db == nil {
		return model.AlbumImportJob{}, false, errors.New("music import worker database is required")
	}
	if w.workerID == "" {
		return model.AlbumImportJob{}, false, errors.New("music import worker id is required")
	}
	now := w.now()
	if err := w.recoverExpiredLeases(ctx, now); err != nil {
		return model.AlbumImportJob{}, false, err
	}
	var candidates []model.AlbumImportJob
	err := w.db.WithContext(ctx).Where("status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", AlbumImportJobStatusQueued, now).Order("created_at ASC").Limit(16).Find(&candidates).Error
	if err != nil {
		return model.AlbumImportJob{}, false, err
	}
	for _, candidate := range candidates {
		// The correlated session clause keeps canceled and terminal imports out of the queue.
		result := w.db.WithContext(ctx).Model(&model.AlbumImportJob{}).Where("id = ? AND status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?) AND EXISTS (SELECT 1 FROM music_album_import_sessions s WHERE s.id = music_album_import_jobs.import_id AND s.status IN ?)", candidate.ID, AlbumImportJobStatusQueued, now, []string{AlbumImportStatusQueued, AlbumImportStatusExtracting, AlbumImportStatusAnalyzing, AlbumImportStatusTranscoding}).Updates(map[string]any{
			"status": AlbumImportJobStatusRunning, "stage": AlbumImportStageExtracting, "locked_by": w.workerID, "locked_at": now, "heartbeat_at": now, "started_at": now, "attempts": gorm.Expr("attempts + 1"), "next_attempt_at": nil,
		})
		if result.Error != nil {
			return model.AlbumImportJob{}, false, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}
		var claimed model.AlbumImportJob
		if err := w.db.WithContext(ctx).First(&claimed, "id = ?", candidate.ID).Error; err != nil {
			return model.AlbumImportJob{}, false, err
		}
		return claimed, true, nil
	}
	return model.AlbumImportJob{}, false, nil
}

func (w *ImportWorker) recoverExpiredLeases(ctx context.Context, now time.Time) error {
	timeout := w.leaseTimeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cutoff := now.Add(-timeout)
	var jobs []model.AlbumImportJob
	if err := w.db.WithContext(ctx).Where("status = ? AND ((heartbeat_at IS NOT NULL AND heartbeat_at <= ?) OR (heartbeat_at IS NULL AND locked_at <= ?))", AlbumImportJobStatusRunning, cutoff, cutoff).Find(&jobs).Error; err != nil {
		return err
	}
	for _, candidate := range jobs {
		if err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var job model.AlbumImportJob
			if err := tx.First(&job, "id = ?", candidate.ID).Error; err != nil {
				return err
			}
			if job.Status != AlbumImportJobStatusRunning || (!leaseExpired(job, cutoff)) {
				return nil
			}
			var session model.AlbumImportSession
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status IN ?", job.ImportID, activeImportSessionStatuses).First(&session).Error; err != nil {
				return nil
			}
			if job.Attempts >= job.MaxAttempts {
				expires := now.Add(importWorkerRetention)
				result := tx.Model(&model.AlbumImportJob{}).Where("id = ? AND status = ? AND ((heartbeat_at IS NOT NULL AND heartbeat_at <= ?) OR (heartbeat_at IS NULL AND locked_at <= ?))", job.ID, AlbumImportJobStatusRunning, cutoff, cutoff).Updates(map[string]any{"status": AlbumImportJobStatusFailed, "stage": AlbumImportStageFailed, "last_error": "worker lease expired", "locked_by": "", "locked_at": nil, "heartbeat_at": nil, "finished_at": now})
				if result.Error != nil || result.RowsAffected == 0 {
					return result.Error
				}
				return tx.Model(&model.AlbumImportSession{}).Where("id = ? AND status IN ?", session.ID, activeImportSessionStatuses).Updates(map[string]any{"status": AlbumImportStatusNeedsAttention, "stage": AlbumImportStageFailed, "error_message": "worker lease expired", "expires_at": expires}).Error
			}
			result := tx.Model(&model.AlbumImportJob{}).Where("id = ? AND status = ? AND ((heartbeat_at IS NOT NULL AND heartbeat_at <= ?) OR (heartbeat_at IS NULL AND locked_at <= ?))", job.ID, AlbumImportJobStatusRunning, cutoff, cutoff).Updates(map[string]any{"status": AlbumImportJobStatusQueued, "stage": AlbumImportStageQueued, "locked_by": "", "locked_at": nil, "heartbeat_at": nil, "next_attempt_at": now})
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func leaseExpired(job model.AlbumImportJob, cutoff time.Time) bool {
	if job.HeartbeatAt != nil {
		return !job.HeartbeatAt.After(cutoff)
	}
	return job.LockedAt != nil && !job.LockedAt.After(cutoff)
}

func (w *ImportWorker) Heartbeat(ctx context.Context, jobID uuid.UUID) error {
	now := w.now()
	result := w.db.WithContext(ctx).Model(&model.AlbumImportJob{}).Where("id = ? AND status = ? AND locked_by = ?", jobID, AlbumImportJobStatusRunning, w.workerID).Update("heartbeat_at", now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("music import job is not held by this worker")
	}
	return nil
}

func (w *ImportWorker) Complete(ctx context.Context, jobID uuid.UUID) error {
	now := w.now()
	return w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.AlbumImportJob
		if err := tx.First(&job, "id = ?", jobID).Error; err != nil {
			return err
		}
		var session model.AlbumImportSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status IN ?", job.ImportID, activeImportSessionStatuses).First(&session).Error; err != nil {
			return errors.New("music import job is not held by this worker")
		}
		result := tx.Model(&model.AlbumImportJob{}).Where("id = ? AND status = ? AND locked_by = ?", job.ID, AlbumImportJobStatusRunning, w.workerID).Updates(map[string]any{"status": "completed", "stage": AlbumImportStageCompleted, "finished_at": now, "locked_by": "", "locked_at": nil})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("music import job is not held by this worker")
		}
		result = tx.Model(&model.AlbumImportSession{}).Where("id = ? AND status IN ?", session.ID, activeImportSessionStatuses).Updates(map[string]any{"status": AlbumImportStatusReady, "stage": AlbumImportStageReady})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("music import session is no longer active")
		}
		return nil
	})
}

func (w *ImportWorker) Retry(ctx context.Context, jobID uuid.UUID, cause error) error {
	now := w.now()
	message := "processor failed"
	if cause != nil {
		message = cause.Error()
	}
	return w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job model.AlbumImportJob
		if err := tx.First(&job, "id = ?", jobID).Error; err != nil {
			return err
		}
		var session model.AlbumImportSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND status IN ?", job.ImportID, activeImportSessionStatuses).First(&session).Error; err != nil {
			return errors.New("music import job is not held by this worker")
		}
		if job.Attempts >= job.MaxAttempts {
			expires := now.Add(importWorkerRetention)
			result := tx.Model(&model.AlbumImportJob{}).Where("id = ? AND status = ? AND locked_by = ?", job.ID, AlbumImportJobStatusRunning, w.workerID).Updates(map[string]any{"status": AlbumImportJobStatusFailed, "stage": AlbumImportStageFailed, "last_error": message, "finished_at": now, "locked_by": "", "locked_at": nil})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return errors.New("music import job is not held by this worker")
			}
			result = tx.Model(&model.AlbumImportSession{}).Where("id = ? AND status IN ?", session.ID, activeImportSessionStatuses).Updates(map[string]any{"status": AlbumImportStatusNeedsAttention, "stage": AlbumImportStageFailed, "error_message": message, "expires_at": expires})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return errors.New("music import session is no longer active")
			}
			return nil
		}
		next := now.Add(importRetryDelay(job.Attempts))
		result := tx.Model(&model.AlbumImportJob{}).Where("id = ? AND status = ? AND locked_by = ?", job.ID, AlbumImportJobStatusRunning, w.workerID).Updates(map[string]any{"status": AlbumImportJobStatusQueued, "stage": AlbumImportStageQueued, "last_error": message, "locked_by": "", "locked_at": nil, "heartbeat_at": nil, "next_attempt_at": next})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("music import job is not held by this worker")
		}
		return nil
	})
}

var activeImportSessionStatuses = []string{AlbumImportStatusQueued, AlbumImportStatusExtracting, AlbumImportStatusAnalyzing, AlbumImportStatusTranscoding}

func importRetryDelay(attempt int) time.Duration {
	delay := importWorkerRetryBase
	for i := 1; i < attempt && delay < importWorkerRetryMax; i++ {
		delay *= 2
	}
	if delay > importWorkerRetryMax {
		return importWorkerRetryMax
	}
	return delay
}

func (w *ImportWorker) RunOnce(ctx context.Context, processor ImportProcessor) (bool, error) {
	if processed, err := RunSongAudioReplacementOnce(ctx, w.db, w.workerID); processed || err != nil {
		return processed, err
	}
	if err := w.CleanupExpired(ctx); err != nil {
		return false, err
	}
	if processor == nil {
		return false, nil
	}
	job, ok, err := w.Claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	if err := w.processWithHeartbeat(ctx, processor, job); err != nil {
		return true, w.Retry(ctx, job.ID, err)
	}
	if err := w.Complete(ctx, job.ID); err != nil {
		return true, err
	}
	if w.completionFinalizer != nil {
		if err := w.completionFinalizer(ctx, job.ImportID); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (w *ImportWorker) processWithHeartbeat(ctx context.Context, processor ImportProcessor, job model.AlbumImportJob) error {
	interval := w.heartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	done := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = w.Heartbeat(ctx, job.ID)
			}
		}
	}()
	err := processor.Process(ctx, job, func() error { return w.Heartbeat(ctx, job.ID) })
	close(done)
	<-heartbeatDone
	return err
}

func (w *ImportWorker) CleanupExpired(ctx context.Context) error {
	if w.db == nil {
		return errors.New("music import worker database is required")
	}
	if w.store == nil {
		return nil
	}
	now := w.now()
	var sessions []model.AlbumImportSession
	if err := w.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at <= ? AND committed_at IS NULL", now).Preload("Files").Find(&sessions).Error; err != nil {
		return err
	}
	for _, session := range sessions {
		if w.beforeCleanupSession != nil {
			w.beforeCleanupSession(session.ID)
		}
		var cleanupErr error
		if err := w.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var locked model.AlbumImportSession
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("Files").Where("id = ? AND expires_at <= ? AND committed_at IS NULL", session.ID, now).First(&locked).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if err := w.cleanupSession(ctx, tx, &locked); err != nil {
				cleanupErr = err
				return nil
			}
			return tx.Model(&locked).Update("expires_at", nil).Error
		}); err != nil {
			return fmt.Errorf("clean up import %s: %w", session.ID, err)
		}
		if cleanupErr != nil {
			return fmt.Errorf("clean up import %s: %w", session.ID, cleanupErr)
		}
	}
	return nil
}

func (w *ImportWorker) cleanupSession(ctx context.Context, db *gorm.DB, session *model.AlbumImportSession) error {
	payload, targets, err := cleanupPayload(session.PayloadJSON)
	if err != nil {
		return err
	}
	for len(targets) > 0 {
		target := targets[0]
		if err := cleanupTarget(w.store, target); err != nil {
			return err
		}
		targets = targets[1:]
		payload["cleanup_targets"] = targets
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if err := db.WithContext(ctx).Model(session).Update("payload_json", string(encoded)).Error; err != nil {
			return err
		}
		session.PayloadJSON = string(encoded)
	}
	for i := range session.Files {
		file := &session.Files[i]
		var targets []importCleanupTarget
		if strings.TrimSpace(file.CleanupJSON) != "" {
			if err := json.Unmarshal([]byte(file.CleanupJSON), &targets); err != nil {
				return err
			}
		}
		for len(targets) > 0 {
			completed := targets[0]
			if err := cleanupTarget(w.store, completed); err != nil {
				return err
			}
			targets = targets[1:]
			encoded, err := json.Marshal(targets)
			if err != nil {
				return err
			}
			if err := db.WithContext(ctx).Model(file).Update("cleanup_json", string(encoded)).Error; err != nil {
				return err
			}
			file.CleanupJSON = string(encoded)
			if completed.Key == file.SourceKey && (completed.Action == "delete" || completed.Action == "abort") {
				if err := db.WithContext(ctx).Model(file).Updates(map[string]any{"source_key": "", "upload_id": ""}).Error; err != nil {
					return err
				}
				file.SourceKey, file.UploadID = "", ""
			}
		}
		if file.SourceKey != "" && file.UploadStatus == AlbumImportFileUploadStatusUploaded {
			if err := cleanupTarget(w.store, importCleanupTarget{Action: "delete", Key: file.SourceKey}); err != nil {
				return err
			}
			if err := db.WithContext(ctx).Model(file).Update("source_key", "").Error; err != nil {
				return err
			}
			file.SourceKey = ""
		} else if file.SourceKey != "" && file.UploadID != "" {
			if err := cleanupTarget(w.store, importCleanupTarget{Action: "abort", Key: file.SourceKey, UploadID: file.UploadID}); err != nil {
				return err
			}
			if err := db.WithContext(ctx).Model(file).Updates(map[string]any{"source_key": "", "upload_id": ""}).Error; err != nil {
				return err
			}
			file.SourceKey, file.UploadID = "", ""
		}
	}
	return nil
}

func cleanupPayload(raw string) (map[string]any, []importCleanupTarget, error) {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, nil, err
	}
	targets := []importCleanupTarget{}
	if value, ok := payload["cleanup_targets"]; ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(encoded, &targets); err != nil {
			return nil, nil, err
		}
	}
	return payload, targets, nil
}

type importCleanupTarget struct {
	Action   string `json:"action"`
	Key      string `json:"key"`
	UploadID string `json:"upload_id"`
}

func cleanupTargets(session model.AlbumImportSession) ([]importCleanupTarget, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(session.PayloadJSON), &payload); err != nil {
		return nil, err
	}
	var targets []importCleanupTarget
	if raw := payload["cleanup_targets"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &targets); err != nil {
			return nil, err
		}
	}
	for _, file := range session.Files {
		if strings.TrimSpace(file.CleanupJSON) != "" {
			var fileTargets []importCleanupTarget
			if err := json.Unmarshal([]byte(file.CleanupJSON), &fileTargets); err != nil {
				return nil, err
			}
			targets = append(targets, fileTargets...)
		}
		if file.SourceKey != "" && file.UploadStatus == AlbumImportFileUploadStatusUploaded {
			targets = append(targets, importCleanupTarget{Action: "delete", Key: file.SourceKey})
		} else if file.SourceKey != "" && file.UploadID != "" {
			targets = append(targets, importCleanupTarget{Action: "abort", Key: file.SourceKey, UploadID: file.UploadID})
		}
	}
	return uniqueCleanupTargets(targets), nil
}

func uniqueCleanupTargets(targets []importCleanupTarget) []importCleanupTarget {
	seen := make(map[string]struct{}, len(targets))
	unique := make([]importCleanupTarget, 0, len(targets))
	for _, target := range targets {
		key := target.Action + "\x00" + target.Key + "\x00" + target.UploadID
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, target)
	}
	return unique
}

func cleanupTarget(store MusicImportObjectStore, target importCleanupTarget) error {
	var err error
	switch target.Action {
	case "delete":
		err = store.DeleteObject(target.Key)
	case "abort":
		err = store.AbortMultipartUpload(target.Key, target.UploadID)
	default:
		return fmt.Errorf("unknown cleanup action %q", target.Action)
	}
	if isMissingCleanupTarget(err) {
		return nil
	}
	return err
}

func isMissingCleanupTarget(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "nosuchkey") || strings.Contains(message, "nosuchupload") || strings.Contains(message, "not found") || strings.Contains(message, "status code: 404")
}

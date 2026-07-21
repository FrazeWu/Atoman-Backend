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
	Process(context.Context, model.AlbumImportJob) error
}

type ImportWorker struct {
	db       *gorm.DB
	store    MusicImportObjectStore
	workerID string
	now      func() time.Time
}

func NewImportWorker(db *gorm.DB, store MusicImportObjectStore, workerID string) *ImportWorker {
	return &ImportWorker{db: db, store: store, workerID: strings.TrimSpace(workerID), now: func() time.Time { return time.Now().UTC() }}
}

func (w *ImportWorker) Claim(ctx context.Context) (model.AlbumImportJob, bool, error) {
	if w.db == nil {
		return model.AlbumImportJob{}, false, errors.New("music import worker database is required")
	}
	if w.workerID == "" {
		return model.AlbumImportJob{}, false, errors.New("music import worker id is required")
	}
	now := w.now()
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
		if job.Status != AlbumImportJobStatusRunning || job.LockedBy != w.workerID {
			return errors.New("music import job is not held by this worker")
		}
		if err := tx.Model(&job).Updates(map[string]any{"status": "completed", "stage": AlbumImportStageCompleted, "finished_at": now, "locked_by": "", "locked_at": nil}).Error; err != nil {
			return err
		}
		return tx.Model(&model.AlbumImportSession{}).Where("id = ?", job.ImportID).Updates(map[string]any{"status": AlbumImportStatusReady, "stage": AlbumImportStageReady}).Error
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
		if job.Status != AlbumImportJobStatusRunning || job.LockedBy != w.workerID {
			return errors.New("music import job is not held by this worker")
		}
		if job.Attempts >= job.MaxAttempts {
			expires := now.Add(importWorkerRetention)
			if err := tx.Model(&job).Updates(map[string]any{"status": AlbumImportJobStatusFailed, "stage": AlbumImportStageFailed, "last_error": message, "finished_at": now, "locked_by": "", "locked_at": nil}).Error; err != nil {
				return err
			}
			return tx.Model(&model.AlbumImportSession{}).Where("id = ?", job.ImportID).Updates(map[string]any{"status": AlbumImportStatusNeedsAttention, "stage": AlbumImportStageFailed, "error_message": message, "expires_at": expires}).Error
		}
		next := now.Add(importRetryDelay(job.Attempts))
		return tx.Model(&job).Updates(map[string]any{"status": AlbumImportJobStatusQueued, "stage": AlbumImportStageQueued, "last_error": message, "locked_by": "", "locked_at": nil, "heartbeat_at": nil, "next_attempt_at": next}).Error
	})
}

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
	if err := w.CleanupExpired(ctx); err != nil {
		return false, err
	}
	job, ok, err := w.Claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	if processor == nil {
		// The command may run before media support ships: put the job back untouched.
		return true, w.db.WithContext(ctx).Model(&model.AlbumImportJob{}).Where("id = ? AND locked_by = ?", job.ID, w.workerID).Updates(map[string]any{"status": AlbumImportJobStatusQueued, "stage": AlbumImportStageQueued, "attempts": gorm.Expr("attempts - 1"), "locked_by": "", "locked_at": nil, "heartbeat_at": nil}).Error
	}
	if err := processor.Process(ctx, job); err != nil {
		return true, w.Retry(ctx, job.ID, err)
	}
	return true, w.Complete(ctx, job.ID)
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
		targets, err := cleanupTargets(session)
		if err != nil {
			return fmt.Errorf("read cleanup targets for %s: %w", session.ID, err)
		}
		for _, target := range targets {
			if err := cleanupTarget(w.store, target); err != nil {
				return fmt.Errorf("clean up import %s: %w", session.ID, err)
			}
		}
		if err := w.db.WithContext(ctx).Delete(&session).Error; err != nil {
			return err
		}
	}
	return nil
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
	switch target.Action {
	case "delete":
		return store.DeleteObject(target.Key)
	case "abort":
		return store.AbortMultipartUpload(target.Key, target.UploadID)
	default:
		return fmt.Errorf("unknown cleanup action %q", target.Action)
	}
}

package music

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"atoman/internal/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const metadataMatchLeaseDuration = 2 * time.Minute

func StartMetadataMatchWorker(ctx context.Context, service *Service, interval time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for ctx.Err() == nil {
			if _, err := service.RunMetadataMatchOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("music metadata match worker failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

func (s *Service) RunMetadataMatchOnce(ctx context.Context) (bool, error) {
	var job model.MusicMetadataMatchJob
	now := time.Now().UTC()
	token := uuid.NewString()
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		claim := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? OR (status = ? AND locked_at < ?)", "queued", "running", now.Add(-metadataMatchLeaseDuration)).
			Order("created_at ASC").Limit(1).Find(&job)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&job).Updates(map[string]any{
			"status": "running", "locked_at": now, "lease_token": token,
		}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var session model.AlbumImportSession
	if err := s.db.WithContext(ctx).First(&session, "id = ?", job.ImportID).Error; err != nil {
		// A transient database failure is retried when the claim expires.
		return true, err
	}
	values, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return true, errors.Join(err, s.setMetadataMatchJobStatus(ctx, job.ID, token, "failed"))
	}
	if session.Status == AlbumImportStatusCanceled || session.Status == AlbumImportStatusCommitted ||
		int64Value(values["metadata_match_generation"]) != job.Generation {
		return true, s.setMetadataMatchJobStatus(ctx, job.ID, token, "canceled")
	}
	var input AlbumImportMetadataPreviewInput
	if err := json.Unmarshal([]byte(job.InputJSON), &input); err != nil {
		return true, errors.Join(err, s.setMetadataMatchJobStatus(ctx, job.ID, token, "failed"))
	}

	// Both API cancellation and another worker's stale-lease reclamation
	// invalidate the running lookup, including across process boundaries.
	lookupCtx, cancelLookup := context.WithCancel(ctx)
	defer cancelLookup()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-lookupCtx.Done():
				return
			case <-ticker.C:
				result := s.db.WithContext(lookupCtx).Model(&model.MusicMetadataMatchJob{}).
					Where("id = ? AND status = ? AND lease_token = ?", job.ID, "running", token).
					Update("locked_at", time.Now().UTC())
				if result.Error != nil || result.RowsAffected != 1 {
					cancelLookup()
					return
				}
				var current struct {
					Status     string
					Generation int64
				}
				if err := s.db.WithContext(lookupCtx).Model(&model.AlbumImportSession{}).
					Select("status, (payload_json::jsonb->>'metadata_match_generation')::bigint AS generation").
					Where("id = ?", job.ImportID).Take(&current).Error; err != nil {
					continue
				}
				if current.Status == AlbumImportStatusCanceled || current.Status == AlbumImportStatusCommitted ||
					current.Generation != job.Generation {
					cancelLookup()
					return
				}
			}
		}
	}()
	preview, lookupErr := s.PreviewAlbumImportMetadata(lookupCtx, input)
	cancelLookup()
	<-monitorDone
	if lookupErr != nil {
		preview.MetadataError = lookupErr.Error()
	}
	result := metadataResultFromPreview(preview)
	// Finish with a fresh short-lived context after request cancellation,
	// but only the current lease owner may publish the result.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return true, s.finishMetadataMatchJob(persistCtx, &session, job, token, result)
}

func (s *Service) setMetadataMatchJobStatus(ctx context.Context, jobID uuid.UUID, token, status string) error {
	return s.db.WithContext(ctx).Model(&model.MusicMetadataMatchJob{}).
		Where("id = ? AND status = ? AND lease_token = ?", jobID, "running", token).
		Update("status", status).Error
}

func (s *Service) finishMetadataMatchJob(ctx context.Context, session *model.AlbumImportSession, job model.MusicMetadataMatchJob, token string, result AlbumImportMetadataResult) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current model.MusicMetadataMatchJob
		claimed := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND lease_token = ?", job.ID, "running", token).First(&current)
		if errors.Is(claimed.Error, gorm.ErrRecordNotFound) {
			return nil // A newer worker reclaimed the lease; never publish old results.
		}
		if claimed.Error != nil {
			return claimed.Error
		}
		written, err := persistAlbumImportMetadataMatchGenerationTx(tx, session.ID, result, job.Generation)
		if err != nil {
			return err
		}
		status := "done"
		if !written {
			status = "canceled"
		}
		return tx.Model(&model.MusicMetadataMatchJob{}).
			Where("id = ? AND status = ? AND lease_token = ?", job.ID, "running", token).
			Update("status", status).Error
	})
}

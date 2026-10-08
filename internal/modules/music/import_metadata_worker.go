package music

import (
	"atoman/internal/model"
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"log"
	"time"
)

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
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		claim := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status = ? OR (status = ? AND locked_at < ?)", "queued", "running", now.Add(-2*time.Minute)).
			Order("created_at ASC").Limit(1).Find(&job)
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&job).Updates(map[string]any{"status": "running", "locked_at": now}).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var session model.AlbumImportSession
	if err := s.db.WithContext(ctx).First(&session, "id = ?", job.ImportID).Error; err != nil {
		return true, err
	}
	values, err := readAlbumImportPayloadMap(session.PayloadJSON)
	if err != nil {
		return true, err
	}
	if session.Status == AlbumImportStatusCanceled || session.Status == AlbumImportStatusCommitted || int64Value(values["metadata_match_generation"]) != job.Generation {
		return true, s.db.WithContext(ctx).Model(&job).Update("status", "canceled").Error
	}
	var input AlbumImportMetadataPreviewInput
	if err := json.Unmarshal([]byte(job.InputJSON), &input); err != nil {
		return true, err
	}
	// API 和 worker 可以处于不同进程，定期核对会话以取消已撤销或被重试替代的请求。
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
				var current struct {
					Status     string
					Generation int64
				}
				if err := s.db.WithContext(lookupCtx).Model(&model.AlbumImportSession{}).
					Select("status, (payload_json::jsonb->>'metadata_match_generation')::bigint AS generation").
					Where("id = ?", job.ImportID).Take(&current).Error; err != nil {
					continue
				}
				if current.Status == AlbumImportStatusCanceled || current.Status == AlbumImportStatusCommitted || current.Generation != job.Generation {
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
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.persistAlbumImportMetadataMatchGeneration(persistCtx, &session, result, job.Generation); err != nil {
		return true, err
	}
	return true, s.db.WithContext(persistCtx).Model(&job).Update("status", "done").Error
}

package service

import (
	"encoding/json"
	"time"

	"atoman/internal/model"

	"gorm.io/gorm"
)

type VideoPreviewThumbnail struct {
	TimeSec int    `json:"time_sec"`
	URL     string `json:"url"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}

type VideoPreviewGenerator interface {
	Generate(video model.Video) ([]VideoPreviewThumbnail, error)
}

type VideoPreviewWorker struct {
	DB          *gorm.DB
	Generator   VideoPreviewGenerator
	MaxAttempts int
	beforeClaim func(job model.VideoProcessingJob)
}

func (w VideoPreviewWorker) ProcessNext() (bool, error) {
	maxAttempts := w.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	job, err := w.claimNext()
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	var video model.Video
	if err := w.DB.First(&video, "id = ?", job.VideoID).Error; err != nil {
		return true, w.failJob(job, maxAttempts, err)
	}

	thumbnails, err := w.Generator.Generate(video)
	if err != nil {
		return true, w.failJob(job, maxAttempts, err)
	}

	raw, err := json.Marshal(thumbnails)
	if err != nil {
		return true, w.failJob(job, maxAttempts, err)
	}

	finishedAt := time.Now()
	return true, w.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&video).Updates(map[string]interface{}{
			"preview_thumbnails": json.RawMessage(raw),
			"processing_status":  "ready",
			"processing_error":   "",
		}).Error; err != nil {
			return err
		}

		return tx.Model(&job).Updates(map[string]interface{}{
			"status":      "ready",
			"finished_at": &finishedAt,
		}).Error
	})
}

func (w VideoPreviewWorker) claimNext() (model.VideoProcessingJob, error) {
	for {
		var job model.VideoProcessingJob
		if err := w.DB.Where("status = ? AND job_type = ?", "pending", "thumbnail_preview").
			Order("created_at ASC").
			First(&job).Error; err != nil {
			return model.VideoProcessingJob{}, err
		}
		if w.beforeClaim != nil {
			w.beforeClaim(job)
		}

		now := time.Now()
		result := w.DB.Model(&model.VideoProcessingJob{}).
			Where("id = ? AND status = ?", job.ID, "pending").
			Updates(map[string]interface{}{
				"status":     "processing",
				"locked_at":  &now,
				"started_at": &now,
			})
		if result.Error != nil {
			return model.VideoProcessingJob{}, result.Error
		}
		if result.RowsAffected == 0 {
			continue
		}

		if err := w.DB.First(&job, "id = ?", job.ID).Error; err != nil {
			return model.VideoProcessingJob{}, err
		}
		return job, nil
	}
}

func (w VideoPreviewWorker) failJob(job model.VideoProcessingJob, maxAttempts int, cause error) error {
	attempts := job.Attempts + 1
	status := "pending"
	videoStatus := "pending"
	if attempts >= maxAttempts {
		status = "failed"
		videoStatus = "failed"
	}

	return w.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&job).Updates(map[string]interface{}{
			"status":     status,
			"attempts":   attempts,
			"last_error": cause.Error(),
		}).Error; err != nil {
			return err
		}

		return tx.Model(&model.Video{}).Where("id = ?", job.VideoID).Updates(map[string]interface{}{
			"processing_status": videoStatus,
			"processing_error":  cause.Error(),
		}).Error
	})
}

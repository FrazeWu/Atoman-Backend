package service

import (
	"encoding/json"
	"strings"
	"time"

	"atoman/internal/model"
	contentmodule "atoman/internal/modules/content"

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
		if err := EnqueueMissingVideoPreviewJobs(w.DB, 20); err != nil {
			return false, err
		}
		job, err = w.claimNext()
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
	}
	if err != nil {
		return false, err
	}

	video, err := contentmodule.LoadVideo(w.DB, contentmodule.VideoQuery(w.DB).Where("videos.video_id = ?", job.VideoID))
	if err != nil {
		return true, w.failJob(job, maxAttempts, err)
	}

	result := VideoPreviewResult{}
	if generator, ok := w.Generator.(VideoPreviewMetadataGenerator); ok {
		result, err = generator.GenerateWithMetadata(video)
	} else {
		result.Thumbnails, err = w.Generator.Generate(video)
	}
	if err != nil {
		return true, w.failJob(job, maxAttempts, err)
	}

	raw, err := json.Marshal(result.Thumbnails)
	if err != nil {
		return true, w.failJob(job, maxAttempts, err)
	}

	finishedAt := time.Now()
	return true, w.DB.Transaction(func(tx *gorm.DB) error {
		contentID, err := contentmodule.VideoContentID(tx, video.ID)
		if err != nil {
			return err
		}
		updates := map[string]interface{}{
			"preview_thumbnails": json.RawMessage(raw),
			"processing_status":  "ready",
			"processing_error":   "",
		}
		var extension model.ContentVideoExtension
		if err := tx.First(&extension, "content_id = ?", contentID).Error; err != nil {
			return err
		}
		if strings.TrimSpace(extension.ThumbnailURL) == "" && strings.TrimSpace(result.ThumbnailURL) != "" {
			updates["thumbnail_url"] = result.ThumbnailURL
			if err := tx.Model(&model.ContentEntry{}).Where("id = ?", contentID).Update("cover_url", result.ThumbnailURL).Error; err != nil {
				return err
			}
		}
		if extension.DurationSec <= 0 && result.DurationSec > 0 {
			updates["duration_sec"] = result.DurationSec
		}
		if err := tx.Model(&model.ContentVideoExtension{}).Where("content_id = ?", contentID).Updates(updates).Error; err != nil {
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

		contentID, err := contentmodule.VideoContentID(tx, job.VideoID)
		if err != nil {
			return err
		}
		return tx.Model(&model.ContentVideoExtension{}).Where("content_id = ?", contentID).Updates(map[string]interface{}{
			"processing_status": videoStatus,
			"processing_error":  cause.Error(),
		}).Error
	})
}
